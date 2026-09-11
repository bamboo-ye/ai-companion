ALTER TABLE ops.performance_budgets
    DROP COLUMN forecast_min_samples,
    DROP COLUMN forecast_lookback_days,
    DROP COLUMN forecast_alerts_enabled;
