// Command api — alert management REST handlers.
// Exposes CRUD for silences, alert history, notification log, routing, and
// a test-notification endpoint.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/yourorg/gosentinel/internal/alerting"
)

// AlertsHandler provides REST endpoints for the alert manager.
type AlertsHandler struct {
	mgr *alerting.AlertManager
}

// NewAlertsHandler creates an AlertsHandler backed by the given AlertManager.
func NewAlertsHandler(mgr *alerting.AlertManager) *AlertsHandler {
	return &AlertsHandler{mgr: mgr}
}

// Mount registers all alert management routes on the given router.
//
//	GET    /api/v1/alerts                    — list all recent alert events
//	GET    /api/v1/alerts/active             — list only firing alerts
//	GET    /api/v1/silences                  — list all silences
//	POST   /api/v1/silences                  — create a silence
//	DELETE /api/v1/silences/{id}             — delete a silence
//	GET    /api/v1/notifications             — notification delivery audit log
//	GET    /api/v1/notifications/{channel}   — log filtered by channel
//	GET    /api/v1/routing                   — current routing config
//	POST   /api/v1/routing                   — update routing for a rule
//	POST   /api/v1/alerts/test               — send a test notification
//	GET    /api/v1/channels                  — list registered notification channels
func (h *AlertsHandler) Mount(r chi.Router) {
	r.Get("/api/v1/alerts", h.listAlerts)
	r.Get("/api/v1/alerts/active", h.listActiveAlerts)
	r.Post("/api/v1/alerts/test", h.testNotification)

	r.Get("/api/v1/silences", h.listSilences)
	r.Post("/api/v1/silences", h.createSilence)
	r.Delete("/api/v1/silences/{id}", h.deleteSilence)

	r.Get("/api/v1/notifications", h.listNotifications)
	r.Get("/api/v1/notifications/{channel}", h.listNotificationsByChannel)

	r.Get("/api/v1/routing", h.listRouting)
	r.Post("/api/v1/routing", h.updateRouting)

	r.Get("/api/v1/channels", h.listChannels)
}

// ── Alert history ─────────────────────────────────────────────────────────────

func (h *AlertsHandler) listAlerts(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, h.mgr.Store.List())
}

func (h *AlertsHandler) listActiveAlerts(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, h.mgr.Store.Active())
}

// ── Test notification ─────────────────────────────────────────────────────────

// testNotificationRequest is the JSON body for POST /api/v1/alerts/test.
type testNotificationRequest struct {
	// Channel is the registered notifier name to test (e.g. "slack", "gmail").
	// Leave empty to test all registered channels.
	Channel  string `json:"channel"`
	RuleName string `json:"rule_name"` // defaults to "test-alert"
	Severity string `json:"severity"`  // defaults to "warning"
	Summary  string `json:"summary"`   // defaults to "GoSentinel test notification"
}

func (h *AlertsHandler) testNotification(w http.ResponseWriter, r *http.Request) {
	var req testNotificationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	if req.RuleName == "" {
		req.RuleName = "test-alert"
	}
	if req.Severity == "" {
		req.Severity = "warning"
	}
	if req.Summary == "" {
		req.Summary = "GoSentinel test notification — if you see this, the channel is working."
	}

	event := &alerting.AlertEvent{
		ID:       fmt.Sprintf("test-%d", time.Now().UnixNano()),
		RuleName: req.RuleName,
		State:    alerting.StateFiring,
		Severity: req.Severity,
		Summary:  req.Summary,
		Value:    1.0,
		FiredAt:  time.Now(),
	}

	// If a specific channel is requested, route only to that channel.
	if req.Channel != "" {
		h.mgr.Routing.Set("__test__", []string{req.Channel})
		event.RuleName = "__test__"
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	if err := h.mgr.Notify(ctx, event); err != nil {
		slog.ErrorContext(r.Context(), "test notification failed", "error", err)
		http.Error(w, fmt.Sprintf("notification error: %v", err), http.StatusInternalServerError)
		return
	}

	// Clean up temporary routing entry.
	if req.Channel != "" {
		h.mgr.Routing.Set("__test__", nil)
	}

	w.WriteHeader(http.StatusOK)
	writeJSON(w, map[string]string{"status": "sent", "channel": req.Channel})
}

// ── Silences ──────────────────────────────────────────────────────────────────

func (h *AlertsHandler) listSilences(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, h.mgr.Silences.List())
}

// createSilenceRequest is the JSON body for POST /api/v1/silences.
type createSilenceRequest struct {
	RuleName  string `json:"rule_name"`  // "" = silence all rules
	CreatedBy string `json:"created_by"`
	Comment   string `json:"comment"`
	Duration  string `json:"duration"` // e.g. "2h", "30m"
}

func (h *AlertsHandler) createSilence(w http.ResponseWriter, r *http.Request) {
	var req createSilenceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	dur, err := time.ParseDuration(req.Duration)
	if err != nil || dur <= 0 {
		http.Error(w, "invalid duration — use Go duration format e.g. '2h'", http.StatusBadRequest)
		return
	}

	now := time.Now()
	s := &alerting.Silence{
		RuleName:  req.RuleName,
		CreatedBy: req.CreatedBy,
		Comment:   req.Comment,
		StartsAt:  now,
		EndsAt:    now.Add(dur),
	}
	id := h.mgr.Silences.Add(s)
	s.ID = id

	slog.InfoContext(r.Context(), "silence created",
		"id", id, "rule", req.RuleName, "duration", req.Duration, "by", req.CreatedBy)

	w.WriteHeader(http.StatusCreated)
	writeJSON(w, s)
}

func (h *AlertsHandler) deleteSilence(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !h.mgr.Silences.Delete(id) {
		http.Error(w, "silence not found", http.StatusNotFound)
		return
	}
	slog.InfoContext(r.Context(), "silence deleted", "id", id)
	w.WriteHeader(http.StatusNoContent)
}

// ── Notification log ──────────────────────────────────────────────────────────

func (h *AlertsHandler) listNotifications(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, h.mgr.NLog.List())
}

func (h *AlertsHandler) listNotificationsByChannel(w http.ResponseWriter, r *http.Request) {
	channel := chi.URLParam(r, "channel")
	writeJSON(w, h.mgr.NLog.ByChannel(channel))
}

// ── Routing ───────────────────────────────────────────────────────────────────

func (h *AlertsHandler) listRouting(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, h.mgr.Routing.All())
}

// updateRoutingRequest is the JSON body for POST /api/v1/routing.
type updateRoutingRequest struct {
	// RuleName is the alert rule name, or "*" to match all rules.
	RuleName string   `json:"rule_name"`
	Channels []string `json:"channels"` // e.g. ["slack", "gmail"]
}

func (h *AlertsHandler) updateRouting(w http.ResponseWriter, r *http.Request) {
	var req updateRoutingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if req.RuleName == "" {
		http.Error(w, "rule_name is required", http.StatusBadRequest)
		return
	}

	h.mgr.Routing.Set(req.RuleName, req.Channels)
	slog.InfoContext(r.Context(), "routing updated",
		"rule", req.RuleName, "channels", req.Channels)

	writeJSON(w, map[string]any{
		"rule_name": req.RuleName,
		"channels":  req.Channels,
	})
}

// ── helpers ───────────────────────────────────────────────────────────────────

// writeJSON encodes v as JSON and writes it to w.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("encoding JSON response", "error", err)
	}
}

// ── Channels ──────────────────────────────────────────────────────────────────

// channelStatus describes a registered notification channel.
type channelStatus struct {
	Name string `json:"name"`
}

// listChannels returns the names of all registered notification channels.
func (h *AlertsHandler) listChannels(w http.ResponseWriter, _ *http.Request) {
	names := h.mgr.RegisteredChannels()
	out := make([]channelStatus, len(names))
	for i, n := range names {
		out[i] = channelStatus{Name: n}
	}
	writeJSON(w, out)
}
