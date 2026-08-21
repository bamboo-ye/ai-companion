ALTER TABLE skill_runs
    ADD COLUMN conversation_id BINARY(16) NULL,
    ADD COLUMN origin_message_id BINARY(16) NULL,
    ADD CONSTRAINT skill_runs_conversation_fk FOREIGN KEY (conversation_id) REFERENCES conversations (id),
    ADD CONSTRAINT skill_runs_origin_message_fk FOREIGN KEY (origin_message_id) REFERENCES messages (id);

CREATE INDEX skill_runs_conversation_created_idx
    ON skill_runs (conversation_id, created_at);

CREATE TABLE skill_run_deliveries (
    run_id BINARY(16) NOT NULL,
    attempt SMALLINT NOT NULL,
    message_id BINARY(16) NOT NULL,
    created_at TIMESTAMP(6) NOT NULL,
    PRIMARY KEY (run_id, attempt),
    UNIQUE KEY skill_run_deliveries_message_uq (message_id),
    CONSTRAINT skill_run_deliveries_run_fk FOREIGN KEY (run_id) REFERENCES skill_runs (id),
    CONSTRAINT skill_run_deliveries_message_fk FOREIGN KEY (message_id) REFERENCES messages (id),
    CHECK (attempt > 1)
);

UPDATE skill_runs AS sr
JOIN messages AS message
  ON BIN_TO_UUID(message.id) = SUBSTRING_INDEX(SUBSTRING_INDEX(sr.create_key, ':', 3), ':', -1)
SET sr.conversation_id = message.conversation_id,
    sr.origin_message_id = message.id
WHERE sr.create_key LIKE 'chat-skill:%';

UPDATE skill_runs AS sr
JOIN messages AS message
  ON BIN_TO_UUID(message.id) = SUBSTRING_INDEX(SUBSTRING_INDEX(sr.create_key, ':', 2), ':', -1)
SET sr.conversation_id = message.conversation_id,
    sr.origin_message_id = message.id
WHERE sr.conversation_id IS NULL
  AND sr.create_key LIKE 'chat-skill:%';
