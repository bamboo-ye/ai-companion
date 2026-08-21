from __future__ import annotations

import json
import re
import sqlite3
from contextlib import closing
from datetime import UTC, datetime
from pathlib import Path
from typing import Any, Literal, Mapping

from .deployment_controller import (
    _canonical_json,
    _digest,
    _ensure_private_database,
    _format_timestamp,
    _normalize_now,
    _parse_timestamp,
    _validate_digest,
    _validate_private_database,
)
from .deployment_shadow import (
    DeploymentShadowConfigError,
    DeploymentShadowError,
    _validate_duration,
    _validate_error_code,
    _validate_shadow_report,
)


HISTORY_SCHEMA_VERSION = 1
MAX_HISTORY_EVENTS = 100_000
_CHAIN_DOMAIN = b"ai-companion/deployment-shadow-history/v1"
_ZERO_CHAIN = "0" * 64
_EVENT_ID_RE = re.compile(r"shadow-event-[0-9a-f]{32}")
_METADATA_COLUMNS = {
    "singleton",
    "schema_version",
    "provider_instance_sha256",
    "created_at",
}
_EVENT_COLUMNS = {
    "sequence",
    "event_id",
    "observed_at",
    "outcome",
    "duration_seconds",
    "error_code",
    "report_sha256",
    "report_json",
    "previous_chain_sha256",
    "chain_sha256",
}


class DeploymentShadowHistoryError(DeploymentShadowError):
    pass


class ShadowHistoryLedger:
    def __init__(
        self,
        path: Path,
        provider_instance_sha256: str,
        *,
        create: bool = True,
        now: datetime | None = None,
    ) -> None:
        try:
            _validate_digest(
                provider_instance_sha256,
                "shadow history provider_instance_sha256",
            )
        except ValueError as exc:
            raise DeploymentShadowConfigError(
                "shadow history provider instance digest is invalid"
            ) from exc
        self.path = path
        self.provider_instance_sha256 = provider_instance_sha256
        if create:
            _ensure_private_database(path)
            self._initialize(_normalize_now(now))
        else:
            _validate_private_database(path)
            self._validate_schema()
        self._validate_binding()

    def record_success(
        self,
        report: Mapping[str, Any],
        duration_seconds: float,
        now: datetime,
    ) -> bool:
        validated = _validate_shadow_report(report)
        if validated["provider_instance_sha256"] != self.provider_instance_sha256:
            raise DeploymentShadowHistoryError(
                "shadow history report provider binding is invalid"
            )
        content = _canonical_json(validated)
        return self._append(
            outcome="success",
            observed_at=_normalize_now(now),
            duration_seconds=_validate_duration(duration_seconds),
            error_code=None,
            report_sha256=_digest(content),
            report_json=content.decode("utf-8"),
        )

    def record_failure(
        self,
        error_code: str,
        duration_seconds: float,
        now: datetime,
    ) -> bool:
        return self._append(
            outcome="error",
            observed_at=_normalize_now(now),
            duration_seconds=_validate_duration(duration_seconds),
            error_code=_validate_error_code(error_code),
            report_sha256=None,
            report_json=None,
        )

    def events(
        self,
        *,
        until: datetime | None = None,
    ) -> list[dict[str, Any]]:
        end = _normalize_now(until or datetime.now(UTC))
        with closing(self._connect(read_only=True)) as connection:
            count = int(
                connection.execute(
                    "SELECT COUNT(*) FROM shadow_history_events WHERE observed_at <= ?",
                    (_format_timestamp(end),),
                ).fetchone()[0]
            )
            if count > MAX_HISTORY_EVENTS:
                raise DeploymentShadowHistoryError(
                    "shadow history exceeds the bounded verification limit"
                )
            rows = connection.execute(
                """
                SELECT * FROM shadow_history_events
                WHERE observed_at <= ?
                ORDER BY sequence
                """,
                (_format_timestamp(end),),
            ).fetchall()
        events = [self._validate_event_row(dict(row)) for row in rows]
        previous = _ZERO_CHAIN
        for expected_sequence, event in enumerate(events, start=1):
            if event["sequence"] != expected_sequence:
                raise DeploymentShadowHistoryError(
                    "shadow history sequence is not contiguous"
                )
            if event["previous_chain_sha256"] != previous:
                raise DeploymentShadowHistoryError(
                    "shadow history previous chain binding is invalid"
                )
            expected_chain = self._chain_digest(previous, self._event_payload(event))
            if event["chain_sha256"] != expected_chain:
                raise DeploymentShadowHistoryError(
                    "shadow history hash chain is invalid"
                )
            previous = str(event["chain_sha256"])
        return events

    def _append(
        self,
        *,
        outcome: Literal["success", "error"],
        observed_at: datetime,
        duration_seconds: float,
        error_code: str | None,
        report_sha256: str | None,
        report_json: str | None,
    ) -> bool:
        timestamp = _format_timestamp(observed_at)
        with closing(self._connect()) as connection, connection:
            connection.execute("BEGIN IMMEDIATE")
            existing = connection.execute(
                "SELECT * FROM shadow_history_events WHERE observed_at = ?",
                (timestamp,),
            ).fetchone()
            last = connection.execute(
                """
                SELECT sequence, chain_sha256 FROM shadow_history_events
                ORDER BY sequence DESC LIMIT 1
                """
            ).fetchone()
            previous_chain = str(last["chain_sha256"]) if last is not None else _ZERO_CHAIN
            sequence = int(last["sequence"]) + 1 if last is not None else 1
            base_payload: dict[str, Any] = {
                "provider_instance_sha256": self.provider_instance_sha256,
                "observed_at": timestamp,
                "outcome": outcome,
                "duration_seconds": duration_seconds,
                "error_code": error_code,
                "report_sha256": report_sha256,
            }
            event_id = f"shadow-event-{_digest(_canonical_json(base_payload))[:32]}"
            event_payload = dict(base_payload)
            event_payload.update(
                {
                    "sequence": sequence,
                    "event_id": event_id,
                    "previous_chain_sha256": previous_chain,
                }
            )
            chain_sha256 = self._chain_digest(previous_chain, event_payload)
            if existing is not None:
                validated = self._validate_event_row(dict(existing))
                expected = {
                    "event_id": event_id,
                    "outcome": outcome,
                    "duration_seconds": duration_seconds,
                    "error_code": error_code,
                    "report_sha256": report_sha256,
                    "report_json": report_json,
                }
                if any(validated.get(field) != value for field, value in expected.items()):
                    raise DeploymentShadowHistoryError(
                        "shadow history timestamp is already bound to another event"
                    )
                connection.commit()
                return False
            connection.execute(
                """
                INSERT INTO shadow_history_events (
                    sequence, event_id, observed_at, outcome, duration_seconds,
                    error_code, report_sha256, report_json,
                    previous_chain_sha256, chain_sha256
                ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                """,
                (
                    sequence,
                    event_id,
                    timestamp,
                    outcome,
                    duration_seconds,
                    error_code,
                    report_sha256,
                    report_json,
                    previous_chain,
                    chain_sha256,
                ),
            )
            connection.commit()
        _validate_private_database(self.path)
        return True

    def _initialize(self, now: datetime) -> None:
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
                connection.execute(
                    """
                    CREATE TABLE shadow_history_metadata (
                        singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
                        schema_version INTEGER NOT NULL,
                        provider_instance_sha256 TEXT NOT NULL,
                        created_at TEXT NOT NULL
                    )
                    """
                )
                connection.execute(
                    """
                    CREATE TABLE shadow_history_events (
                        sequence INTEGER PRIMARY KEY,
                        event_id TEXT NOT NULL UNIQUE,
                        observed_at TEXT NOT NULL UNIQUE,
                        outcome TEXT NOT NULL CHECK (outcome IN ('success', 'error')),
                        duration_seconds REAL NOT NULL CHECK (duration_seconds >= 0),
                        error_code TEXT,
                        report_sha256 TEXT,
                        report_json TEXT,
                        previous_chain_sha256 TEXT NOT NULL,
                        chain_sha256 TEXT NOT NULL
                    )
                    """
                )
                connection.execute(
                    """
                    INSERT INTO shadow_history_metadata (
                        singleton, schema_version, provider_instance_sha256, created_at
                    ) VALUES (1, ?, ?, ?)
                    """,
                    (
                        HISTORY_SCHEMA_VERSION,
                        self.provider_instance_sha256,
                        _format_timestamp(now),
                    ),
                )
                connection.execute(f"PRAGMA user_version = {HISTORY_SCHEMA_VERSION}")
                connection.commit()
            self._validate_schema(connection)
        _validate_private_database(self.path)

    def _validate_schema(self, existing: sqlite3.Connection | None = None) -> None:
        owned = existing is None
        connection = existing or self._connect(read_only=True)
        try:
            version = int(connection.execute("PRAGMA user_version").fetchone()[0])
            tables = {
                str(row[0])
                for row in connection.execute(
                    "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'"
                ).fetchall()
            }
            if version != HISTORY_SCHEMA_VERSION or tables != {
                "shadow_history_metadata",
                "shadow_history_events",
            }:
                raise DeploymentShadowHistoryError("shadow history schema is invalid")
            metadata_columns = {
                str(row[1])
                for row in connection.execute(
                    "PRAGMA table_info(shadow_history_metadata)"
                ).fetchall()
            }
            event_columns = {
                str(row[1])
                for row in connection.execute(
                    "PRAGMA table_info(shadow_history_events)"
                ).fetchall()
            }
            if metadata_columns != _METADATA_COLUMNS or event_columns != _EVENT_COLUMNS:
                raise DeploymentShadowHistoryError("shadow history columns are invalid")
        finally:
            if owned:
                connection.close()

    def _validate_binding(self) -> None:
        with closing(self._connect(read_only=True)) as connection:
            rows = connection.execute(
                "SELECT * FROM shadow_history_metadata"
            ).fetchall()
        if len(rows) != 1:
            raise DeploymentShadowHistoryError("shadow history metadata is invalid")
        row = dict(rows[0])
        if (
            row["singleton"] != 1
            or row["schema_version"] != HISTORY_SCHEMA_VERSION
            or row["provider_instance_sha256"] != self.provider_instance_sha256
        ):
            raise DeploymentShadowHistoryError(
                "shadow history provider binding is invalid"
            )
        _parse_timestamp(str(row["created_at"]), "shadow history created_at")

    def _validate_event_row(self, value: Mapping[str, Any]) -> dict[str, Any]:
        if set(value) != _EVENT_COLUMNS:
            raise DeploymentShadowHistoryError("shadow history event is invalid")
        result = dict(value)
        sequence = result["sequence"]
        if isinstance(sequence, bool) or not isinstance(sequence, int) or sequence < 1:
            raise DeploymentShadowHistoryError("shadow history event sequence is invalid")
        if not isinstance(result["event_id"], str) or _EVENT_ID_RE.fullmatch(
            result["event_id"]
        ) is None:
            raise DeploymentShadowHistoryError("shadow history event ID is invalid")
        _parse_timestamp(str(result["observed_at"]), "shadow history observed_at")
        if result["outcome"] not in {"success", "error"}:
            raise DeploymentShadowHistoryError("shadow history outcome is invalid")
        result["duration_seconds"] = _validate_duration(result["duration_seconds"])
        for field in ("previous_chain_sha256", "chain_sha256"):
            try:
                _validate_digest(str(result[field]), f"shadow history {field}")
            except ValueError as exc:
                raise DeploymentShadowHistoryError(
                    f"shadow history {field} is invalid"
                ) from exc
        if result["outcome"] == "success":
            if result["error_code"] is not None:
                raise DeploymentShadowHistoryError(
                    "successful shadow history event contains an error"
                )
            if not isinstance(result["report_json"], str):
                raise DeploymentShadowHistoryError(
                    "successful shadow history event has no report"
                )
            try:
                report_value = json.loads(result["report_json"])
            except json.JSONDecodeError as exc:
                raise DeploymentShadowHistoryError(
                    "shadow history report JSON is invalid"
                ) from exc
            report = _validate_shadow_report(report_value)
            report_digest = _digest(_canonical_json(report))
            if (
                result["report_sha256"] != report_digest
                or report["provider_instance_sha256"] != self.provider_instance_sha256
                or report["generated_at"] != result["observed_at"]
            ):
                raise DeploymentShadowHistoryError(
                    "shadow history report binding is invalid"
                )
            result["report"] = report
        else:
            if result["report_sha256"] is not None or result["report_json"] is not None:
                raise DeploymentShadowHistoryError(
                    "failed shadow history event contains a report"
                )
            if result["error_code"] is None:
                raise DeploymentShadowHistoryError(
                    "failed shadow history event has no error code"
                )
            _validate_error_code(result["error_code"])
            result["report"] = None
        return result

    def _event_payload(self, event: Mapping[str, Any]) -> dict[str, Any]:
        return {
            "provider_instance_sha256": self.provider_instance_sha256,
            "sequence": event["sequence"],
            "event_id": event["event_id"],
            "observed_at": event["observed_at"],
            "outcome": event["outcome"],
            "duration_seconds": event["duration_seconds"],
            "error_code": event["error_code"],
            "report_sha256": event["report_sha256"],
            "previous_chain_sha256": event["previous_chain_sha256"],
        }

    @staticmethod
    def _chain_digest(previous: str, payload: Mapping[str, Any]) -> str:
        return _digest(_CHAIN_DOMAIN + previous.encode("ascii") + _canonical_json(payload))

    def _connect(self, *, read_only: bool = False) -> sqlite3.Connection:
        _validate_private_database(self.path)
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
        connection.execute("PRAGMA foreign_keys = ON")
        if read_only:
            connection.execute("PRAGMA query_only = ON")
        else:
            connection.execute("PRAGMA synchronous = FULL")
        return connection
