from __future__ import annotations

import hashlib
import json
import os
import re
import sys
from dataclasses import replace
from typing import Any, BinaryIO, Callable, Mapping, cast

from ai_companion_worker.agent_checkpoint import scoped_dsn
from ai_companion_worker.agent_gateway_client import (
    HTTPToolGateway,
    ToolGatewayHTTPError,
)
from ai_companion_worker.agent_governance import (
    BudgetPolicy,
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
from ai_companion_worker.declarative_agent import build_declarative_graph
from ai_companion_worker.langfuse_observability import (
    LangfuseSettings,
    LangfuseTelemetry,
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
        del module, role, prompt_template, context
        if node_type == "router":
            preferred = next(
                (
                    condition
                    for condition in ("respond", "direct", "default", "stop")
                    if condition in conditions
                ),
                conditions[0] if conditions else "",
            )
            return {"condition": preferred}
        return {"response": f"开发环境 Agent 已接收：{message}"}

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
        del module
        if context.get("declarative_tool_node"):
            definitions = context.get("tools")
            if isinstance(definitions, list) and definitions:
                first = definitions[0]
                if isinstance(first, Mapping) and isinstance(first.get("name"), str):
                    return ModelDecision(
                        intent=str(first["name"]),
                        tool_name=str(first["name"]),
                        tool_arguments={},
                    )
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
    telemetry: LangfuseTelemetry | None = None,
) -> dict[str, Any]:
    run_id = _required_string(run, "id")
    _validate_graph_identity(run)
    if runtime is None:
        policy, recursion_limit = _agent_runtime_policy(run)
        runtime = _build_runtime(
            run,
            checkpointer=checkpointer,
            decisions=decisions,
            tools=tools,
            policy=policy,
            recursion_limit=recursion_limit,
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
    trusted_context["tools"] = [
        dict(item) for item in _filter_agent_tool_definitions(run, definitions)
    ]
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
    active_telemetry = telemetry or LangfuseTelemetry(LangfuseSettings())
    with active_telemetry.observe_run(run) as langfuse_run:
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
            response = {
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
                    "agent_definition": _agent_definition_output(run),
                    **_governance_output(result),
                },
            }
        else:
            status = "completed"
            response = {
                "status": status,
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
                    "agent_definition": _agent_definition_output(run),
                    **_governance_output(result),
                },
            }
        langfuse_run.finish(result, status=status)
        _attach_langfuse_reference(response, langfuse_run.reference())
        return response


def run_once(run: Mapping[str, Any]) -> dict[str, Any]:
    from langgraph.checkpoint.postgres import PostgresSaver

    telemetry = LangfuseTelemetry.from_env()
    try:
        _validate_graph_identity(run)
        gateway = HTTPToolGateway.from_env()
        gateway.bind_revision(_required_positive_int(run, "revision"))
        gateway.bind_trace_context(run)
        run_id = _required_string(run, "id")
        # Tool definitions are checkpointed with the initial trusted context. A
        # durable resume only needs the pending checkpoint and its resolution, so
        # avoid an extra gateway round trip on every tool/approval recovery.
        definitions = gateway.definitions(run_id) if _requires_tool_definitions(run) else []
        decisions = _decision_port_for_run(run, telemetry=telemetry)
        _bind_trace_context(decisions, run)
        policy, recursion_limit = _agent_runtime_policy(run)
        dsn = scoped_dsn(os.environ.get("LANGGRAPH_POSTGRES_DSN", ""))
        with PostgresSaver.from_conn_string(dsn) as checkpointer:
            runtime = _build_runtime(
                run,
                checkpointer=checkpointer,
                decisions=decisions,
                tools=gateway,
                policy=policy,
                recursion_limit=recursion_limit,
            )
            return execute_run(
                run,
                checkpointer=checkpointer,
                decisions=decisions,
                tools=gateway,
                definitions=definitions,
                runtime=runtime,
                telemetry=telemetry,
            )
    finally:
        telemetry.flush()
        telemetry.shutdown()


def _requires_tool_definitions(run: Mapping[str, Any]) -> bool:
    return not isinstance(run.get("resume_resolution"), dict)


def _decision_port_from_env(
    environ: Mapping[str, str] | None = None,
    *,
    telemetry: LangfuseTelemetry | None = None,
) -> DecisionPort:
    environment = os.environ if environ is None else environ
    provider = environment.get("MODEL_PROVIDER", "development").strip().lower()
    if provider == "development":
        return DevelopmentDecisionPort()
    if provider == "openrouter":
        return OpenRouterDecisionPort.from_env(environment, telemetry=telemetry)
    raise ValueError("Agent runtime MODEL_PROVIDER must be development or openrouter")


def _decision_port_for_run(
    run: Mapping[str, Any],
    *,
    telemetry: LangfuseTelemetry | None = None,
) -> DecisionPort:
    environment = _model_environment_for_run(run)
    return _decision_port_from_env(environment, telemetry=telemetry)


def _model_environment_for_run(run: Mapping[str, Any]) -> Mapping[str, str] | None:
    raw_snapshot = run.get("model_profile_snapshot")
    if raw_snapshot is None:
        return None
    if not isinstance(raw_snapshot, Mapping):
        raise ValueError("Agent Run model_profile_snapshot must be an object")
    variables = raw_snapshot.get("variables")
    if not isinstance(variables, Mapping) or not variables:
        raise ValueError("Agent Run model profile snapshot requires variables")
    version_id = _required_string(raw_snapshot, "version_id")
    config_version = _required_string(raw_snapshot, "config_version")
    fingerprint = _required_string(raw_snapshot, "fingerprint").lower()
    revision = _required_positive_int(raw_snapshot, "revision")
    if not re.fullmatch(r"[0-9a-f]{64}", fingerprint):
        raise ValueError("Agent Run model profile fingerprint is invalid")
    expected = {
        "model_profile_version_id": version_id,
        "model_profile_config_version": config_version,
        "model_profile_fingerprint": fingerprint,
        "model_profile_revision": revision,
    }
    for name, value in expected.items():
        if run.get(name) != value:
            raise ValueError(f"Agent Run {name} does not match its immutable snapshot")
    environment = dict(os.environ)
    for raw_name, raw_value in variables.items():
        if not isinstance(raw_name, str) or not isinstance(raw_value, str):
            raise ValueError("Agent Run model profile variables must be strings")
        name = raw_name.strip()
        upper = name.upper()
        if (
            not name.startswith("MODEL_")
            or not re.fullmatch(r"[A-Z0-9_]{1,128}", name)
            or upper == "MODEL_API_KEY"
            or upper.endswith(("_SECRET", "_PASSWORD", "_ACCESS_TOKEN", "_AUTH_TOKEN", "_CREDENTIAL"))
            or len(raw_value) > 4096
        ):
            raise ValueError("Agent Run model profile contains an unsafe runtime variable")
        environment[name] = raw_value
    if environment.get("MODEL_CONFIG_VERSION") != config_version:
        raise ValueError("Agent Run model profile config version is inconsistent")
    return environment


def _model_runtime_key(run: Mapping[str, Any]) -> str:
    snapshot = run.get("model_profile_snapshot")
    if isinstance(snapshot, Mapping):
        # Catalog-derived limits can change without publishing a new profile.
        # Cache by the actual captured variables as well as the profile identity.
        variables = json.dumps(snapshot.get("variables", {}), sort_keys=True, separators=(",", ":"))
        return (
            "snapshot:" + _required_string(snapshot, "fingerprint").lower()
            + ":" + hashlib.sha256(variables.encode("utf-8")).hexdigest()
        )
    return "bootstrap:" + os.getenv("MODEL_PROVIDER", "development").strip().lower()


def _agent_runtime_key(run: Mapping[str, Any]) -> str:
    snapshot = run.get("agent_definition_snapshot")
    if isinstance(snapshot, Mapping):
        return "snapshot:" + _required_string(snapshot, "fingerprint").lower()
    return "builtin"


def _agent_definition_for_run(run: Mapping[str, Any]) -> Mapping[str, Any] | None:
    raw_snapshot = run.get("agent_definition_snapshot")
    if raw_snapshot is None:
        return None
    if not isinstance(raw_snapshot, Mapping):
        raise ValueError("Agent Run agent_definition_snapshot must be an object")
    expected: dict[str, str | int] = {
        "agent_definition_key": _required_string(raw_snapshot, "key"),
        "agent_definition_version_id": _required_string(raw_snapshot, "version_id"),
        "agent_definition_version": _required_positive_int(raw_snapshot, "version"),
        "agent_definition_revision": _required_positive_int(raw_snapshot, "revision"),
        "agent_definition_fingerprint": _required_string(
            raw_snapshot, "fingerprint"
        ).lower(),
        "agent_definition_model_profile": _required_string(raw_snapshot, "model_profile"),
    }
    fingerprint = str(expected["agent_definition_fingerprint"])
    if not re.fullmatch(r"[0-9a-f]{64}", fingerprint):
        raise ValueError("Agent Run Agent definition fingerprint is invalid")
    for name, value in expected.items():
        if run.get(name) != value:
            raise ValueError(f"Agent Run {name} does not match its immutable snapshot")
    definition = raw_snapshot.get("definition")
    if not isinstance(definition, Mapping):
        raise ValueError("Agent Run Agent definition requires an object definition")
    modules = definition.get("modules")
    if not isinstance(modules, list) or run.get("module") not in modules:
        raise ValueError("Agent Run module is not assigned to its Agent definition")
    if definition.get("model_profile") != expected["agent_definition_model_profile"]:
        raise ValueError("Agent Run Agent definition model profile is inconsistent")
    model_profile_key = run.get("model_profile_key")
    if (
        isinstance(model_profile_key, str)
        and model_profile_key
        and model_profile_key != expected["agent_definition_model_profile"]
    ):
        raise ValueError("Agent Run model profile does not match its Agent definition")
    return definition


def _agent_runtime_policy(run: Mapping[str, Any]) -> tuple[BudgetPolicy, int]:
    base = BudgetPolicy.from_env()
    definition = _agent_definition_for_run(run)
    if definition is None:
        return base, 256
    budget = definition.get("budget")
    if not isinstance(budget, Mapping):
        raise ValueError("Agent Run Agent definition requires a budget")
    max_steps = _bounded_agent_budget(budget, "max_steps", 1, 128)
    max_model_calls = _bounded_agent_budget(budget, "max_model_calls", 1, 64)
    max_tool_calls = _bounded_agent_budget(budget, "max_tool_calls", 0, 64)
    max_total_tokens = _bounded_agent_budget(budget, "max_total_tokens", 64, 2_000_000)
    _bounded_agent_budget(budget, "timeout_ms", 1_000, 900_000)
    policy = replace(
        base,
        max_actions=min(base.max_actions, max(1, max_tool_calls), max_steps),
        max_model_calls=min(base.max_model_calls, max_model_calls),
        max_prompt_tokens=min(base.max_prompt_tokens, max_total_tokens),
        max_completion_tokens=min(base.max_completion_tokens, max_total_tokens),
        max_total_tokens=min(base.max_total_tokens, max_total_tokens),
    )
    policy.validate()
    # A declarative tool node expands into checkpointed prepare, approval,
    # commit, observe and wait phases. The state-level max_steps remains the
    # user-visible budget; this larger recursion ceiling only accommodates
    # those durable structural transitions.
    return policy, min(1024, max_steps * 8 + 8)


def _bounded_agent_budget(
    budget: Mapping[str, Any], key: str, minimum: int, maximum: int
) -> int:
    value = budget.get(key)
    if (
        not isinstance(value, int)
        or isinstance(value, bool)
        or not minimum <= value <= maximum
    ):
        raise ValueError(f"Agent Run Agent definition budget {key} is invalid")
    return value


def _agent_allowed_tools(run: Mapping[str, Any]) -> set[str] | None:
    definition = _agent_definition_for_run(run)
    if definition is None:
        return None
    nodes = definition.get("nodes")
    if not isinstance(nodes, list):
        raise ValueError("Agent Run Agent definition requires nodes")
    allowed: set[str] = set()
    for node in nodes:
        if not isinstance(node, Mapping):
            raise ValueError("Agent Run Agent definition contains an invalid node")
        if node.get("type") != "tool":
            continue
        tools = node.get("tools")
        if not isinstance(tools, list) or not tools:
            raise ValueError("Agent Run Agent tool node requires an allowlist")
        for tool in tools:
            if not isinstance(tool, str) or not re.fullmatch(r"[a-z0-9_-]{1,96}", tool):
                raise ValueError("Agent Run Agent definition contains an invalid tool")
            allowed.add(tool)
    return allowed


def _filter_agent_tool_definitions(
    run: Mapping[str, Any], definitions: list[dict[str, Any]]
) -> list[dict[str, Any]]:
    allowed = _agent_allowed_tools(run)
    if allowed is None:
        return definitions
    return [item for item in definitions if item.get("name") in allowed]


def _agent_definition_output(run: Mapping[str, Any]) -> dict[str, Any]:
    if _agent_definition_for_run(run) is None:
        return {"source": "builtin"}
    return {
        "source": "agent_studio",
        "compiler": "agent-studio-langgraph/v1",
        "key": run.get("agent_definition_key"),
        "version_id": run.get("agent_definition_version_id"),
        "version": run.get("agent_definition_version"),
        "revision": run.get("agent_definition_revision"),
        "fingerprint": run.get("agent_definition_fingerprint"),
    }


def _build_runtime(
    run: Mapping[str, Any],
    *,
    checkpointer: Any,
    decisions: DecisionPort,
    tools: ToolGateway,
    policy: BudgetPolicy,
    recursion_limit: int,
) -> AgentRuntime:
    definition = _agent_definition_for_run(run)
    if definition is None:
        return AgentRuntime(
            build_graph(
                checkpointer=checkpointer,
                decisions=decisions,
                tools=tools,
                budget_policy=policy,
            ),
            recursion_limit=recursion_limit,
        )
    identity = {
        "key": _required_string(run, "agent_definition_key"),
        "version_id": _required_string(run, "agent_definition_version_id"),
        "fingerprint": _required_string(run, "agent_definition_fingerprint").lower(),
    }
    return AgentRuntime(
        build_declarative_graph(
            definition=definition,
            definition_identity=identity,
            checkpointer=checkpointer,
            decisions=decisions,
            tools=tools,
            budget_policy=policy,
        ),
        recursion_limit=recursion_limit,
        checkpoint_identity={
            "agent_definition_key": identity["key"],
            "agent_definition_version_id": identity["version_id"],
            "agent_definition_fingerprint": identity["fingerprint"],
        },
    )


def serve() -> None:
    from langgraph.checkpoint.postgres import PostgresSaver

    telemetry = LangfuseTelemetry.from_env()
    try:
        gateway = HTTPToolGateway.from_env()
        dsn = scoped_dsn(os.environ.get("LANGGRAPH_POSTGRES_DSN", ""))
        with PostgresSaver.from_conn_string(dsn) as checkpointer:
            runtimes: dict[str, tuple[DecisionPort, AgentRuntime]] = {}

            def runtime_for(run: Mapping[str, Any]) -> tuple[DecisionPort, AgentRuntime]:
                environment = _model_environment_for_run(run)
                policy, recursion_limit = _agent_runtime_policy(run)
                key = _model_runtime_key(run) + "|agent:" + _agent_runtime_key(run)
                cached = runtimes.get(key)
                if cached is not None:
                    return cached
                decisions = _decision_port_from_env(environment, telemetry=telemetry)
                runtime = _build_runtime(
                    run,
                    checkpointer=checkpointer,
                    decisions=decisions,
                    tools=gateway,
                    policy=policy,
                    recursion_limit=recursion_limit,
                )
                if len(runtimes) >= 16:
                    runtimes.pop(next(iter(runtimes)))
                runtimes[key] = (decisions, runtime)
                return decisions, runtime

            def handle(run: Mapping[str, Any]) -> dict[str, Any]:
                _validate_graph_identity(run)
                decisions, runtime = runtime_for(run)
                gateway.bind_revision(_required_positive_int(run, "revision"))
                gateway.bind_trace_context(run)
                _bind_trace_context(decisions, run)
                run_id = _required_string(run, "id")
                definitions = (
                    gateway.definitions(run_id) if _requires_tool_definitions(run) else []
                )
                try:
                    return execute_run(
                        run,
                        checkpointer=checkpointer,
                        decisions=decisions,
                        tools=gateway,
                        definitions=definitions,
                        runtime=runtime,
                        telemetry=telemetry,
                    )
                finally:
                    consume = getattr(decisions, "consume_observability", None)
                    if callable(consume):
                        consume()

            _write_ready(sys.stdout.buffer)
            _serve_requests(sys.stdin.buffer, sys.stdout.buffer, handle)
    finally:
        telemetry.shutdown()


def _bind_trace_context(target: Any, run: Mapping[str, Any]) -> None:
    binder = getattr(target, "bind_trace_context", None)
    if callable(binder):
        binder(run)


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
                        "parallel_task_id", "arbitration_id",
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
                        "prompt_token_upper_bound",
                        "context_manifest",
                    )
                }
            )
    return {
        "parallel_plan": _json_value(result.get("parallel_plan")),
        "research_results": _json_value(result.get("research_results")),
        "specialist_review": _json_value(result.get("specialist_review")),
        "arbitrations": _json_value(result.get("arbitrations")),
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


def _attach_langfuse_reference(response: dict[str, Any], reference: Mapping[str, Any]) -> None:
    output = response.get("output")
    if not isinstance(output, dict):
        return
    observability = output.get("observability")
    if not isinstance(observability, dict):
        observability = {}
        output["observability"] = observability
    observability["langfuse"] = dict(reference)


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
