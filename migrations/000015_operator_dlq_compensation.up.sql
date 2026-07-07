CREATE TABLE compensation_records (
    id BINARY(16) NOT NULL,
    source_type VARCHAR(64) NOT NULL,
    source_id VARCHAR(191) NOT NULL,
    action VARCHAR(128) NOT NULL,
    reason VARCHAR(512) NOT NULL,
    actor VARCHAR(128) NOT NULL,
    status ENUM('recorded','completed','failed') NOT NULL DEFAULT 'recorded',
    metadata JSON NOT NULL,
    created_at TIMESTAMP(6) NOT NULL,
    completed_at TIMESTAMP(6) NULL,
    PRIMARY KEY (id),
    KEY idx_compensation_source (source_type,source_id,created_at),
    KEY idx_compensation_created (created_at),
    KEY idx_compensation_status (status,created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
