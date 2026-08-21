from __future__ import annotations

import json
import os
import sys
import tempfile
import unittest
from contextlib import redirect_stderr
from io import StringIO
from datetime import UTC, datetime
from pathlib import Path
from typing import Any, Mapping
from unittest.mock import patch

from ai_companion_worker.evaluation.direct_canary import (
    build_report as build_direct_report,
    evaluate_case,
    load_baseline as load_direct_baseline,
    load_suite as load_direct_suite,
)
from ai_companion_worker.evaluation.observability import ObservabilityEvalError, build_snapshot
from ai_companion_worker.evaluation.release_gate import (
    ReleaseGateError,
    load_canary_spec,
    main as release_gate_main,
    run_gate,
    try_acquire_canary_lease,
)
from ai_companion_worker.evaluation.release_gate_history import ReleaseGateHistory


ROOT = Path(__file__).resolve().parents[3]
BASELINE = ROOT / "evals" / "observability" / "baselines" / "release.v1.json"


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


def write_spec(
    path: Path,
    command: list[str],
    timeout_seconds: float = 2,
    stabilization_seconds: float = 0,
) -> None:
    path.write_text(
        json.dumps(
            {
                "schema_version": "observability-canary-command-v1",
                "name": "test-canary",
                "command": command,
                "timeout_seconds": timeout_seconds,
                "stabilization_seconds": stabilization_seconds,
            }
        ),
        encoding="utf-8",
    )


def structured_report() -> dict[str, Any]:
    return {
        "schema_version": "agent-observability-canary-report-v1",
        "generated_at": datetime.now(UTC).isoformat().replace("+00:00", "Z"),
        "decision": "pass",
        "source": {
            "baseline_version": "agent-observability-canary-baseline-v1",
            "baseline_sha256": "a" * 64,
        },
        "summary": {
            "cycles": 5,
            "dispatch_hints": 5,
            "not_claimed": 5,
            "terminal_wakes": 15,
            "terminal_no_matches": 15,
            "terminal_errors": 0,
            "wake_latency_max_seconds": 0.1,
            "python_executions": 0,
            "model_calls": 0,
            "duration_ms": 1000,
        },
        "violations": [],
    }


def direct_structured_report() -> dict[str, Any]:
    baseline_path = ROOT / "evals" / "agent" / "baselines" / "direct-canary.v1.json"
    suite_path = ROOT / "evals" / "agent" / "suites" / "direct-canary.v1.json"
    baseline, baseline_digest = load_direct_baseline(baseline_path)
    suite, suite_digest = load_direct_suite(suite_path)
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


class ObservabilityReleaseGateTest(unittest.TestCase):
    def test_repository_canary_specs_are_valid(self) -> None:
        specs = (ROOT / "evals" / "observability" / "canaries").glob("*.json")
        loaded = {load_canary_spec(path)[0]["name"] for path in specs}
        self.assertEqual(loaded, {"api-health", "agent-direct", "agent-observability"})

    def test_history_environment_binding_is_checked_before_canary_dispatch(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            ledger = root / "history.sqlite3"
            ReleaseGateHistory(ledger, "environment-a")

            with patch(
                "ai_companion_worker.evaluation.release_gate.run_gate"
            ) as mocked_run, redirect_stderr(StringIO()):
                result = release_gate_main(
                    [
                        "--metrics-url",
                        "http://metrics.invalid/metrics",
                        "--baseline",
                        str(BASELINE),
                        "--canary-spec",
                        str(
                            ROOT
                            / "evals"
                            / "observability"
                            / "canaries"
                            / "agent-direct.v1.json"
                        ),
                        "--output-root",
                        str(root / "artifacts"),
                        "--history-ledger",
                        str(ledger),
                        "--environment-id",
                        "environment-b",
                    ]
                )

            self.assertEqual(result, 2)
            mocked_run.assert_not_called()
            self.assertFalse((root / "artifacts").exists())

    def test_main_keeps_environment_lease_until_history_record_is_committed(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            environment = "staging-main-lease"
            history = ReleaseGateHistory(root / "history.sqlite3", environment)
            report = {
                "decision": "promote",
                "violations": [],
            }
            observed = False

            def record_while_locked(
                _output: Path, _report: Mapping[str, Any]
            ) -> bool:
                nonlocal observed
                competing = try_acquire_canary_lease(root / "leases", environment)
                observed = competing is None
                if competing is not None:
                    competing.release()
                return True

            argv = [
                "--metrics-url",
                "http://metrics.invalid/metrics",
                "--baseline",
                str(BASELINE),
                "--canary-spec",
                str(
                    ROOT
                    / "evals"
                    / "observability"
                    / "canaries"
                    / "api-health.v1.json"
                ),
                "--output-root",
                str(root / "artifacts"),
                "--canary-lease-root",
                str(root / "leases"),
                "--history-ledger",
                str(root / "history.sqlite3"),
                "--environment-id",
                environment,
            ]
            with (
                patch(
                    "ai_companion_worker.evaluation.release_gate.try_acquire_canary_lease",
                    wraps=try_acquire_canary_lease,
                ),
                patch(
                    "ai_companion_worker.evaluation.release_gate.run_gate",
                    return_value=(report, 0),
                ) as run_gate_mock,
                patch(
                    "ai_companion_worker.evaluation.release_gate._new_run_directory",
                    return_value=root / "artifacts" / "release-gate-test",
                ),
                patch.object(history, "record", side_effect=record_while_locked),
            ):
                # main imports the history class inside the function, so patch the
                # source module and supply this already-bound instance.
                with patch(
                    "ai_companion_worker.evaluation.release_gate_history.ReleaseGateHistory",
                    return_value=history,
                ):
                    result = release_gate_main(argv)

            self.assertEqual(result, 0)
            self.assertTrue(observed)
            self.assertIsNotNone(run_gate_mock.call_args.kwargs["preacquired_lease"])

    def test_successful_canary_and_stable_metrics_promote_without_leaking_arguments(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            spec = root / "canary.json"
            output = root / "gate"
            write_spec(spec, [sys.executable, "-c", "pass", "super-secret-argument"])

            report, result = run_gate(
                "http://metrics.invalid/metrics",
                BASELINE,
                spec,
                output,
                capture=SnapshotSequence([0, 0]),
            )

            self.assertEqual(result, 0)
            self.assertEqual(report["decision"], "promote")
            self.assertEqual(report["violations"], [])
            self.assertEqual(report["phases"]["evaluation"], "passed")
            self.assertNotIn("super-secret-argument", (output / "gate-report.json").read_text())
            self.assertEqual((output / "gate-report.json").stat().st_mode & 0o777, 0o600)
            self.assertEqual(output.stat().st_mode & 0o777, 0o700)
            self.assertIn('failures="0"', (output / "gate-report.xml").read_text())

    def test_nonzero_canary_rolls_back_but_still_captures_after_snapshot(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            spec = root / "canary.json"
            output = root / "gate"
            write_spec(spec, [sys.executable, "-c", "raise SystemExit(7)"])

            report, result = run_gate(
                "http://metrics.invalid/metrics",
                BASELINE,
                spec,
                output,
                capture=SnapshotSequence([0, 0]),
            )

            self.assertEqual(result, 1)
            self.assertEqual(report["decision"], "rollback")
            self.assertEqual(report["canary"]["exit_code"], 7)
            self.assertTrue((output / "after.json").is_file())
            self.assertTrue(any(item["code"] == "canary_failed" for item in report["violations"]))

    def test_timed_out_canary_rolls_back(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            spec = root / "canary.json"
            output = root / "gate"
            write_spec(spec, [sys.executable, "-c", "import time; time.sleep(1)"], 0.01)

            report, result = run_gate(
                "http://metrics.invalid/metrics",
                BASELINE,
                spec,
                output,
                capture=SnapshotSequence([0, 0]),
            )

            self.assertEqual(result, 1)
            self.assertEqual(report["canary"]["status"], "timed_out")
            self.assertTrue(any(item["code"] == "canary_timed_out" for item in report["violations"]))

    def test_agent_observability_requires_and_binds_fresh_structured_report(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            report_path = root / "artifacts" / "agent-eval" / "canary-report.json"
            spec = root / "canary.json"
            output = root / "gate"
            payload = json.dumps(structured_report())
            write_spec(
                spec,
                [sys.executable, "-c", "from pathlib import Path; import os, sys; p=Path(sys.argv[1]); p.parent.mkdir(parents=True, exist_ok=True); p.write_text(sys.argv[2]); os.chmod(p, 0o600)", str(report_path), payload],
            )
            spec_value = json.loads(spec.read_text(encoding="utf-8"))
            spec_value["name"] = "agent-observability"
            spec.write_text(json.dumps(spec_value), encoding="utf-8")
            previous = os.environ.get("AGENT_OBSERVABILITY_CANARY_JSON_REPORT")
            previous_cwd = Path.cwd()
            os.chdir(root)
            os.environ["AGENT_OBSERVABILITY_CANARY_JSON_REPORT"] = "artifacts/agent-eval/canary-report.json"
            try:
                report, result = run_gate(
                    "http://metrics.invalid/metrics", BASELINE, spec, output,
                    capture=SnapshotSequence([0, 0]),
                )
            finally:
                os.chdir(previous_cwd)
                if previous is None:
                    os.environ.pop("AGENT_OBSERVABILITY_CANARY_JSON_REPORT", None)
                else:
                    os.environ["AGENT_OBSERVABILITY_CANARY_JSON_REPORT"] = previous
            self.assertEqual(result, 0)
            self.assertEqual(report["canary"]["report"]["summary"]["model_calls"], 0)
            self.assertTrue((output / "canary-report.json").is_file())
            self.assertEqual((output / "canary-report.json").stat().st_mode & 0o777, 0o600)

    def test_agent_observability_missing_structured_report_rolls_back(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            spec = root / "canary.json"
            output = root / "gate"
            write_spec(spec, [sys.executable, "-c", "pass"])
            spec_value = json.loads(spec.read_text(encoding="utf-8"))
            spec_value["name"] = "agent-observability"
            spec.write_text(json.dumps(spec_value), encoding="utf-8")
            previous = os.environ.get("AGENT_OBSERVABILITY_CANARY_JSON_REPORT")
            previous_cwd = Path.cwd()
            os.chdir(root)
            os.environ["AGENT_OBSERVABILITY_CANARY_JSON_REPORT"] = "artifacts/agent-eval/missing-test-report.json"
            try:
                report, result = run_gate(
                    "http://metrics.invalid/metrics", BASELINE, spec, output,
                    capture=SnapshotSequence([0, 0]),
                )
            finally:
                os.chdir(previous_cwd)
                if previous is None:
                    os.environ.pop("AGENT_OBSERVABILITY_CANARY_JSON_REPORT", None)
                else:
                    os.environ["AGENT_OBSERVABILITY_CANARY_JSON_REPORT"] = previous
            self.assertEqual(result, 1)
            self.assertEqual(report["decision"], "rollback")
            self.assertEqual(report["violations"][0]["code"], "canary_report_invalid")

    def test_agent_direct_requires_recomputed_structured_quality_report(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            report_path = root / "artifacts" / "agent-eval" / "direct-canary.json"
            spec = root / "canary.json"
            output = root / "gate"
            payload = json.dumps(direct_structured_report())
            payload_path = root / "direct-payload.json"
            payload_path.write_text(payload, encoding="utf-8")
            write_spec(
                spec,
                [
                    sys.executable,
                    "-c",
                    "from pathlib import Path; import os, sys; p=Path(sys.argv[1]); p.parent.mkdir(parents=True, exist_ok=True); p.write_bytes(Path(sys.argv[2]).read_bytes()); os.chmod(p, 0o600)",
                    str(report_path),
                    str(payload_path),
                ],
            )
            spec_value = json.loads(spec.read_text(encoding="utf-8"))
            spec_value["name"] = "agent-direct"
            spec.write_text(json.dumps(spec_value), encoding="utf-8")
            environment = {
                "AGENT_DIRECT_CANARY_JSON_REPORT": "artifacts/agent-eval/direct-canary.json",
                "AGENT_DIRECT_CANARY_BASELINE": str(
                    ROOT / "evals" / "agent" / "baselines" / "direct-canary.v1.json"
                ),
                "AGENT_DIRECT_CANARY_SUITE": str(
                    ROOT / "evals" / "agent" / "suites" / "direct-canary.v1.json"
                ),
            }
            previous = {key: os.environ.get(key) for key in environment}
            previous_cwd = Path.cwd()
            os.chdir(root)
            os.environ.update(environment)
            try:
                report, result = run_gate(
                    "http://metrics.invalid/metrics",
                    BASELINE,
                    spec,
                    output,
                    capture=SnapshotSequence([0, 0]),
                )
            finally:
                os.chdir(previous_cwd)
                for key, value in previous.items():
                    if value is None:
                        os.environ.pop(key, None)
                    else:
                        os.environ[key] = value
            self.assertEqual(result, 0)
            self.assertEqual(report["canary"]["report"]["summary"]["direct_rate"], 1.0)
            self.assertEqual(
                report["canary"]["report"]["summary"]["single_model_call_rate"],
                1.0,
            )
            self.assertEqual(report["canary"]["report"]["summary"]["duplicate_violations"], 0)

    def test_agent_direct_tampered_summary_rolls_back_even_when_child_exits_zero(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            report_path = root / "artifacts" / "agent-eval" / "direct-canary.json"
            structured = direct_structured_report()
            structured["summary"]["single_model_call_rate"] = 1.0
            structured["cases"][0]["model_calls"] = 2
            payload_path = root / "direct-payload.json"
            payload_path.write_text(json.dumps(structured), encoding="utf-8")
            spec = root / "canary.json"
            output = root / "gate"
            write_spec(
                spec,
                [
                    sys.executable,
                    "-c",
                    "from pathlib import Path; import os, sys; p=Path(sys.argv[1]); p.parent.mkdir(parents=True, exist_ok=True); p.write_bytes(Path(sys.argv[2]).read_bytes()); os.chmod(p, 0o600)",
                    str(report_path),
                    str(payload_path),
                ],
            )
            spec_value = json.loads(spec.read_text(encoding="utf-8"))
            spec_value["name"] = "agent-direct"
            spec.write_text(json.dumps(spec_value), encoding="utf-8")
            environment = {
                "AGENT_DIRECT_CANARY_JSON_REPORT": "artifacts/agent-eval/direct-canary.json",
                "AGENT_DIRECT_CANARY_BASELINE": str(
                    ROOT / "evals" / "agent" / "baselines" / "direct-canary.v1.json"
                ),
                "AGENT_DIRECT_CANARY_SUITE": str(
                    ROOT / "evals" / "agent" / "suites" / "direct-canary.v1.json"
                ),
            }
            previous = {key: os.environ.get(key) for key in environment}
            previous_cwd = Path.cwd()
            os.chdir(root)
            os.environ.update(environment)
            try:
                report, result = run_gate(
                    "http://metrics.invalid/metrics",
                    BASELINE,
                    spec,
                    output,
                    capture=SnapshotSequence([0, 0]),
                )
            finally:
                os.chdir(previous_cwd)
                for key, value in previous.items():
                    if value is None:
                        os.environ.pop(key, None)
                    else:
                        os.environ[key] = value
            self.assertEqual(result, 1)
            self.assertEqual(report["violations"][0]["code"], "canary_report_invalid")

    def test_environment_lease_rejects_a_concurrent_canary_without_running_it(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            lease_root = root / "leases"
            marker = root / "executed"
            spec = root / "canary.json"
            output = root / "gate"
            write_spec(
                spec,
                [
                    sys.executable,
                    "-c",
                    "from pathlib import Path; import sys; Path(sys.argv[1]).write_text('x')",
                    str(marker),
                ],
            )
            spec_value = json.loads(spec.read_text(encoding="utf-8"))
            spec_value["name"] = "agent-observability"
            spec.write_text(json.dumps(spec_value), encoding="utf-8")
            lease = try_acquire_canary_lease(lease_root, "staging-cn")
            self.assertIsNotNone(lease)
            try:
                report, result = run_gate(
                    "http://metrics.invalid/metrics",
                    BASELINE,
                    spec,
                    output,
                    capture=SnapshotSequence([0, 0]),
                    lease_root=lease_root,
                    environment_id="staging-cn",
                )
            finally:
                assert lease is not None
                lease.release()

            self.assertEqual(result, 1)
            self.assertEqual(report["decision"], "rollback")
            self.assertEqual(report["canary"]["status"], "failed")
            self.assertEqual(report["violations"][0]["code"], "canary_concurrent_run")
            self.assertFalse(marker.exists())
            self.assertTrue((output / "after.json").is_file())
            self.assertIsNone(report["source"]["structured_canary_report_sha256"])

    def test_unsafe_canary_lease_directory_fails_closed(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            lease_root = root / "leases"
            lease_root.mkdir(mode=0o755)
            spec = root / "canary.json"
            output = root / "gate"
            write_spec(spec, [sys.executable, "-c", "pass"])

            report, result = run_gate(
                "http://metrics.invalid/metrics",
                BASELINE,
                spec,
                output,
                capture=SnapshotSequence([0, 0]),
                lease_root=lease_root,
                environment_id="staging-cn",
            )

            self.assertEqual(result, 1)
            self.assertEqual(report["violations"][0]["code"], "canary_lease_invalid")

    def test_metric_regression_rolls_back_after_successful_canary(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            spec = root / "canary.json"
            output = root / "gate"
            write_spec(spec, [sys.executable, "-c", "pass"])

            report, result = run_gate(
                "http://metrics.invalid/metrics",
                BASELINE,
                spec,
                output,
                capture=SnapshotSequence([0, 600]),
            )

            self.assertEqual(result, 1)
            self.assertEqual(report["canary"]["status"], "passed")
            self.assertEqual(report["phases"]["evaluation"], "failed")
            codes = {item["code"] for item in report["violations"]}
            self.assertEqual(codes, {"absolute_slo_exceeded", "release_regression"})

    def test_failed_before_capture_never_runs_canary(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            marker = root / "executed"
            spec = root / "canary.json"
            output = root / "gate"
            write_spec(
                spec,
                [
                    sys.executable,
                    "-c",
                    "from pathlib import Path; import sys; Path(sys.argv[1]).write_text('x')",
                    str(marker),
                ],
            )

            def fail_capture(_url: str) -> dict[str, Any]:
                raise ObservabilityEvalError("controlled capture failure")

            report, result = run_gate(
                "http://metrics.invalid/metrics",
                BASELINE,
                spec,
                output,
                capture=fail_capture,
            )

            self.assertEqual(result, 2)
            self.assertEqual(report["decision"], "rollback")
            self.assertEqual(report["canary"]["status"], "not_run")
            self.assertFalse(marker.exists())

    def test_shell_command_strings_and_duplicate_spec_keys_are_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            shell_spec = root / "shell.json"
            duplicate_spec = root / "duplicate.json"
            write_spec(shell_spec, ["sh", "-c", "echo unsafe"])
            duplicate_spec.write_text(
                '{"schema_version":"observability-canary-command-v1",'
                '"name":"one","name":"two","command":["true"],'
                '"timeout_seconds":1,"stabilization_seconds":0}',
                encoding="utf-8",
            )

            with self.assertRaisesRegex(ReleaseGateError, "shell command string"):
                load_canary_spec(shell_spec)
            with self.assertRaisesRegex(ReleaseGateError, "duplicate JSON key"):
                load_canary_spec(duplicate_spec)


if __name__ == "__main__":
    unittest.main()
