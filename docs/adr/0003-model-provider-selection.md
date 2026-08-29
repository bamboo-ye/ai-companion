# ADR 0003: Use versioned OpenRouter role profiles

- Status: Accepted
- Date: 2026-07-01
- Updated: 2026-08-01

## Context

The launch does not target mainland China. Product policy requires OpenRouter,
prioritizes task quality, then converges cost, and explicitly avoids changing to
the newest model without a measured project-specific benefit.

## Decision

Keep model capabilities behind provider interfaces, but use OpenRouter for
production generative calls. The accepted default role profile is:

- `deepseek/deepseek-v4-flash-0731` as the preferred model for planning,
  routing, composition, assessment, repair, life/work responses, and legacy
  text-only direct chat;
- the companion-only final responder uses the fixed zero-price pool
  `openai/gpt-oss-20b:free`, `google/gemma-4-31b-it:free`, then
  `inclusionai/ling-3.0-flash:free`; it never falls back to a paid responder;
- the prior `openai/gpt-5-mini` model as planning/composition/response fallback,
  and `openai/gpt-5-nano` as routing/assessment fallback;
- `openai/gpt-5-mini` remains the PDF/file model and retained multimodal profile
  because the selected DeepSeek revision is text-only;
- no dynamic `openrouter/free`, `auto` or `latest` aliases. Concrete `:free`
  variants are accepted only for the companion responder role.

Cross-model fallback is sequential and budgeted: DeepSeek is tried first and a
role's previous GPT model is tried only after provider failure or invalid
contract output. OpenRouter may also fail over between compatible providers of
the same concrete model. Requests prefer price, require supported parameters, deny providers
marked as collecting prompt data, and enforce configured maximum prices. ZDR
can be required by deployment policy. Production accepts only the canonical
OpenRouter API endpoint; the generic OpenAI-compatible provider is not a
runtime configuration option.

Graph runs persist config version `2026-08-structured-composer-v2`, the
concrete role manifest and its canonical fingerprint, then enforce trusted
action, call, prompt-token, completion-token and cost
ceilings. Model changes require the project evaluation set, an explicit config
version bump and a canary.

Production API keys remain outside the repository. Embedding, reranking and
raw image/audio/video providers remain separate decisions when those
capabilities are added; they must not inherit the text-only DeepSeek default.

The detailed price comparison, routing fields, budgets and recovery semantics
are maintained in [`../OPENROUTER_MODELS.md`](../OPENROUTER_MODELS.md).
