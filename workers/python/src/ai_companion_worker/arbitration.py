"""Evidence-bound arbitration. Judgments never grant tool or write permissions."""

from __future__ import annotations

import copy
import hashlib
import json
import re
import time
from typing import Any, Callable, Mapping

from ai_companion_worker.agent_governance import apply_model_events, initial_budget_usage

VERSION = "evidence-arbitration-v2"
ACTIONS = {"accept", "reject", "merge", "retain", "need_evidence"}


def canonical(value: Any) -> str:
    return json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":"))


def digest(value: Any) -> str:
    return hashlib.sha256(canonical(value).encode()).hexdigest()


def claim_id(analysis: Mapping[str, Any], claim: Mapping[str, Any]) -> str:
    return str(
        claim.get("id")
        or "claim_"
        + digest(
            {
                "origin": analysis.get("id", analysis.get("question", "")),
                "claim": {**claim, "evidence_ids": sorted(claim.get("evidence_ids", []))},
            }
        )[:24]
    )


def resolution_status(verdicts: list[dict[str, Any]]) -> str:
    resolved = sum(v["action"] not in {"retain", "need_evidence"} for v in verdicts)
    if not verdicts or resolved == len(verdicts):
        return "resolved"
    return "partially_resolved" if resolved else "unresolved"


def effective_arbitrations(records: list[dict[str, Any]]) -> list[dict[str, Any]]:
    """Read v1 history too; only the latest decision per scope/issue is effective."""
    seen: set[tuple[str, str, str]] = set()
    result = []
    for record in reversed(records):
        kept = []
        for decision in record["decisions"]:
            key = (record["kind"], record.get("scope_id", ""), decision["issue_id"])
            if key not in seen:
                seen.add(key)
                kept.append(decision)
        if kept:
            ids = {v["issue_id"] for v in kept}
            result.append(
                {
                    **record,
                    "decisions": kept,
                    "issues": [i for i in record["issues"] if i["id"] in ids],
                    "resolution_status": resolution_status(kept),
                }
            )
    return list(reversed(result))


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
    for analysis in analyses:
        for claim in analysis["claims"]:
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
            groups.setdefault(key, []).append(
                {**claim, "id": claim_id(analysis, claim), "fact": fact}
            )
    issues: list[dict[str, Any]] = []
    for key, candidates in sorted(groups.items()):
        if len({c["fact"]["value"].strip().casefold() for c in candidates}) < 2:
            continue
        issues.append(
            {
                "id": "fact_" + digest(key)[:24],
                "label": " / ".join(k for k in key if k),
                "candidates": list({c["id"]: c for c in candidates}.values()),
            }
        )
    return issues


def review_issues(reviews: list[dict[str, Any]]) -> list[dict[str, Any]]:
    candidates = [
        {**f, "id": "finding_" + digest(f)[:24]} for review in reviews for f in review["findings"]
    ]
    return (
        [
            {
                "id": "review",
                "label": "协调审校意见",
                "candidates": list({c["id"]: c for c in candidates}.values()),
            }
        ]
        if candidates
        else []
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
    if len(by_id) != len(issues) or any(not k for k in by_id):
        raise ValueError("duplicate or empty issue")
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
        if len(candidates) != len(by_id[issue_id]["candidates"]) or any(not k for k in candidates):
            raise ValueError("duplicate or empty candidate")
        allowed_evidence = set(by_id[issue_id].get("evidence_ids", [])) | {
            key for c in candidates.values() for key in c.get("evidence_ids", [])
        }
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
                or (kind != "review" and ref["id"] not in allowed_evidence)
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
        if kind != "review" and action in {"accept", "merge"}:
            if any(not used.intersection(candidates[k]["evidence_ids"]) for k in selected):
                raise ValueError("selected research claim lacks its original source")
        if kind == "research" and action in {"accept", "merge"}:
            comparable: dict[tuple[str, ...], set[str]] = {}
            for key in selected:
                fact = candidates[key].get("fact")
                if fact:
                    scope = tuple(
                        str(fact[k]).strip().casefold()
                        for k in ("subject", "predicate", "scope", "valid_at")
                    )
                    comparable.setdefault(scope, set()).add(str(fact["value"]).strip().casefold())
            if any(len(values) > 1 for values in comparable.values()):
                raise ValueError("resolved selection still contains contradictory facts")
        if kind == "review" and action in {"accept", "merge"} and not instruction.strip():
            raise ValueError("review arbitration requires an actionable instruction")
        requests = decision.get("evidence_requests", [])
        if (
            not isinstance(requests, list)
            or len(requests) > 2
            or (requests and action != "need_evidence")
        ):
            raise ValueError("invalid evidence requests")
        for request in requests:
            if (
                not isinstance(request, dict)
                or set(request) != {"question", "discriminates", "tool_name", "arguments"}
                or not isinstance(request["arguments"], dict)
                or len(canonical(request["arguments"]).encode()) > 4000
                or any(
                    not isinstance(request[k], str) or not 1 <= len(request[k].strip()) <= 1000
                    for k in ("question", "discriminates", "tool_name")
                )
            ):
                raise ValueError("invalid evidence request")
        classification = decision.get(
            "classification",
            "unresolved" if action in {"retain", "need_evidence"} else "contradiction",
        )
        if classification not in {
            "equivalent",
            "scope_difference",
            "version_update",
            "contradiction",
            "unresolved",
        }:
            raise ValueError("invalid classification")
        if action not in {"retain", "need_evidence"} and classification == "unresolved":
            raise ValueError("resolved action has unresolved classification")
        if classification == "version_update" and not any(
            re.search(r"替代|取代|作废|废止|supersed|replaces|obsolete", ref["quote"], re.I)
            for ref in citations
        ):
            raise ValueError("version replacement lacks explicit source evidence")
        responses = decision.get("assessment_responses", [])
        if not isinstance(responses, list) or len(responses) > 144:
            raise ValueError("invalid assessment responses")
        response_ids: set[str] = set()
        for response in responses:
            if (
                not isinstance(response, dict)
                or not isinstance(response.get("id"), str)
                or not response["id"]
                or response["id"] in response_ids
                or not isinstance(response.get("reason"), str)
                or not 1 <= len(response["reason"].strip()) <= 1000
                or not isinstance(response.get("evidence"), list)
                or not 1 <= len(response["evidence"]) <= 12
            ):
                raise ValueError("invalid assessment response")
            response_ids.add(response["id"])
            for ref in response["evidence"]:
                if (
                    not isinstance(ref, dict)
                    or not isinstance(ref.get("id"), str)
                    or ref["id"] not in evidence
                    or (kind != "review" and ref["id"] not in allowed_evidence)
                    or not isinstance(ref.get("quote"), str)
                    or not 1 <= len(ref["quote"]) <= 1000
                    or ref["quote"] not in evidence[ref["id"]]
                ):
                    raise ValueError("ungrounded assessment response")
        verdicts.append(
            {
                "issue_id": issue_id,
                "action": action,
                "selected_ids": selected,
                "reason": reason,
                "instruction": instruction,
                "evidence": citations,
                "classification": classification,
                "evidence_requests": copy.deepcopy(requests),
                "assessment_responses": copy.deepcopy(responses),
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


def arbitration_packet(
    state: Mapping[str, Any], *, require_original: bool = False
) -> dict[str, Any]:
    plan = state["parallel_plan"]
    kind = plan["kind"]
    issues = (
        research_issues(state.get("research_candidates", state["research_results"]))
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
        if require_original:
            from ai_companion_worker.debate import evidence_fragments

            original_ids = {
                str(o.get("parallel_task_id") or f"observation_{i}")
                for i, o in enumerate(state.get("observations", []))
                if evidence_fragments(o.get("data"))
            }
            covered = {c["id"] for issue in issues for c in issue["candidates"]}
            for analysis in state.get("research_candidates", state["research_results"]):
                for claim in analysis["claims"]:
                    identity = claim_id(analysis, claim)
                    roots = primary(claim["evidence_ids"], set())
                    if identity not in covered and not original_ids.intersection(roots):
                        issues.append(
                            {
                                "id": "support_" + digest(identity)[:24],
                                "label": "原文依据核对：" + claim["text"][:120],
                                "candidates": [{**claim, "id": identity, "evidence_ids": roots}],
                            }
                        )
                        covered.add(identity)
    return {
        "version": VERSION,
        "kind": kind,
        "issues": issues,
        "evidence": evidence,
        "task_contract": state.get("task_contract", {}),
    }


def apply_arbitration(state: Mapping[str, Any], record: dict[str, Any]) -> dict[str, Any]:
    kind, issues, verdicts = record["kind"], record["issues"], record["decisions"]
    identity = record["input_hash"]
    plan = {**state["parallel_plan"], "arbitrated": True}
    values: dict[str, Any] = {"parallel_plan": plan}
    if kind == "research":
        originals = copy.deepcopy(state.get("research_candidates", state["research_results"]))
        known = {claim_id(a, c) for a in originals for c in a["claims"]}
        added = [c for issue in issues for c in issue["candidates"] if c["id"] not in known]
        if added:
            originals.append({"question": "补证复核", "claims": added})
        values["research_candidates"] = originals
        excluded = {c["id"] for issue in issues for c in issue["candidates"]}
        for verdict in verdicts:
            excluded.difference_update(verdict["selected_ids"])
        values["research_results"] = [
            {
                **analysis,
                "claims": [c for c in analysis["claims"] if claim_id(analysis, c) not in excluded],
            }
            for analysis in originals
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
        field = "artifact_validation" if review["target"] == "artifact" else "response_validation"
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


def make_arbitration_node(
    *, decisions: Any, allowance: Callable[..., Any], access_reason: Callable[..., str]
) -> Callable[..., Any]:
    def arbitrate(state: dict[str, Any]) -> dict[str, Any]:
        from ai_companion_worker.agent_runtime import ModelBudgetExceeded, _trace_event

        node = "arbitrate_parallel"
        plan = copy.deepcopy(state["parallel_plan"])
        packet = arbitration_packet(state)
        kind, issues, evidence = packet["kind"], packet["issues"], packet["evidence"]
        identity = hashlib.sha256(
            canonical(
                {
                    "packet": packet,
                    "user_id": state["user_id"],
                    "model_manifest": state.get("model_manifest", {}),
                }
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
            status, reason, verdicts = (
                previous.get("call_status", previous["status"]),
                previous["reason"],
                previous["decisions"],
            )
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
            "status": "resolved" if resolution_status(verdicts) == "resolved" else "retained",
            "call_status": (
                previous.get("call_status", "succeeded")
                if previous
                else "succeeded"
                if status == "resolved"
                else "failed"
                if events
                else "skipped"
            ),
            "resolution_status": resolution_status(verdicts),
            "termination_reason": "resolved"
            if resolution_status(verdicts) == "resolved"
            else reason or "evidence_unavailable",
            "scope_id": digest(plan.get("candidate", "research")),
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
        values.update(apply_arbitration(state, record))
        return values

    return arbitrate
