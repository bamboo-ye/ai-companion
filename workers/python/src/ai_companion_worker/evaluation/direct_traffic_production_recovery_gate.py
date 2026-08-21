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
    PRODUCTION_RECOVERY_KEY_ENV as ROLLOUT_KEY_ENV,
    PRODUCTION_RECOVERY_KEY_ID_ENV as ROLLOUT_KEY_ID_ENV,
    DirectRolloutError,
    _validate_attestation as _validate_rollout_attestation,
    _validate_report as _validate_rollout_report,
)
from .direct_traffic_certification import (
    PRODUCTION_RECOVERY_KEY_ENV as CERTIFICATION_KEY_ENV,
    PRODUCTION_RECOVERY_KEY_ID_ENV as CERTIFICATION_KEY_ID_ENV,
    DirectTrafficCertificationError,
    _validate_certification,
)
from .direct_traffic_production_gate import production_namespace_sha256
from .direct_traffic_shadow_gate import (
    PRODUCTION_RECOVERY_KEY_ENV as SHADOW_KEY_ENV,
    PRODUCTION_RECOVERY_KEY_ID_ENV as SHADOW_KEY_ID_ENV,
    DirectTrafficShadowGateError,
    _validate_attestation as _validate_shadow_attestation,
)
from .release_attestation import MAX_CLOCK_SKEW_SECONDS


GATE_SCHEMA_VERSION = "agent-direct-traffic-production-recovery-gate-v1"
ATTESTATION_SCHEMA_VERSION = (
    "agent-direct-traffic-production-recovery-gate-attestation-v1"
)
GATE_FILE = "direct-traffic-production-recovery-gate.json"
ATTESTATION_FILE = "direct-traffic-production-recovery-gate-attestation.json"
KEY_ENV = "OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_RECOVERY_GATE_KEY"
KEY_ID_ENV = "OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_RECOVERY_GATE_KEY_ID"
MAX_ATTESTATION_SECONDS = 15 * 60
DEFAULT_MINIMUM_COOLDOWN_SECONDS = 10 * 60
SIGNING_DOMAIN = b"ai-companion/direct-traffic-production-recovery-gate/v1\0"
_DIGEST_RE = re.compile(r"[0-9a-f]{64}")
_GATE_ID_RE = re.compile(r"direct-traffic-production-recovery-gate-[0-9a-f]{32}")
_ATTESTATION_ID_RE = re.compile(
    r"direct-traffic-production-recovery-gate-attestation-[0-9a-f]{32}"
)


class DirectTrafficProductionRecoveryGateError(ValueError):
    pass


class DirectTrafficProductionRecoveryGateConfigError(
    DirectTrafficProductionRecoveryGateError
):
    pass


class DirectTrafficProductionRecoveryGateVerificationError(
    DirectTrafficProductionRecoveryGateError
):
    pass


def evaluate_recovery_gate(
    rollout_attestation: Mapping[str, Any],
    rollout_report: Mapping[str, Any],
    adapter_certification: Mapping[str, Any],
    shadow_attestation: Mapping[str, Any],
    rollback_receipt: Mapping[str, Any],
    namespace_sha256: str,
    *,
    rollout_key: bytes,
    rollout_key_id: str,
    certification_key: bytes,
    certification_key_id: str,
    shadow_key: bytes,
    shadow_key_id: str,
    minimum_cooldown_seconds: int = DEFAULT_MINIMUM_COOLDOWN_SECONDS,
    max_age_seconds: int = 900,
    now: datetime | None = None,
) -> dict[str, Any]:
    current_time = _normalize_now(now)
    maximum_age = _validate_age(max_age_seconds)
    minimum_cooldown = _validate_cooldown(minimum_cooldown_seconds)
    namespace = _validate_digest(namespace_sha256, "production namespace")
    if len({rollout_key, certification_key, shadow_key}) != 3:
        raise DirectTrafficProductionRecoveryGateConfigError(
            "recovery upstream proof keys must be distinct"
        )
    try:
        rollout = _validate_rollout_attestation(rollout_attestation)
        report = _validate_rollout_report(rollout_report)
        certification = _validate_certification(adapter_certification)
        shadow = _validate_shadow_attestation(shadow_attestation)
    except (
        DirectRolloutError,
        DirectTrafficCertificationError,
        DirectTrafficShadowGateError,
    ) as exc:
        raise DirectTrafficProductionRecoveryGateVerificationError(
            "recovery upstream proof contract is invalid"
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
    _verify_proof(
        shadow,
        shadow_key,
        shadow_key_id,
        b"ai-companion/direct-traffic-shadow-attestation/v1\0",
        "created_at",
        maximum_age,
        current_time,
        "shadow",
    )
    receipt = validate_rollback_receipt(rollback_receipt)
    rollback_time = _parse_timestamp(str(receipt["applied_at"]), "rollback applied_at")
    rollout_time = _parse_timestamp(str(rollout["created_at"]), "rollout created_at")
    shadow_time = _parse_timestamp(str(shadow["created_at"]), "shadow created_at")
    cooldown_seconds = max(0, int((rollout_time - rollback_time).total_seconds()))
    bindings = {
        "environment_sha256": rollout.get("environment_sha256"),
        "provider_instance_sha256": certification.get("provider_instance_sha256"),
        "namespace_sha256": namespace,
        "observer_instance_sha256": shadow.get("observer_instance_sha256"),
    }
    for field, value in bindings.items():
        _validate_digest(value, field)
    violations: list[str] = []
    rollout_report_sha256 = _digest(_canonical_json(report))
    if rollout.get("report_sha256") != rollout_report_sha256:
        violations.append("rollout_report_binding_invalid")
    if (
        report.get("decision_id") != rollout.get("decision_id")
        or report.get("decision") != rollout.get("decision")
        or report.get("current_traffic_percent")
        != rollout.get("current_traffic_percent")
        or report.get("target_traffic_percent")
        != rollout.get("target_traffic_percent")
    ):
        violations.append("rollout_report_identity_mismatch")
    evidence = report.get("evidence")
    if not isinstance(evidence, list) or not evidence:
        violations.append("recovery_health_window_missing")
    else:
        try:
            evidence_times = [
                _parse_timestamp(
                    str(item["observed_at"]), "recovery evidence observed_at"
                )
                for item in evidence
                if isinstance(item, Mapping)
            ]
        except (KeyError, ValueError) as exc:
            raise DirectTrafficProductionRecoveryGateVerificationError(
                "recovery health evidence time is invalid"
            ) from exc
        if len(evidence_times) != len(evidence) or any(
            observed <= rollback_time for observed in evidence_times
        ):
            violations.append("health_window_predates_rollback")
    if rollout.get("decision") != "expand":
        violations.append("rollout_not_expand")
    if rollout.get("current_traffic_percent") != 0:
        violations.append("recovery_stage_not_zero")
    if rollout.get("target_traffic_percent") != 5:
        violations.append("recovery_target_not_five")
    if certification.get("adapter") != "https-direct-traffic-production-recovery":
        violations.append("wrong_recovery_adapter")
    if certification.get("implementation_version") != "1.0.0":
        violations.append("wrong_recovery_adapter_version")
    if shadow.get("decision") != "pass":
        violations.append("recovery_shadow_not_passed")
    if receipt.get("current_traffic_percent") != 0 or receipt.get("revision") != 2:
        violations.append("rollback_receipt_not_zero_revision_two")
    if receipt.get("namespace_sha256") != namespace:
        violations.append("rollback_namespace_mismatch")
    if cooldown_seconds < minimum_cooldown:
        violations.append("recovery_cooldown_not_elapsed")
    if shadow_time <= rollback_time:
        violations.append("shadow_evidence_predates_rollback")
    for field in ("environment_sha256", "provider_instance_sha256"):
        expected = bindings[field]
        if any(
            proof.get(field) != expected
            for proof in (certification, shadow, rollout)
            if field in proof
        ):
            violations.append(f"{field}_mismatch")
    source = {
        "rollout_attestation_sha256": _digest(_canonical_json(rollout)),
        "rollout_report_sha256": rollout_report_sha256,
        "adapter_certification_sha256": _digest(_canonical_json(certification)),
        "shadow_attestation_sha256": _digest(_canonical_json(shadow)),
        "rollback_receipt_sha256": _digest(_canonical_json(receipt)),
    }
    seed = {
        "generated_at": _format_timestamp(current_time),
        **bindings,
        "current_traffic_percent": 0,
        "target_traffic_percent": 5,
        "cooldown_seconds": cooldown_seconds,
        "minimum_cooldown_seconds": minimum_cooldown,
        "source": source,
        "violations": sorted(set(violations)),
    }
    report = {
        "schema_version": GATE_SCHEMA_VERSION,
        "gate_id": (
            "direct-traffic-production-recovery-gate-"
            f"{_digest(_canonical_json(seed))[:32]}"
        ),
        "decision": "pass" if not violations else "hold",
        **seed,
    }
    return _validate_gate(report)


def write_recovery_gate(output_dir: Path, report: Mapping[str, Any]) -> Path:
    validated = _validate_gate(report)
    try:
        _ensure_private_state_directory(output_dir.parent)
        _ensure_private_state_directory(output_dir)
        if list(os.scandir(output_dir)):
            raise DirectTrafficProductionRecoveryGateVerificationError(
                "recovery Gate directory must be empty"
            )
        _write_exclusive_private_json(output_dir / GATE_FILE, validated)
    except (DeploymentResolutionError, OSError) as exc:
        raise DirectTrafficProductionRecoveryGateVerificationError(
            "cannot write private recovery Gate"
        ) from exc
    return output_dir / GATE_FILE


def attest_recovery_gate(
    gate_dir: Path,
    key: bytes,
    key_id: str,
    *,
    rollout_attestation: Mapping[str, Any],
    rollout_report: Mapping[str, Any],
    adapter_certification: Mapping[str, Any],
    shadow_attestation: Mapping[str, Any],
    rollback_receipt: Mapping[str, Any],
    rollout_key: bytes,
    rollout_key_id: str,
    certification_key: bytes,
    certification_key_id: str,
    shadow_key: bytes,
    shadow_key_id: str,
    ttl_seconds: int = 300,
    max_source_age_seconds: int = 900,
    now: datetime | None = None,
) -> dict[str, Any]:
    signing_key = _validate_key(key, "recovery Gate key")
    signing_key_id = _validate_key_id(key_id, "recovery Gate key_id")
    ttl = _validate_age(ttl_seconds)
    current_time = _normalize_now(now)
    report = _read_gate(gate_dir, expect_attestation=False)
    if signing_key in {rollout_key, certification_key, shadow_key}:
        raise DirectTrafficProductionRecoveryGateConfigError(
            "recovery Gate key must be distinct from upstream proof keys"
        )
    generated_at = _parse_timestamp(str(report["generated_at"]), "recovery generated_at")
    if generated_at > current_time + timedelta(seconds=MAX_CLOCK_SKEW_SECONDS):
        raise DirectTrafficProductionRecoveryGateVerificationError(
            "recovery Gate is from the future"
        )
    recomputed = evaluate_recovery_gate(
        rollout_attestation,
        rollout_report,
        adapter_certification,
        shadow_attestation,
        rollback_receipt,
        str(report["namespace_sha256"]),
        rollout_key=rollout_key,
        rollout_key_id=rollout_key_id,
        certification_key=certification_key,
        certification_key_id=certification_key_id,
        shadow_key=shadow_key,
        shadow_key_id=shadow_key_id,
        minimum_cooldown_seconds=int(report["minimum_cooldown_seconds"]),
        max_age_seconds=max_source_age_seconds,
        now=current_time,
    )
    replay_fields = set(report) - {"generated_at", "gate_id", "cooldown_seconds"}
    if any(report[field] != recomputed[field] for field in replay_fields):
        raise DirectTrafficProductionRecoveryGateVerificationError(
            "recovery Gate source replay does not match the report"
        )
    if report["decision"] != "pass":
        raise DirectTrafficProductionRecoveryGateVerificationError(
            "only a passing recovery Gate can be attested"
        )
    payload: dict[str, Any] = {
        "schema_version": ATTESTATION_SCHEMA_VERSION,
        "attestation_id": "",
        "created_at": _format_timestamp(current_time),
        "expires_at": _format_timestamp(current_time + timedelta(seconds=ttl)),
        "gate_id": report["gate_id"],
        "decision": "pass",
        "environment_sha256": report["environment_sha256"],
        "provider_instance_sha256": report["provider_instance_sha256"],
        "namespace_sha256": report["namespace_sha256"],
        "observer_instance_sha256": report["observer_instance_sha256"],
        "current_traffic_percent": 0,
        "target_traffic_percent": 5,
        "cooldown_seconds": report["cooldown_seconds"],
        "minimum_cooldown_seconds": report["minimum_cooldown_seconds"],
        "gate_report_sha256": _digest(_canonical_json(report)),
        **report["source"],
        "key_id": signing_key_id,
        "algorithm": "hmac-sha256",
    }
    identity = {field: item for field, item in payload.items() if field != "attestation_id"}
    payload["attestation_id"] = (
        "direct-traffic-production-recovery-gate-attestation-"
        f"{_digest(_canonical_json(identity))[:32]}"
    )
    result = {**payload, "signature": _sign(signing_key, payload)}
    validated = _validate_attestation(result)
    try:
        _write_exclusive_private_json(gate_dir / ATTESTATION_FILE, validated)
    except FileExistsError:
        return verify_recovery_gate_attestation(
            gate_dir, signing_key, signing_key_id, now=current_time
        )
    return validated


def verify_recovery_gate_attestation(
    gate_dir: Path,
    key: bytes,
    key_id: str,
    *,
    max_age_seconds: int = 900,
    now: datetime | None = None,
) -> dict[str, Any]:
    verification_key = _validate_key(key, "recovery Gate key")
    trusted_key_id = _validate_key_id(key_id, "recovery Gate key_id")
    maximum_age = _validate_age(max_age_seconds)
    current_time = _normalize_now(now)
    report = _read_gate(gate_dir, expect_attestation=True)
    attestation = _read_contract(gate_dir / ATTESTATION_FILE, _validate_attestation)
    _verify_signed_attestation(attestation, verification_key, trusted_key_id, current_time)
    expected = {
        "gate_id": report["gate_id"],
        "gate_report_sha256": _digest(_canonical_json(report)),
        "cooldown_seconds": report["cooldown_seconds"],
        "minimum_cooldown_seconds": report["minimum_cooldown_seconds"],
        **{field: report[field] for field in (
            "environment_sha256", "provider_instance_sha256", "namespace_sha256",
            "observer_instance_sha256",
        )},
        **report["source"],
    }
    if any(attestation.get(field) != item for field, item in expected.items()):
        raise DirectTrafficProductionRecoveryGateVerificationError(
            "recovery Gate attestation binding is invalid"
        )
    generated_at = _parse_timestamp(str(report["generated_at"]), "recovery generated_at")
    if current_time - generated_at > timedelta(seconds=maximum_age):
        raise DirectTrafficProductionRecoveryGateVerificationError(
            "recovery Gate is stale"
        )
    return attestation


def verify_embedded_recovery_gate_attestation(
    value: Any,
    key: bytes,
    key_id: str,
    *,
    environment_sha256: str,
    provider_instance_sha256: str,
    namespace_sha256: str,
    observer_instance_sha256: str,
    rollout_attestation_sha256: str,
    rollout_report_sha256: str,
    adapter_certification_sha256: str,
    shadow_attestation_sha256: str,
    rollback_receipt_sha256: str,
    now: datetime | None = None,
) -> dict[str, Any]:
    current_time = _normalize_now(now)
    attestation = _validate_attestation(value)
    _verify_signed_attestation(
        attestation,
        _validate_key(key, "recovery Gate key"),
        _validate_key_id(key_id, "recovery Gate key_id"),
        current_time,
    )
    expected = {
        "environment_sha256": environment_sha256,
        "provider_instance_sha256": provider_instance_sha256,
        "namespace_sha256": namespace_sha256,
        "observer_instance_sha256": observer_instance_sha256,
        "rollout_attestation_sha256": rollout_attestation_sha256,
        "rollout_report_sha256": rollout_report_sha256,
        "adapter_certification_sha256": adapter_certification_sha256,
        "shadow_attestation_sha256": shadow_attestation_sha256,
        "rollback_receipt_sha256": rollback_receipt_sha256,
        "current_traffic_percent": 0,
        "target_traffic_percent": 5,
        "decision": "pass",
    }
    if any(attestation.get(field) != item for field, item in expected.items()):
        raise DirectTrafficProductionRecoveryGateVerificationError(
            "embedded recovery Gate binding is invalid"
        )
    return attestation


def validate_rollback_receipt(value: Any) -> dict[str, Any]:
    fields = {
        "schema_version", "namespace_sha256", "operation_id", "decision_id",
        "outcome", "current_traffic_percent", "revision", "history_chain_sha256",
        "adapter_certification_id", "emergency_gate_attestation_id", "incident_id",
        "remote_reused", "local_reused", "applied_at",
    }
    if not isinstance(value, Mapping) or set(value) != fields:
        raise DirectTrafficProductionRecoveryGateConfigError(
            "rollback receipt contract is invalid"
        )
    result = dict(value)
    if (
        result["schema_version"]
        != "agent-direct-traffic-production-emergency-receipt-v1"
        or result["current_traffic_percent"] != 0
        or result["revision"] != 2
        or result["outcome"] not in {"applied", "no_op"}
    ):
        raise DirectTrafficProductionRecoveryGateConfigError(
            "rollback receipt state is invalid"
        )
    for field in ("namespace_sha256", "history_chain_sha256"):
        _validate_digest(result[field], field)
    _parse_timestamp(str(result["applied_at"]), "rollback applied_at")
    return result


def _validate_gate(value: Any) -> dict[str, Any]:
    fields = {
        "schema_version", "gate_id", "generated_at", "decision",
        "environment_sha256", "provider_instance_sha256", "namespace_sha256",
        "observer_instance_sha256", "current_traffic_percent",
        "target_traffic_percent", "cooldown_seconds", "minimum_cooldown_seconds",
        "source", "violations",
    }
    if not isinstance(value, Mapping) or set(value) != fields:
        raise DirectTrafficProductionRecoveryGateVerificationError(
            "recovery Gate contract is invalid"
        )
    result = dict(value)
    if (
        result["schema_version"] != GATE_SCHEMA_VERSION
        or _GATE_ID_RE.fullmatch(str(result["gate_id"])) is None
        or result["decision"] not in {"pass", "hold"}
        or result["current_traffic_percent"] != 0
        or result["target_traffic_percent"] != 5
    ):
        raise DirectTrafficProductionRecoveryGateVerificationError(
            "recovery Gate identity is invalid"
        )
    _parse_timestamp(str(result["generated_at"]), "recovery generated_at")
    for field in (
        "environment_sha256", "provider_instance_sha256", "namespace_sha256",
        "observer_instance_sha256",
    ):
        _validate_digest(result[field], field)
    if (
        isinstance(result["cooldown_seconds"], bool)
        or not isinstance(result["cooldown_seconds"], int)
        or result["cooldown_seconds"] < 0
        or _validate_cooldown(result["minimum_cooldown_seconds"])
        != result["minimum_cooldown_seconds"]
    ):
        raise DirectTrafficProductionRecoveryGateVerificationError(
            "recovery Gate cooldown is invalid"
        )
    source_fields = {
        "rollout_attestation_sha256", "rollout_report_sha256",
        "adapter_certification_sha256",
        "shadow_attestation_sha256", "rollback_receipt_sha256",
    }
    source = result["source"]
    if not isinstance(source, Mapping) or set(source) != source_fields:
        raise DirectTrafficProductionRecoveryGateVerificationError(
            "recovery Gate source is invalid"
        )
    for field in source_fields:
        _validate_digest(source[field], field)
    violations = result["violations"]
    if (
        not isinstance(violations, list)
        or violations != sorted(set(violations))
        or any(not isinstance(item, str) or not item for item in violations)
        or (result["decision"] == "pass") != (not violations)
        or (
            result["decision"] == "pass"
            and result["cooldown_seconds"] < result["minimum_cooldown_seconds"]
        )
    ):
        raise DirectTrafficProductionRecoveryGateVerificationError(
            "recovery Gate violations are invalid"
        )
    seed = {
        field: result[field]
        for field in fields
        if field not in {"schema_version", "gate_id", "decision"}
    }
    expected_id = (
        "direct-traffic-production-recovery-gate-"
        f"{_digest(_canonical_json(seed))[:32]}"
    )
    if result["gate_id"] != expected_id:
        raise DirectTrafficProductionRecoveryGateVerificationError(
            "recovery Gate ID binding is invalid"
        )
    return result


def _validate_attestation(value: Any) -> dict[str, Any]:
    fields = {
        "schema_version", "attestation_id", "created_at", "expires_at", "gate_id",
        "decision", "environment_sha256", "provider_instance_sha256",
        "namespace_sha256", "observer_instance_sha256", "current_traffic_percent",
        "target_traffic_percent", "cooldown_seconds", "minimum_cooldown_seconds",
        "gate_report_sha256", "rollout_attestation_sha256",
        "rollout_report_sha256",
        "adapter_certification_sha256", "shadow_attestation_sha256",
        "rollback_receipt_sha256", "key_id", "algorithm", "signature",
    }
    if not isinstance(value, Mapping) or set(value) != fields:
        raise DirectTrafficProductionRecoveryGateVerificationError(
            "recovery Gate attestation contract is invalid"
        )
    result = dict(value)
    cooldown = _validate_cooldown_value(result.get("cooldown_seconds"), allow_zero=True)
    minimum_cooldown = _validate_cooldown(result.get("minimum_cooldown_seconds"))
    if (
        result["schema_version"] != ATTESTATION_SCHEMA_VERSION
        or _ATTESTATION_ID_RE.fullmatch(str(result["attestation_id"])) is None
        or _GATE_ID_RE.fullmatch(str(result["gate_id"])) is None
        or result["decision"] != "pass"
        or result["current_traffic_percent"] != 0
        or result["target_traffic_percent"] != 5
        or result["algorithm"] != "hmac-sha256"
        or cooldown < minimum_cooldown
    ):
        raise DirectTrafficProductionRecoveryGateVerificationError(
            "recovery Gate attestation identity is invalid"
        )
    for field in ("created_at", "expires_at"):
        _parse_timestamp(str(result[field]), f"recovery {field}")
    _validate_key_id(result["key_id"], "recovery Gate key_id")
    for field in (
        "environment_sha256", "provider_instance_sha256", "namespace_sha256",
        "observer_instance_sha256", "gate_report_sha256",
        "rollout_attestation_sha256", "adapter_certification_sha256",
        "rollout_report_sha256",
        "shadow_attestation_sha256", "rollback_receipt_sha256", "signature",
    ):
        _validate_digest(result[field], field)
    identity = {
        field: item
        for field, item in result.items()
        if field not in {"attestation_id", "signature"}
    }
    expected_id = (
        "direct-traffic-production-recovery-gate-attestation-"
        f"{_digest(_canonical_json(identity))[:32]}"
    )
    if result["attestation_id"] != expected_id:
        raise DirectTrafficProductionRecoveryGateVerificationError(
            "recovery Gate attestation ID binding is invalid"
        )
    return result


def _verify_signed_attestation(
    artifact: Mapping[str, Any], key: bytes, key_id: str, now: datetime
) -> None:
    unsigned = {field: item for field, item in artifact.items() if field != "signature"}
    if artifact.get("key_id") != key_id or not hmac.compare_digest(
        str(artifact.get("signature")), _sign(key, unsigned)
    ):
        raise DirectTrafficProductionRecoveryGateVerificationError(
            "recovery Gate signature is invalid"
        )
    _verify_lifetime(artifact, "created_at", MAX_ATTESTATION_SECONDS, now)


def _verify_proof(
    artifact: Mapping[str, Any],
    key: bytes,
    key_id: str,
    domain: bytes,
    created_field: str,
    max_age_seconds: int,
    now: datetime,
    label: str,
) -> None:
    trusted_key = _validate_key(key, f"recovery {label} key")
    trusted_key_id = _validate_key_id(key_id, f"recovery {label} key_id")
    unsigned = {field: item for field, item in artifact.items() if field != "signature"}
    derived = hmac.new(trusted_key, domain, hashlib.sha256).digest()
    expected = hmac.new(derived, _canonical_json(unsigned), hashlib.sha256).hexdigest()
    if artifact.get("key_id") != trusted_key_id or not hmac.compare_digest(
        str(artifact.get("signature")), expected
    ):
        raise DirectTrafficProductionRecoveryGateVerificationError(
            f"recovery {label} signature is invalid"
        )
    _verify_lifetime(artifact, created_field, max_age_seconds, now)


def _verify_lifetime(
    artifact: Mapping[str, Any], created_field: str, max_age_seconds: int, now: datetime
) -> None:
    try:
        created = _parse_timestamp(str(artifact[created_field]), created_field)
        expires = _parse_timestamp(str(artifact["expires_at"]), "expires_at")
    except (KeyError, ValueError) as exc:
        raise DirectTrafficProductionRecoveryGateVerificationError(
            "recovery proof lifetime is invalid"
        ) from exc
    if (
        created > now + timedelta(seconds=MAX_CLOCK_SKEW_SECONDS)
        or expires <= created
        or expires - created > timedelta(seconds=24 * 60 * 60)
        or now >= expires
        or now - created > timedelta(seconds=max_age_seconds)
    ):
        raise DirectTrafficProductionRecoveryGateVerificationError(
            "recovery proof is stale"
        )


def _read_gate(gate_dir: Path, *, expect_attestation: bool) -> dict[str, Any]:
    expected = {GATE_FILE, ATTESTATION_FILE} if expect_attestation else {GATE_FILE}
    try:
        if {entry.name for entry in os.scandir(gate_dir)} != expected:
            raise DirectTrafficProductionRecoveryGateVerificationError(
                "recovery Gate artifact set is invalid"
            )
        return _read_contract(gate_dir / GATE_FILE, _validate_gate)
    except (DeploymentResolutionError, OSError) as exc:
        raise DirectTrafficProductionRecoveryGateVerificationError(
            "cannot read recovery Gate"
        ) from exc


def _read_json(path: Path) -> dict[str, Any]:
    try:
        return _read_contract(
            path,
            lambda value: dict(value) if isinstance(value, Mapping) else _invalid_json(),
        )
    except (DeploymentResolutionError, OSError) as exc:
        raise DirectTrafficProductionRecoveryGateConfigError(
            f"cannot read {path.name}"
        ) from exc


def _invalid_json() -> dict[str, Any]:
    raise DirectTrafficProductionRecoveryGateConfigError("JSON contract is invalid")


def _validate_digest(value: Any, label: str) -> str:
    if not isinstance(value, str) or _DIGEST_RE.fullmatch(value) is None:
        raise DirectTrafficProductionRecoveryGateConfigError(f"{label} is invalid")
    return value


def _validate_age(value: Any) -> int:
    if isinstance(value, bool) or not isinstance(value, int) or not 1 <= value <= 24 * 60 * 60:
        raise DirectTrafficProductionRecoveryGateConfigError("recovery age is invalid")
    return value


def _validate_cooldown(value: Any) -> int:
    if isinstance(value, bool) or not isinstance(value, int) or not 60 <= value <= 24 * 60 * 60:
        raise DirectTrafficProductionRecoveryGateConfigError(
            "recovery cooldown is invalid"
        )
    return value


def _validate_cooldown_value(value: Any, *, allow_zero: bool) -> int:
    minimum = 0 if allow_zero else 60
    if (
        isinstance(value, bool)
        or not isinstance(value, int)
        or not minimum <= value <= 7 * 24 * 60 * 60
    ):
        raise DirectTrafficProductionRecoveryGateConfigError(
            "recovery elapsed cooldown is invalid"
        )
    return value


def _sign(key: bytes, payload: Mapping[str, Any]) -> str:
    derived = hmac.new(key, SIGNING_DOMAIN, hashlib.sha256).digest()
    return hmac.new(derived, _canonical_json(payload), hashlib.sha256).hexdigest()


def _key_from_environment(name: str, key_id_name: str) -> tuple[bytes, str]:
    raw = os.environ.get(name)
    key_id = os.environ.get(key_id_name)
    if raw is None or key_id is None:
        raise DirectTrafficProductionRecoveryGateConfigError(
            f"{name} and {key_id_name} are required"
        )
    return raw.encode("utf-8"), key_id


def main(argv: Sequence[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        description="Authorize production recovery from 0% back to the 5% canary"
    )
    commands = parser.add_subparsers(dest="command", required=True)
    evaluate = commands.add_parser("evaluate")
    attest = commands.add_parser("attest")
    verify = commands.add_parser("verify")
    for command in (evaluate, attest):
        command.add_argument("--rollout-attestation", type=Path, required=True)
        command.add_argument("--rollout-report", type=Path, required=True)
        command.add_argument("--adapter-certification", type=Path, required=True)
        command.add_argument("--shadow-attestation", type=Path, required=True)
        command.add_argument("--rollback-receipt", type=Path, required=True)
    evaluate.add_argument("--namespace-id", required=True)
    evaluate.add_argument("--output-dir", type=Path, required=True)
    evaluate.add_argument(
        "--minimum-cooldown-seconds", type=int, default=DEFAULT_MINIMUM_COOLDOWN_SECONDS
    )
    evaluate.add_argument("--max-age-seconds", type=int, default=900)
    attest.add_argument("--gate-dir", type=Path, required=True)
    attest.add_argument("--ttl-seconds", type=int, default=300)
    attest.add_argument("--max-source-age-seconds", type=int, default=900)
    verify.add_argument("--gate-dir", type=Path, required=True)
    verify.add_argument("--max-age-seconds", type=int, default=900)
    args = parser.parse_args(argv)
    try:
        rollout_key, rollout_key_id = _key_from_environment(
            ROLLOUT_KEY_ENV, ROLLOUT_KEY_ID_ENV
        )
        certification_key, certification_key_id = _key_from_environment(
            CERTIFICATION_KEY_ENV, CERTIFICATION_KEY_ID_ENV
        )
        shadow_key, shadow_key_id = _key_from_environment(SHADOW_KEY_ENV, SHADOW_KEY_ID_ENV)
        if args.command == "evaluate":
            report = evaluate_recovery_gate(
                _read_json(args.rollout_attestation),
                _read_json(args.rollout_report),
                _read_json(args.adapter_certification),
                _read_json(args.shadow_attestation),
                _read_json(args.rollback_receipt),
                production_namespace_sha256(args.namespace_id),
                rollout_key=rollout_key,
                rollout_key_id=rollout_key_id,
                certification_key=certification_key,
                certification_key_id=certification_key_id,
                shadow_key=shadow_key,
                shadow_key_id=shadow_key_id,
                minimum_cooldown_seconds=args.minimum_cooldown_seconds,
                max_age_seconds=args.max_age_seconds,
            )
            write_recovery_gate(args.output_dir, report)
            print(json.dumps({"decision": report["decision"], "gate_id": report["gate_id"]}))
            return 0 if report["decision"] == "pass" else 3
        gate_key, gate_key_id = _key_from_environment(KEY_ENV, KEY_ID_ENV)
        if args.command == "attest":
            value = attest_recovery_gate(
                args.gate_dir,
                gate_key,
                gate_key_id,
                rollout_attestation=_read_json(args.rollout_attestation),
                rollout_report=_read_json(args.rollout_report),
                adapter_certification=_read_json(args.adapter_certification),
                shadow_attestation=_read_json(args.shadow_attestation),
                rollback_receipt=_read_json(args.rollback_receipt),
                rollout_key=rollout_key,
                rollout_key_id=rollout_key_id,
                certification_key=certification_key,
                certification_key_id=certification_key_id,
                shadow_key=shadow_key,
                shadow_key_id=shadow_key_id,
                ttl_seconds=args.ttl_seconds,
                max_source_age_seconds=args.max_source_age_seconds,
            )
        else:
            value = verify_recovery_gate_attestation(
                args.gate_dir,
                gate_key,
                gate_key_id,
                max_age_seconds=args.max_age_seconds,
            )
        print(json.dumps({"gate_id": value["gate_id"], "decision": value["decision"]}))
        return 0
    except (DirectTrafficProductionRecoveryGateError, DeploymentResolutionError, OSError) as exc:
        print(f"production recovery Gate failed: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
