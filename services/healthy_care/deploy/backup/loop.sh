#!/bin/sh
set -eu
umask 077
task_run_backup() {
  export TASK_BACKUP_NAME="$(date -u +%Y%m%dT%H%M%SZ)-$(cat /proc/sys/kernel/random/uuid)"
  # Keep this connection alive during pg_dump and tar. File deletion takes a
  # shared lock with this key; uploads are immutable, so new extra files are safe.
  psql -X -qAt -v ON_ERROR_STOP=1 <<'SQL'
SELECT pg_advisory_lock(626120261019);
\! /bin/sh /opt/backup/snapshot.sh
\if :SHELL_ERROR
SELECT 1/0;
\endif
SELECT pg_advisory_unlock(626120261019);
SQL
}
while :; do
  if task_run_backup; then
    date -u +%s > /backups/.last-success
    echo "Private snapshot complete: $TASK_BACKUP_NAME"
  else
    echo 'Private snapshot failed; no successful completion recorded.' >&2
    if [ "${BACKUP_ONCE:-false}" = 'true' ]; then exit 1; fi
    sleep 60
    continue
  fi
  if [ "${BACKUP_ONCE:-false}" = 'true' ]; then exit 0; fi
  # 18:00 UTC is 02:00 the following day in Asia/Shanghai.
  task_now=$(date -u +%s)
  task_next=$(( task_now / 86400 * 86400 + 64800 ))
  if [ "$task_next" -le "$task_now" ]; then task_next=$(( task_next + 86400 )); fi
  sleep "$(( task_next - task_now ))"
done
