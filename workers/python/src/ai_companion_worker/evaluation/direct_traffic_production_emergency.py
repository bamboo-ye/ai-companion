from __future__ import annotations

import argparse
import json
import os
import sqlite3
import sys
from dataclasses import asdict, dataclass
from datetime import datetime
from pathlib import Path
from typing import Any, Mapping, Sequence

from .deployment_controller import _canonical_json, _digest, _normalize_now, _parse_timestamp
from .direct_rollout import (
    PRODUCTION_EMERGENCY_KEY_ENV as ROLLOUT_KEY_ENV,
    PRODUCTION_EMERGENCY_KEY_ID_ENV as ROLLOUT_KEY_ID_ENV,
    load_policy,
    verify_attestation as verify_rollout_attestation,
)
from .direct_rollout_traffic import (
    SQLiteDirectTrafficSandbox,
    TrafficChangeRequest,
    build_traffic_request,
)
from .direct_traffic_certification import (
    DirectTrafficCertificationError,
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
    PreproductionProviderError,
    _key_from_environment,
    _public_state,
    _validate_result,
    _validate_state,
)
from .direct_traffic_production import (
    TOKEN_ENV as PRODUCTION_TOKEN_ENV,
    HTTPSProductionDirectTrafficAdapter,
    _validate_production_environment,
)
from .direct_traffic_production_emergency_gate import (
    KEY_ENV as EMERGENCY_GATE_KEY_ENV,
    KEY_ID_ENV as EMERGENCY_GATE_KEY_ID_ENV,
    DirectTrafficProductionEmergencyGateError,
    _read_gate,
    _read_json,
    _validate_activation_receipt,
    verify_emergency_gate_attestation,
)
from .release_gate import DEFAULT_CANARY_LEASE_ROOT, try_acquire_canary_lease
from .release_gate_history import ReleaseGateHistory, environment_sha256


ADAPTER_NAME = "https-direct-traffic-production-emergency"
IMPLEMENTATION_VERSION = "1.0.0"
EXECUTION_SCHEMA_VERSION = "agent-direct-traffic-production-emergency-execution-v1"
STATE_SCHEMA_VERSION = "agent-direct-traffic-production-emergency-state-v1"
RESULT_SCHEMA_VERSION = "agent-direct-traffic-production-emergency-result-v1"
RECEIPT_SCHEMA_VERSION = "agent-direct-traffic-production-emergency-receipt-v1"
TOKEN_ENV = "OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EMERGENCY_TOKEN"


@dataclass(frozen=True)
class VerifiedProductionEmergencyExecution:
    request: TrafficChangeRequest
    rollout_attestation: Mapping[str, Any]
    certification: Mapping[str, Any]
    certification_sha256: str
    emergency_gate_attestation: Mapping[str, Any]
    emergency_gate_attestation_sha256: str
    activation_receipt: Mapping[str, Any]
    activation_receipt_sha256: str
    local_state: Mapping[str, Any]
    payload: Mapping[str, Any]


class HTTPSProductionEmergencyDirectTrafficAdapter(
    HTTPSProductionDirectTrafficAdapter
):
    """Production writer that can only perform an independently authorized 5%-to-0% stop."""

    name = ADAPTER_NAME
    implementation_version = IMPLEMENTATION_VERSION
    execution_schema_version = EXECUTION_SCHEMA_VERSION
    execution_extra_fields: frozenset[str] = frozenset()

    def __init__(self, config: HTTPSPreproductionConfig, **kwargs: Any) -> None:
        if config.token == os.environ.get(PRODUCTION_TOKEN_ENV, "__absent__"):
            raise DirectTrafficPreproductionConfigError(
                "emergency token must be distinct from the normal production token"
            )
        super().__init__(config, **kwargs)

    def _validate_provider_state(self, value: Any) -> PreproductionState:
        return _validate_state(value, schema_version=STATE_SCHEMA_VERSION)

    def _validate_provider_result(self, value: Any) -> PreproductionResult:
        return _validate_result(value, schema_version=RESULT_SCHEMA_VERSION)

    def _validate_authorization(
        self, authorization: DirectTrafficExecutionAuthorization
    ) -> DirectTrafficExecutionAuthorization:
        if not isinstance(authorization, VerifiedProductionEmergencyExecution):
            raise DirectTrafficPreproductionConfigError(
                "invalid emergency production authorization"
            )
        return _validate_emergency_execution(authorization, self)


def verify_production_emergency_execution(
    adapter: HTTPSProductionEmergencyDirectTrafficAdapter,
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
    emergency_gate_dir: Path,
    emergency_gate_key: bytes,
    emergency_gate_key_id: str,
    activation_receipt: Mapping[str, Any],
    *,
    max_age_seconds: int = 300,
    now: datetime | None = None,
) -> VerifiedProductionEmergencyExecution:
    current_time = _normalize_now(now)
    _validate_emergency_secrets(
        adapter.config.token,
        rollout_key,
        certification_key,
        emergency_gate_key,
    )
    if (
        sandbox.provider_instance_sha256 != adapter.provider_instance_sha256
        or sandbox.environment_sha256 != adapter.environment_sha256
    ):
        raise DirectTrafficPreproductionConfigError(
            "local production state is not bound to the emergency adapter"
        )
    request = build_traffic_request(
        sandbox,
        decision_dir,
        release_history,
        policy,
        policy_sha256,
        rollout_key,
        rollout_key_id,
        max_age_seconds=max_age_seconds,
        now=current_time,
    )
    rollout = verify_rollout_attestation(
        decision_dir,
        release_history,
        policy,
        policy_sha256,
        rollout_key,
        rollout_key_id,
        max_age_seconds=max_age_seconds,
        require_decision="any",
        now=current_time,
    )
    certification = verify_certification(
        certification_path,
        adapter,
        certification_key,
        certification_key_id,
        require_certification_namespace=False,
        now=current_time,
    )
    validate_execution_binding(certification, adapter)
    gate = verify_emergency_gate_attestation(
        emergency_gate_dir,
        emergency_gate_key,
        emergency_gate_key_id,
        max_age_seconds=max_age_seconds,
        now=current_time,
    )
    gate_report = _read_gate(emergency_gate_dir, expect_attestation=True)
    receipt = _validate_activation_receipt(activation_receipt)
    local_state = sandbox.status()
    if (
        request.decision not in {"rollback", "disable"}
        or request.expected_traffic_percent != 5
        or request.target_traffic_percent != 0
        or request.expected_revision != 1
        or local_state["current_traffic_percent"] != 5
        or local_state["revision"] != 1
        or local_state["operations"] != 1
    ):
        raise DirectTrafficPreproductionConfigError(
            "emergency adapter permits only the initial production 5%-to-0% stop"
        )
    if (
        receipt["namespace_sha256"] != adapter.namespace_sha256
        or receipt["current_traffic_percent"] != 5
        or receipt["revision"] != 1
        or receipt["history_chain_sha256"] != local_state["history_chain_sha256"]
    ):
        raise DirectTrafficPreproductionConfigError(
            "activation receipt does not match current production state"
        )
    rollout_sha256 = _digest(_canonical_json(rollout))
    certification_sha256 = _digest(_canonical_json(certification))
    gate_sha256 = _digest(_canonical_json(gate))
    receipt_sha256 = _digest(_canonical_json(receipt))
    expected_gate_sources = {
        "rollout_attestation_sha256": rollout_sha256,
        "adapter_certification_sha256": certification_sha256,
        "activation_receipt_sha256": receipt_sha256,
    }
    if (
        gate_report["source"] != expected_gate_sources
        or gate["rollout_decision"] != request.decision
        or gate["incident_id"] != gate_report["incident_id"]
    ):
        raise DirectTrafficPreproductionConfigError(
            "emergency Gate is not bound to the rollback proofs"
        )
    payload: dict[str, Any] = {
        "schema_version": EXECUTION_SCHEMA_VERSION,
        "namespace_sha256": adapter.namespace_sha256,
        "rollout_request": asdict(request),
        "rollout_attestation": dict(rollout),
        "adapter_certification": dict(certification),
        "adapter_certification_sha256": certification_sha256,
        "emergency_gate_attestation": dict(gate),
        "emergency_gate_attestation_sha256": gate_sha256,
        "activation_receipt": receipt,
        "activation_receipt_sha256": receipt_sha256,
        "expected_state": _public_state(local_state),
        "request_sha256": "",
    }
    payload["request_sha256"] = _digest(
        _canonical_json(
            {field: value for field, value in payload.items() if field != "request_sha256"}
        )
    )
    return _validate_emergency_execution(
        VerifiedProductionEmergencyExecution(
            request=request,
            rollout_attestation=rollout,
            certification=certification,
            certification_sha256=certification_sha256,
            emergency_gate_attestation=gate,
            emergency_gate_attestation_sha256=gate_sha256,
            activation_receipt=receipt,
            activation_receipt_sha256=receipt_sha256,
            local_state=local_state,
            payload=payload,
        ),
        adapter,
    )


def apply_verified_production_emergency_execution(
    adapter: HTTPSProductionEmergencyDirectTrafficAdapter,
    sandbox: SQLiteDirectTrafficSandbox,
    release_history_path: Path,
    authorization: VerifiedProductionEmergencyExecution,
) -> dict[str, Any]:
    validated = _validate_emergency_execution(authorization, adapter)
    remote, reused_remote = adapter.execute(validated)
    applied_at = _parse_timestamp(remote.applied_at, "emergency production applied_at")
    local = sandbox.apply(
        validated.request,
        release_history_path,
        str(validated.certification["certification_id"]),
        validated.certification_sha256,
        now=applied_at,
    )
    status = sandbox.status()
    if (
        status["current_traffic_percent"] != remote.current_traffic_percent
        or status["revision"] != remote.revision
        or status["operations"] != remote.operations
        or status["history_chain_sha256"] != remote.history_chain_sha256
    ):
        raise DirectTrafficPreproductionIndeterminateError(
            "remote and local emergency production chains did not converge"
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
        "emergency_gate_attestation_id": validated.emergency_gate_attestation[
            "attestation_id"
        ],
        "incident_id": validated.emergency_gate_attestation["incident_id"],
        "remote_reused": reused_remote,
        "local_reused": bool(local["reused"]),
        "applied_at": remote.applied_at,
    }


def _validate_emergency_execution(
    value: VerifiedProductionEmergencyExecution,
    adapter: HTTPSProductionEmergencyDirectTrafficAdapter,
) -> VerifiedProductionEmergencyExecution:
    fields = {
        "schema_version",
        "namespace_sha256",
        "rollout_request",
        "rollout_attestation",
        "adapter_certification",
        "adapter_certification_sha256",
        "emergency_gate_attestation",
        "emergency_gate_attestation_sha256",
        "activation_receipt",
        "activation_receipt_sha256",
        "expected_state",
        "request_sha256",
    }
    payload = value.payload
    if not isinstance(payload, Mapping) or set(payload) != fields:
        raise DirectTrafficPreproductionConfigError(
            "emergency production execution payload is invalid"
        )
    expected_sha256 = _digest(
        _canonical_json(
            {field: item for field, item in payload.items() if field != "request_sha256"}
        )
    )
    bindings = (
        payload["schema_version"] == adapter.execution_schema_version,
        payload["namespace_sha256"] == adapter.namespace_sha256,
        payload["request_sha256"] == expected_sha256,
        payload["rollout_request"] == asdict(value.request),
        payload["rollout_attestation"] == dict(value.rollout_attestation),
        payload["adapter_certification"] == dict(value.certification),
        payload["adapter_certification_sha256"] == value.certification_sha256,
        payload["emergency_gate_attestation"]
        == dict(value.emergency_gate_attestation),
        payload["emergency_gate_attestation_sha256"]
        == value.emergency_gate_attestation_sha256,
        payload["activation_receipt"] == dict(value.activation_receipt),
        payload["activation_receipt_sha256"] == value.activation_receipt_sha256,
        payload["expected_state"] == _public_state(value.local_state),
        value.request.attestation_id
        == value.rollout_attestation.get("attestation_id"),
        value.request.attestation_sha256
        == _digest(_canonical_json(dict(value.rollout_attestation))),
        value.certification_sha256 == _digest(_canonical_json(dict(value.certification))),
        value.emergency_gate_attestation_sha256
        == _digest(_canonical_json(dict(value.emergency_gate_attestation))),
        value.activation_receipt_sha256
        == _digest(_canonical_json(dict(value.activation_receipt))),
    )
    if not all(bindings):
        raise DirectTrafficPreproductionConfigError(
            "emergency production execution identity binding is invalid"
        )
    return value


def _validate_emergency_secrets(
    token: str,
    rollout_key: bytes,
    certification_key: bytes,
    emergency_gate_key: bytes,
) -> None:
    values = (token.encode("utf-8"), rollout_key, certification_key, emergency_gate_key)
    if len(set(values)) != len(values):
        raise DirectTrafficPreproductionConfigError(
            "emergency Provider and proof credentials must all be distinct"
        )
    normal_names = (
        PRODUCTION_TOKEN_ENV,
        "OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_ROLLOUT_KEY",
        "OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_CERTIFICATION_KEY",
        "OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_SHADOW_GATE_KEY",
        "OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_GATE_KEY",
    )
    normal_values = {
        raw.encode("utf-8")
        for name in normal_names
        if (raw := os.environ.get(name)) is not None
    }
    if any(value in normal_values for value in values):
        raise DirectTrafficPreproductionConfigError(
            "emergency credentials must be distinct from normal production credentials"
        )


def main(argv: Sequence[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        description="Apply an independently authorized production 5%-to-0% emergency stop"
    )
    parser.add_argument("--sandbox-ledger", type=Path, required=True)
    parser.add_argument("--release-history-ledger", type=Path, required=True)
    parser.add_argument("--decision-dir", type=Path, required=True)
    parser.add_argument("--policy", type=Path, required=True)
    parser.add_argument("--adapter-certification", type=Path, required=True)
    parser.add_argument("--emergency-gate-dir", type=Path, required=True)
    parser.add_argument("--activation-receipt", type=Path, required=True)
    parser.add_argument("--environment-id", required=True)
    parser.add_argument("--provider-instance", required=True)
    parser.add_argument("--namespace-id", required=True)
    parser.add_argument("--provider-base-url", required=True)
    parser.add_argument("--allowed-host", required=True)
    parser.add_argument("--timeout-seconds", type=float, default=5.0)
    parser.add_argument("--max-age-seconds", type=int, default=300)
    parser.add_argument("--canary-lease-root", type=Path, default=DEFAULT_CANARY_LEASE_ROOT)
    args = parser.parse_args(argv)
    lease = None
    try:
        token = os.environ.get(TOKEN_ENV, "")
        if not token:
            raise DirectTrafficPreproductionConfigError(f"{TOKEN_ENV} is required")
        environment_id = _validate_production_environment(args.environment_id)
        environment_digest = environment_sha256(environment_id)
        adapter = HTTPSProductionEmergencyDirectTrafficAdapter(
            HTTPSPreproductionConfig(
                args.provider_base_url,
                args.allowed_host,
                args.provider_instance,
                environment_digest,
                args.namespace_id,
                token,
                args.timeout_seconds,
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
        rollout_key, rollout_key_id = _key_from_environment(
            ROLLOUT_KEY_ENV, ROLLOUT_KEY_ID_ENV
        )
        certification_key, certification_key_id = certification_key_from_environment(
            profile="production_emergency"
        )
        emergency_gate_key, emergency_gate_key_id = _key_from_environment(
            EMERGENCY_GATE_KEY_ENV, EMERGENCY_GATE_KEY_ID_ENV
        )
        authorization = verify_production_emergency_execution(
            adapter,
            sandbox,
            args.decision_dir,
            history,
            policy,
            policy_sha256,
            rollout_key,
            rollout_key_id,
            args.adapter_certification,
            certification_key,
            certification_key_id,
            args.emergency_gate_dir,
            emergency_gate_key,
            emergency_gate_key_id,
            _read_json(args.activation_receipt),
            max_age_seconds=args.max_age_seconds,
        )
        receipt = apply_verified_production_emergency_execution(
            adapter, sandbox, history.path, authorization
        )
        print(json.dumps(receipt, sort_keys=True))
        return 0
    except (
        DirectTrafficPreproductionConfigError,
        DirectTrafficProductionEmergencyGateError,
        DirectTrafficCertificationError,
        PreproductionProviderError,
        OSError,
        sqlite3.Error,
    ) as exc:
        print(f"direct traffic production emergency stop failed: {exc}", file=sys.stderr)
        return 1
    finally:
        if lease is not None:
            lease.release()


if __name__ == "__main__":
    raise SystemExit(main())
