#!/bin/sh
set -eu
read task_last < /backups/.last-success
task_now=$(date -u +%s)
test "$(( task_now - task_last ))" -lt 172800
