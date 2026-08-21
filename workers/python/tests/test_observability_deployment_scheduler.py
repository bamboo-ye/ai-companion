from __future__ import annotations

import json
import os
import sqlite3
import tempfile
import unittest
from concurrent.futures import ThreadPoolExecutor
from contextlib import redirect_stdout
from datetime import UTC, datetime, timedelta
from io import StringIO
from pathlib import Path
from threading import Event
from unittest.mock import patch

from ai_companion_worker.evaluation.deployment_controller import (
    DeploymentLedger,
    ReconciliationPolicy,
)
from ai_companion_worker.evaluation.deployment_scheduler import (
    CERTIFICATION_SCHEMA_VERSION,
    SCHEDULER_RUN_SCHEMA_VERSION,
    DeploymentSchedulerConfigError,
    DeploymentSchedulerError,
    SQLiteSandboxAdapter,
    build_sandbox_request,
    certify_sandbox_adapter,
    find_adapter_certification_path,
    main,
    run_certified_scheduler_loop,
    run_certified_scheduler_once,
    verify_adapter_certification,
)


KEY = b"sandbox-adapter-certification-key-32-bytes-minimum"
KEY_ID = "sandbox-adapter-certification-v1"
NOW = datetime(2026, 8, 11, 2, 0, tzinfo=UTC)
POLICY = ReconciliationPolicy(
    base_delay_seconds=1,
    max_delay_seconds=4,
    max_attempts=3,
    deadline_seconds=30,
)


def provider_stats(path: Path) -> tuple[int, int]:
    connection = sqlite3.connect(path)
    row = connection.execute(
        "SELECT COUNT(*), COALESCE(SUM(lookup_count), 0) FROM sandbox_operations"
    ).fetchone()
    connection.close()
    if row is None:
        raise AssertionError("sandbox provider statistics are missing")
    return int(row[0]), int(row[1])


def prepare_accepted_operation(
    root: Path,
    adapter: SQLiteSandboxAdapter,
    deployment_id: str = "scheduler-deployment",
) -> tuple[DeploymentLedger, str]:
    ledger = DeploymentLedger(root / "controller" / "ledger.sqlite3")
    request = build_sandbox_request(deployment_id, nonce="scheduler-test")
    ledger.prepare(request, NOW)
    acquired = ledger.acquire(deployment_id, adapter, NOW, 30)
    if isinstance(acquired, dict):
        raise AssertionError("new sandbox deployment unexpectedly terminated")
    result = adapter.submit(acquired.request)
    operation = ledger.finish_result(
        deployment_id,
        acquired.token,
        result,
        NOW,
        POLICY,
    )
    if operation["status"] != "accepted":
        raise AssertionError("sandbox submit did not enter accepted state")
    return ledger, request.idempotency_key


class ObservabilityDeploymentSchedulerTest(unittest.TestCase):
    def test_certification_exercises_and_binds_one_private_sandbox(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            adapter = SQLiteSandboxAdapter(root / "provider" / "sandbox.sqlite3")
            certification, path, created = certify_sandbox_adapter(
                adapter,
                root / "certifications",
                KEY,
                KEY_ID,
                nonce="certification-test-run",
                ttl_seconds=60,
                now=NOW,
            )
            repeated, repeated_path, repeated_created = certify_sandbox_adapter(
                adapter,
                root / "certifications",
                KEY,
                KEY_ID,
                nonce="certification-test-run",
                ttl_seconds=60,
                now=NOW,
            )

            self.assertTrue(created)
            self.assertFalse(repeated_created)
            self.assertEqual(path, repeated_path)
            self.assertEqual(certification, repeated)
            self.assertEqual(certification["schema_version"], CERTIFICATION_SCHEMA_VERSION)
            self.assertEqual(certification["adapter"], "sqlite-sandbox")
            self.assertEqual(
                certification["provider_instance_sha256"],
                adapter.provider_instance_sha256,
            )
            self.assertEqual(certification["report"]["submit_statuses"], [
                "accepted",
                "accepted",
            ])
            self.assertEqual(certification["report"]["lookup_status"], "completed")
            self.assertEqual(provider_stats(adapter.path), (1, 1))
            self.assertEqual(path.parent.stat().st_mode & 0o777, 0o700)
            self.assertEqual(path.stat().st_mode & 0o777, 0o600)
            self.assertEqual(adapter.path.stat().st_mode & 0o777, 0o600)
            verified = verify_adapter_certification(
                path,
                adapter,
                KEY,
                KEY_ID,
                now=NOW + timedelta(seconds=30),
            )
            self.assertEqual(verified, certification)
            self.assertEqual(
                find_adapter_certification_path(
                    path.parent,
                    adapter,
                    KEY,
                    KEY_ID,
                    "certification-test-run",
                    now=NOW + timedelta(seconds=30),
                ),
                path,
            )

            retired = root / "provider" / "retired.sqlite3"
            adapter.path.rename(retired)
            replacement = SQLiteSandboxAdapter(adapter.path)
            with self.assertRaisesRegex(
                DeploymentSchedulerError,
                "registered implementation",
            ):
                verify_adapter_certification(
                    path,
                    replacement,
                    KEY,
                    KEY_ID,
                    now=NOW + timedelta(seconds=30),
                )
            self.assertEqual(provider_stats(replacement.path), (0, 0))

    def test_initialize_cli_creates_ledger_and_reuses_certification_nonce(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            arguments = [
                "initialize-sandbox",
                "--ledger",
                str(root / "controller" / "ledger.sqlite3"),
                "--provider-ledger",
                str(root / "provider" / "sandbox.sqlite3"),
                "--output-dir",
                str(root / "certifications"),
                "--nonce",
                "initialize-cli-test",
                "--ttl-seconds",
                "60",
            ]
            environment = {
                "OBSERVABILITY_ADAPTER_CERTIFICATION_KEY": KEY.decode("utf-8"),
                "OBSERVABILITY_ADAPTER_CERTIFICATION_KEY_ID": KEY_ID,
            }
            first_output = StringIO()
            repeated_output = StringIO()
            with patch.dict(os.environ, environment, clear=False):
                with redirect_stdout(first_output):
                    first_result = main(arguments)
                with redirect_stdout(repeated_output):
                    repeated_result = main(arguments)

            first = json.loads(first_output.getvalue())
            repeated = json.loads(repeated_output.getvalue())
            self.assertEqual(first_result, 0)
            self.assertEqual(repeated_result, 0)
            self.assertEqual(first["status"], "certified")
            self.assertEqual(repeated["status"], "reused")
            self.assertEqual(first["certification_id"], repeated["certification_id"])
            self.assertEqual(first["controller_ledger"]["status"], "ready")
            ledger = DeploymentLedger(root / "controller" / "ledger.sqlite3", create=False)
            self.assertEqual(ledger.reconciliation_health(NOW)["accepted_pending"], 0)

    def test_certified_scheduler_completes_due_operation_without_resubmit(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            adapter = SQLiteSandboxAdapter(root / "provider" / "sandbox.sqlite3")
            certification, path, _created = certify_sandbox_adapter(
                adapter,
                root / "certifications",
                KEY,
                KEY_ID,
                nonce="scheduler-completion-test",
                now=NOW,
            )
            ledger, idempotency_key = prepare_accepted_operation(root, adapter)
            before_rows, before_lookups = provider_stats(adapter.path)

            report = run_certified_scheduler_once(
                ledger,
                adapter,
                path,
                KEY,
                KEY_ID,
                now=NOW + timedelta(seconds=1),
            )

            self.assertEqual(report["schema_version"], SCHEDULER_RUN_SCHEMA_VERSION)
            self.assertEqual(report["certification_id"], certification["certification_id"])
            self.assertEqual(report["before"]["due"], 1)
            self.assertEqual(report["batch"]["selected"], 1)
            self.assertEqual(report["batch"]["completed"], 1)
            self.assertEqual(report["after"]["accepted_pending"], 0)
            self.assertEqual(provider_stats(adapter.path), (before_rows, before_lookups + 1))
            operation = adapter.operation(idempotency_key)
            self.assertIsNotNone(operation)
            assert operation is not None
            self.assertEqual(operation["status"], "completed")
            self.assertEqual(ledger.get("scheduler-deployment")["status"], "completed")
            event_types = [
                event["event_type"] for event in ledger.events("scheduler-deployment")
            ]
            self.assertEqual(event_types.count("dispatch_started"), 1)
            self.assertEqual(event_types.count("reconciliation_started"), 1)
            self.assertEqual(event_types.count("reconciliation_completed"), 1)

    def test_invalid_certification_fails_before_ledger_or_provider_work(self) -> None:
        scenarios = ("tampered", "expired", "wrong_key", "different_provider")
        for scenario in scenarios:
            with self.subTest(scenario=scenario), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                adapter = SQLiteSandboxAdapter(root / "provider" / "sandbox.sqlite3")
                certification, path, _created = certify_sandbox_adapter(
                    adapter,
                    root / "certifications",
                    KEY,
                    KEY_ID,
                    nonce=f"invalid-certification-{scenario}",
                    ttl_seconds=1,
                    now=NOW,
                )
                ledger, idempotency_key = prepare_accepted_operation(root, adapter)
                selected_adapter = adapter
                selected_key = KEY
                selected_now = NOW
                if scenario == "tampered":
                    certification["implementation_version"] = "9.9.9"
                    path.write_text(
                        json.dumps(certification, sort_keys=True),
                        encoding="utf-8",
                    )
                elif scenario == "expired":
                    selected_now = NOW + timedelta(seconds=1)
                elif scenario == "wrong_key":
                    selected_key = b"wrong-sandbox-certification-key-32-bytes-minimum"
                else:
                    selected_adapter = SQLiteSandboxAdapter(
                        root / "other-provider" / "sandbox.sqlite3"
                    )
                provider_before = provider_stats(selected_adapter.path)
                events_before = list(ledger.events("scheduler-deployment"))

                with self.assertRaises(DeploymentSchedulerError):
                    run_certified_scheduler_once(
                        ledger,
                        selected_adapter,
                        path,
                        selected_key,
                        KEY_ID,
                        now=selected_now,
                    )

                self.assertEqual(provider_stats(selected_adapter.path), provider_before)
                self.assertEqual(
                    ledger.events("scheduler-deployment"),
                    events_before,
                )
                self.assertEqual(
                    ledger.get("scheduler-deployment")["reconciliation_count"],
                    0,
                )
                operation = adapter.operation(idempotency_key)
                self.assertIsNotNone(operation)
                assert operation is not None
                self.assertEqual(operation["lookup_count"], 0)

    def test_concurrent_schedulers_perform_one_lookup_and_one_completion(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            adapter = SQLiteSandboxAdapter(root / "provider" / "sandbox.sqlite3")
            _certification, path, _created = certify_sandbox_adapter(
                adapter,
                root / "certifications",
                KEY,
                KEY_ID,
                nonce="concurrent-scheduler-test",
                now=NOW,
            )
            ledger, idempotency_key = prepare_accepted_operation(root, adapter)

            def run() -> dict[str, object]:
                return run_certified_scheduler_once(
                    ledger,
                    adapter,
                    path,
                    KEY,
                    KEY_ID,
                    now=NOW + timedelta(seconds=1),
                )

            with ThreadPoolExecutor(max_workers=16) as executor:
                reports = list(executor.map(lambda _index: run(), range(16)))

            operation = adapter.operation(idempotency_key)
            self.assertIsNotNone(operation)
            assert operation is not None
            self.assertEqual(operation["lookup_count"], 1)
            self.assertEqual(ledger.get("scheduler-deployment")["status"], "completed")
            self.assertEqual(
                sum(int(report["batch"]["completed"]) for report in reports),  # type: ignore[index]
                1,
            )
            event_types = [
                event["event_type"] for event in ledger.events("scheduler-deployment")
            ]
            self.assertEqual(event_types.count("reconciliation_started"), 1)
            self.assertEqual(event_types.count("reconciliation_completed"), 1)

    def test_provider_restart_preserves_idempotency_and_lookup_state(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            path = root / "provider" / "sandbox.sqlite3"
            request = build_sandbox_request("restart-test", nonce="fixed")
            first_adapter = SQLiteSandboxAdapter(path, complete_after_lookups=2)
            instance_sha256 = first_adapter.provider_instance_sha256
            first = first_adapter.submit(request)
            repeated = first_adapter.submit(request)
            restarted = SQLiteSandboxAdapter(path, complete_after_lookups=2)
            self.assertEqual(restarted.provider_instance_sha256, instance_sha256)
            pending = restarted.lookup(request.idempotency_key)
            completed = restarted.lookup(request.idempotency_key)

            self.assertEqual(first, repeated)
            self.assertIsNotNone(pending)
            self.assertIsNotNone(completed)
            assert pending is not None and completed is not None
            self.assertEqual(pending.status, "accepted")
            self.assertEqual(completed.status, "completed")
            self.assertEqual(first.external_operation_id, completed.external_operation_id)
            self.assertEqual(provider_stats(path), (1, 2))

    def test_scheduler_loop_is_bounded_and_interruptible(self) -> None:
        timestamps: list[datetime] = []
        sleeps: list[float] = []

        def run_once(now: datetime) -> dict[str, object]:
            timestamps.append(now)
            return {"generated_at": now.isoformat()}

        reports = run_certified_scheduler_loop(
            run_once,
            interval_seconds=2,
            iterations=3,
            start_time=NOW,
            sleep=sleeps.append,
        )
        self.assertEqual(len(reports), 3)
        self.assertEqual(
            timestamps,
            [NOW, NOW + timedelta(seconds=2), NOW + timedelta(seconds=4)],
        )
        self.assertEqual(sleeps, [2.0, 2.0])

        stop_event = Event()

        def stop_after_first(now: datetime) -> dict[str, object]:
            stop_event.set()
            return {"generated_at": now.isoformat()}

        interrupted = run_certified_scheduler_loop(
            stop_after_first,
            interval_seconds=2,
            iterations=3,
            start_time=NOW,
            stop_event=stop_event,
        )
        self.assertEqual(len(interrupted), 1)

        with self.assertRaises(DeploymentSchedulerConfigError):
            run_certified_scheduler_loop(
                run_once,
                interval_seconds=0,
                iterations=1,
            )

    def test_sandbox_rejects_wrong_schema_and_non_private_database(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            malformed = root / "malformed.sqlite3"
            connection = sqlite3.connect(malformed)
            connection.execute("CREATE TABLE sandbox_operations (idempotency_key TEXT)")
            connection.execute("PRAGMA user_version = 1")
            connection.commit()
            connection.close()
            malformed.chmod(0o600)
            with self.assertRaises(DeploymentSchedulerError):
                SQLiteSandboxAdapter(malformed)

            valid = root / "provider" / "sandbox.sqlite3"
            SQLiteSandboxAdapter(valid)
            valid.chmod(0o644)
            with self.assertRaises(DeploymentSchedulerError):
                SQLiteSandboxAdapter(valid)


if __name__ == "__main__":
    unittest.main()
