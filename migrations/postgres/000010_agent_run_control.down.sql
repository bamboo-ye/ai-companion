DROP INDEX IF EXISTS agent.agent_runs_deadline_idx;

UPDATE agent.runs
SET status = 'failed',
    error_code = 'run_timeout',
    error_message = 'Agent run exceeded its total deadline'
WHERE status = 'timed_out';

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
            'cancelled'
        )
    );

ALTER TABLE agent.runs
    DROP COLUMN deadline_at;
