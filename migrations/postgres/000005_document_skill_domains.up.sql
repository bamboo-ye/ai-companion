CREATE TABLE app.files (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES app.users (id),
    original_name VARCHAR(255) NOT NULL,
    media_type VARCHAR(128) NOT NULL,
    size_bytes BIGINT NOT NULL CHECK (size_bytes >= 0),
    sha256 CHAR(64) NOT NULL CHECK (LENGTH(sha256) = 64),
    storage_key VARCHAR(512) NOT NULL,
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'deleted')),
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    deleted_at TIMESTAMPTZ
);

CREATE UNIQUE INDEX files_user_active_hash_uq
    ON app.files (user_id, sha256)
    WHERE status = 'active';

CREATE INDEX files_user_hash_status_idx
    ON app.files (user_id, sha256, status);

CREATE INDEX files_user_created_idx
    ON app.files (user_id, status, created_at DESC);

CREATE TABLE app.documents (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES app.users (id),
    file_id UUID NOT NULL UNIQUE REFERENCES app.files (id),
    ingest_status TEXT NOT NULL DEFAULT 'queued' CHECK (
        ingest_status IN ('queued', 'processing', 'ready', 'failed', 'deleted')
    ),
    parser_version VARCHAR(64),
    page_count INTEGER NOT NULL DEFAULT 0 CHECK (page_count >= 0),
    chunk_count INTEGER NOT NULL DEFAULT 0 CHECK (chunk_count >= 0),
    failure_code VARCHAR(128),
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    deleted_at TIMESTAMPTZ
);

CREATE INDEX documents_user_status_created_idx
    ON app.documents (user_id, ingest_status, created_at DESC);

CREATE TABLE app.document_ingest_jobs (
    id UUID PRIMARY KEY,
    document_id UUID NOT NULL REFERENCES app.documents (id),
    user_id UUID NOT NULL REFERENCES app.users (id),
    status TEXT NOT NULL DEFAULT 'queued' CHECK (
        status IN ('queued', 'processing', 'completed', 'failed', 'cancelled')
    ),
    idempotency_key VARCHAR(160) NOT NULL UNIQUE,
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    available_at TIMESTAMPTZ NOT NULL,
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    last_error VARCHAR(1024),
    worker_id VARCHAR(128),
    lease_expires_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX document_ingest_ready_idx
    ON app.document_ingest_jobs (status, available_at);

CREATE INDEX document_ingest_user_idx
    ON app.document_ingest_jobs (user_id, status, created_at);

CREATE INDEX document_ingest_lease_idx
    ON app.document_ingest_jobs (status, lease_expires_at, available_at);

CREATE TABLE app.document_pages (
    id UUID PRIMARY KEY,
    document_id UUID NOT NULL REFERENCES app.documents (id),
    user_id UUID NOT NULL REFERENCES app.users (id),
    page_no INTEGER NOT NULL CHECK (page_no > 0),
    text_content TEXT NOT NULL,
    quality_score NUMERIC(5,4) NOT NULL CHECK (quality_score >= 0 AND quality_score <= 1),
    parser_version VARCHAR(64) NOT NULL,
    content_hash CHAR(64) NOT NULL CHECK (LENGTH(content_hash) = 64),
    created_at TIMESTAMPTZ NOT NULL,
    UNIQUE (document_id, page_no, parser_version)
);

CREATE INDEX document_pages_user_document_idx
    ON app.document_pages (user_id, document_id, page_no);

CREATE TABLE app.document_chunks (
    id UUID PRIMARY KEY,
    point_id UUID NOT NULL UNIQUE,
    document_id UUID NOT NULL REFERENCES app.documents (id),
    user_id UUID NOT NULL REFERENCES app.users (id),
    ordinal_no INTEGER NOT NULL CHECK (ordinal_no >= 0),
    page_start INTEGER NOT NULL CHECK (page_start > 0),
    page_end INTEGER NOT NULL CHECK (page_end >= page_start),
    section_path VARCHAR(512) NOT NULL DEFAULT '',
    content TEXT NOT NULL,
    token_count INTEGER NOT NULL CHECK (token_count >= 0),
    content_hash CHAR(64) NOT NULL CHECK (LENGTH(content_hash) = 64),
    parser_version VARCHAR(64) NOT NULL,
    embedding_version VARCHAR(64) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    UNIQUE (document_id, ordinal_no, parser_version)
);

CREATE INDEX document_chunks_user_document_idx
    ON app.document_chunks (user_id, document_id, ordinal_no);

CREATE TABLE app.document_cleanup_jobs (
    id UUID PRIMARY KEY,
    document_id UUID NOT NULL UNIQUE REFERENCES app.documents (id),
    user_id UUID NOT NULL REFERENCES app.users (id),
    storage_key VARCHAR(512) NOT NULL,
    status TEXT NOT NULL DEFAULT 'queued' CHECK (
        status IN ('queued', 'processing', 'completed', 'failed')
    ),
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    available_at TIMESTAMPTZ NOT NULL,
    worker_id VARCHAR(128),
    lease_expires_at TIMESTAMPTZ,
    last_error VARCHAR(1024),
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    completed_at TIMESTAMPTZ
);

CREATE INDEX document_cleanup_claim_idx
    ON app.document_cleanup_jobs (status, available_at, lease_expires_at);

CREATE TABLE app.workspace_document_shares (
    workspace_id UUID NOT NULL,
    document_id UUID NOT NULL REFERENCES app.documents (id),
    shared_by UUID NOT NULL REFERENCES app.users (id),
    created_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (workspace_id, document_id)
);

CREATE INDEX workspace_document_shares_document_idx
    ON app.workspace_document_shares (document_id, created_at);

CREATE INDEX workspace_document_shares_shared_by_idx
    ON app.workspace_document_shares (shared_by, created_at);

CREATE TABLE app.skill_runs (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES app.users (id),
    skill_name VARCHAR(128) NOT NULL,
    skill_version VARCHAR(32) NOT NULL,
    execution_mode TEXT NOT NULL DEFAULT 'inline' CHECK (execution_mode IN ('inline', 'worker')),
    status TEXT NOT NULL CHECK (
        status IN ('waiting_confirmation', 'queued', 'running', 'succeeded', 'failed', 'cancelled')
    ),
    current_state VARCHAR(64) NOT NULL,
    risk_level TEXT NOT NULL CHECK (risk_level IN ('none', 'low', 'medium', 'high')),
    requires_confirmation BOOLEAN NOT NULL,
    input_json JSONB NOT NULL,
    output_json JSONB,
    create_key VARCHAR(191) NOT NULL,
    confirmation_key VARCHAR(191),
    last_action VARCHAR(32) NOT NULL DEFAULT '',
    last_action_key VARCHAR(191),
    attempt SMALLINT NOT NULL CHECK (attempt > 0),
    max_steps SMALLINT NOT NULL CHECK (max_steps > 0),
    timeout_ms INTEGER NOT NULL CHECK (timeout_ms > 0),
    max_input_bytes INTEGER NOT NULL CHECK (max_input_bytes > 0),
    max_cost_micros BIGINT NOT NULL DEFAULT 0 CHECK (max_cost_micros >= 0),
    error_code VARCHAR(128) NOT NULL DEFAULT '',
    error_message VARCHAR(1024) NOT NULL DEFAULT '',
    revision INTEGER NOT NULL CHECK (revision > 0),
    available_at TIMESTAMPTZ NOT NULL,
    worker_id VARCHAR(128),
    lease_expires_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    completed_at TIMESTAMPTZ,
    UNIQUE (user_id, create_key)
);

CREATE INDEX skill_runs_user_created_idx
    ON app.skill_runs (user_id, created_at DESC);

CREATE INDEX skill_runs_status_updated_idx
    ON app.skill_runs (status, updated_at);

CREATE INDEX skill_runs_worker_claim_idx
    ON app.skill_runs (execution_mode, status, available_at, lease_expires_at);

CREATE TABLE app.skill_run_action_keys (
    id BIGSERIAL PRIMARY KEY,
    run_id UUID NOT NULL REFERENCES app.skill_runs (id),
    user_id UUID NOT NULL REFERENCES app.users (id),
    action VARCHAR(32) NOT NULL,
    idempotency_key VARCHAR(191) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    UNIQUE (user_id, idempotency_key)
);

CREATE INDEX skill_run_action_keys_run_idx
    ON app.skill_run_action_keys (run_id, created_at);

CREATE TABLE app.skill_run_steps (
    id UUID PRIMARY KEY,
    run_id UUID NOT NULL REFERENCES app.skill_runs (id),
    sequence_no SMALLINT NOT NULL CHECK (sequence_no > 0),
    state VARCHAR(64) NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('succeeded', 'failed', 'cancelled')),
    tool_name VARCHAR(128) NOT NULL DEFAULT '',
    input_json JSONB,
    output_json JSONB,
    error_code VARCHAR(128) NOT NULL DEFAULT '',
    error_message VARCHAR(1024) NOT NULL DEFAULT '',
    started_at TIMESTAMPTZ NOT NULL,
    completed_at TIMESTAMPTZ,
    UNIQUE (run_id, sequence_no)
);

CREATE TABLE app.tool_executions (
    id UUID PRIMARY KEY,
    run_id UUID NOT NULL REFERENCES app.skill_runs (id),
    user_id UUID NOT NULL REFERENCES app.users (id),
    tool_name VARCHAR(128) NOT NULL,
    risk_level TEXT NOT NULL CHECK (risk_level IN ('none', 'low', 'medium', 'high')),
    status TEXT NOT NULL CHECK (status IN ('succeeded', 'failed', 'cancelled')),
    idempotency_key VARCHAR(191),
    input_sha256 CHAR(64) NOT NULL CHECK (LENGTH(input_sha256) = 64),
    output_json JSONB,
    error_code VARCHAR(128) NOT NULL DEFAULT '',
    error_message VARCHAR(1024) NOT NULL DEFAULT '',
    started_at TIMESTAMPTZ NOT NULL,
    completed_at TIMESTAMPTZ
);

CREATE INDEX tool_executions_user_started_idx
    ON app.tool_executions (user_id, started_at);

CREATE INDEX tool_executions_run_idx
    ON app.tool_executions (run_id, started_at);

CREATE TABLE app.generated_files (
    id UUID PRIMARY KEY,
    run_id UUID NOT NULL REFERENCES app.skill_runs (id),
    user_id UUID NOT NULL REFERENCES app.users (id),
    display_name VARCHAR(255) NOT NULL,
    media_type VARCHAR(191) NOT NULL,
    size_bytes BIGINT NOT NULL CHECK (size_bytes >= 0),
    sha256 CHAR(64) NOT NULL CHECK (LENGTH(sha256) = 64),
    storage_key VARCHAR(1024) NOT NULL UNIQUE,
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'deleted')),
    created_at TIMESTAMPTZ NOT NULL,
    deleted_at TIMESTAMPTZ
);

CREATE INDEX generated_files_user_created_idx
    ON app.generated_files (user_id, created_at DESC);

CREATE TABLE app.workspace_generated_file_shares (
    workspace_id UUID NOT NULL,
    file_id UUID NOT NULL REFERENCES app.generated_files (id),
    shared_by UUID NOT NULL REFERENCES app.users (id),
    created_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (workspace_id, file_id)
);

CREATE INDEX workspace_generated_file_shares_file_idx
    ON app.workspace_generated_file_shares (file_id, created_at);

CREATE INDEX workspace_generated_file_shares_shared_by_idx
    ON app.workspace_generated_file_shares (shared_by, created_at);

CREATE TABLE app.user_skill_settings (
    user_id UUID NOT NULL REFERENCES app.users (id),
    skill_name VARCHAR(128) NOT NULL,
    enabled BOOLEAN NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (user_id, skill_name)
);

CREATE INDEX user_skill_settings_updated_idx
    ON app.user_skill_settings (updated_at);
