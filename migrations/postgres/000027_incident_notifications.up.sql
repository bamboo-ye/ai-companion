CREATE TABLE ops.alert_subscriptions (
    id UUID PRIMARY KEY,
    name VARCHAR(128) NOT NULL,
    rule_id UUID REFERENCES ops.alert_rules(id),
    channel VARCHAR(16) NOT NULL CHECK (channel IN ('email')),
    target VARCHAR(320) NOT NULL,
    minimum_severity VARCHAR(16) NOT NULL CHECK (minimum_severity IN ('warning', 'critical')),
    notify_on_open BOOLEAN NOT NULL DEFAULT TRUE,
    notify_on_resolved BOOLEAN NOT NULL DEFAULT TRUE,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_by VARCHAR(128) NOT NULL,
    updated_by VARCHAR(128) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (notify_on_open OR notify_on_resolved)
);

CREATE UNIQUE INDEX alert_subscriptions_unique_target_idx
    ON ops.alert_subscriptions (COALESCE(rule_id, '00000000-0000-0000-0000-000000000000'::uuid), channel, LOWER(target));
CREATE INDEX alert_subscriptions_enabled_idx
    ON ops.alert_subscriptions (enabled, minimum_severity, rule_id);

CREATE TABLE ops.incident_notifications (
    id UUID PRIMARY KEY,
    incident_id UUID NOT NULL REFERENCES ops.incidents(id) ON DELETE CASCADE,
    subscription_id UUID NOT NULL REFERENCES ops.alert_subscriptions(id),
    transition VARCHAR(16) NOT NULL CHECK (transition IN ('opened', 'resolved')),
    channel VARCHAR(16) NOT NULL CHECK (channel IN ('email')),
    target VARCHAR(320) NOT NULL,
    delivery_id UUID NOT NULL REFERENCES app.email_deliveries(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (incident_id, subscription_id, transition)
);

CREATE INDEX incident_notifications_incident_idx
    ON ops.incident_notifications (incident_id, created_at DESC);
CREATE INDEX incident_notifications_delivery_idx
    ON ops.incident_notifications (delivery_id);
