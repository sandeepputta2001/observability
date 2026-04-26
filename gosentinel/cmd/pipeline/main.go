// Command pipeline is the GoSentinel streaming pipeline: correlation, sampling,
// anomaly detection, SLO tracking, and alert rule evaluation.
package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/yourorg/gosentinel/internal/alerting"
	"github.com/yourorg/gosentinel/internal/anomaly"
	"github.com/yourorg/gosentinel/internal/correlation"
	"github.com/yourorg/gosentinel/internal/sampling"
	"github.com/yourorg/gosentinel/internal/slo"
	"github.com/yourorg/gosentinel/internal/storage"
	"github.com/yourorg/gosentinel/pkg/config"
	otelsetup "github.com/yourorg/gosentinel/pkg/otel"
)

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

	// Bootstrap OTel (instrument the pipeline itself)
	shutdown, err := otelsetup.Setup(ctx, otelsetup.Config{
		OTLPEndpoint:          cfg.OTLPEndpoint,
		ServiceName:           "gosentinel-pipeline",
		ServiceVersion:        "0.1.0",
		UsePrometheusExporter: true,
	})
	if err != nil {
		slog.Error("setting up OTel", "error", err)
		os.Exit(1)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := shutdown(shutdownCtx); err != nil {
			slog.Error("shutting down OTel", "error", err)
		}
	}()

	// Storage clients
	vmClient := storage.NewVictoriaMetricsClient(cfg.VictoriaMetrics.Endpoint)

	// Signal channels
	tracesCh := make(chan *correlation.TraceSpan, 1024)
	metricsCh := make(chan *correlation.MetricPoint, 1024)
	logsCh := make(chan *correlation.LogLine, 1024)
	correlatedCh := make(chan *correlation.CorrelatedEvent, 256)
	sampledCh := make(chan []*sampling.Span, 256)
	anomalyMetricsCh := make(chan *anomaly.MetricPoint, 1024)
	anomaliesCh := make(chan *anomaly.AnomalyEvent, 256)

	// Correlation engine
	corrEngine, err := correlation.NewCorrelationEngine(
		tracesCh, metricsCh, logsCh, correlatedCh,
		cfg.Pipeline.CorrelationWindow,
	)
	if err != nil {
		slog.Error("creating correlation engine", "error", err)
		os.Exit(1)
	}

	// Tail sampler
	policy := &sampling.CompositePolicy{
		Policies: []sampling.SamplingPolicy{
			&sampling.AlwaysSampleErrors{},
			&sampling.LatencyPolicy{Threshold: cfg.Pipeline.LatencyThreshold},
			&sampling.ProbabilisticPolicy{Rate: cfg.Pipeline.SamplingRate},
		},
	}
	tailSampler, err := sampling.NewTailSampler(policy, cfg.Pipeline.BufferTTL, sampledCh)
	if err != nil {
		slog.Error("creating tail sampler", "error", err)
		os.Exit(1)
	}

	// Anomaly detector registry
	detectorRegistry, err := anomaly.NewDetectorRegistry(0.1, 3.0)
	if err != nil {
		slog.Error("creating detector registry", "error", err)
		os.Exit(1)
	}

	// ── AlertManager setup ────────────────────────────────────────────────────

	dedupTTL, _ := time.ParseDuration(cfg.Alerting.DedupTTL)
	if dedupTTL == 0 {
		dedupTTL = 10 * time.Minute
	}

	alertMgr := alerting.NewAlertManager(dedupTTL).
		WithMetrics(alerting.NewAlertManagerMetrics())

	// Register notification channels based on available credentials.
	if cfg.Alerting.SlackWebhook != "" {
		alertMgr.Register("slack", alerting.NewSlackNotifier(cfg.Alerting.SlackWebhook), nil)
		slog.Info("alert channel registered", "channel", "slack")
	}
	if cfg.Alerting.PagerDutyKey != "" {
		alertMgr.Register("pagerduty", alerting.NewPagerDutyNotifier(cfg.Alerting.PagerDutyKey), nil)
		slog.Info("alert channel registered", "channel", "pagerduty")
	}
	if cfg.Alerting.GmailUsername != "" && cfg.Alerting.GmailPassword != "" && len(cfg.Alerting.GmailTo) > 0 {
		gmailCfg := alerting.GmailConfig{
			SMTPHost: cfg.Alerting.SMTPHost,
			SMTPPort: cfg.Alerting.SMTPPort,
			Username: cfg.Alerting.GmailUsername,
			Password: cfg.Alerting.GmailPassword,
			From:     cfg.Alerting.GmailUsername,
			To:       cfg.Alerting.GmailTo,
		}
		alertMgr.Register("gmail", alerting.NewGmailNotifier(gmailCfg), nil)
		slog.Info("alert channel registered", "channel", "gmail")
	}
	if cfg.Alerting.WebhookURL != "" {
		alertMgr.Register("webhook",
			alerting.NewWebhookNotifier(cfg.Alerting.WebhookURL, cfg.Alerting.WebhookSecret), nil)
		slog.Info("alert channel registered", "channel", "webhook")
	}
	if cfg.Alerting.OpsGenieKey != "" {
		alertMgr.Register("opsgenie",
			alerting.NewOpsGenieNotifier(cfg.Alerting.OpsGenieKey, cfg.Alerting.OpsGenieRegion), nil)
		slog.Info("alert channel registered", "channel", "opsgenie")
	}
	if cfg.Alerting.TeamsWebhook != "" {
		alertMgr.Register("teams", alerting.NewTeamsNotifier(cfg.Alerting.TeamsWebhook), nil)
		slog.Info("alert channel registered", "channel", "teams")
	}

	// Wire the rule evaluator: each named channel in a rule's notify list
	// delegates to the AlertManager so routing, dedup, escalation, and the
	// notification log are applied consistently.
	notifiers := map[string]alerting.Notifier{
		"slack":     alertMgr.AsNotifier(),
		"pagerduty": alertMgr.AsNotifier(),
		"gmail":     alertMgr.AsNotifier(),
		"webhook":   alertMgr.AsNotifier(),
		"opsgenie":  alertMgr.AsNotifier(),
		"teams":     alertMgr.AsNotifier(),
	}

	evaluator, err := alerting.NewRuleEvaluator(
		cfg.Alerting.RulesFile, vmClient, nil, notifiers, cfg.Pipeline.EvalInterval,
	)
	if err != nil {
		slog.Warn("creating rule evaluator (rules file may not exist yet)", "error", err)
		evaluator = nil
	}

	// Populate AlertManager routing from the loaded rules so each rule fans out
	// only to its declared channels (e.g. slack + gmail for "High p99 latency").
	if evaluator != nil {
		for _, rule := range evaluator.Rules() {
			if len(rule.Notify) > 0 {
				alertMgr.Routing.Set(rule.Name, rule.Notify)
			}
		}
		slog.Info("alert routing loaded from rules file", "rules", len(evaluator.Rules()))
	}

	// SLO tracker (example SLO)
	sloTracker := slo.NewSLOTracker(
		"api-availability", "order-service", 0.999, 30*24*time.Hour,
		`http_requests_total{status!~"5.."}`,
		`http_requests_total`,
		vmClient,
	)

	// ── Start all components ──────────────────────────────────────────────────

	go corrEngine.Run(ctx)
	go detectorRegistry.ObserveAll(ctx, anomalyMetricsCh, anomaliesCh)

	if evaluator != nil {
		go evaluator.Run(ctx)
	}

	// Drain correlated events (forward to Jaeger in production)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-correlatedCh:
				if !ok {
					return
				}
				slog.Info("correlated event",
					"trace_id", ev.TraceID,
					"service", ev.Service,
				)
			}
		}
	}()

	// Drain sampled traces
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case spans, ok := <-sampledCh:
				if !ok {
					return
				}
				if len(spans) > 0 {
					slog.Debug("sampled trace", "trace_id", spans[0].TraceID, "spans", len(spans))
				}
			}
		}
	}()

	// Drain anomalies — forward to AlertManager as synthetic alert events
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-anomaliesCh:
				if !ok {
					return
				}
				slog.Warn("anomaly detected",
					"service", ev.Service,
					"metric", ev.MetricName,
					"z_score", ev.ZScore,
				)
				// Optionally forward anomalies as alerts
				_ = alertMgr.Notify(ctx, &alerting.AlertEvent{
					ID:       ev.Service + "-anomaly-" + ev.MetricName,
					RuleName: "anomaly-" + ev.MetricName,
					State:    alerting.StateFiring,
					Severity: ev.Severity,
					Summary:  "EWMA anomaly detected: " + ev.MetricName,
					Service:  ev.Service,
					Value:    ev.ZScore,
					FiredAt:  ev.DetectedAt,
				})
			}
		}
	}()

	// SLO check loop
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				violations, err := sloTracker.Check(ctx)
				if err != nil {
					slog.Error("SLO check failed", "error", err)
					continue
				}
				for _, v := range violations {
					slog.Warn("SLO burn rate violation",
						"slo", v.SLOName,
						"burn_rate", v.BurnRate,
						"severity", v.Severity,
					)
				}
			}
		}
	}()

	// Expose /health and /metrics
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok","service":"gosentinel-pipeline"}`))
	})
	mux.Handle("/metrics", promhttp.Handler())

	srv := &http.Server{
		Addr:         cfg.Pipeline.ListenAddr,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	go func() {
		slog.Info("pipeline HTTP server starting", "addr", cfg.Pipeline.ListenAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("pipeline HTTP server error", "error", err)
		}
	}()

	// Keep tail sampler accessible (suppress unused warning)
	_ = tailSampler

	<-ctx.Done()
	slog.Info("pipeline shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("HTTP server shutdown error", "error", err)
	}
}
