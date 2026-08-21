# Production direct-traffic 5%-to-10% Provider contract v1

This boundary is exclusively for `https-direct-traffic-production-expansion-10`. It performs one
compare-and-set transition: `5% / revision 3 / operations 3` to
`10% / revision 4 / operations 4`. It cannot initialize traffic, recover from zero, skip a stage,
or expand beyond 10%.

The writer uses a Provider token that is distinct from normal production, emergency rollback, and
recovery tokens. Its rollout, adapter-certification, shadow, and expansion-Gate keys are also
independent. Client verification and Provider verification both reject key reuse and independently
validate every signed proof.

The expansion Gate binds five immutable inputs: the 5%-to-10% rollout attestation, the full rollout
report, the adapter certification, the zero-drift shadow attestation, and the successful recovery
receipt. Every health observation in the rollout report and the shadow proof must be later than the
recovery receipt. The rollout report must cover at least 600 seconds by default. Therefore an old
healthy window or a recovery-period shadow proof cannot authorize this stage.

The execution body binds the exact local state, all proof digests, the recovery receipt, and a
request digest. The Provider rechecks the exact transition, proof signatures and lifetimes,
identity bindings, recovery chain digest, and expansion-Gate signature before the write. The local
ledger is updated only after the Provider returns a verifiable result; local and remote chains must
converge or the result is indeterminate.

The successful receipt schema is
`agent-direct-traffic-production-expansion-10-receipt-v1`. Its state is exactly 10% at revision 4,
and it carries the recovery operation ID for audit continuity.
