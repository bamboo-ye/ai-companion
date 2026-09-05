from __future__ import annotations

import argparse
import hashlib
import importlib.metadata
import importlib.util
import io
import json
import math
import os
import re
import sys
from dataclasses import asdict, dataclass
from functools import lru_cache
from typing import Any

from pypdf import PdfReader

from ai_companion_worker.pdf_utils import normalize_pdf_bytes
from ai_companion_worker.result_cache import load_json_result, store_json_result

PARSER_VERSION = "pypdf-6.14.2-markdown-v4"
TEXT_PARSER_VERSION = "text-markdown-v3"
SOURCE_IR_VERSION = "document-source-ir-v1"
DOCUMENT_PARSE_CACHE_VERSION = "document-parse-cache-v1"
MAX_CHUNK_TOKENS = 800
OVERLAP_TOKENS = 80
MAX_PAGES = 1000
MAX_PAGE_CHARS = 2_000_000
_MARKDOWN_HEADING = re.compile(r"^(#{1,6})\s+(.+)$")
_PLAIN_HEADING = re.compile(
    r"^(?:第[一二三四五六七八九十百0-9]+[章节部分]|\d+(?:\.\d+)*[、.\s])"
)


@dataclass(frozen=True)
class Page:
    page_no: int
    text: str
    quality: float
    content_hash: str


@dataclass(frozen=True)
class Chunk:
    ordinal: int
    page_start: int
    page_end: int
    section_path: str
    content: str
    token_count: int
    content_hash: str


@dataclass(frozen=True)
class ParseResult:
    parser_version: str
    pages: list[Page]
    chunks: list[Chunk]
    source_ir: dict[str, Any]


def parse_document(
    data: bytes,
    media_type: str,
    *,
    max_pages: int = MAX_PAGES,
) -> ParseResult:
    normalized_data = normalize_pdf_bytes(data) if media_type == "application/pdf" else data
    cache_key = _document_parse_cache_key(normalized_data, media_type, max_pages)
    cached = load_json_result("document-parses", cache_key)
    restored = _restore_parse_result(cached)
    if restored is not None:
        return restored
    if media_type == "application/pdf":
        parser_version, pages = _parse_pdf(normalized_data, max_pages=max_pages)
    elif media_type == "text/plain":
        parser_version = TEXT_PARSER_VERSION
        pages = [_page(1, _normalize_markdown(normalized_data.decode("utf-8-sig")))]
    else:
        raise ValueError("unsupported_media_type")
    if not any(page.text.strip() for page in pages):
        raise ValueError("no_extractable_text")
    chunks = _chunk_pages(pages)
    if not chunks:
        raise ValueError("no_extractable_text")
    result = ParseResult(parser_version, pages, chunks, _build_source_ir(pages, chunks))
    store_json_result("document-parses", cache_key, asdict(result))
    return result


def _document_parse_cache_key(data: bytes, media_type: str, max_pages: int) -> str:
    backend = os.environ.get("PDF_PARSER_BACKEND", "auto").strip().lower()
    docling_version = ""
    if backend in {"auto", "docling"} and _docling_available():
        try:
            docling_version = importlib.metadata.version("docling")
        except importlib.metadata.PackageNotFoundError:
            docling_version = ""
    identity = json.dumps(
        {
            "version": DOCUMENT_PARSE_CACHE_VERSION,
            "media_type": media_type,
            "max_pages": max_pages,
            "backend": backend,
            "pypdf_parser": PARSER_VERSION,
            "text_parser": TEXT_PARSER_VERSION,
            "source_ir": SOURCE_IR_VERSION,
            "docling": docling_version,
            "content_sha256": hashlib.sha256(data).hexdigest(),
        },
        ensure_ascii=False,
        sort_keys=True,
        separators=(",", ":"),
    )
    return hashlib.sha256(identity.encode("utf-8")).hexdigest()


def _restore_parse_result(value: Any) -> ParseResult | None:
    if not isinstance(value, dict):
        return None
    raw_pages = value.get("pages")
    raw_chunks = value.get("chunks")
    source_ir = value.get("source_ir")
    parser_version = value.get("parser_version")
    if (
        not isinstance(parser_version, str)
        or not parser_version
        or not isinstance(raw_pages, list)
        or not isinstance(raw_chunks, list)
        or not isinstance(source_ir, dict)
        or source_ir.get("version") != SOURCE_IR_VERSION
    ):
        return None
    try:
        pages = [
            Page(
                page_no=int(item["page_no"]),
                text=str(item["text"]),
                quality=float(item["quality"]),
                content_hash=str(item["content_hash"]),
            )
            for item in raw_pages
            if isinstance(item, dict)
        ]
        chunks = [
            Chunk(
                ordinal=int(item["ordinal"]),
                page_start=int(item["page_start"]),
                page_end=int(item["page_end"]),
                section_path=str(item["section_path"]),
                content=str(item["content"]),
                token_count=int(item["token_count"]),
                content_hash=str(item["content_hash"]),
            )
            for item in raw_chunks
            if isinstance(item, dict)
        ]
    except (KeyError, TypeError, ValueError):
        return None
    if len(pages) != len(raw_pages) or len(chunks) != len(raw_chunks) or not pages or not chunks:
        return None
    if any(page.page_no <= 0 or not page.content_hash for page in pages):
        return None
    if any(chunk.ordinal <= 0 or chunk.token_count <= 0 or not chunk.content_hash for chunk in chunks):
        return None
    return ParseResult(parser_version, pages, chunks, dict(source_ir))


def count_tokens(value: str) -> int:
    """Count prompt tokens with the configured tokenizer, with a conservative fallback."""
    encoding = _token_encoding()
    if encoding is not None:
        return len(encoding.encode(value))
    return _fallback_token_count(value)


def _parse_pdf(data: bytes, *, max_pages: int) -> tuple[str, list[Page]]:
    data = normalize_pdf_bytes(data)
    backend = os.environ.get("PDF_PARSER_BACKEND", "auto").strip().lower()
    if backend not in {"auto", "pypdf", "docling"}:
        raise ValueError("invalid_pdf_parser_backend")
    if backend == "docling":
        return _parse_pdf_docling(data, max_pages=max_pages)

    pages = _parse_pdf_pypdf(data, max_pages=max_pages)
    if backend == "auto" and _needs_structured_fallback(pages) and _docling_available():
        try:
            return _parse_pdf_docling(data, max_pages=max_pages)
        except Exception:
            # The deterministic fast path remains available if the optional parser fails.
            pass
    return PARSER_VERSION, pages


def _parse_pdf_pypdf(data: bytes, *, max_pages: int) -> list[Page]:
    reader = PdfReader(io.BytesIO(data), strict=False)
    if reader.is_encrypted:
        # Some publishers apply AES encryption while leaving the user password
        # empty.  Those files are readable in ordinary PDF viewers and should
        # remain processable here.  Files that require an actual password are
        # still rejected because the worker has no trusted password input.
        try:
            decrypted = reader.decrypt("")
        except Exception as exc:
            raise ValueError("encrypted_pdf") from exc
        if not decrypted:
            raise ValueError("encrypted_pdf")
    if len(reader.pages) > max_pages:
        raise ValueError("too_many_pages")
    raw_pages: list[str] = []
    for pdf_page in reader.pages:
        try:
            text = pdf_page.extract_text(
                extraction_mode="layout", layout_mode_space_vertically=False
            )
        except Exception:
            text = pdf_page.extract_text() or ""
        if len(text) > MAX_PAGE_CHARS:
            raise ValueError("page_text_too_large")
        raw_pages.append(text or "")
    raw_pages = _remove_repeated_margins(raw_pages)
    return [
        _page(page_no, _layout_to_markdown(text))
        for page_no, text in enumerate(raw_pages, start=1)
    ]


def _parse_pdf_docling(data: bytes, *, max_pages: int) -> tuple[str, list[Page]]:
    if not _docling_available():
        raise ValueError("docling_parser_is_not_installed")
    from docling.datamodel.base_models import DocumentStream  # type: ignore[import-not-found]
    from docling.document_converter import DocumentConverter  # type: ignore[import-not-found]

    source = DocumentStream(name="document.pdf", stream=io.BytesIO(data))
    result = DocumentConverter().convert(
        source,
        max_num_pages=max_pages,
        max_file_size=len(data),
    )
    document = result.document
    page_numbers = sorted(int(value) for value in document.pages)
    if not page_numbers:
        page_numbers = [1]
    pages = [
        _page(
            page_no,
            _normalize_markdown(
                document.export_to_markdown(
                    page_no=page_no,
                    image_placeholder="",
                    traverse_pictures=True,
                )
            ),
        )
        for page_no in page_numbers
    ]
    version = importlib.metadata.version("docling")
    return f"docling-{version}-markdown-v2", pages


def _page(page_no: int, text: str) -> Page:
    normalized = _normalize_markdown(text)
    visible_text = re.sub(r"\s+", "", normalized)
    visible = len(visible_text)
    if visible == 0:
        quality = 0.0
    elif visible < 10:
        quality = 0.2
    elif visible < 40:
        quality = 0.5
    else:
        quality = 1.0
    suspicious = sum(
        char == "\ufffd" or 0xE000 <= ord(char) <= 0xF8FF for char in visible_text
    )
    if visible and suspicious / visible >= 0.01:
        quality = min(quality, 0.3)
    return Page(page_no, normalized, quality, _hash(normalized))


def _normalize_markdown(text: str) -> str:
    text = text.replace("\r\n", "\n").replace("\r", "\n").replace("\x00", "")
    lines = [line.rstrip() for line in text.split("\n")]
    normalized = "\n".join(lines).strip()
    return re.sub(r"\n{3,}", "\n\n", normalized)


def _layout_to_markdown(text: str) -> str:
    normalized = _normalize_markdown(text)
    if not normalized:
        return ""
    lines = normalized.splitlines()
    output: list[str] = []
    index = 0
    while index < len(lines):
        line = lines[index]
        if not line.strip():
            output.append("")
            index += 1
            continue
        table_run: list[str] = []
        cursor = index
        while cursor < len(lines) and _looks_like_fixed_width_row(lines[cursor]):
            table_run.append(lines[cursor].rstrip())
            cursor += 1
        if len(table_run) >= 2:
            output.extend(["```text", *table_run, "```", ""])
            index = cursor
            continue
        value = line.strip()
        if _is_plain_heading(value):
            output.append("## " + value)
        else:
            output.append(line.rstrip())
        index += 1
    return _normalize_markdown("\n".join(output))


def _remove_repeated_margins(pages: list[str]) -> list[str]:
    if len(pages) < 3:
        return pages
    page_lines = [[line.rstrip() for line in page.splitlines()] for page in pages]
    first_keys: list[str] = []
    last_keys: list[str] = []
    for lines in page_lines:
        nonempty = [line.strip() for line in lines if line.strip()]
        if nonempty:
            first_keys.append(_margin_key(nonempty[0]))
            last_keys.append(_margin_key(nonempty[-1]))
    threshold = max(3, math.ceil(len(pages) * 0.6))
    repeated_first = {
        key for key in set(first_keys) if key and first_keys.count(key) >= threshold
    }
    repeated_last = {
        key for key in set(last_keys) if key and last_keys.count(key) >= threshold
    }
    cleaned: list[str] = []
    for lines in page_lines:
        nonempty_indices = [index for index, line in enumerate(lines) if line.strip()]
        if nonempty_indices:
            first = nonempty_indices[0]
            last = nonempty_indices[-1]
            if _margin_key(lines[first].strip()) in repeated_first:
                lines[first] = ""
            if _margin_key(lines[last].strip()) in repeated_last:
                lines[last] = ""
        cleaned.append("\n".join(lines))
    return cleaned


def _chunk_pages(pages: list[Page]) -> list[Chunk]:
    chunks: list[Chunk] = []
    section_stack: list[str] = []
    ordinal = 0
    for page in pages:
        buffer: list[str] = []
        buffer_section = " / ".join(section_stack)

        def emit() -> None:
            nonlocal buffer, ordinal
            content = "\n\n".join(buffer).strip()
            if not content:
                buffer = []
                return
            ordinal += 1
            chunks.append(
                _chunk(ordinal, page.page_no, buffer_section, content)
            )
            buffer = _overlap_tail(buffer)

        blocks = [part.strip() for part in re.split(r"\n\s*\n", page.text) if part.strip()]
        for block in blocks:
            heading = _heading(block)
            if heading is not None:
                if buffer:
                    emit()
                    buffer = []
                level, title = heading
                section_stack = section_stack[: level - 1]
                section_stack.append(title[:160])
                buffer_section = " / ".join(section_stack)
            for part in _split_oversized_block(block, MAX_CHUNK_TOKENS):
                candidate = "\n\n".join([*buffer, part]).strip()
                if buffer and count_tokens(candidate) > MAX_CHUNK_TOKENS:
                    emit()
                    candidate = "\n\n".join([*buffer, part]).strip()
                    if buffer and count_tokens(candidate) > MAX_CHUNK_TOKENS:
                        buffer = []
                buffer.append(part)
        if buffer:
            emit()
    return chunks


def _chunk(ordinal: int, page_no: int, section: str, content: str) -> Chunk:
    content = content.strip()
    return Chunk(
        ordinal=ordinal,
        page_start=page_no,
        page_end=page_no,
        section_path=section,
        content=content,
        token_count=max(1, count_tokens(content)),
        content_hash=_hash(content),
    )


def _split_oversized_block(value: str, limit: int) -> list[str]:
    if count_tokens(value) <= limit:
        return [value]
    encoding = _token_encoding()
    if encoding is not None:
        tokens = encoding.encode(value)
        return [encoding.decode(tokens[start : start + limit]) for start in range(0, len(tokens), limit)]
    parts: list[str] = []
    buffer: list[str] = []
    tokens = 0
    for char in value:
        increment = _fallback_token_count(char)
        if buffer and tokens + increment > limit:
            parts.append("".join(buffer))
            buffer, tokens = [], 0
        buffer.append(char)
        tokens += increment
    if buffer:
        parts.append("".join(buffer))
    return parts


def _overlap_tail(blocks: list[str]) -> list[str]:
    tail: list[str] = []
    tokens = 0
    for block in reversed(blocks):
        block_tokens = count_tokens(block)
        if tail and tokens + block_tokens > OVERLAP_TOKENS:
            break
        if block_tokens > OVERLAP_TOKENS:
            break
        tail.insert(0, block)
        tokens += block_tokens
    return tail


def _heading(value: str) -> tuple[int, str] | None:
    if "\n" in value or len(value) > 180:
        return None
    match = _MARKDOWN_HEADING.match(value)
    if match:
        return len(match.group(1)), match.group(2).strip()
    if _is_plain_heading(value):
        return 2, value.rstrip("：:").strip()
    return None


def _is_plain_heading(value: str) -> bool:
    if len(value) > 100:
        return False
    return bool(_PLAIN_HEADING.match(value) or value.endswith(("：", ":")))


def _looks_like_fixed_width_row(value: str) -> bool:
    stripped = value.strip()
    return bool(stripped and len(re.findall(r"\S(?: {2,}|\t+)\S", stripped)) >= 1)


def _build_source_ir(pages: list[Page], chunks: list[Chunk]) -> dict[str, Any]:
    """Build a domain-neutral structural representation with stable source IDs.

    The text representation remains available for unstructured work.  Fixed-width
    tables additionally retain columns, row groups, explicit/inherited cells and
    page/line provenance so downstream models never need to rediscover hierarchy
    after arbitrary token splits.
    """

    raw_tables: dict[str, dict[str, Any]] = {}
    for page in pages:
        candidate = _page_fixed_width_table(page)
        if candidate is None:
            continue
        signature = _table_signature(candidate["columns"])
        table = raw_tables.setdefault(
            signature,
            {
                "id": f"table:{signature[:16]}",
                "type": "table",
                "columns": candidate["columns"],
                "page_start": page.page_no,
                "page_end": page.page_no,
                "raw_rows": [],
            },
        )
        table["page_start"] = min(int(table["page_start"]), page.page_no)
        table["page_end"] = max(int(table["page_end"]), page.page_no)
        table["raw_rows"].extend(candidate["rows"])

    tables = [_finalize_source_table(value) for value in raw_tables.values()]
    tables = [table for table in tables if table["rows"]]
    tables.sort(key=lambda table: (int(table["page_start"]), str(table["id"])))
    blocks = [
        {
            "id": f"block:{chunk.content_hash[:16]}",
            "type": "text_span",
            "page_start": chunk.page_start,
            "page_end": chunk.page_end,
            "section_path": chunk.section_path,
            "text": chunk.content,
        }
        for chunk in chunks
    ]
    return {
        "version": SOURCE_IR_VERSION,
        "structure_preserved": bool(tables),
        "tables": tables,
        "blocks": blocks,
    }


def _page_fixed_width_table(page: Page) -> dict[str, Any] | None:
    lines = page.text.splitlines()
    candidates: list[tuple[float, int, list[tuple[int, str]]]] = []
    for index, line in enumerate(lines):
        if line.strip().startswith("```"):
            continue
        segments = _layout_segments(line)
        if not 3 <= len(segments) <= 12:
            continue
        combined = " ".join(value for _, value in segments)
        digit_ratio = sum(char.isdigit() for char in combined) / max(1, len(combined))
        short_ratio = sum(len(value) <= 32 for _, value in segments) / len(segments)
        alpha_cells = sum(any(char.isalpha() for char in value) for _, value in segments)
        score = len(segments) * 4 + short_ratio * 3 + alpha_cells - digit_ratio * 100
        if digit_ratio == 0:
            score += 10
        score += max(0.0, 3.0 - index / max(1, len(lines)) * 6)
        candidates.append((score, index, segments))
    if not candidates:
        return None
    _, header_index, header_segments = max(candidates, key=lambda item: item[0])
    column_count = len(header_segments)

    position_counts: dict[int, int] = {}
    for line in lines[header_index + 1 :]:
        for position, _ in _layout_segments(line):
            position_counts[position] = position_counts.get(position, 0) + 1
    clustered: list[dict[str, Any]] = []
    for position, count in sorted(position_counts.items()):
        if clustered and position - int(clustered[-1]["maximum"]) <= 8:
            cluster = clustered[-1]
            cluster["positions"].extend([position] * count)
            cluster["count"] = int(cluster["count"]) + count
            cluster["maximum"] = position
        else:
            clustered.append(
                {
                    "positions": [position] * count,
                    "count": count,
                    "maximum": position,
                }
            )
    ranked = sorted(clustered, key=lambda item: int(item["count"]), reverse=True)
    anchors = sorted(
        {
            min(item["positions"])
            for item in ranked[:column_count]
        }
    )
    if len(anchors) < 3:
        anchors = [position for position, _ in header_segments]
    if anchors and anchors[0] <= 3:
        anchors[0] = 0
    if len(anchors) != column_count:
        anchors = [position for position, _ in header_segments]
    anchors = _spread_layout_anchors(anchors)
    if len(anchors) < 3:
        return None

    header_end = header_index
    for index in range(header_index + 1, min(len(lines), header_index + 4)):
        cells = _slice_layout_line(lines[index], anchors)
        visible = " ".join(cell for cell in cells if cell)
        if not visible or any(char.isdigit() for char in visible):
            break
        if sum(bool(cell) for cell in cells) < 2:
            break
        header_end = index
    header_lines = lines[header_index : header_end + 1]
    labels_by_column: list[list[str]] = [[] for _ in anchors]
    for line in header_lines:
        for position, label in _layout_segments(line):
            nearest = min(range(len(anchors)), key=lambda index: abs(anchors[index] - position))
            labels_by_column[nearest].append(label)
    columns: list[dict[str, Any]] = []
    for column_index, start in enumerate(anchors):
        labels = labels_by_column[column_index]
        label = " ".join(dict.fromkeys(labels)).strip() or f"Column {column_index + 1}"
        columns.append(
            {
                "id": f"c{column_index + 1}",
                "index": column_index,
                "label": label[:120],
                "x_start": start,
            }
        )

    raw_rows: list[dict[str, Any]] = []
    trailing_non_rows = 0
    for line_index in range(header_end + 1, len(lines)):
        line = lines[line_index]
        stripped = line.strip()
        if not stripped or stripped.startswith("```"):
            continue
        if re.search(r"\bPage\s+\d+\s+(?:of|/)\s+\d+\b", stripped, re.I):
            continue
        cells = _slice_layout_line(line, anchors)
        populated = [index for index, cell in enumerate(cells) if cell]
        if len(populated) >= 3:
            trailing_non_rows = 0
            raw_rows.append(
                {
                    "page": page.page_no,
                    "line": line_index + 1,
                    "cells": cells,
                }
            )
            continue
        if populated and len(populated) <= 2 and raw_rows:
            leading_only = max(populated) < max(2, len(anchors) // 2)
            if leading_only and not _later_structural_row(lines, line_index + 1, anchors):
                break
            for column_index in populated:
                previous = raw_rows[-1]["cells"][column_index]
                raw_rows[-1]["cells"][column_index] = (
                    f"{previous} {cells[column_index]}".strip()
                )
            continue
        trailing_non_rows += 1
        if raw_rows and trailing_non_rows >= 8:
            break
    if len(raw_rows) < 2:
        return None
    return {"columns": columns, "rows": raw_rows}


def _later_structural_row(lines: list[str], start: int, anchors: list[int]) -> bool:
    inspected = 0
    for line in lines[start:]:
        if not line.strip() or line.strip().startswith("```"):
            continue
        inspected += 1
        if sum(bool(value) for value in _slice_layout_line(line, anchors)) >= 3:
            return True
        if inspected >= 4:
            break
    return False


def _layout_segments(line: str) -> list[tuple[int, str]]:
    segments: list[tuple[int, str]] = []
    cursor = 0
    for separator in re.finditer(r"(?: {2,}|\t+)", line):
        value = line[cursor : separator.start()]
        stripped = value.strip()
        if stripped:
            segments.append((cursor + len(value) - len(value.lstrip()), stripped))
        cursor = separator.end()
    value = line[cursor:]
    stripped = value.strip()
    if stripped:
        segments.append((cursor + len(value) - len(value.lstrip()), stripped))
    return segments


def _spread_layout_anchors(values: list[int]) -> list[int]:
    result: list[int] = []
    for value in sorted(dict.fromkeys(values)):
        if result and value - result[-1] < 6:
            continue
        result.append(max(0, value))
    return result


def _slice_layout_line(line: str, anchors: list[int]) -> list[str]:
    cells: list[str] = []
    for index, start in enumerate(anchors):
        end = anchors[index + 1] if index + 1 < len(anchors) else len(line)
        cells.append(re.sub(r"\s+", " ", line[start:end]).strip())
    return cells


def _table_signature(columns: list[dict[str, Any]]) -> str:
    labels = [re.sub(r"\W+", "", str(item.get("label") or "").casefold()) for item in columns]
    return _hash(json.dumps(labels, ensure_ascii=False, separators=(",", ":")))


def _finalize_source_table(raw: dict[str, Any]) -> dict[str, Any]:
    table_id = str(raw["id"])
    columns = [dict(value) for value in raw["columns"]]
    for column in columns:
        column["id"] = f"{table_id}:{column['id']}"
    rows: list[dict[str, Any]] = []
    groups: list[dict[str, Any]] = []
    group: dict[str, Any] | None = None
    inherited: list[dict[str, str] | None] = [None] * len(columns)
    for raw_row in raw["raw_rows"]:
        page = int(raw_row["page"])
        line = int(raw_row["line"])
        values = [str(value or "").strip() for value in raw_row["cells"]]
        row_digest = _hash(
            f"{table_id}|{page}|{line}|" + json.dumps(values, ensure_ascii=False)
        )[:16]
        row_id = f"row:{row_digest}"
        if values and values[0]:
            group = {
                "id": f"group:{row_digest}",
                "type": "row_group",
                "row_ids": [],
                "page_start": page,
                "page_end": page,
            }
            groups.append(group)
        elif group is None:
            group = {
                "id": f"group:{table_id.split(':')[-1]}:leading",
                "type": "row_group",
                "row_ids": [],
                "page_start": page,
                "page_end": page,
                "continuation_without_parent": True,
            }
            groups.append(group)
        cells: list[dict[str, Any]] = []
        for index, value in enumerate(values):
            cell_id = f"{row_id}:c{index + 1}"
            cell: dict[str, Any] = {
                "id": cell_id,
                "column_id": columns[index]["id"],
                "text": value,
                "explicit": bool(value),
            }
            if value:
                inherited[index] = {"id": cell_id, "text": value}
            elif inherited[index] is not None:
                cell["inherited_from"] = inherited[index]["id"]
                cell["inherited_text"] = inherited[index]["text"]
            cells.append(cell)
        assert group is not None
        group["row_ids"].append(row_id)
        group["page_end"] = page
        rows.append(
            {
                "id": row_id,
                "type": "row",
                "group_id": group["id"],
                "page": page,
                "line": line,
                "source_locator": f"page {page}, line {line}",
                "cells": cells,
            }
        )
    return {
        "id": table_id,
        "type": "table",
        "page_start": int(raw["page_start"]),
        "page_end": int(raw["page_end"]),
        "columns": columns,
        "row_groups": groups,
        "rows": rows,
    }


def _margin_key(value: str) -> str:
    value = re.sub(r"\d+", "#", value.strip().lower())
    return value if len(value) <= 120 else ""


def _needs_structured_fallback(pages: list[Page]) -> bool:
    if not pages:
        return True
    low_quality = sum(page.quality < 0.5 for page in pages)
    if low_quality == len(pages):
        return True
    # Avoid paying the structured-parser cost for an occasional intentionally
    # blank page; fall back only when low-quality extraction is systemic.
    return low_quality >= max(2, math.ceil(len(pages) * 0.25))


def _docling_available() -> bool:
    return importlib.util.find_spec("docling") is not None


@lru_cache(maxsize=1)
def _token_encoding() -> Any | None:
    try:
        import tiktoken  # type: ignore[import-not-found]
    except ImportError:
        return None
    name = os.environ.get("PDF_TOKEN_ENCODING", "o200k_base").strip() or "o200k_base"
    try:
        return tiktoken.get_encoding(name)
    except Exception:
        # tiktoken may fetch encoding data on first use. An offline worker must
        # still be able to parse documents, so use the local conservative
        # estimator instead of failing the request.
        if name != "o200k_base":
            try:
                return tiktoken.get_encoding("o200k_base")
            except Exception:
                pass
        return None


def _fallback_token_count(value: str) -> int:
    total = 0
    ascii_run = 0

    def flush_ascii() -> None:
        nonlocal ascii_run, total
        if ascii_run:
            total += math.ceil(ascii_run / 4)
            ascii_run = 0

    for char in value:
        codepoint = ord(char)
        if char.isspace():
            flush_ascii()
        elif char.isascii() and char.isalnum():
            ascii_run += 1
        elif (
            0x3400 <= codepoint <= 0x9FFF
            or 0xF900 <= codepoint <= 0xFAFF
            or 0x3040 <= codepoint <= 0x30FF
            or 0xAC00 <= codepoint <= 0xD7AF
        ):
            flush_ascii()
            total += 1
        else:
            flush_ascii()
            total += 1
    flush_ascii()
    return total


def _hash(value: str) -> str:
    return hashlib.sha256(value.encode("utf-8")).hexdigest()


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--media-type", required=True)
    args = parser.parse_args()
    try:
        result = parse_document(sys.stdin.buffer.read(), args.media_type)
    except Exception as exc:
        print(json.dumps({"error": str(exc)}, ensure_ascii=False), file=sys.stderr)
        raise SystemExit(2) from exc
    print(json.dumps(asdict(result), ensure_ascii=False, separators=(",", ":")))


if __name__ == "__main__":
    main()
