"use client";

import { useEffect, useState } from "react";
import { EvidenceReviewCard } from "../../evidence-review-card";
import { restoreEvidenceReview, type EvidenceReview } from "../../evidence-review";
import { MemoryClarificationCard, MemoryCorrectionEditor } from "../../memory-clarification";

type Result = { scenario: string; response: string; evidence_review: EvidenceReview | null; read_count: number; elapsed_ms: number; external_model_calls: number; nodes: string[] };
const cases = [ ["resolved", "新证据解决分歧"], ["unresolved", "重复资料保留分歧"], ["revoked", "来源失效停止交付"], ["write_denied", "拒绝写入补证"], ["off", "关闭辩论"] ];
const storageKey = "debate-preview-synthetic-result-v1";
const initialMemories = [{id: "old", content: "我周一在上海办公。"}, {id: "new", content: "我周一在杭州办公。"}];

export function EvidenceReviewPreview() {
  const [result, setResult] = useState<Result | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [historical, setHistorical] = useState(false);
  const [memories, setMemories] = useState(initialMemories);
  const [editing, setEditing] = useState("");
  const [draft, setDraft] = useState("");
  const [corrected, setCorrected] = useState(false);
  useEffect(() => {
    let active = true;
    async function restore() {
      try {
        const stored = sessionStorage.getItem(storageKey);
        if (!stored) return;
        const saved = JSON.parse(stored) as Result;
        const restored = await restoreEvidenceReview(
          [{id: "preview-message", role: "assistant", content: "<!--ai-agent-run:preview-run|completed|1-->"}], "preview-conversation",
          async () => ({id: "preview-run", conversation_id: "preview-conversation", status: "completed", output: saved}),
        );
        if (active) { setResult({...saved, evidence_review: restored?.review ?? null}); setHistorical(true); }
      } catch { if (active) setError("本地验收记录无法恢复，请重新执行场景。"); }
    }
    void restore();
    return () => { active = false; };
  }, []);
  async function run(scenario: string) {
    setBusy(true); setError(""); setResult(null); setHistorical(false);
    try {
      const response = await fetch("/dev/evidence-review/run", { method: "POST", headers: {"Content-Type": "application/json"}, body: JSON.stringify({scenario}) });
      if (!response.ok) throw new Error("验收场景运行失败，请检查本地服务日志。");
      const next = await response.json() as Result;
      setResult(next);
      sessionStorage.setItem(storageKey, JSON.stringify(next));
    } catch (cause) { setError(cause instanceof Error ? cause.message : "验收失败"); }
    finally { setBusy(false); }
  }
  return <main style={{maxWidth: 850, margin: "40px auto", padding: 24}}>
    <p>本地开发验收 · 合成资料</p><h1>资料核验与证据仲裁</h1>
    <p>执行实际工作流和交付组件。模型与资料接口使用固定验收数据，不发送外部请求，也不代表真实模型准确率。</p>
    <div style={{display: "flex", flexWrap: "wrap", gap: 10, margin: "24px 0"}}>{cases.map(([key,label]) => <button key={key} disabled={busy} onClick={() => void run(key)}>{label}</button>)}</div>
    {busy && <p role="status">正在执行核验流程…</p>}
    {error && <p role="alert">{error}</p>}
    {result && <section aria-label="验收结果">
      <h2>处理结果</h2><p style={{whiteSpace: "pre-wrap"}}>{result.response}</p>
      {historical && <p role="status">已恢复上次合成验收记录（仅当前标签页保存）。</p>}
      {result.evidence_review ? <EvidenceReviewCard review={result.evidence_review} historical={historical}/> : <p>辩论已关闭，没有增加辩论调用。</p>}
      <p>读取资料 {result.read_count} 次 · 外部模型调用 {result.external_model_calls} 次</p>
      <details><summary>查看实际执行节点</summary><p style={{overflowWrap: "anywhere"}}>{result.nodes.join(" → ")}</p></details>
    </section>}
    <section aria-label="记忆澄清验收" style={{marginTop: 32}}>
      <h2>记忆澄清验收</h2><p>以下是独立的合成记录，修改不会写入你的长期记忆。</p>
      {!corrected && <MemoryClarificationCard question={{issue_id: "location", question: "这两条记录分别适用于什么时间或情境？", candidate_ids: ["old", "new"]}} candidates={memories}
        onCorrect={item => {setEditing(item.id);setDraft(item.content);}}/>}
      {editing && <MemoryCorrectionEditor value={draft} onChange={setDraft} onCancel={()=>setEditing("")}
        onSave={content=>{setMemories(items=>items.map(item=>item.id===editing?{...item,content}:item));setEditing("");setCorrected(true);}}/>}
      {corrected && <div role="status"><p>已保存本地验收纠正，另一条记录仍保留。</p>{memories.map(item=><p key={item.id}>{item.content}</p>)}</div>}
    </section>
  </main>;
}
