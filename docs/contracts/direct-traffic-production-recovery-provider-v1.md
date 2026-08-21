# Direct traffic production recovery Provider contract v1

This boundary is exclusively for `https-direct-traffic-production-recovery`. It can perform one
transition: a previously activated and rolled-back namespace at `0%`, revision `2`, two operations
to the `5%` canary at revision `3`. It cannot perform initial activation, skip the rollback, expand
beyond `5%`, or recover from any other revision.

Recovery requires a complete new evidence epoch after the rollback:

1. the immutable emergency rollback receipt matching the current namespace and history-chain digest;
2. a rollout report whose entire stable health window occurs after the rollback, covers the policy
   minimum duration, and recommends exactly `expand 0→5`;
3. a recovery rollout signature bound to that exact report;
4. a fresh certificate for the recovery writer;
5. a recovery-only zero-drift shadow Gate created after rollback;
6. a recovery Gate proving the configured cooldown elapsed and binding all previous artifacts;
7. live CAS state at `0%`, revision `2`, two operations.

Provider token, rollout, certification, shadow and recovery-Gate keys are separate recovery
credentials. Client and Provider both verify signatures, identity, freshness, state, receipt,
cooldown and proof digests. Old initial-activation or pre-rollback proofs are rejected.

The write is idempotent and uses lookup reconciliation after an indeterminate network result. A
successful receipt must report `5%`, revision `3`, and converged append-only local/remote chains.
