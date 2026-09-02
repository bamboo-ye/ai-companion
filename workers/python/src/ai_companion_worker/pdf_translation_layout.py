from __future__ import annotations

import math
import re
import shutil
import subprocess
import tempfile
import unicodedata
from collections import Counter
from dataclasses import dataclass
from pathlib import Path
from statistics import median
from typing import Any

import pymupdf  # type: ignore[import-untyped]
from PIL import Image

from ai_companion_worker.pdf_utils import normalize_pdf_bytes
from ai_companion_worker.translation_language import normalize_translated_script

MAX_LAYOUT_PAGES = 100
MAX_TEXT_BLOCK_CHARS = 20_000
PREVIEW_MAX_DIMENSION = 420
PREVIEW_JPEG_QUALITY = 35
MIN_READABLE_TRANSLATION_FONT_SIZE = 6.0
_WHITESPACE = re.compile(r"[ \t]+")
_BULLET_PREFIX = re.compile(
    r"^(?P<indent>[ \t]*)(?:[•◦▪▫●○‣⁃‧∙·◆◇►▸▶▷✓✔☑]|(?:\d+|[A-Za-z])[.)])(?:[ \t]+|$)"
)
_TRAILING_BULLET = re.compile(r"\s*[•◦▪▫●○‣⁃‧∙·◆◇►▸▶▷✓✔☑]+\s*$")
_INLINE_BULLET = re.compile(r"\s+[•◦▪▫●○‣⁃‧∙·◆◇►▸▶▷✓✔☑]+\s+")
_UNSAFE_OUTPUT_CHARACTER = re.compile(r"[\x00-\x08\x0b\x0c\x0e-\x1f\x7f\ufffd]")
_SAFE_CHARACTER_REPLACEMENTS = str.maketrans(
    {
        "‐": "-",
        "‑": "-",
        "‒": "-",
        "–": "-",
        "—": "-",
        "−": "-",
        "→": "->",
        "←": "<-",
        "↔": "<->",
        "⇒": "=>",
        "⇐": "<=",
        "⇔": "<=>",
        "☺": "",
        "☻": "",
        "🙂": "",
        "😀": "",
        "😊": "",
        "😉": "",
    }
)


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


@dataclass(frozen=True)
class _TranslationPlan:
    group: tuple[PdfTextBlock, ...]
    block: PdfTextBlock
    text: str
    font_size: float | None


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
    simplified_chinese: bool = False,
) -> tuple[bytes, dict[str, Any]]:
    expected_markers = {block.marker for block in layout.blocks}
    if set(translations) != expected_markers:
        raise ValueError("translation_block_markers_invalid")

    document = pymupdf.open(stream=layout.source, filetype="pdf")
    preserved_markers: list[str] = []
    try:
        blocks_by_page: dict[int, list[PdfTextBlock]] = {}
        for block in layout.blocks:
            blocks_by_page.setdefault(block.page_no, []).append(block)

        embedded_font = pymupdf.Font(fontfile=font_path)
        for page_no, blocks in blocks_by_page.items():
            page = document[page_no - 1]
            font_name = f"TranslatedCJK{page_no}"
            probe = pymupdf.open()
            try:
                probe_page = probe.new_page(width=page.rect.width, height=page.rect.height)
                probe_page.insert_font(fontname=font_name, fontfile=font_path)
                plans = [
                    _translation_plan(
                        probe_page,
                        group,
                        translations,
                        embedded_font,
                        font_name,
                        simplified_chinese=simplified_chinese,
                    )
                    for group in _overlapping_text_groups(blocks)
                ]
            finally:
                probe.close()

            translated_plans = [plan for plan in plans if plan.font_size is not None]
            for plan in plans:
                if plan.font_size is None:
                    preserved_markers.extend(item.marker for item in plan.group)
                    continue
                for block in plan.group:
                    page.add_redact_annot(
                        pymupdf.Rect(block.bbox),
                        fill=False,
                        cross_out=False,
                    )
            if not translated_plans:
                continue
            page.apply_redactions(
                images=pymupdf.PDF_REDACT_IMAGE_NONE,
                graphics=pymupdf.PDF_REDACT_LINE_ART_NONE,
                text=pymupdf.PDF_REDACT_TEXT_REMOVE,
            )
            page.insert_font(fontname=font_name, fontfile=font_path)
            for plan in translated_plans:
                if plan.font_size is None or not _insert_text_at_size(
                    page,
                    plan.block,
                    plan.text,
                    font_name,
                    plan.font_size,
                ):
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

    _validate_layout_output(
        layout,
        rendered,
        simplified_chinese=simplified_chinese,
        has_preserved_original_text=bool(preserved_markers),
    )
    return rendered, {
        "layout_preserved": True,
        "text_block_count": len(layout.blocks),
        "image_count": layout.image_count,
        "untranslated_block_count": len(preserved_markers),
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
    output: list[str] = []
    pending_bullet_indent: str | None = None
    for raw_line in unicodedata.normalize("NFC", str(value or "")).splitlines():
        raw_line = raw_line.translate(_SAFE_CHARACTER_REPLACEMENTS)
        raw_line = raw_line.replace("\ufe0f", "").replace("\u200d", "")
        bullet = _BULLET_PREFIX.match(raw_line)
        if bullet:
            indent = " " * min(6, len(bullet.group("indent").expandtabs(2)))
            content = _WHITESPACE.sub(" ", raw_line[bullet.end() :]).strip()
            content = _TRAILING_BULLET.sub("", content).strip()
            content = _INLINE_BULLET.sub(" - ", content).strip()
            if content:
                output.append(f"{indent}- {content}")
            else:
                pending_bullet_indent = indent
            continue
        content = _WHITESPACE.sub(" ", raw_line).strip()
        content = _TRAILING_BULLET.sub("", content).strip()
        content = _INLINE_BULLET.sub(" - ", content).strip()
        if not content:
            continue
        if pending_bullet_indent is not None:
            output.append(f"{pending_bullet_indent}- {content}")
            pending_bullet_indent = None
        else:
            output.append(content)
    if pending_bullet_indent is not None:
        output.append(f"{pending_bullet_indent}-")
    return "\n".join(output).strip()


def _overlapping_text_groups(blocks: list[PdfTextBlock]) -> list[tuple[PdfTextBlock, ...]]:
    remaining = list(blocks)
    groups: list[tuple[PdfTextBlock, ...]] = []
    while remaining:
        group = [remaining.pop(0)]
        changed = True
        while changed:
            changed = False
            for candidate in list(remaining):
                if any(_near_duplicate_layout_box(candidate, member) for member in group):
                    group.append(candidate)
                    remaining.remove(candidate)
                    changed = True
        groups.append(tuple(group))
    return groups


def _near_duplicate_layout_box(left: PdfTextBlock, right: PdfTextBlock) -> bool:
    if left.rotation != right.rotation:
        return False
    left_rect = pymupdf.Rect(left.bbox)
    right_rect = pymupdf.Rect(right.bbox)
    intersection = left_rect & right_rect
    minimum_area = min(left_rect.get_area(), right_rect.get_area())
    return (
        abs(left_rect.x0 - right_rect.x0) <= 4.0
        and abs(left_rect.y0 - right_rect.y0) <= 4.0
        and minimum_area > 0
        and intersection.get_area() / minimum_area >= 0.72
    )


def _merged_text_block(group: tuple[PdfTextBlock, ...]) -> PdfTextBlock:
    if len(group) == 1:
        return group[0]
    rect = pymupdf.Rect(group[0].bbox)
    for block in group[1:]:
        rect |= pymupdf.Rect(block.bbox)
    return PdfTextBlock(
        page_no=group[0].page_no,
        block_no=group[0].block_no,
        bbox=(float(rect.x0), float(rect.y0), float(rect.x1), float(rect.y1)),
        text="\n".join(block.text for block in group),
        font_size=float(median(block.font_size for block in group)),
        color=Counter(block.color for block in group).most_common(1)[0][0],
        rotation=group[0].rotation,
    )


def _translation_plan(
    probe_page: Any,
    group: tuple[PdfTextBlock, ...],
    translations: dict[str, str],
    font: Any,
    font_name: str,
    *,
    simplified_chinese: bool,
) -> _TranslationPlan:
    block = _merged_text_block(group)
    translated = _normalize_translation("\n".join(translations[item.marker] for item in group))
    translated = normalize_translated_script(
        translated,
        simplified_chinese=simplified_chinese,
    )
    if not translated:
        raise ValueError("translation_model_returned_empty_block")
    if any(
        character not in "\n\r\t" and not font.has_glyph(ord(character))
        for character in translated
    ):
        return _TranslationPlan(group=group, block=block, text=translated, font_size=None)
    font_size = _fitted_text_size(probe_page, block, translated, font_name)
    return _TranslationPlan(group=group, block=block, text=translated, font_size=font_size)


def _text_rect(block: PdfTextBlock) -> Any:
    rect = pymupdf.Rect(block.bbox)
    if rect.width > 4.0 and rect.height > 2.0:
        rect = pymupdf.Rect(rect.x0 + 0.5, rect.y0 + 0.2, rect.x1 - 0.5, rect.y1 - 0.2)
    return rect


def _fitted_text_size(page: Any, block: PdfTextBlock, text: str, font_name: str) -> float | None:
    rect = _text_rect(block)
    start_size = min(72.0, max(MIN_READABLE_TRANSLATION_FONT_SIZE, block.font_size * 0.90))
    size = start_size
    tried: set[float] = set()
    while size >= MIN_READABLE_TRANSLATION_FONT_SIZE:
        size = round(max(MIN_READABLE_TRANSLATION_FONT_SIZE, size), 2)
        if size in tried:
            break
        tried.add(size)
        spare = page.insert_textbox(
            rect,
            text,
            fontname=font_name,
            fontsize=size,
            lineheight=1.12,
            color=block.color,
            align=pymupdf.TEXT_ALIGN_LEFT,
            rotate=block.rotation,
            overlay=True,
        )
        if spare >= -0.01:
            return size
        size -= 0.5
    return None


def _insert_text_at_size(
    page: Any,
    block: PdfTextBlock,
    text: str,
    font_name: str,
    font_size: float,
) -> bool:
    spare = page.insert_textbox(
        _text_rect(block),
        text,
        fontname=font_name,
        fontsize=font_size,
        lineheight=1.12,
        color=block.color,
        align=pymupdf.TEXT_ALIGN_LEFT,
        rotate=block.rotation,
        overlay=True,
    )
    return spare >= -0.01


def _validate_layout_output(
    layout: PdfLayout,
    rendered: bytes,
    *,
    simplified_chinese: bool,
    has_preserved_original_text: bool,
) -> None:
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
        for page in output:
            text = page.get_text("text")
            if _UNSAFE_OUTPUT_CHARACTER.search(text):
                raise ValueError("translated_pdf_contains_invalid_glyphs")
            if (
                simplified_chinese
                and not has_preserved_original_text
                and normalize_translated_script(text, simplified_chinese=True) != text
            ):
                raise ValueError("translated_pdf_contains_traditional_chinese")
            _validate_page_text_collisions(page)
    finally:
        output.close()
    _validate_cross_renderer_page_coverage(layout.source, rendered)


def _validate_page_text_collisions(page: Any) -> None:
    blocks = [
        pymupdf.Rect(block[:4])
        for block in page.get_text("blocks", sort=True)
        if int(block[6]) == 0 and str(block[4]).strip()
    ]
    for index, left in enumerate(blocks):
        for right in blocks[index + 1 :]:
            intersection = left & right
            minimum_area = min(left.get_area(), right.get_area())
            if (
                minimum_area > 0
                and intersection.width > 0.8
                and intersection.height > 0.8
                and intersection.get_area() / minimum_area >= 0.35
            ):
                raise ValueError("translated_pdf_contains_overlapping_text")


def _validate_cross_renderer_page_coverage(source: bytes, rendered: bytes) -> None:
    executable = shutil.which("pdftoppm")
    if not executable:
        raise ValueError("pdf_cross_renderer_is_not_available")
    with tempfile.TemporaryDirectory(prefix="pdf-translation-qa-") as directory:
        root = Path(directory)
        source_path = root / "source.pdf"
        rendered_path = root / "rendered.pdf"
        source_path.write_bytes(source)
        rendered_path.write_bytes(rendered)
        source_pages = _render_poppler_pages(executable, source_path, root / "source")
        rendered_pages = _render_poppler_pages(executable, rendered_path, root / "rendered")
        if len(source_pages) != len(rendered_pages):
            raise ValueError("translated_pdf_cross_renderer_page_count_changed")
        for source_page, rendered_page in zip(source_pages, rendered_pages, strict=True):
            source_ink = _rendered_page_ink_ratio(source_page)
            rendered_ink = _rendered_page_ink_ratio(rendered_page)
            if source_ink >= 0.001 and rendered_ink / source_ink < 0.50:
                raise ValueError("translated_pdf_visual_content_missing")


def _render_poppler_pages(executable: str, pdf_path: Path, prefix: Path) -> list[Path]:
    completed = subprocess.run(
        [executable, "-gray", "-png", "-r", "18", str(pdf_path), str(prefix)],
        stdin=subprocess.DEVNULL,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.PIPE,
        check=False,
        timeout=120,
    )
    if completed.returncode != 0:
        raise ValueError("pdf_cross_renderer_failed")
    pages = sorted(prefix.parent.glob(f"{prefix.name}-*.png"))
    if not pages:
        raise ValueError("pdf_cross_renderer_returned_no_pages")
    return pages


def _rendered_page_ink_ratio(path: Path) -> float:
    with Image.open(path) as image:
        grayscale = image.convert("L")
        pixels = grayscale.tobytes()
    return sum(value < 245 for value in pixels) / max(1, len(pixels))
