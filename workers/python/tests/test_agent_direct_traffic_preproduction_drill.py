from __future__ import annotations

import json
import os
import tempfile
import unittest
from datetime import UTC, datetime, timedelta
from pathlib import Path
from unittest.mock import Mock

from ai_companion_worker.evaluation.direct_traffic_preproduction import (
    HTTPSPreproductionConfig,
    TransportResponse,
)
from ai_companion_worker.evaluation.direct_traffic_preproduction_drill import (
    ATTESTATION_FILE,
    REPORT_FILE,
    DirectTrafficPreproductionDrillVerificationError,
    run_live_read_only_probe,
    run_local_contract_drill,
    verify_attested_drill_bundle,
    write_attested_drill_bundle,
)
from ai_companion_worker.evaluation.direct_traffic_preproduction_sandbox import (
    SQLitePreproductionPlatform,
)
from ai_companion_worker.evaluation.release_gate_history import environment_sha256


ROOT = Path(__file__).resolve().parents[3]
POLICY_PATH = ROOT / "evals" / "agent" / "baselines" / "direct-rollout.v1.json"
NOW = datetime(2026, 8, 13, 8, 0, tzinfo=UTC)
VERIFY_NOW = NOW + timedelta(seconds=919)
KEY = b"preproduction-drill-test-key-at-least-32-bytes"
KEY_ID = "preproduction-drill-test-v1"


class AgentDirectTrafficPreproductionDrillTest(unittest.TestCase):
    def test_local_drill_covers_expand_replay_rollback_and_recovery(self) -> None:
        report = run_local_contract_drill(POLICY_PATH, now=NOW)

        self.assertEqual(report["decision"], "pass")
        self.assertEqual(report["mode"], "local-contract-simulation")
        self.assertFalse(report["contains_live_provider_evidence"])
        scenarios = {item["name"]: item["evidence"] for item in report["scenarios"]}
        self.assertEqual(scenarios["expand_timeout_reconciled"]["outcome"], "applied")
        self.assertEqual(scenarios["idempotent_replay"]["provider_put_calls"], 1)
        self.assertTrue(scenarios["idempotent_replay"]["remote_reused"])
        self.assertEqual(
            scenarios["repair_regression_rollback"]["current_traffic_percent"], 5
        )
        self.assertEqual(
            scenarios["offline_write_indeterminate"]["provider_put_calls"], 2
        )
        self.assertEqual(scenarios["offline_recovery_same_key"]["operations"], 3)
        self.assertEqual(report["final_state"]["current_traffic_percent"], 10)

    def test_attested_bundle_detects_report_and_signature_tampering(self) -> None:
        report = run_local_contract_drill(POLICY_PATH, now=NOW)
        with tempfile.TemporaryDirectory() as directory:
            bundle = write_attested_drill_bundle(
                Path(directory) / "evidence",
                report,
                KEY,
                KEY_ID,
                now=NOW + timedelta(seconds=918),
            )
            verified = verify_attested_drill_bundle(
                bundle,
                KEY,
                KEY_ID,
                require_mode="local-contract-simulation",
                now=VERIFY_NOW,
            )
            self.assertEqual(verified, report)
            self.assertEqual(oct((bundle / REPORT_FILE).stat().st_mode & 0o777), "0o600")

            report_path = bundle / REPORT_FILE
            tampered = json.loads(report_path.read_text(encoding="utf-8"))
            tampered["final_state"]["revision"] += 1
            report_path.write_text(json.dumps(tampered), encoding="utf-8")
            os.chmod(report_path, 0o600)
            with self.assertRaises(DirectTrafficPreproductionDrillVerificationError):
                verify_attested_drill_bundle(
                    bundle,
                    KEY,
                    KEY_ID,
                    now=VERIFY_NOW,
                )

    def test_bundle_rejects_stale_and_wrong_mode_evidence(self) -> None:
        report = run_local_contract_drill(POLICY_PATH, now=NOW)
        with tempfile.TemporaryDirectory() as directory:
            bundle = write_attested_drill_bundle(
                Path(directory) / "evidence",
                report,
                KEY,
                KEY_ID,
                ttl_seconds=3600,
                now=NOW + timedelta(seconds=918),
            )
            with self.assertRaisesRegex(
                DirectTrafficPreproductionDrillVerificationError, "mode"
            ):
                verify_attested_drill_bundle(
                    bundle,
                    KEY,
                    KEY_ID,
                    require_mode="live-read-only-probe",
                    now=VERIFY_NOW,
                )
            with self.assertRaisesRegex(
                DirectTrafficPreproductionDrillVerificationError, "stale"
            ):
                verify_attested_drill_bundle(
                    bundle,
                    KEY,
                    KEY_ID,
                    now=NOW + timedelta(seconds=5000),
                )

    def test_live_probe_is_read_only_and_uses_default_https_transport_shape(self) -> None:
        environment_digest = environment_sha256("preproduction-probe-cn")
        provider_instance = "platform-preproduction-probe"
        namespace_id = "preproduction-agent-direct-probe"
        token = "preproduction-probe-token-at-least-32-bytes"
        base_url = "https://traffic.provider.example/api"
        rollout_key = b"probe-rollout-key-at-least-32-bytes"
        certification_key = b"probe-certification-key-at-least-32-bytes"
        shadow_key = b"probe-shadow-key-at-least-32-bytes"
        with tempfile.TemporaryDirectory() as directory:
            platform = SQLitePreproductionPlatform(
                Path(directory) / "platform.sqlite3",
                environment_digest,
                provider_instance,
                namespace_id,
                token,
                rollout_key,
                "probe-rollout-v1",
                certification_key,
                "probe-certification-v1",
                shadow_key,
                "probe-shadow-v1",
                initial_traffic_percent=5,
                base_url=base_url,
                now=lambda: NOW,
            )
            methods: list[str] = []

            def transport(
                method: str,
                url: str,
                supplied_token: str,
                timeout_seconds: float,
                max_response_bytes: int,
                body: bytes | None,
            ) -> TransportResponse:
                methods.append(method)
                return platform.transport(  # type: ignore[arg-type]
                    method,  # type: ignore[arg-type]
                    url,
                    supplied_token,
                    timeout_seconds,
                    max_response_bytes,
                    body,
                )

            report = run_live_read_only_probe(
                HTTPSPreproductionConfig(
                    base_url=base_url,
                    allowed_host="traffic.provider.example",
                    provider_instance=provider_instance,
                    environment_sha256=environment_digest,
                    namespace_id=namespace_id,
                    token=token,
                ),
                transport=transport,  # type: ignore[arg-type]
                now=NOW,
            )

        self.assertEqual(methods, ["GET", "GET"])
        self.assertEqual(platform.write_calls, 0)
        self.assertEqual(report["mode"], "live-read-only-probe")
        self.assertTrue(report["contains_live_provider_evidence"])

    def test_live_probe_rejects_redirected_response(self) -> None:
        config = HTTPSPreproductionConfig(
            base_url="https://traffic.provider.example/api",
            allowed_host="traffic.provider.example",
            provider_instance="platform-preproduction-probe",
            environment_sha256=environment_sha256("preproduction-probe-cn"),
            namespace_id="preproduction-agent-direct-probe",
            token="preproduction-probe-token-at-least-32-bytes",
        )
        transport = Mock(
            return_value=TransportResponse(
                200,
                b"{}",
                "application/json",
                "https://redirected.example/state",
            )
        )
        with self.assertRaisesRegex(Exception, "redirect"):
            run_live_read_only_probe(config, transport=transport, now=NOW)
        self.assertEqual(transport.call_count, 1)

    def test_bundle_rejects_wrong_mode_and_unexpected_artifact(self) -> None:
        report = run_local_contract_drill(POLICY_PATH, now=NOW)
        with tempfile.TemporaryDirectory() as directory:
            bundle = write_attested_drill_bundle(
                Path(directory) / "evidence",
                report,
                KEY,
                KEY_ID,
                now=NOW + timedelta(seconds=918),
            )
            (bundle / "unexpected.txt").write_text("unexpected", encoding="utf-8")
            os.chmod(bundle / "unexpected.txt", 0o600)
            with self.assertRaisesRegex(
                DirectTrafficPreproductionDrillVerificationError, "artifact set"
            ):
                verify_attested_drill_bundle(
                    bundle,
                    KEY,
                    KEY_ID,
                    require_mode="live-read-only-probe",
                    now=VERIFY_NOW,
                )
            self.assertTrue((bundle / ATTESTATION_FILE).is_file())


if __name__ == "__main__":
    unittest.main()
