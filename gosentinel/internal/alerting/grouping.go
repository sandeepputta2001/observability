// Package alerting — AlertGrouper batches related alerts within a time window
// before dispatching, reducing notification noise for correlated failures.
package alerting

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// AlertGroup is a collection of related AlertEvents that fired within the
// grouping window and share the same group key.
type AlertGroup struct {
	Key       string       // e.g. "severity=critical" or "service=order-service"
	Events    []*AlertEvent
	CreatedAt time.Time
	FlushedAt time.Time
}

// Summary returns a human-readable summary of the group.
func (g *AlertGroup) Summary() string {
	if len(g.Events) == 1 {
		return g.Events[0].Summary
	}
	names := make([]string, 0, len(g.Events))
	for _, e := range g.Events {
		names = append(names, e.RuleName)
	}
	return fmt.Sprintf("%d alerts: %s", len(g.Events), strings.Join(names, ", "))
}

// GroupKeyFunc extracts a grouping key from an alert event.
// Events with the same key are batched together.
type GroupKeyFunc func(event *AlertEvent) string

// GroupBySeverity groups alerts by severity level.
func GroupBySeverity(event *AlertEvent) string {
	return "severity=" + event.Severity
}

// GroupByService groups alerts by the originating service.
func GroupByService(event *AlertEvent) string {
	if event.Service != "" {
		return "service=" + event.Service
	}
	return "service=unknown"
}

// GroupByRule groups each alert individually (no batching — default behaviour).
func GroupByRule(event *AlertEvent) string {
	return "rule=" + event.RuleName
}

// AlertGrouper buffers incoming alerts and flushes groups after a wait window.
// This reduces notification noise when many alerts fire simultaneously.
type AlertGrouper struct {
	keyFn      GroupKeyFunc
	waitWindow time.Duration // how long to wait before flushing a group
	flushFn    func(ctx context.Context, group *AlertGroup)

	mu     sync.Mutex
	groups map[string]*pendingGroup
}

type pendingGroup struct {
	group  *AlertGroup
	timer  *time.Timer
}

// NewAlertGrouper creates an AlertGrouper.
//
//   - keyFn determines how events are grouped (e.g. GroupBySeverity).
//   - waitWindow is the time to wait for more events before flushing.
//   - flushFn is called with the completed group when the window expires.
func NewAlertGrouper(keyFn GroupKeyFunc, waitWindow time.Duration,
	flushFn func(ctx context.Context, group *AlertGroup)) *AlertGrouper {
	if keyFn == nil {
		keyFn = GroupByRule
	}
	if waitWindow <= 0 {
		waitWindow = 30 * time.Second
	}
	return &AlertGrouper{
		keyFn:      keyFn,
		waitWindow: waitWindow,
		flushFn:    flushFn,
		groups:     make(map[string]*pendingGroup),
	}
}

// Add adds an event to the appropriate group, resetting the flush timer.
func (g *AlertGrouper) Add(ctx context.Context, event *AlertEvent) {
	key := g.keyFn(event)

	g.mu.Lock()
	defer g.mu.Unlock()

	pg, exists := g.groups[key]
	if !exists {
		pg = &pendingGroup{
			group: &AlertGroup{
				Key:       key,
				CreatedAt: time.Now(),
			},
		}
		g.groups[key] = pg
	}
	pg.group.Events = append(pg.group.Events, event)

	// Reset the flush timer on each new event.
	if pg.timer != nil {
		pg.timer.Stop()
	}
	pg.timer = time.AfterFunc(g.waitWindow, func() {
		g.flush(ctx, key)
	})

	slog.DebugContext(ctx, "alert grouped",
		"key", key, "count", len(pg.group.Events))
}

// flush sends the group to flushFn and removes it from the pending map.
func (g *AlertGrouper) flush(ctx context.Context, key string) {
	g.mu.Lock()
	pg, ok := g.groups[key]
	if !ok {
		g.mu.Unlock()
		return
	}
	delete(g.groups, key)
	pg.group.FlushedAt = time.Now()
	g.mu.Unlock()

	slog.InfoContext(ctx, "flushing alert group",
		"key", key, "alerts", len(pg.group.Events))

	if g.flushFn != nil {
		g.flushFn(ctx, pg.group)
	}
}

// FlushAll immediately flushes all pending groups (call on shutdown).
func (g *AlertGrouper) FlushAll(ctx context.Context) {
	g.mu.Lock()
	keys := make([]string, 0, len(g.groups))
	for k, pg := range g.groups {
		if pg.timer != nil {
			pg.timer.Stop()
		}
		keys = append(keys, k)
	}
	g.mu.Unlock()

	for _, k := range keys {
		g.flush(ctx, k)
	}
}

// GroupedNotifier wraps an AlertGrouper and a downstream Notifier.
// It batches events through the grouper and sends a single representative
// notification per group (using the first event's fields + updated summary).
type GroupedNotifier struct {
	grouper  *AlertGrouper
	notifier Notifier
}

// NewGroupedNotifier creates a GroupedNotifier that batches events for
// waitWindow before forwarding a single notification to notifier.
func NewGroupedNotifier(notifier Notifier, keyFn GroupKeyFunc, waitWindow time.Duration) *GroupedNotifier {
	gn := &GroupedNotifier{notifier: notifier}
	gn.grouper = NewAlertGrouper(keyFn, waitWindow, gn.onFlush)
	return gn
}

// Notify adds the event to the grouper buffer.
func (gn *GroupedNotifier) Notify(ctx context.Context, event *AlertEvent) error {
	gn.grouper.Add(ctx, event)
	return nil
}

// onFlush is called by the grouper when a window expires.
func (gn *GroupedNotifier) onFlush(ctx context.Context, group *AlertGroup) {
	if len(group.Events) == 0 {
		return
	}
	// Use the first event as the representative, update summary.
	representative := *group.Events[0]
	representative.Summary = group.Summary()
	if err := gn.notifier.Notify(ctx, &representative); err != nil {
		slog.ErrorContext(ctx, "grouped notifier flush failed",
			"key", group.Key, "error", err)
	}
}

// FlushAll flushes all pending groups immediately.
func (gn *GroupedNotifier) FlushAll(ctx context.Context) {
	gn.grouper.FlushAll(ctx)
}
