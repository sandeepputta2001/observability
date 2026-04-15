package alerting

import (
	"context"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// mockVMClient is a test double for VMQueryClient.
type mockVMClient struct {
	value float64
}

func (m *mockVMClient) QueryInstant(_ context.Context, _ string, _ time.Time) (float64, error) {
	return m.value, nil
}

// mockNotifier records calls for assertion.
type mockNotifier struct {
	events []*AlertEvent
}

func (m *mockNotifier) Notify(_ context.Context, event *AlertEvent) error {
	m.events = append(m.events, event)
	return nil
}

func TestRuleEvaluator_BroadcastsOnFiring(t *testing.T) {
	vm := &mockVMClient{value: 1.0} // non-zero = firing
	notifier := &mockNotifier{}

	e := &RuleEvaluator{
		rules: []AlertRule{
			{
				Name:        "test-rule",
				Expr:        "some_metric > 0",
				For:         "0s",
				Severity:    "critical",
				Annotations: map[string]string{"summary": "test alert"},
				Notify:      []string{"mock"},
			},
		},
		vmClient:     vm,
		notifiers:    map[string]Notifier{"mock": notifier},
		ticker:       time.NewTicker(time.Hour), // won't tick in test
		pendingSince: make(map[string]time.Time),
	}

	ch, cancel := e.Subscribe()
	defer cancel()

	ctx := context.Background()
	e.evaluate(ctx)

	select {
	case event := <-ch:
		if event.State != StateFiring {
			t.Errorf("expected firing state, got %s", event.State)
		}
		if event.RuleName != "test-rule" {
			t.Errorf("expected rule name 'test-rule', got %s", event.RuleName)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for alert event")
	}
}

func TestRuleEvaluator_NoEventWhenNotFiring(t *testing.T) {
	vm := &mockVMClient{value: 0} // zero = not firing
	e := &RuleEvaluator{
		rules: []AlertRule{
			{Name: "quiet-rule", Expr: "metric == 0", For: "0s", Severity: "warning",
				Annotations: map[string]string{}},
		},
		vmClient:     vm,
		notifiers:    map[string]Notifier{},
		ticker:       time.NewTicker(time.Hour),
		pendingSince: make(map[string]time.Time),
	}

	ch, cancel := e.Subscribe()
	defer cancel()

	e.evaluate(context.Background())

	select {
	case ev := <-ch:
		t.Errorf("unexpected event: %+v", ev)
	case <-time.After(200 * time.Millisecond):
		// expected
	}
}

func TestAlertRulesFile_ParseYAML(t *testing.T) {
	data := []byte(`
rules:
  - name: "High error rate"
    expr: 'rate(http_requests_total{status="5xx"}[5m]) > 0.05'
    for: 2m
    severity: critical
    annotations:
      summary: "High error rate detected"
    notify:
      - slack
      - pagerduty
`)
	var rf AlertRulesFile
	if err := yaml.Unmarshal(data, &rf); err != nil {
		t.Fatalf("parsing rules: %v", err)
	}
	if len(rf.Rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rf.Rules))
	}
	if rf.Rules[0].Name != "High error rate" {
		t.Errorf("unexpected rule name: %s", rf.Rules[0].Name)
	}
	if len(rf.Rules[0].Notify) != 2 {
		t.Errorf("expected 2 notifiers, got %d", len(rf.Rules[0].Notify))
	}
}
