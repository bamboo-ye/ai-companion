from __future__ import annotations

import argparse
import hashlib
import hmac
import json
import os
import re
import secrets
import sqlite3
import sys
from contextlib import closing
from datetime import UTC, datetime, timedelta
from pathlib import Path
from typing import Any, Literal, Mapping, Sequence, cast

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
from .deployment_resolution import (
    DeploymentResolutionError,
    _ensure_private_state_directory,
    _read_contract,
    _validate_key,
    _validate_key_id,
    _write_exclusive_private_json,
)
from .direct_traffic_adapter import (
    ADAPTER_CONTRACT_VERSION,
    CERTIFICATION_ISOLATION_MODE,
    AdapterChangeRequest,
    AdapterChangeResult,
    AdapterTrafficState,
    DirectTrafficAdapter,
)
from .direct_rollout_traffic import (
    DirectTrafficConfigError,
    DirectTrafficError,
    SQLiteDirectTrafficSandbox,
    provider_instance_sha256,
)
from .release_attestation import MAX_CLOCK_SKEW_SECONDS
from .release_gate_history import ReleaseGateHistoryError, environment_sha256


CERTIFICATION_SCHEMA_VERSION = "agent-direct-traffic-adapter-certification-v1"
REPORT_SCHEMA_VERSION = "agent-direct-traffic-adapter-certification-report-v1"
CHANGE_REQUEST_SCHEMA_VERSION = "agent-direct-traffic-certification-change-v1"
KEY_ENV = "OBSERVABILITY_DIRECT_TRAFFIC_ADAPTER_CERTIFICATION_KEY"
KEY_ID_ENV = "OBSERVABILITY_DIRECT_TRAFFIC_ADAPTER_CERTIFICATION_KEY_ID"
PRODUCTION_KEY_ENV = "OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_CERTIFICATION_KEY"
PRODUCTION_KEY_ID_ENV = "OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_CERTIFICATION_KEY_ID"
PRODUCTION_EMERGENCY_KEY_ENV = (
    "OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EMERGENCY_CERTIFICATION_KEY"
)
PRODUCTION_EMERGENCY_KEY_ID_ENV = (
    "OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EMERGENCY_CERTIFICATION_KEY_ID"
)
PRODUCTION_RECOVERY_KEY_ENV = (
    "OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_RECOVERY_CERTIFICATION_KEY"
)
PRODUCTION_RECOVERY_KEY_ID_ENV = (
    "OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_RECOVERY_CERTIFICATION_KEY_ID"
)
PRODUCTION_EXPANSION_KEY_ENV = (
    "OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_CERTIFICATION_KEY"
)
PRODUCTION_EXPANSION_KEY_ID_ENV = (
    "OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_CERTIFICATION_KEY_ID"
)
PRODUCTION_EXPANSION_25_KEY_ENV = (
    "OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_25_CERTIFICATION_KEY"
)
PRODUCTION_EXPANSION_25_KEY_ID_ENV = (
    "OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_25_CERTIFICATION_KEY_ID"
)
PRODUCTION_EXPANSION_50_KEY_ENV = (
    "OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_50_CERTIFICATION_KEY"
)
PRODUCTION_EXPANSION_50_KEY_ID_ENV = (
    "OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_50_CERTIFICATION_KEY_ID"
)
PRODUCTION_EXPANSION_100_KEY_ENV = (
    "OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_100_CERTIFICATION_KEY"
)
PRODUCTION_EXPANSION_100_KEY_ID_ENV = (
    "OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_100_CERTIFICATION_KEY_ID"
)
MAX_CERTIFICATION_TTL_SECONDS = 24 * 60 * 60
_SIGNING_DOMAIN = b"ai-companion/agent-direct-traffic-adapter-certification/v1"
_CHALLENGE_DOMAIN = b"ai-companion/agent-direct-traffic-certification-challenge/v1\0"
_REQUEST_DOMAIN = b"ai-companion/agent-direct-traffic-certification-request/v1\0"
_NAMESPACE_DOMAIN = b"ai-companion/agent-direct-traffic-certification-namespace/v1\0"
_DIGEST_RE = re.compile(r"[0-9a-f]{64}")
_CERTIFICATION_ID_RE = re.compile(r"direct-traffic-adapter-cert-[0-9a-f]{32}")
_CHALLENGE_ID_RE = re.compile(r"direct-traffic-challenge-[0-9a-f]{32}")
_EXTERNAL_OPERATION_ID_RE = re.compile(r"direct-traffic-cert-op-[0-9a-f]{32}")
_SCHEMA_VERSION = 1
_METADATA_COLUMNS = {
    "singleton",
    "schema_version",
    "provider_instance_sha256",
    "environment_sha256",
    "namespace_id",
    "created_at",
}
_CHALLENGE_COLUMNS = {
    "challenge_id",
    "current_traffic_percent",
    "revision",
    "created_at",
    "updated_at",
}
_OPERATION_COLUMNS = {
    "idempotency_key",
    "challenge_id",
    "request_sha256",
    "expected_traffic_percent",
    "target_traffic_percent",
    "expected_revision",
    "status",
    "external_operation_id",
    "current_traffic_percent",
    "revision",
    "created_at",
}


class DirectTrafficCertificationError(ValueError):
    pass


class DirectTrafficCertificationConfigError(DirectTrafficCertificationError):
    pass


class SQLiteDirectTrafficCertificationAdapter:
    name = SQLiteDirectTrafficSandbox.name
    implementation_version = SQLiteDirectTrafficSandbox.implementation_version
    supports_idempotent_apply = True
    supports_lookup = True
    supports_compare_and_swap = True
    certification_isolation_mode = CERTIFICATION_ISOLATION_MODE

    def __init__(
        self,
        path: Path,
        environment_sha256_value: str,
        provider_instance: str,
        *,
        namespace_id: str,
    ) -> None:
        self.path = path
        self.environment_sha256 = _validate_digest(
            environment_sha256_value, "certification environment digest"
        )
        self.provider_instance_sha256 = provider_instance_sha256(provider_instance)
        self.namespace_id = _validate_nonce(namespace_id, "certification namespace")
        try:
            _ensure_private_database(path)
        except DeploymentControllerError as exc:
            raise DirectTrafficCertificationError(
                "traffic certification database is not private"
            ) from exc
        self._initialize()
        self._validate_binding()

    def operational_state_sha256(self) -> str:
        return _digest(
            _canonical_json(
                {
                    "schema_version": ADAPTER_CONTRACT_VERSION,
                    "provider_instance_sha256": self.provider_instance_sha256,
                    "environment_sha256": self.environment_sha256,
                    "operational_namespace": "not-readable-by-certification-adapter",
                }
            )
        )

    def certification_namespace_sha256(self) -> str:
        return hashlib.sha256(
            _NAMESPACE_DOMAIN
            + bytes.fromhex(self.provider_instance_sha256)
            + bytes.fromhex(self.environment_sha256)
            + self.namespace_id.encode("utf-8")
        ).hexdigest()

    def initialize_certification_challenge(
        self, challenge_id: str, initial_traffic_percent: int
    ) -> AdapterTrafficState:
        challenge = _validate_challenge_id(challenge_id)
        initial = _bounded_percent(initial_traffic_percent, "initial traffic")
        timestamp = _format_timestamp(datetime.now(UTC))
        with closing(self._connect()) as connection, connection:
            connection.execute("BEGIN IMMEDIATE")
            existing = connection.execute(
                "SELECT * FROM certification_challenges WHERE challenge_id = ?",
                (challenge,),
            ).fetchone()
            if existing is None:
                connection.execute(
                    """
                    INSERT INTO certification_challenges (
                        challenge_id, current_traffic_percent, revision,
                        created_at, updated_at
                    ) VALUES (?, ?, 0, ?, ?)
                    """,
                    (challenge, initial, timestamp, timestamp),
                )
                connection.commit()
                return AdapterTrafficState(challenge, initial, 0)
            state = self._state(existing)
            if state not in {
                AdapterTrafficState(challenge, initial, 0),
                AdapterTrafficState(challenge, 10, 1),
            }:
                raise DirectTrafficCertificationError(
                    "certification challenge is already bound to another state"
                )
            connection.commit()
            return state

    def read_certification_state(self, challenge_id: str) -> AdapterTrafficState:
        challenge = _validate_challenge_id(challenge_id)
        with closing(self._connect(read_only=True)) as connection:
            row = connection.execute(
                "SELECT * FROM certification_challenges WHERE challenge_id = ?",
                (challenge,),
            ).fetchone()
        if row is None:
            raise DirectTrafficCertificationError(
                "certification challenge does not exist"
            )
        return self._state(row)

    def apply_certification_change(
        self, request: AdapterChangeRequest
    ) -> AdapterChangeResult:
        validated = _validate_change_request(request)
        external_operation_id = (
            f"direct-traffic-cert-op-{validated.idempotency_key[:32]}"
        )
        timestamp = _format_timestamp(datetime.now(UTC))
        with closing(self._connect()) as connection, connection:
            connection.execute("BEGIN IMMEDIATE")
            existing = connection.execute(
                "SELECT * FROM certification_operations WHERE idempotency_key = ?",
                (validated.idempotency_key,),
            ).fetchone()
            if existing is not None:
                if existing["request_sha256"] != validated.request_sha256:
                    raise DirectTrafficCertificationError(
                        "certification idempotency key is bound to another request"
                    )
                connection.commit()
                return self._result(existing)
            challenge = connection.execute(
                "SELECT * FROM certification_challenges WHERE challenge_id = ?",
                (validated.challenge_id,),
            ).fetchone()
            if challenge is None:
                raise DirectTrafficCertificationError(
                    "certification challenge does not exist"
                )
            state = self._state(challenge)
            status: Literal["applied", "conflict"] = "applied"
            current = validated.target_traffic_percent
            revision = state.revision + 1
            if (
                state.current_traffic_percent != validated.expected_traffic_percent
                or state.revision != validated.expected_revision
            ):
                status = "conflict"
                current = state.current_traffic_percent
                revision = state.revision
            else:
                connection.execute(
                    """
                    UPDATE certification_challenges
                    SET current_traffic_percent = ?, revision = ?, updated_at = ?
                    WHERE challenge_id = ?
                    """,
                    (current, revision, timestamp, validated.challenge_id),
                )
            connection.execute(
                """
                INSERT INTO certification_operations (
                    idempotency_key, challenge_id, request_sha256,
                    expected_traffic_percent, target_traffic_percent,
                    expected_revision, status, external_operation_id,
                    current_traffic_percent, revision, created_at
                ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                """,
                (
                    validated.idempotency_key,
                    validated.challenge_id,
                    validated.request_sha256,
                    validated.expected_traffic_percent,
                    validated.target_traffic_percent,
                    validated.expected_revision,
                    status,
                    external_operation_id,
                    current,
                    revision,
                    timestamp,
                ),
            )
            connection.commit()
        return AdapterChangeResult(status, external_operation_id, current, revision)

    def lookup_certification_change(
        self, idempotency_key: str
    ) -> AdapterChangeResult | None:
        key = _validate_digest(idempotency_key, "certification idempotency key")
        with closing(self._connect(read_only=True)) as connection:
            row = connection.execute(
                "SELECT * FROM certification_operations WHERE idempotency_key = ?",
                (key,),
            ).fetchone()
        return self._result(row) if row is not None else None

    def operation_count(self, challenge_id: str) -> int:
        challenge = _validate_challenge_id(challenge_id)
        with closing(self._connect(read_only=True)) as connection:
            return int(
                connection.execute(
                    "SELECT COUNT(*) FROM certification_operations WHERE challenge_id = ?",
                    (challenge,),
                ).fetchone()[0]
            )

    def _initialize(self) -> None:
        with closing(self._connect(unvalidated=True)) as connection, connection:
            version = int(connection.execute("PRAGMA user_version").fetchone()[0])
            tables = _tables(connection)
            if version == 0 and not tables:
                timestamp = _format_timestamp(datetime.now(UTC))
                connection.execute("BEGIN IMMEDIATE")
                connection.execute(
                    """
                    CREATE TABLE certification_metadata (
                        singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
                        schema_version INTEGER NOT NULL,
                        provider_instance_sha256 TEXT NOT NULL,
                        environment_sha256 TEXT NOT NULL,
                        namespace_id TEXT NOT NULL,
                        created_at TEXT NOT NULL
                    )
                    """
                )
                connection.execute(
                    """
                    CREATE TABLE certification_challenges (
                        challenge_id TEXT PRIMARY KEY,
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
                    CREATE TABLE certification_operations (
                        idempotency_key TEXT PRIMARY KEY,
                        challenge_id TEXT NOT NULL,
                        request_sha256 TEXT NOT NULL,
                        expected_traffic_percent INTEGER NOT NULL,
                        target_traffic_percent INTEGER NOT NULL,
                        expected_revision INTEGER NOT NULL,
                        status TEXT NOT NULL CHECK (status IN ('applied', 'conflict')),
                        external_operation_id TEXT NOT NULL,
                        current_traffic_percent INTEGER NOT NULL,
                        revision INTEGER NOT NULL,
                        created_at TEXT NOT NULL,
                        FOREIGN KEY (challenge_id)
                            REFERENCES certification_challenges(challenge_id)
                    )
                    """
                )
                connection.execute(
                    """
                    INSERT INTO certification_metadata (
                        singleton, schema_version, provider_instance_sha256,
                        environment_sha256, namespace_id, created_at
                    ) VALUES (1, ?, ?, ?, ?, ?)
                    """,
                    (
                        _SCHEMA_VERSION,
                        self.provider_instance_sha256,
                        self.environment_sha256,
                        self.namespace_id,
                        timestamp,
                    ),
                )
                connection.execute(f"PRAGMA user_version = {_SCHEMA_VERSION}")
                connection.commit()
            self._validate_schema(connection)

    def _validate_schema(self, connection: sqlite3.Connection) -> None:
        if int(connection.execute("PRAGMA user_version").fetchone()[0]) != _SCHEMA_VERSION:
            raise DirectTrafficCertificationError(
                "traffic certification schema version is invalid"
            )
        if _tables(connection) != {
            "certification_metadata",
            "certification_challenges",
            "certification_operations",
        }:
            raise DirectTrafficCertificationError(
                "traffic certification tables are invalid"
            )
        columns = (
            {
                str(row[1])
                for row in connection.execute(
                    "PRAGMA table_info(certification_metadata)"
                ).fetchall()
            },
            {
                str(row[1])
                for row in connection.execute(
                    "PRAGMA table_info(certification_challenges)"
                ).fetchall()
            },
            {
                str(row[1])
                for row in connection.execute(
                    "PRAGMA table_info(certification_operations)"
                ).fetchall()
            },
        )
        if columns != (_METADATA_COLUMNS, _CHALLENGE_COLUMNS, _OPERATION_COLUMNS):
            raise DirectTrafficCertificationError(
                "traffic certification columns are invalid"
            )

    def _validate_binding(self) -> None:
        with closing(self._connect(read_only=True)) as connection:
            rows = connection.execute("SELECT * FROM certification_metadata").fetchall()
        if len(rows) != 1:
            raise DirectTrafficCertificationError(
                "traffic certification metadata is invalid"
            )
        row = rows[0]
        if (
            row["singleton"] != 1
            or row["schema_version"] != _SCHEMA_VERSION
            or row["provider_instance_sha256"] != self.provider_instance_sha256
            or row["environment_sha256"] != self.environment_sha256
            or row["namespace_id"] != self.namespace_id
        ):
            raise DirectTrafficCertificationError(
                "traffic certification namespace binding is invalid"
            )

    def _state(self, row: sqlite3.Row) -> AdapterTrafficState:
        challenge_id = _validate_challenge_id(row["challenge_id"])
        current = _bounded_percent(row["current_traffic_percent"], "stored traffic")
        revision = _nonnegative_int(row["revision"], "stored revision")
        _parse_timestamp(str(row["created_at"]), "challenge created_at")
        _parse_timestamp(str(row["updated_at"]), "challenge updated_at")
        return AdapterTrafficState(challenge_id, current, revision)

    def _result(self, row: sqlite3.Row) -> AdapterChangeResult:
        if row["status"] not in {"applied", "conflict"}:
            raise DirectTrafficCertificationError(
                "certification operation status is invalid"
            )
        external_id = str(row["external_operation_id"])
        if _EXTERNAL_OPERATION_ID_RE.fullmatch(external_id) is None:
            raise DirectTrafficCertificationError(
                "certification operation ID is invalid"
            )
        return AdapterChangeResult(
            cast(Literal["applied", "conflict"], row["status"]),
            external_id,
            _bounded_percent(row["current_traffic_percent"], "operation traffic"),
            _nonnegative_int(row["revision"], "operation revision"),
        )

    def _connect(
        self, *, read_only: bool = False, unvalidated: bool = False
    ) -> sqlite3.Connection:
        if not unvalidated:
            try:
                _validate_private_database(self.path)
            except DeploymentControllerError as exc:
                raise DirectTrafficCertificationError(
                    "traffic certification database is not private"
                ) from exc
        target = str(self.path)
        if read_only:
            target = f"{self.path.resolve().as_uri()}?mode=ro"
        connection = sqlite3.connect(
            target, uri=read_only, timeout=5.0, isolation_level=None
        )
        connection.row_factory = sqlite3.Row
        connection.execute("PRAGMA busy_timeout = 5000")
        connection.execute("PRAGMA foreign_keys = ON")
        if read_only:
            connection.execute("PRAGMA query_only = ON")
        else:
            connection.execute("PRAGMA synchronous = FULL")
        return connection


def certify_adapter(
    adapter: DirectTrafficAdapter,
    *,
    nonce: str,
) -> dict[str, Any]:
    certification_nonce = _validate_nonce(nonce, "certification nonce")
    _validate_adapter(adapter)
    before = adapter.operational_state_sha256()
    challenge_id = _challenge_id(adapter, certification_nonce)
    initial = adapter.initialize_certification_challenge(challenge_id, 5)
    if initial not in {
        AdapterTrafficState(challenge_id, 5, 0),
        AdapterTrafficState(challenge_id, 10, 1),
    }:
        raise DirectTrafficCertificationError(
            "adapter did not initialize an isolated certification state"
        )
    apply_request = _change_request(challenge_id, 5, 10, 0, certification_nonce, "apply")
    first = adapter.apply_certification_change(apply_request)
    second = adapter.apply_certification_change(apply_request)
    lookup = adapter.lookup_certification_change(apply_request.idempotency_key)
    if lookup is None or first != second or first != lookup:
        raise DirectTrafficCertificationError(
            "adapter did not preserve one idempotent traffic operation"
        )
    if (
        first.status != "applied"
        or first.current_traffic_percent != 10
        or first.revision != 1
    ):
        raise DirectTrafficCertificationError(
            "adapter did not apply the exact certification transition"
        )
    stale_request = _change_request(challenge_id, 5, 25, 0, certification_nonce, "stale")
    stale = adapter.apply_certification_change(stale_request)
    stale_lookup = adapter.lookup_certification_change(stale_request.idempotency_key)
    if (
        stale.status != "conflict"
        or stale.current_traffic_percent != 10
        or stale.revision != 1
        or stale_lookup != stale
    ):
        raise DirectTrafficCertificationError(
            "adapter compare-and-swap conflict contract failed"
        )
    final = adapter.read_certification_state(challenge_id)
    if final != AdapterTrafficState(challenge_id, 10, 1):
        raise DirectTrafficCertificationError(
            "adapter certification final state is inconsistent"
        )
    after = adapter.operational_state_sha256()
    if after != before:
        raise DirectTrafficCertificationError(
            "adapter certification modified operational traffic state"
        )
    report: dict[str, Any] = {
        "schema_version": REPORT_SCHEMA_VERSION,
        "adapter_contract_version": ADAPTER_CONTRACT_VERSION,
        "adapter": adapter.name,
        "implementation_version": adapter.implementation_version,
        "provider_instance_sha256": adapter.provider_instance_sha256,
        "environment_sha256": adapter.environment_sha256,
        "certification_isolation_mode": adapter.certification_isolation_mode,
        "certification_namespace_sha256": adapter.certification_namespace_sha256(),
        "challenge_id": challenge_id,
        "operational_state_before_sha256": before,
        "operational_state_after_sha256": after,
        "idempotent_apply": True,
        "lookup": True,
        "compare_and_swap": True,
        "isolated": True,
        "apply_request_sha256": apply_request.request_sha256,
        "stale_request_sha256": stale_request.request_sha256,
        "external_operation_sha256": _digest(
            first.external_operation_id.encode("utf-8")
        ),
        "final_traffic_percent": final.current_traffic_percent,
        "final_revision": final.revision,
    }
    return _validate_report(report)


def create_certification(
    adapter: DirectTrafficAdapter,
    output_dir: Path,
    key: bytes,
    key_id: str,
    *,
    nonce: str,
    ttl_seconds: int = 3600,
    now: datetime | None = None,
) -> tuple[dict[str, Any], Path, bool]:
    current_time = _normalize_now(now)
    signing_key = _validate_key(key, "direct traffic adapter certification key")
    signing_key_id = _validate_key_id(
        key_id, "direct traffic adapter certification key_id"
    )
    ttl = _bounded_int(
        ttl_seconds, "certification TTL", 1, MAX_CERTIFICATION_TTL_SECONDS
    )
    certification_nonce = _validate_nonce(nonce, "certification nonce")
    certification_id = _certification_id(adapter, certification_nonce)
    try:
        _ensure_private_state_directory(output_dir)
    except DeploymentResolutionError as exc:
        raise DirectTrafficCertificationError(
            "cannot create private traffic certification directory"
        ) from exc
    path = output_dir / f"{certification_id}.json"
    if path.exists() or path.is_symlink():
        existing = verify_certification(
            path, adapter, signing_key, signing_key_id, now=current_time
        )
        return existing, path, False
    report = certify_adapter(adapter, nonce=certification_nonce)
    payload: dict[str, Any] = {
        "schema_version": CERTIFICATION_SCHEMA_VERSION,
        "certification_id": certification_id,
        "certification_nonce": certification_nonce,
        "adapter_contract_version": ADAPTER_CONTRACT_VERSION,
        "adapter": adapter.name,
        "implementation_version": adapter.implementation_version,
        "provider_instance_sha256": adapter.provider_instance_sha256,
        "environment_sha256": adapter.environment_sha256,
        "certification_isolation_mode": adapter.certification_isolation_mode,
        "certification_namespace_sha256": adapter.certification_namespace_sha256(),
        "report_sha256": _digest(_canonical_json(report)),
        "report": report,
        "key_id": signing_key_id,
        "certified_at": _format_timestamp(current_time),
        "expires_at": _format_timestamp(current_time + timedelta(seconds=ttl)),
    }
    certification = dict(payload)
    certification["signature"] = _sign(signing_key, payload)
    certification = _validate_certification(certification)
    try:
        _write_exclusive_private_json(path, certification)
        return certification, path, True
    except FileExistsError:
        existing = verify_certification(
            path, adapter, signing_key, signing_key_id, now=current_time
        )
        return existing, path, False


def verify_certification(
    path: Path,
    adapter: DirectTrafficAdapter,
    key: bytes,
    key_id: str,
    *,
    now: datetime | None = None,
    require_certification_namespace: bool = True,
) -> dict[str, Any]:
    current_time = _normalize_now(now)
    _validate_adapter(adapter)
    verification_key = _validate_key(key, "direct traffic adapter certification key")
    trusted_key_id = _validate_key_id(
        key_id, "direct traffic adapter certification key_id"
    )
    try:
        certification = _read_contract(path, _validate_certification)
    except DeploymentResolutionError as exc:
        raise DirectTrafficCertificationError(
            "cannot read traffic adapter certification"
        ) from exc
    if certification["key_id"] != trusted_key_id:
        raise DirectTrafficCertificationError(
            "traffic adapter certification key_id is not trusted"
        )
    payload = {
        field: value for field, value in certification.items() if field != "signature"
    }
    if not hmac.compare_digest(
        str(certification["signature"]), _sign(verification_key, payload)
    ):
        raise DirectTrafficCertificationError(
            "traffic adapter certification signature is invalid"
        )
    bindings = {
        "adapter_contract_version": ADAPTER_CONTRACT_VERSION,
        "adapter": adapter.name,
        "implementation_version": adapter.implementation_version,
        "provider_instance_sha256": adapter.provider_instance_sha256,
        "environment_sha256": adapter.environment_sha256,
        "certification_isolation_mode": adapter.certification_isolation_mode,
    }
    if certification["certification_id"] != _certification_id_from_values(
        certification
    ):
        raise DirectTrafficCertificationError(
            "traffic adapter certification ID binding is invalid"
        )
    if require_certification_namespace:
        bindings["certification_namespace_sha256"] = (
            adapter.certification_namespace_sha256()
        )
    if any(certification.get(field) != value for field, value in bindings.items()):
        raise DirectTrafficCertificationError(
            "traffic adapter certification does not match registered adapter"
        )
    report = certification["report"]
    if certification["report_sha256"] != _digest(_canonical_json(report)):
        raise DirectTrafficCertificationError(
            "traffic adapter certification report digest is invalid"
        )
    report_bindings = {
        "adapter_contract_version": ADAPTER_CONTRACT_VERSION,
        "adapter": adapter.name,
        "implementation_version": adapter.implementation_version,
        "provider_instance_sha256": adapter.provider_instance_sha256,
        "environment_sha256": adapter.environment_sha256,
        "certification_isolation_mode": adapter.certification_isolation_mode,
    }
    if require_certification_namespace:
        report_bindings["certification_namespace_sha256"] = (
            adapter.certification_namespace_sha256()
        )
    if any(report.get(field) != value for field, value in report_bindings.items()):
        raise DirectTrafficCertificationError(
            "traffic adapter certification report binding is invalid"
        )
    certified_at = _parse_timestamp(
        str(certification["certified_at"]), "traffic certification certified_at"
    )
    expires_at = _parse_timestamp(
        str(certification["expires_at"]), "traffic certification expires_at"
    )
    if certified_at > current_time + timedelta(seconds=MAX_CLOCK_SKEW_SECONDS):
        raise DirectTrafficCertificationError(
            "traffic adapter certification is from the future"
        )
    if (
        expires_at <= certified_at
        or expires_at - certified_at
        > timedelta(seconds=MAX_CERTIFICATION_TTL_SECONDS)
    ):
        raise DirectTrafficCertificationError(
            "traffic adapter certification lifetime is invalid"
        )
    if current_time >= expires_at:
        raise DirectTrafficCertificationError(
            "traffic adapter certification has expired"
        )
    return certification


def validate_execution_binding(
    certification: Mapping[str, Any],
    adapter: DirectTrafficAdapter,
) -> dict[str, Any]:
    validated = _validate_certification(certification)
    bindings = {
        "adapter": adapter.name,
        "implementation_version": adapter.implementation_version,
        "provider_instance_sha256": adapter.provider_instance_sha256,
        "environment_sha256": adapter.environment_sha256,
    }
    if any(validated.get(field) != value for field, value in bindings.items()):
        raise DirectTrafficCertificationError(
            "traffic adapter certification is not bound to the execution adapter"
        )
    report = validated["report"]
    if any(report.get(field) != value for field, value in bindings.items()):
        raise DirectTrafficCertificationError(
            "traffic adapter certification report is not bound to execution"
        )
    if (
        adapter.supports_idempotent_apply is not True
        or adapter.supports_lookup is not True
        or adapter.supports_compare_and_swap is not True
    ):
        raise DirectTrafficCertificationConfigError(
            "execution adapter no longer exposes certified capabilities"
        )
    return validated


def certification_key_from_environment(*, profile: str = "standard") -> tuple[bytes, str]:
    return _key_from_environment(profile=profile)


def _validate_adapter(adapter: DirectTrafficAdapter) -> None:
    fields = {
        "name": adapter.name,
        "implementation_version": adapter.implementation_version,
        "provider_instance_sha256": adapter.provider_instance_sha256,
        "environment_sha256": adapter.environment_sha256,
    }
    if any(
        not isinstance(value, str) or not value or len(value) > 256
        for value in fields.values()
    ):
        raise DirectTrafficCertificationConfigError(
            "traffic adapter identity is invalid"
        )
    _validate_digest(adapter.provider_instance_sha256, "provider instance digest")
    _validate_digest(adapter.environment_sha256, "adapter environment digest")
    if (
        adapter.supports_idempotent_apply is not True
        or adapter.supports_lookup is not True
        or adapter.supports_compare_and_swap is not True
        or adapter.certification_isolation_mode != CERTIFICATION_ISOLATION_MODE
    ):
        raise DirectTrafficCertificationConfigError(
            "traffic adapter required capabilities are missing"
        )
    _validate_digest(
        adapter.operational_state_sha256(), "operational state digest"
    )
    _validate_digest(
        adapter.certification_namespace_sha256(), "certification namespace digest"
    )


def _change_request(
    challenge_id: str,
    expected: int,
    target: int,
    revision: int,
    nonce: str,
    label: str,
) -> AdapterChangeRequest:
    identity: dict[str, Any] = {
        "schema_version": CHANGE_REQUEST_SCHEMA_VERSION,
        "challenge_id": challenge_id,
        "expected_traffic_percent": expected,
        "target_traffic_percent": target,
        "expected_revision": revision,
    }
    seed = _canonical_json({**identity, "nonce": nonce, "label": label})
    idempotency_key = hashlib.sha256(_REQUEST_DOMAIN + seed).hexdigest()
    request_sha256 = _digest(
        _canonical_json({**identity, "idempotency_key": idempotency_key})
    )
    return _validate_change_request(
        AdapterChangeRequest(
            schema_version=CHANGE_REQUEST_SCHEMA_VERSION,
            challenge_id=challenge_id,
            idempotency_key=idempotency_key,
            expected_traffic_percent=expected,
            target_traffic_percent=target,
            expected_revision=revision,
            request_sha256=request_sha256,
        )
    )


def _validate_change_request(request: AdapterChangeRequest) -> AdapterChangeRequest:
    if not isinstance(request, AdapterChangeRequest):
        raise DirectTrafficCertificationConfigError(
            "certification request type is invalid"
        )
    if request.schema_version != CHANGE_REQUEST_SCHEMA_VERSION:
        raise DirectTrafficCertificationConfigError(
            "certification request version is invalid"
        )
    _validate_challenge_id(request.challenge_id)
    _validate_digest(request.idempotency_key, "certification idempotency key")
    _bounded_percent(request.expected_traffic_percent, "expected traffic")
    _bounded_percent(request.target_traffic_percent, "target traffic")
    _nonnegative_int(request.expected_revision, "expected revision")
    identity = {
        "schema_version": request.schema_version,
        "challenge_id": request.challenge_id,
        "expected_traffic_percent": request.expected_traffic_percent,
        "target_traffic_percent": request.target_traffic_percent,
        "expected_revision": request.expected_revision,
        "idempotency_key": request.idempotency_key,
    }
    if request.request_sha256 != _digest(_canonical_json(identity)):
        raise DirectTrafficCertificationConfigError(
            "certification request digest is invalid"
        )
    return request


def _validate_report(value: Any) -> dict[str, Any]:
    fields = {
        "schema_version",
        "adapter_contract_version",
        "adapter",
        "implementation_version",
        "provider_instance_sha256",
        "environment_sha256",
        "certification_isolation_mode",
        "certification_namespace_sha256",
        "challenge_id",
        "operational_state_before_sha256",
        "operational_state_after_sha256",
        "idempotent_apply",
        "lookup",
        "compare_and_swap",
        "isolated",
        "apply_request_sha256",
        "stale_request_sha256",
        "external_operation_sha256",
        "final_traffic_percent",
        "final_revision",
    }
    if not isinstance(value, Mapping) or set(value) != fields:
        raise DirectTrafficCertificationError(
            "traffic adapter certification report fields are invalid"
        )
    report = dict(value)
    if (
        report["schema_version"] != REPORT_SCHEMA_VERSION
        or report["adapter_contract_version"] != ADAPTER_CONTRACT_VERSION
        or report["certification_isolation_mode"] != CERTIFICATION_ISOLATION_MODE
        or any(
            report[field] is not True
            for field in ("idempotent_apply", "lookup", "compare_and_swap", "isolated")
        )
    ):
        raise DirectTrafficCertificationError(
            "traffic adapter certification report controls are invalid"
        )
    for field in (
        "provider_instance_sha256",
        "environment_sha256",
        "certification_namespace_sha256",
        "operational_state_before_sha256",
        "operational_state_after_sha256",
        "apply_request_sha256",
        "stale_request_sha256",
        "external_operation_sha256",
    ):
        _validate_digest(report[field], f"report {field}")
    _validate_challenge_id(report["challenge_id"])
    if report["operational_state_before_sha256"] != report[
        "operational_state_after_sha256"
    ]:
        raise DirectTrafficCertificationError(
            "traffic adapter certification isolation proof is invalid"
        )
    if report["final_traffic_percent"] != 10 or report["final_revision"] != 1:
        raise DirectTrafficCertificationError(
            "traffic adapter certification final state is invalid"
        )
    return report


def _validate_certification(value: Any) -> dict[str, Any]:
    fields = {
        "schema_version",
        "certification_id",
        "certification_nonce",
        "adapter_contract_version",
        "adapter",
        "implementation_version",
        "provider_instance_sha256",
        "environment_sha256",
        "certification_isolation_mode",
        "certification_namespace_sha256",
        "report_sha256",
        "report",
        "key_id",
        "certified_at",
        "expires_at",
        "signature",
    }
    if not isinstance(value, Mapping) or set(value) != fields:
        raise DirectTrafficCertificationError(
            "traffic adapter certification fields are invalid"
        )
    result = dict(value)
    if (
        result["schema_version"] != CERTIFICATION_SCHEMA_VERSION
        or result["adapter_contract_version"] != ADAPTER_CONTRACT_VERSION
        or result["certification_isolation_mode"] != CERTIFICATION_ISOLATION_MODE
        or not isinstance(result["certification_id"], str)
        or _CERTIFICATION_ID_RE.fullmatch(result["certification_id"]) is None
    ):
        raise DirectTrafficCertificationError(
            "traffic adapter certification identity is invalid"
        )
    _validate_nonce(result["certification_nonce"], "certification nonce")
    _validate_key_id(result["key_id"], "traffic adapter certification key_id")
    for field in (
        "provider_instance_sha256",
        "environment_sha256",
        "certification_namespace_sha256",
        "report_sha256",
        "signature",
    ):
        _validate_digest(result[field], f"certification {field}")
    result["report"] = _validate_report(result["report"])
    for field in ("certified_at", "expires_at"):
        _parse_timestamp(str(result[field]), f"traffic certification {field}")
    return result


def _certification_id(adapter: DirectTrafficAdapter, nonce: str) -> str:
    payload = {
        "schema_version": CERTIFICATION_SCHEMA_VERSION,
        "adapter_contract_version": ADAPTER_CONTRACT_VERSION,
        "adapter": adapter.name,
        "implementation_version": adapter.implementation_version,
        "provider_instance_sha256": adapter.provider_instance_sha256,
        "environment_sha256": adapter.environment_sha256,
        "certification_isolation_mode": adapter.certification_isolation_mode,
        "certification_namespace_sha256": adapter.certification_namespace_sha256(),
        "certification_nonce": nonce,
    }
    return _certification_id_from_values(payload)


def _certification_id_from_values(value: Mapping[str, Any]) -> str:
    payload = {
        "schema_version": CERTIFICATION_SCHEMA_VERSION,
        "adapter_contract_version": value["adapter_contract_version"],
        "adapter": value["adapter"],
        "implementation_version": value["implementation_version"],
        "provider_instance_sha256": value["provider_instance_sha256"],
        "environment_sha256": value["environment_sha256"],
        "certification_isolation_mode": value["certification_isolation_mode"],
        "certification_namespace_sha256": value[
            "certification_namespace_sha256"
        ],
        "certification_nonce": value["certification_nonce"],
    }
    return f"direct-traffic-adapter-cert-{_digest(_canonical_json(payload))[:32]}"


def _challenge_id(adapter: DirectTrafficAdapter, nonce: str) -> str:
    digest = hashlib.sha256(
        _CHALLENGE_DOMAIN
        + bytes.fromhex(adapter.provider_instance_sha256)
        + bytes.fromhex(adapter.environment_sha256)
        + bytes.fromhex(adapter.certification_namespace_sha256())
        + nonce.encode("utf-8")
    ).hexdigest()
    return f"direct-traffic-challenge-{digest[:32]}"


def _sign(key: bytes, payload: Mapping[str, Any]) -> str:
    derived = hmac.new(key, _SIGNING_DOMAIN, hashlib.sha256).digest()
    return hmac.new(derived, _canonical_json(payload), hashlib.sha256).hexdigest()


def _key_from_environment(*, profile: str = "standard") -> tuple[bytes, str]:
    if profile not in {
        "standard", "production", "production_emergency", "production_recovery",
        "production_expansion",
        "production_expansion_25",
        "production_expansion_50",
        "production_expansion_100",
    }:
        raise DirectTrafficCertificationConfigError(
            "traffic certification key profile is invalid"
        )
    profiles = {
        "standard": (KEY_ENV, KEY_ID_ENV),
        "production": (PRODUCTION_KEY_ENV, PRODUCTION_KEY_ID_ENV),
        "production_emergency": (
            PRODUCTION_EMERGENCY_KEY_ENV,
            PRODUCTION_EMERGENCY_KEY_ID_ENV,
        ),
        "production_recovery": (
            PRODUCTION_RECOVERY_KEY_ENV,
            PRODUCTION_RECOVERY_KEY_ID_ENV,
        ),
        "production_expansion": (
            PRODUCTION_EXPANSION_KEY_ENV,
            PRODUCTION_EXPANSION_KEY_ID_ENV,
        ),
        "production_expansion_25": (
            PRODUCTION_EXPANSION_25_KEY_ENV,
            PRODUCTION_EXPANSION_25_KEY_ID_ENV,
        ),
        "production_expansion_50": (
            PRODUCTION_EXPANSION_50_KEY_ENV,
            PRODUCTION_EXPANSION_50_KEY_ID_ENV,
        ),
        "production_expansion_100": (
            PRODUCTION_EXPANSION_100_KEY_ENV,
            PRODUCTION_EXPANSION_100_KEY_ID_ENV,
        ),
    }
    key_name, key_id_name = profiles[profile]
    key = os.environ.get(key_name)
    key_id = os.environ.get(key_id_name)
    if key is None or key_id is None:
        raise DirectTrafficCertificationConfigError(
            f"{key_name} and {key_id_name} are required"
        )
    return key.encode("utf-8"), key_id


def _validate_challenge_id(value: Any) -> str:
    if not isinstance(value, str) or _CHALLENGE_ID_RE.fullmatch(value) is None:
        raise DirectTrafficCertificationError(
            "traffic certification challenge ID is invalid"
        )
    return value


def _validate_digest(value: Any, label: str) -> str:
    if not isinstance(value, str) or _DIGEST_RE.fullmatch(value) is None:
        raise DirectTrafficCertificationError(f"{label} is invalid")
    return value


def _validate_nonce(value: Any, label: str) -> str:
    if (
        not isinstance(value, str)
        or not value
        or len(value) > 128
        or value.strip() != value
        or re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._-]{0,127}", value) is None
    ):
        raise DirectTrafficCertificationConfigError(f"{label} is invalid")
    return value


def _bounded_percent(value: Any, label: str) -> int:
    if isinstance(value, bool) or not isinstance(value, int) or not 0 <= value <= 100:
        raise DirectTrafficCertificationConfigError(f"{label} is invalid")
    return value


def _nonnegative_int(value: Any, label: str) -> int:
    if isinstance(value, bool) or not isinstance(value, int) or value < 0:
        raise DirectTrafficCertificationConfigError(f"{label} is invalid")
    return value


def _bounded_int(value: Any, label: str, minimum: int, maximum: int) -> int:
    if (
        isinstance(value, bool)
        or not isinstance(value, int)
        or not minimum <= value <= maximum
    ):
        raise DirectTrafficCertificationConfigError(f"{label} is invalid")
    return value


def _tables(connection: sqlite3.Connection) -> set[str]:
    return {
        str(row[0])
        for row in connection.execute(
            "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'"
        ).fetchall()
    }


def main(argv: Sequence[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        description="Certify the direct traffic adapter protocol in an isolated namespace"
    )
    subparsers = parser.add_subparsers(dest="command", required=True)
    certify = subparsers.add_parser("certify-sandbox")
    verify = subparsers.add_parser("verify-sandbox")
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
        adapter = SQLiteDirectTrafficCertificationAdapter(
            args.certification_ledger,
            environment_sha256(args.environment_id),
            args.provider_instance,
            namespace_id=args.namespace_id,
        )
        key, key_id = _key_from_environment()
        if args.command == "certify-sandbox":
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
                args.certification, adapter, key, key_id
            )
            output = {
                "status": "verified",
                "certification_id": result["certification_id"],
                "expires_at": result["expires_at"],
            }
        print(json.dumps(output, sort_keys=True))
        return 0
    except (
        DirectTrafficCertificationConfigError,
        DirectTrafficConfigError,
        ReleaseGateHistoryError,
    ) as exc:
        print(f"direct traffic certification configuration error: {exc}", file=sys.stderr)
        return 2
    except (
        DirectTrafficCertificationError,
        DirectTrafficError,
        DeploymentControllerError,
        DeploymentResolutionError,
        OSError,
        sqlite3.Error,
    ) as exc:
        print(f"direct traffic certification error: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
