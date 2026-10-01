export type KnowledgeRef = {
  kind: "document" | "wiki" | "memory" | "builtin";
  source_id: string; source_revision: string; fragment_id?: string; content_hash: string;
  locator?: { page_start?: number; page_end?: number; section?: string };
};
export type KnowledgeItem = { ref: KnowledgeRef; title: string; content: string; evidence_state: string; truncated?: boolean; dependencies?: KnowledgeRef[]; dependencies_omitted?: number };
export type KnowledgeResult = { hits: KnowledgeItem[]; manifest: { degraded: string[]; omitted: number; evidence_state: string; candidates: Record<string, number> } };
export type KnowledgeRead = { item: KnowledgeItem; offset: number; next_offset: number; has_more: boolean; total_characters: number };
export const knowledgeKinds: Record<KnowledgeRef["kind"], string> = { document: "原始资料", wiki: "知识 Wiki", memory: "个人记忆", builtin: "产品指南" };
export function knowledgeKey(ref: KnowledgeRef): string { return [ref.kind, ref.source_id, ref.source_revision, ref.fragment_id ?? ""].join(":"); }
export function appendKnowledgeRead(previous: KnowledgeRead | null, next: KnowledgeRead): KnowledgeRead {
  if (next.offset === 0) return next;
  if (!previous || knowledgeKey(previous.item.ref) !== knowledgeKey(next.item.ref) || previous.item.ref.content_hash !== next.item.ref.content_hash || previous.next_offset !== next.offset) throw new Error("资料已变化，请重新打开来源。");
  return { ...next, offset: 0, item: { ...next.item, content: previous.item.content + next.item.content } };
}
