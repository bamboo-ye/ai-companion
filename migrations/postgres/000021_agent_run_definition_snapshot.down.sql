DROP INDEX IF EXISTS agent.agent_runs_definition_version_idx;

ALTER TABLE agent.runs
    DROP CONSTRAINT IF EXISTS agent_runs_definition_snapshot_object_ck,
    DROP COLUMN IF EXISTS agent_definition_snapshot,
    DROP COLUMN IF EXISTS agent_definition_model_profile,
    DROP COLUMN IF EXISTS agent_definition_fingerprint,
    DROP COLUMN IF EXISTS agent_definition_revision,
    DROP COLUMN IF EXISTS agent_definition_version,
    DROP COLUMN IF EXISTS agent_definition_version_id,
    DROP COLUMN IF EXISTS agent_definition_key;
