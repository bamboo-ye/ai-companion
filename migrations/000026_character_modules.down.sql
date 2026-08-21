ALTER TABLE characters
    DROP INDEX idx_characters_user_module_status,
    DROP COLUMN module_key;
