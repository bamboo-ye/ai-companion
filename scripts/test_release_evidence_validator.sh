#!/bin/sh
set -eu

validator="${VALIDATE_RELEASE_EVIDENCE_SCRIPT:-scripts/validate_release_evidence.sh}"
tmp_root="$(mktemp -d "${TMPDIR:-/tmp}/ai-companion-evidence-validator.XXXXXX")"
trap 'rm -rf "$tmp_root"' EXIT HUP INT TERM

write_ready_bundle() {
  dir="$1"
  mkdir -p "$dir"
  for file in \
    healthz.json \
    readyz.json \
    meta.json \
    ops-console-bootstrap.json \
    dead-letter.json \
    kafka-poison-messages.json \
    email-deliveries.json \
    audit-logs.json
  do
    printf '{}\n' > "$dir/$file"
  done

  printf 'ai_companion_http_requests_total 1\n' > "$dir/metrics.prom"
  printf 'id,occurred_at,actor_type,actor_id,actor_label,action,resource_type,resource_id,trace_id,metadata\n' > "$dir/audit-logs.csv"
  cat > "$dir/release-readiness.json" <<'JSON'
{"status":"ready","checks":[{"key":"service_ready","status":"passed"},{"key":"operator_mfa_required","status":"passed"},{"key":"operator_account_store","status":"passed"},{"key":"active_admin_operator","status":"passed"},{"key":"active_mfa_admin_operator","status":"passed"},{"key":"identity_admin_store","status":"passed"},{"key":"operations_store","status":"passed"},{"key":"kafka_horizontal_scaling","status":"warning"},{"key":"https_web_origin","status":"passed"},{"key":"security_headers_enabled","status":"passed"},{"key":"model_provider_configured","status":"passed"}]}
JSON

  for name in \
    healthz \
    readyz \
    meta \
    metrics \
    ops_console_bootstrap \
    release_readiness \
    dead_letter \
    kafka_poison_messages \
    email_deliveries \
    audit_logs \
    audit_csv
  do
    printf '200\n' > "$dir/$name.status"
  done
  cat > "$dir/manifest.txt" <<EOF
GET /healthz healthz -> $dir/healthz.json status=200
GET /readyz readyz -> $dir/readyz.json status=200
GET /v1/meta meta -> $dir/meta.json status=200
GET /metrics metrics -> $dir/metrics.prom status=200
GET /v1/ops/console/bootstrap ops_console_bootstrap -> $dir/ops-console-bootstrap.json status=200
GET /v1/ops/release-readiness release_readiness -> $dir/release-readiness.json status=200
GET /v1/ops/outbox/dead-letter?limit=20 dead_letter -> $dir/dead-letter.json status=200
GET /v1/ops/kafka/poison-messages?limit=20 kafka_poison_messages -> $dir/kafka-poison-messages.json status=200
GET /v1/ops/email/deliveries?limit=20 email_deliveries -> $dir/email-deliveries.json status=200
GET /v1/ops/audit-logs?limit=20 audit_logs -> $dir/audit-logs.json status=200
GET /v1/ops/audit-logs/export?format=csv&limit=20 audit_csv -> $dir/audit-logs.csv status=200
output_dir=$dir
completed_at=2026-07-11T00:00:00Z
EOF
}

write_good_headers() {
  dir="$1"
  cat > "$dir/healthz.headers" <<'HEADERS'
HTTP/1.1 200 OK
Content-Type: application/json; charset=utf-8
X-Content-Type-Options: nosniff
X-Frame-Options: DENY
Referrer-Policy: no-referrer
Cross-Origin-Opener-Policy: same-origin
Permissions-Policy: camera=(), microphone=(), geolocation=()

HEADERS
  for header in \
    readyz \
    meta \
    ops_console_bootstrap \
    release_readiness \
    dead_letter \
    kafka_poison_messages \
    email_deliveries \
    audit_logs
  do
    cat > "$dir/$header.headers" <<'HEADERS'
HTTP/1.1 200 OK
Content-Type: application/json; charset=utf-8

HEADERS
  done
  cat > "$dir/metrics.headers" <<'HEADERS'
HTTP/1.1 200 OK
Content-Type: text/plain; version=0.0.4; charset=utf-8

HEADERS
  cat > "$dir/audit_csv.headers" <<'HEADERS'
HTTP/1.1 200 OK
Content-Type: text/csv; charset=utf-8

HEADERS
}

write_missing_frame_header() {
  dir="$1"
  write_good_headers "$dir"
  cat > "$dir/healthz.headers" <<'HEADERS'
HTTP/1.1 200 OK
Content-Type: application/json; charset=utf-8
X-Content-Type-Options: nosniff
Referrer-Policy: no-referrer
Cross-Origin-Opener-Policy: same-origin
Permissions-Policy: camera=(), microphone=(), geolocation=()

HEADERS
}

expect_rejection() {
  bundle="$1"
  expected="$2"
  label="$3"
  output="$tmp_root/reject-$(basename "$bundle").out"
  if "$validator" "$bundle" >"$output" 2>&1; then
    printf 'FAIL evidence validator accepted invalid bundle: %s\n' "$label" >&2
    exit 1
  fi
  if grep -q "$expected" "$output"; then
    printf 'PASS %s\n' "$label"
  else
    cat "$output" >&2
    printf 'FAIL evidence validator rejected bundle for an unexpected reason: %s\n' "$label" >&2
    exit 1
  fi
}

good_bundle="$tmp_root/good"
write_ready_bundle "$good_bundle"
write_good_headers "$good_bundle"
"$validator" "$good_bundle" >/dev/null
printf 'PASS evidence validator accepts ready bundle with security headers\n'

bad_bundle="$tmp_root/missing-frame-header"
write_ready_bundle "$bad_bundle"
write_missing_frame_header "$bad_bundle"
expect_rejection "$bad_bundle" 'response header missing or invalid: healthz.headers x-frame-options' 'evidence validator rejects missing X-Frame-Options'

missing_readiness_bundle="$tmp_root/missing-readiness-check"
write_ready_bundle "$missing_readiness_bundle"
write_good_headers "$missing_readiness_bundle"
cat > "$missing_readiness_bundle/release-readiness.json" <<'JSON'
{"status":"ready","checks":[{"key":"service_ready","status":"passed"},{"key":"operator_mfa_required","status":"passed"},{"key":"operator_account_store","status":"passed"},{"key":"active_admin_operator","status":"passed"},{"key":"identity_admin_store","status":"passed"},{"key":"operations_store","status":"passed"},{"key":"kafka_horizontal_scaling","status":"warning"},{"key":"https_web_origin","status":"passed"},{"key":"security_headers_enabled","status":"passed"},{"key":"model_provider_configured","status":"passed"}]}
JSON
expect_rejection "$missing_readiness_bundle" 'release-readiness required checks are missing or not passed' 'evidence validator rejects missing required readiness check'

secret_leak_bundle="$tmp_root/secret-leak"
write_ready_bundle "$secret_leak_bundle"
write_good_headers "$secret_leak_bundle"
printf '{"totp_secret":"SHOULDNOTBEHERE"}\n' > "$secret_leak_bundle/audit-logs.json"
expect_rejection "$secret_leak_bundle" 'possible operator token/TOTP secret leak' 'evidence validator rejects token or TOTP leak markers'

missing_status_bundle="$tmp_root/missing-status"
write_ready_bundle "$missing_status_bundle"
write_good_headers "$missing_status_bundle"
rm "$missing_status_bundle/release_readiness.status"
expect_rejection "$missing_status_bundle" 'required file missing or empty: release_readiness.status' 'evidence validator rejects missing required status artifact'

malformed_status_bundle="$tmp_root/malformed-status"
write_ready_bundle "$malformed_status_bundle"
write_good_headers "$malformed_status_bundle"
printf '200 OK\n' > "$malformed_status_bundle/healthz.status"
expect_rejection "$malformed_status_bundle" 'malformed HTTP status: healthz.status=200 OK' 'evidence validator rejects malformed HTTP status artifact'

invalid_json_bundle="$tmp_root/invalid-json"
write_ready_bundle "$invalid_json_bundle"
write_good_headers "$invalid_json_bundle"
printf '<html>not json</html>\n' > "$invalid_json_bundle/healthz.json"
expect_rejection "$invalid_json_bundle" 'invalid JSON payload: healthz.json' 'evidence validator rejects invalid JSON payload'

wrong_content_type_bundle="$tmp_root/wrong-content-type"
write_ready_bundle "$wrong_content_type_bundle"
write_good_headers "$wrong_content_type_bundle"
cat > "$wrong_content_type_bundle/audit_csv.headers" <<'HEADERS'
HTTP/1.1 200 OK
Content-Type: application/json; charset=utf-8

HEADERS
expect_rejection "$wrong_content_type_bundle" 'response header missing or invalid: audit_csv.headers content-type' 'evidence validator rejects wrong audit CSV content type'

missing_manifest_bundle="$tmp_root/missing-manifest-entry"
write_ready_bundle "$missing_manifest_bundle"
write_good_headers "$missing_manifest_bundle"
grep -v ' release_readiness -> ' "$missing_manifest_bundle/manifest.txt" > "$missing_manifest_bundle/manifest.tmp"
mv "$missing_manifest_bundle/manifest.tmp" "$missing_manifest_bundle/manifest.txt"
expect_rejection "$missing_manifest_bundle" 'manifest entry missing or invalid: release_readiness' 'evidence validator rejects missing manifest entry'

manifest_status_mismatch_bundle="$tmp_root/manifest-status-mismatch"
write_ready_bundle "$manifest_status_mismatch_bundle"
write_good_headers "$manifest_status_mismatch_bundle"
printf '201\n' > "$manifest_status_mismatch_bundle/healthz.status"
expect_rejection "$manifest_status_mismatch_bundle" 'manifest status mismatch: healthz' 'evidence validator rejects manifest/status mismatch'
