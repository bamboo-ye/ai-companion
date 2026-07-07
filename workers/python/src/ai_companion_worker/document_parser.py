from __future__ import annotations

import argparse
import hashlib
import io
import json
import re
import sys
from dataclasses import asdict, dataclass

from pypdf import PdfReader

PARSER_VERSION = "pypdf-6.14.2-structural-v1"
MAX_CHUNK_CHARS = 2400
OVERLAP_CHARS = 280
MAX_PAGES = 1000
MAX_PAGE_CHARS = 2_000_000


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


def parse_document(data: bytes, media_type: str) -> ParseResult:
    if media_type == "application/pdf":
        pages = _parse_pdf(data)
    elif media_type == "text/plain":
        pages = [_page(1, data.decode("utf-8"))]
    else:
        raise ValueError("unsupported_media_type")
    if not any(page.text.strip() for page in pages):
        raise ValueError("no_extractable_text")
    chunks = _chunk_pages(pages)
    if not chunks:
        raise ValueError("no_extractable_text")
    return ParseResult(PARSER_VERSION, pages, chunks)


def _parse_pdf(data: bytes) -> list[Page]:
    reader = PdfReader(io.BytesIO(data), strict=False)
    if reader.is_encrypted:
        raise ValueError("encrypted_pdf")
    if len(reader.pages) > MAX_PAGES:
        raise ValueError("too_many_pages")
    pages: list[Page] = []
    for page_no, pdf_page in enumerate(reader.pages, start=1):
        try:
            text = pdf_page.extract_text(
                extraction_mode="layout", layout_mode_space_vertically=False
            )
        except Exception:
            text = pdf_page.extract_text() or ""
        if len(text) > MAX_PAGE_CHARS:
            raise ValueError("page_text_too_large")
        pages.append(_page(page_no, text or ""))
    return pages


def _page(page_no: int, text: str) -> Page:
    normalized = _normalize(text)
    visible = len(re.sub(r"\s+", "", normalized))
    quality = 1.0 if visible >= 40 else 0.5 if visible >= 10 else 0.2
    return Page(page_no, normalized, quality, _hash(normalized))


def _normalize(text: str) -> str:
    text = text.replace("\r\n", "\n").replace("\r", "\n").replace("\x00", "")
    lines = [re.sub(r"[ \t]+", " ", line).strip() for line in text.split("\n")]
    return "\n".join(lines).strip()


def _chunk_pages(pages: list[Page]) -> list[Chunk]:
    chunks: list[Chunk] = []
    ordinal = 0
    for page in pages:
        section = ""
        paragraphs = [part.strip() for part in re.split(r"\n\s*\n|(?<=。)\s*\n", page.text) if part.strip()]
        buffer = ""
        for paragraph in paragraphs:
            if _is_heading(paragraph):
                section = paragraph[:160]
            candidate = paragraph if not buffer else buffer + "\n\n" + paragraph
            if len(candidate) <= MAX_CHUNK_CHARS:
                buffer = candidate
                continue
            if buffer:
                ordinal += 1
                chunks.append(_chunk(ordinal, page.page_no, section, buffer))
                buffer = buffer[-OVERLAP_CHARS:] + "\n\n" + paragraph
            else:
                for start in range(0, len(paragraph), MAX_CHUNK_CHARS - OVERLAP_CHARS):
                    part = paragraph[start : start + MAX_CHUNK_CHARS]
                    if part:
                        ordinal += 1
                        chunks.append(_chunk(ordinal, page.page_no, section, part))
                buffer = ""
        if buffer:
            ordinal += 1
            chunks.append(_chunk(ordinal, page.page_no, section, buffer))
    return chunks


def _chunk(ordinal: int, page_no: int, section: str, content: str) -> Chunk:
    content = content.strip()
    return Chunk(
        ordinal=ordinal,
        page_start=page_no,
        page_end=page_no,
        section_path=section,
        content=content,
        token_count=max(1, (len(content) + 3) // 4),
        content_hash=_hash(content),
    )


def _is_heading(value: str) -> bool:
    if "\n" in value or len(value) > 100:
        return False
    return bool(
        re.match(r"^(?:#{1,6}\s+|第[一二三四五六七八九十百0-9]+[章节部分]|\d+(?:\.\d+)*[、.\s])", value)
        or value.endswith(("：", ":"))
    )


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
