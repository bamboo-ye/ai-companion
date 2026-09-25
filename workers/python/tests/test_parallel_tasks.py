from __future__ import annotations

import threading
import os
import uuid
import json
from typing import Any

import pytest
from langgraph.checkpoint.memory import InMemorySaver
from langgraph.types import Command

from ai_companion_worker.agent_runtime import (
    AgentPlan,
    BudgetPolicy,
    ModelDecision,
    ToolOutcome,
    ToolPreparation,
    build_graph,
    interrupt_payloads,
    runtime_config,
)
from ai_companion_worker.parallel_tasks import validate_research_tasks, merge_task_results
from test_agent_runtime import FakeDecisions, agent_input


def read_definition(name: str, properties: dict[str, Any]) -> dict[str, Any]:
    return {
        "name": name,
        "description": "read evidence",
        "repeatable": True,
        "parameters": {"type": "object", "properties": properties, "additionalProperties": False},
    }


def query_catalog() -> list[dict[str, Any]]:
    return [read_definition("work_wiki_search", {"query": {"type": "string"}})]


def research_tasks() -> tuple[dict[str, Any], ...]:
    return tuple(
        {
            "id": k,
            "question": k,
            "tool_name": "work_wiki_search",
            "arguments": {"query": k},
            "depends_on": [],
        }
        for k in ("cost", "risk")
    )


class ResearchDecisions(FakeDecisions):
    def __init__(self) -> None:
        super().__init__(
            ModelDecision(
                intent="search", tool_name="work_wiki_search", tool_arguments={"query": "compare"}
            )
        )
        self.analysis_barrier = threading.Barrier(2)
        self.analyses: list[str] = []

    def plan(self, **kwargs: Any) -> AgentPlan:
        return AgentPlan(
            "compare", ("research", "synthesize"), "cited response", research_tasks=research_tasks()
        )

    def analyze_evidence(self, *, question: str, context: dict[str, Any]) -> dict[str, Any]:
        self.analyses.append(question)
        self.analysis_barrier.wait(timeout=3)
        return {"claims": [{"text": question, "evidence_ids": list(context["evidence"])}]}

    def respond(self, **kwargs: Any) -> str:
        assert len(kwargs["context"]["research_results"]) == 2
        return "比较结果：成本与风险均有对应来源。"


class ResearchTools:
    def __init__(self) -> None:
        self.barrier = threading.Barrier(2)
        self.prepared: list[dict[str, Any]] = []

    def prepare(self, **kwargs: Any) -> ToolPreparation:
        self.prepared.append(kwargs)
        self.barrier.wait(timeout=3)
        return ToolPreparation(
            status="completed",
            tool_name=kwargs["tool_name"],
            risk_level="none",
            response="evidence",
            data={"source": "doc", "text": kwargs["arguments"]["query"]},
        )

    def commit(self, **kwargs: Any) -> None:
        raise AssertionError("parallel tasks cannot commit writes")


def test_research_fans_out_reads_and_analysis_with_one_synthesis() -> None:
    decisions, tools = ResearchDecisions(), ResearchTools()
    graph = build_graph(checkpointer=InMemorySaver(), decisions=decisions, tools=tools)
    initial = agent_input("research", "work")
    initial["user_message"] = "比较两个方案的成本和风险"
    initial["context"]["tools"] = query_catalog()
    result = graph.invoke(initial, runtime_config("research"))
    assert result["outcome"] == "completed"
    assert len(tools.prepared) == 2 and len({t["idempotency_key"] for t in tools.prepared}) == 2
    assert result["budget_usage"]["actions"] == 2
    assert result["budget_usage"]["model_calls_by_node"]["execute_parallel_model"] == 2
    assert [r["question"] for r in result["research_results"]] == ["cost", "risk"]


def test_plan_rejects_writes_cycles_unknown_dependencies_and_invalid_arguments() -> None:
    tasks = list(research_tasks())
    for mutation in (
        {"tool_name": "life_create_reminder"},
        {"depends_on": ["missing"]},
        {"depends_on": ["cost"]},
        {"arguments": {"query": 4}},
    ):
        with pytest.raises(ValueError):
            validate_research_tasks([{**tasks[0], **mutation}, tasks[1]], query_catalog())
    assert (
        len(
            validate_research_tasks(
                [tasks[0], {**tasks[1], "depends_on": ["cost"]}], query_catalog()
            )
        )
        == 2
    )


class AttachmentTools:
    def __init__(self) -> None:
        self.prepared: list[dict[str, Any]] = []
        self.barrier = threading.Barrier(2)

    def prepare(self, **kwargs: Any) -> ToolPreparation:
        self.prepared.append(kwargs)
        index = kwargs["arguments"]["attachment_index"]
        round_start = kwargs["arguments"]["round_start"]
        if round_start == 1:
            self.barrier.wait(timeout=3)
            data = {"kind": "skill_run", "id": f"task-{index}", "status": "running"}
        else:
            data = {"output": {"has_more": False, "text": "tail", "round_start": round_start}}
        return ToolPreparation(
            status="completed",
            tool_name="work_extract_attached_document",
            risk_level="none",
            response="read",
            data=data,
        )

    def observe(self, **kwargs: Any) -> ToolOutcome:
        return ToolOutcome(
            response="finished",
            data={
                "kind": "skill_run",
                "id": kwargs["task_id"],
                "status": "succeeded",
                "output": {
                    "has_more": kwargs["task_id"] == "task-1",
                    "next_round": 2,
                    "text": "source",
                },
            },
        )


def test_attachments_launch_together_resume_without_replay_and_keep_ordered_rounds() -> None:
    tools = AttachmentTools()
    decisions = FakeDecisions(
        ModelDecision(
            intent="extract",
            tool_name="work_extract_attached_document",
            tool_arguments={"attachment_index": 1},
        )
    )
    graph = build_graph(checkpointer=InMemorySaver(), decisions=decisions, tools=tools)
    initial = agent_input("attachments", "work")
    initial["user_message"] = (
        "请读取全部附件\n<!--ai-document:11111111-1111-1111-1111-111111111111|a.txt-->\n<!--ai-document:22222222-2222-2222-2222-222222222222|b.txt-->"
    )
    initial["context"]["tools"] = [
        read_definition(
            "work_extract_attached_document",
            {
                "attachment_index": {"type": "integer", "minimum": 1, "maximum": 2},
                "round_start": {"type": "integer", "minimum": 1, "maximum": 100},
            },
        )
    ]
    config = runtime_config("attachments")
    result = graph.invoke(initial, config)
    interrupts = interrupt_payloads(result)
    assert len(interrupts) == 1 and len(interrupts[0]["task_ids"]) == 2
    assert len(tools.prepared) == 2
    result = graph.invoke(
        Command(resume={"type": "tool_poll", "task_id": interrupts[0]["task_id"]}), config
    )
    assert result["outcome"] == "completed"
    assert len(tools.prepared) == 3
    assert tools.prepared[-1]["arguments"] == {"attachment_index": 1, "round_start": 2}
    assert result["budget_usage"]["actions"] == 3
    assert len(result["observations"]) == 3


class ReviewDecisions(FakeDecisions):
    def __init__(self) -> None:
        super().__init__(ModelDecision(intent="work_no_tool", response="成本为十元，需核对来源。"))
        self.barrier = threading.Barrier(2)
        self.calls: list[str] = []

    def review_candidate(
        self, *, focus: str, candidate: str, context: dict[str, Any]
    ) -> dict[str, Any]:
        self.calls.append(focus)
        self.barrier.wait(timeout=3)
        return {"passed": True, "findings": []}


def test_specialists_run_only_when_requested(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("AGENT_SPECIALIST_REVIEW_MODE", "requested")
    for requested in (False, True):
        decisions = ReviewDecisions()
        graph = build_graph(
            checkpointer=InMemorySaver(), decisions=decisions, tools=ResearchTools()
        )
        initial = agent_input(f"review-{requested}", "work")
        initial["user_message"] = "请仔细核对" if requested else "你好"
        result = graph.invoke(initial, runtime_config(initial["run_id"]))
        assert result["outcome"] == "completed"
        assert len(decisions.calls) == (2 if requested else 0)
        if requested:
            assert result["specialist_review"]["passed"] is True


def test_conflicting_branch_results_are_not_last_writer_wins() -> None:
    with pytest.raises(ValueError, match="conflicting"):
        merge_task_results(
            [{"id": "a", "generation": 1, "status": "completed"}],
            [{"id": "a", "generation": 1, "status": "failed"}],
        )


def test_action_budget_is_reserved_before_any_parallel_tool_runs() -> None:
    tools = ResearchTools()
    graph = build_graph(
        checkpointer=InMemorySaver(),
        decisions=ResearchDecisions(),
        tools=tools,
        budget_policy=BudgetPolicy(max_actions=1),
    )
    initial = agent_input("limited", "work")
    initial["context"]["tools"] = query_catalog()
    result = graph.invoke(initial, runtime_config("limited"))
    assert result["outcome"] == "action_limit" and not tools.prepared


def test_failed_read_retries_only_its_branch_and_keeps_successful_siblings() -> None:
    class RetryableError(Exception):
        retryable = True

    class Decisions(ResearchDecisions):
        def analyze_evidence(self, *, question: str, context: dict[str, Any]) -> dict[str, Any]:
            self.analyses.append(question)
            return {"claims": [{"text": question, "evidence_ids": list(context["evidence"])}]}

    class Tools(ResearchTools):
        def prepare(self, **kwargs: Any) -> ToolPreparation:
            self.prepared.append(kwargs)
            query = kwargs["arguments"]["query"]
            if (
                query == "risk"
                and sum(t["arguments"]["query"] == "risk" for t in self.prepared) == 1
            ):
                raise RetryableError("temporary")
            return ToolPreparation(
                status="completed",
                tool_name=kwargs["tool_name"],
                risk_level="none",
                response=query,
                data={"source": query},
            )

    tools, decisions = Tools(), Decisions()
    graph = build_graph(checkpointer=InMemorySaver(), decisions=decisions, tools=tools)
    initial = agent_input("retry-branch", "work")
    initial["context"]["tools"] = query_catalog()
    result = graph.invoke(initial, runtime_config("retry-branch"))
    assert result["outcome"] == "completed"
    assert sorted(t["arguments"]["query"] for t in tools.prepared) == ["cost", "risk", "risk"]
    assert sorted(decisions.analyses) == ["cost", "risk"]
    assert result["budget_usage"]["actions"] == 2 and len(result["observations"]) == 2
    retries = [t["idempotency_key"] for t in tools.prepared if t["arguments"]["query"] == "risk"]
    assert retries[0] == retries[1]


@pytest.mark.parametrize("repair", [False, True])
def test_specialist_findings_are_rechecked_after_one_revision(
    repair: bool, monkeypatch: pytest.MonkeyPatch
) -> None:
    monkeypatch.setenv("AGENT_SPECIALIST_REVIEW_MODE", "requested")

    class Decisions(ReviewDecisions):
        def review_candidate(
            self, *, focus: str, candidate: str, context: dict[str, Any]
        ) -> dict[str, Any]:
            self.calls.append(focus)
            if focus == "grounding" and "十元" in candidate:
                return {
                    "passed": False,
                    "findings": [{"quote": "十元", "issue": "缺少价格来源", "evidence_ids": []}],
                }
            return {"passed": True, "findings": []}

        def revise_response(self, **kwargs: Any) -> str:
            assert kwargs["violations"][0]["code"] == "specialist_review"
            return "来源尚未给出价格，需核对。" if repair else kwargs["response"]

    decisions = Decisions()
    graph = build_graph(checkpointer=InMemorySaver(), decisions=decisions, tools=ResearchTools())
    initial = agent_input(f"review-fix-{repair}", "work")
    initial["user_message"] = "请仔细核对"
    result = graph.invoke(initial, runtime_config(initial["run_id"]))
    assert result["outcome"] == ("completed" if repair else "specialist_review_failed")
    assert len(decisions.calls) == 4 and result["specialist_review"]["attempts"] == 2


def test_analysis_cannot_introduce_an_unknown_evidence_reference() -> None:
    class Decisions(ResearchDecisions):
        def analyze_evidence(self, **kwargs: Any) -> dict[str, Any]:
            return {"claims": [{"text": "invented", "evidence_ids": ["another-users-document"]}]}

    graph = build_graph(checkpointer=InMemorySaver(), decisions=Decisions(), tools=ResearchTools())
    initial = agent_input("bad-evidence", "work")
    initial["context"]["tools"] = query_catalog()
    result = graph.invoke(initial, runtime_config(initial["run_id"]))
    assert result["outcome"] == "parallel_task_failed"
    assert not result.get("research_results")
    assert result["budget_usage"]["model_calls_by_node"]["execute_parallel_model"] == 4


def test_dependent_research_waits_for_parent_analysis() -> None:
    order: list[str] = []

    class Decisions(ResearchDecisions):
        def plan(self, **kwargs: Any) -> AgentPlan:
            tasks = list(research_tasks())
            tasks[1] = {**tasks[1], "depends_on": ["cost"]}
            return AgentPlan("compare", ("gather", "answer"), "cited", research_tasks=tuple(tasks))

        def analyze_evidence(self, *, question: str, context: dict[str, Any]) -> dict[str, Any]:
            order.append("analysis_" + question)
            if question == "risk":
                assert "analysis_cost" in context["evidence"]
            return {"claims": [{"text": question, "evidence_ids": list(context["evidence"])}]}

    class Tools(ResearchTools):
        def prepare(self, **kwargs: Any) -> ToolPreparation:
            query = kwargs["arguments"]["query"]
            order.append("read_" + query)
            return ToolPreparation(
                status="completed", tool_name=kwargs["tool_name"], risk_level="none", response=query
            )

    graph = build_graph(checkpointer=InMemorySaver(), decisions=Decisions(), tools=Tools())
    initial = agent_input("dependent-research", "work")
    initial["context"]["tools"] = query_catalog()
    result = graph.invoke(initial, runtime_config(initial["run_id"]))
    assert result["outcome"] == "completed"
    assert order == ["read_cost", "analysis_cost", "read_risk", "analysis_risk"]


def test_branch_budget_overrun_is_charged_and_never_retried() -> None:
    class Decisions(ResearchDecisions):
        def __init__(self) -> None:
            super().__init__()
            self.events = threading.local()

        def analyze_evidence(self, *, question: str, context: dict[str, Any]) -> dict[str, Any]:
            self.analyses.append(question)
            self.events.pending = [
                {
                    "kind": "model_call",
                    "role": "composer",
                    "status": "succeeded",
                    "prompt_tokens": 1000000,
                }
            ]
            return {"claims": []}

        def consume_observability(self) -> list[dict[str, Any]]:
            pending = getattr(self.events, "pending", [])
            self.events.pending = []
            return pending

    decisions = Decisions()
    graph = build_graph(checkpointer=InMemorySaver(), decisions=decisions, tools=ResearchTools())
    initial = agent_input("branch-overrun", "work")
    initial["context"]["tools"] = query_catalog()
    result = graph.invoke(initial, runtime_config(initial["run_id"]))
    assert result["outcome"] == "parallel_task_failed"
    assert sorted(decisions.analyses) == ["cost", "risk"]
    assert result["budget_usage"]["prompt_tokens"] >= 2000000
    assert all(
        r["error"] == "branch_model_budget_exceeded"
        for r in result["parallel_results"]
        if r["id"].startswith("analysis_")
    )


def test_openrouter_planner_and_specialists_use_structured_read_contracts() -> None:
    from test_openrouter_decision import StubOpenRouter

    def reply(value: dict[str, Any]) -> dict[str, Any]:
        return {"choices": [{"message": {"content": json.dumps(value)}}]}

    port = StubOpenRouter(
        [
            reply(
                {
                    "objective": "compare",
                    "steps": ["gather", "answer"],
                    "success_criteria": "cited",
                    "research_tasks": list(research_tasks()),
                }
            ),
            reply({"claims": [{"text": "cost", "evidence_ids": ["read_cost"]}]}),
            reply({"passed": True, "findings": []}),
        ]
    )
    plan = port.plan(module="work", message="compare", context={"tools": query_catalog()})
    assert len(plan.research_tasks) == 2
    context = {"evidence": {"read_cost": {"text": "cost"}}}
    assert port.analyze_evidence(question="cost", context=context)["claims"]
    assert port.review_candidate(focus="grounding", candidate="cost", context=context)["passed"]
    assert all(
        request["response_format"] == {"type": "json_object"} and "tools" not in request
        for request in port.requests
    )


def test_parallel_attachment_checkpoints_survive_postgres_reconnect() -> None:
    dsn = os.getenv("LANGGRAPH_POSTGRES_TEST_DSN")
    if not dsn:
        pytest.skip("LANGGRAPH_POSTGRES_TEST_DSN is not set")
    from langgraph.checkpoint.postgres import PostgresSaver

    run_id = "parallel-postgres-" + uuid.uuid4().hex
    initial = agent_input(run_id, "work")
    initial["user_message"] = "读取全部附件 <!--ai-document:a|a.txt--> <!--ai-document:b|b.txt-->"
    initial["context"]["tools"] = [
        read_definition(
            "work_extract_attached_document",
            {
                "attachment_index": {"type": "integer", "minimum": 1, "maximum": 2},
                "round_start": {"type": "integer", "minimum": 1, "maximum": 100},
            },
        )
    ]
    tools = AttachmentTools()

    def decisions() -> FakeDecisions:
        return FakeDecisions(
            ModelDecision(
                intent="extract",
                tool_name="work_extract_attached_document",
                tool_arguments={"attachment_index": 1},
            )
        )

    with PostgresSaver.from_conn_string(dsn) as saver:
        saver.setup()
        graph = build_graph(checkpointer=saver, decisions=decisions(), tools=tools)
        result = graph.invoke(initial, runtime_config(run_id))
        waiting = interrupt_payloads(result)
        assert len(waiting) == 1 and len(tools.prepared) == 2
    with PostgresSaver.from_conn_string(dsn) as saver:
        graph = build_graph(checkpointer=saver, decisions=decisions(), tools=tools)
        result = graph.invoke(
            Command(resume={"type": "tool_poll", "task_id": waiting[0]["task_id"]}),
            runtime_config(run_id),
        )
        assert result["outcome"] == "completed" and len(tools.prepared) == 3
        assert len(result["observations"]) == 3
