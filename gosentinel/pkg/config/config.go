// Package config provides Viper-based configuration loading with environment variable overrides.
package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// Config holds all GoSentinel configuration.
type Config struct {
	OTLPEndpoint string `mapstructure:"otlp_endpoint"`
	ServiceName  string `mapstructure:"service_name"`

	Pipeline PipelineConfig `mapstructure:"pipeline"`
	API      APIConfig      `mapstructure:"api"`
	UI       UIConfig       `mapstructure:"ui"`

	Jaeger          JaegerConfig          `mapstructure:"jaeger"`
	VictoriaMetrics VictoriaMetricsConfig `mapstructure:"victoria_metrics"`
	Loki            LokiConfig            `mapstructure:"loki"`
	Pyroscope       PyroscopeConfig       `mapstructure:"pyroscope"`
	Postgres        PostgresConfig        `mapstructure:"postgres"`

	Alerting AlertingConfig `mapstructure:"alerting"`
}

// PipelineConfig holds pipeline binary configuration.
type PipelineConfig struct {
	ListenAddr        string        `mapstructure:"listen_addr"`
	OTLPReceiverAddr  string        `mapstructure:"otlp_receiver_addr"`
	CorrelationWindow time.Duration `mapstructure:"correlation_window"`
	BufferTTL         time.Duration `mapstructure:"buffer_ttl"`
	SamplingRate      float64       `mapstructure:"sampling_rate"`
	LatencyThreshold  time.Duration `mapstructure:"latency_threshold"`
	EvalInterval      time.Duration `mapstructure:"eval_interval"`
}

// APIConfig holds API server configuration.
type APIConfig struct {
	ListenAddr string `mapstructure:"listen_addr"`
	JWTSecret  string `mapstructure:"jwt_secret"`
	RateLimit  int    `mapstructure:"rate_limit"`
}

// UIConfig holds UI server configuration.
type UIConfig struct {
	ListenAddr string `mapstructure:"listen_addr"`
	APIAddr    string `mapstructure:"api_addr"`
}

// JaegerConfig holds Jaeger client configuration.
type JaegerConfig struct {
	Endpoint string `mapstructure:"endpoint"`
}

// VictoriaMetricsConfig holds VictoriaMetrics client configuration.
type VictoriaMetricsConfig struct {
	Endpoint string `mapstructure:"endpoint"`
}

// LokiConfig holds Loki client configuration.
type LokiConfig struct {
	Endpoint string `mapstructure:"endpoint"`
}

// PyroscopeConfig holds Pyroscope client configuration.
type PyroscopeConfig struct {
	Endpoint string `mapstructure:"endpoint"`
}

// PostgresConfig holds PostgreSQL connection configuration.
type PostgresConfig struct {
	DSN string `mapstructure:"dsn"`
}

// AlertingConfig holds alerting configuration.
type AlertingConfig struct {
	RulesFile    string `mapstructure:"rules_file"`
	DedupTTL     string `mapstructure:"dedup_ttl"` // e.g. "10m" — suppress duplicate notifications

	// Slack
	SlackWebhook string `mapstructure:"slack_webhook"`

	// PagerDuty
	PagerDutyKey string `mapstructure:"pagerduty_key"`

	// Gmail / SMTP
	GmailUsername string   `mapstructure:"gmail_username"` // sender Gmail address
	GmailPassword string   `mapstructure:"gmail_password"` // Gmail App Password
	GmailTo       []string `mapstructure:"gmail_to"`       // recipient addresses
	SMTPHost      string   `mapstructure:"smtp_host"`      // override host (default: smtp.gmail.com)
	SMTPPort      int      `mapstructure:"smtp_port"`      // override port (default: 587)

	// Generic Webhook
	WebhookURL    string `mapstructure:"webhook_url"`
	WebhookSecret string `mapstructure:"webhook_secret"` // optional HMAC signing secret

	// OpsGenie
	OpsGenieKey    string `mapstructure:"opsgenie_key"`
	OpsGenieRegion string `mapstructure:"opsgenie_region"` // "us" or "eu"

	// Microsoft Teams
	TeamsWebhook string `mapstructure:"teams_webhook"`
}

// Load reads configuration from file and environment variables.
// Environment variables override file values using the prefix GOSENTINEL_.
func Load(cfgFile string) (*Config, error) {
	v := viper.New()

	// Defaults
	v.SetDefault("otlp_endpoint", "localhost:4317")
	v.SetDefault("service_name", "gosentinel")
	v.SetDefault("pipeline.listen_addr", ":9090")
	v.SetDefault("pipeline.otlp_receiver_addr", ":4317")
	v.SetDefault("pipeline.correlation_window", "30s")
	v.SetDefault("pipeline.buffer_ttl", "10s")
	v.SetDefault("pipeline.sampling_rate", 0.1)
	v.SetDefault("pipeline.latency_threshold", "500ms")
	v.SetDefault("pipeline.eval_interval", "30s")
	v.SetDefault("api.listen_addr", ":8080")
	v.SetDefault("api.rate_limit", 100)
	v.SetDefault("ui.listen_addr", ":3000")
	v.SetDefault("ui.api_addr", "http://localhost:8080")
	v.SetDefault("jaeger.endpoint", "localhost:16685")
	v.SetDefault("victoria_metrics.endpoint", "http://localhost:8428")
	v.SetDefault("loki.endpoint", "http://localhost:3100")
	v.SetDefault("pyroscope.endpoint", "http://localhost:4040")
	v.SetDefault("postgres.dsn", "postgres://gosentinel:gosentinel@localhost:5432/gosentinel?sslmode=disable")
	v.SetDefault("alerting.rules_file", "config/alert-rules.yaml")
	v.SetDefault("alerting.dedup_ttl", "10m")
	v.SetDefault("alerting.smtp_host", "smtp.gmail.com")
	v.SetDefault("alerting.smtp_port", 587)
	v.SetDefault("alerting.opsgenie_region", "us")

	v.SetEnvPrefix("GOSENTINEL")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	if cfgFile != "" {
		v.SetConfigFile(cfgFile)
		if err := v.ReadInConfig(); err != nil {
			return nil, fmt.Errorf("reading config file: %w", err)
		}
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshalling config: %w", err)
	}

	return &cfg, nil
}
