ALTER TABLE ops.incidents
    ALTER COLUMN rule_id DROP NOT NULL,
    ADD COLUMN source_type VARCHAR(32) NOT NULL DEFAULT 'log_rule'
        CHECK (source_type IN ('log_rule', 'performance_budget')),
    ADD COLUMN source_id UUID;

UPDATE ops.incidents
SET source_id = rule_id
WHERE source_type = 'log_rule' AND source_id IS NULL;

CREATE UNIQUE INDEX incidents_active_budget_idx
    ON ops.incidents (source_id)
    WHERE source_type = 'performance_budget' AND status IN ('open', 'acknowledged');
CREATE INDEX incidents_source_time_idx
    ON ops.incidents (source_type, opened_at DESC);

ALTER TABLE ops.incident_notifications
    DROP CONSTRAINT incident_notifications_transition_check,
    ADD CONSTRAINT incident_notifications_transition_check
        CHECK (transition IN ('opened', 'escalated', 'resolved'));
