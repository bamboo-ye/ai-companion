ALTER TABLE app.operator_accounts ADD COLUMN session_version BIGINT NOT NULL DEFAULT 1;
CREATE TABLE ops.operator_passkey_records (
    record_key VARCHAR(100) PRIMARY KEY,
    owner_id VARCHAR(128) NOT NULL,
    kind VARCHAR(24) NOT NULL,
    data TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    revision BIGINT NOT NULL DEFAULT 1
);
CREATE INDEX operator_passkey_owner_idx ON ops.operator_passkey_records(kind, owner_id);
CREATE INDEX operator_passkey_expiry_idx ON ops.operator_passkey_records(expires_at);
