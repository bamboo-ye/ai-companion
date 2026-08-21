from __future__ import annotations

import json
import tempfile
import unittest
from pathlib import Path

from ai_companion_worker.evaluation.retry import (
    RetryEvalError,
    apply_baseline,
    build_report,
    load_observations,
    main,
    write_junit,
)


ROOT = Path(__file__).resolve().parents[3]
SAMPLE = ROOT / "evals" / "agent" / "retry" / "canary.sample.jsonl"
BASELINE = ROOT / "evals" / "agent" / "baselines" / "retry.v1.json"


class AgentRetryEvaluationTest(unittest.TestCase):
    def test_canary_sample_passes_versioned_baseline(self) -> None:
        report = build_report(load_observations(SAMPLE))
        baseline = json.loads(BASELINE.read_text(encoding="utf-8"))

        violations = apply_baseline(report, baseline)

        self.assertEqual(violations, [])
        self.assertEqual(report["summary"]["retry_events"], 4)
        self.assertEqual(report["summary"]["retry_runs"], 3)
        self.assertEqual(report["summary"]["recovered_runs"], 3)
        self.assertEqual(report["summary"]["retry_recovery_rate"], 1)
        self.assertEqual(report["summary"]["max_scheduled_delay_ms"], 60000)
        self.assertEqual(report["breakdown"]["policies"]["retry_after"], 2)

    def test_duplicate_schedule_and_completion_fail_the_gate(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            log = root / "retry.jsonl"
            lines = SAMPLE.read_text(encoding="utf-8").splitlines()
            lines.insert(3, lines[2])
            completed = json.loads(lines[4])
            lines.append(json.dumps(completed))
            log.write_text("\n".join(lines) + "\n", encoding="utf-8")

            report = build_report(load_observations(log))
            violations = apply_baseline(
                report,
                json.loads(BASELINE.read_text(encoding="utf-8")),
            )

        self.assertGreater(report["summary"]["duplicate_schedule_count"], 0)
        self.assertGreater(report["summary"]["duplicate_completion_count"], 0)
        self.assertTrue(violations)

    def test_delay_above_policy_limit_returns_nonzero(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            log = root / "retry.jsonl"
            report_path = root / "retry-report.json"
            junit_path = root / "retry-report.xml"
            lines = SAMPLE.read_text(encoding="utf-8").splitlines()
            event = json.loads(lines[2])
            event["delay_ms"] = 200000
            lines[2] = json.dumps(event)
            log.write_text("\n".join(lines) + "\n", encoding="utf-8")

            result = main(
                [
                    "--input",
                    str(log),
                    "--baseline",
                    str(BASELINE),
                    "--json-report",
                    str(report_path),
                    "--junit-report",
                    str(junit_path),
                ]
            )

            report = json.loads(report_path.read_text(encoding="utf-8"))
            self.assertEqual(result, 1)
            self.assertEqual(report["summary"]["delay_policy_violation_count"], 1)
            self.assertIn("retry regression", junit_path.read_text(encoding="utf-8"))

    def test_malformed_log_line_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "retry.jsonl"
            path.write_text("not-json\n", encoding="utf-8")
            with self.assertRaisesRegex(RetryEvalError, "invalid JSON"):
                load_observations(path)

    def test_junit_contains_summary_for_passing_report(self) -> None:
        report = build_report(load_observations(SAMPLE))
        apply_baseline(report, json.loads(BASELINE.read_text(encoding="utf-8")))
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "retry.xml"
            write_junit(path, report)
            content = path.read_text(encoding="utf-8")
        self.assertIn('failures="0"', content)
        self.assertIn("retry_recovery_rate", content)


if __name__ == "__main__":
    unittest.main()
