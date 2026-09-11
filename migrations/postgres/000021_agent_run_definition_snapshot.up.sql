ALTER TABLE agent.runs
    ADD COLUMN agent_definition_key VARCHAR(96),
    ADD COLUMN agent_definition_version_id UUID REFERENCES ops.config_versions (id),
    ADD COLUMN agent_definition_version INTEGER CHECK (agent_definition_version > 0),
    ADD COLUMN agent_definition_revision INTEGER CHECK (agent_definition_revision > 0),
    ADD COLUMN agent_definition_fingerprint CHAR(64),
    ADD COLUMN agent_definition_model_profile VARCHAR(96),
    ADD COLUMN agent_definition_snapshot JSONB;

ALTER TABLE agent.runs
    ADD CONSTRAINT agent_runs_definition_snapshot_object_ck
    CHECK (
        agent_definition_snapshot IS NULL
        OR jsonb_typeof(agent_definition_snapshot) = 'object'
    );

CREATE INDEX agent_runs_definition_version_idx
    ON agent.runs (agent_definition_version_id, created_at DESC)
    WHERE agent_definition_version_id IS NOT NULL;
