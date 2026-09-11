DROP INDEX IF EXISTS eventing.idx_eventing_outbox_trace;

ALTER TABLE eventing.outbox_events
    DROP COLUMN IF EXISTS trace_id;
