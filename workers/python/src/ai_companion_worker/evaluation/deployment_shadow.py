from __future__ import annotations

import argparse
import json
import os
import re
import secrets
import signal
import sqlite3
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import dataclass, field
from datetime import UTC, datetime, timedelta
from http import HTTPStatus
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from threading import Event, RLock, Thread
from typing import Any, Callable, Literal, Mapping, Protocol, Sequence

from .deployment_controller import (
    DeploymentControllerError,
    DeploymentLedger,
    _canonical_json,
    _digest,
    _format_timestamp,
    _normalize_now,
    _parse_timestamp,
    _validate_digest,
)
from .deployment_resolution import (
    _ensure_private_state_directory,
    _read_contract,
    _validate_private_state_directory,
)


PROVIDER_READ_SCHEMA_VERSION = "observability-provider-read-v1"
SHADOW_REPORT_SCHEMA_VERSION = "observability-deployment-shadow-report-v1"
SHADOW_STATE_SCHEMA_VERSION = "observability-deployment-shadow-state-v1"
SHADOW_TOKEN_ENV = "OBSERVABILITY_SHADOW_PROVIDER_TOKEN"
IMPLEMENTATION_VERSION = "1.0.0"
MAX_RESPONSE_BYTES = 64 * 1024
MAX_PROVIDER_ATTEMPTS = 3
MAX_RETRY_DELAY_SECONDS = 5.0
MAX_CONSECUTIVE_FAILURES = 100
_IDENTIFIER_RE = re.compile(r"[A-Za-z0-9][A-Za-z0-9._-]{0,127}")
_ERROR_CODE_RE = re.compile(r"[a-z][a-z0-9_]{0,63}")
_EXTERNAL_ID_RE = re.compile(r"[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}")
_ENV_NAME_RE = re.compile(r"[A-Z][A-Z0-9_]{0,127}")
_ALLOWED_LISTEN_HOSTS = {"127.0.0.1", "0.0.0.0", "::1"}
_OUTCOMES = (
    "match",
    "provider_ahead",
    "provider_behind",
    "provider_missing",
    "external_id_mismatch",
    "status_mismatch",
    "ledger_indeterminate",
    "lookup_error",
)
_DRIFT_OUTCOMES = set(_OUTCOMES) - {"match", "lookup_error"}


class DeploymentShadowError(ValueError):
    pass


class DeploymentShadowConfigError(DeploymentShadowError):
    pass


class ShadowProviderError(RuntimeError):
    def __init__(
        self,
        code: str,
        *,
        retryable: bool = False,
        attempts: int = 1,
        retry_after: str | None = None,
    ) -> None:
        if _ERROR_CODE_RE.fullmatch(code) is None:
            raise ValueError("shadow provider error code is invalid")
        if isinstance(attempts, bool) or not isinstance(attempts, int) or attempts < 1:
            raise ValueError("shadow provider attempts is invalid")
        super().__init__(code)
        self.code = code
        self.retryable = retryable
        self.attempts = attempts
        self.retry_after = retry_after


@dataclass(frozen=True)
class ReadOnlyHTTPProviderConfig:
    name: str
    base_url: str
    allowed_host: str
    provider_instance: str
    token: str = field(repr=False)
    timeout_seconds: float = 5.0
    max_attempts: int = 2
    retry_base_seconds: float = 0.25
    max_response_bytes: int = MAX_RESPONSE_BYTES


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


class ShadowHistoryRecorder(Protocol):
    def record_success(
        self,
        report: Mapping[str, Any],
        duration_seconds: float,
        now: datetime,
    ) -> bool: ...

    def record_failure(
        self,
        error_code: str,
        duration_seconds: float,
        now: datetime,
    ) -> bool: ...


@dataclass(frozen=True)
class ProviderSnapshot:
    idempotency_key: str
    status: Literal["accepted", "completed", "failed"]
    external_operation_id: str
    updated_at: str


@dataclass(frozen=True)
class LookupObservation:
    snapshot: ProviderSnapshot | None
    attempts: int


@dataclass(frozen=True)
class ShadowServiceConfig:
    interval_seconds: int = 30
    limit: int = 100
    max_consecutive_failures: int = 3
    listen_host: str = "127.0.0.1"
    listen_port: int = 9466


class ReadOnlyHTTPProviderAdapter:
    implementation_version = IMPLEMENTATION_VERSION

    def __init__(
        self,
        config: ReadOnlyHTTPProviderConfig,
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
        identity = {
            "name": self.config.name,
            "base_url": self.config.base_url,
            "allowed_host": self.config.allowed_host,
            "provider_instance": self.config.provider_instance,
            "implementation_version": self.implementation_version,
        }
        return _digest(_canonical_json(identity))

    def lookup(
        self,
        idempotency_key: str,
        *,
        now: datetime | None = None,
    ) -> LookupObservation:
        _validate_digest(idempotency_key, "shadow idempotency_key")
        current_time = _normalize_now(now)
        encoded_key = urllib.parse.quote(idempotency_key, safe="")
        url = f"{self.config.base_url}/v1/deployments/by-idempotency-key/{encoded_key}"
        for attempt in range(1, self.config.max_attempts + 1):
            try:
                response = self._transport(
                    url,
                    self.config.token,
                    self.config.timeout_seconds,
                    self.config.max_response_bytes,
                )
                return self._interpret_response(
                    response,
                    url,
                    idempotency_key,
                    current_time,
                    attempt,
                )
            except ShadowProviderError as exc:
                if not exc.retryable or attempt >= self.config.max_attempts:
                    raise ShadowProviderError(
                        exc.code,
                        retryable=exc.retryable,
                        attempts=attempt,
                        retry_after=exc.retry_after,
                    ) from exc
                self._sleep(self._retry_delay(attempt, exc.retry_after))
            except (OSError, TimeoutError) as exc:
                if attempt >= self.config.max_attempts:
                    raise ShadowProviderError(
                        "provider_transport_error",
                        retryable=True,
                        attempts=attempt,
                    ) from exc
                self._sleep(self._retry_delay(attempt, None))
            except Exception as exc:
                raise ShadowProviderError(
                    "provider_transport_exception",
                    attempts=attempt,
                ) from exc
        raise AssertionError("shadow provider attempt loop did not terminate")

    def _interpret_response(
        self,
        response: TransportResponse,
        requested_url: str,
        idempotency_key: str,
        now: datetime,
        attempt: int,
    ) -> LookupObservation:
        if response.final_url != requested_url:
            raise ShadowProviderError("provider_redirect_rejected", attempts=attempt)
        if len(response.body) > self.config.max_response_bytes:
            raise ShadowProviderError("provider_response_too_large", attempts=attempt)
        if response.status == HTTPStatus.NOT_FOUND:
            return LookupObservation(snapshot=None, attempts=attempt)
        if response.status in {
            HTTPStatus.REQUEST_TIMEOUT,
            HTTPStatus.TOO_MANY_REQUESTS,
            HTTPStatus.INTERNAL_SERVER_ERROR,
            HTTPStatus.BAD_GATEWAY,
            HTTPStatus.SERVICE_UNAVAILABLE,
            HTTPStatus.GATEWAY_TIMEOUT,
        }:
            error = ShadowProviderError(
                f"provider_http_{response.status}",
                retryable=True,
                attempts=attempt,
                retry_after=response.retry_after,
            )
            raise error
        if response.status != HTTPStatus.OK:
            raise ShadowProviderError(
                f"provider_http_{response.status}",
                attempts=attempt,
            )
        if response.content_type.split(";", 1)[0].strip().lower() != "application/json":
            raise ShadowProviderError("provider_content_type", attempts=attempt)
        try:
            decoded = json.loads(response.body.decode("utf-8"))
        except (UnicodeDecodeError, json.JSONDecodeError) as exc:
            raise ShadowProviderError("provider_invalid_json", attempts=attempt) from exc
        snapshot = _validate_provider_snapshot(decoded, now=now)
        if snapshot.idempotency_key != idempotency_key:
            raise ShadowProviderError("provider_key_mismatch", attempts=attempt)
        return LookupObservation(snapshot=snapshot, attempts=attempt)

    def _retry_delay(self, attempt: int, retry_after: str | None) -> float:
        if retry_after is not None and retry_after.isdigit():
            return min(float(retry_after), MAX_RETRY_DELAY_SECONDS)
        return float(
            min(
                self.config.retry_base_seconds * (2 ** (attempt - 1)),
                MAX_RETRY_DELAY_SECONDS,
            )
        )


class ShadowRuntimeState:
    def __init__(
        self,
        state_dir: Path,
        adapter: ReadOnlyHTTPProviderAdapter,
        *,
        now: datetime | None = None,
    ) -> None:
        current_time = _normalize_now(now)
        _ensure_private_state_directory(state_dir)
        self.path = state_dir / f"shadow-state-{adapter.provider_instance_sha256[:32]}.json"
        self._lock = RLock()
        self._adapter = adapter.name
        self._implementation_version = adapter.implementation_version
        self._provider_instance_sha256 = adapter.provider_instance_sha256
        self._service_started_at = _format_timestamp(current_time)
        self._up = False
        self._ready = False
        self._successful_runs = 0
        self._failed_runs = 0
        self._consecutive_failures = 0
        self._last_success_at: str | None = None
        self._last_error_at: str | None = None
        self._last_error_code: str | None = None
        self._last_run_duration_seconds: float | None = None
        self._last_report: dict[str, Any] | None = None
        if self.path.exists() or self.path.is_symlink():
            self._restore(_read_contract(self.path, _validate_shadow_state_contract))
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
        validated_report = _validate_shadow_report(report)
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
        batch: Mapping[str, Any] = report["batch"] if report is not None else {}
        values = {
            "ai_companion_deployment_shadow_up": int(bool(snapshot["up"])),
            "ai_companion_deployment_shadow_ready": int(bool(snapshot["ready"])),
            "ai_companion_deployment_shadow_consecutive_failures": int(
                snapshot["consecutive_failures"]
            ),
            "ai_companion_deployment_shadow_last_success_timestamp_seconds": (
                _timestamp_or_zero(snapshot["last_success_at"])
            ),
            "ai_companion_deployment_shadow_last_run_duration_seconds": (
                snapshot["last_run_duration_seconds"] or 0.0
            ),
            "ai_companion_deployment_shadow_selected": int(batch.get("selected", 0)),
            "ai_companion_deployment_shadow_drift": sum(
                int(batch.get(outcome, 0)) for outcome in _DRIFT_OUTCOMES
            ),
            "ai_companion_deployment_shadow_lookup_errors": int(batch.get("lookup_error", 0)),
            "ai_companion_deployment_shadow_retried_lookups": int(batch.get("retried_lookups", 0)),
        }
        lines = [
            "# TYPE ai_companion_deployment_shadow_up gauge",
            f"ai_companion_deployment_shadow_up {values['ai_companion_deployment_shadow_up']}",
            "# TYPE ai_companion_deployment_shadow_ready gauge",
            (
                "ai_companion_deployment_shadow_ready "
                f"{values['ai_companion_deployment_shadow_ready']}"
            ),
            "# TYPE ai_companion_deployment_shadow_runs_total counter",
            (
                'ai_companion_deployment_shadow_runs_total{outcome="success"} '
                f"{snapshot['successful_runs']}"
            ),
            (
                'ai_companion_deployment_shadow_runs_total{outcome="error"} '
                f"{snapshot['failed_runs']}"
            ),
            "# TYPE ai_companion_deployment_shadow_outcomes gauge",
        ]
        for outcome in _OUTCOMES:
            lines.append(
                "ai_companion_deployment_shadow_outcomes"
                f'{{outcome="{outcome}"}} {int(batch.get(outcome, 0))}'
            )
        for name, value in values.items():
            if name in {
                "ai_companion_deployment_shadow_up",
                "ai_companion_deployment_shadow_ready",
            }:
                continue
            lines.extend((f"# TYPE {name} gauge", f"{name} {value}"))
        return "\n".join(lines) + "\n"

    def _restore(self, value: Mapping[str, Any]) -> None:
        expected = {
            "adapter": self._adapter,
            "implementation_version": self._implementation_version,
            "provider_instance_sha256": self._provider_instance_sha256,
        }
        if any(value.get(field) != item for field, item in expected.items()):
            raise DeploymentShadowError("shadow state does not match provider binding")
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
            "schema_version": SHADOW_STATE_SCHEMA_VERSION,
            "adapter": self._adapter,
            "implementation_version": self._implementation_version,
            "provider_instance_sha256": self._provider_instance_sha256,
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
        _write_atomic_private_json(
            self.path,
            self._contract(now),
            _validate_shadow_state_contract,
        )


class ShadowRunner:
    def __init__(
        self,
        run_once: Callable[[datetime], dict[str, Any]],
        state: ShadowRuntimeState,
        *,
        history: ShadowHistoryRecorder | None = None,
        monotonic: Callable[[], float] = time.monotonic,
    ) -> None:
        self._run_once = run_once
        self._state = state
        self._history = history
        self._monotonic = monotonic

    def run_iteration(self, now: datetime | None = None) -> bool:
        current_time = _normalize_now(now)
        started = self._monotonic()
        try:
            report = _validate_shadow_report(self._run_once(current_time))
            duration = max(0.0, self._monotonic() - started)
            if self._history is not None:
                self._history.record_success(report, duration, current_time)
        except (DeploymentShadowError, DeploymentControllerError, OSError, sqlite3.Error) as exc:
            duration = max(0.0, self._monotonic() - started)
            error_code = _classify_error(exc)
            if self._history is not None:
                try:
                    self._history.record_failure(error_code, duration, current_time)
                except (DeploymentShadowError, OSError, sqlite3.Error):
                    error_code = "history_error"
            self._state.record_failure(error_code, duration, current_time)
            return False
        self._state.record_success(report, duration, current_time)
        return True


def run_shadow_reconciliation(
    ledger: DeploymentLedger,
    adapter: ReadOnlyHTTPProviderAdapter,
    *,
    limit: int = 100,
    now: datetime | None = None,
) -> dict[str, Any]:
    current_time = _normalize_now(now)
    validated_limit = _validate_int(limit, "shadow limit", 1, 1000)
    candidates = ledger.shadow_candidates(adapter.name, limit=validated_limit)
    results: list[dict[str, Any]] = []
    counts = {outcome: 0 for outcome in _OUTCOMES}
    retried_lookups = 0
    for candidate in candidates:
        try:
            observation = adapter.lookup(str(candidate["idempotency_key"]), now=current_time)
        except ShadowProviderError as exc:
            outcome = "lookup_error"
            attempts = exc.attempts
            provider_status: str | None = None
            error_code: str | None = exc.code
        else:
            attempts = observation.attempts
            provider_status = (
                observation.snapshot.status if observation.snapshot is not None else None
            )
            error_code = None
            outcome = _compare_candidate(candidate, observation.snapshot)
        retried_lookups += attempts - 1
        counts[outcome] += 1
        results.append(
            {
                "deployment_id": str(candidate["deployment_id"]),
                "ledger_status": str(candidate["status"]),
                "provider_status": provider_status,
                "outcome": outcome,
                "error_code": error_code,
                "attempts": attempts,
                "observed_at": _format_timestamp(current_time),
            }
        )
    batch: dict[str, int] = {
        "selected": len(candidates),
        "retried_lookups": retried_lookups,
    }
    batch.update(counts)
    return _validate_shadow_report(
        {
            "schema_version": SHADOW_REPORT_SCHEMA_VERSION,
            "generated_at": _format_timestamp(current_time),
            "adapter": adapter.name,
            "implementation_version": adapter.implementation_version,
            "provider_instance_sha256": adapter.provider_instance_sha256,
            "batch": batch,
            "results": results,
        }
    )


def run_shadow_service_loop(
    runner: ShadowRunner,
    state: ShadowRuntimeState,
    config: ShadowServiceConfig,
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


def serve_shadow(
    runner: ShadowRunner,
    state: ShadowRuntimeState,
    config: ShadowServiceConfig,
    stop_event: Event,
) -> int:
    validated = _validate_service_config(config)
    server = ThreadingHTTPServer(
        (validated.listen_host, validated.listen_port),
        _handler_factory(state),
    )
    thread = Thread(target=server.serve_forever, name="shadow-health", daemon=True)
    state.set_up(True)
    thread.start()
    try:
        return run_shadow_service_loop(runner, state, validated, stop_event)
    finally:
        state.set_up(False)
        server.shutdown()
        server.server_close()
        thread.join(timeout=5.0)


def main(argv: Sequence[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        description="Read provider deployment state without performing provider writes"
    )
    parser.add_argument("--ledger", type=Path, required=True)
    parser.add_argument("--state-dir", type=Path, required=True)
    parser.add_argument("--history-ledger", type=Path)
    parser.add_argument("--provider-name", required=True)
    parser.add_argument("--provider-base-url", required=True)
    parser.add_argument("--allowed-host", required=True)
    parser.add_argument("--provider-instance", required=True)
    parser.add_argument("--token-env", default=SHADOW_TOKEN_ENV)
    parser.add_argument("--timeout-seconds", type=float, default=5.0)
    parser.add_argument("--max-attempts", type=int, default=2)
    parser.add_argument("--retry-base-seconds", type=float, default=0.25)
    parser.add_argument("--interval-seconds", type=int, default=30)
    parser.add_argument("--limit", type=int, default=100)
    parser.add_argument("--max-consecutive-failures", type=int, default=3)
    parser.add_argument("--listen-host", default="127.0.0.1")
    parser.add_argument("--listen-port", type=int, default=9466)
    parser.add_argument("--once", action="store_true")
    parser.add_argument("--require-match", action="store_true")
    args = parser.parse_args(argv)
    previous_handlers: dict[signal.Signals, Any] = {}
    stop_event = Event()
    try:
        token = _token_from_environment(args.token_env)
        adapter = ReadOnlyHTTPProviderAdapter(
            ReadOnlyHTTPProviderConfig(
                name=args.provider_name,
                base_url=args.provider_base_url,
                allowed_host=args.allowed_host,
                provider_instance=args.provider_instance,
                token=token,
                timeout_seconds=args.timeout_seconds,
                max_attempts=args.max_attempts,
                retry_base_seconds=args.retry_base_seconds,
            )
        )
        ledger = DeploymentLedger(args.ledger, create=False)
        state = ShadowRuntimeState(args.state_dir, adapter)
        from .deployment_shadow_history import ShadowHistoryLedger

        history_path = args.history_ledger or args.state_dir / "history.sqlite3"
        history = ShadowHistoryLedger(
            history_path,
            adapter.provider_instance_sha256,
        )
        config = _validate_service_config(
            ShadowServiceConfig(
                interval_seconds=args.interval_seconds,
                limit=args.limit,
                max_consecutive_failures=args.max_consecutive_failures,
                listen_host=args.listen_host,
                listen_port=args.listen_port,
            )
        )

        def run_once(now: datetime) -> dict[str, Any]:
            return run_shadow_reconciliation(
                ledger,
                adapter,
                limit=config.limit,
                now=now,
            )

        runner = ShadowRunner(run_once, state, history=history)
        if args.once:
            if not runner.run_iteration():
                return 1
            report = state.snapshot()["last_report"]
            print(json.dumps(report, ensure_ascii=False, indent=2))
            if args.require_match and report is not None:
                batch = report["batch"]
                drift = sum(int(batch[outcome]) for outcome in _DRIFT_OUTCOMES)
                if drift > 0 or int(batch["lookup_error"]) > 0:
                    return 3
            return 0
        if args.require_match:
            raise DeploymentShadowConfigError("require-match is only valid with once")
        previous_handlers = _install_signal_handlers(stop_event)
        return serve_shadow(runner, state, config, stop_event)
    except DeploymentShadowConfigError as exc:
        print(f"deployment shadow configuration error: {exc}", file=sys.stderr)
        return 2
    except (DeploymentShadowError, DeploymentControllerError, OSError, sqlite3.Error) as exc:
        print(f"deployment shadow error: {exc}", file=sys.stderr)
        return 1
    finally:
        _restore_signal_handlers(previous_handlers)


def _compare_candidate(
    candidate: Mapping[str, Any],
    provider: ProviderSnapshot | None,
) -> str:
    if provider is None:
        return "provider_missing"
    if candidate["external_operation_id"] != provider.external_operation_id:
        return "external_id_mismatch"
    ledger_status = str(candidate["status"])
    if ledger_status == "indeterminate":
        return "ledger_indeterminate"
    if ledger_status == provider.status:
        return "match"
    if ledger_status == "accepted" and provider.status == "completed":
        return "provider_ahead"
    if ledger_status == "completed" and provider.status == "accepted":
        return "provider_behind"
    return "status_mismatch"


def _validate_provider_config(
    value: ReadOnlyHTTPProviderConfig,
) -> ReadOnlyHTTPProviderConfig:
    _validate_identifier(value.name, "shadow provider name")
    _validate_identifier(value.provider_instance, "shadow provider instance")
    if not isinstance(value.allowed_host, str) or not value.allowed_host:
        raise DeploymentShadowConfigError("shadow provider allowed_host is invalid")
    if value.allowed_host != value.allowed_host.lower() or any(
        character.isspace() for character in value.allowed_host
    ):
        raise DeploymentShadowConfigError("shadow provider allowed_host is invalid")
    try:
        parsed = urllib.parse.urlsplit(value.base_url)
        port = parsed.port
    except ValueError as exc:
        raise DeploymentShadowConfigError("shadow provider base URL is invalid") from exc
    if (
        parsed.scheme != "https"
        or parsed.hostname is None
        or parsed.hostname.lower() != value.allowed_host
        or parsed.username is not None
        or parsed.password is not None
        or parsed.query
        or parsed.fragment
        or not parsed.netloc
        or value.base_url.endswith("/")
        or any(part == ".." for part in parsed.path.split("/"))
    ):
        raise DeploymentShadowConfigError(
            "shadow provider requires an exact allowlisted HTTPS base URL"
        )
    if port is not None and not 1 <= port <= 65535:
        raise DeploymentShadowConfigError("shadow provider port is invalid")
    if (
        not isinstance(value.token, str)
        or not 16 <= len(value.token) <= 4096
        or any(ord(character) < 0x20 or ord(character) == 0x7F for character in value.token)
    ):
        raise DeploymentShadowConfigError("shadow provider token is invalid")
    _validate_float(value.timeout_seconds, "shadow timeout_seconds", 0.1, 30.0)
    _validate_int(value.max_attempts, "shadow max_attempts", 1, MAX_PROVIDER_ATTEMPTS)
    _validate_float(
        value.retry_base_seconds,
        "shadow retry_base_seconds",
        0.0,
        MAX_RETRY_DELAY_SECONDS,
    )
    _validate_int(
        value.max_response_bytes,
        "shadow max_response_bytes",
        1024,
        MAX_RESPONSE_BYTES,
    )
    return value


def _validate_provider_snapshot(
    value: Any,
    *,
    now: datetime,
) -> ProviderSnapshot:
    fields = {
        "schema_version",
        "idempotency_key",
        "status",
        "external_operation_id",
        "updated_at",
    }
    if not isinstance(value, Mapping) or set(value) != fields:
        raise ShadowProviderError("provider_contract")
    if value["schema_version"] != PROVIDER_READ_SCHEMA_VERSION:
        raise ShadowProviderError("provider_contract")
    try:
        _validate_digest(str(value["idempotency_key"]), "provider idempotency_key")
    except DeploymentControllerError as exc:
        raise ShadowProviderError("provider_contract") from exc
    status = value["status"]
    if status not in {"accepted", "completed", "failed"}:
        raise ShadowProviderError("provider_contract")
    external_operation_id = value["external_operation_id"]
    if (
        not isinstance(external_operation_id, str)
        or _EXTERNAL_ID_RE.fullmatch(external_operation_id) is None
    ):
        raise ShadowProviderError("provider_contract")
    try:
        updated_at = _parse_timestamp(str(value["updated_at"]), "provider updated_at")
    except DeploymentControllerError as exc:
        raise ShadowProviderError("provider_contract") from exc
    if updated_at > now + timedelta(minutes=5):
        raise ShadowProviderError("provider_future_timestamp")
    return ProviderSnapshot(
        idempotency_key=str(value["idempotency_key"]),
        status=status,
        external_operation_id=external_operation_id,
        updated_at=_format_timestamp(updated_at),
    )


def _validate_shadow_report(value: Any) -> dict[str, Any]:
    fields = {
        "schema_version",
        "generated_at",
        "adapter",
        "implementation_version",
        "provider_instance_sha256",
        "batch",
        "results",
    }
    if not isinstance(value, Mapping) or set(value) != fields:
        raise DeploymentShadowError("shadow report is invalid")
    result = dict(value)
    if result["schema_version"] != SHADOW_REPORT_SCHEMA_VERSION:
        raise DeploymentShadowError("shadow report schema_version is invalid")
    _parse_timestamp(str(result["generated_at"]), "shadow generated_at")
    _validate_identifier(str(result["adapter"]), "shadow adapter")
    _validate_identifier(
        str(result["implementation_version"]),
        "shadow implementation_version",
    )
    try:
        _validate_digest(
            str(result["provider_instance_sha256"]),
            "shadow provider_instance_sha256",
        )
    except DeploymentControllerError as exc:
        raise DeploymentShadowError("shadow provider instance digest is invalid") from exc
    batch_fields = {"selected", "retried_lookups", *_OUTCOMES}
    batch = result["batch"]
    if not isinstance(batch, Mapping) or set(batch) != batch_fields:
        raise DeploymentShadowError("shadow batch is invalid")
    validated_batch = {
        field: _validate_int(batch[field], f"shadow batch {field}", 0, 1000000)
        for field in batch_fields
    }
    if sum(validated_batch[outcome] for outcome in _OUTCOMES) != validated_batch["selected"]:
        raise DeploymentShadowError("shadow batch outcome counts are inconsistent")
    items = result["results"]
    if not isinstance(items, list) or len(items) != validated_batch["selected"]:
        raise DeploymentShadowError("shadow results are invalid")
    validated_results = [_validate_shadow_result(item) for item in items]
    deployment_ids = [item["deployment_id"] for item in validated_results]
    if len(deployment_ids) != len(set(deployment_ids)):
        raise DeploymentShadowError("shadow results contain duplicate deployments")
    observed_counts = {outcome: 0 for outcome in _OUTCOMES}
    retries = 0
    for item in validated_results:
        observed_counts[item["outcome"]] += 1
        retries += int(item["attempts"]) - 1
    if (
        any(observed_counts[outcome] != validated_batch[outcome] for outcome in _OUTCOMES)
        or retries != validated_batch["retried_lookups"]
    ):
        raise DeploymentShadowError("shadow report summary does not match results")
    result["batch"] = validated_batch
    result["results"] = validated_results
    return result


def _validate_shadow_result(value: Any) -> dict[str, Any]:
    fields = {
        "deployment_id",
        "ledger_status",
        "provider_status",
        "outcome",
        "error_code",
        "attempts",
        "observed_at",
    }
    if not isinstance(value, Mapping) or set(value) != fields:
        raise DeploymentShadowError("shadow result is invalid")
    result = dict(value)
    _validate_identifier(str(result["deployment_id"]), "shadow deployment_id")
    if result["ledger_status"] not in {"accepted", "completed", "indeterminate"}:
        raise DeploymentShadowError("shadow ledger status is invalid")
    if result["provider_status"] not in {None, "accepted", "completed", "failed"}:
        raise DeploymentShadowError("shadow provider status is invalid")
    if result["outcome"] not in _OUTCOMES:
        raise DeploymentShadowError("shadow outcome is invalid")
    if result["error_code"] is not None:
        _validate_error_code(result["error_code"])
    if (result["outcome"] == "lookup_error") != (result["error_code"] is not None):
        raise DeploymentShadowError("shadow result error binding is invalid")
    _validate_int(result["attempts"], "shadow result attempts", 1, MAX_PROVIDER_ATTEMPTS)
    _parse_timestamp(str(result["observed_at"]), "shadow observed_at")
    return result


def _validate_shadow_state_contract(value: Any) -> dict[str, Any]:
    fields = {
        "schema_version",
        "adapter",
        "implementation_version",
        "provider_instance_sha256",
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
        raise DeploymentShadowError("shadow state is invalid")
    result = dict(value)
    if result["schema_version"] != SHADOW_STATE_SCHEMA_VERSION:
        raise DeploymentShadowError("shadow state schema_version is invalid")
    _validate_identifier(str(result["adapter"]), "shadow state adapter")
    _validate_identifier(
        str(result["implementation_version"]),
        "shadow state implementation_version",
    )
    try:
        _validate_digest(
            str(result["provider_instance_sha256"]),
            "shadow state provider_instance_sha256",
        )
    except DeploymentControllerError as exc:
        raise DeploymentShadowError("shadow state provider digest is invalid") from exc
    for name in ("service_started_at", "updated_at"):
        _parse_timestamp(str(result[name]), f"shadow state {name}")
    for name in ("successful_runs", "failed_runs", "consecutive_failures"):
        _validate_int(result[name], f"shadow state {name}", 0, 1000000000)
    if result["consecutive_failures"] > result["failed_runs"]:
        raise DeploymentShadowError("shadow state failure counters are inconsistent")
    for name in ("last_success_at", "last_error_at"):
        if result[name] is not None:
            _parse_timestamp(str(result[name]), f"shadow state {name}")
    if result["last_error_code"] is not None:
        _validate_error_code(result["last_error_code"])
    if result["last_run_duration_seconds"] is not None:
        _validate_duration(result["last_run_duration_seconds"])
    if result["last_report"] is not None:
        result["last_report"] = _validate_shadow_report(result["last_report"])
        report = result["last_report"]
        if any(
            report[field] != result[field]
            for field in (
                "adapter",
                "implementation_version",
                "provider_instance_sha256",
            )
        ):
            raise DeploymentShadowError("shadow state report binding is invalid")
    if (result["successful_runs"] == 0) != (result["last_report"] is None):
        raise DeploymentShadowError("shadow state success counters are inconsistent")
    return result


def _validate_service_config(value: ShadowServiceConfig) -> ShadowServiceConfig:
    _validate_int(value.interval_seconds, "shadow interval_seconds", 1, 15 * 60)
    _validate_int(value.limit, "shadow limit", 1, 1000)
    _validate_int(
        value.max_consecutive_failures,
        "shadow max_consecutive_failures",
        1,
        MAX_CONSECUTIVE_FAILURES,
    )
    _validate_int(value.listen_port, "shadow listen_port", 1024, 65535)
    if value.listen_host not in _ALLOWED_LISTEN_HOSTS:
        raise DeploymentShadowConfigError("shadow listen_host is invalid")
    return value


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
            "User-Agent": "ai-companion-deployment-shadow/1.0",
        },
        method="GET",
    )
    opener = urllib.request.build_opener(_NoRedirectHandler())
    try:
        with opener.open(request, timeout=timeout_seconds) as response:
            body = response.read(max_response_bytes + 1)
            return TransportResponse(
                status=int(response.status),
                body=body,
                content_type=str(response.headers.get("Content-Type", "")),
                final_url=str(response.geturl()),
                retry_after=response.headers.get("Retry-After"),
            )
    except urllib.error.HTTPError as exc:
        body = exc.read(max_response_bytes + 1)
        return TransportResponse(
            status=int(exc.code),
            body=body,
            content_type=str(exc.headers.get("Content-Type", "")),
            final_url=str(exc.geturl()),
            retry_after=exc.headers.get("Retry-After"),
        )
    except urllib.error.URLError as exc:
        raise ShadowProviderError("provider_transport_error", retryable=True) from exc


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


def _handler_factory(state: ShadowRuntimeState) -> type[BaseHTTPRequestHandler]:
    class ShadowHandler(BaseHTTPRequestHandler):
        def do_GET(self) -> None:
            status, body, content_type = _http_response(self.path, state)
            content = (body + "\n").encode("utf-8")
            self.send_response(status)
            self.send_header("Content-Type", content_type)
            self.send_header("Content-Length", str(len(content)))
            self.send_header("Cache-Control", "no-store")
            self.end_headers()
            self.wfile.write(content)

        def log_message(self, _format: str, *args: object) -> None:
            del args

    return ShadowHandler


def _http_response(
    path: str,
    state: ShadowRuntimeState,
) -> tuple[HTTPStatus, str, str]:
    if path == "/metrics":
        return HTTPStatus.OK, state.metrics_text(), "text/plain; version=0.0.4"
    snapshot = state.snapshot()
    if path == "/healthz":
        status = HTTPStatus.OK if snapshot["up"] else HTTPStatus.SERVICE_UNAVAILABLE
        return (
            status,
            json.dumps({"status": "up" if snapshot["up"] else "down"}),
            "application/json",
        )
    if path == "/readyz":
        status = HTTPStatus.OK if snapshot["ready"] else HTTPStatus.SERVICE_UNAVAILABLE
        body = json.dumps({"status": "ready" if snapshot["ready"] else "not_ready"})
        return status, body, "application/json"
    return HTTPStatus.NOT_FOUND, json.dumps({"error": "not_found"}), "application/json"


def _write_atomic_private_json(
    path: Path,
    value: Mapping[str, Any],
    validator: Callable[[Any], dict[str, Any]],
) -> None:
    _validate_private_state_directory(path.parent)
    if path.exists() or path.is_symlink():
        _read_contract(path, validator)
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
            os.close(descriptor)
        except OSError:
            pass
        try:
            temporary.unlink()
        except FileNotFoundError:
            pass
        raise
    else:
        os.close(descriptor)


def _token_from_environment(name: str) -> str:
    if _ENV_NAME_RE.fullmatch(name) is None:
        raise DeploymentShadowConfigError("shadow token environment name is invalid")
    token = os.environ.get(name)
    if token is None:
        raise DeploymentShadowConfigError(f"shadow provider token is missing from {name}")
    return token


def _install_signal_handlers(stop_event: Event) -> dict[signal.Signals, Any]:
    previous: dict[signal.Signals, Any] = {}

    def request_stop(_signum: int, _frame: Any) -> None:
        stop_event.set()

    for item in (signal.SIGINT, signal.SIGTERM):
        previous[item] = signal.getsignal(item)
        signal.signal(item, request_stop)
    return previous


def _restore_signal_handlers(previous: Mapping[signal.Signals, Any]) -> None:
    for item, handler in previous.items():
        signal.signal(item, handler)


def _classify_error(exc: BaseException) -> str:
    if isinstance(exc, DeploymentControllerError):
        return "controller_error"
    if isinstance(exc, sqlite3.Error):
        return "sqlite_error"
    if isinstance(exc, OSError):
        return "filesystem_error"
    return "shadow_error"


def _validate_identifier(value: str, field_name: str) -> str:
    if not isinstance(value, str) or _IDENTIFIER_RE.fullmatch(value) is None:
        raise DeploymentShadowConfigError(f"{field_name} is invalid")
    return value


def _validate_error_code(value: Any) -> str:
    if not isinstance(value, str) or _ERROR_CODE_RE.fullmatch(value) is None:
        raise DeploymentShadowError("shadow error code is invalid")
    return value


def _validate_duration(value: Any) -> float:
    return _validate_float(value, "shadow duration", 0.0, 24 * 60 * 60.0)


def _validate_int(value: Any, field_name: str, minimum: int, maximum: int) -> int:
    if isinstance(value, bool) or not isinstance(value, int) or not minimum <= value <= maximum:
        raise DeploymentShadowConfigError(f"{field_name} is invalid")
    return value


def _validate_float(
    value: Any,
    field_name: str,
    minimum: float,
    maximum: float,
) -> float:
    if (
        isinstance(value, bool)
        or not isinstance(value, (int, float))
        or not minimum <= float(value) <= maximum
    ):
        raise DeploymentShadowConfigError(f"{field_name} is invalid")
    return float(value)


def _timestamp_or_zero(value: Any) -> float:
    if value is None:
        return 0.0
    return _parse_timestamp(str(value), "shadow metric timestamp").timestamp()


if __name__ == "__main__":
    raise SystemExit(main())
