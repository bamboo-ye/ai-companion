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

PARSER_VERSION = "pypdf-6.14.2-markdown-v2"
TEXT_PARSER_VERSION = "text-markdown-v2"
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


def parse_document(
    data: bytes,
    media_type: str,
    *,
    max_pages: int = MAX_PAGES,
) -> ParseResult:
    if media_type == "application/pdf":
        parser_version, pages = _parse_pdf(data, max_pages=max_pages)
    elif media_type == "text/plain":
        parser_version = TEXT_PARSER_VERSION
        pages = [_page(1, _normalize_markdown(data.decode("utf-8-sig")))]
    else:
        raise ValueError("unsupported_media_type")
    if not any(page.text.strip() for page in pages):
        raise ValueError("no_extractable_text")
    chunks = _chunk_pages(pages)
    if not chunks:
        raise ValueError("no_extractable_text")
    return ParseResult(parser_version, pages, chunks)


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
