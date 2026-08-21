from __future__ import annotations

import hashlib
import json
import os
import tempfile
import unittest
from dataclasses import dataclass
from datetime import UTC, datetime, timedelta
from pathlib import Path
from typing import Any
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
    create_certification,
)
from ai_companion_worker.evaluation.direct_traffic_preproduction import (
    TOKEN_ENV as PREPRODUCTION_TOKEN_ENV,
    DirectTrafficPreproductionConfigError,
    HTTPSPreproductionConfig,
    PreproductionProviderError,
)
from ai_companion_worker.evaluation.direct_traffic_preproduction_drill import (
    run_live_read_only_probe,
    run_local_contract_drill,
    write_attested_drill_bundle,
)
from ai_companion_worker.evaluation.direct_traffic_preproduction_sandbox import (
    SQLitePreproductionPlatform,
)
from ai_companion_worker.evaluation.direct_traffic_production import (
    HTTPSProductionDirectTrafficAdapter,
    VerifiedProductionExecution,
    apply_verified_production_execution,
    production_namespace_sha256,
    verify_production_execution,
)
from ai_companion_worker.evaluation.direct_traffic_production_emergency import (
    HTTPSProductionEmergencyDirectTrafficAdapter,
    apply_verified_production_emergency_execution,
    verify_production_emergency_execution,
)
from ai_companion_worker.evaluation.direct_traffic_production_emergency_gate import (
    attest_emergency_gate,
    evaluate_emergency_gate,
    write_emergency_gate,
)
from ai_companion_worker.evaluation.direct_traffic_production_emergency_sandbox import (
    SQLiteProductionEmergencyCertificationAdapter,
    SQLiteProductionEmergencyPlatform,
)
from ai_companion_worker.evaluation.direct_traffic_production_expansion import (
    HTTPSProductionExpansionDirectTrafficAdapter,
    apply_verified_production_expansion_execution,
    verify_production_expansion_execution,
)
from ai_companion_worker.evaluation.direct_traffic_production_expansion_gate import (
    DirectTrafficProductionExpansionGateVerificationError,
    attest_expansion_gate,
    evaluate_expansion_gate,
    write_expansion_gate,
)
from ai_companion_worker.evaluation.direct_traffic_production_expansion_sandbox import (
    SQLiteProductionExpansionCertificationAdapter,
    SQLiteProductionExpansionPlatform,
)
from ai_companion_worker.evaluation.direct_traffic_production_expansion_25 import (
    HTTPSProductionExpansion25DirectTrafficAdapter,
    apply_verified_production_expansion_25_execution,
    verify_production_expansion_25_execution,
)
from ai_companion_worker.evaluation.direct_traffic_production_expansion_25_gate import (
    DirectTrafficProductionExpansion25GateVerificationError,
    attest_expansion_25_gate,
    evaluate_expansion_25_gate,
    write_expansion_25_gate,
)
from ai_companion_worker.evaluation.direct_traffic_production_expansion_25_sandbox import (
    SQLiteProductionExpansion25CertificationAdapter,
    SQLiteProductionExpansion25Platform,
)
from ai_companion_worker.evaluation.direct_traffic_production_expansion_50 import (
    HTTPSProductionExpansion50DirectTrafficAdapter,
    apply_verified_production_expansion_50_execution,
    verify_production_expansion_50_execution,
)
from ai_companion_worker.evaluation.direct_traffic_production_expansion_50_gate import (
    DirectTrafficProductionExpansion50GateConfigError,
    attest_expansion_50_gate,
    evaluate_expansion_50_gate,
    write_expansion_50_gate,
)
from ai_companion_worker.evaluation.direct_traffic_production_expansion_50_sandbox import (
    SQLiteProductionExpansion50CertificationAdapter,
    SQLiteProductionExpansion50Platform,
)
from ai_companion_worker.evaluation.direct_traffic_production_expansion_100 import (
    HTTPSProductionExpansion100DirectTrafficAdapter,
    apply_verified_production_expansion_100_execution,
    verify_production_expansion_100_execution,
)
from ai_companion_worker.evaluation.direct_traffic_production_expansion_100_gate import (
    DirectTrafficProductionExpansion100GateConfigError,
    attest_expansion_100_gate,
    evaluate_expansion_100_gate,
    write_expansion_100_gate,
)
from ai_companion_worker.evaluation.direct_traffic_production_expansion_100_sandbox import (
    SQLiteProductionExpansion100CertificationAdapter,
    SQLiteProductionExpansion100Platform,
)
from ai_companion_worker.evaluation.direct_traffic_production_recovery import (
    HTTPSProductionRecoveryDirectTrafficAdapter,
    apply_verified_production_recovery_execution,
    verify_production_recovery_execution,
)
from ai_companion_worker.evaluation.direct_traffic_production_recovery_gate import (
    DirectTrafficProductionRecoveryGateVerificationError,
    attest_recovery_gate,
    evaluate_recovery_gate,
    write_recovery_gate,
)
from ai_companion_worker.evaluation.direct_traffic_production_recovery_sandbox import (
    SQLiteProductionRecoveryCertificationAdapter,
    SQLiteProductionRecoveryPlatform,
)
from ai_companion_worker.evaluation.direct_traffic_production_gate import (
    DirectTrafficProductionGateError,
    DirectTrafficProductionGateVerificationError,
    attest_production_gate,
    evaluate_production_gate,
    verify_embedded_production_gate_attestation,
    verify_production_gate_attestation,
    write_production_gate,
)
from ai_companion_worker.evaluation.direct_traffic_production_sandbox import (
    SQLiteProductionCertificationAdapter,
    SQLiteProductionPlatform,
)
from ai_companion_worker.evaluation.direct_traffic_shadow import (
    DirectTrafficShadowHistory,
)
from ai_companion_worker.evaluation.direct_traffic_shadow_gate import (
    DirectTrafficShadowGatePolicy,
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
NOW = datetime(2026, 8, 13, 12, 0, tzinfo=UTC)
VERIFY_NOW = NOW + timedelta(seconds=72)
ENVIRONMENT_ID = "production-cn"
ENVIRONMENT_SHA256 = environment_sha256(ENVIRONMENT_ID)
PROVIDER_INSTANCE = "platform-production-blue"
NAMESPACE_ID = "production-agent-direct-blue"
BASE_URL = "https://traffic.provider.example/api"
ALLOWED_HOST = "traffic.provider.example"
TOKEN = "production-provider-token-at-least-32-bytes"
ROLLOUT_KEY = b"production-rollout-key-at-least-32-bytes"
ROLLOUT_KEY_ID = "production-rollout-v1"
CERTIFICATION_KEY = b"production-certification-key-at-least-32-bytes"
CERTIFICATION_KEY_ID = "production-certification-v1"
SHADOW_KEY = b"production-shadow-gate-key-at-least-32-bytes"
SHADOW_KEY_ID = "production-shadow-gate-v1"
PRODUCTION_GATE_KEY = b"production-fourth-gate-key-at-least-32-bytes"
PRODUCTION_GATE_KEY_ID = "production-fourth-gate-v1"
EMERGENCY_TOKEN = "production-emergency-token-at-least-32-bytes"
EMERGENCY_ROLLOUT_KEY = b"production-emergency-rollout-key-at-least-32-bytes"
EMERGENCY_ROLLOUT_KEY_ID = "production-emergency-rollout-v1"
EMERGENCY_CERTIFICATION_KEY = (
    b"production-emergency-certification-key-at-least-32-bytes"
)
EMERGENCY_CERTIFICATION_KEY_ID = "production-emergency-certification-v1"
EMERGENCY_GATE_KEY = b"production-emergency-gate-key-at-least-32-bytes"
EMERGENCY_GATE_KEY_ID = "production-emergency-gate-v1"
RECOVERY_TOKEN = "production-recovery-token-at-least-32-bytes"
RECOVERY_ROLLOUT_KEY = b"production-recovery-rollout-key-at-least-32-bytes"
RECOVERY_ROLLOUT_KEY_ID = "production-recovery-rollout-v1"
RECOVERY_CERTIFICATION_KEY = (
    b"production-recovery-certification-key-at-least-32-bytes"
)
RECOVERY_CERTIFICATION_KEY_ID = "production-recovery-certification-v1"
RECOVERY_SHADOW_KEY = b"production-recovery-shadow-key-at-least-32-bytes"
RECOVERY_SHADOW_KEY_ID = "production-recovery-shadow-v1"
RECOVERY_GATE_KEY = b"production-recovery-gate-key-at-least-32-bytes"
RECOVERY_GATE_KEY_ID = "production-recovery-gate-v1"
EXPANSION_TOKEN = "production-expansion-token-at-least-32-bytes"
EXPANSION_ROLLOUT_KEY = b"production-expansion-rollout-key-at-least-32-bytes"
EXPANSION_ROLLOUT_KEY_ID = "production-expansion-rollout-v1"
EXPANSION_CERTIFICATION_KEY = (
    b"production-expansion-certification-key-at-least-32-bytes"
)
EXPANSION_CERTIFICATION_KEY_ID = "production-expansion-certification-v1"
EXPANSION_SHADOW_KEY = b"production-expansion-shadow-key-at-least-32-bytes"
EXPANSION_SHADOW_KEY_ID = "production-expansion-shadow-v1"
EXPANSION_GATE_KEY = b"production-expansion-gate-key-at-least-32-bytes"
EXPANSION_GATE_KEY_ID = "production-expansion-gate-v1"
EXPANSION_25_TOKEN = "production-expansion-25-token-at-least-32-bytes"
EXPANSION_25_ROLLOUT_KEY = b"production-expansion-25-rollout-key-at-least-32-bytes"
EXPANSION_25_ROLLOUT_KEY_ID = "production-expansion-25-rollout-v1"
EXPANSION_25_CERTIFICATION_KEY = (
    b"production-expansion-25-certification-key-at-least-32-bytes"
)
EXPANSION_25_CERTIFICATION_KEY_ID = "production-expansion-25-certification-v1"
EXPANSION_25_SHADOW_KEY = b"production-expansion-25-shadow-key-at-least-32-bytes"
EXPANSION_25_SHADOW_KEY_ID = "production-expansion-25-shadow-v1"
EXPANSION_25_GATE_KEY = b"production-expansion-25-gate-key-at-least-32-bytes"
EXPANSION_25_GATE_KEY_ID = "production-expansion-25-gate-v1"
EXPANSION_50_TOKEN = "production-expansion-50-token-at-least-32-bytes"
EXPANSION_50_ROLLOUT_KEY = b"production-expansion-50-rollout-key-at-least-32-bytes"
EXPANSION_50_ROLLOUT_KEY_ID = "production-expansion-50-rollout-v1"
EXPANSION_50_CERTIFICATION_KEY = (
    b"production-expansion-50-certification-key-at-least-32-bytes"
)
EXPANSION_50_CERTIFICATION_KEY_ID = "production-expansion-50-certification-v1"
EXPANSION_50_SHADOW_KEY = b"production-expansion-50-shadow-key-at-least-32-bytes"
EXPANSION_50_SHADOW_KEY_ID = "production-expansion-50-shadow-v1"
EXPANSION_50_GATE_KEY = b"production-expansion-50-gate-key-at-least-32-bytes"
EXPANSION_50_GATE_KEY_ID = "production-expansion-50-gate-v1"
EXPANSION_100_TOKEN = "production-expansion-100-token-at-least-32-bytes"
EXPANSION_100_ROLLOUT_KEY = b"production-expansion-100-rollout-key-at-least-32-bytes"
EXPANSION_100_ROLLOUT_KEY_ID = "production-expansion-100-rollout-v1"
EXPANSION_100_CERTIFICATION_KEY = (
    b"production-expansion-100-certification-key-at-least-32-bytes"
)
EXPANSION_100_CERTIFICATION_KEY_ID = "production-expansion-100-certification-v1"
EXPANSION_100_SHADOW_KEY = b"production-expansion-100-shadow-key-at-least-32-bytes"
EXPANSION_100_SHADOW_KEY_ID = "production-expansion-100-shadow-v1"
EXPANSION_100_GATE_KEY = b"production-expansion-100-gate-key-at-least-32-bytes"
EXPANSION_100_GATE_KEY_ID = "production-expansion-100-gate-v1"
DRILL_KEY = b"preproduction-drill-proof-key-at-least-32-bytes"
DRILL_KEY_ID = "preproduction-drill-proof-v1"


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
    *,
    summary: dict[str, int | float | bool] | None = None,
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
                "summary": summary or healthy_summary(),
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
    adapter: HTTPSProductionDirectTrafficAdapter,
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
        "provider": {**public, "observed_at": _format_timestamp(observed_at)},
    }


@dataclass(frozen=True)
class ProductionScenario:
    root: Path
    sandbox: SQLiteDirectTrafficSandbox
    release_history: ReleaseGateHistory
    decision_dir: Path
    policy: dict[str, Any]
    policy_sha256: str
    certification: dict[str, Any]
    certification_path: Path
    shadow_attestation: dict[str, Any]
    shadow_history: DirectTrafficShadowHistory
    shadow_gate_dir: Path
    rollout_attestation: dict[str, Any]
    production_gate_dir: Path
    platform: SQLiteProductionPlatform
    adapter: HTTPSProductionDirectTrafficAdapter
    authorization: VerifiedProductionExecution


def create_preproduction_drill_bundles(root: Path) -> tuple[Path, Path]:
    root.mkdir(mode=0o700)
    evidence_time = NOW + timedelta(seconds=65)
    local_report = run_local_contract_drill(
        POLICY_PATH, now=NOW - timedelta(seconds=853)
    )
    local_bundle = write_attested_drill_bundle(
        root / "local-evidence",
        local_report,
        DRILL_KEY,
        DRILL_KEY_ID,
        now=evidence_time,
    )
    environment_digest = environment_sha256("preproduction-production-probe")
    platform = SQLitePreproductionPlatform(
        root / "live-probe-platform.sqlite3",
        environment_digest,
        "platform-preproduction-production-probe",
        "preproduction-production-probe",
        "preproduction-live-probe-token-at-least-32-bytes",
        b"live-probe-rollout-key-at-least-32-bytes",
        "live-probe-rollout-v1",
        b"live-probe-certification-key-at-least-32-bytes",
        "live-probe-certification-v1",
        b"live-probe-shadow-key-at-least-32-bytes",
        "live-probe-shadow-v1",
        initial_traffic_percent=5,
        base_url=BASE_URL,
        now=lambda: evidence_time,
    )
    live_report = run_live_read_only_probe(
        HTTPSPreproductionConfig(
            BASE_URL,
            ALLOWED_HOST,
            "platform-preproduction-production-probe",
            environment_digest,
            "preproduction-production-probe",
            "preproduction-live-probe-token-at-least-32-bytes",
        ),
        transport=platform.transport,
        now=evidence_time,
    )
    live_bundle = write_attested_drill_bundle(
        root / "live-evidence",
        live_report,
        DRILL_KEY,
        DRILL_KEY_ID,
        now=evidence_time,
    )
    return local_bundle, live_bundle


def create_scenario(root: Path) -> ProductionScenario:
    policy, policy_sha256 = load_policy(POLICY_PATH)
    release_history = ReleaseGateHistory(root / "release-history.sqlite3", ENVIRONMENT_ID)
    for sequence, age in enumerate((600, 300, 0), start=1):
        append_release_gate(release_history, root, sequence, NOW - timedelta(seconds=age))
    rollout = evaluate_rollout(release_history, policy, policy_sha256, 0, now=NOW)
    decision_dir = write_rollout_report(root / "decisions", rollout)
    rollout_attestation, _created = create_attestation(
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
        initial_traffic_percent=0,
    )
    certification_adapter = SQLiteProductionCertificationAdapter(
        root / "certification" / "adapter.sqlite3",
        ENVIRONMENT_SHA256,
        PROVIDER_INSTANCE,
        namespace_id="isolated-production-certification",
    )
    certification, certification_path, _created = create_certification(
        certification_adapter,
        root / "certificates",
        CERTIFICATION_KEY,
        CERTIFICATION_KEY_ID,
        nonce="production-execution-certification",
        ttl_seconds=3600,
        now=NOW + timedelta(seconds=1),
    )
    platform = SQLiteProductionPlatform(
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
        PRODUCTION_GATE_KEY,
        PRODUCTION_GATE_KEY_ID,
        initial_traffic_percent=0,
        base_url=BASE_URL,
        now=lambda: VERIFY_NOW,
    )
    adapter = HTTPSProductionDirectTrafficAdapter(
        HTTPSPreproductionConfig(
            BASE_URL,
            ALLOWED_HOST,
            PROVIDER_INSTANCE,
            ENVIRONMENT_SHA256,
            NAMESPACE_ID,
            TOKEN,
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
    shadow_gate = evaluate_direct_traffic_shadow_history(
        shadow_history,
        DirectTrafficShadowGatePolicy(2, 4, 60, 25, 10, 0),
        now=NOW + timedelta(seconds=65),
    )
    shadow_gate_dir = root / "shadow-gate"
    write_direct_traffic_shadow_gate_report(shadow_gate_dir, shadow_gate)
    shadow_attestation, _created = create_direct_traffic_shadow_attestation(
        shadow_gate_dir,
        shadow_history,
        SHADOW_KEY,
        SHADOW_KEY_ID,
        now=NOW + timedelta(seconds=65),
    )
    local_bundle, live_bundle = create_preproduction_drill_bundles(root / "drills")
    production_gate = evaluate_production_gate(
        local_bundle,
        live_bundle,
        DRILL_KEY,
        DRILL_KEY_ID,
        certification,
        shadow_attestation,
        rollout_attestation,
        production_namespace_sha256(NAMESPACE_ID),
        certification_key=CERTIFICATION_KEY,
        certification_key_id=CERTIFICATION_KEY_ID,
        shadow_key=SHADOW_KEY,
        shadow_key_id=SHADOW_KEY_ID,
        rollout_key=ROLLOUT_KEY,
        rollout_key_id=ROLLOUT_KEY_ID,
        now=NOW + timedelta(seconds=70),
    )
    production_gate_dir = root / "production-gate"
    write_production_gate(production_gate_dir, production_gate)
    attest_production_gate(
        production_gate_dir,
        PRODUCTION_GATE_KEY,
        PRODUCTION_GATE_KEY_ID,
        now=NOW + timedelta(seconds=71),
    )
    authorization = verify_production_execution(
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
        production_gate_dir,
        PRODUCTION_GATE_KEY,
        PRODUCTION_GATE_KEY_ID,
        now=VERIFY_NOW,
    )
    return ProductionScenario(
        root,
        sandbox,
        release_history,
        decision_dir,
        policy,
        policy_sha256,
        certification,
        certification_path,
        shadow_attestation,
        shadow_history,
        shadow_gate_dir,
        rollout_attestation,
        production_gate_dir,
        platform,
        adapter,
        authorization,
    )


class AgentDirectTrafficProductionTest(unittest.TestCase):
    def test_independent_emergency_path_rolls_five_back_to_zero_without_shadow(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            scenario = create_scenario(Path(directory))
            activation_receipt = apply_verified_production_execution(
                scenario.adapter,
                scenario.sandbox,
                scenario.release_history.path,
                scenario.authorization,
            )
            degraded = healthy_summary()
            degraded["duration_p95_ms"] = 20_000.0
            append_release_gate(
                scenario.release_history,
                scenario.root,
                4,
                NOW + timedelta(seconds=80),
                summary=degraded,
            )
            rollback_report = evaluate_rollout(
                scenario.release_history,
                scenario.policy,
                scenario.policy_sha256,
                5,
                now=NOW + timedelta(seconds=81),
            )
            self.assertEqual(rollback_report["decision"], "rollback")
            rollback_dir = write_rollout_report(
                scenario.root / "emergency-decisions", rollback_report
            )
            rollback_attestation, _created = create_attestation(
                rollback_dir,
                scenario.release_history,
                scenario.policy,
                scenario.policy_sha256,
                EMERGENCY_ROLLOUT_KEY,
                EMERGENCY_ROLLOUT_KEY_ID,
                now=NOW + timedelta(seconds=82),
            )
            certification_adapter = SQLiteProductionEmergencyCertificationAdapter(
                scenario.root / "emergency-certification" / "adapter.sqlite3",
                ENVIRONMENT_SHA256,
                PROVIDER_INSTANCE,
                namespace_id="isolated-production-emergency-certification",
            )
            certification, certification_path, _created = create_certification(
                certification_adapter,
                scenario.root / "emergency-certificates",
                EMERGENCY_CERTIFICATION_KEY,
                EMERGENCY_CERTIFICATION_KEY_ID,
                nonce="production-emergency-certification",
                ttl_seconds=300,
                now=NOW + timedelta(seconds=82),
            )
            gate_report = evaluate_emergency_gate(
                rollback_attestation,
                certification,
                activation_receipt,
                production_namespace_sha256(NAMESPACE_ID),
                "INC-LATENCY-20260813",
                "oncall@example.test",
                rollout_key=EMERGENCY_ROLLOUT_KEY,
                rollout_key_id=EMERGENCY_ROLLOUT_KEY_ID,
                certification_key=EMERGENCY_CERTIFICATION_KEY,
                certification_key_id=EMERGENCY_CERTIFICATION_KEY_ID,
                now=NOW + timedelta(seconds=83),
            )
            gate_dir = scenario.root / "emergency-gate"
            write_emergency_gate(gate_dir, gate_report)
            attest_emergency_gate(
                gate_dir,
                EMERGENCY_GATE_KEY,
                EMERGENCY_GATE_KEY_ID,
                rollout_attestation=rollback_attestation,
                adapter_certification=certification,
                activation_receipt=activation_receipt,
                rollout_key=EMERGENCY_ROLLOUT_KEY,
                rollout_key_id=EMERGENCY_ROLLOUT_KEY_ID,
                certification_key=EMERGENCY_CERTIFICATION_KEY,
                certification_key_id=EMERGENCY_CERTIFICATION_KEY_ID,
                now=NOW + timedelta(seconds=84),
            )
            platform = SQLiteProductionEmergencyPlatform(
                scenario.root / "remote" / "platform.sqlite3",
                ENVIRONMENT_SHA256,
                PROVIDER_INSTANCE,
                NAMESPACE_ID,
                EMERGENCY_TOKEN,
                EMERGENCY_ROLLOUT_KEY,
                EMERGENCY_ROLLOUT_KEY_ID,
                EMERGENCY_CERTIFICATION_KEY,
                EMERGENCY_CERTIFICATION_KEY_ID,
                EMERGENCY_GATE_KEY,
                EMERGENCY_GATE_KEY_ID,
                initial_traffic_percent=0,
                base_url=BASE_URL,
                now=lambda: NOW + timedelta(seconds=85),
            )
            adapter = HTTPSProductionEmergencyDirectTrafficAdapter(
                HTTPSPreproductionConfig(
                    BASE_URL,
                    ALLOWED_HOST,
                    PROVIDER_INSTANCE,
                    ENVIRONMENT_SHA256,
                    NAMESPACE_ID,
                    EMERGENCY_TOKEN,
                    retry_base_seconds=0.001,
                ),
                transport=platform.transport,
                sleep=lambda _seconds: None,
            )
            authorization = verify_production_emergency_execution(
                adapter,
                scenario.sandbox,
                rollback_dir,
                scenario.release_history,
                scenario.policy,
                scenario.policy_sha256,
                EMERGENCY_ROLLOUT_KEY,
                EMERGENCY_ROLLOUT_KEY_ID,
                certification_path,
                EMERGENCY_CERTIFICATION_KEY,
                EMERGENCY_CERTIFICATION_KEY_ID,
                gate_dir,
                EMERGENCY_GATE_KEY,
                EMERGENCY_GATE_KEY_ID,
                activation_receipt,
                now=NOW + timedelta(seconds=85),
            )

            tampered_payload = json.loads(
                _canonical_json(dict(authorization.payload)).decode()
            )
            tampered_payload["emergency_gate_attestation"]["signature"] = "f" * 64
            tampered_payload["emergency_gate_attestation_sha256"] = hashlib.sha256(
                _canonical_json(tampered_payload["emergency_gate_attestation"])
            ).hexdigest()
            tampered_payload["request_sha256"] = hashlib.sha256(
                _canonical_json(
                    {
                        key: item
                        for key, item in tampered_payload.items()
                        if key != "request_sha256"
                    }
                )
            ).hexdigest()
            apply_url = (
                f"{BASE_URL}/v1/namespaces/{NAMESPACE_ID}/direct-traffic/changes/"
                f"{authorization.request.idempotency_key}"
            )
            with self.assertRaisesRegex(
                PreproductionProviderError, "provider_emergency_gate_proof"
            ):
                platform.transport(
                    "PUT",
                    apply_url,
                    EMERGENCY_TOKEN,
                    5.0,
                    32 * 1024,
                    _canonical_json(tampered_payload),
                )
            self.assertEqual(platform.status()["operations"], 1)

            receipt = apply_verified_production_emergency_execution(
                adapter,
                scenario.sandbox,
                scenario.release_history.path,
                authorization,
            )
            replay = apply_verified_production_emergency_execution(
                adapter,
                scenario.sandbox,
                scenario.release_history.path,
                authorization,
            )

            self.assertEqual(receipt["current_traffic_percent"], 0)
            self.assertEqual(receipt["incident_id"], "INC-LATENCY-20260813")
            self.assertTrue(replay["remote_reused"])
            self.assertTrue(replay["local_reused"])
            self.assertEqual(platform.status()["operations"], 2)
            self.assertEqual(scenario.sandbox.status()["operations"], 2)
            self.assertNotIn("shadow_attestation", authorization.payload)

            recovery_base = NOW + timedelta(seconds=700)
            for sequence, offset in enumerate((-600, -300, 0), start=5):
                append_release_gate(
                    scenario.release_history,
                    scenario.root,
                    sequence,
                    recovery_base + timedelta(seconds=offset),
                )
            recovery_report = evaluate_rollout(
                scenario.release_history,
                scenario.policy,
                scenario.policy_sha256,
                0,
                now=recovery_base,
            )
            self.assertEqual(recovery_report["decision"], "expand")
            recovery_decision_dir = write_rollout_report(
                scenario.root / "recovery-decisions", recovery_report
            )
            recovery_rollout, _created = create_attestation(
                recovery_decision_dir,
                scenario.release_history,
                scenario.policy,
                scenario.policy_sha256,
                RECOVERY_ROLLOUT_KEY,
                RECOVERY_ROLLOUT_KEY_ID,
                now=recovery_base + timedelta(seconds=1),
            )
            recovery_cert_adapter = SQLiteProductionRecoveryCertificationAdapter(
                scenario.root / "recovery-certification" / "adapter.sqlite3",
                ENVIRONMENT_SHA256,
                PROVIDER_INSTANCE,
                namespace_id="isolated-production-recovery-certification",
            )
            recovery_cert, recovery_cert_path, _created = create_certification(
                recovery_cert_adapter,
                scenario.root / "recovery-certificates",
                RECOVERY_CERTIFICATION_KEY,
                RECOVERY_CERTIFICATION_KEY_ID,
                nonce="production-recovery-certification",
                ttl_seconds=900,
                now=recovery_base,
            )
            recovery_adapter_without_transport = (
                HTTPSProductionRecoveryDirectTrafficAdapter(
                    HTTPSPreproductionConfig(
                        BASE_URL,
                        ALLOWED_HOST,
                        PROVIDER_INSTANCE,
                        ENVIRONMENT_SHA256,
                        NAMESPACE_ID,
                        RECOVERY_TOKEN,
                    )
                )
            )
            recovery_shadow_history = DirectTrafficShadowHistory(
                scenario.root / "recovery-shadow" / "history.sqlite3",
                recovery_adapter_without_transport.observer_instance_sha256,
                recovery_adapter_without_transport.provider_instance_sha256,
                recovery_adapter_without_transport.environment_sha256,
                now=recovery_base - timedelta(seconds=60),
            )
            for offset in (-60, -30, 0):
                observed_at = recovery_base + timedelta(seconds=offset)
                recovery_shadow_history.record(
                    shadow_report(
                        recovery_adapter_without_transport,
                        scenario.sandbox,
                        observed_at,
                    )
                )
            recovery_shadow_gate = evaluate_direct_traffic_shadow_history(
                recovery_shadow_history,
                DirectTrafficShadowGatePolicy(2, 3, 60, 30, 10, 0),
                now=recovery_base + timedelta(seconds=2),
            )
            recovery_shadow_dir = scenario.root / "recovery-shadow-gate"
            write_direct_traffic_shadow_gate_report(
                recovery_shadow_dir, recovery_shadow_gate
            )
            recovery_shadow, _created = create_direct_traffic_shadow_attestation(
                recovery_shadow_dir,
                recovery_shadow_history,
                RECOVERY_SHADOW_KEY,
                RECOVERY_SHADOW_KEY_ID,
                now=recovery_base + timedelta(seconds=2),
            )
            with self.assertRaisesRegex(
                DirectTrafficProductionRecoveryGateVerificationError,
                "signature",
            ):
                evaluate_recovery_gate(
                    recovery_rollout,
                    recovery_report,
                    recovery_cert,
                    scenario.shadow_attestation,
                    receipt,
                    production_namespace_sha256(NAMESPACE_ID),
                    rollout_key=RECOVERY_ROLLOUT_KEY,
                    rollout_key_id=RECOVERY_ROLLOUT_KEY_ID,
                    certification_key=RECOVERY_CERTIFICATION_KEY,
                    certification_key_id=RECOVERY_CERTIFICATION_KEY_ID,
                    shadow_key=RECOVERY_SHADOW_KEY,
                    shadow_key_id=RECOVERY_SHADOW_KEY_ID,
                    minimum_cooldown_seconds=600,
                    now=recovery_base + timedelta(seconds=3),
                )
            recovery_gate_report = evaluate_recovery_gate(
                recovery_rollout,
                recovery_report,
                recovery_cert,
                recovery_shadow,
                receipt,
                production_namespace_sha256(NAMESPACE_ID),
                rollout_key=RECOVERY_ROLLOUT_KEY,
                rollout_key_id=RECOVERY_ROLLOUT_KEY_ID,
                certification_key=RECOVERY_CERTIFICATION_KEY,
                certification_key_id=RECOVERY_CERTIFICATION_KEY_ID,
                shadow_key=RECOVERY_SHADOW_KEY,
                shadow_key_id=RECOVERY_SHADOW_KEY_ID,
                minimum_cooldown_seconds=600,
                now=recovery_base + timedelta(seconds=3),
            )
            recovery_gate_dir = scenario.root / "recovery-gate"
            write_recovery_gate(recovery_gate_dir, recovery_gate_report)
            attest_recovery_gate(
                recovery_gate_dir,
                RECOVERY_GATE_KEY,
                RECOVERY_GATE_KEY_ID,
                rollout_attestation=recovery_rollout,
                rollout_report=recovery_report,
                adapter_certification=recovery_cert,
                shadow_attestation=recovery_shadow,
                rollback_receipt=receipt,
                rollout_key=RECOVERY_ROLLOUT_KEY,
                rollout_key_id=RECOVERY_ROLLOUT_KEY_ID,
                certification_key=RECOVERY_CERTIFICATION_KEY,
                certification_key_id=RECOVERY_CERTIFICATION_KEY_ID,
                shadow_key=RECOVERY_SHADOW_KEY,
                shadow_key_id=RECOVERY_SHADOW_KEY_ID,
                now=recovery_base + timedelta(seconds=4),
            )
            recovery_platform = SQLiteProductionRecoveryPlatform(
                scenario.root / "remote" / "platform.sqlite3",
                ENVIRONMENT_SHA256,
                PROVIDER_INSTANCE,
                NAMESPACE_ID,
                RECOVERY_TOKEN,
                RECOVERY_ROLLOUT_KEY,
                RECOVERY_ROLLOUT_KEY_ID,
                RECOVERY_CERTIFICATION_KEY,
                RECOVERY_CERTIFICATION_KEY_ID,
                RECOVERY_SHADOW_KEY,
                RECOVERY_SHADOW_KEY_ID,
                RECOVERY_GATE_KEY,
                RECOVERY_GATE_KEY_ID,
                initial_traffic_percent=0,
                base_url=BASE_URL,
                now=lambda: recovery_base + timedelta(seconds=5),
            )
            recovery_adapter = HTTPSProductionRecoveryDirectTrafficAdapter(
                HTTPSPreproductionConfig(
                    BASE_URL,
                    ALLOWED_HOST,
                    PROVIDER_INSTANCE,
                    ENVIRONMENT_SHA256,
                    NAMESPACE_ID,
                    RECOVERY_TOKEN,
                    retry_base_seconds=0.001,
                ),
                transport=recovery_platform.transport,
                sleep=lambda _seconds: None,
            )
            recovery_authorization = verify_production_recovery_execution(
                recovery_adapter,
                scenario.sandbox,
                recovery_decision_dir,
                scenario.release_history,
                scenario.policy,
                scenario.policy_sha256,
                RECOVERY_ROLLOUT_KEY,
                RECOVERY_ROLLOUT_KEY_ID,
                recovery_cert_path,
                RECOVERY_CERTIFICATION_KEY,
                RECOVERY_CERTIFICATION_KEY_ID,
                recovery_shadow_dir,
                recovery_shadow_history,
                RECOVERY_SHADOW_KEY,
                RECOVERY_SHADOW_KEY_ID,
                recovery_gate_dir,
                RECOVERY_GATE_KEY,
                RECOVERY_GATE_KEY_ID,
                receipt,
                recovery_report,
                now=recovery_base + timedelta(seconds=5),
            )
            recovery_receipt = apply_verified_production_recovery_execution(
                recovery_adapter,
                scenario.sandbox,
                scenario.release_history.path,
                recovery_authorization,
            )
            self.assertEqual(recovery_receipt["current_traffic_percent"], 5)
            self.assertEqual(recovery_receipt["revision"], 3)
            self.assertEqual(scenario.sandbox.status()["operations"], 3)
            self.assertEqual(recovery_platform.status()["operations"], 3)

            pre_recovery_window_report = evaluate_rollout(
                scenario.release_history,
                scenario.policy,
                scenario.policy_sha256,
                5,
                now=recovery_base + timedelta(seconds=6),
            )
            self.assertEqual(pre_recovery_window_report["decision"], "expand")
            pre_recovery_window_dir = write_rollout_report(
                scenario.root / "pre-recovery-window-decisions",
                pre_recovery_window_report,
            )
            pre_recovery_window_rollout, _created = create_attestation(
                pre_recovery_window_dir,
                scenario.release_history,
                scenario.policy,
                scenario.policy_sha256,
                EXPANSION_ROLLOUT_KEY,
                EXPANSION_ROLLOUT_KEY_ID,
                now=recovery_base + timedelta(seconds=6),
            )

            expansion_base = recovery_base + timedelta(seconds=700)
            for sequence, offset in enumerate((-600, -300, 0), start=8):
                append_release_gate(
                    scenario.release_history,
                    scenario.root,
                    sequence,
                    expansion_base + timedelta(seconds=offset),
                )
            expansion_report = evaluate_rollout(
                scenario.release_history,
                scenario.policy,
                scenario.policy_sha256,
                5,
                now=expansion_base,
            )
            self.assertEqual(expansion_report["decision"], "expand")
            self.assertEqual(expansion_report["target_traffic_percent"], 10)
            expansion_decision_dir = write_rollout_report(
                scenario.root / "expansion-decisions", expansion_report
            )
            expansion_rollout, _created = create_attestation(
                expansion_decision_dir,
                scenario.release_history,
                scenario.policy,
                scenario.policy_sha256,
                EXPANSION_ROLLOUT_KEY,
                EXPANSION_ROLLOUT_KEY_ID,
                now=expansion_base + timedelta(seconds=1),
            )
            expansion_cert_adapter = SQLiteProductionExpansionCertificationAdapter(
                scenario.root / "expansion-certification" / "adapter.sqlite3",
                ENVIRONMENT_SHA256,
                PROVIDER_INSTANCE,
                namespace_id="isolated-production-expansion-certification",
            )
            expansion_cert, expansion_cert_path, _created = create_certification(
                expansion_cert_adapter,
                scenario.root / "expansion-certificates",
                EXPANSION_CERTIFICATION_KEY,
                EXPANSION_CERTIFICATION_KEY_ID,
                nonce="production-expansion-certification",
                ttl_seconds=900,
                now=expansion_base,
            )
            expansion_adapter_without_transport = (
                HTTPSProductionExpansionDirectTrafficAdapter(
                    HTTPSPreproductionConfig(
                        BASE_URL,
                        ALLOWED_HOST,
                        PROVIDER_INSTANCE,
                        ENVIRONMENT_SHA256,
                        NAMESPACE_ID,
                        EXPANSION_TOKEN,
                    )
                )
            )
            old_expansion_shadow_history = DirectTrafficShadowHistory(
                scenario.root / "old-expansion-shadow" / "history.sqlite3",
                expansion_adapter_without_transport.observer_instance_sha256,
                expansion_adapter_without_transport.provider_instance_sha256,
                expansion_adapter_without_transport.environment_sha256,
                now=recovery_base - timedelta(seconds=60),
            )
            for offset in (-60, -30, 0):
                observed_at = recovery_base + timedelta(seconds=offset)
                old_expansion_shadow_history.record(
                    shadow_report(
                        expansion_adapter_without_transport,
                        scenario.sandbox,
                        observed_at,
                    )
                )
            old_expansion_shadow_gate = evaluate_direct_traffic_shadow_history(
                old_expansion_shadow_history,
                DirectTrafficShadowGatePolicy(2, 3, 60, 30, 10, 0),
                now=recovery_base + timedelta(seconds=6),
            )
            old_expansion_shadow_dir = scenario.root / "old-expansion-shadow-gate"
            write_direct_traffic_shadow_gate_report(
                old_expansion_shadow_dir, old_expansion_shadow_gate
            )
            old_expansion_shadow, _created = create_direct_traffic_shadow_attestation(
                old_expansion_shadow_dir,
                old_expansion_shadow_history,
                EXPANSION_SHADOW_KEY,
                EXPANSION_SHADOW_KEY_ID,
                now=recovery_base + timedelta(seconds=6),
            )
            expansion_shadow_history = DirectTrafficShadowHistory(
                scenario.root / "expansion-shadow" / "history.sqlite3",
                expansion_adapter_without_transport.observer_instance_sha256,
                expansion_adapter_without_transport.provider_instance_sha256,
                expansion_adapter_without_transport.environment_sha256,
                now=expansion_base - timedelta(seconds=60),
            )
            for offset in (-60, -30, 0):
                observed_at = expansion_base + timedelta(seconds=offset)
                expansion_shadow_history.record(
                    shadow_report(
                        expansion_adapter_without_transport,
                        scenario.sandbox,
                        observed_at,
                    )
                )
            expansion_shadow_gate = evaluate_direct_traffic_shadow_history(
                expansion_shadow_history,
                DirectTrafficShadowGatePolicy(2, 3, 60, 30, 10, 0),
                now=expansion_base + timedelta(seconds=2),
            )
            expansion_shadow_dir = scenario.root / "expansion-shadow-gate"
            write_direct_traffic_shadow_gate_report(
                expansion_shadow_dir, expansion_shadow_gate
            )
            expansion_shadow, _created = create_direct_traffic_shadow_attestation(
                expansion_shadow_dir,
                expansion_shadow_history,
                EXPANSION_SHADOW_KEY,
                EXPANSION_SHADOW_KEY_ID,
                now=expansion_base + timedelta(seconds=2),
            )
            old_shadow_gate = evaluate_expansion_gate(
                expansion_rollout,
                expansion_report,
                expansion_cert,
                old_expansion_shadow,
                old_expansion_shadow_gate,
                recovery_receipt,
                production_namespace_sha256(NAMESPACE_ID),
                rollout_key=EXPANSION_ROLLOUT_KEY,
                rollout_key_id=EXPANSION_ROLLOUT_KEY_ID,
                certification_key=EXPANSION_CERTIFICATION_KEY,
                certification_key_id=EXPANSION_CERTIFICATION_KEY_ID,
                shadow_key=EXPANSION_SHADOW_KEY,
                shadow_key_id=EXPANSION_SHADOW_KEY_ID,
                now=expansion_base + timedelta(seconds=3),
            )
            self.assertEqual(old_shadow_gate["decision"], "hold")
            self.assertIn(
                "shadow_window_predates_recovery", old_shadow_gate["violations"]
            )
            pre_recovery_gate = evaluate_expansion_gate(
                pre_recovery_window_rollout,
                pre_recovery_window_report,
                expansion_cert,
                expansion_shadow,
                expansion_shadow_gate,
                recovery_receipt,
                production_namespace_sha256(NAMESPACE_ID),
                rollout_key=EXPANSION_ROLLOUT_KEY,
                rollout_key_id=EXPANSION_ROLLOUT_KEY_ID,
                certification_key=EXPANSION_CERTIFICATION_KEY,
                certification_key_id=EXPANSION_CERTIFICATION_KEY_ID,
                shadow_key=EXPANSION_SHADOW_KEY,
                shadow_key_id=EXPANSION_SHADOW_KEY_ID,
                now=expansion_base + timedelta(seconds=3),
            )
            self.assertEqual(pre_recovery_gate["decision"], "hold")
            self.assertIn(
                "health_window_predates_recovery",
                pre_recovery_gate["violations"],
            )
            with self.assertRaisesRegex(
                DirectTrafficProductionExpansionGateVerificationError,
                "signature",
            ):
                evaluate_expansion_gate(
                    expansion_rollout,
                    expansion_report,
                    expansion_cert,
                    recovery_shadow,
                    recovery_shadow_gate,
                    recovery_receipt,
                    production_namespace_sha256(NAMESPACE_ID),
                    rollout_key=EXPANSION_ROLLOUT_KEY,
                    rollout_key_id=EXPANSION_ROLLOUT_KEY_ID,
                    certification_key=EXPANSION_CERTIFICATION_KEY,
                    certification_key_id=EXPANSION_CERTIFICATION_KEY_ID,
                    shadow_key=EXPANSION_SHADOW_KEY,
                    shadow_key_id=EXPANSION_SHADOW_KEY_ID,
                    now=expansion_base + timedelta(seconds=3),
                )
            expansion_gate_report = evaluate_expansion_gate(
                expansion_rollout,
                expansion_report,
                expansion_cert,
                expansion_shadow,
                expansion_shadow_gate,
                recovery_receipt,
                production_namespace_sha256(NAMESPACE_ID),
                rollout_key=EXPANSION_ROLLOUT_KEY,
                rollout_key_id=EXPANSION_ROLLOUT_KEY_ID,
                certification_key=EXPANSION_CERTIFICATION_KEY,
                certification_key_id=EXPANSION_CERTIFICATION_KEY_ID,
                shadow_key=EXPANSION_SHADOW_KEY,
                shadow_key_id=EXPANSION_SHADOW_KEY_ID,
                now=expansion_base + timedelta(seconds=3),
            )
            expansion_gate_dir = scenario.root / "expansion-gate"
            write_expansion_gate(expansion_gate_dir, expansion_gate_report)
            attest_expansion_gate(
                expansion_gate_dir,
                EXPANSION_GATE_KEY,
                EXPANSION_GATE_KEY_ID,
                rollout_attestation=expansion_rollout,
                rollout_report=expansion_report,
                adapter_certification=expansion_cert,
                shadow_attestation=expansion_shadow,
                shadow_gate_report=expansion_shadow_gate,
                recovery_receipt=recovery_receipt,
                rollout_key=EXPANSION_ROLLOUT_KEY,
                rollout_key_id=EXPANSION_ROLLOUT_KEY_ID,
                certification_key=EXPANSION_CERTIFICATION_KEY,
                certification_key_id=EXPANSION_CERTIFICATION_KEY_ID,
                shadow_key=EXPANSION_SHADOW_KEY,
                shadow_key_id=EXPANSION_SHADOW_KEY_ID,
                now=expansion_base + timedelta(seconds=4),
            )
            expansion_platform = SQLiteProductionExpansionPlatform(
                scenario.root / "remote" / "platform.sqlite3",
                ENVIRONMENT_SHA256,
                PROVIDER_INSTANCE,
                NAMESPACE_ID,
                EXPANSION_TOKEN,
                EXPANSION_ROLLOUT_KEY,
                EXPANSION_ROLLOUT_KEY_ID,
                EXPANSION_CERTIFICATION_KEY,
                EXPANSION_CERTIFICATION_KEY_ID,
                EXPANSION_SHADOW_KEY,
                EXPANSION_SHADOW_KEY_ID,
                EXPANSION_GATE_KEY,
                EXPANSION_GATE_KEY_ID,
                initial_traffic_percent=0,
                base_url=BASE_URL,
                now=lambda: expansion_base + timedelta(seconds=5),
            )
            expansion_adapter = HTTPSProductionExpansionDirectTrafficAdapter(
                HTTPSPreproductionConfig(
                    BASE_URL,
                    ALLOWED_HOST,
                    PROVIDER_INSTANCE,
                    ENVIRONMENT_SHA256,
                    NAMESPACE_ID,
                    EXPANSION_TOKEN,
                    retry_base_seconds=0.001,
                ),
                transport=expansion_platform.transport,
                sleep=lambda _seconds: None,
            )
            expansion_authorization = verify_production_expansion_execution(
                expansion_adapter,
                scenario.sandbox,
                expansion_decision_dir,
                scenario.release_history,
                scenario.policy,
                scenario.policy_sha256,
                EXPANSION_ROLLOUT_KEY,
                EXPANSION_ROLLOUT_KEY_ID,
                expansion_cert_path,
                EXPANSION_CERTIFICATION_KEY,
                EXPANSION_CERTIFICATION_KEY_ID,
                expansion_shadow_dir,
                expansion_shadow_history,
                EXPANSION_SHADOW_KEY,
                EXPANSION_SHADOW_KEY_ID,
                expansion_gate_dir,
                EXPANSION_GATE_KEY,
                EXPANSION_GATE_KEY_ID,
                recovery_receipt,
                expansion_report,
                expansion_shadow_gate,
                now=expansion_base + timedelta(seconds=5),
            )
            expansion_receipt = apply_verified_production_expansion_execution(
                expansion_adapter,
                scenario.sandbox,
                scenario.release_history.path,
                expansion_authorization,
            )
            self.assertEqual(expansion_receipt["current_traffic_percent"], 10)
            self.assertEqual(expansion_receipt["revision"], 4)
            self.assertEqual(scenario.sandbox.status()["operations"], 4)
            self.assertEqual(expansion_platform.status()["operations"], 4)

            pre_expansion_25_report = evaluate_rollout(
                scenario.release_history,
                scenario.policy,
                scenario.policy_sha256,
                10,
                now=expansion_base + timedelta(seconds=6),
            )
            pre_expansion_25_dir = write_rollout_report(
                scenario.root / "pre-expansion-25-decisions",
                pre_expansion_25_report,
            )
            pre_expansion_25_rollout, _created = create_attestation(
                pre_expansion_25_dir,
                scenario.release_history,
                scenario.policy,
                scenario.policy_sha256,
                EXPANSION_25_ROLLOUT_KEY,
                EXPANSION_25_ROLLOUT_KEY_ID,
                now=expansion_base + timedelta(seconds=6),
            )

            expansion_25_base = expansion_base + timedelta(seconds=700)
            for sequence, offset in enumerate((-600, -300, 0), start=11):
                append_release_gate(
                    scenario.release_history,
                    scenario.root,
                    sequence,
                    expansion_25_base + timedelta(seconds=offset),
                )
            expansion_25_report = evaluate_rollout(
                scenario.release_history,
                scenario.policy,
                scenario.policy_sha256,
                10,
                now=expansion_25_base,
            )
            self.assertEqual(expansion_25_report["decision"], "expand")
            self.assertEqual(expansion_25_report["target_traffic_percent"], 25)
            expansion_25_decision_dir = write_rollout_report(
                scenario.root / "expansion-25-decisions", expansion_25_report
            )
            expansion_25_rollout, _created = create_attestation(
                expansion_25_decision_dir,
                scenario.release_history,
                scenario.policy,
                scenario.policy_sha256,
                EXPANSION_25_ROLLOUT_KEY,
                EXPANSION_25_ROLLOUT_KEY_ID,
                now=expansion_25_base + timedelta(seconds=1),
            )
            expansion_25_cert_adapter = SQLiteProductionExpansion25CertificationAdapter(
                scenario.root / "expansion-25-certification" / "adapter.sqlite3",
                ENVIRONMENT_SHA256,
                PROVIDER_INSTANCE,
                namespace_id="isolated-production-expansion-25-certification",
            )
            expansion_25_cert, expansion_25_cert_path, _created = create_certification(
                expansion_25_cert_adapter,
                scenario.root / "expansion-25-certificates",
                EXPANSION_25_CERTIFICATION_KEY,
                EXPANSION_25_CERTIFICATION_KEY_ID,
                nonce="production-expansion-25-certification",
                ttl_seconds=900,
                now=expansion_25_base,
            )
            expansion_25_adapter_without_transport = (
                HTTPSProductionExpansion25DirectTrafficAdapter(
                    HTTPSPreproductionConfig(
                        BASE_URL,
                        ALLOWED_HOST,
                        PROVIDER_INSTANCE,
                        ENVIRONMENT_SHA256,
                        NAMESPACE_ID,
                        EXPANSION_25_TOKEN,
                    )
                )
            )
            expansion_25_shadow_history = DirectTrafficShadowHistory(
                scenario.root / "expansion-25-shadow" / "history.sqlite3",
                expansion_25_adapter_without_transport.observer_instance_sha256,
                expansion_25_adapter_without_transport.provider_instance_sha256,
                expansion_25_adapter_without_transport.environment_sha256,
                now=expansion_25_base - timedelta(seconds=60),
            )
            for offset in (-60, -30, 0):
                observed_at = expansion_25_base + timedelta(seconds=offset)
                expansion_25_shadow_history.record(
                    shadow_report(
                        expansion_25_adapter_without_transport,
                        scenario.sandbox,
                        observed_at,
                    )
                )
            expansion_25_shadow_gate = evaluate_direct_traffic_shadow_history(
                expansion_25_shadow_history,
                DirectTrafficShadowGatePolicy(2, 3, 60, 30, 10, 0),
                now=expansion_25_base + timedelta(seconds=2),
            )
            expansion_25_shadow_dir = scenario.root / "expansion-25-shadow-gate"
            write_direct_traffic_shadow_gate_report(
                expansion_25_shadow_dir, expansion_25_shadow_gate
            )
            expansion_25_shadow, _created = create_direct_traffic_shadow_attestation(
                expansion_25_shadow_dir,
                expansion_25_shadow_history,
                EXPANSION_25_SHADOW_KEY,
                EXPANSION_25_SHADOW_KEY_ID,
                now=expansion_25_base + timedelta(seconds=2),
            )
            pre_expansion_25_gate = evaluate_expansion_25_gate(
                pre_expansion_25_rollout,
                pre_expansion_25_report,
                expansion_25_cert,
                expansion_25_shadow,
                expansion_25_shadow_gate,
                expansion_receipt,
                production_namespace_sha256(NAMESPACE_ID),
                rollout_key=EXPANSION_25_ROLLOUT_KEY,
                rollout_key_id=EXPANSION_25_ROLLOUT_KEY_ID,
                certification_key=EXPANSION_25_CERTIFICATION_KEY,
                certification_key_id=EXPANSION_25_CERTIFICATION_KEY_ID,
                shadow_key=EXPANSION_25_SHADOW_KEY,
                shadow_key_id=EXPANSION_25_SHADOW_KEY_ID,
                now=expansion_25_base + timedelta(seconds=3),
            )
            self.assertEqual(pre_expansion_25_gate["decision"], "hold")
            self.assertIn(
                "health_window_predates_prior_expansion",
                pre_expansion_25_gate["violations"],
            )
            with self.assertRaisesRegex(
                DirectTrafficProductionExpansion25GateVerificationError,
                "signature",
            ):
                evaluate_expansion_25_gate(
                    expansion_25_rollout,
                    expansion_25_report,
                    expansion_25_cert,
                    expansion_shadow,
                    expansion_shadow_gate,
                    expansion_receipt,
                    production_namespace_sha256(NAMESPACE_ID),
                    rollout_key=EXPANSION_25_ROLLOUT_KEY,
                    rollout_key_id=EXPANSION_25_ROLLOUT_KEY_ID,
                    certification_key=EXPANSION_25_CERTIFICATION_KEY,
                    certification_key_id=EXPANSION_25_CERTIFICATION_KEY_ID,
                    shadow_key=EXPANSION_25_SHADOW_KEY,
                    shadow_key_id=EXPANSION_25_SHADOW_KEY_ID,
                    now=expansion_25_base + timedelta(seconds=3),
                )
            expansion_25_gate_report = evaluate_expansion_25_gate(
                expansion_25_rollout,
                expansion_25_report,
                expansion_25_cert,
                expansion_25_shadow,
                expansion_25_shadow_gate,
                expansion_receipt,
                production_namespace_sha256(NAMESPACE_ID),
                rollout_key=EXPANSION_25_ROLLOUT_KEY,
                rollout_key_id=EXPANSION_25_ROLLOUT_KEY_ID,
                certification_key=EXPANSION_25_CERTIFICATION_KEY,
                certification_key_id=EXPANSION_25_CERTIFICATION_KEY_ID,
                shadow_key=EXPANSION_25_SHADOW_KEY,
                shadow_key_id=EXPANSION_25_SHADOW_KEY_ID,
                now=expansion_25_base + timedelta(seconds=3),
            )
            expansion_25_gate_dir = scenario.root / "expansion-25-gate"
            write_expansion_25_gate(
                expansion_25_gate_dir, expansion_25_gate_report
            )
            attest_expansion_25_gate(
                expansion_25_gate_dir,
                EXPANSION_25_GATE_KEY,
                EXPANSION_25_GATE_KEY_ID,
                rollout_attestation=expansion_25_rollout,
                rollout_report=expansion_25_report,
                adapter_certification=expansion_25_cert,
                shadow_attestation=expansion_25_shadow,
                shadow_gate_report=expansion_25_shadow_gate,
                prior_expansion_receipt=expansion_receipt,
                rollout_key=EXPANSION_25_ROLLOUT_KEY,
                rollout_key_id=EXPANSION_25_ROLLOUT_KEY_ID,
                certification_key=EXPANSION_25_CERTIFICATION_KEY,
                certification_key_id=EXPANSION_25_CERTIFICATION_KEY_ID,
                shadow_key=EXPANSION_25_SHADOW_KEY,
                shadow_key_id=EXPANSION_25_SHADOW_KEY_ID,
                now=expansion_25_base + timedelta(seconds=4),
            )
            expansion_25_platform = SQLiteProductionExpansion25Platform(
                scenario.root / "remote" / "platform.sqlite3",
                ENVIRONMENT_SHA256,
                PROVIDER_INSTANCE,
                NAMESPACE_ID,
                EXPANSION_25_TOKEN,
                EXPANSION_25_ROLLOUT_KEY,
                EXPANSION_25_ROLLOUT_KEY_ID,
                EXPANSION_25_CERTIFICATION_KEY,
                EXPANSION_25_CERTIFICATION_KEY_ID,
                EXPANSION_25_SHADOW_KEY,
                EXPANSION_25_SHADOW_KEY_ID,
                EXPANSION_25_GATE_KEY,
                EXPANSION_25_GATE_KEY_ID,
                initial_traffic_percent=0,
                base_url=BASE_URL,
                now=lambda: expansion_25_base + timedelta(seconds=5),
            )
            expansion_25_adapter = HTTPSProductionExpansion25DirectTrafficAdapter(
                HTTPSPreproductionConfig(
                    BASE_URL,
                    ALLOWED_HOST,
                    PROVIDER_INSTANCE,
                    ENVIRONMENT_SHA256,
                    NAMESPACE_ID,
                    EXPANSION_25_TOKEN,
                    retry_base_seconds=0.001,
                ),
                transport=expansion_25_platform.transport,
                sleep=lambda _seconds: None,
            )
            expansion_25_authorization = verify_production_expansion_25_execution(
                expansion_25_adapter,
                scenario.sandbox,
                expansion_25_decision_dir,
                scenario.release_history,
                scenario.policy,
                scenario.policy_sha256,
                EXPANSION_25_ROLLOUT_KEY,
                EXPANSION_25_ROLLOUT_KEY_ID,
                expansion_25_cert_path,
                EXPANSION_25_CERTIFICATION_KEY,
                EXPANSION_25_CERTIFICATION_KEY_ID,
                expansion_25_shadow_dir,
                expansion_25_shadow_history,
                EXPANSION_25_SHADOW_KEY,
                EXPANSION_25_SHADOW_KEY_ID,
                expansion_25_gate_dir,
                EXPANSION_25_GATE_KEY,
                EXPANSION_25_GATE_KEY_ID,
                expansion_receipt,
                expansion_25_report,
                expansion_25_shadow_gate,
                now=expansion_25_base + timedelta(seconds=5),
            )
            expansion_25_receipt = apply_verified_production_expansion_25_execution(
                expansion_25_adapter,
                scenario.sandbox,
                scenario.release_history.path,
                expansion_25_authorization,
            )
            self.assertEqual(expansion_25_receipt["current_traffic_percent"], 25)
            self.assertEqual(expansion_25_receipt["revision"], 5)
            self.assertEqual(scenario.sandbox.status()["operations"], 5)
            self.assertEqual(expansion_25_platform.status()["operations"], 5)

            expansion_50_base = expansion_25_base + timedelta(seconds=700)
            for sequence, offset in enumerate((-600, -300, 0), start=14):
                append_release_gate(
                    scenario.release_history,
                    scenario.root,
                    sequence,
                    expansion_50_base + timedelta(seconds=offset),
                )
            expansion_50_report = evaluate_rollout(
                scenario.release_history,
                scenario.policy,
                scenario.policy_sha256,
                25,
                now=expansion_50_base,
            )
            self.assertEqual(expansion_50_report["decision"], "expand")
            self.assertEqual(expansion_50_report["target_traffic_percent"], 50)
            expansion_50_decision_dir = write_rollout_report(
                scenario.root / "expansion-50-decisions", expansion_50_report
            )
            expansion_50_rollout, _created = create_attestation(
                expansion_50_decision_dir,
                scenario.release_history,
                scenario.policy,
                scenario.policy_sha256,
                EXPANSION_50_ROLLOUT_KEY,
                EXPANSION_50_ROLLOUT_KEY_ID,
                now=expansion_50_base + timedelta(seconds=1),
            )
            expansion_50_cert_adapter = SQLiteProductionExpansion50CertificationAdapter(
                scenario.root / "expansion-50-certification" / "adapter.sqlite3",
                ENVIRONMENT_SHA256,
                PROVIDER_INSTANCE,
                namespace_id="isolated-production-expansion-50-certification",
            )
            expansion_50_cert, expansion_50_cert_path, _created = create_certification(
                expansion_50_cert_adapter,
                scenario.root / "expansion-50-certificates",
                EXPANSION_50_CERTIFICATION_KEY,
                EXPANSION_50_CERTIFICATION_KEY_ID,
                nonce="production-expansion-50-certification",
                ttl_seconds=900,
                now=expansion_50_base,
            )
            expansion_50_adapter_without_transport = (
                HTTPSProductionExpansion50DirectTrafficAdapter(
                    HTTPSPreproductionConfig(
                        BASE_URL,
                        ALLOWED_HOST,
                        PROVIDER_INSTANCE,
                        ENVIRONMENT_SHA256,
                        NAMESPACE_ID,
                        EXPANSION_50_TOKEN,
                    )
                )
            )
            expansion_50_shadow_history = DirectTrafficShadowHistory(
                scenario.root / "expansion-50-shadow" / "history.sqlite3",
                expansion_50_adapter_without_transport.observer_instance_sha256,
                expansion_50_adapter_without_transport.provider_instance_sha256,
                expansion_50_adapter_without_transport.environment_sha256,
                now=expansion_50_base - timedelta(seconds=60),
            )
            for offset in (-60, -30, 0):
                observed_at = expansion_50_base + timedelta(seconds=offset)
                expansion_50_shadow_history.record(
                    shadow_report(
                        expansion_50_adapter_without_transport,
                        scenario.sandbox,
                        observed_at,
                    )
                )
            expansion_50_shadow_gate = evaluate_direct_traffic_shadow_history(
                expansion_50_shadow_history,
                DirectTrafficShadowGatePolicy(2, 3, 60, 30, 10, 0),
                now=expansion_50_base + timedelta(seconds=2),
            )
            expansion_50_shadow_dir = scenario.root / "expansion-50-shadow-gate"
            write_direct_traffic_shadow_gate_report(
                expansion_50_shadow_dir, expansion_50_shadow_gate
            )
            expansion_50_shadow, _created = create_direct_traffic_shadow_attestation(
                expansion_50_shadow_dir,
                expansion_50_shadow_history,
                EXPANSION_50_SHADOW_KEY,
                EXPANSION_50_SHADOW_KEY_ID,
                now=expansion_50_base + timedelta(seconds=2),
            )
            with self.assertRaises(DirectTrafficProductionExpansion50GateConfigError):
                evaluate_expansion_50_gate(
                    expansion_50_rollout,
                    expansion_50_report,
                    expansion_50_cert,
                    expansion_50_shadow,
                    expansion_50_shadow_gate,
                    expansion_receipt,
                    production_namespace_sha256(NAMESPACE_ID),
                    rollout_key=EXPANSION_50_ROLLOUT_KEY,
                    rollout_key_id=EXPANSION_50_ROLLOUT_KEY_ID,
                    certification_key=EXPANSION_50_CERTIFICATION_KEY,
                    certification_key_id=EXPANSION_50_CERTIFICATION_KEY_ID,
                    shadow_key=EXPANSION_50_SHADOW_KEY,
                    shadow_key_id=EXPANSION_50_SHADOW_KEY_ID,
                    now=expansion_50_base + timedelta(seconds=3),
                )
            expansion_50_gate_report = evaluate_expansion_50_gate(
                expansion_50_rollout,
                expansion_50_report,
                expansion_50_cert,
                expansion_50_shadow,
                expansion_50_shadow_gate,
                expansion_25_receipt,
                production_namespace_sha256(NAMESPACE_ID),
                rollout_key=EXPANSION_50_ROLLOUT_KEY,
                rollout_key_id=EXPANSION_50_ROLLOUT_KEY_ID,
                certification_key=EXPANSION_50_CERTIFICATION_KEY,
                certification_key_id=EXPANSION_50_CERTIFICATION_KEY_ID,
                shadow_key=EXPANSION_50_SHADOW_KEY,
                shadow_key_id=EXPANSION_50_SHADOW_KEY_ID,
                now=expansion_50_base + timedelta(seconds=3),
            )
            expansion_50_gate_dir = scenario.root / "expansion-50-gate"
            write_expansion_50_gate(expansion_50_gate_dir, expansion_50_gate_report)
            attest_expansion_50_gate(
                expansion_50_gate_dir,
                EXPANSION_50_GATE_KEY,
                EXPANSION_50_GATE_KEY_ID,
                rollout_attestation=expansion_50_rollout,
                rollout_report=expansion_50_report,
                adapter_certification=expansion_50_cert,
                shadow_attestation=expansion_50_shadow,
                shadow_gate_report=expansion_50_shadow_gate,
                prior_expansion_receipt=expansion_25_receipt,
                rollout_key=EXPANSION_50_ROLLOUT_KEY,
                rollout_key_id=EXPANSION_50_ROLLOUT_KEY_ID,
                certification_key=EXPANSION_50_CERTIFICATION_KEY,
                certification_key_id=EXPANSION_50_CERTIFICATION_KEY_ID,
                shadow_key=EXPANSION_50_SHADOW_KEY,
                shadow_key_id=EXPANSION_50_SHADOW_KEY_ID,
                now=expansion_50_base + timedelta(seconds=4),
            )
            expansion_50_platform = SQLiteProductionExpansion50Platform(
                scenario.root / "remote" / "platform.sqlite3",
                ENVIRONMENT_SHA256,
                PROVIDER_INSTANCE,
                NAMESPACE_ID,
                EXPANSION_50_TOKEN,
                EXPANSION_50_ROLLOUT_KEY,
                EXPANSION_50_ROLLOUT_KEY_ID,
                EXPANSION_50_CERTIFICATION_KEY,
                EXPANSION_50_CERTIFICATION_KEY_ID,
                EXPANSION_50_SHADOW_KEY,
                EXPANSION_50_SHADOW_KEY_ID,
                EXPANSION_50_GATE_KEY,
                EXPANSION_50_GATE_KEY_ID,
                initial_traffic_percent=0,
                base_url=BASE_URL,
                now=lambda: expansion_50_base + timedelta(seconds=5),
            )
            expansion_50_adapter = HTTPSProductionExpansion50DirectTrafficAdapter(
                HTTPSPreproductionConfig(
                    BASE_URL,
                    ALLOWED_HOST,
                    PROVIDER_INSTANCE,
                    ENVIRONMENT_SHA256,
                    NAMESPACE_ID,
                    EXPANSION_50_TOKEN,
                    retry_base_seconds=0.001,
                ),
                transport=expansion_50_platform.transport,
                sleep=lambda _seconds: None,
            )
            expansion_50_authorization = verify_production_expansion_50_execution(
                expansion_50_adapter,
                scenario.sandbox,
                expansion_50_decision_dir,
                scenario.release_history,
                scenario.policy,
                scenario.policy_sha256,
                EXPANSION_50_ROLLOUT_KEY,
                EXPANSION_50_ROLLOUT_KEY_ID,
                expansion_50_cert_path,
                EXPANSION_50_CERTIFICATION_KEY,
                EXPANSION_50_CERTIFICATION_KEY_ID,
                expansion_50_shadow_dir,
                expansion_50_shadow_history,
                EXPANSION_50_SHADOW_KEY,
                EXPANSION_50_SHADOW_KEY_ID,
                expansion_50_gate_dir,
                EXPANSION_50_GATE_KEY,
                EXPANSION_50_GATE_KEY_ID,
                expansion_25_receipt,
                expansion_50_report,
                expansion_50_shadow_gate,
                now=expansion_50_base + timedelta(seconds=5),
            )
            expansion_50_receipt = apply_verified_production_expansion_50_execution(
                expansion_50_adapter,
                scenario.sandbox,
                scenario.release_history.path,
                expansion_50_authorization,
            )
            self.assertEqual(expansion_50_receipt["current_traffic_percent"], 50)
            self.assertEqual(expansion_50_receipt["revision"], 6)
            self.assertEqual(scenario.sandbox.status()["operations"], 6)
            self.assertEqual(expansion_50_platform.status()["operations"], 6)

            expansion_100_base = expansion_50_base + timedelta(seconds=700)
            for sequence, offset in enumerate((-600, -300, 0), start=17):
                append_release_gate(
                    scenario.release_history,
                    scenario.root,
                    sequence,
                    expansion_100_base + timedelta(seconds=offset),
                )
            expansion_100_report = evaluate_rollout(
                scenario.release_history,
                scenario.policy,
                scenario.policy_sha256,
                50,
                now=expansion_100_base,
            )
            self.assertEqual(expansion_100_report["decision"], "expand")
            self.assertEqual(expansion_100_report["target_traffic_percent"], 100)
            expansion_100_decision_dir = write_rollout_report(
                scenario.root / "expansion-100-decisions", expansion_100_report
            )
            expansion_100_rollout, _created = create_attestation(
                expansion_100_decision_dir,
                scenario.release_history,
                scenario.policy,
                scenario.policy_sha256,
                EXPANSION_100_ROLLOUT_KEY,
                EXPANSION_100_ROLLOUT_KEY_ID,
                now=expansion_100_base + timedelta(seconds=1),
            )
            expansion_100_cert_adapter = SQLiteProductionExpansion100CertificationAdapter(
                scenario.root / "expansion-100-certification" / "adapter.sqlite3",
                ENVIRONMENT_SHA256,
                PROVIDER_INSTANCE,
                namespace_id="isolated-production-expansion-100-certification",
            )
            expansion_100_cert, expansion_100_cert_path, _created = create_certification(
                expansion_100_cert_adapter,
                scenario.root / "expansion-100-certificates",
                EXPANSION_100_CERTIFICATION_KEY,
                EXPANSION_100_CERTIFICATION_KEY_ID,
                nonce="production-expansion-100-certification",
                ttl_seconds=900,
                now=expansion_100_base,
            )
            expansion_100_adapter_without_transport = (
                HTTPSProductionExpansion100DirectTrafficAdapter(
                    HTTPSPreproductionConfig(
                        BASE_URL,
                        ALLOWED_HOST,
                        PROVIDER_INSTANCE,
                        ENVIRONMENT_SHA256,
                        NAMESPACE_ID,
                        EXPANSION_100_TOKEN,
                    )
                )
            )
            expansion_100_shadow_history = DirectTrafficShadowHistory(
                scenario.root / "expansion-100-shadow" / "history.sqlite3",
                expansion_100_adapter_without_transport.observer_instance_sha256,
                expansion_100_adapter_without_transport.provider_instance_sha256,
                expansion_100_adapter_without_transport.environment_sha256,
                now=expansion_100_base - timedelta(seconds=60),
            )
            for offset in (-60, -30, 0):
                observed_at = expansion_100_base + timedelta(seconds=offset)
                expansion_100_shadow_history.record(
                    shadow_report(
                        expansion_100_adapter_without_transport,
                        scenario.sandbox,
                        observed_at,
                    )
                )
            expansion_100_shadow_gate = evaluate_direct_traffic_shadow_history(
                expansion_100_shadow_history,
                DirectTrafficShadowGatePolicy(2, 3, 60, 30, 10, 0),
                now=expansion_100_base + timedelta(seconds=2),
            )
            expansion_100_shadow_dir = scenario.root / "expansion-100-shadow-gate"
            write_direct_traffic_shadow_gate_report(
                expansion_100_shadow_dir, expansion_100_shadow_gate
            )
            expansion_100_shadow, _created = create_direct_traffic_shadow_attestation(
                expansion_100_shadow_dir,
                expansion_100_shadow_history,
                EXPANSION_100_SHADOW_KEY,
                EXPANSION_100_SHADOW_KEY_ID,
                now=expansion_100_base + timedelta(seconds=2),
            )
            with self.assertRaises(DirectTrafficProductionExpansion100GateConfigError):
                evaluate_expansion_100_gate(
                    expansion_100_rollout,
                    expansion_100_report,
                    expansion_100_cert,
                    expansion_100_shadow,
                    expansion_100_shadow_gate,
                    expansion_25_receipt,
                    production_namespace_sha256(NAMESPACE_ID),
                    rollout_key=EXPANSION_100_ROLLOUT_KEY,
                    rollout_key_id=EXPANSION_100_ROLLOUT_KEY_ID,
                    certification_key=EXPANSION_100_CERTIFICATION_KEY,
                    certification_key_id=EXPANSION_100_CERTIFICATION_KEY_ID,
                    shadow_key=EXPANSION_100_SHADOW_KEY,
                    shadow_key_id=EXPANSION_100_SHADOW_KEY_ID,
                    now=expansion_100_base + timedelta(seconds=3),
                )
            expansion_100_gate_report = evaluate_expansion_100_gate(
                expansion_100_rollout,
                expansion_100_report,
                expansion_100_cert,
                expansion_100_shadow,
                expansion_100_shadow_gate,
                expansion_50_receipt,
                production_namespace_sha256(NAMESPACE_ID),
                rollout_key=EXPANSION_100_ROLLOUT_KEY,
                rollout_key_id=EXPANSION_100_ROLLOUT_KEY_ID,
                certification_key=EXPANSION_100_CERTIFICATION_KEY,
                certification_key_id=EXPANSION_100_CERTIFICATION_KEY_ID,
                shadow_key=EXPANSION_100_SHADOW_KEY,
                shadow_key_id=EXPANSION_100_SHADOW_KEY_ID,
                now=expansion_100_base + timedelta(seconds=3),
            )
            expansion_100_gate_dir = scenario.root / "expansion-100-gate"
            write_expansion_100_gate(expansion_100_gate_dir, expansion_100_gate_report)
            attest_expansion_100_gate(
                expansion_100_gate_dir,
                EXPANSION_100_GATE_KEY,
                EXPANSION_100_GATE_KEY_ID,
                rollout_attestation=expansion_100_rollout,
                rollout_report=expansion_100_report,
                adapter_certification=expansion_100_cert,
                shadow_attestation=expansion_100_shadow,
                shadow_gate_report=expansion_100_shadow_gate,
                prior_expansion_receipt=expansion_50_receipt,
                rollout_key=EXPANSION_100_ROLLOUT_KEY,
                rollout_key_id=EXPANSION_100_ROLLOUT_KEY_ID,
                certification_key=EXPANSION_100_CERTIFICATION_KEY,
                certification_key_id=EXPANSION_100_CERTIFICATION_KEY_ID,
                shadow_key=EXPANSION_100_SHADOW_KEY,
                shadow_key_id=EXPANSION_100_SHADOW_KEY_ID,
                now=expansion_100_base + timedelta(seconds=4),
            )
            expansion_100_platform = SQLiteProductionExpansion100Platform(
                scenario.root / "remote" / "platform.sqlite3",
                ENVIRONMENT_SHA256,
                PROVIDER_INSTANCE,
                NAMESPACE_ID,
                EXPANSION_100_TOKEN,
                EXPANSION_100_ROLLOUT_KEY,
                EXPANSION_100_ROLLOUT_KEY_ID,
                EXPANSION_100_CERTIFICATION_KEY,
                EXPANSION_100_CERTIFICATION_KEY_ID,
                EXPANSION_100_SHADOW_KEY,
                EXPANSION_100_SHADOW_KEY_ID,
                EXPANSION_100_GATE_KEY,
                EXPANSION_100_GATE_KEY_ID,
                initial_traffic_percent=0,
                base_url=BASE_URL,
                now=lambda: expansion_100_base + timedelta(seconds=5),
            )
            expansion_100_adapter = HTTPSProductionExpansion100DirectTrafficAdapter(
                HTTPSPreproductionConfig(
                    BASE_URL,
                    ALLOWED_HOST,
                    PROVIDER_INSTANCE,
                    ENVIRONMENT_SHA256,
                    NAMESPACE_ID,
                    EXPANSION_100_TOKEN,
                    retry_base_seconds=0.001,
                ),
                transport=expansion_100_platform.transport,
                sleep=lambda _seconds: None,
            )
            expansion_100_authorization = verify_production_expansion_100_execution(
                expansion_100_adapter,
                scenario.sandbox,
                expansion_100_decision_dir,
                scenario.release_history,
                scenario.policy,
                scenario.policy_sha256,
                EXPANSION_100_ROLLOUT_KEY,
                EXPANSION_100_ROLLOUT_KEY_ID,
                expansion_100_cert_path,
                EXPANSION_100_CERTIFICATION_KEY,
                EXPANSION_100_CERTIFICATION_KEY_ID,
                expansion_100_shadow_dir,
                expansion_100_shadow_history,
                EXPANSION_100_SHADOW_KEY,
                EXPANSION_100_SHADOW_KEY_ID,
                expansion_100_gate_dir,
                EXPANSION_100_GATE_KEY,
                EXPANSION_100_GATE_KEY_ID,
                expansion_50_receipt,
                expansion_100_report,
                expansion_100_shadow_gate,
                now=expansion_100_base + timedelta(seconds=5),
            )
            expansion_100_receipt = apply_verified_production_expansion_100_execution(
                expansion_100_adapter,
                scenario.sandbox,
                scenario.release_history.path,
                expansion_100_authorization,
            )
            self.assertEqual(expansion_100_receipt["current_traffic_percent"], 100)
            self.assertEqual(expansion_100_receipt["revision"], 7)
            self.assertEqual(scenario.sandbox.status()["operations"], 7)
            self.assertEqual(expansion_100_platform.status()["operations"], 7)

            terminal_report = evaluate_rollout(
                scenario.release_history,
                scenario.policy,
                scenario.policy_sha256,
                100,
                now=expansion_100_base + timedelta(seconds=6),
            )
            self.assertEqual(terminal_report["decision"], "hold")
            self.assertEqual(terminal_report["target_traffic_percent"], 100)
            self.assertEqual(
                terminal_report["reasons"][0]["code"], "maximum_stage_reached"
            )

    def test_four_proofs_apply_only_zero_to_five_and_replay_once(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            scenario = create_scenario(Path(directory))
            first = apply_verified_production_execution(
                scenario.adapter,
                scenario.sandbox,
                scenario.release_history.path,
                scenario.authorization,
            )
            second = apply_verified_production_execution(
                scenario.adapter,
                scenario.sandbox,
                scenario.release_history.path,
                scenario.authorization,
            )

            self.assertEqual(first["outcome"], "applied")
            self.assertEqual(first["current_traffic_percent"], 5)
            self.assertTrue(second["remote_reused"])
            self.assertTrue(second["local_reused"])
            self.assertEqual(scenario.platform.write_calls, 1)
            self.assertEqual(scenario.platform.status()["operations"], 1)
            self.assertEqual(scenario.sandbox.status()["operations"], 1)
            self.assertEqual(
                first["production_gate_attestation_id"],
                scenario.authorization.production_gate_attestation["attestation_id"],
            )

    def test_timeout_after_commit_reconciles_without_second_production_put(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            scenario = create_scenario(Path(directory))
            scenario.platform.timeout_after_next_commit = True

            receipt = apply_verified_production_execution(
                scenario.adapter,
                scenario.sandbox,
                scenario.release_history.path,
                scenario.authorization,
            )

            self.assertEqual(receipt["outcome"], "applied")
            self.assertEqual(scenario.platform.write_calls, 1)
            self.assertEqual(scenario.platform.status()["current_traffic_percent"], 5)

    def test_provider_independently_rejects_tampered_fourth_proof(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            scenario = create_scenario(Path(directory))
            payload = json.loads(
                _canonical_json(dict(scenario.authorization.execution.payload)).decode()
            )
            payload["production_gate_attestation"]["signature"] = "f" * 64
            payload["production_gate_attestation_sha256"] = hashlib.sha256(
                _canonical_json(payload["production_gate_attestation"])
            ).hexdigest()
            payload["request_sha256"] = hashlib.sha256(
                _canonical_json(
                    {key: item for key, item in payload.items() if key != "request_sha256"}
                )
            ).hexdigest()
            request = scenario.authorization.execution.request
            apply_url = (
                f"{BASE_URL}/v1/namespaces/{NAMESPACE_ID}/direct-traffic/changes/"
                f"{request.idempotency_key}"
            )

            with self.assertRaisesRegex(
                PreproductionProviderError, "provider_production_gate_proof"
            ):
                scenario.platform.transport(
                    "PUT", apply_url, TOKEN, 5.0, 32 * 1024, _canonical_json(payload)
                )
            self.assertEqual(scenario.platform.status()["operations"], 0)

    def test_gate_rejects_tampering_and_holds_valid_non_initial_rollout(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            scenario = create_scenario(Path(directory))
            local_bundle, live_bundle = create_preproduction_drill_bundles(
                Path(directory) / "gate-negative-drills"
            )
            wrong_certification = {**scenario.certification, "adapter": "wrong-adapter"}
            with self.assertRaisesRegex(
                DirectTrafficProductionGateVerificationError, "signature"
            ):
                evaluate_production_gate(
                    local_bundle,
                    live_bundle,
                    DRILL_KEY,
                    DRILL_KEY_ID,
                    wrong_certification,
                    scenario.shadow_attestation,
                    scenario.rollout_attestation,
                    production_namespace_sha256(NAMESPACE_ID),
                    certification_key=CERTIFICATION_KEY,
                    certification_key_id=CERTIFICATION_KEY_ID,
                    shadow_key=SHADOW_KEY,
                    shadow_key_id=SHADOW_KEY_ID,
                    rollout_key=ROLLOUT_KEY,
                    rollout_key_id=ROLLOUT_KEY_ID,
                    now=NOW + timedelta(seconds=73),
                )

            later_report = evaluate_rollout(
                scenario.release_history,
                scenario.policy,
                scenario.policy_sha256,
                5,
                now=NOW + timedelta(seconds=69),
            )
            later_dir = write_rollout_report(Path(directory) / "later-decisions", later_report)
            later_rollout, _created = create_attestation(
                later_dir,
                scenario.release_history,
                scenario.policy,
                scenario.policy_sha256,
                ROLLOUT_KEY,
                ROLLOUT_KEY_ID,
                now=NOW + timedelta(seconds=70),
            )
            report = evaluate_production_gate(
                local_bundle,
                live_bundle,
                DRILL_KEY,
                DRILL_KEY_ID,
                scenario.certification,
                scenario.shadow_attestation,
                later_rollout,
                production_namespace_sha256(NAMESPACE_ID),
                certification_key=CERTIFICATION_KEY,
                certification_key_id=CERTIFICATION_KEY_ID,
                shadow_key=SHADOW_KEY,
                shadow_key_id=SHADOW_KEY_ID,
                rollout_key=ROLLOUT_KEY,
                rollout_key_id=ROLLOUT_KEY_ID,
                now=NOW + timedelta(seconds=73),
            )

            self.assertEqual(report["decision"], "hold")
            self.assertIn("production_initial_stage_not_zero", report["violations"])
            self.assertIn("production_target_not_five", report["violations"])

    def test_namespace_token_and_embedded_gate_trust_are_isolated(self) -> None:
        config = HTTPSPreproductionConfig(
            BASE_URL,
            ALLOWED_HOST,
            PROVIDER_INSTANCE,
            ENVIRONMENT_SHA256,
            NAMESPACE_ID,
            TOKEN,
        )
        with patch.dict(os.environ, {PREPRODUCTION_TOKEN_ENV: TOKEN}):
            with self.assertRaisesRegex(
                DirectTrafficPreproductionConfigError, "distinct"
            ):
                HTTPSProductionDirectTrafficAdapter(config)
        with self.assertRaisesRegex(DirectTrafficPreproductionConfigError, "production-"):
            HTTPSProductionDirectTrafficAdapter(
                HTTPSPreproductionConfig(
                    BASE_URL,
                    ALLOWED_HOST,
                    PROVIDER_INSTANCE,
                    ENVIRONMENT_SHA256,
                    "preproduction-agent-direct-blue",
                    TOKEN,
                )
            )

        with tempfile.TemporaryDirectory() as directory:
            scenario = create_scenario(Path(directory))
            embedded = scenario.authorization.production_gate_attestation
            verified = verify_embedded_production_gate_attestation(
                embedded,
                PRODUCTION_GATE_KEY,
                PRODUCTION_GATE_KEY_ID,
                environment_sha256=ENVIRONMENT_SHA256,
                provider_instance_sha256=scenario.adapter.provider_instance_sha256,
                namespace_sha256=scenario.adapter.namespace_sha256,
                observer_instance_sha256=scenario.adapter.observer_instance_sha256,
                now=VERIFY_NOW,
            )
            self.assertEqual(verified["decision"], "pass")
            with self.assertRaises(DirectTrafficProductionGateVerificationError):
                verify_embedded_production_gate_attestation(
                    embedded,
                    b"another-production-gate-key-at-least-32-bytes",
                    PRODUCTION_GATE_KEY_ID,
                    environment_sha256=ENVIRONMENT_SHA256,
                    provider_instance_sha256=scenario.adapter.provider_instance_sha256,
                    namespace_sha256=scenario.adapter.namespace_sha256,
                    observer_instance_sha256=scenario.adapter.observer_instance_sha256,
                    now=VERIFY_NOW,
                )
            with self.assertRaisesRegex(
                DirectTrafficPreproductionConfigError, "distinct"
            ):
                verify_production_execution(
                    scenario.adapter,
                    scenario.sandbox,
                    scenario.decision_dir,
                    scenario.release_history,
                    scenario.policy,
                    scenario.policy_sha256,
                    ROLLOUT_KEY,
                    ROLLOUT_KEY_ID,
                    scenario.certification_path,
                    ROLLOUT_KEY,
                    CERTIFICATION_KEY_ID,
                    scenario.shadow_gate_dir,
                    scenario.shadow_history,
                    SHADOW_KEY,
                    SHADOW_KEY_ID,
                    scenario.production_gate_dir,
                    PRODUCTION_GATE_KEY,
                    PRODUCTION_GATE_KEY_ID,
                    now=VERIFY_NOW,
                )
            with self.assertRaises(DirectTrafficProductionGateError):
                verify_production_gate_attestation(
                    scenario.production_gate_dir,
                    PRODUCTION_GATE_KEY,
                    PRODUCTION_GATE_KEY_ID,
                    max_age_seconds=0,
                    now=VERIFY_NOW,
                )


if __name__ == "__main__":
    unittest.main()
