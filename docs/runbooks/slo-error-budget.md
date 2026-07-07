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
5. A restore drill completed within the last 30 days, or the release is explicitly marked “non-production internal only.”

## Alert routing

- Page: L3 degradation, queue lag >= 500, oldest job age >= 120s, elevated 5xx.
- Ticket: model p95 latency >= 10s, suspected DLQ/compensation review.
- Review next business day: SLO burn, recurring L1/L2 flapping, repeated manual compensation records.
