# Project Completion Status

Implementation review: 2026-09-25, against `edf2802` (bounded parallel agents and evidence arbitration, following quota management and public-release security hardening).

Start with the [beginner guide](GETTING_STARTED.md) for setup and first-use steps.

## Completion decision

The project is complete for the agreed Web/backend platform scope after explicitly skipping:

- M5 financial research MVP;
- M7 native Android/iOS full Beta client development.

This completion status covers the repository implementation, local quality gates, API contracts, migrations, runbooks, and internal-release evidence tooling. It does not claim external production launch, app-store readiness, licensed financial data coverage, or provider-specific OAuth/payment approval.

The milestone decisions below are historical scope decisions. The current review
checks code, UI entry points, configuration and documentation; it does not repeat
every milestone acceptance exercise or certify the deployed environment.

## Current implementation and user-facing entry points

| Capability | Implementation evidence | Current entry point / boundary |
|---|---|---|
| Accounts, profile and conversations | [`internal/identity`](../internal/identity/), [`companion-client.tsx`](../web/app/companion-client.tsx), [`profile-panel.tsx`](../web/app/profile-panel.tsx), [`chat-panel.tsx`](../web/app/chat-panel.tsx) | Registration/login, three character modules, password change, streaming/retry/cancel; real model behavior requires a configured provider |
| Memory and personal Wiki | [`internal/memory`](../internal/memory/), [`internal/document`](../internal/document/), [`wiki-panel.tsx`](../web/app/wiki-panel.tsx) | Memory management, document upload/query, Wiki search/edit/export/feedback; default upload limit 20 MiB, parsing/indexing needs the Worker |
| Onboarding and built-in product knowledge | [`onboarding-content.ts`](../web/app/onboarding-content.ts), [`internal/productknowledge`](../internal/productknowledge/), [`generator`](../scripts/build-product-knowledge.mjs) | Searchable user/admin guides (22/21 chapters) and product-knowledge reader; embedded read-only sources, no personal document quota or remote embedding prerequisite |
| Life and office workflows | [`life-panel.tsx`](../web/app/life-panel.tsx), [`work-panel.tsx`](../web/app/work-panel.tsx), [`task-history-panel.tsx`](../web/app/task-history-panel.tsx), [`internal/skill`](../internal/skill/) | Ledger, reminders, plans, generated files and task confirmation; DOCX/table forms have a separate 700 KiB limit |
| Durable Agent execution | [`cmd/agent-worker`](../cmd/agent-worker/), [`internal/agent`](../internal/agent/), [`Python runtime`](../workers/python/src/ai_companion_worker/agent_runtime.py) | All three default chat modules use Agent Worker; PostgreSQL checkpoints, gateway authorization, bounded concurrency and recovery; Kafka optional |
| Parallel research and content review | [`parallel tasks`](../workers/python/src/ai_companion_worker/parallel_tasks.py), [`arbitration`](../workers/python/src/ai_companion_worker/arbitration.py), [`parallel guide`](PARALLEL_AGENTS.md), [`arbitration guide`](ARBITRATION.md) | Work chat supports conditional multi-attachment reads, validated read-only research and requested source/expression review. At most one review revision; unresolved research disputes stop file generation; rendered file layout is outside the review |
| Administrator identity | [`internal/adminpasskey`](../internal/adminpasskey/), [`admin-invite CLI`](../cmd/admin-invite/main.go), [`login guide`](ADMIN_PASSKEY_LOGIN.md) | Independent `/admin` account, invited WebAuthn passkeys, HttpOnly sessions and recent verification for writes; ordinary registration does not grant admin access |
| Quotas and accounting | [`internal/billing`](../internal/billing/), [`quota API`](../internal/httpserver/billing_quota.go), [`quota editor`](../web/app/ops/quota-policy-editor.tsx), [`quota guide`](ADMIN_QUOTAS.md) | Admin global/per-user overrides, inheritance, effective source and audited usage correction; requires PostgreSQL migration 000043 or MySQL 000032 before deployment |
| Operations and Agent Studio | [`web/app/ops`](../web/app/ops/), [`internal/controlplane`](../internal/controlplane/), [`internal/performance`](../internal/performance/) | Runs/logs/incidents, budgets, versioned configurations, graph/Prompt editing, evaluation and governed release; optional Loki/Langfuse |
| Team, mail, subscription and minor-mode services | [`internal/team`](../internal/team/), [`internal/email`](../internal/email/), [`internal/billing`](../internal/billing/), [`internal/safety`](../internal/safety/), [`HTTP routes`](../internal/httpserver/server.go) | Backend/API capabilities; the current end-user Web navigation does not expose a dedicated page for every service. Mail drafts do not send mail; live delivery needs SMTP configuration; payment-provider integration is not claimed |

Quota overrides are checked against accumulated usage at request start. They are
not concurrent quota reservations or a per-token spending stop for already
running requests. Raising a quota does not reset usage or restart failed tasks.
The separate Agent run budget still controls each run's model/token/cost ceiling.

The current graph is `ai-companion-supervisor@3.71.0`. Skill execution uses bounded execution/parse/render pools; Go/Python requests share provider permits on one host. Wiki synthesis processes all chunks in bounded parallel batches, caches successful shards and reports incomplete semantic coverage while retaining original pages. Wiki and memory arbitration annotate source relationships without removing original evidence or silently replacing memories. The Web Wiki reader shows body notes and conflict notices; the memory panel does not yet expose arbitration details.

Upgrades require PostgreSQL `000044_wiki_shards` and `000045_memory_arbitration` (MySQL `000033` and `000034`) in addition to earlier migrations. Complete old-graph Runs or explicitly restart them before switching graph versions. Public-file protections and dependency changes are recorded in the dated [security review](SECURITY_REVIEW.md); that historical scan is not a fresh vulnerability assessment of this revision.

## Completed milestones

| Milestone | Status | Evidence |
|---|---|---|
| M0 engineering foundation | Complete | `docs/M0_STATUS.md` |
| M1 account, persona, reliable chat | Complete | `docs/M1_STATUS.md` |
| M2 context, memory, Wiki | Complete | `docs/M2_STATUS.md` |
| M3 life assistant | Complete | `docs/M3_STATUS.md` |
| M4 Skill platform and office tools | Complete | `docs/M4_STATUS.md` |
| M5 financial research | Skipped by product decision | `docs/DEVELOPMENT_PLAN.md` |
| M6 reliability and internal release | Complete | `docs/M6_STATUS.md` |
| M7 native Android/iOS Beta | Skipped by product decision | `docs/DEVELOPMENT_PLAN.md` |
| M8 Web/backend 1.0 platform | Complete for backend/internal-release scope | `docs/M8_STATUS.md` |

## Completed platform capabilities

- Account/session/device identity, persona versions, persistent conversation, streaming generation, retry/cancel/recovery.
- Long-term memory, bounded context, document intake, page-level PDF/text parsing, structural chunking, Qdrant retrieval, citations, insufficient-evidence behavior, deletion, and a 100-case M2 quality gate.
- Ledger candidates/confirmation, ledger CRUD, Excel export, plans, reminders, application notification records, and system-reminder adapter boundaries.
- Versioned Skill runtime, deterministic intent routing, MCP tool allowlists, office document/spreadsheet/presentation skills, durable long-task queue leases, Worker takeover, and generated-file access control.
- Database-first asynchronous dispatch with leases and reconciliation, plus an optional Kafka horizontal-scale adapter with outbox/inbox recovery, poison-message handling, DLQ/replay/compensation operations, model circuit breaker, and L0-L3 reliability policy.
- Team workspaces, invitations, resource sharing for documents/generated files/ledger exports, and privacy isolation for non-shared personal data.
- Email delivery queue, SMTP/no-op sender adapters, Worker delivery processing, failed-delivery replay, and audit trail.
- Billing plans, entitlement summary, service-side quota guards, global/per-user overrides with revision conflict detection, and effective-limit source reporting.
- Minor mode and risky Skill capability gating.
- Operator admin APIs, user moderation, operator MFA/RBAC, admin account bootstrap/rotation, lockout prevention, audit CSV export, release-readiness API, production config hardening, and security headers.
- Release evidence collection and validation with semantic tests covering required files, status artifacts, manifest completeness, response headers/content types, JSON payload syntax, readiness details, metrics, CSV header, and secret-leak patterns.
- A user-login-independent `/admin` management console using invited administrator accounts, WebAuthn passkeys and HttpOnly cookie sessions, with Operations, persistent redacted logs, alert/incident workflows, email and console notifications, and evidence export. Bearer + TOTP remains the CLI/automation authentication path.
- Searchable user/admin onboarding, account profile/password change, editable personal Wiki pages, and embedded product knowledge with source hashing, citations and a local retrieval budget.
- Visual Agent Studio with a governed graph canvas, node-level isolated debugging, independent immutable Prompt versions, evaluation gates, stable canary rollout, publish, and rollback.
- Immutable billing/model/Agent configuration control plane, unified Agent/model-cost usage accounting, append-only audited corrections, and durable per-instance convergence reporting.
- Cross-Kafka Trace propagation through transactional Outbox, event envelopes/headers, consumer contexts, asynchronous Agent dispatch, persistent logs, and DLQ inspection.
- Canonical redacted JSON logs collected by Grafana Alloy into Loki, with low-cardinality labels, Run/Trace/Langfuse Agent Trace correlation, and an explicit PostgreSQL query fallback in the management console.
- Cost/quality analytics, anomaly detection, budget forecasting, human recommendation decisions, before/after effect reviews, manual rollback linkage, post-rollback verification, and periodic JSON/Markdown review reports.

## Final local verification gate

Use:

```sh
make release-check
```

The gate currently runs:

- Go full test suite;
- Python Worker pytest tests, including function-based parallel/arbitration cases and existing unittest cases;
- Agent replay, runtime concurrency, performance and retry evaluation gates;
- offline observability release comparison and observability configuration/self-tests;
- OpenAPI YAML parse;
- Docker Compose config validation;
- release evidence script syntax checks;
- release evidence validator semantic tests;
- Git diff whitespace check.

This Make target does not include Web lint/type checking, unit tests or a
production build. Run `pnpm check`, `pnpm test` and `pnpm build` in `web/`
separately. Rebuild embedded documentation with
`node scripts/build-product-knowledge.mjs`, then run its `--check` mode and
`go test ./internal/productknowledge` after any source-document changes.
Database-specific integration tests skip when their dedicated test DSNs are
absent; passing the ordinary test command is not proof that those tests ran.

### Verification performed for the 2026-09-25 review

| Check | Result |
|---|---|
| Go repository tests (`GOCACHE=/tmp/ai-companion-go-cache go test ./...`) | All packages passed, including embedded-knowledge consistency and retrieval; local HTTP test ports were permitted |
| Python Worker (`PYTHONPATH=workers/python/src workers/python/.venv/bin/python -m pytest workers/python/tests -q`) | 561 tests passed, 2 skipped, 142 subtests passed; SWIG deprecation warnings remain |
| Web `pnpm check`, `pnpm test`, `pnpm build` | Lint/type checking passed, 13 tests passed, Next.js 16.3.5 production build succeeded |
| Web guide content and search | 22 user chapters and 21 admin chapters; new multi-source, review and admin troubleshooting topics resolve in guide search; existing navigation/progress tests passed |
| Embedded product knowledge | 138 pages from 10 allowlisted sources, now including parallel execution and arbitration guides; generator `--check` and Go tests passed |
| Documentation links and whitespace | 277 local targets across 8 updated Markdown documents resolved; `git diff --check` passed |

`POSTGRES_TEST_DSN`, `MYSQL_TEST_DSN` and `LANGGRAPH_POSTGRES_TEST_DSN`
were not configured. Database-specific Go integration tests therefore were not
exercised; the two Python skips are PostgreSQL checkpoint reconnect tests. This
review did not run the complete `make release-check`, browser end-to-end flows,
real-provider model/mail canaries, a new dependency vulnerability scan, native
client builds, production migrations or a deployment. The current code and
local checks do not establish which version is running on a server.

## Explicitly out of scope for this completion pass

- M5 financial research, market-data connectors, paid research/reporting pipelines, and any unlicensed financial-data workflow.
- M7 full native Android/iOS productization, store submission assets, TestFlight/Play internal tracks, native app E2E, APNs/FCM provider setup, and mobile crash/performance release metrics.
- External production launch and live traffic rollout.
- Provider-specific approvals and credentials for model vendors, SMTP/OAuth mailbox providers, payment processors, app stores, or regional compliance programs.
- Production operation, retention approval, and SLO ownership for the external Langfuse project or a separately self-hosted instance. The configured local integration remains fail-open, and Agent evaluation, Prompt governance, redacted logs, Trace correlation, and release gates do not depend on remote availability. Loki is implemented as an optional local/remote telemetry backend; Tempo remains a future integration.
- Real internal environment evidence bundle, unless collected separately with `make release-evidence` and validated with `make validate-release-evidence`.

## Deployment handoff

Before inviting external testers or production traffic:

1. Configure real secrets in a secret manager, not in `.env`.
2. Provision an active administrator through the [invitation CLI and Passkey flow](ADMIN_PASSKEY_LOGIN.md), bind a backup credential, and keep production MFA enabled. Prepare separate Bearer + TOTP credentials for authorized evidence-collection automation.
3. Configure non-development model provider and approved region/retention policy.
4. Run migrations against the target database.
5. Initialize LangGraph checkpoints and start Web, API, Worker, Agent Worker, PostgreSQL, Redis, Qdrant, and the configured file storage. Ensure API/Worker file paths share the required persistent volume. Add Kafka only when the measured horizontal-scale criteria in ADR 0005 are met.
6. Collect evidence:

   ```sh
   API_BASE_URL="$API_BASE_URL" \
   OPERATOR_ACCOUNT_TOKEN="$OPERATOR_ACCOUNT_TOKEN" \
   OPERATOR_TOTP="$OPERATOR_TOTP" \
   make release-evidence
   ```

7. Validate evidence:

   ```sh
   RELEASE_EVIDENCE_DIR=.release-evidence/<timestamp> make validate-release-evidence
   ```

8. Verify model concurrency settings and shared permit storage; when upgrading the Agent graph, handle unfinished old-version Runs before the switch.
9. Archive the non-sensitive summary in the release record.
