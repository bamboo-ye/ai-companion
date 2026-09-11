DELETE FROM ops.config_deployments WHERE config_kind = 'agent_definition';
DELETE FROM ops.config_versions WHERE config_kind = 'agent_definition';

DROP INDEX IF EXISTS ops.config_versions_agent_definition_idx;

ALTER TABLE ops.config_deployments
    DROP CONSTRAINT config_deployments_config_kind_check;

ALTER TABLE ops.config_deployments
    ADD CONSTRAINT config_deployments_config_kind_check
    CHECK (config_kind IN ('billing_plan', 'model_profile'));

ALTER TABLE ops.config_versions
    DROP CONSTRAINT config_versions_config_kind_check;

ALTER TABLE ops.config_versions
    ADD CONSTRAINT config_versions_config_kind_check
    CHECK (config_kind IN ('billing_plan', 'model_profile'));
