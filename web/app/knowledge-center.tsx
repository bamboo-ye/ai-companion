"use client";

import { FormEvent, useCallback, useEffect, useRef, useState } from "react";
import { BuiltinKnowledgePanel } from "./builtin-knowledge-panel";
import { appendKnowledgeRead, knowledgeKey, knowledgeKinds, type KnowledgeRead, type KnowledgeRef, type KnowledgeResult } from "./knowledge-types";

const apiBase = process.env.NEXT_PUBLIC_API_BASE_URL ?? "http://localhost:8080";
export function KnowledgeCenter({ token, initialPageID = "", onDocuments, onMemories }: { token: string; initialPageID?: string; onDocuments: () => void; onMemories: () => void }) {
  const [tab, setTab] = useState(initialPageID ? "guide" : "search");
  const [kind, setKind] = useState("all");
  const [workspace, setWorkspace] = useState("");
  const [workspaces, setWorkspaces] = useState<{ id: string; name: string }[]>([]);
  const [result, setResult] = useState<KnowledgeResult | null>(null);
  const [reader, setReader] = useState<KnowledgeRead | null>(null);
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);
  const sequence = useRef(0);
  const request = useCallback(async <T,>(path: string, body?: unknown): Promise<T> => {
    const response = await fetch(`${apiBase}/v1/${path}`, { method: body === undefined ? "GET" : "POST", headers: { "Content-Type": "application/json", Authorization: `Bearer ${token}` }, body: body === undefined ? undefined : JSON.stringify(body), cache: "no-store" });
    const data = await response.json();
    if (!response.ok) throw new Error(data.message ?? "知识暂时无法加载，请重试。");
    return data as T;
  }, [token]);
  useEffect(() => {
    let active = true;
    const pending = sequence;
    void request<{ items: { id: string; name: string }[] }>("workspaces").then(data => { if (active) setWorkspaces(data.items); }).catch(() => { /* Personal knowledge remains usable. */ });
    return () => { active = false; pending.current++; };
  }, [request]);
  function reset() { sequence.current++; setResult(null); setReader(null); setMessage(""); setBusy(false); }
  async function search(event: FormEvent<HTMLFormElement>) {
    event.preventDefault(); const current = ++sequence.current;
    const query = String(new FormData(event.currentTarget).get("query") ?? "");
    setBusy(true); setMessage(""); setReader(null); setResult(null);
    try {
      const next = await request<KnowledgeResult>("knowledge/search", { query, workspace_id: workspace, ...(kind === "all" ? {} : { source_kinds: [kind] }), limit: 12, budget: 6000 });
      if (current === sequence.current) { setResult(next); if (!next.hits.length) setMessage("没有找到可用证据。可以调整关键词、查看资料状态，或补充来源。"); }
    } catch (error) { if (current === sequence.current) setMessage((error as Error).message); }
    finally { if (current === sequence.current) setBusy(false); }
  }
  async function read(ref: KnowledgeRef, offset = 0) {
    const current = ++sequence.current; setBusy(true); setMessage(""); if (!offset) setReader(null);
    try {
      const next = await request<KnowledgeRead>("knowledge/read", { ref, workspace_id: workspace, offset });
      if (current === sequence.current) setReader(appendKnowledgeRead(reader, next));
    } catch (error) { if (current === sequence.current) { setReader(null); setMessage((error as Error).message); } }
    finally { if (current === sequence.current) setBusy(false); }
  }
  async function feedback(event: FormEvent<HTMLFormElement>) {
    event.preventDefault(); if (!reader) return; const current = ++sequence.current; const data = new FormData(event.currentTarget); setBusy(true); setMessage("");
    try { await request("knowledge/feedback", { ref: reader.item.ref, workspace_id: workspace, rating: data.get("rating"), note: data.get("note") }); if (current === sequence.current) setMessage("反馈已保存，并进入知识质量复核队列。"); }
    catch (error) { if (current === sequence.current) setMessage((error as Error).message); }
    finally { if (current === sequence.current) setBusy(false); }
  }
  return <section className="wikiKnowledge" aria-label="知识中心">
    <header><div><h3>知识中心</h3><p>在资料、Wiki、个人记忆和产品指南中查找证据，沿来源核对内容。</p></div><div className="wikiActions"><button type="button" onClick={onDocuments}>管理资料</button><button type="button" onClick={onMemories}>管理记忆</button></div></header>
    <div className="wikiKinds" aria-label="知识中心视图"><button type="button" aria-pressed={tab === "search"} onClick={() => { reset(); setTab("search"); }}>统一搜索</button><button type="button" aria-pressed={tab === "guide"} onClick={() => { reset(); setTab("guide"); }}>浏览产品指南</button></div>
    {tab === "guide" ? <BuiltinKnowledgePanel token={token} initialPageID={initialPageID} /> : <>
      <form className="documentQuery" onSubmit={search}><label>查找知识<input name="query" required minLength={2} maxLength={1000} placeholder="例如：项目预算、饮食偏好、如何生成演示文稿" /></label><label>知识范围<select value={workspace} onChange={event => { reset(); setWorkspace(event.target.value); setKind("all"); }}><option value="">我的知识</option>{workspaces.map(item => <option key={item.id} value={item.id}>{item.name}</option>)}</select></label><button disabled={busy}>搜索</button></form>
      <div className="wikiKinds" aria-label="来源类型">{["all", ...Object.keys(knowledgeKinds)].filter(key => !workspace || key !== "memory").map(key => <button key={key} type="button" aria-pressed={kind === key} onClick={() => { reset(); setKind(key); }}>{key === "all" ? "全部来源" : knowledgeKinds[key as KnowledgeRef["kind"]]}</button>)}</div>
      {busy && <p role="status">正在查找并核对来源…</p>}
      {message && <p className="formMessage" role="status">{message}</p>}
      {!!result?.manifest.degraded.length && <p className="documentNotice">部分检索能力尚未就绪或暂时不可用，本次结果可能不完整。</p>}
      {!!result?.manifest.omitted && <p className="documentNotice">部分候选未展示。请缩小问题范围，或分别搜索各类来源。</p>}
      <div className="wikiColumns"><nav className="wikiPageList" aria-label="知识搜索结果">{result?.hits.map(item => <button key={knowledgeKey(item.ref)} type="button" disabled={busy} onClick={() => void read(item.ref)}><small>{knowledgeKinds[item.ref.kind]}{item.ref.locator?.page_start ? ` · 第 ${item.ref.locator.page_start} 页` : ""}{item.evidence_state === "disputed" ? " · 存在分歧" : ""}</small><strong>{item.title}</strong><span>{item.content.slice(0, 160)}</span></button>)}</nav>
        {reader && <article className="wikiReader" aria-label="知识来源阅读"><header><div><small>{knowledgeKinds[reader.item.ref.kind]}</small><h4>{reader.item.title}</h4></div><button type="button" className="textButton" onClick={() => { sequence.current++; setReader(null); setBusy(false); }}>关闭</button></header>
          <p className="documentNotice">以下是来源记录。检索命中不代表其中的结论已核实。{reader.item.evidence_state === "disputed" ? "此来源存在分歧，请核对原始资料。" : ""}</p>
          <div className="wikiMarkdown">{reader.item.content.split("\n").map((line, index) => <p key={index}>{line || "\u00a0"}</p>)}</div>
          {reader.has_more && <button type="button" disabled={busy} onClick={() => void read(reader.item.ref, reader.next_offset)}>继续阅读</button>}
          {!!reader.item.dependencies?.length && <div className="wikiActions">{reader.item.dependencies.map((ref, index) => <button type="button" disabled={busy} key={`${knowledgeKey(ref)}:${index}`} onClick={() => void read(ref)}>查看原文{ref.locator?.page_start ? `第 ${ref.locator.page_start} 页` : ` ${index + 1}`}</button>)}</div>}
          {!!reader.item.dependencies_omitted && <p>还有 {reader.item.dependencies_omitted} 条来源引用，请在 Wiki 中查看完整记录。</p>}
          <form className="documentQuery" onSubmit={feedback}><label>来源反馈<select name="rating"><option value="helpful">有帮助</option><option value="incorrect">内容有误</option><option value="outdated">已经过期</option><option value="missing">缺少信息</option><option value="unsupported">引用不支持结论</option><option value="permission">可见范围有问题</option></select></label><label>补充说明<input name="note" maxLength={1000} placeholder="描述需要核对的内容" /></label><button disabled={busy}>提交反馈</button></form>
        </article>}
      </div>
    </>}
  </section>;
}
