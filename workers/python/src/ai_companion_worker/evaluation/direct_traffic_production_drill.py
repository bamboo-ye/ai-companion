from __future__ import annotations

import argparse
import hashlib
import hmac
import json
import os
import re
import subprocess
import sys
from datetime import datetime, timedelta
from pathlib import Path
from typing import Any, Mapping, Sequence

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


REPORT_SCHEMA_VERSION = "agent-direct-traffic-production-drill-v1"
ATTESTATION_SCHEMA_VERSION = "agent-direct-traffic-production-drill-attestation-v1"
REPORT_FILE = "direct-traffic-production-drill-report.json"
ATTESTATION_FILE = "direct-traffic-production-drill-attestation.json"
KEY_ENV = "OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_DRILL_KEY"
KEY_ID_ENV = "OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_DRILL_KEY_ID"
MODE = "local-production-integration"
MAX_ATTESTATION_SECONDS = 24 * 60 * 60
MAX_REPORT_AGE_SECONDS = 24 * 60 * 60
_SIGNING_DOMAIN = b"ai-companion/direct-traffic-production-drill-attestation/v1\0"
_CHAIN_DOMAIN = b"ai-companion/direct-traffic-production-drill-chain/v1\0"
_DIGEST_RE = re.compile(r"[0-9a-f]{64}")
_ATTESTATION_ID_RE = re.compile(
    r"direct-traffic-production-drill-attestation-[0-9a-f]{32}"
)
_RAN_RE = re.compile(r"Ran ([1-9][0-9]*) tests? in ")

_TRANSITIONS = (
    ("activation", 0, 5, 0, 1, "agent-direct-traffic-production-receipt-v1"),
    ("emergency_rollback", 5, 0, 1, 2, "agent-direct-traffic-production-emergency-receipt-v1"),
    ("recovery", 0, 5, 2, 3, "agent-direct-traffic-production-recovery-receipt-v1"),
    ("expansion_10", 5, 10, 3, 4, "agent-direct-traffic-production-expansion-10-receipt-v1"),
    ("expansion_25", 10, 25, 4, 5, "agent-direct-traffic-production-expansion-25-receipt-v1"),
    ("expansion_50", 25, 50, 5, 6, "agent-direct-traffic-production-expansion-50-receipt-v1"),
    ("expansion_100", 50, 100, 6, 7, "agent-direct-traffic-production-expansion-100-receipt-v1"),
)
_CHECKS = {
    "cross_stage_receipt_rejected",
    "emergency_rollback_isolated",
    "exact_seven_operation_chain",
    "full_chain_integration_suite_passed",
    "idempotent_replay",
    "namespace_and_credentials_isolated",
    "provider_independent_proof_verification",
    "tampered_proof_rejected",
    "terminal_stage_holds",
    "timeout_after_commit_reconciled",
}
_SOURCE_PATHS = (
    "evals/agent/baselines/direct-rollout.v1.json",
    "workers/python/tests/test_agent_direct_traffic_production.py",
    "workers/python/src/ai_companion_worker/evaluation/direct_traffic_production_stage.py",
    "workers/python/src/ai_companion_worker/evaluation/direct_traffic_production.py",
    "workers/python/src/ai_companion_worker/evaluation/direct_traffic_production_emergency.py",
    "workers/python/src/ai_companion_worker/evaluation/direct_traffic_production_recovery.py",
    "workers/python/src/ai_companion_worker/evaluation/direct_traffic_production_expansion.py",
    "workers/python/src/ai_companion_worker/evaluation/direct_traffic_production_expansion_25.py",
    "workers/python/src/ai_companion_worker/evaluation/direct_traffic_production_expansion_50.py",
    "workers/python/src/ai_companion_worker/evaluation/direct_traffic_production_expansion_100.py",
)
_PRODUCTION_SECRET_ENV_SUFFIXES = (
    "TOKEN",
    "ROLLOUT_KEY",
    "CERTIFICATION_KEY",
    "SHADOW_GATE_KEY",
    "GATE_KEY",
)
_PRODUCTION_SECRET_ENV_PREFIXES = (
    "OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_",
    "OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EMERGENCY_",
    "OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_RECOVERY_",
    "OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_",
    "OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_25_",
    "OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_50_",
    "OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_100_",
)


class DirectTrafficProductionDrillError(ValueError):
    pass


class DirectTrafficProductionDrillConfigError(DirectTrafficProductionDrillError):
    pass


class DirectTrafficProductionDrillVerificationError(DirectTrafficProductionDrillError):
    pass


def run_local_production_drill(
    repository_root: Path,
    *,
    python_executable: str = sys.executable,
    timeout_seconds: int = 120,
    now: datetime | None = None,
) -> dict[str, Any]:
    root = _validate_repository_root(repository_root)
    timeout = _validate_timeout(timeout_seconds)
    executable = str(Path(python_executable).resolve())
    command = (
        executable,
        "-m",
        "unittest",
        "workers/python/tests/test_agent_direct_traffic_production.py",
    )
    environment = dict(os.environ)
    source_root = str(root / "workers" / "python" / "src")
    existing_path = environment.get("PYTHONPATH")
    environment["PYTHONPATH"] = (
        source_root if not existing_path else f"{source_root}{os.pathsep}{existing_path}"
    )
    try:
        completed = subprocess.run(
            command,
            cwd=root,
            env=environment,
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            check=False,
            timeout=timeout,
        )
    except (OSError, subprocess.TimeoutExpired) as exc:
        raise DirectTrafficProductionDrillVerificationError(
            "production integration suite could not complete"
        ) from exc
    output = bytes(completed.stdout)
    try:
        decoded = output.decode("utf-8")
    except UnicodeDecodeError as exc:
        raise DirectTrafficProductionDrillVerificationError(
            "production integration suite output is not UTF-8"
        ) from exc
    match = _RAN_RE.search(decoded)
    tests_run = int(match.group(1)) if match else 0
    if completed.returncode != 0 or tests_run < 1 or "\nOK\n" not in decoded:
        raise DirectTrafficProductionDrillVerificationError(
            "production integration suite did not pass"
        )

    transitions: list[dict[str, Any]] = []
    previous_chain = "0" * 64
    for name, current, target, expected_revision, result_revision, receipt_schema in _TRANSITIONS:
        event: dict[str, Any] = {
            "name": name,
            "current_traffic_percent": current,
            "target_traffic_percent": target,
            "expected_revision": expected_revision,
            "result_revision": result_revision,
            "expected_operations": expected_revision,
            "result_operations": result_revision,
            "receipt_schema_version": receipt_schema,
            "previous_chain_sha256": previous_chain,
            "chain_sha256": "",
        }
        event["chain_sha256"] = _transition_digest(event)
        previous_chain = str(event["chain_sha256"])
        transitions.append(event)

    report = {
        "schema_version": REPORT_SCHEMA_VERSION,
        "generated_at": _format_timestamp(_normalize_now(now)),
        "mode": MODE,
        "decision": "pass",
        "contains_live_provider_evidence": False,
        "suite": {
            "test_module": "workers/python/tests/test_agent_direct_traffic_production.py",
            "tests_run": tests_run,
            "exit_code": completed.returncode,
            "output_sha256": hashlib.sha256(output).hexdigest(),
        },
        "sources": _source_manifest(root),
        "transitions": transitions,
        "checks": sorted(_CHECKS),
        "terminal": {
            "current_traffic_percent": 100,
            "revision": 7,
            "operations": 7,
            "rollout_decision": "hold",
            "rollout_reason": "maximum_stage_reached",
            "chain_sha256": previous_chain,
        },
    }
    return _validate_report(report)


def write_attested_production_drill_bundle(
    output_root: Path,
    report: Mapping[str, Any],
    key: bytes,
    key_id: str,
    *,
    ttl_seconds: int = 3600,
    now: datetime | None = None,
) -> Path:
    validated = _validate_report(report)
    signing_key = _validate_key(key, "production drill attestation key")
    signing_key_id = _validate_key_id(key_id, "production drill attestation key_id")
    ttl = _validate_ttl(ttl_seconds)
    current_time = _normalize_now(now)
    generated_at = _parse_timestamp(str(validated["generated_at"]), "drill generated_at")
    if current_time < generated_at:
        current_time = generated_at
    try:
        _ensure_private_state_directory(output_root)
        output_dir = output_root / f"production-drill-{_digest(_canonical_json(validated))[:16]}"
        _ensure_private_state_directory(output_dir)
        if any(os.scandir(output_dir)):
            raise DirectTrafficProductionDrillVerificationError(
                "production drill output directory must be empty"
            )
        _write_exclusive_private_json(output_dir / REPORT_FILE, validated)
        payload: dict[str, Any] = {
            "schema_version": ATTESTATION_SCHEMA_VERSION,
            "attestation_id": "",
            "created_at": _format_timestamp(current_time),
            "expires_at": _format_timestamp(current_time + timedelta(seconds=ttl)),
            "decision": "pass",
            "mode": MODE,
            "contains_live_provider_evidence": False,
            "report_sha256": _digest(_canonical_json(validated)),
            "source_manifest_sha256": _digest(_canonical_json(validated["sources"])),
            "terminal_chain_sha256": validated["terminal"]["chain_sha256"],
            "key_id": signing_key_id,
            "algorithm": "hmac-sha256",
        }
        identity = {
            field: item for field, item in payload.items() if field != "attestation_id"
        }
        payload["attestation_id"] = (
            "direct-traffic-production-drill-attestation-"
            f"{_digest(_canonical_json(identity))[:32]}"
        )
        attestation = _validate_attestation(
            {**payload, "signature": _sign(signing_key, payload)}
        )
        _write_exclusive_private_json(output_dir / ATTESTATION_FILE, attestation)
        return output_dir
    except (DeploymentResolutionError, OSError) as exc:
        raise DirectTrafficProductionDrillVerificationError(
            "cannot write private production drill bundle"
        ) from exc


def verify_attested_production_drill_bundle(
    bundle_dir: Path,
    key: bytes,
    key_id: str,
    *,
    repository_root: Path | None = None,
    max_age_seconds: int = 3600,
    now: datetime | None = None,
) -> dict[str, Any]:
    verification_key = _validate_key(key, "production drill attestation key")
    expected_key_id = _validate_key_id(key_id, "production drill attestation key_id")
    maximum_age = _validate_ttl(max_age_seconds)
    try:
        if {entry.name for entry in os.scandir(bundle_dir)} != {
            REPORT_FILE,
            ATTESTATION_FILE,
        }:
            raise DirectTrafficProductionDrillVerificationError(
                "production drill bundle artifact set is invalid"
            )
        report = _read_contract(bundle_dir / REPORT_FILE, _validate_report)
        attestation = _read_contract(
            bundle_dir / ATTESTATION_FILE, _validate_attestation
        )
    except (DeploymentResolutionError, OSError) as exc:
        raise DirectTrafficProductionDrillVerificationError(
            "cannot read private production drill bundle"
        ) from exc
    unsigned = {field: item for field, item in attestation.items() if field != "signature"}
    if attestation["key_id"] != expected_key_id or not hmac.compare_digest(
        str(attestation["signature"]), _sign(verification_key, unsigned)
    ):
        raise DirectTrafficProductionDrillVerificationError(
            "production drill signature is invalid"
        )
    expected = {
        "decision": report["decision"],
        "mode": report["mode"],
        "contains_live_provider_evidence": report["contains_live_provider_evidence"],
        "report_sha256": _digest(_canonical_json(report)),
        "source_manifest_sha256": _digest(_canonical_json(report["sources"])),
        "terminal_chain_sha256": report["terminal"]["chain_sha256"],
    }
    if any(attestation.get(field) != item for field, item in expected.items()):
        raise DirectTrafficProductionDrillVerificationError(
            "production drill attestation binding is invalid"
        )
    current_time = _normalize_now(now)
    generated = _parse_timestamp(str(report["generated_at"]), "drill generated_at")
    created = _parse_timestamp(str(attestation["created_at"]), "drill created_at")
    expires = _parse_timestamp(str(attestation["expires_at"]), "drill expires_at")
    if (
        generated > created
        or created > current_time
        or expires <= created
        or expires - created > timedelta(seconds=MAX_ATTESTATION_SECONDS)
        or current_time >= expires
        or current_time - created > timedelta(seconds=maximum_age)
        or current_time - generated > timedelta(seconds=maximum_age)
    ):
        raise DirectTrafficProductionDrillVerificationError(
            "production drill evidence is stale or has invalid time bounds"
        )
    if repository_root is not None and report["sources"] != _source_manifest(
        _validate_repository_root(repository_root)
    ):
        raise DirectTrafficProductionDrillVerificationError(
            "production drill sources do not match the current repository"
        )
    return report


def _source_manifest(root: Path) -> list[dict[str, str]]:
    result: list[dict[str, str]] = []
    for relative in _SOURCE_PATHS:
        path = root / relative
        try:
            metadata = path.lstat()
            content = path.read_bytes()
        except OSError as exc:
            raise DirectTrafficProductionDrillConfigError(
                f"cannot read production drill source {relative}"
            ) from exc
        if not path.is_file() or path.is_symlink() or len(content) != metadata.st_size:
            raise DirectTrafficProductionDrillConfigError(
                f"production drill source {relative} is unsafe"
            )
        result.append({"path": relative, "sha256": hashlib.sha256(content).hexdigest()})
    return result


def _validate_report(value: Any) -> dict[str, Any]:
    fields = {
        "schema_version",
        "generated_at",
        "mode",
        "decision",
        "contains_live_provider_evidence",
        "suite",
        "sources",
        "transitions",
        "checks",
        "terminal",
    }
    if not isinstance(value, Mapping) or set(value) != fields:
        raise DirectTrafficProductionDrillVerificationError(
            "production drill report contract is invalid"
        )
    report = dict(value)
    if any(
        (
            report["schema_version"] != REPORT_SCHEMA_VERSION,
            report["mode"] != MODE,
            report["decision"] != "pass",
            report["contains_live_provider_evidence"] is not False,
        )
    ):
        raise DirectTrafficProductionDrillVerificationError(
            "production drill report identity is invalid"
        )
    _parse_timestamp(str(report["generated_at"]), "drill generated_at")
    suite = report["suite"]
    if (
        not isinstance(suite, Mapping)
        or set(suite) != {"test_module", "tests_run", "exit_code", "output_sha256"}
        or suite["test_module"]
        != "workers/python/tests/test_agent_direct_traffic_production.py"
        or suite["exit_code"] != 0
    ):
        raise DirectTrafficProductionDrillVerificationError(
            "production drill suite evidence is invalid"
        )
    _validate_int(suite["tests_run"], "tests_run", 1, 10000)
    _validate_digest(suite["output_sha256"], "suite output")
    sources = report["sources"]
    if (
        not isinstance(sources, list)
        or len(sources) != len(_SOURCE_PATHS)
        or tuple(item.get("path") if isinstance(item, Mapping) else None for item in sources)
        != _SOURCE_PATHS
    ):
        raise DirectTrafficProductionDrillVerificationError(
            "production drill source manifest is invalid"
        )
    for item in sources:
        if not isinstance(item, Mapping) or set(item) != {"path", "sha256"}:
            raise DirectTrafficProductionDrillVerificationError(
                "production drill source entry is invalid"
            )
        _validate_digest(item["sha256"], "source")
    transitions = report["transitions"]
    if not isinstance(transitions, list) or len(transitions) != len(_TRANSITIONS):
        raise DirectTrafficProductionDrillVerificationError(
            "production drill transition set is invalid"
        )
    previous = "0" * 64
    for event, definition in zip(transitions, _TRANSITIONS, strict=True):
        validated = _validate_transition(event, definition, previous)
        previous = validated["chain_sha256"]
    if report["checks"] != sorted(_CHECKS):
        raise DirectTrafficProductionDrillVerificationError(
            "production drill checks are incomplete"
        )
    terminal = report["terminal"]
    expected_terminal = {
        "current_traffic_percent": 100,
        "revision": 7,
        "operations": 7,
        "rollout_decision": "hold",
        "rollout_reason": "maximum_stage_reached",
        "chain_sha256": previous,
    }
    if terminal != expected_terminal:
        raise DirectTrafficProductionDrillVerificationError(
            "production drill terminal state is invalid"
        )
    return report


def _validate_transition(
    value: Any,
    definition: tuple[str, int, int, int, int, str],
    previous: str,
) -> dict[str, Any]:
    fields = {
        "name",
        "current_traffic_percent",
        "target_traffic_percent",
        "expected_revision",
        "result_revision",
        "expected_operations",
        "result_operations",
        "receipt_schema_version",
        "previous_chain_sha256",
        "chain_sha256",
    }
    if not isinstance(value, Mapping) or set(value) != fields:
        raise DirectTrafficProductionDrillVerificationError(
            "production drill transition contract is invalid"
        )
    event = dict(value)
    name, current, target, expected_revision, result_revision, receipt = definition
    expected = {
        "name": name,
        "current_traffic_percent": current,
        "target_traffic_percent": target,
        "expected_revision": expected_revision,
        "result_revision": result_revision,
        "expected_operations": expected_revision,
        "result_operations": result_revision,
        "receipt_schema_version": receipt,
        "previous_chain_sha256": previous,
    }
    if any(event.get(field) != item for field, item in expected.items()):
        raise DirectTrafficProductionDrillVerificationError(
            "production drill transition identity is invalid"
        )
    _validate_digest(event["chain_sha256"], "transition chain")
    if event["chain_sha256"] != _transition_digest(event):
        raise DirectTrafficProductionDrillVerificationError(
            "production drill transition chain is invalid"
        )
    return event


def _transition_digest(event: Mapping[str, Any]) -> str:
    payload = {field: item for field, item in event.items() if field != "chain_sha256"}
    return hashlib.sha256(_CHAIN_DOMAIN + _canonical_json(payload)).hexdigest()


def _validate_attestation(value: Any) -> dict[str, Any]:
    fields = {
        "schema_version",
        "attestation_id",
        "created_at",
        "expires_at",
        "decision",
        "mode",
        "contains_live_provider_evidence",
        "report_sha256",
        "source_manifest_sha256",
        "terminal_chain_sha256",
        "key_id",
        "algorithm",
        "signature",
    }
    if not isinstance(value, Mapping) or set(value) != fields:
        raise DirectTrafficProductionDrillVerificationError(
            "production drill attestation contract is invalid"
        )
    result = dict(value)
    if any(
        (
            result["schema_version"] != ATTESTATION_SCHEMA_VERSION,
            _ATTESTATION_ID_RE.fullmatch(str(result["attestation_id"])) is None,
            result["decision"] != "pass",
            result["mode"] != MODE,
            result["contains_live_provider_evidence"] is not False,
            result["algorithm"] != "hmac-sha256",
        )
    ):
        raise DirectTrafficProductionDrillVerificationError(
            "production drill attestation identity is invalid"
        )
    for field in ("created_at", "expires_at"):
        _parse_timestamp(str(result[field]), f"drill {field}")
    _validate_key_id(result["key_id"], "production drill attestation key_id")
    for field in (
        "report_sha256",
        "source_manifest_sha256",
        "terminal_chain_sha256",
        "signature",
    ):
        _validate_digest(result[field], field)
    identity = {
        field: item
        for field, item in result.items()
        if field not in {"attestation_id", "signature"}
    }
    expected = (
        "direct-traffic-production-drill-attestation-"
        f"{_digest(_canonical_json(identity))[:32]}"
    )
    if result["attestation_id"] != expected:
        raise DirectTrafficProductionDrillVerificationError(
            "production drill attestation ID binding is invalid"
        )
    return result


def _validate_repository_root(value: Path) -> Path:
    root = value.resolve()
    if not root.is_dir() or not (root / "workers" / "python").is_dir():
        raise DirectTrafficProductionDrillConfigError(
            "production drill repository root is invalid"
        )
    return root


def _validate_timeout(value: Any) -> int:
    if isinstance(value, bool) or not isinstance(value, int) or not 1 <= value <= 900:
        raise DirectTrafficProductionDrillConfigError(
            "production drill timeout is invalid"
        )
    return value


def _validate_ttl(value: Any) -> int:
    if (
        isinstance(value, bool)
        or not isinstance(value, int)
        or not 1 <= value <= MAX_ATTESTATION_SECONDS
    ):
        raise DirectTrafficProductionDrillConfigError(
            "production drill evidence lifetime is invalid"
        )
    return value


def _validate_int(value: Any, label: str, minimum: int, maximum: int) -> int:
    if isinstance(value, bool) or not isinstance(value, int) or not minimum <= value <= maximum:
        raise DirectTrafficProductionDrillVerificationError(f"{label} is invalid")
    return value


def _validate_digest(value: Any, label: str) -> str:
    if not isinstance(value, str) or _DIGEST_RE.fullmatch(value) is None:
        raise DirectTrafficProductionDrillVerificationError(f"{label} digest is invalid")
    return value


def _sign(key: bytes, payload: Mapping[str, Any]) -> str:
    derived = hmac.new(key, _SIGNING_DOMAIN, hashlib.sha256).digest()
    return hmac.new(derived, _canonical_json(payload), hashlib.sha256).hexdigest()


def _key_from_environment() -> tuple[bytes, str]:
    raw = os.environ.get(KEY_ENV)
    key_id = os.environ.get(KEY_ID_ENV)
    if raw is None or key_id is None:
        raise DirectTrafficProductionDrillConfigError(
            f"{KEY_ENV} and {KEY_ID_ENV} are required"
        )
    configured_stage_secrets = {
        value
        for prefix in _PRODUCTION_SECRET_ENV_PREFIXES
        for suffix in _PRODUCTION_SECRET_ENV_SUFFIXES
        if (value := os.environ.get(f"{prefix}{suffix}")) is not None
    }
    if raw in configured_stage_secrets:
        raise DirectTrafficProductionDrillConfigError(
            "production drill key must be distinct from production stage credentials"
        )
    return raw.encode("utf-8"), key_id


def main(argv: Sequence[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        description="Create or verify an attested full production traffic drill"
    )
    commands = parser.add_subparsers(dest="command", required=True)
    simulate = commands.add_parser("simulate")
    simulate.add_argument("--repository-root", type=Path, required=True)
    simulate.add_argument("--output-root", type=Path, required=True)
    simulate.add_argument("--timeout-seconds", type=int, default=120)
    simulate.add_argument("--ttl-seconds", type=int, default=3600)
    verify = commands.add_parser("verify")
    verify.add_argument("--repository-root", type=Path, required=True)
    verify.add_argument("--bundle-dir", type=Path, required=True)
    verify.add_argument("--max-age-seconds", type=int, default=3600)
    args = parser.parse_args(argv)
    try:
        key, key_id = _key_from_environment()
        if args.command == "simulate":
            report = run_local_production_drill(
                args.repository_root, timeout_seconds=args.timeout_seconds
            )
            bundle = write_attested_production_drill_bundle(
                args.output_root,
                report,
                key,
                key_id,
                ttl_seconds=args.ttl_seconds,
            )
            output = {
                "decision": report["decision"],
                "mode": report["mode"],
                "bundle": str(bundle),
                "final_traffic_percent": report["terminal"][
                    "current_traffic_percent"
                ],
            }
        else:
            report = verify_attested_production_drill_bundle(
                args.bundle_dir,
                key,
                key_id,
                repository_root=args.repository_root,
                max_age_seconds=args.max_age_seconds,
            )
            output = {
                "decision": report["decision"],
                "mode": report["mode"],
                "final_traffic_percent": report["terminal"][
                    "current_traffic_percent"
                ],
            }
        print(json.dumps(output, sort_keys=True))
        return 0
    except (DirectTrafficProductionDrillError, OSError) as exc:
        print(f"production drill failed: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
