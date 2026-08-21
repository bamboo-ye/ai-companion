from __future__ import annotations

import argparse
import json
import os
import re
import sqlite3
import sys
import urllib.parse
from dataclasses import dataclass
from datetime import datetime
from pathlib import Path
from typing import Any, Mapping, Sequence

from .deployment_controller import _canonical_json, _digest
from .direct_rollout import (
    PRODUCTION_KEY_ENV as ROLLOUT_KEY_ENV,
    PRODUCTION_KEY_ID_ENV as ROLLOUT_KEY_ID_ENV,
    load_policy,
)
from .direct_rollout_traffic import SQLiteDirectTrafficSandbox
from .direct_traffic_certification import certification_key_from_environment
from .direct_traffic_preproduction import (
    MAX_READ_ATTEMPTS,
    TOKEN_ENV as PREPRODUCTION_TOKEN_ENV,
    DirectTrafficPreproductionConfigError,
    DirectTrafficPreproductionConflictError,
    DirectTrafficPreproductionError,
    DirectTrafficExecutionAuthorization,
    DirectTrafficPreproductionIndeterminateError,
    HTTPSPreproductionConfig,
    HTTPSPreproductionDirectTrafficAdapter,
    PreproductionProviderError,
    PreproductionResult,
    PreproductionState,
    VerifiedPreproductionExecution,
    _key_from_environment,
    _validate_execution,
    _validate_result,
    _validate_state,
    apply_verified_preproduction_execution,
    verify_preproduction_execution,
)
from .direct_traffic_production_gate import (
    KEY_ENV as PRODUCTION_GATE_KEY_ENV,
    KEY_ID_ENV as PRODUCTION_GATE_KEY_ID_ENV,
    DirectTrafficProductionGateError,
    DirectTrafficProductionGateConfigError,
    production_namespace_sha256 as _production_namespace_sha256,
    verify_production_gate_attestation,
    _read_gate,
)
from .direct_traffic_shadow import (
    DirectTrafficShadowHistory,
    ReadOnlyDirectTrafficProviderConfig,
    DirectTrafficShadowError,
    observer_instance_sha256,
)
from .direct_traffic_shadow_gate import (
    PRODUCTION_KEY_ENV as SHADOW_KEY_ENV,
    PRODUCTION_KEY_ID_ENV as SHADOW_KEY_ID_ENV,
)
from .release_gate import DEFAULT_CANARY_LEASE_ROOT, try_acquire_canary_lease
from .release_gate_history import ReleaseGateHistory, environment_sha256


ADAPTER_NAME = "https-direct-traffic-production"
IMPLEMENTATION_VERSION = "1.0.0"
EXECUTION_SCHEMA_VERSION = "agent-direct-traffic-production-execution-v1"
STATE_SCHEMA_VERSION = "agent-direct-traffic-production-state-v1"
RESULT_SCHEMA_VERSION = "agent-direct-traffic-production-result-v1"
RECEIPT_SCHEMA_VERSION = "agent-direct-traffic-production-receipt-v1"
TOKEN_ENV = "OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_TOKEN"
_NAMESPACE_RE = re.compile(r"production-[a-z0-9][a-z0-9-]{0,62}")
_ENVIRONMENT_RE = re.compile(r"production-[a-z0-9][a-z0-9-]{0,62}")


@dataclass(frozen=True)
class VerifiedProductionExecution:
    execution: VerifiedPreproductionExecution
    production_gate_attestation: Mapping[str, Any]
    production_gate_attestation_sha256: str


class HTTPSProductionDirectTrafficAdapter(HTTPSPreproductionDirectTrafficAdapter):
    name = ADAPTER_NAME
    implementation_version = IMPLEMENTATION_VERSION
    execution_schema_version = EXECUTION_SCHEMA_VERSION
    execution_extra_fields = frozenset(
        {"production_gate_attestation", "production_gate_attestation_sha256"}
    )

    def __init__(self, config: HTTPSPreproductionConfig, **kwargs: Any) -> None:
        if config.token == os.environ.get(PREPRODUCTION_TOKEN_ENV, "__absent__"):
            raise DirectTrafficPreproductionConfigError(
                "production token must be distinct from preproduction token"
            )
        super().__init__(config, **kwargs)

    def _validate_adapter_config(
        self, config: HTTPSPreproductionConfig
    ) -> HTTPSPreproductionConfig:
        _validate_production_namespace(config.namespace_id)
        try:
            parsed = urllib.parse.urlsplit(config.base_url)
            port = parsed.port
        except ValueError as exc:
            raise DirectTrafficPreproductionConfigError(
                "production base URL is invalid"
            ) from exc
        if port not in {None, 443}:
            raise DirectTrafficPreproductionConfigError(
                "production endpoint requires HTTPS port 443"
            )
        try:
            observer_instance_sha256(self._observer_config(config))
        except DirectTrafficShadowError as exc:
            raise DirectTrafficPreproductionConfigError(
                "production observer endpoint is invalid"
            ) from exc
        if not 1 <= config.max_read_attempts <= MAX_READ_ATTEMPTS:
            raise DirectTrafficPreproductionConfigError("max read attempts is invalid")
        return config

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

    def _namespace_sha256(self, namespace_id: str) -> str:
        return production_namespace_sha256(namespace_id)

    def _validate_provider_state(self, value: Any) -> PreproductionState:
        return _validate_state(value, schema_version=STATE_SCHEMA_VERSION)

    def _validate_provider_result(self, value: Any) -> PreproductionResult:
        return _validate_result(value, schema_version=RESULT_SCHEMA_VERSION)

    def _validate_authorization(
        self, authorization: DirectTrafficExecutionAuthorization
    ) -> DirectTrafficExecutionAuthorization:
        if not isinstance(authorization, VerifiedPreproductionExecution):
            raise DirectTrafficPreproductionConfigError(
                "invalid production execution authorization"
            )
        validated = _validate_execution(authorization, self)
        gate = validated.payload.get("production_gate_attestation")
        gate_sha256 = validated.payload.get("production_gate_attestation_sha256")
        if (
            not isinstance(gate, Mapping)
            or gate_sha256 != _digest(_canonical_json(dict(gate)))
            or gate.get("decision") != "pass"
            or gate.get("namespace_sha256") != self.namespace_sha256
            or gate.get("environment_sha256") != self.environment_sha256
            or gate.get("provider_instance_sha256") != self.provider_instance_sha256
            or gate.get("observer_instance_sha256") != self.observer_instance_sha256
            or gate.get("current_traffic_percent") != 0
            or gate.get("target_traffic_percent") != 5
        ):
            raise DirectTrafficPreproductionConfigError(
                "production Gate authorization is invalid"
            )
        return validated


def production_namespace_sha256(namespace_id: str) -> str:
    try:
        return _production_namespace_sha256(namespace_id)
    except DirectTrafficProductionGateConfigError as exc:
        raise DirectTrafficPreproductionConfigError(str(exc)) from exc


def verify_production_execution(
    adapter: HTTPSProductionDirectTrafficAdapter,
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
    production_gate_dir: Path,
    production_gate_key: bytes,
    production_gate_key_id: str,
    *,
    max_age_seconds: int = 900,
    now: datetime | None = None,
) -> VerifiedProductionExecution:
    _validate_production_secrets(
        adapter.config.token,
        rollout_key,
        certification_key,
        shadow_key,
        production_gate_key,
    )
    gate = verify_production_gate_attestation(
        production_gate_dir,
        production_gate_key,
        production_gate_key_id,
        max_age_seconds=max_age_seconds,
        now=now,
    )
    gate_report = _read_gate(production_gate_dir, expect_attestation=True)
    gate_sha256 = _digest(_canonical_json(gate))
    base = verify_preproduction_execution(
        adapter,
        sandbox,
        decision_dir,
        release_history,
        policy,
        policy_sha256,
        rollout_key,
        rollout_key_id,
        certification_path,
        certification_key,
        certification_key_id,
        shadow_gate_dir,
        shadow_history,
        shadow_key,
        shadow_key_id,
        max_age_seconds=max_age_seconds,
        now=now,
        execution_extras={
            "production_gate_attestation": gate,
            "production_gate_attestation_sha256": gate_sha256,
        },
    )
    if (
        base.request.decision != "expand"
        or base.request.expected_traffic_percent != 0
        or base.request.target_traffic_percent != 5
        or base.request.expected_revision != 0
        or int(base.local_state["operations"]) != 0
    ):
        raise DirectTrafficPreproductionConfigError(
            "production adapter permits only the initial 0%-to-5% transition"
        )
    expected_sources = {
        "adapter_certification_sha256": base.certification_sha256,
        "shadow_attestation_sha256": base.shadow_attestation_sha256,
        "rollout_attestation_sha256": _digest(_canonical_json(base.rollout_attestation)),
    }
    if any(gate_report["source"].get(field) != value for field, value in expected_sources.items()):
        raise DirectTrafficPreproductionConfigError(
            "production Gate is not bound to the execution proofs"
        )
    adapter._validate_authorization(base)
    return VerifiedProductionExecution(base, gate, gate_sha256)


def apply_verified_production_execution(
    adapter: HTTPSProductionDirectTrafficAdapter,
    sandbox: SQLiteDirectTrafficSandbox,
    release_history_path: Path,
    authorization: VerifiedProductionExecution,
) -> dict[str, Any]:
    receipt = apply_verified_preproduction_execution(
        adapter, sandbox, release_history_path, authorization.execution
    )
    return {
        **receipt,
        "schema_version": RECEIPT_SCHEMA_VERSION,
        "production_gate_attestation_id": authorization.production_gate_attestation[
            "attestation_id"
        ],
    }


def _validate_production_namespace(value: Any) -> str:
    if not isinstance(value, str) or _NAMESPACE_RE.fullmatch(value) is None:
        raise DirectTrafficPreproductionConfigError(
            "namespace must use the explicit production- prefix"
        )
    return value


def _validate_production_environment(value: Any) -> str:
    if not isinstance(value, str) or _ENVIRONMENT_RE.fullmatch(value) is None:
        raise DirectTrafficPreproductionConfigError(
            "environment must use the explicit production- prefix"
        )
    return value


def _validate_production_secrets(
    token: str,
    rollout_key: bytes,
    certification_key: bytes,
    shadow_key: bytes,
    production_gate_key: bytes,
) -> None:
    values = (
        token.encode("utf-8"),
        rollout_key,
        certification_key,
        shadow_key,
        production_gate_key,
    )
    if len(set(values)) != len(values):
        raise DirectTrafficPreproductionConfigError(
            "production Provider and proof credentials must all be distinct"
        )
    preproduction_names = (
        PREPRODUCTION_TOKEN_ENV,
        "OBSERVABILITY_DIRECT_ROLLOUT_ATTESTATION_KEY",
        "OBSERVABILITY_DIRECT_TRAFFIC_ADAPTER_CERTIFICATION_KEY",
        "OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_GATE_KEY",
    )
    preproduction = {
        value.encode("utf-8")
        for name in preproduction_names
        if (value := os.environ.get(name)) is not None
    }
    if any(value in preproduction for value in values):
        raise DirectTrafficPreproductionConfigError(
            "production credentials must be distinct from preproduction credentials"
        )


def main(argv: Sequence[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        description="Apply the one-time 0%-to-5% production direct-traffic transition"
    )
    parser.add_argument("--sandbox-ledger", type=Path, required=True)
    parser.add_argument("--release-history-ledger", type=Path, required=True)
    parser.add_argument("--decision-dir", type=Path, required=True)
    parser.add_argument("--policy", type=Path, required=True)
    parser.add_argument("--adapter-certification", type=Path, required=True)
    parser.add_argument("--shadow-history-ledger", type=Path, required=True)
    parser.add_argument("--shadow-gate-dir", type=Path, required=True)
    parser.add_argument("--production-gate-dir", type=Path, required=True)
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
        if token == os.environ.get(PREPRODUCTION_TOKEN_ENV, "__absent__"):
            raise DirectTrafficPreproductionConfigError(
                "production token must be distinct from preproduction token"
            )
        environment_id = _validate_production_environment(args.environment_id)
        environment_digest = environment_sha256(environment_id)
        adapter = HTTPSProductionDirectTrafficAdapter(
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
            raise DirectTrafficPreproductionConflictError(
                "another environment operation holds the shared lease"
            )
        sandbox = SQLiteDirectTrafficSandbox(
            args.sandbox_ledger, environment_digest, args.provider_instance
        )
        release_history = ReleaseGateHistory(
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
            profile="production"
        )
        shadow_key, shadow_key_id = _key_from_environment(SHADOW_KEY_ENV, SHADOW_KEY_ID_ENV)
        production_key, production_key_id = _key_from_environment(
            PRODUCTION_GATE_KEY_ENV, PRODUCTION_GATE_KEY_ID_ENV
        )
        _validate_production_secrets(
            token,
            rollout_key,
            certification_key,
            shadow_key,
            production_key,
        )
        authorization = verify_production_execution(
            adapter,
            sandbox,
            args.decision_dir,
            release_history,
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
            args.production_gate_dir,
            production_key,
            production_key_id,
            max_age_seconds=args.max_age_seconds,
        )
        receipt = apply_verified_production_execution(
            adapter, sandbox, release_history.path, authorization
        )
        print(json.dumps(receipt, sort_keys=True))
        return 0
    except DirectTrafficPreproductionConflictError as exc:
        print(f"direct traffic production conflict: {exc}", file=sys.stderr)
        return 4
    except DirectTrafficPreproductionIndeterminateError as exc:
        print(f"direct traffic production indeterminate: {exc}", file=sys.stderr)
        return 5
    except (
        DirectTrafficPreproductionError,
        DirectTrafficProductionGateError,
        PreproductionProviderError,
        OSError,
        sqlite3.Error,
    ) as exc:
        print(f"direct traffic production failed: {exc}", file=sys.stderr)
        return 1
    finally:
        if lease is not None:
            lease.release()


if __name__ == "__main__":
    raise SystemExit(main())
