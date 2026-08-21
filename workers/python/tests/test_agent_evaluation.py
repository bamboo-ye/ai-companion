from __future__ import annotations

import tempfile
import unittest
from pathlib import Path

from ai_companion_worker.evaluation.runner import (
    CASE_SCHEMA_VERSION,
    EvalCaseError,
    build_report,
    load_cases,
    run_case,
    validate_case,
    write_junit,
)


ROOT = Path(__file__).resolve().parents[3]
PR_SUITE = ROOT / "evals" / "agent" / "suites" / "pr.jsonl"


class AgentEvaluationTest(unittest.TestCase):
    def test_pr_contract_suite_passes(self) -> None:
        results = [run_case(case) for case in load_cases(PR_SUITE)]
        report = build_report("contract", results)

        self.assertEqual(report["summary"]["cases"], 7)
        self.assertEqual(report["summary"]["passed"], 7)
        self.assertEqual(report["summary"]["hard_pass_rate"], 1.0)

    def test_invalid_case_is_rejected_before_execution(self) -> None:
        with self.assertRaisesRegex(EvalCaseError, "schema_version"):
            validate_case(
                {
                    "schema_version": "old",
                    "id": "invalid",
                    "given": {},
                    "expect": {},
                    "tape": {},
                }
            )

    def test_junit_report_contains_failure_details(self) -> None:
        report = build_report(
            "replay",
            [
                {
                    "id": "failed-case",
                    "passed": False,
                    "actual": {"model_calls": 2, "steps": 3},
                    "violations": [
                        {
                            "code": "forbidden_node",
                            "severity": "hard",
                            "path": "/actual_path",
                        }
                    ],
                }
            ],
        )
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "report.xml"
            write_junit(path, report)
            content = path.read_text(encoding="utf-8")

        self.assertIn("failed-case", content)
        self.assertIn("forbidden_node", content)

    def test_schema_version_constant_is_stable(self) -> None:
        self.assertEqual(CASE_SCHEMA_VERSION, "agent-eval-case-v1")


if __name__ == "__main__":
    unittest.main()
