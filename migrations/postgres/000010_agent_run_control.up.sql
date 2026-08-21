ALTER TABLE agent.runs
    ADD COLUMN deadline_at TIMESTAMPTZ;

UPDATE agent.runs
SET deadline_at = created_at + INTERVAL '15 minutes'
WHERE deadline_at IS NULL;

ALTER TABLE agent.runs
    ALTER COLUMN deadline_at SET NOT NULL;

ALTER TABLE agent.runs
    DROP CONSTRAINT IF EXISTS runs_status_check;

ALTER TABLE agent.runs
    DROP CONSTRAINT IF EXISTS agent_runs_status_check;

ALTER TABLE agent.runs
    ADD CONSTRAINT agent_runs_status_check CHECK (
        status IN (
            'accepted',
            'queued',
            'running',
            'waiting_approval',
            'waiting_tool',
            'completed',
            'failed',
            'cancel_requested',
            'cancelled',
            'timed_out'
        )
    );

CREATE INDEX agent_runs_deadline_idx
    ON agent.runs (deadline_at, created_at)
    WHERE status IN (
        'accepted',
        'queued',
        'running',
        'waiting_approval',
        'waiting_tool',
        'cancel_requested'
    );
