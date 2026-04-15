// Package tracing provides distributed tracing middleware and helpers.
// Implements: W3C TraceContext propagation, baggage, span enrichment,
// trace-log correlation, and exemplar injection for metrics.
package tracing

import (
	"context"
	"log/slog"
	"net/http"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/codes"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
	"go.opentelemetry.io/otel/trace"
)

// SpanFromContext extracts the active span and returns its trace/span IDs as slog attrs.
// Use this to correlate logs with traces (trace-log correlation pattern).
func SpanFromContext(ctx context.Context) (traceID, spanID string) {
	span := trace.SpanFromContext(ctx)
	sc := span.SpanContext()
	if sc.IsValid() {
		return sc.TraceID().String(), sc.SpanID().String()
	}
	return "", ""
}

// LogAttrs returns slog attributes for trace correlation.
// Pattern: inject trace_id + span_id into every log line for correlation in Loki.
func LogAttrs(ctx context.Context) []any {
	traceID, spanID := SpanFromContext(ctx)
	if traceID == "" {
		return nil
	}
	return []any{
		"trace_id", traceID,
		"span_id", spanID,
	}
}

// RecordError marks the active span as errored and records the error event.
// Pattern: always record errors on spans for trace-based debugging.
func RecordError(ctx context.Context, err error, attrs ...attribute.KeyValue) {
	span := trace.SpanFromContext(ctx)
	span.RecordError(err, trace.WithAttributes(attrs...))
	span.SetStatus(codes.Error, err.Error())
}

// AddBaggage adds a key-value pair to the W3C Baggage for downstream propagation.
// Pattern: propagate tenant/user context across service boundaries.
func AddBaggage(ctx context.Context, key, value string) context.Context {
	m, err := baggage.NewMember(key, value)
	if err != nil {
		slog.Warn("invalid baggage member", "key", key, "error", err)
		return ctx
	}
	b, err := baggage.New(m)
	if err != nil {
		slog.Warn("creating baggage", "error", err)
		return ctx
	}
	return baggage.ContextWithBaggage(ctx, b)
}

// BaggageValue retrieves a value from W3C Baggage.
func BaggageValue(ctx context.Context, key string) string {
	return baggage.FromContext(ctx).Member(key).Value()
}

// InjectHTTP injects W3C TraceContext + Baggage headers into an outgoing HTTP request.
// Pattern: always propagate trace context for distributed tracing.
func InjectHTTP(ctx context.Context, req *http.Request) {
	otel.GetTextMapPropagator().Inject(ctx, propagationCarrier(req.Header))
}

// ExtractHTTP extracts W3C TraceContext + Baggage from an incoming HTTP request.
func ExtractHTTP(req *http.Request) context.Context {
	return otel.GetTextMapPropagator().Extract(req.Context(), propagationCarrier(req.Header))
}

// EnrichSpan adds standard semantic convention attributes to the active span.
func EnrichSpan(ctx context.Context, attrs ...attribute.KeyValue) {
	trace.SpanFromContext(ctx).SetAttributes(attrs...)
}

// EnrichSpanWithHTTP adds HTTP semantic convention attributes to the active span.
func EnrichSpanWithHTTP(ctx context.Context, method, path, userAgent string, statusCode int) {
	span := trace.SpanFromContext(ctx)
	span.SetAttributes(
		semconv.HTTPRequestMethodKey.String(method),
		semconv.URLPath(path),
		semconv.UserAgentOriginal(userAgent),
		semconv.HTTPResponseStatusCode(statusCode),
	)
	if statusCode >= 500 {
		span.SetStatus(codes.Error, http.StatusText(statusCode))
	}
}

// propagationCarrier adapts http.Header to the OTel TextMapCarrier interface.
type propagationCarrier http.Header

func (c propagationCarrier) Get(key string) string { return http.Header(c).Get(key) }
func (c propagationCarrier) Set(key, val string)   { http.Header(c).Set(key, val) }
func (c propagationCarrier) Keys() []string {
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	return keys
}
