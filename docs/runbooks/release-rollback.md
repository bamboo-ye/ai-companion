# Internal Release and Rollback Runbook

Date: 2026-07-11

This runbook is for backend internal releases from M8 onward. The bias is boring and reversible: ship small, verify durability and operator controls first, then expand traffic.

## Pre-release checklist

1. Code checks:
   - `make release-check`
   - `go vet ./...` when touching low-level platform code
   - `pnpm check` when touching Web code
2. Database:
   - forward migration reviewed
   - rollback migration exists for schema-only changes
   - migration tested on restored or local Docker MySQL
3. Optional Kafka scale transport (only when `KAFKA_ENABLED=true`):
   - topic allowlist and Compose initializer match event schemas
   - Outbox relay and consumer metrics healthy
   - no unexpected `dead_letter` backlog
4. Operator and security controls:
   - `OPERATOR_MFA_REQUIRED=true` in production
   - at least one active `admin` operator account exists and can authenticate with TOTP
   - `/v1/ops/release-readiness` includes `active_admin_operator=passed`
   - `/v1/ops/release-readiness` includes `active_mfa_admin_operator=passed`
   - operator account management refuses to disable the last active admin or last active MFA admin with `operator_admin_lockout_protection`
   - legacy `OPERATOR_TOKEN` access is not used for normal operation
   - `/v1/ops/release-readiness` returns `status=ready`
   - audit CSV export can be downloaded by an admin operator
5. Reliability:
   - `/metrics` reachable from monitoring network
   - alerts loaded
   - Grafana dashboard importable
6. Product:
   - release notes include user-visible changes
   - known limitations and rollback trigger are written down

## Deployment order

1. Apply database migrations.
2. Deploy Worker with backward-compatible consumers.
3. Deploy API.
4. Run release-readiness check with an admin operator token and TOTP.
5. Deploy Web/native clients only when the server contract is stable.
6. Watch:
   - API 5xx
   - degradation level
   - queue lag and oldest job age
   - model error ratio and latency
   - DLQ list
   - audit log creation for operator actions

## Smoke test

After deployment:

1. Register/login with a test account.
2. Create a character.
3. Send one chat message and confirm `202`.
4. Upload a small text document and confirm queued status.
5. Query `/v1/reliability`.
6. Queue one ledger export in a non-production test user.
7. Query `/v1/ops/release-readiness` with an admin operator account.
8. Export `/v1/ops/audit-logs/export?format=csv&limit=20`.
9. Check `/metrics` includes request counters and reliability gauges.
10. Capture an allowlisted metrics snapshot before the canary window and another after it:

    ```sh
    METRICS_URL="$API_BASE_URL/metrics" \
    OBSERVABILITY_SNAPSHOT=artifacts/observability-eval/before.json \
    make capture-observability

    METRICS_URL="$API_BASE_URL/metrics" \
    OBSERVABILITY_SNAPSHOT=artifacts/observability-eval/after.json \
    make capture-observability

    OBSERVABILITY_BEFORE=artifacts/observability-eval/before.json \
    OBSERVABILITY_AFTER=artifacts/observability-eval/after.json \
    make eval-observability-release
    ```

    The snapshot intentionally drops request-route labels and does not persist the URL,
    headers, operator credentials, prompts, or model responses.

For the normal path, run the same sequence atomically with the read-only health Canary:

```sh
API_BASE_URL="$API_BASE_URL" \
METRICS_URL="$API_BASE_URL/metrics" \
make observability-release-gate
```

The output directory contains private before/after snapshots, metrics JSON/JUnit, and
gate JSON/JUnit. Exit 0 and `decision=promote` make the bundle eligible for signing;
they do not by themselves permit the deployment controller to continue. Exit 1 and
`decision=rollback` block promotion; exit 2 means validation could not start safely and
also blocks promotion. This command only records a rollback decision—it does not change
traffic, images, migrations, or deployment state.

## Signed deployment handoff

The signing job and deployment controller must receive these values from the platform
secret manager, not from command arguments, logs, repository files, or Gate artifacts:

- `OBSERVABILITY_ATTESTATION_KEY`: at least 32 bytes
- `OBSERVABILITY_ATTESTATION_KEY_ID`: non-secret rotation identifier

Set `GATE_DIR` to the `output_dir` printed by the successful Gate. Sign that exact
directory once after a complete Gate:

```sh
OBSERVABILITY_GATE_DIR="$GATE_DIR" \
make attest-observability-release-gate
```

Immediately before promotion, verify the same directory inside the deployment trust
boundary:

```sh
OBSERVABILITY_GATE_DIR="$GATE_DIR" \
OBSERVABILITY_REQUIRED_DECISION=promote \
OBSERVABILITY_ATTESTATION_MAX_AGE=900 \
make verify-observability-release-gate
```

Only exit 0 authorizes the controller to perform its separately configured promotion.
The verifier binds the directory run ID, six fixed artifacts plus the optional structured
Agent Canary report, file sizes and SHA-256
digests, Gate timestamp, recomputed decision, key ID, and HMAC-SHA256 signature. It also
requires directory mode 0700 and file mode 0600, rejects links and unexpected files, and
allows at most 30 seconds of clock skew. A bundle cannot be re-signed in place.

The default required decision is `promote`. For incident evidence, a rollback bundle may
be verified with `OBSERVABILITY_REQUIRED_DECISION=rollback`; `any` is audit-only and must
never be used to authorize promotion. Missing credentials, an incomplete Gate, expiry,
signature failure, a decision mismatch, or any verifier nonzero exit must fail closed and
leave traffic unchanged. Rotate the HMAC key through the secret manager and change its
key ID together; retaining old verification keys is a platform-specific operational choice.

### Promotion authorization consumption

Gate verification remains read-only. Before issuing any authorization, run the dry check
with a deployment ID that uniquely identifies the intended environment and release:

```sh
OBSERVABILITY_GATE_DIR="$GATE_DIR" \
OBSERVABILITY_DEPLOYMENT_ID="$DEPLOYMENT_ID" \
make check-observability-deployment
```

After the normal release approval, explicitly issue a short-lived authorization:

```sh
OBSERVABILITY_GATE_DIR="$GATE_DIR" \
OBSERVABILITY_DEPLOYMENT_ID="$DEPLOYMENT_ID" \
OBSERVABILITY_AUTHORIZATION_TTL=300 \
make issue-observability-deployment-authorization
```

The default state directory is `artifacts/observability-deployment-authorizations`; production
must place it on a trusted, durable 0700 filesystem inside a single authorization-writer trust
boundary. Every authorization file is 0600. The HMAC subkey is domain-separated from the Gate
attestation signature. TTL is bounded to 1–900 seconds and is clipped so it cannot outlive the
Gate. An expired authorization is never replaced in place; use a new deployment ID and rerun
the complete Gate when necessary.

Immediately before invoking the platform-specific adapter, verify the authorization again:

```sh
OBSERVABILITY_GATE_DIR="$GATE_DIR" \
OBSERVABILITY_DEPLOYMENT_ID="$DEPLOYMENT_ID" \
make verify-observability-deployment-authorization
```

Repeated issuance for the same deployment ID and Gate returns the original signed record.
The same deployment ID cannot be rebound to another Gate, including under concurrent writers.
Pass the returned `idempotency_key` to the platform adapter and persist it atomically with the
platform deployment request. A local authorization cannot by itself guarantee exactly-once
effects across an external API: the adapter must use the platform's native idempotency or a
transactional deployment ledger. This layer never calls a cloud API, changes traffic, deploys
an image, or rolls back a database.

### Transactional controller dry run

The controller is platform-neutral and currently registers only `dry-run`. Its default ledger is
`artifacts/observability-deployment-controller/ledger.sqlite3`; production must place this 0600
database in a trusted 0700 directory. First produce a read-only request plan:

```sh
OBSERVABILITY_GATE_DIR="$GATE_DIR" \
OBSERVABILITY_DEPLOYMENT_ID="$DEPLOYMENT_ID" \
OBSERVABILITY_DEPLOYMENT_TARGET=local-dry-run \
make plan-observability-deployment
```

Planning does not create the ledger. Explicitly prepare the request, then simulate fenced dispatch:

```sh
OBSERVABILITY_GATE_DIR="$GATE_DIR" \
OBSERVABILITY_DEPLOYMENT_ID="$DEPLOYMENT_ID" \
OBSERVABILITY_DEPLOYMENT_TARGET=local-dry-run \
make prepare-observability-deployment

OBSERVABILITY_GATE_DIR="$GATE_DIR" \
OBSERVABILITY_DEPLOYMENT_ID="$DEPLOYMENT_ID" \
OBSERVABILITY_DEPLOYMENT_TARGET=local-dry-run \
make simulate-observability-deployment
```

The expected terminal state is `simulated`, with `attempt_count=1`, no external operation ID,
and three ordered events: prepared, dispatch_started, simulated. Repeating simulation returns the
existing terminal row without calling the adapter again. Status is read-only and refuses to create
a missing ledger:

```sh
OBSERVABILITY_DEPLOYMENT_ID="$DEPLOYMENT_ID" make observability-deployment-status
```

Future provider adapters must declare both lookup and idempotent-submit capabilities. After a
lease expires, the controller queries the provider by idempotency key before resubmission. A lookup
miss may be resubmitted only when the provider guarantees idempotent submit; an adapter with neither
capability is fenced into `indeterminate`. Unclassified exceptions and invalid result contracts are
also indeterminate. Operators must reconcile that state manually rather than deleting or editing the
ledger. The current dry-run adapter performs no network or deployment action.

Ledger schema v4 adds an immutable manual-resolution audit row to the v3 durable reconciliation
schedule and v2 `reconciliation_count`. Upgrade v1, v2, or v3 only through the explicit
transactional command. The migration validates the existing table/column contract, preserves
operations and events, adds the counter when upgrading from v1, backfills accepted v2 operations as
immediately due, creates the empty resolution audit table, and changes the schema version in the
same transaction:

```sh
make migrate-observability-deployment-ledger
```

Read-only status never performs this migration. After a future certified provider adapter returns
`accepted`, the dispatch path treats it as non-resubmittable. Reconciliation uses a separate lease
and calls lookup with the original idempotency key only when its persisted schedule is due. The
default first delay is 15 seconds, exponential delay is capped at 120 seconds, lookup is limited to
10 attempts, and the total deadline is 30 minutes. A still-running operation or retryable lookup
error advances the schedule; exhausting the attempt budget or deadline becomes indeterminate without
another lookup. A matching completion becomes completed. Lookup miss, external operation-ID change,
invalid lookup output, or an unknown exception becomes indeterminate and requires manual provider
reconciliation.

Inspect scheduler health without mutating or migrating the ledger:

```sh
make observability-deployment-health
```

Treat non-zero `overdue_deadline` or `schedule_missing` as a page. A rising `due` count or oldest
accepted age means the external scheduler or adapter fleet is not keeping up. The code-level batch
runner selects only due rows, excludes live leases, and reports completed, pending, retryable,
indeterminate, busy, and unavailable-adapter outcomes. The only provider-backed reconciliation CLI
in this repository is the local SQLite sandbox; it is not network capable. A production platform
integration must still register its own independently certified adapter before invoking the batch
runner.

### Dual-controlled manual resolution

Never update an `indeterminate` row with SQLite or an ad-hoc script. First export the provider's
operation result into the approved evidence store and calculate its SHA-256. The repository stores
only that digest, not provider credentials or raw evidence. Create a private evidence envelope:

```sh
OBSERVABILITY_DEPLOYMENT_ID="$DEPLOYMENT_ID" \
OBSERVABILITY_RESOLUTION_STATUS=completed \
OBSERVABILITY_RESOLUTION_REASON=provider_confirmed_completed \
OBSERVABILITY_RESOLUTION_REQUESTED_BY="$REQUESTER_ID" \
OBSERVABILITY_PROVIDER_EVIDENCE_SHA256="$PROVIDER_EVIDENCE_SHA256" \
make create-observability-deployment-resolution-evidence
```

Record the returned evidence path. In a requester job that can access only the requester key, issue
a five-minute request:

```sh
export OBSERVABILITY_RESOLUTION_REQUEST_KEY
export OBSERVABILITY_RESOLUTION_REQUEST_KEY_ID
OBSERVABILITY_RESOLUTION_EVIDENCE="$EVIDENCE_PATH" \
make request-observability-deployment-resolution
```

Record the request path. A different human and secret-manager identity must run the approval job.
It receives only the approval key; it intentionally cannot verify or forge the requester HMAC and
signs the exact request digest after review:

```sh
export OBSERVABILITY_RESOLUTION_APPROVAL_KEY
export OBSERVABILITY_RESOLUTION_APPROVAL_KEY_ID
OBSERVABILITY_RESOLUTION_REQUEST="$REQUEST_PATH" \
OBSERVABILITY_RESOLUTION_APPROVED_BY="$APPROVER_ID" \
make approve-observability-deployment-resolution
```

The final controller job receives both keys for verification. Check first; only an explicit apply
changes the local ledger:

```sh
OBSERVABILITY_RESOLUTION_EVIDENCE="$EVIDENCE_PATH" \
OBSERVABILITY_RESOLUTION_REQUEST="$REQUEST_PATH" \
OBSERVABILITY_RESOLUTION_APPROVAL="$APPROVAL_PATH" \
make check-observability-deployment-resolution

OBSERVABILITY_RESOLUTION_EVIDENCE="$EVIDENCE_PATH" \
OBSERVABILITY_RESOLUTION_REQUEST="$REQUEST_PATH" \
OBSERVABILITY_RESOLUTION_APPROVAL="$APPROVAL_PATH" \
make apply-observability-deployment-resolution
```

Request and approval key IDs, raw key values, and human IDs must all differ. The request is capped at
15 minutes and approval at five minutes; defaults are five and three minutes. It binds
`updated_at`, `error_code`, adapter, external operation ID, and both evidence digests. A state change,
expired signature, tampered artifact, wrong signer, same-person approval, or different second
resolution fails closed. Applying the identical bundle concurrently changes state once and later
returns the same audit record. `completed` requires the ledger's external operation ID; `failed` may
resolve an operation that never received one. Neither path calls the provider.

### Certified local scheduler sandbox

Provider certification requires declared idempotent-submit and lookup capabilities, submits the
identical request twice, then queries the same key and requires all three results to identify one
external operation. The report hashes the external operation ID instead of persisting it. The only
CLI implementation is `sqlite-sandbox`; it rejects targets outside the explicit `isolated-` prefix
and never accesses a network.

Inject a dedicated key and identifier from the secret manager. Do not reuse Gate, authorization,
requester, or approval keys:

```sh
export OBSERVABILITY_ADAPTER_CERTIFICATION_KEY
export OBSERVABILITY_ADAPTER_CERTIFICATION_KEY_ID
export OBSERVABILITY_ADAPTER_CERTIFICATION_NONCE="$CERTIFICATION_RUN_ID"
make certify-observability-sandbox-adapter
```

The returned certificate is private, valid for one hour by default, capped at 24 hours, and binds
the sandbox database's persistent instance ID by SHA-256, adapter implementation version, request and
full certification report. Record its path and run one due batch:

```sh
OBSERVABILITY_ADAPTER_CERTIFICATION="$CERTIFICATION_PATH" \
make run-observability-sandbox-scheduler-once
```

Do not bypass certification verification. A missing, expired, future, modified, wrong-key,
wrong-version, or different-sandbox certificate must stop the run before controller work selection
and provider lookup. Keep the controller ledger, sandbox ledger, certificate directory, and files at
0700/0600. The one-shot command is suitable for an external scheduler; the bounded loop API is the
embedded-service primitive. Parallel invocations are supported by reconciliation leases and fencing.
Only the lease owner may count completion; competing observations report busy or select no row.

Do not treat this local certificate as approval for a production adapter. A write-capable production
integration needs a provider-specific sandbox, endpoint/account binding, credential isolation, review
evidence, and a separately registered implementation. The repository's only network integration is the
read-only shadow observer below; it cannot submit or update a Provider operation.

### Local scheduler service profile

The `deployment-sandbox` Compose profile is opt-in and uses the same local-only adapter. Its setup
container runs as UID 10002, creates or validates the v4 controller ledger, certifies the provider
sandbox, and exits. The scheduler starts only after setup succeeds. Both containers share a named
volume whose certificate, SQLite files and state remain 0600 under 0700 child directories.

```sh
export OBSERVABILITY_ADAPTER_CERTIFICATION_KEY
export OBSERVABILITY_ADAPTER_CERTIFICATION_KEY_ID
export OBSERVABILITY_ADAPTER_CERTIFICATION_NONCE="$CERTIFICATION_RUN_ID"
make deployment-sandbox-up
```

Confirm `http://127.0.0.1:9465/healthz` and `/readyz` return 200, then inspect `/metrics`. Required
signals include `up`, `ready`, success/error run counters, consecutive failures, last success and
duration, certification expiry, accepted/due/overdue and indeterminate gauges. State is atomically
written after each run and bound to the certificate, implementation and persistent provider instance.
Restart must preserve counters; it must not create a new Provider operation for the same nonce.
Name the optional Prometheus scrape job `deployment-scheduler-sandbox`; the unavailable alert is
evaluated only when that target is configured, so environments that never enable this profile remain
silent.

The service retries only until the configured consecutive-failure budget, default three. Certification
is verified on every iteration, so expiry, tampering or instance replacement makes readiness false and
performs no Provider lookup. After the third consecutive error the process exits nonzero for the
container restart policy. SIGINT/SIGTERM sets a stop event, finishes the current bounded iteration,
closes the HTTP server and exits zero. Stop it without affecting application services or deleting the
named volume:

```sh
make deployment-sandbox-down
```

Use a new nonce for planned recertification. The old per-certificate state remains immutable evidence.
See the incident runbook for the four scheduler alerts and recovery criteria.

### Pre-production read-only shadow gate

Before considering a write adapter, run the exact HTTPS Provider read contract against a controller
ledger snapshot. Supply the token through the environment/secret manager, never a command argument:

```sh
export OBSERVABILITY_SHADOW_PROVIDER_NAME
export OBSERVABILITY_SHADOW_PROVIDER_BASE_URL
export OBSERVABILITY_SHADOW_PROVIDER_ALLOWED_HOST
export OBSERVABILITY_SHADOW_PROVIDER_INSTANCE
export OBSERVABILITY_SHADOW_PROVIDER_TOKEN
make deployment-shadow-check
```

Exit 0 requires every selected row to match and every lookup to complete within the bounded retry
policy. Exit 3 means the command ran successfully but found drift or exhausted lookup errors; it is a
release hold, not permission to repair either side. Exit 2 is unsafe/missing configuration and exit 1
is a runtime or state-integrity failure.

For a stable observation window, `make deployment-shadow-up` starts the non-root service with the
controller volume mounted read-only and an independent writable state volume. Confirm readiness and
inspect `ai_companion_deployment_shadow_outcomes`, `drift`, `lookup_errors`, retries and run counters on
loopback port 9466. Name an optional Prometheus target `deployment-shadow-preprod`. Stop with
`make deployment-shadow-down`; this preserves shadow evidence and cannot stop application services.

Do not promote a write adapter until the agreed shadow window has zero lookup errors and zero drift,
Provider credentials are independently verified read-only, and the Provider-specific write/idempotency
certification has separate approval. Shadow reports deliberately exclude endpoint URLs, tokens,
idempotency keys, external operation IDs and raw responses; use Provider audit exports as separately
controlled evidence when a discrepancy requires manual resolution.

The service appends every successful and failed iteration to `history.sqlite3`; the controller ledger
remains read-only. Do not delete failed samples to manufacture a stable period. Evaluate a new empty
bundle directory only after the configured observation period:

```sh
export OBSERVABILITY_SHADOW_PROVIDER_INSTANCE_SHA256
export OBSERVABILITY_SHADOW_GATE_DIR="artifacts/observability-deployment-shadow/gates/$RUN_ID"
make evaluate-deployment-shadow-gate
```

The default policy requires 30 runs, 15 minutes of coverage, 10 selected records, gaps no larger than
90 seconds, freshness within 90 seconds and zero failures/drift/lookup errors. Exit 3 means a valid
`hold` artifact was written. Preserve it for diagnosis, start a new observation window and use a new
gate directory; never edit or overwrite the old report.

Only after exit 0, inject the existing observability attestation key and run:

```sh
make attest-deployment-shadow-gate
make verify-deployment-shadow-gate
```

The attestation is short-lived and binds the exact provider identity, policy/window report digest and
history-chain head. A repeated identical signing job reuses the same valid artifact; a changed bundle,
wrong signer, expiry or stale report fails closed. This attestation deliberately uses a separate HMAC
domain from release promotion and is not accepted by deployment authorization commands.

To exercise the direct Agent path, opt in explicitly with
`OBSERVABILITY_CANARY_SPEC=evals/observability/canaries/agent-direct.v1.json`. That
Canary creates disposable test data and may incur model usage. Never place credentials
or command strings in a Canary spec; commands are stored as argument arrays and executed
without a shell command string.

The direct Gate executes five versioned short-answer cases. All five must complete as
`direct`, use exactly one model call, avoid planning and tool nodes, create exactly one
assistant message each, satisfy topic/length/sentence limits, and finish below the latency
limits in `evals/agent/baselines/direct-canary.v1.json`. It independently re-inspects the
persisted response for repeated sentences. Exact duplicate sentences are removed by the
deterministic response repair without a second model call; remaining duplication, failed
repair, model-based rewrite, content failure, extra model calls or latency regression
blocks promotion. The report is copied into the private Gate bundle and recomputed from
its per-case evidence during attestation, so editing only its summary cannot pass.

For the zero-model online Agent release gate, use
`OBSERVABILITY_CANARY_SPEC=evals/observability/canaries/agent-observability.v1.json`.
It runs five finite synthetic cycles, requires exact `not_claimed` and terminal `no_match`
outcomes, caps wake latency at one second, and requires zero attributable Python executions
and model calls. The gate rejects a missing, stale, unsafe, malformed or failing structured
Canary report even when the child process exits zero. A passing report is copied into the
private Gate directory, its digest and summary are bound into `gate-report.json`, and the
release attestation signs all seven artifacts. Normal business traffic is isolated by dedicated
low-cardinality counters.

Set `OBSERVABILITY_ENVIRONMENT_ID` to a stable, non-secret deployment identity such as
`staging-cn` or `production-cn`. The Gate hashes this value before storage and takes an
exclusive lease under `OBSERVABILITY_CANARY_LEASE_ROOT`. An overlapping Gate returns
`rollback` with `canary_concurrent_run` and does not dispatch its Canary. Every executor for
one environment must share the same lease directory; do not place it in a per-job workspace.

Completed Gates are appended to `OBSERVABILITY_GATE_HISTORY_LEDGER`. This private SQLite
ledger is bound to the environment digest, rejects update/delete operations, and hashes each
event onto its predecessor. Before gray expansion, run `make observability-release-trend` to
verify the complete chain and write `history-trend.json`. The Agent observability trend includes
promote/rollback counts, concurrent rejections, P95 Canary duration, P95 terminal-wake maximum
latency, and accumulated Python/model executions; the last two must remain zero for zero-model
samples. Direct samples add minimum direct/single-call/quality/content rates, direct P95 latency,
repair success rate, duplicate violations and response-quality failures. Any value outside the
versioned baseline blocks gray expansion; do not edit or delete failed history events.

### Direct-answer traffic stages

The multi-window controller is advisory and never calls a traffic platform. After each direct
Gate has been appended to the shared environment ledger, evaluate the exact current stage:

```sh
OBSERVABILITY_ENVIRONMENT_ID=staging-cn \
OBSERVABILITY_GATE_HISTORY_LEDGER=artifacts/observability-eval/history.sqlite3 \
OBSERVABILITY_DIRECT_ROLLOUT_CURRENT_TRAFFIC=5 \
make evaluate-agent-direct-rollout
```

The versioned policy uses stages `0, 5, 10, 25, 50, 100`, a one-sample fast window and a
three-sample stable window covering at least ten minutes. `expand` advances exactly one stage;
`hold` preserves the stage when evidence is missing, stale or irregular; `rollback` selects the
previous stage for execution/latency regressions; `disable` selects 0% for quality, content,
duplicate response or duplicate assistant-delivery failures. Exit codes are 0, 3, 4 and 5
respectively; exit 2 is unsafe configuration and exit 1 is invalid evidence.

Before a platform-specific traffic adapter consumes a recommendation, inject the dedicated
rollout signing key and key ID from the secret manager and sign the new empty decision directory:

```sh
OBSERVABILITY_DIRECT_ROLLOUT_DECISION_DIR="$DECISION_DIR" \
make attest-agent-direct-rollout

OBSERVABILITY_DIRECT_ROLLOUT_DECISION_DIR="$DECISION_DIR" \
OBSERVABILITY_DIRECT_ROLLOUT_REQUIRED_DECISION=expand \
make verify-agent-direct-rollout
```

The rollout key is domain-separated and must not be reused as a release, authorization, shadow
or resolution key. Verification replays the exact policy over the full append-only Gate history,
checks the environment and chain head, validates the signature and freshness, and fails after any
new Gate is appended.

### Direct traffic read-only shadow

After the local traffic adapter is certified, but before any platform mutation adapter is enabled,
configure the platform's read-only `GET /v1/direct-traffic/state` endpoint and inject
`OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_PROVIDER_TOKEN` through the secret manager. The local desired
state ledger and the endpoint must use the same stable target Provider identity from initialization;
do not reuse a ledger initialized under the default local-sandbox identity for another Provider.

Run `make check-agent-direct-traffic-shadow` repeatedly with the same non-secret environment and
Provider identities. `match` requires equality of percentage, revision and history-chain head;
`drift` and `lookup_error` return non-zero. Run `make status-agent-direct-traffic-shadow` to verify
and summarize the complete append-only history.

Stop the rollout investigation on any stale snapshot, Provider identity mismatch, revision/chain
drift, redirect, malformed response or concurrent local state change. This observer is
intentionally GET-only and is not authorization for platform traffic changes.

Once the history satisfies the configured sampling period, create a fresh empty directory and run
`make evaluate-agent-direct-traffic-shadow-gate`. A `hold` result is terminal for that attempt and
must not be signed. For `pass`, inject the dedicated
`OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_GATE_KEY` and `_KEY_ID`, then run
`make attest-agent-direct-traffic-shadow-gate` followed immediately by
`make verify-agent-direct-traffic-shadow-gate`.

Evaluation and both signature operations take the shared environment lock. Signing and verification
replay the policy and require the live event count plus history chain head to match exactly. Any new
observation, timestamp disorder, drift, lookup error, stale sample, signature mismatch or expiry
invalidates the evidence. The key is domain-separated and must not be reused for rollout decisions,
adapter certification, release attestation or deployment shadow attestation.

Before writing a production adapter, exercise the signed decision against the local-only traffic
sandbox. Initialize it once using the stage actually deployed in that environment:

```sh
OBSERVABILITY_ENVIRONMENT_ID=staging-cn \
OBSERVABILITY_DIRECT_ROLLOUT_PROVIDER_INSTANCE=staging-local-sandbox \
OBSERVABILITY_DIRECT_ROLLOUT_INITIAL_TRAFFIC=5 \
make initialize-agent-direct-rollout-sandbox
```

Initialization is idempotent only when environment, provider identity and initial stage all match;
it cannot reset an existing ledger. Before apply, inject the dedicated adapter-certification key
and key ID. These are not the rollout decision key:

```sh
export OBSERVABILITY_DIRECT_TRAFFIC_ADAPTER_CERTIFICATION_KEY
export OBSERVABILITY_DIRECT_TRAFFIC_ADAPTER_CERTIFICATION_KEY_ID
export OBSERVABILITY_DIRECT_TRAFFIC_CERTIFICATION_NONCE=staging-cert-001
make certify-agent-direct-traffic-adapter

export OBSERVABILITY_DIRECT_TRAFFIC_CERTIFICATION="$CERTIFICATE"
make verify-agent-direct-traffic-adapter
```

Certification executes only inside the separate certification namespace. It applies the same
request twice, looks it up by idempotency key, sends a stale-revision request that must conflict,
and proves the operational state digest did not change. The signed certificate is private, valid
for one hour by default and capped at 24 hours. It binds adapter contract/name/version, provider
instance, environment, isolation namespace, challenge requests and results. A retry with the same
nonce recovers after challenge execution without creating more provider operations; intentional
recertification uses a new nonce.

Inject the rollout decision signing key as before and preview first:

```sh
OBSERVABILITY_DIRECT_ROLLOUT_DECISION_DIR="$DECISION_DIR" \
make plan-agent-direct-rollout-traffic

OBSERVABILITY_DIRECT_ROLLOUT_DECISION_DIR="$DECISION_DIR" \
OBSERVABILITY_DIRECT_TRAFFIC_CERTIFICATION="$CERTIFICATE" \
make apply-agent-direct-rollout-sandbox

make status-agent-direct-rollout-sandbox
```

`plan` is read-only and does not require an adapter certificate. `apply` accepts `expand`, `rollback`
and `disable`; a valid `hold` exits 3 and never writes an operation. Before mutation, apply verifies
both independent signatures and requires the certificate to match the execution adapter identity.
Exit 4 is a concurrent/stale-stage conflict, exit 2 is unsafe input, and exit 1 is invalid evidence,
certificate or ledger state. The commands share the environment lease with Canary runs, then fence
the exact Gate chain head again inside the traffic transaction. The sandbox uses an append-only hash
chain, a monotonic revision to prevent ABA replays, and one idempotency identity per signed decision.
A process failure before commit rolls back both traffic state and its audit event; replay after
commit returns the original receipt.

The sandbox never calls a network or platform API. Never use an unsigned, `hold`, expired,
stale-chain, wrong-provider or wrong-environment recommendation.

### Direct traffic preproduction writer

Do not use the global read path as evidence for a namespace write. Set the shadow Provider name to
`https-direct-traffic-preproduction` and its state path to
`/v1/namespaces/<preproduction-namespace>/direct-traffic/shadow-state`, then collect and attest a
fresh zero-drift window for that exact path. The namespace must begin with `preproduction-`; the
client rejects `production`, `prod-*`, staging and unscoped names before opening a connection.

Certify the execution adapter in its isolated namespace, set the resulting certificate path, and
run `make apply-agent-direct-traffic-preproduction`. The operation requires four separate secrets:
rollout signing key, certification key, shadow Gate key and preproduction bearer token. Never put
them in command arguments or evidence artifacts. The Provider receives the three signed artifacts
and must independently verify signature, key ID, expiry, provider/environment/namespace binding,
current stage, revision, operation count and chain head before applying CAS.

The client performs a lookup first, one PUT at most, and a mandatory readback. Exit 4 means CAS or
state conflict. Exit 5 means the PUT outcome could not be proven; do not send another PUT manually.
Query the same idempotency key, restore Provider availability, then rerun the same signed operation:
the client will reuse the Provider result and converge the local ledger. Any changed evidence needs
a new authorization rather than editing or re-signing an old request. If Provider and local chain
heads do not converge, freeze rollout changes and preserve both ledgers for incident review.

The preproduction client cannot target production. Enabling production requires a separate adapter,
separate credentials, a reviewed production namespace policy and another release stage.

Before any Provider write, inject a dedicated drill signing key and run
`make drill-agent-direct-traffic-preproduction`. Preserve the returned private bundle and verify it
with `make verify-agent-direct-traffic-preproduction-drill`. The report must contain five passing
scenarios: timeout-after-commit expansion reconciliation, idempotent replay, repair-regression
rollback, offline indeterminate stop, and same-key recovery. It must also say
`contains_live_provider_evidence=false`; this prevents a simulator result from being presented as a
real environment result.

Next inject the Provider token and run `make probe-agent-direct-traffic-preproduction` against the
exact namespace, host and provider instance intended for the exercise. The probe performs only
`GET state` and `GET change-by-idempotency-key` with a random key; it never calls `PUT`. Verify its
separate bundle and require `mode=live-read-only-probe` plus
`contains_live_provider_evidence=true`. If either probe read fails, redirects, returns a found random
key, or binds another identity, stop before writes.

After both bundles pass, perform one operator-authorized expansion using fresh rollout,
certification and shadow proofs. Confirm the receipt, Provider state, local state, revision,
operation count and chain head. Add a controlled quality regression to the release evidence, obtain
a new signed rollback decision and fresh shadow Gate, then apply the rollback. Never reuse the
expansion authorization for rollback. A PUT timeout remains query-only: restore access and rerun the
same signed operation so its idempotency lookup can reconcile. Preserve both ledgers and the signed
bundles in the release record. The endpoint contract is
`docs/contracts/direct-traffic-preproduction-provider-v1.md`.

### Initial production direct traffic: 0% to 5% only

Production is a new trust boundary, not a preproduction namespace rename. Configure an environment
and namespace beginning with `production-`, the `https-direct-traffic-production` shadow identity,
and five production-only secrets: Provider token, rollout key, adapter-certification key, shadow
Gate key, and fourth production-Gate key. Every value and key ID must be issued by the production
secret manager; do not copy preproduction values.

Before opening the Gate, collect and sign all of the following:

1. a fresh passing local preproduction contract drill bundle;
2. a separate fresh passing live read-only preproduction probe bundle;
3. a production adapter certificate created with
   `make certify-agent-direct-traffic-production-adapter`;
4. a production shadow history whose exact production namespace remains at `0%`, followed by a
   passing shadow Gate signed with `make attest-agent-direct-traffic-production-shadow-gate`;
5. a release decision evaluated with current traffic `0` and signed using
   `make attest-agent-direct-traffic-production-rollout`.

Point the five non-secret artifact variables at those immutable files and run
`make evaluate-agent-direct-traffic-production-gate`. A `hold` decision is terminal for that
attempt. Do not edit an artifact to clear a violation; obtain fresh evidence. For `pass`, set the
returned directory, run `make attest-agent-direct-traffic-production-gate`, then immediately run
`make verify-agent-direct-traffic-production-gate`.

Acquire an operator change window and run `make apply-agent-direct-traffic-production` once. A
successful receipt must show `5%`, revision `1`, one operation, and the fourth Gate attestation ID;
Provider and local chain heads must match. Re-running the same authorization is safe and reuses the
same remote/local operation. Exit `4` is a state conflict. Exit `5` is indeterminate: freeze all
new changes, restore read access, and rerun only the exact same authorization so same-key lookup can
reconcile. Never manually send a second `PUT`.

Stop and keep traffic at `0%` if any proof is stale, an identity differs, shadow drifts, current
traffic/revision/operation count is non-zero, the decision is not exactly `expand 0→5`, or a
production credential matches any other role. This adapter intentionally refuses `5→10`, rollback,
and disable. The next stage needs a new reviewed adapter and Gate; do not weaken this policy.
The Provider contract is `docs/contracts/direct-traffic-production-provider-v1.md`.

### Emergency production direct traffic: 5% to 0%

After the initial production activation, preserve its JSON receipt in private incident evidence. If
the release Gate reports a fast latency/execution regression, evaluate a rollout decision at current
traffic `5`. For duplicate, content, quality, or delivery regressions the decision will be
`disable`; both `rollback` and `disable` select `0%` here.

Use four emergency-only credentials: Provider token, rollout key, adapter-certification key, and
emergency-Gate key. They must be distinct from each other and from every normal-production key.
Then:

1. run `make attest-agent-direct-traffic-production-emergency-rollout`;
2. run `make certify-agent-direct-traffic-production-emergency-adapter`;
3. set the activation receipt, incident ID and authorizing actor, then run
   `make evaluate-agent-direct-traffic-production-emergency-gate`;
4. attest and verify the Gate with the corresponding Make targets;
5. run `make apply-agent-direct-traffic-production-emergency` during the incident change window.

No shadow Gate is accepted or required on this path. Success is exactly `0%`, revision `2`, two
operations, and matching local/remote history chains. Stop if the current state is not exactly
`5%`/revision `1`/one operation, the activation receipt differs, any proof is stale, or credentials
overlap. Preserve the incident-bound Gate, execution envelope, activation receipt and final receipt.
The Provider contract is
`docs/contracts/direct-traffic-production-emergency-provider-v1.md`.

### Recover production after an emergency rollback

Do not reuse the initial activation route or any proof created before the rollback. Keep traffic at
`0%` for at least the configured recovery cooldown. During that time collect a complete healthy
direct-canary stable window and a new zero-drift shadow window using recovery-only credentials.
Every selected health sample and every recovery shadow observation must postdate the rollback.

After the windows pass, create and sign an `expand 0→5` rollout with the `production_recovery` key
profile, certify the recovery adapter, and sign the recovery shadow Gate. Evaluate the recovery
Gate with the exact rollout report, rollout attestation, certificate, shadow attestation and
emergency rollback receipt. The Gate remains `hold` until the cooldown and both post-rollback
windows pass.

Run the recovery Gate evaluate, attest and verify targets, then
`make apply-agent-direct-traffic-production-recovery`. Success is exactly `5%`, revision `3`, three
operations, and matching local/remote chains. Preserve the rollback receipt and all recovery
artifacts together. Any later `5→10` expansion requires a new stage-specific production Gate; this
recovery adapter cannot perform it.

### Expand the recovered canary from 5% to 10%

After recovery, collect a new complete healthy rollout window (600 seconds by default) and a new
zero-drift shadow window using the `https-direct-traffic-production-expansion-10` identity. Create
and verify the isolated expansion adapter certificate, sign the 5%-to-10% rollout with the
`production_expansion` key profile, then evaluate and attest the expansion Gate. The Gate binds the
full rollout report and recovery receipt, so pre-recovery health and shadow evidence fail closed.

Run `make apply-agent-direct-traffic-production-expansion` only after Gate verification. Success
is exactly `10%`, revision `4`, four operations, and an
`agent-direct-traffic-production-expansion-10-receipt-v1` receipt. Preserve this receipt with the
recovery receipt and all five expansion proofs. The exact Provider boundary is documented in
`docs/contracts/direct-traffic-production-expansion-10-provider-v1.md`.

### Expand production from 10% to 25%

Preserve the successful 10% receipt and start fresh 10%-stage health and zero-drift shadow
windows. Use only the `production_expansion_25` rollout profile and the dedicated 25% Provider,
certification, shadow, and Gate credentials. The Gate binds the exact 10% receipt and both complete
reports; evidence collected before the 10% operation is ineligible.

After evaluating, attesting, and verifying the stage Gate, run
`make apply-agent-direct-traffic-production-expansion-25`. Success is exactly `25%`, revision `5`,
five operations, and an `agent-direct-traffic-production-expansion-25-receipt-v1` receipt. The
Provider boundary is `docs/contracts/direct-traffic-production-expansion-25-provider-v1.md`.

### Expand production from 25% to 50%

Preserve the successful 25% receipt and collect fresh health and zero-drift shadow windows after
its `applied_at`. Use only the `production_expansion_50` rollout profile and the dedicated 50%
Provider, certification, shadow, and Gate credentials. The permission-free common evaluator may
calculate bindings, but only the stage-specific Gate may sign and only the stage-specific Provider
may write.

Certify the adapter, attest the 25%-to-50% rollout, evaluate and attest the stage Gate, then verify
it before running `make apply-agent-direct-traffic-production-expansion-50`. Success is exactly
`50%`, revision `6`, six operations, and an
`agent-direct-traffic-production-expansion-50-receipt-v1` receipt. Preserve the receipt and all six
bound artifacts. The Provider boundary is
`docs/contracts/direct-traffic-production-expansion-50-provider-v1.md`.

### Expand production from 50% to 100%

Preserve the successful 50% receipt and collect fresh health and zero-drift shadow windows after
its `applied_at`. Use only the `production_expansion_100` rollout profile and the dedicated 100%
Provider, certification, shadow, and Gate credentials. A 25% or earlier receipt cannot authorize
this terminal transition.

Certify the adapter, attest the 50%-to-100% rollout, evaluate and attest the stage Gate, then verify
it before running `make apply-agent-direct-traffic-production-expansion-100`. Success is exactly
`100%`, revision `7`, seven operations, and an
`agent-direct-traffic-production-expansion-100-receipt-v1` receipt. Preserve the receipt and all
bound artifacts for final acceptance. The Provider boundary is
`docs/contracts/direct-traffic-production-expansion-100-provider-v1.md`.

After success, evaluate the rollout once more at 100%. The expected result is `hold` with reason
`maximum_stage_reached`; any further expansion decision or additional write is an incident.

### Produce the local production integration evidence

Before any live Provider operation, provision a dedicated drill attestation key that is not reused
by a traffic stage, then run `make drill-agent-direct-traffic-production-full`. The command executes
the complete production integration suite and emits a private, signed evidence bundle for the exact
`0→5→0→5→10→25→50→100` chain. Set
`OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_DRILL_BUNDLE` to that bundle and run
`make verify-agent-direct-traffic-production-drill` immediately before sign-off; verification also
checks that the bound source files have not changed.

This bundle is deliberately marked `local-production-integration` and
`contains_live_provider_evidence=false`. It proves the implementation, stage isolation, failure
controls and terminal behavior, but it does not satisfy live Provider acceptance by itself. Final
acceptance still requires a separately signed live read-only probe, operator-approved staged
execution, rollback rehearsal, and collected production observability evidence.

Automated evidence collection:

```sh
API_BASE_URL="$API_BASE_URL" \
OPERATOR_ACCOUNT_TOKEN="$OPERATOR_ACCOUNT_TOKEN" \
OPERATOR_TOTP="$OPERATOR_TOTP" \
make release-evidence
```

The script writes evidence to `.release-evidence/<timestamp>/` by default. Do not commit that directory; copy the non-sensitive summaries into a release evidence record.
The bundle includes response headers; validation checks the required security headers from `healthz.headers`.

Validate the collected bundle before sign-off:

```sh
RELEASE_EVIDENCE_DIR=.release-evidence/<timestamp> make validate-release-evidence
```

Example operator readiness call:

```sh
curl -sS \
  -H "Authorization: Bearer $OPERATOR_ACCOUNT_TOKEN" \
  -H "X-Operator-TOTP: $OPERATOR_TOTP" \
  "$API_BASE_URL/v1/ops/release-readiness"
```

## Rollback triggers

Rollback or disable traffic when any is true:

- Chat acceptance returns persistent 5xx.
- Accepted jobs are not queryable.
- MySQL migration corrupts or blocks core writes.
- `/v1/ops/release-readiness` changes from `ready` to `blocked` after deployment.
- L3 remains active for > 15 minutes after dependency recovery.
- A release introduces cross-user data visibility.
- Admin operator MFA auth or audit export is broken.
- `eval-observability-release` reports an absolute SLO or release-regression violation.
- `observability-release-gate` returns a `rollback` decision or cannot capture its
  required evidence.
- signed Gate verification fails, expires, or does not explicitly authorize `promote`.

## Rollback order

1. Stop or drain Web traffic if user-facing breakage is severe.
2. Roll back API to previous image/commit.
3. Roll back Worker only if consumers are causing side effects; otherwise keep Worker up to drain accepted work.
4. Do not roll back database schema unless the down migration was tested and no newer code has written incompatible rows.
5. If Kafka events were published by the faulty release, inspect DLQ and compensation records before replay.

## Communication

Internal release note template:

- Commit:
- Migration versions:
- User-visible changes:
- Operational changes:
- Release-readiness status:
- Audit export evidence:
- Rollback trigger:
- Verification:
- Known risks:
