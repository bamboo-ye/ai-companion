"""Evaluate paired, human-labelled full-run measurements before enabling a canary.

Input is JSONL with the same case IDs, frozen-source hash and budgets in all four
arms. This reader never runs a model or changes deployment/delivery decisions.
"""

from __future__ import annotations
import argparse
import hashlib
import json
import math
import re
from pathlib import Path
from typing import Any

ARMS = ("baseline_single", "single_supplement", "repeat_same_evidence", "debate")
FLAGS = ("wrong_accept", "false_reject", "resolved", "false_consensus", "valid_supplement")
NUMBERS = ("latency_ms", "cost_micros", "model_calls", "read_calls")


def wilson(successes: int, total: int) -> tuple[float, float]:
    if not total:
        return 0.0, 1.0
    z = 1.96
    proportion = successes / total
    center = (proportion + z * z / (2 * total)) / (1 + z * z / total)
    margin = (
        z
        * math.sqrt(proportion * (1 - proportion) / total + z * z / (4 * total * total))
        / (1 + z * z / total)
    )
    return max(0.0, center - margin), min(1.0, center + margin)


def evaluate(rows: list[dict[str, Any]], thresholds: dict[str, Any]) -> dict[str, Any]:
    required_thresholds = {
        "minimum_cases",
        "wrong_accept_margin",
        "minimum_resolution_gain",
        "maximum_cost_ratio",
        "maximum_p95_latency_ms",
    }
    if set(thresholds) != required_thresholds or any(
        type(v) not in (int, float) or not math.isfinite(v) or v < 0 for v in thresholds.values()
    ):
        raise ValueError("invalid predeclared thresholds")
    if (
        thresholds["minimum_cases"] < 30
        or not isinstance(thresholds["minimum_cases"], int)
        or thresholds["wrong_accept_margin"] > 0.1
    ):
        raise ValueError("insufficient evaluation safety thresholds")
    grouped: dict[str, dict[str, dict[str, Any]]] = {}
    for row in rows:
        if (
            not isinstance(row, dict)
            or row.get("arm") not in ARMS
            or not isinstance(row.get("case_id"), str)
            or not row["case_id"]
        ):
            raise ValueError("invalid case or arm")
        if any(type(row.get(k)) is not bool for k in FLAGS):
            raise ValueError("human-labelled outcome fields must be booleans")
        if any(
            type(row.get(k)) not in (int, float) or not math.isfinite(row[k]) or row[k] < 0
            for k in NUMBERS
        ):
            raise ValueError("missing or invalid full-run usage")
        if type(row["model_calls"]) is not int or type(row["read_calls"]) is not int:
            raise ValueError("call counts must be integers")
        if (
            not isinstance(row.get("source_snapshot_hash"), str)
            or re.fullmatch(r"[0-9a-f]{64}", row["source_snapshot_hash"]) is None
        ):
            raise ValueError("missing frozen source snapshot")
        limits = row.get("limits")
        if not isinstance(limits, dict) or set(limits) != {
            "model_calls",
            "read_calls",
            "cost_micros",
        }:
            raise ValueError("missing common run limits")
        if any(type(v) is not int or v < 0 for v in limits.values()):
            raise ValueError("invalid run limits")
        if (
            row.get("measurement") not in {"live", "synthetic"}
            or row.get("scope") != "full_run"
            or type(row.get("usage_complete")) is not bool
        ):
            raise ValueError("measurements must state origin and full-run coverage")
        if not isinstance(row.get("model_fingerprint"), str) or not row["model_fingerprint"]:
            raise ValueError("missing model identity")
        case = grouped.setdefault(row["case_id"], {})
        if row["arm"] in case:
            raise ValueError("duplicate case/arm")
        case[row["arm"]] = row
    if not grouped:
        raise ValueError("empty evaluation")
    for case in grouped.values():
        if set(case) != set(ARMS):
            raise ValueError("all four arms need the same task set")
        for key in ("source_snapshot_hash", "limits", "model_fingerprint", "measurement"):
            if len({json.dumps(r[key], sort_keys=True) for r in case.values()}) != 1:
                raise ValueError("unpaired source, budget or model configuration")
    metrics: dict[str, Any] = {}
    for arm in ARMS:
        samples = [c[arm] for c in grouped.values()]
        n = len(samples)
        metrics[arm] = {"sample_count": n}
        for flag in FLAGS:
            count = sum(s[flag] for s in samples)
            metrics[arm][flag] = {
                "count": count,
                "rate": count / n,
                "interval_95": list(wilson(count, n)),
            }
        latencies = sorted(s["latency_ms"] for s in samples)
        metrics[arm].update(
            p95_latency_ms=latencies[math.ceil(0.95 * n) - 1],
            total_cost_micros=sum(s["cost_micros"] for s in samples),
            total_model_calls=sum(s["model_calls"] for s in samples),
            total_read_calls=sum(s["read_calls"] for s in samples),
        )
    baseline, debate, single = (
        metrics[k] for k in ("baseline_single", "debate", "single_supplement")
    )
    reasons = []
    if len(grouped) < thresholds["minimum_cases"]:
        reasons.append("insufficient_sample_size")
    if any(r["measurement"] != "live" or not r["usage_complete"] for r in rows):
        reasons.append("live_complete_measurements_required")
    if any(r[k] > r["limits"][k] for r in rows for k in r["limits"]):
        reasons.append("run_budget_exceeded")
    if (
        debate["wrong_accept"]["interval_95"][1] - baseline["wrong_accept"]["interval_95"][0]
        > thresholds["wrong_accept_margin"]
    ):
        reasons.append("wrong_accept_noninferiority_unproven")
    gain = debate["resolved"]["rate"] - baseline["resolved"]["rate"]
    if gain < thresholds["minimum_resolution_gain"]:
        reasons.append("insufficient_resolution_gain")
    if debate["false_consensus"]["count"]:
        reasons.append("false_consensus_observed")
    if debate["p95_latency_ms"] > thresholds["maximum_p95_latency_ms"]:
        reasons.append("latency_limit_exceeded")
    if (
        debate["total_cost_micros"]
        > baseline["total_cost_micros"] * thresholds["maximum_cost_ratio"]
    ):
        reasons.append("cost_limit_exceeded")
    if (
        single["resolved"]["rate"] >= debate["resolved"]["rate"]
        and single["wrong_accept"]["rate"] <= debate["wrong_accept"]["rate"]
        and single["total_cost_micros"] <= debate["total_cost_micros"]
    ):
        reasons.append("simpler_supplementation_is_not_worse")
    repeated = metrics["repeat_same_evidence"]
    if (
        repeated["resolved"]["rate"] >= debate["resolved"]["rate"]
        and repeated["wrong_accept"]["rate"] <= debate["wrong_accept"]["rate"]
        and repeated["total_cost_micros"] <= debate["total_cost_micros"]
    ):
        reasons.append("same_evidence_repetition_is_not_worse")
    return {
        "schema_version": "debate-evaluation-v1",
        "canary_allowed": not reasons,
        "reasons": reasons,
        "metrics": metrics,
        "thresholds": thresholds,
        "thresholds_hash": hashlib.sha256(
            json.dumps(thresholds, sort_keys=True).encode()
        ).hexdigest(),
        "delivery_changed": False,
        "deployment_changed": False,
    }


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--measurements", required=True, type=Path)
    parser.add_argument("--thresholds", required=True, type=Path)
    parser.add_argument("--report", required=True, type=Path)
    args = parser.parse_args()
    report = evaluate(
        [json.loads(line) for line in args.measurements.read_text().splitlines() if line.strip()],
        json.loads(args.thresholds.read_text()),
    )
    args.report.parent.mkdir(parents=True, exist_ok=True)
    args.report.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
    print(
        json.dumps(
            {"canary_allowed": report["canary_allowed"], "reasons": report["reasons"]},
            ensure_ascii=False,
        )
    )
    raise SystemExit(0 if report["canary_allowed"] else 2)


if __name__ == "__main__":
    main()
