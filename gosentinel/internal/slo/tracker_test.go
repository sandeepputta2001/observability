package slo

import (
	"context"
	"testing"
	"time"
)

// mockVM is a test double for VMQuerier.
type mockVM struct {
	results map[string]float64
}

func (m *mockVM) QueryInstant(_ context.Context, expr string, _ time.Time) (float64, error) {
	if v, ok := m.results[expr]; ok {
		return v, nil
	}
	return 0, nil
}

func TestSLOTracker_ComputeBurnRate_Healthy(t *testing.T) {
	vm := &mockVM{results: map[string]float64{
		"sum(increase(good_requests[1h]))":  990,
		"sum(increase(total_requests[1h]))": 1000,
	}}
	tracker := NewSLOTracker("test-slo", "svc", 0.999, 30*24*time.Hour,
		"good_requests", "total_requests", vm)

	rate, err := tracker.ComputeBurnRate(context.Background(), time.Hour)
	if err != nil {
		t.Fatalf("ComputeBurnRate: %v", err)
	}
	// availability = 0.99, error_rate = 0.01, budget = 0.001, burn = 10x
	if rate < 9 || rate > 11 {
		t.Errorf("expected burn rate ~10, got %.2f", rate)
	}
}

func TestSLOTracker_ComputeBurnRate_ZeroTotal(t *testing.T) {
	vm := &mockVM{results: map[string]float64{}}
	tracker := NewSLOTracker("test-slo", "svc", 0.999, 30*24*time.Hour,
		"good_requests", "total_requests", vm)

	rate, err := tracker.ComputeBurnRate(context.Background(), time.Hour)
	if err != nil {
		t.Fatalf("ComputeBurnRate: %v", err)
	}
	if rate != 0 {
		t.Errorf("expected 0 burn rate for zero traffic, got %.2f", rate)
	}
}

func TestDurationToPromRange(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{time.Hour, "1h"},
		{6 * time.Hour, "6h"},
		{30 * time.Minute, "30m"},
		{3 * 24 * time.Hour, "3d"},
		{90 * time.Second, "90s"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := durationToPromRange(tt.d)
			if got != tt.want {
				t.Errorf("durationToPromRange(%v) = %q, want %q", tt.d, got, tt.want)
			}
		})
	}
}
