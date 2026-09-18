CREATE TABLE app.wiki_owners (owner_id VARCHAR(64) PRIMARY KEY);
CREATE TABLE app.wiki_pages (
 id VARCHAR(64) PRIMARY KEY, owner_id VARCHAR(64) NOT NULL, version BIGINT NOT NULL,
 payload JSONB NOT NULL, updated_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX wiki_pages_owner ON app.wiki_pages(owner_id,updated_at);
CREATE TABLE app.wiki_versions (
 page_id VARCHAR(64) NOT NULL, owner_id VARCHAR(64) NOT NULL, version BIGINT NOT NULL,
 payload JSONB NOT NULL, created_at TIMESTAMPTZ NOT NULL, PRIMARY KEY(page_id,version)
);
CREATE TABLE app.wiki_jobs (
 document_id VARCHAR(64) PRIMARY KEY, owner_id VARCHAR(64) NOT NULL,
 status VARCHAR(20) NOT NULL, attempts INT NOT NULL DEFAULT 0, next_attempt TIMESTAMPTZ NOT NULL,
 lease_token VARCHAR(64) NOT NULL DEFAULT '', lease_until TIMESTAMPTZ, last_error VARCHAR(80) NOT NULL DEFAULT ''
);
CREATE INDEX wiki_jobs_claim ON app.wiki_jobs(status,next_attempt,lease_until);
CREATE TABLE app.wiki_feedback (
 id VARCHAR(64) PRIMARY KEY, owner_id VARCHAR(64) NOT NULL, page_id VARCHAR(64) NOT NULL,
 version BIGINT NOT NULL, payload JSONB NOT NULL, created_at TIMESTAMPTZ NOT NULL
);
INSERT INTO app.wiki_jobs (document_id,owner_id,status,next_attempt) SELECT id::text,user_id::text,'queued',CURRENT_TIMESTAMP FROM app.documents WHERE ingest_status='ready' AND deleted_at IS NULL;
INSERT INTO app.wiki_owners (owner_id) SELECT DISTINCT owner_id FROM app.wiki_jobs;
