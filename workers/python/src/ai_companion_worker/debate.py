"""Checkpointed, evidence-gated debate. Participants never receive write tools.

Isolation is a property of the inputs constructed here, not a claim of statistical
independence. Both first-round results must be committed before arbitration.
"""

from __future__ import annotations

import copy
import os
import re
import time
from dataclasses import asdict
from typing import Any, Callable, Mapping, cast

from langgraph.types import Send, interrupt

from ai_companion_worker.agent_governance import (
    GRAPH_NAME,
    GRAPH_VERSION,
    apply_model_events,
    initial_budget_usage,
)
from ai_companion_worker.arbitration import (
    VERSION,
    apply_arbitration,
    arbitration_packet,
    canonical,
    digest,
    resolution_status,
    unresolved,
    validate_fact,
    validate_verdict,
)
from ai_companion_worker.parallel_tasks import READ_TOOLS

PROMPT_VERSION = "evidence-debate-v1"
FOCUSES = ("grounding", "counterexample")
MAX_CALLS = 6
MAX_READS = 2
MAX_BYTES = 48000


def configured_debate_mode() -> str:
    mode = os.getenv("AGENT_DEBATE_MODE", "off").lower()
    if mode not in {"off", "requested", "on_conflict"}:
        raise ValueError("AGENT_DEBATE_MODE must be off, requested or on_conflict")
    return mode


def debate_requested(state: Mapping[str, Any]) -> bool:
    # Only the supervisor captures operator configuration. In-flight runs and
    # pre-snapshot checkpoints must never adopt a newly enabled global switch.
    mode = state.get("debate_mode", "off")
    if mode not in {"off", "requested", "on_conflict"}:
        return False
    if mode == "off" or state.get("module") != "work" or state.get("debate_session"):
        return False
    # A revised candidate gets its existing final review, never another debate.
    if int(state.get("specialist_review", {}).get("attempts", 0)) > 1:
        return False
    return mode == "on_conflict" or bool(
        re.search(
            r"辩论|交叉核验|独立核验|审校|审阅|仔细核对|严格检查|核查来源|debate|fact.check|peer.review",
            str(state.get("user_message", "")),
            re.I,
        )
    )


def _text(value: Any, maximum: int = 1000) -> bool:
    return isinstance(value, str) and 0 < len(value.strip()) <= maximum


def _quotes(refs: Any, evidence: Mapping[str, str], *, required: bool = False) -> None:
    if not isinstance(refs, list) or len(refs) > 12 or (required and not refs):
        raise ValueError("missing or excessive supporting evidence")
    for ref in refs:
        if (
            not isinstance(ref, dict)
            or set(ref) != {"id", "quote"}
            or not isinstance(ref["id"], str)
            or ref["id"] not in evidence
            or not _text(ref["quote"])
            or ref["quote"] not in evidence[ref["id"]]
        ):
            raise ValueError("ungrounded debate citation")


def evidence_fragments(value: Any) -> list[dict[str, str]]:
    """Extract source text, never query arguments, retrieval timestamps or LLM answers.

    Wiki summaries are derived; only their quotes and raw chunk bodies qualify.
    Unknown adapters can expose a text field but retain unknown source lineage.
    """
    found: list[dict[str, str]] = []

    def visit(item: Any, versions: dict[str, str]) -> None:
        if isinstance(item, list):
            for child in item:
                visit(child, versions)
        elif isinstance(item, dict):
            versions = {
                **versions,
                **{
                    str(s["document_id"]): str(s["version"])
                    for s in item.get("sources", [])
                    if isinstance(s, dict) and s.get("document_id") and s.get("version")
                },
            }
            source = str(item.get("document_id") or item.get("source_id") or "")
            chunk = str(item.get("chunk_id") or "")
            text = item.get("quote") or item.get("text")
            # The unified contract carries previews/full excerpts in content.
            # Derived Wiki bodies and personal assertions do not become raw
            # document evidence: read their original dependencies first.
            ref = item.get("ref")
            unified_version = ""
            if isinstance(ref, dict) and ref.get("kind") in {"document", "builtin"}:
                source = str(ref.get("source_id") or "")
                if ref["kind"] == "builtin" and source:
                    source = "product:" + source
                chunk = str(ref.get("fragment_id") or "")
                text = item.get("content")
                unified_version = str(ref.get("source_revision") or "")
            if item.get("citation_url") and item.get("id") and item.get("source"):
                source, text = "product:" + str(item["id"]), item.get("body")
            if not text and item.get("kind") == "chunk":
                text = item.get("body")
            if isinstance(text, str) and text.strip():
                found.append(
                    {
                        "text": text.strip().removesuffix("…"),
                        "source_id": source,
                        "chunk_id": chunk,
                        "version": str(
                            unified_version
                            or item.get("source_revision")
                            or item.get("source_version")
                            or versions.get(source, "")
                            or (item.get("version", "") if source else "")
                        ),
                        "location": str(item.get("page", item.get("page_start", ""))),
                    }
                )
            for key, child in item.items():
                if key not in {"answer", "response", "summary", "arbitration", "sources"}:
                    if isinstance(child, (dict, list)):
                        visit(child, versions)

    visit(value, {})
    return list({digest(f): f for f in found}.values())


def source_references(metadata: Mapping[str, Any]) -> list[dict[str, str]]:
    refs = [
        {k: f[k] for k in ("source_id", "version", "chunk_id", "text")}
        for entry in metadata.values()
        for f in entry["fragments"]
    ]
    return list({digest(r): r for r in refs}.values())


def copy_evidence_metadata(session: Mapping[str, Any]) -> dict[str, Any]:
    return copy.deepcopy(dict(session.get("evidence_metadata", {})))


def verify_sources(tools: Any, run_id: str, metadata: dict[str, Any], deadline: float) -> bool:
    verifier = getattr(tools, "validate_evidence", None)
    if not callable(verifier):
        return False  # Development adapters are explicitly marked unverified.
    references = source_references(metadata)
    versions = verifier(run_id=run_id, references=references, deadline_at=deadline)
    for entry in metadata.values():
        for fragment in entry["fragments"]:
            current = versions.get(fragment["source_id"])
            if not current or (fragment["version"] and fragment["version"] != current):
                raise ValueError("source version or access changed")
            fragment["version"] = current
    return True


def source_manifest(metadata: Mapping[str, Any]) -> dict[str, Any]:
    return {
        key: [
            {k: f[k] for k in ("source_id", "version", "chunk_id", "location")}
            for f in entry["fragments"]
        ]
        for key, entry in metadata.items()
    }


def _normalized(text: str) -> str:
    return re.sub(r"\W+", "", text.casefold())


def _terms(text: str) -> set[str]:
    words = set(re.findall(r"[a-z0-9]{2,}", text.casefold()))
    for run in re.findall(r"[\u3400-\u9fff]+", text):
        words.update(run[i : i + 2] for i in range(len(run) - 1))
    return words - {"证据", "原文", "核对", "资料", "查询", "来源", "请问"}


def read_request_key(tool_name: Any, arguments: Any) -> str:
    def normalize(value: Any) -> Any:
        if isinstance(value, str):
            return value.strip()
        if isinstance(value, dict):
            return {k: normalize(v) for k, v in value.items()}
        if isinstance(value, list):
            return [normalize(v) for v in value]
        return value

    return digest({"tool_name": tool_name, "arguments": normalize(arguments)})


def novel_fragments(
    old: list[dict[str, str]],
    new: list[dict[str, str]],
    question: str,
) -> list[dict[str, str]]:
    """Conservative lexical prefilter; both reviewers must attest semantic novelty."""
    old_texts = [_normalized(f["text"]) for f in old]
    terms = _terms(question)
    accepted = []
    for fragment in new:
        normalized = _normalized(fragment["text"])
        if not normalized or any(normalized in previous for previous in old_texts):
            continue
        # Combining old excerpts or changing spacing is not new evidence.
        residual = normalized
        for previous in sorted(old_texts, key=len, reverse=True):
            if previous:
                residual = residual.replace(previous, "")
        if len(residual) < 2 or not terms.intersection(_terms(fragment["text"])):
            continue
        accepted.append(fragment)
        old_texts.append(normalized)
    return accepted


def validate_position(raw: Any, job: Mapping[str, Any]) -> dict[str, Any]:
    packet = job["packet"]
    issues = {i["id"]: i for i in packet["issues"]}
    expected = {(i["id"], c["id"]) for i in packet["issues"] for c in i["candidates"]}
    if not isinstance(raw, dict) or not isinstance(raw.get("assessments"), list):
        raise ValueError("debate requires candidate assessments")
    seen: set[tuple[str, str]] = set()
    assessments = []
    for entry in raw["assessments"]:
        if not isinstance(entry, dict):
            raise ValueError("invalid assessment")
        key = (entry.get("issue_id"), entry.get("candidate_id"))
        if any(not isinstance(k, str) for k in key) or key not in expected or key in seen:
            raise ValueError("unavailable or repeated candidate")
        seen.add(key)
        if entry.get("stance") not in {"support", "oppose", "uncertain"} or not _text(
            entry.get("reason")
        ):
            raise ValueError("invalid stance")
        _quotes(entry.get("evidence"), packet["evidence"], required=entry["stance"] != "uncertain")
        assessments.append(
            {k: entry[k] for k in ("issue_id", "candidate_id", "stance", "reason", "evidence")}
        )
        assessments[-1]["id"] = job["id"] + ":" + digest(key)[:16]
    if seen != expected:
        raise ValueError("assessment omitted candidates")
    novelty = raw.get("novelty", [])
    if not isinstance(novelty, list) or len(novelty) > 24:
        raise ValueError("invalid novelty report")
    for item in novelty:
        if (
            not isinstance(item, dict)
            or item.get("issue_id") not in issues
            or item.get("evidence_id") not in packet.get("new_evidence_ids", [])
            or item.get("issue_id")
            not in packet.get("new_evidence_issues", {}).get(item.get("evidence_id"), [])
            or not _text(item.get("reason"))
        ):
            raise ValueError("invalid novelty attribution")
        _quotes([{"id": item["evidence_id"], "quote": item.get("quote")}], packet["evidence"])
        quote = _normalized(item["quote"])
        if any(
            quote in _normalized(text)
            for key, text in packet["evidence"].items()
            if key not in {*packet.get("new_evidence_ids", []), "candidate", "request"}
        ):
            raise ValueError("novelty quote repeats existing evidence")
    new_claims = raw.get("new_claims", [])
    if not isinstance(new_claims, list) or len(new_claims) > 12:
        raise ValueError("too many revised claims")
    if new_claims and (job["round"] != 2 or packet["kind"] != "research"):
        raise ValueError("new claims require research evidence supplementation")
    revisions = []
    for item in new_claims:
        if not isinstance(item, dict) or item.get("issue_id") not in issues:
            raise ValueError("invalid revised claim")
        candidates = {c["id"] for c in issues[item["issue_id"]]["candidates"]}
        if item.get("supersedes_claim_id") not in candidates or not _text(item.get("text"), 1500):
            raise ValueError("revised claim must identify its predecessor")
        _quotes(item.get("evidence"), packet["evidence"], required=True)
        evidence_ids = sorted({e["id"] for e in item["evidence"]})
        if not set(evidence_ids).intersection(packet["new_evidence_ids"]):
            raise ValueError("revised claim lacks new evidence")
        validate_fact(item)
        if not item.get("fact"):
            raise ValueError("revised claim requires explicit fact scope")
        predecessor = next(
            c
            for c in issues[item["issue_id"]]["candidates"]
            if c["id"] == item["supersedes_claim_id"]
        )
        if predecessor.get("fact") and any(
            _normalized(item["fact"][k]) != _normalized(predecessor["fact"][k])
            for k in ("subject", "predicate")
        ):
            raise ValueError("revised claim changed the issue subject or predicate")
        candidate = {k: item[k] for k in ("text", "fact", "supersedes_claim_id")}
        candidate.update(
            evidence_ids=evidence_ids,
            id="claim_" + digest(candidate | {"evidence_ids": evidence_ids})[:24],
        )
        revisions.append({"issue_id": item["issue_id"], "candidate": candidate})
    return {"assessments": assessments, "novelty": copy.deepcopy(novelty), "new_claims": revisions}


def validate_debate_verdict(raw: Any, packet: Mapping[str, Any]) -> list[dict[str, Any]]:
    verdicts = validate_verdict(raw, packet["issues"], packet["evidence"], packet["kind"])
    by_id = {d["issue_id"]: d for d in raw["decisions"]}
    for decision in verdicts:
        if decision["action"] in {"retain", "need_evidence"}:
            continue
        assessments = {
            a["id"]: a
            for p in packet["positions"]
            for a in p["assessments"]
            if a["issue_id"] == decision["issue_id"]
        }
        responses = by_id[decision["issue_id"]].get("assessment_responses")
        if not isinstance(responses, list) or len(responses) != len(assessments):
            raise ValueError("resolved arbitration must address every assessment")
        seen = set()
        for response in responses:
            if (
                not isinstance(response, dict)
                or not isinstance(response.get("id"), str)
                or response["id"] not in assessments
                or response["id"] in seen
                or not _text(response.get("reason"))
            ):
                raise ValueError("invalid assessment response")
            seen.add(response["id"])
            _quotes(response.get("evidence"), packet["evidence"], required=True)
        decision["assessment_responses"] = copy.deepcopy(responses)
    return verdicts


def _usage(usage: Mapping[str, Any]) -> dict[str, int]:
    return {
        "remaining_calls": int(usage["model_calls"]),
        "remaining_prompt_tokens": int(usage["prompt_tokens"]),
        "remaining_completion_tokens": int(usage["completion_tokens"]),
        "remaining_total_tokens": int(usage["prompt_tokens"]) + int(usage["completion_tokens"]),
        "remaining_cost_micros": int(usage["cost_micros"]),
    }


def _call(
    decisions: Any,
    method: str,
    kwargs: dict[str, Any],
    allocation: dict[str, int],
    node: str,
    deadline: float,
    validator: Callable[[Any], Any],
) -> dict[str, Any]:
    from ai_companion_worker.agent_runtime import ModelBudgetExceeded

    started = time.perf_counter_ns()
    result: dict[str, Any] = {"status": "failed", "events": []}
    attempted = False
    try:
        if time.time() >= deadline:
            raise TimeoutError("debate deadline expired")
        attempted = True
        raw = getattr(decisions, method)(
            **copy.deepcopy(kwargs),
            context={
                "model_allowance": dict(allocation),
                "deadline_at": deadline,
            },
        )
        result["value"] = validator(raw)
        result["status"] = "completed"
    except Exception as exc:
        result["error"] = type(exc).__name__
        if isinstance(exc, ModelBudgetExceeded):
            attempted = False
    consumer = getattr(decisions, "consume_observability", None)
    events = consumer() if callable(consumer) else []
    if not events and attempted:
        events = [
            {
                "kind": "model_call",
                "role": "composer",
                "status": "succeeded" if result["status"] == "completed" else "error",
                "latency_ms": (time.perf_counter_ns() - started) // 1_000_000,
            }
        ]
    result["events"] = [{**e, "graph_node": node} for e in events]
    used = _usage(apply_model_events(initial_budget_usage(), node=node, events=events))
    if any(used[k] > allocation[k] for k in used):
        result.update(status="failed", error="debate_budget_overrun")
    if time.time() >= deadline:
        result.update(status="failed", error="deadline_exceeded")
    return result


def make_debate_nodes(
    *,
    decisions: Any,
    tools: Any,
    allowance: Callable[..., Any],
    access_reason: Callable[..., str],
    policy: Any,
) -> dict[str, Callable[..., Any]]:
    from ai_companion_worker.agent_runtime import (
        _observation,
        _skill_run_id,
        _skill_run_status,
        _trace_event,
        _trusted_tool_definition,
        _validate_tool_arguments,
    )

    def update(
        state: Mapping[str, Any], node: str, session: dict[str, Any], **values: Any
    ) -> dict[str, Any]:
        return {
            "debate_session": session,
            "steps": int(state.get("steps", 0)) + 1,
            "node_trace": [
                *state.get("node_trace", []),
                _trace_event(
                    node,
                    session["phase"],
                    details={
                        "debate_id": session["id"],
                        "round": session["round"],
                        "reason": session.get("termination_reason", ""),
                    },
                ),
            ],
            **values,
        }

    def stop(session: dict[str, Any], reason: str) -> None:
        session.update(phase="finish", termination_reason=reason)

    def preflight(state: dict[str, Any], session: dict[str, Any]) -> bool:
        if time.time() >= session["deadline_at"]:
            stop(session, "deadline_exceeded")
        elif (
            state["user_id"] != session["user_id"]
            or digest(state.get("model_manifest", {})) != session["model_hash"]
        ):
            stop(session, "snapshot_changed")
        elif digest(state.get("task_contract", {})) != session["contract_hash"]:
            stop(session, "snapshot_changed")
        elif session["phase"] != "finish":
            try:
                session["source_access_verified"] = verify_sources(
                    tools, state["run_id"], session["evidence_metadata"], session["deadline_at"]
                )
            except Exception:
                stop(session, "evidence_access_changed")
        return bool(session["phase"] != "finish")

    def prepare(state: dict[str, Any]) -> dict[str, Any]:
        packet = arbitration_packet(state, require_original=True)
        # Replace tool envelopes with raw source excerpts: query metadata and model
        # answers cannot acquire primary-evidence status by being tool output.
        metadata: dict[str, Any] = {}
        for key, text in list(packet["evidence"].items()):
            if key in {"request", "candidate"}:
                continue
            observation: dict[str, Any] = next(
                (
                    o
                    for i, o in enumerate(state.get("observations", []))
                    if key in {o.get("parallel_task_id"), f"observation_{i}"}
                ),
                {},
            )
            fragments = evidence_fragments(observation.get("data"))
            packet["evidence"][key] = "\n".join(f["text"] for f in fragments)
            metadata[key] = {
                "fragments": fragments,
                "content_hash": digest(fragments),
                "tool_name": observation.get("tool_name", ""),
                "first_round": 1,
                "access_scope": state["user_id"],
            }
        session = {
            "id": digest({"run": state["run_id"], "packet": packet})[:32],
            "user_id": state["user_id"],
            "kind": packet["kind"],
            "round": 1,
            "scope_id": digest(state["parallel_plan"].get("candidate", "research")),
            "phase": "positions",
            "packet": packet,
            "evidence_metadata": metadata,
            "original_plan": copy.deepcopy(state["parallel_plan"]),
            "deadline_at": time.time() + 60,
            "calls": 0,
            "reads": [],
            "rounds": [],
            "positions": [],
            "independence_reports": [],
            "fingerprints": [],
            "model_hash": digest(state.get("model_manifest", {})),
            "model_manifest": copy.deepcopy(state.get("model_manifest", {})),
            "contract_hash": digest(state.get("task_contract", {})),
            "prompt_version": PROMPT_VERSION,
        }
        remaining = allowance(state, {}, node="debate_position")["model_allowance"]
        # Research needs synthesis; review can need revision plus two rechecks and
        # a final arbitration. Reserve five full shares before admitting debate.
        reserve = 5 if packet["kind"] == "review" else 1
        global_left = int(state["budget_limits"]["max_model_calls"]) - int(
            state["budget_usage"]["model_calls"]
        )
        allocation = {k: int(v) // (MAX_CALLS + reserve) for k, v in remaining.items()}
        allocation["remaining_calls"] = 1
        session["allocation"] = allocation
        session["reserve_calls"] = reserve
        session["tool_catalog"] = [
            copy.deepcopy(t)
            for t in state.get("context", {}).get("tools", [])
            if t.get("name") in READ_TOOLS
        ]
        if not packet["issues"]:
            stop(session, "not_needed")
        elif len(packet["issues"]) > 12 or len(canonical(packet).encode()) > MAX_BYTES:
            stop(session, "input_budget_exceeded")
        elif not callable(getattr(decisions, "debate_position", None)) or not callable(
            getattr(decisions, "arbitrate", None)
        ):
            stop(session, "debate_unavailable")
        elif (
            global_left < 3 + reserve
            or int(remaining["remaining_calls"]) < 2
            or any(v < 1 for v in allocation.values())
        ):
            stop(session, "debate_model_budget")
        elif access_reason(state, "debate_position"):
            stop(session, "model_access_unavailable")
        preflight(state, session)
        session["packet"]["source_manifest"] = source_manifest(session["evidence_metadata"])
        if session["phase"] != "finish" and len(canonical(session["packet"]).encode()) > MAX_BYTES:
            stop(session, "input_budget_exceeded")
        return update(state, "prepare_debate", session)

    def dispatch(state: dict[str, Any]) -> str | list[Send]:
        session = state["debate_session"]
        if session["phase"] == "finish":
            return "finish_debate"
        packet = copy.deepcopy(session["packet"])
        if session["round"] == 2:
            packet["challenges"] = copy.deepcopy(session["positions"])
        return [
            Send(
                "debate_position",
                {
                    "debate_job": {
                        "id": f"{session['id']}:r{session['round']}:{focus}",
                        "generation": session["round"],
                        "round": session["round"],
                        "focus": focus,
                        "packet": copy.deepcopy(packet),
                        "snapshot_hash": digest(packet),
                        "allocation": session["allocation"],
                        "deadline_at": session["deadline_at"],
                        "model_hash": session["model_hash"],
                        "prompt_version": PROMPT_VERSION,
                        "source_metadata": copy.deepcopy(session["evidence_metadata"]),
                        "run_id": state["run_id"],
                    }
                },
            )
            for focus in FOCUSES
        ]

    def position(state: dict[str, Any]) -> dict[str, Any]:
        job = state["debate_job"]
        allowed_keys = {"version", "kind", "issues", "evidence", "task_contract", "source_manifest"}
        if job["round"] == 2:
            allowed_keys |= {"challenges", "new_evidence_ids", "new_evidence_issues"}
        isolated = set(job["packet"]) <= allowed_keys
        result: dict[str, Any]
        if len(canonical(job["packet"]).encode()) > MAX_BYTES:
            result = {"status": "failed", "error": "input_budget_exceeded", "events": []}
        elif not isolated or digest(job["packet"]) != job["snapshot_hash"]:
            result = {"status": "failed", "error": "independence_failed", "events": []}
        else:
            try:
                verify_sources(
                    tools, job["run_id"], copy.deepcopy(job["source_metadata"]), job["deadline_at"]
                )
            except Exception:
                result = {"status": "failed", "error": "evidence_access_changed", "events": []}
            else:
                result = _call(
                    decisions,
                    "debate_position",
                    {
                        "focus": job["focus"],
                        "packet": job["packet"],
                    },
                    job["allocation"],
                    "debate_position",
                    job["deadline_at"],
                    lambda raw: validate_position(raw, job),
                )
        result.update(
            id=job["id"],
            generation=job["generation"],
            focus=job["focus"],
            snapshot_hash=job["snapshot_hash"],
            model_hash=job["model_hash"],
            prompt_version=job["prompt_version"],
            isolated=isolated,
        )
        return {"debate_results": [result]}

    def join_positions(state: dict[str, Any]) -> dict[str, Any]:
        session = copy.deepcopy(state["debate_session"])
        results = {r["id"]: r for r in state.get("debate_results", [])}
        group = [results[f"{session['id']}:r{session['round']}:{f}"] for f in FOCUSES]
        events = [e for r in group for e in r["events"]]
        spent = apply_model_events(initial_budget_usage(), node="debate_position", events=events)
        session["calls"] += spent["model_calls"]
        packet = copy.deepcopy(session["packet"])
        if session["round"] == 2:
            packet["challenges"] = session["positions"]
        expected_hash = digest(packet)
        isolation_ok = all(
            r.get("isolated")
            and r.get("snapshot_hash") == expected_hash
            and r.get("model_hash") == session["model_hash"]
            and r.get("prompt_version") == PROMPT_VERSION
            for r in group
        )
        report = {
            "round": session["round"],
            "context_isolation": "passed" if isolation_ok else "failed",
            "assessment_mode": "independent" if session["round"] == 1 else "cross_review",
            "participants": [
                {k: r[k] for k in ("id", "focus", "snapshot_hash", "model_hash", "prompt_version")}
                for r in group
            ],
            "model_correlation": "same_configuration",
            "statistical_independence": "not_established",
            "sources": sorted(
                {
                    f["source_id"]
                    for m in session["evidence_metadata"].values()
                    for f in m["fragments"]
                    if f["source_id"]
                }
            ),
            "unknown_lineage": any(
                not f["source_id"]
                for m in session["evidence_metadata"].values()
                for f in m["fragments"]
            ),
        }
        session["independence_reports"].append(report)
        if not isolation_ok:
            stop(session, "independence_failed")
        elif any(r["status"] != "completed" for r in group):
            stop(
                session,
                next(
                    r.get("error", "position_failed") for r in group if r["status"] != "completed"
                ),
            )
        else:
            session["positions"] = [r["value"] for r in group]
            session["phase"] = "arbitrate"
            if session["round"] == 2:
                # Each issue needs actual new support, not merely a different
                # retrieval hash or a second model restating the same excerpt.
                reports = [
                    {(n["issue_id"], n["evidence_id"]) for n in r["value"]["novelty"]}
                    for r in group
                ]
                agreed = reports[0] & reports[1]
                session["novel_issue_ids"] = sorted({i for i, _ in agreed})
                if not agreed:
                    stop(session, "no_new_evidence")
                for r in group:
                    for revision in r["value"]["new_claims"]:
                        if revision["issue_id"] not in session["novel_issue_ids"]:
                            continue
                        issue = next(
                            i
                            for i in session["packet"]["issues"]
                            if i["id"] == revision["issue_id"]
                        )
                        if revision["candidate"]["id"] not in {
                            c["id"] for c in issue["candidates"]
                        }:
                            issue["candidates"].append(revision["candidate"])
        preflight(state, session)
        return update(
            state,
            "join_debate_positions",
            session,
            budget_usage=apply_model_events(
                state["budget_usage"], node="debate_position", events=events
            ),
            model_events=[*state.get("model_events", []), *events],
        )

    def arbitrate(state: dict[str, Any]) -> dict[str, Any]:
        session = copy.deepcopy(state["debate_session"])
        if not preflight(state, session):
            return update(state, "arbitrate_debate", session)
        packet = {
            **session["packet"],
            "positions": session["positions"],
            "independence": session["independence_reports"][-1],
            "available_read_tools": session["tool_catalog"],
        }
        remaining = allowance(state, {}, node="arbitrate_debate")["model_allowance"]
        allocation = {k: min(v, int(remaining[k])) for k, v in session["allocation"].items()}
        if (
            session["calls"] >= MAX_CALLS
            or any(v < 1 for v in allocation.values())
            or access_reason(state, "arbitrate_debate")
        ):
            stop(session, "debate_model_budget")
            return update(state, "arbitrate_debate", session)
        if len(canonical(packet).encode()) > MAX_BYTES:
            stop(session, "input_budget_exceeded")
            return update(state, "arbitrate_debate", session)
        result = _call(
            decisions,
            "arbitrate",
            {"kind": session["kind"], "packet": packet},
            allocation,
            "arbitrate_debate",
            session["deadline_at"],
            lambda raw: validate_debate_verdict(raw, packet),
        )
        events = result["events"]
        session["calls"] += apply_model_events(
            initial_budget_usage(), node="arbitrate_debate", events=events
        )["model_calls"]
        verdicts = (
            result.get("value", [])
            if result["status"] == "completed"
            else unresolved(packet["issues"], result.get("error", "invalid_verdict"))
        )
        if session["round"] == 2:
            # An unrelated new snippet cannot unlock another disputed fact.
            previous = {d["issue_id"]: d for d in session["rounds"][-1]["decisions"]}
            verdicts = [
                d
                if d["issue_id"] in session["novel_issue_ids"]
                and previous[d["issue_id"]]["action"] in {"retain", "need_evidence"}
                else previous[d["issue_id"]]
                for d in verdicts
            ]
        status = resolution_status(verdicts)
        record = {
            "version": VERSION,
            "input_hash": digest(
                {
                    "packet": packet,
                    "model": session["model_hash"],
                    "user": session["user_id"],
                    "prompt": PROMPT_VERSION,
                }
            ),
            "scope_id": session["scope_id"],
            "kind": session["kind"],
            "issues": copy.deepcopy(packet["issues"]),
            "decisions": verdicts,
            "status": "resolved" if status == "resolved" else "retained",
            "call_status": "succeeded" if result["status"] == "completed" else "failed",
            "resolution_status": status,
            "reason": result.get("error", ""),
            "debate_id": session["id"],
            "round": session["round"],
        }
        session["rounds"].append(record)
        fingerprint = digest(
            {
                "decisions": [
                    {k: d[k] for k in ("issue_id", "action", "selected_ids")} for d in verdicts
                ],
                "evidence": session["evidence_metadata"],
            }
        )
        if result["status"] != "completed":
            stop(session, result.get("error", "invalid_verdict"))
        elif status == "resolved":
            stop(session, "resolved")
        elif fingerprint in session["fingerprints"]:
            stop(session, "repeated_state")
        elif session["round"] >= 2:
            stop(session, "round_limit")
        elif not any(d.get("evidence_requests") for d in verdicts):
            stop(session, "evidence_unavailable")
        else:
            session["phase"] = "prepare_reads"
        session["fingerprints"].append(fingerprint)
        return update(
            state,
            "arbitrate_debate",
            session,
            arbitrations=[*state.get("arbitrations", []), record],
            budget_usage=apply_model_events(
                state["budget_usage"], node="arbitrate_debate", events=events
            ),
            model_events=[*state.get("model_events", []), *events],
        )

    def prepare_reads(state: dict[str, Any]) -> dict[str, Any]:
        session = copy.deepcopy(state["debate_session"])
        if not preflight(state, session):
            return update(state, "prepare_debate_evidence", session)
        remaining = allowance(state, {}, node="debate_position")["model_allowance"]
        global_left = int(state["budget_limits"]["max_model_calls"]) - int(
            state["budget_usage"]["model_calls"]
        )
        if (
            session["calls"] + 3 > MAX_CALLS
            or global_left < 3 + session["reserve_calls"]
            or int(remaining["remaining_calls"]) < 2
            or any(
                int(v) < 3 * session["allocation"][k]
                for k, v in remaining.items()
                if k != "remaining_calls"
            )
        ):
            stop(session, "debate_model_budget")
            return update(state, "prepare_debate_evidence", session)
        used = {
            read_request_key(o.get("tool_name"), o.get("arguments"))
            for o in state.get("observations", [])
        }
        selected: dict[str, Any] = {}
        for decision in session["rounds"][-1]["decisions"]:
            for request in decision.get("evidence_requests", []):
                key = read_request_key(request["tool_name"], request["arguments"])
                if key in used:
                    continue
                try:
                    definition = _trusted_tool_definition(cast(Any, state), request["tool_name"])
                    if request["tool_name"] not in READ_TOOLS or definition is None:
                        raise ValueError("untrusted evidence tool")
                    _validate_tool_arguments(request["arguments"], definition)
                except ValueError:
                    session.setdefault("rejected_requests", []).append(
                        {"issue_id": decision["issue_id"], "reason": "invalid_read_request"}
                    )
                    continue
                if key in selected:
                    selected[key]["issue_ids"].append(decision["issue_id"])
                else:
                    selected[key] = {
                        **copy.deepcopy(request),
                        "id": f"{session['id']}:read:{key[:24]}",
                        "issue_ids": [decision["issue_id"]],
                        "status": "ready",
                    }
        available = min(
            MAX_READS,
            max(
                0,
                int(state.get("action_budget", policy.max_actions))
                - int(state.get("action_index", 0)),
            ),
        )
        session["omitted_requests"] = max(0, len(selected) - available)
        session["reads"] = list(selected.values())[:available]
        if not session["reads"]:
            stop(session, "no_new_evidence" if not selected else "action_budget_exhausted")
        else:
            session["phase"] = "reads"
        action = int(state.get("action_index", 0))
        for task in session["reads"]:
            action += 1
            task["action"] = action
        return update(
            state,
            "prepare_debate_evidence",
            session,
            action_index=action,
            budget_usage={**state["budget_usage"], "actions": action},
        )

    def dispatch_reads(state: dict[str, Any]) -> str | list[Send]:
        session = state["debate_session"]
        if session["phase"] == "finish":
            return "finish_debate"
        return [
            Send(
                "read_debate_evidence",
                {
                    "debate_job": {
                        "task": t,
                        "id": t["id"],
                        "generation": t.get("polls", 0),
                        "run_id": state["run_id"],
                        "user_id": state["user_id"],
                        "module": state["module"],
                        "deadline_at": session["deadline_at"],
                    }
                },
            )
            for t in session["reads"]
            if t["status"] in {"ready", "waiting"}
        ]

    def read(state: dict[str, Any]) -> dict[str, Any]:
        job, task = state["debate_job"], state["debate_job"]["task"]
        result: dict[str, Any] = {
            "id": task["id"],
            "generation": job["generation"],
            "status": "failed",
        }
        try:
            if time.time() >= job["deadline_at"]:
                raise TimeoutError("debate deadline expired")
            kwargs = {"run_id": job["run_id"], "user_id": job["user_id"], "module": job["module"]}
            if task.get("task_id"):
                kwargs["task_id"] = task["task_id"]
            else:
                kwargs.update(
                    tool_name=task["tool_name"],
                    arguments=task["arguments"],
                    idempotency_key=task["id"],
                )
            bounded = getattr(tools, "read_evidence", None)
            if callable(bounded):
                outcome = bounded(**kwargs, deadline_at=job["deadline_at"])
            else:
                outcome = (tools.observe if task.get("task_id") else tools.prepare)(**kwargs)
            if not task.get("task_id") and (
                outcome.status != "completed" or outcome.tool_name != task["tool_name"]
            ):
                raise ValueError("evidence read cannot request confirmation or another tool")
            source = asdict(outcome)
            status = _skill_run_status(outcome.data)
            if status in {"queued", "running"}:
                task_id = _skill_run_id(outcome.data)
                if not task_id:
                    raise ValueError("missing asynchronous evidence task")
                result.update(status="waiting", task_id=task_id)
            elif status in {"failed", "cancelled", "waiting_confirmation"}:
                raise ValueError("evidence task did not complete")
            else:
                result.update(status="completed", source=source)
            if time.time() >= job["deadline_at"]:
                result.update(status="failed", error="deadline_exceeded")
        except Exception as exc:
            result.update(status="failed", error=type(exc).__name__)
        return {"debate_results": [result]}

    def join_reads(state: dict[str, Any]) -> dict[str, Any]:
        session = copy.deepcopy(state["debate_session"])
        results = {r["id"]: r for r in state.get("debate_results", [])}
        for task in session["reads"]:
            result = results[task["id"]]
            task["status"] = result["status"]
            if result.get("task_id"):
                task["task_id"] = result["task_id"]
        if not preflight(state, session):
            return update(state, "join_debate_evidence", session)
        if any(t["status"] == "failed" for t in session["reads"]):
            stop(session, "evidence_read_failed")
            return update(state, "join_debate_evidence", session)
        if any(t["status"] == "waiting" for t in session["reads"]):
            session["phase"] = "wait"
            return update(state, "join_debate_evidence", session)
        old = [f for m in session["evidence_metadata"].values() for f in m["fragments"]]
        old_versions = {
            f["source_id"]: f["version"] for f in old if f["source_id"] and f["version"]
        }
        observations = list(state.get("observations", []))
        new_ids = []
        for task in session["reads"]:
            source = results[task["id"]]["source"]
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
                source,
            )
            observation["parallel_task_id"] = task["id"]
            fragments = evidence_fragments(source.get("data"))
            if any(
                f["source_id"] in old_versions
                and f["version"]
                and old_versions[f["source_id"]] != f["version"]
                for f in fragments
            ):
                stop(session, "source_version_changed")
                return update(state, "join_debate_evidence", session)
            relevant = (
                task["question"]
                + " "
                + " ".join(
                    i["label"] for i in session["packet"]["issues"] if i["id"] in task["issue_ids"]
                )
            )
            novel = novel_fragments(old, fragments, relevant)
            # Persist the full tool result for audit, but expose only raw excerpts
            # to the debate; model summaries never pass the novelty gate.
            observations.append(observation)
            if novel:
                key = task["id"]
                session["packet"]["evidence"][key] = "\n".join(f["text"] for f in novel)
                session["evidence_metadata"][key] = {
                    "fragments": novel,
                    "content_hash": digest(novel),
                    "first_round": 2,
                    "tool_name": task["tool_name"],
                    "access_scope": session["user_id"],
                    "issue_ids": task["issue_ids"],
                }
                new_ids.append(key)
                session["packet"].setdefault("new_evidence_issues", {})[key] = task["issue_ids"]
                for issue in session["packet"]["issues"]:
                    if issue["id"] in task["issue_ids"]:
                        issue.setdefault("evidence_ids", []).append(key)
                old.extend(novel)
        session["packet"]["new_evidence_ids"] = new_ids
        if not new_ids:
            stop(session, "no_new_evidence")
        elif len(canonical(session["packet"]).encode()) > MAX_BYTES:
            stop(session, "input_budget_exceeded")
        else:
            session.update(round=2, phase="positions")
        preflight(state, session)
        session["packet"]["source_manifest"] = source_manifest(session["evidence_metadata"])
        return update(state, "join_debate_evidence", session, observations=observations)

    def wait(state: dict[str, Any]) -> dict[str, Any]:
        session = copy.deepcopy(state["debate_session"])
        if not preflight(state, session):
            return update(state, "wait_debate_evidence", session)
        polls = int(state.get("task_polls", 0))
        if polls >= policy.max_tool_resumes:
            stop(session, "tool_resume_limit")
            return update(state, "wait_debate_evidence", session)
        waiting = [t for t in session["reads"] if t["status"] == "waiting"]
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
            or resolution.get("type") not in {"tool_poll", "tool_result"}
            or resolution.get("task_id", task_id) != task_id
        ):
            raise ValueError("evidence wait resolution does not match its task")
        for task in waiting:
            task["polls"] = task.get("polls", 0) + 1
        session["phase"] = "reads"
        preflight(state, session)
        return update(
            state,
            "wait_debate_evidence",
            session,
            task_polls=polls + 1,
            budget_usage={**state["budget_usage"], "tool_resumes": polls + 1},
        )

    def finish(state: dict[str, Any]) -> dict[str, Any]:
        session = copy.deepcopy(state["debate_session"])
        try:
            verify_sources(
                tools,
                state["run_id"],
                session["evidence_metadata"],
                max(time.time() + 0.01, session["deadline_at"]),
            )
        except Exception:
            stop(session, "evidence_access_changed")
        packet = session["packet"]
        verdicts = (
            session["rounds"][-1]["decisions"]
            if session["rounds"]
            else unresolved(packet["issues"], session["termination_reason"])
        )
        invalid_sources = session["termination_reason"] in {
            "snapshot_changed",
            "source_version_changed",
            "evidence_access_changed",
        }
        if invalid_sources or session["termination_reason"] == "independence_failed":
            verdicts = unresolved(packet["issues"], session["termination_reason"])
        record = {
            "version": VERSION,
            "input_hash": digest(
                {
                    "session": session["id"],
                    "rounds": session["rounds"],
                    "reason": session["termination_reason"],
                }
            ),
            "kind": session["kind"],
            "scope_id": session["scope_id"],
            "issues": packet["issues"],
            "decisions": verdicts,
            "resolution_status": resolution_status(verdicts),
            "termination_reason": session["termination_reason"],
            "call_status": "skipped",
            "status": "resolved" if resolution_status(verdicts) == "resolved" else "retained",
            "reason": session["termination_reason"],
            "debate_id": session["id"],
            "round": session["round"],
        }
        session.update(phase="completed", resolution_status=record["resolution_status"])
        values = apply_arbitration(state, record)
        if invalid_sources:
            values.update(
                outcome="arbitration_unresolved",
                response="来源或权限已变化，已停止使用旧证据，请重新发起核对。",
                research_results=[],
            )
            record["issues"] = [{**i, "label": "来源已失效"} for i in record["issues"]]
        # An unresolved review must not be laundered by a rewrite that removes
        # the disputed wording. Keep the normal one-revision path for resolved
        # findings, and fail closed for missing evidence or failed independence.
        if session["kind"] == "review" and record["resolution_status"] != "resolved":
            values.update(
                outcome="specialist_review_failed",
                response="内容审校仍缺少充分依据，已保留分歧并停止交付。",
            )
        return update(
            state,
            "finish_debate",
            session,
            **values,
            arbitrations=[*state.get("arbitrations", []), record],
        )

    def route(state: dict[str, Any]) -> str:
        return {
            "finish": "finish_debate",
            "arbitrate": "arbitrate_debate",
            "prepare_reads": "prepare_debate_evidence",
            "wait": "wait_debate_evidence",
        }[state["debate_session"]["phase"]]

    return {
        "prepare_debate": prepare,
        "dispatch_debate": dispatch,
        "debate_position": position,
        "join_debate_positions": join_positions,
        "arbitrate_debate": arbitrate,
        "prepare_debate_evidence": prepare_reads,
        "dispatch_debate_evidence": dispatch_reads,
        "read_debate_evidence": read,
        "join_debate_evidence": join_reads,
        "wait_debate_evidence": wait,
        "finish_debate": finish,
        "route_debate": route,
    }
