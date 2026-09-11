from __future__ import annotations

import json
import sys
from collections.abc import Mapping
from dataclasses import dataclass
from typing import Any, cast

from langgraph.checkpoint.memory import InMemorySaver

from ai_companion_worker.agent_governance import BudgetPolicy
from ai_companion_worker.agent_runtime import (
	AgentPlan,
    AgentRuntime,
    DecisionPort,
    ModelBudgetExceeded,
    ModelDecision,
    ModuleKey,
    ToolOutcome,
    ToolPreparation,
    interrupt_payloads,
)
from ai_companion_worker.declarative_agent import (
    CompiledAgentDefinition,
    build_declarative_graph,
    compile_agent_definition,
)

_SCHEMA_VERSION = "agent-studio-synthetic-run/v1"
_MAX_TEXT = 12_000
_ALLOWED_SCENARIO_FIELDS = {"module", "user_message", "routes", "models", "tools"}
_ALLOWED_MODEL_FIELDS = {"response", "prompt_tokens", "completion_tokens"}
_ALLOWED_ROUTE_FIELDS = {
    "condition",
    "response",
    "prompt_tokens",
    "completion_tokens",
}
_ALLOWED_TOOL_FIELDS = {
    "tool_name",
    "arguments",
    "parameters",
    "requires_composition",
    "requires_approval",
    "approval",
    "risk_level",
    "result_status",
    "wait_result",
    "response",
    "prompt_tokens",
    "completion_tokens",
}


@dataclass(frozen=True)
class SandboxScenario:
    module: ModuleKey
    user_message: str
    routes: Mapping[str, Mapping[str, Any]]
    models: Mapping[str, Mapping[str, Any]]
    tools: Mapping[str, Mapping[str, Any]]


class SyntheticDecisions:
    def __init__(self, scenario: SandboxScenario) -> None:
        self._scenario = scenario
        self._events: list[dict[str, Any]] = []
        self.selections: list[dict[str, Any]] = []

    def model_manifest(self) -> dict[str, Any]:
        return {
            "provider": "synthetic",
            "config_version": "agent-studio-sandbox-v1",
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
        del module, message, prompt_template
        node = _node_key(context)
        fixture = (
            self._scenario.routes.get(node, {})
            if node_type == "router"
            else self._scenario.models.get(node, {})
        )
        prompt_tokens = _integer(fixture.get("prompt_tokens"), 0)
        completion_tokens = _integer(fixture.get("completion_tokens"), 0)
        _require_allowance(context, prompt_tokens, completion_tokens)
        self._queue_event(role, prompt_tokens, completion_tokens)
        if node_type == "router":
            condition = str(fixture.get("condition") or (conditions[0] if conditions else ""))
            if condition not in conditions:
                raise ValueError(
                    f"synthetic route for {node} is not one of its compiled conditions"
                )
            return {"condition": condition, "response": str(fixture.get("response") or "")}
        response = str(fixture.get("response") or f"合成模型输出：{node} 已完成。")
        if not response.strip():
            raise ValueError(f"synthetic model response for {node} must not be empty")
        return {"response": response}

    def decide(
        self,
        *,
        module: ModuleKey,
        message: str,
        context: Mapping[str, Any],
    ) -> ModelDecision:
        del module, message
        node = str(context.get("declarative_tool_node") or "")
        fixture = self._scenario.tools.get(node, {})
        definitions = context.get("tools")
        if not isinstance(definitions, list) or not definitions:
            raise ValueError(f"synthetic tool node {node} has no trusted catalog fixture")
        default_name = str(cast(Mapping[str, Any], definitions[0]).get("name") or "")
        tool_name = str(fixture.get("tool_name") or default_name)
        prompt_tokens = _integer(fixture.get("prompt_tokens"), 0)
        completion_tokens = _integer(fixture.get("completion_tokens"), 0)
        _require_allowance(context, prompt_tokens, completion_tokens)
        self._queue_event("router", prompt_tokens, completion_tokens)
        arguments = fixture.get("arguments", {})
        if not isinstance(arguments, Mapping):
            raise TypeError(f"synthetic tool arguments for {node} must be an object")
        requires_composition = fixture.get("requires_composition", False)
        if not isinstance(requires_composition, bool):
            raise TypeError(f"synthetic requires_composition for {node} must be a boolean")
        selection = {
            "node": node,
            "tool_name": tool_name,
            "arguments": dict(arguments),
            "synthetic": True,
        }
        self.selections.append(selection)
        return ModelDecision(
            intent=tool_name,
            tool_name=tool_name,
            tool_arguments=dict(arguments),
            requires_argument_composition=requires_composition,
        )

    def compose_arguments(
        self,
        *,
        module: ModuleKey,
        message: str,
        tool_name: str,
        context: Mapping[str, Any],
    ) -> Mapping[str, Any]:
        del module, message, tool_name
        node = _tool_node_key(context)
        fixture = self._scenario.tools.get(node, {})
        prompt_tokens = _integer(fixture.get("prompt_tokens"), 0)
        completion_tokens = _integer(fixture.get("completion_tokens"), 0)
        _require_allowance(context, prompt_tokens, completion_tokens)
        self._queue_event("composer", prompt_tokens, completion_tokens)
        arguments = fixture.get("arguments", {})
        if not isinstance(arguments, Mapping):
            raise TypeError(f"synthetic tool arguments for {node} must be an object")
        return dict(arguments)

    def consume_observability(self) -> list[dict[str, Any]]:
        events, self._events = self._events, []
        return events

    def plan(self, **_values: Any) -> AgentPlan:
        raise AssertionError("the declarative sandbox cannot invoke the built-in planner")

    def respond(self, **_values: Any) -> str:
        raise AssertionError("the declarative sandbox cannot invoke the built-in responder")

    def _queue_event(self, role: str, prompt_tokens: int, completion_tokens: int) -> None:
        self._events.append(
            {
                "kind": "model_call",
                "role": role,
                "status": "succeeded",
                "provider": "synthetic",
                "requested_model": "synthetic/fixture",
                "returned_model": "synthetic/fixture",
                "prompt_tokens": prompt_tokens,
                "completion_tokens": completion_tokens,
                "cached_tokens": 0,
                "reasoning_tokens": 0,
                "cost_micros": 0,
                "latency_ms": 0,
                "error_status": 0,
                "retryable": False,
            }
        )


class SyntheticTools:
    def __init__(self, scenario: SandboxScenario) -> None:
        self._scenario = scenario
        self.calls: list[dict[str, Any]] = []

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
        del run_id, user_id, module
        node = _node_from_idempotency(idempotency_key)
        fixture = self._scenario.tools.get(node, {})
        risk = str(fixture.get("risk_level") or "none")
        requires_approval = fixture.get("requires_approval", risk in ("medium", "high"))
        if not isinstance(requires_approval, bool):
            raise TypeError(f"synthetic requires_approval for {node} must be a boolean")
        status = "requires_confirmation" if requires_approval else "completed"
        response = str(fixture.get("response") or f"合成工具 {tool_name} 已完成。")
        data = _tool_data(node, fixture, waiting=not requires_approval)
        self.calls.append(
            {
                "node": node,
                "stage": "prepare",
                "tool_name": tool_name,
                "status": status,
                "risk_level": risk,
                "arguments": dict(arguments),
                "external_call": False,
            }
        )
        return ToolPreparation(
            status=cast(Any, status),
            tool_name=tool_name,
            response=response,
            risk_level=cast(Any, risk),
            summary=f"合成审批：{tool_name}",
            confirmation_token=f"synthetic:{node}",
            normalized_arguments=dict(arguments),
            data=data,
        )

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
        del run_id, user_id, module
        node = _node_from_idempotency(idempotency_key)
        fixture = self._scenario.tools.get(node, {})
        tool_name = str(preparation.get("tool_name") or "")
        self.calls.append(
            {
                "node": node,
                "stage": "commit",
                "tool_name": tool_name,
                "status": "simulated" if resolution.get("approved") is True else "rejected",
                "external_call": False,
            }
        )
        return ToolOutcome(
            response=str(fixture.get("response") or f"合成工具 {tool_name} 已完成。"),
            data=_tool_data(node, fixture, waiting=True),
        )

    def observe(
        self,
        *,
        run_id: str,
        user_id: str,
        module: ModuleKey,
        task_id: str,
    ) -> ToolOutcome:
        del run_id, user_id, module
        node = task_id.removeprefix("synthetic-task-")
        fixture = self._scenario.tools.get(node, {})
        wait_result = str(fixture.get("wait_result") or "completed")
        self.calls.append(
            {
                "node": node,
                "stage": "observe",
                "tool_name": str(fixture.get("tool_name") or ""),
                "status": wait_result,
                "external_call": False,
            }
        )
        return ToolOutcome(
            response=str(fixture.get("response") or "合成异步任务已完成。"),
            data={"kind": "skill_run", "id": task_id, "status": wait_result},
        )

    def retry_task(self, **_values: Any) -> ToolOutcome:
        raise AssertionError("the declarative sandbox does not retry production tasks")


def run_sandbox(request: Mapping[str, Any]) -> dict[str, Any]:
    _reject_unknown(request, {"definition", "identity", "scenario"}, "sandbox request")
    definition = request.get("definition")
    identity = request.get("identity")
    scenario_value = request.get("scenario", {})
    if not isinstance(definition, Mapping) or not isinstance(identity, Mapping):
        raise TypeError("sandbox request requires definition and identity objects")
    if not isinstance(scenario_value, Mapping):
        raise TypeError("sandbox scenario must be an object")
    compiled = compile_agent_definition(definition)
    scenario = _scenario(scenario_value, compiled)
    definition_identity = _identity(identity)
    decisions = SyntheticDecisions(scenario)
    tools = SyntheticTools(scenario)
    policy = BudgetPolicy(
        max_actions=compiled.max_tool_calls,
        max_model_calls=compiled.max_model_calls,
        max_prompt_tokens=compiled.max_total_tokens,
        max_completion_tokens=compiled.max_total_tokens,
        max_total_tokens=compiled.max_total_tokens,
        max_tool_resumes=16,
    )
    graph = build_declarative_graph(
        definition=definition,
        definition_identity=definition_identity,
        checkpointer=InMemorySaver(),
        decisions=cast(DecisionPort, decisions),
        tools=tools,
        budget_policy=policy,
    )
    runtime = AgentRuntime(
        graph,
        recursion_limit=1024,
        checkpoint_identity={
            "agent_definition_key": definition_identity["key"],
            "agent_definition_version_id": definition_identity["version_id"],
            "agent_definition_fingerprint": definition_identity["fingerprint"],
        },
    )
    run_id = f"sandbox-{definition_identity['fingerprint'][:16]}"
    catalog = _tool_catalog(compiled, scenario)
    result = runtime.start(
        {
            "run_id": run_id,
            "user_id": "synthetic-user",
            "conversation_id": "synthetic-conversation",
            "character_id": "synthetic-character",
            "module": scenario.module,
            "user_message": scenario.user_message,
            "context": {"tools": catalog, "synthetic": True},
        }
    )
    result = _resolve_interrupts(runtime, result, scenario)
    pending = interrupt_payloads(result)
    status = "completed"
    if pending:
        status = "waiting_approval" if pending[0].get("type") == "tool_approval" else "waiting_tool"
    return {
        "schema_version": _SCHEMA_VERSION,
        "run_id": run_id,
        "mode": "synthetic",
        "status": status,
        "outcome": str(result.get("outcome") or status),
        "response": str(result.get("response") or ""),
        "definition": {
            **definition_identity,
            "compiler": "agent-studio-langgraph/v1",
            "entry_node": compiled.entry_node,
        },
        "safety": {
            "side_effects": False,
            "production_data_access": False,
            "external_model_calls": 0,
            "external_tool_calls": 0,
        },
        "budget": {
            "limits": dict(result.get("budget_limits") or {}),
            "usage": dict(result.get("budget_usage") or {}),
        },
        "node_outputs": dict(result.get("node_outputs") or {}),
        "node_trace": list(result.get("node_trace") or []),
        "model_calls": list(result.get("model_events") or []),
        "tool_calls": tools.calls,
        "tool_selections": decisions.selections,
        "interrupts": [_public_interrupt(item) for item in pending],
        "recovery": dict(result.get("recovery") or {}),
    }


def _resolve_interrupts(
    runtime: AgentRuntime,
    result: dict[str, Any],
    scenario: SandboxScenario,
) -> dict[str, Any]:
    for _ in range(32):
        pending = interrupt_payloads(result)
        if not pending:
            return result
        interrupt_value = pending[0]
        kind = interrupt_value.get("type")
        if kind == "tool_approval":
            node = _node_from_synthetic_token(str(interrupt_value.get("confirmation_token") or ""))
            decision = str(scenario.tools.get(node, {}).get("approval") or "pause")
            if decision in ("pause", "running"):
                return result
            result = runtime.resume(
                str(result.get("run_id") or ""), {"approved": decision == "approve"}
            )
            continue
        if kind == "tool_wait":
            task_id = str(interrupt_value.get("task_id") or "")
            node = task_id.removeprefix("synthetic-task-")
            decision = str(scenario.tools.get(node, {}).get("wait_result") or "completed")
            if decision == "pause":
                return result
            result = runtime.resume(
                str(result.get("run_id") or ""),
                {"type": "tool_poll", "task_id": task_id},
            )
            continue
        raise ValueError("sandbox runtime returned an unsupported interrupt")
    raise ValueError("sandbox interrupt budget exceeded")


def _scenario(value: Mapping[str, Any], compiled: CompiledAgentDefinition) -> SandboxScenario:
    _reject_unknown(value, _ALLOWED_SCENARIO_FIELDS, "sandbox scenario")
    raw_module = value.get("module", compiled.modules[0])
    if raw_module not in compiled.modules:
        raise ValueError("sandbox module is not assigned to this Agent definition")
    message = str(value.get("user_message") or "合成测试请求").strip()
    if not message or len(message) > _MAX_TEXT:
        raise ValueError("sandbox user_message is empty or too long")
    routes = _fixture_map(value.get("routes", {}), compiled, "router", _ALLOWED_ROUTE_FIELDS)
    models = _fixture_map(value.get("models", {}), compiled, "model", _ALLOWED_MODEL_FIELDS)
    tools = _fixture_map(value.get("tools", {}), compiled, "tool", _ALLOWED_TOOL_FIELDS)
    for node, fixture in routes.items():
        allowed = {edge.condition for edge in compiled.outgoing(node)}
        condition = fixture.get("condition")
        if condition is not None and condition not in allowed:
            raise ValueError(f"synthetic route for {node} is not one of its compiled conditions")
        _validate_tokens(fixture, f"route fixture {node}")
    for node, fixture in models.items():
        response = fixture.get("response")
        if response is not None and (
            not isinstance(response, str)
            or not response.strip()
            or len(response) > _MAX_TEXT
        ):
            raise ValueError(f"model fixture {node} has an invalid response")
        _validate_tokens(fixture, f"model fixture {node}")
    for node, fixture in tools.items():
        _validate_tool_fixture(node, fixture, compiled)
    return SandboxScenario(
        module=cast(ModuleKey, raw_module),
        user_message=message,
        routes=routes,
        models=models,
        tools=tools,
    )


def _fixture_map(
    value: Any,
    compiled: CompiledAgentDefinition,
    expected_type: str,
    allowed_fields: set[str],
) -> dict[str, Mapping[str, Any]]:
    if not isinstance(value, Mapping) or len(value) > 64:
        raise ValueError(f"sandbox {expected_type} fixtures must be a bounded object")
    fixtures: dict[str, Mapping[str, Any]] = {}
    for raw_node, raw_fixture in value.items():
        if not isinstance(raw_node, str) or not isinstance(raw_fixture, Mapping):
            raise TypeError(f"sandbox {expected_type} fixtures are invalid")
        node = compiled.nodes.get(raw_node)
        if node is None or node.type != expected_type:
            raise ValueError(f"sandbox fixture {raw_node} does not target a {expected_type} node")
        _reject_unknown(raw_fixture, allowed_fields, f"sandbox fixture {raw_node}")
        fixtures[raw_node] = raw_fixture
    return fixtures


def _validate_tool_fixture(
    node: str,
    fixture: Mapping[str, Any],
    compiled: CompiledAgentDefinition,
) -> None:
    tool_name = str(fixture.get("tool_name") or compiled.nodes[node].tools[0])
    if tool_name not in compiled.nodes[node].tools:
        raise ValueError(f"synthetic tool for {node} is outside its compiled allowlist")
    for key in ("arguments", "parameters"):
        raw = fixture.get(key, {})
        if not isinstance(raw, Mapping):
            raise TypeError(f"tool fixture {node} {key} must be an object")
    for key in ("requires_composition", "requires_approval"):
        raw = fixture.get(key)
        if raw is not None and not isinstance(raw, bool):
            raise ValueError(f"tool fixture {node} {key} must be a boolean")
    if str(fixture.get("approval") or "pause") not in ("approve", "reject", "pause"):
        raise ValueError(f"tool fixture {node} approval is invalid")
    if str(fixture.get("risk_level") or "none") not in ("none", "low", "medium", "high"):
        raise ValueError(f"tool fixture {node} risk_level is invalid")
    if str(fixture.get("result_status") or "completed") not in ("completed", "failed", "waiting"):
        raise ValueError(f"tool fixture {node} result_status is invalid")
    if str(fixture.get("wait_result") or "completed") not in (
        "completed",
        "failed",
        "running",
        "pause",
    ):
        raise ValueError(f"tool fixture {node} wait_result is invalid")
    response = fixture.get("response")
    if response is not None and (
        not isinstance(response, str)
        or not response.strip()
        or len(response) > _MAX_TEXT
    ):
        raise ValueError(f"tool fixture {node} response is invalid")
    _validate_tokens(fixture, f"tool fixture {node}")
    encoded = json.dumps(
        {"arguments": fixture.get("arguments", {}), "parameters": fixture.get("parameters", {})},
        ensure_ascii=False,
        separators=(",", ":"),
    ).encode()
    if len(encoded) > 64 * 1024:
        raise ValueError(f"tool fixture {node} is too large")


def _tool_catalog(
    compiled: CompiledAgentDefinition,
    scenario: SandboxScenario,
) -> list[dict[str, Any]]:
    catalog: dict[str, dict[str, Any]] = {}
    for node in compiled.nodes.values():
        if node.type != "tool":
            continue
        fixture = scenario.tools.get(node.key, {})
        selected = str(fixture.get("tool_name") or node.tools[0])
        for tool_name in node.tools:
            tool_fixture = fixture if tool_name == selected else {}
            catalog[tool_name] = {
                "name": tool_name,
                "description": f"Synthetic fixture for {tool_name}",
                "risk_level": str(tool_fixture.get("risk_level") or "none"),
                "parameters": dict(tool_fixture.get("parameters") or {"type": "object"}),
                "compose_arguments": bool(tool_fixture.get("requires_composition", False)),
            }
    return list(catalog.values())


def _tool_data(node: str, fixture: Mapping[str, Any], *, waiting: bool) -> dict[str, Any]:
    status = str(fixture.get("result_status") or "completed")
    if waiting and status == "waiting":
        return {"kind": "skill_run", "id": f"synthetic-task-{node}", "status": "queued"}
    if status == "failed":
        return {"kind": "skill_run", "id": f"synthetic-task-{node}", "status": "failed"}
    return {"synthetic": True, "status": "completed"}


def _identity(value: Mapping[str, Any]) -> dict[str, str]:
    _reject_unknown(value, {"key", "version_id", "fingerprint"}, "sandbox identity")
    result = {
        name: str(value.get(name) or "").strip()
        for name in ("key", "version_id", "fingerprint")
    }
    if not result["key"] or not result["version_id"] or len(result["fingerprint"]) != 64:
        raise ValueError("sandbox definition identity is invalid")
    return result


def _validate_tokens(fixture: Mapping[str, Any], label: str) -> None:
    for field in ("prompt_tokens", "completion_tokens"):
        raw = fixture.get(field, 0)
        if not isinstance(raw, int) or isinstance(raw, bool) or raw < 0 or raw > 2_000_000:
            raise ValueError(f"{label} {field} is invalid")


def _require_allowance(context: Mapping[str, Any], prompt: int, completion: int) -> None:
    allowance = context.get("model_allowance")
    if not isinstance(allowance, Mapping):
        raise TypeError("sandbox model allowance is missing")
    if _integer(allowance.get("remaining_calls"), 0) < 1:
        raise ModelBudgetExceeded("模型调用预算已用完")
    if prompt + completion > _integer(allowance.get("remaining_total_tokens"), 0):
        raise ModelBudgetExceeded("合成 token 用量超过剩余总预算")
    if prompt > _integer(allowance.get("remaining_prompt_tokens"), 0):
        raise ModelBudgetExceeded("合成 prompt token 用量超过剩余预算")
    if completion > _integer(allowance.get("remaining_completion_tokens"), 0):
        raise ModelBudgetExceeded("合成 completion token 用量超过剩余预算")


def _node_key(context: Mapping[str, Any]) -> str:
    allowance = context.get("model_allowance")
    node = str(allowance.get("graph_node") or "") if isinstance(allowance, Mapping) else ""
    return node.removeprefix("studio__")


def _tool_node_key(context: Mapping[str, Any]) -> str:
    node = _node_key(context)
    return node.removesuffix(".compose")


def _node_from_idempotency(value: str) -> str:
    marker = ":studio:"
    if marker not in value:
        raise ValueError("sandbox tool idempotency key is invalid")
    return value.split(marker, 1)[1].split(":", 1)[0]


def _node_from_synthetic_token(value: str) -> str:
    if not value.startswith("synthetic:"):
        raise ValueError("sandbox approval token is invalid")
    return value.removeprefix("synthetic:")


def _public_interrupt(value: Mapping[str, Any]) -> dict[str, Any]:
    return {
        key: item
        for key, item in value.items()
        if key not in ("confirmation_token", "interrupt_id")
    }


def _reject_unknown(value: Mapping[str, Any], allowed: set[str], label: str) -> None:
    unknown = sorted(str(key) for key in value if key not in allowed)
    if unknown:
        raise ValueError(f"{label} contains unsupported fields: {', '.join(unknown)}")


def _integer(value: Any, default: int) -> int:
    return value if isinstance(value, int) and not isinstance(value, bool) else default


def main() -> None:
    try:
        value = json.load(sys.stdin)
        if not isinstance(value, Mapping):
            raise TypeError("sandbox input must be a JSON object")
        report = run_sandbox(value)
        output: dict[str, Any] = {"ok": True, "report": report}
    except (TypeError, ValueError, json.JSONDecodeError) as exc:
        output = {
            "ok": False,
            "error": {"code": "validation", "message": str(exc) or "invalid sandbox input"},
        }
    except Exception:  # noqa: BLE001 -- the process boundary must return a safe envelope
        output = {
            "ok": False,
            "error": {"code": "runtime", "message": "synthetic Agent runtime failed"},
        }
    json.dump(output, sys.stdout, ensure_ascii=False, separators=(",", ":"))


if __name__ == "__main__":
    main()
