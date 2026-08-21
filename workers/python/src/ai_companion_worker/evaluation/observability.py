from __future__ import annotations

import argparse
import hashlib
import json
import math
import re
import sys
import urllib.error
import urllib.parse
import urllib.request
from datetime import UTC, datetime
from pathlib import Path
from typing import Any, Mapping, Sequence, TypeGuard
from xml.etree import ElementTree


SNAPSHOT_SCHEMA_VERSION = "observability-snapshot-v1"
BASELINE_SCHEMA_VERSION = "observability-release-baseline-v1"
REPORT_SCHEMA_VERSION = "observability-release-report-v1"
MAX_METRICS_BYTES = 2 * 1024 * 1024


class ObservabilityEvalError(ValueError):
    pass


SELECTORS: dict[str, tuple[str, Mapping[str, str]]] = {
    "degradation_level": ("ai_companion_degradation_level", {}),
    "queue_lag": ("ai_companion_queue_lag", {}),
    "oldest_job_age_seconds": ("ai_companion_oldest_job_age_seconds", {}),
    "model_error_ratio": ("ai_companion_model_error_ratio", {}),
    "model_latency_p95_seconds": ("ai_companion_model_latency_p95_seconds", {}),
    "agent_run_duration_p95_seconds": (
        "ai_companion_agent_run_duration_p95_seconds",
        {},
    ),
    "agent_runs_completed": ("ai_companion_agent_runs", {"status": "completed"}),
    "agent_runs_failed": ("ai_companion_agent_runs", {"status": "failed"}),
    "agent_runs_timed_out": ("ai_companion_agent_runs", {"status": "timed_out"}),
    "retry_scheduled": (
        "ai_companion_agent_execution_retries_recent",
        {"outcome": "scheduled"},
    ),
    "retry_recovered": (
        "ai_companion_agent_execution_retries_recent",
        {"outcome": "recovered"},
    ),
    "retry_exhausted": (
        "ai_companion_agent_execution_retries_recent",
        {"outcome": "exhausted"},
    ),
    "retry_deadline_exhausted": (
        "ai_companion_agent_execution_retries_recent",
        {"outcome": "deadline_exhausted"},
    ),
    "retry_recovery_ratio": (
        "ai_companion_agent_execution_retry_recovery_ratio",
        {},
    ),
}
DERIVED_METRICS = {
    "agent_terminal_failures",
    "retry_exhausted_total",
    "retry_settled",
}
METRIC_KEYS = set(SELECTORS) | DERIVED_METRICS
ABSOLUTE_LIMIT_METRICS = {
    "degradation_level",
    "queue_lag",
    "oldest_job_age_seconds",
    "model_error_ratio",
    "model_latency_p95_seconds",
    "agent_run_duration_p95_seconds",
    "retry_scheduled",
    "retry_exhausted_total",
}
RELATIVE_LIMIT_METRICS = {
    "queue_lag",
    "oldest_job_age_seconds",
    "model_error_ratio",
    "model_latency_p95_seconds",
    "agent_run_duration_p95_seconds",
    "agent_terminal_failures",
    "retry_scheduled",
    "retry_exhausted_total",
}
INTEGER_METRICS = {
    "degradation_level",
    "queue_lag",
    "agent_runs_completed",
    "agent_runs_failed",
    "agent_runs_timed_out",
    "retry_scheduled",
    "retry_recovered",
    "retry_exhausted",
    "retry_deadline_exhausted",
    "agent_terminal_failures",
    "retry_exhausted_total",
    "retry_settled",
}
TARGET_NAMES = {selector[0] for selector in SELECTORS.values()}
SAMPLE_RE = re.compile(
    r"^([a-zA-Z_:][a-zA-Z0-9_:]*)(?:\{([^}]*)\})?\s+([^\s]+)(?:\s+\d+)?$"
)
LABEL_RE = re.compile(
    r'\s*([a-zA-Z_][a-zA-Z0-9_]*)\s*=\s*("(?:\\.|[^"\\])*")\s*(?:,|$)'
)


def build_snapshot(metrics_text: str, generated_at: str | None = None) -> dict[str, Any]:
    samples = _parse_samples(metrics_text)
    metrics: dict[str, float] = {}
    for key, (name, expected_labels) in SELECTORS.items():
        sample_key = (name, tuple(sorted(expected_labels.items())))
        if sample_key not in samples:
            raise ObservabilityEvalError(f"required metric sample is missing: {key}")
        metrics[key] = samples[sample_key]

    metrics["agent_terminal_failures"] = (
        metrics["agent_runs_failed"] + metrics["agent_runs_timed_out"]
    )
    metrics["retry_exhausted_total"] = (
        metrics["retry_exhausted"] + metrics["retry_deadline_exhausted"]
    )
    metrics["retry_settled"] = (
        metrics["retry_recovered"] + metrics["retry_exhausted_total"]
    )
    _validate_metric_values(metrics, "metrics input")
    return {
        "schema_version": SNAPSHOT_SCHEMA_VERSION,
        "generated_at": generated_at or datetime.now(UTC).isoformat().replace("+00:00", "Z"),
        "source": {
            "input_kind": "prometheus_text",
            "sha256": hashlib.sha256(metrics_text.encode("utf-8")).hexdigest(),
        },
        "metrics": metrics,
    }


def capture_snapshot_from_url(url: str) -> dict[str, Any]:
    return build_snapshot(_load_metrics_input(None, url))


def write_snapshot(path: Path, snapshot: Mapping[str, Any]) -> None:
    _snapshot_metrics(snapshot, "captured")
    _snapshot_digest(snapshot, "captured")
    _write_private_json(path, snapshot)


def load_baseline(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise ObservabilityEvalError(f"invalid observability baseline: {exc}") from exc
    if not isinstance(value, Mapping):
        raise ObservabilityEvalError("observability baseline must be an object")
    baseline = dict(value)
    validate_baseline(baseline)
    return baseline


def load_snapshot(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise ObservabilityEvalError(f"invalid snapshot {path}: {exc}") from exc
    if not isinstance(value, Mapping):
        raise ObservabilityEvalError(f"snapshot {path} must be an object")
    if set(value) != {"schema_version", "generated_at", "source", "metrics"}:
        raise ObservabilityEvalError(f"snapshot {path} fields are invalid")
    if value.get("schema_version") != SNAPSHOT_SCHEMA_VERSION:
        raise ObservabilityEvalError(f"snapshot {path} has unsupported schema_version")
    generated_at = value.get("generated_at")
    if not isinstance(generated_at, str) or not generated_at:
        raise ObservabilityEvalError(f"snapshot {path} generated_at is required")
    _validate_timestamp(generated_at, f"snapshot {path}")
    source = value.get("source")
    if not isinstance(source, Mapping):
        raise ObservabilityEvalError(f"snapshot {path} source is invalid")
    if set(source) != {"input_kind", "sha256"}:
        raise ObservabilityEvalError(f"snapshot {path} source contains unexpected fields")
    digest = source.get("sha256")
    if source.get("input_kind") != "prometheus_text" or not isinstance(digest, str):
        raise ObservabilityEvalError(f"snapshot {path} source is invalid")
    if not re.fullmatch(r"[0-9a-f]{64}", digest):
        raise ObservabilityEvalError(f"snapshot {path} sha256 is invalid")
    metrics_value = value.get("metrics")
    if not isinstance(metrics_value, Mapping) or set(metrics_value) != METRIC_KEYS:
        raise ObservabilityEvalError(f"snapshot {path} metrics contract is invalid")
    metrics: dict[str, float] = {}
    for key, metric_value in metrics_value.items():
        if not isinstance(key, str) or not _is_finite_nonnegative(metric_value):
            raise ObservabilityEvalError(f"snapshot {path} metric {key} is invalid")
        metrics[key] = float(metric_value)
    _validate_metric_values(metrics, f"snapshot {path}")
    return {
        "schema_version": SNAPSHOT_SCHEMA_VERSION,
        "generated_at": generated_at,
        "source": dict(source),
        "metrics": metrics,
    }


def build_report(
    before: Mapping[str, Any],
    after: Mapping[str, Any],
    baseline: Mapping[str, Any],
) -> tuple[dict[str, Any], list[dict[str, Any]]]:
    before_metrics = _snapshot_metrics(before, "before")
    after_metrics = _snapshot_metrics(after, "after")
    version, absolute, relative, conditional = validate_baseline(baseline)
    violations: list[dict[str, Any]] = []

    for metric, maximum in absolute.items():
        actual = after_metrics[metric]
        if actual > maximum:
            violations.append(
                _violation(metric, f"<= {maximum}", actual, "absolute_slo_exceeded")
            )
    for metric, maximum_increase in relative.items():
        increase = after_metrics[metric] - before_metrics[metric]
        if increase > maximum_increase:
            violations.append(
                _violation(
                    metric,
                    f"increase <= {maximum_increase}",
                    _number(increase),
                    "release_regression",
                )
            )

    sample_metric = str(conditional["sample_metric"])
    ratio_metric = str(conditional["metric"])
    minimum_samples = float(conditional["minimum_samples"])
    minimum = float(conditional["minimum"])
    if after_metrics[sample_metric] >= minimum_samples and after_metrics[ratio_metric] < minimum:
        violations.append(
            _violation(
                ratio_metric,
                f">= {minimum} when {sample_metric} >= {minimum_samples}",
                after_metrics[ratio_metric],
                "conditional_slo_not_met",
            )
        )

    before_digest = _snapshot_digest(before, "before")
    after_digest = _snapshot_digest(after, "after")
    report = {
        "schema_version": REPORT_SCHEMA_VERSION,
        "source": {
            "before_sha256": before_digest,
            "after_sha256": after_digest,
        },
        "summary": {
            "before": before_metrics,
            "after": after_metrics,
            "delta": {
                key: _number(after_metrics[key] - before_metrics[key])
                for key in sorted(METRIC_KEYS)
            },
        },
        "regression": {
            "baseline_version": version,
            "violations": violations,
        },
    }
    return report, violations


def write_junit(path: Path, report: Mapping[str, Any]) -> None:
    regression = report.get("regression")
    regression = regression if isinstance(regression, Mapping) else {}
    violations_value = regression.get("violations")
    violations = violations_value if isinstance(violations_value, list) else []
    suite = ElementTree.Element(
        "testsuite",
        name="observability-release",
        tests="1",
        failures="1" if violations else "0",
    )
    case = ElementTree.SubElement(suite, "testcase", name="release-metrics-baseline")
    if violations:
        failure = ElementTree.SubElement(case, "failure", message="observability regression")
        failure.text = json.dumps(violations, ensure_ascii=False)
    output = ElementTree.SubElement(case, "system-out")
    output.text = json.dumps(report.get("summary", {}), ensure_ascii=False, sort_keys=True)
    path.parent.mkdir(parents=True, exist_ok=True)
    ElementTree.ElementTree(suite).write(path, encoding="utf-8", xml_declaration=True)
    path.chmod(0o600)


def main(argv: Sequence[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Capture and compare release metrics snapshots")
    commands = parser.add_subparsers(dest="command", required=True)

    capture = commands.add_parser("capture", help="Create an allowlisted metrics snapshot")
    source = capture.add_mutually_exclusive_group(required=True)
    source.add_argument("--input", type=Path)
    source.add_argument("--url")
    capture.add_argument("--output", type=Path, required=True)

    compare = commands.add_parser("compare", help="Compare before/after snapshots")
    compare.add_argument("--before", type=Path, required=True)
    compare.add_argument("--after", type=Path, required=True)
    compare.add_argument("--baseline", type=Path, required=True)
    compare.add_argument("--json-report", type=Path)
    compare.add_argument("--junit-report", type=Path)

    args = parser.parse_args(argv)
    try:
        if args.command == "capture":
            metrics_text = _load_metrics_input(args.input, args.url)
            snapshot = build_snapshot(metrics_text)
            _write_private_json(args.output, snapshot)
            print(
                json.dumps(
                    {
                        "schema_version": SNAPSHOT_SCHEMA_VERSION,
                        "output": str(args.output),
                        "sha256": snapshot["source"]["sha256"],
                    },
                    sort_keys=True,
                )
            )
            return 0

        before = load_snapshot(args.before)
        after = load_snapshot(args.after)
        baseline_value = load_baseline(args.baseline)
        report, violations = build_report(before, after, baseline_value)
        encoded = json.dumps(report, ensure_ascii=False, indent=2)
        if args.json_report:
            _write_private_text(args.json_report, encoded + "\n")
        if args.junit_report:
            write_junit(args.junit_report, report)
        print(encoded)
        return 1 if violations else 0
    except (OSError, json.JSONDecodeError, ObservabilityEvalError) as exc:
        print(f"observability eval error: {exc}", file=sys.stderr)
        return 2


def _parse_samples(metrics_text: str) -> dict[tuple[str, tuple[tuple[str, str], ...]], float]:
    if len(metrics_text.encode("utf-8")) > MAX_METRICS_BYTES:
        raise ObservabilityEvalError("metrics input exceeds size limit")
    samples: dict[tuple[str, tuple[tuple[str, str], ...]], float] = {}
    for line_number, raw_line in enumerate(metrics_text.splitlines(), 1):
        line = raw_line.strip()
        if not line or line.startswith("#"):
            continue
        match = SAMPLE_RE.fullmatch(line)
        if match is None:
            if any(line.startswith(name) for name in TARGET_NAMES):
                raise ObservabilityEvalError(f"metrics line {line_number} is malformed")
            continue
        name, labels_text, encoded_value = match.groups()
        if name not in TARGET_NAMES:
            continue
        labels = _parse_labels(labels_text or "", line_number)
        try:
            value = float(encoded_value)
        except ValueError as exc:
            raise ObservabilityEvalError(
                f"metric {name} on line {line_number} is not numeric"
            ) from exc
        if not math.isfinite(value) or value < 0:
            raise ObservabilityEvalError(
                f"metric {name} on line {line_number} must be finite and non-negative"
            )
        key = (name, tuple(sorted(labels.items())))
        if key in samples:
            raise ObservabilityEvalError(f"duplicate metric sample on line {line_number}: {name}")
        samples[key] = value
    return samples


def _parse_labels(labels_text: str, line_number: int) -> dict[str, str]:
    if not labels_text.strip():
        return {}
    labels: dict[str, str] = {}
    position = 0
    while position < len(labels_text):
        match = LABEL_RE.match(labels_text, position)
        if match is None or match.end() <= position:
            raise ObservabilityEvalError(f"metrics labels on line {line_number} are malformed")
        name, encoded_value = match.groups()
        if name in labels:
            raise ObservabilityEvalError(f"duplicate label {name} on line {line_number}")
        try:
            value = json.loads(encoded_value)
        except json.JSONDecodeError as exc:
            raise ObservabilityEvalError(
                f"metric label {name} on line {line_number} is invalid"
            ) from exc
        if not isinstance(value, str):
            raise ObservabilityEvalError(f"metric label {name} on line {line_number} is invalid")
        labels[name] = value
        position = match.end()
    return labels


def validate_baseline(
    baseline: Mapping[str, Any],
) -> tuple[str, dict[str, float], dict[str, float], dict[str, Any]]:
    expected_fields = {
        "schema_version",
        "version",
        "absolute_maximum",
        "relative_maximum_increase",
        "conditional_minimum",
    }
    if set(baseline) != expected_fields:
        raise ObservabilityEvalError("observability baseline fields are invalid")
    if baseline.get("schema_version") != BASELINE_SCHEMA_VERSION:
        raise ObservabilityEvalError("unsupported observability baseline schema_version")
    version = baseline.get("version")
    if not isinstance(version, str) or not version:
        raise ObservabilityEvalError("observability baseline version is required")
    absolute = _thresholds(baseline.get("absolute_maximum"), "absolute_maximum")
    relative = _thresholds(
        baseline.get("relative_maximum_increase"), "relative_maximum_increase"
    )
    if set(absolute) != ABSOLUTE_LIMIT_METRICS:
        raise ObservabilityEvalError("absolute_maximum metrics contract is invalid")
    if set(relative) != RELATIVE_LIMIT_METRICS:
        raise ObservabilityEvalError("relative_maximum_increase metrics contract is invalid")
    conditional_value = baseline.get("conditional_minimum")
    if not isinstance(conditional_value, Mapping):
        raise ObservabilityEvalError("conditional_minimum must be an object")
    conditional = dict(conditional_value)
    if set(conditional) != {"metric", "sample_metric", "minimum_samples", "minimum"}:
        raise ObservabilityEvalError("conditional_minimum fields are invalid")
    for field in ("metric", "sample_metric"):
        metric = conditional.get(field)
        if not isinstance(metric, str) or metric not in METRIC_KEYS:
            raise ObservabilityEvalError(f"conditional_minimum {field} is invalid")
    if conditional["metric"] != "retry_recovery_ratio":
        raise ObservabilityEvalError("conditional_minimum metric contract is invalid")
    if conditional["sample_metric"] != "retry_settled":
        raise ObservabilityEvalError("conditional_minimum sample_metric contract is invalid")
    for field in ("minimum_samples", "minimum"):
        if not _is_finite_nonnegative(conditional.get(field)):
            raise ObservabilityEvalError(f"conditional_minimum {field} is invalid")
    minimum_samples = conditional["minimum_samples"]
    if not isinstance(minimum_samples, int) or isinstance(minimum_samples, bool):
        raise ObservabilityEvalError("conditional_minimum minimum_samples must be an integer")
    if minimum_samples <= 0:
        raise ObservabilityEvalError("conditional_minimum minimum_samples must be positive")
    if float(conditional["minimum"]) > 1:
        raise ObservabilityEvalError("conditional_minimum minimum must be <= 1")
    return version, absolute, relative, conditional


def _thresholds(value: Any, field: str) -> dict[str, float]:
    if not isinstance(value, Mapping) or not value:
        raise ObservabilityEvalError(f"observability baseline {field} must be a non-empty object")
    result: dict[str, float] = {}
    for metric, threshold in value.items():
        if not isinstance(metric, str) or metric not in METRIC_KEYS:
            raise ObservabilityEvalError(f"observability baseline {field} metric is invalid")
        if not _is_finite_nonnegative(threshold):
            raise ObservabilityEvalError(f"observability baseline {field} threshold is invalid")
        result[metric] = float(threshold)
    return result


def _snapshot_metrics(snapshot: Mapping[str, Any], name: str) -> dict[str, float]:
    if set(snapshot) != {"schema_version", "generated_at", "source", "metrics"}:
        raise ObservabilityEvalError(f"{name} snapshot fields are invalid")
    if snapshot.get("schema_version") != SNAPSHOT_SCHEMA_VERSION:
        raise ObservabilityEvalError(f"{name} snapshot schema_version is invalid")
    generated_at = snapshot.get("generated_at")
    if not isinstance(generated_at, str):
        raise ObservabilityEvalError(f"{name} snapshot generated_at is invalid")
    _validate_timestamp(generated_at, f"{name} snapshot")
    metrics_value = snapshot.get("metrics")
    if not isinstance(metrics_value, Mapping) or set(metrics_value) != METRIC_KEYS:
        raise ObservabilityEvalError(f"{name} snapshot metrics contract is invalid")
    result: dict[str, float] = {}
    for metric, value in metrics_value.items():
        if not isinstance(metric, str) or not _is_finite_nonnegative(value):
            raise ObservabilityEvalError(f"{name} snapshot metric {metric} is invalid")
        result[metric] = float(value)
    _validate_metric_values(result, f"{name} snapshot")
    return result


def _snapshot_digest(snapshot: Mapping[str, Any], name: str) -> str:
    source = snapshot.get("source")
    if not isinstance(source, Mapping) or set(source) != {"input_kind", "sha256"}:
        raise ObservabilityEvalError(f"{name} snapshot source is invalid")
    if source.get("input_kind") != "prometheus_text":
        raise ObservabilityEvalError(f"{name} snapshot input_kind is invalid")
    digest = source.get("sha256") if isinstance(source, Mapping) else None
    if not isinstance(digest, str) or re.fullmatch(r"[0-9a-f]{64}", digest) is None:
        raise ObservabilityEvalError(f"{name} snapshot sha256 is invalid")
    return digest


def _load_metrics_input(path: Path | None, url: str | None) -> str:
    if path is not None:
        try:
            data = path.read_bytes()
        except OSError as exc:
            raise ObservabilityEvalError(f"cannot read metrics input: {exc}") from exc
    else:
        parsed = urllib.parse.urlparse(url or "")
        if parsed.scheme not in {"http", "https"} or not parsed.netloc:
            raise ObservabilityEvalError("metrics URL must use http or https")
        if parsed.username or parsed.password or parsed.query or parsed.fragment:
            raise ObservabilityEvalError(
                "metrics URL must not contain credentials, query parameters, or fragments"
            )
        request = urllib.request.Request(url or "", headers={"Accept": "text/plain"})
        try:
            with urllib.request.urlopen(request, timeout=10) as response:
                data = response.read(MAX_METRICS_BYTES + 1)
        except (urllib.error.URLError, TimeoutError) as exc:
            raise ObservabilityEvalError(f"cannot fetch metrics URL: {exc}") from exc
    if len(data) > MAX_METRICS_BYTES:
        raise ObservabilityEvalError("metrics input exceeds size limit")
    try:
        return data.decode("utf-8")
    except UnicodeDecodeError as exc:
        raise ObservabilityEvalError("metrics input must be UTF-8") from exc


def _write_private_json(path: Path, value: Mapping[str, Any]) -> None:
    _write_private_text(path, json.dumps(value, ensure_ascii=False, indent=2) + "\n")


def _write_private_text(path: Path, value: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(value, encoding="utf-8")
    path.chmod(0o600)


def _is_finite_nonnegative(value: Any) -> TypeGuard[int | float]:
    return (
        isinstance(value, (int, float))
        and not isinstance(value, bool)
        and math.isfinite(float(value))
        and value >= 0
    )


def _validate_metric_values(metrics: Mapping[str, float], source: str) -> None:
    for metric in INTEGER_METRICS:
        if not metrics[metric].is_integer():
            raise ObservabilityEvalError(f"{source} metric {metric} must be an integer")
    for ratio_key in ("model_error_ratio", "retry_recovery_ratio"):
        if metrics[ratio_key] > 1:
            raise ObservabilityEvalError(f"{source} metric {ratio_key} must be <= 1")
    if metrics["degradation_level"] > 3:
        raise ObservabilityEvalError(f"{source} degradation_level must be <= 3")
    expected_failures = metrics["agent_runs_failed"] + metrics["agent_runs_timed_out"]
    if metrics["agent_terminal_failures"] != expected_failures:
        raise ObservabilityEvalError(f"{source} agent_terminal_failures is inconsistent")
    expected_exhausted = metrics["retry_exhausted"] + metrics["retry_deadline_exhausted"]
    if metrics["retry_exhausted_total"] != expected_exhausted:
        raise ObservabilityEvalError(f"{source} retry_exhausted_total is inconsistent")
    expected_settled = metrics["retry_recovered"] + metrics["retry_exhausted_total"]
    if metrics["retry_settled"] != expected_settled:
        raise ObservabilityEvalError(f"{source} retry_settled is inconsistent")
    expected_ratio = metrics["retry_recovered"] / expected_settled if expected_settled else 0.0
    if not math.isclose(metrics["retry_recovery_ratio"], expected_ratio, abs_tol=0.000001):
        raise ObservabilityEvalError(f"{source} retry_recovery_ratio is inconsistent")


def _validate_timestamp(value: str, source: str) -> None:
    try:
        parsed = datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError as exc:
        raise ObservabilityEvalError(f"{source} generated_at is invalid") from exc
    if parsed.tzinfo is None or parsed.utcoffset() is None:
        raise ObservabilityEvalError(f"{source} generated_at must include a timezone")


def _number(value: int | float) -> float:
    return round(float(value), 6)


def _violation(metric: str, expected: str, actual: Any, code: str) -> dict[str, Any]:
    return {
        "code": code,
        "severity": "hard",
        "path": f"/summary/after/{metric}",
        "expected": expected,
        "actual": actual,
    }


if __name__ == "__main__":
    raise SystemExit(main())
