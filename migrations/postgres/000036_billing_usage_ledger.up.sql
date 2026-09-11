CREATE TABLE app.billing_usage_adjustments (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES app.users (id),
    resource TEXT NOT NULL CHECK (resource IN (
        'documents', 'skill_runs', 'workspaces', 'agent_runs', 'model_cost_micros'
    )),
    delta BIGINT NOT NULL CHECK (delta <> 0),
    reason VARCHAR(512) NOT NULL CHECK (BTRIM(reason) <> ''),
    actor VARCHAR(128) NOT NULL CHECK (BTRIM(actor) <> ''),
    period_start TIMESTAMPTZ,
    period_end TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK ((period_start IS NULL) = (period_end IS NULL)),
    CHECK (period_end IS NULL OR period_end > period_start)
);

CREATE INDEX billing_usage_adjustments_user_resource_period_idx
    ON app.billing_usage_adjustments (user_id, resource, period_start, period_end, created_at DESC);

COMMENT ON TABLE app.billing_usage_adjustments IS
    'Append-only operator adjustments applied to the unified billing usage ledger.';
