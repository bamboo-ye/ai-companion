from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import sqlite3
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import asdict, dataclass, field
from datetime import datetime
from http import HTTPStatus
from pathlib import Path
from typing import Any, Callable, Literal, Mapping, Protocol, Sequence

from .deployment_controller import (
    DeploymentControllerError,
    _canonical_json,
    _digest,
    _format_timestamp,
    _normalize_now,
    _parse_timestamp,
)
from .direct_rollout import (
    DirectRolloutError,
    KEY_ENV as ROLLOUT_KEY_ENV,
    KEY_ID_ENV as ROLLOUT_KEY_ID_ENV,
    load_policy,
    verify_attestation as verify_rollout_attestation,
)
from .direct_rollout_traffic import (
    DirectTrafficError,
    SQLiteDirectTrafficSandbox,
    TrafficChangeRequest,
    build_traffic_request,
    provider_instance_sha256,
)
from .direct_traffic_adapter import (
    ADAPTER_CONTRACT_VERSION,
    CERTIFICATION_ISOLATION_MODE,
    AdapterChangeRequest,
    AdapterChangeResult,
    AdapterTrafficState,
)
from .direct_traffic_certification import (
    DirectTrafficCertificationError,
    certification_key_from_environment,
    validate_execution_binding,
    verify_certification,
)
from .direct_traffic_shadow import (
    DirectTrafficShadowError,
    DirectTrafficShadowHistory,
    ReadOnlyDirectTrafficProviderConfig,
    observer_instance_sha256,
)
from .direct_traffic_shadow_gate import (
    DirectTrafficShadowGateError,
    KEY_ENV as SHADOW_GATE_KEY_ENV,
    KEY_ID_ENV as SHADOW_GATE_KEY_ID_ENV,
    verify_direct_traffic_shadow_attestation,
)
from .release_gate import (
    DEFAULT_CANARY_LEASE_ROOT,
    ReleaseGateError,
    try_acquire_canary_lease,
)
from .release_gate_history import (
    ReleaseGateHistory,
    ReleaseGateHistoryError,
    environment_sha256,
)


ADAPTER_NAME = "https-direct-traffic-preproduction"
IMPLEMENTATION_VERSION = "1.0.0"
EXECUTION_SCHEMA_VERSION = "agent-direct-traffic-preproduction-execution-v1"
RESULT_SCHEMA_VERSION = "agent-direct-traffic-preproduction-result-v1"
STATE_SCHEMA_VERSION = "agent-direct-traffic-preproduction-state-v1"
TOKEN_ENV = "OBSERVABILITY_DIRECT_TRAFFIC_PREPRODUCTION_TOKEN"
MAX_RESPONSE_BYTES = 32 * 1024
MAX_READ_ATTEMPTS = 3
MAX_RETRY_DELAY_SECONDS = 5.0
_NAMESPACE_DOMAIN = b"ai-companion/direct-traffic-preproduction-namespace/v1\0"
_OPERATIONAL_DOMAIN = b"ai-companion/direct-traffic-preproduction-operational/v1\0"
_DIGEST_RE = re.compile(r"[0-9a-f]{64}")
_NAMESPACE_RE = re.compile(r"preproduction-[a-z0-9][a-z0-9-]{0,62}")


class DirectTrafficPreproductionError(ValueError):
    pass


class DirectTrafficPreproductionConfigError(DirectTrafficPreproductionError):
    pass


class DirectTrafficPreproductionConflictError(DirectTrafficPreproductionError):
    pass


class DirectTrafficPreproductionIndeterminateError(DirectTrafficPreproductionError):
    pass


class PreproductionProviderError(RuntimeError):
    def __init__(
        self,
        code: str,
        *,
        retryable: bool = False,
        indeterminate: bool = False,
        retry_after: str | None = None,
    ) -> None:
        super().__init__(code)
        self.code = code
        self.retryable = retryable
        self.indeterminate = indeterminate
        self.retry_after = retry_after


@dataclass(frozen=True)
class HTTPSPreproductionConfig:
    base_url: str
    allowed_host: str
    provider_instance: str
    environment_sha256: str
    namespace_id: str
    token: str = field(repr=False)
    timeout_seconds: float = 5.0
    max_read_attempts: int = 2
    retry_base_seconds: float = 0.25
    max_response_bytes: int = MAX_RESPONSE_BYTES


@dataclass(frozen=True)
class TransportResponse:
    status: int
    body: bytes
    content_type: str
    final_url: str
    retry_after: str | None = None


class PreproductionTransport(Protocol):
    def __call__(
        self,
        method: Literal["GET", "PUT"],
        url: str,
        token: str,
        timeout_seconds: float,
        max_response_bytes: int,
        body: bytes | None,
    ) -> TransportResponse: ...


class DirectTrafficExecutionAuthorization(Protocol):
    @property
    def request(self) -> TrafficChangeRequest: ...

    @property
    def local_state(self) -> Mapping[str, Any]: ...

    @property
    def payload(self) -> Mapping[str, Any]: ...


@dataclass(frozen=True)
class PreproductionState:
    provider_instance_sha256: str
    environment_sha256: str
    namespace_sha256: str
    current_traffic_percent: int
    revision: int
    operations: int
    history_chain_sha256: str
    observed_at: str


@dataclass(frozen=True)
class PreproductionResult:
    operation_id: str
    idempotency_key: str
    request_sha256: str
    outcome: Literal["applied", "no_op", "conflict"]
    current_traffic_percent: int
    revision: int
    operations: int
    history_chain_sha256: str
    applied_at: str


@dataclass(frozen=True)
class VerifiedPreproductionExecution:
    request: TrafficChangeRequest
    rollout_attestation: Mapping[str, Any]
    certification: Mapping[str, Any]
    certification_sha256: str
    shadow_attestation: Mapping[str, Any]
    shadow_attestation_sha256: str
    local_state: Mapping[str, Any]
    payload: Mapping[str, Any]


class HTTPSPreproductionDirectTrafficAdapter:
    name = ADAPTER_NAME
    implementation_version = IMPLEMENTATION_VERSION
    execution_schema_version = EXECUTION_SCHEMA_VERSION
    execution_extra_fields: frozenset[str] = frozenset()
    supports_idempotent_apply = True
    supports_lookup = True
    supports_compare_and_swap = True
    certification_isolation_mode = CERTIFICATION_ISOLATION_MODE

    def __init__(
        self,
        config: HTTPSPreproductionConfig,
        *,
        transport: PreproductionTransport | None = None,
        sleep: Callable[[float], None] = time.sleep,
    ) -> None:
        self.config = self._validate_adapter_config(config)
        self.provider_instance_sha256 = provider_instance_sha256(self.config.provider_instance)
        self.environment_sha256 = self.config.environment_sha256
        self.namespace_sha256 = self._namespace_sha256(self.config.namespace_id)
        self.observer_instance_sha256 = observer_instance_sha256(
            self._observer_config(self.config)
        )
        self._transport = transport or _default_transport
        self._sleep = sleep

    def _validate_adapter_config(
        self, config: HTTPSPreproductionConfig
    ) -> HTTPSPreproductionConfig:
        return _validate_config(config)

    def _observer_config(
        self, config: HTTPSPreproductionConfig
    ) -> ReadOnlyDirectTrafficProviderConfig:
        return _shadow_observer_config(config)

    def _namespace_sha256(self, namespace_id: str) -> str:
        return preproduction_namespace_sha256(namespace_id)

    def operational_state_sha256(self) -> str:
        return _digest(
            _OPERATIONAL_DOMAIN
            + _canonical_json(
                {
                    "adapter_contract_version": ADAPTER_CONTRACT_VERSION,
                    "adapter": self.name,
                    "implementation_version": self.implementation_version,
                    "provider_instance_sha256": self.provider_instance_sha256,
                    "environment_sha256": self.environment_sha256,
                    "namespace_sha256": self.namespace_sha256,
                }
            )
        )

    def certification_namespace_sha256(self) -> str:
        return _digest(
            _canonical_json(
                {
                    "adapter_contract_version": ADAPTER_CONTRACT_VERSION,
                    "adapter": self.name,
                    "provider_instance_sha256": self.provider_instance_sha256,
                    "environment_sha256": self.environment_sha256,
                    "namespace": "remote-certification-not-used-by-execution-client",
                }
            )
        )

    def initialize_certification_challenge(
        self, challenge_id: str, initial_traffic_percent: int
    ) -> AdapterTrafficState:
        del challenge_id, initial_traffic_percent
        raise DirectTrafficPreproductionConfigError(
            "execution client cannot host certification challenges"
        )

    def read_certification_state(self, challenge_id: str) -> AdapterTrafficState:
        del challenge_id
        raise DirectTrafficPreproductionConfigError(
            "execution client cannot host certification challenges"
        )

    def apply_certification_change(self, request: AdapterChangeRequest) -> AdapterChangeResult:
        del request
        raise DirectTrafficPreproductionConfigError(
            "execution client cannot host certification challenges"
        )

    def lookup_certification_change(self, idempotency_key: str) -> AdapterChangeResult | None:
        del idempotency_key
        raise DirectTrafficPreproductionConfigError(
            "execution client cannot host certification challenges"
        )

    def read_state(self) -> PreproductionState:
        response = self._read_request(self._state_url())
        if response.status != HTTPStatus.OK:
            raise PreproductionProviderError(f"provider_http_{response.status}")
        return self._parse_state(response)

    def lookup(self, idempotency_key: str) -> PreproductionResult | None:
        key = _validate_digest(idempotency_key, "preproduction idempotency_key")
        response = self._read_request(self._lookup_url(key))
        if response.status == HTTPStatus.NOT_FOUND:
            return None
        if response.status != HTTPStatus.OK:
            raise PreproductionProviderError(f"provider_http_{response.status}")
        return self._parse_result(response)

    def execute(
        self,
        authorization: DirectTrafficExecutionAuthorization,
        *,
        reconciliation_attempts: int = 2,
    ) -> tuple[PreproductionResult, bool]:
        attempts = _validate_int(
            reconciliation_attempts, "reconciliation attempts", 1, MAX_READ_ATTEMPTS
        )
        validated = self._validate_authorization(authorization)
        request = validated.request
        existing = self.lookup(request.idempotency_key)
        if existing is not None:
            self._validate_result(existing, validated)
            self._verify_read_after_write(existing)
            return existing, True
        state = self.read_state()
        try:
            _require_matching_pre_state(state, validated.local_state)
        except DirectTrafficPreproductionConflictError:
            raced = self.lookup(request.idempotency_key)
            if raced is None:
                raise
            self._validate_result(raced, validated)
            self._verify_read_after_write(raced)
            return raced, True
        content = _canonical_json(dict(validated.payload))
        try:
            response = self._transport(
                "PUT",
                self._apply_url(request.idempotency_key),
                self.config.token,
                self.config.timeout_seconds,
                self.config.max_response_bytes,
                content,
            )
            result = self._interpret_apply_response(response, validated)
        except PreproductionProviderError as exc:
            if not exc.indeterminate:
                raise
            result = self._reconcile_after_indeterminate(request.idempotency_key, attempts)
        self._validate_result(result, validated)
        if result.outcome == "conflict":
            raise DirectTrafficPreproductionConflictError(
                "preproduction CAS rejected stale traffic state"
            )
        self._verify_read_after_write(result)
        return result, False

    def _validate_authorization(
        self, authorization: DirectTrafficExecutionAuthorization
    ) -> DirectTrafficExecutionAuthorization:
        if not isinstance(authorization, VerifiedPreproductionExecution):
            raise DirectTrafficPreproductionError(
                "invalid preproduction execution authorization"
            )
        return _validate_execution(authorization, self)

    def _reconcile_after_indeterminate(
        self, idempotency_key: str, attempts: int
    ) -> PreproductionResult:
        for attempt in range(1, attempts + 1):
            result = self.lookup(idempotency_key)
            if result is not None:
                return result
            if attempt < attempts:
                self._sleep(self._retry_delay(attempt, None))
        raise DirectTrafficPreproductionIndeterminateError(
            "preproduction write outcome is unknown; PUT was not retried"
        )

    def _verify_read_after_write(self, result: PreproductionResult) -> None:
        state = self.read_state()
        if (
            state.current_traffic_percent != result.current_traffic_percent
            or state.revision != result.revision
            or state.operations != result.operations
            or state.history_chain_sha256 != result.history_chain_sha256
        ):
            raise DirectTrafficPreproductionIndeterminateError(
                "preproduction write-after-read did not confirm the operation"
            )

    def _read_request(self, url: str) -> TransportResponse:
        for attempt in range(1, self.config.max_read_attempts + 1):
            try:
                response = self._transport(
                    "GET",
                    url,
                    self.config.token,
                    self.config.timeout_seconds,
                    self.config.max_response_bytes,
                    None,
                )
                self._validate_transport_response(response, url)
                if response.status in {
                    HTTPStatus.REQUEST_TIMEOUT,
                    HTTPStatus.TOO_MANY_REQUESTS,
                    HTTPStatus.INTERNAL_SERVER_ERROR,
                    HTTPStatus.BAD_GATEWAY,
                    HTTPStatus.SERVICE_UNAVAILABLE,
                    HTTPStatus.GATEWAY_TIMEOUT,
                }:
                    raise PreproductionProviderError(
                        f"provider_http_{response.status}",
                        retryable=True,
                        retry_after=response.retry_after,
                    )
                return response
            except PreproductionProviderError as exc:
                if not exc.retryable or attempt >= self.config.max_read_attempts:
                    raise
                self._sleep(self._retry_delay(attempt, exc.retry_after))
        raise AssertionError("preproduction read attempt loop did not terminate")

    def _interpret_apply_response(
        self,
        response: TransportResponse,
        authorization: DirectTrafficExecutionAuthorization,
    ) -> PreproductionResult:
        requested_url = self._apply_url(authorization.request.idempotency_key)
        self._validate_transport_response(response, requested_url)
        if response.status in {
            HTTPStatus.REQUEST_TIMEOUT,
            HTTPStatus.TOO_MANY_REQUESTS,
            HTTPStatus.INTERNAL_SERVER_ERROR,
            HTTPStatus.BAD_GATEWAY,
            HTTPStatus.SERVICE_UNAVAILABLE,
            HTTPStatus.GATEWAY_TIMEOUT,
        }:
            raise PreproductionProviderError(f"provider_http_{response.status}", indeterminate=True)
        if response.status not in {HTTPStatus.OK, HTTPStatus.CONFLICT}:
            raise PreproductionProviderError(f"provider_http_{response.status}")
        result = self._parse_result(response)
        if response.status == HTTPStatus.CONFLICT and result.outcome != "conflict":
            raise PreproductionProviderError("provider_contract")
        return result

    def _parse_state(self, response: TransportResponse) -> PreproductionState:
        value = self._decode_json(response)
        state = self._validate_provider_state(value)
        self._validate_state_binding(state)
        return state

    def _parse_result(self, response: TransportResponse) -> PreproductionResult:
        value = self._decode_json(response)
        return self._validate_provider_result(value)

    def _validate_provider_state(self, value: Any) -> PreproductionState:
        return _validate_state(value)

    def _validate_provider_result(self, value: Any) -> PreproductionResult:
        return _validate_result(value)

    def _decode_json(self, response: TransportResponse) -> Any:
        if len(response.body) > self.config.max_response_bytes:
            raise PreproductionProviderError("provider_response_too_large")
        if response.content_type.split(";", 1)[0].strip().lower() != "application/json":
            raise PreproductionProviderError("provider_content_type")
        try:
            return json.loads(
                response.body.decode("utf-8"), object_pairs_hook=_reject_duplicate_keys
            )
        except (UnicodeDecodeError, json.JSONDecodeError, ValueError) as exc:
            raise PreproductionProviderError("provider_invalid_json") from exc

    def _validate_state_binding(self, state: PreproductionState) -> None:
        if (
            state.provider_instance_sha256 != self.provider_instance_sha256
            or state.environment_sha256 != self.environment_sha256
            or state.namespace_sha256 != self.namespace_sha256
        ):
            raise PreproductionProviderError("provider_binding_mismatch")

    def _validate_result(
        self,
        result: PreproductionResult,
        authorization: DirectTrafficExecutionAuthorization,
    ) -> None:
        request = authorization.request
        if (
            result.operation_id != request.operation_id
            or result.idempotency_key != request.idempotency_key
            or result.request_sha256 != authorization.payload["request_sha256"]
        ):
            raise PreproductionProviderError("provider_result_binding")
        if result.outcome != "conflict" and (
            result.current_traffic_percent != request.target_traffic_percent
            or result.revision
            != request.expected_revision
            + int(request.target_traffic_percent != request.expected_traffic_percent)
        ):
            raise PreproductionProviderError("provider_result_state")

    def _validate_transport_response(self, response: TransportResponse, requested_url: str) -> None:
        if response.final_url != requested_url:
            raise PreproductionProviderError("provider_redirect_rejected")
        if len(response.body) > self.config.max_response_bytes:
            raise PreproductionProviderError("provider_response_too_large")

    def _base_namespace_url(self) -> str:
        encoded = urllib.parse.quote(self.config.namespace_id, safe="")
        return f"{self.config.base_url}/v1/namespaces/{encoded}/direct-traffic"

    def _state_url(self) -> str:
        return f"{self._base_namespace_url()}/state"

    def _lookup_url(self, idempotency_key: str) -> str:
        return f"{self._base_namespace_url()}/changes/by-idempotency-key/{idempotency_key}"

    def _apply_url(self, idempotency_key: str) -> str:
        return f"{self._base_namespace_url()}/changes/{idempotency_key}"

    def _retry_delay(self, attempt: int, retry_after: str | None) -> float:
        if retry_after is not None and retry_after.isdigit():
            return min(float(retry_after), MAX_RETRY_DELAY_SECONDS)
        return float(
            min(
                self.config.retry_base_seconds * (2 ** (attempt - 1)),
                MAX_RETRY_DELAY_SECONDS,
            )
        )


def preproduction_namespace_sha256(namespace_id: str) -> str:
    namespace = _validate_namespace(namespace_id)
    return hashlib.sha256(_NAMESPACE_DOMAIN + namespace.encode("utf-8")).hexdigest()


def verify_preproduction_execution(
    adapter: HTTPSPreproductionDirectTrafficAdapter,
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
    *,
    max_age_seconds: int = 900,
    now: datetime | None = None,
    execution_extras: Mapping[str, Any] | None = None,
) -> VerifiedPreproductionExecution:
    current_time = _normalize_now(now)
    if (
        sandbox.provider_instance_sha256 != adapter.provider_instance_sha256
        or sandbox.environment_sha256 != adapter.environment_sha256
        or shadow_history.observer_instance_sha256 != adapter.observer_instance_sha256
        or shadow_history.provider_instance_sha256 != adapter.provider_instance_sha256
        or shadow_history.environment_sha256 != adapter.environment_sha256
    ):
        raise DirectTrafficPreproductionConfigError(
            "local desired state or shadow history is not bound to the preproduction adapter"
        )
    request = build_traffic_request(
        sandbox,
        decision_dir,
        release_history,
        policy,
        policy_sha256,
        rollout_key,
        rollout_key_id,
        max_age_seconds=max_age_seconds,
        now=current_time,
    )
    rollout_attestation = verify_rollout_attestation(
        decision_dir,
        release_history,
        policy,
        policy_sha256,
        rollout_key,
        rollout_key_id,
        max_age_seconds=max_age_seconds,
        require_decision="any",
        now=current_time,
    )
    if request.decision == "hold":
        raise DirectTrafficPreproductionConfigError(
            "hold decisions cannot authorize preproduction writes"
        )
    certification = verify_certification(
        certification_path,
        adapter,
        certification_key,
        certification_key_id,
        require_certification_namespace=False,
        now=current_time,
    )
    validate_execution_binding(certification, adapter)
    shadow_attestation = verify_direct_traffic_shadow_attestation(
        shadow_gate_dir,
        shadow_history,
        shadow_key,
        shadow_key_id,
        max_age_seconds=max_age_seconds,
        require_decision="pass",
        now=current_time,
    )
    local_state = sandbox.status()
    _validate_latest_shadow_match(shadow_history, local_state)
    certification_sha256 = _digest(_canonical_json(certification))
    shadow_sha256 = _digest(_canonical_json(shadow_attestation))
    payload: dict[str, Any] = {
        "schema_version": adapter.execution_schema_version,
        "namespace_sha256": adapter.namespace_sha256,
        "rollout_request": asdict(request),
        "rollout_attestation": dict(rollout_attestation),
        "adapter_certification": dict(certification),
        "adapter_certification_sha256": certification_sha256,
        "shadow_attestation": dict(shadow_attestation),
        "shadow_attestation_sha256": shadow_sha256,
        "expected_state": _public_state(local_state),
        "request_sha256": "",
    }
    extras = dict(execution_extras or {})
    if set(extras) != adapter.execution_extra_fields or set(extras) & set(payload):
        raise DirectTrafficPreproductionConfigError(
            "preproduction execution extension fields are invalid"
        )
    payload.update(extras)
    payload["request_sha256"] = _digest(
        _canonical_json({key: value for key, value in payload.items() if key != "request_sha256"})
    )
    return _validate_execution(
        VerifiedPreproductionExecution(
            request=request,
            rollout_attestation=rollout_attestation,
            certification=certification,
            certification_sha256=certification_sha256,
            shadow_attestation=shadow_attestation,
            shadow_attestation_sha256=shadow_sha256,
            local_state=local_state,
            payload=payload,
        ),
        adapter,
    )


def apply_verified_preproduction_execution(
    adapter: HTTPSPreproductionDirectTrafficAdapter,
    sandbox: SQLiteDirectTrafficSandbox,
    release_history_path: Path,
    authorization: VerifiedPreproductionExecution,
) -> dict[str, Any]:
    validated = _validate_execution(authorization, adapter)
    remote, reused_remote = adapter.execute(validated)
    applied_at = _parse_timestamp(remote.applied_at, "preproduction applied_at")
    local = sandbox.apply(
        validated.request,
        release_history_path,
        str(validated.certification["certification_id"]),
        validated.certification_sha256,
        now=applied_at,
    )
    status = sandbox.status()
    if (
        status["current_traffic_percent"] != remote.current_traffic_percent
        or status["revision"] != remote.revision
        or status["operations"] != remote.operations
        or status["history_chain_sha256"] != remote.history_chain_sha256
    ):
        raise DirectTrafficPreproductionIndeterminateError(
            "remote and local direct traffic chains did not converge"
        )
    return {
        "schema_version": "agent-direct-traffic-preproduction-receipt-v1",
        "namespace_sha256": adapter.namespace_sha256,
        "operation_id": remote.operation_id,
        "decision_id": validated.request.decision_id,
        "outcome": remote.outcome,
        "current_traffic_percent": remote.current_traffic_percent,
        "revision": remote.revision,
        "history_chain_sha256": remote.history_chain_sha256,
        "adapter_certification_id": validated.certification["certification_id"],
        "shadow_attestation_id": validated.shadow_attestation["attestation_id"],
        "remote_reused": reused_remote,
        "local_reused": bool(local["reused"]),
        "applied_at": remote.applied_at,
    }


def _validate_latest_shadow_match(
    history: DirectTrafficShadowHistory, local_state: Mapping[str, Any]
) -> None:
    events = history.events()
    if not events:
        raise DirectTrafficPreproductionConfigError(
            "preproduction write requires shadow observations"
        )
    report = events[-1]["report"]
    expected = {
        "current_traffic_percent": int(local_state["current_traffic_percent"]),
        "revision": int(local_state["revision"]),
        "history_chain_sha256": str(local_state["history_chain_sha256"]),
    }
    if (
        report["outcome"] != "match"
        or report["local"] != expected
        or {
            "current_traffic_percent": report["provider"]["current_traffic_percent"],
            "revision": report["provider"]["revision"],
            "history_chain_sha256": report["provider"]["history_chain_sha256"],
        }
        != expected
    ):
        raise DirectTrafficPreproductionConflictError(
            "latest shadow observation does not match current local desired state"
        )


def _validate_execution(
    value: VerifiedPreproductionExecution,
    adapter: HTTPSPreproductionDirectTrafficAdapter,
) -> VerifiedPreproductionExecution:
    if not isinstance(value, VerifiedPreproductionExecution):
        raise DirectTrafficPreproductionConfigError("preproduction execution type is invalid")
    payload = value.payload
    base_fields = {
        "schema_version",
        "namespace_sha256",
        "rollout_request",
        "rollout_attestation",
        "adapter_certification",
        "adapter_certification_sha256",
        "shadow_attestation",
        "shadow_attestation_sha256",
        "expected_state",
        "request_sha256",
    }
    if not isinstance(payload, Mapping) or set(payload) != base_fields | adapter.execution_extra_fields:
        raise DirectTrafficPreproductionConfigError("preproduction execution payload is invalid")
    if payload["schema_version"] != adapter.execution_schema_version:
        raise DirectTrafficPreproductionConfigError("preproduction execution schema is invalid")
    expected = _digest(
        _canonical_json({key: item for key, item in payload.items() if key != "request_sha256"})
    )
    if (
        payload["request_sha256"] != expected
        or payload["namespace_sha256"] != adapter.namespace_sha256
        or payload["rollout_request"] != asdict(value.request)
        or payload["rollout_attestation"] != dict(value.rollout_attestation)
        or value.request.attestation_id != value.rollout_attestation.get("attestation_id")
        or value.request.attestation_sha256
        != _digest(_canonical_json(dict(value.rollout_attestation)))
        or payload["adapter_certification"] != dict(value.certification)
        or payload["adapter_certification_sha256"] != value.certification_sha256
        or payload["shadow_attestation"] != dict(value.shadow_attestation)
        or payload["shadow_attestation_sha256"] != value.shadow_attestation_sha256
        or payload["expected_state"] != _public_state(value.local_state)
        or value.certification_sha256 != _digest(_canonical_json(dict(value.certification)))
        or value.shadow_attestation_sha256
        != _digest(_canonical_json(dict(value.shadow_attestation)))
    ):
        raise DirectTrafficPreproductionConfigError(
            "preproduction execution identity binding is invalid"
        )
    return value


def _require_matching_pre_state(remote: PreproductionState, local: Mapping[str, Any]) -> None:
    expected = _public_state(local)
    if (
        remote.current_traffic_percent != expected["current_traffic_percent"]
        or remote.revision != expected["revision"]
        or remote.operations != int(local["operations"])
        or remote.history_chain_sha256 != expected["history_chain_sha256"]
    ):
        raise DirectTrafficPreproductionConflictError(
            "preproduction state does not match local desired state"
        )


def _public_state(value: Mapping[str, Any]) -> dict[str, Any]:
    return {
        "current_traffic_percent": int(value["current_traffic_percent"]),
        "revision": int(value["revision"]),
        "operations": int(value.get("operations", 0)),
        "history_chain_sha256": str(value["history_chain_sha256"]),
    }


def _validate_config(value: HTTPSPreproductionConfig) -> HTTPSPreproductionConfig:
    _validate_namespace(value.namespace_id)
    try:
        observer_instance_sha256(_shadow_observer_config(value))
    except DirectTrafficShadowError as exc:
        raise DirectTrafficPreproductionConfigError(
            "preproduction provider endpoint is invalid"
        ) from exc
    try:
        parsed = urllib.parse.urlsplit(value.base_url)
        port = parsed.port
    except ValueError as exc:
        raise DirectTrafficPreproductionConfigError("preproduction base URL is invalid") from exc
    if port not in {None, 443}:
        raise DirectTrafficPreproductionConfigError(
            "preproduction endpoint requires HTTPS port 443"
        )
    _validate_int(value.max_read_attempts, "max read attempts", 1, MAX_READ_ATTEMPTS)
    return value


def _shadow_observer_config(
    value: HTTPSPreproductionConfig,
) -> ReadOnlyDirectTrafficProviderConfig:
    namespace = urllib.parse.quote(value.namespace_id, safe="")
    return ReadOnlyDirectTrafficProviderConfig(
        name=ADAPTER_NAME,
        base_url=value.base_url,
        allowed_host=value.allowed_host,
        provider_instance=value.provider_instance,
        environment_sha256=value.environment_sha256,
        token=value.token,
        state_path=(f"/v1/namespaces/{namespace}/direct-traffic/shadow-state"),
        timeout_seconds=value.timeout_seconds,
        max_attempts=value.max_read_attempts,
        retry_base_seconds=value.retry_base_seconds,
        max_response_bytes=value.max_response_bytes,
    )


def _validate_state(
    value: Any, *, schema_version: str = STATE_SCHEMA_VERSION
) -> PreproductionState:
    fields = {
        "schema_version",
        "provider_instance_sha256",
        "environment_sha256",
        "namespace_sha256",
        "current_traffic_percent",
        "revision",
        "operations",
        "history_chain_sha256",
        "observed_at",
    }
    if not isinstance(value, Mapping) or set(value) != fields:
        raise PreproductionProviderError("provider_contract")
    if value["schema_version"] != schema_version:
        raise PreproductionProviderError("provider_contract")
    return PreproductionState(
        provider_instance_sha256=_validate_digest(
            value["provider_instance_sha256"], "provider instance"
        ),
        environment_sha256=_validate_digest(value["environment_sha256"], "environment"),
        namespace_sha256=_validate_digest(value["namespace_sha256"], "namespace"),
        current_traffic_percent=_validate_int(value["current_traffic_percent"], "traffic", 0, 100),
        revision=_validate_int(value["revision"], "revision", 0, 1_000_000_000),
        operations=_validate_int(value["operations"], "operations", 0, 100_000),
        history_chain_sha256=_validate_digest(value["history_chain_sha256"], "chain"),
        observed_at=_format_timestamp(_parse_timestamp(str(value["observed_at"]), "observed_at")),
    )


def _validate_result(
    value: Any, *, schema_version: str = RESULT_SCHEMA_VERSION
) -> PreproductionResult:
    fields = {
        "schema_version",
        "operation_id",
        "idempotency_key",
        "request_sha256",
        "outcome",
        "current_traffic_percent",
        "revision",
        "operations",
        "history_chain_sha256",
        "applied_at",
    }
    if not isinstance(value, Mapping) or set(value) != fields:
        raise PreproductionProviderError("provider_contract")
    if value["schema_version"] != schema_version:
        raise PreproductionProviderError("provider_contract")
    outcome = value["outcome"]
    if outcome not in {"applied", "no_op", "conflict"}:
        raise PreproductionProviderError("provider_contract")
    operation_id = value["operation_id"]
    if (
        not isinstance(operation_id, str)
        or re.fullmatch(r"direct-traffic-op-[0-9a-f]{32}", operation_id) is None
    ):
        raise PreproductionProviderError("provider_contract")
    return PreproductionResult(
        operation_id=operation_id,
        idempotency_key=_validate_digest(value["idempotency_key"], "idempotency"),
        request_sha256=_validate_digest(value["request_sha256"], "request"),
        outcome=outcome,
        current_traffic_percent=_validate_int(value["current_traffic_percent"], "traffic", 0, 100),
        revision=_validate_int(value["revision"], "revision", 0, 1_000_000_000),
        operations=_validate_int(value["operations"], "operations", 0, 100_000),
        history_chain_sha256=_validate_digest(value["history_chain_sha256"], "chain"),
        applied_at=_format_timestamp(_parse_timestamp(str(value["applied_at"]), "applied_at")),
    )


def _validate_namespace(value: Any) -> str:
    if not isinstance(value, str) or _NAMESPACE_RE.fullmatch(value) is None:
        raise DirectTrafficPreproductionConfigError(
            "namespace must use the explicit preproduction- prefix"
        )
    return value


def _validate_digest(value: Any, label: str) -> str:
    if not isinstance(value, str) or _DIGEST_RE.fullmatch(value) is None:
        raise DirectTrafficPreproductionConfigError(f"{label} digest is invalid")
    return value


def _validate_int(value: Any, label: str, minimum: int, maximum: int) -> int:
    if isinstance(value, bool) or not isinstance(value, int) or not minimum <= value <= maximum:
        raise DirectTrafficPreproductionConfigError(f"{label} is invalid")
    return value


def _reject_duplicate_keys(pairs: list[tuple[str, Any]]) -> dict[str, Any]:
    result: dict[str, Any] = {}
    for key, value in pairs:
        if key in result:
            raise ValueError(f"duplicate JSON key: {key}")
        result[key] = value
    return result


def _default_transport(
    method: Literal["GET", "PUT"],
    url: str,
    token: str,
    timeout_seconds: float,
    max_response_bytes: int,
    body: bytes | None,
) -> TransportResponse:
    if method == "GET" and body is not None:
        raise PreproductionProviderError("get_body_rejected")
    if method == "PUT" and body is None:
        raise PreproductionProviderError("put_body_required")
    headers = {
        "Accept": "application/json",
        "Authorization": f"Bearer {token}",
        "User-Agent": "ai-companion-direct-traffic-preproduction/1.0",
    }
    if body is not None:
        headers["Content-Type"] = "application/json"
    request = urllib.request.Request(url, data=body, headers=headers, method=method)
    opener = urllib.request.build_opener(_NoRedirectHandler())
    try:
        with opener.open(request, timeout=timeout_seconds) as response:
            return TransportResponse(
                status=int(response.status),
                body=response.read(max_response_bytes + 1),
                content_type=str(response.headers.get("Content-Type", "")),
                final_url=str(response.geturl()),
                retry_after=response.headers.get("Retry-After"),
            )
    except urllib.error.HTTPError as exc:
        return TransportResponse(
            status=int(exc.code),
            body=exc.read(max_response_bytes + 1),
            content_type=str(exc.headers.get("Content-Type", "")),
            final_url=str(exc.geturl()),
            retry_after=exc.headers.get("Retry-After"),
        )
    except (urllib.error.URLError, TimeoutError, OSError) as exc:
        raise PreproductionProviderError(
            "provider_transport_error",
            retryable=method == "GET",
            indeterminate=method == "PUT",
        ) from exc


class _NoRedirectHandler(urllib.request.HTTPRedirectHandler):
    def redirect_request(
        self,
        req: urllib.request.Request,
        fp: Any,
        code: int,
        msg: str,
        headers: Any,
        newurl: str,
    ) -> urllib.request.Request | None:
        del req, fp, code, msg, headers, newurl
        return None


def _key_from_environment(name: str, key_id_name: str) -> tuple[bytes, str]:
    value = os.environ.get(name)
    key_id = os.environ.get(key_id_name)
    if value is None or key_id is None:
        raise DirectTrafficPreproductionConfigError(f"{name} and {key_id_name} are required")
    return value.encode("utf-8"), key_id


def main(argv: Sequence[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        description="Apply a triple-proven traffic change only to preproduction"
    )
    parser.add_argument("--sandbox-ledger", type=Path, required=True)
    parser.add_argument("--release-history-ledger", type=Path, required=True)
    parser.add_argument("--decision-dir", type=Path, required=True)
    parser.add_argument("--policy", type=Path, required=True)
    parser.add_argument("--adapter-certification", type=Path, required=True)
    parser.add_argument("--shadow-history-ledger", type=Path, required=True)
    parser.add_argument("--shadow-gate-dir", type=Path, required=True)
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
        environment_digest = environment_sha256(args.environment_id)
        adapter = HTTPSPreproductionDirectTrafficAdapter(
            HTTPSPreproductionConfig(
                base_url=args.provider_base_url,
                allowed_host=args.allowed_host,
                provider_instance=args.provider_instance,
                environment_sha256=environment_digest,
                namespace_id=args.namespace_id,
                token=token,
                timeout_seconds=args.timeout_seconds,
            )
        )
        lease = try_acquire_canary_lease(args.canary_lease_root, args.environment_id)
        if lease is None:
            raise DirectTrafficPreproductionConflictError(
                "another environment operation holds the shared lease"
            )
        sandbox = SQLiteDirectTrafficSandbox(
            args.sandbox_ledger, environment_digest, args.provider_instance
        )
        release_history = ReleaseGateHistory(
            args.release_history_ledger, args.environment_id, create=False
        )
        policy, policy_sha256 = load_policy(args.policy)
        shadow_config = ReadOnlyDirectTrafficProviderConfig(
            name=ADAPTER_NAME,
            base_url=args.provider_base_url,
            allowed_host=args.allowed_host,
            provider_instance=args.provider_instance,
            environment_sha256=environment_digest,
            token=token,
            state_path=_shadow_observer_config(adapter.config).state_path,
        )
        shadow_history = DirectTrafficShadowHistory(
            args.shadow_history_ledger,
            observer_instance_sha256(shadow_config),
            adapter.provider_instance_sha256,
            environment_digest,
            create=False,
        )
        rollout_key, rollout_key_id = _key_from_environment(ROLLOUT_KEY_ENV, ROLLOUT_KEY_ID_ENV)
        certification_key, certification_key_id = certification_key_from_environment()
        shadow_key, shadow_key_id = _key_from_environment(
            SHADOW_GATE_KEY_ENV, SHADOW_GATE_KEY_ID_ENV
        )
        authorization = verify_preproduction_execution(
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
            max_age_seconds=args.max_age_seconds,
        )
        receipt = apply_verified_preproduction_execution(
            adapter, sandbox, release_history.path, authorization
        )
        print(json.dumps(receipt, sort_keys=True))
        return 0
    except DirectTrafficPreproductionConflictError as exc:
        print(f"direct traffic preproduction conflict: {exc}", file=sys.stderr)
        return 4
    except DirectTrafficPreproductionIndeterminateError as exc:
        print(f"direct traffic preproduction indeterminate: {exc}", file=sys.stderr)
        return 5
    except DirectTrafficPreproductionConfigError as exc:
        print(f"direct traffic preproduction configuration error: {exc}", file=sys.stderr)
        return 2
    except (
        DirectTrafficPreproductionError,
        PreproductionProviderError,
        DirectTrafficError,
        DirectRolloutError,
        DirectTrafficCertificationError,
        DirectTrafficShadowError,
        DirectTrafficShadowGateError,
        ReleaseGateHistoryError,
        ReleaseGateError,
        DeploymentControllerError,
        OSError,
        sqlite3.Error,
    ) as exc:
        print(f"direct traffic preproduction verification error: {exc}", file=sys.stderr)
        return 1
    finally:
        if lease is not None:
            lease.release()


if __name__ == "__main__":
    raise SystemExit(main())
