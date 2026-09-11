# Loki structured logging runbook

## Scope

The application emits one canonical, redacted JSON record to standard output.
Grafana Alloy discovers only the `api`, `worker`, and `agent-worker` containers,
parses those records, and forwards them to Loki. The management API queries Loki
for interactive exploration and automatically falls back to the PostgreSQL
`ops.system_logs` audit copy when Loki is unavailable or has not ingested a
recent record yet.

This split is intentional:

- Loki is the searchable operational log backend.
- PostgreSQL is the bounded durable fallback used by the management console and
  existing log-based incident workflows.
- Langfuse remains the LLM/Agent-specialized trace backend.
- Prometheus remains the low-cardinality metrics backend.

## Start and verify

Enable the local settings in `.env`, then start the application and
observability profiles:

```sh
docker compose --env-file .env -f deploy/compose/compose.yml \
  --profile app --profile agent --profile agent-runtime --profile observability \
  up -d --build

curl -fsS http://127.0.0.1:3100/ready
curl -fsS http://127.0.0.1:12345/-/ready
```

The management console at `/admin` is the supported query surface. Its log
center shows `Loki` for normal reads and an explicit `PostgreSQL 回退` status
when it has degraded. The browser never receives a Loki bearer token or tenant
credential.

For a direct local smoke query:

```sh
curl -G -fsS http://127.0.0.1:3100/loki/api/v1/query_range \
  --data-urlencode 'query={job="ai-companion",environment="development"}' \
  --data-urlencode 'direction=backward' \
  --data-urlencode 'limit=20'
```

## Correlation contract

Every structured application record includes `service`, `environment`,
`level`, `event`, and a redacted message. When the execution context provides
them, it also includes:

- `trace_id`: the distributed HTTP/Kafka request trace.
- `span_id`: the current HTTP, producer, consumer, dispatcher, or worker hop.
- `trace_flags`: the two-hex-digit W3C sampling flags.
- `run_id`: the business Agent Run identifier.
- `agent_trace_id`: normally the same canonical ID as `trace_id`, which is also
  supplied to Langfuse. Reconciliation without incoming context falls back to
  `SHA-256("agent-run:" + run_id)` truncated to 16 bytes.
- `node` or `graph_node`: the Agent graph location.
- `error_code`: a stable failure classification.

Only bounded deployment identity fields (`job`, `service`, Loki's derived
`service_name`, `environment`, and `level`) are Loki index labels. Run, Trace,
node, event, and error identifiers are stored as structured metadata and remain
searchable without creating unbounded label cardinality.

Example operator queries:

```text
GET /v1/ops/logs?run_id=<run-id>&since=<RFC3339>
GET /v1/ops/logs?trace_id=<request-or-agent-trace-id>&since=<RFC3339>
GET /v1/ops/logs?service=agent-worker&level=ERROR&q=timeout
```

## Privacy and access

The `opslog` handler redacts sensitive-key values, bearer/key-like strings,
email addresses, URL credentials, nested objects, and raw JSON before either
stdout or PostgreSQL receives the record. Do not bypass the handler with a
separate logger, and never log prompts, model responses, request bodies,
cookies, authorization headers, or raw provider payloads.

The local Loki endpoint is bound to loopback and has authentication disabled.
For a remote/multi-tenant Loki deployment, terminate TLS and authentication in
the approved gateway, set `LOKI_BASE_URL`, `LOKI_TENANT_ID`, and
`LOKI_BEARER_TOKEN` only in the server-side secret store, and do not publish the
Loki service directly to the Internet.

## Retention and recovery

The bundled single-node configuration uses TSDB schema v13, filesystem storage,
and seven-day retention. `loki-data` and `alloy-data` are named Docker volumes.
For production-scale retention, move chunks and indexes to supported object
storage, preserve schema periods during upgrades, and back up the authoritative
PostgreSQL operational data according to the database runbook.

If the console reports a fallback:

1. Check `http://127.0.0.1:3100/ready`.
2. Inspect `docker logs ai-companion-loki-1` and
   `docker logs ai-companion-alloy-1` for configuration or push errors.
3. Confirm the Docker socket is mounted read-only and Alloy is discovering the
   three application services.
4. Query `/v1/ops/logs` again after ingestion resumes; no application restart is
   required to leave fallback mode.
