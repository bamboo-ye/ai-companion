from __future__ import annotations

import argparse
import ipaddress
import json
import os
import re
import sqlite3
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from contextlib import closing
from dataclasses import dataclass, field
from datetime import datetime, timedelta
from http import HTTPStatus
from pathlib import Path
from typing import Any, Callable, Literal, Mapping, Protocol, Sequence

from .deployment_controller import (
    _canonical_json,
    _digest,
    _ensure_private_database,
    _format_timestamp,
    _normalize_now,
    _parse_timestamp,
    _validate_private_database,
)
from .direct_rollout_traffic import (
    DirectTrafficError,
    SQLiteDirectTrafficSandbox,
    provider_instance_sha256,
)
from .release_gate import (
    DEFAULT_CANARY_LEASE_ROOT,
    ReleaseGateError,
    try_acquire_canary_lease,
)
from .release_gate_history import ReleaseGateHistoryError, environment_sha256


PROVIDER_STATE_SCHEMA_VERSION = "agent-direct-traffic-provider-state-v1"
SHADOW_REPORT_SCHEMA_VERSION = "agent-direct-traffic-shadow-report-v1"
HISTORY_SCHEMA_VERSION = 1
IMPLEMENTATION_VERSION = "1.0.0"
TOKEN_ENV = "OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_PROVIDER_TOKEN"
MAX_RESPONSE_BYTES = 32 * 1024
MAX_ATTEMPTS = 3
MAX_RETRY_DELAY_SECONDS = 5.0
MAX_HISTORY_EVENTS = 100_000
_ZERO_CHAIN = "0" * 64
_CHAIN_DOMAIN = b"ai-companion/direct-traffic-shadow-history/v1\0"
_IDENTIFIER_RE = re.compile(r"[A-Za-z0-9][A-Za-z0-9._-]{0,127}")
_HOST_RE = re.compile(
    r"(?=.{1,253}\Z)(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+"
    r"[a-z](?:[a-z0-9-]{0,61}[a-z0-9])?"
)
_DIGEST_RE = re.compile(r"[0-9a-f]{64}")
_ERROR_CODE_RE = re.compile(r"[a-z][a-z0-9_]{0,63}")
_EVENT_ID_RE = re.compile(r"direct-traffic-shadow-event-[0-9a-f]{32}")
_OUTCOMES = {"match", "drift", "lookup_error"}
_DRIFT_FIELDS = {
    "current_traffic_percent",
    "revision",
    "history_chain_sha256",
    "provider_snapshot_age",
    "local_state_changed_during_read",
}
_METADATA_COLUMNS = {
    "singleton",
    "schema_version",
    "observer_instance_sha256",
    "provider_instance_sha256",
    "environment_sha256",
    "created_at",
}
_EVENT_COLUMNS = {
    "sequence",
    "event_id",
    "observed_at",
    "outcome",
    "attempts",
    "duration_seconds",
    "error_code",
    "report_sha256",
    "report_json",
    "previous_chain_sha256",
    "chain_sha256",
}
_TRIGGERS = {
    "direct_traffic_shadow_events_no_update",
    "direct_traffic_shadow_events_no_delete",
}


class DirectTrafficShadowError(ValueError):
    pass


class DirectTrafficShadowConfigError(DirectTrafficShadowError):
    pass


class DirectTrafficProviderError(RuntimeError):
    def __init__(
        self,
        code: str,
        *,
        retryable: bool = False,
        attempts: int = 1,
        retry_after: str | None = None,
    ) -> None:
        if _ERROR_CODE_RE.fullmatch(code) is None:
            raise ValueError("direct traffic provider error code is invalid")
        if isinstance(attempts, bool) or not isinstance(attempts, int) or attempts < 1:
            raise ValueError("direct traffic provider attempts is invalid")
        super().__init__(code)
        self.code = code
        self.retryable = retryable
        self.attempts = attempts
        self.retry_after = retry_after


@dataclass(frozen=True)
class ReadOnlyDirectTrafficProviderConfig:
    name: str
    base_url: str
    allowed_host: str
    provider_instance: str
    environment_sha256: str
    token: str = field(repr=False)
    state_path: str = "/v1/direct-traffic/state"
    timeout_seconds: float = 5.0
    max_attempts: int = 2
    retry_base_seconds: float = 0.25
    max_response_bytes: int = MAX_RESPONSE_BYTES
    max_snapshot_age_seconds: int = 90


@dataclass(frozen=True)
class TransportResponse:
    status: int
    body: bytes
    content_type: str
    final_url: str
    retry_after: str | None = None


class ReadTransport(Protocol):
    def __call__(
        self,
        url: str,
        token: str,
        timeout_seconds: float,
        max_response_bytes: int,
    ) -> TransportResponse: ...


@dataclass(frozen=True)
class ProviderTrafficState:
    provider_instance_sha256: str
    environment_sha256: str
    current_traffic_percent: int
    revision: int
    history_chain_sha256: str
    observed_at: str


@dataclass(frozen=True)
class TrafficStateObservation:
    state: ProviderTrafficState
    attempts: int


class ReadOnlyDirectTrafficProviderAdapter:
    implementation_version = IMPLEMENTATION_VERSION

    def __init__(
        self,
        config: ReadOnlyDirectTrafficProviderConfig,
        *,
        transport: ReadTransport | None = None,
        sleep: Callable[[float], None] = time.sleep,
    ) -> None:
        self.config = _validate_provider_config(config)
        self.name = self.config.name
        self._transport = transport or _default_transport
        self._sleep = sleep

    @property
    def provider_instance_sha256(self) -> str:
        return provider_instance_sha256(self.config.provider_instance)

    @property
    def observer_instance_sha256(self) -> str:
        return observer_instance_sha256(self.config)

    def read_state(self, *, now: datetime | None = None) -> TrafficStateObservation:
        current_time = _normalize_now(now)
        url = f"{self.config.base_url}{self.config.state_path}"
        for attempt in range(1, self.config.max_attempts + 1):
            try:
                response = self._transport(
                    url,
                    self.config.token,
                    self.config.timeout_seconds,
                    self.config.max_response_bytes,
                )
                state = self._interpret_response(response, url, current_time, attempt)
                return TrafficStateObservation(state=state, attempts=attempt)
            except DirectTrafficProviderError as exc:
                if not exc.retryable or attempt >= self.config.max_attempts:
                    raise DirectTrafficProviderError(
                        exc.code,
                        retryable=exc.retryable,
                        attempts=attempt,
                        retry_after=exc.retry_after,
                    ) from exc
                self._sleep(self._retry_delay(attempt, exc.retry_after))
            except (OSError, TimeoutError) as exc:
                if attempt >= self.config.max_attempts:
                    raise DirectTrafficProviderError(
                        "provider_transport_error",
                        retryable=True,
                        attempts=attempt,
                    ) from exc
                self._sleep(self._retry_delay(attempt, None))
            except Exception as exc:
                raise DirectTrafficProviderError(
                    "provider_transport_exception", attempts=attempt
                ) from exc
        raise AssertionError("direct traffic provider attempt loop did not terminate")

    def _interpret_response(
        self,
        response: TransportResponse,
        requested_url: str,
        now: datetime,
        attempt: int,
    ) -> ProviderTrafficState:
        if response.final_url != requested_url:
            raise DirectTrafficProviderError("provider_redirect_rejected", attempts=attempt)
        if len(response.body) > self.config.max_response_bytes:
            raise DirectTrafficProviderError("provider_response_too_large", attempts=attempt)
        if response.status in {
            HTTPStatus.REQUEST_TIMEOUT,
            HTTPStatus.TOO_MANY_REQUESTS,
            HTTPStatus.INTERNAL_SERVER_ERROR,
            HTTPStatus.BAD_GATEWAY,
            HTTPStatus.SERVICE_UNAVAILABLE,
            HTTPStatus.GATEWAY_TIMEOUT,
        }:
            raise DirectTrafficProviderError(
                f"provider_http_{response.status}",
                retryable=True,
                attempts=attempt,
                retry_after=response.retry_after,
            )
        if response.status != HTTPStatus.OK:
            raise DirectTrafficProviderError(f"provider_http_{response.status}", attempts=attempt)
        if response.content_type.split(";", 1)[0].strip().lower() != "application/json":
            raise DirectTrafficProviderError("provider_content_type", attempts=attempt)
        try:
            decoded = json.loads(
                response.body.decode("utf-8"), object_pairs_hook=_reject_duplicate_keys
            )
        except (UnicodeDecodeError, json.JSONDecodeError, ValueError) as exc:
            raise DirectTrafficProviderError("provider_invalid_json", attempts=attempt) from exc
        state = _validate_provider_state(decoded, now=now)
        if (
            state.provider_instance_sha256 != self.provider_instance_sha256
            or state.environment_sha256 != self.config.environment_sha256
        ):
            raise DirectTrafficProviderError("provider_binding_mismatch", attempts=attempt)
        return state

    def _retry_delay(self, attempt: int, retry_after: str | None) -> float:
        if retry_after is not None and retry_after.isdigit():
            return min(float(retry_after), MAX_RETRY_DELAY_SECONDS)
        return float(
            min(
                self.config.retry_base_seconds * (2 ** (attempt - 1)),
                MAX_RETRY_DELAY_SECONDS,
            )
        )


def observer_instance_sha256(config: ReadOnlyDirectTrafficProviderConfig) -> str:
    validated = _validate_provider_config(config)
    return _digest(
        _canonical_json(
            {
                "name": validated.name,
                "base_url": validated.base_url,
                "allowed_host": validated.allowed_host,
                "provider_instance": validated.provider_instance,
                "environment_sha256": validated.environment_sha256,
                "state_path": validated.state_path,
                "implementation_version": IMPLEMENTATION_VERSION,
            }
        )
    )


def run_shadow_comparison(
    sandbox: SQLiteDirectTrafficSandbox,
    adapter: ReadOnlyDirectTrafficProviderAdapter,
    history: DirectTrafficShadowHistory,
    *,
    now: datetime | None = None,
    monotonic: Callable[[], float] = time.monotonic,
) -> dict[str, Any]:
    current_time = _normalize_now(now)
    if (
        sandbox.provider_instance_sha256 != adapter.provider_instance_sha256
        or sandbox.environment_sha256 != adapter.config.environment_sha256
        or history.observer_instance_sha256 != adapter.observer_instance_sha256
        or history.provider_instance_sha256 != adapter.provider_instance_sha256
        or history.environment_sha256 != adapter.config.environment_sha256
    ):
        raise DirectTrafficShadowConfigError("direct traffic shadow binding is invalid")
    local_before = sandbox.status()
    started = monotonic()
    try:
        observation = adapter.read_state(now=current_time)
        attempts = observation.attempts
        provider = observation.state
        error_code = None
    except DirectTrafficProviderError as exc:
        attempts = exc.attempts
        provider = None
        error_code = exc.code
    duration = max(0.0, monotonic() - started)
    local_after = sandbox.status()
    drift_fields: list[str] = []
    if local_before != local_after:
        drift_fields.append("local_state_changed_during_read")
    if provider is not None:
        for field_name in (
            "current_traffic_percent",
            "revision",
            "history_chain_sha256",
        ):
            if local_after[field_name] != getattr(provider, field_name):
                drift_fields.append(field_name)
        provider_time = _parse_timestamp(provider.observed_at, "provider observed_at")
        if current_time - provider_time > timedelta(
            seconds=adapter.config.max_snapshot_age_seconds
        ):
            drift_fields.append("provider_snapshot_age")
    outcome: Literal["match", "drift", "lookup_error"]
    if error_code is not None:
        outcome = "lookup_error"
    elif drift_fields:
        outcome = "drift"
    else:
        outcome = "match"
    report = {
        "schema_version": SHADOW_REPORT_SCHEMA_VERSION,
        "generated_at": _format_timestamp(current_time),
        "observer_instance_sha256": adapter.observer_instance_sha256,
        "provider_instance_sha256": adapter.provider_instance_sha256,
        "environment_sha256": adapter.config.environment_sha256,
        "outcome": outcome,
        "drift_fields": sorted(drift_fields),
        "attempts": attempts,
        "duration_seconds": round(duration, 6),
        "error_code": error_code,
        "local": _public_local_state(local_after),
        "provider": _public_provider_state(provider),
    }
    validated = _validate_report(report)
    history.record(validated)
    return validated


class DirectTrafficShadowHistory:
    def __init__(
        self,
        path: Path,
        observer_instance_sha256: str,
        provider_instance_sha256: str,
        environment_sha256: str,
        *,
        create: bool = True,
        now: datetime | None = None,
    ) -> None:
        self.path = path
        self.observer_instance_sha256 = _validate_digest(
            observer_instance_sha256, "shadow observer_instance_sha256"
        )
        self.provider_instance_sha256 = _validate_digest(
            provider_instance_sha256, "shadow provider_instance_sha256"
        )
        self.environment_sha256 = _validate_digest(environment_sha256, "shadow environment_sha256")
        if create:
            _ensure_private_database(path)
            self._initialize(_normalize_now(now))
        else:
            _validate_private_database(path)
            self._validate_schema()
        self._validate_binding()

    def record(self, report: Mapping[str, Any]) -> bool:
        validated = _validate_report(report)
        if (
            validated["observer_instance_sha256"] != self.observer_instance_sha256
            or validated["provider_instance_sha256"] != self.provider_instance_sha256
            or validated["environment_sha256"] != self.environment_sha256
        ):
            raise DirectTrafficShadowError("shadow history report binding is invalid")
        content = _canonical_json(validated)
        report_sha256 = _digest(content)
        base = {
            "observer_instance_sha256": self.observer_instance_sha256,
            "provider_instance_sha256": self.provider_instance_sha256,
            "environment_sha256": self.environment_sha256,
            "observed_at": validated["generated_at"],
            "outcome": validated["outcome"],
            "attempts": validated["attempts"],
            "duration_seconds": validated["duration_seconds"],
            "error_code": validated["error_code"],
            "report_sha256": report_sha256,
        }
        event_id = f"direct-traffic-shadow-event-{_digest(_canonical_json(base))[:32]}"
        with closing(self._connect()) as connection, connection:
            connection.execute("BEGIN IMMEDIATE")
            existing = connection.execute(
                "SELECT * FROM direct_traffic_shadow_events WHERE event_id = ?",
                (event_id,),
            ).fetchone()
            if existing is not None:
                event = self._validate_event(dict(existing))
                if event["report_sha256"] != report_sha256:
                    raise DirectTrafficShadowError("shadow event ID collision")
                connection.commit()
                return False
            last = connection.execute(
                "SELECT sequence, chain_sha256 FROM direct_traffic_shadow_events "
                "ORDER BY sequence DESC LIMIT 1"
            ).fetchone()
            previous = str(last["chain_sha256"]) if last is not None else _ZERO_CHAIN
            sequence = int(last["sequence"]) + 1 if last is not None else 1
            payload = {
                **base,
                "sequence": sequence,
                "event_id": event_id,
                "previous_chain_sha256": previous,
            }
            chain = self._chain_digest(previous, payload)
            connection.execute(
                """
                INSERT INTO direct_traffic_shadow_events (
                    sequence, event_id, observed_at, outcome, attempts,
                    duration_seconds, error_code, report_sha256, report_json,
                    previous_chain_sha256, chain_sha256
                ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                """,
                (
                    sequence,
                    event_id,
                    validated["generated_at"],
                    validated["outcome"],
                    validated["attempts"],
                    validated["duration_seconds"],
                    validated["error_code"],
                    report_sha256,
                    content.decode("utf-8"),
                    previous,
                    chain,
                ),
            )
            connection.commit()
        _validate_private_database(self.path)
        return True

    def events(self) -> list[dict[str, Any]]:
        self._validate_schema()
        self._validate_binding()
        with closing(self._connect(read_only=True)) as connection:
            count = int(
                connection.execute("SELECT COUNT(*) FROM direct_traffic_shadow_events").fetchone()[
                    0
                ]
            )
            if count > MAX_HISTORY_EVENTS:
                raise DirectTrafficShadowError("shadow history verification limit exceeded")
            rows = connection.execute(
                "SELECT * FROM direct_traffic_shadow_events ORDER BY sequence"
            ).fetchall()
        events = [self._validate_event(dict(row)) for row in rows]
        previous = _ZERO_CHAIN
        for expected_sequence, event in enumerate(events, start=1):
            if event["sequence"] != expected_sequence:
                raise DirectTrafficShadowError("shadow history sequence is invalid")
            if event["previous_chain_sha256"] != previous:
                raise DirectTrafficShadowError("shadow history previous chain is invalid")
            expected = self._chain_digest(previous, self._event_payload(event))
            if event["chain_sha256"] != expected:
                raise DirectTrafficShadowError("shadow history hash chain is invalid")
            previous = str(event["chain_sha256"])
        return events

    def status(self) -> dict[str, Any]:
        events = self.events()
        counts = {outcome: 0 for outcome in _OUTCOMES}
        for event in events:
            counts[str(event["outcome"])] += 1
        return {
            "schema_version": "agent-direct-traffic-shadow-history-status-v1",
            "observer_instance_sha256": self.observer_instance_sha256,
            "provider_instance_sha256": self.provider_instance_sha256,
            "environment_sha256": self.environment_sha256,
            "events": len(events),
            "outcomes": counts,
            "chain_sha256": events[-1]["chain_sha256"] if events else _ZERO_CHAIN,
            "last_observed_at": events[-1]["observed_at"] if events else None,
        }

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
                    CREATE TABLE direct_traffic_shadow_metadata (
                        singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
                        schema_version INTEGER NOT NULL,
                        observer_instance_sha256 TEXT NOT NULL,
                        provider_instance_sha256 TEXT NOT NULL,
                        environment_sha256 TEXT NOT NULL,
                        created_at TEXT NOT NULL
                    )
                    """
                )
                connection.execute(
                    """
                    CREATE TABLE direct_traffic_shadow_events (
                        sequence INTEGER PRIMARY KEY,
                        event_id TEXT NOT NULL UNIQUE,
                        observed_at TEXT NOT NULL,
                        outcome TEXT NOT NULL CHECK (
                            outcome IN ('match', 'drift', 'lookup_error')
                        ),
                        attempts INTEGER NOT NULL CHECK (attempts BETWEEN 1 AND 3),
                        duration_seconds REAL NOT NULL CHECK (duration_seconds >= 0),
                        error_code TEXT,
                        report_sha256 TEXT NOT NULL,
                        report_json TEXT NOT NULL,
                        previous_chain_sha256 TEXT NOT NULL,
                        chain_sha256 TEXT NOT NULL
                    )
                    """
                )
                for action in ("UPDATE", "DELETE"):
                    name = f"direct_traffic_shadow_events_no_{action.lower()}"
                    connection.execute(
                        f"CREATE TRIGGER {name} BEFORE {action} ON "
                        "direct_traffic_shadow_events BEGIN SELECT RAISE(ABORT, "
                        "'direct traffic shadow history is append-only'); END"
                    )
                connection.execute(
                    """
                    INSERT INTO direct_traffic_shadow_metadata (
                        singleton, schema_version, observer_instance_sha256,
                        provider_instance_sha256, environment_sha256, created_at
                    ) VALUES (1, ?, ?, ?, ?, ?)
                    """,
                    (
                        HISTORY_SCHEMA_VERSION,
                        self.observer_instance_sha256,
                        self.provider_instance_sha256,
                        self.environment_sha256,
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
            triggers = {
                str(row[0])
                for row in connection.execute(
                    "SELECT name FROM sqlite_master WHERE type='trigger'"
                ).fetchall()
            }
            if (
                version != HISTORY_SCHEMA_VERSION
                or tables != {"direct_traffic_shadow_metadata", "direct_traffic_shadow_events"}
                or triggers != _TRIGGERS
            ):
                raise DirectTrafficShadowError("shadow history schema is invalid")
            metadata_columns = {
                str(row[1])
                for row in connection.execute(
                    "PRAGMA table_info(direct_traffic_shadow_metadata)"
                ).fetchall()
            }
            event_columns = {
                str(row[1])
                for row in connection.execute(
                    "PRAGMA table_info(direct_traffic_shadow_events)"
                ).fetchall()
            }
            if metadata_columns != _METADATA_COLUMNS or event_columns != _EVENT_COLUMNS:
                raise DirectTrafficShadowError("shadow history columns are invalid")
        finally:
            if owned:
                connection.close()

    def _validate_binding(self) -> None:
        with closing(self._connect(read_only=True)) as connection:
            rows = connection.execute("SELECT * FROM direct_traffic_shadow_metadata").fetchall()
        if len(rows) != 1:
            raise DirectTrafficShadowError("shadow history metadata is invalid")
        row = dict(rows[0])
        if (
            row["singleton"] != 1
            or row["schema_version"] != HISTORY_SCHEMA_VERSION
            or row["observer_instance_sha256"] != self.observer_instance_sha256
            or row["provider_instance_sha256"] != self.provider_instance_sha256
            or row["environment_sha256"] != self.environment_sha256
        ):
            raise DirectTrafficShadowError("shadow history binding is invalid")
        _parse_timestamp(str(row["created_at"]), "shadow history created_at")

    def _validate_event(self, value: Mapping[str, Any]) -> dict[str, Any]:
        if set(value) != _EVENT_COLUMNS:
            raise DirectTrafficShadowError("shadow history event is invalid")
        event = dict(value)
        _validate_int(event["sequence"], "shadow sequence", 1, MAX_HISTORY_EVENTS)
        if (
            not isinstance(event["event_id"], str)
            or _EVENT_ID_RE.fullmatch(event["event_id"]) is None
        ):
            raise DirectTrafficShadowError("shadow history event ID is invalid")
        _parse_timestamp(str(event["observed_at"]), "shadow observed_at")
        if event["outcome"] not in _OUTCOMES:
            raise DirectTrafficShadowError("shadow history outcome is invalid")
        _validate_int(event["attempts"], "shadow attempts", 1, MAX_ATTEMPTS)
        _validate_duration(event["duration_seconds"])
        if event["error_code"] is not None:
            _validate_error_code(event["error_code"])
        for field_name in (
            "report_sha256",
            "previous_chain_sha256",
            "chain_sha256",
        ):
            _validate_digest(str(event[field_name]), f"shadow {field_name}")
        try:
            report_value = json.loads(
                str(event["report_json"]), object_pairs_hook=_reject_duplicate_keys
            )
        except (json.JSONDecodeError, ValueError) as exc:
            raise DirectTrafficShadowError("shadow history report JSON is invalid") from exc
        report = _validate_report(report_value)
        if (
            _digest(_canonical_json(report)) != event["report_sha256"]
            or report["generated_at"] != event["observed_at"]
            or report["outcome"] != event["outcome"]
            or report["attempts"] != event["attempts"]
            or report["duration_seconds"] != event["duration_seconds"]
            or report["error_code"] != event["error_code"]
            or report["observer_instance_sha256"] != self.observer_instance_sha256
            or report["provider_instance_sha256"] != self.provider_instance_sha256
            or report["environment_sha256"] != self.environment_sha256
        ):
            raise DirectTrafficShadowError("shadow history report binding is invalid")
        event["report"] = report
        return event

    def _event_payload(self, event: Mapping[str, Any]) -> dict[str, Any]:
        return {
            "observer_instance_sha256": self.observer_instance_sha256,
            "provider_instance_sha256": self.provider_instance_sha256,
            "environment_sha256": self.environment_sha256,
            "sequence": event["sequence"],
            "event_id": event["event_id"],
            "observed_at": event["observed_at"],
            "outcome": event["outcome"],
            "attempts": event["attempts"],
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
        connection = sqlite3.connect(target, uri=read_only, timeout=5.0, isolation_level=None)
        connection.row_factory = sqlite3.Row
        connection.execute("PRAGMA busy_timeout = 5000")
        if read_only:
            connection.execute("PRAGMA query_only = ON")
        else:
            connection.execute("PRAGMA synchronous = FULL")
        return connection


def _validate_provider_config(
    value: ReadOnlyDirectTrafficProviderConfig,
) -> ReadOnlyDirectTrafficProviderConfig:
    _validate_identifier(value.name, "provider name")
    _validate_identifier(value.provider_instance, "provider instance")
    try:
        _validate_digest(value.environment_sha256, "provider environment_sha256")
    except DirectTrafficShadowError as exc:
        raise DirectTrafficShadowConfigError("provider environment_sha256 is invalid") from exc
    if (
        not isinstance(value.allowed_host, str)
        or _HOST_RE.fullmatch(value.allowed_host) is None
        or value.allowed_host != value.allowed_host.lower()
    ):
        raise DirectTrafficShadowConfigError("provider allowed_host is invalid")
    try:
        ipaddress.ip_address(value.allowed_host)
    except ValueError:
        pass
    else:
        raise DirectTrafficShadowConfigError("provider host cannot be an IP address")
    try:
        parsed = urllib.parse.urlsplit(value.base_url)
        port = parsed.port
    except ValueError as exc:
        raise DirectTrafficShadowConfigError("provider base URL is invalid") from exc
    if (
        parsed.scheme != "https"
        or parsed.hostname is None
        or parsed.hostname.lower() != value.allowed_host
        or parsed.username is not None
        or parsed.password is not None
        or parsed.query
        or parsed.fragment
        or not parsed.netloc
        or port not in {None, 443}
        or value.base_url.endswith("/")
        or "%" in value.base_url
        or "\\" in value.base_url
        or any(ord(character) < 0x21 or ord(character) > 0x7E for character in value.base_url)
        or "//" in parsed.path
        or any(part in {".", ".."} for part in parsed.path.split("/"))
    ):
        raise DirectTrafficShadowConfigError("provider requires an exact allowlisted HTTPS origin")
    if (
        not isinstance(value.state_path, str)
        or not value.state_path.startswith("/")
        or value.state_path.endswith("/")
        or "?" in value.state_path
        or "#" in value.state_path
        or "%" in value.state_path
        or "\\" in value.state_path
        or "//" in value.state_path
        or any(part in {"", ".", ".."} for part in value.state_path.split("/")[1:])
        or any(ord(character) < 0x21 or ord(character) > 0x7E for character in value.state_path)
    ):
        raise DirectTrafficShadowConfigError("provider state path is invalid")
    if (
        not isinstance(value.token, str)
        or not 16 <= len(value.token) <= 4096
        or any(ord(character) < 0x20 or ord(character) == 0x7F for character in value.token)
    ):
        raise DirectTrafficShadowConfigError("provider token is invalid")
    _validate_float(value.timeout_seconds, "timeout_seconds", 0.1, 30.0)
    try:
        _validate_int(value.max_attempts, "max_attempts", 1, MAX_ATTEMPTS)
        _validate_int(value.max_response_bytes, "max_response_bytes", 1024, MAX_RESPONSE_BYTES)
        _validate_int(value.max_snapshot_age_seconds, "max_snapshot_age_seconds", 1, 3600)
    except DirectTrafficShadowError as exc:
        raise DirectTrafficShadowConfigError(str(exc)) from exc
    _validate_float(
        value.retry_base_seconds,
        "retry_base_seconds",
        0.0,
        MAX_RETRY_DELAY_SECONDS,
    )
    return value


def _validate_provider_state(value: Any, *, now: datetime) -> ProviderTrafficState:
    fields = {
        "schema_version",
        "provider_instance_sha256",
        "environment_sha256",
        "current_traffic_percent",
        "revision",
        "history_chain_sha256",
        "observed_at",
    }
    if not isinstance(value, Mapping) or set(value) != fields:
        raise DirectTrafficProviderError("provider_contract")
    if value["schema_version"] != PROVIDER_STATE_SCHEMA_VERSION:
        raise DirectTrafficProviderError("provider_contract")
    try:
        provider_digest = _validate_digest(
            value["provider_instance_sha256"], "provider instance digest"
        )
        environment_digest = _validate_digest(
            value["environment_sha256"], "provider environment digest"
        )
        chain = _validate_digest(value["history_chain_sha256"], "provider chain")
        percent = _validate_int(
            value["current_traffic_percent"], "provider traffic percent", 0, 100
        )
        revision = _validate_int(value["revision"], "provider revision", 0, 1_000_000_000)
        observed_at = _parse_timestamp(str(value["observed_at"]), "provider observed_at")
    except (DirectTrafficShadowError, ValueError) as exc:
        raise DirectTrafficProviderError("provider_contract") from exc
    if observed_at > now + timedelta(minutes=5):
        raise DirectTrafficProviderError("provider_future_timestamp")
    return ProviderTrafficState(
        provider_instance_sha256=provider_digest,
        environment_sha256=environment_digest,
        current_traffic_percent=percent,
        revision=revision,
        history_chain_sha256=chain,
        observed_at=_format_timestamp(observed_at),
    )


def _validate_report(value: Any) -> dict[str, Any]:
    fields = {
        "schema_version",
        "generated_at",
        "observer_instance_sha256",
        "provider_instance_sha256",
        "environment_sha256",
        "outcome",
        "drift_fields",
        "attempts",
        "duration_seconds",
        "error_code",
        "local",
        "provider",
    }
    if not isinstance(value, Mapping) or set(value) != fields:
        raise DirectTrafficShadowError("direct traffic shadow report is invalid")
    result = dict(value)
    if result["schema_version"] != SHADOW_REPORT_SCHEMA_VERSION:
        raise DirectTrafficShadowError("direct traffic shadow report schema is invalid")
    _parse_timestamp(str(result["generated_at"]), "shadow generated_at")
    for field_name in (
        "observer_instance_sha256",
        "provider_instance_sha256",
        "environment_sha256",
    ):
        _validate_digest(result[field_name], f"shadow {field_name}")
    if result["outcome"] not in _OUTCOMES:
        raise DirectTrafficShadowError("shadow outcome is invalid")
    if (
        not isinstance(result["drift_fields"], list)
        or result["drift_fields"] != sorted(set(result["drift_fields"]))
        or any(field_name not in _DRIFT_FIELDS for field_name in result["drift_fields"])
    ):
        raise DirectTrafficShadowError("shadow drift fields are invalid")
    _validate_int(result["attempts"], "shadow attempts", 1, MAX_ATTEMPTS)
    result["duration_seconds"] = _validate_duration(result["duration_seconds"])
    if result["error_code"] is not None:
        _validate_error_code(result["error_code"])
    _validate_local_state(result["local"])
    if result["provider"] is not None:
        _validate_public_provider_state(result["provider"])
    if result["outcome"] == "match" and (
        result["drift_fields"] or result["error_code"] is not None or result["provider"] is None
    ):
        raise DirectTrafficShadowError("matching shadow report is inconsistent")
    if result["outcome"] == "drift" and (
        not result["drift_fields"] or result["error_code"] is not None or result["provider"] is None
    ):
        raise DirectTrafficShadowError("drift shadow report is inconsistent")
    if result["outcome"] == "lookup_error" and (
        result["error_code"] is None or result["provider"] is not None
    ):
        raise DirectTrafficShadowError("failed shadow report is inconsistent")
    return result


def _public_local_state(value: Mapping[str, Any]) -> dict[str, Any]:
    return {
        "current_traffic_percent": value["current_traffic_percent"],
        "revision": value["revision"],
        "history_chain_sha256": value["history_chain_sha256"],
    }


def _public_provider_state(value: ProviderTrafficState | None) -> dict[str, Any] | None:
    if value is None:
        return None
    return {
        "current_traffic_percent": value.current_traffic_percent,
        "revision": value.revision,
        "history_chain_sha256": value.history_chain_sha256,
        "observed_at": value.observed_at,
    }


def _validate_local_state(value: Any) -> None:
    if not isinstance(value, Mapping) or set(value) != {
        "current_traffic_percent",
        "revision",
        "history_chain_sha256",
    }:
        raise DirectTrafficShadowError("shadow local state is invalid")
    _validate_int(value["current_traffic_percent"], "local traffic percent", 0, 100)
    _validate_int(value["revision"], "local revision", 0, 1_000_000_000)
    _validate_digest(value["history_chain_sha256"], "local chain")


def _validate_public_provider_state(value: Any) -> None:
    if not isinstance(value, Mapping) or set(value) != {
        "current_traffic_percent",
        "revision",
        "history_chain_sha256",
        "observed_at",
    }:
        raise DirectTrafficShadowError("shadow provider state is invalid")
    _validate_int(value["current_traffic_percent"], "provider traffic percent", 0, 100)
    _validate_int(value["revision"], "provider revision", 0, 1_000_000_000)
    _validate_digest(value["history_chain_sha256"], "provider chain")
    _parse_timestamp(str(value["observed_at"]), "provider observed_at")


def _default_transport(
    url: str,
    token: str,
    timeout_seconds: float,
    max_response_bytes: int,
) -> TransportResponse:
    request = urllib.request.Request(
        url,
        headers={
            "Accept": "application/json",
            "Authorization": f"Bearer {token}",
            "User-Agent": "ai-companion-direct-traffic-shadow/1.0",
        },
        method="GET",
    )
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
    except urllib.error.URLError as exc:
        raise DirectTrafficProviderError("provider_transport_error", retryable=True) from exc


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


def _validate_identifier(value: Any, label: str) -> str:
    if not isinstance(value, str) or _IDENTIFIER_RE.fullmatch(value) is None:
        raise DirectTrafficShadowConfigError(f"{label} is invalid")
    return value


def _validate_digest(value: Any, label: str) -> str:
    if not isinstance(value, str) or _DIGEST_RE.fullmatch(value) is None:
        raise DirectTrafficShadowError(f"{label} is invalid")
    return value


def _validate_error_code(value: Any) -> str:
    if not isinstance(value, str) or _ERROR_CODE_RE.fullmatch(value) is None:
        raise DirectTrafficShadowError("shadow error code is invalid")
    return value


def _validate_int(value: Any, label: str, minimum: int, maximum: int) -> int:
    if isinstance(value, bool) or not isinstance(value, int) or value < minimum or value > maximum:
        raise DirectTrafficShadowError(f"{label} is invalid")
    return value


def _validate_float(value: Any, label: str, minimum: float, maximum: float) -> float:
    if (
        isinstance(value, bool)
        or not isinstance(value, (int, float))
        or not minimum <= float(value) <= maximum
    ):
        raise DirectTrafficShadowConfigError(f"{label} is invalid")
    return float(value)


def _validate_duration(value: Any) -> float:
    return _validate_float(value, "shadow duration_seconds", 0.0, 3600.0)


def _reject_duplicate_keys(pairs: list[tuple[str, Any]]) -> dict[str, Any]:
    result: dict[str, Any] = {}
    for key, value in pairs:
        if key in result:
            raise ValueError(f"duplicate JSON key: {key}")
        result[key] = value
    return result


def _config_from_args(args: argparse.Namespace, token: str) -> ReadOnlyDirectTrafficProviderConfig:
    return ReadOnlyDirectTrafficProviderConfig(
        name=args.provider_name,
        base_url=args.provider_base_url,
        allowed_host=args.allowed_host,
        provider_instance=args.provider_instance,
        environment_sha256=environment_sha256(args.environment_id),
        token=token,
        state_path=getattr(args, "state_path", "/v1/direct-traffic/state"),
        timeout_seconds=getattr(args, "timeout_seconds", 5.0),
        max_attempts=getattr(args, "max_attempts", 2),
        retry_base_seconds=getattr(args, "retry_base_seconds", 0.25),
        max_snapshot_age_seconds=getattr(args, "max_snapshot_age_seconds", 90),
    )


def _add_binding_arguments(parser: argparse.ArgumentParser) -> None:
    parser.add_argument("--history-ledger", type=Path, required=True)
    parser.add_argument("--environment-id", required=True)
    parser.add_argument("--provider-name", required=True)
    parser.add_argument("--provider-base-url", required=True)
    parser.add_argument("--allowed-host", required=True)
    parser.add_argument("--provider-instance", required=True)
    parser.add_argument("--state-path", default="/v1/direct-traffic/state")


def main(argv: Sequence[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        description="Read and compare direct traffic state without platform writes"
    )
    subparsers = parser.add_subparsers(dest="command", required=True)
    check = subparsers.add_parser("check")
    _add_binding_arguments(check)
    check.add_argument("--sandbox-ledger", type=Path, required=True)
    check.add_argument("--timeout-seconds", type=float, default=5.0)
    check.add_argument("--max-attempts", type=int, default=2)
    check.add_argument("--retry-base-seconds", type=float, default=0.25)
    check.add_argument("--max-snapshot-age-seconds", type=int, default=90)
    check.add_argument("--canary-lease-root", type=Path, default=DEFAULT_CANARY_LEASE_ROOT)
    check.add_argument("--require-match", action="store_true")
    status = subparsers.add_parser("status")
    _add_binding_arguments(status)
    args = parser.parse_args(argv)
    lease = None
    try:
        token = os.environ.get(TOKEN_ENV, "")
        config = _config_from_args(args, token or "x" * 16)
        if args.command == "check" and not token:
            raise DirectTrafficShadowConfigError(f"{TOKEN_ENV} is required")
        adapter = ReadOnlyDirectTrafficProviderAdapter(config)
        history = DirectTrafficShadowHistory(
            args.history_ledger,
            adapter.observer_instance_sha256,
            adapter.provider_instance_sha256,
            adapter.config.environment_sha256,
            create=args.command == "check",
        )
        if args.command == "status":
            print(json.dumps(history.status(), sort_keys=True))
            return 0
        lease = try_acquire_canary_lease(args.canary_lease_root, args.environment_id)
        if lease is None:
            raise DirectTrafficShadowError("environment lease is already held")
        sandbox = SQLiteDirectTrafficSandbox(
            args.sandbox_ledger,
            adapter.config.environment_sha256,
            args.provider_instance,
        )
        report = run_shadow_comparison(sandbox, adapter, history)
        print(json.dumps(report, sort_keys=True))
        return int(bool(args.require_match and report["outcome"] != "match"))
    except (
        DirectTrafficShadowError,
        DirectTrafficProviderError,
        DirectTrafficError,
        ReleaseGateError,
        ReleaseGateHistoryError,
        OSError,
        sqlite3.Error,
    ) as exc:
        print(f"direct traffic shadow error: {exc}", file=sys.stderr)
        return 1
    finally:
        if lease is not None:
            lease.release()


if __name__ == "__main__":
    raise SystemExit(main())
