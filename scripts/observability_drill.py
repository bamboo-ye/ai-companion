#!/usr/bin/env python3
"""Synthetic retry metrics, webhook sink, and live alert-chain verifier."""

from __future__ import annotations

import json
import sys
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import dataclass, field
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any, Callable


RETRY_ALERTS = {
    "AICompanionAgentRetryStorm",
    "AICompanionAgentRetryRecoveryLow",
    "AICompanionAgentRetryExhausted",
}
MODES = {
    "healthy": {
        "scheduled": 0,
        "recovered": 0,
        "exhausted": 0,
        "deadline_exhausted": 0,
        "ratio": 0.0,
    },
    "incident": {
        "scheduled": 24,
        "recovered": 1,
        "exhausted": 3,
        "deadline_exhausted": 1,
        "ratio": 0.2,
    },
}


@dataclass
class DrillState:
    mode: str = "healthy"
    deliveries: list[dict[str, Any]] = field(default_factory=list)
    lock: threading.Lock = field(default_factory=threading.Lock)

    def set_mode(self, mode: str) -> None:
        if mode not in MODES:
            raise ValueError(f"unknown mode: {mode}")
        with self.lock:
            self.mode = mode

    def reset(self) -> None:
        with self.lock:
            self.mode = "healthy"
            self.deliveries.clear()

    def metrics(self) -> str:
        with self.lock:
            values = dict(MODES[self.mode])
        outcomes = ("scheduled", "recovered", "exhausted", "deadline_exhausted")
        lines = [
            "# HELP ai_companion_agent_execution_retries_recent Synthetic retry outcomes for the alert drill.",
            "# TYPE ai_companion_agent_execution_retries_recent gauge",
        ]
        lines.extend(
            'ai_companion_agent_execution_retries_recent{outcome="%s"} %s'
            % (outcome, values[outcome])
            for outcome in outcomes
        )
        lines.extend(
            [
                "# HELP ai_companion_agent_execution_retry_recovery_ratio Synthetic retry recovery ratio.",
                "# TYPE ai_companion_agent_execution_retry_recovery_ratio gauge",
                "ai_companion_agent_execution_retry_recovery_ratio %s" % values["ratio"],
                "",
            ]
        )
        return "\n".join(lines)

    def record_webhook(self, payload: dict[str, Any]) -> None:
        received_at = time.time()
        status = str(payload.get("status", ""))
        alerts = payload.get("alerts")
        if not isinstance(alerts, list):
            raise ValueError("webhook alerts must be a list")
        records: list[dict[str, Any]] = []
        for alert in alerts:
            if not isinstance(alert, dict):
                continue
            labels = alert.get("labels") if isinstance(alert.get("labels"), dict) else {}
            records.append(
                {
                    "alertname": str(labels.get("alertname", "")),
                    "status": str(alert.get("status") or status),
                    "startsAt": str(alert.get("startsAt", "")),
                    "endsAt": str(alert.get("endsAt", "")),
                    "received_at": received_at,
                }
            )
        with self.lock:
            self.deliveries.extend(records)

    def snapshot(self) -> dict[str, Any]:
        with self.lock:
            return {"mode": self.mode, "deliveries": list(self.deliveries)}


STATE = DrillState()


class DrillHandler(BaseHTTPRequestHandler):
    server_version = "AICompanionObservabilityDrill/1.0"

    def log_message(self, format: str, *args: object) -> None:
        return

    def _send(self, status: int, body: bytes, content_type: str) -> None:
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _json(self, status: int, value: Any) -> None:
        self._send(status, json.dumps(value, sort_keys=True).encode(), "application/json")

    def do_GET(self) -> None:  # noqa: N802
        path = urllib.parse.urlparse(self.path).path
        if path == "/healthz":
            self._json(200, {"status": "ok"})
            return
        if path == "/metrics":
            self._send(200, STATE.metrics().encode(), "text/plain; version=0.0.4")
            return
        if path == "/state":
            self._json(200, STATE.snapshot())
            return
        self._json(404, {"error": "not found"})

    def do_POST(self) -> None:  # noqa: N802
        path = urllib.parse.urlparse(self.path).path
        try:
            if path == "/reset":
                STATE.reset()
                self._json(200, STATE.snapshot())
                return
            if path.startswith("/mode/"):
                STATE.set_mode(path.removeprefix("/mode/"))
                self._json(200, STATE.snapshot())
                return
            if path == "/webhook":
                length = int(self.headers.get("Content-Length", "0"))
                payload = json.loads(self.rfile.read(length) or b"{}")
                if not isinstance(payload, dict):
                    raise ValueError("webhook payload must be an object")
                STATE.record_webhook(payload)
                self._json(200, {"accepted": True})
                return
        except (ValueError, json.JSONDecodeError) as error:
            self._json(400, {"error": str(error)})
            return
        self._json(404, {"error": "not found"})


def request_json(url: str, method: str = "GET") -> dict[str, Any]:
    request = urllib.request.Request(url, method=method)
    try:
        with urllib.request.urlopen(request, timeout=5) as response:
            payload = json.load(response)
    except (urllib.error.URLError, TimeoutError, json.JSONDecodeError) as error:
        raise RuntimeError(f"request failed: {method} {url}: {error}") from error
    if not isinstance(payload, dict):
        raise RuntimeError(f"expected JSON object from {url}")
    return payload


def wait_for(
    description: str,
    predicate: Callable[[], tuple[bool, Any]],
    timeout_seconds: float,
) -> Any:
    deadline = time.monotonic() + timeout_seconds
    last_value: Any = None
    last_report = 0.0
    while time.monotonic() < deadline:
        try:
            ready, last_value = predicate()
            if ready:
                print(f"PASS {description}", flush=True)
                return last_value
        except RuntimeError as error:
            last_value = str(error)
        now = time.monotonic()
        if now - last_report >= 10:
            print(f"WAIT {description}: {last_value}", flush=True)
            last_report = now
        time.sleep(1)
    raise RuntimeError(f"timeout waiting for {description}: {last_value}")


def prometheus_retry_states() -> dict[str, str]:
    payload = request_json("http://prometheus:9090/api/v1/rules?type=alert")
    states: dict[str, str] = {}
    for group in payload.get("data", {}).get("groups", []):
        for rule in group.get("rules", []):
            name = rule.get("name")
            if name in RETRY_ALERTS:
                states[str(name)] = str(rule.get("state", ""))
    return states


def prometheus_target_up() -> bool:
    query = urllib.parse.urlencode({"query": 'up{job="retry-drill"}'})
    payload = request_json(f"http://prometheus:9090/api/v1/query?{query}")
    results = payload.get("data", {}).get("result", [])
    return bool(results and results[0].get("value", [None, "0"])[1] == "1")


def delivery_statuses() -> dict[str, set[str]]:
    payload = request_json("http://drill-sink:18080/state")
    statuses = {name: set() for name in RETRY_ALERTS}
    for delivery in payload.get("deliveries", []):
        name = delivery.get("alertname")
        if name in statuses:
            statuses[name].add(str(delivery.get("status", "")))
    return statuses


def all_deliveries_have(status: str) -> tuple[bool, dict[str, list[str]]]:
    values = delivery_statuses()
    formatted = {name: sorted(statuses) for name, statuses in values.items()}
    return all(status in statuses for statuses in values.values()), formatted


def alertmanager_active_names() -> set[str]:
    with urllib.request.urlopen("http://alertmanager:9093/api/v2/alerts", timeout=5) as response:
        payload = json.load(response)
    if not isinstance(payload, list):
        raise RuntimeError("Alertmanager alerts response must be a list")
    return {
        str(alert.get("labels", {}).get("alertname", ""))
        for alert in payload
        if isinstance(alert, dict)
    } & RETRY_ALERTS


def verify() -> None:
    wait_for(
        "Prometheus readiness",
        lambda: (request_json("http://prometheus:9090/api/v1/status/runtimeinfo").get("status") == "success", None),
        45,
    )
    wait_for(
        "Alertmanager readiness",
        lambda: (isinstance(request_json("http://alertmanager:9093/api/v2/status"), dict), None),
        45,
    )
    request_json("http://drill-sink:18080/reset", method="POST")
    wait_for("synthetic metrics scrape", lambda: (prometheus_target_up(), None), 30)

    healthy_states = wait_for(
        "all retry rules loaded and healthy metrics remain inactive",
        lambda: (
            (states := prometheus_retry_states()).keys() == RETRY_ALERTS
            and all(state == "inactive" for state in states.values()),
            states,
        ),
        45,
    )
    if alertmanager_active_names():
        raise RuntimeError("retry alert false positive while the sample count is zero")

    request_json("http://drill-sink:18080/mode/incident", method="POST")
    print("INFO incident metrics enabled; preserving production 1m hold duration", flush=True)
    firing_states = wait_for(
        "all three retry alerts firing",
        lambda: (
            (states := prometheus_retry_states()).keys() == RETRY_ALERTS
            and all(state == "firing" for state in states.values()),
            states,
        ),
        125,
    )
    wait_for(
        "Alertmanager received all three alerts",
        lambda: (
            (names := alertmanager_active_names()) == RETRY_ALERTS,
            sorted(names),
        ),
        30,
    )
    firing_deliveries = wait_for(
        "webhook received all firing notifications",
        lambda: all_deliveries_have("firing"),
        30,
    )

    request_json("http://drill-sink:18080/mode/healthy", method="POST")
    print("INFO healthy metrics restored; waiting for resolved delivery", flush=True)
    recovered_states = wait_for(
        "all retry rules return inactive",
        lambda: (
            (states := prometheus_retry_states()).keys() == RETRY_ALERTS
            and all(state == "inactive" for state in states.values()),
            states,
        ),
        50,
    )
    wait_for(
        "Alertmanager has no active retry alerts",
        lambda: ((names := alertmanager_active_names()) == set(), sorted(names)),
        30,
    )
    resolved_deliveries = wait_for(
        "webhook received all resolved notifications",
        lambda: all_deliveries_have("resolved"),
        30,
    )

    print(
        json.dumps(
            {
                "alertmanager_delivery": "passed",
                "alerts": sorted(RETRY_ALERTS),
                "firing_states": firing_states,
                "healthy_states": healthy_states,
                "recovered_states": recovered_states,
                "firing_deliveries": firing_deliveries,
                "resolved_deliveries": resolved_deliveries,
                "sample_guard": "passed",
            },
            sort_keys=True,
        ),
        flush=True,
    )


def self_test() -> None:
    state = DrillState()
    assert 'outcome="scheduled"} 0' in state.metrics()
    state.set_mode("incident")
    metrics = state.metrics()
    assert 'outcome="scheduled"} 24' in metrics
    assert "recovery_ratio 0.2" in metrics
    state.record_webhook(
        {
            "status": "firing",
            "alerts": [
                {
                    "status": "firing",
                    "labels": {"alertname": "AICompanionAgentRetryStorm"},
                }
            ],
        }
    )
    snapshot = state.snapshot()
    assert snapshot["deliveries"][0]["status"] == "firing"
    assert snapshot["deliveries"][0]["alertname"] == "AICompanionAgentRetryStorm"
    try:
        state.set_mode("missing")
    except ValueError:
        pass
    else:
        raise AssertionError("unknown metric modes must be rejected")
    print("observability_drill_self_test=passed")


def main() -> None:
    command = sys.argv[1] if len(sys.argv) > 1 else ""
    if command == "serve":
        ThreadingHTTPServer(("0.0.0.0", 18080), DrillHandler).serve_forever()
        return
    if command == "verify":
        verify()
        return
    if command == "self-test":
        self_test()
        return
    raise SystemExit("usage: observability_drill.py {serve|verify|self-test}")


if __name__ == "__main__":
    main()
