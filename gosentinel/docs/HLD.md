# GoSentinel — High-Level Design (HLD)

## 1. Executive Summary

GoSentinel is a self-hosted, full-stack observability platform for Go microservice fleets. It ingests the four pillars of observability — **metrics, distributed traces, structured logs, and continuous profiles** — correlates them on `trace_id`, detects anomalies using EWMA, evaluates SLO burn rates, fires alerts through a multi-channel alert manager, and exposes everything through a unified gRPC/REST query API, an HTMX dashboard, and a Grafana LGTM stack.

---

## 2. System Context

```
┌─────────────────────────────────────────────────────────────────────────┐
│                         External Users / Operators                       │
│              (browser, curl, Grafana, Slack, Gmail, PagerDuty)           │
└──────────────────────────────┬──────────────────────────────────────────┘
                               │ HTTPS
                               ▼
┌─────────────────────────────────────────────────────────────────────────┐
│                          GoSentinel Platform                              │
│                                                                           │
│  ┌──────────┐   ┌──────────┐   ┌──────────┐   ┌──────────────────────┐ │
│  │  UI :3000│   │ API :8080│   │ Pipeline │   │  OTel Collector      │ │
│  │  HTMX    │◄──│ gRPC+REST│◄──│  :9090   │◄──│  :4317/:4318         │ │
│  │  Alpine  │   │ ConnectRPC│  │ Streaming│   │  tail_sampling       │ │
│  └──────────┘   └──────────┘   └──────────┘   └──────────────────────┘ │
│                                      │                    │              │
│                                      │         ┌──────────┴──────────┐  │
│                                      │         │  LGTM Stack         │  │
│                                      │         │  Tempo  (traces)    │  │
│                                      │         │  Mimir  (metrics)   │  │
│                                      │         │  Loki   (logs)      │  │
│                                      │         │  Grafana (dashboards│  │
│                                      │         └─────────────────────┘  │
│                                      ▼                    ▲              │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌────────────┴───────────┐  │
│  │  Jaeger  │  │Victoria  │  │   Loki   │  │  Pyroscope             │  │
│  │ (compat) │  │ Metrics  │  │  (logs)  │  │  Profiles              │  │
│  └──────────┘  └──────────┘  └──────────┘  └────────────────────────┘  │
│                                                                           │
│  ┌──────────────────────────────────────────────────────────────────┐   │
│  │  Instrumented Microservices (OTel SDK + pprof + slog + Pyroscope)│   │
│  │  order-service :8081  inventory-service :8082  payment-service :8083 │
│  └──────────────────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────────────────┘
```

---

## 3. Architecture Principles

| Principle | Implementation |
|-----------|---------------|
| **Separation of concerns** | Four independent binaries: collector, pipeline, api, ui |
| **Pull-based metrics** | Prometheus scrape model via `/metrics` endpoints |
| **Push-based traces/logs** | OTLP gRPC push to OTel Collector |
| **Tail-based sampling** | Sample after full trace is assembled (error/latency/probabilistic) |
| **Signal correlation** | Join traces + metrics + logs on `trace_id` within 30s window |
| **Anomaly detection** | EWMA 3σ detector per metric series, no ML dependency |
| **SLO-driven alerting** | Google SRE Book multi-window burn rate (1h/6h/3d windows) |
| **LGTM observability** | Grafana LGTM stack (Loki+Grafana+Tempo+Mimir) via OpenTelemetry |
| **Zero-trust networking** | NetworkPolicy default-deny + explicit allow |
| **Least-privilege IRSA** | Per-workload IAM roles via OIDC federation |
| **GitOps-ready** | All config in YAML/Terraform, no manual kubectl |

---

## 4. Component Architecture

### 4.1 OTel Collector (contrib 0.102)

Receives OTLP from all services and fans out to all backends:

```
OTLP gRPC/HTTP ──► memory_limiter ──► resource ──► batch ──► tail_sampling
                                                                    │
                                          ┌─────────────────────────┤
                                          ▼                         ▼
                                   Traces pipeline           Metrics pipeline
                                          │                         │
                              ┌───────────┤              ┌──────────┤
                              ▼           ▼              ▼          ▼
                           Tempo       Jaeger          Mimir    VictoriaMetrics
                         (primary)   (compat)        (primary)   (compat)

                                   Logs pipeline
                                          │
                                          ▼
                                        Loki
```

- Scrapes Prometheus `/metrics` from GoSentinel pipeline and API
- Tail sampling policies: always-sample-errors, high-latency (>500ms), probabilistic (10%)
- Resource processor adds `deployment.environment` and `k8s.cluster.name` attributes

### 4.2 Grafana LGTM Stack

The LGTM stack provides unified observability dashboards with native signal correlation:

| Component | Role | Port | Version |
|-----------|------|------|---------|
| **Loki** | Log aggregation | 3100 | 3.0.0 |
| **Grafana** | Unified dashboards | 3000 | 11.0.0 |
| **Tempo** | Distributed tracing | 3200, 4317 | 2.4.1 |
| **Mimir** | Long-term metrics | 9009 | 2.12.0 |

Grafana pre-provisioned datasources:
- **Mimir** (default) — primary metrics with Prometheus-compatible query API
- **Loki** — logs with derived field linking `trace_id` → Tempo
- **Tempo** — traces with service map, span metrics, trace-to-log correlation
- **VictoriaMetrics** — GoSentinel pipeline metrics (compat)
- **Jaeger** — trace queries (compat)
- **Pyroscope** — continuous profiling

Tempo metrics generator pushes span metrics and service graph metrics to Mimir, enabling:
- `traces_spanmetrics_calls_total` — request rate derived from traces
- `traces_spanmetrics_duration_seconds` — latency derived from traces
- Service dependency graph in Grafana Explore

### 4.3 GoSentinel Pipeline

The core streaming engine. Runs six concurrent subsystems:

```
OTLP Receiver ──► TailSampler ──► Jaeger/Tempo exporter
                       │
                       ▼
              CorrelationEngine ──► CorrelatedEvent channel
              (trace+metric+log join on trace_id, 30s window)
                       │
                       ▼
              DetectorRegistry ──► AnomalyEvent channel ──► AlertManager
              (EWMA 3σ per metric series)                        │
                       │                                         │
                       ▼                                         │
              RuleEvaluator ──────────────────────────────────► AlertManager
              (MetricsQL rules, PostgreSQL state)                │
                       │                                         ▼
                       │                          EscalationPolicy (time-based)
                       │                                         │
                       │                          RoutingConfig (per-rule channels)
                       │                                         │
                       │              ┌──────────────────────────┼──────────────────┐
                       │              ▼              ▼           ▼                  ▼
                       │           Slack           Gmail    PagerDuty          OpsGenie
                       │           Teams         Webhook
                       │
                       ▼
              SLOTracker ──► BurnRateViolation alerts
              (multi-window: 1h/6h/3d)
```

### 4.3.1 AlertManager — End-to-End Notification Flow

```
AlertEvent (from RuleEvaluator or EWMA anomaly)
    │
    ├─► AlertStore.Record()           always — history ring buffer (500 events)
    │
    ├─► Prometheus metrics update     gosentinel_alertmanager_alerts_total
    │
    ├─► SilenceManager.IsSilenced()   drop if rule is silenced
    │       └─► gosentinel_alertmanager_silenced_total++
    │
    ├─► Deduplication check           drop if same (rule, state) within dedupTTL
    │       └─► gosentinel_alertmanager_dedup_total++
    │
    ├─► EscalationPolicy.ChannelsFor()  time-based channel escalation
    │       ├── Level 0 (immediate):  slack + gmail        (repeat: 10m)
    │       ├── Level 1 (after 5m):   + pagerduty          (repeat: 30m)
    │       └── Level 2 (after 15m):  + opsgenie           (repeat: 60m)
    │
    ├─► RoutingConfig.ChannelsFor()   per-rule static override (fallback)
    │
    └─► Fan-out (goroutine per channel)
            │
            ├─► sendWithRetry()       exponential back-off (2s → 4s → 30s cap, 3 attempts)
            │       ├─► SlackNotifier.Notify()       rich attachment with severity fields
            │       ├─► GmailNotifier.Notify()       HTML email, TLS/STARTTLS dual-mode
            │       ├─► PagerDutyNotifier.Notify()   Events API v2 trigger/resolve
            │       ├─► OpsGenieNotifier.Notify()    Alerts API v2, P1–P3 priority
            │       ├─► TeamsNotifier.Notify()       Adaptive Card v1.4
            │       └─► WebhookNotifier.Notify()     HMAC-SHA256 signed JSON POST
            │
            ├─► NotificationLog.Record()             audit trail (1000 records)
            └─► gosentinel_alertmanager_notifications_total{channel, status}
                gosentinel_alertmanager_notification_duration_seconds{channel}
```

### 4.3.2 Alert Grouping

`AlertGrouper` batches related alerts within a configurable wait window (default 30s)
before dispatching, reducing notification noise during correlated failure cascades.

```
AlertEvent ──► AlertGrouper.Add()
                    │
                    ├── GroupBySeverity  — batch by severity level
                    ├── GroupByService   — batch by originating service
                    └── GroupByRule      — one notification per rule (default)
                    │
                    └── timer.AfterFunc(waitWindow) ──► flushFn(group)
                                                              │
                                                              └─► Notifier.Notify(representative)
```

### 4.4 GoSentinel API

- ConnectRPC server (gRPC + HTTP/JSON on same port 8080)
- `GetServiceHealth`: concurrent fan-out to VM + Jaeger + Pyroscope via `errgroup`
- `StreamAlerts`: server-streaming RPC, broadcast pattern with `sync.Map` subscribers
- Middleware: JWT auth → rate limiter (token bucket) → OTel tracing → request logging
- Alert management REST API:

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/alerts` | Full alert history (ring buffer, 500 events) |
| GET | `/api/v1/alerts/active` | Currently firing alerts |
| POST | `/api/v1/alerts/test` | Send a test notification to one or all channels |
| GET | `/api/v1/channels` | List registered notification channels |
| GET/POST | `/api/v1/silences` | Manage alert silences |
| DELETE | `/api/v1/silences/{id}` | Remove a silence |
| GET | `/api/v1/notifications` | Notification delivery audit log |
| GET | `/api/v1/notifications/{channel}` | Log filtered by channel name |
| GET | `/api/v1/routing` | Current per-rule channel routing |
| POST | `/api/v1/routing` | Update routing for a rule at runtime |

### 4.5 GoSentinel UI

- Go HTTP server with chi router
- HTMX partials auto-refresh (service table: 15s, panels: 30s)
- SSE `/sse/alerts` for real-time alert banner (Alpine.js EventSource)
- Reverse-proxies `/api/v1/*` to the API server (avoids CORS)
- Templates embedded via `go:embed`

---

## 5. Observability Patterns Implemented

### 5.1 RED Method (per service endpoint)
- **Rate**: `http_requests_total` counter
- **Errors**: `http_errors_total` counter + `status=~"5.."` label
- **Duration**: `http_request_duration_seconds` histogram (p50/p95/p99)

### 5.2 USE Method (per resource)
- **Utilization**: CPU fraction, heap fraction
- **Saturation**: goroutine count, queue depth, in-flight requests
- **Errors**: GC count, GC pause time, crash restarts

### 5.3 Four Golden Signals (Google SRE)
- **Latency**: histogram_quantile(0.99, ...)
- **Traffic**: rate(http_requests_total[5m])
- **Errors**: rate(5xx) / rate(total)
- **Saturation**: CPU utilization, memory utilization

### 5.4 SLI/SLO/Error Budget
- SLI: availability = good_requests / total_requests
- SLO: 99.9% availability over 30-day rolling window
- Error budget: 1 - SLO target = 0.1% = 43.2 min/month
- Multi-window burn rate alerts (Google SRE Book Chapter 5)

### 5.5 Distributed Tracing
- W3C TraceContext + Baggage propagation across all service calls
- Trace-log correlation: `trace_id` + `span_id` in every log line
- Tail-based sampling: keep errors, high-latency, 10% probabilistic
- Traces stored in Tempo (primary) and Jaeger (compat)

### 5.6 Continuous Profiling
- Pyroscope Go SDK: CPU, goroutine, alloc profiles
- Correlated with traces via time range in Pyroscope deep links
- Top CPU function surfaced in `GetServiceHealth` response

### 5.7 Anomaly Detection
- EWMA (Exponentially Weighted Moving Average) per metric series
- 3σ threshold: flag values > 3 standard deviations from EWMA mean
- Warm-up period: first 10 observations excluded
- Severity: warning (3σ), critical (6σ)
- Anomalies forwarded to AlertManager as synthetic alert events

### 5.8 Alert Manager Observability

The AlertManager itself is instrumented with Prometheus metrics:

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `gosentinel_alertmanager_notifications_total` | Counter | channel, status | Delivery attempts |
| `gosentinel_alertmanager_notification_duration_seconds` | Histogram | channel | Delivery latency |
| `gosentinel_alertmanager_alerts_total` | Counter | rule, state, severity | Events processed |
| `gosentinel_alertmanager_silenced_total` | Counter | rule | Suppressed by silence |
| `gosentinel_alertmanager_dedup_total` | Counter | rule | Suppressed by dedup |
| `gosentinel_alertmanager_active_alerts` | Gauge | — | Currently firing |
| `gosentinel_alertmanager_retry_total` | Counter | channel | Retry attempts |

---

## 6. Data Flow

```
Service ──OTLP──► OTel Collector
                       │
                       ├── Traces  ──► Tempo (primary LGTM) + Jaeger (compat)
                       ├── Metrics ──► Mimir (primary LGTM) + VictoriaMetrics (compat)
                       └── Logs    ──► Loki
                       │
                       └── Prometheus scrape ──► GoSentinel pipeline + API metrics

Pipeline ──reads──► VictoriaMetrics (MetricsQL rule evaluation)
         ──joins──► CorrelationEngine (trace_id window join)
         ──detects──► DetectorRegistry (EWMA anomaly) ──► AlertManager
         ──evaluates──► RuleEvaluator (MetricsQL rules) ──► AlertManager
         ──tracks──► SLOTracker (burn rate)

AlertManager ──► EscalationPolicy ──► RoutingConfig ──► Notifiers
             ──► AlertStore (history ring buffer)
             ──► NotificationLog (audit ring buffer)
             ──► Prometheus metrics

API ──fan-out──► VictoriaMetrics + Jaeger + Pyroscope + Loki
    ──streams──► Alert subscribers (broadcast channel)
    ──REST──► AlertManager (silences, routing, history, test)

UI ──HTMX──► API (partials, proxy)
   ──SSE──► Pipeline (alert stream)

Grafana ──► Mimir (metrics)
        ──► Loki (logs + trace correlation)
        ──► Tempo (traces + service map + span metrics)
        ──► Pyroscope (profiles)
        ──► VictoriaMetrics (GoSentinel metrics)
        ──► Jaeger (trace queries)
```

---

## 7. Deployment Environments

| Environment | Command | Backends | Notes |
|-------------|---------|----------|-------|
| Local dev | `make docker-up` | Docker Compose | All backends in containers |
| Minikube | `make minikube-up` | LGTM + compat | No registry needed, images built in-cluster |
| Production (EKS) | `make helm-install` | Managed (RDS, ECR) | HPA, PDB, IRSA, NetworkPolicy |

### Minikube resource requirements

| Resource | Minimum | Recommended |
|----------|---------|-------------|
| CPUs | 4 | 6 |
| Memory | 6 GB | 8 GB |
| Disk | 20 GB | 30 GB |

---

## 8. Infrastructure Architecture (AWS EKS)

```
AWS Account
├── VPC (10.0.0.0/16)
│   ├── Public Subnets (3 AZs) — NAT Gateways, Load Balancers
│   ├── Private Subnets (3 AZs) — EKS Worker Nodes
│   └── Intra Subnets (3 AZs) — RDS PostgreSQL
│
├── EKS Cluster (gosentinel-prod)
│   ├── System Node Group (t3.medium × 2) — CoreDNS, kube-proxy
│   ├── GoSentinel Node Group (m5.xlarge × 3-10) — workloads
│   ├── Add-ons: CoreDNS, kube-proxy, VPC CNI, EBS CSI
│   └── OIDC Provider (for IRSA)
│
├── RDS PostgreSQL 16 (Multi-AZ, encrypted, Performance Insights)
├── ECR Repositories (6 repos, immutable tags, scan on push)
├── KMS Keys (EKS secrets, RDS encryption)
├── IAM Roles (IRSA per workload, least-privilege)
└── VPC Flow Logs → CloudWatch Logs
```

---

## 9. Security Design

| Layer | Control |
|-------|---------|
| Network | VPC private subnets, NetworkPolicy default-deny, Security Groups |
| Identity | IRSA (no long-lived credentials), JWT for API auth |
| Secrets | AWS Secrets Manager, K8s Secrets (base64, not plaintext in git) |
| Container | Non-root UID 65534, readOnlyRootFilesystem, drop ALL capabilities, seccomp RuntimeDefault |
| Data | RDS encryption at rest (KMS), TLS in transit, EKS envelope encryption |
| Audit | VPC Flow Logs, EKS audit logs, CloudTrail, NotificationLog |
| Webhooks | HMAC-SHA256 signature on outbound webhook payloads (X-GoSentinel-Signature) |
| Gmail | App Password (not account password), TLS 1.2+ enforced |

---

## 10. Scalability Design

| Component | Scaling Strategy |
|-----------|-----------------|
| Pipeline | HPA on CPU (2-8 replicas), stateless (ristretto in-process) |
| API | HPA on CPU (3-12 replicas), stateless |
| UI | Fixed 2 replicas (low resource) |
| VictoriaMetrics | Single-node for dev, VMCluster for prod |
| Mimir | Single-binary for Minikube, microservices mode for prod |
| Loki | Monolithic for dev, microservices mode for prod |
| Tempo | Single-binary for Minikube, distributed for prod |
| Jaeger | all-in-one for dev, distributed for prod |

---

## 11. Technology Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Metrics storage | VictoriaMetrics + Mimir | VM for GoSentinel queries (MetricsQL); Mimir for LGTM unified dashboards |
| Trace storage | Jaeger + Tempo | Jaeger for GoSentinel API; Tempo for LGTM trace-to-log-to-metric correlation |
| Log storage | Loki | Label-based, integrates with Grafana, low cost |
| Profile storage | Pyroscope | Continuous profiling, Grafana integration |
| Observability stack | Grafana LGTM | Native signal correlation, service map, span metrics, single pane of glass |
| OTel Collector | contrib 0.102 | Tail sampling, multi-backend fan-out, Prometheus scraping |
| API protocol | ConnectRPC | gRPC + HTTP/JSON on same port, no proxy needed |
| Frontend | HTMX + Alpine.js | No build step, Go templates, SSE-native |
| Anomaly detection | EWMA | No ML dependency, O(1) memory, configurable sensitivity |
| Sampling | Tail-based | See all spans before deciding, keeps errors/slow traces |
| Alert notifications | AlertManager fan-out | Slack, Gmail (SMTP), PagerDuty, OpsGenie, Teams, Webhook — concurrent with retry |
| Alert escalation | EscalationPolicy | Time-based channel escalation (immediate → 5m → 15m) |
| Alert grouping | AlertGrouper | Batch correlated alerts within 30s window to reduce noise |
| Alert routing | RoutingConfig | Per-rule channel selection loaded from YAML, updatable at runtime via REST |
| Notification audit | NotificationLog | In-memory ring buffer (1000 records) of every delivery attempt |
| Alert metrics | AlertManagerMetrics | Prometheus counters/histograms for notification pipeline observability |

---

## 12. Architecture Decision Records

| ADR | Title | Status |
|-----|-------|--------|
| [ADR-001](adr/001-tail-sampling-in-process.md) | Tail sampling in-process | Accepted |
| [ADR-002](adr/002-alertmanager-design.md) | AlertManager multi-channel design | Accepted |
| [ADR-003](adr/003-ewma-anomaly-detection.md) | EWMA anomaly detection | Accepted |
| [ADR-004](adr/004-lgtm-stack.md) | Grafana LGTM stack for observability | Accepted |
