# Direct traffic production drill evidence contract v1

The local production integration drill runs the production test module in a fresh subprocess and
records only non-secret evidence: exit status, test count, output digest, bound source digests,
the seven exact state transitions, required control checks, and the terminal 100% state. It never
records keys, tokens, test output, prompts, response content, or live Provider payloads.

The transition manifest is independently hash-chained and fixed to
`0→5→0→5→10→25→50→100`, ending at revision and operation count 7. The terminal evaluator result
must be `hold / maximum_stage_reached`. Any missing, reordered, or altered stage fails validation.

The report and source manifest are bound into an HMAC attestation issued with a dedicated drill
key. Evidence is stored in a private two-file directory and verification rejects extra artifacts,
wrong key IDs, invalid signatures, stale evidence, changed source files, and report tampering.
`contains_live_provider_evidence` is always false: this evidence cannot replace live Provider
probing or operator-approved production evidence.
