#!/bin/sh
set -eu

API_BASE_URL=${API_BASE_URL:-http://127.0.0.1:8080}
CANARY_MESSAGE=${CANARY_MESSAGE:-今天午饭花了50元}
CANARY_APPROVE=${CANARY_APPROVE:-true}
CANARY_TIMEOUT_SECONDS=${CANARY_TIMEOUT_SECONDS:-180}

for command in curl jq; do
	if ! command -v "$command" >/dev/null 2>&1; then
		printf 'missing required command: %s\n' "$command" >&2
		exit 1
	fi
done

case "$CANARY_APPROVE" in
	true|false) ;;
	*)
		printf 'CANARY_APPROVE must be true or false\n' >&2
		exit 1
		;;
esac

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
email="agent-life-canary-$unique_suffix@example.com"
device_key="agent-life-canary-$unique_suffix"

register_payload="$(jq -nc \
	--arg email "$email" \
	--arg device "$device_key" \
	'{
		email: $email,
		password: "CanaryPass123!",
		display_name: "Agent Life Canary",
		timezone: "Asia/Shanghai",
		locale: "zh-CN",
		device: {device_key: $device, name: "Agent Life Canary", platform: "service"}
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
runtime="$(printf '%s' "$accepted_response" | jq -er '.runtime')"
run_id="$(printf '%s' "$accepted_response" | jq -er '.agent_run.id')"
if [ "$runtime" != "agent" ] || printf '%s' "$accepted_response" | jq -e 'has("job")' >/dev/null; then
	printf 'life canary was not routed exclusively to Agent runtime\n' >&2
	exit 1
fi

waited=0
resolution_submitted=false
approval_summary=
final_status=
while [ "$waited" -lt "$CANARY_TIMEOUT_SECONDS" ]; do
	run_response="$(request_json GET "$API_BASE_URL/v1/agent-runs/$run_id" "$token")"
	status="$(printf '%s' "$run_response" | jq -er '.status')"
	case "$status" in
	waiting_approval)
		approval_summary="$(printf '%s' "$run_response" | jq -er '.output.interrupts[0].summary')"
		if printf '%s' "$run_response" | jq -e '.. | objects | has("confirmation_token")' >/dev/null; then
			printf 'public Agent Run response exposed confirmation_token\n' >&2
			exit 1
		fi
		if [ "$resolution_submitted" = false ]; then
			revision="$(printf '%s' "$run_response" | jq -er '.revision')"
			resolution_key="$run_id:$revision"
			request_json POST \
				"$API_BASE_URL/v1/agent-runs/$run_id/resolve" \
				"$token" \
				"$(jq -nc --argjson approved "$CANARY_APPROVE" '{approved:$approved}')" \
				"$resolution_key" >/dev/null
			resolution_submitted=true
		fi
		;;
	completed)
		final_status=$status
		break
		;;
	failed|cancelled)
		printf 'Agent Run ended in %s: %s\n' \
			"$status" \
			"$(printf '%s' "$run_response" | jq -r '.error_message // "unknown error"')" >&2
		exit 1
		;;
	esac
	sleep 1
	waited=$((waited + 1))
done

if [ "$final_status" != "completed" ]; then
	printf 'Agent Run did not complete within %s seconds\n' "$CANARY_TIMEOUT_SECONDS" >&2
	exit 1
fi

messages_response="$(request_json GET \
	"$API_BASE_URL/v1/conversations/$conversation_id/messages" \
	"$token")"
assistant_messages="$(printf '%s' "$messages_response" | jq '[.items[] | select(.role=="assistant")] | length')"
if [ "$assistant_messages" -lt 1 ]; then
	printf 'Agent Run completed without an assistant message\n' >&2
	exit 1
fi

ledger_matches=0
if [ "$CANARY_APPROVE" = true ]; then
	ledger_response="$(request_json GET "$API_BASE_URL/v1/ledger/entries?limit=20" "$token")"
	ledger_matches="$(printf '%s' "$ledger_response" |
		jq '[.items[] | select(.direction=="expense" and .amount_minor==5000)] | length')"
	if [ "$ledger_matches" -ne 1 ]; then
		printf 'expected exactly one CNY 50 expense, got %s\n' "$ledger_matches" >&2
		exit 1
	fi
fi

jq -nc \
	--arg email "$email" \
	--arg run_id "$run_id" \
	--arg summary "$approval_summary" \
	--arg status "$final_status" \
	--argjson approved "$CANARY_APPROVE" \
	--argjson assistant_messages "$assistant_messages" \
	--argjson ledger_matches "$ledger_matches" \
	'{
		email: $email,
		run_id: $run_id,
		approval_summary: $summary,
		approved: $approved,
		final_status: $status,
		assistant_messages: $assistant_messages,
		ledger_matches: $ledger_matches
	}'
