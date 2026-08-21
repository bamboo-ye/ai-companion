from __future__ import annotations

import json
import tempfile
import unittest
from pathlib import Path
from typing import Any

from ai_companion_worker.evaluation.observability import (
    METRIC_KEYS,
    ObservabilityEvalError,
    build_report,
    build_snapshot,
    load_snapshot,
    main,
    write_junit,
)


ROOT = Path(__file__).resolve().parents[3]
BASELINE = ROOT / "evals" / "observability" / "baselines" / "release.v1.json"
BEFORE = ROOT / "evals" / "observability" / "snapshots" / "before.sample.json"
AFTER = ROOT / "evals" / "observability" / "snapshots" / "after.sample.json"


def metrics_text(**overrides: float) -> str:
    values = {
        "degradation": 0,
        "queue": 4,
        "oldest": 2,
        "model_error": 0.02,
        "model_latency": 1.2,
        "agent_duration": 10,
        "completed": 10,
        "failed": 0,
        "timed_out": 0,
        "scheduled": 2,
        "recovered": 2,
        "exhausted": 0,
        "deadline": 0,
        "recovery_ratio": 1,
    }
    values.update(overrides)
    return "\n".join(
        [
            f"ai_companion_degradation_level {values['degradation']}",
            f"ai_companion_queue_lag {values['queue']}",
            f"ai_companion_oldest_job_age_seconds {values['oldest']}",
            f"ai_companion_model_error_ratio {values['model_error']}",
            f"ai_companion_model_latency_p95_seconds {values['model_latency']}",
            f"ai_companion_agent_run_duration_p95_seconds {values['agent_duration']}",
            f'ai_companion_agent_runs{{status="completed"}} {values["completed"]}',
            f'ai_companion_agent_runs{{status="failed"}} {values["failed"]}',
            f'ai_companion_agent_runs{{status="timed_out"}} {values["timed_out"]}',
            f'ai_companion_agent_execution_retries_recent{{outcome="scheduled"}} {values["scheduled"]}',
            f'ai_companion_agent_execution_retries_recent{{outcome="recovered"}} {values["recovered"]}',
            f'ai_companion_agent_execution_retries_recent{{outcome="exhausted"}} {values["exhausted"]}',
            f'ai_companion_agent_execution_retries_recent{{outcome="deadline_exhausted"}} {values["deadline"]}',
            f"ai_companion_agent_execution_retry_recovery_ratio {values['recovery_ratio']}",
            "",
        ]
    )


def sample_snapshot(**overrides: float) -> dict[str, Any]:
    return build_snapshot(metrics_text(**overrides), "2026-08-10T00:00:00Z")


class ObservabilityEvaluationTest(unittest.TestCase):
    def setUp(self) -> None:
        self.baseline = json.loads(BASELINE.read_text(encoding="utf-8"))

    def test_snapshot_is_allowlisted_and_derives_terminal_totals(self) -> None:
        snapshot = sample_snapshot(
            failed=1,
            timed_out=2,
            recovered=3,
            exhausted=1,
            deadline=1,
            recovery_ratio=0.6,
        )

        self.assertEqual(set(snapshot["metrics"]), METRIC_KEYS)
        self.assertEqual(snapshot["metrics"]["agent_terminal_failures"], 3)
        self.assertEqual(snapshot["metrics"]["retry_exhausted_total"], 2)
        self.assertEqual(snapshot["metrics"]["retry_settled"], 5)
        self.assertEqual(set(snapshot["source"]), {"input_kind", "sha256"})

    def test_sample_release_comparison_passes(self) -> None:
        report, violations = build_report(
            load_snapshot(BEFORE), load_snapshot(AFTER), self.baseline
        )

        self.assertEqual(violations, [])
        self.assertEqual(report["regression"]["baseline_version"], "release.v1")

    def test_absolute_and_relative_regressions_are_both_reported(self) -> None:
        report, violations = build_report(
            sample_snapshot(queue=4), sample_snapshot(queue=600), self.baseline
        )

        self.assertEqual(len(violations), 2)
        self.assertEqual(
            {violation["code"] for violation in violations},
            {"absolute_slo_exceeded", "release_regression"},
        )
        self.assertEqual(report["summary"]["delta"]["queue_lag"], 596)

    def test_recovery_ratio_uses_minimum_sample_guard(self) -> None:
        _, no_sample_violations = build_report(
            sample_snapshot(),
            sample_snapshot(recovered=0, recovery_ratio=0),
            self.baseline,
        )
        _, low_recovery_violations = build_report(
            sample_snapshot(),
            sample_snapshot(recovered=3, exhausted=1, deadline=1, recovery_ratio=0.6),
            self.baseline,
        )

        self.assertFalse(
            any(v["code"] == "conditional_slo_not_met" for v in no_sample_violations)
        )
        self.assertTrue(
            any(v["code"] == "conditional_slo_not_met" for v in low_recovery_violations)
        )

    def test_missing_duplicate_and_nonfinite_metrics_are_rejected(self) -> None:
        missing = metrics_text().replace("ai_companion_queue_lag 4\n", "")
        with self.assertRaisesRegex(ObservabilityEvalError, "queue_lag"):
            build_snapshot(missing)
        with self.assertRaisesRegex(ObservabilityEvalError, "duplicate metric"):
            build_snapshot(metrics_text() + "ai_companion_queue_lag 4\n")
        with self.assertRaisesRegex(ObservabilityEvalError, "finite"):
            build_snapshot(metrics_text(model_error=float("nan")))

    def test_tampered_derived_metrics_and_recovery_ratio_are_rejected(self) -> None:
        snapshot = sample_snapshot()
        snapshot["metrics"]["retry_settled"] = 99
        with self.assertRaisesRegex(ObservabilityEvalError, "retry_settled is inconsistent"):
            build_report(snapshot, sample_snapshot(), self.baseline)

        with self.assertRaisesRegex(ObservabilityEvalError, "recovery_ratio is inconsistent"):
            build_snapshot(metrics_text(recovered=3, exhausted=1, deadline=1, recovery_ratio=1))

    def test_baseline_cannot_remove_or_replace_required_controls(self) -> None:
        missing_limit = json.loads(json.dumps(self.baseline))
        del missing_limit["absolute_maximum"]["degradation_level"]
        with self.assertRaisesRegex(ObservabilityEvalError, "metrics contract"):
            build_report(sample_snapshot(), sample_snapshot(), missing_limit)

        replaced_condition = json.loads(json.dumps(self.baseline))
        replaced_condition["conditional_minimum"]["metric"] = "model_error_ratio"
        with self.assertRaisesRegex(ObservabilityEvalError, "metric contract"):
            build_report(sample_snapshot(), sample_snapshot(), replaced_condition)

    def test_observability_json_contracts_have_no_duplicate_keys(self) -> None:
        paths = [
            ROOT / "evals" / "observability" / "baselines" / "release.v1.json",
            *(ROOT / "evals" / "observability" / "schemas").glob("*.json"),
            *(ROOT / "evals" / "observability" / "canaries").glob("*.json"),
            ROOT / "evals" / "agent" / "baselines" / "observability-canary.v1.json",
            ROOT / "evals" / "agent" / "baselines" / "direct-canary.v1.json",
            ROOT / "evals" / "agent" / "suites" / "direct-canary.v1.json",
            *(ROOT / "evals" / "agent" / "schemas").glob("*.json"),
            BEFORE,
            AFTER,
        ]

        def reject_duplicates(pairs: list[tuple[str, Any]]) -> dict[str, Any]:
            result: dict[str, Any] = {}
            for key, value in pairs:
                if key in result:
                    raise ValueError(f"duplicate JSON key: {key}")
                result[key] = value
            return result

        for path in paths:
            with self.subTest(path=path):
                json.loads(path.read_text(encoding="utf-8"), object_pairs_hook=reject_duplicates)

    def test_capture_writes_private_sanitized_snapshot(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source = root / "metrics.prom"
            output = root / "snapshot.json"
            source.write_text(
                metrics_text() + 'ai_companion_http_requests_total{route="/secret"} 1\n',
                encoding="utf-8",
            )

            result = main(["capture", "--input", str(source), "--output", str(output)])
            snapshot = json.loads(output.read_text(encoding="utf-8"))

            self.assertEqual(result, 0)
            self.assertNotIn("secret", output.read_text(encoding="utf-8"))
            self.assertEqual(output.stat().st_mode & 0o777, 0o600)
            self.assertEqual(set(snapshot["metrics"]), METRIC_KEYS)

    def test_compare_returns_nonzero_and_writes_junit_on_regression(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            before = root / "before.json"
            after = root / "after.json"
            report = root / "report.json"
            junit = root / "report.xml"
            before.write_text(json.dumps(sample_snapshot()), encoding="utf-8")
            after.write_text(json.dumps(sample_snapshot(degradation=1)), encoding="utf-8")

            result = main(
                [
                    "compare",
                    "--before",
                    str(before),
                    "--after",
                    str(after),
                    "--baseline",
                    str(BASELINE),
                    "--json-report",
                    str(report),
                    "--junit-report",
                    str(junit),
                ]
            )

            self.assertEqual(result, 1)
            self.assertIn("absolute_slo_exceeded", report.read_text(encoding="utf-8"))
            self.assertIn('failures="1"', junit.read_text(encoding="utf-8"))

    def test_passing_junit_has_no_failure(self) -> None:
        report, _ = build_report(load_snapshot(BEFORE), load_snapshot(AFTER), self.baseline)
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "report.xml"
            write_junit(path, report)
            content = path.read_text(encoding="utf-8")
            mode = path.stat().st_mode & 0o777
        self.assertIn('failures="0"', content)
        self.assertEqual(mode, 0o600)


if __name__ == "__main__":
    unittest.main()
