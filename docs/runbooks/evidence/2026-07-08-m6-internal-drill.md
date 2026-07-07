# 2026-07-08 M6 final internal recovery, fault, and load drill evidence

Date/time: 2026-07-08 Asia/Shanghai

Scope: local final internal Docker environment for M6 acceptance:

- MySQL: `ai-companion-mysql-1` (`mysql:8.4.10`)
- Kafka: `ai-companion-kafka-1` (`apache/kafka:4.3.0`)
- Redis: `ai-companion-redis-1` (`redis:8.8.0-alpine`)
- Qdrant: `ai-companion-qdrant-1` (`qdrant/qdrant:v1.18.0`)
- API/Worker: current workspace code in `/Users/windcry1/Documents/ai_companion`

Notes:

- The workspace is a staging tree (`git status` reported `No commits yet on master` with project files untracked).
- The existing default database contained historical failed/accepted jobs and historical Kafka topic records. The drill first captured those findings, then continued on an isolated database `ai_companion_drill_20260708` using the same Docker MySQL/Kafka/Redis/Qdrant services.
- No external model provider was used; `MODEL_PROVIDER=development` was used to avoid network and model spend during the internal drill.

## 1. Baseline environment

Docker dependencies were running before the drill:

```text
ai-companion-kafka-1    apache/kafka:4.3.0     Up 32 hours (healthy)    0.0.0.0:9092->9092/tcp
ai-companion-qdrant-1   qdrant/qdrant:v1.18.0  Up 5 days               0.0.0.0:6333-6334->6333-6334/tcp
ai-companion-redis-1    redis:8.8.0-alpine     Up 6 days (healthy)     0.0.0.0:6379->6379/tcp
ai-companion-mysql-1    mysql:8.4.10           Up 6 days (healthy)     0.0.0.0:3306->3306/tcp, 33060/tcp
```

Default database baseline:

```text
schema_migrations: count=15, min=000001, max=000015
outbox_events: 8
compensation_records: 0
```

## 2. MySQL backup and restore drill

Initial dump command without `--no-tablespaces` failed because the application user did not have `PROCESS` privilege. The drill was retried with `--no-tablespaces`.

Backup artifact inside the MySQL container:

```text
/tmp/ai_companion_restore_drill.sql
size: 104K
lines: 1389
```

Restore target:

```text
database: ai_companion_restore_drill
charset/collation: utf8mb4 / utf8mb4_0900_ai_ci
```

Migration verification against restored DB:

```text
skip 000001_platform_foundation.up.sql
skip 000002_identity_character_chat.up.sql
skip 000003_generation_events.up.sql
skip 000004_long_term_memory.up.sql
skip 000005_document_intake.up.sql
skip 000006_document_parse_and_chunks.up.sql
skip 000007_conversation_context.up.sql
skip 000008_ledger.up.sql
skip 000009_plans_and_reminders.up.sql
skip 000010_skill_runtime.up.sql
skip 000011_skill_user_settings.up.sql
skip 000012_skill_worker_leases.up.sql
skip 000013_kafka_outbox_relay.up.sql
skip 000014_async_algorithm_services.up.sql
skip 000015_operator_dlq_compensation.up.sql
```

Restored database table checks:

```text
schema_migrations: count=15, min=000001, max=000015
users: 8
generation_jobs: 6
outbox_events: 8
document_ingest_jobs: 6
skill_runs: 0
compensation_records: 0
```

Result: passed. A backup generated from the current DB can be restored to an isolated DB and accepted by the current migration binary.

## 3. Default database findings

API and Worker were first started against the default internal database.

Health checks:

```text
GET /healthz => {"status":"ok"}
GET /readyz => {"status":"ready"}
GET /v1/meta => {"build":{"version":"dev","commit":"unknown","date":"unknown"},"environment":"development","service":"ai-companion-api"}
```

The first Worker startup failed:

```text
durable workers ready
durable worker failed: Kafka ledger consumer: Error 1411 (HY000): Incorrect string value: '' for function uuid_to_bin
```

Follow-up analysis:

- The error shape matches `UUID_TO_BIN(<non-uuid>)`.
- Historical Kafka records from earlier tests can poison a consumer group that starts from the beginning.
- Resetting the internal consumer group offsets to latest allowed the Worker to start without clearing Kafka topics.

The default DB also entered L3 because of historical failed/accepted generation jobs:

```text
ai_companion_degradation_level 3
ai_companion_queue_lag 0
ai_companion_model_error_ratio 1.000000
```

Result: default internal DB is not suitable as a clean final acceptance baseline without operational cleanup. This is a real drill finding.

## 4. Isolated final drill database setup

Created isolated DB:

```text
database: ai_companion_drill_20260708
```

Applied migrations:

```text
applied 000001_platform_foundation.up.sql
applied 000002_identity_character_chat.up.sql
applied 000003_generation_events.up.sql
applied 000004_long_term_memory.up.sql
applied 000005_document_intake.up.sql
applied 000006_document_parse_and_chunks.up.sql
applied 000007_conversation_context.up.sql
applied 000008_ledger.up.sql
applied 000009_plans_and_reminders.up.sql
applied 000010_skill_runtime.up.sql
applied 000011_skill_user_settings.up.sql
applied 000012_skill_worker_leases.up.sql
applied 000013_kafka_outbox_relay.up.sql
applied 000014_async_algorithm_services.up.sql
applied 000015_operator_dlq_compensation.up.sql
```

API/Worker startup:

```text
API: api listening address=:8080 environment=development
API: degradation level=L0 reason="signals healthy" queue_lag=0
Worker: durable workers ready
```

Initial isolated metrics:

```text
ai_companion_degradation_level 0
ai_companion_queue_lag 0
ai_companion_model_error_ratio 0.000000
```

## 5. API + Kafka + Worker smoke drill

Flow:

1. Register user.
2. Create character.
3. Create conversation.
4. Send chat message.
5. Poll generation job.
6. Fetch messages.

Evidence:

```text
run_id=20260708023810
http_status=202
user_created=true
character_id=a681d12e-4185-4b2b-8329-e73ef2544671
conversation_id=a58396cd-3e21-47f3-9925-fb5b1a04e928
job_id=fe8920fe-2a98-4ca8-b416-b85e89c85803
final_status=completed
message_count=4
```

Result: passed.

## 6. Worker stop and takeover recovery drill

Fault injected: stopped Worker while API stayed online.

During Worker downtime:

```text
run_id=20260708023846
http_status=202
conversation_id=60cc01f1-c7e2-4ef1-be8f-a8c085476d21
job_id=e92c8c0d-1468-46fe-aa0a-3e56334a56df
status_while_worker_down=accepted
recent_outbox=chat.command.v1:pending,memory.extract.v1:pending
```

After Worker restart:

```text
job_id=e92c8c0d-1468-46fe-aa0a-3e56334a56df
final_after_worker_restart=completed
recent_outbox=memory.extract.v1:published,chat.command.v1:published,memory.extract.v1:published,chat.command.v1:published
inbox=ai-companion-background-v1-chat:2,ai-companion-background-v1-memory:2
```

Result: passed. Accepted jobs and pending outbox events survived Worker downtime and completed after Worker restart.

## 7. Kafka outage and recovery drill

Fault injected:

```text
docker stop ai-companion-kafka-1
```

During Kafka outage:

```text
run_id=20260708024007
http_status=202
conversation_id=50642ed0-5459-4f0b-b321-7961c9ff640d
job_id=f4569cb6-7f6e-4ce1-92ed-f92179f229bb
status_while_kafka_down=accepted
recent_outbox=chat.command.v1:pending,memory.extract.v1:pending
```

Worker observed the broker outage and exited:

```text
durable worker failed: Kafka notification consumer: unable to join group session: unable to dial: dial tcp [::1]:9092: connect: connection refused
```

Kafka recovery:

```text
docker start ai-companion-kafka-1
docker inspect => running/healthy
```

After Kafka and Worker recovery:

```text
job_id=f4569cb6-7f6e-4ce1-92ed-f92179f229bb
final_after_kafka_recovery=completed
recent_outbox=chat.command.v1:published,memory.extract.v1:published,memory.extract.v1:published,chat.command.v1:published
inbox=ai-companion-background-v1-chat:2,ai-companion-background-v1-memory:3
```

Result: passed with operational caveat. API accepted and persisted work while Kafka was down; after Kafka and Worker restart, pending work completed. Worker currently exits on Kafka broker outage, so process supervision/restart is required.

## 8. Load drill

An initial 20-message run was submitted successfully but the shell statistics parser failed due an unsafe awk quoting mistake. The DB showed those 20 jobs completed:

```text
completed 20
```

The official counted run used a safer parser:

```text
run_id=20260708024229
start_utc=2026-07-07 18:42:29
submitted=20
accepted=20
completed=20
statuses=completed 20
metrics=ai_companion_degradation_level=0,ai_companion_queue_lag=5,ai_companion_model_error_ratio=0.000000
```

Result: passed. 20 concurrent chat requests were accepted and completed. The queue lag briefly reached 5 and drained afterwards.

## 9. Document parser, chunking, Qdrant indexing, and retrieval drill

Flow:

1. Register user.
2. Upload a small UTF-8 TXT file via `POST /v1/documents`.
3. Worker consumes `document.ingest.v1`.
4. Python parser returns page/chunk data.
5. Worker upserts to Qdrant.
6. Query document retrieval API.

Evidence:

```text
run_id=20260708024321
upload_status=202
document_id=617f22d2-a87d-4603-a5c7-0b18a17739f0
final_doc_status=ready
db_doc=ready 1 1
query_sufficient=true
citation_count=1
qdrant=ok:1
```

Result: passed.

## 10. Final state

Final Docker dependency state:

```text
ai-companion-kafka-1    apache/kafka:4.3.0     Up 3 minutes (healthy)   0.0.0.0:9092->9092/tcp
ai-companion-qdrant-1   qdrant/qdrant:v1.18.0  Up 5 days               0.0.0.0:6333-6334->6333-6334/tcp
ai-companion-redis-1    redis:8.8.0-alpine     Up 6 days (healthy)     0.0.0.0:6379->6379/tcp
ai-companion-mysql-1    mysql:8.4.10           Up 6 days (healthy)     0.0.0.0:3306->3306/tcp, 33060/tcp
```

Final API metrics:

```text
ai_companion_degradation_level 0
ai_companion_queue_lag 0
ai_companion_model_error_ratio 0.000000
```

Final isolated drill DB counts:

```text
generation_jobs:
completed 43

outbox_events:
chat.command.v1 published 43
document.ingest.v1 published 1
memory.extract.v1 published 43

inbox_events:
ai-companion-background-v1-chat 43
ai-companion-background-v1-document 1
ai-companion-background-v1-memory 43

documents:
ready 1 page_count_sum=1 chunk_count_sum=1
```

## 11. Findings and follow-ups

Passed:

- MySQL backup restore and migration compatibility.
- Clean API/Worker/Kafka chat path.
- Worker downtime durable recovery.
- Kafka outage durable recovery after broker and Worker restart.
- 20-message concurrent load drill.
- Document parse/chunk/Qdrant retrieval path.

Operational findings:

1. Historical Kafka topic records with non-UUID event IDs can crash a consumer writing to the MySQL inbox table.
   - Workaround used in drill: reset internal consumer group offsets to latest.
   - Recommended fix: make Kafka consumer poison-message handling explicit, validate UUID event IDs before processing, and commit/route invalid envelopes to an operator-visible DLQ instead of exiting the whole Worker.
2. Worker exits when Kafka is unavailable.
   - Recovery passed after restart, but the final environment needs supervisor restart policy and alerting.
3. The default internal DB was not a clean acceptance baseline because old failed/accepted jobs pushed reliability to L3.
   - Recommended fix: define a pre-drill cleanup/reset procedure or run final acceptance on an isolated DB snapshot.

