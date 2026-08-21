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
    PRODUCTION_EXPANSION_100_KEY_ENV as ROLLOUT_KEY_ENV,
    PRODUCTION_EXPANSION_100_KEY_ID_ENV as ROLLOUT_KEY_ID_ENV,
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
    _key_from_environment,
    _public_state,
    _validate_result,
    _validate_state,
)
from .direct_traffic_production import TOKEN_ENV as PRODUCTION_TOKEN_ENV, _validate_production_environment
from .direct_traffic_production_emergency import TOKEN_ENV as EMERGENCY_TOKEN_ENV
from .direct_traffic_production_expansion import TOKEN_ENV as EXPANSION_10_TOKEN_ENV
from .direct_traffic_production_expansion_50 import (
    HTTPSProductionExpansion50DirectTrafficAdapter,
    TOKEN_ENV as EXPANSION_50_TOKEN_ENV,
)
from .direct_traffic_production_expansion_100_gate import (
    KEY_ENV as EXPANSION_100_GATE_KEY_ENV,
    KEY_ID_ENV as EXPANSION_100_GATE_KEY_ID_ENV,
    _read_gate,
    _read_json,
    validate_prior_expansion_receipt,
    verify_expansion_100_gate_attestation,
)
from .direct_traffic_production_recovery import TOKEN_ENV as RECOVERY_TOKEN_ENV
from .direct_traffic_shadow import DirectTrafficShadowHistory, ReadOnlyDirectTrafficProviderConfig
from .direct_traffic_shadow_gate import (
    PRODUCTION_EXPANSION_100_KEY_ENV as SHADOW_KEY_ENV,
    PRODUCTION_EXPANSION_100_KEY_ID_ENV as SHADOW_KEY_ID_ENV,
    verify_direct_traffic_shadow_attestation,
)
from .release_gate import DEFAULT_CANARY_LEASE_ROOT, try_acquire_canary_lease
from .release_gate_history import ReleaseGateHistory, environment_sha256


ADAPTER_NAME = "https-direct-traffic-production-expansion-100"
IMPLEMENTATION_VERSION = "1.0.0"
EXECUTION_SCHEMA_VERSION = "agent-direct-traffic-production-expansion-100-execution-v1"
STATE_SCHEMA_VERSION = "agent-direct-traffic-production-expansion-100-state-v1"
RESULT_SCHEMA_VERSION = "agent-direct-traffic-production-expansion-100-result-v1"
RECEIPT_SCHEMA_VERSION = "agent-direct-traffic-production-expansion-100-receipt-v1"
TOKEN_ENV = "OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_100_TOKEN"


@dataclass(frozen=True)
class VerifiedProductionExpansion100Execution:
    request: TrafficChangeRequest
    rollout_attestation: Mapping[str, Any]
    certification: Mapping[str, Any]
    certification_sha256: str
    shadow_attestation: Mapping[str, Any]
    shadow_attestation_sha256: str
    expansion_100_gate_attestation: Mapping[str, Any]
    expansion_100_gate_attestation_sha256: str
    prior_expansion_receipt: Mapping[str, Any]
    prior_expansion_receipt_sha256: str
    local_state: Mapping[str, Any]
    payload: Mapping[str, Any]


class HTTPSProductionExpansion100DirectTrafficAdapter(
    HTTPSProductionExpansion50DirectTrafficAdapter
):
    name = ADAPTER_NAME
    implementation_version = IMPLEMENTATION_VERSION
    execution_schema_version = EXECUTION_SCHEMA_VERSION
    execution_extra_fields: frozenset[str] = frozenset()

    def __init__(self, config: HTTPSPreproductionConfig, **kwargs: Any) -> None:
        prior_tokens = {
            value
            for name in (
                PRODUCTION_TOKEN_ENV,
                EMERGENCY_TOKEN_ENV,
                RECOVERY_TOKEN_ENV,
                EXPANSION_10_TOKEN_ENV,
                EXPANSION_50_TOKEN_ENV,
            )
            if (value := os.environ.get(name)) is not None
        }
        if config.token in prior_tokens:
            raise DirectTrafficPreproductionConfigError(
                "expansion token must be distinct from prior production tokens"
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
        if not isinstance(authorization, VerifiedProductionExpansion100Execution):
            raise DirectTrafficPreproductionConfigError(
                "invalid production expansion authorization"
            )
        return _validate_expansion_100_execution(authorization, self)


def verify_production_expansion_100_execution(
    adapter: HTTPSProductionExpansion100DirectTrafficAdapter,
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
    expansion_100_gate_dir: Path,
    expansion_100_gate_key: bytes,
    expansion_100_gate_key_id: str,
    prior_expansion_receipt: Mapping[str, Any],
    rollout_report: Mapping[str, Any],
    shadow_gate_report: Mapping[str, Any],
    *,
    max_age_seconds: int = 900,
    now: datetime | None = None,
) -> VerifiedProductionExpansion100Execution:
    current_time = _normalize_now(now)
    _validate_expansion_100_secrets(
        adapter.config.token,
        rollout_key,
        certification_key,
        shadow_key,
        expansion_100_gate_key,
    )
    if any(
        (
            sandbox.provider_instance_sha256 != adapter.provider_instance_sha256,
            sandbox.environment_sha256 != adapter.environment_sha256,
            shadow_history.observer_instance_sha256 != adapter.observer_instance_sha256,
            shadow_history.provider_instance_sha256 != adapter.provider_instance_sha256,
            shadow_history.environment_sha256 != adapter.environment_sha256,
        )
    ):
        raise DirectTrafficPreproductionConfigError(
            "expansion state or shadow evidence is not bound to the adapter"
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
        require_decision="expand",
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
    shadow = verify_direct_traffic_shadow_attestation(
        shadow_gate_dir,
        shadow_history,
        shadow_key,
        shadow_key_id,
        max_age_seconds=max_age_seconds,
        require_decision="pass",
        now=current_time,
    )
    gate = verify_expansion_100_gate_attestation(
        expansion_100_gate_dir,
        expansion_100_gate_key,
        expansion_100_gate_key_id,
        max_age_seconds=max_age_seconds,
        now=current_time,
    )
    gate_report = _read_gate(expansion_100_gate_dir, expect_attestation=True)
    receipt = validate_prior_expansion_receipt(prior_expansion_receipt)
    local_state = sandbox.status()
    if any(
        (
            request.decision != "expand",
            request.expected_traffic_percent != 50,
            request.target_traffic_percent != 100,
            request.expected_revision != 6,
            local_state["current_traffic_percent"] != 50,
            local_state["revision"] != 6,
            local_state["operations"] != 6,
            receipt["history_chain_sha256"] != local_state["history_chain_sha256"],
            receipt["namespace_sha256"] != adapter.namespace_sha256,
        )
    ):
        raise DirectTrafficPreproductionConfigError(
            "expansion permits only the proven 50%-to-100% transition"
        )
    digests = {
        "rollout_attestation_sha256": _digest(_canonical_json(rollout)),
        "rollout_report_sha256": _digest(_canonical_json(dict(rollout_report))),
        "adapter_certification_sha256": _digest(_canonical_json(certification)),
        "shadow_attestation_sha256": _digest(_canonical_json(shadow)),
        "shadow_gate_report_sha256": _digest(
            _canonical_json(dict(shadow_gate_report))
        ),
        "prior_expansion_receipt_sha256": _digest(_canonical_json(receipt)),
    }
    if gate_report["source"] != digests:
        raise DirectTrafficPreproductionConfigError(
            "expansion Gate is not bound to the execution proofs"
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
        "shadow_gate_report_sha256": digests["shadow_gate_report_sha256"],
        "expansion_100_gate_attestation": dict(gate),
        "expansion_100_gate_attestation_sha256": gate_sha256,
        "prior_expansion_receipt": receipt,
        "prior_expansion_receipt_sha256": digests["prior_expansion_receipt_sha256"],
        "expected_state": _public_state(local_state),
        "rollout_report_sha256": digests["rollout_report_sha256"],
        "request_sha256": "",
    }
    payload["request_sha256"] = _digest(
        _canonical_json(
            {field: item for field, item in payload.items() if field != "request_sha256"}
        )
    )
    return _validate_expansion_100_execution(
        VerifiedProductionExpansion100Execution(
            request,
            rollout,
            certification,
            digests["adapter_certification_sha256"],
            shadow,
            digests["shadow_attestation_sha256"],
            gate,
            gate_sha256,
            receipt,
            digests["prior_expansion_receipt_sha256"],
            local_state,
            payload,
        ),
        adapter,
    )


def apply_verified_production_expansion_100_execution(
    adapter: HTTPSProductionExpansion100DirectTrafficAdapter,
    sandbox: SQLiteDirectTrafficSandbox,
    release_history_path: Path,
    authorization: VerifiedProductionExpansion100Execution,
) -> dict[str, Any]:
    validated = _validate_expansion_100_execution(authorization, adapter)
    remote, reused_remote = adapter.execute(validated)
    applied_at = _parse_timestamp(remote.applied_at, "expansion applied_at")
    local = sandbox.apply(
        validated.request,
        release_history_path,
        str(validated.certification["certification_id"]),
        validated.certification_sha256,
        now=applied_at,
    )
    status = sandbox.status()
    if any(
        (
            status["current_traffic_percent"] != remote.current_traffic_percent,
            status["revision"] != remote.revision,
            status["operations"] != remote.operations,
            status["history_chain_sha256"] != remote.history_chain_sha256,
        )
    ):
        raise DirectTrafficPreproductionIndeterminateError(
            "remote and local expansion chains did not converge"
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
        "expansion_100_gate_attestation_id": (
            validated.expansion_100_gate_attestation["attestation_id"]
        ),
        "prior_expansion_operation_id": validated.prior_expansion_receipt["operation_id"],
        "remote_reused": reused_remote,
        "local_reused": bool(local["reused"]),
        "applied_at": remote.applied_at,
    }


def _validate_expansion_100_execution(
    value: VerifiedProductionExpansion100Execution,
    adapter: HTTPSProductionExpansion100DirectTrafficAdapter,
) -> VerifiedProductionExpansion100Execution:
    fields = {
        "schema_version",
        "namespace_sha256",
        "rollout_request",
        "rollout_attestation",
        "adapter_certification",
        "adapter_certification_sha256",
        "shadow_attestation",
        "shadow_attestation_sha256",
        "expansion_100_gate_attestation",
        "expansion_100_gate_attestation_sha256",
        "prior_expansion_receipt",
        "prior_expansion_receipt_sha256",
        "expected_state",
        "request_sha256",
        "rollout_report_sha256",
        "shadow_gate_report_sha256",
    }
    payload = value.payload
    if not isinstance(payload, Mapping) or set(payload) != fields:
        raise DirectTrafficPreproductionConfigError("expansion execution payload is invalid")
    expected = _digest(
        _canonical_json(
            {field: item for field, item in payload.items() if field != "request_sha256"}
        )
    )
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
        payload["expansion_100_gate_attestation"]
        == dict(value.expansion_100_gate_attestation),
        payload["expansion_100_gate_attestation_sha256"]
        == value.expansion_100_gate_attestation_sha256,
        payload["prior_expansion_receipt"] == dict(value.prior_expansion_receipt),
        payload["prior_expansion_receipt_sha256"]
        == value.prior_expansion_receipt_sha256,
        payload["rollout_report_sha256"]
        == value.expansion_100_gate_attestation.get("rollout_report_sha256"),
        payload["shadow_gate_report_sha256"]
        == value.expansion_100_gate_attestation.get("shadow_gate_report_sha256"),
        value.request.attestation_id == value.rollout_attestation.get("attestation_id"),
        value.request.attestation_sha256
        == _digest(_canonical_json(dict(value.rollout_attestation))),
        value.certification_sha256 == _digest(_canonical_json(dict(value.certification))),
        value.shadow_attestation_sha256
        == _digest(_canonical_json(dict(value.shadow_attestation))),
        value.expansion_100_gate_attestation_sha256
        == _digest(_canonical_json(dict(value.expansion_100_gate_attestation))),
        value.prior_expansion_receipt_sha256
        == _digest(_canonical_json(dict(value.prior_expansion_receipt))),
        payload["expected_state"] == _public_state(value.local_state),
    )
    if not all(checks):
        raise DirectTrafficPreproductionConfigError(
            "expansion execution identity binding is invalid"
        )
    return value


def _validate_expansion_100_secrets(token: str, *keys: bytes) -> None:
    values = (token.encode("utf-8"), *keys)
    if len(set(values)) != len(values):
        raise DirectTrafficPreproductionConfigError(
            "expansion Provider and proof credentials must all be distinct"
        )


def main(argv: Sequence[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Apply the production 50%-to-100% expansion")
    parser.add_argument("--sandbox-ledger", type=Path, required=True)
    parser.add_argument("--release-history-ledger", type=Path, required=True)
    parser.add_argument("--decision-dir", type=Path, required=True)
    parser.add_argument("--policy", type=Path, required=True)
    parser.add_argument("--adapter-certification", type=Path, required=True)
    parser.add_argument("--shadow-history-ledger", type=Path, required=True)
    parser.add_argument("--shadow-gate-dir", type=Path, required=True)
    parser.add_argument("--expansion-gate-dir", type=Path, required=True)
    parser.add_argument("--prior-expansion-receipt", type=Path, required=True)
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
        adapter = HTTPSProductionExpansion100DirectTrafficAdapter(
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
        shadow_history = DirectTrafficShadowHistory(
            args.shadow_history_ledger,
            adapter.observer_instance_sha256,
            adapter.provider_instance_sha256,
            adapter.environment_sha256,
            create=False,
        )
        rollout_key, rollout_key_id = _key_from_environment(
            ROLLOUT_KEY_ENV, ROLLOUT_KEY_ID_ENV
        )
        certification_key, certification_key_id = certification_key_from_environment(
            profile="production_expansion_100"
        )
        shadow_key, shadow_key_id = _key_from_environment(
            SHADOW_KEY_ENV, SHADOW_KEY_ID_ENV
        )
        gate_key, gate_key_id = _key_from_environment(
            EXPANSION_100_GATE_KEY_ENV, EXPANSION_100_GATE_KEY_ID_ENV
        )
        authorization = verify_production_expansion_100_execution(
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
            args.shadow_gate_dir,
            shadow_history,
            shadow_key,
            shadow_key_id,
            args.expansion_gate_dir,
            gate_key,
            gate_key_id,
            _read_json(args.prior_expansion_receipt),
            _read_json(args.decision_dir / ROLLOUT_REPORT_FILE),
            _read_json(args.shadow_gate_dir / "direct-traffic-shadow-gate-report.json"),
            max_age_seconds=args.max_age_seconds,
        )
        receipt = apply_verified_production_expansion_100_execution(
            adapter, sandbox, history.path, authorization
        )
        print(json.dumps(receipt, sort_keys=True))
        return 0
    except (ValueError, RuntimeError, OSError, sqlite3.Error) as exc:
        print(f"direct traffic production expansion failed: {exc}", file=sys.stderr)
        return 1
    finally:
        if lease is not None:
            lease.release()


if __name__ == "__main__":
    raise SystemExit(main())
