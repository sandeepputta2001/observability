// Package alerting implements alert notifiers for Slack and PagerDuty.
package alerting

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// SlackNotifier sends alert notifications to a Slack webhook.
type SlackNotifier struct {
	WebhookURL string
	client     *http.Client
}

// NewSlackNotifier creates a SlackNotifier with the given webhook URL.
func NewSlackNotifier(webhookURL string) *SlackNotifier {
	return &SlackNotifier{
		WebhookURL: webhookURL,
		client:     &http.Client{Timeout: 10 * time.Second},
	}
}

// slackPayload is the Slack incoming webhook payload.
type slackPayload struct {
	Text        string            `json:"text"`
	Attachments []slackAttachment `json:"attachments"`
}

type slackAttachment struct {
	Color  string `json:"color"`
	Title  string `json:"title"`
	Text   string `json:"text"`
	Footer string `json:"footer"`
	Ts     int64  `json:"ts"`
}

// Notify implements Notifier by posting a rich attachment to the Slack webhook.
func (n *SlackNotifier) Notify(ctx context.Context, event *AlertEvent) error {
	color := "#36a64f" // green = resolved
	if event.State == StateFiring {
		switch event.Severity {
		case "critical":
			color = "#ff0000"
		case "warning":
			color = "#ffa500"
		default:
			color = "#ffcc00"
		}
	}

	payload := slackPayload{
		Text: fmt.Sprintf("[%s] Alert: %s", event.State, event.RuleName),
		Attachments: []slackAttachment{
			{
				Color:  color,
				Title:  event.RuleName,
				Text:   fmt.Sprintf("%s\nValue: %.4f", event.Summary, event.Value),
				Footer: fmt.Sprintf("GoSentinel | severity: %s", event.Severity),
				Ts:     time.Now().Unix(),
			},
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshalling slack payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.WebhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating slack request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := n.client.Do(req)
	if err != nil {
		return fmt.Errorf("posting to slack: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("slack webhook returned status %d", resp.StatusCode)
	}
	return nil
}

// PagerDutyNotifier sends alert notifications via PagerDuty Events API v2.
type PagerDutyNotifier struct {
	IntegrationKey string
	client         *http.Client
}

// NewPagerDutyNotifier creates a PagerDutyNotifier with the given integration key.
func NewPagerDutyNotifier(integrationKey string) *PagerDutyNotifier {
	return &PagerDutyNotifier{
		IntegrationKey: integrationKey,
		client:         &http.Client{Timeout: 10 * time.Second},
	}
}

// pdPayload is the PagerDuty Events API v2 payload.
type pdPayload struct {
	RoutingKey  string    `json:"routing_key"`
	EventAction string    `json:"event_action"` // trigger | resolve
	DedupKey    string    `json:"dedup_key"`
	Payload     pdDetails `json:"payload"`
}

type pdDetails struct {
	Summary  string `json:"summary"`
	Severity string `json:"severity"`
	Source   string `json:"source"`
}

// Notify implements Notifier by posting to the PagerDuty Events API v2.
func (n *PagerDutyNotifier) Notify(ctx context.Context, event *AlertEvent) error {
	action := "trigger"
	if event.State == StateResolved {
		action = "resolve"
	}

	payload := pdPayload{
		RoutingKey:  n.IntegrationKey,
		EventAction: action,
		DedupKey:    event.RuleName,
		Payload: pdDetails{
			Summary:  fmt.Sprintf("[%s] %s: %s", event.Severity, event.RuleName, event.Summary),
			Severity: event.Severity,
			Source:   "gosentinel",
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshalling pagerduty payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://events.pagerduty.com/v2/enqueue", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating pagerduty request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := n.client.Do(req)
	if err != nil {
		return fmt.Errorf("posting to pagerduty: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("pagerduty returned status %d", resp.StatusCode)
	}
	return nil
}
