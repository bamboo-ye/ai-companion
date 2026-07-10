# M8 Backend Platform Status

Date: 2026-07-08

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

## Next slices

- Admin console UI contract and operational audit export.
