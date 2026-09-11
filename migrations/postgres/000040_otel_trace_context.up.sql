ALTER TABLE eventing.outbox_events
    ADD COLUMN traceparent VARCHAR(255),
    ADD COLUMN tracestate VARCHAR(512);

COMMENT ON COLUMN eventing.outbox_events.traceparent IS
    'W3C Trace Context captured when the durable event was created';
COMMENT ON COLUMN eventing.outbox_events.tracestate IS
    'Vendor trace state propagated with traceparent';

ALTER TABLE ops.system_logs
    ADD COLUMN span_id VARCHAR(16),
    ADD COLUMN trace_flags VARCHAR(2);
