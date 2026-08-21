from __future__ import annotations

import json
import sqlite3
import sys
import tempfile
import unittest
from pathlib import Path
from typing import Any

from ai_companion_worker.evaluation.observability import build_snapshot
from ai_companion_worker.evaluation.release_gate import run_gate
from ai_companion_worker.evaluation.release_gate_history import (
    ReleaseGateHistory,
    ReleaseGateHistoryError,
)


ROOT = Path(__file__).resolve().parents[3]
BASELINE = ROOT / "evals" / "observability" / "baselines" / "release.v1.json"


def metrics_text(queue_lag: int = 0) -> str:
    return "\n".join(
        [
            "ai_companion_degradation_level 0",
            f"ai_companion_queue_lag {queue_lag}",
            "ai_companion_oldest_job_age_seconds 0",
            "ai_companion_model_error_ratio 0",
            "ai_companion_model_latency_p95_seconds 0",
            "ai_companion_agent_run_duration_p95_seconds 0",
            'ai_companion_agent_runs{status="completed"} 0',
            'ai_companion_agent_runs{status="failed"} 0',
            'ai_companion_agent_runs{status="timed_out"} 0',
            'ai_companion_agent_execution_retries_recent{outcome="scheduled"} 0',
            'ai_companion_agent_execution_retries_recent{outcome="recovered"} 0',
            'ai_companion_agent_execution_retries_recent{outcome="exhausted"} 0',
            'ai_companion_agent_execution_retries_recent{outcome="deadline_exhausted"} 0',
            "ai_companion_agent_execution_retry_recovery_ratio 0",
            "",
        ]
    )


class SnapshotSequence:
    def __init__(self, queues: list[int]) -> None:
        self.queues = queues
        self.requests = 0

    def __call__(self, _url: str) -> dict[str, Any]:
        index = min(self.requests, len(self.queues) - 1)
        self.requests += 1
        return build_snapshot(metrics_text(self.queues[index]))


def make_gate(root: Path, name: str, after_queue: int) -> tuple[Path, dict[str, Any]]:
    spec = root / f"{name}.spec.json"
    spec.write_text(
        json.dumps(
            {
                "schema_version": "observability-canary-command-v1",
                "name": "history-test",
                "command": [sys.executable, "-c", "pass"],
                "timeout_seconds": 2,
                "stabilization_seconds": 0,
            }
        ),
        encoding="utf-8",
    )
    gate_dir = root / f"release-gate-{name}"
    report, _result = run_gate(
        "http://metrics.invalid/metrics",
        BASELINE,
        spec,
        gate_dir,
        capture=SnapshotSequence([0, after_queue]),
    )
    return gate_dir, report


def bind_direct_summary(
    gate_dir: Path,
    report: dict[str, Any],
    *,
    direct_rate: float,
    single_call_rate: float,
    quality_rate: float,
    content_rate: float,
    duration_p95_ms: float,
    repair_attempts: int,
    repair_successes: int,
    duplicate_violations: int,
    response_quality_failures: int,
    model_calls: int,
) -> None:
    report["canary"]["name"] = "agent-direct"
    report["canary"]["report"] = {
        "schema_version": "agent-direct-canary-report-v1",
        "summary": {
            "cases": 5,
            "direct_rate": direct_rate,
            "single_model_call_rate": single_call_rate,
            "quality_pass_rate": quality_rate,
            "content_pass_rate": content_rate,
            "duration_p95_ms": duration_p95_ms,
            "repair_attempted_runs": repair_attempts,
            "repair_succeeded_runs": repair_successes,
            "duplicate_violations": duplicate_violations,
            "response_quality_failures": response_quality_failures,
            "model_calls": model_calls,
        },
    }
    path = gate_dir / "gate-report.json"
    path.write_text(json.dumps(report), encoding="utf-8")
    path.chmod(0o600)


class ObservabilityReleaseGateHistoryTest(unittest.TestCase):
    def test_history_is_idempotent_hash_chained_and_reports_trends(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            ledger = root / "state" / "history.sqlite3"
            history = ReleaseGateHistory(ledger, "staging-cn")
            first_dir, first = make_gate(root, "first", 0)
            second_dir, second = make_gate(root, "second", 600)

            self.assertTrue(history.record(first_dir, first))
            self.assertFalse(history.record(first_dir, first))
            self.assertTrue(history.record(second_dir, second))
            events = history.events()
            trend = history.trend(limit=20)

            self.assertEqual(len(events), 2)
            self.assertEqual(events[1]["previous_chain_sha256"], events[0]["chain_sha256"])
            self.assertEqual(trend["summary"]["promote"], 1)
            self.assertEqual(trend["summary"]["rollback"], 1)
            self.assertEqual(trend["window"]["history_chain_sha256"], events[-1]["chain_sha256"])
            self.assertEqual(ledger.stat().st_mode & 0o777, 0o600)

    def test_direct_quality_trend_aggregates_minimum_rates_and_repairs(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            history = ReleaseGateHistory(root / "state" / "history.sqlite3", "staging-cn")
            first_dir, first = make_gate(root, "direct-first", 0)
            second_dir, second = make_gate(root, "direct-second", 600)
            bind_direct_summary(
                first_dir,
                first,
                direct_rate=1.0,
                single_call_rate=1.0,
                quality_rate=1.0,
                content_rate=1.0,
                duration_p95_ms=900,
                repair_attempts=1,
                repair_successes=1,
                duplicate_violations=0,
                response_quality_failures=0,
                model_calls=5,
            )
            bind_direct_summary(
                second_dir,
                second,
                direct_rate=0.8,
                single_call_rate=0.8,
                quality_rate=0.8,
                content_rate=0.6,
                duration_p95_ms=1200,
                repair_attempts=2,
                repair_successes=1,
                duplicate_violations=1,
                response_quality_failures=1,
                model_calls=6,
            )

            history.record(first_dir, first)
            history.record(second_dir, second)
            summary = history.trend(limit=20)["summary"]

            self.assertEqual(summary["direct_canary_runs"], 2)
            self.assertEqual(summary["direct_cases"], 10)
            self.assertEqual(summary["direct_rate_min"], 0.8)
            self.assertEqual(summary["single_model_call_rate_min"], 0.8)
            self.assertEqual(summary["quality_pass_rate_min"], 0.8)
            self.assertEqual(summary["content_pass_rate_min"], 0.6)
            self.assertEqual(summary["direct_duration_p95_ms"], 1200)
            self.assertEqual(summary["direct_repair_attempted_runs"], 3)
            self.assertEqual(summary["direct_repair_succeeded_runs"], 2)
            self.assertEqual(summary["direct_repair_success_rate"], 0.666667)
            self.assertEqual(summary["direct_duplicate_violations"], 1)
            self.assertEqual(summary["direct_response_quality_failures"], 1)
            self.assertEqual(summary["model_calls"], 11)

    def test_history_detects_report_tampering(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            ledger = root / "state" / "history.sqlite3"
            history = ReleaseGateHistory(ledger, "staging-cn")
            gate_dir, report = make_gate(root, "tamper", 0)
            history.record(gate_dir, report)

            connection = sqlite3.connect(ledger)
            try:
                with self.assertRaisesRegex(sqlite3.IntegrityError, "append-only"):
                    connection.execute(
                        "UPDATE release_gate_history_events SET report_json = '{}' WHERE sequence = 1"
                    )
                connection.rollback()
                connection.execute("DROP TRIGGER release_gate_history_events_no_update")
                connection.execute(
                    "UPDATE release_gate_history_events SET report_json = '{}' WHERE sequence = 1"
                )
                connection.commit()
            finally:
                connection.close()

            with self.assertRaisesRegex(ReleaseGateHistoryError, "report digest"):
                history.events()

    def test_history_is_bound_to_one_environment(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            ledger = Path(directory) / "state" / "history.sqlite3"
            ReleaseGateHistory(ledger, "staging-cn")
            with self.assertRaisesRegex(ReleaseGateHistoryError, "environment binding"):
                ReleaseGateHistory(ledger, "production-cn")


if __name__ == "__main__":
    unittest.main()
