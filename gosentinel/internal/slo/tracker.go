// Package slo implements Google SRE Book multi-window burn rate SLO tracking.
package slo

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// VMQuerier is the interface for querying VictoriaMetrics (subset used by SLOTracker).
type VMQuerier interface {
	// QueryInstant executes a MetricsQL instant query and returns a scalar result.
	QueryInstant(ctx context.Context, expr string, t time.Time) (float64, error)
}

// BurnRateAlert defines a multi-window burn rate alert threshold.
type BurnRateAlert struct {
	// ShortWindow is the fast-burn detection window (e.g. 1h).
	ShortWindow time.Duration
	// LongWindow is the slow-burn confirmation window (e.g. 6h).
	LongWindow time.Duration
	// BurnRate is the multiplier above which the alert fires (e.g. 14.4 for fast burn).
	BurnRate float64
	// Severity is the alert severity label (e.g. "critical", "warning").
	Severity string
}

// BurnRateViolation is emitted when a burn rate threshold is exceeded.
type BurnRateViolation struct {
	SLOName     string
	Service     string
	BurnRate    float64
	ShortWindow time.Duration
	LongWindow  time.Duration
	Severity    string
	DetectedAt  time.Time
}

// SLOTracker tracks error budget burn rates for a single SLO.
type SLOTracker struct {
	// Name is the human-readable SLO name.
	Name string
	// Service is the service this SLO applies to.
	Service string
	// Target is the SLO target (e.g. 0.999 for 99.9%).
	Target float64
	// Window is the rolling compliance window (e.g. 30 days).
	Window time.Duration
	// GoodRequestsExpr is the MetricsQL expression for good (successful) requests.
	GoodRequestsExpr string
	// TotalRequestsExpr is the MetricsQL expression for total requests.
	TotalRequestsExpr string

	vmClient       VMQuerier
	burnRateAlerts []BurnRateAlert
}

// NewSLOTracker creates a tracker with Google SRE Book default multi-window burn rate alerts.
func NewSLOTracker(name, service string, target float64, window time.Duration,
	goodExpr, totalExpr string, vm VMQuerier) *SLOTracker {
	return &SLOTracker{
		Name:              name,
		Service:           service,
		Target:            target,
		Window:            window,
		GoodRequestsExpr:  goodExpr,
		TotalRequestsExpr: totalExpr,
		vmClient:          vm,
		burnRateAlerts: []BurnRateAlert{
			// Fast burn: 2% budget in 1h (14.4x burn rate)
			{ShortWindow: 1 * time.Hour, LongWindow: 5 * time.Minute, BurnRate: 14.4, Severity: "critical"},
			// Slow burn: 5% budget in 6h (6x burn rate)
			{ShortWindow: 6 * time.Hour, LongWindow: 30 * time.Minute, BurnRate: 6.0, Severity: "critical"},
			// Gradual burn: 10% budget in 3 days (3x burn rate)
			{ShortWindow: 3 * 24 * time.Hour, LongWindow: 6 * time.Hour, BurnRate: 3.0, Severity: "warning"},
			// Slow leak: 10% budget in 3 days (1x burn rate)
			{ShortWindow: 3 * 24 * time.Hour, LongWindow: 24 * time.Hour, BurnRate: 1.0, Severity: "warning"},
		},
	}
}

// ComputeBurnRate calculates the error budget burn rate over the given window.
// Burn rate = (1 - actual_availability) / (1 - target)
func (t *SLOTracker) ComputeBurnRate(ctx context.Context, window time.Duration) (float64, error) {
	now := time.Now()
	windowStr := durationToPromRange(window)

	goodExpr := fmt.Sprintf("sum(increase(%s[%s]))", t.GoodRequestsExpr, windowStr)
	totalExpr := fmt.Sprintf("sum(increase(%s[%s]))", t.TotalRequestsExpr, windowStr)

	good, err := t.vmClient.QueryInstant(ctx, goodExpr, now)
	if err != nil {
		return 0, fmt.Errorf("querying good requests: %w", err)
	}

	total, err := t.vmClient.QueryInstant(ctx, totalExpr, now)
	if err != nil {
		return 0, fmt.Errorf("querying total requests: %w", err)
	}

	if total == 0 {
		return 0, nil
	}

	availability := good / total
	errorRate := 1 - availability
	errorBudget := 1 - t.Target

	if errorBudget == 0 {
		return 0, nil
	}

	burnRate := errorRate / errorBudget
	return burnRate, nil
}

// Check evaluates all burn rate alert thresholds and returns any violations.
func (t *SLOTracker) Check(ctx context.Context) ([]BurnRateViolation, error) {
	var violations []BurnRateViolation

	for _, alert := range t.burnRateAlerts {
		shortBurn, err := t.ComputeBurnRate(ctx, alert.ShortWindow)
		if err != nil {
			slog.ErrorContext(ctx, "computing short window burn rate",
				"slo", t.Name, "window", alert.ShortWindow, "error", err)
			continue
		}

		longBurn, err := t.ComputeBurnRate(ctx, alert.LongWindow)
		if err != nil {
			slog.ErrorContext(ctx, "computing long window burn rate",
				"slo", t.Name, "window", alert.LongWindow, "error", err)
			continue
		}

		// Both windows must exceed the threshold (reduces false positives)
		if shortBurn >= alert.BurnRate && longBurn >= alert.BurnRate {
			violations = append(violations, BurnRateViolation{
				SLOName:     t.Name,
				Service:     t.Service,
				BurnRate:    shortBurn,
				ShortWindow: alert.ShortWindow,
				LongWindow:  alert.LongWindow,
				Severity:    alert.Severity,
				DetectedAt:  time.Now(),
			})
			slog.WarnContext(ctx, "SLO burn rate violation",
				"slo", t.Name,
				"service", t.Service,
				"burn_rate", shortBurn,
				"threshold", alert.BurnRate,
				"severity", alert.Severity,
			)
		}
	}

	return violations, nil
}

// durationToPromRange converts a Go duration to a Prometheus range string (e.g. "1h", "30m").
func durationToPromRange(d time.Duration) string {
	if d >= 24*time.Hour && d%(24*time.Hour) == 0 {
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
	if d >= time.Hour && d%time.Hour == 0 {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	if d >= time.Minute && d%time.Minute == 0 {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%ds", int(d.Seconds()))
}
