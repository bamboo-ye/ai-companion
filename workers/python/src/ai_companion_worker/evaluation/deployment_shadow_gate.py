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
    _validate_digest,
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
from .deployment_shadow import _DRIFT_OUTCOMES
from .deployment_shadow_history import (
    DeploymentShadowHistoryError,
    ShadowHistoryLedger,
)
from .release_attestation import KEY_ENV, KEY_ID_ENV, MAX_CLOCK_SKEW_SECONDS


SHADOW_GATE_SCHEMA_VERSION = "observability-deployment-shadow-gate-v1"
SHADOW_ATTESTATION_SCHEMA_VERSION = "observability-deployment-shadow-attestation-v1"
SHADOW_GATE_REPORT_FILE = "shadow-gate-report.json"
SHADOW_ATTESTATION_FILE = "shadow-attestation.json"
SHADOW_ATTESTATION_ALGORITHM = "hmac-sha256"
MAX_ATTESTATION_TTL_SECONDS = 24 * 60 * 60
MAX_ATTESTATION_AGE_SECONDS = 24 * 60 * 60
_SIGNING_DOMAIN = b"ai-companion/deployment-shadow-attestation/v1"
_GATE_ID_RE = re.compile(r"shadow-gate-[0-9a-f]{32}")
_ATTESTATION_ID_RE = re.compile(r"shadow-attestation-[0-9a-f]{32}")
_VIOLATION_CODE_RE = re.compile(r"[a-z][a-z0-9_]{0,63}")


class DeploymentShadowGateError(ValueError):
    pass


class DeploymentShadowGateConfigError(DeploymentShadowGateError):
    pass


class DeploymentShadowGateVerificationError(DeploymentShadowGateError):
    pass


@dataclass(frozen=True)
class ShadowGatePolicy:
    minimum_runs: int = 30
    minimum_window_seconds: int = 15 * 60
    minimum_selected_total: int = 10
    maximum_gap_seconds: int = 90
    maximum_report_age_seconds: int = 90
    maximum_drift_total: int = 0
    maximum_lookup_errors_total: int = 0


def evaluate_shadow_history(
    history: ShadowHistoryLedger,
    policy: ShadowGatePolicy = ShadowGatePolicy(),
    *,
    now: datetime | None = None,
) -> dict[str, Any]:
    validated_policy = _validate_policy(policy)
    current_time = _normalize_now(now)
    all_events = history.events(until=current_time)
    window_start = current_time - timedelta(
        seconds=validated_policy.minimum_window_seconds
    )
    events = [
        event
        for event in all_events
        if _parse_timestamp(str(event["observed_at"]), "shadow gate observed_at")
        >= window_start
    ]
    successful = [event for event in events if event["outcome"] == "success"]
    failed = [event for event in events if event["outcome"] == "error"]
    selected_total = 0
    drift_total = 0
    lookup_errors_total = 0
    retried_lookups_total = 0
    for event in successful:
        report = event["report"]
        if not isinstance(report, Mapping):
            raise DeploymentShadowGateVerificationError(
                "successful shadow history event has no validated report"
            )
        batch = report["batch"]
        selected_total += int(batch["selected"])
        drift_total += sum(int(batch[outcome]) for outcome in _DRIFT_OUTCOMES)
        lookup_errors_total += int(batch["lookup_error"])
        retried_lookups_total += int(batch["retried_lookups"])

    times = [
        _parse_timestamp(str(event["observed_at"]), "shadow gate observed_at")
        for event in events
    ]
    first_observed_at = times[0] if times else None
    last_observed_at = times[-1] if times else None
    observed_duration = (
        max(0.0, (last_observed_at - first_observed_at).total_seconds())
        if first_observed_at is not None and last_observed_at is not None
        else 0.0
    )
    gaps = [
        max(0.0, (right - left).total_seconds())
        for left, right in zip(times, times[1:])
    ]
    maximum_gap = max(gaps, default=0.0)
    last_age = (
        max(0.0, (current_time - last_observed_at).total_seconds())
        if last_observed_at is not None
        else None
    )
    required_coverage = max(
        0,
        validated_policy.minimum_window_seconds
        - validated_policy.maximum_gap_seconds,
    )
    violations: list[dict[str, Any]] = []
    _append_violation(
        violations,
        "insufficient_runs",
        len(events) >= validated_policy.minimum_runs,
        f">={validated_policy.minimum_runs}",
        len(events),
    )
    _append_violation(
        violations,
        "insufficient_window",
        observed_duration >= required_coverage,
        f">={required_coverage}",
        observed_duration,
    )
    _append_violation(
        violations,
        "insufficient_selected",
        selected_total >= validated_policy.minimum_selected_total,
        f">={validated_policy.minimum_selected_total}",
        selected_total,
    )
    _append_violation(violations, "failed_runs", len(failed) == 0, "0", len(failed))
    _append_violation(
        violations,
        "drift_detected",
        drift_total <= validated_policy.maximum_drift_total,
        f"<={validated_policy.maximum_drift_total}",
        drift_total,
    )
    _append_violation(
        violations,
        "lookup_errors",
        lookup_errors_total <= validated_policy.maximum_lookup_errors_total,
        f"<={validated_policy.maximum_lookup_errors_total}",
        lookup_errors_total,
    )
    _append_violation(
        violations,
        "observation_gap",
        maximum_gap <= validated_policy.maximum_gap_seconds,
        f"<={validated_policy.maximum_gap_seconds}",
        maximum_gap,
    )
    _append_violation(
        violations,
        "observation_stale",
        last_age is not None
        and last_age <= validated_policy.maximum_report_age_seconds,
        f"<={validated_policy.maximum_report_age_seconds}",
        last_age,
    )
    last_chain = str(events[-1]["chain_sha256"]) if events else "0" * 64
    window = {
        "started_at": (
            _format_timestamp(first_observed_at)
            if first_observed_at is not None
            else None
        ),
        "ended_at": (
            _format_timestamp(last_observed_at)
            if last_observed_at is not None
            else None
        ),
        "duration_seconds": observed_duration,
        "runs": len(events),
        "successful_runs": len(successful),
        "failed_runs": len(failed),
        "selected_total": selected_total,
        "drift_total": drift_total,
        "lookup_errors_total": lookup_errors_total,
        "retried_lookups_total": retried_lookups_total,
        "maximum_gap_seconds": maximum_gap,
        "last_report_age_seconds": last_age,
        "first_event_id": events[0]["event_id"] if events else None,
        "last_event_id": events[-1]["event_id"] if events else None,
        "history_chain_sha256": last_chain,
    }
    gate_seed = {
        "provider_instance_sha256": history.provider_instance_sha256,
        "policy": asdict(validated_policy),
        "window": window,
        "violations": violations,
    }
    report = {
        "schema_version": SHADOW_GATE_SCHEMA_VERSION,
        "gate_id": f"shadow-gate-{_digest(_canonical_json(gate_seed))[:32]}",
        "generated_at": _format_timestamp(current_time),
        "decision": "pass" if not violations else "hold",
        "provider_instance_sha256": history.provider_instance_sha256,
        "policy": asdict(validated_policy),
        "window": window,
        "violations": violations,
    }
    return _validate_gate_report(report)


def write_shadow_gate_report(
    output_dir: Path,
    report: Mapping[str, Any],
) -> Path:
    validated = _validate_gate_report(report)
    try:
        _ensure_private_state_directory(output_dir.parent)
        _ensure_private_state_directory(output_dir)
    except DeploymentResolutionError as exc:
        raise DeploymentShadowGateVerificationError(
            "cannot create private shadow gate directory"
        ) from exc
    try:
        entries = list(os.scandir(output_dir))
    except OSError as exc:
        raise DeploymentShadowGateVerificationError(
            f"cannot list shadow gate directory: {exc}"
        ) from exc
    if entries:
        raise DeploymentShadowGateVerificationError(
            "shadow gate directory must be empty"
        )
    path = output_dir / SHADOW_GATE_REPORT_FILE
    _write_exclusive_private_json(path, validated)
    return path


def create_shadow_attestation(
    gate_dir: Path,
    key: bytes,
    key_id: str,
    *,
    ttl_seconds: int = 900,
    now: datetime | None = None,
) -> tuple[dict[str, Any], bool]:
    signing_key = _validate_key(key, "shadow attestation key")
    signing_key_id = _validate_key_id(key_id, "shadow attestation key_id")
    ttl = _validate_int(
        ttl_seconds,
        "shadow attestation TTL",
        1,
        MAX_ATTESTATION_TTL_SECONDS,
    )
    current_time = _normalize_now(now)
    report = _read_gate_report(gate_dir, expect_attestation=None)
    if report["decision"] != "pass":
        raise DeploymentShadowGateVerificationError(
            "only a passing shadow gate can be attested"
        )
    generated_at = _parse_timestamp(str(report["generated_at"]), "shadow gate generated_at")
    if generated_at > current_time + timedelta(seconds=MAX_CLOCK_SKEW_SECONDS):
        raise DeploymentShadowGateVerificationError(
            "shadow gate report timestamp is in the future"
        )
    report_sha256 = _digest(_canonical_json(report))
    expires_at = current_time + timedelta(seconds=ttl)
    payload: dict[str, Any] = {
        "schema_version": SHADOW_ATTESTATION_SCHEMA_VERSION,
        "attestation_id": "",
        "created_at": _format_timestamp(current_time),
        "expires_at": _format_timestamp(expires_at),
        "gate_id": report["gate_id"],
        "decision": report["decision"],
        "provider_instance_sha256": report["provider_instance_sha256"],
        "gate_report_sha256": report_sha256,
        "history_chain_sha256": report["window"]["history_chain_sha256"],
        "key_id": signing_key_id,
        "algorithm": SHADOW_ATTESTATION_ALGORITHM,
    }
    identity_payload = {field: value for field, value in payload.items() if field != "attestation_id"}
    payload["attestation_id"] = (
        f"shadow-attestation-{_digest(_canonical_json(identity_payload))[:32]}"
    )
    attestation = dict(payload)
    attestation["signature"] = _sign(signing_key, payload)
    attestation = _validate_attestation(attestation)
    path = gate_dir / SHADOW_ATTESTATION_FILE
    try:
        _write_exclusive_private_json(path, attestation)
        return attestation, True
    except FileExistsError:
        existing = verify_shadow_attestation(
            gate_dir,
            signing_key,
            signing_key_id,
            require_decision="pass",
            now=current_time,
        )
        if existing["gate_report_sha256"] != report_sha256:
            raise DeploymentShadowGateVerificationError(
                "existing shadow attestation is bound to another report"
            )
        return existing, False


def verify_shadow_attestation(
    gate_dir: Path,
    key: bytes,
    expected_key_id: str,
    *,
    max_age_seconds: int = 900,
    require_decision: Literal["pass", "hold", "any"] = "pass",
    now: datetime | None = None,
) -> dict[str, Any]:
    verification_key = _validate_key(key, "shadow attestation key")
    key_id = _validate_key_id(expected_key_id, "shadow attestation key_id")
    maximum_age = _validate_int(
        max_age_seconds,
        "shadow attestation maximum age",
        1,
        MAX_ATTESTATION_AGE_SECONDS,
    )
    if require_decision not in {"pass", "hold", "any"}:
        raise DeploymentShadowGateConfigError(
            "shadow attestation required decision is invalid"
        )
    current_time = _normalize_now(now)
    report = _read_gate_report(gate_dir, expect_attestation=True)
    attestation = _read_contract(
        gate_dir / SHADOW_ATTESTATION_FILE,
        _validate_attestation,
    )
    if attestation["key_id"] != key_id:
        raise DeploymentShadowGateVerificationError(
            "shadow attestation key_id is not trusted"
        )
    payload = {field: value for field, value in attestation.items() if field != "signature"}
    expected_signature = _sign(verification_key, payload)
    if not hmac.compare_digest(str(attestation["signature"]), expected_signature):
        raise DeploymentShadowGateVerificationError(
            "shadow attestation signature is invalid"
        )
    report_sha256 = _digest(_canonical_json(report))
    expected_bindings = {
        "gate_id": report["gate_id"],
        "decision": report["decision"],
        "provider_instance_sha256": report["provider_instance_sha256"],
        "gate_report_sha256": report_sha256,
        "history_chain_sha256": report["window"]["history_chain_sha256"],
    }
    if any(attestation.get(field) != value for field, value in expected_bindings.items()):
        raise DeploymentShadowGateVerificationError(
            "shadow attestation does not match gate evidence"
        )
    if require_decision != "any" and report["decision"] != require_decision:
        raise DeploymentShadowGateVerificationError(
            f"shadow gate decision {report['decision']} does not satisfy {require_decision}"
        )
    created_at = _parse_timestamp(
        str(attestation["created_at"]),
        "shadow attestation created_at",
    )
    expires_at = _parse_timestamp(
        str(attestation["expires_at"]),
        "shadow attestation expires_at",
    )
    generated_at = _parse_timestamp(str(report["generated_at"]), "shadow gate generated_at")
    if created_at > current_time + timedelta(seconds=MAX_CLOCK_SKEW_SECONDS):
        raise DeploymentShadowGateVerificationError(
            "shadow attestation timestamp is in the future"
        )
    if generated_at > created_at + timedelta(seconds=MAX_CLOCK_SKEW_SECONDS):
        raise DeploymentShadowGateVerificationError(
            "shadow gate was generated after attestation"
        )
    if expires_at <= created_at or expires_at - created_at > timedelta(
        seconds=MAX_ATTESTATION_TTL_SECONDS
    ):
        raise DeploymentShadowGateVerificationError(
            "shadow attestation lifetime is invalid"
        )
    if current_time >= expires_at:
        raise DeploymentShadowGateVerificationError("shadow attestation has expired")
    if current_time - created_at > timedelta(seconds=maximum_age):
        raise DeploymentShadowGateVerificationError("shadow attestation is too old")
    if current_time - generated_at > timedelta(seconds=maximum_age):
        raise DeploymentShadowGateVerificationError("shadow gate report is too old")
    return attestation


def main(argv: Sequence[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        description="Evaluate and attest a stable read-only Provider shadow window"
    )
    subparsers = parser.add_subparsers(dest="command", required=True)
    evaluate_parser = subparsers.add_parser("evaluate")
    evaluate_parser.add_argument("--history-ledger", type=Path, required=True)
    evaluate_parser.add_argument("--provider-instance-sha256", required=True)
    evaluate_parser.add_argument("--output-dir", type=Path, required=True)
    evaluate_parser.add_argument("--minimum-runs", type=int, default=30)
    evaluate_parser.add_argument("--minimum-window-seconds", type=int, default=900)
    evaluate_parser.add_argument("--minimum-selected-total", type=int, default=10)
    evaluate_parser.add_argument("--maximum-gap-seconds", type=int, default=90)
    evaluate_parser.add_argument("--maximum-report-age-seconds", type=int, default=90)
    evaluate_parser.add_argument("--maximum-drift-total", type=int, default=0)
    evaluate_parser.add_argument("--maximum-lookup-errors-total", type=int, default=0)
    attest_parser = subparsers.add_parser("attest")
    attest_parser.add_argument("--gate-dir", type=Path, required=True)
    attest_parser.add_argument("--ttl-seconds", type=int, default=900)
    verify_parser = subparsers.add_parser("verify")
    verify_parser.add_argument("--gate-dir", type=Path, required=True)
    verify_parser.add_argument("--max-age-seconds", type=int, default=900)
    verify_parser.add_argument(
        "--require-decision",
        choices=("pass", "hold", "any"),
        default="pass",
    )
    args = parser.parse_args(argv)
    try:
        if args.command == "evaluate":
            history = ShadowHistoryLedger(
                args.history_ledger,
                args.provider_instance_sha256,
                create=False,
            )
            report = evaluate_shadow_history(
                history,
                ShadowGatePolicy(
                    minimum_runs=args.minimum_runs,
                    minimum_window_seconds=args.minimum_window_seconds,
                    minimum_selected_total=args.minimum_selected_total,
                    maximum_gap_seconds=args.maximum_gap_seconds,
                    maximum_report_age_seconds=args.maximum_report_age_seconds,
                    maximum_drift_total=args.maximum_drift_total,
                    maximum_lookup_errors_total=args.maximum_lookup_errors_total,
                ),
            )
            path = write_shadow_gate_report(args.output_dir, report)
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
        key, key_id = _key_from_environment()
        if args.command == "attest":
            result, created = create_shadow_attestation(
                args.gate_dir,
                key,
                key_id,
                ttl_seconds=args.ttl_seconds,
            )
            status = "created" if created else "reused"
        else:
            result = verify_shadow_attestation(
                args.gate_dir,
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
    except DeploymentShadowGateConfigError as exc:
        print(f"deployment shadow gate configuration error: {exc}", file=sys.stderr)
        return 2
    except (
        DeploymentShadowGateError,
        DeploymentShadowHistoryError,
        DeploymentResolutionError,
        OSError,
    ) as exc:
        print(f"deployment shadow gate verification error: {exc}", file=sys.stderr)
        return 1


def _validate_policy(value: ShadowGatePolicy) -> ShadowGatePolicy:
    _validate_int(value.minimum_runs, "shadow gate minimum runs", 1, 10000)
    _validate_int(
        value.minimum_window_seconds,
        "shadow gate minimum window",
        1,
        7 * 24 * 60 * 60,
    )
    _validate_int(
        value.minimum_selected_total,
        "shadow gate minimum selected total",
        1,
        100000000,
    )
    _validate_int(value.maximum_gap_seconds, "shadow gate maximum gap", 1, 24 * 60 * 60)
    if value.maximum_gap_seconds > value.minimum_window_seconds:
        raise DeploymentShadowGateConfigError(
            "shadow gate maximum gap cannot exceed the observation window"
        )
    _validate_int(
        value.maximum_report_age_seconds,
        "shadow gate maximum report age",
        1,
        24 * 60 * 60,
    )
    _validate_int(value.maximum_drift_total, "shadow gate maximum drift", 0, 1000000)
    _validate_int(
        value.maximum_lookup_errors_total,
        "shadow gate maximum lookup errors",
        0,
        1000000,
    )
    return value


def _validate_gate_report(value: Any) -> dict[str, Any]:
    fields = {
        "schema_version",
        "gate_id",
        "generated_at",
        "decision",
        "provider_instance_sha256",
        "policy",
        "window",
        "violations",
    }
    if not isinstance(value, Mapping) or set(value) != fields:
        raise DeploymentShadowGateVerificationError("shadow gate report is invalid")
    result = dict(value)
    if result["schema_version"] != SHADOW_GATE_SCHEMA_VERSION:
        raise DeploymentShadowGateVerificationError(
            "shadow gate schema_version is invalid"
        )
    if not isinstance(result["gate_id"], str) or _GATE_ID_RE.fullmatch(
        result["gate_id"]
    ) is None:
        raise DeploymentShadowGateVerificationError("shadow gate ID is invalid")
    _parse_timestamp(str(result["generated_at"]), "shadow gate generated_at")
    if result["decision"] not in {"pass", "hold"}:
        raise DeploymentShadowGateVerificationError("shadow gate decision is invalid")
    try:
        _validate_digest(
            str(result["provider_instance_sha256"]),
            "shadow gate provider_instance_sha256",
        )
    except ValueError as exc:
        raise DeploymentShadowGateVerificationError(
            "shadow gate provider digest is invalid"
        ) from exc
    policy_value = result["policy"]
    if not isinstance(policy_value, Mapping):
        raise DeploymentShadowGateVerificationError("shadow gate policy is invalid")
    try:
        policy = _validate_policy(ShadowGatePolicy(**dict(policy_value)))
    except (TypeError, DeploymentShadowGateError) as exc:
        raise DeploymentShadowGateVerificationError("shadow gate policy is invalid") from exc
    if set(policy_value) != set(asdict(policy)):
        raise DeploymentShadowGateVerificationError("shadow gate policy fields are invalid")
    result["policy"] = asdict(policy)
    result["window"] = _validate_window(result["window"])
    violations = result["violations"]
    if not isinstance(violations, list) or len(violations) > 16:
        raise DeploymentShadowGateVerificationError("shadow gate violations are invalid")
    validated_violations = [_validate_violation(item) for item in violations]
    codes = [item["code"] for item in validated_violations]
    if len(codes) != len(set(codes)):
        raise DeploymentShadowGateVerificationError(
            "shadow gate violations contain duplicates"
        )
    expected_violations = _expected_violations(policy, result["window"])
    if validated_violations != expected_violations:
        raise DeploymentShadowGateVerificationError(
            "shadow gate violations do not match policy and window evidence"
        )
    if (result["decision"] == "pass") != (len(validated_violations) == 0):
        raise DeploymentShadowGateVerificationError(
            "shadow gate decision does not match violations"
        )
    result["violations"] = validated_violations
    gate_seed = {
        "provider_instance_sha256": result["provider_instance_sha256"],
        "policy": result["policy"],
        "window": result["window"],
        "violations": result["violations"],
    }
    expected_id = f"shadow-gate-{_digest(_canonical_json(gate_seed))[:32]}"
    if result["gate_id"] != expected_id:
        raise DeploymentShadowGateVerificationError(
            "shadow gate ID binding is invalid"
        )
    return result


def _validate_window(value: Any) -> dict[str, Any]:
    fields = {
        "started_at",
        "ended_at",
        "duration_seconds",
        "runs",
        "successful_runs",
        "failed_runs",
        "selected_total",
        "drift_total",
        "lookup_errors_total",
        "retried_lookups_total",
        "maximum_gap_seconds",
        "last_report_age_seconds",
        "first_event_id",
        "last_event_id",
        "history_chain_sha256",
    }
    if not isinstance(value, Mapping) or set(value) != fields:
        raise DeploymentShadowGateVerificationError("shadow gate window is invalid")
    result = dict(value)
    for field in (
        "runs",
        "successful_runs",
        "failed_runs",
        "selected_total",
        "drift_total",
        "lookup_errors_total",
        "retried_lookups_total",
    ):
        _validate_int(result[field], f"shadow gate window {field}", 0, 100000000)
    if result["successful_runs"] + result["failed_runs"] != result["runs"]:
        raise DeploymentShadowGateVerificationError(
            "shadow gate window run counters are inconsistent"
        )
    if (
        result["drift_total"] > result["selected_total"]
        or result["lookup_errors_total"] > result["selected_total"]
    ):
        raise DeploymentShadowGateVerificationError(
            "shadow gate window result counters are inconsistent"
        )
    for field in ("duration_seconds", "maximum_gap_seconds"):
        result[field] = _validate_float(
            result[field],
            f"shadow gate window {field}",
            0.0,
            7 * 24 * 60 * 60.0,
        )
    if result["last_report_age_seconds"] is not None:
        result["last_report_age_seconds"] = _validate_float(
            result["last_report_age_seconds"],
            "shadow gate last report age",
            0.0,
            7 * 24 * 60 * 60.0,
        )
    for field in ("started_at", "ended_at"):
        if result[field] is not None:
            _parse_timestamp(str(result[field]), f"shadow gate {field}")
    for field in ("first_event_id", "last_event_id"):
        if result[field] is not None and (
            not isinstance(result[field], str)
            or re.fullmatch(r"shadow-event-[0-9a-f]{32}", result[field]) is None
        ):
            raise DeploymentShadowGateVerificationError(
                f"shadow gate window {field} is invalid"
            )
    empty = result["runs"] == 0
    nullable_fields = (
        "started_at",
        "ended_at",
        "last_report_age_seconds",
        "first_event_id",
        "last_event_id",
    )
    if any((result[field] is None) != empty for field in nullable_fields):
        raise DeploymentShadowGateVerificationError(
            "shadow gate empty window binding is invalid"
        )
    if empty:
        if any(
            result[field] != 0
            for field in (
                "successful_runs",
                "failed_runs",
                "selected_total",
                "drift_total",
                "lookup_errors_total",
                "retried_lookups_total",
                "duration_seconds",
                "maximum_gap_seconds",
            )
        ):
            raise DeploymentShadowGateVerificationError(
                "shadow gate empty window counters are invalid"
            )
    else:
        started_at = _parse_timestamp(str(result["started_at"]), "shadow gate started_at")
        ended_at = _parse_timestamp(str(result["ended_at"]), "shadow gate ended_at")
        expected_duration = (ended_at - started_at).total_seconds()
        if expected_duration < 0 or abs(expected_duration - result["duration_seconds"]) > 0.001:
            raise DeploymentShadowGateVerificationError(
                "shadow gate window duration binding is invalid"
            )
        if result["maximum_gap_seconds"] > result["duration_seconds"]:
            raise DeploymentShadowGateVerificationError(
                "shadow gate maximum gap exceeds its window"
            )
    try:
        _validate_digest(
            str(result["history_chain_sha256"]),
            "shadow gate history chain",
        )
    except ValueError as exc:
        raise DeploymentShadowGateVerificationError(
            "shadow gate history chain is invalid"
        ) from exc
    if empty != (result["history_chain_sha256"] == "0" * 64):
        raise DeploymentShadowGateVerificationError(
            "shadow gate history chain empty binding is invalid"
        )
    return result


def _validate_violation(value: Any) -> dict[str, Any]:
    if not isinstance(value, Mapping) or set(value) != {"code", "expected", "actual"}:
        raise DeploymentShadowGateVerificationError("shadow gate violation is invalid")
    result = dict(value)
    if not isinstance(result["code"], str) or _VIOLATION_CODE_RE.fullmatch(
        result["code"]
    ) is None:
        raise DeploymentShadowGateVerificationError(
            "shadow gate violation code is invalid"
        )
    if not isinstance(result["expected"], str) or not result["expected"]:
        raise DeploymentShadowGateVerificationError(
            "shadow gate violation expected value is invalid"
        )
    if result["actual"] is not None:
        actual = result["actual"]
        if (
            isinstance(actual, bool)
            or not isinstance(actual, (int, float))
            or not 0 <= actual <= 1000000000
        ):
            raise DeploymentShadowGateVerificationError(
                "shadow gate violation actual value is invalid"
            )
    return result


def _validate_attestation(value: Any) -> dict[str, Any]:
    fields = {
        "schema_version",
        "attestation_id",
        "created_at",
        "expires_at",
        "gate_id",
        "decision",
        "provider_instance_sha256",
        "gate_report_sha256",
        "history_chain_sha256",
        "key_id",
        "algorithm",
        "signature",
    }
    if not isinstance(value, Mapping) or set(value) != fields:
        raise DeploymentShadowGateVerificationError(
            "shadow attestation contract is invalid"
        )
    result = dict(value)
    if result["schema_version"] != SHADOW_ATTESTATION_SCHEMA_VERSION:
        raise DeploymentShadowGateVerificationError(
            "shadow attestation schema_version is invalid"
        )
    if not isinstance(result["attestation_id"], str) or _ATTESTATION_ID_RE.fullmatch(
        result["attestation_id"]
    ) is None:
        raise DeploymentShadowGateVerificationError(
            "shadow attestation ID is invalid"
        )
    if not isinstance(result["gate_id"], str) or _GATE_ID_RE.fullmatch(
        result["gate_id"]
    ) is None:
        raise DeploymentShadowGateVerificationError(
            "shadow attestation gate ID is invalid"
        )
    if result["decision"] not in {"pass", "hold"}:
        raise DeploymentShadowGateVerificationError(
            "shadow attestation decision is invalid"
        )
    if result["algorithm"] != SHADOW_ATTESTATION_ALGORITHM:
        raise DeploymentShadowGateVerificationError(
            "shadow attestation algorithm is invalid"
        )
    _validate_key_id(str(result["key_id"]), "shadow attestation key_id")
    for field in (
        "provider_instance_sha256",
        "gate_report_sha256",
        "history_chain_sha256",
        "signature",
    ):
        try:
            _validate_digest(str(result[field]), f"shadow attestation {field}")
        except ValueError as exc:
            raise DeploymentShadowGateVerificationError(
                f"shadow attestation {field} is invalid"
            ) from exc
    for field in ("created_at", "expires_at"):
        _parse_timestamp(str(result[field]), f"shadow attestation {field}")
    identity_payload = {
        field: item
        for field, item in result.items()
        if field not in {"attestation_id", "signature"}
    }
    expected_id = (
        f"shadow-attestation-{_digest(_canonical_json(identity_payload))[:32]}"
    )
    if result["attestation_id"] != expected_id:
        raise DeploymentShadowGateVerificationError(
            "shadow attestation ID binding is invalid"
        )
    return result


def _read_gate_report(
    gate_dir: Path,
    *,
    expect_attestation: bool | None,
) -> dict[str, Any]:
    _validate_private_state_directory(gate_dir)
    try:
        entries = {entry.name for entry in os.scandir(gate_dir)}
    except OSError as exc:
        raise DeploymentShadowGateVerificationError(
            f"cannot list shadow gate directory: {exc}"
        ) from exc
    expected = {SHADOW_GATE_REPORT_FILE}
    if expect_attestation is True:
        expected.add(SHADOW_ATTESTATION_FILE)
    if expect_attestation is None:
        allowed = (
            {SHADOW_GATE_REPORT_FILE},
            {SHADOW_GATE_REPORT_FILE, SHADOW_ATTESTATION_FILE},
        )
        if entries not in allowed:
            raise DeploymentShadowGateVerificationError(
                "shadow gate artifact set is invalid"
            )
    elif entries != expected:
        raise DeploymentShadowGateVerificationError(
            "shadow gate artifact set is invalid"
        )
    return _read_contract(gate_dir / SHADOW_GATE_REPORT_FILE, _validate_gate_report)


def _append_violation(
    violations: list[dict[str, Any]],
    code: str,
    passed: bool,
    expected: str,
    actual: int | float | None,
) -> None:
    if not passed:
        violations.append({"code": code, "expected": expected, "actual": actual})


def _expected_violations(
    policy: ShadowGatePolicy,
    window: Mapping[str, Any],
) -> list[dict[str, Any]]:
    result: list[dict[str, Any]] = []
    required_coverage = max(
        0,
        policy.minimum_window_seconds - policy.maximum_gap_seconds,
    )
    _append_violation(
        result,
        "insufficient_runs",
        int(window["runs"]) >= policy.minimum_runs,
        f">={policy.minimum_runs}",
        int(window["runs"]),
    )
    _append_violation(
        result,
        "insufficient_window",
        float(window["duration_seconds"]) >= required_coverage,
        f">={required_coverage}",
        float(window["duration_seconds"]),
    )
    _append_violation(
        result,
        "insufficient_selected",
        int(window["selected_total"]) >= policy.minimum_selected_total,
        f">={policy.minimum_selected_total}",
        int(window["selected_total"]),
    )
    _append_violation(
        result,
        "failed_runs",
        int(window["failed_runs"]) == 0,
        "0",
        int(window["failed_runs"]),
    )
    _append_violation(
        result,
        "drift_detected",
        int(window["drift_total"]) <= policy.maximum_drift_total,
        f"<={policy.maximum_drift_total}",
        int(window["drift_total"]),
    )
    _append_violation(
        result,
        "lookup_errors",
        int(window["lookup_errors_total"]) <= policy.maximum_lookup_errors_total,
        f"<={policy.maximum_lookup_errors_total}",
        int(window["lookup_errors_total"]),
    )
    _append_violation(
        result,
        "observation_gap",
        float(window["maximum_gap_seconds"]) <= policy.maximum_gap_seconds,
        f"<={policy.maximum_gap_seconds}",
        float(window["maximum_gap_seconds"]),
    )
    last_age = window["last_report_age_seconds"]
    _append_violation(
        result,
        "observation_stale",
        last_age is not None and float(last_age) <= policy.maximum_report_age_seconds,
        f"<={policy.maximum_report_age_seconds}",
        float(last_age) if last_age is not None else None,
    )
    return result


def _sign(key: bytes, payload: Mapping[str, Any]) -> str:
    derived = hmac.new(key, _SIGNING_DOMAIN, hashlib.sha256).digest()
    return hmac.new(derived, _canonical_json(payload), hashlib.sha256).hexdigest()


def _key_from_environment() -> tuple[bytes, str]:
    key = os.environ.get(KEY_ENV)
    key_id = os.environ.get(KEY_ID_ENV)
    if key is None or key_id is None:
        raise DeploymentShadowGateConfigError(
            f"{KEY_ENV} and {KEY_ID_ENV} are required"
        )
    return key.encode("utf-8"), key_id


def _validate_int(value: Any, field_name: str, minimum: int, maximum: int) -> int:
    if isinstance(value, bool) or not isinstance(value, int) or not minimum <= value <= maximum:
        raise DeploymentShadowGateConfigError(f"{field_name} is invalid")
    return value


def _validate_float(
    value: Any,
    field_name: str,
    minimum: float,
    maximum: float,
) -> float:
    if (
        isinstance(value, bool)
        or not isinstance(value, (int, float))
        or not minimum <= float(value) <= maximum
    ):
        raise DeploymentShadowGateVerificationError(f"{field_name} is invalid")
    return float(value)


if __name__ == "__main__":
    raise SystemExit(main())
