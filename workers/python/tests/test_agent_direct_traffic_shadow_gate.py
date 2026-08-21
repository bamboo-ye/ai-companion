from __future__ import annotations

import json
import tempfile
import unittest
from concurrent.futures import ThreadPoolExecutor
from contextlib import redirect_stdout
from datetime import UTC, datetime, timedelta
from io import StringIO
from pathlib import Path
from typing import Any, Literal
from unittest.mock import patch

from ai_companion_worker.evaluation.deployment_controller import (
    _canonical_json,
    _digest,
    _format_timestamp,
)
from ai_companion_worker.evaluation.direct_rollout_traffic import (
    provider_instance_sha256,
)
from ai_companion_worker.evaluation.direct_traffic_shadow import (
    DirectTrafficShadowHistory,
    ReadOnlyDirectTrafficProviderConfig,
    observer_instance_sha256,
)
from ai_companion_worker.evaluation.direct_traffic_shadow_gate import (
    DirectTrafficShadowGatePolicy,
    DirectTrafficShadowGateVerificationError,
    create_direct_traffic_shadow_attestation,
    evaluate_direct_traffic_shadow_history,
    main,
    verify_direct_traffic_shadow_attestation,
    write_direct_traffic_shadow_gate_report,
)
from ai_companion_worker.evaluation.release_gate_history import environment_sha256


NOW = datetime(2026, 8, 12, 8, 0, tzinfo=UTC)
ENVIRONMENT_ID = "staging-cn"
ENVIRONMENT_SHA256 = environment_sha256(ENVIRONMENT_ID)
PROVIDER_INSTANCE = "platform-preprod-blue"
PROVIDER_SHA256 = provider_instance_sha256(PROVIDER_INSTANCE)
KEY = b"direct-traffic-shadow-gate-test-key-at-least-32-bytes"
KEY_ID = "direct-traffic-shadow-gate-test-v1"


def config() -> ReadOnlyDirectTrafficProviderConfig:
    return ReadOnlyDirectTrafficProviderConfig(
        name="preprod-platform-reader",
        base_url="https://traffic.provider.example/api",
        allowed_host="traffic.provider.example",
        provider_instance=PROVIDER_INSTANCE,
        environment_sha256=ENVIRONMENT_SHA256,
        token="direct-traffic-shadow-history-binding-token",
    )


def report(
    observed_at: datetime,
    *,
    outcome: Literal["match", "drift", "lookup_error"] = "match",
    attempts: int = 1,
) -> dict[str, Any]:
    provider = {
        "current_traffic_percent": 5,
        "revision": 0,
        "history_chain_sha256": "0" * 64,
        "observed_at": _format_timestamp(observed_at),
    }
    drift_fields: list[str] = []
    error_code = None
    if outcome == "drift":
        provider["current_traffic_percent"] = 10
        drift_fields = ["current_traffic_percent"]
    elif outcome == "lookup_error":
        provider = None  # type: ignore[assignment]
        error_code = "provider_http_503"
    return {
        "schema_version": "agent-direct-traffic-shadow-report-v1",
        "generated_at": _format_timestamp(observed_at),
        "observer_instance_sha256": observer_instance_sha256(config()),
        "provider_instance_sha256": PROVIDER_SHA256,
        "environment_sha256": ENVIRONMENT_SHA256,
        "outcome": outcome,
        "drift_fields": drift_fields,
        "attempts": attempts,
        "duration_seconds": 0.1,
        "error_code": error_code,
        "local": {
            "current_traffic_percent": 5,
            "revision": 0,
            "history_chain_sha256": "0" * 64,
        },
        "provider": provider,
    }


def history_at(root: Path) -> DirectTrafficShadowHistory:
    return DirectTrafficShadowHistory(
        root / "history.sqlite3",
        observer_instance_sha256(config()),
        PROVIDER_SHA256,
        ENVIRONMENT_SHA256,
        now=NOW,
    )


def passing_history(
    root: Path,
) -> tuple[DirectTrafficShadowHistory, DirectTrafficShadowGatePolicy]:
    history = history_at(root)
    for offset in (0, 20, 40, 60):
        history.record(report(NOW + timedelta(seconds=offset)))
    policy = DirectTrafficShadowGatePolicy(
        fast_window_runs=2,
        stable_window_runs=4,
        minimum_stable_window_seconds=60,
        maximum_gap_seconds=25,
        maximum_report_age_seconds=10,
        maximum_retried_observations=1,
    )
    return history, policy


class AgentDirectTrafficShadowGateTest(unittest.TestCase):
    def test_two_healthy_windows_pass_and_bind_full_live_history(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            history, policy = passing_history(root)
            gate = evaluate_direct_traffic_shadow_history(
                history, policy, now=NOW + timedelta(seconds=65)
            )

            self.assertEqual(gate["decision"], "pass")
            self.assertEqual(gate["violations"], [])
            self.assertEqual(gate["windows"]["fast"]["selected"], 2)
            self.assertEqual(gate["windows"]["stable"]["selected"], 4)
            self.assertEqual(gate["windows"]["stable"]["coverage_seconds"], 60.0)
            status = history.status()
            self.assertEqual(gate["source"]["history_events"], status["events"])
            self.assertEqual(
                gate["source"]["history_chain_sha256"], status["chain_sha256"]
            )
            path = write_direct_traffic_shadow_gate_report(root / "gate", gate)
            self.assertEqual(path.stat().st_mode & 0o777, 0o600)
            self.assertEqual(path.parent.stat().st_mode & 0o777, 0o700)

    def test_recent_drift_and_lookup_error_block_fast_and_stable_windows(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            history = history_at(root)
            history.record(report(NOW))
            history.record(report(NOW + timedelta(seconds=20)))
            history.record(report(NOW + timedelta(seconds=40), outcome="drift"))
            history.record(
                report(NOW + timedelta(seconds=60), outcome="lookup_error", attempts=2)
            )
            gate = evaluate_direct_traffic_shadow_history(
                history,
                DirectTrafficShadowGatePolicy(
                    fast_window_runs=2,
                    stable_window_runs=4,
                    minimum_stable_window_seconds=60,
                    maximum_gap_seconds=25,
                    maximum_report_age_seconds=10,
                    maximum_retried_observations=0,
                ),
                now=NOW + timedelta(seconds=65),
            )
            codes = {item["code"] for item in gate["violations"]}
            self.assertEqual(gate["decision"], "hold")
            self.assertTrue(
                {
                    "recent_drift",
                    "recent_lookup_errors",
                    "stable_drift",
                    "stable_lookup_errors",
                    "excessive_retries",
                }.issubset(codes)
            )

    def test_insufficient_gapped_stale_and_future_evidence_is_held(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            history = history_at(root)
            history.record(report(NOW))
            history.record(report(NOW + timedelta(seconds=100)))
            gate = evaluate_direct_traffic_shadow_history(
                history,
                DirectTrafficShadowGatePolicy(
                    fast_window_runs=2,
                    stable_window_runs=4,
                    minimum_stable_window_seconds=120,
                    maximum_gap_seconds=30,
                    maximum_report_age_seconds=5,
                ),
                now=NOW + timedelta(seconds=120),
            )
            codes = {item["code"] for item in gate["violations"]}
            self.assertTrue(
                {
                    "insufficient_stable_runs",
                    "insufficient_stable_window",
                    "observation_gap",
                    "observation_stale",
                }.issubset(codes)
            )

            future = history_at(root / "future")
            future.record(report(NOW + timedelta(seconds=10)))
            future.record(report(NOW + timedelta(seconds=20)))
            future_gate = evaluate_direct_traffic_shadow_history(
                future,
                DirectTrafficShadowGatePolicy(
                    fast_window_runs=1,
                    stable_window_runs=2,
                    minimum_stable_window_seconds=10,
                    maximum_gap_seconds=10,
                    maximum_retried_observations=2,
                ),
                now=NOW,
            )
            self.assertIn(
                "future_observation",
                {item["code"] for item in future_gate["violations"]},
            )

            unordered = history_at(root / "unordered")
            unordered.record(report(NOW + timedelta(seconds=20)))
            unordered.record(report(NOW + timedelta(seconds=10)))
            unordered_gate = evaluate_direct_traffic_shadow_history(
                unordered,
                DirectTrafficShadowGatePolicy(
                    fast_window_runs=1,
                    stable_window_runs=2,
                    minimum_stable_window_seconds=1,
                    maximum_gap_seconds=1,
                    maximum_retried_observations=2,
                ),
                now=NOW + timedelta(seconds=21),
            )
            self.assertIn(
                "non_monotonic_observations",
                {item["code"] for item in unordered_gate["violations"]},
            )

    def test_attestation_is_idempotent_fresh_signed_and_history_bound(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            history, policy = passing_history(root)
            gate_time = NOW + timedelta(seconds=65)
            gate = evaluate_direct_traffic_shadow_history(
                history, policy, now=gate_time
            )
            gate_dir = root / "gate"
            write_direct_traffic_shadow_gate_report(gate_dir, gate)

            attestation, created = create_direct_traffic_shadow_attestation(
                gate_dir,
                history,
                KEY,
                KEY_ID,
                ttl_seconds=60,
                now=gate_time + timedelta(seconds=1),
            )
            self.assertTrue(created)
            reused, created = create_direct_traffic_shadow_attestation(
                gate_dir,
                history,
                KEY,
                KEY_ID,
                ttl_seconds=60,
                now=gate_time + timedelta(seconds=1),
            )
            self.assertFalse(created)
            self.assertEqual(reused["attestation_id"], attestation["attestation_id"])
            verified = verify_direct_traffic_shadow_attestation(
                gate_dir,
                history,
                KEY,
                KEY_ID,
                now=gate_time + timedelta(seconds=2),
            )
            self.assertEqual(verified["gate_id"], gate["gate_id"])

            with self.assertRaises(DirectTrafficShadowGateVerificationError):
                verify_direct_traffic_shadow_attestation(
                    gate_dir,
                    history,
                    b"wrong-direct-traffic-shadow-gate-key-at-least-32-bytes",
                    KEY_ID,
                    now=gate_time + timedelta(seconds=2),
                )
            with self.assertRaises(DirectTrafficShadowGateVerificationError):
                verify_direct_traffic_shadow_attestation(
                    gate_dir,
                    history,
                    KEY,
                    KEY_ID,
                    now=gate_time + timedelta(seconds=62),
                )
            with self.assertRaisesRegex(
                DirectTrafficShadowGateVerificationError, "too old to attest"
            ):
                create_direct_traffic_shadow_attestation(
                    gate_dir,
                    history,
                    KEY,
                    KEY_ID,
                    max_gate_age_seconds=1,
                    now=gate_time + timedelta(seconds=2),
                )

    def test_new_observation_immediately_invalidates_signed_gate(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            history, policy = passing_history(root)
            gate_time = NOW + timedelta(seconds=65)
            gate_dir = root / "gate"
            write_direct_traffic_shadow_gate_report(
                gate_dir,
                evaluate_direct_traffic_shadow_history(
                    history, policy, now=gate_time
                ),
            )
            create_direct_traffic_shadow_attestation(
                gate_dir,
                history,
                KEY,
                KEY_ID,
                now=gate_time + timedelta(seconds=1),
            )
            history.record(report(NOW + timedelta(seconds=70)))
            with self.assertRaisesRegex(
                DirectTrafficShadowGateVerificationError, "invalidated by current history"
            ):
                verify_direct_traffic_shadow_attestation(
                    gate_dir,
                    history,
                    KEY,
                    KEY_ID,
                    now=NOW + timedelta(seconds=71),
                )

    def test_concurrent_signers_converge_on_one_attestation(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            history, policy = passing_history(root)
            gate_time = NOW + timedelta(seconds=65)
            gate_dir = root / "gate"
            write_direct_traffic_shadow_gate_report(
                gate_dir,
                evaluate_direct_traffic_shadow_history(
                    history, policy, now=gate_time
                ),
            )

            def sign(_index: int) -> tuple[str, bool]:
                result, created = create_direct_traffic_shadow_attestation(
                    gate_dir,
                    history,
                    KEY,
                    KEY_ID,
                    now=gate_time + timedelta(seconds=1),
                )
                return str(result["attestation_id"]), created

            with ThreadPoolExecutor(max_workers=8) as executor:
                results = list(executor.map(sign, range(8)))
            self.assertEqual(len({item[0] for item in results}), 1)
            self.assertEqual(sum(item[1] for item in results), 1)
            self.assertEqual(
                {path.name for path in gate_dir.iterdir()},
                {
                    "direct-traffic-shadow-gate-report.json",
                    "direct-traffic-shadow-attestation.json",
                },
            )

    def test_hold_cannot_be_attested_and_report_tampering_is_detected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            history = history_at(root)
            held = evaluate_direct_traffic_shadow_history(
                history,
                DirectTrafficShadowGatePolicy(
                    fast_window_runs=1,
                    stable_window_runs=2,
                    minimum_stable_window_seconds=1,
                    maximum_gap_seconds=1,
                    maximum_retried_observations=2,
                ),
                now=NOW,
            )
            held_dir = root / "held"
            write_direct_traffic_shadow_gate_report(held_dir, held)
            with self.assertRaises(DirectTrafficShadowGateVerificationError):
                create_direct_traffic_shadow_attestation(
                    held_dir, history, KEY, KEY_ID, now=NOW
                )

            passing, policy = passing_history(root / "passing")
            gate_dir = root / "passing-gate"
            gate = evaluate_direct_traffic_shadow_history(
                passing, policy, now=NOW + timedelta(seconds=65)
            )
            write_direct_traffic_shadow_gate_report(gate_dir, gate)
            create_direct_traffic_shadow_attestation(
                gate_dir,
                passing,
                KEY,
                KEY_ID,
                now=NOW + timedelta(seconds=66),
            )
            path = gate_dir / "direct-traffic-shadow-gate-report.json"
            tampered = json.loads(path.read_text(encoding="utf-8"))
            tampered["windows"]["stable"]["match"] = 3
            tampered["windows"]["stable"]["drift"] = 1
            path.write_text(json.dumps(tampered), encoding="utf-8")
            with self.assertRaises(DirectTrafficShadowGateVerificationError):
                verify_direct_traffic_shadow_attestation(
                    gate_dir,
                    passing,
                    KEY,
                    KEY_ID,
                    now=NOW + timedelta(seconds=67),
                )

    def test_signer_replays_gate_and_rejects_recomputed_forged_pass(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            history = history_at(root)
            held = evaluate_direct_traffic_shadow_history(
                history,
                DirectTrafficShadowGatePolicy(
                    fast_window_runs=1,
                    stable_window_runs=2,
                    minimum_stable_window_seconds=1,
                    maximum_gap_seconds=1,
                    maximum_retried_observations=2,
                ),
                now=NOW,
            )
            forged = dict(held)
            forged["decision"] = "pass"
            forged["violations"] = []
            seed = {
                "generated_at": forged["generated_at"],
                "source": forged["source"],
                "policy": forged["policy"],
                "windows": forged["windows"],
                "violations": forged["violations"],
            }
            forged["gate_id"] = (
                "direct-traffic-shadow-gate-"
                f"{_digest(_canonical_json(seed))[:32]}"
            )
            gate_dir = root / "forged"
            write_direct_traffic_shadow_gate_report(gate_dir, forged)
            with self.assertRaisesRegex(
                DirectTrafficShadowGateVerificationError,
                "evaluation does not match current history",
            ):
                create_direct_traffic_shadow_attestation(
                    gate_dir,
                    history,
                    KEY,
                    KEY_ID,
                    now=NOW,
                )

    def test_cli_evaluate_attest_and_verify_are_separate_and_chain_bound(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            current_time = datetime.now(UTC)
            history = DirectTrafficShadowHistory(
                root / "history.sqlite3",
                observer_instance_sha256(config()),
                PROVIDER_SHA256,
                ENVIRONMENT_SHA256,
                now=current_time - timedelta(seconds=61),
            )
            for offset in (-60, -40, -20, -1):
                history.record(report(current_time + timedelta(seconds=offset)))
            common = [
                "--history-ledger",
                str(history.path),
                "--environment-id",
                ENVIRONMENT_ID,
                "--provider-name",
                "preprod-platform-reader",
                "--provider-base-url",
                "https://traffic.provider.example/api",
                "--allowed-host",
                "traffic.provider.example",
                "--provider-instance",
                PROVIDER_INSTANCE,
                "--canary-lease-root",
                str(root / "leases"),
            ]
            gate_dir = root / "gate"
            output = StringIO()
            with redirect_stdout(output):
                code = main(
                    [
                        "evaluate",
                        *common,
                        "--output-dir",
                        str(gate_dir),
                        "--fast-window-runs",
                        "2",
                        "--stable-window-runs",
                        "4",
                        "--minimum-stable-window-seconds",
                        "59",
                        "--maximum-gap-seconds",
                        "21",
                        "--maximum-report-age-seconds",
                        "5",
                        "--maximum-retried-observations",
                        "1",
                    ]
                )
            self.assertEqual(code, 0)
            self.assertEqual(json.loads(output.getvalue())["decision"], "pass")

            signing_env = {
                "OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_GATE_KEY": KEY.decode("utf-8"),
                "OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_GATE_KEY_ID": KEY_ID,
            }
            output = StringIO()
            with patch.dict("os.environ", signing_env, clear=False), redirect_stdout(output):
                code = main(
                    [
                        "attest",
                        *common,
                        "--gate-dir",
                        str(gate_dir),
                        "--ttl-seconds",
                        "60",
                    ]
                )
            self.assertEqual(code, 0)
            self.assertEqual(json.loads(output.getvalue())["status"], "created")

            output = StringIO()
            with patch.dict("os.environ", signing_env, clear=False), redirect_stdout(output):
                code = main(
                    [
                        "verify",
                        *common,
                        "--gate-dir",
                        str(gate_dir),
                        "--max-age-seconds",
                        "60",
                    ]
                )
            self.assertEqual(code, 0)
            self.assertEqual(json.loads(output.getvalue())["status"], "verified")


if __name__ == "__main__":
    unittest.main()
