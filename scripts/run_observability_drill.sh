#!/bin/sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
COMPOSE_FILE="$ROOT_DIR/deploy/observability/compose.drill.yml"
PROJECT_NAME=${OBSERVABILITY_DRILL_PROJECT:-ai-companion-observability-drill}

compose() {
  docker compose --project-name "$PROJECT_NAME" -f "$COMPOSE_FILE" "$@"
}

cleanup() {
  compose down --volumes --remove-orphans >/dev/null 2>&1 || true
}

trap cleanup EXIT HUP INT TERM
cleanup

compose config --quiet
compose up -d

compose exec -T prometheus /bin/promtool check config /etc/prometheus/prometheus.yml
compose exec -T prometheus /bin/promtool check rules /etc/prometheus/rules/prometheus-rules.yml
compose exec -T alertmanager /bin/amtool check-config /etc/alertmanager/alertmanager.yml
compose exec -T drill-sink python -u /opt/drill/observability_drill.py verify

printf '%s\n' 'observability_drill=passed'
