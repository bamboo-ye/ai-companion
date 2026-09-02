from __future__ import annotations

import math
import re
from collections import Counter
from dataclasses import dataclass
from statistics import median
from typing import Any

import pymupdf  # type: ignore[import-untyped]

from ai_companion_worker.pdf_utils import normalize_pdf_bytes

MAX_LAYOUT_PAGES = 100
MAX_TEXT_BLOCK_CHARS = 20_000
PREVIEW_MAX_DIMENSION = 420
PREVIEW_JPEG_QUALITY = 35
_WHITESPACE = re.compile(r"[ \t]+")


@dataclass(frozen=True)
class PdfTextBlock:
    page_no: int
    block_no: int
    bbox: tuple[float, float, float, float]
    text: str
    font_size: float
    color: tuple[float, float, float]
    rotation: int

    @property
    def marker(self) -> str:
        return f"[[PAGE {self.page_no} BLOCK {self.block_no}]]"


@dataclass(frozen=True)
class PdfLayout:
    source: bytes
    parser_version: str
    page_count: int
    blocks: tuple[PdfTextBlock, ...]
    page_previews: dict[int, bytes]
    page_sizes: tuple[tuple[float, float], ...]
    page_rotations: tuple[int, ...]
    image_placements: tuple[tuple[int, float, float, float, float, int, int], ...]

    @property
    def image_count(self) -> int:
        return len(self.image_placements)


def extract_pdf_layout(source: bytes, *, max_pages: int = MAX_LAYOUT_PAGES) -> PdfLayout:
    normalized = normalize_pdf_bytes(source)
    document = pymupdf.open(stream=normalized, filetype="pdf")
    try:
        if document.needs_pass and not document.authenticate(""):
            raise ValueError("encrypted_pdf")
        if document.page_count > max_pages:
            raise ValueError("too_many_pages")

        blocks: list[PdfTextBlock] = []
        previews: dict[int, bytes] = {}
        page_sizes: list[tuple[float, float]] = []
        page_rotations: list[int] = []
        image_placements: list[tuple[int, float, float, float, float, int, int]] = []
        for page_index in range(document.page_count):
            page = document[page_index]
            page_no = page_index + 1
            page_sizes.append((float(page.rect.width), float(page.rect.height)))
            page_rotations.append(int(page.rotation))
            image_placements.extend(_page_image_placements(page, page_no))
            page_blocks = _page_text_blocks(page, page_no)
            blocks.extend(page_blocks)
            if page_blocks:
                previews[page_no] = _page_preview(page)
        if not blocks:
            raise ValueError("no_extractable_text")
        return PdfLayout(
            source=normalized,
            parser_version=f"pymupdf-{pymupdf.VersionBind}-layout-v1",
            page_count=document.page_count,
            blocks=tuple(blocks),
            page_previews=previews,
            page_sizes=tuple(page_sizes),
            page_rotations=tuple(page_rotations),
            image_placements=tuple(image_placements),
        )
    finally:
        document.close()


def render_layout_translation(
    layout: PdfLayout,
    translations: dict[str, str],
    *,
    font_path: str,
) -> tuple[bytes, dict[str, Any]]:
    expected_markers = {block.marker for block in layout.blocks}
    if set(translations) != expected_markers:
        raise ValueError("translation_block_markers_invalid")

    document = pymupdf.open(stream=layout.source, filetype="pdf")
    overflow_markers: list[str] = []
    try:
        blocks_by_page: dict[int, list[PdfTextBlock]] = {}
        for block in layout.blocks:
            blocks_by_page.setdefault(block.page_no, []).append(block)

        for page_no, blocks in blocks_by_page.items():
            page = document[page_no - 1]
            for block in blocks:
                page.add_redact_annot(
                    pymupdf.Rect(block.bbox),
                    fill=False,
                    cross_out=False,
                )
            page.apply_redactions(
                images=pymupdf.PDF_REDACT_IMAGE_NONE,
                graphics=pymupdf.PDF_REDACT_LINE_ART_NONE,
                text=pymupdf.PDF_REDACT_TEXT_REMOVE,
            )
            font_name = f"TranslatedCJK{page_no}"
            page.insert_font(fontname=font_name, fontfile=font_path)
            for block in blocks:
                translated = _normalize_translation(translations[block.marker])
                if not translated:
                    raise ValueError("translation_model_returned_empty_block")
                if not _insert_fitted_text(page, block, translated, font_name):
                    overflow_markers.append(block.marker)

        if overflow_markers:
            raise ValueError("translated_text_does_not_fit_layout")
        document.subset_fonts()
        rendered = document.tobytes(
            garbage=4,
            deflate=True,
            deflate_fonts=True,
            clean=True,
        )
    finally:
        document.close()

    _validate_layout_output(layout, rendered)
    return rendered, {
        "layout_preserved": True,
        "text_block_count": len(layout.blocks),
        "image_count": layout.image_count,
        "untranslated_block_count": 0,
        "overflow_block_count": 0,
    }


def _page_text_blocks(page: Any, page_no: int) -> list[PdfTextBlock]:
    flags = pymupdf.TEXTFLAGS_DICT & ~pymupdf.TEXT_PRESERVE_IMAGES
    payload = page.get_text("dict", flags=flags, sort=True)
    output: list[PdfTextBlock] = []
    for raw_block in payload.get("blocks", []):
        if raw_block.get("type") != 0:
            continue
        lines = raw_block.get("lines", [])
        text_lines: list[str] = []
        spans: list[dict[str, Any]] = []
        directions: list[tuple[float, float]] = []
        for line in lines:
            visible_spans = [
                span
                for span in line.get("spans", [])
                if int(span.get("alpha", 255)) > 0 and str(span.get("text", "")).strip()
            ]
            if not visible_spans:
                continue
            line_text = "".join(str(span.get("text", "")) for span in visible_spans).strip()
            if line_text:
                text_lines.append(line_text)
                spans.extend(visible_spans)
                direction = line.get("dir", (1.0, 0.0))
                if isinstance(direction, (list, tuple)) and len(direction) == 2:
                    directions.append((float(direction[0]), float(direction[1])))
        text = "\n".join(text_lines).strip()
        if not text:
            continue
        if len(text) > MAX_TEXT_BLOCK_CHARS:
            raise ValueError("pdf_text_block_too_large")
        bbox = tuple(float(value) for value in raw_block.get("bbox", ()))
        if len(bbox) != 4 or bbox[2] <= bbox[0] or bbox[3] <= bbox[1]:
            continue
        output.append(
            PdfTextBlock(
                page_no=page_no,
                block_no=len(output) + 1,
                bbox=(bbox[0], bbox[1], bbox[2], bbox[3]),
                text=text,
                font_size=_dominant_font_size(spans),
                color=_dominant_color(spans),
                rotation=_dominant_rotation(directions),
            )
        )
    return output


def _page_preview(page: Any) -> bytes:
    longest = max(float(page.rect.width), float(page.rect.height), 1.0)
    scale = min(1.0, PREVIEW_MAX_DIMENSION / longest)
    pixmap = page.get_pixmap(matrix=pymupdf.Matrix(scale, scale), alpha=False)
    return pixmap.tobytes("jpeg", jpg_quality=PREVIEW_JPEG_QUALITY)


def _page_image_placements(
    page: Any,
    page_no: int,
) -> list[tuple[int, float, float, float, float, int, int]]:
    placements: list[tuple[int, float, float, float, float, int, int]] = []
    for image in page.get_image_info(xrefs=True):
        bbox = tuple(float(value) for value in image.get("bbox", ()))
        if len(bbox) != 4:
            continue
        placements.append(
            (
                page_no,
                round(bbox[0], 2),
                round(bbox[1], 2),
                round(bbox[2], 2),
                round(bbox[3], 2),
                int(image.get("width", 0)),
                int(image.get("height", 0)),
            )
        )
    return placements


def _dominant_font_size(spans: list[dict[str, Any]]) -> float:
    sizes = [float(span.get("size", 10.0)) for span in spans if float(span.get("size", 0)) > 0]
    return max(4.0, min(72.0, float(median(sizes)) if sizes else 10.0))


def _dominant_color(spans: list[dict[str, Any]]) -> tuple[float, float, float]:
    weighted: Counter[int] = Counter()
    for span in spans:
        weighted[int(span.get("color", 0))] += max(1, len(str(span.get("text", ""))))
    value = weighted.most_common(1)[0][0] if weighted else 0
    return (
        ((value >> 16) & 0xFF) / 255.0,
        ((value >> 8) & 0xFF) / 255.0,
        (value & 0xFF) / 255.0,
    )


def _dominant_rotation(directions: list[tuple[float, float]]) -> int:
    if not directions:
        return 0
    dx, dy = directions[0]
    angle = int(round(math.degrees(math.atan2(-dy, dx)) / 90.0) * 90) % 360
    return angle if angle in {0, 90, 180, 270} else 0


def _normalize_translation(value: str) -> str:
    lines = [_WHITESPACE.sub(" ", line).strip() for line in value.splitlines()]
    return "\n".join(line for line in lines if line).strip()


def _insert_fitted_text(page: Any, block: PdfTextBlock, text: str, font_name: str) -> bool:
    rect = pymupdf.Rect(block.bbox)
    start_size = min(72.0, max(4.0, block.font_size * 0.92))
    size = start_size
    while size >= 3.5:
        spare = page.insert_textbox(
            rect,
            text,
            fontname=font_name,
            fontsize=size,
            lineheight=1.05,
            color=block.color,
            align=pymupdf.TEXT_ALIGN_LEFT,
            rotate=block.rotation,
            overlay=True,
        )
        if spare >= -0.01:
            return True
        size -= 0.5
    return False


def _validate_layout_output(layout: PdfLayout, rendered: bytes) -> None:
    output = pymupdf.open(stream=rendered, filetype="pdf")
    try:
        if output.page_count != layout.page_count:
            raise ValueError("translated_pdf_page_count_changed")
        sizes = tuple((float(page.rect.width), float(page.rect.height)) for page in output)
        rotations = tuple(int(page.rotation) for page in output)
        if any(
            abs(before[0] - after[0]) > 0.01 or abs(before[1] - after[1]) > 0.01
            for before, after in zip(layout.page_sizes, sizes, strict=True)
        ):
            raise ValueError("translated_pdf_page_size_changed")
        if rotations != layout.page_rotations:
            raise ValueError("translated_pdf_page_rotation_changed")
        images: list[tuple[int, float, float, float, float, int, int]] = []
        for index, page in enumerate(output):
            images.extend(_page_image_placements(page, index + 1))
        if tuple(images) != layout.image_placements:
            raise ValueError("translated_pdf_image_layout_changed")
    finally:
        output.close()
