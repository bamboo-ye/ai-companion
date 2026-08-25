from __future__ import annotations

import unittest
from typing import Any, Mapping

from langgraph.checkpoint.memory import InMemorySaver

from ai_companion_worker.agent_governance import BudgetPolicy, GRAPH_VERSION
from ai_companion_worker.agent_runtime import (
    AgentAssessment,
    AgentPlan,
    AgentRuntime,
    ModelDecision,
    ModuleKey,
    RepairDecision,
    ToolOutcome,
    ToolPreparation,
    _document_source_coverage,
    _latest_document_continuation,
    _merge_presentation_arguments,
    _normalize_presentation_arguments,
    _presentation_document_batches,
    _validate_checkpoint_identity,
    _validate_tool_arguments,
    build_graph,
    interrupt_payloads,
    runtime_config,
    tool_allowed,
)


class FakeDecisions:
    def __init__(self, decision: ModelDecision) -> None:
        self.decision = decision
        self.modules: list[str] = []

    def decide(
        self,
        *,
        module: ModuleKey,
        message: str,
        context: Mapping[str, Any],
    ) -> ModelDecision:
        del message
        self.modules.append(module)
        observations = context.get("observations")
        if self.decision.tool_name and isinstance(observations, list) and observations:
            latest = observations[-1]
            response = (
                str(latest.get("response") or "工具结果已返回。")
                if isinstance(latest, dict)
                else "工具结果已返回。"
            )
            return ModelDecision(intent=f"{module}_no_tool", response=response)
        return self.decision

    def plan(
        self,
        *,
        module: ModuleKey,
        message: str,
        context: Mapping[str, Any],
    ) -> AgentPlan:
        del module, context
        return AgentPlan(
            objective=message,
            steps=("执行工具", "观察结果", "交付结果"),
            success_criteria="返回真实工具结果。",
        )

    def assess(
        self,
        *,
        module: ModuleKey,
        message: str,
        context: Mapping[str, Any],
    ) -> AgentAssessment:
        del module, message
        observations = context.get("observations")
        latest_tool = ""
        if isinstance(observations, list) and observations:
            latest = observations[-1]
            if isinstance(latest, dict):
                latest_tool = str(latest.get("tool_name") or "")
        status = (
            "continue"
            if latest_tool in ("work_extract_attached_document", "work_create_pptx_outline")
            else "completed"
        )
        return AgentAssessment(status=status, reason="测试进度评估")


class FakeTools:
    def __init__(
        self,
        preparation: ToolPreparation,
        observations: list[ToolOutcome] | None = None,
    ) -> None:
        self.preparation = preparation
        self.observations = observations or []
        self.prepared: list[dict[str, Any]] = []
        self.committed: list[dict[str, Any]] = []
        self.retried: list[dict[str, Any]] = []

    def prepare(self, **values: Any) -> ToolPreparation:
        self.prepared.append(dict(values))
        return self.preparation

    def commit(self, **values: Any) -> ToolOutcome:
        self.committed.append(dict(values))
        return ToolOutcome(response="操作已确认并完成。", data={"written": True})

    def observe(self, **_values: Any) -> ToolOutcome:
        if not self.observations:
            raise AssertionError("unexpected task observation")
        return self.observations.pop(0)

    def retry_task(self, **values: Any) -> ToolOutcome:
        self.retried.append(dict(values))
        raise AssertionError("unexpected task retry")


class PresentationGateDecisions(FakeDecisions):
    def __init__(self) -> None:
        super().__init__(
            ModelDecision(
                intent="work_generate_pptx",
                tool_name="work_generate_pptx",
                tool_arguments={
                    "title": "课程表",
                    "audience": "学生",
                    "style": "表格",
                    "brief": "所有课程",
                    "slide_count": 4,
                },
            )
        )


def agent_input(run_id: str, module: ModuleKey = "life") -> dict[str, Any]:
    return {
        "run_id": run_id,
        "user_id": "00000000-0000-0000-0000-000000000001",
        "conversation_id": "00000000-0000-0000-0000-000000000002",
        "character_id": "00000000-0000-0000-0000-000000000003",
        "module": module,
        "user_message": "今天午饭花了50元",
        "context": {"timezone": "Asia/Shanghai"},
    }


def complete_english_email_arguments() -> dict[str, Any]:
    return {
        "to": [],
        "subject": "Question About Semester A Project Courses",
        "purpose": "Ask whether a Project course may be taken in Semester A.",
        "output_language": "en-US",
        "relationship": "first_contact",
        "introduction_policy": "required",
        "sender_name": "Alex Chen",
        "salutation": "Dear Sir or Madam,",
        "introduction": "My name is Alex Chen, and I am a prospective student.",
        "body_paragraphs": [
            "I am planning my Semester A schedule and am considering the Project course."
        ],
        "request_or_next_step": "Could you please let me know whether any prerequisites apply?",
        "courtesy": "Thank you for your time and assistance.",
        "closing": "Kind regards,",
        "signature_lines": ["Alex Chen"],
        "tone": "formal",
    }


def email_tool_definition() -> dict[str, Any]:
    arguments = complete_english_email_arguments()
    properties: dict[str, Any] = {}
    for key, value in arguments.items():
        if isinstance(value, list):
            properties[key] = {"type": "array", "items": {"type": "string"}}
        else:
            properties[key] = {"type": "string"}
    properties["introduction"] = {"type": "string"}
    return {
        "name": "work_draft_email",
        "description": "生成经过质量门禁的完整邮件草稿",
        "compose_arguments": True,
        "parameters": {
            "type": "object",
            "required": [key for key in arguments if key != "to"],
            "properties": properties,
            "additionalProperties": False,
        },
    }


class AgentRuntimeTest(unittest.TestCase):
    def runtime(
        self,
        decision: ModelDecision,
        preparation: ToolPreparation | None = None,
    ) -> tuple[AgentRuntime, FakeDecisions, FakeTools]:
        decisions = FakeDecisions(decision)
        tools = FakeTools(
            preparation
            or ToolPreparation(
                status="completed",
                tool_name=decision.tool_name,
                response="查询完成。",
            )
        )
        graph = build_graph(
            checkpointer=InMemorySaver(),
            decisions=decisions,
            tools=tools,
        )
        return AgentRuntime(graph), decisions, tools

    def test_thread_id_is_exactly_agent_run_id(self) -> None:
        self.assertEqual(
            runtime_config("run-123")["configurable"]["thread_id"],
            "run-123",
        )

    def test_generated_pptx_is_not_terminal_without_quality_evidence(self) -> None:
        decisions = PresentationGateDecisions()
        tools = FakeTools(
            ToolPreparation(
                status="completed",
                tool_name="work_generate_pptx",
                response="工作任务已执行完成。",
                data={
                    "kind": "skill_run",
                    "id": "pptx-low-quality",
                    "skill_name": "office.pptx_generate",
                    "status": "succeeded",
                    "output": {"source_coverage": {"coverage_ratio": 0.5, "truncated": True}},
                    "files": [{"name": "partial.pptx"}],
                },
            )
        )
        graph = build_graph(checkpointer=InMemorySaver(), decisions=decisions, tools=tools)
        payload = agent_input("run-pptx-quality-failed", "work")
        payload["user_message"] = (
            "整理所有课程代码和时间并生成中文PPT\n<!--ai-document:doc-1|courses.pdf-->"
        )
        result = AgentRuntime(graph).start(payload)
        self.assertEqual(result["outcome"], "artifact_quality_failed")
        self.assertFalse(result["artifact_validation"]["passed"])
        self.assertIn("未通过", result["response"])

    def test_generated_pptx_with_quality_evidence_can_complete(self) -> None:
        decisions = PresentationGateDecisions()
        tools = FakeTools(
            ToolPreparation(
                status="completed",
                tool_name="work_generate_pptx",
                response="工作任务已执行完成。",
                data={
                    "kind": "skill_run",
                    "id": "pptx-verified",
                    "skill_name": "office.pptx_generate",
                    "status": "succeeded",
                    "output": {
                        "quality_report": {"passed": True, "violations": []},
                        "source_coverage": {"coverage_ratio": 1.0, "truncated": False},
                    },
                    "files": [{"name": "verified.pptx"}],
                },
            )
        )
        graph = build_graph(checkpointer=InMemorySaver(), decisions=decisions, tools=tools)
        result = AgentRuntime(graph).start(agent_input("run-pptx-quality-passed", "work"))
        self.assertEqual(result["outcome"], "completed")
        self.assertTrue(result["artifact_validation"]["passed"])

    def test_text_response_cannot_complete_an_explicit_artifact_goal(self) -> None:
        graph = build_graph(
            checkpointer=InMemorySaver(),
            decisions=FakeDecisions(
                ModelDecision(intent="work_no_tool", response="课程已经整理完成。")
            ),
            tools=FakeTools(ToolPreparation(status="completed", tool_name="", response="")),
        )
        payload = agent_input("run-artifact-missing", "work")
        payload["user_message"] = "整理全部体育课并生成中文 PPT"
        result = AgentRuntime(graph).start(payload)
        self.assertEqual(result["outcome"], "artifact_missing")
        self.assertIn("尚未产出", result["response"])

    def test_response_revision_failure_remains_terminal(self) -> None:
        class FailedRevisionDecisions(FakeDecisions):
            def __init__(self) -> None:
                super().__init__(
                    ModelDecision(
                        intent="work_no_tool",
                        response=(
                            "体育课程已经完成整理，包含课程名称、上课时间和课程代码。\n\n"
                            "体育课程已经完成整理，包含课程名称、上课时间以及课程代码。"
                        ),
                    )
                )
                self.events: list[dict[str, Any]] = []

            def consume_observability(self) -> list[dict[str, Any]]:
                events, self.events = self.events, []
                return events

            def revise_response(self, **_values: Any) -> str:
                self.events.append(
                    {
                        "kind": "model_call",
                        "role": "responder",
                        "status": "succeeded",
                        "prompt_tokens": 100,
                        "completion_tokens": 1024,
                        "reasoning_tokens": 1024,
                        "cost_micros": 5,
                        "error_status": 0,
                        "retryable": False,
                    }
                )
                raise RuntimeError("empty revised response")

        decisions = FailedRevisionDecisions()
        graph = build_graph(
            checkpointer=InMemorySaver(),
            decisions=decisions,
            tools=FakeTools(ToolPreparation(status="completed", tool_name="", response="")),
        )
        payload = agent_input("run-revision-terminal", "work")
        payload["user_message"] = "请总结课程信息"
        result = AgentRuntime(graph).start(payload)
        self.assertEqual(result["outcome"], "model_invalid_response")
        self.assertIn("未通过节点契约", result["response"])
        revise_index = next(
            index
            for index, item in enumerate(result["node_trace"])
            if item["node"] == "revise_response"
        )
        self.assertNotIn(
            "response_quality_gate",
            [item["node"] for item in result["node_trace"][revise_index + 1 :]],
        )

    def test_document_coverage_deduplicates_retried_round_windows(self) -> None:
        observation = {
            "tool_name": "work_extract_attached_document",
            "arguments": {"attachment_index": 1, "round_start": 1},
            "data": {
                "output": {
                    "selected_chunk_count": 5,
                    "total_chunk_count": 10,
                    "completed_rounds": 1,
                    "round_count": 2,
                    "round_start": 1,
                    "next_round": 2,
                    "rounds": [
                        {
                            "round_no": 1,
                            "chunk_start": 0,
                            "chunk_end": 4,
                            "chunk_count": 5,
                        }
                    ],
                }
            },
        }
        coverage = _document_source_coverage({"observations": [observation, dict(observation)]})
        self.assertEqual(coverage["selected_chunk_count"], 5)
        self.assertEqual(coverage["completed_rounds"], 1)
        self.assertEqual(coverage["coverage_ratio"], 0.5)

    def test_document_coverage_waits_for_every_bound_attachment(self) -> None:
        coverage = _document_source_coverage(
            {
                "task_contract": {
                    "source_required": True,
                    "source_document_ids": ["doc-1", "doc-2"],
                },
                "observations": [
                    {
                        "tool_name": "work_extract_attached_document",
                        "arguments": {"attachment_index": 1},
                        "data": {
                            "output": {
                                "selected_chunk_count": 4,
                                "total_chunk_count": 4,
                                "completed_rounds": 1,
                                "round_count": 1,
                                "round_start": 1,
                                "next_round": 0,
                            }
                        },
                    }
                ],
            }
        )
        self.assertEqual(coverage["coverage_ratio"], 0.5)
        self.assertTrue(coverage["truncated"])
        self.assertEqual(coverage["completed_attachment_count"], 1)
        self.assertEqual(coverage["expected_attachment_count"], 2)
        self.assertTrue(coverage["truncated"])

    def test_oversized_document_rounds_are_preserved_and_merged_without_summary(self) -> None:
        state: dict[str, Any] = {
            "task_contract": {
                "exhaustive": True,
                "requested_fields": ["code", "name", "time"],
                "output_language": "zh-CN",
            },
            "plan": {"objective": "整理全部课程并生成中文 PPT"},
            "observations": [
                {
                    "tool_name": "work_extract_attached_document",
                    "arguments": {"attachment_index": 1, "round_start": 1},
                    "data": {
                        "output": {
                            "source_filename": "courses.pdf",
                            "text": (
                                "[[DOCUMENT ROUND 1]]\n[[PAGE 1]]\n"
                                "PED1101 Canoeing 10:00-11:50\n"
                                "[[DOCUMENT ROUND 2]]\n[[PAGE 2]]\n"
                                "PED1102 Swimming 14:00-14:50"
                            ),
                            "rounds": [
                                {"round_no": 1, "token_count": 100},
                                {"round_no": 2, "token_count": 100},
                            ],
                        }
                    },
                }
            ],
        }
        batches = _presentation_document_batches(state)  # type: ignore[arg-type]
        self.assertEqual([item["batch_id"] for item in batches], ["a1:r1", "a1:r2"])
        self.assertNotIn("PED1102", batches[0]["processing_text"])
        self.assertIn("[[PREVIOUS ROUND OVERLAP", batches[1]["processing_text"])

        first = _normalize_presentation_arguments(
            {
                "title": "课程表",
                "audience": "学生",
                "style": "表格",
                "brief": "第一轮",
                "slide_count": 3,
                "table": {
                    "title": "全部课程",
                    "columns": ["课程代码", "课程名称", "上课时间", "来源位置"],
                    "source_locator": "Page 1",
                    "rows": [
                        {
                            "cells": ["PED1101", "Canoeing", "10:00-11:50"],
                            "source_locator": "Page 1",
                        }
                    ],
                },
            },
            state,  # type: ignore[arg-type]
        )
        self.assertNotIn("source_locator", first["table"])
        self.assertEqual(first["table"]["columns"], ["课程代码", "课程名称", "上课时间"])
        self.assertEqual(len(first["table"]["rows"][0]["cells"]), 3)

        merged = _merge_presentation_arguments(
            first,
            {
                "title": "课程表",
                "audience": "学生",
                "style": "表格",
                "brief": "第二轮",
                "slide_count": 3,
                "table": {
                    "title": "全部课程",
                    "columns": ["课程代码", "课程名称", "上课时间"],
                    "rows": [
                        {
                            "cells": ["PED1101", "Canoeing", "10:00-11:50"],
                            "source_locator": "Page 1 overlap",
                        },
                        {
                            "cells": ["PED1102", "Swimming", "14:00-14:50"],
                            "source_locator": "Page 2",
                        },
                    ],
                },
            },
            state,  # type: ignore[arg-type]
        )
        self.assertEqual(len(merged["table"]["rows"]), 2)
        self.assertIn("Page 1 overlap", merged["table"]["rows"][0]["source_locator"])
        self.assertIn("2 条来源记录", merged["brief"])

    def test_document_continuation_uses_exact_next_round(self) -> None:
        state = {
            "observations": [
                {
                    "tool_name": "work_extract_attached_document",
                    "arguments": {"attachment_index": 2, "round_start": 5},
                    "data": {"output": {"has_more": True, "next_round": 9}},
                }
            ]
        }
        self.assertEqual(
            _latest_document_continuation(state),  # type: ignore[arg-type]
            {"attachment_index": 2, "round_start": 9},
        )

    def test_exhaustive_ppt_composition_checkpoints_each_document_round(self) -> None:
        class RoundDecisions(FakeDecisions):
            def __init__(self) -> None:
                super().__init__(ModelDecision(intent="unused"))
                self.composed_batches: list[str] = []

            def decide(self, **values: Any) -> ModelDecision:
                observations = values["context"].get("observations", [])
                if not observations:
                    return ModelDecision(
                        intent="extract",
                        tool_name="work_extract_attached_document",
                        tool_arguments={"attachment_index": 1, "round_start": 1},
                    )
                return ModelDecision(
                    intent="generate_pptx",
                    tool_name="work_generate_pptx",
                    requires_argument_composition=True,
                )

            def compose_arguments(self, **values: Any) -> Mapping[str, Any]:
                context = values["context"]
                batch = context["document_processing_round"]
                self.composed_batches.append(str(batch["batch_id"]))
                text = context["observations"][0]["data"]["output"]["text"]
                if "PED1102" in text and "[[CURRENT ROUND]]" in text:
                    code, name, schedule, locator = (
                        "PED1102",
                        "Swimming",
                        "周四 14:00-14:50",
                        "Page 2",
                    )
                else:
                    code, name, schedule, locator = (
                        "PED1101",
                        "Canoeing",
                        "周三 10:00-11:50",
                        "Page 1",
                    )
                return {
                    "title": "体育课课程表",
                    "audience": "学生",
                    "style": "表格",
                    "brief": "当前轮次",
                    "slide_count": 3,
                    "table": {
                        "columns": ["课程代码", "课程名称", "上课时间"],
                        "rows": [
                            {
                                "cells": [code, name, schedule],
                                "source_locator": locator,
                            }
                        ],
                    },
                }

            def assess(self, **_values: Any) -> AgentAssessment:
                return AgentAssessment(status="continue", reason="继续生成 PPT")

        class RoundTools(FakeTools):
            def prepare(self, **values: Any) -> ToolPreparation:
                self.prepared.append(dict(values))
                if values["tool_name"] == "work_extract_attached_document":
                    return ToolPreparation(
                        status="completed",
                        tool_name="work_extract_attached_document",
                        response="文档已完整提取。",
                        data={
                            "output": {
                                "source_filename": "courses.pdf",
                                "text": (
                                    "[[DOCUMENT ROUND 1]]\n[[PAGE 1]]\n"
                                    "PED1101 Canoeing 10:00-11:50\n"
                                    "[[DOCUMENT ROUND 2]]\n[[PAGE 2]]\n"
                                    "PED1102 Swimming 14:00-14:50"
                                ),
                                "rounds": [
                                    {"round_no": 1, "token_count": 100},
                                    {"round_no": 2, "token_count": 100},
                                ],
                                "selected_chunk_count": 2,
                                "total_chunk_count": 2,
                                "completed_rounds": 2,
                                "round_count": 2,
                                "round_start": 1,
                                "next_round": 3,
                                "has_more": False,
                                "truncated": False,
                                "coverage_ratio": 1.0,
                            }
                        },
                    )
                arguments = values["arguments"]
                rows = arguments["table"]["rows"]
                return ToolPreparation(
                    status="completed",
                    tool_name="work_generate_pptx",
                    response="PPT 已生成。",
                    data={
                        "kind": "skill_run",
                        "id": "round-pptx",
                        "skill_name": "office.pptx_generate",
                        "status": "succeeded",
                        "output": {
                            "outline": [
                                {
                                    "page": 2,
                                    "table": {
                                        "columns": arguments["table"]["columns"],
                                        "row_count": len(rows),
                                        "source_locators": [row["source_locator"] for row in rows],
                                    },
                                }
                            ],
                            "quality_report": {
                                "passed": True,
                                "violations": [],
                                "table_row_count": len(rows),
                            },
                            "source_coverage": {
                                "coverage_ratio": 1.0,
                                "truncated": False,
                            },
                            "source_overwritten": False,
                        },
                        "files": [{"name": "courses.pptx"}],
                    },
                )

        decisions = RoundDecisions()
        tools = RoundTools(ToolPreparation(status="completed", tool_name=""))
        runtime = AgentRuntime(
            build_graph(
                checkpointer=InMemorySaver(),
                decisions=decisions,
                tools=tools,
            )
        )
        payload = agent_input("run-round-processing", "work")
        payload["user_message"] = (
            "整理所有体育课的名称、上课时间和课程代码，并用中文PPT展示"
            "\n<!--ai-document:doc-1|courses.pdf-->"
        )
        payload["context"]["tools"] = [
            {
                "name": "work_extract_attached_document",
                "description": "提取附件",
                "parameters": {
                    "type": "object",
                    "required": ["attachment_index"],
                    "properties": {
                        "attachment_index": {"type": "integer"},
                        "round_start": {"type": "integer"},
                    },
                    "additionalProperties": False,
                },
                "repeatable": True,
                "identity_fields": ["attachment_index", "round_start"],
            },
            {
                "name": "work_generate_pptx",
                "description": "生成 PPTX",
                "compose_arguments": True,
                "parameters": {
                    "type": "object",
                    "required": ["title", "audience", "style", "brief", "slide_count"],
                    "properties": {
                        "title": {"type": "string"},
                        "audience": {"type": "string"},
                        "style": {"type": "string"},
                        "brief": {"type": "string"},
                        "slide_count": {"type": "integer"},
                        "table": {"type": "object"},
                        "task_contract": {"type": "object"},
                        "source_coverage": {"type": "object"},
                    },
                    "additionalProperties": False,
                },
            },
        ]
        result = runtime.start(payload)
        self.assertEqual(result["outcome"], "completed")
        self.assertEqual(decisions.composed_batches, ["a1:r1", "a1:r2"])
        ppt_arguments = next(
            item["arguments"]
            for item in tools.prepared
            if item["tool_name"] == "work_generate_pptx"
        )
        self.assertEqual(
            [row["cells"][0] for row in ppt_arguments["table"]["rows"]],
            ["PED1101", "PED1102"],
        )
        self.assertTrue(result["document_processing"]["complete"])
        self.assertEqual(result["document_processing"]["merged_record_count"], 2)

    def test_tool_schema_rejects_non_finite_values_and_unsupported_keywords(self) -> None:
        with self.assertRaisesRegex(ValueError, "finite"):
            _validate_tool_arguments(
                {"score": float("nan")},
                {
                    "parameters": {
                        "type": "object",
                        "properties": {"score": {"type": "number"}},
                    }
                },
            )
        with self.assertRaisesRegex(ValueError, "unsupported"):
            _validate_tool_arguments(
                {"value": "x"},
                {
                    "parameters": {
                        "type": "object",
                        "properties": {"value": {"$ref": "#/$defs/value"}},
                        "$defs": {"value": {"type": "string"}},
                    }
                },
            )

    def test_old_graph_checkpoint_is_rejected_before_resume(self) -> None:
        with self.assertRaisesRegex(ValueError, "graph version"):
            _validate_checkpoint_identity(
                {"graph_version": "3.1.0"},
                run_id="old-run",
                initial=None,
            )

    def test_supervisor_routes_to_module_subgraph(self) -> None:
        runtime, decisions, tools = self.runtime(
            ModelDecision(intent="casual_chat", response="我在，慢慢说。")
        )
        result = runtime.start(agent_input("run-companion", "companion"))
        self.assertEqual(decisions.modules, ["companion"])
        self.assertEqual(result["response"], "我在，慢慢说。")
        self.assertEqual(result["outcome"], "completed")
        self.assertEqual(tools.prepared, [])

    def test_module_allowlist_blocks_cross_module_tool(self) -> None:
        runtime, _, tools = self.runtime(
            ModelDecision(
                intent="document_translate",
                tool_name="office.pdf_translate",
                tool_arguments={"document_id": "document-1"},
            )
        )
        result = runtime.start(agent_input("run-life-denied", "life"))
        self.assertEqual(result["outcome"], "tool_denied")
        self.assertIn("无权", result["response"])
        self.assertEqual(tools.prepared, [])

    def test_read_tool_completes_without_interrupt(self) -> None:
        runtime, _, tools = self.runtime(
            ModelDecision(intent="plan_query", tool_name="life.query_today_plan"),
            ToolPreparation(
                status="completed",
                tool_name="life.query_today_plan",
                response="今天有一项计划。",
                data={"count": 1},
            ),
        )
        result = runtime.start(agent_input("run-query"))
        self.assertEqual(interrupt_payloads(result), [])
        self.assertEqual(result["response"], "今天有一项计划。")
        self.assertEqual(len(tools.prepared), 1)
        self.assertEqual(tools.committed, [])

    def test_requires_plan_tool_runs_planner_before_precondition_check(self) -> None:
        runtime, _, tools = self.runtime(
            ModelDecision(
                intent="task_completion",
                tool_name="life_prepare_task_completion",
                tool_arguments={"title": "选课", "date_hint": "8月10号"},
            ),
            ToolPreparation(
                status="completed",
                tool_name="life_prepare_task_completion",
                response="检查后没有找到唯一匹配，未执行完成操作。",
                data={"status": "ambiguous", "candidates": []},
            ),
        )
        payload = agent_input("run-completion-plan")
        payload["user_message"] = "我完成了8月10号的选课"
        payload["context"]["tools"] = [
            {
                "name": "life_prepare_task_completion",
                "description": "先查询候选，再准备完成操作",
                "requires_plan": True,
                "parameters": {
                    "type": "object",
                    "required": ["title"],
                    "properties": {
                        "title": {"type": "string"},
                        "date_hint": {"type": "string"},
                    },
                    "additionalProperties": False,
                },
            }
        ]

        result = runtime.start(payload)

        nodes = [event["node"] for event in result["node_trace"]]
        self.assertIn("plan", nodes)
        self.assertLess(nodes.index("plan"), nodes.index("prepare_tool"))
        self.assertEqual(result["execution_mode"], "agentic")
        self.assertEqual(
            result["execution_mode_reason"],
            "trusted_tool_requires_precondition_plan",
        )
        self.assertEqual(len(tools.prepared), 1)
        self.assertEqual(tools.committed, [])

    def test_marked_tool_composes_arguments_before_prepare(self) -> None:
        class ComposingDecisions(FakeDecisions):
            def __init__(self) -> None:
                super().__init__(
                    ModelDecision(
                        intent="work_create_markdown_document",
                        tool_name="work_create_markdown_document",
                    )
                )
                self.compositions = 0

            def compose_arguments(
                self,
                *,
                module: ModuleKey,
                message: str,
                tool_name: str,
                context: Mapping[str, Any],
            ) -> Mapping[str, Any]:
                del module, message, context
                self.compositions += 1
                self.assert_tool = tool_name
                return {"title": "方案", "content": "# 完整方案"}

        decisions = ComposingDecisions()
        tools = FakeTools(
            ToolPreparation(
                status="completed",
                tool_name="work_create_markdown_document",
                response="文档已创建。",
            )
        )
        graph = build_graph(
            checkpointer=InMemorySaver(),
            decisions=decisions,
            tools=tools,
        )
        input_payload = agent_input("run-compose-arguments", "work")
        input_payload["context"]["tools"] = [
            {
                "name": "work_create_markdown_document",
                "description": "创建 Markdown 文档",
                "parameters": {
                    "type": "object",
                    "required": ["title", "content"],
                    "properties": {
                        "title": {"type": "string"},
                        "content": {"type": "string", "maxLength": 10000},
                    },
                    "additionalProperties": False,
                },
                "compose_arguments": True,
            }
        ]
        result = AgentRuntime(graph).start(input_payload)
        self.assertEqual(decisions.compositions, 1)
        self.assertEqual(decisions.assert_tool, "work_create_markdown_document")
        self.assertEqual(
            tools.prepared[0]["arguments"],
            {"title": "方案", "content": "# 完整方案"},
        )
        self.assertIn(
            "compose_arguments",
            [event["node"] for event in result["node_trace"]],
        )
        self.assertEqual(
            result["budget_usage"]["model_calls_by_role"]["composer"],
            1,
        )

    def test_email_quality_gate_rewrites_once_before_tool_dispatch(self) -> None:
        class EmailDecisions(FakeDecisions):
            def __init__(self) -> None:
                super().__init__(
                    ModelDecision(
                        intent="work_draft_email",
                        tool_name="work_draft_email",
                        requires_argument_composition=True,
                    )
                )
                self.compositions = 0
                self.validation_contexts: list[dict[str, Any]] = []

            def compose_arguments(self, **values: Any) -> Mapping[str, Any]:
                self.compositions += 1
                self.validation_contexts.append(dict(values["context"].get("email_validation", {})))
                if self.compositions == 1:
                    invalid = complete_english_email_arguments()
                    invalid["salutation"] = "您好！"
                    invalid["introduction"] = ""
                    invalid["body_paragraphs"] = ["请问可以选 Project 课吗？"]
                    invalid["signature_lines"] = []
                    return invalid
                return complete_english_email_arguments()

        decisions = EmailDecisions()
        tools = FakeTools(
            ToolPreparation(
                status="completed",
                tool_name="work_draft_email",
                response="邮件草稿已生成。",
                data={
                    "kind": "skill_run",
                    "status": "succeeded",
                    "output": {
                        "send_status": "draft_only",
                        "quality_report": {"passed": True},
                    },
                },
            )
        )
        graph = build_graph(checkpointer=InMemorySaver(), decisions=decisions, tools=tools)
        payload = agent_input("run-email-quality-rewrite", "work")
        payload["user_message"] = "请写一封英文邮件咨询 Project 课"
        payload["context"].update(
            {
                "email_profile": {
                    "sender_name": "Alex Chen",
                    "default_language": "zh-CN",
                },
                "tools": [email_tool_definition()],
            }
        )
        result = AgentRuntime(graph).start(payload)
        self.assertEqual(result["outcome"], "completed")
        self.assertEqual(decisions.compositions, 2)
        self.assertEqual(len(tools.prepared), 1)
        self.assertEqual(tools.prepared[0]["arguments"]["output_language"], "en-US")
        self.assertEqual(result["email_rewrite_attempts"], 1)
        self.assertTrue(result["email_validation"]["passed"])
        self.assertIn(
            "english_language_contamination",
            decisions.validation_contexts[1]["violations"],
        )
        quality_events = [
            event for event in result["node_trace"] if event["node"] == "email_quality_gate"
        ]
        self.assertEqual(
            [event["status"] for event in quality_events],
            ["retrying", "succeeded"],
        )

    def test_email_quality_gate_stops_after_one_failed_rewrite(self) -> None:
        class InvalidEmailDecisions(FakeDecisions):
            def __init__(self) -> None:
                super().__init__(
                    ModelDecision(
                        intent="work_draft_email",
                        tool_name="work_draft_email",
                        requires_argument_composition=True,
                    )
                )
                self.compositions = 0

            def compose_arguments(self, **_values: Any) -> Mapping[str, Any]:
                self.compositions += 1
                invalid = complete_english_email_arguments()
                invalid["salutation"] = "你好"
                invalid["introduction"] = ""
                invalid["signature_lines"] = []
                return invalid

        decisions = InvalidEmailDecisions()
        tools = FakeTools(ToolPreparation(status="completed", tool_name="work_draft_email"))
        graph = build_graph(checkpointer=InMemorySaver(), decisions=decisions, tools=tools)
        payload = agent_input("run-email-quality-blocked", "work")
        payload["user_message"] = "写一封英文邮件"
        payload["context"].update(
            {
                "email_profile": {"sender_name": "Alex Chen"},
                "tools": [email_tool_definition()],
            }
        )
        result = AgentRuntime(graph).start(payload)
        self.assertEqual(result["outcome"], "email_quality_failed")
        self.assertEqual(decisions.compositions, 2)
        self.assertEqual(tools.prepared, [])
        self.assertIn("未通过", result["response"])

    def test_presentation_quality_gate_rewrites_brief_only_payload_before_dispatch(
        self,
    ) -> None:
        class PresentationDecisions(FakeDecisions):
            def __init__(self) -> None:
                super().__init__(
                    ModelDecision(
                        intent="work_generate_pptx",
                        tool_name="work_generate_pptx",
                        requires_argument_composition=True,
                    )
                )
                self.compositions = 0
                self.validation_contexts: list[dict[str, Any]] = []

            def compose_arguments(self, **values: Any) -> Mapping[str, Any]:
                self.compositions += 1
                self.validation_contexts.append(
                    dict(values["context"].get("artifact_validation", {}))
                )
                base = {
                    "title": "体育课课程安排总览",
                    "audience": "学生",
                    "style": "清晰、规范",
                    "brief": "整理课程代码、名称和上课时间",
                    "slide_count": 5,
                }
                if self.compositions == 1:
                    return base
                return {
                    **base,
                    "table": {
                        "columns": ["课程代码", "课程名称", "上课时间"],
                        "rows": [
                            {
                                "cells": ["PED1101", "Canoeing", "周三 10:00-11:50"],
                                "source_locator": "page:1",
                            }
                        ],
                    },
                }

        decisions = PresentationDecisions()
        tools = FakeTools(
            ToolPreparation(
                status="completed",
                tool_name="work_generate_pptx",
                response="工作任务已执行完成。",
                data={
                    "kind": "skill_run",
                    "id": "pptx-structured",
                    "skill_name": "office.pptx_generate",
                    "status": "succeeded",
                    "output": {
                        "outline": [
                            {"page": 1, "title": "体育课课程安排总览"},
                            {
                                "page": 2,
                                "title": "课程安排",
                                "table": {
                                    "columns": ["课程代码", "课程名称", "上课时间"],
                                    "row_count": 1,
                                    "source_locators": ["page:1"],
                                },
                            },
                            {"page": 3, "title": "内容概览"},
                        ],
                        "quality_report": {
                            "passed": True,
                            "violations": [],
                            "table_row_count": 1,
                        },
                        "source_coverage": {
                            "coverage_ratio": 1.0,
                            "truncated": False,
                        },
                    },
                    "files": [{"name": "courses.pptx"}],
                },
            )
        )
        graph = build_graph(checkpointer=InMemorySaver(), decisions=decisions, tools=tools)
        payload = agent_input("run-presentation-quality-rewrite", "work")
        payload["user_message"] = (
            "整理所有体育课的名称、上课时间和课程代码，并用中文PPT展示"
            "\n<!--ai-document:doc-1|courses.pdf-->"
        )
        payload["context"]["tools"] = [
            {
                "name": "work_generate_pptx",
                "description": "生成 PPTX",
                "compose_arguments": True,
                "parameters": {
                    "type": "object",
                    "required": ["title", "audience", "style", "brief", "slide_count"],
                    "properties": {
                        "title": {"type": "string"},
                        "audience": {"type": "string"},
                        "style": {"type": "string"},
                        "brief": {"type": "string"},
                        "slide_count": {"type": "integer"},
                        "table": {"type": "object"},
                        "task_contract": {"type": "object"},
                        "source_coverage": {"type": "object"},
                    },
                    "additionalProperties": False,
                },
            }
        ]

        result = AgentRuntime(graph).start(payload)

        self.assertEqual(result["outcome"], "completed")
        self.assertEqual(decisions.compositions, 2)
        self.assertEqual(len(tools.prepared), 1)
        self.assertIn("table", tools.prepared[0]["arguments"])
        self.assertEqual(result["presentation_rewrite_attempts"], 1)
        self.assertTrue(result["presentation_validation"]["passed"])
        self.assertIn(
            "structured_table_missing",
            {item["code"] for item in decisions.validation_contexts[1]["violations"]},
        )

    def test_filename_failure_is_repaired_without_an_extra_model_call(self) -> None:
        class PresentationDecisions(FakeDecisions):
            def __init__(self) -> None:
                super().__init__(
                    ModelDecision(
                        intent="work_generate_pptx",
                        tool_name="work_generate_pptx",
                    )
                )

            def compose_arguments(self, **_values: Any) -> Mapping[str, Any]:
                return {
                    "title": "Important Dates - Semester A 2026/27",
                    "audience": "New Students",
                    "style": "professional",
                    "brief": "Key dates",
                    "slide_count": 5,
                    "filename": "Important Dates - Semester A 2026/27.pptx",
                }

        class RepairingTools(FakeTools):
            def retry_task(self, **values: Any) -> ToolOutcome:
                self.retried.append(dict(values))
                return ToolOutcome(
                    response="工作任务已执行完成。",
                    data={
                        "kind": "skill_run",
                        "id": "pptx-task-1",
                        "skill_name": "office.pptx_generate",
                        "status": "succeeded",
                        "attempt": 2,
                        "output": {"quality_report": {"passed": True, "violations": []}},
                        "files": [{"name": "Important-Dates-Semester-A-2026-27.pptx"}],
                    },
                )

        decisions = PresentationDecisions()
        tools = RepairingTools(
            ToolPreparation(
                status="completed",
                tool_name="work_generate_pptx",
                response="工作任务执行失败。",
                data={
                    "kind": "skill_run",
                    "id": "pptx-task-1",
                    "skill_name": "office.pptx_generate",
                    "status": "failed",
                    "attempt": 1,
                    "error_code": "invalid_output_filename",
                    "failure": {
                        "contract_version": "tool-failure-v1",
                        "code": "invalid_output_filename",
                        "category": "argument_validation",
                        "phase": "pre_execution",
                        "message": "输出文件名必须是文件名，不能包含目录分隔符。",
                        "retry_same_input": False,
                        "repairable": True,
                        "side_effect_state": "none",
                        "field_paths": ["/filename"],
                        "allowed_repairs": [
                            "remove_optional_filename",
                            "filename.safe_basename",
                        ],
                    },
                },
            )
        )
        graph = build_graph(checkpointer=InMemorySaver(), decisions=decisions, tools=tools)
        payload = agent_input("run-self-repair", "work")
        payload["context"]["tools"] = [
            {
                "name": "work_generate_pptx",
                "description": "生成 PPTX",
                "compose_arguments": True,
                "parameters": {
                    "type": "object",
                    "required": ["title", "audience", "style", "brief", "slide_count"],
                    "properties": {
                        "title": {"type": "string"},
                        "audience": {"type": "string"},
                        "style": {"type": "string"},
                        "brief": {"type": "string"},
                        "slide_count": {"type": "integer"},
                        "filename": {
                            "type": "string",
                            "pattern": r"^[^/\\]+\.pptx$",
                        },
                    },
                    "additionalProperties": False,
                },
                "repair_policies": [
                    {
                        "operator_id": "remove_optional_filename",
                        "field_path": "/filename",
                        "extension": ".pptx",
                        "semantics_preserving": True,
                        "preflight": True,
                    },
                    {
                        "operator_id": "filename.safe_basename",
                        "field_path": "/filename",
                        "extension": ".pptx",
                        "semantics_preserving": True,
                        "preflight": True,
                    },
                ],
            }
        ]
        result = AgentRuntime(graph).start(payload)
        self.assertEqual(result["outcome"], "completed")
        self.assertEqual(result["repair_attempts"], 1)
        self.assertEqual(result["budget_usage"]["repair_model_calls"], 0)
        self.assertEqual(
            tools.prepared[0]["arguments"]["filename"],
            "Important-Dates-Semester-A-2026-27.pptx",
        )
        self.assertNotIn("filename", tools.retried[0]["arguments"])
        self.assertEqual(tools.retried[0]["operator_id"], "remove_optional_filename")
        self.assertEqual(result["repair_history"][-1]["validation"], "succeeded")
        self.assertEqual(result["repair_history"][-1]["result"], "succeeded")

    def test_unknown_allowlisted_failure_uses_one_bounded_repair_model_call(self) -> None:
        class RepairPlanningDecisions(FakeDecisions):
            def __init__(self) -> None:
                super().__init__(
                    ModelDecision(
                        intent="work_generate_pptx",
                        tool_name="work_generate_pptx",
                        tool_arguments={"filename": "old.pptx"},
                    )
                )
                self.repair_calls = 0

            def repair(self, **_values: Any) -> RepairDecision:
                self.repair_calls += 1
                return RepairDecision(
                    strategy="restricted_patch",
                    operator_id="filename.rewrite_extension",
                    patches=(
                        {
                            "op": "replace",
                            "path": "/filename",
                            "value": "safe.pptx",
                        },
                    ),
                    reason_code="normalize_extension",
                )

        class RepairingTools(FakeTools):
            def retry_task(self, **values: Any) -> ToolOutcome:
                self.retried.append(dict(values))
                return ToolOutcome(
                    response="工作任务已执行完成。",
                    data={
                        "kind": "skill_run",
                        "id": "pptx-task-model-repair",
                        "skill_name": "office.pptx_generate",
                        "status": "succeeded",
                        "attempt": 2,
                        "output": {"quality_report": {"passed": True, "violations": []}},
                        "files": [{"name": "safe.pptx"}],
                    },
                )

        decisions = RepairPlanningDecisions()
        tools = RepairingTools(
            ToolPreparation(
                status="completed",
                tool_name="work_generate_pptx",
                response="工作任务执行失败。",
                data={
                    "kind": "skill_run",
                    "id": "pptx-task-model-repair",
                    "skill_name": "office.pptx_generate",
                    "status": "failed",
                    "attempt": 1,
                    "error_code": "unsupported_filename_variant",
                    "failure": {
                        "contract_version": "tool-failure-v1",
                        "code": "unsupported_filename_variant",
                        "category": "argument_validation",
                        "phase": "pre_execution",
                        "message": "输出文件名需要规范化。",
                        "retry_same_input": False,
                        "repairable": True,
                        "side_effect_state": "none",
                        "field_paths": ["/filename"],
                        "allowed_repairs": ["filename.rewrite_extension"],
                    },
                },
            )
        )
        graph = build_graph(checkpointer=InMemorySaver(), decisions=decisions, tools=tools)
        payload = agent_input("run-model-self-repair", "work")
        payload["context"]["tools"] = [
            {
                "name": "work_generate_pptx",
                "description": "生成 PPTX",
                "parameters": {
                    "type": "object",
                    "required": ["filename"],
                    "properties": {
                        "filename": {
                            "type": "string",
                            "pattern": r"^[^/\\]+\.pptx$",
                        }
                    },
                    "additionalProperties": False,
                },
                "repair_policies": [
                    {
                        "operator_id": "filename.rewrite_extension",
                        "field_path": "/filename",
                        "extension": ".pptx",
                        "semantics_preserving": True,
                        "preflight": False,
                    }
                ],
            }
        ]
        result = AgentRuntime(graph).start(payload)
        self.assertEqual(result["outcome"], "completed")
        self.assertEqual(decisions.repair_calls, 1)
        self.assertEqual(result["budget_usage"]["repair_model_calls"], 1)
        self.assertEqual(tools.retried[0]["arguments"]["filename"], "safe.pptx")
        self.assertEqual(result["repair_history"][-1]["strategy"], "llm_patch")
        self.assertEqual(result["repair_history"][-1]["result"], "succeeded")

    def test_composer_schema_failure_settles_usage_and_skips_prepare(self) -> None:
        class InvalidComposer(FakeDecisions):
            def __init__(self) -> None:
                super().__init__(
                    ModelDecision(
                        intent="work_create_markdown_document",
                        tool_name="work_create_markdown_document",
                    )
                )
                self.events = [
                    {"kind": "model_call", "role": "router", "cost_micros": 1},
                    {
                        "kind": "model_call",
                        "role": "composer",
                        "cost_micros": 7,
                        "prompt_tokens": 20,
                        "completion_tokens": 4,
                    },
                ]

            def consume_observability(self) -> list[dict[str, Any]]:
                return [self.events.pop(0)]

            def compose_arguments(self, **_values: Any) -> Mapping[str, Any]:
                return {"title": "缺少正文"}

        decisions = InvalidComposer()
        tools = FakeTools(
            ToolPreparation(
                status="completed",
                tool_name="work_create_markdown_document",
                response="不应执行。",
            )
        )
        graph = build_graph(
            checkpointer=InMemorySaver(),
            decisions=decisions,
            tools=tools,
        )
        input_payload = agent_input("run-invalid-composition", "work")
        input_payload["context"]["tools"] = [
            {
                "name": "work_create_markdown_document",
                "description": "创建 Markdown 文档",
                "parameters": {
                    "type": "object",
                    "required": ["title", "content"],
                    "properties": {
                        "title": {"type": "string"},
                        "content": {"type": "string"},
                    },
                    "additionalProperties": False,
                },
                "compose_arguments": True,
            }
        ]
        result = AgentRuntime(graph).start(input_payload)
        self.assertEqual(result["outcome"], "model_invalid_response")
        self.assertEqual(result["budget_usage"]["cost_micros"], 8)
        self.assertEqual(
            result["budget_usage"]["model_calls_by_role"]["composer"],
            1,
        )
        self.assertEqual(tools.prepared, [])

    def test_prepare_retry_does_not_replay_completed_composer(self) -> None:
        class CountingComposer(FakeDecisions):
            def __init__(self) -> None:
                super().__init__(
                    ModelDecision(
                        intent="work_create_markdown_document",
                        tool_name="work_create_markdown_document",
                    )
                )
                self.compositions = 0

            def compose_arguments(self, **_values: Any) -> Mapping[str, Any]:
                self.compositions += 1
                return {"title": "恢复测试", "content": "已编排正文"}

        class FlakyPrepare(FakeTools):
            def __init__(self) -> None:
                super().__init__(
                    ToolPreparation(
                        status="completed",
                        tool_name="work_create_markdown_document",
                        response="文档已创建。",
                    )
                )
                self.prepare_attempts = 0

            def prepare(self, **values: Any) -> ToolPreparation:
                self.prepare_attempts += 1
                if self.prepare_attempts == 1:
                    raise RuntimeError("temporary prepare failure")
                return super().prepare(**values)

        decisions = CountingComposer()
        tools = FlakyPrepare()
        graph = build_graph(
            checkpointer=InMemorySaver(),
            decisions=decisions,
            tools=tools,
        )
        runtime = AgentRuntime(graph)
        input_payload = agent_input("run-compose-prepare-retry", "work")
        input_payload["context"]["tools"] = [
            {
                "name": "work_create_markdown_document",
                "description": "创建 Markdown 文档",
                "parameters": {
                    "type": "object",
                    "required": ["title", "content"],
                    "properties": {
                        "title": {"type": "string"},
                        "content": {"type": "string"},
                    },
                    "additionalProperties": False,
                },
                "compose_arguments": True,
            }
        ]
        with self.assertRaisesRegex(RuntimeError, "temporary prepare"):
            runtime.start(input_payload)
        final = runtime.start(input_payload)
        self.assertEqual(final["outcome"], "completed")
        self.assertEqual(decisions.compositions, 1)
        self.assertEqual(tools.prepare_attempts, 2)
        self.assertEqual(
            tools.prepared[0]["arguments"],
            {"title": "恢复测试", "content": "已编排正文"},
        )

    def test_selected_tool_must_exist_in_trusted_catalog(self) -> None:
        runtime, _, tools = self.runtime(
            ModelDecision(
                intent="unknown",
                tool_name="life.query_unknown",
            )
        )
        input_payload = agent_input("run-tool-not-in-catalog")
        input_payload["context"]["tools"] = [
            {
                "name": "life.query_today_plan",
                "description": "查询今日计划",
                "parameters": {"type": "object"},
            }
        ]
        with self.assertRaisesRegex(ValueError, "does not contain"):
            runtime.start(input_payload)
        self.assertEqual(tools.prepared, [])

    def test_router_arguments_are_schema_validated_before_prepare(self) -> None:
        class InvalidRouterArguments(FakeDecisions):
            def __init__(self) -> None:
                super().__init__(
                    ModelDecision(
                        intent="monthly_query",
                        tool_name="life_query_month",
                        tool_arguments={"month": "2026-13", "scope": "other"},
                    )
                )
                self.events = [
                    {
                        "kind": "model_call",
                        "role": "router",
                        "cost_micros": 3,
                        "prompt_tokens": 12,
                        "completion_tokens": 4,
                    },
                ]

            def consume_observability(self) -> list[dict[str, Any]]:
                return [self.events.pop(0)]

        decisions = InvalidRouterArguments()
        tools = FakeTools(
            ToolPreparation(
                status="completed",
                tool_name="life_query_month",
                response="不应执行。",
            )
        )
        runtime = AgentRuntime(
            build_graph(
                checkpointer=InMemorySaver(),
                decisions=decisions,
                tools=tools,
            )
        )
        input_payload = agent_input("run-invalid-router-arguments")
        input_payload["context"]["tools"] = [
            {
                "name": "life_query_month",
                "parameters": {
                    "type": "object",
                    "required": ["month", "scope"],
                    "properties": {
                        "month": {
                            "type": "string",
                            "pattern": r"^20\d{2}-(0[1-9]|1[0-2])$",
                        },
                        "scope": {
                            "oneOf": [
                                {"const": "personal"},
                                {"const": "shared"},
                            ]
                        },
                    },
                    "additionalProperties": False,
                },
            }
        ]
        result = runtime.start(input_payload)
        self.assertEqual(result["outcome"], "model_invalid_response")
        self.assertEqual(result["budget_usage"]["cost_micros"], 3)
        self.assertEqual(tools.prepared, [])

    def test_write_tool_interrupts_then_resumes_with_same_run(self) -> None:
        runtime, _, tools = self.runtime(
            ModelDecision(
                intent="ledger_write",
                tool_name="life.prepare_ledger_entry",
                tool_arguments={"text": "今天午饭花了50元"},
            ),
            ToolPreparation(
                status="requires_confirmation",
                tool_name="life.prepare_ledger_entry",
                risk_level="medium",
                summary="支出 50.00 元，分类：餐饮",
                confirmation_token="candidate-1",
                normalized_arguments={"amount_minor": 5000, "currency": "CNY"},
            ),
        )
        first = runtime.start(agent_input("run-ledger"))
        payloads = interrupt_payloads(first)
        self.assertEqual(len(payloads), 1)
        self.assertEqual(payloads[0]["confirmation_token"], "candidate-1")
        self.assertTrue(payloads[0]["interrupt_id"])
        self.assertEqual(tools.committed, [])

        replay = runtime.start(agent_input("run-ledger"))
        replay_payloads = interrupt_payloads(replay)
        self.assertEqual(
            replay_payloads[0]["interrupt_id"],
            payloads[0]["interrupt_id"],
        )

        changed_catalog_input = agent_input("run-ledger")
        changed_catalog_input["context"]["tools"] = [
            {
                "name": "life.prepare_ledger_entry",
                "description": "部署后更新的工具描述",
                "parameters": {"type": "object", "properties": {}},
            }
        ]
        changed_catalog_replay = runtime.start(changed_catalog_input)
        self.assertEqual(
            interrupt_payloads(changed_catalog_replay)[0]["interrupt_id"],
            payloads[0]["interrupt_id"],
        )
        self.assertEqual(
            changed_catalog_replay["tool_catalog_fingerprint"],
            first["tool_catalog_fingerprint"],
        )

        changed_identity_input = agent_input("run-ledger")
        changed_identity_input["user_id"] = "00000000-0000-0000-0000-000000000099"
        with self.assertRaisesRegex(ValueError, "user_id does not match checkpoint"):
            runtime.start(changed_identity_input)

        with self.assertRaisesRegex(ValueError, "stale"):
            runtime.resume(
                "run-ledger",
                {"approved": True, "interrupt_id": "stale-interrupt"},
            )

        final = runtime.resume(
            "run-ledger",
            {
                "approved": True,
                "interrupt_id": payloads[0]["interrupt_id"],
            },
        )
        self.assertEqual(final["outcome"], "completed")
        self.assertEqual(final["response"], "操作已确认并完成。")
        self.assertEqual(len(tools.committed), 1)
        self.assertEqual(tools.committed[0]["run_id"], "run-ledger")
        self.assertEqual(
            tools.committed[0]["idempotency_key"],
            "run-ledger:action:1:commit",
        )
        self.assertEqual(final["graph_version"], GRAPH_VERSION)
        self.assertEqual(final["budget_usage"]["actions"], 1)
        self.assertEqual(final["budget_usage"]["model_calls"], 1)
        self.assertTrue(final["node_contracts"]["approval"]["recovery"])
        self.assertEqual(len(final["model_manifest"]["fingerprint"]), 64)

    def test_consumed_approval_resolution_replays_failed_commit_node(self) -> None:
        class FlakyCommitTools(FakeTools):
            def __init__(self) -> None:
                super().__init__(
                    ToolPreparation(
                        status="requires_confirmation",
                        tool_name="life.prepare_ledger_entry",
                        risk_level="medium",
                        summary="支出 50.00 元",
                        confirmation_token="candidate-retry",
                    )
                )
                self.commit_attempts = 0

            def commit(self, **values: Any) -> ToolOutcome:
                self.commit_attempts += 1
                if self.commit_attempts == 1:
                    raise RuntimeError("temporary commit failure")
                return super().commit(**values)

        decisions = FakeDecisions(
            ModelDecision(
                intent="ledger_write",
                tool_name="life.prepare_ledger_entry",
            )
        )
        tools = FlakyCommitTools()
        graph = build_graph(
            checkpointer=InMemorySaver(),
            decisions=decisions,
            tools=tools,
        )
        runtime = AgentRuntime(graph)
        first = runtime.start(agent_input("run-consumed-resolution"))
        payload = interrupt_payloads(first)[0]
        resolution = {
            "approved": True,
            "interrupt_id": payload["interrupt_id"],
        }
        with self.assertRaisesRegex(RuntimeError, "temporary commit"):
            runtime.resume("run-consumed-resolution", resolution)

        # The durable run still carries the same resolution. LangGraph already
        # checkpointed it, so recovery must continue at commit_tool without
        # trying to resume the interrupt a second time.
        final = runtime.resume("run-consumed-resolution", resolution)
        self.assertEqual(final["outcome"], "completed")
        self.assertEqual(tools.commit_attempts, 2)
        self.assertEqual(len(tools.committed), 1)
        self.assertEqual(final["recovery"]["approval_resumes"], 1)

    def test_failed_observe_inside_interrupt_reuses_same_resolution(self) -> None:
        class FlakyObserveTools(FakeTools):
            def __init__(self) -> None:
                super().__init__(
                    ToolPreparation(
                        status="completed",
                        tool_name="life_query_today_plan",
                        response="查询任务已入队。",
                        data={
                            "kind": "skill_run",
                            "id": "task-flaky-observe",
                            "status": "queued",
                        },
                    )
                )
                self.observe_attempts = 0

            def observe(self, **_values: Any) -> ToolOutcome:
                self.observe_attempts += 1
                if self.observe_attempts == 1:
                    raise RuntimeError("temporary observe failure")
                return ToolOutcome(
                    response="查询完成。",
                    data={
                        "kind": "skill_run",
                        "id": "task-flaky-observe",
                        "status": "succeeded",
                    },
                )

        decisions = FakeDecisions(
            ModelDecision(
                intent="plan_query",
                tool_name="life_query_today_plan",
            )
        )
        tools = FlakyObserveTools()
        graph = build_graph(
            checkpointer=InMemorySaver(),
            decisions=decisions,
            tools=tools,
        )
        runtime = AgentRuntime(graph)
        first = runtime.start(agent_input("run-flaky-observe"))
        payload = interrupt_payloads(first)[0]
        resolution = {
            "type": "tool_poll",
            "task_id": payload["task_id"],
            "interrupt_id": payload["interrupt_id"],
        }
        with self.assertRaisesRegex(RuntimeError, "temporary observe"):
            runtime.resume("run-flaky-observe", resolution)

        final = runtime.resume("run-flaky-observe", resolution)
        self.assertEqual(final["outcome"], "completed")
        self.assertEqual(final["response"], "查询完成。")
        self.assertEqual(tools.observe_attempts, 2)
        self.assertEqual(final["recovery"]["tool_resumes"], 1)

    def test_failed_model_attempts_are_settled_as_terminal_graph_state(self) -> None:
        class FailingDecisions(FakeDecisions):
            def __init__(self) -> None:
                super().__init__(ModelDecision(intent="unused", response="unused"))
                self.events: list[dict[str, Any]] = []
                self.decision_attempts = 0

            def consume_observability(self) -> list[dict[str, Any]]:
                events, self.events = self.events, []
                return events

            def decide(self, **_values: Any) -> ModelDecision:
                self.decision_attempts += 1
                self.events.append(
                    {
                        "kind": "model_call",
                        "role": "router",
                        "status": "error",
                        "prompt_tokens": 120,
                        "completion_tokens": 0,
                        "cost_micros": 7,
                        "error_status": 503,
                        "retryable": True,
                    }
                )
                raise RuntimeError("all configured models failed")

        decisions = FailingDecisions()
        tools = FakeTools(
            ToolPreparation(
                status="completed",
                tool_name="life_query_today_plan",
                response="unused",
            )
        )
        graph = build_graph(
            checkpointer=InMemorySaver(),
            decisions=decisions,
            tools=tools,
        )
        runtime = AgentRuntime(graph)
        result = runtime.start(agent_input("run-model-failure"))
        self.assertEqual(result["outcome"], "model_unavailable")
        self.assertEqual(result["budget_usage"]["model_calls"], 1)
        self.assertEqual(result["budget_usage"]["prompt_tokens"], 120)
        self.assertEqual(result["budget_usage"]["cost_micros"], 7)
        self.assertEqual(result["model_events"][-1]["error_status"], 503)
        replay = runtime.start(agent_input("run-model-failure"))
        self.assertEqual(replay["outcome"], "model_unavailable")
        self.assertEqual(decisions.decision_attempts, 1)

    def test_semantically_invalid_model_result_is_settled_before_checkpoint(self) -> None:
        class InvalidPlanDecisions(FakeDecisions):
            def __init__(self) -> None:
                super().__init__(
                    ModelDecision(
                        intent="plan_query",
                        tool_name="life_query_today_plan",
                    )
                )
                self.events: list[dict[str, Any]] = []
                self.plan_attempts = 0

            def consume_observability(self) -> list[dict[str, Any]]:
                events, self.events = self.events, []
                return events

            def plan(self, **_values: Any) -> AgentPlan:
                self.plan_attempts += 1
                self.events.append(
                    {
                        "kind": "model_call",
                        "role": "planner",
                        "status": "succeeded",
                        "prompt_tokens": 80,
                        "completion_tokens": 12,
                        "cost_micros": 5,
                        "error_status": 0,
                        "retryable": False,
                    }
                )
                return AgentPlan(
                    objective="   ",
                    steps=("看似有效的步骤",),
                    success_criteria="   ",
                )

        decisions = InvalidPlanDecisions()
        graph = build_graph(
            checkpointer=InMemorySaver(),
            decisions=decisions,
            tools=FakeTools(
                ToolPreparation(
                    status="completed",
                    tool_name="life_query_today_plan",
                    response="unused",
                )
            ),
        )
        runtime = AgentRuntime(graph)
        payload = agent_input("run-invalid-plan")
        payload["context"]["tools"] = [
            {
                "name": "life_query_today_plan",
                "repeatable": True,
                "parameters": {"type": "object", "properties": {}},
            }
        ]
        result = runtime.start(payload)
        self.assertEqual(result["outcome"], "model_invalid_response")
        self.assertEqual(result["budget_usage"]["model_calls"], 2)
        self.assertEqual(result["budget_usage"]["prompt_tokens"], 80)
        self.assertEqual(result["budget_usage"]["completion_tokens"], 12)
        self.assertEqual(result["budget_usage"]["cost_micros"], 5)
        self.assertEqual(result["model_events"][-1]["status"], "succeeded")
        self.assertEqual(decisions.plan_attempts, 1)

    def test_same_version_with_changed_model_manifest_stops_old_checkpoint(self) -> None:
        class ManifestDecisions(FakeDecisions):
            def __init__(self, model: str) -> None:
                super().__init__(
                    ModelDecision(
                        intent="plan_query",
                        tool_name="life_query_today_plan",
                    )
                )
                self.model = model
                self.assessment_calls = 0

            def model_manifest(self) -> dict[str, Any]:
                return {
                    "provider": "openrouter",
                    "config_version": "same-human-version",
                    "pinned": True,
                    "roles": {"router": {"models": [self.model]}},
                }

            def assess(self, **values: Any) -> AgentAssessment:
                self.assessment_calls += 1
                return super().assess(**values)

        checkpointer = InMemorySaver()
        tools = FakeTools(
            ToolPreparation(
                status="completed",
                tool_name="life_query_today_plan",
                response="查询任务已入队。",
                data={"kind": "skill_run", "id": "task-version", "status": "queued"},
            ),
            observations=[
                ToolOutcome(
                    response="查询完成。",
                    data={
                        "kind": "skill_run",
                        "id": "task-version",
                        "status": "succeeded",
                    },
                )
            ],
        )
        old_decisions = ManifestDecisions("openai/gpt-5-nano")
        old_runtime = AgentRuntime(
            build_graph(
                checkpointer=checkpointer,
                decisions=old_decisions,
                tools=tools,
            )
        )
        first = old_runtime.start(agent_input("run-model-manifest"))
        payload = interrupt_payloads(first)[0]

        new_decisions = ManifestDecisions("openai/gpt-5-mini")
        new_runtime = AgentRuntime(
            build_graph(
                checkpointer=checkpointer,
                decisions=new_decisions,
                tools=tools,
            )
        )
        result = new_runtime.resume(
            "run-model-manifest",
            {
                "type": "tool_poll",
                "task_id": payload["task_id"],
                "interrupt_id": payload["interrupt_id"],
            },
        )
        self.assertEqual(result["outcome"], "completed")
        self.assertEqual(result["response"], "查询完成。")
        self.assertEqual(new_decisions.assessment_calls, 0)

    def test_direct_path_completes_with_one_model_call_and_keeps_action_cap(self) -> None:
        decisions = FakeDecisions(
            ModelDecision(intent="casual_chat", response="不应调用第二个模型节点")
        )
        tools = FakeTools(
            ToolPreparation(
                status="completed",
                tool_name="life_query_today_plan",
                response="unused",
            )
        )
        policy = BudgetPolicy(
            max_actions=2,
            max_model_calls=1,
            max_prompt_tokens=1000,
            max_completion_tokens=1000,
            max_cost_micros=1000,
            max_tool_resumes=2,
            tool_poll_interval_ms=500,
        )
        runtime = AgentRuntime(
            build_graph(
                checkpointer=InMemorySaver(),
                decisions=decisions,
                tools=tools,
                budget_policy=policy,
            )
        )
        result = runtime.start(agent_input("run-model-budget"))
        self.assertEqual(result["outcome"], "completed")
        self.assertEqual(result["action_budget"], 2)
        self.assertEqual(result["budget_usage"]["model_calls"], 1)
        self.assertEqual(decisions.modules, ["life"])
        self.assertEqual(tools.prepared, [])

    def test_failed_node_continues_checkpoint_without_restarting_supervisor(self) -> None:
        class CountingDecisions(FakeDecisions):
            def __init__(self) -> None:
                super().__init__(
                    ModelDecision(
                        intent="plan_query",
                        tool_name="life_query_today_plan",
                    )
                )
                self.plan_calls = 0

            def plan(self, **values: Any) -> AgentPlan:
                self.plan_calls += 1
                return super().plan(**values)

        class FlakyTools(FakeTools):
            def __init__(self) -> None:
                super().__init__(
                    ToolPreparation(
                        status="completed",
                        tool_name="life_query_today_plan",
                        response="今天没有计划。",
                    )
                )
                self.attempts = 0

            def prepare(self, **values: Any) -> ToolPreparation:
                self.attempts += 1
                if self.attempts == 1:
                    raise RuntimeError("temporary gateway failure")
                return super().prepare(**values)

        decisions = CountingDecisions()
        tools = FlakyTools()
        graph = build_graph(
            checkpointer=InMemorySaver(),
            decisions=decisions,
            tools=tools,
        )
        runtime = AgentRuntime(graph)
        value = agent_input("run-recover-node")
        with self.assertRaisesRegex(RuntimeError, "temporary"):
            runtime.start(value)

        result = AgentRuntime(graph).start(value)
        self.assertEqual(result["response"], "今天没有计划。")
        self.assertEqual(decisions.plan_calls, 0)
        self.assertEqual(decisions.modules, ["life"])
        self.assertEqual(tools.attempts, 2)

    def test_rejected_write_never_calls_commit(self) -> None:
        runtime, _, tools = self.runtime(
            ModelDecision(
                intent="reminder_write",
                tool_name="life.prepare_reminder",
            ),
            ToolPreparation(
                status="requires_confirmation",
                tool_name="life.prepare_reminder",
                risk_level="medium",
                summary="明天 09:00 提醒开会",
                confirmation_token="reminder-1",
            ),
        )
        runtime.start(agent_input("run-reminder"))
        result = runtime.resume("run-reminder", {"approved": False})
        self.assertEqual(result["outcome"], "cancelled")
        self.assertEqual(tools.committed, [])

    def test_tool_allowlist_supports_current_and_target_names(self) -> None:
        self.assertTrue(tool_allowed("life", "life_prepare_reminder"))
        self.assertTrue(tool_allowed("life", "life.prepare_reminder"))
        self.assertTrue(tool_allowed("work", "office.pdf_translate"))
        self.assertFalse(tool_allowed("companion", "life.query_today_plan"))

    def test_async_attachment_extraction_is_observed_before_next_action(self) -> None:
        class SequencedDecisions(FakeDecisions):
            def __init__(self) -> None:
                super().__init__(
                    ModelDecision(
                        intent="extract_attachment",
                        tool_name="work_extract_attached_document",
                        tool_arguments={"attachment_index": 1},
                    )
                )
                self.items = [
                    self.decision,
                    ModelDecision(
                        intent="extract_attachment",
                        tool_name="work_extract_attached_document",
                        tool_arguments={"attachment_index": 2},
                    ),
                    ModelDecision(
                        intent="create_presentation_outline",
                        tool_name="work_create_pptx_outline",
                        tool_arguments={
                            "title": "课程内容",
                            "audience": "同学",
                            "style": "简洁",
                            "brief": "提取后的重点",
                            "slide_count": 6,
                        },
                    ),
                    ModelDecision(
                        intent="create_presentation",
                        tool_name="work_generate_pptx",
                        tool_arguments={
                            "title": "课程内容",
                            "audience": "同学",
                            "style": "简洁",
                            "brief": "提取后的重点",
                            "slide_count": 6,
                        },
                    ),
                ]
                self.contexts: list[dict[str, Any]] = []

            def decide(
                self,
                *,
                module: ModuleKey,
                message: str,
                context: Mapping[str, Any],
            ) -> ModelDecision:
                del message
                self.modules.append(module)
                self.contexts.append(dict(context))
                return self.items.pop(0)

        decisions = SequencedDecisions()

        class SequencedTools(FakeTools):
            def prepare(self, **values: Any) -> ToolPreparation:
                self.prepared.append(dict(values))
                if values["tool_name"] == "work_extract_attached_document":
                    attachment_index = values["arguments"]["attachment_index"]
                    return ToolPreparation(
                        status="completed",
                        tool_name="work_extract_attached_document",
                        response="附件提取任务已入队。",
                        data={
                            "kind": "skill_run",
                            "id": f"extract-{attachment_index}",
                            "skill_name": "office.document_extract",
                            "status": "queued",
                        },
                    )
                if values["tool_name"] == "work_create_pptx_outline":
                    return ToolPreparation(
                        status="completed",
                        tool_name="work_create_pptx_outline",
                        response="PPT 大纲任务已入队。",
                        data={
                            "kind": "skill_run",
                            "id": "outline-1",
                            "skill_name": "office.pptx_outline",
                            "status": "queued",
                        },
                    )
                return ToolPreparation(
                    status="requires_confirmation",
                    tool_name="work_generate_pptx",
                    risk_level="medium",
                    summary="生成课程内容.pptx",
                    confirmation_token="ppt-confirm",
                )

        tools = SequencedTools(
            ToolPreparation(
                status="completed",
                tool_name="unused",
                response="unused",
            ),
            observations=[
                ToolOutcome(
                    response="附件正文提取完成。",
                    data={
                        "kind": "skill_run",
                        "id": "extract-1",
                        "skill_name": "office.document_extract",
                        "status": "succeeded",
                        "output": {"text": "课程的核心内容"},
                    },
                ),
                ToolOutcome(
                    response="第二个附件正文提取完成。",
                    data={
                        "kind": "skill_run",
                        "id": "extract-2",
                        "skill_name": "office.document_extract",
                        "status": "succeeded",
                        "output": {"text": "课程的补充内容"},
                    },
                ),
                ToolOutcome(
                    response="PPT 大纲生成完成。",
                    data={
                        "kind": "skill_run",
                        "id": "outline-1",
                        "skill_name": "office.pptx_outline",
                        "status": "succeeded",
                        "output": {"outline": [{"title": "课程内容"}]},
                    },
                ),
            ],
        )
        graph = build_graph(
            checkpointer=InMemorySaver(),
            decisions=decisions,
            tools=tools,
        )
        first = AgentRuntime(graph).start(agent_input("run-ppt", "work"))
        payloads = interrupt_payloads(first)
        self.assertEqual(len(payloads), 1)
        self.assertEqual(payloads[0]["type"], "tool_wait")
        self.assertEqual(payloads[0]["task_id"], "extract-1")
        self.assertEqual(payloads[0]["poll_after_ms"], 500)
        self.assertEqual(payloads[0]["poll_max_ms"], 5_000)
        self.assertEqual(len(tools.observations), 3)
        runtime = AgentRuntime(graph)
        current = first
        while True:
            current_payloads = interrupt_payloads(current)
            self.assertEqual(len(current_payloads), 1, current)
            payload = current_payloads[0]
            if payload["type"] == "tool_approval":
                break
            current = runtime.resume(
                "run-ppt",
                {
                    "type": "tool_poll",
                    "task_id": payload["task_id"],
                    "interrupt_id": payload["interrupt_id"],
                },
            )
        first = current
        payloads = interrupt_payloads(first)
        self.assertEqual(payloads[0]["tool_name"], "work_generate_pptx")
        self.assertEqual(
            [item["tool_name"] for item in tools.prepared],
            [
                "work_extract_attached_document",
                "work_extract_attached_document",
                "work_create_pptx_outline",
                "work_generate_pptx",
            ],
        )
        self.assertEqual(
            decisions.contexts[1]["observations"][0]["data"]["output"]["text"],
            "课程的核心内容",
        )
        self.assertEqual(
            decisions.contexts[2]["observations"][1]["data"]["output"]["text"],
            "课程的补充内容",
        )
        self.assertEqual(
            decisions.contexts[3]["observations"][2]["data"]["output"]["outline"][0]["title"],
            "课程内容",
        )
        self.assertEqual(
            [item["idempotency_key"] for item in tools.prepared[:2]],
            ["run-ppt:action:1:prepare", "run-ppt:action:2:prepare"],
        )
        self.assertEqual(first["plan"]["steps"], ["执行工具", "观察结果", "交付结果"])


if __name__ == "__main__":
    unittest.main()
