CREATE INDEX agent_runs_operations_created_idx
    ON agent.runs (created_at DESC, id DESC);

CREATE INDEX agent_runs_operations_filter_idx
    ON agent.runs (status, module_key, created_at DESC, id DESC);
