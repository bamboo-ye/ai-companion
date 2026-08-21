# Production direct-traffic 50%-to-100% Provider contract v1

This boundary is exclusively for `https-direct-traffic-production-expansion-100`. It performs the
terminal compare-and-set transition: `50% / revision 6 / operations 6` to
`100% / revision 7 / operations 7`. It cannot activate, recover, replay an earlier expansion, use
a 25% receipt, or write beyond the policy maximum.

The Provider token and four proof keys are independent from all earlier production stages. The
stage Gate binds the exact 50% receipt, full 50%-to-100% rollout report, adapter certificate, new
zero-drift shadow attestation, and the full shadow Gate report. Health and shadow evidence must
begin after the 50% receipt was applied; the health window covers at least 600 seconds by default.

Client and Provider independently validate exact state, proof signatures and lifetimes, report
digests, identity bindings, and receipt-chain continuity. A successful result is exactly 100% at
revision 7 with seven operations. At 100%, the rollout evaluator returns `maximum_stage_reached`
and `hold`, so no further expansion request can be created.
