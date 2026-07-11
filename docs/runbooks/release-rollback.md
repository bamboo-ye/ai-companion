# Internal Release and Rollback Runbook

Date: 2026-07-11

This runbook is for backend internal releases from M8 onward. The bias is boring and reversible: ship small, verify durability and operator controls first, then expand traffic.

## Pre-release checklist

1. Code checks:
   - `make release-check`
   - `go vet ./...` when touching low-level platform code
   - `pnpm check` when touching Web code
2. Database:
   - forward migration reviewed
   - rollback migration exists for schema-only changes
   - migration tested on restored or local Docker MySQL
3. Kafka:
   - topic allowlist and Compose initializer match event schemas
   - Outbox relay metrics healthy
   - no unexpected `dead_letter` backlog
4. Operator and security controls:
   - `OPERATOR_MFA_REQUIRED=true` in production
   - at least one active `admin` operator account exists and can authenticate with TOTP
   - `/v1/ops/release-readiness` includes `active_admin_operator=passed`
   - `/v1/ops/release-readiness` includes `active_mfa_admin_operator=passed`
   - operator account management refuses to disable the last active admin or last active MFA admin with `operator_admin_lockout_protection`
   - legacy `OPERATOR_TOKEN` access is not used for normal operation
   - `/v1/ops/release-readiness` returns `status=ready`
   - audit CSV export can be downloaded by an admin operator
5. Reliability:
   - `/metrics` reachable from monitoring network
   - alerts loaded
   - Grafana dashboard importable
6. Product:
   - release notes include user-visible changes
   - known limitations and rollback trigger are written down

## Deployment order

1. Apply database migrations.
2. Deploy Worker with backward-compatible consumers.
3. Deploy API.
4. Run release-readiness check with an admin operator token and TOTP.
5. Deploy Web/native clients only when the server contract is stable.
6. Watch:
   - API 5xx
   - degradation level
   - queue lag and oldest job age
   - model error ratio and latency
   - DLQ list
   - audit log creation for operator actions

## Smoke test

After deployment:

1. Register/login with a test account.
2. Create a character.
3. Send one chat message and confirm `202`.
4. Upload a small text document and confirm queued status.
5. Query `/v1/reliability`.
6. Queue one ledger export in a non-production test user.
7. Query `/v1/ops/release-readiness` with an admin operator account.
8. Export `/v1/ops/audit-logs/export?format=csv&limit=20`.
9. Check `/metrics` includes request counters and reliability gauges.

Automated evidence collection:

```sh
API_BASE_URL="$API_BASE_URL" \
OPERATOR_ACCOUNT_TOKEN="$OPERATOR_ACCOUNT_TOKEN" \
OPERATOR_TOTP="$OPERATOR_TOTP" \
make release-evidence
```

The script writes evidence to `.release-evidence/<timestamp>/` by default. Do not commit that directory; copy the non-sensitive summaries into a release evidence record.
The bundle includes response headers; validation checks the required security headers from `healthz.headers`.

Validate the collected bundle before sign-off:

```sh
RELEASE_EVIDENCE_DIR=.release-evidence/<timestamp> make validate-release-evidence
```

Example operator readiness call:

```sh
curl -sS \
  -H "Authorization: Bearer $OPERATOR_ACCOUNT_TOKEN" \
  -H "X-Operator-TOTP: $OPERATOR_TOTP" \
  "$API_BASE_URL/v1/ops/release-readiness"
```

## Rollback triggers

Rollback or disable traffic when any is true:

- Chat acceptance returns persistent 5xx.
- Accepted jobs are not queryable.
- MySQL migration corrupts or blocks core writes.
- `/v1/ops/release-readiness` changes from `ready` to `blocked` after deployment.
- L3 remains active for > 15 minutes after dependency recovery.
- A release introduces cross-user data visibility.
- Admin operator MFA auth or audit export is broken.

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
- Release-readiness status:
- Audit export evidence:
- Rollback trigger:
- Verification:
- Known risks:
