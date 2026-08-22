from __future__ import annotations

import unittest

from ai_companion_worker.task_quality import (
    artifact_observation_applicable,
    compile_task_contract,
    validate_artifact_observation,
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
