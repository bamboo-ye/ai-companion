# M6 Incident Response Runbook

Date: 2026-07-08

Use this runbook for internal release incidents involving API errors, queue lag, Kafka delivery, model outages, or DLQ recovery. Always preserve user data first; do not “fix” by deleting accepted work.

## First five minutes

1. Confirm scope:
   - `GET /healthz`
   - `GET /readyz`
   - `GET /metrics`
   - Authenticated `GET /v1/reliability`
2. Capture the current degradation level, queue lag, oldest job age, model error ratio, and p95 latency.
3. Check recent deploy:
   - latest git commit
   - API/Worker start time
   - configuration changes
4. Pick the primary path below.

## L3 保护模式

Symptoms:

- `ai_companion_degradation_level >= 3`
- New chat messages return `202` but no generation starts.
- Document query may return `degraded=true`.

Actions:

1. Confirm accepted requests are durable:
   - sample recent generation jobs with status `accepted`
   - confirm no broad 5xx on chat acceptance
2. Check dependencies:
   - MySQL availability
   - Kafka broker and consumer group health
   - model provider error rate and latency
   - Worker process health
3. If model-only outage:
   - keep L3/accept-only behavior
   - do not manually fail accepted jobs
   - wait for circuit cooldown or switch model provider after approval
4. Recovery:
   - verify degradation steps down L3 -> L2 -> L1 -> L0
   - confirm accepted jobs resume through Worker reconciler

## 队列积压

Symptoms:

- `ai_companion_queue_lag >= 500`
- `ai_companion_oldest_job_age_seconds >= 120`

Actions:

1. Identify the backlog class from MySQL reliability sampler queries:
   - Skill runs
   - document ingestion
   - document cleanup
   - chat generation
   - ledger exports
   - notification deliveries
   - Outbox relay
2. Scale or restart only the affected Worker group.
3. If Kafka consumer group is stuck, restart consumers; Inbox dedupe protects repeated events.
4. Do not lower lease durations during an incident unless stale workers are confirmed dead.

## 模型供应商故障

Symptoms:

- `ai_companion_model_error_ratio >= 0.5`
- `ai_companion_model_latency_p95_seconds >= 10`
- generation jobs are deferred with `model_circuit_open`

Actions:

1. Verify provider status and region/network path.
2. Keep the circuit breaker enabled; it protects accepted jobs from false failures.
3. If switching provider, record:
   - previous provider/model
   - new provider/model
   - approval
   - expected cost/latency change
4. After recovery, confirm deferred jobs return to `completed` or retryable terminal states.

## API 5xx

Symptoms:

- alert `AICompanionHTTP5xxHigh`
- user reports failed API actions

Actions:

1. Use `X-Trace-ID` from response or logs to find route-specific errors.
2. Check whether 5xx is isolated to:
   - document upload/query
   - Skill run creation/download
   - ledger export
   - auth/session
3. If write paths are affected, prefer rollback over hotfix unless data corruption risk is understood.
4. Confirm `POST /v1/conversations/{id}/messages` still preserves messages before declaring chat available.

## DLQ 重放与补偿

Prerequisites:

- Use `Authorization: Bearer $OPERATOR_TOKEN`.
- Set `X-Operator-ID` to a real operator identifier.

Inspect:

```bash
curl -H "Authorization: Bearer $OPERATOR_TOKEN" \
  http://localhost:8080/v1/ops/outbox/dead-letter
```

Replay one event:

```bash
curl -X POST \
  -H "Authorization: Bearer $OPERATOR_TOKEN" \
  -H "X-Operator-ID: sre-oncall" \
  -H "Content-Type: application/json" \
  -d '{"reason":"dependency recovered; safe to replay"}' \
  http://localhost:8080/v1/ops/outbox/$EVENT_ID/replay
```

Rules:

- Replay only after confirming the downstream operation is idempotent.
- Do not edit Kafka payloads manually.
- Record manual repairs as compensation records if the replay is not sufficient.

## Post-incident

Within one business day:

1. Record duration, impact, root cause, and affected job/event IDs.
2. Export relevant metrics screenshot or Prometheus range.
3. List compensations and DLQ replay IDs.
4. Decide whether to adjust alert thresholds, circuit breaker settings, or runbooks.
