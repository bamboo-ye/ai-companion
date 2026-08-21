from __future__ import annotations

import hashlib
import json
import re
import unicodedata
from typing import Any, Mapping

TOOL_FAILURE_CONTRACT_VERSION = "tool-failure-v1"
REPAIR_CATALOG_VERSION = "1.0.0"

_WINDOWS_RESERVED = {
    "CON",
    "PRN",
    "AUX",
    "NUL",
    *(f"COM{index}" for index in range(1, 10)),
    *(f"LPT{index}" for index in range(1, 10)),
}
_UNSAFE_FILENAME = re.compile(r"[<>:\"/\\|?*\x00-\x1f\x7f]+")
_DASHES = re.compile(r"[-\s]+")


def canonical_arguments_hash(arguments: Mapping[str, Any]) -> str:
    encoded = json.dumps(
        dict(arguments),
        allow_nan=False,
        ensure_ascii=False,
        separators=(",", ":"),
        sort_keys=True,
    ).encode("utf-8")
    return hashlib.sha256(encoded).hexdigest()


def safe_basename(value: str, extension: str, *, max_bytes: int = 240) -> str:
    raw = unicodedata.normalize("NFKC", value).strip()
    normalized_extension = extension.strip()
    if normalized_extension and not normalized_extension.startswith("."):
        normalized_extension = "." + normalized_extension
    if normalized_extension and raw.lower().endswith(normalized_extension.lower()):
        raw = raw[: -len(normalized_extension)]
    name = _UNSAFE_FILENAME.sub("-", raw)
    name = _DASHES.sub("-", name).strip(" .-")
    if not name:
        name = "output"
    if name.upper() in _WINDOWS_RESERVED:
        name = "output-" + name.lower()
    suffix = normalized_extension
    budget = max(16, max_bytes - len(suffix.encode("utf-8")))
    if len(name.encode("utf-8")) > budget:
        digest = hashlib.sha256(name.encode("utf-8")).hexdigest()[:10]
        prefix_budget = max(1, budget - len(digest) - 1)
        name = _truncate_utf8(name, prefix_budget).rstrip(" .-") or "output"
        name = f"{name}-{digest}"
    return name + suffix


def preflight_repair(
    arguments: Mapping[str, Any],
    definition: Mapping[str, Any],
) -> tuple[dict[str, Any], list[dict[str, Any]]]:
    repaired = dict(arguments)
    records: list[dict[str, Any]] = []
    for policy in _repair_policies(definition):
        if policy.get("preflight") is not True:
            continue
        if policy.get("operator_id") != "filename.safe_basename":
            continue
        field = _top_level_field(policy.get("field_path"))
        if not field or field not in repaired:
            continue
        current = repaired.get(field)
        if not isinstance(current, str) or not current.strip():
            continue
        normalized = safe_basename(current, str(policy.get("extension") or ""))
        if normalized == current:
            continue
        before_hash = canonical_arguments_hash(repaired)
        repaired[field] = normalized
        records.append(
            {
                "strategy": "preflight",
                "operator_id": "filename.safe_basename",
                "changed_paths": [f"/{field}"],
                "before_args_hash": before_hash,
                "after_args_hash": canonical_arguments_hash(repaired),
                "repair_catalog_version": REPAIR_CATALOG_VERSION,
            }
        )
    return repaired, records


def apply_known_repair(
    arguments: Mapping[str, Any],
    failure: Mapping[str, Any],
    definition: Mapping[str, Any],
) -> dict[str, Any] | None:
    allowed = {
        str(item).strip()
        for item in failure.get("allowed_repairs", [])
        if isinstance(item, str) and item.strip()
    }
    failure_paths = {
        str(item).strip()
        for item in failure.get("field_paths", [])
        if isinstance(item, str) and item.strip()
    }
    for policy in _repair_policies(definition):
        operator_id = str(policy.get("operator_id") or "")
        field_path = str(policy.get("field_path") or "")
        if operator_id not in allowed or field_path not in failure_paths:
            continue
        field = _top_level_field(field_path)
        if not field:
            continue
        repaired = dict(arguments)
        if operator_id == "remove_optional_filename":
            if field not in repaired:
                continue
            del repaired[field]
        elif operator_id == "filename.safe_basename":
            current = repaired.get(field)
            if not isinstance(current, str) or not current.strip():
                continue
            repaired[field] = safe_basename(
                current,
                str(policy.get("extension") or ""),
            )
        else:
            continue
        if canonical_arguments_hash(repaired) == canonical_arguments_hash(arguments):
            continue
        return {
            "strategy": "deterministic",
            "operator_id": operator_id,
            "arguments": repaired,
            "changed_paths": [field_path],
            "before_args_hash": canonical_arguments_hash(arguments),
            "after_args_hash": canonical_arguments_hash(repaired),
            "repair_catalog_version": REPAIR_CATALOG_VERSION,
        }
    return None


def apply_restricted_patches(
    arguments: Mapping[str, Any],
    patches: list[Mapping[str, Any]],
    *,
    allowed_paths: set[str],
) -> tuple[dict[str, Any], list[str]]:
    repaired = dict(arguments)
    changed: list[str] = []
    for patch in patches:
        operation = patch.get("op")
        path = patch.get("path")
        if operation not in ("replace", "remove") or not isinstance(path, str):
            raise ValueError("repair patch operation is not allowed")
        if path not in allowed_paths:
            raise ValueError("repair patch path is not allowed")
        field = _top_level_field(path)
        if not field:
            raise ValueError("only top-level repair paths are supported")
        if operation == "remove":
            if field not in repaired:
                raise ValueError("repair patch does not change arguments")
            del repaired[field]
        else:
            repaired[field] = patch.get("value")
        changed.append(path)
    return repaired, sorted(set(changed))


def repair_operator_allowed(
    definition: Mapping[str, Any],
    operator_id: str,
    changed_paths: list[str],
) -> bool:
    paths = set(changed_paths)
    if not paths:
        return False
    allowed_paths = {
        str(policy.get("field_path"))
        for policy in _repair_policies(definition)
        if policy.get("operator_id") == operator_id
        and policy.get("semantics_preserving") is True
        and isinstance(policy.get("field_path"), str)
    }
    return paths.issubset(allowed_paths)


def known_repair_available(
    failure: Mapping[str, Any],
    definition: Mapping[str, Any],
) -> bool:
    allowed = set(failure.get("allowed_repairs") or [])
    paths = set(failure.get("field_paths") or [])
    return any(
        policy.get("operator_id") in allowed
        and policy.get("field_path") in paths
        and policy.get("semantics_preserving") is True
        for policy in _repair_policies(definition)
    )


def _repair_policies(definition: Mapping[str, Any]) -> list[Mapping[str, Any]]:
    raw = definition.get("repair_policies", [])
    if not isinstance(raw, list):
        return []
    return [item for item in raw if isinstance(item, Mapping)]


def _top_level_field(path: Any) -> str:
    if not isinstance(path, str) or not path.startswith("/"):
        return ""
    field = path[1:]
    if not field or "/" in field or "~" in field:
        return ""
    return field


def _truncate_utf8(value: str, maximum_bytes: int) -> str:
    encoded = value.encode("utf-8")[:maximum_bytes]
    while encoded:
        try:
            return encoded.decode("utf-8")
        except UnicodeDecodeError:
            encoded = encoded[:-1]
    return ""
