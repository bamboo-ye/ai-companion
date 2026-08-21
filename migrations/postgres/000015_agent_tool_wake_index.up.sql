CREATE INDEX agent_runs_waiting_tool_task_idx
    ON agent.runs (user_id, (resume_resolution->>'task_id'))
    WHERE status='waiting_tool';
