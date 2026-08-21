#!/bin/sh
set -eu

METRICS_URL=${AGENT_METRICS_URL:-http://127.0.0.1:9467/metrics}
BROKERS=${KAFKA_BROKERS:-127.0.0.1:9092}
CYCLES=${AGENT_OBSERVABILITY_CANARY_CYCLES:-5}
INTERVAL=${AGENT_OBSERVABILITY_CANARY_INTERVAL:-100ms}
TIMEOUT=${AGENT_OBSERVABILITY_CANARY_TIMEOUT:-30s}
GOCACHE=${GOCACHE:-/tmp/ai-companion-go-cache}
BASELINE=${AGENT_OBSERVABILITY_CANARY_BASELINE:-evals/agent/baselines/observability-canary.v1.json}
JSON_REPORT=${AGENT_OBSERVABILITY_CANARY_JSON_REPORT:-artifacts/agent-eval/observability-canary.json}
JUNIT_REPORT=${AGENT_OBSERVABILITY_CANARY_JUNIT_REPORT:-artifacts/agent-eval/observability-canary.xml}
CANARY_BIN=${AGENT_OBSERVABILITY_CANARY_BIN:-bin/agent-observability-canary}

if [ ! -x "$CANARY_BIN" ]; then
  mkdir -p "$(dirname "$CANARY_BIN")"
  GOCACHE="$GOCACHE" go build -trimpath -o "$CANARY_BIN" ./cmd/agent-observability-canary
fi

set +e
result=$("$CANARY_BIN" \
  --brokers "$BROKERS" \
  --metrics-url "$METRICS_URL" \
  --cycles "$CYCLES" \
  --interval "$INTERVAL" \
  --timeout "$TIMEOUT" \
  --baseline "$BASELINE" \
  --json-report "$JSON_REPORT" \
  --junit-report "$JUNIT_REPORT" 2>&1)
status=$?
set -e

if [ "$status" -ne 0 ]; then
  printf '%s\n' "$result" | sed 's/ canary_run_ids=.*$//'
  echo "agent_observability_canary_report=$JSON_REPORT"
  exit "$status"
fi

run_ids=$(printf '%s\n' "$result" | sed -n 's/.* canary_run_ids=//p' | tr ',' ' ')
if [ -z "$run_ids" ]; then
  echo "agent_observability_canary=failed reason=missing_run_ids" >&2
  exit 1
fi

printf '%s\n' "$result" | sed 's/ canary_run_ids=.*$//'
echo "agent_observability_canary_report=$JSON_REPORT"
