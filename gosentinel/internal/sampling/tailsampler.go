// Package sampling implements tail-based trace sampling policies.
package sampling

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"sync"
	"time"

	"github.com/dgraph-io/ristretto"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Span represents a single trace span for sampling decisions.
type Span struct {
	TraceID   string
	SpanID    string
	Service   string
	Operation string
	Duration  time.Duration
	Error     bool
	Status    string
	StartTime time.Time
}

// SamplingPolicy decides whether a complete trace should be kept.
type SamplingPolicy interface {
	// ShouldSample returns true if the trace represented by spans should be kept.
	ShouldSample(spans []*Span) bool
}

// AlwaysSampleErrors keeps any trace that contains at least one error span.
type AlwaysSampleErrors struct{}

// ShouldSample implements SamplingPolicy.
func (p *AlwaysSampleErrors) ShouldSample(spans []*Span) bool {
	for _, s := range spans {
		if s.Error {
			return true
		}
	}
	return false
}

// ProbabilisticPolicy keeps traces with the configured probability.
type ProbabilisticPolicy struct {
	// Rate is the fraction of traces to keep (0.0–1.0).
	Rate float64
}

// ShouldSample implements SamplingPolicy.
func (p *ProbabilisticPolicy) ShouldSample(_ []*Span) bool {
	return rand.Float64() < p.Rate //nolint:gosec
}

// LatencyPolicy keeps traces where any span exceeds the threshold duration.
type LatencyPolicy struct {
	// Threshold is the minimum span duration to trigger sampling.
	Threshold time.Duration
}

// ShouldSample implements SamplingPolicy.
func (p *LatencyPolicy) ShouldSample(spans []*Span) bool {
	for _, s := range spans {
		if s.Duration >= p.Threshold {
			return true
		}
	}
	return false
}

// CompositePolicy keeps a trace if any of its child policies returns true.
type CompositePolicy struct {
	Policies []SamplingPolicy
}

// ShouldSample implements SamplingPolicy.
func (p *CompositePolicy) ShouldSample(spans []*Span) bool {
	for _, policy := range p.Policies {
		if policy.ShouldSample(spans) {
			return true
		}
	}
	return false
}

// TailSampler buffers spans by trace_id and evaluates sampling policies once the
// buffer TTL expires, emitting complete traces that pass the policy to out.
type TailSampler struct {
	cache     *ristretto.Cache
	policy    SamplingPolicy
	bufferTTL time.Duration
	out       chan<- []*Span

	mu     sync.Mutex
	timers map[string]*time.Timer

	sampledTotal metric.Int64Counter
	droppedTotal metric.Int64Counter
}

// NewTailSampler creates a TailSampler with the given policy, buffer TTL, and output channel.
func NewTailSampler(policy SamplingPolicy, bufferTTL time.Duration, out chan<- []*Span) (*TailSampler, error) {
	cache, err := ristretto.NewCache(&ristretto.Config{
		NumCounters: 1e6,
		MaxCost:     1 << 26,
		BufferItems: 64,
	})
	if err != nil {
		return nil, fmt.Errorf("creating ristretto cache: %w", err)
	}

	meter := otel.GetMeterProvider().Meter("gosentinel/sampling")

	sampledTotal, err := meter.Int64Counter("tail_sampler_sampled_total",
		metric.WithDescription("Total traces kept by tail sampler"),
	)
	if err != nil {
		return nil, fmt.Errorf("creating sampled counter: %w", err)
	}

	droppedTotal, err := meter.Int64Counter("tail_sampler_dropped_total",
		metric.WithDescription("Total traces dropped by tail sampler"),
	)
	if err != nil {
		return nil, fmt.Errorf("creating dropped counter: %w", err)
	}

	return &TailSampler{
		cache:        cache,
		policy:       policy,
		bufferTTL:    bufferTTL,
		out:          out,
		timers:       make(map[string]*time.Timer),
		sampledTotal: sampledTotal,
		droppedTotal: droppedTotal,
	}, nil
}

// Ingest buffers a span. After bufferTTL from the first span for a trace_id,
// the buffered spans are evaluated against the policy and emitted if kept.
func (ts *TailSampler) Ingest(ctx context.Context, span *Span) {
	val, found := ts.cache.Get(span.TraceID)
	var spans []*Span
	if found {
		spans = val.([]*Span)
	}
	spans = append(spans, span)
	ts.cache.SetWithTTL(span.TraceID, spans, int64(len(spans)), ts.bufferTTL*2)

	ts.mu.Lock()
	if _, exists := ts.timers[span.TraceID]; !exists {
		traceID := span.TraceID
		ts.timers[traceID] = time.AfterFunc(ts.bufferTTL, func() {
			ts.evict(ctx, traceID)
		})
	}
	ts.mu.Unlock()
}

// evict retrieves buffered spans for traceID, evaluates the policy, and emits if kept.
func (ts *TailSampler) evict(ctx context.Context, traceID string) {
	ts.mu.Lock()
	delete(ts.timers, traceID)
	ts.mu.Unlock()

	val, found := ts.cache.Get(traceID)
	if !found {
		return
	}
	ts.cache.Del(traceID)

	spans := val.([]*Span)
	service := ""
	if len(spans) > 0 {
		service = spans[0].Service
	}

	if ts.policy.ShouldSample(spans) {
		ts.sampledTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("service", service)))
		select {
		case ts.out <- spans:
		case <-ctx.Done():
		}
	} else {
		ts.droppedTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("service", service)))
		slog.DebugContext(ctx, "tail sampler dropped trace", "trace_id", traceID, "service", service)
	}
}
