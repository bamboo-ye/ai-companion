CREATE TABLE ops.alert_rules (
    id UUID PRIMARY KEY,
    name VARCHAR(128) NOT NULL,
    description VARCHAR(512) NOT NULL DEFAULT '',
    service VARCHAR(128),
    level VARCHAR(8) NOT NULL CHECK (level IN ('DEBUG', 'INFO', 'WARN', 'ERROR')),
    event_prefix VARCHAR(128),
    window_minutes INTEGER NOT NULL CHECK (window_minutes BETWEEN 1 AND 1440),
    threshold INTEGER NOT NULL CHECK (threshold BETWEEN 1 AND 1000000),
    severity VARCHAR(16) NOT NULL CHECK (severity IN ('warning', 'critical')),
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_by VARCHAR(128) NOT NULL,
    updated_by VARCHAR(128) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE ops.incidents (
    id UUID PRIMARY KEY,
    rule_id UUID NOT NULL REFERENCES ops.alert_rules(id),
    status VARCHAR(16) NOT NULL CHECK (status IN ('open', 'acknowledged', 'resolved')),
    severity VARCHAR(16) NOT NULL CHECK (severity IN ('warning', 'critical')),
    title VARCHAR(128) NOT NULL,
    summary VARCHAR(512) NOT NULL,
    service VARCHAR(128),
    level VARCHAR(8) NOT NULL,
    event_prefix VARCHAR(128),
    observed_value INTEGER NOT NULL,
    threshold INTEGER NOT NULL,
    window_minutes INTEGER NOT NULL,
    window_started_at TIMESTAMPTZ NOT NULL,
    window_ended_at TIMESTAMPTZ NOT NULL,
    evidence JSONB NOT NULL DEFAULT '{}'::jsonb,
    opened_at TIMESTAMPTZ NOT NULL,
    acknowledged_at TIMESTAMPTZ,
    acknowledged_by VARCHAR(128),
    resolved_at TIMESTAMPTZ,
    resolved_by VARCHAR(128),
    resolution VARCHAR(512),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX incidents_active_rule_idx ON ops.incidents (rule_id) WHERE status IN ('open', 'acknowledged');
CREATE INDEX incidents_status_time_idx ON ops.incidents (status, opened_at DESC);
CREATE INDEX incidents_severity_time_idx ON ops.incidents (severity, opened_at DESC);

INSERT INTO ops.alert_rules
    (id,name,description,service,level,event_prefix,window_minutes,threshold,severity,enabled,created_by,updated_by)
VALUES
    ('30000000-0000-4000-8000-000000000001','API 错误突增','5 分钟内 API ERROR 日志达到 5 条时开单','ai-companion-api','ERROR',NULL,5,5,'critical',TRUE,'system','system'),
    ('30000000-0000-4000-8000-000000000002','Agent 执行错误','10 分钟内 Agent Worker ERROR 日志达到 3 条时开单','ai-companion-agent-worker','ERROR',NULL,10,3,'critical',TRUE,'system','system'),
    ('30000000-0000-4000-8000-000000000003','系统警告突增','15 分钟内任意服务 WARN 日志达到 10 条时开单',NULL,'WARN',NULL,15,10,'warning',TRUE,'system','system')
ON CONFLICT (id) DO NOTHING;
