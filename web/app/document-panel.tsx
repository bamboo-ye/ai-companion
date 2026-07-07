"use client";

import { FormEvent, useCallback, useEffect, useState } from "react";

const apiBase = process.env.NEXT_PUBLIC_API_BASE_URL ?? "http://localhost:8080";

type DocumentItem = {
  id: string;
  job_id: string;
  name: string;
  media_type: string;
  size_bytes: number;
  status: "queued" | "processing" | "ready" | "failed";
  page_count: number;
  chunk_count: number;
  failure_code?: string;
};

type QueryResult = {
  answer: string;
  sufficient: boolean;
  citations: Array<{
    chunk_id: string;
    document_name: string;
    page_start: number;
    page_end: number;
    section_path?: string;
    quote: string;
  }>;
};

export function DocumentPanel({ token, onClose }: { token: string; onClose: () => void }) {
  const [items, setItems] = useState<DocumentItem[]>([]);
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);
  const [queryResult, setQueryResult] = useState<QueryResult | null>(null);

  const load = useCallback(async () => {
    const response = await fetch(`${apiBase}/v1/documents`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    if (!response.ok) throw new Error("无法加载文档");
    setItems(((await response.json()) as { items: DocumentItem[] }).items);
  }, [token]);

  useEffect(() => {
    queueMicrotask(() => void load().catch((error: Error) => setMessage(error.message)));
  }, [load]);

  useEffect(() => {
    if (!items.some((item) => item.status === "queued" || item.status === "processing")) return;
    const timer = window.setInterval(() => {
      void load().catch((error: Error) => setMessage(error.message));
    }, 1500);
    return () => window.clearInterval(timer);
  }, [items, load]);

  async function upload(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = event.currentTarget;
    const input = form.elements.namedItem("file") as HTMLInputElement;
    if (!input.files?.[0]) return;
    setBusy(true);
    setMessage("");
    try {
      const body = new FormData();
      body.append("file", input.files[0]);
      const response = await fetch(`${apiBase}/v1/documents`, {
        method: "POST",
        headers: { Authorization: `Bearer ${token}` },
        body,
      });
      const payload = (await response.json()) as { deduplicated?: boolean; message?: string };
      if (!response.ok) throw new Error(payload.message ?? "上传失败");
      form.reset();
      setMessage(payload.deduplicated ? "相同内容已存在，没有重复创建任务。" : "文件已安全保存并进入解析队列。");
      await load();
    } catch (error) {
      setMessage(error instanceof Error ? error.message : "上传失败");
    } finally {
      setBusy(false);
    }
  }

  async function remove(item: DocumentItem) {
    if (!window.confirm(`确定删除“${item.name}”吗？删除后将无法继续检索此文档。`)) return;
    try {
      const response = await fetch(`${apiBase}/v1/documents/${item.id}`, {
        method: "DELETE",
        headers: { Authorization: `Bearer ${token}` },
      });
      if (!response.ok) throw new Error("删除失败");
      setMessage("文档已删除。");
      await load();
    } catch (error) {
      setMessage(error instanceof Error ? error.message : "删除失败");
    }
  }

  async function queryDocuments(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = event.currentTarget;
    const query = String(new FormData(form).get("query") ?? "").trim();
    if (!query) return;
    setBusy(true);
    setMessage("");
    try {
      const response = await fetch(`${apiBase}/v1/documents/query`, {
        method: "POST",
        headers: { "Content-Type": "application/json", Authorization: `Bearer ${token}` },
        body: JSON.stringify({ query, limit: 5 }),
      });
      const payload = (await response.json()) as QueryResult & { message?: string };
      if (!response.ok) throw new Error(payload.message ?? "检索失败");
      setQueryResult(payload);
    } catch (error) {
      setMessage(error instanceof Error ? error.message : "检索失败");
    } finally {
      setBusy(false);
    }
  }

  return (
    <section className="documentPanel" aria-label="文档库">
      <header>
        <div>
          <small>M2 · 文档摄取</small>
          <h3>我的文档</h3>
        </div>
        <button className="textButton" type="button" onClick={onClose}>返回角色</button>
      </header>
      <form className="documentUpload" onSubmit={upload}>
        <label>
          选择文本 PDF 或 UTF-8 文本文件
          <input name="file" type="file" accept=".pdf,.txt,application/pdf,text/plain" required />
        </label>
        <button type="submit" disabled={busy}>{busy ? "正在检查并保存…" : "上传文档"}</button>
      </form>
      <div className="documentNotice">解析完成后可基于页级片段检索；回答必须显示文档名和页码，证据不足时不会猜测。</div>
      {message && <p className="formMessage" role="status">{message}</p>}
      <div className="documentList">
        {items.length === 0 ? <p className="emptyState">还没有文档。</p> : items.map((item) => (
          <article key={item.id}>
            <div>
              <strong>{item.name}</strong>
              <small>{formatBytes(item.size_bytes)} · {item.media_type}</small>
              <p>{statusLabels[item.status]}{item.status === "ready" ? ` · ${item.page_count} 页 / ${item.chunk_count} 个片段` : ""}</p>
            </div>
            <button className="dangerButton" type="button" onClick={() => void remove(item)}>删除</button>
          </article>
        ))}
      </div>
      <form className="documentQuery" onSubmit={queryDocuments}>
        <label>
          向已解析文档提问
          <input name="query" minLength={2} maxLength={1000} required placeholder="例如：项目计划什么时候启动？" />
        </label>
        <button type="submit" disabled={busy || !items.some((item) => item.status === "ready")}>检索证据</button>
      </form>
      {queryResult && (
        <section className={`queryResult ${queryResult.sufficient ? "sufficient" : "insufficient"}`} aria-label="文档检索结果">
          <p>{queryResult.answer}</p>
          {queryResult.citations.length > 0 && (
            <ol>
              {queryResult.citations.map((citation) => (
                <li key={citation.chunk_id}>
                  <strong>《{citation.document_name}》第 {citation.page_start}{citation.page_end > citation.page_start ? `–${citation.page_end}` : ""} 页</strong>
                  <span>{citation.quote}</span>
                </li>
              ))}
            </ol>
          )}
        </section>
      )}
    </section>
  );
}

const statusLabels: Record<DocumentItem["status"], string> = {
  queued: "等待解析",
  processing: "正在解析",
  ready: "可检索",
  failed: "解析失败",
};

function formatBytes(value: number) {
  if (value < 1024) return `${value} B`;
  if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KB`;
  return `${(value / 1024 / 1024).toFixed(1)} MB`;
}
