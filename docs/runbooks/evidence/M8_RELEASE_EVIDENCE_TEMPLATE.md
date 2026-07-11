# M8 internal release evidence template

Date/time:

Environment:

- API base URL:
- API image/commit:
- Worker image/commit:
- Database:
- Kafka brokers:
- Redis:
- Qdrant:
- Model provider:
- Operator account used for verification:

## 1. Static release gates

Run from the repository root before deployment:

```text
make release-check
go vet ./...
```

Evidence:

```text
paste command output summary here
```

Result:

- [ ] Passed
- [ ] Failed with ticket:

## 2. Migration verification

Target database:

```text
schema_migrations before:
schema_migrations after:
new migration versions:
rollback/down scripts reviewed:
```

Evidence:

```text
paste migration output and schema_migrations query here
```

Result:

- [ ] Passed
- [ ] Failed with ticket:

## 3. Operator authentication and MFA

Verify production-like operator configuration:

```text
APP_ENV=production
OPERATOR_MFA_REQUIRED=true
WEB_ORIGIN=https://...
```

Operator account checks:

- [ ] admin operator can authenticate with token + `X-Operator-TOTP`
- [ ] viewer operator cannot access admin-only endpoints
- [ ] legacy `OPERATOR_TOKEN` is not used for normal operations

Evidence:

```text
paste HTTP status/body snippets without tokens or TOTP secrets
```

## 4. Release-readiness API

Preferred automated collection:

```sh
API_BASE_URL=https://api.example.com \
OPERATOR_ACCOUNT_TOKEN=... \
OPERATOR_TOTP=123456 \
make release-evidence
```

Artifacts:

```text
evidence_dir:
manifest:
status files:
response headers files:
release-readiness file:
audit CSV file:
```

Validation:

```sh
RELEASE_EVIDENCE_DIR=<evidence_dir> make validate-release-evidence
```

Validation result:

- [ ] Passed
- [ ] Failed with ticket:

Command:

```sh
curl -sS \
  -H "Authorization: Bearer $OPERATOR_ACCOUNT_TOKEN" \
  -H "X-Operator-TOTP: $OPERATOR_TOTP" \
  "$API_BASE_URL/v1/ops/release-readiness"
```

Expected:

- aggregate `status` is `ready`
- required checks are `passed`
- `active_admin_operator` is `passed`
- `active_mfa_admin_operator` is `passed`
- validator-required checks are present:
  - `service_ready`
  - `operator_mfa_required`
  - `operator_account_store`
  - `active_admin_operator`
  - `active_mfa_admin_operator`
  - `identity_admin_store`
  - `operations_store`
  - `kafka_transport_enabled`
  - `https_web_origin`
  - `security_headers_enabled`
  - `model_provider_configured`
- warnings are understood and documented

Evidence:

```json
{}
```

Result:

- [ ] Passed
- [ ] Blocked with ticket:

## 5. Operations smoke

Checks:

- [ ] `GET /healthz`
- [ ] `GET /readyz`
- [ ] `GET /v1/meta`
- [ ] `GET /metrics` from monitoring network
- [ ] `healthz.headers` contains `X-Content-Type-Options`, `X-Frame-Options`, `Referrer-Policy`, `Cross-Origin-Opener-Policy`, and `Permissions-Policy`
- [ ] `GET /v1/ops/outbox/dead-letter`
- [ ] `GET /v1/ops/kafka/poison-messages`
- [ ] `GET /v1/ops/email/deliveries`
- [ ] `GET /v1/ops/console/bootstrap`

Evidence:

```text
paste status codes and non-sensitive snippets
```

## 6. Audit export evidence

Command:

```sh
curl -sS \
  -H "Authorization: Bearer $OPERATOR_ACCOUNT_TOKEN" \
  -H "X-Operator-TOTP: $OPERATOR_TOTP" \
  "$API_BASE_URL/v1/ops/audit-logs/export?format=csv&limit=20" \
  -o audit-logs-sample.csv
```

Expected CSV columns:

```text
id,occurred_at,actor_type,actor_id,actor_label,action,resource_type,resource_id,trace_id,metadata
```

Evidence:

```text
row count:
header:
sample actions:
```

Result:

- [ ] Passed
- [ ] Failed with ticket:

## 7. Fault/load drill subset

Choose at least one from each group for an internal release; run the full set before broader beta:

Fault:

- [ ] Kill Worker and verify accepted jobs recover
- [ ] Stop Kafka and verify Outbox resumes
- [ ] Force model provider timeout and verify degradation/defer behavior

Load:

- [ ] Chat acceptance burst
- [ ] Document upload burst
- [ ] Operator audit export sample

Evidence:

```text
paste metrics/status summaries
```

## 8. Rollback decision

Rollback trigger reviewed:

- [ ] yes
- [ ] no

Known risks:

- 

Decision:

- [ ] proceed
- [ ] hold
- [ ] rollback

Approver:

Timestamp:
