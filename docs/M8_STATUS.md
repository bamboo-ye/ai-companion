# M8 Backend Platform Status

Date: 2026-07-08

This is a historical slice-by-slice record. For the 2026-09-20 implementation
review, current Passkey login, product knowledge, quota overrides and verification
boundaries, see [project status](PROJECT_COMPLETION.md). Earlier management-key
login descriptions and test results below describe their original slices.

M7 native Android/iOS client development is intentionally skipped for this development pass. Work continues on backend/platform capabilities that do not require client UI.

## Slice 1: team workspace foundation

Implemented:

- Added workspace, workspace member, and workspace invitation domain models.
- Added MySQL migration `000017_team_workspaces` for:
  - `workspaces`
  - `workspace_members`
  - `workspace_invitations`
- Added in-memory and MySQL-backed Team stores.
- Added authenticated workspace APIs:
  - `GET /v1/workspaces`
  - `POST /v1/workspaces`
  - `GET /v1/workspaces/{workspace_id}/members`
  - `GET /v1/workspaces/{workspace_id}/invitations`
  - `POST /v1/workspaces/{workspace_id}/invitations`
  - `POST /v1/workspace-invitations/{invitation_id}/accept`
- Enforced role boundaries:
  - Only active members can list members.
  - Only owners/admins can create or list invitations.
  - Only the authenticated user whose email matches the invitation can accept it.
- Wired the Team store into the API process so MySQL deployments persist workspace data.
- Documented the API in OpenAPI.

## Verification

| Check | Result |
|---|---|
| Owner can create workspace | Passed |
| Owner can invite by email and role | Passed |
| Wrong user cannot accept another user's invitation | Passed |
| Matching invited user can accept and becomes member | Passed |
| Non-member cannot list workspace members | Passed |
| Go full test suite | Passed |

## Slice 2: workspace document sharing

Implemented:

- Added `workspace_document_shares` migration for many-to-many workspace/document links.
- Added document service/store support for idempotent sharing and workspace-visible document listing.
- Enforced access boundaries:
  - Only active workspace members can list shared documents.
  - Only active workspace members can attempt sharing into a workspace.
  - Only the document owner can share their personal document into the workspace.
  - Deleted documents are automatically hidden from workspace document lists.
- Added authenticated workspace document APIs:
  - `GET /v1/workspaces/{workspace_id}/documents`
  - `POST /v1/workspaces/{workspace_id}/documents`
- Documented the API in OpenAPI.

## Slice 2 Verification

| Check | Result |
|---|---|
| Owner can share own document into workspace | Passed |
| Workspace member can list shared document | Passed |
| Member cannot share another user's document | Passed |
| Non-member cannot list workspace documents | Passed |
| Deleted shared document disappears from workspace list | Passed |
| MySQL migration `000018_workspace_document_shares` | Passed |

## Slice 3: workspace-scoped document retrieval

Implemented:

- Added workspace-scoped document query API:
  - `POST /v1/workspaces/{workspace_id}/documents/query`
- Added document service support for querying only ready documents shared into a workspace.
- Added vector-index support for document-scoped search independent of a single owner `user_id`.
- Kept a service-level authorization guard after vector search:
  - candidate document IDs come from workspace shares;
  - only ready, non-deleted shared documents can become citations;
  - hits for unshared documents are discarded even if the vector index returns them.
- Reliability degradation policy also applies to workspace document retrieval.
- Documented the API in OpenAPI.

## Slice 3 Verification

| Check | Result |
|---|---|
| Workspace query cites only shared ready documents | Passed |
| Leaky vector results cannot expose unshared documents | Passed |
| Non-member cannot query workspace documents | Passed |
| Qdrant shared search uses document ID scope | Passed |
| Go full test suite | Passed |
| OpenAPI YAML parse | Passed |

## Slice 4: workspace skill artifact sharing

Implemented:

- Added `workspace_generated_file_shares` migration for many-to-many workspace/generated-file links.
- Added Skill service/store support for idempotent sharing, workspace-visible generated-file listing, and workspace-member download.
- Kept sharing scoped to generated files only; private Skill run input/output is not exposed through workspace APIs.
- Enforced access boundaries:
  - Only active workspace members can list or download shared generated files.
  - Only active workspace members can attempt sharing into a workspace.
  - Only the generated-file owner can share their file into the workspace.
- Added authenticated workspace Skill file APIs:
  - `GET /v1/workspaces/{workspace_id}/skill-files`
  - `POST /v1/workspaces/{workspace_id}/skill-files`
  - `GET /v1/workspaces/{workspace_id}/skill-files/{file_id}`
- Documented the API in OpenAPI.

## Slice 4 Verification

| Check | Result |
|---|---|
| Owner can share own Skill generated file | Passed |
| Workspace member can list shared Skill file | Passed |
| Workspace member can download shared Skill file | Passed |
| Member cannot share another user's Skill file | Passed |
| Non-member cannot list or download shared Skill files | Passed |
| MySQL migration `000019_workspace_generated_file_shares` | Passed |
| Go full test suite | Passed |
| OpenAPI YAML parse | Passed |

## Slice 5: workspace ledger export sharing

Implemented:

- Added `workspace_ledger_export_shares` migration for many-to-many workspace/ledger-export links.
- Added Ledger service/store support for idempotent sharing, workspace-visible export listing, and workspace-member download.
- Kept sharing scoped to completed export files only; raw ledger entries are not exposed through workspace APIs.
- Enforced access boundaries:
  - Only active workspace members can list or download shared ledger exports.
  - Only active workspace members can attempt sharing into a workspace.
  - Only the ledger export owner can share their completed export.
  - Queued, processing, failed, or missing-file exports cannot be shared.
- Added authenticated workspace ledger export APIs:
  - `GET /v1/workspaces/{workspace_id}/ledger-exports`
  - `POST /v1/workspaces/{workspace_id}/ledger-exports`
  - `GET /v1/workspaces/{workspace_id}/ledger-exports/{export_id}`
- Documented the API in OpenAPI.

## Slice 5 Verification

| Check | Result |
|---|---|
| Owner can share own completed ledger export | Passed |
| Workspace member can list shared ledger export | Passed |
| Workspace member can download shared ledger export | Passed |
| Member cannot share another user's ledger export | Passed |
| Non-member cannot list or download shared ledger exports | Passed |
| MySQL migration `000020_workspace_ledger_export_shares` | Passed |
| Go full test suite | Passed |
| OpenAPI YAML parse | Passed |

## Slice 6: workspace invitation email delivery

Implemented:

- Added reusable Email delivery domain service with:
  - queued delivery creation;
  - lease-based Worker claim;
  - sent/failed terminal status;
  - no-op sender for local/test environments;
  - SMTP sender adapter for real outbound email.
- Added `email_deliveries` migration with resource linkage, recipient, template, status, provider message ID, failure code, attempts, and lease fields.
- Creating a workspace invitation now queues an email delivery and returns `email_delivery` metadata in the API response.
- MySQL email delivery creation writes:
  - `email_deliveries`;
  - `audit_logs` entry for `email.delivery.queued`;
  - `outbox_events` command `email.deliver.v1`.
- Worker now supports:
  - email reconciler for durable queued deliveries;
  - Kafka consumer for `email.deliver.v1`;
  - SMTP sending when SMTP config is present, otherwise safe no-op sending.
- Successful and failed sends are recorded in `audit_logs` as `email.delivery.sent` / `email.delivery.failed`.
- Documented the API response in OpenAPI.

## Slice 6 Verification

| Check | Result |
|---|---|
| Workspace invitation queues email delivery | Passed |
| Email Worker send marks delivery sent | Passed |
| Kafka topic allowlist includes `email.deliver.v1` | Passed |
| OpenAPI documents `email_delivery` response | Passed |
| MySQL migration `000021_email_deliveries` | Passed |
| Go full test suite | Passed |
| OpenAPI YAML parse | Passed |

## Slice 7: email delivery operations

Implemented:

- Added Email delivery query and replay operations to the Email service/store:
  - list by status;
  - get detail by delivery ID;
  - replay only failed deliveries back to queued.
- MySQL replay writes:
  - delivery status back to `queued`;
  - a new `email.deliver.v1` outbox event;
  - `audit_logs` entry for `email.delivery.replayed`.
- Added operator APIs:
  - `GET /v1/ops/email/deliveries`
  - `GET /v1/ops/email/deliveries/{delivery_id}`
  - `POST /v1/ops/email/deliveries/{delivery_id}/replay`
- Replay requires an operator reason and creates a compensation record with action `email.delivery.replay`.
- Documented the operator APIs in OpenAPI.

## Slice 7 Verification

| Check | Result |
|---|---|
| Operator can list failed email deliveries | Passed |
| Operator can inspect one email delivery | Passed |
| Operator can replay failed delivery to queued | Passed |
| Replay creates compensation record | Passed |
| Replaying non-failed delivery is rejected | Passed |
| Go full test suite | Passed |
| OpenAPI YAML parse | Passed |
| Git diff whitespace check | Passed |

## Slice 8: subscription entitlement and quota checks

Implemented:

- Added Billing entitlement domain service with default plan catalog:
  - `free`: 10 active documents, 20 Skill runs/month, 3 owned workspaces;
  - `pro`: 200 active documents, 1000 Skill runs/month, 20 owned workspaces;
  - `team`: 1000 active documents, 5000 Skill runs/month, 100 owned workspaces.
- Added MySQL migration `000022_billing_entitlements` for:
  - `billing_plans`;
  - `billing_subscriptions`.
- Added authenticated Billing API:
  - `GET /v1/billing/me`
- Added quota guards to resource-creating endpoints:
  - document upload checks active document quota;
  - Skill run creation checks monthly Skill run quota;
  - workspace creation checks owned workspace quota.
- Quota exhaustion returns `402` with `quota_exceeded`, resource name, plan code, used count, and limit.
- Wired Billing store into the API process so MySQL deployments read subscriptions and count usage from persistent business tables.
- Documented the API and `402` responses in OpenAPI.

## Slice 8 Verification

| Check | Result |
|---|---|
| Billing summary returns implicit free plan and usage | Passed |
| Free plan blocks document upload after quota exhaustion | Passed |
| Free plan blocks Skill run creation after quota exhaustion | Passed |
| Free plan blocks workspace creation after quota exhaustion | Passed |
| MySQL migration `000022_billing_entitlements` | Passed |
| Go full test suite | Passed |
| OpenAPI YAML parse | Passed |
| Git diff whitespace check | Passed |

## Slice 9: minor/guardian safety policy and risky capability gating

Implemented:

- Added account-level Safety policy service with:
  - implicit default policy for users without a stored row;
  - `minor_mode`;
  - normalized `guardian_email`;
  - `risky_skills_allowed`.
- Added MySQL migration `000023_user_safety_policies` for `user_safety_policies`.
- MySQL policy updates write `audit_logs` entry `safety.policy.update`.
- Added authenticated Safety APIs:
  - `GET /v1/safety/me`
  - `PATCH /v1/safety/me`
- Enforced guardrails:
  - enabling `minor_mode` requires `guardian_email`;
  - `minor_mode` always forces `risky_skills_allowed=false`;
  - medium/high risk Skill starts are blocked when the account policy disallows risky skills;
  - no-risk Skills remain available under minor mode.
- Documented the APIs and `403 safety_capability_blocked` response in OpenAPI.

## Slice 9 Verification

| Check | Result |
|---|---|
| Default Safety policy allows safe and risky Skills for adult accounts | Passed |
| Enabling minor mode requires guardian email | Passed |
| Minor mode normalizes guardian email and forces risky Skills off | Passed |
| Minor mode blocks medium-risk Skill start | Passed |
| Minor mode allows no-risk Skill start | Passed |
| Adult account can voluntarily disable risky Skills | Passed |
| MySQL migration `000023_user_safety_policies` | Passed |
| Go full test suite | Passed |
| OpenAPI YAML parse | Passed |
| Git diff whitespace check | Passed |

## Slice 10: operator user moderation and support audit trail

Implemented:

- Added Identity admin service for operator-visible user account management.
- User status is now explicit in the Identity model and returned by admin/user-facing schemas.
- Disabled users are excluded from normal login and access-token authentication.
- Disabling a user revokes active refresh sessions.
- Added operator APIs:
  - `GET /v1/ops/users`
  - `GET /v1/ops/users/{user_id}`
  - `POST /v1/ops/users/{user_id}/disable`
  - `POST /v1/ops/users/{user_id}/enable`
  - `GET /v1/ops/audit-logs`
- Moderation actions require an operator reason and write `audit_logs` action `user.status.update`.
- Audit logs can be filtered by `resource_type` and `resource_id`.
- Documented the operator APIs in OpenAPI.

## Slice 10 Verification

| Check | Result |
|---|---|
| Operator can list and inspect users including status | Passed |
| Operator can disable a user with reason | Passed |
| Disabled user cannot use existing access token | Passed |
| Disabled user cannot log in | Passed |
| Operator can inspect moderation audit log | Passed |
| Operator can re-enable user and user can log in again | Passed |
| Go full test suite | Passed |
| OpenAPI YAML parse | Passed |
| Git diff whitespace check | Passed |

## Slice 11: operator MFA accounts and role-gated operations

Implemented:

- Added operator authentication domain with:
  - token hash lookup;
  - TOTP verification with ±1 time-step tolerance;
  - roles `viewer`, `support`, and `admin`;
  - last-authenticated timestamp update.
- Added MySQL migration `000024_operator_accounts` for `operator_accounts`.
- Added optional `OPERATOR_MFA_REQUIRED` config.
- Existing legacy `OPERATOR_TOKEN` remains supported when MFA is not required.
- When operator account authentication is configured:
  - account tokens are matched by SHA-256 token hash;
  - MFA-enabled accounts must provide `X-Operator-TOTP`;
  - account ID becomes the audited operator actor.
- Added role gates:
  - `viewer` can inspect operator surfaces;
  - `support`/`admin` can replay DLQ/email deliveries, create compensation records, and moderate users.
- Documented `X-Operator-TOTP`, role failures, and MFA behavior in OpenAPI and `.env.example`.

## Slice 11 Verification

| Check | Result |
|---|---|
| Operator account without TOTP is rejected when MFA is enabled | Passed |
| Legacy OPERATOR_TOKEN is rejected when MFA is required | Passed |
| Viewer operator can inspect DLQ | Passed |
| Viewer operator cannot replay DLQ | Passed |
| Support operator with valid TOTP can replay DLQ | Passed |
| MySQL migration `000024_operator_accounts` | Passed |
| Go full test suite | Passed |
| OpenAPI YAML parse | Passed |
| Git diff whitespace check | Passed |

## Slice 12: operator account bootstrap and admin management APIs

Implemented:

- Added admin-only operator account APIs:
  - `GET /v1/ops/operators`
  - `POST /v1/ops/operators`
  - `GET /v1/ops/operators/{operator_id}`
  - `POST /v1/ops/operators/{operator_id}/disable`
  - `POST /v1/ops/operators/{operator_id}/enable`
  - `POST /v1/ops/operators/{operator_id}/reset-token`
  - `POST /v1/ops/operators/{operator_id}/reset-mfa`
- Preserved legacy `OPERATOR_TOKEN` as an admin bootstrap path when `OPERATOR_MFA_REQUIRED=false`.
- Creation and reset endpoints return plaintext operator tokens/TOTP secrets only once.
- Operator account list/detail responses never expose token hashes or TOTP secrets.
- Added role enforcement so only `admin` operators can manage operator accounts; `viewer`/`support` remain blocked from these APIs.
- Added MySQL and memory-store support for operator account listing, creation, status changes, token reset, and MFA reset.
- Operator account mutations write `audit_logs` actions:
  - `operator.create`
  - `operator.status.update`
  - `operator.token.reset`
  - `operator.mfa.reset`
- Documented the operator account management APIs in OpenAPI.

## Slice 12 Verification

| Check | Result |
|---|---|
| Legacy operator token can bootstrap the first admin account | Passed |
| Admin can create support and viewer operator accounts | Passed |
| Viewer operator cannot create/manage operator accounts | Passed |
| Support operator can authenticate with generated token and TOTP | Passed |
| Reset token invalidates the old token and returns a one-time replacement | Passed |
| Disabled operator account cannot authenticate | Passed |
| Go full test suite | Passed |
| OpenAPI YAML parse | Passed |
| Git diff whitespace check | Passed |

## Slice 13: admin console bootstrap contract and audit CSV export

Implemented:

- Added `GET /v1/ops/console/bootstrap` for admin-console clients to load:
  - current operator actor/role/MFA/legacy context;
  - role-derived capability matrix;
  - navigation section contract;
  - supported filters for audit logs and operator accounts;
  - page/export limits.
- Extended audit log filtering with:
  - `actor_type`;
  - `action`.
- Added admin-only `GET /v1/ops/audit-logs/export` for CSV audit downloads.
- CSV export includes stable columns:
  - `id`
  - `occurred_at`
  - `actor_type`
  - `actor_id`
  - `actor_label`
  - `action`
  - `resource_type`
  - `resource_id`
  - `trace_id`
  - `metadata`
- Export defaults to CSV and caps exported rows at 5000.
- Documented console bootstrap, audit filters, and CSV export in OpenAPI.

## Slice 13 Verification

| Check | Result |
|---|---|
| Operator console bootstrap returns capability matrix and sections | Passed |
| Audit log list supports `actor_type` and `action` filters | Passed |
| Audit CSV export returns CSV headers, attachment filename, and matching records | Passed |
| Unsupported export format is rejected | Passed |
| Go full test suite | Passed |
| OpenAPI YAML parse | Passed |
| Git diff whitespace check | Passed |

## Slice 14: production operator security hardening

Implemented:

- Production configuration now refuses to start unless:
  - `AUTH_TOKEN_SECRET` is explicitly configured;
  - `OPERATOR_TOKEN` is explicitly configured;
  - `OPERATOR_MFA_REQUIRED=true`;
  - `WEB_ORIGIN` uses `https://`.
- Added global HTTP security headers:
  - `X-Content-Type-Options: nosniff`;
  - `X-Frame-Options: DENY`;
  - `Referrer-Policy: no-referrer`;
  - `Cross-Origin-Opener-Policy: same-origin`;
  - `Permissions-Policy: camera=(), microphone=(), geolocation=()`.
- Documented production operator MFA and HTTPS origin requirements in `.env.example`.

## Slice 14 Verification

| Check | Result |
|---|---|
| Production config rejects missing/default operator token | Passed |
| Production config rejects `OPERATOR_MFA_REQUIRED=false` | Passed |
| Production config rejects non-HTTPS `WEB_ORIGIN` | Passed |
| HTTP responses include security headers | Passed |
| Go full test suite | Passed |
| OpenAPI YAML parse | Passed |
| Git diff whitespace check | Passed |

## Slice 15: release-readiness checklist API

Implemented:

- Added admin-only `GET /v1/ops/release-readiness`.
- The checklist reports:
  - service readiness flag;
  - operator MFA requirement;
  - operator account store availability;
  - identity admin store availability;
  - operations store availability;
  - Kafka transport enablement;
  - production HTTPS `WEB_ORIGIN`;
  - global security header middleware;
  - non-development model provider configuration.
- The endpoint returns aggregate status:
  - `ready` when no required checks fail;
  - `blocked` when at least one required check fails.
- Non-required unmet checks are returned as `warning`.
- Documented release-readiness in OpenAPI.

## Slice 15 Verification

| Check | Result |
|---|---|
| Production readiness reports `blocked` when required checks fail | Passed |
| Viewer operator cannot access release-readiness | Passed |
| MFA admin operator can access release-readiness | Passed |
| Fully configured production-like server reports `ready` | Passed |
| Go full test suite | Passed |
| OpenAPI YAML parse | Passed |
| Git diff whitespace check | Passed |

## Slice 16: production runbook refresh and deploy evidence capture

Implemented:

- Added `make release-check` as the backend static release gate:
  - Go test suite;
  - Python worker tests;
  - OpenAPI YAML parse;
  - Compose config validation;
  - Git diff whitespace check.
- Refreshed `docs/runbooks/release-rollback.md` for M8:
  - operator MFA/account requirements;
  - release-readiness API gate;
  - audit CSV export evidence;
  - Kafka/DLQ watch items;
  - rollback triggers for readiness and operator auth regressions.
- Refreshed `docs/runbooks/security-fault-load-tests.md` for M8:
  - operator MFA and role checks;
  - security headers;
  - audit export;
  - release-readiness fault toggle.
- Added `docs/runbooks/evidence/M8_RELEASE_EVIDENCE_TEMPLATE.md` for internal release sign-off.

## Slice 16 Verification

| Check | Result |
|---|---|
| `make release-check` target exists and covers backend static gates | Passed |
| Release runbook references operator MFA and release-readiness | Passed |
| Security/fault/load plan includes M8 operator/audit checks | Passed |
| Evidence template includes readiness, audit export, fault/load, and rollback decision sections | Passed |
| Go full test suite | Passed |
| OpenAPI YAML parse | Passed |
| Git diff whitespace check | Passed |

## Slice 17: deployment evidence automation hook

Implemented:

- Added `scripts/collect_release_evidence.sh` to collect release evidence from a running API.
- The script captures:
  - `/healthz`;
  - `/readyz`;
  - `/v1/meta`;
  - `/metrics`;
  - `/v1/ops/console/bootstrap`;
  - `/v1/ops/release-readiness`;
  - `/v1/ops/outbox/dead-letter`;
  - `/v1/ops/kafka/poison-messages`;
  - `/v1/ops/email/deliveries`;
  - `/v1/ops/audit-logs`;
  - `/v1/ops/audit-logs/export`.
- Added `.release-evidence/` to `.gitignore` so internal evidence bundles are not accidentally committed.
- Added `make release-evidence` for operator-triggered evidence collection.
- Extended `make release-check` with shell syntax validation for the evidence collection script.
- Updated release runbook and M8 evidence template to reference the automated collection path.

## Slice 17 Verification

| Check | Result |
|---|---|
| Evidence collection script validates required env vars | Passed |
| Evidence collection script avoids writing operator token/TOTP to output | Passed |
| `make release-check` validates evidence script syntax | Passed |
| Release runbook references `make release-evidence` | Passed |
| Evidence template references generated artifacts | Passed |
| Go full test suite | Passed |
| OpenAPI YAML parse | Passed |
| Git diff whitespace check | Passed |

## Slice 18: release evidence validation gate

Implemented:

- Added `scripts/validate_release_evidence.sh` for offline validation of a collected evidence directory.
- The validator checks:
  - required evidence files are present and non-empty;
  - captured HTTP status files are 2xx;
  - `release-readiness.json` reports `status=ready`;
  - audit CSV header matches the stable contract;
  - metrics include `ai_companion_http_requests_total`;
  - evidence does not contain obvious operator token/TOTP leak patterns.
- Added `make validate-release-evidence` using `RELEASE_EVIDENCE_DIR`.
- Extended `make release-check` with shell syntax validation for the evidence validator.
- Updated release runbook and M8 evidence template to run validation after collection.

## Slice 18 Verification

| Check | Result |
|---|---|
| Evidence validator accepts a complete ready fixture | Passed |
| Evidence validator rejects missing/invalid evidence | Passed |
| Evidence validator rejects possible token/TOTP leaks | Passed |
| `make release-check` validates validator script syntax | Passed |
| Release runbook and evidence template reference validation command | Passed |
| Go full test suite | Passed |
| OpenAPI YAML parse | Passed |
| Git diff whitespace check | Passed |

## Slice 19: active admin operator release gate

Implemented:

- Extended `GET /v1/ops/release-readiness` with `active_admin_operator`.
- The check verifies the configured operator account store contains at least one active `admin` operator account.
- In production, missing active admin operator is a blocking `failed` check.
- In non-production, missing active admin operator is surfaced as a warning.
- Updated tests to cover both blocked and ready release-readiness responses.
- Updated release runbook and evidence template to require `active_admin_operator=passed`.

## Slice 19 Verification

| Check | Result |
|---|---|
| Release-readiness reports missing active admin operator as a failed production check | Passed |
| Release-readiness reports active admin operator in ready production-like response | Passed |
| Release runbook and evidence template mention the active admin gate | Passed |
| Go full test suite | Passed |
| OpenAPI YAML parse | Passed |
| Git diff whitespace check | Passed |

## Slice 20: release-readiness evidence detail validation

Implemented:

- Strengthened `scripts/validate_release_evidence.sh` to parse `release-readiness.json`.
- The validator now requires aggregate `status=ready` and these checks to be present and `passed`:
  - `service_ready`
  - `operator_mfa_required`
  - `operator_account_store`
  - `active_admin_operator`
  - `identity_admin_store`
  - `operations_store`
  - `kafka_transport_enabled` (historical key; ADR 0005 replaces it with optional `kafka_horizontal_scaling`)
  - `https_web_origin`
  - `security_headers_enabled`
  - `model_provider_configured`
- Updated the M8 evidence template with the explicit required readiness check list.

## Slice 20 Verification

| Check | Result |
|---|---|
| Evidence validator accepts a fixture with all required readiness checks passed | Passed |
| Evidence validator rejects a ready fixture missing a required readiness check | Passed |
| Evidence validator rejects a ready fixture with a required readiness check not passed | Passed |
| Evidence template lists required readiness checks | Passed |
| `make release-check` passes | Passed |
| Git diff whitespace check | Passed |

## Slice 21: operator account MFA enforcement

Implemented:

- Tightened operator account authentication so an account without MFA is not marked MFA-verified.
- When `OPERATOR_MFA_REQUIRED=true`, operator account authentication now rejects accounts that have not completed MFA verification.
- Extended `GET /v1/ops/release-readiness` with `active_mfa_admin_operator`.
- The new readiness check requires at least one active admin operator account with MFA enabled before production release.
- Updated release evidence validation so `active_mfa_admin_operator` must be present and `passed`.
- Updated release runbook and evidence template to include the active MFA admin gate.

## Slice 21 Verification

| Check | Result |
|---|---|
| `OPERATOR_MFA_REQUIRED=true` rejects an operator account without MFA | Passed |
| Release-readiness includes `active_mfa_admin_operator` in blocked and ready responses | Passed |
| Evidence validator requires `active_mfa_admin_operator=passed` | Passed |
| Release runbook and evidence template mention the active MFA admin gate | Passed |
| `make release-check` passes | Passed |
| Git diff whitespace check | Passed |

## Slice 22: evidence validator secret-pattern false-positive hardening

Implemented:

- Refined `scripts/validate_release_evidence.sh` secret leak detection.
- Removed the overly broad raw six-digit pattern that could reject normal timestamps such as `.release-evidence/20260711T...`.
- Kept context-aware leak detection for:
  - `Authorization: Bearer`;
  - `X-Operator-TOTP`;
  - `OPERATOR_ACCOUNT_TOKEN`;
  - `OPERATOR_TOTP`;
  - `totp_secret`;
  - `operator_totp`;
  - generated `op_...` operator tokens.

## Slice 22 Verification

| Check | Result |
|---|---|
| Evidence validator accepts a ready fixture whose manifest contains timestamp-like paths | Passed |
| Evidence validator rejects contextual TOTP/token leak markers | Passed |
| `make release-check` passes | Passed |
| Git diff whitespace check | Passed |

## Slice 23: operator admin lockout prevention

Implemented:

- Added service-level protection against disabling the last active `admin` operator account.
- Added service-level protection against disabling the last active MFA-enabled admin operator account.
- Added service-level protection against resetting MFA to disabled on the last active MFA-enabled admin operator account.
- Kept the protection in `opsauth.Service` so HTTP handlers and future operational tools share the same invariant.
- Updated release runbook to call out last-admin / last-MFA-admin lockout prevention.

## Slice 23 Verification

| Check | Result |
|---|---|
| Disabling the last active admin operator is rejected | Passed |
| Disabling the last active MFA admin operator is rejected | Passed |
| Disabling MFA on the last active MFA admin operator is rejected | Passed |
| Disabling one admin succeeds when another MFA admin remains | Passed |
| `make release-check` passes | Passed |
| Git diff whitespace check | Passed |

## Slice 24: MySQL transactional admin lockout guard

Implemented:

- Added a MySQL store-level transactional guard for operator account status changes.
- Added a MySQL store-level transactional guard for disabling operator MFA.
- The guard locks the target operator row and the active admin operator set with `FOR UPDATE` before mutation.
- The store now rejects writes that would remove the last active admin operator or the last active MFA-enabled admin operator, matching the service-level invariant under concurrent requests.

## Slice 24 Verification

| Check | Result |
|---|---|
| MySQL operator store compiles with transactional admin lockout guard | Passed |
| `make release-check` passes | Passed |
| Git diff whitespace check | Passed |

## Slice 25: operator lockout error contract

Implemented:

- Added a dedicated `opsauth.ErrAdminLockout` error that still wraps `ErrForbidden`.
- Operator account APIs now return stable error code `operator_admin_lockout_protection` when a request would remove the last active admin or last active MFA-enabled admin.
- MySQL transactional guard returns the same domain error as the service-level guard.
- Updated security and release runbooks with the explicit lockout-protection error code.

## Slice 25 Verification

| Check | Result |
|---|---|
| Service-level last-admin guard returns `ErrAdminLockout` and remains compatible with `ErrForbidden` | Passed |
| HTTP operator account APIs expose `operator_admin_lockout_protection` for last-admin and last-MFA-admin attempts | Passed |
| `make release-check` passes | Passed |
| Git diff whitespace check | Passed |

## Slice 26: security header release evidence validation

Implemented:

- Strengthened `scripts/validate_release_evidence.sh` to require `healthz.headers`.
- The release evidence validator now checks:
  - `X-Content-Type-Options: nosniff`;
  - `X-Frame-Options: DENY`;
  - `Referrer-Policy: no-referrer`;
  - `Cross-Origin-Opener-Policy: same-origin`;
  - `Permissions-Policy: camera=(), microphone=(), geolocation=()`.
- Updated the M8 evidence template and release runbook to call out response-header evidence.

## Slice 26 Verification

| Check | Result |
|---|---|
| Evidence validator requires security header artifact | Passed |
| Evidence validator verifies required production security headers | Passed |
| `make release-check` passes | Passed |
| Git diff whitespace check | Passed |

## Slice 27: release evidence validator semantic tests

Implemented:

- Added `scripts/test_release_evidence_validator.sh`.
- The script builds temporary release evidence bundles and verifies:
  - a complete ready bundle with required security headers is accepted;
  - a bundle missing `X-Frame-Options` is rejected with the expected security-header failure.
- Added `make test-release-evidence-validator`.
- Wired the semantic validator test into `make release-check` alongside shell syntax checks.

## Slice 27 Verification

| Check | Result |
|---|---|
| Release evidence validator semantic test accepts complete fixture | Passed |
| Release evidence validator semantic test rejects missing security header fixture | Passed |
| `make release-check` passes | Passed |
| Git diff whitespace check | Passed |

## Slice 28: release evidence validator failure coverage

Implemented:

- Expanded `scripts/test_release_evidence_validator.sh` with additional negative fixtures.
- The semantic test now verifies the evidence validator rejects:
  - bundles missing a required release-readiness check;
  - bundles containing token/TOTP leak markers such as `totp_secret`.
- Kept these checks wired into `make release-check` through the existing validator semantic test target.

## Slice 28 Verification

| Check | Result |
|---|---|
| Validator semantic test rejects missing required readiness checks | Passed |
| Validator semantic test rejects token/TOTP leak markers | Passed |
| `make release-check` passes | Passed |
| Git diff whitespace check | Passed |

## Slice 29: release evidence required status artifacts

Implemented:

- Strengthened `scripts/validate_release_evidence.sh` to require every expected HTTP status artifact.
- Required status files now include health/readiness/meta/metrics/operator evidence endpoints and audit CSV export status.
- Expanded `scripts/test_release_evidence_validator.sh` to reject bundles missing `release_readiness.status`.
- Updated the M8 evidence template to list status files as required artifacts.

## Slice 29 Verification

| Check | Result |
|---|---|
| Evidence validator requires all endpoint status artifacts | Passed |
| Validator semantic test rejects missing required status artifact | Passed |
| `make release-check` passes | Passed |
| Git diff whitespace check | Passed |

## Slice 30: release evidence payload format validation

Implemented:

- Strengthened `scripts/validate_release_evidence.sh` to reject malformed HTTP status artifacts.
- Added JSON syntax validation for all required JSON evidence payloads:
  - health/readiness/meta;
  - operator console bootstrap and release-readiness;
  - DLQ, Kafka poison, email delivery, and audit-log payloads.
- Expanded `scripts/test_release_evidence_validator.sh` to reject:
  - malformed status files such as `200 OK`;
  - non-JSON payloads such as HTML error pages captured as `.json`.

## Slice 30 Verification

| Check | Result |
|---|---|
| Validator semantic test rejects malformed HTTP status artifacts | Passed |
| Validator semantic test rejects invalid JSON payloads | Passed |
| `make release-check` passes | Passed |
| Git diff whitespace check | Passed |

## Slice 31: release evidence response content-type validation

Implemented:

- Strengthened `scripts/validate_release_evidence.sh` to require response-header artifacts for all collected endpoints.
- The validator now checks response `Content-Type` for:
  - JSON evidence endpoints;
  - Prometheus metrics;
  - audit CSV export.
- Expanded `scripts/test_release_evidence_validator.sh` so the complete fixture includes all response headers.
- Added a negative fixture that rejects an audit CSV response captured with the wrong content type.
- Updated the M8 evidence template to list response header files as required artifacts.

## Slice 31 Verification

| Check | Result |
|---|---|
| Evidence validator requires response header artifacts | Passed |
| Validator semantic test rejects wrong audit CSV content type | Passed |
| `make release-check` passes | Passed |
| Git diff whitespace check | Passed |

## Slice 32: release evidence manifest completeness validation

Implemented:

- Strengthened `scripts/validate_release_evidence.sh` to validate `manifest.txt`.
- The validator now requires manifest entries for every collected endpoint, plus `output_dir` and ISO-8601 UTC `completed_at`.
- Updated `scripts/test_release_evidence_validator.sh` to generate collector-shaped manifests in the complete fixture.
- Added a negative fixture that rejects evidence bundles missing the `release_readiness` manifest entry.

## Slice 32 Verification

| Check | Result |
|---|---|
| Evidence validator requires manifest entries for all collected endpoints | Passed |
| Validator semantic test rejects missing manifest entry | Passed |
| `make release-check` passes | Passed |
| Git diff whitespace check | Passed |

## Slice 33: release evidence manifest/status consistency

Implemented:

- Strengthened `scripts/validate_release_evidence.sh` so each manifest entry must match its corresponding `.status` artifact.
- The validator now rejects evidence bundles where manifest `status=...` disagrees with the status file, even if both values are 2xx-shaped.
- Expanded `scripts/test_release_evidence_validator.sh` with a manifest/status mismatch fixture.

## Slice 33 Verification

| Check | Result |
|---|---|
| Evidence validator compares manifest status with status artifacts | Passed |
| Validator semantic test rejects manifest/status mismatch | Passed |
| `make release-check` passes | Passed |
| Git diff whitespace check | Passed |

## Slice 34: project completion scope closure

Implemented:

- Finalized the project completion boundary after the product decision to skip M5 and M7.
- Added `docs/PROJECT_COMPLETION.md` as the canonical completion summary.
- Updated `README.md` and `docs/DEVELOPMENT_PLAN.md` so milestone status no longer points to stale M1/M4/M6 in-progress boundaries.
- Kept release evidence collection against a real internal environment as a deployment handoff item rather than a local implementation blocker.

## Slice 34 Verification

| Check | Result |
|---|---|
| Completion summary documents completed and skipped milestones | Passed |
| README and development plan point to the final completion boundary | Passed |
| `make release-check` passes | Passed |
| Git diff whitespace check | Passed |

## Completion conclusion

- With M5 financial research and M7 native Android/iOS Beta explicitly skipped, M0-M4, M6, and the M8 Web/backend platform scope are complete for local repository handoff.
- External production launch still requires target-environment evidence collection, real provider credentials, operator account bootstrap, and environment-specific approvals.
