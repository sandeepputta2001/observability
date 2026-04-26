// Package alerting — AlertManager orchestrates multi-channel notification
// delivery with deduplication, retry with exponential back-off, per-channel
// routing, escalation policies, alert grouping, and a full notification audit log.
package alerting

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// RetryConfig controls retry behaviour for a notifier.
type RetryConfig struct {
	MaxAttempts int           // total attempts (1 = no retry)
	InitialWait time.Duration // wait before second attempt
	MaxWait     time.Duration // cap on exponential back-off
}

// DefaultRetryConfig is used when no explicit config is provided.
var DefaultRetryConfig = RetryConfig{
	MaxAttempts: 3,
	InitialWait: 2 * time.Second,
	MaxWait:     30 * time.Second,
}

// channelEntry pairs a Notifier with its retry config.
type channelEntry struct {
	name  string
	n     Notifier
	retry RetryConfig
}

// AlertManager orchestrates multi-channel notification delivery.
//
// Features:
//   - Routing: per-rule channel selection via RoutingConfig.
//   - Deduplication: identical (ruleID, state) pairs suppressed within a window.
//   - Retry with exponential back-off per channel.
//   - Concurrent fan-out: all matched channels notified in parallel.
//   - Silence: events for silenced rules are dropped before dispatch.
//   - Escalation: severity/time-based channel escalation via EscalationPolicy.
//   - History: all events recorded in AlertStore.
//   - Audit log: every delivery attempt recorded in NotificationLog.
//   - Metrics: Prometheus counters/histograms for observability.
type AlertManager struct {
	channels []channelEntry
	dedupTTL time.Duration

	mu      sync.Mutex
	dedupDB map[string]time.Time // key -> last sent time

	// firingStart tracks when each rule entered the firing state (for escalation).
	firingStart map[string]time.Time

	// Optional integrations — set after construction.
	Store      *AlertStore
	Silences   *SilenceManager
	Routing    *RoutingConfig
	NLog       *NotificationLog
	Escalation *EscalationPolicy
	Metrics    *AlertManagerMetrics
}

// NewAlertManager creates an AlertManager.
// dedupTTL is the minimum interval between identical notifications (0 = disabled).
func NewAlertManager(dedupTTL time.Duration) *AlertManager {
	return &AlertManager{
		dedupTTL:    dedupTTL,
		dedupDB:     make(map[string]time.Time),
		firingStart: make(map[string]time.Time),
		Store:       NewAlertStore(0),
		Silences:    NewSilenceManager(),
		Routing:     NewRoutingConfig(),
		NLog:        NewNotificationLog(0),
	}
}

// WithMetrics attaches Prometheus metrics to the AlertManager.
func (m *AlertManager) WithMetrics(metrics *AlertManagerMetrics) *AlertManager {
	m.Metrics = metrics
	return m
}

// WithEscalation attaches an EscalationPolicy to the AlertManager.
func (m *AlertManager) WithEscalation(ep *EscalationPolicy) *AlertManager {
	m.Escalation = ep
	return m
}

// Register adds a named notifier with optional retry config.
// If retry is nil, DefaultRetryConfig is used.
func (m *AlertManager) Register(name string, n Notifier, retry *RetryConfig) {
	r := DefaultRetryConfig
	if retry != nil {
		r = *retry
	}
	m.channels = append(m.channels, channelEntry{name: name, n: n, retry: r})
}

// AsNotifier returns the AlertManager itself as a Notifier so it can be used
// as a drop-in replacement for individual notifiers.
func (m *AlertManager) AsNotifier() Notifier {
	return notifierFunc(m.Notify)
}

// Notify fans out the event to all matched channels concurrently.
// Routing, deduplication, silence, and escalation checks are applied before dispatch.
// The event is always recorded in the AlertStore regardless of suppression.
func (m *AlertManager) Notify(ctx context.Context, event *AlertEvent) error {
	// Always record to history.
	if m.Store != nil {
		m.Store.Record(event)
	}

	// Track metrics for every event.
	if m.Metrics != nil {
		m.Metrics.AlertsTotal.WithLabelValues(event.RuleName, string(event.State), event.Severity).Inc()
		m.Metrics.ActiveAlerts.Set(float64(len(m.Store.Active())))
	}

	// Update firing start time for escalation tracking.
	m.mu.Lock()
	if event.State == StateFiring {
		if _, ok := m.firingStart[event.RuleName]; !ok {
			m.firingStart[event.RuleName] = time.Now()
		}
	} else if event.State == StateResolved {
		delete(m.firingStart, event.RuleName)
		if m.Escalation != nil {
			m.Escalation.Reset(event.RuleName)
		}
	}
	m.mu.Unlock()

	// Check silence before dedup so silenced alerts still appear in history.
	if m.Silences != nil && m.Silences.IsSilenced(event.RuleName) {
		slog.DebugContext(ctx, "alert manager: suppressing silenced notification",
			"rule", event.RuleName)
		if m.Metrics != nil {
			m.Metrics.SilencedTotal.WithLabelValues(event.RuleName).Inc()
		}
		return nil
	}

	if m.isDuplicate(event) {
		slog.DebugContext(ctx, "alert manager: suppressing duplicate notification",
			"rule", event.RuleName, "state", event.State)
		if m.Metrics != nil {
			m.Metrics.DedupTotal.WithLabelValues(event.RuleName).Inc()
		}
		return nil
	}
	m.recordSent(event)

	// Determine which channels to use for this rule.
	targets := m.resolveChannels(event)

	var wg sync.WaitGroup
	for _, ch := range targets {
		ch := ch
		wg.Add(1)
		go func() {
			defer wg.Done()
			start := time.Now()
			err := m.sendWithRetry(ctx, ch, event)
			elapsed := time.Since(start)

			m.logDelivery(ch.name, event, err)

			if m.Metrics != nil {
				status := "sent"
				if err != nil {
					status = "failed"
				}
				m.Metrics.NotificationsTotal.WithLabelValues(ch.name, status).Inc()
				m.Metrics.NotificationDuration.WithLabelValues(ch.name).Observe(elapsed.Seconds())
			}

			if err != nil {
				slog.ErrorContext(ctx, "alert manager: notification failed",
					"channel", ch.name,
					"rule", event.RuleName,
					"error", err,
				)
			}
		}()
	}
	wg.Wait()
	return nil
}

// resolveChannels returns the channelEntry list to notify for a given event.
//
// Resolution order:
//  1. EscalationPolicy (if set) — uses firing duration to pick channels.
//  2. RoutingConfig (if set) — per-rule static channel list.
//  3. All registered channels (fallback).
func (m *AlertManager) resolveChannels(event *AlertEvent) []channelEntry {
	var names []string

	// Escalation policy takes precedence when the alert is firing.
	if m.Escalation != nil && event.State == StateFiring {
		m.mu.Lock()
		firingDur := time.Duration(0)
		if start, ok := m.firingStart[event.RuleName]; ok {
			firingDur = time.Since(start)
		}
		m.mu.Unlock()

		names = m.Escalation.ChannelsFor(event.RuleName, firingDur)
	}

	// Fall back to static routing config.
	if len(names) == 0 && m.Routing != nil {
		names = m.Routing.ChannelsFor(event.RuleName)
	}

	// Fall back to all channels.
	if len(names) == 0 {
		return m.channels
	}

	nameSet := make(map[string]struct{}, len(names))
	for _, n := range names {
		nameSet[n] = struct{}{}
	}

	var out []channelEntry
	for _, ch := range m.channels {
		if _, ok := nameSet[ch.name]; ok {
			out = append(out, ch)
		}
	}
	return out
}

// sendWithRetry attempts to deliver an event to a single channel, retrying on
// failure with exponential back-off.
func (m *AlertManager) sendWithRetry(ctx context.Context, ch channelEntry, event *AlertEvent) error {
	wait := ch.retry.InitialWait
	var lastErr error
	for attempt := 1; attempt <= ch.retry.MaxAttempts; attempt++ {
		if err := ch.n.Notify(ctx, event); err == nil {
			if attempt > 1 {
				slog.InfoContext(ctx, "alert manager: notification succeeded after retry",
					"channel", ch.name, "attempt", attempt)
			}
			return nil
		} else {
			lastErr = err
			slog.WarnContext(ctx, "alert manager: notification attempt failed",
				"channel", ch.name,
				"attempt", attempt,
				"max_attempts", ch.retry.MaxAttempts,
				"error", err,
			)
			if m.Metrics != nil && attempt > 1 {
				m.Metrics.RetryTotal.WithLabelValues(ch.name).Inc()
			}
		}

		if attempt == ch.retry.MaxAttempts {
			break
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}

		wait *= 2
		if wait > ch.retry.MaxWait {
			wait = ch.retry.MaxWait
		}
	}
	return lastErr
}

// logDelivery records a notification attempt in the NotificationLog.
func (m *AlertManager) logDelivery(channel string, event *AlertEvent, err error) {
	if m.NLog == nil {
		return
	}
	rec := &NotificationRecord{
		Timestamp: time.Now(),
		Channel:   channel,
		RuleName:  event.RuleName,
		State:     string(event.State),
		Severity:  event.Severity,
		Status:    NotificationSent,
	}
	if err != nil {
		rec.Status = NotificationFailed
		rec.Error = err.Error()
	}
	m.NLog.Record(rec)
}

// isDuplicate returns true if an identical (rule, state) notification was sent
// within the dedup window.
func (m *AlertManager) isDuplicate(event *AlertEvent) bool {
	if m.dedupTTL == 0 {
		return false
	}
	key := event.RuleName + ":" + string(event.State)
	m.mu.Lock()
	defer m.mu.Unlock()
	if last, ok := m.dedupDB[key]; ok {
		if time.Since(last) < m.dedupTTL {
			return true
		}
	}
	return false
}

func (m *AlertManager) recordSent(event *AlertEvent) {
	if m.dedupTTL == 0 {
		return
	}
	key := event.RuleName + ":" + string(event.State)
	m.mu.Lock()
	m.dedupDB[key] = time.Now()
	m.mu.Unlock()
}

// RegisteredChannels returns the names of all registered notifier channels.
func (m *AlertManager) RegisteredChannels() []string {
	names := make([]string, len(m.channels))
	for i, ch := range m.channels {
		names[i] = ch.name
	}
	return names
}

// notifierFunc is an adapter to use a plain function as a Notifier.
type notifierFunc func(ctx context.Context, event *AlertEvent) error

func (f notifierFunc) Notify(ctx context.Context, event *AlertEvent) error {
	return f(ctx, event)
}
