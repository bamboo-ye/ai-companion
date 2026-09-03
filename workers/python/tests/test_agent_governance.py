from __future__ import annotations

import json
import re
import unittest
from pathlib import Path

from ai_companion_worker.agent_governance import (
    GRAPH_VERSION,
    BudgetPolicy,
    apply_model_events,
    initial_budget_usage,
    model_budget_exhaustion,
    model_manifest_fingerprint,
    node_contract_manifest,
    tool_catalog_fingerprint,
)


class AgentGovernanceTest(unittest.TestCase):
    def test_go_runtime_and_eval_baseline_use_same_graph_version(self) -> None:
        repository = Path(__file__).resolve().parents[3]
        go_source = (repository / "internal/agent/service.go").read_text(encoding="utf-8")
        match = re.search(r'GraphVersion\s*=\s*"([^"]+)"', go_source)
        self.assertIsNotNone(match)
        self.assertEqual(match.group(1), GRAPH_VERSION)
        baseline = json.loads(
            (repository / "evals/agent/baselines/pr.v1.json").read_text(encoding="utf-8")
        )
        self.assertEqual(baseline["graph_version"], GRAPH_VERSION)

    def test_every_graph_node_has_an_executable_contract(self) -> None:
        contracts = node_contract_manifest()
        self.assertEqual(contracts["plan"]["model_role"], "planner")
        self.assertEqual(contracts["generate_response"]["model_role"], "responder")
        self.assertEqual(contracts["email_quality_gate"]["kind"], "deterministic")
        self.assertEqual(
            contracts["email_quality_gate"]["recovery"], "checkpoint_replay"
        )
        self.assertEqual(contracts["approval"]["recovery"], "typed_interrupt")
        self.assertEqual(contracts["wait_task"]["kind"], "external_wait")
        self.assertEqual(contracts["commit_tool"]["side_effects"], "approved_write")

    def test_model_usage_accumulates_attempts_tokens_and_integer_cost(self) -> None:
        usage = apply_model_events(
            initial_budget_usage(),
            node="life",
            events=[
                {
                    "kind": "model_call",
                    "role": "router",
                    "prompt_tokens": 100,
                    "completion_tokens": 20,
                    "cached_tokens": 30,
                    "reasoning_tokens": 4,
                    "cost_micros": 9,
                },
                {
                    "kind": "model_call",
                    "role": "router",
                    "prompt_tokens": 80,
                    "completion_tokens": 10,
                    "cost_micros": 6,
                },
            ],
        )
        self.assertEqual(usage["model_calls"], 2)
        self.assertEqual(usage["prompt_tokens"], 180)
        self.assertEqual(usage["completion_tokens"], 30)
        self.assertEqual(usage["cost_micros"], 15)
        self.assertEqual(usage["model_calls_by_node"], {"life": 2})
        self.assertEqual(usage["model_calls_by_role"], {"router": 2})

    def test_budget_exhaustion_is_deterministic(self) -> None:
        limits = BudgetPolicy(max_model_calls=2).limits()
        usage = initial_budget_usage()
        usage["model_calls"] = 2
        self.assertIn("调用次数", model_budget_exhaustion(limits, usage, "life"))

        usage = initial_budget_usage()
        usage["model_calls_by_node"] = {"plan": 2}
        self.assertIn("plan", model_budget_exhaustion(limits, usage, "plan"))

    def test_tool_poll_backoff_limits_are_explicit(self) -> None:
        policy = BudgetPolicy()
        self.assertEqual(policy.tool_poll_interval_ms, 500)
        self.assertEqual(policy.tool_poll_max_interval_ms, 5_000)
        self.assertEqual(policy.limits()["tool_poll_max_interval_ms"], 5_000)
        with self.assertRaisesRegex(ValueError, "POLL_MAX_INTERVAL"):
            BudgetPolicy(
                tool_poll_interval_ms=1_000,
                tool_poll_max_interval_ms=500,
            ).validate()

    def test_model_manifest_fingerprint_is_canonical_and_self_excluding(self) -> None:
        first = {"provider": "openrouter", "roles": {"router": ["model-a"]}}
        second = {"roles": {"router": ["model-a"]}, "provider": "openrouter"}
        fingerprint = model_manifest_fingerprint(first)
        self.assertEqual(fingerprint, model_manifest_fingerprint(second))
        second["fingerprint"] = fingerprint
        self.assertEqual(fingerprint, model_manifest_fingerprint(second))
        second["roles"] = {"router": ["model-b"]}
        self.assertNotEqual(fingerprint, model_manifest_fingerprint(second))

    def test_tool_catalog_fingerprint_is_canonical_and_contract_sensitive(self) -> None:
        first = [
            {
                "name": "life_query_plan",
                "parameters": {"type": "object", "properties": {}},
            }
        ]
        second = [
            {
                "parameters": {"properties": {}, "type": "object"},
                "name": "life_query_plan",
            }
        ]
        fingerprint = tool_catalog_fingerprint(first)
        self.assertEqual(fingerprint, tool_catalog_fingerprint(second))
        second[0]["parameters"] = {
            "type": "object",
            "required": ["month"],
        }
        self.assertNotEqual(fingerprint, tool_catalog_fingerprint(second))


if __name__ == "__main__":
    unittest.main()
