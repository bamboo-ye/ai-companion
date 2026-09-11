ALTER TABLE ops.performance_budget_recommendation_decisions
    ADD COLUMN recommendation_budget_revision INTEGER
        CHECK (recommendation_budget_revision > 0),
    ADD COLUMN applied_at TIMESTAMPTZ,
    ADD COLUMN applied_by VARCHAR(128),
    ADD COLUMN applied_budget_revision INTEGER
        CHECK (applied_budget_revision > 0),
    ADD CONSTRAINT performance_budget_recommendation_application_complete CHECK (
        (applied_at IS NULL AND applied_by IS NULL AND applied_budget_revision IS NULL)
        OR
        (applied_at IS NOT NULL AND applied_by IS NOT NULL AND applied_budget_revision IS NOT NULL)
    );

CREATE INDEX performance_budget_recommendation_decisions_applied_idx
    ON ops.performance_budget_recommendation_decisions (applied_at DESC)
    WHERE applied_at IS NOT NULL;
