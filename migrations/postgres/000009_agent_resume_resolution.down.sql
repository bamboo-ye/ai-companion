DROP INDEX IF EXISTS agent.agent_interrupts_resolution_key_uq;

ALTER TABLE agent.interrupts
    DROP COLUMN IF EXISTS resolution_key;

ALTER TABLE agent.runs
    DROP COLUMN IF EXISTS resume_resolution;
