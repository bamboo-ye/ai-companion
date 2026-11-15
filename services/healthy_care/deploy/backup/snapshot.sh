#!/bin/sh
set -eu
umask 077
task_tmp="/backups/.partial-$TASK_BACKUP_NAME"
mkdir "$task_tmp"
pg_dump -Fc --file="$task_tmp/database.dump"
tar -C /data/files -czf "$task_tmp/attachments.tar.gz" .
task_db_hash=$(sha256sum "$task_tmp/database.dump" | cut -d' ' -f1)
task_file_hash=$(sha256sum "$task_tmp/attachments.tar.gz" | cut -d' ' -f1)
printf '{"version":1,"created_at":"%s","files":{"database.dump":"%s","attachments.tar.gz":"%s"}}\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$task_db_hash" "$task_file_hash" > "$task_tmp/manifest.json"
mv "$task_tmp" "/backups/$TASK_BACKUP_NAME"
task_keep=${BACKUP_KEEP_DAYS:-7}
case "$task_keep" in ''|*[!0-9]*) echo 'Invalid BACKUP_KEEP_DAYS' >&2;exit 1;; esac
if [ "$task_keep" -gt 0 ]; then
  find /backups -mindepth 1 -maxdepth 1 -type d -name '20*' -mtime "+$task_keep" -exec rm -rf -- {} +
fi
