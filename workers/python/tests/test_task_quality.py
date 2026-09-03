from __future__ import annotations

import unittest

from ai_companion_worker.task_quality import (
    artifact_observation_applicable,
    compile_task_contract,
    localize_presentation_field_value,
    normalize_presentation_name_translation,
    presentation_source_record_keys,
    select_presentation_temporal_source_values,
    validate_artifact_observation,
    validate_presentation_arguments,
)


class TaskQualityTests(unittest.TestCase):
    def test_localize_time_repairs_split_weekdays_and_removes_capacity(self) -> None:
        value = localize_presentation_field_value(
            "time",
            "14/9 (Mo n) 1600-1650 12；9/9 (We d) 1100-1150；10/9 (T hu) 0900-0950",
            "zh-CN",
        )
        self.assertEqual(
            value,
            "14/9 (周一) 1600-1650；9/9 (周三) 1100-1150；10/9 (周四) 0900-0950",
        )

    def test_temporal_source_values_drop_stale_adjacent_column_noise(self) -> None:
        self.assertEqual(
            select_presentation_temporal_source_values(
                ["9/9, 16/9 (Wed)", "hu)", "0930-1120 10"]
            ),
            ["9/9, 16/9 (Wed)", "0930-1120 10"],
        )
        self.assertEqual(
            select_presentation_temporal_source_values(
                ["11/9, 18/9 (Fri)", "(Thu)", "1600-1750 12"]
            ),
            ["11/9, 18/9 (Fri)", "1600-1750 12"],
        )
        self.assertEqual(
            select_presentation_temporal_source_values(
                ["14/9, 21/9 (Mo", "n)", "1500-1550 14"]
            ),
            ["14/9, 21/9 (Mon)", "1500-1550 14"],
        )

    def test_name_translation_normalizes_source_proficiency_level(self) -> None:
        self.assertEqual(
            normalize_presentation_name_translation(
                "太极拳（24式，小学）",
                "Tai Chi Chuan (24 styles) - Ele",
                "zh-CN",
            ),
            "太极拳（24式，初级）",
        )
        self.assertEqual(
            normalize_presentation_name_translation(
                "跑步（提升班）",
                "Running - Improver",
                "zh-CN",
            ),
            "跑步（提高班）",
        )
    def test_compile_contract_locks_exhaustive_ppt_fields(self) -> None:
        contract = compile_task_contract(
            "帮我整理所有体育课的名称、上课时间和课程代码，并用中文PPT展示"
            "\n<!--ai-document:doc-1|courses.pdf-->",
            "work",
        )
        self.assertTrue(contract["exhaustive"])
        self.assertTrue(contract["source_required"])
        self.assertEqual(contract["artifact_types"], ["pptx"])
        self.assertEqual(contract["requested_fields"], ["code", "name", "time"])
        self.assertEqual(contract["output_language"], "zh-CN")

    def test_compile_contract_fails_closed_for_legacy_visible_attachment(self) -> None:
        contract = compile_task_contract(
            "整理所有体育课并生成中文 PPT\n📎 courses.pdf",
            "work",
        )
        self.assertTrue(contract["source_required"])
        self.assertEqual(contract["source_document_ids"], [])

    def test_structured_presentation_arguments_cannot_fall_back_to_brief_only(self) -> None:
        contract = compile_task_contract(
            "整理所有体育课的名称、上课时间和课程代码，并用中文PPT展示",
            "work",
        )
        violations = validate_presentation_arguments(
            {
                "title": "体育课课程安排总览",
                "audience": "学生",
                "style": "清晰、规范",
                "brief": "提取自课程表 PDF",
                "slide_count": 10,
            },
            contract,
        )
        self.assertEqual(
            {item["code"] for item in violations},
            {"structured_table_missing"},
        )

    def test_structured_presentation_arguments_accept_grounded_rows(self) -> None:
        contract = compile_task_contract(
            "整理所有体育课的名称、上课时间和课程代码，并用中文PPT展示",
            "work",
        )
        violations = validate_presentation_arguments(
            {
                "table": {
                    "columns": ["课程代码", "课程名称", "上课时间"],
                    "rows": [
                        {
                            "cells": ["PED1101", "独木舟", "周三 10:00-11:50"],
                            "source_locator": "page:1",
                        }
                    ],
                }
            },
            contract,
        )
        self.assertEqual(violations, [])

    def test_chinese_presentation_rejects_untranslated_names_and_weekdays(self) -> None:
        contract = compile_task_contract(
            "整理所有体育课的名称、上课时间和课程代码，并用中文PPT展示",
            "work",
        )
        violations = validate_presentation_arguments(
            {
                "table": {
                    "columns": ["课程代码", "课程名称", "上课时间"],
                    "rows": [
                        {
                            "cells": ["PED1101", "Canoeing", "Monday 10:00-11:50"],
                            "source_locator": "page:1",
                        },
                        {
                            "cells": ["PED1315", "Tabata训练（初级）", "周一 14:00-14:50"],
                            "source_locator": "page:2",
                        }
                    ],
                }
            },
            contract,
        )
        language_codes = {item["code"] for item in violations}
        self.assertIn("presentation_name_language_mismatch", language_codes)
        self.assertIn("presentation_time_language_mismatch", language_codes)
        name_violation = next(
            item
            for item in violations
            if item["code"] == "presentation_name_language_mismatch"
        )
        self.assertEqual(name_violation["affected_count"], 2)

    def test_chinese_presentation_rejects_orphan_and_conflicting_weekdays(self) -> None:
        contract = compile_task_contract(
            "整理所有体育课的名称、上课时间和课程代码，并用中文PPT展示",
            "work",
        )
        violations = validate_presentation_arguments(
            {
                "table": {
                    "columns": ["课程代码", "课程名称", "上课时间"],
                    "rows": [
                        {
                            "cells": ["PED1402", "高尔夫（初级）", "9/9 (周三) hu 0930-1120"],
                            "source_locator": "page:2",
                        },
                        {
                            "cells": ["PED1406", "木球（初级）", "11/9 (周五) (周四) 1600-1750"],
                            "source_locator": "page:2",
                        },
                    ],
                }
            },
            contract,
        )
        time_violation = next(
            item
            for item in violations
            if item["code"] == "presentation_time_language_mismatch"
        )
        self.assertEqual(time_violation["affected_rows"], [1, 2])

    def test_chinese_presentation_rejects_english_visible_content_without_fields(self) -> None:
        contract = compile_task_contract(
            "提取 Important Dates 信息，并用中文 PPT 展示",
            "work",
        )
        violations = validate_presentation_arguments(
            {
                "title": "Important Dates",
                "table": {
                    "title": "Semester A 2026/27",
                    "columns": ["Month", "Date and Event"],
                    "rows": [
                        {
                            "cells": [
                                "July",
                                "Release of Class Schedule and online add/drop period",
                            ]
                        }
                    ],
                },
            },
            contract,
        )

        self.assertEqual(contract["requested_fields"], [])
        language = next(
            item
            for item in violations
            if item["code"] == "presentation_visible_language_mismatch"
        )
        self.assertGreaterEqual(language["affected_count"], 4)
        self.assertEqual(language["affected_rows"], [1])

    def test_chinese_visible_content_rejects_english_parenthetical_sentences(self) -> None:
        contract = compile_task_contract("用中文 PPT 展示重要日期", "work")
        violations = validate_presentation_arguments(
            {
                "title": "重要日期",
                "table": {
                    "columns": ["日期", "事项"],
                    "rows": [
                        {
                            "cells": [
                                "2026-08-31",
                                "学期开始（Semester begins and tuition is due）",
                            ]
                        }
                    ],
                },
            },
            contract,
        )

        self.assertIn(
            "presentation_visible_language_mismatch",
            {item["code"] for item in violations},
        )

    def test_chinese_visible_content_allows_codes_and_acronyms_in_chinese(self) -> None:
        contract = compile_task_contract("用中文 PPT 展示重要日期", "work")
        violations = validate_presentation_arguments(
            {
                "title": "A学期重要日期",
                "table": {
                    "title": "新生日程",
                    "columns": ["日期", "事项"],
                    "rows": [
                        {
                            "cells": [
                                "2026-08-31",
                                "学生通过AIMS系统办理选课，课程代码PED1101保持不变",
                            ]
                        }
                    ],
                },
            },
            contract,
        )

        self.assertEqual(violations, [])

    def test_simplified_chinese_presentation_rejects_traditional_title_and_filename(self) -> None:
        contract = compile_task_contract("整理注意事项，并生成中文PPT", "work")
        violations = validate_presentation_arguments(
            {
                "title": "錄取須知與重要注意事項",
                "filename": "錄取須知與重要注意事項.pptx",
                "brief": (
                    "## 接受录取与缴费须知\n"
                    "- 请在截止日期前确认录取并缴费。\n"
                    "- 保留付款确认记录。"
                ),
                "slide_count": 3,
            },
            contract,
        )

        language = next(
            item
            for item in violations
            if item["code"] == "presentation_visible_language_mismatch"
        )
        self.assertEqual(language["affected_fields"], ["title", "filename"])

    def test_non_table_presentation_rejects_production_plan_as_audience_content(self) -> None:
        contract = compile_task_contract(
            "对比 CS5187.pdf、CS5297.pdf 和 CS5491.pdf，并用中文 PPT 展示",
            "work",
        )
        violations = validate_presentation_arguments(
            {
                "title": "三门课程对比",
                "audience": "选课学生",
                "style": "简洁",
                "brief": (
                    "本演示文稿基于三份课程文件。\n"
                    "页面结构（共7页）\n"
                    "1. 封面：三门课程对比\n"
                    "2. CS5187 示例摘录，用于对比页"
                ),
                "slide_count": 7,
            },
            contract,
        )

        codes = {item["code"] for item in violations}
        self.assertIn("presentation_audience_meta_content", codes)
        self.assertIn("presentation_brief_structure_invalid", codes)
        self.assertIn("presentation_source_subject_missing", codes)

    def test_non_table_presentation_accepts_canonical_audience_sections(self) -> None:
        contract = compile_task_contract(
            "对比 CS5187.pdf、CS5297.pdf 和 CS5491.pdf，并用中文 PPT 展示",
            "work",
        )
        brief = "\n".join(
            (
                "## CS5187：视觉计算",
                "- 聚焦图像处理、几何分析与视觉应用。",
                "- 适合希望建立计算机视觉基础的学生。",
                "## CS5297：人工智能",
                "- 覆盖人工智能方法、推理与应用。",
                "- 适合希望系统理解智能算法的学生。",
                "## CS5491：人工智能安全",
                "- 关注模型风险、防护与可信部署。",
                "- 适合重视安全治理和可靠性的学生。",
                "## 横向比较",
                "- 三门课分别侧重视觉、通用智能与安全。",
                "- 选择时应结合知识基础和项目方向。",
                "## 选择建议",
                "- 视觉方向优先考虑 CS5187。",
                "- 通用智能或安全方向可分别考虑 CS5297、CS5491。",
            )
        )
        violations = validate_presentation_arguments(
            {
                "title": "三门课程对比",
                "audience": "选课学生",
                "style": "简洁",
                "brief": brief,
                "slide_count": 7,
            },
            contract,
        )

        self.assertEqual(violations, [])

    def test_non_table_presentation_rejects_oversized_headings_and_bullets(self) -> None:
        contract = compile_task_contract("用中文 PPT 展示课程差异", "work")
        violations = validate_presentation_arguments(
            {
                "brief": "## " + "过长标题" * 8 + "\n- " + "很长的要点" * 30 + "\n- 第二条要点",
                "slide_count": 3,
            },
            contract,
        )

        codes = {item["code"] for item in violations}
        self.assertIn("presentation_heading_too_long", codes)
        self.assertIn("presentation_bullet_too_long", codes)

    def test_exhaustive_presentation_rejects_partial_scope_labels(self) -> None:
        contract = compile_task_contract(
            "整理所有体育课的名称、上课时间和课程代码，并用中文PPT展示",
            "work",
        )
        violations = validate_presentation_arguments(
            {
                "title": "体育课程时间表（节选）",
                "filename": "体育课程时间表_摘要.pptx",
                "table": {
                    "title": "课程安排示例",
                    "columns": ["课程代码", "课程名称", "上课时间"],
                    "rows": [
                        {
                            "cells": ["PED1101", "独木舟", "周三 10:00-11:50"],
                            "source_locator": "page:1",
                        }
                    ],
                },
            },
            contract,
        )
        scope = next(
            item
            for item in violations
            if item["code"] == "presentation_exhaustive_scope_mislabeled"
        )
        self.assertEqual(
            set(scope["fields"]),
            {"title", "filename", "table.title"},
        )

    def test_structured_presentation_rejects_duplicate_entities_and_delimiter_noise(
        self,
    ) -> None:
        contract = compile_task_contract(
            "整理所有体育课的名称、上课时间和课程代码，并用中文PPT展示",
            "work",
        )
        violations = validate_presentation_arguments(
            {
                "table": {
                    "columns": ["课程代码", "课程名称", "上课时间"],
                    "rows": [
                        {
                            "cells": ["PED1305", "Physical Fitness", "周一；；周三"],
                            "source_locator": "page:2",
                            "entity_id": "course:PED1305",
                        },
                        {
                            "cells": ["PED1305", "Physical Fitness", "周五"],
                            "source_locator": "page:3",
                            "entity_id": "course:PED1305",
                        },
                    ],
                }
            },
            contract,
        )
        codes = {item["code"] for item in violations}
        self.assertIn("duplicate_output_entities", codes)
        self.assertIn("presentation_cell_delimiter_noise", codes)

    def test_exhaustive_attached_table_rejects_missing_source_ir(self) -> None:
        contract = compile_task_contract(
            "整理所有体育课的名称、上课时间和课程代码，并用中文PPT展示"
            "\n<!--ai-document:doc-1|courses.pdf-->",
            "work",
        )
        violations = validate_presentation_arguments(
            {
                "table": {
                    "columns": ["课程代码", "课程名称", "上课时间"],
                    "rows": [
                        {
                            "cells": ["PED1101", "Canoeing", "周三 10:00-11:50"],
                            "source_locator": "page:1",
                        }
                    ],
                }
            },
            contract,
            source_ir={"version": "document-source-ir-v1", "tables": []},
        )
        self.assertIn(
            "source_structure_unavailable",
            {item["code"] for item in violations},
        )

    def test_exhaustive_time_rejects_see_original_placeholder(self) -> None:
        contract = compile_task_contract(
            "整理所有体育课的名称、上课时间和课程代码，并用中文PPT展示",
            "work",
        )
        violations = validate_presentation_arguments(
            {
                "table": {
                    "columns": ["课程代码", "课程名称", "上课时间"],
                    "rows": [
                        {
                            "cells": ["PED1305", "Physical Fitness", "见课程表与网页"],
                            "source_locator": "page:2",
                        }
                    ],
                }
            },
            contract,
        )
        self.assertIn(
            "requested_time_values_incomplete",
            {item["code"] for item in violations},
        )

        below_violations = validate_presentation_arguments(
            {
                "table": {
                    "columns": ["课程代码", "课程名称", "上课时间"],
                    "rows": [
                        {
                            "cells": ["PED1305", "Physical Fitness", "（多节，见下列来源）"],
                            "source_locator": "page:2",
                        }
                    ],
                }
            },
            contract,
        )
        self.assertIn(
            "requested_time_values_incomplete",
            {item["code"] for item in below_violations},
        )

    def test_time_field_rejects_non_temporal_metadata_fragments(self) -> None:
        contract = compile_task_contract(
            "整理所有体育课的名称、上课时间和课程代码，并用中文PPT展示",
            "work",
        )
        violations = validate_presentation_arguments(
            {
                "table": {
                    "columns": ["课程代码", "课程名称", "上课时间"],
                    "rows": [
                        {
                            "cells": [
                                "PED1402",
                                "Golf",
                                "周三 09:30-11:20；(部分节次标注有容量/场地信息)",
                            ],
                            "source_locator": "page:2",
                        }
                    ],
                }
            },
            contract,
        )
        mismatch = next(
            item
            for item in violations
            if item["code"] == "presentation_field_semantic_mismatch"
        )
        self.assertEqual(mismatch["field"], "time")
        self.assertEqual(mismatch["affected_rows"], [1])
        self.assertIn("容量/场地信息", mismatch["examples"][0])

    def test_structured_presentation_rejects_column_cell_mismatch_before_worker(self) -> None:
        contract = compile_task_contract(
            "整理所有体育课的名称、上课时间和课程代码，并用中文PPT展示",
            "work",
        )
        violations = validate_presentation_arguments(
            {
                "table": {
                    "columns": ["课程代码", "课程名称", "上课时间", "来源位置"],
                    "rows": [
                        {
                            "cells": ["PED1101", "Canoeing", "周三 10:00-11:50"],
                            "source_locator": "page:1",
                        }
                    ],
                }
            },
            contract,
        )
        mismatch = next(item for item in violations if item["code"] == "table_cell_count_mismatch")
        self.assertEqual(mismatch["affected_rows"], [1])

    def test_exhaustive_code_table_must_cover_repeated_source_record_keys(self) -> None:
        contract = compile_task_contract(
            "整理所有体育课的名称、上课时间和课程代码，并用中文PPT展示",
            "work",
        )
        observations = [
            {
                "tool_name": "work_extract_attached_document",
                "data": {
                    "output": {
                        "text": (
                            "PED1101 Canoeing T01 1000-1150\n"
                            "PED 1204 Hip Hop T01 1500-1650\n"
                            "PED1305 Physical Fitness T01 0900-0950\n"
                            "LG3009 is a venue reference, not a repeated course family\n"
                        )
                    }
                },
            }
        ]
        expected = presentation_source_record_keys(observations, contract)
        self.assertEqual(expected, ["PED1101", "PED1204", "PED1305"])
        violations = validate_presentation_arguments(
            {
                "table": {
                    "columns": ["课程代码", "课程名称", "上课时间"],
                    "rows": [
                        {
                            "cells": ["PED1101", "Canoeing", "周三 10:00-11:50"],
                            "source_locator": "page:1",
                        },
                        {
                            "cells": ["PED1204", "Hip Hop", "周一 15:00-16:50"],
                            "source_locator": "page:1",
                        },
                    ],
                }
            },
            contract,
            expected_record_keys=expected,
        )
        missing = next(item for item in violations if item["code"] == "source_records_missing")
        self.assertEqual(missing["expected_count"], 3)
        self.assertEqual(missing["observed_count"], 2)
        self.assertEqual(missing["missing_keys"], ["PED1305"])

    def test_generic_follow_up_inherits_artifact_goal_for_same_attachment(self) -> None:
        original = (
            "整理所有体育课的名称、上课时间和课程代码，并用中文PPT展示"
            "\n<!--ai-document:doc-1|courses.pdf-->"
        )
        contract = compile_task_contract(
            "请处理这个文件\n<!--ai-document:doc-1|courses.pdf-->",
            "work",
            [{"role": "user", "content": original}],
        )
        self.assertTrue(contract["inherited_from_history"])
        self.assertEqual(contract["artifact_types"], ["pptx"])
        self.assertEqual(contract["requested_fields"], ["code", "name", "time"])
        self.assertTrue(contract["exhaustive"])

    def test_explicit_confirmation_inherits_only_immediate_pending_attachment_goal(self) -> None:
        original = "整理所有体育课并用中文PPT展示\n<!--ai-document:doc-1|courses.pdf-->"
        contract = compile_task_contract(
            "确认",
            "work",
            [
                {"role": "user", "content": original},
                {
                    "role": "assistant",
                    "content": "请确认以上附件提取和 PPT 生成范围，确认后我就开始。",
                },
            ],
        )
        self.assertTrue(contract["inherited_from_history"])
        self.assertTrue(contract["source_required"])
        self.assertEqual(contract["artifact_types"], ["pptx"])

        unrelated = compile_task_contract(
            "确认",
            "work",
            [
                {"role": "user", "content": original},
                {"role": "assistant", "content": "请确认是否继续聊天。"},
            ],
        )
        self.assertFalse(unrelated["inherited_from_history"])
        self.assertEqual(unrelated["artifact_types"], [])

    def test_style_answer_inherits_immediate_pending_attachment_goal(self) -> None:
        original = (
            "重新帮我整理所有体育课的名称、上课时间和课程代码，并用中文ppt展示"
            "\n<!--ai-document:doc-1|courses.pdf-->"
        )
        contract = compile_task_contract(
            "合并成表格形式展示",
            "work",
            [
                {"role": "user", "content": original},
                {
                    "role": "assistant",
                    "content": (
                        "请确认附件提取和 PPT 生成范围，确认后我就开始。"
                        "是否需要将同一课程的所有上课时间合并成表格形式展示？"
                    ),
                },
            ],
        )
        self.assertTrue(contract["inherited_from_history"])
        self.assertEqual(contract["artifact_types"], ["pptx"])
        self.assertIn("补充要求：合并成表格形式展示", contract["objective"])

        cancelled = compile_task_contract(
            "算了，不做了",
            "work",
            [
                {"role": "user", "content": original},
                {
                    "role": "assistant",
                    "content": "请确认附件提取和 PPT 生成范围，确认后我就开始。",
                },
            ],
        )
        self.assertFalse(cancelled["inherited_from_history"])

    def test_known_missing_attachment_reply_can_recover_original_bound_file(self) -> None:
        original = (
            "整理所有体育课的名称、上课时间和课程代码，并用中文ppt展示"
            "\n<!--ai-document:doc-1|courses.pdf-->"
        )
        contract = compile_task_contract(
            "继续",
            "work",
            [
                {"role": "user", "content": original},
                {
                    "role": "assistant",
                    "content": "请确认附件提取和 PPT 生成范围，确认后我就开始。",
                },
                {"role": "user", "content": "合并成表格形式展示"},
                {"role": "assistant", "content": "请先上传一个需要读取的附件。"},
            ],
        )
        self.assertTrue(contract["inherited_from_history"])
        self.assertEqual(contract["artifact_types"], ["pptx"])
        self.assertIn("补充要求：合并成表格形式展示", contract["objective"])

    def test_pptx_file_without_quality_report_is_not_complete(self) -> None:
        report = validate_artifact_observation(
            {
                "status": "succeeded",
                "skill_name": "office.pptx_generate",
                "files": [{"name": "bad.pptx"}],
                "output": {"source_coverage": {"coverage_ratio": 1.0, "truncated": False}},
            },
            {"exhaustive": True},
        )
        self.assertFalse(report["passed"])
        self.assertIn(
            "artifact_quality_report_missing",
            {item["code"] for item in report["violations"]},
        )

    def test_pptx_quality_claim_cannot_hide_zero_structured_rows(self) -> None:
        report = validate_artifact_observation(
            {
                "status": "succeeded",
                "skill_name": "office.pptx_generate",
                "files": [{"name": "courses.pptx"}],
                "output": {
                    "outline": [
                        {"page": 1, "title": "课程总览"},
                        {"page": 2, "title": "内容概览"},
                    ],
                    "quality_report": {
                        "passed": True,
                        "violations": [],
                        "table_row_count": 0,
                    },
                    "source_coverage": {"coverage_ratio": 1.0, "truncated": False},
                },
            },
            compile_task_contract(
                "整理所有体育课的名称、上课时间和课程代码，并用中文PPT展示",
                "work",
            ),
        )
        self.assertFalse(report["passed"])
        self.assertIn(
            "structured_table_missing",
            {item["code"] for item in report["violations"]},
        )

    def test_pptx_requires_complete_source_coverage(self) -> None:
        report = validate_artifact_observation(
            {
                "status": "succeeded",
                "skill_name": "office.pptx_generate",
                "files": [{"name": "partial.pptx"}],
                "output": {
                    "quality_report": {"passed": True, "violations": []},
                    "source_coverage": {"coverage_ratio": 0.5, "truncated": True},
                },
            },
            {"exhaustive": True},
        )
        self.assertFalse(report["passed"])
        self.assertIn(
            "source_coverage_incomplete",
            {item["code"] for item in report["violations"]},
        )

    def test_generic_artifact_gate_rejects_wrong_file_type(self) -> None:
        data = {
            "status": "succeeded",
            "skill_name": "office.markdown_document",
            "files": [{"name": "report.pdf"}],
            "output": {"source_overwritten": False},
        }
        self.assertTrue(artifact_observation_applicable(data))
        report = validate_artifact_observation(data, {"artifact_types": ["markdown"]})
        self.assertFalse(report["passed"])
        self.assertIn(
            "artifact_type_mismatch",
            {item["code"] for item in report["violations"]},
        )

    def test_generic_artifact_gate_accepts_safe_markdown_file(self) -> None:
        report = validate_artifact_observation(
            {
                "status": "succeeded",
                "skill_name": "office.markdown_document",
                "files": [{"name": "report.md"}],
                "output": {"source_overwritten": False},
            },
            {"artifact_types": ["markdown"]},
        )
        self.assertTrue(report["applicable"])
        self.assertTrue(report["passed"])
        self.assertEqual(report["produced_artifact_types"], ["markdown"])

    def test_exhaustive_source_artifact_requires_coverage_evidence(self) -> None:
        report = validate_artifact_observation(
            {
                "status": "succeeded",
                "skill_name": "office.docx_edit",
                "files": [{"name": "edited.docx"}],
                "output": {"source_overwritten": False},
            },
            {"artifact_types": ["docx"], "exhaustive": True, "source_required": True},
        )
        self.assertFalse(report["passed"])
        self.assertIn(
            "source_coverage_missing",
            {item["code"] for item in report["violations"]},
        )


if __name__ == "__main__":
    unittest.main()
