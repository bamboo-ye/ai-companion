from __future__ import annotations

import hashlib
import json
import os
from dataclasses import asdict, dataclass
from typing import Any, Literal, Mapping

GRAPH_NAME = "ai-companion-supervisor"
GRAPH_VERSION = "3.21.0"
DEFAULT_MODEL_CONFIG_VERSION = "2026-08-structured-composer-v4"

NodeKind = Literal["deterministic", "model", "tool", "human", "external_wait"]


@dataclass(frozen=True)
class NodeContract:
    responsibility: str
    kind: NodeKind
    model_role: str = ""
    side_effects: str = "none"
    recovery: str = "replay_safe"
    max_model_calls: int = 0
    allowed_writes: tuple[str, ...] = ()


_MODEL_CONTROL_WRITES = (
    "budget_usage",
    "model_events",
    "node_trace",
    "outcome",
    "response",
    "needs_response",
    "steps",
)
_MODULE_WRITES = (
    *_MODEL_CONTROL_WRITES,
    "intent",
    "execution_mode",
    "execution_mode_reason",
    "proposed_tool",
    "preparation",
    "tool_result",
)


NODE_CONTRACTS: dict[str, NodeContract] = {
    "supervisor": NodeContract(
        responsibility="Validate input and initialize immutable run governance.",
        kind="deterministic",
        allowed_writes=(
            "graph_name",
            "graph_version",
            "observations",
            "action_index",
            "action_budget",
            "pending_task_id",
            "task_polls",
            "assessment",
            "needs_response",
            "node_contracts",
            "budget_limits",
            "budget_usage",
            "model_manifest",
            "tool_catalog_fingerprint",
            "model_events",
            "node_trace",
            "recovery",
            "failure",
            "failed_task_id",
            "repair",
            "repair_attempts",
            "repair_history",
            "repair_fingerprints",
            "repair_started_at_ms",
            "email_validation",
            "email_rewrite_attempts",
            "presentation_validation",
            "presentation_rewrite_attempts",
            "document_processing",
            "response_validation",
            "response_rewrite_attempts",
            "execution_mode",
            "execution_mode_reason",
            "task_contract",
            "artifact_validation",
            "steps",
        ),
    ),
    "plan": NodeContract(
        responsibility="Produce an observable execution plan without invoking tools.",
        kind="model",
        model_role="planner",
        recovery="checkpoint_replay",
        max_model_calls=1,
        allowed_writes=(*_MODEL_CONTROL_WRITES, "plan", "action_budget"),
    ),
    "companion": NodeContract(
        responsibility="Route or answer companion requests within the supplied catalog.",
        kind="model",
        model_role="router",
        recovery="checkpoint_replay",
        max_model_calls=8,
        allowed_writes=_MODULE_WRITES,
    ),
    "life": NodeContract(
        responsibility="Route or answer life requests within the supplied catalog.",
        kind="model",
        model_role="router",
        recovery="checkpoint_replay",
        max_model_calls=8,
        allowed_writes=_MODULE_WRITES,
    ),
    "work": NodeContract(
        responsibility="Route or answer work requests within the supplied catalog.",
        kind="model",
        model_role="router",
        recovery="checkpoint_replay",
        max_model_calls=8,
        allowed_writes=_MODULE_WRITES,
    ),
    "compose_arguments": NodeContract(
        responsibility=(
            "Generate grounded arguments for one trusted-catalog tool already "
            "selected by the router, checkpointing oversized document rounds "
            "and merging validated partial records deterministically."
        ),
        kind="model",
        model_role="composer",
        recovery="checkpoint_replay",
        max_model_calls=48,
        allowed_writes=(*_MODEL_CONTROL_WRITES, "proposed_tool", "document_processing"),
    ),
    "email_quality_gate": NodeContract(
        responsibility=(
            "Validate language fidelity, polite email structure, introduction, "
            "sender identity and signature, and validate structured presentation "
            "arguments before tool dispatch; request at most two bounded rewrites."
        ),
        kind="deterministic",
        recovery="checkpoint_replay",
        allowed_writes=(
            "proposed_tool",
            "email_validation",
            "email_rewrite_attempts",
            "presentation_validation",
            "presentation_rewrite_attempts",
            "artifact_validation",
            "outcome",
            "response",
            "node_trace",
            "steps",
        ),
    ),
    "generate_response": NodeContract(
        responsibility=(
            "Generate final conversational text without selecting or invoking tools; "
            "the companion module uses its isolated free responder role."
        ),
        kind="model",
        model_role="responder",
        recovery="checkpoint_replay",
        max_model_calls=3,
        allowed_writes=_MODEL_CONTROL_WRITES,
    ),
    "response_quality_gate": NodeContract(
        responsibility=(
            "Remove exact repetition deterministically, report near-duplicate "
            "content and allow at most one bounded response rewrite."
        ),
        kind="deterministic",
        recovery="checkpoint_replay",
        allowed_writes=(
            "response",
            "response_validation",
            "response_rewrite_attempts",
            "outcome",
            "node_trace",
            "steps",
        ),
    ),
    "revise_response": NodeContract(
        responsibility=(
            "Rewrite only a response that failed the deterministic quality policy, "
            "preserving trusted facts and the original request."
        ),
        kind="model",
        model_role="responder",
        recovery="checkpoint_replay",
        max_model_calls=2,
        allowed_writes=(
            *_MODEL_CONTROL_WRITES,
            "response_validation",
        ),
    ),
    "preflight_normalize": NodeContract(
        responsibility="Apply versioned, semantics-preserving argument normalization and validate the trusted schema.",
        kind="deterministic",
        allowed_writes=(
            "proposed_tool",
            "repair_history",
            "failure",
            "response",
            "outcome",
            "node_trace",
            "steps",
        ),
    ),
    "prepare_tool": NodeContract(
        responsibility="Validate and idempotently prepare exactly one proposed tool action.",
        kind="tool",
        side_effects="idempotent_prepare",
        recovery="idempotency_key",
        allowed_writes=(
            "preparation",
            "response",
            "tool_result",
            "pending_task_id",
            "task_polls",
            "action_index",
            "budget_usage",
            "node_trace",
            "steps",
            "outcome",
        ),
    ),
    "approval": NodeContract(
        responsibility="Suspend before a user-visible write and accept a typed decision.",
        kind="human",
        recovery="typed_interrupt",
        allowed_writes=("approval", "recovery", "node_trace", "steps"),
    ),
    "commit_tool": NodeContract(
        responsibility="Commit only the previously prepared and approved action.",
        kind="tool",
        side_effects="approved_write",
        recovery="idempotency_key",
        allowed_writes=("tool_result", "response", "node_trace", "steps"),
    ),
    "reject_tool": NodeContract(
        responsibility="Terminate a rejected action without committing business data.",
        kind="deterministic",
        allowed_writes=("response", "outcome", "node_trace"),
    ),
    "observe_result": NodeContract(
        responsibility="Normalize a tool result into an evidence-bearing observation.",
        kind="deterministic",
        allowed_writes=(
            "pending_task_id",
            "response",
            "observations",
            "outcome",
            "failure",
            "failed_task_id",
            "repair_history",
            "node_trace",
            "steps",
        ),
    ),
    "continue_document_extraction": NodeContract(
        responsibility=(
            "Continue an oversized attachment at the exact trusted next_round "
            "without spending router or assessor calls between extraction windows."
        ),
        kind="deterministic",
        recovery="checkpoint_replay",
        allowed_writes=(
            "proposed_tool",
            "preparation",
            "tool_result",
            "outcome",
            "response",
            "node_trace",
            "steps",
        ),
    ),
    "artifact_quality_gate": NodeContract(
        responsibility=(
            "Treat generated files as evidence and verify the task contract, "
            "source coverage and artifact-specific quality before completion."
        ),
        kind="deterministic",
        recovery="checkpoint_replay",
        allowed_writes=(
            "artifact_validation",
            "outcome",
            "response",
            "node_trace",
            "steps",
        ),
    ),
    "classify_tool_failure": NodeContract(
        responsibility="Classify a trusted tool failure and select one bounded recovery strategy.",
        kind="deterministic",
        allowed_writes=(
            "failure",
            "repair",
            "repair_started_at_ms",
            "response",
            "outcome",
            "node_trace",
            "steps",
        ),
    ),
    "apply_known_repair": NodeContract(
        responsibility="Apply one allowlisted deterministic repair operator without side effects.",
        kind="deterministic",
        allowed_writes=("repair", "response", "outcome", "node_trace", "steps"),
    ),
    "plan_repair_llm": NodeContract(
        responsibility="Use the pinned repairer model to propose a schema-constrained repair plan.",
        kind="model",
        model_role="repairer",
        recovery="checkpoint_replay",
        max_model_calls=1,
        allowed_writes=(*_MODEL_CONTROL_WRITES, "repair"),
    ),
    "validate_repair": NodeContract(
        responsibility="Validate changed paths, trusted schema, loop fingerprint, and repair budget.",
        kind="deterministic",
        allowed_writes=(
            "proposed_tool",
            "repair",
            "repair_started_at_ms",
            "repair_attempts",
            "repair_history",
            "repair_fingerprints",
            "response",
            "outcome",
            "node_trace",
            "steps",
        ),
    ),
    "retry_tool": NodeContract(
        responsibility="Retry the same logical task with one validated argument revision.",
        kind="tool",
        side_effects="idempotent_repair_retry",
        recovery="idempotency_key",
        allowed_writes=(
            "tool_result",
            "pending_task_id",
            "task_polls",
            "response",
            "failure",
            "node_trace",
            "steps",
        ),
    ),
    "reconcile_side_effect": NodeContract(
        responsibility="Stop automatic replay when a failed tool may have produced side effects.",
        kind="deterministic",
        allowed_writes=("response", "outcome", "node_trace", "steps"),
    ),
    "assess_progress": NodeContract(
        responsibility="Compare observations with success criteria and decide whether to continue.",
        kind="model",
        model_role="assessor",
        recovery="checkpoint_replay",
        max_model_calls=6,
        allowed_writes=(*_MODEL_CONTROL_WRITES, "assessment"),
    ),
    "wait_task": NodeContract(
        responsibility="Durably suspend while an external task is unfinished; observe once per resume.",
        kind="external_wait",
        recovery="typed_interrupt",
        allowed_writes=(
            "pending_task_id",
            "outcome",
            "response",
            "tool_result",
            "task_polls",
            "budget_usage",
            "recovery",
            "node_trace",
            "steps",
        ),
    ),
    "continue_action": NodeContract(
        responsibility="Clear action-local state before the next independently budgeted action.",
        kind="deterministic",
        allowed_writes=(
            "proposed_tool",
            "preparation",
            "tool_result",
            "pending_task_id",
            "task_polls",
            "assessment",
            "outcome",
            "needs_response",
            "failure",
            "failed_task_id",
            "repair",
            "repair_started_at_ms",
            "email_validation",
            "email_rewrite_attempts",
            "presentation_validation",
            "presentation_rewrite_attempts",
            "document_processing",
            "response_validation",
            "response_rewrite_attempts",
            "node_trace",
            "steps",
        ),
    ),
    "finalize": NodeContract(
        responsibility="Return a non-empty response and the terminal governance summary.",
        kind="deterministic",
        allowed_writes=("outcome", "response", "node_trace", "steps"),
    ),
}


@dataclass(frozen=True)
class BudgetPolicy:
    max_actions: int = 10
    max_model_calls: int = 64
    max_prompt_tokens: int = 1_000_000
    max_completion_tokens: int = 128_000
    max_cost_micros: int = 250_000
    max_tool_resumes: int = 120
    tool_poll_interval_ms: int = 500
    tool_poll_max_interval_ms: int = 5_000
    max_repair_attempts: int = 2
    max_repair_model_calls: int = 1
    max_repair_cost_micros: int = 1_000
    max_repair_added_latency_ms: int = 15_000

    @classmethod
    def from_env(cls) -> BudgetPolicy:
        policy = cls(
            max_actions=_positive_env("AGENT_MAX_ACTIONS", 10),
            max_model_calls=_positive_env("AGENT_MAX_MODEL_CALLS", 64),
            max_prompt_tokens=_positive_env("AGENT_MAX_PROMPT_TOKENS", 1_000_000),
            max_completion_tokens=_positive_env("AGENT_MAX_COMPLETION_TOKENS", 128_000),
            max_cost_micros=_positive_env("AGENT_MAX_COST_MICROS", 250_000),
            max_tool_resumes=_positive_env("AGENT_MAX_TOOL_RESUMES", 120),
            tool_poll_interval_ms=_positive_env("AGENT_TOOL_POLL_INTERVAL_MS", 500),
            tool_poll_max_interval_ms=_positive_env("AGENT_TOOL_POLL_MAX_INTERVAL_MS", 5_000),
            max_repair_attempts=_positive_env("AGENT_MAX_REPAIR_ATTEMPTS", 2),
            max_repair_model_calls=_positive_env("AGENT_MAX_REPAIR_MODEL_CALLS", 1),
            max_repair_cost_micros=_positive_env("AGENT_MAX_REPAIR_COST_MICROS", 1_000),
            max_repair_added_latency_ms=_positive_env("AGENT_MAX_REPAIR_ADDED_LATENCY_MS", 15_000),
        )
        policy.validate()
        return policy

    def validate(self) -> None:
        if self.max_actions > 100:
            raise ValueError("AGENT_MAX_ACTIONS must be at most 100")
        if self.max_model_calls > 100:
            raise ValueError("AGENT_MAX_MODEL_CALLS must be at most 100")
        if self.max_prompt_tokens > 10_000_000:
            raise ValueError("AGENT_MAX_PROMPT_TOKENS must be at most 10000000")
        if self.max_completion_tokens > 1_000_000:
            raise ValueError("AGENT_MAX_COMPLETION_TOKENS must be at most 1000000")
        if self.max_cost_micros > 10_000_000:
            raise ValueError("AGENT_MAX_COST_MICROS must be at most 10000000")
        if self.max_tool_resumes > 10_000:
            raise ValueError("AGENT_MAX_TOOL_RESUMES must be at most 10000")
        if not 500 <= self.tool_poll_interval_ms <= 300_000:
            raise ValueError("AGENT_TOOL_POLL_INTERVAL_MS must be between 500 and 300000")
        if not self.tool_poll_interval_ms <= self.tool_poll_max_interval_ms <= 300_000:
            raise ValueError(
                "AGENT_TOOL_POLL_MAX_INTERVAL_MS must be between "
                "AGENT_TOOL_POLL_INTERVAL_MS and 300000"
            )
        if self.max_repair_attempts > 10:
            raise ValueError("AGENT_MAX_REPAIR_ATTEMPTS must be at most 10")
        if self.max_repair_model_calls > 3:
            raise ValueError("AGENT_MAX_REPAIR_MODEL_CALLS must be at most 3")
        if self.max_repair_cost_micros > self.max_cost_micros:
            raise ValueError("AGENT_MAX_REPAIR_COST_MICROS must not exceed the run cost budget")
        if self.max_repair_added_latency_ms > 120_000:
            raise ValueError("AGENT_MAX_REPAIR_ADDED_LATENCY_MS must be at most 120000")

    def limits(self) -> dict[str, Any]:
        return {
            "max_actions": self.max_actions,
            "max_model_calls": self.max_model_calls,
            "max_prompt_tokens": self.max_prompt_tokens,
            "max_completion_tokens": self.max_completion_tokens,
            "max_cost_micros": self.max_cost_micros,
            "max_tool_resumes": self.max_tool_resumes,
            "tool_poll_interval_ms": self.tool_poll_interval_ms,
            "tool_poll_max_interval_ms": self.tool_poll_max_interval_ms,
            "max_repair_attempts": self.max_repair_attempts,
            "max_repair_model_calls": self.max_repair_model_calls,
            "max_repair_cost_micros": self.max_repair_cost_micros,
            "max_repair_added_latency_ms": self.max_repair_added_latency_ms,
            "node_model_calls": {
                name: contract.max_model_calls
                for name, contract in NODE_CONTRACTS.items()
                if contract.max_model_calls > 0
            },
        }


def node_contract_manifest() -> dict[str, dict[str, Any]]:
    return {name: asdict(contract) for name, contract in NODE_CONTRACTS.items()}


def initial_budget_usage() -> dict[str, Any]:
    return {
        "actions": 0,
        "model_calls": 0,
        "prompt_tokens": 0,
        "completion_tokens": 0,
        "cached_tokens": 0,
        "reasoning_tokens": 0,
        "cost_micros": 0,
        "tool_resumes": 0,
        "repair_model_calls": 0,
        "repair_cost_micros": 0,
        "model_calls_by_node": {},
        "model_calls_by_role": {},
    }


def model_budget_exhaustion(
    limits: Mapping[str, Any],
    usage: Mapping[str, Any],
    node: str,
) -> str:
    checks = (
        ("model_calls", "max_model_calls", "模型调用次数"),
        ("prompt_tokens", "max_prompt_tokens", "输入 token"),
        ("completion_tokens", "max_completion_tokens", "输出 token"),
        ("cost_micros", "max_cost_micros", "模型成本"),
    )
    for usage_key, limit_key, label in checks:
        if _non_negative_int(usage.get(usage_key)) >= _positive_int(limits.get(limit_key), 1):
            return f"{label}预算已用完"
    node_limits = limits.get("node_model_calls")
    calls_by_node = usage.get("model_calls_by_node")
    if isinstance(node_limits, Mapping) and isinstance(calls_by_node, Mapping):
        node_limit = _non_negative_int(node_limits.get(node))
        if node_limit and _non_negative_int(calls_by_node.get(node)) >= node_limit:
            return f"节点 {node} 的模型调用预算已用完"
    return ""


def apply_model_events(
    usage: Mapping[str, Any],
    *,
    node: str,
    events: list[dict[str, Any]],
) -> dict[str, Any]:
    updated = dict(usage)
    calls_by_node = _int_mapping(updated.get("model_calls_by_node"))
    calls_by_role = _int_mapping(updated.get("model_calls_by_role"))
    calls = 0
    for event in events:
        if event.get("kind") != "model_call":
            continue
        calls += 1
        role = event.get("role")
        if isinstance(role, str) and role:
            calls_by_role[role] = calls_by_role.get(role, 0) + 1
        for key in (
            "prompt_tokens",
            "completion_tokens",
            "cached_tokens",
            "reasoning_tokens",
            "cost_micros",
        ):
            updated[key] = _non_negative_int(updated.get(key)) + _non_negative_int(event.get(key))
    updated["model_calls"] = _non_negative_int(updated.get("model_calls")) + calls
    if node == "plan_repair_llm":
        updated["repair_model_calls"] = _non_negative_int(updated.get("repair_model_calls")) + calls
        updated["repair_cost_micros"] = _non_negative_int(updated.get("repair_cost_micros")) + sum(
            _non_negative_int(event.get("cost_micros"))
            for event in events
            if event.get("kind") == "model_call"
        )
    calls_by_node[node] = calls_by_node.get(node, 0) + calls
    updated["model_calls_by_node"] = calls_by_node
    updated["model_calls_by_role"] = calls_by_role
    return updated


def model_config_version(manifest: Mapping[str, Any]) -> str:
    value = manifest.get("config_version")
    return value.strip() if isinstance(value, str) else ""


def model_manifest_fingerprint(manifest: Mapping[str, Any]) -> str:
    canonical = {str(key): value for key, value in manifest.items() if key != "fingerprint"}
    try:
        encoded = json.dumps(
            canonical,
            allow_nan=False,
            ensure_ascii=False,
            sort_keys=True,
            separators=(",", ":"),
        ).encode("utf-8")
    except (TypeError, ValueError) as exc:
        raise ValueError("model manifest must be canonical JSON") from exc
    return hashlib.sha256(encoded).hexdigest()


def tool_catalog_fingerprint(catalog: Any) -> str:
    """Return a stable identity for the exact trusted tool contract set."""
    if catalog is None:
        catalog = []
    if not isinstance(catalog, list) or not all(isinstance(item, Mapping) for item in catalog):
        raise ValueError("trusted tool catalog must be an array of objects")
    normalized = [dict(item) for item in catalog]
    try:
        encoded = json.dumps(
            normalized,
            allow_nan=False,
            ensure_ascii=False,
            sort_keys=True,
            separators=(",", ":"),
        ).encode("utf-8")
    except (TypeError, ValueError) as exc:
        raise ValueError("trusted tool catalog must be canonical JSON") from exc
    return hashlib.sha256(encoded).hexdigest()


def _positive_env(name: str, fallback: int) -> int:
    value = os.getenv(name)
    if value is None or not value.strip():
        return fallback
    try:
        result = int(value)
    except ValueError as exc:
        raise ValueError(f"{name} must be a positive integer") from exc
    if result <= 0:
        raise ValueError(f"{name} must be a positive integer")
    return result


def _positive_int(value: Any, fallback: int) -> int:
    if isinstance(value, int) and not isinstance(value, bool) and value > 0:
        return value
    return fallback


def _non_negative_int(value: Any) -> int:
    if isinstance(value, int) and not isinstance(value, bool) and value >= 0:
        return value
    return 0


def _int_mapping(value: Any) -> dict[str, int]:
    if not isinstance(value, Mapping):
        return {}
    return {
        str(key): _non_negative_int(item) for key, item in value.items() if isinstance(key, str)
    }
