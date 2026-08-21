from __future__ import annotations

import json
import os
import sqlite3
import tempfile
import threading
import unittest
from contextlib import redirect_stdout
from concurrent.futures import ThreadPoolExecutor
from datetime import UTC, datetime, timedelta
from pathlib import Path
from io import StringIO
from unittest.mock import patch

from ai_companion_worker.evaluation.direct_rollout import (
    DirectRolloutVerificationError,
    create_attestation,
    evaluate_rollout,
    load_policy,
    write_rollout_report,
)
from ai_companion_worker.evaluation.direct_rollout_traffic import (
    DirectTrafficConfigError,
    DirectTrafficConflictError,
    DirectTrafficVerificationError,
    SQLiteDirectTrafficSandbox,
    build_traffic_request,
    main as traffic_main,
    plan_traffic_change,
)
from ai_companion_worker.evaluation.direct_traffic_certification import (
    KEY_ENV as CERTIFICATION_KEY_ENV,
    KEY_ID_ENV as CERTIFICATION_KEY_ID_ENV,
    SQLiteDirectTrafficCertificationAdapter,
    create_certification,
)
from ai_companion_worker.evaluation.release_gate_history import (
    ReleaseGateHistory,
    environment_sha256,
)


ROOT = Path(__file__).resolve().parents[3]
POLICY_PATH = ROOT / "evals" / "agent" / "baselines" / "direct-rollout.v1.json"
KEY = b"direct-rollout-traffic-test-key-at-least-32-bytes"
KEY_ID = "direct-rollout-traffic-test-v1"
ENVIRONMENT = "staging-cn"
PROVIDER = "local-sandbox-a"
CERTIFICATION_ID = "direct-traffic-adapter-cert-" + "a" * 32
CERTIFICATION_SHA256 = "b" * 64


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
) -> None:
    gate_dir = root / f"release-gate-traffic-{sequence:03d}"
    gate_dir.mkdir(mode=0o700)
    report = {
        "schema_version": "observability-release-gate-report-v1",
        "generated_at": observed_at.isoformat().replace("+00:00", "Z"),
        "decision": "promote",
        "source": {},
        "canary": {
            "name": "agent-direct",
            "status": "passed",
            "exit_code": 0,
            "duration_ms": 5000,
            "report": {
                "schema_version": "agent-direct-canary-report-v1",
                "decision": "pass",
                "baseline_version": "agent-direct-canary-baseline-v1",
                "summary": summary or healthy_summary(),
            },
        },
        "phases": {},
        "violations": [],
    }
    report_path = gate_dir / "gate-report.json"
    report_path.write_text(json.dumps(report), encoding="utf-8")
    report_path.chmod(0o600)
    xml_path = gate_dir / "gate-report.xml"
    xml_path.write_text("<testsuite/>", encoding="utf-8")
    xml_path.chmod(0o600)
    history.record(gate_dir, report)


class AgentDirectRolloutTrafficTest(unittest.TestCase):
    def setUp(self) -> None:
        self.policy, self.policy_sha256 = load_policy(POLICY_PATH)
        self.now = datetime(2026, 8, 12, 12, 0, tzinfo=UTC)

    def history_with_stable_window(
        self, root: Path
    ) -> ReleaseGateHistory:
        history = ReleaseGateHistory(root / "history.sqlite3", ENVIRONMENT)
        for sequence, age in enumerate((600, 300, 0), start=1):
            append_gate(history, root, sequence, self.now - timedelta(seconds=age))
        return history

    def sign_decision(
        self,
        root: Path,
        history: ReleaseGateHistory,
        current: int,
        now: datetime,
    ) -> Path:
        report = evaluate_rollout(
            history,
            self.policy,
            self.policy_sha256,
            current,
            now=now,
        )
        decision_dir = write_rollout_report(root / "decisions", report)
        create_attestation(
            decision_dir,
            history,
            self.policy,
            self.policy_sha256,
            KEY,
            KEY_ID,
            now=now + timedelta(seconds=1),
        )
        return decision_dir

    def sandbox(
        self, root: Path, *, initial: int = 5
    ) -> SQLiteDirectTrafficSandbox:
        return SQLiteDirectTrafficSandbox(
            root / "provider" / "traffic.sqlite3",
            environment_sha256(ENVIRONMENT),
            PROVIDER,
            create=True,
            initial_traffic_percent=initial,
        )

    def request(
        self,
        sandbox: SQLiteDirectTrafficSandbox,
        decision_dir: Path,
        history: ReleaseGateHistory,
        now: datetime,
    ):
        return build_traffic_request(
            sandbox,
            decision_dir,
            history,
            self.policy,
            self.policy_sha256,
            KEY,
            KEY_ID,
            now=now,
        )

    def apply(self, sandbox, request, history, *, now, before_commit=None):
        return sandbox.apply(
            request,
            history.path,
            CERTIFICATION_ID,
            CERTIFICATION_SHA256,
            now=now,
            before_commit=before_commit,
        )

    def test_signed_expand_applies_once_and_replay_reuses_receipt(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            history = self.history_with_stable_window(root)
            decision_dir = self.sign_decision(root, history, 5, self.now)
            sandbox = self.sandbox(root)
            request = self.request(
                sandbox, decision_dir, history, self.now + timedelta(seconds=2)
            )

            plan = plan_traffic_change(sandbox, request)
            first = self.apply(
                sandbox,
                request,
                history,
                now=self.now + timedelta(seconds=3),
            )
            replay_request = self.request(
                sandbox, decision_dir, history, self.now + timedelta(seconds=4)
            )
            replay_plan = plan_traffic_change(sandbox, replay_request)
            second = self.apply(
                sandbox,
                replay_request,
                history,
                now=self.now + timedelta(seconds=4),
            )
            status = sandbox.status()

            self.assertEqual(plan["action"], "ready")
            self.assertEqual(first["outcome"], "applied")
            self.assertFalse(first["reused"])
            self.assertEqual(replay_plan["action"], "reused")
            self.assertTrue(second["reused"])
            self.assertEqual(first["operation_id"], second["operation_id"])
            self.assertEqual(status["current_traffic_percent"], 10)
            self.assertEqual(status["revision"], 1)
            self.assertEqual(status["operations"], 1)
            self.assertEqual(sandbox.path.stat().st_mode & 0o777, 0o600)
            self.assertEqual(sandbox.path.parent.stat().st_mode & 0o777, 0o700)

    def test_concurrent_replay_writes_one_atomic_operation(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            history = self.history_with_stable_window(root)
            decision_dir = self.sign_decision(root, history, 5, self.now)
            sandbox = self.sandbox(root)
            request = self.request(
                sandbox, decision_dir, history, self.now + timedelta(seconds=2)
            )
            barrier = threading.Barrier(2)

            def execute() -> dict[str, object]:
                barrier.wait(timeout=5)
                return self.apply(
                    sandbox,
                    request,
                    history,
                    now=self.now + timedelta(seconds=3),
                )

            with ThreadPoolExecutor(max_workers=2) as executor:
                receipts = list(executor.map(lambda _: execute(), range(2)))

            self.assertEqual(sorted(item["reused"] for item in receipts), [False, True])
            self.assertEqual(sandbox.status()["operations"], 1)
            self.assertEqual(sandbox.status()["revision"], 1)

    def test_stale_request_is_rejected_after_another_signed_transition(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            history = self.history_with_stable_window(root)
            expand_dir = self.sign_decision(root, history, 5, self.now)
            sandbox = self.sandbox(root)
            stale_expand = self.request(
                sandbox, expand_dir, history, self.now + timedelta(seconds=2)
            )

            bad = healthy_summary()
            bad["duplicate_violations"] = 1
            append_gate(history, root, 4, self.now + timedelta(seconds=3), summary=bad)
            disable_dir = self.sign_decision(
                root, history, 5, self.now + timedelta(seconds=3)
            )
            disable = self.request(
                sandbox, disable_dir, history, self.now + timedelta(seconds=5)
            )
            self.apply(
                sandbox,
                disable,
                history,
                now=self.now + timedelta(seconds=6),
            )

            with self.assertRaisesRegex(
                DirectTrafficConflictError, "invalidated|no longer current"
            ):
                self.apply(
                    sandbox,
                    stale_expand,
                    history,
                    now=self.now + timedelta(seconds=7),
                )
            self.assertEqual(sandbox.status()["current_traffic_percent"], 0)
            self.assertEqual(sandbox.status()["operations"], 1)

    def test_hold_is_read_only_and_expired_or_tampered_attestation_fails(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            history = ReleaseGateHistory(root / "history.sqlite3", ENVIRONMENT)
            append_gate(history, root, 1, self.now)
            decision_dir = self.sign_decision(root, history, 5, self.now)
            sandbox = self.sandbox(root)
            request = self.request(
                sandbox, decision_dir, history, self.now + timedelta(seconds=2)
            )

            self.assertEqual(plan_traffic_change(sandbox, request)["action"], "hold")
            with self.assertRaisesRegex(
                DirectTrafficConfigError, "do not authorize"
            ):
                self.apply(
                    sandbox,
                    request,
                    history,
                    now=self.now + timedelta(seconds=3),
                )
            self.assertEqual(sandbox.status()["operations"], 0)

            with self.assertRaisesRegex(
                DirectRolloutVerificationError, "expired|too old"
            ):
                self.request(
                    sandbox,
                    decision_dir,
                    history,
                    self.now + timedelta(seconds=1000),
                )

            attestation_path = decision_dir / "direct-rollout-attestation.json"
            attestation = json.loads(attestation_path.read_text(encoding="utf-8"))
            attestation["signature"] = "0" * 64
            attestation_path.write_text(json.dumps(attestation), encoding="utf-8")
            attestation_path.chmod(0o600)
            with self.assertRaisesRegex(
                DirectRolloutVerificationError, "signature is invalid"
            ):
                self.request(
                    sandbox, decision_dir, history, self.now + timedelta(seconds=2)
                )

    def test_provider_environment_initial_stage_and_ledger_tamper_fail_closed(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            sandbox = self.sandbox(root)
            with self.assertRaisesRegex(
                DirectTrafficVerificationError, "binding is invalid"
            ):
                SQLiteDirectTrafficSandbox(
                    sandbox.path,
                    environment_sha256("another-environment"),
                    PROVIDER,
                )
            with self.assertRaisesRegex(
                DirectTrafficVerificationError, "binding is invalid"
            ):
                SQLiteDirectTrafficSandbox(
                    sandbox.path,
                    environment_sha256(ENVIRONMENT),
                    "another-provider",
                )
            with self.assertRaisesRegex(
                DirectTrafficConfigError, "another initial stage"
            ):
                SQLiteDirectTrafficSandbox(
                    sandbox.path,
                    environment_sha256(ENVIRONMENT),
                    PROVIDER,
                    create=True,
                    initial_traffic_percent=10,
                )

            with sqlite3.connect(sandbox.path) as connection:
                connection.execute(
                    "UPDATE direct_traffic_metadata SET current_traffic_percent = 10"
                )
            with self.assertRaisesRegex(
                DirectTrafficVerificationError, "does not match history"
            ):
                sandbox.status()

    def test_fault_before_commit_rolls_back_and_retry_succeeds(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            history = self.history_with_stable_window(root)
            decision_dir = self.sign_decision(root, history, 5, self.now)
            sandbox = self.sandbox(root)
            request = self.request(
                sandbox, decision_dir, history, self.now + timedelta(seconds=2)
            )

            def fail() -> None:
                raise RuntimeError("simulated process failure")

            with self.assertRaisesRegex(RuntimeError, "simulated process failure"):
                self.apply(
                    sandbox,
                    request,
                    history,
                    now=self.now + timedelta(seconds=3),
                    before_commit=fail,
                )
            self.assertEqual(sandbox.status()["current_traffic_percent"], 5)
            self.assertEqual(sandbox.status()["operations"], 0)

            receipt = self.apply(
                sandbox,
                request,
                history,
                now=self.now + timedelta(seconds=4),
            )
            self.assertFalse(receipt["reused"])
            self.assertEqual(sandbox.status()["current_traffic_percent"], 10)

    def test_history_head_is_fenced_again_inside_apply_transaction(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            history = self.history_with_stable_window(root)
            decision_dir = self.sign_decision(root, history, 5, self.now)
            sandbox = self.sandbox(root)
            request = self.request(
                sandbox, decision_dir, history, self.now + timedelta(seconds=2)
            )

            append_gate(history, root, 4, self.now + timedelta(seconds=3))

            with self.assertRaisesRegex(
                DirectTrafficConflictError, "new release Gate evidence"
            ):
                self.apply(
                    sandbox,
                    request,
                    history,
                    now=self.now + timedelta(seconds=4),
                )
            self.assertEqual(sandbox.status()["current_traffic_percent"], 5)
            self.assertEqual(sandbox.status()["operations"], 0)

    def test_apply_cli_requires_both_signatures_and_audits_certificate(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            now = datetime.now(UTC).replace(microsecond=0)
            history = ReleaseGateHistory(root / "history.sqlite3", ENVIRONMENT)
            for sequence, age in enumerate((600, 300, 0), start=1):
                append_gate(history, root, sequence, now - timedelta(seconds=age))
            decision_dir = self.sign_decision(root, history, 5, now)
            sandbox = self.sandbox(root)
            certification_adapter = SQLiteDirectTrafficCertificationAdapter(
                root / "certification" / "provider.sqlite3",
                environment_sha256(ENVIRONMENT),
                PROVIDER,
                namespace_id="cli-certification-test",
            )
            certification_key = (
                b"direct-traffic-cli-certification-key-with-32-bytes"
            )
            certification_key_id = "direct-traffic-cli-certification-v1"
            certificate, certificate_path, _created = create_certification(
                certification_adapter,
                root / "certificates",
                certification_key,
                certification_key_id,
                nonce="cli-certification-test",
                now=now,
            )
            environment = {
                "OBSERVABILITY_DIRECT_ROLLOUT_ATTESTATION_KEY": KEY.decode(),
                "OBSERVABILITY_DIRECT_ROLLOUT_ATTESTATION_KEY_ID": KEY_ID,
                CERTIFICATION_KEY_ENV: certification_key.decode(),
                CERTIFICATION_KEY_ID_ENV: certification_key_id,
            }
            output = StringIO()
            arguments = [
                "apply",
                "--sandbox-ledger",
                str(sandbox.path),
                "--environment-id",
                ENVIRONMENT,
                "--provider-instance",
                PROVIDER,
                "--decision-dir",
                str(decision_dir),
                "--history-ledger",
                str(history.path),
                "--policy",
                str(POLICY_PATH),
                "--canary-lease-root",
                str(root / "leases"),
                "--adapter-certification",
                str(certificate_path),
            ]
            with patch.dict(os.environ, environment, clear=False), redirect_stdout(output):
                result = traffic_main(arguments)

            receipt = json.loads(output.getvalue())
            operation = sandbox.operation(receipt["decision_id"])
            self.assertEqual(result, 0)
            self.assertEqual(
                receipt["adapter_certification_id"],
                certificate["certification_id"],
            )
            self.assertIsNotNone(operation)
            assert operation is not None
            self.assertEqual(
                operation["adapter_certification_id"],
                certificate["certification_id"],
            )
            self.assertEqual(sandbox.status()["current_traffic_percent"], 10)


if __name__ == "__main__":
    unittest.main()
