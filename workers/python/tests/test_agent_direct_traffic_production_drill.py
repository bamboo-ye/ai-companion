from __future__ import annotations

import json
import os
import shutil
import tempfile
import unittest
from datetime import UTC, datetime, timedelta
from pathlib import Path

from ai_companion_worker.evaluation.direct_traffic_production_drill import (
    ATTESTATION_FILE,
    REPORT_FILE,
    DirectTrafficProductionDrillVerificationError,
    run_local_production_drill,
    verify_attested_production_drill_bundle,
    write_attested_production_drill_bundle,
)


ROOT = Path(__file__).resolve().parents[3]
NOW = datetime(2026, 8, 14, 8, 0, tzinfo=UTC)
KEY = b"production-drill-test-key-at-least-32-bytes"
KEY_ID = "production-drill-test-v1"


class AgentDirectTrafficProductionDrillTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.report = run_local_production_drill(ROOT, now=NOW)

    def test_full_local_production_chain_is_exact_and_terminal(self) -> None:
        report = self.report
        self.assertEqual(report["decision"], "pass")
        self.assertEqual(report["mode"], "local-production-integration")
        self.assertFalse(report["contains_live_provider_evidence"])
        self.assertGreaterEqual(report["suite"]["tests_run"], 6)
        self.assertEqual(
            [item["target_traffic_percent"] for item in report["transitions"]],
            [5, 0, 5, 10, 25, 50, 100],
        )
        self.assertEqual(report["terminal"]["current_traffic_percent"], 100)
        self.assertEqual(report["terminal"]["revision"], 7)
        self.assertEqual(report["terminal"]["operations"], 7)
        self.assertEqual(report["terminal"]["rollout_reason"], "maximum_stage_reached")

    def test_attested_bundle_verifies_against_current_sources(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            bundle = write_attested_production_drill_bundle(
                Path(directory) / "evidence",
                self.report,
                KEY,
                KEY_ID,
                now=NOW,
            )
            verified = verify_attested_production_drill_bundle(
                bundle,
                KEY,
                KEY_ID,
                repository_root=ROOT,
                now=NOW + timedelta(seconds=1),
            )
            self.assertEqual(verified, self.report)
            self.assertEqual(oct((bundle / REPORT_FILE).stat().st_mode & 0o777), "0o600")
            self.assertEqual(
                oct((bundle / ATTESTATION_FILE).stat().st_mode & 0o777), "0o600"
            )

    def test_report_tampering_and_unexpected_artifact_fail_closed(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            bundle = write_attested_production_drill_bundle(
                Path(directory) / "evidence",
                self.report,
                KEY,
                KEY_ID,
                now=NOW,
            )
            report_path = bundle / REPORT_FILE
            tampered = json.loads(report_path.read_text(encoding="utf-8"))
            tampered["terminal"]["revision"] = 8
            report_path.write_text(json.dumps(tampered), encoding="utf-8")
            os.chmod(report_path, 0o600)
            with self.assertRaises(DirectTrafficProductionDrillVerificationError):
                verify_attested_production_drill_bundle(
                    bundle, KEY, KEY_ID, now=NOW + timedelta(seconds=1)
                )

        with tempfile.TemporaryDirectory() as directory:
            bundle = write_attested_production_drill_bundle(
                Path(directory) / "evidence",
                self.report,
                KEY,
                KEY_ID,
                now=NOW,
            )
            unexpected = bundle / "unexpected.txt"
            unexpected.write_text("unexpected", encoding="utf-8")
            os.chmod(unexpected, 0o600)
            with self.assertRaisesRegex(
                DirectTrafficProductionDrillVerificationError, "artifact set"
            ):
                verify_attested_production_drill_bundle(
                    bundle, KEY, KEY_ID, now=NOW + timedelta(seconds=1)
                )

    def test_stale_or_source_drifted_bundle_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            bundle = write_attested_production_drill_bundle(
                Path(directory) / "evidence",
                self.report,
                KEY,
                KEY_ID,
                ttl_seconds=60,
                now=NOW,
            )
            with self.assertRaisesRegex(
                DirectTrafficProductionDrillVerificationError, "stale"
            ):
                verify_attested_production_drill_bundle(
                    bundle, KEY, KEY_ID, now=NOW + timedelta(seconds=61)
                )

        with tempfile.TemporaryDirectory() as directory:
            fake_root = Path(directory) / "repository"
            for source in self.report["sources"]:
                relative = Path(source["path"])
                target = fake_root / relative
                target.parent.mkdir(parents=True, exist_ok=True)
                shutil.copyfile(ROOT / relative, target)
            drifted = fake_root / self.report["sources"][0]["path"]
            drifted.write_bytes(drifted.read_bytes() + b"\n")
            bundle = write_attested_production_drill_bundle(
                Path(directory) / "evidence",
                self.report,
                KEY,
                KEY_ID,
                now=NOW,
            )
            with self.assertRaisesRegex(
                DirectTrafficProductionDrillVerificationError, "sources"
            ):
                verify_attested_production_drill_bundle(
                    bundle,
                    KEY,
                    KEY_ID,
                    repository_root=fake_root,
                    now=NOW + timedelta(seconds=1),
                )


if __name__ == "__main__":
    unittest.main()
