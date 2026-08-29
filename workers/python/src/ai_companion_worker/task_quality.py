from __future__ import annotations

import re
from pathlib import PurePath
from typing import Any, Mapping


TASK_CONTRACT_VERSION = "task-contract-v1"
ARTIFACT_QUALITY_POLICY_VERSION = "artifact-quality-v1"

_ARTIFACT_SKILL_TYPES = {
    "office.markdown_document": "markdown",
    "office.docx_edit": "docx",
    "office.pptx_generate": "pptx",
    "office.tabular_profile": "json",
    "office.pdf_translate": "pdf",
}
_ARTIFACT_SUFFIX_TYPES = {
    ".md": "markdown",
    ".docx": "docx",
    ".pptx": "pptx",
    ".xlsx": "xlsx",
    ".pdf": "pdf",
    ".json": "json",
    ".csv": "csv",
}

_REQUESTED_FIELD_ALIASES = {
    "code": ("代码", "编号", "code"),
    "name": ("名称", "课程", "name"),
    "time": ("时间", "日期", "星期", "时段", "time", "date", "schedule"),
    "venue": ("地点", "教室", "场地", "venue", "location"),
}


def compile_task_contract(
    message: str,
    module: str,
    history: Any = None,
) -> dict[str, Any]:
    current = message.strip()
    normalized = _inherited_artifact_request(current, history)
    source_document_ids = _document_ids(current) or _document_ids(normalized)
    visible = re.sub(r"<!--ai-document:[^>]+-->", "", normalized)
    lowered = visible.casefold()
    artifact_types = _artifact_types(normalized)

    exhaustive = bool(
        re.search(
            r"(?:所有|全部|完整|逐一|每(?:一|门|项|条)|\ball\b|\bevery\b|\bcomplete\b)", lowered
        )
    )
    requested_fields: list[str] = []
    field_patterns = (
        ("code", r"(?:课程代码|课程编号|代码|编号|\bcode\b)"),
        ("name", r"(?:课程名称|名称|名字|\bname\b)"),
        ("time", r"(?:上课时间|时间|日期|星期|时段|\btime\b|\bdate\b|\bschedule\b)"),
        ("venue", r"(?:地点|教室|场地|\bvenue\b|\blocation\b)"),
    )
    for field, pattern in field_patterns:
        if re.search(pattern, lowered):
            requested_fields.append(field)

    hard_requirements = ["grounded_in_trusted_observations"]
    if artifact_types:
        hard_requirements.extend(("artifact_exists", "artifact_opens", "artifact_readable"))
    if exhaustive:
        hard_requirements.append("source_coverage_complete")
    if requested_fields:
        hard_requirements.append("requested_fields_present")

    return {
        "version": TASK_CONTRACT_VERSION,
        "module": module,
        "objective": normalized,
        "artifact_types": artifact_types,
        "source_document_ids": list(dict.fromkeys(source_document_ids)),
        "source_required": "<!--ai-document:" in current or "<!--ai-document:" in normalized,
        "exhaustive": exhaustive,
        "requested_fields": requested_fields,
        "output_language": "zh-CN" if re.search(r"(?:中文|汉语|chinese)", lowered) else "",
        "hard_requirements": hard_requirements,
        "completion_policy": "all_hard_requirements_pass",
        "inherited_from_history": normalized != current,
    }


def task_contract_requires_artifact(task_contract: Mapping[str, Any]) -> bool:
    artifact_types = task_contract.get("artifact_types")
    return isinstance(artifact_types, list) and any(
        isinstance(value, str) and bool(value.strip()) for value in artifact_types
    )


def task_contract_artifact_satisfied(
    task_contract: Mapping[str, Any],
    artifact_validation: Mapping[str, Any],
) -> bool:
    if not task_contract_requires_artifact(task_contract):
        return True
    return (
        artifact_validation.get("applicable") is True and artifact_validation.get("passed") is True
    )


def validate_presentation_arguments(
    arguments: Mapping[str, Any],
    task_contract: Mapping[str, Any],
    *,
    expected_record_keys: list[str] | None = None,
    source_ir: Mapping[str, Any] | None = None,
) -> list[dict[str, Any]]:
    """Reject structurally valid PPT arguments that cannot satisfy the task."""

    requested_fields = _requested_fields(task_contract)
    if not requested_fields:
        return []
    table = arguments.get("table")
    if not isinstance(table, Mapping):
        return [
            {
                "code": "structured_table_missing",
                "message": "结构化字段任务必须提供表格数据，不能只提交内容简报",
            }
        ]
    columns = table.get("columns")
    rows = table.get("rows")
    violations = _requested_field_violations(columns, requested_fields)
    if (
        task_contract.get("exhaustive") is True
        and task_contract.get("source_required") is True
        and (not source_ir or source_ir.get("structure_preserved") is not True)
    ):
        violations.append(
            {
                "code": "source_structure_unavailable",
                "message": "完整结构化来源任务必须使用真实 Source IR，不能退回自由文本推测",
            }
        )
    if not isinstance(rows, list) or not rows:
        violations.append(
            {"code": "structured_rows_missing", "message": "结构化字段任务没有记录行"}
        )
    elif isinstance(columns, list):
        mismatched_rows = [
            index
            for index, row in enumerate(rows, start=1)
            if not isinstance(row, Mapping)
            or not isinstance(row.get("cells"), list)
            or len(row["cells"]) != len(columns)
        ]
        if mismatched_rows:
            violations.append(
                {
                    "code": "table_cell_count_mismatch",
                    "message": "每条记录的单元格数量必须与表头数量一致",
                    "affected_rows": mismatched_rows[:20],
                    "affected_count": len(mismatched_rows),
                }
            )
        entity_rows: dict[str, list[int]] = {}
        delimiter_noise_rows: list[int] = []
        empty_cell_rows: list[int] = []
        repeated_delimiter = re.compile(r"(?:[；;]\s*){2,}")
        punctuation_only = re.compile(r"^[\s；;,，、|/\\:：.。·•↳\-–—]+$")
        for index, row in enumerate(rows, start=1):
            if not isinstance(row, Mapping):
                continue
            entity_id = str(row.get("entity_id") or "").strip()
            if entity_id:
                entity_rows.setdefault(entity_id, []).append(index)
            cells = row.get("cells")
            if not isinstance(cells, list):
                continue
            if any(not str(value or "").strip() for value in cells):
                empty_cell_rows.append(index)
            if any(
                repeated_delimiter.search(str(value or ""))
                or punctuation_only.fullmatch(str(value or "").strip())
                for value in cells
                if str(value or "").strip()
            ):
                delimiter_noise_rows.append(index)
        duplicate_entities = {
            entity_id: positions for entity_id, positions in entity_rows.items() if len(positions) > 1
        }
        if duplicate_entities:
            violations.append(
                {
                    "code": "duplicate_output_entities",
                    "message": "同一来源实体只能对应一条逻辑输出记录，续行只能在最终渲染时生成",
                    "entity_ids": list(duplicate_entities)[:20],
                    "affected_rows": [
                        row_index
                        for positions in list(duplicate_entities.values())[:20]
                        for row_index in positions
                    ][:40],
                }
            )
        if delimiter_noise_rows:
            violations.append(
                {
                    "code": "presentation_cell_delimiter_noise",
                    "message": "表格单元格包含重复分隔符或纯标点碎片",
                    "affected_rows": delimiter_noise_rows[:20],
                    "affected_count": len(delimiter_noise_rows),
                }
            )
        if empty_cell_rows:
            violations.append(
                {
                    "code": "presentation_cell_empty",
                    "message": "结构化表格的逻辑记录不得包含空单元格",
                    "affected_rows": empty_cell_rows[:20],
                    "affected_count": len(empty_cell_rows),
                }
            )
    if isinstance(rows, list) and rows and task_contract.get("exhaustive") is True:
        missing_locators = sum(
            1
            for row in rows
            if not isinstance(row, Mapping)
            or not isinstance(row.get("source_locator"), str)
            or not row["source_locator"].strip()
        )
        if missing_locators:
            violations.append(
                {
                    "code": "source_locator_missing",
                    "message": "完整性任务的每条记录都必须保留来源位置",
                    "missing_rows": missing_locators,
                }
            )
        time_column = _requested_field_column(
            columns if isinstance(columns, list) else [],
            "time",
        )
        if time_column >= 0:
            vague_rows: list[int] = []
            vague_pattern = re.compile(
                r"(?:详见|见(?:课程表|原表|原文)|(?:\betc\.?\b)|\.{3}|…|\bvarious\b|\bmultiple\b|"
                r"times?\s+vary|不同时段|多(?:个|组|种)(?:时段|时间|组次))",
                re.IGNORECASE,
            )
            for index, row in enumerate(rows):
                cells = row.get("cells") if isinstance(row, Mapping) else None
                if (
                    not isinstance(cells, list)
                    or time_column >= len(cells)
                    or vague_pattern.search(str(cells[time_column] or ""))
                ):
                    vague_rows.append(index + 1)
            if vague_rows:
                violations.append(
                    {
                        "code": "requested_time_values_incomplete",
                        "message": "完整性任务中的时间字段不得使用省略或“详见原表”占位",
                        "affected_rows": vague_rows[:20],
                        "affected_count": len(vague_rows),
                    }
                )
    if source_ir and source_ir.get("structure_preserved") is True:
        violations.extend(
            _source_mapping_violations(
                arguments,
                table if isinstance(table, Mapping) else {},
                source_ir,
                exhaustive=task_contract.get("exhaustive") is True,
            )
        )

    expected_record_keys = expected_record_keys or []
    if expected_record_keys and isinstance(columns, list) and isinstance(rows, list):
        code_column = _requested_field_column(columns, "code")
        if code_column >= 0:
            observed = {
                _canonical_record_key(row["cells"][code_column])
                for row in rows
                if isinstance(row, Mapping) and isinstance(row.get("cells"), list)
                if code_column < len(row["cells"])
            }
            missing = [key for key in expected_record_keys if key not in observed]
            if missing:
                violations.append(
                    {
                        "code": "source_records_missing",
                        "message": "表格没有覆盖来源中识别出的全部记录代码",
                        "expected_count": len(expected_record_keys),
                        "observed_count": len(observed),
                        "missing_keys": missing[:20],
                    }
                )
    return _deduplicate_violations(violations)


def _source_mapping_violations(
    arguments: Mapping[str, Any],
    table: Mapping[str, Any],
    source_ir: Mapping[str, Any],
    *,
    exhaustive: bool,
) -> list[dict[str, Any]]:
    violations: list[dict[str, Any]] = []
    mapping = arguments.get("mapping_contract")
    if not isinstance(mapping, Mapping):
        return [
            {
                "code": "source_mapping_contract_missing",
                "message": "结构化来源任务必须先锁定来源到目标的字段映射和实体粒度",
            }
        ]
    if mapping.get("version") != "target-mapping-v1":
        violations.append(
            {
                "code": "source_mapping_contract_version_invalid",
                "message": "来源映射契约版本无效",
            }
        )
    source_tables = {
        str(value.get("id") or ""): value
        for value in source_ir.get("tables", [])
        if isinstance(value, Mapping) and str(value.get("id") or "")
    }
    table_ids = mapping.get("source_table_ids")
    selected_ids = (
        list(dict.fromkeys(str(value) for value in table_ids if str(value)))
        if isinstance(table_ids, list)
        else []
    )
    unknown_tables = [value for value in selected_ids if value not in source_tables]
    if not selected_ids or unknown_tables:
        violations.append(
            {
                "code": "source_mapping_table_invalid",
                "message": "来源映射必须引用已提取的逻辑表",
                "unknown_table_ids": unknown_tables[:20],
            }
        )
        return violations
    entity_level = str(mapping.get("entity_level") or "")
    if entity_level not in ("row_group", "row"):
        violations.append(
            {
                "code": "source_mapping_entity_level_invalid",
                "message": "实体粒度必须是 row_group 或 row",
            }
        )
        return violations

    columns = table.get("columns")
    column_count = len(columns) if isinstance(columns, list) else 0
    available_columns = {
        str(column.get("id") or "")
        for table_id in selected_ids
        for column in source_tables[table_id].get("columns", [])
        if isinstance(column, Mapping) and str(column.get("id") or "")
    }
    field_mappings = mapping.get("field_mappings")
    mapped_targets: set[int] = set()
    mapped_sources: set[str] = set()
    target_sources: dict[int, set[str]] = {}
    if isinstance(field_mappings, list):
        for raw_mapping in field_mappings:
            if not isinstance(raw_mapping, Mapping):
                continue
            target_index = raw_mapping.get("target_index")
            source_columns = raw_mapping.get("source_column_ids")
            mode = str(raw_mapping.get("mode") or "")
            if (
                not isinstance(target_index, int)
                or isinstance(target_index, bool)
                or not 0 <= target_index < column_count
                or mode not in ("direct", "aggregate")
                or not isinstance(source_columns, list)
                or not source_columns
            ):
                continue
            normalized_sources = {str(value) for value in source_columns if str(value)}
            if not normalized_sources or not normalized_sources.issubset(available_columns):
                continue
            if mapped_sources.intersection(normalized_sources):
                violations.append(
                    {
                        "code": "source_column_mapping_ambiguous",
                        "message": "同一来源列不能在锁定契约中漂移到多个目标字段",
                    }
                )
            mapped_targets.add(target_index)
            mapped_sources.update(normalized_sources)
            target_sources.setdefault(target_index, set()).update(normalized_sources)
    if mapped_targets != set(range(column_count)):
        violations.append(
            {
                "code": "source_field_mapping_incomplete",
                "message": "每个目标字段都必须绑定到有效来源列",
                "mapped_target_indices": sorted(mapped_targets),
                "expected_target_count": column_count,
            }
        )
    uncovered_table_fields = [
        {"table_id": table_id, "target_index": target_index}
        for table_id in selected_ids
        for target_index in range(column_count)
        if not target_sources.get(target_index, set()).intersection(
            {
                str(column.get("id") or "")
                for column in source_tables[table_id].get("columns", [])
                if isinstance(column, Mapping)
            }
        )
    ]
    if uncovered_table_fields:
        violations.append(
            {
                "code": "source_table_field_mapping_incomplete",
                "message": "每个相关逻辑表都必须为全部目标字段提供来源列映射",
                "uncovered": uncovered_table_fields[:20],
            }
        )

    expected_entities: set[str] = set()
    entity_rows: dict[str, set[str]] = {}
    all_source_text: set[str] = set()
    for table_id in selected_ids:
        source_table = source_tables[table_id]
        rows = [value for value in source_table.get("rows", []) if isinstance(value, Mapping)]
        rows_by_group: dict[str, set[str]] = {}
        for row in rows:
            row_id = str(row.get("id") or "")
            group_id = str(row.get("group_id") or "")
            if not row_id:
                continue
            rows_by_group.setdefault(group_id, set()).add(row_id)
            for cell in row.get("cells", []):
                if not isinstance(cell, Mapping):
                    continue
                for key in ("text", "inherited_text"):
                    text = re.sub(r"\s+", " ", str(cell.get(key) or "")).strip().casefold()
                    if text:
                        all_source_text.add(text)
        if entity_level == "row":
            for row in rows:
                row_id = str(row.get("id") or "")
                if row_id:
                    expected_entities.add(row_id)
                    entity_rows[row_id] = {row_id}
        else:
            for group in source_table.get("row_groups", []):
                if not isinstance(group, Mapping):
                    continue
                group_id = str(group.get("id") or "")
                if not group_id:
                    continue
                expected_entities.add(group_id)
                entity_rows.setdefault(group_id, set()).update(rows_by_group.get(group_id, set()))

    output_rows = table.get("rows")
    observed_entities: set[str] = set()
    observed_refs: dict[str, set[str]] = {}
    extra_entities: list[str] = []
    missing_evidence: list[int] = []
    invalid_evidence: list[int] = []
    placeholder_rows: list[int] = []
    placeholder = re.compile(
        r"(?:未(?:知|提供|出现|找到)|待补|无来源|not\s+(?:found|provided|available)|unknown)",
        re.I,
    )
    if isinstance(output_rows, list):
        for index, row in enumerate(output_rows, start=1):
            if not isinstance(row, Mapping):
                continue
            entity_id = str(row.get("entity_id") or "").strip()
            if entity_id:
                observed_entities.add(entity_id)
                if entity_id not in expected_entities:
                    extra_entities.append(entity_id)
            source_refs = row.get("source_refs")
            refs = (
                {str(value) for value in source_refs if str(value)}
                if isinstance(source_refs, list)
                else set()
            )
            if not entity_id or not refs:
                missing_evidence.append(index)
            elif entity_id not in entity_rows or not refs.issubset(entity_rows[entity_id]):
                invalid_evidence.append(index)
            else:
                observed_refs.setdefault(entity_id, set()).update(refs)
            cells = row.get("cells")
            if isinstance(cells, list):
                for value in cells:
                    normalized = re.sub(r"\s+", " ", str(value or "")).strip().casefold()
                    if placeholder.search(normalized) and normalized not in all_source_text:
                        placeholder_rows.append(index)
                        break
    if extra_entities:
        violations.append(
            {
                "code": "unmapped_output_entities",
                "message": "输出包含无法映射回来源实体的额外记录",
                "entity_ids": list(dict.fromkeys(extra_entities))[:20],
            }
        )
    if exhaustive:
        missing_entities = sorted(expected_entities - observed_entities)
        if missing_entities:
            violations.append(
                {
                    "code": "source_entities_missing",
                    "message": "输出没有覆盖锁定粒度下的全部来源实体",
                    "expected_count": len(expected_entities),
                    "observed_count": len(observed_entities.intersection(expected_entities)),
                    "entity_ids": missing_entities[:20],
                }
            )
        unreferenced_rows = {
            entity_id: sorted(source_rows - observed_refs.get(entity_id, set()))
            for entity_id, source_rows in entity_rows.items()
            if source_rows - observed_refs.get(entity_id, set())
        }
        if unreferenced_rows:
            violations.append(
                {
                    "code": "source_child_rows_unreferenced",
                    "message": "完整性任务中每个父实体的全部来源子行都必须进入合并结果",
                    "affected_entity_count": len(unreferenced_rows),
                    "entity_ids": list(unreferenced_rows)[:20],
                    "missing_source_refs": [
                        value
                        for values in list(unreferenced_rows.values())[:20]
                        for value in values[:5]
                    ][:40],
                }
            )
    if missing_evidence:
        violations.append(
            {
                "code": "source_evidence_missing",
                "message": "每条输出记录必须携带来源实体 ID 和来源行引用",
                "affected_rows": missing_evidence[:20],
            }
        )
    if invalid_evidence:
        violations.append(
            {
                "code": "source_evidence_invalid",
                "message": "输出记录引用了不属于该来源实体的行",
                "affected_rows": invalid_evidence[:20],
            }
        )
    if placeholder_rows:
        violations.append(
            {
                "code": "ungrounded_placeholder_values",
                "message": "输出不得用来源中不存在的占位文本代替真实字段值",
                "affected_rows": placeholder_rows[:20],
            }
        )
    return violations


def presentation_source_record_keys(
    observations: Any,
    task_contract: Mapping[str, Any],
) -> list[str]:
    """Derive repeated line-leading identifiers for exhaustive code coverage."""

    if "code" not in _requested_fields(task_contract) or not isinstance(observations, list):
        return []
    grouped: dict[str, set[str]] = {}
    pattern = re.compile(r"(?mi)^\s*(?:[|•*-]\s*)?([A-Z]{2,8})\s*[- ]?\s*(\d{3,6}[A-Z]?)\b")
    for observation in observations:
        if not isinstance(observation, Mapping):
            continue
        data = observation.get("data")
        output = data.get("output") if isinstance(data, Mapping) else None
        text = output.get("text") if isinstance(output, Mapping) else None
        if not isinstance(text, str):
            continue
        for prefix, digits in pattern.findall(text):
            normalized_prefix = prefix.upper()
            grouped.setdefault(normalized_prefix, set()).add(normalized_prefix + digits.upper())
    repeated_groups = [values for values in grouped.values() if len(values) >= 2]
    return sorted({key for values in repeated_groups for key in values})


def _inherited_artifact_request(message: str, history: Any) -> str:
    """Continue only an explicit artifact request bound to the same attachment.

    This is deliberately narrower than general conversational goal inheritance:
    a generic follow-up may recover requirements only from a prior user turn that
    contains one of the exact trusted document markers present on the new turn.
    """
    if _artifact_types(message):
        return message
    document_ids = set(_document_ids(message))
    if not isinstance(history, list):
        return message
    if not document_ids:
        pending = _pending_artifact_request(message, history)
        return pending or message
    for item in reversed(history):
        if not isinstance(item, Mapping) or item.get("role") != "user":
            continue
        content = item.get("content")
        if not isinstance(content, str) or not document_ids.intersection(_document_ids(content)):
            continue
        if _artifact_types(content):
            return content.strip()
    return message


def _pending_artifact_request(message: str, history: list[Any]) -> str:
    if not _is_artifact_workflow_followup(message):
        return ""
    if len(history) < 2:
        return ""
    assistant = history[-1]
    user = history[-2]
    inherited_supplements: list[str] = []
    if (
        _is_failed_runtime_reply(assistant)
        and isinstance(user, Mapping)
        and user.get("role") == "user"
        and isinstance(user.get("content"), str)
        and _document_ids(str(user["content"]))
        and _artifact_types(str(user["content"]))
    ):
        return str(user["content"]).strip()
    if (
        _is_missing_attachment_reply(assistant)
        and isinstance(user, Mapping)
        and user.get("role") == "user"
        and isinstance(user.get("content"), str)
        and _is_artifact_workflow_followup(str(user["content"]))
        and len(history) >= 4
    ):
        inherited_supplements.append(str(user["content"]).strip())
        assistant = history[-3]
        user = history[-4]
    if not isinstance(assistant, Mapping) or not isinstance(user, Mapping):
        return ""
    assistant_text = assistant.get("content")
    user_text = user.get("content")
    if (
        assistant.get("role") != "assistant"
        or user.get("role") != "user"
        or not isinstance(assistant_text, str)
        or not isinstance(user_text, str)
        or not _is_attachment_workflow_confirmation(assistant_text)
        or not _document_ids(user_text)
        or not _artifact_types(user_text)
    ):
        return ""
    supplement = message.strip()
    if supplement and supplement not in inherited_supplements:
        inherited_supplements.append(supplement)
    return user_text.strip() + "".join(
        f"\n补充要求：{value}" for value in inherited_supplements if value
    )


def _is_attachment_workflow_confirmation(text: str) -> bool:
    return (
        "请确认" in text
        and "确认后" in text
        and bool(re.search(r"(?:附件|提取|文件)", text))
        and bool(re.search(r"(?:生成|pptx?|演示文稿|幻灯片)", text, re.IGNORECASE))
    )


def _is_missing_attachment_reply(item: Any) -> bool:
    return (
        isinstance(item, Mapping)
        and item.get("role") == "assistant"
        and isinstance(item.get("content"), str)
        and "请先上传一个需要读取的附件" in str(item["content"])
    )


def _is_failed_runtime_reply(item: Any) -> bool:
    if not isinstance(item, Mapping) or item.get("role") != "assistant":
        return False
    content = item.get("content")
    if not isinstance(content, str):
        return False
    return bool(
        re.search(
            r"<!--ai-(?:agent-run|generation-job):[^>]+\|(?:failed|timed_out)(?:\||-->)",
            content,
        )
    )


def _is_artifact_workflow_followup(message: str) -> bool:
    normalized = re.sub(r"[\s，。！？!?.]", "", message)
    if not normalized or len(normalized) > 200:
        return False
    if re.search(r"(?:取消|停止|不用|不做|算了|换个|另外|无关)", normalized):
        return False
    if normalized in {
        "确认",
        "好的",
        "好",
        "开始",
        "继续",
        "可以",
        "重试",
        "再试",
        "重新执行",
        "重新开始",
    }:
        return True
    return bool(
        re.search(
            r"(?:表格|逐条|合并|简洁|图标|分组|分类|排序|每页|中文|英文|动画|版式|样式|横版|竖版)",
            normalized,
        )
    )


def _document_ids(message: str) -> list[str]:
    return [
        value.strip()
        for value in re.findall(r"<!--ai-document:([^|>]+)(?:\|[^>]*)?-->", message)
        if value.strip()
    ]


def _artifact_types(message: str) -> list[str]:
    visible = re.sub(r"<!--ai-document:[^>]+-->", "", message)
    lowered = visible.casefold()
    result: list[str] = []
    patterns = (
        ("pptx", r"(?:(?<![a-z0-9])pptx?(?![a-z0-9])|演示文稿|幻灯片)"),
        ("docx", r"(?:(?<![a-z0-9])docx?(?![a-z0-9])|word\s*文档)"),
        ("xlsx", r"(?:(?<![a-z0-9])xlsx?(?![a-z0-9])|excel|电子表格)"),
        ("pdf", r"(?<![a-z0-9])pdf(?![a-z0-9])"),
        ("markdown", r"(?:(?<![a-z0-9])markdown(?![a-z0-9])|(?<![a-z0-9])md(?![a-z0-9]))"),
    )
    for artifact_type, pattern in patterns:
        if re.search(pattern, lowered):
            result.append(artifact_type)
    return result


def validate_artifact_observation(
    data: Any,
    task_contract: Mapping[str, Any],
) -> dict[str, Any]:
    report: dict[str, Any] = {
        "policy_version": ARTIFACT_QUALITY_POLICY_VERSION,
        "applicable": False,
        "passed": True,
        "violations": [],
    }
    if not artifact_observation_applicable(data):
        return report
    report["applicable"] = True
    skill_name = str(data.get("skill_name") or "")
    expected_skill_type = _ARTIFACT_SKILL_TYPES.get(skill_name, "")
    files = data.get("files")
    files = files if isinstance(files, list) else []
    produced_types: list[str] = []
    output = data.get("output")
    quality = output.get("quality_report") if isinstance(output, Mapping) else None
    violations: list[dict[str, Any]] = []

    if not files:
        violations.append({"code": "artifact_file_missing", "message": "生成文件不存在"})
    for item in files:
        name = item.get("name") if isinstance(item, Mapping) else None
        if not isinstance(name, str) or not name.strip() or "/" in name or "\\" in name:
            violations.append(
                {"code": "artifact_file_metadata_invalid", "message": "生成文件名无效"}
            )
            continue
        artifact_type = _ARTIFACT_SUFFIX_TYPES.get(PurePath(name).suffix.casefold(), "")
        if not artifact_type:
            violations.append(
                {
                    "code": "artifact_type_unknown",
                    "message": "无法确认生成文件类型",
                    "filename": name,
                }
            )
            continue
        produced_types.append(artifact_type)

    if expected_skill_type and produced_types and expected_skill_type not in produced_types:
        violations.append(
            {
                "code": "artifact_type_mismatch",
                "message": "生成文件类型与执行工具不一致",
                "expected": [expected_skill_type],
                "produced": produced_types,
            }
        )
    requested_types = [
        str(value)
        for value in task_contract.get("artifact_types", [])
        if isinstance(value, str) and value
    ]
    if requested_types and produced_types and not set(requested_types).intersection(produced_types):
        violations.append(
            {
                "code": "artifact_type_mismatch",
                "message": "生成文件类型与用户要求不一致",
                "expected": requested_types,
                "produced": produced_types,
            }
        )

    if isinstance(output, Mapping) and output.get("source_overwritten") is True:
        violations.append({"code": "source_overwritten", "message": "生成过程覆盖了来源文件"})

    if skill_name == "office.pptx_generate" and not isinstance(quality, Mapping):
        violations.append(
            {"code": "artifact_quality_report_missing", "message": "PPTX 缺少制品质量报告"}
        )
    elif isinstance(quality, Mapping):
        raw_violations = quality.get("violations")
        if isinstance(raw_violations, list):
            violations.extend(
                dict(item) if isinstance(item, Mapping) else {"code": str(item)}
                for item in raw_violations
            )
        if quality.get("passed") is not True and not violations:
            violations.append({"code": "artifact_quality_failed", "message": "PPTX 未通过质量门禁"})

    if skill_name == "office.pptx_generate" and isinstance(output, Mapping):
        violations.extend(_presentation_output_violations(output, task_contract))

    if isinstance(output, Mapping):
        coverage = output.get("source_coverage")
        if task_contract.get("exhaustive") is True:
            if not isinstance(coverage, Mapping):
                violations.append(
                    {"code": "source_coverage_missing", "message": "完整性任务缺少来源覆盖率"}
                )
            elif coverage.get("truncated") is True or coverage.get("coverage_ratio") != 1:
                violations.append(
                    {
                        "code": "source_coverage_incomplete",
                        "message": "来源分片尚未全部处理",
                        "coverage_ratio": coverage.get("coverage_ratio"),
                    }
                )

    report["skill_name"] = skill_name
    report["expected_artifact_types"] = requested_types or (
        [expected_skill_type] if expected_skill_type else []
    )
    report["produced_artifact_types"] = list(dict.fromkeys(produced_types))
    report["violations"] = _deduplicate_violations(violations)
    report["passed"] = not report["violations"]
    return report


def _presentation_output_violations(
    output: Mapping[str, Any],
    task_contract: Mapping[str, Any],
) -> list[dict[str, Any]]:
    requested_fields = _requested_fields(task_contract)
    if not requested_fields:
        return []
    outline = output.get("outline")
    table_slides: list[Mapping[str, Any]] = []
    if isinstance(outline, list):
        for slide in outline:
            if not isinstance(slide, Mapping):
                continue
            table = slide.get("table")
            if isinstance(table, Mapping):
                table_slides.append(table)
    if not table_slides:
        return [
            {
                "code": "structured_table_missing",
                "message": "PPT 未包含用户要求的结构化记录表格",
            }
        ]
    columns = table_slides[0].get("columns")
    violations = _requested_field_violations(columns, requested_fields)
    row_count = 0
    for table in table_slides:
        value = table.get("row_count")
        if isinstance(value, int) and not isinstance(value, bool):
            row_count += max(0, value)
    if row_count < 1:
        violations.append({"code": "structured_rows_missing", "message": "PPT 表格没有任何记录"})
    quality = output.get("quality_report")
    reported_count = quality.get("table_row_count") if isinstance(quality, Mapping) else None
    if (
        isinstance(reported_count, int)
        and not isinstance(reported_count, bool)
        and reported_count != row_count
    ):
        violations.append(
            {
                "code": "table_row_count_mismatch",
                "message": "PPT 质量报告与实际表格记录数不一致",
                "reported": reported_count,
                "observed": row_count,
            }
        )
    if task_contract.get("exhaustive") is True and any(
        not isinstance(table.get("source_locators"), list) or not table["source_locators"]
        for table in table_slides
    ):
        violations.append(
            {
                "code": "source_locator_missing",
                "message": "完整性任务的 PPT 表格缺少来源位置",
            }
        )
    return _deduplicate_violations(violations)


def _requested_fields(task_contract: Mapping[str, Any]) -> list[str]:
    values = task_contract.get("requested_fields")
    if not isinstance(values, list):
        return []
    return [str(value).strip() for value in values if str(value).strip()]


def _requested_field_violations(
    columns: Any,
    requested_fields: list[str],
) -> list[dict[str, Any]]:
    if not isinstance(columns, list):
        return [{"code": "table_columns_missing", "message": "结构化表格缺少表头"}]
    header_text = " ".join(str(value) for value in columns).casefold()
    violations: list[dict[str, Any]] = []
    for field in requested_fields:
        aliases = _REQUESTED_FIELD_ALIASES.get(field, (field,))
        if not any(alias in header_text for alias in aliases):
            violations.append(
                {
                    "code": "requested_field_missing",
                    "field": field,
                    "message": f"缺少字段：{field}",
                }
            )
    return violations


def _requested_field_column(columns: list[Any], field: str) -> int:
    aliases = _REQUESTED_FIELD_ALIASES.get(field, (field,))
    for index, value in enumerate(columns):
        header = str(value).casefold()
        if any(alias in header for alias in aliases):
            return index
    return -1


def _canonical_record_key(value: Any) -> str:
    return re.sub(r"[^A-Z0-9]", "", str(value).upper())


def artifact_observation_applicable(data: Any) -> bool:
    if not isinstance(data, Mapping) or data.get("status") != "succeeded":
        return False
    if str(data.get("skill_name") or "") in _ARTIFACT_SKILL_TYPES:
        return True
    files = data.get("files")
    return isinstance(files, list) and bool(files)


def _deduplicate_violations(items: list[dict[str, Any]]) -> list[dict[str, Any]]:
    result: list[dict[str, Any]] = []
    seen: set[str] = set()
    for item in items:
        code = str(item.get("code") or "artifact_quality_failed")
        if code in seen:
            continue
        seen.add(code)
        copied = dict(item)
        copied["code"] = code
        result.append(copied)
    return result
