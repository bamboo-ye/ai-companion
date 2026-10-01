"""标准库实验；只读导入真实上下文模块，不加载模型/数据库/Graph 依赖。"""
from __future__ import annotations

import importlib.util
from pathlib import Path
from typing import TypedDict, cast


class ClaimedShape(TypedDict):
    approved: bool


wrong = ["not", "a", "dict"]
claimed = cast(ClaimedShape, wrong)
assert claimed is wrong
assert isinstance(True, int)
print("类型实验：cast 不转换对象；bool 也是 int，需要额外排除")

original = {"history": [{"content": "原文"}]}
copied = dict(original)
copied["history"][0]["content"] = "修改后"
assert original["history"][0]["content"] == "修改后"
assert copied is not original
assert copied["history"] is original["history"]
print("复制实验：外层 dict 已复制，嵌套 history 仍共享")

# 直接按路径导入单个纯模块，不导入整个 ai_companion_worker 包。
root = Path(__file__).resolve().parents[3]
source = root / "workers/python/src/ai_companion_worker/conversation_context.py"
spec = importlib.util.spec_from_file_location("reading_context", source)
assert spec is not None and spec.loader is not None
context_module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(context_module)


def item(sequence: int, characters: int, bubble: int = 0) -> dict:
    return {"sequence": sequence, "bubble": bubble, "role": "assistant", "content": "甲" * characters}


# 真代码的每条成本为正文估算 + 4：三组总成本从旧到新为 600、800、700。
raw = [item(10, 596), item(20, 796), item(30, 296), item(30, 396, 1)]
bundle = {"conversation_context": {"version": context_module.VERSION, "history": raw}}
selected, omitted = context_module.history_view(bundle, "router")
assert context_module.NODE_HISTORY_BUDGETS["router"] == 2000
assert [entry["sequence"] for entry in selected] == [20, 30, 30]
assert omitted == 1
print("真实 history_view：成本 600/800/700，预算 2000，保留 sequence=20,30,30")

oversized = {"conversation_context": {"version": context_module.VERSION, "history": [item(40, 1997)]}}
selected, omitted = context_module.history_view(oversized, "router")
assert len(selected) == 1 and omitted == 0
assert context_module.estimate_tokens(selected[0]["content"]) + 4 == 2001
print("真实 history_view：最新组成本 2001，仍完整保留；这不替代最终窗口检查")

manifest = context_module.diagnostic_manifest({
    "recent_tokens": True,
    "memory_tokens": 3,
    "raw_text": "仅是虚构示例，正文不应进入此诊断清单",
    "sources": [{"kind": "memory", "id": "M1", "content": "示例私有正文", "version": "v1"}],
})
assert "recent_tokens" not in manifest
assert "raw_text" not in manifest
assert "content" not in manifest["sources"][0]
assert manifest["memory_tokens"] == 3
print("真实 diagnostic_manifest：排除布尔计数和正文，只保留允许字段")

# 覆盖率是独立教学计算，不是假称文档读取 API 自动替调用者累计。
all_chunks = {"C1", "C2", "C3", "C4", "C5", "C6"}
round_one = {"C1", "C2", "C3", "C4"}
round_two = {"C4", "C5", "C6"}
covered = round_one | round_two
assert covered == all_chunks
assert len(round_one) + len(round_two) == 7
assert len(covered) == 6
print("覆盖实验：两轮数量相加为 7，去重并集为 6，覆盖 6/6")
print("Python 实验全部通过（两项真实模块函数 + 类型/集合实验，不调用模型）")
