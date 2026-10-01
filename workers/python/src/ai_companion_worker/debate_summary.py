"""Content-free delivery projection; raw sources stay in checkpoint audit state."""

from typing import Any, Mapping


def debate_summary(state: Mapping[str, Any]) -> dict[str, Any] | None:
    session = state.get("debate_session")
    if not isinstance(session, dict) or session.get("termination_reason") == "not_needed":
        return None
    rounds = session.get("rounds", [])
    decisions = rounds[-1]["decisions"] if rounds else []
    total = len(session.get("packet", {}).get("issues", []))
    invalid = state.get("outcome") == "evidence_access_changed" or session.get(
        "termination_reason"
    ) in {
        "snapshot_changed",
        "source_version_changed",
        "evidence_access_changed",
    }
    isolation_passed = bool(session.get("independence_reports")) and all(
        r.get("context_isolation") == "passed" for r in session["independence_reports"]
    ) and session.get("termination_reason") != "independence_failed"
    sources_verified = session.get("source_access_verified") is True and not invalid
    resolved = (
        0 if not isolation_passed or not sources_verified else sum(d["action"] not in {"retain", "need_evidence"} for d in decisions)
    )
    return {
        "status": "resolved"
        if total and resolved == total
        else "partially_resolved"
        if resolved
        else "unresolved",
        "total_issues": total,
        "resolved_issues": resolved,
        "rounds": len(rounds),
        "model_calls": session.get("calls", 0),
        "additional_reads": len(session.get("reads", [])),
        "isolation_passed": isolation_passed,
        "sources_verified": sources_verified,
        "termination_reason": "evidence_access_changed"
        if invalid
        else "verification_incomplete"
        if session.get("termination_reason") == "resolved" and (not isolation_passed or not sources_verified)
        else session.get("termination_reason", "in_progress"),
    }
