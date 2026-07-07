CREATE TABLE users (
    id BINARY(16) NOT NULL,
    email VARCHAR(320) NOT NULL,
    password_hash VARCHAR(255) NOT NULL,
    display_name VARCHAR(80) NOT NULL,
    timezone VARCHAR(64) NOT NULL,
    locale VARCHAR(16) NOT NULL DEFAULT 'zh-CN',
    status ENUM('active', 'disabled', 'deleted') NOT NULL DEFAULT 'active',
    created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    UNIQUE KEY uk_users_email (email)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE user_devices (
    id BINARY(16) NOT NULL,
    user_id BINARY(16) NOT NULL,
    device_key VARCHAR(191) NOT NULL,
    name VARCHAR(120) NOT NULL,
    platform ENUM('web', 'ios', 'android', 'service') NOT NULL,
    timezone VARCHAR(64) NOT NULL,
    last_seen_at TIMESTAMP(6) NOT NULL,
    created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    UNIQUE KEY uk_user_devices_key (user_id, device_key),
    CONSTRAINT fk_user_devices_user FOREIGN KEY (user_id) REFERENCES users(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE refresh_sessions (
    id BINARY(16) NOT NULL,
    user_id BINARY(16) NOT NULL,
    device_id BINARY(16) NOT NULL,
    token_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    expires_at TIMESTAMP(6) NOT NULL,
    last_used_at TIMESTAMP(6) NOT NULL,
    revoked_at TIMESTAMP(6) NULL,
    rotated_to_id BINARY(16) NULL,
    created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    UNIQUE KEY uk_refresh_sessions_token_hash (token_hash),
    KEY idx_refresh_sessions_user_active (user_id, revoked_at, expires_at),
    CONSTRAINT fk_refresh_sessions_user FOREIGN KEY (user_id) REFERENCES users(id),
    CONSTRAINT fk_refresh_sessions_device FOREIGN KEY (device_id) REFERENCES user_devices(id),
    CONSTRAINT fk_refresh_sessions_rotated_to FOREIGN KEY (rotated_to_id) REFERENCES refresh_sessions(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE characters (
    id BINARY(16) NOT NULL,
    user_id BINARY(16) NOT NULL,
    name VARCHAR(80) NOT NULL,
    avatar_url VARCHAR(2048) NULL,
    relationship_label VARCHAR(120) NOT NULL,
    personality TEXT NOT NULL,
    speech_style TEXT NOT NULL,
    hobbies JSON NOT NULL,
    boundaries JSON NOT NULL,
    initiative ENUM('low', 'balanced', 'high') NOT NULL DEFAULT 'balanced',
    reply_length ENUM('short', 'medium', 'long') NOT NULL DEFAULT 'short',
    sticker_style VARCHAR(255) NOT NULL DEFAULT '',
    raw_prompt TEXT NOT NULL,
    persona_version INT UNSIGNED NOT NULL DEFAULT 1,
    status ENUM('active', 'disabled', 'deleted') NOT NULL DEFAULT 'active',
    created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    KEY idx_characters_user_status (user_id, status, created_at),
    CONSTRAINT fk_characters_user FOREIGN KEY (user_id) REFERENCES users(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE character_persona_versions (
    character_id BINARY(16) NOT NULL,
    version INT UNSIGNED NOT NULL,
    compiler_version VARCHAR(64) NOT NULL,
    compiled_persona JSON NOT NULL,
    created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (character_id, version),
    CONSTRAINT fk_persona_versions_character FOREIGN KEY (character_id) REFERENCES characters(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE conversations (
    id BINARY(16) NOT NULL,
    user_id BINARY(16) NOT NULL,
    character_id BINARY(16) NOT NULL,
    title VARCHAR(255) NOT NULL DEFAULT '',
    next_sequence BIGINT UNSIGNED NOT NULL DEFAULT 1,
    last_message_at TIMESTAMP(6) NULL,
    created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    KEY idx_conversations_user_updated (user_id, updated_at),
    CONSTRAINT fk_conversations_user FOREIGN KEY (user_id) REFERENCES users(id),
    CONSTRAINT fk_conversations_character FOREIGN KEY (character_id) REFERENCES characters(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE messages (
    id BINARY(16) NOT NULL,
    conversation_id BINARY(16) NOT NULL,
    user_id BINARY(16) NOT NULL,
    role ENUM('user', 'assistant', 'system', 'tool') NOT NULL,
    sequence_no BIGINT UNSIGNED NOT NULL,
    bubble_no SMALLINT UNSIGNED NOT NULL DEFAULT 1,
    content TEXT NOT NULL,
    status ENUM('accepted', 'streaming', 'completed', 'cancelled', 'failed') NOT NULL,
    reply_to_id BINARY(16) NULL,
    created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    completed_at TIMESTAMP(6) NULL,
    PRIMARY KEY (id),
    UNIQUE KEY uk_messages_conversation_sequence_bubble (conversation_id, sequence_no, bubble_no),
    KEY idx_messages_conversation_cursor (conversation_id, sequence_no, bubble_no),
    CONSTRAINT fk_messages_conversation FOREIGN KEY (conversation_id) REFERENCES conversations(id),
    CONSTRAINT fk_messages_user FOREIGN KEY (user_id) REFERENCES users(id),
    CONSTRAINT fk_messages_reply_to FOREIGN KEY (reply_to_id) REFERENCES messages(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE generation_jobs (
    id BINARY(16) NOT NULL,
    conversation_id BINARY(16) NOT NULL,
    user_message_id BINARY(16) NOT NULL,
    status ENUM('accepted', 'running', 'completed', 'cancel_requested', 'cancelled', 'failed', 'timed_out') NOT NULL,
    attempt SMALLINT UNSIGNED NOT NULL DEFAULT 1,
    model_provider VARCHAR(64) NULL,
    model_name VARCHAR(128) NULL,
    deadline_at TIMESTAMP(6) NOT NULL,
    error_code VARCHAR(64) NULL,
    error_message VARCHAR(1024) NULL,
    created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    started_at TIMESTAMP(6) NULL,
    completed_at TIMESTAMP(6) NULL,
    PRIMARY KEY (id),
    UNIQUE KEY uk_generation_jobs_user_message_attempt (user_message_id, attempt),
    KEY idx_generation_jobs_conversation_status (conversation_id, status, created_at),
    CONSTRAINT fk_generation_jobs_conversation FOREIGN KEY (conversation_id) REFERENCES conversations(id),
    CONSTRAINT fk_generation_jobs_user_message FOREIGN KEY (user_message_id) REFERENCES messages(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE model_usage_records (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    user_id BINARY(16) NOT NULL,
    generation_job_id BINARY(16) NOT NULL,
    provider VARCHAR(64) NOT NULL,
    model VARCHAR(128) NOT NULL,
    input_tokens INT UNSIGNED NOT NULL DEFAULT 0,
    output_tokens INT UNSIGNED NOT NULL DEFAULT 0,
    estimated_cost_micros BIGINT UNSIGNED NOT NULL DEFAULT 0,
    latency_ms INT UNSIGNED NOT NULL DEFAULT 0,
    created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    KEY idx_model_usage_user_created (user_id, created_at),
    CONSTRAINT fk_model_usage_user FOREIGN KEY (user_id) REFERENCES users(id),
    CONSTRAINT fk_model_usage_job FOREIGN KEY (generation_job_id) REFERENCES generation_jobs(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
