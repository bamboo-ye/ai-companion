DROP INDEX IF EXISTS ops.performance_budget_recommendation_decisions_applied_idx;

ALTER TABLE ops.performance_budget_recommendation_decisions
    DROP CONSTRAINT IF EXISTS performance_budget_recommendation_application_complete,
    DROP COLUMN IF EXISTS applied_budget_revision,
    DROP COLUMN IF EXISTS applied_by,
    DROP COLUMN IF EXISTS applied_at,
    DROP COLUMN IF EXISTS recommendation_budget_revision;
