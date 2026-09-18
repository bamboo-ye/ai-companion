"use client";

import { FormEvent, useCallback, useEffect, useState } from "react";

const apiBase = process.env.NEXT_PUBLIC_API_BASE_URL ?? "http://localhost:8080";
type WikiPage = {
  id: string; version: number; kind: string; title: string; body: string;
  sources: { document_id: string; version: string; name: string }[];
  evidence: { document_id: string; chunk_id: string; quote: string; page: number }[];
  links: string[]; conflicts: string[]; edited: boolean; stale: boolean; updated_at: string;
};
const kinds: Record<string, string> = { source: "来源摘要", topic: "主题", entity: "实体", decision: "决策" };

export function WikiPanel({ token }: { token: string }) {
  const [pages, setPages] = useState<WikiPage[]>([]);
  const [selected, setSelected] = useState<WikiPage | null>(null);
  const [versions, setVersions] = useState<WikiPage[]>([]);
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);
  const [editing, setEditing] = useState(false);
  const [title, setTitle] = useState("");
  const [body, setBody] = useState("");
  const [filter, setFilter] = useState("all");
  const [searching, setSearching] = useState(false);

  const request = useCallback(async (path: string, init?: RequestInit) => {
    const response = await fetch(`${apiBase}/v1/wiki/${path}`, {
      ...init, headers: { "Content-Type": "application/json", Authorization: `Bearer ${token}`, ...init?.headers },
    });
    if (!response.ok) {
      const data = await response.json().catch(() => ({}));
      throw new Error(data.message ?? "Wiki 暂时不可用，请重试。");
    }
    return response;
  }, [token]);

  const load = useCallback(async () => {
    const data = await (await request("pages")).json() as { items: WikiPage[] };
    setPages(data.items);
    setSelected(current => {
      if (!current) return current;
      const latest = data.items.find(page => page.id === current.id);
      return !latest || latest.stale || (!editing && latest.version !== current.version) ? null : current;
    });
  }, [request, editing]);

  useEffect(() => {
    let active = true;
    const refresh = () => { if (active && !searching) void load().catch((error: Error) => setMessage(error.message)); };
    queueMicrotask(refresh);
    const timer = window.setInterval(refresh, 5000);
    return () => { active = false; window.clearInterval(timer); };
  }, [load, searching]);

  async function open(id: string) {
    setBusy(true); setMessage(""); setEditing(false); setVersions([]);
    try { setSelected(await (await request(`pages/${encodeURIComponent(id)}`)).json() as WikiPage); }
    catch (error) { setSelected(null); setMessage((error as Error).message); }
    finally { setBusy(false); }
  }
  async function search(event: FormEvent<HTMLFormElement>) {
    event.preventDefault(); setSearching(true); setBusy(true); setMessage("");
    const query = String(new FormData(event.currentTarget).get("query") ?? "");
    try {
      const data = await (await request("search", { method: "POST", body: JSON.stringify({ query, limit: 8, token_budget: 6000 }) })).json() as { hits: { page: WikiPage }[]; degraded: boolean };
      setPages(data.hits.map(hit => hit.page));
      setMessage(data.hits.length ? (data.degraded ? "语义服务暂时不可用，已显示关键词检索结果。" : "已找到相关页面，可打开核对来源。") : "没有找到足够证据。可以在下方直接检索原文。");
    } catch (error) { setMessage((error as Error).message); }
    finally { setBusy(false); }
  }
  async function save() {
    if (!selected) return; setBusy(true);
    try {
      const next = await (await request(`pages/${selected.id}`, { method: "PATCH", body: JSON.stringify({ version: selected.version, title, body }) })).json() as WikiPage;
      setSelected(next); setEditing(false); setMessage("新版本已保存，自动编译会保留你的修改。"); await load();
    } catch (error) { setMessage((error as Error).message); }
    finally { setBusy(false); }
  }
  async function feedback(rating: string) {
    if (!selected) return;
    try { await request(`pages/${selected.id}/feedback`, { method: "POST", body: JSON.stringify({ version: selected.version, rating, note: "" }) }); setMessage(rating === "helpful" ? "已记录反馈。" : "已记录问题并安排重新核对来源。"); }
    catch (error) { setMessage((error as Error).message); }
  }
  async function exportPage() {
    if (!selected) return;
    try {
      const response = await request(`pages/${selected.id}/export`);
      const url = URL.createObjectURL(await response.blob()); const anchor = document.createElement("a");
      anchor.href = url; anchor.download = `${selected.title.replace(/[\\/:*?"<>|]/g, "_")}.md`; anchor.click();
      window.setTimeout(() => URL.revokeObjectURL(url), 1000);
    } catch (error) { setMessage((error as Error).message); }
  }

  return <section className="wikiKnowledge" aria-label="Wiki 知识页面">
    <header><div><h4>知识页面</h4><p>从资料中积累主题与决策，每条内容都可追溯到原文。</p></div><button type="button" className="textButton" disabled={busy} onClick={() => { setSearching(false); void load().catch(error => setMessage(error.message)); }}>全部页面</button></header>
    <form className="documentQuery" onSubmit={search}><label>查找知识<input name="query" minLength={2} maxLength={1000} required placeholder="搜索主题、实体或决策" /></label><button disabled={busy}>搜索页面</button></form>
    <div className="wikiKinds" aria-label="页面分类">{["all", "source", "topic", "entity", "decision"].map(kind => <button type="button" key={kind} aria-pressed={filter === kind} onClick={() => setFilter(kind)}>{kinds[kind] ?? "全部"}</button>)}</div>
    {message && <p role="status" className="formMessage">{message}</p>}
    <div className="wikiColumns">
      <nav aria-label="Wiki 页面列表" className="wikiPageList">
        {!pages.length && <p className="emptyState">资料解析后会自动编译为知识页面。也可以对已解析资料点击“重建知识”。</p>}
        {pages.filter(page => filter === "all" || page.kind === filter).map(page => <div key={page.id}><button type="button" disabled={page.stale || busy} aria-current={selected?.id === page.id ? "page" : undefined} onClick={() => void open(page.id)}><small>{kinds[page.kind] ?? page.kind} · v{page.version}{page.edited ? " · 人工修订" : ""}</small><strong>{page.title}</strong><span>{page.sources?.map(source => source.name).join("、")}</span>{page.stale && <em>来源已更新，等待核对</em>}</button>{page.stale && <button type="button" disabled={busy} onClick={() => void request(`pages/${page.id}/rebuild`, { method: "POST", body: JSON.stringify({ version: page.version }) }).then(() => setMessage("已重新编译，旧修订保留在版本历史中。" )).catch(error => setMessage(error.message))}>按新来源重建</button>}</div>)}
      </nav>
      {selected && <article className="wikiReader" aria-label="Wiki 页面正文">
        <header><div><small>{kinds[selected.kind]} · v{selected.version}</small><h4>{selected.title}</h4></div><button className="textButton" type="button" onClick={() => setSelected(null)}>关闭</button></header>
        <div className="wikiActions"><button type="button" onClick={() => { setTitle(selected.title); setBody(selected.body); setEditing(true); }}>编辑</button><button type="button" onClick={() => void exportPage()}>导出 Markdown</button><button type="button" onClick={() => void request(`pages/${selected.id}/versions`).then(response => response.json()).then(data => setVersions(data.items)).catch(error => setMessage(error.message))}>版本历史</button></div>
        {!!selected.conflicts?.length && <aside className="documentNotice"><strong>来源存在冲突，尚未确认</strong>{selected.conflicts.map((conflict, i) => <p key={i}>{conflict}</p>)}</aside>}
        {editing ? <form className="wikiEditor" onSubmit={event => { event.preventDefault(); void save(); }}><label>标题<input value={title} maxLength={160} required onChange={event => setTitle(event.target.value)} /></label><label>Markdown 正文<textarea value={body} maxLength={24000} rows={14} onChange={event => setBody(event.target.value)} /></label><button disabled={busy}>保存新版本</button><button type="button" onClick={() => setEditing(false)}>取消</button></form> : <div className="wikiMarkdown">{selected.body.split("\n").map((line, i) => line.startsWith("#") ? <h5 key={i}>{line.replace(/^#+\s*/, "")}</h5> : <p key={i}>{line || "\u00a0"}</p>)}</div>}
        <details open><summary>原文证据与引用</summary>{selected.evidence?.map((evidence, i) => <blockquote key={`${evidence.chunk_id}-${i}`} id={`chunk-${evidence.chunk_id}`}><small>{selected.sources.find(source => source.document_id === evidence.document_id)?.name} · 第 {evidence.page} 页</small><p>{evidence.quote}</p><code>[chunk:{evidence.chunk_id}]</code></blockquote>)}</details>
        {!!selected.links?.length && <details><summary>相关页面（{selected.links.length}）</summary><div className="wikiActions">{selected.links.map(id => <button type="button" key={id} onClick={() => void open(id)}>{pages.find(page => page.id === id)?.title ?? "查看关联来源"}</button>)}</div></details>}
        {!!versions.length && <details open><summary>版本历史</summary>{versions.map(version => <details key={version.version}><summary>v{version.version} · {new Date(version.updated_at).toLocaleString()} {version.edited ? "人工修订" : "自动编译"}</summary><pre>{version.body}</pre></details>)}</details>}
        <div className="wikiActions" aria-label="页面质量反馈"><button type="button" onClick={() => void feedback("helpful")}>有帮助</button><button type="button" onClick={() => void feedback("incorrect")}>内容有误</button><button type="button" onClick={() => void feedback("outdated")}>内容过期</button></div>
      </article>}
    </div>
  </section>;
}
