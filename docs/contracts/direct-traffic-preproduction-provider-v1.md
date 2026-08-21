# Direct traffic preproduction Provider contract v1

This is the network boundary for `https-direct-traffic-preproduction` only. It cannot authorize or
address a production, staging, global, or unscoped namespace. A namespace must match
`preproduction-[a-z0-9][a-z0-9-]{0,62}`.

## Transport

- HTTPS on port 443, exact configured host, bearer authentication, no redirects.
- Requests use `Accept: application/json`; the single write also uses
  `Content-Type: application/json`.
- Responses must be `application/json`, contain no duplicate JSON keys, and be at most 32 KiB.
- The Provider must not log bearer tokens or the embedded proof signatures.

## Endpoints

All endpoints are relative to the configured base URL.

| Method | Path | Required behavior |
|---|---|---|
| `GET` | `/v1/namespaces/{namespace}/direct-traffic/state` | Return the exact current state contract. |
| `GET` | `/v1/namespaces/{namespace}/direct-traffic/shadow-state` | Return the read-only shadow state contract. |
| `GET` | `/v1/namespaces/{namespace}/direct-traffic/changes/by-idempotency-key/{key}` | Return the immutable result or `404`. |
| `PUT` | `/v1/namespaces/{namespace}/direct-traffic/changes/{key}` | Apply the triple-proven CAS once. |

The client never retries `PUT`. A transport error or 408/429/5xx response after `PUT` is an unknown
outcome: the client performs only the idempotency lookup. The Provider must persist the result under
the key atomically with the traffic mutation. Reusing a key with a different request digest must
fail.

## Write verification

Before mutation the Provider independently verifies:

1. the request digest and path idempotency key;
2. rollout, adapter-certification, and shadow-Gate HMAC signatures and trusted key IDs;
3. all proof lifetimes;
4. adapter name/version plus provider, environment, observer, and namespace identities;
5. the signed decision transition;
6. expected traffic, revision, operation count, and history-chain head.

A stale compare-and-swap returns `409` with a versioned `conflict` result and performs no mutation.
Success returns a versioned immutable result. The client then forces a state read; success is not
accepted until Provider and local append-only chains converge.

## Drills and evidence

`make drill-agent-direct-traffic-preproduction` runs the complete local contract simulation:
expansion with timeout-after-commit reconciliation, duplicate replay, repair-regression rollback,
offline unknown outcome, and same-key recovery. The signed report explicitly sets
`contains_live_provider_evidence=false` and is not a substitute for a Provider integration record.

`make probe-agent-direct-traffic-preproduction` runs only two real HTTPS reads: state plus lookup of a
fresh random idempotency key. It never invokes a mutation method and records
`contains_live_provider_evidence=true`. A full preproduction sign-off requires both a passing local
drill bundle and a passing live read-only probe bundle, followed by an operator-authorized expansion
and rollback using fresh triple proofs.
