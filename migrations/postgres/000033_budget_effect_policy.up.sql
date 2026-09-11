ALTER TABLE ops.performance_budgets
    ADD COLUMN effect_observation_days SMALLINT NOT NULL DEFAULT 30
        CHECK (effect_observation_days BETWEEN 7 AND 180),
    ADD COLUMN effect_min_samples SMALLINT NOT NULL DEFAULT 5
        CHECK (effect_min_samples BETWEEN 5 AND 100);

ALTER TABLE ops.performance_budget_recommendation_decisions
    ADD COLUMN effect_observation_days SMALLINT
        CHECK (effect_observation_days BETWEEN 7 AND 180),
    ADD COLUMN effect_min_samples SMALLINT
        CHECK (effect_min_samples BETWEEN 5 AND 100);

UPDATE ops.performance_budget_recommendation_decisions
SET effect_observation_days = 30,
    effect_min_samples = 5
WHERE applied_at IS NOT NULL;

ALTER TABLE ops.performance_budget_recommendation_decisions
    ADD CONSTRAINT performance_budget_recommendation_effect_policy_complete CHECK (
        (applied_at IS NULL AND effect_observation_days IS NULL AND effect_min_samples IS NULL)
        OR
        (applied_at IS NOT NULL AND effect_observation_days IS NOT NULL AND effect_min_samples IS NOT NULL)
    );
