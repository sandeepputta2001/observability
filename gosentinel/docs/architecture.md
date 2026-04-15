# GoSentinel Architecture

## Overview

GoSentinel is a self-hosted observability platform for Go microservice fleets. It collects the four pillars of observability — metrics, traces, logs, and continuous profiles — correlates them on `trace_id`, detects anomalies, evaluates alert rules, and exposes everything through a unified query API and HTMX dashboard.

## Component Diagram

```
┌─────────────────────────────────────────────────────────────────┐
│                     Instrumented Services                        │
│  order-service  inventory-service  payment-service               │
│  (OTel SDK + pprof + slog + Pyroscope Go SDK)                   │
└────────────────────────┬────────────────────────────────────────┘
                         │ OTLP gRPC (traces + metrics + logs)
                         ▼
              ┌─────────────────────┐
              │   OTel Collector    │  tail_sampling, batching,
              │   (contrib 0.99)    │  resource enrichment
              └──┬──────┬──────┬───┘
                 │      │      │
          Jaeger │  VM  │  Loki│
                 ▼      ▼      ▼
         ┌──────────────────────────┐
         │   GoSentinel Pipeline    │
         │  CorrelationEngine       │  joins trace+metric+log on trace_id
         │  TailSampler             │  error/latency/probabilistic policies
         │  DetectorRegistry (EWMA) │  3-sigma anomaly detection
         │  RuleEvaluator           │  MetricsQL rules → Slack/PagerDuty
         │  SLOTracker              │  multi-window burn rate alerting
         └──────────────────────────┘
                         │
                         ▼
              ┌─────────────────────┐
              │  GoSentinel API     │  connectrpc (gRPC + HTTP/JSON)
              │  QueryService       │  concurrent fan-out via errgroup
              │  SLOService         │  JWT auth + rate limiting
              └──────────┬──────────┘
                         │
                         ▼
              ┌─────────────────────┐
              │  GoSentinel UI      │  HTMX + Alpine.js
              │  Service map        │  auto-refresh every 15s
              │  Service detail     │  4 panels, each refreshes 30s
              │  SSE /sse/alerts    │  real-time alert banner
              └─────────────────────┘
```

## Signal Flow

1. Services emit OTLP spans, metrics, and logs to the OTel Collector on `:4317`.
2. The Collector applies tail sampling, batches, and fans out to Jaeger (traces), VictoriaMetrics (metrics), and Loki (logs).
3. The Pipeline binary subscribes to all three streams, joins them on `trace_id` within a 30s window, and emits `CorrelatedEvent`s.
4. The EWMA detector observes metric points and fires `AnomalyEvent`s when z-score > 3σ.
5. The RuleEvaluator queries VictoriaMetrics every 30s, tracks pending/firing/resolved state in PostgreSQL, and notifies Slack/PagerDuty.
6. The API server fans out queries to all four backends concurrently and returns correlated results.
7. The UI server renders HTMX partials and streams alert events via SSE.

## Key Design Decisions

See `docs/adr/` for Architecture Decision Records.
