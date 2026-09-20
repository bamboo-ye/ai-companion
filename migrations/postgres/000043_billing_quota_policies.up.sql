CREATE TABLE app.billing_quota_policies (
    scope_key VARCHAR(70) PRIMARY KEY,
    limits_json TEXT NOT NULL,
    revision BIGINT NOT NULL CHECK (revision > 0),
    actor VARCHAR(128) NOT NULL,
    reason VARCHAR(512) NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);
