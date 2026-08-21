#!/bin/sh
set -eu

API_BASE_URL=${API_BASE_URL:-http://127.0.0.1:8080}
COMPOSE_FILE=${COMPOSE_FILE:-deploy/compose/compose.yml}
CANARY_DEADLINE_SECONDS=${CANARY_DEADLINE_SECONDS:-4}
CANARY_TIMEOUT_SECONDS=${CANARY_TIMEOUT_SECONDS:-90}

for command in curl jq docker; do
	if ! command -v "$command" >/dev/null 2>&1; then
		printf 'missing required command: %s\n' "$command" >&2
		exit 1
	fi
done
case "$CANARY_DEADLINE_SECONDS" in
	''|*[!0-9]*|0)
		printf 'CANARY_DEADLINE_SECONDS must be a positive integer\n' >&2
		exit 1
		;;
esac

request_json() {
	method=$1
	url=$2
	token=$3
	body=${4:-}

	set -- -fsS -X "$method" -H 'Content-Type: application/json'
	if [ -n "$token" ]; then
		set -- "$@" -H "Authorization: Bearer $token"
	fi
	if [ -n "$body" ]; then
		set -- "$@" --data "$body"
	fi
	curl "$@" "$url"
}

start_agent_worker() {
	docker compose --env-file .env -f "$COMPOSE_FILE" \
		--profile app --profile agent --profile agent-runtime \
		start agent-worker >/dev/null
}
trap start_agent_worker EXIT INT TERM

docker compose --env-file .env -f "$COMPOSE_FILE" \
	--profile app --profile agent --profile agent-runtime \
	stop agent-worker >/dev/null

unique_suffix="$(date +%s)-$$"
email="agent-timeout-canary-$unique_suffix@example.com"
register_payload="$(jq -nc \
	--arg email "$email" \
	--arg device "agent-timeout-canary-$unique_suffix" \
	'{
		email: $email,
		password: "CanaryPass123!",
		display_name: "Agent Timeout Canary",
		timezone: "Asia/Shanghai",
		locale: "zh-CN",
		device: {device_key: $device, name: "Agent Timeout Canary", platform: "service"}
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
	'{"content":"请详细分析并安排我今天所有生活事项，完成后再回复。"}')"
run_id="$(printf '%s' "$accepted_response" | jq -er '.agent_run.id')"
initial_status="$(printf '%s' "$accepted_response" | jq -er '.agent_run.status')"
if [ "$initial_status" != "accepted" ]; then
	printf 'expected accepted while Agent Worker is stopped, got %s\n' "$initial_status" >&2
	exit 1
fi

case "$run_id" in
????????-????-????-????-????????????) ;;
*)
	printf 'invalid run id\n' >&2
	exit 1
	;;
esac
docker exec ai-companion-postgres-1 psql \
	-U ai_companion -d ai_companion -v ON_ERROR_STOP=1 \
	-c "UPDATE agent.runs SET deadline_at=CURRENT_TIMESTAMP + INTERVAL '$CANARY_DEADLINE_SECONDS seconds' WHERE id='$run_id' AND status='accepted'" \
	>/dev/null

start_agent_worker
trap - EXIT INT TERM

waited=0
max_wait_ticks=$((CANARY_TIMEOUT_SECONDS * 5))
final_status=
while [ "$waited" -lt "$max_wait_ticks" ]; do
	run_response="$(request_json GET "$API_BASE_URL/v1/agent-runs/$run_id" "$token")"
	final_status="$(printf '%s' "$run_response" | jq -er '.status')"
	case "$final_status" in
	timed_out) break ;;
	completed|failed|cancelled)
		printf 'timeout run ended in %s\n' "$final_status" >&2
		exit 1
		;;
	esac
	sleep 0.2
	waited=$((waited + 1))
done
if [ "$final_status" != "timed_out" ]; then
	printf 'run did not reach timed_out within %s seconds\n' "$CANARY_TIMEOUT_SECONDS" >&2
	exit 1
fi

messages_response="$(request_json GET \
	"$API_BASE_URL/v1/conversations/$conversation_id/messages" "$token")"
assistant_messages="$(printf '%s' "$messages_response" |
	jq '[.items[] | select(.role=="assistant")] | length')"
if [ "$assistant_messages" -ne 0 ]; then
	printf 'timed-out run persisted %s assistant messages\n' "$assistant_messages" >&2
	exit 1
fi

event_types="$(docker exec ai-companion-postgres-1 psql \
	-U ai_companion -d ai_companion -Atc \
	"SELECT string_agg(event_type,',' ORDER BY sequence_no) FROM agent.run_events WHERE run_id='$run_id'")"
case ",$event_types," in
*,timed_out,*) ;;
*)
	printf 'timeout event missing: %s\n' "$event_types" >&2
	exit 1
	;;
esac

jq -nc \
	--arg run_id "$run_id" \
	--arg final_status "$final_status" \
	--arg event_types "$event_types" \
	--argjson assistant_messages "$assistant_messages" \
	'{
		run_id: $run_id,
		final_status: $final_status,
		event_types: ($event_types | split(",")),
		assistant_messages: $assistant_messages
	}'
