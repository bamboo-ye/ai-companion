CREATE TABLE ops.system_logs (
    id BIGSERIAL PRIMARY KEY,
    occurred_at TIMESTAMPTZ NOT NULL,
    service VARCHAR(128) NOT NULL,
    environment VARCHAR(64) NOT NULL,
    level VARCHAR(8) NOT NULL CHECK (level IN ('DEBUG', 'INFO', 'WARN', 'ERROR')),
    event VARCHAR(128) NOT NULL,
    message VARCHAR(512) NOT NULL,
    trace_id VARCHAR(128),
    run_id VARCHAR(128),
    node VARCHAR(128),
    error_code VARCHAR(128),
    attributes JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX system_logs_time_idx ON ops.system_logs (occurred_at DESC, id DESC);
CREATE INDEX system_logs_service_time_idx ON ops.system_logs (service, occurred_at DESC, id DESC);
CREATE INDEX system_logs_level_time_idx ON ops.system_logs (level, occurred_at DESC, id DESC);
CREATE INDEX system_logs_trace_idx ON ops.system_logs (trace_id, occurred_at DESC) WHERE trace_id IS NOT NULL;
CREATE INDEX system_logs_run_idx ON ops.system_logs (run_id, occurred_at DESC) WHERE run_id IS NOT NULL;
