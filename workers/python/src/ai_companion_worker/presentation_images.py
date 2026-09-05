from __future__ import annotations

import base64
import hashlib
import io
import json
import os
import re
import time
import urllib.error
import urllib.request
from dataclasses import dataclass
from typing import Any, Mapping

from PIL import Image

from ai_companion_worker.result_cache import load_json_result, store_json_result


PRESENTATION_IMAGE_GENERATOR_VERSION = "presentation-image-generator-v2"
MAX_VISUAL_BYTES = 2 * 1024 * 1024
MAX_RESPONSE_BYTES = 12 * 1024 * 1024


@dataclass(frozen=True)
class PresentationImageConfig:
    base_url: str
    api_key: str
    model: str
    http_referer: str
    app_title: str

    @classmethod
    def from_env(cls) -> PresentationImageConfig:
        return cls(
            base_url=os.environ.get(
                "MODEL_BASE_URL", "https://openrouter.ai/api/v1"
            ).rstrip("/"),
            api_key=os.environ.get("MODEL_API_KEY", "").strip(),
            model=os.environ.get("MODEL_PRESENTATION_IMAGE_NAME", "").strip(),
            http_referer=os.environ.get("MODEL_HTTP_REFERER", "http://localhost:3000"),
            app_title=os.environ.get("MODEL_APP_TITLE", "伴AI"),
        )

    def validate(self) -> None:
        if self.base_url != "https://openrouter.ai/api/v1":
            raise ValueError("presentation_image_endpoint_is_invalid")
        if (
            not self.api_key
            or not self.model
            or self.model.startswith(("openrouter/auto", "auto"))
        ):
            raise ValueError("presentation_image_model_is_not_configured")


def generate_presentation_visual(
    topic: Mapping[str, Any],
    *,
    config: PresentationImageConfig,
    reserved_cost_micros: int,
) -> tuple[dict[str, Any], dict[str, Any]]:
    """Generate exactly one independent visual, with no deck/layout coupling."""

    config.validate()
    prompt = presentation_image_prompt(topic)
    cache_key = hashlib.sha256(
        json.dumps(
            {
                "version": PRESENTATION_IMAGE_GENERATOR_VERSION,
                "model": config.model,
                "prompt": prompt,
                "aspect_ratio": "16:9",
                "quality": "medium",
                "output_format": "jpeg",
                "output_compression": 84,
            },
            ensure_ascii=False,
            sort_keys=True,
            separators=(",", ":"),
        ).encode("utf-8")
    ).hexdigest()
    cached = load_json_result("presentation-images", cache_key)
    cached_result = _cached_visual(cached, config=config, prompt=prompt)
    if cached_result is not None:
        return cached_result

    body = json.dumps(
        {
            "model": config.model,
            "prompt": prompt,
            "n": 1,
            "aspect_ratio": "16:9",
            "quality": "medium",
            "output_format": "jpeg",
            "output_compression": 84,
        },
        ensure_ascii=False,
    ).encode("utf-8")
    request = urllib.request.Request(
        config.base_url + "/images",
        data=body,
        method="POST",
        headers={
            "Authorization": "Bearer " + config.api_key,
            "Content-Type": "application/json",
            "HTTP-Referer": config.http_referer,
            "X-OpenRouter-Title": config.app_title,
        },
    )
    started_ns = time.perf_counter_ns()
    try:
        with urllib.request.urlopen(request, timeout=120) as response:
            raw = response.read(MAX_RESPONSE_BYTES + 1)
    except urllib.error.HTTPError as exc:
        raise ValueError(f"presentation_image_http_{exc.code}") from exc
    except Exception as exc:
        raise ValueError("presentation_image_request_failed") from exc
    latency_ms = max(0, (time.perf_counter_ns() - started_ns) // 1_000_000)
    if len(raw) > MAX_RESPONSE_BYTES:
        raise ValueError("presentation_image_response_too_large")
    try:
        response_payload = json.loads(raw)
        image_item = response_payload["data"][0]
        image_data = base64.b64decode(image_item["b64_json"], validate=True)
    except (KeyError, IndexError, TypeError, ValueError, json.JSONDecodeError) as exc:
        raise ValueError("presentation_image_response_invalid") from exc
    image_data, media_type, width, height = normalize_visual_image(image_data)
    usage_payload = response_payload.get("usage")
    usage_payload = usage_payload if isinstance(usage_payload, dict) else {}
    reported_cost = _reported_cost_micros(usage_payload.get("cost"))
    cost_micros = reported_cost if reported_cost is not None else reserved_cost_micros
    visual = {
        "kind": "generated",
        "data": image_data,
        "media_type": media_type,
        "width": width,
        "height": height,
        "model": config.model,
        "prompt": prompt,
        "source_locator": "AI 生成配图",
    }
    usage = {
        "provider": "openrouter",
        "requested_model": config.model,
        "returned_model": str(response_payload.get("model") or config.model),
        "upstream_provider": str(response_payload.get("provider") or ""),
        "prompt_tokens": _non_negative_int(usage_payload.get("prompt_tokens")),
        "completion_tokens": _non_negative_int(usage_payload.get("completion_tokens")),
        "cost_micros": cost_micros,
        "cost_accounting": (
            "reported" if reported_cost is not None else "reserved_upper_bound"
        ),
        "latency_ms": latency_ms,
        "cache_hit": False,
    }
    store_json_result(
        "presentation-images",
        cache_key,
        {
            "image_base64": base64.b64encode(image_data).decode("ascii"),
            "media_type": media_type,
            "width": width,
            "height": height,
            "returned_model": usage["returned_model"],
            "upstream_provider": usage["upstream_provider"],
        },
    )
    return visual, usage


def presentation_image_prompt(topic: Mapping[str, Any]) -> str:
    title = re.sub(r"\s+", " ", str(topic.get("title") or "")).strip()[:160]
    bullets = [
        re.sub(r"\s+", " ", str(value or "")).strip()[:220]
        for value in topic.get("bullets", [])
        if str(value or "").strip()
    ][:4]
    return (
        "Create one clean 16:9 editorial presentation illustration. "
        "Use a coherent professional visual metaphor, realistic or polished 3D style, "
        "ample negative space, and no words, letters, numerals, logos, watermarks, UI, "
        f"or decorative borders. Topic: {title}. Context: {'; '.join(bullets)}"
    )


def normalize_visual_image(data: bytes) -> tuple[bytes, str, int, int]:
    try:
        with Image.open(io.BytesIO(data)) as image:
            width, height = image.size
            if width < 320 or height < 180 or width * height > 25_000_000:
                raise ValueError("presentation_image_dimensions_are_invalid")
            image.load()
            image_format = str(image.format or "").upper()
            if image_format in {"JPEG", "PNG"} and len(data) <= MAX_VISUAL_BYTES:
                return data, "image/jpeg" if image_format == "JPEG" else "image/png", width, height
            converted = image.convert("RGB")
            output = io.BytesIO()
            converted.save(output, format="JPEG", quality=84, optimize=True)
    except ValueError:
        raise
    except Exception as exc:
        raise ValueError("presentation_image_is_invalid") from exc
    normalized = output.getvalue()
    if len(normalized) > MAX_VISUAL_BYTES:
        raise ValueError("presentation_image_is_too_large")
    return normalized, "image/jpeg", width, height


def _cached_visual(
    value: Any,
    *,
    config: PresentationImageConfig,
    prompt: str,
) -> tuple[dict[str, Any], dict[str, Any]] | None:
    if not isinstance(value, dict):
        return None
    try:
        data = base64.b64decode(str(value["image_base64"]), validate=True)
        data, media_type, width, height = normalize_visual_image(data)
    except (KeyError, TypeError, ValueError):
        return None
    return {
        "kind": "generated",
        "data": data,
        "media_type": media_type,
        "width": width,
        "height": height,
        "model": config.model,
        "prompt": prompt,
        "source_locator": "AI 生成配图",
    }, {
        "provider": "cache",
        "requested_model": config.model,
        "returned_model": str(value.get("returned_model") or config.model),
        "upstream_provider": str(value.get("upstream_provider") or ""),
        "prompt_tokens": 0,
        "completion_tokens": 0,
        "cost_micros": 0,
        "cost_accounting": "cache_hit",
        "latency_ms": 0,
        "cache_hit": True,
    }


def _reported_cost_micros(value: Any) -> int | None:
    try:
        amount = float(value)
    except (TypeError, ValueError):
        return None
    if amount < 0:
        return None
    return round(amount * 1_000_000)


def _non_negative_int(value: Any) -> int:
    return value if isinstance(value, int) and not isinstance(value, bool) and value >= 0 else 0
