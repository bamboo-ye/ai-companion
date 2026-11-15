CREATE TABLE product_leaflets (
 id text NOT NULL, version text NOT NULL, data jsonb NOT NULL,
 content_sha256 text NOT NULL, review_status text NOT NULL CHECK(review_status IN ('reference','reviewed','retired')),
 valid_until timestamptz NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(id,version)
);
CREATE INDEX product_leaflets_published_idx ON product_leaflets(review_status,valid_until);
