from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import sqlite3
import stat
import sys
from contextlib import closing
from datetime import UTC, datetime
from pathlib import Path
from typing import Any, Mapping, Sequence

from .deployment_controller import (
    _canonical_json,
    _ensure_private_database,
    _format_timestamp,
    _parse_timestamp,
    _validate_private_database,
)


HISTORY_SCHEMA_VERSION = 1
HISTORY_REPORT_SCHEMA_VERSION = "observability-release-gate-history-report-v1"
MAX_HISTORY_EVENTS = 100_000
MAX_GATE_ARTIFACT_BYTES = 4 * 1024 * 1024
_CHAIN_DOMAIN = b"ai-companion/observability-release-gate-history/v1"
_ENVIRONMENT_DOMAIN = b"ai-companion/observability-canary-environment/v1\0"
_ZERO_CHAIN = "0" * 64
_DIGEST_RE = re.compile(r"[0-9a-f]{64}")
_RUN_ID_RE = re.compile(r"release-gate-[A-Za-z0-9._-]{1,160}")
_METADATA_COLUMNS = {
    "singleton",
    "schema_version",
    "environment_sha256",
    "created_at",
}
_EVENT_COLUMNS = {
    "sequence",
    "event_id",
    "run_id",
    "observed_at",
    "decision",
    "canary_name",
    "canary_status",
    "canary_duration_ms",
    "violations_count",
    "gate_report_sha256",
    "gate_bundle_sha256",
    "report_json_sha256",
    "report_json",
    "previous_chain_sha256",
    "chain_sha256",
}
_TRIGGERS = {
    "release_gate_history_events_no_update",
    "release_gate_history_events_no_delete",
}


class ReleaseGateHistoryError(ValueError):
    pass


class ReleaseGateHistory:
    def __init__(self, path: Path, environment_id: str, *, create: bool = True) -> None:
        self.path = path
        self.environment_sha256 = environment_sha256(environment_id)
        if create:
            _ensure_private_database(path)
            self._initialize()
        else:
            _validate_private_database(path)
            self._validate_schema()
        self._validate_binding()

    def record(self, gate_dir: Path, report: Mapping[str, Any]) -> bool:
        run_id = gate_dir.name
        if _RUN_ID_RE.fullmatch(run_id) is None:
            raise ReleaseGateHistoryError("release gate history run ID is invalid")
        validated = _validate_gate_report(report)
        report_path = gate_dir / "gate-report.json"
        raw = _read_private_file(report_path)
        try:
            disk_report = json.loads(raw)
        except (UnicodeDecodeError, json.JSONDecodeError) as exc:
            raise ReleaseGateHistoryError("release gate history report JSON is invalid") from exc
        if disk_report != validated:
            raise ReleaseGateHistoryError("release gate history report does not match memory")
        gate_report_sha256 = _digest(raw)
        gate_bundle_sha256 = _bundle_digest(gate_dir)
        report_json = _canonical_json(validated).decode("utf-8")
        report_json_sha256 = _digest(report_json.encode("utf-8"))
        canary = validated["canary"]
        observed_at = str(validated["generated_at"])
        base_payload: dict[str, Any] = {
            "environment_sha256": self.environment_sha256,
            "run_id": run_id,
            "observed_at": observed_at,
            "decision": validated["decision"],
            "canary_name": canary["name"],
            "canary_status": canary["status"],
            "canary_duration_ms": canary["duration_ms"],
            "violations_count": len(validated["violations"]),
            "gate_report_sha256": gate_report_sha256,
            "gate_bundle_sha256": gate_bundle_sha256,
            "report_json_sha256": report_json_sha256,
        }
        event_id = f"gate-event-{_digest(_canonical_json(base_payload))[:32]}"
        with closing(self._connect()) as connection, connection:
            connection.execute("BEGIN IMMEDIATE")
            existing = connection.execute(
                "SELECT * FROM release_gate_history_events WHERE run_id = ?",
                (run_id,),
            ).fetchone()
            last = connection.execute(
                """
                SELECT sequence, chain_sha256 FROM release_gate_history_events
                ORDER BY sequence DESC LIMIT 1
                """
            ).fetchone()
            previous_chain = str(last["chain_sha256"]) if last is not None else _ZERO_CHAIN
            sequence = int(last["sequence"]) + 1 if last is not None else 1
            event_payload = dict(base_payload)
            event_payload.update(
                {
                    "sequence": sequence,
                    "event_id": event_id,
                    "previous_chain_sha256": previous_chain,
                }
            )
            chain_sha256 = _chain_digest(previous_chain, event_payload)
            if existing is not None:
                current = self._validate_event_row(dict(existing))
                expected = {
                    **{
                        field: value
                        for field, value in base_payload.items()
                        if field != "environment_sha256"
                    },
                    "event_id": event_id,
                    "report_json": report_json,
                }
                if any(current.get(field) != value for field, value in expected.items()):
                    raise ReleaseGateHistoryError(
                        "release gate history run ID is already bound to another event"
                    )
                return False
            connection.execute(
                """
                INSERT INTO release_gate_history_events (
                    sequence, event_id, run_id, observed_at, decision,
                    canary_name, canary_status, canary_duration_ms, violations_count,
                    gate_report_sha256, gate_bundle_sha256, report_json,
                    report_json_sha256, previous_chain_sha256, chain_sha256
                ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                """,
                (
                    sequence,
                    event_id,
                    run_id,
                    observed_at,
                    validated["decision"],
                    canary["name"],
                    canary["status"],
                    canary["duration_ms"],
                    len(validated["violations"]),
                    gate_report_sha256,
                    gate_bundle_sha256,
                    report_json,
                    report_json_sha256,
                    previous_chain,
                    chain_sha256,
                ),
            )
        return True

    def events(self) -> list[dict[str, Any]]:
        with closing(self._connect(read_only=True)) as connection:
            count = int(
                connection.execute("SELECT COUNT(*) FROM release_gate_history_events").fetchone()[0]
            )
            if count > MAX_HISTORY_EVENTS:
                raise ReleaseGateHistoryError("release gate history exceeds verification limit")
            rows = connection.execute(
                "SELECT * FROM release_gate_history_events ORDER BY sequence"
            ).fetchall()
        events = [self._validate_event_row(dict(row)) for row in rows]
        previous = _ZERO_CHAIN
        for expected_sequence, event in enumerate(events, start=1):
            if event["sequence"] != expected_sequence:
                raise ReleaseGateHistoryError("release gate history sequence is not contiguous")
            if event["previous_chain_sha256"] != previous:
                raise ReleaseGateHistoryError("release gate history previous chain is invalid")
            if event["chain_sha256"] != _chain_digest(
                previous,
                self._event_payload(event),
            ):
                raise ReleaseGateHistoryError("release gate history hash chain is invalid")
            previous = str(event["chain_sha256"])
        return events

    def trend(self, limit: int = 20) -> dict[str, Any]:
        if isinstance(limit, bool) or not 1 <= limit <= 1000:
            raise ReleaseGateHistoryError("release gate history limit must be 1 to 1000")
        all_events = self.events()
        selected = all_events[-limit:]
        durations = sorted(int(item["canary_duration_ms"]) for item in selected)
        wake_latencies: list[float] = []
        python_executions = 0
        model_calls = 0
        concurrent_rejections = 0
        direct_canary_runs = 0
        direct_cases = 0
        direct_rates: list[float] = []
        single_call_rates: list[float] = []
        quality_rates: list[float] = []
        content_rates: list[float] = []
        direct_durations: list[float] = []
        direct_repair_attempts = 0
        direct_repair_successes = 0
        duplicate_violations = 0
        response_quality_failures = 0
        for event in selected:
            report = event["report"]
            summary = report["canary"].get("report")
            if isinstance(summary, Mapping):
                summary_value = summary.get("summary")
                if isinstance(summary_value, Mapping):
                    latency = summary_value.get("wake_latency_max_seconds")
                    if isinstance(latency, (int, float)) and not isinstance(latency, bool):
                        wake_latencies.append(float(latency))
                    python_executions += _nonnegative_int(
                        summary_value.get("python_executions"),
                        default=0,
                    )
                    model_calls += _nonnegative_int(summary_value.get("model_calls"), default=0)
                    if summary.get("schema_version") == "agent-direct-canary-report-v1":
                        direct_canary_runs += 1
                        direct_cases += _nonnegative_int(summary_value.get("cases"), default=0)
                        for field, target in (
                            ("direct_rate", direct_rates),
                            ("single_model_call_rate", single_call_rates),
                            ("quality_pass_rate", quality_rates),
                            ("content_pass_rate", content_rates),
                            ("duration_p95_ms", direct_durations),
                        ):
                            field_value = summary_value.get(field)
                            if isinstance(field_value, (int, float)) and not isinstance(
                                field_value, bool
                            ):
                                target.append(float(field_value))
                        direct_repair_attempts += _nonnegative_int(
                            summary_value.get("repair_attempted_runs"),
                            default=0,
                        )
                        direct_repair_successes += _nonnegative_int(
                            summary_value.get("repair_succeeded_runs"),
                            default=0,
                        )
                        duplicate_violations += _nonnegative_int(
                            summary_value.get("duplicate_violations"),
                            default=0,
                        )
                        response_quality_failures += _nonnegative_int(
                            summary_value.get("response_quality_failures"),
                            default=0,
                        )
            if any(
                isinstance(item, Mapping) and item.get("code") == "canary_concurrent_run"
                for item in report["violations"]
            ):
                concurrent_rejections += 1
        return {
            "schema_version": HISTORY_REPORT_SCHEMA_VERSION,
            "generated_at": _format_timestamp(datetime.now(UTC)),
            "environment_sha256": self.environment_sha256,
            "window": {
                "requested": limit,
                "selected": len(selected),
                "first_observed_at": selected[0]["observed_at"] if selected else None,
                "last_observed_at": selected[-1]["observed_at"] if selected else None,
                "history_chain_sha256": selected[-1]["chain_sha256"]
                if selected
                else _ZERO_CHAIN,
            },
            "summary": {
                "promote": sum(item["decision"] == "promote" for item in selected),
                "rollback": sum(item["decision"] == "rollback" for item in selected),
                "concurrent_rejections": concurrent_rejections,
                "canary_duration_p95_ms": _percentile(durations, 0.95),
                "wake_latency_max_p95_seconds": _percentile(wake_latencies, 0.95),
                "python_executions": python_executions,
                "model_calls": model_calls,
                "direct_canary_runs": direct_canary_runs,
                "direct_cases": direct_cases,
                "direct_rate_min": min(direct_rates, default=0.0),
                "single_model_call_rate_min": min(single_call_rates, default=0.0),
                "quality_pass_rate_min": min(quality_rates, default=0.0),
                "content_pass_rate_min": min(content_rates, default=0.0),
                "direct_duration_p95_ms": _percentile(direct_durations, 0.95),
                "direct_repair_attempted_runs": direct_repair_attempts,
                "direct_repair_succeeded_runs": direct_repair_successes,
                "direct_repair_success_rate": _rate(
                    direct_repair_successes,
                    direct_repair_attempts,
                    empty=1.0,
                ),
                "direct_duplicate_violations": duplicate_violations,
                "direct_response_quality_failures": response_quality_failures,
            },
            "runs": [
                {
                    "sequence": item["sequence"],
                    "run_id": item["run_id"],
                    "observed_at": item["observed_at"],
                    "decision": item["decision"],
                    "canary_status": item["canary_status"],
                    "canary_duration_ms": item["canary_duration_ms"],
                    "violations_count": item["violations_count"],
                    "chain_sha256": item["chain_sha256"],
                }
                for item in selected
            ],
        }

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
                connection.execute(
                    """
                    CREATE TABLE release_gate_history_metadata (
                        singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
                        schema_version INTEGER NOT NULL,
                        environment_sha256 TEXT NOT NULL,
                        created_at TEXT NOT NULL
                    )
                    """
                )
                connection.execute(
                    """
                    CREATE TABLE release_gate_history_events (
                        sequence INTEGER PRIMARY KEY,
                        event_id TEXT NOT NULL UNIQUE,
                        run_id TEXT NOT NULL UNIQUE,
                        observed_at TEXT NOT NULL,
                        decision TEXT NOT NULL CHECK (decision IN ('promote', 'rollback')),
                        canary_name TEXT NOT NULL,
                        canary_status TEXT NOT NULL,
                        canary_duration_ms INTEGER NOT NULL CHECK (canary_duration_ms >= 0),
                        violations_count INTEGER NOT NULL CHECK (violations_count >= 0),
                        gate_report_sha256 TEXT NOT NULL,
                        gate_bundle_sha256 TEXT NOT NULL,
                        report_json_sha256 TEXT NOT NULL,
                        report_json TEXT NOT NULL,
                        previous_chain_sha256 TEXT NOT NULL,
                        chain_sha256 TEXT NOT NULL
                    )
                    """
                )
                connection.execute(
                    """
                    CREATE TRIGGER release_gate_history_events_no_update
                    BEFORE UPDATE ON release_gate_history_events
                    BEGIN
                        SELECT RAISE(ABORT, 'release gate history is append-only');
                    END
                    """
                )
                connection.execute(
                    """
                    CREATE TRIGGER release_gate_history_events_no_delete
                    BEFORE DELETE ON release_gate_history_events
                    BEGIN
                        SELECT RAISE(ABORT, 'release gate history is append-only');
                    END
                    """
                )
                connection.execute(
                    """
                    INSERT INTO release_gate_history_metadata (
                        singleton, schema_version, environment_sha256, created_at
                    ) VALUES (1, ?, ?, ?)
                    """,
                    (
                        HISTORY_SCHEMA_VERSION,
                        self.environment_sha256,
                        _format_timestamp(datetime.now(UTC)),
                    ),
                )
                connection.execute(f"PRAGMA user_version = {HISTORY_SCHEMA_VERSION}")
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
                "release_gate_history_metadata",
                "release_gate_history_events",
            }:
                raise ReleaseGateHistoryError("release gate history schema is invalid")
            metadata_columns = {
                str(row[1])
                for row in connection.execute(
                    "PRAGMA table_info(release_gate_history_metadata)"
                ).fetchall()
            }
            event_columns = {
                str(row[1])
                for row in connection.execute(
                    "PRAGMA table_info(release_gate_history_events)"
                ).fetchall()
            }
            if metadata_columns != _METADATA_COLUMNS or event_columns != _EVENT_COLUMNS:
                raise ReleaseGateHistoryError("release gate history columns are invalid")
            triggers = {
                str(row[0])
                for row in connection.execute(
                    "SELECT name FROM sqlite_master WHERE type='trigger'"
                ).fetchall()
            }
            if triggers != _TRIGGERS:
                raise ReleaseGateHistoryError("release gate history triggers are invalid")
        finally:
            if owned:
                connection.close()

    def _validate_binding(self) -> None:
        with closing(self._connect(read_only=True)) as connection:
            rows = connection.execute("SELECT * FROM release_gate_history_metadata").fetchall()
        if len(rows) != 1:
            raise ReleaseGateHistoryError("release gate history metadata is invalid")
        row = dict(rows[0])
        if (
            row["singleton"] != 1
            or row["schema_version"] != HISTORY_SCHEMA_VERSION
            or row["environment_sha256"] != self.environment_sha256
        ):
            raise ReleaseGateHistoryError("release gate history environment binding is invalid")
        _parse_timestamp(str(row["created_at"]), "release gate history created_at")

    def _validate_event_row(self, value: Mapping[str, Any]) -> dict[str, Any]:
        if set(value) != _EVENT_COLUMNS:
            raise ReleaseGateHistoryError("release gate history event is invalid")
        event = dict(value)
        if (
            isinstance(event["sequence"], bool)
            or not isinstance(event["sequence"], int)
            or event["sequence"] < 1
            or not isinstance(event["event_id"], str)
            or re.fullmatch(r"gate-event-[0-9a-f]{32}", event["event_id"]) is None
            or not isinstance(event["run_id"], str)
            or _RUN_ID_RE.fullmatch(event["run_id"]) is None
        ):
            raise ReleaseGateHistoryError("release gate history event identity is invalid")
        _parse_timestamp(str(event["observed_at"]), "release gate history observed_at")
        for field in (
            "gate_report_sha256",
            "gate_bundle_sha256",
            "report_json_sha256",
            "previous_chain_sha256",
            "chain_sha256",
        ):
            if not isinstance(event[field], str) or _DIGEST_RE.fullmatch(event[field]) is None:
                raise ReleaseGateHistoryError(f"release gate history {field} is invalid")
        if event["decision"] not in {"promote", "rollback"}:
            raise ReleaseGateHistoryError("release gate history decision is invalid")
        if not isinstance(event["report_json"], str):
            raise ReleaseGateHistoryError("release gate history report is invalid")
        if _digest(event["report_json"].encode("utf-8")) != event["report_json_sha256"]:
            raise ReleaseGateHistoryError("release gate history report digest is invalid")
        try:
            report = _validate_gate_report(json.loads(event["report_json"]))
        except (UnicodeDecodeError, json.JSONDecodeError) as exc:
            raise ReleaseGateHistoryError("release gate history report JSON is invalid") from exc
        canary = report["canary"]
        expected = {
            "observed_at": report["generated_at"],
            "decision": report["decision"],
            "canary_name": canary["name"],
            "canary_status": canary["status"],
            "canary_duration_ms": canary["duration_ms"],
            "violations_count": len(report["violations"]),
        }
        if any(event[field] != expected[field] for field in expected):
            raise ReleaseGateHistoryError("release gate history report binding is invalid")
        event["report"] = report
        return event

    def _event_payload(self, event: Mapping[str, Any]) -> dict[str, Any]:
        return {
            "environment_sha256": self.environment_sha256,
            "sequence": event["sequence"],
            "event_id": event["event_id"],
            "run_id": event["run_id"],
            "observed_at": event["observed_at"],
            "decision": event["decision"],
            "canary_name": event["canary_name"],
            "canary_status": event["canary_status"],
            "canary_duration_ms": event["canary_duration_ms"],
            "violations_count": event["violations_count"],
            "gate_report_sha256": event["gate_report_sha256"],
            "gate_bundle_sha256": event["gate_bundle_sha256"],
            "report_json_sha256": event["report_json_sha256"],
            "previous_chain_sha256": event["previous_chain_sha256"],
        }

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
        if read_only:
            connection.execute("PRAGMA query_only = ON")
        else:
            connection.execute("PRAGMA synchronous = FULL")
        return connection


def environment_sha256(environment_id: str) -> str:
    if (
        not isinstance(environment_id, str)
        or not environment_id
        or len(environment_id) > 256
        or environment_id.strip() != environment_id
        or any(ord(character) < 32 for character in environment_id)
    ):
        raise ReleaseGateHistoryError("release gate environment ID is invalid")
    return hashlib.sha256(_ENVIRONMENT_DOMAIN + environment_id.encode("utf-8")).hexdigest()


def _validate_gate_report(value: Any) -> dict[str, Any]:
    if not isinstance(value, Mapping) or set(value) != {
        "schema_version",
        "generated_at",
        "decision",
        "source",
        "canary",
        "phases",
        "violations",
    }:
        raise ReleaseGateHistoryError("release gate history report contract is invalid")
    report = dict(value)
    if report.get("schema_version") != "observability-release-gate-report-v1":
        raise ReleaseGateHistoryError("release gate history report version is invalid")
    if report.get("decision") not in {"promote", "rollback"}:
        raise ReleaseGateHistoryError("release gate history decision is invalid")
    generated_at = report.get("generated_at")
    if not isinstance(generated_at, str):
        raise ReleaseGateHistoryError("release gate history timestamp is invalid")
    _parse_timestamp(generated_at, "release gate history generated_at")
    canary = report.get("canary")
    if not isinstance(canary, Mapping):
        raise ReleaseGateHistoryError("release gate history canary is invalid")
    duration = canary.get("duration_ms")
    if (
        not isinstance(canary.get("name"), str)
        or not isinstance(canary.get("status"), str)
        or not isinstance(duration, int)
        or isinstance(duration, bool)
        or duration < 0
    ):
        raise ReleaseGateHistoryError("release gate history canary is invalid")
    if not isinstance(report.get("violations"), list):
        raise ReleaseGateHistoryError("release gate history violations are invalid")
    return report


def _read_private_file(path: Path) -> bytes:
    flags = os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0)
    try:
        descriptor = os.open(path, flags)
    except OSError as exc:
        raise ReleaseGateHistoryError(f"cannot open release gate artifact: {exc}") from exc
    try:
        metadata = os.fstat(descriptor)
        if (
            not stat.S_ISREG(metadata.st_mode)
            or stat.S_IMODE(metadata.st_mode) != 0o600
            or metadata.st_nlink != 1
            or metadata.st_size <= 0
            or metadata.st_size > MAX_GATE_ARTIFACT_BYTES
            or (hasattr(os, "getuid") and metadata.st_uid != os.getuid())
        ):
            raise ReleaseGateHistoryError("release gate artifact is unsafe")
        content = b""
        while len(content) <= MAX_GATE_ARTIFACT_BYTES:
            chunk = os.read(descriptor, min(64 * 1024, MAX_GATE_ARTIFACT_BYTES + 1 - len(content)))
            if not chunk:
                break
            content += chunk
        if len(content) != metadata.st_size:
            raise ReleaseGateHistoryError("release gate artifact changed while reading")
        return content
    finally:
        os.close(descriptor)


def _bundle_digest(gate_dir: Path) -> str:
    try:
        entries = sorted(entry.name for entry in os.scandir(gate_dir))
    except OSError as exc:
        raise ReleaseGateHistoryError(f"cannot list release gate bundle: {exc}") from exc
    allowed = {
        "before.json",
        "after.json",
        "metrics-report.json",
        "metrics-report.xml",
        "gate-report.json",
        "gate-report.xml",
        "canary-report.json",
    }
    if not {"gate-report.json", "gate-report.xml"}.issubset(entries) or not set(entries).issubset(allowed):
        raise ReleaseGateHistoryError("release gate bundle artifact set is invalid")
    manifest = [
        {"name": name, "sha256": _digest(content), "size": len(content)}
        for name in entries
        for content in (_read_private_file(gate_dir / name),)
    ]
    return _digest(_canonical_json({"artifacts": manifest}))


def _chain_digest(previous: str, payload: Mapping[str, Any]) -> str:
    return _digest(_CHAIN_DOMAIN + previous.encode("ascii") + _canonical_json(payload))


def _digest(value: bytes) -> str:
    return hashlib.sha256(value).hexdigest()


def _nonnegative_int(value: Any, *, default: int) -> int:
    if isinstance(value, int) and not isinstance(value, bool) and value >= 0:
        return value
    return default


def _percentile(values: Sequence[float | int], quantile: float) -> float:
    if not values:
        return 0.0
    ordered = sorted(float(value) for value in values)
    index = max(0, min(len(ordered) - 1, int((len(ordered) * quantile) + 0.999999) - 1))
    return ordered[index]


def _rate(numerator: int, denominator: int, *, empty: float) -> float:
    return round(numerator / denominator, 6) if denominator else empty


def main(argv: Sequence[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Verify release gate history and report trends")
    parser.add_argument("--ledger", type=Path, required=True)
    parser.add_argument("--environment-id", required=True)
    parser.add_argument("--limit", type=int, default=20)
    parser.add_argument("--output", type=Path)
    args = parser.parse_args(argv)
    try:
        history = ReleaseGateHistory(args.ledger, args.environment_id, create=False)
        report = history.trend(args.limit)
        content = json.dumps(report, ensure_ascii=False, indent=2) + "\n"
        if args.output is None:
            print(content, end="")
        else:
            args.output.parent.mkdir(parents=True, exist_ok=True)
            args.output.write_text(content, encoding="utf-8")
            args.output.chmod(0o600)
    except (OSError, ReleaseGateHistoryError, ValueError) as exc:
        print(f"release gate history error: {exc}", file=sys.stderr)
        return 2
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
