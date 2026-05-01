# ADR-003: EWMA Anomaly Detection

**Status:** Accepted  
**Date:** 2024-01-20  
**Deciders:** GoSentinel Team

---

## Context

GoSentinel needs to detect anomalous metric values in real-time without requiring a separate ML service, training data, or complex statistical infrastructure. The detector must:

- Work on streaming data with O(1) memory per metric series
- Adapt to changing baselines (e.g., traffic patterns change over time)
- Have a configurable sensitivity threshold
- Produce a severity classification (warning vs critical)
- Integrate with the existing AlertManager pipeline

## Decision

Use **Exponentially Weighted Moving Average (EWMA)** with a 3σ threshold for anomaly detection, implemented in `internal/anomaly/ewma.go`.

### Algorithm

```
Observe(value):
  count++
  if count == 1: mean = value; return false   // seed
  diff = value - mean
  mean = mean + α × diff                       // EWMA mean
  variance = (1-α) × (variance + α × diff²)   // EWMA variance
  if count < 10: return false                  // warm-up period
  stddev = √variance
  if stddev == 0: return false                 // constant series
  z_score = |value - mean| / stddev
  return z_score >= σ, z_score
```

### Parameters

| Parameter | Default | Effect |
|-----------|---------|--------|
| α (alpha) | 0.1 | Smoothing factor. Higher = more reactive to recent values |
| σ (sigma) | 3.0 | Alert threshold. Higher = fewer false positives |
| warm-up | 10 obs | Minimum observations before alerting |

### Severity mapping

- `warning`: z-score ≥ 3σ (< 6σ)
- `critical`: z-score ≥ 6σ

### Integration

Anomalies are forwarded to `AlertManager.Notify()` as synthetic `AlertEvent`s with `RuleName = "anomaly-<metric_name>"`. This means they go through the full notification pipeline (silences, dedup, escalation, routing).

## Alternatives Considered

### Static thresholds
- **Pros:** Simple, predictable
- **Cons:** Requires manual tuning per metric, doesn't adapt to baseline changes

### Isolation Forest / ML-based
- **Pros:** More sophisticated, handles multivariate anomalies
- **Cons:** Requires training data, separate ML service, much higher complexity

### Z-score with rolling window
- **Pros:** Simple, well-understood
- **Cons:** O(n) memory per series (stores window), doesn't adapt as smoothly

## Consequences

- **Positive:** O(1) memory per series, adapts to baseline changes, no external dependencies, configurable sensitivity
- **Negative:** Univariate only (one metric at a time), may miss correlated anomalies across metrics, warm-up period means first 10 observations are never flagged
- **Neutral:** False positive rate depends on α and σ tuning; defaults (0.1, 3.0) are conservative
