CREATE TABLE operator_accounts (
    id VARCHAR(128) NOT NULL,
    display_name VARCHAR(160) NOT NULL,
    role ENUM('viewer','support','admin') NOT NULL DEFAULT 'viewer',
    status ENUM('active','disabled') NOT NULL DEFAULT 'active',
    token_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    totp_secret VARCHAR(128) NOT NULL DEFAULT '',
    mfa_enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    last_authenticated_at TIMESTAMP(6) NULL,
    PRIMARY KEY (id),
    UNIQUE KEY uk_operator_accounts_token_hash (token_hash),
    KEY idx_operator_accounts_role_status (role,status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
