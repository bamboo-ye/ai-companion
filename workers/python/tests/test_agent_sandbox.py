from __future__ import annotations

import unittest
from typing import Any

from ai_companion_worker.agent_sandbox import run_sandbox


def definition(*, with_tool: bool = False) -> dict[str, Any]:
    if with_tool:
        return {
            "display_name": "Sandbox tool",
            "description": "Synthetic tool path",
            "modules": ["life"],
            "model_profile": "production-default",
            "entry_node": "route",
            "nodes": [
                {
                    "key": "route",
                    "type": "router",
                    "model_role": "router",
                    "prompt_template": "route",
                },
                {"key": "query", "type": "tool", "tools": ["life_query_today_plan"]},
                {
                    "key": "answer",
                    "type": "model",
                    "model_role": "responder",
                    "prompt_template": "answer",
                },
                {"key": "done", "type": "end"},
            ],
            "edges": [
                {"from": "route", "to": "query", "condition": "query"},
                {"from": "route", "to": "answer", "condition": "direct"},
                {"from": "query", "to": "answer"},
                {"from": "answer", "to": "done"},
            ],
            "budget": {
                "max_steps": 8,
                "max_model_calls": 4,
                "max_tool_calls": 1,
                "max_total_tokens": 4096,
                "timeout_ms": 60_000,
            },
        }
    return {
        "display_name": "Sandbox model",
        "description": "Synthetic model path",
        "modules": ["work"],
        "model_profile": "production-default",
        "entry_node": "route",
        "nodes": [
            {
                "key": "route",
                "type": "router",
                "model_role": "router",
                "prompt_template": "route",
            },
            {
                "key": "answer",
                "type": "model",
                "model_role": "responder",
                "prompt_template": "answer",
            },
            {"key": "done", "type": "end"},
        ],
        "edges": [
            {"from": "route", "to": "answer", "condition": "respond"},
            {"from": "route", "to": "done", "condition": "stop"},
            {"from": "answer", "to": "done"},
        ],
        "budget": {
            "max_steps": 8,
            "max_model_calls": 4,
            "max_tool_calls": 0,
            "max_total_tokens": 4096,
            "timeout_ms": 60_000,
        },
    }


def request(graph: dict[str, Any], scenario: dict[str, Any]) -> dict[str, Any]:
    return {
        "definition": graph,
        "identity": {
            "key": "sandbox-agent",
            "version_id": "draft",
            "fingerprint": "a" * 64,
        },
        "scenario": scenario,
    }


class AgentSandboxTest(unittest.TestCase):
    def test_runs_compiled_graph_with_synthetic_model_events(self) -> None:
        report = run_sandbox(
            request(
                definition(),
                {
                    "module": "work",
                    "user_message": "给我一个合成答案",
                    "routes": {
                        "route": {
                            "condition": "respond",
                            "prompt_tokens": 11,
                            "completion_tokens": 1,
                        }
                    },
                    "models": {
                        "answer": {
                            "response": "这是合成回答。",
                            "prompt_tokens": 17,
                            "completion_tokens": 6,
                        }
                    },
                },
            )
        )

        self.assertEqual(report["status"], "completed")
        self.assertEqual(report["outcome"], "completed")
        self.assertEqual(report["response"], "这是合成回答。")
        self.assertEqual(report["budget"]["usage"]["model_calls"], 2)
        self.assertEqual(report["budget"]["usage"]["prompt_tokens"], 28)
        self.assertEqual(report["budget"]["usage"]["completion_tokens"], 7)
        self.assertFalse(report["safety"]["side_effects"])
        self.assertEqual(report["safety"]["external_model_calls"], 0)
        self.assertTrue(all(call["provider"] == "synthetic" for call in report["model_calls"]))

    def test_stops_at_synthetic_approval_without_committing(self) -> None:
        report = run_sandbox(
            request(
                definition(with_tool=True),
                {
                    "module": "life",
                    "routes": {"route": {"condition": "query"}},
                    "tools": {
                        "query": {
                            "tool_name": "life_query_today_plan",
                            "requires_approval": True,
                            "risk_level": "medium",
                            "approval": "pause",
                            "response": "合成计划已读取。",
                        }
                    },
                },
            )
        )

        self.assertEqual(report["status"], "waiting_approval")
        self.assertEqual(report["interrupts"][0]["type"], "tool_approval")
        self.assertNotIn("confirmation_token", report["interrupts"][0])
        self.assertEqual([item["stage"] for item in report["tool_calls"]], ["prepare"])
        self.assertEqual(report["safety"]["external_tool_calls"], 0)

    def test_approves_and_resumes_synthetic_async_tool(self) -> None:
        report = run_sandbox(
            request(
                definition(with_tool=True),
                {
                    "module": "life",
                    "routes": {"route": {"condition": "query"}},
                    "models": {"answer": {"response": "已根据合成计划回答。"}},
                    "tools": {
                        "query": {
                            "requires_approval": True,
                            "approval": "approve",
                            "result_status": "waiting",
                            "wait_result": "completed",
                            "response": "合成异步计划已完成。",
                        }
                    },
                },
            )
        )

        self.assertEqual(report["status"], "completed")
        self.assertEqual(report["response"], "已根据合成计划回答。")
        self.assertEqual(
            [item["stage"] for item in report["tool_calls"]],
            ["prepare", "commit", "observe"],
        )
        self.assertEqual(report["recovery"]["approval_resumes"], 1)
        self.assertEqual(report["recovery"]["tool_resumes"], 1)

    def test_rejects_fixture_outside_compiled_tool_allowlist(self) -> None:
        with self.assertRaisesRegex(ValueError, "outside its compiled allowlist"):
            run_sandbox(
                request(
                    definition(with_tool=True),
                    {
                        "module": "life",
                        "tools": {"query": {"tool_name": "life_delete_everything"}},
                    },
                )
            )

    def test_enforces_synthetic_token_budget_before_dispatch(self) -> None:
        report = run_sandbox(
            request(
                definition(),
                {
                    "module": "work",
                    "routes": {
                        "route": {
                            "condition": "respond",
                            "prompt_tokens": 4096,
                            "completion_tokens": 1,
                        }
                    },
                },
            )
        )

        self.assertEqual(report["status"], "completed")
        self.assertEqual(report["outcome"], "model_budget_exhausted")
        self.assertEqual(report["budget"]["usage"]["model_calls"], 0)


if __name__ == "__main__":
    unittest.main()
