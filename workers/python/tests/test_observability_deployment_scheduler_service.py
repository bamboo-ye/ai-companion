from __future__ import annotations

import json
import sqlite3
import tempfile
import unittest
from datetime import UTC, datetime, timedelta
from http import HTTPStatus
from pathlib import Path
from threading import Event
from typing import Any

from ai_companion_worker.evaluation.deployment_controller import DeploymentLedger
from ai_companion_worker.evaluation.deployment_scheduler import (
    DeploymentSchedulerError,
    SQLiteSandboxAdapter,
    certify_sandbox_adapter,
    run_certified_scheduler_once,
)
from ai_companion_worker.evaluation.deployment_scheduler_service import (
    SERVICE_STATE_SCHEMA_VERSION,
    DeploymentSchedulerServiceConfigError,
    SchedulerRunner,
    SchedulerRuntimeState,
    SchedulerServiceConfig,
    _http_response,
    _validate_service_config,
    run_scheduler_service_loop,
)


KEY = b"scheduler-service-certification-key-32-bytes-minimum"
KEY_ID = "scheduler-service-certification-v1"
NOW = datetime(2026, 8, 11, 3, 0, tzinfo=UTC)


def provider_stats(path: Path) -> tuple[int, int]:
    connection = sqlite3.connect(path)
    row = connection.execute(
        "SELECT COUNT(*), COALESCE(SUM(lookup_count), 0) FROM sandbox_operations"
    ).fetchone()
    connection.close()
    if row is None:
        raise AssertionError("sandbox provider statistics are missing")
    return int(row[0]), int(row[1])


def service_fixture(
    root: Path,
    *,
    ttl_seconds: int = 3600,
) -> tuple[
    DeploymentLedger,
    SQLiteSandboxAdapter,
    dict[str, Any],
    Path,
    SchedulerRuntimeState,
]:
    adapter = SQLiteSandboxAdapter(root / "provider" / "sandbox.sqlite3")
    certification, certification_path, _created = certify_sandbox_adapter(
        adapter,
        root / "certifications",
        KEY,
        KEY_ID,
        nonce="scheduler-service-test",
        ttl_seconds=ttl_seconds,
        now=NOW,
    )
    ledger = DeploymentLedger(root / "controller" / "ledger.sqlite3")
    state = SchedulerRuntimeState(
        root / "service-state",
        adapter,
        certification,
        now=NOW,
    )
    return ledger, adapter, certification, certification_path, state


def scheduler_report(
    ledger: DeploymentLedger,
    adapter: SQLiteSandboxAdapter,
    certification_path: Path,
    now: datetime = NOW + timedelta(seconds=1),
) -> dict[str, Any]:
    return run_certified_scheduler_once(
        ledger,
        adapter,
        certification_path,
        KEY,
        KEY_ID,
        now=now,
    )


class ObservabilityDeploymentSchedulerServiceTest(unittest.TestCase):
    def test_success_state_metrics_and_counters_survive_restart(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            ledger, adapter, certification, certification_path, state = service_fixture(root)
            ticks = iter((10.0, 10.25))
            runner = SchedulerRunner(
                lambda now: scheduler_report(ledger, adapter, certification_path, now),
                state,
                monotonic=lambda: next(ticks),
            )

            self.assertTrue(runner.run_iteration(NOW + timedelta(seconds=1)))
            snapshot = state.snapshot()
            self.assertTrue(snapshot["ready"])
            self.assertEqual(snapshot["successful_runs"], 1)
            self.assertEqual(snapshot["failed_runs"], 0)
            self.assertEqual(snapshot["last_run_duration_seconds"], 0.25)
            self.assertEqual(snapshot["last_report"]["batch"]["selected"], 0)
            metrics = state.metrics_text()
            self.assertIn(
                'ai_companion_deployment_scheduler_runs_total{outcome="success"} 1',
                metrics,
            )
            self.assertIn("ai_companion_deployment_scheduler_ready 1", metrics)
            self.assertIn("ai_companion_deployment_scheduler_due 0", metrics)
            self.assertEqual(state.path.parent.stat().st_mode & 0o777, 0o700)
            self.assertEqual(state.path.stat().st_mode & 0o777, 0o600)

            restarted = SchedulerRuntimeState(
                root / "service-state",
                adapter,
                certification,
                now=NOW + timedelta(seconds=2),
            )
            restored = restarted.snapshot()
            self.assertEqual(restored["successful_runs"], 1)
            self.assertEqual(restored["last_report"], snapshot["last_report"])
            self.assertFalse(restored["ready"])
            persisted = json.loads(state.path.read_text(encoding="utf-8"))
            self.assertEqual(persisted["schema_version"], SERVICE_STATE_SCHEMA_VERSION)

    def test_consecutive_failures_stop_service_at_exact_budget(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            _ledger, _adapter, _certification, _path, state = service_fixture(root)
            ticks = iter((0.0, 0.1, 1.0, 1.2, 2.0, 2.3))

            def fail(_now: datetime) -> dict[str, Any]:
                raise DeploymentSchedulerError("synthetic service failure")

            runner = SchedulerRunner(fail, state, monotonic=lambda: next(ticks))
            times = iter(
                (
                    NOW + timedelta(seconds=1),
                    NOW + timedelta(seconds=2),
                    NOW + timedelta(seconds=3),
                )
            )
            waits: list[float] = []
            result = run_scheduler_service_loop(
                runner,
                state,
                SchedulerServiceConfig(interval_seconds=5, max_consecutive_failures=3),
                Event(),
                now=lambda: next(times),
                wait=lambda seconds: waits.append(seconds) or False,
            )

            snapshot = state.snapshot()
            self.assertEqual(result, 1)
            self.assertEqual(snapshot["failed_runs"], 3)
            self.assertEqual(snapshot["consecutive_failures"], 3)
            self.assertEqual(snapshot["last_error_code"], "scheduler_error")
            self.assertFalse(snapshot["ready"])
            self.assertEqual(waits, [5.0, 5.0])

    def test_success_resets_failure_budget_and_graceful_stop_returns_zero(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            ledger, adapter, _certification, path, state = service_fixture(root)
            stop_event = Event()
            calls = 0

            def recover(now: datetime) -> dict[str, Any]:
                nonlocal calls
                calls += 1
                if calls == 1:
                    raise DeploymentSchedulerError("temporary failure")
                stop_event.set()
                return scheduler_report(ledger, adapter, path, now)

            ticks = iter((0.0, 0.1, 1.0, 1.2))
            runner = SchedulerRunner(recover, state, monotonic=lambda: next(ticks))
            result = run_scheduler_service_loop(
                runner,
                state,
                SchedulerServiceConfig(interval_seconds=1, max_consecutive_failures=3),
                stop_event,
                now=lambda: NOW + timedelta(seconds=calls + 1),
                wait=lambda _seconds: False,
            )

            snapshot = state.snapshot()
            self.assertEqual(result, 0)
            self.assertEqual(snapshot["successful_runs"], 1)
            self.assertEqual(snapshot["failed_runs"], 1)
            self.assertEqual(snapshot["consecutive_failures"], 0)
            self.assertIsNone(snapshot["last_error_code"])
            self.assertTrue(snapshot["ready"])

    def test_expired_certificate_retries_without_provider_lookup(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            ledger, adapter, _certification, path, state = service_fixture(
                root,
                ttl_seconds=1,
            )
            before = provider_stats(adapter.path)
            ticks = iter((0.0, 0.1))
            runner = SchedulerRunner(
                lambda now: scheduler_report(ledger, adapter, path, now),
                state,
                monotonic=lambda: next(ticks),
            )

            self.assertFalse(runner.run_iteration(NOW + timedelta(seconds=1)))
            self.assertEqual(provider_stats(adapter.path), before)
            self.assertEqual(state.snapshot()["last_error_code"], "scheduler_error")

    def test_health_readiness_metrics_and_unknown_http_routes(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            ledger, adapter, _certification, path, state = service_fixture(root)
            ticks = iter((0.0, 0.1))
            runner = SchedulerRunner(
                lambda now: scheduler_report(ledger, adapter, path, now),
                state,
                monotonic=lambda: next(ticks),
            )
            runner.run_iteration(NOW + timedelta(seconds=1))
            state.set_up(True)
            health = _http_response("/healthz", state)
            ready = _http_response("/readyz", state)
            metrics = _http_response("/metrics", state)
            missing = _http_response("/unknown", state)
            self.assertEqual(health[0], HTTPStatus.OK)
            self.assertEqual(json.loads(health[1]), {"status": "up"})
            self.assertEqual(ready[0], HTTPStatus.OK)
            self.assertEqual(json.loads(ready[1]), {"status": "ready"})
            self.assertEqual(metrics[0], HTTPStatus.OK)
            self.assertEqual(metrics[2], "text/plain; version=0.0.4")
            self.assertIn("ai_companion_deployment_scheduler_up 1", metrics[1])
            self.assertEqual(missing[0], HTTPStatus.NOT_FOUND)

    def test_state_tampering_and_unsafe_network_config_fail_closed(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            _ledger, adapter, certification, _path, state = service_fixture(root)
            persisted = json.loads(state.path.read_text(encoding="utf-8"))
            persisted["successful_runs"] = 1
            state.path.write_text(json.dumps(persisted), encoding="utf-8")
            with self.assertRaisesRegex(ValueError, "success counters"):
                SchedulerRuntimeState(
                    root / "service-state",
                    adapter,
                    certification,
                    now=NOW,
                )

            with self.assertRaises(DeploymentSchedulerServiceConfigError):
                _validate_service_config(SchedulerServiceConfig(listen_host="192.0.2.1"))
            with self.assertRaises(DeploymentSchedulerServiceConfigError):
                _validate_service_config(
                    SchedulerServiceConfig(max_consecutive_failures=0)
                )


if __name__ == "__main__":
    unittest.main()
