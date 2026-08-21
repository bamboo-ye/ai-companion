#!/bin/sh
set -u

DOCKER_BIN=${DOCKER_BIN:-docker}
CURL_BIN=${CURL_BIN:-curl}
COMPOSE_PROJECT=${COMPOSE_PROJECT:-ai-companion}
HEALTH_STATE_DIR=${HEALTH_STATE_DIR:-/var/lib/ai-companion-monitor}
LOCAL_READY_URL=${LOCAL_READY_URL:-http://127.0.0.1:8080/readyz}
PUBLIC_READY_URL=${PUBLIC_READY_URL:-https://or.windcry1.com/readyz}
CPU_WARN_PERCENT=${CPU_WARN_PERCENT:-80}
HEALTH_CHECK_SERVICES=${HEALTH_CHECK_SERVICES:-"postgres redis kafka qdrant minio api worker agent-worker web"}

mkdir -p "$HEALTH_STATE_DIR"
report=$(mktemp "$HEALTH_STATE_DIR/latest.XXXXXX")
trap 'rm -f "$report"' EXIT HUP INT TERM
overall=healthy

record_failure() {
	overall=unhealthy
	printf 'failure=%s\n' "$1" >>"$report"
}

printf 'checked_at=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" >"$report"
printf 'project=%s\n' "$COMPOSE_PROJECT" >>"$report"

if ! "$DOCKER_BIN" info >/dev/null 2>&1; then
	record_failure docker_unavailable
else
	container_ids=$(
		"$DOCKER_BIN" ps -aq \
			--filter "label=com.docker.compose.project=$COMPOSE_PROJECT"
	)
	if [ -z "$container_ids" ]; then
		record_failure no_project_containers
		inspect_output=
	else
		# One inspect call covers the complete project; one-shot migration containers
		# are ignored below because only long-running services are required.
		inspect_output=$(
			"$DOCKER_BIN" inspect \
				--format '{{index .Config.Labels "com.docker.compose.service"}}|{{.State.Status}}|{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}|{{.RestartCount}}|{{.HostConfig.RestartPolicy.Name}}|{{.Name}}' \
				$container_ids 2>/dev/null
		)
	fi

	for service in $HEALTH_CHECK_SERVICES; do
		line=$(printf '%s\n' "$inspect_output" | awk -F '|' -v wanted="$service" '$1 == wanted { print; exit }')
		if [ -z "$line" ]; then
			record_failure "service_missing:$service"
			continue
		fi
		state=$(printf '%s\n' "$line" | awk -F '|' '{ print $2 }')
		health=$(printf '%s\n' "$line" | awk -F '|' '{ print $3 }')
		restarts=$(printf '%s\n' "$line" | awk -F '|' '{ print $4 }')
		restart_policy=$(printf '%s\n' "$line" | awk -F '|' '{ print $5 }')
		printf 'service=%s state=%s health=%s restarts=%s restart_policy=%s\n' "$service" "$state" "$health" "$restarts" "$restart_policy" >>"$report"
		if [ "$state" != running ]; then
			record_failure "service_not_running:$service"
		elif [ "$health" != none ] && [ "$health" != healthy ]; then
			record_failure "service_not_healthy:$service:$health"
		fi
		if [ "$restart_policy" != unless-stopped ]; then
			record_failure "restart_policy_mismatch:$service:$restart_policy"
		fi
	done

	running_ids=$(
		"$DOCKER_BIN" ps -q \
			--filter "label=com.docker.compose.project=$COMPOSE_PROJECT"
	)
	if [ -n "$running_ids" ]; then
		"$DOCKER_BIN" stats --no-stream \
			--format 'resource={{.Name}} cpu={{.CPUPerc}} memory={{.MemUsage}}' \
			$running_ids >>"$report" 2>&1 || record_failure docker_stats_failed
	fi
fi

if "$CURL_BIN" -fsS --max-time 5 "$LOCAL_READY_URL" >/dev/null; then
	printf 'endpoint=local_ready status=ok\n' >>"$report"
else
	record_failure local_ready_failed
fi

if "$CURL_BIN" -fsS --max-time 10 "$PUBLIC_READY_URL" >/dev/null; then
	printf 'endpoint=public_ready status=ok\n' >>"$report"
else
	record_failure public_ready_failed
fi

if awk -v threshold="$CPU_WARN_PERCENT" '
	BEGIN { found = 0 }
	/^resource=/ {
		for (i = 1; i <= NF; i++) {
			if ($i ~ /^cpu=/) {
				value = $i
				sub(/^cpu=/, "", value)
				sub(/%$/, "", value)
				if ((value + 0) >= threshold) found = 1
			}
		}
	}
	END { exit found ? 0 : 1 }
' "$report"; then
	printf 'warning=container_cpu_at_or_above_%s_percent\n' "$CPU_WARN_PERCENT" >>"$report"
fi

printf 'overall=%s\n' "$overall" >>"$report"
cat "$report"
mv "$report" "$HEALTH_STATE_DIR/latest.txt"
trap - EXIT HUP INT TERM

if [ "$overall" != healthy ]; then
	exit 1
fi
