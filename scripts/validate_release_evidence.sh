#!/bin/sh
set -eu

usage() {
  cat <<'EOF'
Validate an M8 release evidence directory.

Usage:
  scripts/validate_release_evidence.sh <evidence-dir>

The validator checks required files, manifest completeness, HTTP status files,
response headers, JSON payload syntax, release-readiness aggregate status and
required checks, audit CSV headers, metrics presence, and obvious secret leak
patterns.
EOF
}

if [ "${1:-}" = "-h" ] || [ "${1:-}" = "--help" ]; then
  usage
  exit 0
fi

evidence_dir="${1:-${RELEASE_EVIDENCE_DIR:-}}"
if [ -z "$evidence_dir" ]; then
  echo "missing evidence directory" >&2
  usage >&2
  exit 2
fi
if [ ! -d "$evidence_dir" ]; then
  echo "evidence directory not found: $evidence_dir" >&2
  exit 2
fi

failed=0

fail() {
  failed=1
  printf 'FAIL %s\n' "$1" >&2
}

pass() {
  printf 'PASS %s\n' "$1"
}

require_file() {
  path="$evidence_dir/$1"
  if [ ! -s "$path" ]; then
    fail "required file missing or empty: $1"
    return
  fi
  pass "required file present: $1"
}

for file in \
  manifest.txt \
  healthz.json \
  healthz.headers \
  readyz.json \
  meta.json \
  metrics.prom \
  ops-console-bootstrap.json \
  release-readiness.json \
  dead-letter.json \
  kafka-poison-messages.json \
  email-deliveries.json \
  audit-logs.json \
  audit-logs.csv
do
  require_file "$file"
done

for status_file in \
  healthz.status \
  readyz.status \
  meta.status \
  metrics.status \
  ops_console_bootstrap.status \
  release_readiness.status \
  dead_letter.status \
  kafka_poison_messages.status \
  email_deliveries.status \
  audit_logs.status \
  audit_csv.status
do
  require_file "$status_file"
done

require_manifest_entry() {
 name="$1"
  status_file="$evidence_dir/$name.status"
  status="$(cat "$status_file" 2>/dev/null || true)"
  if ! grep -Eq "^[A-Z]+ [^[:space:]]+ $name -> .+ status=[0-9]{3}$" "$evidence_dir/manifest.txt"; then
    fail "manifest entry missing or invalid: $name"
    return
  fi
  if grep -Eq "^[A-Z]+ [^[:space:]]+ $name -> .+ status=$status$" "$evidence_dir/manifest.txt"; then
    pass "manifest entry present: $name"
  else
    fail "manifest status mismatch: $name"
  fi
}

for manifest_entry in \
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
  require_manifest_entry "$manifest_entry"
done

if grep -Eq '^output_dir=.+$' "$evidence_dir/manifest.txt"; then
  pass "manifest output_dir present"
else
  fail "manifest output_dir missing"
fi
if grep -Eq '^completed_at=[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$' "$evidence_dir/manifest.txt"; then
  pass "manifest completed_at present"
else
  fail "manifest completed_at missing or invalid"
fi

for header_file in \
  readyz.headers \
  meta.headers \
  metrics.headers \
  ops_console_bootstrap.headers \
  release_readiness.headers \
  dead_letter.headers \
  kafka_poison_messages.headers \
  email_deliveries.headers \
  audit_logs.headers \
  audit_csv.headers
do
  require_file "$header_file"
done

for status_file in "$evidence_dir"/*.status; do
  [ -e "$status_file" ] || {
    fail "no status files found"
    break
  }
  status="$(cat "$status_file")"
  case "$status" in
    [0-9][0-9][0-9])
      case "$status" in
        2*) pass "2xx status: $(basename "$status_file")=$status" ;;
        *) fail "non-2xx status: $(basename "$status_file")=$status" ;;
      esac
      ;;
    *) fail "malformed HTTP status: $(basename "$status_file")=$status" ;;
  esac
done

require_json() {
  file="$evidence_dir/$1"
  if python3 - "$file" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as handle:
    json.load(handle)
PY
  then
    pass "valid JSON payload: $1"
  else
    fail "invalid JSON payload: $1"
  fi
}

for json_file in \
  healthz.json \
  readyz.json \
  meta.json \
  ops-console-bootstrap.json \
  release-readiness.json \
  dead-letter.json \
  kafka-poison-messages.json \
  email-deliveries.json \
  audit-logs.json
do
  require_json "$json_file"
done

require_header() {
  file="$evidence_dir/$1"
  header="$2"
  pattern="$3"
  if grep -Eiq "^$header:[[:space:]]*$pattern[[:space:]]*\r?$" "$file"; then
    pass "response header valid: $1 $header"
  else
    fail "response header missing or invalid: $1 $header"
  fi
}

for json_header_file in \
  healthz.headers \
  readyz.headers \
  meta.headers \
  ops_console_bootstrap.headers \
  release_readiness.headers \
  dead_letter.headers \
  kafka_poison_messages.headers \
  email_deliveries.headers \
  audit_logs.headers
do
  require_header "$json_header_file" "content-type" "application/json(;.*)?"
done
require_header metrics.headers "content-type" "text/plain(;.*)?"
require_header audit_csv.headers "content-type" "text/csv(;.*)?"

require_header healthz.headers "x-content-type-options" "nosniff"
require_header healthz.headers "x-frame-options" "DENY"
require_header healthz.headers "referrer-policy" "no-referrer"
require_header healthz.headers "cross-origin-opener-policy" "same-origin"
require_header healthz.headers "permissions-policy" "camera=\(\), microphone=\(\), geolocation=\(\)"

if grep -Eq '"status"[[:space:]]*:[[:space:]]*"ready"' "$evidence_dir/release-readiness.json"; then
	pass "release-readiness status is ready"
else
	fail "release-readiness status is not ready"
fi

if python3 - "$evidence_dir/release-readiness.json" <<'PY'
import json
import sys

required = {
    "service_ready",
    "operator_mfa_required",
    "operator_account_store",
    "active_admin_operator",
    "active_mfa_admin_operator",
    "identity_admin_store",
    "operations_store",
    "https_web_origin",
    "security_headers_enabled",
    "model_provider_configured",
}
optional = {"kafka_horizontal_scaling"}

with open(sys.argv[1], "r", encoding="utf-8") as handle:
    payload = json.load(handle)

if payload.get("status") != "ready":
    raise SystemExit("aggregate status is not ready")

checks = payload.get("checks")
if not isinstance(checks, list):
    raise SystemExit("checks is not a list")

by_key = {}
for check in checks:
    if isinstance(check, dict) and isinstance(check.get("key"), str):
        by_key[check["key"]] = check

missing = sorted((required | optional) - by_key.keys())
if missing:
    raise SystemExit("missing checks: " + ", ".join(missing))

not_passed = sorted(key for key in required if by_key[key].get("status") != "passed")
if not_passed:
    raise SystemExit("required checks not passed: " + ", ".join(not_passed))

invalid_optional = sorted(
    key for key in optional if by_key[key].get("status") not in {"passed", "warning"}
)
if invalid_optional:
    raise SystemExit("optional checks invalid: " + ", ".join(invalid_optional))
PY
then
	pass "release-readiness required checks are passed"
else
	fail "release-readiness required checks are missing or not passed"
fi

expected_csv_header='id,occurred_at,actor_type,actor_id,actor_label,action,resource_type,resource_id,trace_id,metadata'
actual_csv_header="$(sed -n '1p' "$evidence_dir/audit-logs.csv" | tr -d '\r')"
if [ "$actual_csv_header" = "$expected_csv_header" ]; then
  pass "audit CSV header is stable"
else
  fail "audit CSV header mismatch: $actual_csv_header"
fi

if grep -q 'ai_companion_http_requests_total' "$evidence_dir/metrics.prom"; then
  pass "metrics include HTTP request counter"
else
  fail "metrics missing ai_companion_http_requests_total"
fi

if grep -RIEq '(Authorization:[[:space:]]*Bearer|X-Operator-TOTP|OPERATOR_ACCOUNT_TOKEN|OPERATOR_TOTP|totp_secret|operator_totp|op_[0-9a-f]{32,})' "$evidence_dir"; then
  fail "possible operator token/TOTP secret leak in evidence directory"
else
  pass "no obvious operator token/TOTP secret leak patterns"
fi

if [ "$failed" -ne 0 ]; then
  echo "release evidence validation failed: $evidence_dir" >&2
  exit 1
fi

echo "release evidence validation passed: $evidence_dir"
