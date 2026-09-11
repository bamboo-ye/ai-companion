CREATE TABLE ops.performance_budgets (
    id UUID PRIMARY KEY,
    name VARCHAR(128) NOT NULL,
    module_key VARCHAR(16) CHECK (module_key IN ('companion', 'life', 'work')),
    period VARCHAR(16) NOT NULL CHECK (period IN ('daily', 'monthly')),
    cost_limit_micros BIGINT NOT NULL CHECK (cost_limit_micros > 0),
    warning_ratio DOUBLE PRECISION NOT NULL DEFAULT 0.8 CHECK (warning_ratio > 0 AND warning_ratio < 1),
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
    created_by VARCHAR(128) NOT NULL,
    updated_by VARCHAR(128) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX performance_budgets_scope_period_idx
    ON ops.performance_budgets (COALESCE(module_key, ''), period);
CREATE INDEX performance_budgets_enabled_idx
    ON ops.performance_budgets (enabled, period, module_key);
