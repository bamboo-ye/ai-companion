"""Checkpointed, bounded read/research DAGs and independent specialist reviews.

Only the join node writes parent observations and usage. Every dispatched node
owns one result slot; tools remain subject to the Go gateway's ACL/revision fence.
"""

from __future__ import annotations

import copy
import hashlib
import json
import os
import re
import time
from dataclasses import asdict
from typing import Any, Callable, Mapping, cast

from langgraph.types import Send, interrupt

from ai_companion_worker.arbitration import research_issues, validate_fact

from ai_companion_worker.agent_governance import (
    GRAPH_NAME,
    GRAPH_VERSION,
    apply_model_events,
    initial_budget_usage,
)

READ_TOOLS = frozenset(
    {
        "work_wiki_search",
        "work_wiki_read",
        "work_wiki_follow_links",
        "work_query_documents",
        "product_knowledge_search",
        "product_knowledge_read",
    }
)
EXTRACT_TOOL = "work_extract_attached_document"


def merge_task_results(
    left: list[dict[str, Any]], right: list[dict[str, Any]]
) -> list[dict[str, Any]]:
    if any(item.get("reset") is True for item in right):
        return [item for item in right if item.get("reset") is not True]
    merged = {str(item["id"]): item for item in left}
    for item in right:
        key = str(item["id"])
        previous = merged.get(key)
        if previous is None or int(item["generation"]) > int(previous["generation"]):
            merged[key] = item
        elif item["generation"] == previous["generation"] and item != previous:
            raise ValueError("conflicting parallel task results")
    return [merged[key] for key in sorted(merged)]


def validate_research_tasks(value: Any, catalog: Any) -> list[dict[str, Any]]:
    from ai_companion_worker.agent_runtime import _validate_tool_arguments

    if value in (None, (), []):
        return []
    if not isinstance(value, (list, tuple)) or not 2 <= len(value) <= 6:
        raise ValueError("research plan requires 2..6 tasks")
    definitions = {item["name"]: item for item in catalog if isinstance(item, dict)}
    tasks: list[dict[str, Any]] = []
    ids: set[str] = set()
    for item in value:
        if not isinstance(item, Mapping):
            raise ValueError("research task must be an object")
        task_id = item.get("id")
        question = item.get("question")
        name, arguments, dependencies = (
            item.get("tool_name"),
            item.get("arguments"),
            item.get("depends_on", []),
        )
        if (
            not isinstance(task_id, str)
            or not re.fullmatch(r"[a-z][a-z0-9_]{0,31}", task_id)
            or task_id in ids
        ):
            raise ValueError("invalid or duplicate research task id")
        if not isinstance(question, str) or not 1 <= len(question.strip()) <= 1000:
            raise ValueError("research question must be 1..1000 characters")
        if not isinstance(name, str) or name not in READ_TOOLS or name not in definitions:
            raise ValueError("research tool is not in the trusted read-only catalog")
        if not isinstance(arguments, Mapping):
            raise ValueError("research arguments must be an object")
        _validate_tool_arguments(arguments, definitions[name])
        if (
            not isinstance(dependencies, list)
            or not all(isinstance(d, str) for d in dependencies)
            or len(set(dependencies)) != len(dependencies)
        ):
            raise ValueError("research dependencies must be unique task ids")
        ids.add(task_id)
        tasks.append(
            {
                "id": task_id,
                "question": question.strip(),
                "tool_name": name,
                "arguments": dict(arguments),
                "depends_on": dependencies,
            }
        )
    completed: set[str] = set()
    while len(completed) < len(tasks):
        ready = {
            t["id"] for t in tasks if t["id"] not in completed and set(t["depends_on"]) <= completed
        }
        if not ready:
            raise ValueError("research dependencies contain a cycle or unknown task")
        completed.update(ready)
    return tasks


def task_route(state: Mapping[str, Any]) -> bool:
    if state.get("module") != "work" or state.get("parallel_plan"):
        return False
    proposed = state.get("proposed_tool", {})
    sources = state.get("task_contract", {}).get("source_document_ids", [])
    extraction = (
        proposed.get("name") == EXTRACT_TOOL
        and 2 <= len(sources) <= 3
        and os.getenv("AGENT_PARALLEL_ATTACHMENTS", "true").lower() == "true"
    )
    return extraction or bool(state.get("plan", {}).get("research_tasks"))


def review_requested(state: Mapping[str, Any]) -> bool:
    if state.get("module") != "work" or state.get("outcome") not in (None, "", "completed"):
        return False
    mode = os.getenv("AGENT_SPECIALIST_REVIEW_MODE", "requested").lower()
    if mode not in ("off", "requested", "always"):
        raise ValueError("AGENT_SPECIALIST_REVIEW_MODE must be off, requested or always")
    if mode == "off":
        return False
    if mode == "requested" and not re.search(
        r"审校|审阅|仔细核对|严格检查|核查来源|fact.check|peer.review",
        str(state.get("user_message", "")),
        re.I,
    ):
        return False
    candidate = review_candidate(state)
    previous = state.get("specialist_review", {})
    return (
        bool(candidate)
        and (
            previous.get("candidate_hash") != _digest(candidate) or previous.get("passed") is False
        )
        and int(previous.get("attempts", 0)) < 2
    )


def review_candidate(state: Mapping[str, Any]) -> str:
    if state.get("artifact_validation", {}).get("applicable") is True:
        return json.dumps(
            state.get("proposed_tool", {}).get("arguments", {}), ensure_ascii=False, sort_keys=True
        )
    return str(state.get("response", ""))


def _digest(value: str) -> str:
    return hashlib.sha256(value.encode()).hexdigest()


def make_parallel_nodes(
    *,
    tools: Any,
    decisions: Any,
    policy: Any,
    allowance: Callable[..., Any],
    access_reason: Callable[..., str],
) -> dict[str, Callable[..., Any]]:
    from ai_companion_worker.agent_runtime import (
        _skill_run_status,
        _skill_run_id,
        _trusted_tool_definition,
        _validate_tool_arguments,
        _trace_event,
        _observation,
        _updated_recovery,
    )

    def update(state: Mapping[str, Any], node: str, **values: Any) -> dict[str, Any]:
        return {
            "node_trace": [*state.get("node_trace", []), _trace_event(node, "succeeded")],
            "steps": int(state.get("steps", 0)) + 1,
            **values,
        }

    def prepare_parallel(state: dict[str, Any]) -> dict[str, Any]:
        tasks: list[dict[str, Any]] = []
        if state.get("proposed_tool", {}).get("name") == EXTRACT_TOOL:
            kind = "attachments"
            for i, _ in enumerate(state["task_contract"]["source_document_ids"], 1):
                tasks.append(
                    {
                        "id": f"attachment_{i}_round_1",
                        "kind": "read",
                        "tool_name": EXTRACT_TOOL,
                        "arguments": {"attachment_index": i, "round_start": 1},
                        "depends_on": [],
                    }
                )
        else:
            kind = "research"
            for task in validate_research_tasks(
                state["plan"]["research_tasks"], state["context"].get("tools", [])
            ):
                deps = ["analysis_" + d for d in task["depends_on"]]
                tasks.append(
                    {**task, "id": "read_" + task["id"], "kind": "read", "depends_on": deps}
                )
                tasks.append(
                    {
                        "id": "analysis_" + task["id"],
                        "kind": "analysis",
                        "question": task["question"],
                        "depends_on": ["read_" + task["id"], *deps],
                    }
                )
        return update(
            state,
            "prepare_parallel",
            parallel_plan={"kind": kind, "tasks": tasks, "generation": 0},
            parallel_results=[{"reset": True}],
            proposed_tool={},
            preparation={},
            tool_result={},
            outcome="",
        )

    def prepare_review(state: dict[str, Any]) -> dict[str, Any]:
        candidate = review_candidate(state)
        # Do not silently review a prefix of a large artifact. The caller can
        # explicitly request a smaller review when it exceeds this budget.
        if len(candidate) > 24000:
            return update(
                state,
                "prepare_review",
                outcome="review_budget_exhausted",
                response="待审校内容超过本次审校窗口，请缩小审校范围。",
            )
        tasks = [
            {"id": kind, "kind": "review", "focus": kind, "depends_on": []}
            for kind in ("grounding", "clarity")
        ]
        review = {
            "candidate_hash": _digest(candidate),
            "attempts": int(state.get("specialist_review", {}).get("attempts", 0)) + 1,
            "target": "artifact"
            if state.get("artifact_validation", {}).get("applicable") is True
            else "response",
        }
        return update(
            state,
            "prepare_review",
            parallel_plan={
                "kind": "review",
                "tasks": tasks,
                "generation": 0,
                "candidate": candidate,
            },
            parallel_results=[{"reset": True}],
            specialist_review=review,
            outcome="",
        )

    def schedule_parallel(state: dict[str, Any]) -> dict[str, Any]:
        plan = copy.deepcopy(state["parallel_plan"])
        done = {t["id"] for t in plan["tasks"] if t.get("status") == "completed"}
        ready = [
            t
            for t in plan["tasks"]
            if t.get("status", "ready") in ("ready", "retry") and set(t["depends_on"]) <= done
        ]
        reads = [t for t in ready if t["kind"] == "read" and not t.get("action")]
        action_index = int(state.get("action_index", 0))
        if action_index + len(reads) > int(state.get("action_budget", policy.max_actions)):
            return update(
                state,
                "schedule_parallel",
                outcome="action_limit",
                response="并行任务的工具行动预算已用完，已保留完成结果。",
            )
        for task in reads:
            definition = _trusted_tool_definition(cast(Any, state), task["tool_name"])
            if task["tool_name"] not in READ_TOOLS | {EXTRACT_TOOL} or definition is None:
                raise ValueError("parallel task requested an unavailable read tool")
            _validate_tool_arguments(task["arguments"], definition)
            action_index += 1
            task["action"] = action_index
        models = [t for t in ready if t["kind"] != "read"]
        if models:
            reason = access_reason(state, "execute_parallel_model")
            remaining = allowance(state, {}, node="execute_parallel_model")["model_allowance"]
            if reason or any(int(v) < len(models) + 1 for v in remaining.values()):
                return update(
                    state,
                    "schedule_parallel",
                    outcome="model_budget_exhausted",
                    response="并行分析或审校的模型预算不足，已保留完成结果。",
                )
            for i, task in enumerate(models):
                # Keep one share for the eventual parent synthesis/repair.
                task["allocation"] = {
                    k: divmod(int(v), len(models) + 1)[0] for k, v in remaining.items()
                }
                task["allocation"]["remaining_calls"] = min(
                    2, task["allocation"]["remaining_calls"]
                )
        plan["generation"] += 1
        plan["dispatch"] = [t["id"] for t in ready]
        for task in ready:
            task["status"] = "dispatched"
        usage = dict(state.get("budget_usage", initial_budget_usage()))
        usage["actions"] = action_index
        return update(
            state,
            "schedule_parallel",
            parallel_plan=plan,
            action_index=action_index,
            budget_usage=usage,
        )

    def dispatch_parallel(state: dict[str, Any]) -> str | list[Send]:
        if state.get("outcome"):
            return "finalize"
        plan = state["parallel_plan"]
        results = {r["id"]: r for r in state.get("parallel_results", [])}
        sends: list[Send] = []
        for task in plan["tasks"]:
            if task["id"] not in plan["dispatch"]:
                continue
            job = {
                "task": task,
                "generation": plan["generation"],
                "run_id": state["run_id"],
                "user_id": state["user_id"],
                "module": state["module"],
                "message": state["user_message"],
                "candidate": plan.get("candidate", ""),
                "dependencies": {d: results[d] for d in task["depends_on"]},
                "observations": state.get("observations", []) if task["kind"] == "review" else [],
                "task_contract": state.get("task_contract", {}),
            }
            sends.append(
                Send(
                    "execute_parallel_read" if task["kind"] == "read" else "execute_parallel_model",
                    {"parallel_job": job},
                )
            )
        if not sends:
            raise ValueError("parallel scheduler has no runnable tasks")
        return sends

    def execute_parallel_read(state: dict[str, Any]) -> dict[str, Any]:
        job, task = state["parallel_job"], state["parallel_job"]["task"]
        result: dict[str, Any] = {
            "id": task["id"],
            "generation": job["generation"],
            "events": [],
            "status": "completed",
        }
        try:
            if task.get("task_id"):
                outcome = tools.observe(
                    run_id=job["run_id"],
                    user_id=job["user_id"],
                    module=job["module"],
                    task_id=task["task_id"],
                )
            else:
                outcome = tools.prepare(
                    run_id=job["run_id"],
                    user_id=job["user_id"],
                    module=job["module"],
                    tool_name=task["tool_name"],
                    arguments=task["arguments"],
                    idempotency_key=f"{job['run_id']}:task:{task['id']}:prepare",
                )
                if outcome.tool_name != task["tool_name"] or outcome.status != "completed":
                    raise ValueError(
                        "parallel read must not require approval or return another tool"
                    )
            result["source"] = asdict(outcome)
            status = _skill_run_status(outcome.data)
            if status in ("queued", "running"):
                result.update(status="waiting", task_id=_skill_run_id(outcome.data))
                if not result["task_id"]:
                    raise ValueError("asynchronous read has no task id")
            elif status in ("failed", "cancelled", "waiting_confirmation"):
                result.update(status="failed", retryable=False, error="read_task_" + status)
            elif not outcome.response.strip():
                raise ValueError("read result must contain a response")
        except Exception as exc:
            result.update(
                status="failed",
                error=type(exc).__name__,
                retryable=bool(getattr(exc, "retryable", False)),
            )
        return {"parallel_results": [result]}

    def execute_parallel_model(state: dict[str, Any]) -> dict[str, Any]:
        job, task = state["parallel_job"], state["parallel_job"]["task"]
        result: dict[str, Any] = {
            "id": task["id"],
            "generation": job["generation"],
            "status": "completed",
        }
        started = time.perf_counter_ns()
        role = "composer"
        try:
            method = getattr(
                decisions, "analyze_evidence" if task["kind"] == "analysis" else "review_candidate"
            )
            evidence = {
                key: r.get("source", r.get("analysis", {}))
                for key, r in job["dependencies"].items()
            }
            if task["kind"] == "review":
                evidence = {f"observation_{i}": o for i, o in enumerate(job["observations"])}
            context = {
                "model_allowance": task["allocation"],
                "evidence": evidence,
                "task_contract": job["task_contract"],
            }
            if task["kind"] == "analysis":
                analysis = method(question=task["question"], context=context)
                claims = analysis.get("claims")
                if not isinstance(claims, list) or len(claims) > 12:
                    raise ValueError("research claims must be a bounded list")
                for claim in claims:
                    if (
                        not isinstance(claim, dict)
                        or not isinstance(claim.get("text"), str)
                        or not 1 <= len(claim["text"]) <= 1500
                        or not isinstance(claim.get("evidence_ids"), list)
                        or not claim["evidence_ids"]
                        or not all(
                            isinstance(k, str) and k in evidence for k in claim["evidence_ids"]
                        )
                    ):
                        raise ValueError("research claim lacks supplied evidence")
                    validate_fact(claim)
                result["analysis"] = {"question": task["question"], "claims": claims}
            else:
                review = method(focus=task["focus"], candidate=job["candidate"], context=context)
                findings = review.get("findings")
                if (
                    not isinstance(review.get("passed"), bool)
                    or not isinstance(findings, list)
                    or len(findings) > 4
                    or review["passed"] == bool(findings)
                ):
                    raise ValueError("invalid specialist review")
                for finding in findings:
                    if (
                        not isinstance(finding, dict)
                        or not isinstance(finding.get("quote"), str)
                        or not finding["quote"]
                        or finding["quote"] not in job["candidate"]
                        or not isinstance(finding.get("issue"), str)
                        or not 1 <= len(finding["issue"]) <= 500
                    ):
                        raise ValueError("review finding must identify actual candidate text")
                    if not isinstance(finding.get("evidence_ids", []), list) or not all(
                        isinstance(k, str) and k in evidence
                        for k in finding.get("evidence_ids", [])
                    ):
                        raise ValueError("review cited unavailable evidence")
                result["review"] = review
        except Exception as exc:
            result.update(
                status="failed",
                error=type(exc).__name__,
                retryable=bool(getattr(exc, "retryable", False)) or isinstance(exc, ValueError),
            )
        consume = getattr(decisions, "consume_observability", None)
        events = consume() if callable(consume) else []
        if not events:
            events = [
                {
                    "kind": "model_call",
                    "role": role,
                    "status": "succeeded" if result["status"] == "completed" else "error",
                    "latency_ms": (time.perf_counter_ns() - started) // 1_000_000,
                }
            ]
        result["events"] = [
            {**e, "graph_node": "execute_parallel_model", "parallel_task_id": task["id"]}
            for e in events
        ]
        spent = apply_model_events(
            initial_budget_usage(), node="execute_parallel_model", events=result["events"]
        )
        allocation = task["allocation"]
        consumed = {
            "remaining_calls": spent["model_calls"],
            "remaining_prompt_tokens": spent["prompt_tokens"],
            "remaining_completion_tokens": spent["completion_tokens"],
            "remaining_total_tokens": spent["prompt_tokens"] + spent["completion_tokens"],
            "remaining_cost_micros": spent["cost_micros"],
        }
        if any(int(value) > int(allocation[key]) for key, value in consumed.items()):
            # Settle all actual usage, even if the provider exceeded its
            # reservation. Such a branch must never receive another retry.
            result.update(status="failed", retryable=False, error="branch_model_budget_exceeded")
        return {"parallel_results": [result]}

    def join_parallel(state: dict[str, Any]) -> dict[str, Any]:
        plan = copy.deepcopy(state["parallel_plan"])
        results = {r["id"]: r for r in state.get("parallel_results", [])}
        observations = list(state.get("observations", []))
        usage = dict(state.get("budget_usage", initial_budget_usage()))
        events = list(state.get("model_events", []))
        additions: list[dict[str, Any]] = []
        failed: list[str] = []
        for task in plan["tasks"]:
            if task["id"] not in plan["dispatch"]:
                continue
            result = results[task["id"]]
            if result["generation"] != plan["generation"]:
                raise ValueError("missing result from the current dispatch generation")
            if result["events"]:
                usage = apply_model_events(
                    usage, node="execute_parallel_model", events=result["events"]
                )
                events.extend(result["events"])
            task["status"] = result["status"]
            if result["status"] == "failed":
                task["failures"] = int(task.get("failures", 0)) + 1
                if result.get("retryable") and task["failures"] < 2:
                    task["status"] = "retry"
                else:
                    failed.append(task["id"])
            elif result["status"] == "waiting":
                task["task_id"] = result["task_id"]
            elif task["kind"] == "read":
                observation = _observation(
                    cast(
                        Any,
                        {
                            "action_index": task["action"],
                            "proposed_tool": {
                                "name": task["tool_name"],
                                "arguments": task["arguments"],
                            },
                        },
                    ),
                    result["source"],
                )
                observation["parallel_task_id"] = task["id"]
                observations.append(observation)
                if plan["kind"] == "attachments":
                    data = result["source"].get("data")
                    output = data.get("output") if isinstance(data, Mapping) else None
                    if not isinstance(output, Mapping):
                        failed.append(task["id"])
                    elif output.get("has_more") is True:
                        next_round = output.get("next_round")
                        current = task["arguments"]["round_start"]
                        if (
                            not isinstance(next_round, int)
                            or isinstance(next_round, bool)
                            or not current < next_round <= 100
                        ):
                            failed.append(task["id"])
                        else:
                            index = task["arguments"]["attachment_index"]
                            additions.append(
                                {
                                    "id": f"attachment_{index}_round_{next_round}",
                                    "kind": "read",
                                    "tool_name": EXTRACT_TOOL,
                                    "arguments": {
                                        "attachment_index": index,
                                        "round_start": next_round,
                                    },
                                    "depends_on": [task["id"]],
                                }
                            )
        plan["tasks"].extend(additions)
        values: dict[str, Any] = {
            "parallel_plan": plan,
            "observations": observations,
            "budget_usage": usage,
            "model_events": events,
        }
        if failed:
            values.update(
                outcome="parallel_task_failed",
                response="部分并行任务未完成，已保留成功分支结果：" + ", ".join(failed),
            )
        elif all(t.get("status") == "completed" for t in plan["tasks"]):
            plan["complete"] = True
            if plan["kind"] == "research":
                values["research_results"] = [
                    results[t["id"]]["analysis"] for t in plan["tasks"] if t["kind"] == "analysis"
                ]
                values.update(proposed_tool={}, needs_response=True)
            elif plan["kind"] == "review":
                reviews = [results[t["id"]]["review"] for t in plan["tasks"]]
                findings = [f for r in reviews for f in r["findings"]]
                values["specialist_review"] = {
                    **state["specialist_review"],
                    "passed": not findings,
                    "reviews": reviews,
                }
                if findings:
                    violations = [
                        {
                            "code": "specialist_review",
                            "message": f["issue"],
                            "quote": f["quote"],
                            "evidence_ids": f.get("evidence_ids", []),
                        }
                        for f in findings
                    ]
                    target = state["specialist_review"]["target"]
                    field = "artifact_validation" if target == "artifact" else "response_validation"
                    values[field] = {
                        **state.get(field, {}),
                        "passed": False,
                        "violations": violations,
                    }
                else:
                    values["outcome"] = "completed"
        return update(state, "join_parallel", **values)

    def after_join_parallel(state: dict[str, Any]) -> str:
        if state.get("outcome") not in (None, "", "completed"):
            return "finalize"
        plan = state["parallel_plan"]
        if plan.get("complete"):
            if plan["kind"] == "review":
                if not state["specialist_review"].get("passed") and not plan.get("arbitrated"):
                    return "arbitrate_parallel"
                if state["specialist_review"].get("passed"):
                    return "finalize"
                return (
                    "continue_action"
                    if state["specialist_review"]["target"] == "artifact"
                    else "revise_response"
                )
            if plan["kind"] == "research":
                if not plan.get("arbitrated") and research_issues(state["research_results"]):
                    return "arbitrate_parallel"
                return (
                    "continue_action"
                    if state.get("task_contract", {}).get("artifact_types")
                    else "generate_response"
                )
            return "continue_action"
        if any(t.get("status", "ready") in ("ready", "retry") for t in plan["tasks"]):
            done = {t["id"] for t in plan["tasks"] if t.get("status") == "completed"}
            if any(
                t.get("status", "ready") in ("ready", "retry") and set(t["depends_on"]) <= done
                for t in plan["tasks"]
            ):
                return "schedule_parallel"
        return "wait_parallel"

    def wait_parallel(state: dict[str, Any]) -> dict[str, Any]:
        plan = copy.deepcopy(state["parallel_plan"])
        waiting = [t for t in plan["tasks"] if t.get("status") == "waiting"]
        if not waiting:
            raise ValueError("parallel plan is deadlocked")
        polls = int(state.get("task_polls", 0))
        if polls >= policy.max_tool_resumes:
            return update(
                state,
                "wait_parallel",
                outcome="tool_timeout",
                response="并行读取等待已达到恢复上限，请在工作台查看任务结果。",
            )
        task_id = waiting[0]["task_id"]
        resolution = interrupt(
            {
                "type": "tool_wait",
                "graph_name": GRAPH_NAME,
                "graph_version": GRAPH_VERSION,
                "run_id": state["run_id"],
                "task_id": task_id,
                "task_ids": [t["task_id"] for t in waiting],
                "resume_attempt": polls + 1,
                "poll_after_ms": policy.tool_poll_interval_ms,
                "poll_max_ms": policy.tool_poll_max_interval_ms,
                "resume_schema": {"type": "tool_poll", "task_id": task_id},
            }
        )
        if (
            not isinstance(resolution, dict)
            or resolution.get("type") not in ("tool_poll", "tool_result")
            or resolution.get("task_id", task_id) != task_id
        ):
            raise ValueError("parallel wait resolution does not match its task")
        for task in waiting:
            task["status"] = "ready"
        usage = dict(state.get("budget_usage", initial_budget_usage()))
        usage["tool_resumes"] = polls + 1
        return update(
            state,
            "wait_parallel",
            parallel_plan=plan,
            task_polls=polls + 1,
            budget_usage=usage,
            recovery=_updated_recovery(
                cast(Any, state), counter="tool_resumes", interrupt_type="tool_wait"
            ),
        )

    return {
        "prepare_parallel": prepare_parallel,
        "schedule_parallel": schedule_parallel,
        "dispatch_parallel": dispatch_parallel,
        "execute_parallel_read": execute_parallel_read,
        "execute_parallel_model": execute_parallel_model,
        "join_parallel": join_parallel,
        "after_join_parallel": after_join_parallel,
        "wait_parallel": wait_parallel,
        "prepare_review": prepare_review,
    }
