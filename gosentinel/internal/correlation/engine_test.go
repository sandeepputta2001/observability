package correlation

import (
	"context"
	"testing"
	"time"
)

func TestCorrelationEngine_EmitsOnAllThreeSignals(t *testing.T) {
	traces := make(chan *TraceSpan, 1)
	metrics := make(chan *MetricPoint, 1)
	logs := make(chan *LogLine, 1)
	out := make(chan *CorrelatedEvent, 1)

	engine, err := NewCorrelationEngine(traces, metrics, logs, out, 5*time.Second)
	if err != nil {
		t.Fatalf("NewCorrelationEngine: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go engine.Run(ctx)

	const traceID = "abc123"
	traces <- &TraceSpan{TraceID: traceID, Service: "svc", Operation: "op"}
	metrics <- &MetricPoint{TraceID: traceID, Service: "svc", Name: "latency", Value: 42}
	logs <- &LogLine{TraceID: traceID, Service: "svc", Message: "hello"}

	select {
	case ev := <-out:
		if ev.TraceID != traceID {
			t.Errorf("expected trace_id %q, got %q", traceID, ev.TraceID)
		}
		if ev.Span == nil {
			t.Error("expected span to be set")
		}
		if len(ev.Metrics) == 0 {
			t.Error("expected metrics to be set")
		}
		if len(ev.Logs) == 0 {
			t.Error("expected logs to be set")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for correlated event")
	}
}

func TestCorrelationEngine_NoEmitWithMissingSignals(t *testing.T) {
	traces := make(chan *TraceSpan, 1)
	metrics := make(chan *MetricPoint, 1)
	logs := make(chan *LogLine, 1)
	out := make(chan *CorrelatedEvent, 1)

	engine, err := NewCorrelationEngine(traces, metrics, logs, out, 5*time.Second)
	if err != nil {
		t.Fatalf("NewCorrelationEngine: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go engine.Run(ctx)

	// Only send span and metric — no log
	traces <- &TraceSpan{TraceID: "xyz", Service: "svc"}
	metrics <- &MetricPoint{TraceID: "xyz", Service: "svc", Name: "rps", Value: 1}

	select {
	case ev := <-out:
		t.Errorf("unexpected correlated event: %+v", ev)
	case <-time.After(300 * time.Millisecond):
		// expected: no event
	}
}

func TestCorrelationEngine_MultipleTraces(t *testing.T) {
	traces := make(chan *TraceSpan, 10)
	metrics := make(chan *MetricPoint, 10)
	logs := make(chan *LogLine, 10)
	out := make(chan *CorrelatedEvent, 10)

	engine, err := NewCorrelationEngine(traces, metrics, logs, out, 5*time.Second)
	if err != nil {
		t.Fatalf("NewCorrelationEngine: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go engine.Run(ctx)

	ids := []string{"t1", "t2", "t3"}
	for _, id := range ids {
		traces <- &TraceSpan{TraceID: id, Service: "svc"}
		metrics <- &MetricPoint{TraceID: id, Service: "svc", Name: "m", Value: 1}
		logs <- &LogLine{TraceID: id, Service: "svc", Message: "msg"}
	}

	received := map[string]bool{}
	timeout := time.After(2 * time.Second)
	for len(received) < len(ids) {
		select {
		case ev := <-out:
			received[ev.TraceID] = true
		case <-timeout:
			t.Fatalf("timed out; received %d/%d events", len(received), len(ids))
		}
	}
}
