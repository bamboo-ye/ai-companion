#!/bin/sh
set -eu
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" \
  -v app_password="$APP_DB_PASSWORD" -v agent_password="$AGENT_DB_PASSWORD" <<'SQL'
CREATE ROLE hc_app LOGIN PASSWORD :'app_password';
CREATE ROLE hc_agent LOGIN PASSWORD :'agent_password';
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
CREATE SCHEMA agent AUTHORIZATION hc_agent;
GRANT USAGE ON SCHEMA public TO hc_app;
SQL
