CREATE TABLE app.wiki_shards (
 owner_id VARCHAR(64) NOT NULL, document_id VARCHAR(64) NOT NULL,
 cache_key VARCHAR(64) NOT NULL, payload JSONB NOT NULL, expires_at TIMESTAMPTZ NOT NULL,
 PRIMARY KEY (owner_id,document_id,cache_key)
);
CREATE INDEX wiki_shards_expiry ON app.wiki_shards(expires_at);
