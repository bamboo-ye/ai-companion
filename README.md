<p align="center">
  <img src="web/public/icons/logo.png" width="128" alt="AI Companion mascot">
</p>

# 伴AI · AI Companion

<p align="center"><strong>Someone to talk to. A hand with life. A partner for getting things done.</strong></p>
<p align="center">Companionship · Life management · Office productivity</p>

<p align="center">
  <a href="README.zh-CN.md">简体中文</a> · <a href="README.md">English</a><br>
  <a href="#quick-start">Quick start</a> · <a href="#product-in-action">See it in action</a> · <a href="#technical-highlights">Technical highlights</a> · <a href="#documentation">Docs</a> · <a href="https://github.com/bamboo-ye/ai-companion/issues">Feedback</a>
</p>

<p align="center">
  <a href="go.mod"><img src="https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white" alt="Go 1.26"></a>
  <a href="web/package.json"><img src="https://img.shields.io/badge/Next.js-16-111111?logo=nextdotjs" alt="Next.js 16"></a>
  <a href="workers/python/pyproject.toml"><img src="https://img.shields.io/badge/Agent-LangGraph-64748B" alt="LangGraph Agent"></a>
  <a href="#quick-start"><img src="https://img.shields.io/badge/Self--hosted-Docker-2496ED?logo=docker&logoColor=white" alt="Self-hosted with Docker"></a>
</p>

AI Companion is a self-hostable personal AI workspace. Continue a meaningful conversation, organize everyday tasks in natural language, and turn your own material into office deliverables. Characters, memory, knowledge and tools work together—from a request to a result you can check.

![Companion home: character conversations and long-term memory](docs/images/readme/companion-dashboard.jpg)

## One workspace, three ways to help

| What you need | Where to start | What you get |
| --- | --- | --- |
| Talk things through and pick up where you left off | **Companion** | Custom characters, multi-turn conversations, inspectable and correctable memory |
| Organize daily plans and spending | **Life assistant** | Today's plan, reminders, ledger records and summaries, Excel export |
| Turn ideas and source material into deliverables | **Work partner** | Multi-attachment research, requested review, knowledge Wiki, PPTX generation, PDF translation, DOCX copy editing, CSV/XLSX analysis |

All three experiences share context, knowledge and governed tools. A separate admin console manages models, Agents, permissions, quotas and execution records.

## Why AI Companion

- **Conversations with continuity.** Recent messages, sourced summaries and correctable memories retain useful preferences and constraints across sessions.
- **Knowledge with evidence, disagreements included.** Search, read and edit your Wiki, then follow citations to the source. Detectable conflicts in research, Wiki pages and memory are checked against evidence; uncertainty remains explicit when evidence is insufficient.
- **Results beyond the chat.** Ledger entries appear in the ledger, reminders in the reminder list, and office tasks return files you can use.
- **Collaboration for complex work.** Read attachments independently, schedule research by dependency, and request parallel source/expression review. Simple conversations retain their short path.
- **A plan for long-running work.** Complete source intake, bounded parallel batches and deterministic merging handle material beyond one model window, with targeted recovery.
- **Control over execution.** Host the app and data services yourself, configure models, allowlist tools and set global/user quotas. Parallel calls share provider permits; the server enforces authorization and required approvals.

## Quick start

Install **Git, Docker Desktop / Docker Compose v2, and Make**. The full Docker setup supplies Go, Python and Node.js; host installations are only needed for local development.

### 1. Get the project and configuration

```bash
git clone https://github.com/bamboo-ye/ai-companion.git
cd ai-companion
test -f .env || cp .env.example .env
```

> **Choose your first-run mode:** the default `MODEL_PROVIDER=development` needs no model key and helps check registration, pages and task plumbing, but returns fixed mock responses. For the natural-language conversations, tool selection and content generation shown below, first configure a server-side provider, key and concrete model IDs using the [model guide (Chinese)](docs/OPENROUTER_MODELS.md). Requests go to your configured provider; self-hosting does not imply offline inference.

### 2. Start the app

```bash
make docker-up
make docker-ps
```

Open **<http://localhost:3000>**, register, sign in and add a character to start a conversation. Compose applies business migrations and initializes Agent checkpoints. The first build downloads images and dependencies.

### 3. Try your first task

These are example requests to try after connecting a real model, not actions already performed:

| Module | Try saying | Check the result |
| --- | --- | --- |
| Companion | “I'm nervous about tomorrow's project presentation. Help me work through what worries me most.” | Add more context and see whether the next reply follows the conversation |
| Life assistant | “I spent CNY 18 on lunch today. Record it as a dining expense.” | Review and confirm the candidate, then open the ledger |
| Life assistant | “Remind me tomorrow at 3 p.m. to prepare the project demo.” | Verify the date, time and timezone, then open reminders |
| Work partner | “Create an illustrated PPT introducing AI Companion, with a table of its main features.” | Check the completion message and file entry in the conversation |
| Work partner | Upload 2–3 sources: “Compare the proposals' costs, risks and implementation requirements in a table with sources. Fact-check the numbers and list unresolved disagreements.” | Verify citations, scope and disagreements; review defaults to on-request, subject to deployment settings and Run budgets |

<details>
<summary>Troubleshooting, admin access and local development</summary>

- Follow logs with `make docker-logs`; stop services while retaining data volumes with `make docker-down`.
- API health: <http://localhost:8080/healthz>; readiness: <http://localhost:8080/readyz>.
- All three chat modules use the Agent by default, so the Agent Worker must be ready.
- Admin console: <http://localhost:3000/admin>. It requires a [separate invitation and Passkey (Chinese)](docs/ADMIN_PASSKEY_LOGIN.md), not an ordinary user account.
- Replace all `change-*` development secrets before exposing services. Configure the deployment's domain and secrets; never commit real keys.
- See the [first-run guide (Chinese)](docs/GETTING_STARTED.md) for setup and troubleshooting, or the [engineering guide](docs/ENGINEERING.md#quick-start) for host development and additional commands.

</details>

## Product in action

The following real UI and conversation captures come from a local environment with sanitized demo accounts and sample data. They show request, execution and result verification—not actions performed on the current reader's account. The new parallelism and arbitration mechanisms have separate implementation and test references below; these existing captures do not validate them.

### Companion: from listening to practical guidance

The user expresses anxiety about a presentation, then explains the worry about having too much material and running overtime. The character follows that context with suggestions for trimming content, timeboxing sections and rehearsing. Long-term preferences can be inspected and maintained separately. The homepage remains at the top of this README; the conversation is below.

<details>
<summary>See the multi-turn conversation: presentation anxiety and preparation</summary>

![Companion conversation: emotional continuity and time-management suggestions](docs/images/readme/companion-conversation.jpg)

</details>

### Life assistant: records you can find after the conversation

Describe an expense, review and confirm it, then check the ledger. Create a reminder and verify it in the list. The demo shows a **CNY 18.00** ledger record and a reminder titled **“准备 AI Companion 项目演示材料”** with its trigger time and notification channel.

<details>
<summary>See the life workspace and expense flow: conversation → ledger</summary>

Plans, reminders and spending share one workspace:

![Life dashboard: daily plan, reminders and ledger](docs/images/readme/life-dashboard.jpg)

Describe the expense in chat and confirm the proposed write:

![Life conversation: natural-language expense entry and successful write](docs/images/readme/life-conversation.jpg)

Open the ledger to verify the record and monthly summary:

![Ledger result: CNY 18 expense and monthly totals](docs/images/readme/life-ledger-result.jpg)

</details>

<details>
<summary>See the reminder flow: creation conversation → reminder record</summary>

First, request the reminder and receive the creation result:

![Life conversation: successful creation of the demo-material reminder](docs/images/readme/life-reminder-conversation.jpg)

Then verify its title, time and channel in the reminder list:

![Reminder result: demo-material reminder and trigger time](docs/images/readme/life-reminder-result.jpg)

</details>

### Work partner: one request, an office deliverable

The exact prompt sent in this demo was:

> 帮我生成一个图文并茂的ppt介绍伴A I，使用表格介绍主要功能

It asks for an illustrated presentation introducing AI Companion, with a table of its main features. The Work partner selects the PPTX generation Skill and returns a `banyai-intro.pptx` file entry in the same conversation. Relevant built-in product knowledge can ground the introduction in documented features and technical details.

<p align="center">
  <img src="docs/images/readme/work-ppt-conversation.jpg" width="600" alt="Actual Work partner conversation: illustrated PPT request, successful completion and file entry">
</p>

<details>
<summary>See the workbench: versioned Skills, intent routing and MCP controls</summary>

The workbench offers attachment extraction, DOCX copy editing, PDF translation, PPTX generation and CSV/XLSX analysis. Background execution connects task status, confirmation steps and artifact permissions.

![Work partner workbench: Skills, intent routing and MCP controls](docs/images/readme/workbench.jpg)

</details>

## Technical highlights

The project's current focus is **context engineering, controlled multi-Agent collaboration, Agentic RAG and evidence arbitration**. Beyond processing more material, the design addresses provenance, conflicting claims, recovery and cost control. The following reflects repository implementation checked on **2026-09-25**, not the deployed version of any particular server.

| Focus | Implementation | Practical value |
| --- | --- | --- |
| **Complete large-file processing** | Ordered extraction rounds, stable Source IR entity IDs, token/character bounds, coverage gates, deterministic merging and targeted retries | Maintains a complete source-processing path beyond one model window |
| **Bounded multi-Agent collaboration** | Parallel Runs, process pools and Composer branches; independent reads of 2–3 attachments, dependency-aware research with 2–6 tasks, requested source/expression review | Makes complex work recoverable and overlaps independent steps |
| **Cross-language concurrency control** | Separate Skill execution, parsing and rendering caps; Go/Python provider permits coordinated by shared-directory file locks | Controls aggregate demand, not just each individual pool |
| **Shared context and memory** | Versioned Go/Python snapshots, sourced summaries, memory correction chains, per-role budgets and full-request window checks | Keeps chat and tool execution consistent and correctable |
| **Parallel knowledge compilation and on-demand retrieval** | Complete source pages plus bounded semantic batches; ordered topic/entity/decision merges, 24-hour shard-candidate caching, Agent-driven search, reading and citation following | Combines source-level traceability with readable summaries and reusable successful work |
| **Evidence arbitration** | A structured protocol for review, research, Wiki and memory; candidate ownership, citation and literal-quote validation | Lets models propose judgments while deterministic checks enforce evidence and authority boundaries |
| **Recoverable tool execution** | PostgreSQL Runs/checkpoints, human approval, idempotency, leases and revision fencing; Go controls business writes | Tracks state and side effects through retries, cancellation and interruptions |
| **Documentation as product knowledge** | Allowlisted docs, SHA-256 versions, `go:embed`, local BM25 and guide citations | Grounds product questions and generation tasks without extra uploads |
| **Identity and cost governance** | Admin Passkeys, recent verification, per-resource quota inheritance, revision checks and transactional audits | Makes privileged changes, limits and model costs accountable |

### 1. Multi-Agent collaboration: dependencies before dispatch

Work requests can read **2–3 attachments** in independent lanes while following each attachment's `next_round` cursor in order. The Planner may also propose **2–6 research questions**. The runtime validates an acyclic dependency graph, read-only tool allowlists and arguments before dispatching ready reads and analyses, then synthesizes against the original evidence. Writes and approvals remain with the Go Tool Gateway.

When the user explicitly requests a “fact-check” or “peer review”, **source and expression specialists** check candidate replies or generation arguments in parallel by default. At most one revision and recheck are allowed; a failed review stops delivery. This reviews content, not a rendered PPT's visual layout. Specialists reuse the existing model configuration, so agreement is not independent multi-model consensus.

Calls, tokens and cost allowances are allocated before dispatch, with a reserve for final synthesis. Usage from successful and failed calls is settled. Checkpoints retain branch results and asynchronous task IDs, allowing resumed Runs to observe existing jobs and retry retryable failed branches at most once. Shared Go/Python provider permits default to **8**, using reliable local shared storage on one host—not distributed cross-host rate limiting.

### 2. Large files: separate complete intake from semantic compression

| Pipeline | Default bounds | Coverage and recovery |
| --- | --- | --- |
| Attachment intake and file generation | Upload limit 20 MiB; Composer batches of 2,000 estimated tokens / 12,000 source characters; concurrency 3, hard cap 4 | Complete all read rounds before generation; stable Source IR IDs, ordered merges, coverage gates and targeted retries |
| Wiki semantic compilation | Up to 24,000 UTF-8 bytes per batch; per-document concurrency 3 and ceiling of 64 batches | Keep a page for every original chunk; exceeding the semantic batch budget records `semantic_batch_budget_exceeded` instead of presenting a prefix as complete coverage |
| Wiki rebuilds and caching | Shard candidates cached for 24 hours; at most one retry per failed batch | Bind caches to user, document, source version, model and content; revalidate evidence and permissions before publication, which remains serial per owner |

Completeness means full intake and verifiable source/entity coverage, not flawless parsing of every format or a summary that repeats every detail. Controlled tests check overlap, ordering and recovery. Real latency depends on the sources and model service; simulated tests do not establish production speedup ratios.

### 3. Evidence arbitration: disagreements remain inspectable

`evidence-arbitration-v1` structures review findings, research claims, Wiki differences and memory relationships as issues with candidates and citations. Structured research facts are compared by subject, predicate, scope and effective time; differing values conflict only within the same scope. Analysis citations must resolve to original tool evidence rather than circular references to other model conclusions.

A verdict must reference a candidate belonging to its issue, visible evidence and a quote literally present in that evidence. Insufficient evidence, invalid verdicts or insufficient budgets retain uncertainty; unresolved research disputes stop file generation. A later upload alone cannot supersede an older Wiki source, and memory arbitration never automatically overwrites or deletes records. Supersession still requires explicit correction.

[Parallel tasks and upgrade settings](docs/PARALLEL_AGENTS.md) · [Arbitration protocol and validation](docs/ARBITRATION.md) · [Large-file design (Chinese)](docs/blog/ppt-generation-vibe-coding/03-large-files-and-intermediate-representation.md) · [Engineering guide](docs/ENGINEERING.md)

### Engineering challenges and solutions

| Challenge | Mechanism | Verification |
| --- | --- | --- |
| Missing/duplicate records or unstable batch ordering | Source IR, coverage gates, ordered joins and targeted retries | [Composition tests](workers/python/tests/test_agent_runtime.py) |
| Invalid task dependencies, repeated work or missing usage after partial failure | Validate dependencies and read-only tools; reserve budgets, retain successful siblings, resume checkpoints and settle at join | [Parallel task tests](workers/python/tests/test_parallel_tasks.py) |
| Independently bounded processes still overload the provider together | Shared provider permits, deadline-aware waiting, release on process exit and separate parse/render caps | [Cross-language permit tests](internal/platform/modelquota/slots_test.go) · [Skill runner tests](internal/skill/runner_test.go) |
| Reviewer agreement hides unsupported claims, or review loops indefinitely | Original-evidence checks, issue-by-issue verdicts and literal quotes; one revision/recheck, unresolved claims retained | [Arbitration and bounded-repair tests](workers/python/tests/test_arbitration.py) |
| Semantic compilation silently covers only a prefix or reuses stale sources | UTF-8-safe complete batching, explicit budget-exceeded status, versioned shard caches and citation checks | [Wiki synthesis tests](internal/document/wiki_synthesis_test.go) |
| Similarity or a model judgment overwrites conflicting memories | Similarity only triggers a relationship check; retain both, bind content fingerprints and require explicit correction for supersession | [Memory arbitration tests](internal/memory/arbitration_test.go) |
| Lost constraints or fact attribution in compressed history | Complete message groups, sourced summaries, correction chains and final window checks | [Shared context tests](internal/conversation/context_snapshot_test.go) |
| Stale knowledge after source access is revoked | Revalidate every dependency and version; invalidate permission-aware caches | [Wiki tests](internal/document/wiki_test.go) |
| Crashes, duplicate dispatch or late responses after cancellation | Durable state, idempotency, lease takeover and revision fencing | [Agent Worker tests](internal/agent/worker_test.go) |

For human-edit conflicts, concurrent admin changes and knowledge consistency, see the [full engineering challenge matrix](docs/ENGINEERING.md#engineering-challenges-and-solutions).

With development dependencies installed, run `make test-python` and `make eval-agent-arbitration`; database recovery cases require isolated test databases. These check control and recovery contracts, not real-model accuracy, latency or cost. Existing deployments also need Wiki-shard and memory-arbitration migrations, rebuilt API/Worker/Agent images and completion or explicit restart of unfinished old-graph Runs. See the linked topic guides for upgrade details.

## Architecture and stack

A **modular Go monolith, independent asynchronous workers and a Python Agent runtime** separate application authority from model reasoning. Models propose actions and generate content; Go authorizes business writes. PostgreSQL stores authoritative state; Kafka is an optional dispatch accelerator.

| Layer | Technologies |
| --- | --- |
| Web | Next.js 16 · React 19 · TypeScript |
| API and business logic | Go 1.26 · OpenAPI · PostgreSQL 18 |
| Agents and processing | Python · LangGraph · PostgreSQL Checkpointer · Versioned Skills |
| Retrieval and files | Qdrant · Redis · MinIO · Source IR · Wiki · Local BM25 |
| Deployment and observability | Docker Compose · Caddy · OpenTelemetry · Prometheus · Loki / Alloy · Optional Langfuse |

[Architecture and processing diagrams (Chinese)](docs/TECHNICAL_GUIDE.zh-CN.md#系统架构) · [Repository map](docs/ENGINEERING.md#repository-map) · [Concurrency settings (Chinese)](docs/TECHNICAL_GUIDE.zh-CN.md#关键并行与分片参数)

## Documentation

Most topic guides are currently in Chinese; the engineering and operations guide is in English.

| Looking for | Start here |
| --- | --- |
| Installation, registration, first tasks and troubleshooting | [First-run guide](docs/GETTING_STARTED.md) |
| Model providers, server-side keys, roles and budgets | [Model configuration](docs/OPENROUTER_MODELS.md) |
| Large files, parallelism, architecture and trade-offs | [English engineering guide](docs/ENGINEERING.md) · [中文技术指南](docs/TECHNICAL_GUIDE.zh-CN.md) |
| Attachments, review, disputes and upgrades | [Parallel tasks and resources](docs/PARALLEL_AGENTS.md) · [Evidence arbitration](docs/ARBITRATION.md) |
| Memory, Wiki, retrieval and context organization | [Context management](docs/CONTEXT_MANAGEMENT.md) |
| Updating built-in product descriptions and help | [Product knowledge maintenance](docs/PRODUCT_KNOWLEDGE.md) |
| Admin login and user quotas | [Passkey administration](docs/ADMIN_PASSKEY_LOGIN.md) · [Quota management](docs/ADMIN_QUOTAS.md) |
| Operations, quality gates and verification records | [Runbooks](docs/runbooks/) · [Engineering and release commands](docs/ENGINEERING.md) · [Project verification](docs/PROJECT_COMPLETION.md) |
| Reporting vulnerabilities and public-release checks | [Security policy](SECURITY.md) · [Security review record](docs/SECURITY_REVIEW.md) |
| APIs, domain design and architecture decisions | [OpenAPI](api/openapi/openapi.yaml) · [Detailed design](docs/DETAILED_DESIGN.md) · [ADRs](docs/adr/) |

## Get involved

Share use cases and feedback in [Issues](https://github.com/bamboo-ye/ai-companion/issues), or improve code, tests, documentation and demos through [Pull Requests](https://github.com/bamboo-ye/ai-companion/pulls). Include sanitized reproduction steps, versions and logs; never post model keys, credentials or private documents.

See the [engineering guide](docs/ENGINEERING.md#documentation-maintenance) for development and validation. After editing the Chinese README, technical guide or other product-knowledge sources, refresh the embedded catalog:

```bash
node scripts/build-product-knowledge.mjs
node scripts/build-product-knowledge.mjs --check
go test ./internal/productknowledge
```

If AI Companion is useful to you, a Star helps others discover the project.
