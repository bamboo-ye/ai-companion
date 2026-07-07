CREATE TABLE files (
    id BINARY(16) NOT NULL,
    user_id BINARY(16) NOT NULL,
    original_name VARCHAR(255) NOT NULL,
    media_type VARCHAR(128) NOT NULL,
    size_bytes BIGINT UNSIGNED NOT NULL,
    sha256 CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    storage_key VARCHAR(512) NOT NULL,
    status ENUM('active','deleted') NOT NULL DEFAULT 'active',
    active_sha256 CHAR(64) CHARACTER SET ascii COLLATE ascii_bin GENERATED ALWAYS AS (CASE WHEN status='active' THEN sha256 ELSE NULL END) STORED,
    created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    deleted_at TIMESTAMP(6) NULL,
    PRIMARY KEY (id),
    UNIQUE KEY uk_files_user_active_hash (user_id,active_sha256),
    KEY idx_files_user_hash_status (user_id,sha256,status),
    KEY idx_files_user_created (user_id,status,created_at),
    CONSTRAINT fk_files_user FOREIGN KEY (user_id) REFERENCES users(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE documents (
    id BINARY(16) NOT NULL,
    user_id BINARY(16) NOT NULL,
    file_id BINARY(16) NOT NULL,
    ingest_status ENUM('queued','processing','ready','failed','deleted') NOT NULL DEFAULT 'queued',
    parser_version VARCHAR(64) NULL,
    page_count INT UNSIGNED NOT NULL DEFAULT 0,
    chunk_count INT UNSIGNED NOT NULL DEFAULT 0,
    failure_code VARCHAR(128) NULL,
    created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    deleted_at TIMESTAMP(6) NULL,
    PRIMARY KEY (id),
    UNIQUE KEY uk_documents_file (file_id),
    KEY idx_documents_user_status_created (user_id,ingest_status,created_at),
    CONSTRAINT fk_documents_user FOREIGN KEY (user_id) REFERENCES users(id),
    CONSTRAINT fk_documents_file FOREIGN KEY (file_id) REFERENCES files(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE document_ingest_jobs (
    id BINARY(16) NOT NULL,
    document_id BINARY(16) NOT NULL,
    user_id BINARY(16) NOT NULL,
    status ENUM('queued','processing','completed','failed','cancelled') NOT NULL DEFAULT 'queued',
    idempotency_key VARCHAR(160) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    attempts INT UNSIGNED NOT NULL DEFAULT 0,
    available_at TIMESTAMP(6) NOT NULL,
    started_at TIMESTAMP(6) NULL,
    finished_at TIMESTAMP(6) NULL,
    last_error VARCHAR(1024) NULL,
    created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    UNIQUE KEY uk_document_ingest_idempotency (idempotency_key),
    KEY idx_document_ingest_ready (status,available_at),
    KEY idx_document_ingest_user (user_id,status,created_at),
    CONSTRAINT fk_document_ingest_document FOREIGN KEY (document_id) REFERENCES documents(id),
    CONSTRAINT fk_document_ingest_user FOREIGN KEY (user_id) REFERENCES users(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
