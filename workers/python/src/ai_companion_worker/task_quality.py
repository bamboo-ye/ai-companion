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
        re.search(r"(?:所有|全部|完整|逐一|每(?:一|门|项|条)|\ball\b|\bevery\b|\bcomplete\b)", lowered)
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
        pending = _confirmed_pending_artifact_request(message, history)
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


def _confirmed_pending_artifact_request(message: str, history: list[Any]) -> str:
    confirmation = re.sub(r"[\s，。！？!?.]", "", message)
    if confirmation not in {"确认", "好的", "好", "开始", "继续", "可以"}:
        return ""
    if len(history) < 2:
        return ""
    assistant = history[-1]
    user = history[-2]
    if not isinstance(assistant, Mapping) or not isinstance(user, Mapping):
        return ""
    assistant_text = assistant.get("content")
    user_text = user.get("content")
    if (
        assistant.get("role") != "assistant"
        or user.get("role") != "user"
        or not isinstance(assistant_text, str)
        or not isinstance(user_text, str)
        or "请确认" not in assistant_text
        or "确认后" not in assistant_text
        or not re.search(r"(?:附件|提取|文件)", assistant_text)
        or not re.search(r"(?:生成|pptx?|演示文稿|幻灯片)", assistant_text, re.IGNORECASE)
        or not _document_ids(user_text)
        or not _artifact_types(user_text)
    ):
        return ""
    return user_text.strip()


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
        if (
            not isinstance(name, str)
            or not name.strip()
            or "/" in name
            or "\\" in name
        ):
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
        violations.append(
            {"code": "source_overwritten", "message": "生成过程覆盖了来源文件"}
        )

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
            violations.append(
                {"code": "artifact_quality_failed", "message": "PPTX 未通过质量门禁"}
            )

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
