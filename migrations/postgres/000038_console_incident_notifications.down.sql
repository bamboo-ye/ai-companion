DELETE FROM ops.incident_notifications WHERE channel='console';
DELETE FROM ops.alert_subscriptions WHERE channel='console';
ALTER TABLE ops.incident_notifications DROP CONSTRAINT incident_notifications_delivery_channel_check;
ALTER TABLE ops.incident_notifications DROP COLUMN sent_at,DROP COLUMN failure_code,DROP COLUMN attempts,DROP COLUMN status;
ALTER TABLE ops.incident_notifications ALTER COLUMN delivery_id SET NOT NULL;
ALTER TABLE ops.incident_notifications DROP CONSTRAINT incident_notifications_channel_check;
ALTER TABLE ops.incident_notifications ADD CONSTRAINT incident_notifications_channel_check CHECK (channel IN ('email'));
ALTER TABLE ops.alert_subscriptions DROP CONSTRAINT alert_subscriptions_channel_check;
ALTER TABLE ops.alert_subscriptions ADD CONSTRAINT alert_subscriptions_channel_check CHECK (channel IN ('email'));
