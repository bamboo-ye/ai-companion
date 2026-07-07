# M1 Account, Character, and Reliable Chat Status

Date: 2026-07-01

## Completed in slice 1

- Registration and login with PBKDF2-SHA256 password hashing.
- Short-lived signed access tokens and hashed, rotating refresh tokens.
- Current-session logout and all-session logout.
- Device identity, platform, timezone, and last-seen persistence.
- User-scoped character CRUD with soft deletion.
- Deterministic persona compilation with immutable version history and non-overridable system safety rules.
- MySQL 8.4 schema for accounts, devices, sessions, characters, persona versions, conversations, ordered messages, generation jobs, and model usage.
- Persistent MySQL stores for the account and character paths, with explicit in-memory fallback only when `MYSQL_DSN` is absent.
- Web registration and character-creation experience using the M1 API.
- OpenAPI 3.1 contracts for the delivered identity and character endpoints.

## Verification

| Check | Result |
|---|---|
| Go format, unit tests, and `go vet` | Passed |
| Refresh-token rotation and reuse rejection | Passed |
| Two distinct character inputs compile to distinct personas | Passed |
| Persona update creates immutable version 2 | Passed |
| MySQL 8.4 M0 + M1 migrations | Passed, 13 tables |
| Persistent register/login/character flow across API restart | Passed |
| Ordered multi-bubble messages and SSE replay across API restart | Passed |
| Cancellation prevents post-cancel assistant bubbles | Passed |
| Web ESLint and TypeScript | Passed |
| Next.js production build | Passed |
| OpenAPI YAML parse | Passed |

## Completed in slice 2

- User-scoped conversation creation and cursor-based message history.
- Transactional per-conversation sequence allocation; assistant bubbles share one sequence and use stable bubble order.
- Persist-before-execute generation jobs and a replaceable model-provider interface.
- Deterministic persona-aware development provider for local and CI verification.
- Semantic long-response splitting into two to five bubbles.
- Persisted SSE event log with `Last-Event-ID` / `after_event_id` replay after reconnect or API restart.
- Generation cancellation that prevents post-cancel assistant bubbles, terminal timeout/failure states, and retry attempts.
- Model token, latency, and estimated-cost persistence boundary.
- Web chat history, streamed bubble rendering, stop generation, and retry controls.

Slice 2 was verified against MySQL 8.4: a response produced five ordered bubbles, and six messages plus eight generation events were recovered after an API restart.

## Completed in slice 3

- Redis-backed session presence with sliding TTL and authenticated heartbeat/status endpoints.
- Redis fixed-window distributed chat rate limiting with `429`, `Retry-After`, and remaining-budget headers.
- Input/output content-safety policy boundary with explicit prohibited-content tests that preserve ordinary emotional-support conversations.
- Configurable OpenAI-compatible production provider with bounded HTTP client, authentication, persona system prompt, usage parsing, and provider tests.
- Strict production configuration validation without selecting a provider, region, or model before those product decisions are approved.
- Startup recovery that converts interrupted accepted/running jobs to persisted `failed/process_interrupted` state so users can retry.

## Slice 3 verification

| Check | Result |
|---|---|
| Redis 8.8 presence TTL | Passed |
| Distributed chat limit (`202` then `429`) | Passed |
| MySQL interrupted-job recovery and failure event | Passed |
| OpenAI-compatible request/auth/usage contract | Passed with local provider fixture |
| Safety rejection and emotional-support allow cases | Passed |
| Browser registration, role creation, SSE chat, and history recovery | Passed |
| Migration runner idempotency | Passed |

M1 engineering scope is complete. Activating a non-development model still requires the separate provider, region, privacy, and cost decision recorded in ADR 0003; no vendor credentials are committed.
