from __future__ import annotations

import json
import tempfile
import unittest
from pathlib import Path

from ai_companion_worker.evaluation.performance import (
    PerformanceEvalError,
    apply_baseline,
    build_report,
    load_observations,
    main,
    write_junit,
)


ROOT = Path(__file__).resolve().parents[3]
SAMPLE = ROOT / "evals" / "agent" / "performance" / "canary.sample.jsonl"
BASELINE = ROOT / "evals" / "agent" / "baselines" / "performance.v2.json"


class AgentPerformanceEvaluationTest(unittest.TestCase):
    def test_canary_sample_passes_versioned_baseline(self) -> None:
        report = build_report(load_observations(SAMPLE))
        baseline = json.loads(BASELINE.read_text(encoding="utf-8"))

        violations = apply_baseline(report, baseline)

        self.assertEqual(violations, [])
        self.assertEqual(report["summary"]["dispatch_samples"], 12)
        self.assertEqual(report["summary"]["processed_dispatch_samples"], 12)
        self.assertEqual(report["summary"]["paired_execution_samples"], 12)
        self.assertEqual(report["summary"]["python_samples"], 12)
        self.assertEqual(report["summary"]["direct_python_samples"], 6)
        self.assertEqual(report["summary"]["warm_direct_python_samples"], 4)
        self.assertEqual(report["summary"]["warmup_samples"], 1)
        self.assertEqual(report["summary"]["warmup_ready_processes"], 1)
        self.assertEqual(report["summary"]["tool_wake_samples"], 4)
        self.assertEqual(report["summary"]["awakened_tool_run_samples"], 4)
        self.assertEqual(report["summary"]["tool_wake_latency_p95_ms"], 245)
        self.assertEqual(report["summary"]["direct_runtime_overhead_p95_ms"], 120)
        self.assertEqual(
            report["summary"]["direct_max_model_attempt_timeout_p95_ms"],
            15000,
        )
        self.assertEqual(report["summary"]["cold_start_rate"], 0.166667)

    def test_regression_is_reported_and_returns_nonzero(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            log = root / "worker.jsonl"
            report_path = root / "report.json"
            junit_path = root / "report.xml"
            lines = SAMPLE.read_text(encoding="utf-8").splitlines()
            python_index = max(
                index
                for index, line in enumerate(lines)
                if json.loads(line).get("msg") == "Agent Python execution"
            )
            value = json.loads(lines[python_index])
            value["success"] = False
            value["recycled"] = True
            value["error_code"] = "runtime_contract"
            lines[python_index] = json.dumps(value)
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
            self.assertGreaterEqual(len(report["regression"]["violations"]), 2)
            self.assertIn("performance regression", junit_path.read_text(encoding="utf-8"))

    def test_not_claimed_duplicate_is_excluded_from_processed_latency(self) -> None:
        observations = load_observations(SAMPLE)
        observations["dispatch"].append(
            {
                "run_id": "duplicate-run",
                "queue_wait_ms": 0,
                "execution_duration_ms": 1,
                "processed": False,
                "outcome": "not_claimed",
            }
        )

        report = build_report(observations)

        self.assertEqual(report["summary"]["dispatch_samples"], 13)
        self.assertEqual(report["summary"]["processed_dispatch_samples"], 12)
        self.assertEqual(report["summary"]["paired_execution_samples"], 12)
        self.assertEqual(report["summary"]["dispatch_duration_p95_ms"], 15180)
        self.assertEqual(report["breakdown"]["dispatch_outcomes"]["not_claimed"], 1)

    def test_zero_match_terminal_event_does_not_satisfy_wake_sample_gate(self) -> None:
        observations = load_observations(SAMPLE)
        observations["tool_wake"].append(
            {
                "task_id": "unrelated-task",
                "event_type": "skill.run.succeeded.v1",
                "event_age_ms": 10,
                "wake_duration_ms": 1,
                "wake_latency_ms": 11,
                "awakened_runs": 0,
            }
        )

        report = build_report(observations)

        self.assertEqual(report["summary"]["tool_wake_samples"], 4)
        self.assertEqual(report["summary"]["awakened_tool_run_samples"], 4)

    def test_missing_tool_wakes_fail_v2_minimums(self) -> None:
        observations = load_observations(SAMPLE)
        observations["tool_wake"] = []
        report = build_report(observations)

        violations = apply_baseline(
            report, json.loads(BASELINE.read_text(encoding="utf-8"))
        )

        paths = {item["path"] for item in violations}
        self.assertIn("/summary/tool_wake_samples", paths)
        self.assertIn("/summary/awakened_tool_run_samples", paths)

    def test_malformed_tool_wake_log_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "worker.jsonl"
            path.write_text(
                json.dumps(
                    {
                        "msg": "Agent tool task wake",
                        "task_id": "task-1",
                        "event_type": "skill.run.unknown.v1",
                        "event_age_ms": 1,
                        "wake_duration_ms": 1,
                        "wake_latency_ms": 2,
                        "awakened_runs": 1,
                    }
                )
                + "\n",
                encoding="utf-8",
            )
            with self.assertRaisesRegex(PerformanceEvalError, "invalid tool wake"):
                load_observations(path)

    def test_malformed_log_line_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "worker.jsonl"
            path.write_text("not-json\n", encoding="utf-8")
            with self.assertRaisesRegex(PerformanceEvalError, "invalid JSON"):
                load_observations(path)

    def test_junit_contains_summary_for_passing_report(self) -> None:
        report = build_report(load_observations(SAMPLE))
        apply_baseline(report, json.loads(BASELINE.read_text(encoding="utf-8")))
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "performance.xml"
            write_junit(path, report)
            content = path.read_text(encoding="utf-8")
        self.assertIn('failures="0"', content)
        self.assertIn("direct_python_duration_p95_ms", content)


if __name__ == "__main__":
    unittest.main()
