# Healthy Care personal workspace

This is the personal health runtime adapted from the supplied `healthy_care` checkout at revision `8eb376f`. The Go module owns records, model run state, action confirmations and private attachments. The Python runtime handles triage, response validation and LangGraph checkpoints. The user interface is integrated into Companion's Next.js app.

The service accepts a Companion-verified account ID over a private gateway. Standalone password login, research routes, and SciencePal execution are excluded. Only source code, required build files and the two personal workspace image assets were imported. The two services use dedicated credentials and persistent health volumes; see the [setup guide](../../docs/HEALTH_ASSISTANT.md).
