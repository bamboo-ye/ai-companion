from __future__ import annotations

import json
import unittest
from dataclasses import replace
from pathlib import Path

from ai_companion_worker.agent_runtime import AgentRuntime, ModelBudgetExceeded, ModelDecision, ToolPreparation, build_graph, tool_allowed
from ai_companion_worker.agent_worker import _governance_output, _model_runtime_key
from ai_companion_worker.conversation_context import add_references, model_history
from ai_companion_worker.openrouter_decision import _available_tool_definitions
from test_openrouter_decision import StubOpenRouter
from test_agent_runtime import FakeDecisions, FakeTools, agent_input
from langgraph.checkpoint.memory import InMemorySaver


def replay() -> dict:
    path = Path(__file__).resolve().parents[3] / "evals/context/conversation-context.v1.json"
    return {"conversation_context": json.loads(path.read_text(encoding="utf-8"))}


class ConversationContextTest(unittest.TestCase):
    def test_complete_agent_flow_turns_retrieval_into_an_answer_in_every_module(self) -> None:
        class HelpDecisions(FakeDecisions):
            def respond(self, *, module, message, context):
                return "操作指南：" + context["observations"][-1]["data"]["body"]

        for module in ("companion", "life", "work"):
            with self.subTest(module=module):
                decisions = HelpDecisions(ModelDecision(intent="product_help", tool_name="product_knowledge_search", tool_arguments={"query": "如何上传资料"}))
                tools = FakeTools(ToolPreparation(status="completed", tool_name="product_knowledge_search", response="检索完成", data={"body": "上传 PDF 或 TXT，等待解析完成。"}))
                payload = agent_input("product-help-" + module, module)
                payload["user_message"] = "如何上传资料"
                payload["context"] = replay()
                payload["context"]["tools"] = [{"name": "product_knowledge_search", "description": "产品知识", "repeatable": True, "parameters": {"type": "object", "properties": {"query": {"type": "string"}}, "required": ["query"]}}]
                runtime = AgentRuntime(build_graph(checkpointer=InMemorySaver(), decisions=decisions, tools=tools))
                result = runtime.start(payload)
                self.assertEqual(result["outcome"], "completed")
                self.assertIn("操作指南", result["response"])
                self.assertEqual(len(tools.prepared), 1)
                self.assertEqual(tools.committed, [])

    def test_product_tools_are_allowed_without_opening_a_new_mutation_prefix(self) -> None:
        for module in ("companion", "life", "work"):
            self.assertTrue(tool_allowed(module, "product_knowledge_search"))
            self.assertTrue(tool_allowed(module, "product_knowledge_read"))
            self.assertFalse(tool_allowed(module, "product_knowledge_delete"))
        self.assertFalse(tool_allowed("companion", "work_wiki_read"))
        self.assertFalse(tool_allowed("life", "work_generate_pptx"))

    def test_life_product_lookup_is_composed_with_evidence_instead_of_echoing_status(self) -> None:
        port = StubOpenRouter([{"choices": [{"message": {"content": "依据指南给出的操作步骤"}}]}])
        context = replay()
        context["observations"] = [{"tool_name": "product_knowledge_read", "status": "completed", "response": "检索完成", "data": {"body": "补查取得的全文证据"}}]
        self.assertEqual(port.respond(module="life", message="具体步骤", context=context), "依据指南给出的操作步骤")
        request = port.requests[0]
        self.assertIn("补查取得的全文证据", json.dumps(request, ensure_ascii=False))
        self.assertEqual(request["messages"][-1]["content"], "具体步骤")
        self.assertFalse(any("补查取得的全文证据" in m["content"] for m in request["messages"] if m["role"] == "system"))

    def test_builtin_knowledge_reaches_all_nodes_as_reference_data(self) -> None:
        context = replay()
        context["conversation_context"]["knowledge"] = [{
            "content": "产品知识测试正文：请输入当前密码。",
            "source": {"kind": "builtin_knowledge", "id": "builtin-user-profile", "version": "abc"},
            "estimated_tokens": 30,
        }]
        context["conversation_context"]["manifest"]["knowledge_tokens"] = 30
        for role in ("router", "planner", "composer", "assessor", "repairer", "responder"):
            with self.subTest(role=role):
                payload = {"messages": [{"role": "user", "content": "current"}]}
                add_references(payload, context, role=role)
                self.assertEqual(payload["_context_manifest"]["knowledge_tokens"], 30)
                self.assertIn("产品知识测试正文", json.dumps(payload, ensure_ascii=False))
                self.assertFalse(any("产品知识测试正文" in m["content"] for m in payload["messages"] if m["role"] == "system"))
                self.assertEqual(payload["messages"][-1]["content"], "current")

    def test_builtin_reads_share_the_bounded_knowledge_tool_budget(self) -> None:
        definitions = [{"name": "product_knowledge_read", "repeatable": True}, {"name": "work_wiki_read", "repeatable": True}, {"name": "life_no_tool"}]
        observations = [{"tool_name": "product_knowledge_read", "status": "completed"} for _ in range(8)]
        self.assertEqual(_available_tool_definitions(definitions, observations), [{"name": "life_no_tool"}])

    def test_node_budgets_keep_groups_and_protected_summary(self) -> None:
        context = replay()
        context["conversation_context"]["history"] = [
            {"id": f"m{i}", "sequence": i // 2, "role": "assistant", "content": "中文" * 100}
            for i in range(30)
        ]
        router = model_history(context, 20, "router")
        composer = model_history(context, 20, "composer")
        self.assertLess(len(router), len(composer))
        self.assertEqual(len(router) % 2, 0)
        payload = {"messages": [{"role": "user", "content": "current"}]}
        add_references(payload, context, role="router")
        self.assertGreater(payload["_context_manifest"]["history_omitted"], 0)
        self.assertIn("不要超过六分钟", json.dumps(payload, ensure_ascii=False))
        self.assertEqual(payload["messages"][-1]["content"], "current")

    def test_wiki_traversal_stops_after_eight_successful_calls(self) -> None:
        definitions = [{"name": "work_wiki_read", "repeatable": True}, {"name": "work_no_tool"}]
        observations = [{"tool_name": "work_wiki_read", "status": "completed"} for _ in range(8)]
        self.assertEqual(_available_tool_definitions(definitions, observations), [{"name": "work_no_tool"}])

    def test_runtime_cache_distinguishes_captured_context_windows(self) -> None:
        run = {"model_profile_snapshot": {"fingerprint": "a" * 64, "variables": {"MODEL_RESPONDER_CONTEXT_WINDOW": "65536"}}}
        before = _model_runtime_key(run)
        run["model_profile_snapshot"]["variables"]["MODEL_RESPONDER_CONTEXT_WINDOW"] = "32768"
        self.assertNotEqual(before, _model_runtime_key(run))

    def test_snapshot_history_survives_legacy_count_limit_without_duplicate_request(self) -> None:
        context = replay()
        context["conversation_context"]["history"] = [
            {"id": f"m{i}", "role": "user", "content": f"第{i}轮约束"}
            for i in range(55)
        ]
        context["history"] = [{"role": "user", "content": "stale legacy history"}]
        port = StubOpenRouter([{"choices": [{"message": {"content": "回答"}}]}])
        port.respond(module="work", message="CURRENT REQUEST", context=context)
        request = port.requests[0]
        messages = request["messages"]
        contents = [item["content"] for item in messages]
        self.assertIn("第0轮约束", contents)
        self.assertIn("第54轮约束", contents)
        self.assertNotIn("stale legacy history", contents)
        self.assertEqual(contents.count("CURRENT REQUEST"), 1)
        self.assertEqual(contents.count("第0轮约束"), 1)
        self.assertEqual(messages[-1]["content"], "CURRENT REQUEST")
        self.assertNotIn("_context_manifest", request)
        reference = next(item for item in messages if item["content"].startswith("会话参考资料："))
        self.assertEqual(reference["role"], "user")
        self.assertIn("不要超过六分钟", reference["content"])
        self.assertIn("original-fact", reference["content"])
        events = port.consume_observability()
        self.assertIn("context_manifest", events[0])
        self.assertGreater(events[0]["prompt_token_upper_bound"], 0)
        self.assertNotIn("不吃香菜", json.dumps(events[0]["context_manifest"], ensure_ascii=False))
        self.assertEqual(
            _governance_output({"model_events": events})["model"]["calls"][0]["context_manifest"],
            events[0]["context_manifest"],
        )

    def test_declarative_node_projects_new_history_and_reference_once(self) -> None:
        port = StubOpenRouter([{"choices": [{"message": {"content": "回答"}}]}])
        port.execute_declarative_node(
            module="work", message="current", node_type="model", role="responder",
            prompt_template="依据提供的资料回答", conditions=(), context=replay(),
        )
        messages = port.requests[0]["messages"]
        text = json.dumps(messages, ensure_ascii=False)
        self.assertEqual(text.count("请继续准备项目汇报。"), 1)
        self.assertEqual(text.count("不要超过六分钟"), 1)
        self.assertEqual(json.loads(messages[-1]["content"])["request"], "current")

    def test_manifest_drops_content_from_source_metadata(self) -> None:
        context = replay()
        context["conversation_context"]["manifest"]["sources"][0]["content"] = "private"
        payload = {"messages": [{"role": "user", "content": "current"}]}
        add_references(payload, context)
        self.assertNotIn("private", json.dumps(payload["_context_manifest"]))

    def test_other_model_nodes_receive_history_and_references(self) -> None:
        port = StubOpenRouter([])
        for role in ("planner", "composer", "assessor", "repairer"):
            with self.subTest(role=role):
                payload = {"messages": [{"role": "user", "content": "current"}], "max_tokens": 256}
                port._apply_model_allowance(payload, replay(), role)
                contents = [item["content"] for item in payload["messages"]]
                self.assertIn("请继续准备项目汇报。", contents)
                self.assertEqual(contents[-1], "current")
                self.assertIn("不要超过六分钟", "\n".join(contents))

    def test_legacy_checkpoint_keeps_existing_projection(self) -> None:
        context = {"history": [{"role": "user", "content": str(i)} for i in range(60)]}
        self.assertEqual(len(model_history(context, 20)), 20)
        self.assertEqual(model_history(context, 20)[0]["content"], "40")
        payload = {"messages": [{"role": "user", "content": "current"}]}
        add_references(payload, context)
        self.assertEqual(payload, {"messages": [{"role": "user", "content": "current"}]})

    def test_full_request_budget_rejects_reference_and_tool_overflow_before_network(self) -> None:
        port = StubOpenRouter([])
        port._config = replace(port._config, context_window=4096)
        context = replay()
        context["conversation_context"]["summary"]["content"] = "背景" * 2000
        with self.assertRaises(ModelBudgetExceeded):
            port.respond(module="work", message="current", context=context)
        self.assertEqual(port.requests, [])
        with self.assertRaises(ModelBudgetExceeded):
            port._apply_model_allowance(
                {"messages": [{"role": "user", "content": "hi"}], "max_tokens": 256,
                 "tools": [{"description": "schema" * 2000}]}, {}, "router",
            )

    def test_role_window_and_current_output_reserve_apply_without_run_allowance(self) -> None:
        port = StubOpenRouter([])
        port._config = replace(port._config, role_context_windows={"responder": 2048})
        with self.assertRaises(ModelBudgetExceeded):
            port.respond(module="work", message="current", context={})
        self.assertEqual(port.requests, [])

    def test_unknown_snapshot_version_does_not_silently_lose_context(self) -> None:
        context = replay()
        context["conversation_context"]["version"] = "future-version"
        port = StubOpenRouter([])
        with self.assertRaises(ValueError):
            port.respond(module="work", message="current", context=context)
        self.assertEqual(port.requests, [])


if __name__ == "__main__":
    unittest.main()
