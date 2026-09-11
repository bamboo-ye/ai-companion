DROP INDEX IF EXISTS agent.agent_runs_model_profile_version_idx;

ALTER TABLE agent.runs
    DROP CONSTRAINT IF EXISTS agent_runs_model_profile_snapshot_object_ck,
    DROP COLUMN IF EXISTS model_profile_snapshot,
    DROP COLUMN IF EXISTS model_profile_fingerprint,
    DROP COLUMN IF EXISTS model_profile_config_version,
    DROP COLUMN IF EXISTS model_profile_revision,
    DROP COLUMN IF EXISTS model_profile_version_id,
    DROP COLUMN IF EXISTS model_profile_key;
