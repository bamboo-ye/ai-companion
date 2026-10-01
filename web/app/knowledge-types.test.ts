import assert from "node:assert/strict";
import test from "node:test";
import { appendKnowledgeRead, type KnowledgeRead } from "./knowledge-types.ts";

const first: KnowledgeRead = { item: { ref: { kind: "document", source_id: "doc", source_revision: "v1", fragment_id: "chunk", content_hash: "hash" }, title: "原文", content: "第一段", evidence_state: "unassessed" }, offset: 0, next_offset: 3, has_more: true, total_characters: 6 };
test("knowledge continuation preserves Chinese text and source identity", () => {
  const next = { ...first, item: { ...first.item, content: "第二段" }, offset: 3, next_offset: 6, has_more: false };
  assert.equal(appendKnowledgeRead(first, next).item.content, "第一段第二段");
  assert.throws(() => appendKnowledgeRead(null, next));
  assert.throws(() => appendKnowledgeRead(first, { ...next, offset: 2 }));
  assert.throws(() => appendKnowledgeRead(first, { ...next, item: { ...next.item, ref: { ...next.item.ref, source_revision: "v2" } } }));
  assert.throws(() => appendKnowledgeRead(first, { ...next, item: { ...next.item, ref: { ...next.item.ref, content_hash: "changed" } } }));
  assert.equal(appendKnowledgeRead(first, first).item.content, "第一段");
});
