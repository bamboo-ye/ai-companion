from __future__ import annotations

import hashlib
import json
import sqlite3
import tempfile
import unittest
import urllib.request
from concurrent.futures import ThreadPoolExecutor
from dataclasses import dataclass, replace
from datetime import UTC, datetime, timedelta
from pathlib import Path
from typing import Any, Literal
from unittest.mock import patch

from ai_companion_worker.evaluation.deployment_controller import (
    _canonical_json,
    _format_timestamp,
)
from ai_companion_worker.evaluation.direct_rollout import (
    create_attestation,
    evaluate_rollout,
    load_policy,
    write_rollout_report,
)
from ai_companion_worker.evaluation.direct_rollout_traffic import (
    SQLiteDirectTrafficSandbox,
)
from ai_companion_worker.evaluation.direct_traffic_certification import (
    DirectTrafficCertificationError,
    create_certification,
)
from ai_companion_worker.evaluation.direct_traffic_preproduction import (
    ADAPTER_NAME,
    DirectTrafficPreproductionConfigError,
    DirectTrafficPreproductionConflictError,
    DirectTrafficPreproductionIndeterminateError,
    HTTPSPreproductionConfig,
    HTTPSPreproductionDirectTrafficAdapter,
    PreproductionProviderError,
    TransportResponse,
    VerifiedPreproductionExecution,
    _default_transport,
    apply_verified_preproduction_execution,
    verify_preproduction_execution,
)
from ai_companion_worker.evaluation.direct_traffic_preproduction_sandbox import (
    SQLitePreproductionCertificationAdapter,
    SQLitePreproductionPlatform,
)
from ai_companion_worker.evaluation.direct_traffic_shadow import (
    DirectTrafficShadowHistory,
)
from ai_companion_worker.evaluation.direct_traffic_shadow_gate import (
    DirectTrafficShadowGatePolicy,
    DirectTrafficShadowGateVerificationError,
    create_direct_traffic_shadow_attestation,
    evaluate_direct_traffic_shadow_history,
    write_direct_traffic_shadow_gate_report,
)
from ai_companion_worker.evaluation.release_gate_history import (
    ReleaseGateHistory,
    environment_sha256,
)


ROOT = Path(__file__).resolve().parents[3]
POLICY_PATH = ROOT / "evals" / "agent" / "baselines" / "direct-rollout.v1.json"
NOW = datetime(2026, 8, 12, 12, 0, tzinfo=UTC)
VERIFY_NOW = NOW + timedelta(seconds=66)
ENVIRONMENT_ID = "preproduction-cn"
ENVIRONMENT_SHA256 = environment_sha256(ENVIRONMENT_ID)
PROVIDER_INSTANCE = "platform-preproduction-blue"
NAMESPACE_ID = "preproduction-agent-direct-blue"
BASE_URL = "https://traffic.provider.example/api"
ALLOWED_HOST = "traffic.provider.example"
TOKEN = "preproduction-provider-token-at-least-32-bytes"
ROLLOUT_KEY = b"preproduction-rollout-key-at-least-32-bytes"
ROLLOUT_KEY_ID = "preproduction-rollout-v1"
CERTIFICATION_KEY = b"preproduction-certification-key-at-least-32-bytes"
CERTIFICATION_KEY_ID = "preproduction-certification-v1"
SHADOW_KEY = b"preproduction-shadow-gate-key-at-least-32-bytes"
SHADOW_KEY_ID = "preproduction-shadow-gate-v1"


def healthy_summary() -> dict[str, int | float | bool]:
    return {
        "cases": 5,
        "completed": 5,
        "assistant_messages": 5,
        "assistant_delivery_violations": 0,
        "direct_runs": 5,
        "direct_rate": 1.0,
        "single_model_call_runs": 5,
        "single_model_call_rate": 1.0,
        "quality_passed_runs": 5,
        "quality_pass_rate": 1.0,
        "content_passed_runs": 5,
        "content_pass_rate": 1.0,
        "repair_attempted_runs": 0,
        "repair_succeeded_runs": 0,
        "repair_success_rate": 1.0,
        "deterministic_repairs": 0,
        "rewrite_runs": 0,
        "response_quality_failures": 0,
        "duplicate_violations": 0,
        "planned_runs": 0,
        "tool_actions": 0,
        "model_calls": 5,
        "duration_p95_ms": 4000.0,
        "duration_max_ms": 4000,
        "repair_probe_passed": True,
    }


def append_release_gate(
    history: ReleaseGateHistory,
    root: Path,
    sequence: int,
    observed_at: datetime,
) -> None:
    gate_dir = root / f"release-gate-{sequence:03d}"
    gate_dir.mkdir(mode=0o700)
    report = {
        "schema_version": "observability-release-gate-report-v1",
        "generated_at": _format_timestamp(observed_at),
        "decision": "promote",
        "source": {},
        "canary": {
            "name": "agent-direct",
            "status": "passed",
            "exit_code": 0,
            "duration_ms": 5000,
            "report": {
                "schema_version": "agent-direct-canary-report-v1",
                "decision": "pass",
                "baseline_version": "agent-direct-canary-baseline-v1",
                "summary": healthy_summary(),
            },
        },
        "phases": {},
        "violations": [],
    }
    report_path = gate_dir / "gate-report.json"
    report_path.write_text(json.dumps(report), encoding="utf-8")
    report_path.chmod(0o600)
    xml_path = gate_dir / "gate-report.xml"
    xml_path.write_text("<testsuite/>", encoding="utf-8")
    xml_path.chmod(0o600)
    history.record(gate_dir, report)


def shadow_report(
    adapter: HTTPSPreproductionDirectTrafficAdapter,
    sandbox: SQLiteDirectTrafficSandbox,
    observed_at: datetime,
) -> dict[str, Any]:
    state = sandbox.status()
    public = {
        "current_traffic_percent": state["current_traffic_percent"],
        "revision": state["revision"],
        "history_chain_sha256": state["history_chain_sha256"],
    }
    return {
        "schema_version": "agent-direct-traffic-shadow-report-v1",
        "generated_at": _format_timestamp(observed_at),
        "observer_instance_sha256": adapter.observer_instance_sha256,
        "provider_instance_sha256": adapter.provider_instance_sha256,
        "environment_sha256": adapter.environment_sha256,
        "outcome": "match",
        "drift_fields": [],
        "attempts": 1,
        "duration_seconds": 0.1,
        "error_code": None,
        "local": public,
        "provider": {
            **public,
            "observed_at": _format_timestamp(observed_at),
        },
    }


@dataclass(frozen=True)
class Scenario:
    root: Path
    sandbox: SQLiteDirectTrafficSandbox
    release_history: ReleaseGateHistory
    decision_dir: Path
    policy: dict[str, Any]
    policy_sha256: str
    certification_path: Path
    shadow_history: DirectTrafficShadowHistory
    shadow_gate_dir: Path
    platform: SQLitePreproductionPlatform
    adapter: HTTPSPreproductionDirectTrafficAdapter
    authorization: VerifiedPreproductionExecution


def create_scenario(
    root: Path,
    *,
    platform_initial_traffic: int = 5,
    certification_ttl_seconds: int = 3600,
) -> Scenario:
    policy, policy_sha256 = load_policy(POLICY_PATH)
    release_history = ReleaseGateHistory(root / "release-history.sqlite3", ENVIRONMENT_ID)
    for sequence, age in enumerate((600, 300, 0), start=1):
        append_release_gate(release_history, root, sequence, NOW - timedelta(seconds=age))
    rollout = evaluate_rollout(release_history, policy, policy_sha256, 5, now=NOW)
    decision_dir = write_rollout_report(root / "decisions", rollout)
    create_attestation(
        decision_dir,
        release_history,
        policy,
        policy_sha256,
        ROLLOUT_KEY,
        ROLLOUT_KEY_ID,
        now=NOW + timedelta(seconds=1),
    )
    sandbox = SQLiteDirectTrafficSandbox(
        root / "local" / "traffic.sqlite3",
        ENVIRONMENT_SHA256,
        PROVIDER_INSTANCE,
        create=True,
        initial_traffic_percent=5,
    )
    certification_adapter = SQLitePreproductionCertificationAdapter(
        root / "certification" / "adapter.sqlite3",
        ENVIRONMENT_SHA256,
        PROVIDER_INSTANCE,
        namespace_id="isolated-preproduction-certification",
    )
    _certificate, certification_path, _created = create_certification(
        certification_adapter,
        root / "certificates",
        CERTIFICATION_KEY,
        CERTIFICATION_KEY_ID,
        nonce="preproduction-execution-certification",
        ttl_seconds=certification_ttl_seconds,
        now=NOW + timedelta(seconds=1),
    )
    platform = SQLitePreproductionPlatform(
        root / "remote" / "platform.sqlite3",
        ENVIRONMENT_SHA256,
        PROVIDER_INSTANCE,
        NAMESPACE_ID,
        TOKEN,
        ROLLOUT_KEY,
        ROLLOUT_KEY_ID,
        CERTIFICATION_KEY,
        CERTIFICATION_KEY_ID,
        SHADOW_KEY,
        SHADOW_KEY_ID,
        initial_traffic_percent=platform_initial_traffic,
        base_url=BASE_URL,
        now=lambda: VERIFY_NOW,
    )
    adapter = HTTPSPreproductionDirectTrafficAdapter(
        HTTPSPreproductionConfig(
            base_url=BASE_URL,
            allowed_host=ALLOWED_HOST,
            provider_instance=PROVIDER_INSTANCE,
            environment_sha256=ENVIRONMENT_SHA256,
            namespace_id=NAMESPACE_ID,
            token=TOKEN,
            retry_base_seconds=0.001,
        ),
        transport=platform.transport,
        sleep=lambda _seconds: None,
    )
    shadow_history = DirectTrafficShadowHistory(
        root / "shadow" / "history.sqlite3",
        adapter.observer_instance_sha256,
        adapter.provider_instance_sha256,
        adapter.environment_sha256,
        now=NOW,
    )
    for offset in (0, 20, 40, 60):
        observed_at = NOW + timedelta(seconds=offset)
        shadow_history.record(shadow_report(adapter, sandbox, observed_at))
    shadow_policy = DirectTrafficShadowGatePolicy(
        fast_window_runs=2,
        stable_window_runs=4,
        minimum_stable_window_seconds=60,
        maximum_gap_seconds=25,
        maximum_report_age_seconds=10,
        maximum_retried_observations=0,
    )
    gate = evaluate_direct_traffic_shadow_history(
        shadow_history, shadow_policy, now=NOW + timedelta(seconds=65)
    )
    shadow_gate_dir = root / "shadow-gate"
    write_direct_traffic_shadow_gate_report(shadow_gate_dir, gate)
    create_direct_traffic_shadow_attestation(
        shadow_gate_dir,
        shadow_history,
        SHADOW_KEY,
        SHADOW_KEY_ID,
        now=NOW + timedelta(seconds=65),
    )
    authorization = verify_preproduction_execution(
        adapter,
        sandbox,
        decision_dir,
        release_history,
        policy,
        policy_sha256,
        ROLLOUT_KEY,
        ROLLOUT_KEY_ID,
        certification_path,
        CERTIFICATION_KEY,
        CERTIFICATION_KEY_ID,
        shadow_gate_dir,
        shadow_history,
        SHADOW_KEY,
        SHADOW_KEY_ID,
        now=VERIFY_NOW,
    )
    return Scenario(
        root,
        sandbox,
        release_history,
        decision_dir,
        policy,
        policy_sha256,
        certification_path,
        shadow_history,
        shadow_gate_dir,
        platform,
        adapter,
        authorization,
    )


class AgentDirectTrafficPreproductionTest(unittest.TestCase):
    def test_default_transport_builds_exact_get_and_put_without_redirect_handler(self) -> None:
        captured: list[tuple[urllib.request.Request, float, int]] = []

        class FakeResponse:
            status = 200
            headers = {"Content-Type": "application/json"}

            def __init__(self, url: str) -> None:
                self.url = url

            def __enter__(self) -> FakeResponse:
                return self

            def __exit__(self, *_args: Any) -> None:
                return None

            def read(self, amount: int) -> bytes:
                captured[-1] = (captured[-1][0], captured[-1][1], amount)
                return b"{}"

            def geturl(self) -> str:
                return self.url

        class FakeOpener:
            def open(self, request: urllib.request.Request, *, timeout: float) -> FakeResponse:
                captured.append((request, timeout, 0))
                return FakeResponse(request.full_url)

        state_url = f"{BASE_URL}/v1/namespaces/{NAMESPACE_ID}/direct-traffic/state"
        apply_url = f"{BASE_URL}/v1/namespaces/{NAMESPACE_ID}/direct-traffic/changes/{'a' * 64}"
        with patch(
            "ai_companion_worker.evaluation.direct_traffic_preproduction.urllib.request.build_opener",
            side_effect=lambda *_handlers: FakeOpener(),
        ) as build_opener:
            _default_transport("GET", state_url, TOKEN, 2.0, 4096, None)
            _default_transport("PUT", apply_url, TOKEN, 3.0, 8192, b"{}")

        self.assertEqual([item[0].get_method() for item in captured], ["GET", "PUT"])
        self.assertIsNone(captured[0][0].data)
        self.assertEqual(captured[1][0].data, b"{}")
        self.assertEqual(captured[0][0].get_header("Authorization"), f"Bearer {TOKEN}")
        self.assertEqual(captured[1][0].get_header("Content-type"), "application/json")
        self.assertEqual([item[1] for item in captured], [2.0, 3.0])
        self.assertEqual([item[2] for item in captured], [4097, 8193])
        self.assertEqual(build_opener.call_count, 2)
        self.assertTrue(all(len(call.args) == 1 for call in build_opener.call_args_list))

    def test_namespace_and_endpoint_are_fail_closed(self) -> None:
        base = HTTPSPreproductionConfig(
            BASE_URL,
            ALLOWED_HOST,
            PROVIDER_INSTANCE,
            ENVIRONMENT_SHA256,
            NAMESPACE_ID,
            TOKEN,
        )
        for namespace in ("production", "prod-blue", "staging", "preproduction-"):
            with self.subTest(namespace=namespace):
                with self.assertRaises(DirectTrafficPreproductionConfigError):
                    HTTPSPreproductionDirectTrafficAdapter(replace(base, namespace_id=namespace))
        for url in (
            "http://traffic.provider.example/api",
            "https://traffic.provider.example:444/api",
            "https://user@traffic.provider.example/api",
            "https://127.0.0.1/api",
        ):
            with self.subTest(url=url):
                with self.assertRaises(DirectTrafficPreproductionConfigError):
                    HTTPSPreproductionDirectTrafficAdapter(replace(base, base_url=url))
        adapter = HTTPSPreproductionDirectTrafficAdapter(base)
        self.assertNotIn(TOKEN, repr(adapter.config))
        self.assertEqual(adapter.name, ADAPTER_NAME)

    def test_three_proofs_apply_once_and_replay_without_second_put(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            scenario = create_scenario(Path(directory))
            first = apply_verified_preproduction_execution(
                scenario.adapter,
                scenario.sandbox,
                scenario.release_history.path,
                scenario.authorization,
            )
            second = apply_verified_preproduction_execution(
                scenario.adapter,
                scenario.sandbox,
                scenario.release_history.path,
                scenario.authorization,
            )

            self.assertEqual(first["outcome"], "applied")
            self.assertFalse(first["remote_reused"])
            self.assertTrue(second["remote_reused"])
            self.assertTrue(second["local_reused"])
            self.assertEqual(scenario.platform.write_calls, 1)
            self.assertEqual(scenario.platform.status()["operations"], 1)
            self.assertEqual(scenario.sandbox.status()["operations"], 1)
            self.assertEqual(
                scenario.platform.status()["history_chain_sha256"],
                scenario.sandbox.status()["history_chain_sha256"],
            )
            self.assertNotIn(TOKEN, json.dumps(second))
            self.assertNotIn(BASE_URL, json.dumps(second))

    def test_timeout_after_commit_reconciles_without_retrying_put(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            scenario = create_scenario(Path(directory))
            scenario.platform.timeout_after_next_commit = True

            receipt = apply_verified_preproduction_execution(
                scenario.adapter,
                scenario.sandbox,
                scenario.release_history.path,
                scenario.authorization,
            )

            self.assertEqual(receipt["outcome"], "applied")
            self.assertEqual(scenario.platform.write_calls, 1)
            self.assertEqual(scenario.platform.status()["operations"], 1)

    def test_unknown_write_outcome_stops_without_retrying_put(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            scenario = create_scenario(Path(directory))
            puts = 0

            def timeout_before_commit(
                method: Literal["GET", "PUT"],
                url: str,
                token: str,
                timeout_seconds: float,
                max_response_bytes: int,
                body: bytes | None,
            ) -> TransportResponse:
                nonlocal puts
                if method == "PUT":
                    puts += 1
                    raise PreproductionProviderError("provider_transport_error", indeterminate=True)
                return scenario.platform.transport(
                    method,
                    url,
                    token,
                    timeout_seconds,
                    max_response_bytes,
                    body,
                )

            adapter = HTTPSPreproductionDirectTrafficAdapter(
                scenario.adapter.config,
                transport=timeout_before_commit,
                sleep=lambda _seconds: None,
            )
            with self.assertRaisesRegex(
                DirectTrafficPreproductionIndeterminateError, "PUT was not retried"
            ):
                adapter.execute(scenario.authorization)
            self.assertEqual(puts, 1)
            self.assertEqual(scenario.platform.write_calls, 0)
            self.assertEqual(scenario.platform.status()["operations"], 0)

    def test_stale_state_and_corrupt_readback_fail_closed(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            stale = create_scenario(Path(directory) / "stale", platform_initial_traffic=10)
            with self.assertRaises(DirectTrafficPreproductionConflictError):
                stale.adapter.execute(stale.authorization)
            self.assertEqual(stale.platform.write_calls, 0)

            scenario = create_scenario(Path(directory) / "readback")

            def corrupt_after_put(
                method: Literal["GET", "PUT"],
                url: str,
                token: str,
                timeout_seconds: float,
                max_response_bytes: int,
                body: bytes | None,
            ) -> TransportResponse:
                response = scenario.platform.transport(
                    method,
                    url,
                    token,
                    timeout_seconds,
                    max_response_bytes,
                    body,
                )
                if method == "PUT":
                    scenario.platform.corrupt_next_state_read = True
                return response

            adapter = HTTPSPreproductionDirectTrafficAdapter(
                scenario.adapter.config,
                transport=corrupt_after_put,
                sleep=lambda _seconds: None,
            )
            with self.assertRaises(DirectTrafficPreproductionIndeterminateError):
                adapter.execute(scenario.authorization)
            self.assertEqual(scenario.platform.write_calls, 1)
            self.assertEqual(scenario.platform.status()["operations"], 1)
            self.assertEqual(scenario.sandbox.status()["operations"], 0)

    def test_tampered_or_invalidated_proofs_never_reach_provider(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            scenario = create_scenario(Path(directory))
            payload = dict(scenario.authorization.payload)
            payload["namespace_sha256"] = "f" * 64
            tampered = replace(scenario.authorization, payload=payload)
            with self.assertRaises(DirectTrafficPreproductionConfigError):
                scenario.adapter.execute(tampered)
            self.assertEqual(scenario.platform.write_calls, 0)

            scenario.shadow_history.record(
                shadow_report(
                    scenario.adapter,
                    scenario.sandbox,
                    NOW + timedelta(seconds=61),
                )
            )
            with self.assertRaises(DirectTrafficShadowGateVerificationError):
                verify_preproduction_execution(
                    scenario.adapter,
                    scenario.sandbox,
                    scenario.decision_dir,
                    scenario.release_history,
                    scenario.policy,
                    scenario.policy_sha256,
                    ROLLOUT_KEY,
                    ROLLOUT_KEY_ID,
                    scenario.certification_path,
                    CERTIFICATION_KEY,
                    CERTIFICATION_KEY_ID,
                    scenario.shadow_gate_dir,
                    scenario.shadow_history,
                    SHADOW_KEY,
                    SHADOW_KEY_ID,
                    now=VERIFY_NOW,
                )
            self.assertEqual(scenario.platform.write_calls, 0)

    def test_provider_independently_rejects_embedded_signature_tampering(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            scenario = create_scenario(Path(directory))
            payload = json.loads(
                _canonical_json(dict(scenario.authorization.payload)).decode("utf-8")
            )
            payload["shadow_attestation"]["signature"] = "f" * 64
            payload["shadow_attestation_sha256"] = hashlib.sha256(
                _canonical_json(payload["shadow_attestation"])
            ).hexdigest()
            payload["request_sha256"] = hashlib.sha256(
                _canonical_json(
                    {key: value for key, value in payload.items() if key != "request_sha256"}
                )
            ).hexdigest()
            request = scenario.authorization.request
            apply_url = (
                f"{BASE_URL}/v1/namespaces/{NAMESPACE_ID}/direct-traffic/changes/"
                f"{request.idempotency_key}"
            )
            with self.assertRaisesRegex(PreproductionProviderError, "provider_proof_signature"):
                scenario.platform.transport(
                    "PUT",
                    apply_url,
                    TOKEN,
                    5.0,
                    32 * 1024,
                    _canonical_json(payload),
                )
            self.assertEqual(scenario.platform.status()["operations"], 0)
            self.assertEqual(scenario.platform.write_calls, 1)

    def test_expired_certification_blocks_authorization(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            with self.assertRaises(DirectTrafficCertificationError):
                create_scenario(Path(directory), certification_ttl_seconds=30)

    def test_concurrent_replays_converge_to_one_remote_and_local_operation(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            scenario = create_scenario(Path(directory))

            def apply_once(_index: int) -> dict[str, Any]:
                return apply_verified_preproduction_execution(
                    scenario.adapter,
                    scenario.sandbox,
                    scenario.release_history.path,
                    scenario.authorization,
                )

            with ThreadPoolExecutor(max_workers=8) as executor:
                receipts = list(executor.map(apply_once, range(8)))

            self.assertEqual(len(receipts), 8)
            self.assertEqual(scenario.platform.status()["operations"], 1)
            self.assertEqual(scenario.sandbox.status()["operations"], 1)
            self.assertEqual(
                {item["history_chain_sha256"] for item in receipts},
                {scenario.platform.status()["history_chain_sha256"]},
            )

    def test_platform_detects_privileged_operation_chain_tampering(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            scenario = create_scenario(Path(directory))
            apply_verified_preproduction_execution(
                scenario.adapter,
                scenario.sandbox,
                scenario.release_history.path,
                scenario.authorization,
            )
            with sqlite3.connect(scenario.platform.path) as connection:
                connection.execute("DROP TRIGGER preproduction_operations_no_update")
                connection.execute(
                    "UPDATE preproduction_operations SET operation_json = ?",
                    (_canonical_json({"tampered": True}).decode("utf-8"),),
                )
                connection.execute(
                    "CREATE TRIGGER preproduction_operations_no_update "
                    "BEFORE UPDATE ON preproduction_operations BEGIN "
                    "SELECT RAISE(ABORT, 'preproduction history is append-only'); END"
                )
                connection.commit()
            with self.assertRaises(DirectTrafficPreproductionConfigError):
                scenario.platform.status()


if __name__ == "__main__":
    unittest.main()
