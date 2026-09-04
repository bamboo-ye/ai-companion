# ADR 0005: Database-first asynchronous dispatch with optional Kafka scaling

Status: accepted (2026-09-04)

Supersedes the transport requirement in ADR 0004. The Kafka adapter and its
event contracts remain supported.

## Context

The current product is a single-region modular monolith. PostgreSQL already
stores every durable job, Agent Run, retry schedule, lease and idempotency key.
Using Kafka as a mandatory hop adds a broker, topic provisioning, consumer-group
operations and another failure domain before measured load requires them.

## Decision

- PostgreSQL job rows and leases are the default asynchronous dispatch path.
  Workers poll runnable rows every second, claim with bounded concurrency and
  `SKIP LOCKED`, and retain the existing idempotency and retry contracts.
- Business transactions continue to write domain state and an Outbox record.
  In database mode, a settlement relay marks the Outbox handoff as
  `database.reconciled`; this records that durable domain state, rather than a
  broker delivery, is responsible for execution. It is not a task-completion
  acknowledgement.
- Kafka is an optional dispatch accelerator for horizontal scaling. Setting
  `KAFKA_ENABLED=true`, providing `KAFKA_BROKERS`, and enabling the Compose
  `kafka-scale` profile activates the existing Outbox publisher and consumer
  groups. Database polling then becomes a 30-second stranded-work recovery
  sweep.
- LangGraph checkpoints and business tables remain authoritative in both
  modes. Kafka delivery is at-least-once and never replaces leases, revision
  fencing, Inbox deduplication or reconciliation.
- Kafka becomes a recommended deployment step only after measurements show
  sustained database-claim pressure, a need for many Worker replicas, or event
  fan-out/retention requirements that the database dispatcher should not carry.

## Consequences

The default development and initial production topology no longer needs Kafka,
which reduces startup dependencies and operational cost. Database polling adds
bounded query load and up to one polling interval of dispatch latency. The
Kafka path remains continuously testable as a scale profile, but enabling it is
an explicit capacity decision rather than a release-readiness requirement.
