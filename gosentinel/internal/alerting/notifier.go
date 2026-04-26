// Package alerting implements alert notifiers for Slack, PagerDuty, Gmail, Webhook, OpsGenie, and Teams.
package alerting

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/smtp"
	"strings"
	"time"
)

// ── Slack Notifier ────────────────────────────────────────────────────────────

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

type slackPayload struct {
	Text        string            `json:"text"`
	Attachments []slackAttachment `json:"attachments"`
}

type slackAttachment struct {
	Color     string       `json:"color"`
	Title     string       `json:"title"`
	Text      string       `json:"text"`
	Footer    string       `json:"footer"`
	Ts        int64        `json:"ts"`
	Fields    []slackField `json:"fields,omitempty"`
	MarkdownIn []string    `json:"mrkdwn_in,omitempty"`
}

type slackField struct {
	Title string `json:"title"`
	Value string `json:"value"`
	Short bool   `json:"short"`
}

// Notify implements Notifier by posting a rich attachment to the Slack webhook.
func (n *SlackNotifier) Notify(ctx context.Context, event *AlertEvent) error {
	color := "#36a64f" // green = resolved
	emoji := "✅"
	if event.State == StateFiring {
		emoji = "🔥"
		switch event.Severity {
		case "critical":
			color = "#d32f2f"
		case "warning":
			color = "#f57c00"
		default:
			color = "#ffcc00"
		}
	}

	fields := []slackField{
		{Title: "Severity", Value: strings.ToUpper(event.Severity), Short: true},
		{Title: "State", Value: string(event.State), Short: true},
		{Title: "Value", Value: fmt.Sprintf("%.4f", event.Value), Short: true},
		{Title: "Fired At", Value: event.FiredAt.Format("2006-01-02 15:04:05 UTC"), Short: true},
	}
	if event.Service != "" {
		fields = append(fields, slackField{Title: "Service", Value: event.Service, Short: true})
	}

	payload := slackPayload{
		Text: fmt.Sprintf("%s *[%s]* Alert: *%s*", emoji, strings.ToUpper(string(event.State)), event.RuleName),
		Attachments: []slackAttachment{
			{
				Color:      color,
				Title:      event.RuleName,
				Text:       event.Summary,
				Footer:     "GoSentinel Alert Manager",
				Ts:         time.Now().Unix(),
				Fields:     fields,
				MarkdownIn: []string{"text", "fields"},
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

// ── PagerDuty Notifier ────────────────────────────────────────────────────────

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

type pdPayload struct {
	RoutingKey  string    `json:"routing_key"`
	EventAction string    `json:"event_action"` // trigger | resolve
	DedupKey    string    `json:"dedup_key"`
	Payload     pdDetails `json:"payload"`
	Links       []pdLink  `json:"links,omitempty"`
}

type pdDetails struct {
	Summary   string            `json:"summary"`
	Severity  string            `json:"severity"`
	Source    string            `json:"source"`
	Timestamp string            `json:"timestamp"`
	CustomDetails map[string]string `json:"custom_details,omitempty"`
}

type pdLink struct {
	Href string `json:"href"`
	Text string `json:"text"`
}

// Notify implements Notifier by posting to the PagerDuty Events API v2.
func (n *PagerDutyNotifier) Notify(ctx context.Context, event *AlertEvent) error {
	action := "trigger"
	if event.State == StateResolved {
		action = "resolve"
	}

	details := map[string]string{
		"value":    fmt.Sprintf("%.4f", event.Value),
		"fired_at": event.FiredAt.Format(time.RFC3339),
	}
	for k, v := range event.Labels {
		details[k] = v
	}

	payload := pdPayload{
		RoutingKey:  n.IntegrationKey,
		EventAction: action,
		DedupKey:    fmt.Sprintf("gosentinel-%s", event.RuleName),
		Payload: pdDetails{
			Summary:       fmt.Sprintf("[%s] %s: %s", strings.ToUpper(event.Severity), event.RuleName, event.Summary),
			Severity:      event.Severity,
			Source:        "gosentinel",
			Timestamp:     event.FiredAt.Format(time.RFC3339),
			CustomDetails: details,
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

// ── Gmail (SMTP) Notifier ────────────────────────────────────────────────────

// GmailConfig holds SMTP configuration for Gmail notifications.
type GmailConfig struct {
	// SMTPHost is the SMTP server host (default: smtp.gmail.com).
	SMTPHost string
	// SMTPPort is the SMTP server port.
	// Use 465 for implicit TLS, 587 for STARTTLS (default).
	SMTPPort int
	// Username is the Gmail address used to authenticate.
	Username string
	// Password is the Gmail App Password (not your account password).
	// Generate at: https://myaccount.google.com/apppasswords
	Password string
	// From is the sender address shown in the email.
	From string
	// To is the list of recipient addresses.
	To []string
}

// GmailNotifier sends alert notifications via Gmail SMTP.
type GmailNotifier struct {
	cfg  GmailConfig
	tmpl *template.Template
}

var emailHTMLTmpl = template.Must(template.New("email").Parse(`<!DOCTYPE html>
<html>
<head><meta charset="UTF-8"><style>
body{font-family:Arial,sans-serif;background:#f4f4f4;padding:20px;margin:0}
.card{background:#fff;border-radius:8px;padding:24px;max-width:600px;margin:auto;box-shadow:0 2px 8px rgba(0,0,0,.12)}
.header{border-bottom:1px solid #eee;padding-bottom:16px;margin-bottom:16px}
.badge{display:inline-block;padding:4px 12px;border-radius:4px;font-weight:bold;color:#fff;font-size:13px;text-transform:uppercase}
.critical{background:#d32f2f}.warning{background:#f57c00}.info{background:#1976d2}.resolved{background:#388e3c}
.meta{color:#666;font-size:13px;margin-top:16px;border-top:1px solid #eee;padding-top:12px}
.meta table{width:100%;border-collapse:collapse}
.meta td{padding:4px 8px 4px 0;vertical-align:top}
.meta td:first-child{font-weight:bold;white-space:nowrap;width:120px}
h2{margin:0 0 4px;font-size:20px}
h3{margin:12px 0 4px;font-size:16px;color:#333}
p{margin:4px 0;color:#444;line-height:1.5}
.footer{text-align:center;color:#999;font-size:11px;margin-top:20px}
</style></head>
<body>
<div class="card">
  <div class="header">
    <h2>🔔 GoSentinel Alert</h2>
    <span class="badge {{.Severity}}">{{.State}} · {{.Severity}}</span>
  </div>
  <h3>{{.RuleName}}</h3>
  <p>{{.Summary}}</p>
  <div class="meta">
    <table>
      <tr><td>Value</td><td>{{printf "%.4f" .Value}}</td></tr>
      {{if .Service}}<tr><td>Service</td><td>{{.Service}}</td></tr>{{end}}
      <tr><td>Fired At</td><td>{{.FiredAt.Format "2006-01-02 15:04:05 UTC"}}</td></tr>
      {{if not .ResolvedAt.IsZero}}<tr><td>Resolved At</td><td>{{.ResolvedAt.Format "2006-01-02 15:04:05 UTC"}}</td></tr>{{end}}
      {{range $k, $v := .Labels}}<tr><td>{{$k}}</td><td>{{$v}}</td></tr>{{end}}
    </table>
  </div>
  <div class="footer">Sent by GoSentinel Alert Manager</div>
</div>
</body></html>`))

// NewGmailNotifier creates a GmailNotifier.
func NewGmailNotifier(cfg GmailConfig) *GmailNotifier {
	if cfg.SMTPHost == "" {
		cfg.SMTPHost = "smtp.gmail.com"
	}
	if cfg.SMTPPort == 0 {
		cfg.SMTPPort = 587
	}
	if cfg.From == "" {
		cfg.From = cfg.Username
	}
	return &GmailNotifier{cfg: cfg, tmpl: emailHTMLTmpl}
}

// Notify implements Notifier by sending an HTML email via Gmail SMTP.
func (n *GmailNotifier) Notify(_ context.Context, event *AlertEvent) error {
	var body bytes.Buffer
	if err := n.tmpl.Execute(&body, event); err != nil {
		return fmt.Errorf("rendering email template: %w", err)
	}

	subject := fmt.Sprintf("[GoSentinel][%s] %s — %s",
		strings.ToUpper(string(event.State)), event.Severity, event.RuleName)

	msg := buildMIMEMessage(n.cfg.From, n.cfg.To, subject, body.String())
	auth := smtp.PlainAuth("", n.cfg.Username, n.cfg.Password, n.cfg.SMTPHost)

	// Port 465 → implicit TLS; port 587 → STARTTLS via smtp.SendMail
	if n.cfg.SMTPPort == 465 {
		return n.sendTLS(auth, msg)
	}
	addr := fmt.Sprintf("%s:%d", n.cfg.SMTPHost, n.cfg.SMTPPort)
	return smtp.SendMail(addr, auth, n.cfg.From, n.cfg.To, []byte(msg))
}

// sendTLS sends mail over an implicit TLS connection (port 465).
func (n *GmailNotifier) sendTLS(auth smtp.Auth, msg string) error {
	tlsCfg := &tls.Config{ServerName: n.cfg.SMTPHost, MinVersion: tls.VersionTLS12}
	addr := fmt.Sprintf("%s:465", n.cfg.SMTPHost)

	conn, err := tls.Dial("tcp", addr, tlsCfg)
	if err != nil {
		return fmt.Errorf("tls dial %s: %w", addr, err)
	}
	defer conn.Close()

	client, err := smtp.NewClient(conn, n.cfg.SMTPHost)
	if err != nil {
		return fmt.Errorf("smtp new client: %w", err)
	}
	defer client.Close()

	if err := client.Auth(auth); err != nil {
		return fmt.Errorf("smtp auth: %w", err)
	}
	if err := client.Mail(n.cfg.From); err != nil {
		return fmt.Errorf("smtp MAIL FROM: %w", err)
	}
	for _, to := range n.cfg.To {
		if err := client.Rcpt(to); err != nil {
			return fmt.Errorf("smtp RCPT TO %s: %w", to, err)
		}
	}
	wc, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp DATA: %w", err)
	}
	defer wc.Close()
	_, err = wc.Write([]byte(msg))
	return err
}

// buildMIMEMessage constructs a MIME email message with HTML body.
func buildMIMEMessage(from string, to []string, subject, htmlBody string) string {
	var sb strings.Builder
	sb.WriteString("MIME-Version: 1.0\r\n")
	sb.WriteString(fmt.Sprintf("From: GoSentinel Alerts <%s>\r\n", from))
	sb.WriteString(fmt.Sprintf("To: %s\r\n", strings.Join(to, ", ")))
	sb.WriteString(fmt.Sprintf("Subject: %s\r\n", subject))
	sb.WriteString("Content-Type: text/html; charset=UTF-8\r\n")
	sb.WriteString("\r\n")
	sb.WriteString(htmlBody)
	return sb.String()
}

// ── Generic Webhook Notifier ─────────────────────────────────────────────────

// WebhookNotifier sends alert notifications to a generic HTTP webhook endpoint.
// The payload is a JSON-serialised AlertEvent. An optional HMAC-SHA256 signature
// is sent as the X-GoSentinel-Signature header.
type WebhookNotifier struct {
	URL    string
	Secret string // optional HMAC-SHA256 signing secret
	client *http.Client
}

type webhookPayload struct {
	Version    string            `json:"version"`
	Source     string            `json:"source"`
	ID         string            `json:"id"`
	RuleName   string            `json:"rule_name"`
	State      string            `json:"state"`
	Severity   string            `json:"severity"`
	Summary    string            `json:"summary"`
	Service    string            `json:"service,omitempty"`
	Value      float64           `json:"value"`
	FiredAt    time.Time         `json:"fired_at"`
	ResolvedAt *time.Time        `json:"resolved_at,omitempty"`
	Labels     map[string]string `json:"labels,omitempty"`
}

// NewWebhookNotifier creates a WebhookNotifier.
func NewWebhookNotifier(url, secret string) *WebhookNotifier {
	return &WebhookNotifier{
		URL:    url,
		Secret: secret,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// Notify implements Notifier by POSTing a JSON payload to the webhook URL.
func (n *WebhookNotifier) Notify(ctx context.Context, event *AlertEvent) error {
	p := webhookPayload{
		Version:  "1",
		Source:   "gosentinel",
		ID:       event.ID,
		RuleName: event.RuleName,
		State:    string(event.State),
		Severity: event.Severity,
		Summary:  event.Summary,
		Service:  event.Service,
		Value:    event.Value,
		FiredAt:  event.FiredAt,
		Labels:   event.Labels,
	}
	if !event.ResolvedAt.IsZero() {
		t := event.ResolvedAt
		p.ResolvedAt = &t
	}

	body, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("marshalling webhook payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "GoSentinel/1.0")
	if n.Secret != "" {
		req.Header.Set("X-GoSentinel-Signature", hmacSHA256Hex(n.Secret, body))
	}

	resp, err := n.client.Do(req)
	if err != nil {
		return fmt.Errorf("posting to webhook: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned status %d", resp.StatusCode)
	}
	return nil
}

// ── OpsGenie Notifier ────────────────────────────────────────────────────────

// OpsGenieNotifier sends alert notifications via OpsGenie Alerts API v2.
type OpsGenieNotifier struct {
	APIKey string
	Region string // "us" (default) or "eu"
	client *http.Client
}

// NewOpsGenieNotifier creates an OpsGenieNotifier.
func NewOpsGenieNotifier(apiKey, region string) *OpsGenieNotifier {
	if region == "" {
		region = "us"
	}
	return &OpsGenieNotifier{
		APIKey: apiKey,
		Region: region,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

type opsGeniePayload struct {
	Message     string            `json:"message"`
	Alias       string            `json:"alias"`
	Description string            `json:"description"`
	Priority    string            `json:"priority"`
	Source      string            `json:"source"`
	Tags        []string          `json:"tags"`
	Details     map[string]string `json:"details"`
}

type opsGenieClosePayload struct {
	Source string `json:"source"`
	Note   string `json:"note"`
}

// Notify implements Notifier by calling the OpsGenie Alerts API.
func (n *OpsGenieNotifier) Notify(ctx context.Context, event *AlertEvent) error {
	baseURL := "https://api.opsgenie.com"
	if n.Region == "eu" {
		baseURL = "https://api.eu.opsgenie.com"
	}

	alias := fmt.Sprintf("gosentinel-%s", event.RuleName)

	if event.State == StateResolved {
		return n.closeAlert(ctx, baseURL, alias)
	}

	priority := "P3"
	switch event.Severity {
	case "critical":
		priority = "P1"
	case "warning":
		priority = "P2"
	}

	details := map[string]string{
		"value":    fmt.Sprintf("%.4f", event.Value),
		"fired_at": event.FiredAt.Format(time.RFC3339),
	}
	for k, v := range event.Labels {
		details[k] = v
	}

	p := opsGeniePayload{
		Message:     fmt.Sprintf("[%s] %s", strings.ToUpper(event.Severity), event.RuleName),
		Alias:       alias,
		Description: event.Summary,
		Priority:    priority,
		Source:      "gosentinel",
		Tags:        []string{"gosentinel", event.Severity},
		Details:     details,
	}

	body, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("marshalling opsgenie payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/v2/alerts", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating opsgenie request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "GenieKey "+n.APIKey)

	resp, err := n.client.Do(req)
	if err != nil {
		return fmt.Errorf("posting to opsgenie: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("opsgenie returned status %d", resp.StatusCode)
	}
	return nil
}

func (n *OpsGenieNotifier) closeAlert(ctx context.Context, baseURL, alias string) error {
	p := opsGenieClosePayload{Source: "gosentinel", Note: "Alert resolved by GoSentinel"}
	body, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("marshalling opsgenie close payload: %w", err)
	}

	url := fmt.Sprintf("%s/v2/alerts/%s/close?identifierType=alias", baseURL, alias)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating opsgenie close request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "GenieKey "+n.APIKey)

	resp, err := n.client.Do(req)
	if err != nil {
		return fmt.Errorf("closing opsgenie alert: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("opsgenie close returned status %d", resp.StatusCode)
	}
	return nil
}

// ── Microsoft Teams Notifier ─────────────────────────────────────────────────

// TeamsNotifier sends alert notifications to a Microsoft Teams incoming webhook.
type TeamsNotifier struct {
	WebhookURL string
	client     *http.Client
}

// NewTeamsNotifier creates a TeamsNotifier with the given webhook URL.
func NewTeamsNotifier(webhookURL string) *TeamsNotifier {
	return &TeamsNotifier{
		WebhookURL: webhookURL,
		client:     &http.Client{Timeout: 10 * time.Second},
	}
}

type teamsPayload struct {
	Type        string        `json:"type"`
	Attachments []teamsAttach `json:"attachments"`
}

type teamsAttach struct {
	ContentType string    `json:"contentType"`
	Content     teamsCard `json:"content"`
}

type teamsCard struct {
	Schema  string       `json:"$schema"`
	Type    string       `json:"type"`
	Version string       `json:"version"`
	Body    []teamsBlock `json:"body"`
}

type teamsBlock struct {
	Type  string `json:"type"`
	Text  string `json:"text"`
	Wrap  bool   `json:"wrap,omitempty"`
	Style string `json:"style,omitempty"` // "heading" | "default"
	Size  string `json:"size,omitempty"`  // "Large" | "Medium"
	Color string `json:"color,omitempty"` // "Attention" | "Warning" | "Good"
}

// Notify implements Notifier by posting an Adaptive Card to the Teams webhook.
func (n *TeamsNotifier) Notify(ctx context.Context, event *AlertEvent) error {
	titleColor := "Good"
	if event.State == StateFiring {
		switch event.Severity {
		case "critical":
			titleColor = "Attention"
		case "warning":
			titleColor = "Warning"
		}
	}

	card := teamsPayload{
		Type: "message",
		Attachments: []teamsAttach{
			{
				ContentType: "application/vnd.microsoft.card.adaptive",
				Content: teamsCard{
					Schema:  "http://adaptivecards.io/schemas/adaptive-card.json",
					Type:    "AdaptiveCard",
					Version: "1.4",
					Body: []teamsBlock{
						{
							Type:  "TextBlock",
							Text:  fmt.Sprintf("🔔 GoSentinel Alert — %s", event.RuleName),
							Style: "heading", Size: "Large", Color: titleColor,
						},
						{
							Type: "TextBlock",
							Text: fmt.Sprintf("**State:** %s  |  **Severity:** %s", event.State, event.Severity),
							Wrap: true,
						},
						{Type: "TextBlock", Text: event.Summary, Wrap: true},
						{
							Type: "TextBlock",
							Text: fmt.Sprintf("**Value:** %.4f  |  **Fired:** %s",
								event.Value, event.FiredAt.Format("2006-01-02 15:04:05 UTC")),
							Wrap: true,
						},
					},
				},
			},
		},
	}

	body, err := json.Marshal(card)
	if err != nil {
		return fmt.Errorf("marshalling teams payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.WebhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating teams request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := n.client.Do(req)
	if err != nil {
		return fmt.Errorf("posting to teams: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("teams webhook returned status %d", resp.StatusCode)
	}
	return nil
}
