from __future__ import annotations

import re
from typing import Any, Mapping


EMAIL_DRAFT_POLICY_VERSION = "email-draft-policy-2.1.0"

_HAN = re.compile(r"[\u3400-\u4dbf\u4e00-\u9fff]")
_ENGLISH_REQUEST = re.compile(
    r"(?:英文|英语|全英|in\s+english|english\s+(?:email|mail|draft))",
    re.IGNORECASE,
)
_CHINESE_REQUEST = re.compile(
    r"(?:中文|汉语|in\s+chinese|chinese\s+(?:email|mail|draft))",
    re.IGNORECASE,
)
_EMAIL_REQUEST_IN_BODY = re.compile(
    r"(?:\?|\b(?:could|would|can|will)\s+you\b|"
    r"\bplease\s+(?:let|tell|confirm|advise|provide|share|reply|respond|send|clarify|explain)\b|"
    r"\bi\s+am\s+writing\s+to\s+(?:ask|inquire|request)\b|"
    r"\bi\s+would\s+(?:like|appreciate)\s+to\s+(?:know|ask|request)\b|"
    r"请问|烦请|劳烦|能否|可否|是否可以|"
    r"请(?:您)?(?:告知|确认|回复|提供|说明|协助)|希望(?:您)?)",
    re.IGNORECASE,
)
_EMAIL_COURTESY_IN_BODY = re.compile(
    r"(?:\bthank(?:s|\s+you)?\b|\bappreciat\w*\b|\bgrateful\b|感谢|谢谢|感激)",
    re.IGNORECASE,
)
_COMPARABLE_TOKEN = re.compile(r"[a-z0-9]+|[\u3400-\u4dbf\u4e00-\u9fff]", re.IGNORECASE)
_DUPLICATE_STOP_WORDS = {
    "a",
    "an",
    "and",
    "are",
    "be",
    "for",
    "i",
    "in",
    "is",
    "it",
    "me",
    "my",
    "of",
    "on",
    "or",
    "please",
    "that",
    "the",
    "this",
    "to",
    "we",
    "you",
    "your",
}


def requested_email_language(message: str) -> str:
    """Return only an explicit user language instruction.

    Locale and profile defaults are intentionally handled by the composer. They
    must never override an explicit language request.
    """

    if _ENGLISH_REQUEST.search(message):
        return "en-US"
    if _CHINESE_REQUEST.search(message):
        return "zh-CN"
    return ""


def validate_email_arguments(
    arguments: Mapping[str, Any],
    *,
    message: str = "",
    email_profile: Mapping[str, Any] | None = None,
) -> list[str]:
    """Validate the model-composed email contract without another model call."""

    violations: list[str] = []
    language = _text(arguments.get("output_language"))
    if language not in ("en-US", "zh-CN"):
        violations.append("unsupported_output_language")
    requested = requested_email_language(message)
    if requested and language != requested:
        violations.append("explicit_language_mismatch")

    relationship = _text(arguments.get("relationship"))
    if relationship not in ("first_contact", "ongoing", "reply", "unknown"):
        violations.append("invalid_relationship")
    introduction_policy = _text(arguments.get("introduction_policy"))
    if introduction_policy not in ("required", "auto", "omit"):
        violations.append("invalid_introduction_policy")

    required_strings = (
        "sender_name",
        "subject",
        "purpose",
        "salutation",
        "request_or_next_step",
        "courtesy",
        "closing",
        "tone",
    )
    for field in required_strings:
        if not _text(arguments.get(field)):
            violations.append(f"missing_{field}")

    paragraphs = _text_list(arguments.get("body_paragraphs"))
    if not paragraphs:
        violations.append("missing_body_paragraphs")
    elif len(paragraphs) > 6:
        violations.append("too_many_body_paragraphs")
    if any(_EMAIL_REQUEST_IN_BODY.search(paragraph) for paragraph in paragraphs):
        violations.append("request_content_in_body")
    if any(_EMAIL_COURTESY_IN_BODY.search(paragraph) for paragraph in paragraphs):
        violations.append("courtesy_content_in_body")
    signatures = _text_list(arguments.get("signature_lines"))
    if not signatures:
        violations.append("missing_signature")
    elif len(signatures) > 4:
        violations.append("signature_too_long")

    sender_name = _text(arguments.get("sender_name"))
    profile_name = _text((email_profile or {}).get("sender_name"))
    if profile_name and sender_name and sender_name != profile_name:
        violations.append("sender_identity_mismatch")
    elif not profile_name and sender_name and sender_name != "[Your Name]":
        violations.append("untrusted_sender_identity")

    introduction = _text(arguments.get("introduction"))
    request_or_next_step = _text(arguments.get("request_or_next_step"))
    courtesy = _text(arguments.get("courtesy"))
    introduction_required = introduction_policy == "required" or (
        introduction_policy != "omit"
        and relationship in ("first_contact", "unknown")
    )
    if relationship == "first_contact" and introduction_policy == "omit":
        violations.append("first_contact_introduction_omitted")
    elif introduction_required and not introduction:
        violations.append("missing_introduction")
    if introduction and sender_name and sender_name.casefold() not in introduction.casefold():
        violations.append("introduction_missing_sender")
    if signatures and sender_name and not any(
        sender_name.casefold() in line.casefold() for line in signatures
    ):
        violations.append("signature_missing_sender")

    content_sections = [introduction, *paragraphs, request_or_next_step, courtesy]
    if _has_duplicate_content(content_sections):
        violations.append("duplicate_email_content")

    if language == "en-US":
        english_fields = [
            _text(arguments.get("subject")),
            _text(arguments.get("salutation")),
            introduction,
            *paragraphs,
            _text(arguments.get("request_or_next_step")),
            _text(arguments.get("courtesy")),
            _text(arguments.get("closing")),
        ]
        if any(_contains_unapproved_han(value, sender_name) for value in english_fields):
            violations.append("english_language_contamination")
        salutation = _text(arguments.get("salutation")).casefold()
        if salutation and not salutation.startswith(("dear ", "hello ", "hi ", "to ")):
            violations.append("english_salutation_not_polite")
        courtesy = _text(arguments.get("courtesy")).casefold()
        if courtesy and not any(
            token in courtesy for token in ("thank", "appreciat", "grateful")
        ):
            violations.append("english_courtesy_missing")
        closing = _text(arguments.get("closing")).casefold()
        if closing and not any(
            token in closing
            for token in ("sincerely", "regards", "respectfully", "best wishes", "yours")
        ):
            violations.append("english_closing_invalid")

    return _unique(violations)


def quality_report(violations: list[str]) -> dict[str, Any]:
    return {
        "passed": not violations,
        "policy_version": EMAIL_DRAFT_POLICY_VERSION,
        "violations": list(violations),
    }


def _contains_unapproved_han(value: str, sender_name: str) -> bool:
    if not value:
        return False
    candidate = value.replace(sender_name, "") if sender_name else value
    return _HAN.search(candidate) is not None


def _has_duplicate_content(values: list[str]) -> bool:
    for index, value in enumerate(values):
        if not value:
            continue
        for candidate in values[index + 1 :]:
            if candidate and _email_content_similar(value, candidate):
                return True
    return False


def _email_content_similar(left: str, right: str) -> bool:
    left_normalized = "".join(_COMPARABLE_TOKEN.findall(left.casefold()))
    right_normalized = "".join(_COMPARABLE_TOKEN.findall(right.casefold()))
    if not left_normalized or not right_normalized:
        return False
    if left_normalized == right_normalized:
        return True
    shorter, longer = sorted((left_normalized, right_normalized), key=len)
    if len(shorter) >= 24 and shorter in longer and len(shorter) / len(longer) >= 0.7:
        return True

    left_tokens = _content_token_set(left)
    right_tokens = _content_token_set(right)
    if min(len(left_tokens), len(right_tokens)) < 4:
        return False
    overlap = len(left_tokens & right_tokens)
    if overlap < 4:
        return False
    coverage = overlap / min(len(left_tokens), len(right_tokens))
    union = len(left_tokens | right_tokens)
    return coverage >= 0.8 and union > 0 and overlap / union >= 0.55


def _content_token_set(value: str) -> set[str]:
    return {
        token
        for token in _COMPARABLE_TOKEN.findall(value.casefold())
        if token not in _DUPLICATE_STOP_WORDS
    }


def _text(value: Any) -> str:
    return value.strip() if isinstance(value, str) else ""


def _text_list(value: Any) -> list[str]:
    if not isinstance(value, list):
        return []
    return [item.strip() for item in value if isinstance(item, str) and item.strip()]


def _unique(values: list[str]) -> list[str]:
    result: list[str] = []
    for value in values:
        if value not in result:
            result.append(value)
    return result
