CREATE TABLE kafka_poison_messages (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    consumer_name VARCHAR(128) NOT NULL,
    topic VARCHAR(128) NOT NULL,
    partition_no INT NOT NULL,
    offset_no BIGINT NOT NULL,
    event_id VARCHAR(191) NULL,
    event_type VARCHAR(128) NULL,
    aggregate_id VARCHAR(191) NULL,
    reason VARCHAR(1024) NOT NULL,
    envelope JSON NULL,
    observed_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    UNIQUE KEY uk_kafka_poison_offset (consumer_name, topic, partition_no, offset_no),
    KEY idx_kafka_poison_observed (observed_at),
    KEY idx_kafka_poison_topic (topic, observed_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
