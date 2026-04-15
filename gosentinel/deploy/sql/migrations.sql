-- GoSentinel Database Schema
-- Run with: psql "$GOSENTINEL_POSTGRES_DSN" -f deploy/sql/migrations.sql

BEGIN;

-- Alert state tracking (used by RuleEvaluator)
CREATE TABLE IF NOT EXISTS alert_states (
    rule_name   TEXT        PRIMARY KEY,
    state       TEXT        NOT NULL CHECK (state IN ('pending', 'firing')),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Alert history for audit trail
CREATE TABLE IF NOT EXISTS alert_history (
    id          BIGSERIAL   PRIMARY KEY,
    rule_name   TEXT        NOT NULL,
    state       TEXT        NOT NULL,
    severity    TEXT        NOT NULL,
    summary     TEXT,
    value       DOUBLE PRECISION,
    fired_at    TIMESTAMPTZ,
    resolved_at TIMESTAMPTZ,
    labels      JSONB,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_alert_history_rule_name ON alert_history(rule_name);
CREATE INDEX IF NOT EXISTS idx_alert_history_fired_at  ON alert_history(fired_at DESC);

-- SLO state
CREATE TABLE IF NOT EXISTS slo_state (
    slo_name              TEXT        PRIMARY KEY,
    service               TEXT        NOT NULL,
    target                DOUBLE PRECISION NOT NULL,
    error_budget_remaining DOUBLE PRECISION,
    burn_rate             DOUBLE PRECISION,
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Anomaly events for historical analysis
CREATE TABLE IF NOT EXISTS anomaly_events (
    id          BIGSERIAL   PRIMARY KEY,
    service     TEXT        NOT NULL,
    metric_name TEXT        NOT NULL,
    value       DOUBLE PRECISION NOT NULL,
    z_score     DOUBLE PRECISION NOT NULL,
    severity    TEXT        NOT NULL,
    detected_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_anomaly_service     ON anomaly_events(service, detected_at DESC);
CREATE INDEX IF NOT EXISTS idx_anomaly_detected_at ON anomaly_events(detected_at DESC);

COMMIT;
