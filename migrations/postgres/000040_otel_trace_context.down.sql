ALTER TABLE ops.system_logs
    DROP COLUMN IF EXISTS trace_flags,
    DROP COLUMN IF EXISTS span_id;

ALTER TABLE eventing.outbox_events
    DROP COLUMN IF EXISTS tracestate,
    DROP COLUMN IF EXISTS traceparent;
