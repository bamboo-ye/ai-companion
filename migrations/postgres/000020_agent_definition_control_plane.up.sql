ALTER TABLE ops.config_versions
    DROP CONSTRAINT config_versions_config_kind_check;

ALTER TABLE ops.config_versions
    ADD CONSTRAINT config_versions_config_kind_check
    CHECK (config_kind IN ('billing_plan', 'model_profile', 'agent_definition'));

ALTER TABLE ops.config_deployments
    DROP CONSTRAINT config_deployments_config_kind_check;

ALTER TABLE ops.config_deployments
    ADD CONSTRAINT config_deployments_config_kind_check
    CHECK (config_kind IN ('billing_plan', 'model_profile', 'agent_definition'));

CREATE INDEX config_versions_agent_definition_idx
    ON ops.config_versions (config_key, version DESC)
    WHERE config_kind = 'agent_definition';
