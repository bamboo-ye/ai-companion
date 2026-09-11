from __future__ import annotations

import hashlib
import math
import os
import re
import threading
from contextlib import contextmanager
from dataclasses import dataclass
from typing import Any, Callable, Iterator, Mapping
from urllib.parse import urlsplit

_MAX_CAPTURED_STRING = 8_000
_MAX_METADATA_STRING = 512
_SECRET_KEY = re.compile(
    r"(?:api[_-]?key|secret|password|passwd|access[_-]?token|auth[_-]?token|credential)",
    re.IGNORECASE,
)
_BEARER = re.compile(r"(?i)\bbearer\s+[a-z0-9._~+/=-]{12,}")
_EMAIL = re.compile(r"(?i)\b[a-z0-9.!#$%&'*+/=?^_`{|}~-]+@[a-z0-9.-]+\.[a-z]{2,}\b")
_LONG_TOKEN = re.compile(r"\b(?:sk|pk|key|token)[-_][a-z0-9_-]{12,}\b", re.IGNORECASE)


@dataclass(frozen=True)
class LangfuseSettings:
    enabled: bool = False
    public_key: str = ""
    secret_key: str = ""
    base_url: str = "https://cloud.langfuse.com"
    sample_rate: float = 1.0
    capture_content: bool = False
    environment: str = "development"
    release: str = "local"
    flush_at: int = 20
    flush_interval: float = 5.0
    timeout_seconds: int = 3

    @classmethod
    def from_env(cls, environ: Mapping[str, str] | None = None) -> LangfuseSettings:
        environment = os.environ if environ is None else environ
        enabled = _bool_value(environment.get("LANGFUSE_ENABLED", "false"), "LANGFUSE_ENABLED")
        capture_content = _bool_value(
            environment.get("LANGFUSE_CAPTURE_CONTENT", "false"),
            "LANGFUSE_CAPTURE_CONTENT",
        )
        public_key = environment.get("LANGFUSE_PUBLIC_KEY", "").strip()
        secret_key = environment.get("LANGFUSE_SECRET_KEY", "").strip()
        base_url = environment.get("LANGFUSE_BASE_URL", "https://cloud.langfuse.com").strip()
        sample_rate = _float_value(
            environment.get("LANGFUSE_SAMPLE_RATE", "1"),
            "LANGFUSE_SAMPLE_RATE",
            minimum=0,
            maximum=1,
        )
        flush_at = _int_value(
            environment.get("LANGFUSE_FLUSH_AT", "20"),
            "LANGFUSE_FLUSH_AT",
            minimum=1,
            maximum=1_000,
        )
        flush_interval = _float_value(
            environment.get("LANGFUSE_FLUSH_INTERVAL", "5"),
            "LANGFUSE_FLUSH_INTERVAL",
            minimum=0.1,
            maximum=300,
        )
        timeout_seconds = _int_value(
            environment.get("LANGFUSE_TIMEOUT_SECONDS", "3"),
            "LANGFUSE_TIMEOUT_SECONDS",
            minimum=1,
            maximum=30,
        )
        if enabled and (not public_key or not secret_key):
            raise ValueError(
                "LANGFUSE_PUBLIC_KEY and LANGFUSE_SECRET_KEY are required when LANGFUSE_ENABLED=true"
            )
        if enabled and not base_url:
            raise ValueError("LANGFUSE_BASE_URL is required when LANGFUSE_ENABLED=true")
        if enabled:
            parsed_url = urlsplit(base_url)
            if (
                parsed_url.scheme not in ("http", "https")
                or not parsed_url.netloc
                or parsed_url.username is not None
                or parsed_url.password is not None
                or parsed_url.query
                or parsed_url.fragment
            ):
                raise ValueError(
                    "LANGFUSE_BASE_URL must be an absolute HTTP(S) URL without credentials, query or fragment"
                )
            if (
                environment.get("APP_ENV", "development").strip() == "production"
                and parsed_url.scheme != "https"
            ):
                raise ValueError("LANGFUSE_BASE_URL must use https outside local development")
        return cls(
            enabled=enabled,
            public_key=public_key,
            secret_key=secret_key,
            base_url=base_url.rstrip("/"),
            sample_rate=sample_rate,
            capture_content=capture_content,
            environment=(
                environment.get("LANGFUSE_TRACING_ENVIRONMENT", "").strip()
                or environment.get("APP_ENV", "development").strip()
                or "development"
            ),
            release=environment.get("LANGFUSE_RELEASE", "local").strip() or "local",
            flush_at=flush_at,
            flush_interval=flush_interval,
            timeout_seconds=timeout_seconds,
        )


class GenerationObservation:
    def __init__(self, observation: Any | None, *, capture_content: bool) -> None:
        self._observation = observation
        self._capture_content = capture_content
        self._finished = False

    def succeed(self, result: Mapping[str, Any], event: Mapping[str, Any]) -> None:
        if self._observation is None or self._finished:
            return
        usage_details = {
            "input": _non_negative_int(event.get("prompt_tokens")),
            "output": _non_negative_int(event.get("completion_tokens")),
            "cache_read_input_tokens": _non_negative_int(event.get("cached_tokens")),
            "reasoning": _non_negative_int(event.get("reasoning_tokens")),
        }
        usage_details["total"] = usage_details["input"] + usage_details["output"]
        cost_micros = _non_negative_int(event.get("cost_micros"))
        returned_model = _safe_string(event.get("returned_model"), 256)
        try:
            self._observation.update(
                model=returned_model or _safe_string(event.get("requested_model"), 256),
                output=_generation_output(result, capture_content=self._capture_content),
                usage_details=usage_details,
                cost_details={"total": cost_micros / 1_000_000},
                metadata=_generation_metadata(event),
                level="DEFAULT",
            )
            self._finished = True
        except Exception:
            # Telemetry must never affect a model response already obtained.
            return

    def fail(self, error: BaseException, event: Mapping[str, Any]) -> None:
        if self._observation is None or self._finished:
            return
        try:
            self._observation.update(
                output={"status": "error"},
                metadata=_generation_metadata(event),
                level="ERROR",
                status_message=_safe_error(error),
            )
            self._finished = True
        except Exception:
            return

    def close(self) -> None:
        if self._observation is None:
            return
        try:
            self._observation.end()
        except Exception:
            return


class RunObservation:
    def __init__(
        self,
        telemetry: LangfuseTelemetry,
        observation: Any | None,
        *,
        trace_id: str,
        run: Mapping[str, Any],
        setup_succeeded: bool,
    ) -> None:
        self._telemetry = telemetry
        self._observation = observation
        self._trace_id = trace_id
        self._run = run
        self._setup_succeeded = setup_succeeded
        self._finished = False

    def finish(self, result: Mapping[str, Any], *, status: str) -> None:
        if self._observation is None or self._finished:
            return
        try:
            self._emit_node_observations(result)
            outcome = _safe_string(result.get("outcome"), 128) or status
            response = result.get("response")
            output: dict[str, Any] = {
                "status": status,
                "outcome": outcome,
                "intent": _safe_string(result.get("intent"), 256),
                "response_chars": len(response) if isinstance(response, str) else 0,
            }
            if self._telemetry.settings.capture_content and isinstance(response, str):
                output["response"] = _sanitize(response)
            self._observation.update(
                output=output,
                metadata={
                    **_result_metadata(result),
                    "status": status,
                },
                level="ERROR" if outcome in ("failed", "error") else "DEFAULT",
            )
            self._emit_scores(result, status=status)
            self._finished = True
        except Exception:
            return

    def fail(self, error: BaseException) -> None:
        if self._observation is None or self._finished:
            return
        try:
            self._observation.update(
                output={"status": "error"},
                level="ERROR",
                status_message=_safe_error(error),
            )
            self._observation.score(
                name="agent_completed",
                value=False,
                data_type="BOOLEAN",
                comment="Agent runtime raised an exception.",
            )
            self._finished = True
        except Exception:
            return

    def reference(self) -> dict[str, Any]:
        if not self._telemetry.settings.enabled:
            return {"enabled": False}
        return {
            "enabled": True,
            "trace_id": self._trace_id,
            "initialized": self._setup_succeeded,
            "capture_content": self._telemetry.settings.capture_content,
            "environment": self._telemetry.settings.environment,
        }

    def close(self) -> None:
        if self._observation is None:
            return
        try:
            self._observation.end()
        except Exception:
            return

    def _emit_node_observations(self, result: Mapping[str, Any]) -> None:
        observation = self._observation
        if observation is None:
            return
        raw_trace = result.get("node_trace")
        if not isinstance(raw_trace, list):
            return
        for index, raw in enumerate(raw_trace[:256]):
            if not isinstance(raw, Mapping):
                continue
            node = _safe_string(raw.get("node"), 128) or f"node-{index + 1}"
            status = _safe_string(raw.get("status"), 64) or "unknown"
            duration_ms = _non_negative_int(raw.get("duration_ms"))
            try:
                span = observation.start_observation(
                    name=f"node.{node}",
                    as_type=_node_observation_type(node),
                    metadata={
                        "graph_node": node,
                        "status": status,
                        "recorded_duration_ms": duration_ms,
                        "sequence": index + 1,
                    },
                    level="ERROR" if status in ("failed", "error") else "DEFAULT",
                )
                span.end()
            except Exception:
                continue

    def _emit_scores(self, result: Mapping[str, Any], *, status: str) -> None:
        observation = self._observation
        if observation is None or status != "completed":
            return
        completed = result.get("outcome") not in ("failed", "error")
        observation.score(
            name="agent_completed",
            value=completed,
            data_type="BOOLEAN",
        )
        validation = result.get("response_validation")
        if isinstance(validation, Mapping) and isinstance(validation.get("passed"), bool):
            observation.score(
                name="response_contract_passed",
                value=validation["passed"],
                data_type="BOOLEAN",
            )
        steps = _non_negative_int(result.get("steps"))
        observation.score(name="agent_steps", value=float(steps), data_type="NUMERIC")
        usage = result.get("budget_usage")
        if isinstance(usage, Mapping):
            observation.score(
                name="model_cost_usd",
                value=_non_negative_int(usage.get("cost_micros")) / 1_000_000,
                data_type="NUMERIC",
            )


class LangfuseTelemetry:
    """Fail-open Langfuse bridge for one serial Agent Worker process."""

    def __init__(
        self,
        settings: LangfuseSettings,
        *,
        client: Any | None = None,
        client_factory: Callable[..., Any] | None = None,
    ) -> None:
        self.settings = settings
        self._lock = threading.RLock()
        self._active_root: Any | None = None
        self._client: Any | None = None
        if not settings.enabled:
            return
        if client is not None:
            self._client = client
            return
        if client_factory is None:
            from langfuse import Langfuse

            client_factory = Langfuse
        self._client = client_factory(
            public_key=settings.public_key,
            secret_key=settings.secret_key,
            base_url=settings.base_url,
            sample_rate=settings.sample_rate,
            environment=settings.environment,
            release=settings.release,
            flush_at=settings.flush_at,
            flush_interval=settings.flush_interval,
            timeout=settings.timeout_seconds,
            mask=_langfuse_mask,
        )

    @classmethod
    def from_env(
        cls,
        environ: Mapping[str, str] | None = None,
        *,
        client_factory: Callable[..., Any] | None = None,
    ) -> LangfuseTelemetry:
        return cls(LangfuseSettings.from_env(environ), client_factory=client_factory)

    @contextmanager
    def observe_run(self, run: Mapping[str, Any]) -> Iterator[RunObservation]:
        if not self.settings.enabled or self._client is None:
            yield RunObservation(self, None, trace_id="", run=run, setup_succeeded=False)
            return
        run_id = _required_text(run.get("id"), "Agent Run id")
        trace_context = _otel_trace_context(run)
        trace_id = (
            str(trace_context["trace_id"])
            if trace_context is not None
            else self._trace_id(run_id)
        )
        if trace_context is None:
            trace_context = {"trace_id": trace_id}
        root: Any | None = None
        try:
            root = self._client.start_observation(
                trace_context=trace_context,
                name=f"agent.{_safe_string(run.get('module'), 64) or 'unknown'}",
                as_type="agent",
                input=_run_input(run, capture_content=self.settings.capture_content),
                metadata=_run_metadata(run),
                version=_run_version(run),
            )
        except Exception:
            root = None
        observation = RunObservation(
            self,
            root,
            trace_id=trace_id,
            run=run,
            setup_succeeded=root is not None,
        )
        if root is not None:
            with self._lock:
                self._active_root = root
        try:
            yield observation
        except BaseException as exc:
            observation.fail(exc)
            raise
        finally:
            with self._lock:
                if self._active_root is root:
                    self._active_root = None
            observation.close()

    @contextmanager
    def observe_generation(
        self,
        *,
        role: str,
        requested_model: str,
        payload: Mapping[str, Any],
        timeout_seconds: float,
    ) -> Iterator[GenerationObservation]:
        with self._lock:
            root = self._active_root
        child: Any | None = None
        if root is not None:
            try:
                child = root.start_observation(
                    name=f"llm.{_safe_string(role, 64) or 'unknown'}",
                    as_type="generation",
                    input=_generation_input(
                        payload,
                        capture_content=self.settings.capture_content,
                    ),
                    metadata={
                        "role": _safe_string(role, 64),
                        "provider": "openrouter",
                        "timeout_ms": max(0, math.ceil(timeout_seconds * 1_000)),
                    },
                    model=_safe_string(requested_model, 256),
                    model_parameters=_model_parameters(payload),
                )
            except Exception:
                child = None
        observation = GenerationObservation(
            child,
            capture_content=self.settings.capture_content,
        )
        try:
            yield observation
        finally:
            observation.close()

    def flush(self) -> None:
        if self._client is None:
            return
        try:
            self._client.flush()
        except Exception:
            return

    def shutdown(self) -> None:
        if self._client is None:
            return
        try:
            self._client.shutdown()
        except Exception:
            return

    def _trace_id(self, run_id: str) -> str:
        if self._client is not None:
            try:
                value = self._client.create_trace_id(seed=f"agent-run:{run_id}")
                if isinstance(value, str) and re.fullmatch(r"[0-9a-f]{32}", value):
                    return value
            except Exception:
                pass
        return hashlib.sha256(f"agent-run:{run_id}".encode()).hexdigest()[:32]


def _otel_trace_context(run: Mapping[str, Any]) -> dict[str, str] | None:
    value = run.get("otel_traceparent")
    if not isinstance(value, str):
        return None
    match = re.fullmatch(
        r"00-([0-9a-fA-F]{32})-([0-9a-fA-F]{16})-([0-9a-fA-F]{2})",
        value.strip(),
    )
    if match is None:
        return None
    trace_id, parent_span_id, _flags = (part.lower() for part in match.groups())
    if trace_id == "0" * 32 or parent_span_id == "0" * 16:
        return None
    return {"trace_id": trace_id, "parent_span_id": parent_span_id}


def _run_input(run: Mapping[str, Any], *, capture_content: bool) -> dict[str, Any]:
    payload = run.get("input")
    payload = payload if isinstance(payload, Mapping) else {}
    message = payload.get("user_message", payload.get("text"))
    context = payload.get("context")
    result: dict[str, Any] = {
        "message_chars": len(message) if isinstance(message, str) else 0,
        "context_keys": sorted(str(key)[:128] for key in context)[:128]
        if isinstance(context, Mapping)
        else [],
    }
    if capture_content and isinstance(message, str):
        result["user_message"] = _sanitize(message)
    return result


def _run_metadata(run: Mapping[str, Any]) -> dict[str, Any]:
    metadata = {
        "run_id": _safe_string(run.get("id"), 128),
        "module": _safe_string(run.get("module"), 64),
        "graph_name": _safe_string(run.get("graph_name"), 128),
        "graph_version": _safe_string(run.get("graph_version"), 128),
        "user_ref": _stable_reference(run.get("user_id")),
        "conversation_ref": _stable_reference(run.get("conversation_id")),
        "character_ref": _stable_reference(run.get("character_id")),
        "agent_definition_key": _safe_string(run.get("agent_definition_key"), 128),
        "agent_definition_version_id": _safe_string(run.get("agent_definition_version_id"), 128),
        "agent_definition_version": _non_negative_int(run.get("agent_definition_version")),
        "agent_definition_fingerprint": _safe_string(run.get("agent_definition_fingerprint"), 128),
        "model_profile_key": _safe_string(run.get("model_profile_key"), 128),
        "model_profile_version_id": _safe_string(run.get("model_profile_version_id"), 128),
        "model_profile_config_version": _safe_string(run.get("model_profile_config_version"), 128),
    }
    return {key: value for key, value in metadata.items() if value not in ("", 0)}


def _run_version(run: Mapping[str, Any]) -> str:
    agent_version = _non_negative_int(run.get("agent_definition_version"))
    if agent_version:
        return f"agent-v{agent_version}"
    return _safe_string(run.get("graph_version"), 128) or "builtin"


def _generation_input(payload: Mapping[str, Any], *, capture_content: bool) -> dict[str, Any]:
    messages = payload.get("messages")
    message_count = len(messages) if isinstance(messages, list) else 0
    result: dict[str, Any] = {"message_count": message_count}
    if capture_content and isinstance(messages, list):
        result["messages"] = _sanitize(messages)
    return result


def _generation_output(result: Mapping[str, Any], *, capture_content: bool) -> dict[str, Any]:
    choices = result.get("choices")
    response_chars = 0
    safe_choices: Any = None
    if isinstance(choices, list):
        for choice in choices:
            if not isinstance(choice, Mapping):
                continue
            message = choice.get("message")
            content = message.get("content") if isinstance(message, Mapping) else None
            if isinstance(content, str):
                response_chars += len(content)
        if capture_content:
            safe_choices = _sanitize(choices)
    output: dict[str, Any] = {
        "status": "succeeded",
        "choice_count": len(choices) if isinstance(choices, list) else 0,
        "response_chars": response_chars,
    }
    if safe_choices is not None:
        output["choices"] = safe_choices
    return output


def _generation_metadata(event: Mapping[str, Any]) -> dict[str, Any]:
    keys = (
        "role",
        "status",
        "provider",
        "requested_model",
        "returned_model",
        "upstream_provider",
        "latency_ms",
        "timeout_ms",
        "reasoning_effort",
        "error_status",
        "retryable",
        "contract_valid",
        "contract_error",
    )
    return {
        key: _sanitize(event.get(key), max_string=_MAX_METADATA_STRING)
        for key in keys
        if event.get(key) not in (None, "")
    }


def _model_parameters(
    payload: Mapping[str, Any],
) -> dict[str, str | int | float | bool | list[str] | None]:
    result: dict[str, str | int | float | bool | list[str] | None] = {}
    for key in ("temperature", "max_tokens", "top_p", "seed"):
        value = payload.get(key)
        if isinstance(value, (str, int, float, bool)) and not isinstance(value, complex):
            result[key] = value
    reasoning = payload.get("reasoning")
    if isinstance(reasoning, Mapping):
        effort = reasoning.get("effort")
        if isinstance(effort, str):
            result["reasoning_effort"] = effort[:64]
    return result


def _result_metadata(result: Mapping[str, Any]) -> dict[str, Any]:
    usage = result.get("budget_usage")
    usage = usage if isinstance(usage, Mapping) else {}
    repair_history = result.get("repair_history")
    model_events = result.get("model_events")
    return {
        "steps": _non_negative_int(result.get("steps")),
        "model_calls": _non_negative_int(usage.get("model_calls")),
        "prompt_tokens": _non_negative_int(usage.get("prompt_tokens")),
        "completion_tokens": _non_negative_int(usage.get("completion_tokens")),
        "cost_micros": _non_negative_int(usage.get("cost_micros")),
        "repair_count": len(repair_history) if isinstance(repair_history, list) else 0,
        "model_event_count": len(model_events) if isinstance(model_events, list) else 0,
        "execution_mode": _safe_string(result.get("execution_mode"), 128),
    }


def _node_observation_type(node: str) -> str:
    lowered = node.casefold()
    if any(part in lowered for part in ("tool", "commit", "observe", "execute")):
        return "tool"
    if "guard" in lowered or "validat" in lowered:
        return "guardrail"
    return "span"


def _stable_reference(value: Any) -> str:
    if not isinstance(value, str) or not value.strip():
        return ""
    return "sha256:" + hashlib.sha256(value.strip().encode()).hexdigest()[:24]


def _sanitize(value: Any, *, max_string: int = _MAX_CAPTURED_STRING, depth: int = 0) -> Any:
    if depth >= 8:
        return "[TRUNCATED_DEPTH]"
    if isinstance(value, Mapping):
        result: dict[str, Any] = {}
        for raw_key, raw_value in list(value.items())[:128]:
            key = str(raw_key)[:128]
            result[key] = (
                "[REDACTED]"
                if _SECRET_KEY.search(key)
                else _sanitize(raw_value, max_string=max_string, depth=depth + 1)
            )
        return result
    if isinstance(value, (list, tuple)):
        return [
            _sanitize(item, max_string=max_string, depth=depth + 1) for item in list(value)[:128]
        ]
    if isinstance(value, str):
        sanitized = _BEARER.sub("Bearer [REDACTED]", value)
        sanitized = _EMAIL.sub("[REDACTED_EMAIL]", sanitized)
        sanitized = _LONG_TOKEN.sub("[REDACTED_TOKEN]", sanitized)
        if len(sanitized) > max_string:
            return sanitized[:max_string] + "[[TRUNCATED]]"
        return sanitized
    if value is None or isinstance(value, (bool, int, float)):
        return value
    return _safe_string(value, max_string)


def _langfuse_mask(*, data: Any, **_kwargs: dict[str, Any]) -> Any:
    return _sanitize(data)


def _safe_error(error: BaseException) -> str:
    message = str(error).strip() or type(error).__name__
    return _safe_string(_sanitize(message), _MAX_METADATA_STRING)


def _safe_string(value: Any, maximum: int) -> str:
    if isinstance(value, str):
        return value.strip()[:maximum]
    if value is None:
        return ""
    return str(value).strip()[:maximum]


def _required_text(value: Any, label: str) -> str:
    result = _safe_string(value, 512)
    if not result:
        raise ValueError(f"{label} is required")
    return result


def _non_negative_int(value: Any) -> int:
    return value if isinstance(value, int) and not isinstance(value, bool) and value >= 0 else 0


def _bool_value(value: str, name: str) -> bool:
    normalized = value.strip().casefold()
    if normalized == "true":
        return True
    if normalized == "false":
        return False
    raise ValueError(f"{name} must be true or false")


def _float_value(value: str, name: str, *, minimum: float, maximum: float) -> float:
    try:
        parsed = float(value)
    except ValueError as exc:
        raise ValueError(f"{name} must be a number") from exc
    if not math.isfinite(parsed) or not minimum <= parsed <= maximum:
        raise ValueError(f"{name} must be between {minimum} and {maximum}")
    return parsed


def _int_value(value: str, name: str, *, minimum: int, maximum: int) -> int:
    try:
        parsed = int(value)
    except ValueError as exc:
        raise ValueError(f"{name} must be an integer") from exc
    if not minimum <= parsed <= maximum:
        raise ValueError(f"{name} must be between {minimum} and {maximum}")
    return parsed
