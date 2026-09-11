ALTER TABLE ops.performance_budgets
    ADD COLUMN forecast_alerts_enabled BOOLEAN NOT NULL DEFAULT TRUE,
    ADD COLUMN forecast_lookback_days SMALLINT NOT NULL DEFAULT 7
        CHECK (forecast_lookback_days BETWEEN 1 AND 90),
    ADD COLUMN forecast_min_samples INTEGER NOT NULL DEFAULT 20
        CHECK (forecast_min_samples BETWEEN 5 AND 10000);

UPDATE ops.performance_budgets
SET forecast_lookback_days = 30
WHERE period = 'monthly';
