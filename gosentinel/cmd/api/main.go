// Command api is the GoSentinel gRPC + REST query API server.
// It exposes QueryService and SLOService via connectrpc (gRPC + HTTP/JSON on same port).
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"connectrpc.com/connect"
	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/yourorg/gosentinel/internal/alerting"
	"github.com/yourorg/gosentinel/internal/anomaly"
	"github.com/yourorg/gosentinel/internal/storage"
	"github.com/yourorg/gosentinel/pkg/config"
	"github.com/yourorg/gosentinel/pkg/middleware"
	otelsetup "github.com/yourorg/gosentinel/pkg/otel"
	"golang.org/x/sync/errgroup"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/yourorg/gosentinel/gen/go/gosentinel/v1"
	pbconnect "github.com/yourorg/gosentinel/gen/go/gosentinel/v1/gosentinelv1connect"
)

// QueryServer implements QueryService.
type QueryServer struct {
	jaeger    *storage.JaegerClient
	vm        *storage.VictoriaMetricsClient
	loki      *storage.LokiClient
	pyroscope *storage.PyroscopeClient
	anomalies *anomaly.DetectorRegistry
	evaluator *alerting.RuleEvaluator
}

// GetServiceHealth fans out to all backends concurrently using errgroup.
func (s *QueryServer) GetServiceHealth(
	ctx context.Context,
	req *connect.Request[pb.GetServiceHealthRequest],
) (*connect.Response[pb.GetServiceHealthResponse], error) {
	service := req.Msg.GetService()
	lookback := time.Duration(req.Msg.GetLookbackMinutes()) * time.Minute
	if lookback == 0 {
		lookback = 60 * time.Minute
	}
	now := time.Now()
	start := now.Add(-lookback)

	g, gctx := errgroup.WithContext(ctx)

	var (
		errorRate, requestRate float64
		p50, p95, p99          float64
		topSpans               []*pb.SpanSummary
		hotspot                string
	)

	g.Go(func() error {
		er, err := s.vm.QueryInstant(gctx,
			fmt.Sprintf(`rate(http_requests_total{service="%s",status=~"5.."}[5m])`, service), now)
		if err != nil {
			return fmt.Errorf("error rate: %w", err)
		}
		errorRate = er
		rr, err := s.vm.QueryInstant(gctx,
			fmt.Sprintf(`rate(http_requests_total{service="%s"}[5m])`, service), now)
		if err != nil {
			return fmt.Errorf("request rate: %w", err)
		}
		requestRate = rr
		for _, pct := range []struct {
			q   float64
			dst *float64
		}{{0.5, &p50}, {0.95, &p95}, {0.99, &p99}} {
			val, err := s.vm.QueryInstant(gctx,
				fmt.Sprintf(`histogram_quantile(%g, rate(http_request_duration_seconds_bucket{service="%s"}[5m]))`, pct.q, service), now)
			if err != nil {
				return fmt.Errorf("p%.0f: %w", pct.q*100, err)
			}
			*pct.dst = val * 1000
		}
		return nil
	})

	g.Go(func() error {
		traces, err := s.jaeger.FindTraces(gctx, &storage.TraceQueryParameters{
			ServiceName: service,
			Tags:        map[string]string{"error": "true"},
			StartTime:   start,
			EndTime:     now,
			NumTraces:   5,
		})
		if err != nil {
			return fmt.Errorf("jaeger: %w", err)
		}
		for _, t := range traces {
			for _, sp := range t.Spans {
				if sp.Error {
					topSpans = append(topSpans, &pb.SpanSummary{
						TraceId: sp.TraceID, SpanId: sp.SpanID,
						Operation: sp.Operation, Service: sp.Service,
						DurationMs: float64(sp.Duration.Milliseconds()), Error: sp.Error,
					})
				}
			}
		}
		return nil
	})

	g.Go(func() error {
		fns, err := s.pyroscope.GetTopFunctions(gctx, service, start, now, 1)
		if err != nil {
			return fmt.Errorf("pyroscope: %w", err)
		}
		if len(fns) > 0 {
			hotspot = fns[0].FunctionName
		}
		return nil
	})

	if err := g.Wait(); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&pb.GetServiceHealthResponse{
		Service: service, ErrorRate: errorRate,
		P50LatencyMs: p50, P95LatencyMs: p95, P99LatencyMs: p99,
		RequestRate: requestRate, TopErrorSpans: topSpans,
		CpuHotspotFunction: hotspot, QueriedAt: timestamppb.New(now),
	}), nil
}

// GetCorrelatedTrace implements QueryService.GetCorrelatedTrace.
func (s *QueryServer) GetCorrelatedTrace(
	ctx context.Context,
	req *connect.Request[pb.GetCorrelatedTraceRequest],
) (*connect.Response[pb.GetCorrelatedTraceResponse], error) {
	trace, err := s.jaeger.GetTrace(ctx, req.Msg.GetTraceId())
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("getting trace: %w", err))
	}
	var spans []*pb.SpanSummary
	for _, sp := range trace.Spans {
		spans = append(spans, &pb.SpanSummary{
			TraceId: sp.TraceID, SpanId: sp.SpanID,
			Operation: sp.Operation, Service: sp.Service,
			DurationMs: float64(sp.Duration.Milliseconds()), Error: sp.Error,
		})
	}
	event := &pb.CorrelatedEvent{TraceId: req.Msg.GetTraceId(), Timestamp: timestamppb.Now()}
	if len(spans) > 0 {
		event.Span = spans[0]
	}
	return connect.NewResponse(&pb.GetCorrelatedTraceResponse{Event: event}), nil
}

// ListAnomalies implements QueryService.ListAnomalies.
func (s *QueryServer) ListAnomalies(
	_ context.Context,
	_ *connect.Request[pb.ListAnomaliesRequest],
) (*connect.Response[pb.ListAnomaliesResponse], error) {
	return connect.NewResponse(&pb.ListAnomaliesResponse{}), nil
}

// StreamAlerts implements QueryService.StreamAlerts as a server-streaming RPC.
func (s *QueryServer) StreamAlerts(
	ctx context.Context,
	req *connect.Request[pb.StreamAlertsRequest],
	stream *connect.ServerStream[pb.AlertEvent],
) error {
	if s.evaluator == nil {
		<-ctx.Done()
		return nil
	}
	ch, cancel := s.evaluator.Subscribe()
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return nil
		case event, ok := <-ch:
			if !ok {
				return nil
			}
			if svc := req.Msg.GetService(); svc != "" && event.Service != svc {
				continue
			}
			if err := stream.Send(&pb.AlertEvent{
				Id: event.ID, RuleName: event.RuleName,
				State: string(event.State), Severity: event.Severity,
				Summary: event.Summary, Service: event.Service,
				Value: event.Value, FiredAt: timestamppb.New(event.FiredAt),
			}); err != nil {
				return fmt.Errorf("sending alert: %w", err)
			}
		}
	}
}

// SLOServer implements SLOService.
type SLOServer struct{}

// ListSLOs implements SLOService.ListSLOs.
func (s *SLOServer) ListSLOs(_ context.Context, _ *connect.Request[pb.ListSLOsRequest]) (*connect.Response[pb.ListSLOsResponse], error) {
	return connect.NewResponse(&pb.ListSLOsResponse{}), nil
}

// GetErrorBudget implements SLOService.GetErrorBudget.
func (s *SLOServer) GetErrorBudget(_ context.Context, _ *connect.Request[pb.GetErrorBudgetRequest]) (*connect.Response[pb.GetErrorBudgetResponse], error) {
	return connect.NewResponse(&pb.GetErrorBudgetResponse{QueriedAt: timestamppb.Now()}), nil
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
		OTLPEndpoint: cfg.OTLPEndpoint, ServiceName: "gosentinel-api",
		ServiceVersion: "0.1.0", UsePrometheusExporter: true,
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

	jaegerClient, err := storage.NewJaegerClient(cfg.Jaeger.Endpoint)
	if err != nil {
		slog.Error("creating jaeger client", "error", err)
		os.Exit(1)
	}
	defer jaegerClient.Close()

	vmClient := storage.NewVictoriaMetricsClient(cfg.VictoriaMetrics.Endpoint)
	lokiClient := storage.NewLokiClient(cfg.Loki.Endpoint)
	pyroscopeClient := storage.NewPyroscopeClient(cfg.Pyroscope.Endpoint)
	_ = lokiClient

	detectorRegistry, err := anomaly.NewDetectorRegistry(0.1, 3.0)
	if err != nil {
		slog.Error("creating detector registry", "error", err)
		os.Exit(1)
	}

	queryServer := &QueryServer{
		jaeger: jaegerClient, vm: vmClient,
		pyroscope: pyroscopeClient, anomalies: detectorRegistry,
	}
	sloServer := &SLOServer{}

	queryPath, queryHandler := pbconnect.NewQueryServiceHandler(queryServer,
		connect.WithInterceptors(loggingInterceptor()),
	)
	sloPath, sloHandler := pbconnect.NewSLOServiceHandler(sloServer)

	r := chi.NewRouter()
	r.Use(chimiddleware.Recoverer)
	r.Use(middleware.RequestLogger)
	r.Use(middleware.RateLimiter(cfg.API.RateLimit))
	r.Mount(queryPath, queryHandler)
	r.Mount(sloPath, sloHandler)
	r.Get("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","service":"gosentinel-api"}`))
	})
	r.Handle("/metrics", promhttp.Handler())

	srv := &http.Server{
		Addr: cfg.API.ListenAddr, Handler: r,
		ReadTimeout: 10 * time.Second, WriteTimeout: 30 * time.Second,
	}
	go func() {
		slog.Info("API server starting", "addr", cfg.API.ListenAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("API server error", "error", err)
		}
	}()

	<-ctx.Done()
	slog.Info("API server shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("HTTP shutdown error", "error", err)
	}
}

// loggingInterceptor returns a connect interceptor that logs RPC calls.
func loggingInterceptor() connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			slog.InfoContext(ctx, "rpc", "procedure", req.Spec().Procedure)
			return next(ctx, req)
		}
	}
}
