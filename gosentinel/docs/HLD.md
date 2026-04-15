# GoSentinel — High-Level Design (HLD)

## 1. Executive Summary

GoSentinel is a self-hosted, full-stack observability platform for Go microservice fleets. It ingests the four pillars of observability — **metrics, distributed traces, structured logs, and continuous profiles** — correlates them on `trace_id`, detects anomalies using EWMA, evaluates SLO burn rates, fires alerts, and exposes everything through a unified gRPC/REST query API and an HTMX dashboard.

---

## 2. System Context

```
┌─────────────────────────────────────────────────────────────────────────┐
│                         External Users / Operators                       │
│                    (browser, curl, Grafana, PagerDuty)                   │
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
│                                      │                    ▲              │
│                                      ▼                    │              │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌────────────┴───────────┐  │
│  │  Jaeger  │  │Victoria  │  │   Loki   │  │  Pyroscope             │  │
│  │  Traces  │  │ Metrics  │  │   Logs   │  │  Profiles              │  │
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
| **Zero-trust networking** | NetworkPolicy default-deny + explicit allow |
| **Least-privilege IRSA** | Per-workload IAM roles via OIDC federation |
| **GitOps-ready** | All config in YAML/Terraform, no manual kubectl |

---

## 4. Component Architecture

### 4.1 OTel Collector
- Receives OTLP gRPC (4317) and HTTP (4318) from all services
- Applies: `memory_limiter` → `batch` → `tail_sampling`
- Exports: traces → Jaeger, metrics → VictoriaMetrics, logs → Loki
- Scrapes Prometheus `/metrics` endpoints from all services

### 4.2 GoSentinel Pipeline
The core streaming engine. Runs five concurrent subsystems:

```
OTLP Receiver ──► TailSampler ──► Jaeger exporter
                       │
                       ▼
              CorrelationEngine ──► CorrelatedEvent channel
              (trace+metric+log join on trace_id, 30s window)
                       │
                       ▼
              DetectorRegistry ──► AnomalyEvent channel
              (EWMA 3σ per metric series)
                       │
                       ▼
              RuleEvaluator ──► Notifiers (Slack, PagerDuty)
              (MetricsQL rules, PostgreSQL state)
                       │
                       ▼
              SLOTracker ──► BurnRateViolation alerts
              (multi-window: 1h/6h/3d)
```

### 4.3 GoSentinel API
- ConnectRPC server (gRPC + HTTP/JSON on same port 8080)
- `GetServiceHealth`: concurrent fan-out to VM + Jaeger + Pyroscope via `errgroup`
- `StreamAlerts`: server-streaming RPC, broadcast pattern with `sync.Map` subscribers
- Middleware: JWT auth → rate limiter (token bucket) → OTel tracing → request logging

### 4.4 GoSentinel UI
- Go HTTP server with chi router
- HTMX partials auto-refresh (service table: 15s, panels: 30s)
- SSE `/sse/alerts` for real-time alert banner (Alpine.js EventSource)
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

### 5.6 Continuous Profiling
- Pyroscope Go SDK: CPU, goroutine, alloc profiles
- Correlated with traces via time range in Pyroscope deep links
- Top CPU function surfaced in `GetServiceHealth` response

### 5.7 Anomaly Detection
- EWMA (Exponentially Weighted Moving Average) per metric series
- 3σ threshold: flag values > 3 standard deviations from EWMA mean
- Warm-up period: first 10 observations excluded
- Severity: warning (3σ), critical (6σ)

---

## 6. Data Flow

```
Service ──OTLP──► OTel Collector ──► Jaeger (traces)
                                 ──► VictoriaMetrics (metrics)
                                 ──► Loki (logs)
                                 ──► Pyroscope (profiles, direct push)

Pipeline ──reads──► VictoriaMetrics (streaming metric points)
         ──reads──► Loki (WebSocket tail)
         ──reads──► Jaeger (span stream)
         ──joins──► CorrelationEngine (trace_id window join)
         ──detects──► DetectorRegistry (EWMA anomaly)
         ──evaluates──► RuleEvaluator (MetricsQL rules)
         ──tracks──► SLOTracker (burn rate)

API ──fan-out──► VictoriaMetrics + Jaeger + Pyroscope + Loki
    ──streams──► Alert subscribers (broadcast channel)

UI ──HTMX──► API (partials)
   ──SSE──► Pipeline (alert stream)
```

---

## 7. Infrastructure Architecture (AWS EKS)

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

## 8. Security Design

| Layer | Control |
|-------|---------|
| Network | VPC private subnets, NetworkPolicy default-deny, Security Groups |
| Identity | IRSA (no long-lived credentials), JWT for API auth |
| Secrets | AWS Secrets Manager, K8s Secrets (base64, not plaintext in git) |
| Container | Non-root UID 65534, readOnlyRootFilesystem, drop ALL capabilities, seccomp RuntimeDefault |
| Data | RDS encryption at rest (KMS), TLS in transit, EKS envelope encryption |
| Audit | VPC Flow Logs, EKS audit logs, CloudTrail |

---

## 9. Scalability Design

| Component | Scaling Strategy |
|-----------|-----------------|
| Pipeline | HPA on CPU (2-8 replicas), stateless (ristretto in-process) |
| API | HPA on CPU (3-12 replicas), stateless |
| UI | Fixed 2 replicas (low resource) |
| VictoriaMetrics | Single-node for dev, VMCluster for prod |
| Loki | Monolithic for dev, microservices mode for prod |
| Jaeger | all-in-one for dev, distributed for prod |

---

## 10. Technology Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Metrics storage | VictoriaMetrics | 10x more efficient than Prometheus, MetricsQL superset |
| Trace storage | Jaeger | CNCF standard, gRPC query API |
| Log storage | Loki | Label-based, integrates with Grafana, low cost |
| Profile storage | Pyroscope | Continuous profiling, Grafana integration |
| API protocol | ConnectRPC | gRPC + HTTP/JSON on same port, no proxy needed |
| Frontend | HTMX + Alpine.js | No build step, Go templates, SSE-native |
| Anomaly detection | EWMA | No ML dependency, O(1) memory, configurable sensitivity |
| Sampling | Tail-based | See all spans before deciding, keeps errors/slow traces |
