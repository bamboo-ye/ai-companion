# Production direct-traffic 25%-to-50% Provider contract v1

This boundary is exclusively for `https-direct-traffic-production-expansion-50`. It performs one
compare-and-set transition: `25% / revision 5 / operations 5` to
`50% / revision 6 / operations 6`. It cannot activate, recover, replay an earlier expansion, skip
past 50%, or use another stage's receipt.

The Provider token and four proof keys are independent from all earlier production stages. The
stage Gate binds the exact 25% receipt, full 25%-to-50% rollout report, adapter certificate, new
zero-drift shadow attestation, and the full shadow Gate report. Both health and shadow windows must
begin after the 25% receipt was applied; the health window covers at least 600 seconds by default.

The permission-free stage evidence evaluator can be reused by expansion Gates, but it has no key,
filesystem, network, or compare-and-set authority. Client and Provider independently validate exact
state, proof signatures and lifetimes, report digests, identity bindings, and the prior receipt
chain. A successful result is exactly 50% at revision 6 with six operations. The receipt retains
the prior expansion operation ID for audit continuity.
