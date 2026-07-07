from __future__ import annotations

import unittest

from ai_companion_worker.document_parser import parse_document


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
