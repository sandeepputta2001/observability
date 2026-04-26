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
│   ├── alerting/             — rule evaluation + notifiers + escalation + grouping
│   │   ├── evaluator.go      — RuleEvaluator (MetricsQL → state machine → AlertManager)
│   │   ├── manager.go        — AlertManager (routing, dedup, retry, fan-out)
│   │   ├── notifier.go       — Slack, Gmail, PagerDuty, OpsGenie, Teams, Webhook
│   │   ├── escalation.go     — EscalationPolicy (time/severity-based channel escalation)
│   │   ├── grouping.go       — AlertGrouper (batch correlated alerts, reduce noise)
│   │   ├── metrics.go        — AlertManagerMetrics (Prometheus instrumentation)
│   │   ├── routing.go        — RoutingConfig (per-rule channel map)
│   │   ├── store.go          — AlertStore + SilenceManager
│   │   ├── notification_log.go — NotificationLog (audit ring buffer)
│   │   └── hmac.go           — HMAC-SHA256 webhook signing
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
      → AlertManager.Notify() as synthetic alert event
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
                                                        └──► AlertManager.Notify()
                                                                  │
                                                    EscalationPolicy.ChannelsFor(rule, firingDur)
                                                                  │ (fallback)
                                                         RoutingConfig.ChannelsFor(rule)
                                                                  │
                                                    ┌─────────────┼──────────────────┐
                                                    ▼             ▼                  ▼
                                                 Slack          Gmail           PagerDuty
                                                 Teams        Webhook           OpsGenie
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

## 6. AlertManager — Detailed Design

### Notification Pipeline

```
AlertEvent
    │
    ├─► AlertStore.Record()           always — ring buffer (500 events)
    │
    ├─► Prometheus metrics update     alerts_total{rule, state, severity}++
    │                                 active_alerts = len(Store.Active())
    │
    ├─► firingStart tracking          record when rule first entered firing state
    │
    ├─► SilenceManager.IsSilenced()   drop if rule is silenced
    │       └─► silenced_total{rule}++
    │
    ├─► isDuplicate()                 drop if same (rule, state) within dedupTTL
    │       └─► dedup_total{rule}++
    │
    ├─► resolveChannels()
    │       ├── EscalationPolicy.ChannelsFor(rule, firingDuration)  [priority 1]
    │       ├── RoutingConfig.ChannelsFor(rule)                     [priority 2]
    │       └── all registered channels                             [fallback]
    │
    └─► fan-out (one goroutine per channel)
            │
            ├─► sendWithRetry()       exponential back-off
            │       attempt 1 ──► Notifier.Notify()
            │       wait 2s
            │       attempt 2 ──► Notifier.Notify()  → retry_total{channel}++
            │       wait 4s (capped at 30s)
            │       attempt 3 ──► Notifier.Notify()  → retry_total{channel}++
            │
            ├─► NotificationLog.Record()   audit trail (1000 records)
            └─► notifications_total{channel, status}++
                notification_duration_seconds{channel}.Observe(elapsed)
```

### AlertManager struct

```go
type AlertManager struct {
    channels    []channelEntry       // registered notifiers
    dedupTTL    time.Duration
    dedupDB     map[string]time.Time // (rule:state) → last sent
    firingStart map[string]time.Time // rule → when it started firing

    Store      *AlertStore
    Silences   *SilenceManager
    Routing    *RoutingConfig
    NLog       *NotificationLog
    Escalation *EscalationPolicy    // optional — time-based escalation
    Metrics    *AlertManagerMetrics // optional — Prometheus instrumentation
}
```

### RoutingConfig

```go
// Per-rule channel routing — thread-safe, updatable at runtime.
type RoutingConfig struct {
    mu     sync.RWMutex
    routes map[string][]string  // rule name → channel names
}

// "*" wildcard applies to every alert.
// Rule-specific entries are merged on top.
func (rc *RoutingConfig) ChannelsFor(ruleName string) []string
```

### EscalationPolicy

```go
// EscalationLevel defines a single escalation tier.
type EscalationLevel struct {
    After          time.Duration // how long alert must be firing
    Channels       []string      // notifiers to use at this level
    RepeatInterval time.Duration // suppress re-notification within window
}

// Default policy (applied via "*" wildcard):
//   Level 0 (immediate):  slack + gmail,     repeat every 10m
//   Level 1 (after 5m):   + pagerduty,       repeat every 30m
//   Level 2 (after 15m):  + opsgenie,        repeat every 60m
```

### AlertGrouper

```go
// AlertGrouper batches related alerts within a wait window.
type AlertGrouper struct {
    keyFn      GroupKeyFunc          // GroupBySeverity | GroupByService | GroupByRule
    waitWindow time.Duration         // default 30s
    flushFn    func(ctx, *AlertGroup)
    groups     map[string]*pendingGroup
}

// GroupKeyFunc options:
//   GroupBySeverity  — "severity=critical"
//   GroupByService   — "service=order-service"
//   GroupByRule      — "rule=High error rate" (no batching)
```

### NotificationLog

```go
// Ring buffer of delivery records — newest-first on List().
type NotificationLog struct {
    mu      sync.RWMutex
    records []*NotificationRecord  // max 1000
}

type NotificationRecord struct {
    Timestamp time.Time
    Channel   string
    RuleName  string
    State     string
    Severity  string
    Status    NotificationStatus  // "sent" | "failed"
    Error     string
}
```

### Notifier Implementations

| Notifier | Transport | Auth | Payload |
|----------|-----------|------|---------|
| SlackNotifier | HTTPS POST | Webhook URL | JSON attachment with color-coded severity + fields |
| GmailNotifier | SMTP TLS (port 465) / STARTTLS (port 587) | App Password | HTML email with styled card + label table |
| PagerDutyNotifier | HTTPS POST | Integration Key | Events API v2 trigger/resolve + custom_details |
| OpsGenieNotifier | HTTPS POST | API Key header | Alerts API v2, P1–P3 priority, close on resolve |
| TeamsNotifier | HTTPS POST | Webhook URL | Adaptive Card v1.4 |
| WebhookNotifier | HTTPS POST | HMAC-SHA256 header | JSON AlertEvent + metadata |

### Gmail SMTP Dual-Mode

```
GmailNotifier.Notify():
  if SMTPPort == 465:
    tls.Dial("tcp", "smtp.gmail.com:465", tlsCfg{MinVersion: TLS12})
    smtp.NewClient(conn) → Auth → Mail → Rcpt → Data
  else (port 587, default):
    smtp.SendMail("smtp.gmail.com:587", PlainAuth, ...) // STARTTLS
```

### AlertManagerMetrics (Prometheus)

```
gosentinel_alertmanager_notifications_total{channel, status}   Counter
gosentinel_alertmanager_notification_duration_seconds{channel} Histogram
gosentinel_alertmanager_alerts_total{rule, state, severity}    Counter
gosentinel_alertmanager_silenced_total{rule}                   Counter
gosentinel_alertmanager_dedup_total{rule}                      Counter
gosentinel_alertmanager_active_alerts                          Gauge
gosentinel_alertmanager_retry_total{channel}                   Counter
```

---

## 7. QueryServer — Concurrent Fan-Out

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

## 8. HTTP Middleware Chain

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

## 9. SSE Alert Stream

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

## 10. SLO Burn Rate Calculation

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

## 11. Config Loading

```
Priority (highest to lowest):
  1. Environment variables: GOSENTINEL_OTLP_ENDPOINT
  2. Config file (--config flag): config.yaml
  3. Defaults (hardcoded in pkg/config/config.go)

Viper key mapping: "." → "_" for env vars
Example: victoria_metrics.endpoint → GOSENTINEL_VICTORIA_METRICS_ENDPOINT
```

---

## 12. Kubernetes Resource Sizing

| Component | CPU Request | CPU Limit | Mem Request | Mem Limit | Replicas |
|-----------|-------------|-----------|-------------|-----------|----------|
| pipeline  | 200m        | 500m      | 256Mi       | 512Mi     | 2-8 (HPA)|
| api       | 100m        | 500m      | 128Mi       | 512Mi     | 3-12 (HPA)|
| ui        | 50m         | 200m      | 64Mi        | 256Mi     | 2        |
| otel-collector | 100m  | 500m      | 256Mi       | 512Mi     | 1        |

---

## 13. Graceful Shutdown Sequence

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

## 14. Error Handling Conventions

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

## 15. Testing Strategy

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

---

## 16. Alert Manager REST API Reference

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/alerts` | All recent alert events (ring buffer, 500) |
| GET | `/api/v1/alerts/active` | Currently firing alerts |
| POST | `/api/v1/alerts/test` | Send a test notification |
| GET | `/api/v1/channels` | List registered notification channels |
| GET | `/api/v1/silences` | List silences |
| POST | `/api/v1/silences` | Create a silence |
| DELETE | `/api/v1/silences/{id}` | Delete a silence |
| GET | `/api/v1/notifications` | Notification delivery audit log |
| GET | `/api/v1/notifications/{channel}` | Log filtered by channel |
| GET | `/api/v1/routing` | Current per-rule routing config |
| POST | `/api/v1/routing` | Update routing for a rule at runtime |

### Test notification request body

```json
{
  "channel": "gmail",
  "rule_name": "test-alert",
  "severity": "warning",
  "summary": "GoSentinel test notification"
}
```

### Create silence request body

```json
{
  "rule_name": "High memory usage",
  "created_by": "ops-team",
  "comment": "Planned maintenance window",
  "duration": "2h"
}
```

### Update routing request body

```json
{
  "rule_name": "Service down",
  "channels": ["pagerduty", "gmail", "slack"]
}
```
