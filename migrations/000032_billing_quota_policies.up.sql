CREATE TABLE billing_quota_policies (
    scope_key VARCHAR(70) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
    limits_json TEXT NOT NULL,
    revision BIGINT NOT NULL CHECK (revision > 0),
    actor VARCHAR(128) NOT NULL,
    reason VARCHAR(512) NOT NULL,
    updated_at DATETIME(6) NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
