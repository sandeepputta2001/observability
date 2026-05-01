# GoSentinel

A self-hosted, full-stack observability platform for Go microservice fleets.
Collects metrics, distributed traces, structured logs, and continuous profiles —
correlates them, detects anomalies, evaluates SLOs, fires alerts, and exposes
everything through a unified query API, live dashboard, and Grafana LGTM stack.

```
┌─────────────────────────────────────────────────────────────────┐
│  order-service  inventory-service  payment-service               │
│  (OTel SDK + pprof + slog + Pyroscope)                          │
└────────────────────────┬────────────────────────────────────────┘
                         │ OTLP gRPC
                         ▼
              ┌─────────────────────┐
              │   OTel Collector    │  tail sampling + fan-out
              └──┬──────┬──────┬───┘
         Tempo/  │  Mimir│  Loki│   ← LGTM stack (primary)
         Jaeger  │  /VM  │      │   ← compat backends
                 ▼      ▼      ▼
         ┌──────────────────────────┐
         │   GoSentinel Pipeline    │  correlation + anomaly + SLO + alerts
         └──────────────────────────┘
                         │
              ┌──────────┴──────────┐
              │  GoSentinel API     │  gRPC + HTTP/JSON (ConnectRPC)
              └──────────┬──────────┘
         ┌───────────────┴───────────────┐
         │                               │
┌────────┴────────┐             ┌────────┴────────┐
│  GoSentinel UI  │             │     Grafana      │
│  HTMX + SSE     │             │  LGTM dashboards │
└─────────────────┘             └─────────────────┘
```

---

## Features

- **RED metrics** — Rate, Errors, Duration per endpoint
- **USE metrics** — Utilization, Saturation, Errors per resource
- **Four Golden Signals** — Latency, Traffic, Errors, Saturation
- **Distributed tracing** — W3C TraceContext, tail-based sampling
- **Trace-log correlation** — `trace_id` in every log line
- **Continuous profiling** — CPU, goroutine, alloc via Pyroscope
- **EWMA anomaly detection** — 3σ alerting, no ML dependency; anomalies forwarded to AlertManager
- **SLO burn rate** — Google SRE Book multi-window (1h/6h/3d)
- **Grafana LGTM stack** — Loki + Grafana + Tempo + Mimir via OpenTelemetry; trace↔log↔metric correlation
- **Alert routing** — per-rule channel selection (Slack, Gmail, PagerDuty, OpsGenie, Teams, Webhook)
- **Escalation policy** — time-based channel escalation (immediate → 5 min → 15 min)
- **Alert grouping** — batch correlated alerts within 30 s window to reduce noise
- **Notification audit log** — every delivery attempt recorded with status and error
- **Alert manager metrics** — Prometheus counters/histograms for notification pipeline
- **Test notifications** — fire a test alert to any channel via REST API
- **Alert silences** — mute noisy rules with time-bounded silences
- **Live dashboard** — HTMX auto-refresh, SSE real-time alerts
- **Minikube deploy** — one-command local Kubernetes with full LGTM stack
- **Production K8s** — HPA, PDB, TopologySpread, NetworkPolicy, IRSA
- **Terraform EKS** — VPC, EKS, RDS, ECR, IAM modules

---

## Prerequisites

| Tool | Version | Purpose |
|------|---------|---------|
| Go | 1.22+ | Build |
| Docker + Compose | 24+ | Local dev |
| minikube | 1.30+ | Local Kubernetes |
| kubectl | 1.28+ | K8s management |
| Helm | 3.14+ | K8s packaging (optional) |
| Terraform | 1.7+ | Infrastructure (optional) |
| AWS CLI | 2.x | ECR push, EKS auth (optional) |
| buf | 1.x | Proto generation (optional) |
| golangci-lint | 1.57+ | Linting (optional) |

---

## Quick Start — Local Dev

### 1. Clone and start all backends

```bash
git clone https://github.com/yourorg/gosentinel
cd gosentinel
make docker-up
```

This starts: Jaeger, VictoriaMetrics, Loki, Pyroscope, Prometheus, Grafana,
PostgreSQL, OTel Collector, and all three example services.

### 2. Run the database migrations

```bash
export GOSENTINEL_POSTGRES_DSN="postgres://gosentinel:gosentinel@localhost:5432/gosentinel?sslmode=disable"
make db-migrate
```

### 3. Start GoSentinel binaries

In separate terminals:

```bash
# Pipeline (correlation + anomaly + alerting)
go run ./cmd/pipeline/

# Query API (gRPC + REST on :8080)
go run ./cmd/api/

# UI dashboard (HTMX on :3000)
go run ./cmd/ui/
```

### 4. Verify everything works

```bash
# Health checks
curl http://localhost:9090/health   # pipeline
curl http://localhost:8080/health   # api
curl http://localhost:3000/         # ui (browser)

# Query service health via ConnectRPC HTTP/JSON
curl -X POST http://localhost:8080/gosentinel.v1.QueryService/GetServiceHealth \
  -H "Content-Type: application/json" \
  -d '{"service": "order-service", "lookback_minutes": 60}'

# Generate some traffic
for i in $(seq 1 20); do
  curl -s http://localhost:8081/orders > /dev/null
  curl -s http://localhost:8082/inventory/check > /dev/null
done
```

### 5. Open dashboards

| Service | URL | Credentials |
|---------|-----|-------------|
| GoSentinel UI | http://localhost:3000 | — |
| Grafana | http://localhost:3001 | admin/admin |
| Jaeger UI | http://localhost:16686 | — |
| Prometheus | http://localhost:9090 | — |
| Pyroscope | http://localhost:4040 | — |
| VictoriaMetrics | http://localhost:8428 | — |

---

## Build

```bash
# Build all four binaries into ./bin/
make build

# Run all tests with race detector
make test

# Run only alerting tests
make test-alerting

# Generate coverage report
make coverage

# Lint
make lint

# Build Docker images
make docker-build IMAGE_TAG=v0.1.0

# Show all available targets
make help
```

---

## Minikube Deployment (Local Kubernetes)

Deploy the full stack — LGTM observability (Loki, Grafana, Tempo, Mimir) + OpenTelemetry Collector + GoSentinel — on a local Minikube cluster with a single command.

### Requirements

| Tool | Version | Install |
|------|---------|---------|
| minikube | ≥ 1.30 | https://minikube.sigs.k8s.io/docs/start/ |
| kubectl | ≥ 1.28 | https://kubernetes.io/docs/tasks/tools/ |
| docker | ≥ 24 | https://docs.docker.com/get-docker/ |

### Resource requirements

The LGTM stack is memory-hungry. Minikube needs at least:

| Resource | Minimum | Recommended |
|----------|---------|-------------|
| CPUs | 4 | 6 |
| Memory | 6 GB | 8 GB |
| Disk | 20 GB | 30 GB |

If you have more resources, pass them as env vars:
```bash
MINIKUBE_CPUS=6 MINIKUBE_MEMORY=8192 make minikube-up
```

### One-command deploy

```bash
cd gosentinel
make minikube-up
```

This will:
1. Start Minikube (4 CPUs, 6 GB RAM, docker driver)
2. Build `gosentinel/pipeline:dev`, `gosentinel/api:dev`, `gosentinel/ui:dev` inside Minikube's Docker daemon (no registry needed)
3. Deploy in order: namespace → config/secrets → PostgreSQL → LGTM stack → OTel Collector → GoSentinel
4. Wait for each component to be ready
5. Print all access URLs

### What gets deployed

```
gosentinel namespace
├── Infrastructure
│   ├── postgres          — alert state persistence
│   └── otel-collector    — OTLP receiver, fan-out to all backends
│
├── LGTM Stack (Grafana Observability)
│   ├── loki              — logs (port 3100)
│   ├── tempo             — traces via OTLP (port 3200, 4317)
│   ├── mimir             — metrics via remote_write (port 9009)
│   ├── grafana           — dashboards (port 3000) — admin/gosentinel
│   │
│   └── (compatibility backends)
│       ├── victoriametrics — GoSentinel pipeline queries (port 8428)
│       ├── jaeger          — GoSentinel API trace queries (port 16686)
│       └── pyroscope       — continuous profiling (port 4040)
│
└── GoSentinel Application
    ├── gosentinel-pipeline — correlation + anomaly + alerting (port 9090)
    ├── gosentinel-api      — gRPC + REST query API (port 8080)
    └── gosentinel-ui       — HTMX dashboard (port 3000)
```

### OpenTelemetry signal routing

```
Services (OTLP) ──► otel-collector
                         │
                         ├── Traces  ──► Tempo (primary) + Jaeger (compat)
                         ├── Metrics ──► Mimir (primary) + VictoriaMetrics (compat)
                         └── Logs    ──► Loki
```

### Access the services

After `make minikube-up` completes, use any of these methods:

**Option A — minikube service (opens browser):**
```bash
make minikube-grafana    # Grafana dashboards
make minikube-ui         # GoSentinel UI
```

**Option B — port-forward everything to localhost:**
```bash
make minikube-pf
# Then open:
#   http://localhost:3001  — Grafana (admin/gosentinel)
#   http://localhost:3000  — GoSentinel UI
#   http://localhost:8080  — GoSentinel API
#   http://localhost:16686 — Jaeger UI
#   http://localhost:3200  — Tempo
#   http://localhost:9009  — Mimir
```

**Option C — get URLs directly:**
```bash
minikube service gosentinel-ui -n gosentinel --url
minikube service grafana -n gosentinel --url
```

### Grafana datasources (pre-configured)

| Datasource | Type | Backend |
|------------|------|---------|
| Mimir | Prometheus | Primary metrics (LGTM) |
| VictoriaMetrics | Prometheus | GoSentinel pipeline metrics |
| Loki | Loki | Logs with trace correlation |
| Tempo | Tempo | Traces with service map + metrics |
| Jaeger | Jaeger | Trace queries (compat) |
| Pyroscope | Pyroscope | Continuous profiling |

### Configure alert channels

Edit the Secret in `deploy/k8s/minikube/01-configmap.yaml` before deploying, or patch after:

```bash
# Gmail
kubectl patch secret gosentinel-secrets -n gosentinel \
  --type='json' \
  -p='[
    {"op":"replace","path":"/stringData/gmail_username","value":"alerts@yourdomain.com"},
    {"op":"replace","path":"/stringData/gmail_password","value":"xxxx-xxxx-xxxx-xxxx"},
    {"op":"replace","path":"/stringData/gmail_to","value":"oncall@yourdomain.com"}
  ]'

# Slack
kubectl patch secret gosentinel-secrets -n gosentinel \
  --type='json' \
  -p='[{"op":"replace","path":"/stringData/slack_webhook","value":"https://hooks.slack.com/..."}]'

# Restart pipeline to pick up new secrets
kubectl rollout restart deployment/gosentinel-pipeline -n gosentinel
```

### Test alert notifications

```bash
make minikube-test-alert
# or manually:
API_URL=$(minikube service gosentinel-api -n gosentinel --url)
curl -X POST $API_URL/api/v1/alerts/test \
  -H "Content-Type: application/json" \
  -d '{"channel":"slack","severity":"critical","summary":"Test from Minikube"}'
```

### Rebuild after code changes

```bash
make minikube-rebuild
```

### Useful commands

```bash
make minikube-status            # show all pods/services/PVCs
make minikube-logs-pipeline     # tail pipeline logs
make minikube-logs-otel         # tail OTel collector logs
make minikube-logs-all          # tail all component logs simultaneously
make minikube-channels          # list registered notification channels
kubectl get pods -n gosentinel -w   # watch pod status
```

### Tear down

```bash
make minikube-down        # delete namespace only (keeps cluster)
make minikube-delete      # delete entire Minikube cluster
```

---

## Kubernetes Deployment (Production / EKS)

### Option A — Raw manifests

```bash
# Set your cluster context
kubectl config use-context my-cluster

# Apply everything
make k8s-apply

# Check status
make k8s-status

# Tail pipeline logs
make k8s-logs
```

### Option B — Helm

```bash
# Install
make helm-install IMAGE_TAG=v0.1.0

# Upgrade
helm upgrade gosentinel deploy/helm/gosentinel \
  --namespace gosentinel \
  --set pipeline.image.tag=v0.1.1 \
  --wait

# Uninstall
make helm-uninstall
```

### Customising values

Edit `deploy/helm/gosentinel/values.yaml` or pass `--set` flags:

```bash
helm upgrade --install gosentinel deploy/helm/gosentinel \
  --set api.replicaCount=5 \
  --set backends.victoriaMetricsEndpoint=http://my-vm:8428 \
  --set alerting.slackWebhook=https://hooks.slack.com/... \
  --set alerting.gmailUsername=alerts@yourdomain.com \
  --set alerting.gmailPassword=xxxx-xxxx-xxxx-xxxx \
  --set alerting.gmailTo=oncall@yourdomain.com

# Validate chart before installing
make helm-lint
make helm-template
```

---

## AWS EKS Deployment (Terraform)

### 1. Bootstrap Terraform state backend

```bash
# Create S3 bucket and DynamoDB table for state locking
aws s3 mb s3://gosentinel-terraform-state-prod --region us-east-1
aws s3api put-bucket-versioning \
  --bucket gosentinel-terraform-state-prod \
  --versioning-configuration Status=Enabled
aws dynamodb create-table \
  --table-name gosentinel-terraform-locks \
  --attribute-definitions AttributeName=LockID,AttributeType=S \
  --key-schema AttributeName=LockID,KeyType=HASH \
  --billing-mode PAY_PER_REQUEST \
  --region us-east-1
```

### 2. Initialise and apply

```bash
# Dev environment
make tf-init TF_ENV=dev
make tf-plan TF_ENV=dev
make tf-apply TF_ENV=dev

# Production environment
make tf-init TF_ENV=prod
make tf-plan TF_ENV=prod
make tf-apply TF_ENV=prod
```

### 3. Configure kubectl

```bash
make kubeconfig CLUSTER=gosentinel-prod AWS_REGION=us-east-1
# or
aws eks update-kubeconfig --region us-east-1 --name gosentinel-prod
```

### 4. Push images to ECR and deploy

```bash
make docker-push IMAGE_TAG=v0.1.0 AWS_REGION=us-east-1
make helm-install IMAGE_TAG=v0.1.0
```

---

## Configuration Reference

All configuration is loaded via Viper. Environment variables take precedence
over the config file. Prefix all env vars with `GOSENTINEL_`.

| Key | Env Var | Default | Description |
|-----|---------|---------|-------------|
| `otlp_endpoint` | `GOSENTINEL_OTLP_ENDPOINT` | `localhost:4317` | OTel Collector gRPC endpoint |
| `pipeline.listen_addr` | `GOSENTINEL_PIPELINE_LISTEN_ADDR` | `:9090` | Pipeline HTTP listen address |
| `pipeline.correlation_window` | `GOSENTINEL_PIPELINE_CORRELATION_WINDOW` | `30s` | Trace-metric-log join window |
| `pipeline.sampling_rate` | `GOSENTINEL_PIPELINE_SAMPLING_RATE` | `0.1` | Probabilistic sampling rate |
| `pipeline.latency_threshold` | `GOSENTINEL_PIPELINE_LATENCY_THRESHOLD` | `500ms` | Latency sampling threshold |
| `api.listen_addr` | `GOSENTINEL_API_LISTEN_ADDR` | `:8080` | API HTTP listen address |
| `api.jwt_secret` | `GOSENTINEL_API_JWT_SECRET` | — | JWT signing secret (required) |
| `api.rate_limit` | `GOSENTINEL_API_RATE_LIMIT` | `100` | Requests/second per client |
| `ui.listen_addr` | `GOSENTINEL_UI_LISTEN_ADDR` | `:3000` | UI HTTP listen address |
| `jaeger.endpoint` | `GOSENTINEL_JAEGER_ENDPOINT` | `localhost:16685` | Jaeger gRPC query endpoint |
| `victoria_metrics.endpoint` | `GOSENTINEL_VICTORIA_METRICS_ENDPOINT` | `http://localhost:8428` | VictoriaMetrics base URL |
| `loki.endpoint` | `GOSENTINEL_LOKI_ENDPOINT` | `http://localhost:3100` | Loki base URL |
| `pyroscope.endpoint` | `GOSENTINEL_PYROSCOPE_ENDPOINT` | `http://localhost:4040` | Pyroscope base URL |
| `postgres.dsn` | `GOSENTINEL_POSTGRES_DSN` | — | PostgreSQL connection string |
| `alerting.rules_file` | `GOSENTINEL_ALERTING_RULES_FILE` | `config/alert-rules.yaml` | Alert rules YAML path |
| `alerting.dedup_ttl` | `GOSENTINEL_ALERTING_DEDUP_TTL` | `10m` | Suppress duplicate notifications within this window |
| `alerting.slack_webhook` | `GOSENTINEL_ALERTING_SLACK_WEBHOOK` | — | Slack incoming webhook URL |
| `alerting.pagerduty_key` | `GOSENTINEL_ALERTING_PAGERDUTY_KEY` | — | PagerDuty integration key |
| `alerting.gmail_username` | `GOSENTINEL_ALERTING_GMAIL_USERNAME` | — | Gmail sender address |
| `alerting.gmail_password` | `GOSENTINEL_ALERTING_GMAIL_PASSWORD` | — | Gmail App Password (not account password) |
| `alerting.gmail_to` | `GOSENTINEL_ALERTING_GMAIL_TO` | — | Comma-separated recipient addresses |
| `alerting.smtp_host` | `GOSENTINEL_ALERTING_SMTP_HOST` | `smtp.gmail.com` | SMTP server host |
| `alerting.smtp_port` | `GOSENTINEL_ALERTING_SMTP_PORT` | `587` | SMTP port: 587 (STARTTLS) or 465 (implicit TLS) |
| `alerting.webhook_url` | `GOSENTINEL_ALERTING_WEBHOOK_URL` | — | Generic webhook endpoint URL |
| `alerting.webhook_secret` | `GOSENTINEL_ALERTING_WEBHOOK_SECRET` | — | HMAC-SHA256 signing secret for webhook |
| `alerting.opsgenie_key` | `GOSENTINEL_ALERTING_OPSGENIE_KEY` | — | OpsGenie API key |
| `alerting.opsgenie_region` | `GOSENTINEL_ALERTING_OPSGENIE_REGION` | `us` | OpsGenie region (`us` or `eu`) |
| `alerting.teams_webhook` | `GOSENTINEL_ALERTING_TEAMS_WEBHOOK` | — | Microsoft Teams incoming webhook URL |

---

## Alert Manager

GoSentinel ships a full end-to-end alert manager with six notification channels,
escalation policies, alert grouping, and Prometheus instrumentation.

### Supported channels

| Channel | Transport | What you need |
|---------|-----------|---------------|
| `slack` | HTTPS webhook | Incoming Webhook URL |
| `gmail` | SMTP TLS/STARTTLS | Gmail address + App Password |
| `pagerduty` | HTTPS Events API v2 | Integration Key |
| `opsgenie` | HTTPS Alerts API v2 | API Key |
| `teams` | HTTPS Adaptive Card | Incoming Webhook URL |
| `webhook` | HTTPS POST (JSON) | Any HTTP endpoint; optional HMAC secret |

### How it works

1. `RuleEvaluator` evaluates MetricsQL rules every 30 s (configurable).
2. EWMA anomaly detector forwards anomalies as synthetic alert events.
3. On a state transition (pending → firing → resolved) `AlertManager.Notify()` is called.
4. `AlertManager` checks silences → deduplication → escalation policy → routing config → fans out to matched channels concurrently.
5. Each channel is retried up to 3 times with exponential back-off (2 s → 4 s → 30 s cap).
6. Every delivery attempt (success or failure) is recorded in the `NotificationLog`.
7. Prometheus metrics track delivery counts, latency, retries, silences, and dedup hits.

### Escalation Policy

Alerts automatically escalate to additional channels the longer they remain firing:

| Level | Fires after | Channels | Repeat interval |
|-------|-------------|----------|-----------------|
| 0 | immediately | slack, gmail | 10 min |
| 1 | 5 min | + pagerduty | 30 min |
| 2 | 15 min | + opsgenie | 60 min |

Override per-rule via `POST /api/v1/routing` or configure `EscalationPolicy` in code.

### Alert Grouping

`AlertGrouper` batches related alerts within a 30 s window before dispatching,
reducing notification noise during correlated failure cascades. Group keys:

- `GroupBySeverity` — batch all critical alerts together
- `GroupByService` — batch all alerts from the same service
- `GroupByRule` — one notification per rule (default, no batching)

### Gmail Setup

1. Enable 2-Step Verification on your Google account.
2. Generate an App Password at https://myaccount.google.com/apppasswords
3. Set `GOSENTINEL_ALERTING_GMAIL_USERNAME` and `GOSENTINEL_ALERTING_GMAIL_PASSWORD`.
4. Set `GOSENTINEL_ALERTING_GMAIL_TO` to a comma-separated list of recipients.

GoSentinel supports both STARTTLS (port 587, default) and implicit TLS (port 465).
Set `GOSENTINEL_ALERTING_SMTP_PORT=465` to use implicit TLS.

### Alert Rules

Edit `config/alert-rules.yaml`. The `notify` list controls which channels receive each rule:

```yaml
rules:
  - name: "High error rate"
    expr: 'rate(http_requests_total{status=~"5.."}[5m]) > 0.05'
    for: 2m
    severity: critical
    annotations:
      summary: "Service error rate above 5%"
    notify: [slack, pagerduty, gmail, opsgenie, teams]

  - name: "High p99 latency"
    expr: 'histogram_quantile(0.99, rate(http_request_duration_seconds_bucket[5m])) > 1.0'
    for: 5m
    severity: warning
    annotations:
      summary: "P99 latency above 1 second"
    notify: [slack, gmail, webhook, teams]
```

Supported notifiers: `slack`, `gmail`, `pagerduty`, `opsgenie`, `teams`, `webhook`

### Alert Manager REST API

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

#### Test a notification channel

```bash
# Test Slack
curl -X POST http://localhost:8080/api/v1/alerts/test \
  -H "Content-Type: application/json" \
  -d '{"channel":"slack","severity":"warning","summary":"Test from GoSentinel"}'

# Test Gmail
curl -X POST http://localhost:8080/api/v1/alerts/test \
  -H "Content-Type: application/json" \
  -d '{"channel":"gmail","summary":"Test email from GoSentinel"}'

# Test all channels at once
curl -X POST http://localhost:8080/api/v1/alerts/test \
  -H "Content-Type: application/json" \
  -d '{}'
```

#### List registered channels

```bash
curl http://localhost:8080/api/v1/channels
# [{"name":"slack"},{"name":"gmail"},{"name":"pagerduty"}]
```

#### Silence a noisy rule

```bash
curl -X POST http://localhost:8080/api/v1/silences \
  -H "Content-Type: application/json" \
  -d '{"rule_name":"High memory usage","created_by":"ops","comment":"Planned maintenance","duration":"2h"}'
```

#### Update routing at runtime

```bash
# Route "Service down" only to PagerDuty and Gmail
curl -X POST http://localhost:8080/api/v1/routing \
  -H "Content-Type: application/json" \
  -d '{"rule_name":"Service down","channels":["pagerduty","gmail"]}'
```

#### View notification audit log

```bash
curl http://localhost:8080/api/v1/notifications
curl http://localhost:8080/api/v1/notifications/gmail
```

### Alert Manager Metrics

The alert manager exposes Prometheus metrics at `/metrics` (pipeline `:9090`):

| Metric | Description |
|--------|-------------|
| `gosentinel_alertmanager_notifications_total{channel,status}` | Delivery attempts |
| `gosentinel_alertmanager_notification_duration_seconds{channel}` | Delivery latency |
| `gosentinel_alertmanager_alerts_total{rule,state,severity}` | Events processed |
| `gosentinel_alertmanager_silenced_total{rule}` | Suppressed by silence |
| `gosentinel_alertmanager_dedup_total{rule}` | Suppressed by dedup |
| `gosentinel_alertmanager_active_alerts` | Currently firing |
| `gosentinel_alertmanager_retry_total{channel}` | Retry attempts |

---

## Observability Patterns

GoSentinel implements all major industry observability patterns:

| Pattern | Where |
|---------|-------|
| RED (Rate/Errors/Duration) | `internal/metrics/registry.go` — `REDMetrics` |
| USE (Utilization/Saturation/Errors) | `internal/metrics/registry.go` — `USEMetrics` |
| Four Golden Signals | `internal/metrics/registry.go` — `GoldenSignals` |
| SLI/SLO/Error Budget | `internal/slo/tracker.go` — `SLOTracker` |
| Multi-window burn rate | `internal/slo/tracker.go` — `BurnRateAlert` |
| Distributed tracing | `pkg/otel/instrumentation.go` + `internal/tracing/` |
| Trace-log correlation | `trace_id` + `span_id` in every `slog` call |
| Tail-based sampling | `internal/sampling/tailsampler.go` |
| Continuous profiling | Pyroscope SDK in all example services |
| EWMA anomaly detection | `internal/anomaly/ewma.go` |
| Structured health checks | `internal/health/checker.go` |
| W3C TraceContext propagation | `internal/tracing/middleware.go` |

---

## Project Structure

```
gosentinel/
├── cmd/                    # Binary entrypoints
│   ├── collector/          # OTel Collector health sidecar
│   ├── pipeline/           # Streaming pipeline (correlation + anomaly + alerting)
│   ├── api/                # gRPC + REST query API
│   └── ui/                 # HTMX frontend
├── internal/               # Private packages
│   ├── alerting/           # AlertManager, notifiers, escalation, grouping, metrics
│   │   ├── evaluator.go    # RuleEvaluator — MetricsQL state machine
│   │   ├── manager.go      # AlertManager — routing, dedup, retry, fan-out
│   │   ├── notifier.go     # Slack, Gmail, PagerDuty, OpsGenie, Teams, Webhook
│   │   ├── escalation.go   # EscalationPolicy — time-based channel escalation
│   │   ├── grouping.go     # AlertGrouper — batch correlated alerts
│   │   ├── metrics.go      # AlertManagerMetrics — Prometheus instrumentation
│   │   ├── routing.go      # RoutingConfig — per-rule channel map
│   │   ├── store.go        # AlertStore + SilenceManager
│   │   ├── notification_log.go  # NotificationLog — audit ring buffer
│   │   └── hmac.go         # HMAC-SHA256 webhook signing
│   ├── anomaly/            # EWMA detector + registry
│   ├── correlation/        # Trace-metric-log stream join
│   ├── health/             # Structured health checks
│   ├── metrics/            # RED/USE/Golden Signal metrics
│   ├── sampling/           # Tail-based sampling policies
│   ├── slo/                # SLO burn rate tracker
│   ├── storage/            # Jaeger/VM/Loki/Pyroscope clients
│   └── tracing/            # Distributed tracing helpers
├── pkg/                    # Shared packages
│   ├── config/             # Viper config loader
│   ├── middleware/         # JWT, rate limiter, OTel middleware
│   └── otel/               # OTel SDK bootstrap
├── scripts/
│   ├── minikube-up.sh      # Start Minikube + deploy full LGTM stack
│   └── minikube-down.sh    # Tear down namespace or cluster
├── proto/                  # Protobuf definitions
├── gen/                    # Generated proto stubs
├── examples/services/      # Instrumented example microservices
├── config/                 # OTel Collector, Prometheus, alert rules
├── deploy/
│   ├── docker/             # Dockerfiles (pipeline, api, ui)
│   ├── grafana/            # Dashboards + datasource provisioning
│   ├── helm/gosentinel/    # Helm chart
│   ├── k8s/
│   │   ├── minikube/       # Minikube manifests (00-namespace → 06-dashboard)
│   │   ├── gosentinel/     # Production manifests (api, pipeline, ui, configmap)
│   │   ├── monitoring/     # ServiceMonitors + PrometheusRules
│   │   ├── network/        # NetworkPolicies
│   │   └── rbac/           # ServiceAccounts, Roles, RoleBindings
│   └── sql/                # Database migrations
├── terraform/
│   ├── modules/            # Reusable TF modules (vpc, eks, rds, ecr, iam)
│   └── environments/       # dev + prod root modules
└── docs/
    ├── HLD.md              # High-Level Design
    ├── LLD.md              # Low-Level Design
    ├── architecture.md     # Component diagram + signal flow
    └── adr/                # Architecture Decision Records (001–004)
```

---

## Development

### Running tests

```bash
make test           # all tests with race detector
make test-short     # skip integration tests
make test-alerting  # alerting package only
make coverage       # HTML coverage report
```

### Adding a new alert rule

1. Edit `config/alert-rules.yaml`
2. Add rule with `name`, `expr` (MetricsQL), `for`, `severity`, `notify`
3. Restart pipeline: `go run ./cmd/pipeline/`

In Minikube, update the ConfigMap and restart:
```bash
kubectl edit configmap gosentinel-alert-rules -n gosentinel
kubectl rollout restart deployment/gosentinel-pipeline -n gosentinel
```

### Adding a new notification channel

1. Implement `Notifier` interface in `internal/alerting/notifier.go`
2. Add config fields to `pkg/config/config.go` and `AlertingConfig`
3. Register in `cmd/pipeline/main.go` and `cmd/api/main.go`
4. Add env var to `.env.example` and `deploy/k8s/minikube/01-configmap.yaml`

### Adding a new storage backend

1. Create `internal/storage/mybackend.go`
2. Implement the client with context-aware methods
3. Add config fields to `pkg/config/config.go`
4. Wire into `cmd/api/main.go` and `cmd/pipeline/main.go`

### Regenerating proto stubs

```bash
go install github.com/bufbuild/buf/cmd/buf@latest
make proto-gen
```

---

## Troubleshooting

**Pipeline can't connect to VictoriaMetrics**
```bash
curl http://localhost:8428/health
# Check: GOSENTINEL_VICTORIA_METRICS_ENDPOINT is set correctly
```

**No traces in Jaeger / Tempo**
```bash
# Check OTel Collector is running and healthy
curl http://localhost:13133/
docker compose logs otel-collector
# In Minikube:
make minikube-logs-otel
```

**API returns 401**
```bash
# The API requires a JWT. For local dev, set a simple secret:
export GOSENTINEL_API_JWT_SECRET=dev-secret
# Then generate a token (example using jwt-cli):
jwt encode --secret dev-secret --sub test
curl -H "Authorization: Bearer <token>" http://localhost:8080/health
```

**Alert notifications not being sent**
```bash
# Check which channels are registered
curl http://localhost:8080/api/v1/channels

# Send a test notification
curl -X POST http://localhost:8080/api/v1/alerts/test \
  -H "Content-Type: application/json" \
  -d '{"channel":"slack","severity":"warning","summary":"Test"}'

# Check the notification audit log
curl http://localhost:8080/api/v1/notifications | python3 -m json.tool

# In Minikube:
make minikube-channels
make minikube-test-alert CHANNEL=gmail
```

**Gmail not sending**
```bash
# Verify App Password (NOT your account password)
# Generate at: https://myaccount.google.com/apppasswords
# Test SMTP connectivity:
curl -v smtp://smtp.gmail.com:587
```

**Pods not starting in Minikube**
```bash
kubectl describe pod -l app=gosentinel-pipeline -n gosentinel
kubectl logs deployment/gosentinel-pipeline -n gosentinel --previous
# Check init container logs:
kubectl logs deployment/gosentinel-pipeline -n gosentinel -c wait-postgres
kubectl logs deployment/gosentinel-pipeline -n gosentinel -c db-migrate
```

**Pods not starting in production K8s**
```bash
kubectl describe pod -l app.kubernetes.io/name=gosentinel-pipeline -n gosentinel
kubectl logs -l app.kubernetes.io/name=gosentinel-pipeline -n gosentinel --previous
```

**Mimir not ready in Minikube**
```bash
# Mimir needs ~60s to start. Check:
kubectl logs deployment/mimir -n gosentinel
# If OOM killed, increase Minikube memory:
minikube stop
minikube start --memory=8192 --cpus=6
```

**Grafana datasources showing errors**
```bash
# Verify backends are healthy:
kubectl get pods -n gosentinel | grep -E "mimir|loki|tempo|victoriametrics"
# Re-provision datasources:
kubectl rollout restart deployment/grafana -n gosentinel
```

---

## License

Apache 2.0 — see [LICENSE](LICENSE)
