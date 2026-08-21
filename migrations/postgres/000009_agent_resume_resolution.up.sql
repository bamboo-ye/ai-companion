ALTER TABLE agent.runs
    ADD COLUMN resume_resolution JSONB;

ALTER TABLE agent.interrupts
    ADD COLUMN resolution_key TEXT;

CREATE UNIQUE INDEX agent_interrupts_resolution_key_uq
    ON agent.interrupts (run_id, resolution_key)
    WHERE resolution_key IS NOT NULL;
