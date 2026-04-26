// Package alerting — RoutingConfig maps alert rule names (or "*" wildcard) to
// a set of notification channel names. It is loaded from the alert-rules YAML
// and consulted by the AlertManager before fan-out.
package alerting

import (
	"sync"
)

// RoutingConfig holds a thread-safe mapping of rule → channel names.
// A rule name of "*" matches every alert.
type RoutingConfig struct {
	mu     sync.RWMutex
	routes map[string][]string // rule name → channel names
}

// NewRoutingConfig creates an empty RoutingConfig.
func NewRoutingConfig() *RoutingConfig {
	return &RoutingConfig{routes: make(map[string][]string)}
}

// Set replaces the channel list for a rule.
func (rc *RoutingConfig) Set(ruleName string, channels []string) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rc.routes[ruleName] = channels
}

// ChannelsFor returns the deduplicated list of channel names that should
// receive notifications for the given rule. Wildcard "*" entries are merged.
func (rc *RoutingConfig) ChannelsFor(ruleName string) []string {
	rc.mu.RLock()
	defer rc.mu.RUnlock()

	seen := make(map[string]struct{})
	var out []string

	add := func(names []string) {
		for _, n := range names {
			if _, ok := seen[n]; !ok {
				seen[n] = struct{}{}
				out = append(out, n)
			}
		}
	}

	if ch, ok := rc.routes["*"]; ok {
		add(ch)
	}
	if ch, ok := rc.routes[ruleName]; ok {
		add(ch)
	}
	return out
}

// All returns a snapshot of all routes.
func (rc *RoutingConfig) All() map[string][]string {
	rc.mu.RLock()
	defer rc.mu.RUnlock()
	out := make(map[string][]string, len(rc.routes))
	for k, v := range rc.routes {
		cp := make([]string, len(v))
		copy(cp, v)
		out[k] = cp
	}
	return out
}
