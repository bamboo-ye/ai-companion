ALTER TABLE outbox_events
    ADD COLUMN status ENUM('pending','publishing','published','dead_letter') NOT NULL DEFAULT 'pending' AFTER published_at,
    ADD COLUMN available_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) AFTER status,
    ADD COLUMN worker_id VARCHAR(128) NULL AFTER attempts,
    ADD COLUMN lease_expires_at TIMESTAMP(6) NULL AFTER worker_id,
    ADD COLUMN published_topic VARCHAR(128) NULL AFTER lease_expires_at,
    ADD COLUMN published_partition INT NULL AFTER published_topic,
    ADD COLUMN published_offset BIGINT NULL AFTER published_partition,
    ADD KEY idx_outbox_relay_claim (status,available_at,lease_expires_at);

UPDATE outbox_events
SET status='published',available_at=occurred_at
WHERE published_at IS NOT NULL;

UPDATE outbox_events
SET available_at=occurred_at
WHERE published_at IS NULL;
