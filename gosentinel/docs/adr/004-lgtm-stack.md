# ADR-004: Grafana LGTM Stack for Observability

**Status:** Accepted  
**Date:** 2024-03-01  
**Deciders:** GoSentinel Team

---

## Context

GoSentinel needs a unified observability backend for the Minikube local development environment that:

- Covers all four observability signals: metrics, traces, logs, profiles
- Integrates natively with Grafana for unified dashboards
- Supports trace-to-log and trace-to-metric correlation in the UI
- Runs on a single Minikube node with reasonable resource usage
- Uses OpenTelemetry as the collection protocol

The existing production setup uses Jaeger (traces) + VictoriaMetrics (metrics) + Loki (logs) + Pyroscope (profiles). These are kept for GoSentinel's internal client compatibility.

## Decision

Add the **Grafana LGTM stack** (Loki + Grafana + Tempo + Mimir) as the primary observability backend for Minikube, alongside the existing compatibility backends.

### Stack components

| Component | Role | Port | Version |
|-----------|------|------|---------|
| **Loki** | Log aggregation | 3100 | 3.0.0 |
| **Grafana** | Unified dashboards | 3000 | 11.0.0 |
| **Tempo** | Distributed tracing | 3200, 4317 | 2.4.1 |
| **Mimir** | Long-term metrics | 9009 | 2.12.0 |
| VictoriaMetrics | GoSentinel pipeline queries | 8428 | 1.99.0 |
| Jaeger | GoSentinel API trace queries | 16686 | 1.57 |
| Pyroscope | Continuous profiling | 4040 | 1.5.0 |

### OTel Collector routing

```
OTLP input
    ├── Traces  → Tempo (primary) + Jaeger (compat)
    ├── Metrics → Mimir (primary) + VictoriaMetrics (compat)
    └── Logs    → Loki
```

### Grafana datasource integration

All datasources are pre-provisioned via ConfigMap:
- **Mimir** — primary metrics (default datasource)
- **Loki** — logs with derived field for `trace_id` → Tempo link
- **Tempo** — traces with service map, span metrics, trace-to-log correlation
- **VictoriaMetrics** — GoSentinel pipeline metrics
- **Jaeger** — trace queries (compat)
- **Pyroscope** — continuous profiling

### Tempo metrics generator

Tempo's metrics generator is configured to push span metrics and service graph metrics to Mimir, enabling:
- `traces_spanmetrics_calls_total` — request rate from traces
- `traces_spanmetrics_duration_seconds` — latency from traces
- Service dependency graph in Grafana

## Alternatives Considered

### Prometheus + Jaeger + Loki (existing stack)
- **Pros:** Already deployed, familiar
- **Cons:** No native trace-to-metric correlation, no service map, Grafana datasource integration is manual

### Elastic Stack (ELK)
- **Pros:** Mature, rich query language
- **Cons:** High memory usage (Elasticsearch), not OTel-native, expensive licensing for full features

### Signoz
- **Pros:** OTel-native, unified UI
- **Cons:** Less mature, harder to self-host, different architecture

## Consequences

- **Positive:** Native Grafana integration, trace-to-log-to-metric correlation, service map, span metrics, single pane of glass for all signals
- **Negative:** Higher memory footprint (Mimir needs ~512MB, Tempo ~256MB), more complex deployment, two sets of backends to maintain
- **Neutral:** Minikube requires 6GB RAM minimum; existing GoSentinel code unchanged (still queries VictoriaMetrics and Jaeger)
