DROP INDEX IF EXISTS ops.performance_budget_effect_review_status_idx;

ALTER TABLE ops.performance_budget_recommendation_decisions
    DROP CONSTRAINT IF EXISTS performance_budget_effect_review_complete,
    DROP COLUMN IF EXISTS effect_closed_at,
    DROP COLUMN IF EXISTS effect_closed_by,
    DROP COLUMN IF EXISTS effect_closed_reason,
    DROP COLUMN IF EXISTS effect_reviewed_at,
    DROP COLUMN IF EXISTS effect_reviewed_by,
    DROP COLUMN IF EXISTS effect_review_reason,
    DROP COLUMN IF EXISTS effect_disposition,
    DROP COLUMN IF EXISTS effect_review_status;
