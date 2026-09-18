# Security, Fault, and Load Test Plan

Date: 2026-07-11

This is the M8 internal-release test plan. It is not a replacement for external security review; it is the minimum self-check before inviting broader testers.

## Security checks

| Area | Check |
|---|---|
| Auth | Invalid/expired tokens are rejected; refresh token reuse is rejected. |
| Operator surface | `/v1/ops/*` rejects end-user tokens, requires operator auth, and production requires TOTP-backed operator accounts. |
| Operator roles | `viewer` can inspect, `support` can replay/moderate, and only `admin` can manage operator accounts, export audit CSV, and view release-readiness. |
| Operator bootstrap | Web administrators enroll through single-use invitations issued by the offline `admin-invite` command or an authorized admin. Production retains `OPERATOR_MFA_REQUIRED=true`; CLI Bearer + TOTP remains supported. |
| Admin Passkey sessions | UV, RP ID, origin and signatures are required; challenges are consumed once. Cookies are HttpOnly/Secure/SameSite, writes validate Origin and CSRF header and require recent verification. Disable/re-enable must not revive old sessions. See `docs/ADMIN_PASSKEY_LOGIN.md`. |
| Operator lockout prevention | Disabling the last active `admin` or last MFA-enabled admin returns `operator_admin_lockout_protection`. |
| Object access | Skill files, ledger exports, and uploaded documents cannot be downloaded by another user. |
| Prompt injection | Retrieved document content is treated as evidence, not tool/system instructions. |
| File handling | Unsupported files and EICAR test content are rejected. |
| MCP | Only operator-configured server/tool allowlists are visible; commands/env are not exposed to users. |
| Logs | Request logs include trace, method, route, status, and duration; they do not include request body, tokens, file text, or provider secrets. |
| CORS | Only configured `WEB_ORIGIN` receives browser CORS access. |
| Security headers | HTTP responses include nosniff, frame-deny, no-referrer, COOP, and restrictive Permissions-Policy headers. |
| Audit export | Admin can export CSV audit logs; support/viewer cannot. |

## Fault drills

| Fault | Expected behavior |
|---|---|
| Kill Worker | Accepted jobs remain durable; expired leases are reclaimed. |
| Stop Kafka broker (`kafka-scale` only) | Durable database jobs continue through reconciliation; broker publication resumes after recovery. |
| Model provider timeout | Circuit breaker opens; claimed generation jobs defer back to `accepted`. |
| Qdrant unavailable | Document ingestion/query fails or degrades without losing uploaded files. |
| Redis unavailable | Presence/rate-limit returns explicit unavailable errors; MySQL authority remains safe. |
| Duplicate Kafka delivery (`kafka-scale` only) | Inbox dedupe prevents repeated side effects. |
| API restart during SSE | Client can reconnect and fetch generation events by `Last-Event-ID`. |

## Load checks

Run these with local/internal data only:

1. Chat acceptance burst:
   - 100 concurrent accepted messages across 20 conversations.
   - Expected: p95 acceptance < 500 ms, no missing generation jobs.
2. Same-conversation ordering:
   - 20 messages in one conversation.
   - Expected: durable generation order stays serial.
3. Document upload burst:
   - 50 small text/PDF uploads.
   - Expected: jobs queued, no duplicate active file rows for identical hashes.
4. Durable queue backlog:
   - pause Worker, enqueue jobs, resume Worker; repeat with Kafka lag when testing `kafka-scale`.
   - Expected: database backlog drains and oldest job age returns below 15 s; optional Kafka lag also drains.
5. Model outage:
   - force provider failures until circuit opens.
   - Expected: accepted jobs remain accepted/deferred, not failed.
6. Operator audit export:
   - create 100 moderation/operator actions and export CSV.
   - Expected: export returns stable headers, quoted JSON metadata, and no secrets.
7. Release-readiness:
   - flip one production-like readiness prerequisite in a staging env.
   - Expected: `/v1/ops/release-readiness` changes from `ready` to `blocked`.

## Exit criteria

- No cross-user data access.
- No accepted user request lost.
- No duplicate ledger/reminder/Skill side effect from duplicate delivery.
- L0-L3 policy transitions match M6 status tests.
- Operator MFA/role/audit controls match M8 status tests.
- `/v1/ops/release-readiness` is `ready` in the target internal environment before inviting testers.
- Every failed load/fault run has a ticket or documented product exception.
