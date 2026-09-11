ALTER TABLE ops.performance_budget_recommendation_decisions
    ADD COLUMN rollback_applied_at TIMESTAMPTZ,
    ADD COLUMN rollback_applied_by VARCHAR(128),
    ADD COLUMN rollback_budget_revision INTEGER CHECK (rollback_budget_revision > 0),
    ADD CONSTRAINT performance_budget_rollback_application_complete CHECK (
        (rollback_applied_at IS NULL
            AND rollback_applied_by IS NULL
            AND rollback_budget_revision IS NULL)
        OR
        (rollback_applied_at IS NOT NULL
            AND rollback_applied_by IS NOT NULL
            AND rollback_budget_revision IS NOT NULL)
    ),
    ADD CONSTRAINT performance_budget_rollback_application_semantics CHECK (
        rollback_applied_at IS NULL
        OR
        (effect_disposition = 'rollback_planned'
            AND rollback_budget_revision > applied_budget_revision)
    );

CREATE INDEX performance_budget_rollback_application_idx
    ON ops.performance_budget_recommendation_decisions (budget_id, applied_budget_revision, effect_reviewed_at DESC)
    WHERE effect_review_status = 'acknowledged'
        AND effect_disposition = 'rollback_planned'
        AND rollback_applied_at IS NULL;
