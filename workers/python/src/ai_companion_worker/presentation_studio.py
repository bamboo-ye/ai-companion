"""Office skill adapter for the presentation design, preview and export workflow."""
from __future__ import annotations

import base64
import copy
from typing import Any

from ai_companion_worker.presentation_design import (
    VERSION, catalog, layout_deck, plan_deck, repair_layouts, validate_deck,
)
from ai_companion_worker.presentation_render import (
    design_file, export_pptx, normalize_assets, render_pptx,
)


def generate(payload: dict[str, Any], *, include_file: bool) -> dict[str, Any]:
    # Keep the established source and task-contract gates at the same boundary.
    from ai_companion_worker import office_tools as office

    requested = payload.get("slide_count", 8)
    if isinstance(requested, bool) or not isinstance(requested, int) or not 1 <= requested <= 60:
        raise ValueError("slide_count_must_be_between_1_and_60")
    table = office._presentation_table(payload.get("table"))
    display_rows = office._presentation_display_rows(table["rows"]) if table else []
    coverage = office._presentation_source_coverage(payload.get("source_coverage"))
    contract = payload.get("task_contract")
    contract = contract if isinstance(contract, dict) else {}
    deck = validate_deck(payload["deck_spec"]) if "deck_spec" in payload else plan_deck(payload)
    if table and "deck_spec" not in payload:
        content = []
        for index, page in enumerate(office._presentation_table_pages(display_rows)):
            rows = page["rows"]
            slide = {
                "id": f"table-{index}", "title": table["title"] or deck["title"],
                "source": "；".join(dict.fromkeys(r["source_locator"] for r in rows)),
                "items": [], "layout": "table",
                "table": {"columns": table["columns"], "rows": [r["cells"] for r in rows]},
            }
            if page.get("detail"):
                slide["layout"] = "columns"
                slide["items"] = [{"id": f"field-{i}", "text": f"{column}：{cell}"} for i, (column, cell) in enumerate(zip(table["columns"], rows[0]["cells"], strict=True))]
                del slide["table"]
            content.append(slide)
        deck["slides"] = [deck["slides"][0], *content, {"id": "closing", "title": "内容概览", "layout": "closing", "items": [{"id": "count", "text": f"共整理 {len(table['rows'])} 条记录"}]}]
        deck = validate_deck(deck)
    visual_slides = [s for s in deck["slides"][1:-1] if s["layout"] != "capability-map"]
    topics = [{"title": s["title"], "bullets": [i["text"] for i in s["items"]]} for s in visual_slides]
    if "deck_spec" not in payload and not payload.get("project_name") and visual_slides:
        visuals, visual_report, model_usage = office._presentation_visuals(payload, topics, include_file=include_file or bool(payload.get("source_documents")), allow_generated=include_file)
        additional_pages = []
        for index, visual in enumerate(visuals):
            if visual is None or index >= len(visual_slides):
                continue
            asset_id = f"visual-{index}"
            deck["assets"][asset_id] = {"media_type": visual["media_type"], "data_base64": base64.b64encode(visual["data"]).decode(), "source": visual.get("source_locator", "AI 生成插画")}
            if visual.get("kind") == "source":
                deck["assets"][asset_id].update(kind="source", source_page=visual["source_page"])
            slide = visual_slides[index]
            if slide.get("table"):
                slide = {"id": f"visual-page-{index}", "title": slide["title"], "layout": "gallery", "items": []}
                additional_pages.append(slide)
            slide["image_id"] = asset_id
            if slide["layout"] != "gallery":
                slide["layout"] = "image-right"
            slide["source"] = visual.get("source_locator", "")
        if additional_pages:
            deck["slides"][-1:-1] = additional_pages
        deck = validate_deck(deck)
    else:
        visual_report = {"policy_version": "presentation-visuals-v2", "mode": "design", "placed_visual_count": sum(bool(s.get("image_id")) for s in deck["slides"]), "source_document_count": 0, "generated_visual_count": 0}
        model_usage = {"provider": "none", "cost_micros": 0}
    style_prompt = payload.get("style_prompt", "")
    if style_prompt:
        from ai_companion_worker.presentation_style import design_style
        try:
            deck, style_usage = design_style(deck, style_prompt)
        except office.ModelBackedOperationError as exc:
            usages = [usage for usage in (model_usage, exc.model_usage) if usage.get('provider') != 'none']
            raise office.ModelBackedOperationError(exc.code, office._aggregate_translation_usage(usages, 275_000)) from exc
        usages = [usage for usage in (model_usage, style_usage) if usage.get("provider") != "none"]
        model_usage = office._aggregate_translation_usage(usages, 275_000)
    normalize_assets(deck)
    deck, repairs = repair_layouts(deck)
    # Re-normalization preserves dimensions intentionally omitted by input validation.
    normalize_assets(deck)
    scenes, violations = layout_deck(deck)
    visual_report["placed_visual_count"] = sum(n["type"] == "image" for scene in scenes for n in scene["nodes"])
    outline = []
    for index, slide in enumerate(deck["slides"]):
        bullets = [i["text"] for i in slide["items"]]
        if slide.get("table"):
            bullets.extend(" | ".join(row) for row in slide["table"]["rows"])
        outline.append({"page": index + 1, "title": slide["title"], "bullets": bullets, "layout": slide["layout"]})
        asset = deck["assets"].get(slide.get("image_id"))
        if asset and asset.get("kind") == "source":
            cited = office._presentation_topic_source_pages({"title": slide["title"], "bullets": [*bullets, slide["source"]]})
            if asset["source_page"] not in cited:
                violations.append({"code": "source_visual_topic_mismatch", "slide": index + 1, "message": "原图的页码与页面来源说明不一致，请核对引用"})
    if table and "deck_spec" in payload:
        visible = "\n".join([s["title"] + "\n" + "\n".join(s["bullets"]) for s in outline])
        for row in table["rows"]:
            if any(cell and cell not in visible for cell in row["cells"]):
                violations.append({"code": "design_source_record_missing", "message": "设计稿中缺少原始表格字段，请恢复后导出"})
                break
    # v2 titles are measured and wrapped by the design gate, unlike v1's single line.
    gate_outline = copy.deepcopy(outline)
    for slide in gate_outline:
        slide["title"] = slide["title"][:36]
    quality = office._presentation_quality_report(
        outline=gate_outline, style=str(payload.get("style", "")), table=table,
        task_contract=contract, source_coverage=coverage, display_rows=display_rows,
        layout_violations=violations, visual_report=visual_report,
        filename=payload.get("filename"), brief=str(payload.get("brief", "")), requested_slide_count=requested,
        validated_design="deck_spec" in payload or bool(payload.get("project_name") and not payload.get("brief")),
    )
    quality.update(policy_version="presentation-quality-v3", repairs=repairs)
    render_report: dict[str, Any] = {"status": "not_requested", "passed": False}
    files = []
    previews: list[str] = []
    if include_file and quality["passed"]:
        filename = office._pptx_output_name(payload.get("filename"), deck["title"])
        try:
            data = export_pptx(deck, scenes)
            render_report, pdf, previews = render_pptx(data, len(deck["slides"]))
        except Exception as exc:
            if model_usage.get("cost_micros", 0):
                raise office.ModelBackedOperationError("演示导出失败，请从已保存的设计稿重试", model_usage) from exc
            raise
        if render_report["status"] in {"verified", "failed"} and not render_report["passed"]:
            quality["violations"].extend(render_report.get("violations", [{"code": "render_failed", "message": render_report.get("message", "实际渲染失败")}]))
            quality["passed"] = False
        if quality["passed"]:
            files = [office._file(filename, "application/vnd.openxmlformats-officedocument.presentationml.presentation", data), office._file(filename[:-5] + ".deck.json", "application/json", design_file(deck))]
            if pdf is not None:
                files.append(office._file(filename[:-5] + ".pdf", "application/pdf", pdf))
            if len(deck["slides"]) == 1 and deck["slides"][0]["layout"] == "capability-map" and previews and previews[0].startswith("data:image/png;base64,"):
                files.append(office._file(filename[:-5] + ".png", "image/png", base64.b64decode(previews[0].split(",", 1)[1])))
    return {"output": {
        "title": deck["title"], "audience": deck["audience"], "style": str(payload.get("style", deck["theme_id"])),
        "slide_count": len(deck["slides"]), "requested_slide_count": requested, "outline": outline,
        "source_coverage": coverage, "visual_report": visual_report, "model_usage": model_usage,
        "quality_report": quality, "source_overwritten": False,
        "design_version": VERSION, "deck_spec": deck, "scenes": scenes, "catalog": catalog(),
        "render_report": render_report, "rendered_previews": previews,
        "warnings": ([f"为保留内容，实际生成 {len(deck['slides'])} 页，目标为 {requested} 页"] if len(deck["slides"]) != requested else []) + ([render_report["message"]] if render_report.get("message") else []),
    }, "files": files}
