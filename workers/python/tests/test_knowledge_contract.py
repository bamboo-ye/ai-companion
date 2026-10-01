from ai_companion_worker.debate import evidence_fragments, source_references
from ai_companion_worker.parallel_tasks import READ_TOOLS, validate_research_tasks


def test_unified_raw_evidence_survives_checkpoint_adapter():
    raw = {"ref": {"kind": "document", "source_id": "doc", "source_revision": "r1", "fragment_id": "chunk", "content_hash": "hash"}, "content": "准确的原文", "title": "资料"}
    guide = {"ref": {"kind": "builtin", "source_id": "guide", "source_revision": "r2"}, "content": "产品说明"}
    fragments = evidence_fragments({"hits": [raw, guide]})
    assert len(fragments) == 2
    assert fragments[0]["version"] == "r1"
    assert fragments[0]["chunk_id"] == "chunk"
    assert fragments[1]["source_id"] == "product:guide"
    refs = source_references({"tool": {"fragments": fragments}})
    assert refs[0] == {"source_id": "doc", "version": "r1", "chunk_id": "chunk", "text": "准确的原文"}


def test_derived_summaries_are_not_promoted_to_raw_evidence():
    for kind in ("wiki", "memory"):
        assert evidence_fragments({"item": {"ref": {"kind": kind, "source_id": "id", "source_revision": "v"}, "content": "尚未独立核实的摘要"}}) == []


def test_unified_tools_use_existing_bounded_read_plan():
    assert {"work_knowledge_search", "work_knowledge_read"} <= READ_TOOLS
    catalog = [{"name": "work_knowledge_search", "parameters": {"type": "object", "properties": {"query": {"type": "string"}}, "required": ["query"], "additionalProperties": False}}]
    tasks = [{"id": key, "question": key, "tool_name": "work_knowledge_search", "arguments": {"query": key}, "depends_on": []} for key in ("cost", "risk")]
    assert len(validate_research_tasks(tasks, catalog)) == 2
