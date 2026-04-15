// Command order-service is a sample instrumented Go microservice.
// It demonstrates OTel tracing, Prometheus metrics, pprof, and structured logging.
package main

import (
	"context"
	"encoding/json"
	"fmt"
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
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const (
	serviceName    = "order-service"
	serviceVersion = "0.1.0"
	listenAddr     = ":8081"
	inventoryAddr  = "http://inventory-service:8082"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	otlpEndpoint := envOr("OTLP_ENDPOINT", "otel-collector:4317")
	pyroscopeEndpoint := envOr("PYROSCOPE_ENDPOINT", "http://pyroscope:4040")

	// Start Pyroscope continuous profiling
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

	// Bootstrap OTel
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
	mux.Handle("/orders", otelhttp.NewHandler(http.HandlerFunc(listOrdersHandler), "list-orders"))
	mux.Handle("/orders/create", otelhttp.NewHandler(http.HandlerFunc(createOrderHandler), "create-order"))

	srv := &http.Server{
		Addr:         listenAddr,
		Handler:      otelhttp.NewHandler(mux, serviceName),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	go func() {
		slog.Info("order-service starting", "addr", listenAddr)
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
	_, _ = w.Write([]byte(`{"status":"ok","service":"order-service"}`))
}

func listOrdersHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	span := trace.SpanFromContext(ctx)
	sc := span.SpanContext()

	slog.InfoContext(ctx, "listing orders",
		"trace_id", sc.TraceID().String(),
		"span_id", sc.SpanID().String(),
	)

	// Call inventory service to check stock
	if err := callInventory(ctx, "/inventory/check"); err != nil {
		slog.ErrorContext(ctx, "inventory check failed",
			"trace_id", sc.TraceID().String(),
			"error", err,
		)
		span.SetAttributes(attribute.Bool("error", true))
		http.Error(w, "inventory unavailable", http.StatusServiceUnavailable)
		return
	}

	orders := []map[string]interface{}{
		{"id": "ord-001", "item": "widget", "qty": 2},
		{"id": "ord-002", "item": "gadget", "qty": 1},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(orders)
}

func createOrderHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	span := trace.SpanFromContext(ctx)
	sc := span.SpanContext()

	slog.InfoContext(ctx, "creating order",
		"trace_id", sc.TraceID().String(),
		"span_id", sc.SpanID().String(),
	)

	// Simulate work
	time.Sleep(10 * time.Millisecond)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]string{"id": "ord-003", "status": "created"})
}

// callInventory makes an outbound HTTP call with W3C traceparent propagation.
func callInventory(ctx context.Context, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, inventoryAddr+path, nil)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}

	// Inject W3C TraceContext headers
	otel.GetTextMapPropagator().Inject(ctx, propagationCarrier(req.Header))

	client := &http.Client{
		Transport: otelhttp.NewTransport(http.DefaultTransport),
		Timeout:   5 * time.Second,
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("calling inventory: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("inventory returned %d", resp.StatusCode)
	}
	return nil
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

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
