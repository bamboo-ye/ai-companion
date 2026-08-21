# M6 Reliability and Internal Release Status

Date: 2026-07-08

M5 financial research is explicitly skipped by product decision. No financial data pipeline, report generator, or unlicensed market-data connector is included in M6.

## Slice 1: reliability control plane

Implemented:

- A concurrency-safe L0-L3 degradation controller driven by runnable durable-job count, oldest runnable-job age, recent generation failure ratio, and five-minute model latency p95.
- Consecutive-sample entry hysteresis, candidate reset when signals flap, minimum dwell time, and one-level-at-a-time recovery from L3 to L0.
- Explicit policies for full RAG, memory extraction, model class, durable long-task queuing, and L3 accept-only behavior. This slice exposes policy state; later slices apply every policy to the relevant execution path.
- Real MySQL sampling for expired/queued Skill and document leases, generation failures/timeouts, and model usage latency.
- `X-Trace-ID` validation/generation and propagation on every HTTP response; request logs use trace ID, bounded route patterns, status, and duration without logging request bodies.
- Prometheus text metrics for route/status counts, cumulative duration, degradation level, queue lag/age, model error ratio, and p95 latency.
- Public `GET /metrics` for monitoring-network scraping and authenticated `GET /v1/reliability` for the current signal, hysteresis, and policy snapshot.
- `RELIABILITY_POLL_INTERVAL` configuration with a ten-second default and bounded five-second sample timeout.

## Slice 1 verification

| Check | Result |
|---|---|
| Consecutive bad samples required before entering degradation | Passed |
| Flapping signal resets the pending transition | Passed |
| Minimum dwell blocks premature recovery | Passed |
| L2 recovers through L1 rather than jumping to L0 | Passed |
| L3 policy preserves accepted work and selects local-only execution | Passed |
| Metrics use ServeMux patterns instead of user/resource paths | Passed |
| Existing SSE endpoint retains `http.Flusher` support | Passed |
| Client trace is validated and propagated; missing trace is generated | Passed |
| Reliability snapshot requires authentication | Passed |
| Go full test suite | Passed |
| OpenAPI 1.2 YAML parse | Passed |
| Real MySQL queued-job age triggers L3 after hysteresis | Passed |
| Prometheus output reflects real L3 and queue lag | Passed |

## Slice 2: Kafka Outbox delivery foundation

Implemented:

- Accepted ADR 0004: Kafka is the only production asynchronous transport; MySQL remains transactional state, Outbox/Inbox, lease, retry, DLQ, and recovery storage.
- Pinned `franz-go v1.21.0`; Producer uses `acks=all`, the client's default idempotent production, aggregate-key partitioning, Snappy compression, and synchronous broker acknowledgement.
- Migration `000013_kafka_outbox_relay` adds pending/publishing/published/dead-letter states, availability, Relay leases, attempts, error detail, and Kafka topic/partition/offset metadata.
- Transactional Outbox claims use `FOR UPDATE SKIP LOCKED`; expired Relay leases are reclaimable.
- Exponential retry is capped at five minutes; exhausted events move to DLQ, with a state-checked replay primitive that resets attempts.
- Inbox records use the existing `(consumer_name,event_id)` primary key and report duplicates without repeating work.
- Skill queue transitions now emit `skill.execute.v1` in the same MySQL transaction as the queued run.
- Kafka topics are a fixed application allowlist. Compose health-checks Kafka and a one-shot `kafka-init` service creates ten three-partition development topics.
- Versioned Kafka envelope v2 includes event/aggregate identity, version, timestamp, and payload; event ID/type/version are repeated as Kafka headers.
- The reliability sampler now counts unpublished/expired Outbox work in queue lag and age.
- Document and Skill command consumers use one-record sequential processing, disabled auto-commit, process-then-Inbox ordering, and manual offset commit.
- Kafka payload IDs drive precise document-job and Skill-run claims rather than an unrelated “next job” scan.
- Duplicate events are committed after Inbox detection without invoking the processor again; processing failures create neither Inbox rows nor committed offsets.
- Existing database polling runs every 30 seconds only as stranded-work reconciliation when Kafka is enabled.
- Empty Kafka fetches during group coordination are treated as normal and continue polling; each durable runner reports its own source on terminal errors, and expected cancellation during shutdown is silent.

### Slice 2 verification

| Check | Result |
|---|---|
| Successful Relay stores broker topic, partition, and offset | Passed |
| Duplicate Inbox event is rejected by consumer/event identity | Passed |
| Publish failure uses exponential availability backoff | Passed |
| Attempt limit moves event to DLQ; replay resets it to pending | Passed |
| Non-allowlisted topic is rejected before Kafka access | Passed |
| Compose Kafka health check and fixed-topic initializer | Passed |
| Real MySQL Outbox → Kafka 4.3 publish and envelope readback | Passed |
| Fresh migration count | Passed (`13`) |
| Duplicate consumer event invokes processor once and commits both deliveries | Passed |
| Processing failure does not write Inbox or commit offset | Passed |
| Specific-ID claims leave unrelated queued jobs untouched | Covered by store/runner tests |
| API → transactional Outbox → Relay → Kafka 4.3 consumer group → Worker → Inbox | Passed with reconciliation interval set to `1h` |
| Re-delivered event reaches consumer-group lag `0` without repeating the tool side effect | Passed (`Inbox 1→1`, tool executions `1→1`) |
| Broker interruption preserves accepted work and resumes automatically | Passed (`queued/publishing` → `succeeded/published`) |
| Worker remains alive across broker interruption and normal shutdown emits no false Relay failure | Passed |

## Slice 3: algorithm-service asynchronous migration

Implemented:

- Chat generation is accepted transactionally with `chat.command.v1`, claimed by exact job ID, protected by a durable lease/reconciler boundary, and recoverable after Worker loss. Queued cancellation terminates immediately; running cancellation cannot be overwritten by a late completion.
- Superseded on 2026-07-18: automatic pattern-based memory extraction no longer emits `memory.extract.v1`. Explicit long-term memory is selected by native model tool calling inside `chat.command.v1`, validated server-side, and written with source identifiers; the legacy topic is drained for compatibility only.
- Ledger XLSX export is now a `202 Accepted` job API with idempotency key, polling, durable artifact storage, `ledger.export.v1`, lease recovery, and a separate download endpoint.
- Due in-app notification deliveries are transactionally scheduled to `notification.deliver.v1` and delivered idempotently by a dedicated consumer group. System-reminder synchronization remains a native client responsibility.
- Document deletion records a cleanup job and `document.cleanup.v1` before asynchronously removing Qdrant points and the stored blob; retry state survives Worker restarts.
- Each workload uses an isolated Kafka consumer group to prevent document, model, export, and notification work from blocking one another. MySQL reconcilers remain the recovery path for lost commands and expired leases.
- Reliability queue lag/age now includes chat, ledger export, document cleanup, due notifications, document ingestion, Skill execution, and Outbox Relay work.
- Five new command payload schemas and the expanded envelope allowlist document the wire contract; local Kafka initialization creates all ten fixed topics.

## Slice 4: operator DLQ and compensation recovery

Implemented:

- Added an operator-only recovery surface protected by `OPERATOR_TOKEN`, independent from end-user Bearer tokens.
- Exposed DLQ inspection endpoints for dead-lettered Outbox events, including detail lookup for diagnosis.
- Added state-checked DLQ replay that moves only `dead_letter` events back to `pending` so the existing Relay path republishes them with normal Kafka acknowledgement and retry behavior.
- Added `compensation_records` with source/action/reason/actor/status/metadata so replay, manual notification repair, file rollback, or external remediation can be audited outside ordinary user flows.
- DLQ replay automatically writes an `outbox.replay` compensation record with the operator actor and reason.
- Documented the Operations API in OpenAPI with a separate `operatorBearerAuth` security scheme.

### Slice 4 verification

| Check | Result |
|---|---|
| Missing or invalid operator token is rejected | Passed |
| Dead-lettered Outbox events can be listed | Passed |
| DLQ replay restores the event to `pending` | Passed |
| DLQ replay creates a compensation record with actor and reason | Passed |
| Manual compensation records validate required fields and status | Passed |
| OpenAPI YAML parse | Passed |
| Targeted Go tests for eventbus, MySQL store, HTTP server, and config | Passed |

## Slice 5: degradation policy enforcement and circuit breakers

Implemented:

- Chat generation now reads the live reliability policy instead of only exposing it. L3 `accept_only` persists the user message and accepted generation job but does not start model execution.
- Worker chat consumers also honor L3 `accept_only`: Kafka commands can be acknowledged while the durable job remains accepted for later reconciler takeover after recovery.
- Model-selected explicit memory writes honor the live policy inside chat processing. The Worker acknowledges legacy `memory.extract.v1` events without creating new memory so historical queues can drain safely.
- Chat context construction skips long-term recall when policy disables full RAG/context enrichment and emits a traceable `rag_skipped` generation event.
- Document Q&A applies the same full-RAG policy and returns a degraded, evidence-insufficient response without hitting the vector index during L1-L3 protection.
- Model providers are wrapped with a configurable dependency circuit breaker. When the model circuit is open, claimed jobs are deferred back to `accepted` with a later `available_at` instead of being marked failed.
- API and Worker both sample reliability signals and wire the current policy into chat execution, so request preservation works in background processing as well as inline tests.

### Slice 5 verification

| Check | Result |
|---|---|
| Circuit breaker opens after consecutive dependency failures | Passed |
| Circuit breaker half-opens after cooldown and closes on success | Passed |
| L3 chat accept-only preserves the user message and accepted job without calling the provider | Passed |
| Open model circuit defers a claimed job back to `accepted` rather than failing it | Passed |
| Degraded document query skips RAG and returns `degraded=true` with no fabricated evidence | Passed |
| Targeted Go tests for reliability, conversation, HTTP server, config, and MySQL store | Passed |

## Slice 6: internal release observability and runbooks

Implemented:

- Added Prometheus alert rules for L3 degradation, durable queue lag, oldest runnable job age, model error ratio, model p95 latency, HTTP 5xx growth, and suspected DLQ review.
- Added a Grafana dashboard JSON for degradation level, queue lag, oldest job age, model health, and route/status request rates.
- Added SLO and error-budget policy for internal release gates, alert routing, and release freeze thresholds.
- Added incident response runbook covering L3 accept-only, queue backlog, model outage, API 5xx, DLQ replay, and compensation records.
- Added backup/restore, object restore, Qdrant rebuild, deletion, and data-export drill runbooks.
- Added security, fault, and load test plan for auth, operator surface, object access, prompt injection, Kafka duplicate delivery, Worker loss, model outage, and backlog recovery.
- Added Web PWA internal-release package: manifest, installable SVG icon, production service-worker registration, and a network-first shell cache that intentionally avoids caching user API data.
- Recorded the final internal restore/fault/load drill evidence under `docs/runbooks/evidence/2026-07-08-m6-internal-drill.md`.
- Hardened Kafka consumers against historical poison records: invalid envelopes and non-UUID event IDs are recorded to `kafka_poison_messages`, committed, skipped, and surfaced through the operator API instead of crashing the Worker.
- Kafka consumer broker/session errors are now treated as transient, logged, and retried by the runner rather than terminating the Worker process.
- Added operator visibility for skipped Kafka poison messages through `GET /v1/ops/kafka/poison-messages`.

### Slice 6 verification

| Check | Result |
|---|---|
| Prometheus rules YAML parses | Passed |
| Grafana dashboard JSON parses | Passed |
| PWA manifest TypeScript compiles through `pnpm check` | Passed |
| Service worker avoids `/v1/*` and `/api/*` user data caching | Passed by code review |
| Go full test suite | Passed |
| OpenAPI YAML parse | Passed |
| Real MySQL restore drill and migration verification | Passed |
| Worker downtime durable recovery drill | Passed |
| Kafka outage durable recovery drill | Passed |
| 20-message load drill | Passed |
| TXT document parse/chunk/Qdrant retrieval drill | Passed |
| Poison Kafka event IDs are recorded and skipped without processing | Passed |
| Transient Kafka consumer errors retry without stopping the runner | Passed |

## M6 final drill evidence

- Evidence: `docs/runbooks/evidence/2026-07-08-m6-internal-drill.md`
- Result: passed with follow-up hardening implemented for Kafka poison-message visibility and consumer retry behavior.

M6 is complete for the internal-release scope.
