CREATE TABLE app.users (
    id UUID PRIMARY KEY,
    email VARCHAR(320) NOT NULL,
    password_hash VARCHAR(255) NOT NULL,
    display_name VARCHAR(80) NOT NULL,
    timezone VARCHAR(64) NOT NULL,
    locale VARCHAR(16) NOT NULL DEFAULT 'zh-CN',
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled', 'deleted')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX users_email_ci_uq ON app.users (LOWER(email));

CREATE TABLE app.user_devices (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES app.users (id),
    device_key VARCHAR(191) NOT NULL,
    name VARCHAR(120) NOT NULL,
    platform TEXT NOT NULL CHECK (platform IN ('web', 'ios', 'android', 'service')),
    timezone VARCHAR(64) NOT NULL,
    last_seen_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (user_id, device_key)
);

CREATE TABLE app.refresh_sessions (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES app.users (id),
    device_id UUID NOT NULL REFERENCES app.user_devices (id),
    token_hash CHAR(64) NOT NULL UNIQUE CHECK (LENGTH(token_hash) = 64),
    expires_at TIMESTAMPTZ NOT NULL,
    last_used_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ,
    rotated_to_id UUID REFERENCES app.refresh_sessions (id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX refresh_sessions_user_active_idx
    ON app.refresh_sessions (user_id, revoked_at, expires_at);

CREATE TABLE app.characters (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES app.users (id),
    module_key TEXT NOT NULL DEFAULT 'companion' CHECK (module_key IN ('companion', 'life', 'work')),
    name VARCHAR(80) NOT NULL,
    avatar_url VARCHAR(2048),
    relationship_label VARCHAR(120) NOT NULL,
    personality TEXT NOT NULL,
    speech_style TEXT NOT NULL,
    hobbies JSONB NOT NULL,
    boundaries JSONB NOT NULL,
    initiative TEXT NOT NULL DEFAULT 'balanced' CHECK (initiative IN ('low', 'balanced', 'high')),
    reply_length TEXT NOT NULL DEFAULT 'short' CHECK (reply_length IN ('short', 'medium', 'long')),
    sticker_style VARCHAR(255) NOT NULL DEFAULT '',
    raw_prompt TEXT NOT NULL,
    persona_version INTEGER NOT NULL DEFAULT 1 CHECK (persona_version > 0),
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled', 'deleted')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX characters_user_status_idx
    ON app.characters (user_id, status, created_at);

CREATE INDEX characters_user_module_status_idx
    ON app.characters (user_id, module_key, status, created_at);

CREATE TABLE app.character_persona_versions (
    character_id UUID NOT NULL REFERENCES app.characters (id),
    version INTEGER NOT NULL CHECK (version > 0),
    compiler_version VARCHAR(64) NOT NULL,
    compiled_persona JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (character_id, version)
);

CREATE TABLE app.conversations (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES app.users (id),
    character_id UUID NOT NULL REFERENCES app.characters (id),
    title VARCHAR(255) NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'deleted')),
    next_sequence BIGINT NOT NULL DEFAULT 1 CHECK (next_sequence > 0),
    last_message_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX conversations_user_updated_idx
    ON app.conversations (user_id, updated_at DESC);

CREATE INDEX conversations_user_status_updated_idx
    ON app.conversations (user_id, status, updated_at DESC);

CREATE TABLE app.messages (
    id UUID PRIMARY KEY,
    conversation_id UUID NOT NULL REFERENCES app.conversations (id),
    user_id UUID NOT NULL REFERENCES app.users (id),
    role TEXT NOT NULL CHECK (role IN ('user', 'assistant', 'system', 'tool')),
    sequence_no BIGINT NOT NULL CHECK (sequence_no > 0),
    bubble_no SMALLINT NOT NULL DEFAULT 1 CHECK (bubble_no > 0),
    content TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('accepted', 'streaming', 'completed', 'cancelled', 'failed')),
    reply_to_id UUID REFERENCES app.messages (id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    completed_at TIMESTAMPTZ,
    UNIQUE (conversation_id, sequence_no, bubble_no)
);

CREATE INDEX messages_conversation_cursor_idx
    ON app.messages (conversation_id, sequence_no, bubble_no);

CREATE TABLE app.generation_jobs (
    id UUID PRIMARY KEY,
    conversation_id UUID NOT NULL REFERENCES app.conversations (id),
    user_message_id UUID NOT NULL REFERENCES app.messages (id),
    status TEXT NOT NULL CHECK (
        status IN ('accepted', 'running', 'completed', 'cancel_requested', 'cancelled', 'failed', 'timed_out')
    ),
    attempt SMALLINT NOT NULL DEFAULT 1 CHECK (attempt > 0),
    model_provider VARCHAR(64),
    model_name VARCHAR(128),
    deadline_at TIMESTAMPTZ NOT NULL,
    available_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    worker_id VARCHAR(128),
    lease_expires_at TIMESTAMPTZ,
    error_code VARCHAR(64),
    error_message VARCHAR(1024),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    UNIQUE (user_message_id, attempt)
);

CREATE INDEX generation_jobs_conversation_status_idx
    ON app.generation_jobs (conversation_id, status, created_at);

CREATE INDEX generation_jobs_worker_claim_idx
    ON app.generation_jobs (status, available_at, lease_expires_at);

CREATE TABLE app.model_usage_records (
    id BIGSERIAL PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES app.users (id),
    generation_job_id UUID NOT NULL REFERENCES app.generation_jobs (id),
    provider VARCHAR(64) NOT NULL,
    model VARCHAR(128) NOT NULL,
    input_tokens INTEGER NOT NULL DEFAULT 0 CHECK (input_tokens >= 0),
    output_tokens INTEGER NOT NULL DEFAULT 0 CHECK (output_tokens >= 0),
    estimated_cost_micros BIGINT NOT NULL DEFAULT 0 CHECK (estimated_cost_micros >= 0),
    latency_ms INTEGER NOT NULL DEFAULT 0 CHECK (latency_ms >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX model_usage_user_created_idx
    ON app.model_usage_records (user_id, created_at);

CREATE TABLE app.generation_job_events (
    id BIGSERIAL PRIMARY KEY,
    generation_job_id UUID NOT NULL REFERENCES app.generation_jobs (id),
    event_type VARCHAR(64) NOT NULL,
    event_data JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX generation_job_events_job_cursor_idx
    ON app.generation_job_events (generation_job_id, id);

CREATE TABLE app.conversation_summaries (
    id UUID PRIMARY KEY,
    conversation_id UUID NOT NULL REFERENCES app.conversations (id),
    user_id UUID NOT NULL REFERENCES app.users (id),
    version INTEGER NOT NULL CHECK (version > 0),
    start_sequence BIGINT NOT NULL CHECK (start_sequence > 0),
    end_sequence BIGINT NOT NULL CHECK (end_sequence > 0),
    range_started_at TIMESTAMPTZ NOT NULL,
    range_ended_at TIMESTAMPTZ NOT NULL,
    content TEXT NOT NULL,
    token_count INTEGER NOT NULL CHECK (token_count >= 0),
    summarizer_version VARCHAR(64) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (conversation_id, version),
    CHECK (start_sequence <= end_sequence)
);

CREATE INDEX conversation_summaries_latest_idx
    ON app.conversation_summaries (conversation_id, end_sequence DESC);

CREATE INDEX conversation_summaries_user_created_idx
    ON app.conversation_summaries (user_id, created_at);

ALTER TABLE agent.runs
    ADD CONSTRAINT agent_runs_user_fk
        FOREIGN KEY (user_id) REFERENCES app.users (id),
    ADD CONSTRAINT agent_runs_conversation_fk
        FOREIGN KEY (conversation_id) REFERENCES app.conversations (id),
    ADD CONSTRAINT agent_runs_character_fk
        FOREIGN KEY (character_id) REFERENCES app.characters (id);
