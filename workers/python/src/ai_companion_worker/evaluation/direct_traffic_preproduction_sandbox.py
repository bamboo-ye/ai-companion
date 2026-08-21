from __future__ import annotations

import argparse
import hashlib
import hmac
import json
import secrets
import sqlite3
import sys
import urllib.parse
from contextlib import closing
from datetime import UTC, datetime
from pathlib import Path
from typing import Any, Callable, Literal, Mapping, Sequence

from .deployment_controller import (
    DeploymentControllerError,
    _canonical_json,
    _digest,
    _ensure_private_database,
    _format_timestamp,
    _parse_timestamp,
    _validate_private_database,
)
from .direct_rollout_traffic import (
    TrafficChangeRequest,
    _chain_digest,
    _validate_request,
    provider_instance_sha256,
)
from .direct_traffic_certification import (
    DirectTrafficCertificationError,
    SQLiteDirectTrafficCertificationAdapter,
    certification_key_from_environment,
    create_certification,
    verify_certification,
)
from .direct_traffic_preproduction import (
    ADAPTER_NAME,
    EXECUTION_SCHEMA_VERSION,
    IMPLEMENTATION_VERSION,
    RESULT_SCHEMA_VERSION,
    STATE_SCHEMA_VERSION,
    DirectTrafficPreproductionConfigError,
    HTTPSPreproductionConfig,
    PreproductionProviderError,
    PreproductionTransport,
    TransportResponse,
    _shadow_observer_config,
    preproduction_namespace_sha256,
)
from .direct_traffic_shadow import ReadOnlyDirectTrafficProviderConfig, observer_instance_sha256
from .release_gate_history import environment_sha256


_SCHEMA_VERSION = 1
_ZERO_CHAIN = "0" * 64
_ROLLOUT_SIGNING_DOMAIN = b"ai-companion/agent-direct-rollout-attestation/v1"
_CERTIFICATION_SIGNING_DOMAIN = b"ai-companion/agent-direct-traffic-adapter-certification/v1"
_SHADOW_SIGNING_DOMAIN = b"ai-companion/direct-traffic-shadow-attestation/v1\0"
_TABLES = {"preproduction_metadata", "preproduction_operations"}
_TRIGGERS = {
    "preproduction_operations_no_update",
    "preproduction_operations_no_delete",
}


class SQLitePreproductionCertificationAdapter(SQLiteDirectTrafficCertificationAdapter):
    """Certification namespace with the same identity as the HTTPS executor."""

    name = ADAPTER_NAME
    implementation_version = IMPLEMENTATION_VERSION


class SQLitePreproductionPlatform:
    """Local-only platform state machine implementing the remote HTTPS contract."""

    adapter_name = ADAPTER_NAME
    implementation_version = IMPLEMENTATION_VERSION
    execution_schema_version = EXECUTION_SCHEMA_VERSION
    state_schema_version = STATE_SCHEMA_VERSION
    result_schema_version = RESULT_SCHEMA_VERSION
    execution_extra_fields: frozenset[str] = frozenset()

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
        shadow_key: bytes | None,
        shadow_key_id: str | None,
        *,
        initial_traffic_percent: int,
        base_url: str = "https://traffic.provider.example/api",
        now: Callable[[], datetime] = lambda: datetime.now(UTC),
    ) -> None:
        self.path = path
        self.environment_sha256 = environment_sha256
        self.provider_instance_sha256 = provider_instance_sha256(provider_instance)
        self.namespace_id = namespace_id
        self.namespace_sha256 = self._namespace_sha256(namespace_id)
        self.token = token
        parsed_base_url = urllib.parse.urlsplit(base_url)
        if parsed_base_url.hostname is None:
            raise DirectTrafficPreproductionConfigError("preproduction sandbox base URL is invalid")
        observer_config = HTTPSPreproductionConfig(
            base_url=base_url,
            allowed_host=parsed_base_url.hostname,
            provider_instance=provider_instance,
            environment_sha256=environment_sha256,
            namespace_id=namespace_id,
            token=token,
        )
        self.observer_instance_sha256 = observer_instance_sha256(
            self._observer_config(observer_config)
        )
        self._trusted_proofs = self._proof_trust(
            rollout_key,
            rollout_key_id,
            certification_key,
            certification_key_id,
            shadow_key,
            shadow_key_id,
        )
        self.base_url = base_url
        self.now = now
        self.write_calls = 0
        self.read_calls = 0
        self.timeout_after_next_commit = False
        self.corrupt_next_state_read = False
        try:
            _ensure_private_database(path)
        except DeploymentControllerError as exc:
            raise DirectTrafficPreproductionConfigError(
                "preproduction sandbox database is not private"
            ) from exc
        self._initialize(initial_traffic_percent)

    def _proof_trust(
        self,
        rollout_key: bytes,
        rollout_key_id: str,
        certification_key: bytes,
        certification_key_id: str,
        shadow_key: bytes | None,
        shadow_key_id: str | None,
    ) -> tuple[tuple[bytes, str, bytes], ...]:
        if shadow_key is None or shadow_key_id is None:
            raise DirectTrafficPreproductionConfigError(
                "preproduction shadow proof trust is required"
            )
        return (
            (rollout_key, rollout_key_id, _ROLLOUT_SIGNING_DOMAIN),
            (certification_key, certification_key_id, _CERTIFICATION_SIGNING_DOMAIN),
            (shadow_key, shadow_key_id, _SHADOW_SIGNING_DOMAIN),
        )

    def _namespace_sha256(self, namespace_id: str) -> str:
        return preproduction_namespace_sha256(namespace_id)

    def _observer_config(
        self, config: HTTPSPreproductionConfig
    ) -> ReadOnlyDirectTrafficProviderConfig:
        return _shadow_observer_config(config)

    @property
    def transport(self) -> PreproductionTransport:
        return self._transport

    def status(self) -> dict[str, Any]:
        with closing(self._connect(read_only=True)) as connection:
            connection.execute("BEGIN")
            metadata, operations = self._verified_state(connection)
        return self._state_payload(metadata, operations)

    def operation(self, idempotency_key: str) -> dict[str, Any] | None:
        with closing(self._connect(read_only=True)) as connection:
            connection.execute("BEGIN")
            self._verified_state(connection)
            row = connection.execute(
                "SELECT result_json FROM preproduction_operations WHERE idempotency_key = ?",
                (idempotency_key,),
            ).fetchone()
        return json.loads(str(row["result_json"])) if row is not None else None

    def _transport(
        self,
        method: Literal["GET", "PUT"],
        url: str,
        token: str,
        timeout_seconds: float,
        max_response_bytes: int,
        body: bytes | None,
    ) -> TransportResponse:
        del timeout_seconds, max_response_bytes
        if token != self.token:
            return self._response(401, {"error": "unauthorized"}, url)
        parsed = urllib.parse.urlsplit(url)
        expected_origin = urllib.parse.urlsplit(self.base_url)
        if (
            parsed.scheme != expected_origin.scheme
            or parsed.netloc != expected_origin.netloc
            or parsed.query
            or parsed.fragment
        ):
            return self._response(404, {"error": "not_found"}, url)
        prefix = f"{expected_origin.path}/v1/namespaces/{self.namespace_id}/direct-traffic"
        if method == "GET":
            self.read_calls += 1
            if body is not None:
                return self._response(400, {"error": "get_body"}, url)
            if parsed.path == f"{prefix}/state":
                payload = self.status()
                if self.corrupt_next_state_read:
                    self.corrupt_next_state_read = False
                    payload = dict(payload)
                    payload["revision"] = int(payload["revision"]) + 1
                return self._response(200, payload, url)
            if parsed.path == f"{prefix}/shadow-state":
                payload = self.status()
                payload = {
                    "schema_version": "agent-direct-traffic-provider-state-v1",
                    "provider_instance_sha256": payload["provider_instance_sha256"],
                    "environment_sha256": payload["environment_sha256"],
                    "current_traffic_percent": payload["current_traffic_percent"],
                    "revision": payload["revision"],
                    "history_chain_sha256": payload["history_chain_sha256"],
                    "observed_at": payload["observed_at"],
                }
                return self._response(200, payload, url)
            lookup_prefix = f"{prefix}/changes/by-idempotency-key/"
            if parsed.path.startswith(lookup_prefix):
                result = self.operation(parsed.path[len(lookup_prefix) :])
                return self._response(
                    200 if result is not None else 404,
                    result or {"error": "not_found"},
                    url,
                )
            return self._response(404, {"error": "not_found"}, url)
        if method != "PUT" or body is None:
            return self._response(405, {"error": "method_not_allowed"}, url)
        apply_prefix = f"{prefix}/changes/"
        if not parsed.path.startswith(apply_prefix):
            return self._response(404, {"error": "not_found"}, url)
        self.write_calls += 1
        idempotency_key = parsed.path[len(apply_prefix) :]
        try:
            payload = json.loads(body.decode("utf-8"))
            result, status = self._apply(idempotency_key, payload)
        except (UnicodeDecodeError, json.JSONDecodeError, ValueError) as exc:
            raise PreproductionProviderError("provider_contract") from exc
        if self.timeout_after_next_commit and status == 200:
            self.timeout_after_next_commit = False
            raise PreproductionProviderError("provider_transport_error", indeterminate=True)
        return self._response(status, result, url)

    def _apply(self, idempotency_key: str, payload: Any) -> tuple[dict[str, Any], int]:
        validated, request, request_sha256 = self._validate_execution_payload(
            idempotency_key, payload
        )
        timestamp = _format_timestamp(self.now())
        with closing(self._connect()) as connection, connection:
            connection.execute("BEGIN IMMEDIATE")
            metadata, operations = self._verified_state(connection)
            existing = connection.execute(
                "SELECT * FROM preproduction_operations WHERE idempotency_key = ?",
                (idempotency_key,),
            ).fetchone()
            if existing is not None:
                if str(existing["request_sha256"]) != request_sha256:
                    raise PreproductionProviderError("provider_idempotency_conflict")
                connection.commit()
                return json.loads(str(existing["result_json"])), 200
            current = int(metadata["current_traffic_percent"])
            revision = int(metadata["revision"])
            chain = str(metadata["history_chain_sha256"])
            if (
                current != request.expected_traffic_percent
                or revision != request.expected_revision
                or chain != validated["expected_state"]["history_chain_sha256"]
                or len(operations) != validated["expected_state"]["operations"]
            ):
                result = self._result_payload(
                    request,
                    request_sha256,
                    "conflict",
                    current,
                    revision,
                    len(operations),
                    chain,
                    timestamp,
                )
                connection.commit()
                return result, 409
            outcome: Literal["applied", "no_op"] = (
                "no_op" if request.target_traffic_percent == current else "applied"
            )
            new_revision = revision + int(outcome == "applied")
            operation_payload = {
                "environment_sha256": self.environment_sha256,
                "provider_instance_sha256": self.provider_instance_sha256,
                "sequence": len(operations) + 1,
                "operation_id": request.operation_id,
                "decision_id": request.decision_id,
                "idempotency_key": request.idempotency_key,
                "attestation_id": request.attestation_id,
                "attestation_sha256": request.attestation_sha256,
                "history_chain_sha256": request.history_chain_sha256,
                "adapter_certification_id": validated["adapter_certification"]["certification_id"],
                "adapter_certification_sha256": validated["adapter_certification_sha256"],
                "key_id": request.key_id,
                "decision": request.decision,
                "expected_traffic_percent": request.expected_traffic_percent,
                "target_traffic_percent": request.target_traffic_percent,
                "previous_revision": revision,
                "new_revision": new_revision,
                "outcome": outcome,
                "occurred_at": timestamp,
                "previous_chain_sha256": chain,
            }
            new_chain = _chain_digest(chain, operation_payload)
            result = self._result_payload(
                request,
                request_sha256,
                outcome,
                request.target_traffic_percent,
                new_revision,
                len(operations) + 1,
                new_chain,
                timestamp,
            )
            connection.execute(
                """
                INSERT INTO preproduction_operations (
                    sequence, operation_id, idempotency_key, request_sha256,
                    operation_json, result_json, previous_chain_sha256,
                    chain_sha256, occurred_at
                ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
                """,
                (
                    len(operations) + 1,
                    request.operation_id,
                    request.idempotency_key,
                    request_sha256,
                    _canonical_json(operation_payload).decode("utf-8"),
                    _canonical_json(result).decode("utf-8"),
                    chain,
                    new_chain,
                    timestamp,
                ),
            )
            connection.execute(
                """
                UPDATE preproduction_metadata SET
                    current_traffic_percent = ?, revision = ?, operations = ?,
                    history_chain_sha256 = ?, updated_at = ? WHERE singleton = 1
                """,
                (
                    request.target_traffic_percent,
                    new_revision,
                    len(operations) + 1,
                    new_chain,
                    timestamp,
                ),
            )
            connection.commit()
        return result, 200

    def _validate_execution_payload(
        self, idempotency_key: str, value: Any
    ) -> tuple[dict[str, Any], TrafficChangeRequest, str]:
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
        if not isinstance(value, Mapping) or set(value) != base_fields | self.execution_extra_fields:
            raise PreproductionProviderError("provider_contract")
        result = dict(value)
        expected_sha256 = _digest(
            _canonical_json({key: item for key, item in result.items() if key != "request_sha256"})
        )
        if (
            result["schema_version"] != self.execution_schema_version
            or result["namespace_sha256"] != self.namespace_sha256
            or result["request_sha256"] != expected_sha256
            or not isinstance(result["rollout_request"], Mapping)
        ):
            raise PreproductionProviderError("provider_contract")
        request = _validate_request(TrafficChangeRequest(**dict(result["rollout_request"])))
        if (
            request.idempotency_key != idempotency_key
            or request.environment_sha256 != self.environment_sha256
            or request.provider_instance_sha256 != self.provider_instance_sha256
            or not isinstance(result["rollout_attestation"], Mapping)
            or request.attestation_id != result["rollout_attestation"].get("attestation_id")
            or request.attestation_sha256
            != _digest(_canonical_json(dict(result["rollout_attestation"])))
            or result["adapter_certification_sha256"]
            != _digest(_canonical_json(dict(result["adapter_certification"])))
            or result["shadow_attestation_sha256"]
            != _digest(_canonical_json(dict(result["shadow_attestation"])))
            or result["adapter_certification"].get("adapter") != self.adapter_name
            or result["adapter_certification"].get("implementation_version")
            != self.implementation_version
            or result["adapter_certification"].get("provider_instance_sha256")
            != self.provider_instance_sha256
            or result["adapter_certification"].get("environment_sha256") != self.environment_sha256
            or result["shadow_attestation"].get("decision") != "pass"
            or result["shadow_attestation"].get("provider_instance_sha256")
            != self.provider_instance_sha256
            or result["shadow_attestation"].get("environment_sha256") != self.environment_sha256
        ):
            raise PreproductionProviderError("provider_proof_binding")
        self._verify_embedded_proofs(result, request)
        return result, request, expected_sha256

    def _verify_embedded_proofs(
        self, payload: Mapping[str, Any], request: TrafficChangeRequest
    ) -> None:
        artifacts = (
            payload["rollout_attestation"],
            payload["adapter_certification"],
            payload["shadow_attestation"],
        )
        now = self.now()
        for artifact, trust in zip(artifacts, self._trusted_proofs, strict=True):
            key, key_id, domain = trust
            if (
                not isinstance(artifact, Mapping)
                or artifact.get("key_id") != key_id
                or not isinstance(artifact.get("signature"), str)
                or len(key) < 32
            ):
                raise PreproductionProviderError("provider_proof_signature")
            unsigned = {field: value for field, value in artifact.items() if field != "signature"}
            derived = hmac.new(key, domain, hashlib.sha256).digest()
            expected = hmac.new(derived, _canonical_json(unsigned), hashlib.sha256).hexdigest()
            if not hmac.compare_digest(str(artifact["signature"]), expected):
                raise PreproductionProviderError("provider_proof_signature")
        rollout, certification, shadow = artifacts
        if (
            rollout.get("attestation_id") != request.attestation_id
            or rollout.get("decision_id") != request.decision_id
            or rollout.get("decision") != request.decision
            or rollout.get("current_traffic_percent") != request.expected_traffic_percent
            or rollout.get("target_traffic_percent") != request.target_traffic_percent
            or rollout.get("environment_sha256") != self.environment_sha256
            or rollout.get("history_chain_sha256") != request.history_chain_sha256
            or certification.get("adapter") != self.adapter_name
            or certification.get("implementation_version") != self.implementation_version
            or shadow.get("decision") != "pass"
            or shadow.get("observer_instance_sha256") != self.observer_instance_sha256
        ):
            raise PreproductionProviderError("provider_proof_binding")
        try:
            proof_times = (
                (
                    _parse_timestamp(str(rollout["created_at"]), "rollout created_at"),
                    _parse_timestamp(str(rollout["expires_at"]), "rollout expires_at"),
                ),
                (
                    _parse_timestamp(
                        str(certification["certified_at"]), "certification certified_at"
                    ),
                    _parse_timestamp(str(certification["expires_at"]), "certification expires_at"),
                ),
                (
                    _parse_timestamp(str(shadow["created_at"]), "shadow created_at"),
                    _parse_timestamp(str(shadow["expires_at"]), "shadow expires_at"),
                ),
            )
        except (KeyError, DeploymentControllerError) as exc:
            raise PreproductionProviderError("provider_proof_time") from exc
        if any(created > now or now >= expires for created, expires in proof_times):
            raise PreproductionProviderError("provider_proof_time")

    def _verified_state(
        self, connection: sqlite3.Connection
    ) -> tuple[Mapping[str, Any], list[Mapping[str, Any]]]:
        metadata_rows = connection.execute("SELECT * FROM preproduction_metadata").fetchall()
        operations = connection.execute(
            "SELECT * FROM preproduction_operations ORDER BY sequence"
        ).fetchall()
        if len(metadata_rows) != 1:
            raise DirectTrafficPreproductionConfigError("preproduction metadata is invalid")
        metadata = dict(metadata_rows[0])
        previous = _ZERO_CHAIN
        for sequence, row in enumerate(operations, start=1):
            operation = dict(row)
            try:
                operation_payload = json.loads(str(operation["operation_json"]))
                result = json.loads(str(operation["result_json"]))
            except (json.JSONDecodeError, TypeError, ValueError) as exc:
                raise DirectTrafficPreproductionConfigError(
                    "preproduction operation record is invalid"
                ) from exc
            if (
                operation["sequence"] != sequence
                or operation["previous_chain_sha256"] != previous
                or not isinstance(operation_payload, Mapping)
                or not isinstance(result, Mapping)
                or operation["operation_json"] != _canonical_json(operation_payload).decode("utf-8")
                or operation["result_json"] != _canonical_json(result).decode("utf-8")
                or operation["chain_sha256"] != _chain_digest(previous, operation_payload)
                or operation["chain_sha256"] != result.get("history_chain_sha256")
                or operation["operation_id"] != operation_payload.get("operation_id")
                or operation["idempotency_key"] != operation_payload.get("idempotency_key")
                or operation["request_sha256"] != result.get("request_sha256")
                or operation["occurred_at"] != operation_payload.get("occurred_at")
                or operation["occurred_at"] != result.get("applied_at")
            ):
                raise DirectTrafficPreproductionConfigError(
                    "preproduction operation chain is invalid"
                )
            previous = str(operation["chain_sha256"])
        if (
            metadata["provider_instance_sha256"] != self.provider_instance_sha256
            or metadata["environment_sha256"] != self.environment_sha256
            or metadata["namespace_sha256"] != self.namespace_sha256
            or int(metadata["operations"]) != len(operations)
            or metadata["history_chain_sha256"] != previous
        ):
            raise DirectTrafficPreproductionConfigError("preproduction platform binding is invalid")
        return metadata, [dict(row) for row in operations]

    def _state_payload(
        self, metadata: Mapping[str, Any], operations: list[Mapping[str, Any]]
    ) -> dict[str, Any]:
        return {
            "schema_version": self.state_schema_version,
            "provider_instance_sha256": self.provider_instance_sha256,
            "environment_sha256": self.environment_sha256,
            "namespace_sha256": self.namespace_sha256,
            "current_traffic_percent": int(metadata["current_traffic_percent"]),
            "revision": int(metadata["revision"]),
            "operations": len(operations),
            "history_chain_sha256": str(metadata["history_chain_sha256"]),
            "observed_at": _format_timestamp(self.now()),
        }

    def _result_payload(
        self,
        request: TrafficChangeRequest,
        request_sha256: str,
        outcome: Literal["applied", "no_op", "conflict"],
        current: int,
        revision: int,
        operations: int,
        chain: str,
        applied_at: str,
    ) -> dict[str, Any]:
        return {
            "schema_version": self.result_schema_version,
            "operation_id": request.operation_id,
            "idempotency_key": request.idempotency_key,
            "request_sha256": request_sha256,
            "outcome": outcome,
            "current_traffic_percent": current,
            "revision": revision,
            "operations": operations,
            "history_chain_sha256": chain,
            "applied_at": applied_at,
        }

    @staticmethod
    def _response(status: int, body: Mapping[str, Any], url: str) -> TransportResponse:
        return TransportResponse(
            status=status,
            body=_canonical_json(body),
            content_type="application/json",
            final_url=url,
        )

    def _initialize(self, initial_traffic_percent: int) -> None:
        if (
            isinstance(initial_traffic_percent, bool)
            or not isinstance(initial_traffic_percent, int)
            or not 0 <= initial_traffic_percent <= 100
        ):
            raise DirectTrafficPreproductionConfigError("preproduction initial traffic is invalid")
        with closing(self._connect(unvalidated=True)) as connection, connection:
            version = int(connection.execute("PRAGMA user_version").fetchone()[0])
            tables = {
                str(row[0])
                for row in connection.execute(
                    "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'"
                ).fetchall()
            }
            if version == 0 and not tables:
                timestamp = _format_timestamp(self.now())
                connection.execute("BEGIN IMMEDIATE")
                connection.execute(
                    """
                    CREATE TABLE preproduction_metadata (
                        singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
                        provider_instance_sha256 TEXT NOT NULL,
                        environment_sha256 TEXT NOT NULL,
                        namespace_sha256 TEXT NOT NULL,
                        initial_traffic_percent INTEGER NOT NULL,
                        current_traffic_percent INTEGER NOT NULL,
                        revision INTEGER NOT NULL,
                        operations INTEGER NOT NULL,
                        history_chain_sha256 TEXT NOT NULL,
                        created_at TEXT NOT NULL,
                        updated_at TEXT NOT NULL
                    )
                    """
                )
                connection.execute(
                    """
                    CREATE TABLE preproduction_operations (
                        sequence INTEGER PRIMARY KEY,
                        operation_id TEXT NOT NULL UNIQUE,
                        idempotency_key TEXT NOT NULL UNIQUE,
                        request_sha256 TEXT NOT NULL,
                        operation_json TEXT NOT NULL,
                        result_json TEXT NOT NULL,
                        previous_chain_sha256 TEXT NOT NULL,
                        chain_sha256 TEXT NOT NULL,
                        occurred_at TEXT NOT NULL
                    )
                    """
                )
                for action in ("UPDATE", "DELETE"):
                    connection.execute(
                        f"CREATE TRIGGER preproduction_operations_no_{action.lower()} "
                        f"BEFORE {action} ON preproduction_operations BEGIN "
                        "SELECT RAISE(ABORT, 'preproduction history is append-only'); END"
                    )
                connection.execute(
                    """
                    INSERT INTO preproduction_metadata VALUES (
                        1, ?, ?, ?, ?, ?, 0, 0, ?, ?, ?
                    )
                    """,
                    (
                        self.provider_instance_sha256,
                        self.environment_sha256,
                        self.namespace_sha256,
                        initial_traffic_percent,
                        initial_traffic_percent,
                        _ZERO_CHAIN,
                        timestamp,
                        timestamp,
                    ),
                )
                connection.execute(f"PRAGMA user_version = {_SCHEMA_VERSION}")
                connection.commit()
            triggers = {
                str(row[0])
                for row in connection.execute(
                    "SELECT name FROM sqlite_master WHERE type='trigger'"
                ).fetchall()
            }
            if (
                version not in {0, _SCHEMA_VERSION}
                or (tables != set() and tables != _TABLES)
                or triggers != _TRIGGERS
            ):
                raise DirectTrafficPreproductionConfigError(
                    "preproduction sandbox schema is invalid"
                )
        self.status()

    def _connect(self, *, read_only: bool = False, unvalidated: bool = False) -> sqlite3.Connection:
        if not unvalidated:
            _validate_private_database(self.path)
        target = str(self.path)
        if read_only:
            target = f"{self.path.resolve().as_uri()}?mode=ro"
        connection = sqlite3.connect(target, uri=read_only, timeout=5.0, isolation_level=None)
        connection.row_factory = sqlite3.Row
        connection.execute("PRAGMA busy_timeout = 5000")
        if read_only:
            connection.execute("PRAGMA query_only = ON")
        else:
            connection.execute("PRAGMA synchronous = FULL")
        return connection


def main(argv: Sequence[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        description="Certify the preproduction HTTPS writer in an isolated local namespace"
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
        adapter = SQLitePreproductionCertificationAdapter(
            args.certification_ledger,
            environment_sha256(args.environment_id),
            args.provider_instance,
            namespace_id=args.namespace_id,
        )
        key, key_id = certification_key_from_environment()
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
            result = verify_certification(
                args.certification,
                adapter,
                key,
                key_id,
            )
            output = {
                "status": "verified",
                "certification_id": result["certification_id"],
                "expires_at": result["expires_at"],
            }
        print(json.dumps(output, sort_keys=True))
        return 0
    except DirectTrafficPreproductionConfigError as exc:
        print(f"preproduction certification configuration error: {exc}", file=sys.stderr)
        return 2
    except (DirectTrafficCertificationError, OSError, sqlite3.Error) as exc:
        print(f"preproduction certification error: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
