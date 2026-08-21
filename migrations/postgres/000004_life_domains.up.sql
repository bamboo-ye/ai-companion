CREATE TABLE app.ledger_candidates (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES app.users (id),
    source_message_id UUID REFERENCES app.messages (id),
    raw_text VARCHAR(2000) NOT NULL,
    direction TEXT CHECK (direction IN ('income', 'expense')),
    currency CHAR(3),
    amount_minor BIGINT CHECK (amount_minor IS NULL OR amount_minor > 0),
    category VARCHAR(64) NOT NULL DEFAULT 'other',
    merchant VARCHAR(255) NOT NULL DEFAULT '',
    occurred_at TIMESTAMPTZ,
    timezone VARCHAR(64) NOT NULL,
    time_precision TEXT CHECK (time_precision IN ('minute', 'part_of_day', 'date')),
    confidence NUMERIC(5,4) NOT NULL CHECK (confidence >= 0 AND confidence <= 1),
    needs_clarification JSONB NOT NULL DEFAULT '[]'::JSONB,
    status TEXT NOT NULL CHECK (
        status IN ('pending', 'needs_clarification', 'confirmed', 'expired', 'cancelled')
    ),
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX ledger_candidates_user_status_idx
    ON app.ledger_candidates (user_id, status, created_at);

CREATE TABLE app.ledger_entries (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES app.users (id),
    candidate_id UUID UNIQUE REFERENCES app.ledger_candidates (id),
    idempotency_key VARCHAR(191) NOT NULL,
    direction TEXT NOT NULL CHECK (direction IN ('income', 'expense')),
    currency CHAR(3) NOT NULL,
    amount_minor BIGINT NOT NULL CHECK (amount_minor > 0),
    category VARCHAR(64) NOT NULL,
    merchant VARCHAR(255) NOT NULL DEFAULT '',
    occurred_at TIMESTAMPTZ NOT NULL,
    timezone VARCHAR(64) NOT NULL,
    note VARCHAR(1000) NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'deleted')),
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    deleted_at TIMESTAMPTZ,
    UNIQUE (user_id, idempotency_key)
);

CREATE INDEX ledger_entries_user_occurred_idx
    ON app.ledger_entries (user_id, status, occurred_at DESC);

CREATE INDEX ledger_entries_user_category_idx
    ON app.ledger_entries (user_id, status, category, occurred_at DESC);

CREATE TABLE app.ledger_exports (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES app.users (id),
    month CHAR(7) NOT NULL,
    currency CHAR(3) NOT NULL,
    timezone VARCHAR(64) NOT NULL,
    request_key VARCHAR(191),
    status TEXT NOT NULL CHECK (
        status IN ('queued', 'processing', 'completed', 'failed', 'expired')
    ),
    available_at TIMESTAMPTZ NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    worker_id VARCHAR(128),
    lease_expires_at TIMESTAMPTZ,
    storage_key VARCHAR(1024),
    file_name VARCHAR(255),
    media_type VARCHAR(191),
    sha256 CHAR(64),
    size_bytes BIGINT CHECK (size_bytes IS NULL OR size_bytes >= 0),
    failure_code VARCHAR(128),
    last_error VARCHAR(1024),
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    completed_at TIMESTAMPTZ,
    UNIQUE (user_id, request_key)
);

CREATE INDEX ledger_exports_user_created_idx
    ON app.ledger_exports (user_id, created_at DESC);

CREATE INDEX ledger_exports_worker_claim_idx
    ON app.ledger_exports (status, available_at, lease_expires_at);

CREATE TABLE app.plans (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES app.users (id),
    title VARCHAR(160) NOT NULL,
    local_date DATE NOT NULL,
    timezone VARCHAR(64) NOT NULL,
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'completed', 'cancelled')),
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX plans_user_date_idx
    ON app.plans (user_id, status, local_date);

CREATE TABLE app.plan_items (
    id UUID PRIMARY KEY,
    plan_id UUID NOT NULL REFERENCES app.plans (id),
    title VARCHAR(255) NOT NULL,
    priority TEXT NOT NULL CHECK (priority IN ('low', 'medium', 'high')),
    estimated_minutes SMALLINT NOT NULL DEFAULT 0 CHECK (
        estimated_minutes >= 0 AND estimated_minutes <= 1440
    ),
    starts_at TIMESTAMPTZ,
    ends_at TIMESTAMPTZ,
    location VARCHAR(255) NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'pending' CHECK (
        status IN ('pending', 'in_progress', 'completed', 'skipped', 'cancelled')
    ),
    source VARCHAR(64) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    CHECK (ends_at IS NULL OR starts_at IS NULL OR ends_at > starts_at)
);

CREATE INDEX plan_items_plan_status_idx
    ON app.plan_items (plan_id, status, starts_at);

CREATE TABLE app.reminders (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES app.users (id),
    plan_item_id UUID REFERENCES app.plan_items (id),
    source_message_id UUID REFERENCES app.messages (id),
    raw_text VARCHAR(2000) NOT NULL,
    title VARCHAR(255) NOT NULL,
    due_at TIMESTAMPTZ,
    local_due VARCHAR(32) NOT NULL DEFAULT '',
    timezone VARCHAR(64) NOT NULL,
    time_precision TEXT CHECK (time_precision IN ('minute', 'part_of_day', 'date')),
    recurrence TEXT NOT NULL DEFAULT 'none' CHECK (recurrence IN ('none', 'daily', 'weekly')),
    needs_clarification JSONB NOT NULL DEFAULT '[]'::JSONB,
    status TEXT NOT NULL CHECK (
        status IN ('needs_clarification', 'pending_confirmation', 'active', 'completed', 'cancelled')
    ),
    confirmation_key VARCHAR(191),
    system_sync_status TEXT NOT NULL DEFAULT 'not_requested' CHECK (
        system_sync_status IN (
            'not_requested', 'pending', 'synced', 'permission_denied',
            'failed', 'conflict', 'disconnected'
        )
    ),
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    UNIQUE (user_id, confirmation_key)
);

CREATE INDEX reminders_user_due_idx
    ON app.reminders (user_id, status, due_at);

CREATE TABLE app.external_reminder_links (
    reminder_id UUID PRIMARY KEY REFERENCES app.reminders (id),
    provider TEXT NOT NULL CHECK (
        provider IN ('ios_eventkit', 'android_calendar', 'android_alarm', 'google_tasks')
    ),
    external_id VARCHAR(512) NOT NULL,
    external_revision VARCHAR(255) NOT NULL DEFAULT '',
    sync_status TEXT NOT NULL CHECK (
        sync_status IN ('synced', 'permission_denied', 'failed', 'conflict', 'disconnected')
    ),
    last_error_code VARCHAR(128) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    UNIQUE (provider, external_id)
);

CREATE TABLE app.reminder_events (
    id BIGSERIAL PRIMARY KEY,
    reminder_id UUID NOT NULL REFERENCES app.reminders (id),
    user_id UUID NOT NULL REFERENCES app.users (id),
    event_type TEXT NOT NULL CHECK (
        event_type IN ('confirmed', 'completed', 'snoozed', 'skipped', 'rescheduled', 'sync_updated')
    ),
    event_data JSONB NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX reminder_events_user_idx
    ON app.reminder_events (user_id, occurred_at);

CREATE TABLE app.notification_deliveries (
    id UUID PRIMARY KEY,
    reminder_id UUID NOT NULL REFERENCES app.reminders (id),
    user_id UUID NOT NULL REFERENCES app.users (id),
    channel TEXT NOT NULL CHECK (channel IN ('in_app', 'apns', 'fcm', 'local')),
    scheduled_at TIMESTAMPTZ NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('queued', 'delivered', 'failed', 'cancelled')),
    enqueued_at TIMESTAMPTZ,
    provider_message_id VARCHAR(512),
    failure_code VARCHAR(128),
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    UNIQUE (reminder_id, channel, scheduled_at)
);

CREATE INDEX notification_deliveries_due_idx
    ON app.notification_deliveries (status, scheduled_at);

CREATE INDEX notification_dispatch_idx
    ON app.notification_deliveries (status, enqueued_at, scheduled_at);

-- Workspace ownership and membership remain in the later team-domain slice.
-- Keeping the join table here preserves the complete ledger.Store contract;
-- its workspace foreign key is added when app.workspaces is migrated.
CREATE TABLE app.workspace_ledger_export_shares (
    workspace_id UUID NOT NULL,
    export_id UUID NOT NULL REFERENCES app.ledger_exports (id),
    shared_by UUID NOT NULL REFERENCES app.users (id),
    created_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (workspace_id, export_id)
);

CREATE INDEX workspace_ledger_export_shares_export_idx
    ON app.workspace_ledger_export_shares (export_id, created_at);

CREATE INDEX workspace_ledger_export_shares_shared_by_idx
    ON app.workspace_ledger_export_shares (shared_by, created_at);
