from __future__ import annotations

import argparse
import hashlib
import json
import math
import os
import re
import secrets
import stat
import sys
import time
import urllib.error
import urllib.request
from datetime import UTC, datetime
from pathlib import Path
from typing import Any, Mapping, Sequence
from xml.etree import ElementTree

from ai_companion_worker.response_quality import inspect_and_repair_response


REPORT_SCHEMA_VERSION = "agent-direct-canary-report-v1"
BASELINE_SCHEMA_VERSION = "agent-direct-canary-baseline-v1"
SUITE_SCHEMA_VERSION = "agent-direct-canary-suite-v1"
MAX_CONFIG_BYTES = 64 * 1024
MAX_RESPONSE_BYTES = 2 * 1024 * 1024
_CASE_ID_RE = re.compile(r"[a-z0-9][a-z0-9_-]{0,63}")
_SENTENCE = re.compile(r"[^。！？.!?\n]+[。！？.!?]?", re.UNICODE)
REPORT_CASE_FIELDS = {
    "id",
    "status",
    "execution_mode",
    "outcome",
    "model_calls",
    "duration_ms",
    "assistant_messages",
    "quality_passed",
    "content_passed",
    "deterministic_repairs",
    "rewrite_attempts",
    "repair_attempted",
    "repair_succeeded",
    "duplicate_violations",
    "planned",
    "tool_actions",
    "sentence_count",
}


class DirectCanaryError(ValueError):
    pass


def load_baseline(path: Path) -> tuple[dict[str, Any], str]:
    value, digest = _load_config(path, "direct Canary baseline")
    if set(value) != {"schema_version", "version", "minimum", "maximum", "required"}:
        raise DirectCanaryError("direct Canary baseline fields are invalid")
    if value.get("schema_version") != BASELINE_SCHEMA_VERSION:
        raise DirectCanaryError("unsupported direct Canary baseline schema_version")
    if value.get("version") != BASELINE_SCHEMA_VERSION:
        raise DirectCanaryError("direct Canary baseline version is invalid")
    minimum = _thresholds(value.get("minimum"), "minimum")
    maximum = _thresholds(value.get("maximum"), "maximum")
    required = value.get("required")
    if required != {"repair_probe_passed": True}:
        raise DirectCanaryError("direct Canary required controls are invalid")
    return {
        "schema_version": BASELINE_SCHEMA_VERSION,
        "version": BASELINE_SCHEMA_VERSION,
        "minimum": minimum,
        "maximum": maximum,
        "required": dict(required),
    }, digest


def load_suite(path: Path) -> tuple[dict[str, Any], str]:
    value, digest = _load_config(path, "direct Canary suite")
    if set(value) != {"schema_version", "version", "cases"}:
        raise DirectCanaryError("direct Canary suite fields are invalid")
    if value.get("schema_version") != SUITE_SCHEMA_VERSION:
        raise DirectCanaryError("unsupported direct Canary suite schema_version")
    version = value.get("version")
    cases = value.get("cases")
    if version != SUITE_SCHEMA_VERSION or not isinstance(cases, list) or not 1 <= len(cases) <= 20:
        raise DirectCanaryError("direct Canary suite is invalid")
    validated: list[dict[str, Any]] = []
    identifiers: set[str] = set()
    for item in cases:
        if not isinstance(item, Mapping) or set(item) != {
            "id",
            "message",
            "min_chars",
            "max_chars",
            "max_sentences",
            "required_term_groups",
        }:
            raise DirectCanaryError("direct Canary case fields are invalid")
        identifier = item.get("id")
        message = item.get("message")
        minimum = item.get("min_chars")
        maximum = item.get("max_chars")
        max_sentences = item.get("max_sentences")
        groups = item.get("required_term_groups")
        if (
            not isinstance(identifier, str)
            or _CASE_ID_RE.fullmatch(identifier) is None
            or identifier in identifiers
            or not isinstance(message, str)
            or not message.strip()
            or len(message) > 500
            or isinstance(minimum, bool)
            or not isinstance(minimum, int)
            or isinstance(maximum, bool)
            or not isinstance(maximum, int)
            or not 1 <= minimum <= maximum <= 2000
            or isinstance(max_sentences, bool)
            or not isinstance(max_sentences, int)
            or not 1 <= max_sentences <= 20
            or not isinstance(groups, list)
            or not 1 <= len(groups) <= 10
        ):
            raise DirectCanaryError("direct Canary case is invalid")
        safe_groups: list[list[str]] = []
        for group in groups:
            if (
                not isinstance(group, list)
                or not 1 <= len(group) <= 10
                or any(
                    not isinstance(term, str)
                    or not term.strip()
                    or len(term) > 40
                    for term in group
                )
            ):
                raise DirectCanaryError("direct Canary required term group is invalid")
            safe_groups.append([str(term).strip() for term in group])
        identifiers.add(identifier)
        validated.append(
            {
                "id": identifier,
                "message": message.strip(),
                "min_chars": minimum,
                "max_chars": maximum,
                "max_sentences": max_sentences,
                "required_term_groups": safe_groups,
            }
        )
    return {
        "schema_version": SUITE_SCHEMA_VERSION,
        "version": SUITE_SCHEMA_VERSION,
        "cases": validated,
    }, digest


def evaluate_case(
    case: Mapping[str, Any],
    run_payload: Mapping[str, Any],
    duration_ms: int,
    *,
    assistant_messages: int = 1,
) -> dict[str, Any]:
    if (
        isinstance(assistant_messages, bool)
        or not isinstance(assistant_messages, int)
        or assistant_messages < 0
    ):
        raise DirectCanaryError("direct Agent Run assistant message count is invalid")
    output = run_payload.get("output")
    if not isinstance(output, Mapping):
        raise DirectCanaryError("completed direct Agent Run output is invalid")
    execution_mode = output.get("execution_mode")
    outcome = output.get("outcome")
    response = output.get("response")
    if not isinstance(response, str) or not response.strip():
        raise DirectCanaryError("completed direct Agent Run response is invalid")
    budget = output.get("budget")
    usage = budget.get("usage") if isinstance(budget, Mapping) else None
    model_calls = usage.get("model_calls") if isinstance(usage, Mapping) else None
    if isinstance(model_calls, bool) or not isinstance(model_calls, int) or model_calls < 0:
        raise DirectCanaryError("direct Agent Run model call usage is invalid")
    model = output.get("model")
    model_events = model.get("calls") if isinstance(model, Mapping) else None
    if not isinstance(model_events, list) or len(model_events) != model_calls:
        raise DirectCanaryError("direct Agent Run model events do not match budget usage")
    validation = output.get("response_validation")
    if not isinstance(validation, Mapping):
        raise DirectCanaryError("direct Agent Run response validation is invalid")
    quality_passed = validation.get("passed") is True
    repairs = validation.get("repairs")
    rewrite_attempts = validation.get("rewrite_attempt")
    if not isinstance(repairs, list):
        raise DirectCanaryError("direct Agent Run deterministic repairs are invalid")
    if (
        isinstance(rewrite_attempts, bool)
        or not isinstance(rewrite_attempts, int)
        or rewrite_attempts < 0
        or rewrite_attempts > 1
    ):
        raise DirectCanaryError("direct Agent Run rewrite attempts are invalid")
    observability = output.get("observability")
    node_trace = observability.get("node_trace") if isinstance(observability, Mapping) else None
    if not isinstance(node_trace, list):
        raise DirectCanaryError("direct Agent Run node trace is invalid")
    nodes = [
        str(item.get("node"))
        for item in node_trace
        if isinstance(item, Mapping) and isinstance(item.get("node"), str)
    ]
    planned = "plan" in nodes
    tool_actions = sum(node in {"prepare_tool", "commit_tool", "retry_tool"} for node in nodes)
    final_inspection = inspect_and_repair_response(response)
    final_repairs = final_inspection.report.get("repairs", [])
    final_violations = final_inspection.report.get("violations", [])
    duplicate_violations = len(final_repairs) + len(final_violations)
    sentence_count = len([item for item in _SENTENCE.findall(response) if item.strip()])
    required_groups = case.get("required_term_groups", [])
    content_passed = (
        int(case["min_chars"]) <= len(response.strip()) <= int(case["max_chars"])
        and sentence_count <= int(case["max_sentences"])
        and all(
            any(str(term).casefold() in response.casefold() for term in group)
            for group in required_groups
            if isinstance(group, list)
        )
        and "已停止交付" not in response
    )
    repair_attempted = bool(repairs) or rewrite_attempts > 0
    repair_succeeded = (
        repair_attempted
        and quality_passed
        and duplicate_violations == 0
        and outcome == "completed"
    )
    return {
        "id": str(case["id"]),
        "status": str(run_payload.get("status") or ""),
        "execution_mode": str(execution_mode or ""),
        "outcome": str(outcome or ""),
        "model_calls": model_calls,
        "duration_ms": duration_ms,
        "assistant_messages": assistant_messages,
        "quality_passed": quality_passed,
        "content_passed": content_passed,
        "deterministic_repairs": len(repairs),
        "rewrite_attempts": rewrite_attempts,
        "repair_attempted": repair_attempted,
        "repair_succeeded": repair_succeeded,
        "duplicate_violations": duplicate_violations,
        "planned": planned,
        "tool_actions": tool_actions,
        "sentence_count": sentence_count,
    }


def build_report(
    results: Sequence[Mapping[str, Any]],
    *,
    baseline: Mapping[str, Any],
    baseline_sha256: str,
    suite: Mapping[str, Any],
    suite_sha256: str,
) -> tuple[dict[str, Any], list[dict[str, Any]]]:
    cases = len(suite.get("cases", []))
    completed = sum(
        item.get("status") == "completed" and item.get("outcome") == "completed"
        for item in results
    )
    direct_runs = sum(item.get("execution_mode") == "direct" for item in results)
    single_model_call_runs = sum(item.get("model_calls") == 1 for item in results)
    quality_passed_runs = sum(item.get("quality_passed") is True for item in results)
    content_passed_runs = sum(item.get("content_passed") is True for item in results)
    repair_attempted_runs = sum(item.get("repair_attempted") is True for item in results)
    repair_succeeded_runs = sum(item.get("repair_succeeded") is True for item in results)
    durations = [int(item.get("duration_ms", 0)) for item in results]
    probe = inspect_and_repair_response(
        "缓存可以减少重复计算。缓存可以减少重复计算。它还能提高访问速度。"
    )
    repair_probe_passed = (
        probe.report.get("passed") is True
        and probe.report.get("changed") is True
        and len(probe.report.get("repairs", [])) == 1
        and probe.response == "缓存可以减少重复计算。它还能提高访问速度。"
    )
    summary: dict[str, Any] = {
        "cases": cases,
        "completed": completed,
        "assistant_messages": sum(
            int(item.get("assistant_messages", 0)) for item in results
        ),
        "assistant_delivery_violations": sum(
            item.get("assistant_messages") != 1 for item in results
        ),
        "direct_runs": direct_runs,
        "direct_rate": _rate(direct_runs, cases),
        "single_model_call_runs": single_model_call_runs,
        "single_model_call_rate": _rate(single_model_call_runs, cases),
        "quality_passed_runs": quality_passed_runs,
        "quality_pass_rate": _rate(quality_passed_runs, cases),
        "content_passed_runs": content_passed_runs,
        "content_pass_rate": _rate(content_passed_runs, cases),
        "repair_attempted_runs": repair_attempted_runs,
        "repair_succeeded_runs": repair_succeeded_runs,
        "repair_success_rate": _rate(repair_succeeded_runs, repair_attempted_runs)
        if repair_attempted_runs
        else 1.0,
        "deterministic_repairs": sum(int(item.get("deterministic_repairs", 0)) for item in results),
        "rewrite_runs": sum(int(item.get("rewrite_attempts", 0)) > 0 for item in results),
        "response_quality_failures": sum(
            item.get("outcome") == "response_quality_failed" for item in results
        ),
        "duplicate_violations": sum(int(item.get("duplicate_violations", 0)) for item in results),
        "planned_runs": sum(item.get("planned") is True for item in results),
        "tool_actions": sum(int(item.get("tool_actions", 0)) for item in results),
        "model_calls": sum(int(item.get("model_calls", 0)) for item in results),
        "duration_p95_ms": _percentile(durations, 0.95),
        "duration_max_ms": max(durations, default=0),
        "repair_probe_passed": repair_probe_passed,
    }
    violations: list[dict[str, Any]] = []
    for field, threshold in _thresholds(baseline.get("minimum"), "minimum").items():
        actual = summary.get(field)
        if not _number(actual) or _as_number(actual) < threshold:
            violations.append(_violation("minimum_not_met", field, f">= {threshold}", actual))
    for field, threshold in _thresholds(baseline.get("maximum"), "maximum").items():
        actual = summary.get(field)
        if not _number(actual) or _as_number(actual) > threshold:
            violations.append(
                _violation("direct_canary_regression", field, f"<= {threshold}", actual)
            )
    if summary["repair_probe_passed"] is not True:
        violations.append(
            _violation(
                "repair_probe_failed",
                "repair_probe_passed",
                "true",
                summary["repair_probe_passed"],
            )
        )
    report = {
        "schema_version": REPORT_SCHEMA_VERSION,
        "generated_at": datetime.now(UTC).isoformat().replace("+00:00", "Z"),
        "decision": "fail" if violations else "pass",
        "source": {
            "baseline_version": baseline.get("version"),
            "baseline_sha256": baseline_sha256,
            "suite_version": suite.get("version"),
            "suite_sha256": suite_sha256,
        },
        "summary": summary,
        "cases": [dict(item) for item in results],
        "violations": violations,
    }
    return report, violations


def validate_report(
    value: Any,
    *,
    baseline_path: Path = Path("evals/agent/baselines/direct-canary.v1.json"),
    suite_path: Path = Path("evals/agent/suites/direct-canary.v1.json"),
) -> dict[str, Any]:
    if not isinstance(value, Mapping) or set(value) != {
        "schema_version",
        "generated_at",
        "decision",
        "source",
        "summary",
        "cases",
        "violations",
    }:
        raise DirectCanaryError("direct Canary report fields are invalid")
    if value.get("schema_version") != REPORT_SCHEMA_VERSION:
        raise DirectCanaryError("direct Canary report schema_version is invalid")
    generated_at = value.get("generated_at")
    if not isinstance(generated_at, str):
        raise DirectCanaryError("direct Canary report timestamp is invalid")
    try:
        timestamp = datetime.fromisoformat(generated_at.replace("Z", "+00:00"))
    except ValueError as exc:
        raise DirectCanaryError("direct Canary report timestamp is invalid") from exc
    if timestamp.tzinfo is None:
        raise DirectCanaryError("direct Canary report timestamp is invalid")
    baseline, baseline_sha256 = load_baseline(baseline_path)
    suite, suite_sha256 = load_suite(suite_path)
    expected_source = {
        "baseline_version": baseline["version"],
        "baseline_sha256": baseline_sha256,
        "suite_version": suite["version"],
        "suite_sha256": suite_sha256,
    }
    if value.get("source") != expected_source:
        raise DirectCanaryError("direct Canary report source is invalid")
    cases = value.get("cases")
    if not isinstance(cases, list) or len(cases) != len(suite["cases"]):
        raise DirectCanaryError("direct Canary report cases are invalid")
    expected_ids = [str(item["id"]) for item in suite["cases"]]
    safe_cases: list[dict[str, Any]] = []
    for expected_id, item in zip(expected_ids, cases, strict=True):
        if not isinstance(item, Mapping) or set(item) != REPORT_CASE_FIELDS:
            raise DirectCanaryError("direct Canary report case fields are invalid")
        result = dict(item)
        if (
            result.get("id") != expected_id
            or result.get("status") not in {
                "completed",
                "failed",
                "cancelled",
                "timed_out",
                "waiting_approval",
                "waiting_tool",
            }
            or result.get("execution_mode") not in {"direct", "single_action", "agentic"}
            or not isinstance(result.get("outcome"), str)
        ):
            raise DirectCanaryError("direct Canary report case identity is invalid")
        for field in (
            "model_calls",
            "duration_ms",
            "assistant_messages",
            "deterministic_repairs",
            "rewrite_attempts",
            "duplicate_violations",
            "tool_actions",
            "sentence_count",
        ):
            item_value = result.get(field)
            if (
                isinstance(item_value, bool)
                or not isinstance(item_value, int)
                or item_value < 0
            ):
                raise DirectCanaryError("direct Canary report case integer is invalid")
        if result["rewrite_attempts"] > 1:
            raise DirectCanaryError("direct Canary report rewrite attempts are invalid")
        for field in (
            "quality_passed",
            "content_passed",
            "repair_attempted",
            "repair_succeeded",
            "planned",
        ):
            if not isinstance(result.get(field), bool):
                raise DirectCanaryError("direct Canary report case boolean is invalid")
        if result["repair_succeeded"] and not result["repair_attempted"]:
            raise DirectCanaryError("direct Canary report repair result is invalid")
        safe_cases.append(result)
    summary = value.get("summary")
    if not isinstance(summary, Mapping):
        raise DirectCanaryError("direct Canary report summary is invalid")
    recomputed, _ = build_report(
        safe_cases,
        baseline=baseline,
        baseline_sha256=baseline_sha256,
        suite=suite,
        suite_sha256=suite_sha256,
    )
    if (
        value.get("decision") != recomputed["decision"]
        or dict(summary) != recomputed["summary"]
        or value.get("violations") != recomputed["violations"]
    ):
        raise DirectCanaryError("direct Canary report summary is inconsistent")
    return {
        "schema_version": REPORT_SCHEMA_VERSION,
        "generated_at": generated_at,
        "decision": recomputed["decision"],
        "source": expected_source,
        "summary": recomputed["summary"],
        "cases": safe_cases,
        "violations": recomputed["violations"],
    }


class DirectCanaryClient:
    def __init__(self, base_url: str, *, request_timeout: float, run_timeout: float) -> None:
        value = base_url.rstrip("/")
        if not value.startswith(("http://127.0.0.1:", "http://localhost:", "https://")):
            raise DirectCanaryError("direct Canary API base URL is invalid")
        self.base_url = value
        self.request_timeout = request_timeout
        self.run_timeout = run_timeout

    def execute(self, suite: Mapping[str, Any], poll_interval: float) -> list[dict[str, Any]]:
        suffix = f"{int(time.time())}-{secrets.token_hex(6)}"
        email = f"agent-direct-canary-{suffix}@example.com"
        registration = self._request(
            "POST",
            "/v1/auth/register",
            {
                "email": email,
                "password": "CanaryPass123!",
                "display_name": "Agent Direct Canary",
                "timezone": "Asia/Shanghai",
                "locale": "zh-CN",
                "device": {
                    "device_key": f"agent-direct-canary-{suffix}",
                    "name": "Agent Direct Canary",
                    "platform": "service",
                },
            },
        )
        token = _required_string(registration, "access_token")
        character_payload = self._request(
            "POST", "/v1/characters", {"module": "companion"}, token
        )
        character = character_payload.get("character")
        if not isinstance(character, Mapping):
            raise DirectCanaryError("direct Canary character response is invalid")
        character_id = _required_string(character, "id")
        conversation = self._request(
            "POST", "/v1/conversations", {"character_id": character_id}, token
        )
        conversation_id = _required_string(conversation, "id")
        results: list[dict[str, Any]] = []
        assistant_messages_seen = 0
        for case in suite.get("cases", []):
            if not isinstance(case, Mapping):
                raise DirectCanaryError("direct Canary case is invalid")
            started_ns = time.perf_counter_ns()
            accepted = self._request(
                "POST",
                f"/v1/conversations/{conversation_id}/messages",
                {"content": str(case["message"])},
                token,
            )
            if accepted.get("runtime") != "agent":
                raise DirectCanaryError("direct Canary was not routed to Agent runtime")
            agent_run = accepted.get("agent_run")
            if not isinstance(agent_run, Mapping):
                raise DirectCanaryError("direct Canary accepted run is invalid")
            run_id = _required_string(agent_run, "id")
            deadline = time.monotonic() + self.run_timeout
            run_payload: dict[str, Any] = {}
            while time.monotonic() < deadline:
                run_payload = self._request("GET", f"/v1/agent-runs/{run_id}", None, token)
                status = run_payload.get("status")
                if status == "completed":
                    break
                if status in {"failed", "cancelled", "timed_out", "waiting_approval", "waiting_tool"}:
                    raise DirectCanaryError("direct Canary Agent Run ended unexpectedly")
                time.sleep(poll_interval)
            if run_payload.get("status") != "completed":
                raise DirectCanaryError("direct Canary Agent Run timed out")
            duration_ms = max(0, (time.perf_counter_ns() - started_ns) // 1_000_000)
            messages = self._request(
                "GET", f"/v1/conversations/{conversation_id}/messages", None, token
            )
            items = messages.get("items")
            if not isinstance(items, list):
                raise DirectCanaryError("direct Canary messages response is invalid")
            assistant_messages = sum(
                isinstance(item, Mapping) and item.get("role") == "assistant"
                for item in items
            )
            delivered = assistant_messages - assistant_messages_seen
            if delivered < 0:
                raise DirectCanaryError("direct Canary assistant message count regressed")
            assistant_messages_seen = assistant_messages
            results.append(
                evaluate_case(
                    case,
                    run_payload,
                    duration_ms,
                    assistant_messages=delivered,
                )
            )
        return results

    def _request(
        self,
        method: str,
        path: str,
        payload: Mapping[str, Any] | None,
        token: str = "",
    ) -> dict[str, Any]:
        body = None if payload is None else json.dumps(payload).encode("utf-8")
        request = urllib.request.Request(
            self.base_url + path,
            data=body,
            method=method,
            headers={
                "Accept": "application/json",
                "Content-Type": "application/json",
                **({"Authorization": f"Bearer {token}"} if token else {}),
            },
        )
        try:
            with urllib.request.urlopen(request, timeout=self.request_timeout) as response:
                raw = response.read(MAX_RESPONSE_BYTES + 1)
        except urllib.error.HTTPError as exc:
            raise DirectCanaryError(f"direct Canary API returned HTTP {exc.code}") from exc
        except (urllib.error.URLError, TimeoutError, OSError) as exc:
            raise DirectCanaryError("direct Canary API request failed") from exc
        if len(raw) > MAX_RESPONSE_BYTES:
            raise DirectCanaryError("direct Canary API response is too large")
        try:
            value = json.loads(raw, object_pairs_hook=_reject_duplicate_keys)
        except (UnicodeDecodeError, json.JSONDecodeError, ValueError) as exc:
            raise DirectCanaryError("direct Canary API response is invalid") from exc
        if not isinstance(value, dict):
            raise DirectCanaryError("direct Canary API response must be an object")
        return value


def write_report(path: Path, report: Mapping[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    path.chmod(0o600)


def write_junit(path: Path, report: Mapping[str, Any]) -> None:
    violations = report.get("violations")
    failures = violations if isinstance(violations, list) else []
    suite = ElementTree.Element(
        "testsuite",
        name="agent-direct-canary",
        tests="1",
        failures="1" if failures else "0",
    )
    case = ElementTree.SubElement(suite, "testcase", name="direct-quality-and-latency")
    if failures:
        failure = ElementTree.SubElement(case, "failure", message="direct Canary failed")
        failure.text = json.dumps(failures, ensure_ascii=False)
    output = ElementTree.SubElement(case, "system-out")
    output.text = json.dumps(report.get("summary", {}), ensure_ascii=False, sort_keys=True)
    path.parent.mkdir(parents=True, exist_ok=True)
    ElementTree.ElementTree(suite).write(path, encoding="utf-8", xml_declaration=True)
    path.chmod(0o600)


def run(argv: Sequence[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Run structured online Agent direct Canary")
    parser.add_argument(
        "--api-base-url",
        default=os.environ.get("API_BASE_URL") or "http://127.0.0.1:8080",
    )
    parser.add_argument(
        "--baseline",
        type=Path,
        default=Path(os.environ.get("AGENT_DIRECT_CANARY_BASELINE", "evals/agent/baselines/direct-canary.v1.json")),
    )
    parser.add_argument(
        "--suite",
        type=Path,
        default=Path(os.environ.get("AGENT_DIRECT_CANARY_SUITE", "evals/agent/suites/direct-canary.v1.json")),
    )
    parser.add_argument(
        "--json-report",
        type=Path,
        default=Path(os.environ.get("AGENT_DIRECT_CANARY_JSON_REPORT", "artifacts/agent-eval/direct-canary.json")),
    )
    parser.add_argument(
        "--junit-report",
        type=Path,
        default=Path(os.environ.get("AGENT_DIRECT_CANARY_JUNIT_REPORT", "artifacts/agent-eval/direct-canary.xml")),
    )
    parser.add_argument("--request-timeout", type=float, default=10.0)
    parser.add_argument("--run-timeout", type=float, default=45.0)
    parser.add_argument("--poll-interval", type=float, default=0.25)
    args = parser.parse_args(argv)
    try:
        if not 1 <= args.request_timeout <= 60 or not 1 <= args.run_timeout <= 300:
            raise DirectCanaryError("direct Canary timeout is invalid")
        if not 0.05 <= args.poll_interval <= 5:
            raise DirectCanaryError("direct Canary poll interval is invalid")
        baseline, baseline_sha256 = load_baseline(args.baseline)
        suite, suite_sha256 = load_suite(args.suite)
        results = DirectCanaryClient(
            args.api_base_url,
            request_timeout=args.request_timeout,
            run_timeout=args.run_timeout,
        ).execute(suite, args.poll_interval)
        report, violations = build_report(
            results,
            baseline=baseline,
            baseline_sha256=baseline_sha256,
            suite=suite,
            suite_sha256=suite_sha256,
        )
        write_report(args.json_report, report)
        write_junit(args.junit_report, report)
    except (OSError, DirectCanaryError, ValueError) as exc:
        print(f"agent_direct_canary=error reason={type(exc).__name__}", file=sys.stderr)
        return 2
    summary = report["summary"]
    print(
        "agent_direct_canary={} cases={} direct_rate={:.3f} single_call_rate={:.3f} "
        "quality_rate={:.3f} duplicates={} p95_ms={:.0f}".format(
            report["decision"],
            summary["cases"],
            summary["direct_rate"],
            summary["single_model_call_rate"],
            summary["quality_pass_rate"],
            summary["duplicate_violations"],
            summary["duration_p95_ms"],
        )
    )
    print(f"agent_direct_canary_report={args.json_report}")
    return 1 if violations else 0


def _load_config(path: Path, name: str) -> tuple[dict[str, Any], str]:
    try:
        metadata = path.lstat()
        raw = path.read_bytes()
    except OSError as exc:
        raise DirectCanaryError(f"cannot read {name}") from exc
    if not stat.S_ISREG(metadata.st_mode) or path.is_symlink() or not 0 < len(raw) <= MAX_CONFIG_BYTES:
        raise DirectCanaryError(f"{name} file is unsafe")
    try:
        value = json.loads(raw, object_pairs_hook=_reject_duplicate_keys)
    except (UnicodeDecodeError, json.JSONDecodeError, ValueError) as exc:
        raise DirectCanaryError(f"{name} JSON is invalid") from exc
    if not isinstance(value, dict):
        raise DirectCanaryError(f"{name} must be an object")
    return value, hashlib.sha256(raw).hexdigest()


def _thresholds(value: Any, name: str) -> dict[str, float]:
    if not isinstance(value, Mapping) or not value:
        raise DirectCanaryError(f"direct Canary {name} thresholds are invalid")
    result: dict[str, float] = {}
    for field, threshold in value.items():
        if (
            not isinstance(field, str)
            or not field
            or not _number(threshold)
            or float(threshold) < 0
        ):
            raise DirectCanaryError(f"direct Canary {name} threshold is invalid")
        result[field] = float(threshold)
    return result


def _violation(code: str, field: str, expected: str, actual: Any) -> dict[str, Any]:
    return {
        "code": code,
        "path": f"/summary/{field}",
        "expected": expected,
        "actual": actual,
    }


def _rate(numerator: int, denominator: int) -> float:
    return round(numerator / denominator, 6) if denominator else 0.0


def _percentile(values: Sequence[int], quantile: float) -> float:
    if not values:
        return 0.0
    ordered = sorted(values)
    index = max(0, min(len(ordered) - 1, math.ceil(len(ordered) * quantile) - 1))
    return float(ordered[index])


def _number(value: Any) -> bool:
    return isinstance(value, (int, float)) and not isinstance(value, bool) and math.isfinite(float(value))


def _as_number(value: Any) -> float:
    if not _number(value):
        raise DirectCanaryError("direct Canary numeric value is invalid")
    return float(value)


def _required_string(value: Mapping[str, Any], field: str) -> str:
    result = value.get(field)
    if not isinstance(result, str) or not result.strip():
        raise DirectCanaryError(f"direct Canary response field {field} is invalid")
    return result.strip()


def _reject_duplicate_keys(pairs: list[tuple[str, Any]]) -> dict[str, Any]:
    result: dict[str, Any] = {}
    for key, value in pairs:
        if key in result:
            raise ValueError(f"duplicate JSON key: {key}")
        result[key] = value
    return result


if __name__ == "__main__":
    raise SystemExit(run())
