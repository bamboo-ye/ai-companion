ALTER TABLE app.skill_runs
    ADD COLUMN conversation_id UUID REFERENCES app.conversations (id),
    ADD COLUMN origin_message_id UUID REFERENCES app.messages (id);

CREATE INDEX skill_runs_conversation_created_idx
    ON app.skill_runs (conversation_id, created_at DESC)
    WHERE conversation_id IS NOT NULL;

CREATE TABLE app.skill_run_deliveries (
    run_id UUID NOT NULL REFERENCES app.skill_runs (id),
    attempt SMALLINT NOT NULL CHECK (attempt > 1),
    message_id UUID NOT NULL UNIQUE REFERENCES app.messages (id),
    created_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (run_id, attempt)
);

UPDATE app.skill_runs AS sr
SET conversation_id = message.conversation_id,
    origin_message_id = message.id
FROM app.messages AS message
WHERE sr.create_key LIKE 'chat-skill:%'
  AND split_part(sr.create_key, ':', 3) ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
  AND message.id = split_part(sr.create_key, ':', 3)::UUID;

-- Early chat skill keys used chat-skill:<message-id>[:<job-id>].
UPDATE app.skill_runs AS sr
SET conversation_id = message.conversation_id,
    origin_message_id = message.id
FROM app.messages AS message
WHERE sr.conversation_id IS NULL
  AND sr.create_key LIKE 'chat-skill:%'
  AND split_part(sr.create_key, ':', 2) ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
  AND message.id = split_part(sr.create_key, ':', 2)::UUID;
