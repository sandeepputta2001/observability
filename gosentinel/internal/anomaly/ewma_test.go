package anomaly

import (
	"math"
	"testing"
)

func TestEWMADetector_WarmUp(t *testing.T) {
	d := NewEWMADetector(0.1, 3.0)
	// First 10 observations should never be anomalous
	for i := 0; i < 10; i++ {
		anomaly, _ := d.Observe(float64(i * 1000)) // extreme values
		if anomaly {
			t.Errorf("observation %d flagged as anomaly during warm-up", i)
		}
	}
}

func TestEWMADetector_DetectsAnomaly(t *testing.T) {
	d := NewEWMADetector(0.1, 3.0)

	// Warm up with stable values
	for i := 0; i < 50; i++ {
		d.Observe(100.0)
	}

	// Inject a spike
	anomaly, zScore := d.Observe(10000.0)
	if !anomaly {
		t.Errorf("expected anomaly for spike value, z_score=%.2f", zScore)
	}
	if zScore < 3.0 {
		t.Errorf("expected z_score >= 3.0, got %.2f", zScore)
	}
}

func TestEWMADetector_NoFalsePositiveOnStableData(t *testing.T) {
	d := NewEWMADetector(0.1, 3.0)

	for i := 0; i < 100; i++ {
		anomaly, _ := d.Observe(100.0 + float64(i%5)) // small variation
		if anomaly {
			t.Errorf("false positive at observation %d", i)
		}
	}
}

func TestEWMADetector_Reset(t *testing.T) {
	d := NewEWMADetector(0.1, 3.0)
	for i := 0; i < 50; i++ {
		d.Observe(100.0)
	}
	d.Reset()
	if d.mean != 0 || d.variance != 0 || d.count != 0 {
		t.Error("Reset did not clear state")
	}
}

func TestEWMADetector_ZScoreIsNonNegative(t *testing.T) {
	d := NewEWMADetector(0.1, 3.0)
	for i := 0; i < 50; i++ {
		d.Observe(100.0)
	}
	_, zScore := d.Observe(50.0)
	if math.IsNaN(zScore) || zScore < 0 {
		t.Errorf("expected non-negative z_score, got %.2f", zScore)
	}
}

func TestDetectorRegistry_GetOrCreate(t *testing.T) {
	r, err := NewDetectorRegistry(0.1, 3.0)
	if err != nil {
		t.Fatalf("NewDetectorRegistry: %v", err)
	}

	d1 := r.GetOrCreate("svc:latency")
	d2 := r.GetOrCreate("svc:latency")
	if d1 != d2 {
		t.Error("expected same detector instance for same key")
	}

	d3 := r.GetOrCreate("svc:error_rate")
	if d1 == d3 {
		t.Error("expected different detector for different key")
	}
}
