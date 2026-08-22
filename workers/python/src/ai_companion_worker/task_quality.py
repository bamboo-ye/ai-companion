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


def compile_task_contract(message: str, module: str) -> dict[str, Any]:
    normalized = message.strip()
    visible = re.sub(r"<!--ai-document:[^>]+-->", "", normalized)
    lowered = visible.casefold()
    artifact_types: list[str] = []
    if re.search(r"(?:(?<![a-z0-9])pptx?(?![a-z0-9])|演示文稿|幻灯片)", lowered):
        artifact_types.append("pptx")
    if re.search(r"(?:(?<![a-z0-9])docx?(?![a-z0-9])|word\s*文档)", lowered):
        artifact_types.append("docx")
    if re.search(r"(?:(?<![a-z0-9])xlsx?(?![a-z0-9])|excel|电子表格)", lowered):
        artifact_types.append("xlsx")
    if re.search(r"(?<![a-z0-9])pdf(?![a-z0-9])", lowered):
        artifact_types.append("pdf")
    if re.search(r"(?:(?<![a-z0-9])markdown(?![a-z0-9])|(?<![a-z0-9])md(?![a-z0-9]))", lowered):
        artifact_types.append("markdown")

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
        "source_required": "<!--ai-document:" in normalized,
        "exhaustive": exhaustive,
        "requested_fields": requested_fields,
        "output_language": "zh-CN" if re.search(r"(?:中文|汉语|chinese)", lowered) else "",
        "hard_requirements": hard_requirements,
        "completion_policy": "all_hard_requirements_pass",
    }


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
