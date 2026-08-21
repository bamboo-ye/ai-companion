from __future__ import annotations

import hashlib
import json
import os
import sqlite3
import tempfile
import unittest
from concurrent.futures import ThreadPoolExecutor
from datetime import UTC, datetime, timedelta
from pathlib import Path
from typing import Literal

from ai_companion_worker.evaluation.deployment_controller import (
    DeploymentBusyError,
    DeploymentLedger,
)
from ai_companion_worker.evaluation.deployment_resolution import (
    DeploymentResolutionConfigError,
    DeploymentResolutionError,
    apply_manual_resolution,
    approve_resolution_request,
    check_manual_resolution,
    create_resolution_evidence,
    issue_resolution_request,
)


REQUEST_KEY = b"manual-resolution-request-key-32-bytes-minimum"
REQUEST_KEY_ID = "manual-resolution-request-v1"
APPROVAL_KEY = b"manual-resolution-approval-key-32-bytes-minimum"
APPROVAL_KEY_ID = "manual-resolution-approval-v1"
PROVIDER_EVIDENCE_SHA256 = "a" * 64


def digest(value: str) -> str:
    return hashlib.sha256(value.encode("utf-8")).hexdigest()


def make_indeterminate_ledger(
    root: Path,
    deployment_id: str = "deploy-manual-resolution",
    *,
    external_operation_id: str | None = "op-manual-resolution",
) -> tuple[DeploymentLedger, datetime]:
    ledger_path = root / "controller" / "ledger.sqlite3"
    ledger = DeploymentLedger(ledger_path)
    now = datetime(2026, 8, 11, 1, 0, tzinfo=UTC)
    timestamp = now.isoformat().replace("+00:00", "Z")
    connection = sqlite3.connect(ledger_path)
    connection.execute("PRAGMA foreign_keys = ON")
    connection.execute(
        """
        INSERT INTO deployment_operations (
            deployment_id, authorization_id, idempotency_key, run_id,
            target, adapter, authorization_sha256, gate_report_sha256,
            request_sha256, status, attempt_count, reconciliation_count,
            lease_token, lease_expires_at, external_operation_id, error_code,
            created_at, updated_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'indeterminate', 1, 2,
                  NULL, NULL, ?, 'lookup_miss_after_accept', ?, ?)
        """,
        (
            deployment_id,
            f"auth-{digest(deployment_id)[:32]}",
            digest(f"idempotency:{deployment_id}"),
            f"run-{digest(deployment_id)[:16]}",
            "production-canary",
            "async-adapter",
            digest(f"authorization:{deployment_id}"),
            digest(f"gate:{deployment_id}"),
            digest(f"request:{deployment_id}"),
            external_operation_id,
            timestamp,
            timestamp,
        ),
    )
    connection.execute(
        """
        INSERT INTO deployment_events (
            deployment_id, sequence, event_type, status, attempt, occurred_at
        ) VALUES (?, 1, 'reconciliation_indeterminate', 'indeterminate', 2, ?)
        """,
        (deployment_id, timestamp),
    )
    connection.commit()
    connection.close()
    return ledger, now + timedelta(seconds=1)


def sign_resolution(
    ledger: DeploymentLedger,
    state_dir: Path,
    now: datetime,
    *,
    deployment_id: str = "deploy-manual-resolution",
    proposed_status: Literal["completed", "failed"] = "completed",
    reason_code: str = "provider_confirmed_completed",
    requested_by: str = "operator-requester",
    approved_by: str = "operator-approver",
    provider_evidence_sha256: str = PROVIDER_EVIDENCE_SHA256,
    request_ttl: int = 300,
    approval_ttl: int = 180,
) -> tuple[Path, Path, Path]:
    _evidence, evidence_path, _created = create_resolution_evidence(
        ledger,
        state_dir,
        deployment_id,
        proposed_status,
        reason_code,
        requested_by,
        provider_evidence_sha256,
        now=now,
    )
    _request, request_path, _created = issue_resolution_request(
        ledger,
        evidence_path,
        state_dir,
        REQUEST_KEY,
        REQUEST_KEY_ID,
        ttl_seconds=request_ttl,
        now=now + timedelta(seconds=1),
    )
    _approval, approval_path, _created = approve_resolution_request(
        request_path,
        state_dir,
        APPROVAL_KEY,
        APPROVAL_KEY_ID,
        approved_by,
        ttl_seconds=approval_ttl,
        now=now + timedelta(seconds=2),
    )
    return evidence_path, request_path, approval_path


class ObservabilityDeploymentResolutionTest(unittest.TestCase):
    def test_dual_signed_completed_resolution_is_atomic_and_idempotent(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            ledger, now = make_indeterminate_ledger(root)
            state_dir = root / "resolutions"
            evidence_path, request_path, approval_path = sign_resolution(
                ledger,
                state_dir,
                now,
            )
            _request, repeated_request_path, request_created = issue_resolution_request(
                ledger,
                evidence_path,
                state_dir,
                REQUEST_KEY,
                REQUEST_KEY_ID,
                now=now + timedelta(seconds=2),
            )
            _approval, repeated_approval_path, approval_created = (
                approve_resolution_request(
                    request_path,
                    state_dir,
                    APPROVAL_KEY,
                    APPROVAL_KEY_ID,
                    "operator-approver",
                    now=now + timedelta(seconds=3),
                )
            )
            self.assertFalse(request_created)
            self.assertFalse(approval_created)
            self.assertEqual(repeated_request_path, request_path)
            self.assertEqual(repeated_approval_path, approval_path)
            resolution, status_value = check_manual_resolution(
                ledger,
                evidence_path,
                request_path,
                approval_path,
                REQUEST_KEY,
                REQUEST_KEY_ID,
                APPROVAL_KEY,
                APPROVAL_KEY_ID,
                now=now + timedelta(seconds=4),
            )
            self.assertEqual(status_value, "ready")
            self.assertEqual(resolution.proposed_status, "completed")

            operation, applied = apply_manual_resolution(
                ledger,
                evidence_path,
                request_path,
                approval_path,
                REQUEST_KEY,
                REQUEST_KEY_ID,
                APPROVAL_KEY,
                APPROVAL_KEY_ID,
                now=now + timedelta(seconds=4),
            )
            repeated, repeated_applied = apply_manual_resolution(
                ledger,
                evidence_path,
                request_path,
                approval_path,
                REQUEST_KEY,
                REQUEST_KEY_ID,
                APPROVAL_KEY,
                APPROVAL_KEY_ID,
                now=now + timedelta(seconds=5),
            )
            self.assertTrue(applied)
            self.assertFalse(repeated_applied)
            self.assertEqual(operation, repeated)
            self.assertEqual(operation["status"], "completed")
            self.assertIsNone(operation["error_code"])
            audit = ledger.resolution("deploy-manual-resolution")
            self.assertEqual(audit["resolution_id"], resolution.resolution_id)
            self.assertEqual(audit["requested_by"], "operator-requester")
            self.assertEqual(audit["approved_by"], "operator-approver")
            self.assertEqual(
                ledger.events("deploy-manual-resolution")[-1]["event_type"],
                "manual_resolution_completed",
            )
            self.assertEqual(state_dir.stat().st_mode & 0o777, 0o700)
            for path in (evidence_path, request_path, approval_path):
                self.assertEqual(path.stat().st_mode & 0o777, 0o600)

    def test_concurrent_consumption_changes_state_once(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            ledger, now = make_indeterminate_ledger(root)
            artifacts = sign_resolution(ledger, root / "resolutions", now)

            def apply() -> bool:
                _operation, created = apply_manual_resolution(
                    ledger,
                    *artifacts,
                    REQUEST_KEY,
                    REQUEST_KEY_ID,
                    APPROVAL_KEY,
                    APPROVAL_KEY_ID,
                    now=now + timedelta(seconds=3),
                )
                return created

            with ThreadPoolExecutor(max_workers=8) as executor:
                results = list(executor.map(lambda _index: apply(), range(16)))

            self.assertEqual(sum(results), 1)
            self.assertEqual(ledger.get("deploy-manual-resolution")["status"], "completed")
            connection = sqlite3.connect(root / "controller" / "ledger.sqlite3")
            resolution_count = connection.execute(
                "SELECT COUNT(*) FROM deployment_resolutions"
            ).fetchone()[0]
            connection.close()
            self.assertEqual(resolution_count, 1)
            self.assertEqual(len(ledger.events("deploy-manual-resolution")), 2)

    def test_failed_resolution_supports_operation_without_external_id(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            ledger, now = make_indeterminate_ledger(root, external_operation_id=None)
            artifacts = sign_resolution(
                ledger,
                root / "resolutions",
                now,
                proposed_status="failed",
                reason_code="provider_confirmed_failed",
            )
            operation, applied = apply_manual_resolution(
                ledger,
                *artifacts,
                REQUEST_KEY,
                REQUEST_KEY_ID,
                APPROVAL_KEY,
                APPROVAL_KEY_ID,
                now=now + timedelta(seconds=3),
            )
            self.assertTrue(applied)
            self.assertEqual(operation["status"], "failed")
            self.assertEqual(operation["error_code"], "manual_resolution_failed")
            self.assertEqual(
                ledger.resolution("deploy-manual-resolution")["reason_code"],
                "provider_confirmed_failed",
            )

    def test_tampering_wrong_keys_and_same_actor_fail_closed(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            ledger, now = make_indeterminate_ledger(root)
            evidence_path, request_path, approval_path = sign_resolution(
                ledger,
                root / "resolutions",
                now,
            )
            with self.assertRaisesRegex(DeploymentResolutionError, "signature is invalid"):
                check_manual_resolution(
                    ledger,
                    evidence_path,
                    request_path,
                    approval_path,
                    b"wrong-request-key-material-32-bytes-minimum",
                    REQUEST_KEY_ID,
                    APPROVAL_KEY,
                    APPROVAL_KEY_ID,
                    now=now + timedelta(seconds=3),
                )

            evidence = json.loads(evidence_path.read_text(encoding="utf-8"))
            evidence["reason_code"] = "forged_reason"
            evidence_path.write_text(json.dumps(evidence), encoding="utf-8")
            evidence_path.chmod(0o600)
            with self.assertRaisesRegex(DeploymentResolutionError, "does not match the evidence"):
                check_manual_resolution(
                    ledger,
                    evidence_path,
                    request_path,
                    approval_path,
                    REQUEST_KEY,
                    REQUEST_KEY_ID,
                    APPROVAL_KEY,
                    APPROVAL_KEY_ID,
                    now=now + timedelta(seconds=3),
                )

        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            ledger, now = make_indeterminate_ledger(root)
            _evidence, evidence_path, _created = create_resolution_evidence(
                ledger,
                root / "resolutions",
                "deploy-manual-resolution",
                "completed",
                "provider_confirmed_completed",
                "same-operator",
                PROVIDER_EVIDENCE_SHA256,
                now=now,
            )
            _request, request_path, _created = issue_resolution_request(
                ledger,
                evidence_path,
                root / "resolutions",
                REQUEST_KEY,
                REQUEST_KEY_ID,
                now=now + timedelta(seconds=1),
            )
            with self.assertRaisesRegex(
                DeploymentResolutionConfigError,
                "requester and approver",
            ):
                approve_resolution_request(
                    request_path,
                    root / "resolutions",
                    APPROVAL_KEY,
                    APPROVAL_KEY_ID,
                    "same-operator",
                    now=now + timedelta(seconds=2),
                )

    def test_expiry_and_ledger_state_fence_block_consumption(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            ledger, now = make_indeterminate_ledger(root)
            artifacts = sign_resolution(
                ledger,
                root / "resolutions",
                now,
                request_ttl=5,
                approval_ttl=3,
            )
            with self.assertRaisesRegex(DeploymentResolutionError, "has expired"):
                check_manual_resolution(
                    ledger,
                    *artifacts,
                    REQUEST_KEY,
                    REQUEST_KEY_ID,
                    APPROVAL_KEY,
                    APPROVAL_KEY_ID,
                    now=now + timedelta(seconds=6),
                )

        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            ledger, now = make_indeterminate_ledger(root)
            artifacts = sign_resolution(ledger, root / "resolutions", now)
            ledger_path = root / "controller" / "ledger.sqlite3"
            connection = sqlite3.connect(ledger_path)
            connection.execute(
                """
                UPDATE deployment_operations SET updated_at = ?
                WHERE deployment_id = 'deploy-manual-resolution'
                """,
                ((now + timedelta(seconds=10)).isoformat().replace("+00:00", "Z"),),
            )
            connection.commit()
            connection.close()
            with self.assertRaises(DeploymentBusyError):
                check_manual_resolution(
                    ledger,
                    *artifacts,
                    REQUEST_KEY,
                    REQUEST_KEY_ID,
                    APPROVAL_KEY,
                    APPROVAL_KEY_ID,
                    now=now + timedelta(seconds=3),
                )

    def test_different_signed_resolution_cannot_replace_consumed_resolution(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            ledger, now = make_indeterminate_ledger(root)
            first = sign_resolution(ledger, root / "resolutions", now)
            second = sign_resolution(
                ledger,
                root / "resolutions",
                now,
                reason_code="provider_export_confirmed",
                provider_evidence_sha256="b" * 64,
            )
            apply_manual_resolution(
                ledger,
                *first,
                REQUEST_KEY,
                REQUEST_KEY_ID,
                APPROVAL_KEY,
                APPROVAL_KEY_ID,
                now=now + timedelta(seconds=3),
            )
            with self.assertRaisesRegex(DeploymentResolutionError, "different manual resolution"):
                check_manual_resolution(
                    ledger,
                    *second,
                    REQUEST_KEY,
                    REQUEST_KEY_ID,
                    APPROVAL_KEY,
                    APPROVAL_KEY_ID,
                    now=now + timedelta(seconds=3),
                )

    def test_v3_ledger_migrates_to_v4_without_changing_operation(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            ledger, _now = make_indeterminate_ledger(root)
            before = ledger.get("deploy-manual-resolution")
            ledger_path = root / "controller" / "ledger.sqlite3"
            connection = sqlite3.connect(ledger_path)
            connection.execute("DROP TABLE deployment_resolutions")
            connection.execute("PRAGMA user_version = 3")
            connection.commit()
            connection.close()

            migrated = DeploymentLedger(ledger_path)
            self.assertEqual(migrated.get("deploy-manual-resolution"), before)
            self.assertIsNone(migrated.resolution("deploy-manual-resolution"))
            connection = sqlite3.connect(ledger_path)
            version = connection.execute("PRAGMA user_version").fetchone()[0]
            connection.close()
            self.assertEqual(version, 4)

    def test_insecure_or_linked_resolution_artifacts_are_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            ledger, now = make_indeterminate_ledger(root)
            evidence_path, request_path, approval_path = sign_resolution(
                ledger,
                root / "resolutions",
                now,
            )
            approval_path.chmod(0o644)
            with self.assertRaisesRegex(DeploymentResolutionError, "permissions must be 0600"):
                check_manual_resolution(
                    ledger,
                    evidence_path,
                    request_path,
                    approval_path,
                    REQUEST_KEY,
                    REQUEST_KEY_ID,
                    APPROVAL_KEY,
                    APPROVAL_KEY_ID,
                    now=now + timedelta(seconds=3),
                )

        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            ledger, now = make_indeterminate_ledger(root)
            evidence_path, request_path, approval_path = sign_resolution(
                ledger,
                root / "resolutions",
                now,
            )
            outside = root / "outside.json"
            approval_path.replace(outside)
            os.symlink(outside, approval_path)
            with self.assertRaisesRegex(DeploymentResolutionError, "regular file"):
                check_manual_resolution(
                    ledger,
                    evidence_path,
                    request_path,
                    approval_path,
                    REQUEST_KEY,
                    REQUEST_KEY_ID,
                    APPROVAL_KEY,
                    APPROVAL_KEY_ID,
                    now=now + timedelta(seconds=3),
                )


if __name__ == "__main__":
    unittest.main()
