from __future__ import annotations

import json
import os
import sys
import tempfile
import unittest
from datetime import UTC, datetime, timedelta
from pathlib import Path
from typing import Any

from ai_companion_worker.evaluation.direct_canary import (
    build_report as build_direct_report,
    evaluate_case,
    load_baseline as load_direct_baseline,
    load_suite as load_direct_suite,
)
from ai_companion_worker.evaluation.observability import build_snapshot
from ai_companion_worker.evaluation.release_attestation import (
    ATTESTATION_FILE,
    AttestationConfigError,
    AttestationVerificationError,
    create_attestation,
    verify_attestation,
)
from ai_companion_worker.evaluation.release_gate import run_gate, try_acquire_canary_lease


ROOT = Path(__file__).resolve().parents[3]
BASELINE = ROOT / "evals" / "observability" / "baselines" / "release.v1.json"
DIRECT_BASELINE = ROOT / "evals" / "agent" / "baselines" / "direct-canary.v1.json"
DIRECT_SUITE = ROOT / "evals" / "agent" / "suites" / "direct-canary.v1.json"
KEY = b"release-attestation-test-key-32-bytes-minimum"
OTHER_KEY = b"different-release-attestation-key-32-bytes"
KEY_ID = "test-key-v1"


def metrics_text(queue_lag: int = 0) -> str:
    return "\n".join(
        [
            "ai_companion_degradation_level 0",
            f"ai_companion_queue_lag {queue_lag}",
            "ai_companion_oldest_job_age_seconds 0",
            "ai_companion_model_error_ratio 0",
            "ai_companion_model_latency_p95_seconds 0",
            "ai_companion_agent_run_duration_p95_seconds 0",
            'ai_companion_agent_runs{status="completed"} 0',
            'ai_companion_agent_runs{status="failed"} 0',
            'ai_companion_agent_runs{status="timed_out"} 0',
            'ai_companion_agent_execution_retries_recent{outcome="scheduled"} 0',
            'ai_companion_agent_execution_retries_recent{outcome="recovered"} 0',
            'ai_companion_agent_execution_retries_recent{outcome="exhausted"} 0',
            'ai_companion_agent_execution_retries_recent{outcome="deadline_exhausted"} 0',
            "ai_companion_agent_execution_retry_recovery_ratio 0",
            "",
        ]
    )


class SnapshotSequence:
    def __init__(self, queues: list[int]) -> None:
        self.queues = queues
        self.requests = 0

    def __call__(self, _url: str) -> dict[str, Any]:
        index = min(self.requests, len(self.queues) - 1)
        self.requests += 1
        return build_snapshot(metrics_text(self.queues[index]))


def make_bundle(root: Path, after_queue: int = 0) -> tuple[Path, dict[str, Any]]:
    spec = root / "canary.json"
    spec.write_text(
        json.dumps(
            {
                "schema_version": "observability-canary-command-v1",
                "name": "attestation-test",
                "command": [sys.executable, "-c", "pass"],
                "timeout_seconds": 2,
                "stabilization_seconds": 0,
            }
        ),
        encoding="utf-8",
    )
    gate_dir = root / "release-gate-test"
    report, _result = run_gate(
        "http://metrics.invalid/metrics",
        BASELINE,
        spec,
        gate_dir,
        capture=SnapshotSequence([0, after_queue]),
    )
    return gate_dir, report


def make_structured_bundle(root: Path) -> tuple[Path, dict[str, Any]]:
    report_path = root / "artifacts" / "agent-eval" / "structured-canary.json"
    structured = {
        "schema_version": "agent-observability-canary-report-v1",
        "generated_at": datetime.now(UTC).isoformat().replace("+00:00", "Z"),
        "decision": "pass",
        "source": {
            "baseline_version": "agent-observability-canary-baseline-v1",
            "baseline_sha256": "a" * 64,
        },
        "summary": {
            "cycles": 5, "dispatch_hints": 5, "not_claimed": 5,
            "terminal_wakes": 15, "terminal_no_matches": 15, "terminal_errors": 0,
            "wake_latency_max_seconds": 0.1, "python_executions": 0,
            "model_calls": 0, "duration_ms": 1000,
        },
        "violations": [],
    }
    spec = root / "structured-canary-spec.json"
    spec.write_text(json.dumps({
        "schema_version": "observability-canary-command-v1",
        "name": "agent-observability",
        "command": [sys.executable, "-c", "from pathlib import Path; import os, sys; p=Path(sys.argv[1]); p.parent.mkdir(parents=True, exist_ok=True); p.write_text(sys.argv[2]); os.chmod(p, 0o600)", str(report_path), json.dumps(structured)],
        "timeout_seconds": 2,
        "stabilization_seconds": 0,
    }), encoding="utf-8")
    gate_dir = root / "release-gate-structured-test"
    previous = os.environ.get("AGENT_OBSERVABILITY_CANARY_JSON_REPORT")
    previous_cwd = Path.cwd()
    os.chdir(root)
    os.environ["AGENT_OBSERVABILITY_CANARY_JSON_REPORT"] = "artifacts/agent-eval/structured-canary.json"
    try:
        report, _result = run_gate(
            "http://metrics.invalid/metrics", BASELINE, spec, gate_dir,
            capture=SnapshotSequence([0, 0]),
        )
    finally:
        os.chdir(previous_cwd)
        if previous is None:
            os.environ.pop("AGENT_OBSERVABILITY_CANARY_JSON_REPORT", None)
        else:
            os.environ["AGENT_OBSERVABILITY_CANARY_JSON_REPORT"] = previous
    return gate_dir, report


def direct_structured_report() -> dict[str, Any]:
    baseline, baseline_digest = load_direct_baseline(DIRECT_BASELINE)
    suite, suite_digest = load_direct_suite(DIRECT_SUITE)
    responses = [
        "缓存能减少重复计算并提高访问速度。",
        "接口幂等性保证同一请求执行多次仍得到一致结果。",
        "数据有序才能根据中间值判断并排除一半查找范围。",
        "事务回滚用于撤销未完成操作并恢复数据库一致状态。",
        "网络重试采用退避可以降低请求频率并避免加重拥塞。",
    ]
    results = []
    for case, response in zip(suite["cases"], responses, strict=True):
        payload = {
            "status": "completed",
            "output": {
                "outcome": "completed",
                "execution_mode": "direct",
                "response": response,
                "response_validation": {
                    "passed": True,
                    "repairs": [],
                    "rewrite_attempt": 0,
                },
                "budget": {"usage": {"model_calls": 1}},
                "model": {"calls": [{"status": "success"}]},
                "observability": {
                    "node_trace": [
                        {"node": "supervisor"},
                        {"node": "companion"},
                        {"node": "response_quality_gate"},
                    ]
                },
            },
        }
        results.append(evaluate_case(case, payload, 1000))
    report, violations = build_direct_report(
        results,
        baseline=baseline,
        baseline_sha256=baseline_digest,
        suite=suite,
        suite_sha256=suite_digest,
    )
    assert not violations
    return report


def make_direct_structured_bundle(root: Path) -> tuple[Path, dict[str, Any]]:
    report_path = root / "artifacts" / "agent-eval" / "direct-canary.json"
    payload_path = root / "direct-canary-payload.json"
    payload_path.write_text(json.dumps(direct_structured_report()), encoding="utf-8")
    spec = root / "direct-canary-spec.json"
    spec.write_text(
        json.dumps(
            {
                "schema_version": "observability-canary-command-v1",
                "name": "agent-direct",
                "command": [
                    sys.executable,
                    "-c",
                    (
                        "from pathlib import Path; import os, sys; "
                        "p=Path(sys.argv[1]); p.parent.mkdir(parents=True, exist_ok=True); "
                        "p.write_bytes(Path(sys.argv[2]).read_bytes()); os.chmod(p, 0o600)"
                    ),
                    str(report_path),
                    str(payload_path),
                ],
                "timeout_seconds": 2,
                "stabilization_seconds": 0,
            }
        ),
        encoding="utf-8",
    )
    gate_dir = root / "release-gate-direct-structured-test"
    environment = {
        "AGENT_DIRECT_CANARY_JSON_REPORT": "artifacts/agent-eval/direct-canary.json",
        "AGENT_DIRECT_CANARY_BASELINE": str(DIRECT_BASELINE),
        "AGENT_DIRECT_CANARY_SUITE": str(DIRECT_SUITE),
    }
    previous = {key: os.environ.get(key) for key in environment}
    previous_cwd = Path.cwd()
    os.chdir(root)
    os.environ.update(environment)
    try:
        report, _result = run_gate(
            "http://metrics.invalid/metrics",
            BASELINE,
            spec,
            gate_dir,
            capture=SnapshotSequence([0, 0]),
        )
    finally:
        os.chdir(previous_cwd)
        for key, value in previous.items():
            if value is None:
                os.environ.pop(key, None)
            else:
                os.environ[key] = value
    return gate_dir, report


def make_concurrent_rollback_bundle(root: Path) -> tuple[Path, dict[str, Any]]:
    spec = root / "concurrent-canary-spec.json"
    spec.write_text(json.dumps({
        "schema_version": "observability-canary-command-v1",
        "name": "agent-observability",
        "command": [sys.executable, "-c", "raise SystemExit('must not execute')"],
        "timeout_seconds": 2,
        "stabilization_seconds": 0,
    }), encoding="utf-8")
    lease_root = root / "leases"
    lease = try_acquire_canary_lease(lease_root, "staging-cn")
    assert lease is not None
    gate_dir = root / "release-gate-concurrent-test"
    try:
        report, _result = run_gate(
            "http://metrics.invalid/metrics",
            BASELINE,
            spec,
            gate_dir,
            capture=SnapshotSequence([0, 0]),
            lease_root=lease_root,
            environment_id="staging-cn",
        )
    finally:
        lease.release()
    return gate_dir, report


def report_time(report: dict[str, Any]) -> datetime:
    return datetime.fromisoformat(str(report["generated_at"]).replace("Z", "+00:00")).astimezone(
        UTC
    )


class ObservabilityReleaseAttestationTest(unittest.TestCase):
    def test_promote_bundle_can_be_signed_and_verified(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            gate_dir, report = make_bundle(Path(directory))
            now = report_time(report) + timedelta(seconds=1)

            attestation = create_attestation(gate_dir, KEY, KEY_ID, now=now)
            verified = verify_attestation(
                gate_dir,
                KEY,
                KEY_ID,
                expected_run_id=gate_dir.name,
                now=now + timedelta(seconds=1),
            )

            self.assertEqual(attestation, verified)
            self.assertEqual(verified["decision"], "promote")
            self.assertEqual(len(verified["artifacts"]), 6)
            self.assertEqual((gate_dir / ATTESTATION_FILE).stat().st_mode & 0o777, 0o600)
            self.assertEqual(gate_dir.stat().st_mode & 0o777, 0o700)
            with self.assertRaisesRegex(AttestationVerificationError, "already exists"):
                create_attestation(gate_dir, KEY, KEY_ID, now=now)

    def test_structured_canary_bundle_is_signed_and_tamper_evident(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            gate_dir, report = make_structured_bundle(Path(directory))
            now = report_time(report) + timedelta(seconds=1)
            attestation = create_attestation(gate_dir, KEY, KEY_ID, now=now)
            verified = verify_attestation(gate_dir, KEY, KEY_ID, now=now)
            self.assertEqual(len(verified["artifacts"]), 7)
            self.assertEqual(attestation["decision"], "promote")

            report_path = gate_dir / "canary-report.json"
            report_path.write_bytes(report_path.read_bytes() + b" ")
            with self.assertRaisesRegex(AttestationVerificationError, "manifest does not match"):
                verify_attestation(gate_dir, KEY, KEY_ID, now=now)

    def test_direct_quality_bundle_is_recomputed_signed_and_verified(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            gate_dir, report = make_direct_structured_bundle(Path(directory))
            now = report_time(report) + timedelta(seconds=1)

            attestation = create_attestation(gate_dir, KEY, KEY_ID, now=now)
            verified = verify_attestation(gate_dir, KEY, KEY_ID, now=now)

            self.assertEqual(attestation, verified)
            self.assertEqual(verified["decision"], "promote")
            self.assertEqual(len(verified["artifacts"]), 7)
            self.assertEqual(report["canary"]["report"]["summary"]["direct_rate"], 1.0)
            self.assertEqual(
                report["canary"]["report"]["summary"]["duplicate_violations"],
                0,
            )

    def test_concurrent_canary_rollback_bundle_can_be_signed_and_verified(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            gate_dir, report = make_concurrent_rollback_bundle(Path(directory))
            now = report_time(report) + timedelta(seconds=1)
            attestation = create_attestation(gate_dir, KEY, KEY_ID, now=now)
            verified = verify_attestation(
                gate_dir,
                KEY,
                KEY_ID,
                require_decision="rollback",
                now=now,
            )

            self.assertEqual(attestation["decision"], "rollback")
            self.assertEqual(verified["decision"], "rollback")
            self.assertEqual(len(verified["artifacts"]), 6)

    def test_tampered_artifact_and_inconsistent_decision_are_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            gate_dir, report = make_bundle(Path(directory))
            now = report_time(report) + timedelta(seconds=1)
            create_attestation(gate_dir, KEY, KEY_ID, now=now)
            after_path = gate_dir / "after.json"
            after_path.write_bytes(after_path.read_bytes() + b" ")

            with self.assertRaisesRegex(
                AttestationVerificationError,
                "manifest does not match",
            ):
                verify_attestation(gate_dir, KEY, KEY_ID, now=now)

        with tempfile.TemporaryDirectory() as directory:
            gate_dir, report = make_bundle(Path(directory))
            after_path = gate_dir / "after.json"
            after = json.loads(after_path.read_text(encoding="utf-8"))
            after["metrics"]["queue_lag"] = 7
            after_path.write_text(json.dumps(after), encoding="utf-8")
            after_path.chmod(0o600)

            with self.assertRaisesRegex(
                AttestationVerificationError,
                "summary does not match snapshot evidence",
            ):
                create_attestation(
                    gate_dir,
                    KEY,
                    KEY_ID,
                    now=report_time(report) + timedelta(seconds=1),
                )

        with tempfile.TemporaryDirectory() as directory:
            gate_dir, report = make_bundle(Path(directory))
            gate_report_path = gate_dir / "gate-report.json"
            gate_report = json.loads(gate_report_path.read_text(encoding="utf-8"))
            gate_report["decision"] = "rollback"
            gate_report_path.write_text(json.dumps(gate_report), encoding="utf-8")
            gate_report_path.chmod(0o600)

            with self.assertRaisesRegex(
                AttestationVerificationError,
                "decision is inconsistent",
            ):
                create_attestation(
                    gate_dir,
                    KEY,
                    KEY_ID,
                    now=report_time(report) + timedelta(seconds=1),
                )

    def test_wrong_key_key_id_and_expected_run_are_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            gate_dir, report = make_bundle(Path(directory))
            now = report_time(report) + timedelta(seconds=1)
            create_attestation(gate_dir, KEY, KEY_ID, now=now)

            with self.assertRaisesRegex(AttestationVerificationError, "signature is invalid"):
                verify_attestation(gate_dir, OTHER_KEY, KEY_ID, now=now)
            with self.assertRaisesRegex(AttestationVerificationError, "expected signer"):
                verify_attestation(gate_dir, KEY, "other-key-v1", now=now)
            with self.assertRaisesRegex(AttestationVerificationError, "expected run"):
                verify_attestation(
                    gate_dir,
                    KEY,
                    KEY_ID,
                    expected_run_id="release-gate-other",
                    now=now,
                )

    def test_expired_and_future_attestations_are_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            gate_dir, report = make_bundle(Path(directory))
            gate_time = report_time(report)
            created_at = gate_time + timedelta(seconds=1)
            create_attestation(gate_dir, KEY, KEY_ID, now=created_at)

            with self.assertRaisesRegex(AttestationVerificationError, "expired"):
                verify_attestation(
                    gate_dir,
                    KEY,
                    KEY_ID,
                    max_age_seconds=60,
                    now=created_at + timedelta(seconds=61),
                )
            with self.assertRaisesRegex(AttestationVerificationError, "future"):
                verify_attestation(
                    gate_dir,
                    KEY,
                    KEY_ID,
                    now=created_at - timedelta(seconds=31),
                )

    def test_rollback_requires_explicit_acceptance(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            gate_dir, report = make_bundle(Path(directory), after_queue=600)
            now = report_time(report) + timedelta(seconds=1)
            attestation = create_attestation(gate_dir, KEY, KEY_ID, now=now)
            self.assertEqual(attestation["decision"], "rollback")

            with self.assertRaisesRegex(AttestationVerificationError, "required decision"):
                verify_attestation(gate_dir, KEY, KEY_ID, now=now)
            self.assertEqual(
                verify_attestation(
                    gate_dir,
                    KEY,
                    KEY_ID,
                    require_decision="rollback",
                    now=now,
                )["decision"],
                "rollback",
            )
            self.assertEqual(
                verify_attestation(
                    gate_dir,
                    KEY,
                    KEY_ID,
                    require_decision="any",
                    now=now,
                )["decision"],
                "rollback",
            )

    def test_symlink_insecure_mode_and_unexpected_artifact_are_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            gate_dir, report = make_bundle(Path(directory))
            target = Path(directory) / "outside.json"
            target.write_bytes((gate_dir / "before.json").read_bytes())
            target.chmod(0o600)
            (gate_dir / "before.json").unlink()
            os.symlink(target, gate_dir / "before.json")
            with self.assertRaisesRegex(AttestationVerificationError, "cannot open artifact"):
                create_attestation(
                    gate_dir,
                    KEY,
                    KEY_ID,
                    now=report_time(report) + timedelta(seconds=1),
                )

        with tempfile.TemporaryDirectory() as directory:
            gate_dir, report = make_bundle(Path(directory))
            (gate_dir / "before.json").chmod(0o644)
            with self.assertRaisesRegex(AttestationVerificationError, "permissions must be 0600"):
                create_attestation(
                    gate_dir,
                    KEY,
                    KEY_ID,
                    now=report_time(report) + timedelta(seconds=1),
                )

        with tempfile.TemporaryDirectory() as directory:
            gate_dir, report = make_bundle(Path(directory))
            extra = gate_dir / "notes.txt"
            extra.write_text("untrusted", encoding="utf-8")
            extra.chmod(0o600)
            with self.assertRaisesRegex(AttestationVerificationError, "unexpected"):
                create_attestation(
                    gate_dir,
                    KEY,
                    KEY_ID,
                    now=report_time(report) + timedelta(seconds=1),
                )

    def test_short_key_duplicate_json_and_unsafe_age_are_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            gate_dir, report = make_bundle(Path(directory))
            now = report_time(report) + timedelta(seconds=1)
            with self.assertRaisesRegex(AttestationConfigError, "at least 32 bytes"):
                create_attestation(gate_dir, b"short", KEY_ID, now=now)
            with self.assertRaisesRegex(AttestationConfigError, "between 1"):
                create_attestation(gate_dir, KEY, KEY_ID, max_age_seconds=0, now=now)

            create_attestation(gate_dir, KEY, KEY_ID, now=now)
            path = gate_dir / ATTESTATION_FILE
            original = path.read_text(encoding="utf-8").rstrip()
            duplicate = original[:-1] + ',"signature":"' + ("0" * 64) + '"}'
            path.write_text(duplicate, encoding="utf-8")
            path.chmod(0o600)
            with self.assertRaisesRegex(AttestationVerificationError, "duplicate JSON key"):
                verify_attestation(gate_dir, KEY, KEY_ID, now=now)


if __name__ == "__main__":
    unittest.main()
