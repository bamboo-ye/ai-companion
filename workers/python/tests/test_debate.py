from __future__ import annotations

import copy
import json
import threading
import time
from pathlib import Path
from typing import Any

import pytest
from langgraph.checkpoint.memory import InMemorySaver
from langgraph.types import Command

from ai_companion_worker.agent_runtime import (
    BudgetPolicy,
    ToolPreparation,
    ToolOutcome,
    build_graph,
    runtime_config,
)
from ai_companion_worker.arbitration import (
    effective_arbitrations,
    research_issues,
    resolution_status,
    validate_verdict,
)
from ai_companion_worker.debate import (
    evidence_fragments,
    novel_fragments,
    validate_position,
    make_debate_nodes,
)
from test_agent_runtime import agent_input
from test_arbitration import ArbitrationDecisions, EvidenceTools, ReviewArbitrator
from test_parallel_tasks import query_catalog, ResearchDecisions, ResearchTools


NEW_TEXT = "预算：100元为最终批复，200元是未通过的历史草案。"


class DebateTools(EvidenceTools):
    def __init__(self, mode: str = "new") -> None:
        super().__init__()
        self.mode = mode
        self.observed = 0

    def prepare(self, **kwargs: Any) -> ToolPreparation:
        if kwargs["arguments"]["query"] in {"cost", "risk"}:
            return super().prepare(**kwargs)
        self.prepared.append(kwargs)
        if self.mode == "fail":
            raise PermissionError("source revoked")
        if self.mode == "async":
            return ToolPreparation(
                status="completed",
                tool_name=kwargs["tool_name"],
                risk_level="none",
                response="正在读取",
                data={"kind": "skill_run", "id": "evidence-task", "status": "running"},
            )
        text = {"new": NEW_TEXT, "duplicate": "预算：100元", "irrelevant": "天气晴朗"}.get(
            self.mode, NEW_TEXT
        )
        return ToolPreparation(
            status="completed",
            tool_name=kwargs["tool_name"],
            risk_level="none",
            response=text,
            data={"text": text, "document_id": "original", "chunk_id": "footnote"},
        )

    def observe(self, **kwargs: Any) -> ToolOutcome:
        self.observed += 1
        return ToolOutcome(response=NEW_TEXT, data={"text": NEW_TEXT})


class DebateDecisions(ArbitrationDecisions):
    def __init__(self, behavior: str = "supplement") -> None:
        super().__init__()
        self.behavior = behavior
        self.position_calls: list[dict[str, Any]] = []
        self.position_barrier = threading.Barrier(2)

    def debate_position(
        self, *, focus: str, packet: dict[str, Any], context: dict[str, Any]
    ) -> dict[str, Any]:
        self.position_calls.append({"focus": focus, "packet": copy.deepcopy(packet)})
        self.position_barrier.wait(timeout=3)
        assert context["model_allowance"]["remaining_calls"] == 1
        assert context["deadline_at"] > time.time()
        assert "positions" not in packet and "arbitrations" not in packet
        if "new_evidence_ids" not in packet:
            assert "challenges" not in packet
        assessments = [
            {
                "issue_id": i["id"],
                "candidate_id": c["id"],
                "stance": "uncertain",
                "reason": "需要核对最终预算",
                "evidence": [],
            }
            for i in packet["issues"]
            for c in i["candidates"]
        ]
        novelty = [
            {
                "issue_id": issue,
                "evidence_id": key,
                "quote": packet["evidence"][key],
                "reason": "明确了最终批复与草案的关系",
            }
            for key in packet.get("new_evidence_ids", [])
            for issue in packet["new_evidence_issues"][key]
        ]
        if self.behavior == "no_novelty":
            novelty = []
        if self.behavior == "position_failure" and focus == "counterexample":
            raise TimeoutError("provider failure")
        if self.behavior == "invented_quote":
            assessments[0].update(
                stance="support",
                evidence=[{"id": next(iter(packet["evidence"])), "quote": "不存在的引文"}],
            )
        return {"assessments": assessments, "novelty": novelty, "new_claims": []}

    def arbitrate(
        self, *, kind: str, packet: dict[str, Any], context: dict[str, Any]
    ) -> dict[str, Any]:
        if "positions" not in packet:
            return super().arbitrate(kind=kind, packet=packet, context=context)
        self.arbitration_calls.append(copy.deepcopy(packet))
        assert len(packet["positions"]) == 2
        needs = not packet.get("new_evidence_ids") and self.behavior != "resolve_first"
        if self.behavior == "never_resolve":
            needs = True
        out = []
        for issue in packet["issues"]:
            candidate = issue["candidates"][0]
            key = "candidate" if kind == "review" else candidate["evidence_ids"][0]
            citations = [{"id": key, "quote": packet["evidence"][key]}]
            d = {
                "issue_id": issue["id"],
                "action": "need_evidence" if needs else "accept",
                "selected_ids": [] if needs else [candidate["id"]],
                "reason": "核对原始记录",
                "instruction": "保留价格信息并明确它尚待来源核对" if kind == "review" else "",
                "classification": "unresolved" if needs else "contradiction",
                "evidence": [] if needs else citations,
            }
            if needs:
                d["evidence_requests"] = [
                    {
                        "question": "最终预算是哪一项",
                        "discriminates": "批复或草案",
                        "tool_name": "life_create_reminder"
                        if self.behavior == "write_request"
                        else "work_wiki_search",
                        "arguments": {
                            "query": "cost" if self.behavior == "repeat_query" else "最终预算"
                        },
                    }
                ]
            else:
                d["assessment_responses"] = [
                    {"id": a["id"], "reason": "来源明确支持此处理", "evidence": citations}
                    for p in packet["positions"]
                    for a in p["assessments"]
                    if a["issue_id"] == issue["id"]
                ]
                if self.behavior == "ignore_challenge":
                    d["assessment_responses"] = []
            out.append(d)
        return {"decisions": out}


def run_debate(
    monkeypatch: pytest.MonkeyPatch,
    *,
    behavior: str = "supplement",
    evidence: str = "new",
    policy: BudgetPolicy | None = None,
) -> tuple[dict[str, Any], DebateDecisions, DebateTools]:
    monkeypatch.setenv("AGENT_DEBATE_MODE", "on_conflict")
    decisions, tools = DebateDecisions(behavior), DebateTools(evidence)
    initial = agent_input("debate", "work")
    initial["context"]["tools"] = query_catalog()
    graph = build_graph(
        checkpointer=InMemorySaver(), decisions=decisions, tools=tools, budget_policy=policy
    )
    return graph.invoke(initial, runtime_config("debate")), decisions, tools


def test_isolated_rounds_introduce_evidence_and_replace_effective_dispute(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    result, decisions, tools = run_debate(monkeypatch)
    assert result["outcome"] == "completed"
    session = result["debate_session"]
    assert session["termination_reason"] == session["resolution_status"] == "resolved"
    assert session["round"] == 2 and session["calls"] == 6
    assert len(decisions.position_calls) == 4 and len(tools.prepared) == 3
    assert len(session["rounds"]) == 2
    assert session["rounds"][0]["call_status"] == "succeeded"
    assert session["rounds"][0]["resolution_status"] == "unresolved"
    assert session["independence_reports"][0]["assessment_mode"] == "independent"
    assert session["independence_reports"][1]["assessment_mode"] == "cross_review"
    assert all(
        r["statistical_independence"] == "not_established" for r in session["independence_reports"]
    )
    assert [len(a["claims"]) for a in result["research_candidates"]] == [1, 1]
    assert [len(a["claims"]) for a in result["research_results"]] == [1, 0]
    assert "仍待核对" not in result["response"]
    assert all(
        v["action"] == "accept"
        for r in decisions.response_context["arbitrations"]
        for v in r["decisions"]
    )


@pytest.mark.parametrize("evidence", ["duplicate", "irrelevant", "fail"])
def test_no_novel_evidence_or_failed_read_cannot_unlock_second_round(
    monkeypatch: pytest.MonkeyPatch, evidence: str
) -> None:
    result, decisions, tools = run_debate(monkeypatch, evidence=evidence)
    assert result["debate_session"]["resolution_status"] == "unresolved"
    assert result["debate_session"]["calls"] == 3
    assert len(decisions.position_calls) == 2 and len(tools.prepared) == 3
    assert "仍待核对" in result["response"]


@pytest.mark.parametrize(
    "behavior,reason,calls",
    [
        ("position_failure", "TimeoutError", 2),
        ("invented_quote", "ValueError", 2),
        ("write_request", "no_new_evidence", 3),
        ("repeat_query", "no_new_evidence", 3),
        ("never_resolve", "round_limit", 6),
        ("no_novelty", "no_new_evidence", 5),
        ("ignore_challenge", "ValueError", 6),
    ],
)
def test_bounded_failures_keep_disputes(
    monkeypatch: pytest.MonkeyPatch, behavior: str, reason: str, calls: int
) -> None:
    result, _, tools = run_debate(monkeypatch, behavior=behavior)
    assert result["debate_session"]["termination_reason"] == reason
    assert result["debate_session"]["resolution_status"] == "unresolved"
    assert result["debate_session"]["calls"] == calls
    assert len(tools.prepared) <= 3
    assert all(not a["claims"] for a in result["research_results"])


def test_resolves_without_forcing_opposition_or_extra_reads(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    result, decisions, tools = run_debate(monkeypatch, behavior="resolve_first")
    assert result["debate_session"]["calls"] == 3
    assert len(decisions.position_calls) == 2 and len(tools.prepared) == 2
    assert result["debate_session"]["resolution_status"] == "resolved"


def test_budget_reserves_synthesis_before_starting(monkeypatch: pytest.MonkeyPatch) -> None:
    result, decisions, tools = run_debate(monkeypatch, policy=BudgetPolicy(max_model_calls=6))
    assert result["debate_session"]["termination_reason"] == "debate_model_budget"
    assert not decisions.position_calls
    assert len(tools.prepared) == 2
    assert result["outcome"] == "completed" and "仍待核对" in result["response"]


@pytest.mark.parametrize(
    "node",
    [
        "debate_position",
        "join_debate_positions",
        "arbitrate_debate",
        "prepare_debate_evidence",
        "read_debate_evidence",
        "join_debate_evidence",
        "finish_debate",
    ],
)
def test_checkpoint_resume_reuses_committed_work(
    monkeypatch: pytest.MonkeyPatch, node: str
) -> None:
    monkeypatch.setenv("AGENT_DEBATE_MODE", "on_conflict")
    decisions, tools, saver = DebateDecisions(), DebateTools(), InMemorySaver()
    initial = agent_input("resume-debate", "work")
    initial["context"]["tools"] = query_catalog()
    config = runtime_config(initial["run_id"])
    graph = build_graph(checkpointer=saver, decisions=decisions, tools=tools)
    graph.invoke(initial, config, interrupt_before=[node])
    graph = build_graph(checkpointer=saver, decisions=decisions, tools=tools)
    result = graph.invoke(None, config)
    assert result["debate_session"]["resolution_status"] == "resolved"
    assert len(decisions.position_calls) == 4
    assert len(decisions.arbitration_calls) == 2
    assert len(tools.prepared) == 3
    graph.invoke(None, config)
    assert len(tools.prepared) == 3 and len(decisions.position_calls) == 4


def test_deadline_persists_across_restart(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("AGENT_DEBATE_MODE", "on_conflict")
    decisions, tools = DebateDecisions(), DebateTools()
    initial = agent_input("expired-debate", "work")
    initial["context"]["tools"] = query_catalog()
    config = runtime_config(initial["run_id"])
    graph = build_graph(checkpointer=InMemorySaver(), decisions=decisions, tools=tools)
    graph.invoke(initial, config, interrupt_before=["arbitrate_debate"])
    session = graph.get_state(config).values["debate_session"]
    graph.update_state(config, {"debate_session": {**session, "deadline_at": time.time() - 1}})
    result = graph.invoke(None, config)
    assert result["debate_session"]["termination_reason"] == "deadline_exceeded"
    assert not decisions.arbitration_calls and len(tools.prepared) == 2


def test_switch_off_preserves_existing_arbitration(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("AGENT_DEBATE_MODE", "off")
    initial = agent_input("off-debate", "work")
    initial["context"]["tools"] = query_catalog()
    decisions = DebateDecisions()
    result = build_graph(
        checkpointer=InMemorySaver(), decisions=decisions, tools=DebateTools()
    ).invoke(initial, runtime_config(initial["run_id"]))
    assert not result.get("debate_session") and not decisions.position_calls
    assert len(decisions.arbitration_calls) == 1


@pytest.mark.parametrize("started_mode,resume_mode,expected_calls", [("off", "on_conflict", 0), ("on_conflict", "off", 4)])
def test_resume_uses_the_original_feature_mode(
    monkeypatch: pytest.MonkeyPatch, started_mode: str, resume_mode: str, expected_calls: int
) -> None:
    monkeypatch.setenv("AGENT_DEBATE_MODE", started_mode)
    initial = agent_input("freeze-mode-" + started_mode, "work")
    initial["context"]["tools"] = query_catalog()
    # Client-supplied preferences cannot override the operator's configured mode.
    initial["context"]["debate_mode"] = resume_mode
    decisions, saver, tools = DebateDecisions(), InMemorySaver(), DebateTools()
    config = runtime_config(initial["run_id"])
    graph = build_graph(checkpointer=saver, decisions=decisions, tools=tools)
    graph.invoke(initial, config, interrupt_before=["plan"])
    monkeypatch.setenv("AGENT_DEBATE_MODE", resume_mode)
    result = build_graph(checkpointer=saver, decisions=decisions, tools=tools).invoke(None, config)
    assert result["debate_mode"] == started_mode
    assert len(decisions.position_calls) == expected_calls


def test_source_identity_and_retrieval_metadata_do_not_fake_novelty() -> None:
    old = evidence_fragments({"text": "预算：100元", "document_id": "original"})
    repeated = evidence_fragments(
        {"text": "预算：100元", "document_id": "mirror", "retrieved_at": "tomorrow"}
    )
    assert not novel_fragments(old, repeated, "最终预算")
    assert not evidence_fragments({"answer": "预算：300元", "response": "预算：300元"})
    assert novel_fragments(old, evidence_fragments({"text": NEW_TEXT}), "最终预算")


def test_ids_survive_order_changes_and_partial_resolution_is_explicit() -> None:
    decisions = ArbitrationDecisions()
    analyses = [
        decisions.analyze_evidence(question=q, context={"evidence": {q: {}}})
        for q in ("cost", "risk")
    ]
    first, second = research_issues(analyses), research_issues(list(reversed(analyses)))
    assert first[0]["id"] == second[0]["id"]
    assert {c["id"] for c in first[0]["candidates"]} == {c["id"] for c in second[0]["candidates"]}
    assert (
        resolution_status([{"action": "accept"}, {"action": "need_evidence"}])
        == "partially_resolved"
    )


CONTRACT_CASES = json.loads(
    (
        Path(__file__).resolve().parents[3] / "docs/contracts/evidence-arbitration-v2.cases.json"
    ).read_text()
)


@pytest.mark.parametrize("case", CONTRACT_CASES, ids=lambda c: c["name"])
def test_shared_v2_contract(case: dict[str, Any]) -> None:
    source = case["input"]
    args = (
        {"decisions": case["decisions"]},
        source["issues"],
        {e["id"]: e["text"] for e in source["evidence"]},
        source["kind"],
    )
    if not case["valid"]:
        with pytest.raises(ValueError):
            validate_verdict(*args)
    else:
        assert resolution_status(validate_verdict(*args)) == case["resolution"]


def test_legacy_history_uses_latest_issue_decision_not_record_status() -> None:
    records = [
        {
            "version": "evidence-arbitration-v1",
            "kind": "research",
            "status": "resolved",
            "issues": [{"id": "a"}, {"id": "b"}],
            "decisions": [
                {"issue_id": "a", "action": "need_evidence"},
                {"issue_id": "b", "action": "retain"},
            ],
        },
        {
            "version": "evidence-arbitration-v2",
            "kind": "research",
            "issues": [{"id": "a"}],
            "decisions": [{"issue_id": "a", "action": "accept"}],
        },
    ]
    current = effective_arbitrations(records)
    assert [r["resolution_status"] for r in current] == ["unresolved", "resolved"]
    assert current[0]["decisions"] == [{"issue_id": "b", "action": "retain"}]
    assert len(records[0]["decisions"]) == 2


def test_async_evidence_resume_does_not_start_another_read(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("AGENT_DEBATE_MODE", "on_conflict")
    decisions, tools = DebateDecisions(), DebateTools("async")
    initial = agent_input("async-debate", "work")
    initial["context"]["tools"] = query_catalog()
    graph = build_graph(checkpointer=InMemorySaver(), decisions=decisions, tools=tools)
    config = runtime_config(initial["run_id"])
    graph.invoke(initial, config)
    assert graph.get_state(config).next == ("wait_debate_evidence",)
    result = graph.invoke(Command(resume={"type": "tool_poll", "task_id": "evidence-task"}), config)
    assert result["debate_session"]["resolution_status"] == "resolved"
    assert len(tools.prepared) == 3 and tools.observed == 1
    assert result["budget_usage"]["actions"] == 3


class ReviewDebate(ReviewArbitrator):
    def __init__(self, behavior: str) -> None:
        super().__init__("merge")
        self.behavior = behavior
        self.position_calls: list[dict[str, Any]] = []
        self.arbitration_calls: list[dict[str, Any]] = []
        self.position_barrier = threading.Barrier(2)

    def debate_position(self, **kwargs: Any) -> dict[str, Any]:
        return DebateDecisions.debate_position(self, **kwargs)

    def arbitrate(self, **kwargs: Any) -> dict[str, Any]:
        if "positions" in kwargs["packet"]:
            return DebateDecisions.arbitrate(self, **kwargs)
        return super().arbitrate(**kwargs)


@pytest.mark.parametrize("behavior", ["resolve_first", "repeat_query"])
def test_review_repairs_once_only_after_resolved_arbitration(
    monkeypatch: pytest.MonkeyPatch, behavior: str
) -> None:
    monkeypatch.setenv("AGENT_DEBATE_MODE", "on_conflict")
    decisions = ReviewDebate(behavior)
    initial = agent_input("review-debate", "work")
    initial["user_message"] = "请审校这段内容"
    graph = build_graph(checkpointer=InMemorySaver(), decisions=decisions, tools=DebateTools())
    result = graph.invoke(initial, runtime_config(initial["run_id"]))
    if behavior == "resolve_first":
        assert result["outcome"] == "completed" and decisions.revisions == 1
    else:
        assert result["outcome"] == "specialist_review_failed" and decisions.revisions == 0
    assert len(decisions.position_calls) == 2


def test_position_cannot_omit_candidate_or_fabricate_novelty() -> None:
    packet = {
        "kind": "research",
        "issues": [{"id": "budget", "candidates": [{"id": "a"}]}],
        "evidence": {"old": "预算100元", "new": "预算100元，适用于试点"},
        "new_evidence_ids": ["new"],
        "new_evidence_issues": {"new": ["budget"]},
    }
    job = {"id": "position", "packet": packet, "round": 2}
    with pytest.raises(ValueError, match="omitted"):
        validate_position({"assessments": []}, job)
    with pytest.raises(ValueError, match="repeats"):
        validate_position(
            {
                "assessments": [
                    {
                        "issue_id": "budget",
                        "candidate_id": "a",
                        "stance": "uncertain",
                        "reason": "待核对",
                        "evidence": [],
                    }
                ],
                "novelty": [
                    {
                        "issue_id": "budget",
                        "evidence_id": "new",
                        "quote": "预算100元",
                        "reason": "更肯定",
                    }
                ],
            },
            job,
        )


def test_initial_context_rejects_peer_or_dependency_output_before_model_call() -> None:
    from ai_companion_worker.arbitration import digest

    decisions = DebateDecisions()
    nodes = make_debate_nodes(
        decisions=decisions,
        tools=DebateTools(),
        allowance=lambda *a, **k: {},
        access_reason=lambda *a, **k: "",
        policy=BudgetPolicy(),
    )
    for forbidden in ("positions", "challenges", "arbitrations", "dependencies", "analysis"):
        packet = {"issues": [], forbidden: "peer result"}
        result = nodes["debate_position"](
            {
                "debate_job": {
                    "packet": packet,
                    "snapshot_hash": digest(packet),
                    "id": "a",
                    "round": 1,
                    "generation": 1,
                    "focus": "grounding",
                    "model_hash": "model",
                    "prompt_version": "test",
                }
            }
        )["debate_results"][0]
        assert result["error"] == "independence_failed" and not result["events"]
    assert not decisions.position_calls


class VerifiedTools(DebateTools):
    def __init__(self) -> None:
        super().__init__()
        self.revoked = False
        self.version = "v1"

    def prepare(self, **kwargs: Any) -> ToolPreparation:
        result = super().prepare(**kwargs)
        result.data["document_id"] = "original"
        return result

    def validate_evidence(self, **kwargs: Any) -> dict[str, str]:
        if self.revoked:
            raise PermissionError("source unavailable")
        return {r["source_id"]: self.version for r in kwargs["references"]}


@pytest.mark.parametrize("boundary", ["debate_position", "arbitrate_debate", "generate_response"])
@pytest.mark.parametrize("change", ["revoke", "version"])
def test_resume_rechecks_sources_before_using_or_delivering_old_evidence(
    monkeypatch: pytest.MonkeyPatch, boundary: str, change: str
) -> None:
    monkeypatch.setenv("AGENT_DEBATE_MODE", "on_conflict")
    decisions, tools = DebateDecisions("resolve_first"), VerifiedTools()
    initial = agent_input("source-fence", "work")
    initial["context"]["tools"] = query_catalog()
    config = runtime_config(initial["run_id"])
    graph = build_graph(checkpointer=InMemorySaver(), decisions=decisions, tools=tools)
    graph.invoke(initial, config, interrupt_before=[boundary])
    if change == "revoke":
        tools.revoked = True
    else:
        tools.version = "v2"
    result = graph.invoke(None, config)
    assert result["outcome"] == "evidence_access_changed"
    assert "100元" not in result["response"] and not decisions.response_context


def test_provider_overrun_is_charged_and_no_more_calls_start(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    class Overrun(DebateDecisions):
        def __init__(self) -> None:
            super().__init__()
            self.events = threading.local()

        def debate_position(self, **kwargs: Any) -> dict[str, Any]:
            value = super().debate_position(**kwargs)
            self.events.value = [
                {
                    "kind": "model_call",
                    "role": "composer",
                    "status": "succeeded",
                    "cost_micros": 300000,
                }
            ]
            return value

        def consume_observability(self) -> list[dict[str, Any]]:
            value = getattr(self.events, "value", [])
            self.events.value = []
            return value

    monkeypatch.setenv("AGENT_DEBATE_MODE", "on_conflict")
    decisions = Overrun()
    initial = agent_input("overrun-debate", "work")
    initial["context"]["tools"] = query_catalog()
    result = build_graph(
        checkpointer=InMemorySaver(), decisions=decisions, tools=DebateTools()
    ).invoke(initial, runtime_config(initial["run_id"]))
    assert result["budget_usage"]["cost_micros"] == 600000
    assert result["debate_session"]["termination_reason"] == "debate_budget_overrun"
    assert not decisions.arbitration_calls and len(decisions.position_calls) == 2


def test_unresolved_research_stops_artifact_generation(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("AGENT_DEBATE_MODE", "on_conflict")
    initial = agent_input("artifact-debate", "work")
    initial["context"]["tools"] = query_catalog()
    decisions, tools = DebateDecisions(), DebateTools("duplicate")
    graph = build_graph(checkpointer=InMemorySaver(), decisions=decisions, tools=tools)
    config = runtime_config(initial["run_id"])
    graph.invoke(initial, config, interrupt_before=["prepare_debate"])
    state = graph.get_state(config).values
    graph.update_state(
        config, {"task_contract": {**state["task_contract"], "artifact_types": ["pptx"]}}
    )
    result = graph.invoke(None, config)
    assert result["outcome"] == "arbitration_unresolved"
    assert not decisions.response_context
    assert all(r["tool_name"] == "work_wiki_search" for r in tools.prepared)


def test_revised_claim_is_registered_without_overwriting_old_candidates(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    class Revised(DebateDecisions):
        def debate_position(self, **kwargs: Any) -> dict[str, Any]:
            result = super().debate_position(**kwargs)
            packet = kwargs["packet"]
            if packet.get("new_evidence_ids"):
                issue, key = packet["issues"][0], packet["new_evidence_ids"][0]
                old = issue["candidates"][0]
                result["new_claims"] = [
                    {
                        "issue_id": issue["id"],
                        "supersedes_claim_id": old["id"],
                        "text": "最终批复预算100元",
                        "fact": {**old["fact"], "scope": "最终批复"},
                        "evidence": [{"id": key, "quote": NEW_TEXT}],
                    }
                ]
            return result

        def arbitrate(self, **kwargs: Any) -> dict[str, Any]:
            result = super().arbitrate(**kwargs)
            packet = kwargs["packet"]
            if packet.get("new_evidence_ids"):
                revised = next(
                    c for c in packet["issues"][0]["candidates"] if c.get("supersedes_claim_id")
                )
                result["decisions"][0].update(
                    selected_ids=[revised["id"]],
                    evidence=[{"id": revised["evidence_ids"][0], "quote": NEW_TEXT}],
                )
            return result

    monkeypatch.setenv("AGENT_DEBATE_MODE", "on_conflict")
    initial = agent_input("revised-debate", "work")
    initial["context"]["tools"] = query_catalog()
    result = build_graph(
        checkpointer=InMemorySaver(), decisions=Revised(), tools=DebateTools()
    ).invoke(initial, runtime_config(initial["run_id"]))
    assert result["debate_session"]["resolution_status"] == "resolved"
    assert sum(len(a["claims"]) for a in result["research_candidates"]) == 3
    selected = [c for a in result["research_results"] for c in a["claims"]]
    assert len(selected) == 1 and selected[0]["fact"]["scope"] == "最终批复"


def test_openrouter_position_is_structured_isolated_and_has_no_tools() -> None:
    from test_openrouter_decision import StubOpenRouter

    reply = {"assessments": [], "novelty": [], "new_claims": []}
    port = StubOpenRouter([{"choices": [{"message": {"content": json.dumps(reply)}}]}])
    assert (
        port.debate_position(
            focus="grounding", packet={"issues": []}, context={"deadline_at": time.time() + 10}
        )
        == reply
    )
    assert "tools" not in port.requests[0]
    assert port.requests[0]["response_format"] == {"type": "json_object"}


def test_no_issues_adds_no_debate_calls_even_when_enabled(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("AGENT_DEBATE_MODE", "on_conflict")
    initial = agent_input("no-debate-issue", "work")
    initial["context"]["tools"] = query_catalog()
    result = build_graph(
        checkpointer=InMemorySaver(), decisions=ResearchDecisions(), tools=ResearchTools()
    ).invoke(initial, runtime_config(initial["run_id"]))
    assert not result.get("debate_session")
    assert not result["budget_usage"]["model_calls_by_node"].get("debate_position")


def test_missing_original_evidence_is_detected_even_without_disagreement() -> None:
    from ai_companion_worker.arbitration import arbitration_packet

    analysis = {"question": "budget", "claims": [{"text": "预算100元", "evidence_ids": ["read"]}]}
    state = {
        "parallel_plan": {"kind": "research"},
        "research_results": [analysis],
        "observations": [{"parallel_task_id": "read", "data": {"answer": "预算100元"}}],
    }
    assert not arbitration_packet(state)["issues"]
    issues = arbitration_packet(state, require_original=True)["issues"]
    assert len(issues) == 1 and len(issues[0]["candidates"]) == 1


def test_merge_cannot_disguise_same_scope_contradictory_facts() -> None:
    decisions = ArbitrationDecisions()
    analyses = [
        decisions.analyze_evidence(question=q, context={"evidence": {q: {}}})
        for q in ("cost", "risk")
    ]
    issues = research_issues(analyses)
    verdict = {
        "decisions": [
            {
                "issue_id": issues[0]["id"],
                "action": "merge",
                "reason": "both agree",
                "selected_ids": [c["id"] for c in issues[0]["candidates"]],
                "evidence": [{"id": "cost", "quote": "100元"}, {"id": "risk", "quote": "200元"}],
            }
        ]
    }
    with pytest.raises(ValueError, match="contradictory"):
        validate_verdict(verdict, issues, {"cost": "100元", "risk": "200元"}, "research")
