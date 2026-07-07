from __future__ import annotations

import base64
import io
import json
import unittest
from zipfile import ZipFile

from docx import Document
from openpyxl import Workbook

from ai_companion_worker.office_tools import execute


class OfficeToolsTest(unittest.TestCase):
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
        self.assertEqual([item.text for item in edited.paragraphs], ["原始段落", "新增第一段", "新增第二段"])
        self.assertEqual(source.getvalue(), original)
        self.assertFalse(result["output"]["source_overwritten"])

    def test_pptx_generate_has_requested_slide_count_and_outline(self) -> None:
        result = execute(
            "pptx_generate",
            {"title": "季度复盘", "audience": "管理层", "style": "简洁", "brief": "业绩亮点\n风险与机会", "slide_count": 5},
        )
        generated = base64.b64decode(result["files"][0]["data_base64"])
        with ZipFile(io.BytesIO(generated)) as archive:
            slides = [name for name in archive.namelist() if name.startswith("ppt/slides/slide") and name.endswith(".xml")]
        self.assertEqual(len(slides), 5)
        self.assertEqual(len(result["output"]["outline"]), 5)

        outline = execute(
            "pptx_outline",
            {"title": "季度复盘", "audience": "管理层", "style": "简洁", "brief": "业绩亮点\n风险与机会", "slide_count": 5},
        )
        self.assertEqual(outline["files"], [])
        self.assertEqual(outline["output"]["outline"], result["output"]["outline"])

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
            {"source_filename": "sample.xlsx", "source_base64": base64.b64encode(source.getvalue()).decode()},
        )
        self.assertEqual(result["output"]["sheet_name"], "数据")
        self.assertEqual(result["output"]["columns"][1]["numeric"]["mean"], 12.5)


if __name__ == "__main__":
    unittest.main()
