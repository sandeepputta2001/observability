# GoSentinel — Low-Level Design (LLD)

## 1. Module Structure

```
github.com/yourorg/gosentinel
├── cmd/
│   ├── collector/main.go     — health endpoint sidecar
│   ├── pipeline/main.go      — streaming pipeline entrypoint
│   ├── api/main.go           — ConnectRPC query API
│   └── ui/main.go            — HTMX frontend server
├── internal/
│   ├── correlation/          — trace-metric-log stream join
│   ├── sampling/             — tail-based sampling policies
│   ├── anomaly/              — EWMA anomaly detection
│   ├── slo/                  — SLO burn rate tracking
│   ├── alerting/             — rule evaluation + notifiers
│   ├── storage/              — backend client wrappers
│   ├── metrics/              — RED/USE/Golden Signal metrics
│   ├── health/               — structured health checks
│   └── tracing/              — distributed tracing helpers
├── pkg/
│   ├── otel/                 — shared OTel SDK bootstrap
│   ├── middleware/           — HTTP middleware chain
│   └── config/               — Viper config loader
└── gen/go/gosentinel/v1/     — generated proto stubs
```

---

## 2. CorrelationEngine — Detailed Design

### Data Structures

```go
// sync.Map bucket store — avoids ristretto async write issue
type CorrelationEngine struct {
    buckets sync.Map  // trace_id -> *bucket
    window  time.Duration
}

type bucket struct {
    mu        sync.Mutex
    span      *TraceSpan
    metrics   []*MetricPoint
    logs      []*LogLine
    createdAt time.Time
}
```

### Algorithm

```
For each incoming signal (span | metric | log):
  1. LoadOrStore bucket for trace_id
  2. Lock bucket, apply signal
  3. Check completeness: span != nil && len(metrics) > 0 && len(logs) > 0
  4. If complete: emit CorrelatedEvent, delete bucket
  5. Background ticker (window/2): evict buckets older than window

Complexity: O(1) per signal, O(n) eviction scan every window/2
Memory: bounded by active trace count × bucket size
```

### OTel Instrumentation
- `correlation_events_total` — Int64Counter, labels: service
- `correlation_join_latency_seconds` — Float64Histogram, buckets: 1ms–30s

---

## 3. TailSampler — Detailed Design

### Policy Chain (OR semantics)

```
CompositePolicy
├── AlwaysSampleErrors   — any span.Error == true
├── LatencyPolicy        — any span.Duration >= 500ms
└── ProbabilisticPolicy  — rand.Float64() < 0.10
```

### Buffer Lifecycle

```
Ingest(span):
  1. LoadOrStore span slice in ristretto (TTL = 2×bufferTTL)
  2. If first span for trace_id: schedule time.AfterFunc(bufferTTL, evict)
  3. evict():
     a. Retrieve spans from cache
     b. policy.ShouldSample(spans) → keep or drop
     c. If keep: send to out channel
     d. Record sampled_total or dropped_total counter
```

### Metrics
- `tail_sampler_sampled_total` — Int64Counter
- `tail_sampler_dropped_total` — Int64Counter

---

## 4. EWMADetector — Detailed Design

### Algorithm

```
Observe(value):
  count++
  if count == 1: mean = value; return false
  diff = value - mean
  mean = mean + α × diff
  variance = (1-α) × (variance + α × diff²)
  if count < 10: return false  // warm-up
  stddev = √variance
  if stddev == 0: return false
  z_score = |value - mean| / stddev
  return z_score >= σ, z_score
```

Parameters:
- α = 0.1 (smoothing factor — higher = more reactive)
- σ = 3.0 (alert threshold — 3 standard deviations)

### DetectorRegistry

```
ObserveAll(ctx, points <-chan *MetricPoint, anomalies chan<- *AnomalyEvent):
  for each point:
    key = point.Service + ":" + point.Name
    detector = registry.GetOrCreate(key)  // sync.Map LoadOrStore
    isAnomaly, zScore = detector.Observe(point.Value)
    if isAnomaly:
      emit AnomalyEvent{severity: "warning" if z<6σ else "critical"}
```

---

## 5. RuleEvaluator — Detailed Design

### State Machine

```
                    isFiring && for=0
                    ┌──────────────────────────────────────┐
                    │                                      ▼
[no state] ──isFiring──► [pending] ──for elapsed──► [firing] ──!isFiring──► [resolved]
                                                        │
                                                        └──► notify(Slack, PagerDuty)
```

### PostgreSQL Schema

```sql
CREATE TABLE alert_states (
    rule_name  TEXT PRIMARY KEY,
    state      TEXT NOT NULL CHECK (state IN ('pending', 'firing')),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

### Broadcast Pattern

```go
// sync.Map of subscriber channels — O(1) subscribe/unsubscribe
type RuleEvaluator struct {
    subscribers sync.Map  // *chan *AlertEvent
}

func (e *RuleEvaluator) Subscribe() (<-chan *AlertEvent, func()) {
    ch := make(chan *AlertEvent, 64)
    e.subscribers.Store(key, ch)
    return ch, func() { e.subscribers.Delete(key); close(ch) }
}
```

---

## 6. QueryServer — Concurrent Fan-Out

```go
func (s *QueryServer) GetServiceHealth(ctx, req) (resp, error) {
    g, gctx := errgroup.WithContext(ctx)

    // Goroutine 1: VictoriaMetrics — error_rate, p50/p95/p99, request_rate
    g.Go(func() error { ... vmClient.QueryInstant(gctx, ...) ... })

    // Goroutine 2: Jaeger — top 5 error spans
    g.Go(func() error { ... jaegerClient.FindTraces(gctx, ...) ... })

    // Goroutine 3: Pyroscope — top CPU function
    g.Go(func() error { ... pyroscopeClient.GetTopFunctions(gctx, ...) ... })

    // All three run concurrently; total latency = max(individual latencies)
    if err := g.Wait(); err != nil { return nil, err }
    return &pb.GetServiceHealthResponse{...}, nil
}
```

---

## 7. HTTP Middleware Chain

```
Request
  │
  ▼
OTelTracing (otelhttp.NewHandler) — creates root span, injects trace context
  │
  ▼
RateLimiter (token bucket, 100 req/s per IP) — returns 429 if exceeded
  │
  ▼
JWTAuth (golang-jwt/jwt/v5) — validates Bearer token, injects claims
  │
  ▼
RequestLogger (slog with trace_id + span_id) — structured access log
  │
  ▼
REDMetrics.Middleware — records requests_total, errors_total, duration
  │
  ▼
Handler
```

---

## 8. SSE Alert Stream

```go
func sseAlertsHandler(w http.ResponseWriter, r *http.Request) {
    flusher := w.(http.Flusher)
    w.Header().Set("Content-Type", "text/event-stream")
    w.Header().Set("Cache-Control", "no-cache")
    w.Header().Set("X-Accel-Buffering", "no")  // disable nginx buffering

    ticker := time.NewTicker(15 * time.Second)  // keepalive
    for {
        select {
        case <-r.Context().Done(): return
        case <-ticker.C:
            fmt.Fprintf(w, ": keepalive\n\n")
            flusher.Flush()
        case alert := <-alertCh:
            fmt.Fprintf(w, "data: %s\n\n", json.Marshal(alert))
            flusher.Flush()
        }
    }
}
```

---

## 9. SLO Burn Rate Calculation

```
availability(window) = sum(increase(good_requests[window])) / sum(increase(total_requests[window]))
error_rate(window)   = 1 - availability(window)
burn_rate(window)    = error_rate(window) / (1 - SLO_target)

Multi-window alert fires when BOTH windows exceed threshold:
  Fast burn:  burn_rate(1h) >= 14.4 AND burn_rate(5m) >= 14.4
  Slow burn:  burn_rate(6h) >= 6.0  AND burn_rate(30m) >= 6.0
  Gradual:    burn_rate(3d) >= 3.0  AND burn_rate(6h) >= 3.0
```

---

## 10. Config Loading

```
Priority (highest to lowest):
  1. Environment variables: GOSENTINEL_OTLP_ENDPOINT
  2. Config file (--config flag): config.yaml
  3. Defaults (hardcoded in pkg/config/config.go)

Viper key mapping: "." → "_" for env vars
Example: victoria_metrics.endpoint → GOSENTINEL_VICTORIA_METRICS_ENDPOINT
```

---

## 11. Kubernetes Resource Sizing

| Component | CPU Request | CPU Limit | Mem Request | Mem Limit | Replicas |
|-----------|-------------|-----------|-------------|-----------|----------|
| pipeline  | 200m        | 500m      | 256Mi       | 512Mi     | 2-8 (HPA)|
| api       | 100m        | 500m      | 128Mi       | 512Mi     | 3-12 (HPA)|
| ui        | 50m         | 200m      | 64Mi        | 256Mi     | 2        |
| otel-collector | 100m  | 500m      | 256Mi       | 512Mi     | 1        |

---

## 12. Graceful Shutdown Sequence

```
SIGTERM received
  │
  ├── signal.NotifyContext cancels root context
  │
  ├── HTTP server: srv.Shutdown(15s timeout)
  │     └── drains in-flight requests
  │
  ├── Pipeline goroutines: ctx.Done() → return
  │     ├── CorrelationEngine.Run() exits
  │     ├── DetectorRegistry.ObserveAll() exits
  │     └── RuleEvaluator.Run() exits
  │
  └── OTel shutdown (10s timeout)
        ├── TracerProvider.Shutdown() — flushes pending spans
        ├── MeterProvider.Shutdown() — flushes pending metrics
        └── gRPC connection.Close()
```

---

## 13. Error Handling Conventions

```go
// Always wrap with context
return fmt.Errorf("operation description: %w", err)

// Never discard errors
if err := something(); err != nil {
    slog.ErrorContext(ctx, "description", "error", err)
    return fmt.Errorf("something: %w", err)
}

// Structured logging with trace correlation
slog.ErrorContext(ctx, "msg",
    "trace_id", traceID,
    "error", err,
    "key", value,
)
```

---

## 14. Testing Strategy

| Layer | Approach | Coverage Target |
|-------|----------|-----------------|
| Unit | Table-driven, `testing.T`, no external deps | >80% |
| Integration | Docker Compose, real backends | key flows |
| Race detection | `-race` flag on all tests | all packages |
| Benchmarks | `testing.B` for hot paths (EWMA, correlation) | critical paths |

Test patterns used:
- Mock interfaces (VMQuerier, Notifier) for unit isolation
- Channel-based assertions with `time.After` timeouts
- `sync.WaitGroup` for goroutine synchronization in tests
