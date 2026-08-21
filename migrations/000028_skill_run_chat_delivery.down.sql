DROP TABLE IF EXISTS skill_run_deliveries;
DROP INDEX skill_runs_conversation_created_idx ON skill_runs;
ALTER TABLE skill_runs
    DROP FOREIGN KEY skill_runs_origin_message_fk,
    DROP FOREIGN KEY skill_runs_conversation_fk,
    DROP COLUMN origin_message_id,
    DROP COLUMN conversation_id;
