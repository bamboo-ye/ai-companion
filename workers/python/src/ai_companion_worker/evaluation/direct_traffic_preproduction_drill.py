from __future__ import annotations

import argparse
import hashlib
import hmac
import json
import os
import re
import secrets
import sys
import tempfile
from dataclasses import dataclass
from datetime import UTC, datetime, timedelta
from pathlib import Path
from typing import Any, Literal, Mapping, Sequence, cast

from .deployment_controller import (
    DeploymentControllerError,
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
    DirectRolloutError,
    create_attestation,
    evaluate_rollout,
    load_policy,
    write_rollout_report,
)
from .direct_rollout_traffic import SQLiteDirectTrafficSandbox
from .direct_traffic_certification import (
    DirectTrafficCertificationError,
    create_certification,
)
from .direct_traffic_preproduction import (
    ADAPTER_NAME,
    ADAPTER_CONTRACT_VERSION,
    IMPLEMENTATION_VERSION,
    DirectTrafficPreproductionError,
    DirectTrafficPreproductionIndeterminateError,
    HTTPSPreproductionConfig,
    HTTPSPreproductionDirectTrafficAdapter,
    PreproductionProviderError,
    PreproductionState,
    PreproductionTransport,
    apply_verified_preproduction_execution,
    verify_preproduction_execution,
)
from .direct_traffic_preproduction_sandbox import (
    SQLitePreproductionCertificationAdapter,
    SQLitePreproductionPlatform,
)
from .direct_traffic_shadow import (
    DirectTrafficShadowError,
    DirectTrafficShadowHistory,
    ReadTransport,
    ReadOnlyDirectTrafficProviderAdapter,
    ReadOnlyDirectTrafficProviderConfig,
    TransportResponse as ShadowTransportResponse,
    run_shadow_comparison,
)
from .direct_traffic_shadow_gate import (
    DirectTrafficShadowGateError,
    DirectTrafficShadowGatePolicy,
    create_direct_traffic_shadow_attestation,
    evaluate_direct_traffic_shadow_history,
    write_direct_traffic_shadow_gate_report,
)
from .release_gate_history import (
    ReleaseGateHistory,
    ReleaseGateHistoryError,
    environment_sha256,
)


REPORT_SCHEMA_VERSION = "agent-direct-traffic-preproduction-drill-v1"
ATTESTATION_SCHEMA_VERSION = "agent-direct-traffic-preproduction-drill-attestation-v1"
REPORT_FILE = "direct-traffic-preproduction-drill-report.json"
ATTESTATION_FILE = "direct-traffic-preproduction-drill-attestation.json"
KEY_ENV = "OBSERVABILITY_DIRECT_TRAFFIC_PREPRODUCTION_DRILL_KEY"
KEY_ID_ENV = "OBSERVABILITY_DIRECT_TRAFFIC_PREPRODUCTION_DRILL_KEY_ID"
MAX_ATTESTATION_SECONDS = 24 * 60 * 60
MAX_REPORT_AGE_SECONDS = 24 * 60 * 60
_SIGNING_DOMAIN = b"ai-companion/direct-traffic-preproduction-drill-attestation/v1\0"
_PROBE_DOMAIN = b"ai-companion/direct-traffic-preproduction-read-only-probe/v1\0"
_DIGEST_RE = re.compile(r"[0-9a-f]{64}")
_ATTESTATION_ID_RE = re.compile(
    r"direct-traffic-preproduction-drill-attestation-[0-9a-f]{32}"
)
_MODES = {"local-contract-simulation", "live-read-only-probe"}
_LOCAL_SCENARIOS = (
    "expand_timeout_reconciled",
    "idempotent_replay",
    "repair_regression_rollback",
    "offline_write_indeterminate",
    "offline_recovery_same_key",
)
_LIVE_SCENARIOS = ("https_state_contract", "https_unknown_lookup_contract")
_LOCAL_CHECKS = {
    "automatic_rollback",
    "idempotent_replay",
    "local_remote_chain_convergence",
    "lookup_only_timeout_reconciliation",
    "offline_fail_closed",
    "provider_independent_three_proof_verification",
    "same_key_recovery",
}
_LIVE_CHECKS = {
    "https_lookup_contract",
    "https_state_contract",
    "no_mutation_method_invoked",
}


class DirectTrafficPreproductionDrillError(ValueError):
    pass


class DirectTrafficPreproductionDrillConfigError(DirectTrafficPreproductionDrillError):
    pass


class DirectTrafficPreproductionDrillVerificationError(DirectTrafficPreproductionDrillError):
    pass


@dataclass
class _Clock:
    value: datetime

    def now(self) -> datetime:
        return self.value


@dataclass(frozen=True)
class _ScenarioContext:
    root: Path
    environment_id: str
    environment_sha256: str
    provider_instance: str
    namespace_id: str
    token: str
    rollout_key: bytes
    rollout_key_id: str
    certification_key: bytes
    certification_key_id: str
    shadow_key: bytes
    shadow_key_id: str
    policy: dict[str, Any]
    policy_sha256: str
    release_history: ReleaseGateHistory
    sandbox: SQLiteDirectTrafficSandbox
    platform: SQLitePreproductionPlatform
    adapter: HTTPSPreproductionDirectTrafficAdapter
    certification_path: Path
    shadow_history: DirectTrafficShadowHistory
    shadow_adapter: ReadOnlyDirectTrafficProviderAdapter
    clock: _Clock


def run_local_contract_drill(
    policy_path: Path,
    *,
    now: datetime | None = None,
) -> dict[str, Any]:
    base_time = _normalize_now(now)
    with tempfile.TemporaryDirectory(prefix="ai-companion-preproduction-drill-") as directory:
        context = _create_local_context(Path(directory), policy_path, base_time)
        scenarios: list[dict[str, Any]] = []
        client_put_attempts = 0

        expand_shadow_gate = _collect_shadow_gate(
            context,
            base_time,
            sequence=1,
        )
        expand = _create_authorization(
            context,
            current_traffic_percent=5,
            decision_time=base_time,
            shadow_gate_dir=expand_shadow_gate,
            verify_time=base_time + timedelta(seconds=66),
            require_decision="expand",
        )
        context.clock.value = base_time + timedelta(seconds=66)
        context.platform.timeout_after_next_commit = True
        client_put_attempts += 1
        expand_receipt = apply_verified_preproduction_execution(
            context.adapter,
            context.sandbox,
            context.release_history.path,
            expand,
        )
        _require_converged(context, traffic=10, revision=1, operations=1)
        scenarios.append(
            _local_scenario(
                "expand_timeout_reconciled",
                "expand",
                "applied",
                context,
                client_put_attempts,
                expand_receipt,
            )
        )

        replay_receipt = apply_verified_preproduction_execution(
            context.adapter,
            context.sandbox,
            context.release_history.path,
            expand,
        )
        if not replay_receipt["remote_reused"] or not replay_receipt["local_reused"]:
            raise DirectTrafficPreproductionDrillVerificationError(
                "the expansion replay was not idempotent"
            )
        _require_converged(context, traffic=10, revision=1, operations=1)
        scenarios.append(
            _local_scenario(
                "idempotent_replay",
                "expand",
                "reused",
                context,
                client_put_attempts,
                replay_receipt,
            )
        )

        regression_time = base_time + timedelta(seconds=120)
        _append_release_gate(
            context.release_history,
            context.root,
            4,
            regression_time,
            healthy=False,
        )
        rollback_shadow_gate = _collect_shadow_gate(
            context,
            base_time + timedelta(seconds=130),
            sequence=2,
        )
        rollback = _create_authorization(
            context,
            current_traffic_percent=10,
            decision_time=regression_time + timedelta(seconds=1),
            shadow_gate_dir=rollback_shadow_gate,
            verify_time=base_time + timedelta(seconds=196),
            require_decision="rollback",
        )
        context.clock.value = base_time + timedelta(seconds=196)
        client_put_attempts += 1
        rollback_receipt = apply_verified_preproduction_execution(
            context.adapter,
            context.sandbox,
            context.release_history.path,
            rollback,
        )
        _require_converged(context, traffic=5, revision=2, operations=2)
        scenarios.append(
            _local_scenario(
                "repair_regression_rollback",
                "rollback",
                "applied",
                context,
                client_put_attempts,
                rollback_receipt,
            )
        )

        for sequence, offset in enumerate((240, 540, 840), start=5):
            _append_release_gate(
                context.release_history,
                context.root,
                sequence,
                base_time + timedelta(seconds=offset),
                healthy=True,
            )
        recovery_shadow_gate = _collect_shadow_gate(
            context,
            base_time + timedelta(seconds=850),
            sequence=3,
        )
        recovery = _create_authorization(
            context,
            current_traffic_percent=5,
            decision_time=base_time + timedelta(seconds=841),
            shadow_gate_dir=recovery_shadow_gate,
            verify_time=base_time + timedelta(seconds=916),
            require_decision="expand",
        )
        context.clock.value = base_time + timedelta(seconds=916)
        offline_puts = 0

        def offline_transport(
            method: Literal["GET", "PUT"],
            url: str,
            token: str,
            timeout_seconds: float,
            max_response_bytes: int,
            body: bytes | None,
        ) -> Any:
            nonlocal offline_puts
            if method == "PUT":
                offline_puts += 1
                raise PreproductionProviderError(
                    "provider_transport_error", indeterminate=True
                )
            return context.platform.transport(
                method,
                url,
                token,
                timeout_seconds,
                max_response_bytes,
                body,
            )

        offline_adapter = HTTPSPreproductionDirectTrafficAdapter(
            context.adapter.config,
            transport=offline_transport,
            sleep=lambda _seconds: None,
        )
        try:
            offline_adapter.execute(recovery)
        except DirectTrafficPreproductionIndeterminateError:
            pass
        else:
            raise DirectTrafficPreproductionDrillVerificationError(
                "offline PUT did not stop as indeterminate"
            )
        if offline_puts != 1:
            raise DirectTrafficPreproductionDrillVerificationError(
                "offline PUT was retried"
            )
        client_put_attempts += offline_puts
        _require_converged(context, traffic=5, revision=2, operations=2)
        scenarios.append(
            _local_scenario(
                "offline_write_indeterminate",
                "expand",
                "indeterminate",
                context,
                client_put_attempts,
                None,
            )
        )

        context.clock.value = base_time + timedelta(seconds=917)
        client_put_attempts += 1
        recovery_receipt = apply_verified_preproduction_execution(
            context.adapter,
            context.sandbox,
            context.release_history.path,
            recovery,
        )
        _require_converged(context, traffic=10, revision=3, operations=3)
        scenarios.append(
            _local_scenario(
                "offline_recovery_same_key",
                "expand",
                "applied",
                context,
                client_put_attempts,
                recovery_receipt,
            )
        )

        final_state = context.sandbox.status()
        report = {
            "schema_version": REPORT_SCHEMA_VERSION,
            "generated_at": _format_timestamp(base_time + timedelta(seconds=918)),
            "mode": "local-contract-simulation",
            "decision": "pass",
            "contains_live_provider_evidence": False,
            "environment_sha256": context.environment_sha256,
            "provider_instance_sha256": context.adapter.provider_instance_sha256,
            "namespace_sha256": context.adapter.namespace_sha256,
            "observer_instance_sha256": context.adapter.observer_instance_sha256,
            "protocol": _protocol_contract(),
            "scenarios": scenarios,
            "checks": sorted(_LOCAL_CHECKS),
            "final_state": _state_summary(final_state),
        }
        return _validate_report(report)


def run_live_read_only_probe(
    config: HTTPSPreproductionConfig,
    *,
    transport: PreproductionTransport | None = None,
    now: datetime | None = None,
) -> dict[str, Any]:
    current_time = _normalize_now(now)
    adapter = HTTPSPreproductionDirectTrafficAdapter(config, transport=transport)
    state = adapter.read_state()
    unknown_key = hashlib.sha256(_PROBE_DOMAIN + secrets.token_bytes(32)).hexdigest()
    if adapter.lookup(unknown_key) is not None:
        raise DirectTrafficPreproductionDrillVerificationError(
            "Provider returned an operation for the random probe key"
        )
    state_summary = _preproduction_state_summary(state)
    report = {
        "schema_version": REPORT_SCHEMA_VERSION,
        "generated_at": _format_timestamp(current_time),
        "mode": "live-read-only-probe",
        "decision": "pass",
        "contains_live_provider_evidence": True,
        "environment_sha256": adapter.environment_sha256,
        "provider_instance_sha256": adapter.provider_instance_sha256,
        "namespace_sha256": adapter.namespace_sha256,
        "observer_instance_sha256": adapter.observer_instance_sha256,
        "protocol": _protocol_contract(),
        "scenarios": [
            {
                "name": "https_state_contract",
                "status": "passed",
                "evidence": state_summary,
            },
            {
                "name": "https_unknown_lookup_contract",
                "status": "passed",
                "evidence": {
                    "idempotency_key_sha256": hashlib.sha256(
                        unknown_key.encode("ascii")
                    ).hexdigest(),
                    "found": False,
                },
            },
        ],
        "checks": sorted(_LIVE_CHECKS),
        "final_state": state_summary,
    }
    return _validate_report(report)


def write_attested_drill_bundle(
    output_root: Path,
    report: Mapping[str, Any],
    key: bytes,
    key_id: str,
    *,
    ttl_seconds: int = 3600,
    now: datetime | None = None,
) -> Path:
    validated = _validate_report(report)
    signing_key = _validate_key(key, "preproduction drill attestation key")
    signing_key_id = _validate_key_id(key_id, "preproduction drill attestation key_id")
    if isinstance(ttl_seconds, bool) or not 1 <= ttl_seconds <= MAX_ATTESTATION_SECONDS:
        raise DirectTrafficPreproductionDrillConfigError("drill attestation TTL is invalid")
    current_time = _normalize_now(now)
    generated_at = _parse_timestamp(str(validated["generated_at"]), "drill generated_at")
    if current_time < generated_at:
        current_time = generated_at
    try:
        _ensure_private_state_directory(output_root)
        suffix = _digest(_canonical_json(validated))[:16]
        output_dir = output_root / f"preproduction-drill-{suffix}"
        _ensure_private_state_directory(output_dir)
        if list(os.scandir(output_dir)):
            raise DirectTrafficPreproductionDrillVerificationError(
                "preproduction drill output directory must be empty"
            )
        _write_exclusive_private_json(output_dir / REPORT_FILE, validated)
        report_sha256 = _digest(_canonical_json(validated))
        payload: dict[str, Any] = {
            "schema_version": ATTESTATION_SCHEMA_VERSION,
            "attestation_id": "",
            "created_at": _format_timestamp(current_time),
            "expires_at": _format_timestamp(current_time + timedelta(seconds=ttl_seconds)),
            "decision": validated["decision"],
            "mode": validated["mode"],
            "environment_sha256": validated["environment_sha256"],
            "provider_instance_sha256": validated["provider_instance_sha256"],
            "namespace_sha256": validated["namespace_sha256"],
            "observer_instance_sha256": validated["observer_instance_sha256"],
            "report_sha256": report_sha256,
            "key_id": signing_key_id,
            "algorithm": "hmac-sha256",
        }
        identity = {field: value for field, value in payload.items() if field != "attestation_id"}
        payload["attestation_id"] = (
            "direct-traffic-preproduction-drill-attestation-"
            f"{_digest(_canonical_json(identity))[:32]}"
        )
        attestation = dict(payload)
        attestation["signature"] = _sign(signing_key, payload)
        _write_exclusive_private_json(
            output_dir / ATTESTATION_FILE,
            _validate_attestation(attestation),
        )
        return output_dir
    except (DeploymentResolutionError, OSError) as exc:
        raise DirectTrafficPreproductionDrillVerificationError(
            "cannot write private preproduction drill bundle"
        ) from exc


def verify_attested_drill_bundle(
    bundle_dir: Path,
    key: bytes,
    key_id: str,
    *,
    max_age_seconds: int = 3600,
    require_mode: Literal[
        "local-contract-simulation", "live-read-only-probe", "any"
    ] = "any",
    now: datetime | None = None,
) -> dict[str, Any]:
    verification_key = _validate_key(key, "preproduction drill attestation key")
    expected_key_id = _validate_key_id(key_id, "preproduction drill attestation key_id")
    if (
        isinstance(max_age_seconds, bool)
        or not 1 <= max_age_seconds <= MAX_REPORT_AGE_SECONDS
    ):
        raise DirectTrafficPreproductionDrillConfigError("drill maximum age is invalid")
    if require_mode not in {*_MODES, "any"}:
        raise DirectTrafficPreproductionDrillConfigError("required drill mode is invalid")
    try:
        entries = {entry.name for entry in os.scandir(bundle_dir)}
        if entries != {REPORT_FILE, ATTESTATION_FILE}:
            raise DirectTrafficPreproductionDrillVerificationError(
                "preproduction drill bundle artifact set is invalid"
            )
        report = _read_contract(bundle_dir / REPORT_FILE, _validate_report)
        attestation = _read_contract(
            bundle_dir / ATTESTATION_FILE,
            _validate_attestation,
        )
    except (DeploymentResolutionError, OSError) as exc:
        raise DirectTrafficPreproductionDrillVerificationError(
            "cannot read private preproduction drill bundle"
        ) from exc
    if attestation["key_id"] != expected_key_id:
        raise DirectTrafficPreproductionDrillVerificationError(
            "preproduction drill key_id is not trusted"
        )
    unsigned = {field: value for field, value in attestation.items() if field != "signature"}
    if not hmac.compare_digest(
        str(attestation["signature"]), _sign(verification_key, unsigned)
    ):
        raise DirectTrafficPreproductionDrillVerificationError(
            "preproduction drill signature is invalid"
        )
    expected = {
        "decision": report["decision"],
        "mode": report["mode"],
        "environment_sha256": report["environment_sha256"],
        "provider_instance_sha256": report["provider_instance_sha256"],
        "namespace_sha256": report["namespace_sha256"],
        "observer_instance_sha256": report["observer_instance_sha256"],
        "report_sha256": _digest(_canonical_json(report)),
    }
    if any(attestation.get(field) != value for field, value in expected.items()):
        raise DirectTrafficPreproductionDrillVerificationError(
            "preproduction drill attestation binding is invalid"
        )
    if require_mode != "any" and report["mode"] != require_mode:
        raise DirectTrafficPreproductionDrillVerificationError(
            "preproduction drill mode does not satisfy the requirement"
        )
    current_time = _normalize_now(now)
    generated_at = _parse_timestamp(str(report["generated_at"]), "drill generated_at")
    created_at = _parse_timestamp(str(attestation["created_at"]), "drill created_at")
    expires_at = _parse_timestamp(str(attestation["expires_at"]), "drill expires_at")
    if (
        generated_at > created_at
        or created_at > current_time
        or expires_at <= created_at
        or expires_at - created_at > timedelta(seconds=MAX_ATTESTATION_SECONDS)
        or current_time >= expires_at
        or current_time - created_at > timedelta(seconds=max_age_seconds)
        or current_time - generated_at > timedelta(seconds=max_age_seconds)
    ):
        raise DirectTrafficPreproductionDrillVerificationError(
            "preproduction drill evidence is stale or has invalid time bounds"
        )
    return report


def _create_local_context(
    root: Path,
    policy_path: Path,
    base_time: datetime,
) -> _ScenarioContext:
    environment_id = "preproduction-drill-cn"
    environment_digest = environment_sha256(environment_id)
    provider_instance = "platform-preproduction-drill-blue"
    namespace_id = "preproduction-agent-direct-drill"
    token = secrets.token_urlsafe(32)
    rollout_key = secrets.token_bytes(32)
    certification_key = secrets.token_bytes(32)
    shadow_key = secrets.token_bytes(32)
    rollout_key_id = "preproduction-drill-rollout-v1"
    certification_key_id = "preproduction-drill-certification-v1"
    shadow_key_id = "preproduction-drill-shadow-v1"
    policy, policy_sha256 = load_policy(policy_path)
    release_history = ReleaseGateHistory(root / "release-history.sqlite3", environment_id)
    for sequence, offset in enumerate((-600, -300, 0), start=1):
        _append_release_gate(
            release_history,
            root,
            sequence,
            base_time + timedelta(seconds=offset),
            healthy=True,
        )
    sandbox = SQLiteDirectTrafficSandbox(
        root / "local-traffic.sqlite3",
        environment_digest,
        provider_instance,
        create=True,
        initial_traffic_percent=5,
    )
    certification_adapter = SQLitePreproductionCertificationAdapter(
        root / "certification.sqlite3",
        environment_digest,
        provider_instance,
        namespace_id="isolated-preproduction-drill-v1",
    )
    _certificate, certification_path, _created = create_certification(
        certification_adapter,
        root / "certificates",
        certification_key,
        certification_key_id,
        nonce="preproduction-drill-certification",
        ttl_seconds=7200,
        now=base_time,
    )
    base_url = "https://traffic.provider.example/api"
    clock = _Clock(base_time)
    platform = SQLitePreproductionPlatform(
        root / "platform.sqlite3",
        environment_digest,
        provider_instance,
        namespace_id,
        token,
        rollout_key,
        rollout_key_id,
        certification_key,
        certification_key_id,
        shadow_key,
        shadow_key_id,
        initial_traffic_percent=5,
        base_url=base_url,
        now=clock.now,
    )
    adapter = HTTPSPreproductionDirectTrafficAdapter(
        HTTPSPreproductionConfig(
            base_url=base_url,
            allowed_host="traffic.provider.example",
            provider_instance=provider_instance,
            environment_sha256=environment_digest,
            namespace_id=namespace_id,
            token=token,
            retry_base_seconds=0.001,
        ),
        transport=platform.transport,
        sleep=lambda _seconds: None,
    )

    def shadow_transport(
        url: str,
        supplied_token: str,
        timeout_seconds: float,
        max_response_bytes: int,
    ) -> ShadowTransportResponse:
        response = platform.transport(
            "GET",
            url,
            supplied_token,
            timeout_seconds,
            max_response_bytes,
            None,
        )
        return ShadowTransportResponse(
            response.status,
            response.body,
            response.content_type,
            response.final_url,
            response.retry_after,
        )

    state_path = (
        f"/v1/namespaces/{namespace_id}/direct-traffic/shadow-state"
    )
    shadow_adapter = ReadOnlyDirectTrafficProviderAdapter(
        ReadOnlyDirectTrafficProviderConfig(
            name=ADAPTER_NAME,
            base_url=base_url,
            allowed_host="traffic.provider.example",
            provider_instance=provider_instance,
            environment_sha256=environment_digest,
            token=token,
            state_path=state_path,
            retry_base_seconds=0.001,
            max_snapshot_age_seconds=10,
        ),
        transport=cast(ReadTransport, shadow_transport),
        sleep=lambda _seconds: None,
    )
    if shadow_adapter.observer_instance_sha256 != adapter.observer_instance_sha256:
        raise DirectTrafficPreproductionDrillVerificationError(
            "preproduction drill observer identity is inconsistent"
        )
    shadow_history = DirectTrafficShadowHistory(
        root / "shadow-history.sqlite3",
        adapter.observer_instance_sha256,
        adapter.provider_instance_sha256,
        adapter.environment_sha256,
        now=base_time,
    )
    return _ScenarioContext(
        root,
        environment_id,
        environment_digest,
        provider_instance,
        namespace_id,
        token,
        rollout_key,
        rollout_key_id,
        certification_key,
        certification_key_id,
        shadow_key,
        shadow_key_id,
        policy,
        policy_sha256,
        release_history,
        sandbox,
        platform,
        adapter,
        certification_path,
        shadow_history,
        shadow_adapter,
        clock,
    )


def _collect_shadow_gate(
    context: _ScenarioContext,
    start: datetime,
    *,
    sequence: int,
) -> Path:
    for offset in (0, 20, 40, 60):
        observed_at = start + timedelta(seconds=offset)
        context.clock.value = observed_at
        report = run_shadow_comparison(
            context.sandbox,
            context.shadow_adapter,
            context.shadow_history,
            now=observed_at,
        )
        if report["outcome"] != "match":
            raise DirectTrafficPreproductionDrillVerificationError(
                "preproduction drill shadow state did not converge"
            )
    gate_time = start + timedelta(seconds=65)
    policy = DirectTrafficShadowGatePolicy(
        fast_window_runs=2,
        stable_window_runs=4,
        minimum_stable_window_seconds=60,
        maximum_gap_seconds=25,
        maximum_report_age_seconds=10,
        maximum_retried_observations=0,
    )
    report = evaluate_direct_traffic_shadow_history(
        context.shadow_history,
        policy,
        now=gate_time,
    )
    if report["decision"] != "pass":
        raise DirectTrafficPreproductionDrillVerificationError(
            "preproduction drill shadow Gate did not pass"
        )
    gate_dir = context.root / "shadow-gates" / f"gate-{sequence:02d}"
    write_direct_traffic_shadow_gate_report(gate_dir, report)
    create_direct_traffic_shadow_attestation(
        gate_dir,
        context.shadow_history,
        context.shadow_key,
        context.shadow_key_id,
        ttl_seconds=900,
        now=gate_time,
    )
    return gate_dir


def _create_authorization(
    context: _ScenarioContext,
    *,
    current_traffic_percent: int,
    decision_time: datetime,
    shadow_gate_dir: Path,
    verify_time: datetime,
    require_decision: Literal["expand", "rollback"],
) -> Any:
    report = evaluate_rollout(
        context.release_history,
        context.policy,
        context.policy_sha256,
        current_traffic_percent,
        now=decision_time,
    )
    if report["decision"] != require_decision:
        raise DirectTrafficPreproductionDrillVerificationError(
            f"drill expected {require_decision}, got {report['decision']}"
        )
    decision_dir = write_rollout_report(context.root / "decisions", report)
    create_attestation(
        decision_dir,
        context.release_history,
        context.policy,
        context.policy_sha256,
        context.rollout_key,
        context.rollout_key_id,
        ttl_seconds=900,
        now=decision_time + timedelta(seconds=1),
    )
    return verify_preproduction_execution(
        context.adapter,
        context.sandbox,
        decision_dir,
        context.release_history,
        context.policy,
        context.policy_sha256,
        context.rollout_key,
        context.rollout_key_id,
        context.certification_path,
        context.certification_key,
        context.certification_key_id,
        shadow_gate_dir,
        context.shadow_history,
        context.shadow_key,
        context.shadow_key_id,
        now=verify_time,
    )


def _append_release_gate(
    history: ReleaseGateHistory,
    root: Path,
    sequence: int,
    observed_at: datetime,
    *,
    healthy: bool,
) -> None:
    summary = _healthy_summary()
    if not healthy:
        summary["repair_success_rate"] = 0.0
    report = {
        "schema_version": "observability-release-gate-report-v1",
        "generated_at": _format_timestamp(observed_at),
        "decision": "promote" if healthy else "rollback",
        "source": {},
        "canary": {
            "name": "agent-direct",
            "status": "passed" if healthy else "failed",
            "exit_code": 0 if healthy else 3,
            "duration_ms": 5000,
            "report": {
                "schema_version": "agent-direct-canary-report-v1",
                "decision": "pass" if healthy else "hold",
                "baseline_version": "agent-direct-canary-baseline-v1",
                "summary": summary,
            },
        },
        "phases": {},
        "violations": [] if healthy else [{"code": "repair_regression"}],
    }
    gate_dir = root / f"release-gate-drill-{sequence:03d}"
    try:
        _ensure_private_state_directory(gate_dir)
        _write_exclusive_private_json(gate_dir / "gate-report.json", report)
        _write_exclusive_private_bytes(
            gate_dir / "gate-report.xml",
            b"<testsuite name=\"preproduction-drill\" tests=\"1\" failures=\"0\"/>\n",
        )
    except DeploymentResolutionError as exc:
        raise DirectTrafficPreproductionDrillVerificationError(
            "cannot create drill release Gate evidence"
        ) from exc
    history.record(gate_dir, report)


def _write_exclusive_private_bytes(path: Path, content: bytes) -> None:
    flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_NOFOLLOW", 0)
    descriptor = os.open(path, flags, 0o600)
    try:
        with os.fdopen(descriptor, "wb", closefd=False) as output:
            output.write(content)
            output.flush()
            os.fsync(output.fileno())
        os.chmod(path, 0o600)
    finally:
        os.close(descriptor)


def _healthy_summary() -> dict[str, int | float | bool]:
    return {
        "cases": 5,
        "completed": 5,
        "assistant_messages": 5,
        "assistant_delivery_violations": 0,
        "direct_runs": 5,
        "direct_rate": 1.0,
        "single_model_call_runs": 5,
        "single_model_call_rate": 1.0,
        "quality_passed_runs": 5,
        "quality_pass_rate": 1.0,
        "content_passed_runs": 5,
        "content_pass_rate": 1.0,
        "repair_attempted_runs": 0,
        "repair_succeeded_runs": 0,
        "repair_success_rate": 1.0,
        "deterministic_repairs": 0,
        "rewrite_runs": 0,
        "response_quality_failures": 0,
        "duplicate_violations": 0,
        "planned_runs": 0,
        "tool_actions": 0,
        "model_calls": 5,
        "duration_p95_ms": 4000.0,
        "duration_max_ms": 4000,
        "repair_probe_passed": True,
    }


def _require_converged(
    context: _ScenarioContext,
    *,
    traffic: int,
    revision: int,
    operations: int,
) -> None:
    local = _state_summary(context.sandbox.status())
    remote = _state_summary(context.platform.status())
    if (
        local != remote
        or local["current_traffic_percent"] != traffic
        or local["revision"] != revision
        or local["operations"] != operations
    ):
        raise DirectTrafficPreproductionDrillVerificationError(
            "preproduction drill local and remote states did not converge"
        )


def _local_scenario(
    name: str,
    decision: str,
    outcome: str,
    context: _ScenarioContext,
    client_put_attempts: int,
    receipt: Mapping[str, Any] | None,
) -> dict[str, Any]:
    state = _state_summary(context.platform.status())
    return {
        "name": name,
        "status": "passed",
        "evidence": {
            "decision": decision,
            "outcome": outcome,
            **state,
            "client_put_attempts": client_put_attempts,
            "provider_put_calls": context.platform.write_calls,
            "remote_reused": bool(receipt and receipt["remote_reused"]),
            "local_reused": bool(receipt and receipt["local_reused"]),
        },
    }


def _protocol_contract() -> dict[str, Any]:
    return {
        "adapter_contract_version": ADAPTER_CONTRACT_VERSION,
        "adapter": ADAPTER_NAME,
        "implementation_version": IMPLEMENTATION_VERSION,
        "methods": [
            "GET state",
            "GET change-by-idempotency-key",
            "PUT change",
        ],
        "https_only": True,
        "redirects_allowed": False,
        "production_namespace_supported": False,
        "put_retry_limit": 0,
    }


def _state_summary(value: Mapping[str, Any]) -> dict[str, Any]:
    return {
        "current_traffic_percent": int(value["current_traffic_percent"]),
        "revision": int(value["revision"]),
        "operations": int(value["operations"]),
        "history_chain_sha256": str(value["history_chain_sha256"]),
    }


def _preproduction_state_summary(value: PreproductionState) -> dict[str, Any]:
    return {
        "current_traffic_percent": value.current_traffic_percent,
        "revision": value.revision,
        "operations": value.operations,
        "history_chain_sha256": value.history_chain_sha256,
    }


def _validate_report(value: Any) -> dict[str, Any]:
    fields = {
        "schema_version",
        "generated_at",
        "mode",
        "decision",
        "contains_live_provider_evidence",
        "environment_sha256",
        "provider_instance_sha256",
        "namespace_sha256",
        "observer_instance_sha256",
        "protocol",
        "scenarios",
        "checks",
        "final_state",
    }
    if not isinstance(value, Mapping) or set(value) != fields:
        raise DirectTrafficPreproductionDrillVerificationError(
            "preproduction drill report contract is invalid"
        )
    report = dict(value)
    if (
        report["schema_version"] != REPORT_SCHEMA_VERSION
        or report["mode"] not in _MODES
        or report["decision"] != "pass"
        or report["contains_live_provider_evidence"]
        != (report["mode"] == "live-read-only-probe")
    ):
        raise DirectTrafficPreproductionDrillVerificationError(
            "preproduction drill report identity is invalid"
        )
    _parse_timestamp(str(report["generated_at"]), "drill generated_at")
    for field in (
        "environment_sha256",
        "provider_instance_sha256",
        "namespace_sha256",
        "observer_instance_sha256",
    ):
        _validate_digest(report[field], field)
    if report["protocol"] != _protocol_contract():
        raise DirectTrafficPreproductionDrillVerificationError(
            "preproduction drill protocol contract is invalid"
        )
    scenarios = report["scenarios"]
    names = _LOCAL_SCENARIOS if report["mode"] == "local-contract-simulation" else _LIVE_SCENARIOS
    if (
        not isinstance(scenarios, list)
        or tuple(item.get("name") if isinstance(item, Mapping) else None for item in scenarios)
        != names
    ):
        raise DirectTrafficPreproductionDrillVerificationError(
            "preproduction drill scenario set is invalid"
        )
    validated_scenarios = [
        _validate_local_scenario(item)
        if report["mode"] == "local-contract-simulation"
        else _validate_live_scenario(item)
        for item in scenarios
    ]
    expected_checks = _LOCAL_CHECKS if report["mode"] == "local-contract-simulation" else _LIVE_CHECKS
    if report["checks"] != sorted(expected_checks):
        raise DirectTrafficPreproductionDrillVerificationError(
            "preproduction drill checks are incomplete"
        )
    final_state = _validate_state_summary(report["final_state"])
    if report["mode"] == "local-contract-simulation":
        final_evidence = validated_scenarios[-1]["evidence"]
        if any(final_state[field] != final_evidence[field] for field in final_state):
            raise DirectTrafficPreproductionDrillVerificationError(
                "preproduction drill final state is inconsistent"
            )
    elif final_state != validated_scenarios[0]["evidence"]:
        raise DirectTrafficPreproductionDrillVerificationError(
            "preproduction probe final state is inconsistent"
        )
    report["scenarios"] = validated_scenarios
    report["final_state"] = final_state
    return report


def _validate_local_scenario(value: Any) -> dict[str, Any]:
    if not isinstance(value, Mapping) or set(value) != {"name", "status", "evidence"}:
        raise DirectTrafficPreproductionDrillVerificationError("local drill scenario is invalid")
    evidence_fields = {
        "decision",
        "outcome",
        "current_traffic_percent",
        "revision",
        "operations",
        "history_chain_sha256",
        "client_put_attempts",
        "provider_put_calls",
        "remote_reused",
        "local_reused",
    }
    evidence = value["evidence"]
    if (
        value["status"] != "passed"
        or not isinstance(evidence, Mapping)
        or set(evidence) != evidence_fields
        or evidence["decision"] not in {"expand", "rollback"}
        or evidence["outcome"] not in {"applied", "reused", "indeterminate"}
        or not isinstance(evidence["remote_reused"], bool)
        or not isinstance(evidence["local_reused"], bool)
    ):
        raise DirectTrafficPreproductionDrillVerificationError("local drill evidence is invalid")
    state = _validate_state_summary(evidence)
    for field in ("client_put_attempts", "provider_put_calls"):
        _validate_int(evidence[field], field, 0, 100)
    result = dict(value)
    result["evidence"] = {**dict(evidence), **state}
    return result


def _validate_live_scenario(value: Any) -> dict[str, Any]:
    if not isinstance(value, Mapping) or set(value) != {"name", "status", "evidence"}:
        raise DirectTrafficPreproductionDrillVerificationError("live probe scenario is invalid")
    if value["status"] != "passed" or not isinstance(value["evidence"], Mapping):
        raise DirectTrafficPreproductionDrillVerificationError("live probe evidence is invalid")
    result = dict(value)
    if value["name"] == "https_state_contract":
        result["evidence"] = _validate_state_summary(value["evidence"])
    elif value["name"] == "https_unknown_lookup_contract":
        evidence = value["evidence"]
        if set(evidence) != {"idempotency_key_sha256", "found"} or evidence["found"] is not False:
            raise DirectTrafficPreproductionDrillVerificationError(
                "live probe lookup evidence is invalid"
            )
        _validate_digest(evidence["idempotency_key_sha256"], "probe key")
    else:
        raise DirectTrafficPreproductionDrillVerificationError("live probe name is invalid")
    return result


def _validate_state_summary(value: Any) -> dict[str, Any]:
    fields = {
        "current_traffic_percent",
        "revision",
        "operations",
        "history_chain_sha256",
    }
    if not isinstance(value, Mapping) or not fields.issubset(value):
        raise DirectTrafficPreproductionDrillVerificationError("drill state evidence is invalid")
    result = {field: value[field] for field in fields}
    _validate_int(result["current_traffic_percent"], "traffic", 0, 100)
    _validate_int(result["revision"], "revision", 0, 1_000_000_000)
    _validate_int(result["operations"], "operations", 0, 100_000)
    _validate_digest(result["history_chain_sha256"], "history chain")
    return result


def _validate_attestation(value: Any) -> dict[str, Any]:
    fields = {
        "schema_version",
        "attestation_id",
        "created_at",
        "expires_at",
        "decision",
        "mode",
        "environment_sha256",
        "provider_instance_sha256",
        "namespace_sha256",
        "observer_instance_sha256",
        "report_sha256",
        "key_id",
        "algorithm",
        "signature",
    }
    if not isinstance(value, Mapping) or set(value) != fields:
        raise DirectTrafficPreproductionDrillVerificationError(
            "preproduction drill attestation contract is invalid"
        )
    result = dict(value)
    if (
        result["schema_version"] != ATTESTATION_SCHEMA_VERSION
        or not isinstance(result["attestation_id"], str)
        or _ATTESTATION_ID_RE.fullmatch(result["attestation_id"]) is None
        or result["decision"] != "pass"
        or result["mode"] not in _MODES
        or result["algorithm"] != "hmac-sha256"
    ):
        raise DirectTrafficPreproductionDrillVerificationError(
            "preproduction drill attestation identity is invalid"
        )
    _parse_timestamp(str(result["created_at"]), "drill created_at")
    _parse_timestamp(str(result["expires_at"]), "drill expires_at")
    _validate_key_id(result["key_id"], "preproduction drill attestation key_id")
    for field in (
        "environment_sha256",
        "provider_instance_sha256",
        "namespace_sha256",
        "observer_instance_sha256",
        "report_sha256",
        "signature",
    ):
        _validate_digest(result[field], field)
    identity = {field: item for field, item in result.items() if field not in {"attestation_id", "signature"}}
    expected_id = (
        "direct-traffic-preproduction-drill-attestation-"
        f"{_digest(_canonical_json(identity))[:32]}"
    )
    if result["attestation_id"] != expected_id:
        raise DirectTrafficPreproductionDrillVerificationError(
            "preproduction drill attestation ID binding is invalid"
        )
    return result


def _validate_digest(value: Any, label: str) -> str:
    if not isinstance(value, str) or _DIGEST_RE.fullmatch(value) is None:
        raise DirectTrafficPreproductionDrillVerificationError(f"{label} digest is invalid")
    return value


def _validate_int(value: Any, label: str, minimum: int, maximum: int) -> int:
    if isinstance(value, bool) or not isinstance(value, int) or not minimum <= value <= maximum:
        raise DirectTrafficPreproductionDrillVerificationError(f"{label} is invalid")
    return value


def _sign(key: bytes, payload: Mapping[str, Any]) -> str:
    derived = hmac.new(key, _SIGNING_DOMAIN, hashlib.sha256).digest()
    return hmac.new(derived, _canonical_json(payload), hashlib.sha256).hexdigest()


def _key_from_environment() -> tuple[bytes, str]:
    key = os.environ.get(KEY_ENV)
    key_id = os.environ.get(KEY_ID_ENV)
    if key is None or key_id is None:
        raise DirectTrafficPreproductionDrillConfigError(
            f"{KEY_ENV} and {KEY_ID_ENV} are required"
        )
    return key.encode("utf-8"), key_id


def main(argv: Sequence[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        description="Run and attest preproduction Provider contract drills"
    )
    commands = parser.add_subparsers(dest="command", required=True)
    local = commands.add_parser("run-local")
    local.add_argument("--policy", type=Path, required=True)
    local.add_argument("--output-root", type=Path, required=True)
    local.add_argument("--ttl-seconds", type=int, default=3600)
    probe = commands.add_parser("probe")
    probe.add_argument("--environment-id", required=True)
    probe.add_argument("--provider-instance", required=True)
    probe.add_argument("--namespace-id", required=True)
    probe.add_argument("--provider-base-url", required=True)
    probe.add_argument("--allowed-host", required=True)
    probe.add_argument("--timeout-seconds", type=float, default=5.0)
    probe.add_argument("--output-root", type=Path, required=True)
    probe.add_argument("--ttl-seconds", type=int, default=3600)
    verify = commands.add_parser("verify")
    verify.add_argument("--bundle", type=Path, required=True)
    verify.add_argument("--max-age-seconds", type=int, default=3600)
    verify.add_argument(
        "--require-mode",
        choices=["local-contract-simulation", "live-read-only-probe", "any"],
        default="any",
    )
    commands.add_parser("self-test")
    args = parser.parse_args(argv)
    try:
        if args.command == "self-test":
            root = Path(__file__).resolve().parents[5]
            policy_path = root / "evals" / "agent" / "baselines" / "direct-rollout.v1.json"
            fixed_now = datetime(2026, 8, 13, 8, 0, tzinfo=UTC)
            key = secrets.token_bytes(32)
            with tempfile.TemporaryDirectory() as directory:
                report = run_local_contract_drill(policy_path, now=fixed_now)
                bundle = write_attested_drill_bundle(
                    Path(directory) / "evidence",
                    report,
                    key,
                    "preproduction-drill-self-test-v1",
                    now=fixed_now + timedelta(seconds=918),
                )
                verify_attested_drill_bundle(
                    bundle,
                    key,
                    "preproduction-drill-self-test-v1",
                    now=fixed_now + timedelta(seconds=919),
                )
            print("direct_traffic_preproduction_drill_self_test=passed")
            return 0
        key, key_id = _key_from_environment()
        if args.command == "run-local":
            completed_at = datetime.now(UTC)
            report = run_local_contract_drill(
                args.policy,
                now=completed_at - timedelta(seconds=918),
            )
            bundle = write_attested_drill_bundle(
                args.output_root,
                report,
                key,
                key_id,
                ttl_seconds=args.ttl_seconds,
                now=completed_at,
            )
            print(json.dumps({"decision": report["decision"], "bundle": str(bundle)}))
            return 0
        if args.command == "probe":
            token = os.environ.get(
                "OBSERVABILITY_DIRECT_TRAFFIC_PREPRODUCTION_TOKEN", ""
            )
            if not token:
                raise DirectTrafficPreproductionDrillConfigError(
                    "OBSERVABILITY_DIRECT_TRAFFIC_PREPRODUCTION_TOKEN is required"
                )
            report = run_live_read_only_probe(
                HTTPSPreproductionConfig(
                    base_url=args.provider_base_url,
                    allowed_host=args.allowed_host,
                    provider_instance=args.provider_instance,
                    environment_sha256=environment_sha256(args.environment_id),
                    namespace_id=args.namespace_id,
                    token=token,
                    timeout_seconds=args.timeout_seconds,
                )
            )
            bundle = write_attested_drill_bundle(
                args.output_root,
                report,
                key,
                key_id,
                ttl_seconds=args.ttl_seconds,
            )
            print(json.dumps({"decision": report["decision"], "bundle": str(bundle)}))
            return 0
        report = verify_attested_drill_bundle(
            args.bundle,
            key,
            key_id,
            max_age_seconds=args.max_age_seconds,
            require_mode=args.require_mode,
        )
        print(json.dumps({"decision": report["decision"], "mode": report["mode"]}))
        return 0
    except (
        DirectTrafficPreproductionDrillError,
        DirectTrafficPreproductionError,
        PreproductionProviderError,
        DirectRolloutError,
        DirectTrafficCertificationError,
        DirectTrafficShadowError,
        DirectTrafficShadowGateError,
        ReleaseGateHistoryError,
        DeploymentControllerError,
        DeploymentResolutionError,
        OSError,
    ) as exc:
        print(f"direct traffic preproduction drill failed: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
