// Command ui is the GoSentinel HTMX + Alpine.js frontend server.
package main

import (
	"context"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/yourorg/gosentinel/internal/storage"
	"github.com/yourorg/gosentinel/pkg/config"
	otelsetup "github.com/yourorg/gosentinel/pkg/otel"
)

//go:embed templates/* static/*
var assets embed.FS

// UIServer holds dependencies for the UI HTTP handlers.
type UIServer struct {
	vm        *storage.VictoriaMetricsClient
	jaeger    *storage.JaegerClient
	loki      *storage.LokiClient
	pyroscope *storage.PyroscopeClient
	tmpl      *template.Template
	alertCh   <-chan alertMsg
	apiAddr   string // base URL of the GoSentinel API server, e.g. "http://localhost:8080"
}

// alertMsg is a JSON-serialisable alert for SSE.
type alertMsg struct {
	ID       string    `json:"id"`
	RuleName string    `json:"rule_name"`
	State    string    `json:"state"`
	Severity string    `json:"severity"`
	Summary  string    `json:"summary"`
	FiredAt  time.Time `json:"fired_at"`
}

// serviceRow is a single row in the service map table.
type serviceRow struct {
	Name         string
	ErrorRate    float64
	P99LatencyMs float64
	RequestRate  float64
	ActiveAlerts int
	ErrorClass   string // "ok" | "warn" | "crit"
	LatencyClass string
}

func main() {
	cfgFile := flag.String("config", "", "path to config file")
	flag.Parse()

	cfg, err := config.Load(*cfgFile)
	if err != nil {
		slog.Error("loading config", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	otelShutdown, err := otelsetup.Setup(ctx, otelsetup.Config{
		OTLPEndpoint: cfg.OTLPEndpoint, ServiceName: "gosentinel-ui",
		ServiceVersion: "0.1.0", UsePrometheusExporter: false,
	})
	if err != nil {
		slog.Error("setting up OTel", "error", err)
		os.Exit(1)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := otelShutdown(shutdownCtx); err != nil {
			slog.Error("OTel shutdown", "error", err)
		}
	}()

	tmpl, err := template.ParseFS(assets, "templates/*.html")
	if err != nil {
		slog.Error("parsing templates", "error", err)
		os.Exit(1)
	}

	jaegerClient, err := storage.NewJaegerClient(cfg.Jaeger.Endpoint)
	if err != nil {
		slog.Error("creating jaeger client", "error", err)
		os.Exit(1)
	}
	defer jaegerClient.Close()

	alertBroadcast := make(chan alertMsg, 64)

	ui := &UIServer{
		vm:        storage.NewVictoriaMetricsClient(cfg.VictoriaMetrics.Endpoint),
		jaeger:    jaegerClient,
		loki:      storage.NewLokiClient(cfg.Loki.Endpoint),
		pyroscope: storage.NewPyroscopeClient(cfg.Pyroscope.Endpoint),
		tmpl:      tmpl,
		alertCh:   alertBroadcast,
		apiAddr:   cfg.UI.APIAddr,
	}

	r := chi.NewRouter()
	r.Use(chimiddleware.Recoverer)
	r.Use(chimiddleware.Logger)

	r.Get("/", ui.serviceMapPage)
	r.Get("/services/{name}", ui.serviceDetailPage)
	r.Get("/alerts", ui.alertsPage)
	r.Get("/slos", ui.slosPage)
	r.Get("/sse/alerts", ui.sseAlertsHandler)

	r.Get("/partials/service-table", ui.serviceTablePartial)
	r.Get("/partials/service/{name}/traces", ui.serviceTracesPartial)
	r.Get("/partials/service/{name}/metrics", ui.serviceMetricsPartial)
	r.Get("/partials/service/{name}/logs", ui.serviceLogsPartial)
	r.Get("/partials/service/{name}/profile", ui.serviceProfilePartial)

	// Proxy alert management API calls to the API server so the browser
	// only needs to talk to the UI origin (avoids CORS issues).
	r.Mount("/api/v1", ui.apiProxy(cfg.UI.APIAddr))

	r.Handle("/static/*", http.FileServer(http.FS(assets)))

	srv := &http.Server{
		Addr: cfg.UI.ListenAddr, Handler: r,
		ReadTimeout: 10 * time.Second, WriteTimeout: 30 * time.Second,
	}
	go func() {
		slog.Info("UI server starting", "addr", cfg.UI.ListenAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("UI server error", "error", err)
		}
	}()

	<-ctx.Done()
	slog.Info("UI server shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("HTTP shutdown error", "error", err)
	}
}

func (ui *UIServer) serviceMapPage(w http.ResponseWriter, r *http.Request) {
	if err := ui.tmpl.ExecuteTemplate(w, "service-map.html", nil); err != nil {
		slog.ErrorContext(r.Context(), "rendering service-map", "error", err)
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

func (ui *UIServer) serviceDetailPage(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if err := ui.tmpl.ExecuteTemplate(w, "service-detail.html", map[string]string{"Service": name}); err != nil {
		slog.ErrorContext(r.Context(), "rendering service-detail", "error", err)
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

func (ui *UIServer) alertsPage(w http.ResponseWriter, r *http.Request) {
	if err := ui.tmpl.ExecuteTemplate(w, "alerts.html", nil); err != nil {
		slog.ErrorContext(r.Context(), "rendering alerts", "error", err)
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

func (ui *UIServer) slosPage(w http.ResponseWriter, r *http.Request) {
	if err := ui.tmpl.ExecuteTemplate(w, "slos.html", nil); err != nil {
		slog.ErrorContext(r.Context(), "rendering slos", "error", err)
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

// sseAlertsHandler streams alert events as Server-Sent Events.
func (ui *UIServer) sseAlertsHandler(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	// Send a keepalive comment every 15s
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()
		case alert := <-ui.alertCh:
			data, err := json.Marshal(alert)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}
}

func (ui *UIServer) serviceTablePartial(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	services, err := ui.vm.LabelValues(ctx, "service")
	if err != nil {
		slog.ErrorContext(ctx, "fetching service labels", "error", err)
		services = []string{}
	}

	var rows []serviceRow
	now := time.Now()
	for _, svc := range services {
		er, _ := ui.vm.QueryInstant(ctx,
			fmt.Sprintf(`rate(http_requests_total{service="%s",status=~"5.."}[5m])`, svc), now)
		p99, _ := ui.vm.QueryInstant(ctx,
			fmt.Sprintf(`histogram_quantile(0.99, rate(http_request_duration_seconds_bucket{service="%s"}[5m]))`, svc), now)
		rr, _ := ui.vm.QueryInstant(ctx,
			fmt.Sprintf(`rate(http_requests_total{service="%s"}[5m])`, svc), now)

		errClass := "ok"
		if er > 0.05 {
			errClass = "crit"
		} else if er > 0.01 {
			errClass = "warn"
		}
		latClass := "ok"
		if p99*1000 > 1000 {
			latClass = "crit"
		} else if p99*1000 > 500 {
			latClass = "warn"
		}

		rows = append(rows, serviceRow{
			Name: svc, ErrorRate: er, P99LatencyMs: p99 * 1000,
			RequestRate: rr, ErrorClass: errClass, LatencyClass: latClass,
		})
	}

	if err := ui.tmpl.ExecuteTemplate(w, "service-table-partial.html", rows); err != nil {
		slog.ErrorContext(ctx, "rendering service table partial", "error", err)
	}
}

func (ui *UIServer) serviceTracesPartial(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	ctx := r.Context()
	traces, err := ui.jaeger.FindTraces(ctx, &storage.TraceQueryParameters{
		ServiceName: name,
		StartTime:   time.Now().Add(-1 * time.Hour),
		EndTime:     time.Now(),
		NumTraces:   20,
	})
	if err != nil {
		slog.ErrorContext(ctx, "fetching traces", "error", err)
		traces = nil
	}
	if err := ui.tmpl.ExecuteTemplate(w, "traces-partial.html", traces); err != nil {
		slog.ErrorContext(ctx, "rendering traces partial", "error", err)
	}
}

func (ui *UIServer) serviceMetricsPartial(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	ctx := r.Context()
	now := time.Now()
	results, err := ui.vm.QueryRange(ctx,
		fmt.Sprintf(`rate(http_requests_total{service="%s"}[1m])`, name),
		now.Add(-30*time.Minute), now, time.Minute,
	)
	if err != nil {
		slog.ErrorContext(ctx, "fetching metrics", "error", err)
		results = nil
	}
	if err := ui.tmpl.ExecuteTemplate(w, "metrics-partial.html", results); err != nil {
		slog.ErrorContext(ctx, "rendering metrics partial", "error", err)
	}
}

func (ui *UIServer) serviceLogsPartial(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	ctx := r.Context()
	streams, err := ui.loki.QueryRange(ctx,
		fmt.Sprintf(`{service="%s"}`, name),
		time.Now().Add(-15*time.Minute), time.Now(),
	)
	if err != nil {
		slog.ErrorContext(ctx, "fetching logs", "error", err)
		streams = nil
	}
	if err := ui.tmpl.ExecuteTemplate(w, "logs-partial.html", streams); err != nil {
		slog.ErrorContext(ctx, "rendering logs partial", "error", err)
	}
}

func (ui *UIServer) serviceProfilePartial(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	ctx := r.Context()
	fns, err := ui.pyroscope.GetTopFunctions(ctx, name, time.Now().Add(-15*time.Minute), time.Now(), 10)
	if err != nil {
		slog.ErrorContext(ctx, "fetching profile", "error", err)
		fns = nil
	}
	if err := ui.tmpl.ExecuteTemplate(w, "profile-partial.html", fns); err != nil {
		slog.ErrorContext(ctx, "rendering profile partial", "error", err)
	}
}

// apiProxy returns an http.Handler that reverse-proxies /api/v1/* requests to
// the GoSentinel API server. This lets the browser call /api/v1/alerts without
// hitting CORS restrictions.
func (ui *UIServer) apiProxy(apiAddr string) http.Handler {
	target, err := url.Parse(apiAddr)
	if err != nil {
		slog.Error("parsing API addr for proxy", "addr", apiAddr, "error", err)
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "proxy misconfigured", http.StatusInternalServerError)
		})
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	return proxy
}
