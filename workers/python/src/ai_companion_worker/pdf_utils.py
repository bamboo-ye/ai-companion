from __future__ import annotations

_PDF_HEADER = b"%PDF-"
_MAX_LEADING_WHITESPACE = 64
_SAFE_LEADING_WHITESPACE = frozenset(b" \t\r\n\f")


def normalize_pdf_bytes(data: bytes) -> bytes:
    """Return a PDF byte stream whose header and internal offsets share origin."""
    if data.startswith(_PDF_HEADER):
        return data
    search_limit = min(
        len(data),
        _MAX_LEADING_WHITESPACE + len(_PDF_HEADER),
    )
    index = data.find(_PDF_HEADER, 0, search_limit)
    if index <= 0 or any(value not in _SAFE_LEADING_WHITESPACE for value in data[:index]):
        raise ValueError("invalid_pdf_header")
    return data[index:]
