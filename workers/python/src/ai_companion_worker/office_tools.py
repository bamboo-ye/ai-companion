from __future__ import annotations

import base64
import csv
import io
import json
import math
import os
import re
import sys
import time
import urllib.error
import urllib.request
import zipfile
from collections import Counter
from decimal import ROUND_CEILING, Decimal, InvalidOperation
from pathlib import PurePath
from statistics import fmean
from typing import Any

from docx import Document
from openpyxl import load_workbook  # type: ignore[import-untyped]
from pptx import Presentation
from pptx.dml.color import RGBColor
from pptx.util import Inches, Pt
from xml.sax.saxutils import escape

from ai_companion_worker.document_parser import ParseResult, count_tokens, parse_document

MAX_SOURCE_BYTES = 700 * 1024
MAX_PDF_SOURCE_BYTES = 8 * 1024 * 1024
MAX_DOCUMENT_CONTEXT_TOKENS = 4_000
MAX_PDF_TRANSLATION_CHARS = 40_000
MAX_TRANSLATION_COST_MICROS = 60_000
MAX_MODEL_RESPONSE_BYTES = 4 << 20
MAX_ARCHIVE_BYTES = 25 * 1024 * 1024
MAX_ROWS = 10_000
MAX_COLUMNS = 100


class ModelBackedOperationError(ValueError):
    """A post-dispatch failure that must retain the provider usage record."""

    def __init__(self, code: str, model_usage: dict[str, Any]) -> None:
        super().__init__(code)
        self.code = code
        self.model_usage = model_usage


def execute(operation: str, payload: dict[str, Any]) -> dict[str, Any]:
    if operation == "docx_edit":
        return _edit_docx(payload)
    if operation == "pptx_generate":
        return _generate_pptx(payload, include_file=True)
    if operation == "pptx_outline":
        return _generate_pptx(payload, include_file=False)
    if operation == "document_extract":
        return _extract_document(payload)
    if operation == "tabular_profile":
        return _profile_tabular(payload)
    if operation == "pdf_translate":
        return _translate_pdf(payload)
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
        "files": [
            _file(
                name,
                "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
                data,
            )
        ],
    }


def _generate_pptx(payload: dict[str, Any], *, include_file: bool) -> dict[str, Any]:
    title = _required_text(payload, "title", 200)
    audience = _required_text(payload, "audience", 200)
    style = _required_text(payload, "style", 100)
    brief = _required_text(payload, "brief", 10_000)
    slide_count = payload.get("slide_count")
    if (
        not isinstance(slide_count, int)
        or isinstance(slide_count, bool)
        or not 3 <= slide_count <= 20
    ):
        raise ValueError("slide_count_must_be_between_3_and_20")
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
    _style_slide(
        title_slide,
        title_color=RGBColor(43, 63, 117),  # type: ignore[no-untyped-call]
    )
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
        _style_slide(
            slide,
            title_color=RGBColor(72, 104, 183),  # type: ignore[no-untyped-call]
        )
        outline.append({"page": page, "title": heading, "bullets": bullets})

    files: list[dict[str, Any]] = []
    if include_file:
        output = io.BytesIO()
        presentation.save(output)
        files.append(
            _file(
                _pptx_output_name(payload.get("filename"), title),
                "application/vnd.openxmlformats-officedocument.presentationml.presentation",
                output.getvalue(),
            )
        )
    return {
        "output": {
            "title": title,
            "audience": audience,
            "style": style,
            "slide_count": slide_count,
            "outline": outline,
            "source_overwritten": False,
        },
        "files": files,
    }


def _extract_document(payload: dict[str, Any]) -> dict[str, Any]:
    source_name, source = _source_file(
        payload,
        {".pdf", ".txt", ".md"},
        MAX_PDF_SOURCE_BYTES,
    )
    media_type = _required_text(payload, "media_type", 200)
    try:
        parsed = parse_document(source, media_type, max_pages=100)
    except UnicodeDecodeError as exc:
        raise ValueError("text_document_must_be_utf8") from exc
    except ValueError as exc:
        code = str(exc)
        if code == "too_many_pages":
            raise ValueError("pdf_has_too_many_pages") from exc
        if code == "no_extractable_text":
            raise ValueError("document_has_no_extractable_text") from exc
        raise
    extracted, selected_chunks, context_tokens = _document_context(
        parsed,
        MAX_DOCUMENT_CONTEXT_TOKENS,
    )
    original_character_count = sum(len(page.text) for page in parsed.pages)
    low_quality_pages = [page.page_no for page in parsed.pages if page.quality < 0.5]
    return {
        "output": {
            "source_filename": source_name,
            "media_type": media_type,
            "format": "markdown",
            "parser_version": parsed.parser_version,
            "page_count": len(parsed.pages),
            "character_count": original_character_count,
            "token_count": context_tokens,
            "selected_chunk_count": selected_chunks,
            "total_chunk_count": len(parsed.chunks),
            "text": extracted,
            "truncated": selected_chunks < len(parsed.chunks),
            "low_quality_pages": low_quality_pages,
            "source_overwritten": False,
        },
        "files": [],
    }


def _document_context(parsed: ParseResult, token_budget: int) -> tuple[str, int, int]:
    chunks = parsed.chunks
    if not chunks:
        raise ValueError("document_has_no_extractable_text")
    content_budget = max(256, int(token_budget * 0.9))
    total_tokens = sum(chunk.token_count for chunk in chunks)
    if total_tokens <= content_budget:
        selected = list(chunks)
    else:
        slots = max(2, min(len(chunks), content_budget // 600))
        indices = [0, len(chunks) - 1]
        indices.extend(
            round(index * (len(chunks) - 1) / (slots - 1))
            for index in range(1, slots - 1)
        )
        selected = []
        selected_tokens = 0
        for index in dict.fromkeys(indices):
            chunk = chunks[index]
            if selected and selected_tokens + chunk.token_count > content_budget:
                continue
            selected.append(chunk)
            selected_tokens += chunk.token_count
        for chunk in chunks:
            if chunk in selected or selected_tokens + chunk.token_count > content_budget:
                continue
            selected.append(chunk)
            selected_tokens += chunk.token_count
        selected.sort(key=lambda chunk: chunk.ordinal)
    blocks: list[str] = []
    for chunk in selected:
        marker = f"[[PAGE {chunk.page_start}]]"
        if chunk.page_end > chunk.page_start:
            marker = f"[[PAGES {chunk.page_start}-{chunk.page_end}]]"
        section = f"<!-- section: {chunk.section_path} -->\n" if chunk.section_path else ""
        blocks.append(f"{marker}\n{section}{chunk.content}".strip())
    context = "\n\n".join(blocks)
    while len(selected) > 1 and count_tokens(context) > token_budget:
        selected.pop()
        blocks.pop()
        context = "\n\n".join(blocks)
    return context, len(selected), count_tokens(context)


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
    body = [
        list(row[: len(headers)]) + [None] * max(0, len(headers) - len(row)) for row in rows[1:]
    ]
    body = [row[: len(headers)] for row in body]
    profiles = [
        _column_profile(header, [row[index] for row in body])
        for index, header in enumerate(headers)
    ]
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


def _translate_pdf(payload: dict[str, Any]) -> dict[str, Any]:
    source_name, source = _source_file(payload, {".pdf"}, MAX_PDF_SOURCE_BYTES)
    target_language = _required_text(payload, "target_language", 80)
    try:
        parsed = parse_document(source, "application/pdf", max_pages=100)
    except ValueError as exc:
        code = str(exc)
        if code == "too_many_pages":
            raise ValueError("pdf_has_too_many_pages") from exc
        if code == "no_extractable_text":
            raise ValueError("pdf_has_no_extractable_text") from exc
        raise
    source_text = "\n\n".join(
        f"[[PAGE {page.page_no}]]\n{page.text}" for page in parsed.pages if page.text.strip()
    )
    if len(source_text) > MAX_PDF_TRANSLATION_CHARS:
        raise ValueError("pdf_text_is_too_long_for_translation")
    output_name = _pdf_output_name(payload.get("output_filename"), source_name, target_language)
    translated, model_usage = _openrouter_translate(source_text, target_language)
    try:
        output = _render_translated_pdf(translated, target_language)
    except Exception as exc:
        raise ModelBackedOperationError("translation_render_failed", model_usage) from exc
    return {
        "output": {
            "source_filename": source_name,
            "output_filename": output_name,
            "target_language": target_language,
            "page_count": len(parsed.pages),
            "parser_version": parsed.parser_version,
            "source_overwritten": False,
            "model_usage": model_usage,
        },
        "files": [_file(output_name, "application/pdf", output)],
    }


def _openrouter_translate(text: str, target_language: str) -> tuple[str, dict[str, Any]]:
    provider = os.environ.get("MODEL_PROVIDER", "development").strip().lower()
    if provider != "openrouter":
        raise ValueError("translation_model_provider_must_be_openrouter")
    base_url = os.environ.get("MODEL_BASE_URL", "https://openrouter.ai/api/v1").rstrip("/")
    if base_url != "https://openrouter.ai/api/v1":
        raise ValueError("MODEL_BASE_URL_must_use_canonical_openrouter_endpoint")
    api_key = os.environ.get("MODEL_API_KEY", "").strip()
    model = os.environ.get(
        "MODEL_TRANSLATION_NAME", "openai/gpt-5-mini"
    ).strip()
    if not api_key or not model:
        raise ValueError("translation_model_is_not_configured")
    if _dynamic_model(model):
        raise ValueError("translation_model_must_use_a_concrete_slug")
    configured_max_tokens = _bounded_positive_int("MODEL_TRANSLATION_MAX_TOKENS", 8_000, 32_768)
    max_cost_micros = min(
        _bounded_positive_int(
            "MODEL_TRANSLATION_MAX_COST_MICROS",
            MAX_TRANSLATION_COST_MICROS,
            MAX_TRANSLATION_COST_MICROS,
        ),
        MAX_TRANSLATION_COST_MICROS,
    )
    prompt_price = _positive_decimal("MODEL_TRANSLATION_MAX_PROMPT_PRICE", "0.3")
    completion_price = _positive_decimal("MODEL_TRANSLATION_MAX_COMPLETION_PRICE", "2.5")
    if prompt_price > Decimal("0.3") or completion_price > Decimal("2.5"):
        raise ValueError("translation_model_price_ceiling_is_too_high")
    messages = [
        {
            "role": "system",
            "content": (
                f"Translate the supplied PDF text into {target_language}. "
                "Preserve headings, paragraphs, lists and [[PAGE n]] markers. "
                "Return only the translated text, without commentary."
            ),
        },
        {"role": "user", "content": text},
    ]
    prompt_bound = (
        len(json.dumps(messages, ensure_ascii=False, separators=(",", ":")).encode("utf-8")) + 128
    )
    reserved_prompt_cost = _decimal_ceil(Decimal(prompt_bound) * prompt_price)
    affordable_output_tokens = int(
        Decimal(max(0, max_cost_micros - reserved_prompt_cost)) / completion_price
    )
    max_tokens = min(configured_max_tokens, affordable_output_tokens)
    if max_tokens < 256:
        raise ValueError("translation_model_budget_exhausted")
    request_body = json.dumps(
        {
            "model": model,
            "messages": messages,
            "max_tokens": max_tokens,
            "reasoning": {"effort": "minimal", "exclude": True},
            "provider": {
                "data_collection": os.environ.get("MODEL_DATA_COLLECTION", "deny"),
                "zdr": _env_bool("MODEL_ZDR_REQUIRED", False),
                "sort": os.environ.get("MODEL_PROVIDER_SORT", "price"),
                "allow_fallbacks": _env_bool("MODEL_ALLOW_PROVIDER_FALLBACKS", True),
                "require_parameters": True,
                "max_price": {
                    "prompt": float(prompt_price),
                    "completion": float(completion_price),
                },
            },
        },
        ensure_ascii=False,
    ).encode("utf-8")
    request = urllib.request.Request(
        base_url + "/chat/completions",
        data=request_body,
        method="POST",
        headers={
            "Authorization": "Bearer " + api_key,
            "Content-Type": "application/json",
            "HTTP-Referer": os.environ.get("MODEL_HTTP_REFERER", "http://localhost:3000"),
            "X-OpenRouter-Title": os.environ.get("MODEL_APP_TITLE", "伴AI"),
        },
    )
    started_ns = time.perf_counter_ns()
    try:
        with urllib.request.urlopen(request, timeout=90) as response:
            raw = response.read(MAX_MODEL_RESPONSE_BYTES + 1)
    except urllib.error.HTTPError as exc:
        raise ValueError(f"translation_model_http_{exc.code}") from exc
    except Exception as exc:
        raise ValueError("translation_model_request_failed") from exc
    latency_ms = max(0, (time.perf_counter_ns() - started_ns) // 1_000_000)
    model_usage = {
        "provider": "openrouter",
        "config_version": os.environ.get(
            "MODEL_CONFIG_VERSION", "2026-08-bounded-fallback-v1"
        ),
        "requested_model": model,
        "returned_model": "",
        "upstream_provider": "",
        "prompt_tokens": 0,
        "completion_tokens": 0,
        "cost_micros": max_cost_micros,
        "cost_accounting": "reserved_upper_bound",
        "latency_ms": latency_ms,
        "max_cost_micros": max_cost_micros,
    }
    if len(raw) > MAX_MODEL_RESPONSE_BYTES:
        raise ModelBackedOperationError("translation_model_response_too_large", model_usage)
    try:
        payload = json.loads(raw)
    except (json.JSONDecodeError, UnicodeDecodeError) as exc:
        raise ModelBackedOperationError(
            "translation_model_returned_invalid_json", model_usage
        ) from exc
    if not isinstance(payload, dict):
        raise ModelBackedOperationError("translation_model_returned_invalid_json", model_usage)
    usage = payload.get("usage")
    usage = usage if isinstance(usage, dict) else {}
    returned_model = payload.get("model")
    upstream_provider = payload.get("provider")
    reported_cost_micros = _reported_cost_micros(usage.get("cost"))
    model_usage.update(
        {
            "returned_model": (returned_model if isinstance(returned_model, str) else ""),
            "upstream_provider": (upstream_provider if isinstance(upstream_provider, str) else ""),
            "prompt_tokens": _non_negative_int(usage.get("prompt_tokens")),
            "completion_tokens": _non_negative_int(usage.get("completion_tokens")),
            "cost_micros": (
                reported_cost_micros if reported_cost_micros is not None else max_cost_micros
            ),
            "cost_accounting": (
                "reported" if reported_cost_micros is not None else "reserved_upper_bound"
            ),
        }
    )
    if reported_cost_micros is None:
        raise ModelBackedOperationError("translation_model_usage_invalid", model_usage)
    try:
        finish_reason = payload["choices"][0].get("finish_reason")
    except (KeyError, IndexError, TypeError, AttributeError) as exc:
        raise ModelBackedOperationError("translation_model_response_invalid", model_usage) from exc
    if finish_reason != "stop":
        code = (
            "translation_model_truncated"
            if finish_reason == "length"
            else "translation_model_incomplete"
        )
        raise ModelBackedOperationError(code, model_usage)
    try:
        translated = _translated_content(payload)
    except ValueError as exc:
        code = str(exc)
        if not code.startswith("translation_model_"):
            code = "translation_model_response_invalid"
        raise ModelBackedOperationError(code, model_usage) from exc
    if _page_markers(translated) != _page_markers(text):
        raise ModelBackedOperationError("translation_page_markers_invalid", model_usage)
    return translated, model_usage


def _translated_content(payload: dict[str, Any]) -> str:
    try:
        content = payload["choices"][0]["message"]["content"]
    except (KeyError, IndexError, TypeError) as exc:
        raise ValueError("translation_model_response_invalid") from exc
    if isinstance(content, str):
        translated = content.strip()
    elif isinstance(content, list):
        translated = "\n".join(
            str(item.get("text", "")).strip()
            for item in content
            if isinstance(item, dict) and item.get("type") == "text"
        ).strip()
    else:
        translated = ""
    if not translated:
        raise ValueError("translation_model_returned_empty_text")
    return translated


def _page_markers(text: str) -> list[int]:
    return [
        int(value)
        for value in re.findall(
            r"^[ \t]*\[\[PAGE\s+(\d+)\]\][ \t]*\r?$",
            text,
            re.IGNORECASE | re.MULTILINE,
        )
    ]


def _render_translated_pdf(text: str, target_language: str) -> bytes:
    from reportlab.lib.enums import TA_LEFT  # type: ignore[import-untyped]
    from reportlab.lib.pagesizes import A4  # type: ignore[import-untyped]
    from reportlab.lib.styles import (  # type: ignore[import-untyped]
        ParagraphStyle,
        getSampleStyleSheet,
    )
    from reportlab.pdfbase import pdfmetrics  # type: ignore[import-untyped]
    from reportlab.pdfbase.cidfonts import UnicodeCIDFont  # type: ignore[import-untyped]
    from reportlab.platypus import (  # type: ignore[import-untyped]
        PageBreak,
        Paragraph,
        SimpleDocTemplate,
        Spacer,
    )

    output = io.BytesIO()
    pdfmetrics.registerFont(UnicodeCIDFont("STSong-Light"))
    styles = getSampleStyleSheet()
    body_style = ParagraphStyle(
        "TranslatedBody",
        parent=styles["BodyText"],
        fontName="STSong-Light",
        fontSize=10.5,
        leading=16,
        alignment=TA_LEFT,
        spaceAfter=8,
    )
    heading_style = ParagraphStyle(
        "TranslatedHeading",
        parent=body_style,
        fontSize=15,
        leading=20,
        spaceAfter=12,
    )
    story: list[Any] = [
        Paragraph(escape(f"PDF Translation · {target_language}"), heading_style),
        Spacer(1, 8),
    ]
    page_marker = re.compile(r"^\[\[PAGE\s+\d+\]\]$", re.IGNORECASE)
    for block in re.split(r"\n\s*\n", text):
        value = block.strip()
        if not value:
            continue
        if page_marker.match(value):
            if len(story) > 2:
                story.append(PageBreak())
            continue
        story.append(
            Paragraph("<br/>".join(escape(line) for line in value.splitlines()), body_style)
        )
    document = SimpleDocTemplate(
        output,
        pagesize=A4,
        rightMargin=42,
        leftMargin=42,
        topMargin=42,
        bottomMargin=42,
        title=f"PDF Translation - {target_language}",
    )
    document.build(story)
    return output.getvalue()


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
        inferred = (
            "number"
            if len(numbers) == len(present)
            else "boolean"
            if all(isinstance(value, bool) for value in present)
            else "text"
        )
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


def _source_file(
    payload: dict[str, Any], suffixes: set[str], maximum_bytes: int = MAX_SOURCE_BYTES
) -> tuple[str, bytes]:
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
    if not data or len(data) > maximum_bytes:
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


def _pdf_output_name(value: Any, source_name: str, target_language: str) -> str:
    if isinstance(value, str) and value.strip():
        return _safe_filename(value, ".pdf")
    language = re.sub(r"[^\w\u4e00-\u9fff]+", "-", target_language, flags=re.UNICODE).strip("-")
    language = language or "translated"
    return _safe_filename(PurePath(source_name).stem + f"-{language}", ".pdf")


def _pptx_output_name(value: Any, title: str) -> str:
    if isinstance(value, str) and value.strip():
        return _safe_filename(value, ".pptx")
    title_filename = re.sub(r"[/\\]+", "-", title)
    title_filename = re.sub(r"\s*-\s*", "-", title_filename)
    return _safe_filename(title_filename, ".pptx")


def _safe_filename(value: str, suffix: str) -> str:
    raw_name = value.strip()
    name = PurePath(raw_name).name.strip()
    if name != raw_name or "/" in raw_name or "\\" in raw_name:
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
    background.fore_color.rgb = RGBColor(247, 249, 255)  # type: ignore[no-untyped-call]
    if slide.shapes.title is not None:
        for paragraph in slide.shapes.title.text_frame.paragraphs:
            paragraph.font.bold = True
            paragraph.font.color.rgb = title_color


def _file(name: str, media_type: str, data: bytes) -> dict[str, str]:
    return {
        "name": name,
        "media_type": media_type,
        "data_base64": base64.b64encode(data).decode("ascii"),
    }


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


def _dynamic_model(model: str) -> bool:
    normalized = model.strip().lower()
    return (
        normalized in ("openrouter/free", "openrouter/auto")
        or normalized.startswith("~")
        or normalized.endswith((":free", ":nitro", ":floor"))
        or normalized.endswith("-latest")
    )


def _bounded_positive_int(name: str, fallback: int, maximum: int) -> int:
    raw = os.environ.get(name, str(fallback)).strip()
    try:
        value = int(raw)
    except ValueError as exc:
        raise ValueError(f"{name}_must_be_a_positive_integer") from exc
    if value <= 0 or value > maximum:
        raise ValueError(f"{name}_is_out_of_range")
    return value


def _positive_decimal(name: str, fallback: str) -> Decimal:
    try:
        value = Decimal(os.environ.get(name, fallback).strip())
    except InvalidOperation as exc:
        raise ValueError(f"{name}_must_be_positive") from exc
    if not value.is_finite() or value <= 0:
        raise ValueError(f"{name}_must_be_positive")
    return value


def _decimal_ceil(value: Decimal) -> int:
    return int(value.to_integral_value(rounding=ROUND_CEILING))


def _env_bool(name: str, fallback: bool) -> bool:
    raw = os.environ.get(name)
    if raw is None or not raw.strip():
        return fallback
    normalized = raw.strip().lower()
    if normalized in ("1", "true", "yes", "on"):
        return True
    if normalized in ("0", "false", "no", "off"):
        return False
    raise ValueError(f"{name}_must_be_true_or_false")


def _non_negative_int(value: Any) -> int:
    if isinstance(value, int) and not isinstance(value, bool) and value >= 0:
        return value
    return 0


def _reported_cost_micros(value: Any) -> int | None:
    try:
        cost = Decimal(str(value))
    except (InvalidOperation, ValueError):
        return None
    if not cost.is_finite() or cost < 0:
        return None
    return _decimal_ceil(cost * Decimal(1_000_000))


def main() -> None:
    try:
        request = json.load(sys.stdin)
        operation = request.get("operation")
        payload = request.get("input")
        if not isinstance(operation, str) or not isinstance(payload, dict):
            raise ValueError("invalid_request")
        result = execute(operation, payload)
    except ModelBackedOperationError as exc:
        result = {
            "output": {
                "model_usage": exc.model_usage,
                "operation_error": {"code": exc.code},
            },
            "files": [],
        }
    except Exception as exc:
        print(
            json.dumps(_tool_failure_for_exception(exc), ensure_ascii=False),
            file=sys.stderr,
        )
        raise SystemExit(2) from exc
    print(json.dumps(result, ensure_ascii=False, separators=(",", ":")))


def _tool_failure_for_exception(exc: Exception) -> dict[str, Any]:
    code = str(exc).strip() or "office_worker_failed"
    if code == "invalid_output_filename":
        return {
            "contract_version": "tool-failure-v1",
            "code": code,
            "category": "argument_validation",
            "phase": "pre_execution",
            "message": "输出文件名必须是文件名，不能包含目录分隔符。",
            "retry_same_input": False,
            "repairable": True,
            "side_effect_state": "none",
            "field_paths": ["/filename"],
            "allowed_repairs": [
                "remove_optional_filename",
                "filename.safe_basename",
            ],
            "safe_details": {"constraint": "output_basename"},
        }
    return {
        "contract_version": "tool-failure-v1",
        "code": code if len(code) <= 128 else "office_worker_failed",
        "category": "argument_validation" if isinstance(exc, ValueError) else "execution",
        "phase": "pre_execution" if isinstance(exc, ValueError) else "execution",
        "message": "Office 工具参数无效。" if isinstance(exc, ValueError) else "Office 工具执行失败。",
        "retry_same_input": False,
        "repairable": False,
        "side_effect_state": "none" if isinstance(exc, ValueError) else "unknown",
    }


if __name__ == "__main__":
    main()
