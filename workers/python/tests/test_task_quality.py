from __future__ import annotations

import unittest

from ai_companion_worker.task_quality import (
    artifact_observation_applicable,
    compile_task_contract,
    presentation_source_record_keys,
    validate_artifact_observation,
    validate_presentation_arguments,
)


class TaskQualityTests(unittest.TestCase):
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
                            "cells": ["PED1101", "Canoeing", "周三 10:00-11:50"],
                            "source_locator": "page:1",
                        }
                    ],
                }
            },
            contract,
        )
        self.assertEqual(violations, [])

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
