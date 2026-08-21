from __future__ import annotations

import argparse
import hashlib
import hmac
import json
import os
import secrets
import sqlite3
import sys
import time
from contextlib import closing
from datetime import UTC, datetime, timedelta
from pathlib import Path
from threading import Event
from typing import Any, Callable, Literal, Mapping, Sequence, cast

from .deployment_controller import (
    ADAPTER_CERTIFICATION_SCHEMA_VERSION,
    AdapterResult,
    DeploymentAdapterError,
    DeploymentControllerConfigError,
    DeploymentControllerError,
    DeploymentRequest,
    DeploymentLedger,
    _canonical_json,
    _digest,
    _ensure_private_database,
    _format_timestamp,
    _normalize_now,
    _parse_timestamp,
    _validate_digest,
    _validate_lease,
    _validate_private_database,
    _validate_request,
    certify_adapter,
    run_reconciliation_batch,
)
from .deployment_resolution import (
    DeploymentResolutionError,
    _ensure_private_state_directory,
    _read_contract,
    _validate_key,
    _validate_key_id,
    _validate_private_state_directory,
    _write_exclusive_private_json,
)
from .release_attestation import MAX_CLOCK_SKEW_SECONDS


CERTIFICATION_SCHEMA_VERSION = "observability-adapter-certification-attestation-v1"
SCHEDULER_RUN_SCHEMA_VERSION = "observability-deployment-scheduler-run-v1"
CERTIFICATION_KEY_ENV = "OBSERVABILITY_ADAPTER_CERTIFICATION_KEY"
CERTIFICATION_KEY_ID_ENV = "OBSERVABILITY_ADAPTER_CERTIFICATION_KEY_ID"
MAX_CERTIFICATION_TTL_SECONDS = 24 * 60 * 60
MAX_SCHEDULER_ITERATIONS = 1000
MAX_SCHEDULER_INTERVAL_SECONDS = 15 * 60
_CERTIFICATION_DOMAIN = b"ai-companion/deployment-adapter-certification/v1"
_SANDBOX_SCHEMA_VERSION = 2
_SANDBOX_OPERATION_COLUMNS = {
    "idempotency_key",
    "request_sha256",
    "external_operation_id",
    "status",
    "lookup_count",
    "complete_after_lookups",
    "created_at",
    "updated_at",
}
_SANDBOX_METADATA_COLUMNS = {"singleton", "instance_id", "created_at"}


class DeploymentSchedulerError(ValueError):
    pass


class DeploymentSchedulerConfigError(DeploymentSchedulerError):
    pass


class SQLiteSandboxAdapter:
    name = "sqlite-sandbox"
    implementation_version = "1.0.0"
    supports_idempotent_submit = True
    supports_lookup = True

    def __init__(self, path: Path, *, complete_after_lookups: int = 1) -> None:
        if (
            isinstance(complete_after_lookups, bool)
            or not isinstance(complete_after_lookups, int)
            or not 1 <= complete_after_lookups <= 100
        ):
            raise DeploymentSchedulerConfigError(
                "sandbox completion lookup count must be between 1 and 100"
            )
        self.path = path
        self.complete_after_lookups = complete_after_lookups
        try:
            _ensure_private_database(path)
        except DeploymentControllerError as exc:
            raise DeploymentSchedulerError(
                "sandbox provider ledger is not private"
            ) from exc
        self._initialize()

    @property
    def provider_instance_sha256(self) -> str:
        _validate_sandbox_database(self.path)
        with closing(self._connect()) as connection, connection:
            row = connection.execute(
                "SELECT instance_id FROM sandbox_metadata WHERE singleton = 1"
            ).fetchone()
        if row is None:
            raise DeploymentSchedulerError(
                "sandbox provider instance identity is missing"
            )
        return _digest(str(row["instance_id"]).encode("utf-8"))

    def submit(self, request: DeploymentRequest) -> AdapterResult:
        _validate_request(request)
        if not request.target.startswith("isolated-"):
            raise DeploymentAdapterError("sandbox_target_required", indeterminate=True)
        external_operation_id = f"sandbox-{request.idempotency_key[:24]}"
        timestamp = _format_timestamp(datetime.now(UTC))
        with closing(self._connect()) as connection, connection:
            connection.execute("BEGIN IMMEDIATE")
            existing = connection.execute(
                "SELECT * FROM sandbox_operations WHERE idempotency_key = ?",
                (request.idempotency_key,),
            ).fetchone()
            if existing is not None:
                if existing["request_sha256"] != request.request_sha256:
                    raise DeploymentAdapterError(
                        "sandbox_idempotency_conflict",
                        indeterminate=True,
                    )
                connection.commit()
                return AdapterResult(
                    status=_sandbox_status(existing["status"]),
                    external_operation_id=str(existing["external_operation_id"]),
                )
            connection.execute(
                """
                INSERT INTO sandbox_operations (
                    idempotency_key, request_sha256, external_operation_id,
                    status, lookup_count, complete_after_lookups, created_at, updated_at
                ) VALUES (?, ?, ?, 'accepted', 0, ?, ?, ?)
                """,
                (
                    request.idempotency_key,
                    request.request_sha256,
                    external_operation_id,
                    self.complete_after_lookups,
                    timestamp,
                    timestamp,
                ),
            )
            connection.commit()
        return AdapterResult(status="accepted", external_operation_id=external_operation_id)

    def lookup(self, idempotency_key: str) -> AdapterResult | None:
        _validate_digest(idempotency_key, "idempotency_key")
        timestamp = _format_timestamp(datetime.now(UTC))
        with closing(self._connect()) as connection, connection:
            connection.execute("BEGIN IMMEDIATE")
            row = connection.execute(
                "SELECT * FROM sandbox_operations WHERE idempotency_key = ?",
                (idempotency_key,),
            ).fetchone()
            if row is None:
                connection.commit()
                return None
            lookup_count = int(row["lookup_count"]) + 1
            status_value: Literal["accepted", "completed"] = _sandbox_status(
                row["status"]
            )
            if lookup_count >= int(row["complete_after_lookups"]):
                status_value = "completed"
            connection.execute(
                """
                UPDATE sandbox_operations
                SET lookup_count = ?, status = ?, updated_at = ?
                WHERE idempotency_key = ?
                """,
                (lookup_count, status_value, timestamp, idempotency_key),
            )
            connection.commit()
        return AdapterResult(
            status=status_value,
            external_operation_id=str(row["external_operation_id"]),
        )

    def operation(self, idempotency_key: str) -> dict[str, Any] | None:
        _validate_digest(idempotency_key, "idempotency_key")
        with closing(self._connect()) as connection, connection:
            row = connection.execute(
                "SELECT * FROM sandbox_operations WHERE idempotency_key = ?",
                (idempotency_key,),
            ).fetchone()
        return dict(row) if row is not None else None

    def _initialize(self) -> None:
        with closing(self._connect()) as connection, connection:
            version = int(connection.execute("PRAGMA user_version").fetchone()[0])
            tables = {
                str(row[0])
                for row in connection.execute(
                    "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'"
                ).fetchall()
            }
            if version == 0 and not tables:
                connection.execute("BEGIN IMMEDIATE")
                self._create_operations_table(connection)
                self._create_metadata_table(connection)
                connection.execute(f"PRAGMA user_version = {_SANDBOX_SCHEMA_VERSION}")
                connection.commit()
            elif version == 1 and tables == {"sandbox_operations"}:
                self._validate_operation_columns(connection)
                connection.execute("BEGIN IMMEDIATE")
                self._create_metadata_table(connection)
                connection.execute(f"PRAGMA user_version = {_SANDBOX_SCHEMA_VERSION}")
                connection.commit()
            elif version != _SANDBOX_SCHEMA_VERSION or tables != {
                "sandbox_operations",
                "sandbox_metadata",
            }:
                raise DeploymentSchedulerError("sandbox provider ledger schema is invalid")
            self._validate_operation_columns(connection)
            metadata_columns = {
                str(row[1])
                for row in connection.execute(
                    "PRAGMA table_info(sandbox_metadata)"
                ).fetchall()
            }
            if metadata_columns != _SANDBOX_METADATA_COLUMNS:
                raise DeploymentSchedulerError(
                    "sandbox provider metadata columns are invalid"
                )
            metadata = connection.execute(
                "SELECT singleton, instance_id, created_at FROM sandbox_metadata"
            ).fetchall()
            if (
                len(metadata) != 1
                or int(metadata[0]["singleton"]) != 1
                or not isinstance(metadata[0]["instance_id"], str)
                or not str(metadata[0]["instance_id"]).startswith("sandbox-instance-")
            ):
                raise DeploymentSchedulerError(
                    "sandbox provider instance identity is invalid"
                )
            _parse_timestamp(str(metadata[0]["created_at"]), "sandbox created_at")
        _validate_sandbox_database(self.path)

    @staticmethod
    def _create_operations_table(connection: sqlite3.Connection) -> None:
        connection.execute(
            """
            CREATE TABLE sandbox_operations (
                idempotency_key TEXT PRIMARY KEY,
                request_sha256 TEXT NOT NULL,
                external_operation_id TEXT NOT NULL UNIQUE,
                status TEXT NOT NULL CHECK (status IN ('accepted', 'completed')),
                lookup_count INTEGER NOT NULL CHECK (lookup_count >= 0),
                complete_after_lookups INTEGER NOT NULL
                    CHECK (complete_after_lookups > 0),
                created_at TEXT NOT NULL,
                updated_at TEXT NOT NULL
            )
            """
        )

    @staticmethod
    def _create_metadata_table(connection: sqlite3.Connection) -> None:
        connection.execute(
            """
            CREATE TABLE sandbox_metadata (
                singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
                instance_id TEXT NOT NULL UNIQUE,
                created_at TEXT NOT NULL
            )
            """
        )
        connection.execute(
            """
            INSERT INTO sandbox_metadata (singleton, instance_id, created_at)
            VALUES (1, ?, ?)
            """,
            (
                f"sandbox-instance-{secrets.token_hex(16)}",
                _format_timestamp(datetime.now(UTC)),
            ),
        )

    @staticmethod
    def _validate_operation_columns(connection: sqlite3.Connection) -> None:
        columns = {
            str(row[1])
            for row in connection.execute(
                "PRAGMA table_info(sandbox_operations)"
            ).fetchall()
        }
        if columns != _SANDBOX_OPERATION_COLUMNS:
            raise DeploymentSchedulerError(
                "sandbox provider ledger columns are invalid"
            )

    def _connect(self) -> sqlite3.Connection:
        _validate_sandbox_database(self.path)
        connection = sqlite3.connect(str(self.path), timeout=5.0, isolation_level=None)
        connection.row_factory = sqlite3.Row
        connection.execute("PRAGMA busy_timeout = 5000")
        connection.execute("PRAGMA synchronous = FULL")
        return connection


def build_sandbox_request(
    deployment_id: str,
    *,
    nonce: str | None = None,
) -> DeploymentRequest:
    suffix = nonce or secrets.token_hex(8)
    seed = _digest(f"{deployment_id}:{suffix}".encode("utf-8"))
    payload = {
        "schema_version": "observability-deployment-request-v1",
        "deployment_id": deployment_id,
        "authorization_id": f"auth-{seed[:32]}",
        "idempotency_key": _digest(f"sandbox-idempotency:{seed}".encode("utf-8")),
        "run_id": f"sandbox-{seed[:24]}",
        "target": "isolated-certification",
        "adapter": SQLiteSandboxAdapter.name,
        "authorization_sha256": _digest(f"authorization:{seed}".encode("utf-8")),
        "gate_report_sha256": _digest(f"gate:{seed}".encode("utf-8")),
    }
    request_digest = _digest(_canonical_json(payload))
    request = DeploymentRequest(**payload, request_sha256=request_digest)
    _validate_request(request)
    return request


def certify_sandbox_adapter(
    adapter: SQLiteSandboxAdapter,
    output_dir: Path,
    key: bytes,
    key_id: str,
    *,
    nonce: str,
    ttl_seconds: int = 3600,
    now: datetime | None = None,
) -> tuple[dict[str, Any], Path, bool]:
    current_time = _normalize_now(now)
    signing_key = _validate_key(key, "certification key")
    signing_key_id = _validate_key_id(key_id, "certification key_id")
    certification_nonce = _validate_key_id(nonce, "certification nonce")
    ttl = _validate_certification_ttl(ttl_seconds)
    request = build_sandbox_request(
        f"certify-{adapter.name}",
        nonce=certification_nonce,
    )
    certification_id = _certification_id(
        adapter.name,
        adapter.implementation_version,
        adapter.provider_instance_sha256,
        request.request_sha256,
        certification_nonce,
    )
    _ensure_private_state_directory(output_dir)
    path = output_dir / f"{certification_id}.json"
    if path.exists() or path.is_symlink():
        existing = verify_adapter_certification(
            path,
            adapter,
            signing_key,
            signing_key_id,
            now=current_time,
        )
        return existing, path, False
    report = certify_adapter(adapter, request)
    report_sha256 = _digest(_canonical_json(report))
    payload: dict[str, Any] = {
        "schema_version": CERTIFICATION_SCHEMA_VERSION,
        "certification_id": certification_id,
        "certification_nonce": certification_nonce,
        "adapter": adapter.name,
        "implementation_version": adapter.implementation_version,
        "provider_instance_sha256": adapter.provider_instance_sha256,
        "supports_idempotent_submit": adapter.supports_idempotent_submit,
        "supports_lookup": adapter.supports_lookup,
        "request_sha256": request.request_sha256,
        "report_sha256": report_sha256,
        "report": report,
        "key_id": signing_key_id,
        "certified_at": _format_timestamp(current_time),
        "expires_at": _format_timestamp(current_time + timedelta(seconds=ttl)),
    }
    certification = dict(payload)
    certification["signature"] = _sign_certification(signing_key, payload)
    certification = _validate_certification_contract(certification)
    try:
        _write_exclusive_private_json(path, certification)
        return certification, path, True
    except FileExistsError:
        existing = verify_adapter_certification(
            path,
            adapter,
            signing_key,
            signing_key_id,
            now=current_time,
        )
        return existing, path, False


def verify_adapter_certification(
    path: Path,
    adapter: SQLiteSandboxAdapter,
    key: bytes,
    key_id: str,
    *,
    now: datetime | None = None,
) -> dict[str, Any]:
    current_time = _normalize_now(now)
    verification_key = _validate_key(key, "certification key")
    expected_key_id = _validate_key_id(key_id, "certification key_id")
    certification = _read_contract(path, _validate_certification_contract)
    if certification["key_id"] != expected_key_id:
        raise DeploymentSchedulerError("adapter certification key_id is not trusted")
    payload = {field: value for field, value in certification.items() if field != "signature"}
    expected_signature = _sign_certification(verification_key, payload)
    if not hmac.compare_digest(str(certification["signature"]), expected_signature):
        raise DeploymentSchedulerError("adapter certification signature is invalid")
    expected_bindings = {
        "adapter": adapter.name,
        "implementation_version": adapter.implementation_version,
        "provider_instance_sha256": adapter.provider_instance_sha256,
        "supports_idempotent_submit": adapter.supports_idempotent_submit,
        "supports_lookup": adapter.supports_lookup,
    }
    if any(certification.get(field) != value for field, value in expected_bindings.items()):
        raise DeploymentSchedulerError(
            "adapter certification does not match the registered implementation"
        )
    report = certification["report"]
    if certification["report_sha256"] != _digest(_canonical_json(report)):
        raise DeploymentSchedulerError("adapter certification report digest is invalid")
    expected_certification_id = _certification_id(
        str(certification["adapter"]),
        str(certification["implementation_version"]),
        str(certification["provider_instance_sha256"]),
        str(certification["request_sha256"]),
        str(certification["certification_nonce"]),
    )
    if certification["certification_id"] != expected_certification_id:
        raise DeploymentSchedulerError("adapter certification ID binding is invalid")
    if certification["request_sha256"] != report["request_sha256"]:
        raise DeploymentSchedulerError(
            "adapter certification request binding is invalid"
        )
    certified_at = _parse_timestamp(str(certification["certified_at"]), "certified_at")
    expires_at = _parse_timestamp(str(certification["expires_at"]), "expires_at")
    if certified_at > current_time + timedelta(seconds=MAX_CLOCK_SKEW_SECONDS):
        raise DeploymentSchedulerError("adapter certification timestamp is in the future")
    if expires_at <= certified_at or (
        expires_at - certified_at > timedelta(seconds=MAX_CERTIFICATION_TTL_SECONDS)
    ):
        raise DeploymentSchedulerError("adapter certification lifetime is invalid")
    if current_time >= expires_at:
        raise DeploymentSchedulerError("adapter certification has expired")
    if report["adapter"] != certification["adapter"]:
        raise DeploymentSchedulerError("adapter certification report binding is invalid")
    return certification


def find_adapter_certification_path(
    directory: Path,
    adapter: SQLiteSandboxAdapter,
    key: bytes,
    key_id: str,
    nonce: str,
    *,
    now: datetime | None = None,
) -> Path:
    current_time = _normalize_now(now)
    expected_nonce = _validate_key_id(nonce, "certification nonce")
    _validate_private_state_directory(directory)
    matches: list[Path] = []
    try:
        entries = sorted(directory.iterdir())
    except OSError as exc:
        raise DeploymentSchedulerError(
            f"cannot list adapter certification directory: {exc}"
        ) from exc
    for path in entries:
        if not path.name.startswith("adapter-cert-") or path.suffix != ".json":
            raise DeploymentSchedulerError(
                "adapter certification directory contains an unexpected artifact"
            )
        certification = _read_contract(path, _validate_certification_contract)
        if certification["certification_nonce"] != expected_nonce:
            continue
        matches.append(path)
    if len(matches) != 1:
        raise DeploymentSchedulerError(
            "exactly one adapter certification must match the requested nonce"
        )
    verify_adapter_certification(
        matches[0],
        adapter,
        key,
        key_id,
        now=current_time,
    )
    return matches[0]


def run_certified_scheduler_once(
    ledger: DeploymentLedger,
    adapter: SQLiteSandboxAdapter,
    certification_path: Path,
    key: bytes,
    key_id: str,
    *,
    limit: int = 100,
    lease_seconds: int = 30,
    now: datetime | None = None,
) -> dict[str, Any]:
    current_time = _normalize_now(now)
    if isinstance(limit, bool) or not isinstance(limit, int) or not 1 <= limit <= 1000:
        raise DeploymentSchedulerConfigError("scheduler batch limit is invalid")
    _validate_lease(lease_seconds)
    certification = verify_adapter_certification(
        certification_path,
        adapter,
        key,
        key_id,
        now=current_time,
    )
    before = ledger.reconciliation_health(current_time)
    batch = run_reconciliation_batch(
        ledger,
        {adapter.name: adapter},
        limit=limit,
        lease_seconds=lease_seconds,
        now=current_time,
    )
    after = ledger.reconciliation_health(current_time)
    return _validate_scheduler_run_contract({
        "schema_version": SCHEDULER_RUN_SCHEMA_VERSION,
        "generated_at": _format_timestamp(current_time),
        "adapter": adapter.name,
        "implementation_version": adapter.implementation_version,
        "certification_id": certification["certification_id"],
        "before": before,
        "batch": batch,
        "after": after,
    })


def run_certified_scheduler_loop(
    run_once: Callable[[datetime], dict[str, Any]],
    *,
    interval_seconds: int,
    iterations: int,
    start_time: datetime | None = None,
    stop_event: Event | None = None,
    sleep: Callable[[float], None] = time.sleep,
) -> list[dict[str, Any]]:
    if (
        isinstance(interval_seconds, bool)
        or not isinstance(interval_seconds, int)
        or not 1 <= interval_seconds <= MAX_SCHEDULER_INTERVAL_SECONDS
    ):
        raise DeploymentSchedulerConfigError("scheduler interval is invalid")
    if (
        isinstance(iterations, bool)
        or not isinstance(iterations, int)
        or not 1 <= iterations <= MAX_SCHEDULER_ITERATIONS
    ):
        raise DeploymentSchedulerConfigError("scheduler iteration count is invalid")
    current_time = _normalize_now(start_time)
    reports: list[dict[str, Any]] = []
    for index in range(iterations):
        if stop_event is not None and stop_event.is_set():
            break
        reports.append(run_once(current_time))
        if index + 1 < iterations:
            if stop_event is not None:
                if stop_event.wait(float(interval_seconds)):
                    break
            else:
                sleep(float(interval_seconds))
            current_time += timedelta(seconds=interval_seconds)
    return reports


def main(argv: Sequence[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        description="Certify and run the isolated SQLite deployment provider sandbox"
    )
    subparsers = parser.add_subparsers(dest="command", required=True)
    certify_parser = subparsers.add_parser("certify-sandbox")
    certify_parser.add_argument("--provider-ledger", type=Path, required=True)
    certify_parser.add_argument("--output-dir", type=Path, required=True)
    certify_parser.add_argument("--nonce", required=True)
    certify_parser.add_argument("--ttl-seconds", type=int, default=3600)
    initialize_parser = subparsers.add_parser("initialize-sandbox")
    initialize_parser.add_argument("--ledger", type=Path, required=True)
    initialize_parser.add_argument("--provider-ledger", type=Path, required=True)
    initialize_parser.add_argument("--output-dir", type=Path, required=True)
    initialize_parser.add_argument("--nonce", required=True)
    initialize_parser.add_argument("--ttl-seconds", type=int, default=3600)
    run_parser = subparsers.add_parser("run-sandbox-once")
    run_parser.add_argument("--ledger", type=Path, required=True)
    run_parser.add_argument("--provider-ledger", type=Path, required=True)
    run_parser.add_argument("--certification", type=Path, required=True)
    run_parser.add_argument("--limit", type=int, default=100)
    run_parser.add_argument("--lease-seconds", type=int, default=30)
    args = parser.parse_args(argv)
    try:
        key, key_id = _certification_key_from_environment()
        adapter = SQLiteSandboxAdapter(args.provider_ledger)
        if args.command in {"certify-sandbox", "initialize-sandbox"}:
            ledger_status: dict[str, Any] | None = None
            if args.command == "initialize-sandbox":
                ledger = DeploymentLedger(args.ledger)
                ledger_status = {
                    "path": str(ledger.path),
                    "status": "ready",
                }
            certification, path, created = certify_sandbox_adapter(
                adapter,
                args.output_dir,
                key,
                key_id,
                nonce=args.nonce,
                ttl_seconds=args.ttl_seconds,
            )
            output = {
                "adapter": adapter.name,
                "certification": str(path),
                "certification_id": certification["certification_id"],
                "certification_nonce": certification["certification_nonce"],
                "expires_at": certification["expires_at"],
                "status": "certified" if created else "reused",
            }
            if ledger_status is not None:
                output["controller_ledger"] = ledger_status
        else:
            ledger = DeploymentLedger(args.ledger, create=False)
            output = run_certified_scheduler_once(
                ledger,
                adapter,
                args.certification,
                key,
                key_id,
                limit=args.limit,
                lease_seconds=args.lease_seconds,
            )
    except (
        DeploymentSchedulerConfigError,
        DeploymentControllerConfigError,
    ) as exc:
        print(f"deployment scheduler configuration error: {exc}", file=sys.stderr)
        return 2
    except (
        DeploymentSchedulerError,
        DeploymentControllerError,
        DeploymentResolutionError,
        OSError,
        sqlite3.Error,
    ) as exc:
        print(f"deployment scheduler error: {exc}", file=sys.stderr)
        return 1
    print(json.dumps(output, sort_keys=True))
    return 0


def _validate_certification_contract(value: Any) -> dict[str, Any]:
    fields = {
        "schema_version", "certification_id", "certification_nonce", "adapter", "implementation_version",
        "provider_instance_sha256", "supports_idempotent_submit", "supports_lookup", "request_sha256",
        "report_sha256", "report", "key_id", "certified_at", "expires_at", "signature",
    }
    if not isinstance(value, Mapping) or set(value) != fields:
        raise DeploymentSchedulerError("adapter certification contract is invalid")
    result = dict(value)
    if result["schema_version"] != CERTIFICATION_SCHEMA_VERSION:
        raise DeploymentSchedulerError("adapter certification schema_version is invalid")
    for field in (
        "certification_id",
        "certification_nonce",
        "adapter",
        "implementation_version",
        "key_id",
    ):
        if not isinstance(result[field], str) or not result[field]:
            raise DeploymentSchedulerError(f"adapter certification {field} is invalid")
    if not str(result["certification_id"]).startswith("adapter-cert-") or len(
        str(result["certification_id"])
    ) != len("adapter-cert-") + 32:
        raise DeploymentSchedulerError(
            "adapter certification certification_id is invalid"
        )
    _validate_key_id(str(result["key_id"]), "certification key_id")
    _validate_key_id(str(result["certification_nonce"]), "certification nonce")
    for field in ("supports_idempotent_submit", "supports_lookup"):
        if result[field] is not True:
            raise DeploymentSchedulerError("adapter certification capabilities are invalid")
    for field in (
        "provider_instance_sha256",
        "request_sha256",
        "report_sha256",
        "signature",
    ):
        _validate_digest(str(result[field]), field)
    result["report"] = _validate_certification_report(result["report"])
    for field in ("certified_at", "expires_at"):
        if not isinstance(result[field], str):
            raise DeploymentSchedulerError(f"adapter certification {field} is invalid")
        _parse_timestamp(str(result[field]), field)
    return result


def _validate_certification_report(value: Any) -> dict[str, Any]:
    fields = {
        "schema_version",
        "adapter",
        "passed",
        "idempotent_submit",
        "lookup",
        "request_sha256",
        "external_operation_sha256",
        "submit_statuses",
        "lookup_status",
    }
    if not isinstance(value, Mapping) or set(value) != fields:
        raise DeploymentSchedulerError("adapter certification report is invalid")
    result = dict(value)
    if result["schema_version"] != ADAPTER_CERTIFICATION_SCHEMA_VERSION:
        raise DeploymentSchedulerError(
            "adapter certification report schema_version is invalid"
        )
    if (
        not isinstance(result["adapter"], str)
        or not result["adapter"]
        or result["passed"] is not True
        or result["idempotent_submit"] is not True
        or result["lookup"] is not True
    ):
        raise DeploymentSchedulerError("adapter certification report is invalid")
    _validate_digest(str(result["request_sha256"]), "request_sha256")
    _validate_digest(
        str(result["external_operation_sha256"]),
        "external_operation_sha256",
    )
    submit_statuses = result["submit_statuses"]
    if (
        not isinstance(submit_statuses, list)
        or len(submit_statuses) != 2
        or any(status not in {"accepted", "completed"} for status in submit_statuses)
        or result["lookup_status"] not in {"accepted", "completed"}
    ):
        raise DeploymentSchedulerError(
            "adapter certification report results are invalid"
        )
    return result


def _validate_scheduler_run_contract(value: Any) -> dict[str, Any]:
    fields = {
        "schema_version",
        "generated_at",
        "adapter",
        "implementation_version",
        "certification_id",
        "before",
        "batch",
        "after",
    }
    if not isinstance(value, Mapping) or set(value) != fields:
        raise DeploymentSchedulerError("deployment scheduler report is invalid")
    result = dict(value)
    if result["schema_version"] != SCHEDULER_RUN_SCHEMA_VERSION:
        raise DeploymentSchedulerError(
            "deployment scheduler report schema_version is invalid"
        )
    for field in ("generated_at", "adapter", "implementation_version", "certification_id"):
        if not isinstance(result[field], str) or not result[field]:
            raise DeploymentSchedulerError(
                f"deployment scheduler report {field} is invalid"
            )
    _parse_timestamp(str(result["generated_at"]), "generated_at")
    expected_health_fields = {
        "schema_version",
        "generated_at",
        "accepted_pending",
        "due",
        "overdue_deadline",
        "schedule_missing",
        "indeterminate",
        "oldest_accepted_age_seconds",
    }
    for field in ("before", "after"):
        health = result[field]
        if not isinstance(health, Mapping) or set(health) != expected_health_fields:
            raise DeploymentSchedulerError(
                f"deployment scheduler report {field} health is invalid"
            )
    expected_batch_fields = {
        "selected",
        "completed",
        "pending",
        "indeterminate",
        "busy",
        "adapter_unavailable",
        "retryable_errors",
    }
    batch = result["batch"]
    if (
        not isinstance(batch, Mapping)
        or set(batch) != expected_batch_fields
        or any(isinstance(item, bool) or not isinstance(item, int) or item < 0 for item in batch.values())
    ):
        raise DeploymentSchedulerError("deployment scheduler batch report is invalid")
    return result


def _sign_certification(key: bytes, payload: Mapping[str, Any]) -> str:
    derived = hmac.new(key, _CERTIFICATION_DOMAIN, hashlib.sha256).digest()
    return hmac.new(derived, _canonical_json(payload), hashlib.sha256).hexdigest()


def _certification_id(
    adapter: str,
    implementation_version: str,
    provider_instance_sha256: str,
    request_sha256: str,
    certification_nonce: str,
) -> str:
    binding = {
        "adapter": adapter,
        "implementation_version": implementation_version,
        "provider_instance_sha256": provider_instance_sha256,
        "request_sha256": request_sha256,
        "certification_nonce": certification_nonce,
    }
    return f"adapter-cert-{_digest(_canonical_json(binding))[:32]}"


def _validate_certification_ttl(value: int) -> int:
    if isinstance(value, bool) or not isinstance(value, int):
        raise DeploymentSchedulerConfigError("certification TTL must be an integer")
    if not 1 <= value <= MAX_CERTIFICATION_TTL_SECONDS:
        raise DeploymentSchedulerConfigError("certification TTL is outside policy")
    return value


def _sandbox_status(value: Any) -> Literal["accepted", "completed"]:
    if value not in {"accepted", "completed"}:
        raise DeploymentSchedulerError("sandbox provider status is invalid")
    return cast(Literal["accepted", "completed"], value)


def _validate_sandbox_database(path: Path) -> None:
    try:
        _validate_private_database(path)
    except DeploymentControllerError as exc:
        raise DeploymentSchedulerError(
            "sandbox provider ledger is not private"
        ) from exc


def _certification_key_from_environment() -> tuple[bytes, str]:
    key = os.environ.get(CERTIFICATION_KEY_ENV)
    key_id = os.environ.get(CERTIFICATION_KEY_ID_ENV)
    if key is None or key_id is None:
        raise DeploymentSchedulerConfigError(
            "adapter certification key and key ID are required"
        )
    return key.encode("utf-8"), key_id


if __name__ == "__main__":
    raise SystemExit(main())
