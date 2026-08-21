ALTER TABLE agent.runs
    DROP CONSTRAINT IF EXISTS agent_runs_character_fk,
    DROP CONSTRAINT IF EXISTS agent_runs_conversation_fk,
    DROP CONSTRAINT IF EXISTS agent_runs_user_fk;

DROP TABLE IF EXISTS app.conversation_summaries;
DROP TABLE IF EXISTS app.generation_job_events;
DROP TABLE IF EXISTS app.model_usage_records;
DROP TABLE IF EXISTS app.generation_jobs;
DROP TABLE IF EXISTS app.messages;
DROP TABLE IF EXISTS app.conversations;
DROP TABLE IF EXISTS app.character_persona_versions;
DROP TABLE IF EXISTS app.characters;
DROP TABLE IF EXISTS app.refresh_sessions;
DROP TABLE IF EXISTS app.user_devices;
DROP TABLE IF EXISTS app.users;
