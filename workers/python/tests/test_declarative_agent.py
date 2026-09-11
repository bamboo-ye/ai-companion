from __future__ import annotations

import unittest
from typing import Any, Mapping

from langgraph.checkpoint.memory import InMemorySaver

from ai_companion_worker.agent_governance import BudgetPolicy
from ai_companion_worker.agent_runtime import (
    AgentRuntime,
    ModelDecision,
    ModuleKey,
    ToolOutcome,
    ToolPreparation,
    interrupt_payloads,
)
from ai_companion_worker.declarative_agent import (
    build_declarative_graph,
    compile_agent_definition,
)


class StudioDecisions:
    def __init__(
        self,
        *,
        routes: list[str] | None = None,
        compose_failures: int = 0,
    ) -> None:
        self.routes = routes or []
        self.nodes: list[str] = []
        self.tool_selections = 0
        self.compose_failures = compose_failures
        self.composer_calls = 0

    def model_manifest(self) -> dict[str, Any]:
        return {
            "provider": "test",
            "config_version": "studio-test-v1",
            "pinned": True,
            "roles": {},
        }

    def execute_declarative_node(
        self,
        *,
        module: ModuleKey,
        message: str,
        node_type: str,
        role: str,
        prompt_template: str,
        conditions: tuple[str, ...],
        context: Mapping[str, Any],
    ) -> Mapping[str, Any]:
        del module, message, role, context
        self.nodes.append(prompt_template)
        if node_type == "router":
            condition = self.routes.pop(0) if self.routes else conditions[0]
            return {"condition": condition}
        return {"response": "Studio 模型节点已完成。"}

    def decide(
        self,
        *,
        module: ModuleKey,
        message: str,
        context: Mapping[str, Any],
    ) -> ModelDecision:
        del module, message
        self.tool_selections += 1
        definitions = context.get("tools", [])
        name = str(definitions[0]["name"])
        return ModelDecision(intent=name, tool_name=name, tool_arguments={})

    def compose_arguments(
        self,
        *,
        module: ModuleKey,
        message: str,
        tool_name: str,
        context: Mapping[str, Any],
    ) -> Mapping[str, Any]:
        del module, message, tool_name, context
        self.composer_calls += 1
        if self.composer_calls <= self.compose_failures:
            raise RuntimeError("temporary composer failure")
        return {"query": "today"}


class StudioTools:
    def __init__(
        self,
        preparation: ToolPreparation,
        *,
        observations: list[ToolOutcome] | None = None,
    ) -> None:
        self.preparation = preparation
        self.observations = observations or []
        self.prepares = 0
        self.commits = 0

    def prepare(self, **_values: Any) -> ToolPreparation:
        self.prepares += 1
        return self.preparation

    def commit(self, **_values: Any) -> ToolOutcome:
        self.commits += 1
        return ToolOutcome(response="审批后已完成。", data={"written": True})

    def observe(self, **_values: Any) -> ToolOutcome:
        if not self.observations:
            raise AssertionError("unexpected observation")
        return self.observations.pop(0)


class FlakyPrepareTools(StudioTools):
    def prepare(self, **values: Any) -> ToolPreparation:
        self.prepares += 1
        if self.prepares == 1:
            raise RuntimeError("temporary gateway failure")
        return self.preparation


def definition(*, entry: str = "route") -> dict[str, Any]:
    return {
        "display_name": "Studio test",
        "description": "test",
        "modules": ["life"],
        "model_profile": "production-default",
        "entry_node": entry,
        "nodes": [
            {
                "key": "route",
                "type": "router",
                "model_role": "router",
                "prompt_template": "route prompt",
            },
            {
                "key": "answer",
                "type": "model",
                "model_role": "responder",
                "prompt_template": "answer prompt",
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


def tool_definition() -> dict[str, Any]:
    return {
        "display_name": "Studio tool test",
        "description": "test",
        "modules": ["life"],
        "model_profile": "production-default",
        "entry_node": "query",
        "nodes": [
            {"key": "query", "type": "tool", "tools": ["life_query_today_plan"]},
            {
                "key": "answer",
                "type": "model",
                "model_role": "responder",
                "prompt_template": "answer from the tool result",
            },
            {"key": "done", "type": "end"},
        ],
        "edges": [
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


def initial(run_id: str, *, tools: list[dict[str, Any]] | None = None) -> dict[str, Any]:
    return {
        "run_id": run_id,
        "user_id": "user-1",
        "conversation_id": "conversation-1",
        "character_id": "character-1",
        "module": "life",
        "user_message": "测试请求",
        "context": {"tools": tools or []},
    }


def runtime(
    graph_definition: Mapping[str, Any],
    decisions: StudioDecisions,
    tools: StudioTools,
) -> AgentRuntime:
    identity = {"key": "studio-test", "version_id": "version-1", "fingerprint": "a" * 64}
    graph = build_declarative_graph(
        definition=graph_definition,
        definition_identity=identity,
        checkpointer=InMemorySaver(),
        decisions=decisions,
        tools=tools,
        budget_policy=BudgetPolicy(max_actions=4, max_model_calls=8),
    )
    return AgentRuntime(
        graph,
        recursion_limit=128,
        checkpoint_identity={
            "agent_definition_key": identity["key"],
            "agent_definition_version_id": identity["version_id"],
            "agent_definition_fingerprint": identity["fingerprint"],
        },
    )


class DeclarativeAgentTest(unittest.TestCase):
    def test_compiles_and_executes_router_model_end_graph(self) -> None:
        decisions = StudioDecisions(routes=["respond"])
        agent = runtime(
            definition(),
            decisions,
            StudioTools(ToolPreparation(status="completed", tool_name="unused", response="unused")),
        )

        result = agent.start(initial("studio-model"))

        self.assertEqual(result["outcome"], "completed")
        self.assertEqual(result["response"], "Studio 模型节点已完成。")
        self.assertEqual(result["execution_mode"], "agent_studio")
        self.assertEqual(result["budget_usage"]["model_calls"], 2)
        self.assertEqual(result["budget_limits"]["max_model_calls"], 4)
        self.assertEqual(result["budget_limits"]["max_total_tokens"], 4096)
        trace_nodes = [item["node"] for item in result["node_trace"]]
        self.assertIn("studio__route", trace_nodes)
        self.assertIn("studio__answer", trace_nodes)

    def test_tool_approval_resumes_the_same_checkpoint_once(self) -> None:
        decisions = StudioDecisions()
        tools = StudioTools(
            ToolPreparation(
                status="requires_confirmation",
                tool_name="life_query_today_plan",
                risk_level="medium",
                summary="执行测试操作",
                confirmation_token="signed-token",
                normalized_arguments={},
            )
        )
        agent = runtime(tool_definition(), decisions, tools)
        catalog = [
            {
                "name": "life_query_today_plan",
                "description": "query",
                "parameters": {"type": "object", "additionalProperties": False},
            }
        ]

        waiting = agent.start(initial("studio-approval", tools=catalog))
        pending = interrupt_payloads(waiting)
        self.assertEqual(pending[0]["type"], "tool_approval")
        self.assertEqual(pending[0]["agent_definition_fingerprint"], "a" * 64)

        result = agent.resume(
            "studio-approval",
            {"approved": True, "interrupt_id": pending[0]["interrupt_id"]},
        )
        self.assertEqual(result["outcome"], "completed")
        self.assertEqual(tools.prepares, 1)
        self.assertEqual(tools.commits, 1)
        self.assertEqual(result["recovery"]["approval_resumes"], 1)

    def test_async_tool_wait_is_durable_and_observed_once(self) -> None:
        tools = StudioTools(
            ToolPreparation(
                status="completed",
                tool_name="life_query_today_plan",
                response="任务已入队。",
                data={"kind": "skill_run", "id": "task-1", "status": "queued"},
            ),
            observations=[
                ToolOutcome(
                    response="任务完成。",
                    data={"kind": "skill_run", "id": "task-1", "status": "succeeded"},
                )
            ],
        )
        agent = runtime(tool_definition(), StudioDecisions(), tools)
        catalog = [
            {
                "name": "life_query_today_plan",
                "description": "query",
                "parameters": {"type": "object", "additionalProperties": False},
            }
        ]

        waiting = agent.start(initial("studio-wait", tools=catalog))
        pending = interrupt_payloads(waiting)
        self.assertEqual(pending[0]["type"], "tool_wait")
        result = agent.resume(
            "studio-wait",
            {
                "type": "tool_poll",
                "task_id": "task-1",
                "interrupt_id": pending[0]["interrupt_id"],
            },
        )
        self.assertEqual(result["outcome"], "completed")
        self.assertEqual(result["recovery"]["tool_resumes"], 1)
        self.assertEqual(result["observations"][0]["status"], "succeeded")

    def test_prepare_retry_reuses_checkpointed_model_selection(self) -> None:
        decisions = StudioDecisions()
        tools = FlakyPrepareTools(
            ToolPreparation(
                status="completed",
                tool_name="life_query_today_plan",
                response="今天没有计划。",
            )
        )
        agent = runtime(tool_definition(), decisions, tools)
        catalog = [
            {
                "name": "life_query_today_plan",
                "description": "query",
                "parameters": {"type": "object", "additionalProperties": False},
            }
        ]
        run_input = initial("studio-prepare-retry", tools=catalog)

        with self.assertRaisesRegex(RuntimeError, "temporary gateway failure"):
            agent.start(run_input)
        result = agent.execute("studio-prepare-retry", initial=run_input)

        self.assertEqual(result["outcome"], "completed")
        self.assertEqual(decisions.tool_selections, 1)
        self.assertEqual(tools.prepares, 2)

    def test_composer_retry_reuses_checkpointed_tool_selection(self) -> None:
        decisions = StudioDecisions(compose_failures=1)
        tools = StudioTools(
            ToolPreparation(
                status="completed",
                tool_name="life_query_today_plan",
                response="今天没有计划。",
            )
        )
        agent = runtime(tool_definition(), decisions, tools)
        catalog = [
            {
                "name": "life_query_today_plan",
                "description": "query",
                "compose_arguments": True,
                "parameters": {
                    "type": "object",
                    "required": ["query"],
                    "properties": {"query": {"type": "string"}},
                    "additionalProperties": False,
                },
            }
        ]
        run_input = initial("studio-compose-retry", tools=catalog)

        with self.assertRaisesRegex(RuntimeError, "temporary composer failure"):
            agent.start(run_input)
        result = agent.execute("studio-compose-retry", initial=run_input)

        self.assertEqual(result["outcome"], "completed")
        self.assertEqual(decisions.tool_selections, 1)
        self.assertEqual(decisions.composer_calls, 2)
        self.assertEqual(tools.prepares, 1)

    def test_cycle_stops_at_the_persisted_step_budget(self) -> None:
        graph_definition = definition()
        graph_definition["nodes"].append(
            {
                "key": "loop",
                "type": "model",
                "model_role": "responder",
                "prompt_template": "loop prompt",
            }
        )
        graph_definition["edges"] = [
            {"from": "route", "to": "loop", "condition": "loop"},
            {"from": "route", "to": "done", "condition": "stop"},
            {"from": "loop", "to": "route"},
        ]
        graph_definition["nodes"] = [
            item for item in graph_definition["nodes"] if item["key"] != "answer"
        ]
        graph_definition["budget"]["max_steps"] = 2
        agent = runtime(
            graph_definition,
            StudioDecisions(routes=["loop", "loop"]),
            StudioTools(ToolPreparation(status="completed", tool_name="unused", response="unused")),
        )

        result = agent.start(initial("studio-cycle"))

        self.assertEqual(result["outcome"], "step_budget_exhausted")
        self.assertIn("步数预算", result["response"])
        self.assertEqual(result["steps"], 2)

    def test_compiler_rejects_duplicate_router_conditions(self) -> None:
        graph_definition = definition()
        graph_definition["edges"][1]["condition"] = "respond"
        with self.assertRaisesRegex(ValueError, "unique valid conditions"):
            compile_agent_definition(graph_definition)


if __name__ == "__main__":
    unittest.main()
