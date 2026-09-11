CREATE SCHEMA IF NOT EXISTS ops;

CREATE TABLE ops.config_versions (
    id UUID PRIMARY KEY,
    config_kind TEXT NOT NULL CHECK (config_kind IN ('billing_plan', 'model_profile')),
    config_key VARCHAR(96) NOT NULL,
    version INTEGER NOT NULL CHECK (version > 0),
    base_version INTEGER NOT NULL DEFAULT 0 CHECK (base_version >= 0),
    schema_version VARCHAR(64) NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('draft', 'validated', 'submitted', 'published', 'superseded')),
    payload JSONB NOT NULL,
    fingerprint CHAR(64) NOT NULL,
    reason VARCHAR(512) NOT NULL,
    created_by VARCHAR(128) NOT NULL,
    validated_by VARCHAR(128),
    submitted_by VARCHAR(128),
    published_by VARCHAR(128),
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    validated_at TIMESTAMPTZ,
    submitted_at TIMESTAMPTZ,
    published_at TIMESTAMPTZ,
    UNIQUE (config_kind, config_key, version)
);

CREATE INDEX config_versions_key_history_idx
    ON ops.config_versions (config_kind, config_key, version DESC);

CREATE INDEX config_versions_workflow_idx
    ON ops.config_versions (status, updated_at DESC);

CREATE TABLE ops.config_deployments (
    environment VARCHAR(64) NOT NULL,
    config_kind TEXT NOT NULL CHECK (config_kind IN ('billing_plan', 'model_profile')),
    config_key VARCHAR(96) NOT NULL,
    version_id UUID NOT NULL REFERENCES ops.config_versions (id),
    revision INTEGER NOT NULL CHECK (revision > 0),
    deployed_by VARCHAR(128) NOT NULL,
    deployed_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (environment, config_kind, config_key)
);

CREATE TABLE ops.provider_connections (
    key VARCHAR(96) PRIMARY KEY,
    provider_type VARCHAR(64) NOT NULL,
    display_name VARCHAR(120) NOT NULL,
    base_url TEXT NOT NULL,
    credential_ref VARCHAR(191) NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('active', 'disabled')),
    capabilities JSONB NOT NULL DEFAULT '{}'::JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE ops.model_catalog (
    model_id VARCHAR(191) PRIMARY KEY,
    provider_key VARCHAR(96) NOT NULL REFERENCES ops.provider_connections (key),
    display_name VARCHAR(191) NOT NULL,
    model_class VARCHAR(32) NOT NULL CHECK (model_class IN ('free', 'standard', 'quality')),
    context_window INTEGER NOT NULL CHECK (context_window > 0),
    max_output_tokens INTEGER NOT NULL CHECK (max_output_tokens > 0),
    prompt_price NUMERIC(12,6) NOT NULL CHECK (prompt_price >= 0),
    completion_price NUMERIC(12,6) NOT NULL CHECK (completion_price >= 0),
    zero_price BOOLEAN NOT NULL DEFAULT FALSE,
    status TEXT NOT NULL CHECK (status IN ('approved', 'disabled')),
    capabilities JSONB NOT NULL DEFAULT '{}'::JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

INSERT INTO ops.provider_connections (
    key,provider_type,display_name,base_url,credential_ref,status,capabilities
) VALUES (
    'openrouter-production','openrouter','OpenRouter Production',
    'https://openrouter.ai/api/v1','env:MODEL_API_KEY','active',
    '{"data_collection_policy":true,"zdr_routing":true,"provider_sort":true}'::jsonb
);

INSERT INTO ops.model_catalog (
    model_id,provider_key,display_name,model_class,context_window,max_output_tokens,
    prompt_price,completion_price,zero_price,status,capabilities
) VALUES
    ('deepseek/deepseek-v4-flash-0731','openrouter-production','DeepSeek V4 Flash 0731','standard',131072,32768,0.300000,2.500000,FALSE,'approved','{"text":true,"structured_output":true}'::jsonb),
    ('openai/gpt-5-mini','openrouter-production','OpenAI GPT-5 mini','quality',400000,32768,0.300000,2.500000,FALSE,'approved','{"text":true,"structured_output":true,"reasoning":true}'::jsonb),
    ('openai/gpt-oss-20b:free','openrouter-production','GPT OSS 20B Free','free',131072,32768,0,0,TRUE,'approved','{"text":true,"structured_output":true}'::jsonb),
    ('google/gemma-4-31b-it:free','openrouter-production','Gemma 4 31B IT Free','free',131072,32768,0,0,TRUE,'approved','{"text":true,"structured_output":true}'::jsonb);

INSERT INTO ops.config_versions (
    id,config_kind,config_key,version,base_version,schema_version,status,payload,
    fingerprint,reason,created_by,validated_by,submitted_by,published_by,
    created_at,updated_at,validated_at,submitted_at,published_at
) VALUES
    ('10000000-0000-4000-8000-000000000001','billing_plan','free',1,0,'billing-plan-v1','published',
     '{"display_name":"Free","status":"active","effective_mode":"immediate","limits":[{"resource":"documents_active","limit":10},{"resource":"skill_runs_monthly","limit":20},{"resource":"workspaces","limit":3}],"allowed_model_classes":["free"],"allowed_skills":[]}'::jsonb,
     '0000000000000000000000000000000000000000000000000000000000000001','Bootstrap existing Free plan','system','system','system','system',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP),
    ('10000000-0000-4000-8000-000000000002','billing_plan','pro',1,0,'billing-plan-v1','published',
     '{"display_name":"Pro","status":"active","effective_mode":"immediate","limits":[{"resource":"documents_active","limit":200},{"resource":"skill_runs_monthly","limit":1000},{"resource":"workspaces","limit":20}],"allowed_model_classes":["standard","quality"],"allowed_skills":[]}'::jsonb,
     '0000000000000000000000000000000000000000000000000000000000000002','Bootstrap existing Pro plan','system','system','system','system',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP),
    ('10000000-0000-4000-8000-000000000003','billing_plan','team',1,0,'billing-plan-v1','published',
     '{"display_name":"Team","status":"active","effective_mode":"immediate","limits":[{"resource":"documents_active","limit":1000},{"resource":"skill_runs_monthly","limit":5000},{"resource":"workspaces","limit":100}],"allowed_model_classes":["standard","quality"],"allowed_skills":[]}'::jsonb,
     '0000000000000000000000000000000000000000000000000000000000000003','Bootstrap existing Team plan','system','system','system','system',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP),
    ('20000000-0000-4000-8000-000000000001','model_profile','production-default',1,0,'model-profile-v1','published',
     '{"provider_connection":"openrouter-production","config_version":"bootstrap-v1","roles":{"planner":{"models":["deepseek/deepseek-v4-flash-0731"],"max_tokens":1024,"reasoning_effort":"high","timeout_ms":30000,"attempt_timeout_ms":15000},"router":{"models":["deepseek/deepseek-v4-flash-0731"],"max_tokens":256,"reasoning_effort":"low","timeout_ms":30000,"attempt_timeout_ms":15000},"composer":{"models":["openai/gpt-5-mini"],"max_tokens":12288,"reasoning_effort":"low","timeout_ms":90000,"attempt_timeout_ms":30000},"assessor":{"models":["deepseek/deepseek-v4-flash-0731"],"max_tokens":160,"reasoning_effort":"minimal","timeout_ms":30000,"attempt_timeout_ms":15000},"responder":{"models":["deepseek/deepseek-v4-flash-0731"],"max_tokens":1024,"reasoning_effort":"minimal","timeout_ms":30000,"attempt_timeout_ms":15000},"companion_responder":{"models":["openai/gpt-oss-20b:free","google/gemma-4-31b-it:free"],"max_tokens":1024,"reasoning_effort":"low","timeout_ms":30000,"attempt_timeout_ms":15000},"repairer":{"models":["deepseek/deepseek-v4-flash-0731"],"max_tokens":256,"reasoning_effort":"minimal","timeout_ms":30000,"attempt_timeout_ms":15000},"translation":{"models":["deepseek/deepseek-v4-flash-0731"],"max_tokens":8000,"reasoning_effort":"low","timeout_ms":90000,"attempt_timeout_ms":30000}},"provider_policy":{"sort":"price","allow_fallbacks":true,"require_parameters":true,"data_collection":"deny","zdr_required":false,"max_prompt_price":0.3,"max_completion_price":2.5}}'::jsonb,
     '0000000000000000000000000000000000000000000000000000000000000004','Bootstrap governed model profile','system','system','system','system',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP);

INSERT INTO ops.config_deployments (
    environment,config_kind,config_key,version_id,revision,deployed_by,deployed_at
) VALUES
    ('default','billing_plan','free','10000000-0000-4000-8000-000000000001',1,'system',CURRENT_TIMESTAMP),
    ('default','billing_plan','pro','10000000-0000-4000-8000-000000000002',1,'system',CURRENT_TIMESTAMP),
    ('default','billing_plan','team','10000000-0000-4000-8000-000000000003',1,'system',CURRENT_TIMESTAMP),
    ('default','model_profile','production-default','20000000-0000-4000-8000-000000000001',1,'system',CURRENT_TIMESTAMP);
