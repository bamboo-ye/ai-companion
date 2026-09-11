ALTER TABLE ops.alert_subscriptions DROP CONSTRAINT alert_subscriptions_channel_check;
ALTER TABLE ops.alert_subscriptions ADD CONSTRAINT alert_subscriptions_channel_check
    CHECK (channel IN ('email','console'));

ALTER TABLE ops.incident_notifications DROP CONSTRAINT incident_notifications_channel_check;
ALTER TABLE ops.incident_notifications ADD CONSTRAINT incident_notifications_channel_check
    CHECK (channel IN ('email','console'));
ALTER TABLE ops.incident_notifications DROP CONSTRAINT incident_notifications_delivery_id_fkey;
ALTER TABLE ops.incident_notifications ALTER COLUMN delivery_id DROP NOT NULL;
ALTER TABLE ops.incident_notifications
    ADD COLUMN status TEXT NOT NULL DEFAULT 'queued' CHECK (status IN ('queued','processing','sent','failed')),
    ADD COLUMN attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    ADD COLUMN failure_code VARCHAR(128) NOT NULL DEFAULT '',
    ADD COLUMN sent_at TIMESTAMPTZ;

UPDATE ops.incident_notifications n
SET status=d.status,attempts=d.attempts,failure_code=d.failure_code,sent_at=d.sent_at
FROM app.email_deliveries d WHERE d.id=n.delivery_id;

ALTER TABLE ops.incident_notifications ADD CONSTRAINT incident_notifications_delivery_id_fkey
    FOREIGN KEY (delivery_id) REFERENCES app.email_deliveries(id);
ALTER TABLE ops.incident_notifications ADD CONSTRAINT incident_notifications_delivery_channel_check
    CHECK ((channel='email' AND delivery_id IS NOT NULL) OR (channel='console' AND delivery_id IS NULL));
