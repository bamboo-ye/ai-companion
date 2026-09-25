from __future__ import annotations

import copy
import json
import os
import uuid
import threading
from contextlib import nullcontext
from typing import Any

import pytest
from langgraph.checkpoint.memory import InMemorySaver

from ai_companion_worker.agent_runtime import (
    BudgetPolicy,
    ToolPreparation,
    build_graph,
    runtime_config,
)
from ai_companion_worker.arbitration import research_issues, validate_verdict
from test_agent_runtime import agent_input
from test_parallel_tasks import ResearchDecisions, ResearchTools, ReviewDecisions, query_catalog


class EvidenceTools(ResearchTools):
    def prepare(self, **kwargs: Any) -> ToolPreparation:
        self.prepared.append(kwargs)
        value = "100元" if kwargs["arguments"]["query"] == "cost" else "200元"
        return ToolPreparation(
            status="completed",
            tool_name=kwargs["tool_name"],
            risk_level="none",
            response="预算：" + value,
            data={"text": "预算：" + value},
        )


class ArbitrationDecisions(ResearchDecisions):
    def __init__(self, mode: str = "accept") -> None:
        super().__init__()
        self.mode = mode
        self.arbitration_calls: list[dict[str, Any]] = []
        self.response_context: dict[str, Any] = {}

    def analyze_evidence(self, *, question: str, context: dict[str, Any]) -> dict[str, Any]:
        value = "100元" if question == "cost" else "200元"
        return {
            "claims": [
                {
                    "text": "预算为" + value,
                    "evidence_ids": list(context["evidence"]),
                    "fact": {
                        "subject": "项目",
                        "predicate": "预算",
                        "value": value,
                        "scope": "总预算",
                        "valid_at": "",
                    },
                }
            ]
        }

    def arbitrate(
        self, *, kind: str, packet: dict[str, Any], context: dict[str, Any]
    ) -> dict[str, Any]:
        self.arbitration_calls.append(packet)
        assert context["model_allowance"]["remaining_calls"] == 1
        if self.mode == "failure":
            raise TimeoutError("provider unavailable")
        issue = packet["issues"][0]
        selected = issue["candidates"][0]
        action = "need_evidence" if self.mode == "uncertain" else "accept"
        ref = selected["evidence_ids"][0]
        return {
            "decisions": [
                {
                    "issue_id": issue["id"],
                    "action": action,
                    "selected_ids": [] if action == "need_evidence" else [selected["id"]],
                    "reason": "核对原文",
                    "instruction": "",
                    "evidence": [
                        {"id": "foreign" if self.mode == "foreign" else ref, "quote": "预算：100元"}
                    ],
                }
            ]
        }

    def respond(self, **kwargs: Any) -> str:
        self.response_context = kwargs["context"]
        return "已完成资料对照。"


def run_research(
    decisions: ArbitrationDecisions, policy: BudgetPolicy | None = None
) -> dict[str, Any]:
    graph = build_graph(
        checkpointer=InMemorySaver(),
        decisions=decisions,
        tools=EvidenceTools(),
        budget_policy=policy,
    )
    initial = agent_input("arbitration-" + decisions.mode, "work")
    initial["context"]["tools"] = query_catalog()
    return graph.invoke(initial, runtime_config(initial["run_id"]))


def test_research_arbitration_selects_only_supported_existing_claims() -> None:
    decisions = ArbitrationDecisions()
    result = run_research(decisions)
    assert result["outcome"] == "completed"
    assert [len(a["claims"]) for a in result["research_results"]] == [1, 0]
    assert len(result["observations"]) == 2
    assert len(decisions.arbitration_calls) == 1
    assert result["budget_usage"]["model_calls_by_node"]["arbitrate_parallel"] == 1
    assert decisions.response_context["arbitrations"][0]["decisions"][0]["selected_ids"] == [
        "claim_0_0"
    ]


@pytest.mark.parametrize("mode", ["uncertain", "failure", "foreign"])
def test_uncertain_or_invalid_arbitration_preserves_visible_disputes(mode: str) -> None:
    decisions = ArbitrationDecisions(mode)
    result = run_research(decisions)
    assert result["outcome"] == "completed"
    assert all(not a["claims"] for a in result["research_results"])
    assert "仍待核对的来源分歧" in result["response"]
    assert len(result["arbitrations"][0]["issues"][0]["candidates"]) == 2
    assert result["budget_usage"]["model_calls_by_node"]["arbitrate_parallel"] == 1


def test_arbitration_reserves_synthesis_budget_and_skips_without_capacity() -> None:
    decisions = ArbitrationDecisions()
    result = run_research(decisions, BudgetPolicy(max_model_calls=5))
    assert result["outcome"] == "completed"
    assert not decisions.arbitration_calls
    assert result["budget_usage"]["model_calls"] == 5
    assert result["arbitrations"][0]["reason"] == "arbitration_model_budget"
    assert "仍待核对" in result["response"]


def test_conflict_detection_respects_scope_and_effective_time() -> None:
    decisions = ArbitrationDecisions()
    analyses = [
        decisions.analyze_evidence(question=q, context={"evidence": {q: {}}})
        for q in ("cost", "risk")
    ]
    assert len(research_issues(analyses)) == 1
    analyses[1]["claims"][0]["fact"]["scope"] = "另一个项目"
    assert not research_issues(analyses)
    analyses[1]["claims"][0]["fact"]["scope"] = "总预算"
    analyses[1]["claims"][0]["fact"]["valid_at"] = "2027"
    assert not research_issues(analyses)


@pytest.mark.parametrize("backend", ["memory", "postgres"])
def test_arbitration_checkpoint_resume_does_not_replay_sources(backend: str) -> None:
    saver = InMemorySaver()
    dsn = os.getenv("LANGGRAPH_POSTGRES_TEST_DSN")
    if backend == "postgres" and not dsn:
        pytest.skip("LANGGRAPH_POSTGRES_TEST_DSN is not set")

    def checkpoint() -> Any:
        if backend == "memory":
            return nullcontext(saver)
        from langgraph.checkpoint.postgres import PostgresSaver

        return PostgresSaver.from_conn_string(dsn)

    decisions, tools = ArbitrationDecisions(), EvidenceTools()
    initial = agent_input("arbitration-resume-" + uuid.uuid4().hex, "work")
    initial["context"]["tools"] = query_catalog()
    config = runtime_config(initial["run_id"])
    with checkpoint() as first:
        if backend == "postgres":
            first.setup()
        graph = build_graph(checkpointer=first, decisions=decisions, tools=tools)
        graph.invoke(initial, config, interrupt_before=["arbitrate_parallel"])
        assert len(tools.prepared) == 2 and not decisions.arbitration_calls
    with checkpoint() as second:
        graph = build_graph(checkpointer=second, decisions=decisions, tools=tools)
        result = graph.invoke(None, config)
        assert result["outcome"] == "completed"
        assert len(tools.prepared) == 2 and len(decisions.arbitration_calls) == 1
        graph.invoke(None, config)
        assert len(decisions.arbitration_calls) == 1


class ReviewArbitrator(ReviewDecisions):
    def __init__(self, action: str) -> None:
        super().__init__()
        self.action = action
        self.arbitrations = 0
        self.revisions = 0

    def review_candidate(
        self, *, focus: str, candidate: str, context: dict[str, Any]
    ) -> dict[str, Any]:
        if "十元" not in candidate:
            return {"passed": True, "findings": []}
        return {
            "passed": False,
            "findings": [
                {
                    "quote": "十元",
                    "issue": "应核对价格" if focus == "grounding" else "精简价格说明",
                    "evidence_ids": [],
                }
            ],
        }

    def arbitrate(
        self, *, kind: str, packet: dict[str, Any], context: dict[str, Any]
    ) -> dict[str, Any]:
        self.arbitrations += 1
        return {
            "decisions": [
                {
                    "issue_id": "review",
                    "action": self.action,
                    "selected_ids": [c["id"] for c in packet["issues"][0]["candidates"]]
                    if self.action == "merge"
                    else [],
                    "reason": "结合候选和用户要求核对",
                    "instruction": "保留价格信息并明确它尚待来源核对",
                    "evidence": [{"id": "candidate", "quote": "十元"}],
                }
            ]
        }

    def revise_response(self, **kwargs: Any) -> str:
        self.revisions += 1
        assert len(kwargs["violations"]) == 1
        assert kwargs["violations"][0]["message"] == "保留价格信息并明确它尚待来源核对"
        return "价格尚待来源核对。"


@pytest.mark.parametrize("action", ["reject", "merge"])
def test_review_arbitration_rejects_false_findings_or_coordinates_one_revision(action: str) -> None:
    decisions = ReviewArbitrator(action)
    graph = build_graph(checkpointer=InMemorySaver(), decisions=decisions, tools=EvidenceTools())
    initial = agent_input("review-arbitration", "work")
    initial["user_message"] = "请仔细核对"
    result = graph.invoke(initial, runtime_config(initial["run_id"]))
    assert result["outcome"] == "completed"
    assert decisions.arbitrations == 1
    assert decisions.revisions == (1 if action == "merge" else 0)
    assert result["specialist_review"]["passed"] is True


def test_unchanged_review_reuses_verdict_and_stops_after_one_repair() -> None:
    class Decisions(ReviewArbitrator):
        def revise_response(self, **kwargs: Any) -> str:
            self.revisions += 1
            return kwargs["response"]

    decisions = Decisions("merge")
    graph = build_graph(checkpointer=InMemorySaver(), decisions=decisions, tools=EvidenceTools())
    initial = agent_input("review-unchanged-arbitration", "work")
    initial["user_message"] = "请仔细核对"
    result = graph.invoke(initial, runtime_config(initial["run_id"]))
    assert result["outcome"] == "specialist_review_failed"
    assert decisions.arbitrations == 1 and decisions.revisions == 1


def test_verdict_rejects_omissions_fabricated_quotes_and_unknown_candidates() -> None:
    issues = [{"id": "i", "candidates": [{"id": "a", "evidence_ids": ["source"]}]}]
    good = {
        "issue_id": "i",
        "action": "accept",
        "selected_ids": ["a"],
        "reason": "原文",
        "evidence": [{"id": "source", "quote": "事实"}],
    }
    assert validate_verdict({"decisions": [good]}, issues, {"source": "事实"}, "research")
    for mutation in (
        {"selected_ids": ["foreign"]},
        {"evidence": [{"id": "source", "quote": "虚构"}]},
        {"issue_id": "unknown"},
        {"action": "grant_tool_access"},
    ):
        with pytest.raises(ValueError):
            validate_verdict(
                {"decisions": [{**copy.deepcopy(good), **mutation}]},
                issues,
                {"source": "事实"},
                "research",
            )
    with pytest.raises(ValueError):
        validate_verdict({"decisions": []}, issues, {"source": "事实"}, "research")


def test_openrouter_arbitration_is_one_structured_call_without_tools() -> None:
    from test_openrouter_decision import StubOpenRouter

    reply = {"decisions": []}
    port = StubOpenRouter([{"choices": [{"message": {"content": json.dumps(reply)}}]}])
    assert port.arbitrate(kind="review", packet={"issues": []}, context={}) == reply
    assert port.requests[0]["response_format"] == {"type": "json_object"}
    assert "tools" not in port.requests[0]


def test_provider_overrun_is_accounted_and_retained_without_retry() -> None:
    class Decisions(ArbitrationDecisions):
        def __init__(self) -> None:
            super().__init__()
            self.events = threading.local()

        def arbitrate(self, **kwargs: Any) -> dict[str, Any]:
            verdict = super().arbitrate(**kwargs)
            self.events.pending = [
                {
                    "kind": "model_call",
                    "role": "composer",
                    "status": "succeeded",
                    "prompt_tokens": 1000001,
                    "cost_micros": 300001,
                }
            ]
            return verdict

        def consume_observability(self) -> list[dict[str, Any]]:
            events = getattr(self.events, "pending", [])
            self.events.pending = []
            return events

    decisions = Decisions()
    result = run_research(decisions)
    assert len(decisions.arbitration_calls) == 1
    assert result["arbitrations"][0]["reason"] == "arbitration_budget_overrun"
    assert result["budget_usage"]["cost_micros"] >= 300001
    assert result["outcome"] == "model_budget_exhausted"


def test_last_node_permit_can_be_used_when_global_synthesis_capacity_remains() -> None:
    decisions, tools = ArbitrationDecisions(), EvidenceTools()
    graph = build_graph(checkpointer=InMemorySaver(), decisions=decisions, tools=tools)
    initial = agent_input("last-arbitration-permit", "work")
    initial["context"]["tools"] = query_catalog()
    config = runtime_config(initial["run_id"])
    graph.invoke(initial, config, interrupt_before=["arbitrate_parallel"])
    state = graph.get_state(config).values
    usage = {
        **state["budget_usage"],
        "model_calls_by_node": {
            **state["budget_usage"]["model_calls_by_node"],
            "arbitrate_parallel": 2,
        },
        "model_calls": state["budget_usage"]["model_calls"] + 2,
    }
    graph.update_state(config, {"budget_usage": usage}, as_node="join_parallel")
    result = graph.invoke(None, config)
    assert result["outcome"] == "completed"
    assert len(decisions.arbitration_calls) == 1
    assert result["budget_usage"]["model_calls_by_node"]["arbitrate_parallel"] == 3


def test_provider_preflight_budget_rejection_is_not_a_billable_call() -> None:
    from ai_companion_worker.agent_runtime import ModelBudgetExceeded

    class Decisions(ArbitrationDecisions):
        def arbitrate(self, **kwargs: Any) -> dict[str, Any]:
            raise ModelBudgetExceeded("prompt cannot fit reserved allowance")

    result = run_research(Decisions())
    assert result["outcome"] == "completed"
    assert result["arbitrations"][0]["reason"] == "ModelBudgetExceeded"
    assert result["budget_usage"]["model_calls_by_node"]["arbitrate_parallel"] == 0
    assert "仍待核对" in result["response"]
