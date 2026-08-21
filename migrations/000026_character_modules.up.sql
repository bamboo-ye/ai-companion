ALTER TABLE characters
    ADD COLUMN module_key ENUM('companion', 'life', 'work') NOT NULL DEFAULT 'companion' AFTER user_id,
    ADD KEY idx_characters_user_module_status (user_id, module_key, status, created_at);
