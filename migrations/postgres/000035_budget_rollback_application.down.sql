DROP INDEX IF EXISTS ops.performance_budget_rollback_application_idx;

ALTER TABLE ops.performance_budget_recommendation_decisions
    DROP CONSTRAINT IF EXISTS performance_budget_rollback_application_semantics,
    DROP CONSTRAINT IF EXISTS performance_budget_rollback_application_complete,
    DROP COLUMN IF EXISTS rollback_budget_revision,
    DROP COLUMN IF EXISTS rollback_applied_by,
    DROP COLUMN IF EXISTS rollback_applied_at;
