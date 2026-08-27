from __future__ import annotations

import json
import math
import os
import re
import time
import uuid
from dataclasses import asdict, dataclass, field
from datetime import date, datetime
from typing import Any, Callable, Literal, Mapping, Protocol, TypedDict, cast

# LangGraph checkpoint deserialization must never import arbitrary Python
# modules from database-controlled payloads.
os.environ.setdefault("LANGGRAPH_STRICT_MSGPACK", "true")

from langgraph.graph import END, START, StateGraph
from langgraph.types import Command, interrupt

from ai_companion_worker.email_quality import (
    EMAIL_DRAFT_POLICY_VERSION,
    quality_report as email_quality_report,
    validate_email_arguments,
)
from ai_companion_worker.response_quality import inspect_and_repair_response
from ai_companion_worker.task_quality import (
    artifact_observation_applicable,
    compile_task_contract,
    presentation_source_record_keys,
    task_contract_artifact_satisfied,
    validate_artifact_observation,
    validate_presentation_arguments,
)
from ai_companion_worker.agent_governance import (
    GRAPH_NAME,
    GRAPH_VERSION,
    NODE_CONTRACTS,
    BudgetPolicy,
    apply_model_events,
    initial_budget_usage,
    model_budget_exhaustion,
    model_config_version,
    model_manifest_fingerprint,
    node_contract_manifest,
    tool_catalog_fingerprint,
)
from ai_companion_worker.tool_repair import (
    REPAIR_CATALOG_VERSION,
    TOOL_FAILURE_CONTRACT_VERSION,
    apply_known_repair,
    apply_restricted_patches,
    canonical_arguments_hash,
    known_repair_available,
    preflight_repair,
    repair_operator_allowed,
    safe_basename,
)

ModuleKey = Literal["companion", "life", "work"]
RiskLevel = Literal["none", "low", "medium", "high"]
ToolStatus = Literal["completed", "requires_confirmation"]
AssessmentStatus = Literal["completed", "continue", "blocked"]
ExecutionMode = Literal["direct", "single_action", "agentic"]


class AgentInput(TypedDict):
    run_id: str
    user_id: str
    conversation_id: str
    character_id: str
    module: ModuleKey
    user_message: str
    context: dict[str, Any]


class AgentState(AgentInput, total=False):
    graph_name: str
    graph_version: str
    plan: dict[str, Any]
    observations: list[dict[str, Any]]
    action_index: int
    action_budget: int
    pending_task_id: str
    task_polls: int
    assessment: dict[str, Any]
    intent: str
    proposed_tool: dict[str, Any]
    preparation: dict[str, Any]
    approval: dict[str, Any]
    tool_result: dict[str, Any]
    response: str
    outcome: str
    steps: int
    needs_response: bool
    node_contracts: dict[str, dict[str, Any]]
    budget_limits: dict[str, Any]
    budget_usage: dict[str, Any]
    model_manifest: dict[str, Any]
    tool_catalog_fingerprint: str
    model_events: list[dict[str, Any]]
    node_trace: list[dict[str, Any]]
    recovery: dict[str, Any]
    failure: dict[str, Any]
    failed_task_id: str
    repair: dict[str, Any]
    repair_attempts: int
    repair_history: list[dict[str, Any]]
    repair_fingerprints: list[str]
    repair_started_at_ms: int
    email_validation: dict[str, Any]
    email_rewrite_attempts: int
    presentation_validation: dict[str, Any]
    presentation_rewrite_attempts: int
    response_validation: dict[str, Any]
    response_rewrite_attempts: int
    execution_mode: ExecutionMode
    execution_mode_reason: str
    task_contract: dict[str, Any]
    artifact_validation: dict[str, Any]
    document_processing: dict[str, Any]


@dataclass(frozen=True)
class ModelDecision:
    intent: str
    response: str = ""
    tool_name: str = ""
    tool_arguments: Mapping[str, Any] = field(default_factory=dict)
    requires_argument_composition: bool = False
    needs_response: bool = False


@dataclass(frozen=True)
class AgentPlan:
    objective: str
    steps: tuple[str, ...]
    success_criteria: str


@dataclass(frozen=True)
class AgentAssessment:
    status: AssessmentStatus
    reason: str


@dataclass(frozen=True)
class RepairDecision:
    strategy: Literal["operator", "restricted_patch", "ask_user", "abort"]
    operator_id: str = ""
    patches: tuple[Mapping[str, Any], ...] = ()
    reason_code: str = ""


class ModelBudgetExceeded(RuntimeError):
    """Raised before dispatch when no safe model allowance remains."""


@dataclass(frozen=True)
class ToolPreparation:
    status: ToolStatus
    tool_name: str
    response: str = ""
    risk_level: RiskLevel = "none"
    summary: str = ""
    confirmation_token: str = ""
    normalized_arguments: Mapping[str, Any] = field(default_factory=dict)
    data: Any = field(default_factory=dict)


@dataclass(frozen=True)
class ToolOutcome:
    response: str
    data: Any = field(default_factory=dict)


class DecisionPort(Protocol):
    def plan(
        self,
        *,
        module: ModuleKey,
        message: str,
        context: Mapping[str, Any],
    ) -> AgentPlan: ...

    def decide(
        self,
        *,
        module: ModuleKey,
        message: str,
        context: Mapping[str, Any],
    ) -> ModelDecision: ...

    def compose_arguments(
        self,
        *,
        module: ModuleKey,
        message: str,
        tool_name: str,
        context: Mapping[str, Any],
    ) -> Mapping[str, Any]: ...

    def respond(
        self,
        *,
        module: ModuleKey,
        message: str,
        context: Mapping[str, Any],
    ) -> str: ...

    def revise_response(
        self,
        *,
        module: ModuleKey,
        message: str,
        response: str,
        violations: list[Mapping[str, Any]],
        context: Mapping[str, Any],
    ) -> str: ...

    def assess(
        self,
        *,
        module: ModuleKey,
        message: str,
        context: Mapping[str, Any],
    ) -> AgentAssessment: ...

    def repair(
        self,
        *,
        module: ModuleKey,
        message: str,
        tool_name: str,
        arguments: Mapping[str, Any],
        failure: Mapping[str, Any],
        context: Mapping[str, Any],
    ) -> RepairDecision: ...


class ToolGateway(Protocol):
    def prepare(
        self,
        *,
        run_id: str,
        user_id: str,
        module: ModuleKey,
        tool_name: str,
        arguments: Mapping[str, Any],
        idempotency_key: str,
    ) -> ToolPreparation: ...

    def commit(
        self,
        *,
        run_id: str,
        user_id: str,
        module: ModuleKey,
        preparation: Mapping[str, Any],
        resolution: Mapping[str, Any],
        idempotency_key: str,
    ) -> ToolOutcome: ...

    def observe(
        self,
        *,
        run_id: str,
        user_id: str,
        module: ModuleKey,
        task_id: str,
    ) -> ToolOutcome: ...

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
    ) -> ToolOutcome: ...


_ALLOWED_TOOL_PREFIXES: dict[ModuleKey, tuple[str, ...]] = {
    "companion": ("companion_", "companion.", "memory_", "memory."),
    "life": ("life_", "life.", "memory_", "memory."),
    "work": (
        "work_",
        "work.",
        "office_",
        "office.",
        "document_",
        "document.",
        "memory_",
        "memory.",
    ),
}


def runtime_config(run_id: str) -> dict[str, Any]:
    normalized = run_id.strip()
    if not normalized:
        raise ValueError("run_id is required")
    return {
        # The top-level LangGraph namespace must remain empty; non-empty
        # checkpoint_ns values are reserved for compiled subgraphs. Version
        # compatibility is enforced by the persisted graph identity instead.
        "configurable": {"thread_id": normalized},
        "recursion_limit": 256,
    }


def tool_allowed(module: ModuleKey, tool_name: str) -> bool:
    normalized = tool_name.strip().lower()
    return bool(normalized) and normalized.startswith(_ALLOWED_TOOL_PREFIXES[module])


def _tool_requires_argument_composition(
    state: AgentState,
    tool_name: str,
) -> bool:
    definition = _trusted_tool_definition(state, tool_name)
    return bool(definition is not None and definition.get("compose_arguments") is True)


def _trusted_tool_definition(
    state: AgentState,
    tool_name: str,
) -> Mapping[str, Any] | None:
    context = state.get("context", {})
    definitions = context.get("tools") if isinstance(context, dict) else None
    # Unit-level or development ports may not supply a catalog. Production
    # Agent runs always receive the Go-owned list under this key.
    if definitions is None:
        return None
    if not isinstance(definitions, list):
        raise ValueError("trusted tool catalog must be an array")
    matches = [
        item for item in definitions if isinstance(item, dict) and item.get("name") == tool_name
    ]
    if not matches:
        raise ValueError("trusted tool catalog does not contain the selected tool")
    if len(matches) != 1:
        raise ValueError("trusted tool catalog contains duplicate tool names")
    return cast(Mapping[str, Any], matches[0])


def _validate_tool_arguments(
    arguments: Mapping[str, Any],
    definition: Mapping[str, Any],
) -> None:
    schema = definition.get("parameters")
    if not isinstance(schema, Mapping):
        raise ValueError("trusted tool parameter schema must be an object")
    _validate_schema_value(dict(arguments), schema, path="arguments", depth=0)


def _normalize_presentation_arguments(
    arguments: Mapping[str, Any],
    state: AgentState,
) -> dict[str, Any]:
    """Apply semantics-preserving defaults before any presentation gate.

    Tool-calling providers may omit a required title even when the supplied
    schema marks it required. The title is identity metadata, so it is safe to
    derive it from the already-composed table title or trusted plan objective.
    Visible table headings are also localized deterministically when the task
    contract explicitly requires Chinese output.
    """

    normalized = dict(arguments)
    filename = normalized.get("filename")
    if isinstance(filename, str) and filename.strip():
        # The presentation quality gate precedes generic preflight in the
        # graph. Apply the same allowlisted basename normalization here so a
        # repairable path cannot be trapped behind that gate.
        normalized["filename"] = safe_basename(filename, ".pptx")
    table = normalized.get("table")
    table_copy = dict(table) if isinstance(table, Mapping) else None
    title = normalized.get("title")
    if not isinstance(title, str) or not title.strip():
        table_title = table_copy.get("title") if table_copy is not None else None
        plan = state.get("plan", {})
        objective = plan.get("objective") if isinstance(plan, Mapping) else None
        fallback = (
            table_title if isinstance(table_title, str) and table_title.strip() else objective
        )
        normalized["title"] = (
            fallback.strip() if isinstance(fallback, str) and fallback.strip() else "演示文稿"
        )

    task_contract = state.get("task_contract", {})
    if table_copy is not None:
        # Only the trusted table fields may cross the quality gate. Models
        # commonly nest harness-owned fields such as source_coverage inside the
        # table during a repair pass, while the schema rejects all unknown
        # properties.
        table_copy = {
            key: table_copy[key] for key in ("title", "columns", "rows") if key in table_copy
        }
        requested = _presentation_requested_columns(task_contract)
        columns = table_copy.get("columns")
        rows = table_copy.get("rows")
        if requested and isinstance(columns, list) and isinstance(rows, list):
            column_fields = [_presentation_column_field(value) for value in columns]
            requested_fields = [field for field, _ in requested]
            selected_indices: list[int] = []
            for field in requested_fields:
                try:
                    selected_indices.append(column_fields.index(field))
                except ValueError:
                    selected_indices = []
                    break
            source_index = next(
                (index for index, field in enumerate(column_fields) if field == "source_locator"),
                -1,
            )
            normalized_rows: list[dict[str, Any]] = []
            for raw_row in rows:
                if not isinstance(raw_row, Mapping):
                    continue
                cells = raw_row.get("cells")
                if not isinstance(cells, list):
                    continue
                if selected_indices and max(selected_indices, default=-1) < len(cells):
                    visible_cells = [str(cells[index]).strip() for index in selected_indices]
                elif len(cells) >= len(requested_fields):
                    # When headings are localized but still ordered according
                    # to the task contract, preserve that trusted order.
                    visible_cells = [str(value).strip() for value in cells[: len(requested_fields)]]
                else:
                    visible_cells = [str(value).strip() for value in cells]
                locator = str(raw_row.get("source_locator") or "").strip()
                if not locator and 0 <= source_index < len(cells):
                    locator = str(cells[source_index] or "").strip()
                if not locator:
                    locator = _presentation_source_locator(visible_cells, state)
                for expanded_cells in _expand_presentation_cells(visible_cells):
                    normalized_rows.append({"cells": expanded_cells, "source_locator": locator})
            chinese = isinstance(task_contract, Mapping) and str(
                task_contract.get("output_language") or ""
            ).casefold().startswith("zh")
            table_copy["columns"] = [label if chinese else field for field, label in requested]
            table_copy["rows"] = normalized_rows
        elif isinstance(columns, list):
            aliases = {
                "code": "课程代码",
                "name": "课程名称",
                "time": "上课时间",
                "date": "上课时间",
                "schedule": "上课时间",
                "venue": "上课地点",
                "location": "上课地点",
                "source_locator": "来源位置",
            }
            table_copy["columns"] = [
                aliases.get(str(column).strip().casefold(), column) for column in columns
            ]
        normalized["table"] = table_copy
    return normalized


def _presentation_requested_columns(task_contract: Any) -> list[tuple[str, str]]:
    if not isinstance(task_contract, Mapping):
        return []
    values = task_contract.get("requested_fields")
    if not isinstance(values, list):
        return []
    labels = {
        "code": "课程代码",
        "name": "课程名称",
        "time": "上课时间",
        "date": "日期",
        "schedule": "上课时间",
        "venue": "上课地点",
        "location": "上课地点",
    }
    result: list[tuple[str, str]] = []
    for value in values:
        field = str(value or "").strip().casefold()
        if field in ("date", "schedule"):
            field = "time"
        elif field == "location":
            field = "venue"
        if field and field in labels and field not in {item[0] for item in result}:
            result.append((field, labels[field]))
    return result


def _presentation_column_field(value: Any) -> str:
    normalized = re.sub(r"[\s_\-/:：]+", "", str(value or "").strip().casefold())
    aliases = {
        "code": "code",
        "coursecode": "code",
        "课程代码": "code",
        "课程编号": "code",
        "name": "name",
        "coursename": "name",
        "课程名称": "name",
        "课程名": "name",
        "time": "time",
        "date": "time",
        "schedule": "time",
        "datetime": "time",
        "上课时间": "time",
        "时间": "time",
        "日期": "time",
        "venue": "venue",
        "location": "venue",
        "上课地点": "venue",
        "地点": "venue",
        "sourcelocator": "source_locator",
        "source": "source_locator",
        "来源位置": "source_locator",
        "来源": "source_locator",
    }
    return aliases.get(normalized, normalized)


_PRESENTATION_CELL_RENDER_LIMIT = 120
_PRESENTATION_PAGE_MARKER = re.compile(r"\[\[(?P<page>PAGES?\s+\d+(?:-\d+)?)\]\]", re.I)


def _presentation_source_locator(cells: list[str], state: AgentState) -> str:
    """Recover a grounded row locator from the extracted source when omitted."""

    needles = [str(value).strip() for value in cells if str(value).strip()]
    if not needles:
        return ""
    # Course and record identifiers are normally the first visible field and
    # are much safer anchors than a generic name or time fragment.
    needle = needles[0]
    for observation in state.get("observations", []):
        if not isinstance(observation, Mapping):
            continue
        data = observation.get("data")
        output = data.get("output") if isinstance(data, Mapping) else None
        if not isinstance(output, Mapping):
            continue
        text = output.get("text")
        if not isinstance(text, str):
            continue
        match = re.search(re.escape(needle), text, re.I)
        if match is None:
            continue
        markers = list(_PRESENTATION_PAGE_MARKER.finditer(text, 0, match.start()))
        if markers:
            return markers[-1].group("page").title()
        filename = str(output.get("source_filename") or "").strip()
        round_start = int(output.get("round_start") or 1)
        if filename:
            return f"{filename}, round {round_start}"[:160]
    return ""


def _split_presentation_cell(value: Any) -> list[str]:
    """Split dense cell text without dropping non-whitespace source content."""

    remaining = re.sub(r"\s+", " ", str(value or "")).strip()
    if len(remaining) <= _PRESENTATION_CELL_RENDER_LIMIT:
        return [remaining]
    result: list[str] = []
    while len(remaining) > _PRESENTATION_CELL_RENDER_LIMIT:
        window = remaining[: _PRESENTATION_CELL_RENDER_LIMIT + 1]
        candidates = [
            (window.rfind(";"), 1),
            (window.rfind("；"), 1),
            (window.rfind(" "), 0),
        ]
        boundary, suffix = max(candidates, key=lambda item: item[0])
        if boundary < _PRESENTATION_CELL_RENDER_LIMIT // 2:
            cut = _PRESENTATION_CELL_RENDER_LIMIT
        else:
            cut = boundary + suffix
        segment = remaining[:cut].strip()
        if segment:
            result.append(segment)
        remaining = remaining[cut:].strip()
    if remaining:
        result.append(remaining)
    return result or [""]


def _expand_presentation_cells(cells: list[str]) -> list[list[str]]:
    """Expand one dense record into readable continuation rows losslessly."""

    segmented = [_split_presentation_cell(value) for value in cells]
    row_count = max((len(items) for items in segmented), default=1)
    result: list[list[str]] = []
    for index in range(row_count):
        row: list[str] = []
        for items in segmented:
            if len(items) == 1:
                row.append(items[0])
            elif index < len(items):
                row.append(items[index])
            else:
                row.append("同上")
        result.append(row)
    return result


_DOCUMENT_ROUND_MARKER = re.compile(r"(?m)^\[\[DOCUMENT ROUND (?P<round>\d+)\]\]\s*$")
_DOCUMENT_PROCESSING_OVERLAP_CHARS = 1_200


def _presentation_document_batches(state: AgentState) -> list[dict[str, Any]]:
    """Return ordered, de-duplicated extraction rounds for downstream work."""

    batches: dict[tuple[int, int], dict[str, Any]] = {}
    for observation in state.get("observations", []):
        if not isinstance(observation, Mapping):
            continue
        if observation.get("tool_name") != "work_extract_attached_document":
            continue
        arguments = observation.get("arguments")
        attachment_index = (
            int(arguments.get("attachment_index") or 1) if isinstance(arguments, Mapping) else 1
        )
        data = observation.get("data")
        output = data.get("output") if isinstance(data, Mapping) else None
        if not isinstance(output, Mapping):
            continue
        text = output.get("text")
        if not isinstance(text, str) or not text.strip():
            continue
        manifest = output.get("rounds")
        manifest_items = manifest if isinstance(manifest, list) else []
        tokens_by_round: dict[int, int] = {}
        for item in manifest_items:
            if not isinstance(item, Mapping):
                continue
            item_round = item.get("round_no")
            if isinstance(item_round, int) and not isinstance(item_round, bool):
                tokens_by_round[item_round] = int(item.get("token_count") or 0)
        matches = list(_DOCUMENT_ROUND_MARKER.finditer(text))
        if matches:
            for index, match in enumerate(matches):
                round_no = int(match.group("round"))
                start = match.end()
                end = matches[index + 1].start() if index + 1 < len(matches) else len(text)
                round_text = text[start:end].strip()
                if round_text:
                    batches[(attachment_index, round_no)] = {
                        "attachment_index": attachment_index,
                        "round_no": round_no,
                        "text": round_text,
                        "token_count": tokens_by_round.get(round_no, 0),
                        "source_filename": str(output.get("source_filename") or ""),
                    }
            continue
        # Backward-compatible fallback for observations created before round
        # markers were introduced.  Such a window remains one bounded batch.
        round_no = int(output.get("round_start") or 1)
        batches[(attachment_index, round_no)] = {
            "attachment_index": attachment_index,
            "round_no": round_no,
            "text": text.strip(),
            "token_count": int(output.get("token_count") or 0),
            "source_filename": str(output.get("source_filename") or ""),
        }
    ordered = [batches[key] for key in sorted(batches)]
    previous_tail = ""
    for batch in ordered:
        current = str(batch["text"])
        if previous_tail:
            batch["processing_text"] = (
                "[[PREVIOUS ROUND OVERLAP — CONTEXT ONLY]]\n"
                + previous_tail
                + "\n[[CURRENT ROUND]]\n"
                + current
            )
        else:
            batch["processing_text"] = current
        previous_tail = current[-_DOCUMENT_PROCESSING_OVERLAP_CHARS:]
        batch["batch_id"] = f"a{batch['attachment_index']}:r{batch['round_no']}"
    return ordered


def _document_batch_observation(batch: Mapping[str, Any]) -> dict[str, Any]:
    locator = f"attachment:{batch.get('attachment_index', 1)} round:{batch.get('round_no', 1)}"
    return {
        "tool_name": "work_extract_attached_document",
        "arguments": {
            "attachment_index": int(batch.get("attachment_index") or 1),
            "round_start": int(batch.get("round_no") or 1),
        },
        "status": "completed",
        "response": f"正在处理文档分轮 {locator}",
        "data": {
            "output": {
                "source_filename": str(batch.get("source_filename") or ""),
                "format": "markdown",
                "text": str(batch.get("processing_text") or batch.get("text") or ""),
                "round_start": int(batch.get("round_no") or 1),
                "completed_rounds": 1,
                "round_count": 1,
                "truncated": False,
                "coverage_ratio": 1.0,
                "source_locator": locator,
            }
        },
    }


def _presentation_row_key(row: Mapping[str, Any]) -> str:
    cells = row.get("cells")
    if not isinstance(cells, list):
        return ""
    normalized = [re.sub(r"\s+", " ", str(value or "").strip()).casefold() for value in cells]
    return json.dumps(normalized, ensure_ascii=False, separators=(",", ":"))


def _merge_presentation_arguments(
    base: Mapping[str, Any],
    addition: Mapping[str, Any],
    state: AgentState,
) -> dict[str, Any]:
    merged = dict(base)
    for key in ("title", "audience", "style", "brief", "filename"):
        value = addition.get(key)
        if key not in merged or not str(merged.get(key) or "").strip():
            if value is not None:
                merged[key] = value
    base_table = base.get("table")
    added_table = addition.get("table")
    if isinstance(base_table, Mapping) or isinstance(added_table, Mapping):
        first = dict(base_table) if isinstance(base_table, Mapping) else {}
        second = dict(added_table) if isinstance(added_table, Mapping) else {}
        columns = first.get("columns") or second.get("columns") or []
        rows: list[dict[str, Any]] = []
        row_positions: dict[str, int] = {}
        for raw in [*(first.get("rows") or []), *(second.get("rows") or [])]:
            if not isinstance(raw, Mapping):
                continue
            row = {
                "cells": list(raw.get("cells") or []),
                "source_locator": str(raw.get("source_locator") or "").strip(),
            }
            key = _presentation_row_key(row)
            if not key:
                continue
            if key in row_positions:
                position = row_positions[key]
                locators = [
                    value.strip()
                    for value in (
                        rows[position].get("source_locator", ""),
                        row.get("source_locator", ""),
                    )
                    if isinstance(value, str) and value.strip()
                ]
                rows[position]["source_locator"] = "; ".join(dict.fromkeys(locators))[:160]
                continue
            row_positions[key] = len(rows)
            rows.append(row)
        merged["table"] = {
            "title": str(first.get("title") or second.get("title") or "").strip()[:60],
            "columns": list(columns),
            "rows": rows,
        }
        merged["brief"] = f"依据已逐轮提取、校验并合并的 {len(rows)} 条来源记录生成结构化演示文稿。"
        requested_slides = max(
            int(base.get("slide_count") or 0),
            int(addition.get("slide_count") or 0),
            3,
        )
        merged["slide_count"] = min(20, max(requested_slides, math.ceil(len(rows) / 6) + 2))
    return _normalize_presentation_arguments(merged, state)


def _presentation_repair_base(arguments: Mapping[str, Any], report: Any) -> dict[str, Any]:
    base = dict(arguments)
    table = base.get("table")
    if not isinstance(table, Mapping) or not isinstance(report, Mapping):
        return base
    affected: set[int] = set()
    violations = report.get("violations")
    if isinstance(violations, list):
        for violation in violations:
            if not isinstance(violation, Mapping):
                continue
            rows = violation.get("affected_rows")
            if isinstance(rows, list):
                affected.update(
                    int(value)
                    for value in rows
                    if isinstance(value, int) and not isinstance(value, bool) and value > 0
                )
    table_copy = dict(table)
    rows = table_copy.get("rows")
    if affected and isinstance(rows, list):
        table_copy["rows"] = [
            row for index, row in enumerate(rows, start=1) if index not in affected
        ]
    table_copy.pop("source_locator", None)
    base["table"] = table_copy
    return base


def _missing_presentation_record_keys(
    arguments: Mapping[str, Any],
    expected: list[str],
) -> list[str]:
    table = arguments.get("table")
    rows = table.get("rows") if isinstance(table, Mapping) else None
    observed: set[str] = set()
    if isinstance(rows, list):
        for row in rows:
            cells = row.get("cells") if isinstance(row, Mapping) else None
            if not isinstance(cells, list) or not cells:
                continue
            code = re.sub(r"[^A-Z0-9]", "", str(cells[0] or "").upper())
            observed.update(key for key in expected if key in code)
    return [key for key in expected if key not in observed]


def _repair_document_batches(
    batches: list[dict[str, Any]],
    base_arguments: Mapping[str, Any],
    state: AgentState,
) -> tuple[list[dict[str, Any]], list[str]]:
    expected = presentation_source_record_keys(
        state.get("observations", []),
        state.get("task_contract", {}),
    )
    missing = _missing_presentation_record_keys(base_arguments, expected)
    if not missing:
        return batches, []
    selected = [
        batch
        for batch in batches
        if any(
            key
            in re.sub(
                r"[^A-Z0-9]",
                "",
                str(batch.get("text") or "").upper(),
            )
            for key in missing
        )
    ]
    return selected or batches, missing


def _validate_schema_value(
    value: Any,
    schema: Mapping[str, Any],
    *,
    path: str,
    depth: int,
) -> None:
    if depth > 8:
        raise ValueError("tool argument schema is too deeply nested")
    unsupported = sorted(
        keyword
        for keyword in (
            "$ref",
            "$defs",
            "if",
            "then",
            "else",
            "dependentRequired",
            "dependentSchemas",
            "patternProperties",
            "propertyNames",
            "contains",
            "minContains",
            "maxContains",
            "prefixItems",
            "unevaluatedProperties",
            "unevaluatedItems",
        )
        if keyword in schema
    )
    if unsupported:
        raise ValueError(f"trusted tool schema keyword {unsupported[0]!r} is unsupported")

    if "const" in schema and value != schema["const"]:
        raise ValueError(f"{path} does not match the required constant")

    all_of = schema.get("allOf")
    if all_of is not None:
        if not isinstance(all_of, list) or not all(isinstance(item, Mapping) for item in all_of):
            raise ValueError("trusted allOf must contain schemas")
        for item in all_of:
            _validate_schema_value(value, item, path=path, depth=depth + 1)

    for keyword, minimum_matches, maximum_matches in (
        ("anyOf", 1, None),
        ("oneOf", 1, 1),
    ):
        alternatives = schema.get(keyword)
        if alternatives is None:
            continue
        if (
            not isinstance(alternatives, list)
            or not alternatives
            or not all(isinstance(item, Mapping) for item in alternatives)
        ):
            raise ValueError(f"trusted {keyword} must contain schemas")
        matches = sum(
            _schema_matches(value, item, path=path, depth=depth + 1) for item in alternatives
        )
        if matches < minimum_matches or (maximum_matches is not None and matches > maximum_matches):
            raise ValueError(f"{path} does not satisfy {keyword}")

    excluded = schema.get("not")
    if excluded is not None:
        if not isinstance(excluded, Mapping):
            raise ValueError("trusted not must contain a schema")
        if _schema_matches(value, excluded, path=path, depth=depth + 1):
            raise ValueError(f"{path} matches a forbidden schema")

    expected = schema.get("type")
    if isinstance(expected, list):
        if not expected or not all(isinstance(item, str) for item in expected):
            raise ValueError("trusted schema type list must contain strings")
        type_schema = {
            key: item
            for key, item in schema.items()
            if key not in ("allOf", "anyOf", "oneOf", "not")
        }
        if not any(
            _schema_matches(
                value,
                {**type_schema, "type": item},
                path=path,
                depth=depth + 1,
            )
            for item in expected
        ):
            raise ValueError(f"{path} does not match an allowed type")
        return
    if expected == "object":
        if not isinstance(value, dict):
            raise ValueError(f"{path} must be an object")
        properties = schema.get("properties", {})
        if not isinstance(properties, Mapping):
            raise ValueError("trusted object properties must be an object")
        required = schema.get("required", [])
        if not isinstance(required, list) or not all(isinstance(item, str) for item in required):
            raise ValueError("trusted object required fields must be strings")
        missing = [item for item in required if item not in value]
        if missing:
            raise ValueError(f"{path} is missing required field {missing[0]}")
        if schema.get("additionalProperties") is False:
            extras = [str(key) for key in value if key not in properties]
            if extras:
                raise ValueError(f"{path} contains unsupported field {extras[0]}")
        minimum = schema.get("minProperties")
        maximum = schema.get("maxProperties")
        if isinstance(minimum, int) and len(value) < minimum:
            raise ValueError(f"{path} has too few properties")
        if isinstance(maximum, int) and len(value) > maximum:
            raise ValueError(f"{path} has too many properties")
        additional = schema.get("additionalProperties")
        for key, item in value.items():
            child = properties.get(key)
            if isinstance(child, Mapping):
                _validate_schema_value(
                    item,
                    child,
                    path=f"{path}.{key}",
                    depth=depth + 1,
                )
            elif isinstance(additional, Mapping):
                _validate_schema_value(
                    item,
                    additional,
                    path=f"{path}.{key}",
                    depth=depth + 1,
                )
    elif expected == "array":
        if not isinstance(value, list):
            raise ValueError(f"{path} must be an array")
        minimum, maximum = schema.get("minItems"), schema.get("maxItems")
        if isinstance(minimum, int) and len(value) < minimum:
            raise ValueError(f"{path} has too few items")
        if isinstance(maximum, int) and len(value) > maximum:
            raise ValueError(f"{path} has too many items")
        if schema.get("uniqueItems") is True:
            encoded_items: list[str] = []
            for item in value:
                try:
                    encoded_items.append(
                        json.dumps(
                            item,
                            allow_nan=False,
                            ensure_ascii=False,
                            sort_keys=True,
                            separators=(",", ":"),
                        )
                    )
                except (TypeError, ValueError) as exc:
                    raise ValueError(f"{path} contains a non-JSON value") from exc
            if len(set(encoded_items)) != len(encoded_items):
                raise ValueError(f"{path} must contain unique items")
        item_schema = schema.get("items")
        if isinstance(item_schema, Mapping):
            for index, item in enumerate(value):
                _validate_schema_value(
                    item,
                    item_schema,
                    path=f"{path}[{index}]",
                    depth=depth + 1,
                )
    elif expected == "string":
        if not isinstance(value, str):
            raise ValueError(f"{path} must be a string")
        minimum, maximum = schema.get("minLength"), schema.get("maxLength")
        if isinstance(minimum, int) and len(value) < minimum:
            raise ValueError(f"{path} is too short")
        if isinstance(maximum, int) and len(value) > maximum:
            raise ValueError(f"{path} is too long")
        pattern = schema.get("pattern")
        if pattern is not None:
            if not isinstance(pattern, str):
                raise ValueError("trusted string pattern must be a string")
            try:
                matched = re.search(pattern, value) is not None
            except re.error as exc:
                raise ValueError("trusted string pattern is invalid") from exc
            if not matched:
                raise ValueError(f"{path} does not match the required pattern")
        value_format = schema.get("format")
        if value_format is not None:
            if not isinstance(value_format, str):
                raise ValueError("trusted string format must be a string")
            _validate_string_format(value, value_format, path)
    elif expected == "integer":
        if not isinstance(value, int) or isinstance(value, bool):
            raise ValueError(f"{path} must be an integer")
        minimum, maximum = schema.get("minimum"), schema.get("maximum")
        if isinstance(minimum, (int, float)) and value < minimum:
            raise ValueError(f"{path} is below its minimum")
        if isinstance(maximum, (int, float)) and value > maximum:
            raise ValueError(f"{path} is above its maximum")
        _validate_numeric_constraints(value, schema, path)
    elif expected == "number":
        if not isinstance(value, (int, float)) or isinstance(value, bool):
            raise ValueError(f"{path} must be a number")
        _validate_numeric_constraints(value, schema, path)
    elif expected == "boolean":
        if not isinstance(value, bool):
            raise ValueError(f"{path} must be a boolean")
    elif expected == "null":
        if value is not None:
            raise ValueError(f"{path} must be null")
    elif expected is not None:
        raise ValueError(f"trusted schema contains unsupported type {expected!r}")
    enum = schema.get("enum")
    if isinstance(enum, list) and value not in enum:
        raise ValueError(f"{path} is outside the allowed values")


def _schema_matches(
    value: Any,
    schema: Mapping[str, Any],
    *,
    path: str,
    depth: int,
) -> bool:
    try:
        _validate_schema_value(value, schema, path=path, depth=depth)
    except ValueError:
        return False
    return True


def _validate_numeric_constraints(
    value: int | float,
    schema: Mapping[str, Any],
    path: str,
) -> None:
    if not math.isfinite(value):
        raise ValueError(f"{path} must be a finite number")
    minimum = schema.get("minimum")
    maximum = schema.get("maximum")
    exclusive_minimum = schema.get("exclusiveMinimum")
    exclusive_maximum = schema.get("exclusiveMaximum")
    if isinstance(minimum, (int, float)) and value < minimum:
        raise ValueError(f"{path} is below its minimum")
    if isinstance(maximum, (int, float)) and value > maximum:
        raise ValueError(f"{path} is above its maximum")
    if isinstance(exclusive_minimum, (int, float)) and value <= exclusive_minimum:
        raise ValueError(f"{path} is below its exclusive minimum")
    if isinstance(exclusive_maximum, (int, float)) and value >= exclusive_maximum:
        raise ValueError(f"{path} is above its exclusive maximum")
    multiple = schema.get("multipleOf")
    if isinstance(multiple, (int, float)):
        if not math.isfinite(multiple) or multiple <= 0:
            raise ValueError("trusted multipleOf must be positive")
        quotient = value / multiple
        if abs(quotient - round(quotient)) > 1e-9:
            raise ValueError(f"{path} is not a permitted multiple")


def _validate_string_format(value: str, value_format: str, path: str) -> None:
    try:
        if value_format == "email":
            if re.fullmatch(r"[^@\s]+@[^@\s]+\.[^@\s]+", value) is None:
                raise ValueError
        elif value_format == "date":
            date.fromisoformat(value)
        elif value_format == "date-time":
            datetime.fromisoformat(value.replace("Z", "+00:00"))
        elif value_format == "uuid":
            uuid.UUID(value)
        else:
            raise ValueError(f"trusted string format {value_format!r} is unsupported")
    except (ValueError, TypeError) as exc:
        if str(exc).startswith("trusted string format"):
            raise
        raise ValueError(f"{path} is not a valid {value_format}") from exc


def _validate_input(value: Mapping[str, Any]) -> AgentInput:
    required = (
        "run_id",
        "user_id",
        "conversation_id",
        "character_id",
        "module",
        "user_message",
    )
    for key in required:
        if not isinstance(value.get(key), str) or not str(value[key]).strip():
            raise ValueError(f"{key} is required")
    module = value["module"]
    if module not in _ALLOWED_TOOL_PREFIXES:
        raise ValueError("module must be companion, life, or work")
    context = value.get("context", {})
    if not isinstance(context, dict):
        raise ValueError("context must be an object")
    return cast(
        AgentInput,
        {
            "run_id": str(value["run_id"]).strip(),
            "user_id": str(value["user_id"]).strip(),
            "conversation_id": str(value["conversation_id"]).strip(),
            "character_id": str(value["character_id"]).strip(),
            "module": module,
            "user_message": str(value["user_message"]).strip(),
            "context": dict(context),
        },
    )


def _supervise(
    state: AgentState,
    *,
    budget_policy: BudgetPolicy,
    manifest: Mapping[str, Any],
) -> dict[str, Any]:
    _validate_input(state)
    limits = budget_policy.limits()
    context = state.get("context", {})
    catalog = context.get("tools", []) if isinstance(context, dict) else []
    catalog_fingerprint = tool_catalog_fingerprint(catalog)
    return {
        "graph_name": GRAPH_NAME,
        "graph_version": GRAPH_VERSION,
        "observations": [],
        "action_index": 0,
        "action_budget": budget_policy.max_actions,
        "pending_task_id": "",
        "task_polls": 0,
        "assessment": {},
        "task_contract": compile_task_contract(
            state["user_message"],
            state["module"],
            context.get("history") if isinstance(context, dict) else None,
        ),
        "artifact_validation": {},
        "document_processing": {},
        "needs_response": False,
        "node_contracts": node_contract_manifest(),
        "budget_limits": limits,
        "budget_usage": initial_budget_usage(),
        "model_manifest": dict(manifest),
        "tool_catalog_fingerprint": catalog_fingerprint,
        "model_events": [],
        "failure": {},
        "failed_task_id": "",
        "repair": {},
        "repair_attempts": 0,
        "repair_history": [],
        "repair_fingerprints": [],
        "repair_started_at_ms": 0,
        "email_validation": {},
        "email_rewrite_attempts": 0,
        "presentation_validation": {},
        "presentation_rewrite_attempts": 0,
        "response_validation": {},
        "response_rewrite_attempts": 0,
        "execution_mode": "direct",
        "execution_mode_reason": "pending_initial_decision",
        "node_trace": [
            _trace_event(
                "supervisor",
                "succeeded",
                details={
                    "graph_version": GRAPH_VERSION,
                    "model_config_version": model_config_version(manifest),
                    "model_manifest_fingerprint": manifest.get("fingerprint", ""),
                    "tool_catalog_fingerprint": catalog_fingerprint,
                },
            )
        ],
        "recovery": {
            "checkpoint_namespace": "top-level",
            "graph_identity": f"{GRAPH_NAME}@{GRAPH_VERSION}",
            "tool_catalog_fingerprint": catalog_fingerprint,
            "semantics": "at_least_once_with_idempotent_side_effects",
            "approval_resumes": 0,
            "tool_resumes": 0,
            "last_interrupt_type": "",
            "tool_failure_contract_version": TOOL_FAILURE_CONTRACT_VERSION,
            "repair_catalog_version": REPAIR_CATALOG_VERSION,
        },
        "steps": 1,
    }


def _module_route(state: AgentState) -> str:
    return state["module"]


def _classify_execution_mode(
    state: AgentState,
    *,
    tool_name: str,
    definition: Mapping[str, Any] | None,
) -> tuple[ExecutionMode, str]:
    existing = state.get("execution_mode")
    if existing == "agentic" or state.get("plan"):
        return "agentic", "existing_multi_step_plan"
    if not tool_name:
        return "direct", "model_answered_without_project_tool"
    if definition is not None and definition.get("requires_plan") is True:
        return "agentic", "trusted_tool_requires_precondition_plan"
    if definition is not None and definition.get("repeatable") is True:
        return "agentic", "repeatable_tool_requires_progress_tracking"
    normalized = tool_name.strip().lower().replace(".", "_")
    if normalized == "work_extract_attached_document":
        return "agentic", "intermediate_artifact_step"
    return "single_action", "one_trusted_tool_can_complete_request"


def build_graph(
    *,
    checkpointer: Any,
    decisions: DecisionPort,
    tools: ToolGateway,
    budget_policy: BudgetPolicy | None = None,
) -> Any:
    policy = budget_policy or BudgetPolicy.from_env()
    policy.validate()
    manifest = _decision_manifest(decisions)

    def supervise(state: AgentState) -> dict[str, Any]:
        return _supervise(state, budget_policy=policy, manifest=manifest)

    def model_access_reason(state: AgentState, node: str) -> str:
        persisted_manifest = state.get("model_manifest", {})
        persisted_version = model_config_version(persisted_manifest)
        current_version = model_config_version(manifest)
        if persisted_version and current_version and persisted_version != current_version:
            return (
                "模型配置版本已从 "
                f"{persisted_version} 变更为 {current_version}，不能在旧检查点上混用"
            )
        if persisted_manifest:
            persisted_fingerprint = model_manifest_fingerprint(persisted_manifest)
            recorded_fingerprint = persisted_manifest.get("fingerprint")
            if (
                isinstance(recorded_fingerprint, str)
                and recorded_fingerprint
                and recorded_fingerprint != persisted_fingerprint
            ):
                return "模型配置清单指纹校验失败，不能继续使用该检查点"
            if persisted_fingerprint != model_manifest_fingerprint(manifest):
                return "模型配置清单与当前运行时不一致，不能在旧检查点上混用"
        return model_budget_exhaustion(
            state.get("budget_limits", policy.limits()),
            state.get("budget_usage", initial_budget_usage()),
            node,
        )

    def model_terminal_update(
        state: AgentState,
        *,
        node: str,
        reason: str,
    ) -> dict[str, Any]:
        outcome = (
            "model_version_mismatch" if reason.startswith("模型配置") else "model_budget_exhausted"
        )
        return {
            "outcome": outcome,
            "response": f"任务已安全停止：{reason}。请保留已有结果并发起新的运行。",
            "needs_response": False,
            "node_trace": [
                *state.get("node_trace", []),
                _trace_event(node, "blocked", details={"reason": reason}),
            ],
            "steps": state.get("steps", 0) + 1,
        }

    def with_model_allowance(
        state: AgentState,
        context: Mapping[str, Any],
        *,
        node: str,
    ) -> dict[str, Any]:
        updated = dict(context)
        limits = state.get("budget_limits", policy.limits())
        usage = state.get("budget_usage", initial_budget_usage())
        remaining_calls = max(
            0,
            int(limits.get("max_model_calls", policy.max_model_calls))
            - int(usage.get("model_calls", 0)),
        )
        node_limits = limits.get("node_model_calls", {})
        calls_by_node = usage.get("model_calls_by_node", {})
        if isinstance(node_limits, Mapping) and isinstance(calls_by_node, Mapping):
            node_limit = node_limits.get(node)
            if isinstance(node_limit, int) and not isinstance(node_limit, bool):
                consumed = calls_by_node.get(node, 0)
                consumed = (
                    consumed if isinstance(consumed, int) and not isinstance(consumed, bool) else 0
                )
                remaining_calls = min(remaining_calls, max(0, node_limit - consumed))
        updated["model_allowance"] = {
            "remaining_calls": remaining_calls,
            "remaining_prompt_tokens": max(
                0,
                int(limits.get("max_prompt_tokens", policy.max_prompt_tokens))
                - int(usage.get("prompt_tokens", 0)),
            ),
            "remaining_completion_tokens": max(
                0,
                int(limits.get("max_completion_tokens", policy.max_completion_tokens))
                - int(usage.get("completion_tokens", 0)),
            ),
            "remaining_cost_micros": max(
                0,
                int(limits.get("max_cost_micros", policy.max_cost_micros))
                - int(usage.get("cost_micros", 0)),
            ),
        }
        return updated

    def model_observability_update(
        state: AgentState,
        *,
        node: str,
        role: str,
        started_ns: int,
    ) -> dict[str, Any]:
        consumer = getattr(decisions, "consume_observability", None)
        raw_events = consumer() if callable(consumer) else []
        events = [dict(item) for item in raw_events if isinstance(item, dict)]
        if not events:
            events = [
                {
                    "kind": "model_call",
                    "role": role,
                    "status": "succeeded",
                    "provider": manifest.get("provider", "unknown"),
                    "requested_model": "",
                    "returned_model": "",
                    "upstream_provider": "",
                    "generation_id": "",
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
                _trace_event(
                    node,
                    "succeeded",
                    started_ns=started_ns,
                    details={
                        "model_calls": len(events),
                        "roles": sorted(
                            {str(item.get("role")) for item in events if item.get("role")}
                        ),
                    },
                ),
            ],
        }

    def model_failure_update(
        state: AgentState,
        *,
        node: str,
        role: str,
        started_ns: int,
        error: Exception,
    ) -> dict[str, Any]:
        consumer = getattr(decisions, "consume_observability", None)
        raw_events = consumer() if callable(consumer) else []
        events = [dict(item) for item in raw_events if isinstance(item, dict)]
        # Only provider calls with an auditable event become a normal graph
        # terminal. Programming and contract errors must still fail loudly.
        if not events:
            raise error
        for event in events:
            event["graph_node"] = node
        usage = apply_model_events(
            state.get("budget_usage", initial_budget_usage()),
            node=node,
            events=events,
        )
        error_statuses = sorted(
            {
                int(item.get("error_status", 0))
                for item in events
                if isinstance(item.get("error_status"), int)
                and not isinstance(item.get("error_status"), bool)
                and int(item.get("error_status", 0)) > 0
            }
        )
        authentication_error = any(status in (401, 403) for status in error_statuses)
        provider_error = bool(error_statuses) or any(
            item.get("status") == "error" for item in events
        )
        if authentication_error:
            outcome = "model_authentication_error"
            response = "模型服务鉴权失败，任务已安全停止；请修复 OpenRouter 凭据后发起新的运行。"
        elif provider_error:
            outcome = "model_unavailable"
            response = "模型服务当前不可用，任务已安全停止；已有工具结果和预算账本均已保留。"
        else:
            outcome = "model_invalid_response"
            response = (
                "模型返回内容未通过节点契约校验，任务已安全停止；调用成本和已有结果均已保留。"
            )
        return {
            "budget_usage": usage,
            "model_events": [*state.get("model_events", []), *events],
            "outcome": outcome,
            "response": response,
            "needs_response": False,
            "node_trace": [
                *state.get("node_trace", []),
                _trace_event(
                    node,
                    "failed",
                    started_ns=started_ns,
                    details={
                        "error_type": type(error).__name__,
                        "error_statuses": error_statuses,
                        "model_calls": len(events),
                        "role": role,
                    },
                ),
            ],
            "steps": state.get("steps", 0) + 1,
        }

    def contracted(
        name: str,
        function: Callable[[AgentState], dict[str, Any] | Command[Any]],
    ) -> Callable[[AgentState], dict[str, Any] | Command[Any]]:
        allowed = set(NODE_CONTRACTS[name].allowed_writes)

        def invoke(state: AgentState) -> dict[str, Any] | Command[Any]:
            result = function(state)
            update = result.update if isinstance(result, Command) else result
            if isinstance(update, Mapping):
                unexpected = set(update) - allowed
                if unexpected:
                    raise ValueError(
                        f"node {name} wrote fields outside its contract: "
                        + ",".join(sorted(str(item) for item in unexpected))
                    )
            return result

        return invoke

    def create_plan(state: AgentState) -> dict[str, Any]:
        started_ns = time.perf_counter_ns()
        planner = getattr(decisions, "plan", None)
        if callable(planner):
            reason = model_access_reason(state, "plan")
            if reason:
                return model_terminal_update(state, node="plan", reason=reason)
            try:
                plan = planner(
                    module=state["module"],
                    message=state["user_message"],
                    context=with_model_allowance(
                        state,
                        state.get("context", {}),
                        node="plan",
                    ),
                )
                if not plan.objective.strip() or not plan.success_criteria.strip():
                    raise ValueError("agent plan objective and success criteria are required")
                steps = tuple(step.strip() for step in plan.steps if step.strip())
                if not steps or len(steps) > 8:
                    raise ValueError("agent plan must contain between 1 and 8 steps")
            except ModelBudgetExceeded as exc:
                return model_terminal_update(state, node="plan", reason=str(exc))
            except Exception as exc:
                return model_failure_update(
                    state,
                    node="plan",
                    role="planner",
                    started_ns=started_ns,
                    error=exc,
                )
            model_update = model_observability_update(
                state,
                node="plan",
                role="planner",
                started_ns=started_ns,
            )
        else:
            plan = AgentPlan(
                objective=state["user_message"],
                steps=(
                    "理解目标和可信输入",
                    "选择并执行最小必要工具",
                    "观察工具结果和任务状态",
                    "根据观察继续行动或交付结果",
                ),
                success_criteria="以真实工具结果完成请求，不臆造数据。",
            )
            steps = tuple(step.strip() for step in plan.steps if step.strip())
            model_update = {
                "node_trace": [
                    *state.get("node_trace", []),
                    _trace_event("plan", "succeeded", started_ns=started_ns),
                ]
            }
        return {
            **model_update,
            "plan": {
                "objective": plan.objective.strip(),
                "steps": list(steps),
                "success_criteria": plan.success_criteria.strip(),
            },
            "action_budget": policy.max_actions,
            "steps": state.get("steps", 0) + 1,
        }

    def decide(state: AgentState) -> dict[str, Any]:
        if state.get("action_index", 0) >= state.get("action_budget", 6):
            return {
                "outcome": "action_limit",
                "response": "任务已用完受信策略分配的行动预算，已安全停止。请查看已有结果后重试未完成部分。",
                "node_trace": [
                    *state.get("node_trace", []),
                    _trace_event(
                        state["module"],
                        "blocked",
                        details={"reason": "action_limit"},
                    ),
                ],
                "steps": state.get("steps", 0) + 1,
            }
        node = state["module"]
        reason = model_access_reason(state, node)
        if reason:
            return model_terminal_update(state, node=node, reason=reason)
        started_ns = time.perf_counter_ns()
        decision_context = with_model_allowance(
            state,
            state.get("context", {}),
            node=node,
        )
        decision_context["agent_plan"] = dict(state.get("plan", {}))
        decision_context["observations"] = list(state.get("observations", []))
        decision_context["action_index"] = state.get("action_index", 0)
        decision_context["task_contract"] = dict(state.get("task_contract", {}))
        decision_context["artifact_validation"] = dict(state.get("artifact_validation", {}))
        decision_context["source_coverage"] = _document_source_coverage(state)
        try:
            decision = decisions.decide(
                module=state["module"],
                message=state["user_message"],
                context=decision_context,
            )
            intent = decision.intent.strip()
            if not intent:
                raise ValueError("model decision intent is required")
            tool_name = decision.tool_name.strip()
            tool_arguments = dict(decision.tool_arguments) if tool_name else {}
            requires_composition = False
            if tool_name:
                definition = _trusted_tool_definition(state, tool_name)
                requires_composition = bool(
                    definition is not None and definition.get("compose_arguments") is True
                )
                if decision.requires_argument_composition and not requires_composition:
                    raise ValueError(
                        "model decision requested argument composition outside "
                        "the trusted tool catalog"
                    )
                if not requires_composition and definition is not None:
                    _validate_tool_arguments(tool_arguments, definition)
            elif decision.requires_argument_composition:
                raise ValueError("argument composition requires a selected tool")
            response = decision.response.strip()
            if not tool_name and not response and not decision.needs_response:
                raise ValueError("model decision must contain a response or one tool call")
        except ModelBudgetExceeded as exc:
            return model_terminal_update(state, node=node, reason=str(exc))
        except Exception as exc:
            return model_failure_update(
                state,
                node=node,
                role="router",
                started_ns=started_ns,
                error=exc,
            )
        model_update = model_observability_update(
            state,
            node=node,
            role="router",
            started_ns=started_ns,
        )
        execution_mode, execution_mode_reason = _classify_execution_mode(
            state,
            tool_name=tool_name,
            definition=(_trusted_tool_definition(state, tool_name) if tool_name else None),
        )
        update: dict[str, Any] = {
            **model_update,
            "intent": intent,
            "execution_mode": execution_mode,
            "execution_mode_reason": execution_mode_reason,
            "proposed_tool": {},
            "preparation": {},
            "tool_result": {},
            "needs_response": False,
            "steps": state.get("steps", 0) + 1,
        }
        if tool_name:
            if not tool_allowed(state["module"], tool_name):
                update.update(
                    {
                        "outcome": "tool_denied",
                        "response": "当前角色无权调用该工具，请切换到对应模块后重试。",
                    }
                )
                return update
            update["proposed_tool"] = {
                "name": tool_name,
                "arguments": tool_arguments,
                "compose_arguments": requires_composition,
            }
            return update
        if response:
            update.update({"outcome": "completed", "response": response})
            return update
        if decision.needs_response:
            update.update({"outcome": "", "response": "", "needs_response": True})
            return update
        return update

    def after_decision(state: AgentState) -> str:
        proposed = state.get("proposed_tool")
        if proposed and state.get("execution_mode") == "agentic" and not state.get("plan"):
            return "plan"
        if proposed and proposed.get("compose_arguments") is True:
            return "compose_arguments"
        if proposed:
            return "preflight_normalize"
        if state.get("needs_response"):
            return "generate_response"
        if state.get("outcome") == "completed" and state.get("response"):
            return "response_quality_gate"
        return "finalize"

    def after_plan(state: AgentState) -> str:
        if state.get("outcome") in (
            "model_budget_exhausted",
            "model_version_mismatch",
            "model_authentication_error",
            "model_invalid_response",
            "model_unavailable",
        ):
            return "finalize"
        return after_decision(state)

    def compose_arguments(state: AgentState) -> dict[str, Any]:
        node = "compose_arguments"
        reason = model_access_reason(state, node)
        if reason:
            return model_terminal_update(state, node=node, reason=reason)
        proposed = state.get("proposed_tool", {})
        tool_name = proposed.get("name")
        if (
            not isinstance(tool_name, str)
            or not tool_name.strip()
            or proposed.get("compose_arguments") is not True
        ):
            raise ValueError("argument composer requires one explicitly marked proposed tool")
        composer = getattr(decisions, "compose_arguments", None)
        if not callable(composer):
            raise ValueError("decision port must implement compose_arguments for marked tools")
        normalized_tool_name = tool_name.strip()
        presentation_tool = normalized_tool_name in (
            "work_create_pptx_outline",
            "work_generate_pptx",
        )
        task_contract = state.get("task_contract", {})
        all_document_batches = (
            _presentation_document_batches(state)
            if presentation_tool
            and isinstance(task_contract, Mapping)
            and task_contract.get("exhaustive") is True
            and bool(_presentation_requested_columns(task_contract))
            else []
        )
        round_processing = len(all_document_batches) > 1
        rewrite_attempt = int(state.get("presentation_rewrite_attempts", 0))
        raw_processing = state.get("document_processing", {})
        processing = dict(raw_processing) if isinstance(raw_processing, Mapping) else {}
        raw_base_arguments = proposed.get("arguments")
        base_arguments = dict(raw_base_arguments) if isinstance(raw_base_arguments, Mapping) else {}
        selected_batches = all_document_batches
        missing_keys: list[str] = []
        if round_processing and rewrite_attempt > 0:
            base_arguments = _normalize_presentation_arguments(
                _presentation_repair_base(
                    base_arguments,
                    state.get("artifact_validation", {}),
                ),
                state,
            )
            selected_batches, missing_keys = _repair_document_batches(
                all_document_batches,
                base_arguments,
                state,
            )
        processing_key = (
            canonical_arguments_hash(
                {
                    "version": "document-processing-v1",
                    "tool_name": normalized_tool_name,
                    "rewrite_attempt": rewrite_attempt,
                    "batches": [batch.get("batch_id") for batch in selected_batches],
                }
            )
            if round_processing
            else ""
        )
        if round_processing and processing.get("processing_key") != processing_key:
            processing = {
                "version": "document-processing-v1",
                "processing_key": processing_key,
                "tool_name": normalized_tool_name,
                "rewrite_attempt": rewrite_attempt,
                "batch_count": len(selected_batches),
                "next_batch_index": 0,
                "processed_batches": [],
                "missing_record_keys": missing_keys,
                "complete": False,
            }
        batch: dict[str, Any] | None = None
        if round_processing:
            batch_index = int(processing.get("next_batch_index") or 0)
            if not 0 <= batch_index < len(selected_batches):
                raise ValueError("document processing batch checkpoint is out of range")
            batch = selected_batches[batch_index]
        started_ns = time.perf_counter_ns()
        composition_context = with_model_allowance(
            state,
            state.get("context", {}),
            node=node,
        )
        composition_context["agent_plan"] = dict(state.get("plan", {}))
        composition_context["observations"] = (
            [_document_batch_observation(batch)]
            if batch is not None
            else list(state.get("observations", []))
        )
        composition_context["action_index"] = state.get("action_index", 0)
        composition_context["task_contract"] = dict(task_contract)
        composition_context["email_validation"] = dict(state.get("email_validation", {}))
        composition_context["artifact_validation"] = dict(state.get("artifact_validation", {}))
        composition_context["source_coverage"] = _document_source_coverage(state)
        composition_context["previous_arguments"] = base_arguments
        if batch is not None:
            composition_context["document_processing_round"] = {
                "version": "document-processing-v1",
                "batch_id": batch.get("batch_id"),
                "round_number": int(processing.get("next_batch_index") or 0) + 1,
                "round_count": len(selected_batches),
                "source_round": int(batch.get("round_no") or 1),
                "attachment_index": int(batch.get("attachment_index") or 1),
                "rewrite_attempt": rewrite_attempt,
                "missing_record_keys": missing_keys,
            }
        try:
            arguments = composer(
                module=state["module"],
                message=state["user_message"],
                tool_name=normalized_tool_name,
                context=composition_context,
            )
            if not isinstance(arguments, Mapping):
                raise ValueError("composed tool arguments must be an object")
            normalized_arguments = dict(arguments)
            definition = _trusted_tool_definition(state, normalized_tool_name)
            if definition is None:
                raise ValueError("argument composition requires a trusted tool definition")
            if presentation_tool:
                normalized_arguments = _normalize_presentation_arguments(
                    normalized_arguments,
                    state,
                )
                if batch is not None:
                    normalized_arguments = _merge_presentation_arguments(
                        base_arguments,
                        normalized_arguments,
                        state,
                    )
                parameters = definition.get("parameters")
                properties = (
                    parameters.get("properties") if isinstance(parameters, Mapping) else None
                )
                if isinstance(properties, Mapping) and "task_contract" in properties:
                    normalized_arguments["task_contract"] = dict(task_contract)
                if isinstance(properties, Mapping) and "source_coverage" in properties:
                    normalized_arguments["source_coverage"] = _document_source_coverage(state)
        except ModelBudgetExceeded as exc:
            return model_terminal_update(state, node=node, reason=str(exc))
        except Exception as exc:
            return model_failure_update(
                state,
                node=node,
                role="composer",
                started_ns=started_ns,
                error=exc,
            )
        model_update = model_observability_update(
            state,
            node=node,
            role="composer",
            started_ns=started_ns,
        )
        processing_update = processing
        compose_more = False
        if batch is not None:
            raw_processed = processing.get("processed_batches")
            processed = list(raw_processed) if isinstance(raw_processed, list) else []
            processed.append(
                {
                    "batch_id": batch.get("batch_id"),
                    "attachment_index": batch.get("attachment_index"),
                    "source_round": batch.get("round_no"),
                    "input_characters": len(str(batch.get("processing_text") or "")),
                    "record_count": len(
                        normalized_arguments.get("table", {}).get("rows", [])
                        if isinstance(normalized_arguments.get("table"), Mapping)
                        else []
                    ),
                }
            )
            next_batch_index = int(processing.get("next_batch_index") or 0) + 1
            compose_more = next_batch_index < len(selected_batches)
            processing_update = {
                **processing,
                "next_batch_index": next_batch_index,
                "processed_batches": processed,
                "complete": not compose_more,
                "merged_record_count": len(
                    normalized_arguments.get("table", {}).get("rows", [])
                    if isinstance(normalized_arguments.get("table"), Mapping)
                    else []
                ),
            }
            trace = model_update.get("node_trace")
            if isinstance(trace, list) and trace and isinstance(trace[-1], dict):
                details = trace[-1].get("details")
                if isinstance(details, dict):
                    details.update(
                        {
                            "document_processing": True,
                            "batch_id": batch.get("batch_id"),
                            "batch_number": next_batch_index,
                            "batch_count": len(selected_batches),
                            "merged_record_count": processing_update["merged_record_count"],
                        }
                    )
        return {
            **model_update,
            "proposed_tool": {
                "name": normalized_tool_name,
                "arguments": normalized_arguments,
                "compose_arguments": compose_more,
            },
            "document_processing": processing_update,
            "steps": state.get("steps", 0) + 1,
        }

    def after_composition(state: AgentState) -> str:
        if state.get("outcome") in (
            "model_budget_exhausted",
            "model_version_mismatch",
            "model_authentication_error",
            "model_invalid_response",
            "model_unavailable",
        ):
            return "finalize"
        if state.get("proposed_tool", {}).get("compose_arguments") is True:
            return "compose_arguments"
        return "email_quality_gate"

    def email_quality_gate(state: AgentState) -> dict[str, Any]:
        started_ns = time.perf_counter_ns()
        proposed = state.get("proposed_tool", {})
        tool_name = str(proposed.get("name") or "")
        arguments = proposed.get("arguments")
        if tool_name in ("work_create_pptx_outline", "work_generate_pptx"):
            if not isinstance(arguments, Mapping):
                raise ValueError("presentation quality gate requires composed arguments")
            definition = _trusted_tool_definition(state, tool_name)
            schema_violations: list[dict[str, Any]] = []
            if definition is not None:
                try:
                    _validate_tool_arguments(arguments, definition)
                except Exception as exc:
                    schema_violations.append(
                        {
                            "code": "trusted_schema_invalid",
                            "message": "演示文稿参数未通过可信 Schema 校验",
                            "validator": type(exc).__name__,
                            "reason": str(exc).strip()[:240],
                        }
                    )
            violations = [
                *schema_violations,
                *validate_presentation_arguments(
                    arguments,
                    state.get("task_contract", {}),
                    expected_record_keys=presentation_source_record_keys(
                        state.get("observations", []),
                        state.get("task_contract", {}),
                    ),
                ),
            ]
            attempts = int(state.get("presentation_rewrite_attempts", 0))
            report = {
                "policy_version": "presentation-arguments-v1",
                "applicable": True,
                "passed": not violations,
                "violations": violations,
                "rewrite_attempt": attempts,
            }
            if not violations:
                return {
                    "email_validation": {},
                    "presentation_validation": report,
                    "artifact_validation": {},
                    "node_trace": [
                        *state.get("node_trace", []),
                        _trace_event(
                            "email_quality_gate",
                            "succeeded",
                            started_ns=started_ns,
                            details={
                                "policy_version": report["policy_version"],
                                "presentation": True,
                                "rewrite_attempt": attempts,
                            },
                        ),
                    ],
                    "steps": state.get("steps", 0) + 1,
                }
            if attempts < 2:
                report["rewrite_attempt"] = attempts + 1
                return {
                    "proposed_tool": {
                        **dict(proposed),
                        "compose_arguments": True,
                    },
                    "email_validation": {},
                    "presentation_validation": report,
                    "presentation_rewrite_attempts": attempts + 1,
                    "artifact_validation": report,
                    "node_trace": [
                        *state.get("node_trace", []),
                        _trace_event(
                            "email_quality_gate",
                            "retrying",
                            started_ns=started_ns,
                            details={
                                "policy_version": report["policy_version"],
                                "presentation": True,
                                "violations": violations,
                                "rewrite_attempt": attempts + 1,
                            },
                        ),
                    ],
                    "steps": state.get("steps", 0) + 1,
                }
            return {
                "email_validation": {},
                "presentation_validation": report,
                "artifact_validation": report,
                "outcome": "artifact_quality_failed",
                "response": (
                    "演示文稿参数连续两次未通过结构化内容检查，已停止生成和交付不完整文件。"
                ),
                "node_trace": [
                    *state.get("node_trace", []),
                    _trace_event(
                        "email_quality_gate",
                        "blocked",
                        started_ns=started_ns,
                        details={
                            "policy_version": report["policy_version"],
                            "presentation": True,
                            "violations": violations,
                            "rewrite_attempt": attempts,
                        },
                    ),
                ],
                "steps": state.get("steps", 0) + 1,
            }
        if tool_name != "work_draft_email":
            return {
                "email_validation": {},
                "presentation_validation": {},
                "node_trace": [
                    *state.get("node_trace", []),
                    _trace_event(
                        "email_quality_gate",
                        "succeeded",
                        started_ns=started_ns,
                        details={"skipped": True},
                    ),
                ],
                "steps": state.get("steps", 0) + 1,
            }
        if not isinstance(arguments, Mapping):
            raise ValueError("email quality gate requires composed arguments")
        context = state.get("context", {})
        profile = context.get("email_profile", {}) if isinstance(context, dict) else {}
        if not isinstance(profile, Mapping):
            profile = {}
        email_violations = validate_email_arguments(
            arguments,
            message=state["user_message"],
            email_profile=profile,
        )
        report = email_quality_report(email_violations)
        attempts = int(state.get("email_rewrite_attempts", 0))
        report["rewrite_attempt"] = attempts
        if not email_violations:
            return {
                "email_validation": report,
                "node_trace": [
                    *state.get("node_trace", []),
                    _trace_event(
                        "email_quality_gate",
                        "succeeded",
                        started_ns=started_ns,
                        details={
                            "policy_version": EMAIL_DRAFT_POLICY_VERSION,
                            "rewrite_attempt": attempts,
                        },
                    ),
                ],
                "steps": state.get("steps", 0) + 1,
            }
        if attempts < 1:
            report["rewrite_attempt"] = attempts + 1
            return {
                "proposed_tool": {
                    **dict(proposed),
                    "compose_arguments": True,
                },
                "email_validation": report,
                "email_rewrite_attempts": attempts + 1,
                "node_trace": [
                    *state.get("node_trace", []),
                    _trace_event(
                        "email_quality_gate",
                        "retrying",
                        started_ns=started_ns,
                        details={
                            "policy_version": EMAIL_DRAFT_POLICY_VERSION,
                            "violations": email_violations,
                            "rewrite_attempt": attempts + 1,
                        },
                    ),
                ],
                "steps": state.get("steps", 0) + 1,
            }
        return {
            "email_validation": report,
            "outcome": "email_quality_failed",
            "response": "邮件草稿连续两次未通过语言与礼貌结构检查，已停止交付不完整结果。请补充收件人称呼或发件人身份后重试。",
            "node_trace": [
                *state.get("node_trace", []),
                _trace_event(
                    "email_quality_gate",
                    "blocked",
                    started_ns=started_ns,
                    details={
                        "policy_version": EMAIL_DRAFT_POLICY_VERSION,
                        "violations": email_violations,
                        "rewrite_attempt": attempts,
                    },
                ),
            ],
            "steps": state.get("steps", 0) + 1,
        }

    def after_email_quality(state: AgentState) -> str:
        if state.get("outcome") in ("email_quality_failed", "artifact_quality_failed"):
            return "finalize"
        proposed = state.get("proposed_tool", {})
        if proposed.get("compose_arguments") is True:
            return "compose_arguments"
        return "preflight_normalize"

    def preflight_normalize(state: AgentState) -> dict[str, Any]:
        started_ns = time.perf_counter_ns()
        proposed = state.get("proposed_tool", {})
        tool_name = proposed.get("name")
        arguments = proposed.get("arguments")
        if not isinstance(tool_name, str) or not isinstance(arguments, Mapping):
            raise ValueError("preflight requires one proposed tool and arguments")
        definition = _trusted_tool_definition(state, tool_name)
        if definition is None:
            return {
                "proposed_tool": dict(proposed),
                "repair_history": list(state.get("repair_history", [])),
                "failure": {},
                "outcome": "",
                "node_trace": [
                    *state.get("node_trace", []),
                    _trace_event(
                        "preflight_normalize",
                        "succeeded",
                        started_ns=started_ns,
                        details={"tool_name": tool_name, "catalog": "development"},
                    ),
                ],
                "steps": state.get("steps", 0) + 1,
            }
        try:
            normalized, records = preflight_repair(arguments, definition)
            _validate_tool_arguments(normalized, definition)
        except Exception as exc:
            return {
                "failure": {
                    "contract_version": TOOL_FAILURE_CONTRACT_VERSION,
                    "code": "invalid_tool_arguments",
                    "category": "argument_validation",
                    "phase": "pre_execution",
                    "message": "工具参数未通过可信 Schema 校验。",
                    "retry_same_input": False,
                    "repairable": False,
                    "side_effect_state": "none",
                    "safe_details": {"validator": type(exc).__name__},
                },
                "outcome": "model_invalid_response",
                "response": "模型生成的工具参数未通过可信 Schema 校验，任务已安全停止。",
                "node_trace": [
                    *state.get("node_trace", []),
                    _trace_event(
                        "preflight_normalize",
                        "blocked",
                        started_ns=started_ns,
                        details={"reason": "invalid_tool_arguments"},
                    ),
                ],
                "steps": state.get("steps", 0) + 1,
            }
        history = [*state.get("repair_history", []), *records]
        return {
            "proposed_tool": {
                **dict(proposed),
                "arguments": normalized,
                "argument_provenance": {str(key): "model_composed" for key in normalized},
            },
            "repair_history": history,
            "failure": {},
            "outcome": "",
            "node_trace": [
                *state.get("node_trace", []),
                _trace_event(
                    "preflight_normalize",
                    "repaired" if records else "succeeded",
                    started_ns=started_ns,
                    details={
                        "tool_name": tool_name,
                        "repair_count": len(records),
                        "operators": [str(item.get("operator_id")) for item in records],
                    },
                ),
            ],
            "steps": state.get("steps", 0) + 1,
        }

    def after_preflight(state: AgentState) -> str:
        return "finalize" if state.get("outcome") else "prepare_tool"

    def generate_response(state: AgentState) -> dict[str, Any]:
        node = "generate_response"
        reason = model_access_reason(state, node)
        if reason:
            return model_terminal_update(state, node=node, reason=reason)
        responder = getattr(decisions, "respond", None)
        if not callable(responder):
            raise ValueError("decision port must implement respond for conversational output")
        started_ns = time.perf_counter_ns()
        response_context = with_model_allowance(
            state,
            state.get("context", {}),
            node=node,
        )
        response_context["agent_plan"] = dict(state.get("plan", {}))
        response_context["observations"] = list(state.get("observations", []))
        response_context["task_contract"] = dict(state.get("task_contract", {}))
        try:
            response = responder(
                module=state["module"],
                message=state["user_message"],
                context=response_context,
            )
            if not isinstance(response, str) or not response.strip():
                raise ValueError("model response must be non-empty")
        except ModelBudgetExceeded as exc:
            return model_terminal_update(state, node=node, reason=str(exc))
        except Exception as exc:
            return model_failure_update(
                state,
                node=node,
                role="responder",
                started_ns=started_ns,
                error=exc,
            )
        return {
            **model_observability_update(
                state,
                node=node,
                role="responder",
                started_ns=started_ns,
            ),
            "response": response.strip(),
            "needs_response": False,
            "outcome": "completed",
            "steps": state.get("steps", 0) + 1,
        }

    def response_quality_gate(state: AgentState) -> dict[str, Any]:
        started_ns = time.perf_counter_ns()
        inspected = inspect_and_repair_response(state.get("response", ""))
        report = dict(inspected.report)
        attempts = int(state.get("response_rewrite_attempts", 0))
        report["rewrite_attempt"] = attempts
        prior_outcome = str(state.get("outcome") or "")
        if prior_outcome and prior_outcome != "completed":
            return {
                "response": inspected.response,
                "response_validation": report,
                "outcome": prior_outcome,
                "node_trace": [
                    *state.get("node_trace", []),
                    _trace_event(
                        "response_quality_gate",
                        "skipped",
                        started_ns=started_ns,
                        details={
                            "reason": "terminal_outcome_preserved",
                            "outcome": prior_outcome,
                        },
                    ),
                ],
                "steps": state.get("steps", 0) + 1,
            }
        if report.get("passed") is True:
            return {
                "response": inspected.response,
                "response_validation": report,
                "outcome": "completed",
                "node_trace": [
                    *state.get("node_trace", []),
                    _trace_event(
                        "response_quality_gate",
                        "succeeded",
                        started_ns=started_ns,
                        details={
                            "policy_version": report.get("policy_version", ""),
                            "deterministic_repairs": len(report.get("repairs", [])),
                            "rewrite_attempt": attempts,
                        },
                    ),
                ],
                "steps": state.get("steps", 0) + 1,
            }
        reviser = getattr(decisions, "revise_response", None)
        if attempts < 1 and callable(reviser):
            report["rewrite_attempt"] = attempts + 1
            return {
                "response": inspected.response,
                "response_validation": report,
                "response_rewrite_attempts": attempts + 1,
                "outcome": "",
                "node_trace": [
                    *state.get("node_trace", []),
                    _trace_event(
                        "response_quality_gate",
                        "retrying",
                        started_ns=started_ns,
                        details={
                            "policy_version": report.get("policy_version", ""),
                            "violation_codes": [
                                str(item.get("code") or "")
                                for item in report.get("violations", [])
                                if isinstance(item, Mapping)
                            ],
                            "rewrite_attempt": attempts + 1,
                        },
                    ),
                ],
                "steps": state.get("steps", 0) + 1,
            }
        return {
            "response_validation": report,
            "outcome": "response_quality_failed",
            "response": (
                "生成内容在一次受限修复后仍未通过重复性质量检查，"
                "已停止交付可能重复或不完整的结果。请缩小生成范围后重试。"
            ),
            "node_trace": [
                *state.get("node_trace", []),
                _trace_event(
                    "response_quality_gate",
                    "blocked",
                    started_ns=started_ns,
                    details={
                        "policy_version": report.get("policy_version", ""),
                        "rewrite_attempt": attempts,
                    },
                ),
            ],
            "steps": state.get("steps", 0) + 1,
        }

    def after_response_quality(state: AgentState) -> str:
        if (
            state.get("response_validation", {}).get("passed") is False
            and state.get("outcome") != "response_quality_failed"
        ):
            return "revise_response"
        return "finalize"

    def after_response_revision(state: AgentState) -> str:
        outcome = str(state.get("outcome") or "")
        if outcome and outcome != "completed":
            return "finalize"
        return "response_quality_gate"

    def revise_response(state: AgentState) -> dict[str, Any]:
        node = "revise_response"
        reason = model_access_reason(state, node)
        if reason:
            return model_terminal_update(state, node=node, reason=reason)
        reviser = getattr(decisions, "revise_response", None)
        if not callable(reviser):
            raise ValueError("decision port must implement bounded response revision")
        violations = state.get("response_validation", {}).get("violations", [])
        safe_violations = [dict(item) for item in violations if isinstance(item, Mapping)]
        started_ns = time.perf_counter_ns()
        revision_context = with_model_allowance(
            state,
            state.get("context", {}),
            node=node,
        )
        revision_context["agent_plan"] = dict(state.get("plan", {}))
        revision_context["observations"] = list(state.get("observations", []))
        revision_context["task_contract"] = dict(state.get("task_contract", {}))
        try:
            response = reviser(
                module=state["module"],
                message=state["user_message"],
                response=state.get("response", ""),
                violations=safe_violations,
                context=revision_context,
            )
            if not isinstance(response, str) or not response.strip():
                raise ValueError("revised response must be non-empty")
        except ModelBudgetExceeded as exc:
            return model_terminal_update(state, node=node, reason=str(exc))
        except Exception as exc:
            return model_failure_update(
                state,
                node=node,
                role="responder",
                started_ns=started_ns,
                error=exc,
            )
        return {
            **model_observability_update(
                state,
                node=node,
                role="responder",
                started_ns=started_ns,
            ),
            "response": response.strip(),
            "response_validation": {},
            "outcome": "",
            "steps": state.get("steps", 0) + 1,
        }

    def prepare_tool(state: AgentState) -> dict[str, Any]:
        started_ns = time.perf_counter_ns()
        proposed = state["proposed_tool"]
        action_index = state.get("action_index", 0) + 1
        if action_index > policy.max_actions:
            return {
                "outcome": "action_limit",
                "response": "任务已用完工具行动预算，未执行新的工具操作。",
                "node_trace": [
                    *state.get("node_trace", []),
                    _trace_event(
                        "prepare_tool",
                        "blocked",
                        details={"reason": "action_limit"},
                    ),
                ],
                "steps": state.get("steps", 0) + 1,
            }
        preparation = tools.prepare(
            run_id=state["run_id"],
            user_id=state["user_id"],
            module=state["module"],
            tool_name=str(proposed["name"]),
            arguments=cast(Mapping[str, Any], proposed.get("arguments", {})),
            idempotency_key=f"{state['run_id']}:action:{action_index}:prepare",
        )
        if preparation.tool_name != proposed["name"]:
            raise ValueError("tool gateway returned a mismatched tool")
        if preparation.status == "requires_confirmation":
            if not preparation.confirmation_token or not preparation.summary:
                raise ValueError("confirmation token and summary are required")
        elif not preparation.response.strip():
            raise ValueError("completed tool preparation must contain a response")
        usage = dict(state.get("budget_usage", initial_budget_usage()))
        usage["actions"] = action_index
        return {
            "preparation": asdict(preparation),
            "response": preparation.response,
            "tool_result": {},
            "pending_task_id": "",
            "task_polls": 0,
            "action_index": action_index,
            "budget_usage": usage,
            "node_trace": [
                *state.get("node_trace", []),
                _trace_event(
                    "prepare_tool",
                    "succeeded",
                    started_ns=started_ns,
                    details={
                        "action": action_index,
                        "tool_name": preparation.tool_name,
                        "status": preparation.status,
                    },
                ),
            ],
            "steps": state.get("steps", 0) + 1,
        }

    def after_prepare(state: AgentState) -> str:
        if state.get("outcome") == "action_limit":
            return "finalize"
        preparation = state["preparation"]
        if preparation["status"] == "requires_confirmation":
            return "approval"
        return "observe_result"

    def approval(
        state: AgentState,
    ) -> Command[Literal["commit_tool", "reject_tool"]]:
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
        update = {
            "approval": dict(resolution),
            "recovery": _updated_recovery(
                state,
                counter="approval_resumes",
                interrupt_type="tool_approval",
            ),
            "node_trace": [
                *state.get("node_trace", []),
                _trace_event(
                    "approval",
                    "resumed",
                    details={"approved": resolution["approved"]},
                ),
            ],
            "steps": state.get("steps", 0) + 1,
        }
        if resolution["approved"]:
            return Command(update=update, goto="commit_tool")
        return Command(update=update, goto="reject_tool")

    def commit_tool(state: AgentState) -> dict[str, Any]:
        started_ns = time.perf_counter_ns()
        outcome = tools.commit(
            run_id=state["run_id"],
            user_id=state["user_id"],
            module=state["module"],
            preparation=state["preparation"],
            resolution=state["approval"],
            idempotency_key=(f"{state['run_id']}:action:{state.get('action_index', 0)}:commit"),
        )
        if not outcome.response.strip():
            raise ValueError("tool gateway commit must contain a response")
        return {
            "tool_result": asdict(outcome),
            "response": outcome.response,
            "node_trace": [
                *state.get("node_trace", []),
                _trace_event(
                    "commit_tool",
                    "succeeded",
                    started_ns=started_ns,
                    details={
                        "action": state.get("action_index", 0),
                        "tool_name": state.get("preparation", {}).get("tool_name", ""),
                    },
                ),
            ],
            "steps": state.get("steps", 0) + 1,
        }

    def reject_tool(state: AgentState) -> dict[str, Any]:
        return {
            "response": "已取消，本次操作没有写入业务数据。",
            "outcome": "cancelled",
            "node_trace": [
                *state.get("node_trace", []),
                _trace_event("reject_tool", "succeeded"),
            ],
        }

    def observe_result(state: AgentState) -> dict[str, Any]:
        started_ns = time.perf_counter_ns()
        source = state.get("tool_result") or state.get("preparation") or {}
        data = source.get("data") if isinstance(source, dict) else None
        status = _skill_run_status(data)
        task_id = _skill_run_id(data)
        if status in ("queued", "running") and task_id:
            return {
                "pending_task_id": task_id,
                "response": str(source.get("response") or "工作任务正在执行。"),
                "node_trace": [
                    *state.get("node_trace", []),
                    _trace_event(
                        "observe_result",
                        "waiting",
                        started_ns=started_ns,
                        details={"task_id": task_id, "status": status},
                    ),
                ],
                "steps": state.get("steps", 0) + 1,
            }
        observation = _observation(state, source)
        observations = [*state.get("observations", []), observation]
        repair_history = list(state.get("repair_history", []))
        if (
            status in ("succeeded", "failed", "cancelled")
            and state.get("repair", {}).get("validation") == "succeeded"
            and repair_history
        ):
            latest_repair = dict(repair_history[-1])
            latest_repair["result"] = "succeeded" if status == "succeeded" else "failed"
            latest_repair["task_status"] = status
            if isinstance(data, dict):
                task_attempt = data.get("attempt")
                if isinstance(task_attempt, int) and not isinstance(task_attempt, bool):
                    latest_repair["task_attempt"] = task_attempt
            repair_history[-1] = latest_repair
        outcome = "completed"
        response = str(source.get("response") or state.get("response") or "").strip()
        failure: dict[str, Any] = {}
        failed_task_id = ""
        if status in ("failed", "cancelled"):
            outcome = "tool_failed"
            failed_task_id = task_id
            failure = _skill_run_failure(data)
            failure["task_id"] = task_id
            failure["tool_name"] = str(state.get("proposed_tool", {}).get("name") or "")
            failure["arguments_hash"] = canonical_arguments_hash(
                cast(
                    Mapping[str, Any],
                    state.get("proposed_tool", {}).get("arguments") or {},
                )
            )
        return {
            "observations": observations,
            "pending_task_id": "",
            "response": response,
            "outcome": outcome,
            "failure": failure,
            "failed_task_id": failed_task_id,
            "repair_history": repair_history,
            "node_trace": [
                *state.get("node_trace", []),
                _trace_event(
                    "observe_result",
                    "succeeded" if outcome == "completed" else "failed",
                    started_ns=started_ns,
                    details={"status": status or "completed"},
                ),
            ],
            "steps": state.get("steps", 0) + 1,
        }

    def after_observation(state: AgentState) -> str:
        if state.get("pending_task_id"):
            return "wait_task"
        if state.get("outcome") == "tool_failed":
            return "classify_tool_failure"
        if _latest_document_continuation(state):
            return "continue_document_extraction"
        # A generated file is evidence, not proof that the user objective was
        # satisfied. Artifact-producing skills pass through a deterministic
        # contract gate before the Harness may finalize the run.
        if _latest_observation_requires_artifact_gate(state):
            return "artifact_quality_gate"
        preparation = state.get("preparation", {})
        if preparation.get("status") == "requires_confirmation":
            return "finalize"
        if state.get("execution_mode") != "agentic":
            return "finalize"
        return "assess_progress"

    def continue_document_extraction(state: AgentState) -> dict[str, Any]:
        arguments = _latest_document_continuation(state)
        if not arguments:
            raise ValueError("document continuation requires a pending extraction round")
        if state.get("action_index", 0) >= state.get("action_budget", policy.max_actions):
            return {
                "outcome": "action_limit",
                "response": "大文件仍有未读取轮次，但本次运行的文档行动预算已用完。",
                "node_trace": [
                    *state.get("node_trace", []),
                    _trace_event(
                        "continue_document_extraction",
                        "blocked",
                        details={"reason": "action_limit", **arguments},
                    ),
                ],
                "steps": state.get("steps", 0) + 1,
            }
        return {
            "proposed_tool": {
                "name": "work_extract_attached_document",
                "arguments": arguments,
                "compose_arguments": False,
            },
            "preparation": {},
            "tool_result": {},
            "outcome": "",
            "response": "正在继续读取并处理大文件的下一轮内容。",
            "node_trace": [
                *state.get("node_trace", []),
                _trace_event(
                    "continue_document_extraction",
                    "succeeded",
                    details=arguments,
                ),
            ],
            "steps": state.get("steps", 0) + 1,
        }

    def artifact_quality_gate(state: AgentState) -> dict[str, Any]:
        started_ns = time.perf_counter_ns()
        observations = state.get("observations", [])
        latest = observations[-1] if observations else {}
        data = latest.get("data") if isinstance(latest, dict) else None
        report = validate_artifact_observation(
            data,
            state.get("task_contract", {}),
        )
        applicable = report.get("applicable") is True
        passed = report.get("passed") is True
        if not applicable or passed:
            outcome = "completed"
            response = state.get("response", "")
            status = "succeeded"
        elif state.get("action_index", 0) < state.get("action_budget", policy.max_actions):
            outcome = ""
            response = (
                "生成文件未通过任务完整性或制品质量门禁，Harness 将在行动预算内重新整理来源并生成。"
            )
            status = "retrying"
        else:
            outcome = "artifact_quality_failed"
            response = "生成文件未通过质量门禁，且自动修复预算已用完。"
            status = "blocked"
        return {
            "artifact_validation": report,
            "outcome": outcome,
            "response": response,
            "node_trace": [
                *state.get("node_trace", []),
                _trace_event(
                    "artifact_quality_gate",
                    status,
                    started_ns=started_ns,
                    details={
                        "applicable": applicable,
                        "passed": passed,
                        "violation_codes": [
                            str(item.get("code") or "")
                            for item in report.get("violations", [])
                            if isinstance(item, Mapping)
                        ],
                    },
                ),
            ],
            "steps": state.get("steps", 0) + 1,
        }

    def after_artifact_quality(state: AgentState) -> str:
        if state.get("artifact_validation", {}).get("passed") is True:
            return "finalize"
        if state.get("outcome") == "artifact_quality_failed":
            return "finalize"
        return "continue_action"

    def classify_tool_failure(state: AgentState) -> dict[str, Any]:
        started_ns = time.perf_counter_ns()
        now_ms = int(time.time() * 1000)
        repair_started_at_ms = state.get("repair_started_at_ms", 0) or now_ms
        failure = dict(state.get("failure", {}))
        code = str(failure.get("code") or "tool_failed")
        side_effect_state = str(failure.get("side_effect_state") or "unknown")
        arguments = cast(
            Mapping[str, Any],
            state.get("proposed_tool", {}).get("arguments") or {},
        )
        fingerprint = _repair_failure_fingerprint(
            str(state.get("proposed_tool", {}).get("name") or ""),
            code,
            arguments,
        )
        maximum = int(
            state.get("budget_limits", {}).get("max_repair_attempts", policy.max_repair_attempts)
        )
        strategy = "terminal"
        reason = "not_repairable"
        if fingerprint in state.get("repair_fingerprints", []):
            reason = "repeat_failure_fingerprint"
        elif now_ms - repair_started_at_ms >= int(
            state.get("budget_limits", {}).get(
                "max_repair_added_latency_ms", policy.max_repair_added_latency_ms
            )
        ):
            reason = "repair_latency_budget"
        elif state.get("repair_attempts", 0) >= maximum:
            reason = "repair_attempt_budget"
        elif side_effect_state != "none":
            strategy, reason = "reconcile", "side_effect_not_clear"
        elif failure.get("retry_same_input") is True:
            strategy, reason = "transient", "same_input_retry"
        else:
            tool_name = str(state.get("proposed_tool", {}).get("name") or "")
            definition = _trusted_tool_definition(state, tool_name)
            if definition is not None and known_repair_available(failure, definition):
                strategy, reason = "deterministic", "known_repair"
            elif failure.get("repairable") is True and failure.get("field_paths"):
                usage = state.get("budget_usage", {})
                repair_calls = int(usage.get("repair_model_calls", 0))
                repair_cost = int(usage.get("repair_cost_micros", 0))
                limits = state.get("budget_limits", {})
                if repair_calls >= int(
                    limits.get("max_repair_model_calls", policy.max_repair_model_calls)
                ):
                    reason = "repair_model_call_budget"
                elif repair_cost >= int(
                    limits.get("max_repair_cost_micros", policy.max_repair_cost_micros)
                ):
                    reason = "repair_model_cost_budget"
                else:
                    strategy, reason = "llm", "unknown_repairable"
        update: dict[str, Any] = {
            "failure": {**failure, "fingerprint": fingerprint},
            "repair": {"strategy": strategy, "reason": reason},
            "repair_started_at_ms": repair_started_at_ms,
            "node_trace": [
                *state.get("node_trace", []),
                _trace_event(
                    "classify_tool_failure",
                    "succeeded" if strategy != "terminal" else "blocked",
                    started_ns=started_ns,
                    details={"code": code, "strategy": strategy, "reason": reason},
                ),
            ],
            "steps": state.get("steps", 0) + 1,
        }
        if strategy == "terminal":
            update["outcome"] = "tool_failed"
            update["response"] = _safe_failure_response(failure)
        return update

    def after_failure_classification(state: AgentState) -> str:
        strategy = state.get("repair", {}).get("strategy")
        return {
            "deterministic": "apply_known_repair",
            "transient": "validate_repair",
            "llm": "plan_repair_llm",
            "reconcile": "reconcile_side_effect",
        }.get(str(strategy), "finalize")

    def apply_known_repair_node(state: AgentState) -> dict[str, Any]:
        started_ns = time.perf_counter_ns()
        proposed = state.get("proposed_tool", {})
        tool_name = str(proposed.get("name") or "")
        arguments = cast(Mapping[str, Any], proposed.get("arguments") or {})
        definition = _trusted_tool_definition(state, tool_name)
        repair = (
            apply_known_repair(arguments, state.get("failure", {}), definition)
            if definition is not None
            else None
        )
        if repair is None:
            return {
                "repair": {"strategy": "llm", "reason": "known_repair_not_applicable"},
                "node_trace": [
                    *state.get("node_trace", []),
                    _trace_event(
                        "apply_known_repair",
                        "blocked",
                        started_ns=started_ns,
                        details={"reason": "operator_not_applicable"},
                    ),
                ],
                "steps": state.get("steps", 0) + 1,
            }
        return {
            "repair": repair,
            "node_trace": [
                *state.get("node_trace", []),
                _trace_event(
                    "apply_known_repair",
                    "succeeded",
                    started_ns=started_ns,
                    details={
                        "operator_id": repair.get("operator_id"),
                        "changed_paths": repair.get("changed_paths", []),
                    },
                ),
            ],
            "steps": state.get("steps", 0) + 1,
        }

    def after_known_repair(state: AgentState) -> str:
        if isinstance(state.get("repair", {}).get("arguments"), Mapping):
            return "validate_repair"
        return "plan_repair_llm"

    def plan_repair_llm(state: AgentState) -> dict[str, Any]:
        node = "plan_repair_llm"
        reason = model_access_reason(state, node)
        if reason:
            return model_terminal_update(state, node=node, reason=reason)
        repairer = getattr(decisions, "repair", None)
        if not callable(repairer):
            return {
                "repair": {"strategy": "abort", "reason": "repairer_unavailable"},
                "outcome": "tool_failed",
                "response": _safe_failure_response(state.get("failure", {})),
                "node_trace": [
                    *state.get("node_trace", []),
                    _trace_event(node, "blocked", details={"reason": "repairer_unavailable"}),
                ],
                "steps": state.get("steps", 0) + 1,
            }
        started_ns = time.perf_counter_ns()
        proposed = state.get("proposed_tool", {})
        tool_name = str(proposed.get("name") or "")
        arguments = cast(Mapping[str, Any], proposed.get("arguments") or {})
        definition = _trusted_tool_definition(state, tool_name)
        failure_paths = {
            str(item)
            for item in state.get("failure", {}).get("field_paths", [])
            if isinstance(item, str)
        }
        failure_repairs = {
            str(item)
            for item in state.get("failure", {}).get("allowed_repairs", [])
            if isinstance(item, str)
        }
        repair_policies = []
        if definition is not None:
            repair_policies = [
                {
                    key: policy.get(key)
                    for key in (
                        "operator_id",
                        "field_path",
                        "extension",
                        "source_field",
                        "semantics_preserving",
                    )
                }
                for policy in definition.get("repair_policies", [])
                if isinstance(policy, Mapping)
                and policy.get("semantics_preserving") is True
                and policy.get("field_path") in failure_paths
                and policy.get("operator_id") in failure_repairs
            ]
        repair_context = with_model_allowance(
            state,
            {
                "repair_history": list(state.get("repair_history", [])),
                "repair_policies": repair_policies,
            },
            node=node,
        )
        allowance = dict(repair_context.get("model_allowance", {}))
        usage = state.get("budget_usage", {})
        limits = state.get("budget_limits", {})
        allowance["remaining_calls"] = min(
            int(allowance.get("remaining_calls", 0)),
            max(
                0,
                int(limits.get("max_repair_model_calls", policy.max_repair_model_calls))
                - int(usage.get("repair_model_calls", 0)),
            ),
        )
        allowance["remaining_cost_micros"] = min(
            int(allowance.get("remaining_cost_micros", 0)),
            max(
                0,
                int(limits.get("max_repair_cost_micros", policy.max_repair_cost_micros))
                - int(usage.get("repair_cost_micros", 0)),
            ),
        )
        allowance["remaining_prompt_tokens"] = min(
            int(allowance.get("remaining_prompt_tokens", 0)), 2_048
        )
        allowance["remaining_completion_tokens"] = min(
            int(allowance.get("remaining_completion_tokens", 0)), 256
        )
        repair_context["model_allowance"] = allowance
        try:
            decision = repairer(
                module=state["module"],
                message=state["user_message"],
                tool_name=tool_name,
                arguments=arguments,
                failure=state.get("failure", {}),
                context=repair_context,
            )
            if decision.strategy == "operator":
                restricted_failure = {
                    **state.get("failure", {}),
                    "allowed_repairs": [decision.operator_id],
                }
                planned = (
                    apply_known_repair(arguments, restricted_failure, definition)
                    if definition is not None
                    else None
                )
                if planned is None:
                    raise ValueError("repairer selected an unavailable operator")
                planned["strategy"] = "llm_operator"
                planned["reason_code"] = decision.reason_code
            elif decision.strategy == "restricted_patch":
                repaired, changed = apply_restricted_patches(
                    arguments,
                    [dict(item) for item in decision.patches],
                    allowed_paths={
                        str(item)
                        for item in state.get("failure", {}).get("field_paths", [])
                        if isinstance(item, str)
                    },
                )
                planned = {
                    "strategy": "llm_patch",
                    "operator_id": decision.operator_id,
                    "arguments": repaired,
                    "changed_paths": changed,
                    "before_args_hash": canonical_arguments_hash(arguments),
                    "after_args_hash": canonical_arguments_hash(repaired),
                    "reason_code": decision.reason_code,
                    "repair_catalog_version": REPAIR_CATALOG_VERSION,
                }
            else:
                planned = {
                    "strategy": decision.strategy,
                    "reason_code": decision.reason_code,
                }
        except ModelBudgetExceeded as exc:
            return model_terminal_update(state, node=node, reason=str(exc))
        except Exception as exc:
            return model_failure_update(
                state,
                node=node,
                role="repairer",
                started_ns=started_ns,
                error=exc,
            )
        model_update = model_observability_update(
            state,
            node=node,
            role="repairer",
            started_ns=started_ns,
        )
        update: dict[str, Any] = {
            **model_update,
            "repair": planned,
            "steps": state.get("steps", 0) + 1,
        }
        if planned.get("strategy") in ("ask_user", "abort"):
            update["outcome"] = "tool_failed"
            update["response"] = (
                "工具失败后需要调整业务参数，请补充或确认后再重试。"
                if planned.get("strategy") == "ask_user"
                else _safe_failure_response(state.get("failure", {}))
            )
        return update

    def after_repair_plan(state: AgentState) -> str:
        return (
            "validate_repair"
            if isinstance(state.get("repair", {}).get("arguments"), Mapping)
            else "finalize"
        )

    def validate_repair(state: AgentState) -> dict[str, Any]:
        started_ns = time.perf_counter_ns()
        proposed = state.get("proposed_tool", {})
        current = cast(Mapping[str, Any], proposed.get("arguments") or {})
        failure = state.get("failure", {})
        repair = dict(state.get("repair", {}))
        transient = repair.get("strategy") == "transient"
        repaired: Any
        if transient:
            repaired = dict(current)
            repair.update(
                {
                    "operator_id": "transport.retry",
                    "arguments": repaired,
                    "changed_paths": [],
                    "before_args_hash": canonical_arguments_hash(current),
                    "after_args_hash": canonical_arguments_hash(repaired),
                }
            )
        else:
            repaired = repair.get("arguments")
        reason = ""
        if not isinstance(repaired, Mapping):
            reason = "missing_repaired_arguments"
        else:
            repaired = dict(repaired)
            allowed_paths = {
                str(item) for item in failure.get("field_paths", []) if isinstance(item, str)
            }
            changed_paths = [str(item) for item in repair.get("changed_paths", [])]
            allowed_repairs = {
                str(item) for item in failure.get("allowed_repairs", []) if isinstance(item, str)
            }
            definition = _trusted_tool_definition(state, str(proposed.get("name") or ""))
            if transient:
                if failure.get("retry_same_input") is not True:
                    reason = "same_input_retry_not_allowed"
            elif not set(changed_paths) or not set(changed_paths).issubset(allowed_paths):
                reason = "repair_changed_unapproved_paths"
            elif str(repair.get("operator_id") or "") not in allowed_repairs:
                reason = "repair_operator_not_allowed_by_failure"
            elif definition is None or not repair_operator_allowed(
                definition, str(repair.get("operator_id") or ""), changed_paths
            ):
                reason = "repair_operator_not_allowed"
            if not reason and definition is not None:
                try:
                    _validate_tool_arguments(repaired, definition)
                except ValueError:
                    reason = "repaired_arguments_invalid"
            before_hash = canonical_arguments_hash(current)
            after_hash = canonical_arguments_hash(repaired)
            if not reason and not transient and before_hash == after_hash:
                reason = "repair_did_not_change_arguments"
            fingerprint = _repair_failure_fingerprint(
                str(proposed.get("name") or ""),
                str(failure.get("code") or "tool_failed"),
                repaired,
            )
            if not reason and fingerprint in state.get("repair_fingerprints", []):
                reason = "repair_loop_detected"
        if reason:
            return {
                "repair": {**repair, "validation": "blocked", "reason": reason},
                "outcome": "tool_failed",
                "response": _safe_failure_response(failure),
                "node_trace": [
                    *state.get("node_trace", []),
                    _trace_event(
                        "validate_repair",
                        "blocked",
                        started_ns=started_ns,
                        details={"reason": reason},
                    ),
                ],
                "steps": state.get("steps", 0) + 1,
            }
        attempt = state.get("repair_attempts", 0) + 1
        record = {
            "attempt": attempt,
            "strategy": repair.get("strategy"),
            "operator_id": repair.get("operator_id"),
            "failure_code": failure.get("code"),
            "changed_paths": repair.get("changed_paths", []),
            "before_args_hash": repair.get("before_args_hash"),
            "after_args_hash": repair.get("after_args_hash"),
            "repair_catalog_version": REPAIR_CATALOG_VERSION,
            "validation": "succeeded",
        }
        return {
            "proposed_tool": {**dict(proposed), "arguments": dict(repaired)},
            "repair": {**repair, "validation": "succeeded", "fingerprint": fingerprint},
            "repair_attempts": attempt,
            "repair_history": [*state.get("repair_history", []), record],
            "repair_fingerprints": [
                *state.get("repair_fingerprints", []),
                fingerprint,
            ],
            "outcome": "",
            "node_trace": [
                *state.get("node_trace", []),
                _trace_event(
                    "validate_repair",
                    "succeeded",
                    started_ns=started_ns,
                    details={
                        "attempt": attempt,
                        "operator_id": repair.get("operator_id"),
                        "changed_paths": repair.get("changed_paths", []),
                    },
                ),
            ],
            "steps": state.get("steps", 0) + 1,
        }

    def retry_tool(state: AgentState) -> dict[str, Any]:
        started_ns = time.perf_counter_ns()
        retry = getattr(tools, "retry_task", None)
        if not callable(retry):
            raise ValueError("tool gateway must implement retry_task")
        task_id = state.get("failed_task_id", "")
        repair = state.get("repair", {})
        arguments = cast(Mapping[str, Any], state.get("proposed_tool", {}).get("arguments") or {})
        outcome = retry(
            run_id=state["run_id"],
            user_id=state["user_id"],
            module=state["module"],
            task_id=task_id,
            arguments=arguments,
            operator_id=str(repair.get("operator_id") or ""),
            idempotency_key=(
                f"{state['run_id']}:action:{state.get('action_index', 0)}:"
                f"repair:{state.get('repair_attempts', 0)}:"
                f"{str(repair.get('after_args_hash') or '')[:24]}"
            ),
        )
        if not outcome.response.strip():
            raise ValueError("tool repair retry must contain a response")
        return {
            "tool_result": asdict(outcome),
            "response": outcome.response,
            "pending_task_id": "",
            "task_polls": 0,
            "failure": {},
            "node_trace": [
                *state.get("node_trace", []),
                _trace_event(
                    "retry_tool",
                    "succeeded",
                    started_ns=started_ns,
                    details={
                        "task_id": task_id,
                        "repair_attempt": state.get("repair_attempts", 0),
                        "operator_id": repair.get("operator_id"),
                    },
                ),
            ],
            "steps": state.get("steps", 0) + 1,
        }

    def after_repair_validation(state: AgentState) -> str:
        return (
            "retry_tool"
            if state.get("repair", {}).get("validation") == "succeeded" and not state.get("outcome")
            else "finalize"
        )

    def reconcile_side_effect(state: AgentState) -> dict[str, Any]:
        return {
            "outcome": "tool_failed",
            "response": "工具失败且可能已经产生部分结果，已停止自动重试；请在历史任务中核对后再操作。",
            "node_trace": [
                *state.get("node_trace", []),
                _trace_event(
                    "reconcile_side_effect",
                    "blocked",
                    details={"reason": "side_effect_state_unknown"},
                ),
            ],
            "steps": state.get("steps", 0) + 1,
        }

    def assess_progress(state: AgentState) -> dict[str, Any]:
        reason = model_access_reason(state, "assess_progress")
        if reason:
            terminal_update = model_terminal_update(
                state,
                node="assess_progress",
                reason=reason,
            )
            terminal_update["assessment"] = {"status": "blocked", "reason": reason}
            return terminal_update
        started_ns = time.perf_counter_ns()
        assessment_context = with_model_allowance(
            state,
            state.get("context", {}),
            node="assess_progress",
        )
        assessment_context["agent_plan"] = dict(state.get("plan", {}))
        assessment_context["observations"] = list(state.get("observations", []))
        assessment_context["action_index"] = state.get("action_index", 0)
        assessment_context["task_contract"] = dict(state.get("task_contract", {}))
        try:
            assessment = decisions.assess(
                module=state["module"],
                message=state["user_message"],
                context=assessment_context,
            )
            if assessment.status not in ("completed", "continue", "blocked"):
                raise ValueError("agent assessment status is invalid")
            if not assessment.reason.strip():
                raise ValueError("agent assessment reason is required")
        except ModelBudgetExceeded as exc:
            terminal_update = model_terminal_update(
                state,
                node="assess_progress",
                reason=str(exc),
            )
            terminal_update["assessment"] = {
                "status": "blocked",
                "reason": str(exc),
            }
            return terminal_update
        except Exception as exc:
            failure_update = model_failure_update(
                state,
                node="assess_progress",
                role="assessor",
                started_ns=started_ns,
                error=exc,
            )
            failure_update["assessment"] = {
                "status": "blocked",
                "reason": "模型服务未能完成进度评估",
            }
            return failure_update
        model_update = model_observability_update(
            state,
            node="assess_progress",
            role="assessor",
            started_ns=started_ns,
        )
        update: dict[str, Any] = {
            **model_update,
            "assessment": asdict(assessment),
            "steps": state.get("steps", 0) + 1,
        }
        if assessment.status == "completed":
            update["outcome"] = "completed"
        elif assessment.status == "blocked":
            update["outcome"] = "blocked"
        else:
            update["outcome"] = ""
        return update

    def after_assessment(state: AgentState) -> str:
        status = state.get("assessment", {}).get("status")
        if (
            status in ("completed", "continue")
            and not task_contract_artifact_satisfied(
                state.get("task_contract", {}),
                state.get("artifact_validation", {}),
            )
            and state.get("action_index", 0) < state.get("action_budget", policy.max_actions)
        ):
            return "continue_action"
        return "continue_action" if status == "continue" else "finalize"

    def wait_task(state: AgentState) -> dict[str, Any]:
        polls = state.get("task_polls", 0)
        max_resumes = int(
            state.get("budget_limits", {}).get("max_tool_resumes", policy.max_tool_resumes)
        )
        if polls >= max_resumes:
            return {
                "pending_task_id": "",
                "outcome": "tool_timeout",
                "response": "工作任务仍在处理中，恢复预算已用完；可稍后到工作台查看结果。",
                "node_trace": [
                    *state.get("node_trace", []),
                    _trace_event(
                        "wait_task",
                        "blocked",
                        details={"reason": "tool_resume_limit", "polls": polls},
                    ),
                ],
                "steps": state.get("steps", 0) + 1,
            }
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
                "run_id": state["run_id"],
                "task_id": task_id,
                "resume_attempt": polls + 1,
                "poll_after_ms": policy.tool_poll_interval_ms,
                "poll_max_ms": policy.tool_poll_max_interval_ms,
                "resume_schema": {
                    "type": "tool_poll",
                    "task_id": task_id,
                },
            }
        )
        if not isinstance(resolution, dict) or resolution.get("type") not in (
            "tool_poll",
            "tool_result",
        ):
            raise ValueError("tool wait resolution requires type:tool_poll")
        resolved_task_id = resolution.get("task_id")
        if isinstance(resolved_task_id, str) and resolved_task_id != task_id:
            raise ValueError("tool wait resolution task_id does not match")
        started_ns = time.perf_counter_ns()
        outcome = tools.observe(
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
            "recovery": _updated_recovery(
                state,
                counter="tool_resumes",
                interrupt_type="tool_wait",
            ),
            "node_trace": [
                *state.get("node_trace", []),
                _trace_event(
                    "wait_task",
                    "resumed",
                    started_ns=started_ns,
                    details={
                        "task_id": task_id,
                        "resume_attempt": polls + 1,
                        "observed_status": _skill_run_status(outcome.data),
                    },
                ),
            ],
            "steps": state.get("steps", 0) + 1,
        }

    def after_wait(state: AgentState) -> str:
        if state.get("outcome") == "tool_timeout":
            return "finalize"
        status = _skill_run_status(state.get("tool_result", {}).get("data"))
        if status in ("queued", "running"):
            return "wait_task"
        return "observe_result"

    def continue_action(state: AgentState) -> dict[str, Any]:
        return {
            "proposed_tool": {},
            "preparation": {},
            "tool_result": {},
            "pending_task_id": "",
            "task_polls": 0,
            "assessment": {},
            "outcome": "",
            "needs_response": False,
            "failure": {},
            "failed_task_id": "",
            "repair": {},
            "repair_started_at_ms": 0,
            "email_validation": {},
            "email_rewrite_attempts": 0,
            "presentation_validation": {},
            "presentation_rewrite_attempts": 0,
            "document_processing": {},
            "node_trace": [
                *state.get("node_trace", []),
                _trace_event("continue_action", "succeeded"),
            ],
            "steps": state.get("steps", 0) + 1,
        }

    def finalize(state: AgentState) -> dict[str, Any]:
        if not state.get("response", "").strip():
            raise ValueError("final response is required")
        outcome = state.get("outcome", "completed")
        response = state.get("response", "")
        artifact_validation = state.get("artifact_validation", {})
        if (
            artifact_validation.get("applicable") is True
            and artifact_validation.get("passed") is not True
        ):
            # Preserve a more specific upstream model terminal.  Earlier
            # versions overwrote model_invalid_response here, which made a
            # malformed repair response look like a generic artifact failure.
            if outcome not in (
                "model_budget_exhausted",
                "model_version_mismatch",
                "model_authentication_error",
                "model_invalid_response",
                "model_unavailable",
            ):
                outcome = "artifact_quality_failed"
                violation_codes = [
                    str(item.get("code") or "")
                    for item in artifact_validation.get("violations", [])
                    if isinstance(item, Mapping) and str(item.get("code") or "").strip()
                ]
                detail = "、".join(dict.fromkeys(violation_codes[:5]))
                response = "生成文件未通过任务完整性与制品质量门禁，已停止交付不合格结果。"
                if detail:
                    response += f" 未通过项：{detail}。"
        elif outcome in ("", "completed") and not task_contract_artifact_satisfied(
            state.get("task_contract", {}), artifact_validation
        ):
            outcome = "artifact_missing"
            response = "任务要求的生成文件尚未产出并通过质量门禁，已停止文字结果提前收尾。"
        return {
            "outcome": outcome,
            "response": response,
            "node_trace": [
                *state.get("node_trace", []),
                _trace_event(
                    "finalize",
                    "succeeded",
                    details={"outcome": outcome},
                ),
            ],
            "steps": state.get("steps", 0) + 1,
        }

    builder = StateGraph(AgentState)

    def register(
        name: str,
        function: Callable[[AgentState], dict[str, Any] | Command[Any]],
    ) -> None:
        # LangGraph's public overload cannot currently express a callable that
        # returns either a state update or Command. Keep the cast at this one
        # integration boundary while retaining strict types inside the graph.
        builder.add_node(name, cast(Any, contracted(name, function)))

    register("supervisor", supervise)
    register("plan", create_plan)
    register("companion", decide)
    register("life", decide)
    register("work", decide)
    register("compose_arguments", compose_arguments)
    register("email_quality_gate", email_quality_gate)
    register("preflight_normalize", preflight_normalize)
    register("generate_response", generate_response)
    register("response_quality_gate", response_quality_gate)
    register("revise_response", revise_response)
    register("prepare_tool", prepare_tool)
    register("approval", approval)
    register("commit_tool", commit_tool)
    register("reject_tool", reject_tool)
    register("observe_result", observe_result)
    register("continue_document_extraction", continue_document_extraction)
    register("artifact_quality_gate", artifact_quality_gate)
    register("classify_tool_failure", classify_tool_failure)
    register("apply_known_repair", apply_known_repair_node)
    register("plan_repair_llm", plan_repair_llm)
    register("validate_repair", validate_repair)
    register("retry_tool", retry_tool)
    register("reconcile_side_effect", reconcile_side_effect)
    register("assess_progress", assess_progress)
    register("wait_task", wait_task)
    register("continue_action", continue_action)
    register("finalize", finalize)

    builder.add_edge(START, "supervisor")
    builder.add_conditional_edges(
        "supervisor",
        _module_route,
        {
            "companion": "companion",
            "life": "life",
            "work": "work",
        },
    )
    for module in ("companion", "life", "work"):
        builder.add_conditional_edges(
            module,
            after_decision,
            {
                "plan": "plan",
                "compose_arguments": "compose_arguments",
                "preflight_normalize": "preflight_normalize",
                "generate_response": "generate_response",
                "response_quality_gate": "response_quality_gate",
                "finalize": "finalize",
            },
        )
    builder.add_conditional_edges(
        "plan",
        after_plan,
        {
            "compose_arguments": "compose_arguments",
            "preflight_normalize": "preflight_normalize",
            "generate_response": "generate_response",
            "response_quality_gate": "response_quality_gate",
            "finalize": "finalize",
        },
    )
    builder.add_conditional_edges(
        "compose_arguments",
        after_composition,
        {
            "compose_arguments": "compose_arguments",
            "email_quality_gate": "email_quality_gate",
            "finalize": "finalize",
        },
    )
    builder.add_conditional_edges(
        "email_quality_gate",
        after_email_quality,
        {
            "compose_arguments": "compose_arguments",
            "preflight_normalize": "preflight_normalize",
            "finalize": "finalize",
        },
    )
    builder.add_conditional_edges(
        "preflight_normalize",
        after_preflight,
        {"prepare_tool": "prepare_tool", "finalize": "finalize"},
    )
    builder.add_edge("generate_response", "response_quality_gate")
    builder.add_conditional_edges(
        "response_quality_gate",
        after_response_quality,
        {
            "revise_response": "revise_response",
            "finalize": "finalize",
        },
    )
    builder.add_conditional_edges(
        "revise_response",
        after_response_revision,
        {
            "response_quality_gate": "response_quality_gate",
            "finalize": "finalize",
        },
    )
    builder.add_conditional_edges(
        "prepare_tool",
        after_prepare,
        {
            "approval": "approval",
            "observe_result": "observe_result",
            "finalize": "finalize",
        },
    )
    builder.add_edge("commit_tool", "observe_result")
    builder.add_conditional_edges(
        "observe_result",
        after_observation,
        {
            "wait_task": "wait_task",
            "continue_document_extraction": "continue_document_extraction",
            "assess_progress": "assess_progress",
            "classify_tool_failure": "classify_tool_failure",
            "artifact_quality_gate": "artifact_quality_gate",
            "finalize": "finalize",
        },
    )
    builder.add_conditional_edges(
        "continue_document_extraction",
        lambda state: (
            "finalize" if state.get("outcome") == "action_limit" else "preflight_normalize"
        ),
        {
            "preflight_normalize": "preflight_normalize",
            "finalize": "finalize",
        },
    )
    builder.add_conditional_edges(
        "artifact_quality_gate",
        after_artifact_quality,
        {"continue_action": "continue_action", "finalize": "finalize"},
    )
    builder.add_conditional_edges(
        "classify_tool_failure",
        after_failure_classification,
        {
            "apply_known_repair": "apply_known_repair",
            "validate_repair": "validate_repair",
            "plan_repair_llm": "plan_repair_llm",
            "reconcile_side_effect": "reconcile_side_effect",
            "finalize": "finalize",
        },
    )
    builder.add_conditional_edges(
        "apply_known_repair",
        after_known_repair,
        {
            "validate_repair": "validate_repair",
            "plan_repair_llm": "plan_repair_llm",
        },
    )
    builder.add_conditional_edges(
        "plan_repair_llm",
        after_repair_plan,
        {"validate_repair": "validate_repair", "finalize": "finalize"},
    )
    builder.add_conditional_edges(
        "validate_repair",
        after_repair_validation,
        {"retry_tool": "retry_tool", "finalize": "finalize"},
    )
    builder.add_edge("retry_tool", "observe_result")
    builder.add_edge("reconcile_side_effect", "finalize")
    builder.add_conditional_edges(
        "assess_progress",
        after_assessment,
        {
            "continue_action": "continue_action",
            "finalize": "finalize",
        },
    )
    builder.add_conditional_edges(
        "wait_task",
        after_wait,
        {
            "wait_task": "wait_task",
            "observe_result": "observe_result",
            "finalize": "finalize",
        },
    )
    builder.add_conditional_edges(
        "continue_action",
        _module_route,
        {"companion": "companion", "life": "life", "work": "work"},
    )
    builder.add_edge("reject_tool", END)
    builder.add_edge("finalize", END)
    return builder.compile(checkpointer=checkpointer)


def _skill_run_status(value: Any) -> str:
    if not isinstance(value, dict) or value.get("kind") != "skill_run":
        return ""
    status = value.get("status")
    return status if isinstance(status, str) else ""


def _skill_run_id(value: Any) -> str:
    if not isinstance(value, dict) or value.get("kind") != "skill_run":
        return ""
    task_id = value.get("id")
    return task_id.strip() if isinstance(task_id, str) else ""


def _skill_run_failure(value: Any) -> dict[str, Any]:
    if not isinstance(value, dict):
        return _generic_tool_failure("tool_failed")
    raw = value.get("failure")
    if (
        isinstance(raw, dict)
        and raw.get("contract_version") == TOOL_FAILURE_CONTRACT_VERSION
        and isinstance(raw.get("code"), str)
    ):
        failure = {
            "contract_version": TOOL_FAILURE_CONTRACT_VERSION,
            "code": raw["code"].strip() or "tool_failed",
            "category": str(raw.get("category") or "execution"),
            "phase": str(raw.get("phase") or "execution"),
            "message": str(raw.get("message") or ""),
            "retry_same_input": raw.get("retry_same_input") is True,
            "repairable": raw.get("repairable") is True,
            "side_effect_state": str(raw.get("side_effect_state") or "unknown"),
            "field_paths": [
                item
                for item in raw.get("field_paths", [])
                if isinstance(item, str) and item.startswith("/")
            ],
            "allowed_repairs": [
                item
                for item in raw.get("allowed_repairs", [])
                if isinstance(item, str) and item.strip()
            ],
            "safe_details": (
                dict(raw.get("safe_details", {}))
                if isinstance(raw.get("safe_details"), dict)
                else {}
            ),
        }
        return failure
    return _generic_tool_failure(str(value.get("error_code") or "tool_failed"))


def _generic_tool_failure(code: str) -> dict[str, Any]:
    return {
        "contract_version": TOOL_FAILURE_CONTRACT_VERSION,
        "code": code.strip() or "tool_failed",
        "category": "execution",
        "phase": "execution",
        "message": "",
        "retry_same_input": False,
        "repairable": False,
        "side_effect_state": "unknown",
        "field_paths": [],
        "allowed_repairs": [],
        "safe_details": {},
    }


def _repair_failure_fingerprint(
    tool_name: str,
    failure_code: str,
    arguments: Mapping[str, Any],
) -> str:
    return canonical_arguments_hash(
        {
            "tool": tool_name,
            "code": failure_code,
            "arguments": dict(arguments),
        }
    )


def _safe_failure_response(failure: Mapping[str, Any]) -> str:
    message = failure.get("message")
    if isinstance(message, str) and message.strip():
        return "工作任务执行失败：" + message.strip()
    return "工作任务执行失败，已停止不安全或无效的自动重试；可以在历史任务中查看详情。"


def _observation(state: AgentState, source: Mapping[str, Any]) -> dict[str, Any]:
    return {
        "action": state.get("action_index", 0),
        "tool_name": str(state.get("proposed_tool", {}).get("name") or ""),
        "arguments": dict(state.get("proposed_tool", {}).get("arguments") or {}),
        "status": _skill_run_status(source.get("data")) or "completed",
        "response": str(source.get("response") or "").strip(),
        "data": source.get("data"),
    }


def _latest_observation_requires_artifact_gate(state: AgentState) -> bool:
    observations = state.get("observations", [])
    if not observations:
        return False
    data = observations[-1].get("data")
    return artifact_observation_applicable(data)


def _latest_document_continuation(state: AgentState) -> dict[str, int]:
    observations = state.get("observations", [])
    if not observations:
        return {}
    latest = observations[-1]
    if (
        not isinstance(latest, Mapping)
        or latest.get("tool_name") != "work_extract_attached_document"
    ):
        return {}
    data = latest.get("data")
    output = data.get("output") if isinstance(data, Mapping) else None
    if not isinstance(output, Mapping) or output.get("has_more") is not True:
        return {}
    next_round = output.get("next_round")
    if not isinstance(next_round, int) or isinstance(next_round, bool) or next_round < 1:
        return {}
    arguments = latest.get("arguments")
    attachment_index = (
        int(arguments.get("attachment_index") or 1) if isinstance(arguments, Mapping) else 1
    )
    return {"attachment_index": attachment_index, "round_start": next_round}


def _document_source_coverage(state: AgentState) -> dict[str, Any]:
    observations = state.get("observations", [])
    reports: list[tuple[int, Mapping[str, Any]]] = []
    for observation in observations:
        if not isinstance(observation, Mapping):
            continue
        if observation.get("tool_name") != "work_extract_attached_document":
            continue
        data = observation.get("data")
        output = data.get("output") if isinstance(data, Mapping) else None
        if isinstance(output, Mapping):
            arguments = observation.get("arguments")
            attachment_index = (
                int(arguments.get("attachment_index") or 1) if isinstance(arguments, Mapping) else 1
            )
            reports.append((attachment_index, output))
    if not reports:
        task_contract = state.get("task_contract", {})
        source_required = task_contract.get("source_required") is True
        source_document_ids = task_contract.get("source_document_ids")
        expected_attachments = (
            len(source_document_ids) if isinstance(source_document_ids, list) else 0
        )
        return {
            "coverage_ratio": 0.0 if source_required else 1.0,
            "truncated": source_required,
            "completed_attachment_count": 0,
            "expected_attachment_count": expected_attachments,
            "completed_rounds": 0,
            "round_count": 0,
            "selected_chunk_count": 0,
            "total_chunk_count": 0,
        }
    attachments: dict[int, dict[str, Any]] = {}
    for attachment_index, report in reports:
        current = attachments.setdefault(
            attachment_index,
            {
                "selected_ordinals": set(),
                "round_nos": set(),
                "fallback_windows": set(),
                "fallback_selected": 0,
                "fallback_rounds": 0,
                "total": 0,
                "round_count": 0,
            },
        )
        current["total"] = max(current["total"], int(report.get("total_chunk_count") or 0))
        current["round_count"] = max(current["round_count"], int(report.get("round_count") or 0))
        manifest = report.get("rounds")
        valid_manifest = False
        if isinstance(manifest, list):
            for item in manifest:
                if not isinstance(item, Mapping):
                    continue
                start = item.get("chunk_start")
                end = item.get("chunk_end")
                round_no = item.get("round_no")
                if (
                    isinstance(start, int)
                    and not isinstance(start, bool)
                    and isinstance(end, int)
                    and not isinstance(end, bool)
                    and 0 <= start <= end
                ):
                    current["selected_ordinals"].update(range(start, end + 1))
                    valid_manifest = True
                if isinstance(round_no, int) and not isinstance(round_no, bool):
                    current["round_nos"].add(round_no)
        if not valid_manifest:
            window = (
                int(report.get("round_start") or 1),
                int(report.get("next_round") or 0),
                int(report.get("selected_chunk_count") or 0),
            )
            if window not in current["fallback_windows"]:
                current["fallback_windows"].add(window)
                current["fallback_selected"] += window[2]
                current["fallback_rounds"] += int(report.get("completed_rounds") or 0)
    selected_chunks = sum(
        min(
            len(item["selected_ordinals"]) + item["fallback_selected"],
            item["total"],
        )
        for item in attachments.values()
    )
    total_chunks = sum(item["total"] for item in attachments.values())
    chunk_coverage_ratio = selected_chunks / total_chunks if total_chunks else 0.0
    source_document_ids = state.get("task_contract", {}).get("source_document_ids")
    expected_attachments = len(source_document_ids) if isinstance(source_document_ids, list) else 0
    attachment_coverage_ratio = (
        min(1.0, len(attachments) / expected_attachments) if expected_attachments > 0 else 1.0
    )
    coverage_ratio = chunk_coverage_ratio * attachment_coverage_ratio
    return {
        "coverage_ratio": coverage_ratio,
        "truncated": selected_chunks < total_chunks or attachment_coverage_ratio < 1,
        "completed_attachment_count": len(attachments),
        "expected_attachment_count": expected_attachments,
        "completed_rounds": sum(
            len(item["round_nos"]) + item["fallback_rounds"] for item in attachments.values()
        ),
        "round_count": sum(item["round_count"] for item in attachments.values()),
        "selected_chunk_count": selected_chunks,
        "total_chunk_count": total_chunks,
    }


def _decision_manifest(decisions: DecisionPort) -> dict[str, Any]:
    provider = getattr(decisions, "model_manifest", None)
    if callable(provider):
        value = provider()
        if isinstance(value, dict) and model_config_version(value):
            manifest = dict(value)
            manifest["fingerprint"] = model_manifest_fingerprint(manifest)
            return manifest
        raise ValueError("decision port model manifest requires config_version")
    implementation = type(decisions).__name__
    manifest = {
        "provider": implementation,
        "config_version": f"{implementation}:v1",
        "pinned": True,
        "roles": {},
    }
    manifest["fingerprint"] = model_manifest_fingerprint(manifest)
    return manifest


def _trace_event(
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
    if started_ns <= 0:
        return 0
    return max(0, (time.perf_counter_ns() - started_ns) // 1_000_000)


def _updated_recovery(
    state: AgentState,
    *,
    counter: str,
    interrupt_type: str,
) -> dict[str, Any]:
    recovery = dict(state.get("recovery", {}))
    current = recovery.get(counter, 0)
    recovery[counter] = (
        current + 1 if isinstance(current, int) and not isinstance(current, bool) else 1
    )
    recovery["last_interrupt_type"] = interrupt_type
    return recovery


def _snapshot_interrupt_payloads(snapshot: Any) -> list[dict[str, Any]]:
    payloads: list[dict[str, Any]] = []
    tasks = getattr(snapshot, "tasks", ())
    for task in tasks:
        for item in getattr(task, "interrupts", ()):
            value = getattr(item, "value", None)
            if not isinstance(value, dict):
                continue
            payload = dict(value)
            interrupt_id = getattr(item, "id", None)
            if isinstance(interrupt_id, str) and interrupt_id:
                payload["interrupt_id"] = interrupt_id
            payloads.append(payload)
    return payloads


def _validate_resolution(
    resolution: Mapping[str, Any],
    pending: list[dict[str, Any]],
) -> dict[str, Any]:
    if len(pending) != 1:
        raise ValueError("Agent Run requires exactly one pending interrupt")
    expected = pending[0]
    expected_id = expected.get("interrupt_id")
    provided_id = resolution.get("interrupt_id")
    if isinstance(provided_id, str) and isinstance(expected_id, str) and provided_id != expected_id:
        raise ValueError("resume resolution interrupt_id is stale")
    expected_type = expected.get("type")
    normalized = dict(resolution)
    if isinstance(expected_id, str):
        normalized["interrupt_id"] = expected_id
    if expected_type == "tool_approval":
        if not isinstance(normalized.get("approved"), bool):
            raise ValueError("approval resolution must contain approved:boolean")
        normalized.setdefault("type", "tool_approval")
        return normalized
    if expected_type == "tool_wait":
        if normalized.get("type") not in ("tool_poll", "tool_result"):
            raise ValueError("tool wait resolution requires type:tool_poll")
        expected_task = expected.get("task_id")
        provided_task = normalized.get("task_id")
        if (
            isinstance(provided_task, str)
            and isinstance(expected_task, str)
            and provided_task != expected_task
        ):
            raise ValueError("tool wait resolution task_id is stale")
        if isinstance(expected_task, str):
            normalized["task_id"] = expected_task
        return normalized
    raise ValueError("unsupported Agent Run interrupt type")


def _validate_checkpoint_identity(
    values: Mapping[str, Any],
    *,
    run_id: str,
    initial: Mapping[str, Any] | None,
) -> None:
    graph_name = values.get("graph_name")
    graph_version = values.get("graph_version")
    if isinstance(graph_name, str) and graph_name and graph_name != GRAPH_NAME:
        raise ValueError("checkpoint graph name is incompatible with this runtime")
    if isinstance(graph_version, str) and graph_version and graph_version != GRAPH_VERSION:
        raise ValueError(
            "checkpoint graph version is incompatible with this runtime; "
            "drain or restart the old run"
        )

    # A checkpoint taken before supervisor has no governance identity yet and
    # is safe to continue into supervisor. Every supervised checkpoint is
    # bound to the exact Go-owned catalog used to make its model decisions.
    if graph_version != GRAPH_VERSION:
        return
    recorded = values.get("tool_catalog_fingerprint")
    if not isinstance(recorded, str) or not recorded:
        raise ValueError("checkpoint is missing its trusted tool catalog fingerprint")
    context = values.get("context", {})
    if not isinstance(context, Mapping):
        raise ValueError("checkpoint context must be an object")
    persisted = tool_catalog_fingerprint(context.get("tools", []))
    if persisted != recorded:
        raise ValueError("checkpoint trusted tool catalog fingerprint is invalid")
    if initial is None:
        return
    normalized = _validate_input(initial)
    if normalized["run_id"] != run_id:
        raise ValueError("initial Agent input run_id does not match checkpoint")
    # The catalog is immutable checkpoint state, not resume input. Production
    # resumes intentionally omit the catalog to avoid an unnecessary gateway
    # round trip, and deployments may expose a newer catalog while an older
    # run is waiting for approval or a tool result. Continue with the exact
    # persisted catalog after validating its fingerprint above. A resumed
    # graph can therefore neither replace nor erase the catalog it originally
    # used for model routing.
    for identity_field in (
        "user_id",
        "conversation_id",
        "character_id",
        "module",
        "user_message",
    ):
        persisted_identity = values.get(identity_field)
        if persisted_identity is not None and persisted_identity != normalized[identity_field]:
            raise ValueError(f"initial Agent input {identity_field} does not match checkpoint")


class AgentRuntime:
    def __init__(self, graph: Any) -> None:
        self._graph = graph

    def start(self, value: Mapping[str, Any]) -> dict[str, Any]:
        initial = _validate_input(value)
        return self.execute(initial["run_id"], initial=initial)

    def resume(self, run_id: str, resolution: Mapping[str, Any]) -> dict[str, Any]:
        return self.execute(run_id, resolution=resolution)

    def execute(
        self,
        run_id: str,
        *,
        initial: Mapping[str, Any] | None = None,
        resolution: Mapping[str, Any] | None = None,
    ) -> dict[str, Any]:
        config = runtime_config(run_id)
        snapshot = self._graph.get_state(config)
        values = getattr(snapshot, "values", {})
        values = dict(values) if isinstance(values, Mapping) else {}
        next_nodes = tuple(getattr(snapshot, "next", ()) or ())
        if not values:
            if resolution is not None:
                raise ValueError("cannot resume an Agent Run without a checkpoint")
            if initial is None:
                raise ValueError("initial Agent input is required")
            result = self._graph.invoke(
                _validate_input(initial),
                config=config,
            )
            return cast(dict[str, Any], result)
        _validate_checkpoint_identity(
            values,
            run_id=run_id,
            initial=initial,
        )
        pending = _snapshot_interrupt_payloads(snapshot)
        if pending:
            if resolution is None:
                values["__interrupt_payloads__"] = pending
                return cast(dict[str, Any], values)
            normalized = _validate_resolution(resolution, pending)
            result = self._graph.invoke(
                Command(resume=normalized),
                config=config,
            )
            return cast(dict[str, Any], result)
        if not next_nodes:
            return cast(dict[str, Any], values)
        # A durable caller may retain the trusted resolution after LangGraph
        # has checkpointed the interrupt node but a later node failed. In that
        # state the resolution is already consumed; replay from the next node
        # without applying it a second time.
        result = self._graph.invoke(None, config=config)
        return cast(dict[str, Any], result)


def interrupt_payloads(result: Mapping[str, Any]) -> list[dict[str, Any]]:
    cached = result.get("__interrupt_payloads__")
    if isinstance(cached, list):
        return [dict(item) for item in cached if isinstance(item, dict)]
    values = result.get("__interrupt__", ())
    payloads: list[dict[str, Any]] = []
    for item in values:
        value = getattr(item, "value", None)
        if isinstance(value, dict):
            payload = dict(value)
            interrupt_id = getattr(item, "id", None)
            if isinstance(interrupt_id, str) and interrupt_id:
                payload["interrupt_id"] = interrupt_id
            payloads.append(payload)
    return payloads
