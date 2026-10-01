"""Original starter decks and page presets, rendered by the production layout engine.

Regenerate the browser catalog with:
python -m ai_companion_worker.presentation_library web/public/presentation-library.json
"""
from __future__ import annotations

import copy
import json
import sys
from pathlib import Path
from typing import Any

from ai_companion_worker.presentation_design import LAYOUTS, VERSION, THEMES, layout_deck, validate_deck


def page(layout: str, title: str, *items: str, **extra: Any) -> dict[str, Any]:
    return {"id": layout, "layout": layout, "title": title,
            "items": [{"id": f"item-{i}", "text": text} for i, text in enumerate(items)], **extra}


def design(title: str, theme: str, slides: list[dict[str, Any]]) -> dict[str, Any]:
    slides = copy.deepcopy(slides)
    for i, slide in enumerate(slides):
        slide["id"] = f"page-{i}"
    deck = validate_deck({"version": VERSION, "id": "library", "title": title,
                          "audience": "项目团队" if theme != "editorial" else "课程学员",
                          "theme_id": theme, "animation": "explain" if theme == "editorial" else "subtle",
                          "slides": slides})
    scenes, violations = layout_deck(deck)
    if violations:
        raise ValueError(violations)
    return {"deck": deck, "scenes": scenes}


def build_library() -> dict[str, Any]:
    presets = [
        ("开场与收尾", "建立主题与演示背景", page("cover", "演示标题", "副标题 · 汇报人 · 日期")),
        ("开场与收尾", "标题与品牌色形成对比", page("cover-split", "让主题\n清晰呈现", "副标题 · 汇报人 · 日期")),
        ("开场与收尾", "提示叙事进入新章节", page("section", "章节标题", "用一句话说明本节重点")),
        ("开场与收尾", "说明听众将看到什么", page("agenda", "内容安排", "背景与目标", "方案与依据", "执行与下一步")),
        ("开场与收尾", "回顾重点并明确下一步", page("closing", "交流与下一步", "需要确认的事项", "负责人及时间安排")),
        ("观点与对比", "突出一个清晰结论", page("statement", "核心观点", "用一句有依据的话，说明最重要的结论。")),
        ("观点与对比", "组织多个独立要点", page("bullets", "关键要点", "写下第一项重点及其依据", "写下第二项重点及其影响", "写下下一步行动")),
        ("观点与对比", "并列展示两组内容", page("columns", "两个观察角度", "观察一：背景与现状", "补充相关事实与依据", "观察二：变化与影响", "说明对应行动")),
        ("观点与对比", "按相同维度比较方案", page("comparison", "方案对比", "方案一：适用场景", "成本与约束：待填写", "方案二：适用场景", "成本与约束：待填写")),
        ("观点与对比", "引用原话并保留出处", page("quote", "观点引用", "在这里填写引用原文，并核对出处。", source="引用来源：待填写")),
        ("观点与对比", "用四个角度组织思考", page("matrix", "四个观察角度", "已有优势\n填写依据", "现有不足\n填写依据", "外部机会\n填写依据", "外部风险\n填写依据")),
        ("流程与计划", "按顺序讲解操作步骤", page("steps", "实施步骤", "明确目标", "制定方案", "执行与检查", "复盘改进")),
        ("流程与计划", "展示阶段与里程碑", page("timeline", "实施计划", "阶段一\n目标与交付物", "阶段二\n目标与交付物", "阶段三\n目标与交付物")),
        ("数据与证据", "突出有来源的关键指标", page("metrics", "关键指标", "指标一\n数值待填写", "指标二\n数值待填写", "指标三\n数值待填写")),
        ("数据与证据", "比较不同类别的数量", page("chart-bar", "分类比较（示例）", "示例数据，请替换为真实数据", chart={"labels": ["类别甲", "类别乙", "类别丙"], "values": [12, 24, 18], "series": "示例数据", "unit": ""})),
        ("数据与证据", "比较各阶段的结果", page("chart-column", "阶段比较（示例）", "示例数据，请替换为真实数据", chart={"labels": ["阶段一", "阶段二", "阶段三"], "values": [12, 24, 18], "series": "示例数据", "unit": ""})),
        ("数据与证据", "展示数值随时间的变化", page("chart-line", "趋势变化（示例）", "示例数据，请替换为真实数据", chart={"labels": ["时间一", "时间二", "时间三"], "values": [12, 24, 18], "series": "示例数据", "unit": ""})),
        ("数据与证据", "保留结构化记录与字段", page("table", "任务安排", table={"columns": ["事项", "负责人", "时间"], "rows": [["待填写", "待填写", "待填写"], ["待填写", "待填写", "待填写"]]})),
    ]
    templates = [
        ("review", "季度复盘", "商务汇报", "用事实梳理进展，明确问题与下一步。", "business", [
            page("cover", "季度复盘", "汇报周期：待填写", "汇报人：待填写"),
            page("agenda", "本次汇报", "目标与进展", "重点工作", "问题与风险", "下一步计划"),
            page("metrics", "目标与进展", "目标一\n结果待填写", "目标二\n结果待填写", "目标三\n结果待填写"),
            page("columns", "重点工作", "工作一：待填写", "成果与依据：待填写", "工作二：待填写", "成果与依据：待填写"),
            page("comparison", "问题与应对", "问题与影响：待填写", "相关事实：待填写", "应对措施：待填写", "负责人及时间：待填写"),
            page("timeline", "下一步计划", "近期\n目标与交付物", "中期\n目标与交付物", "后续\n目标与交付物"),
            page("closing", "需要确认的事项", "决策事项：待填写", "资源支持：待填写"),
        ]),
        ("proposal", "项目提案", "方案提案", "从问题、方案到实施，完整说明决策依据。", "technology", [
            page("cover-split", "项目提案", "项目名称：待填写", "提案人：待填写"),
            page("statement", "项目目标", "填写希望解决的问题与可衡量的目标。"),
            page("columns", "现状与机会", "现状：待填写", "事实与来源：待填写", "机会：待填写", "预期影响：待填写"),
            page("comparison", "方案选择", "方案一：待填写", "成本与约束：待填写", "方案二：待填写", "成本与约束：待填写"),
            page("steps", "实施路径", "需求确认", "方案验证", "分步实施", "验收复盘"),
            page("table", "资源与责任", table={"columns": ["工作项", "负责人", "时间", "资源"], "rows": [["待填写"] * 4, ["待填写"] * 4]}),
            page("closing", "决策与下一步", "需要批准的事项：待填写", "启动时间：待填写"),
        ]),
        ("lesson", "课程讲解", "教学培训", "按概念、示例、练习组织一节完整课程。", "editorial", [
            page("cover", "课程讲解", "课程名称：待填写", "讲师：待填写"),
            page("bullets", "学习目标", "能够解释：待填写", "能够完成：待填写", "能够判断：待填写"),
            page("statement", "核心概念", "填写概念定义，并说明适用条件。"),
            page("comparison", "示例与分析", "示例情境：待填写", "观察结果：待填写", "分析依据：待填写", "适用边界：待填写"),
            page("steps", "操作步骤", "准备材料", "完成操作", "检查结果", "分析原因"),
            page("quote", "课堂练习", "请结合课程内容，分析一个真实问题。"),
            page("closing", "回顾与延伸", "今天的重点：待填写", "课后任务：待填写"),
        ]),
    ]
    return {"version": 1, "templates": [
        {"id": key, "title": title, "category": category, "description": description, **design(title, theme, slides)}
        for key, title, category, description, theme, slides in templates
    ], "pages": [{"id": slide["layout"], "title": LAYOUTS[slide["layout"]], "category": category,
                  "description": description, "variants": {theme: design(slide["title"], theme, [slide]) for theme in THEMES}}
                 for category, description, slide in presets]}


if __name__ == "__main__":
    Path(sys.argv[1]).write_text(json.dumps(build_library(), ensure_ascii=False, separators=(",", ":")) + "\n", encoding="utf-8")
