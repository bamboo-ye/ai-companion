from __future__ import annotations

import json
import os
import threading
import time
import unittest
from concurrent.futures import ThreadPoolExecutor
from typing import Any, Mapping
from unittest.mock import patch

from ai_companion_worker.openrouter_decision import (
    OpenRouterConfig,
    OpenRouterDecisionPort,
    OpenRouterError,
    _model_visible_presentation_parameters,
    _validate_narrative_batch_arguments,
    _required_artifact_tool,
    _presentation_batch_parameters,
)
from ai_companion_worker.agent_runtime import ModelBudgetExceeded


class StubOpenRouter(OpenRouterDecisionPort):
    def __init__(
        self,
        responses: list[dict[str, Any] | OpenRouterError],
        *,
        elapsed_seconds: list[float] | None = None,
    ) -> None:
        self.now = 0.0
        super().__init__(
            OpenRouterConfig(
                base_url="https://openrouter.ai/api/v1",
                api_key="test-key",
                models=("openrouter/free", "free/fallback"),
            ),
            monotonic=lambda: self.now,
        )
        self.responses = responses
        self.elapsed_seconds = list(elapsed_seconds or ())
        self.requests: list[dict[str, Any]] = []
        self.request_timeouts: list[float] = []

    def _request(
        self,
        payload: Mapping[str, Any],
        *,
        timeout_seconds: float,
    ) -> dict[str, Any]:
        self.requests.append(dict(payload))
        self.request_timeouts.append(timeout_seconds)
        if self.elapsed_seconds:
            self.now += self.elapsed_seconds.pop(0)
        response = self.responses.pop(0)
        if isinstance(response, OpenRouterError):
            raise response
        return response


def tool_response(name: str, arguments: str = "{}") -> dict[str, Any]:
    return {
        "choices": [
            {
                "message": {
                    "tool_calls": [
                        {
                            "id": "call-1",
                            "type": "function",
                            "function": {
                                "name": name,
                                "arguments": arguments,
                            },
                        }
                    ]
                }
            }
        ]
    }


def context() -> dict[str, Any]:
    return {
        "tools": [
            {
                "name": "life_query_today_plan",
                "description": "查询真实今日计划",
                "parameters": {"type": "object", "properties": {}},
            },
            {
                "name": "life_no_tool",
                "description": "仅普通聊天时选择",
                "parameters": {"type": "object", "properties": {}},
            },
        ],
        "system_prompt": "你是小满；没有真实数据时不得杜撰。",
    }


class OpenRouterDecisionPortTest(unittest.TestCase):
    def test_observability_buffers_are_isolated_between_parallel_branches(self) -> None:
        port = StubOpenRouter([])
        barrier = threading.Barrier(2)

        def record(branch: str) -> list[dict[str, Any]]:
            port._observability_buffer().append({"kind": "model_call", "branch": branch})
            barrier.wait(timeout=1)
            return port.consume_observability()

        with ThreadPoolExecutor(max_workers=2) as executor:
            futures = [executor.submit(record, branch) for branch in ("a", "b")]
            observed = [future.result(timeout=2) for future in futures]

        self.assertEqual(
            {events[0]["branch"] for events in observed},
            {"a", "b"},
        )
        self.assertTrue(all(len(events) == 1 for events in observed))

    def test_artifact_router_uses_public_presentation_orchestrator_for_all_capabilities(
        self,
    ) -> None:
        tools = [
            {"name": "work_generate_pptx"},
            {"name": "work_generate_table_pptx"},
            {"name": "work_generate_visual_pptx"},
        ]
        for mode in ("narrative", "structured_table", "illustrated", "composed"):
            with self.subTest(mode=mode):
                self.assertEqual(
                    _required_artifact_tool(
                        {
                            "task_contract": {
                                "artifact_types": ["pptx"],
                                "presentation_mode": mode,
                            },
                            "artifact_validation": {},
                        },
                        tools,
                    ),
                    "work_generate_pptx",
                )

    def test_router_hides_private_presentation_children(self) -> None:
        port = StubOpenRouter([tool_response("work_generate_pptx")])
        definitions = [
            {
                "name": name,
                "description": name,
                "compose_arguments": True,
                "parameters": {"type": "object", "properties": {}},
            }
            for name in (
                "work_generate_pptx",
                "work_generate_table_pptx",
                "work_generate_visual_pptx",
            )
        ]
        decision = port.decide(
            module="work",
            message="生成带表格和配图的 PPT",
            context={
                "tools": definitions,
                "task_contract": {
                    "artifact_types": ["pptx"],
                    "presentation_capabilities": ["narrative", "table", "visual"],
                },
                "artifact_validation": {},
            },
        )
        self.assertEqual(decision.tool_name, "work_generate_pptx")
        exposed = [item["function"]["name"] for item in port.requests[0]["tools"]]
        self.assertEqual(exposed, ["work_generate_pptx"])

    def test_model_visible_presentation_schema_is_capability_scoped(self) -> None:
        schema = {
            "type": "object",
            "required": ["brief", "table", "task_contract"],
            "properties": {
                "brief": {"type": "string"},
                "table": {"type": "object"},
                "mapping_contract": {"type": "object"},
                "task_contract": {"type": "object"},
                "source_coverage": {"type": "object"},
            },
            "additionalProperties": False,
        }
        narrative = _model_visible_presentation_parameters(schema)
        self.assertEqual(set(narrative["properties"]), {"brief"})
        self.assertEqual(narrative["required"], ["brief"])
        structured = _model_visible_presentation_parameters(schema, structured=True)
        self.assertEqual(
            set(structured["properties"]),
            {"brief", "table", "mapping_contract"},
        )

    def test_public_ppt_composer_projects_private_table_child_schema(self) -> None:
        arguments = {
            "title": "课程一览",
            "audience": "学生",
            "style": "简洁",
            "brief": "课程安排",
            "slide_count": 4,
            "table": {
                "columns": ["课程代码", "课程名称"],
                "rows": [
                    {
                        "cells": ["PED1101", "独木舟"],
                        "source_locator": "Page 1",
                    }
                ],
            },
        }
        common = {
            "title": {"type": "string"},
            "audience": {"type": "string"},
            "style": {"type": "string"},
            "brief": {"type": "string"},
            "slide_count": {"type": "integer"},
            "task_contract": {"type": "object"},
            "source_coverage": {"type": "object"},
        }
        table = {
            "type": "object",
            "properties": {"rows": {"type": "array"}},
        }
        port = StubOpenRouter(
            [tool_response("work_generate_pptx", json.dumps(arguments, ensure_ascii=False))]
        )
        port.compose_arguments(
            module="work",
            message="整理课程并生成带表格的 PPT",
            tool_name="work_generate_pptx",
            context={
                "task_contract": {
                    "presentation_capabilities": ["narrative", "table"],
                    "requested_fields": ["code", "name"],
                },
                "tools": [
                    {
                        "name": "work_generate_pptx",
                        "description": "PPT 主工具",
                        "compose_arguments": True,
                        "parameters": {
                            "type": "object",
                            "properties": common,
                            "additionalProperties": False,
                        },
                    },
                    {
                        "name": "work_generate_table_pptx",
                        "description": "表格子工具",
                        "compose_arguments": True,
                        "parameters": {
                            "type": "object",
                            "properties": {**common, "table": table},
                            "additionalProperties": False,
                        },
                    },
                ],
            },
        )
        function = port.requests[0]["tools"][0]["function"]
        self.assertEqual(function["name"], "work_generate_pptx")
        self.assertIn("table", function["parameters"]["properties"])

    def test_compact_entity_batch_schema_leaves_provenance_to_harness(self) -> None:
        schema = {
            "type": "object",
            "properties": {
                "title": {"type": "string"},
                "audience": {"type": "string"},
                "style": {"type": "string"},
                "table": {
                    "type": "object",
                    "properties": {
                        "rows": {
                            "type": "array",
                            "items": {
                                "type": "object",
                                "required": ["cells", "source_locator"],
                                "properties": {
                                    "cells": {"type": "array"},
                                    "entity_id": {"type": "string"},
                                    "source_locator": {"type": "string"},
                                    "source_refs": {"type": "array"},
                                },
                                "additionalProperties": False,
                            },
                        }
                    },
                },
            },
        }
        compact = _presentation_batch_parameters(schema, harness_injects_provenance=True)
        items = compact["properties"]["table"]["properties"]["rows"]["items"]
        self.assertEqual(items["required"], ["cells", "entity_id"])
        self.assertNotIn("source_locator", items["properties"])
        self.assertNotIn("source_refs", items["properties"])

    def test_default_profile_prefers_deepseek_and_keeps_gpt_fallbacks(self) -> None:
        with patch.dict(os.environ, {"MODEL_API_KEY": "test-key"}, clear=True):
            config = OpenRouterConfig.from_env()
        self.assertEqual(
            config.models,
            (
                "deepseek/deepseek-v4-flash-0731",
                "openai/gpt-5-mini",
            ),
        )
        self.assertEqual(
            config.models_for("router"),
            (
                "deepseek/deepseek-v4-flash-0731",
                "openai/gpt-5-nano",
            ),
        )
        self.assertEqual(
            config.models_for("composer"),
            (
                "deepseek/deepseek-v4-flash-0731",
                "openai/gpt-5-mini",
            ),
        )
        self.assertEqual(
            config.models_for("repairer"),
            ("deepseek/deepseek-v4-flash-0731",),
        )
        self.assertEqual(
            config.models_for("companion_responder"),
            (
                "openai/gpt-oss-20b:free",
                "google/gemma-4-31b-it:free",
                "inclusionai/ling-3.0-flash:free",
            ),
        )
        self.assertEqual(
            config.config_version,
            "2026-08-structured-composer-v4",
        )
        self.assertEqual(config.preferred_max_latency_p90, 8)
        self.assertEqual(config.timeout_seconds, 30)
        self.assertEqual(config.attempt_timeout_seconds, 15)
        self.assertEqual(config.composer_timeout_seconds, 90)
        self.assertEqual(config.composer_attempt_timeout_seconds, 30)
        self.assertEqual(config.min_fallback_timeout_seconds, 5)
        self.assertEqual(config.composer_batch_max_tokens, 6144)
        self.assertEqual(config.reasoning_effort, "minimal")
        self.assertEqual(config.planner_reasoning_effort, "high")
        self.assertEqual(config.router_reasoning_effort, "low")
        self.assertEqual(config.composer_reasoning_effort, "low")
        self.assertEqual(config.assessor_reasoning_effort, "minimal")
        self.assertEqual(config.fallback_reasoning_effort, "low")

    def test_model_generates_structured_execution_plan(self) -> None:
        port = StubOpenRouter(
            [
                {
                    "choices": [
                        {
                            "message": {
                                "content": (
                                    '{"objective":"根据附件制作演示文稿",'
                                    '"steps":["提取附件正文","观察提取结果",'
                                    '"生成PPTX","观察并交付文件"],'
                                    '"success_criteria":"返回可下载的PPTX文件"}'
                                )
                            }
                        }
                    ]
                }
            ]
        )
        plan = port.plan(
            module="work",
            message="根据附件做一个PPT",
            context=context(),
        )
        self.assertEqual(plan.objective, "根据附件制作演示文稿")
        self.assertEqual(plan.steps[0], "提取附件正文")
        self.assertNotIn("tools", port.requests[0])
        self.assertNotIn("tool_choice", port.requests[0])
        self.assertNotIn("temperature", port.requests[0])
        self.assertEqual(
            port.requests[0]["response_format"],
            {"type": "json_object"},
        )
        self.assertEqual(port.requests[0]["reasoning"]["effort"], "high")

    def test_life_completion_plan_requires_lookup_disambiguation_and_confirmation(self) -> None:
        port = StubOpenRouter(
            [
                {
                    "choices": [
                        {
                            "message": {
                                "content": (
                                    '{"objective":"安全完成选课事项",'
                                    '"steps":["查询未完成事项","按标题和日期消歧",'
                                    '"请求确认","确认后更新并校验"],'
                                    '"success_criteria":"唯一匹配且经确认后状态为已完成"}'
                                )
                            }
                        }
                    ]
                }
            ]
        )
        port.plan(
            module="life",
            message="我完成了8月10号的选课",
            context={
                "tools": [
                    {
                        "name": "life_prepare_task_completion",
                        "description": "先查找唯一候选再准备完成",
                        "parameters": {"type": "object", "properties": {}},
                        "requires_plan": True,
                    }
                ]
            },
        )
        request = port.requests[0]
        system_prompt = request["messages"][0]["content"]
        user_payload = json.loads(request["messages"][1]["content"])
        self.assertIn("查询真实未完成事项", system_prompt)
        self.assertIn("无匹配或多匹配", system_prompt)
        self.assertTrue(user_payload["available_tools"][0]["requires_plan"])

    def test_planner_classifies_presentation_capability_before_routing(self) -> None:
        port = StubOpenRouter(
            [
                {
                    "choices": [
                        {
                            "message": {
                                "content": json.dumps(
                                    {
                                        "objective": "介绍智能系统",
                                        "steps": ["组织叙事", "生成演示", "检查结果"],
                                        "success_criteria": "生成适合十分钟讲演的演示文稿",
                                        "task_intent": {
                                            "presentation_capabilities": ["narrative"],
                                            "requested_fields": [],
                                            "confidence": "high",
                                            "rationale": "十分钟是讲演时长",
                                        },
                                    },
                                    ensure_ascii=False,
                                )
                            }
                        }
                    ]
                }
            ]
        )
        plan = port.plan(
            module="work",
            message="帮我做一份简单介绍智能系统的ppt，10分钟讲演时间",
            context={
                "tools": [
                    {
                        "name": name,
                        "description": name,
                        "parameters": {"type": "object", "properties": {}},
                    }
                    for name in (
                        "work_generate_pptx",
                        "work_generate_table_pptx",
                        "work_generate_visual_pptx",
                    )
                ],
                "task_contract": {"artifact_types": ["pptx"]},
            },
        )
        self.assertEqual(plan.task_intent["presentation_capabilities"], ["narrative"])
        system_prompt = port.requests[0]["messages"][0]["content"]
        self.assertIn("讲演时长", system_prompt)
        self.assertIn("可以同时出现", system_prompt)
        user_payload = json.loads(port.requests[0]["messages"][1]["content"])
        self.assertEqual(user_payload["task_contract"]["artifact_types"], ["pptx"])
        self.assertEqual(
            [item["name"] for item in user_payload["available_tools"]],
            ["work_generate_pptx"],
        )
        self.assertEqual(port.requests[0]["max_tokens"], 1024)

    def test_plan_failure_uses_generic_decoupled_fallback(self) -> None:
        port = StubOpenRouter(
            [
                tool_response("work_generate_pptx"),
                OpenRouterError("rate limited", status_code=429),
            ]
        )
        plan = port.plan(
            module="work",
            message="根据附件做一个PPT",
            context=context(),
        )
        self.assertEqual(plan.objective, "根据附件做一个PPT")
        self.assertIn("独立工具", plan.steps[1])
        self.assertIn("观察", plan.steps[2])
        self.assertEqual(len(port.requests), 2)

    def test_semantically_empty_plan_uses_default_without_losing_usage(self) -> None:
        port = StubOpenRouter(
            [
                {
                    "choices": [
                        {
                            "message": {
                                "content": (
                                    '{"objective":"   ","steps":["x"],"success_criteria":"   "}'
                                )
                            }
                        }
                    ],
                    "usage": {
                        "prompt_tokens": 30,
                        "completion_tokens": 9,
                        "cost": 0.000004,
                    },
                }
            ]
        )
        plan = port.plan(
            module="work",
            message="根据附件做一个PPT",
            context=context(),
        )
        self.assertEqual(plan.objective, "根据附件做一个PPT")
        events = port.consume_observability()
        self.assertEqual(events[0]["prompt_tokens"], 30)
        self.assertEqual(events[0]["cost_micros"], 4)

    def test_returns_exact_model_selected_tool(self) -> None:
        port = StubOpenRouter(
            [tool_response("life_query_today_plan", '{"local_date":"2026-07-24"}')]
        )
        decision = port.decide(
            module="life",
            message="我今天的计划是什么",
            context=context(),
        )
        self.assertEqual(decision.tool_name, "life_query_today_plan")
        self.assertEqual(
            decision.tool_arguments,
            {"local_date": "2026-07-24"},
        )
        self.assertEqual(port.requests[0]["tool_choice"], "required")
        self.assertNotIn("parallel_tool_calls", port.requests[0])
        self.assertEqual(port.requests[0]["model"], "openrouter/free")
        self.assertNotIn("models", port.requests[0])
        self.assertEqual(port.requests[0]["provider"]["sort"], "price")
        self.assertEqual(
            port.requests[0]["provider"]["preferred_max_latency"],
            {"p90": 8},
        )
        self.assertTrue(port.requests[0]["provider"]["require_parameters"])
        self.assertTrue(port.requests[0]["provider"]["allow_fallbacks"])
        self.assertEqual(
            port.requests[0]["provider"]["max_price"],
            {"prompt": 0.3, "completion": 2.5},
        )

    def test_life_router_receives_contrastive_completion_few_shots(self) -> None:
        port = StubOpenRouter(
            [
                tool_response(
                    "life_prepare_task_completion",
                    '{"title":"选课"}',
                )
            ]
        )
        life_context = {
            "tools": [
                {
                    "name": "life_prepare_task_completion",
                    "description": "完成已有事项",
                    "parameters": {"type": "object", "properties": {}},
                    "requires_plan": True,
                },
                {
                    "name": "life_query_active_reminders",
                    "description": "查询提醒",
                    "parameters": {"type": "object", "properties": {}},
                },
                {
                    "name": "life_prepare_today_plan",
                    "description": "新增今日计划",
                    "parameters": {"type": "object", "properties": {}},
                },
            ]
        }

        decision = port.decide(
            module="life",
            message="我完成了8月10号的选课",
            context=life_context,
        )

        self.assertEqual(decision.tool_name, "life_prepare_task_completion")
        self.assertEqual(decision.tool_arguments, {"title": "选课"})
        prompt = "\n".join(
            str(message.get("content", ""))
            for message in port.requests[0]["messages"]
            if message.get("role") == "system"
        )
        self.assertIn("我完成了8月10号的选课", prompt)
        self.assertIn("life_prepare_task_completion", prompt)
        self.assertIn("8月10号有选课提醒吗", prompt)
        self.assertIn("life_query_active_reminders", prompt)
        self.assertIn("把选课加入今日计划", prompt)
        self.assertIn("我还没完成选课", prompt)
        self.assertIn("我准备完成选课", prompt)
        self.assertIn("date_hint", prompt)
        self.assertIn("不能只凭关键词", prompt)

    def test_life_router_treats_direct_answer_as_missing_reminder_slot(self) -> None:
        port = StubOpenRouter([])
        life_context = {
            "history": [
                {"role": "user", "content": "提醒我提交报销材料"},
                {"role": "assistant", "content": "还需要补充提醒日期。"},
            ],
            "tools": [
                {
                    "name": "life_prepare_reminder",
                    "description": "创建或继续补充提醒",
                    "parameters": {
                        "type": "object",
                        "properties": {
                            "title": {"type": "string"},
                            "date_hint": {"type": "string"},
                        },
                    },
                },
                {
                    "name": "life_no_tool",
                    "description": "无需工具",
                    "parameters": {"type": "object", "properties": {}},
                },
            ],
        }

        decision = port.decide(
            module="life",
            message="下周五",
            context=life_context,
        )

        self.assertEqual(decision.tool_name, "life_prepare_reminder")
        self.assertEqual(decision.tool_arguments, {})
        self.assertEqual(port.requests, [])

    def test_life_router_locks_exact_ledger_clarification_to_unfinished_flow(self) -> None:
        port = StubOpenRouter([])
        life_context = {
            "history": [
                {"role": "user", "content": "吃饭花了20"},
                {
                    "role": "assistant",
                    "content": (
                        "已保留金额“¥ 20.00”、类型“支出”；还需要补充“发生时间”，"
                        "补充后我就能写入生活账本。"
                    ),
                },
            ],
            "tools": [
                {
                    "name": "life_prepare_ledger_entry",
                    "description": "创建或继续补充账单",
                    "parameters": {"type": "object", "properties": {}},
                },
                {
                    "name": "life_prepare_schedule_change",
                    "description": "调整或继续补充事项时间",
                    "parameters": {"type": "object", "properties": {}},
                },
                {
                    "name": "life_no_tool",
                    "description": "无需工具",
                    "parameters": {"type": "object", "properties": {}},
                },
            ],
        }

        decision = port.decide(
            module="life",
            message="今天‘",
            context=life_context,
        )

        self.assertEqual(decision.tool_name, "life_prepare_ledger_entry")
        self.assertEqual(decision.tool_arguments, {})
        self.assertEqual(port.requests, [])

    def test_explicit_today_plan_query_can_leave_ledger_clarification(self) -> None:
        port = StubOpenRouter([tool_response("life_query_today_plan")])
        life_context = {
            "history": [
                {"role": "user", "content": "吃饭花了20"},
                {
                    "role": "assistant",
                    "content": "还需要补充“发生时间”，补充后我就能写入生活账本。",
                },
            ],
            "tools": [
                {
                    "name": "life_prepare_ledger_entry",
                    "description": "创建或继续补充账单",
                    "parameters": {"type": "object", "properties": {}},
                },
                {
                    "name": "life_query_today_plan",
                    "description": "查询今日计划",
                    "parameters": {"type": "object", "properties": {}},
                },
                {
                    "name": "life_no_tool",
                    "description": "无需工具",
                    "parameters": {"type": "object", "properties": {}},
                },
            ],
        }

        decision = port.decide(
            module="life",
            message="今天的计划是什么",
            context=life_context,
        )

        self.assertEqual(decision.tool_name, "life_query_today_plan")
        self.assertEqual(port.requests[0]["tool_choice"], "required")

    def test_required_continuation_arguments_are_pinned_to_one_tool(self) -> None:
        port = StubOpenRouter([tool_response("life_prepare_today_plan", '{"title":"查看邮件"}')])
        life_context = {
            "history": [
                {"role": "user", "content": "加入今日计划"},
                {
                    "role": "assistant",
                    "content": "请告诉我要加入今日计划的具体事项。",
                },
            ],
            "tools": [
                {
                    "name": "life_prepare_today_plan",
                    "description": "创建今日计划",
                    "parameters": {
                        "type": "object",
                        "properties": {"title": {"type": "string"}},
                        "required": ["title"],
                    },
                },
                {
                    "name": "life_query_today_plan",
                    "description": "查询今日计划",
                    "parameters": {"type": "object", "properties": {}},
                },
                {
                    "name": "life_no_tool",
                    "description": "无需工具",
                    "parameters": {"type": "object", "properties": {}},
                },
            ],
        }

        decision = port.decide(
            module="life",
            message="查看邮件",
            context=life_context,
        )

        self.assertEqual(decision.tool_name, "life_prepare_today_plan")
        self.assertEqual(decision.tool_arguments, {"title": "查看邮件"})
        self.assertEqual(
            port.requests[0]["tool_choice"],
            {
                "type": "function",
                "function": {"name": "life_prepare_today_plan"},
            },
        )
        self.assertEqual(
            [item["function"]["name"] for item in port.requests[0]["tools"]],
            ["life_prepare_today_plan"],
        )

    def test_router_selects_and_composer_builds_complex_tool_arguments(self) -> None:
        port = StubOpenRouter(
            [
                tool_response(
                    "work_create_markdown_document",
                    '{"content":"must be discarded"}',
                ),
                tool_response(
                    "work_create_markdown_document",
                    '{"title":"方案","content":"# 方案\\n\\n完整正文"}',
                ),
            ]
        )
        complex_context = {
            "tools": [
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
        }
        decision = port.decide(
            module="work",
            message="创建一份方案文档",
            context=complex_context,
        )
        self.assertTrue(decision.requires_argument_composition)
        self.assertEqual(decision.tool_arguments, {})
        router_schema = port.requests[0]["tools"][0]["function"]["parameters"]
        self.assertEqual(router_schema["properties"], {})

        arguments = port.compose_arguments(
            module="work",
            message="创建一份方案文档",
            tool_name=decision.tool_name,
            context=complex_context,
        )
        self.assertEqual(arguments["title"], "方案")
        self.assertIn("完整正文", arguments["content"])
        composer_tool = port.requests[1]["tools"][0]["function"]
        self.assertIn("content", composer_tool["parameters"]["properties"])
        self.assertEqual(
            port.requests[1]["tool_choice"],
            {
                "type": "function",
                "function": {"name": "work_create_markdown_document"},
            },
        )
        self.assertEqual(port.requests[1]["max_tokens"], 12288)

    def test_composer_and_cross_model_fallback_use_bounded_reasoning(self) -> None:
        port = StubOpenRouter(
            [
                {"choices": [{"message": {"content": ""}}]},
                tool_response(
                    "work_create_markdown_document",
                    '{"title":"方案","content":"完整正文"}',
                ),
            ]
        )
        port._config = OpenRouterConfig(
            base_url="https://openrouter.ai/api/v1",
            api_key="test-key",
            models=("deepseek/pinned", "openai/pinned"),
            composer_models=("deepseek/pinned", "openai/pinned"),
            reasoning_effort="high",
            composer_reasoning_effort="low",
            fallback_reasoning_effort="minimal",
        )
        complex_context = {
            "tools": [
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
                    },
                    "compose_arguments": True,
                }
            ]
        }

        arguments = port.compose_arguments(
            module="work",
            message="创建一份方案文档",
            tool_name="work_create_markdown_document",
            context=complex_context,
        )

        self.assertEqual(arguments["title"], "方案")
        self.assertEqual(
            [request["reasoning"]["effort"] for request in port.requests],
            ["low", "minimal"],
        )
        events = port.consume_observability()
        self.assertEqual(events[0]["contract_error"], "model_tool_call_count")
        self.assertFalse(events[0]["contract_valid"])
        self.assertEqual(
            [event["reasoning_effort"] for event in events],
            ["low", "minimal"],
        )

    def test_email_composer_uses_profile_language_contract_and_quality_fallback(self) -> None:
        arguments = {
            "to": [],
            "subject": "Question About Project Courses",
            "purpose": "Ask whether a Project course may be taken in Semester A.",
            "output_language": "en-US",
            "relationship": "first_contact",
            "introduction_policy": "required",
            "sender_name": "Alex Chen",
            "salutation": "Dear Sir or Madam,",
            "introduction": "My name is Alex Chen, and I am a prospective student.",
            "body_paragraphs": ["The Project course is part of my planned Semester A schedule."],
            "request_or_next_step": "Could you please share the prerequisites?",
            "courtesy": "Thank you for your time and assistance.",
            "closing": "Kind regards,",
            "signature_lines": ["Alex Chen"],
            "tone": "formal",
        }
        port = StubOpenRouter(
            [
                tool_response("work_draft_email", json.dumps(arguments)),
                tool_response("work_draft_email", json.dumps(arguments)),
            ]
        )
        email_context = {
            "tools": [
                {
                    "name": "work_draft_email",
                    "description": "生成完整邮件草稿",
                    "compose_arguments": True,
                    "parameters": {"type": "object", "properties": {}},
                }
            ],
            "email_profile": {
                "sender_name": "Alex Chen",
                "default_language": "zh-CN",
            },
        }
        composed = port.compose_arguments(
            module="work",
            message="请写一封英文邮件询问 Project 课",
            tool_name="work_draft_email",
            context=email_context,
        )
        self.assertEqual(composed["output_language"], "en-US")
        self.assertEqual(port.requests[0]["model"], "openrouter/free")
        self.assertEqual(port.requests[0]["max_tokens"], 1200)
        self.assertIn("邮件专用编排", port.requests[0]["messages"][0]["content"])
        self.assertIn("body_paragraphs 只能写背景", port.requests[0]["messages"][0]["content"])
        self.assertIn("各字段不得换一种说法重复", port.requests[0]["messages"][0]["content"])
        routed = json.loads(port.requests[0]["messages"][1]["content"])
        self.assertEqual(routed["email_profile"]["sender_name"], "Alex Chen")

        retry_context = dict(email_context)
        retry_context["email_validation"] = {
            "passed": False,
            "violations": ["english_language_contamination"],
        }
        port.compose_arguments(
            module="work",
            message="请写一封英文邮件询问 Project 课",
            tool_name="work_draft_email",
            context=retry_context,
        )
        self.assertEqual(port.requests[1]["model"], "free/fallback")
        self.assertIn(
            "english_language_contamination",
            port.requests[1]["messages"][0]["content"],
        )

    def test_presentation_composer_uses_quality_feedback_and_diverse_fallback(self) -> None:
        arguments = {
            "title": "体育课课程安排总览",
            "audience": "学生",
            "style": "表格",
            "brief": "完整课程安排",
            "slide_count": 5,
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
        port = StubOpenRouter([tool_response("work_generate_pptx", json.dumps(arguments))])
        presentation_context = {
            "tools": [
                {
                    "name": "work_generate_pptx",
                    "description": "生成 PPTX",
                    "compose_arguments": True,
                    "parameters": {
                        "type": "object",
                        "required": ["title", "audience", "style", "brief", "slide_count"],
                        "additionalProperties": False,
                        "properties": {
                            "title": {"type": "string"},
                            "audience": {"type": "string"},
                            "style": {"type": "string"},
                            "brief": {"type": "string"},
                            "slide_count": {"type": "integer"},
                            "mapping_contract": {"type": "object"},
                            "task_contract": {"type": "object"},
                            "source_coverage": {"type": "object"},
                            "table": {
                                "type": "object",
                                "properties": {"rows": {"type": "array"}},
                            },
                        },
                    },
                }
            ],
            "artifact_validation": {
                "passed": False,
                "violations": [
                    {
                        "code": "structured_table_missing",
                        "message": "必须提供表格",
                    }
                ],
            },
        }

        composed = port.compose_arguments(
            module="work",
            message="整理所有体育课并生成中文 PPT",
            tool_name="work_generate_pptx",
            context=presentation_context,
        )

        self.assertIn("table", composed)
        self.assertEqual(port.requests[0]["model"], "free/fallback")
        self.assertIn(
            "structured_table_missing",
            port.requests[0]["messages"][0]["content"],
        )
        self.assertIn(
            "课程名称必须翻译",
            port.requests[0]["messages"][0]["content"],
        )
        self.assertIn(
            "task_contract.output_language",
            port.requests[0]["messages"][0]["content"],
        )
        self.assertIn(
            "不得使用‘节选’、‘摘要’、‘示例’、‘部分’",
            port.requests[0]["messages"][0]["content"],
        )
        self.assertIn(
            "不得保留夹杂在中文名称中的拉丁字母单词",
            port.requests[0]["messages"][0]["content"],
        )

    def test_presentation_composer_processes_only_the_current_document_round(self) -> None:
        arguments = {
            "title": "体育课课程安排",
            "audience": "学生",
            "style": "表格",
            "brief": "当前轮次课程记录",
            "slide_count": 3,
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
        port = StubOpenRouter(
            [tool_response("work_generate_pptx", json.dumps(arguments, ensure_ascii=False))]
        )
        context = {
            "tools": [
                {
                    "name": "work_generate_pptx",
                    "description": "生成 PPTX",
                    "compose_arguments": True,
                    "parameters": {
                        "type": "object",
                        "required": ["title", "audience", "style", "brief", "slide_count"],
                        "additionalProperties": False,
                        "properties": {
                            "title": {"type": "string"},
                            "audience": {"type": "string"},
                            "style": {"type": "string"},
                            "brief": {"type": "string"},
                            "slide_count": {"type": "integer"},
                            "mapping_contract": {"type": "object"},
                            "task_contract": {"type": "object"},
                            "source_coverage": {"type": "object"},
                            "table": {
                                "type": "object",
                                "properties": {"rows": {"type": "array"}},
                            },
                        },
                    },
                }
            ],
            "document_processing_round": {
                "batch_id": "a1:r1",
                "round_number": 1,
                "round_count": 3,
            },
            "artifact_validation": {
                "passed": False,
                "violations": [{"code": "source_records_missing"}],
            },
            "observations": [
                {
                    "tool_name": "work_extract_attached_document",
                    "data": {"output": {"text": "PED1101 Canoeing 10:00-11:50"}},
                }
            ],
        }
        port.compose_arguments(
            module="work",
            message="整理所有体育课并生成中文 PPT",
            tool_name="work_generate_pptx",
            context=context,
        )
        system_prompt = port.requests[0]["messages"][0]["content"]
        self.assertIn("只处理 completed_observations 中当前这一轮", system_prompt)
        self.assertIn("只重新生成当前来源轮次", system_prompt)
        self.assertNotIn("必须重新生成全部参数", system_prompt)
        self.assertIn("严禁放在table对象上", system_prompt.replace(" ", ""))
        routed = json.loads(port.requests[0]["messages"][1]["content"])
        self.assertEqual(routed["document_processing_round"]["batch_id"], "a1:r1")
        self.assertIn("PED1101", routed["completed_observations"][0]["data"]["output"]["text"])
        self.assertEqual(port.requests[0]["max_tokens"], 6144)
        batch_schema = port.requests[0]["tools"][0]["function"]["parameters"]
        self.assertEqual(
            set(batch_schema["properties"]),
            {"title", "audience", "style", "table"},
        )
        self.assertEqual(
            batch_schema["required"],
            ["title", "audience", "style", "table"],
        )
        self.assertIn("紧凑中间格式", system_prompt)

    def test_focused_presentation_round_uses_relevance_prompt_not_mapping_contract(self) -> None:
        port = StubOpenRouter(
            [
                {
                    "choices": [
                        {
                            "message": {
                                "tool_calls": [
                                    {
                                        "function": {
                                            "name": "work_generate_pptx",
                                            "arguments": json.dumps(
                                                {
                                                    "title": "重要日期",
                                                    "audience": "新生",
                                                    "style": "简洁表格",
                                                    "brief": (
                                                        "## A学期重要日期\n"
                                                        "- 开学日期以第4页为准\n"
                                                        "- 注册截止日期以第4页为准"
                                                    ),
                                                    "slide_count": 3,
                                                },
                                                ensure_ascii=False,
                                            ),
                                        }
                                    }
                                ]
                            }
                        }
                    ]
                }
            ]
        )
        context = {
            "tools": [
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
                        },
                        "additionalProperties": False,
                    },
                }
            ],
            "task_contract": {"output_language": "zh-CN", "requested_fields": []},
            "document_processing_round": {
                "batch_id": "a1:focus1",
                "round_number": 1,
                "round_count": 1,
                "focused": True,
                "structured": False,
                "focus_phrases": ["Important Dates"],
            },
            "observations": [
                {
                    "tool_name": "work_extract_attached_document",
                    "data": {"output": {"text": "[[PAGE 4]] Important Dates"}},
                }
            ],
        }

        port.compose_arguments(
            module="work",
            message="提取 Important Dates 并生成中文 PPT",
            tool_name="work_generate_pptx",
            context=context,
        )
        system_prompt = port.requests[0]["messages"][0]["content"]

        self.assertIn("相关证据页或上下文窗口", system_prompt)
        self.assertIn("不得把多条事实概括成摘要", system_prompt)
        self.assertNotIn("第一轮必须生成 mapping_contract", system_prompt)
        self.assertIn("非表格演示章节", system_prompt)
        self.assertIn("按来源顺序去重合并章节", system_prompt)
        self.assertNotIn("当前轮次 table", system_prompt)
        self.assertIn("（来源：第N页）", system_prompt)
        self.assertIn("禁止为了简化而代数改写、重新缩放", system_prompt)
        self.assertIn("不得自行补出公式", system_prompt)

    def test_bounded_narrative_round_preserves_composer_fallback_attempt(self) -> None:
        arguments = {
            "title": "课程重点",
            "audience": "学生",
            "style": "简洁图文",
            "brief": "## 核心概念\n- 感知连接输入与系统\n- 推理支持后续决策",
            "slide_count": 3,
        }
        port = StubOpenRouter(
            [
                OpenRouterError("primary timed out", status_code=408),
                tool_response(
                    "work_generate_pptx",
                    json.dumps(arguments, ensure_ascii=False),
                ),
            ],
            elapsed_seconds=[30, 1],
        )
        presentation_context = {
            "tools": [
                {
                    "name": "work_generate_pptx",
                    "description": "生成叙事型 PPTX",
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
                            "mapping_contract": {"type": "object"},
                            "task_contract": {"type": "object"},
                            "source_coverage": {"type": "object"},
                        },
                        "additionalProperties": False,
                    },
                }
            ],
            "task_contract": {
                "artifact_types": ["pptx"],
                "presentation_capabilities": ["narrative", "visual"],
                "output_language": "zh-CN",
            },
            "document_processing_round": {
                "batch_id": "a1:r1:s1",
                "round_number": 1,
                "round_count": 15,
                "structured": False,
                "focused": False,
            },
            "observations": [
                {
                    "tool_name": "work_extract_attached_document",
                    "data": {
                        "output": {
                            "text": "[[PAGE 1]]\n" + "课程材料。" * 1_400,
                            "source_pages": [1],
                            "source_locator": "Lecture2b.pdf · 第 1 页",
                        }
                    },
                }
            ],
            # Matches the remaining allowance seen in the failed production
            # checkpoint. The bounded source must leave room for both models.
            "model_allowance": {
                "remaining_calls": 61,
                "remaining_prompt_tokens": 996_038,
                "remaining_completion_tokens": 129_898,
                "remaining_cost_micros": 247_757,
            },
        }

        composed = port.compose_arguments(
            module="work",
            message="提取主要内容并生成中文图文 PPT",
            tool_name="work_generate_pptx",
            context=presentation_context,
        )

        self.assertEqual(composed["title"], "课程重点")
        self.assertEqual(
            [request["model"] for request in port.requests],
            ["openrouter/free", "free/fallback"],
        )
        self.assertEqual(port.request_timeouts, [30, 30])
        composer_schema = port.requests[0]["tools"][0]["function"]["parameters"]
        self.assertIn("brief", composer_schema["properties"])
        self.assertIn("brief", composer_schema["required"])
        self.assertNotIn("table", composer_schema["properties"])
        self.assertNotIn("mapping_contract", composer_schema["properties"])
        self.assertIn(
            "非表格演示章节",
            port.requests[0]["messages"][0]["content"],
        )
        events = port.consume_observability()
        self.assertEqual([event["status"] for event in events], ["error", "succeeded"])

    def test_composer_falls_back_when_first_tool_arguments_violate_contract(self) -> None:
        valid = {
            "title": "课程重点",
            "audience": "学生",
            "style": "简洁图文",
            "brief": "## 核心概念\n- 感知连接输入与系统\n- 推理支持后续决策",
            # A batch can repeat the requested final deck size. The Harness
            # recomputes the merged slide count after all sections are joined.
            "slide_count": 5,
        }
        port = StubOpenRouter(
            [
                tool_response("work_generate_pptx", '{"audience":"学生"}'),
                tool_response(
                    "work_generate_pptx",
                    json.dumps(valid, ensure_ascii=False),
                ),
            ]
        )
        context = {
            "tools": [
                {
                    "name": "work_generate_pptx",
                    "description": "生成叙事型 PPTX",
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
                        },
                        "additionalProperties": False,
                    },
                }
            ],
            "task_contract": {
                "artifact_types": ["pptx"],
                "presentation_capabilities": ["narrative"],
            },
            "document_processing_round": {
                "batch_id": "a1:r1",
                "round_number": 1,
                "round_count": 2,
                "structured": False,
            },
            "observations": [],
        }

        composed = port.compose_arguments(
            module="work",
            message="生成课程 PPT",
            tool_name="work_generate_pptx",
            context=context,
        )

        self.assertEqual(composed, valid)
        self.assertEqual(len(port.requests), 2)
        events = port.consume_observability()
        self.assertFalse(events[0]["contract_valid"])
        self.assertEqual(events[0]["contract_error"], "model_tool_arguments_invalid")
        self.assertEqual(events[1]["status"], "succeeded")

    def test_narrative_batch_allows_extra_valid_sections_for_final_merge(self) -> None:
        brief = "\n\n".join(
            [
                "## 几何直观\n- 第一条事实\n- 第二条事实",
                "## 最大间隔\n- 第一条事实\n- 第二条事实",
                "## 支持向量\n- 第一条事实\n- 第二条事实",
            ]
        )

        _validate_narrative_batch_arguments({"brief": brief, "slide_count": 12})

    def test_repairer_returns_strict_allowlisted_plan(self) -> None:
        port = StubOpenRouter(
            [
                {
                    "choices": [
                        {
                            "message": {
                                "content": (
                                    '{"strategy":"operator",'
                                    '"operator_id":"filename.safe_basename",'
                                    '"patches":[],"reason_code":"path_separator"}'
                                )
                            }
                        }
                    ]
                }
            ]
        )
        decision = port.repair(
            module="work",
            message="生成 PPT",
            tool_name="work_generate_pptx",
            arguments={"filename": "A/B.pptx"},
            failure={
                "code": "invalid_output_filename",
                "category": "argument_validation",
                "phase": "pre_execution",
                "field_paths": ["/filename"],
                "allowed_repairs": ["filename.safe_basename"],
            },
            context={
                "repair_history": [],
                "repair_policies": [
                    {
                        "operator_id": "filename.safe_basename",
                        "field_path": "/filename",
                        "extension": ".pptx",
                        "semantics_preserving": True,
                    }
                ],
            },
        )
        self.assertEqual(decision.operator_id, "filename.safe_basename")
        response_format = port.requests[0]["response_format"]
        self.assertEqual(response_format["type"], "json_schema")
        self.assertTrue(response_format["json_schema"]["strict"])
        self.assertEqual(
            response_format["json_schema"]["schema"]["properties"]["operator_id"]["enum"],
            ["filename.safe_basename"],
        )
        repair_request = json.loads(port.requests[0]["messages"][1]["content"])
        self.assertEqual(
            repair_request["operator_catalog"][0]["operator_id"],
            "filename.safe_basename",
        )
        self.assertEqual(port.requests[0]["temperature"], 0)
        self.assertEqual(port.requests[0]["max_tokens"], 256)

    def test_no_tool_returns_conversation_in_the_routing_call(self) -> None:
        port = StubOpenRouter(
            [
                tool_response("life_no_tool"),
                {"choices": [{"message": {"content": "我在，想聊什么都可以。"}}]},
            ]
        )
        decision = port.decide(
            module="life",
            message="今天心情不错",
            context=context(),
        )
        self.assertEqual(decision.tool_name, "")
        self.assertFalse(decision.needs_response)
        self.assertEqual(decision.response, "我在，想聊什么都可以。")
        self.assertEqual(len(port.requests), 2)
        self.assertIn("tools", port.requests[0])
        self.assertNotIn("tools", port.requests[1])

    def test_records_openrouter_usage_and_actual_model(self) -> None:
        response = tool_response("life_query_today_plan")
        response.update(
            {
                "id": "gen-1",
                "model": "openai/gpt-5-nano",
                "provider": "OpenAI",
                "usage": {
                    "prompt_tokens": 100,
                    "completion_tokens": 12,
                    "cost": 0.0000004,
                    "prompt_tokens_details": {"cached_tokens": 40},
                    "completion_tokens_details": {"reasoning_tokens": 2},
                },
            }
        )
        port = StubOpenRouter([response])
        port.decide(
            module="life",
            message="我今天的计划是什么",
            context=context(),
        )
        events = port.consume_observability()
        self.assertEqual(len(events), 1)
        self.assertEqual(events[0]["returned_model"], "openai/gpt-5-nano")
        self.assertEqual(events[0]["upstream_provider"], "OpenAI")
        self.assertEqual(events[0]["prompt_tokens"], 100)
        self.assertEqual(events[0]["cached_tokens"], 40)
        self.assertEqual(events[0]["reasoning_tokens"], 2)
        self.assertEqual(events[0]["cost_micros"], 1)

    def test_role_models_and_price_routing_are_explicit(self) -> None:
        port = OpenRouterDecisionPort(
            OpenRouterConfig(
                base_url="https://openrouter.ai/api/v1",
                api_key="test-key",
                models=("openai/gpt-5-mini",),
                planner_models=("openai/gpt-5-mini",),
                router_models=("openai/gpt-5-nano",),
                composer_models=("openai/gpt-5-mini",),
                assessor_models=("openai/gpt-5-nano",),
                responder_models=("openai/gpt-5-mini",),
                require_pinned_models=True,
            )
        )
        manifest = port.model_manifest()
        self.assertEqual(
            manifest["roles"]["router"]["models"],
            ["openai/gpt-5-nano"],
        )
        self.assertEqual(
            manifest["roles"]["composer"]["models"],
            ["openai/gpt-5-mini"],
        )
        self.assertEqual(
            manifest["roles"]["companion_responder"]["models"],
            [
                "openai/gpt-oss-20b:free",
                "google/gemma-4-31b-it:free",
                "inclusionai/ling-3.0-flash:free",
            ],
        )
        self.assertEqual(
            manifest["roles"]["companion_responder"]["billing_class"],
            "free",
        )
        self.assertTrue(manifest["pinned"])
        self.assertEqual(manifest["routing"]["sort"], "price")
        self.assertEqual(
            manifest["routing"]["preferred_max_latency"],
            {"p90": 8},
        )
        self.assertTrue(manifest["routing"]["require_parameters"])
        self.assertTrue(manifest["routing"]["allow_fallbacks"])
        self.assertEqual(
            manifest["routing"]["max_price"],
            {"prompt": 0.3, "completion": 2.5},
        )
        self.assertEqual(
            manifest["routing"]["timeouts"],
            {
                "fallback_deadline_seconds": 30,
                "attempt_timeout_seconds": 15,
                "composer_fallback_deadline_seconds": 90,
                "composer_attempt_timeout_seconds": 30,
                "min_fallback_timeout_seconds": 5,
            },
        )
        self.assertEqual(manifest["roles"]["composer"]["batch_max_output_tokens"], 6144)
        self.assertEqual(manifest["roles"]["planner"]["max_output_tokens"], 1024)
        self.assertEqual(manifest["inference"]["planner_reasoning_effort"], "high")
        self.assertEqual(manifest["inference"]["router_reasoning_effort"], "low")
        self.assertEqual(manifest["inference"]["composer_reasoning_effort"], "low")
        self.assertEqual(manifest["inference"]["assessor_reasoning_effort"], "minimal")
        self.assertEqual(manifest["inference"]["fallback_reasoning_effort"], "low")

    def test_remaining_run_budget_bounds_completion_before_dispatch(self) -> None:
        port = StubOpenRouter([tool_response("life_query_today_plan")])
        limited_context = context()
        limited_context["model_allowance"] = {
            "remaining_calls": 1,
            "remaining_prompt_tokens": 200_000,
            "remaining_completion_tokens": 90,
            "remaining_cost_micros": 50_000,
        }
        port.decide(
            module="life",
            message="我今天的计划是什么",
            context=limited_context,
        )
        # Prompt and cost ceilings leave enough room; the stricter 90-token
        # completion budget wins.
        self.assertEqual(port.requests[0]["max_tokens"], 90)

    def test_cross_model_fallback_reserves_budget_for_every_attempt(self) -> None:
        port = StubOpenRouter(
            [
                {"choices": [{"message": {"content": "直接回答了用户"}}]},
                tool_response("life_query_today_plan"),
            ]
        )
        limited_context = context()
        limited_context["model_allowance"] = {
            "remaining_calls": 2,
            "remaining_prompt_tokens": 200_000,
            "remaining_completion_tokens": 200,
            "remaining_cost_micros": 50_000,
        }
        decision = port.decide(
            module="life",
            message="我今天的计划是什么",
            context=limited_context,
        )
        self.assertEqual(decision.tool_name, "life_query_today_plan")
        self.assertEqual(len(port.requests), 2)
        self.assertEqual(
            [request["max_tokens"] for request in port.requests],
            [100, 100],
        )

    def test_prompt_budget_blocks_dispatch_before_any_fallback(self) -> None:
        port = StubOpenRouter([tool_response("life_query_today_plan")])
        limited_context = context()
        limited_context["model_allowance"] = {
            "remaining_calls": 2,
            "remaining_prompt_tokens": 1,
            "remaining_completion_tokens": 200,
            "remaining_cost_micros": 50_000,
        }
        with self.assertRaisesRegex(ModelBudgetExceeded, "输入 token"):
            port.decide(
                module="life",
                message="我今天的计划是什么",
                context=limited_context,
            )
        self.assertEqual(port.requests, [])

    def test_production_pin_policy_rejects_dynamic_router(self) -> None:
        with self.assertRaises(ValueError):
            OpenRouterDecisionPort(
                OpenRouterConfig(
                    base_url="https://openrouter.ai/api/v1",
                    api_key="test-key",
                    models=("openrouter/free",),
                    require_pinned_models=True,
                )
            )

    def test_pinned_companion_responder_rejects_non_free_models(self) -> None:
        with self.assertRaisesRegex(ValueError, "zero-price"):
            OpenRouterConfig(
                base_url="https://openrouter.ai/api/v1",
                api_key="test-key",
                models=("openai/gpt-5-mini",),
                companion_responder_models=("openai/gpt-5-mini",),
                require_pinned_models=True,
            ).validate()

    def test_companion_uses_free_responder_role_only(self) -> None:
        port = StubOpenRouter(
            [
                {"choices": [{"message": {"content": "我在这里。"}}]},
                {"choices": [{"message": {"content": "工作回复。"}}]},
            ]
        )
        self.assertEqual(
            port.respond(
                module="companion",
                message="陪我聊聊",
                context=context(),
            ),
            "我在这里。",
        )
        self.assertEqual(
            port.requests[0]["model"],
            "openai/gpt-oss-20b:free",
        )
        self.assertEqual(
            port.respond(
                module="work",
                message="总结一下",
                context=context(),
            ),
            "工作回复。",
        )
        self.assertEqual(port.requests[1]["model"], "openrouter/free")

    def test_life_response_returns_database_observation_verbatim(self) -> None:
        port = StubOpenRouter([])
        trusted = "今日计划（2026-08-14）：\n- 2026-08-14 查看邮件（时间待安排）"
        response = port.respond(
            module="life",
            message="查看今日计划",
            context={
                **context(),
                "observations": [
                    {
                        "tool_name": "life_query_today_plan",
                        "status": "completed",
                        "response": trusted,
                    }
                ],
            },
        )
        self.assertEqual(response, trusted)
        self.assertEqual(port.requests, [])

    def test_response_revision_only_includes_three_latest_observations(self) -> None:
        port = StubOpenRouter([{"choices": [{"message": {"content": "已去除重复内容。"}}]}])
        observations = [{"sequence": sequence} for sequence in range(5)]

        revised = port.revise_response(
            module="work",
            message="修复重复内容",
            response="重复。重复。",
            violations=[{"kind": "exact_duplicate"}],
            context={"observations": observations},
        )

        self.assertEqual(revised, "已去除重复内容。")
        revision_request = json.loads(port.requests[0]["messages"][1]["content"])
        self.assertEqual(
            revision_request["trusted_observations"],
            observations[-3:],
        )
        self.assertEqual(port.requests[0]["temperature"], 0)

    def test_empty_response_revision_uses_bounded_fallback(self) -> None:
        port = StubOpenRouter(
            [
                {"choices": [{"message": {"content": ""}}]},
                {"choices": [{"message": {"content": "已由备用模型完成修复。"}}]},
            ]
        )
        revised = port.revise_response(
            module="work",
            message="修复重复内容",
            response="重复内容",
            violations=[{"code": "near_duplicate_paragraph"}],
            context={
                "model_allowance": {
                    "remaining_calls": 2,
                    "remaining_prompt_tokens": 100_000,
                    "remaining_completion_tokens": 4_096,
                    "remaining_cost_micros": 1_000_000,
                }
            },
        )
        self.assertEqual(revised, "已由备用模型完成修复。")
        self.assertEqual(
            [request["model"] for request in port.requests],
            ["openrouter/free", "free/fallback"],
        )

    def test_config_rejects_non_finite_price_ceiling(self) -> None:
        with self.assertRaisesRegex(ValueError, "max prices"):
            OpenRouterConfig(
                base_url="https://openrouter.ai/api/v1",
                api_key="test-key",
                models=("openai/gpt-5-mini",),
                max_prompt_price=float("nan"),
            ).validate()

    def test_config_rejects_invalid_provider_latency_preference(self) -> None:
        with self.assertRaisesRegex(ValueError, "PREFERRED_MAX_LATENCY"):
            OpenRouterConfig(
                base_url="https://openrouter.ai/api/v1",
                api_key="test-key",
                models=("openai/gpt-5-mini",),
                preferred_max_latency_p90=float("nan"),
            ).validate()

    def test_config_rejects_attempt_timeout_above_shared_deadline(self) -> None:
        with self.assertRaisesRegex(ValueError, "cannot exceed MODEL_TIMEOUT_SECONDS"):
            OpenRouterConfig(
                base_url="https://openrouter.ai/api/v1",
                api_key="test-key",
                models=("openai/gpt-5-mini",),
                timeout_seconds=10,
                attempt_timeout_seconds=11,
            ).validate()

    def test_three_model_timeouts_share_one_deadline(self) -> None:
        port = StubOpenRouter(
            [
                OpenRouterError("primary timed out", status_code=408),
                OpenRouterError("fallback timed out", status_code=408),
                OpenRouterError("last fallback timed out", status_code=408),
            ],
            elapsed_seconds=[15, 10, 5],
        )

        with self.assertRaises(OpenRouterError):
            port.respond(module="companion", message="你好", context=context())

        self.assertEqual(port.request_timeouts, [15, 10, 5])
        events = port.consume_observability()
        self.assertEqual([item["timeout_ms"] for item in events], [15000, 10000, 5000])
        self.assertTrue(all(item["retryable"] for item in events))

    def test_fast_failure_preserves_full_timeout_for_next_model(self) -> None:
        port = StubOpenRouter(
            [
                OpenRouterError(
                    "provider unavailable",
                    status_code=503,
                    retry_after="7",
                ),
                tool_response("life_query_today_plan"),
            ]
        )

        decision = port.decide(
            module="life",
            message="我今天的计划是什么",
            context=context(),
        )

        self.assertEqual(decision.tool_name, "life_query_today_plan")
        self.assertEqual(port.request_timeouts, [15, 15])
        events = port.consume_observability()
        self.assertEqual([item["status"] for item in events], ["error", "succeeded"])
        self.assertEqual(events[0]["retry_after"], "7")

    def test_fallback_deadline_exhaustion_is_retryable(self) -> None:
        port = StubOpenRouter(
            [OpenRouterError("transport exceeded timeout", status_code=408)],
            elapsed_seconds=[31],
        )

        with self.assertRaisesRegex(OpenRouterError, "deadline exhausted") as raised:
            port.decide(
                module="life",
                message="我今天的计划是什么",
                context=context(),
            )

        self.assertEqual(raised.exception.status_code, 408)
        self.assertTrue(raised.exception.retryable)
        self.assertEqual(port.request_timeouts, [15])

    def test_real_request_timeouts_are_bounded_by_shared_wall_clock(self) -> None:
        port = OpenRouterDecisionPort(
            OpenRouterConfig(
                base_url="https://openrouter.ai/api/v1",
                api_key="test-key",
                models=("openrouter/free", "free/fallback"),
                timeout_seconds=0.3,
                attempt_timeout_seconds=0.12,
                min_fallback_timeout_seconds=0.06,
            )
        )
        observed_timeouts: list[float] = []

        def slow_timeout(*_args: Any, timeout: float, **_kwargs: Any) -> None:
            observed_timeouts.append(timeout)
            time.sleep(timeout)
            raise TimeoutError("controlled timeout")

        started = time.monotonic()
        with patch("urllib.request.urlopen", side_effect=slow_timeout):
            with self.assertRaises(OpenRouterError):
                port.respond(module="companion", message="你好", context=context())
        elapsed = time.monotonic() - started

        self.assertEqual(len(observed_timeouts), 3)
        self.assertAlmostEqual(observed_timeouts[0], 0.12, places=2)
        self.assertLessEqual(sum(observed_timeouts), 0.31)
        self.assertGreaterEqual(elapsed, 0.25)
        self.assertLess(elapsed, 0.6)

    def test_http_attempt_has_a_true_wall_clock_deadline(self) -> None:
        port = OpenRouterDecisionPort(
            OpenRouterConfig(
                base_url="https://openrouter.ai/api/v1",
                api_key="test-key",
                models=("openrouter/free",),
                companion_responder_models=("openrouter/free",),
                timeout_seconds=0.2,
                attempt_timeout_seconds=0.05,
                min_fallback_timeout_seconds=0.01,
            )
        )

        def ignore_socket_timeout(*_args: Any, **_kwargs: Any) -> None:
            time.sleep(0.5)

        started = time.monotonic()
        with patch("urllib.request.urlopen", side_effect=ignore_socket_timeout):
            with self.assertRaisesRegex(OpenRouterError, "wall-clock deadline") as raised:
                port.respond(module="companion", message="你好", context=context())
        elapsed = time.monotonic() - started

        self.assertEqual(raised.exception.status_code, 408)
        self.assertLess(elapsed, 0.25)
        events = port.consume_observability()
        self.assertEqual(events[0]["error_status"], 408)
        self.assertLess(events[0]["latency_ms"], 250)

    def test_zero_provider_latency_preference_omits_request_hint(self) -> None:
        port = StubOpenRouter([tool_response("life_query_today_plan")])
        port._config = OpenRouterConfig(
            base_url="https://openrouter.ai/api/v1",
            api_key="test-key",
            models=("openrouter/free", "free/fallback"),
            preferred_max_latency_p90=0,
        )
        port.decide(module="life", message="我今天的计划是什么", context=context())
        self.assertNotIn("preferred_max_latency", port.requests[0]["provider"])

    def test_completed_email_tool_is_removed_and_observed_draft_is_returned(self) -> None:
        port = StubOpenRouter([tool_response("work_no_tool")])
        decision = port.decide(
            module="work",
            message="帮我写一封课程咨询邮件",
            context={
                "tools": [
                    {
                        "name": "work_draft_email",
                        "description": "生成邮件草稿",
                        "parameters": {"type": "object"},
                    },
                    {
                        "name": "work_no_tool",
                        "description": "无需继续调用工具",
                        "parameters": {"type": "object"},
                    },
                ],
                "observations": [
                    {
                        "tool_name": "work_draft_email",
                        "status": "succeeded",
                        "response": "邮件草稿：\n\n您好，请问可以选 Project 课吗？",
                    }
                ],
            },
        )
        self.assertEqual(
            decision.response,
            "邮件草稿：\n\n您好，请问可以选 Project 课吗？",
        )
        self.assertEqual(len(port.requests), 0)

    def test_repeatable_attachment_tool_remains_for_next_attachment(self) -> None:
        port = StubOpenRouter(
            [tool_response("work_extract_attached_document", '{"attachment_index":2}')]
        )
        decision = port.decide(
            module="work",
            message="根据两个附件做一个PPT",
            context={
                "tools": [
                    {
                        "name": "work_extract_attached_document",
                        "description": "每次独立提取一个附件",
                        "parameters": {
                            "type": "object",
                            "required": ["attachment_index"],
                            "properties": {
                                "attachment_index": {
                                    "type": "integer",
                                    "minimum": 1,
                                    "maximum": 2,
                                }
                            },
                        },
                        "repeatable": True,
                        "identity_fields": ["attachment_index"],
                    },
                    {
                        "name": "work_no_tool",
                        "description": "无需继续调用工具",
                        "parameters": {"type": "object"},
                    },
                ],
                "observations": [
                    {
                        "tool_name": "work_extract_attached_document",
                        "arguments": {"attachment_index": 1},
                        "status": "succeeded",
                    }
                ],
            },
        )
        self.assertEqual(decision.tool_name, "work_extract_attached_document")
        self.assertEqual(decision.tool_arguments, {"attachment_index": 2})
        offered = port.requests[0]["tools"][0]["function"]["parameters"]
        self.assertEqual(
            offered["properties"]["attachment_index"]["enum"],
            [2],
        )

    def test_pending_ppt_goal_forces_extract_then_generate(self) -> None:
        port = StubOpenRouter(
            [
                tool_response(
                    "work_extract_attached_document",
                    '{"attachment_index":1,"round_start":1}',
                ),
                tool_response("work_generate_pptx"),
            ]
        )
        tools = [
            {
                "name": "work_extract_attached_document",
                "description": "提取附件",
                "parameters": {
                    "type": "object",
                    "required": ["attachment_index", "round_start"],
                    "properties": {
                        "attachment_index": {"type": "integer", "minimum": 1, "maximum": 1},
                        "round_start": {"type": "integer", "minimum": 1, "maximum": 100},
                    },
                },
                "repeatable": True,
                "identity_fields": ["attachment_index", "round_start"],
            },
            {
                "name": "work_generate_pptx",
                "description": "生成 PPTX",
                "compose_arguments": True,
                "parameters": {"type": "object", "properties": {}},
            },
            {
                "name": "work_no_tool",
                "description": "结束",
                "parameters": {"type": "object", "properties": {}},
            },
        ]
        task_contract = {
            "artifact_types": ["pptx"],
            "source_required": True,
        }
        first = port.decide(
            module="work",
            message="确认",
            context={
                "tools": tools,
                "task_contract": task_contract,
                "artifact_validation": {},
                "source_coverage": {"coverage_ratio": 0.0, "truncated": True},
            },
        )
        self.assertEqual(first.tool_name, "work_extract_attached_document")
        self.assertEqual(
            port.requests[0]["tool_choice"]["function"]["name"],
            "work_extract_attached_document",
        )
        self.assertEqual(len(port.requests[0]["tools"]), 1)

        second = port.decide(
            module="work",
            message="确认",
            context={
                "tools": tools,
                "task_contract": task_contract,
                "artifact_validation": {},
                "source_coverage": {"coverage_ratio": 1.0, "truncated": False},
                "observations": [
                    {
                        "tool_name": "work_extract_attached_document",
                        "arguments": {"attachment_index": 1, "round_start": 1},
                        "status": "succeeded",
                    }
                ],
            },
        )
        self.assertEqual(second.tool_name, "work_generate_pptx")
        self.assertTrue(second.requires_argument_composition)
        self.assertEqual(
            port.requests[1]["tool_choice"]["function"]["name"],
            "work_generate_pptx",
        )
        self.assertEqual(len(port.requests[1]["tools"]), 1)

    def test_goal_assessment_marks_completed_email_draft_terminal(self) -> None:
        port = StubOpenRouter(
            [
                {
                    "choices": [
                        {
                            "message": {
                                "content": (
                                    '{"status":"completed","reason":"完整邮件草稿已经生成"}'
                                )
                            }
                        }
                    ]
                }
            ]
        )
        assessment = port.assess(
            module="work",
            message="帮我写一封课程咨询邮件",
            context={
                "agent_plan": {"success_criteria": "生成完整且礼貌的邮件草稿"},
                "observations": [
                    {
                        "tool_name": "work_draft_email",
                        "status": "succeeded",
                        "response": "邮件草稿已生成",
                        "data": {
                            "output": {
                                "subject": "课程咨询",
                                "body": "您好……",
                                "send_status": "draft_only",
                                "quality_report": {"passed": True},
                            }
                        },
                    }
                ],
            },
        )
        self.assertEqual(assessment.status, "completed")

    def test_goal_assessment_blocks_email_without_quality_evidence(self) -> None:
        port = StubOpenRouter([])
        assessment = port.assess(
            module="work",
            message="帮我写一封英文课程咨询邮件",
            context={
                "observations": [
                    {
                        "tool_name": "work_draft_email",
                        "status": "succeeded",
                        "data": {
                            "output": {
                                "body": "您好，Could you help me?",
                                "send_status": "draft_only",
                            }
                        },
                    }
                ]
            },
        )
        self.assertEqual(assessment.status, "blocked")
        self.assertEqual(port.requests, [])

    def test_rejects_parallel_or_missing_tool_call(self) -> None:
        invalid = {
            "choices": [
                {
                    "message": {
                        "tool_calls": [
                            {
                                "function": {
                                    "name": "life_no_tool",
                                    "arguments": "{}",
                                }
                            },
                            {
                                "function": {
                                    "name": "life_query_today_plan",
                                    "arguments": "{}",
                                }
                            },
                        ]
                    }
                }
            ]
        }
        port = StubOpenRouter(
            [
                invalid,
                invalid,
            ]
        )
        with self.assertRaises(OpenRouterError):
            port.decide(module="life", message="你好", context=context())

    def test_rejects_malformed_tool_arguments(self) -> None:
        port = StubOpenRouter(
            [
                tool_response("life_query_today_plan", "{not-json"),
                tool_response("life_query_today_plan", "{still-not-json"),
            ]
        )
        with self.assertRaises(OpenRouterError):
            port.decide(
                module="life",
                message="我今天的计划是什么",
                context=context(),
            )

    def test_falls_back_when_first_model_ignores_required_tool_choice(self) -> None:
        port = StubOpenRouter(
            [
                {"choices": [{"message": {"content": "直接回答了用户"}}]},
                tool_response("life_query_today_plan", "{}"),
            ]
        )
        decision = port.decide(
            module="life",
            message="我今天的计划是什么",
            context=context(),
        )
        self.assertEqual(decision.tool_name, "life_query_today_plan")
        self.assertEqual(
            [request["model"] for request in port.requests],
            ["openrouter/free", "free/fallback"],
        )

    def test_falls_back_when_model_embeds_known_tool_call_in_content(self) -> None:
        port = StubOpenRouter(
            [
                {
                    "choices": [
                        {
                            "message": {
                                "content": (
                                    "我来处理。\n<｜DSML｜toolcalls>\n"
                                    '<｜DSML｜invoke name="work_translate_attached_pdf">'
                                    "</｜DSML｜invoke>"
                                )
                            }
                        }
                    ]
                },
                tool_response(
                    "work_translate_attached_pdf",
                    '{"target_language":"中文"}',
                ),
            ]
        )
        decision = port.decide(
            module="work",
            message="请处理",
            context={
                "tools": [
                    {
                        "name": "work_translate_attached_pdf",
                        "description": "翻译附件 PDF",
                        "parameters": {
                            "type": "object",
                            "properties": {
                                "target_language": {"type": "string"},
                            },
                        },
                    }
                ]
            },
        )
        self.assertEqual(decision.tool_name, "work_translate_attached_pdf")
        self.assertEqual(decision.tool_arguments, {"target_language": "中文"})
        self.assertEqual(
            [request["model"] for request in port.requests],
            ["openrouter/free", "free/fallback"],
        )

    def test_attached_document_action_cannot_finish_as_direct_text(self) -> None:
        port = StubOpenRouter(
            [
                {"choices": [{"message": {"content": "我来帮你翻译这份文件。"}}]},
                tool_response(
                    "work_translate_attached_pdf",
                    '{"target_language":"中文"}',
                ),
            ]
        )
        decision = port.decide(
            module="work",
            message=("帮我翻译\n<!--ai-document:doc-1|CS5494-week1.pdf-->"),
            context={
                "tools": [
                    {
                        "name": "work_translate_attached_pdf",
                        "description": "翻译附件 PDF",
                        "parameters": {
                            "type": "object",
                            "properties": {
                                "target_language": {"type": "string"},
                            },
                        },
                    }
                ]
            },
        )
        self.assertEqual(decision.tool_name, "work_translate_attached_pdf")
        self.assertEqual(len(port.requests), 2)

    def test_falls_back_when_first_model_rejects_tool_call_options(self) -> None:
        port = StubOpenRouter(
            [
                OpenRouterError(
                    "model does not support required tool choice",
                    status_code=422,
                ),
                tool_response("life_query_today_plan", "{}"),
            ]
        )
        decision = port.decide(
            module="life",
            message="我今天的计划是什么",
            context=context(),
        )
        self.assertEqual(decision.tool_name, "life_query_today_plan")
        self.assertEqual(
            [request["model"] for request in port.requests],
            ["openrouter/free", "free/fallback"],
        )


if __name__ == "__main__":
    unittest.main()
