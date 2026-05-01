# GoSentinel Architecture

## Overview

GoSentinel is a self-hosted observability platform for Go microservice fleets. It collects the four pillars of observability — metrics, traces, logs, and continuous profiles — correlates them on `trace_id`, detects anomalies, evaluates alert rules, and exposes everything through a unified query API and HTMX dashboard.

## Component Diagram

```
┌─────────────────────────────────────────────────────────────────────────┐
│                         Instrumented Services                            │
│  order-service :8081  inventory-service :8082  payment-service :8083    │
│  (OTel SDK + pprof + slog + Pyroscope Go SDK)                           │
└────────────────────────────┬────────────────────────────────────────────┘
                             │ OTLP gRPC (traces + metrics + logs)
                             ▼
              ┌──────────────────────────────┐
              │   OpenTelemetry Collector    │  tail_sampling, batching,
              │   (contrib 0.102)            │  resource enrichment
              └──┬──────────┬──────────┬────┘
                 │          │          │
          Traces │  Metrics │   Logs   │
                 ▼          ▼          ▼
         ┌──────────┐ ┌──────────┐ ┌──────────┐
         │  Tempo   │ │  Mimir   │ │   Loki   │  ← LGTM Stack (primary)
         │ (traces) │ │(metrics) │ │  (logs)  │
         └──────────┘ └──────────┘ └──────────┘
                 │          │          │
                 │  (compat)│  (compat)│
                 ▼          ▼          │
         ┌──────────┐ ┌──────────┐    │
         │  Jaeger  │ │Victoria  │    │  ← Compatibility backends
         │ (traces) │ │ Metrics  │    │    (GoSentinel internal clients)
         └──────────┘ └──────────┘    │
                                      │
         ┌────────────────────────────┘
         │
         ▼
┌──────────────────────────────────────────────────────────────────────┐
│                       GoSentinel Pipeline                             │
│                                                                       │
│  CorrelationEngine  — joins trace+metric+log on trace_id (30s window)│
│  TailSampler        — error/latency/probabilistic sampling policies  │
│  DetectorRegistry   — EWMA 3σ anomaly detection per metric series    │
│  RuleEvaluator      — MetricsQL rules → pending/firing/resolved FSM  │
│  SLOTracker         — multi-window burn rate (1h/6h/3d)              │
│  AlertManager       — routing, dedup, escalation, fan-out, retry     │
│                                                                       │
│  Alert Channels:                                                      │
│    Slack  Gmail  PagerDuty  OpsGenie  Teams  Webhook                 │
└──────────────────────────────────────────────────────────────────────┘
                         │
                         ▼
              ┌─────────────────────┐
              │  GoSentinel API     │  ConnectRPC (gRPC + HTTP/JSON :8080)
              │  QueryService       │  concurrent fan-out via errgroup
              │  SLOService         │  JWT auth + rate limiting
              │  AlertManager REST  │  silences, routing, history, test
              └──────────┬──────────┘
                         │
                         ▼
              ┌─────────────────────┐
              │  GoSentinel UI      │  HTMX + Alpine.js :3000
              │  Service map        │  auto-refresh every 15s
              │  Service detail     │  4 panels, each refreshes 30s
              │  SSE /sse/alerts    │  real-time alert banner
              └─────────────────────┘
                         │
                         ▼
              ┌─────────────────────┐
              │  Grafana :3000      │  LGTM dashboards
              │  Datasources:       │  Mimir, Loki, Tempo, Pyroscope,
              │                     │  VictoriaMetrics, Jaeger
              └─────────────────────┘
```

## Signal Flow

1. Services emit OTLP spans, metrics, and logs to the OTel Collector on `:4317`.
2. The Collector applies tail sampling, batches, and fans out to:
   - **Traces** → Tempo (primary LGTM) + Jaeger (GoSentinel API compat)
   - **Metrics** → Mimir (primary LGTM) + VictoriaMetrics (GoSentinel pipeline queries)
   - **Logs** → Loki
3. The Pipeline binary subscribes to all three streams, joins them on `trace_id` within a 30s window, and emits `CorrelatedEvent`s.
4. The EWMA detector observes metric points and fires `AnomalyEvent`s when z-score > 3σ. Anomalies are forwarded to the AlertManager as synthetic alert events.
5. The RuleEvaluator queries VictoriaMetrics every 30s, tracks pending/firing/resolved state in PostgreSQL, and calls `AlertManager.Notify()` on state transitions.
6. The AlertManager applies silences → deduplication → escalation policy → routing config → fans out to matched channels concurrently with exponential back-off retry.
7. The API server fans out queries to all four backends concurrently and returns correlated results.
8. The UI server renders HTMX partials and streams alert events via SSE.
9. Grafana provides unified dashboards across all LGTM backends with pre-configured datasources and the GoSentinel Overview dashboard.

## AlertManager Notification Flow

```
AlertEvent
    │
    ├─► AlertStore.Record()           always — history ring buffer (500 events)
    ├─► Prometheus metrics update     gosentinel_alertmanager_alerts_total
    ├─► SilenceManager.IsSilenced()   drop if rule is silenced
    ├─► Deduplication check           drop if same (rule, state) within dedupTTL
    ├─► EscalationPolicy.ChannelsFor()  time-based channel escalation
    │       Level 0 (immediate):  slack + gmail
    │       Level 1 (after 5m):   + pagerduty
    │       Level 2 (after 15m):  + opsgenie
    ├─► RoutingConfig.ChannelsFor()   per-rule static override (fallback)
    └─► Fan-out (goroutine per channel)
            ├─► sendWithRetry()       exponential back-off (2s → 4s → 30s cap)
            │       ├─► SlackNotifier.Notify()
            │       ├─► GmailNotifier.Notify()
            │       ├─► PagerDutyNotifier.Notify()
            │       ├─► OpsGenieNotifier.Notify()
            │       ├─► TeamsNotifier.Notify()
            │       └─► WebhookNotifier.Notify()
            └─► NotificationLog.Record()
```

## Deployment Environments

| Environment | Backend Stack | Deployment Method |
|-------------|--------------|-------------------|
| Local dev | Docker Compose | `make docker-up` |
| Minikube | LGTM (Loki+Grafana+Tempo+Mimir) + compat backends | `make minikube-up` |
| Production (EKS) | Managed backends (RDS, ECR) | `make helm-install` or `make k8s-apply` |

## Key Design Decisions

See `docs/adr/` for Architecture Decision Records:
- [ADR-001](adr/001-tail-sampling-in-process.md) — Tail sampling in-process
- [ADR-002](adr/002-alertmanager-design.md) — AlertManager multi-channel design
- [ADR-003](adr/003-ewma-anomaly-detection.md) — EWMA anomaly detection
- [ADR-004](adr/004-lgtm-stack.md) — Grafana LGTM stack for observability
