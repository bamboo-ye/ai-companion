# Direct traffic production Provider contract v1

This boundary is exclusively for `https-direct-traffic-production`. It can perform exactly one
transition: an empty `production-*` namespace at `0%`, revision `0`, and zero operations to `5%`,
revision `1`. It cannot authorize later expansion, rollback, disable, a non-production environment,
or a preproduction/global namespace. The separate emergency `5→0` boundary is documented in
[`direct-traffic-production-emergency-provider-v1.md`](direct-traffic-production-emergency-provider-v1.md).

## Isolation and transport

- The environment and namespace must match `production-[a-z0-9][a-z0-9-]{0,62}`.
- Namespace hashing uses a production-only domain and cannot collide with preproduction identity.
- HTTPS is restricted to port 443 and the exact allowed host. Redirects, credentials in URLs,
  private IP endpoints, duplicate JSON keys, oversized bodies, and non-JSON responses are rejected.
- Provider bearer token plus rollout, adapter-certification, shadow-Gate, and production-Gate keys
  are five distinct production secrets. The runner rejects values reused across roles or matching
  configured preproduction keys.

## Endpoints

All paths are relative to the configured HTTPS base URL.

| Method | Path | Required behavior |
|---|---|---|
| `GET` | `/v1/namespaces/{namespace}/direct-traffic/state` | Return the exact production state contract. |
| `GET` | `/v1/namespaces/{namespace}/direct-traffic/shadow-state` | Return the read-only shadow state contract. |
| `GET` | `/v1/namespaces/{namespace}/direct-traffic/changes/by-idempotency-key/{key}` | Return the immutable result or `404`. |
| `PUT` | `/v1/namespaces/{namespace}/direct-traffic/changes/{key}` | Apply the four-proof initial CAS once. |

The client performs lookup, at most one `PUT`, idempotency reconciliation, and mandatory readback.
It never retries an indeterminate `PUT`. The Provider must persist result and traffic mutation in
one transaction, then return the same immutable result for the same key and digest.

## Four-proof authorization

Before mutation, the Provider independently verifies:

1. a production rollout signature for `expand 0→5` and revision `0`;
2. a production adapter certificate for name `https-direct-traffic-production` version `1.0.0`;
3. a fresh passing production shadow Gate for the exact observer/provider/environment identities;
4. a fresh production Gate signed by its own key, bound to the production namespace and the exact
   digests of proofs 1–3.

The production Gate is issued only after verifying a signed local preproduction failure/recovery
drill plus a separate signed live read-only preproduction Provider probe. The Gate reports `hold`
for any wrong adapter, missing shadow pass, non-expand decision, non-zero current stage, or target
other than `5%`; a `hold` Gate cannot be signed.

The client and Provider both require the live state to be exactly `0%`, revision `0`, zero
operations, and zero history-chain head. This deliberately makes the adapter unusable for `5→10`
or for rollback; those require a separately reviewed next-stage adapter and policy.

## Failure behavior and evidence

- A stale compare-and-swap returns conflict and performs no mutation.
- After a possibly committed timeout, perform only same-key lookup and rerun the same authorization;
  changing any proof creates a different request and is rejected.
- Success is not accepted until Provider and local append-only ledgers agree on stage, revision,
  operation count, and chain head.
- Preserve the signed local drill, live probe, rollout, certificate, shadow Gate, production Gate,
  receipt, and both final ledger summaries as one release record. Never preserve keys or tokens.

Run `make drill-agent-direct-traffic-production` for the offline four-proof contract suite. The
operator sequence and stop conditions are defined in `docs/runbooks/release-rollback.md`.
