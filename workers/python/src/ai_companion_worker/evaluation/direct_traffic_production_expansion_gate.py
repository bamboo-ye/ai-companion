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
    PRODUCTION_EXPANSION_KEY_ENV as ROLLOUT_KEY_ENV,
    PRODUCTION_EXPANSION_KEY_ID_ENV as ROLLOUT_KEY_ID_ENV,
    DirectRolloutError,
    _validate_attestation as _validate_rollout_attestation,
    _validate_report as _validate_rollout_report,
)
from .direct_traffic_certification import (
    PRODUCTION_EXPANSION_KEY_ENV as CERTIFICATION_KEY_ENV,
    PRODUCTION_EXPANSION_KEY_ID_ENV as CERTIFICATION_KEY_ID_ENV,
    DirectTrafficCertificationError,
    _validate_certification,
)
from .direct_traffic_shadow_gate import (
    PRODUCTION_EXPANSION_KEY_ENV as SHADOW_KEY_ENV,
    PRODUCTION_EXPANSION_KEY_ID_ENV as SHADOW_KEY_ID_ENV,
    DirectTrafficShadowGateError,
    _validate_attestation as _validate_shadow_attestation,
    _validate_gate_report as _validate_shadow_gate_report,
)
from .direct_traffic_production_gate import production_namespace_sha256
from .release_attestation import MAX_CLOCK_SKEW_SECONDS


GATE_SCHEMA_VERSION = "agent-direct-traffic-production-expansion-10-gate-v1"
ATTESTATION_SCHEMA_VERSION = (
    "agent-direct-traffic-production-expansion-10-gate-attestation-v1"
)
GATE_FILE = "direct-traffic-production-expansion-10-gate.json"
ATTESTATION_FILE = "direct-traffic-production-expansion-10-gate-attestation.json"
KEY_ENV = "OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_GATE_KEY"
KEY_ID_ENV = "OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_GATE_KEY_ID"
SIGNING_DOMAIN = b"ai-companion/direct-traffic-production-expansion-10-gate/v1\0"
MAX_ATTESTATION_SECONDS = 15 * 60
DEFAULT_MINIMUM_HEALTH_WINDOW_SECONDS = 10 * 60
_DIGEST_RE = re.compile(r"[0-9a-f]{64}")
_GATE_ID_RE = re.compile(r"direct-traffic-production-expansion-10-gate-[0-9a-f]{32}")
_ATTESTATION_ID_RE = re.compile(
    r"direct-traffic-production-expansion-10-gate-attestation-[0-9a-f]{32}"
)


class DirectTrafficProductionExpansionGateError(ValueError):
    pass


class DirectTrafficProductionExpansionGateConfigError(
    DirectTrafficProductionExpansionGateError
):
    pass


class DirectTrafficProductionExpansionGateVerificationError(
    DirectTrafficProductionExpansionGateError
):
    pass


def evaluate_expansion_gate(
    rollout_attestation: Mapping[str, Any],
    rollout_report: Mapping[str, Any],
    adapter_certification: Mapping[str, Any],
    shadow_attestation: Mapping[str, Any],
    shadow_gate_report: Mapping[str, Any],
    recovery_receipt: Mapping[str, Any],
    namespace_sha256: str,
    *,
    rollout_key: bytes,
    rollout_key_id: str,
    certification_key: bytes,
    certification_key_id: str,
    shadow_key: bytes,
    shadow_key_id: str,
    minimum_health_window_seconds: int = DEFAULT_MINIMUM_HEALTH_WINDOW_SECONDS,
    max_age_seconds: int = 900,
    now: datetime | None = None,
) -> dict[str, Any]:
    current_time = _normalize_now(now)
    maximum_age = _validate_age(max_age_seconds)
    minimum_window = _validate_health_window(minimum_health_window_seconds)
    namespace = _validate_digest(namespace_sha256, "production namespace")
    if len({rollout_key, certification_key, shadow_key}) != 3:
        raise DirectTrafficProductionExpansionGateConfigError(
            "expansion upstream proof keys must be distinct"
        )
    try:
        rollout = _validate_rollout_attestation(rollout_attestation)
        report = _validate_rollout_report(rollout_report)
        certification = _validate_certification(adapter_certification)
        shadow = _validate_shadow_attestation(shadow_attestation)
        shadow_report = _validate_shadow_gate_report(shadow_gate_report)
    except (
        DirectRolloutError,
        DirectTrafficCertificationError,
        DirectTrafficShadowGateError,
    ) as exc:
        raise DirectTrafficProductionExpansionGateVerificationError(
            "expansion upstream proof contract is invalid"
        ) from exc
    _verify_proof(
        rollout, rollout_key, rollout_key_id,
        b"ai-companion/agent-direct-rollout-attestation/v1", "created_at",
        maximum_age, current_time, "rollout",
    )
    _verify_proof(
        certification, certification_key, certification_key_id,
        b"ai-companion/agent-direct-traffic-adapter-certification/v1", "certified_at",
        maximum_age, current_time, "certification",
    )
    _verify_proof(
        shadow, shadow_key, shadow_key_id,
        b"ai-companion/direct-traffic-shadow-attestation/v1\0", "created_at",
        maximum_age, current_time, "shadow",
    )
    receipt = validate_recovery_receipt(recovery_receipt)
    recovery_time = _parse_timestamp(str(receipt["applied_at"]), "recovery applied_at")
    rollout_time = _parse_timestamp(str(rollout["created_at"]), "rollout created_at")
    shadow_time = _parse_timestamp(str(shadow["created_at"]), "shadow created_at")
    bindings = {
        "environment_sha256": rollout.get("environment_sha256"),
        "provider_instance_sha256": certification.get("provider_instance_sha256"),
        "namespace_sha256": namespace,
        "observer_instance_sha256": shadow.get("observer_instance_sha256"),
    }
    for field, value in bindings.items():
        _validate_digest(value, field)

    violations: list[str] = []
    report_sha256 = _digest(_canonical_json(report))
    if rollout.get("report_sha256") != report_sha256:
        violations.append("rollout_report_binding_invalid")
    if any((
        report.get("decision_id") != rollout.get("decision_id"),
        report.get("decision") != rollout.get("decision"),
        report.get("current_traffic_percent") != rollout.get("current_traffic_percent"),
        report.get("target_traffic_percent") != rollout.get("target_traffic_percent"),
    )):
        violations.append("rollout_report_identity_mismatch")
    if any((
        rollout.get("decision") != "expand",
        rollout.get("current_traffic_percent") != 5,
        rollout.get("target_traffic_percent") != 10,
    )):
        violations.append("expansion_stage_not_five_to_ten")
    if certification.get("adapter") != "https-direct-traffic-production-expansion-10":
        violations.append("wrong_expansion_adapter")
    if certification.get("implementation_version") != "1.0.0":
        violations.append("wrong_expansion_adapter_version")
    shadow_report_sha256 = _digest(_canonical_json(shadow_report))
    if any((
        shadow.get("gate_report_sha256") != shadow_report_sha256,
        shadow.get("gate_id") != shadow_report.get("gate_id"),
        shadow.get("decision") != shadow_report.get("decision"),
    )):
        violations.append("shadow_report_binding_invalid")
    if any((
        certification.get("environment_sha256") != bindings["environment_sha256"],
        shadow.get("environment_sha256") != bindings["environment_sha256"],
        shadow.get("provider_instance_sha256") != bindings["provider_instance_sha256"],
        receipt.get("namespace_sha256") != namespace,
    )):
        violations.append("expansion_identity_mismatch")
    stable = report.get("windows", {}).get("stable", {})
    coverage = stable.get("coverage_seconds") if isinstance(stable, Mapping) else None
    if isinstance(coverage, bool) or not isinstance(coverage, (int, float)):
        violations.append("health_window_missing")
        coverage_seconds = 0
    else:
        coverage_seconds = max(0, int(coverage))
        if coverage_seconds < minimum_window:
            violations.append("health_window_too_short")
    evidence = report.get("evidence")
    if not isinstance(evidence, list) or not evidence:
        violations.append("expansion_health_evidence_missing")
    else:
        try:
            evidence_times = [
                _parse_timestamp(str(item["observed_at"]), "expansion evidence observed_at")
                for item in evidence
                if isinstance(item, Mapping)
            ]
        except (KeyError, ValueError) as exc:
            raise DirectTrafficProductionExpansionGateVerificationError(
                "expansion health evidence time is invalid"
            ) from exc
        if len(evidence_times) != len(evidence) or any(
            observed <= recovery_time for observed in evidence_times
        ):
            violations.append("health_window_predates_recovery")
    if rollout_time <= recovery_time:
        violations.append("rollout_proof_predates_recovery")
    if shadow_time <= recovery_time:
        violations.append("shadow_proof_predates_recovery")
    shadow_stable = shadow_report.get("windows", {}).get("stable", {})
    shadow_generated = _parse_timestamp(
        str(shadow_report["generated_at"]), "shadow Gate generated_at"
    )
    shadow_latest_age = (
        shadow_stable.get("latest_age_seconds")
        if isinstance(shadow_stable, Mapping)
        else None
    )
    shadow_coverage = (
        shadow_stable.get("coverage_seconds")
        if isinstance(shadow_stable, Mapping)
        else None
    )
    if (
        isinstance(shadow_latest_age, bool)
        or not isinstance(shadow_latest_age, (int, float))
        or isinstance(shadow_coverage, bool)
        or not isinstance(shadow_coverage, (int, float))
    ):
        violations.append("shadow_window_missing")
    else:
        shadow_window_start = shadow_generated - timedelta(
            seconds=float(shadow_latest_age) + float(shadow_coverage)
        )
        if shadow_window_start <= recovery_time:
            violations.append("shadow_window_predates_recovery")

    source = {
        "rollout_attestation_sha256": _digest(_canonical_json(rollout)),
        "rollout_report_sha256": report_sha256,
        "adapter_certification_sha256": _digest(_canonical_json(certification)),
        "shadow_attestation_sha256": _digest(_canonical_json(shadow)),
        "shadow_gate_report_sha256": shadow_report_sha256,
        "recovery_receipt_sha256": _digest(_canonical_json(receipt)),
    }
    report_result: dict[str, Any] = {
        "schema_version": GATE_SCHEMA_VERSION,
        "gate_id": "",
        "generated_at": _format_timestamp(current_time),
        "decision": "pass" if not violations else "hold",
        **bindings,
        "current_traffic_percent": 5,
        "target_traffic_percent": 10,
        "health_window_seconds": coverage_seconds,
        "minimum_health_window_seconds": minimum_window,
        "source": source,
        "violations": sorted(set(violations)),
    }
    seed = {
        field: item for field, item in report_result.items()
        if field not in {"schema_version", "gate_id", "decision"}
    }
    report_result["gate_id"] = (
        "direct-traffic-production-expansion-10-gate-"
        f"{_digest(_canonical_json(seed))[:32]}"
    )
    return _validate_gate(report_result)


def write_expansion_gate(output_dir: Path, report: Mapping[str, Any]) -> Path:
    validated = _validate_gate(report)
    try:
        _ensure_private_state_directory(output_dir)
        if any(os.scandir(output_dir)):
            raise DirectTrafficProductionExpansionGateVerificationError(
                "expansion Gate directory must be empty"
            )
        _write_exclusive_private_json(output_dir / GATE_FILE, validated)
    except (DeploymentResolutionError, OSError) as exc:
        raise DirectTrafficProductionExpansionGateVerificationError(
            "cannot write private expansion Gate"
        ) from exc
    return output_dir / GATE_FILE


def attest_expansion_gate(
    gate_dir: Path,
    key: bytes,
    key_id: str,
    *,
    rollout_attestation: Mapping[str, Any],
    rollout_report: Mapping[str, Any],
    adapter_certification: Mapping[str, Any],
    shadow_attestation: Mapping[str, Any],
    shadow_gate_report: Mapping[str, Any],
    recovery_receipt: Mapping[str, Any],
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
    signing_key = _validate_key(key, "expansion Gate key")
    signing_key_id = _validate_key_id(key_id, "expansion Gate key_id")
    ttl = _validate_age(ttl_seconds)
    current_time = _normalize_now(now)
    if signing_key in {rollout_key, certification_key, shadow_key}:
        raise DirectTrafficProductionExpansionGateConfigError(
            "expansion Gate key must be distinct from upstream proof keys"
        )
    report = _read_gate(gate_dir, expect_attestation=False)
    recomputed = evaluate_expansion_gate(
        rollout_attestation, rollout_report, adapter_certification,
        shadow_attestation, shadow_gate_report, recovery_receipt,
        str(report["namespace_sha256"]),
        rollout_key=rollout_key, rollout_key_id=rollout_key_id,
        certification_key=certification_key,
        certification_key_id=certification_key_id,
        shadow_key=shadow_key, shadow_key_id=shadow_key_id,
        minimum_health_window_seconds=int(report["minimum_health_window_seconds"]),
        max_age_seconds=max_source_age_seconds, now=current_time,
    )
    replay_fields = set(report) - {"generated_at", "gate_id"}
    if any(report[field] != recomputed[field] for field in replay_fields):
        raise DirectTrafficProductionExpansionGateVerificationError(
            "expansion Gate source replay does not match the report"
        )
    if report["decision"] != "pass":
        raise DirectTrafficProductionExpansionGateVerificationError(
            "only a passing expansion Gate can be attested"
        )
    payload: dict[str, Any] = {
        "schema_version": ATTESTATION_SCHEMA_VERSION,
        "attestation_id": "",
        "created_at": _format_timestamp(current_time),
        "expires_at": _format_timestamp(current_time + timedelta(seconds=ttl)),
        "gate_id": report["gate_id"],
        "decision": "pass",
        **{field: report[field] for field in (
            "environment_sha256", "provider_instance_sha256", "namespace_sha256",
            "observer_instance_sha256", "current_traffic_percent",
            "target_traffic_percent", "health_window_seconds",
            "minimum_health_window_seconds",
        )},
        "gate_report_sha256": _digest(_canonical_json(report)),
        **report["source"],
        "key_id": signing_key_id,
        "algorithm": "hmac-sha256",
    }
    identity = {field: item for field, item in payload.items() if field != "attestation_id"}
    payload["attestation_id"] = (
        "direct-traffic-production-expansion-10-gate-attestation-"
        f"{_digest(_canonical_json(identity))[:32]}"
    )
    result = _validate_attestation({**payload, "signature": _sign(signing_key, payload)})
    try:
        _write_exclusive_private_json(gate_dir / ATTESTATION_FILE, result)
    except FileExistsError:
        return verify_expansion_gate_attestation(
            gate_dir, signing_key, signing_key_id, now=current_time
        )
    return result


def verify_expansion_gate_attestation(
    gate_dir: Path,
    key: bytes,
    key_id: str,
    *,
    max_age_seconds: int = 900,
    now: datetime | None = None,
) -> dict[str, Any]:
    current_time = _normalize_now(now)
    report = _read_gate(gate_dir, expect_attestation=True)
    attestation = _read_contract(gate_dir / ATTESTATION_FILE, _validate_attestation)
    _verify_signed_attestation(attestation, key, key_id, current_time)
    expected = {
        "gate_id": report["gate_id"],
        "gate_report_sha256": _digest(_canonical_json(report)),
        **{field: report[field] for field in (
            "environment_sha256", "provider_instance_sha256", "namespace_sha256",
            "observer_instance_sha256", "health_window_seconds",
            "minimum_health_window_seconds",
        )},
        **report["source"],
    }
    if any(attestation.get(field) != item for field, item in expected.items()):
        raise DirectTrafficProductionExpansionGateVerificationError(
            "expansion Gate attestation binding is invalid"
        )
    generated = _parse_timestamp(str(report["generated_at"]), "expansion generated_at")
    if current_time - generated > timedelta(seconds=_validate_age(max_age_seconds)):
        raise DirectTrafficProductionExpansionGateVerificationError(
            "expansion Gate is stale"
        )
    return attestation


def verify_embedded_expansion_gate_attestation(
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
    shadow_gate_report_sha256: str,
    recovery_receipt_sha256: str,
    now: datetime | None = None,
) -> dict[str, Any]:
    attestation = _validate_attestation(value)
    _verify_signed_attestation(attestation, key, key_id, _normalize_now(now))
    expected = {
        "environment_sha256": environment_sha256,
        "provider_instance_sha256": provider_instance_sha256,
        "namespace_sha256": namespace_sha256,
        "observer_instance_sha256": observer_instance_sha256,
        "rollout_attestation_sha256": rollout_attestation_sha256,
        "rollout_report_sha256": rollout_report_sha256,
        "adapter_certification_sha256": adapter_certification_sha256,
        "shadow_attestation_sha256": shadow_attestation_sha256,
        "shadow_gate_report_sha256": shadow_gate_report_sha256,
        "recovery_receipt_sha256": recovery_receipt_sha256,
        "current_traffic_percent": 5,
        "target_traffic_percent": 10,
        "decision": "pass",
    }
    if any(attestation.get(field) != item for field, item in expected.items()):
        raise DirectTrafficProductionExpansionGateVerificationError(
            "embedded expansion Gate binding is invalid"
        )
    return attestation


def validate_recovery_receipt(value: Any) -> dict[str, Any]:
    fields = {
        "schema_version", "namespace_sha256", "operation_id", "decision_id",
        "outcome", "current_traffic_percent", "revision", "history_chain_sha256",
        "adapter_certification_id", "shadow_attestation_id",
        "recovery_gate_attestation_id", "remote_reused", "local_reused", "applied_at",
    }
    if not isinstance(value, Mapping) or set(value) != fields:
        raise DirectTrafficProductionExpansionGateConfigError(
            "recovery receipt contract is invalid"
        )
    result = dict(value)
    if any((
        result["schema_version"] != "agent-direct-traffic-production-recovery-receipt-v1",
        result["current_traffic_percent"] != 5,
        result["revision"] != 3,
        result["outcome"] not in {"applied", "no_op"},
    )):
        raise DirectTrafficProductionExpansionGateConfigError(
            "recovery receipt state is invalid"
        )
    for field in ("namespace_sha256", "history_chain_sha256"):
        _validate_digest(result[field], field)
    _parse_timestamp(str(result["applied_at"]), "recovery applied_at")
    return result


def _validate_gate(value: Any) -> dict[str, Any]:
    fields = {
        "schema_version", "gate_id", "generated_at", "decision",
        "environment_sha256", "provider_instance_sha256", "namespace_sha256",
        "observer_instance_sha256", "current_traffic_percent", "target_traffic_percent",
        "health_window_seconds", "minimum_health_window_seconds", "source", "violations",
    }
    if not isinstance(value, Mapping) or set(value) != fields:
        raise DirectTrafficProductionExpansionGateVerificationError(
            "expansion Gate contract is invalid"
        )
    result = dict(value)
    if any((
        result["schema_version"] != GATE_SCHEMA_VERSION,
        _GATE_ID_RE.fullmatch(str(result["gate_id"])) is None,
        result["decision"] not in {"pass", "hold"},
        result["current_traffic_percent"] != 5,
        result["target_traffic_percent"] != 10,
    )):
        raise DirectTrafficProductionExpansionGateVerificationError(
            "expansion Gate identity is invalid"
        )
    _parse_timestamp(str(result["generated_at"]), "expansion generated_at")
    for field in (
        "environment_sha256", "provider_instance_sha256", "namespace_sha256",
        "observer_instance_sha256",
    ):
        _validate_digest(result[field], field)
    window = _validate_elapsed_window(result["health_window_seconds"])
    minimum = _validate_health_window(result["minimum_health_window_seconds"])
    source_fields = {
        "rollout_attestation_sha256", "rollout_report_sha256",
        "adapter_certification_sha256", "shadow_attestation_sha256",
        "shadow_gate_report_sha256",
        "recovery_receipt_sha256",
    }
    if not isinstance(result["source"], Mapping) or set(result["source"]) != source_fields:
        raise DirectTrafficProductionExpansionGateVerificationError(
            "expansion Gate source is invalid"
        )
    for field in source_fields:
        _validate_digest(result["source"][field], field)
    violations = result["violations"]
    if (
        not isinstance(violations, list)
        or violations != sorted(set(violations))
        or any(not isinstance(item, str) or not item for item in violations)
        or (result["decision"] == "pass") != (not violations)
        or (result["decision"] == "pass" and window < minimum)
    ):
        raise DirectTrafficProductionExpansionGateVerificationError(
            "expansion Gate violations are invalid"
        )
    seed = {
        field: result[field] for field in fields
        if field not in {"schema_version", "gate_id", "decision"}
    }
    expected = (
        "direct-traffic-production-expansion-10-gate-"
        f"{_digest(_canonical_json(seed))[:32]}"
    )
    if result["gate_id"] != expected:
        raise DirectTrafficProductionExpansionGateVerificationError(
            "expansion Gate ID binding is invalid"
        )
    return result


def _validate_attestation(value: Any) -> dict[str, Any]:
    fields = {
        "schema_version", "attestation_id", "created_at", "expires_at", "gate_id",
        "decision", "environment_sha256", "provider_instance_sha256",
        "namespace_sha256", "observer_instance_sha256", "current_traffic_percent",
        "target_traffic_percent", "health_window_seconds",
        "minimum_health_window_seconds", "gate_report_sha256",
        "rollout_attestation_sha256", "rollout_report_sha256",
        "adapter_certification_sha256", "shadow_attestation_sha256",
        "shadow_gate_report_sha256",
        "recovery_receipt_sha256", "key_id", "algorithm", "signature",
    }
    if not isinstance(value, Mapping) or set(value) != fields:
        raise DirectTrafficProductionExpansionGateVerificationError(
            "expansion Gate attestation contract is invalid"
        )
    result = dict(value)
    if any((
        result["schema_version"] != ATTESTATION_SCHEMA_VERSION,
        _ATTESTATION_ID_RE.fullmatch(str(result["attestation_id"])) is None,
        _GATE_ID_RE.fullmatch(str(result["gate_id"])) is None,
        result["decision"] != "pass",
        result["current_traffic_percent"] != 5,
        result["target_traffic_percent"] != 10,
        result["algorithm"] != "hmac-sha256",
        _validate_elapsed_window(result["health_window_seconds"])
        < _validate_health_window(result["minimum_health_window_seconds"]),
    )):
        raise DirectTrafficProductionExpansionGateVerificationError(
            "expansion Gate attestation identity is invalid"
        )
    for field in ("created_at", "expires_at"):
        _parse_timestamp(str(result[field]), f"expansion {field}")
    _validate_key_id(result["key_id"], "expansion Gate key_id")
    for field in fields - {
        "schema_version", "attestation_id", "created_at", "expires_at", "gate_id",
        "decision", "current_traffic_percent", "target_traffic_percent",
        "health_window_seconds", "minimum_health_window_seconds", "key_id", "algorithm",
    }:
        _validate_digest(result[field], field)
    identity = {
        field: item for field, item in result.items()
        if field not in {"attestation_id", "signature"}
    }
    expected = (
        "direct-traffic-production-expansion-10-gate-attestation-"
        f"{_digest(_canonical_json(identity))[:32]}"
    )
    if result["attestation_id"] != expected:
        raise DirectTrafficProductionExpansionGateVerificationError(
            "expansion Gate attestation ID binding is invalid"
        )
    return result


def _verify_signed_attestation(
    artifact: Mapping[str, Any], key: bytes, key_id: str, now: datetime
) -> None:
    trusted_key = _validate_key(key, "expansion Gate key")
    trusted_key_id = _validate_key_id(key_id, "expansion Gate key_id")
    unsigned = {field: item for field, item in artifact.items() if field != "signature"}
    if artifact.get("key_id") != trusted_key_id or not hmac.compare_digest(
        str(artifact.get("signature")), _sign(trusted_key, unsigned)
    ):
        raise DirectTrafficProductionExpansionGateVerificationError(
            "expansion Gate signature is invalid"
        )
    _verify_lifetime(artifact, "created_at", MAX_ATTESTATION_SECONDS, now)


def _verify_proof(
    artifact: Mapping[str, Any], key: bytes, key_id: str, domain: bytes,
    created_field: str, max_age_seconds: int, now: datetime, label: str,
) -> None:
    trusted_key = _validate_key(key, f"expansion {label} key")
    trusted_id = _validate_key_id(key_id, f"expansion {label} key_id")
    unsigned = {field: item for field, item in artifact.items() if field != "signature"}
    derived = hmac.new(trusted_key, domain, hashlib.sha256).digest()
    expected = hmac.new(derived, _canonical_json(unsigned), hashlib.sha256).hexdigest()
    if artifact.get("key_id") != trusted_id or not hmac.compare_digest(
        str(artifact.get("signature")), expected
    ):
        raise DirectTrafficProductionExpansionGateVerificationError(
            f"expansion {label} signature is invalid"
        )
    _verify_lifetime(artifact, created_field, max_age_seconds, now)


def _verify_lifetime(
    artifact: Mapping[str, Any], created_field: str, max_age_seconds: int, now: datetime
) -> None:
    try:
        created = _parse_timestamp(str(artifact[created_field]), created_field)
        expires = _parse_timestamp(str(artifact["expires_at"]), "expires_at")
    except (KeyError, ValueError) as exc:
        raise DirectTrafficProductionExpansionGateVerificationError(
            "expansion proof lifetime is invalid"
        ) from exc
    if (
        created > now + timedelta(seconds=MAX_CLOCK_SKEW_SECONDS)
        or expires <= created
        or expires - created > timedelta(days=1)
        or now >= expires
        or now - created > timedelta(seconds=max_age_seconds)
    ):
        raise DirectTrafficProductionExpansionGateVerificationError(
            "expansion proof is stale"
        )


def _read_gate(gate_dir: Path, *, expect_attestation: bool) -> dict[str, Any]:
    expected = {GATE_FILE, ATTESTATION_FILE} if expect_attestation else {GATE_FILE}
    try:
        if {entry.name for entry in os.scandir(gate_dir)} != expected:
            raise DirectTrafficProductionExpansionGateVerificationError(
                "expansion Gate artifact set is invalid"
            )
        return _read_contract(gate_dir / GATE_FILE, _validate_gate)
    except (DeploymentResolutionError, OSError) as exc:
        raise DirectTrafficProductionExpansionGateVerificationError(
            "cannot read expansion Gate"
        ) from exc


def _validate_digest(value: Any, label: str) -> str:
    if not isinstance(value, str) or _DIGEST_RE.fullmatch(value) is None:
        raise DirectTrafficProductionExpansionGateConfigError(f"{label} is invalid")
    return value


def _validate_age(value: Any) -> int:
    if isinstance(value, bool) or not isinstance(value, int) or not 1 <= value <= 86400:
        raise DirectTrafficProductionExpansionGateConfigError("expansion age is invalid")
    return value


def _validate_health_window(value: Any) -> int:
    if isinstance(value, bool) or not isinstance(value, int) or not 60 <= value <= 86400:
        raise DirectTrafficProductionExpansionGateConfigError(
            "expansion minimum health window is invalid"
        )
    return value


def _validate_elapsed_window(value: Any) -> int:
    if isinstance(value, bool) or not isinstance(value, int) or not 0 <= value <= 604800:
        raise DirectTrafficProductionExpansionGateConfigError(
            "expansion health window is invalid"
        )
    return value


def _sign(key: bytes, payload: Mapping[str, Any]) -> str:
    derived = hmac.new(key, SIGNING_DOMAIN, hashlib.sha256).digest()
    return hmac.new(derived, _canonical_json(payload), hashlib.sha256).hexdigest()


def _read_json(path: Path) -> dict[str, Any]:
    try:
        return _read_contract(
            path,
            lambda value: dict(value) if isinstance(value, Mapping) else _invalid_json(),
        )
    except (DeploymentResolutionError, OSError) as exc:
        raise DirectTrafficProductionExpansionGateConfigError(
            f"cannot read {path.name}"
        ) from exc


def _invalid_json() -> dict[str, Any]:
    raise DirectTrafficProductionExpansionGateConfigError("JSON contract is invalid")


def _key_from_environment(name: str, key_id_name: str) -> tuple[bytes, str]:
    raw = os.environ.get(name)
    key_id = os.environ.get(key_id_name)
    if raw is None or key_id is None:
        raise DirectTrafficProductionExpansionGateConfigError(
            f"{name} and {key_id_name} are required"
        )
    return raw.encode("utf-8"), key_id


def main(argv: Sequence[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        description="Authorize the production 5%-to-10% expansion stage"
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
        command.add_argument("--shadow-gate-report", type=Path, required=True)
        command.add_argument("--recovery-receipt", type=Path, required=True)
    evaluate.add_argument("--namespace-id", required=True)
    evaluate.add_argument("--output-dir", type=Path, required=True)
    evaluate.add_argument(
        "--minimum-health-window-seconds",
        type=int,
        default=DEFAULT_MINIMUM_HEALTH_WINDOW_SECONDS,
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
        shadow_key, shadow_key_id = _key_from_environment(
            SHADOW_KEY_ENV, SHADOW_KEY_ID_ENV
        )
        if args.command == "evaluate":
            report = evaluate_expansion_gate(
                _read_json(args.rollout_attestation),
                _read_json(args.rollout_report),
                _read_json(args.adapter_certification),
                _read_json(args.shadow_attestation),
                _read_json(args.shadow_gate_report),
                _read_json(args.recovery_receipt),
                production_namespace_sha256(args.namespace_id),
                rollout_key=rollout_key,
                rollout_key_id=rollout_key_id,
                certification_key=certification_key,
                certification_key_id=certification_key_id,
                shadow_key=shadow_key,
                shadow_key_id=shadow_key_id,
                minimum_health_window_seconds=args.minimum_health_window_seconds,
                max_age_seconds=args.max_age_seconds,
            )
            write_expansion_gate(args.output_dir, report)
            print(json.dumps({"decision": report["decision"], "gate_id": report["gate_id"]}))
            return 0 if report["decision"] == "pass" else 3
        gate_key, gate_key_id = _key_from_environment(KEY_ENV, KEY_ID_ENV)
        if args.command == "attest":
            value = attest_expansion_gate(
                args.gate_dir,
                gate_key,
                gate_key_id,
                rollout_attestation=_read_json(args.rollout_attestation),
                rollout_report=_read_json(args.rollout_report),
                adapter_certification=_read_json(args.adapter_certification),
                shadow_attestation=_read_json(args.shadow_attestation),
                shadow_gate_report=_read_json(args.shadow_gate_report),
                recovery_receipt=_read_json(args.recovery_receipt),
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
            value = verify_expansion_gate_attestation(
                args.gate_dir,
                gate_key,
                gate_key_id,
                max_age_seconds=args.max_age_seconds,
            )
        print(json.dumps({"gate_id": value["gate_id"], "decision": value["decision"]}))
        return 0
    except (
        DirectTrafficProductionExpansionGateError,
        DeploymentResolutionError,
        OSError,
    ) as exc:
        print(f"production expansion Gate failed: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
