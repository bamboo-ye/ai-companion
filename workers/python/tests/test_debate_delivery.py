from __future__ import annotations

import json
import pytest
from ai_companion_worker.debate_summary import debate_summary
from ai_companion_worker.evaluation.debate_scenarios import run_scenario


@pytest.mark.parametrize(
    "case, status, reads",
    [
        ("resolved", "resolved", 3),
        ("unresolved", "unresolved", 3),
        ("revoked", "unresolved", 3),
        ("write_denied", "unresolved", 2),
        ("off", None, 2),
    ],
)
def test_browser_scenarios_execute_actual_graph(case: str, status: str | None, reads: int) -> None:
    result = run_scenario(case)
    assert result["read_count"] == reads
    assert result["external_model_calls"] == 0
    if status is None:
        assert result["evidence_review"] is None
        assert "prepare_debate" not in result["nodes"]
    else:
        summary = result["evidence_review"]
        assert summary["status"] == status
        assert summary["model_calls"] <= 6
        if case == "resolved":
            assert result["outcome"] == "completed"
            assert "100 元" in result["response"]
            assert summary["rounds"] == 2
        if case == "revoked":
            assert "100" not in result["response"]
            assert not summary["sources_verified"]


def test_delivery_summary_does_not_expose_sources_or_false_legacy_success() -> None:
    state = {
        "debate_session": {
            "packet": {"issues": [{"label": "PRIVATE"}]},
            "rounds": [{"decisions": [{"action": "need_evidence", "reason": "PRIVATE"}]}],
            "termination_reason": "no_new_evidence",
        }
    }
    summary = debate_summary(state)
    assert summary and summary["status"] == "unresolved"
    assert "PRIVATE" not in json.dumps(summary)
    assert debate_summary({}) is None


@pytest.mark.parametrize("field", ["source_access_verified", "independence_reports"])
def test_unverified_success_is_not_delivered_as_resolved(field: str) -> None:
    session = {
        "packet": {"issues": [{"id": "issue"}]},
        "rounds": [{"decisions": [{"action": "accept"}]}],
        "termination_reason": "resolved",
        "source_access_verified": True,
        "independence_reports": [{"context_isolation": "passed"}],
    }
    session.pop(field)
    summary = debate_summary({"debate_session": session})
    assert summary and summary["status"] == "unresolved"
    assert summary["resolved_issues"] == 0
    assert summary["termination_reason"] == "verification_incomplete"
