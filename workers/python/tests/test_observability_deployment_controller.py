from __future__ import annotations

import json
import os
import sqlite3
import sys
import tempfile
import threading
import time
import unittest
from concurrent.futures import ThreadPoolExecutor
from datetime import UTC, datetime, timedelta
from pathlib import Path
from typing import Any

from ai_companion_worker.evaluation.deployment_authorization import (
    issue_deployment_authorization,
)
from ai_companion_worker.evaluation.deployment_controller import (
    AdapterCertificationError,
    AdapterResult,
    DeploymentAdapterError,
    DeploymentBusyError,
    DeploymentControllerError,
    DeploymentIndeterminateError,
    DeploymentLedger,
    DeploymentNotDueError,
    DryRunAdapter,
    ReconciliationPolicy,
    certify_adapter,
    dispatch_deployment,
    plan_deployment,
    prepare_deployment,
    reconcile_deployment,
    run_reconciliation_batch,
)
from ai_companion_worker.evaluation.observability import build_snapshot
from ai_companion_worker.evaluation.release_attestation import create_attestation
from ai_companion_worker.evaluation.release_gate import run_gate


ROOT = Path(__file__).resolve().parents[3]
BASELINE = ROOT / "evals" / "observability" / "baselines" / "release.v1.json"
KEY = b"deployment-controller-test-key-32-bytes-minimum"
KEY_ID = "deployment-controller-test-v1"
TEST_RECONCILIATION_POLICY = ReconciliationPolicy(
    base_delay_seconds=1,
    max_delay_seconds=4,
    max_attempts=3,
    deadline_seconds=30,
)


def metrics_text() -> str:
    return "\n".join(
        [
            "ai_companion_degradation_level 0",
            "ai_companion_queue_lag 0",
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


def make_authorized_bundle(
    root: Path,
    deployment_id: str,
) -> tuple[Path, Path, datetime]:
    spec = root / f"{deployment_id}.canary.json"
    spec.write_text(
        json.dumps(
            {
                "schema_version": "observability-canary-command-v1",
                "name": "controller-test",
                "command": [sys.executable, "-c", "pass"],
                "timeout_seconds": 2,
                "stabilization_seconds": 0,
            }
        ),
        encoding="utf-8",
    )
    gate_dir = root / f"release-gate-{deployment_id}"
    report, result = run_gate(
        "http://metrics.invalid/metrics",
        BASELINE,
        spec,
        gate_dir,
        capture=lambda _url: build_snapshot(metrics_text()),
    )
    if result != 0:
        raise AssertionError("test Gate did not promote")
    gate_time = datetime.fromisoformat(
        str(report["generated_at"]).replace("Z", "+00:00")
    ).astimezone(UTC)
    signed_at = gate_time + timedelta(seconds=1)
    create_attestation(gate_dir, KEY, KEY_ID, now=signed_at)
    authorization, created = issue_deployment_authorization(
        gate_dir,
        root / "authorizations",
        KEY,
        KEY_ID,
        deployment_id,
        now=signed_at + timedelta(seconds=1),
    )
    if not created:
        raise AssertionError("test authorization was not created")
    authorization_path = (
        root / "authorizations" / f"{authorization['deployment_id']}.authorization.json"
    )
    return gate_dir, authorization_path, signed_at + timedelta(seconds=2)


class CountingAdapter:
    name = "counting-adapter"
    supports_idempotent_submit = True
    supports_lookup = False

    def __init__(self, delay: float = 0.0) -> None:
        self.delay = delay
        self.calls = 0
        self.keys: list[str] = []
        self._lock = threading.Lock()

    def submit(self, request: Any) -> AdapterResult:
        with self._lock:
            self.calls += 1
            self.keys.append(str(request.idempotency_key))
        if self.delay:
            time.sleep(self.delay)
        return AdapterResult(
            status="completed",
            external_operation_id=f"op-{request.idempotency_key[:16]}",
        )

    def lookup(self, _idempotency_key: str) -> AdapterResult | None:
        return None


class RetryOnceAdapter(CountingAdapter):
    name = "retry-once-adapter"

    def submit(self, request: Any) -> AdapterResult:
        with self._lock:
            self.calls += 1
            self.keys.append(str(request.idempotency_key))
            call = self.calls
        if call == 1:
            raise DeploymentAdapterError("temporary_failure", retryable=True)
        return AdapterResult(status="completed", external_operation_id="op-recovered")


class LookupAdapter(CountingAdapter):
    name = "lookup-adapter"
    supports_idempotent_submit = False
    supports_lookup = True

    def __init__(self) -> None:
        super().__init__()
        self.lookups = 0

    def lookup(self, _idempotency_key: str) -> AdapterResult | None:
        self.lookups += 1
        return AdapterResult(status="completed", external_operation_id="op-reconciled")


class UnsafeAdapter(CountingAdapter):
    name = "unsafe-adapter"
    supports_idempotent_submit = False
    supports_lookup = False


class InvalidResultAdapter(CountingAdapter):
    name = "invalid-result-adapter"

    def submit(self, request: Any) -> AdapterResult:
        self.calls += 1
        self.keys.append(str(request.idempotency_key))
        return AdapterResult(status="accepted")


class AsyncAdapter(CountingAdapter):
    name = "async-adapter"
    supports_lookup = True

    def __init__(self, lookup_results: list[AdapterResult | None] | None = None) -> None:
        super().__init__()
        self.lookups = 0
        self.lookup_results = lookup_results or [
            AdapterResult(status="completed", external_operation_id="op-async")
        ]

    def submit(self, request: Any) -> AdapterResult:
        with self._lock:
            self.calls += 1
            self.keys.append(str(request.idempotency_key))
        return AdapterResult(status="accepted", external_operation_id="op-async")

    def lookup(self, _idempotency_key: str) -> AdapterResult | None:
        self.lookups += 1
        index = min(self.lookups - 1, len(self.lookup_results) - 1)
        return self.lookup_results[index]


class SlowAsyncAdapter(AsyncAdapter):
    name = "slow-async-adapter"

    def lookup(self, idempotency_key: str) -> AdapterResult | None:
        time.sleep(0.05)
        return super().lookup(idempotency_key)


class RetryLookupAdapter(AsyncAdapter):
    name = "retry-lookup-adapter"

    def lookup(self, idempotency_key: str) -> AdapterResult | None:
        self.lookups += 1
        if self.lookups == 1:
            raise DeploymentAdapterError("temporary_lookup_failure", retryable=True)
        return AdapterResult(status="completed", external_operation_id="op-async")


class ChangingOperationAdapter(AsyncAdapter):
    name = "changing-operation-adapter"

    def submit(self, request: Any) -> AdapterResult:
        self.calls += 1
        self.keys.append(str(request.idempotency_key))
        return AdapterResult(
            status="accepted",
            external_operation_id=f"op-changing-{self.calls}",
        )

    def lookup(self, _idempotency_key: str) -> AdapterResult | None:
        return AdapterResult(status="completed", external_operation_id="op-changing-lookup")


class ObservabilityDeploymentControllerTest(unittest.TestCase):
    def test_plan_is_read_only_and_prepare_is_private_and_idempotent(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            gate_dir, authorization_path, now = make_authorized_bundle(
                root,
                "deploy-controller-plan",
            )
            ledger_path = root / "controller" / "ledger.sqlite3"
            request = plan_deployment(
                gate_dir,
                authorization_path,
                KEY,
                KEY_ID,
                "deploy-controller-plan",
                "staging",
                DryRunAdapter.name,
                now=now,
            )
            self.assertFalse(ledger_path.parent.exists())
            self.assertEqual(len(request.request_sha256), 64)

            ledger = DeploymentLedger(ledger_path)
            first, first_created = prepare_deployment(
                ledger,
                gate_dir,
                authorization_path,
                KEY,
                KEY_ID,
                "deploy-controller-plan",
                "staging",
                DryRunAdapter.name,
                now=now,
            )
            second, second_created = prepare_deployment(
                ledger,
                gate_dir,
                authorization_path,
                KEY,
                KEY_ID,
                "deploy-controller-plan",
                "staging",
                DryRunAdapter.name,
                now=now,
            )
            self.assertTrue(first_created)
            self.assertFalse(second_created)
            self.assertEqual(first, second)
            self.assertEqual(ledger.events("deploy-controller-plan")[0]["event_type"], "prepared")
            self.assertEqual(ledger_path.parent.stat().st_mode & 0o777, 0o700)
            self.assertEqual(ledger_path.stat().st_mode & 0o777, 0o600)

    def test_dry_run_simulates_once_without_external_operation(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            gate_dir, authorization_path, now = make_authorized_bundle(
                root,
                "deploy-controller-dry-run",
            )
            ledger = DeploymentLedger(root / "controller" / "ledger.sqlite3")
            first = dispatch_deployment(
                ledger,
                gate_dir,
                authorization_path,
                KEY,
                KEY_ID,
                "deploy-controller-dry-run",
                "local-dry-run",
                DryRunAdapter(),
                now=now,
            )
            second = dispatch_deployment(
                ledger,
                gate_dir,
                authorization_path,
                KEY,
                KEY_ID,
                "deploy-controller-dry-run",
                "local-dry-run",
                DryRunAdapter(),
                now=now + timedelta(seconds=1),
            )
            self.assertEqual(first["status"], "simulated")
            self.assertEqual(first, second)
            self.assertIsNone(first["external_operation_id"])
            self.assertEqual(first["attempt_count"], 1)
            self.assertEqual(
                [event["event_type"] for event in ledger.events("deploy-controller-dry-run")],
                ["prepared", "dispatch_started", "simulated"],
            )

    def test_concurrent_dispatch_submits_once(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            deployment_id = "deploy-controller-concurrent"
            gate_dir, authorization_path, now = make_authorized_bundle(root, deployment_id)
            ledger = DeploymentLedger(root / "controller" / "ledger.sqlite3")
            adapter = CountingAdapter(delay=0.05)

            def dispatch() -> str:
                try:
                    result = dispatch_deployment(
                        ledger,
                        gate_dir,
                        authorization_path,
                        KEY,
                        KEY_ID,
                        deployment_id,
                        "production-canary",
                        adapter,
                        now=now,
                    )
                    return str(result["status"])
                except DeploymentBusyError:
                    return "busy"

            with ThreadPoolExecutor(max_workers=12) as executor:
                results = list(executor.map(lambda _index: dispatch(), range(24)))

            self.assertEqual(adapter.calls, 1)
            self.assertEqual(len(set(adapter.keys)), 1)
            self.assertIn("completed", results)
            self.assertEqual(ledger.get(deployment_id)["attempt_count"], 1)

    def test_retryable_failure_reuses_idempotency_key_and_attempts_twice(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            deployment_id = "deploy-controller-retry"
            gate_dir, authorization_path, now = make_authorized_bundle(root, deployment_id)
            ledger = DeploymentLedger(root / "controller" / "ledger.sqlite3")
            adapter = RetryOnceAdapter()

            with self.assertRaisesRegex(DeploymentControllerError, "temporary_failure"):
                dispatch_deployment(
                    ledger,
                    gate_dir,
                    authorization_path,
                    KEY,
                    KEY_ID,
                    deployment_id,
                    "production-canary",
                    adapter,
                    now=now,
                )
            self.assertEqual(ledger.get(deployment_id)["status"], "retryable_failed")

            completed = dispatch_deployment(
                ledger,
                gate_dir,
                authorization_path,
                KEY,
                KEY_ID,
                deployment_id,
                "production-canary",
                adapter,
                now=now + timedelta(seconds=1),
            )
            self.assertEqual(completed["status"], "completed")
            self.assertEqual(completed["attempt_count"], 2)
            self.assertEqual(len(set(adapter.keys)), 1)

    def test_stale_lease_reconciles_by_lookup_without_resubmit(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            deployment_id = "deploy-controller-reconcile"
            gate_dir, authorization_path, now = make_authorized_bundle(root, deployment_id)
            ledger = DeploymentLedger(root / "controller" / "ledger.sqlite3")
            adapter = LookupAdapter()
            prepare_deployment(
                ledger,
                gate_dir,
                authorization_path,
                KEY,
                KEY_ID,
                deployment_id,
                "production-canary",
                adapter.name,
                now=now,
            )
            lease = ledger.acquire(deployment_id, adapter, now, 1)
            self.assertFalse(isinstance(lease, dict))

            reconciled = dispatch_deployment(
                ledger,
                gate_dir,
                authorization_path,
                KEY,
                KEY_ID,
                deployment_id,
                "production-canary",
                adapter,
                now=now + timedelta(seconds=2),
            )
            self.assertEqual(reconciled["status"], "completed")
            self.assertEqual(reconciled["external_operation_id"], "op-reconciled")
            self.assertEqual(reconciled["attempt_count"], 2)
            self.assertEqual(adapter.lookups, 1)
            self.assertEqual(adapter.calls, 0)

    def test_stale_non_idempotent_dispatch_becomes_indeterminate(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            deployment_id = "deploy-controller-unsafe"
            gate_dir, authorization_path, now = make_authorized_bundle(root, deployment_id)
            ledger = DeploymentLedger(root / "controller" / "ledger.sqlite3")
            adapter = UnsafeAdapter()
            prepare_deployment(
                ledger,
                gate_dir,
                authorization_path,
                KEY,
                KEY_ID,
                deployment_id,
                "production-canary",
                adapter.name,
                now=now,
            )
            ledger.acquire(deployment_id, adapter, now, 1)

            with self.assertRaisesRegex(DeploymentIndeterminateError, "cannot be retried"):
                dispatch_deployment(
                    ledger,
                    gate_dir,
                    authorization_path,
                    KEY,
                    KEY_ID,
                    deployment_id,
                    "production-canary",
                    adapter,
                    now=now + timedelta(seconds=2),
                )
            self.assertEqual(ledger.get(deployment_id)["status"], "indeterminate")
            self.assertEqual(adapter.calls, 0)

    def test_accepted_operation_reconciles_pending_then_completed(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            deployment_id = "deploy-controller-accepted"
            gate_dir, authorization_path, now = make_authorized_bundle(root, deployment_id)
            ledger = DeploymentLedger(root / "controller" / "ledger.sqlite3")
            adapter = AsyncAdapter(
                [
                    AdapterResult(status="accepted", external_operation_id="op-async"),
                    AdapterResult(status="completed", external_operation_id="op-async"),
                ]
            )
            accepted = dispatch_deployment(
                ledger,
                gate_dir,
                authorization_path,
                KEY,
                KEY_ID,
                deployment_id,
                "production-canary",
                adapter,
                reconciliation_policy=TEST_RECONCILIATION_POLICY,
                now=now,
            )
            pending = reconcile_deployment(
                ledger,
                deployment_id,
                adapter,
                now=now + timedelta(seconds=1),
            )
            completed = reconcile_deployment(
                ledger,
                deployment_id,
                adapter,
                now=now + timedelta(seconds=3),
            )

            self.assertEqual(accepted["status"], "accepted")
            self.assertEqual(pending["status"], "accepted")
            self.assertEqual(completed["status"], "completed")
            self.assertEqual(completed["attempt_count"], 1)
            self.assertEqual(completed["reconciliation_count"], 2)
            self.assertEqual(adapter.calls, 1)
            self.assertEqual(adapter.lookups, 2)
            self.assertEqual(
                [event["event_type"] for event in ledger.events(deployment_id)],
                [
                    "prepared",
                    "dispatch_started",
                    "accepted",
                    "reconciliation_started",
                    "reconciliation_pending",
                    "reconciliation_started",
                    "reconciliation_completed",
                ],
            )

    def test_reconciliation_schedule_defers_backs_off_and_exhausts_budget(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            deployment_id = "deploy-controller-reconcile-schedule"
            gate_dir, authorization_path, now = make_authorized_bundle(root, deployment_id)
            ledger = DeploymentLedger(root / "controller" / "ledger.sqlite3")
            policy = ReconciliationPolicy(
                base_delay_seconds=2,
                max_delay_seconds=4,
                max_attempts=3,
                deadline_seconds=30,
            )
            adapter = AsyncAdapter(
                [AdapterResult(status="accepted", external_operation_id="op-async")]
            )
            dispatch_deployment(
                ledger,
                gate_dir,
                authorization_path,
                KEY,
                KEY_ID,
                deployment_id,
                "production-canary",
                adapter,
                reconciliation_policy=policy,
                now=now,
            )

            schedule = ledger.schedule(deployment_id)
            self.assertIsNotNone(schedule)
            self.assertEqual(schedule["last_delay_seconds"], 2)
            self.assertEqual(ledger.due_reconciliations(now + timedelta(seconds=1)), [])
            health = ledger.reconciliation_health(now + timedelta(seconds=1))
            self.assertEqual(health["accepted_pending"], 1)
            self.assertEqual(health["due"], 0)
            with self.assertRaises(DeploymentNotDueError):
                reconcile_deployment(
                    ledger,
                    deployment_id,
                    adapter,
                    now=now + timedelta(seconds=1),
                )
            self.assertEqual(adapter.lookups, 0)

            first_pending = reconcile_deployment(
                ledger,
                deployment_id,
                adapter,
                now=now + timedelta(seconds=2),
            )
            self.assertEqual(first_pending["status"], "accepted")
            self.assertEqual(ledger.schedule(deployment_id)["last_delay_seconds"], 4)
            self.assertEqual(ledger.due_reconciliations(now + timedelta(seconds=5)), [])
            second_pending = reconcile_deployment(
                ledger,
                deployment_id,
                adapter,
                now=now + timedelta(seconds=6),
            )
            self.assertEqual(second_pending["status"], "accepted")
            exhausted = reconcile_deployment(
                ledger,
                deployment_id,
                adapter,
                now=now + timedelta(seconds=10),
            )
            self.assertEqual(exhausted["status"], "indeterminate")
            self.assertEqual(exhausted["error_code"], "reconciliation_budget_exhausted")
            self.assertEqual(adapter.lookups, 3)
            self.assertEqual(
                ledger.events(deployment_id)[-1]["event_type"],
                "reconciliation_budget_exhausted",
            )

    def test_reconciliation_deadline_exhaustion_avoids_provider_lookup(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            deployment_id = "deploy-controller-reconcile-deadline"
            gate_dir, authorization_path, now = make_authorized_bundle(root, deployment_id)
            ledger = DeploymentLedger(root / "controller" / "ledger.sqlite3")
            adapter = AsyncAdapter()
            dispatch_deployment(
                ledger,
                gate_dir,
                authorization_path,
                KEY,
                KEY_ID,
                deployment_id,
                "production-canary",
                adapter,
                reconciliation_policy=ReconciliationPolicy(
                    base_delay_seconds=1,
                    max_delay_seconds=1,
                    max_attempts=10,
                    deadline_seconds=2,
                ),
                now=now,
            )
            self.assertEqual(
                ledger.due_reconciliations(now + timedelta(seconds=2)),
                [deployment_id],
            )
            with self.assertRaisesRegex(DeploymentIndeterminateError, "deadline"):
                reconcile_deployment(
                    ledger,
                    deployment_id,
                    adapter,
                    now=now + timedelta(seconds=2),
                )
            operation = ledger.get(deployment_id)
            self.assertEqual(operation["status"], "indeterminate")
            self.assertEqual(operation["error_code"], "reconciliation_deadline_exhausted")
            self.assertEqual(adapter.lookups, 0)

    def test_reconciliation_batch_selects_only_due_operations(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            deployment_id = "deploy-controller-reconcile-batch"
            gate_dir, authorization_path, now = make_authorized_bundle(root, deployment_id)
            ledger = DeploymentLedger(root / "controller" / "ledger.sqlite3")
            adapter = AsyncAdapter()
            dispatch_deployment(
                ledger,
                gate_dir,
                authorization_path,
                KEY,
                KEY_ID,
                deployment_id,
                "production-canary",
                adapter,
                reconciliation_policy=TEST_RECONCILIATION_POLICY,
                now=now,
            )
            early = run_reconciliation_batch(
                ledger,
                {adapter.name: adapter},
                now=now,
            )
            self.assertEqual(early["selected"], 0)
            self.assertEqual(adapter.lookups, 0)
            completed = run_reconciliation_batch(
                ledger,
                {adapter.name: adapter},
                now=now + timedelta(seconds=1),
            )
            self.assertEqual(completed["selected"], 1)
            self.assertEqual(completed["completed"], 1)
            self.assertEqual(adapter.lookups, 1)
            health = ledger.reconciliation_health(now + timedelta(seconds=1))
            self.assertEqual(health["accepted_pending"], 0)
            self.assertEqual(health["due"], 0)

    def test_concurrent_reconciliation_performs_one_lookup(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            deployment_id = "deploy-controller-reconcile-concurrent"
            gate_dir, authorization_path, now = make_authorized_bundle(root, deployment_id)
            ledger = DeploymentLedger(root / "controller" / "ledger.sqlite3")
            adapter = SlowAsyncAdapter()
            dispatch_deployment(
                ledger,
                gate_dir,
                authorization_path,
                KEY,
                KEY_ID,
                deployment_id,
                "production-canary",
                adapter,
                reconciliation_policy=TEST_RECONCILIATION_POLICY,
                now=now,
            )

            def reconcile() -> str:
                try:
                    result = reconcile_deployment(
                        ledger,
                        deployment_id,
                        adapter,
                        now=now + timedelta(seconds=1),
                    )
                    return str(result["status"])
                except DeploymentBusyError:
                    return "busy"

            with ThreadPoolExecutor(max_workers=12) as executor:
                results = list(executor.map(lambda _index: reconcile(), range(24)))

            self.assertIn("completed", results)
            self.assertEqual(adapter.calls, 1)
            self.assertEqual(adapter.lookups, 1)
            self.assertEqual(ledger.get(deployment_id)["reconciliation_count"], 1)

    def test_reconciliation_retry_miss_and_operation_mismatch_fail_closed(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            deployment_id = "deploy-controller-reconcile-retry"
            gate_dir, authorization_path, now = make_authorized_bundle(root, deployment_id)
            ledger = DeploymentLedger(root / "controller" / "ledger.sqlite3")
            adapter = RetryLookupAdapter()
            dispatch_deployment(
                ledger,
                gate_dir,
                authorization_path,
                KEY,
                KEY_ID,
                deployment_id,
                "production-canary",
                adapter,
                reconciliation_policy=TEST_RECONCILIATION_POLICY,
                now=now,
            )
            with self.assertRaisesRegex(DeploymentControllerError, "temporary_lookup_failure"):
                reconcile_deployment(
                    ledger,
                    deployment_id,
                    adapter,
                    now=now + timedelta(seconds=1),
                )
            self.assertEqual(ledger.get(deployment_id)["status"], "accepted")
            self.assertEqual(ledger.get(deployment_id)["error_code"], "temporary_lookup_failure")
            completed = reconcile_deployment(
                ledger,
                deployment_id,
                adapter,
                now=now + timedelta(seconds=3),
            )
            self.assertEqual(completed["status"], "completed")
            self.assertEqual(completed["reconciliation_count"], 2)

        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            deployment_id = "deploy-controller-reconcile-miss"
            gate_dir, authorization_path, now = make_authorized_bundle(root, deployment_id)
            ledger = DeploymentLedger(root / "controller" / "ledger.sqlite3")
            miss_adapter = AsyncAdapter([None])
            dispatch_deployment(
                ledger,
                gate_dir,
                authorization_path,
                KEY,
                KEY_ID,
                deployment_id,
                "production-canary",
                miss_adapter,
                reconciliation_policy=TEST_RECONCILIATION_POLICY,
                now=now,
            )
            with self.assertRaisesRegex(DeploymentIndeterminateError, "was not found"):
                reconcile_deployment(
                    ledger,
                    deployment_id,
                    miss_adapter,
                    now=now + timedelta(seconds=1),
                )
            self.assertEqual(ledger.get(deployment_id)["status"], "indeterminate")

        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            deployment_id = "deploy-controller-reconcile-mismatch"
            gate_dir, authorization_path, now = make_authorized_bundle(root, deployment_id)
            ledger = DeploymentLedger(root / "controller" / "ledger.sqlite3")
            mismatch_adapter = AsyncAdapter(
                [AdapterResult(status="completed", external_operation_id="op-other")]
            )
            dispatch_deployment(
                ledger,
                gate_dir,
                authorization_path,
                KEY,
                KEY_ID,
                deployment_id,
                "production-canary",
                mismatch_adapter,
                reconciliation_policy=TEST_RECONCILIATION_POLICY,
                now=now,
            )
            with self.assertRaisesRegex(DeploymentIndeterminateError, "different external"):
                reconcile_deployment(
                    ledger,
                    deployment_id,
                    mismatch_adapter,
                    now=now + timedelta(seconds=1),
                )
            self.assertEqual(ledger.get(deployment_id)["status"], "indeterminate")
            self.assertEqual(
                ledger.get(deployment_id)["error_code"],
                "external_operation_mismatch",
            )

    def test_adapter_certification_proves_idempotent_submit_and_lookup(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            deployment_id = "deploy-controller-certification"
            gate_dir, authorization_path, now = make_authorized_bundle(root, deployment_id)
            request = plan_deployment(
                gate_dir,
                authorization_path,
                KEY,
                KEY_ID,
                deployment_id,
                "isolated-certification",
                AsyncAdapter.name,
                now=now,
            )
            adapter = AsyncAdapter()
            report = certify_adapter(adapter, request)
            self.assertTrue(report["passed"])
            self.assertEqual(report["submit_statuses"], ["accepted", "accepted"])
            self.assertEqual(report["lookup_status"], "completed")
            self.assertNotIn("op-async", json.dumps(report))

            changing_request = plan_deployment(
                gate_dir,
                authorization_path,
                KEY,
                KEY_ID,
                deployment_id,
                "isolated-certification",
                ChangingOperationAdapter.name,
                now=now,
            )
            with self.assertRaisesRegex(AdapterCertificationError, "one external operation"):
                certify_adapter(ChangingOperationAdapter(), changing_request)
            unsafe_request = plan_deployment(
                gate_dir,
                authorization_path,
                KEY,
                KEY_ID,
                deployment_id,
                "isolated-certification",
                UnsafeAdapter.name,
                now=now,
            )
            with self.assertRaisesRegex(AdapterCertificationError, "requires idempotent"):
                certify_adapter(UnsafeAdapter(), unsafe_request)

    def test_v1_ledger_is_migrated_to_v4_without_losing_operations(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            controller_dir = root / "controller"
            controller_dir.mkdir(mode=0o700)
            ledger_path = controller_dir / "ledger.sqlite3"
            connection = sqlite3.connect(ledger_path)
            connection.executescript(
                """
                CREATE TABLE deployment_operations (
                    deployment_id TEXT PRIMARY KEY,
                    authorization_id TEXT NOT NULL UNIQUE,
                    idempotency_key TEXT NOT NULL UNIQUE,
                    run_id TEXT NOT NULL,
                    target TEXT NOT NULL,
                    adapter TEXT NOT NULL,
                    authorization_sha256 TEXT NOT NULL,
                    gate_report_sha256 TEXT NOT NULL,
                    request_sha256 TEXT NOT NULL,
                    status TEXT NOT NULL,
                    attempt_count INTEGER NOT NULL,
                    lease_token TEXT,
                    lease_expires_at TEXT,
                    external_operation_id TEXT,
                    error_code TEXT,
                    created_at TEXT NOT NULL,
                    updated_at TEXT NOT NULL
                );
                CREATE TABLE deployment_events (
                    id INTEGER PRIMARY KEY AUTOINCREMENT,
                    deployment_id TEXT NOT NULL,
                    sequence INTEGER NOT NULL,
                    event_type TEXT NOT NULL,
                    status TEXT NOT NULL,
                    attempt INTEGER NOT NULL,
                    occurred_at TEXT NOT NULL,
                    UNIQUE (deployment_id, sequence)
                );
                INSERT INTO deployment_operations VALUES (
                    'deploy-v1', 'auth-v1',
                    'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
                    'run-v1', 'local-dry-run', 'dry-run',
                    'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',
                    'cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc',
                    'dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd',
                    'simulated', 1, NULL, NULL, NULL, NULL,
                    '2026-08-10T00:00:00Z', '2026-08-10T00:00:00Z'
                );
                PRAGMA user_version = 1;
                """
            )
            connection.commit()
            connection.close()
            ledger_path.chmod(0o600)

            ledger = DeploymentLedger(ledger_path)
            operation = ledger.get("deploy-v1")
            self.assertEqual(operation["status"], "simulated")
            self.assertEqual(operation["reconciliation_count"], 0)
            connection = sqlite3.connect(ledger_path)
            version = connection.execute("PRAGMA user_version").fetchone()[0]
            connection.close()
            self.assertEqual(version, 4)
            self.assertIsNone(ledger.schedule("deploy-v1"))

    def test_v2_accepted_operation_is_backfilled_during_v4_migration(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            deployment_id = "deploy-v2-accepted"
            gate_dir, authorization_path, now = make_authorized_bundle(root, deployment_id)
            ledger_path = root / "controller" / "ledger.sqlite3"
            ledger = DeploymentLedger(ledger_path)
            prepare_deployment(
                ledger,
                gate_dir,
                authorization_path,
                KEY,
                KEY_ID,
                deployment_id,
                "production-canary",
                AsyncAdapter.name,
                now=now,
            )
            connection = sqlite3.connect(ledger_path)
            connection.execute("DROP TABLE deployment_resolutions")
            connection.execute("DROP TABLE deployment_reconciliation_schedule")
            connection.execute(
                """
                UPDATE deployment_operations
                SET status = 'accepted', external_operation_id = 'op-migrated',
                    updated_at = ?
                WHERE deployment_id = ?
                """,
                (now.isoformat().replace("+00:00", "Z"), deployment_id),
            )
            connection.execute("PRAGMA user_version = 2")
            connection.commit()
            connection.close()

            migrated = DeploymentLedger(ledger_path)
            operation = migrated.get(deployment_id)
            schedule = migrated.schedule(deployment_id)
            self.assertEqual(operation["status"], "accepted")
            self.assertIsNotNone(schedule)
            self.assertEqual(schedule["next_reconcile_at"], schedule["accepted_at"])
            self.assertEqual(
                migrated.due_reconciliations(now),
                [deployment_id],
            )
            connection = sqlite3.connect(ledger_path)
            version = connection.execute("PRAGMA user_version").fetchone()[0]
            connection.close()
            self.assertEqual(version, 4)

    def test_invalid_adapter_result_and_insecure_ledger_fail_closed(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            deployment_id = "deploy-controller-invalid-result"
            gate_dir, authorization_path, now = make_authorized_bundle(root, deployment_id)
            ledger_path = root / "controller" / "ledger.sqlite3"
            ledger = DeploymentLedger(ledger_path)
            adapter = InvalidResultAdapter()
            with self.assertRaisesRegex(DeploymentIndeterminateError, "result is invalid"):
                dispatch_deployment(
                    ledger,
                    gate_dir,
                    authorization_path,
                    KEY,
                    KEY_ID,
                    deployment_id,
                    "production-canary",
                    adapter,
                    now=now,
                )
            operation = ledger.get(deployment_id)
            self.assertEqual(operation["status"], "indeterminate")
            self.assertEqual(operation["error_code"], "adapter_contract")

            ledger_path.chmod(0o644)
            with self.assertRaisesRegex(DeploymentControllerError, "permissions must be 0600"):
                DeploymentLedger(ledger_path, create=False)

        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            missing = root / "missing" / "ledger.sqlite3"
            with self.assertRaisesRegex(DeploymentControllerError, "cannot inspect"):
                DeploymentLedger(missing, create=False)
            self.assertFalse(missing.parent.exists())

        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            ledger_path = root / "controller" / "ledger.sqlite3"
            DeploymentLedger(ledger_path)
            outside = root / "outside.sqlite3"
            ledger_path.replace(outside)
            os.symlink(outside, ledger_path)
            with self.assertRaisesRegex(DeploymentControllerError, "regular file"):
                DeploymentLedger(ledger_path, create=False)

        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            controller_dir = root / "controller"
            controller_dir.mkdir(mode=0o700)
            ledger_path = controller_dir / "ledger.sqlite3"
            connection = sqlite3.connect(ledger_path)
            connection.execute("CREATE TABLE unrelated_data (id INTEGER PRIMARY KEY)")
            connection.commit()
            connection.close()
            ledger_path.chmod(0o600)
            with self.assertRaisesRegex(DeploymentControllerError, "not an empty"):
                DeploymentLedger(ledger_path)


if __name__ == "__main__":
    unittest.main()
