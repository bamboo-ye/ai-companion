# ADR 0003: Defer model-provider lock-in until a bake-off

- Status: Proposed
- Date: 2026-07-01

## Context

Launch region, privacy boundary, expected concurrency, and model budget are not known.

## Decision

Keep chat, intent, embedding, reranking, and vision behind separate provider interfaces. During M0, evaluate candidate providers on quality, latency, cost, regional processing, retention, and failure behavior. Do not add production API keys to the repository.

