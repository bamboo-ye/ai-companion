ALTER TABLE skill_runs
    MODIFY COLUMN status ENUM('waiting_confirmation','queued','running','succeeded','failed','cancelled') NOT NULL,
    ADD COLUMN execution_mode ENUM('inline','worker') NOT NULL DEFAULT 'inline' AFTER skill_version,
    ADD COLUMN available_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) AFTER revision,
    ADD COLUMN worker_id VARCHAR(128) NULL AFTER available_at,
    ADD COLUMN lease_expires_at TIMESTAMP(6) NULL AFTER worker_id,
    ADD KEY idx_skill_runs_worker_claim (execution_mode,status,available_at,lease_expires_at);
