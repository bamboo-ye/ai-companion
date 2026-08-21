from __future__ import annotations

import json
import tempfile
import unittest
from datetime import UTC, datetime, timedelta
from pathlib import Path

from ai_companion_worker.evaluation.direct_rollout import (
    ATTESTATION_FILE,
    DirectRolloutConfigError,
    DirectRolloutVerificationError,
    create_attestation,
    evaluate_rollout,
    load_policy,
    verify_attestation,
    write_rollout_report,
)
from ai_companion_worker.evaluation.release_gate_history import ReleaseGateHistory


ROOT = Path(__file__).resolve().parents[3]
POLICY_PATH = ROOT / "evals" / "agent" / "baselines" / "direct-rollout.v1.json"
KEY = b"direct-rollout-test-key-with-at-least-32-bytes"
KEY_ID = "direct-rollout-test-v1"
ENVIRONMENT = "staging-cn"


def healthy_summary() -> dict[str, int | float | bool]:
    return {
        "cases": 5,
        "completed": 5,
        "assistant_messages": 5,
        "assistant_delivery_violations": 0,
        "direct_runs": 5,
        "direct_rate": 1.0,
        "single_model_call_runs": 5,
        "single_model_call_rate": 1.0,
        "quality_passed_runs": 5,
        "quality_pass_rate": 1.0,
        "content_passed_runs": 5,
        "content_pass_rate": 1.0,
        "repair_attempted_runs": 0,
        "repair_succeeded_runs": 0,
        "repair_success_rate": 1.0,
        "deterministic_repairs": 0,
        "rewrite_runs": 0,
        "response_quality_failures": 0,
        "duplicate_violations": 0,
        "planned_runs": 0,
        "tool_actions": 0,
        "model_calls": 5,
        "duration_p95_ms": 4000.0,
        "duration_max_ms": 4000,
        "repair_probe_passed": True,
    }


def append_gate(
    history: ReleaseGateHistory,
    root: Path,
    sequence: int,
    observed_at: datetime,
    *,
    summary: dict[str, int | float | bool] | None = None,
    decision: str = "promote",
) -> Path:
    gate_dir = root / f"release-gate-sample-{sequence:03d}"
    gate_dir.mkdir(mode=0o700)
    value = summary or healthy_summary()
    report = {
        "schema_version": "observability-release-gate-report-v1",
        "generated_at": observed_at.astimezone(UTC).isoformat().replace("+00:00", "Z"),
        "decision": decision,
        "source": {},
        "canary": {
            "name": "agent-direct",
            "status": "passed" if decision == "promote" else "failed",
            "exit_code": 0 if decision == "promote" else 1,
            "duration_ms": 5000,
            "report": {
                "schema_version": "agent-direct-canary-report-v1",
                "decision": "pass" if decision == "promote" else "fail",
                "baseline_version": "agent-direct-canary-baseline-v1",
                "summary": value,
            },
        },
        "phases": {},
        "violations": [] if decision == "promote" else [{"code": "canary_failed"}],
    }
    report_path = gate_dir / "gate-report.json"
    report_path.write_text(json.dumps(report), encoding="utf-8")
    report_path.chmod(0o600)
    xml_path = gate_dir / "gate-report.xml"
    xml_path.write_text("<testsuite/>", encoding="utf-8")
    xml_path.chmod(0o600)
    history.record(gate_dir, report)
    return gate_dir


class AgentDirectRolloutTest(unittest.TestCase):
    def setUp(self) -> None:
        self.policy, self.policy_digest = load_policy(POLICY_PATH)
        self.now = datetime(2026, 8, 12, 12, 0, tzinfo=UTC)

    def make_history(self, root: Path) -> ReleaseGateHistory:
        return ReleaseGateHistory(root / "history.sqlite3", ENVIRONMENT)

    def append_stable_window(
        self, history: ReleaseGateHistory, root: Path
    ) -> None:
        for sequence, age in enumerate((600, 300, 0), start=1):
            append_gate(history, root, sequence, self.now - timedelta(seconds=age))

    def test_stable_window_expands_exactly_one_stage(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            history = self.make_history(root)
            self.append_stable_window(history, root)

            report = evaluate_rollout(
                history, self.policy, self.policy_digest, 5, now=self.now
            )

            self.assertEqual(report["decision"], "expand")
            self.assertEqual(report["target_traffic_percent"], 10)
            self.assertEqual(report["windows"]["fast"]["selected"], 1)
            self.assertEqual(report["windows"]["stable"]["selected"], 3)
            self.assertEqual(report["windows"]["stable"]["coverage_seconds"], 600)

    def test_insufficient_stale_and_maximum_stage_windows_hold(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            history = self.make_history(root)
            append_gate(history, root, 1, self.now)
            insufficient = evaluate_rollout(
                history, self.policy, self.policy_digest, 5, now=self.now
            )
            self.assertEqual(insufficient["decision"], "hold")
            self.assertEqual(insufficient["reasons"][0]["code"], "insufficient_direct_samples")

        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            history = self.make_history(root)
            self.append_stable_window(history, root)
            stale = evaluate_rollout(
                history,
                self.policy,
                self.policy_digest,
                5,
                now=self.now + timedelta(seconds=301),
            )
            maximum = evaluate_rollout(
                history, self.policy, self.policy_digest, 100, now=self.now
            )
            self.assertEqual(stale["decision"], "hold")
            self.assertEqual(stale["reasons"][0]["code"], "stale_direct_sample")
            self.assertEqual(maximum["decision"], "hold")
            self.assertEqual(maximum["target_traffic_percent"], 100)

    def test_fast_latency_regression_rolls_back_one_stage(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            history = self.make_history(root)
            self.append_stable_window(history, root)
            slow = healthy_summary()
            slow["duration_p95_ms"] = 16000.0
            append_gate(history, root, 4, self.now + timedelta(seconds=1), summary=slow)

            report = evaluate_rollout(
                history,
                self.policy,
                self.policy_digest,
                25,
                now=self.now + timedelta(seconds=1),
            )

            self.assertEqual(report["decision"], "rollback")
            self.assertEqual(report["target_traffic_percent"], 10)
            self.assertIn("latency_regression", report["windows"]["fast"]["failure_codes"])

            minimum = evaluate_rollout(
                history,
                self.policy,
                self.policy_digest,
                0,
                now=self.now + timedelta(seconds=1),
            )
            self.assertEqual(minimum["decision"], "hold")
            self.assertEqual(minimum["target_traffic_percent"], 0)

    def test_fast_quality_duplicate_or_delivery_regression_disables(self) -> None:
        for field, value, code in (
            ("quality_pass_rate", 0.8, "quality_regression"),
            ("duplicate_violations", 1, "duplicate_content_detected"),
            ("assistant_delivery_violations", 1, "assistant_delivery_regression"),
        ):
            with self.subTest(field=field), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                history = self.make_history(root)
                self.append_stable_window(history, root)
                bad = healthy_summary()
                bad[field] = value
                append_gate(history, root, 4, self.now + timedelta(seconds=1), summary=bad)

                report = evaluate_rollout(
                    history,
                    self.policy,
                    self.policy_digest,
                    50,
                    now=self.now + timedelta(seconds=1),
                )

                self.assertEqual(report["decision"], "disable")
                self.assertEqual(report["target_traffic_percent"], 0)
                self.assertIn(code, report["windows"]["fast"]["failure_codes"])

    def test_signed_decision_is_idempotent_tamper_evident_and_invalidated_by_new_history(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            history = self.make_history(root)
            self.append_stable_window(history, root)
            report = evaluate_rollout(
                history, self.policy, self.policy_digest, 5, now=self.now
            )
            decision_dir = write_rollout_report(root / "decisions", report)

            first, created = create_attestation(
                decision_dir,
                history,
                self.policy,
                self.policy_digest,
                KEY,
                KEY_ID,
                now=self.now + timedelta(seconds=1),
            )
            second, reused = create_attestation(
                decision_dir,
                history,
                self.policy,
                self.policy_digest,
                KEY,
                KEY_ID,
                now=self.now + timedelta(seconds=1),
            )
            verified = verify_attestation(
                decision_dir,
                history,
                self.policy,
                self.policy_digest,
                KEY,
                KEY_ID,
                require_decision="expand",
                now=self.now + timedelta(seconds=2),
            )

            self.assertTrue(created)
            self.assertFalse(reused)
            self.assertEqual(first, second)
            self.assertEqual(verified["target_traffic_percent"], 10)
            self.assertEqual((decision_dir / ATTESTATION_FILE).stat().st_mode & 0o777, 0o600)

            attestation_path = decision_dir / ATTESTATION_FILE
            original = attestation_path.read_text(encoding="utf-8")
            tampered = json.loads(original)
            tampered["signature"] = "0" * 64
            attestation_path.write_text(json.dumps(tampered), encoding="utf-8")
            attestation_path.chmod(0o600)
            with self.assertRaisesRegex(DirectRolloutVerificationError, "signature is invalid"):
                verify_attestation(
                    decision_dir,
                    history,
                    self.policy,
                    self.policy_digest,
                    KEY,
                    KEY_ID,
                    now=self.now + timedelta(seconds=2),
                )
            attestation_path.write_text(original, encoding="utf-8")
            attestation_path.chmod(0o600)

            append_gate(history, root, 4, self.now + timedelta(seconds=3))
            with self.assertRaisesRegex(
                DirectRolloutVerificationError, "current history and policy"
            ):
                verify_attestation(
                    decision_dir,
                    history,
                    self.policy,
                    self.policy_digest,
                    KEY,
                    KEY_ID,
                    now=self.now + timedelta(seconds=4),
                )

    def test_invalid_stage_duplicate_policy_keys_and_report_tampering_are_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            history = self.make_history(root)
            with self.assertRaisesRegex(DirectRolloutConfigError, "exact policy stage"):
                evaluate_rollout(history, self.policy, self.policy_digest, 7, now=self.now)

            duplicate = root / "duplicate-policy.json"
            duplicate.write_text(
                POLICY_PATH.read_text(encoding="utf-8").replace(
                    '"schema_version": "agent-direct-rollout-policy-v1",',
                    '"schema_version": "agent-direct-rollout-policy-v1",'
                    '"schema_version": "agent-direct-rollout-policy-v1",',
                ),
                encoding="utf-8",
            )
            with self.assertRaisesRegex(DirectRolloutConfigError, "JSON is invalid"):
                load_policy(duplicate)

            self.append_stable_window(history, root)
            report = evaluate_rollout(
                history, self.policy, self.policy_digest, 5, now=self.now
            )
            report["target_traffic_percent"] = 100
            with self.assertRaisesRegex(DirectRolloutVerificationError, "ID binding"):
                write_rollout_report(root / "decisions", report)


if __name__ == "__main__":
    unittest.main()
