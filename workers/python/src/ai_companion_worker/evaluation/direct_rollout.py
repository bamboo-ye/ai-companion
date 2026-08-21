from __future__ import annotations

import argparse
import hashlib
import hmac
import json
import math
import os
import re
import sys
from datetime import UTC, datetime, timedelta
from pathlib import Path
from typing import Any, Literal, Mapping, Sequence, cast

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
    _write_exclusive_private_json,
)
from .release_attestation import MAX_CLOCK_SKEW_SECONDS
from .release_gate_history import ReleaseGateHistory, ReleaseGateHistoryError


POLICY_SCHEMA_VERSION = "agent-direct-rollout-policy-v1"
REPORT_SCHEMA_VERSION = "agent-direct-rollout-report-v1"
ATTESTATION_SCHEMA_VERSION = "agent-direct-rollout-attestation-v1"
REPORT_FILE = "direct-rollout-report.json"
ATTESTATION_FILE = "direct-rollout-attestation.json"
KEY_ENV = "OBSERVABILITY_DIRECT_ROLLOUT_ATTESTATION_KEY"
KEY_ID_ENV = "OBSERVABILITY_DIRECT_ROLLOUT_ATTESTATION_KEY_ID"
PRODUCTION_KEY_ENV = "OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_ROLLOUT_KEY"
PRODUCTION_KEY_ID_ENV = "OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_ROLLOUT_KEY_ID"
PRODUCTION_EMERGENCY_KEY_ENV = (
    "OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EMERGENCY_ROLLOUT_KEY"
)
PRODUCTION_EMERGENCY_KEY_ID_ENV = (
    "OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EMERGENCY_ROLLOUT_KEY_ID"
)
PRODUCTION_RECOVERY_KEY_ENV = (
    "OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_RECOVERY_ROLLOUT_KEY"
)
PRODUCTION_RECOVERY_KEY_ID_ENV = (
    "OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_RECOVERY_ROLLOUT_KEY_ID"
)
PRODUCTION_EXPANSION_KEY_ENV = (
    "OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_ROLLOUT_KEY"
)
PRODUCTION_EXPANSION_KEY_ID_ENV = (
    "OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_ROLLOUT_KEY_ID"
)
PRODUCTION_EXPANSION_25_KEY_ENV = (
    "OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_25_ROLLOUT_KEY"
)
PRODUCTION_EXPANSION_25_KEY_ID_ENV = (
    "OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_25_ROLLOUT_KEY_ID"
)
PRODUCTION_EXPANSION_50_KEY_ENV = (
    "OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_50_ROLLOUT_KEY"
)
PRODUCTION_EXPANSION_50_KEY_ID_ENV = (
    "OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_50_ROLLOUT_KEY_ID"
)
PRODUCTION_EXPANSION_100_KEY_ENV = (
    "OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_100_ROLLOUT_KEY"
)
PRODUCTION_EXPANSION_100_KEY_ID_ENV = (
    "OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_100_ROLLOUT_KEY_ID"
)
MAX_CONFIG_BYTES = 64 * 1024
MAX_ATTESTATION_SECONDS = 24 * 60 * 60
_SIGNING_DOMAIN = b"ai-companion/agent-direct-rollout-attestation/v1"
_DECISION_ID_RE = re.compile(r"direct-rollout-[0-9a-f]{32}")
_ATTESTATION_ID_RE = re.compile(r"direct-rollout-attestation-[0-9a-f]{32}")
_DECISIONS = {"expand", "hold", "rollback", "disable"}
_MINIMUM_FIELDS = {
    "direct_rate",
    "single_model_call_rate",
    "quality_pass_rate",
    "content_pass_rate",
    "repair_success_rate",
}
_MAXIMUM_FIELDS = {
    "duration_p95_ms",
    "assistant_delivery_violations",
    "duplicate_violations",
    "response_quality_failures",
    "planned_runs",
    "tool_actions",
    "rewrite_runs",
}
_CRITICAL_CODES = {
    "assistant_delivery_regression",
    "content_regression",
    "duplicate_content_detected",
    "quality_regression",
    "response_quality_failure",
}


class DirectRolloutError(ValueError):
    pass


class DirectRolloutConfigError(DirectRolloutError):
    pass


class DirectRolloutVerificationError(DirectRolloutError):
    pass


def load_policy(path: Path) -> tuple[dict[str, Any], str]:
    try:
        metadata = path.lstat()
        raw = path.read_bytes()
    except OSError as exc:
        raise DirectRolloutConfigError("cannot read direct rollout policy") from exc
    if (
        not path.is_file()
        or path.is_symlink()
        or metadata.st_size <= 0
        or metadata.st_size > MAX_CONFIG_BYTES
        or len(raw) != metadata.st_size
    ):
        raise DirectRolloutConfigError("direct rollout policy file is unsafe")
    try:
        value = json.loads(raw, object_pairs_hook=_reject_duplicate_keys)
    except (UnicodeDecodeError, json.JSONDecodeError, ValueError) as exc:
        raise DirectRolloutConfigError("direct rollout policy JSON is invalid") from exc
    return _validate_policy(value), hashlib.sha256(raw).hexdigest()


def evaluate_rollout(
    history: ReleaseGateHistory,
    policy: Mapping[str, Any],
    policy_sha256: str,
    current_traffic_percent: int,
    *,
    now: datetime | None = None,
) -> dict[str, Any]:
    validated_policy = _validate_policy(policy)
    if not _digest_value(policy_sha256):
        raise DirectRolloutConfigError("direct rollout policy digest is invalid")
    stages = cast(list[int], validated_policy["traffic_stages"])
    if current_traffic_percent not in stages:
        raise DirectRolloutConfigError(
            "current direct rollout traffic must be an exact policy stage"
        )
    current_time = _normalize_now(now)
    all_events = history.events()
    direct_events = [
        _direct_evidence(event)
        for event in all_events
        if event.get("canary_name") == "agent-direct"
    ]
    for item in direct_events:
        item["failure_codes"] = _sample_failure_codes(item, validated_policy)
    fast_requested = int(validated_policy["fast_window_runs"])
    stable_required = int(validated_policy["stable_window_runs"])
    fast = direct_events[-fast_requested:]
    stable = direct_events[-stable_required:]
    fast_failures = _window_failure_codes(fast, validated_policy)
    stable_failures = _window_failure_codes(stable, validated_policy)
    stable_times = [
        _parse_timestamp(str(item["observed_at"]), "direct rollout observed_at")
        for item in stable
    ]
    coverage_seconds = (
        max(0.0, (stable_times[-1] - stable_times[0]).total_seconds())
        if len(stable_times) >= 2
        else 0.0
    )
    gaps = [
        max(0.0, (right - left).total_seconds())
        for left, right in zip(stable_times, stable_times[1:])
    ]
    maximum_gap_seconds = max(gaps, default=0.0)
    latest_age_seconds = (
        max(0.0, (current_time - stable_times[-1]).total_seconds())
        if stable_times and stable_times[-1] <= current_time
        else None
    )
    future_sample = bool(stable_times and stable_times[-1] > current_time)
    reasons: list[dict[str, Any]] = []
    previous_stage = stages[max(0, stages.index(current_traffic_percent) - 1)]
    next_stage = stages[min(len(stages) - 1, stages.index(current_traffic_percent) + 1)]
    critical = sorted(set(fast_failures) & _CRITICAL_CODES)
    if not direct_events:
        decision = "hold"
        target = current_traffic_percent
        reasons.append(_reason("insufficient_direct_samples", f">={stable_required}", 0))
    elif future_sample:
        decision = "hold"
        target = current_traffic_percent
        reasons.append(_reason("future_direct_sample", "false", True))
    elif latest_age_seconds is None or latest_age_seconds > float(
        validated_policy["maximum_report_age_seconds"]
    ):
        decision = "hold"
        target = current_traffic_percent
        reasons.append(
            _reason(
                "stale_direct_sample",
                f"<={validated_policy['maximum_report_age_seconds']}",
                latest_age_seconds,
            )
        )
    elif critical:
        decision = "disable"
        target = 0
        reasons.extend(_reason(code, "0", 1) for code in critical)
    elif fast_failures:
        decision = "rollback" if previous_stage < current_traffic_percent else "hold"
        target = previous_stage
        reasons.extend(_reason(code, "0", 1) for code in sorted(set(fast_failures)))
    elif len(stable) < stable_required:
        decision = "hold"
        target = current_traffic_percent
        reasons.append(
            _reason("insufficient_direct_samples", f">={stable_required}", len(stable))
        )
    elif stable_failures:
        decision = "hold"
        target = current_traffic_percent
        reasons.extend(_reason(code, "0", 1) for code in sorted(set(stable_failures)))
    elif coverage_seconds < float(validated_policy["minimum_stable_window_seconds"]):
        decision = "hold"
        target = current_traffic_percent
        reasons.append(
            _reason(
                "insufficient_stable_window",
                f">={validated_policy['minimum_stable_window_seconds']}",
                coverage_seconds,
            )
        )
    elif maximum_gap_seconds > float(validated_policy["maximum_gap_seconds"]):
        decision = "hold"
        target = current_traffic_percent
        reasons.append(
            _reason(
                "direct_sample_gap",
                f"<={validated_policy['maximum_gap_seconds']}",
                maximum_gap_seconds,
            )
        )
    elif current_traffic_percent == stages[-1]:
        decision = "hold"
        target = current_traffic_percent
        reasons.append(_reason("maximum_stage_reached", str(stages[-1]), target))
    else:
        decision = "expand"
        target = next_stage
        reasons.append(_reason("stable_window_passed", f"{stable_required}", len(stable)))

    history_chain = str(all_events[-1]["chain_sha256"]) if all_events else "0" * 64
    evidence = stable
    report: dict[str, Any] = {
        "schema_version": REPORT_SCHEMA_VERSION,
        "decision_id": "",
        "generated_at": _format_timestamp(current_time),
        "decision": decision,
        "current_traffic_percent": current_traffic_percent,
        "target_traffic_percent": target,
        "environment_sha256": history.environment_sha256,
        "source": {
            "policy_version": validated_policy["version"],
            "policy_sha256": policy_sha256,
            "history_chain_sha256": history_chain,
            "history_events": len(all_events),
            "direct_events": len(direct_events),
        },
        "windows": {
            "fast": {
                "requested": fast_requested,
                "selected": len(fast),
                "healthy": len(fast) - sum(bool(item["failure_codes"]) for item in fast),
                "failure_codes": sorted(set(fast_failures)),
            },
            "stable": {
                "required": stable_required,
                "selected": len(stable),
                "healthy": len(stable)
                - sum(bool(item["failure_codes"]) for item in stable),
                "coverage_seconds": coverage_seconds,
                "maximum_gap_seconds": maximum_gap_seconds,
                "latest_age_seconds": latest_age_seconds,
                "failure_codes": sorted(set(stable_failures)),
            },
        },
        "evidence": evidence,
        "reasons": reasons,
    }
    report["decision_id"] = _decision_id(report)
    return _validate_report(report)


def write_rollout_report(output_root: Path, report: Mapping[str, Any]) -> Path:
    validated = _validate_report(report)
    try:
        _ensure_private_state_directory(output_root)
        timestamp = datetime.now(UTC).strftime("%Y%m%dT%H%M%S%fZ")
        output_dir = output_root / f"direct-rollout-{timestamp}-{os.getpid()}"
        _ensure_private_state_directory(output_dir)
        _write_exclusive_private_json(output_dir / REPORT_FILE, validated)
    except DeploymentResolutionError as exc:
        raise DirectRolloutVerificationError("cannot write private rollout report") from exc
    return output_dir


def create_attestation(
    decision_dir: Path,
    history: ReleaseGateHistory,
    policy: Mapping[str, Any],
    policy_sha256: str,
    key: bytes,
    key_id: str,
    *,
    ttl_seconds: int = 900,
    now: datetime | None = None,
) -> tuple[dict[str, Any], bool]:
    signing_key = _validate_key(key, "direct rollout attestation key")
    signing_key_id = _validate_key_id(key_id, "direct rollout attestation key_id")
    ttl = _bounded_int(ttl_seconds, "attestation TTL", 1, MAX_ATTESTATION_SECONDS)
    current_time = _normalize_now(now)
    report = _read_and_recompute_report(
        decision_dir, history, policy, policy_sha256, expect_attestation=None
    )
    generated_at = _parse_timestamp(str(report["generated_at"]), "rollout generated_at")
    if generated_at > current_time + timedelta(seconds=MAX_CLOCK_SKEW_SECONDS):
        raise DirectRolloutVerificationError("direct rollout report is from the future")
    report_sha256 = _digest(_canonical_json(report))
    payload: dict[str, Any] = {
        "schema_version": ATTESTATION_SCHEMA_VERSION,
        "attestation_id": "",
        "created_at": _format_timestamp(current_time),
        "expires_at": _format_timestamp(current_time + timedelta(seconds=ttl)),
        "decision_id": report["decision_id"],
        "decision": report["decision"],
        "current_traffic_percent": report["current_traffic_percent"],
        "target_traffic_percent": report["target_traffic_percent"],
        "environment_sha256": report["environment_sha256"],
        "report_sha256": report_sha256,
        "history_chain_sha256": report["source"]["history_chain_sha256"],
        "key_id": signing_key_id,
        "algorithm": "hmac-sha256",
    }
    identity = {field: value for field, value in payload.items() if field != "attestation_id"}
    payload["attestation_id"] = (
        f"direct-rollout-attestation-{_digest(_canonical_json(identity))[:32]}"
    )
    attestation = dict(payload)
    attestation["signature"] = _sign(signing_key, payload)
    attestation = _validate_attestation(attestation)
    path = decision_dir / ATTESTATION_FILE
    try:
        _write_exclusive_private_json(path, attestation)
        return attestation, True
    except FileExistsError:
        existing = verify_attestation(
            decision_dir,
            history,
            policy,
            policy_sha256,
            signing_key,
            signing_key_id,
            require_decision="any",
            now=current_time,
        )
        if existing["report_sha256"] != report_sha256:
            raise DirectRolloutVerificationError(
                "existing direct rollout attestation is bound to another report"
            )
        return existing, False


def verify_attestation(
    decision_dir: Path,
    history: ReleaseGateHistory,
    policy: Mapping[str, Any],
    policy_sha256: str,
    key: bytes,
    expected_key_id: str,
    *,
    max_age_seconds: int = 900,
    require_decision: Literal["expand", "hold", "rollback", "disable", "any"] = "any",
    now: datetime | None = None,
) -> dict[str, Any]:
    verification_key = _validate_key(key, "direct rollout attestation key")
    key_id = _validate_key_id(expected_key_id, "direct rollout attestation key_id")
    maximum_age = _bounded_int(
        max_age_seconds, "attestation maximum age", 1, MAX_ATTESTATION_SECONDS
    )
    if require_decision not in {*_DECISIONS, "any"}:
        raise DirectRolloutConfigError("required rollout decision is invalid")
    current_time = _normalize_now(now)
    report = _read_and_recompute_report(
        decision_dir, history, policy, policy_sha256, expect_attestation=True
    )
    try:
        attestation = _read_contract(
            decision_dir / ATTESTATION_FILE, _validate_attestation
        )
    except DeploymentResolutionError as exc:
        raise DirectRolloutVerificationError("cannot read rollout attestation") from exc
    if attestation["key_id"] != key_id:
        raise DirectRolloutVerificationError("direct rollout attestation key_id is not trusted")
    payload = {field: value for field, value in attestation.items() if field != "signature"}
    if not hmac.compare_digest(str(attestation["signature"]), _sign(verification_key, payload)):
        raise DirectRolloutVerificationError("direct rollout attestation signature is invalid")
    bindings = {
        "decision_id": report["decision_id"],
        "decision": report["decision"],
        "current_traffic_percent": report["current_traffic_percent"],
        "target_traffic_percent": report["target_traffic_percent"],
        "environment_sha256": report["environment_sha256"],
        "report_sha256": _digest(_canonical_json(report)),
        "history_chain_sha256": report["source"]["history_chain_sha256"],
    }
    if any(attestation.get(field) != value for field, value in bindings.items()):
        raise DirectRolloutVerificationError(
            "direct rollout attestation does not match current evidence"
        )
    if require_decision != "any" and report["decision"] != require_decision:
        raise DirectRolloutVerificationError(
            f"direct rollout decision {report['decision']} does not satisfy {require_decision}"
        )
    created_at = _parse_timestamp(str(attestation["created_at"]), "attestation created_at")
    expires_at = _parse_timestamp(str(attestation["expires_at"]), "attestation expires_at")
    generated_at = _parse_timestamp(str(report["generated_at"]), "rollout generated_at")
    if created_at > current_time + timedelta(seconds=MAX_CLOCK_SKEW_SECONDS):
        raise DirectRolloutVerificationError("direct rollout attestation is from the future")
    if generated_at > created_at + timedelta(seconds=MAX_CLOCK_SKEW_SECONDS):
        raise DirectRolloutVerificationError("direct rollout report postdates attestation")
    if expires_at <= created_at or expires_at - created_at > timedelta(
        seconds=MAX_ATTESTATION_SECONDS
    ):
        raise DirectRolloutVerificationError("direct rollout attestation lifetime is invalid")
    if current_time >= expires_at:
        raise DirectRolloutVerificationError("direct rollout attestation has expired")
    if current_time - created_at > timedelta(seconds=maximum_age):
        raise DirectRolloutVerificationError("direct rollout attestation is too old")
    if current_time - generated_at > timedelta(seconds=maximum_age):
        raise DirectRolloutVerificationError("direct rollout report is too old")
    return attestation


def _read_and_recompute_report(
    decision_dir: Path,
    history: ReleaseGateHistory,
    policy: Mapping[str, Any],
    policy_sha256: str,
    *,
    expect_attestation: bool | None,
) -> dict[str, Any]:
    expected = {REPORT_FILE}
    if expect_attestation is True:
        expected.add(ATTESTATION_FILE)
    try:
        entries = {entry.name for entry in os.scandir(decision_dir)}
    except OSError as exc:
        raise DirectRolloutVerificationError("cannot list rollout decision directory") from exc
    if expect_attestation is None and entries not in ({REPORT_FILE}, {REPORT_FILE, ATTESTATION_FILE}):
        raise DirectRolloutVerificationError("rollout decision artifact set is invalid")
    if expect_attestation is not None and entries != expected:
        raise DirectRolloutVerificationError("rollout decision artifact set is invalid")
    try:
        report = _read_contract(decision_dir / REPORT_FILE, _validate_report)
    except DeploymentResolutionError as exc:
        raise DirectRolloutVerificationError("cannot read rollout report") from exc
    generated_at = _parse_timestamp(str(report["generated_at"]), "rollout generated_at")
    recomputed = evaluate_rollout(
        history,
        policy,
        policy_sha256,
        int(report["current_traffic_percent"]),
        now=generated_at,
    )
    if report != recomputed:
        raise DirectRolloutVerificationError(
            "direct rollout report does not match current history and policy"
        )
    return report


def _direct_evidence(event: Mapping[str, Any]) -> dict[str, Any]:
    report = event.get("report")
    canary = report.get("canary") if isinstance(report, Mapping) else None
    structured = canary.get("report") if isinstance(canary, Mapping) else None
    summary = structured.get("summary") if isinstance(structured, Mapping) else None
    safe_summary = _direct_summary(summary)
    evidence = {
        "sequence": event["sequence"],
        "run_id": event["run_id"],
        "observed_at": event["observed_at"],
        "gate_decision": event["decision"],
        "canary_status": event["canary_status"],
        "gate_report_sha256": event["gate_report_sha256"],
        "summary": safe_summary,
        "failure_codes": [],
    }
    evidence["failure_codes"] = _sample_failure_codes(evidence, None)
    return evidence


def _direct_summary(value: Any) -> dict[str, Any] | None:
    if not isinstance(value, Mapping):
        return None
    required = _MINIMUM_FIELDS | _MAXIMUM_FIELDS | {"cases", "model_calls"}
    if not required.issubset(value):
        return None
    result: dict[str, Any] = {}
    for field in sorted(required):
        item = value.get(field)
        if isinstance(item, bool) or not isinstance(item, (int, float)):
            return None
        number = float(item)
        if not math.isfinite(number) or number < 0:
            return None
        result[field] = item
    return result


def _window_failure_codes(
    evidence: Sequence[Mapping[str, Any]], policy: Mapping[str, Any]
) -> list[str]:
    failures: list[str] = []
    for item in evidence:
        failures.extend(_sample_failure_codes(item, policy))
    return failures


def _sample_failure_codes(
    evidence: Mapping[str, Any], policy: Mapping[str, Any] | None
) -> list[str]:
    failures: list[str] = []
    if evidence.get("gate_decision") != "promote":
        failures.append("release_gate_rollback")
    if evidence.get("canary_status") != "passed":
        failures.append("direct_canary_failed")
    summary = evidence.get("summary")
    if not isinstance(summary, Mapping):
        failures.append("direct_report_missing")
        return failures
    if policy is None:
        return failures
    minimum = cast(Mapping[str, Any], policy["minimum"])
    maximum = cast(Mapping[str, Any], policy["maximum"])
    code_by_minimum = {
        "direct_rate": "direct_path_regression",
        "single_model_call_rate": "single_model_call_regression",
        "quality_pass_rate": "quality_regression",
        "content_pass_rate": "content_regression",
        "repair_success_rate": "repair_regression",
    }
    code_by_maximum = {
        "duration_p95_ms": "latency_regression",
        "assistant_delivery_violations": "assistant_delivery_regression",
        "duplicate_violations": "duplicate_content_detected",
        "response_quality_failures": "response_quality_failure",
        "planned_runs": "unexpected_planning",
        "tool_actions": "unexpected_tool_action",
        "rewrite_runs": "unexpected_model_rewrite",
    }
    for field, threshold in minimum.items():
        if float(summary[field]) < float(threshold):
            failures.append(code_by_minimum[field])
    for field, threshold in maximum.items():
        if float(summary[field]) > float(threshold):
            failures.append(code_by_maximum[field])
    return failures


def _validate_policy(value: Any) -> dict[str, Any]:
    fields = {
        "schema_version",
        "version",
        "traffic_stages",
        "fast_window_runs",
        "stable_window_runs",
        "minimum_stable_window_seconds",
        "maximum_gap_seconds",
        "maximum_report_age_seconds",
        "minimum",
        "maximum",
    }
    if not isinstance(value, Mapping) or set(value) != fields:
        raise DirectRolloutConfigError("direct rollout policy fields are invalid")
    result = dict(value)
    if (
        result.get("schema_version") != POLICY_SCHEMA_VERSION
        or result.get("version") != POLICY_SCHEMA_VERSION
    ):
        raise DirectRolloutConfigError("direct rollout policy version is invalid")
    stages = result.get("traffic_stages")
    if (
        not isinstance(stages, list)
        or not 2 <= len(stages) <= 20
        or any(isinstance(item, bool) or not isinstance(item, int) for item in stages)
        or stages != sorted(set(stages))
        or stages[0] != 0
        or stages[-1] != 100
    ):
        raise DirectRolloutConfigError("direct rollout traffic stages are invalid")
    fast = _bounded_int(result["fast_window_runs"], "fast window", 1, 20)
    stable = _bounded_int(result["stable_window_runs"], "stable window", fast, 100)
    result["fast_window_runs"] = fast
    result["stable_window_runs"] = stable
    result["minimum_stable_window_seconds"] = _bounded_int(
        result["minimum_stable_window_seconds"], "stable window duration", 0, 7 * 86400
    )
    result["maximum_gap_seconds"] = _bounded_int(
        result["maximum_gap_seconds"], "maximum sample gap", 1, 7 * 86400
    )
    result["maximum_report_age_seconds"] = _bounded_int(
        result["maximum_report_age_seconds"], "maximum report age", 1, 7 * 86400
    )
    result["minimum"] = _thresholds(result["minimum"], _MINIMUM_FIELDS, rate=True)
    result["maximum"] = _thresholds(result["maximum"], _MAXIMUM_FIELDS, rate=False)
    return result


def _validate_report(value: Any) -> dict[str, Any]:
    fields = {
        "schema_version",
        "decision_id",
        "generated_at",
        "decision",
        "current_traffic_percent",
        "target_traffic_percent",
        "environment_sha256",
        "source",
        "windows",
        "evidence",
        "reasons",
    }
    if not isinstance(value, Mapping) or set(value) != fields:
        raise DirectRolloutVerificationError("direct rollout report fields are invalid")
    result = dict(value)
    if result["schema_version"] != REPORT_SCHEMA_VERSION:
        raise DirectRolloutVerificationError("direct rollout report version is invalid")
    if not isinstance(result["decision_id"], str) or _DECISION_ID_RE.fullmatch(
        result["decision_id"]
    ) is None:
        raise DirectRolloutVerificationError("direct rollout decision ID is invalid")
    _parse_timestamp(str(result["generated_at"]), "direct rollout generated_at")
    if result["decision"] not in _DECISIONS:
        raise DirectRolloutVerificationError("direct rollout decision is invalid")
    for field in ("current_traffic_percent", "target_traffic_percent"):
        _bounded_int(result[field], field, 0, 100)
    if not _digest_value(result["environment_sha256"]):
        raise DirectRolloutVerificationError("direct rollout environment digest is invalid")
    source = result["source"]
    if not isinstance(source, Mapping) or set(source) != {
        "policy_version",
        "policy_sha256",
        "history_chain_sha256",
        "history_events",
        "direct_events",
    }:
        raise DirectRolloutVerificationError("direct rollout source is invalid")
    if source["policy_version"] != POLICY_SCHEMA_VERSION or not _digest_value(
        source["policy_sha256"]
    ) or not _digest_value(source["history_chain_sha256"]):
        raise DirectRolloutVerificationError("direct rollout source binding is invalid")
    _bounded_int(source["history_events"], "history events", 0, 100000)
    _bounded_int(source["direct_events"], "direct events", 0, 100000)
    if not isinstance(result["windows"], Mapping) or set(result["windows"]) != {
        "fast",
        "stable",
    }:
        raise DirectRolloutVerificationError("direct rollout windows are invalid")
    if not isinstance(result["evidence"], list) or len(result["evidence"]) > 100:
        raise DirectRolloutVerificationError("direct rollout evidence is invalid")
    if not isinstance(result["reasons"], list) or not result["reasons"]:
        raise DirectRolloutVerificationError("direct rollout reasons are invalid")
    if result["decision_id"] != _decision_id(result):
        raise DirectRolloutVerificationError("direct rollout decision ID binding is invalid")
    return result


def _validate_attestation(value: Any) -> dict[str, Any]:
    fields = {
        "schema_version",
        "attestation_id",
        "created_at",
        "expires_at",
        "decision_id",
        "decision",
        "current_traffic_percent",
        "target_traffic_percent",
        "environment_sha256",
        "report_sha256",
        "history_chain_sha256",
        "key_id",
        "algorithm",
        "signature",
    }
    if not isinstance(value, Mapping) or set(value) != fields:
        raise DirectRolloutVerificationError("direct rollout attestation fields are invalid")
    result = dict(value)
    if result["schema_version"] != ATTESTATION_SCHEMA_VERSION:
        raise DirectRolloutVerificationError("direct rollout attestation version is invalid")
    if not isinstance(result["attestation_id"], str) or _ATTESTATION_ID_RE.fullmatch(
        result["attestation_id"]
    ) is None:
        raise DirectRolloutVerificationError("direct rollout attestation ID is invalid")
    if result["decision"] not in _DECISIONS or result["algorithm"] != "hmac-sha256":
        raise DirectRolloutVerificationError("direct rollout attestation decision is invalid")
    if not isinstance(result["decision_id"], str) or _DECISION_ID_RE.fullmatch(
        result["decision_id"]
    ) is None:
        raise DirectRolloutVerificationError("direct rollout attestation decision ID is invalid")
    for field in ("created_at", "expires_at"):
        _parse_timestamp(str(result[field]), f"direct rollout {field}")
    for field in (
        "environment_sha256",
        "report_sha256",
        "history_chain_sha256",
        "signature",
    ):
        if not _digest_value(result[field]):
            raise DirectRolloutVerificationError("direct rollout attestation digest is invalid")
    _validate_key_id(result["key_id"], "direct rollout attestation key_id")
    for field in ("current_traffic_percent", "target_traffic_percent"):
        _bounded_int(result[field], field, 0, 100)
    identity = {
        field: item for field, item in result.items() if field not in {"attestation_id", "signature"}
    }
    expected_id = f"direct-rollout-attestation-{_digest(_canonical_json(identity))[:32]}"
    if result["attestation_id"] != expected_id:
        raise DirectRolloutVerificationError("direct rollout attestation ID binding is invalid")
    return result


def _decision_id(report: Mapping[str, Any]) -> str:
    payload = {field: value for field, value in report.items() if field != "decision_id"}
    return f"direct-rollout-{_digest(_canonical_json(payload))[:32]}"


def _reason(code: str, expected: str, actual: Any) -> dict[str, Any]:
    return {"code": code, "expected": expected, "actual": actual}


def _thresholds(value: Any, fields: set[str], *, rate: bool) -> dict[str, float]:
    if not isinstance(value, Mapping) or set(value) != fields:
        raise DirectRolloutConfigError("direct rollout thresholds are invalid")
    result: dict[str, float] = {}
    for field in sorted(fields):
        item = value[field]
        if (
            isinstance(item, bool)
            or not isinstance(item, (int, float))
            or not math.isfinite(float(item))
            or float(item) < 0
            or (rate and float(item) > 1)
        ):
            raise DirectRolloutConfigError("direct rollout threshold is invalid")
        result[field] = float(item)
    return result


def _bounded_int(value: Any, label: str, minimum: int, maximum: int) -> int:
    if (
        isinstance(value, bool)
        or not isinstance(value, int)
        or not minimum <= value <= maximum
    ):
        raise DirectRolloutConfigError(f"direct rollout {label} is invalid")
    return value


def _digest_value(value: Any) -> bool:
    return isinstance(value, str) and re.fullmatch(r"[0-9a-f]{64}", value) is not None


def _sign(key: bytes, payload: Mapping[str, Any]) -> str:
    derived = hmac.new(key, _SIGNING_DOMAIN, hashlib.sha256).digest()
    return hmac.new(derived, _canonical_json(payload), hashlib.sha256).hexdigest()


def _reject_duplicate_keys(pairs: list[tuple[str, Any]]) -> dict[str, Any]:
    result: dict[str, Any] = {}
    for key, value in pairs:
        if key in result:
            raise ValueError(f"duplicate JSON key: {key}")
        result[key] = value
    return result


def _key_from_environment(*, profile: str = "standard") -> tuple[bytes, str]:
    if profile not in {
        "standard", "production", "production_emergency", "production_recovery",
        "production_expansion",
        "production_expansion_25",
        "production_expansion_50",
        "production_expansion_100",
    }:
        raise DirectRolloutConfigError("direct rollout key profile is invalid")
    profiles = {
        "standard": (KEY_ENV, KEY_ID_ENV),
        "production": (PRODUCTION_KEY_ENV, PRODUCTION_KEY_ID_ENV),
        "production_emergency": (
            PRODUCTION_EMERGENCY_KEY_ENV,
            PRODUCTION_EMERGENCY_KEY_ID_ENV,
        ),
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
    }
    key_name, key_id_name = profiles[profile]
    key = os.environ.get(key_name)
    key_id = os.environ.get(key_id_name)
    if key is None or key_id is None:
        raise DirectRolloutConfigError(
            f"{key_name} and {key_id_name} are required"
        )
    return key.encode("utf-8"), key_id


def _new_report_exit(decision: str) -> int:
    return {"expand": 0, "hold": 3, "rollback": 4, "disable": 5}[decision]


def main(argv: Sequence[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        description="Evaluate and attest multi-window Agent direct rollout decisions"
    )
    subparsers = parser.add_subparsers(dest="command", required=True)
    evaluate = subparsers.add_parser("evaluate")
    evaluate.add_argument("--history-ledger", type=Path, required=True)
    evaluate.add_argument("--environment-id", required=True)
    evaluate.add_argument("--policy", type=Path, required=True)
    evaluate.add_argument("--current-traffic-percent", type=int, required=True)
    evaluate.add_argument("--output-root", type=Path, required=True)
    attest = subparsers.add_parser("attest")
    verify = subparsers.add_parser("verify")
    for command in (attest, verify):
        command.add_argument("--decision-dir", type=Path, required=True)
        command.add_argument("--history-ledger", type=Path, required=True)
        command.add_argument("--environment-id", required=True)
        command.add_argument("--policy", type=Path, required=True)
    attest.add_argument("--ttl-seconds", type=int, default=900)
    attest.add_argument(
        "--key-profile",
        choices=(
            "standard", "production", "production_emergency", "production_recovery",
            "production_expansion",
            "production_expansion_25",
            "production_expansion_50",
            "production_expansion_100",
        ),
        default="standard",
    )
    verify.add_argument("--max-age-seconds", type=int, default=900)
    verify.add_argument(
        "--key-profile",
        choices=(
            "standard", "production", "production_emergency", "production_recovery",
            "production_expansion",
            "production_expansion_25",
            "production_expansion_50",
            "production_expansion_100",
        ),
        default="standard",
    )
    verify.add_argument(
        "--require-decision",
        choices=("expand", "hold", "rollback", "disable", "any"),
        default="any",
    )
    args = parser.parse_args(argv)
    try:
        policy, policy_sha256 = load_policy(args.policy)
        history = ReleaseGateHistory(
            args.history_ledger, args.environment_id, create=False
        )
        if args.command == "evaluate":
            report = evaluate_rollout(
                history,
                policy,
                policy_sha256,
                args.current_traffic_percent,
            )
            output_dir = write_rollout_report(args.output_root, report)
            print(
                json.dumps(
                    {
                        "decision": report["decision"],
                        "decision_id": report["decision_id"],
                        "current_traffic_percent": report["current_traffic_percent"],
                        "target_traffic_percent": report["target_traffic_percent"],
                        "output_dir": str(output_dir),
                    },
                    sort_keys=True,
                )
            )
            return _new_report_exit(str(report["decision"]))
        key, key_id = _key_from_environment(profile=args.key_profile)
        if args.command == "attest":
            result, created = create_attestation(
                args.decision_dir,
                history,
                policy,
                policy_sha256,
                key,
                key_id,
                ttl_seconds=args.ttl_seconds,
            )
            status = "created" if created else "reused"
        else:
            result = verify_attestation(
                args.decision_dir,
                history,
                policy,
                policy_sha256,
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
                    "target_traffic_percent": result["target_traffic_percent"],
                    "expires_at": result["expires_at"],
                    "status": status,
                },
                sort_keys=True,
            )
        )
        return 0
    except (DirectRolloutConfigError, ReleaseGateHistoryError) as exc:
        print(f"direct rollout configuration error: {exc}", file=sys.stderr)
        return 2
    except (DirectRolloutError, DeploymentResolutionError, OSError) as exc:
        print(f"direct rollout verification error: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
