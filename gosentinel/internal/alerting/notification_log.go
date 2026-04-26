// Package alerting — NotificationLog keeps an in-memory audit trail of every
// notification attempt (success or failure) made by the AlertManager.
package alerting

import (
	"sync"
	"time"
)

// NotificationStatus indicates whether a notification delivery succeeded.
type NotificationStatus string

const (
	NotificationSent   NotificationStatus = "sent"
	NotificationFailed NotificationStatus = "failed"
)

// NotificationRecord is a single delivery attempt entry.
type NotificationRecord struct {
	Timestamp time.Time          `json:"timestamp"`
	Channel   string             `json:"channel"`
	RuleName  string             `json:"rule_name"`
	State     string             `json:"state"`
	Severity  string             `json:"severity"`
	Status    NotificationStatus `json:"status"`
	Error     string             `json:"error,omitempty"`
}

const defaultMaxRecords = 1000

// NotificationLog is a thread-safe ring buffer of delivery records.
type NotificationLog struct {
	mu      sync.RWMutex
	records []*NotificationRecord
	max     int
}

// NewNotificationLog creates a NotificationLog that retains up to max records.
func NewNotificationLog(max int) *NotificationLog {
	if max <= 0 {
		max = defaultMaxRecords
	}
	return &NotificationLog{max: max}
}

// Record appends a delivery record, evicting the oldest when full.
func (l *NotificationLog) Record(r *NotificationRecord) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.records = append(l.records, r)
	if len(l.records) > l.max {
		l.records = l.records[len(l.records)-l.max:]
	}
}

// List returns a snapshot of all records, newest first.
func (l *NotificationLog) List() []*NotificationRecord {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]*NotificationRecord, len(l.records))
	for i, r := range l.records {
		out[len(l.records)-1-i] = r
	}
	return out
}

// ByChannel returns records for a specific channel, newest first.
func (l *NotificationLog) ByChannel(channel string) []*NotificationRecord {
	l.mu.RLock()
	defer l.mu.RUnlock()
	var out []*NotificationRecord
	for i := len(l.records) - 1; i >= 0; i-- {
		if l.records[i].Channel == channel {
			out = append(out, l.records[i])
		}
	}
	return out
}
