from __future__ import annotations

import unittest

from ai_companion_worker.response_quality import inspect_and_repair_response


class ResponseQualityTest(unittest.TestCase):
    def test_exact_duplicate_sentence_is_removed_without_model_rewrite(self) -> None:
        result = inspect_and_repair_response(
            "缓存可以减少重复计算。缓存可以减少重复计算。它通常会占用额外空间。"
        )

        self.assertTrue(result.report["passed"])
        self.assertEqual(
            result.response,
            "缓存可以减少重复计算。它通常会占用额外空间。",
        )
        self.assertEqual(
            result.report["repairs"][0]["code"],
            "exact_duplicate_sentence_removed",
        )

    def test_exact_duplicate_paragraph_is_removed(self) -> None:
        result = inspect_and_repair_response("第一段内容。\n\n第一段内容。\n\n第二段内容。")

        self.assertTrue(result.report["passed"])
        self.assertEqual(result.response, "第一段内容。\n\n第二段内容。")

    def test_english_spacing_and_markdown_lines_are_preserved(self) -> None:
        result = inspect_and_repair_response(
            "One sentence. One sentence. Two sentences.\n- first item\n- second item"
        )

        self.assertTrue(result.report["passed"])
        self.assertEqual(
            result.response,
            "One sentence. Two sentences.\n- first item\n- second item",
        )

    def test_near_duplicate_paragraph_requires_bounded_rewrite(self) -> None:
        result = inspect_and_repair_response(
            "请在本周五下午五点之前提交完整的项目报告和所有相关附件。\n\n"
            "请在本周五下午五点前提交完整项目报告和全部相关附件。"
        )

        self.assertFalse(result.report["passed"])
        self.assertEqual(
            result.report["violations"][0]["code"],
            "near_duplicate_paragraph",
        )


if __name__ == "__main__":
    unittest.main()
