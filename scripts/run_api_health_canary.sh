#!/bin/sh
set -eu

API_BASE_URL=${API_BASE_URL:-http://127.0.0.1:8080}

case "$API_BASE_URL" in
http://*|https://*) ;;
*)
	printf '%s\n' 'API_BASE_URL must start with http:// or https://' >&2
	exit 2
	;;
esac

curl -fsS --max-time 10 "${API_BASE_URL%/}/healthz" >/dev/null
curl -fsS --max-time 10 "${API_BASE_URL%/}/readyz" >/dev/null
printf '%s\n' 'api_health_canary=passed'
