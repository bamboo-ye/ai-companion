"use client";

import { FormEvent, useCallback, useEffect, useState } from "react";
import { adminFetch, adminHeaders, type AdminCredentials } from "./admin-auth";
import { knowledgeKinds, type KnowledgeRef } from "../knowledge-types";
import styles from "./operations.module.css";

type Feedback = { id: string; ref: KnowledgeRef; rating: string; note: string; created_at: string };
type Metrics = { contract: string; projections: Record<string, number>; index_spec: { model: string; revision: string } };
export function KnowledgePanel({ credentials, canReview, canRebuild }: { credentials: AdminCredentials; canReview: boolean; canRebuild: boolean }) {
  const [metrics, setMetrics] = useState<Metrics | null>(null);
  const [items, setItems] = useState<Feedback[]>([]);
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);
  const request = useCallback(async <T,>(path: string, body?: unknown): Promise<T> => {
    const response = await adminFetch(`/v1/ops/knowledge/${path}`, { method: body === undefined ? "GET" : "POST", headers: { ...adminHeaders(credentials), "Content-Type": "application/json" }, body: body === undefined ? undefined : JSON.stringify(body), cache: "no-store" });
    const data = await response.json(); if (!response.ok) throw new Error(data.message ?? "知识运维暂不可用"); return data as T;
  }, [credentials]);
  const load = useCallback(async () => {
    const [next, pending] = await Promise.all([request<Metrics>("metrics"), canReview ? request<{ items: Feedback[] }>("feedback") : Promise.resolve({ items: [] })]);
    return { next, pending };
  }, [request, canReview]);
  useEffect(() => { let active = true; void load().then(({ next, pending }) => { if (active) { setMetrics(next); setItems(pending.items); } }).catch(error => { if (active) setMessage(error.message); }); return () => { active = false; }; }, [load]);
  async function retry() {
    setBusy(true); setMessage("");
    try { const result = await request<{ retried: number }>("retry", {}); const { next, pending } = await load(); setMetrics(next); setItems(pending.items); setMessage(`已重新排队 ${result.retried} 项失败任务。`); }
    catch (error) { setMessage((error as Error).message); } finally { setBusy(false); }
  }
  async function rebuild() {
    setBusy(true); setMessage("");
    try { const result = await request<{ requeued: number }>("rebuild", {}); const { next, pending } = await load(); setMetrics(next); setItems(pending.items); setMessage(`已排队重建 ${result.requeued} 项当前索引，后台会逐步恢复。`); }
    catch (error) { setMessage((error as Error).message); } finally { setBusy(false); }
  }
  async function resolve(event: FormEvent<HTMLFormElement>, id: string) {
    event.preventDefault(); const resolution = String(new FormData(event.currentTarget).get("resolution") ?? ""); setBusy(true); setMessage("");
    try { await request(`feedback/${encodeURIComponent(id)}/resolve`, { resolution }); setItems(current => current.filter(item => item.id !== id)); setMessage("复核结果已保存。内容修正请通过对应资料或记忆管理完成。"); }
    catch (error) { setMessage((error as Error).message); } finally { setBusy(false); }
  }
  return <section><header className={styles.topbar}><div><p>KNOWLEDGE</p><h1>知识质量</h1><p>查看知识加工进度，复核来源反馈。</p></div>{canReview && <button type="button" disabled={busy} onClick={() => void retry()}>重试失败任务</button>}</header>
    {message && <p role="status">{message}</p>}
    {canRebuild && <p><button type="button" disabled={busy} onClick={() => void rebuild()}>重建当前检索索引</button> 用于索引丢失或需要重新加工，可能产生模型用量。</p>}
    {metrics && <><p>语义模型：{metrics.index_spec.model || "未启用"} · 版本：{metrics.index_spec.revision}</p><dl className="wikiActions">{Object.entries({ queued: "等待加工", processing: "正在加工", ready: "已就绪", failed: "需重试" }).map(([key, label]) => <div key={key}><dt>{label}</dt><dd>{metrics.projections[key] ?? 0}</dd></div>)}</dl></>}
    {canReview && <><h2>待复核反馈</h2><p>每次显示最早的 100 条。完成当前批次后重新打开此页即可继续处理。</p>{items.length === 0 && <p>暂无待复核反馈。</p>}{items.map(item => <article key={item.id} className="wikiReader"><h3>{knowledgeKinds[item.ref.kind]} · {({ helpful: "有帮助", incorrect: "内容有误", outdated: "已经过期", missing: "缺少信息", unsupported: "引用不支持结论", permission: "可见范围问题" } as Record<string, string>)[item.rating]}</h3><p>{item.note || "用户未补充说明"}</p><small>来源 {item.ref.source_id} · {new Date(item.created_at).toLocaleString("zh-CN")}</small><form className="documentQuery" onSubmit={event => void resolve(event, item.id)}><label>复核结论<input name="resolution" required minLength={2} maxLength={1000} placeholder="记录核对结果及已采取的修正" /></label><button disabled={busy}>完成复核</button></form></article>)}</>}
  </section>;
}
