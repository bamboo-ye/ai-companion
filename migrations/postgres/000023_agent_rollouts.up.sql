CREATE TABLE ops.agent_rollouts (
    id UUID PRIMARY KEY,
    environment VARCHAR(64) NOT NULL,
    agent_key VARCHAR(96) NOT NULL,
    agent_version_id UUID NOT NULL REFERENCES ops.config_versions (id),
    agent_version INTEGER NOT NULL CHECK (agent_version > 0),
    agent_fingerprint CHAR(64) NOT NULL,
    baseline_version_id UUID NOT NULL REFERENCES ops.config_versions (id),
    baseline_version INTEGER NOT NULL CHECK (baseline_version > 0),
    baseline_fingerprint CHAR(64) NOT NULL,
    traffic_percent SMALLINT NOT NULL CHECK (traffic_percent BETWEEN 1 AND 50),
    routing_revision INTEGER NOT NULL CHECK (routing_revision > 0),
    policy JSONB NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('running','ready','paused','promoted','aborted')),
    decision TEXT NOT NULL CHECK (decision IN ('collecting','pass','fail')),
    summary JSONB NOT NULL DEFAULT '{}'::JSONB,
    violations JSONB NOT NULL DEFAULT '[]'::JSONB,
    revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
    created_by VARCHAR(128) NOT NULL,
    updated_by VARCHAR(128) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    evaluated_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    CHECK (agent_version_id <> baseline_version_id)
);

CREATE UNIQUE INDEX agent_rollouts_active_environment_uq
    ON ops.agent_rollouts (environment)
    WHERE status IN ('running','ready');

CREATE INDEX agent_rollouts_agent_history_idx
    ON ops.agent_rollouts (agent_key, created_at DESC);

CREATE INDEX agent_rollouts_version_gate_idx
    ON ops.agent_rollouts (agent_version_id, agent_fingerprint, created_at DESC);

CREATE INDEX agent_rollouts_routing_idx
    ON ops.agent_rollouts (environment, status)
    WHERE status IN ('running','ready');
