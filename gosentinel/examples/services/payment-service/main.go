// Command payment-service is a sample instrumented Go microservice.
package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/grafana/pyroscope-go"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	otelsetup "github.com/yourorg/gosentinel/pkg/otel"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/trace"
)

const (
	serviceName    = "payment-service"
	serviceVersion = "0.1.0"
	listenAddr     = ":8083"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	otlpEndpoint := envOr("OTLP_ENDPOINT", "otel-collector:4317")
	pyroscopeEndpoint := envOr("PYROSCOPE_ENDPOINT", "http://pyroscope:4040")

	_, err := pyroscope.Start(pyroscope.Config{
		ApplicationName: serviceName,
		ServerAddress:   pyroscopeEndpoint,
		ProfileTypes: []pyroscope.ProfileType{
			pyroscope.ProfileCPU,
			pyroscope.ProfileGoroutines,
			pyroscope.ProfileAllocObjects,
		},
	})
	if err != nil {
		slog.Warn("pyroscope start failed", "error", err)
	}

	shutdown, err := otelsetup.Setup(ctx, otelsetup.Config{
		OTLPEndpoint:          otlpEndpoint,
		ServiceName:           serviceName,
		ServiceVersion:        serviceVersion,
		UsePrometheusExporter: true,
	})
	if err != nil {
		slog.Error("OTel setup failed", "error", err)
		os.Exit(1)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := shutdown(shutdownCtx); err != nil {
			slog.Error("OTel shutdown error", "error", err)
		}
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/health", healthHandler)
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/debug/pprof/", http.DefaultServeMux.ServeHTTP)
	mux.Handle("/payment/authorize", otelhttp.NewHandler(http.HandlerFunc(authorizeHandler), "authorize-payment"))
	mux.Handle("/payment/capture", otelhttp.NewHandler(http.HandlerFunc(captureHandler), "capture-payment"))

	srv := &http.Server{
		Addr:         listenAddr,
		Handler:      otelhttp.NewHandler(mux, serviceName),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	go func() {
		slog.Info("payment-service starting", "addr", listenAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "error", err)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("shutdown error", "error", err)
	}
}

func healthHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"ok","service":"payment-service"}`))
}

func authorizeHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	span := trace.SpanFromContext(ctx)
	sc := span.SpanContext()

	slog.InfoContext(ctx, "authorizing payment",
		"trace_id", sc.TraceID().String(),
		"span_id", sc.SpanID().String(),
	)

	// Simulate payment processing latency
	time.Sleep(20 * time.Millisecond)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status":        "authorized",
		"authorization": "auth-xyz-789",
	})
}

func captureHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	span := trace.SpanFromContext(ctx)
	sc := span.SpanContext()

	slog.InfoContext(ctx, "capturing payment",
		"trace_id", sc.TraceID().String(),
		"span_id", sc.SpanID().String(),
	)

	time.Sleep(15 * time.Millisecond)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "captured"})
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
