"use client";

import { FormEvent, useCallback, useEffect, useRef, useState } from "react";

const apiBase = process.env.NEXT_PUBLIC_API_BASE_URL ?? "http://localhost:8080";
type Page = { id: string; title: string; group: string; audience: string; body: string; source: string; section: string; version: string; links: string[] };
const audiences: Record<string, string> = { user: "用户指南", admin: "后台指南", project: "项目介绍", technical: "技术说明" };

export function BuiltinKnowledgePanel({ token, initialPageID = "" }: { token: string; initialPageID?: string }) {
  const [pages, setPages] = useState<Page[]>([]);
  const [selected, setSelected] = useState<Page | null>(null);
  const [audience, setAudience] = useState("all");
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);
  const readSequence = useRef(0);
  const request = useCallback(async (path: string, init?: RequestInit) => {
    const response = await fetch(`${apiBase}/v1/knowledge/builtin/${path}`, { ...init, headers: { "Content-Type": "application/json", Authorization: `Bearer ${token}` }, cache: "no-store" });
    if (!response.ok) throw new Error((await response.json().catch(() => ({}))).message ?? "产品知识暂时无法加载，请重试。");
    return response.json();
  }, [token]);
  const open = useCallback(async (id: string) => {
    const sequence = ++readSequence.current;
    setBusy(true); setMessage("");
    try { const page = await request(`pages/${encodeURIComponent(id)}`) as Page; if (sequence === readSequence.current) setSelected(page); }
    catch (error) { if (sequence === readSequence.current) { setSelected(null); setMessage((error as Error).message); } }
    finally { if (sequence === readSequence.current) setBusy(false); }
  }, [request]);
  const load = useCallback(async () => {
    const data = await request("pages") as { items: Page[] }; setPages(data.items);
  }, [request]);
  useEffect(() => {
    let active = true;
    const pendingReads = readSequence;
    void request("pages").then(data => { if (active) setPages(data.items); }).catch(error => { if (active) setMessage(error.message); });
    if (initialPageID) queueMicrotask(() => { if (active) void open(initialPageID); });
    return () => { active = false; pendingReads.current++; };
  }, [request, open, initialPageID]);
  async function search(event: FormEvent<HTMLFormElement>) {
    event.preventDefault(); setBusy(true); setMessage("");
    const query = String(new FormData(event.currentTarget).get("query") ?? "");
    try {
      const data = await request("search", { method: "POST", body: JSON.stringify({ query, limit: 12 }) }) as { hits: { page: Page }[] };
      setPages(data.hits.map(hit => hit.page)); setAudience("all");
      if (!data.hits.length) setMessage("没有找到相关说明，请换用功能名称或具体操作搜索。");
    } catch (error) { setMessage((error as Error).message); }
    finally { setBusy(false); }
  }
  return <section className="wikiKnowledge" aria-label="内置产品知识">
    <header><div><h4>产品知识</h4><p>项目介绍、完整使用指南与操作说明已内置，无需上传。三个模块的对话都可按需引用。</p></div><button type="button" className="textButton" disabled={busy} onClick={() => { setAudience("all"); void load().catch(error => setMessage(error.message)); }}>全部章节</button></header>
    <form className="documentQuery" onSubmit={search}><label>查找使用说明<input name="query" required minLength={2} maxLength={1000} placeholder="例如：如何上传资料、管理记忆、生成 PPT" /></label><button disabled={busy}>搜索指南</button></form>
    <div className="wikiKinds" aria-label="指南分类">{["all", ...Object.keys(audiences)].map(key => <button type="button" key={key} aria-pressed={audience === key} onClick={() => setAudience(key)}>{audiences[key] ?? "全部"}</button>)}</div>
    {message && <p className="formMessage" role="status">{message}</p>}
    <div className="wikiColumns">
      <nav className="wikiPageList" aria-label="内置知识章节">{pages.filter(page => audience === "all" || page.audience === audience).map(page => <button type="button" key={page.id} disabled={busy} aria-current={selected?.id === page.id ? "page" : undefined} onClick={() => void open(page.id)}><small>{audiences[page.audience]} · {page.group}</small><strong>{page.title}</strong></button>)}</nav>
      {selected && <article className="wikiReader" aria-label="内置知识全文">
        <header><div><small>{audiences[selected.audience]} · 随应用版本更新</small><h4>{selected.title}</h4></div><button type="button" className="textButton" onClick={() => setSelected(null)}>关闭</button></header>
        <p className="documentNotice">{selected.source} · {selected.section} · 版本 {selected.version.slice(0, 12)}</p>
        <div className="wikiMarkdown">{selected.body.split("\n").map((line, i) => line.startsWith("#") ? <h5 key={i}>{line.replace(/^#+\s*/, "")}</h5> : <p key={i}>{line || "\u00a0"}</p>)}</div>
        {!!selected.links.length && <div className="wikiActions">{selected.links.map(id => <button type="button" key={id} disabled={busy} onClick={() => void open(id)}>{pages.find(page => page.id === id)?.title ?? "继续阅读同一章节"}</button>)}</div>}
        <p><small>说明中的示例不代表操作已执行。请在对应功能中核对参数与结果。</small></p>
      </article>}
    </div>
  </section>;
}
