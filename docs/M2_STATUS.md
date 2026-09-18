# M2 Context, Memory, and Wiki Status

Date: 2026-07-03

## Completed in slice 1

- Explicit high-confidence extraction for “记住 / 请记住 / 记一下” messages.
- Greeting and non-explicit conversation filtering.
- Normalized hash deduplication and sensitivity classification.
- Durable source conversation/message traceability, confidence, importance, pin, validity, and supersession fields.
- User-scoped memory list, manual create, edit, pin, delete, and clear APIs.
- Relevant memory recall injected into the generation context.
- Deleted memories are excluded immediately from recall.
- Web memory management for viewing, adding, editing, pinning, deleting, and clearing.
- MySQL migration `000004_long_term_memory` and OpenAPI contracts.

## Completed in slice 2: document intake

- Authenticated multipart upload for text PDF and UTF-8 text files.
- Configurable 20 MiB limit, real content-type detection, binary rejection, safe file-name normalization, and EICAR test-signature rejection.
- Content-addressed blob-store boundary with a private local filesystem adapter for development.
- Per-user SHA-256 deduplication with a generated-column unique constraint that remains safe after soft deletion.
- Atomic MySQL creation of file metadata, document status, ingest job, and `document.ingest.v1` outbox event.
- User-scoped list/status/delete APIs; deletion removes the active blob and cancels queued/processing ingestion.
- Web document library for upload, queued-state display, duplicate feedback, and confirmed deletion.
- MySQL migration `000005_document_intake` and OpenAPI 0.3 contracts.

## Completed in slice 3: parsing and retrieval

- Isolated Python parser pinned to `pypdf 6.14.2`, with page-preserving extraction for text PDF and UTF-8 text.
- Encrypted-PDF, empty-text, page-count, and per-page output limits; scanned PDFs remain explicit OCR follow-up work.
- Heading-aware, page-bounded structural chunks targeting roughly 600 tokens with overlap and stable content hashes.
- MySQL page/chunk persistence, worker leases, deterministic chunk/point UUIDs, idempotent replacement, and terminal ingest states.
- Dedicated document-worker image combining the Go lease/control plane with the isolated Python parser runtime.
- Versioned 256-dimensional hashing baseline plus sparse token vectors, stored as named Qdrant vectors.
- Qdrant Query API dense/sparse prefetch with RRF fusion and mandatory user/document payload filters.
- Evidence-grounded query API and Web UI with document/page citations; lexical post-check returns an explicit insufficient-evidence answer instead of guessing.
- Deleted documents remain invisible even if external vector/blob cleanup is delayed; Qdrant deletion is also attempted immediately.
- MySQL migration `000006_document_parse_and_chunks` and expanded OpenAPI contracts.

## Completed in slice 4: bounded context and quality gate

- Configurable recent-context and rolling-summary token budgets with language-aware deterministic estimates.
- Recent context keeps complete message groups; assistant multi-bubble replies are never cut in the middle.
- Versioned rolling summaries retain covered sequence and time ranges, summarizer version, and measured token count.
- MySQL summary writes serialize per conversation and are idempotent under competing generation jobs.
- Generation events expose summary rolls and their recent/summary token measurements for diagnosis.
- Type-aware exponential date decay for long-term memory recall; stable preferences decay more slowly than commitments.
- A checked-in 100-case Chinese/English Wiki baseline covering context precision/recall, citation correctness, faithfulness, and insufficient-evidence accuracy.
- MySQL migration `000007_conversation_context` and `make eval-m2` quality gate.

## Verification

| Check | Result |
|---|---|
| “你好” creates no memory | Passed |
| Duplicate explicit preference creates one memory | Passed |
| Related query recalls “我不吃香菜” | Passed |
| Deleted memory is not recalled | Passed |
| Unrelated query does not recall a preference sharing only a common character | Passed |
| Sensitive phone/email/ID classification | Passed |
| Web add, source display, edit, pin, and delete browser flow | Passed |
| `PATCH` CORS preflight contract | Passed |
| Fresh migration (4 versions / 15 tables) and second-run idempotency | Passed |
| Go tests, vet, OpenAPI parse, Web check, and production build | Passed |
| Unsupported binary, oversized file, unsafe signature, and traversal rejection | Passed |
| PDF magic detection does not trust the filename or browser MIME | Passed |
| Same-user duplicate returns the original document/job; another user remains isolated | Passed |
| Real MySQL upload creates one file/document/job/outbox event | Passed |
| Two concurrent identical uploads resolve to one document/job (`deduplicated`: false/true) | Passed |
| Web document library queued-state and confirmed-delete flow | Passed |
| Deleted document is hidden, its blob removed, and queued job cancelled | Passed |
| Fresh migration (5 versions / 18 tables) and second-run idempotency | Passed |
| pypdf text-PDF page extraction and structural chunk tests | Passed |
| Go-to-Python parser subprocess contract | Passed |
| Upload → claim → parse → index → ready → cited query lifecycle | Passed (in-memory index) |
| Unrelated query returns `sufficient: false` with no citations | Passed |
| Qdrant collection/upsert/RRF/filter/delete REST contract | Passed (simulated HTTP transport) |
| Web cited-query UI lint, type-check, and production build | Passed |
| Real PDF → Python parser → MySQL page/chunk persistence → Qdrant indexing | Passed (1 page / 1 chunk) |
| Real Qdrant related query returns the cited document and page | Passed (`m2-rag-real.pdf`, page 1) |
| Real Qdrant unrelated query returns insufficient evidence and no citations | Passed |
| Document deletion removes the Qdrant point and prevents later retrieval | Passed (0 remaining points) |
| Browser cited-query and insufficient-evidence flows | Passed (0 console errors) |
| Fresh migration (6 versions / 20 tables) and second-run idempotency | Passed |
| Complete-message recent window and multi-bubble boundary tests | Passed |
| Rolling summary range, version, idempotency, and strict token budget | Passed |
| Real MySQL long conversation produced 3 summary versions and 3 diagnostic events | Passed (maximum 64 / 64 summary tokens) |
| Type-aware memory date-decay ranking | Passed |
| M2 Chinese/English offline Wiki quality gate | Passed (100 cases; all five baseline metrics 1.000) |
| Fresh migration (7 versions / 21 tables) and second-run idempotency | Passed |

## Deferred production upgrades

- Replace the hashing baseline after Chinese/multilingual embedding evaluation; dual-write the next index version.
- OCR/visual fallback, bounding boxes, MinIO adapter, outbox publisher, cleanup reconciliation, and retry/DLQ controls.
- Expand the deterministic 100-case gate with provider-specific and adversarial evaluation after model/region/privacy approval.

M2 is complete against its development-plan acceptance criteria: controllable memory, bounded recent context, durable rolling summaries, text-PDF Wiki retrieval with page citations, insufficient-evidence behavior, idempotent ingestion/deletion, and a repeatable 100-case quality gate. The upgrades above are retained as Beta/production hardening and do not block starting M3.
