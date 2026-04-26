// Package alerting — EscalationPolicy routes alerts to different channels
// based on severity, time-of-day, and repeat count.
package alerting

import (
	"sync"
	"time"
)

// EscalationLevel defines a single escalation tier.
type EscalationLevel struct {
	// After is how long the alert must be firing before this level activates.
	// Level 0 (immediate) should have After = 0.
	After time.Duration
	// Channels is the list of notifier names to use at this level.
	Channels []string
	// RepeatInterval suppresses re-notification at this level within the window.
	RepeatInterval time.Duration
}

// EscalationPolicy maps a rule name (or "*" wildcard) to an ordered list of
// escalation levels. Levels are evaluated in order; the first whose After
// threshold has been exceeded is used.
type EscalationPolicy struct {
	mu      sync.RWMutex
	levels  map[string][]EscalationLevel // rule → levels
	lastSent map[string]map[int]time.Time // rule → level → last sent time
}

// NewEscalationPolicy creates an empty EscalationPolicy.
func NewEscalationPolicy() *EscalationPolicy {
	return &EscalationPolicy{
		levels:   make(map[string][]EscalationLevel),
		lastSent: make(map[string]map[int]time.Time),
	}
}

// Set registers escalation levels for a rule name (use "*" for wildcard).
func (p *EscalationPolicy) Set(ruleName string, levels []EscalationLevel) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.levels[ruleName] = levels
}

// ChannelsFor returns the channels to notify for a rule that has been firing
// for firingDuration. It respects RepeatInterval to avoid re-notifying too
// frequently at the same level.
func (p *EscalationPolicy) ChannelsFor(ruleName string, firingDuration time.Duration) []string {
	p.mu.Lock()
	defer p.mu.Unlock()

	levels := p.mergedLevels(ruleName)
	if len(levels) == 0 {
		return nil
	}

	now := time.Now()
	var channels []string
	seen := make(map[string]struct{})

	for i, lvl := range levels {
		if firingDuration < lvl.After {
			continue
		}
		// Check repeat interval
		if lvl.RepeatInterval > 0 {
			if sentMap, ok := p.lastSent[ruleName]; ok {
				if last, ok := sentMap[i]; ok && now.Sub(last) < lvl.RepeatInterval {
					continue
				}
			}
		}
		// Record send time
		if _, ok := p.lastSent[ruleName]; !ok {
			p.lastSent[ruleName] = make(map[int]time.Time)
		}
		p.lastSent[ruleName][i] = now

		for _, ch := range lvl.Channels {
			if _, ok := seen[ch]; !ok {
				seen[ch] = struct{}{}
				channels = append(channels, ch)
			}
		}
	}
	return channels
}

// mergedLevels returns levels for a rule, merging wildcard "*" entries first.
// Caller must hold p.mu.
func (p *EscalationPolicy) mergedLevels(ruleName string) []EscalationLevel {
	var out []EscalationLevel
	if wc, ok := p.levels["*"]; ok {
		out = append(out, wc...)
	}
	if specific, ok := p.levels[ruleName]; ok {
		out = append(out, specific...)
	}
	return out
}

// Reset clears the last-sent tracking for a rule (call on alert resolution).
func (p *EscalationPolicy) Reset(ruleName string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.lastSent, ruleName)
}

// DefaultEscalationPolicy builds a sensible default policy:
//   - Level 0 (immediate): slack + gmail
//   - Level 1 (after 5m): + pagerduty
//   - Level 2 (after 15m): + opsgenie
func DefaultEscalationPolicy() *EscalationPolicy {
	ep := NewEscalationPolicy()
	ep.Set("*", []EscalationLevel{
		{After: 0, Channels: []string{"slack", "gmail"}, RepeatInterval: 10 * time.Minute},
		{After: 5 * time.Minute, Channels: []string{"pagerduty"}, RepeatInterval: 30 * time.Minute},
		{After: 15 * time.Minute, Channels: []string{"opsgenie"}, RepeatInterval: 60 * time.Minute},
	})
	return ep
}
