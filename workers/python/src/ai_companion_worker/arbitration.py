"""Evidence-bound arbitration. Judgments never grant tool or write permissions."""

from __future__ import annotations

import copy
import hashlib
import json
import re
import time
from typing import Any, Callable, Mapping

from ai_companion_worker.agent_governance import apply_model_events, initial_budget_usage

VERSION = "evidence-arbitration-v1"
ACTIONS = {"accept", "reject", "merge", "retain", "need_evidence"}


def canonical(value: Any) -> str:
    return json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":"))


def validate_fact(claim: Mapping[str, Any]) -> None:
    fact = claim.get("fact")
    if fact is None:
        return
    if not isinstance(fact, dict) or set(fact) != {
        "subject",
        "predicate",
        "value",
        "scope",
        "valid_at",
    }:
        raise ValueError("invalid structured research fact")
    for key, value in fact.items():
        if (
            not isinstance(value, str)
            or len(value) > 300
            or (key in {"subject", "predicate", "value"} and not value.strip())
        ):
            raise ValueError("invalid research fact field")


def research_issues(analyses: list[dict[str, Any]]) -> list[dict[str, Any]]:
    groups: dict[tuple[str, ...], list[dict[str, Any]]] = {}
    for i, analysis in enumerate(analyses):
        for j, claim in enumerate(analysis["claims"]):
            fact = claim.get("fact")
            if not fact:
                # Legacy adapters can still expose simple field:value claims.
                match = re.fullmatch(r"([^:：\n]{1,80})[:：]\s*([^\n]{1,300})", claim["text"])
                if not match:
                    continue
                fact = {
                    "subject": "",
                    "predicate": match[1],
                    "value": match[2],
                    "scope": "",
                    "valid_at": "",
                }
            key = tuple(
                str(fact[k]).strip().casefold()
                for k in ("subject", "predicate", "scope", "valid_at")
            )
            groups.setdefault(key, []).append({**claim, "id": f"claim_{i}_{j}", "fact": fact})
    issues: list[dict[str, Any]] = []
    for key, candidates in sorted(groups.items()):
        if len({c["fact"]["value"].strip().casefold() for c in candidates}) < 2:
            continue
        issues.append(
            {
                "id": f"fact_{len(issues)}",
                "label": " / ".join(k for k in key if k),
                "candidates": candidates,
            }
        )
    return issues


def review_issues(reviews: list[dict[str, Any]]) -> list[dict[str, Any]]:
    candidates = [
        {**f, "id": f"finding_{i}_{j}"}
        for i, review in enumerate(reviews)
        for j, f in enumerate(review["findings"])
    ]
    return (
        [{"id": "review", "label": "协调审校意见", "candidates": candidates}] if candidates else []
    )


def validate_verdict(
    raw: Any, issues: list[dict[str, Any]], evidence: Mapping[str, str], kind: str
) -> list[dict[str, Any]]:
    if (
        not isinstance(raw, dict)
        or not isinstance(raw.get("decisions"), list)
        or len(raw["decisions"]) != len(issues)
    ):
        raise ValueError("arbitration must cover every issue exactly once")
    by_id = {issue["id"]: issue for issue in issues}
    seen: set[str] = set()
    verdicts = []
    for decision in raw["decisions"]:
        if not isinstance(decision, dict):
            raise ValueError("invalid arbitration decision")
        issue_id, action = decision.get("issue_id"), decision.get("action")
        if (
            not isinstance(issue_id, str)
            or issue_id not in by_id
            or issue_id in seen
            or action not in ACTIONS
        ):
            raise ValueError("unknown, repeated issue or action")
        seen.add(issue_id)
        candidates = {c["id"]: c for c in by_id[issue_id]["candidates"]}
        selected = decision.get("selected_ids")
        if (
            not isinstance(selected, list)
            or not all(isinstance(k, str) and k in candidates for k in selected)
            or len(set(selected)) != len(selected)
        ):
            raise ValueError("arbitration selected an unavailable candidate")
        if (action in {"accept", "merge"} and not selected) or (
            action in {"reject", "retain", "need_evidence"} and selected
        ):
            raise ValueError("arbitration selection contradicts its action")
        reason, instruction = decision.get("reason"), decision.get("instruction", "")
        if (
            not isinstance(reason, str)
            or not 1 <= len(reason.strip()) <= 1000
            or not isinstance(instruction, str)
            or len(instruction) > 1000
        ):
            raise ValueError("arbitration requires bounded reasons and instructions")
        citations = decision.get("evidence", [])
        if not isinstance(citations, list) or len(citations) > 12:
            raise ValueError("invalid arbitration evidence")
        used: set[str] = set()
        for ref in citations:
            if (
                not isinstance(ref, dict)
                or not isinstance(ref.get("id"), str)
                or ref["id"] not in evidence
            ):
                raise ValueError("arbitration cited unavailable evidence")
            quote = ref.get("quote")
            if (
                not isinstance(quote, str)
                or not 1 <= len(quote) <= 1000
                or quote not in evidence[ref["id"]]
            ):
                raise ValueError("arbitration quote is not in supplied evidence")
            used.add(ref["id"])
        if action in {"accept", "merge", "reject"} and not used:
            raise ValueError("a resolved judgment requires evidence")
        if kind == "research" and action in {"accept", "merge"}:
            if any(not used.intersection(candidates[k]["evidence_ids"]) for k in selected):
                raise ValueError("selected research claim lacks its original source")
        if kind == "review" and action in {"accept", "merge"} and not instruction.strip():
            raise ValueError("review arbitration requires an actionable instruction")
        verdicts.append(
            {
                "issue_id": issue_id,
                "action": action,
                "selected_ids": selected,
                "reason": reason,
                "instruction": instruction,
                "evidence": citations,
            }
        )
    return sorted(verdicts, key=lambda v: v["issue_id"])


def unresolved(issues: list[dict[str, Any]], reason: str) -> list[dict[str, Any]]:
    return [
        {
            "issue_id": i["id"],
            "action": "retain",
            "selected_ids": [],
            "reason": reason,
            "instruction": "",
            "evidence": [],
        }
        for i in issues
    ]


def make_arbitration_node(
    *, decisions: Any, allowance: Callable[..., Any], access_reason: Callable[..., str]
) -> Callable[..., Any]:
    def arbitrate(state: dict[str, Any]) -> dict[str, Any]:
        from ai_companion_worker.agent_runtime import ModelBudgetExceeded, _trace_event

        node = "arbitrate_parallel"
        plan = copy.deepcopy(state["parallel_plan"])
        kind = plan["kind"]
        issues = (
            research_issues(state["research_results"])
            if kind == "research"
            else review_issues(state["specialist_review"]["reviews"])
        )
        evidence: dict[str, str] = {}
        for i, observation in enumerate(state.get("observations", [])):
            evidence[str(observation.get("parallel_task_id") or f"observation_{i}")] = canonical(
                observation
            )
            if kind == "review":
                evidence[f"observation_{i}"] = canonical(observation)
        if kind == "review":
            evidence.update(request=str(state["user_message"]), candidate=plan["candidate"])
        # Dependent analysis IDs refer to other claims, never to primary evidence.
        if kind == "research":
            results = {r["id"]: r for r in state.get("parallel_results", [])}

            def primary(ids: list[str], visiting: set[str]) -> list[str]:
                found: set[str] = set()
                for key in ids:
                    if key in evidence:
                        found.add(key)
                    elif key not in visiting:
                        for claim in results.get(key, {}).get("analysis", {}).get("claims", []):
                            found.update(primary(claim["evidence_ids"], visiting | {key}))
                return sorted(found)

            for issue in issues:
                for candidate in issue["candidates"]:
                    candidate["evidence_ids"] = primary(candidate["evidence_ids"], set())
        packet = {
            "version": VERSION,
            "kind": kind,
            "issues": issues,
            "evidence": evidence,
            "task_contract": state.get("task_contract", {}),
        }
        identity = hashlib.sha256(
            canonical(
                {"packet": packet, "model_manifest": state.get("model_manifest", {})}
            ).encode()
        ).hexdigest()
        previous = next(
            (r for r in state.get("arbitrations", []) if r["input_hash"] == identity), None
        )
        events: list[dict[str, Any]] = []
        started = time.perf_counter_ns()
        status, reason = "retained", "evidence_unavailable"
        verdicts = unresolved(issues, reason)
        if previous is not None:
            status, reason, verdicts = previous["status"], previous["reason"], previous["decisions"]
        else:
            remaining = allowance(state, {}, node=node)["model_allowance"]
            method = getattr(decisions, "arbitrate", None)
            reason = access_reason(state, node)
            allocation = {k: int(v) // 2 for k, v in remaining.items()}
            allocation["remaining_calls"] = 1
            global_calls = int(
                state.get("budget_limits", {}).get("max_model_calls", remaining["remaining_calls"])
            ) - int(state.get("budget_usage", {}).get("model_calls", 0))
            if reason:
                reason = "model_access_unavailable"
            elif not callable(method):
                reason = "arbitrator_unavailable"
            elif len(issues) > 12 or len(canonical(packet).encode()) > 48000:
                reason = "arbitration_input_budget"
            elif (
                int(remaining["remaining_calls"]) < 1
                or global_calls < 2
                or any(int(v) < 2 for k, v in remaining.items() if k != "remaining_calls")
            ):
                reason = "arbitration_model_budget"
            else:
                preflight_blocked = False
                try:
                    raw = method(kind=kind, packet=packet, context={"model_allowance": allocation})
                    verdicts = validate_verdict(raw, issues, evidence, kind)
                    status, reason = "resolved", ""
                except Exception as exc:
                    reason = type(exc).__name__
                    preflight_blocked = isinstance(exc, ModelBudgetExceeded)
                consume = getattr(decisions, "consume_observability", None)
                events = consume() if callable(consume) else []
                if not events and not preflight_blocked:
                    events = [
                        {
                            "kind": "model_call",
                            "role": "composer",
                            "status": "succeeded" if status == "resolved" else "error",
                            "latency_ms": (time.perf_counter_ns() - started) // 1_000_000,
                        }
                    ]
                events = [
                    {**event, "graph_node": node, "arbitration_id": identity} for event in events
                ]
                spent = apply_model_events(initial_budget_usage(), node=node, events=events)
                consumed = {
                    "remaining_calls": spent["model_calls"],
                    "remaining_prompt_tokens": spent["prompt_tokens"],
                    "remaining_completion_tokens": spent["completion_tokens"],
                    "remaining_total_tokens": spent["prompt_tokens"] + spent["completion_tokens"],
                    "remaining_cost_micros": spent["cost_micros"],
                }
                if any(int(v) > allocation[k] for k, v in consumed.items()):
                    status, reason = "retained", "arbitration_budget_overrun"
            if status != "resolved":
                verdicts = unresolved(issues, reason)
        record = {
            "version": VERSION,
            "input_hash": identity,
            "kind": kind,
            "status": status,
            "reason": reason,
            "issues": issues,
            "decisions": verdicts,
        }
        plan["arbitrated"] = True
        values: dict[str, Any] = {
            "parallel_plan": plan,
            "arbitrations": [*state.get("arbitrations", []), record]
            if previous is None
            else state["arbitrations"],
            "budget_usage": apply_model_events(
                state.get("budget_usage", initial_budget_usage()), node=node, events=events
            ),
            "model_events": [*state.get("model_events", []), *events],
            "node_trace": [
                *state.get("node_trace", []),
                _trace_event(
                    node,
                    status,
                    details={
                        "kind": kind,
                        "issue_count": len(issues),
                        "reason": reason,
                        "cache_hit": previous is not None,
                    },
                ),
            ],
            "steps": int(state.get("steps", 0)) + 1,
        }
        if kind == "research":
            excluded = {c["id"] for issue in issues for c in issue["candidates"]}
            for verdict in verdicts:
                excluded.difference_update(verdict["selected_ids"])
            values["research_results"] = [
                {
                    **analysis,
                    "claims": [
                        c
                        for j, c in enumerate(analysis["claims"])
                        if f"claim_{i}_{j}" not in excluded
                    ],
                }
                for i, analysis in enumerate(state["research_results"])
            ]
            if state.get("task_contract", {}).get("artifact_types") and any(
                v["action"] in {"retain", "need_evidence"} for v in verdicts
            ):
                values.update(
                    outcome="arbitration_unresolved",
                    response="资料存在尚未解决的来源分歧，需核对后再生成文件。已保留原始证据和研究结果。",
                )
        else:
            verdict = verdicts[0]
            original = issues[0]["candidates"]
            if verdict["action"] == "reject":
                findings = []
            elif verdict["action"] in {"accept", "merge"}:
                selected = [c for c in original if c["id"] in verdict["selected_ids"]]
                findings = [
                    {
                        "quote": selected[0]["quote"],
                        "issue": verdict["instruction"],
                        "evidence_ids": [ref["id"] for ref in verdict["evidence"]],
                    }
                ]
            else:
                findings = original
            review = {
                **state["specialist_review"],
                "passed": not findings,
                "arbitration_id": identity,
            }
            field = (
                "artifact_validation" if review["target"] == "artifact" else "response_validation"
            )
            values["specialist_review"] = review
            values[field] = {
                **state.get(field, {}),
                "passed": not findings,
                "violations": [
                    {
                        "code": "specialist_review",
                        "message": f["issue"],
                        "quote": f["quote"],
                        "evidence_ids": f.get("evidence_ids", []),
                    }
                    for f in findings
                ],
            }
            if not findings:
                values["outcome"] = "completed"
            elif int(review["attempts"]) >= 2:
                values.update(
                    outcome="specialist_review_failed",
                    response="内容经一次修订后仍未通过专业审校，已停止交付。",
                )
        return values

    return arbitrate
