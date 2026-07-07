DROP TABLE IF EXISTS document_cleanup_jobs;

ALTER TABLE notification_deliveries
    DROP INDEX idx_notification_dispatch,
    DROP COLUMN enqueued_at;

ALTER TABLE ledger_exports
    DROP INDEX idx_ledger_exports_worker_claim,
    DROP INDEX uk_ledger_exports_user_request,
    DROP COLUMN last_error,
    DROP COLUMN media_type,
    DROP COLUMN file_name,
    DROP COLUMN lease_expires_at,
    DROP COLUMN worker_id,
    DROP COLUMN attempts,
    DROP COLUMN available_at,
    DROP COLUMN request_key;

ALTER TABLE generation_jobs
    DROP INDEX idx_generation_jobs_worker_claim,
    DROP COLUMN lease_expires_at,
    DROP COLUMN worker_id,
    DROP COLUMN available_at;
