CREATE TABLE knowledge_projections (
 id VARCHAR(64) PRIMARY KEY,
 owner_id VARCHAR(64) NOT NULL,
 kind VARCHAR(32) NOT NULL,
 source_id VARCHAR(160) NOT NULL,
 spec_id VARCHAR(80) NOT NULL,
 payload JSON NOT NULL,
 vector JSON,
 status VARCHAR(16) NOT NULL,
 attempts INTEGER NOT NULL DEFAULT 0,
 failure_code VARCHAR(80),
 lease_token VARCHAR(64),
 lease_expires DATETIME(6),
 available_at DATETIME(6) NOT NULL,
 last_seen DATETIME(6) NOT NULL
);
CREATE INDEX knowledge_projection_claim ON knowledge_projections(spec_id,status,available_at);
CREATE INDEX knowledge_projection_source ON knowledge_projections(owner_id,kind,source_id);
CREATE INDEX knowledge_projection_retention ON knowledge_projections(last_seen);

CREATE TABLE knowledge_feedback (id VARCHAR(64) PRIMARY KEY,user_id VARCHAR(64) NOT NULL,rating VARCHAR(32) NOT NULL,payload JSON NOT NULL,created_at DATETIME(6) NOT NULL,status VARCHAR(16) NOT NULL);
CREATE INDEX knowledge_feedback_pending ON knowledge_feedback(status,created_at);
