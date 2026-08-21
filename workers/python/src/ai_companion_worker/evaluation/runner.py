from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path
from typing import Any, Mapping, Sequence, cast
from xml.etree import ElementTree

from langgraph.checkpoint.memory import InMemorySaver

from ai_companion_worker.agent_governance import (
    DEFAULT_MODEL_CONFIG_VERSION,
    GRAPH_NAME,
    GRAPH_VERSION,
)
from ai_companion_worker.agent_runtime import (
    AgentAssessment,
    AgentPlan,
    AgentRuntime,
    ModelDecision,
    ModuleKey,
    RepairDecision,
    ToolOutcome,
    ToolPreparation,
    build_graph,
    interrupt_payloads,
)

CASE_SCHEMA_VERSION = "agent-eval-case-v1"
REPORT_SCHEMA_VERSION = "agent-eval-report-v1"


class EvalCaseError(ValueError):
    pass


class ScriptedDecisions:
    def __init__(self, tape: Mapping[str, Any]) -> None:
        self._queues = {
            key: [dict(item) if isinstance(item, Mapping) else item for item in value]
            for key, value in tape.items()
            if isinstance(value, list)
        }

    def model_manifest(self) -> dict[str, Any]:
        return {
            "provider": "agent-eval",
            "config_version": DEFAULT_MODEL_CONFIG_VERSION,
            "pinned": True,
            "roles": {},
        }

    def plan(
        self,
        *,
        module: ModuleKey,
        message: str,
        context: Mapping[str, Any],
    ) -> AgentPlan:
        del module, context
        value = self._pop(
            "plans",
            {
                "objective": message,
                "steps": ["执行必要工具", "观察结果", "交付结果"],
                "success_criteria": "返回可核验的真实结果。",
            },
        )
        if not isinstance(value, Mapping):
            raise EvalCaseError("scripted plan must be an object")
        steps = value.get("steps", [])
        return AgentPlan(
            objective=str(value.get("objective") or ""),
            steps=tuple(str(item) for item in steps if isinstance(item, str)),
            success_criteria=str(value.get("success_criteria") or ""),
        )

    def decide(
        self,
        *,
        module: ModuleKey,
        message: str,
        context: Mapping[str, Any],
    ) -> ModelDecision:
        del module, message, context
        value = self._pop("decisions")
        if not isinstance(value, Mapping):
            raise EvalCaseError("scripted decision must be an object")
        arguments = value.get("tool_arguments", {})
        if not isinstance(arguments, Mapping):
            raise EvalCaseError("scripted tool_arguments must be an object")
        return ModelDecision(
            intent=str(value.get("intent") or ""),
            response=str(value.get("response") or ""),
            tool_name=str(value.get("tool_name") or ""),
            tool_arguments=dict(arguments),
            requires_argument_composition=(value.get("requires_argument_composition") is True),
            needs_response=value.get("needs_response") is True,
        )

    def compose_arguments(
        self,
        *,
        module: ModuleKey,
        message: str,
        tool_name: str,
        context: Mapping[str, Any],
    ) -> Mapping[str, Any]:
        del module, message, tool_name, context
        value = self._pop("compositions")
        if not isinstance(value, Mapping):
            raise EvalCaseError("scripted composition must be an object")
        return dict(value)

    def respond(
        self,
        *,
        module: ModuleKey,
        message: str,
        context: Mapping[str, Any],
    ) -> str:
        del module, message, context
        return str(self._pop("responses"))

    def revise_response(
        self,
        *,
        module: ModuleKey,
        message: str,
        response: str,
        violations: list[Mapping[str, Any]],
        context: Mapping[str, Any],
    ) -> str:
        del module, message, response, violations, context
        return str(self._pop("revisions"))

    def assess(
        self,
        *,
        module: ModuleKey,
        message: str,
        context: Mapping[str, Any],
    ) -> AgentAssessment:
        del module, message, context
        value = self._pop(
            "assessments",
            {"status": "completed", "reason": "评测脚本确认目标已完成"},
        )
        if not isinstance(value, Mapping):
            raise EvalCaseError("scripted assessment must be an object")
        return AgentAssessment(
            status=cast(Any, str(value.get("status") or "")),
            reason=str(value.get("reason") or ""),
        )

    def repair(
        self,
        *,
        module: ModuleKey,
        message: str,
        tool_name: str,
        arguments: Mapping[str, Any],
        failure: Mapping[str, Any],
        context: Mapping[str, Any],
    ) -> RepairDecision:
        del module, message, tool_name, arguments, failure, context
        return RepairDecision(strategy="abort", reason_code="eval_default_abort")

    def _pop(self, key: str, default: Any = None) -> Any:
        queue = self._queues.get(key, [])
        if queue:
            return queue.pop(0)
        if default is not None:
            return default
        raise EvalCaseError(f"scripted tape exhausted: {key}")


class ScriptedTools:
    def __init__(self, tape: Mapping[str, Any]) -> None:
        self.prepared: list[dict[str, Any]] = []
        self.committed: list[dict[str, Any]] = []
        self._preparations = _mapping_queue(tape.get("preparations"))
        self._commits = _mapping_queue(tape.get("commits"))
        self._observations = _mapping_queue(tape.get("observations"))
        self._retries = _mapping_queue(tape.get("retries"))

    def prepare(self, **values: Any) -> ToolPreparation:
        self.prepared.append(dict(values))
        item = self._pop(
            self._preparations,
            {
                "status": "completed",
                "tool_name": values.get("tool_name", ""),
                "response": "工具执行完成。",
            },
        )
        return ToolPreparation(
            status=cast(Any, item.get("status", "completed")),
            tool_name=str(item.get("tool_name") or values.get("tool_name") or ""),
            response=str(item.get("response") or ""),
            risk_level=cast(Any, item.get("risk_level", "none")),
            summary=str(item.get("summary") or ""),
            confirmation_token=str(item.get("confirmation_token") or ""),
            normalized_arguments=_mapping(item.get("normalized_arguments")),
            data=item.get("data", {}),
        )

    def commit(self, **values: Any) -> ToolOutcome:
        self.committed.append(dict(values))
        item = self._pop(
            self._commits,
            {"response": "操作已确认并完成。", "data": {}},
        )
        return ToolOutcome(response=str(item.get("response") or ""), data=item.get("data", {}))

    def observe(self, **_values: Any) -> ToolOutcome:
        item = self._pop(self._observations)
        return ToolOutcome(response=str(item.get("response") or ""), data=item.get("data", {}))

    def retry_task(self, **_values: Any) -> ToolOutcome:
        item = self._pop(self._retries)
        return ToolOutcome(response=str(item.get("response") or ""), data=item.get("data", {}))

    @staticmethod
    def _pop(
        queue: list[dict[str, Any]], default: Mapping[str, Any] | None = None
    ) -> dict[str, Any]:
        if queue:
            return queue.pop(0)
        if default is not None:
            return dict(default)
        raise EvalCaseError("scripted tool tape exhausted")


def load_cases(path: Path) -> list[dict[str, Any]]:
    cases: list[dict[str, Any]] = []
    for line_number, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
        if not line.strip():
            continue
        try:
            value = json.loads(line)
        except json.JSONDecodeError as exc:
            raise EvalCaseError(f"{path}:{line_number}: invalid JSON") from exc
        if not isinstance(value, dict):
            raise EvalCaseError(f"{path}:{line_number}: case must be an object")
        validate_case(value, source=f"{path}:{line_number}")
        cases.append(value)
    if not cases:
        raise EvalCaseError(f"{path}: suite must contain at least one case")
    return cases


def validate_case(case: Mapping[str, Any], *, source: str = "case") -> None:
    if case.get("schema_version") != CASE_SCHEMA_VERSION:
        raise EvalCaseError(f"{source}: unsupported schema_version")
    if not isinstance(case.get("id"), str) or not str(case["id"]).strip():
        raise EvalCaseError(f"{source}: id is required")
    if not isinstance(case.get("given"), Mapping):
        raise EvalCaseError(f"{source}: given must be an object")
    if not isinstance(case.get("expect"), Mapping):
        raise EvalCaseError(f"{source}: expect must be an object")
    if not isinstance(case.get("tape"), Mapping):
        raise EvalCaseError(f"{source}: tape must be an object")
    given = cast(Mapping[str, Any], case["given"])
    if given.get("module") not in ("companion", "life", "work"):
        raise EvalCaseError(f"{source}: invalid module")
    if not isinstance(given.get("user_message"), str) or not str(given["user_message"]).strip():
        raise EvalCaseError(f"{source}: user_message is required")


def run_case(case: Mapping[str, Any]) -> dict[str, Any]:
    case_id = str(case["id"])
    given = cast(Mapping[str, Any], case["given"])
    expect = cast(Mapping[str, Any], case["expect"])
    tape = cast(Mapping[str, Any], case["tape"])
    context = _mapping(given.get("context"))
    tools_value = given.get("tools", [])
    if not isinstance(tools_value, list) or not all(
        isinstance(item, Mapping) for item in tools_value
    ):
        raise EvalCaseError(f"{case_id}: given.tools must be an array of objects")
    context["tools"] = [dict(item) for item in tools_value]
    decisions = ScriptedDecisions(tape)
    tools = ScriptedTools(tape)
    runtime = AgentRuntime(
        build_graph(
            checkpointer=InMemorySaver(),
            decisions=decisions,
            tools=tools,
        )
    )
    result = runtime.start(
        {
            "run_id": f"eval-{case_id}",
            "user_id": "eval-user",
            "conversation_id": "eval-conversation",
            "character_id": "eval-character",
            "module": cast(ModuleKey, given["module"]),
            "user_message": str(given["user_message"]).strip(),
            "context": context,
        }
    )
    interrupts = interrupt_payloads(result)
    status = "completed"
    if interrupts:
        interrupt_types = {str(item.get("type") or "") for item in interrupts}
        if interrupt_types == {"tool_approval"}:
            status = "waiting_approval"
        elif interrupt_types == {"tool_wait"}:
            status = "waiting_tool"
        else:
            status = "unsupported_interrupt"
    actual_path = [
        str(item.get("node") or "")
        for item in result.get("node_trace", [])
        if isinstance(item, Mapping)
    ]
    actual = {
        "status": status,
        "outcome": str(result.get("outcome") or status),
        "execution_mode": str(result.get("execution_mode") or ""),
        "response": str(result.get("response") or ""),
        "quality_pass": (
            result.get("response_validation", {}).get("passed")
            if isinstance(result.get("response_validation"), Mapping)
            else None
        ),
        "actual_path": actual_path,
        "tool_sequence": [str(item.get("tool_name") or "") for item in tools.prepared],
        "model_calls": int(result.get("budget_usage", {}).get("model_calls", 0)),
        "steps": int(result.get("steps", 0)),
        "repair_count": len(result.get("repair_history", [])),
        "graph_version": str(result.get("graph_version") or ""),
        "model_config_version": str(result.get("model_manifest", {}).get("config_version") or ""),
        "tool_catalog_fingerprint": str(result.get("tool_catalog_fingerprint") or ""),
    }
    violations = score_case(expect, actual)
    return {
        "id": case_id,
        "passed": not violations,
        "actual": actual,
        "violations": violations,
    }


def score_case(expect: Mapping[str, Any], actual: Mapping[str, Any]) -> list[dict[str, Any]]:
    violations: list[dict[str, Any]] = []

    def exact(field: str) -> None:
        if field in expect and actual.get(field) != expect.get(field):
            violations.append(
                _violation(field, expect.get(field), actual.get(field), "exact_mismatch")
            )

    for field in ("status", "execution_mode", "tool_sequence", "quality_pass"):
        exact(field)
    if "outcome" in expect:
        allowed = expect["outcome"]
        allowed_values = allowed if isinstance(allowed, list) else [allowed]
        if actual.get("outcome") not in allowed_values:
            violations.append(
                _violation("outcome", allowed_values, actual.get("outcome"), "unexpected_outcome")
            )
    path = actual.get("actual_path", [])
    path = path if isinstance(path, list) else []
    for node in _string_list(expect.get("required_nodes")):
        if node not in path:
            violations.append(_violation("actual_path", node, path, "missing_node"))
    for node in _string_list(expect.get("forbidden_nodes")):
        if node in path:
            violations.append(_violation("actual_path", node, path, "forbidden_node"))
    for field in ("max_model_calls", "max_steps", "max_retries"):
        if field not in expect:
            continue
        actual_field = {
            "max_model_calls": "model_calls",
            "max_steps": "steps",
            "max_retries": "repair_count",
        }[field]
        limit = expect[field]
        value = actual.get(actual_field)
        if not isinstance(limit, int) or not isinstance(value, int) or value > limit:
            violations.append(_violation(actual_field, f"<= {limit}", value, "budget_exceeded"))
    response = str(actual.get("response") or "")
    for value in _string_list(expect.get("response_contains")):
        if value not in response:
            violations.append(_violation("response", value, response, "missing_content"))
    for value in _string_list(expect.get("response_not_contains")):
        if value in response:
            violations.append(_violation("response", value, response, "forbidden_content"))
    return violations


def build_report(mode: str, results: Sequence[Mapping[str, Any]]) -> dict[str, Any]:
    model_calls = [int(item["actual"]["model_calls"]) for item in results]
    steps = [int(item["actual"]["steps"]) for item in results]
    failed = [str(item["id"]) for item in results if item.get("passed") is not True]
    return {
        "schema_version": REPORT_SCHEMA_VERSION,
        "mode": mode,
        "source": {
            "graph_name": GRAPH_NAME,
            "graph_version": GRAPH_VERSION,
            "model_config_version": DEFAULT_MODEL_CONFIG_VERSION,
        },
        "summary": {
            "cases": len(results),
            "passed": len(results) - len(failed),
            "hard_pass_rate": round((len(results) - len(failed)) / len(results), 6),
            "model_calls_p50": _percentile(model_calls, 0.50),
            "model_calls_p95": _percentile(model_calls, 0.95),
            "steps_p95": _percentile(steps, 0.95),
        },
        "regression": {"new_failures": failed},
        "cases": list(results),
    }


def apply_baseline(
    report: dict[str, Any],
    baseline: Mapping[str, Any],
) -> list[dict[str, Any]]:
    violations: list[dict[str, Any]] = []
    if baseline.get("schema_version") != "agent-eval-baseline-v1":
        raise EvalCaseError("unsupported Agent eval baseline schema_version")
    summary = report.get("summary", {})
    if not isinstance(summary, Mapping):
        raise EvalCaseError("Agent eval report summary is invalid")
    expected_graph = baseline.get("graph_version")
    source = report.get("source", {})
    actual_graph = source.get("graph_version") if isinstance(source, Mapping) else None
    if isinstance(expected_graph, str) and actual_graph != expected_graph:
        violations.append(
            _violation(
                "source/graph_version",
                expected_graph,
                actual_graph,
                "baseline_identity_mismatch",
            )
        )
    minimum_cases = baseline.get("minimum_cases", 0)
    if not isinstance(minimum_cases, int) or int(summary.get("cases", 0)) < minimum_cases:
        violations.append(
            _violation(
                "summary/cases", f">= {minimum_cases}", summary.get("cases"), "minimum_cases"
            )
        )
    for field, threshold in _mapping(baseline.get("minimum")).items():
        actual = summary.get(field)
        if (
            not isinstance(threshold, (int, float))
            or not isinstance(actual, (int, float))
            or actual < threshold
        ):
            violations.append(
                _violation(f"summary/{field}", f">= {threshold}", actual, "baseline_regression")
            )
    for field, threshold in _mapping(baseline.get("maximum")).items():
        actual = summary.get(field)
        if (
            not isinstance(threshold, (int, float))
            or not isinstance(actual, (int, float))
            or actual > threshold
        ):
            violations.append(
                _violation(f"summary/{field}", f"<= {threshold}", actual, "baseline_regression")
            )
    report["regression"] = {
        **_mapping(report.get("regression")),
        "baseline_version": str(baseline.get("version") or ""),
        "violations": violations,
    }
    return violations


def write_junit(path: Path, report: Mapping[str, Any]) -> None:
    cases = report.get("cases", [])
    cases = cases if isinstance(cases, list) else []
    failures = sum(1 for item in cases if isinstance(item, Mapping) and not item.get("passed"))
    suite = ElementTree.Element(
        "testsuite",
        name="agent-eval",
        tests=str(len(cases)),
        failures=str(failures),
    )
    for item in cases:
        if not isinstance(item, Mapping):
            continue
        case = ElementTree.SubElement(suite, "testcase", name=str(item.get("id") or ""))
        if item.get("passed") is not True:
            failure = ElementTree.SubElement(case, "failure", message="contract violation")
            failure.text = json.dumps(item.get("violations", []), ensure_ascii=False)
    path.parent.mkdir(parents=True, exist_ok=True)
    ElementTree.ElementTree(suite).write(path, encoding="utf-8", xml_declaration=True)


def main(argv: Sequence[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Run versioned Agent contract evaluations")
    parser.add_argument("--mode", choices=("contract", "replay"), default="contract")
    parser.add_argument("--suite", type=Path, required=True)
    parser.add_argument("--baseline", type=Path)
    parser.add_argument("--json-report", type=Path)
    parser.add_argument("--junit-report", type=Path)
    args = parser.parse_args(argv)
    try:
        cases = load_cases(args.suite)
        results = [run_case(case) for case in cases]
        baseline = json.loads(args.baseline.read_text(encoding="utf-8")) if args.baseline else None
        if baseline is not None and not isinstance(baseline, dict):
            raise EvalCaseError("Agent eval baseline must be an object")
    except (EvalCaseError, OSError, ValueError) as exc:
        print(f"agent eval error: {exc}", file=sys.stderr)
        return 2
    report = build_report(args.mode, results)
    baseline_violations = apply_baseline(report, baseline) if baseline else []
    encoded = json.dumps(report, ensure_ascii=False, indent=2)
    if args.json_report:
        args.json_report.parent.mkdir(parents=True, exist_ok=True)
        args.json_report.write_text(encoded + "\n", encoding="utf-8")
    if args.junit_report:
        write_junit(args.junit_report, report)
    print(encoded)
    return (
        0
        if report["summary"]["passed"] == report["summary"]["cases"] and not baseline_violations
        else 1
    )


def _mapping(value: Any) -> dict[str, Any]:
    return dict(value) if isinstance(value, Mapping) else {}


def _mapping_queue(value: Any) -> list[dict[str, Any]]:
    if not isinstance(value, list):
        return []
    return [dict(item) for item in value if isinstance(item, Mapping)]


def _string_list(value: Any) -> list[str]:
    return [str(item) for item in value] if isinstance(value, list) else []


def _violation(path: str, expected: Any, actual: Any, code: str) -> dict[str, Any]:
    return {
        "code": code,
        "severity": "hard",
        "path": "/" + path,
        "expected": expected,
        "actual": actual,
    }


def _percentile(values: Sequence[int], percentile: float) -> int:
    if not values:
        return 0
    ordered = sorted(values)
    index = max(0, min(len(ordered) - 1, int((len(ordered) - 1) * percentile + 0.999999)))
    return ordered[index]


if __name__ == "__main__":
    raise SystemExit(main())
