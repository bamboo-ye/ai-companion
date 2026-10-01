import copy
from pathlib import Path
import json
import pytest
from ai_companion_worker.evaluation.debate_gate import ARMS, evaluate, wilson

THRESHOLDS = json.loads(
    (Path(__file__).resolve().parents[3] / "evals/agent/debate/thresholds.v1.json").read_text()
)


def samples(n=1):
    return [
        {
            "case_id": str(i),
            "arm": arm,
            "source_snapshot_hash": "a" * 64,
            "model_fingerprint": "model-v1",
            "limits": {"model_calls": 20, "read_calls": 5, "cost_micros": 100},
            "wrong_accept": False,
            "false_reject": False,
            "resolved": arm == "debate",
            "false_consensus": False,
            "valid_supplement": arm == "debate",
            "latency_ms": 1000,
            "cost_micros": 2 if arm == "debate" else 1,
            "model_calls": 6,
            "read_calls": 2,
            "scope": "full_run",
            "measurement": "live",
            "usage_complete": True,
        }
        for i in range(n)
        for arm in ARMS
    ]


def test_small_perfect_sample_is_not_proof_of_safety():
    result = evaluate(samples(), THRESHOLDS)
    assert not result["canary_allowed"]
    assert "wrong_accept_noninferiority_unproven" in result["reasons"]
    assert wilson(0, 1)[1] > 0.7


def test_paired_live_gate_and_simpler_control():
    rows = samples(400)
    assert evaluate(rows, THRESHOLDS)["canary_allowed"]
    for row in rows:
        if row["arm"] == "single_supplement":
            row["resolved"] = True
    assert "simpler_supplementation_is_not_worse" in evaluate(rows, THRESHOLDS)["reasons"]


def test_repeated_same_evidence_control_can_disprove_debate_benefit():
    rows = samples(400)
    for row in rows:
        if row["arm"] == "repeat_same_evidence":
            row["resolved"] = True
    assert "same_evidence_repetition_is_not_worse" in evaluate(rows, THRESHOLDS)["reasons"]


def test_snapshot_must_be_a_digest_not_just_64_characters():
    rows = samples()
    for row in rows:
        row["source_snapshot_hash"] = "z" * 64
    with pytest.raises(ValueError, match="snapshot"):
        evaluate(rows, THRESHOLDS)


@pytest.mark.parametrize(
    "change",
    [
        {"measurement": "synthetic"},
        {"usage_complete": False},
        {"cost_micros": 1000},
        {"false_consensus": True},
    ],
)
def test_synthetic_incomplete_or_unsafe_measurement_cannot_enable(change):
    rows = samples(400)
    for row in rows:
        if "measurement" in change or row["arm"] == "debate":
            row.update(change)
    result = evaluate(rows, THRESHOLDS)
    assert not result["canary_allowed"] and not result["delivery_changed"]


@pytest.mark.parametrize(
    "mutation",
    ["missing_arm", "different_snapshot", "different_budget", "duplicate", "invalid_number"],
)
def test_unfair_comparisons_fail_validation(mutation):
    rows = samples()
    if mutation == "missing_arm":
        rows.pop()
    elif mutation == "duplicate":
        rows.append(copy.deepcopy(rows[0]))
    elif mutation == "different_snapshot":
        rows[0]["source_snapshot_hash"] = "b" * 64
    elif mutation == "different_budget":
        rows[0]["limits"]["model_calls"] = 200
    else:
        rows[0]["cost_micros"] = float("nan")
    with pytest.raises(ValueError):
        evaluate(rows, THRESHOLDS)
