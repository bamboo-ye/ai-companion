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
from .direct_traffic_preproduction_drill import (
    KEY_ENV as DRILL_KEY_ENV,
    KEY_ID_ENV as DRILL_KEY_ID_ENV,
    verify_attested_drill_bundle,
)
from .direct_rollout import (
    PRODUCTION_KEY_ENV as ROLLOUT_KEY_ENV,
    PRODUCTION_KEY_ID_ENV as ROLLOUT_KEY_ID_ENV,
    DirectRolloutError,
    _validate_attestation as _validate_rollout_attestation,
)
from .direct_traffic_certification import (
    PRODUCTION_KEY_ENV as CERTIFICATION_KEY_ENV,
    PRODUCTION_KEY_ID_ENV as CERTIFICATION_KEY_ID_ENV,
    DirectTrafficCertificationError,
    _validate_certification,
)
from .direct_traffic_shadow_gate import (
    PRODUCTION_KEY_ENV as SHADOW_KEY_ENV,
    PRODUCTION_KEY_ID_ENV as SHADOW_KEY_ID_ENV,
    DirectTrafficShadowGateError,
    _validate_attestation as _validate_shadow_attestation,
)
from .release_attestation import MAX_CLOCK_SKEW_SECONDS


GATE_SCHEMA_VERSION = "agent-direct-traffic-production-gate-v1"
ATTESTATION_SCHEMA_VERSION = "agent-direct-traffic-production-gate-attestation-v1"
GATE_FILE = "direct-traffic-production-gate.json"
ATTESTATION_FILE = "direct-traffic-production-gate-attestation.json"
KEY_ENV = "OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_GATE_KEY"
KEY_ID_ENV = "OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_GATE_KEY_ID"
MAX_ATTESTATION_SECONDS = 24 * 60 * 60
PRODUCTION_GATE_SIGNING_DOMAIN = (
    b"ai-companion/direct-traffic-production-gate-attestation/v1\0"
)
_DIGEST_RE = re.compile(r"[0-9a-f]{64}")
_NAMESPACE_RE = re.compile(r"production-[a-z0-9][a-z0-9-]{0,62}")
_NAMESPACE_DOMAIN = b"ai-companion/direct-traffic-production-namespace/v1\0"
_GATE_ID_RE = re.compile(r"direct-traffic-production-gate-[0-9a-f]{32}")
_ATTESTATION_ID_RE = re.compile(
    r"direct-traffic-production-gate-attestation-[0-9a-f]{32}"
)


class DirectTrafficProductionGateError(ValueError):
    pass


class DirectTrafficProductionGateConfigError(DirectTrafficProductionGateError):
    pass


class DirectTrafficProductionGateVerificationError(DirectTrafficProductionGateError):
    pass


def evaluate_production_gate(
    local_drill_bundle: Path,
    live_probe_bundle: Path,
    drill_key: bytes,
    drill_key_id: str,
    production_adapter_certification: Mapping[str, Any],
    production_shadow_attestation: Mapping[str, Any],
    rollout_attestation: Mapping[str, Any],
    namespace_sha256: str,
    *,
    certification_key: bytes,
    certification_key_id: str,
    shadow_key: bytes,
    shadow_key_id: str,
    rollout_key: bytes,
    rollout_key_id: str,
    max_age_seconds: int = 900,
    now: datetime | None = None,
) -> dict[str, Any]:
    current_time = _normalize_now(now)
    local_drill = verify_attested_drill_bundle(
        local_drill_bundle,
        drill_key,
        drill_key_id,
        max_age_seconds=max_age_seconds,
        require_mode="local-contract-simulation",
        now=current_time,
    )
    live_probe = verify_attested_drill_bundle(
        live_probe_bundle,
        drill_key,
        drill_key_id,
        max_age_seconds=max_age_seconds,
        require_mode="live-read-only-probe",
        now=current_time,
    )
    try:
        certification = _validate_certification(production_adapter_certification)
        shadow = _validate_shadow_attestation(production_shadow_attestation)
        rollout = _validate_rollout_attestation(rollout_attestation)
    except (
        DirectRolloutError,
        DirectTrafficCertificationError,
        DirectTrafficShadowGateError,
    ) as exc:
        raise DirectTrafficProductionGateVerificationError(
            "production upstream proof contract is invalid"
        ) from exc
    _validate_distinct_keys(
        drill_key, certification_key, shadow_key, rollout_key
    )
    _verify_upstream_proof(
        certification,
        certification_key,
        certification_key_id,
        b"ai-companion/agent-direct-traffic-adapter-certification/v1",
        "certified_at",
        "adapter certification",
        max_age_seconds,
        current_time,
    )
    _verify_upstream_proof(
        shadow,
        shadow_key,
        shadow_key_id,
        b"ai-companion/direct-traffic-shadow-attestation/v1\0",
        "created_at",
        "shadow attestation",
        max_age_seconds,
        current_time,
    )
    _verify_upstream_proof(
        rollout,
        rollout_key,
        rollout_key_id,
        b"ai-companion/agent-direct-rollout-attestation/v1",
        "created_at",
        "rollout attestation",
        max_age_seconds,
        current_time,
    )
    namespace = _validate_digest(namespace_sha256, "production namespace")
    bindings = {
        "environment_sha256": rollout.get("environment_sha256"),
        "provider_instance_sha256": certification.get("provider_instance_sha256"),
        "namespace_sha256": namespace,
        "observer_instance_sha256": shadow.get("observer_instance_sha256"),
    }
    for field, value in bindings.items():
        _validate_digest(value, field)
    violations: list[str] = []
    if local_drill.get("decision") != "pass":
        violations.append("local_drill_not_passed")
    if live_probe.get("decision") != "pass":
        violations.append("live_probe_not_passed")
    if certification.get("adapter") != "https-direct-traffic-production":
        violations.append("wrong_production_adapter")
    if certification.get("implementation_version") != "1.0.0":
        violations.append("wrong_production_adapter_version")
    if shadow.get("decision") != "pass":
        violations.append("production_shadow_not_passed")
    if rollout.get("decision") != "expand":
        violations.append("rollout_not_expand")
    if rollout.get("current_traffic_percent") != 0:
        violations.append("production_initial_stage_not_zero")
    if rollout.get("target_traffic_percent") != 5:
        violations.append("production_target_not_five")
    if local_drill.get("contains_live_provider_evidence") is not False:
        violations.append("local_drill_mode_invalid")
    if live_probe.get("contains_live_provider_evidence") is not True:
        violations.append("live_probe_mode_invalid")
    for field in ("environment_sha256", "provider_instance_sha256"):
        expected = bindings[field]
        if any(
            item.get(field) != expected
            for item in (certification, shadow, rollout)
            if field in item
        ):
            violations.append(f"{field}_mismatch")
    source = {
        "local_drill_report_sha256": _digest(_canonical_json(local_drill)),
        "live_probe_report_sha256": _digest(_canonical_json(live_probe)),
        "adapter_certification_sha256": _digest(_canonical_json(certification)),
        "shadow_attestation_sha256": _digest(_canonical_json(shadow)),
        "rollout_attestation_sha256": _digest(_canonical_json(rollout)),
    }
    seed = {
        "generated_at": _format_timestamp(current_time),
        **bindings,
        "current_traffic_percent": 0,
        "target_traffic_percent": 5,
        "source": source,
        "violations": sorted(set(violations)),
    }
    report: dict[str, Any] = {
        "schema_version": GATE_SCHEMA_VERSION,
        "gate_id": f"direct-traffic-production-gate-{_digest(_canonical_json(seed))[:32]}",
        "generated_at": seed["generated_at"],
        "decision": "pass" if not violations else "hold",
        "environment_sha256": bindings["environment_sha256"],
        "provider_instance_sha256": bindings["provider_instance_sha256"],
        "namespace_sha256": namespace,
        "observer_instance_sha256": bindings["observer_instance_sha256"],
        "current_traffic_percent": 0,
        "target_traffic_percent": 5,
        "source": source,
        "violations": sorted(set(violations)),
    }
    return _validate_gate(report)


def production_namespace_sha256(namespace_id: Any) -> str:
    if not isinstance(namespace_id, str) or _NAMESPACE_RE.fullmatch(namespace_id) is None:
        raise DirectTrafficProductionGateConfigError(
            "namespace must use the explicit production- prefix"
        )
    return _digest(_NAMESPACE_DOMAIN + namespace_id.encode("utf-8"))


def write_production_gate(output_dir: Path, report: Mapping[str, Any]) -> Path:
    validated = _validate_gate(report)
    try:
        _ensure_private_state_directory(output_dir.parent)
        _ensure_private_state_directory(output_dir)
        if list(os.scandir(output_dir)):
            raise DirectTrafficProductionGateVerificationError(
                "production Gate directory must be empty"
            )
        _write_exclusive_private_json(output_dir / GATE_FILE, validated)
    except (DeploymentResolutionError, OSError) as exc:
        raise DirectTrafficProductionGateVerificationError(
            "cannot write private production Gate"
        ) from exc
    return output_dir / GATE_FILE


def attest_production_gate(
    gate_dir: Path,
    key: bytes,
    key_id: str,
    *,
    ttl_seconds: int = 900,
    now: datetime | None = None,
) -> dict[str, Any]:
    signing_key = _validate_key(key, "production Gate key")
    signing_key_id = _validate_key_id(key_id, "production Gate key_id")
    if isinstance(ttl_seconds, bool) or not 1 <= ttl_seconds <= MAX_ATTESTATION_SECONDS:
        raise DirectTrafficProductionGateConfigError("production Gate TTL is invalid")
    current_time = _normalize_now(now)
    report = _read_gate(gate_dir, expect_attestation=False)
    if report["decision"] != "pass":
        raise DirectTrafficProductionGateVerificationError(
            "only a passing production Gate can be attested"
        )
    generated_at = _parse_timestamp(str(report["generated_at"]), "production Gate generated_at")
    if generated_at > current_time + timedelta(seconds=MAX_CLOCK_SKEW_SECONDS):
        raise DirectTrafficProductionGateVerificationError("production Gate is from the future")
    payload: dict[str, Any] = {
        "schema_version": ATTESTATION_SCHEMA_VERSION,
        "attestation_id": "",
        "created_at": _format_timestamp(current_time),
        "expires_at": _format_timestamp(current_time + timedelta(seconds=ttl_seconds)),
        "gate_id": report["gate_id"],
        "decision": "pass",
        "environment_sha256": report["environment_sha256"],
        "provider_instance_sha256": report["provider_instance_sha256"],
        "namespace_sha256": report["namespace_sha256"],
        "observer_instance_sha256": report["observer_instance_sha256"],
        "current_traffic_percent": 0,
        "target_traffic_percent": 5,
        "gate_report_sha256": _digest(_canonical_json(report)),
        "adapter_certification_sha256": report["source"]["adapter_certification_sha256"],
        "shadow_attestation_sha256": report["source"]["shadow_attestation_sha256"],
        "rollout_attestation_sha256": report["source"]["rollout_attestation_sha256"],
        "key_id": signing_key_id,
        "algorithm": "hmac-sha256",
    }
    identity = {field: value for field, value in payload.items() if field != "attestation_id"}
    payload["attestation_id"] = (
        "direct-traffic-production-gate-attestation-"
        f"{_digest(_canonical_json(identity))[:32]}"
    )
    attestation = dict(payload)
    attestation["signature"] = _sign(signing_key, payload)
    validated = _validate_attestation(attestation)
    try:
        _write_exclusive_private_json(gate_dir / ATTESTATION_FILE, validated)
    except FileExistsError:
        return verify_production_gate_attestation(
            gate_dir, signing_key, signing_key_id, now=current_time
        )
    return validated


def verify_production_gate_attestation(
    gate_dir: Path,
    key: bytes,
    key_id: str,
    *,
    max_age_seconds: int = 900,
    now: datetime | None = None,
) -> dict[str, Any]:
    _validate_max_age(max_age_seconds)
    verification_key = _validate_key(key, "production Gate key")
    trusted_key_id = _validate_key_id(key_id, "production Gate key_id")
    current_time = _normalize_now(now)
    report = _read_gate(gate_dir, expect_attestation=True)
    attestation = _read_contract(gate_dir / ATTESTATION_FILE, _validate_attestation)
    if attestation["key_id"] != trusted_key_id:
        raise DirectTrafficProductionGateVerificationError("production Gate key_id is not trusted")
    unsigned = {field: value for field, value in attestation.items() if field != "signature"}
    if not hmac.compare_digest(str(attestation["signature"]), _sign(verification_key, unsigned)):
        raise DirectTrafficProductionGateVerificationError("production Gate signature is invalid")
    expected = {
        "gate_id": report["gate_id"],
        "decision": report["decision"],
        "environment_sha256": report["environment_sha256"],
        "provider_instance_sha256": report["provider_instance_sha256"],
        "namespace_sha256": report["namespace_sha256"],
        "observer_instance_sha256": report["observer_instance_sha256"],
        "current_traffic_percent": report["current_traffic_percent"],
        "target_traffic_percent": report["target_traffic_percent"],
        "gate_report_sha256": _digest(_canonical_json(report)),
        "adapter_certification_sha256": report["source"]["adapter_certification_sha256"],
        "shadow_attestation_sha256": report["source"]["shadow_attestation_sha256"],
        "rollout_attestation_sha256": report["source"]["rollout_attestation_sha256"],
    }
    if any(attestation.get(field) != value for field, value in expected.items()):
        raise DirectTrafficProductionGateVerificationError(
            "production Gate attestation binding is invalid"
        )
    created_at = _parse_timestamp(str(attestation["created_at"]), "production Gate created_at")
    expires_at = _parse_timestamp(str(attestation["expires_at"]), "production Gate expires_at")
    generated_at = _parse_timestamp(str(report["generated_at"]), "production Gate generated_at")
    if (
        created_at > current_time + timedelta(seconds=MAX_CLOCK_SKEW_SECONDS)
        or generated_at > created_at + timedelta(seconds=MAX_CLOCK_SKEW_SECONDS)
        or expires_at <= created_at
        or current_time >= expires_at
        or current_time - created_at > timedelta(seconds=max_age_seconds)
        or current_time - generated_at > timedelta(seconds=max_age_seconds)
    ):
        raise DirectTrafficProductionGateVerificationError("production Gate is stale")
    return attestation


def verify_embedded_production_gate_attestation(
    value: Any,
    key: bytes,
    key_id: str,
    *,
    environment_sha256: str,
    provider_instance_sha256: str,
    namespace_sha256: str,
    observer_instance_sha256: str,
    now: datetime | None = None,
) -> dict[str, Any]:
    """Verify the fourth proof without trusting the client-side verifier."""
    verification_key = _validate_key(key, "production Gate key")
    trusted_key_id = _validate_key_id(key_id, "production Gate key_id")
    attestation = _validate_attestation(value)
    if attestation["key_id"] != trusted_key_id:
        raise DirectTrafficProductionGateVerificationError(
            "production Gate key_id is not trusted"
        )
    unsigned = {field: item for field, item in attestation.items() if field != "signature"}
    if not hmac.compare_digest(
        str(attestation["signature"]), _sign(verification_key, unsigned)
    ):
        raise DirectTrafficProductionGateVerificationError(
            "production Gate signature is invalid"
        )
    expected = {
        "environment_sha256": _validate_digest(
            environment_sha256, "production environment"
        ),
        "provider_instance_sha256": _validate_digest(
            provider_instance_sha256, "production provider instance"
        ),
        "namespace_sha256": _validate_digest(
            namespace_sha256, "production namespace"
        ),
        "observer_instance_sha256": _validate_digest(
            observer_instance_sha256, "production observer instance"
        ),
        "current_traffic_percent": 0,
        "target_traffic_percent": 5,
        "decision": "pass",
    }
    if any(attestation.get(field) != item for field, item in expected.items()):
        raise DirectTrafficProductionGateVerificationError(
            "production Gate execution binding is invalid"
        )
    current_time = _normalize_now(now)
    created_at = _parse_timestamp(
        str(attestation["created_at"]), "production Gate created_at"
    )
    expires_at = _parse_timestamp(
        str(attestation["expires_at"]), "production Gate expires_at"
    )
    if (
        created_at > current_time + timedelta(seconds=MAX_CLOCK_SKEW_SECONDS)
        or expires_at <= created_at
        or expires_at - created_at > timedelta(seconds=MAX_ATTESTATION_SECONDS)
        or current_time >= expires_at
    ):
        raise DirectTrafficProductionGateVerificationError(
            "production Gate embedded authorization is stale"
        )
    return attestation


def _validate_gate(value: Any) -> dict[str, Any]:
    fields = {
        "schema_version", "gate_id", "generated_at", "decision",
        "environment_sha256", "provider_instance_sha256", "namespace_sha256",
        "observer_instance_sha256", "current_traffic_percent",
        "target_traffic_percent", "source", "violations",
    }
    if not isinstance(value, Mapping) or set(value) != fields:
        raise DirectTrafficProductionGateVerificationError("production Gate contract is invalid")
    report = dict(value)
    if (
        report["schema_version"] != GATE_SCHEMA_VERSION
        or not isinstance(report["gate_id"], str)
        or _GATE_ID_RE.fullmatch(report["gate_id"]) is None
        or report["decision"] not in {"pass", "hold"}
        or report["current_traffic_percent"] != 0
        or report["target_traffic_percent"] != 5
    ):
        raise DirectTrafficProductionGateVerificationError("production Gate identity is invalid")
    _parse_timestamp(str(report["generated_at"]), "production Gate generated_at")
    for field in (
        "environment_sha256", "provider_instance_sha256", "namespace_sha256",
        "observer_instance_sha256",
    ):
        _validate_digest(report[field], field)
    source = report["source"]
    source_fields = {
        "local_drill_report_sha256", "live_probe_report_sha256",
        "adapter_certification_sha256", "shadow_attestation_sha256",
        "rollout_attestation_sha256",
    }
    if not isinstance(source, Mapping) or set(source) != source_fields:
        raise DirectTrafficProductionGateVerificationError("production Gate source is invalid")
    for field in source_fields:
        _validate_digest(source[field], field)
    violations = report["violations"]
    if (
        not isinstance(violations, list)
        or any(not isinstance(item, str) or not item for item in violations)
        or violations != sorted(set(violations))
        or (report["decision"] == "pass") != (not violations)
    ):
        raise DirectTrafficProductionGateVerificationError("production Gate violations are invalid")
    seed = {field: report[field] for field in fields if field not in {"schema_version", "gate_id", "decision"}}
    expected_id = f"direct-traffic-production-gate-{_digest(_canonical_json(seed))[:32]}"
    if report["gate_id"] != expected_id:
        raise DirectTrafficProductionGateVerificationError("production Gate ID binding is invalid")
    return report


def _validate_attestation(value: Any) -> dict[str, Any]:
    fields = {
        "schema_version", "attestation_id", "created_at", "expires_at", "gate_id",
        "decision", "environment_sha256", "provider_instance_sha256", "namespace_sha256",
        "observer_instance_sha256", "current_traffic_percent", "target_traffic_percent",
        "gate_report_sha256", "adapter_certification_sha256",
        "shadow_attestation_sha256", "rollout_attestation_sha256",
        "key_id", "algorithm", "signature",
    }
    if not isinstance(value, Mapping) or set(value) != fields:
        raise DirectTrafficProductionGateVerificationError(
            "production Gate attestation contract is invalid"
        )
    result = dict(value)
    if (
        result["schema_version"] != ATTESTATION_SCHEMA_VERSION
        or not isinstance(result["attestation_id"], str)
        or _ATTESTATION_ID_RE.fullmatch(result["attestation_id"]) is None
        or result["decision"] != "pass"
        or result["current_traffic_percent"] != 0
        or result["target_traffic_percent"] != 5
        or result["algorithm"] != "hmac-sha256"
    ):
        raise DirectTrafficProductionGateVerificationError(
            "production Gate attestation identity is invalid"
        )
    _parse_timestamp(str(result["created_at"]), "production Gate created_at")
    _parse_timestamp(str(result["expires_at"]), "production Gate expires_at")
    _validate_key_id(result["key_id"], "production Gate key_id")
    for field in (
        "environment_sha256", "provider_instance_sha256", "namespace_sha256",
        "observer_instance_sha256", "gate_report_sha256",
        "adapter_certification_sha256", "shadow_attestation_sha256",
        "rollout_attestation_sha256", "signature",
    ):
        _validate_digest(result[field], field)
    identity = {field: item for field, item in result.items() if field not in {"attestation_id", "signature"}}
    expected_id = (
        "direct-traffic-production-gate-attestation-"
        f"{_digest(_canonical_json(identity))[:32]}"
    )
    if result["attestation_id"] != expected_id:
        raise DirectTrafficProductionGateVerificationError(
            "production Gate attestation ID binding is invalid"
        )
    return result


def _read_gate(gate_dir: Path, *, expect_attestation: bool) -> dict[str, Any]:
    expected = {GATE_FILE, ATTESTATION_FILE} if expect_attestation else {GATE_FILE}
    try:
        entries = {entry.name for entry in os.scandir(gate_dir)}
    except OSError as exc:
        raise DirectTrafficProductionGateVerificationError("cannot list production Gate") from exc
    if entries != expected:
        raise DirectTrafficProductionGateVerificationError(
            "production Gate artifact set is invalid"
        )
    try:
        return _read_contract(gate_dir / GATE_FILE, _validate_gate)
    except DeploymentResolutionError as exc:
        raise DirectTrafficProductionGateVerificationError("cannot read production Gate") from exc


def _require_mapping(value: Any, label: str) -> dict[str, Any]:
    if not isinstance(value, Mapping):
        raise DirectTrafficProductionGateConfigError(f"{label} is invalid")
    return dict(value)


def _validate_digest(value: Any, label: str) -> str:
    if not isinstance(value, str) or _DIGEST_RE.fullmatch(value) is None:
        raise DirectTrafficProductionGateConfigError(f"{label} digest is invalid")
    return value


def _validate_max_age(value: Any) -> int:
    if isinstance(value, bool) or not isinstance(value, int) or not 1 <= value <= MAX_ATTESTATION_SECONDS:
        raise DirectTrafficProductionGateConfigError(
            "production Gate max age is invalid"
        )
    return value


def _verify_upstream_proof(
    artifact: Mapping[str, Any],
    key: bytes,
    key_id: str,
    domain: bytes,
    created_field: str,
    label: str,
    max_age_seconds: int,
    now: datetime,
) -> None:
    signing_key = _validate_key(key, f"production {label} key")
    trusted_key_id = _validate_key_id(key_id, f"production {label} key_id")
    _validate_max_age(max_age_seconds)
    signature = artifact.get("signature")
    if (
        artifact.get("key_id") != trusted_key_id
        or not isinstance(signature, str)
        or _DIGEST_RE.fullmatch(signature) is None
    ):
        raise DirectTrafficProductionGateVerificationError(
            f"production {label} trust is invalid"
        )
    unsigned = {field: item for field, item in artifact.items() if field != "signature"}
    derived = hmac.new(signing_key, domain, hashlib.sha256).digest()
    expected = hmac.new(derived, _canonical_json(unsigned), hashlib.sha256).hexdigest()
    if not hmac.compare_digest(signature, expected):
        raise DirectTrafficProductionGateVerificationError(
            f"production {label} signature is invalid"
        )
    try:
        created_at = _parse_timestamp(str(artifact[created_field]), f"{label} created_at")
        expires_at = _parse_timestamp(str(artifact["expires_at"]), f"{label} expires_at")
    except (KeyError, ValueError) as exc:
        raise DirectTrafficProductionGateVerificationError(
            f"production {label} lifetime is invalid"
        ) from exc
    if (
        created_at > now + timedelta(seconds=MAX_CLOCK_SKEW_SECONDS)
        or expires_at <= created_at
        or expires_at - created_at > timedelta(seconds=MAX_ATTESTATION_SECONDS)
        or now >= expires_at
        or now - created_at > timedelta(seconds=max_age_seconds)
    ):
        raise DirectTrafficProductionGateVerificationError(
            f"production {label} is stale"
        )


def _validate_distinct_keys(*keys: bytes) -> None:
    validated = tuple(
        _validate_key(key, "production proof key")
        for key in keys
    )
    if len(set(validated)) != len(validated):
        raise DirectTrafficProductionGateConfigError(
            "production proof keys must be distinct"
        )


def _sign(key: bytes, payload: Mapping[str, Any]) -> str:
    derived = hmac.new(key, PRODUCTION_GATE_SIGNING_DOMAIN, hashlib.sha256).digest()
    return hmac.new(derived, _canonical_json(payload), hashlib.sha256).hexdigest()


def _key_from_environment(name: str, key_id_name: str) -> tuple[bytes, str]:
    key = os.environ.get(name)
    key_id = os.environ.get(key_id_name)
    if key is None or key_id is None:
        raise DirectTrafficProductionGateConfigError(f"{name} and {key_id_name} are required")
    return key.encode("utf-8"), key_id


def _read_json(path: Path) -> dict[str, Any]:
    try:
        return _read_contract(
            path,
            lambda value: _require_mapping(value, path.name),
        )
    except (DeploymentResolutionError, OSError) as exc:
        raise DirectTrafficProductionGateConfigError(f"cannot read {path.name}") from exc


def main(argv: Sequence[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Evaluate and attest the 0%-to-5% production Gate")
    commands = parser.add_subparsers(dest="command", required=True)
    evaluate = commands.add_parser("evaluate")
    evaluate.add_argument("--local-drill-bundle", type=Path, required=True)
    evaluate.add_argument("--live-probe-bundle", type=Path, required=True)
    evaluate.add_argument("--adapter-certification", type=Path, required=True)
    evaluate.add_argument("--shadow-attestation", type=Path, required=True)
    evaluate.add_argument("--rollout-attestation", type=Path, required=True)
    evaluate.add_argument("--namespace-id", required=True)
    evaluate.add_argument("--output-dir", type=Path, required=True)
    evaluate.add_argument("--max-age-seconds", type=int, default=900)
    attest = commands.add_parser("attest")
    attest.add_argument("--gate-dir", type=Path, required=True)
    attest.add_argument("--ttl-seconds", type=int, default=900)
    verify = commands.add_parser("verify")
    verify.add_argument("--gate-dir", type=Path, required=True)
    verify.add_argument("--max-age-seconds", type=int, default=900)
    args = parser.parse_args(argv)
    try:
        if args.command == "evaluate":
            drill_key, drill_key_id = _key_from_environment(DRILL_KEY_ENV, DRILL_KEY_ID_ENV)
            certification_key, certification_key_id = _key_from_environment(
                CERTIFICATION_KEY_ENV, CERTIFICATION_KEY_ID_ENV
            )
            shadow_key, shadow_key_id = _key_from_environment(
                SHADOW_KEY_ENV, SHADOW_KEY_ID_ENV
            )
            rollout_key, rollout_key_id = _key_from_environment(
                ROLLOUT_KEY_ENV, ROLLOUT_KEY_ID_ENV
            )
            report = evaluate_production_gate(
                args.local_drill_bundle, args.live_probe_bundle, drill_key, drill_key_id,
                _read_json(args.adapter_certification), _read_json(args.shadow_attestation),
                _read_json(args.rollout_attestation),
                production_namespace_sha256(args.namespace_id),
                certification_key=certification_key,
                certification_key_id=certification_key_id,
                shadow_key=shadow_key,
                shadow_key_id=shadow_key_id,
                rollout_key=rollout_key,
                rollout_key_id=rollout_key_id,
                max_age_seconds=args.max_age_seconds,
            )
            write_production_gate(args.output_dir, report)
            print(json.dumps({"decision": report["decision"], "gate_dir": str(args.output_dir)}))
            return 0 if report["decision"] == "pass" else 3
        gate_key, gate_key_id = _key_from_environment(KEY_ENV, KEY_ID_ENV)
        if args.command == "attest":
            certification_key, _certification_key_id = _key_from_environment(
                CERTIFICATION_KEY_ENV, CERTIFICATION_KEY_ID_ENV
            )
            shadow_key, _shadow_key_id = _key_from_environment(
                SHADOW_KEY_ENV, SHADOW_KEY_ID_ENV
            )
            rollout_key, _rollout_key_id = _key_from_environment(
                ROLLOUT_KEY_ENV, ROLLOUT_KEY_ID_ENV
            )
            drill_key, _drill_key_id = _key_from_environment(
                DRILL_KEY_ENV, DRILL_KEY_ID_ENV
            )
            _validate_distinct_keys(
                gate_key,
                certification_key,
                shadow_key,
                rollout_key,
                drill_key,
            )
            value = attest_production_gate(
                args.gate_dir, gate_key, gate_key_id, ttl_seconds=args.ttl_seconds
            )
            print(json.dumps({"attestation_id": value["attestation_id"]}))
            return 0
        value = verify_production_gate_attestation(
            args.gate_dir, gate_key, gate_key_id, max_age_seconds=args.max_age_seconds
        )
        print(json.dumps({"decision": value["decision"], "gate_id": value["gate_id"]}))
        return 0
    except (DirectTrafficProductionGateError, DeploymentResolutionError, OSError) as exc:
        print(f"direct traffic production Gate failed: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
