from __future__ import annotations

import argparse
import hashlib
import hmac
import json
import os
import re
import sys
from datetime import datetime, timedelta
from pathlib import Path
from typing import Any, Mapping, Sequence

from .deployment_controller import (
    _canonical_json,
    _digest,
    _format_timestamp,
    _normalize_now,
    _parse_timestamp,
)
from .deployment_resolution import (
    DeploymentResolutionError,
    _ensure_private_state_directory,
    _read_contract,
    _validate_key,
    _validate_key_id,
    _write_exclusive_private_json,
)
from .direct_rollout import (
    PRODUCTION_EMERGENCY_KEY_ENV as ROLLOUT_KEY_ENV,
    PRODUCTION_EMERGENCY_KEY_ID_ENV as ROLLOUT_KEY_ID_ENV,
    DirectRolloutError,
    _validate_attestation as _validate_rollout_attestation,
)
from .direct_traffic_certification import (
    PRODUCTION_EMERGENCY_KEY_ENV as CERTIFICATION_KEY_ENV,
    PRODUCTION_EMERGENCY_KEY_ID_ENV as CERTIFICATION_KEY_ID_ENV,
    DirectTrafficCertificationError,
    _validate_certification,
)
from .release_attestation import MAX_CLOCK_SKEW_SECONDS
from .direct_traffic_production_gate import production_namespace_sha256


GATE_SCHEMA_VERSION = "agent-direct-traffic-production-emergency-gate-v1"
ATTESTATION_SCHEMA_VERSION = (
    "agent-direct-traffic-production-emergency-gate-attestation-v1"
)
GATE_FILE = "direct-traffic-production-emergency-gate.json"
ATTESTATION_FILE = "direct-traffic-production-emergency-gate-attestation.json"
KEY_ENV = "OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EMERGENCY_GATE_KEY"
KEY_ID_ENV = "OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EMERGENCY_GATE_KEY_ID"
MAX_ATTESTATION_SECONDS = 15 * 60
SIGNING_DOMAIN = b"ai-companion/direct-traffic-production-emergency-gate/v1\0"
_DIGEST_RE = re.compile(r"[0-9a-f]{64}")
_INCIDENT_RE = re.compile(r"INC-[A-Z0-9][A-Z0-9-]{2,63}")
_ACTOR_RE = re.compile(r"[A-Za-z0-9][A-Za-z0-9._@-]{2,127}")
_GATE_ID_RE = re.compile(r"direct-traffic-production-emergency-gate-[0-9a-f]{32}")
_ATTESTATION_ID_RE = re.compile(
    r"direct-traffic-production-emergency-gate-attestation-[0-9a-f]{32}"
)


class DirectTrafficProductionEmergencyGateError(ValueError):
    pass


class DirectTrafficProductionEmergencyGateConfigError(
    DirectTrafficProductionEmergencyGateError
):
    pass


class DirectTrafficProductionEmergencyGateVerificationError(
    DirectTrafficProductionEmergencyGateError
):
    pass


def evaluate_emergency_gate(
    rollout_attestation: Mapping[str, Any],
    adapter_certification: Mapping[str, Any],
    activation_receipt: Mapping[str, Any],
    namespace_sha256: str,
    incident_id: str,
    authorized_by: str,
    *,
    rollout_key: bytes,
    rollout_key_id: str,
    certification_key: bytes,
    certification_key_id: str,
    max_age_seconds: int = 300,
    now: datetime | None = None,
) -> dict[str, Any]:
    current_time = _normalize_now(now)
    maximum_age = _validate_age(max_age_seconds)
    incident = _validate_incident(incident_id)
    actor = _validate_actor(authorized_by)
    namespace = _validate_digest(namespace_sha256, "production namespace")
    if rollout_key == certification_key:
        raise DirectTrafficProductionEmergencyGateConfigError(
            "emergency proof keys must be distinct"
        )
    try:
        rollout = _validate_rollout_attestation(rollout_attestation)
        certification = _validate_certification(adapter_certification)
    except (DirectRolloutError, DirectTrafficCertificationError) as exc:
        raise DirectTrafficProductionEmergencyGateVerificationError(
            "emergency upstream proof contract is invalid"
        ) from exc
    _verify_proof(
        rollout,
        rollout_key,
        rollout_key_id,
        b"ai-companion/agent-direct-rollout-attestation/v1",
        "created_at",
        maximum_age,
        current_time,
        "rollout",
    )
    _verify_proof(
        certification,
        certification_key,
        certification_key_id,
        b"ai-companion/agent-direct-traffic-adapter-certification/v1",
        "certified_at",
        maximum_age,
        current_time,
        "certification",
    )
    receipt = _validate_activation_receipt(activation_receipt)
    bindings = {
        "environment_sha256": rollout.get("environment_sha256"),
        "provider_instance_sha256": certification.get("provider_instance_sha256"),
        "namespace_sha256": namespace,
    }
    for field, value in bindings.items():
        _validate_digest(value, field)
    violations: list[str] = []
    if rollout.get("decision") not in {"rollback", "disable"}:
        violations.append("decision_not_emergency_stop")
    if rollout.get("current_traffic_percent") != 5:
        violations.append("current_stage_not_five")
    if rollout.get("target_traffic_percent") != 0:
        violations.append("target_not_zero")
    if certification.get("adapter") != "https-direct-traffic-production-emergency":
        violations.append("wrong_emergency_adapter")
    if certification.get("implementation_version") != "1.0.0":
        violations.append("wrong_emergency_adapter_version")
    if receipt.get("current_traffic_percent") != 5 or receipt.get("revision") != 1:
        violations.append("activation_receipt_not_initial_five")
    if receipt.get("namespace_sha256") != namespace:
        violations.append("activation_namespace_mismatch")
    for field in ("environment_sha256", "provider_instance_sha256"):
        expected = bindings[field]
        if certification.get(field) != expected:
            violations.append(f"{field}_mismatch")
    source = {
        "rollout_attestation_sha256": _digest(_canonical_json(rollout)),
        "adapter_certification_sha256": _digest(_canonical_json(certification)),
        "activation_receipt_sha256": _digest(_canonical_json(receipt)),
    }
    seed = {
        "generated_at": _format_timestamp(current_time),
        "incident_id": incident,
        "authorized_by": actor,
        **bindings,
        "rollout_decision": rollout.get("decision"),
        "current_traffic_percent": 5,
        "target_traffic_percent": 0,
        "source": source,
        "violations": sorted(set(violations)),
    }
    report = {
        "schema_version": GATE_SCHEMA_VERSION,
        "gate_id": (
            "direct-traffic-production-emergency-gate-"
            f"{_digest(_canonical_json(seed))[:32]}"
        ),
        "decision": "pass" if not violations else "hold",
        **seed,
    }
    return _validate_gate(report)


def write_emergency_gate(output_dir: Path, report: Mapping[str, Any]) -> Path:
    validated = _validate_gate(report)
    try:
        _ensure_private_state_directory(output_dir.parent)
        _ensure_private_state_directory(output_dir)
        if list(os.scandir(output_dir)):
            raise DirectTrafficProductionEmergencyGateVerificationError(
                "emergency Gate directory must be empty"
            )
        _write_exclusive_private_json(output_dir / GATE_FILE, validated)
    except (DeploymentResolutionError, OSError) as exc:
        raise DirectTrafficProductionEmergencyGateVerificationError(
            "cannot write private emergency Gate"
        ) from exc
    return output_dir / GATE_FILE


def attest_emergency_gate(
    gate_dir: Path,
    key: bytes,
    key_id: str,
    *,
    rollout_attestation: Mapping[str, Any],
    adapter_certification: Mapping[str, Any],
    activation_receipt: Mapping[str, Any],
    rollout_key: bytes,
    rollout_key_id: str,
    certification_key: bytes,
    certification_key_id: str,
    ttl_seconds: int = 300,
    max_source_age_seconds: int = 300,
    now: datetime | None = None,
) -> dict[str, Any]:
    signing_key = _validate_key(key, "emergency Gate key")
    signing_key_id = _validate_key_id(key_id, "emergency Gate key_id")
    ttl = _validate_age(ttl_seconds)
    current_time = _normalize_now(now)
    report = _read_gate(gate_dir, expect_attestation=False)
    if report["decision"] != "pass":
        raise DirectTrafficProductionEmergencyGateVerificationError(
            "only a passing emergency Gate can be attested"
        )
    recomputed = evaluate_emergency_gate(
        rollout_attestation,
        adapter_certification,
        activation_receipt,
        str(report["namespace_sha256"]),
        str(report["incident_id"]),
        str(report["authorized_by"]),
        rollout_key=rollout_key,
        rollout_key_id=rollout_key_id,
        certification_key=certification_key,
        certification_key_id=certification_key_id,
        max_age_seconds=max_source_age_seconds,
        now=current_time,
    )
    replay_fields = set(report) - {"generated_at", "gate_id"}
    if any(report[field] != recomputed[field] for field in replay_fields):
        raise DirectTrafficProductionEmergencyGateVerificationError(
            "emergency Gate source replay does not match the report"
        )
    generated_at = _parse_timestamp(str(report["generated_at"]), "emergency generated_at")
    if generated_at > current_time + timedelta(seconds=MAX_CLOCK_SKEW_SECONDS):
        raise DirectTrafficProductionEmergencyGateVerificationError(
            "emergency Gate is from the future"
        )
    payload: dict[str, Any] = {
        "schema_version": ATTESTATION_SCHEMA_VERSION,
        "attestation_id": "",
        "created_at": _format_timestamp(current_time),
        "expires_at": _format_timestamp(current_time + timedelta(seconds=ttl)),
        "gate_id": report["gate_id"],
        "decision": "pass",
        "incident_id": report["incident_id"],
        "authorized_by": report["authorized_by"],
        "rollout_decision": report["rollout_decision"],
        "environment_sha256": report["environment_sha256"],
        "provider_instance_sha256": report["provider_instance_sha256"],
        "namespace_sha256": report["namespace_sha256"],
        "current_traffic_percent": 5,
        "target_traffic_percent": 0,
        "gate_report_sha256": _digest(_canonical_json(report)),
        **report["source"],
        "key_id": signing_key_id,
        "algorithm": "hmac-sha256",
    }
    identity = {field: item for field, item in payload.items() if field != "attestation_id"}
    payload["attestation_id"] = (
        "direct-traffic-production-emergency-gate-attestation-"
        f"{_digest(_canonical_json(identity))[:32]}"
    )
    result = dict(payload)
    result["signature"] = _sign(signing_key, payload)
    validated = _validate_attestation(result)
    try:
        _write_exclusive_private_json(gate_dir / ATTESTATION_FILE, validated)
    except FileExistsError:
        return verify_emergency_gate_attestation(
            gate_dir, signing_key, signing_key_id, now=current_time
        )
    return validated


def verify_emergency_gate_attestation(
    gate_dir: Path,
    key: bytes,
    key_id: str,
    *,
    max_age_seconds: int = 300,
    now: datetime | None = None,
) -> dict[str, Any]:
    verification_key = _validate_key(key, "emergency Gate key")
    trusted_key_id = _validate_key_id(key_id, "emergency Gate key_id")
    maximum_age = _validate_age(max_age_seconds)
    current_time = _normalize_now(now)
    report = _read_gate(gate_dir, expect_attestation=True)
    attestation = _read_contract(gate_dir / ATTESTATION_FILE, _validate_attestation)
    if attestation["key_id"] != trusted_key_id:
        raise DirectTrafficProductionEmergencyGateVerificationError(
            "emergency Gate key_id is not trusted"
        )
    unsigned = {field: item for field, item in attestation.items() if field != "signature"}
    if not hmac.compare_digest(
        str(attestation["signature"]), _sign(verification_key, unsigned)
    ):
        raise DirectTrafficProductionEmergencyGateVerificationError(
            "emergency Gate signature is invalid"
        )
    expected = {
        "gate_id": report["gate_id"],
        "incident_id": report["incident_id"],
        "authorized_by": report["authorized_by"],
        "rollout_decision": report["rollout_decision"],
        "environment_sha256": report["environment_sha256"],
        "provider_instance_sha256": report["provider_instance_sha256"],
        "namespace_sha256": report["namespace_sha256"],
        "gate_report_sha256": _digest(_canonical_json(report)),
        **report["source"],
    }
    if any(attestation.get(field) != item for field, item in expected.items()):
        raise DirectTrafficProductionEmergencyGateVerificationError(
            "emergency Gate binding is invalid"
        )
    _verify_lifetime(attestation, "created_at", maximum_age, current_time)
    generated_at = _parse_timestamp(str(report["generated_at"]), "emergency generated_at")
    if current_time - generated_at > timedelta(seconds=maximum_age):
        raise DirectTrafficProductionEmergencyGateVerificationError(
            "emergency Gate is stale"
        )
    return attestation


def verify_embedded_emergency_gate_attestation(
    value: Any,
    key: bytes,
    key_id: str,
    *,
    environment_sha256: str,
    provider_instance_sha256: str,
    namespace_sha256: str,
    rollout_attestation_sha256: str,
    adapter_certification_sha256: str,
    activation_receipt_sha256: str,
    now: datetime | None = None,
) -> dict[str, Any]:
    verification_key = _validate_key(key, "emergency Gate key")
    trusted_key_id = _validate_key_id(key_id, "emergency Gate key_id")
    attestation = _validate_attestation(value)
    unsigned = {field: item for field, item in attestation.items() if field != "signature"}
    if (
        attestation["key_id"] != trusted_key_id
        or not hmac.compare_digest(
            str(attestation["signature"]), _sign(verification_key, unsigned)
        )
    ):
        raise DirectTrafficProductionEmergencyGateVerificationError(
            "embedded emergency Gate signature is invalid"
        )
    expected = {
        "environment_sha256": environment_sha256,
        "provider_instance_sha256": provider_instance_sha256,
        "namespace_sha256": namespace_sha256,
        "rollout_attestation_sha256": rollout_attestation_sha256,
        "adapter_certification_sha256": adapter_certification_sha256,
        "activation_receipt_sha256": activation_receipt_sha256,
        "current_traffic_percent": 5,
        "target_traffic_percent": 0,
        "decision": "pass",
    }
    if any(attestation.get(field) != item for field, item in expected.items()):
        raise DirectTrafficProductionEmergencyGateVerificationError(
            "embedded emergency Gate binding is invalid"
        )
    _verify_lifetime(attestation, "created_at", MAX_ATTESTATION_SECONDS, _normalize_now(now))
    return attestation


def _validate_gate(value: Any) -> dict[str, Any]:
    fields = {
        "schema_version", "gate_id", "generated_at", "decision", "incident_id",
        "authorized_by", "rollout_decision", "environment_sha256",
        "provider_instance_sha256", "namespace_sha256", "current_traffic_percent",
        "target_traffic_percent", "source", "violations",
    }
    if not isinstance(value, Mapping) or set(value) != fields:
        raise DirectTrafficProductionEmergencyGateVerificationError(
            "emergency Gate contract is invalid"
        )
    result = dict(value)
    if (
        result["schema_version"] != GATE_SCHEMA_VERSION
        or _GATE_ID_RE.fullmatch(str(result["gate_id"])) is None
        or result["decision"] not in {"pass", "hold"}
        or result["rollout_decision"] not in {"expand", "hold", "rollback", "disable"}
        or result["current_traffic_percent"] != 5
        or result["target_traffic_percent"] != 0
    ):
        raise DirectTrafficProductionEmergencyGateVerificationError(
            "emergency Gate identity is invalid"
        )
    if result["decision"] == "pass" and result["rollout_decision"] not in {
        "rollback",
        "disable",
    }:
        raise DirectTrafficProductionEmergencyGateVerificationError(
            "passing emergency Gate requires a stop decision"
        )
    _validate_incident(result["incident_id"])
    _validate_actor(result["authorized_by"])
    _parse_timestamp(str(result["generated_at"]), "emergency generated_at")
    for field in ("environment_sha256", "provider_instance_sha256", "namespace_sha256"):
        _validate_digest(result[field], field)
    source = result["source"]
    source_fields = {
        "rollout_attestation_sha256", "adapter_certification_sha256",
        "activation_receipt_sha256",
    }
    if not isinstance(source, Mapping) or set(source) != source_fields:
        raise DirectTrafficProductionEmergencyGateVerificationError(
            "emergency Gate source is invalid"
        )
    for field in source_fields:
        _validate_digest(source[field], field)
    violations = result["violations"]
    if (
        not isinstance(violations, list)
        or violations != sorted(set(violations))
        or any(not isinstance(item, str) or not item for item in violations)
        or (result["decision"] == "pass") != (not violations)
    ):
        raise DirectTrafficProductionEmergencyGateVerificationError(
            "emergency Gate violations are invalid"
        )
    seed = {field: result[field] for field in fields if field not in {"schema_version", "gate_id", "decision"}}
    expected_id = (
        "direct-traffic-production-emergency-gate-"
        f"{_digest(_canonical_json(seed))[:32]}"
    )
    if result["gate_id"] != expected_id:
        raise DirectTrafficProductionEmergencyGateVerificationError(
            "emergency Gate ID binding is invalid"
        )
    return result


def _validate_attestation(value: Any) -> dict[str, Any]:
    fields = {
        "schema_version", "attestation_id", "created_at", "expires_at", "gate_id",
        "decision", "incident_id", "authorized_by", "rollout_decision",
        "environment_sha256", "provider_instance_sha256", "namespace_sha256",
        "current_traffic_percent", "target_traffic_percent", "gate_report_sha256",
        "rollout_attestation_sha256", "adapter_certification_sha256",
        "activation_receipt_sha256", "key_id", "algorithm", "signature",
    }
    if not isinstance(value, Mapping) or set(value) != fields:
        raise DirectTrafficProductionEmergencyGateVerificationError(
            "emergency Gate attestation contract is invalid"
        )
    result = dict(value)
    if (
        result["schema_version"] != ATTESTATION_SCHEMA_VERSION
        or _ATTESTATION_ID_RE.fullmatch(str(result["attestation_id"])) is None
        or _GATE_ID_RE.fullmatch(str(result["gate_id"])) is None
        or result["decision"] != "pass"
        or result["rollout_decision"] not in {"rollback", "disable"}
        or result["current_traffic_percent"] != 5
        or result["target_traffic_percent"] != 0
        or result["algorithm"] != "hmac-sha256"
    ):
        raise DirectTrafficProductionEmergencyGateVerificationError(
            "emergency Gate attestation identity is invalid"
        )
    _validate_incident(result["incident_id"])
    _validate_actor(result["authorized_by"])
    _validate_key_id(result["key_id"], "emergency Gate key_id")
    for field in ("created_at", "expires_at"):
        _parse_timestamp(str(result[field]), f"emergency {field}")
    for field in (
        "environment_sha256", "provider_instance_sha256", "namespace_sha256",
        "gate_report_sha256", "rollout_attestation_sha256",
        "adapter_certification_sha256", "activation_receipt_sha256", "signature",
    ):
        _validate_digest(result[field], field)
    identity = {field: item for field, item in result.items() if field not in {"attestation_id", "signature"}}
    expected_id = (
        "direct-traffic-production-emergency-gate-attestation-"
        f"{_digest(_canonical_json(identity))[:32]}"
    )
    if result["attestation_id"] != expected_id:
        raise DirectTrafficProductionEmergencyGateVerificationError(
            "emergency Gate attestation ID binding is invalid"
        )
    return result


def _validate_activation_receipt(value: Any) -> dict[str, Any]:
    if not isinstance(value, Mapping):
        raise DirectTrafficProductionEmergencyGateConfigError(
            "activation receipt is invalid"
        )
    result = dict(value)
    required = {
        "schema_version", "namespace_sha256", "operation_id", "decision_id", "outcome",
        "current_traffic_percent", "revision", "history_chain_sha256",
        "adapter_certification_id", "shadow_attestation_id",
        "production_gate_attestation_id", "remote_reused", "local_reused", "applied_at",
    }
    if set(result) != required or result.get("schema_version") != "agent-direct-traffic-production-receipt-v1":
        raise DirectTrafficProductionEmergencyGateConfigError(
            "activation receipt contract is invalid"
        )
    for field in ("namespace_sha256", "history_chain_sha256"):
        _validate_digest(result[field], field)
    return result


def _verify_proof(
    artifact: Mapping[str, Any], key: bytes, key_id: str, domain: bytes,
    created_field: str, max_age_seconds: int, now: datetime, label: str,
) -> None:
    trusted_key = _validate_key(key, f"emergency {label} key")
    trusted_key_id = _validate_key_id(key_id, f"emergency {label} key_id")
    unsigned = {field: item for field, item in artifact.items() if field != "signature"}
    derived = hmac.new(trusted_key, domain, hashlib.sha256).digest()
    expected = hmac.new(derived, _canonical_json(unsigned), hashlib.sha256).hexdigest()
    if artifact.get("key_id") != trusted_key_id or not hmac.compare_digest(
        str(artifact.get("signature")), expected
    ):
        raise DirectTrafficProductionEmergencyGateVerificationError(
            f"emergency {label} signature is invalid"
        )
    _verify_lifetime(artifact, created_field, max_age_seconds, now)


def _verify_lifetime(
    artifact: Mapping[str, Any], created_field: str, max_age_seconds: int, now: datetime
) -> None:
    try:
        created = _parse_timestamp(str(artifact[created_field]), f"{created_field}")
        expires = _parse_timestamp(str(artifact["expires_at"]), "expires_at")
    except (KeyError, ValueError) as exc:
        raise DirectTrafficProductionEmergencyGateVerificationError(
            "emergency proof lifetime is invalid"
        ) from exc
    if (
        created > now + timedelta(seconds=MAX_CLOCK_SKEW_SECONDS)
        or expires <= created
        or expires - created > timedelta(seconds=MAX_ATTESTATION_SECONDS)
        or now >= expires
        or now - created > timedelta(seconds=max_age_seconds)
    ):
        raise DirectTrafficProductionEmergencyGateVerificationError(
            "emergency proof is stale"
        )


def _read_gate(gate_dir: Path, *, expect_attestation: bool) -> dict[str, Any]:
    expected = {GATE_FILE, ATTESTATION_FILE} if expect_attestation else {GATE_FILE}
    try:
        if {entry.name for entry in os.scandir(gate_dir)} != expected:
            raise DirectTrafficProductionEmergencyGateVerificationError(
                "emergency Gate artifact set is invalid"
            )
        return _read_contract(gate_dir / GATE_FILE, _validate_gate)
    except (DeploymentResolutionError, OSError) as exc:
        raise DirectTrafficProductionEmergencyGateVerificationError(
            "cannot read emergency Gate"
        ) from exc


def _read_json(path: Path) -> dict[str, Any]:
    try:
        return _read_contract(
            path,
            lambda value: dict(value) if isinstance(value, Mapping) else _invalid_json(),
        )
    except (DeploymentResolutionError, OSError) as exc:
        raise DirectTrafficProductionEmergencyGateConfigError(
            f"cannot read {path.name}"
        ) from exc


def _invalid_json() -> dict[str, Any]:
    raise DirectTrafficProductionEmergencyGateConfigError("JSON contract is invalid")


def _validate_digest(value: Any, label: str) -> str:
    if not isinstance(value, str) or _DIGEST_RE.fullmatch(value) is None:
        raise DirectTrafficProductionEmergencyGateConfigError(f"{label} is invalid")
    return value


def _validate_incident(value: Any) -> str:
    if not isinstance(value, str) or _INCIDENT_RE.fullmatch(value) is None:
        raise DirectTrafficProductionEmergencyGateConfigError("incident ID is invalid")
    return value


def _validate_actor(value: Any) -> str:
    if not isinstance(value, str) or _ACTOR_RE.fullmatch(value) is None:
        raise DirectTrafficProductionEmergencyGateConfigError("authorized actor is invalid")
    return value


def _validate_age(value: Any) -> int:
    if isinstance(value, bool) or not isinstance(value, int) or not 1 <= value <= MAX_ATTESTATION_SECONDS:
        raise DirectTrafficProductionEmergencyGateConfigError("emergency age is invalid")
    return value


def _sign(key: bytes, payload: Mapping[str, Any]) -> str:
    derived = hmac.new(key, SIGNING_DOMAIN, hashlib.sha256).digest()
    return hmac.new(derived, _canonical_json(payload), hashlib.sha256).hexdigest()


def _key_from_environment(name: str, key_id_name: str) -> tuple[bytes, str]:
    raw = os.environ.get(name)
    key_id = os.environ.get(key_id_name)
    if raw is None or key_id is None:
        raise DirectTrafficProductionEmergencyGateConfigError(
            f"{name} and {key_id_name} are required"
        )
    return raw.encode("utf-8"), key_id


def main(argv: Sequence[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Authorize an emergency production 5%-to-0% stop")
    commands = parser.add_subparsers(dest="command", required=True)
    evaluate = commands.add_parser("evaluate")
    evaluate.add_argument("--rollout-attestation", type=Path, required=True)
    evaluate.add_argument("--adapter-certification", type=Path, required=True)
    evaluate.add_argument("--activation-receipt", type=Path, required=True)
    evaluate.add_argument("--namespace-id", required=True)
    evaluate.add_argument("--incident-id", required=True)
    evaluate.add_argument("--authorized-by", required=True)
    evaluate.add_argument("--output-dir", type=Path, required=True)
    evaluate.add_argument("--max-age-seconds", type=int, default=300)
    attest = commands.add_parser("attest")
    attest.add_argument("--gate-dir", type=Path, required=True)
    attest.add_argument("--rollout-attestation", type=Path, required=True)
    attest.add_argument("--adapter-certification", type=Path, required=True)
    attest.add_argument("--activation-receipt", type=Path, required=True)
    attest.add_argument("--ttl-seconds", type=int, default=300)
    attest.add_argument("--max-source-age-seconds", type=int, default=300)
    verify = commands.add_parser("verify")
    verify.add_argument("--gate-dir", type=Path, required=True)
    verify.add_argument("--max-age-seconds", type=int, default=300)
    args = parser.parse_args(argv)
    try:
        if args.command == "evaluate":
            rollout_key, rollout_key_id = _key_from_environment(
                ROLLOUT_KEY_ENV, ROLLOUT_KEY_ID_ENV
            )
            certification_key, certification_key_id = _key_from_environment(
                CERTIFICATION_KEY_ENV, CERTIFICATION_KEY_ID_ENV
            )
            report = evaluate_emergency_gate(
                _read_json(args.rollout_attestation),
                _read_json(args.adapter_certification),
                _read_json(args.activation_receipt),
                production_namespace_sha256(args.namespace_id),
                args.incident_id,
                args.authorized_by,
                rollout_key=rollout_key,
                rollout_key_id=rollout_key_id,
                certification_key=certification_key,
                certification_key_id=certification_key_id,
                max_age_seconds=args.max_age_seconds,
            )
            write_emergency_gate(args.output_dir, report)
            print(json.dumps({"decision": report["decision"], "gate_id": report["gate_id"]}))
            return 0 if report["decision"] == "pass" else 3
        key, key_id = _key_from_environment(KEY_ENV, KEY_ID_ENV)
        if args.command == "attest":
            rollout_key, _rollout_key_id = _key_from_environment(
                ROLLOUT_KEY_ENV, ROLLOUT_KEY_ID_ENV
            )
            certification_key, _certification_key_id = _key_from_environment(
                CERTIFICATION_KEY_ENV, CERTIFICATION_KEY_ID_ENV
            )
            if key in {rollout_key, certification_key}:
                raise DirectTrafficProductionEmergencyGateConfigError(
                    "emergency Gate key must be distinct from upstream proof keys"
                )
            value = attest_emergency_gate(
                args.gate_dir,
                key,
                key_id,
                rollout_attestation=_read_json(args.rollout_attestation),
                adapter_certification=_read_json(args.adapter_certification),
                activation_receipt=_read_json(args.activation_receipt),
                rollout_key=rollout_key,
                rollout_key_id=_rollout_key_id,
                certification_key=certification_key,
                certification_key_id=_certification_key_id,
                ttl_seconds=args.ttl_seconds,
                max_source_age_seconds=args.max_source_age_seconds,
            )
        else:
            value = verify_emergency_gate_attestation(
                args.gate_dir, key, key_id, max_age_seconds=args.max_age_seconds
            )
        print(json.dumps({"gate_id": value["gate_id"], "decision": value["decision"]}))
        return 0
    except (DirectTrafficProductionEmergencyGateError, DeploymentResolutionError, OSError) as exc:
        print(f"production emergency Gate failed: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
