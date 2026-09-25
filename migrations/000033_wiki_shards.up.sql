CREATE TABLE wiki_shards (
 owner_id VARCHAR(64) NOT NULL, document_id VARCHAR(64) NOT NULL,
 cache_key VARCHAR(64) NOT NULL, payload JSON NOT NULL, expires_at DATETIME(6) NOT NULL,
 PRIMARY KEY (owner_id,document_id,cache_key)
);
CREATE INDEX wiki_shards_expiry ON wiki_shards(expires_at);
