# Backup, Restore, Deletion, and Export Drills

Date: 2026-07-08

This runbook defines the minimum drill set for the M6 internal release. It is written for the current local/early deployment shape: MySQL as authority, object storage/local file stores for artifacts, Kafka as asynchronous transport, and Qdrant as rebuildable retrieval index.

## Data classes

| Data | Authority | Recovery rule |
|---|---|---|
| Accounts, sessions, characters, chat, memory, ledger, reminders, Skill runs | MySQL | Restore from backup; never reconstruct from logs. |
| Outbox/Inbox and compensation records | MySQL | Restore with business data; they are part of recovery state. |
| Uploaded documents and generated files | object storage / local file store | Restore from object backup; metadata lives in MySQL. |
| Qdrant document index | Qdrant | Rebuild from MySQL document chunks if necessary. |
| Kafka messages | Kafka | Not the authority; accepted work is recoverable through MySQL Outbox and durable job tables. |

## MySQL restore drill

Goal: prove RPO <= 15 minutes and RTO <= 2 hours.

Steps:

1. Create a labeled backup from the source environment.
2. Restore to an isolated database/schema.
3. Run migrations against the restored schema.
4. Verify:
   - latest `schema_migrations`
   - sample user can authenticate in the restored environment
   - recent conversations and generation jobs exist
   - Outbox dead-letter and compensation records are present
5. Start API/Worker against the restored database in isolation.
6. Confirm `/readyz`, `/metrics`, and an authenticated `/v1/reliability` snapshot.
7. Record elapsed restore time and backup timestamp.

## Object store restore drill

Steps:

1. Pick one uploaded document and one generated ledger/Skill artifact.
2. Restore object keys to an isolated bucket or local directory.
3. Confirm metadata rows point to restored keys.
4. Download through authenticated API.
5. Delete the isolated copy after evidence is captured.

## Qdrant rebuild drill

Qdrant is rebuildable state.

Steps:

1. Create an isolated Qdrant collection.
2. Replay ready document chunks from MySQL into the collection.
3. Run the M2 retrieval evaluation gate.
4. Compare document query citations against the source collection.
5. Switch only after the new collection passes quality and freshness checks.

## Account deletion drill

Goal: verify user-visible data disappears from query paths and external cleanup jobs are durable.

Current M6 scope has document deletion and memory deletion primitives; full account deletion remains a future compliance workflow. For available deletion paths:

1. Create a test user with memory, uploaded document, ledger entry, reminder, and generated file.
2. Delete memory and document through API.
3. Confirm:
   - deleted memory is not recalled
   - deleted document cannot be read or queried
   - document cleanup job exists or completed
   - object/vector cleanup is eventually completed or retryable
4. Record any data class still requiring full account deletion workflow.

## Data export drill

Steps:

1. Create ledger entries for a test user.
2. Queue ledger export with an `Idempotency-Key`.
3. Verify `202 Accepted`, polling, Worker completion, and authenticated file download.
4. Reuse the same idempotency key and confirm dedupe.
5. Confirm exported file hash/size is stored and download does not expose other users' artifacts.

## Drill evidence template

- Date/time:
- Environment:
- Commit:
- Operator:
- Backup timestamp:
- RPO observed:
- RTO observed:
- Checks passed:
- Compensations/manual actions:
- Follow-up issues:
