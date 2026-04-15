// Package health implements structured health checks following the
// Health Check Response Format for HTTP APIs (draft-inadarei-api-health-check).
// Pattern: Liveness + Readiness + Startup probes with dependency checks.
package health

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"runtime"
	"sync"
	"time"
)

// Status represents the health status of a component.
type Status string

const (
	StatusPass Status = "pass"
	StatusFail Status = "fail"
	StatusWarn Status = "warn"
)

// CheckFunc is a function that performs a single health check.
type CheckFunc func(ctx context.Context) ComponentHealth

// ComponentHealth is the health status of a single dependency.
type ComponentHealth struct {
	Status      Status        `json:"status"`
	ComponentID string        `json:"componentId,omitempty"`
	Output      string        `json:"output,omitempty"`
	Time        time.Time     `json:"time"`
	Duration    time.Duration `json:"observedDuration,omitempty"`
}

// HealthResponse is the full health check response body.
type HealthResponse struct {
	Status      Status                       `json:"status"`
	Version     string                       `json:"version"`
	Description string                       `json:"description"`
	Checks      map[string][]ComponentHealth `json:"checks,omitempty"`
	Output      string                       `json:"output,omitempty"`
}

// Checker runs registered health checks and exposes HTTP handlers.
type Checker struct {
	mu          sync.RWMutex
	checks      map[string]CheckFunc
	version     string
	description string
	// ready tracks whether the service has completed startup
	ready bool
}

// NewChecker creates a Checker with the given service metadata.
func NewChecker(version, description string) *Checker {
	return &Checker{
		checks:      make(map[string]CheckFunc),
		version:     version,
		description: description,
	}
}

// Register adds a named health check function.
func (c *Checker) Register(name string, fn CheckFunc) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.checks[name] = fn
}

// SetReady marks the service as ready to serve traffic.
func (c *Checker) SetReady(ready bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ready = ready
}

// LivenessHandler returns 200 if the process is alive (not deadlocked).
// Kubernetes uses this to decide whether to restart the pod.
func (c *Checker) LivenessHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/health+json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(HealthResponse{
		Status:      StatusPass,
		Version:     c.version,
		Description: c.description + " (liveness)",
	})
}

// ReadinessHandler returns 200 only when all dependencies are healthy.
// Kubernetes uses this to decide whether to send traffic to the pod.
func (c *Checker) ReadinessHandler(w http.ResponseWriter, r *http.Request) {
	c.mu.RLock()
	ready := c.ready
	checks := make(map[string]CheckFunc, len(c.checks))
	for k, v := range c.checks {
		checks[k] = v
	}
	c.mu.RUnlock()

	if !ready {
		w.Header().Set("Content-Type", "application/health+json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(HealthResponse{
			Status:  StatusFail,
			Output:  "service not yet ready",
			Version: c.version,
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	resp := c.runChecks(ctx, checks)
	status := http.StatusOK
	if resp.Status == StatusFail {
		status = http.StatusServiceUnavailable
	}

	w.Header().Set("Content-Type", "application/health+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(resp)
}

// HealthHandler returns the full health status including all dependency checks.
func (c *Checker) HealthHandler(w http.ResponseWriter, r *http.Request) {
	c.mu.RLock()
	checks := make(map[string]CheckFunc, len(c.checks))
	for k, v := range c.checks {
		checks[k] = v
	}
	c.mu.RUnlock()

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	resp := c.runChecks(ctx, checks)
	status := http.StatusOK
	if resp.Status == StatusFail {
		status = http.StatusServiceUnavailable
	}

	w.Header().Set("Content-Type", "application/health+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(resp)
}

// runChecks executes all registered checks concurrently.
func (c *Checker) runChecks(ctx context.Context, checks map[string]CheckFunc) HealthResponse {
	type result struct {
		name   string
		health ComponentHealth
	}

	results := make(chan result, len(checks))
	for name, fn := range checks {
		name, fn := name, fn
		go func() {
			start := time.Now()
			h := fn(ctx)
			h.Duration = time.Since(start)
			h.Time = time.Now()
			results <- result{name: name, health: h}
		}()
	}

	allChecks := make(map[string][]ComponentHealth)
	overallStatus := StatusPass

	for range checks {
		r := <-results
		allChecks[r.name] = []ComponentHealth{r.health}
		if r.health.Status == StatusFail {
			overallStatus = StatusFail
			slog.Warn("health check failed", "check", r.name, "output", r.health.Output)
		} else if r.health.Status == StatusWarn && overallStatus == StatusPass {
			overallStatus = StatusWarn
		}
	}

	return HealthResponse{
		Status:      overallStatus,
		Version:     c.version,
		Description: c.description,
		Checks:      allChecks,
	}
}

// ── Standard Check Factories ──────────────────────────────────────────────────

// HTTPCheck returns a CheckFunc that performs an HTTP GET health check.
func HTTPCheck(name, url string) CheckFunc {
	client := &http.Client{Timeout: 3 * time.Second}
	return func(ctx context.Context) ComponentHealth {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return ComponentHealth{Status: StatusFail, ComponentID: name,
				Output: fmt.Sprintf("creating request: %v", err)}
		}
		resp, err := client.Do(req)
		if err != nil {
			return ComponentHealth{Status: StatusFail, ComponentID: name,
				Output: fmt.Sprintf("GET %s: %v", url, err)}
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 500 {
			return ComponentHealth{Status: StatusFail, ComponentID: name,
				Output: fmt.Sprintf("HTTP %d from %s", resp.StatusCode, url)}
		}
		return ComponentHealth{Status: StatusPass, ComponentID: name}
	}
}

// GoroutineLeakCheck warns if goroutine count exceeds threshold.
func GoroutineLeakCheck(threshold int) CheckFunc {
	return func(_ context.Context) ComponentHealth {
		count := runtime.NumGoroutine()
		if count > threshold {
			return ComponentHealth{
				Status:      StatusWarn,
				ComponentID: "goroutines",
				Output:      fmt.Sprintf("goroutine count %d exceeds threshold %d", count, threshold),
			}
		}
		return ComponentHealth{Status: StatusPass, ComponentID: "goroutines"}
	}
}
