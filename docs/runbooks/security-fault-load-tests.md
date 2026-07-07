# Security, Fault, and Load Test Plan

Date: 2026-07-08

This is the M6 internal-release test plan. It is not a replacement for external security review; it is the minimum self-check before inviting broader testers.

## Security checks

| Area | Check |
|---|---|
| Auth | Invalid/expired tokens are rejected; refresh token reuse is rejected. |
| Operator surface | `/v1/ops/*` rejects end-user tokens and requires `OPERATOR_TOKEN`. |
| Object access | Skill files, ledger exports, and uploaded documents cannot be downloaded by another user. |
| Prompt injection | Retrieved document content is treated as evidence, not tool/system instructions. |
| File handling | Unsupported files and EICAR test content are rejected. |
| MCP | Only operator-configured server/tool allowlists are visible; commands/env are not exposed to users. |
| Logs | Request logs include trace, method, route, status, and duration; they do not include request body, tokens, file text, or provider secrets. |
| CORS | Only configured `WEB_ORIGIN` receives browser CORS access. |

## Fault drills

| Fault | Expected behavior |
|---|---|
| Kill Worker | Accepted jobs remain durable; expired leases are reclaimed. |
| Stop Kafka broker | Outbox events remain pending/publishing and resume after broker recovery. |
| Model provider timeout | Circuit breaker opens; claimed generation jobs defer back to `accepted`. |
| Qdrant unavailable | Document ingestion/query fails or degrades without losing uploaded files. |
| Redis unavailable | Presence/rate-limit returns explicit unavailable errors; MySQL authority remains safe. |
| Duplicate Kafka delivery | Inbox dedupe prevents repeated side effects. |
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
4. Kafka backlog:
   - pause Worker, enqueue jobs, resume Worker.
   - Expected: lag drains; oldest job age returns below 15 s.
5. Model outage:
   - force provider failures until circuit opens.
   - Expected: accepted jobs remain accepted/deferred, not failed.

## Exit criteria

- No cross-user data access.
- No accepted user request lost.
- No duplicate ledger/reminder/Skill side effect from duplicate delivery.
- L0-L3 policy transitions match M6 status tests.
- Every failed load/fault run has a ticket or documented product exception.
