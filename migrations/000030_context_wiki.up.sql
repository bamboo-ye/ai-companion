CREATE TABLE wiki_owners (owner_id VARCHAR(64) PRIMARY KEY);
CREATE TABLE wiki_pages (
 id VARCHAR(64) PRIMARY KEY, owner_id VARCHAR(64) NOT NULL, version BIGINT NOT NULL,
 payload JSON NOT NULL, updated_at DATETIME(6) NOT NULL
);
CREATE INDEX wiki_pages_owner ON wiki_pages(owner_id,updated_at);
CREATE TABLE wiki_versions (
 page_id VARCHAR(64) NOT NULL, owner_id VARCHAR(64) NOT NULL, version BIGINT NOT NULL,
 payload JSON NOT NULL, created_at DATETIME(6) NOT NULL, PRIMARY KEY(page_id,version)
);
CREATE TABLE wiki_jobs (
 document_id VARCHAR(64) PRIMARY KEY, owner_id VARCHAR(64) NOT NULL,
 status VARCHAR(20) NOT NULL, attempts INT NOT NULL DEFAULT 0, next_attempt DATETIME(6) NOT NULL,
 lease_token VARCHAR(64) NOT NULL DEFAULT '', lease_until DATETIME(6), last_error VARCHAR(80) NOT NULL DEFAULT ''
);
CREATE INDEX wiki_jobs_claim ON wiki_jobs(status,next_attempt,lease_until);
CREATE TABLE wiki_feedback (
 id VARCHAR(64) PRIMARY KEY, owner_id VARCHAR(64) NOT NULL, page_id VARCHAR(64) NOT NULL,
 version BIGINT NOT NULL, payload JSON NOT NULL, created_at DATETIME(6) NOT NULL
);
INSERT INTO wiki_jobs (document_id,owner_id,status,next_attempt) SELECT BIN_TO_UUID(id),BIN_TO_UUID(user_id),'queued',CURRENT_TIMESTAMP FROM documents WHERE ingest_status='ready' AND deleted_at IS NULL;
INSERT INTO wiki_owners (owner_id) SELECT DISTINCT owner_id FROM wiki_jobs;
