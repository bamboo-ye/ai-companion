from __future__ import annotations

import re
import time
from dataclasses import asdict, dataclass
from typing import Any, Callable, Mapping, TypedDict, cast

from langgraph.graph import END, START, StateGraph
from langgraph.types import Command, interrupt

from ai_companion_worker.agent_governance import (
    GRAPH_NAME,
    GRAPH_VERSION,
    BudgetPolicy,
    apply_model_events,
    initial_budget_usage,
    model_budget_exhaustion,
    model_config_version,
    tool_catalog_fingerprint,
)
from ai_companion_worker.agent_runtime import (
    DecisionPort,
    ModelBudgetExceeded,
    ModuleKey,
    ToolGateway,
    ToolOutcome,
    ToolPreparation,
    _decision_manifest,
    _validate_input,
    tool_allowed,
    validate_arguments_against_schema,
)

_KEY = re.compile(r"[a-z0-9_-]{1,96}\Z")
_MODEL_ROLES = frozenset(
    (
        "planner",
        "router",
        "composer",
        "assessor",
        "responder",
        "companion_responder",
        "repairer",
    )
)


class DeclarativeAgentState(TypedDict, total=False):
    run_id: str
    user_id: str
    conversation_id: str
    character_id: str
    module: ModuleKey
    user_message: str
    context: dict[str, Any]
    graph_name: str
    graph_version: str
    agent_definition_key: str
    agent_definition_version_id: str
    agent_definition_fingerprint: str
    response: str
    outcome: str
    intent: str
    execution_mode: str
    steps: int
    action_index: int
    action_budget: int
    active_elapsed_ms: int
    timeout_ms: int
    node_outputs: dict[str, Any]
    observations: list[dict[str, Any]]
    proposed_tool: dict[str, Any]
    tool_requires_composition: bool
    preparation: dict[str, Any]
    approval: dict[str, Any]
    tool_result: dict[str, Any]
    pending_task_id: str
    task_polls: int
    route_condition: str
    budget_limits: dict[str, Any]
    budget_usage: dict[str, Any]
    model_manifest: dict[str, Any]
    model_events: list[dict[str, Any]]
    tool_catalog_fingerprint: str
    node_contracts: dict[str, dict[str, Any]]
    node_trace: list[dict[str, Any]]
    recovery: dict[str, Any]
    _studio_stop: bool


@dataclass(frozen=True)
class DeclarativeNode:
    key: str
    type: str
    model_role: str = ""
    prompt_template: str = ""
    tools: tuple[str, ...] = ()


@dataclass(frozen=True)
class DeclarativeEdge:
    source: str
    target: str
    condition: str = ""


@dataclass(frozen=True)
class CompiledAgentDefinition:
    display_name: str
    modules: tuple[ModuleKey, ...]
    model_profile: str
    entry_node: str
    nodes: Mapping[str, DeclarativeNode]
    edges: tuple[DeclarativeEdge, ...]
    max_steps: int
    max_model_calls: int
    max_tool_calls: int
    max_total_tokens: int
    timeout_ms: int

    def outgoing(self, key: str) -> tuple[DeclarativeEdge, ...]:
        return tuple(edge for edge in self.edges if edge.source == key)

    def manifest(self) -> dict[str, Any]:
        return {
            "compiler": "agent-studio-langgraph/v1",
            "entry_node": self.entry_node,
            "node_count": len(self.nodes),
            "edge_count": len(self.edges),
            "nodes": [
                {
                    "key": node.key,
                    "type": node.type,
                    "model_role": node.model_role,
                    "tools": list(node.tools),
                    "outgoing": [
                        {"to": edge.target, "condition": edge.condition}
                        for edge in self.outgoing(node.key)
                    ],
                }
                for node in self.nodes.values()
            ],
            "budget": {
                "max_steps": self.max_steps,
                "max_model_calls": self.max_model_calls,
                "max_tool_calls": self.max_tool_calls,
                "max_total_tokens": self.max_total_tokens,
                "timeout_ms": self.timeout_ms,
            },
        }


def compile_agent_definition(value: Mapping[str, Any]) -> CompiledAgentDefinition:
    """Compile and defensively validate the Go-owned declarative graph snapshot."""

    display_name = _required_string(value, "display_name", 120)
    model_profile = _required_key(value, "model_profile")
    entry_node = _required_key(value, "entry_node")
    raw_modules = value.get("modules")
    if not isinstance(raw_modules, list) or not raw_modules:
        raise ValueError("Agent definition modules must be a non-empty array")
    modules: list[ModuleKey] = []
    for raw_module in raw_modules:
        if raw_module not in ("companion", "life", "work") or raw_module in modules:
            raise ValueError("Agent definition contains an invalid or duplicate module")
        modules.append(cast(ModuleKey, raw_module))

    raw_nodes = value.get("nodes")
    if not isinstance(raw_nodes, list) or not 1 <= len(raw_nodes) <= 64:
        raise ValueError("Agent definition nodes must contain between one and 64 entries")
    nodes: dict[str, DeclarativeNode] = {}
    for raw_node in raw_nodes:
        if not isinstance(raw_node, Mapping):
            raise ValueError("Agent definition node must be an object")
        key = _required_key(raw_node, "key")
        node_type = _required_string(raw_node, "type", 16)
        if key in nodes or node_type not in ("model", "router", "tool", "end"):
            raise ValueError("Agent definition contains an invalid or duplicate node")
        model_role = str(raw_node.get("model_role") or "").strip()
        prompt_template = str(raw_node.get("prompt_template") or "").strip()
        raw_tools = raw_node.get("tools", [])
        if not isinstance(raw_tools, list):
            raise ValueError(f"Agent node {key} tools must be an array")
        node_tools: list[str] = []
        for raw_tool in raw_tools:
            if (
                not isinstance(raw_tool, str)
                or _KEY.fullmatch(raw_tool) is None
                or raw_tool in node_tools
                or len(node_tools) >= 8
            ):
                raise ValueError(f"Agent node {key} has an invalid tool allowlist")
            node_tools.append(raw_tool)
        if node_type in ("model", "router"):
            if (
                model_role not in _MODEL_ROLES
                or not prompt_template
                or len(prompt_template) > 12_000
                or node_tools
            ):
                raise ValueError(f"Agent node {key} has an invalid model contract")
        elif model_role or prompt_template:
            raise ValueError(f"Agent node {key} cannot define model fields")
        if node_type == "tool" and not node_tools:
            raise ValueError(f"Agent tool node {key} requires an allowlist")
        nodes[key] = DeclarativeNode(
            key=key,
            type=node_type,
            model_role=model_role,
            prompt_template=prompt_template,
            tools=tuple(node_tools),
        )
    if entry_node not in nodes or not any(node.type == "end" for node in nodes.values()):
        raise ValueError("Agent definition requires its entry node and at least one end node")

    raw_edges = value.get("edges")
    if not isinstance(raw_edges, list) or len(raw_edges) > 256:
        raise ValueError("Agent definition edges must be an array of at most 256 entries")
    edges: list[DeclarativeEdge] = []
    identities: set[tuple[str, str, str]] = set()
    conditions: dict[str, set[str]] = {}
    for raw_edge in raw_edges:
        if not isinstance(raw_edge, Mapping):
            raise ValueError("Agent definition edge must be an object")
        source = _required_key(raw_edge, "from")
        target = _required_key(raw_edge, "to")
        condition = str(raw_edge.get("condition") or "").strip()
        if source not in nodes or target not in nodes or source == target:
            raise ValueError("Agent definition contains an invalid edge")
        if nodes[source].type == "router":
            if _KEY.fullmatch(condition) is None or condition in conditions.setdefault(
                source, set()
            ):
                raise ValueError(f"Agent router {source} requires unique valid conditions")
            conditions[source].add(condition)
        elif condition:
            raise ValueError(f"Agent non-router edge from {source} cannot define a condition")
        identity = (source, target, condition)
        if identity in identities:
            raise ValueError("Agent definition contains a duplicate edge")
        identities.add(identity)
        edges.append(DeclarativeEdge(source=source, target=target, condition=condition))

    compiled = CompiledAgentDefinition(
        display_name=display_name,
        modules=tuple(modules),
        model_profile=model_profile,
        entry_node=entry_node,
        nodes=nodes,
        edges=tuple(edges),
        max_steps=_required_int(value, "budget", "max_steps", 1, 128),
        max_model_calls=_required_int(value, "budget", "max_model_calls", 1, 64),
        max_tool_calls=_required_int(value, "budget", "max_tool_calls", 0, 64),
        max_total_tokens=_required_int(value, "budget", "max_total_tokens", 64, 2_000_000),
        timeout_ms=_required_int(value, "budget", "timeout_ms", 1_000, 900_000),
    )
    for key, node in nodes.items():
        outgoing = compiled.outgoing(key)
        if node.type == "end" and outgoing:
            raise ValueError(f"Agent end node {key} cannot have outgoing edges")
        if node.type == "router" and len(outgoing) < 2:
            raise ValueError(f"Agent router {key} requires at least two outgoing edges")
        if node.type in ("model", "tool") and len(outgoing) != 1:
            raise ValueError(f"Agent node {key} requires exactly one outgoing edge")
    if compiled.max_tool_calls == 0 and any(node.type == "tool" for node in nodes.values()):
        raise ValueError("Agent tool nodes require a positive max_tool_calls budget")
    if _reachable(entry_node, compiled) != set(nodes):
        raise ValueError("all Agent nodes must be reachable from entry_node")
    terminating = {key for key, node in nodes.items() if node.type == "end"}
    while True:
        expanded = terminating | {
            key
            for key in nodes
            if any(edge.target in terminating for edge in compiled.outgoing(key))
        }
        if expanded == terminating:
            break
        terminating = expanded
    if terminating != set(nodes):
        raise ValueError("every Agent node must have a path to an end node")
    return compiled


def build_declarative_graph(
    *,
    definition: Mapping[str, Any],
    definition_identity: Mapping[str, str],
    checkpointer: Any,
    decisions: DecisionPort,
    tools: ToolGateway,
    budget_policy: BudgetPolicy,
) -> Any:
    compiled = compile_agent_definition(definition)
    policy = budget_policy
    policy.validate()
    manifest = _decision_manifest(decisions)

    def runtime_name(key: str) -> str:
        return f"studio__{key}"

    terminal_name = "__studio_terminal"
    init_name = "__studio_init"
    builder = StateGraph(DeclarativeAgentState)

    def initialize(state: DeclarativeAgentState) -> dict[str, Any]:
        _validate_input(state)
        if state["module"] not in compiled.modules:
            raise ValueError("Agent Run module is not assigned to the compiled definition")
        catalog = state.get("context", {}).get("tools", [])
        catalog_names = {
            item.get("name")
            for item in catalog
            if isinstance(item, Mapping) and isinstance(item.get("name"), str)
        }
        configured_tools = {tool for node in compiled.nodes.values() for tool in node.tools}
        missing = sorted(configured_tools - catalog_names)
        if missing:
            raise ValueError(
                "trusted tool catalog is missing configured tools: " + ", ".join(missing)
            )
        limits = policy.limits()
        limits["max_actions"] = min(policy.max_actions, compiled.max_tool_calls)
        limits["max_model_calls"] = min(policy.max_model_calls, compiled.max_model_calls)
        limits["max_total_tokens"] = min(policy.max_total_tokens, compiled.max_total_tokens)
        limits["max_prompt_tokens"] = min(policy.max_prompt_tokens, compiled.max_total_tokens)
        limits["max_completion_tokens"] = min(
            policy.max_completion_tokens, compiled.max_total_tokens
        )
        limits["max_steps"] = compiled.max_steps
        limits["timeout_ms"] = compiled.timeout_ms
        node_contracts = {
            f"studio.{node.key}": {
                "kind": node.type,
                "model_role": node.model_role,
                "tools": list(node.tools),
                "recovery": (
                    "typed_interrupt_and_idempotency_key"
                    if node.type == "tool"
                    else "checkpoint_replay"
                ),
            }
            for node in compiled.nodes.values()
        }
        catalog_fingerprint = tool_catalog_fingerprint(catalog)
        return {
            "graph_name": GRAPH_NAME,
            "graph_version": GRAPH_VERSION,
            "agent_definition_key": definition_identity.get("key", ""),
            "agent_definition_version_id": definition_identity.get("version_id", ""),
            "agent_definition_fingerprint": definition_identity.get("fingerprint", ""),
            "response": "",
            "outcome": "",
            "intent": "",
            "execution_mode": "agent_studio",
            "steps": 0,
            "action_index": 0,
            "action_budget": compiled.max_tool_calls,
            "active_elapsed_ms": 0,
            "timeout_ms": compiled.timeout_ms,
            "node_outputs": {},
            "observations": [],
            "proposed_tool": {},
            "tool_requires_composition": False,
            "preparation": {},
            "approval": {},
            "tool_result": {},
            "pending_task_id": "",
            "task_polls": 0,
            "route_condition": "",
            "budget_limits": limits,
            "budget_usage": initial_budget_usage(),
            "model_manifest": dict(manifest),
            "model_events": [],
            "tool_catalog_fingerprint": catalog_fingerprint,
            "node_contracts": node_contracts,
            "node_trace": [
                _trace(
                    "studio.initialize",
                    "succeeded",
                    details={
                        "compiler": "agent-studio-langgraph/v1",
                        "entry_node": compiled.entry_node,
                        "definition_fingerprint": definition_identity.get("fingerprint", ""),
                    },
                )
            ],
            "recovery": {
                "checkpoint_namespace": "top-level",
                "graph_identity": f"{GRAPH_NAME}@{GRAPH_VERSION}",
                "agent_definition_fingerprint": definition_identity.get("fingerprint", ""),
                "tool_catalog_fingerprint": catalog_fingerprint,
                "semantics": "at_least_once_with_idempotent_side_effects",
                "approval_resumes": 0,
                "tool_resumes": 0,
                "last_interrupt_type": "",
            },
            "_studio_stop": False,
        }

    def terminal(state: DeclarativeAgentState) -> dict[str, Any]:
        response = str(state.get("response") or "").strip()
        if not response:
            response = "Agent 已完成执行，但没有生成可交付回复。"
        return {
            "response": response,
            "outcome": str(state.get("outcome") or "completed"),
            "node_trace": [*state.get("node_trace", []), _trace("studio.terminal", "succeeded")],
            "_studio_stop": False,
        }

    builder.add_node(init_name, initialize)
    builder.add_node(terminal_name, terminal)
    builder.add_edge(START, init_name)
    builder.add_edge(init_name, runtime_name(compiled.entry_node))
    builder.add_edge(terminal_name, END)

    for node in compiled.nodes.values():
        if node.type == "model":
            _register_model_node(
                builder,
                node=node,
                successor=runtime_name(compiled.outgoing(node.key)[0].target),
                runtime_name=runtime_name,
                terminal_name=terminal_name,
                compiled=compiled,
                decisions=decisions,
                policy=policy,
                manifest=manifest,
            )
        elif node.type == "router":
            _register_router_node(
                builder,
                node=node,
                outgoing=compiled.outgoing(node.key),
                runtime_name=runtime_name,
                terminal_name=terminal_name,
                compiled=compiled,
                decisions=decisions,
                policy=policy,
                manifest=manifest,
            )
        elif node.type == "tool":
            _register_tool_node(
                builder,
                node=node,
                successor=runtime_name(compiled.outgoing(node.key)[0].target),
                runtime_name=runtime_name,
                terminal_name=terminal_name,
                compiled=compiled,
                decisions=decisions,
                tools=tools,
                policy=policy,
                manifest=manifest,
            )
        else:
            name = runtime_name(node.key)

            def finish(
                state: DeclarativeAgentState,
                *,
                node_key: str = node.key,
            ) -> dict[str, Any]:
                return {
                    "outcome": str(state.get("outcome") or "completed"),
                    "node_trace": [
                        *state.get("node_trace", []),
                        _trace(f"studio.{node_key}", "succeeded"),
                    ],
                }

            builder.add_node(name, finish)
            builder.add_edge(name, terminal_name)
    return builder.compile(checkpointer=checkpointer)


def _register_model_node(
    builder: Any,
    *,
    node: DeclarativeNode,
    successor: str,
    runtime_name: Callable[[str], str],
    terminal_name: str,
    compiled: CompiledAgentDefinition,
    decisions: DecisionPort,
    policy: BudgetPolicy,
    manifest: Mapping[str, Any],
) -> None:
    del runtime_name
    name = f"studio__{node.key}"

    def execute(state: DeclarativeAgentState) -> dict[str, Any]:
        blocked = _execution_guard(state, compiled, name)
        if blocked:
            return blocked
        started = time.perf_counter_ns()
        context = _model_context(state, name, policy)
        executor = getattr(decisions, "execute_declarative_node", None)
        try:
            if callable(executor):
                raw = executor(
                    module=state["module"],
                    message=state["user_message"],
                    node_type="model",
                    role=node.model_role,
                    prompt_template=node.prompt_template,
                    conditions=(),
                    context=context,
                )
                if not isinstance(raw, Mapping):
                    raise ValueError("declarative model node must return an object")
                response = str(raw.get("response") or "").strip()
            else:
                responder = getattr(decisions, "respond", None)
                if callable(responder):
                    response = str(
                        responder(
                            module=state["module"],
                            message=state["user_message"],
                            context=context,
                        )
                    ).strip()
                else:
                    response = decisions.decide(
                        module=state["module"],
                        message=state["user_message"],
                        context=context,
                    ).response.strip()
            if not response:
                raise ValueError(f"Agent model node {node.key} returned an empty response")
        except ModelBudgetExceeded as exc:
            return _terminal_update(state, name, "model_budget_exhausted", str(exc))
        except Exception as exc:
            return _model_failure_update(
                state,
                decisions=decisions,
                node=name,
                role=node.model_role,
                started_ns=started,
                error=exc,
            )
        model_update = _model_update(
            state,
            decisions=decisions,
            node=name,
            role=node.model_role,
            manifest=manifest,
            started_ns=started,
        )
        elapsed = _elapsed_ms(started)
        outputs = dict(state.get("node_outputs", {}))
        outputs[node.key] = {"response": response}
        return {
            **model_update,
            "response": response,
            "node_outputs": outputs,
            "steps": state.get("steps", 0) + 1,
            "active_elapsed_ms": state.get("active_elapsed_ms", 0) + elapsed,
        }

    builder.add_node(name, execute)
    builder.add_conditional_edges(
        name,
        lambda state: "stop" if state.get("_studio_stop") else "next",
        {"stop": terminal_name, "next": successor},
    )


def _register_router_node(
    builder: Any,
    *,
    node: DeclarativeNode,
    outgoing: tuple[DeclarativeEdge, ...],
    runtime_name: Callable[[str], str],
    terminal_name: str,
    compiled: CompiledAgentDefinition,
    decisions: DecisionPort,
    policy: BudgetPolicy,
    manifest: Mapping[str, Any],
) -> None:
    name = runtime_name(node.key)
    allowed = tuple(edge.condition for edge in outgoing)

    def execute(state: DeclarativeAgentState) -> dict[str, Any]:
        blocked = _execution_guard(state, compiled, name)
        if blocked:
            return blocked
        started = time.perf_counter_ns()
        context = _model_context(state, name, policy)
        executor = getattr(decisions, "execute_declarative_node", None)
        try:
            if callable(executor):
                raw = executor(
                    module=state["module"],
                    message=state["user_message"],
                    node_type="router",
                    role=node.model_role,
                    prompt_template=node.prompt_template,
                    conditions=allowed,
                    context=context,
                )
                if not isinstance(raw, Mapping):
                    raise ValueError("declarative router node must return an object")
                condition = str(raw.get("condition") or "").strip()
                response = str(raw.get("response") or "").strip()
            else:
                decision = decisions.decide(
                    module=state["module"],
                    message=state["user_message"],
                    context=context,
                )
                response = decision.response.strip()
                candidates = (decision.tool_name, decision.intent, "respond" if response else "")
                condition = next(
                    (candidate for candidate in candidates if candidate in allowed), ""
                )
            if condition not in allowed:
                raise ValueError(f"Agent router {node.key} selected an unknown condition")
        except ModelBudgetExceeded as exc:
            return _terminal_update(state, name, "model_budget_exhausted", str(exc))
        except Exception as exc:
            return _model_failure_update(
                state,
                decisions=decisions,
                node=name,
                role=node.model_role,
                started_ns=started,
                error=exc,
            )
        model_update = _model_update(
            state,
            decisions=decisions,
            node=name,
            role=node.model_role,
            manifest=manifest,
            started_ns=started,
        )
        elapsed = _elapsed_ms(started)
        outputs = dict(state.get("node_outputs", {}))
        outputs[node.key] = {"condition": condition, **({"response": response} if response else {})}
        return {
            **model_update,
            "route_condition": condition,
            "intent": condition,
            "response": response or state.get("response", ""),
            "node_outputs": outputs,
            "steps": state.get("steps", 0) + 1,
            "active_elapsed_ms": state.get("active_elapsed_ms", 0) + elapsed,
        }

    builder.add_node(name, execute)
    routes = {edge.condition: runtime_name(edge.target) for edge in outgoing}
    routes["__STUDIO_STOP__"] = terminal_name
    builder.add_conditional_edges(
        name,
        lambda state: "__STUDIO_STOP__" if state.get("_studio_stop") else state["route_condition"],
        routes,
    )


def _register_tool_node(
    builder: Any,
    *,
    node: DeclarativeNode,
    successor: str,
    runtime_name: Callable[[str], str],
    terminal_name: str,
    compiled: CompiledAgentDefinition,
    decisions: DecisionPort,
    tools: ToolGateway,
    policy: BudgetPolicy,
    manifest: Mapping[str, Any],
) -> None:
    name = runtime_name(node.key)
    compose_name = f"__studio_compose__{node.key}"
    prepare_name = f"__studio_prepare__{node.key}"
    approval_name = f"__studio_approval__{node.key}"
    commit_name = f"__studio_commit__{node.key}"
    reject_name = f"__studio_reject__{node.key}"
    observe_name = f"__studio_observe__{node.key}"
    wait_name = f"__studio_wait__{node.key}"

    def select_tool(state: DeclarativeAgentState) -> dict[str, Any]:
        blocked = _execution_guard(state, compiled, name)
        if blocked:
            return blocked
        action_index = state.get("action_index", 0) + 1
        if action_index > compiled.max_tool_calls or action_index > policy.max_actions:
            return _terminal_update(state, name, "tool_budget_exhausted", "工具调用预算已用完")
        reason = model_budget_exhaustion(
            state.get("budget_limits", policy.limits()),
            state.get("budget_usage", initial_budget_usage()),
            name + ".select",
        )
        if reason:
            return _terminal_update(state, name, "model_budget_exhausted", reason)
        catalog = _tool_subset(state, node.tools)
        started = time.perf_counter_ns()
        context = _model_context(state, name + ".select", policy)
        context["tools"] = catalog
        context["declarative_tool_node"] = node.key
        try:
            decision = decisions.decide(
                module=state["module"],
                message=state["user_message"],
                context=context,
            )
        except ModelBudgetExceeded as exc:
            return _terminal_update(state, name, "model_budget_exhausted", str(exc))
        except Exception as exc:
            return _model_failure_update(
                state,
                decisions=decisions,
                node=name + ".select",
                role="router",
                started_ns=started,
                error=exc,
            )
        if decision.tool_name not in node.tools:
            return _model_failure_update(
                state,
                decisions=decisions,
                node=name + ".select",
                role="router",
                started_ns=started,
                error=ValueError(
                    f"Agent tool node {node.key} selected a tool outside its allowlist"
                ),
            )
        definition = next(item for item in catalog if item.get("name") == decision.tool_name)
        arguments = dict(decision.tool_arguments)
        needs_composition = (
            decision.requires_argument_composition or definition.get("compose_arguments") is True
        )
        model_update = _model_update(
            state,
            decisions=decisions,
            node=name + ".select",
            role="router",
            manifest=manifest,
            started_ns=started,
        )
        schema = definition.get("parameters")
        if not isinstance(schema, Mapping):
            raise ValueError("trusted tool parameter schema must be an object")
        try:
            if not tool_allowed(state["module"], decision.tool_name):
                raise ValueError("Agent tool is not allowed for the selected module")
            if not needs_composition:
                validate_arguments_against_schema(arguments, schema)
        except ValueError as exc:
            intermediate = cast(DeclarativeAgentState, {**state, **model_update})
            terminal = _terminal_update(
                intermediate,
                name,
                "model_invalid_response",
                f"模型生成的工具调用未通过受信契约：{exc}",
            )
            return {**model_update, **terminal}
        usage = dict(model_update["budget_usage"])
        usage["actions"] = action_index
        elapsed = _elapsed_ms(started)
        return {
            **model_update,
            "proposed_tool": {"name": decision.tool_name, "arguments": arguments},
            "tool_requires_composition": needs_composition,
            "preparation": {},
            "approval": {},
            "tool_result": {},
            "pending_task_id": "",
            "task_polls": 0,
            "action_index": action_index,
            "budget_usage": usage,
            "steps": state.get("steps", 0) + 1,
            "active_elapsed_ms": state.get("active_elapsed_ms", 0) + elapsed,
            "node_trace": [
                *cast(list[dict[str, Any]], model_update["node_trace"]),
                _trace(
                    name,
                    "selected",
                    started_ns=started,
                    details={"tool_name": decision.tool_name},
                ),
            ],
        }

    def compose_arguments(state: DeclarativeAgentState) -> dict[str, Any]:
        reason = model_budget_exhaustion(
            state.get("budget_limits", policy.limits()),
            state.get("budget_usage", initial_budget_usage()),
            name + ".compose",
        )
        if reason:
            return _terminal_update(state, name, "model_budget_exhausted", reason)
        composer = getattr(decisions, "compose_arguments", None)
        if not callable(composer):
            return _terminal_update(
                state,
                name,
                "model_invalid_response",
                "当前模型端口不能生成工具参数",
            )
        proposed = state["proposed_tool"]
        tool_name = str(proposed.get("name") or "")
        started = time.perf_counter_ns()
        try:
            arguments = dict(
                composer(
                    module=state["module"],
                    message=state["user_message"],
                    tool_name=tool_name,
                    context=_model_context(state, name + ".compose", policy),
                )
            )
        except ModelBudgetExceeded as exc:
            return _terminal_update(state, name, "model_budget_exhausted", str(exc))
        except Exception as exc:
            return _model_failure_update(
                state,
                decisions=decisions,
                node=name + ".compose",
                role="composer",
                started_ns=started,
                error=exc,
            )
        model_update = _model_update(
            state,
            decisions=decisions,
            node=name + ".compose",
            role="composer",
            manifest=manifest,
            started_ns=started,
        )
        catalog = _tool_subset(state, node.tools)
        definition = next(item for item in catalog if item.get("name") == tool_name)
        schema = definition.get("parameters")
        if not isinstance(schema, Mapping):
            raise ValueError("trusted tool parameter schema must be an object")
        try:
            validate_arguments_against_schema(arguments, schema)
        except ValueError as exc:
            intermediate = cast(DeclarativeAgentState, {**state, **model_update})
            terminal = _terminal_update(
                intermediate,
                name,
                "model_invalid_response",
                f"模型生成的工具参数未通过受信契约：{exc}",
            )
            return {**model_update, **terminal}
        return {
            **model_update,
            "proposed_tool": {"name": tool_name, "arguments": arguments},
            "tool_requires_composition": False,
            "active_elapsed_ms": state.get("active_elapsed_ms", 0) + _elapsed_ms(started),
            "node_trace": [
                *cast(list[dict[str, Any]], model_update["node_trace"]),
                _trace(compose_name, "succeeded", started_ns=started),
            ],
        }

    def prepare_tool(state: DeclarativeAgentState) -> dict[str, Any]:
        started = time.perf_counter_ns()
        proposed = state["proposed_tool"]
        tool_name = str(proposed.get("name") or "")
        preparation = tools.prepare(
            run_id=state["run_id"],
            user_id=state["user_id"],
            module=state["module"],
            tool_name=tool_name,
            arguments=cast(Mapping[str, Any], proposed.get("arguments") or {}),
            idempotency_key=(
                f"{state['run_id']}:studio:{node.key}:{state.get('action_index', 0)}:prepare"
            ),
        )
        _validate_preparation(preparation, tool_name)
        return {
            "preparation": asdict(preparation),
            "response": preparation.response or state.get("response", ""),
            "active_elapsed_ms": state.get("active_elapsed_ms", 0) + _elapsed_ms(started),
            "node_trace": [
                *state.get("node_trace", []),
                _trace(
                    prepare_name,
                    "succeeded",
                    started_ns=started,
                    details={"tool_name": tool_name, "status": preparation.status},
                ),
            ],
        }

    def approval(state: DeclarativeAgentState) -> Command[Any]:
        preparation = state["preparation"]
        resolution = interrupt(
            {
                "type": "tool_approval",
                "graph_name": GRAPH_NAME,
                "graph_version": GRAPH_VERSION,
                "model_config_version": model_config_version(state.get("model_manifest", {})),
                "model_manifest_fingerprint": state.get("model_manifest", {}).get(
                    "fingerprint", ""
                ),
                "tool_catalog_fingerprint": state.get("tool_catalog_fingerprint", ""),
                "agent_definition_fingerprint": state.get("agent_definition_fingerprint", ""),
                "run_id": state["run_id"],
                "tool_name": preparation["tool_name"],
                "risk_level": preparation["risk_level"],
                "summary": preparation["summary"],
                "arguments": preparation["normalized_arguments"],
                "confirmation_token": preparation["confirmation_token"],
            }
        )
        if not isinstance(resolution, dict) or not isinstance(resolution.get("approved"), bool):
            raise ValueError("approval resolution must contain approved:boolean")
        recovery = _recovery_update(state, "approval_resumes", "tool_approval")
        update = {
            "approval": dict(resolution),
            "recovery": recovery,
            "node_trace": [
                *state.get("node_trace", []),
                _trace(approval_name, "resumed", details={"approved": resolution["approved"]}),
            ],
        }
        return Command(update=update, goto=commit_name if resolution["approved"] else reject_name)

    def commit(state: DeclarativeAgentState) -> dict[str, Any]:
        started = time.perf_counter_ns()
        outcome = tools.commit(
            run_id=state["run_id"],
            user_id=state["user_id"],
            module=state["module"],
            preparation=state["preparation"],
            resolution=state["approval"],
            idempotency_key=(
                f"{state['run_id']}:studio:{node.key}:{state.get('action_index', 0)}:commit"
            ),
        )
        if not outcome.response.strip():
            raise ValueError("tool gateway commit must contain a response")
        return {
            "tool_result": asdict(outcome),
            "response": outcome.response,
            "active_elapsed_ms": state.get("active_elapsed_ms", 0) + _elapsed_ms(started),
            "node_trace": [
                *state.get("node_trace", []),
                _trace(commit_name, "succeeded", started_ns=started),
            ],
        }

    def reject(state: DeclarativeAgentState) -> dict[str, Any]:
        return {
            "response": "已取消，本次操作没有写入业务数据。",
            "outcome": "cancelled",
            "node_trace": [*state.get("node_trace", []), _trace(reject_name, "succeeded")],
        }

    def observe(state: DeclarativeAgentState) -> dict[str, Any]:
        source = state.get("tool_result") or state.get("preparation") or {}
        data = source.get("data") if isinstance(source, Mapping) else None
        status, task_id = _skill_status(data), _skill_id(data)
        if status in ("queued", "running") and task_id:
            return {
                "pending_task_id": task_id,
                "response": str(source.get("response") or "工作任务正在执行。"),
                "node_trace": [
                    *state.get("node_trace", []),
                    _trace(observe_name, "waiting", details={"task_id": task_id, "status": status}),
                ],
            }
        proposed = state.get("proposed_tool", {})
        observation = {
            "action": state.get("action_index", 0),
            "tool_name": str(proposed.get("name") or ""),
            "arguments": dict(proposed.get("arguments") or {}),
            "status": status or "completed",
            "response": str(source.get("response") or "").strip(),
            "data": data,
        }
        outputs = dict(state.get("node_outputs", {}))
        outputs[node.key] = observation
        return {
            "observations": [*state.get("observations", []), observation],
            "node_outputs": outputs,
            "pending_task_id": "",
            "response": observation["response"] or state.get("response", ""),
            "outcome": "tool_failed" if status in ("failed", "cancelled") else "",
            "node_trace": [
                *state.get("node_trace", []),
                _trace(
                    observe_name,
                    "failed" if status in ("failed", "cancelled") else "succeeded",
                    details={"status": status or "completed"},
                ),
            ],
        }

    def wait_for_tool(state: DeclarativeAgentState) -> dict[str, Any]:
        polls = state.get("task_polls", 0)
        if polls >= policy.max_tool_resumes:
            return _terminal_update(
                state,
                wait_name,
                "tool_timeout",
                "工作任务仍在处理中，恢复预算已用完；可稍后到工作台查看结果。",
            )
        task_id = state["pending_task_id"]
        resolution = interrupt(
            {
                "type": "tool_wait",
                "graph_name": GRAPH_NAME,
                "graph_version": GRAPH_VERSION,
                "model_config_version": model_config_version(state.get("model_manifest", {})),
                "model_manifest_fingerprint": state.get("model_manifest", {}).get(
                    "fingerprint", ""
                ),
                "tool_catalog_fingerprint": state.get("tool_catalog_fingerprint", ""),
                "agent_definition_fingerprint": state.get("agent_definition_fingerprint", ""),
                "run_id": state["run_id"],
                "task_id": task_id,
                "resume_attempt": polls + 1,
                "poll_after_ms": policy.tool_poll_interval_ms,
                "poll_max_ms": policy.tool_poll_max_interval_ms,
                "resume_schema": {"type": "tool_poll", "task_id": task_id},
            }
        )
        if not isinstance(resolution, dict) or resolution.get("type") not in (
            "tool_poll",
            "tool_result",
        ):
            raise ValueError("tool wait resolution requires type:tool_poll")
        if isinstance(resolution.get("task_id"), str) and resolution["task_id"] != task_id:
            raise ValueError("tool wait resolution task_id does not match")
        started = time.perf_counter_ns()
        outcome: ToolOutcome = tools.observe(
            run_id=state["run_id"],
            user_id=state["user_id"],
            module=state["module"],
            task_id=task_id,
        )
        usage = dict(state.get("budget_usage", initial_budget_usage()))
        usage["tool_resumes"] = polls + 1
        return {
            "tool_result": asdict(outcome),
            "response": outcome.response,
            "task_polls": polls + 1,
            "budget_usage": usage,
            "recovery": _recovery_update(state, "tool_resumes", "tool_wait"),
            "active_elapsed_ms": state.get("active_elapsed_ms", 0) + _elapsed_ms(started),
            "node_trace": [
                *state.get("node_trace", []),
                _trace(wait_name, "resumed", started_ns=started, details={"task_id": task_id}),
            ],
        }

    builder.add_node(name, select_tool)
    builder.add_node(compose_name, compose_arguments)
    builder.add_node(prepare_name, prepare_tool)
    builder.add_node(approval_name, approval)
    builder.add_node(commit_name, commit)
    builder.add_node(reject_name, reject)
    builder.add_node(observe_name, observe)
    builder.add_node(wait_name, wait_for_tool)
    builder.add_conditional_edges(
        name,
        lambda state: (
            "stop"
            if state.get("_studio_stop")
            else "compose"
            if state.get("tool_requires_composition")
            else "prepare"
        ),
        {"stop": terminal_name, "compose": compose_name, "prepare": prepare_name},
    )
    builder.add_conditional_edges(
        compose_name,
        lambda state: "stop" if state.get("_studio_stop") else "prepare",
        {"stop": terminal_name, "prepare": prepare_name},
    )
    builder.add_conditional_edges(
        prepare_name,
        lambda state: (
            "approval"
            if state.get("preparation", {}).get("status") == "requires_confirmation"
            else "observe"
        ),
        {"approval": approval_name, "observe": observe_name},
    )
    builder.add_edge(commit_name, observe_name)
    builder.add_edge(reject_name, successor)
    builder.add_conditional_edges(
        observe_name,
        lambda state: "wait" if state.get("pending_task_id") else "next",
        {"wait": wait_name, "next": successor},
    )
    builder.add_conditional_edges(
        wait_name,
        lambda state: (
            "stop"
            if state.get("_studio_stop")
            else "wait"
            if _skill_status(state.get("tool_result", {}).get("data")) in ("queued", "running")
            else "observe"
        ),
        {"stop": terminal_name, "wait": wait_name, "observe": observe_name},
    )


def _execution_guard(
    state: DeclarativeAgentState,
    compiled: CompiledAgentDefinition,
    node: str,
) -> dict[str, Any]:
    if state.get("steps", 0) >= compiled.max_steps:
        return _terminal_update(state, node, "step_budget_exhausted", "Agent 节点步数预算已用完")
    if state.get("active_elapsed_ms", 0) >= compiled.timeout_ms:
        return _terminal_update(state, node, "runtime_timeout", "Agent 主动执行时间预算已用完")
    reason = model_budget_exhaustion(
        state.get("budget_limits", {}),
        state.get("budget_usage", initial_budget_usage()),
        node,
    )
    if reason and compiled.nodes[node.removeprefix("studio__")].type in ("model", "router"):
        return _terminal_update(state, node, "model_budget_exhausted", reason)
    return {}


def _terminal_update(
    state: DeclarativeAgentState,
    node: str,
    outcome: str,
    reason: str,
) -> dict[str, Any]:
    return {
        "outcome": outcome,
        "response": f"任务已安全停止：{reason}。",
        "_studio_stop": True,
        "node_trace": [
            *state.get("node_trace", []),
            _trace(node, "blocked", details={"reason": reason}),
        ],
    }


def _model_context(
    state: DeclarativeAgentState,
    node: str,
    policy: BudgetPolicy,
) -> dict[str, Any]:
    context = dict(state.get("context", {}))
    context["observations"] = list(state.get("observations", []))
    context["node_outputs"] = dict(state.get("node_outputs", {}))
    context["tool_result"] = dict(state.get("tool_result", {}))
    limits = state.get("budget_limits", policy.limits())
    usage = state.get("budget_usage", initial_budget_usage())
    total_remaining = max(
        0,
        int(limits.get("max_total_tokens", policy.max_total_tokens))
        - int(usage.get("prompt_tokens", 0))
        - int(usage.get("completion_tokens", 0)),
    )
    context["model_allowance"] = {
        "remaining_calls": max(
            0,
            int(limits.get("max_model_calls", policy.max_model_calls))
            - int(usage.get("model_calls", 0)),
        ),
        "remaining_prompt_tokens": min(
            total_remaining,
            max(
                0,
                int(limits.get("max_prompt_tokens", policy.max_prompt_tokens))
                - int(usage.get("prompt_tokens", 0)),
            ),
        ),
        "remaining_completion_tokens": min(
            total_remaining,
            max(
                0,
                int(limits.get("max_completion_tokens", policy.max_completion_tokens))
                - int(usage.get("completion_tokens", 0)),
            ),
        ),
        "remaining_total_tokens": total_remaining,
        "remaining_cost_micros": max(
            0,
            int(limits.get("max_cost_micros", policy.max_cost_micros))
            - int(usage.get("cost_micros", 0)),
        ),
        "graph_node": node,
    }
    return context


def _model_update(
    state: DeclarativeAgentState,
    *,
    decisions: DecisionPort,
    node: str,
    role: str,
    manifest: Mapping[str, Any],
    started_ns: int,
) -> dict[str, Any]:
    consumer = getattr(decisions, "consume_observability", None)
    raw_events = consumer() if callable(consumer) else []
    events = [dict(item) for item in raw_events if isinstance(item, Mapping)]
    if not events:
        events = [
            {
                "kind": "model_call",
                "role": role,
                "status": "succeeded",
                "provider": manifest.get("provider", "unknown"),
                "requested_model": "",
                "returned_model": "",
                "prompt_tokens": 0,
                "completion_tokens": 0,
                "cached_tokens": 0,
                "reasoning_tokens": 0,
                "cost_micros": 0,
                "latency_ms": _elapsed_ms(started_ns),
                "error_status": 0,
                "retryable": False,
            }
        ]
    for event in events:
        event["graph_node"] = node
    usage = apply_model_events(
        state.get("budget_usage", initial_budget_usage()),
        node=node,
        events=events,
    )
    return {
        "budget_usage": usage,
        "model_events": [*state.get("model_events", []), *events],
        "node_trace": [
            *state.get("node_trace", []),
            _trace(
                node,
                "succeeded",
                started_ns=started_ns,
                details={"model_calls": len(events), "role": role},
            ),
        ],
    }


def _model_failure_update(
    state: DeclarativeAgentState,
    *,
    decisions: DecisionPort,
    node: str,
    role: str,
    started_ns: int,
    error: Exception,
) -> dict[str, Any]:
    consumer = getattr(decisions, "consume_observability", None)
    raw_events = consumer() if callable(consumer) else []
    events = [dict(item) for item in raw_events if isinstance(item, Mapping)]
    if not events:
        raise error
    for event in events:
        event["graph_node"] = node
    usage = apply_model_events(
        state.get("budget_usage", initial_budget_usage()),
        node=node,
        events=events,
    )
    statuses = {
        int(event.get("error_status", 0))
        for event in events
        if isinstance(event.get("error_status"), int)
        and not isinstance(event.get("error_status"), bool)
    }
    if statuses & {401, 403}:
        outcome = "model_authentication_error"
        response = "模型服务鉴权失败，Agent 已安全停止。"
    elif any(event.get("status") == "error" for event in events):
        outcome = "model_unavailable"
        response = "模型服务当前不可用，Agent 已安全停止；已有检查点和预算账本均已保留。"
    else:
        outcome = "model_invalid_response"
        response = "模型结果未通过声明式节点契约，Agent 已安全停止。"
    return {
        "budget_usage": usage,
        "model_events": [*state.get("model_events", []), *events],
        "outcome": outcome,
        "response": response,
        "_studio_stop": True,
        "active_elapsed_ms": state.get("active_elapsed_ms", 0) + _elapsed_ms(started_ns),
        "node_trace": [
            *state.get("node_trace", []),
            _trace(
                node,
                "failed",
                started_ns=started_ns,
                details={"role": role, "error": type(error).__name__},
            ),
        ],
    }


def _tool_subset(
    state: DeclarativeAgentState,
    allowed: tuple[str, ...],
) -> list[dict[str, Any]]:
    raw_catalog = state.get("context", {}).get("tools", [])
    if not isinstance(raw_catalog, list):
        raise ValueError("trusted tool catalog must be an array")
    selected = [
        dict(item)
        for item in raw_catalog
        if isinstance(item, Mapping) and item.get("name") in allowed
    ]
    selected_names = [str(item.get("name") or "") for item in selected]
    if len(selected_names) != len(allowed) or set(selected_names) != set(allowed):
        raise ValueError("trusted tool catalog does not contain the Agent tool allowlist")
    return selected


def _validate_preparation(preparation: ToolPreparation, tool_name: str) -> None:
    if preparation.tool_name != tool_name:
        raise ValueError("tool gateway returned a mismatched tool")
    if preparation.status == "requires_confirmation":
        if not preparation.confirmation_token or not preparation.summary:
            raise ValueError("confirmation token and summary are required")
    elif preparation.status == "completed":
        if not preparation.response.strip():
            raise ValueError("completed tool preparation must contain a response")
    else:
        raise ValueError("tool gateway returned an unsupported preparation status")


def _recovery_update(
    state: DeclarativeAgentState,
    counter: str,
    interrupt_type: str,
) -> dict[str, Any]:
    recovery = dict(state.get("recovery", {}))
    current = recovery.get(counter, 0)
    recovery[counter] = current + 1 if isinstance(current, int) else 1
    recovery["last_interrupt_type"] = interrupt_type
    return recovery


def _skill_status(value: Any) -> str:
    if not isinstance(value, Mapping) or value.get("kind") != "skill_run":
        return ""
    return str(value.get("status") or "")


def _skill_id(value: Any) -> str:
    if not isinstance(value, Mapping) or value.get("kind") != "skill_run":
        return ""
    return str(value.get("id") or "").strip()


def _reachable(entry: str, compiled: CompiledAgentDefinition) -> set[str]:
    visited = {entry}
    pending = [entry]
    while pending:
        current = pending.pop()
        for edge in compiled.outgoing(current):
            if edge.target not in visited:
                visited.add(edge.target)
                pending.append(edge.target)
    return visited


def _required_string(value: Mapping[str, Any], key: str, maximum: int) -> str:
    raw = value.get(key)
    if not isinstance(raw, str) or not raw.strip() or len(raw.strip()) > maximum:
        raise ValueError(f"Agent definition {key} is invalid")
    return raw.strip()


def _required_key(value: Mapping[str, Any], key: str) -> str:
    result = _required_string(value, key, 96)
    if _KEY.fullmatch(result) is None:
        raise ValueError(f"Agent definition {key} is invalid")
    return result


def _required_int(
    value: Mapping[str, Any],
    object_key: str,
    key: str,
    minimum: int,
    maximum: int,
) -> int:
    nested = value.get(object_key)
    if not isinstance(nested, Mapping):
        raise ValueError(f"Agent definition {object_key} must be an object")
    raw = nested.get(key)
    if not isinstance(raw, int) or isinstance(raw, bool) or not minimum <= raw <= maximum:
        raise ValueError(f"Agent definition budget {key} is invalid")
    return raw


def _trace(
    node: str,
    status: str,
    *,
    started_ns: int = 0,
    details: Mapping[str, Any] | None = None,
) -> dict[str, Any]:
    return {
        "node": node,
        "status": status,
        "duration_ms": _elapsed_ms(started_ns) if started_ns else 0,
        "details": dict(details or {}),
    }


def _elapsed_ms(started_ns: int) -> int:
    return max(0, (time.perf_counter_ns() - started_ns) // 1_000_000)
