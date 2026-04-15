package sampling

import (
	"context"
	"testing"
	"time"
)

func TestAlwaysSampleErrors(t *testing.T) {
	p := &AlwaysSampleErrors{}
	tests := []struct {
		name   string
		spans  []*Span
		expect bool
	}{
		{"no error spans", []*Span{{Error: false}}, false},
		{"one error span", []*Span{{Error: false}, {Error: true}}, true},
		{"all error spans", []*Span{{Error: true}, {Error: true}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := p.ShouldSample(tt.spans); got != tt.expect {
				t.Errorf("ShouldSample() = %v, want %v", got, tt.expect)
			}
		})
	}
}

func TestLatencyPolicy(t *testing.T) {
	p := &LatencyPolicy{Threshold: 500 * time.Millisecond}
	tests := []struct {
		name   string
		spans  []*Span
		expect bool
	}{
		{"below threshold", []*Span{{Duration: 100 * time.Millisecond}}, false},
		{"at threshold", []*Span{{Duration: 500 * time.Millisecond}}, true},
		{"above threshold", []*Span{{Duration: 1 * time.Second}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := p.ShouldSample(tt.spans); got != tt.expect {
				t.Errorf("ShouldSample() = %v, want %v", got, tt.expect)
			}
		})
	}
}

func TestCompositePolicy_OR(t *testing.T) {
	p := &CompositePolicy{
		Policies: []SamplingPolicy{
			&AlwaysSampleErrors{},
			&LatencyPolicy{Threshold: 500 * time.Millisecond},
		},
	}
	// Neither condition met
	if p.ShouldSample([]*Span{{Duration: 100 * time.Millisecond, Error: false}}) {
		t.Error("expected false when no policy matches")
	}
	// Error condition met
	if !p.ShouldSample([]*Span{{Error: true}}) {
		t.Error("expected true when error policy matches")
	}
	// Latency condition met
	if !p.ShouldSample([]*Span{{Duration: 1 * time.Second}}) {
		t.Error("expected true when latency policy matches")
	}
}

func TestTailSampler_EmitsErrorTrace(t *testing.T) {
	out := make(chan []*Span, 1)
	policy := &AlwaysSampleErrors{}
	sampler, err := NewTailSampler(policy, 100*time.Millisecond, out)
	if err != nil {
		t.Fatalf("NewTailSampler: %v", err)
	}

	ctx := context.Background()
	sampler.Ingest(ctx, &Span{TraceID: "err-trace", Service: "svc", Error: true})

	select {
	case spans := <-out:
		if len(spans) == 0 {
			t.Error("expected spans in output")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for sampled trace")
	}
}

func TestTailSampler_DropsNonErrorTrace(t *testing.T) {
	out := make(chan []*Span, 1)
	policy := &AlwaysSampleErrors{}
	sampler, err := NewTailSampler(policy, 100*time.Millisecond, out)
	if err != nil {
		t.Fatalf("NewTailSampler: %v", err)
	}

	ctx := context.Background()
	sampler.Ingest(ctx, &Span{TraceID: "ok-trace", Service: "svc", Error: false})

	select {
	case spans := <-out:
		t.Errorf("unexpected sampled trace: %+v", spans)
	case <-time.After(500 * time.Millisecond):
		// expected: dropped
	}
}
