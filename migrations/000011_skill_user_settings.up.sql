CREATE TABLE user_skill_settings (
    user_id BINARY(16) NOT NULL,
    skill_name VARCHAR(128) NOT NULL,
    enabled BOOLEAN NOT NULL,
    created_at TIMESTAMP(6) NOT NULL,
    updated_at TIMESTAMP(6) NOT NULL,
    PRIMARY KEY (user_id,skill_name),
    KEY idx_user_skill_settings_updated (updated_at),
    CONSTRAINT fk_user_skill_settings_user FOREIGN KEY (user_id) REFERENCES users(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
