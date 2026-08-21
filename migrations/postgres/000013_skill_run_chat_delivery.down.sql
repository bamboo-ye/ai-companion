DROP TABLE IF EXISTS app.skill_run_deliveries;
DROP INDEX IF EXISTS app.skill_runs_conversation_created_idx;
ALTER TABLE app.skill_runs
    DROP COLUMN IF EXISTS origin_message_id,
    DROP COLUMN IF EXISTS conversation_id;
