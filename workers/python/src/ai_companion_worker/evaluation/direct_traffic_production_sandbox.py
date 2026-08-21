from __future__ import annotations

import argparse
import json
import secrets
import sqlite3
import sys
import urllib.parse
from datetime import datetime
from pathlib import Path
from typing import Any, Callable, Mapping, Sequence

from .deployment_controller import _canonical_json, _digest
from .direct_rollout_traffic import TrafficChangeRequest
from .direct_traffic_preproduction import (
    DirectTrafficPreproductionConfigError,
    HTTPSPreproductionConfig,
    PreproductionProviderError,
)
from .direct_traffic_preproduction_sandbox import (
    SQLitePreproductionCertificationAdapter,
    SQLitePreproductionPlatform,
)
from .direct_traffic_certification import (
    DirectTrafficCertificationError,
    certification_key_from_environment,
    create_certification,
    verify_certification,
)
from .direct_traffic_production import (
    ADAPTER_NAME,
    EXECUTION_SCHEMA_VERSION,
    IMPLEMENTATION_VERSION,
    RESULT_SCHEMA_VERSION,
    STATE_SCHEMA_VERSION,
    _validate_production_environment,
    production_namespace_sha256,
)
from .direct_traffic_production_gate import (
    DirectTrafficProductionGateError,
    verify_embedded_production_gate_attestation,
)
from .direct_traffic_shadow import ReadOnlyDirectTrafficProviderConfig
from .release_gate_history import environment_sha256


class SQLiteProductionCertificationAdapter(SQLitePreproductionCertificationAdapter):
    name = ADAPTER_NAME
    implementation_version = IMPLEMENTATION_VERSION


class SQLiteProductionPlatform(SQLitePreproductionPlatform):
    """Local-only Provider simulator for the isolated production protocol."""

    adapter_name = ADAPTER_NAME
    implementation_version = IMPLEMENTATION_VERSION
    execution_schema_version = EXECUTION_SCHEMA_VERSION
    state_schema_version = STATE_SCHEMA_VERSION
    result_schema_version = RESULT_SCHEMA_VERSION
    execution_extra_fields = frozenset(
        {"production_gate_attestation", "production_gate_attestation_sha256"}
    )

    def __init__(
        self,
        path: Path,
        environment_sha256: str,
        provider_instance: str,
        namespace_id: str,
        token: str,
        rollout_key: bytes,
        rollout_key_id: str,
        certification_key: bytes,
        certification_key_id: str,
        shadow_key: bytes,
        shadow_key_id: str,
        production_gate_key: bytes,
        production_gate_key_id: str,
        *,
        initial_traffic_percent: int,
        base_url: str = "https://traffic.provider.example/api",
        now: Callable[[], datetime],
    ) -> None:
        proof_keys = (
            rollout_key,
            certification_key,
            shadow_key,
            production_gate_key,
        )
        if len(set(proof_keys)) != len(proof_keys):
            raise PreproductionProviderError("provider_production_proof_key_reuse")
        self._production_gate_key = production_gate_key
        self._production_gate_key_id = production_gate_key_id
        super().__init__(
            path,
            environment_sha256,
            provider_instance,
            namespace_id,
            token,
            rollout_key,
            rollout_key_id,
            certification_key,
            certification_key_id,
            shadow_key,
            shadow_key_id,
            initial_traffic_percent=initial_traffic_percent,
            base_url=base_url,
            now=now,
        )

    def _namespace_sha256(self, namespace_id: str) -> str:
        return production_namespace_sha256(namespace_id)

    def _observer_config(
        self, config: HTTPSPreproductionConfig
    ) -> ReadOnlyDirectTrafficProviderConfig:
        namespace = urllib.parse.quote(config.namespace_id, safe="")
        return ReadOnlyDirectTrafficProviderConfig(
            name=ADAPTER_NAME,
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

    def _validate_execution_payload(
        self, idempotency_key: str, value: Any
    ) -> tuple[dict[str, Any], TrafficChangeRequest, str]:
        payload, request, request_sha256 = super()._validate_execution_payload(
            idempotency_key, value
        )
        gate = payload.get("production_gate_attestation")
        expected_state = payload.get("expected_state")
        if (
            not isinstance(gate, Mapping)
            or payload.get("production_gate_attestation_sha256")
            != _digest(_canonical_json(dict(gate)))
            or request.decision != "expand"
            or request.expected_traffic_percent != 0
            or request.target_traffic_percent != 5
            or request.expected_revision != 0
            or not isinstance(expected_state, Mapping)
            or expected_state.get("current_traffic_percent") != 0
            or expected_state.get("revision") != 0
            or expected_state.get("operations") != 0
            or gate.get("rollout_attestation_sha256")
            != _digest(_canonical_json(dict(payload["rollout_attestation"])))
            or gate.get("adapter_certification_sha256")
            != payload.get("adapter_certification_sha256")
            or gate.get("shadow_attestation_sha256")
            != payload.get("shadow_attestation_sha256")
        ):
            raise PreproductionProviderError("provider_production_gate_binding")
        try:
            verify_embedded_production_gate_attestation(
                gate,
                self._production_gate_key,
                self._production_gate_key_id,
                environment_sha256=self.environment_sha256,
                provider_instance_sha256=self.provider_instance_sha256,
                namespace_sha256=self.namespace_sha256,
                observer_instance_sha256=self.observer_instance_sha256,
                now=self.now(),
            )
        except DirectTrafficProductionGateError as exc:
            raise PreproductionProviderError("provider_production_gate_proof") from exc
        return payload, request, request_sha256


def main(argv: Sequence[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        description="Certify the production HTTPS writer in an isolated local namespace"
    )
    subparsers = parser.add_subparsers(dest="command", required=True)
    certify = subparsers.add_parser("certify")
    verify = subparsers.add_parser("verify")
    for command in (certify, verify):
        command.add_argument("--certification-ledger", type=Path, required=True)
        command.add_argument("--environment-id", required=True)
        command.add_argument("--provider-instance", required=True)
        command.add_argument("--namespace-id", required=True)
    certify.add_argument("--output-dir", type=Path, required=True)
    certify.add_argument("--nonce", default=secrets.token_hex(16))
    certify.add_argument("--ttl-seconds", type=int, default=3600)
    verify.add_argument("--certification", type=Path, required=True)
    args = parser.parse_args(argv)
    try:
        environment_id = _validate_production_environment(args.environment_id)
        adapter = SQLiteProductionCertificationAdapter(
            args.certification_ledger,
            environment_sha256(environment_id),
            args.provider_instance,
            namespace_id=args.namespace_id,
        )
        key, key_id = certification_key_from_environment(profile="production")
        if args.command == "certify":
            result, path, created = create_certification(
                adapter,
                args.output_dir,
                key,
                key_id,
                nonce=args.nonce,
                ttl_seconds=args.ttl_seconds,
            )
            output = {
                "status": "certified" if created else "reused",
                "certification_id": result["certification_id"],
                "certification": str(path),
                "expires_at": result["expires_at"],
            }
        else:
            result = verify_certification(args.certification, adapter, key, key_id)
            output = {
                "status": "verified",
                "certification_id": result["certification_id"],
                "expires_at": result["expires_at"],
            }
        print(json.dumps(output, sort_keys=True))
        return 0
    except DirectTrafficPreproductionConfigError as exc:
        print(f"production certification configuration error: {exc}", file=sys.stderr)
        return 2
    except (DirectTrafficCertificationError, OSError, sqlite3.Error) as exc:
        print(f"production certification error: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
