// Package alerting — AlertStore keeps an in-memory ring buffer of recent alert
// events and exposes a Silence manager so operators can mute noisy rules.
package alerting

import (
	"fmt"
	"sync"
	"time"
)

// ── Alert Store ──────────────────────────────────────────────────────────────

const defaultMaxEvents = 500

// AlertStore is a thread-safe ring buffer of recent AlertEvents.
type AlertStore struct {
	mu     sync.RWMutex
	events []*AlertEvent
	max    int
}

// NewAlertStore creates an AlertStore that retains up to max events.
func NewAlertStore(max int) *AlertStore {
	if max <= 0 {
		max = defaultMaxEvents
	}
	return &AlertStore{max: max}
}

// Record appends an event, evicting the oldest when the buffer is full.
func (s *AlertStore) Record(event *AlertEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
	if len(s.events) > s.max {
		s.events = s.events[len(s.events)-s.max:]
	}
}

// List returns a snapshot of all stored events, newest first.
func (s *AlertStore) List() []*AlertEvent {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*AlertEvent, len(s.events))
	for i, e := range s.events {
		out[len(s.events)-1-i] = e
	}
	return out
}

// Active returns only events whose State is StateFiring.
func (s *AlertStore) Active() []*AlertEvent {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*AlertEvent
	for i := len(s.events) - 1; i >= 0; i-- {
		if s.events[i].State == StateFiring {
			out = append(out, s.events[i])
		}
	}
	return out
}

// ── Silence Manager ──────────────────────────────────────────────────────────

// Silence suppresses notifications for a rule within a time window.
type Silence struct {
	ID        string    `json:"id"`
	RuleName  string    `json:"rule_name"`  // exact rule name to silence ("" = all)
	CreatedBy string    `json:"created_by"` // operator identifier
	Comment   string    `json:"comment"`
	StartsAt  time.Time `json:"starts_at"`
	EndsAt    time.Time `json:"ends_at"`
}

// IsActive returns true if the silence is currently in effect.
func (s *Silence) IsActive(now time.Time) bool {
	return !now.Before(s.StartsAt) && now.Before(s.EndsAt)
}

// SilenceManager stores and evaluates silences.
type SilenceManager struct {
	mu       sync.RWMutex
	silences map[string]*Silence // id -> silence
}

// NewSilenceManager creates an empty SilenceManager.
func NewSilenceManager() *SilenceManager {
	return &SilenceManager{silences: make(map[string]*Silence)}
}

// Add registers a new silence and returns its generated ID.
func (m *SilenceManager) Add(s *Silence) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s.ID == "" {
		s.ID = fmt.Sprintf("silence-%d", time.Now().UnixNano())
	}
	m.silences[s.ID] = s
	return s.ID
}

// Delete removes a silence by ID.
func (m *SilenceManager) Delete(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.silences[id]; !ok {
		return false
	}
	delete(m.silences, id)
	return true
}

// List returns all silences (active and expired).
func (m *SilenceManager) List() []*Silence {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Silence, 0, len(m.silences))
	for _, s := range m.silences {
		out = append(out, s)
	}
	return out
}

// IsSilenced returns true if the given rule is currently silenced.
func (m *SilenceManager) IsSilenced(ruleName string) bool {
	now := time.Now()
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, s := range m.silences {
		if !s.IsActive(now) {
			continue
		}
		if s.RuleName == "" || s.RuleName == ruleName {
			return true
		}
	}
	return false
}
