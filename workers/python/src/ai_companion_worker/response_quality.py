from __future__ import annotations

import difflib
import re
from dataclasses import dataclass
from typing import Any


RESPONSE_QUALITY_POLICY_VERSION = "response-quality-1.0.0"

_PARAGRAPH_BREAK = re.compile(r"\n\s*\n+")
_SENTENCE = re.compile(r"[^。！？.!?\n]+[。！？.!?]?", re.UNICODE)
_COMPARABLE = re.compile(r"[a-z0-9]+|[\u3400-\u4dbf\u4e00-\u9fff]", re.IGNORECASE)


@dataclass(frozen=True)
class ResponseQualityResult:
    response: str
    report: dict[str, Any]


def inspect_and_repair_response(response: str) -> ResponseQualityResult:
    """Remove exact repetition and report remaining near-duplicate content.

    Exact duplicate paragraphs and sentences can be removed without changing
    meaning. Near duplicates are only reported because a model rewrite is
    safer than deleting text that may contain one additional fact.
    """

    original = response.strip()
    repaired, repairs = _remove_exact_duplicates(original)
    violations = _near_duplicate_violations(repaired)
    return ResponseQualityResult(
        response=repaired,
        report={
            "passed": not violations,
            "policy_version": RESPONSE_QUALITY_POLICY_VERSION,
            "violations": violations,
            "repairs": repairs,
            "changed": repaired != original,
        },
    )


def _remove_exact_duplicates(response: str) -> tuple[str, list[dict[str, Any]]]:
    paragraphs = [item.strip() for item in _PARAGRAPH_BREAK.split(response) if item.strip()]
    seen_paragraphs: set[str] = set()
    repaired: list[str] = []
    repairs: list[dict[str, Any]] = []
    for paragraph_index, paragraph in enumerate(paragraphs):
        normalized = _normalize(paragraph)
        if normalized and normalized in seen_paragraphs:
            repairs.append(
                {
                    "code": "exact_duplicate_paragraph_removed",
                    "location": f"/paragraphs/{paragraph_index}",
                }
            )
            continue
        seen_paragraphs.add(normalized)
        seen_sentences: set[str] = set()
        kept_lines: list[str] = []
        for line_index, line in enumerate(paragraph.splitlines()):
            sentences = [item.strip() for item in _SENTENCE.findall(line) if item.strip()]
            kept_sentences: list[str] = []
            for sentence_index, sentence in enumerate(sentences):
                sentence_key = _normalize(sentence)
                if sentence_key and sentence_key in seen_sentences:
                    repairs.append(
                        {
                            "code": "exact_duplicate_sentence_removed",
                            "location": (
                                f"/paragraphs/{paragraph_index}/lines/{line_index}"
                                f"/sentences/{sentence_index}"
                            ),
                        }
                    )
                    continue
                seen_sentences.add(sentence_key)
                kept_sentences.append(sentence)
            if kept_sentences:
                kept_lines.append(_join_sentences(kept_sentences))
        if kept_lines:
            repaired.append("\n".join(kept_lines))
    return "\n\n".join(repaired).strip(), repairs


def _near_duplicate_violations(response: str) -> list[dict[str, Any]]:
    paragraphs = [item.strip() for item in _PARAGRAPH_BREAK.split(response) if item.strip()]
    violations: list[dict[str, Any]] = []
    for left_index, left in enumerate(paragraphs):
        left_key = _normalize(left)
        if len(left_key) < 24:
            continue
        for right_index in range(left_index + 1, len(paragraphs)):
            right_key = _normalize(paragraphs[right_index])
            if len(right_key) < 24:
                continue
            ratio = difflib.SequenceMatcher(None, left_key, right_key).ratio()
            if ratio < 0.88:
                continue
            violations.append(
                {
                    "code": "near_duplicate_paragraph",
                    "severity": "repairable",
                    "locations": [
                        f"/paragraphs/{left_index}",
                        f"/paragraphs/{right_index}",
                    ],
                    "similarity": round(ratio, 4),
                }
            )
    return violations


def _normalize(value: str) -> str:
    return "".join(_COMPARABLE.findall(value.casefold()))


def _join_sentences(values: list[str]) -> str:
    result = ""
    for value in values:
        separator = ""
        if result and result[-1] in ".!?" and value and value[0].isascii() and value[0].isalnum():
            separator = " "
        result += separator + value
    return result
