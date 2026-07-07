ALTER TABLE generation_jobs
    ADD COLUMN available_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) AFTER deadline_at,
    ADD COLUMN worker_id VARCHAR(128) NULL AFTER available_at,
    ADD COLUMN lease_expires_at TIMESTAMP(6) NULL AFTER worker_id,
    ADD KEY idx_generation_jobs_worker_claim (status,available_at,lease_expires_at);

UPDATE generation_jobs
SET status='cancelled',
    error_code='cancelled',
    error_message='generation was cancelled',
    completed_at=COALESCE(completed_at,CURRENT_TIMESTAMP(6)),
    worker_id=NULL,
    lease_expires_at=NULL
WHERE status='cancel_requested';

ALTER TABLE ledger_exports
    ADD COLUMN request_key VARCHAR(191) NULL AFTER timezone,
    ADD COLUMN available_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) AFTER status,
    ADD COLUMN attempts INT UNSIGNED NOT NULL DEFAULT 0 AFTER available_at,
    ADD COLUMN worker_id VARCHAR(128) NULL AFTER attempts,
    ADD COLUMN lease_expires_at TIMESTAMP(6) NULL AFTER worker_id,
    ADD COLUMN file_name VARCHAR(255) NULL AFTER storage_key,
    ADD COLUMN media_type VARCHAR(191) NULL AFTER file_name,
    ADD COLUMN last_error VARCHAR(1024) NULL AFTER failure_code,
    ADD UNIQUE KEY uk_ledger_exports_user_request (user_id,request_key),
    ADD KEY idx_ledger_exports_worker_claim (status,available_at,lease_expires_at);

ALTER TABLE notification_deliveries
    ADD COLUMN enqueued_at TIMESTAMP(6) NULL AFTER status,
    ADD KEY idx_notification_dispatch (status,enqueued_at,scheduled_at);

CREATE TABLE document_cleanup_jobs (
    id BINARY(16) NOT NULL,
    document_id BINARY(16) NOT NULL,
    user_id BINARY(16) NOT NULL,
    storage_key VARCHAR(512) NOT NULL,
    status ENUM('queued','processing','completed','failed') NOT NULL DEFAULT 'queued',
    attempts INT UNSIGNED NOT NULL DEFAULT 0,
    available_at TIMESTAMP(6) NOT NULL,
    worker_id VARCHAR(128) NULL,
    lease_expires_at TIMESTAMP(6) NULL,
    last_error VARCHAR(1024) NULL,
    created_at TIMESTAMP(6) NOT NULL,
    updated_at TIMESTAMP(6) NOT NULL,
    completed_at TIMESTAMP(6) NULL,
    PRIMARY KEY (id),
    UNIQUE KEY uk_document_cleanup_document (document_id),
    KEY idx_document_cleanup_claim (status,available_at,lease_expires_at),
    CONSTRAINT fk_document_cleanup_document FOREIGN KEY (document_id) REFERENCES documents(id),
    CONSTRAINT fk_document_cleanup_user FOREIGN KEY (user_id) REFERENCES users(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
