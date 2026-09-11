CREATE TABLE ops.evaluation_runs (
    id UUID PRIMARY KEY,
    agent_version_id UUID NOT NULL REFERENCES ops.config_versions (id),
    agent_key VARCHAR(96) NOT NULL,
    agent_version INTEGER NOT NULL CHECK (agent_version > 0),
    agent_fingerprint CHAR(64) NOT NULL,
    suite_name VARCHAR(96) NOT NULL,
    suite_version VARCHAR(64) NOT NULL,
    suite_fingerprint CHAR(64) NOT NULL,
    suite JSONB NOT NULL,
    baseline_run_id UUID REFERENCES ops.evaluation_runs (id),
    status TEXT NOT NULL CHECK (status IN ('running', 'completed')),
    decision TEXT CHECK (decision IN ('pass', 'fail')),
    summary JSONB NOT NULL DEFAULT '{}'::JSONB,
    results JSONB NOT NULL DEFAULT '[]'::JSONB,
    created_by VARCHAR(128) NOT NULL,
    started_at TIMESTAMPTZ NOT NULL,
    completed_at TIMESTAMPTZ,
    CHECK (
        (status = 'running' AND decision IS NULL AND completed_at IS NULL)
        OR (status = 'completed' AND decision IS NOT NULL AND completed_at IS NOT NULL)
    )
);

CREATE INDEX evaluation_runs_agent_version_idx
    ON ops.evaluation_runs (agent_version_id, started_at DESC);

CREATE INDEX evaluation_runs_release_gate_idx
    ON ops.evaluation_runs (agent_version_id, agent_fingerprint, completed_at DESC)
    WHERE status = 'completed' AND decision = 'pass';

CREATE INDEX evaluation_runs_suite_idx
    ON ops.evaluation_runs (suite_name, suite_version, started_at DESC);
