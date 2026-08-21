from __future__ import annotations

import json
import os
import urllib.error
import urllib.parse
import urllib.request
from typing import Any, Mapping, cast

from ai_companion_worker.agent_runtime import (
    ModuleKey,
    RiskLevel,
    ToolOutcome,
    ToolPreparation,
    ToolStatus,
)


class ToolGatewayHTTPError(RuntimeError):
    """A gateway rejection with status-aware retry semantics."""

    def __init__(
        self,
        message: str,
        *,
        status_code: int,
        retry_after: str = "",
    ) -> None:
        super().__init__(message)
        self.status_code = status_code
        self.retry_after = retry_after.strip()
        self.retryable = status_code in (408, 409, 429) or 500 <= status_code < 600


class HTTPToolGateway:
    """Authenticated adapter for the Go-owned domain tool gateway.

    User, character, conversation, and module identifiers are intentionally
    omitted from HTTP payloads. The Go service derives them from the persisted
    Agent Run and treats the Python process only as an orchestration client.
    """

    def __init__(
        self,
        base_url: str,
        service_token: str,
        timeout_seconds: float = 10.0,
        expected_revision: int = 0,
    ) -> None:
        normalized_url = base_url.strip().rstrip("/")
        if not normalized_url.startswith(("http://", "https://")):
            raise ValueError("agent gateway base URL must use http or https")
        if not service_token.strip():
            raise ValueError("agent gateway service token is required")
        if timeout_seconds <= 0:
            raise ValueError("agent gateway timeout must be positive")
        self._base_url = normalized_url
        self._service_token = service_token.strip()
        self._timeout_seconds = timeout_seconds
        self._expected_revision = 0
        if expected_revision:
            self.bind_revision(expected_revision)

    @classmethod
    def from_env(cls) -> HTTPToolGateway:
        timeout = float(os.getenv("AGENT_GATEWAY_TIMEOUT_SECONDS", "10"))
        return cls(
            os.getenv("AGENT_GATEWAY_URL", "http://api:8080"),
            os.environ["AGENT_GATEWAY_TOKEN"],
            timeout,
        )

    def bind_revision(self, revision: int) -> None:
        if not isinstance(revision, int) or isinstance(revision, bool) or revision <= 0:
            raise ValueError("agent gateway expected revision must be positive")
        self._expected_revision = revision

    def prepare(
        self,
        *,
        run_id: str,
        user_id: str,
        module: ModuleKey,
        tool_name: str,
        arguments: Mapping[str, Any],
        idempotency_key: str,
    ) -> ToolPreparation:
        del user_id, module
        result = self._post(
            f"/internal/v1/agent/runs/{self._run_path(run_id)}/tools/prepare",
            {
                "call_key": idempotency_key,
                "tool_name": tool_name,
                "arguments": dict(arguments),
            },
        )
        status = result.get("status")
        risk_level = result.get("risk_level", "none")
        if status not in ("completed", "requires_confirmation"):
            raise RuntimeError("agent gateway returned an invalid tool status")
        if risk_level not in ("none", "low", "medium", "high"):
            raise RuntimeError("agent gateway returned an invalid risk level")
        normalized_arguments = result.get("normalized_arguments", {})
        if not isinstance(normalized_arguments, dict):
            raise RuntimeError("agent gateway returned invalid normalized arguments")
        return ToolPreparation(
            status=cast(ToolStatus, status),
            tool_name=self._required_string(result, "tool_name"),
            response=self._optional_string(result, "response"),
            risk_level=cast(RiskLevel, risk_level),
            summary=self._optional_string(result, "summary"),
            confirmation_token=self._optional_string(result, "confirmation_token"),
            normalized_arguments=normalized_arguments,
            data=result.get("data"),
        )

    def definitions(self, run_id: str) -> list[dict[str, Any]]:
        result = self._request(
            f"/internal/v1/agent/runs/{self._run_path(run_id)}/tools",
            method="GET",
        )
        items = result.get("items")
        if not isinstance(items, list) or not all(isinstance(item, dict) for item in items):
            raise RuntimeError("agent gateway returned invalid tool definitions")
        return [cast(dict[str, Any], dict(item)) for item in items]

    def commit(
        self,
        *,
        run_id: str,
        user_id: str,
        module: ModuleKey,
        preparation: Mapping[str, Any],
        resolution: Mapping[str, Any],
        idempotency_key: str,
    ) -> ToolOutcome:
        del user_id, module
        approved = resolution.get("approved")
        token = preparation.get("confirmation_token")
        if approved is not True:
            raise ValueError("approved confirmation resolution is required")
        if not isinstance(token, str) or not token.strip():
            raise ValueError("confirmation token is required")
        result = self._post(
            f"/internal/v1/agent/runs/{self._run_path(run_id)}/tools/commit",
            {
                "call_key": idempotency_key,
                "confirmation_token": token,
                "approved": True,
            },
        )
        return ToolOutcome(
            response=self._required_string(result, "response"),
            data=result.get("data"),
        )

    def observe(
        self,
        *,
        run_id: str,
        user_id: str,
        module: ModuleKey,
        task_id: str,
    ) -> ToolOutcome:
        del user_id, module
        result = self._request(
            (
                f"/internal/v1/agent/runs/{self._run_path(run_id)}/tasks/"
                f"{self._run_path(task_id)}"
            ),
            method="GET",
        )
        return ToolOutcome(
            response=self._required_string(result, "response"),
            data=result.get("data"),
        )

    def retry_task(
        self,
        *,
        run_id: str,
        user_id: str,
        module: ModuleKey,
        task_id: str,
        arguments: Mapping[str, Any],
        operator_id: str,
        idempotency_key: str,
    ) -> ToolOutcome:
        del user_id, module
        result = self._post(
            (
                f"/internal/v1/agent/runs/{self._run_path(run_id)}/tasks/"
                f"{self._run_path(task_id)}/retry"
            ),
            {
                "call_key": idempotency_key,
                "operator_id": operator_id,
                "arguments": dict(arguments),
            },
        )
        return ToolOutcome(
            response=self._required_string(result, "response"),
            data=result.get("data"),
        )

    def _post(self, path: str, payload: Mapping[str, Any]) -> dict[str, Any]:
        return self._request(path, method="POST", payload=payload)

    def _request(
        self,
        path: str,
        *,
        method: str,
        payload: Mapping[str, Any] | None = None,
    ) -> dict[str, Any]:
        data = None
        if payload is not None:
            data = json.dumps(payload, ensure_ascii=False, separators=(",", ":")).encode(
                "utf-8"
            )
        request = urllib.request.Request(
            self._base_url + path,
            data=data,
            method=method,
            headers={
                "Authorization": f"Bearer {self._service_token}",
                "Content-Type": "application/json",
                "Accept": "application/json",
                **(
                    {"X-Agent-Run-Revision": str(self._expected_revision)}
                    if self._expected_revision > 0
                    else {}
                ),
            },
        )
        try:
            with urllib.request.urlopen(request, timeout=self._timeout_seconds) as response:
                body = response.read()
        except urllib.error.HTTPError as exc:
            message = self._http_error_message(exc)
            raise ToolGatewayHTTPError(
                f"agent gateway rejected request ({exc.code}): {message}",
                status_code=exc.code,
                retry_after=exc.headers.get("Retry-After", ""),
            ) from exc
        except urllib.error.URLError as exc:
            raise RuntimeError("agent gateway is unavailable") from exc
        try:
            decoded = json.loads(body)
        except (json.JSONDecodeError, UnicodeDecodeError) as exc:
            raise RuntimeError("agent gateway returned invalid JSON") from exc
        if not isinstance(decoded, dict):
            raise RuntimeError("agent gateway response must be an object")
        return cast(dict[str, Any], decoded)

    @staticmethod
    def _run_path(run_id: str) -> str:
        normalized = run_id.strip()
        if not normalized:
            raise ValueError("run_id is required")
        return urllib.parse.quote(normalized, safe="")

    @staticmethod
    def _required_string(value: Mapping[str, Any], key: str) -> str:
        result = value.get(key)
        if not isinstance(result, str) or not result.strip():
            raise RuntimeError(f"agent gateway response requires {key}")
        return result

    @staticmethod
    def _optional_string(value: Mapping[str, Any], key: str) -> str:
        result = value.get(key, "")
        if not isinstance(result, str):
            raise RuntimeError(f"agent gateway response contains invalid {key}")
        return result

    @staticmethod
    def _http_error_message(error: urllib.error.HTTPError) -> str:
        try:
            body = json.loads(error.read())
        except (json.JSONDecodeError, UnicodeDecodeError):
            return "request failed"
        if isinstance(body, dict) and isinstance(body.get("message"), str):
            return cast(str, body["message"])
        return "request failed"
