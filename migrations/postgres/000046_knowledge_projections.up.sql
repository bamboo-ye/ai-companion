CREATE TABLE app.knowledge_projections (
 id VARCHAR(64) PRIMARY KEY,
 owner_id VARCHAR(64) NOT NULL,
 kind VARCHAR(32) NOT NULL,
 source_id VARCHAR(160) NOT NULL,
 spec_id VARCHAR(80) NOT NULL,
 payload JSONB NOT NULL,
 vector JSONB,
 status VARCHAR(16) NOT NULL,
 attempts INTEGER NOT NULL DEFAULT 0,
 failure_code VARCHAR(80),
 lease_token VARCHAR(64),
 lease_expires TIMESTAMPTZ,
 available_at TIMESTAMPTZ NOT NULL,
 last_seen TIMESTAMPTZ NOT NULL
);
CREATE INDEX knowledge_projection_claim ON app.knowledge_projections(spec_id,status,available_at);
CREATE INDEX knowledge_projection_source ON app.knowledge_projections(owner_id,kind,source_id);
CREATE INDEX knowledge_projection_retention ON app.knowledge_projections(last_seen);

CREATE TABLE app.knowledge_feedback (id VARCHAR(64) PRIMARY KEY,user_id VARCHAR(64) NOT NULL,rating VARCHAR(32) NOT NULL,payload JSONB NOT NULL,created_at TIMESTAMPTZ NOT NULL,status VARCHAR(16) NOT NULL);
CREATE INDEX knowledge_feedback_pending ON app.knowledge_feedback(status,created_at);
