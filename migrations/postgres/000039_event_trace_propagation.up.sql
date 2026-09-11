ALTER TABLE eventing.outbox_events
    ADD COLUMN trace_id VARCHAR(64);

CREATE INDEX idx_eventing_outbox_trace
    ON eventing.outbox_events (trace_id, occurred_at DESC)
    WHERE trace_id IS NOT NULL;
