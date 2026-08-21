ALTER TABLE conversations
    DROP INDEX idx_conversations_user_status_updated,
    DROP COLUMN status;
