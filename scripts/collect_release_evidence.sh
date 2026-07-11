#!/bin/sh
set -eu

usage() {
  cat <<'EOF'
Collect M8 release evidence from a running API.

Required environment:
  API_BASE_URL                 e.g. https://api.internal.example.com
  OPERATOR_ACCOUNT_TOKEN        operator account token; not written to output
  OPERATOR_TOTP                 current six-digit operator TOTP; not written to output

Optional environment:
  RELEASE_EVIDENCE_DIR          output directory; defaults to .release-evidence/<timestamp>
  RELEASE_AUDIT_LIMIT           audit CSV row cap; defaults to 20

Example:
  API_BASE_URL=https://api.example.com \
  OPERATOR_ACCOUNT_TOKEN=... \
  OPERATOR_TOTP=123456 \
  scripts/collect_release_evidence.sh
EOF
}

if [ "${1:-}" = "-h" ] || [ "${1:-}" = "--help" ]; then
  usage
  exit 0
fi

require_env() {
  name="$1"
  eval "value=\${$name:-}"
  if [ -z "$value" ]; then
    echo "missing required environment: $name" >&2
    usage >&2
    exit 2
  fi
}

require_env API_BASE_URL
require_env OPERATOR_ACCOUNT_TOKEN
require_env OPERATOR_TOTP

case "$API_BASE_URL" in
  http://*|https://*) ;;
  *)
    echo "API_BASE_URL must start with http:// or https://" >&2
    exit 2
    ;;
esac

base_url="${API_BASE_URL%/}"
timestamp="$(date -u '+%Y%m%dT%H%M%SZ')"
output_dir="${RELEASE_EVIDENCE_DIR:-.release-evidence/$timestamp}"
audit_limit="${RELEASE_AUDIT_LIMIT:-20}"

umask 077
mkdir -p "$output_dir"
manifest="$output_dir/manifest.txt"
: > "$manifest"

record() {
  name="$1"
  method="$2"
  path="$3"
  destination="$4"
  auth="$5"
  tmp_headers="$output_dir/$name.headers"
  status_file="$output_dir/$name.status"
  if [ "$auth" = "operator" ]; then
    status="$(
      curl -sS -X "$method" \
        -H "Authorization: Bearer $OPERATOR_ACCOUNT_TOKEN" \
        -H "X-Operator-TOTP: $OPERATOR_TOTP" \
        -D "$tmp_headers" \
        -o "$destination" \
        -w '%{http_code}' \
        "$base_url$path"
    )"
  else
    status="$(
      curl -sS -X "$method" \
        -D "$tmp_headers" \
        -o "$destination" \
        -w '%{http_code}' \
        "$base_url$path"
    )"
  fi
  printf '%s\n' "$status" > "$status_file"
  printf '%s %s %s -> %s status=%s\n' "$method" "$path" "$name" "$destination" "$status" >> "$manifest"
}

record "healthz" "GET" "/healthz" "$output_dir/healthz.json" "none"
record "readyz" "GET" "/readyz" "$output_dir/readyz.json" "none"
record "meta" "GET" "/v1/meta" "$output_dir/meta.json" "none"
record "metrics" "GET" "/metrics" "$output_dir/metrics.prom" "none"
record "ops_console_bootstrap" "GET" "/v1/ops/console/bootstrap" "$output_dir/ops-console-bootstrap.json" "operator"
record "release_readiness" "GET" "/v1/ops/release-readiness" "$output_dir/release-readiness.json" "operator"
record "dead_letter" "GET" "/v1/ops/outbox/dead-letter?limit=20" "$output_dir/dead-letter.json" "operator"
record "kafka_poison_messages" "GET" "/v1/ops/kafka/poison-messages?limit=20" "$output_dir/kafka-poison-messages.json" "operator"
record "email_deliveries" "GET" "/v1/ops/email/deliveries?limit=20" "$output_dir/email-deliveries.json" "operator"
record "audit_logs" "GET" "/v1/ops/audit-logs?limit=20" "$output_dir/audit-logs.json" "operator"
record "audit_csv" "GET" "/v1/ops/audit-logs/export?format=csv&limit=$audit_limit" "$output_dir/audit-logs.csv" "operator"

failed=0
for status_file in "$output_dir"/*.status; do
  status="$(cat "$status_file")"
  case "$status" in
    2*) ;;
    *)
      failed=1
      printf 'non-2xx response: %s status=%s\n' "$status_file" "$status" >> "$manifest"
      ;;
  esac
done

printf 'output_dir=%s\n' "$output_dir" >> "$manifest"
printf 'completed_at=%s\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" >> "$manifest"

if [ "$failed" -ne 0 ]; then
  echo "release evidence collected with non-2xx responses: $output_dir" >&2
  exit 1
fi

echo "release evidence collected: $output_dir"
