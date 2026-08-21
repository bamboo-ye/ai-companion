from __future__ import annotations

import argparse
import json
import math
import sys
from collections import Counter
from pathlib import Path
from typing import Any, Mapping, Sequence, TypeGuard
from xml.etree import ElementTree

REPORT_SCHEMA_VERSION = "agent-performance-report-v2"
BASELINE_SCHEMA_VERSION = "agent-performance-baseline-v2"
DISPATCH_MESSAGE = "Agent run dispatch completed"
PYTHON_MESSAGE = "Agent Python execution"
WARMUP_MESSAGE = "Agent Python pool warmup"
TOOL_WAKE_MESSAGE = "Agent tool task wake"


class PerformanceEvalError(ValueError):
    pass


def load_observations(path: Path) -> dict[str, list[dict[str, Any]]]:
    dispatch: list[dict[str, Any]] = []
    python: list[dict[str, Any]] = []
    warmup: list[dict[str, Any]] = []
    tool_wake: list[dict[str, Any]] = []
    for line_number, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
        if not line.strip():
            continue
        source = f"{path}:{line_number}"
        try:
            value = json.loads(line)
        except json.JSONDecodeError as exc:
            raise PerformanceEvalError(f"{source}: invalid JSON log line") from exc
        if not isinstance(value, Mapping):
            raise PerformanceEvalError(f"{source}: log line must be an object")
        message = value.get("msg")
        if message == DISPATCH_MESSAGE:
            dispatch.append(_dispatch_observation(value, source))
        elif message == PYTHON_MESSAGE:
            python.append(_python_observation(value, source))
        elif message == WARMUP_MESSAGE:
            warmup.append(_warmup_observation(value, source))
        elif message == TOOL_WAKE_MESSAGE:
            tool_wake.append(_tool_wake_observation(value, source))
    return {
        "dispatch": dispatch,
        "python": python,
        "warmup": warmup,
        "tool_wake": tool_wake,
    }


def build_report(observations: Mapping[str, Sequence[Mapping[str, Any]]]) -> dict[str, Any]:
    dispatch = list(observations.get("dispatch", []))
    python = list(observations.get("python", []))
    warmup = list(observations.get("warmup", []))
    tool_wake = [
        item
        for item in observations.get("tool_wake", [])
        if _number(item.get("awakened_runs", 0)) > 0
    ]
    processed_dispatch = [
        item
        for item in dispatch
        if item.get("processed") is True and item.get("outcome") == "processed"
    ]
    queue_wait = [_number(item["queue_wait_ms"]) for item in processed_dispatch]
    dispatch_duration = [
        _number(item["execution_duration_ms"]) for item in processed_dispatch
    ]
    pool_wait = [_number(item["pool_wait_ms"]) for item in python]
    python_duration = [_number(item["duration_ms"]) for item in python]
    model_duration = [_number(item["model_duration_ms"]) for item in python]
    max_model_attempt_timeout = [
        _number(item["max_model_attempt_timeout_ms"]) for item in python
    ]
    runtime_overhead = [_number(item["runtime_overhead_ms"]) for item in python]
    direct_duration = [
        _number(item["duration_ms"])
        for item in python
        if item.get("execution_mode") == "direct"
    ]
    warm_direct_duration = [
        _number(item["duration_ms"])
        for item in python
        if item.get("execution_mode") == "direct" and item.get("cold_start") is False
    ]
    direct_model_duration = [
        _number(item["model_duration_ms"])
        for item in python
        if item.get("execution_mode") == "direct"
    ]
    direct_max_model_attempt_timeout = [
        _number(item["max_model_attempt_timeout_ms"])
        for item in python
        if item.get("execution_mode") == "direct"
    ]
    direct_runtime_overhead = [
        _number(item["runtime_overhead_ms"])
        for item in python
        if item.get("execution_mode") == "direct"
    ]
    dispatch_errors = sum(item.get("outcome") == "error" for item in dispatch)
    python_errors = sum(item.get("success") is not True for item in python)
    cold_starts = sum(item.get("cold_start") is True for item in python)
    recycles = sum(item.get("recycled") is True for item in python)
    warmup_errors = sum(item.get("success") is not True for item in warmup)
    processed_run_ids = {str(item["run_id"]) for item in processed_dispatch}
    python_run_ids = {str(item["run_id"]) for item in python}
    return {
        "schema_version": REPORT_SCHEMA_VERSION,
        "source": {
            "dispatch_event": DISPATCH_MESSAGE,
            "python_event": PYTHON_MESSAGE,
            "warmup_event": WARMUP_MESSAGE,
            "tool_wake_event": TOOL_WAKE_MESSAGE,
        },
        "summary": {
            "dispatch_samples": len(dispatch),
            "processed_dispatch_samples": len(processed_dispatch),
            "paired_execution_samples": len(processed_run_ids & python_run_ids),
            "python_samples": len(python),
            "direct_python_samples": len(direct_duration),
            "warm_direct_python_samples": len(warm_direct_duration),
            "warmup_samples": len(warmup),
            "warmup_ready_processes": sum(int(item["ready"]) for item in warmup),
            "warmup_duration_p95_ms": _percentile(
                [_number(item["duration_ms"]) for item in warmup], 0.95
            ),
            "warmup_error_rate": _rate(warmup_errors, len(warmup)),
            "tool_wake_samples": len(tool_wake),
            "awakened_tool_run_samples": sum(
                int(item["awakened_runs"]) for item in tool_wake
            ),
            "tool_wake_event_age_p95_ms": _percentile(
                [_number(item["event_age_ms"]) for item in tool_wake], 0.95
            ),
            "tool_wake_duration_p95_ms": _percentile(
                [_number(item["wake_duration_ms"]) for item in tool_wake], 0.95
            ),
            "tool_wake_latency_p95_ms": _percentile(
                [_number(item["wake_latency_ms"]) for item in tool_wake], 0.95
            ),
            "queue_wait_p50_ms": _percentile(queue_wait, 0.50),
            "queue_wait_p95_ms": _percentile(queue_wait, 0.95),
            "dispatch_duration_p50_ms": _percentile(dispatch_duration, 0.50),
            "dispatch_duration_p95_ms": _percentile(dispatch_duration, 0.95),
            "pool_wait_p50_ms": _percentile(pool_wait, 0.50),
            "pool_wait_p95_ms": _percentile(pool_wait, 0.95),
            "python_duration_p50_ms": _percentile(python_duration, 0.50),
            "python_duration_p95_ms": _percentile(python_duration, 0.95),
            "model_duration_p50_ms": _percentile(model_duration, 0.50),
            "model_duration_p95_ms": _percentile(model_duration, 0.95),
            "max_model_attempt_timeout_p95_ms": _percentile(
                max_model_attempt_timeout, 0.95
            ),
            "runtime_overhead_p50_ms": _percentile(runtime_overhead, 0.50),
            "runtime_overhead_p95_ms": _percentile(runtime_overhead, 0.95),
            "direct_python_duration_p95_ms": _percentile(direct_duration, 0.95),
            "direct_model_duration_p95_ms": _percentile(direct_model_duration, 0.95),
            "direct_max_model_attempt_timeout_p95_ms": _percentile(
                direct_max_model_attempt_timeout, 0.95
            ),
            "direct_runtime_overhead_p95_ms": _percentile(
                direct_runtime_overhead, 0.95
            ),
            "warm_direct_python_duration_p95_ms": _percentile(
                warm_direct_duration, 0.95
            ),
            "dispatch_error_rate": _rate(dispatch_errors, len(dispatch)),
            "python_error_rate": _rate(python_errors, len(python)),
            "cold_start_rate": _rate(cold_starts, len(python)),
            "recycle_rate": _rate(recycles, len(python)),
        },
        "breakdown": {
            "dispatch_outcomes": dict(sorted(Counter(item["outcome"] for item in dispatch).items())),
            "execution_modes": dict(
                sorted(Counter(item["execution_mode"] or "unknown" for item in python).items())
            ),
            "python_error_codes": dict(
                sorted(
                    Counter(
                        item["error_code"] or "unclassified"
                        for item in python
                        if item.get("success") is not True
                    ).items()
                )
            ),
            "tool_wake_event_types": dict(
                sorted(Counter(item["event_type"] for item in tool_wake).items())
            ),
        },
        "regression": {"baseline_version": "", "violations": []},
    }


def apply_baseline(
    report: dict[str, Any], baseline: Mapping[str, Any]
) -> list[dict[str, Any]]:
    if baseline.get("schema_version") != BASELINE_SCHEMA_VERSION:
        raise PerformanceEvalError("unsupported Agent performance baseline schema_version")
    summary = report.get("summary")
    if not isinstance(summary, Mapping):
        raise PerformanceEvalError("Agent performance report summary is invalid")
    violations: list[dict[str, Any]] = []
    for field, threshold in _thresholds(baseline.get("minimum"), "minimum").items():
        actual = summary.get(field)
        if not _is_number(actual) or actual < threshold:
            violations.append(_violation(field, f">= {threshold}", actual, "minimum_not_met"))
    for field, threshold in _thresholds(baseline.get("maximum"), "maximum").items():
        actual = summary.get(field)
        if not _is_number(actual) or actual > threshold:
            violations.append(
                _violation(field, f"<= {threshold}", actual, "performance_regression")
            )
    report["regression"] = {
        "baseline_version": str(baseline.get("version") or ""),
        "violations": violations,
    }
    return violations


def write_junit(path: Path, report: Mapping[str, Any]) -> None:
    regression = report.get("regression")
    regression = regression if isinstance(regression, Mapping) else {}
    violations = regression.get("violations")
    violations = violations if isinstance(violations, list) else []
    suite = ElementTree.Element(
        "testsuite",
        name="agent-performance",
        tests="1",
        failures="1" if violations else "0",
    )
    case = ElementTree.SubElement(suite, "testcase", name="performance-baseline")
    if violations:
        failure = ElementTree.SubElement(case, "failure", message="performance regression")
        failure.text = json.dumps(violations, ensure_ascii=False)
    output = ElementTree.SubElement(case, "system-out")
    output.text = json.dumps(report.get("summary", {}), ensure_ascii=False, sort_keys=True)
    path.parent.mkdir(parents=True, exist_ok=True)
    ElementTree.ElementTree(suite).write(path, encoding="utf-8", xml_declaration=True)


def main(argv: Sequence[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Evaluate structured Agent worker performance logs")
    parser.add_argument("--input", type=Path, required=True)
    parser.add_argument("--baseline", type=Path, required=True)
    parser.add_argument("--json-report", type=Path)
    parser.add_argument("--junit-report", type=Path)
    args = parser.parse_args(argv)
    try:
        observations = load_observations(args.input)
        baseline = json.loads(args.baseline.read_text(encoding="utf-8"))
        if not isinstance(baseline, Mapping):
            raise PerformanceEvalError("Agent performance baseline must be an object")
        report = build_report(observations)
        violations = apply_baseline(report, baseline)
    except (OSError, PerformanceEvalError, ValueError) as exc:
        print(f"agent performance eval error: {exc}", file=sys.stderr)
        return 2
    encoded = json.dumps(report, ensure_ascii=False, indent=2)
    if args.json_report:
        args.json_report.parent.mkdir(parents=True, exist_ok=True)
        args.json_report.write_text(encoded + "\n", encoding="utf-8")
    if args.junit_report:
        write_junit(args.junit_report, report)
    print(encoded)
    return 1 if violations else 0


def _dispatch_observation(value: Mapping[str, Any], source: str) -> dict[str, Any]:
    outcome = _required_string(value, "outcome", source)
    if outcome not in {"processed", "error", "not_claimed"}:
        raise PerformanceEvalError(f"{source}: invalid outcome")
    processed = value.get("processed")
    if not isinstance(processed, bool):
        raise PerformanceEvalError(f"{source}: processed must be a boolean")
    return {
        "run_id": _required_string(value, "run_id", source),
        "queue_wait_ms": _nonnegative_number(value, "queue_wait_ms", source),
        "execution_duration_ms": _nonnegative_number(
            value, "execution_duration_ms", source
        ),
        "processed": processed,
        "outcome": outcome,
    }


def _python_observation(value: Mapping[str, Any], source: str) -> dict[str, Any]:
    return {
        "run_id": _required_string(value, "run_id", source),
        "execution_mode": _optional_string(value.get("execution_mode")),
        "pool_wait_ms": _nonnegative_number(value, "pool_wait_ms", source),
        "duration_ms": _nonnegative_number(value, "duration_ms", source),
        "model_calls": _nonnegative_number(value, "model_calls", source),
        "model_duration_ms": _nonnegative_number(value, "model_duration_ms", source),
        "max_model_attempt_timeout_ms": _nonnegative_number(
            value, "max_model_attempt_timeout_ms", source
        ),
        "runtime_overhead_ms": _nonnegative_number(value, "runtime_overhead_ms", source),
        "cold_start": _required_bool(value, "cold_start", source),
        "recycled": _required_bool(value, "recycled", source),
        "success": _required_bool(value, "success", source),
        "error_code": _optional_string(value.get("error_code")),
    }


def _warmup_observation(value: Mapping[str, Any], source: str) -> dict[str, Any]:
    return {
        "requested": _nonnegative_number(value, "requested", source),
        "ready": _nonnegative_number(value, "ready", source),
        "duration_ms": _nonnegative_number(value, "duration_ms", source),
        "success": _required_bool(value, "success", source),
    }


def _tool_wake_observation(value: Mapping[str, Any], source: str) -> dict[str, Any]:
    event_type = _required_string(value, "event_type", source)
    if event_type not in {
        "skill.run.succeeded.v1",
        "skill.run.failed.v1",
        "skill.run.cancelled.v1",
    }:
        raise PerformanceEvalError(f"{source}: invalid tool wake event_type")
    awakened_runs = _nonnegative_number(value, "awakened_runs", source)
    if not awakened_runs.is_integer():
        raise PerformanceEvalError(f"{source}: awakened_runs must be an integer")
    return {
        "task_id": _required_string(value, "task_id", source),
        "event_type": event_type,
        "event_age_ms": _nonnegative_number(value, "event_age_ms", source),
        "wake_duration_ms": _nonnegative_number(value, "wake_duration_ms", source),
        "wake_latency_ms": _nonnegative_number(value, "wake_latency_ms", source),
        "awakened_runs": awakened_runs,
    }


def _required_string(value: Mapping[str, Any], field: str, source: str) -> str:
    result = value.get(field)
    if not isinstance(result, str) or not result.strip():
        raise PerformanceEvalError(f"{source}: {field} is required")
    return result.strip()


def _optional_string(value: Any) -> str:
    return value.strip() if isinstance(value, str) else ""


def _required_bool(value: Mapping[str, Any], field: str, source: str) -> bool:
    result = value.get(field)
    if not isinstance(result, bool):
        raise PerformanceEvalError(f"{source}: {field} must be a boolean")
    return result


def _nonnegative_number(value: Mapping[str, Any], field: str, source: str) -> float:
    result = value.get(field)
    if not _is_number(result) or not math.isfinite(float(result)) or result < 0:
        raise PerformanceEvalError(f"{source}: {field} must be a finite non-negative number")
    return _number(result)


def _thresholds(value: Any, name: str) -> dict[str, float]:
    if not isinstance(value, Mapping):
        raise PerformanceEvalError(f"Agent performance baseline {name} must be an object")
    result: dict[str, float] = {}
    for field, threshold in value.items():
        if not isinstance(field, str) or not _is_number(threshold) or threshold < 0:
            raise PerformanceEvalError(f"Agent performance baseline {name} is invalid")
        result[field] = _number(threshold)
    return result


def _rate(count: int, total: int) -> float:
    return round(count / total, 6) if total else 0.0


def _percentile(values: Sequence[float], percentile: float) -> float:
    if not values:
        return 0.0
    ordered = sorted(values)
    index = max(0, min(len(ordered) - 1, math.ceil(len(ordered) * percentile) - 1))
    return _number(ordered[index])


def _number(value: Any) -> float:
    return round(float(value), 6)


def _is_number(value: Any) -> TypeGuard[int | float]:
    return isinstance(value, (int, float)) and not isinstance(value, bool)


def _violation(field: str, expected: Any, actual: Any, code: str) -> dict[str, Any]:
    return {
        "code": code,
        "severity": "hard",
        "path": f"/summary/{field}",
        "expected": expected,
        "actual": actual,
    }


if __name__ == "__main__":
    raise SystemExit(main())
