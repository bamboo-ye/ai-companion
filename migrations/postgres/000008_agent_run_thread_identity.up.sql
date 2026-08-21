ALTER TABLE agent.runs
    ADD CONSTRAINT agent_runs_thread_matches_id
    CHECK (thread_id = id::text) NOT VALID;

ALTER TABLE agent.runs
    VALIDATE CONSTRAINT agent_runs_thread_matches_id;
