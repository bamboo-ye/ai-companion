from __future__ import annotations

import argparse
import json
import os
import secrets
import signal
import sqlite3
import sys
import time
from dataclasses import dataclass
from datetime import UTC, datetime
from http import HTTPStatus
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from threading import Event, RLock, Thread
from typing import Any, Callable, Mapping, Sequence

from .deployment_controller import (
    DeploymentControllerConfigError,
    DeploymentControllerError,
    DeploymentLedger,
    _format_timestamp,
    _normalize_now,
    _parse_timestamp,
    _validate_digest,
)
from .deployment_resolution import (
    DeploymentResolutionError,
    _ensure_private_state_directory,
    _read_contract,
    _validate_private_state_directory,
)
from .deployment_scheduler import (
    DeploymentSchedulerConfigError,
    DeploymentSchedulerError,
    SQLiteSandboxAdapter,
    _certification_key_from_environment,
    find_adapter_certification_path,
    _validate_scheduler_run_contract,
    run_certified_scheduler_once,
    verify_adapter_certification,
)


SERVICE_STATE_SCHEMA_VERSION = "observability-deployment-scheduler-state-v1"
MAX_CONSECUTIVE_FAILURES = 100
MAX_LISTEN_PORT = 65535
_ALLOWED_LISTEN_HOSTS = {"127.0.0.1", "0.0.0.0", "::1"}
_EXPECTED_SERVICE_ERRORS = (
    DeploymentSchedulerError,
    DeploymentControllerError,
    DeploymentResolutionError,
    OSError,
    sqlite3.Error,
)


class DeploymentSchedulerServiceError(ValueError):
    pass


class DeploymentSchedulerServiceConfigError(DeploymentSchedulerServiceError):
    pass


@dataclass(frozen=True)
class SchedulerServiceConfig:
    interval_seconds: int = 5
    limit: int = 100
    lease_seconds: int = 30
    max_consecutive_failures: int = 3
    listen_host: str = "127.0.0.1"
    listen_port: int = 9465


class SchedulerRuntimeState:
    def __init__(
        self,
        state_dir: Path,
        adapter: SQLiteSandboxAdapter,
        certification: Mapping[str, Any],
        *,
        now: datetime | None = None,
    ) -> None:
        current_time = _normalize_now(now)
        _ensure_private_state_directory(state_dir)
        self.path = state_dir / (
            f"scheduler-state-{certification['certification_id']}.json"
        )
        self._lock = RLock()
        self._up = False
        self._ready = False
        self._service_started_at = _format_timestamp(current_time)
        self._adapter = adapter.name
        self._implementation_version = adapter.implementation_version
        self._provider_instance_sha256 = adapter.provider_instance_sha256
        self._certification_id = str(certification["certification_id"])
        self._certification_expires_at = str(certification["expires_at"])
        self._successful_runs = 0
        self._failed_runs = 0
        self._consecutive_failures = 0
        self._last_success_at: str | None = None
        self._last_error_at: str | None = None
        self._last_error_code: str | None = None
        self._last_run_duration_seconds: float | None = None
        self._last_report: dict[str, Any] | None = None
        if self.path.exists() or self.path.is_symlink():
            self._restore(_read_contract(self.path, _validate_service_state_contract))
        else:
            self._persist(current_time)

    def set_up(self, value: bool) -> None:
        with self._lock:
            self._up = value

    def record_success(
        self,
        report: Mapping[str, Any],
        duration_seconds: float,
        now: datetime,
    ) -> None:
        validated_report = _validate_scheduler_run_contract(report)
        duration = _validate_duration(duration_seconds)
        current_time = _normalize_now(now)
        with self._lock:
            self._successful_runs += 1
            self._consecutive_failures = 0
            self._ready = True
            self._last_success_at = _format_timestamp(current_time)
            self._last_error_code = None
            self._last_run_duration_seconds = duration
            self._last_report = validated_report
            self._persist(current_time)

    def record_failure(
        self,
        error_code: str,
        duration_seconds: float,
        now: datetime,
    ) -> None:
        code = _validate_error_code(error_code)
        duration = _validate_duration(duration_seconds)
        current_time = _normalize_now(now)
        with self._lock:
            self._failed_runs += 1
            self._consecutive_failures += 1
            self._ready = False
            self._last_error_at = _format_timestamp(current_time)
            self._last_error_code = code
            self._last_run_duration_seconds = duration
            self._persist(current_time)

    def snapshot(self) -> dict[str, Any]:
        with self._lock:
            result = self._contract(datetime.now(UTC))
            result["up"] = self._up
            result["ready"] = self._ready
            return result

    def metrics_text(self) -> str:
        snapshot = self.snapshot()
        report = snapshot["last_report"]
        after: Mapping[str, Any] = report["after"] if report is not None else {}
        expiry = _parse_timestamp(
            str(snapshot["certification_expires_at"]),
            "certification_expires_at",
        ).timestamp()
        last_success = _timestamp_or_zero(snapshot["last_success_at"])
        duration = snapshot["last_run_duration_seconds"] or 0.0
        values = {
            "ai_companion_deployment_scheduler_up": int(bool(snapshot["up"])),
            "ai_companion_deployment_scheduler_ready": int(bool(snapshot["ready"])),
            "ai_companion_deployment_scheduler_consecutive_failures": int(
                snapshot["consecutive_failures"]
            ),
            "ai_companion_deployment_scheduler_last_success_timestamp_seconds": last_success,
            "ai_companion_deployment_scheduler_last_run_duration_seconds": duration,
            "ai_companion_deployment_scheduler_certification_expiry_timestamp_seconds": expiry,
            "ai_companion_deployment_scheduler_accepted_pending": int(
                after.get("accepted_pending", 0)
            ),
            "ai_companion_deployment_scheduler_due": int(after.get("due", 0)),
            "ai_companion_deployment_scheduler_overdue_deadline": int(
                after.get("overdue_deadline", 0)
            ),
            "ai_companion_deployment_scheduler_indeterminate": int(
                after.get("indeterminate", 0)
            ),
        }
        lines = [
            "# TYPE ai_companion_deployment_scheduler_up gauge",
            f"ai_companion_deployment_scheduler_up {values['ai_companion_deployment_scheduler_up']}",
            "# TYPE ai_companion_deployment_scheduler_ready gauge",
            f"ai_companion_deployment_scheduler_ready {values['ai_companion_deployment_scheduler_ready']}",
            "# TYPE ai_companion_deployment_scheduler_runs_total counter",
            (
                "ai_companion_deployment_scheduler_runs_total{outcome=\"success\"} "
                f"{snapshot['successful_runs']}"
            ),
            (
                "ai_companion_deployment_scheduler_runs_total{outcome=\"error\"} "
                f"{snapshot['failed_runs']}"
            ),
        ]
        for name, value in values.items():
            if name in {
                "ai_companion_deployment_scheduler_up",
                "ai_companion_deployment_scheduler_ready",
            }:
                continue
            lines.extend((f"# TYPE {name} gauge", f"{name} {value}"))
        return "\n".join(lines) + "\n"

    def _restore(self, value: Mapping[str, Any]) -> None:
        expected_bindings = {
            "adapter": self._adapter,
            "implementation_version": self._implementation_version,
            "provider_instance_sha256": self._provider_instance_sha256,
            "certification_id": self._certification_id,
            "certification_expires_at": self._certification_expires_at,
        }
        if any(value.get(field) != expected for field, expected in expected_bindings.items()):
            raise DeploymentSchedulerServiceError(
                "scheduler state does not match the certified adapter"
            )
        self._successful_runs = int(value["successful_runs"])
        self._failed_runs = int(value["failed_runs"])
        self._consecutive_failures = int(value["consecutive_failures"])
        self._last_success_at = value["last_success_at"]
        self._last_error_at = value["last_error_at"]
        self._last_error_code = value["last_error_code"]
        self._last_run_duration_seconds = value["last_run_duration_seconds"]
        self._last_report = value["last_report"]

    def _contract(self, now: datetime) -> dict[str, Any]:
        return {
            "schema_version": SERVICE_STATE_SCHEMA_VERSION,
            "adapter": self._adapter,
            "implementation_version": self._implementation_version,
            "provider_instance_sha256": self._provider_instance_sha256,
            "certification_id": self._certification_id,
            "certification_expires_at": self._certification_expires_at,
            "service_started_at": self._service_started_at,
            "updated_at": _format_timestamp(now),
            "successful_runs": self._successful_runs,
            "failed_runs": self._failed_runs,
            "consecutive_failures": self._consecutive_failures,
            "last_success_at": self._last_success_at,
            "last_error_at": self._last_error_at,
            "last_error_code": self._last_error_code,
            "last_run_duration_seconds": self._last_run_duration_seconds,
            "last_report": self._last_report,
        }

    def _persist(self, now: datetime) -> None:
        _write_atomic_private_json(self.path, self._contract(now))


class SchedulerRunner:
    def __init__(
        self,
        run_once: Callable[[datetime], dict[str, Any]],
        state: SchedulerRuntimeState,
        *,
        monotonic: Callable[[], float] = time.monotonic,
    ) -> None:
        self._run_once = run_once
        self._state = state
        self._monotonic = monotonic

    def run_iteration(self, now: datetime | None = None) -> bool:
        current_time = _normalize_now(now)
        started = self._monotonic()
        try:
            report = _validate_scheduler_run_contract(self._run_once(current_time))
        except _EXPECTED_SERVICE_ERRORS as exc:
            duration = max(0.0, self._monotonic() - started)
            self._state.record_failure(_classify_error(exc), duration, current_time)
            return False
        duration = max(0.0, self._monotonic() - started)
        self._state.record_success(report, duration, current_time)
        return True


def run_scheduler_service_loop(
    runner: SchedulerRunner,
    state: SchedulerRuntimeState,
    config: SchedulerServiceConfig,
    stop_event: Event,
    *,
    now: Callable[[], datetime] = lambda: datetime.now(UTC),
    wait: Callable[[float], bool] | None = None,
) -> int:
    validated = _validate_service_config(config)
    wait_for_stop = wait or stop_event.wait
    while not stop_event.is_set():
        success = runner.run_iteration(now())
        snapshot = state.snapshot()
        if not success and int(snapshot["consecutive_failures"]) >= (
            validated.max_consecutive_failures
        ):
            return 1
        if wait_for_stop(float(validated.interval_seconds)):
            break
    return 0


def serve_scheduler(
    runner: SchedulerRunner,
    state: SchedulerRuntimeState,
    config: SchedulerServiceConfig,
    stop_event: Event,
) -> int:
    validated = _validate_service_config(config)
    handler = _handler_factory(state)
    server = ThreadingHTTPServer(
        (validated.listen_host, validated.listen_port),
        handler,
    )
    thread = Thread(target=server.serve_forever, name="scheduler-health", daemon=True)
    state.set_up(True)
    thread.start()
    try:
        return run_scheduler_service_loop(runner, state, validated, stop_event)
    finally:
        state.set_up(False)
        server.shutdown()
        server.server_close()
        thread.join(timeout=5.0)


def main(argv: Sequence[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        description="Run the certified local deployment scheduler sandbox service"
    )
    parser.add_argument("--ledger", type=Path, required=True)
    parser.add_argument("--provider-ledger", type=Path, required=True)
    certification_group = parser.add_mutually_exclusive_group(required=True)
    certification_group.add_argument("--certification", type=Path)
    certification_group.add_argument("--certification-dir", type=Path)
    parser.add_argument("--certification-nonce")
    parser.add_argument("--state-dir", type=Path, required=True)
    parser.add_argument("--interval-seconds", type=int, default=5)
    parser.add_argument("--limit", type=int, default=100)
    parser.add_argument("--lease-seconds", type=int, default=30)
    parser.add_argument("--max-consecutive-failures", type=int, default=3)
    parser.add_argument("--listen-host", default="127.0.0.1")
    parser.add_argument("--listen-port", type=int, default=9465)
    args = parser.parse_args(argv)
    stop_event = Event()
    previous_handlers: dict[signal.Signals, Any] = {}
    try:
        key, key_id = _certification_key_from_environment()
        adapter = SQLiteSandboxAdapter(args.provider_ledger)
        certification_path = args.certification
        if args.certification_dir is not None:
            if args.certification_nonce is None:
                raise DeploymentSchedulerServiceConfigError(
                    "certification nonce is required with certification directory"
                )
            certification_path = find_adapter_certification_path(
                args.certification_dir,
                adapter,
                key,
                key_id,
                args.certification_nonce,
            )
        elif args.certification_nonce is not None:
            raise DeploymentSchedulerServiceConfigError(
                "certification nonce requires a certification directory"
            )
        if certification_path is None:
            raise DeploymentSchedulerServiceConfigError(
                "certification path could not be resolved"
            )
        certification = verify_adapter_certification(
            certification_path,
            adapter,
            key,
            key_id,
        )
        ledger = DeploymentLedger(args.ledger, create=False)
        state = SchedulerRuntimeState(
            args.state_dir,
            adapter,
            certification,
        )
        config = _validate_service_config(SchedulerServiceConfig(
            interval_seconds=args.interval_seconds,
            limit=args.limit,
            lease_seconds=args.lease_seconds,
            max_consecutive_failures=args.max_consecutive_failures,
            listen_host=args.listen_host,
            listen_port=args.listen_port,
        ))

        def run_once(now: datetime) -> dict[str, Any]:
            return run_certified_scheduler_once(
                ledger,
                adapter,
                certification_path,
                key,
                key_id,
                limit=config.limit,
                lease_seconds=config.lease_seconds,
                now=now,
            )

        runner = SchedulerRunner(run_once, state)
        previous_handlers = _install_signal_handlers(stop_event)
        return serve_scheduler(runner, state, config, stop_event)
    except (
        DeploymentSchedulerServiceConfigError,
        DeploymentSchedulerConfigError,
        DeploymentControllerConfigError,
    ) as exc:
        print(f"deployment scheduler service configuration error: {exc}", file=sys.stderr)
        return 2
    except (
        DeploymentSchedulerServiceError,
        DeploymentSchedulerError,
        DeploymentControllerError,
        DeploymentResolutionError,
        OSError,
        sqlite3.Error,
    ) as exc:
        print(f"deployment scheduler service error: {exc}", file=sys.stderr)
        return 1
    finally:
        _restore_signal_handlers(previous_handlers)


def _handler_factory(
    state: SchedulerRuntimeState,
) -> type[BaseHTTPRequestHandler]:
    class SchedulerHandler(BaseHTTPRequestHandler):
        def do_GET(self) -> None:
            status, body, content_type = _http_response(self.path, state)
            self._respond(status, body, content_type)

        def log_message(self, _format: str, *args: object) -> None:
            del args

        def _respond(
            self,
            status: HTTPStatus,
            body: str,
            content_type: str = "application/json",
        ) -> None:
            content = (body + "\n").encode("utf-8")
            self.send_response(status)
            self.send_header("Content-Type", content_type)
            self.send_header("Content-Length", str(len(content)))
            self.send_header("Cache-Control", "no-store")
            self.end_headers()
            self.wfile.write(content)

    return SchedulerHandler


def _http_response(
    path: str,
    state: SchedulerRuntimeState,
) -> tuple[HTTPStatus, str, str]:
    if path == "/metrics":
        return HTTPStatus.OK, state.metrics_text(), "text/plain; version=0.0.4"
    snapshot = state.snapshot()
    if path == "/healthz":
        status = HTTPStatus.OK if snapshot["up"] else HTTPStatus.SERVICE_UNAVAILABLE
        return status, json.dumps({"status": "up" if snapshot["up"] else "down"}), "application/json"
    if path == "/readyz":
        status = HTTPStatus.OK if snapshot["ready"] else HTTPStatus.SERVICE_UNAVAILABLE
        body = json.dumps({"status": "ready" if snapshot["ready"] else "not_ready"})
        return status, body, "application/json"
    return HTTPStatus.NOT_FOUND, json.dumps({"error": "not_found"}), "application/json"


def _validate_service_config(value: SchedulerServiceConfig) -> SchedulerServiceConfig:
    for field, item, minimum, maximum in (
        ("interval_seconds", value.interval_seconds, 1, 15 * 60),
        ("limit", value.limit, 1, 1000),
        ("lease_seconds", value.lease_seconds, 1, 5 * 60),
        (
            "max_consecutive_failures",
            value.max_consecutive_failures,
            1,
            MAX_CONSECUTIVE_FAILURES,
        ),
        ("listen_port", value.listen_port, 1024, MAX_LISTEN_PORT),
    ):
        if isinstance(item, bool) or not isinstance(item, int) or not minimum <= item <= maximum:
            raise DeploymentSchedulerServiceConfigError(
                f"scheduler service {field} is invalid"
            )
    if value.listen_host not in _ALLOWED_LISTEN_HOSTS:
        raise DeploymentSchedulerServiceConfigError(
            "scheduler service listen_host is invalid"
        )
    return value


def _validate_service_state_contract(value: Any) -> dict[str, Any]:
    fields = {
        "schema_version",
        "adapter",
        "implementation_version",
        "provider_instance_sha256",
        "certification_id",
        "certification_expires_at",
        "service_started_at",
        "updated_at",
        "successful_runs",
        "failed_runs",
        "consecutive_failures",
        "last_success_at",
        "last_error_at",
        "last_error_code",
        "last_run_duration_seconds",
        "last_report",
    }
    if not isinstance(value, Mapping) or set(value) != fields:
        raise DeploymentSchedulerServiceError("scheduler service state is invalid")
    result = dict(value)
    if result["schema_version"] != SERVICE_STATE_SCHEMA_VERSION:
        raise DeploymentSchedulerServiceError(
            "scheduler service state schema_version is invalid"
        )
    for field in (
        "adapter",
        "implementation_version",
        "provider_instance_sha256",
        "certification_id",
        "certification_expires_at",
        "service_started_at",
        "updated_at",
    ):
        if not isinstance(result[field], str) or not result[field]:
            raise DeploymentSchedulerServiceError(
                f"scheduler service state {field} is invalid"
            )
    _validate_digest(
        str(result["provider_instance_sha256"]),
        "provider_instance_sha256",
    )
    certification_id = str(result["certification_id"])
    if not certification_id.startswith("adapter-cert-") or len(certification_id) != 45:
        raise DeploymentSchedulerServiceError(
            "scheduler service state certification_id is invalid"
        )
    for field in (
        "certification_expires_at",
        "service_started_at",
        "updated_at",
    ):
        _parse_timestamp(str(result[field]), field)
    for field in ("successful_runs", "failed_runs", "consecutive_failures"):
        item = result[field]
        if isinstance(item, bool) or not isinstance(item, int) or item < 0:
            raise DeploymentSchedulerServiceError(
                f"scheduler service state {field} is invalid"
            )
    if result["consecutive_failures"] > result["failed_runs"]:
        raise DeploymentSchedulerServiceError(
            "scheduler service state failure counters are inconsistent"
        )
    for field in ("last_success_at", "last_error_at"):
        item = result[field]
        if item is not None:
            if not isinstance(item, str):
                raise DeploymentSchedulerServiceError(
                    f"scheduler service state {field} is invalid"
                )
            _parse_timestamp(item, field)
    if result["last_error_code"] is not None:
        _validate_error_code(result["last_error_code"])
    if result["last_run_duration_seconds"] is not None:
        _validate_duration(result["last_run_duration_seconds"])
    if result["last_report"] is not None:
        result["last_report"] = _validate_scheduler_run_contract(result["last_report"])
        report = result["last_report"]
        if (
            report["adapter"] != result["adapter"]
            or report["implementation_version"] != result["implementation_version"]
            or report["certification_id"] != result["certification_id"]
        ):
            raise DeploymentSchedulerServiceError(
                "scheduler service state report binding is invalid"
            )
    if (result["successful_runs"] == 0) != (result["last_report"] is None):
        raise DeploymentSchedulerServiceError(
            "scheduler service state success counters are inconsistent"
        )
    return result


def _write_atomic_private_json(path: Path, value: Mapping[str, Any]) -> None:
    _validate_private_state_directory(path.parent)
    if path.exists() or path.is_symlink():
        _read_contract(path, _validate_service_state_contract)
    temporary = path.parent / f".{path.name}.{secrets.token_hex(8)}.tmp"
    descriptor = os.open(
        temporary,
        os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_NOFOLLOW", 0),
        0o600,
    )
    try:
        content = json.dumps(value, ensure_ascii=False, indent=2).encode("utf-8") + b"\n"
        with os.fdopen(descriptor, "wb", closefd=False) as output:
            output.write(content)
            output.flush()
            os.fsync(output.fileno())
        os.replace(temporary, path)
        os.chmod(path, 0o600)
        directory_descriptor = os.open(path.parent, os.O_RDONLY)
        try:
            os.fsync(directory_descriptor)
        finally:
            os.close(directory_descriptor)
    except BaseException:
        try:
            temporary.unlink()
        except OSError:
            pass
        raise
    finally:
        os.close(descriptor)


def _validate_duration(value: Any) -> float:
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        raise DeploymentSchedulerServiceError("scheduler run duration is invalid")
    result = float(value)
    if result < 0 or result > 24 * 60 * 60 or result != result:
        raise DeploymentSchedulerServiceError("scheduler run duration is invalid")
    return result


def _validate_error_code(value: Any) -> str:
    allowed = {
        "controller_error",
        "filesystem_error",
        "scheduler_error",
        "sqlite_error",
    }
    if not isinstance(value, str) or value not in allowed:
        raise DeploymentSchedulerServiceError("scheduler error code is invalid")
    return value


def _classify_error(error: BaseException) -> str:
    if isinstance(error, DeploymentSchedulerError):
        return "scheduler_error"
    if isinstance(error, DeploymentControllerError):
        return "controller_error"
    if isinstance(error, DeploymentResolutionError):
        return "filesystem_error"
    if isinstance(error, sqlite3.Error):
        return "sqlite_error"
    return "filesystem_error"


def _timestamp_or_zero(value: Any) -> float:
    if value is None:
        return 0.0
    if not isinstance(value, str):
        raise DeploymentSchedulerServiceError("scheduler timestamp is invalid")
    return _parse_timestamp(value, "scheduler timestamp").timestamp()


def _install_signal_handlers(stop_event: Event) -> dict[signal.Signals, Any]:
    previous: dict[signal.Signals, Any] = {}

    def stop(_signum: int, _frame: Any) -> None:
        stop_event.set()

    for selected_signal in (signal.SIGINT, signal.SIGTERM):
        previous[selected_signal] = signal.getsignal(selected_signal)
        signal.signal(selected_signal, stop)
    return previous


def _restore_signal_handlers(previous: Mapping[signal.Signals, Any]) -> None:
    for selected_signal, handler in previous.items():
        signal.signal(selected_signal, handler)


if __name__ == "__main__":
    raise SystemExit(main())
