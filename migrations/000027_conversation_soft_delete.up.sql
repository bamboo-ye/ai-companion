ALTER TABLE conversations
    ADD COLUMN status ENUM('active', 'deleted') NOT NULL DEFAULT 'active' AFTER title,
    ADD KEY idx_conversations_user_status_updated (user_id, status, updated_at);
