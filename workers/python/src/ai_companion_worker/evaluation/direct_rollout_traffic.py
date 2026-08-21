from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import sqlite3
import sys
from contextlib import closing
from dataclasses import asdict, dataclass
from datetime import UTC, datetime
from pathlib import Path
from typing import Any, Callable, Literal, Mapping, Sequence, cast

from .deployment_controller import (
    DeploymentControllerError,
    _canonical_json,
    _digest,
    _ensure_private_database,
    _format_timestamp,
    _normalize_now,
    _parse_timestamp,
    _validate_private_database,
)
from .direct_rollout import (
    KEY_ENV,
    KEY_ID_ENV,
    DirectRolloutConfigError,
    DirectRolloutError,
    load_policy,
    verify_attestation,
)
from .direct_traffic_adapter import (
    ADAPTER_CONTRACT_VERSION,
    CERTIFICATION_ISOLATION_MODE,
    AdapterChangeRequest,
    AdapterChangeResult,
    AdapterTrafficState,
)
from .release_gate_history import (
    HISTORY_SCHEMA_VERSION,
    ReleaseGateHistory,
    ReleaseGateHistoryError,
    environment_sha256,
)
from .release_gate import (
    DEFAULT_CANARY_LEASE_ROOT,
    ReleaseGateError,
    try_acquire_canary_lease,
)


TRAFFIC_SCHEMA_VERSION = 1
REQUEST_SCHEMA_VERSION = "agent-direct-traffic-request-v1"
PLAN_SCHEMA_VERSION = "agent-direct-traffic-plan-v1"
STATUS_SCHEMA_VERSION = "agent-direct-traffic-status-v1"
MAX_OPERATIONS = 100_000
_ZERO_CHAIN = "0" * 64
_PROVIDER_DOMAIN = b"ai-companion/agent-direct-traffic-provider/v1\0"
_IDEMPOTENCY_DOMAIN = b"ai-companion/agent-direct-traffic-idempotency/v1\0"
_CHAIN_DOMAIN = b"ai-companion/agent-direct-traffic-history/v1\0"
_DIGEST_RE = re.compile(r"[0-9a-f]{64}")
_DECISION_ID_RE = re.compile(r"direct-rollout-[0-9a-f]{32}")
_ATTESTATION_ID_RE = re.compile(r"direct-rollout-attestation-[0-9a-f]{32}")
_ADAPTER_CERTIFICATION_ID_RE = re.compile(
    r"direct-traffic-adapter-cert-[0-9a-f]{32}"
)
_OPERATION_ID_RE = re.compile(r"direct-traffic-op-[0-9a-f]{32}")
_DECISIONS = {"expand", "hold", "rollback", "disable"}
_METADATA_COLUMNS = {
    "singleton",
    "schema_version",
    "environment_sha256",
    "provider_instance_sha256",
    "initial_traffic_percent",
    "current_traffic_percent",
    "revision",
    "created_at",
    "updated_at",
}
_OPERATION_COLUMNS = {
    "sequence",
    "operation_id",
    "decision_id",
    "idempotency_key",
    "attestation_id",
    "attestation_sha256",
    "history_chain_sha256",
    "adapter_certification_id",
    "adapter_certification_sha256",
    "key_id",
    "decision",
    "expected_traffic_percent",
    "target_traffic_percent",
    "previous_revision",
    "new_revision",
    "outcome",
    "occurred_at",
    "previous_chain_sha256",
    "chain_sha256",
}
_TRIGGERS = {
    "direct_traffic_operations_no_update",
    "direct_traffic_operations_no_delete",
}


class DirectTrafficError(ValueError):
    pass


class DirectTrafficConfigError(DirectTrafficError):
    pass


class DirectTrafficVerificationError(DirectTrafficError):
    pass


class DirectTrafficConflictError(DirectTrafficError):
    pass


@dataclass(frozen=True)
class TrafficChangeRequest:
    schema_version: str
    operation_id: str
    idempotency_key: str
    environment_sha256: str
    provider_instance_sha256: str
    attestation_id: str
    attestation_sha256: str
    history_chain_sha256: str
    key_id: str
    decision_id: str
    decision: Literal["expand", "hold", "rollback", "disable"]
    expected_traffic_percent: int
    target_traffic_percent: int
    expected_revision: int


class SQLiteDirectTrafficSandbox:
    """An atomic, local-only traffic adapter used before any platform integration."""

    name = "sqlite-direct-traffic-sandbox"
    implementation_version = "1.0.0"
    supports_idempotent_apply = True
    supports_lookup = True
    supports_compare_and_swap = True
    certification_isolation_mode = CERTIFICATION_ISOLATION_MODE
    supports_optimistic_concurrency = True

    def __init__(
        self,
        path: Path,
        environment_sha256: str,
        provider_instance: str,
        *,
        create: bool = False,
        initial_traffic_percent: int | None = None,
    ) -> None:
        self.path = path
        self.environment_sha256 = _validate_digest(
            environment_sha256, "traffic environment_sha256"
        )
        self.provider_instance_sha256 = provider_instance_sha256(provider_instance)
        if create:
            if initial_traffic_percent is None:
                raise DirectTrafficConfigError("initial traffic percentage is required")
            initial = _bounded_percent(initial_traffic_percent, "initial traffic percentage")
            try:
                _ensure_private_database(path)
            except DeploymentControllerError as exc:
                raise DirectTrafficVerificationError(
                    "direct traffic sandbox database is not private"
                ) from exc
            self._initialize(initial)
        else:
            if initial_traffic_percent is not None:
                raise DirectTrafficConfigError(
                    "initial traffic percentage is only valid during initialization"
                )
            try:
                _validate_private_database(path)
            except DeploymentControllerError as exc:
                raise DirectTrafficVerificationError(
                    "direct traffic sandbox database is not private"
                ) from exc
            self._validate_schema()
        self._validate_binding()
        state = self.status()
        if (
            create
            and initial_traffic_percent is not None
            and state["initial_traffic_percent"] != initial_traffic_percent
        ):
            raise DirectTrafficConfigError(
                "existing direct traffic sandbox has another initial stage"
            )

    def status(self) -> dict[str, Any]:
        with closing(self._connect(read_only=True)) as connection:
            metadata, operations = self._verified_state(connection)
        chain = str(operations[-1]["chain_sha256"]) if operations else _ZERO_CHAIN
        return {
            "schema_version": STATUS_SCHEMA_VERSION,
            "adapter": self.name,
            "provider_instance_sha256": self.provider_instance_sha256,
            "environment_sha256": self.environment_sha256,
            "initial_traffic_percent": int(metadata["initial_traffic_percent"]),
            "current_traffic_percent": int(metadata["current_traffic_percent"]),
            "revision": int(metadata["revision"]),
            "operations": len(operations),
            "history_chain_sha256": chain,
        }

    def operation(self, decision_id: str) -> dict[str, Any] | None:
        _validate_decision_id(decision_id)
        with closing(self._connect(read_only=True)) as connection:
            self._verified_state(connection)
            row = connection.execute(
                "SELECT * FROM direct_traffic_operations WHERE decision_id = ?",
                (decision_id,),
            ).fetchone()
        return dict(row) if row is not None else None

    def replay_receipt(
        self, request: TrafficChangeRequest
    ) -> dict[str, Any] | None:
        validated = _validate_request(request)
        operation = self.operation(validated.decision_id)
        if operation is None:
            return None
        existing = self._validate_operation_row(operation)
        self._validate_existing(existing, validated)
        return self._receipt(existing, reused=True)

    def operational_state_sha256(self) -> str:
        status = self.status()
        return _digest(
            _canonical_json(
                {
                    "schema_version": ADAPTER_CONTRACT_VERSION,
                    "provider_instance_sha256": self.provider_instance_sha256,
                    "environment_sha256": self.environment_sha256,
                    "current_traffic_percent": status["current_traffic_percent"],
                    "revision": status["revision"],
                    "history_chain_sha256": status["history_chain_sha256"],
                }
            )
        )

    def certification_namespace_sha256(self) -> str:
        return _digest(
            _canonical_json(
                {
                    "schema_version": ADAPTER_CONTRACT_VERSION,
                    "isolation_mode": self.certification_isolation_mode,
                    "provider_instance_sha256": self.provider_instance_sha256,
                    "environment_sha256": self.environment_sha256,
                    "namespace": "unsupported",
                }
            )
        )

    def initialize_certification_challenge(
        self, challenge_id: str, initial_traffic_percent: int
    ) -> AdapterTrafficState:
        raise DirectTrafficConfigError(
            "operational sandbox cannot host certification challenges"
        )

    def read_certification_state(self, challenge_id: str) -> AdapterTrafficState:
        raise DirectTrafficConfigError(
            "operational sandbox cannot host certification challenges"
        )

    def apply_certification_change(
        self, request: AdapterChangeRequest
    ) -> AdapterChangeResult:
        raise DirectTrafficConfigError(
            "operational sandbox cannot host certification challenges"
        )

    def lookup_certification_change(
        self, idempotency_key: str
    ) -> AdapterChangeResult | None:
        raise DirectTrafficConfigError(
            "operational sandbox cannot host certification challenges"
        )

    def apply(
        self,
        request: TrafficChangeRequest,
        history_ledger: Path,
        adapter_certification_id: str,
        adapter_certification_sha256: str,
        *,
        now: datetime | None = None,
        before_commit: Callable[[], None] | None = None,
    ) -> dict[str, Any]:
        validated = _validate_request(request)
        certification_id = _validate_adapter_certification_id(
            adapter_certification_id
        )
        certification_sha256 = _validate_digest(
            adapter_certification_sha256,
            "traffic adapter certification digest",
        )
        if validated.decision == "hold":
            raise DirectTrafficConfigError(
                "hold decisions do not authorize a traffic mutation"
            )
        timestamp = _format_timestamp(_normalize_now(now))
        try:
            _validate_private_database(history_ledger)
        except DeploymentControllerError as exc:
            raise DirectTrafficVerificationError(
                "release Gate history database is not private"
            ) from exc
        with closing(self._connect()) as connection, connection:
            connection.execute(
                "ATTACH DATABASE ? AS rollout_history",
                (str(history_ledger.resolve()),),
            )
            connection.execute("BEGIN IMMEDIATE")
            self._validate_history_fence(connection, validated)
            metadata, operations = self._verified_state(connection)
            existing_rows = connection.execute(
                """
                SELECT * FROM direct_traffic_operations
                WHERE decision_id = ? OR idempotency_key = ? OR operation_id = ?
                """,
                (
                    validated.decision_id,
                    validated.idempotency_key,
                    validated.operation_id,
                ),
            ).fetchall()
            if existing_rows:
                if len(existing_rows) != 1:
                    raise DirectTrafficVerificationError(
                        "direct traffic idempotency identities are inconsistent"
                    )
                existing = self._validate_operation_row(dict(existing_rows[0]))
                self._validate_existing(existing, validated)
                connection.commit()
                return self._receipt(existing, reused=True)

            current = int(metadata["current_traffic_percent"])
            revision = int(metadata["revision"])
            if (
                current != validated.expected_traffic_percent
                or revision != validated.expected_revision
            ):
                raise DirectTrafficConflictError(
                    "signed traffic stage or sandbox revision is no longer current"
                )
            outcome: Literal["applied", "no_op"] = (
                "no_op" if validated.target_traffic_percent == current else "applied"
            )
            new_revision = revision + 1 if outcome == "applied" else revision
            sequence = len(operations) + 1
            previous_chain = (
                str(operations[-1]["chain_sha256"]) if operations else _ZERO_CHAIN
            )
            payload: dict[str, Any] = {
                "environment_sha256": self.environment_sha256,
                "provider_instance_sha256": self.provider_instance_sha256,
                "sequence": sequence,
                "operation_id": validated.operation_id,
                "decision_id": validated.decision_id,
                "idempotency_key": validated.idempotency_key,
                "attestation_id": validated.attestation_id,
                "attestation_sha256": validated.attestation_sha256,
                "history_chain_sha256": validated.history_chain_sha256,
                "adapter_certification_id": certification_id,
                "adapter_certification_sha256": certification_sha256,
                "key_id": validated.key_id,
                "decision": validated.decision,
                "expected_traffic_percent": validated.expected_traffic_percent,
                "target_traffic_percent": validated.target_traffic_percent,
                "previous_revision": revision,
                "new_revision": new_revision,
                "outcome": outcome,
                "occurred_at": timestamp,
                "previous_chain_sha256": previous_chain,
            }
            chain_sha256 = _chain_digest(previous_chain, payload)
            connection.execute(
                """
                INSERT INTO direct_traffic_operations (
                    sequence, operation_id, decision_id, idempotency_key,
                    attestation_id, attestation_sha256, key_id, decision,
                    history_chain_sha256,
                    adapter_certification_id, adapter_certification_sha256,
                    expected_traffic_percent, target_traffic_percent,
                    previous_revision, new_revision, outcome, occurred_at,
                    previous_chain_sha256, chain_sha256
                ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                """,
                (
                    sequence,
                    validated.operation_id,
                    validated.decision_id,
                    validated.idempotency_key,
                    validated.attestation_id,
                    validated.attestation_sha256,
                    validated.key_id,
                    validated.decision,
                    validated.history_chain_sha256,
                    certification_id,
                    certification_sha256,
                    validated.expected_traffic_percent,
                    validated.target_traffic_percent,
                    revision,
                    new_revision,
                    outcome,
                    timestamp,
                    previous_chain,
                    chain_sha256,
                ),
            )
            connection.execute(
                """
                UPDATE direct_traffic_metadata
                SET current_traffic_percent = ?, revision = ?, updated_at = ?
                WHERE singleton = 1
                """,
                (validated.target_traffic_percent, new_revision, timestamp),
            )
            if before_commit is not None:
                before_commit()
            connection.commit()
            row = connection.execute(
                "SELECT * FROM direct_traffic_operations WHERE sequence = ?",
                (sequence,),
            ).fetchone()
        if row is None:
            raise DirectTrafficVerificationError("direct traffic operation was not persisted")
        return self._receipt(self._validate_operation_row(dict(row)), reused=False)

    def _initialize(self, initial_traffic_percent: int) -> None:
        with closing(self._connect(unvalidated=True)) as connection, connection:
            version = int(connection.execute("PRAGMA user_version").fetchone()[0])
            tables = _table_names(connection)
            if version == 0 and not tables:
                timestamp = _format_timestamp(datetime.now(UTC))
                connection.execute("BEGIN IMMEDIATE")
                connection.execute(
                    """
                    CREATE TABLE direct_traffic_metadata (
                        singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
                        schema_version INTEGER NOT NULL,
                        environment_sha256 TEXT NOT NULL,
                        provider_instance_sha256 TEXT NOT NULL,
                        initial_traffic_percent INTEGER NOT NULL
                            CHECK (initial_traffic_percent BETWEEN 0 AND 100),
                        current_traffic_percent INTEGER NOT NULL
                            CHECK (current_traffic_percent BETWEEN 0 AND 100),
                        revision INTEGER NOT NULL CHECK (revision >= 0),
                        created_at TEXT NOT NULL,
                        updated_at TEXT NOT NULL
                    )
                    """
                )
                connection.execute(
                    """
                    CREATE TABLE direct_traffic_operations (
                        sequence INTEGER PRIMARY KEY,
                        operation_id TEXT NOT NULL UNIQUE,
                        decision_id TEXT NOT NULL UNIQUE,
                        idempotency_key TEXT NOT NULL UNIQUE,
                        attestation_id TEXT NOT NULL,
                        attestation_sha256 TEXT NOT NULL,
                        key_id TEXT NOT NULL,
                        history_chain_sha256 TEXT NOT NULL,
                        adapter_certification_id TEXT NOT NULL,
                        adapter_certification_sha256 TEXT NOT NULL,
                        decision TEXT NOT NULL
                            CHECK (decision IN ('expand', 'rollback', 'disable')),
                        expected_traffic_percent INTEGER NOT NULL
                            CHECK (expected_traffic_percent BETWEEN 0 AND 100),
                        target_traffic_percent INTEGER NOT NULL
                            CHECK (target_traffic_percent BETWEEN 0 AND 100),
                        previous_revision INTEGER NOT NULL CHECK (previous_revision >= 0),
                        new_revision INTEGER NOT NULL CHECK (new_revision >= 0),
                        outcome TEXT NOT NULL CHECK (outcome IN ('applied', 'no_op')),
                        occurred_at TEXT NOT NULL,
                        previous_chain_sha256 TEXT NOT NULL,
                        chain_sha256 TEXT NOT NULL
                    )
                    """
                )
                connection.execute(
                    """
                    CREATE TRIGGER direct_traffic_operations_no_update
                    BEFORE UPDATE ON direct_traffic_operations
                    BEGIN
                        SELECT RAISE(ABORT, 'direct traffic history is append-only');
                    END
                    """
                )
                connection.execute(
                    """
                    CREATE TRIGGER direct_traffic_operations_no_delete
                    BEFORE DELETE ON direct_traffic_operations
                    BEGIN
                        SELECT RAISE(ABORT, 'direct traffic history is append-only');
                    END
                    """
                )
                connection.execute(
                    """
                    INSERT INTO direct_traffic_metadata (
                        singleton, schema_version, environment_sha256,
                        provider_instance_sha256, initial_traffic_percent,
                        current_traffic_percent, revision, created_at, updated_at
                    ) VALUES (1, ?, ?, ?, ?, ?, 0, ?, ?)
                    """,
                    (
                        TRAFFIC_SCHEMA_VERSION,
                        self.environment_sha256,
                        self.provider_instance_sha256,
                        initial_traffic_percent,
                        initial_traffic_percent,
                        timestamp,
                        timestamp,
                    ),
                )
                connection.execute(f"PRAGMA user_version = {TRAFFIC_SCHEMA_VERSION}")
                connection.commit()
            self._validate_schema(connection)

    def _validate_schema(self, existing: sqlite3.Connection | None = None) -> None:
        owned = existing is None
        connection = existing or self._connect(unvalidated=True, read_only=True)
        try:
            version = int(connection.execute("PRAGMA user_version").fetchone()[0])
            if version != TRAFFIC_SCHEMA_VERSION or _table_names(connection) != {
                "direct_traffic_metadata",
                "direct_traffic_operations",
            }:
                raise DirectTrafficVerificationError(
                    "direct traffic sandbox schema is invalid"
                )
            metadata_columns = {
                str(row[1])
                for row in connection.execute(
                    "PRAGMA table_info(direct_traffic_metadata)"
                ).fetchall()
            }
            operation_columns = {
                str(row[1])
                for row in connection.execute(
                    "PRAGMA table_info(direct_traffic_operations)"
                ).fetchall()
            }
            triggers = {
                str(row[0])
                for row in connection.execute(
                    "SELECT name FROM sqlite_master WHERE type='trigger'"
                ).fetchall()
            }
            if (
                metadata_columns != _METADATA_COLUMNS
                or operation_columns != _OPERATION_COLUMNS
                or triggers != _TRIGGERS
            ):
                raise DirectTrafficVerificationError(
                    "direct traffic sandbox columns or triggers are invalid"
                )
        finally:
            if owned:
                connection.close()

    def _validate_binding(self) -> None:
        with closing(self._connect(read_only=True)) as connection:
            rows = connection.execute("SELECT * FROM direct_traffic_metadata").fetchall()
        if len(rows) != 1:
            raise DirectTrafficVerificationError(
                "direct traffic sandbox metadata is invalid"
            )
        metadata = dict(rows[0])
        if (
            metadata.get("singleton") != 1
            or metadata.get("schema_version") != TRAFFIC_SCHEMA_VERSION
            or metadata.get("environment_sha256") != self.environment_sha256
            or metadata.get("provider_instance_sha256")
            != self.provider_instance_sha256
        ):
            raise DirectTrafficVerificationError(
                "direct traffic sandbox binding is invalid"
            )

    def _validate_history_fence(
        self,
        connection: sqlite3.Connection,
        request: TrafficChangeRequest,
    ) -> None:
        version = int(
            connection.execute("PRAGMA rollout_history.user_version").fetchone()[0]
        )
        tables = {
            str(row[0])
            for row in connection.execute(
                """
                SELECT name FROM rollout_history.sqlite_master
                WHERE type='table' AND name NOT LIKE 'sqlite_%'
                """
            ).fetchall()
        }
        if version != HISTORY_SCHEMA_VERSION or tables != {
            "release_gate_history_metadata",
            "release_gate_history_events",
        }:
            raise DirectTrafficVerificationError(
                "release Gate history schema fence is invalid"
            )
        metadata = connection.execute(
            """
            SELECT environment_sha256
            FROM rollout_history.release_gate_history_metadata
            WHERE singleton = 1
            """
        ).fetchall()
        if (
            len(metadata) != 1
            or str(metadata[0]["environment_sha256"]) != request.environment_sha256
        ):
            raise DirectTrafficVerificationError(
                "release Gate history environment fence is invalid"
            )
        head = connection.execute(
            """
            SELECT chain_sha256
            FROM rollout_history.release_gate_history_events
            ORDER BY sequence DESC LIMIT 1
            """
        ).fetchone()
        actual_chain = str(head["chain_sha256"]) if head is not None else _ZERO_CHAIN
        if actual_chain != request.history_chain_sha256:
            raise DirectTrafficConflictError(
                "new release Gate evidence invalidated the signed traffic decision"
            )

    def _verified_state(
        self, connection: sqlite3.Connection
    ) -> tuple[dict[str, Any], list[dict[str, Any]]]:
        self._validate_schema(connection)
        metadata_rows = connection.execute(
            "SELECT * FROM direct_traffic_metadata"
        ).fetchall()
        if len(metadata_rows) != 1:
            raise DirectTrafficVerificationError(
                "direct traffic sandbox metadata is invalid"
            )
        metadata = dict(metadata_rows[0])
        if set(metadata) != _METADATA_COLUMNS:
            raise DirectTrafficVerificationError(
                "direct traffic sandbox metadata fields are invalid"
            )
        if (
            metadata["singleton"] != 1
            or metadata["schema_version"] != TRAFFIC_SCHEMA_VERSION
            or metadata["environment_sha256"] != self.environment_sha256
            or metadata["provider_instance_sha256"]
            != self.provider_instance_sha256
        ):
            raise DirectTrafficVerificationError(
                "direct traffic sandbox binding is invalid"
            )
        initial = _bounded_percent(
            metadata["initial_traffic_percent"], "stored initial traffic percentage"
        )
        _bounded_percent(
            metadata["current_traffic_percent"], "stored current traffic percentage"
        )
        revision = _nonnegative_int(metadata["revision"], "stored traffic revision")
        _parse_timestamp(str(metadata["created_at"]), "traffic sandbox created_at")
        _parse_timestamp(str(metadata["updated_at"]), "traffic sandbox updated_at")
        count = int(
            connection.execute(
                "SELECT COUNT(*) FROM direct_traffic_operations"
            ).fetchone()[0]
        )
        if count > MAX_OPERATIONS:
            raise DirectTrafficVerificationError(
                "direct traffic history exceeds verification limit"
            )
        rows = connection.execute(
            "SELECT * FROM direct_traffic_operations ORDER BY sequence"
        ).fetchall()
        operations = [self._validate_operation_row(dict(row)) for row in rows]
        expected_current = initial
        expected_revision = 0
        previous_chain = _ZERO_CHAIN
        for sequence, operation in enumerate(operations, start=1):
            if (
                operation["sequence"] != sequence
                or operation["previous_chain_sha256"] != previous_chain
                or operation["expected_traffic_percent"] != expected_current
                or operation["previous_revision"] != expected_revision
            ):
                raise DirectTrafficVerificationError(
                    "direct traffic history continuity is invalid"
                )
            payload = self._operation_payload(operation)
            if operation["chain_sha256"] != _chain_digest(previous_chain, payload):
                raise DirectTrafficVerificationError(
                    "direct traffic history hash chain is invalid"
                )
            if operation["outcome"] == "applied":
                if (
                    operation["target_traffic_percent"] == expected_current
                    or operation["new_revision"] != expected_revision + 1
                ):
                    raise DirectTrafficVerificationError(
                        "direct traffic applied transition is invalid"
                    )
                expected_current = int(operation["target_traffic_percent"])
                expected_revision += 1
            else:
                if (
                    operation["target_traffic_percent"] != expected_current
                    or operation["new_revision"] != expected_revision
                ):
                    raise DirectTrafficVerificationError(
                        "direct traffic no-op transition is invalid"
                    )
            previous_chain = str(operation["chain_sha256"])
        if (
            int(metadata["current_traffic_percent"]) != expected_current
            or revision != expected_revision
        ):
            raise DirectTrafficVerificationError(
                "direct traffic materialized state does not match history"
            )
        return metadata, operations

    def _validate_operation_row(self, value: Mapping[str, Any]) -> dict[str, Any]:
        if set(value) != _OPERATION_COLUMNS:
            raise DirectTrafficVerificationError(
                "direct traffic operation fields are invalid"
            )
        operation = dict(value)
        sequence = _nonnegative_int(operation["sequence"], "operation sequence")
        if sequence < 1:
            raise DirectTrafficVerificationError(
                "direct traffic operation sequence is invalid"
            )
        if (
            not isinstance(operation["operation_id"], str)
            or _OPERATION_ID_RE.fullmatch(operation["operation_id"]) is None
        ):
            raise DirectTrafficVerificationError(
                "direct traffic operation ID is invalid"
            )
        _validate_decision_id(operation["decision_id"])
        _validate_digest(operation["idempotency_key"], "traffic idempotency_key")
        if (
            not isinstance(operation["attestation_id"], str)
            or _ATTESTATION_ID_RE.fullmatch(operation["attestation_id"]) is None
        ):
            raise DirectTrafficVerificationError(
                "direct traffic attestation ID is invalid"
            )
        _validate_digest(operation["attestation_sha256"], "traffic attestation digest")
        _validate_digest(operation["history_chain_sha256"], "traffic history chain")
        _validate_adapter_certification_id(operation["adapter_certification_id"])
        _validate_digest(
            operation["adapter_certification_sha256"],
            "traffic adapter certification digest",
        )
        if (
            not isinstance(operation["key_id"], str)
            or not operation["key_id"]
            or len(operation["key_id"]) > 128
        ):
            raise DirectTrafficVerificationError("direct traffic key ID is invalid")
        if operation["decision"] not in {"expand", "rollback", "disable"}:
            raise DirectTrafficVerificationError(
                "direct traffic operation decision is invalid"
            )
        for field in ("expected_traffic_percent", "target_traffic_percent"):
            _bounded_percent(operation[field], f"operation {field}")
        _nonnegative_int(operation["previous_revision"], "operation previous revision")
        _nonnegative_int(operation["new_revision"], "operation new revision")
        if operation["outcome"] not in {"applied", "no_op"}:
            raise DirectTrafficVerificationError(
                "direct traffic operation outcome is invalid"
            )
        _parse_timestamp(str(operation["occurred_at"]), "traffic operation occurred_at")
        for field in ("previous_chain_sha256", "chain_sha256"):
            _validate_digest(operation[field], f"traffic operation {field}")
        return operation

    def _validate_existing(
        self, existing: Mapping[str, Any], request: TrafficChangeRequest
    ) -> None:
        expected = {
            "operation_id": request.operation_id,
            "decision_id": request.decision_id,
            "idempotency_key": request.idempotency_key,
            "attestation_id": request.attestation_id,
            "attestation_sha256": request.attestation_sha256,
            "history_chain_sha256": request.history_chain_sha256,
            "key_id": request.key_id,
            "decision": request.decision,
            "expected_traffic_percent": request.expected_traffic_percent,
            "target_traffic_percent": request.target_traffic_percent,
            "previous_revision": request.expected_revision,
        }
        if any(existing.get(field) != value for field, value in expected.items()):
            raise DirectTrafficConflictError(
                "direct traffic decision is already bound to another operation"
            )

    def _operation_payload(self, operation: Mapping[str, Any]) -> dict[str, Any]:
        return {
            "environment_sha256": self.environment_sha256,
            "provider_instance_sha256": self.provider_instance_sha256,
            **{
                field: operation[field]
                for field in _OPERATION_COLUMNS
                if field != "chain_sha256"
            },
        }

    def _receipt(self, operation: Mapping[str, Any], *, reused: bool) -> dict[str, Any]:
        return {
            "operation_id": operation["operation_id"],
            "decision_id": operation["decision_id"],
            "decision": operation["decision"],
            "adapter_certification_id": operation["adapter_certification_id"],
            "outcome": operation["outcome"],
            "target_traffic_percent": operation["target_traffic_percent"],
            "new_revision": operation["new_revision"],
            "history_chain_sha256": operation["chain_sha256"],
            "reused": reused,
        }

    def _connect(
        self, *, read_only: bool = False, unvalidated: bool = False
    ) -> sqlite3.Connection:
        if not unvalidated:
            try:
                _validate_private_database(self.path)
            except DeploymentControllerError as exc:
                raise DirectTrafficVerificationError(
                    "direct traffic sandbox database is not private"
                ) from exc
        target = str(self.path)
        if read_only:
            target = f"{self.path.resolve().as_uri()}?mode=ro"
        connection = sqlite3.connect(
            target,
            uri=read_only,
            timeout=5.0,
            isolation_level=None,
        )
        connection.row_factory = sqlite3.Row
        connection.execute("PRAGMA busy_timeout = 5000")
        if read_only:
            connection.execute("PRAGMA query_only = ON")
        else:
            connection.execute("PRAGMA synchronous = FULL")
        return connection


def provider_instance_sha256(provider_instance: str) -> str:
    if (
        not isinstance(provider_instance, str)
        or not provider_instance
        or len(provider_instance) > 256
        or provider_instance.strip() != provider_instance
        or any(ord(character) < 32 for character in provider_instance)
    ):
        raise DirectTrafficConfigError("direct traffic provider instance is invalid")
    return hashlib.sha256(
        _PROVIDER_DOMAIN + provider_instance.encode("utf-8")
    ).hexdigest()


def build_traffic_request(
    sandbox: SQLiteDirectTrafficSandbox,
    decision_dir: Path,
    history: ReleaseGateHistory,
    policy: Mapping[str, Any],
    policy_sha256: str,
    key: bytes,
    key_id: str,
    *,
    max_age_seconds: int = 900,
    now: datetime | None = None,
) -> TrafficChangeRequest:
    attestation = verify_attestation(
        decision_dir,
        history,
        policy,
        policy_sha256,
        key,
        key_id,
        max_age_seconds=max_age_seconds,
        require_decision="any",
        now=now,
    )
    if attestation["environment_sha256"] != sandbox.environment_sha256:
        raise DirectTrafficVerificationError(
            "signed rollout environment does not match traffic sandbox"
        )
    stages_value = policy.get("traffic_stages")
    if not isinstance(stages_value, list):
        raise DirectTrafficConfigError("direct traffic policy stages are invalid")
    stages = cast(list[int], stages_value)
    current = _bounded_percent(
        attestation["current_traffic_percent"], "signed current traffic percentage"
    )
    target = _bounded_percent(
        attestation["target_traffic_percent"], "signed target traffic percentage"
    )
    decision = cast(
        Literal["expand", "hold", "rollback", "disable"], attestation["decision"]
    )
    _validate_transition(decision, current, target, stages)
    existing = sandbox.operation(str(attestation["decision_id"]))
    status = sandbox.status()
    expected_revision = (
        int(existing["previous_revision"])
        if existing is not None
        else int(status["revision"])
    )
    attestation_sha256 = _digest(_canonical_json(attestation))
    identity: dict[str, Any] = {
        "schema_version": REQUEST_SCHEMA_VERSION,
        "environment_sha256": sandbox.environment_sha256,
        "provider_instance_sha256": sandbox.provider_instance_sha256,
        "attestation_id": attestation["attestation_id"],
        "attestation_sha256": attestation_sha256,
        "history_chain_sha256": attestation["history_chain_sha256"],
        "key_id": attestation["key_id"],
        "decision_id": attestation["decision_id"],
        "decision": decision,
        "expected_traffic_percent": current,
        "target_traffic_percent": target,
        "expected_revision": expected_revision,
    }
    idempotency_key = hashlib.sha256(
        _IDEMPOTENCY_DOMAIN + _canonical_json(identity)
    ).hexdigest()
    request = TrafficChangeRequest(
        **identity,
        operation_id=f"direct-traffic-op-{idempotency_key[:32]}",
        idempotency_key=idempotency_key,
    )
    return _validate_request(request)


def plan_traffic_change(
    sandbox: SQLiteDirectTrafficSandbox, request: TrafficChangeRequest
) -> dict[str, Any]:
    validated = _validate_request(request)
    status = sandbox.status()
    replay = sandbox.replay_receipt(validated)
    if replay is not None:
        action = "reused"
    elif validated.decision == "hold":
        action = "hold"
    elif (
        status["current_traffic_percent"] != validated.expected_traffic_percent
        or status["revision"] != validated.expected_revision
    ):
        action = "conflict"
    else:
        action = "ready"
    return {
        "schema_version": PLAN_SCHEMA_VERSION,
        "action": action,
        "request": asdict(validated),
        "observed_state": {
            "current_traffic_percent": status["current_traffic_percent"],
            "revision": status["revision"],
            "history_chain_sha256": status["history_chain_sha256"],
        },
    }


def _validate_request(request: TrafficChangeRequest) -> TrafficChangeRequest:
    if not isinstance(request, TrafficChangeRequest):
        raise DirectTrafficConfigError("direct traffic request type is invalid")
    if request.schema_version != REQUEST_SCHEMA_VERSION:
        raise DirectTrafficConfigError("direct traffic request version is invalid")
    if _OPERATION_ID_RE.fullmatch(request.operation_id) is None:
        raise DirectTrafficConfigError("direct traffic request operation ID is invalid")
    _validate_digest(request.idempotency_key, "traffic request idempotency_key")
    _validate_digest(request.environment_sha256, "traffic request environment_sha256")
    _validate_digest(
        request.provider_instance_sha256, "traffic request provider instance digest"
    )
    if (
        _ATTESTATION_ID_RE.fullmatch(request.attestation_id) is None
        or not request.key_id
        or len(request.key_id) > 128
    ):
        raise DirectTrafficConfigError("direct traffic request attestation is invalid")
    _validate_digest(request.attestation_sha256, "traffic request attestation digest")
    _validate_digest(request.history_chain_sha256, "traffic request history chain")
    _validate_decision_id(request.decision_id)
    if request.decision not in _DECISIONS:
        raise DirectTrafficConfigError("direct traffic request decision is invalid")
    _bounded_percent(
        request.expected_traffic_percent, "request expected traffic percentage"
    )
    _bounded_percent(request.target_traffic_percent, "request target traffic percentage")
    _nonnegative_int(request.expected_revision, "request expected revision")
    if (
        (request.decision == "expand" and request.target_traffic_percent <= request.expected_traffic_percent)
        or (
            request.decision == "hold"
            and request.target_traffic_percent != request.expected_traffic_percent
        )
        or (
            request.decision == "rollback"
            and request.target_traffic_percent >= request.expected_traffic_percent
        )
        or (request.decision == "disable" and request.target_traffic_percent != 0)
    ):
        raise DirectTrafficConfigError(
            "direct traffic request transition direction is invalid"
        )
    identity = {
        field: value
        for field, value in asdict(request).items()
        if field not in {"operation_id", "idempotency_key"}
    }
    expected_key = hashlib.sha256(
        _IDEMPOTENCY_DOMAIN + _canonical_json(identity)
    ).hexdigest()
    if (
        request.idempotency_key != expected_key
        or request.operation_id != f"direct-traffic-op-{expected_key[:32]}"
    ):
        raise DirectTrafficConfigError(
            "direct traffic request identity binding is invalid"
        )
    return request


def _validate_transition(
    decision: str, current: int, target: int, stages: Sequence[int]
) -> None:
    if (
        not stages
        or current not in stages
        or target not in stages
        or list(stages) != sorted(set(stages))
    ):
        raise DirectTrafficVerificationError(
            "signed rollout transition is not on the policy stage ladder"
        )
    index = stages.index(current)
    if decision == "expand":
        valid = index + 1 < len(stages) and target == stages[index + 1]
    elif decision == "hold":
        valid = target == current
    elif decision == "rollback":
        valid = index > 0 and target == stages[index - 1]
    elif decision == "disable":
        valid = target == 0 and target <= current
    else:
        valid = False
    if not valid:
        raise DirectTrafficVerificationError(
            "signed rollout decision and target transition are inconsistent"
        )


def _chain_digest(previous_chain: str, payload: Mapping[str, Any]) -> str:
    return hashlib.sha256(
        _CHAIN_DOMAIN
        + bytes.fromhex(previous_chain)
        + _canonical_json(payload)
    ).hexdigest()


def _table_names(connection: sqlite3.Connection) -> set[str]:
    return {
        str(row[0])
        for row in connection.execute(
            "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'"
        ).fetchall()
    }


def _validate_decision_id(value: Any) -> str:
    if not isinstance(value, str) or _DECISION_ID_RE.fullmatch(value) is None:
        raise DirectTrafficVerificationError("direct traffic decision ID is invalid")
    return value


def _validate_adapter_certification_id(value: Any) -> str:
    if (
        not isinstance(value, str)
        or _ADAPTER_CERTIFICATION_ID_RE.fullmatch(value) is None
    ):
        raise DirectTrafficVerificationError(
            "direct traffic adapter certification ID is invalid"
        )
    return value


def _validate_digest(value: Any, label: str) -> str:
    if not isinstance(value, str) or _DIGEST_RE.fullmatch(value) is None:
        raise DirectTrafficVerificationError(f"{label} is invalid")
    return value


def _bounded_percent(value: Any, label: str) -> int:
    if isinstance(value, bool) or not isinstance(value, int) or not 0 <= value <= 100:
        raise DirectTrafficConfigError(f"{label} is invalid")
    return value


def _nonnegative_int(value: Any, label: str) -> int:
    if isinstance(value, bool) or not isinstance(value, int) or value < 0:
        raise DirectTrafficVerificationError(f"{label} is invalid")
    return value


def _key_from_environment() -> tuple[bytes, str]:
    key = os.environ.get(KEY_ENV)
    key_id = os.environ.get(KEY_ID_ENV)
    if key is None or key_id is None:
        raise DirectTrafficConfigError(f"{KEY_ENV} and {KEY_ID_ENV} are required")
    return key.encode("utf-8"), key_id


def _common_parser(command: argparse.ArgumentParser) -> None:
    command.add_argument("--sandbox-ledger", type=Path, required=True)
    command.add_argument("--environment-id", required=True)
    command.add_argument("--provider-instance", required=True)


def main(argv: Sequence[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        description="Safely execute signed direct-traffic decisions in a local SQLite sandbox"
    )
    subparsers = parser.add_subparsers(dest="command", required=True)
    initialize = subparsers.add_parser("initialize")
    _common_parser(initialize)
    initialize.add_argument("--policy", type=Path, required=True)
    initialize.add_argument("--initial-traffic-percent", type=int, required=True)
    status_parser = subparsers.add_parser("status")
    _common_parser(status_parser)
    plan = subparsers.add_parser("plan")
    apply_parser = subparsers.add_parser("apply")
    for command in (plan, apply_parser):
        _common_parser(command)
        command.add_argument("--decision-dir", type=Path, required=True)
        command.add_argument("--history-ledger", type=Path, required=True)
        command.add_argument("--policy", type=Path, required=True)
        command.add_argument("--max-age-seconds", type=int, default=900)
        command.add_argument(
            "--canary-lease-root",
            type=Path,
            default=DEFAULT_CANARY_LEASE_ROOT,
        )
    apply_parser.add_argument("--adapter-certification", type=Path, required=True)
    args = parser.parse_args(argv)
    try:
        history = None
        if args.command == "initialize":
            policy, _ = load_policy(args.policy)
            stages = cast(list[int], policy["traffic_stages"])
            if args.initial_traffic_percent not in stages:
                raise DirectTrafficConfigError(
                    "initial traffic percentage must be an exact policy stage"
                )
            sandbox = SQLiteDirectTrafficSandbox(
                args.sandbox_ledger,
                environment_sha256(args.environment_id),
                args.provider_instance,
                create=True,
                initial_traffic_percent=args.initial_traffic_percent,
            )
            print(json.dumps(sandbox.status(), sort_keys=True))
            return 0
        environment_digest = environment_sha256(args.environment_id)
        sandbox = SQLiteDirectTrafficSandbox(
            args.sandbox_ledger,
            environment_digest,
            args.provider_instance,
        )
        if args.command == "status":
            print(json.dumps(sandbox.status(), sort_keys=True))
            return 0
        lease = try_acquire_canary_lease(
            args.canary_lease_root, args.environment_id
        )
        if lease is None:
            raise DirectTrafficConflictError(
                "another Canary or traffic operation holds the environment lease"
            )
        try:
            policy, policy_sha256 = load_policy(args.policy)
            history = ReleaseGateHistory(
                args.history_ledger, args.environment_id, create=False
            )
            key, key_id = _key_from_environment()
            # Verification happens after the environment lease. The adapter also
            # fences the history-chain head inside the traffic commit transaction.
            request = build_traffic_request(
                sandbox,
                args.decision_dir,
                history,
                policy,
                policy_sha256,
                key,
                key_id,
                max_age_seconds=args.max_age_seconds,
            )
            traffic_plan = plan_traffic_change(sandbox, request)
            if args.command == "plan":
                print(json.dumps(traffic_plan, sort_keys=True))
                return {"ready": 0, "reused": 0, "hold": 3, "conflict": 4}[
                    str(traffic_plan["action"])
                ]
            if traffic_plan["action"] == "hold":
                print(json.dumps(traffic_plan, sort_keys=True))
                return 3
            if traffic_plan["action"] == "conflict":
                print(json.dumps(traffic_plan, sort_keys=True))
                return 4
            from .direct_traffic_certification import (
                certification_key_from_environment,
                validate_execution_binding,
                verify_certification,
            )

            certification_key, certification_key_id = (
                certification_key_from_environment()
            )
            certification = verify_certification(
                args.adapter_certification,
                sandbox,
                certification_key,
                certification_key_id,
                require_certification_namespace=False,
            )
            validate_execution_binding(certification, sandbox)
            certification_sha256 = _digest(_canonical_json(certification))
            receipt = sandbox.apply(
                request,
                args.history_ledger,
                str(certification["certification_id"]),
                certification_sha256,
            )
            print(json.dumps(receipt, sort_keys=True))
            return 0
        finally:
            lease.release()
    except DirectTrafficConflictError as exc:
        print(f"direct traffic conflict: {exc}", file=sys.stderr)
        return 4
    except (DirectTrafficConfigError, DirectRolloutConfigError) as exc:
        print(f"direct traffic configuration error: {exc}", file=sys.stderr)
        return 2
    except (
        DirectTrafficError,
        DirectRolloutError,
        ValueError,
        ReleaseGateHistoryError,
        ReleaseGateError,
        DeploymentControllerError,
        OSError,
        sqlite3.Error,
    ) as exc:
        print(f"direct traffic verification error: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
