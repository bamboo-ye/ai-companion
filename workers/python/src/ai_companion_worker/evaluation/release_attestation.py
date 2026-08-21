from __future__ import annotations

import argparse
import hashlib
import hmac
import json
import math
import os
import re
import stat
import sys
from datetime import UTC, datetime, timedelta
from pathlib import Path
from typing import Any, Mapping, Sequence

from .observability import (
    INTEGER_METRICS,
    METRIC_KEYS,
    REPORT_SCHEMA_VERSION,
    SNAPSHOT_SCHEMA_VERSION,
)
from .release_gate import GATE_REPORT_SCHEMA_VERSION


ATTESTATION_SCHEMA_VERSION = "observability-release-gate-attestation-v1"
ATTESTATION_ALGORITHM = "hmac-sha256"
ATTESTATION_FILE = "attestation.json"
REQUIRED_ARTIFACTS = (
    "after.json",
    "before.json",
    "gate-report.json",
    "gate-report.xml",
    "metrics-report.json",
    "metrics-report.xml",
)
OPTIONAL_ARTIFACTS = ("canary-report.json",)
MAX_ARTIFACT_BYTES = 4 * 1024 * 1024
MAX_ATTESTATION_BYTES = 64 * 1024
MAX_ATTESTATION_AGE_SECONDS = 24 * 60 * 60
MAX_CLOCK_SKEW_SECONDS = 30
KEY_ENV = "OBSERVABILITY_ATTESTATION_KEY"
KEY_ID_ENV = "OBSERVABILITY_ATTESTATION_KEY_ID"
_DIGEST_RE = re.compile(r"[0-9a-f]{64}")
_IDENTIFIER_RE = re.compile(r"[A-Za-z0-9][A-Za-z0-9._-]{0,127}")
_KEY_ID_RE = re.compile(r"[A-Za-z0-9][A-Za-z0-9._-]{0,63}")


class AttestationError(ValueError):
    pass


class AttestationConfigError(AttestationError):
    pass


class AttestationVerificationError(AttestationError):
    pass


def create_attestation(
    gate_dir: Path,
    key: bytes,
    key_id: str,
    *,
    max_age_seconds: int = 900,
    now: datetime | None = None,
) -> dict[str, Any]:
    signing_key = _validate_key(key)
    signing_key_id = _validate_key_id(key_id)
    current_time = _normalize_now(now)
    maximum_age = _validate_max_age(max_age_seconds)
    _validate_gate_directory(gate_dir)
    try:
        (gate_dir / ATTESTATION_FILE).lstat()
    except FileNotFoundError:
        pass
    else:
        raise AttestationVerificationError("attestation already exists")
    artifact_names = _gate_artifact_names(gate_dir, attested=False)

    artifacts, artifact_data = _read_artifacts(gate_dir, artifact_names)
    decision, gate_generated_at = _validate_gate_bundle(artifact_data)
    _validate_timestamp_age(
        gate_generated_at,
        current_time,
        maximum_age,
        "gate report",
    )
    run_id = _validate_run_id(gate_dir.name)
    payload: dict[str, Any] = {
        "schema_version": ATTESTATION_SCHEMA_VERSION,
        "created_at": _format_timestamp(current_time),
        "gate_generated_at": _format_timestamp(gate_generated_at),
        "run_id": run_id,
        "decision": decision,
        "key_id": signing_key_id,
        "algorithm": ATTESTATION_ALGORITHM,
        "gate_report_sha256": _digest(artifact_data["gate-report.json"]),
        "artifacts": artifacts,
    }
    signature = hmac.new(signing_key, _canonical_json(payload), hashlib.sha256).hexdigest()
    attestation = dict(payload)
    attestation["signature"] = signature
    _write_exclusive_private_json(gate_dir / ATTESTATION_FILE, attestation)
    return attestation


def verify_attestation(
    gate_dir: Path,
    key: bytes,
    expected_key_id: str,
    *,
    max_age_seconds: int = 900,
    require_decision: str = "promote",
    expected_run_id: str | None = None,
    now: datetime | None = None,
) -> dict[str, Any]:
    verification_key = _validate_key(key)
    key_id = _validate_key_id(expected_key_id)
    maximum_age = _validate_max_age(max_age_seconds)
    if require_decision not in {"promote", "rollback", "any"}:
        raise AttestationConfigError("require_decision must be promote, rollback, or any")
    if expected_run_id is not None:
        expected_run_id = _validate_run_id(expected_run_id)
    current_time = _normalize_now(now)

    _validate_gate_directory(gate_dir)
    artifact_names = _gate_artifact_names(gate_dir, attested=True)
    artifacts, artifact_data = _read_artifacts(gate_dir, artifact_names)
    attestation_data = _read_private_regular_file(
        gate_dir,
        ATTESTATION_FILE,
        MAX_ATTESTATION_BYTES,
    )
    attestation_value = _load_json(attestation_data, ATTESTATION_FILE)
    attestation = _validate_attestation_contract(attestation_value)

    if attestation["key_id"] != key_id:
        raise AttestationVerificationError("attestation key_id does not match expected signer")
    if expected_run_id is not None and attestation["run_id"] != expected_run_id:
        raise AttestationVerificationError("attestation run_id does not match expected run")
    if attestation["run_id"] != _validate_run_id(gate_dir.name):
        raise AttestationVerificationError("attestation run_id does not match gate directory")

    signature = str(attestation["signature"])
    signed_payload = {key: value for key, value in attestation.items() if key != "signature"}
    expected_signature = hmac.new(
        verification_key,
        _canonical_json(signed_payload),
        hashlib.sha256,
    ).hexdigest()
    if not hmac.compare_digest(signature, expected_signature):
        raise AttestationVerificationError("attestation signature is invalid")

    if attestation["artifacts"] != artifacts:
        raise AttestationVerificationError("attested artifact manifest does not match bundle")
    gate_report_digest = _digest(artifact_data["gate-report.json"])
    if attestation["gate_report_sha256"] != gate_report_digest:
        raise AttestationVerificationError("attested gate report digest does not match bundle")

    decision, gate_generated_at = _validate_gate_bundle(artifact_data)
    if attestation["decision"] != decision:
        raise AttestationVerificationError("attested decision does not match gate evidence")
    if require_decision != "any" and decision != require_decision:
        raise AttestationVerificationError(
            f"attested decision {decision} does not satisfy required decision {require_decision}"
        )

    created_at = _parse_timestamp(str(attestation["created_at"]), "attestation created_at")
    attested_gate_time = _parse_timestamp(
        str(attestation["gate_generated_at"]),
        "attestation gate_generated_at",
    )
    if attested_gate_time != gate_generated_at:
        raise AttestationVerificationError(
            "attested gate_generated_at does not match gate report"
        )
    if gate_generated_at > created_at + timedelta(seconds=MAX_CLOCK_SKEW_SECONDS):
        raise AttestationVerificationError("gate report was generated after attestation")
    _validate_timestamp_age(created_at, current_time, maximum_age, "attestation")
    _validate_timestamp_age(gate_generated_at, current_time, maximum_age, "gate report")
    return attestation


def main(argv: Sequence[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        description="Create or verify a signed observability release gate bundle"
    )
    subparsers = parser.add_subparsers(dest="command", required=True)
    create_parser = subparsers.add_parser("create")
    create_parser.add_argument("--gate-dir", type=Path, required=True)
    create_parser.add_argument("--max-age-seconds", type=int, default=900)
    verify_parser = subparsers.add_parser("verify")
    verify_parser.add_argument("--gate-dir", type=Path, required=True)
    verify_parser.add_argument("--max-age-seconds", type=int, default=900)
    verify_parser.add_argument(
        "--require-decision",
        choices=("promote", "rollback", "any"),
        default="promote",
    )
    verify_parser.add_argument("--expected-run-id")
    args = parser.parse_args(argv)

    try:
        key, key_id = _key_from_environment()
        if args.command == "create":
            attestation = create_attestation(
                args.gate_dir,
                key,
                key_id,
                max_age_seconds=args.max_age_seconds,
            )
        else:
            attestation = verify_attestation(
                args.gate_dir,
                key,
                key_id,
                max_age_seconds=args.max_age_seconds,
                require_decision=args.require_decision,
                expected_run_id=args.expected_run_id,
            )
    except AttestationConfigError as exc:
        print(f"observability attestation configuration error: {exc}", file=sys.stderr)
        return 2
    except (AttestationError, OSError) as exc:
        print(f"observability attestation verification error: {exc}", file=sys.stderr)
        return 1 if args.command == "verify" else 2

    print(
        json.dumps(
            {
                "algorithm": attestation["algorithm"],
                "decision": attestation["decision"],
                "key_id": attestation["key_id"],
                "run_id": attestation["run_id"],
                "status": "created" if args.command == "create" else "verified",
            },
            sort_keys=True,
        )
    )
    return 0


def _key_from_environment() -> tuple[bytes, str]:
    value = os.environ.get(KEY_ENV)
    key_id = os.environ.get(KEY_ID_ENV)
    if value is None:
        raise AttestationConfigError(f"{KEY_ENV} is required")
    if key_id is None:
        raise AttestationConfigError(f"{KEY_ID_ENV} is required")
    return value.encode("utf-8"), key_id


def _validate_key(key: bytes) -> bytes:
    if not isinstance(key, bytes) or len(key) < 32:
        raise AttestationConfigError("attestation key must contain at least 32 bytes")
    return key


def _validate_key_id(key_id: str) -> str:
    if not isinstance(key_id, str) or _KEY_ID_RE.fullmatch(key_id) is None:
        raise AttestationConfigError("attestation key_id is invalid")
    return key_id


def _validate_run_id(run_id: str) -> str:
    if _IDENTIFIER_RE.fullmatch(run_id) is None:
        raise AttestationVerificationError("gate run_id is invalid")
    return run_id


def _validate_max_age(value: int) -> int:
    if isinstance(value, bool) or not isinstance(value, int):
        raise AttestationConfigError("max age must be an integer")
    if not 1 <= value <= MAX_ATTESTATION_AGE_SECONDS:
        raise AttestationConfigError(
            f"max age must be between 1 and {MAX_ATTESTATION_AGE_SECONDS} seconds"
        )
    return value


def _validate_gate_directory(gate_dir: Path) -> None:
    try:
        metadata = gate_dir.lstat()
    except OSError as exc:
        raise AttestationVerificationError(f"cannot inspect gate directory: {exc}") from exc
    if not stat.S_ISDIR(metadata.st_mode) or gate_dir.is_symlink():
        raise AttestationVerificationError("gate directory must be a real directory")
    if stat.S_IMODE(metadata.st_mode) != 0o700:
        raise AttestationVerificationError("gate directory permissions must be 0700")
    if hasattr(os, "getuid") and metadata.st_uid != os.getuid():
        raise AttestationVerificationError("gate directory must be owned by the current user")


def _require_entry_set(gate_dir: Path, expected: set[str]) -> None:
    try:
        actual = {entry.name for entry in os.scandir(gate_dir)}
    except OSError as exc:
        raise AttestationVerificationError(f"cannot list gate directory: {exc}") from exc
    if actual != expected:
        missing = sorted(expected - actual)
        unexpected = sorted(actual - expected)
        raise AttestationVerificationError(
            f"gate artifact set is invalid; missing={missing}, unexpected={unexpected}"
        )


def _gate_artifact_names(gate_dir: Path, *, attested: bool) -> tuple[str, ...]:
    try:
        actual = {entry.name for entry in os.scandir(gate_dir)}
    except OSError as exc:
        raise AttestationVerificationError(f"cannot list gate directory: {exc}") from exc
    required = set(REQUIRED_ARTIFACTS)
    if attested:
        required.add(ATTESTATION_FILE)
    allowed = required | set(OPTIONAL_ARTIFACTS)
    if not required.issubset(actual) or not actual.issubset(allowed):
        missing = sorted(required - actual)
        unexpected = sorted(actual - allowed)
        raise AttestationVerificationError(
            f"gate artifact set is invalid; missing={missing}, unexpected={unexpected}"
        )
    return tuple((*REQUIRED_ARTIFACTS, *(name for name in OPTIONAL_ARTIFACTS if name in actual)))


def _read_artifacts(
    gate_dir: Path,
    names: Sequence[str],
) -> tuple[list[dict[str, Any]], dict[str, bytes]]:
    manifest: list[dict[str, Any]] = []
    data: dict[str, bytes] = {}
    for name in names:
        content = _read_private_regular_file(gate_dir, name, MAX_ARTIFACT_BYTES)
        data[name] = content
        manifest.append({"name": name, "sha256": _digest(content), "size": len(content)})
    return manifest, data


def _read_private_regular_file(gate_dir: Path, name: str, maximum: int) -> bytes:
    if Path(name).name != name or name in {".", ".."}:
        raise AttestationVerificationError("artifact name is invalid")
    path = gate_dir / name
    flags = os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0)
    try:
        descriptor = os.open(path, flags)
    except OSError as exc:
        raise AttestationVerificationError(f"cannot open artifact {name}: {exc}") from exc
    try:
        metadata = os.fstat(descriptor)
        if not stat.S_ISREG(metadata.st_mode):
            raise AttestationVerificationError(f"artifact {name} must be a regular file")
        if stat.S_IMODE(metadata.st_mode) != 0o600:
            raise AttestationVerificationError(f"artifact {name} permissions must be 0600")
        if metadata.st_nlink != 1:
            raise AttestationVerificationError(f"artifact {name} must not be hard-linked")
        if hasattr(os, "getuid") and metadata.st_uid != os.getuid():
            raise AttestationVerificationError(
                f"artifact {name} must be owned by the current user"
            )
        if metadata.st_size <= 0 or metadata.st_size > maximum:
            raise AttestationVerificationError(f"artifact {name} size is invalid")
        chunks: list[bytes] = []
        remaining = maximum + 1
        while remaining > 0:
            chunk = os.read(descriptor, min(64 * 1024, remaining))
            if not chunk:
                break
            chunks.append(chunk)
            remaining -= len(chunk)
        content = b"".join(chunks)
        if len(content) != metadata.st_size or len(content) > maximum:
            raise AttestationVerificationError(f"artifact {name} changed while being read")
        return content
    finally:
        os.close(descriptor)


def _validate_gate_bundle(data: Mapping[str, bytes]) -> tuple[str, datetime]:
    before = _load_json(data["before.json"], "before.json")
    after = _load_json(data["after.json"], "after.json")
    metrics_report = _load_json(data["metrics-report.json"], "metrics-report.json")
    gate_report = _load_json(data["gate-report.json"], "gate-report.json")
    canary_report = (
        _load_json(data["canary-report.json"], "canary-report.json")
        if "canary-report.json" in data
        else None
    )

    before_digest, before_metrics = _snapshot_contract(before, "before.json")
    after_digest, after_metrics = _snapshot_contract(after, "after.json")
    metrics_source, metrics_violations, baseline_version = _metrics_report_contract(
        metrics_report,
        before_metrics,
        after_metrics,
    )
    if metrics_source != {
        "before_sha256": before_digest,
        "after_sha256": after_digest,
    }:
        raise AttestationVerificationError(
            "metrics report source does not match snapshot evidence"
        )
    decision, gate_generated_at = _gate_report_contract(
        gate_report,
        metrics_violations,
    )
    gate_source = gate_report["source"]
    expected_source = {
        "before_sha256": before_digest,
        "after_sha256": after_digest,
        "baseline_version": baseline_version,
    }
    if any(gate_source.get(key) != value for key, value in expected_source.items()):
        raise AttestationVerificationError(
            "gate report source does not match metrics and snapshot evidence"
        )
    _validate_structured_canary_evidence(
        gate_report,
        canary_report,
        data.get("canary-report.json"),
    )
    return decision, gate_generated_at


def _snapshot_contract(value: Any, name: str) -> tuple[str, dict[str, float]]:
    if not isinstance(value, Mapping) or set(value) != {
        "schema_version",
        "generated_at",
        "source",
        "metrics",
    }:
        raise AttestationVerificationError(f"{name} contract is invalid")
    if value.get("schema_version") != SNAPSHOT_SCHEMA_VERSION:
        raise AttestationVerificationError(f"{name} schema_version is invalid")
    generated_at = value.get("generated_at")
    if not isinstance(generated_at, str):
        raise AttestationVerificationError(f"{name} generated_at is invalid")
    _parse_timestamp(generated_at, f"{name} generated_at")
    source = value.get("source")
    if not isinstance(source, Mapping) or set(source) != {"input_kind", "sha256"}:
        raise AttestationVerificationError(f"{name} source is invalid")
    digest = source.get("sha256")
    if source.get("input_kind") != "prometheus_text" or not _is_digest(digest):
        raise AttestationVerificationError(f"{name} source is invalid")
    metrics_value = value.get("metrics")
    if not isinstance(metrics_value, Mapping) or set(metrics_value) != METRIC_KEYS:
        raise AttestationVerificationError(f"{name} metrics are invalid")
    metrics: dict[str, float] = {}
    for metric_name, metric_value in metrics_value.items():
        if (
            not isinstance(metric_name, str)
            or not isinstance(metric_value, (int, float))
            or isinstance(metric_value, bool)
            or not math.isfinite(float(metric_value))
            or float(metric_value) < 0
        ):
            raise AttestationVerificationError(f"{name} metrics are invalid")
        number = float(metric_value)
        if metric_name in INTEGER_METRICS and not number.is_integer():
            raise AttestationVerificationError(f"{name} integer metric is invalid")
        metrics[metric_name] = number
    expected_derived = {
        "agent_terminal_failures": metrics["agent_runs_failed"]
        + metrics["agent_runs_timed_out"],
        "retry_exhausted_total": metrics["retry_exhausted"]
        + metrics["retry_deadline_exhausted"],
        "retry_settled": metrics["retry_recovered"]
        + metrics["retry_exhausted"]
        + metrics["retry_deadline_exhausted"],
    }
    if any(
        not _numbers_equal(metrics[key], expected)
        for key, expected in expected_derived.items()
    ):
        raise AttestationVerificationError(f"{name} derived metrics are inconsistent")
    settled = metrics["retry_settled"]
    expected_ratio = metrics["retry_recovered"] / settled if settled > 0 else 0.0
    if not 0 <= metrics["retry_recovery_ratio"] <= 1 or not _numbers_equal(
        metrics["retry_recovery_ratio"],
        expected_ratio,
    ):
        raise AttestationVerificationError(f"{name} retry recovery ratio is inconsistent")
    return str(digest), metrics


def _metrics_report_contract(
    value: Any,
    before_metrics: Mapping[str, float],
    after_metrics: Mapping[str, float],
) -> tuple[dict[str, str], list[Any], str]:
    if not isinstance(value, Mapping) or set(value) != {
        "schema_version",
        "source",
        "summary",
        "regression",
    }:
        raise AttestationVerificationError("metrics-report.json contract is invalid")
    if value.get("schema_version") != REPORT_SCHEMA_VERSION:
        raise AttestationVerificationError("metrics-report.json schema_version is invalid")
    source = value.get("source")
    if not isinstance(source, Mapping) or set(source) != {"before_sha256", "after_sha256"}:
        raise AttestationVerificationError("metrics-report.json source is invalid")
    before_digest = source.get("before_sha256")
    after_digest = source.get("after_sha256")
    if not _is_digest(before_digest) or not _is_digest(after_digest):
        raise AttestationVerificationError("metrics-report.json source is invalid")
    summary = value.get("summary")
    if not isinstance(summary, Mapping) or set(summary) != {"before", "after", "delta"}:
        raise AttestationVerificationError("metrics-report.json summary is invalid")
    reported_before = _metric_mapping(summary.get("before"), "metrics report before", False)
    reported_after = _metric_mapping(summary.get("after"), "metrics report after", False)
    reported_delta = _metric_mapping(summary.get("delta"), "metrics report delta", True)
    if not _metric_mappings_equal(reported_before, before_metrics):
        raise AttestationVerificationError(
            "metrics report before summary does not match snapshot evidence"
        )
    if not _metric_mappings_equal(reported_after, after_metrics):
        raise AttestationVerificationError(
            "metrics report after summary does not match snapshot evidence"
        )
    expected_delta = {
        key: after_metrics[key] - before_metrics[key]
        for key in METRIC_KEYS
    }
    if not _metric_mappings_equal(reported_delta, expected_delta):
        raise AttestationVerificationError("metrics report delta is inconsistent")
    regression = value.get("regression")
    if not isinstance(regression, Mapping) or set(regression) != {
        "baseline_version",
        "violations",
    }:
        raise AttestationVerificationError("metrics-report.json regression is invalid")
    baseline_version = regression.get("baseline_version")
    violations = regression.get("violations")
    if not isinstance(baseline_version, str) or not baseline_version:
        raise AttestationVerificationError("metrics-report.json baseline version is invalid")
    if not isinstance(violations, list):
        raise AttestationVerificationError("metrics-report.json violations are invalid")
    return (
        {"before_sha256": str(before_digest), "after_sha256": str(after_digest)},
        violations,
        baseline_version,
    )


def _metric_mapping(value: Any, field: str, allow_negative: bool) -> dict[str, float]:
    if not isinstance(value, Mapping) or set(value) != METRIC_KEYS:
        raise AttestationVerificationError(f"{field} is invalid")
    result: dict[str, float] = {}
    for key, raw in value.items():
        if (
            not isinstance(key, str)
            or not isinstance(raw, (int, float))
            or isinstance(raw, bool)
            or not math.isfinite(float(raw))
            or (not allow_negative and float(raw) < 0)
        ):
            raise AttestationVerificationError(f"{field} is invalid")
        result[key] = float(raw)
    return result


def _metric_mappings_equal(
    left: Mapping[str, float],
    right: Mapping[str, float],
) -> bool:
    return set(left) == set(right) and all(
        _numbers_equal(left[key], right[key]) for key in left
    )


def _numbers_equal(left: float, right: float) -> bool:
    return math.isclose(left, right, rel_tol=1e-9, abs_tol=1e-9)


def _gate_report_contract(
    value: Any,
    metrics_violations: list[Any],
) -> tuple[str, datetime]:
    if not isinstance(value, Mapping) or set(value) != {
        "schema_version",
        "generated_at",
        "decision",
        "source",
        "canary",
        "phases",
        "violations",
    }:
        raise AttestationVerificationError("gate-report.json contract is invalid")
    if value.get("schema_version") != GATE_REPORT_SCHEMA_VERSION:
        raise AttestationVerificationError("gate-report.json schema_version is invalid")
    generated_at_value = value.get("generated_at")
    if not isinstance(generated_at_value, str):
        raise AttestationVerificationError("gate-report.json generated_at is invalid")
    generated_at = _parse_timestamp(
        generated_at_value,
        "gate-report.json generated_at",
    )
    source = value.get("source")
    source_fields = {
        "canary_spec_sha256",
        "command_sha256",
        "before_sha256",
        "after_sha256",
        "baseline_version",
        "structured_canary_report_sha256",
    }
    if not isinstance(source, Mapping) or set(source) != source_fields:
        raise AttestationVerificationError("gate-report.json source is invalid")
    if not _is_digest(source.get("canary_spec_sha256")) or not _is_digest(
        source.get("command_sha256")
    ):
        raise AttestationVerificationError("gate-report.json source is invalid")
    canary = value.get("canary")
    if not isinstance(canary, Mapping) or set(canary) != {
        "name",
        "status",
        "exit_code",
        "duration_ms",
        "report",
    }:
        raise AttestationVerificationError("gate-report.json canary is invalid")
    status_value = canary.get("status")
    exit_code = canary.get("exit_code")
    duration = canary.get("duration_ms")
    if status_value not in {"passed", "failed", "timed_out"}:
        raise AttestationVerificationError("completed gate canary status is invalid")
    if exit_code is not None and (not isinstance(exit_code, int) or isinstance(exit_code, bool)):
        raise AttestationVerificationError("gate-report.json canary exit_code is invalid")
    if not isinstance(duration, int) or isinstance(duration, bool) or duration < 0:
        raise AttestationVerificationError("gate-report.json canary duration is invalid")
    phases = value.get("phases")
    phase_names = {
        "before_snapshot",
        "canary",
        "stabilization",
        "after_snapshot",
        "evaluation",
    }
    if not isinstance(phases, Mapping) or set(phases) != phase_names:
        raise AttestationVerificationError("gate-report.json phases are invalid")
    allowed_phase_values = {"passed", "failed", "timed_out"}
    if any(phase not in allowed_phase_values for phase in phases.values()):
        raise AttestationVerificationError("gate-report.json phases are invalid")
    violations = value.get("violations")
    if not isinstance(violations, list):
        raise AttestationVerificationError("gate-report.json violations are invalid")
    if status_value == "passed":
        if exit_code != 0 or phases.get("canary") != "passed":
            raise AttestationVerificationError("passed canary evidence is inconsistent")
        if violations != metrics_violations:
            raise AttestationVerificationError(
                "gate violations do not match metrics violations"
            )
    else:
        if phases.get("canary") != status_value:
            raise AttestationVerificationError("failed canary evidence is inconsistent")
        if not violations or not isinstance(violations[0], Mapping):
            raise AttestationVerificationError("failed canary violation is missing")
        expected_code = "canary_timed_out" if status_value == "timed_out" else "canary_failed"
        allowed_codes = {
            expected_code,
            "canary_report_failed",
            "canary_report_invalid",
            "canary_concurrent_run",
            "canary_lease_invalid",
        }
        if violations[0].get("code") not in allowed_codes or violations[1:] != metrics_violations:
            raise AttestationVerificationError("failed canary violations are inconsistent")

    healthy = (
        status_value == "passed"
        and exit_code == 0
        and all(phase == "passed" for phase in phases.values())
        and not violations
    )
    expected_decision = "promote" if healthy else "rollback"
    if value.get("decision") != expected_decision:
        raise AttestationVerificationError("gate decision is inconsistent with evidence")
    return expected_decision, generated_at


def _validate_structured_canary_evidence(
    gate_report: Mapping[str, Any],
    canary_report: Any,
    canary_report_bytes: bytes | None,
) -> None:
    source = gate_report.get("source")
    canary = gate_report.get("canary")
    if not isinstance(source, Mapping) or not isinstance(canary, Mapping):
        raise AttestationVerificationError("gate report structured canary evidence is invalid")
    digest = source.get("structured_canary_report_sha256")
    embedded = canary.get("report")
    canary_name = canary.get("name")
    if canary_name not in {"agent-observability", "agent-direct"}:
        if digest is not None or embedded is not None or canary_report is not None:
            raise AttestationVerificationError("unexpected structured canary evidence")
        return
    violation_codes = {
        item.get("code")
        for item in gate_report.get("violations", [])
        if isinstance(item, Mapping)
    }
    if canary.get("status") == "failed" and violation_codes & {
        "canary_concurrent_run",
        "canary_lease_invalid",
    }:
        if digest is not None or embedded is not None or canary_report is not None:
            raise AttestationVerificationError("unexpected structured canary evidence")
        return
    if canary_report_bytes is None or not _is_digest(digest):
        raise AttestationVerificationError("structured canary report evidence is missing")
    if str(digest) != _digest(canary_report_bytes):
        raise AttestationVerificationError("structured canary report digest is invalid")
    if not isinstance(canary_report, Mapping):
        raise AttestationVerificationError("structured canary report contract is invalid")
    if canary_name == "agent-direct":
        _validate_direct_canary_evidence(canary, embedded, canary_report)
        return
    if set(canary_report) != {
        "schema_version", "generated_at", "decision", "source", "summary", "violations"
    }:
        raise AttestationVerificationError("structured canary report contract is invalid")
    report_source = canary_report.get("source")
    report_summary = canary_report.get("summary")
    if (
        canary_report.get("schema_version") != "agent-observability-canary-report-v1"
        or canary_report.get("decision") not in {"pass", "fail"}
        or not isinstance(report_source, Mapping)
        or set(report_source) != {"baseline_version", "baseline_sha256"}
        or not _is_digest(report_source.get("baseline_sha256"))
        or not isinstance(report_summary, Mapping)
    ):
        raise AttestationVerificationError("structured canary report contract is invalid")
    generated_at = canary_report.get("generated_at")
    if not isinstance(generated_at, str):
        raise AttestationVerificationError("structured canary report timestamp is invalid")
    _parse_timestamp(generated_at, "structured canary report generated_at")
    expected_embedded = {
        "schema_version": canary_report["schema_version"],
        "decision": canary_report["decision"],
        "baseline_version": report_source.get("baseline_version"),
        "summary": report_summary,
    }
    if embedded != expected_embedded:
        raise AttestationVerificationError("structured canary summary does not match evidence")
    violations = canary_report.get("violations")
    if not isinstance(violations, list) or (
        canary_report.get("decision") == "pass" and violations
    ) or (canary_report.get("decision") == "fail" and not violations):
        raise AttestationVerificationError("structured canary decision is inconsistent")
    if canary.get("status") == "passed" and (
        canary_report.get("decision") != "pass" or canary.get("exit_code") != 0
    ):
        raise AttestationVerificationError("passed structured canary evidence is inconsistent")


def _validate_direct_canary_evidence(
    canary: Mapping[str, Any],
    embedded: Any,
    canary_report: Mapping[str, Any],
) -> None:
    from .direct_canary import DirectCanaryError, validate_report

    try:
        validated = validate_report(
            canary_report,
            baseline_path=Path(
                os.environ.get(
                    "AGENT_DIRECT_CANARY_BASELINE",
                    "evals/agent/baselines/direct-canary.v1.json",
                )
            ),
            suite_path=Path(
                os.environ.get(
                    "AGENT_DIRECT_CANARY_SUITE",
                    "evals/agent/suites/direct-canary.v1.json",
                )
            ),
        )
    except DirectCanaryError as exc:
        raise AttestationVerificationError("direct canary report contract is invalid") from exc
    source = validated["source"]
    expected_embedded = {
        "schema_version": validated["schema_version"],
        "decision": validated["decision"],
        "baseline_version": source["baseline_version"],
        "summary": validated["summary"],
    }
    if embedded != expected_embedded:
        raise AttestationVerificationError("structured canary summary does not match evidence")
    if canary.get("status") == "passed" and (
        validated["decision"] != "pass" or canary.get("exit_code") != 0
    ):
        raise AttestationVerificationError("passed structured canary evidence is inconsistent")


def _validate_attestation_contract(value: Any) -> dict[str, Any]:
    fields = {
        "schema_version",
        "created_at",
        "gate_generated_at",
        "run_id",
        "decision",
        "key_id",
        "algorithm",
        "gate_report_sha256",
        "artifacts",
        "signature",
    }
    if not isinstance(value, Mapping) or set(value) != fields:
        raise AttestationVerificationError("attestation contract is invalid")
    result = dict(value)
    if result.get("schema_version") != ATTESTATION_SCHEMA_VERSION:
        raise AttestationVerificationError("attestation schema_version is invalid")
    if result.get("algorithm") != ATTESTATION_ALGORITHM:
        raise AttestationVerificationError("attestation algorithm is invalid")
    if result.get("decision") not in {"promote", "rollback"}:
        raise AttestationVerificationError("attestation decision is invalid")
    created_at = result.get("created_at")
    gate_generated_at = result.get("gate_generated_at")
    run_id = result.get("run_id")
    if not isinstance(created_at, str) or not isinstance(gate_generated_at, str):
        raise AttestationVerificationError("attestation timestamp is invalid")
    if not isinstance(run_id, str):
        raise AttestationVerificationError("attestation run_id is invalid")
    _parse_timestamp(created_at, "attestation created_at")
    _parse_timestamp(gate_generated_at, "attestation gate_generated_at")
    _validate_run_id(run_id)
    if not isinstance(result.get("key_id"), str) or _KEY_ID_RE.fullmatch(
        str(result["key_id"])
    ) is None:
        raise AttestationVerificationError("attestation key_id is invalid")
    if not _is_digest(result.get("gate_report_sha256")) or not _is_digest(
        result.get("signature")
    ):
        raise AttestationVerificationError("attestation digest is invalid")
    artifacts = result.get("artifacts")
    if not isinstance(artifacts, list) or len(artifacts) not in {
        len(REQUIRED_ARTIFACTS),
        len(REQUIRED_ARTIFACTS) + len(OPTIONAL_ARTIFACTS),
    }:
        raise AttestationVerificationError("attestation artifacts are invalid")
    names: list[str] = []
    allowed_artifacts = set(REQUIRED_ARTIFACTS) | set(OPTIONAL_ARTIFACTS)
    for artifact in artifacts:
        if not isinstance(artifact, Mapping) or set(artifact) != {"name", "sha256", "size"}:
            raise AttestationVerificationError("attestation artifact entry is invalid")
        name = artifact.get("name")
        size = artifact.get("size")
        if (
            not isinstance(name, str)
            or name not in allowed_artifacts
            or not _is_digest(artifact.get("sha256"))
            or not isinstance(size, int)
            or isinstance(size, bool)
            or not 1 <= size <= MAX_ARTIFACT_BYTES
        ):
            raise AttestationVerificationError("attestation artifact entry is invalid")
        names.append(name)
    expected_names = tuple(
        (*REQUIRED_ARTIFACTS, *(name for name in OPTIONAL_ARTIFACTS if name in names))
    )
    if tuple(names) != expected_names:
        raise AttestationVerificationError("attestation artifact order is invalid")
    return result


def _load_json(data: bytes, name: str) -> Any:
    try:
        return json.loads(data, object_pairs_hook=_reject_duplicate_keys)
    except (UnicodeDecodeError, json.JSONDecodeError, ValueError) as exc:
        raise AttestationVerificationError(f"invalid JSON artifact {name}: {exc}") from exc


def _reject_duplicate_keys(pairs: list[tuple[str, Any]]) -> dict[str, Any]:
    result: dict[str, Any] = {}
    for key, value in pairs:
        if key in result:
            raise ValueError(f"duplicate JSON key: {key}")
        result[key] = value
    return result


def _parse_timestamp(value: str, field: str) -> datetime:
    try:
        timestamp = datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError as exc:
        raise AttestationVerificationError(f"{field} is invalid") from exc
    if timestamp.tzinfo is None:
        raise AttestationVerificationError(f"{field} must include a timezone")
    return timestamp.astimezone(UTC)


def _validate_timestamp_age(
    timestamp: datetime,
    now: datetime,
    max_age_seconds: int,
    field: str,
) -> None:
    age = (now - timestamp).total_seconds()
    if age < -MAX_CLOCK_SKEW_SECONDS:
        raise AttestationVerificationError(f"{field} timestamp is in the future")
    if age > max_age_seconds:
        raise AttestationVerificationError(f"{field} has expired")


def _normalize_now(value: datetime | None) -> datetime:
    result = value or datetime.now(UTC)
    if result.tzinfo is None:
        raise AttestationConfigError("current time must include a timezone")
    return result.astimezone(UTC)


def _format_timestamp(value: datetime) -> str:
    return value.astimezone(UTC).isoformat().replace("+00:00", "Z")


def _canonical_json(value: Mapping[str, Any]) -> bytes:
    return json.dumps(
        value,
        ensure_ascii=False,
        sort_keys=True,
        separators=(",", ":"),
    ).encode("utf-8")


def _digest(value: bytes) -> str:
    return hashlib.sha256(value).hexdigest()


def _is_digest(value: Any) -> bool:
    return isinstance(value, str) and _DIGEST_RE.fullmatch(value) is not None


def _write_exclusive_private_json(path: Path, value: Mapping[str, Any]) -> None:
    flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_NOFOLLOW", 0)
    try:
        descriptor = os.open(path, flags, 0o600)
    except FileExistsError as exc:
        raise AttestationVerificationError("attestation already exists") from exc
    try:
        content = json.dumps(value, ensure_ascii=False, indent=2).encode("utf-8") + b"\n"
        with os.fdopen(descriptor, "wb", closefd=False) as output:
            output.write(content)
            output.flush()
            os.fsync(output.fileno())
        os.chmod(path, 0o600)
    except BaseException:
        try:
            path.unlink()
        except OSError:
            pass
        raise
    finally:
        os.close(descriptor)


if __name__ == "__main__":
    raise SystemExit(main())
