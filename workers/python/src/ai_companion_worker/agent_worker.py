from __future__ import annotations

import json
import os
import sys
from typing import Any, BinaryIO, Callable, Mapping, cast

from ai_companion_worker.agent_checkpoint import scoped_dsn
from ai_companion_worker.agent_gateway_client import (
    HTTPToolGateway,
    ToolGatewayHTTPError,
)
from ai_companion_worker.agent_governance import (
    DEFAULT_MODEL_CONFIG_VERSION,
    GRAPH_NAME,
    GRAPH_VERSION,
)
from ai_companion_worker.agent_runtime import (
    AgentAssessment,
    AgentPlan,
    AgentRuntime,
    DecisionPort,
    ModelDecision,
    ModuleKey,
    RepairDecision,
    ToolGateway,
    build_graph,
    interrupt_payloads,
)
from ai_companion_worker.openrouter_decision import (
    OpenRouterDecisionPort,
    OpenRouterError,
)

_MAX_INPUT_BYTES = 4 << 20
_RUNTIME_PROTOCOL_VERSION = "agent-runtime-jsonl-v1"


class DevelopmentDecisionPort:
    def model_manifest(self) -> dict[str, Any]:
        return {
            "provider": "development",
            "config_version": "development-v1",
            "pinned": True,
            "roles": {},
        }

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
            steps=("理解目标", "执行必要工具", "观察结果", "交付结果"),
            success_criteria="返回可核验的实际结果。",
        )

    def decide(
        self,
        *,
        module: ModuleKey,
        message: str,
        context: Mapping[str, Any],
    ) -> ModelDecision:
        del module, context
        return ModelDecision(
            intent="development_no_tool",
            needs_response=True,
        )

    def compose_arguments(
        self,
        *,
        module: ModuleKey,
        message: str,
        tool_name: str,
        context: Mapping[str, Any],
    ) -> Mapping[str, Any]:
        del module, message, tool_name, context
        raise ValueError("development decision port does not compose production tool arguments")

    def respond(
        self,
        *,
        module: ModuleKey,
        message: str,
        context: Mapping[str, Any],
    ) -> str:
        del module, context
        return f"开发环境 Agent 已接收：{message}"

    def revise_response(
        self,
        *,
        module: ModuleKey,
        message: str,
        response: str,
        violations: list[Mapping[str, Any]],
        context: Mapping[str, Any],
    ) -> str:
        del module, message, violations, context
        return response

    def assess(
        self,
        *,
        module: ModuleKey,
        message: str,
        context: Mapping[str, Any],
    ) -> AgentAssessment:
        del module, message, context
        return AgentAssessment(status="completed", reason="开发环境结果已返回")

    def repair(
        self,
        *,
        module: ModuleKey,
        message: str,
        tool_name: str,
        arguments: Mapping[str, Any],
        failure: Mapping[str, Any],
        context: Mapping[str, Any],
    ) -> RepairDecision:
        del module, message, tool_name, arguments, failure, context
        return RepairDecision(strategy="abort", reason_code="development_mode")


def execute_run(
    run: Mapping[str, Any],
    *,
    checkpointer: Any,
    decisions: DecisionPort,
    tools: ToolGateway,
    definitions: list[dict[str, Any]],
    runtime: AgentRuntime | None = None,
) -> dict[str, Any]:
    run_id = _required_string(run, "id")
    _validate_graph_identity(run)
    if runtime is None:
        runtime = AgentRuntime(
            build_graph(
                checkpointer=checkpointer,
                decisions=decisions,
                tools=tools,
            )
        )
    module = _module(run.get("module"))
    input_payload = run.get("input")
    if not isinstance(input_payload, dict):
        raise ValueError("Agent Run input must be an object")
    message = input_payload.get("user_message", input_payload.get("text"))
    if not isinstance(message, str) or not message.strip():
        raise ValueError("Agent Run input requires user_message or text")
    context = input_payload.get("context", {})
    if not isinstance(context, dict):
        raise ValueError("Agent Run context must be an object")
    trusted_context = dict(context)
    trusted_context["tools"] = [dict(item) for item in definitions]
    initial = {
        "run_id": run_id,
        "user_id": _required_string(run, "user_id"),
        "conversation_id": _required_string(run, "conversation_id"),
        "character_id": _required_string(run, "character_id"),
        "module": module,
        "user_message": message.strip(),
        "context": trusted_context,
    }
    raw_resolution = run.get("resume_resolution")
    resolution = raw_resolution if isinstance(raw_resolution, dict) else None
    if raw_resolution is not None and resolution is None:
        raise ValueError("Agent Run resume_resolution must be an object")
    result = runtime.execute(
        run_id,
        initial=initial,
        resolution=resolution,
    )
    interrupts = interrupt_payloads(result)
    if interrupts:
        interrupt_types = {item.get("type") for item in interrupts}
        if interrupt_types == {"tool_approval"}:
            status = "waiting_approval"
        elif interrupt_types == {"tool_wait"}:
            status = "waiting_tool"
        else:
            raise ValueError("Agent runtime returned unsupported mixed interrupts")
        return {
            "status": status,
            "output": {
                "outcome": status,
                "intent": _optional_string(result, "intent"),
                "response": _optional_string(result, "response"),
                "interrupts": interrupts,
                "plan": _json_value(result.get("plan")),
                "observations": _json_value(result.get("observations")),
                "assessment": _json_value(result.get("assessment")),
                "execution_mode": _optional_string(result, "execution_mode"),
                "response_validation": _json_value(result.get("response_validation")),
                "action_budget": result.get("action_budget", 0),
                **_governance_output(result),
            },
        }
    return {
        "status": "completed",
        "output": {
            "outcome": _optional_string(result, "outcome") or "completed",
            "intent": _optional_string(result, "intent"),
            "response": _required_string(result, "response"),
            "tool_result": _json_value(result.get("tool_result")),
            "plan": _json_value(result.get("plan")),
            "observations": _json_value(result.get("observations")),
            "assessment": _json_value(result.get("assessment")),
            "execution_mode": _optional_string(result, "execution_mode"),
            "response_validation": _json_value(result.get("response_validation")),
            "action_budget": result.get("action_budget", 0),
            "steps": result.get("steps", 0),
            **_governance_output(result),
        },
    }


def run_once(run: Mapping[str, Any]) -> dict[str, Any]:
    from langgraph.checkpoint.postgres import PostgresSaver

    _validate_graph_identity(run)
    gateway = HTTPToolGateway.from_env()
    gateway.bind_revision(_required_positive_int(run, "revision"))
    run_id = _required_string(run, "id")
    # Tool definitions are checkpointed with the initial trusted context. A
    # durable resume only needs the pending checkpoint and its resolution, so
    # avoid an extra gateway round trip on every tool/approval recovery.
    definitions = gateway.definitions(run_id) if _requires_tool_definitions(run) else []
    decisions = _decision_port_from_env()
    dsn = scoped_dsn(os.environ.get("LANGGRAPH_POSTGRES_DSN", ""))
    with PostgresSaver.from_conn_string(dsn) as checkpointer:
        return execute_run(
            run,
            checkpointer=checkpointer,
            decisions=decisions,
            tools=gateway,
            definitions=definitions,
        )


def _requires_tool_definitions(run: Mapping[str, Any]) -> bool:
    return not isinstance(run.get("resume_resolution"), dict)


def _decision_port_from_env() -> DecisionPort:
    provider = os.getenv("MODEL_PROVIDER", "development").strip().lower()
    if provider == "development":
        return DevelopmentDecisionPort()
    if provider == "openrouter":
        return OpenRouterDecisionPort.from_env()
    raise ValueError("Agent runtime MODEL_PROVIDER must be development or openrouter")


def serve() -> None:
    from langgraph.checkpoint.postgres import PostgresSaver

    gateway = HTTPToolGateway.from_env()
    decisions = _decision_port_from_env()
    dsn = scoped_dsn(os.environ.get("LANGGRAPH_POSTGRES_DSN", ""))
    with PostgresSaver.from_conn_string(dsn) as checkpointer:
        runtime = AgentRuntime(
            build_graph(
                checkpointer=checkpointer,
                decisions=decisions,
                tools=gateway,
            )
        )

        def handle(run: Mapping[str, Any]) -> dict[str, Any]:
            _validate_graph_identity(run)
            gateway.bind_revision(_required_positive_int(run, "revision"))
            run_id = _required_string(run, "id")
            definitions = gateway.definitions(run_id) if _requires_tool_definitions(run) else []
            try:
                return execute_run(
                    run,
                    checkpointer=checkpointer,
                    decisions=decisions,
                    tools=gateway,
                    definitions=definitions,
                    runtime=runtime,
                )
            finally:
                consume = getattr(decisions, "consume_observability", None)
                if callable(consume):
                    consume()

        _write_ready(sys.stdout.buffer)
        _serve_requests(sys.stdin.buffer, sys.stdout.buffer, handle)


def _ready_envelope() -> dict[str, str]:
    return {
        "type": "ready",
        "protocol_version": _RUNTIME_PROTOCOL_VERSION,
        "graph_name": GRAPH_NAME,
        "graph_version": GRAPH_VERSION,
    }


def _write_ready(output_stream: BinaryIO) -> None:
    output_stream.write(
        json.dumps(
            _ready_envelope(),
            ensure_ascii=False,
            separators=(",", ":"),
        ).encode("utf-8")
        + b"\n"
    )
    output_stream.flush()


def _serve_requests(
    input_stream: BinaryIO,
    output_stream: BinaryIO,
    handler: Callable[[Mapping[str, Any]], dict[str, Any]],
) -> None:
    for raw in input_stream:
        try:
            if not raw.strip() or len(raw) > _MAX_INPUT_BYTES:
                raise ValueError("Agent Run input is empty or too large")
            decoded = json.loads(raw)
            if not isinstance(decoded, dict):
                raise ValueError("Agent Run input must be a JSON object")
            envelope: dict[str, Any] = {
                "ok": True,
                "result": handler(cast(dict[str, Any], decoded)),
            }
        except Exception as exc:
            error = _error_envelope(exc)
            error["recycle"] = _requires_process_recycle(exc)
            envelope = {"ok": False, "error": error}
        output_stream.write(
            json.dumps(envelope, ensure_ascii=False, separators=(",", ":")).encode("utf-8") + b"\n"
        )
        output_stream.flush()


def _requires_process_recycle(error: BaseException) -> bool:
    current: BaseException | None = error
    for _ in range(8):
        if current is None:
            return False
        if type(current).__module__.split(".", 1)[0] == "psycopg":
            return True
        next_error = current.__cause__ or current.__context__
        current = next_error if isinstance(next_error, BaseException) else None
    return False


def main() -> None:
    if sys.argv[1:] == ["--serve"]:
        try:
            serve()
        except Exception as exc:
            print(
                "agent runtime error:"
                + json.dumps(
                    _error_envelope(exc),
                    ensure_ascii=False,
                    separators=(",", ":"),
                ),
                file=sys.stderr,
                flush=True,
            )
            raise SystemExit(1) from exc
        return
    try:
        raw = sys.stdin.buffer.read(_MAX_INPUT_BYTES + 1)
        if not raw or len(raw) > _MAX_INPUT_BYTES:
            raise ValueError("Agent Run input is empty or too large")
        decoded = json.loads(raw)
        if not isinstance(decoded, dict):
            raise ValueError("Agent Run input must be a JSON object")
        result = run_once(cast(dict[str, Any], decoded))
        print(
            json.dumps(result, ensure_ascii=False, separators=(",", ":")),
            flush=True,
        )
    except Exception as exc:
        print(
            "agent runtime error:"
            + json.dumps(
                _error_envelope(exc),
                ensure_ascii=False,
                separators=(",", ":"),
            ),
            file=sys.stderr,
            flush=True,
        )
        raise SystemExit(1) from exc


def _required_string(value: Mapping[str, Any], key: str) -> str:
    result = value.get(key)
    if not isinstance(result, str) or not result.strip():
        raise ValueError(f"Agent Run requires {key}")
    return result.strip()


def _optional_string(value: Mapping[str, Any], key: str) -> str:
    result = value.get(key, "")
    if not isinstance(result, str):
        return ""
    return result.strip()


def _required_positive_int(value: Mapping[str, Any], key: str) -> int:
    result = value.get(key)
    if not isinstance(result, int) or isinstance(result, bool) or result <= 0:
        raise ValueError(f"Agent Run requires positive {key}")
    return result


def _module(value: Any) -> ModuleKey:
    if value not in ("companion", "life", "work"):
        raise ValueError("Agent Run module must be companion, life, or work")
    return cast(ModuleKey, value)


def _json_value(value: Any) -> Any:
    try:
        json.dumps(value)
    except (TypeError, ValueError):
        return None
    return value


def _validate_graph_identity(run: Mapping[str, Any]) -> None:
    graph_name = run.get("graph_name")
    graph_version = run.get("graph_version")
    if isinstance(graph_name, str) and graph_name.strip() and graph_name != GRAPH_NAME:
        raise ValueError(f"Agent Run graph_name {graph_name!r} is incompatible with {GRAPH_NAME!r}")
    if isinstance(graph_version, str) and graph_version.strip() and graph_version != GRAPH_VERSION:
        raise ValueError(
            f"Agent Run graph_version {graph_version!r} is incompatible with {GRAPH_VERSION!r}"
        )


def _governance_output(result: Mapping[str, Any]) -> dict[str, Any]:
    raw_events = result.get("model_events")
    public_events: list[dict[str, Any]] = []
    if isinstance(raw_events, list):
        for item in raw_events:
            if not isinstance(item, dict):
                continue
            public_events.append(
                {
                    key: item.get(key)
                    for key in (
                        "graph_node",
                        "role",
                        "status",
                        "provider",
                        "requested_model",
                        "returned_model",
                        "prompt_tokens",
                        "completion_tokens",
                        "cached_tokens",
                        "reasoning_tokens",
                        "cost_micros",
                        "latency_ms",
                        "timeout_ms",
                        "reasoning_effort",
                        "error_status",
                        "retryable",
                        "retry_after",
                        "contract_valid",
                        "contract_error",
                    )
                }
            )
    return {
        "graph": {
            "name": _optional_string(result, "graph_name") or GRAPH_NAME,
            "version": _optional_string(result, "graph_version") or GRAPH_VERSION,
            "tool_catalog_fingerprint": _optional_string(result, "tool_catalog_fingerprint"),
        },
        "node_contracts": _json_value(result.get("node_contracts")),
        "budget": {
            "limits": _json_value(result.get("budget_limits")),
            "usage": _json_value(result.get("budget_usage")),
        },
        "model": {
            "manifest": _json_value(result.get("model_manifest")),
            "calls": public_events,
            "default_config_version": DEFAULT_MODEL_CONFIG_VERSION,
        },
        "observability": {
            "node_trace": _json_value(result.get("node_trace")),
            "repair_history": _json_value(result.get("repair_history")),
            "repair_fingerprints": _json_value(result.get("repair_fingerprints")),
            "document_processing": _json_value(result.get("document_processing")),
        },
        "recovery": _json_value(result.get("recovery")),
    }


def _error_envelope(exc: Exception) -> dict[str, Any]:
    message = str(exc).strip() or type(exc).__name__
    if len(message) > 1000:
        message = message[:1000]
    if isinstance(exc, OpenRouterError):
        code = "model_authentication" if exc.status_code in (401, 403) else "model_unavailable"
        return {
            "code": code,
            "retryable": exc.retryable,
            "provider_status": exc.status_code,
            "retry_after": exc.retry_after[:128],
            "message": message,
        }
    if isinstance(exc, ToolGatewayHTTPError):
        return {
            "code": "tool_gateway",
            "retryable": exc.retryable,
            "provider_status": exc.status_code,
            "retry_after": exc.retry_after[:128],
            "message": message,
        }
    if isinstance(exc, ValueError):
        return {"code": "runtime_contract", "retryable": False, "message": message}
    if isinstance(exc, RuntimeError):
        retryable = "unavailable" in message.lower() or "timed out" in message.lower()
        return {
            "code": "tool_gateway" if "gateway" in message.lower() else "runtime_error",
            "retryable": retryable,
            "message": message,
        }
    return {"code": "runtime_error", "retryable": True, "message": message}


if __name__ == "__main__":
    main()
