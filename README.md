# 伴AI

伴AI is a conversational personal AI workspace spanning emotional companionship, life assistance, and work skills. M1 now provides persistent accounts, versioned personas, and reliable streaming chat.

Current milestone verification is recorded in [`docs/M1_STATUS.md`](docs/M1_STATUS.md).

## Repository map

```text
cmd/api, cmd/worker       Go API and asynchronous worker processes
internal/                 Go platform and future domain modules
workers/python/           Isolated document/data/media algorithm worker
web/                      Next.js web client
mobile/android/           Kotlin + Jetpack Compose Android client
mobile/ios/               SwiftUI app source and testable Swift core
deploy/                   Docker Compose and service images
api/openapi/              HTTP contract
events/schemas/           Event contract
migrations/               Forward and rollback SQL migrations
docs/adr/                  Architecture decisions
```

## Prerequisites

- Go 1.26+
- Python 3.12+
- Node.js 24+ and pnpm 11+
- Docker Desktop with Compose v2
- Android: JDK 17, Android Studio with API 37, Gradle 9.4.1
- iOS: Xcode 26+ and XcodeGen

## Quick start

```bash
cp .env.example .env
# Replace every change-* secret before exposing services beyond localhost.
make infra-up
make migrate
make run-api
```

Long-running document and Office tasks require the durable worker in another terminal:

```bash
make run-worker
```

Production asynchronous delivery uses Kafka through the transactional Outbox Relay. Chat generation, memory extraction, document ingestion/cleanup, Skill execution, ledger export, and notification delivery use isolated consumer groups; MySQL leases remain recovery state rather than the primary production queue. Local `make infra-up` starts Kafka and the one-shot fixed-topic initializer. See [`docs/adr/0004-kafka-asynchronous-transport.md`](docs/adr/0004-kafka-asynchronous-transport.md).

In another terminal:

```bash
curl http://localhost:8080/healthz
curl http://localhost:8080/readyz
curl http://localhost:8080/v1/meta
```

Run the locally available quality gate:

```bash
make check
```

Run the M2 retrieval quality gate:

```bash
make eval-m2
```

Run the M4 deterministic intent-routing quality gate:

```bash
make eval-m4
```

Start the web client after installing dependencies:

```bash
cd web
pnpm install
pnpm dev
```

## Platform notes

- `MODEL_PROVIDER=development` is deterministic and local. Use `openai-compatible` only after provider, region, privacy, and cost approval; configure the base URL, model, secret, timeout, and per-million-token costs.
- Local Compose credentials are development-only and come from `.env`; production secrets must use a secret manager.
- The iOS app project is generated from `mobile/ios/project.yml` using XcodeGen. `swift test --package-path mobile/ios` tests its core without full Xcode.
- The Android project is pinned to AGP 9.2, Gradle 9.4.1, Kotlin 2.4, API 37, and the stable June 2026 Compose BOM.
- System reminder writes are never silent: users authorize and confirm them. The app remains the durable source if a platform permission is denied or revoked.

## Current milestone boundary

M1, M2, and M3 are complete. M2 provides controllable long-term memory with date decay, token-budgeted recent context, durable rolling summaries, authenticated PDF/text intake, page-level pypdf extraction, structural chunks, leased ingest jobs, Qdrant dense/sparse RRF retrieval, page citations, evidence-insufficient behavior, deletion, the Web document library, and a checked-in 100-case quality gate. The deterministic hashing embedding remains an offline baseline; provider-specific embeddings, OCR/visual fallback, MinIO, and reliability automation are Beta/production-hardening work.

M3 adds safe ledger and reminder candidates from chat, explicit idempotent confirmation, ledger CRUD/monthly summaries/Excel export, daily plans, timezone and DST-safe reminders, durable in-app notification records, explicit system-sync states, iOS EventKit and Android Calendar/Alarm adapters, and the Web life-assistant workspace. Platform permission denial never removes the application reminder or masquerades as a successful system write. See [`docs/M3_STATUS.md`](docs/M3_STATUS.md) for acceptance evidence and later production-hardening boundaries.

M4 is complete. It provides a bounded, versioned Skill/Tool Runtime, seven working office Skills, per-user controls, deterministic intent routing, and an MCP 2025-11-25 stdio client guarded by operator-owned server and tool allowlists. Long-running Office work uses a durable MySQL queue with renewable leases, expired-lease takeover, and revision fencing against stale Worker commits. Routing only suggests an action; it never executes one. Risk-aware confirmation, authenticated generated files, and the isolated Python Office process preserve the rule that source files are never overwritten. Acceptance evidence and current limits are recorded in [`docs/M4_STATUS.md`](docs/M4_STATUS.md).

M5 financial research is skipped by explicit product decision. M6 is in progress; it now covers trace propagation, Prometheus metrics, real queue/model signal sampling, a hysteretic L0-L3 reliability control plane, Kafka async recovery, operator DLQ replay, model circuit breakers, internal-release alerts/runbooks, and Web PWA packaging. See [`docs/M6_STATUS.md`](docs/M6_STATUS.md).
