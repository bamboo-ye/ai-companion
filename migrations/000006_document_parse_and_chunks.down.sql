DROP TABLE IF EXISTS document_chunks;
DROP TABLE IF EXISTS document_pages;
ALTER TABLE document_ingest_jobs
    DROP INDEX idx_document_ingest_lease,
    DROP COLUMN lease_expires_at,
    DROP COLUMN worker_id;
