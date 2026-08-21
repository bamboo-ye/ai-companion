from __future__ import annotations

import unittest

from ai_companion_worker.document_parser import (
    MAX_CHUNK_TOKENS,
    Page,
    _chunk_pages,
    _layout_to_markdown,
    _needs_structured_fallback,
    _remove_repeated_margins,
    parse_document,
)


class DocumentParserTest(unittest.TestCase):
    def test_text_document_keeps_page_and_heading(self) -> None:
        result = parse_document("第一章 结论\n\n证据必须带页码。".encode(), "text/plain")
        self.assertEqual(len(result.pages), 1)
        self.assertEqual(result.chunks[0].page_start, 1)
        self.assertIn("证据必须带页码", result.chunks[0].content)

    def test_pdf_extracts_page_text(self) -> None:
        result = parse_document(_sample_pdf(), "application/pdf")
        self.assertEqual(len(result.pages), 1)
        self.assertIn("Evidence lives on page one", result.pages[0].text)
        self.assertEqual(result.chunks[0].page_start, 1)

    def test_pdf_with_safe_leading_whitespace_is_normalized(self) -> None:
        result = parse_document(b"\n\n\n\n\n" + _sample_pdf(), "application/pdf")
        self.assertEqual(len(result.pages), 1)
        self.assertIn("Evidence lives on page one", result.pages[0].text)

    def test_fixed_width_table_is_preserved_as_markdown_code(self) -> None:
        markdown = _layout_to_markdown("Name      Score\nAlice     98\nBob       87")
        self.assertIn("```text", markdown)
        self.assertIn("Name      Score", markdown)
        self.assertIn("Alice     98", markdown)

    def test_repeated_page_margins_are_removed(self) -> None:
        pages = _remove_repeated_margins(
            [
                f"Quarterly Report\nPage body {page}\nConfidential - {page}"
                for page in range(1, 5)
            ]
        )
        self.assertTrue(all("Quarterly Report" not in page for page in pages))
        self.assertTrue(all("Confidential" not in page for page in pages))
        self.assertTrue(all("Page body" in page for page in pages))

    def test_chinese_chunks_use_token_budget_instead_of_character_division(self) -> None:
        result = parse_document(("第一章 结论\n\n" + "证据" * 1_200).encode(), "text/plain")
        self.assertGreater(len(result.chunks), 1)
        self.assertTrue(all(chunk.token_count <= MAX_CHUNK_TOKENS for chunk in result.chunks))
        self.assertGreater(sum(chunk.token_count for chunk in result.chunks), 1_000)

    def test_heading_context_carries_across_page_boundaries(self) -> None:
        chunks = _chunk_pages(
            [
                Page(1, "## Project Alpha\n\nFirst page evidence.", 1.0, "page-1"),
                Page(2, "Second page evidence.", 1.0, "page-2"),
            ]
        )
        second_page = next(chunk for chunk in chunks if chunk.page_start == 2)
        self.assertEqual(second_page.section_path, "Project Alpha")

    def test_structured_fallback_ignores_one_blank_page_but_catches_systemic_failure(
        self,
    ) -> None:
        good = Page(1, "Readable evidence", 1.0, "good")
        blank = Page(2, "", 0.0, "blank")
        self.assertFalse(_needs_structured_fallback([good, blank]))
        self.assertTrue(_needs_structured_fallback([blank]))
        self.assertTrue(_needs_structured_fallback([good, blank, blank, good]))


def _sample_pdf() -> bytes:
    objects = [
        b"<< /Type /Catalog /Pages 2 0 R >>",
        b"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
        b"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
        b"<< /Length 62 >>\nstream\nBT /F1 12 Tf 72 720 Td (Evidence lives on page one.) Tj ET\nendstream",
        b"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
    ]
    output = bytearray(b"%PDF-1.4\n")
    offsets = [0]
    for number, body in enumerate(objects, start=1):
        offsets.append(len(output))
        output.extend(f"{number} 0 obj\n".encode())
        output.extend(body)
        output.extend(b"\nendobj\n")
    xref = len(output)
    output.extend(f"xref\n0 {len(objects) + 1}\n".encode())
    output.extend(b"0000000000 65535 f \n")
    for offset in offsets[1:]:
        output.extend(f"{offset:010d} 00000 n \n".encode())
    output.extend(
        f"trailer\n<< /Size {len(objects) + 1} /Root 1 0 R >>\nstartxref\n{xref}\n%%EOF\n".encode()
    )
    return bytes(output)


if __name__ == "__main__":
    unittest.main()
