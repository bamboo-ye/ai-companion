#!/bin/sh
set -eu

API_BASE_URL=${API_BASE_URL:-http://127.0.0.1:8080}
CANARY_MESSAGE=${CANARY_MESSAGE:-请先仔细分析我的请求，再给我一份今天生活安排的建议。}
CANARY_TIMEOUT_SECONDS=${CANARY_TIMEOUT_SECONDS:-60}

for command in curl jq; do
	if ! command -v "$command" >/dev/null 2>&1; then
		printf 'missing required command: %s\n' "$command" >&2
		exit 1
	fi
done

request_json() {
	method=$1
	url=$2
	token=$3
	body=${4:-}
	idempotency_key=${5:-}

	set -- -fsS -X "$method" -H 'Content-Type: application/json'
	if [ -n "$token" ]; then
		set -- "$@" -H "Authorization: Bearer $token"
	fi
	if [ -n "$idempotency_key" ]; then
		set -- "$@" -H "Idempotency-Key: $idempotency_key"
	fi
	if [ -n "$body" ]; then
		set -- "$@" --data "$body"
	fi
	curl "$@" "$url"
}

unique_suffix="$(date +%s)-$$"
email="agent-control-canary-$unique_suffix@example.com"
register_payload="$(jq -nc \
	--arg email "$email" \
	--arg device "agent-control-canary-$unique_suffix" \
	'{
		email: $email,
		password: "CanaryPass123!",
		display_name: "Agent Control Canary",
		timezone: "Asia/Shanghai",
		locale: "zh-CN",
		device: {device_key: $device, name: "Agent Control Canary", platform: "service"}
	}')"
register_response="$(request_json POST "$API_BASE_URL/v1/auth/register" "" "$register_payload")"
token="$(printf '%s' "$register_response" | jq -er '.access_token')"
character_response="$(request_json POST "$API_BASE_URL/v1/characters" "$token" '{"module":"life"}')"
character_id="$(printf '%s' "$character_response" | jq -er '.character.id')"
conversation_response="$(request_json POST "$API_BASE_URL/v1/conversations" "$token" \
	"$(jq -nc --arg id "$character_id" '{character_id:$id}')")"
conversation_id="$(printf '%s' "$conversation_response" | jq -er '.id')"
accepted_response="$(request_json POST \
	"$API_BASE_URL/v1/conversations/$conversation_id/messages" \
	"$token" \
	"$(jq -nc --arg content "$CANARY_MESSAGE" '{content:$content}')")"
run_id="$(printf '%s' "$accepted_response" | jq -er '.agent_run.id')"

waited=0
saw_running=false
cancel_status=
while [ "$waited" -lt 200 ]; do
	run_response="$(request_json GET "$API_BASE_URL/v1/agent-runs/$run_id" "$token")"
	status="$(printf '%s' "$run_response" | jq -er '.status')"
	case "$status" in
	running)
		saw_running=true
		cancel_response="$(request_json POST \
			"$API_BASE_URL/v1/agent-runs/$run_id/cancel" \
			"$token" "" "$run_id:cancel")"
		cancel_status="$(printf '%s' "$cancel_response" | jq -er '.run.status')"
		break
		;;
	completed|failed|cancelled|timed_out)
		printf 'run became %s before running cancellation was observed\n' "$status" >&2
		exit 1
		;;
	esac
	sleep 0.05
	waited=$((waited + 1))
done

if [ "$saw_running" != true ] || [ "$cancel_status" != "cancel_requested" ]; then
	printf 'running cancellation was not persisted: observed=%s response=%s\n' \
		"$saw_running" "$cancel_status" >&2
	exit 1
fi

waited=0
max_wait_ticks=$((CANARY_TIMEOUT_SECONDS * 10))
final_status=
while [ "$waited" -lt "$max_wait_ticks" ]; do
	run_response="$(request_json GET "$API_BASE_URL/v1/agent-runs/$run_id" "$token")"
	final_status="$(printf '%s' "$run_response" | jq -er '.status')"
	case "$final_status" in
	cancelled) break ;;
	completed|failed|timed_out)
		printf 'cancelled run ended in %s\n' "$final_status" >&2
		exit 1
		;;
	esac
	sleep 0.1
	waited=$((waited + 1))
done
if [ "$final_status" != "cancelled" ]; then
	printf 'run did not finalize cancellation\n' >&2
	exit 1
fi

messages_response="$(request_json GET \
	"$API_BASE_URL/v1/conversations/$conversation_id/messages" "$token")"
assistant_messages="$(printf '%s' "$messages_response" |
	jq '[.items[] | select(.role=="assistant")] | length')"
if [ "$assistant_messages" -ne 0 ]; then
	printf 'cancelled run persisted %s assistant messages\n' "$assistant_messages" >&2
	exit 1
fi

jq -nc \
	--arg run_id "$run_id" \
	--arg final_status "$final_status" \
	--arg cancel_status "$cancel_status" \
	--argjson assistant_messages "$assistant_messages" \
	'{
		run_id: $run_id,
		running_cancel_status: $cancel_status,
		final_status: $final_status,
		assistant_messages: $assistant_messages
	}'
