# ADR 001: In-Process Tail-Based Sampling

## Status
Accepted

## Context
The OTel Collector supports tail sampling via the `tail_sampling` processor, but it requires all spans for a trace to arrive at the same collector instance. For a single-node local dev setup this is fine, but the GoSentinel pipeline also needs to make sampling decisions based on correlated signals (e.g., drop a trace only if it has no errors AND no anomalous metrics).

## Decision
Implement an in-process `TailSampler` in `internal/sampling/tailsampler.go` using a ristretto LRU cache as the span buffer. The `CompositePolicy` allows combining error, latency, and probabilistic policies with OR semantics.

## Consequences
- Sampling decisions are made after the full trace is assembled (bufferTTL window).
- Memory bounded by ristretto cache MaxCost.
- Requires the pipeline to receive all spans for a trace (use sticky routing in production).
