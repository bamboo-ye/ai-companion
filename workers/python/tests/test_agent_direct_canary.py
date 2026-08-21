from __future__ import annotations

import json
import tempfile
import unittest
from pathlib import Path
from typing import Any

from ai_companion_worker.evaluation.direct_canary import (
    DirectCanaryError,
    build_report,
    evaluate_case,
    load_baseline,
    load_suite,
    validate_report,
)


ROOT = Path(__file__).resolve().parents[3]
BASELINE = ROOT / "evals" / "agent" / "baselines" / "direct-canary.v1.json"
SUITE = ROOT / "evals" / "agent" / "suites" / "direct-canary.v1.json"


def run_payload(
    response: str,
    *,
    model_calls: int = 1,
    execution_mode: str = "direct",
    outcome: str = "completed",
    response_validation: dict[str, Any] | None = None,
    nodes: list[str] | None = None,
) -> dict[str, Any]:
    validation = response_validation or {
        "passed": True,
        "policy_version": "response-quality-1.0.0",
        "violations": [],
        "repairs": [],
        "changed": False,
        "rewrite_attempt": 0,
    }
    return {
        "status": "completed",
        "output": {
            "outcome": outcome,
            "execution_mode": execution_mode,
            "response": response,
            "response_validation": validation,
            "budget": {"usage": {"model_calls": model_calls}},
            "model": {"calls": [{"status": "success"} for _ in range(model_calls)]},
            "observability": {
                "node_trace": [
                    {"node": node, "status": "succeeded"}
                    for node in (nodes or ["supervisor", "companion", "response_quality_gate"])
                ]
            },
        },
    }


class AgentDirectCanaryTest(unittest.TestCase):
    def setUp(self) -> None:
        self.baseline, self.baseline_digest = load_baseline(BASELINE)
        self.suite, self.suite_digest = load_suite(SUITE)

    def test_passing_results_require_direct_single_call_quality_and_content(self) -> None:
        responses = [
            "缓存能减少重复计算并提高访问速度。",
            "接口幂等性保证同一请求执行多次仍得到一致结果。",
            "数据有序才能根据中间值判断并排除一半查找范围。",
            "事务回滚用于撤销未完成操作并恢复数据库一致状态。",
            "网络重试采用退避可以降低请求频率并避免加重拥塞。",
        ]
        results = [
            evaluate_case(case, run_payload(response), 1000 + index * 100)
            for index, (case, response) in enumerate(
                zip(self.suite["cases"], responses, strict=True)
            )
        ]

        report, violations = build_report(
            results,
            baseline=self.baseline,
            baseline_sha256=self.baseline_digest,
            suite=self.suite,
            suite_sha256=self.suite_digest,
        )

        self.assertEqual(violations, [])
        self.assertEqual(report["decision"], "pass")
        self.assertEqual(report["summary"]["single_model_call_rate"], 1.0)
        self.assertEqual(report["summary"]["duplicate_violations"], 0)
        self.assertTrue(report["summary"]["repair_probe_passed"])
        self.assertEqual(validate_report(report), report)

    def test_duplicate_output_is_detected_and_deterministic_repair_is_counted(self) -> None:
        case = self.suite["cases"][0]
        duplicate = evaluate_case(
            case,
            run_payload("缓存减少重复计算。缓存减少重复计算。还能提高访问速度。"),
            1000,
        )
        repaired = evaluate_case(
            case,
            run_payload(
                "缓存减少重复计算并提高访问速度。",
                response_validation={
                    "passed": True,
                    "policy_version": "response-quality-1.0.0",
                    "violations": [],
                    "repairs": [
                        {
                            "code": "exact_duplicate_sentence_removed",
                            "location": "/paragraphs/0/lines/0/sentences/1",
                        }
                    ],
                    "changed": True,
                    "rewrite_attempt": 0,
                },
            ),
            1000,
        )

        self.assertEqual(duplicate["duplicate_violations"], 1)
        self.assertFalse(duplicate["quality_passed"] is False)
        self.assertTrue(repaired["repair_attempted"])
        self.assertTrue(repaired["repair_succeeded"])
        self.assertEqual(repaired["duplicate_violations"], 0)

    def test_plan_second_model_call_and_content_failure_block_report(self) -> None:
        responses = [
            "缓存能减少重复计算并提高访问速度。",
            "接口幂等性保证同一请求执行多次仍得到一致结果。",
            "数据有序才能根据中间值判断并排除一半查找范围。",
            "事务回滚用于撤销未完成操作并恢复数据库一致状态。",
            "网络重试采用退避可以降低请求频率并避免加重拥塞。",
        ]
        results = [
            evaluate_case(case, run_payload(response), 1000)
            for case, response in zip(self.suite["cases"], responses, strict=True)
        ]
        results[0] = evaluate_case(
            self.suite["cases"][0],
            run_payload(
                "这个问题需要详细分析。",
                model_calls=2,
                nodes=["supervisor", "companion", "plan", "response_quality_gate"],
            ),
            31_000,
        )

        report, violations = build_report(
            results,
            baseline=self.baseline,
            baseline_sha256=self.baseline_digest,
            suite=self.suite,
            suite_sha256=self.suite_digest,
        )
        fields = {item["path"] for item in violations}

        self.assertEqual(report["decision"], "fail")
        self.assertIn("/summary/single_model_call_rate", fields)
        self.assertIn("/summary/content_pass_rate", fields)
        self.assertIn("/summary/planned_runs", fields)
        self.assertIn("/summary/duration_max_ms", fields)

    def test_duplicate_assistant_delivery_blocks_report(self) -> None:
        responses = [
            "缓存能减少重复计算并提高访问速度。",
            "接口幂等性保证同一请求执行多次仍得到一致结果。",
            "数据有序才能根据中间值判断并排除一半查找范围。",
            "事务回滚用于撤销未完成操作并恢复数据库一致状态。",
            "网络重试采用退避可以降低请求频率并避免加重拥塞。",
        ]
        results = [
            evaluate_case(case, run_payload(response), 1000)
            for case, response in zip(self.suite["cases"], responses, strict=True)
        ]

        results[0]["assistant_messages"] = 2
        results[1]["assistant_messages"] = 0
        report, violations = build_report(
            results,
            baseline=self.baseline,
            baseline_sha256=self.baseline_digest,
            suite=self.suite,
            suite_sha256=self.suite_digest,
        )

        self.assertEqual(report["decision"], "fail")
        self.assertIn(
            "/summary/assistant_delivery_violations",
            {item["path"] for item in violations},
        )

    def test_report_rejects_source_summary_and_case_tampering(self) -> None:
        responses = [
            "缓存能减少重复计算并提高访问速度。",
            "接口幂等性保证同一请求执行多次仍得到一致结果。",
            "数据有序才能根据中间值判断并排除一半查找范围。",
            "事务回滚用于撤销未完成操作并恢复数据库一致状态。",
            "网络重试采用退避可以降低请求频率并避免加重拥塞。",
        ]
        results = [
            evaluate_case(case, run_payload(response), 1000)
            for case, response in zip(self.suite["cases"], responses, strict=True)
        ]
        report, _ = build_report(
            results,
            baseline=self.baseline,
            baseline_sha256=self.baseline_digest,
            suite=self.suite,
            suite_sha256=self.suite_digest,
        )
        tampered = json.loads(json.dumps(report))
        tampered["summary"]["direct_rate"] = 0.5
        with self.assertRaisesRegex(DirectCanaryError, "summary is inconsistent"):
            validate_report(tampered)
        tampered = json.loads(json.dumps(report))
        tampered["source"]["suite_sha256"] = "0" * 64
        with self.assertRaisesRegex(DirectCanaryError, "source is invalid"):
            validate_report(tampered)
        tampered = json.loads(json.dumps(report))
        tampered["cases"][0]["model_calls"] = -1
        with self.assertRaisesRegex(DirectCanaryError, "integer is invalid"):
            validate_report(tampered)

    def test_duplicate_keys_and_relaxed_required_controls_are_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            duplicate = root / "duplicate.json"
            duplicate.write_text(
                '{"schema_version":"agent-direct-canary-baseline-v1",'
                '"schema_version":"agent-direct-canary-baseline-v1",'
                '"version":"agent-direct-canary-baseline-v1",'
                '"minimum":{"cases":5},"maximum":{"model_calls":5},'
                '"required":{"repair_probe_passed":true}}',
                encoding="utf-8",
            )
            with self.assertRaisesRegex(DirectCanaryError, "JSON is invalid"):
                load_baseline(duplicate)

            relaxed = root / "relaxed.json"
            value = json.loads(BASELINE.read_text(encoding="utf-8"))
            value["required"]["repair_probe_passed"] = False
            relaxed.write_text(json.dumps(value), encoding="utf-8")
            with self.assertRaisesRegex(DirectCanaryError, "required controls"):
                load_baseline(relaxed)


if __name__ == "__main__":
    unittest.main()
