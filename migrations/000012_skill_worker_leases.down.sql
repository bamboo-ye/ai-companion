UPDATE skill_runs
SET status='failed',current_state='failed',error_code='queue_rollback',error_message='Queued execution was disabled by schema rollback',completed_at=CURRENT_TIMESTAMP(6)
WHERE status='queued';

ALTER TABLE skill_runs
    DROP INDEX idx_skill_runs_worker_claim,
    DROP COLUMN lease_expires_at,
    DROP COLUMN worker_id,
    DROP COLUMN available_at,
    DROP COLUMN execution_mode,
    MODIFY COLUMN status ENUM('waiting_confirmation','running','succeeded','failed','cancelled') NOT NULL;
