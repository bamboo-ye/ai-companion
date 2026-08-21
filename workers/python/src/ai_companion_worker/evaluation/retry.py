from __future__ import annotations

import argparse
import json
import math
import sys
from collections import Counter, defaultdict
from pathlib import Path
from typing import Any, Mapping, Sequence, TypeGuard
from xml.etree import ElementTree

REPORT_SCHEMA_VERSION = "agent-retry-report-v1"
BASELINE_SCHEMA_VERSION = "agent-retry-baseline-v1"
READY_MESSAGE = "Agent worker ready"
RETRY_MESSAGE = "Agent execution retry scheduled"
PYTHON_MESSAGE = "Agent Python execution"


class RetryEvalError(ValueError):
    pass


def load_observations(path: Path) -> dict[str, list[dict[str, Any]]]:
    observations: dict[str, list[dict[str, Any]]] = {
        "ready": [],
        "retry": [],
        "python": [],
    }
    for line_number, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
        if not line.strip():
            continue
        source = f"{path}:{line_number}"
        try:
            value = json.loads(line)
        except json.JSONDecodeError as exc:
            raise RetryEvalError(f"{source}: invalid JSON log line") from exc
        if not isinstance(value, Mapping):
            raise RetryEvalError(f"{source}: log line must be an object")
        message = value.get("msg")
        if message == READY_MESSAGE:
            observations["ready"].append(_ready_observation(value, source, line_number))
        elif message == RETRY_MESSAGE:
            observations["retry"].append(_retry_observation(value, source, line_number))
        elif message == PYTHON_MESSAGE:
            observations["python"].append(_python_observation(value, source, line_number))
    return observations


def build_report(observations: Mapping[str, Sequence[Mapping[str, Any]]]) -> dict[str, Any]:
    ready = sorted(observations.get("ready", []), key=_sequence)
    retries = sorted(observations.get("retry", []), key=_sequence)
    python = sorted(observations.get("python", []), key=_sequence)
    retry_by_run: dict[str, list[Mapping[str, Any]]] = defaultdict(list)
    python_by_run: dict[str, list[Mapping[str, Any]]] = defaultdict(list)
    for item in retries:
        retry_by_run[str(item["run_id"])].append(item)
    for item in python:
        python_by_run[str(item["run_id"])].append(item)

    duplicate_schedule_count = sum(
        count - 1
        for count in Counter(
            (str(item["run_id"]), int(item["attempt"])) for item in retries
        ).values()
        if count > 1
    )
    attempt_sequence_violations = 0
    attempt_limit_violations = 0
    delay_policy_violations = 0
    deadline_violations = 0
    policy_config_mismatches = 0
    orphan_retries = 0
    recovered_runs = 0
    duplicate_completions = 0

    for run_id, run_retries in retry_by_run.items():
        attempts = [int(item["attempt"]) for item in run_retries]
        if attempts != list(range(1, len(attempts) + 1)):
            attempt_sequence_violations += 1
        previous_retry_sequence = 0
        run_python = python_by_run.get(run_id, [])
        for item in run_retries:
            attempt = int(item["attempt"])
            if attempt >= int(item["maximum_attempts"]):
                attempt_limit_violations += 1
            if not _delay_matches_policy(item):
                delay_policy_violations += 1
            if float(item["deadline_remaining_ms"]) <= float(item["delay_ms"]):
                deadline_violations += 1
            if not _matches_latest_ready(item, ready):
                policy_config_mismatches += 1
            item_sequence = int(item["sequence"])
            has_preceding_failure = any(
                previous_retry_sequence < int(execution["sequence"]) < item_sequence
                and execution.get("success") is False
                for execution in run_python
            )
            if not has_preceding_failure:
                orphan_retries += 1
            previous_retry_sequence = item_sequence

        last_retry_sequence = int(run_retries[-1]["sequence"])
        completions = [
            item
            for item in run_python
            if int(item["sequence"]) > last_retry_sequence
            and item.get("success") is True
            and item.get("result_status") == "completed"
        ]
        if completions:
            recovered_runs += 1
        if len(completions) > 1:
            duplicate_completions += len(completions) - 1

    retry_runs = len(retry_by_run)
    unrecovered_runs = retry_runs - recovered_runs
    return {
        "schema_version": REPORT_SCHEMA_VERSION,
        "source": {
            "ready_event": READY_MESSAGE,
            "retry_event": RETRY_MESSAGE,
            "python_event": PYTHON_MESSAGE,
        },
        "summary": {
            "ready_samples": len(ready),
            "retry_events": len(retries),
            "retry_runs": retry_runs,
            "retry_after_events": sum(item["policy"] == "retry_after" for item in retries),
            "exponential_events": sum(item["policy"] == "exponential" for item in retries),
            "recovered_runs": recovered_runs,
            "unrecovered_runs": unrecovered_runs,
            "retry_recovery_rate": _rate(recovered_runs, retry_runs),
            "max_scheduled_delay_ms": max(
                (float(item["delay_ms"]) for item in retries), default=0.0
            ),
            "duplicate_schedule_count": duplicate_schedule_count,
            "attempt_sequence_violation_count": attempt_sequence_violations,
            "attempt_limit_violation_count": attempt_limit_violations,
            "delay_policy_violation_count": delay_policy_violations,
            "deadline_violation_count": deadline_violations,
            "policy_config_mismatch_count": policy_config_mismatches,
            "orphan_retry_count": orphan_retries,
            "duplicate_completion_count": duplicate_completions,
        },
        "breakdown": {
            "policies": dict(sorted(Counter(str(item["policy"]) for item in retries).items())),
            "provider_statuses": dict(
                sorted(Counter(str(item["provider_status"]) for item in retries).items())
            ),
            "error_codes": dict(
                sorted(Counter(str(item["error_code"]) for item in retries).items())
            ),
        },
        "regression": {"baseline_version": "", "violations": []},
    }


def apply_baseline(report: dict[str, Any], baseline: Mapping[str, Any]) -> list[dict[str, Any]]:
    if baseline.get("schema_version") != BASELINE_SCHEMA_VERSION:
        raise RetryEvalError("unsupported Agent retry baseline schema_version")
    summary = report.get("summary")
    if not isinstance(summary, Mapping):
        raise RetryEvalError("Agent retry report summary is invalid")
    violations: list[dict[str, Any]] = []
    for field, threshold in _thresholds(baseline.get("minimum"), "minimum").items():
        actual = summary.get(field)
        if not _is_number(actual) or actual < threshold:
            violations.append(_violation(field, f">= {threshold}", actual, "minimum_not_met"))
    for field, threshold in _thresholds(baseline.get("maximum"), "maximum").items():
        actual = summary.get(field)
        if not _is_number(actual) or actual > threshold:
            violations.append(_violation(field, f"<= {threshold}", actual, "retry_regression"))
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
        name="agent-retry",
        tests="1",
        failures="1" if violations else "0",
    )
    case = ElementTree.SubElement(suite, "testcase", name="retry-baseline")
    if violations:
        failure = ElementTree.SubElement(case, "failure", message="retry regression")
        failure.text = json.dumps(violations, ensure_ascii=False)
    output = ElementTree.SubElement(case, "system-out")
    output.text = json.dumps(report.get("summary", {}), ensure_ascii=False, sort_keys=True)
    path.parent.mkdir(parents=True, exist_ok=True)
    ElementTree.ElementTree(suite).write(path, encoding="utf-8", xml_declaration=True)


def main(argv: Sequence[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Evaluate structured Agent retry logs")
    parser.add_argument("--input", type=Path, required=True)
    parser.add_argument("--baseline", type=Path, required=True)
    parser.add_argument("--json-report", type=Path)
    parser.add_argument("--junit-report", type=Path)
    args = parser.parse_args(argv)
    try:
        observations = load_observations(args.input)
        baseline = json.loads(args.baseline.read_text(encoding="utf-8"))
        if not isinstance(baseline, Mapping):
            raise RetryEvalError("Agent retry baseline must be an object")
        report = build_report(observations)
        violations = apply_baseline(report, baseline)
    except (OSError, RetryEvalError, ValueError) as exc:
        print(f"agent retry eval error: {exc}", file=sys.stderr)
        return 2
    encoded = json.dumps(report, ensure_ascii=False, indent=2)
    if args.json_report:
        args.json_report.parent.mkdir(parents=True, exist_ok=True)
        args.json_report.write_text(encoded + "\n", encoding="utf-8")
    if args.junit_report:
        write_junit(args.junit_report, report)
    print(encoded)
    return 1 if violations else 0


def _ready_observation(
    value: Mapping[str, Any], source: str, sequence: int
) -> dict[str, Any]:
    return {
        "sequence": sequence,
        "maximum_attempts": _positive_int(value, "max_execution_attempts", source),
        "base_delay_ms": _positive_number(value, "retry_base_delay_ms", source),
        "max_delay_ms": _positive_number(value, "retry_max_delay_ms", source),
        "jitter_percent": _percentage(value, "retry_jitter_percent", source),
    }


def _retry_observation(
    value: Mapping[str, Any], source: str, sequence: int
) -> dict[str, Any]:
    policy = _required_string(value, "policy", source)
    if policy not in {"retry_after", "exponential"}:
        raise RetryEvalError(f"{source}: invalid retry policy")
    provider_status = _nonnegative_int(value, "provider_status", source)
    if provider_status > 599:
        raise RetryEvalError(f"{source}: provider_status must not exceed 599")
    return {
        "sequence": sequence,
        "run_id": _required_string(value, "run_id", source),
        "attempt": _positive_int(value, "attempt", source),
        "maximum_attempts": _positive_int(value, "maximum_attempts", source),
        "delay_ms": _nonnegative_number(value, "delay_ms", source),
        "retry_after_ms": _nonnegative_number(value, "retry_after_ms", source),
        "base_delay_ms": _positive_number(value, "base_delay_ms", source),
        "max_delay_ms": _positive_number(value, "max_delay_ms", source),
        "jitter_percent": _percentage(value, "jitter_percent", source),
        "policy": policy,
        "provider_status": provider_status,
        "error_code": _required_string(value, "error_code", source),
        "deadline_remaining_ms": _positive_number(
            value, "deadline_remaining_ms", source
        ),
    }


def _python_observation(
    value: Mapping[str, Any], source: str, sequence: int
) -> dict[str, Any]:
    return {
        "sequence": sequence,
        "run_id": _required_string(value, "run_id", source),
        "success": _required_bool(value, "success", source),
        "result_status": _optional_string(value.get("result_status")),
        "error_code": _optional_string(value.get("error_code")),
    }


def _delay_matches_policy(item: Mapping[str, Any]) -> bool:
    delay = float(item["delay_ms"])
    maximum = float(item["max_delay_ms"])
    jitter = float(item["jitter_percent"])
    if delay > maximum:
        return False
    if item["policy"] == "retry_after":
        advised = float(item["retry_after_ms"])
        if int(item["provider_status"]) not in (429, 503) or advised <= 0:
            return False
        lower = min(advised, maximum)
    else:
        if float(item["retry_after_ms"]) != 0:
            return False
        exponent = max(0, int(item["attempt"]) - 1)
        lower = min(float(item["base_delay_ms"]) * (2**exponent), maximum)
    upper = min(lower + lower * jitter / 100, maximum)
    return lower <= delay <= upper


def _matches_latest_ready(
    retry: Mapping[str, Any], ready: Sequence[Mapping[str, Any]]
) -> bool:
    candidates = [item for item in ready if _sequence(item) < _sequence(retry)]
    if not candidates:
        return False
    latest = candidates[-1]
    return (
        int(retry["maximum_attempts"]) == int(latest["maximum_attempts"])
        and float(retry["base_delay_ms"]) == float(latest["base_delay_ms"])
        and float(retry["max_delay_ms"]) == float(latest["max_delay_ms"])
        and int(retry["jitter_percent"]) == int(latest["jitter_percent"])
    )


def _sequence(item: Mapping[str, Any]) -> int:
    return int(item.get("sequence", 0))


def _required_string(value: Mapping[str, Any], field: str, source: str) -> str:
    result = value.get(field)
    if not isinstance(result, str) or not result.strip():
        raise RetryEvalError(f"{source}: {field} is required")
    return result.strip()


def _optional_string(value: Any) -> str:
    return value.strip() if isinstance(value, str) else ""


def _required_bool(value: Mapping[str, Any], field: str, source: str) -> bool:
    result = value.get(field)
    if not isinstance(result, bool):
        raise RetryEvalError(f"{source}: {field} must be a boolean")
    return result


def _positive_int(value: Mapping[str, Any], field: str, source: str) -> int:
    result = value.get(field)
    if not isinstance(result, int) or isinstance(result, bool) or result <= 0:
        raise RetryEvalError(f"{source}: {field} must be a positive integer")
    return result


def _nonnegative_int(value: Mapping[str, Any], field: str, source: str) -> int:
    result = value.get(field)
    if not isinstance(result, int) or isinstance(result, bool) or result < 0:
        raise RetryEvalError(f"{source}: {field} must be a non-negative integer")
    return result


def _percentage(value: Mapping[str, Any], field: str, source: str) -> int:
    result = _nonnegative_int(value, field, source)
    if result > 100:
        raise RetryEvalError(f"{source}: {field} must not exceed 100")
    return result


def _nonnegative_number(value: Mapping[str, Any], field: str, source: str) -> float:
    result = value.get(field)
    if not _is_number(result) or not math.isfinite(float(result)) or result < 0:
        raise RetryEvalError(f"{source}: {field} must be a finite non-negative number")
    return _number(result)


def _positive_number(value: Mapping[str, Any], field: str, source: str) -> float:
    result = _nonnegative_number(value, field, source)
    if result <= 0:
        raise RetryEvalError(f"{source}: {field} must be positive")
    return result


def _thresholds(value: Any, name: str) -> dict[str, float]:
    if not isinstance(value, Mapping):
        raise RetryEvalError(f"Agent retry baseline {name} must be an object")
    result: dict[str, float] = {}
    for field, threshold in value.items():
        if not isinstance(field, str) or not _is_number(threshold) or threshold < 0:
            raise RetryEvalError(f"Agent retry baseline {name} is invalid")
        result[field] = _number(threshold)
    return result


def _rate(count: int, total: int) -> float:
    return round(count / total, 6) if total else 0.0


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
