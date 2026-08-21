from __future__ import annotations

from dataclasses import dataclass
from datetime import timedelta
from typing import Any, Mapping

from .deployment_controller import _canonical_json, _digest, _parse_timestamp


class ProductionStageEvidenceError(ValueError):
    """Raised when already-authenticated stage evidence is structurally unusable."""


@dataclass(frozen=True)
class ProductionExpansionBoundary:
    """Immutable, permission-free description of one production transition."""

    current_traffic_percent: int
    target_traffic_percent: int
    adapter_name: str
    adapter_version: str
    stage_violation: str


@dataclass(frozen=True)
class ProductionStageEvidence:
    bindings: Mapping[str, str]
    source: Mapping[str, str]
    violations: tuple[str, ...]
    health_window_seconds: int


def evaluate_production_stage_evidence(
    boundary: ProductionExpansionBoundary,
    rollout: Mapping[str, Any],
    report: Mapping[str, Any],
    certification: Mapping[str, Any],
    shadow: Mapping[str, Any],
    shadow_report: Mapping[str, Any],
    prior_receipt: Mapping[str, Any],
    namespace_sha256: str,
    minimum_health_window_seconds: int,
) -> ProductionStageEvidence:
    """Evaluate bindings and time windows without keys, I/O, or write authority.

    Callers remain responsible for validating each contract, authenticating every
    proof, fixing the boundary constant in trusted code, and applying the CAS.
    """

    try:
        prior_time = _parse_timestamp(
            str(prior_receipt["applied_at"]), "prior expansion applied_at"
        )
        rollout_time = _parse_timestamp(
            str(rollout["created_at"]), "rollout created_at"
        )
        shadow_time = _parse_timestamp(
            str(shadow["created_at"]), "shadow created_at"
        )
    except (KeyError, ValueError) as exc:
        raise ProductionStageEvidenceError(
            "production stage evidence time is invalid"
        ) from exc

    bindings = {
        "environment_sha256": str(rollout.get("environment_sha256", "")),
        "provider_instance_sha256": str(
            certification.get("provider_instance_sha256", "")
        ),
        "namespace_sha256": namespace_sha256,
        "observer_instance_sha256": str(
            shadow.get("observer_instance_sha256", "")
        ),
    }
    violations: list[str] = []
    report_sha256 = _digest(_canonical_json(report))
    if rollout.get("report_sha256") != report_sha256:
        violations.append("rollout_report_binding_invalid")
    if any(
        (
            report.get("decision_id") != rollout.get("decision_id"),
            report.get("decision") != rollout.get("decision"),
            report.get("current_traffic_percent")
            != rollout.get("current_traffic_percent"),
            report.get("target_traffic_percent")
            != rollout.get("target_traffic_percent"),
        )
    ):
        violations.append("rollout_report_identity_mismatch")
    if any(
        (
            rollout.get("decision") != "expand",
            rollout.get("current_traffic_percent")
            != boundary.current_traffic_percent,
            rollout.get("target_traffic_percent")
            != boundary.target_traffic_percent,
        )
    ):
        violations.append(boundary.stage_violation)
    if certification.get("adapter") != boundary.adapter_name:
        violations.append("wrong_expansion_adapter")
    if certification.get("implementation_version") != boundary.adapter_version:
        violations.append("wrong_expansion_adapter_version")

    shadow_report_sha256 = _digest(_canonical_json(shadow_report))
    if any(
        (
            shadow.get("gate_report_sha256") != shadow_report_sha256,
            shadow.get("gate_id") != shadow_report.get("gate_id"),
            shadow.get("decision") != shadow_report.get("decision"),
        )
    ):
        violations.append("shadow_report_binding_invalid")
    if any(
        (
            certification.get("environment_sha256")
            != bindings["environment_sha256"],
            shadow.get("environment_sha256") != bindings["environment_sha256"],
            shadow.get("provider_instance_sha256")
            != bindings["provider_instance_sha256"],
            prior_receipt.get("namespace_sha256") != namespace_sha256,
        )
    ):
        violations.append("expansion_identity_mismatch")

    stable = report.get("windows", {}).get("stable", {})
    coverage = stable.get("coverage_seconds") if isinstance(stable, Mapping) else None
    if isinstance(coverage, bool) or not isinstance(coverage, (int, float)):
        violations.append("health_window_missing")
        coverage_seconds = 0
    else:
        coverage_seconds = max(0, int(coverage))
        if coverage_seconds < minimum_health_window_seconds:
            violations.append("health_window_too_short")

    evidence = report.get("evidence")
    if not isinstance(evidence, list) or not evidence:
        violations.append("expansion_health_evidence_missing")
    else:
        try:
            evidence_times = [
                _parse_timestamp(
                    str(item["observed_at"]), "expansion evidence observed_at"
                )
                for item in evidence
                if isinstance(item, Mapping)
            ]
        except (KeyError, ValueError) as exc:
            raise ProductionStageEvidenceError(
                "expansion health evidence time is invalid"
            ) from exc
        if len(evidence_times) != len(evidence) or any(
            observed <= prior_time for observed in evidence_times
        ):
            violations.append("health_window_predates_prior_expansion")

    if rollout_time <= prior_time:
        violations.append("rollout_proof_predates_prior_expansion")
    if shadow_time <= prior_time:
        violations.append("shadow_proof_predates_prior_expansion")
    try:
        shadow_stable = shadow_report.get("windows", {}).get("stable", {})
        shadow_generated = _parse_timestamp(
            str(shadow_report["generated_at"]), "shadow Gate generated_at"
        )
    except (KeyError, ValueError) as exc:
        raise ProductionStageEvidenceError(
            "shadow stage evidence time is invalid"
        ) from exc
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
        if shadow_window_start <= prior_time:
            violations.append("shadow_window_predates_prior_expansion")

    source = {
        "rollout_attestation_sha256": _digest(_canonical_json(rollout)),
        "rollout_report_sha256": report_sha256,
        "adapter_certification_sha256": _digest(_canonical_json(certification)),
        "shadow_attestation_sha256": _digest(_canonical_json(shadow)),
        "shadow_gate_report_sha256": shadow_report_sha256,
        "prior_expansion_receipt_sha256": _digest(
            _canonical_json(prior_receipt)
        ),
    }
    return ProductionStageEvidence(
        bindings=bindings,
        source=source,
        violations=tuple(sorted(set(violations))),
        health_window_seconds=coverage_seconds,
    )
