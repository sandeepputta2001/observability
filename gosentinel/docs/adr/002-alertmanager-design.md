# ADR-002: AlertManager Multi-Channel Design

**Status:** Accepted  
**Date:** 2024-01-15  
**Deciders:** GoSentinel Team

---

## Context

GoSentinel needs to deliver alert notifications to multiple external systems (Slack, email, PagerDuty, OpsGenie, Teams, generic webhooks) with reliability guarantees. The system must handle:

- Concurrent delivery to multiple channels without blocking
- Transient failures in external APIs (network timeouts, rate limits)
- Duplicate suppression to avoid alert fatigue
- Time-based escalation (notify more channels the longer an alert fires)
- Audit trail for compliance and debugging
- Runtime routing updates without restarts

## Decision

Implement a custom `AlertManager` in `internal/alerting/manager.go` rather than integrating with Prometheus Alertmanager.

### Architecture

```
AlertEvent → AlertStore → SilenceCheck → DedupCheck → EscalationPolicy → RoutingConfig → Fan-out
                                                                                              │
                                                                              ┌───────────────┤
                                                                              ▼               ▼
                                                                         sendWithRetry   sendWithRetry
                                                                              │               │
                                                                         Notifier.Notify  Notifier.Notify
                                                                              │               │
                                                                         NotificationLog  NotificationLog
```

### Key design choices

1. **Concurrent fan-out with goroutines** — each channel notified in a separate goroutine; `sync.WaitGroup` ensures all complete before returning.

2. **Exponential back-off retry** — 3 attempts with 2s → 4s → 30s cap. Configurable per channel via `RetryConfig`.

3. **Deduplication by (rule, state) tuple** — suppresses repeated notifications within a configurable TTL (default 10m). Prevents alert storms.

4. **EscalationPolicy** — time-based channel escalation using `firingStart` tracking. Wildcard `"*"` applies to all rules; rule-specific levels override.

5. **RoutingConfig** — per-rule static channel list loaded from YAML, updatable at runtime via REST API. Falls back to all channels if no routing configured.

6. **NotificationLog** — in-memory ring buffer (1000 records) of every delivery attempt with status and error. Exposed via REST API for audit.

7. **AlertStore** — in-memory ring buffer (500 events) of all alert state transitions. Separate from NotificationLog.

8. **Prometheus metrics** — 7 counters/histograms/gauges for observability of the notification pipeline itself.

## Alternatives Considered

### Prometheus Alertmanager
- **Pros:** Battle-tested, rich routing DSL, inhibition rules, silences UI
- **Cons:** Separate binary to deploy and maintain, complex configuration, no native Go API for programmatic control, doesn't integrate with GoSentinel's internal alert state machine

### Simple direct notification
- **Pros:** Minimal code
- **Cons:** No deduplication, no retry, no audit trail, no routing

## Consequences

- **Positive:** Full control over notification pipeline, tight integration with GoSentinel's alert state machine, Prometheus metrics for observability, REST API for runtime management
- **Negative:** Must maintain our own implementation; missing some Alertmanager features (inhibition rules, complex routing trees, silences UI)
- **Neutral:** In-memory state means alert history is lost on restart (acceptable for audit log; alert state is persisted in PostgreSQL)
