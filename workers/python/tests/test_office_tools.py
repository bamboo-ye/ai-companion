from __future__ import annotations

import base64
import io
import json
import os
import unittest
from unittest.mock import Mock, patch
from zipfile import ZipFile

from docx import Document
from openpyxl import Workbook
from pptx import Presentation
from pptx.util import Inches
from reportlab.pdfgen import canvas

from ai_companion_worker.office_tools import (
    ModelBackedOperationError,
    _openrouter_translate,
    _presentation_display_rows,
    _translated_content,
    execute,
    main,
)


class OfficeToolsTest(unittest.TestCase):
    def test_presentation_display_rows_keep_one_logical_record(self) -> None:
        schedule = "；".join(f"T{index:02d} 周一 09:00-09:50" for index in range(12))
        rows = _presentation_display_rows(
            [
                {
                    "cells": [
                        "PED1305",
                        "体能训练",
                        schedule,
                    ],
                    "source_locator": "page:2",
                    "entity_id": "course:PED1305",
                }
            ]
        )
        self.assertEqual(len(rows), 1)
        self.assertEqual(rows[0]["cells"], ["PED1305", "体能训练", schedule])
        self.assertFalse(rows[0]["continuation"])

    def test_pptx_quality_rejects_english_visible_content_for_chinese_deck(self) -> None:
        result = execute(
            "pptx_generate",
            {
                "title": "Important Dates",
                "audience": "新生",
                "style": "简洁表格",
                "brief": "展示重要日期",
                "slide_count": 3,
                "table": {
                    "title": "Semester A 2026/27",
                    "columns": ["Month", "Event"],
                    "rows": [
                        {
                            "cells": ["July", "Release of Class Schedule"],
                            "source_locator": "Page 4",
                        }
                    ],
                },
                "task_contract": {
                    "requested_fields": [],
                    "output_language": "zh-CN",
                },
                "source_coverage": {"coverage_ratio": 1.0, "truncated": False},
            },
        )

        report = result["output"]["quality_report"]
        self.assertFalse(report["passed"])
        self.assertEqual(result["files"], [])
        self.assertIn(
            "presentation_visible_language_mismatch",
            {item["code"] for item in report["violations"]},
        )

    def test_translation_response_rejects_null_content(self) -> None:
        with self.assertRaisesRegex(ValueError, "translation_model_returned_empty_text"):
            _translated_content({"choices": [{"message": {"content": None}}]})
        self.assertEqual(
            _translated_content(
                {"choices": [{"message": {"content": [{"type": "text", "text": "翻译结果"}]}}]}
            ),
            "翻译结果",
        )

    @patch("urllib.request.urlopen")
    def test_pdf_translation_uses_budgeted_openrouter_policy_and_usage(self, urlopen: Mock) -> None:
        response = urlopen.return_value.__enter__.return_value
        response.read.return_value = json.dumps(
            {
                "model": "openai/gpt-5-mini",
                "provider": "OpenAI",
                "choices": [
                    {
                        "finish_reason": "stop",
                        "message": {"content": "Translated text"},
                    }
                ],
                "usage": {
                    "prompt_tokens": 30,
                    "completion_tokens": 8,
                    "cost": 0.000004,
                },
            }
        ).encode()
        with patch.dict(
            os.environ,
            {
                "MODEL_PROVIDER": "openrouter",
                "MODEL_API_KEY": "test-key",
                "MODEL_NAME": "deepseek/deepseek-v4-flash-0731",
                "MODEL_TRANSLATION_MAX_TOKENS": "8000",
                "MODEL_TRANSLATION_MAX_COST_MICROS": "60000",
                "MODEL_TRANSLATION_MAX_PROMPT_PRICE": "0.3",
                "MODEL_TRANSLATION_MAX_COMPLETION_PRICE": "2.5",
                "MODEL_DATA_COLLECTION": "deny",
                "MODEL_ZDR_REQUIRED": "false",
                "MODEL_PROVIDER_SORT": "price",
                "MODEL_ALLOW_PROVIDER_FALLBACKS": "true",
            },
            clear=True,
        ):
            translated, usage = _openrouter_translate("Source text", "English")
        self.assertEqual(translated, "Translated text")
        self.assertEqual(usage["cost_micros"], 4)
        self.assertEqual(usage["cost_accounting"], "reported")
        self.assertEqual(usage["returned_model"], "openai/gpt-5-mini")
        request = urlopen.call_args.args[0]
        body = json.loads(request.data)
        self.assertNotIn("temperature", body)
        self.assertEqual(body["model"], "openai/gpt-5-mini")
        self.assertEqual(body["max_tokens"], 8000)
        self.assertEqual(body["provider"]["data_collection"], "deny")
        self.assertTrue(body["provider"]["require_parameters"])
        self.assertEqual(
            body["provider"]["max_price"],
            {"prompt": 0.3, "completion": 2.5},
        )

    @patch("urllib.request.urlopen")
    def test_pdf_translation_rejects_truncated_response_and_retains_usage(
        self, urlopen: Mock
    ) -> None:
        response = urlopen.return_value.__enter__.return_value
        response.read.return_value = json.dumps(
            {
                "model": "openai/gpt-5-mini",
                "choices": [
                    {
                        "finish_reason": "length",
                        "message": {"content": "Partial translation"},
                    }
                ],
                "usage": {
                    "prompt_tokens": 30,
                    "completion_tokens": 8,
                    "cost": 0.000004,
                },
            }
        ).encode()
        with patch.dict(
            os.environ,
            {
                "MODEL_PROVIDER": "openrouter",
                "MODEL_API_KEY": "test-key",
                "MODEL_TRANSLATION_NAME": "openai/gpt-5-mini",
            },
        ):
            with self.assertRaises(ModelBackedOperationError) as caught:
                _openrouter_translate("Source text", "English")
        self.assertEqual(caught.exception.code, "translation_model_truncated")
        self.assertEqual(caught.exception.model_usage["cost_micros"], 4)

    @patch("urllib.request.urlopen")
    def test_pdf_translation_requires_the_complete_page_marker_sequence(
        self, urlopen: Mock
    ) -> None:
        response = urlopen.return_value.__enter__.return_value
        response.read.return_value = json.dumps(
            {
                "model": "openai/gpt-5-mini",
                "choices": [
                    {
                        "finish_reason": "stop",
                        "message": {"content": "[[PAGE 1]]\nFirst page translated"},
                    }
                ],
                "usage": {
                    "prompt_tokens": 40,
                    "completion_tokens": 10,
                    "cost": 0.000005,
                },
            }
        ).encode()
        with patch.dict(
            os.environ,
            {
                "MODEL_PROVIDER": "openrouter",
                "MODEL_API_KEY": "test-key",
                "MODEL_TRANSLATION_NAME": "openai/gpt-5-mini",
            },
        ):
            with self.assertRaises(ModelBackedOperationError) as caught:
                _openrouter_translate("[[PAGE 1]]\nFirst\n\n[[PAGE 2]]\nSecond", "English")
        self.assertEqual(caught.exception.code, "translation_page_markers_invalid")
        self.assertEqual(caught.exception.model_usage["cost_micros"], 5)

    @patch("urllib.request.urlopen")
    def test_pdf_translation_invalid_content_retains_usage(self, urlopen: Mock) -> None:
        response = urlopen.return_value.__enter__.return_value
        response.read.return_value = json.dumps(
            {
                "model": "openai/gpt-5-mini",
                "choices": [{"finish_reason": "stop", "message": {"content": None}}],
                "usage": {
                    "prompt_tokens": 20,
                    "completion_tokens": 1,
                    "cost": 0.000003,
                },
            }
        ).encode()
        with patch.dict(
            os.environ,
            {
                "MODEL_PROVIDER": "openrouter",
                "MODEL_API_KEY": "test-key",
                "MODEL_TRANSLATION_NAME": "openai/gpt-5-mini",
            },
        ):
            with self.assertRaises(ModelBackedOperationError) as caught:
                _openrouter_translate("Source text", "English")
        self.assertEqual(caught.exception.code, "translation_model_returned_empty_text")
        self.assertEqual(caught.exception.model_usage["cost_micros"], 3)

    @patch("urllib.request.urlopen")
    def test_pdf_translation_invalid_json_retains_reserved_usage_bound(self, urlopen: Mock) -> None:
        response = urlopen.return_value.__enter__.return_value
        response.read.return_value = b"not-json"
        with patch.dict(
            os.environ,
            {
                "MODEL_PROVIDER": "openrouter",
                "MODEL_API_KEY": "test-key",
                "MODEL_TRANSLATION_NAME": "openai/gpt-5-mini",
                "MODEL_TRANSLATION_MAX_COST_MICROS": "60000",
            },
        ):
            with self.assertRaises(ModelBackedOperationError) as caught:
                _openrouter_translate("Source text", "English")
        self.assertEqual(caught.exception.code, "translation_model_returned_invalid_json")
        self.assertEqual(caught.exception.model_usage["cost_micros"], 60_000)
        self.assertEqual(
            caught.exception.model_usage["cost_accounting"],
            "reserved_upper_bound",
        )

    def test_pdf_translation_render_failure_retains_model_usage(self) -> None:
        source = io.BytesIO()
        document = canvas.Canvas(source)
        document.drawString(72, 760, "Source page")
        document.save()
        usage = {"provider": "openrouter", "cost_micros": 7}
        with (
            patch(
                "ai_companion_worker.office_tools._openrouter_translate",
                return_value=("[[PAGE 1]]\nTranslated page", usage),
            ),
            patch(
                "ai_companion_worker.office_tools._render_translated_pdf",
                side_effect=RuntimeError("render exploded"),
            ),
        ):
            with self.assertRaises(ModelBackedOperationError) as caught:
                execute(
                    "pdf_translate",
                    {
                        "source_filename": "source.pdf",
                        "source_base64": base64.b64encode(source.getvalue()).decode(),
                        "target_language": "English",
                    },
                )
        self.assertEqual(caught.exception.code, "translation_render_failed")
        self.assertEqual(caught.exception.model_usage, usage)

    def test_main_transports_billed_operation_failure_as_structured_output(
        self,
    ) -> None:
        usage = {"provider": "openrouter", "cost_micros": 9}
        stdin = io.StringIO('{"operation":"pdf_translate","input":{}}')
        with (
            patch("sys.stdin", stdin),
            patch("sys.stdout", new_callable=io.StringIO) as stdout,
            patch(
                "ai_companion_worker.office_tools.execute",
                side_effect=ModelBackedOperationError("translation_render_failed", usage),
            ),
        ):
            main()
        envelope = json.loads(stdout.getvalue())
        self.assertEqual(envelope["files"], [])
        self.assertEqual(envelope["output"]["model_usage"], usage)
        self.assertEqual(
            envelope["output"]["operation_error"]["code"],
            "translation_render_failed",
        )

    @patch("urllib.request.urlopen")
    def test_pdf_translation_budget_can_block_before_dispatch(self, urlopen: Mock) -> None:
        with patch.dict(
            os.environ,
            {
                "MODEL_PROVIDER": "openrouter",
                "MODEL_API_KEY": "test-key",
                "MODEL_TRANSLATION_NAME": "openai/gpt-5-mini",
                "MODEL_TRANSLATION_MAX_COST_MICROS": "100",
            },
        ):
            with self.assertRaisesRegex(ValueError, "budget_exhausted"):
                _openrouter_translate("Source text", "English")
        urlopen.assert_not_called()

    @patch("urllib.request.urlopen")
    def test_pdf_translation_rejects_relaxed_price_ceiling(self, urlopen: Mock) -> None:
        with patch.dict(
            os.environ,
            {
                "MODEL_PROVIDER": "openrouter",
                "MODEL_API_KEY": "test-key",
                "MODEL_TRANSLATION_NAME": "openai/gpt-5-mini",
                "MODEL_TRANSLATION_MAX_PROMPT_PRICE": "0.31",
            },
        ):
            with self.assertRaisesRegex(ValueError, "price_ceiling"):
                _openrouter_translate("Source text", "English")
        urlopen.assert_not_called()

    def test_docx_edit_creates_copy_and_keeps_source_unchanged(self) -> None:
        source = io.BytesIO()
        document = Document()
        document.add_paragraph("原始段落")
        document.save(source)
        original = source.getvalue()
        result = execute(
            "docx_edit",
            {
                "source_filename": "source.docx",
                "source_base64": base64.b64encode(original).decode(),
                "append_text": "新增第一段\n新增第二段",
            },
        )
        generated = base64.b64decode(result["files"][0]["data_base64"])
        edited = Document(io.BytesIO(generated))
        self.assertEqual(
            [item.text for item in edited.paragraphs], ["原始段落", "新增第一段", "新增第二段"]
        )
        self.assertEqual(source.getvalue(), original)
        self.assertFalse(result["output"]["source_overwritten"])

    def test_pptx_generate_has_requested_slide_count_and_outline(self) -> None:
        result = execute(
            "pptx_generate",
            {
                "title": "季度复盘",
                "audience": "管理层",
                "style": "简洁",
                "brief": "业绩亮点\n风险与机会",
                "slide_count": 5,
            },
        )
        generated = base64.b64decode(result["files"][0]["data_base64"])
        with ZipFile(io.BytesIO(generated)) as archive:
            slides = [
                name
                for name in archive.namelist()
                if name.startswith("ppt/slides/slide") and name.endswith(".xml")
            ]
        self.assertEqual(len(slides), 5)
        self.assertEqual(len(result["output"]["outline"]), 5)
        self.assertTrue(result["output"]["quality_report"]["passed"])

        outline = execute(
            "pptx_outline",
            {
                "title": "季度复盘",
                "audience": "管理层",
                "style": "简洁",
                "brief": "业绩亮点\n风险与机会",
                "slide_count": 5,
            },
        )
        self.assertEqual(outline["files"], [])
        self.assertEqual(outline["output"]["outline"], result["output"]["outline"])

    def test_pptx_title_with_slash_is_safe_for_outline_and_generated_filename(self) -> None:
        payload = {
            "title": "Important Dates - Semester A 2026/27",
            "audience": "New Students",
            "style": "professional",
            "brief": "Key dates timeline and summary",
            "slide_count": 5,
        }

        outline = execute("pptx_outline", payload)
        self.assertEqual(outline["files"], [])
        self.assertEqual(outline["output"]["title"], "Important Dates - Semester A 2026/27")

        presentation = execute("pptx_generate", payload)
        self.assertEqual(
            presentation["files"][0]["name"],
            "Important-Dates-Semester-A-2026-27.pptx",
        )

    def test_pptx_table_preserves_rows_fields_coverage_and_unique_pages(self) -> None:
        rows = [
            {
                "cells": [f"PED{1100 + index}", f"课程 {index}", f"周三 {9 + index:02d}:00"],
                "source_locator": f"page:{1 + index // 6}",
            }
            for index in range(1, 13)
        ]
        result = execute(
            "pptx_generate",
            {
                "title": "体育课程表",
                "audience": "选课同学",
                "style": "简洁清晰，表格为主",
                "brief": "完整展示课程代码、名称和上课时间",
                "slide_count": 5,
                "table": {
                    "title": "课程安排",
                    "columns": ["课程代码", "课程名称", "上课时间"],
                    "rows": rows,
                },
                "task_contract": {
                    "exhaustive": True,
                    "requested_fields": ["code", "name", "time"],
                },
                "source_coverage": {
                    "coverage_ratio": 1.0,
                    "truncated": False,
                    "completed_rounds": 2,
                    "round_count": 2,
                },
            },
        )
        output = result["output"]
        self.assertTrue(output["quality_report"]["passed"], output["quality_report"])
        self.assertEqual(output["quality_report"]["table_row_count"], 12)
        self.assertEqual(
            output["outline"][-1]["bullets"][-1],
            "详细信息见前页表格，来源说明见各页页脚",
        )
        self.assertEqual(
            list(
                dict.fromkeys(
                    locator
                    for slide in output["outline"][1:-1]
                    for locator in slide["table"]["source_locators"]
                )
            ),
            ["page:1", "page:2", "page:3"],
        )
        signatures = [json.dumps(item, sort_keys=True) for item in output["outline"][1:-1]]
        self.assertEqual(len(signatures), len(set(signatures)))
        deck = Presentation(io.BytesIO(base64.b64decode(result["files"][0]["data_base64"])))
        for slide in list(deck.slides)[1:-1]:
            table_shape = next(shape for shape in slide.shapes if shape.has_table)
            footer = next(
                shape
                for shape in slide.shapes
                if shape.has_text_frame and shape.text.startswith("来源：")
            )
            self.assertGreaterEqual(
                footer.top,
                table_shape.top + table_shape.height + Inches(0.25),
            )

    def test_pptx_long_schedule_uses_one_unsplit_detail_cell(self) -> None:
        schedule = "；".join(
            f"2026年{1 + index // 28}月{1 + index % 28}日 周一 09:00-09:50"
            for index in range(72)
        )
        result = execute(
            "pptx_generate",
            {
                "title": "体育课程表",
                "audience": "选课同学",
                "style": "简洁清晰，表格为主",
                "brief": "完整展示课程代码、名称和上课时间",
                "slide_count": 3,
                "table": {
                    "title": "课程安排",
                    "columns": ["课程代码", "课程名称", "上课时间"],
                    "rows": [
                        {
                            "cells": ["PED1305", "体能训练", schedule],
                            "source_locator": "page:2",
                            "entity_id": "course:PED1305",
                        }
                    ],
                },
                "task_contract": {
                    "exhaustive": True,
                    "requested_fields": ["code", "name", "time"],
                    "output_language": "zh-CN",
                },
                "source_coverage": {
                    "coverage_ratio": 1.0,
                    "truncated": False,
                },
            },
        )
        output = result["output"]
        self.assertTrue(output["quality_report"]["passed"], output["quality_report"])
        self.assertEqual(output["quality_report"]["table_row_count"], 1)
        self.assertEqual(output["quality_report"]["display_row_count"], 1)
        self.assertEqual(output["slide_count"], 3)
        self.assertTrue(output["outline"][1]["table"]["detail"])
        self.assertEqual(output["outline"][1]["table"]["display_row_count"], 1)
        deck = Presentation(io.BytesIO(base64.b64decode(result["files"][0]["data_base64"])))
        detail_slide = deck.slides[1]
        table_shape = next(shape for shape in detail_slide.shapes if shape.has_table)
        value_paragraph = table_shape.table.cell(0, 0).text_frame.paragraphs[1]
        self.assertLessEqual(value_paragraph.font.size.pt, 10)
        footer = next(
            shape
            for shape in detail_slide.shapes
            if shape.has_text_frame and shape.text.startswith("来源：")
        )
        self.assertGreaterEqual(
            footer.top,
            table_shape.top + table_shape.height + Inches(0.25),
        )

    def test_pptx_exhaustive_quality_rejects_partial_scope_labels(self) -> None:
        result = execute(
            "pptx_generate",
            {
                "title": "体育课程表（节选）",
                "filename": "体育课程表_摘要.pptx",
                "audience": "选课同学",
                "style": "简洁表格",
                "brief": "完整课程",
                "slide_count": 3,
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
                "task_contract": {
                    "exhaustive": True,
                    "requested_fields": ["code", "name", "time"],
                    "output_language": "zh-CN",
                },
                "source_coverage": {"coverage_ratio": 1.0, "truncated": False},
            },
        )
        report = result["output"]["quality_report"]
        self.assertFalse(report["passed"])
        self.assertEqual(result["files"], [])
        self.assertIn(
            "presentation_exhaustive_scope_mislabeled",
            {item["code"] for item in report["violations"]},
        )

    def test_pptx_structured_contract_rejects_brief_only_content(self) -> None:
        result = execute(
            "pptx_generate",
            {
                "title": "体育课课程安排总览",
                "audience": "学生",
                "style": "清晰、规范",
                "brief": "提取自课程表 PDF",
                "slide_count": 10,
                "task_contract": {
                    "exhaustive": True,
                    "requested_fields": ["code", "name", "time"],
                },
                "source_coverage": {"coverage_ratio": 1.0, "truncated": False},
            },
        )
        report = result["output"]["quality_report"]
        self.assertFalse(report["passed"])
        self.assertIn(
            "structured_table_missing",
            {item["code"] for item in report["violations"]},
        )
        self.assertEqual(report["table_row_count"], 0)

    def test_pptx_exhaustive_source_rejects_incomplete_coverage(self) -> None:
        result = execute(
            "pptx_generate",
            {
                "title": "课程表",
                "audience": "学生",
                "style": "表格",
                "brief": "所有课程",
                "slide_count": 4,
                "table": {
                    "columns": ["课程代码", "课程名称"],
                    "rows": [{"cells": ["PED1101", "独木舟"], "source_locator": "page:1"}],
                },
                "task_contract": {"exhaustive": True, "requested_fields": ["code", "name"]},
                "source_coverage": {"coverage_ratio": 0.5, "truncated": True},
            },
        )
        self.assertFalse(result["output"]["quality_report"]["passed"])
        self.assertIn(
            "source_coverage_incomplete",
            {item["code"] for item in result["output"]["quality_report"]["violations"]},
        )

    def test_pptx_quality_rejects_duplicate_entities_and_delimiter_noise(self) -> None:
        result = execute(
            "pptx_generate",
            {
                "title": "体育课程表",
                "audience": "学生",
                "style": "表格",
                "brief": "所有课程",
                "slide_count": 4,
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
                },
                "task_contract": {
                    "exhaustive": True,
                    "requested_fields": ["code", "name", "time"],
                },
                "source_coverage": {"coverage_ratio": 1.0, "truncated": False},
            },
        )
        report = result["output"]["quality_report"]
        self.assertFalse(report["passed"])
        self.assertEqual(result["files"], [])
        codes = {item["code"] for item in report["violations"]}
        self.assertIn("duplicate_logical_entities", codes)
        self.assertIn("presentation_cell_delimiter_noise", codes)

    def test_pptx_quality_rejects_visible_reference_guidance(self) -> None:
        result = execute(
            "pptx_generate",
            {
                "title": "体育课程表",
                "audience": "学生",
                "style": "表格",
                "brief": "所有课程",
                "slide_count": 4,
                "table": {
                    "columns": ["课程代码", "课程名称", "上课时间"],
                    "rows": [
                        {
                            "cells": ["PED1305", "Physical Fitness", "（多节，见下列来源）"],
                            "source_locator": "page:2",
                        }
                    ],
                },
                "task_contract": {
                    "exhaustive": True,
                    "requested_fields": ["code", "name", "time"],
                },
                "source_coverage": {"coverage_ratio": 1.0, "truncated": False},
            },
        )
        report = result["output"]["quality_report"]
        self.assertFalse(report["passed"])
        self.assertIn(
            "presentation_reference_guidance_visible",
            {item["code"] for item in report["violations"]},
        )

    def test_pptx_quality_rejects_off_field_metadata_in_time_column(self) -> None:
        result = execute(
            "pptx_generate",
            {
                "title": "体育课程表",
                "audience": "学生",
                "style": "表格",
                "brief": "所有课程",
                "slide_count": 4,
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
                },
                "task_contract": {
                    "exhaustive": True,
                    "requested_fields": ["code", "name", "time"],
                },
                "source_coverage": {"coverage_ratio": 1.0, "truncated": False},
            },
        )
        report = result["output"]["quality_report"]
        self.assertFalse(report["passed"])
        self.assertIn(
            "presentation_field_semantic_mismatch",
            {item["code"] for item in report["violations"]},
        )

    def test_pptx_explicit_filename_still_rejects_paths(self) -> None:
        with self.assertRaisesRegex(ValueError, "invalid_output_filename"):
            execute(
                "pptx_generate",
                {
                    "title": "Quarterly review",
                    "audience": "Leadership",
                    "style": "professional",
                    "brief": "Results and next steps",
                    "slide_count": 4,
                    "filename": "reports/quarterly-review.pptx",
                },
            )

    def test_document_extract_returns_pdf_text_without_creating_a_file(self) -> None:
        source = io.BytesIO()
        document = canvas.Canvas(source)
        document.drawString(72, 760, "CS5491 course overview")
        document.showPage()
        document.drawString(72, 760, "Assessment and project requirements")
        document.save()
        result = execute(
            "document_extract",
            {
                "source_filename": "CS5491.pdf",
                "source_base64": base64.b64encode(b"\n\n\n\n\n" + source.getvalue()).decode(),
                "media_type": "application/pdf",
            },
        )
        self.assertEqual(result["files"], [])
        self.assertEqual(result["output"]["page_count"], 2)
        self.assertEqual(result["output"]["format"], "markdown")
        self.assertIn("parser_version", result["output"])
        self.assertIn("CS5491 course overview", result["output"]["text"])
        self.assertFalse(result["output"]["source_overwritten"])

    def test_document_extract_limits_large_attachment_context_by_tokens(self) -> None:
        text = "\n\n".join(f"## Section {index}\n" + "证据" * 500 for index in range(20))
        result = execute(
            "document_extract",
            {
                "source_filename": "long.txt",
                "source_base64": base64.b64encode(text.encode()).decode(),
                "media_type": "text/plain",
            },
        )
        output = result["output"]
        self.assertTrue(output["truncated"])
        self.assertLessEqual(output["token_count"], 16_000)
        self.assertLess(output["selected_chunk_count"], output["total_chunk_count"])
        self.assertIn("[[DOCUMENT ROUND 1]]", output["text"])
        self.assertIn("[[PAGE 1]]", output["text"])
        self.assertGreater(output["round_count"], output["completed_rounds"])
        self.assertTrue(output["has_more"])
        self.assertLess(output["coverage_ratio"], 1)

    def test_csv_profile_reports_types_missing_duplicates_and_download(self) -> None:
        source = "name,amount,note\nA,10,ok\nB,20,\nB,20,\n".encode()
        result = execute(
            "tabular_profile",
            {"source_filename": "sample.csv", "source_base64": base64.b64encode(source).decode()},
        )
        output = result["output"]
        self.assertEqual(output["row_count"], 3)
        self.assertEqual(output["duplicate_rows"], 1)
        self.assertEqual(output["columns"][1]["inferred_type"], "number")
        report = json.loads(base64.b64decode(result["files"][0]["data_base64"]))
        self.assertEqual(report["columns"][2]["missing_count"], 2)

    def test_xlsx_profile_uses_active_sheet(self) -> None:
        workbook = Workbook()
        sheet = workbook.active
        sheet.title = "数据"
        sheet.append(["项目", "数值"])
        sheet.append(["A", 12.5])
        source = io.BytesIO()
        workbook.save(source)
        result = execute(
            "tabular_profile",
            {
                "source_filename": "sample.xlsx",
                "source_base64": base64.b64encode(source.getvalue()).decode(),
            },
        )
        self.assertEqual(result["output"]["sheet_name"], "数据")
        self.assertEqual(result["output"]["columns"][1]["numeric"]["mean"], 12.5)


if __name__ == "__main__":
    unittest.main()
