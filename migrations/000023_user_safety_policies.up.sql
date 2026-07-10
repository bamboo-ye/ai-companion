CREATE TABLE user_safety_policies (
    user_id BINARY(16) NOT NULL,
    minor_mode BOOLEAN NOT NULL DEFAULT FALSE,
    guardian_email VARCHAR(320) NOT NULL DEFAULT '',
    risky_skills_allowed BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (user_id),
    KEY idx_user_safety_policies_minor_mode (minor_mode,updated_at),
    CONSTRAINT fk_user_safety_policies_user FOREIGN KEY (user_id) REFERENCES users(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
