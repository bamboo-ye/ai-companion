# Direct traffic production emergency Provider contract v1

This boundary is exclusively for `https-direct-traffic-production-emergency`. It can perform one
state transition: the initial production state at `5%`, revision `1`, one operation to `0%`,
revision `2`. It accepts only signed `rollback` or `disable` decisions. It cannot expand traffic,
start from any later rollout state, or use a non-`production-*` namespace.

The emergency route deliberately does not require a fresh shadow observation. An outage or drift in
the normal writer/observer path must not prevent traffic removal. Instead, both client and Provider
independently require:

1. a fresh rollout attestation signed by the emergency rollout key for exactly `5→0`;
2. a certificate for adapter `https-direct-traffic-production-emergency` version `1.0.0`, signed by
   the emergency certification key;
3. the immutable receipt from the original `0→5` activation, matching namespace, `5%`, revision
   `1`, and the current history-chain digest;
4. a passing emergency Gate signed by a third emergency proof key and bound to the exact two proofs,
   activation receipt, incident ID, and authorizing actor;
5. live compare-and-swap state at `5%`, revision `1`, and exactly one operation.

The emergency bearer token and all three emergency proof keys must be mutually distinct and must not
match any configured normal-production credential. The Provider repeats signature, freshness,
identity, transition, receipt and Gate checks; client validation is not a trust boundary.

`PUT` is idempotent by the signed request digest. A timeout after commit is resolved with lookup and
never by a second blind write. A successful result must be `0%`, revision `2`, two operations, and
the remote and local append-only history-chain digests must converge.

Run `make drill-agent-direct-traffic-production` for the complete `0→5→0` contract drill, including
an injected latency regression and proof-replay verification.

Traffic may return to `5%` only through the separate
[`direct-traffic-production-recovery-provider-v1.md`](direct-traffic-production-recovery-provider-v1.md)
boundary after its cooldown and post-rollback evidence window pass.
