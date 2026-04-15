// Package correlation implements stream joining of traces, metrics, and logs on trace_id.
package correlation

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// TraceSpan is a single span received from the OTLP pipeline.
type TraceSpan struct {
	TraceID   string
	SpanID    string
	Service   string
	Operation string
	StartTime time.Time
	Duration  time.Duration
	Error     bool
	Status    string
}

// MetricPoint is a single metric observation tagged with a trace_id.
type MetricPoint struct {
	TraceID   string
	Service   string
	Name      string
	Value     float64
	Labels    map[string]string
	Timestamp time.Time
}

// LogLine is a single structured log entry tagged with a trace_id.
type LogLine struct {
	TraceID   string
	Service   string
	Level     string
	Message   string
	Fields    map[string]string
	Timestamp time.Time
}

// CorrelatedEvent is emitted when all three signal types for a trace_id are present.
type CorrelatedEvent struct {
	TraceID   string
	Service   string
	Timestamp time.Time
	Span      *TraceSpan
	Metrics   []*MetricPoint
	Logs      []*LogLine
}

// bucket holds partial signal data for a single trace_id.
type bucket struct {
	mu        sync.Mutex
	span      *TraceSpan
	metrics   []*MetricPoint
	logs      []*LogLine
	createdAt time.Time
}

// CorrelationEngine joins trace, metric, and log streams on trace_id within a time window.
// It uses a sync.Map for the bucket store to avoid ristretto's async write semantics.
type CorrelationEngine struct {
	traces  <-chan *TraceSpan
	metrics <-chan *MetricPoint
	logs    <-chan *LogLine
	out     chan<- *CorrelatedEvent
	window  time.Duration

	buckets sync.Map // key: trace_id -> *bucket

	eventsTotal metric.Int64Counter
	joinLatency metric.Float64Histogram
}

// NewCorrelationEngine creates a CorrelationEngine with the given channels and join window.
func NewCorrelationEngine(
	traces <-chan *TraceSpan,
	metrics <-chan *MetricPoint,
	logs <-chan *LogLine,
	out chan<- *CorrelatedEvent,
	window time.Duration,
) (*CorrelationEngine, error) {
	meter := otel.GetMeterProvider().Meter("gosentinel/correlation")

	eventsTotal, err := meter.Int64Counter("correlation_events_total",
		metric.WithDescription("Total number of correlated events emitted"),
	)
	if err != nil {
		return nil, fmt.Errorf("creating correlation_events_total counter: %w", err)
	}

	joinLatency, err := meter.Float64Histogram("correlation_join_latency_seconds",
		metric.WithDescription("Latency from first signal to correlated event emission"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return nil, fmt.Errorf("creating correlation_join_latency_seconds histogram: %w", err)
	}

	return &CorrelationEngine{
		traces:      traces,
		metrics:     metrics,
		logs:        logs,
		out:         out,
		window:      window,
		eventsTotal: eventsTotal,
		joinLatency: joinLatency,
	}, nil
}

// Run starts the correlation fan-in loop. It blocks until ctx is cancelled.
// A background goroutine evicts stale buckets that exceed the join window.
func (e *CorrelationEngine) Run(ctx context.Context) {
	// Eviction ticker: clean up incomplete buckets older than window.
	evictTicker := time.NewTicker(e.window / 2)
	defer evictTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.InfoContext(ctx, "correlation engine shutting down")
			return

		case <-evictTicker.C:
			e.evictStale()

		case span, ok := <-e.traces:
			if !ok {
				return
			}
			e.upsert(ctx, span.TraceID, func(b *bucket) {
				b.span = span
			})

		case mp, ok := <-e.metrics:
			if !ok {
				return
			}
			e.upsert(ctx, mp.TraceID, func(b *bucket) {
				b.metrics = append(b.metrics, mp)
			})

		case ll, ok := <-e.logs:
			if !ok {
				return
			}
			e.upsert(ctx, ll.TraceID, func(b *bucket) {
				b.logs = append(b.logs, ll)
			})
		}
	}
}

// upsert retrieves or creates a bucket for traceID, applies fn, then checks for completeness.
func (e *CorrelationEngine) upsert(ctx context.Context, traceID string, fn func(*bucket)) {
	start := time.Now()

	val, _ := e.buckets.LoadOrStore(traceID, &bucket{createdAt: time.Now()})
	b := val.(*bucket)

	b.mu.Lock()
	fn(b)
	complete := b.span != nil && len(b.metrics) > 0 && len(b.logs) > 0
	var event *CorrelatedEvent
	if complete {
		event = &CorrelatedEvent{
			TraceID:   traceID,
			Service:   b.span.Service,
			Timestamp: time.Now(),
			Span:      b.span,
			Metrics:   b.metrics,
			Logs:      b.logs,
		}
	}
	b.mu.Unlock()

	if event != nil {
		e.buckets.Delete(traceID)
		select {
		case e.out <- event:
			e.eventsTotal.Add(ctx, 1, metric.WithAttributes(
				attribute.String("service", event.Service),
			))
			e.joinLatency.Record(ctx, time.Since(start).Seconds())
		case <-ctx.Done():
		}
	}
}

// evictStale removes buckets that have exceeded the join window without completing.
func (e *CorrelationEngine) evictStale() {
	cutoff := time.Now().Add(-e.window)
	e.buckets.Range(func(key, val any) bool {
		b := val.(*bucket)
		b.mu.Lock()
		old := b.createdAt.Before(cutoff)
		b.mu.Unlock()
		if old {
			e.buckets.Delete(key)
		}
		return true
	})
}
