from __future__ import annotations

import unittest
from typing import Any

from ai_companion_worker.langfuse_observability import (
    LangfuseSettings,
    LangfuseTelemetry,
)


class FakeObservation:
    def __init__(self, values: dict[str, Any]) -> None:
        self.values = values
        self.updates: list[dict[str, Any]] = []
        self.children: list[FakeObservation] = []
        self.scores: list[dict[str, Any]] = []
        self.ended = False

    def start_observation(self, **values: Any) -> FakeObservation:
        child = FakeObservation(values)
        self.children.append(child)
        return child

    def update(self, **values: Any) -> FakeObservation:
        self.updates.append(values)
        return self

    def score(self, **values: Any) -> None:
        self.scores.append(values)

    def end(self) -> FakeObservation:
        self.ended = True
        return self


class FakeClient:
    def __init__(self, *, fail_start: bool = False) -> None:
        self.fail_start = fail_start
        self.roots: list[FakeObservation] = []
        self.flushed = False
        self.stopped = False

    def create_trace_id(self, *, seed: str) -> str:
        self.seed = seed
        return "a" * 32

    def start_observation(self, **values: Any) -> FakeObservation:
        if self.fail_start:
            raise RuntimeError("collector unavailable")
        root = FakeObservation(values)
        self.roots.append(root)
        return root

    def flush(self) -> None:
        self.flushed = True

    def shutdown(self) -> None:
        self.stopped = True


def run_payload() -> dict[str, Any]:
    return {
        "id": "run-123",
        "user_id": "private-user-id",
        "conversation_id": "private-conversation-id",
        "character_id": "private-character-id",
        "module": "work",
        "graph_name": "ai-companion-agent",
        "graph_version": "2026-09",
        "agent_definition_key": "work-default",
        "agent_definition_version_id": "agent-version-7",
        "agent_definition_version": 7,
        "agent_definition_fingerprint": "b" * 64,
        "model_profile_key": "production-default",
        "model_profile_version_id": "model-version-4",
        "model_profile_config_version": "2026-09-models",
        "input": {
            "text": "给 alice@example.com 发送报告，Bearer private-token-123456789",
            "context": {"timezone": "Asia/Shanghai", "api_key": "private"},
        },
    }


def runtime_result() -> dict[str, Any]:
    return {
        "outcome": "completed",
        "intent": "work_generate_document",
        "response": "报告已生成",
        "steps": 4,
        "execution_mode": "planned",
        "response_validation": {"passed": True},
        "budget_usage": {
            "model_calls": 1,
            "prompt_tokens": 120,
            "completion_tokens": 30,
            "cost_micros": 750,
        },
        "model_events": [{"status": "succeeded"}],
        "repair_history": [],
        "node_trace": [
            {"node": "supervisor", "status": "succeeded", "duration_ms": 2},
            {"node": "execute_tool", "status": "succeeded", "duration_ms": 12},
            {"node": "validate_response", "status": "succeeded", "duration_ms": 1},
        ],
    }


class LangfuseSettingsTests(unittest.TestCase):
    def test_disabled_is_the_default(self) -> None:
        settings = LangfuseSettings.from_env({})

        self.assertFalse(settings.enabled)
        self.assertFalse(settings.capture_content)
        self.assertEqual(settings.base_url, "https://cloud.langfuse.com")

    def test_enabled_requires_both_project_keys(self) -> None:
        with self.assertRaisesRegex(ValueError, "LANGFUSE_PUBLIC_KEY"):
            LangfuseSettings.from_env({"LANGFUSE_ENABLED": "true"})

    def test_limits_are_validated(self) -> None:
        environment = {
            "LANGFUSE_ENABLED": "true",
            "LANGFUSE_PUBLIC_KEY": "pk-test",
            "LANGFUSE_SECRET_KEY": "sk-test",
            "LANGFUSE_SAMPLE_RATE": "1.5",
        }
        with self.assertRaisesRegex(ValueError, "LANGFUSE_SAMPLE_RATE"):
            LangfuseSettings.from_env(environment)

    def test_enabled_rejects_credentials_in_base_url(self) -> None:
        environment = {
            "LANGFUSE_ENABLED": "true",
            "LANGFUSE_PUBLIC_KEY": "pk-test",
            "LANGFUSE_SECRET_KEY": "sk-test",
            "LANGFUSE_BASE_URL": "https://user:secret@langfuse.example.com",
        }
        with self.assertRaisesRegex(ValueError, "without credentials"):
            LangfuseSettings.from_env(environment)


class LangfuseTelemetryTests(unittest.TestCase):
    def settings(self, *, capture_content: bool = False) -> LangfuseSettings:
        return LangfuseSettings(
            enabled=True,
            public_key="pk-test",
            secret_key="sk-test",
            capture_content=capture_content,
            environment="test",
            release="test-release",
        )

    def test_exports_agent_generation_nodes_scores_and_cost_without_content(self) -> None:
        client = FakeClient()
        telemetry = LangfuseTelemetry(self.settings(), client=client)
        run = run_payload()

        with telemetry.observe_run(run) as observed_run:
            with telemetry.observe_generation(
                role="composer",
                requested_model="openai/gpt-test",
                payload={
                    "messages": [{"role": "user", "content": "private model prompt"}],
                    "temperature": 0,
                    "max_tokens": 512,
                },
                timeout_seconds=8.5,
            ) as generation:
                generation.succeed(
                    {
                        "model": "openai/gpt-test-2026",
                        "choices": [
                            {"message": {"role": "assistant", "content": "private output"}}
                        ],
                    },
                    {
                        "role": "composer",
                        "status": "succeeded",
                        "provider": "openrouter",
                        "requested_model": "openai/gpt-test",
                        "returned_model": "openai/gpt-test-2026",
                        "prompt_tokens": 120,
                        "completion_tokens": 30,
                        "cached_tokens": 10,
                        "reasoning_tokens": 4,
                        "cost_micros": 750,
                        "latency_ms": 400,
                    },
                )
            observed_run.finish(runtime_result(), status="completed")
            reference = observed_run.reference()

        self.assertEqual(client.seed, "agent-run:run-123")
        self.assertEqual(reference["trace_id"], "a" * 32)
        self.assertTrue(reference["initialized"])
        self.assertFalse(reference["capture_content"])
        root = client.roots[0]
        self.assertEqual(root.values["as_type"], "agent")
        self.assertEqual(root.values["version"], "agent-v7")
        self.assertNotIn("user_message", root.values["input"])
        self.assertNotIn("private-user-id", str(root.values["metadata"]))
        self.assertIn("sha256:", root.values["metadata"]["user_ref"])

        generation = next(item for item in root.children if item.values["as_type"] == "generation")
        self.assertEqual(generation.values["input"], {"message_count": 1})
        self.assertNotIn("private model prompt", str(generation.values))
        self.assertEqual(generation.updates[0]["usage_details"]["total"], 150)
        self.assertEqual(generation.updates[0]["cost_details"]["total"], 0.00075)
        self.assertNotIn("private output", str(generation.updates))
        self.assertTrue(generation.ended)

        node_types = [item.values["as_type"] for item in root.children[1:]]
        self.assertEqual(node_types, ["span", "tool", "guardrail"])
        scores = {item["name"]: item["value"] for item in root.scores}
        self.assertEqual(scores["agent_completed"], True)
        self.assertEqual(scores["response_contract_passed"], True)
        self.assertEqual(scores["agent_steps"], 4.0)
        self.assertEqual(scores["model_cost_usd"], 0.00075)
        self.assertTrue(root.ended)

    def test_content_capture_is_opt_in_and_redacts_common_secrets(self) -> None:
        client = FakeClient()
        telemetry = LangfuseTelemetry(self.settings(capture_content=True), client=client)

        with telemetry.observe_run(run_payload()) as observed_run:
            observed_run.finish(runtime_result(), status="completed")

        captured = str(client.roots[0].values["input"])
        self.assertIn("user_message", captured)
        self.assertNotIn("alice@example.com", captured)
        self.assertNotIn("private-token-123456789", captured)
        self.assertIn("REDACTED", captured)

    def test_joins_propagated_otel_trace_context(self) -> None:
        client = FakeClient()
        telemetry = LangfuseTelemetry(self.settings(), client=client)
        run = run_payload()
        run["otel_traceparent"] = (
            "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
        )

        with telemetry.observe_run(run) as observed_run:
            reference = observed_run.reference()

        self.assertEqual(reference["trace_id"], "4bf92f3577b34da6a3ce929d0e0e4736")
        self.assertEqual(
            client.roots[0].values["trace_context"],
            {
                "trace_id": "4bf92f3577b34da6a3ce929d0e0e4736",
                "parent_span_id": "00f067aa0ba902b7",
            },
        )
        self.assertFalse(hasattr(client, "seed"))

    def test_export_setup_failure_is_fail_open(self) -> None:
        client = FakeClient(fail_start=True)
        telemetry = LangfuseTelemetry(self.settings(), client=client)

        with telemetry.observe_run(run_payload()) as observed_run:
            observed_run.finish(runtime_result(), status="completed")
            reference = observed_run.reference()

        self.assertEqual(reference["enabled"], True)
        self.assertEqual(reference["initialized"], False)

    def test_non_terminal_resume_phase_does_not_publish_final_scores(self) -> None:
        client = FakeClient()
        telemetry = LangfuseTelemetry(self.settings(), client=client)

        with telemetry.observe_run(run_payload()) as observed_run:
            observed_run.finish(runtime_result(), status="waiting_approval")

        self.assertEqual(client.roots[0].scores, [])

    def test_runtime_exception_is_preserved_and_trace_is_closed(self) -> None:
        client = FakeClient()
        telemetry = LangfuseTelemetry(self.settings(), client=client)

        with self.assertRaisesRegex(RuntimeError, "runtime failed"):
            with telemetry.observe_run(run_payload()):
                raise RuntimeError("runtime failed with Bearer private-token-123456789")

        root = client.roots[0]
        self.assertTrue(root.ended)
        self.assertEqual(root.updates[0]["level"], "ERROR")
        self.assertNotIn("private-token-123456789", root.updates[0]["status_message"])

    def test_flush_and_shutdown_are_forwarded(self) -> None:
        client = FakeClient()
        telemetry = LangfuseTelemetry(self.settings(), client=client)

        telemetry.flush()
        telemetry.shutdown()

        self.assertTrue(client.flushed)
        self.assertTrue(client.stopped)

    def test_client_factory_receives_bounded_async_export_configuration(self) -> None:
        captured: dict[str, Any] = {}
        client = FakeClient()

        def factory(**values: Any) -> FakeClient:
            captured.update(values)
            return client

        telemetry = LangfuseTelemetry(self.settings(), client_factory=factory)

        self.assertIsNotNone(telemetry)
        self.assertEqual(captured["flush_at"], 20)
        self.assertEqual(captured["flush_interval"], 5.0)
        self.assertEqual(captured["timeout"], 3)
        self.assertEqual(captured["sample_rate"], 1.0)
        masked = captured["mask"](data={"api_key": "secret", "email": "alice@example.com"})
        self.assertEqual(masked["api_key"], "[REDACTED]")
        self.assertEqual(masked["email"], "[REDACTED_EMAIL]")


if __name__ == "__main__":
    unittest.main()
