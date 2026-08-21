from __future__ import annotations

import json
import tempfile
import threading
import unittest
from dataclasses import replace
from datetime import UTC, datetime, timedelta
from pathlib import Path
from concurrent.futures import ThreadPoolExecutor

from ai_companion_worker.evaluation.direct_traffic_certification import (
    CERTIFICATION_SCHEMA_VERSION,
    DirectTrafficCertificationConfigError,
    DirectTrafficCertificationError,
    SQLiteDirectTrafficCertificationAdapter,
    certify_adapter,
    create_certification,
    validate_execution_binding,
    verify_certification,
)
from ai_companion_worker.evaluation.direct_rollout_traffic import (
    SQLiteDirectTrafficSandbox,
)
from ai_companion_worker.evaluation.release_gate_history import environment_sha256


KEY = b"direct-traffic-adapter-certification-test-key-32-bytes"
KEY_ID = "direct-traffic-adapter-certification-test-v1"
ENVIRONMENT = "staging-cn"
PROVIDER = "staging-provider-a"
NAMESPACE = "isolated-certification-a"
NOW = datetime(2026, 8, 13, 1, 0, tzinfo=UTC)


class BrokenCompareAndSwapAdapter(SQLiteDirectTrafficCertificationAdapter):
    def apply_certification_change(self, request):
        result = super().apply_certification_change(request)
        if result.status == "conflict":
            return replace(result, status="applied")
        return result


class LeakingIsolationAdapter(SQLiteDirectTrafficCertificationAdapter):
    def __init__(self, *args, **kwargs):
        self.operational_revision = 0
        super().__init__(*args, **kwargs)

    def operational_state_sha256(self) -> str:
        return f"{self.operational_revision:064x}"

    def apply_certification_change(self, request):
        result = super().apply_certification_change(request)
        self.operational_revision += 1
        return result


class AgentDirectTrafficCertificationTest(unittest.TestCase):
    def adapter(
        self,
        root: Path,
        *,
        adapter_type=SQLiteDirectTrafficCertificationAdapter,
        provider: str = PROVIDER,
        namespace: str = NAMESPACE,
    ):
        root.mkdir(mode=0o700, parents=True, exist_ok=True)
        return adapter_type(
            root / "certification" / "provider.sqlite3",
            environment_sha256(ENVIRONMENT),
            provider,
            namespace_id=namespace,
        )

    def test_isolated_challenge_proves_idempotency_lookup_and_cas(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            adapter = self.adapter(root)

            report = certify_adapter(adapter, nonce="certification-test")

            self.assertTrue(report["idempotent_apply"])
            self.assertTrue(report["lookup"])
            self.assertTrue(report["compare_and_swap"])
            self.assertTrue(report["isolated"])
            self.assertEqual(report["final_traffic_percent"], 10)
            self.assertEqual(report["final_revision"], 1)
            self.assertEqual(
                report["operational_state_before_sha256"],
                report["operational_state_after_sha256"],
            )
            self.assertEqual(adapter.operation_count(report["challenge_id"]), 2)
            self.assertEqual(adapter.path.stat().st_mode & 0o777, 0o600)
            self.assertEqual(adapter.path.parent.stat().st_mode & 0o777, 0o700)

    def test_signed_certificate_is_private_idempotent_and_adapter_bound(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            adapter = self.adapter(root)
            certificate, path, created = create_certification(
                adapter,
                root / "certificates",
                KEY,
                KEY_ID,
                nonce="signed-certification-test",
                ttl_seconds=60,
                now=NOW,
            )
            repeated, repeated_path, repeated_created = create_certification(
                adapter,
                root / "certificates",
                KEY,
                KEY_ID,
                nonce="signed-certification-test",
                ttl_seconds=60,
                now=NOW,
            )
            verified = verify_certification(
                path, adapter, KEY, KEY_ID, now=NOW + timedelta(seconds=30)
            )

            self.assertTrue(created)
            self.assertFalse(repeated_created)
            self.assertEqual(path, repeated_path)
            self.assertEqual(certificate, repeated)
            self.assertEqual(verified, certificate)
            self.assertEqual(
                certificate["schema_version"], CERTIFICATION_SCHEMA_VERSION
            )
            self.assertEqual(path.stat().st_mode & 0o777, 0o600)

            with self.assertRaisesRegex(
                DirectTrafficCertificationError, "expired"
            ):
                verify_certification(
                    path, adapter, KEY, KEY_ID, now=NOW + timedelta(seconds=61)
                )

            different_provider = self.adapter(
                root / "other", provider="staging-provider-b"
            )
            with self.assertRaisesRegex(
                DirectTrafficCertificationError, "registered adapter"
            ):
                verify_certification(
                    path,
                    different_provider,
                    KEY,
                    KEY_ID,
                    now=NOW + timedelta(seconds=30),
                )

    def test_tamper_wrong_key_namespace_and_implementation_fail_closed(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            adapter = self.adapter(root)
            _certificate, path, _created = create_certification(
                adapter,
                root / "certificates",
                KEY,
                KEY_ID,
                nonce="tamper-certification-test",
                now=NOW,
            )
            original = path.read_text(encoding="utf-8")
            tampered = json.loads(original)
            tampered["report"]["compare_and_swap"] = False
            path.write_text(json.dumps(tampered), encoding="utf-8")
            path.chmod(0o600)
            with self.assertRaisesRegex(
                DirectTrafficCertificationError, "controls|signature"
            ):
                verify_certification(
                    path, adapter, KEY, KEY_ID, now=NOW + timedelta(seconds=1)
                )
            path.write_text(original, encoding="utf-8")
            path.chmod(0o600)

            with self.assertRaisesRegex(
                DirectTrafficCertificationError, "signature is invalid"
            ):
                verify_certification(
                    path,
                    adapter,
                    b"different-direct-traffic-certification-key-value",
                    KEY_ID,
                    now=NOW + timedelta(seconds=1),
                )

            different_namespace = self.adapter(
                root / "other-namespace", namespace="isolated-certification-b"
            )
            with self.assertRaisesRegex(
                DirectTrafficCertificationError, "registered adapter"
            ):
                verify_certification(
                    path,
                    different_namespace,
                    KEY,
                    KEY_ID,
                    now=NOW + timedelta(seconds=1),
                )

    def test_broken_cas_or_isolation_cannot_be_certified(self) -> None:
        for adapter_type, message in (
            (BrokenCompareAndSwapAdapter, "compare-and-swap"),
            (LeakingIsolationAdapter, "operational traffic state"),
        ):
            with self.subTest(adapter=adapter_type.__name__), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                adapter = self.adapter(root, adapter_type=adapter_type)
                with self.assertRaisesRegex(
                    DirectTrafficCertificationError, message
                ):
                    certify_adapter(adapter, nonce="broken-adapter-test")

    def test_missing_capabilities_and_namespace_rebinding_are_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            adapter = self.adapter(root)
            adapter.supports_lookup = False  # type: ignore[misc]
            with self.assertRaisesRegex(
                DirectTrafficCertificationConfigError, "capabilities"
            ):
                certify_adapter(adapter, nonce="missing-capability-test")

        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.adapter(root)
            with self.assertRaisesRegex(
                DirectTrafficCertificationError, "namespace binding"
            ):
                self.adapter(root, namespace="different-namespace")

    def test_retry_after_challenge_before_certificate_reuses_provider_operations(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            adapter = self.adapter(root)
            first = certify_adapter(adapter, nonce="crash-recovery-test")

            recovered, _path, created = create_certification(
                adapter,
                root / "certificates",
                KEY,
                KEY_ID,
                nonce="crash-recovery-test",
                now=NOW,
            )

            self.assertTrue(created)
            self.assertEqual(recovered["report"], first)
            self.assertEqual(adapter.operation_count(first["challenge_id"]), 2)

    def test_concurrent_certificate_writers_converge_on_one_private_file(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            adapter = self.adapter(root)
            barrier = threading.Barrier(2)

            def execute():
                barrier.wait(timeout=5)
                return create_certification(
                    adapter,
                    root / "certificates",
                    KEY,
                    KEY_ID,
                    nonce="concurrent-certification-test",
                    now=NOW,
                )

            with ThreadPoolExecutor(max_workers=2) as executor:
                results = list(executor.map(lambda _: execute(), range(2)))

            self.assertEqual(sorted(item[2] for item in results), [False, True])
            self.assertEqual(results[0][0], results[1][0])
            self.assertEqual(results[0][1], results[1][1])
            self.assertEqual(len(list((root / "certificates").glob("*.json"))), 1)
            self.assertEqual(
                adapter.operation_count(results[0][0]["report"]["challenge_id"]),
                2,
            )

    def test_certificate_is_accepted_only_by_bound_execution_adapter(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            certification_adapter = self.adapter(root)
            _certificate, path, _created = create_certification(
                certification_adapter,
                root / "certificates",
                KEY,
                KEY_ID,
                nonce="execution-binding-test",
                now=NOW,
            )
            operational = SQLiteDirectTrafficSandbox(
                root / "operational" / "traffic.sqlite3",
                environment_sha256(ENVIRONMENT),
                PROVIDER,
                create=True,
                initial_traffic_percent=5,
            )
            verified = verify_certification(
                path,
                operational,
                KEY,
                KEY_ID,
                now=NOW + timedelta(seconds=1),
                require_certification_namespace=False,
            )

            self.assertEqual(
                validate_execution_binding(verified, operational), verified
            )

            other = SQLiteDirectTrafficSandbox(
                root / "other-operational" / "traffic.sqlite3",
                environment_sha256(ENVIRONMENT),
                "another-provider",
                create=True,
                initial_traffic_percent=5,
            )
            with self.assertRaisesRegex(
                DirectTrafficCertificationError, "registered adapter"
            ):
                verify_certification(
                    path,
                    other,
                    KEY,
                    KEY_ID,
                    now=NOW + timedelta(seconds=1),
                    require_certification_namespace=False,
                )


if __name__ == "__main__":
    unittest.main()
