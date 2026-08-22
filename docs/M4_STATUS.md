# M4 Skill Platform Status

Date: 2026-07-04

## Slice 1: bounded Skill Runtime and office starter skills

Implemented in the working tree:

- A versioned Skill registry with JSON input/output contracts, risk level, confirmation policy, timeout, step budget, and a typed Tool handler.
- Explicit finite states and persisted checkpoints: `receive → validate → plan → confirm/execute → deliver`, with terminal `failed` and `cancelled` states.
- Optimistic revision checks and user-scoped create/confirm/cancel/retry idempotency keys.
- Queryable task steps, attempts, failure codes, Tool execution records, audit records, and transactional succeeded/failed/cancelled Skill terminal outbox events. Agent-originated async Skills use these events for immediate resume while preserving scheduled reconciliation fallback.
- Generated-file metadata and authenticated download; files use new UUID storage keys and never overwrite source files.
- Three built-in starter Skills:
  - `office.translate`: deterministic side-effect-free translation preview.
  - `office.email_draft`: draft-only email output with no provider connection or send operation.
  - `office.markdown_document`: medium-risk file creation that cannot execute before explicit confirmation.
- REST/OpenAPI 0.7 endpoints for manifests, run creation/list/detail, confirmation, cancellation, retry, and file download.
- Web work-partner panel with Skill catalog, translation/email/document forms, confirmation cards, progress steps, errors, retry, and downloads.
- MySQL migration `000010_skill_runtime` for runs, steps, Tool executions, and generated files.

## Verification completed

| Check | Result |
|---|---|
| A newly registered no-side-effect Skill runs without chat-core changes | Passed |
| Duplicate create key returns the original run | Passed |
| Medium-risk file Skill has zero files before confirmation | Passed |
| Repeated confirmation creates exactly one file | Passed |
| Different confirmation key is rejected | Passed |
| Cancelled run can be retried and returns to confirmation | Passed |
| Failed Tool exposes step/error and retries deterministically | Passed |
| Cross-user run and file access returns not found | Passed |
| Source-overwrite flag remains false and file bytes are downloadable | Passed |
| HTTP handler and runtime unit tests | Passed |
| Go full test suite, vet, and formatting | Passed |
| Web ESLint, TypeScript, and production build | Passed |
| OpenAPI YAML parse | Passed |
| Fresh MySQL migration plus idempotent second migration (`10` versions / `35` tables) | Passed |
| Persistent REST acceptance for create/confirm/cancel/retry/download and cross-user isolation | Passed |
| Browser E2E for Skill catalog, translation, draft-only email, confirmation gate, steps, and download availability | Passed |
| Browser page and console error check | Passed (0 errors) |
| Punctuation-aware deterministic phrase translation regression | Passed |

Persistent acceptance produced the expected bounded side effects: 4 runs, 24 steps, 4 historical action keys, 4 Tool executions, 2 generated files, 7 Skill-run audit records, and 4 succeeded outbox events. The browser test also caught and verified the fix for a Chinese sentence-final punctuation phrase-match edge case.

## Slice 1 completion

The bounded Skill Runtime and office starter slice is complete. Temporary acceptance data and generated files are not part of the deliverable.

## Slice 2: Office files and tabular profiling

Implemented:

- An isolated, timeout-bounded Python Office process with a strict JSON/base64 contract and bounded stdout/stderr.
- `office.docx_edit`: accepts a small macro-free DOCX, shows a confirmation card, and appends paragraphs only to a newly generated copy with a change summary.
- `office.pptx_outline`: produces a side-effect-free per-slide outline from explicit title, audience, slide count, style, and brief.
- `office.pptx_generate`: creates a new `.pptx` directly once the required parameters are complete; generated files never overwrite an existing source.
- `office.tabular_profile`: profiles UTF-8 CSV and macro-free XLSX headers, inferred types, missing values, unique values, duplicate rows, and numeric min/max/mean, with a versioned downloadable JSON report.
- Web file pickers and Office forms, redacted confirmation previews for inline file data, task history, and download actions.
- API container support for the pinned Office worker dependencies and OpenAPI 0.8 documentation.
- File guards: 700KB compressed input, 25MB archive expansion, `.docx/.csv/.xlsx` allowlist, macro rejection, 100-column limit, and a 10,000-row analysis ceiling.

### Slice 2 verification

| Check | Result |
|---|---|
| Python DOCX copy preserves source bytes and appends two paragraphs | Passed |
| Generated PPTX is a valid OOXML package with requested slide count | Passed |
| PPTX outline returns no file and matches generated outline | Passed |
| CSV and XLSX profiles report deterministic quality and numeric statistics | Passed |
| Go-to-Python worker contract, timeout, base64 file decode, and Skill output validation | Passed |
| DOCX/PPTX confirmation produces zero files before confirmation | Passed |
| Real API generation and authenticated download for DOCX, PPTX, and XLSX report | Passed |
| Web catalog exposes seven Skills and renders audience/page/style confirmation parameters | Passed |
| Empty generated-file collections remain JSON arrays after persistence | Passed |
| Browser regression for null file collections | Caught, fixed, and post-fix flow passed |
| Exhaustive attachments use ordered rounds, cleaning manifests, continuation cursors, and deduplicated source coverage | Passed |
| Generated artifacts pass the task-contract gate before the Harness can finalize | Passed |
| PPTX table rows preserve requested fields and source locators without slide overflow | Passed |

Current slice limits are intentional: DOCX editing appends paragraphs rather than performing tracked arbitrary edits; PPTX uses the built-in deterministic visual theme rather than user templates; XLSX analysis reads the active sheet and does not execute formulas or external links. Ordered fallback extraction now supports files up to the 20MB product upload ceiling; OCR-heavy/scanned documents and files beyond that ceiling remain production-hardening work.

## Slice 3: intent routing and MCP allowlists

Implemented:

- A chat-independent deterministic intent router covering `casual_chat`, `ledger`, `reminder`, `document_qa`, `office`, `finance`, `image`, and `unknown`.
- Every route returns confidence, missing slots, risk level, extracted slots, optional Skill suggestion, decision layer, reason, and a traceable router version.
- Conservative fallback behavior: ambiguous requests return `fallback_required`; the route endpoint never starts a Skill or invokes an MCP tool.
- A 30-case checked-in M4 intent fixture with macro-F1 quality-gate coverage.
- An MCP 2025-11-25 stdio adapter implementing `initialize`, `notifications/initialized`, paginated `tools/list`, and `tools/call` with JSON-RPC response correlation.
- Operator-owned server configuration with absolute executable paths, explicit argument arrays, explicit environment entries, timeouts, and server/tool double allowlists.
- Remote descriptions and annotations are treated as untrusted; remote input/output schemas are validated, stdout/stderr are bounded, unsolicited server requests are denied, and API secrets are not inherited by default.
- Authenticated `POST /v1/intent/route` and read-only `GET /v1/mcp/servers`; no generic public MCP call endpoint is exposed.
- A Web Skill advisor and visible safe-default MCP allowlist status.

### Slice 3 verification

| Check | Result |
|---|---|
| 30-case deterministic intent fixture macro-F1 | Passed (>= 0.95) |
| Route output includes slots, risk, suggestion, layer, and version | Passed |
| Suggestions never auto-execute a Skill or MCP tool | Passed |
| Relative MCP commands and malformed/duplicate tool names are rejected | Passed |
| Disallowed server/tool is rejected before process spawn | Passed |
| MCP initialize/initialized/list/call lifecycle fixture | Passed |
| Protocol version mismatch, unadvertised tool, and invalid arguments are rejected | Passed |
| MCP catalog omits command, arguments, and environment values | Passed |
| Web advisor and empty MCP safe-default state | Passed |

Current boundaries are deliberate: the rule layer hands ambiguous inputs to a future small-model classifier rather than guessing; stdio MCP configuration is operator-owned and disabled by default; MCP calls remain internal until a versioned Skill explicitly binds an allowlisted tool and applies the normal confirmation policy.

## Slice 4: user controls and restart recovery

Implemented:

- User-scoped Skill enable/disable settings with enabled-by-default behavior and MySQL persistence in migration `000011_skill_user_settings`.
- Authenticated `PUT /v1/skills/{skill_name}/settings`; catalog reads return the effective setting for the caller.
- Disabled Skills reject new runs, confirmations, and retries without changing other users or the global operator allowlist.
- Every persistent setting change creates an audit record.
- The Web Skill catalog exposes explicit enable/disable controls and disables the matching execution buttons.
- API startup recovery moves any synchronous Skill run left in `running` by process interruption to `failed` with `execution_interrupted`; the existing idempotent retry flow can then resume it safely.

### Slice 4 verification

| Check | Result |
|---|---|
| One user's disabled Skill does not affect another user | Passed |
| Disabled Skill cannot start; re-enable restores execution | Passed |
| Missing/non-boolean setting payload is rejected | Passed |
| Interrupted in-memory run becomes queryable failed work and can retry | Passed |
| Full Go test suite | Passed |
| Web ESLint, TypeScript, and production build | Passed |
| OpenAPI 1.0 YAML parse | Passed |
| Fresh MySQL migration plus idempotent second migration (`11` versions) | Passed |

## Slice 5: durable queue leases and Worker takeover

Implemented:

- Worker execution mode for PPTX outline/generation, DOCX copy editing, and CSV/XLSX profiling; API creation or confirmation returns a durable `queued` run without invoking Python in the request process.
- MySQL migration `000012_skill_worker_leases` adds queue state, execution mode, availability, Worker ownership, lease expiry, and a claim index.
- Atomic `FOR UPDATE SKIP LOCKED` claims support multiple Workers without double assignment.
- Workers renew leases while tools run. Expired leases can be taken over after a crash or stalled process.
- Every claim advances the optimistic revision. A stale Worker cannot commit output after takeover; generated files from rejected commits are removed.
- Cancellation and retry advance the same revision fence and release current Worker ownership.
- API startup recovery only fails interrupted inline work; leased Worker work remains available for takeover.
- The existing `cmd/worker` now runs document ingestion and Skill execution concurrently, with independently configurable poll, lease, and renewal durations.
- Web task history displays `queued` separately from `running`, and confirmation notices no longer claim that background work finished synchronously.

### Slice 5 verification

| Check | Result |
|---|---|
| Worker-mode Skill remains queued until claimed | Passed |
| Lease renewal prevents premature takeover | Passed |
| Expired lease is claimed by a second Worker | Passed |
| Revision fence rejects the old Worker's completion | Passed |
| API returns HTTP 202 and `execution_mode=worker` | Passed |
| Real MySQL simulated crash → expired lease → new Worker completion | Passed |
| Completed run releases Worker and lease fields | Passed |
| Fresh and repeated migration (`12` versions) | Passed |

M4 is complete. Future production work includes queue-depth metrics/alerts, orphan-file garbage collection, dead-letter operations, and horizontal-load testing; these do not block the durable execution contract delivered here.
