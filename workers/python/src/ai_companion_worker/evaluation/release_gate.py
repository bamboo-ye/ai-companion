from __future__ import annotations

import argparse
import fcntl
import hashlib
import json
import math
import os
import re
import stat
import subprocess
import sys
import time
from datetime import UTC, datetime, timedelta
from pathlib import Path
from typing import Any, Callable, Mapping, Sequence
from xml.etree import ElementTree

from .observability import (
    ObservabilityEvalError,
    build_report,
    capture_snapshot_from_url,
    load_baseline,
    write_junit,
    write_snapshot,
)


CANARY_SCHEMA_VERSION = "observability-canary-command-v1"
GATE_REPORT_SCHEMA_VERSION = "observability-release-gate-report-v1"
MAX_SPEC_BYTES = 16 * 1024
STRUCTURED_CANARY_SCHEMA_VERSION = "agent-observability-canary-report-v1"
STRUCTURED_CANARY_REPORT_ENV = "AGENT_OBSERVABILITY_CANARY_JSON_REPORT"
DIRECT_CANARY_SCHEMA_VERSION = "agent-direct-canary-report-v1"
DIRECT_CANARY_REPORT_ENV = "AGENT_DIRECT_CANARY_JSON_REPORT"
DEFAULT_CANARY_LEASE_ROOT = Path("artifacts/observability-eval/.canary-leases")
DEFAULT_GATE_HISTORY_LEDGER = Path("artifacts/observability-eval/history.sqlite3")
MAX_ENVIRONMENT_ID_LENGTH = 256


class ReleaseGateError(ValueError):
    pass


class CanaryLease:
    def __init__(self, descriptor: int, environment_sha256: str) -> None:
        self._descriptor = descriptor
        self.environment_sha256 = environment_sha256

    def release(self) -> None:
        descriptor = self._descriptor
        if descriptor < 0:
            return
        self._descriptor = -1
        try:
            fcntl.flock(descriptor, fcntl.LOCK_UN)
        finally:
            os.close(descriptor)


def try_acquire_canary_lease(root: Path, environment_id: str) -> CanaryLease | None:
    environment_sha256 = _environment_digest(environment_id)
    _ensure_private_lease_directory(root)
    path = root / f"{environment_sha256}.lock"
    flags = os.O_RDWR | os.O_CREAT | getattr(os, "O_NOFOLLOW", 0)
    try:
        descriptor = os.open(path, flags, 0o600)
    except OSError as exc:
        raise ReleaseGateError(f"cannot open canary lease: {exc}") from exc
    acquired = False
    try:
        metadata = os.fstat(descriptor)
        if (
            not stat.S_ISREG(metadata.st_mode)
            or stat.S_IMODE(metadata.st_mode) != 0o600
            or metadata.st_nlink != 1
            or (hasattr(os, "getuid") and metadata.st_uid != os.getuid())
        ):
            raise ReleaseGateError("canary lease file is unsafe")
        try:
            fcntl.flock(descriptor, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            return None
        acquired = True
        payload = json.dumps(
            {
                "schema_version": "observability-canary-lease-v1",
                "environment_sha256": environment_sha256,
                "acquired_at": datetime.now(UTC).isoformat().replace("+00:00", "Z"),
                "pid": os.getpid(),
            },
            sort_keys=True,
            separators=(",", ":"),
        ).encode("utf-8")
        os.ftruncate(descriptor, 0)
        os.write(descriptor, payload)
        os.fsync(descriptor)
        return CanaryLease(descriptor, environment_sha256)
    except OSError as exc:
        if acquired:
            try:
                fcntl.flock(descriptor, fcntl.LOCK_UN)
            finally:
                os.close(descriptor)
        raise ReleaseGateError(f"cannot initialize canary lease: {exc}") from exc
    finally:
        if not acquired:
            os.close(descriptor)


def _environment_digest(environment_id: str) -> str:
    if (
        not isinstance(environment_id, str)
        or not environment_id
        or len(environment_id) > MAX_ENVIRONMENT_ID_LENGTH
        or environment_id.strip() != environment_id
        or any(ord(character) < 32 for character in environment_id)
    ):
        raise ReleaseGateError("canary environment ID is invalid")
    return hashlib.sha256(
        b"ai-companion/observability-canary-environment/v1\0"
        + environment_id.encode("utf-8")
    ).hexdigest()


def _ensure_private_lease_directory(path: Path) -> None:
    try:
        path.mkdir(mode=0o700, parents=True, exist_ok=False)
    except FileExistsError:
        pass
    except OSError as exc:
        raise ReleaseGateError(f"cannot create canary lease directory: {exc}") from exc
    try:
        metadata = path.lstat()
    except OSError as exc:
        raise ReleaseGateError(f"cannot inspect canary lease directory: {exc}") from exc
    if (
        not stat.S_ISDIR(metadata.st_mode)
        or path.is_symlink()
        or stat.S_IMODE(metadata.st_mode) != 0o700
        or (hasattr(os, "getuid") and metadata.st_uid != os.getuid())
    ):
        raise ReleaseGateError("canary lease directory is unsafe")


def load_canary_spec(path: Path) -> tuple[dict[str, Any], str]:
    try:
        raw = path.read_bytes()
    except OSError as exc:
        raise ReleaseGateError(f"cannot read canary spec: {exc}") from exc
    if len(raw) > MAX_SPEC_BYTES:
        raise ReleaseGateError("canary spec exceeds size limit")
    try:
        value = json.loads(raw, object_pairs_hook=_reject_duplicate_keys)
    except (UnicodeDecodeError, json.JSONDecodeError, ValueError) as exc:
        raise ReleaseGateError(f"invalid canary spec: {exc}") from exc
    if not isinstance(value, Mapping):
        raise ReleaseGateError("canary spec must be an object")
    if set(value) != {
        "schema_version",
        "name",
        "command",
        "timeout_seconds",
        "stabilization_seconds",
    }:
        raise ReleaseGateError("canary spec fields are invalid")
    if value.get("schema_version") != CANARY_SCHEMA_VERSION:
        raise ReleaseGateError("unsupported canary spec schema_version")
    name = value.get("name")
    if not isinstance(name, str) or re.fullmatch(r"[a-z0-9][a-z0-9_-]{0,63}", name) is None:
        raise ReleaseGateError("canary spec name is invalid")
    command_value = value.get("command")
    if not isinstance(command_value, list) or not 1 <= len(command_value) <= 32:
        raise ReleaseGateError("canary command must contain 1 to 32 arguments")
    command: list[str] = []
    for argument in command_value:
        if not isinstance(argument, str) or not argument or len(argument) > 1024 or "\x00" in argument:
            raise ReleaseGateError("canary command argument is invalid")
        command.append(argument)
    executable = Path(command[0]).name.lower()
    if executable in {"sh", "bash", "zsh", "dash", "ksh"} and len(command) > 1:
        if command[1] in {"-c", "-lc"}:
            raise ReleaseGateError("canary command must not use a shell command string")
    timeout = _bounded_seconds(value.get("timeout_seconds"), "timeout_seconds", 0, 1800)
    if timeout <= 0:
        raise ReleaseGateError("canary timeout_seconds must be positive")
    stabilization = _bounded_seconds(
        value.get("stabilization_seconds"), "stabilization_seconds", 0, 300
    )
    spec = {
        "schema_version": CANARY_SCHEMA_VERSION,
        "name": name,
        "command": command,
        "timeout_seconds": timeout,
        "stabilization_seconds": stabilization,
    }
    return spec, hashlib.sha256(raw).hexdigest()


def run_gate(
    metrics_url: str,
    baseline_path: Path,
    canary_spec_path: Path,
    output_dir: Path,
    capture: Callable[[str], dict[str, Any]] | None = None,
    *,
    lease_root: Path | None = None,
    environment_id: str | None = None,
    preacquired_lease: CanaryLease | None = None,
    lease_already_checked: bool = False,
) -> tuple[dict[str, Any], int]:
    baseline = load_baseline(baseline_path)
    spec, spec_digest = load_canary_spec(canary_spec_path)
    _create_private_directory(output_dir)
    phases = {
        "before_snapshot": "pending",
        "canary": "pending",
        "stabilization": "pending",
        "after_snapshot": "pending",
        "evaluation": "pending",
    }
    capture_snapshot = capture or capture_snapshot_from_url

    try:
        before = capture_snapshot(metrics_url)
        write_snapshot(output_dir / "before.json", before)
        phases["before_snapshot"] = "passed"
    except ObservabilityEvalError:
        phases["before_snapshot"] = "failed"
        report = _gate_report(
            spec,
            spec_digest,
            phases,
            "not_run",
            None,
            0,
            [_gate_violation("before_capture_failed", "before metrics snapshot is required", None)],
        )
        _write_gate_artifacts(output_dir, report)
        return report, 2

    canary_started = time.monotonic()
    canary_started_at = datetime.now(UTC)
    canary_status = "passed"
    canary_exit_code: int | None = 0
    structured_report_path: Path | None = None
    structured_violation: dict[str, Any] | None = None
    structured_canary: dict[str, Any] | None = None
    if preacquired_lease is not None and lease_root is None:
        raise ReleaseGateError("pre-acquired Canary lease requires its lease root")
    if (
        preacquired_lease is not None
        and preacquired_lease.environment_sha256
        != _environment_digest(environment_id or metrics_url)
    ):
        raise ReleaseGateError("pre-acquired Canary lease environment is invalid")
    lease: CanaryLease | None = preacquired_lease
    release_lease = preacquired_lease is None
    if lease_root is not None:
        try:
            if lease is None and not lease_already_checked:
                lease = try_acquire_canary_lease(
                    lease_root,
                    environment_id or metrics_url,
                )
            if lease is None:
                canary_status = "failed"
                canary_exit_code = None
                structured_violation = _gate_violation(
                    "canary_concurrent_run",
                    "only one release canary may run in an environment",
                    {
                        "environment_sha256": _environment_digest(
                            environment_id or metrics_url
                        ),
                        "lease_backend": "file-v1",
                    },
                )
        except ReleaseGateError as exc:
            canary_status = "failed"
            canary_exit_code = None
            structured_violation = _gate_violation(
                "canary_lease_invalid",
                "a safe environment-scoped canary lease is required",
                str(exc),
            )
    try:
        if canary_status == "passed":
            try:
                structured_report_path = _structured_canary_report_path(spec)
            except ReleaseGateError as exc:
                canary_status = "failed"
                canary_exit_code = None
                structured_violation = _gate_violation(
                    "canary_report_invalid",
                    "safe structured canary report path is required",
                    str(exc),
                )
        if canary_status == "passed" and structured_report_path is not None:
            try:
                structured_report_path.unlink(missing_ok=True)
            except OSError as exc:
                canary_status = "failed"
                canary_exit_code = None
                structured_violation = _gate_violation(
                    "canary_report_invalid",
                    "stale structured canary report must be removable",
                    str(exc),
                )
        try:
            if canary_status == "passed":
                completed = subprocess.run(
                    spec["command"],
                    check=False,
                    timeout=float(spec["timeout_seconds"]),
                )
                canary_exit_code = completed.returncode
                if completed.returncode != 0:
                    canary_status = "failed"
        except subprocess.TimeoutExpired:
            canary_status = "timed_out"
            canary_exit_code = None
        except OSError:
            canary_status = "failed"
            canary_exit_code = None
        if structured_report_path is not None:
            try:
                structured_canary = _load_structured_canary_report(
                    structured_report_path,
                    canary_started_at=canary_started_at,
                )
                if canary_status == "passed" and structured_canary["decision"] != "pass":
                    canary_status = "failed"
                    structured_violation = _gate_violation(
                        "canary_report_failed",
                        "structured canary report decision must be pass",
                        structured_canary["decision"],
                    )
            except ReleaseGateError as exc:
                if canary_status != "timed_out":
                    canary_status = "failed"
                structured_violation = _gate_violation(
                    "canary_report_invalid",
                    "fresh structured canary report is required",
                    str(exc),
                )
        if structured_canary is not None:
            _write_private_bytes(
                output_dir / "canary-report.json",
                structured_canary["report_bytes"],
            )
    finally:
        if release_lease and lease is not None:
            lease.release()
    canary_duration_ms = round((time.monotonic() - canary_started) * 1000)
    phases["canary"] = canary_status

    stabilization = float(spec["stabilization_seconds"])
    if stabilization > 0:
        time.sleep(stabilization)
    phases["stabilization"] = "passed"

    try:
        after = capture_snapshot(metrics_url)
        write_snapshot(output_dir / "after.json", after)
        phases["after_snapshot"] = "passed"
    except ObservabilityEvalError:
        phases["after_snapshot"] = "failed"
        violations = _canary_violations(canary_status, canary_exit_code, structured_violation)
        violations.append(
            _gate_violation("after_capture_failed", "after metrics snapshot is required", None)
        )
        report = _gate_report(
            spec,
            spec_digest,
            phases,
            canary_status,
            canary_exit_code,
            canary_duration_ms,
            violations,
            structured_canary=structured_canary,
        )
        _write_gate_artifacts(output_dir, report)
        return report, 1

    metrics_report, metrics_violations = build_report(before, after, baseline)
    phases["evaluation"] = "failed" if metrics_violations else "passed"
    _write_private_json(output_dir / "metrics-report.json", metrics_report)
    write_junit(output_dir / "metrics-report.xml", metrics_report)

    violations = _canary_violations(canary_status, canary_exit_code, structured_violation)
    violations.extend(metrics_violations)
    report = _gate_report(
        spec,
        spec_digest,
        phases,
        canary_status,
        canary_exit_code,
        canary_duration_ms,
        violations,
        metrics_report,
        structured_canary,
    )
    _write_gate_artifacts(output_dir, report)
    return report, 1 if violations else 0


def write_gate_junit(path: Path, report: Mapping[str, Any]) -> None:
    violations_value = report.get("violations")
    violations = violations_value if isinstance(violations_value, list) else []
    canary_failures = [
        item for item in violations if isinstance(item, Mapping) and str(item.get("code", "")).startswith("canary_")
    ]
    metrics_failures = [item for item in violations if item not in canary_failures]
    suite = ElementTree.Element(
        "testsuite",
        name="observability-release-gate",
        tests="2",
        failures=str(int(bool(canary_failures)) + int(bool(metrics_failures))),
    )
    for name, failures in (("canary", canary_failures), ("metrics", metrics_failures)):
        case = ElementTree.SubElement(suite, "testcase", name=name)
        if failures:
            failure = ElementTree.SubElement(case, "failure", message=f"{name} gate failed")
            failure.text = json.dumps(failures, ensure_ascii=False)
    output = ElementTree.SubElement(suite, "system-out")
    output.text = json.dumps(
        {"decision": report.get("decision"), "phases": report.get("phases")},
        ensure_ascii=False,
        sort_keys=True,
    )
    path.parent.mkdir(parents=True, exist_ok=True)
    ElementTree.ElementTree(suite).write(path, encoding="utf-8", xml_declaration=True)
    path.chmod(0o600)


def main(argv: Sequence[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Run an auditable observability release gate")
    parser.add_argument("--metrics-url", required=True)
    parser.add_argument("--baseline", type=Path, required=True)
    parser.add_argument("--canary-spec", type=Path, required=True)
    parser.add_argument("--output-root", type=Path, required=True)
    parser.add_argument("--canary-lease-root", type=Path, default=DEFAULT_CANARY_LEASE_ROOT)
    parser.add_argument("--history-ledger", type=Path, default=DEFAULT_GATE_HISTORY_LEDGER)
    parser.add_argument(
        "--environment-id",
        help="Non-secret stable environment identity; defaults to the metrics URL",
    )
    args = parser.parse_args(argv)
    lease: CanaryLease | None = None
    try:
        from .release_gate_history import ReleaseGateHistory

        stable_environment = args.environment_id or args.metrics_url
        history = ReleaseGateHistory(
            args.history_ledger,
            stable_environment,
        )
        lease = try_acquire_canary_lease(
            args.canary_lease_root,
            stable_environment,
        )
        output_dir = _new_run_directory(args.output_root)
        report, result = run_gate(
            args.metrics_url,
            args.baseline,
            args.canary_spec,
            output_dir,
            lease_root=args.canary_lease_root,
            environment_id=args.environment_id,
            preacquired_lease=lease,
            lease_already_checked=True,
        )
        history.record(output_dir, report)
    except (OSError, ObservabilityEvalError, ReleaseGateError, ValueError) as exc:
        print(f"observability release gate error: {exc}", file=sys.stderr)
        return 2
    finally:
        if lease is not None:
            lease.release()
    print(
        json.dumps(
            {
                "decision": report["decision"],
                "output_dir": str(output_dir),
                "violations": len(report["violations"]),
            },
            sort_keys=True,
        )
    )
    return result


def _gate_report(
    spec: Mapping[str, Any],
    spec_digest: str,
    phases: Mapping[str, str],
    canary_status: str,
    canary_exit_code: int | None,
    canary_duration_ms: int,
    violations: Sequence[Mapping[str, Any]],
    metrics_report: Mapping[str, Any] | None = None,
    structured_canary: Mapping[str, Any] | None = None,
) -> dict[str, Any]:
    source: dict[str, Any] = {
        "canary_spec_sha256": spec_digest,
        "command_sha256": hashlib.sha256(
            json.dumps(spec["command"], ensure_ascii=False, separators=(",", ":")).encode()
        ).hexdigest(),
    }
    if metrics_report is not None:
        metrics_source = metrics_report.get("source")
        if isinstance(metrics_source, Mapping):
            source["before_sha256"] = metrics_source.get("before_sha256")
            source["after_sha256"] = metrics_source.get("after_sha256")
        regression = metrics_report.get("regression")
        if isinstance(regression, Mapping):
            source["baseline_version"] = regression.get("baseline_version")
    if structured_canary is not None:
        source["structured_canary_report_sha256"] = structured_canary["report_sha256"]
    else:
        source["structured_canary_report_sha256"] = None
    return {
        "schema_version": GATE_REPORT_SCHEMA_VERSION,
        "generated_at": datetime.now(UTC).isoformat().replace("+00:00", "Z"),
        "decision": "rollback" if violations else "promote",
        "source": source,
        "canary": {
            "name": spec["name"],
            "status": canary_status,
            "exit_code": canary_exit_code,
            "duration_ms": canary_duration_ms,
            "report": None
            if structured_canary is None
            else {
                "schema_version": structured_canary["schema_version"],
                "decision": structured_canary["decision"],
                "baseline_version": structured_canary["baseline_version"],
                "summary": structured_canary["summary"],
            },
        },
        "phases": dict(phases),
        "violations": [dict(item) for item in violations],
    }


def _canary_violations(
    status: str,
    exit_code: int | None,
    structured_violation: Mapping[str, Any] | None = None,
) -> list[dict[str, Any]]:
    if status == "timed_out":
        return [_gate_violation("canary_timed_out", "canary must finish before timeout", None)]
    if structured_violation is not None:
        return [dict(structured_violation)]
    if status == "passed":
        return []
    return [_gate_violation("canary_failed", "canary exit code must be 0", exit_code)]


def _gate_violation(code: str, expected: str, actual: Any) -> dict[str, Any]:
    return {
        "code": code,
        "severity": "hard",
        "path": "/canary" if code.startswith("canary_") else "/phases",
        "expected": expected,
        "actual": actual,
    }


def _structured_canary_report_path(spec: Mapping[str, Any]) -> Path | None:
    name = spec.get("name")
    if name not in {"agent-observability", "agent-direct"}:
        return None
    environment = (
        STRUCTURED_CANARY_REPORT_ENV
        if name == "agent-observability"
        else DIRECT_CANARY_REPORT_ENV
    )
    default_path = (
        "artifacts/agent-eval/observability-canary.json"
        if name == "agent-observability"
        else "artifacts/agent-eval/direct-canary.json"
    )
    value = os.environ.get(
        environment,
        default_path,
    )
    path = Path(value)
    if (
        not value.strip()
        or path.is_absolute()
        or ".." in path.parts
        or len(path.parts) < 3
        or path.parts[:2] != ("artifacts", "agent-eval")
        or path.suffix != ".json"
    ):
        raise ReleaseGateError("structured canary report path is invalid")
    for parent in (Path("artifacts"), Path("artifacts") / "agent-eval"):
        try:
            metadata = parent.lstat()
        except FileNotFoundError:
            continue
        except OSError as exc:
            raise ReleaseGateError(f"cannot inspect structured canary directory: {exc}") from exc
        if not stat.S_ISDIR(metadata.st_mode) or parent.is_symlink():
            raise ReleaseGateError("structured canary report directory is unsafe")
    return path


def _load_structured_canary_report(path: Path, *, canary_started_at: datetime) -> dict[str, Any]:
    try:
        metadata = path.lstat()
    except OSError as exc:
        raise ReleaseGateError(f"cannot inspect structured canary report: {exc}") from exc
    if (
        not stat.S_ISREG(metadata.st_mode)
        or path.is_symlink()
        or stat.S_IMODE(metadata.st_mode) != 0o600
        or metadata.st_nlink != 1
        or (hasattr(os, "getuid") and metadata.st_uid != os.getuid())
        or metadata.st_size <= 0
        or metadata.st_size > MAX_SPEC_BYTES
    ):
        raise ReleaseGateError("structured canary report file is unsafe")
    try:
        raw = path.read_bytes()
    except OSError as exc:
        raise ReleaseGateError(f"cannot read structured canary report: {exc}") from exc
    if len(raw) == 0 or len(raw) > MAX_SPEC_BYTES:
        raise ReleaseGateError("structured canary report size is invalid")
    try:
        value = json.loads(raw, object_pairs_hook=_reject_duplicate_keys)
    except (UnicodeDecodeError, json.JSONDecodeError, ValueError) as exc:
        raise ReleaseGateError(f"invalid structured canary report: {exc}") from exc
    if not isinstance(value, Mapping):
        raise ReleaseGateError("structured canary report must be an object")
    schema_version = value.get("schema_version")
    if schema_version == DIRECT_CANARY_SCHEMA_VERSION:
        return _load_direct_canary_report(
            value,
            raw,
            canary_started_at=canary_started_at,
        )
    if set(value) != {
        "schema_version", "generated_at", "decision", "source", "summary", "violations"
    }:
        raise ReleaseGateError("structured canary report fields are invalid")
    if value.get("schema_version") != STRUCTURED_CANARY_SCHEMA_VERSION:
        raise ReleaseGateError("structured canary report schema_version is invalid")
    generated_at_value = value.get("generated_at")
    try:
        generated_at = datetime.fromisoformat(str(generated_at_value).replace("Z", "+00:00"))
    except ValueError as exc:
        raise ReleaseGateError("structured canary report generated_at is invalid") from exc
    if generated_at.tzinfo is None:
        raise ReleaseGateError("structured canary report generated_at is invalid")
    generated_at = generated_at.astimezone(UTC)
    if (
        generated_at < canary_started_at - timedelta(seconds=1)
        or generated_at > datetime.now(UTC) + timedelta(seconds=30)
    ):
        raise ReleaseGateError("structured canary report is stale or from the future")
    source = value.get("source")
    if not isinstance(source, Mapping) or set(source) != {"baseline_version", "baseline_sha256"}:
        raise ReleaseGateError("structured canary report source is invalid")
    digest = source.get("baseline_sha256")
    if (
        source.get("baseline_version") != "agent-observability-canary-baseline-v1"
        or not isinstance(digest, str)
        or re.fullmatch(r"[0-9a-f]{64}", digest) is None
    ):
        raise ReleaseGateError("structured canary baseline digest is invalid")
    summary = value.get("summary")
    summary_fields = {
        "cycles", "dispatch_hints", "not_claimed", "terminal_wakes",
        "terminal_no_matches", "terminal_errors", "wake_latency_max_seconds",
        "python_executions", "model_calls", "duration_ms",
    }
    if not isinstance(summary, Mapping) or set(summary) != summary_fields:
        raise ReleaseGateError("structured canary summary is invalid")
    integer_fields = summary_fields - {"wake_latency_max_seconds"}
    if any(
        not isinstance(summary[field], int) or isinstance(summary[field], bool) or summary[field] < 0
        for field in integer_fields
    ):
        raise ReleaseGateError("structured canary summary integer is invalid")
    latency = summary.get("wake_latency_max_seconds")
    if (
        not isinstance(latency, (int, float))
        or isinstance(latency, bool)
        or not math.isfinite(float(latency))
        or latency < 0
    ):
        raise ReleaseGateError("structured canary latency is invalid")
    violations = value.get("violations")
    decision = value.get("decision")
    if decision not in {"pass", "fail"} or not isinstance(violations, list):
        raise ReleaseGateError("structured canary decision is invalid")
    if any(
        not isinstance(item, Mapping)
        or set(item) != {"code", "path", "expected", "actual"}
        or not isinstance(item.get("code"), str)
        or not isinstance(item.get("path"), str)
        or not isinstance(item.get("expected"), str)
        for item in violations
    ):
        raise ReleaseGateError("structured canary violations are invalid")
    if (decision == "pass" and violations) or (decision == "fail" and not violations):
        raise ReleaseGateError("structured canary decision and violations are inconsistent")
    if decision == "pass" and not (
        5 <= summary["cycles"] <= 100
        and summary["dispatch_hints"] >= summary["cycles"]
        and summary["not_claimed"] == summary["cycles"]
        and summary["terminal_wakes"] == summary["cycles"] * 3
        and summary["terminal_no_matches"] == summary["cycles"] * 3
        and summary["terminal_errors"] == 0
        and float(summary["wake_latency_max_seconds"]) <= 1
        and summary["python_executions"] == 0
        and summary["model_calls"] == 0
        and summary["duration_ms"] <= 60_000
    ):
        raise ReleaseGateError("structured canary pass summary violates the v1 baseline")
    return {
        "schema_version": STRUCTURED_CANARY_SCHEMA_VERSION,
        "decision": decision,
        "baseline_version": source.get("baseline_version"),
        "summary": dict(summary),
        "report_sha256": hashlib.sha256(raw).hexdigest(),
        "report_bytes": raw,
    }


def _load_direct_canary_report(
    value: Mapping[str, Any],
    raw: bytes,
    *,
    canary_started_at: datetime,
) -> dict[str, Any]:
    from .direct_canary import DirectCanaryError, validate_report

    try:
        report = validate_report(
            value,
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
        raise ReleaseGateError(f"invalid direct canary report: {exc}") from exc
    generated_at_value = report.get("generated_at")
    try:
        generated_at = datetime.fromisoformat(str(generated_at_value).replace("Z", "+00:00"))
    except ValueError as exc:
        raise ReleaseGateError("direct canary report generated_at is invalid") from exc
    if generated_at.tzinfo is None:
        raise ReleaseGateError("direct canary report generated_at is invalid")
    generated_at = generated_at.astimezone(UTC)
    if (
        generated_at < canary_started_at - timedelta(seconds=1)
        or generated_at > datetime.now(UTC) + timedelta(seconds=30)
    ):
        raise ReleaseGateError("direct canary report is stale or from the future")
    source = report["source"]
    return {
        "schema_version": DIRECT_CANARY_SCHEMA_VERSION,
        "decision": report["decision"],
        "baseline_version": source["baseline_version"],
        "summary": report["summary"],
        "report_sha256": hashlib.sha256(raw).hexdigest(),
        "report_bytes": raw,
    }


def _bounded_seconds(value: Any, field: str, minimum: float, maximum: float) -> float:
    if not isinstance(value, (int, float)) or isinstance(value, bool):
        raise ReleaseGateError(f"canary {field} must be a number")
    result = float(value)
    if not minimum <= result <= maximum:
        raise ReleaseGateError(f"canary {field} is outside the allowed range")
    return result


def _reject_duplicate_keys(pairs: list[tuple[str, Any]]) -> dict[str, Any]:
    result: dict[str, Any] = {}
    for key, value in pairs:
        if key in result:
            raise ValueError(f"duplicate JSON key: {key}")
        result[key] = value
    return result


def _new_run_directory(root: Path) -> Path:
    root.mkdir(parents=True, exist_ok=True)
    root.chmod(0o700)
    timestamp = datetime.now(UTC).strftime("%Y%m%dT%H%M%S%fZ")
    path = root / f"release-gate-{timestamp}-{os.getpid()}"
    return path


def _create_private_directory(path: Path) -> None:
    path.mkdir(parents=True, exist_ok=False)
    path.chmod(0o700)


def _write_gate_artifacts(output_dir: Path, report: Mapping[str, Any]) -> None:
    _write_private_json(output_dir / "gate-report.json", report)
    write_gate_junit(output_dir / "gate-report.xml", report)


def _write_private_json(path: Path, value: Mapping[str, Any]) -> None:
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    path.chmod(0o600)


def _write_private_bytes(path: Path, value: bytes) -> None:
    path.write_bytes(value)
    path.chmod(0o600)


if __name__ == "__main__":
    raise SystemExit(main())
