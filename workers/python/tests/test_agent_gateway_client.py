from __future__ import annotations

import io
import unittest
from typing import Any, Mapping
from unittest.mock import patch
from urllib.error import HTTPError

from ai_companion_worker.agent_gateway_client import (
    HTTPToolGateway,
    ToolGatewayHTTPError,
)


class RecordingGateway(HTTPToolGateway):
    def __init__(self, response: Mapping[str, Any]) -> None:
        super().__init__("http://api:8080", "service-token")
        self.response = dict(response)
        self.requests: list[tuple[str, dict[str, Any]]] = []

    def _post(self, path: str, payload: Mapping[str, Any]) -> dict[str, Any]:
        self.requests.append((path, dict(payload)))
        return dict(self.response)


class DefinitionGateway(HTTPToolGateway):
    def __init__(self) -> None:
        super().__init__("http://api:8080", "service-token")
        self.request: tuple[str, str] | None = None

    def _request(
        self,
        path: str,
        *,
        method: str,
        payload: Mapping[str, Any] | None = None,
    ) -> dict[str, Any]:
        del payload
        self.request = (path, method)
        return {
            "items": [
                {
                    "name": "life_query_today_plan",
                    "description": "查询真实今日计划",
                    "parameters": {"type": "object"},
                }
            ]
        }


class ObserveGateway(HTTPToolGateway):
    def __init__(self) -> None:
        super().__init__("http://api:8080", "service-token")
        self.request: tuple[str, str] | None = None

    def _request(
        self,
        path: str,
        *,
        method: str,
        payload: Mapping[str, Any] | None = None,
    ) -> dict[str, Any]:
        del payload
        self.request = (path, method)
        return {
            "response": "工作任务已执行完成。",
            "data": {"kind": "skill_run", "id": "task/1", "status": "succeeded"},
        }


class HTTPToolGatewayTest(unittest.TestCase):
    @patch("urllib.request.urlopen")
    def test_http_500_is_a_retryable_gateway_error(self, urlopen: Any) -> None:
        urlopen.side_effect = HTTPError(
            "http://api:8080/internal/v1/agent/runs/run-1/tools",
            500,
            "Internal Server Error",
            {"Retry-After": "11"},
            io.BytesIO(b'{"message":"temporary failure"}'),
        )
        gateway = HTTPToolGateway("http://api:8080", "service-token")

        with self.assertRaises(ToolGatewayHTTPError) as raised:
            gateway.definitions("run-1")

        self.assertEqual(raised.exception.status_code, 500)
        self.assertTrue(raised.exception.retryable)
        self.assertEqual(raised.exception.retry_after, "11")

    @patch("urllib.request.urlopen")
    def test_http_400_is_a_permanent_gateway_error(self, urlopen: Any) -> None:
        urlopen.side_effect = HTTPError(
            "http://api:8080/internal/v1/agent/runs/run-1/tools",
            400,
            "Bad Request",
            {},
            io.BytesIO(b'{"message":"invalid request"}'),
        )
        gateway = HTTPToolGateway("http://api:8080", "service-token")

        with self.assertRaises(ToolGatewayHTTPError) as raised:
            gateway.definitions("run-1")

        self.assertEqual(raised.exception.status_code, 400)
        self.assertFalse(raised.exception.retryable)

    @patch("urllib.request.urlopen")
    def test_bound_revision_is_sent_as_a_gateway_fence(self, urlopen: Any) -> None:
        response = urlopen.return_value.__enter__.return_value
        response.read.return_value = b'{"items":[]}'
        gateway = HTTPToolGateway(
            "http://api:8080",
            "service-token",
            expected_revision=7,
        )
        self.assertEqual(gateway.definitions("run-1"), [])
        request = urlopen.call_args.args[0]
        self.assertEqual(request.get_header("X-agent-run-revision"), "7")

    def test_revision_fence_rejects_nonpositive_values(self) -> None:
        gateway = HTTPToolGateway("http://api:8080", "service-token")
        with self.assertRaises(ValueError):
            gateway.bind_revision(0)

    def test_definitions_are_loaded_from_the_go_gateway(self) -> None:
        gateway = DefinitionGateway()
        definitions = gateway.definitions("run/1")
        self.assertEqual(definitions[0]["name"], "life_query_today_plan")
        self.assertEqual(
            gateway.request,
            ("/internal/v1/agent/runs/run%2F1/tools", "GET"),
        )

    def test_prepare_omits_untrusted_identity_fields(self) -> None:
        gateway = RecordingGateway(
            {
                "status": "requires_confirmation",
                "tool_name": "life_prepare_ledger_entry",
                "risk_level": "medium",
                "summary": "支出 50 元",
                "confirmation_token": "signed-token",
                "normalized_arguments": {"amount_minor": 5000},
            }
        )
        prepared = gateway.prepare(
            run_id="run/1",
            user_id="untrusted-user",
            module="life",
            tool_name="life_prepare_ledger_entry",
            arguments={"amount": 50},
            idempotency_key="run-1:prepare",
        )
        self.assertEqual(prepared.confirmation_token, "signed-token")
        path, payload = gateway.requests[0]
        self.assertEqual(
            path,
            "/internal/v1/agent/runs/run%2F1/tools/prepare",
        )
        self.assertNotIn("user_id", payload)
        self.assertNotIn("module", payload)
        self.assertEqual(payload["call_key"], "run-1:prepare")

    def test_commit_sends_only_signed_confirmation(self) -> None:
        gateway = RecordingGateway(
            {"response": "已写入生活账本。", "data": {"entry_id": "entry-1"}}
        )
        outcome = gateway.commit(
            run_id="run-1",
            user_id="untrusted-user",
            module="life",
            preparation={"confirmation_token": "signed-token"},
            resolution={"approved": True},
            idempotency_key="run-1:commit",
        )
        self.assertIn("生活账本", outcome.response)
        _, payload = gateway.requests[0]
        self.assertEqual(
            payload,
            {
                "call_key": "run-1:commit",
                "confirmation_token": "signed-token",
                "approved": True,
            },
        )

    def test_commit_rejects_unapproved_resolution_locally(self) -> None:
        gateway = RecordingGateway({"response": "unused"})
        with self.assertRaises(ValueError):
            gateway.commit(
                run_id="run-1",
                user_id="user-1",
                module="life",
                preparation={"confirmation_token": "signed-token"},
                resolution={"approved": False},
                idempotency_key="run-1:commit",
            )
        self.assertEqual(gateway.requests, [])

    def test_observe_uses_runtime_endpoint_without_identity_fields(self) -> None:
        gateway = ObserveGateway()
        outcome = gateway.observe(
            run_id="run/1",
            user_id="untrusted-user",
            module="work",
            task_id="task/1",
        )
        self.assertEqual(outcome.data["status"], "succeeded")
        self.assertEqual(
            gateway.request,
            ("/internal/v1/agent/runs/run%2F1/tasks/task%2F1", "GET"),
        )

    def test_repair_retry_keeps_task_identity_and_sends_only_repaired_arguments(self) -> None:
        gateway = RecordingGateway(
            {
                "response": "工作任务已重新进入执行队列。",
                "data": {
                    "kind": "skill_run",
                    "id": "task/1",
                    "status": "queued",
                    "attempt": 2,
                },
            }
        )
        outcome = gateway.retry_task(
            run_id="run/1",
            user_id="untrusted-user",
            module="work",
            task_id="task/1",
            arguments={"title": "Important Dates - Semester A 2026/27"},
            operator_id="remove_optional_filename",
            idempotency_key="repair-key",
        )
        self.assertEqual(outcome.data["attempt"], 2)
        path, payload = gateway.requests[0]
        self.assertEqual(
            path,
            "/internal/v1/agent/runs/run%2F1/tasks/task%2F1/retry",
        )
        self.assertEqual(payload["operator_id"], "remove_optional_filename")
        self.assertNotIn("user_id", payload)


if __name__ == "__main__":
    unittest.main()
