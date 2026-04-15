// Package alerting implements alert rule evaluation and state management.
package alerting

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"gopkg.in/yaml.v3"
)

// AlertState represents the lifecycle state of an alert.
type AlertState string

const (
	StatePending  AlertState = "pending"
	StateFiring   AlertState = "firing"
	StateResolved AlertState = "resolved"
)

// AlertRule defines a single alerting rule loaded from YAML.
type AlertRule struct {
	Name        string            `yaml:"name"`
	Expr        string            `yaml:"expr"`
	For         string            `yaml:"for"`
	Severity    string            `yaml:"severity"`
	Annotations map[string]string `yaml:"annotations"`
	Notify      []string          `yaml:"notify"`
}

// AlertRulesFile is the top-level YAML structure for alert rules.
type AlertRulesFile struct {
	Rules []AlertRule `yaml:"rules"`
}

// AlertEvent represents a state transition for an alert.
type AlertEvent struct {
	ID         string
	RuleName   string
	State      AlertState
	Severity   string
	Summary    string
	Service    string
	Value      float64
	FiredAt    time.Time
	ResolvedAt time.Time
	Labels     map[string]string
}

// VMQueryClient is the interface for querying VictoriaMetrics used by the evaluator.
type VMQueryClient interface {
	QueryInstant(ctx context.Context, expr string, t time.Time) (float64, error)
}

// Notifier sends alert notifications.
type Notifier interface {
	// Notify sends a notification for the given alert event.
	Notify(ctx context.Context, event *AlertEvent) error
}

// RuleEvaluator evaluates alert rules on a ticker and manages state in PostgreSQL.
type RuleEvaluator struct {
	rules     []AlertRule
	vmClient  VMQueryClient
	db        *pgxpool.Pool
	notifiers map[string]Notifier
	ticker    *time.Ticker
	interval  time.Duration

	// subscribers is a broadcast map of alert event channels.
	subscribers sync.Map

	mu           sync.RWMutex
	pendingSince map[string]time.Time // rule name -> time entered pending
}

// NewRuleEvaluator creates a RuleEvaluator from a YAML rules file.
func NewRuleEvaluator(rulesFile string, vm VMQueryClient, db *pgxpool.Pool,
	notifiers map[string]Notifier, interval time.Duration) (*RuleEvaluator, error) {

	data, err := os.ReadFile(rulesFile)
	if err != nil {
		return nil, fmt.Errorf("reading rules file %q: %w", rulesFile, err)
	}

	var rf AlertRulesFile
	if err := yaml.Unmarshal(data, &rf); err != nil {
		return nil, fmt.Errorf("parsing rules file: %w", err)
	}

	return &RuleEvaluator{
		rules:        rf.Rules,
		vmClient:     vm,
		db:           db,
		notifiers:    notifiers,
		ticker:       time.NewTicker(interval),
		interval:     interval,
		pendingSince: make(map[string]time.Time),
	}, nil
}

// Subscribe returns a channel that receives alert events. The caller must drain it.
func (e *RuleEvaluator) Subscribe() (<-chan *AlertEvent, func()) {
	ch := make(chan *AlertEvent, 64)
	key := fmt.Sprintf("%p", ch)
	e.subscribers.Store(key, ch)
	cancel := func() {
		e.subscribers.Delete(key)
		close(ch)
	}
	return ch, cancel
}

// Run starts the evaluation loop. It blocks until ctx is cancelled.
func (e *RuleEvaluator) Run(ctx context.Context) {
	defer e.ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			slog.InfoContext(ctx, "rule evaluator shutting down")
			return
		case <-e.ticker.C:
			e.evaluate(ctx)
		}
	}
}

// evaluate runs one evaluation cycle across all rules.
func (e *RuleEvaluator) evaluate(ctx context.Context) {
	now := time.Now()
	for _, rule := range e.rules {
		rule := rule
		value, err := e.vmClient.QueryInstant(ctx, rule.Expr, now)
		if err != nil {
			slog.ErrorContext(ctx, "evaluating rule",
				"rule", rule.Name, "error", err)
			continue
		}

		e.processResult(ctx, rule, value, now)
	}
}

// processResult handles state transitions for a single rule evaluation result.
func (e *RuleEvaluator) processResult(ctx context.Context, rule AlertRule, value float64, now time.Time) {
	// A non-zero value means the rule expression is firing.
	isFiring := value > 0

	currentState, err := e.getState(ctx, rule.Name)
	if err != nil {
		slog.ErrorContext(ctx, "getting alert state", "rule", rule.Name, "error", err)
		return
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	switch {
	case isFiring && currentState == "":
		// New: enter pending. If for-duration is 0, fire immediately.
		e.pendingSince[rule.Name] = now
		forDur, _ := time.ParseDuration(rule.For)
		if forDur == 0 {
			if err := e.setState(ctx, rule.Name, string(StateFiring)); err != nil {
				slog.ErrorContext(ctx, "setting firing state", "rule", rule.Name, "error", err)
				return
			}
			event := &AlertEvent{
				ID:       fmt.Sprintf("%s-%d", rule.Name, now.UnixNano()),
				RuleName: rule.Name,
				State:    StateFiring,
				Severity: rule.Severity,
				Summary:  rule.Annotations["summary"],
				Value:    value,
				FiredAt:  now,
				Labels:   rule.Annotations,
			}
			e.broadcast(event)
			e.notify(ctx, rule, event)
			return
		}
		if err := e.setState(ctx, rule.Name, string(StatePending)); err != nil {
			slog.ErrorContext(ctx, "setting pending state", "rule", rule.Name, "error", err)
		}

	case isFiring && currentState == string(StatePending):
		// Check if "for" duration has elapsed
		forDur, _ := time.ParseDuration(rule.For)
		if now.Sub(e.pendingSince[rule.Name]) >= forDur {
			if err := e.setState(ctx, rule.Name, string(StateFiring)); err != nil {
				slog.ErrorContext(ctx, "setting firing state", "rule", rule.Name, "error", err)
				return
			}
			event := &AlertEvent{
				ID:       fmt.Sprintf("%s-%d", rule.Name, now.UnixNano()),
				RuleName: rule.Name,
				State:    StateFiring,
				Severity: rule.Severity,
				Summary:  rule.Annotations["summary"],
				Value:    value,
				FiredAt:  now,
				Labels:   rule.Annotations,
			}
			e.broadcast(event)
			e.notify(ctx, rule, event)
		}

	case !isFiring && (currentState == string(StateFiring) || currentState == string(StatePending)):
		// Resolved
		delete(e.pendingSince, rule.Name)
		if err := e.setState(ctx, rule.Name, ""); err != nil {
			slog.ErrorContext(ctx, "clearing alert state", "rule", rule.Name, "error", err)
			return
		}
		event := &AlertEvent{
			ID:         fmt.Sprintf("%s-%d-resolved", rule.Name, now.UnixNano()),
			RuleName:   rule.Name,
			State:      StateResolved,
			Severity:   rule.Severity,
			Summary:    rule.Annotations["summary"],
			Value:      value,
			ResolvedAt: now,
			Labels:     rule.Annotations,
		}
		e.broadcast(event)
		e.notify(ctx, rule, event)
	}
}

// broadcast sends an alert event to all subscribers.
func (e *RuleEvaluator) broadcast(event *AlertEvent) {
	e.subscribers.Range(func(_, val any) bool {
		ch := val.(chan *AlertEvent)
		select {
		case ch <- event:
		default:
			// Drop if subscriber is slow
		}
		return true
	})
}

// notify calls all configured notifiers for a rule.
func (e *RuleEvaluator) notify(ctx context.Context, rule AlertRule, event *AlertEvent) {
	for _, name := range rule.Notify {
		n, ok := e.notifiers[name]
		if !ok {
			slog.WarnContext(ctx, "unknown notifier", "name", name)
			continue
		}
		if err := n.Notify(ctx, event); err != nil {
			slog.ErrorContext(ctx, "sending notification",
				"notifier", name, "rule", rule.Name, "error", err)
		}
	}
}

// getState retrieves the current alert state from PostgreSQL.
func (e *RuleEvaluator) getState(ctx context.Context, ruleName string) (string, error) {
	if e.db == nil {
		return "", nil
	}
	var state string
	err := e.db.QueryRow(ctx,
		`SELECT state FROM alert_states WHERE rule_name = $1`, ruleName,
	).Scan(&state)
	if err != nil {
		// No row means no state
		return "", nil //nolint:nilerr
	}
	return state, nil
}

// setState upserts the alert state in PostgreSQL.
func (e *RuleEvaluator) setState(ctx context.Context, ruleName, state string) error {
	if e.db == nil {
		return nil
	}
	if state == "" {
		_, err := e.db.Exec(ctx, `DELETE FROM alert_states WHERE rule_name = $1`, ruleName)
		return err
	}
	_, err := e.db.Exec(ctx,
		`INSERT INTO alert_states (rule_name, state, updated_at)
		 VALUES ($1, $2, NOW())
		 ON CONFLICT (rule_name) DO UPDATE SET state = $2, updated_at = NOW()`,
		ruleName, state,
	)
	return err
}
