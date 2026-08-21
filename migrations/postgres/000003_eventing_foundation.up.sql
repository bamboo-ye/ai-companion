CREATE TABLE eventing.outbox_events (
    id UUID PRIMARY KEY,
    aggregate_type VARCHAR(64) NOT NULL,
    aggregate_id UUID NOT NULL,
    event_type VARCHAR(128) NOT NULL,
    event_version SMALLINT NOT NULL DEFAULT 1 CHECK (event_version > 0),
    payload JSONB NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    published_at TIMESTAMPTZ,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (
        status IN ('pending', 'publishing', 'published', 'dead_letter')
    ),
    available_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    last_error VARCHAR(1024),
    worker_id VARCHAR(128),
    lease_expires_at TIMESTAMPTZ,
    published_topic VARCHAR(128),
    published_partition INTEGER,
    published_offset BIGINT
);

CREATE INDEX outbox_unpublished_idx
    ON eventing.outbox_events (published_at, occurred_at);

CREATE INDEX outbox_relay_claim_idx
    ON eventing.outbox_events (status, available_at, lease_expires_at);

CREATE TABLE eventing.inbox_events (
    consumer_name VARCHAR(128) NOT NULL,
    event_id UUID NOT NULL,
    processed_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (consumer_name, event_id)
);

CREATE TABLE eventing.kafka_poison_messages (
    id BIGSERIAL PRIMARY KEY,
    consumer_name VARCHAR(128) NOT NULL,
    topic VARCHAR(128) NOT NULL,
    partition_no INTEGER NOT NULL,
    offset_no BIGINT NOT NULL,
    event_id VARCHAR(191),
    event_type VARCHAR(128),
    aggregate_id VARCHAR(191),
    reason VARCHAR(1024) NOT NULL,
    envelope JSONB,
    observed_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (consumer_name, topic, partition_no, offset_no)
);

CREATE INDEX kafka_poison_observed_idx
    ON eventing.kafka_poison_messages (observed_at);

CREATE INDEX kafka_poison_topic_idx
    ON eventing.kafka_poison_messages (topic, observed_at);

CREATE TABLE eventing.audit_logs (
    id BIGSERIAL PRIMARY KEY,
    actor_type VARCHAR(32) NOT NULL,
    actor_id UUID,
    action VARCHAR(128) NOT NULL,
    resource_type VARCHAR(64) NOT NULL,
    resource_id UUID,
    trace_id VARCHAR(64),
    metadata JSONB NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX audit_resource_idx
    ON eventing.audit_logs (resource_type, resource_id, occurred_at);

CREATE INDEX audit_actor_idx
    ON eventing.audit_logs (actor_type, actor_id, occurred_at);

CREATE TABLE eventing.compensation_records (
    id UUID PRIMARY KEY,
    source_type VARCHAR(64) NOT NULL,
    source_id VARCHAR(191) NOT NULL,
    action VARCHAR(128) NOT NULL,
    reason VARCHAR(512) NOT NULL,
    actor VARCHAR(128) NOT NULL,
    status TEXT NOT NULL DEFAULT 'recorded' CHECK (
        status IN ('recorded', 'completed', 'failed')
    ),
    metadata JSONB NOT NULL DEFAULT '{}'::JSONB,
    created_at TIMESTAMPTZ NOT NULL,
    completed_at TIMESTAMPTZ
);

CREATE INDEX compensation_source_idx
    ON eventing.compensation_records (source_type, source_id, created_at);

CREATE INDEX compensation_created_idx
    ON eventing.compensation_records (created_at);

CREATE INDEX compensation_status_idx
    ON eventing.compensation_records (status, created_at);
