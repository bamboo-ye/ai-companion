CREATE TABLE ops.runtime_config_reports (
    instance_id VARCHAR(128) NOT NULL,
    service VARCHAR(64) NOT NULL,
    environment VARCHAR(64) NOT NULL,
    config_kind TEXT NOT NULL CHECK (config_kind IN ('billing_plan','model_profile','agent_definition','prompt')),
    config_key VARCHAR(96) NOT NULL,
    version_id UUID REFERENCES ops.config_versions (id),
    revision INTEGER NOT NULL DEFAULT 0 CHECK (revision >= 0),
    fingerprint CHAR(64),
    status TEXT NOT NULL CHECK (status IN ('applied','error')),
    last_error VARCHAR(1024) NOT NULL DEFAULT '',
    started_at TIMESTAMPTZ NOT NULL,
    last_seen_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (instance_id, config_kind, config_key),
    CHECK (fingerprint IS NULL OR LENGTH(fingerprint) = 64),
    CHECK (status <> 'applied' OR (version_id IS NOT NULL AND revision > 0 AND fingerprint IS NOT NULL))
);

CREATE INDEX runtime_config_reports_environment_seen_idx
    ON ops.runtime_config_reports (environment, last_seen_at DESC, service, instance_id);

COMMENT ON TABLE ops.runtime_config_reports IS
    'Runtime heartbeats proving the immutable configuration revision actually loaded by each service instance.';
