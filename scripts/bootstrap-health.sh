#!/bin/sh
# Add isolated development credentials without replacing existing ones.
set -eu
umask 077
env_file=${1:-.env}
if [ ! -f "$env_file" ]; then
  if [ "$env_file" = .env ]; then
    cp .env.example "$env_file"
  else
    echo "Create $env_file from .env.example before running this script." >&2
    exit 1
  fi
fi

for name in HEALTH_ASSISTANT_TOKEN HEALTH_POSTGRES_PASSWORD HEALTH_APP_DB_PASSWORD HEALTH_AGENT_DB_PASSWORD HEALTH_INTERNAL_TOKEN HEALTH_RUN_SIGNING_KEY; do
  if grep -Eq "^[[:space:]]*(export[[:space:]]+)?${name}=" "$env_file"; then
    continue
  fi
  secret=$(openssl rand -hex 32)
  printf '\n%s=%s\n' "$name" "$secret" >> "$env_file"
done
printf 'Health assistant credentials are set in %s.\n' "$env_file"
