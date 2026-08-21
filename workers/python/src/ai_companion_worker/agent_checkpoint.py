from __future__ import annotations

import argparse
import json
import os
from typing import Any, Mapping, cast
from urllib.parse import parse_qs, urlencode, urlsplit, urlunsplit
from uuid import uuid4

os.environ.setdefault("LANGGRAPH_STRICT_MSGPACK", "true")


def scoped_dsn(value: str) -> str:
    raw = value.strip()
    if not raw:
        raise ValueError("LANGGRAPH_POSTGRES_DSN is required")
    parsed = urlsplit(raw)
    if parsed.scheme not in {"postgres", "postgresql"} or not parsed.hostname:
        raise ValueError("LANGGRAPH_POSTGRES_DSN must be a PostgreSQL URI")
    query = parse_qs(parsed.query, keep_blank_values=True)
    options = query.get("options", [])
    if not any("search_path=langgraph,public" in option for option in options):
        query["options"] = ["-csearch_path=langgraph,public"]
    normalized_query = urlencode(
        [(key, item) for key, values in query.items() for item in values]
    )
    return urlunsplit(
        (parsed.scheme, parsed.netloc, parsed.path, normalized_query, parsed.fragment)
    )


def setup_checkpoint(dsn: str) -> dict[str, object]:
    from langgraph.checkpoint.postgres import PostgresSaver
    from psycopg import connect

    normalized = scoped_dsn(dsn)
    with PostgresSaver.from_conn_string(normalized) as checkpointer:
        checkpointer.setup()
    expected = (
        "checkpoint_migrations",
        "checkpoints",
        "checkpoint_blobs",
        "checkpoint_writes",
    )
    with connect(normalized) as connection:
        with connection.cursor() as cursor:
            cursor.execute(
                """
                SELECT table_name
                FROM information_schema.tables
                WHERE table_schema='langgraph'
                ORDER BY table_name
                """
            )
            tables = {str(row[0]) for row in cursor.fetchall()}
    missing = sorted(set(expected) - tables)
    if missing:
        raise RuntimeError(f"LangGraph checkpoint tables missing: {missing}")
    return {
        "status": "ready",
        "schema": "langgraph",
        "tables": sorted(tables),
        "strict_msgpack": os.environ.get("LANGGRAPH_STRICT_MSGPACK", "").lower()
        == "true",
    }


def smoke_checkpoint(dsn: str) -> dict[str, object]:
    from langchain_core.runnables import RunnableConfig
    from langgraph.checkpoint.postgres import PostgresSaver

    from ai_companion_worker.agent_runtime import (
        AgentAssessment,
        AgentPlan,
        AgentRuntime,
        ModelDecision,
        ModuleKey,
        ToolOutcome,
        ToolPreparation,
        build_graph,
        interrupt_payloads,
        runtime_config,
    )

    class SmokeDecisions:
        def plan(
            self,
            *,
            module: ModuleKey,
            message: str,
            context: Mapping[str, Any],
        ) -> AgentPlan:
            del module, context
            return AgentPlan(
                objective=message,
                steps=("识别账单", "等待确认", "写入并观察"),
                success_criteria="账单只在确认后写入。",
            )

        def decide(
            self,
            *,
            module: ModuleKey,
            message: str,
            context: Mapping[str, Any],
        ) -> ModelDecision:
            return ModelDecision(
                intent="ledger_write",
                tool_name="life.prepare_ledger_entry",
                tool_arguments={"text": message},
            )

        def respond(
            self,
            *,
            module: ModuleKey,
            message: str,
            context: Mapping[str, Any],
        ) -> str:
            del module, message, context
            return "PostgreSQL 检查点响应。"

        def assess(
            self,
            *,
            module: ModuleKey,
            message: str,
            context: Mapping[str, Any],
        ) -> AgentAssessment:
            del module, message, context
            return AgentAssessment(status="completed", reason="写入已完成")

    class SmokeTools:
        commits = 0

        def prepare(self, **_values: Any) -> ToolPreparation:
            return ToolPreparation(
                status="requires_confirmation",
                tool_name="life.prepare_ledger_entry",
                risk_level="medium",
                summary="支出 50.00 元，分类：餐饮",
                confirmation_token="checkpoint-smoke-candidate",
                normalized_arguments={"amount_minor": 5000, "currency": "CNY"},
            )

        def commit(self, **_values: Any) -> ToolOutcome:
            self.commits += 1
            return ToolOutcome(response="PostgreSQL 检查点恢复成功。")

        def observe(self, **_values: Any) -> ToolOutcome:
            raise RuntimeError("checkpoint smoke should not observe an async task")

    normalized = scoped_dsn(dsn)
    run_id = str(uuid4())
    tools = SmokeTools()
    with PostgresSaver.from_conn_string(normalized) as checkpointer:
        checkpointer.setup()
        runtime = AgentRuntime(
            build_graph(
                checkpointer=checkpointer,
                decisions=SmokeDecisions(),
                tools=tools,
            )
        )
        first = runtime.start(
            {
                "run_id": run_id,
                "user_id": "00000000-0000-0000-0000-000000000001",
                "conversation_id": "00000000-0000-0000-0000-000000000002",
                "character_id": "00000000-0000-0000-0000-000000000003",
                "module": "life",
                "user_message": "今天午饭花了50元",
                "context": {"timezone": "Asia/Shanghai"},
            }
        )
        payloads = interrupt_payloads(first)
        if len(payloads) != 1 or tools.commits != 0:
            raise RuntimeError("graph did not persist before approval")
        if (
            checkpointer.get_tuple(
                cast(RunnableConfig, runtime_config(run_id))
            )
            is None
        ):
            raise RuntimeError("PostgreSQL checkpoint was not found")
        final = runtime.resume(run_id, {"approved": True})
        if final.get("outcome") != "completed" or tools.commits != 1:
            raise RuntimeError("graph did not resume from PostgreSQL checkpoint")
        checkpointer.delete_thread(run_id)
    return {
        "status": "passed",
        "schema": "langgraph",
        "thread_id_equals_agent_run_id": True,
        "interrupts": len(payloads),
        "commits_before_approval": 0,
        "commits_after_approval": tools.commits,
    }


def main() -> None:
    parser = argparse.ArgumentParser(description="Set up LangGraph PostgreSQL checkpoints")
    parser.add_argument(
        "command",
        nargs="?",
        default="setup",
        choices=("setup", "smoke"),
    )
    args = parser.parse_args()
    if args.command == "setup":
        payload = setup_checkpoint(os.environ.get("LANGGRAPH_POSTGRES_DSN", ""))
        print(json.dumps(payload, ensure_ascii=False, sort_keys=True), flush=True)
    if args.command == "smoke":
        payload = smoke_checkpoint(os.environ.get("LANGGRAPH_POSTGRES_DSN", ""))
        print(json.dumps(payload, ensure_ascii=False, sort_keys=True), flush=True)


if __name__ == "__main__":
    main()
