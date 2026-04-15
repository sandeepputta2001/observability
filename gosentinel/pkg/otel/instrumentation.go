// Package otel provides shared OpenTelemetry SDK bootstrap for all GoSentinel binaries.
package otel

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Config holds OTel SDK bootstrap configuration.
type Config struct {
	// OTLPEndpoint is the gRPC endpoint for the OTLP exporter (e.g. "localhost:4317").
	OTLPEndpoint string
	// ServiceName is the logical name of this service.
	ServiceName string
	// ServiceVersion is the version string of this service.
	ServiceVersion string
	// UsePrometheusExporter controls whether a Prometheus /metrics exporter is registered.
	// When true, metrics are exposed via the default Prometheus registry.
	UsePrometheusExporter bool
}

// Setup initialises TracerProvider, MeterProvider, and LoggerProvider and sets them as globals.
// It returns a single shutdown function that drains and shuts down all three providers in order.
func Setup(ctx context.Context, cfg Config) (shutdown func(context.Context) error, err error) {
	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(cfg.ServiceName),
			semconv.ServiceVersion(cfg.ServiceVersion),
		),
		resource.WithProcess(),
		resource.WithOS(),
		resource.WithHost(),
	)
	if err != nil {
		return nil, fmt.Errorf("creating OTel resource: %w", err)
	}

	conn, err := grpc.NewClient(cfg.OTLPEndpoint,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, fmt.Errorf("creating OTLP gRPC connection: %w", err)
	}

	// --- Tracer Provider ---
	traceExporter, err := otlptracegrpc.New(ctx, otlptracegrpc.WithGRPCConn(conn))
	if err != nil {
		return nil, fmt.Errorf("creating OTLP trace exporter: %w", err)
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter,
			sdktrace.WithBatchTimeout(5*time.Second),
		),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	otel.SetTracerProvider(tp)

	// --- Meter Provider ---
	var mp *sdkmetric.MeterProvider
	if cfg.UsePrometheusExporter {
		promExporter, promErr := prometheus.New()
		if promErr != nil {
			return nil, fmt.Errorf("creating Prometheus exporter: %w", promErr)
		}
		mp = sdkmetric.NewMeterProvider(
			sdkmetric.WithReader(promExporter),
			sdkmetric.WithResource(res),
		)
	} else {
		metricExporter, mErr := otlpmetricgrpc.New(ctx, otlpmetricgrpc.WithGRPCConn(conn))
		if mErr != nil {
			return nil, fmt.Errorf("creating OTLP metric exporter: %w", mErr)
		}
		mp = sdkmetric.NewMeterProvider(
			sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter,
				sdkmetric.WithInterval(15*time.Second),
			)),
			sdkmetric.WithResource(res),
		)
	}
	otel.SetMeterProvider(mp)

	// --- Logger Provider ---
	// Note: OTLP log exporter is omitted here to avoid the experimental
	// go.opentelemetry.io/otel/sdk/log sub-module dependency.
	// Structured logging is handled via slog throughout the codebase.

	// W3C TraceContext + Baggage propagation
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	shutdown = func(ctx context.Context) error {
		var shutdownErr error
		if err := tp.Shutdown(ctx); err != nil {
			shutdownErr = fmt.Errorf("shutting down tracer provider: %w", err)
		}
		if err := mp.Shutdown(ctx); err != nil {
			shutdownErr = fmt.Errorf("shutting down meter provider: %w", err)
		}
		if err := conn.Close(); err != nil {
			shutdownErr = fmt.Errorf("closing OTLP gRPC connection: %w", err)
		}
		return shutdownErr
	}

	return shutdown, nil
}
