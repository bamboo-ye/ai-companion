ALTER TABLE ops.performance_budget_recommendation_decisions
    ADD COLUMN effect_review_status VARCHAR(24)
        CHECK (effect_review_status IN ('acknowledged', 'closed')),
    ADD COLUMN effect_disposition VARCHAR(32)
        CHECK (effect_disposition IN ('rollback_planned', 'continue_observing')),
    ADD COLUMN effect_review_reason VARCHAR(512),
    ADD COLUMN effect_reviewed_by VARCHAR(128),
    ADD COLUMN effect_reviewed_at TIMESTAMPTZ,
    ADD COLUMN effect_closed_reason VARCHAR(512),
    ADD COLUMN effect_closed_by VARCHAR(128),
    ADD COLUMN effect_closed_at TIMESTAMPTZ,
    ADD CONSTRAINT performance_budget_effect_review_complete CHECK (
        (effect_review_status IS NULL
            AND effect_disposition IS NULL
            AND effect_review_reason IS NULL
            AND effect_reviewed_by IS NULL
            AND effect_reviewed_at IS NULL
            AND effect_closed_reason IS NULL
            AND effect_closed_by IS NULL
            AND effect_closed_at IS NULL)
        OR
        (effect_review_status = 'acknowledged'
            AND effect_disposition IS NOT NULL
            AND effect_review_reason IS NOT NULL
            AND effect_reviewed_by IS NOT NULL
            AND effect_reviewed_at IS NOT NULL
            AND effect_closed_reason IS NULL
            AND effect_closed_by IS NULL
            AND effect_closed_at IS NULL)
        OR
        (effect_review_status = 'closed'
            AND effect_disposition IS NOT NULL
            AND effect_review_reason IS NOT NULL
            AND effect_reviewed_by IS NOT NULL
            AND effect_reviewed_at IS NOT NULL
            AND effect_closed_reason IS NOT NULL
            AND effect_closed_by IS NOT NULL
            AND effect_closed_at IS NOT NULL)
    );

CREATE INDEX performance_budget_effect_review_status_idx
    ON ops.performance_budget_recommendation_decisions (effect_review_status, effect_reviewed_at DESC)
    WHERE effect_review_status IS NOT NULL;
