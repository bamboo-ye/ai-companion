DELETE FROM ops.config_deployments WHERE config_kind = 'prompt';
DELETE FROM ops.config_versions WHERE config_kind = 'prompt';

DROP INDEX IF EXISTS ops.config_versions_prompt_idx;

ALTER TABLE ops.config_deployments
    DROP CONSTRAINT config_deployments_config_kind_check;

ALTER TABLE ops.config_deployments
    ADD CONSTRAINT config_deployments_config_kind_check
    CHECK (config_kind IN ('billing_plan', 'model_profile', 'agent_definition'));

ALTER TABLE ops.config_versions
    DROP CONSTRAINT config_versions_config_kind_check;

ALTER TABLE ops.config_versions
    ADD CONSTRAINT config_versions_config_kind_check
    CHECK (config_kind IN ('billing_plan', 'model_profile', 'agent_definition'));
