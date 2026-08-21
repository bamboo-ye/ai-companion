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

from .deployment_controller import _canonical_json, _digest, _parse_timestamp
from .direct_rollout_traffic import TrafficChangeRequest, _validate_request
from .direct_traffic_preproduction import HTTPSPreproductionConfig, PreproductionProviderError
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
from .direct_traffic_preproduction import DirectTrafficPreproductionConfigError
from .direct_traffic_production import _validate_production_environment
from .direct_traffic_production import production_namespace_sha256
from .direct_traffic_production_emergency import (
    ADAPTER_NAME,
    EXECUTION_SCHEMA_VERSION,
    IMPLEMENTATION_VERSION,
    RESULT_SCHEMA_VERSION,
    STATE_SCHEMA_VERSION,
)
from .direct_traffic_production_emergency_gate import (
    DirectTrafficProductionEmergencyGateError,
    _validate_activation_receipt,
    verify_embedded_emergency_gate_attestation,
)
from .direct_traffic_shadow import ReadOnlyDirectTrafficProviderConfig
from .release_gate_history import environment_sha256


class SQLiteProductionEmergencyCertificationAdapter(
    SQLitePreproductionCertificationAdapter
):
    name = ADAPTER_NAME
    implementation_version = IMPLEMENTATION_VERSION


class SQLiteProductionEmergencyPlatform(SQLitePreproductionPlatform):
    """Provider-side simulator for the separately trusted production stop path."""

    adapter_name = ADAPTER_NAME
    implementation_version = IMPLEMENTATION_VERSION
    execution_schema_version = EXECUTION_SCHEMA_VERSION
    state_schema_version = STATE_SCHEMA_VERSION
    result_schema_version = RESULT_SCHEMA_VERSION

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
        emergency_gate_key: bytes,
        emergency_gate_key_id: str,
        *,
        initial_traffic_percent: int,
        base_url: str = "https://traffic.provider.example/api",
        now: Callable[[], datetime],
    ) -> None:
        if len({rollout_key, certification_key, emergency_gate_key}) != 3:
            raise PreproductionProviderError("provider_emergency_proof_key_reuse")
        self._emergency_gate_key = emergency_gate_key
        self._emergency_gate_key_id = emergency_gate_key_id
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
            None,
            None,
            initial_traffic_percent=initial_traffic_percent,
            base_url=base_url,
            now=now,
        )

    def _proof_trust(
        self,
        rollout_key: bytes,
        rollout_key_id: str,
        certification_key: bytes,
        certification_key_id: str,
        shadow_key: bytes | None,
        shadow_key_id: str | None,
    ) -> tuple[tuple[bytes, str, bytes], ...]:
        del shadow_key, shadow_key_id
        return (
            (
                rollout_key,
                rollout_key_id,
                b"ai-companion/agent-direct-rollout-attestation/v1",
            ),
            (
                certification_key,
                certification_key_id,
                b"ai-companion/agent-direct-traffic-adapter-certification/v1",
            ),
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
            state_path=f"/v1/namespaces/{namespace}/direct-traffic/emergency-state",
            timeout_seconds=config.timeout_seconds,
            max_attempts=config.max_read_attempts,
            retry_base_seconds=config.retry_base_seconds,
            max_response_bytes=config.max_response_bytes,
        )

    def _validate_execution_payload(
        self, idempotency_key: str, value: Any
    ) -> tuple[dict[str, Any], TrafficChangeRequest, str]:
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
        if not isinstance(value, Mapping) or set(value) != fields:
            raise PreproductionProviderError("provider_contract")
        result = dict(value)
        expected_sha256 = _digest(
            _canonical_json(
                {field: item for field, item in result.items() if field != "request_sha256"}
            )
        )
        try:
            request_value = result["rollout_request"]
            if not isinstance(request_value, Mapping):
                raise TypeError("rollout request")
            request = _validate_request(TrafficChangeRequest(**dict(request_value)))
            rollout = result["rollout_attestation"]
            certification = result["adapter_certification"]
            gate = result["emergency_gate_attestation"]
            receipt = _validate_activation_receipt(result["activation_receipt"])
            expected_state = result["expected_state"]
        except (TypeError, ValueError, KeyError) as exc:
            raise PreproductionProviderError("provider_contract") from exc
        mappings = (rollout, certification, gate, expected_state)
        if (
            result["schema_version"] != self.execution_schema_version
            or result["namespace_sha256"] != self.namespace_sha256
            or result["request_sha256"] != expected_sha256
            or any(not isinstance(item, Mapping) for item in mappings)
            or request.idempotency_key != idempotency_key
            or request.environment_sha256 != self.environment_sha256
            or request.provider_instance_sha256 != self.provider_instance_sha256
            or request.decision not in {"rollback", "disable"}
            or request.expected_traffic_percent != 5
            or request.target_traffic_percent != 0
            or request.expected_revision != 1
            or request.attestation_id != rollout.get("attestation_id")
            or request.attestation_sha256 != _digest(_canonical_json(dict(rollout)))
            or result["adapter_certification_sha256"]
            != _digest(_canonical_json(dict(certification)))
            or result["emergency_gate_attestation_sha256"]
            != _digest(_canonical_json(dict(gate)))
            or result["activation_receipt_sha256"]
            != _digest(_canonical_json(receipt))
            or certification.get("adapter") != self.adapter_name
            or certification.get("implementation_version") != self.implementation_version
            or certification.get("provider_instance_sha256")
            != self.provider_instance_sha256
            or certification.get("environment_sha256") != self.environment_sha256
            or expected_state.get("current_traffic_percent") != 5
            or expected_state.get("revision") != 1
            or expected_state.get("operations") != 1
            or receipt.get("namespace_sha256") != self.namespace_sha256
            or receipt.get("current_traffic_percent") != 5
            or receipt.get("revision") != 1
            or receipt.get("history_chain_sha256")
            != expected_state.get("history_chain_sha256")
        ):
            raise PreproductionProviderError("provider_emergency_binding")
        self._verify_emergency_proofs(
            result,
            request,
            dict(rollout),
            dict(certification),
            dict(gate),
            receipt,
        )
        return result, request, expected_sha256

    def _verify_emergency_proofs(
        self,
        payload: Mapping[str, Any],
        request: TrafficChangeRequest,
        rollout: dict[str, Any],
        certification: dict[str, Any],
        gate: dict[str, Any],
        receipt: dict[str, Any],
    ) -> None:
        now = self.now()
        for artifact, trust, created_field in zip(
            (rollout, certification),
            self._trusted_proofs,
            ("created_at", "certified_at"),
            strict=True,
        ):
            self._verify_hmac(artifact, trust)
            try:
                created = _parse_timestamp(str(artifact[created_field]), created_field)
                expires = _parse_timestamp(str(artifact["expires_at"]), "expires_at")
            except (KeyError, ValueError) as exc:
                raise PreproductionProviderError("provider_proof_time") from exc
            if created > now or now >= expires:
                raise PreproductionProviderError("provider_proof_time")
        if (
            rollout.get("decision_id") != request.decision_id
            or rollout.get("decision") != request.decision
            or rollout.get("current_traffic_percent") != 5
            or rollout.get("target_traffic_percent") != 0
            or rollout.get("history_chain_sha256") != request.history_chain_sha256
            or rollout.get("environment_sha256") != self.environment_sha256
        ):
            raise PreproductionProviderError("provider_emergency_rollout_proof")
        try:
            verify_embedded_emergency_gate_attestation(
                gate,
                self._emergency_gate_key,
                self._emergency_gate_key_id,
                environment_sha256=self.environment_sha256,
                provider_instance_sha256=self.provider_instance_sha256,
                namespace_sha256=self.namespace_sha256,
                rollout_attestation_sha256=_digest(_canonical_json(rollout)),
                adapter_certification_sha256=_digest(_canonical_json(certification)),
                activation_receipt_sha256=_digest(_canonical_json(receipt)),
                now=now,
            )
        except DirectTrafficProductionEmergencyGateError as exc:
            raise PreproductionProviderError("provider_emergency_gate_proof") from exc
        if gate.get("rollout_decision") != request.decision:
            raise PreproductionProviderError("provider_emergency_gate_binding")

    @staticmethod
    def _verify_hmac(
        artifact: Mapping[str, Any], trust: tuple[bytes, str, bytes]
    ) -> None:
        import hashlib
        import hmac

        key, key_id, domain = trust
        unsigned = {field: item for field, item in artifact.items() if field != "signature"}
        derived = hmac.new(key, domain, hashlib.sha256).digest()
        expected = hmac.new(derived, _canonical_json(unsigned), hashlib.sha256).hexdigest()
        if (
            artifact.get("key_id") != key_id
            or not hmac.compare_digest(str(artifact.get("signature")), expected)
        ):
            raise PreproductionProviderError("provider_proof_signature")


def main(argv: Sequence[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        description="Certify the isolated production emergency writer"
    )
    commands = parser.add_subparsers(dest="command", required=True)
    certify = commands.add_parser("certify")
    verify = commands.add_parser("verify")
    for command in (certify, verify):
        command.add_argument("--certification-ledger", type=Path, required=True)
        command.add_argument("--environment-id", required=True)
        command.add_argument("--provider-instance", required=True)
        command.add_argument("--namespace-id", required=True)
    certify.add_argument("--output-dir", type=Path, required=True)
    certify.add_argument("--nonce", default=secrets.token_hex(16))
    certify.add_argument("--ttl-seconds", type=int, default=300)
    verify.add_argument("--certification", type=Path, required=True)
    args = parser.parse_args(argv)
    try:
        environment_id = _validate_production_environment(args.environment_id)
        adapter = SQLiteProductionEmergencyCertificationAdapter(
            args.certification_ledger,
            environment_sha256(environment_id),
            args.provider_instance,
            namespace_id=args.namespace_id,
        )
        key, key_id = certification_key_from_environment(
            profile="production_emergency"
        )
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
        print(f"production emergency certification configuration error: {exc}", file=sys.stderr)
        return 2
    except (DirectTrafficCertificationError, OSError, sqlite3.Error) as exc:
        print(f"production emergency certification error: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
