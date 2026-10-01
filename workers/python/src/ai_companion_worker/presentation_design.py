"""Versioned, renderer-independent presentation documents and deterministic layouts.

Coordinates and font sizes are in points on a 960 x 540 canvas. The browser and
PPTX writer consume the same scene; neither makes independent layout decisions.
Only source content is planned here. Facts and chart values are never invented.
"""
from __future__ import annotations

import copy
import hashlib
import math
import re
import unicodedata
from typing import Any

VERSION = "presentation-design-v2"
MAX_SLIDES = 60
THEMES: dict[str, dict[str, str]] = {
    "companion": {"name": "伴AI · 温暖陪伴", "background": "FFFAF3", "ink": "553E3A", "accent": "A35082", "muted": "7C6864", "surface": "F1EAF6", "heading_font": "Noto Sans CJK SC", "body_font": "Noto Sans CJK SC"},
    "business": {"name": "远山 · 商务", "background": "F6F7FB", "ink": "172B4D", "accent": "305BD6", "muted": "52627D", "surface": "E5EAF6", "heading_font": "Noto Sans CJK SC", "body_font": "Noto Sans CJK SC"},
    "technology": {"name": "深空 · 科技", "background": "111827", "ink": "F3F7FF", "accent": "72DCD1", "muted": "B2BED1", "surface": "223149", "heading_font": "Noto Sans CJK SC", "body_font": "Noto Sans CJK SC"},
    "editorial": {"name": "纸间 · 人文", "background": "FAF6EE", "ink": "302D27", "accent": "A64C33", "muted": "6F675B", "surface": "EBE3D5", "heading_font": "Noto Serif CJK SC", "body_font": "Noto Sans CJK SC"},
}
LAYOUTS = {
    "capability-map": "分层架构图", "cover": "主标题", "cover-split": "分栏封面", "section": "章节页",
    "statement": "重点陈述", "bullets": "清晰列表", "columns": "双栏展开",
    "comparison": "方案对比", "steps": "步骤流程", "timeline": "时间轴",
    "metrics": "关键数字", "quote": "引用", "image-right": "右侧配图",
    "image-left": "左侧配图", "image-top": "上图下文", "gallery": "图片展板",
    "chart-bar": "条形图", "chart-column": "柱状图", "chart-line": "折线图",
    "table": "数据表格", "closing": "结尾", "agenda": "目录", "matrix": "四象限",
}
ANIMATIONS = {"none", "subtle", "explain", "showcase"}
FONTS = {"Noto Sans CJK SC", "Noto Serif CJK SC", "WenQuanYi Zen Hei"}


def catalog() -> dict[str, Any]:
    return {"themes": THEMES, "layouts": LAYOUTS, "fonts": sorted(FONTS)}


def _string(value: Any, limit: int, field: str, *, empty: bool = True) -> str:
    if not isinstance(value, str) or len(value) > limit or (not empty and not value.strip()) or re.search(r"[\x00-\x08\x0b\x0c\x0e-\x1f]", value):
        raise ValueError(f"presentation_{field}_invalid")
    return value.strip()


def _identifier(value: Any) -> str:
    if not isinstance(value, str) or not re.fullmatch(r"[a-zA-Z0-9_-]{1,80}", value):
        raise ValueError("presentation_object_id_invalid")
    return value


def _number(value: Any, low: float, high: float) -> float:
    if isinstance(value, bool) or not isinstance(value, (int, float)) or not math.isfinite(value) or not low <= value <= high:
        raise ValueError("presentation_number_out_of_range")
    return float(value)


def _color(value: Any) -> str:
    if not isinstance(value, str) or not re.fullmatch(r"[0-9a-fA-F]{6}", value):
        raise ValueError("presentation_color_invalid")
    return value.upper()


def theme_for(deck: dict[str, Any]) -> dict[str, str]:
    theme = dict(THEMES[deck["theme_id"]])
    theme.update(deck.get("brand", {}))
    return theme


def validate_deck(value: Any) -> dict[str, Any]:
    if not isinstance(value, dict) or value.get("version") != VERSION:
        raise ValueError("presentation_design_version_invalid")
    deck: dict[str, Any] = {"version": VERSION, "id": _identifier(value.get("id")), "title": _string(value.get("title"), 200, "title", empty=False), "audience": _string(value.get("audience", ""), 200, "audience")}
    deck["theme_id"] = value.get("theme_id", "business")
    if deck["theme_id"] not in THEMES:
        raise ValueError("presentation_theme_invalid")
    deck["mode"] = value.get("mode", "present")
    if deck["mode"] not in {"present", "read", "teach"}:
        raise ValueError("presentation_mode_invalid")
    deck["animation"] = value.get("animation", "subtle")
    if deck["animation"] not in ANIMATIONS:
        raise ValueError("presentation_animation_invalid")
    deck["revision"] = int(_number(value.get("revision", 1), 1, 1_000_000))
    deck["brand"] = {}
    brand = value.get("brand", {})
    if not isinstance(brand, dict):
        raise ValueError("presentation_brand_invalid")
    for key in ("accent", "background", "ink", "muted", "surface"):
        if key in brand:
            deck["brand"][key] = _color(brand[key])
    for key in ("heading_font", "body_font"):
        if key in brand:
            if brand[key] not in FONTS:
                raise ValueError("presentation_font_invalid")
            deck["brand"][key] = brand[key]
    typography = value.get("typography", {})
    if not isinstance(typography, dict):
        raise ValueError("presentation_typography_invalid")
    deck["typography"] = {key: _number(typography.get(key, default), low, high) for key, default, low, high in (("title_size", 30, 26, 44), ("body_size", 18 if deck['mode'] == 'read' else 21, 16, 26), ("radius", 12, 0, 24))}
    profile = value.get("style_profile")
    if profile is not None:
        if not isinstance(profile, dict):
            raise ValueError("presentation_style_profile_invalid")
        deck["style_profile"] = {key: _string(profile.get(key, ""), limit, "style_" + key) for key, limit in (("name", 100), ("rationale", 800), ("prompt", 2000))}
        if "storyline" in profile:
            deck["style_profile"]["storyline"] = _string(profile["storyline"], 600, "storyline")
    raw_slides = value.get("slides")
    if not isinstance(raw_slides, list) or not 1 <= len(raw_slides) <= MAX_SLIDES:
        raise ValueError("presentation_slide_limit_exceeded")
    slides = []
    identifiers: set[str] = set()
    total_chars = 0
    for raw in raw_slides:
        if not isinstance(raw, dict):
            raise ValueError("presentation_slide_invalid")
        slide: dict[str, Any] = {"id": _identifier(raw.get("id")), "title": _string(raw.get("title"), 200, "slide_title", empty=False), "layout": raw.get("layout", "bullets"), "locked": raw.get("locked") is True}
        if slide["id"] in identifiers or slide["layout"] not in LAYOUTS:
            raise ValueError("presentation_layout_or_id_invalid")
        identifiers.add(slide["id"])
        items = raw.get("items", [])
        if not isinstance(items, list) or len(items) > 100:
            raise ValueError("presentation_items_invalid")
        slide["items"] = []
        item_ids: set[str] = set()
        for index, item in enumerate(items):
            if not isinstance(item, dict):
                raise ValueError("presentation_item_invalid")
            item_id = _identifier(item.get("id", f"item-{index}"))
            if item_id in item_ids:
                raise ValueError("presentation_duplicate_item_id")
            item_ids.add(item_id)
            slide["items"].append({"id": item_id, "text": _string(item.get("text", ""), 10000, "item"), "source": _string(item.get("source", ""), 1000, "source")})
        if slide['layout'] == 'capability-map':
            from ai_companion_worker.presentation_diagram import validate_regions
            slide['regions'] = validate_regions(raw.get('regions'), slide['items'])
        slide["notes"] = _string(raw.get("notes", ""), 10000, "notes")
        if "purpose" in raw:
            slide["purpose"] = _string(raw["purpose"], 160, "page_purpose")
        slide["source"] = _string(raw.get("source", ""), 2000, "source")
        slide["image_id"] = _identifier(raw["image_id"]) if raw.get("image_id") else ""
        slide["image_fit"] = raw.get("image_fit", "contain")
        if slide["image_fit"] not in {"contain", "cover"}:
            raise ValueError("presentation_image_fit_invalid")
        slide["animation"] = raw.get("animation", "inherit")
        if slide["animation"] not in ANIMATIONS | {"inherit"}:
            raise ValueError("presentation_animation_invalid")
        slide["duration_ms"] = int(_number(raw.get("duration_ms", 450), 100, 2000))
        slide["advance"] = raw.get("advance", "click")
        if slide["advance"] not in {"click", "auto"}:
            raise ValueError("presentation_advance_invalid")
        slide["overrides"] = {}
        overrides = raw.get("overrides", {})
        if not isinstance(overrides, dict) or len(overrides) > 160:
            raise ValueError("presentation_overrides_invalid")
        for node_id, fields in overrides.items():
            _identifier(node_id)
            if not isinstance(fields, dict):
                raise ValueError("presentation_override_invalid")
            clean: dict[str, Any] = {}
            for key, high in (("x", 960), ("y", 540), ("w", 960), ("h", 540), ("font_size", 72)):
                if key in fields:
                    clean[key] = _number(fields[key], 10 if key == "font_size" else 0, high)
            for key in ("color", "fill"):
                if key in fields:
                    clean[key] = _color(fields[key])
            clean["locked"] = fields.get("locked") is True
            slide["overrides"][node_id] = clean
        chart = raw.get("chart")
        if chart is not None:
            if not isinstance(chart, dict):
                raise ValueError("presentation_chart_invalid")
            labels, values = chart.get("labels"), chart.get("values")
            if not isinstance(labels, list) or not isinstance(values, list) or not 1 <= len(labels) <= 12 or len(labels) != len(values):
                raise ValueError("presentation_chart_data_invalid")
            slide["chart"] = {"labels": [_string(x, 80, "chart_label", empty=False) for x in labels], "values": [_number(x, -1e12, 1e12) for x in values], "series": _string(chart.get("series", "数值"), 100, "chart_series"), "unit": _string(chart.get("unit", ""), 20, "chart_unit")}
        table = raw.get("table")
        if table is not None:
            if not isinstance(table, dict) or not isinstance(table.get("columns"), list) or not 1 <= len(table["columns"]) <= 8 or not isinstance(table.get("rows"), list) or not 1 <= len(table["rows"]) <= 12:
                raise ValueError("presentation_table_invalid")
            columns = [_string(x, 100, "column") for x in table["columns"]]
            rows = []
            for row in table["rows"]:
                if not isinstance(row, list) or len(row) != len(columns):
                    raise ValueError("presentation_table_row_invalid")
                rows.append([_string(x, 3000, "cell") for x in row])
            slide["table"] = {"columns": columns, "rows": rows}
        total_chars += len(slide["title"]) + len(slide["notes"]) + sum(len(i["text"]) for i in slide["items"])
        if "table" in slide:
            total_chars += sum(len(cell) for row in slide["table"]["rows"] for cell in row)
        slides.append(slide)
    if total_chars > 150000:
        raise ValueError("presentation_content_limit_exceeded")
    deck["slides"] = slides
    # Assets are normalized and checked by the image decoder at the boundary.
    assets = value.get("assets", {})
    if not isinstance(assets, dict) or len(assets) > 30:
        raise ValueError("presentation_assets_invalid")
    deck["assets"] = {}
    total_asset_bytes = 0
    for key, asset in assets.items():
        _identifier(key)
        if not isinstance(asset, dict) or asset.get("media_type") not in {"image/png", "image/jpeg"}:
            raise ValueError("presentation_asset_type_invalid")
        data = _string(asset.get("data_base64"), 3_000_000, "asset_data", empty=False)
        total_asset_bytes += len(data)
        deck["assets"][key] = {"media_type": asset["media_type"], "data_base64": data, "source": _string(asset.get("source", ""), 2000, "asset_source")}
        if asset.get("kind") == "source":
            deck["assets"][key].update(kind="source", source_page=int(_number(asset.get("source_page"), 1, 10000)))
    if total_asset_bytes > 20_000_000 or any(s["image_id"] and s["image_id"] not in deck["assets"] for s in slides):
        raise ValueError("presentation_asset_missing_or_too_large")
    return deck


def _units(text: str) -> float:
    return sum(1.0 if unicodedata.east_asian_width(c) in {"W", "F"} else (0.3 if c.isspace() else 0.6) for c in text)


def wrap_text(text: str, width: float, size: float) -> list[str]:
    """Conservative explicit line breaks, without deleting any characters."""
    capacity = max(1.0, width / size - 0.4)
    result = []
    for paragraph in text.split("\n"):
        line = ""
        for char in paragraph:
            if line and _units(line + char) > capacity and char not in "，。！？；：、）》】”’.,!?;:)]}":
                result.append(line)
                line = ""
            line += char
        result.append(line)
    return result or [""]


def table_geometry(table: dict[str, Any], width: float, height: float) -> dict[str, Any]:
    """One measured cell grid shared by the preview, gate and Office renderer."""
    rows = [table["columns"], *table["rows"]]
    cell_width = width / len(table["columns"])
    size = 15
    while True:
        lines = [[wrap_text(cell, cell_width - 18, size) for cell in row] for row in rows]
        heights = [max(len(cell) for cell in row) * size * 1.3 + 18 for row in lines]
        if sum(heights) <= height + 1 or size <= 12:
            return {"font_size": size, "row_heights": heights, "cell_lines": lines, "fits": sum(heights) <= height + 1}
        size -= 1


def _chunks(text: str, limit: int = 140) -> list[str]:
    if len(text) <= limit:
        return [text]
    chunks = []
    while len(text) > limit:
        cut = max(text.rfind(p, limit // 2, limit) + 1 for p in "。；，、.!?; ")
        cut = cut or limit
        chunks.append(text[:cut])
        text = text[cut:]
    if text:
        chunks.append(text)
    return chunks


def choose_layout(title: str, items: list[dict[str, str]], index: int) -> str:
    if re.search(r"流程|步骤|路径|过程", title) and 2 <= len(items) <= 5:
        return "steps"
    if re.search(r"时间|路线|计划|里程碑|阶段", title) and 2 <= len(items) <= 5:
        return "timeline"
    if re.search(r"比较|对比|差异|优劣|方案", title) and 2 <= len(items) <= 4:
        return "comparison"
    if re.search(r"指标|数字|成果|业绩", title) and 1 <= len(items) <= 4 and all(re.match(r"\D{0,15}\d", i["text"]) for i in items):
        return "metrics"
    if len(items) == 1 and len(items[0]["text"]) <= 100:
        return "statement"
    if len(items) <= 4 and sum(len(i["text"]) for i in items) < 180:
        return "columns" if index % 2 else "bullets"
    return "bullets"


def plan_deck(payload: dict[str, Any]) -> dict[str, Any]:
    from ai_companion_worker.presentation_projects import is_companion, project_deck
    if not payload.get('brief') and (payload.get("project_name") or is_companion(payload.get("title"))):
        return project_deck({**payload, "project_name": payload.get("project_name") or payload["title"]})
    title = _string(payload.get("title"), 200, "title", empty=False)
    brief = _string(payload.get("brief"), 100000, "brief", empty=False)
    theme_id = payload.get("theme_id") or ("technology" if re.search(r"科技|技术|深色", str(payload.get("style", ""))) else "editorial" if re.search(r"人文|文艺|研究|学术", str(payload.get("style", ""))) else "business")
    sections: list[dict[str, Any]] = []
    module: dict[str, Any] | None = None
    for line in brief.splitlines():
        if not line.strip():
            continue
        if re.match(r"^###\s+", line) and sections:
            module = {"title": re.sub(r"^###\s+", "", line).strip(), "items": []}
            sections[-1].setdefault("modules", []).append(module)
        elif re.match(r"^#{1,3}\s+", line):
            sections.append({"title": re.sub(r"^#{1,3}\s+", "", line).strip(), "items": []})
            module = None
        else:
            if not sections:
                sections.append({"title": title, "items": []})
            text = re.sub(r"^\s*[-*•]\s+", "", line).strip()
            (module if module is not None else sections[-1])["items"].append(text)
    # Nested headings describe modules on ONE content page, not extra slides.
    # The same page can live alongside comparisons, processes and conclusions.
    for section in sections:
        for child in section.get("modules", []):
            section["items"].append(child["title"] + "\n" + "\n".join(child["items"]))
    if payload.get('slide_count') == 1:
        items = [{'id': f'block-{i}', 'text': section['title']+'\n'+'\n'.join(section['items'])} for i, section in enumerate(sections)]
        if len(items) == 1:
            items = [{'id': f'block-{i}', 'text': value} for i, value in enumerate(sections[0]['items'])]
        slide = {'id': 'overview', 'title': title, 'items': items, 'layout': 'capability-map' if len(items) >= 2 else 'statement'}
        return validate_deck({'version': VERSION, 'id': 'deck-'+hashlib.sha256((title+brief).encode()).hexdigest()[:16], 'title': title, 'audience': payload.get('audience', ''), 'theme_id': theme_id, 'mode': payload.get('presentation_mode', 'present'), 'animation': payload.get('animation', 'explain'), 'slides': [slide], 'brand': payload.get('brand', {})})
    # Requested count is a target. Never pad a deck with invented filler.
    target = max(1, int(payload.get("slide_count", 8)) - 2)
    if len(sections) == 1 and len(sections[0]["items"]) > 1 and not sections[0].get("modules"):
        all_items = sections[0]["items"]
        stride = max(1, math.ceil(len(all_items) / target))
        sections = [{"title": title if i == 0 else f"{title} · {i // stride + 1}", "items": all_items[i:i + stride]} for i in range(0, len(all_items), stride)]
    slides: list[dict[str, Any]] = [{"id": "cover", "title": title, "items": [{"id": "audience", "text": payload.get("audience", "")}], "layout": "cover"}]
    for si, section in enumerate(sections):
        if section.get("modules"):
            if len(section["items"]) != len(section["modules"]):
                raise ValueError("图解页请将所有说明放入 ### 模块，或另设一个 ## 内容页")
            items = [{"id": f"s{si}-module-{ii}", "text": text} for ii, text in enumerate(section["items"])]
            slides.append({"id": f"slide-{si}-0", "title": section["title"], "items": items, "layout": "capability-map"})
            continue
        items = [{"id": f"s{si}-i{ii}-p{pi}", "text": part} for ii, text in enumerate(section["items"]) for pi, part in enumerate(_chunks(text))]
        if not items:
            slides.append({"id": f"section-{si}", "title": section["title"], "layout": "section", "items": []})
            continue
        group: list[dict[str, str]] = []
        groups = []
        for item in items:
            if group and (len(group) >= 5 or sum(len(i["text"]) for i in group) + len(item["text"]) > 330):
                groups.append(group)
                group = []
            group.append(item)
        if group:
            groups.append(group)
        for gi, group in enumerate(groups):
            slides.append({"id": f"slide-{si}-{gi}", "title": section["title"] + (f" · {gi + 1}/{len(groups)}" if len(groups) > 1 else ""), "items": group, "layout": choose_layout(section["title"], group, len(slides))})
    slides.append({"id": "closing", "title": "交流与讨论", "layout": "closing", "items": [{"id": f"recap-{i}", "text": section["title"]} for i, section in enumerate(sections[:5])]})
    return validate_deck({"version": VERSION, "id": "deck-" + hashlib.sha256((title + brief).encode()).hexdigest()[:16], "title": title, "audience": payload.get("audience", ""), "theme_id": theme_id, "mode": payload.get("presentation_mode", "present"), "animation": payload.get("animation", "subtle"), "slides": slides, "brand": payload.get("brand", {})})


def _text(nodes: list[dict[str, Any]], node_id: str, text: str, x: float, y: float, w: float, h: float, size: float, color: str, font: str, *, bold: bool = False, group: int = 0) -> None:
    nodes.append({"id": node_id, "type": "text", "text": text, "x": x, "y": y, "w": w, "h": h, "font_size": size, "color": color, "font": font, "bold": bold, "group": group})


def _shape(nodes: list[dict[str, Any]], node_id: str, x: float, y: float, w: float, h: float, fill: str, *, kind: str = "rect", group: int = 0) -> None:
    nodes.append({"id": node_id, "type": "shape", "x": x, "y": y, "w": w, "h": h, "fill": fill, "kind": kind, "group": group})


def scene_for_slide(slide: dict[str, Any], deck: dict[str, Any], index: int) -> dict[str, Any]:
    theme = theme_for(deck)
    ink, accent, muted, surface = (theme[k] for k in ("ink", "accent", "muted", "surface"))
    font, heading_font = theme["body_font"], theme["heading_font"]
    layout, items = slide["layout"], slide["items"]
    nodes: list[dict[str, Any]] = []
    body_size = deck.get("typography", {}).get("body_size", 18 if deck["mode"] == "read" else 21)
    warnings: list[str] = []
    x: float
    y: float
    w: float
    h: float

    def text(node_id: str, value: str, x: float, y: float, w: float, h: float, size: float = body_size, color: str = ink, bold: bool = False, group: int = 0) -> None:
        _text(nodes, node_id, value, x, y, w, h, size, color, heading_font if node_id == "title" else font, bold=bold, group=group)

    if layout == "capability-map":
        from ai_companion_worker.presentation_diagram import capability_scene
        nodes.extend(capability_scene(slide, deck, theme))
    elif layout in {"cover", "cover-split", "section", "closing"}:
        if layout == "cover-split":
            _shape(nodes, "color-field", 630, 0, 330, 540, accent)
            title_w = 536
        else:
            title_w = 832
        _shape(nodes, "accent-rule", 64, 130, 76, 5, accent)
        text("title", slide["title"], 64, 146, title_w, 194, deck['typography']['title_size'] * 44 / 30, ink, True)
        text("subtitle", "\n".join(i["text"] for i in items), 68, 352, title_w, 118, 18, muted, group=1)
    else:
        _shape(nodes, "accent-rule", 48, 43, 5, 37, accent)
        text("title", slide["title"], 68, 39, 840, 75, deck.get("typography", {}).get("title_size", 30), ink, True)
        y0, bottom = 137.0, 470.0
        if layout in {"chart-bar", "chart-column", "chart-line"} and slide.get("chart"):
            nodes.append({"id": "chart", "type": "chart", "x": 64, "y": 143, "w": 610, "h": 305, "chart": slide["chart"], "chart_type": layout.removeprefix("chart-"), "color": accent, "font": font, "group": 1})
            text("chart-comment", "\n".join(i["text"] for i in items), 707, 157, 200, 290, 17, ink, group=2)
        elif layout == "table" and slide.get("table"):
            table_y = 205 if items else 135
            if items:
                text("table-comment", "\n".join(i["text"] for i in items), 64, 125, 832, 70, 17, ink, group=1)
            nodes.append({"id": "table", "type": "table", "x": 56, "y": table_y, "w": 848, "h": 475 - table_y, "table": slide["table"], "font": font, "color": ink, "fill": surface, "accent": accent, "header_color": theme["background"], "group": 2 if items else 1})
        elif layout in {"statement", "quote"}:
            if layout == "quote":
                text("quote-mark", "“", 64, 128, 84, 72, 54, accent)
            value = "\n".join(i["text"] for i in items)
            text("statement", value, 98, 211 if layout == "quote" else 152, 760, 250 if layout == "quote" else 280, 32 if len(value) < 100 else 24, ink, group=1)
        elif layout in {"steps", "timeline", "metrics", "matrix"} and 1 <= len(items) <= (4 if layout in {"metrics", "matrix"} else 5):
            count = len(items)
            if layout == "matrix":
                _shape(nodes, "axis-v", 479, 137, 1, 327, surface)
                _shape(nodes, "axis-h", 64, 299, 832, 1, surface)
            for i, item in enumerate(items):
                if layout == "matrix":
                    x, y, w, h = 70 + i % 2 * 420, 148 + i // 2 * 164, 370, 133
                else:
                    w = 832 / count - 22
                    x, y, h = 64 + i * (832 / count), 164, 276
                if layout == "timeline":
                    _shape(nodes, f"rail-{i}", x, 284, w + 22, 2, surface, group=i + 1)
                    _shape(nodes, f"point-{i}", x + 7, 278, 14, 14, accent, kind="ellipse", group=i + 1)
                    text(f"number-{i}", str(i + 1).zfill(2), x, 247 if i % 2 else 306, w, 30, 18, accent, True, i + 1)
                    text(item["id"], item["text"], x, 153 if i % 2 == 0 else 350, w, 115, 19, ink, group=i + 1)
                elif layout == "steps":
                    _shape(nodes, f"step-{i}", x, y, 34, 34, accent, kind="ellipse", group=i + 1)
                    text(f"number-{i}", str(i + 1).zfill(2), x + 5, y + 5, 27, 26, 17, theme["background"], True, i + 1)
                    if i < count - 1:
                        _shape(nodes, f"connector-{i}", x + 42, y + 16, w - 17, 2, surface, group=i + 1)
                    text(item["id"], item["text"], x, y + 55, w, h - 55, 19, ink, group=i + 1)
                elif layout == "metrics":
                    match = re.search(r"[-+]?\d[\d,.]*(?:%|％|万|亿|倍|人|项|天)?", item["text"])
                    if match:
                        text(f"metric-{i}", match.group(), x, 164, w, 72, 42, accent, True, i + 1)
                        remainder = item["text"][:match.start()] + item["text"][match.end():]
                        text(item["id"], remainder, x, 259, w, 180, 19, ink, group=i + 1)
                    else:
                        text(item["id"], item["text"], x, y, w, h, 22, ink, group=i + 1)
                else:
                    text(item["id"], item["text"], x, y, w, h, 20, ink, group=i + 1)
        else:
            has_image = bool(slide.get("image_id"))
            if layout.startswith("image-") or layout == "gallery":
                if has_image:
                    if layout == "gallery":
                        box = (64, 126, 570, 336)
                    elif layout == "image-top":
                        box = (64, 126, 832, 208)
                        y0 = 353
                    elif layout == "image-left":
                        box = (56, 137, 420, 325)
                    else:
                        box = (488, 137, 416, 325)
                    nodes.append({"id": "image", "type": "image", "x": box[0], "y": box[1], "w": box[2], "h": box[3], "asset_id": slide["image_id"], "fit": slide["image_fit"], "group": 1})
                else:
                    warnings.append("此图文版式尚未配图，已保留全文")
            columns = 2 if layout in {"columns", "comparison"} or (has_image and layout == "image-top") else 1
            if layout == "comparison":
                _shape(nodes, "panel-left", 54, 124, 411, 356, surface)
                _shape(nodes, "panel-right", 486, 124, 421, 356, surface)
            group_size = max(1, math.ceil(len(items) / columns))
            for col in range(columns):
                col_items = items[col * group_size:(col + 1) * group_size]
                width = 390.0 if columns == 2 or (has_image and layout in {"image-left", "image-right"}) else 816.0
                x = 64 + 432 * col
                if has_image and layout == "image-left":
                    x = 516
                elif has_image and layout == "gallery":
                    x, width = 673, 231
                available = bottom - y0
                gap = 18.0
                size = body_size
                while size > 16 and sum(len(wrap_text(i["text"], width - 26, size)) * size * 1.3 + gap for i in col_items) > available:
                    size -= 1
                y = y0
                for n, item in enumerate(col_items):
                    height = len(wrap_text(item["text"], width - 26, size)) * size * 1.3 + 3
                    if layout == "agenda":
                        text(f"number-{n}", str(n + 1).zfill(2), x, y, 40, height, size, accent, True, n + 1)
                    else:
                        _shape(nodes, f"marker-{item['id']}", x, y + size * .48, 5, 5, accent, kind="ellipse", group=n + 1)
                    text(item["id"], item["text"], x + (58 if layout == "agenda" else 20), y, width - (64 if layout == "agenda" else 26), height, size, ink, group=n + 1)
                    y += height + gap
    text("page-number", f"{index + 1:02d}", 880, 506, 36, 18, 11, muted)
    if slide.get("source"):
        text("source", slide["source"], 64, 497, 774, 32, 10, muted)
    for node in nodes:
        node.update(slide.get("overrides", {}).get(node["id"], {}))
        if node["type"] == "table":
            node.update(table_geometry(node["table"], node["w"], node["h"]))
        if node["type"] == "text":
            # Explicit wrapping makes preview and native PPTX use identical breaks.
            size = node["font_size"]
            minimum = 24 if node["id"] == "title" else 10 if node["id"] in {"source", "page-number"} else 16
            lines = wrap_text(node["text"], node["w"], size)
            if not node.get("locked"):
                while len(lines) * size * 1.3 > node["h"] and size > minimum:
                    size -= 1
                    lines = wrap_text(node["text"], node["w"], size)
            node.update(font_size=size, lines=lines, line_height=round(size * 1.3, 2))
    animation = slide.get("animation", "inherit")
    if animation == "inherit":
        animation = deck["animation"]
    return {"id": slide["id"], "background": theme["background"], "foreground": ink, "nodes": nodes, "animation": animation, "duration_ms": slide["duration_ms"], "advance": slide["advance"], "warnings": warnings}


def layout_deck(deck: dict[str, Any]) -> tuple[list[dict[str, Any]], list[dict[str, Any]]]:
    scenes, violations = [], []
    for index, slide in enumerate(deck["slides"]):
        scene = scene_for_slide(slide, deck, index)
        scenes.append(scene)
        if slide.get("image_id") and not any(n["type"] == "image" for n in scene["nodes"]):
            violations.append({"code": "design_hidden_image", "slide": index + 1, "message": "此版式没有显示附加图片，请选择图文版式或移除图片"})
        for kind in ("chart", "table"):
            if slide.get(kind) and not any(n["type"] == kind for n in scene["nodes"]):
                violations.append({"code": "design_hidden_data", "slide": index + 1, "message": "此版式没有显示已保存的数据，请选择对应图表或表格版式"})
        for node in scene["nodes"]:
            reason = ""
            if node["x"] < 0 or node["y"] < 0 or node["x"] + node["w"] > 961 or node["y"] + node["h"] > 541:
                reason = "对象超出画布"
            if node["type"] == "text" and len(node["lines"]) * node["line_height"] > node["h"] + 2:
                reason = "文字超过可读容量，请拆页或换版式"
            if node["type"] == "table" and not node["fits"]:
                reason = "表格超过可读容量，请减少单页记录或拆分表格"
            if reason:
                violations.append({"code": "design_overflow", "slide": index + 1, "object_id": node["id"], "message": reason})
        # Intentional decorative overlaps are excluded; text pairs must not overlap.
        texts = [n for n in scene["nodes"] if n["type"] == "text" and n["text"]]
        for i, left in enumerate(texts):
            for right in texts[i + 1:]:
                if min(left["x"] + left["w"], right["x"] + right["w"]) - max(left["x"], right["x"]) > 2 and min(left["y"] + left["h"], right["y"] + right["h"]) - max(left["y"], right["y"]) > 2:
                    violations.append({"code": "design_overlap", "slide": index + 1, "object_id": left["id"], "message": "文本对象重叠：" + right["id"]})
    return scenes, violations


def repair_layouts(deck: dict[str, Any]) -> tuple[dict[str, Any], list[str]]:
    """Bounded deterministic repair; locked or manually positioned content is retained."""
    deck = copy.deepcopy(deck)
    repairs = []
    for _ in range(2):
        _, violations = layout_deck(deck)
        failing = {v["slide"] - 1 for v in violations}
        if not failing:
            break
        result = []
        for index, slide in enumerate(deck["slides"]):
            if index not in failing or slide["locked"] or slide["overrides"] or slide.get("chart") or slide["layout"] == "capability-map":
                result.append(slide)
                continue
            remaining = len(deck["slides"]) - index - 1
            can_split = len(result) + remaining + 2 <= MAX_SLIDES
            if slide.get("table"):
                if slide["layout"] == "table" and len(slide["table"]["rows"]) > 1 and can_split:
                    midpoint = math.ceil(len(slide["table"]["rows"]) / 2)
                    second = copy.deepcopy(slide)
                    second["id"] = f"{slide['id'][:54]}-part-{index}-{len(result)}"
                    second["table"]["rows"] = slide["table"]["rows"][midpoint:]
                    slide["table"]["rows"] = slide["table"]["rows"][:midpoint]
                    result.extend([slide, second])
                    repairs.append(f"第 {index + 1} 页表格已拆页，保留全部记录")
                else:
                    result.append(slide)
            elif slide.get("image_id") and can_split and slide["items"]:
                picture = copy.deepcopy(slide)
                picture.update(id=f"{slide['id'][:54]}-image-{index}-{len(result)}", layout="gallery", items=[])
                slide.update(layout="bullets", image_id="")
                result.extend([slide, picture])
                repairs.append(f"第 {index + 1} 页图文已分开，原图与全文均保留")
            elif slide["layout"] not in {"bullets", "cover", "section", "closing"}:
                slide["layout"] = "bullets"
                repairs.append(f"第 {index + 1} 页已改用宽版文字布局")
                result.append(slide)
            elif len(slide["items"]) > 1 and can_split:
                midpoint = math.ceil(len(slide["items"]) / 2)
                second = copy.deepcopy(slide)
                second["id"] = f"{slide['id'][:54]}-part-{index}-{len(result)}"
                second["items"] = slide["items"][midpoint:]
                slide["items"] = slide["items"][:midpoint]
                result.extend([slide, second])
                repairs.append(f"第 {index + 1} 页已拆页以保留全文")
            else:
                result.append(slide)
        deck["slides"] = result
    return validate_deck(deck), repairs
