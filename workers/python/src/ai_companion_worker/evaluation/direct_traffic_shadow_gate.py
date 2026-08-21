from __future__ import annotations

import argparse
import hashlib
import hmac
import json
import os
import re
import sys
from dataclasses import asdict, dataclass
from datetime import datetime, timedelta
from pathlib import Path
from typing import Any, Literal, Mapping, Sequence

from .deployment_controller import (
    _canonical_json,
    _digest,
    _format_timestamp,
    _normalize_now,
    _parse_timestamp,
)
from .deployment_resolution import (
    DeploymentResolutionError,
    _ensure_private_state_directory,
    _read_contract,
    _validate_key,
    _validate_key_id,
    _validate_private_state_directory,
    _write_exclusive_private_json,
)
from .direct_rollout_traffic import provider_instance_sha256
from .direct_traffic_shadow import (
    DirectTrafficShadowError,
    DirectTrafficShadowHistory,
    ReadOnlyDirectTrafficProviderConfig,
    observer_instance_sha256,
)
from .release_attestation import MAX_CLOCK_SKEW_SECONDS
from .release_gate import (
    DEFAULT_CANARY_LEASE_ROOT,
    ReleaseGateError,
    try_acquire_canary_lease,
)
from .release_gate_history import ReleaseGateHistoryError, environment_sha256


GATE_SCHEMA_VERSION = "agent-direct-traffic-shadow-gate-v1"
ATTESTATION_SCHEMA_VERSION = "agent-direct-traffic-shadow-attestation-v1"
GATE_REPORT_FILE = "direct-traffic-shadow-gate-report.json"
ATTESTATION_FILE = "direct-traffic-shadow-attestation.json"
ATTESTATION_ALGORITHM = "hmac-sha256"
KEY_ENV = "OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_GATE_KEY"
KEY_ID_ENV = "OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_GATE_KEY_ID"
PRODUCTION_KEY_ENV = "OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_SHADOW_GATE_KEY"
PRODUCTION_KEY_ID_ENV = "OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_SHADOW_GATE_KEY_ID"
PRODUCTION_RECOVERY_KEY_ENV = (
    "OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_RECOVERY_SHADOW_GATE_KEY"
)
PRODUCTION_RECOVERY_KEY_ID_ENV = (
    "OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_RECOVERY_SHADOW_GATE_KEY_ID"
)
PRODUCTION_EXPANSION_KEY_ENV = (
    "OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_SHADOW_GATE_KEY"
)
PRODUCTION_EXPANSION_KEY_ID_ENV = (
    "OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_SHADOW_GATE_KEY_ID"
)
PRODUCTION_EXPANSION_25_KEY_ENV = (
    "OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_25_SHADOW_GATE_KEY"
)
PRODUCTION_EXPANSION_25_KEY_ID_ENV = (
    "OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_25_SHADOW_GATE_KEY_ID"
)
PRODUCTION_EXPANSION_50_KEY_ENV = (
    "OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_50_SHADOW_GATE_KEY"
)
PRODUCTION_EXPANSION_50_KEY_ID_ENV = (
    "OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_50_SHADOW_GATE_KEY_ID"
)
PRODUCTION_EXPANSION_100_KEY_ENV = (
    "OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_100_SHADOW_GATE_KEY"
)
PRODUCTION_EXPANSION_100_KEY_ID_ENV = (
    "OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_100_SHADOW_GATE_KEY_ID"
)
MAX_ATTESTATION_SECONDS = 24 * 60 * 60
_SIGNING_DOMAIN = b"ai-companion/direct-traffic-shadow-attestation/v1\0"
_GATE_ID_RE = re.compile(r"direct-traffic-shadow-gate-[0-9a-f]{32}")
_ATTESTATION_ID_RE = re.compile(r"direct-traffic-shadow-attestation-[0-9a-f]{32}")
_VIOLATION_CODE_RE = re.compile(r"[a-z][a-z0-9_]{0,63}")
_DIGEST_RE = re.compile(r"[0-9a-f]{64}")


class DirectTrafficShadowGateError(ValueError):
    pass


class DirectTrafficShadowGateConfigError(DirectTrafficShadowGateError):
    pass


class DirectTrafficShadowGateVerificationError(DirectTrafficShadowGateError):
    pass


@dataclass(frozen=True)
class DirectTrafficShadowGatePolicy:
    fast_window_runs: int = 3
    stable_window_runs: int = 30
    minimum_stable_window_seconds: int = 15 * 60
    maximum_gap_seconds: int = 90
    maximum_report_age_seconds: int = 90
    maximum_retried_observations: int = 3


def evaluate_direct_traffic_shadow_history(
    history: DirectTrafficShadowHistory,
    policy: DirectTrafficShadowGatePolicy = DirectTrafficShadowGatePolicy(),
    *,
    now: datetime | None = None,
) -> dict[str, Any]:
    validated_policy = _validate_policy(policy)
    current_time = _normalize_now(now)
    events = history.events()
    fast = events[-validated_policy.fast_window_runs :]
    stable = events[-validated_policy.stable_window_runs :]
    fast_summary = _summarize_window(
        fast,
        requested=validated_policy.fast_window_runs,
        now=current_time,
    )
    stable_summary = _summarize_window(
        stable,
        requested=validated_policy.stable_window_runs,
        now=current_time,
    )
    violations: list[dict[str, Any]] = []
    _append_violation(
        violations,
        "insufficient_fast_runs",
        fast_summary["selected"] >= validated_policy.fast_window_runs,
        f">={validated_policy.fast_window_runs}",
        fast_summary["selected"],
    )
    _append_violation(
        violations,
        "recent_drift",
        fast_summary["drift"] == 0,
        "0",
        fast_summary["drift"],
    )
    _append_violation(
        violations,
        "recent_lookup_errors",
        fast_summary["lookup_error"] == 0,
        "0",
        fast_summary["lookup_error"],
    )
    _append_violation(
        violations,
        "insufficient_stable_runs",
        stable_summary["selected"] >= validated_policy.stable_window_runs,
        f">={validated_policy.stable_window_runs}",
        stable_summary["selected"],
    )
    _append_violation(
        violations,
        "stable_drift",
        stable_summary["drift"] == 0,
        "0",
        stable_summary["drift"],
    )
    _append_violation(
        violations,
        "stable_lookup_errors",
        stable_summary["lookup_error"] == 0,
        "0",
        stable_summary["lookup_error"],
    )
    _append_violation(
        violations,
        "insufficient_stable_window",
        stable_summary["coverage_seconds"] >= validated_policy.minimum_stable_window_seconds,
        f">={validated_policy.minimum_stable_window_seconds}",
        stable_summary["coverage_seconds"],
    )
    _append_violation(
        violations,
        "observation_gap",
        stable_summary["maximum_gap_seconds"] <= validated_policy.maximum_gap_seconds,
        f"<={validated_policy.maximum_gap_seconds}",
        stable_summary["maximum_gap_seconds"],
    )
    _append_violation(
        violations,
        "observation_stale",
        stable_summary["latest_age_seconds"] is not None
        and stable_summary["latest_age_seconds"] <= validated_policy.maximum_report_age_seconds,
        f"<={validated_policy.maximum_report_age_seconds}",
        stable_summary["latest_age_seconds"],
    )
    _append_violation(
        violations,
        "future_observation",
        stable_summary["future_observations"] == 0,
        "0",
        stable_summary["future_observations"],
    )
    _append_violation(
        violations,
        "non_monotonic_observations",
        stable_summary["non_monotonic_observations"] == 0,
        "0",
        stable_summary["non_monotonic_observations"],
    )
    _append_violation(
        violations,
        "excessive_retries",
        stable_summary["retried_observations"] <= validated_policy.maximum_retried_observations,
        f"<={validated_policy.maximum_retried_observations}",
        stable_summary["retried_observations"],
    )
    history_chain = str(events[-1]["chain_sha256"]) if events else "0" * 64
    source = {
        "observer_instance_sha256": history.observer_instance_sha256,
        "provider_instance_sha256": history.provider_instance_sha256,
        "environment_sha256": history.environment_sha256,
        "history_events": len(events),
        "history_chain_sha256": history_chain,
    }
    generated_at = _format_timestamp(current_time)
    gate_seed = {
        "generated_at": generated_at,
        "source": source,
        "policy": asdict(validated_policy),
        "windows": {"fast": fast_summary, "stable": stable_summary},
        "violations": violations,
    }
    report: dict[str, Any] = {
        "schema_version": GATE_SCHEMA_VERSION,
        "gate_id": f"direct-traffic-shadow-gate-{_digest(_canonical_json(gate_seed))[:32]}",
        "generated_at": generated_at,
        "decision": "pass" if not violations else "hold",
        "source": source,
        "policy": asdict(validated_policy),
        "windows": {"fast": fast_summary, "stable": stable_summary},
        "violations": violations,
    }
    return _validate_gate_report(report)


def write_direct_traffic_shadow_gate_report(
    output_dir: Path,
    report: Mapping[str, Any],
) -> Path:
    validated = _validate_gate_report(report)
    try:
        _ensure_private_state_directory(output_dir.parent)
        _ensure_private_state_directory(output_dir)
    except DeploymentResolutionError as exc:
        raise DirectTrafficShadowGateVerificationError(
            "cannot create private direct traffic shadow Gate directory"
        ) from exc
    try:
        entries = list(os.scandir(output_dir))
    except OSError as exc:
        raise DirectTrafficShadowGateVerificationError(
            f"cannot list direct traffic shadow Gate directory: {exc}"
        ) from exc
    if entries:
        raise DirectTrafficShadowGateVerificationError(
            "direct traffic shadow Gate directory must be empty"
        )
    path = output_dir / GATE_REPORT_FILE
    _write_exclusive_private_json(path, validated)
    return path


def create_direct_traffic_shadow_attestation(
    gate_dir: Path,
    history: DirectTrafficShadowHistory,
    key: bytes,
    key_id: str,
    *,
    ttl_seconds: int = 900,
    max_gate_age_seconds: int = 900,
    now: datetime | None = None,
) -> tuple[dict[str, Any], bool]:
    signing_key = _validate_key(key, "direct traffic shadow Gate key")
    signing_key_id = _validate_key_id(key_id, "direct traffic shadow Gate key_id")
    ttl = _validate_int(
        ttl_seconds,
        "direct traffic shadow attestation TTL",
        1,
        MAX_ATTESTATION_SECONDS,
    )
    maximum_gate_age = _validate_int(
        max_gate_age_seconds,
        "direct traffic shadow Gate maximum age",
        1,
        MAX_ATTESTATION_SECONDS,
    )
    current_time = _normalize_now(now)
    report = _read_gate_report(gate_dir, expect_attestation=None)
    if report["decision"] != "pass":
        raise DirectTrafficShadowGateVerificationError(
            "only a passing direct traffic shadow Gate can be attested"
        )
    _verify_live_history_binding(report, history)
    _verify_report_evaluation(report, history)
    generated_at = _parse_timestamp(
        str(report["generated_at"]), "direct traffic shadow Gate generated_at"
    )
    if generated_at > current_time + timedelta(seconds=MAX_CLOCK_SKEW_SECONDS):
        raise DirectTrafficShadowGateVerificationError(
            "direct traffic shadow Gate timestamp is in the future"
        )
    if current_time - generated_at > timedelta(seconds=maximum_gate_age):
        raise DirectTrafficShadowGateVerificationError(
            "direct traffic shadow Gate report is too old to attest"
        )
    report_sha256 = _digest(_canonical_json(report))
    payload: dict[str, Any] = {
        "schema_version": ATTESTATION_SCHEMA_VERSION,
        "attestation_id": "",
        "created_at": _format_timestamp(current_time),
        "expires_at": _format_timestamp(current_time + timedelta(seconds=ttl)),
        "gate_id": report["gate_id"],
        "decision": report["decision"],
        "observer_instance_sha256": report["source"]["observer_instance_sha256"],
        "provider_instance_sha256": report["source"]["provider_instance_sha256"],
        "environment_sha256": report["source"]["environment_sha256"],
        "history_events": report["source"]["history_events"],
        "history_chain_sha256": report["source"]["history_chain_sha256"],
        "gate_report_sha256": report_sha256,
        "key_id": signing_key_id,
        "algorithm": ATTESTATION_ALGORITHM,
    }
    identity = {field: value for field, value in payload.items() if field != "attestation_id"}
    payload["attestation_id"] = (
        f"direct-traffic-shadow-attestation-{_digest(_canonical_json(identity))[:32]}"
    )
    attestation = dict(payload)
    attestation["signature"] = _sign(signing_key, payload)
    attestation = _validate_attestation(attestation)
    path = gate_dir / ATTESTATION_FILE
    try:
        _write_exclusive_private_json(path, attestation)
        return attestation, True
    except FileExistsError:
        existing = verify_direct_traffic_shadow_attestation(
            gate_dir,
            history,
            signing_key,
            signing_key_id,
            require_decision="pass",
            now=current_time,
        )
        if existing["gate_report_sha256"] != report_sha256:
            raise DirectTrafficShadowGateVerificationError(
                "existing direct traffic shadow attestation is bound to another report"
            )
        return existing, False


def verify_direct_traffic_shadow_attestation(
    gate_dir: Path,
    history: DirectTrafficShadowHistory,
    key: bytes,
    expected_key_id: str,
    *,
    max_age_seconds: int = 900,
    require_decision: Literal["pass", "hold", "any"] = "pass",
    now: datetime | None = None,
) -> dict[str, Any]:
    verification_key = _validate_key(key, "direct traffic shadow Gate key")
    key_id = _validate_key_id(expected_key_id, "direct traffic shadow Gate key_id")
    maximum_age = _validate_int(
        max_age_seconds,
        "direct traffic shadow attestation maximum age",
        1,
        MAX_ATTESTATION_SECONDS,
    )
    if require_decision not in {"pass", "hold", "any"}:
        raise DirectTrafficShadowGateConfigError(
            "direct traffic shadow required decision is invalid"
        )
    current_time = _normalize_now(now)
    report = _read_gate_report(gate_dir, expect_attestation=True)
    _verify_live_history_binding(report, history)
    _verify_report_evaluation(report, history)
    attestation = _read_contract(
        gate_dir / ATTESTATION_FILE,
        _validate_attestation,
    )
    if attestation["key_id"] != key_id:
        raise DirectTrafficShadowGateVerificationError(
            "direct traffic shadow attestation key_id is not trusted"
        )
    payload = {field: value for field, value in attestation.items() if field != "signature"}
    if not hmac.compare_digest(str(attestation["signature"]), _sign(verification_key, payload)):
        raise DirectTrafficShadowGateVerificationError(
            "direct traffic shadow attestation signature is invalid"
        )
    report_sha256 = _digest(_canonical_json(report))
    expected = {
        "gate_id": report["gate_id"],
        "decision": report["decision"],
        "observer_instance_sha256": report["source"]["observer_instance_sha256"],
        "provider_instance_sha256": report["source"]["provider_instance_sha256"],
        "environment_sha256": report["source"]["environment_sha256"],
        "history_events": report["source"]["history_events"],
        "history_chain_sha256": report["source"]["history_chain_sha256"],
        "gate_report_sha256": report_sha256,
    }
    if any(attestation.get(field) != value for field, value in expected.items()):
        raise DirectTrafficShadowGateVerificationError(
            "direct traffic shadow attestation does not match Gate evidence"
        )
    if require_decision != "any" and report["decision"] != require_decision:
        raise DirectTrafficShadowGateVerificationError(
            f"direct traffic shadow decision {report['decision']} does not satisfy "
            f"{require_decision}"
        )
    created_at = _parse_timestamp(
        str(attestation["created_at"]), "direct traffic shadow attestation created_at"
    )
    expires_at = _parse_timestamp(
        str(attestation["expires_at"]), "direct traffic shadow attestation expires_at"
    )
    generated_at = _parse_timestamp(
        str(report["generated_at"]), "direct traffic shadow Gate generated_at"
    )
    if created_at > current_time + timedelta(seconds=MAX_CLOCK_SKEW_SECONDS):
        raise DirectTrafficShadowGateVerificationError(
            "direct traffic shadow attestation timestamp is in the future"
        )
    if generated_at > created_at + timedelta(seconds=MAX_CLOCK_SKEW_SECONDS):
        raise DirectTrafficShadowGateVerificationError(
            "direct traffic shadow Gate was generated after attestation"
        )
    if expires_at <= created_at or expires_at - created_at > timedelta(
        seconds=MAX_ATTESTATION_SECONDS
    ):
        raise DirectTrafficShadowGateVerificationError(
            "direct traffic shadow attestation lifetime is invalid"
        )
    if current_time >= expires_at:
        raise DirectTrafficShadowGateVerificationError(
            "direct traffic shadow attestation has expired"
        )
    if current_time - created_at > timedelta(seconds=maximum_age):
        raise DirectTrafficShadowGateVerificationError(
            "direct traffic shadow attestation is too old"
        )
    if current_time - generated_at > timedelta(seconds=maximum_age):
        raise DirectTrafficShadowGateVerificationError(
            "direct traffic shadow Gate report is too old"
        )
    return attestation


def _verify_live_history_binding(
    report: Mapping[str, Any], history: DirectTrafficShadowHistory
) -> None:
    events = history.events()
    chain = str(events[-1]["chain_sha256"]) if events else "0" * 64
    expected = {
        "observer_instance_sha256": history.observer_instance_sha256,
        "provider_instance_sha256": history.provider_instance_sha256,
        "environment_sha256": history.environment_sha256,
        "history_events": len(events),
        "history_chain_sha256": chain,
    }
    source = report["source"]
    if any(source.get(field) != value for field, value in expected.items()):
        raise DirectTrafficShadowGateVerificationError(
            "direct traffic shadow Gate is invalidated by current history"
        )


def _verify_report_evaluation(
    report: Mapping[str, Any], history: DirectTrafficShadowHistory
) -> None:
    policy = DirectTrafficShadowGatePolicy(**dict(report["policy"]))
    generated_at = _parse_timestamp(
        str(report["generated_at"]), "direct traffic shadow Gate generated_at"
    )
    expected = evaluate_direct_traffic_shadow_history(
        history,
        policy,
        now=generated_at,
    )
    if not hmac.compare_digest(
        _canonical_json(expected),
        _canonical_json(dict(report)),
    ):
        raise DirectTrafficShadowGateVerificationError(
            "direct traffic shadow Gate evaluation does not match current history"
        )


def _summarize_window(
    events: Sequence[Mapping[str, Any]],
    *,
    requested: int,
    now: datetime,
) -> dict[str, Any]:
    times = [
        _parse_timestamp(str(event["observed_at"]), "shadow Gate observed_at") for event in events
    ]
    gaps = [max(0.0, (right - left).total_seconds()) for left, right in zip(times, times[1:])]
    future = sum(observed_at > now for observed_at in times)
    non_monotonic = sum(right <= left for left, right in zip(times, times[1:]))
    latest_age = max(0.0, (now - times[-1]).total_seconds()) if times and times[-1] <= now else None
    counts = {"match": 0, "drift": 0, "lookup_error": 0}
    retried = 0
    for event in events:
        counts[str(event["outcome"])] += 1
        retried += int(int(event["attempts"]) > 1)
    return {
        "requested": requested,
        "selected": len(events),
        "match": counts["match"],
        "drift": counts["drift"],
        "lookup_error": counts["lookup_error"],
        "retried_observations": retried,
        "coverage_seconds": (
            max(0.0, (times[-1] - times[0]).total_seconds()) if len(times) >= 2 else 0.0
        ),
        "maximum_gap_seconds": max(gaps, default=0.0),
        "latest_age_seconds": latest_age,
        "future_observations": future,
        "non_monotonic_observations": non_monotonic,
        "first_event_id": events[0]["event_id"] if events else None,
        "last_event_id": events[-1]["event_id"] if events else None,
    }


def _validate_policy(
    value: DirectTrafficShadowGatePolicy,
) -> DirectTrafficShadowGatePolicy:
    _validate_int(value.fast_window_runs, "fast window runs", 1, 10_000)
    _validate_int(value.stable_window_runs, "stable window runs", 2, 10_000)
    if value.fast_window_runs > value.stable_window_runs:
        raise DirectTrafficShadowGateConfigError("fast window cannot exceed stable window")
    _validate_int(
        value.minimum_stable_window_seconds,
        "minimum stable window seconds",
        1,
        7 * 24 * 60 * 60,
    )
    _validate_int(
        value.maximum_gap_seconds,
        "maximum gap seconds",
        1,
        24 * 60 * 60,
    )
    if value.maximum_gap_seconds > value.minimum_stable_window_seconds:
        raise DirectTrafficShadowGateConfigError("maximum gap cannot exceed the stable window")
    _validate_int(
        value.maximum_report_age_seconds,
        "maximum report age seconds",
        1,
        24 * 60 * 60,
    )
    _validate_int(
        value.maximum_retried_observations,
        "maximum retried observations",
        0,
        value.stable_window_runs,
    )
    return value


def _validate_gate_report(value: Any) -> dict[str, Any]:
    fields = {
        "schema_version",
        "gate_id",
        "generated_at",
        "decision",
        "source",
        "policy",
        "windows",
        "violations",
    }
    if not isinstance(value, Mapping) or set(value) != fields:
        raise DirectTrafficShadowGateVerificationError("shadow Gate report is invalid")
    report = dict(value)
    if report["schema_version"] != GATE_SCHEMA_VERSION:
        raise DirectTrafficShadowGateVerificationError("shadow Gate schema_version is invalid")
    if not isinstance(report["gate_id"], str) or _GATE_ID_RE.fullmatch(report["gate_id"]) is None:
        raise DirectTrafficShadowGateVerificationError("shadow Gate ID is invalid")
    _parse_timestamp(str(report["generated_at"]), "shadow Gate generated_at")
    if report["decision"] not in {"pass", "hold"}:
        raise DirectTrafficShadowGateVerificationError("shadow Gate decision is invalid")
    source = _validate_source(report["source"])
    policy = _validate_policy_mapping(report["policy"])
    windows_value = report["windows"]
    if not isinstance(windows_value, Mapping) or set(windows_value) != {
        "fast",
        "stable",
    }:
        raise DirectTrafficShadowGateVerificationError("shadow Gate windows are invalid")
    windows = {
        "fast": _validate_window(windows_value["fast"]),
        "stable": _validate_window(windows_value["stable"]),
    }
    if (
        windows["fast"]["requested"] != policy["fast_window_runs"]
        or windows["stable"]["requested"] != policy["stable_window_runs"]
        or windows["fast"]["selected"] > windows["stable"]["selected"]
        or windows["stable"]["selected"] > source["history_events"]
    ):
        raise DirectTrafficShadowGateVerificationError("shadow Gate window binding is invalid")
    violations = _validate_violations(report["violations"])
    if (report["decision"] == "pass") != (not violations):
        raise DirectTrafficShadowGateVerificationError(
            "shadow Gate decision does not match violations"
        )
    seed = {
        "generated_at": report["generated_at"],
        "source": source,
        "policy": policy,
        "windows": windows,
        "violations": violations,
    }
    expected_id = f"direct-traffic-shadow-gate-{_digest(_canonical_json(seed))[:32]}"
    if report["gate_id"] != expected_id:
        raise DirectTrafficShadowGateVerificationError("shadow Gate ID binding is invalid")
    report["source"] = source
    report["policy"] = policy
    report["windows"] = windows
    report["violations"] = violations
    return report


def _validate_source(value: Any) -> dict[str, Any]:
    fields = {
        "observer_instance_sha256",
        "provider_instance_sha256",
        "environment_sha256",
        "history_events",
        "history_chain_sha256",
    }
    if not isinstance(value, Mapping) or set(value) != fields:
        raise DirectTrafficShadowGateVerificationError("shadow Gate source is invalid")
    result = dict(value)
    for field in (
        "observer_instance_sha256",
        "provider_instance_sha256",
        "environment_sha256",
        "history_chain_sha256",
    ):
        _validate_digest(result[field], f"shadow Gate {field}")
    _validate_int(result["history_events"], "shadow Gate history events", 0, 100_000)
    return result


def _validate_policy_mapping(value: Any) -> dict[str, Any]:
    fields = {
        "fast_window_runs",
        "stable_window_runs",
        "minimum_stable_window_seconds",
        "maximum_gap_seconds",
        "maximum_report_age_seconds",
        "maximum_retried_observations",
    }
    if not isinstance(value, Mapping) or set(value) != fields:
        raise DirectTrafficShadowGateVerificationError("shadow Gate policy is invalid")
    try:
        validated = _validate_policy(DirectTrafficShadowGatePolicy(**dict(value)))
    except (TypeError, DirectTrafficShadowGateConfigError) as exc:
        raise DirectTrafficShadowGateVerificationError("shadow Gate policy is invalid") from exc
    return asdict(validated)


def _validate_window(value: Any) -> dict[str, Any]:
    fields = {
        "requested",
        "selected",
        "match",
        "drift",
        "lookup_error",
        "retried_observations",
        "coverage_seconds",
        "maximum_gap_seconds",
        "latest_age_seconds",
        "future_observations",
        "non_monotonic_observations",
        "first_event_id",
        "last_event_id",
    }
    if not isinstance(value, Mapping) or set(value) != fields:
        raise DirectTrafficShadowGateVerificationError("shadow Gate window is invalid")
    result = dict(value)
    for field in (
        "requested",
        "selected",
        "match",
        "drift",
        "lookup_error",
        "retried_observations",
        "future_observations",
        "non_monotonic_observations",
    ):
        _validate_int(result[field], f"shadow Gate window {field}", 0, 100_000)
    for field in ("coverage_seconds", "maximum_gap_seconds"):
        result[field] = _validate_number(
            result[field], f"shadow Gate window {field}", 0.0, 7 * 24 * 60 * 60
        )
    if result["latest_age_seconds"] is not None:
        result["latest_age_seconds"] = _validate_number(
            result["latest_age_seconds"],
            "shadow Gate window latest age",
            0.0,
            7 * 24 * 60 * 60,
        )
    selected = result["selected"]
    if (
        result["match"] + result["drift"] + result["lookup_error"] != selected
        or result["requested"] < selected
        or result["retried_observations"] > selected
        or result["future_observations"] > selected
        or result["non_monotonic_observations"] > max(0, selected - 1)
    ):
        raise DirectTrafficShadowGateVerificationError("shadow Gate window counts are inconsistent")
    if selected == 0:
        if result["first_event_id"] is not None or result["last_event_id"] is not None:
            raise DirectTrafficShadowGateVerificationError(
                "empty shadow Gate window contains event IDs"
            )
    else:
        for field in ("first_event_id", "last_event_id"):
            event_id = result[field]
            if (
                not isinstance(event_id, str)
                or re.fullmatch(r"direct-traffic-shadow-event-[0-9a-f]{32}", event_id) is None
            ):
                raise DirectTrafficShadowGateVerificationError(
                    "shadow Gate window event ID is invalid"
                )
    return result


def _validate_violations(value: Any) -> list[dict[str, Any]]:
    if not isinstance(value, list):
        raise DirectTrafficShadowGateVerificationError("shadow Gate violations are invalid")
    result: list[dict[str, Any]] = []
    seen: set[str] = set()
    for item in value:
        if not isinstance(item, Mapping) or set(item) != {
            "code",
            "expected",
            "actual",
        }:
            raise DirectTrafficShadowGateVerificationError("shadow Gate violation is invalid")
        code = item["code"]
        if (
            not isinstance(code, str)
            or _VIOLATION_CODE_RE.fullmatch(code) is None
            or code in seen
            or not isinstance(item["expected"], str)
        ):
            raise DirectTrafficShadowGateVerificationError("shadow Gate violation is invalid")
        seen.add(code)
        result.append(dict(item))
    return result


def _validate_attestation(value: Any) -> dict[str, Any]:
    fields = {
        "schema_version",
        "attestation_id",
        "created_at",
        "expires_at",
        "gate_id",
        "decision",
        "observer_instance_sha256",
        "provider_instance_sha256",
        "environment_sha256",
        "history_events",
        "history_chain_sha256",
        "gate_report_sha256",
        "key_id",
        "algorithm",
        "signature",
    }
    if not isinstance(value, Mapping) or set(value) != fields:
        raise DirectTrafficShadowGateVerificationError(
            "direct traffic shadow attestation is invalid"
        )
    result = dict(value)
    if result["schema_version"] != ATTESTATION_SCHEMA_VERSION:
        raise DirectTrafficShadowGateVerificationError(
            "direct traffic shadow attestation schema is invalid"
        )
    if (
        not isinstance(result["attestation_id"], str)
        or _ATTESTATION_ID_RE.fullmatch(result["attestation_id"]) is None
    ):
        raise DirectTrafficShadowGateVerificationError(
            "direct traffic shadow attestation ID is invalid"
        )
    if not isinstance(result["gate_id"], str) or _GATE_ID_RE.fullmatch(result["gate_id"]) is None:
        raise DirectTrafficShadowGateVerificationError(
            "direct traffic shadow attestation Gate ID is invalid"
        )
    if result["decision"] not in {"pass", "hold"}:
        raise DirectTrafficShadowGateVerificationError(
            "direct traffic shadow attestation decision is invalid"
        )
    for field in (
        "observer_instance_sha256",
        "provider_instance_sha256",
        "environment_sha256",
        "history_chain_sha256",
        "gate_report_sha256",
        "signature",
    ):
        _validate_digest(result[field], f"shadow attestation {field}")
    _validate_int(result["history_events"], "shadow attestation history events", 0, 100_000)
    _parse_timestamp(str(result["created_at"]), "shadow attestation created_at")
    _parse_timestamp(str(result["expires_at"]), "shadow attestation expires_at")
    _validate_key_id(str(result["key_id"]), "shadow attestation key_id")
    if result["algorithm"] != ATTESTATION_ALGORITHM:
        raise DirectTrafficShadowGateVerificationError(
            "direct traffic shadow attestation algorithm is invalid"
        )
    payload = {
        field: item
        for field, item in result.items()
        if field not in {"signature", "attestation_id"}
    }
    expected_id = f"direct-traffic-shadow-attestation-{_digest(_canonical_json(payload))[:32]}"
    if result["attestation_id"] != expected_id:
        raise DirectTrafficShadowGateVerificationError(
            "direct traffic shadow attestation ID binding is invalid"
        )
    return result


def _read_gate_report(gate_dir: Path, *, expect_attestation: bool | None) -> dict[str, Any]:
    _validate_private_state_directory(gate_dir)
    names = {entry.name for entry in os.scandir(gate_dir)}
    expected = {GATE_REPORT_FILE}
    if expect_attestation is True:
        expected.add(ATTESTATION_FILE)
    if expect_attestation is None and ATTESTATION_FILE in names:
        expected.add(ATTESTATION_FILE)
    if names != expected:
        raise DirectTrafficShadowGateVerificationError(
            "direct traffic shadow Gate bundle is incomplete or contains extra files"
        )
    return _read_contract(gate_dir / GATE_REPORT_FILE, _validate_gate_report)


def _append_violation(
    violations: list[dict[str, Any]],
    code: str,
    passed: bool,
    expected: str,
    actual: Any,
) -> None:
    if not passed:
        violations.append({"code": code, "expected": expected, "actual": actual})


def _sign(key: bytes, payload: Mapping[str, Any]) -> str:
    derived = hmac.new(key, _SIGNING_DOMAIN, hashlib.sha256).digest()
    return hmac.new(derived, _canonical_json(payload), hashlib.sha256).hexdigest()


def _validate_digest(value: Any, label: str) -> str:
    if not isinstance(value, str) or _DIGEST_RE.fullmatch(value) is None:
        raise DirectTrafficShadowGateVerificationError(f"{label} is invalid")
    return value


def _validate_int(value: Any, label: str, minimum: int, maximum: int) -> int:
    if isinstance(value, bool) or not isinstance(value, int) or value < minimum or value > maximum:
        raise DirectTrafficShadowGateConfigError(f"{label} is invalid")
    return value


def _validate_number(value: Any, label: str, minimum: float, maximum: float) -> float:
    if (
        isinstance(value, bool)
        or not isinstance(value, (int, float))
        or not minimum <= float(value) <= maximum
    ):
        raise DirectTrafficShadowGateVerificationError(f"{label} is invalid")
    return float(value)


def _key_from_environment(*, profile: str = "standard") -> tuple[bytes, str]:
    if profile not in {
        "standard", "production", "production_recovery", "production_expansion",
        "production_expansion_25",
        "production_expansion_50",
        "production_expansion_100",
    }:
        raise DirectTrafficShadowGateConfigError("shadow Gate key profile is invalid")
    key_name, key_id_name = {
        "standard": (KEY_ENV, KEY_ID_ENV),
        "production": (PRODUCTION_KEY_ENV, PRODUCTION_KEY_ID_ENV),
        "production_recovery": (
            PRODUCTION_RECOVERY_KEY_ENV,
            PRODUCTION_RECOVERY_KEY_ID_ENV,
        ),
        "production_expansion": (
            PRODUCTION_EXPANSION_KEY_ENV,
            PRODUCTION_EXPANSION_KEY_ID_ENV,
        ),
        "production_expansion_25": (
            PRODUCTION_EXPANSION_25_KEY_ENV,
            PRODUCTION_EXPANSION_25_KEY_ID_ENV,
        ),
        "production_expansion_50": (
            PRODUCTION_EXPANSION_50_KEY_ENV,
            PRODUCTION_EXPANSION_50_KEY_ID_ENV,
        ),
        "production_expansion_100": (
            PRODUCTION_EXPANSION_100_KEY_ENV,
            PRODUCTION_EXPANSION_100_KEY_ID_ENV,
        ),
    }[profile]
    raw = os.environ.get(key_name)
    key_id = os.environ.get(key_id_name)
    if raw is None or key_id is None:
        raise DirectTrafficShadowGateConfigError(
            f"{key_name} and {key_id_name} are required"
        )
    return raw.encode("utf-8"), key_id


def _add_history_arguments(parser: argparse.ArgumentParser) -> None:
    parser.add_argument("--history-ledger", type=Path, required=True)
    parser.add_argument("--environment-id", required=True)
    parser.add_argument("--provider-name", required=True)
    parser.add_argument("--provider-base-url", required=True)
    parser.add_argument("--allowed-host", required=True)
    parser.add_argument("--provider-instance", required=True)
    parser.add_argument("--state-path", default="/v1/direct-traffic/state")
    parser.add_argument("--canary-lease-root", type=Path, default=DEFAULT_CANARY_LEASE_ROOT)


def _open_history(args: argparse.Namespace) -> DirectTrafficShadowHistory:
    environment_digest = environment_sha256(args.environment_id)
    config = ReadOnlyDirectTrafficProviderConfig(
        name=args.provider_name,
        base_url=args.provider_base_url,
        allowed_host=args.allowed_host,
        provider_instance=args.provider_instance,
        environment_sha256=environment_digest,
        token="history-binding-token",
        state_path=args.state_path,
    )
    return DirectTrafficShadowHistory(
        args.history_ledger,
        observer_instance_sha256(config),
        provider_instance_sha256(args.provider_instance),
        environment_digest,
        create=False,
    )


def main(argv: Sequence[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        description="Evaluate and sign a multi-window direct traffic shadow Gate"
    )
    subparsers = parser.add_subparsers(dest="command", required=True)
    evaluate = subparsers.add_parser("evaluate")
    _add_history_arguments(evaluate)
    evaluate.add_argument("--output-dir", type=Path, required=True)
    evaluate.add_argument("--fast-window-runs", type=int, default=3)
    evaluate.add_argument("--stable-window-runs", type=int, default=30)
    evaluate.add_argument("--minimum-stable-window-seconds", type=int, default=900)
    evaluate.add_argument("--maximum-gap-seconds", type=int, default=90)
    evaluate.add_argument("--maximum-report-age-seconds", type=int, default=90)
    evaluate.add_argument("--maximum-retried-observations", type=int, default=3)
    attest = subparsers.add_parser("attest")
    _add_history_arguments(attest)
    attest.add_argument("--gate-dir", type=Path, required=True)
    attest.add_argument("--ttl-seconds", type=int, default=900)
    attest.add_argument("--max-gate-age-seconds", type=int, default=900)
    attest.add_argument(
        "--key-profile",
        choices=(
            "standard", "production", "production_recovery", "production_expansion",
            "production_expansion_25",
            "production_expansion_50",
            "production_expansion_100",
        ),
        default="standard",
    )
    verify = subparsers.add_parser("verify")
    _add_history_arguments(verify)
    verify.add_argument("--gate-dir", type=Path, required=True)
    verify.add_argument("--max-age-seconds", type=int, default=900)
    verify.add_argument("--require-decision", choices=("pass", "hold", "any"), default="pass")
    verify.add_argument(
        "--key-profile",
        choices=(
            "standard", "production", "production_recovery", "production_expansion",
            "production_expansion_25",
            "production_expansion_50",
            "production_expansion_100",
        ),
        default="standard",
    )
    args = parser.parse_args(argv)
    lease = None
    try:
        lease = try_acquire_canary_lease(args.canary_lease_root, args.environment_id)
        if lease is None:
            raise DirectTrafficShadowGateVerificationError("environment lease is already held")
        history = _open_history(args)
        if args.command == "evaluate":
            report = evaluate_direct_traffic_shadow_history(
                history,
                DirectTrafficShadowGatePolicy(
                    fast_window_runs=args.fast_window_runs,
                    stable_window_runs=args.stable_window_runs,
                    minimum_stable_window_seconds=args.minimum_stable_window_seconds,
                    maximum_gap_seconds=args.maximum_gap_seconds,
                    maximum_report_age_seconds=args.maximum_report_age_seconds,
                    maximum_retried_observations=args.maximum_retried_observations,
                ),
            )
            path = write_direct_traffic_shadow_gate_report(args.output_dir, report)
            print(
                json.dumps(
                    {
                        "decision": report["decision"],
                        "gate_id": report["gate_id"],
                        "output": str(path),
                        "violations": len(report["violations"]),
                    },
                    sort_keys=True,
                )
            )
            return 0 if report["decision"] == "pass" else 3
        key, key_id = _key_from_environment(profile=args.key_profile)
        if args.command == "attest":
            result, created = create_direct_traffic_shadow_attestation(
                args.gate_dir,
                history,
                key,
                key_id,
                ttl_seconds=args.ttl_seconds,
                max_gate_age_seconds=args.max_gate_age_seconds,
            )
            status = "created" if created else "reused"
        else:
            result = verify_direct_traffic_shadow_attestation(
                args.gate_dir,
                history,
                key,
                key_id,
                max_age_seconds=args.max_age_seconds,
                require_decision=args.require_decision,
            )
            status = "verified"
        print(
            json.dumps(
                {
                    "attestation_id": result["attestation_id"],
                    "decision": result["decision"],
                    "expires_at": result["expires_at"],
                    "gate_id": result["gate_id"],
                    "status": status,
                },
                sort_keys=True,
            )
        )
        return 0
    except DirectTrafficShadowGateConfigError as exc:
        print(f"direct traffic shadow Gate configuration error: {exc}", file=sys.stderr)
        return 2
    except (
        DirectTrafficShadowGateError,
        DirectTrafficShadowError,
        DeploymentResolutionError,
        ReleaseGateError,
        ReleaseGateHistoryError,
        OSError,
    ) as exc:
        print(f"direct traffic shadow Gate verification error: {exc}", file=sys.stderr)
        return 1
    finally:
        if lease is not None:
            lease.release()


if __name__ == "__main__":
    raise SystemExit(main())
