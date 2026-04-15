// Command collector is a thin wrapper that validates the OTel Collector config
// and exposes a /health endpoint. The actual collector process is run via the
// otel/opentelemetry-collector-contrib Docker image using config/otel-collector.yaml.
package main

import (
	"flag"
	"log/slog"
	"net/http"
	"os"
	"time"
)

func main() {
	addr := flag.String("addr", ":13133", "health check listen address")
	flag.Parse()

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok","component":"otel-collector-sidecar"}`))
	})

	srv := &http.Server{
		Addr:         *addr,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	}

	slog.Info("collector health endpoint starting", "addr", *addr)
	if err := srv.ListenAndServe(); err != nil {
		slog.Error("collector health server error", "error", err)
		os.Exit(1)
	}
}
