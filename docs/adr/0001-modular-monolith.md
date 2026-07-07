# ADR 0001: Start with a modular monolith

- Status: Accepted
- Date: 2026-07-01

## Context

伴AI spans chat, memory, RAG, life tools, office skills, finance, billing, and administration. Splitting every capability into a service before boundaries and traffic are known would slow delivery and make local development fragile.

## Decision

Use one Go module with explicit domain packages and separate `api` and `worker` processes. Use Kafka contracts for asynchronous work. Python algorithm workers are isolated behind Kafka/gRPC contracts and cannot bypass Go authorization for external side effects.

## Consequences

Transactions and refactoring remain simple during product discovery. Domain modules must not modify each other's tables directly. A module may be extracted after load, ownership, or deployment requirements justify it.

