# M6 SLO and Error Budget

Date: 2026-07-08

This runbook defines the internal-release reliability bar. It is intentionally conservative until target DAU, region, model provider, and paid data-retention requirements are confirmed.

## Service level objectives

| Area | SLO | Measurement |
|---|---:|---|
| API availability | 99.9% monthly | Non-5xx responses for authenticated API routes. |
| Accepted chat durability | 99.99% monthly | `POST /v1/conversations/{id}/messages` returns `202` only after message + generation job are durable. |
| Chat accept latency | p95 < 500 ms | API request duration for message acceptance, excluding model generation. |
| General API latency | p95 < 300 ms | Authenticated non-streaming API routes, excluding file upload body transfer. |
| Async task queryability | 100% | Every accepted chat/document/Skill/export/notification job has a queryable durable state. |
| Queue recovery | oldest runnable job < 120 s | `ai_companion_oldest_job_age_seconds`. |
| Model health | error ratio < 20% | `ai_companion_model_error_ratio`. |
| Agent retry recovery | >= 80% when at least 5 retrying runs settle in 5 min | `ai_companion_agent_execution_retry_recovery_ratio` guarded by settled outcomes. |
| Agent retry pressure | < 20 schedules in 5 min | `ai_companion_agent_execution_retries_recent{outcome="scheduled"}`. |
| Evidence safety | no fabricated RAG answer in degraded mode | Document query returns `degraded=true` or `sufficient=false` when evidence is unavailable. |
| Backup posture | RPO <= 15 min, RTO <= 2 h | Backup/restore drill evidence. |

## Error budget policy

- Monthly API availability budget at 99.9%: about 43 minutes of user-visible downtime.
- Burning 25% of the monthly budget in 24 hours freezes risky releases until the cause is understood.
- Burning 50% of the monthly budget in 7 days requires a corrective action issue and a rollback/feature-flag review.
- L3 `accept_only` is not counted as data loss when accepted requests remain durable and queryable; it still counts as user-visible degradation.

## Release gates

Before an internal release:

1. `go test ./...`, `go vet ./...`, `pnpm check`, OpenAPI YAML parse, and Compose config validation pass.
2. `GET /metrics` exposes degradation, queue, model, and HTTP counters.
3. Prometheus rules in `deploy/observability/prometheus-rules.yml` load successfully.
4. Grafana dashboard in `deploy/observability/grafana-dashboard.json` imports successfully.
5. For production-bound retry/observability changes, `make observability-drill` proves
   zero-sample suppression, firing delivery, and resolved delivery against the exact
   production rule durations. The manual `Observability alert drill` workflow provides
   the same optional remote gate.
6. Capture sanitized `/metrics` snapshots before and after the canary window, then run
   `make eval-observability-release`. Any hard violation blocks promotion.
   `METRICS_URL=<api>/metrics make observability-release-gate` performs these steps as
   one auditable operation with the read-only health Canary by default.
7. A restore drill completed within the last 30 days, or the release is explicitly marked “non-production internal only.”

Run `make validate-observability` before loading the files. The gate checks the retry
thresholds, one-minute hold duration, recovery minimum-sample guard, runbook links,
unique panel IDs, and Dashboard metric references, including negative self-tests.
`make observability-drill` adds official Prometheus/Alertmanager syntax checks and a
real isolated notification-chain exercise; it requires Docker and normally takes about
two minutes because it does not shorten the one-minute production hold duration.

The versioned release comparison baseline is
`evals/observability/baselines/release.v1.json`. Absolute limits stay below the alert/SLO
boundaries: L0 only, queue lag < 500, oldest runnable job < 120s, model errors < 20%,
model p95 < 10s, Agent p95 <= 80% of the default timeout, retry schedules < 20, and
exhausted outcomes <= 2. It additionally rejects a release-window increase above 50
queued jobs, 30s oldest-job age, 5 percentage points model error, 2s model p95, 30s
Agent p95, one terminal Agent failure, five retry schedules, or one exhausted outcome.
Retry recovery is checked only when at least five outcomes settle and must then remain
>= 80%. Baselines are reviewed and versioned; the evaluator never rewrites them.
The orchestrated gate returns `promote` only when all five phases pass. Canary failure,
timeout, unavailable after-snapshot, absolute SLO breach, or relative regression returns
`rollback`. An unavailable before-snapshot prevents the Canary from running. The gate
does not invoke a deployment rollback command; a deployment controller may consume its
exit code/report only after that external action is separately authorized.

## Alert routing

- Page: L3 degradation, queue lag >= 500, oldest job age >= 120s, elevated 5xx,
  Agent retry storm, or retry recovery below 80% with at least 5 settled samples.
- Ticket: model p95 latency >= 10s, at least 3 exhausted/deadline-exhausted Agent retries,
  or suspected DLQ/compensation review.
- Review next business day: SLO burn, recurring L1/L2 flapping, repeated manual compensation records.
