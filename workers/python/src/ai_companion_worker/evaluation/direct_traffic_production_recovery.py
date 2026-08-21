from __future__ import annotations

import argparse
import json
import os
import sqlite3
import sys
import urllib.parse
from dataclasses import asdict, dataclass
from datetime import datetime
from pathlib import Path
from typing import Any, Mapping, Sequence

from .deployment_controller import _canonical_json, _digest, _normalize_now, _parse_timestamp
from .direct_rollout import (
    PRODUCTION_RECOVERY_KEY_ENV as ROLLOUT_KEY_ENV,
    PRODUCTION_RECOVERY_KEY_ID_ENV as ROLLOUT_KEY_ID_ENV,
    REPORT_FILE as ROLLOUT_REPORT_FILE,
    load_policy,
    verify_attestation as verify_rollout_attestation,
)
from .direct_rollout_traffic import SQLiteDirectTrafficSandbox, TrafficChangeRequest, build_traffic_request
from .direct_traffic_certification import (
    certification_key_from_environment,
    validate_execution_binding,
    verify_certification,
)
from .direct_traffic_preproduction import (
    DirectTrafficExecutionAuthorization,
    DirectTrafficPreproductionConfigError,
    DirectTrafficPreproductionIndeterminateError,
    HTTPSPreproductionConfig,
    PreproductionResult,
    PreproductionState,
    _public_state,
    _key_from_environment,
    _validate_result,
    _validate_state,
)
from .direct_traffic_production import (
    HTTPSProductionDirectTrafficAdapter,
    TOKEN_ENV as PRODUCTION_TOKEN_ENV,
    _validate_production_environment,
)
from .direct_traffic_production_emergency import TOKEN_ENV as EMERGENCY_TOKEN_ENV
from .direct_traffic_production_recovery_gate import (
    KEY_ENV as RECOVERY_GATE_KEY_ENV,
    KEY_ID_ENV as RECOVERY_GATE_KEY_ID_ENV,
    _read_json,
    _read_gate,
    validate_rollback_receipt,
    verify_recovery_gate_attestation,
)
from .direct_traffic_shadow import (
    DirectTrafficShadowHistory,
    ReadOnlyDirectTrafficProviderConfig,
)
from .direct_traffic_shadow_gate import (
    PRODUCTION_RECOVERY_KEY_ENV as SHADOW_KEY_ENV,
    PRODUCTION_RECOVERY_KEY_ID_ENV as SHADOW_KEY_ID_ENV,
    verify_direct_traffic_shadow_attestation,
)
from .release_gate import DEFAULT_CANARY_LEASE_ROOT, try_acquire_canary_lease
from .release_gate_history import ReleaseGateHistory, environment_sha256


ADAPTER_NAME = "https-direct-traffic-production-recovery"
IMPLEMENTATION_VERSION = "1.0.0"
EXECUTION_SCHEMA_VERSION = "agent-direct-traffic-production-recovery-execution-v1"
STATE_SCHEMA_VERSION = "agent-direct-traffic-production-recovery-state-v1"
RESULT_SCHEMA_VERSION = "agent-direct-traffic-production-recovery-result-v1"
RECEIPT_SCHEMA_VERSION = "agent-direct-traffic-production-recovery-receipt-v1"
TOKEN_ENV = "OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_RECOVERY_TOKEN"


@dataclass(frozen=True)
class VerifiedProductionRecoveryExecution:
    request: TrafficChangeRequest
    rollout_attestation: Mapping[str, Any]
    certification: Mapping[str, Any]
    certification_sha256: str
    shadow_attestation: Mapping[str, Any]
    shadow_attestation_sha256: str
    recovery_gate_attestation: Mapping[str, Any]
    recovery_gate_attestation_sha256: str
    rollback_receipt: Mapping[str, Any]
    rollback_receipt_sha256: str
    local_state: Mapping[str, Any]
    payload: Mapping[str, Any]


class HTTPSProductionRecoveryDirectTrafficAdapter(HTTPSProductionDirectTrafficAdapter):
    name = ADAPTER_NAME
    implementation_version = IMPLEMENTATION_VERSION
    execution_schema_version = EXECUTION_SCHEMA_VERSION
    execution_extra_fields: frozenset[str] = frozenset()

    def __init__(self, config: HTTPSPreproductionConfig, **kwargs: Any) -> None:
        normal_tokens = {
            value
            for name in (PRODUCTION_TOKEN_ENV, EMERGENCY_TOKEN_ENV)
            if (value := os.environ.get(name)) is not None
        }
        if config.token in normal_tokens:
            raise DirectTrafficPreproductionConfigError(
                "recovery token must be distinct from production and emergency tokens"
            )
        super().__init__(config, **kwargs)

    def _observer_config(
        self, config: HTTPSPreproductionConfig
    ) -> ReadOnlyDirectTrafficProviderConfig:
        namespace = urllib.parse.quote(config.namespace_id, safe="")
        return ReadOnlyDirectTrafficProviderConfig(
            name=self.name,
            base_url=config.base_url,
            allowed_host=config.allowed_host,
            provider_instance=config.provider_instance,
            environment_sha256=config.environment_sha256,
            token=config.token,
            state_path=f"/v1/namespaces/{namespace}/direct-traffic/shadow-state",
            timeout_seconds=config.timeout_seconds,
            max_attempts=config.max_read_attempts,
            retry_base_seconds=config.retry_base_seconds,
            max_response_bytes=config.max_response_bytes,
        )

    def _validate_provider_state(self, value: Any) -> PreproductionState:
        return _validate_state(value, schema_version=STATE_SCHEMA_VERSION)

    def _validate_provider_result(self, value: Any) -> PreproductionResult:
        return _validate_result(value, schema_version=RESULT_SCHEMA_VERSION)

    def _validate_authorization(
        self, authorization: DirectTrafficExecutionAuthorization
    ) -> DirectTrafficExecutionAuthorization:
        if not isinstance(authorization, VerifiedProductionRecoveryExecution):
            raise DirectTrafficPreproductionConfigError(
                "invalid recovery production authorization"
            )
        return _validate_recovery_execution(authorization, self)


def verify_production_recovery_execution(
    adapter: HTTPSProductionRecoveryDirectTrafficAdapter,
    sandbox: SQLiteDirectTrafficSandbox,
    decision_dir: Path,
    release_history: ReleaseGateHistory,
    policy: Mapping[str, Any],
    policy_sha256: str,
    rollout_key: bytes,
    rollout_key_id: str,
    certification_path: Path,
    certification_key: bytes,
    certification_key_id: str,
    shadow_gate_dir: Path,
    shadow_history: DirectTrafficShadowHistory,
    shadow_key: bytes,
    shadow_key_id: str,
    recovery_gate_dir: Path,
    recovery_gate_key: bytes,
    recovery_gate_key_id: str,
    rollback_receipt: Mapping[str, Any],
    rollout_report: Mapping[str, Any],
    *,
    max_age_seconds: int = 900,
    now: datetime | None = None,
) -> VerifiedProductionRecoveryExecution:
    current_time = _normalize_now(now)
    _validate_recovery_secrets(
        adapter.config.token,
        rollout_key,
        certification_key,
        shadow_key,
        recovery_gate_key,
    )
    if (
        sandbox.provider_instance_sha256 != adapter.provider_instance_sha256
        or sandbox.environment_sha256 != adapter.environment_sha256
        or shadow_history.observer_instance_sha256 != adapter.observer_instance_sha256
        or shadow_history.provider_instance_sha256 != adapter.provider_instance_sha256
        or shadow_history.environment_sha256 != adapter.environment_sha256
    ):
        raise DirectTrafficPreproductionConfigError(
            "recovery state or shadow evidence is not bound to the adapter"
        )
    request = build_traffic_request(
        sandbox, decision_dir, release_history, policy, policy_sha256,
        rollout_key, rollout_key_id, max_age_seconds=max_age_seconds, now=current_time,
    )
    rollout = verify_rollout_attestation(
        decision_dir, release_history, policy, policy_sha256,
        rollout_key, rollout_key_id, max_age_seconds=max_age_seconds,
        require_decision="expand", now=current_time,
    )
    certification = verify_certification(
        certification_path, adapter, certification_key, certification_key_id,
        require_certification_namespace=False, now=current_time,
    )
    validate_execution_binding(certification, adapter)
    shadow = verify_direct_traffic_shadow_attestation(
        shadow_gate_dir, shadow_history, shadow_key, shadow_key_id,
        max_age_seconds=max_age_seconds, require_decision="pass", now=current_time,
    )
    gate = verify_recovery_gate_attestation(
        recovery_gate_dir, recovery_gate_key, recovery_gate_key_id,
        max_age_seconds=max_age_seconds, now=current_time,
    )
    gate_report = _read_gate(recovery_gate_dir, expect_attestation=True)
    receipt = validate_rollback_receipt(rollback_receipt)
    local_state = sandbox.status()
    if (
        request.decision != "expand"
        or request.expected_traffic_percent != 0
        or request.target_traffic_percent != 5
        or request.expected_revision != 2
        or local_state["current_traffic_percent"] != 0
        or local_state["revision"] != 2
        or local_state["operations"] != 2
        or receipt["history_chain_sha256"] != local_state["history_chain_sha256"]
        or receipt["namespace_sha256"] != adapter.namespace_sha256
    ):
        raise DirectTrafficPreproductionConfigError(
            "recovery permits only the post-rollback 0%-to-5% transition"
        )
    digests = {
        "rollout_attestation_sha256": _digest(_canonical_json(rollout)),
        "rollout_report_sha256": _digest(_canonical_json(dict(rollout_report))),
        "adapter_certification_sha256": _digest(_canonical_json(certification)),
        "shadow_attestation_sha256": _digest(_canonical_json(shadow)),
        "rollback_receipt_sha256": _digest(_canonical_json(receipt)),
    }
    if gate_report["source"] != digests:
        raise DirectTrafficPreproductionConfigError(
            "recovery Gate is not bound to the execution proofs"
        )
    gate_sha256 = _digest(_canonical_json(gate))
    payload: dict[str, Any] = {
        "schema_version": EXECUTION_SCHEMA_VERSION,
        "namespace_sha256": adapter.namespace_sha256,
        "rollout_request": asdict(request),
        "rollout_attestation": dict(rollout),
        "adapter_certification": dict(certification),
        "adapter_certification_sha256": digests["adapter_certification_sha256"],
        "shadow_attestation": dict(shadow),
        "shadow_attestation_sha256": digests["shadow_attestation_sha256"],
        "recovery_gate_attestation": dict(gate),
        "recovery_gate_attestation_sha256": gate_sha256,
        "rollback_receipt": receipt,
        "rollback_receipt_sha256": digests["rollback_receipt_sha256"],
        "expected_state": _public_state(local_state),
        "rollout_report_sha256": digests["rollout_report_sha256"],
        "request_sha256": "",
    }
    payload["request_sha256"] = _digest(_canonical_json(
        {field: item for field, item in payload.items() if field != "request_sha256"}
    ))
    return _validate_recovery_execution(
        VerifiedProductionRecoveryExecution(
            request, rollout, certification, digests["adapter_certification_sha256"],
            shadow, digests["shadow_attestation_sha256"], gate, gate_sha256,
            receipt, digests["rollback_receipt_sha256"], local_state, payload,
        ),
        adapter,
    )


def apply_verified_production_recovery_execution(
    adapter: HTTPSProductionRecoveryDirectTrafficAdapter,
    sandbox: SQLiteDirectTrafficSandbox,
    release_history_path: Path,
    authorization: VerifiedProductionRecoveryExecution,
) -> dict[str, Any]:
    validated = _validate_recovery_execution(authorization, adapter)
    remote, reused_remote = adapter.execute(validated)
    applied_at = _parse_timestamp(remote.applied_at, "recovery applied_at")
    local = sandbox.apply(
        validated.request, release_history_path,
        str(validated.certification["certification_id"]),
        validated.certification_sha256, now=applied_at,
    )
    status = sandbox.status()
    if any((
        status["current_traffic_percent"] != remote.current_traffic_percent,
        status["revision"] != remote.revision,
        status["operations"] != remote.operations,
        status["history_chain_sha256"] != remote.history_chain_sha256,
    )):
        raise DirectTrafficPreproductionIndeterminateError(
            "remote and local recovery chains did not converge"
        )
    return {
        "schema_version": RECEIPT_SCHEMA_VERSION,
        "namespace_sha256": adapter.namespace_sha256,
        "operation_id": remote.operation_id,
        "decision_id": validated.request.decision_id,
        "outcome": remote.outcome,
        "current_traffic_percent": remote.current_traffic_percent,
        "revision": remote.revision,
        "history_chain_sha256": remote.history_chain_sha256,
        "adapter_certification_id": validated.certification["certification_id"],
        "shadow_attestation_id": validated.shadow_attestation["attestation_id"],
        "recovery_gate_attestation_id": validated.recovery_gate_attestation["attestation_id"],
        "remote_reused": reused_remote,
        "local_reused": bool(local["reused"]),
        "applied_at": remote.applied_at,
    }


def _validate_recovery_execution(
    value: VerifiedProductionRecoveryExecution,
    adapter: HTTPSProductionRecoveryDirectTrafficAdapter,
) -> VerifiedProductionRecoveryExecution:
    fields = {
        "schema_version", "namespace_sha256", "rollout_request", "rollout_attestation",
        "adapter_certification", "adapter_certification_sha256", "shadow_attestation",
        "shadow_attestation_sha256", "recovery_gate_attestation",
        "recovery_gate_attestation_sha256", "rollback_receipt",
        "rollback_receipt_sha256", "expected_state", "request_sha256",
        "rollout_report_sha256",
    }
    payload = value.payload
    if not isinstance(payload, Mapping) or set(payload) != fields:
        raise DirectTrafficPreproductionConfigError("recovery execution payload is invalid")
    expected = _digest(_canonical_json(
        {field: item for field, item in payload.items() if field != "request_sha256"}
    ))
    checks = (
        payload["schema_version"] == adapter.execution_schema_version,
        payload["namespace_sha256"] == adapter.namespace_sha256,
        payload["request_sha256"] == expected,
        payload["rollout_request"] == asdict(value.request),
        payload["rollout_attestation"] == dict(value.rollout_attestation),
        payload["adapter_certification"] == dict(value.certification),
        payload["adapter_certification_sha256"] == value.certification_sha256,
        payload["shadow_attestation"] == dict(value.shadow_attestation),
        payload["shadow_attestation_sha256"] == value.shadow_attestation_sha256,
        payload["recovery_gate_attestation"] == dict(value.recovery_gate_attestation),
        payload["recovery_gate_attestation_sha256"]
        == value.recovery_gate_attestation_sha256,
        payload["rollback_receipt"] == dict(value.rollback_receipt),
        payload["rollback_receipt_sha256"] == value.rollback_receipt_sha256,
        payload["rollout_report_sha256"]
        == value.recovery_gate_attestation.get("rollout_report_sha256"),
        value.request.attestation_id == value.rollout_attestation.get("attestation_id"),
        value.request.attestation_sha256 == _digest(_canonical_json(dict(value.rollout_attestation))),
        value.certification_sha256 == _digest(_canonical_json(dict(value.certification))),
        value.shadow_attestation_sha256 == _digest(_canonical_json(dict(value.shadow_attestation))),
        value.recovery_gate_attestation_sha256 == _digest(_canonical_json(dict(value.recovery_gate_attestation))),
        value.rollback_receipt_sha256 == _digest(_canonical_json(dict(value.rollback_receipt))),
        payload["expected_state"] == _public_state(value.local_state),
    )
    if not all(checks):
        raise DirectTrafficPreproductionConfigError(
            "recovery execution identity binding is invalid"
        )
    return value


def _validate_recovery_secrets(token: str, *keys: bytes) -> None:
    values = (token.encode("utf-8"), *keys)
    if len(set(values)) != len(values):
        raise DirectTrafficPreproductionConfigError(
            "recovery Provider and proof credentials must all be distinct"
        )


def main(argv: Sequence[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        description="Apply the post-rollback production 0%-to-5% recovery"
    )
    parser.add_argument("--sandbox-ledger", type=Path, required=True)
    parser.add_argument("--release-history-ledger", type=Path, required=True)
    parser.add_argument("--decision-dir", type=Path, required=True)
    parser.add_argument("--policy", type=Path, required=True)
    parser.add_argument("--adapter-certification", type=Path, required=True)
    parser.add_argument("--shadow-history-ledger", type=Path, required=True)
    parser.add_argument("--shadow-gate-dir", type=Path, required=True)
    parser.add_argument("--recovery-gate-dir", type=Path, required=True)
    parser.add_argument("--rollback-receipt", type=Path, required=True)
    parser.add_argument("--environment-id", required=True)
    parser.add_argument("--provider-instance", required=True)
    parser.add_argument("--namespace-id", required=True)
    parser.add_argument("--provider-base-url", required=True)
    parser.add_argument("--allowed-host", required=True)
    parser.add_argument("--timeout-seconds", type=float, default=5.0)
    parser.add_argument("--max-age-seconds", type=int, default=900)
    parser.add_argument("--canary-lease-root", type=Path, default=DEFAULT_CANARY_LEASE_ROOT)
    args = parser.parse_args(argv)
    lease = None
    try:
        token = os.environ.get(TOKEN_ENV, "")
        if not token:
            raise DirectTrafficPreproductionConfigError(f"{TOKEN_ENV} is required")
        environment_id = _validate_production_environment(args.environment_id)
        environment_digest = environment_sha256(environment_id)
        adapter = HTTPSProductionRecoveryDirectTrafficAdapter(
            HTTPSPreproductionConfig(
                args.provider_base_url, args.allowed_host, args.provider_instance,
                environment_digest, args.namespace_id, token, args.timeout_seconds,
            )
        )
        lease = try_acquire_canary_lease(args.canary_lease_root, environment_id)
        if lease is None:
            raise DirectTrafficPreproductionConfigError(
                "another environment operation holds the shared lease"
            )
        sandbox = SQLiteDirectTrafficSandbox(
            args.sandbox_ledger, environment_digest, args.provider_instance
        )
        history = ReleaseGateHistory(
            args.release_history_ledger, environment_id, create=False
        )
        policy, policy_sha256 = load_policy(args.policy)
        shadow_history = DirectTrafficShadowHistory(
            args.shadow_history_ledger, adapter.observer_instance_sha256,
            adapter.provider_instance_sha256, adapter.environment_sha256, create=False,
        )
        rollout_key, rollout_key_id = _key_from_environment(
            ROLLOUT_KEY_ENV, ROLLOUT_KEY_ID_ENV
        )
        certification_key, certification_key_id = certification_key_from_environment(
            profile="production_recovery"
        )
        shadow_key, shadow_key_id = _key_from_environment(SHADOW_KEY_ENV, SHADOW_KEY_ID_ENV)
        gate_key, gate_key_id = _key_from_environment(
            RECOVERY_GATE_KEY_ENV, RECOVERY_GATE_KEY_ID_ENV
        )
        authorization = verify_production_recovery_execution(
            adapter, sandbox, args.decision_dir, history, policy, policy_sha256,
            rollout_key, rollout_key_id, args.adapter_certification,
            certification_key, certification_key_id, args.shadow_gate_dir,
            shadow_history, shadow_key, shadow_key_id, args.recovery_gate_dir,
            gate_key, gate_key_id, _read_json(args.rollback_receipt),
            _read_json(args.decision_dir / ROLLOUT_REPORT_FILE),
            max_age_seconds=args.max_age_seconds,
        )
        receipt = apply_verified_production_recovery_execution(
            adapter, sandbox, history.path, authorization
        )
        print(json.dumps(receipt, sort_keys=True))
        return 0
    except (ValueError, RuntimeError, OSError, sqlite3.Error) as exc:
        print(f"direct traffic production recovery failed: {exc}", file=sys.stderr)
        return 1
    finally:
        if lease is not None:
            lease.release()


if __name__ == "__main__":
    raise SystemExit(main())
