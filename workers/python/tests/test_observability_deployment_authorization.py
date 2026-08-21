from __future__ import annotations

import json
import os
import sys
import tempfile
import unittest
from concurrent.futures import ThreadPoolExecutor
from datetime import UTC, datetime, timedelta
from pathlib import Path
from typing import Any

from ai_companion_worker.evaluation.deployment_authorization import (
    DeploymentAuthorizationConfigError,
    DeploymentAuthorizationError,
    check_gate_for_deployment,
    issue_deployment_authorization,
    verify_deployment_authorization,
)
from ai_companion_worker.evaluation.observability import build_snapshot
from ai_companion_worker.evaluation.release_attestation import (
    AttestationError,
    create_attestation,
)
from ai_companion_worker.evaluation.release_gate import run_gate


ROOT = Path(__file__).resolve().parents[3]
BASELINE = ROOT / "evals" / "observability" / "baselines" / "release.v1.json"
KEY = b"deployment-authorization-test-key-32-bytes-minimum"
OTHER_KEY = b"other-deployment-authorization-key-32-bytes"
KEY_ID = "deployment-test-key-v1"


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


def make_signed_bundle(
    root: Path,
    name: str = "release-gate-authorization-test",
    after_queue: int = 0,
) -> tuple[Path, datetime]:
    spec = root / f"{name}.canary.json"
    spec.write_text(
        json.dumps(
            {
                "schema_version": "observability-canary-command-v1",
                "name": "authorization-test",
                "command": [sys.executable, "-c", "pass"],
                "timeout_seconds": 2,
                "stabilization_seconds": 0,
            }
        ),
        encoding="utf-8",
    )
    gate_dir = root / name
    report, _result = run_gate(
        "http://metrics.invalid/metrics",
        BASELINE,
        spec,
        gate_dir,
        capture=SnapshotSequence([0, after_queue]),
    )
    gate_time = datetime.fromisoformat(
        str(report["generated_at"]).replace("Z", "+00:00")
    ).astimezone(UTC)
    signed_at = gate_time + timedelta(seconds=1)
    create_attestation(gate_dir, KEY, KEY_ID, now=signed_at)
    return gate_dir, signed_at + timedelta(seconds=1)


class ObservabilityDeploymentAuthorizationTest(unittest.TestCase):
    def test_check_is_read_only_and_issue_verify_are_private(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            gate_dir, now = make_signed_bundle(root)
            state_dir = root / "authorizations"

            readiness = check_gate_for_deployment(
                gate_dir,
                KEY,
                KEY_ID,
                "deploy-20260810-a",
                now=now,
            )
            self.assertEqual(readiness["decision"], "promote")
            self.assertFalse(state_dir.exists())

            authorization, created = issue_deployment_authorization(
                gate_dir,
                state_dir,
                KEY,
                KEY_ID,
                "deploy-20260810-a",
                now=now,
            )
            path = state_dir / "deploy-20260810-a.authorization.json"
            verified = verify_deployment_authorization(
                gate_dir,
                path,
                KEY,
                KEY_ID,
                "deploy-20260810-a",
                now=now + timedelta(seconds=1),
            )

            self.assertTrue(created)
            self.assertEqual(authorization, verified)
            self.assertEqual(authorization["scope"], "release-promotion")
            self.assertEqual(state_dir.stat().st_mode & 0o777, 0o700)
            self.assertEqual(path.stat().st_mode & 0o777, 0o600)

    def test_repeated_issue_is_idempotent_and_concurrent_issue_writes_once(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            gate_dir, now = make_signed_bundle(root)
            state_dir = root / "authorizations"

            first, first_created = issue_deployment_authorization(
                gate_dir,
                state_dir,
                KEY,
                KEY_ID,
                "deploy-idempotent",
                now=now,
            )
            second, second_created = issue_deployment_authorization(
                gate_dir,
                state_dir,
                KEY,
                KEY_ID,
                "deploy-idempotent",
                now=now + timedelta(seconds=1),
            )
            self.assertTrue(first_created)
            self.assertFalse(second_created)
            self.assertEqual(first, second)

        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            gate_dir, now = make_signed_bundle(root)
            state_dir = root / "authorizations"

            def issue() -> tuple[dict[str, Any], bool]:
                return issue_deployment_authorization(
                    gate_dir,
                    state_dir,
                    KEY,
                    KEY_ID,
                    "deploy-concurrent",
                    now=now,
                )

            with ThreadPoolExecutor(max_workers=8) as executor:
                results = list(executor.map(lambda _index: issue(), range(16)))

            self.assertEqual(sum(1 for _authorization, created in results if created), 1)
            self.assertEqual(
                {authorization["authorization_id"] for authorization, _created in results},
                {results[0][0]["authorization_id"]},
            )
            self.assertEqual(len(list(state_dir.glob("*.authorization.json"))), 1)

    def test_deployment_id_cannot_be_rebound_to_a_different_gate(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            first_gate, now = make_signed_bundle(root, "release-gate-first")
            second_gate, _second_now = make_signed_bundle(root, "release-gate-second")
            state_dir = root / "authorizations"
            issue_deployment_authorization(
                first_gate,
                state_dir,
                KEY,
                KEY_ID,
                "deploy-conflict",
                now=now,
            )

            with self.assertRaisesRegex(
                DeploymentAuthorizationError,
                "does not match the verified Gate",
            ):
                issue_deployment_authorization(
                    second_gate,
                    state_dir,
                    KEY,
                    KEY_ID,
                    "deploy-conflict",
                    now=now,
                )

    def test_tampering_wrong_key_and_expiry_are_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            gate_dir, now = make_signed_bundle(root)
            state_dir = root / "authorizations"
            issue_deployment_authorization(
                gate_dir,
                state_dir,
                KEY,
                KEY_ID,
                "deploy-tamper",
                now=now,
                ttl_seconds=10,
            )
            path = state_dir / "deploy-tamper.authorization.json"

            with self.assertRaisesRegex(AttestationError, "signature is invalid"):
                verify_deployment_authorization(
                    gate_dir,
                    path,
                    OTHER_KEY,
                    KEY_ID,
                    "deploy-tamper",
                    now=now,
                )
            with self.assertRaisesRegex(DeploymentAuthorizationError, "expired"):
                verify_deployment_authorization(
                    gate_dir,
                    path,
                    KEY,
                    KEY_ID,
                    "deploy-tamper",
                    now=now + timedelta(seconds=11),
                )

            value = json.loads(path.read_text(encoding="utf-8"))
            value["deployment_id"] = "deploy-forged"
            path.write_text(json.dumps(value), encoding="utf-8")
            path.chmod(0o600)
            with self.assertRaisesRegex(DeploymentAuthorizationError, "signature is invalid"):
                verify_deployment_authorization(
                    gate_dir,
                    path,
                    KEY,
                    KEY_ID,
                    "deploy-tamper",
                    now=now,
                )

    def test_rollback_gate_never_issues_promotion_authorization(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            gate_dir, now = make_signed_bundle(root, after_queue=600)
            with self.assertRaisesRegex(AttestationError, "required decision promote"):
                issue_deployment_authorization(
                    gate_dir,
                    root / "authorizations",
                    KEY,
                    KEY_ID,
                    "deploy-rollback",
                    now=now,
                )
            self.assertFalse((root / "authorizations").exists())

    def test_insecure_state_and_symlink_authorization_are_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            gate_dir, now = make_signed_bundle(root)
            state_dir = root / "authorizations"
            state_dir.mkdir(mode=0o755)
            with self.assertRaisesRegex(DeploymentAuthorizationError, "must be 0700"):
                issue_deployment_authorization(
                    gate_dir,
                    state_dir,
                    KEY,
                    KEY_ID,
                    "deploy-insecure",
                    now=now,
                )

        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            gate_dir, now = make_signed_bundle(root)
            state_dir = root / "authorizations"
            issue_deployment_authorization(
                gate_dir,
                state_dir,
                KEY,
                KEY_ID,
                "deploy-symlink",
                now=now,
            )
            path = state_dir / "deploy-symlink.authorization.json"
            outside = root / "outside.authorization.json"
            path.replace(outside)
            os.symlink(outside, path)
            with self.assertRaisesRegex(DeploymentAuthorizationError, "cannot open"):
                verify_deployment_authorization(
                    gate_dir,
                    path,
                    KEY,
                    KEY_ID,
                    "deploy-symlink",
                    now=now,
                )

    def test_invalid_ids_ttl_and_duplicate_json_are_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            gate_dir, now = make_signed_bundle(root)
            with self.assertRaisesRegex(DeploymentAuthorizationConfigError, "deployment_id"):
                check_gate_for_deployment(gate_dir, KEY, KEY_ID, "../unsafe", now=now)
            with self.assertRaisesRegex(DeploymentAuthorizationConfigError, "TTL"):
                issue_deployment_authorization(
                    gate_dir,
                    root / "authorizations",
                    KEY,
                    KEY_ID,
                    "deploy-invalid-ttl",
                    ttl_seconds=0,
                    now=now,
                )

            state_dir = root / "valid-authorizations"
            issue_deployment_authorization(
                gate_dir,
                state_dir,
                KEY,
                KEY_ID,
                "deploy-duplicate-json",
                now=now,
            )
            path = state_dir / "deploy-duplicate-json.authorization.json"
            original = path.read_text(encoding="utf-8").rstrip()
            duplicate = original[:-1] + ',"signature":"' + ("0" * 64) + '"}'
            path.write_text(duplicate, encoding="utf-8")
            path.chmod(0o600)
            with self.assertRaisesRegex(DeploymentAuthorizationError, "duplicate JSON key"):
                verify_deployment_authorization(
                    gate_dir,
                    path,
                    KEY,
                    KEY_ID,
                    "deploy-duplicate-json",
                    now=now,
                )


if __name__ == "__main__":
    unittest.main()
