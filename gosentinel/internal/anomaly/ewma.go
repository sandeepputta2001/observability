// Package anomaly implements EWMA-based anomaly detection with 3-sigma alerting.
package anomaly

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// EWMADetector detects anomalies using Exponentially Weighted Moving Average.
// It maintains a running mean and variance and fires when a value exceeds sigma standard deviations.
type EWMADetector struct {
	alpha    float64 // smoothing factor (0 < alpha <= 1)
	mean     float64 // current EWMA mean
	variance float64 // current EWMA variance
	sigma    float64 // alert threshold multiplier
	count    int64   // number of observations (warm-up guard)
	mu       sync.RWMutex
}

// NewEWMADetector creates a detector with the given smoothing factor and sigma threshold.
// alpha=0.1 and sigma=3.0 are sensible defaults.
func NewEWMADetector(alpha, sigma float64) *EWMADetector {
	return &EWMADetector{
		alpha: alpha,
		sigma: sigma,
	}
}

// Observe updates the EWMA state with a new value and returns whether it is anomalous
// along with its z-score. The first 10 observations are used as warm-up and never flagged.
func (d *EWMADetector) Observe(value float64) (anomaly bool, zScore float64) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.count++
	if d.count == 1 {
		d.mean = value
		d.variance = 0
		return false, 0
	}

	diff := value - d.mean
	d.mean = d.mean + d.alpha*diff
	d.variance = (1 - d.alpha) * (d.variance + d.alpha*diff*diff)

	// Warm-up: don't alert for first 10 observations
	if d.count < 10 {
		return false, 0
	}

	stddev := math.Sqrt(d.variance)
	if stddev == 0 {
		return false, 0
	}

	zScore = math.Abs(value-d.mean) / stddev
	return zScore >= d.sigma, zScore
}

// Reset clears the detector state.
func (d *EWMADetector) Reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.mean = 0
	d.variance = 0
	d.count = 0
}

// AnomalyEvent represents a detected anomaly from the registry.
type AnomalyEvent struct {
	ID         string
	Service    string
	MetricName string
	Value      float64
	ZScore     float64
	DetectedAt time.Time
	Severity   string
}

// DetectorRegistry manages a set of EWMA detectors keyed by "service:metric_name".
type DetectorRegistry struct {
	detectors sync.Map // key: string -> *EWMADetector
	alpha     float64
	sigma     float64

	anomaliesTotal metric.Int64Counter
}

// NewDetectorRegistry creates a registry with the given EWMA parameters.
func NewDetectorRegistry(alpha, sigma float64) (*DetectorRegistry, error) {
	meter := otel.GetMeterProvider().Meter("gosentinel/anomaly")

	anomaliesTotal, err := meter.Int64Counter("anomaly_detections_total",
		metric.WithDescription("Total number of anomalies detected"),
	)
	if err != nil {
		return nil, fmt.Errorf("creating anomaly counter: %w", err)
	}

	return &DetectorRegistry{
		alpha:          alpha,
		sigma:          sigma,
		anomaliesTotal: anomaliesTotal,
	}, nil
}

// GetOrCreate returns the detector for key, creating one if it doesn't exist.
func (r *DetectorRegistry) GetOrCreate(key string) *EWMADetector {
	val, _ := r.detectors.LoadOrStore(key, NewEWMADetector(r.alpha, r.sigma))
	return val.(*EWMADetector)
}

// MetricPoint is a single metric observation for the registry.
type MetricPoint struct {
	TraceID   string
	Service   string
	Name      string
	Value     float64
	Labels    map[string]string
	Timestamp time.Time
}

// ObserveAll fans metric points from the input channel to the appropriate detector.
// Anomaly events are sent to the anomalies channel. Runs until ctx is cancelled.
func (r *DetectorRegistry) ObserveAll(ctx context.Context, points <-chan *MetricPoint, anomalies chan<- *AnomalyEvent) {
	for {
		select {
		case <-ctx.Done():
			slog.InfoContext(ctx, "detector registry shutting down")
			return
		case mp, ok := <-points:
			if !ok {
				return
			}
			key := mp.Service + ":" + mp.Name
			detector := r.GetOrCreate(key)
			isAnomaly, zScore := detector.Observe(mp.Value)
			if isAnomaly {
				severity := "warning"
				if zScore > r.sigma*2 {
					severity = "critical"
				}
				event := &AnomalyEvent{
					ID:         fmt.Sprintf("%s-%d", key, time.Now().UnixNano()),
					Service:    mp.Service,
					MetricName: mp.Name,
					Value:      mp.Value,
					ZScore:     zScore,
					DetectedAt: time.Now(),
					Severity:   severity,
				}
				r.anomaliesTotal.Add(ctx, 1, metric.WithAttributes(
					attribute.String("service", mp.Service),
					attribute.String("severity", severity),
				))
				slog.WarnContext(ctx, "anomaly detected",
					"service", mp.Service,
					"metric", mp.Name,
					"value", mp.Value,
					"z_score", zScore,
					"severity", severity,
				)
				select {
				case anomalies <- event:
				case <-ctx.Done():
					return
				}
			}
		}
	}
}
