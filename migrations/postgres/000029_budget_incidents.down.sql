DELETE FROM eventing.outbox_events
WHERE aggregate_id IN (
    SELECT delivery_id::text
    FROM ops.incident_notifications
    WHERE incident_id IN (SELECT id FROM ops.incidents WHERE source_type = 'performance_budget')
);

DELETE FROM ops.incident_notifications
WHERE incident_id IN (SELECT id FROM ops.incidents WHERE source_type = 'performance_budget');

DELETE FROM app.email_deliveries
WHERE resource_type = 'incident'
  AND resource_id IN (SELECT id FROM ops.incidents WHERE source_type = 'performance_budget');

DELETE FROM eventing.audit_logs
WHERE resource_type = 'incident'
  AND resource_id IN (SELECT id FROM ops.incidents WHERE source_type = 'performance_budget');

DELETE FROM ops.incidents WHERE source_type = 'performance_budget';

ALTER TABLE ops.incident_notifications
    DROP CONSTRAINT incident_notifications_transition_check,
    ADD CONSTRAINT incident_notifications_transition_check
        CHECK (transition IN ('opened', 'resolved'));

DROP INDEX IF EXISTS ops.incidents_source_time_idx;
DROP INDEX IF EXISTS ops.incidents_active_budget_idx;

ALTER TABLE ops.incidents
    DROP COLUMN source_id,
    DROP COLUMN source_type,
    ALTER COLUMN rule_id SET NOT NULL;
