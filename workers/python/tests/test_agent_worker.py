from __future__ import annotations

import io
import json
import unittest
from typing import Any, Mapping

from langgraph.checkpoint.memory import InMemorySaver

from ai_companion_worker.agent_gateway_client import ToolGatewayHTTPError
from ai_companion_worker.agent_governance import GRAPH_NAME, GRAPH_VERSION
from ai_companion_worker.agent_runtime import (
    AgentAssessment,
    ModelDecision,
    ModuleKey,
    ToolOutcome,
    ToolPreparation,
)
from ai_companion_worker.agent_worker import (
    _error_envelope,
    _governance_output,
    _ready_envelope,
    _requires_tool_definitions,
    _serve_requests,
    execute_run,
)
from ai_companion_worker.openrouter_decision import OpenRouterError


class RecordingDecisions:
    def __init__(self, decision: ModelDecision) -> None:
        self.decision = decision
        self.contexts: list[dict[str, Any]] = []

    def decide(
        self,
        *,
        module: ModuleKey,
        message: str,
        context: Mapping[str, Any],
    ) -> ModelDecision:
        del module, message
        self.contexts.append(dict(context))
        observations = context.get("observations")
        if self.decision.tool_name and isinstance(observations, list) and observations:
            latest = observations[-1]
            response = (
                str(latest.get("response") or "工具结果已返回。")
                if isinstance(latest, dict)
                else "工具结果已返回。"
            )
            return ModelDecision(intent="life_no_tool", response=response)
        return self.decision

    def assess(
        self,
        *,
        module: ModuleKey,
        message: str,
        context: Mapping[str, Any],
    ) -> AgentAssessment:
        del module, message, context
        return AgentAssessment(status="completed", reason="工具结果已满足目标")


class StubTools:
    def __init__(
        self,
        preparation: ToolPreparation,
        observations: list[ToolOutcome] | None = None,
    ) -> None:
        self.preparation = preparation
        self.observations = observations or []
        self.commits = 0

    def prepare(self, **_values: Any) -> ToolPreparation:
        return self.preparation

    def commit(self, **_values: Any) -> ToolOutcome:
        self.commits += 1
        return ToolOutcome(response="已提交。")

    def observe(self, **_values: Any) -> ToolOutcome:
        if not self.observations:
            raise AssertionError("unexpected observation")
        return self.observations.pop(0)


def run_payload(run_id: str) -> dict[str, Any]:
    return {
        "id": run_id,
        "user_id": "00000000-0000-0000-0000-000000000001",
        "conversation_id": "00000000-0000-0000-0000-000000000002",
        "character_id": "00000000-0000-0000-0000-000000000003",
        "module": "life",
        "input": {
            "text": "我今天的计划是什么",
            "context": {"timezone": "Asia/Shanghai"},
        },
    }


def tool_definition(name: str) -> dict[str, Any]:
    return {
        "name": name,
        "description": "测试工具",
        "parameters": {"type": "object"},
    }


class AgentWorkerTest(unittest.TestCase):
    def test_governance_output_preserves_non_sensitive_model_timeout(self) -> None:
        output = _governance_output(
            {
                "model_events": [
                    {
                        "kind": "model_call",
                        "role": "router",
                        "status": "succeeded",
                        "timeout_ms": 15000,
                    }
                ]
            }
        )

        self.assertEqual(output["model"]["calls"][0]["timeout_ms"], 15000)

    def test_daemon_ready_envelope_pins_protocol_and_graph(self) -> None:
        self.assertEqual(
            _ready_envelope(),
            {
                "type": "ready",
                "protocol_version": "agent-runtime-jsonl-v1",
                "graph_name": GRAPH_NAME,
                "graph_version": GRAPH_VERSION,
            },
        )

    def test_gateway_http_error_envelope_preserves_retryability(self) -> None:
        retryable = _error_envelope(
            ToolGatewayHTTPError(
                "gateway failed",
                status_code=429,
                retry_after="17",
            )
        )
        permanent = _error_envelope(ToolGatewayHTTPError("gateway rejected input", status_code=400))

        self.assertEqual(retryable["code"], "tool_gateway")
        self.assertTrue(retryable["retryable"])
        self.assertEqual(retryable["provider_status"], 429)
        self.assertEqual(retryable["retry_after"], "17")
        self.assertEqual(permanent["code"], "tool_gateway")
        self.assertFalse(permanent["retryable"])

    def test_model_error_envelope_preserves_retry_advice(self) -> None:
        error = _error_envelope(
            OpenRouterError(
                "rate limited",
                status_code=429,
                retry_after="23",
            )
        )

        self.assertEqual(error["code"], "model_unavailable")
        self.assertTrue(error["retryable"])
        self.assertEqual(error["provider_status"], 429)
        self.assertEqual(error["retry_after"], "23")

    def test_completed_run_returns_serializable_worker_contract(self) -> None:
        definitions = [
            {
                "name": "life_query_today_plan",
                "description": "查询真实计划",
                "parameters": {"type": "object"},
            }
        ]
        decisions = RecordingDecisions(
            ModelDecision(
                intent="plan_query",
                tool_name="life_query_today_plan",
            )
        )
        result = execute_run(
            run_payload("run-completed"),
            checkpointer=InMemorySaver(),
            decisions=decisions,
            tools=StubTools(
                ToolPreparation(
                    status="completed",
                    tool_name="life_query_today_plan",
                    response="今天没有计划。",
                )
            ),
            definitions=definitions,
        )
        self.assertEqual(result["status"], "completed")
        self.assertEqual(result["output"]["response"], "今天没有计划。")
        self.assertEqual(result["output"]["assessment"], {})
        self.assertEqual(result["output"]["execution_mode"], "single_action")
        self.assertEqual(decisions.contexts[0]["tools"], definitions)
        self.assertEqual(result["output"]["graph"]["version"], GRAPH_VERSION)
        self.assertEqual(
            len(result["output"]["graph"]["tool_catalog_fingerprint"]),
            64,
        )
        self.assertIn("limits", result["output"]["budget"])
        self.assertTrue(result["output"]["model"]["calls"])
        self.assertTrue(result["output"]["observability"]["node_trace"])
        nodes = [item["node"] for item in result["output"]["observability"]["node_trace"]]
        self.assertNotIn("plan", nodes)
        self.assertNotIn("assess_progress", nodes)

    def test_confirmation_interrupt_pauses_durable_run(self) -> None:
        decisions = RecordingDecisions(
            ModelDecision(
                intent="ledger_write",
                tool_name="life_prepare_ledger_entry",
                tool_arguments={"text": "午饭50元"},
            )
        )
        result = execute_run(
            run_payload("run-waiting"),
            checkpointer=InMemorySaver(),
            decisions=decisions,
            tools=StubTools(
                ToolPreparation(
                    status="requires_confirmation",
                    tool_name="life_prepare_ledger_entry",
                    risk_level="medium",
                    summary="餐饮支出 50 元",
                    confirmation_token="signed-token",
                )
            ),
            definitions=[tool_definition("life_prepare_ledger_entry")],
        )
        self.assertEqual(result["status"], "waiting_approval")
        interrupts = result["output"]["interrupts"]
        self.assertEqual(len(interrupts), 1)
        self.assertEqual(interrupts[0]["confirmation_token"], "signed-token")

    def test_resume_uses_existing_checkpoint_and_commits_once(self) -> None:
        checkpointer = InMemorySaver()
        decisions = RecordingDecisions(
            ModelDecision(
                intent="ledger_write",
                tool_name="life_prepare_ledger_entry",
                tool_arguments={"text": "午饭50元"},
            )
        )
        tools = StubTools(
            ToolPreparation(
                status="requires_confirmation",
                tool_name="life_prepare_ledger_entry",
                risk_level="medium",
                summary="餐饮支出 50 元",
                confirmation_token="signed-token",
            )
        )
        run = run_payload("run-resume")
        first = execute_run(
            run,
            checkpointer=checkpointer,
            decisions=decisions,
            tools=tools,
            definitions=[tool_definition("life_prepare_ledger_entry")],
        )
        self.assertEqual(first["status"], "waiting_approval")
        run["resume_resolution"] = {"approved": True}
        final = execute_run(
            run,
            checkpointer=checkpointer,
            decisions=decisions,
            tools=tools,
            definitions=[tool_definition("life_prepare_ledger_entry")],
        )
        self.assertEqual(final["status"], "completed")
        self.assertEqual(final["output"]["response"], "已提交。")
        self.assertEqual(tools.commits, 1)

    def test_resume_reuses_frozen_catalog_when_live_catalog_changes(self) -> None:
        checkpointer = InMemorySaver()
        decisions = RecordingDecisions(
            ModelDecision(
                intent="ledger_write",
                tool_name="life_prepare_ledger_entry",
                tool_arguments={"text": "午饭50元"},
            )
        )
        tools = StubTools(
            ToolPreparation(
                status="requires_confirmation",
                tool_name="life_prepare_ledger_entry",
                risk_level="medium",
                summary="餐饮支出 50 元",
                confirmation_token="signed-token",
            )
        )
        run = run_payload("run-catalog-version")
        definitions = [tool_definition("life_prepare_ledger_entry")]
        first = execute_run(
            run,
            checkpointer=checkpointer,
            decisions=decisions,
            tools=tools,
            definitions=definitions,
        )
        self.assertEqual(first["status"], "waiting_approval")
        omitted_catalog_replay = execute_run(
            run,
            checkpointer=checkpointer,
            decisions=decisions,
            tools=tools,
            definitions=[],
        )
        self.assertEqual(omitted_catalog_replay["status"], "waiting_approval")
        self.assertEqual(
            omitted_catalog_replay["output"]["graph"]["tool_catalog_fingerprint"],
            first["output"]["graph"]["tool_catalog_fingerprint"],
        )
        run["resume_resolution"] = {"approved": True}
        changed = [dict(definitions[0])]
        changed[0]["description"] = "已改变的工具契约"
        final = execute_run(
            run,
            checkpointer=checkpointer,
            decisions=decisions,
            tools=tools,
            definitions=changed,
        )
        self.assertEqual(final["status"], "completed")
        self.assertEqual(tools.commits, 1)
        self.assertEqual(
            final["output"]["graph"]["tool_catalog_fingerprint"],
            first["output"]["graph"]["tool_catalog_fingerprint"],
        )

    def test_waiting_tool_resumes_same_checkpoint_once(self) -> None:
        checkpointer = InMemorySaver()
        decisions = RecordingDecisions(
            ModelDecision(
                intent="query_async",
                tool_name="life_query_today_plan",
            )
        )
        tools = StubTools(
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
        run = run_payload("run-tool-wait")
        first = execute_run(
            run,
            checkpointer=checkpointer,
            decisions=decisions,
            tools=tools,
            definitions=[tool_definition("life_query_today_plan")],
        )
        self.assertEqual(first["status"], "waiting_tool")
        self.assertEqual(len(tools.observations), 1)
        interrupt = first["output"]["interrupts"][0]
        run["resume_resolution"] = {
            "type": "tool_poll",
            "task_id": "task-1",
            "interrupt_id": interrupt["interrupt_id"],
        }
        final = execute_run(
            run,
            checkpointer=checkpointer,
            decisions=decisions,
            tools=tools,
            definitions=[tool_definition("life_query_today_plan")],
        )
        self.assertEqual(final["status"], "completed")
        self.assertEqual(final["output"]["response"], "任务完成。")
        self.assertEqual(final["output"]["recovery"]["tool_resumes"], 1)

    def test_resume_reuses_checkpointed_tool_definitions(self) -> None:
        self.assertTrue(_requires_tool_definitions(run_payload("run-initial")))
        resumed = run_payload("run-resumed")
        resumed["resume_resolution"] = {
            "type": "tool_poll",
            "task_id": "task-1",
        }
        self.assertFalse(_requires_tool_definitions(resumed))

    def test_daemon_protocol_isolates_one_failed_request(self) -> None:
        source = io.BytesIO(b'{"id":"one"}\n{"id":"fail"}\n{"id":"three"}\n')
        target = io.BytesIO()
        calls: list[str] = []

        def handle(run: Mapping[str, Any]) -> dict[str, Any]:
            run_id = str(run["id"])
            calls.append(run_id)
            if run_id == "fail":
                raise ValueError("request failed")
            return {"status": "completed", "output": {"response": run_id}}

        _serve_requests(source, target, handle)

        envelopes = [json.loads(line) for line in target.getvalue().splitlines()]
        self.assertEqual(calls, ["one", "fail", "three"])
        self.assertTrue(envelopes[0]["ok"])
        self.assertFalse(envelopes[1]["ok"])
        self.assertEqual(envelopes[1]["error"]["message"], "request failed")
        self.assertTrue(envelopes[2]["ok"])
        self.assertEqual(envelopes[2]["result"]["output"]["response"], "three")

    def test_daemon_marks_checkpoint_connection_failure_for_recycle(self) -> None:
        class FakePsycopgError(RuntimeError):
            pass

        FakePsycopgError.__module__ = "psycopg.errors"
        source = io.BytesIO(b'{"id":"database-error"}\n')
        target = io.BytesIO()

        def handle(_run: Mapping[str, Any]) -> dict[str, Any]:
            raise FakePsycopgError("connection closed")

        _serve_requests(source, target, handle)

        envelope = json.loads(target.getvalue())
        self.assertFalse(envelope["ok"])
        self.assertTrue(envelope["error"]["recycle"])

    def test_rejects_graph_version_mismatch_before_execution(self) -> None:
        run = run_payload("run-old-graph")
        run["graph_name"] = GRAPH_NAME
        run["graph_version"] = "2.0.0"
        with self.assertRaisesRegex(ValueError, "graph_version"):
            execute_run(
                run,
                checkpointer=InMemorySaver(),
                decisions=RecordingDecisions(ModelDecision(intent="chat", response="unused")),
                tools=StubTools(
                    ToolPreparation(
                        status="completed",
                        tool_name="life_query_today_plan",
                        response="unused",
                    )
                ),
                definitions=[],
            )


if __name__ == "__main__":
    unittest.main()
