CREATE TABLE generation_job_events (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    generation_job_id BINARY(16) NOT NULL,
    event_type VARCHAR(64) NOT NULL,
    event_data JSON NOT NULL,
    created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    KEY idx_generation_job_events_job_cursor (generation_job_id, id),
    CONSTRAINT fk_generation_job_events_job FOREIGN KEY (generation_job_id) REFERENCES generation_jobs(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
