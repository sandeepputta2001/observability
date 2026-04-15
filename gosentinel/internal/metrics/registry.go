// Package metrics implements all industry-standard observability metrics patterns:
// RED (Rate/Errors/Duration), USE (Utilization/Saturation/Errors),
// Four Golden Signals, SLI/SLO tracking, and custom business metrics.
package metrics

import (
	"context"
	"fmt"
	"net/http"
	"runtime"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// ── RED Method Metrics ────────────────────────────────────────────────────────

// REDMetrics implements the RED method: Rate, Errors, Duration.
// Every service endpoint should record these three signals.
type REDMetrics struct {
	// Rate: requests per second
	RequestsTotal metric.Int64Counter
	// Errors: failed requests
	ErrorsTotal metric.Int64Counter
	// Duration: request latency histogram (enables p50/p95/p99)
	RequestDuration metric.Float64Histogram
	// InFlight: current concurrent requests (saturation signal)
	InFlightRequests metric.Int64UpDownCounter

	service string
}

// NewREDMetrics creates RED metrics for the given service name.
func NewREDMetrics(service string) (*REDMetrics, error) {
	meter := otel.GetMeterProvider().Meter("gosentinel/" + service)

	requestsTotal, err := meter.Int64Counter(
		"http_requests_total",
		metric.WithDescription("Total number of HTTP requests (RED: Rate)"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return nil, fmt.Errorf("creating http_requests_total: %w", err)
	}

	errorsTotal, err := meter.Int64Counter(
		"http_errors_total",
		metric.WithDescription("Total number of HTTP errors (RED: Errors)"),
		metric.WithUnit("{error}"),
	)
	if err != nil {
		return nil, fmt.Errorf("creating http_errors_total: %w", err)
	}

	requestDuration, err := meter.Float64Histogram(
		"http_request_duration_seconds",
		metric.WithDescription("HTTP request latency distribution (RED: Duration)"),
		metric.WithUnit("s"),
		// Explicit buckets aligned with SLO thresholds
		metric.WithExplicitBucketBoundaries(
			0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0, 2.5, 5.0, 10.0,
		),
	)
	if err != nil {
		return nil, fmt.Errorf("creating http_request_duration_seconds: %w", err)
	}

	inFlight, err := meter.Int64UpDownCounter(
		"http_requests_in_flight",
		metric.WithDescription("Current number of in-flight HTTP requests (Saturation)"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return nil, fmt.Errorf("creating http_requests_in_flight: %w", err)
	}

	return &REDMetrics{
		RequestsTotal:    requestsTotal,
		ErrorsTotal:      errorsTotal,
		RequestDuration:  requestDuration,
		InFlightRequests: inFlight,
		service:          service,
	}, nil
}

// RecordRequest records a completed HTTP request with all RED signals.
func (m *REDMetrics) RecordRequest(ctx context.Context, method, path, status string, duration time.Duration, isError bool) {
	attrs := metric.WithAttributes(
		attribute.String("service", m.service),
		attribute.String("method", method),
		attribute.String("path", path),
		attribute.String("status", status),
	)
	m.RequestsTotal.Add(ctx, 1, attrs)
	m.RequestDuration.Record(ctx, duration.Seconds(), attrs)
	if isError {
		m.ErrorsTotal.Add(ctx, 1, attrs)
	}
}

// Middleware returns an HTTP middleware that automatically records RED metrics.
func (m *REDMetrics) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		attrs := metric.WithAttributes(
			attribute.String("service", m.service),
			attribute.String("method", r.Method),
		)
		m.InFlightRequests.Add(r.Context(), 1, attrs)
		defer m.InFlightRequests.Add(r.Context(), -1, attrs)

		rw := &statusRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(rw, r)

		isError := rw.status >= 500
		m.RecordRequest(r.Context(), r.Method, r.URL.Path,
			fmt.Sprintf("%d", rw.status), time.Since(start), isError)
	})
}

// ── USE Method Metrics ────────────────────────────────────────────────────────

// USEMetrics implements the USE method: Utilization, Saturation, Errors.
// Primarily for resource-level metrics (CPU, memory, goroutines, connections).
type USEMetrics struct {
	// Utilization: fraction of time resource is busy
	CPUUtilization    metric.Float64ObservableGauge
	MemoryUtilization metric.Float64ObservableGauge
	// Saturation: queue depth / backlog
	GoroutineCount metric.Int64ObservableGauge
	HeapInUse      metric.Int64ObservableGauge
	GCPauseTotal   metric.Float64ObservableCounter
	// Errors: resource errors
	GCCount metric.Int64ObservableCounter
}

// NewUSEMetrics creates USE metrics and registers runtime observers.
func NewUSEMetrics(service string) (*USEMetrics, error) {
	meter := otel.GetMeterProvider().Meter("gosentinel/" + service + "/runtime")

	cpuUtil, err := meter.Float64ObservableGauge(
		"process_cpu_utilization",
		metric.WithDescription("CPU utilization fraction (USE: Utilization)"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("creating cpu_utilization: %w", err)
	}

	memUtil, err := meter.Float64ObservableGauge(
		"process_memory_utilization",
		metric.WithDescription("Heap memory utilization fraction (USE: Utilization)"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("creating memory_utilization: %w", err)
	}

	goroutines, err := meter.Int64ObservableGauge(
		"process_goroutines",
		metric.WithDescription("Number of goroutines (USE: Saturation)"),
		metric.WithUnit("{goroutine}"),
	)
	if err != nil {
		return nil, fmt.Errorf("creating goroutines: %w", err)
	}

	heapInUse, err := meter.Int64ObservableGauge(
		"process_heap_inuse_bytes",
		metric.WithDescription("Heap bytes in use (USE: Utilization)"),
		metric.WithUnit("By"),
	)
	if err != nil {
		return nil, fmt.Errorf("creating heap_inuse: %w", err)
	}

	gcPause, err := meter.Float64ObservableCounter(
		"process_gc_pause_seconds_total",
		metric.WithDescription("Total GC pause time (USE: Errors/Saturation)"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return nil, fmt.Errorf("creating gc_pause: %w", err)
	}

	gcCount, err := meter.Int64ObservableCounter(
		"process_gc_count_total",
		metric.WithDescription("Total GC cycles (USE: Errors)"),
		metric.WithUnit("{cycle}"),
	)
	if err != nil {
		return nil, fmt.Errorf("creating gc_count: %w", err)
	}

	u := &USEMetrics{
		CPUUtilization:    cpuUtil,
		MemoryUtilization: memUtil,
		GoroutineCount:    goroutines,
		HeapInUse:         heapInUse,
		GCPauseTotal:      gcPause,
		GCCount:           gcCount,
	}

	// Register batch observer for all runtime metrics
	_, err = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)

		o.ObserveInt64(goroutines, int64(runtime.NumGoroutine()))
		o.ObserveInt64(heapInUse, int64(ms.HeapInuse))
		o.ObserveFloat64(gcPause, float64(ms.PauseTotalNs)/1e9)
		o.ObserveInt64(gcCount, int64(ms.NumGC))

		// Memory utilization: HeapInuse / HeapSys
		if ms.HeapSys > 0 {
			o.ObserveFloat64(memUtil, float64(ms.HeapInuse)/float64(ms.HeapSys))
		}
		return nil
	}, cpuUtil, memUtil, goroutines, heapInUse, gcPause, gcCount)
	if err != nil {
		return nil, fmt.Errorf("registering runtime callback: %w", err)
	}

	return u, nil
}

// ── Four Golden Signals ───────────────────────────────────────────────────────

// GoldenSignals bundles all four Google SRE golden signals in one struct.
// Latency + Traffic + Errors + Saturation.
type GoldenSignals struct {
	// 1. Latency — how long it takes to service a request
	Latency metric.Float64Histogram
	// 2. Traffic — how much demand is placed on the system
	Traffic metric.Int64Counter
	// 3. Errors — rate of requests that fail
	Errors metric.Int64Counter
	// 4. Saturation — how "full" the service is
	QueueDepth     metric.Int64UpDownCounter
	ThreadPoolUtil metric.Float64ObservableGauge
}

// NewGoldenSignals creates all four golden signal metrics.
func NewGoldenSignals(service string) (*GoldenSignals, error) {
	meter := otel.GetMeterProvider().Meter("gosentinel/" + service + "/golden")

	latency, err := meter.Float64Histogram(
		"golden_signal_latency_seconds",
		metric.WithDescription("Golden Signal 1: Request latency"),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5),
	)
	if err != nil {
		return nil, fmt.Errorf("creating latency: %w", err)
	}

	traffic, err := meter.Int64Counter(
		"golden_signal_traffic_total",
		metric.WithDescription("Golden Signal 2: Request traffic"),
	)
	if err != nil {
		return nil, fmt.Errorf("creating traffic: %w", err)
	}

	errors, err := meter.Int64Counter(
		"golden_signal_errors_total",
		metric.WithDescription("Golden Signal 3: Request errors"),
	)
	if err != nil {
		return nil, fmt.Errorf("creating errors: %w", err)
	}

	queueDepth, err := meter.Int64UpDownCounter(
		"golden_signal_queue_depth",
		metric.WithDescription("Golden Signal 4: Queue depth (saturation)"),
	)
	if err != nil {
		return nil, fmt.Errorf("creating queue_depth: %w", err)
	}

	threadUtil, err := meter.Float64ObservableGauge(
		"golden_signal_thread_utilization",
		metric.WithDescription("Golden Signal 4: Thread pool utilization (saturation)"),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("creating thread_util: %w", err)
	}

	return &GoldenSignals{
		Latency:        latency,
		Traffic:        traffic,
		Errors:         errors,
		QueueDepth:     queueDepth,
		ThreadPoolUtil: threadUtil,
	}, nil
}

// ── Business / Domain Metrics ─────────────────────────────────────────────────

// PipelineMetrics tracks GoSentinel-specific pipeline health metrics.
type PipelineMetrics struct {
	// Correlation engine
	CorrelationEventsTotal metric.Int64Counter
	CorrelationJoinLatency metric.Float64Histogram
	CorrelationCacheSize   metric.Int64ObservableGauge
	// Sampling
	SampledTracesTotal metric.Int64Counter
	DroppedTracesTotal metric.Int64Counter
	SamplingRatio      metric.Float64ObservableGauge
	// Anomaly detection
	AnomaliesDetected metric.Int64Counter
	AnomalyZScore     metric.Float64Histogram
	// Alert evaluation
	AlertsFired      metric.Int64Counter
	AlertsResolved   metric.Int64Counter
	RuleEvalDuration metric.Float64Histogram
	// SLO
	SLOBurnRate          metric.Float64ObservableGauge
	ErrorBudgetRemaining metric.Float64ObservableGauge
}

// NewPipelineMetrics creates all pipeline-specific metrics.
func NewPipelineMetrics() (*PipelineMetrics, error) {
	meter := otel.GetMeterProvider().Meter("gosentinel/pipeline")

	corrTotal, err := meter.Int64Counter("pipeline_correlation_events_total",
		metric.WithDescription("Total correlated events emitted"))
	if err != nil {
		return nil, fmt.Errorf("correlation_events_total: %w", err)
	}

	corrLatency, err := meter.Float64Histogram("pipeline_correlation_join_latency_seconds",
		metric.WithDescription("Time from first signal to correlated event"),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(0.001, 0.01, 0.1, 0.5, 1, 5, 30))
	if err != nil {
		return nil, fmt.Errorf("correlation_join_latency: %w", err)
	}

	cacheSize, err := meter.Int64ObservableGauge("pipeline_correlation_cache_size",
		metric.WithDescription("Number of incomplete trace buckets in cache"))
	if err != nil {
		return nil, fmt.Errorf("correlation_cache_size: %w", err)
	}

	sampledTotal, err := meter.Int64Counter("pipeline_sampled_traces_total",
		metric.WithDescription("Traces kept by tail sampler"))
	if err != nil {
		return nil, fmt.Errorf("sampled_traces_total: %w", err)
	}

	droppedTotal, err := meter.Int64Counter("pipeline_dropped_traces_total",
		metric.WithDescription("Traces dropped by tail sampler"))
	if err != nil {
		return nil, fmt.Errorf("dropped_traces_total: %w", err)
	}

	samplingRatio, err := meter.Float64ObservableGauge("pipeline_sampling_ratio",
		metric.WithDescription("Current effective sampling ratio"),
		metric.WithUnit("1"))
	if err != nil {
		return nil, fmt.Errorf("sampling_ratio: %w", err)
	}

	anomaliesDetected, err := meter.Int64Counter("pipeline_anomalies_detected_total",
		metric.WithDescription("Total anomalies detected by EWMA detector"))
	if err != nil {
		return nil, fmt.Errorf("anomalies_detected: %w", err)
	}

	anomalyZScore, err := meter.Float64Histogram("pipeline_anomaly_z_score",
		metric.WithDescription("Z-score distribution of detected anomalies"),
		metric.WithExplicitBucketBoundaries(3, 4, 5, 6, 8, 10, 15, 20))
	if err != nil {
		return nil, fmt.Errorf("anomaly_z_score: %w", err)
	}

	alertsFired, err := meter.Int64Counter("pipeline_alerts_fired_total",
		metric.WithDescription("Total alerts fired"))
	if err != nil {
		return nil, fmt.Errorf("alerts_fired: %w", err)
	}

	alertsResolved, err := meter.Int64Counter("pipeline_alerts_resolved_total",
		metric.WithDescription("Total alerts resolved"))
	if err != nil {
		return nil, fmt.Errorf("alerts_resolved: %w", err)
	}

	ruleEvalDuration, err := meter.Float64Histogram("pipeline_rule_eval_duration_seconds",
		metric.WithDescription("Time to evaluate all alert rules"),
		metric.WithUnit("s"))
	if err != nil {
		return nil, fmt.Errorf("rule_eval_duration: %w", err)
	}

	sloBurnRate, err := meter.Float64ObservableGauge("pipeline_slo_burn_rate",
		metric.WithDescription("Current SLO error budget burn rate multiplier"))
	if err != nil {
		return nil, fmt.Errorf("slo_burn_rate: %w", err)
	}

	errorBudget, err := meter.Float64ObservableGauge("pipeline_slo_error_budget_remaining",
		metric.WithDescription("Remaining SLO error budget fraction"),
		metric.WithUnit("1"))
	if err != nil {
		return nil, fmt.Errorf("error_budget_remaining: %w", err)
	}

	return &PipelineMetrics{
		CorrelationEventsTotal: corrTotal,
		CorrelationJoinLatency: corrLatency,
		CorrelationCacheSize:   cacheSize,
		SampledTracesTotal:     sampledTotal,
		DroppedTracesTotal:     droppedTotal,
		SamplingRatio:          samplingRatio,
		AnomaliesDetected:      anomaliesDetected,
		AnomalyZScore:          anomalyZScore,
		AlertsFired:            alertsFired,
		AlertsResolved:         alertsResolved,
		RuleEvalDuration:       ruleEvalDuration,
		SLOBurnRate:            sloBurnRate,
		ErrorBudgetRemaining:   errorBudget,
	}, nil
}

// RecordAnomaly records an anomaly detection event with its z-score.
func (m *PipelineMetrics) RecordAnomaly(ctx context.Context, service, metricName string, zScore float64) {
	attrs := metricAttrs(service, metricName)
	m.AnomaliesDetected.Add(ctx, 1, attrs)
	m.AnomalyZScore.Record(ctx, zScore, attrs)
}

// RecordAlertFired records an alert state transition to firing.
func (m *PipelineMetrics) RecordAlertFired(ctx context.Context, ruleName, severity string) {
	m.AlertsFired.Add(ctx, 1, metric.WithAttributes(
		attribute.String("rule", ruleName),
		attribute.String("severity", severity),
	))
}

// RecordAlertResolved records an alert state transition to resolved.
func (m *PipelineMetrics) RecordAlertResolved(ctx context.Context, ruleName string) {
	m.AlertsResolved.Add(ctx, 1, metric.WithAttributes(
		attribute.String("rule", ruleName),
	))
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// metricAttrs returns a MeasurementOption with service and metric_name attributes.
func metricAttrs(service, metricName string) metric.MeasurementOption {
	return metric.WithAttributes(
		attribute.String("service", service),
		attribute.String("metric_name", metricName),
	)
}

// statusRecorder captures the HTTP response status code.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}
