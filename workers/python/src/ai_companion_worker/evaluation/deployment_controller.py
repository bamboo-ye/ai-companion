from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import secrets
import sqlite3
import stat
import sys
from contextlib import closing
from dataclasses import asdict, dataclass
from datetime import UTC, datetime, timedelta
from pathlib import Path
from typing import Any, Literal, Mapping, Protocol, Sequence

from .deployment_authorization import (
    DeploymentAuthorizationError,
    verify_deployment_authorization,
)
from .release_attestation import KEY_ENV, KEY_ID_ENV, AttestationError


REQUEST_SCHEMA_VERSION = "observability-deployment-request-v1"
ADAPTER_CERTIFICATION_SCHEMA_VERSION = "observability-adapter-certification-v1"
RECONCILIATION_HEALTH_SCHEMA_VERSION = (
    "observability-deployment-reconciliation-health-v1"
)
LEDGER_SCHEMA_VERSION = 4
MAX_LEASE_SECONDS = 5 * 60
MAX_RECONCILIATION_DELAY_SECONDS = 15 * 60
MAX_RECONCILIATION_ATTEMPTS = 100
MAX_RECONCILIATION_DEADLINE_SECONDS = 24 * 60 * 60
_IDENTIFIER_RE = re.compile(r"[A-Za-z0-9][A-Za-z0-9._-]{0,127}")
_TARGET_RE = re.compile(r"[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}")
_DIGEST_RE = re.compile(r"[0-9a-f]{64}")
_ERROR_CODE_RE = re.compile(r"[a-z][a-z0-9_]{0,63}")
_EXTERNAL_ID_RE = re.compile(r"[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}")
_TERMINAL_STATUSES = {"accepted", "completed", "simulated"}
_OPERATION_COLUMNS = {
    "deployment_id",
    "authorization_id",
    "idempotency_key",
    "run_id",
    "target",
    "adapter",
    "authorization_sha256",
    "gate_report_sha256",
    "request_sha256",
    "status",
    "attempt_count",
    "reconciliation_count",
    "lease_token",
    "lease_expires_at",
    "external_operation_id",
    "error_code",
    "created_at",
    "updated_at",
}
_EVENT_COLUMNS = {
    "id",
    "deployment_id",
    "sequence",
    "event_type",
    "status",
    "attempt",
    "occurred_at",
}
_RECONCILIATION_SCHEDULE_COLUMNS = {
    "deployment_id",
    "accepted_at",
    "next_reconcile_at",
    "deadline_at",
    "base_delay_seconds",
    "max_delay_seconds",
    "max_attempts",
    "last_delay_seconds",
}
_RESOLUTION_COLUMNS = {
    "resolution_id",
    "deployment_id",
    "proposed_status",
    "adapter",
    "external_operation_id",
    "reason_code",
    "expected_updated_at",
    "previous_error_code",
    "evidence_sha256",
    "provider_evidence_sha256",
    "request_sha256",
    "approval_sha256",
    "requested_by",
    "approved_by",
    "requester_key_id",
    "approver_key_id",
    "resolved_at",
}
_RESOLUTION_ID_RE = re.compile(r"resolve-[0-9a-f]{32}")


class DeploymentControllerError(ValueError):
    pass


class DeploymentControllerConfigError(DeploymentControllerError):
    pass


class DeploymentBusyError(DeploymentControllerError):
    pass


class DeploymentNotDueError(DeploymentBusyError):
    pass


class DeploymentIndeterminateError(DeploymentControllerError):
    pass


class AdapterCertificationError(DeploymentControllerError):
    pass


class DeploymentAdapterError(RuntimeError):
    def __init__(
        self,
        code: str,
        *,
        retryable: bool = False,
        indeterminate: bool = False,
    ) -> None:
        if _ERROR_CODE_RE.fullmatch(code) is None:
            raise ValueError("adapter error code is invalid")
        super().__init__(code)
        self.code = code
        self.retryable = retryable
        self.indeterminate = indeterminate


@dataclass(frozen=True)
class DeploymentRequest:
    schema_version: str
    deployment_id: str
    authorization_id: str
    idempotency_key: str
    run_id: str
    target: str
    adapter: str
    authorization_sha256: str
    gate_report_sha256: str
    request_sha256: str


@dataclass(frozen=True)
class AdapterResult:
    status: Literal["accepted", "completed", "simulated"]
    external_operation_id: str | None = None


@dataclass(frozen=True)
class ReconciliationPolicy:
    base_delay_seconds: int = 15
    max_delay_seconds: int = 120
    max_attempts: int = 10
    deadline_seconds: int = 30 * 60


DEFAULT_RECONCILIATION_POLICY = ReconciliationPolicy()


@dataclass(frozen=True)
class ManualResolution:
    resolution_id: str
    deployment_id: str
    proposed_status: Literal["completed", "failed"]
    adapter: str
    external_operation_id: str | None
    reason_code: str
    expected_updated_at: str
    expected_error_code: str | None
    evidence_sha256: str
    provider_evidence_sha256: str
    request_sha256: str
    approval_sha256: str
    requested_by: str
    approved_by: str
    requester_key_id: str
    approver_key_id: str


class DeploymentAdapter(Protocol):
    name: str
    supports_idempotent_submit: bool
    supports_lookup: bool

    def submit(self, request: DeploymentRequest) -> AdapterResult:
        ...

    def lookup(self, idempotency_key: str) -> AdapterResult | None:
        ...


class DryRunAdapter:
    name = "dry-run"
    supports_idempotent_submit = True
    supports_lookup = False

    def submit(self, request: DeploymentRequest) -> AdapterResult:
        _validate_request(request)
        return AdapterResult(status="simulated")

    def lookup(self, idempotency_key: str) -> AdapterResult | None:
        _validate_digest(idempotency_key, "idempotency_key")
        return None


@dataclass(frozen=True)
class _Lease:
    request: DeploymentRequest
    token: str
    attempt: int
    recovering: bool


@dataclass(frozen=True)
class _ReconciliationLease:
    request: DeploymentRequest
    token: str
    reconciliation: int


class DeploymentLedger:
    def __init__(self, path: Path, *, create: bool = True) -> None:
        self.path = path
        if create:
            _ensure_private_database(path)
            self._initialize()
        else:
            _validate_private_database(path)
            self._validate_schema()

    def prepare(
        self,
        request: DeploymentRequest,
        now: datetime,
    ) -> tuple[dict[str, Any], bool]:
        _validate_request(request)
        timestamp = _format_timestamp(now)
        with closing(self._connect()) as connection, connection:
            connection.execute("BEGIN IMMEDIATE")
            existing = self._select_operation(connection, request.deployment_id)
            if existing is not None:
                self._require_same_request(existing, request)
                connection.commit()
                return existing, False
            connection.execute(
                """
                INSERT INTO deployment_operations (
                    deployment_id, authorization_id, idempotency_key, run_id,
                    target, adapter, authorization_sha256, gate_report_sha256,
                    request_sha256, status, attempt_count, created_at, updated_at
                ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'prepared', 0, ?, ?)
                """,
                (
                    request.deployment_id,
                    request.authorization_id,
                    request.idempotency_key,
                    request.run_id,
                    request.target,
                    request.adapter,
                    request.authorization_sha256,
                    request.gate_report_sha256,
                    request.request_sha256,
                    timestamp,
                    timestamp,
                ),
            )
            self._append_event(
                connection,
                request.deployment_id,
                "prepared",
                "prepared",
                0,
                timestamp,
            )
            operation = self._select_required(connection, request.deployment_id)
            connection.commit()
        self._validate_files()
        return operation, True

    def acquire(
        self,
        deployment_id: str,
        adapter: DeploymentAdapter,
        now: datetime,
        lease_seconds: int,
    ) -> _Lease | dict[str, Any]:
        _validate_adapter(adapter)
        deployment_id = _validate_identifier(deployment_id, "deployment_id")
        lease_duration = _validate_lease(lease_seconds)
        timestamp = _format_timestamp(now)
        lease_expiry = _format_timestamp(now + timedelta(seconds=lease_duration))
        token = secrets.token_hex(32)
        with closing(self._connect()) as connection, connection:
            connection.execute("BEGIN IMMEDIATE")
            operation = self._select_required(connection, deployment_id)
            if operation["adapter"] != adapter.name:
                raise DeploymentControllerError("ledger adapter does not match request")
            status_value = str(operation["status"])
            if status_value in _TERMINAL_STATUSES:
                connection.commit()
                return operation
            if status_value in {"failed", "indeterminate"}:
                raise DeploymentControllerError(
                    f"deployment status {status_value} cannot be dispatched"
                )

            recovering = False
            if status_value == "dispatching":
                lease_expires_at = _parse_timestamp(
                    str(operation["lease_expires_at"]),
                    "ledger lease_expires_at",
                )
                if now < lease_expires_at:
                    raise DeploymentBusyError("deployment dispatch lease is still active")
                recovering = True
                if not adapter.supports_lookup and not adapter.supports_idempotent_submit:
                    self._finish_without_lease(
                        connection,
                        operation,
                        "indeterminate",
                        "stale_non_idempotent_dispatch",
                        timestamp,
                    )
                    connection.commit()
                    raise DeploymentIndeterminateError(
                        "stale dispatch cannot be retried without lookup or idempotency"
                    )
            elif status_value not in {"prepared", "retryable_failed"}:
                raise DeploymentControllerError("ledger deployment status is invalid")

            attempt = int(operation["attempt_count"]) + 1
            updated = connection.execute(
                """
                UPDATE deployment_operations
                SET status = 'dispatching', attempt_count = ?, lease_token = ?,
                    lease_expires_at = ?, error_code = NULL, updated_at = ?
                WHERE deployment_id = ? AND updated_at = ?
                """,
                (
                    attempt,
                    token,
                    lease_expiry,
                    timestamp,
                    deployment_id,
                    operation["updated_at"],
                ),
            )
            if updated.rowcount != 1:
                raise DeploymentBusyError("deployment ledger changed during lease acquisition")
            self._append_event(
                connection,
                deployment_id,
                "dispatch_recovered" if recovering else "dispatch_started",
                "dispatching",
                attempt,
                timestamp,
            )
            request = _request_from_operation(
                self._select_required(connection, deployment_id)
            )
            connection.commit()
        return _Lease(request=request, token=token, attempt=attempt, recovering=recovering)

    def finish_result(
        self,
        deployment_id: str,
        token: str,
        result: AdapterResult,
        now: datetime,
        reconciliation_policy: ReconciliationPolicy = DEFAULT_RECONCILIATION_POLICY,
    ) -> dict[str, Any]:
        _validate_adapter_result(result)
        _validate_reconciliation_policy(reconciliation_policy)
        return self._finish_with_lease(
            deployment_id,
            token,
            result.status,
            None,
            result.external_operation_id,
            now,
            reconciliation_policy,
        )

    def finish_error(
        self,
        deployment_id: str,
        token: str,
        error: DeploymentAdapterError,
        now: datetime,
    ) -> dict[str, Any]:
        if error.indeterminate:
            status_value = "indeterminate"
        elif error.retryable:
            status_value = "retryable_failed"
        else:
            status_value = "failed"
        return self._finish_with_lease(
            deployment_id,
            token,
            status_value,
            error.code,
            None,
            now,
        )

    def acquire_reconciliation(
        self,
        deployment_id: str,
        adapter: DeploymentAdapter,
        now: datetime,
        lease_seconds: int,
    ) -> _ReconciliationLease | dict[str, Any]:
        _validate_adapter(adapter)
        if not adapter.supports_lookup:
            raise DeploymentControllerConfigError(
                "accepted deployment reconciliation requires adapter lookup"
            )
        deployment_id = _validate_identifier(deployment_id, "deployment_id")
        lease_duration = _validate_lease(lease_seconds)
        timestamp = _format_timestamp(now)
        lease_expiry = _format_timestamp(now + timedelta(seconds=lease_duration))
        token = secrets.token_hex(32)
        with closing(self._connect()) as connection, connection:
            connection.execute("BEGIN IMMEDIATE")
            operation = self._select_required(connection, deployment_id)
            if operation["adapter"] != adapter.name:
                raise DeploymentControllerError("ledger adapter does not match request")
            status_value = str(operation["status"])
            if status_value == "completed":
                raise DeploymentNotDueError(
                    "completed deployment does not require reconciliation"
                )
            if status_value != "accepted":
                raise DeploymentControllerError(
                    f"deployment status {status_value} cannot be reconciled"
                )
            lease_token = operation.get("lease_token")
            if lease_token is not None:
                lease_expires_at = _parse_timestamp(
                    str(operation["lease_expires_at"]),
                    "ledger reconciliation lease_expires_at",
                )
                if now < lease_expires_at:
                    raise DeploymentBusyError("deployment reconciliation lease is still active")
            schedule = self._select_schedule(connection, deployment_id)
            if schedule is None:
                self._finish_without_lease(
                    connection,
                    operation,
                    "indeterminate",
                    "reconciliation_schedule_missing",
                    timestamp,
                    event_type="reconciliation_schedule_missing",
                    event_attempt=int(operation["reconciliation_count"]),
                )
                connection.commit()
                raise DeploymentIndeterminateError(
                    "accepted deployment has no reconciliation schedule"
                )
            next_reconcile_at = _parse_timestamp(
                str(schedule["next_reconcile_at"]),
                "ledger next_reconcile_at",
            )
            if now < next_reconcile_at:
                raise DeploymentNotDueError("deployment reconciliation is not due")
            deadline_at = _parse_timestamp(
                str(schedule["deadline_at"]),
                "ledger deadline_at",
            )
            if now >= deadline_at:
                self._finish_without_lease(
                    connection,
                    operation,
                    "indeterminate",
                    "reconciliation_deadline_exhausted",
                    timestamp,
                    event_type="reconciliation_deadline_exhausted",
                    event_attempt=int(operation["reconciliation_count"]),
                )
                connection.commit()
                raise DeploymentIndeterminateError(
                    "deployment reconciliation deadline is exhausted"
                )
            if int(operation["reconciliation_count"]) >= int(schedule["max_attempts"]):
                self._finish_without_lease(
                    connection,
                    operation,
                    "indeterminate",
                    "reconciliation_budget_exhausted",
                    timestamp,
                    event_type="reconciliation_budget_exhausted",
                    event_attempt=int(operation["reconciliation_count"]),
                )
                connection.commit()
                raise DeploymentIndeterminateError(
                    "deployment reconciliation attempt budget is exhausted"
                )
            reconciliation = int(operation["reconciliation_count"]) + 1
            updated = connection.execute(
                """
                UPDATE deployment_operations
                SET reconciliation_count = ?, lease_token = ?, lease_expires_at = ?,
                    error_code = NULL, updated_at = ?
                WHERE deployment_id = ? AND updated_at = ?
                """,
                (
                    reconciliation,
                    token,
                    lease_expiry,
                    timestamp,
                    deployment_id,
                    operation["updated_at"],
                ),
            )
            if updated.rowcount != 1:
                raise DeploymentBusyError(
                    "deployment ledger changed during reconciliation lease acquisition"
                )
            self._append_event(
                connection,
                deployment_id,
                "reconciliation_started",
                "accepted",
                reconciliation,
                timestamp,
            )
            request = _request_from_operation(
                self._select_required(connection, deployment_id)
            )
            connection.commit()
        return _ReconciliationLease(
            request=request,
            token=token,
            reconciliation=reconciliation,
        )

    def finish_reconciliation_result(
        self,
        deployment_id: str,
        token: str,
        result: AdapterResult,
        now: datetime,
    ) -> dict[str, Any]:
        _validate_adapter_result(result)
        if result.status not in {"accepted", "completed"}:
            raise DeploymentControllerError(
                "reconciliation result must be accepted or completed"
            )
        timestamp = _format_timestamp(now)
        with closing(self._connect()) as connection, connection:
            connection.execute("BEGIN IMMEDIATE")
            operation = self._select_required(connection, deployment_id)
            if operation["status"] != "accepted" or operation["lease_token"] != token:
                raise DeploymentBusyError("deployment reconciliation lease fence was lost")
            if operation["external_operation_id"] != result.external_operation_id:
                self._finish_reconciliation_locked(
                    connection,
                    operation,
                    token,
                    "indeterminate",
                    "external_operation_mismatch",
                    "reconciliation_indeterminate",
                    timestamp,
                )
                connection.commit()
                raise DeploymentIndeterminateError(
                    "adapter lookup returned a different external operation"
                )
            event_type = (
                "reconciliation_completed"
                if result.status == "completed"
                else "reconciliation_pending"
            )
            if result.status == "accepted":
                self._finish_reconciliation_pending_locked(
                    connection,
                    operation,
                    token,
                    None,
                    event_type,
                    now,
                )
            else:
                self._finish_reconciliation_locked(
                    connection,
                    operation,
                    token,
                    result.status,
                    None,
                    event_type,
                    timestamp,
                )
            completed = self._select_required(connection, deployment_id)
            connection.commit()
        self._validate_files()
        return completed

    def finish_reconciliation_error(
        self,
        deployment_id: str,
        token: str,
        error: DeploymentAdapterError,
        now: datetime,
    ) -> dict[str, Any]:
        timestamp = _format_timestamp(now)
        status_value = "accepted" if error.retryable and not error.indeterminate else "indeterminate"
        event_type = (
            "reconciliation_retryable"
            if status_value == "accepted"
            else "reconciliation_indeterminate"
        )
        with closing(self._connect()) as connection, connection:
            connection.execute("BEGIN IMMEDIATE")
            operation = self._select_required(connection, deployment_id)
            if operation["status"] != "accepted" or operation["lease_token"] != token:
                raise DeploymentBusyError("deployment reconciliation lease fence was lost")
            if status_value == "accepted":
                self._finish_reconciliation_pending_locked(
                    connection,
                    operation,
                    token,
                    error.code,
                    event_type,
                    now,
                )
            else:
                self._finish_reconciliation_locked(
                    connection,
                    operation,
                    token,
                    status_value,
                    error.code,
                    event_type,
                    timestamp,
                )
            completed = self._select_required(connection, deployment_id)
            connection.commit()
        self._validate_files()
        return completed

    def get(self, deployment_id: str) -> dict[str, Any]:
        deployment_id = _validate_identifier(deployment_id, "deployment_id")
        with closing(self._connect()) as connection, connection:
            return self._select_required(connection, deployment_id)

    def events(self, deployment_id: str) -> list[dict[str, Any]]:
        deployment_id = _validate_identifier(deployment_id, "deployment_id")
        with closing(self._connect()) as connection, connection:
            rows = connection.execute(
                """
                SELECT sequence, event_type, status, attempt, occurred_at
                FROM deployment_events
                WHERE deployment_id = ? ORDER BY sequence
                """,
                (deployment_id,),
            ).fetchall()
        return [dict(row) for row in rows]

    def schedule(self, deployment_id: str) -> dict[str, Any] | None:
        deployment_id = _validate_identifier(deployment_id, "deployment_id")
        with closing(self._connect()) as connection, connection:
            return self._select_schedule(connection, deployment_id)

    def due_reconciliations(
        self,
        now: datetime,
        *,
        limit: int = 100,
    ) -> list[str]:
        current_time = _normalize_now(now)
        if isinstance(limit, bool) or not isinstance(limit, int) or not 1 <= limit <= 1000:
            raise DeploymentControllerConfigError(
                "reconciliation batch limit must be between 1 and 1000"
            )
        timestamp = _format_timestamp(current_time)
        with closing(self._connect()) as connection, connection:
            rows = connection.execute(
                """
                SELECT operation.deployment_id
                FROM deployment_operations AS operation
                JOIN deployment_reconciliation_schedule AS schedule
                    ON schedule.deployment_id = operation.deployment_id
                WHERE operation.status = 'accepted'
                  AND schedule.next_reconcile_at <= ?
                  AND (
                      operation.lease_token IS NULL
                      OR operation.lease_expires_at <= ?
                  )
                ORDER BY schedule.next_reconcile_at, operation.deployment_id
                LIMIT ?
                """,
                (timestamp, timestamp, limit),
            ).fetchall()
        return [str(row[0]) for row in rows]

    def shadow_candidates(
        self,
        adapter: str,
        *,
        limit: int = 100,
    ) -> list[dict[str, Any]]:
        """Read a bounded deployment sample without leasing or mutating the ledger."""
        adapter_name = _validate_identifier(adapter, "shadow adapter")
        if isinstance(limit, bool) or not isinstance(limit, int) or not 1 <= limit <= 1000:
            raise DeploymentControllerConfigError(
                "shadow candidate limit must be between 1 and 1000"
            )
        with closing(self._connect_read_only()) as connection:
            rows = connection.execute(
                """
                SELECT deployment_id, idempotency_key, status,
                       external_operation_id, updated_at
                FROM deployment_operations
                WHERE adapter = ?
                  AND status IN ('accepted', 'completed', 'indeterminate')
                ORDER BY updated_at DESC, deployment_id
                LIMIT ?
                """,
                (adapter_name, limit),
            ).fetchall()
        return [dict(row) for row in rows]

    def reconciliation_health(self, now: datetime) -> dict[str, Any]:
        current_time = _normalize_now(now)
        timestamp = _format_timestamp(current_time)
        with closing(self._connect()) as connection, connection:
            counts = connection.execute(
                """
                SELECT
                    SUM(CASE WHEN operation.status = 'accepted' THEN 1 ELSE 0 END),
                    SUM(CASE WHEN operation.status = 'accepted'
                              AND schedule.next_reconcile_at <= ?
                              AND (operation.lease_token IS NULL
                                   OR operation.lease_expires_at <= ?)
                             THEN 1 ELSE 0 END),
                    SUM(CASE WHEN operation.status = 'accepted'
                              AND schedule.deadline_at <= ?
                             THEN 1 ELSE 0 END),
                    SUM(CASE WHEN operation.status = 'accepted'
                              AND schedule.deployment_id IS NULL
                             THEN 1 ELSE 0 END),
                    SUM(CASE WHEN operation.status = 'indeterminate' THEN 1 ELSE 0 END),
                    MIN(CASE WHEN operation.status = 'accepted'
                                  AND schedule.accepted_at IS NOT NULL
                             THEN schedule.accepted_at END)
                FROM deployment_operations AS operation
                LEFT JOIN deployment_reconciliation_schedule AS schedule
                    ON schedule.deployment_id = operation.deployment_id
                """,
                (timestamp, timestamp, timestamp),
            ).fetchone()
        oldest_accepted_age_seconds: float | None = None
        if counts is not None and counts[5] is not None:
            accepted_at = _parse_timestamp(str(counts[5]), "ledger accepted_at")
            oldest_accepted_age_seconds = max(
                0.0,
                (current_time - accepted_at).total_seconds(),
            )
        return {
            "schema_version": RECONCILIATION_HEALTH_SCHEMA_VERSION,
            "generated_at": timestamp,
            "accepted_pending": int(counts[0] or 0) if counts is not None else 0,
            "due": int(counts[1] or 0) if counts is not None else 0,
            "overdue_deadline": int(counts[2] or 0) if counts is not None else 0,
            "schedule_missing": int(counts[3] or 0) if counts is not None else 0,
            "indeterminate": int(counts[4] or 0) if counts is not None else 0,
            "oldest_accepted_age_seconds": oldest_accepted_age_seconds,
        }

    def resolution(self, deployment_id: str) -> dict[str, Any] | None:
        deployment_id = _validate_identifier(deployment_id, "deployment_id")
        with closing(self._connect()) as connection, connection:
            row = connection.execute(
                """
                SELECT * FROM deployment_resolutions WHERE deployment_id = ?
                """,
                (deployment_id,),
            ).fetchone()
        return dict(row) if row is not None else None

    def apply_manual_resolution(
        self,
        resolution: ManualResolution,
        now: datetime,
    ) -> tuple[dict[str, Any], bool]:
        _validate_manual_resolution(resolution)
        current_time = _normalize_now(now)
        timestamp = _format_timestamp(current_time)
        with closing(self._connect()) as connection, connection:
            connection.execute("BEGIN IMMEDIATE")
            existing_row = connection.execute(
                """
                SELECT * FROM deployment_resolutions WHERE resolution_id = ?
                """,
                (resolution.resolution_id,),
            ).fetchone()
            if existing_row is not None:
                existing = dict(existing_row)
                self._require_same_resolution(existing, resolution)
                operation = self._select_required(connection, resolution.deployment_id)
                if operation["status"] != resolution.proposed_status:
                    raise DeploymentControllerError(
                        "consumed resolution does not match deployment status"
                    )
                connection.commit()
                return operation, False
            conflicting_row = connection.execute(
                """
                SELECT resolution_id FROM deployment_resolutions
                WHERE deployment_id = ?
                """,
                (resolution.deployment_id,),
            ).fetchone()
            if conflicting_row is not None:
                raise DeploymentControllerError(
                    "deployment already consumed a different manual resolution"
                )
            operation = self._select_required(connection, resolution.deployment_id)
            if operation["status"] != "indeterminate":
                raise DeploymentControllerError(
                    "only an indeterminate deployment can be manually resolved"
                )
            expected_bindings = {
                "adapter": resolution.adapter,
                "external_operation_id": resolution.external_operation_id,
                "updated_at": resolution.expected_updated_at,
                "error_code": resolution.expected_error_code,
            }
            if any(operation.get(field) != value for field, value in expected_bindings.items()):
                raise DeploymentBusyError(
                    "deployment changed after the manual resolution request was signed"
                )
            if resolution.proposed_status == "completed" and (
                resolution.external_operation_id is None
            ):
                raise DeploymentControllerError(
                    "completed manual resolution requires an external operation identifier"
                )
            connection.execute(
                """
                INSERT INTO deployment_resolutions (
                    resolution_id, deployment_id, proposed_status, adapter,
                    external_operation_id, reason_code, expected_updated_at,
                    previous_error_code, evidence_sha256,
                    provider_evidence_sha256, request_sha256, approval_sha256,
                    requested_by, approved_by, requester_key_id, approver_key_id,
                    resolved_at
                ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                """,
                (
                    resolution.resolution_id,
                    resolution.deployment_id,
                    resolution.proposed_status,
                    resolution.adapter,
                    resolution.external_operation_id,
                    resolution.reason_code,
                    resolution.expected_updated_at,
                    resolution.expected_error_code,
                    resolution.evidence_sha256,
                    resolution.provider_evidence_sha256,
                    resolution.request_sha256,
                    resolution.approval_sha256,
                    resolution.requested_by,
                    resolution.approved_by,
                    resolution.requester_key_id,
                    resolution.approver_key_id,
                    timestamp,
                ),
            )
            updated = connection.execute(
                """
                UPDATE deployment_operations
                SET status = ?, error_code = ?, lease_token = NULL,
                    lease_expires_at = NULL, updated_at = ?
                WHERE deployment_id = ? AND status = 'indeterminate'
                  AND updated_at = ?
                """,
                (
                    resolution.proposed_status,
                    None
                    if resolution.proposed_status == "completed"
                    else "manual_resolution_failed",
                    timestamp,
                    resolution.deployment_id,
                    resolution.expected_updated_at,
                ),
            )
            if updated.rowcount != 1:
                raise DeploymentBusyError(
                    "deployment manual resolution status fence was lost"
                )
            self._append_event(
                connection,
                resolution.deployment_id,
                f"manual_resolution_{resolution.proposed_status}",
                resolution.proposed_status,
                max(
                    int(operation["attempt_count"]),
                    int(operation["reconciliation_count"]),
                ),
                timestamp,
            )
            completed = self._select_required(connection, resolution.deployment_id)
            connection.commit()
        self._validate_files()
        return completed, True

    def _finish_with_lease(
        self,
        deployment_id: str,
        token: str,
        status_value: str,
        error_code: str | None,
        external_operation_id: str | None,
        now: datetime,
        reconciliation_policy: ReconciliationPolicy | None = None,
    ) -> dict[str, Any]:
        timestamp = _format_timestamp(now)
        with closing(self._connect()) as connection, connection:
            connection.execute("BEGIN IMMEDIATE")
            operation = self._select_required(connection, deployment_id)
            if operation["status"] != "dispatching" or operation["lease_token"] != token:
                raise DeploymentBusyError("deployment dispatch lease fence was lost")
            updated = connection.execute(
                """
                UPDATE deployment_operations
                SET status = ?, lease_token = NULL, lease_expires_at = NULL,
                    external_operation_id = ?, error_code = ?, updated_at = ?
                WHERE deployment_id = ? AND lease_token = ?
                """,
                (
                    status_value,
                    external_operation_id,
                    error_code,
                    timestamp,
                    deployment_id,
                    token,
                ),
            )
            if updated.rowcount != 1:
                raise DeploymentBusyError("deployment dispatch lease fence was lost")
            if status_value == "accepted":
                if reconciliation_policy is None:
                    raise DeploymentControllerConfigError(
                        "accepted deployment requires a reconciliation policy"
                    )
                self._insert_schedule(
                    connection,
                    deployment_id,
                    now,
                    reconciliation_policy,
                )
            self._append_event(
                connection,
                deployment_id,
                status_value,
                status_value,
                int(operation["attempt_count"]),
                timestamp,
            )
            result = self._select_required(connection, deployment_id)
            connection.commit()
        self._validate_files()
        return result

    def _finish_without_lease(
        self,
        connection: sqlite3.Connection,
        operation: Mapping[str, Any],
        status_value: str,
        error_code: str,
        timestamp: str,
        *,
        event_type: str | None = None,
        event_attempt: int | None = None,
    ) -> None:
        connection.execute(
            """
            UPDATE deployment_operations
            SET status = ?, lease_token = NULL, lease_expires_at = NULL,
                error_code = ?, updated_at = ?
            WHERE deployment_id = ?
            """,
            (status_value, error_code, timestamp, operation["deployment_id"]),
        )
        self._append_event(
            connection,
            str(operation["deployment_id"]),
            event_type or status_value,
            status_value,
            int(operation["attempt_count"]) if event_attempt is None else event_attempt,
            timestamp,
        )

    def _finish_reconciliation_locked(
        self,
        connection: sqlite3.Connection,
        operation: Mapping[str, Any],
        token: str,
        status_value: str,
        error_code: str | None,
        event_type: str,
        timestamp: str,
    ) -> None:
        updated = connection.execute(
            """
            UPDATE deployment_operations
            SET status = ?, lease_token = NULL, lease_expires_at = NULL,
                error_code = ?, updated_at = ?
            WHERE deployment_id = ? AND lease_token = ?
            """,
            (
                status_value,
                error_code,
                timestamp,
                operation["deployment_id"],
                token,
            ),
        )
        if updated.rowcount != 1:
            raise DeploymentBusyError("deployment reconciliation lease fence was lost")
        self._append_event(
            connection,
            str(operation["deployment_id"]),
            event_type,
            status_value,
            int(operation["reconciliation_count"]),
            timestamp,
        )

    def _finish_reconciliation_pending_locked(
        self,
        connection: sqlite3.Connection,
        operation: Mapping[str, Any],
        token: str,
        error_code: str | None,
        event_type: str,
        now: datetime,
    ) -> None:
        timestamp = _format_timestamp(now)
        schedule = self._select_schedule_required(
            connection,
            str(operation["deployment_id"]),
        )
        deadline_at = _parse_timestamp(
            str(schedule["deadline_at"]),
            "ledger deadline_at",
        )
        reconciliation_count = int(operation["reconciliation_count"])
        if now >= deadline_at:
            self._finish_reconciliation_locked(
                connection,
                operation,
                token,
                "indeterminate",
                "reconciliation_deadline_exhausted",
                "reconciliation_deadline_exhausted",
                timestamp,
            )
            return
        if reconciliation_count >= int(schedule["max_attempts"]):
            self._finish_reconciliation_locked(
                connection,
                operation,
                token,
                "indeterminate",
                "reconciliation_budget_exhausted",
                "reconciliation_budget_exhausted",
                timestamp,
            )
            return
        self._finish_reconciliation_locked(
            connection,
            operation,
            token,
            "accepted",
            error_code,
            event_type,
            timestamp,
        )
        base_delay = int(schedule["base_delay_seconds"])
        max_delay = int(schedule["max_delay_seconds"])
        delay = min(base_delay * (1 << min(reconciliation_count, 30)), max_delay)
        connection.execute(
            """
            UPDATE deployment_reconciliation_schedule
            SET next_reconcile_at = ?, last_delay_seconds = ?
            WHERE deployment_id = ?
            """,
            (
                _format_timestamp(now + timedelta(seconds=delay)),
                delay,
                operation["deployment_id"],
            ),
        )

    def _initialize(self) -> None:
        with closing(self._connect()) as connection, connection:
            connection.execute("BEGIN IMMEDIATE")
            version_row = connection.execute("PRAGMA user_version").fetchone()
            version = int(version_row[0]) if version_row is not None else 0
            table_rows = connection.execute(
                """
                SELECT name FROM sqlite_master
                WHERE type = 'table' AND name NOT LIKE 'sqlite_%'
                """
            ).fetchall()
            tables = {str(row[0]) for row in table_rows}
            if version == LEDGER_SCHEMA_VERSION:
                if tables != {
                    "deployment_operations",
                    "deployment_events",
                    "deployment_reconciliation_schedule",
                    "deployment_resolutions",
                }:
                    raise DeploymentControllerError("deployment ledger schema is invalid")
            elif version in {1, 2, 3}:
                expected_tables = {"deployment_operations", "deployment_events"}
                if version == 3:
                    expected_tables.add("deployment_reconciliation_schedule")
                if tables != expected_tables:
                    raise DeploymentControllerError(
                        f"deployment ledger v{version} schema is invalid"
                    )
                operation_columns = {
                    str(row[1])
                    for row in connection.execute(
                        "PRAGMA table_info(deployment_operations)"
                    ).fetchall()
                }
                event_columns = {
                    str(row[1])
                    for row in connection.execute(
                        "PRAGMA table_info(deployment_events)"
                    ).fetchall()
                }
                expected_operation_columns = _OPERATION_COLUMNS
                if version == 1:
                    expected_operation_columns = _OPERATION_COLUMNS - {
                        "reconciliation_count"
                    }
                if (
                    operation_columns != expected_operation_columns
                    or event_columns != _EVENT_COLUMNS
                ):
                    raise DeploymentControllerError(
                        f"deployment ledger v{version} columns are invalid"
                    )
                if version == 1:
                    connection.execute(
                        """
                        ALTER TABLE deployment_operations
                        ADD COLUMN reconciliation_count INTEGER NOT NULL DEFAULT 0
                        CHECK (reconciliation_count >= 0)
                        """
                    )
                if version in {1, 2}:
                    self._create_schedule_table(connection)
                    self._backfill_reconciliation_schedules(connection)
                else:
                    schedule_columns = {
                        str(row[1])
                        for row in connection.execute(
                            "PRAGMA table_info(deployment_reconciliation_schedule)"
                        ).fetchall()
                    }
                    if schedule_columns != _RECONCILIATION_SCHEDULE_COLUMNS:
                        raise DeploymentControllerError(
                            "deployment ledger v3 columns are invalid"
                        )
                self._create_resolution_table(connection)
                connection.execute(f"PRAGMA user_version = {LEDGER_SCHEMA_VERSION}")
            elif version != 0 or tables:
                raise DeploymentControllerError(
                    "existing database is not an empty deployment ledger"
                )
            else:
                connection.execute(
                    """
                    CREATE TABLE deployment_operations (
                        deployment_id TEXT PRIMARY KEY,
                        authorization_id TEXT NOT NULL UNIQUE,
                        idempotency_key TEXT NOT NULL UNIQUE,
                        run_id TEXT NOT NULL,
                        target TEXT NOT NULL,
                        adapter TEXT NOT NULL,
                        authorization_sha256 TEXT NOT NULL,
                        gate_report_sha256 TEXT NOT NULL,
                        request_sha256 TEXT NOT NULL,
                        status TEXT NOT NULL CHECK (status IN (
                            'prepared', 'dispatching', 'accepted', 'completed',
                            'simulated', 'retryable_failed', 'failed', 'indeterminate'
                        )),
                        attempt_count INTEGER NOT NULL CHECK (attempt_count >= 0),
                        reconciliation_count INTEGER NOT NULL DEFAULT 0
                            CHECK (reconciliation_count >= 0),
                        lease_token TEXT,
                        lease_expires_at TEXT,
                        external_operation_id TEXT,
                        error_code TEXT,
                        created_at TEXT NOT NULL,
                        updated_at TEXT NOT NULL
                    )
                    """
                )
                connection.execute(
                    """
                    CREATE TABLE deployment_events (
                        id INTEGER PRIMARY KEY AUTOINCREMENT,
                        deployment_id TEXT NOT NULL
                            REFERENCES deployment_operations(deployment_id),
                        sequence INTEGER NOT NULL,
                        event_type TEXT NOT NULL,
                        status TEXT NOT NULL,
                        attempt INTEGER NOT NULL,
                        occurred_at TEXT NOT NULL,
                        UNIQUE (deployment_id, sequence)
                    )
                    """
                )
                connection.execute(
                    """
                    CREATE INDEX deployment_events_time_idx
                    ON deployment_events (deployment_id, occurred_at)
                    """
                )
                self._create_schedule_table(connection)
                self._create_resolution_table(connection)
                connection.execute(f"PRAGMA user_version = {LEDGER_SCHEMA_VERSION}")
            connection.commit()
        self._validate_schema()
        self._validate_files()

    def _validate_schema(self) -> None:
        with closing(self._connect()) as connection, connection:
            version_row = connection.execute("PRAGMA user_version").fetchone()
            table_rows = connection.execute(
                """
                SELECT name FROM sqlite_master
                WHERE type = 'table' AND name NOT LIKE 'sqlite_%'
                """
            ).fetchall()
        version = int(version_row[0]) if version_row is not None else 0
        tables = {str(row[0]) for row in table_rows}
        if version != LEDGER_SCHEMA_VERSION or tables != {
            "deployment_operations",
            "deployment_events",
            "deployment_reconciliation_schedule",
            "deployment_resolutions",
        }:
            raise DeploymentControllerError("deployment ledger schema is invalid")
        with closing(self._connect()) as connection, connection:
            operation_columns = {
                str(row[1])
                for row in connection.execute(
                    "PRAGMA table_info(deployment_operations)"
                ).fetchall()
            }
            event_columns = {
                str(row[1])
                for row in connection.execute(
                    "PRAGMA table_info(deployment_events)"
                ).fetchall()
            }
            schedule_columns = {
                str(row[1])
                for row in connection.execute(
                    "PRAGMA table_info(deployment_reconciliation_schedule)"
                ).fetchall()
            }
            resolution_columns = {
                str(row[1])
                for row in connection.execute(
                    "PRAGMA table_info(deployment_resolutions)"
                ).fetchall()
            }
        if (
            operation_columns != _OPERATION_COLUMNS
            or event_columns != _EVENT_COLUMNS
            or schedule_columns != _RECONCILIATION_SCHEDULE_COLUMNS
            or resolution_columns != _RESOLUTION_COLUMNS
        ):
            raise DeploymentControllerError("deployment ledger columns are invalid")

    def _create_schedule_table(self, connection: sqlite3.Connection) -> None:
        connection.execute(
            """
            CREATE TABLE deployment_reconciliation_schedule (
                deployment_id TEXT PRIMARY KEY
                    REFERENCES deployment_operations(deployment_id) ON DELETE CASCADE,
                accepted_at TEXT NOT NULL,
                next_reconcile_at TEXT NOT NULL,
                deadline_at TEXT NOT NULL,
                base_delay_seconds INTEGER NOT NULL
                    CHECK (base_delay_seconds > 0),
                max_delay_seconds INTEGER NOT NULL
                    CHECK (max_delay_seconds >= base_delay_seconds),
                max_attempts INTEGER NOT NULL CHECK (max_attempts > 0),
                last_delay_seconds INTEGER NOT NULL
                    CHECK (last_delay_seconds >= 0)
            )
            """
        )
        connection.execute(
            """
            CREATE INDEX deployment_reconciliation_due_idx
            ON deployment_reconciliation_schedule (next_reconcile_at, deployment_id)
            """
        )

    def _create_resolution_table(self, connection: sqlite3.Connection) -> None:
        connection.execute(
            """
            CREATE TABLE deployment_resolutions (
                resolution_id TEXT PRIMARY KEY,
                deployment_id TEXT NOT NULL UNIQUE
                    REFERENCES deployment_operations(deployment_id),
                proposed_status TEXT NOT NULL
                    CHECK (proposed_status IN ('completed', 'failed')),
                adapter TEXT NOT NULL,
                external_operation_id TEXT,
                reason_code TEXT NOT NULL,
                expected_updated_at TEXT NOT NULL,
                previous_error_code TEXT,
                evidence_sha256 TEXT NOT NULL,
                provider_evidence_sha256 TEXT NOT NULL,
                request_sha256 TEXT NOT NULL UNIQUE,
                approval_sha256 TEXT NOT NULL UNIQUE,
                requested_by TEXT NOT NULL,
                approved_by TEXT NOT NULL,
                requester_key_id TEXT NOT NULL,
                approver_key_id TEXT NOT NULL,
                resolved_at TEXT NOT NULL
            )
            """
        )

    def _backfill_reconciliation_schedules(
        self,
        connection: sqlite3.Connection,
    ) -> None:
        policy = DEFAULT_RECONCILIATION_POLICY
        rows = connection.execute(
            """
            SELECT deployment_id, updated_at
            FROM deployment_operations
            WHERE status = 'accepted'
            """
        ).fetchall()
        for row in rows:
            accepted_at = _parse_timestamp(
                str(row["updated_at"]),
                "ledger accepted updated_at",
            )
            connection.execute(
                """
                INSERT INTO deployment_reconciliation_schedule (
                    deployment_id, accepted_at, next_reconcile_at, deadline_at,
                    base_delay_seconds, max_delay_seconds, max_attempts,
                    last_delay_seconds
                ) VALUES (?, ?, ?, ?, ?, ?, ?, 0)
                """,
                (
                    row["deployment_id"],
                    _format_timestamp(accepted_at),
                    _format_timestamp(accepted_at),
                    _format_timestamp(
                        accepted_at + timedelta(seconds=policy.deadline_seconds)
                    ),
                    policy.base_delay_seconds,
                    policy.max_delay_seconds,
                    policy.max_attempts,
                ),
            )

    def _connect(self) -> sqlite3.Connection:
        _validate_private_database(self.path)
        connection = sqlite3.connect(str(self.path), timeout=5.0, isolation_level=None)
        connection.row_factory = sqlite3.Row
        connection.execute("PRAGMA foreign_keys = ON")
        connection.execute("PRAGMA busy_timeout = 5000")
        connection.execute("PRAGMA synchronous = FULL")
        return connection

    def _connect_read_only(self) -> sqlite3.Connection:
        _validate_private_database(self.path)
        uri = f"{self.path.resolve().as_uri()}?mode=ro"
        connection = sqlite3.connect(
            uri,
            uri=True,
            timeout=5.0,
            isolation_level=None,
        )
        connection.row_factory = sqlite3.Row
        connection.execute("PRAGMA query_only = ON")
        connection.execute("PRAGMA foreign_keys = ON")
        connection.execute("PRAGMA busy_timeout = 5000")
        return connection

    def _select_operation(
        self,
        connection: sqlite3.Connection,
        deployment_id: str,
    ) -> dict[str, Any] | None:
        row = connection.execute(
            "SELECT * FROM deployment_operations WHERE deployment_id = ?",
            (deployment_id,),
        ).fetchone()
        return dict(row) if row is not None else None

    def _select_required(
        self,
        connection: sqlite3.Connection,
        deployment_id: str,
    ) -> dict[str, Any]:
        operation = self._select_operation(connection, deployment_id)
        if operation is None:
            raise DeploymentControllerError("deployment operation does not exist")
        return operation

    def _select_schedule(
        self,
        connection: sqlite3.Connection,
        deployment_id: str,
    ) -> dict[str, Any] | None:
        row = connection.execute(
            """
            SELECT * FROM deployment_reconciliation_schedule
            WHERE deployment_id = ?
            """,
            (deployment_id,),
        ).fetchone()
        return dict(row) if row is not None else None

    def _select_schedule_required(
        self,
        connection: sqlite3.Connection,
        deployment_id: str,
    ) -> dict[str, Any]:
        schedule = self._select_schedule(connection, deployment_id)
        if schedule is None:
            raise DeploymentControllerError(
                "deployment reconciliation schedule does not exist"
            )
        return schedule

    def _insert_schedule(
        self,
        connection: sqlite3.Connection,
        deployment_id: str,
        accepted_at: datetime,
        policy: ReconciliationPolicy,
    ) -> None:
        _validate_reconciliation_policy(policy)
        connection.execute(
            """
            INSERT INTO deployment_reconciliation_schedule (
                deployment_id, accepted_at, next_reconcile_at, deadline_at,
                base_delay_seconds, max_delay_seconds, max_attempts,
                last_delay_seconds
            ) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
            """,
            (
                deployment_id,
                _format_timestamp(accepted_at),
                _format_timestamp(
                    accepted_at + timedelta(seconds=policy.base_delay_seconds)
                ),
                _format_timestamp(
                    accepted_at + timedelta(seconds=policy.deadline_seconds)
                ),
                policy.base_delay_seconds,
                policy.max_delay_seconds,
                policy.max_attempts,
                policy.base_delay_seconds,
            ),
        )

    def _require_same_request(
        self,
        operation: Mapping[str, Any],
        request: DeploymentRequest,
    ) -> None:
        expected = asdict(request)
        expected.pop("schema_version")
        for field, value in expected.items():
            if operation.get(field) != value:
                raise DeploymentControllerError(
                    "deployment_id is already bound to a different request"
                )

    def _require_same_resolution(
        self,
        existing: Mapping[str, Any],
        resolution: ManualResolution,
    ) -> None:
        expected = asdict(resolution)
        expected["previous_error_code"] = expected.pop("expected_error_code")
        for field, value in expected.items():
            if existing.get(field) != value:
                raise DeploymentControllerError(
                    "resolution_id is already bound to different signed evidence"
                )

    def _append_event(
        self,
        connection: sqlite3.Connection,
        deployment_id: str,
        event_type: str,
        status_value: str,
        attempt: int,
        occurred_at: str,
    ) -> None:
        row = connection.execute(
            "SELECT COALESCE(MAX(sequence), 0) + 1 FROM deployment_events WHERE deployment_id = ?",
            (deployment_id,),
        ).fetchone()
        sequence = int(row[0]) if row is not None else 1
        connection.execute(
            """
            INSERT INTO deployment_events (
                deployment_id, sequence, event_type, status, attempt, occurred_at
            ) VALUES (?, ?, ?, ?, ?, ?)
            """,
            (deployment_id, sequence, event_type, status_value, attempt, occurred_at),
        )

    def _validate_files(self) -> None:
        _validate_private_database(self.path)


def plan_deployment(
    gate_dir: Path,
    authorization_path: Path,
    key: bytes,
    key_id: str,
    deployment_id: str,
    target: str,
    adapter_name: str,
    *,
    max_age_seconds: int = 900,
    now: datetime | None = None,
) -> DeploymentRequest:
    current_time = _normalize_now(now)
    deployment_id = _validate_identifier(deployment_id, "deployment_id")
    target = _validate_target(target)
    adapter_name = _validate_identifier(adapter_name, "adapter")
    authorization = verify_deployment_authorization(
        gate_dir,
        authorization_path,
        key,
        key_id,
        deployment_id,
        max_age_seconds=max_age_seconds,
        now=current_time,
    )
    authorization_digest = _digest(_canonical_json(authorization))
    payload = {
        "schema_version": REQUEST_SCHEMA_VERSION,
        "deployment_id": deployment_id,
        "authorization_id": authorization["authorization_id"],
        "idempotency_key": authorization["idempotency_key"],
        "run_id": authorization["run_id"],
        "target": target,
        "adapter": adapter_name,
        "authorization_sha256": authorization_digest,
        "gate_report_sha256": authorization["gate_report_sha256"],
    }
    request_digest = _digest(_canonical_json(payload))
    request = DeploymentRequest(**payload, request_sha256=request_digest)
    _validate_request(request)
    return request


def prepare_deployment(
    ledger: DeploymentLedger,
    gate_dir: Path,
    authorization_path: Path,
    key: bytes,
    key_id: str,
    deployment_id: str,
    target: str,
    adapter_name: str,
    *,
    max_age_seconds: int = 900,
    now: datetime | None = None,
) -> tuple[dict[str, Any], bool]:
    current_time = _normalize_now(now)
    request = plan_deployment(
        gate_dir,
        authorization_path,
        key,
        key_id,
        deployment_id,
        target,
        adapter_name,
        max_age_seconds=max_age_seconds,
        now=current_time,
    )
    return ledger.prepare(request, current_time)


def dispatch_deployment(
    ledger: DeploymentLedger,
    gate_dir: Path,
    authorization_path: Path,
    key: bytes,
    key_id: str,
    deployment_id: str,
    target: str,
    adapter: DeploymentAdapter,
    *,
    max_age_seconds: int = 900,
    lease_seconds: int = 30,
    reconciliation_policy: ReconciliationPolicy = DEFAULT_RECONCILIATION_POLICY,
    now: datetime | None = None,
) -> dict[str, Any]:
    current_time = _normalize_now(now)
    _validate_reconciliation_policy(reconciliation_policy)
    prepare_deployment(
        ledger,
        gate_dir,
        authorization_path,
        key,
        key_id,
        deployment_id,
        target,
        adapter.name,
        max_age_seconds=max_age_seconds,
        now=current_time,
    )
    acquired = ledger.acquire(
        deployment_id,
        adapter,
        current_time,
        lease_seconds,
    )
    if isinstance(acquired, dict):
        return acquired

    try:
        result: AdapterResult | None = None
        if acquired.recovering and adapter.supports_lookup:
            result = adapter.lookup(acquired.request.idempotency_key)
        if result is None and not (
            acquired.recovering or adapter.supports_idempotent_submit
        ):
            result = adapter.submit(acquired.request)
        elif result is None and acquired.recovering and not adapter.supports_idempotent_submit:
            error = DeploymentAdapterError(
                "lookup_miss_without_idempotency",
                indeterminate=True,
            )
            ledger.finish_error(deployment_id, acquired.token, error, current_time)
            raise DeploymentIndeterminateError(
                "adapter lookup missed and submit is not idempotent"
            )
        elif result is None:
            result = adapter.submit(acquired.request)
    except DeploymentAdapterError as exc:
        ledger.finish_error(deployment_id, acquired.token, exc, current_time)
        raise DeploymentControllerError(f"deployment adapter failed: {exc.code}") from exc
    except DeploymentIndeterminateError:
        raise
    except Exception as exc:
        error = DeploymentAdapterError("adapter_exception", indeterminate=True)
        ledger.finish_error(deployment_id, acquired.token, error, current_time)
        raise DeploymentIndeterminateError(
            "deployment adapter raised an unclassified exception"
        ) from exc
    try:
        _validate_adapter_result(result)
    except DeploymentControllerError as exc:
        error = DeploymentAdapterError("adapter_contract", indeterminate=True)
        ledger.finish_error(deployment_id, acquired.token, error, current_time)
        raise DeploymentIndeterminateError("deployment adapter result is invalid") from exc
    return ledger.finish_result(
        deployment_id,
        acquired.token,
        result,
        current_time,
        reconciliation_policy,
    )


def reconcile_deployment(
    ledger: DeploymentLedger,
    deployment_id: str,
    adapter: DeploymentAdapter,
    *,
    lease_seconds: int = 30,
    now: datetime | None = None,
) -> dict[str, Any]:
    current_time = _normalize_now(now)
    acquired = ledger.acquire_reconciliation(
        deployment_id,
        adapter,
        current_time,
        lease_seconds,
    )
    if isinstance(acquired, dict):
        return acquired
    try:
        result = adapter.lookup(acquired.request.idempotency_key)
    except DeploymentAdapterError as exc:
        ledger.finish_reconciliation_error(
            deployment_id,
            acquired.token,
            exc,
            current_time,
        )
        raise DeploymentControllerError(
            f"deployment reconciliation failed: {exc.code}"
        ) from exc
    except Exception as exc:
        error = DeploymentAdapterError("lookup_exception", indeterminate=True)
        ledger.finish_reconciliation_error(
            deployment_id,
            acquired.token,
            error,
            current_time,
        )
        raise DeploymentIndeterminateError(
            "deployment lookup raised an unclassified exception"
        ) from exc
    if result is None:
        error = DeploymentAdapterError("lookup_miss_after_accept", indeterminate=True)
        ledger.finish_reconciliation_error(
            deployment_id,
            acquired.token,
            error,
            current_time,
        )
        raise DeploymentIndeterminateError(
            "accepted deployment was not found by idempotency key"
        )
    try:
        _validate_adapter_result(result)
        if result.status not in {"accepted", "completed"}:
            raise DeploymentControllerError(
                "reconciliation result must be accepted or completed"
            )
    except DeploymentControllerError as exc:
        error = DeploymentAdapterError("lookup_contract", indeterminate=True)
        ledger.finish_reconciliation_error(
            deployment_id,
            acquired.token,
            error,
            current_time,
        )
        raise DeploymentIndeterminateError(
            "deployment lookup result is invalid"
        ) from exc
    return ledger.finish_reconciliation_result(
        deployment_id,
        acquired.token,
        result,
        current_time,
    )


def run_reconciliation_batch(
    ledger: DeploymentLedger,
    adapters: Mapping[str, DeploymentAdapter],
    *,
    limit: int = 100,
    lease_seconds: int = 30,
    now: datetime | None = None,
) -> dict[str, int]:
    current_time = _normalize_now(now)
    for adapter_name, adapter in adapters.items():
        _validate_identifier(adapter_name, "adapter registry key")
        _validate_adapter(adapter)
        if adapter.name != adapter_name:
            raise DeploymentControllerConfigError(
                "adapter registry key does not match adapter name"
            )
    deployment_ids = ledger.due_reconciliations(current_time, limit=limit)
    summary = {
        "selected": len(deployment_ids),
        "completed": 0,
        "pending": 0,
        "indeterminate": 0,
        "busy": 0,
        "adapter_unavailable": 0,
        "retryable_errors": 0,
    }
    for deployment_id in deployment_ids:
        operation = ledger.get(deployment_id)
        selected_adapter = adapters.get(str(operation["adapter"]))
        if selected_adapter is None:
            summary["adapter_unavailable"] += 1
            continue
        try:
            result = reconcile_deployment(
                ledger,
                deployment_id,
                selected_adapter,
                lease_seconds=lease_seconds,
                now=current_time,
            )
        except DeploymentNotDueError:
            summary["busy"] += 1
        except DeploymentBusyError:
            summary["busy"] += 1
        except DeploymentIndeterminateError:
            summary["indeterminate"] += 1
        except DeploymentControllerError:
            latest = ledger.get(deployment_id)
            if latest["status"] == "accepted":
                summary["retryable_errors"] += 1
            elif latest["status"] == "indeterminate":
                summary["indeterminate"] += 1
            else:
                raise
        else:
            if result["status"] == "completed":
                summary["completed"] += 1
            elif result["status"] == "accepted":
                summary["pending"] += 1
            elif result["status"] == "indeterminate":
                summary["indeterminate"] += 1
            else:
                raise DeploymentControllerError(
                    "reconciliation batch produced an unexpected status"
                )
    return summary


def certify_adapter(
    adapter: DeploymentAdapter,
    request: DeploymentRequest,
) -> dict[str, Any]:
    """Exercise an adapter only against an explicitly isolated certification target."""
    _validate_adapter(adapter)
    _validate_request(request)
    if request.adapter != adapter.name:
        raise AdapterCertificationError(
            "certification request adapter does not match adapter name"
        )
    if not adapter.supports_idempotent_submit or not adapter.supports_lookup:
        raise AdapterCertificationError(
            "production adapter certification requires idempotent submit and lookup"
        )
    try:
        first = adapter.submit(request)
        second = adapter.submit(request)
        lookup = adapter.lookup(request.idempotency_key)
    except DeploymentAdapterError as exc:
        raise AdapterCertificationError(
            f"adapter certification failed: {exc.code}"
        ) from exc
    except Exception as exc:
        raise AdapterCertificationError(
            "adapter certification raised an unclassified exception"
        ) from exc
    if lookup is None:
        raise AdapterCertificationError("adapter certification lookup returned no operation")
    for result in (first, second, lookup):
        _validate_adapter_result(result)
        if result.status not in {"accepted", "completed"}:
            raise AdapterCertificationError(
                "adapter certification must produce an external operation"
            )
    operation_ids = {
        first.external_operation_id,
        second.external_operation_id,
        lookup.external_operation_id,
    }
    if len(operation_ids) != 1 or None in operation_ids:
        raise AdapterCertificationError(
            "adapter did not preserve one external operation for the idempotency key"
        )
    external_id = str(first.external_operation_id)
    return {
        "schema_version": ADAPTER_CERTIFICATION_SCHEMA_VERSION,
        "adapter": adapter.name,
        "passed": True,
        "idempotent_submit": True,
        "lookup": True,
        "request_sha256": request.request_sha256,
        "external_operation_sha256": _digest(external_id.encode("utf-8")),
        "submit_statuses": [first.status, second.status],
        "lookup_status": lookup.status,
    }


def main(argv: Sequence[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        description="Plan, persist, or simulate an authorized deployment without cloud writes"
    )
    subparsers = parser.add_subparsers(dest="command", required=True)
    for command in ("plan", "prepare", "simulate", "status", "health", "migrate"):
        command_parser = subparsers.add_parser(command)
        if command not in {"health", "migrate"}:
            command_parser.add_argument("--deployment-id", required=True)
        if command not in {"status", "health", "migrate"}:
            command_parser.add_argument("--gate-dir", type=Path, required=True)
            command_parser.add_argument("--authorization", type=Path, required=True)
            command_parser.add_argument("--target", required=True)
            command_parser.add_argument("--max-age-seconds", type=int, default=900)
        if command in {"prepare", "simulate", "status", "health", "migrate"}:
            command_parser.add_argument("--ledger", type=Path, required=True)
        if command == "simulate":
            command_parser.add_argument("--lease-seconds", type=int, default=30)
    args = parser.parse_args(argv)

    try:
        if args.command == "migrate":
            DeploymentLedger(args.ledger)
            output = {
                "ledger_schema_version": LEDGER_SCHEMA_VERSION,
                "status": "ready",
            }
        elif args.command == "status":
            ledger = DeploymentLedger(args.ledger, create=False)
            operation = ledger.get(args.deployment_id)
            output = _public_operation(operation, len(ledger.events(args.deployment_id)))
            output["reconciliation_schedule"] = ledger.schedule(args.deployment_id)
            output["manual_resolution"] = ledger.resolution(args.deployment_id)
        elif args.command == "health":
            ledger = DeploymentLedger(args.ledger, create=False)
            output = ledger.reconciliation_health(datetime.now(UTC))
        else:
            key, key_id = _key_from_environment()
            if args.command == "plan":
                request = plan_deployment(
                    args.gate_dir,
                    args.authorization,
                    key,
                    key_id,
                    args.deployment_id,
                    args.target,
                    DryRunAdapter.name,
                    max_age_seconds=args.max_age_seconds,
                )
                output = {
                    "adapter": request.adapter,
                    "deployment_id": request.deployment_id,
                    "idempotency_key": request.idempotency_key,
                    "request_sha256": request.request_sha256,
                    "status": "planned",
                    "target": request.target,
                }
            else:
                ledger = DeploymentLedger(args.ledger)
                if args.command == "prepare":
                    operation, created = prepare_deployment(
                        ledger,
                        args.gate_dir,
                        args.authorization,
                        key,
                        key_id,
                        args.deployment_id,
                        args.target,
                        DryRunAdapter.name,
                        max_age_seconds=args.max_age_seconds,
                    )
                    output = _public_operation(
                        operation,
                        len(ledger.events(args.deployment_id)),
                    )
                    output["result"] = "created" if created else "reused"
                else:
                    operation = dispatch_deployment(
                        ledger,
                        args.gate_dir,
                        args.authorization,
                        key,
                        key_id,
                        args.deployment_id,
                        args.target,
                        DryRunAdapter(),
                        max_age_seconds=args.max_age_seconds,
                        lease_seconds=args.lease_seconds,
                    )
                    output = _public_operation(
                        operation,
                        len(ledger.events(args.deployment_id)),
                    )
                    output["result"] = "dry-run-only"
    except DeploymentControllerConfigError as exc:
        print(f"deployment controller configuration error: {exc}", file=sys.stderr)
        return 2
    except (
        AttestationError,
        DeploymentAuthorizationError,
        DeploymentControllerError,
        OSError,
        sqlite3.Error,
    ) as exc:
        print(f"deployment controller error: {exc}", file=sys.stderr)
        return 1

    print(json.dumps(output, sort_keys=True))
    return 0


def _public_operation(operation: Mapping[str, Any], event_count: int) -> dict[str, Any]:
    return {
        "adapter": operation["adapter"],
        "attempt_count": operation["attempt_count"],
        "deployment_id": operation["deployment_id"],
        "event_count": event_count,
        "external_operation_id": operation["external_operation_id"],
        "idempotency_key": operation["idempotency_key"],
        "reconciliation_count": operation["reconciliation_count"],
        "status": operation["status"],
        "target": operation["target"],
    }


def _request_from_operation(operation: Mapping[str, Any]) -> DeploymentRequest:
    return DeploymentRequest(
        schema_version=REQUEST_SCHEMA_VERSION,
        deployment_id=str(operation["deployment_id"]),
        authorization_id=str(operation["authorization_id"]),
        idempotency_key=str(operation["idempotency_key"]),
        run_id=str(operation["run_id"]),
        target=str(operation["target"]),
        adapter=str(operation["adapter"]),
        authorization_sha256=str(operation["authorization_sha256"]),
        gate_report_sha256=str(operation["gate_report_sha256"]),
        request_sha256=str(operation["request_sha256"]),
    )


def _validate_request(request: DeploymentRequest) -> None:
    if request.schema_version != REQUEST_SCHEMA_VERSION:
        raise DeploymentControllerError("deployment request schema_version is invalid")
    _validate_identifier(request.deployment_id, "deployment_id")
    _validate_identifier(request.authorization_id, "authorization_id")
    _validate_digest(request.idempotency_key, "idempotency_key")
    _validate_identifier(request.run_id, "run_id")
    _validate_target(request.target)
    _validate_identifier(request.adapter, "adapter")
    _validate_digest(request.authorization_sha256, "authorization_sha256")
    _validate_digest(request.gate_report_sha256, "gate_report_sha256")
    _validate_digest(request.request_sha256, "request_sha256")
    payload = asdict(request)
    payload.pop("request_sha256")
    if _digest(_canonical_json(payload)) != request.request_sha256:
        raise DeploymentControllerError("deployment request digest is inconsistent")


def _validate_adapter(adapter: DeploymentAdapter) -> None:
    _validate_identifier(adapter.name, "adapter")
    if not isinstance(adapter.supports_idempotent_submit, bool) or not isinstance(
        adapter.supports_lookup,
        bool,
    ):
        raise DeploymentControllerConfigError("adapter capabilities are invalid")


def _validate_adapter_result(result: AdapterResult) -> None:
    if result.status not in _TERMINAL_STATUSES:
        raise DeploymentControllerError("adapter result status is invalid")
    if result.status == "simulated":
        if result.external_operation_id is not None:
            raise DeploymentControllerError(
                "simulated adapter result must not contain an external operation"
            )
    elif (
        not isinstance(result.external_operation_id, str)
        or _EXTERNAL_ID_RE.fullmatch(result.external_operation_id) is None
    ):
        raise DeploymentControllerError(
            "external adapter result requires a safe operation identifier"
        )


def _validate_identifier(value: str, field: str) -> str:
    if not isinstance(value, str) or _IDENTIFIER_RE.fullmatch(value) is None:
        raise DeploymentControllerConfigError(f"{field} is invalid")
    return value


def _validate_target(value: str) -> str:
    if not isinstance(value, str) or _TARGET_RE.fullmatch(value) is None:
        raise DeploymentControllerConfigError("deployment target is invalid")
    return value


def _validate_digest(value: str, field: str) -> str:
    if not isinstance(value, str) or _DIGEST_RE.fullmatch(value) is None:
        raise DeploymentControllerError(f"{field} is invalid")
    return value


def _validate_lease(value: int) -> int:
    if isinstance(value, bool) or not isinstance(value, int):
        raise DeploymentControllerConfigError("lease duration must be an integer")
    if not 1 <= value <= MAX_LEASE_SECONDS:
        raise DeploymentControllerConfigError(
            f"lease duration must be between 1 and {MAX_LEASE_SECONDS} seconds"
        )
    return value


def _validate_reconciliation_policy(policy: ReconciliationPolicy) -> None:
    if not isinstance(policy, ReconciliationPolicy):
        raise DeploymentControllerConfigError("reconciliation policy is invalid")
    values = {
        "base delay": policy.base_delay_seconds,
        "maximum delay": policy.max_delay_seconds,
        "maximum attempts": policy.max_attempts,
        "deadline": policy.deadline_seconds,
    }
    for field, value in values.items():
        if isinstance(value, bool) or not isinstance(value, int):
            raise DeploymentControllerConfigError(
                f"reconciliation {field} must be an integer"
            )
    if not 1 <= policy.base_delay_seconds <= MAX_RECONCILIATION_DELAY_SECONDS:
        raise DeploymentControllerConfigError(
            "reconciliation base delay is outside the supported range"
        )
    if not (
        policy.base_delay_seconds
        <= policy.max_delay_seconds
        <= MAX_RECONCILIATION_DELAY_SECONDS
    ):
        raise DeploymentControllerConfigError(
            "reconciliation maximum delay is outside the supported range"
        )
    if not 1 <= policy.max_attempts <= MAX_RECONCILIATION_ATTEMPTS:
        raise DeploymentControllerConfigError(
            "reconciliation maximum attempts is outside the supported range"
        )
    if not 1 <= policy.deadline_seconds <= MAX_RECONCILIATION_DEADLINE_SECONDS:
        raise DeploymentControllerConfigError(
            "reconciliation deadline is outside the supported range"
        )
    if policy.deadline_seconds < policy.base_delay_seconds:
        raise DeploymentControllerConfigError(
            "reconciliation deadline must include the initial delay"
        )


def _validate_manual_resolution(resolution: ManualResolution) -> None:
    if not isinstance(resolution, ManualResolution):
        raise DeploymentControllerConfigError("manual resolution is invalid")
    if _RESOLUTION_ID_RE.fullmatch(resolution.resolution_id) is None:
        raise DeploymentControllerConfigError("resolution_id is invalid")
    _validate_identifier(resolution.deployment_id, "deployment_id")
    if resolution.proposed_status not in {"completed", "failed"}:
        raise DeploymentControllerConfigError("manual resolution status is invalid")
    _validate_identifier(resolution.adapter, "adapter")
    if resolution.external_operation_id is not None and (
        _EXTERNAL_ID_RE.fullmatch(resolution.external_operation_id) is None
    ):
        raise DeploymentControllerConfigError("external operation identifier is invalid")
    if _ERROR_CODE_RE.fullmatch(resolution.reason_code) is None:
        raise DeploymentControllerConfigError("manual resolution reason code is invalid")
    if resolution.expected_error_code is not None and (
        _ERROR_CODE_RE.fullmatch(resolution.expected_error_code) is None
    ):
        raise DeploymentControllerConfigError("expected error code is invalid")
    _parse_timestamp(resolution.expected_updated_at, "resolution expected_updated_at")
    for field, value in (
        ("evidence_sha256", resolution.evidence_sha256),
        ("provider_evidence_sha256", resolution.provider_evidence_sha256),
        ("request_sha256", resolution.request_sha256),
        ("approval_sha256", resolution.approval_sha256),
    ):
        _validate_digest(value, field)
    for field, value in (
        ("requested_by", resolution.requested_by),
        ("approved_by", resolution.approved_by),
        ("requester_key_id", resolution.requester_key_id),
        ("approver_key_id", resolution.approver_key_id),
    ):
        _validate_identifier(value, field)
    if resolution.requested_by == resolution.approved_by:
        raise DeploymentControllerConfigError(
            "manual resolution requester and approver must be different"
        )
    if resolution.requester_key_id == resolution.approver_key_id:
        raise DeploymentControllerConfigError(
            "manual resolution signer key IDs must be different"
        )


def _ensure_private_database(path: Path) -> None:
    _ensure_private_directory(path.parent)
    try:
        descriptor = os.open(
            path,
            os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_NOFOLLOW", 0),
            0o600,
        )
    except FileExistsError:
        pass
    else:
        os.close(descriptor)
    _validate_private_database(path)


def _ensure_private_directory(path: Path) -> None:
    try:
        path.mkdir(mode=0o700, parents=False, exist_ok=False)
    except FileExistsError:
        pass
    except OSError as exc:
        raise DeploymentControllerError(f"cannot create ledger directory: {exc}") from exc
    try:
        metadata = path.lstat()
    except OSError as exc:
        raise DeploymentControllerError(f"cannot inspect ledger directory: {exc}") from exc
    if not stat.S_ISDIR(metadata.st_mode) or path.is_symlink():
        raise DeploymentControllerError("ledger directory must be a real directory")
    if stat.S_IMODE(metadata.st_mode) != 0o700:
        raise DeploymentControllerError("ledger directory permissions must be 0700")
    if hasattr(os, "getuid") and metadata.st_uid != os.getuid():
        raise DeploymentControllerError("ledger directory must be owned by the current user")


def _validate_private_database(path: Path) -> None:
    try:
        metadata = path.lstat()
    except OSError as exc:
        raise DeploymentControllerError(f"cannot inspect deployment ledger: {exc}") from exc
    if not stat.S_ISREG(metadata.st_mode) or path.is_symlink():
        raise DeploymentControllerError("deployment ledger must be a regular file")
    if stat.S_IMODE(metadata.st_mode) != 0o600:
        raise DeploymentControllerError("deployment ledger permissions must be 0600")
    if metadata.st_nlink != 1:
        raise DeploymentControllerError("deployment ledger must not be hard-linked")
    if hasattr(os, "getuid") and metadata.st_uid != os.getuid():
        raise DeploymentControllerError("deployment ledger must be owned by the current user")


def _key_from_environment() -> tuple[bytes, str]:
    value = os.environ.get(KEY_ENV)
    key_id = os.environ.get(KEY_ID_ENV)
    if value is None:
        raise DeploymentControllerConfigError(f"{KEY_ENV} is required")
    if key_id is None:
        raise DeploymentControllerConfigError(f"{KEY_ID_ENV} is required")
    return value.encode("utf-8"), key_id


def _normalize_now(value: datetime | None) -> datetime:
    result = value or datetime.now(UTC)
    if result.tzinfo is None:
        raise DeploymentControllerConfigError("current time must include a timezone")
    return result.astimezone(UTC)


def _parse_timestamp(value: str, field: str) -> datetime:
    try:
        result = datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError as exc:
        raise DeploymentControllerError(f"{field} is invalid") from exc
    if result.tzinfo is None:
        raise DeploymentControllerError(f"{field} must include a timezone")
    return result.astimezone(UTC)


def _format_timestamp(value: datetime) -> str:
    return value.astimezone(UTC).isoformat().replace("+00:00", "Z")


def _canonical_json(value: Mapping[str, Any]) -> bytes:
    return json.dumps(
        value,
        ensure_ascii=False,
        sort_keys=True,
        separators=(",", ":"),
    ).encode("utf-8")


def _digest(value: bytes) -> str:
    return hashlib.sha256(value).hexdigest()


if __name__ == "__main__":
    raise SystemExit(main())
