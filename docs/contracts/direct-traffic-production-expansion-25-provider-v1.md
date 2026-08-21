# Production direct-traffic 10%-to-25% Provider contract v1

This boundary is exclusively for `https-direct-traffic-production-expansion-25`. It performs one
compare-and-set transition: `10% / revision 4 / operations 4` to
`25% / revision 5 / operations 5`. It cannot activate, recover, repeat the 5%-to-10% stage, skip
past 25%, or use another stage's receipt.

The Provider token and four proof keys are independent from all earlier production stages. The
stage Gate binds the 10% expansion receipt, full 10%-to-25% rollout report, adapter certificate,
new zero-drift shadow attestation, and the full shadow Gate report. Both health and shadow windows
must begin after the 10% receipt was applied and cover at least 600 seconds for health by default.

Client and Provider independently verify exact state, proof signatures and lifetimes, report
digests, identity bindings, and the prior receipt chain. A successful result is exactly 25% at
revision 5 with five operations. The receipt retains the prior expansion operation ID for audit
continuity.
