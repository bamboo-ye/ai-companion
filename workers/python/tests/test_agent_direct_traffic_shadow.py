from __future__ import annotations

import hashlib
import json
import sqlite3
import tempfile
import unittest
import urllib.request
from concurrent.futures import ThreadPoolExecutor
from contextlib import closing, redirect_stderr, redirect_stdout
from datetime import UTC, datetime, timedelta
from io import StringIO
from pathlib import Path
from typing import Any
from unittest.mock import patch

from ai_companion_worker.evaluation.deployment_controller import (
    _format_timestamp,
)
from ai_companion_worker.evaluation.direct_rollout_traffic import (
    SQLiteDirectTrafficSandbox,
    provider_instance_sha256,
)
from ai_companion_worker.evaluation.direct_traffic_shadow import (
    PROVIDER_STATE_SCHEMA_VERSION,
    DirectTrafficProviderError,
    DirectTrafficShadowConfigError,
    DirectTrafficShadowError,
    DirectTrafficShadowHistory,
    ReadOnlyDirectTrafficProviderAdapter,
    ReadOnlyDirectTrafficProviderConfig,
    TransportResponse,
    _default_transport,
    main,
    observer_instance_sha256,
    run_shadow_comparison,
)
from ai_companion_worker.evaluation.release_gate_history import environment_sha256


NOW = datetime(2026, 8, 12, 8, 0, tzinfo=UTC)
TOKEN = "direct-traffic-shadow-token-value"
ENVIRONMENT_ID = "staging-cn"
PROVIDER_INSTANCE = "platform-preprod-blue"
ENVIRONMENT_SHA256 = environment_sha256(ENVIRONMENT_ID)
PROVIDER_SHA256 = provider_instance_sha256(PROVIDER_INSTANCE)


def make_config(**overrides: Any) -> ReadOnlyDirectTrafficProviderConfig:
    values: dict[str, Any] = {
        "name": "preprod-platform-reader",
        "base_url": "https://traffic.provider.example/api",
        "allowed_host": "traffic.provider.example",
        "provider_instance": PROVIDER_INSTANCE,
        "environment_sha256": ENVIRONMENT_SHA256,
        "token": TOKEN,
        "max_attempts": 2,
        "retry_base_seconds": 0.1,
        "max_snapshot_age_seconds": 90,
    }
    values.update(overrides)
    return ReadOnlyDirectTrafficProviderConfig(**values)


def response(
    url: str,
    local: dict[str, Any],
    *,
    status: int = 200,
    percent: int | None = None,
    revision: int | None = None,
    chain: str | None = None,
    observed_at: datetime = NOW,
    provider_sha256: str = PROVIDER_SHA256,
    environment_digest: str = ENVIRONMENT_SHA256,
    body: bytes | None = None,
    final_url: str | None = None,
    retry_after: str | None = None,
) -> TransportResponse:
    payload = {
        "schema_version": PROVIDER_STATE_SCHEMA_VERSION,
        "provider_instance_sha256": provider_sha256,
        "environment_sha256": environment_digest,
        "current_traffic_percent": (
            local["current_traffic_percent"] if percent is None else percent
        ),
        "revision": local["revision"] if revision is None else revision,
        "history_chain_sha256": (local["history_chain_sha256"] if chain is None else chain),
        "observed_at": _format_timestamp(observed_at),
    }
    return TransportResponse(
        status=status,
        body=body if body is not None else json.dumps(payload).encode("utf-8"),
        content_type="application/json; charset=utf-8",
        final_url=final_url or url,
        retry_after=retry_after,
    )


class AgentDirectTrafficShadowTest(unittest.TestCase):
    def _sandbox(self, root: Path) -> SQLiteDirectTrafficSandbox:
        return SQLiteDirectTrafficSandbox(
            root / "traffic" / "state.sqlite3",
            ENVIRONMENT_SHA256,
            PROVIDER_INSTANCE,
            create=True,
            initial_traffic_percent=5,
        )

    def _history(
        self,
        root: Path,
        adapter: ReadOnlyDirectTrafficProviderAdapter,
    ) -> DirectTrafficShadowHistory:
        return DirectTrafficShadowHistory(
            root / "shadow" / "history.sqlite3",
            adapter.observer_instance_sha256,
            adapter.provider_instance_sha256,
            ENVIRONMENT_SHA256,
            now=NOW,
        )

    def test_https_origin_allowlist_and_secret_handling_are_fail_closed(self) -> None:
        adapter = ReadOnlyDirectTrafficProviderAdapter(make_config())
        self.assertNotIn(TOKEN, repr(adapter.config))
        self.assertFalse(hasattr(adapter, "apply"))
        self.assertFalse(hasattr(adapter, "submit"))
        self.assertEqual(
            adapter.observer_instance_sha256,
            ReadOnlyDirectTrafficProviderAdapter(
                make_config(token="rotated-secret-token-value")
            ).observer_instance_sha256,
        )
        invalid = (
            make_config(base_url="http://traffic.provider.example/api"),
            make_config(base_url="https://user@traffic.provider.example/api"),
            make_config(base_url="https://traffic.provider.example:444/api"),
            make_config(base_url="https://traffic.provider.example/api?method=PUT"),
            make_config(base_url="https://traffic.provider.example/api/%2e%2e/admin"),
            make_config(base_url="https://traffic.provider.example/api\\admin"),
            make_config(allowed_host="127.0.0.1", base_url="https://127.0.0.1/api"),
            make_config(allowed_host="localhost", base_url="https://localhost/api"),
            make_config(max_attempts=4),
            make_config(token="short"),
        )
        for config in invalid:
            with self.subTest(config=repr(config)):
                with self.assertRaises(DirectTrafficShadowConfigError):
                    ReadOnlyDirectTrafficProviderAdapter(config)

    def test_read_uses_one_fixed_endpoint_and_retries_only_transient_errors(self) -> None:
        calls: list[tuple[str, str]] = []
        sleeps: list[float] = []
        local = {
            "current_traffic_percent": 5,
            "revision": 0,
            "history_chain_sha256": "0" * 64,
        }

        def recover(url: str, token: str, _timeout: float, _limit: int) -> TransportResponse:
            calls.append((url, token))
            if len(calls) == 1:
                return response(url, local, status=503, retry_after="20")
            return response(url, local)

        adapter = ReadOnlyDirectTrafficProviderAdapter(
            make_config(), transport=recover, sleep=sleeps.append
        )
        observation = adapter.read_state(now=NOW)
        self.assertEqual(observation.attempts, 2)
        self.assertEqual(sleeps, [5.0])
        self.assertEqual(
            calls,
            [
                ("https://traffic.provider.example/api/v1/direct-traffic/state", TOKEN),
                ("https://traffic.provider.example/api/v1/direct-traffic/state", TOKEN),
            ],
        )

        denied_calls = 0

        def denied(url: str, *_args: Any) -> TransportResponse:
            nonlocal denied_calls
            denied_calls += 1
            return response(url, local, status=401)

        with self.assertRaises(DirectTrafficProviderError) as raised:
            ReadOnlyDirectTrafficProviderAdapter(make_config(), transport=denied).read_state(
                now=NOW
            )
        self.assertEqual(raised.exception.code, "provider_http_401")
        self.assertEqual(raised.exception.attempts, 1)
        self.assertEqual(denied_calls, 1)

    def test_default_transport_constructs_only_get_and_disables_redirects(self) -> None:
        captured: dict[str, Any] = {}

        class FakeResponse:
            status = 200
            headers = {"Content-Type": "application/json"}

            def __enter__(self) -> FakeResponse:
                return self

            def __exit__(self, *_args: Any) -> None:
                return None

            def read(self, amount: int) -> bytes:
                captured["read_limit"] = amount
                return b"{}"

            def geturl(self) -> str:
                return "https://traffic.provider.example/api/v1/direct-traffic/state"

        class FakeOpener:
            def open(self, request: urllib.request.Request, *, timeout: float) -> FakeResponse:
                captured["request"] = request
                captured["timeout"] = timeout
                return FakeResponse()

        with patch(
            "ai_companion_worker.evaluation.direct_traffic_shadow.urllib.request.build_opener",
            return_value=FakeOpener(),
        ) as build_opener:
            _default_transport(
                "https://traffic.provider.example/api/v1/direct-traffic/state",
                TOKEN,
                2.0,
                4096,
            )
        request = captured["request"]
        self.assertEqual(request.get_method(), "GET")
        self.assertIsNone(request.data)
        self.assertEqual(request.get_header("Authorization"), f"Bearer {TOKEN}")
        self.assertEqual(captured["read_limit"], 4097)
        self.assertEqual(build_opener.call_count, 1)
        self.assertEqual(len(build_opener.call_args.args), 1)

    def test_state_path_is_identity_bound_and_strictly_validated(self) -> None:
        first = make_config(state_path="/v1/namespaces/preproduction-a/direct-traffic/shadow-state")
        second = make_config(
            state_path="/v1/namespaces/preproduction-b/direct-traffic/shadow-state"
        )
        self.assertNotEqual(observer_instance_sha256(first), observer_instance_sha256(second))
        for path in ("relative", "/", "/v1//state", "/v1/../state", "/v1/state?x=1"):
            with self.subTest(path=path):
                with self.assertRaises(DirectTrafficShadowConfigError):
                    ReadOnlyDirectTrafficProviderAdapter(make_config(state_path=path))

    def test_redirect_duplicate_keys_and_identity_rebinding_are_rejected(self) -> None:
        local = {
            "current_traffic_percent": 5,
            "revision": 0,
            "history_chain_sha256": "0" * 64,
        }
        cases = (
            lambda url: response(
                url, local, final_url="https://attacker.example/v1/direct-traffic/state"
            ),
            lambda url: response(
                url,
                local,
                body=(
                    b'{"schema_version":"agent-direct-traffic-provider-state-v1",'
                    b'"schema_version":"forged"}'
                ),
            ),
            lambda url: response(url, local, provider_sha256="f" * 64),
            lambda url: response(url, local, environment_digest="e" * 64),
        )
        expected = (
            "provider_redirect_rejected",
            "provider_invalid_json",
            "provider_binding_mismatch",
            "provider_binding_mismatch",
        )
        for factory, code in zip(cases, expected, strict=True):
            with self.subTest(code=code):
                adapter = ReadOnlyDirectTrafficProviderAdapter(
                    make_config(), transport=lambda url, *_args, f=factory: f(url)
                )
                with self.assertRaises(DirectTrafficProviderError) as raised:
                    adapter.read_state(now=NOW)
                self.assertEqual(raised.exception.code, code)

    def test_match_drift_stale_and_lookup_failure_are_audited_without_secrets(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            sandbox = self._sandbox(root)
            local = sandbox.status()
            replies = iter(
                (
                    response("ignored", local),
                    response("ignored", local, percent=10),
                    response("ignored", local, observed_at=NOW - timedelta(seconds=91)),
                    response("ignored", local, status=503),
                    response("ignored", local, status=503),
                )
            )

            def transport(url: str, *_args: Any) -> TransportResponse:
                item = next(replies)
                return TransportResponse(
                    status=item.status,
                    body=item.body,
                    content_type=item.content_type,
                    final_url=url,
                    retry_after=item.retry_after,
                )

            adapter = ReadOnlyDirectTrafficProviderAdapter(
                make_config(), transport=transport, sleep=lambda _seconds: None
            )
            history = self._history(root, adapter)
            ledger_hash = hashlib.sha256(sandbox.path.read_bytes()).hexdigest()
            ticks = iter((0.0, 0.1, 1.0, 1.1, 2.0, 2.1, 3.0, 3.1))
            reports = [
                run_shadow_comparison(
                    sandbox, adapter, history, now=NOW, monotonic=lambda: next(ticks)
                )
                for _ in range(4)
            ]
            self.assertEqual(
                [item["outcome"] for item in reports],
                ["match", "drift", "drift", "lookup_error"],
            )
            self.assertEqual(reports[1]["drift_fields"], ["current_traffic_percent"])
            self.assertEqual(reports[2]["drift_fields"], ["provider_snapshot_age"])
            self.assertEqual(reports[3]["attempts"], 2)
            self.assertEqual(reports[3]["error_code"], "provider_http_503")
            self.assertEqual(hashlib.sha256(sandbox.path.read_bytes()).hexdigest(), ledger_hash)
            serialized = json.dumps(history.status()) + json.dumps(history.events())
            self.assertNotIn(TOKEN, serialized)
            self.assertNotIn("traffic.provider.example", serialized)
            self.assertEqual(
                history.status()["outcomes"],
                {"match": 1, "drift": 2, "lookup_error": 1},
            )

    def test_local_change_during_provider_read_never_reports_match(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            sandbox = self._sandbox(root)
            first = sandbox.status()
            changed = dict(first)
            changed["current_traffic_percent"] = 10
            changed["revision"] = 1
            statuses = iter((first, changed))

            class ChangingSandbox:
                provider_instance_sha256 = PROVIDER_SHA256
                environment_sha256 = ENVIRONMENT_SHA256

                def status(self) -> dict[str, Any]:
                    return next(statuses)

            adapter = ReadOnlyDirectTrafficProviderAdapter(
                make_config(), transport=lambda url, *_args: response(url, changed)
            )
            history = self._history(root, adapter)
            report = run_shadow_comparison(
                ChangingSandbox(),  # type: ignore[arg-type]
                adapter,
                history,
                now=NOW,
                monotonic=iter((0.0, 0.1)).__next__,
            )
            self.assertEqual(report["outcome"], "drift")
            self.assertEqual(report["drift_fields"], ["local_state_changed_during_read"])

    def test_history_is_concurrent_idempotent_bound_and_append_only(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            sandbox = self._sandbox(root)
            local = sandbox.status()
            adapter = ReadOnlyDirectTrafficProviderAdapter(
                make_config(), transport=lambda url, *_args: response(url, local)
            )
            history = self._history(root, adapter)
            report = run_shadow_comparison(
                sandbox,
                adapter,
                history,
                now=NOW,
                monotonic=iter((0.0, 0.1)).__next__,
            )
            with ThreadPoolExecutor(max_workers=8) as executor:
                results = list(executor.map(lambda _index: history.record(report), range(8)))
            self.assertFalse(any(results))
            self.assertEqual(len(history.events()), 1)
            with closing(sqlite3.connect(history.path)) as connection:
                with self.assertRaises(sqlite3.IntegrityError):
                    connection.execute("UPDATE direct_traffic_shadow_events SET outcome = 'drift'")
                with self.assertRaises(sqlite3.IntegrityError):
                    connection.execute("DELETE FROM direct_traffic_shadow_events")
            with self.assertRaises(DirectTrafficShadowError):
                DirectTrafficShadowHistory(
                    history.path,
                    adapter.observer_instance_sha256,
                    "f" * 64,
                    ENVIRONMENT_SHA256,
                    create=False,
                )

    def test_history_hash_chain_detects_privileged_tampering(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            sandbox = self._sandbox(root)
            local = sandbox.status()
            adapter = ReadOnlyDirectTrafficProviderAdapter(
                make_config(), transport=lambda url, *_args: response(url, local)
            )
            history = self._history(root, adapter)
            run_shadow_comparison(
                sandbox,
                adapter,
                history,
                now=NOW,
                monotonic=iter((0.0, 0.1)).__next__,
            )
            with closing(sqlite3.connect(history.path)) as connection:
                connection.execute("DROP TRIGGER direct_traffic_shadow_events_no_update")
                connection.execute(
                    "UPDATE direct_traffic_shadow_events SET chain_sha256 = ?",
                    ("f" * 64,),
                )
                connection.commit()
            with self.assertRaises(DirectTrafficShadowError):
                history.events()

    def test_cli_check_and_status_preserve_read_only_exit_contract(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            sandbox = self._sandbox(root)
            local = sandbox.status()
            adapter = ReadOnlyDirectTrafficProviderAdapter(
                make_config(),
                transport=lambda url, *_args: response(url, local, observed_at=datetime.now(UTC)),
            )
            sandbox_digest_before = hashlib.sha256(sandbox.path.read_bytes()).hexdigest()
            arguments = [
                "--history-ledger",
                str(root / "shadow" / "history.sqlite3"),
                "--environment-id",
                ENVIRONMENT_ID,
                "--provider-name",
                "preprod-platform-reader",
                "--provider-base-url",
                "https://traffic.provider.example/api",
                "--allowed-host",
                "traffic.provider.example",
                "--provider-instance",
                PROVIDER_INSTANCE,
            ]
            output = StringIO()
            check_errors = StringIO()
            with (
                patch.dict(
                    "os.environ",
                    {"OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_PROVIDER_TOKEN": TOKEN},
                    clear=False,
                ),
                patch(
                    "ai_companion_worker.evaluation.direct_traffic_shadow.ReadOnlyDirectTrafficProviderAdapter",
                    return_value=adapter,
                ),
                redirect_stdout(output),
                redirect_stderr(check_errors),
            ):
                code = main(
                    [
                        "check",
                        *arguments,
                        "--sandbox-ledger",
                        str(sandbox.path),
                        "--canary-lease-root",
                        str(root / "leases"),
                        "--require-match",
                    ]
                )
            self.assertEqual(code, 0, check_errors.getvalue())
            self.assertEqual(json.loads(output.getvalue())["outcome"], "match")
            self.assertEqual(
                hashlib.sha256(sandbox.path.read_bytes()).hexdigest(),
                sandbox_digest_before,
            )

            output = StringIO()
            with redirect_stdout(output):
                code = main(["status", *arguments])
            self.assertEqual(code, 0)
            self.assertEqual(json.loads(output.getvalue())["outcomes"]["match"], 1)

            drift_adapter = ReadOnlyDirectTrafficProviderAdapter(
                make_config(),
                transport=lambda url, *_args: response(
                    url,
                    local,
                    percent=10,
                    observed_at=datetime.now(UTC),
                ),
            )
            output = StringIO()
            with (
                patch.dict(
                    "os.environ",
                    {"OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_PROVIDER_TOKEN": TOKEN},
                    clear=False,
                ),
                patch(
                    "ai_companion_worker.evaluation.direct_traffic_shadow.ReadOnlyDirectTrafficProviderAdapter",
                    return_value=drift_adapter,
                ),
                redirect_stdout(output),
            ):
                code = main(
                    [
                        "check",
                        *arguments,
                        "--sandbox-ledger",
                        str(sandbox.path),
                        "--canary-lease-root",
                        str(root / "leases"),
                        "--require-match",
                    ]
                )
            self.assertEqual(code, 1)
            self.assertEqual(json.loads(output.getvalue())["outcome"], "drift")

            errors = StringIO()
            with patch.dict("os.environ", {}, clear=True), redirect_stderr(errors):
                code = main(
                    [
                        "check",
                        *arguments,
                        "--sandbox-ledger",
                        str(sandbox.path),
                    ]
                )
            self.assertEqual(code, 1)
            self.assertIn("OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_PROVIDER_TOKEN", errors.getvalue())


if __name__ == "__main__":
    unittest.main()
