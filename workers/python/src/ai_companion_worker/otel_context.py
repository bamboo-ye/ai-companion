from __future__ import annotations

import re
from typing import Any, Mapping

_TRACEPARENT = re.compile(
    r"00-([0-9a-fA-F]{32})-([0-9a-fA-F]{16})-([0-9a-fA-F]{2})"
)


def propagation_headers(run: Mapping[str, Any]) -> dict[str, str]:
    value = run.get("otel_traceparent")
    if not isinstance(value, str):
        return {}
    match = _TRACEPARENT.fullmatch(value.strip())
    if match is None:
        return {}
    trace_id, span_id, flags = (part.lower() for part in match.groups())
    if trace_id == "0" * 32 or span_id == "0" * 16:
        return {}
    headers = {"traceparent": f"00-{trace_id}-{span_id}-{flags}"}
    state = run.get("otel_tracestate")
    if isinstance(state, str):
        state = state.strip()
        if state and len(state) <= 512 and "\r" not in state and "\n" not in state:
            headers["tracestate"] = state
    return headers
