# ADR 0004: Kafka as the project-wide asynchronous transport

Status: accepted (2026-07-06)

## Context

The product requires one message-queue technology across the project. Earlier milestones used MySQL rows and leases both as durable job state and as a polling queue. That recovery model is useful, but database polling is not the production dispatch transport.

## Decision

- Kafka is the only production transport for asynchronous commands and events.
- A business transaction writes domain state and an Outbox row atomically. The Relay publishes the immutable event envelope to an allowlisted Kafka topic and records the broker topic, partition, and offset.
- Producers use `acks=all` and idempotent writes. Consumers use groups, disable automatic offset advancement, process before committing, and persist `consumer_name + event_id` in Inbox for duplicate suppression.
- Delivery is described honestly as at-least-once. Exactly-once Kafka-to-MySQL side effects are achieved through application idempotency and Inbox records, not marketing terminology or distributed two-phase commit.
- MySQL remains the source of truth for job status, leases, audit, retry schedule, DLQ state, and disaster recovery. A low-frequency reconciliation sweep may re-emit stranded work, but it is not the normal dispatch queue.
- Redis remains presence, rate-limit, and ephemeral realtime distribution infrastructure; it is not a durable task queue.
- Topic creation is explicit and allowlisted. Production IaC must provision the same topics as the Compose `kafka-init` service.

## Migration sequence

1. Transactional Outbox Relay, retry/DLQ state, Inbox primitive, fixed topic catalog, and Kafka observability.
2. Document and Skill workers consume `document.ingest.v1` and `skill.execute.v1`; database polling becomes reconciliation-only.
3. Chat generation, memory extraction, and notification delivery move to their Kafka command topics.
4. Remove remaining primary polling loops after duplicate-delivery, broker-outage, rebalance, and restore drills pass.

## Consequences

Kafka availability becomes part of production readiness, while accepted requests remain safe in Outbox during outages. Operating the system requires consumer-lag alerts, partition planning, retention policies, ACLs/TLS/SASL outside local Compose, DLQ replay controls, and schema compatibility checks.
