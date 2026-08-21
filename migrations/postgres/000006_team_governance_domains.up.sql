CREATE TABLE app.workspaces (
    id UUID PRIMARY KEY,
    name VARCHAR(160) NOT NULL,
    owner_id UUID NOT NULL REFERENCES app.users (id),
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled', 'deleted')),
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX workspaces_owner_status_idx
    ON app.workspaces (owner_id, status, created_at);

CREATE TABLE app.workspace_members (
    workspace_id UUID NOT NULL REFERENCES app.workspaces (id),
    user_id UUID NOT NULL REFERENCES app.users (id),
    role TEXT NOT NULL DEFAULT 'member' CHECK (role IN ('owner', 'admin', 'member')),
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled', 'removed')),
    joined_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (workspace_id, user_id)
);

CREATE INDEX workspace_members_user_idx
    ON app.workspace_members (user_id, status, joined_at);

CREATE TABLE app.workspace_invitations (
    id UUID PRIMARY KEY,
    workspace_id UUID NOT NULL REFERENCES app.workspaces (id),
    email VARCHAR(320) NOT NULL,
    role TEXT NOT NULL DEFAULT 'member' CHECK (role IN ('admin', 'member')),
    status TEXT NOT NULL DEFAULT 'pending' CHECK (
        status IN ('pending', 'accepted', 'revoked', 'expired')
    ),
    invited_by UUID NOT NULL REFERENCES app.users (id),
    created_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    accepted_at TIMESTAMPTZ,
    UNIQUE (workspace_id, email, status)
);

CREATE INDEX workspace_invitations_workspace_idx
    ON app.workspace_invitations (workspace_id, status, created_at);

CREATE INDEX workspace_invitations_email_idx
    ON app.workspace_invitations (email, status, expires_at);

ALTER TABLE app.workspace_ledger_export_shares
    ADD CONSTRAINT workspace_ledger_export_shares_workspace_fk
    FOREIGN KEY (workspace_id) REFERENCES app.workspaces (id) NOT VALID;

ALTER TABLE app.workspace_document_shares
    ADD CONSTRAINT workspace_document_shares_workspace_fk
    FOREIGN KEY (workspace_id) REFERENCES app.workspaces (id) NOT VALID;

ALTER TABLE app.workspace_generated_file_shares
    ADD CONSTRAINT workspace_generated_file_shares_workspace_fk
    FOREIGN KEY (workspace_id) REFERENCES app.workspaces (id) NOT VALID;

CREATE TABLE app.email_deliveries (
    id UUID PRIMARY KEY,
    actor_id UUID REFERENCES app.users (id),
    resource_type VARCHAR(64) NOT NULL,
    resource_id UUID NOT NULL,
    template VARCHAR(128) NOT NULL,
    recipient_email VARCHAR(320) NOT NULL,
    subject VARCHAR(255) NOT NULL,
    body_text TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'queued' CHECK (
        status IN ('queued', 'processing', 'sent', 'failed')
    ),
    provider VARCHAR(64) NOT NULL DEFAULT '',
    provider_message_id VARCHAR(191) NOT NULL DEFAULT '',
    failure_code VARCHAR(128) NOT NULL DEFAULT '',
    last_error VARCHAR(1024) NOT NULL DEFAULT '',
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    available_at TIMESTAMPTZ NOT NULL,
    worker_id VARCHAR(128),
    lease_expires_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    sent_at TIMESTAMPTZ
);

CREATE INDEX email_deliveries_resource_idx
    ON app.email_deliveries (resource_type, resource_id, created_at);

CREATE INDEX email_deliveries_recipient_idx
    ON app.email_deliveries (recipient_email, status, created_at);

CREATE INDEX email_deliveries_claim_idx
    ON app.email_deliveries (status, available_at, lease_expires_at);

CREATE TABLE app.billing_plans (
    code VARCHAR(64) PRIMARY KEY,
    display_name VARCHAR(120) NOT NULL,
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived')),
    document_limit INTEGER NOT NULL CHECK (document_limit >= 0),
    skill_runs_monthly_limit INTEGER NOT NULL CHECK (skill_runs_monthly_limit >= 0),
    workspace_limit INTEGER NOT NULL CHECK (workspace_limit >= 0),
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);

INSERT INTO app.billing_plans (
    code,
    display_name,
    status,
    document_limit,
    skill_runs_monthly_limit,
    workspace_limit,
    created_at,
    updated_at
) VALUES
    ('free', 'Free', 'active', 10, 20, 3, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
    ('pro', 'Pro', 'active', 200, 1000, 20, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
    ('team', 'Team', 'active', 1000, 5000, 100, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
ON CONFLICT (code) DO UPDATE SET
    display_name = EXCLUDED.display_name,
    status = EXCLUDED.status,
    document_limit = EXCLUDED.document_limit,
    skill_runs_monthly_limit = EXCLUDED.skill_runs_monthly_limit,
    workspace_limit = EXCLUDED.workspace_limit,
    updated_at = EXCLUDED.updated_at;

CREATE TABLE app.billing_subscriptions (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES app.users (id),
    plan_code VARCHAR(64) NOT NULL REFERENCES app.billing_plans (code),
    status TEXT NOT NULL DEFAULT 'active' CHECK (
        status IN ('trialing', 'active', 'past_due', 'cancelled', 'expired')
    ),
    current_period_start TIMESTAMPTZ NOT NULL,
    current_period_end TIMESTAMPTZ NOT NULL,
    provider VARCHAR(64) NOT NULL DEFAULT '',
    provider_ref VARCHAR(191) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    CHECK (current_period_end > current_period_start)
);

CREATE INDEX billing_subscriptions_user_status_period_idx
    ON app.billing_subscriptions (user_id, status, current_period_end);

CREATE INDEX billing_subscriptions_provider_ref_idx
    ON app.billing_subscriptions (provider, provider_ref);

CREATE TABLE app.user_safety_policies (
    user_id UUID PRIMARY KEY REFERENCES app.users (id),
    minor_mode BOOLEAN NOT NULL DEFAULT FALSE,
    guardian_email VARCHAR(320) NOT NULL DEFAULT '',
    risky_skills_allowed BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    CHECK (NOT minor_mode OR guardian_email <> ''),
    CHECK (NOT minor_mode OR NOT risky_skills_allowed)
);

CREATE INDEX user_safety_policies_minor_mode_idx
    ON app.user_safety_policies (minor_mode, updated_at);

CREATE TABLE app.operator_accounts (
    id VARCHAR(128) PRIMARY KEY,
    display_name VARCHAR(160) NOT NULL,
    role TEXT NOT NULL DEFAULT 'viewer' CHECK (role IN ('viewer', 'support', 'admin')),
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    token_hash CHAR(64) NOT NULL UNIQUE CHECK (LENGTH(token_hash) = 64),
    totp_secret VARCHAR(128) NOT NULL DEFAULT '',
    mfa_enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    last_authenticated_at TIMESTAMPTZ
);

CREATE INDEX operator_accounts_role_status_idx
    ON app.operator_accounts (role, status);
