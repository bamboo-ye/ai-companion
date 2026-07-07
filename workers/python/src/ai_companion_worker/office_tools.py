from __future__ import annotations

import base64
import csv
import io
import json
import math
import re
import sys
import zipfile
from collections import Counter
from pathlib import PurePath
from statistics import fmean
from typing import Any

from docx import Document
from openpyxl import load_workbook  # type: ignore[import-untyped]
from pptx import Presentation
from pptx.dml.color import RGBColor
from pptx.util import Inches, Pt

MAX_SOURCE_BYTES = 700 * 1024
MAX_ARCHIVE_BYTES = 25 * 1024 * 1024
MAX_ROWS = 10_000
MAX_COLUMNS = 100


def execute(operation: str, payload: dict[str, Any]) -> dict[str, Any]:
    if operation == "docx_edit":
        return _edit_docx(payload)
    if operation == "pptx_generate":
        return _generate_pptx(payload)
    if operation == "pptx_outline":
        result = _generate_pptx(payload)
        result["files"] = []
        return result
    if operation == "tabular_profile":
        return _profile_tabular(payload)
    raise ValueError("unsupported_operation")


def _edit_docx(payload: dict[str, Any]) -> dict[str, Any]:
    source_name, source = _source_file(payload, {".docx"})
    _validate_ooxml(source, reject_macros=True)
    append_text = _required_text(payload, "append_text", 20_000)
    document = Document(io.BytesIO(source))
    paragraphs = [line.strip() for line in append_text.splitlines() if line.strip()]
    if not paragraphs:
        raise ValueError("append_text_is_empty")
    for paragraph in paragraphs:
        document.add_paragraph(paragraph)
    output = io.BytesIO()
    document.save(output)
    data = output.getvalue()
    name = _output_name(payload.get("output_filename"), source_name, "-edited.docx")
    return {
        "output": {
            "source_filename": source_name,
            "output_filename": name,
            "change_summary": f"在文档副本末尾新增 {len(paragraphs)} 个段落",
            "appended_paragraphs": len(paragraphs),
            "source_overwritten": False,
        },
        "files": [_file(name, "application/vnd.openxmlformats-officedocument.wordprocessingml.document", data)],
    }


def _generate_pptx(payload: dict[str, Any]) -> dict[str, Any]:
    title = _required_text(payload, "title", 200)
    audience = _required_text(payload, "audience", 200)
    style = _required_text(payload, "style", 100)
    brief = _required_text(payload, "brief", 10_000)
    slide_count = payload.get("slide_count")
    if not isinstance(slide_count, int) or isinstance(slide_count, bool) or not 3 <= slide_count <= 20:
        raise ValueError("slide_count_must_be_between_3_and_20")
    name = _safe_filename(str(payload.get("filename") or title), ".pptx")
    sections = [part.strip(" -•\t") for part in re.split(r"[\r\n]+", brief) if part.strip()]
    if not sections:
        sections = [brief]

    presentation = Presentation()
    presentation.slide_width = Inches(13.333)
    presentation.slide_height = Inches(7.5)
    outline: list[dict[str, Any]] = []
    title_slide = presentation.slides.add_slide(presentation.slide_layouts[0])
    title_slide.shapes.title.text = title
    subtitle = title_slide.placeholders[1]
    subtitle.text = f"面向：{audience}\n风格：{style}"
    _style_slide(title_slide, title_color=RGBColor(43, 63, 117))
    outline.append({"page": 1, "title": title, "bullets": [f"面向：{audience}", f"风格：{style}"]})

    for page in range(2, slide_count + 1):
        is_last = page == slide_count
        heading = "总结与下一步" if is_last else sections[(page - 2) % len(sections)][:80]
        bullets = (
            ["回顾核心信息", "确认关键决策", "明确后续行动"]
            if is_last
            else _slide_bullets(sections, page - 2, brief)
        )
        slide = presentation.slides.add_slide(presentation.slide_layouts[1])
        slide.shapes.title.text = heading
        frame = slide.placeholders[1].text_frame
        frame.clear()
        for index, bullet in enumerate(bullets):
            paragraph = frame.paragraphs[0] if index == 0 else frame.add_paragraph()
            paragraph.text = bullet
            paragraph.level = 0
            paragraph.font.size = Pt(24)
        _style_slide(slide, title_color=RGBColor(72, 104, 183))
        outline.append({"page": page, "title": heading, "bullets": bullets})

    output = io.BytesIO()
    presentation.save(output)
    return {
        "output": {
            "title": title,
            "audience": audience,
            "style": style,
            "slide_count": slide_count,
            "outline": outline,
            "source_overwritten": False,
        },
        "files": [_file(name, "application/vnd.openxmlformats-officedocument.presentationml.presentation", output.getvalue())],
    }


def _profile_tabular(payload: dict[str, Any]) -> dict[str, Any]:
    source_name, source = _source_file(payload, {".csv", ".xlsx"})
    suffix = PurePath(source_name).suffix.lower()
    sheet_name = "CSV"
    if suffix == ".csv":
        rows = _read_csv(source)
    else:
        _validate_ooxml(source, reject_macros=True)
        rows, sheet_name = _read_xlsx(source)
    if not rows:
        raise ValueError("tabular_file_is_empty")
    headers = _headers(rows[0])
    body = [list(row[: len(headers)]) + [None] * max(0, len(headers) - len(row)) for row in rows[1:]]
    body = [row[: len(headers)] for row in body]
    profiles = [_column_profile(header, [row[index] for row in body]) for index, header in enumerate(headers)]
    duplicate_rows = len(body) - len({_stable_row(row) for row in body})
    report = {
        "analysis_version": "office-tools-tabular-v1",
        "source_filename": source_name,
        "sheet_name": sheet_name,
        "row_count": len(body),
        "column_count": len(headers),
        "duplicate_rows": duplicate_rows,
        "truncated": len(body) >= MAX_ROWS,
        "columns": profiles,
        "source_overwritten": False,
    }
    report_name = _safe_filename(PurePath(source_name).stem + "-analysis", ".json")
    report_data = json.dumps(report, ensure_ascii=False, indent=2, default=str).encode("utf-8")
    return {
        "output": report,
        "files": [_file(report_name, "application/json; charset=utf-8", report_data)],
    }


def _read_csv(data: bytes) -> list[list[Any]]:
    try:
        text = data.decode("utf-8-sig")
    except UnicodeDecodeError as exc:
        raise ValueError("csv_must_be_utf8") from exc
    sample = text[:8192]
    try:
        dialect = csv.Sniffer().sniff(sample, delimiters=",;\t|")
    except csv.Error:
        dialect = csv.excel
    reader = csv.reader(io.StringIO(text), dialect)
    rows: list[list[Any]] = []
    for index, row in enumerate(reader):
        if index > MAX_ROWS:
            break
        if len(row) > MAX_COLUMNS:
            raise ValueError("too_many_columns")
        rows.append(row)
    return rows


def _read_xlsx(data: bytes) -> tuple[list[list[Any]], str]:
    workbook = load_workbook(io.BytesIO(data), read_only=True, data_only=True, keep_links=False)
    try:
        sheet = workbook.active
        rows: list[list[Any]] = []
        for index, row in enumerate(sheet.iter_rows(values_only=True)):
            if index > MAX_ROWS:
                break
            values = list(row)
            if len(values) > MAX_COLUMNS:
                values = values[:MAX_COLUMNS]
            rows.append(values)
        return rows, sheet.title
    finally:
        workbook.close()


def _column_profile(name: str, values: list[Any]) -> dict[str, Any]:
    missing = sum(value is None or str(value).strip() == "" for value in values)
    present = [value for value in values if value is not None and str(value).strip() != ""]
    numbers = [float(value) for value in present if _is_number(value)]
    inferred = "empty"
    if present:
        inferred = "number" if len(numbers) == len(present) else "boolean" if all(isinstance(value, bool) for value in present) else "text"
    profile: dict[str, Any] = {
        "name": name,
        "inferred_type": inferred,
        "missing_count": missing,
        "unique_count": len({_stable_value(value) for value in present}),
    }
    if numbers:
        profile["numeric"] = {"min": min(numbers), "max": max(numbers), "mean": fmean(numbers)}
    return profile


def _headers(row: list[Any]) -> list[str]:
    if len(row) > MAX_COLUMNS:
        raise ValueError("too_many_columns")
    result: list[str] = []
    counts: Counter[str] = Counter()
    for index, value in enumerate(row, start=1):
        base = str(value).strip() if value is not None else ""
        base = base or f"column_{index}"
        counts[base] += 1
        result.append(base if counts[base] == 1 else f"{base}_{counts[base]}")
    return result


def _source_file(payload: dict[str, Any], suffixes: set[str]) -> tuple[str, bytes]:
    name = str(payload.get("source_filename") or "").strip()
    if PurePath(name).name != name or PurePath(name).suffix.lower() not in suffixes:
        raise ValueError("unsupported_source_filename")
    encoded = payload.get("source_base64")
    if not isinstance(encoded, str) or not encoded:
        raise ValueError("source_base64_is_required")
    try:
        data = base64.b64decode(encoded, validate=True)
    except ValueError as exc:
        raise ValueError("invalid_source_base64") from exc
    if not data or len(data) > MAX_SOURCE_BYTES:
        raise ValueError("source_file_size_is_invalid")
    return name, data


def _validate_ooxml(data: bytes, reject_macros: bool) -> None:
    try:
        with zipfile.ZipFile(io.BytesIO(data)) as archive:
            total = sum(item.file_size for item in archive.infolist())
            names = {item.filename.lower() for item in archive.infolist()}
            if total > MAX_ARCHIVE_BYTES:
                raise ValueError("expanded_archive_is_too_large")
            if reject_macros and any(name.endswith("vbaproject.bin") for name in names):
                raise ValueError("macro_enabled_files_are_not_supported")
    except zipfile.BadZipFile as exc:
        raise ValueError("invalid_ooxml_file") from exc


def _required_text(payload: dict[str, Any], field: str, maximum: int) -> str:
    value = payload.get(field)
    if not isinstance(value, str) or not value.strip():
        raise ValueError(f"{field}_is_required")
    value = value.strip()
    if len(value) > maximum:
        raise ValueError(f"{field}_is_too_long")
    return value


def _output_name(value: Any, source_name: str, fallback_suffix: str) -> str:
    if isinstance(value, str) and value.strip():
        return _safe_filename(value, ".docx")
    return _safe_filename(PurePath(source_name).stem + fallback_suffix, ".docx")


def _safe_filename(value: str, suffix: str) -> str:
    name = PurePath(value).name.strip()
    if name != value.strip():
        raise ValueError("invalid_output_filename")
    if name.lower().endswith(suffix):
        name = name[: -len(suffix)]
    name = re.sub(r"[^\w\-.\u4e00-\u9fff]+", "-", name, flags=re.UNICODE).strip(".-")
    if not name:
        name = "output"
    return name[: 240 - len(suffix)] + suffix


def _slide_bullets(sections: list[str], offset: int, brief: str) -> list[str]:
    primary = sections[offset % len(sections)]
    candidates = [part.strip(" -•\t") for part in re.split(r"[。；;]", primary) if part.strip()]
    if len(candidates) < 2:
        candidates.extend([part for part in sections if part != primary])
    if len(candidates) < 2:
        candidates.append(brief[:120])
    return [value[:120] for value in candidates[:4]]


def _style_slide(slide: Any, title_color: RGBColor) -> None:
    background = slide.background.fill
    background.solid()
    background.fore_color.rgb = RGBColor(247, 249, 255)
    if slide.shapes.title is not None:
        for paragraph in slide.shapes.title.text_frame.paragraphs:
            paragraph.font.bold = True
            paragraph.font.color.rgb = title_color


def _file(name: str, media_type: str, data: bytes) -> dict[str, str]:
    return {"name": name, "media_type": media_type, "data_base64": base64.b64encode(data).decode("ascii")}


def _is_number(value: Any) -> bool:
    if isinstance(value, bool):
        return False
    try:
        number = float(value)
    except (TypeError, ValueError):
        return False
    return math.isfinite(number)


def _stable_value(value: Any) -> str:
    return json.dumps(value, ensure_ascii=False, sort_keys=True, default=str)


def _stable_row(row: list[Any]) -> str:
    return json.dumps(row, ensure_ascii=False, sort_keys=True, default=str)


def main() -> None:
    try:
        request = json.load(sys.stdin)
        operation = request.get("operation")
        payload = request.get("input")
        if not isinstance(operation, str) or not isinstance(payload, dict):
            raise ValueError("invalid_request")
        result = execute(operation, payload)
    except Exception as exc:
        print(json.dumps({"error": str(exc)}, ensure_ascii=False), file=sys.stderr)
        raise SystemExit(2) from exc
    print(json.dumps(result, ensure_ascii=False, separators=(",", ":")))


if __name__ == "__main__":
    main()
