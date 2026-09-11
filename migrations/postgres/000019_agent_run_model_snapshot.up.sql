ALTER TABLE agent.runs
    ADD COLUMN model_profile_key VARCHAR(96),
    ADD COLUMN model_profile_version_id UUID REFERENCES ops.config_versions (id),
    ADD COLUMN model_profile_revision INTEGER CHECK (model_profile_revision > 0),
    ADD COLUMN model_profile_config_version VARCHAR(128),
    ADD COLUMN model_profile_fingerprint CHAR(64),
    ADD COLUMN model_profile_snapshot JSONB;

ALTER TABLE agent.runs
    ADD CONSTRAINT agent_runs_model_profile_snapshot_object_ck
    CHECK (
        model_profile_snapshot IS NULL
        OR jsonb_typeof(model_profile_snapshot) = 'object'
    );

CREATE INDEX agent_runs_model_profile_version_idx
    ON agent.runs (model_profile_version_id, created_at DESC)
    WHERE model_profile_version_id IS NOT NULL;
