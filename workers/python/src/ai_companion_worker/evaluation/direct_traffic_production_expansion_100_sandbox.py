from __future__ import annotations

import argparse
import hashlib
import hmac
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
from .direct_traffic_certification import (
    certification_key_from_environment,
    create_certification,
    verify_certification,
)
from .direct_traffic_preproduction import HTTPSPreproductionConfig, PreproductionProviderError
from .direct_traffic_production import _validate_production_environment
from .direct_traffic_production_expansion_50_sandbox import (
    SQLiteProductionExpansion50Platform,
)
from .direct_traffic_production_expansion_100 import (
    ADAPTER_NAME,
    EXECUTION_SCHEMA_VERSION,
    IMPLEMENTATION_VERSION,
    RESULT_SCHEMA_VERSION,
    STATE_SCHEMA_VERSION,
)
from .direct_traffic_production_expansion_100_gate import (
    DirectTrafficProductionExpansion100GateError,
    validate_prior_expansion_receipt,
    verify_embedded_expansion_100_gate_attestation,
)
from .direct_traffic_preproduction_sandbox import SQLitePreproductionCertificationAdapter
from .direct_traffic_shadow import ReadOnlyDirectTrafficProviderConfig
from .release_gate_history import environment_sha256


class SQLiteProductionExpansion100CertificationAdapter(
    SQLitePreproductionCertificationAdapter
):
    name = ADAPTER_NAME
    implementation_version = IMPLEMENTATION_VERSION


class SQLiteProductionExpansion100Platform(SQLiteProductionExpansion50Platform):
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
        shadow_key: bytes,
        shadow_key_id: str,
        expansion_100_gate_key: bytes,
        expansion_100_gate_key_id: str,
        *,
        initial_traffic_percent: int,
        base_url: str = "https://traffic.provider.example/api",
        now: Callable[[], datetime],
    ) -> None:
        self._expansion_100_gate_key = expansion_100_gate_key
        self._expansion_100_gate_key_id = expansion_100_gate_key_id
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
            expansion_100_gate_key,
            expansion_100_gate_key_id,
            initial_traffic_percent=initial_traffic_percent,
            base_url=base_url,
            now=now,
        )

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
        if not isinstance(value, Mapping) or set(value) != fields:
            raise PreproductionProviderError("provider_contract")
        result = dict(value)
        expected_sha = _digest(
            _canonical_json(
                {field: item for field, item in result.items() if field != "request_sha256"}
            )
        )
        try:
            request_value = result["rollout_request"]
            if not isinstance(request_value, Mapping):
                raise TypeError("request")
            request = _validate_request(TrafficChangeRequest(**dict(request_value)))
            rollout = dict(result["rollout_attestation"])
            certification = dict(result["adapter_certification"])
            shadow = dict(result["shadow_attestation"])
            gate = dict(result["expansion_100_gate_attestation"])
            prior = validate_prior_expansion_receipt(result["prior_expansion_receipt"])
            expected_state = dict(result["expected_state"])
        except (TypeError, ValueError, KeyError) as exc:
            raise PreproductionProviderError("provider_contract") from exc
        if any(
            (
                result["schema_version"] != self.execution_schema_version,
                result["namespace_sha256"] != self.namespace_sha256,
                result["request_sha256"] != expected_sha,
                request.idempotency_key != idempotency_key,
                request.environment_sha256 != self.environment_sha256,
                request.provider_instance_sha256 != self.provider_instance_sha256,
                request.decision != "expand",
                request.expected_traffic_percent != 50,
                request.target_traffic_percent != 100,
                request.expected_revision != 6,
                request.attestation_id != rollout.get("attestation_id"),
                request.attestation_sha256 != _digest(_canonical_json(rollout)),
                result["adapter_certification_sha256"]
                != _digest(_canonical_json(certification)),
                result["shadow_attestation_sha256"] != _digest(_canonical_json(shadow)),
                result["expansion_100_gate_attestation_sha256"]
                != _digest(_canonical_json(gate)),
                result["prior_expansion_receipt_sha256"]
                != _digest(_canonical_json(prior)),
                certification.get("adapter") != self.adapter_name,
                certification.get("implementation_version")
                != self.implementation_version,
                certification.get("provider_instance_sha256")
                != self.provider_instance_sha256,
                certification.get("environment_sha256") != self.environment_sha256,
                shadow.get("provider_instance_sha256")
                != self.provider_instance_sha256,
                shadow.get("environment_sha256") != self.environment_sha256,
                expected_state.get("current_traffic_percent") != 50,
                expected_state.get("revision") != 6,
                expected_state.get("operations") != 6,
                prior.get("namespace_sha256") != self.namespace_sha256,
                prior.get("history_chain_sha256")
                != expected_state.get("history_chain_sha256"),
            )
        ):
            raise PreproductionProviderError("provider_expansion_binding")
        self._verify_expansion_proofs(
            rollout,
            certification,
            shadow,
            gate,
            prior,
            request,
            str(result["rollout_report_sha256"]),
            str(result["shadow_gate_report_sha256"]),
        )
        return result, request, expected_sha

    def _verify_expansion_proofs(
        self,
        rollout: dict[str, Any],
        certification: dict[str, Any],
        shadow: dict[str, Any],
        gate: dict[str, Any],
        prior: dict[str, Any],
        request: TrafficChangeRequest,
        rollout_report_sha256: str,
        shadow_gate_report_sha256: str,
    ) -> None:
        now = self.now()
        for artifact, trust, created_field in zip(
            (rollout, certification, shadow),
            self._trusted_proofs,
            ("created_at", "certified_at", "created_at"),
            strict=True,
        ):
            key, key_id, domain = trust
            unsigned = {
                field: item for field, item in artifact.items() if field != "signature"
            }
            derived = hmac.new(key, domain, hashlib.sha256).digest()
            signature = hmac.new(
                derived, _canonical_json(unsigned), hashlib.sha256
            ).hexdigest()
            if artifact.get("key_id") != key_id or not hmac.compare_digest(
                str(artifact.get("signature")), signature
            ):
                raise PreproductionProviderError("provider_proof_signature")
            created = _parse_timestamp(str(artifact[created_field]), created_field)
            expires = _parse_timestamp(str(artifact["expires_at"]), "expires_at")
            if created > now or now >= expires:
                raise PreproductionProviderError("provider_proof_time")
        expected_observer = self.observer_instance_sha256
        if any(
            (
                rollout.get("decision_id") != request.decision_id,
                rollout.get("decision") != "expand",
                rollout.get("current_traffic_percent") != 50,
                rollout.get("target_traffic_percent") != 100,
                rollout.get("history_chain_sha256") != request.history_chain_sha256,
                shadow.get("decision") != "pass",
                shadow.get("observer_instance_sha256") != expected_observer,
            )
        ):
            raise PreproductionProviderError("provider_expansion_proof_binding")
        try:
            verify_embedded_expansion_100_gate_attestation(
                gate,
                self._expansion_100_gate_key,
                self._expansion_100_gate_key_id,
                environment_sha256=self.environment_sha256,
                provider_instance_sha256=self.provider_instance_sha256,
                namespace_sha256=self.namespace_sha256,
                observer_instance_sha256=expected_observer,
                rollout_attestation_sha256=_digest(_canonical_json(rollout)),
                rollout_report_sha256=rollout_report_sha256,
                adapter_certification_sha256=_digest(_canonical_json(certification)),
                shadow_attestation_sha256=_digest(_canonical_json(shadow)),
                shadow_gate_report_sha256=shadow_gate_report_sha256,
                prior_expansion_receipt_sha256=_digest(_canonical_json(prior)),
                now=now,
            )
        except DirectTrafficProductionExpansion100GateError as exc:
            raise PreproductionProviderError("provider_expansion_100_gate_proof") from exc


def main(argv: Sequence[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        description="Certify the isolated production 50%-to-100% expansion writer"
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
    certify.add_argument("--ttl-seconds", type=int, default=900)
    verify.add_argument("--certification", type=Path, required=True)
    args = parser.parse_args(argv)
    try:
        adapter = SQLiteProductionExpansion100CertificationAdapter(
            args.certification_ledger,
            environment_sha256(_validate_production_environment(args.environment_id)),
            args.provider_instance,
            namespace_id=args.namespace_id,
        )
        key, key_id = certification_key_from_environment(
            profile="production_expansion_100"
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
    except (ValueError, OSError, sqlite3.Error) as exc:
        print(f"production expansion certification failed: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
