# 伴AI

伴AI is a conversational personal AI workspace spanning emotional companionship, life assistance, work skills, collaboration, billing/safety controls, and operator-run internal-release workflows.

Current completion scope and verification are recorded in [`docs/PROJECT_COMPLETION.md`](docs/PROJECT_COMPLETION.md).

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

Run the backend/internal-release gate used for the completed M0-M8 scope:

```bash
make release-check
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

## Current completion boundary

M0-M4, M6, and the M8 Web/backend platform scope are complete. M5 financial research and M7 native Android/iOS client expansion are skipped by explicit product decision for this completion pass.

The completed scope includes persistent accounts, versioned personas, reliable streaming chat, memory/RAG, life-assistant ledger and reminders, Skill/office tools, Kafka-based asynchronous execution, reliability/degradation controls, team workspaces, email delivery/replay operations, billing quota guards, minor-mode safety gating, operator MFA/RBAC/admin APIs, audit CSV export, release-readiness checks, and automated release evidence collection/validation.

See [`docs/PROJECT_COMPLETION.md`](docs/PROJECT_COMPLETION.md), [`docs/M6_STATUS.md`](docs/M6_STATUS.md), and [`docs/M8_STATUS.md`](docs/M8_STATUS.md) for acceptance evidence and remaining deployment-only prerequisites.
