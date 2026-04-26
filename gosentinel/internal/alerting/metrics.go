// Package alerting — AlertManagerMetrics instruments the AlertManager with
// Prometheus counters and histograms for observability of the notification pipeline.
package alerting

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// AlertManagerMetrics holds all Prometheus metrics for the alert manager.
type AlertManagerMetrics struct {
	// NotificationsTotal counts notification attempts by channel and status.
	NotificationsTotal *prometheus.CounterVec

	// NotificationDuration tracks how long each notification delivery takes.
	NotificationDuration *prometheus.HistogramVec

	// AlertsTotal counts alert events by rule and state.
	AlertsTotal *prometheus.CounterVec

	// SilencedTotal counts alerts suppressed by silences.
	SilencedTotal *prometheus.CounterVec

	// DedupTotal counts alerts suppressed by deduplication.
	DedupTotal *prometheus.CounterVec

	// ActiveAlerts tracks the current number of firing alerts.
	ActiveAlerts prometheus.Gauge

	// RetryTotal counts retry attempts per channel.
	RetryTotal *prometheus.CounterVec
}

// NewAlertManagerMetrics registers and returns all alert manager metrics.
// Uses promauto so they are registered with the default Prometheus registry.
func NewAlertManagerMetrics() *AlertManagerMetrics {
	return &AlertManagerMetrics{
		NotificationsTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Namespace: "gosentinel",
			Subsystem: "alertmanager",
			Name:      "notifications_total",
			Help:      "Total notification delivery attempts by channel and status.",
		}, []string{"channel", "status"}),

		NotificationDuration: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "gosentinel",
			Subsystem: "alertmanager",
			Name:      "notification_duration_seconds",
			Help:      "Time taken to deliver a notification per channel.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"channel"}),

		AlertsTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Namespace: "gosentinel",
			Subsystem: "alertmanager",
			Name:      "alerts_total",
			Help:      "Total alert events processed by rule and state.",
		}, []string{"rule", "state", "severity"}),

		SilencedTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Namespace: "gosentinel",
			Subsystem: "alertmanager",
			Name:      "silenced_total",
			Help:      "Total alerts suppressed by active silences.",
		}, []string{"rule"}),

		DedupTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Namespace: "gosentinel",
			Subsystem: "alertmanager",
			Name:      "dedup_total",
			Help:      "Total alerts suppressed by deduplication.",
		}, []string{"rule"}),

		ActiveAlerts: promauto.NewGauge(prometheus.GaugeOpts{
			Namespace: "gosentinel",
			Subsystem: "alertmanager",
			Name:      "active_alerts",
			Help:      "Current number of firing alerts in the store.",
		}),

		RetryTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Namespace: "gosentinel",
			Subsystem: "alertmanager",
			Name:      "retry_total",
			Help:      "Total notification retry attempts per channel.",
		}, []string{"channel"}),
	}
}
