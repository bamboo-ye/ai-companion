from __future__ import annotations

import json
import io
import sqlite3
import tempfile
import unittest
from datetime import UTC, datetime, timedelta
from contextlib import redirect_stdout
from pathlib import Path
from typing import Any

from ai_companion_worker.evaluation.deployment_controller import (
    DeploymentLedger,
    _canonical_json,
    _digest,
    _format_timestamp,
)
from ai_companion_worker.evaluation.deployment_shadow import (
    SHADOW_REPORT_SCHEMA_VERSION,
    DeploymentShadowError,
    ReadOnlyHTTPProviderAdapter,
    ReadOnlyHTTPProviderConfig,
    ShadowRunner,
    ShadowRuntimeState,
    run_shadow_reconciliation,
)
from ai_companion_worker.evaluation.deployment_shadow_gate import (
    DeploymentShadowGateVerificationError,
    ShadowGatePolicy,
    create_shadow_attestation,
    evaluate_shadow_history,
    main as shadow_gate_main,
    verify_shadow_attestation,
    write_shadow_gate_report,
)
from ai_companion_worker.evaluation.deployment_shadow_history import (
    DeploymentShadowHistoryError,
    ShadowHistoryLedger,
)


NOW = datetime(2026, 8, 12, 1, 0, tzinfo=UTC)
PROVIDER_DIGEST = "a" * 64
KEY = b"shadow-gate-test-signing-key-at-least-32-bytes"
KEY_ID = "shadow-gate-test-v1"


def shadow_report(
    observed_at: datetime,
    *,
    outcome: str = "match",
    provider_digest: str = PROVIDER_DIGEST,
) -> dict[str, Any]:
    outcomes = {
        "match",
        "provider_ahead",
        "provider_behind",
        "provider_missing",
        "external_id_mismatch",
        "status_mismatch",
        "ledger_indeterminate",
        "lookup_error",
    }
    counts = {name: int(name == outcome) for name in outcomes}
    provider_status: str | None = "accepted"
    if outcome == "provider_ahead":
        provider_status = "completed"
    elif outcome in {"provider_missing", "lookup_error"}:
        provider_status = None
    error_code = "provider_http_503" if outcome == "lookup_error" else None
    return {
        "schema_version": SHADOW_REPORT_SCHEMA_VERSION,
        "generated_at": _format_timestamp(observed_at),
        "adapter": "test-provider",
        "implementation_version": "1.0.0",
        "provider_instance_sha256": provider_digest,
        "batch": {
            "selected": 1,
            "retried_lookups": 0,
            **counts,
        },
        "results": [
            {
                "deployment_id": "shadow-gate-deployment",
                "ledger_status": "accepted",
                "provider_status": provider_status,
                "outcome": outcome,
                "error_code": error_code,
                "attempts": 1,
                "observed_at": _format_timestamp(observed_at),
            }
        ],
    }


def passing_history(root: Path) -> tuple[ShadowHistoryLedger, ShadowGatePolicy]:
    history = ShadowHistoryLedger(root / "history.sqlite3", PROVIDER_DIGEST, now=NOW)
    for offset in (0, 30, 60, 90):
        observed_at = NOW + timedelta(seconds=offset)
        history.record_success(shadow_report(observed_at), 0.1, observed_at)
    policy = ShadowGatePolicy(
        minimum_runs=4,
        minimum_window_seconds=90,
        minimum_selected_total=4,
        maximum_gap_seconds=35,
        maximum_report_age_seconds=10,
    )
    return history, policy


class ObservabilityDeploymentShadowGateTest(unittest.TestCase):
    def test_history_is_private_append_only_bound_and_idempotent(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            history = ShadowHistoryLedger(root / "history.sqlite3", PROVIDER_DIGEST, now=NOW)
            report = shadow_report(NOW)

            self.assertTrue(history.record_success(report, 0.25, NOW))
            self.assertFalse(history.record_success(report, 0.25, NOW))
            self.assertTrue(
                history.record_failure(
                    "shadow_error",
                    0.5,
                    NOW + timedelta(seconds=30),
                )
            )
            events = history.events(until=NOW + timedelta(seconds=31))
            self.assertEqual([event["sequence"] for event in events], [1, 2])
            self.assertEqual([event["outcome"] for event in events], ["success", "error"])
            self.assertEqual(events[1]["previous_chain_sha256"], events[0]["chain_sha256"])
            self.assertEqual(history.path.stat().st_mode & 0o777, 0o600)
            self.assertEqual(history.path.parent.stat().st_mode & 0o777, 0o700)

            with self.assertRaises(DeploymentShadowHistoryError):
                history.record_failure("different_error", 0.25, NOW)
            with self.assertRaises(DeploymentShadowHistoryError):
                history.record_success(
                    shadow_report(
                        NOW + timedelta(seconds=60),
                        provider_digest="b" * 64,
                    ),
                    0.1,
                    NOW + timedelta(seconds=60),
                )
            with self.assertRaises(DeploymentShadowHistoryError):
                ShadowHistoryLedger(history.path, "b" * 64, create=False)

    def test_history_hash_chain_and_embedded_report_detect_tampering(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            history, _policy = passing_history(root)
            connection = sqlite3.connect(history.path)
            connection.execute(
                "UPDATE shadow_history_events SET chain_sha256 = ? WHERE sequence = 2",
                ("f" * 64,),
            )
            connection.commit()
            connection.close()

            with self.assertRaises(DeploymentShadowHistoryError):
                history.events(until=NOW + timedelta(seconds=100))

    def test_stable_window_passes_and_binds_the_history_chain(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            history, policy = passing_history(root)
            report = evaluate_shadow_history(
                history,
                policy,
                now=NOW + timedelta(seconds=90),
            )

            self.assertEqual(report["decision"], "pass")
            self.assertEqual(report["violations"], [])
            self.assertEqual(report["window"]["runs"], 4)
            self.assertEqual(report["window"]["selected_total"], 4)
            self.assertEqual(
                report["window"]["history_chain_sha256"],
                history.events(until=NOW + timedelta(seconds=90))[-1]["chain_sha256"],
            )
            path = write_shadow_gate_report(root / "gate", report)
            self.assertEqual(path.stat().st_mode & 0o777, 0o600)
            self.assertEqual(path.parent.stat().st_mode & 0o777, 0o700)

    def test_failed_drifted_gapped_and_stale_window_is_held(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            history = ShadowHistoryLedger(root / "history.sqlite3", PROVIDER_DIGEST, now=NOW)
            history.record_success(shadow_report(NOW), 0.1, NOW)
            history.record_failure(
                "provider_transport_error",
                0.2,
                NOW + timedelta(seconds=30),
            )
            history.record_success(
                shadow_report(NOW + timedelta(seconds=60), outcome="provider_ahead"),
                0.1,
                NOW + timedelta(seconds=60),
            )
            history.record_success(
                shadow_report(NOW + timedelta(seconds=120), outcome="lookup_error"),
                0.1,
                NOW + timedelta(seconds=120),
            )
            report = evaluate_shadow_history(
                history,
                ShadowGatePolicy(
                    minimum_runs=4,
                    minimum_window_seconds=150,
                    minimum_selected_total=4,
                    maximum_gap_seconds=35,
                    maximum_report_age_seconds=20,
                ),
                now=NOW + timedelta(seconds=150),
            )
            codes = {item["code"] for item in report["violations"]}

            self.assertEqual(report["decision"], "hold")
            self.assertTrue(
                {
                    "insufficient_selected",
                    "failed_runs",
                    "drift_detected",
                    "lookup_errors",
                    "observation_gap",
                    "observation_stale",
                }.issubset(codes)
            )

    def test_attestation_is_fresh_idempotent_and_bound_to_pass_evidence(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            history, policy = passing_history(root)
            gate_time = NOW + timedelta(seconds=90)
            report = evaluate_shadow_history(history, policy, now=gate_time)
            gate_dir = root / "gate"
            write_shadow_gate_report(gate_dir, report)

            attestation, created = create_shadow_attestation(
                gate_dir,
                KEY,
                KEY_ID,
                ttl_seconds=60,
                now=gate_time + timedelta(seconds=1),
            )
            self.assertTrue(created)
            reused, created = create_shadow_attestation(
                gate_dir,
                KEY,
                KEY_ID,
                ttl_seconds=60,
                now=gate_time + timedelta(seconds=1),
            )
            self.assertFalse(created)
            self.assertEqual(reused["attestation_id"], attestation["attestation_id"])
            verified = verify_shadow_attestation(
                gate_dir,
                KEY,
                KEY_ID,
                now=gate_time + timedelta(seconds=2),
            )
            self.assertEqual(verified["gate_id"], report["gate_id"])

            with self.assertRaises(DeploymentShadowGateVerificationError):
                verify_shadow_attestation(
                    gate_dir,
                    b"different-shadow-gate-key-at-least-32-bytes",
                    KEY_ID,
                    now=gate_time + timedelta(seconds=2),
                )
            with self.assertRaises(DeploymentShadowGateVerificationError):
                verify_shadow_attestation(
                    gate_dir,
                    KEY,
                    KEY_ID,
                    now=gate_time + timedelta(seconds=62),
                )

            tampered = json.loads(
                (gate_dir / "shadow-gate-report.json").read_text(encoding="utf-8")
            )
            tampered["window"]["history_chain_sha256"] = "b" * 64
            gate_seed = {
                "provider_instance_sha256": tampered["provider_instance_sha256"],
                "policy": tampered["policy"],
                "window": tampered["window"],
                "violations": tampered["violations"],
            }
            tampered["gate_id"] = (
                f"shadow-gate-{_digest(_canonical_json(gate_seed))[:32]}"
            )
            (gate_dir / "shadow-gate-report.json").write_text(
                json.dumps(tampered),
                encoding="utf-8",
            )
            with self.assertRaises(DeploymentShadowGateVerificationError):
                verify_shadow_attestation(
                    gate_dir,
                    KEY,
                    KEY_ID,
                    now=gate_time + timedelta(seconds=2),
                )

    def test_hold_cannot_be_attested_and_runner_records_both_outcomes(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            history = ShadowHistoryLedger(root / "history.sqlite3", PROVIDER_DIGEST, now=NOW)
            held = evaluate_shadow_history(
                history,
                ShadowGatePolicy(
                    minimum_runs=1,
                    minimum_window_seconds=1,
                    minimum_selected_total=1,
                    maximum_gap_seconds=1,
                    maximum_report_age_seconds=1,
                ),
                now=NOW,
            )
            gate_dir = root / "held-gate"
            write_shadow_gate_report(gate_dir, held)
            with self.assertRaises(DeploymentShadowGateVerificationError):
                create_shadow_attestation(gate_dir, KEY, KEY_ID, now=NOW)

            adapter = ReadOnlyHTTPProviderAdapter(
                ReadOnlyHTTPProviderConfig(
                    name="empty-provider",
                    base_url="https://empty.provider.invalid",
                    allowed_host="empty.provider.invalid",
                    provider_instance="test",
                    token="runner-history-test-token-at-least-32-bytes",
                )
            )
            ledger = DeploymentLedger(root / "controller" / "ledger.sqlite3")
            runner_history = ShadowHistoryLedger(
                root / "runner-history.sqlite3",
                adapter.provider_instance_sha256,
                now=NOW,
            )
            state = ShadowRuntimeState(root / "state", adapter, now=NOW)
            ticks = iter((0.0, 0.1))
            success = ShadowRunner(
                lambda now: run_shadow_reconciliation(ledger, adapter, now=now),
                state,
                history=runner_history,
                monotonic=lambda: next(ticks),
            )
            self.assertTrue(success.run_iteration(NOW))

            failure_ticks = iter((1.0, 1.1))

            def fail(_now: datetime) -> dict[str, Any]:
                raise DeploymentShadowError("synthetic failure")

            failure = ShadowRunner(
                fail,
                state,
                history=runner_history,
                monotonic=lambda: next(failure_ticks),
            )
            self.assertFalse(failure.run_iteration(NOW + timedelta(seconds=1)))
            self.assertEqual(
                [event["outcome"] for event in runner_history.events(until=NOW + timedelta(seconds=2))],
                ["success", "error"],
            )

            serialized = json.dumps(runner_history.events(until=NOW + timedelta(seconds=2)))
            self.assertNotIn("runner-history-test-token", serialized)

    def test_evaluate_cli_returns_zero_for_pass_and_three_for_hold(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            current_time = datetime.now(UTC)
            history = ShadowHistoryLedger(
                root / "history.sqlite3",
                PROVIDER_DIGEST,
                now=current_time - timedelta(seconds=60),
            )
            for offset in (-59, -40, -20, -1):
                observed_at = current_time + timedelta(seconds=offset)
                history.record_success(shadow_report(observed_at), 0.1, observed_at)
            output = io.StringIO()
            with redirect_stdout(output):
                result = shadow_gate_main(
                    [
                        "evaluate",
                        "--history-ledger",
                        str(history.path),
                        "--provider-instance-sha256",
                        PROVIDER_DIGEST,
                        "--output-dir",
                        str(root / "passing-gate"),
                        "--minimum-runs",
                        "4",
                        "--minimum-window-seconds",
                        "60",
                        "--minimum-selected-total",
                        "4",
                        "--maximum-gap-seconds",
                        "30",
                        "--maximum-report-age-seconds",
                        "10",
                    ]
                )
            self.assertEqual(result, 0)
            self.assertEqual(json.loads(output.getvalue())["decision"], "pass")

            empty = ShadowHistoryLedger(
                root / "empty.sqlite3",
                "b" * 64,
                now=current_time,
            )
            with redirect_stdout(io.StringIO()):
                result = shadow_gate_main(
                    [
                        "evaluate",
                        "--history-ledger",
                        str(empty.path),
                        "--provider-instance-sha256",
                        "b" * 64,
                        "--output-dir",
                        str(root / "held-cli-gate"),
                        "--minimum-runs",
                        "1",
                        "--minimum-window-seconds",
                        "1",
                        "--minimum-selected-total",
                        "1",
                        "--maximum-gap-seconds",
                        "1",
                        "--maximum-report-age-seconds",
                        "1",
                    ]
                )
            self.assertEqual(result, 3)


if __name__ == "__main__":
    unittest.main()
