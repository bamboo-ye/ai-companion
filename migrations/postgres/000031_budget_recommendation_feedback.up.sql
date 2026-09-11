CREATE TABLE ops.performance_budget_recommendation_decisions (
    id UUID PRIMARY KEY,
    budget_id UUID NOT NULL REFERENCES ops.performance_budgets(id) ON DELETE CASCADE,
    recommendation_key CHAR(64) NOT NULL CHECK (recommendation_key ~ '^[0-9a-f]{64}$'),
    action VARCHAR(32) NOT NULL CHECK (action IN ('increase_sample_gate', 'decrease_sample_gate')),
    decision VARCHAR(16) NOT NULL CHECK (decision IN ('accepted', 'rejected')),
    confidence VARCHAR(16) NOT NULL CHECK (confidence IN ('low', 'medium', 'high')),
    outcome_count INTEGER NOT NULL CHECK (outcome_count >= 5),
    hit_rate DOUBLE PRECISION NOT NULL CHECK (hit_rate >= 0 AND hit_rate <= 1),
    average_lead_time_minutes DOUBLE PRECISION NOT NULL CHECK (average_lead_time_minutes >= 0),
    current_lookback_days SMALLINT NOT NULL CHECK (current_lookback_days BETWEEN 1 AND 90),
    current_min_samples INTEGER NOT NULL CHECK (current_min_samples BETWEEN 5 AND 10000),
    proposed_lookback_days SMALLINT NOT NULL CHECK (proposed_lookback_days BETWEEN 1 AND 90),
    proposed_min_samples INTEGER NOT NULL CHECK (proposed_min_samples BETWEEN 5 AND 10000),
    recommendation_reason TEXT NOT NULL,
    operator_reason VARCHAR(512) NOT NULL CHECK (char_length(operator_reason) BETWEEN 2 AND 512),
    decided_by VARCHAR(128) NOT NULL,
    decided_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (budget_id, recommendation_key)
);

CREATE INDEX performance_budget_recommendation_decisions_budget_idx
    ON ops.performance_budget_recommendation_decisions (budget_id, decided_at DESC);
