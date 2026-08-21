CREATE INDEX agent_run_events_retry_window_idx
    ON agent.run_events (created_at, run_id)
    WHERE event_type = 'queued';

CREATE INDEX agent_runs_terminal_metrics_idx
    ON agent.runs (completed_at)
    WHERE status IN ('completed', 'failed', 'cancelled', 'timed_out');
