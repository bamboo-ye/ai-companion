DROP INDEX IF EXISTS agent.agent_runs_dispatch_idx;

CREATE INDEX agent_runs_dispatch_idx
    ON agent.runs (status, available_at, created_at)
    WHERE status IN ('accepted', 'queued');
