from __future__ import annotations

import hashlib
import json
import tempfile
import unittest
import urllib.parse
from datetime import UTC, datetime
from http import HTTPStatus
from pathlib import Path
from threading import Event
from typing import Any

from ai_companion_worker.evaluation.deployment_controller import (
    DeploymentLedger,
    ReconciliationPolicy,
)
from ai_companion_worker.evaluation.deployment_scheduler import (
    SQLiteSandboxAdapter,
    build_sandbox_request,
)
from ai_companion_worker.evaluation.deployment_shadow import (
    PROVIDER_READ_SCHEMA_VERSION,
    DeploymentShadowConfigError,
    DeploymentShadowError,
    ReadOnlyHTTPProviderAdapter,
    ReadOnlyHTTPProviderConfig,
    ShadowProviderError,
    ShadowRunner,
    ShadowRuntimeState,
    ShadowServiceConfig,
    TransportResponse,
    _http_response,
    run_shadow_reconciliation,
    run_shadow_service_loop,
)


NOW = datetime(2026, 8, 11, 8, 0, tzinfo=UTC)
TOKEN = "shadow-test-token-with-at-least-32-bytes"
POLICY = ReconciliationPolicy(
    base_delay_seconds=1,
    max_delay_seconds=4,
    max_attempts=3,
    deadline_seconds=30,
)


def make_config(**overrides: Any) -> ReadOnlyHTTPProviderConfig:
    values: dict[str, Any] = {
        "name": "sqlite-sandbox",
        "base_url": "https://preprod.provider.example/api",
        "allowed_host": "preprod.provider.example",
        "provider_instance": "preprod-blue",
        "token": TOKEN,
        "timeout_seconds": 2.0,
        "max_attempts": 2,
        "retry_base_seconds": 0.25,
    }
    values.update(overrides)
    return ReadOnlyHTTPProviderConfig(**values)


def prepare_accepted(
    ledger: DeploymentLedger,
    provider: SQLiteSandboxAdapter,
    deployment_id: str,
) -> dict[str, Any]:
    request = build_sandbox_request(deployment_id, nonce=deployment_id)
    ledger.prepare(request, NOW)
    acquired = ledger.acquire(deployment_id, provider, NOW, 30)
    if isinstance(acquired, dict):
        raise AssertionError("deployment unexpectedly terminated before submit")
    result = provider.submit(acquired.request)
    operation = ledger.finish_result(
        deployment_id,
        acquired.token,
        result,
        NOW,
        POLICY,
    )
    if operation["status"] != "accepted":
        raise AssertionError("test deployment did not become accepted")
    return operation


def provider_response(
    url: str,
    idempotency_key: str,
    status: str,
    external_operation_id: str,
    *,
    http_status: int = 200,
    retry_after: str | None = None,
) -> TransportResponse:
    body = b""
    content_type = "application/json"
    if http_status == 200:
        body = json.dumps(
            {
                "schema_version": PROVIDER_READ_SCHEMA_VERSION,
                "idempotency_key": idempotency_key,
                "status": status,
                "external_operation_id": external_operation_id,
                "updated_at": "2026-08-11T08:00:00Z",
            }
        ).encode("utf-8")
    return TransportResponse(
        status=http_status,
        body=body,
        content_type=content_type,
        final_url=url,
        retry_after=retry_after,
    )


class ObservabilityDeploymentShadowTest(unittest.TestCase):
    def test_https_allowlist_token_and_provider_identity_are_fail_closed(self) -> None:
        adapter = ReadOnlyHTTPProviderAdapter(make_config())
        first_identity = adapter.provider_instance_sha256
        rotated = ReadOnlyHTTPProviderAdapter(make_config(token="z" * 64))
        moved = ReadOnlyHTTPProviderAdapter(make_config(provider_instance="preprod-green"))

        self.assertEqual(first_identity, rotated.provider_instance_sha256)
        self.assertNotEqual(first_identity, moved.provider_instance_sha256)
        self.assertNotIn(TOKEN, repr(adapter.config))
        self.assertFalse(hasattr(adapter, "submit"))
        for invalid in (
            make_config(base_url="http://preprod.provider.example/api"),
            make_config(base_url="https://user@preprod.provider.example/api"),
            make_config(allowed_host="other.provider.example"),
            make_config(base_url="https://preprod.provider.example/api?write=true"),
            make_config(token="short"),
            make_config(max_attempts=4),
        ):
            with self.subTest(invalid=repr(invalid)):
                with self.assertRaises(DeploymentShadowConfigError):
                    ReadOnlyHTTPProviderAdapter(invalid)

    def test_retry_is_bounded_and_only_used_for_retryable_get_failures(self) -> None:
        calls: list[str] = []
        sleeps: list[float] = []
        key = "a" * 64

        def recover(
            url: str,
            token: str,
            _timeout: float,
            _limit: int,
        ) -> TransportResponse:
            self.assertEqual(token, TOKEN)
            calls.append(url)
            if len(calls) == 1:
                return provider_response(
                    url,
                    key,
                    "accepted",
                    "op-recovered",
                    http_status=503,
                    retry_after="10",
                )
            return provider_response(url, key, "accepted", "op-recovered")

        adapter = ReadOnlyHTTPProviderAdapter(
            make_config(),
            transport=recover,
            sleep=sleeps.append,
        )
        observation = adapter.lookup(key, now=NOW)
        self.assertEqual(observation.attempts, 2)
        self.assertEqual(observation.snapshot.status, "accepted")
        self.assertEqual(len(calls), 2)
        self.assertEqual(sleeps, [5.0])
        self.assertTrue(all("/by-idempotency-key/" in url for url in calls))

        denied_calls = 0

        def denied(url: str, *_args: Any) -> TransportResponse:
            nonlocal denied_calls
            denied_calls += 1
            return provider_response(
                url,
                key,
                "accepted",
                "op-denied",
                http_status=401,
            )

        with self.assertRaises(ShadowProviderError) as raised:
            ReadOnlyHTTPProviderAdapter(make_config(), transport=denied).lookup(
                key,
                now=NOW,
            )
        self.assertEqual(raised.exception.code, "provider_http_401")
        self.assertEqual(raised.exception.attempts, 1)
        self.assertEqual(denied_calls, 1)

    def test_shadow_classifies_drift_and_does_not_modify_controller_ledger(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            ledger = DeploymentLedger(root / "controller" / "ledger.sqlite3")
            sandbox = SQLiteSandboxAdapter(root / "provider" / "provider.sqlite3")
            operations = {
                name: prepare_accepted(ledger, sandbox, name)
                for name in (
                    "shadow-match",
                    "shadow-ahead",
                    "shadow-missing",
                    "shadow-id-mismatch",
                    "shadow-status-mismatch",
                    "shadow-lookup-error",
                )
            }
            ledger_before = hashlib.sha256(ledger.path.read_bytes()).hexdigest()
            operation_before = {
                name: ledger.get(name) for name in operations
            }
            events_before = {name: ledger.events(name) for name in operations}

            attempts: dict[str, int] = {}

            def transport(
                url: str,
                _token: str,
                _timeout: float,
                _limit: int,
            ) -> TransportResponse:
                key = urllib.parse.unquote(url.rsplit("/", 1)[-1])
                deployment_id = next(
                    name
                    for name, operation in operations.items()
                    if operation["idempotency_key"] == key
                )
                attempts[deployment_id] = attempts.get(deployment_id, 0) + 1
                external_id = str(operations[deployment_id]["external_operation_id"])
                if deployment_id == "shadow-missing":
                    return provider_response(
                        url,
                        key,
                        "accepted",
                        external_id,
                        http_status=404,
                    )
                if deployment_id == "shadow-id-mismatch":
                    external_id = "op-different"
                if deployment_id == "shadow-lookup-error":
                    return provider_response(
                        url,
                        key,
                        "accepted",
                        external_id,
                        http_status=503,
                    )
                provider_status = {
                    "shadow-ahead": "completed",
                    "shadow-status-mismatch": "failed",
                }.get(deployment_id, "accepted")
                return provider_response(url, key, provider_status, external_id)

            adapter = ReadOnlyHTTPProviderAdapter(
                make_config(),
                transport=transport,
                sleep=lambda _seconds: None,
            )
            report = run_shadow_reconciliation(ledger, adapter, limit=20, now=NOW)

            self.assertEqual(report["batch"]["selected"], 6)
            self.assertEqual(report["batch"]["match"], 1)
            self.assertEqual(report["batch"]["provider_ahead"], 1)
            self.assertEqual(report["batch"]["provider_missing"], 1)
            self.assertEqual(report["batch"]["external_id_mismatch"], 1)
            self.assertEqual(report["batch"]["status_mismatch"], 1)
            self.assertEqual(report["batch"]["lookup_error"], 1)
            self.assertEqual(report["batch"]["retried_lookups"], 1)
            self.assertEqual(attempts["shadow-lookup-error"], 2)
            serialized_report = json.dumps(report)
            self.assertNotIn(TOKEN, serialized_report)
            self.assertNotIn("https://preprod.provider.example", serialized_report)
            for operation in operations.values():
                self.assertNotIn(str(operation["idempotency_key"]), serialized_report)
                self.assertNotIn(str(operation["external_operation_id"]), serialized_report)
            self.assertEqual(
                hashlib.sha256(ledger.path.read_bytes()).hexdigest(),
                ledger_before,
            )
            for deployment_id in operations:
                self.assertEqual(ledger.get(deployment_id), operation_before[deployment_id])
                self.assertEqual(ledger.events(deployment_id), events_before[deployment_id])

    def test_state_metrics_persist_and_tampering_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            ledger = DeploymentLedger(root / "controller" / "ledger.sqlite3")
            adapter = ReadOnlyHTTPProviderAdapter(
                make_config(),
                transport=lambda url, *_args: TransportResponse(
                    status=404,
                    body=b"",
                    content_type="application/json",
                    final_url=url,
                ),
            )
            state = ShadowRuntimeState(root / "state", adapter, now=NOW)
            ticks = iter((1.0, 1.25))
            runner = ShadowRunner(
                lambda now: run_shadow_reconciliation(ledger, adapter, now=now),
                state,
                monotonic=lambda: next(ticks),
            )

            self.assertTrue(runner.run_iteration(NOW))
            snapshot = state.snapshot()
            self.assertTrue(snapshot["ready"])
            self.assertEqual(snapshot["successful_runs"], 1)
            self.assertEqual(snapshot["last_run_duration_seconds"], 0.25)
            metrics = state.metrics_text()
            self.assertIn("ai_companion_deployment_shadow_ready 1", metrics)
            self.assertIn("ai_companion_deployment_shadow_drift 0", metrics)
            self.assertEqual(state.path.parent.stat().st_mode & 0o777, 0o700)
            self.assertEqual(state.path.stat().st_mode & 0o777, 0o600)

            restarted = ShadowRuntimeState(root / "state", adapter, now=NOW)
            self.assertEqual(restarted.snapshot()["successful_runs"], 1)
            self.assertFalse(restarted.snapshot()["ready"])
            tampered = json.loads(state.path.read_text(encoding="utf-8"))
            tampered["provider_instance_sha256"] = "0" * 64
            state.path.write_text(json.dumps(tampered), encoding="utf-8")
            with self.assertRaises(DeploymentShadowError):
                ShadowRuntimeState(root / "state", adapter, now=NOW)

    def test_service_failure_budget_and_http_contract_are_deterministic(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            adapter = ReadOnlyHTTPProviderAdapter(make_config())
            state = ShadowRuntimeState(root / "state", adapter, now=NOW)
            ticks = iter((0.0, 0.1, 1.0, 1.1, 2.0, 2.1))

            def fail(_now: datetime) -> dict[str, Any]:
                raise DeploymentShadowError("synthetic shadow failure")

            runner = ShadowRunner(fail, state, monotonic=lambda: next(ticks))
            waits: list[float] = []
            result = run_shadow_service_loop(
                runner,
                state,
                ShadowServiceConfig(
                    interval_seconds=4,
                    max_consecutive_failures=3,
                ),
                Event(),
                now=lambda: NOW,
                wait=lambda seconds: waits.append(seconds) or False,
            )
            self.assertEqual(result, 1)
            self.assertEqual(state.snapshot()["failed_runs"], 3)
            self.assertEqual(state.snapshot()["last_error_code"], "shadow_error")
            self.assertEqual(waits, [4.0, 4.0])

            status, body, content_type = _http_response("/healthz", state)
            self.assertEqual(status, HTTPStatus.SERVICE_UNAVAILABLE)
            self.assertEqual(json.loads(body), {"status": "down"})
            self.assertEqual(content_type, "application/json")
            status, body, _content_type = _http_response("/readyz", state)
            self.assertEqual(status, HTTPStatus.SERVICE_UNAVAILABLE)
            self.assertEqual(json.loads(body), {"status": "not_ready"})
            status, body, content_type = _http_response("/metrics", state)
            self.assertEqual(status, HTTPStatus.OK)
            self.assertIn("ai_companion_deployment_shadow_consecutive_failures 3", body)
            self.assertEqual(content_type, "text/plain; version=0.0.4")
            status, body, _content_type = _http_response("/unknown", state)
            self.assertEqual(status, HTTPStatus.NOT_FOUND)
            self.assertEqual(json.loads(body), {"error": "not_found"})


if __name__ == "__main__":
    unittest.main()
