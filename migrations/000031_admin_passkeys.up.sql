ALTER TABLE operator_accounts ADD COLUMN session_version BIGINT NOT NULL DEFAULT 1;
CREATE TABLE operator_passkey_records (
    record_key VARCHAR(100) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
    owner_id VARCHAR(128) COLLATE utf8mb4_bin NOT NULL,
    kind VARCHAR(24) NOT NULL,
    data MEDIUMTEXT NOT NULL,
    expires_at DATETIME(6) NOT NULL,
    revision BIGINT NOT NULL DEFAULT 1,
    INDEX operator_passkey_owner_idx(kind, owner_id),
    INDEX operator_passkey_expiry_idx(expires_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
