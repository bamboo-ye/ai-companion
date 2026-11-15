ALTER TABLE agent_runs DROP CONSTRAINT agent_runs_message_id_key;
ALTER TABLE agent_runs ADD COLUMN retry_of uuid UNIQUE REFERENCES agent_runs(id) ON DELETE SET NULL;
CREATE INDEX agent_runs_message_idx ON agent_runs(message_id,created_at DESC);
