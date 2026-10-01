"""Deterministic acceptance scenarios using the production graph, without network IO.

These validate orchestration and delivery only; they do not measure model accuracy.
Run in a separate process so feature configuration cannot affect a live worker.
"""

from __future__ import annotations

import argparse
import json
import os
import time
import uuid
from typing import Any, Mapping, NoReturn

from langgraph.checkpoint.memory import InMemorySaver
from ai_companion_worker.agent_runtime import (
    AgentPlan,
    ModelDecision,
    ToolPreparation,
    build_graph,
    runtime_config,
)
from ai_companion_worker.agent_worker import _governance_output

SCENARIOS = ("resolved", "unresolved", "revoked", "write_denied", "off")
FOOTNOTE = "最终预算：100元。200元为历史草案，已由本批复明确替代。"
CATALOG = [
    {
        "name": "work_wiki_search",
        "description": "读取合成验收资料",
        "repeatable": True,
        "parameters": {
            "type": "object",
            "properties": {"query": {"type": "string"}},
            "required": ["query"],
            "additionalProperties": False,
        },
    }
]


class ScenarioTools:
    def __init__(self, case: str):
        self.case, self.revoked = case, False
        self.reads: list[dict[str, Any]] = []
        self.sources = {"a": "预算：100元", "b": "预算：200元", "footnote": FOOTNOTE}

    def prepare(self, **kwargs: Any) -> ToolPreparation:
        assert kwargs["tool_name"] == "work_wiki_search"
        self.reads.append(kwargs)
        query = kwargs["arguments"]["query"]
        key = query if query in {"a", "b"} else "footnote"
        if key == "footnote" and self.case == "unresolved":
            key = "a"
        if key == "footnote" and self.case == "revoked":
            self.revoked = True
        text = self.sources[key]
        return ToolPreparation(
            status="completed",
            tool_name=kwargs["tool_name"],
            risk_level="none",
            response=text,
            data={
                "text": text,
                "document_id": key,
                "chunk_id": key,
                "source_version": "fixture-v1",
            },
        )

    def validate_evidence(
        self, *, references: list[dict[str, str]], **kwargs: Any
    ) -> dict[str, str]:
        if self.revoked:
            raise PermissionError("synthetic source revoked")
        for ref in references:
            if (
                ref["source_id"] not in self.sources
                or ref["text"] not in self.sources[ref["source_id"]]
            ):
                raise ValueError("invalid fixture reference")
        return {r["source_id"]: "fixture-v1" for r in references}

    def commit(self, **kwargs: Any) -> NoReturn:
        raise AssertionError("debate must not perform writes")

    def observe(self, **kwargs: Any) -> NoReturn:
        raise AssertionError("scenario has no asynchronous tasks")

    def retry_task(self, **kwargs: Any) -> NoReturn:
        raise AssertionError("scenario must not retry tasks")


class ScenarioDecisions:
    def __init__(self, case: str):
        self.case = case

    def compose_arguments(self, **kwargs: Any) -> NoReturn:
        raise AssertionError("scenario does not compose writes")

    def revise_response(self, **kwargs: Any) -> NoReturn:
        raise AssertionError("scenario does not rewrite claims")

    def assess(self, **kwargs: Any) -> NoReturn:
        raise AssertionError("scenario completes via synthesis")

    def repair(self, **kwargs: Any) -> NoReturn:
        raise AssertionError("scenario does not repair tools")

    def plan(self, **kwargs: Any) -> AgentPlan:
        tasks = tuple(
            {
                "id": k,
                "question": k,
                "tool_name": "work_wiki_search",
                "arguments": {"query": k},
                "depends_on": [],
            }
            for k in ("a", "b")
        )
        return AgentPlan(
            "对照预算资料",
            ("分别读取", "核验分歧", "说明依据"),
            "依据不足时明确保留",
            research_tasks=tasks,
        )

    def decide(self, **kwargs: Any) -> ModelDecision:
        return ModelDecision(
            intent="search", tool_name="work_wiki_search", tool_arguments={"query": "a"}
        )

    def analyze_evidence(self, *, question: str, context: dict[str, Any]) -> dict[str, Any]:
        value = "100元" if question == "a" else "200元"
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

    def debate_position(self, *, packet: dict[str, Any], **kwargs: Any) -> dict[str, Any]:
        return {
            "assessments": [
                {
                    "issue_id": i["id"],
                    "candidate_id": c["id"],
                    "stance": "uncertain",
                    "reason": "核对原文与预算口径",
                    "evidence": [],
                }
                for i in packet["issues"]
                for c in i["candidates"]
            ],
            "novelty": [
                {
                    "issue_id": issue,
                    "evidence_id": key,
                    "quote": packet["evidence"][key],
                    "reason": "补充原文明确了草案和批复的替代关系",
                }
                for key in packet.get("new_evidence_ids", [])
                for issue in packet["new_evidence_issues"][key]
            ],
            "new_claims": [],
        }

    def arbitrate(self, *, packet: dict[str, Any], **kwargs: Any) -> dict[str, Any]:
        decisions = []
        for issue in packet["issues"]:
            selected = next(c for c in issue["candidates"] if c["fact"]["value"] == "100元")
            new = packet.get("new_evidence_ids", [])
            decision: dict[str, Any] = {
                "issue_id": issue["id"],
                "action": "accept" if new else "need_evidence",
                "classification": "version_update" if new else "unresolved",
                "selected_ids": [selected["id"]] if new else [],
                "reason": "最终批复明确替代草案" if new else "需要预算批复或适用范围说明",
                "evidence": [],
            }
            if new:
                refs = [
                    {"id": selected["evidence_ids"][0], "quote": "预算：100元"},
                    {"id": new[0], "quote": FOOTNOTE},
                ]
                decision["evidence"] = refs
                decision["assessment_responses"] = [
                    {"id": a["id"], "reason": "新增批复明确区分最终金额与草案", "evidence": refs}
                    for p in packet["positions"]
                    for a in p["assessments"]
                ]
            else:
                decision["evidence_requests"] = [
                    {
                        "question": "最终预算依据是什么",
                        "discriminates": "预算批复与草案",
                        "tool_name": "life_create_reminder"
                        if self.case == "write_denied"
                        else "work_wiki_search",
                        "arguments": {"query": "最终预算"},
                    }
                ]
            decisions.append(decision)
        return {"decisions": decisions}

    def respond(self, *, context: Mapping[str, Any], **kwargs: Any) -> str:
        claims = [c for a in context["research_results"] for c in a["claims"]]
        if claims:
            return "最终批复的预算为 100 元；200 元属于已被替代的历史草案。"
        return "两份资料的预算表述不同，目前缺少有效的新依据，不能确认唯一金额。"

    def review_candidate(self, **kwargs: Any) -> dict[str, Any]:
        return {"passed": True, "findings": []}


def run_scenario(case: str) -> dict[str, Any]:
    if case not in SCENARIOS:
        raise ValueError("unknown scenario")
    previous = os.environ.get("AGENT_DEBATE_MODE")
    os.environ["AGENT_DEBATE_MODE"] = "off" if case == "off" else "requested"
    try:
        tools = ScenarioTools(case)
        graph = build_graph(
            checkpointer=InMemorySaver(), decisions=ScenarioDecisions(case), tools=tools
        )
        run_id = "acceptance-" + uuid.uuid4().hex
        initial = {
            "run_id": run_id,
            "user_id": "synthetic-user",
            "conversation_id": "synthetic-conversation",
            "character_id": "synthetic-character",
            "module": "work",
            "user_message": "请独立核验两份预算资料，说明分歧及需要补充的依据。",
            "context": {"tools": CATALOG},
        }
        started = time.monotonic()
        state = graph.invoke(initial, runtime_config(run_id))
        output = _governance_output(state)
        return {
            "scenario": case,
            "synthetic": True,
            "external_model_calls": 0,
            "external_tool_calls": 0,
            "response": state["response"],
            "outcome": state["outcome"],
            "evidence_review": output["evidence_review"],
            "budget": output["budget"],
            "elapsed_ms": round((time.monotonic() - started) * 1000),
            "read_count": len(tools.reads),
            "nodes": [n["node"] for n in state["node_trace"]],
        }
    finally:
        if previous is None:
            os.environ.pop("AGENT_DEBATE_MODE", None)
        else:
            os.environ["AGENT_DEBATE_MODE"] = previous


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("case", choices=SCENARIOS)
    args = parser.parse_args()
    print(json.dumps(run_scenario(args.case), ensure_ascii=False))


if __name__ == "__main__":
    main()
