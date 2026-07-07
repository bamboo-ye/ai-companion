ALTER TABLE document_ingest_jobs
    ADD COLUMN worker_id VARCHAR(128) NULL AFTER last_error,
    ADD COLUMN lease_expires_at TIMESTAMP(6) NULL AFTER worker_id,
    ADD KEY idx_document_ingest_lease (status,lease_expires_at,available_at);

CREATE TABLE document_pages (
    id BINARY(16) NOT NULL,
    document_id BINARY(16) NOT NULL,
    user_id BINARY(16) NOT NULL,
    page_no INT UNSIGNED NOT NULL,
    text_content MEDIUMTEXT NOT NULL,
    quality_score DECIMAL(5,4) NOT NULL,
    parser_version VARCHAR(64) NOT NULL,
    content_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    UNIQUE KEY uk_document_pages_version (document_id,page_no,parser_version),
    KEY idx_document_pages_user_document (user_id,document_id,page_no),
    CONSTRAINT fk_document_pages_document FOREIGN KEY (document_id) REFERENCES documents(id),
    CONSTRAINT fk_document_pages_user FOREIGN KEY (user_id) REFERENCES users(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE document_chunks (
    id BINARY(16) NOT NULL,
    point_id BINARY(16) NOT NULL,
    document_id BINARY(16) NOT NULL,
    user_id BINARY(16) NOT NULL,
    ordinal_no INT UNSIGNED NOT NULL,
    page_start INT UNSIGNED NOT NULL,
    page_end INT UNSIGNED NOT NULL,
    section_path VARCHAR(512) NOT NULL DEFAULT '',
    content MEDIUMTEXT NOT NULL,
    token_count INT UNSIGNED NOT NULL,
    content_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    parser_version VARCHAR(64) NOT NULL,
    embedding_version VARCHAR(64) NOT NULL,
    created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    UNIQUE KEY uk_document_chunks_version (document_id,ordinal_no,parser_version),
    UNIQUE KEY uk_document_chunks_point (point_id),
    KEY idx_document_chunks_user_document (user_id,document_id,ordinal_no),
    CONSTRAINT fk_document_chunks_document FOREIGN KEY (document_id) REFERENCES documents(id),
    CONSTRAINT fk_document_chunks_user FOREIGN KEY (user_id) REFERENCES users(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
