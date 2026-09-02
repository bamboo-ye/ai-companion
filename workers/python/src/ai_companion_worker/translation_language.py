from __future__ import annotations

import re
from dataclasses import dataclass
from typing import Any


@dataclass(frozen=True)
class TranslationLanguagePolicy:
    requested: str
    model_label: str
    output_label: str
    simplified_chinese: bool


_SIMPLIFIED_CHINESE_ALIASES = {
    "chinese",
    "mandarin",
    "mandarin chinese",
    "simplified chinese",
    "zh",
    "zh-cn",
    "zh-hans",
    "中文",
    "汉语",
    "漢語",
    "普通话",
    "普通話",
    "简体",
    "簡體",
    "简体中文",
    "簡體中文",
}
_TRADITIONAL_CHINESE_ALIASES = {
    "traditional chinese",
    "zh-hant",
    "zh-hk",
    "zh-mo",
    "zh-tw",
    "繁体",
    "繁體",
    "繁体中文",
    "繁體中文",
}
_SIMPLIFIED_HINT = re.compile(r"(?:simplified|简体|簡體|zh(?:-|_)?(?:cn|hans))", re.IGNORECASE)
_TRADITIONAL_HINT = re.compile(
    r"(?:traditional|繁体|繁體|zh(?:-|_)?(?:tw|hk|mo|hant))",
    re.IGNORECASE,
)
_CHINESE_HINT = re.compile(r"(?:chinese|mandarin|中文|汉语|漢語|普通话|普通話|^zh$)", re.IGNORECASE)
_T2S: Any | None = None


def translation_language_policy(value: str) -> TranslationLanguagePolicy:
    requested = re.sub(r"\s+", " ", str(value or "")).strip()
    normalized = requested.casefold().replace("_", "-")
    if normalized in _TRADITIONAL_CHINESE_ALIASES or _TRADITIONAL_HINT.search(normalized):
        return TranslationLanguagePolicy(
            requested=requested,
            model_label="繁體中文（zh-TW）",
            output_label="繁體中文",
            simplified_chinese=False,
        )
    if (
        normalized in _SIMPLIFIED_CHINESE_ALIASES
        or _SIMPLIFIED_HINT.search(normalized)
        or _CHINESE_HINT.search(normalized)
    ):
        return TranslationLanguagePolicy(
            requested=requested,
            model_label="简体中文（zh-CN，仅使用简体字）",
            output_label="简体中文",
            simplified_chinese=True,
        )
    return TranslationLanguagePolicy(
        requested=requested,
        model_label=requested,
        output_label=requested,
        simplified_chinese=False,
    )


def normalize_translated_script(value: str, *, simplified_chinese: bool) -> str:
    text = str(value or "")
    if not simplified_chinese:
        return text
    global _T2S
    if _T2S is None:
        from opencc import OpenCC  # type: ignore[import-untyped]

        _T2S = OpenCC("t2s")
    return str(_T2S.convert(text))
