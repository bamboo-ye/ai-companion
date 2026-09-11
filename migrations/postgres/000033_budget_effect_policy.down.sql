ALTER TABLE ops.performance_budget_recommendation_decisions
    DROP CONSTRAINT IF EXISTS performance_budget_recommendation_effect_policy_complete,
    DROP COLUMN IF EXISTS effect_min_samples,
    DROP COLUMN IF EXISTS effect_observation_days;

ALTER TABLE ops.performance_budgets
    DROP COLUMN IF EXISTS effect_min_samples,
    DROP COLUMN IF EXISTS effect_observation_days;
