# Internal Release and Rollback Runbook

Date: 2026-07-08

This runbook is for M6 internal releases. The bias is boring and reversible: ship small, verify durability first, then expand traffic.

## Pre-release checklist

1. Code checks:
   - `go test ./...`
   - `go vet ./...`
   - `pnpm check`
   - OpenAPI YAML parse
   - Compose config validation
2. Database:
   - forward migration reviewed
   - rollback migration exists for schema-only changes
   - migration tested on restored or local Docker MySQL
3. Kafka:
   - topic allowlist and Compose initializer match event schemas
   - Outbox relay metrics healthy
   - no unexpected `dead_letter` backlog
4. Reliability:
   - `/metrics` reachable from monitoring network
   - alerts loaded
   - Grafana dashboard importable
5. Product:
   - release notes include user-visible changes
   - known limitations and rollback trigger are written down

## Deployment order

1. Apply database migrations.
2. Deploy Worker with backward-compatible consumers.
3. Deploy API.
4. Deploy Web.
5. Watch:
   - API 5xx
   - degradation level
   - queue lag and oldest job age
   - model error ratio and latency
   - DLQ list

## Smoke test

After deployment:

1. Register/login with a test account.
2. Create a character.
3. Send one chat message and confirm `202`.
4. Upload a small text document and confirm queued status.
5. Query `/v1/reliability`.
6. Queue one ledger export in a non-production test user.
7. Check `/metrics` includes request counters and reliability gauges.

## Rollback triggers

Rollback or disable traffic when any is true:

- Chat acceptance returns persistent 5xx.
- Accepted jobs are not queryable.
- MySQL migration corrupts or blocks core writes.
- L3 remains active for > 15 minutes after dependency recovery.
- A release introduces cross-user data visibility.

## Rollback order

1. Stop or drain Web traffic if user-facing breakage is severe.
2. Roll back API to previous image/commit.
3. Roll back Worker only if consumers are causing side effects; otherwise keep Worker up to drain accepted work.
4. Do not roll back database schema unless the down migration was tested and no newer code has written incompatible rows.
5. If Kafka events were published by the faulty release, inspect DLQ and compensation records before replay.

## Communication

Internal release note template:

- Commit:
- Migration versions:
- User-visible changes:
- Operational changes:
- Rollback trigger:
- Verification:
- Known risks:
