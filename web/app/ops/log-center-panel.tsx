"use client";

import { FormEvent, useCallback, useEffect, useMemo, useState } from "react";

import { adminFetch, adminHeaders, type AdminCredentials } from "./admin-auth";
import styles from "./operations.module.css";



type Credentials = AdminCredentials;
type LogEntry = {
  id: number;
  occurred_at: string;
  service: string;
  environment: string;
  level: "DEBUG" | "INFO" | "WARN" | "ERROR";
  event: string;
  message: string;
  trace_id?: string;
  span_id?: string;
  trace_flags?: string;
  run_id?: string;
  agent_trace_id?: string;
  node?: string;
  error_code?: string;
  attributes: Record<string, unknown>;
};
type LogPage = {
  items: LogEntry[];
  next_before_id?: number;
  summary: { total: number; errors: number; warnings: number; services: number };
  services: string[];
  source?: "loki" | "postgres" | "memory";
  degraded?: boolean;
  truncated?: boolean;
};
type LogFilters = { range: string; service: string; level: string; query: string; traceID: string; runID: string };
export type LogFocus = { service?: string; level?: string; query?: string; range?: string };

const initialFilters: LogFilters = { range: "1h", service: "", level: "", query: "", traceID: "", runID: "" };
const rangeMilliseconds: Record<string, number> = { "15m": 15 * 60_000, "1h": 60 * 60_000, "6h": 6 * 60 * 60_000, "24h": 24 * 60 * 60_000, "7d": 7 * 24 * 60 * 60_000 };

export function LogCenterPanel({ credentials, onOpenRun, focus }: { credentials: Credentials; onOpenRun: (runID: string) => void; focus?: LogFocus }) {
  const focusedFilters = { ...initialFilters, range: focus?.range ?? initialFilters.range, service: focus?.service ?? "", level: focus?.level ?? "", query: focus?.query ?? "" };
  const [filters, setFilters] = useState<LogFilters>(focusedFilters);
  const [applied, setApplied] = useState<LogFilters>(focusedFilters);
  const [page, setPage] = useState<LogPage>({ items: [], summary: { total: 0, errors: 0, warnings: 0, services: 0 }, services: [], source: "memory" });
  const [expanded, setExpanded] = useState<number | null>(null);
  const [loading, setLoading] = useState(true);
  const [loadingMore, setLoadingMore] = useState(false);
  const [autoRefresh, setAutoRefresh] = useState(true);
  const [error, setError] = useState("");
  const [lastUpdated, setLastUpdated] = useState<Date | null>(null);

  const fetchLogs = useCallback(async (requested: LogFilters, beforeID?: number) => {
    const query = logQuery(requested, beforeID);
    const result = await logFetch<LogPage>(`/v1/ops/logs?${query}`, credentials);
    setPage((current) => beforeID ? { ...result, items: [...current.items, ...result.items] } : result);
    setLastUpdated(new Date());
  }, [credentials]);

  const refresh = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      await fetchLogs(applied);
    } catch (cause) {
      setError(logError(cause));
    } finally {
      setLoading(false);
    }
  }, [applied, fetchLogs]);

  useEffect(() => {
    let cancelled = false;
    void logFetch<LogPage>(`/v1/ops/logs?${logQuery(applied)}`, credentials).then((result) => {
      if (cancelled) return;
      setPage(result);
      setLastUpdated(new Date());
      setLoading(false);
    }).catch((cause: unknown) => {
      if (cancelled) return;
      setError(logError(cause));
      setLoading(false);
    });
    return () => { cancelled = true; };
  }, [applied, credentials]);

  useEffect(() => {
    if (!autoRefresh) return;
    const timer = window.setInterval(() => { void fetchLogs(applied).catch((cause: unknown) => setError(logError(cause))); }, 15_000);
    return () => window.clearInterval(timer);
  }, [applied, autoRefresh, fetchLogs]);

  function applyFilters(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setApplied(filters);
    setExpanded(null);
  }

  function clearCorrelation() {
    const next = { ...filters, traceID: "", runID: "" };
    setFilters(next);
    setApplied(next);
  }

  async function loadMore() {
    if (!page.next_before_id) return;
    setLoadingMore(true);
    setError("");
    try {
      await fetchLogs(applied, page.next_before_id);
    } catch (cause) {
      setError(logError(cause));
    } finally {
      setLoadingMore(false);
    }
  }

  const visibleServices = useMemo(() => Array.from(new Set([...page.services, ...(filters.service ? [filters.service] : [])])).sort(), [filters.service, page.services]);

  return <>
    <header className={styles.topbar}>
      <div><p>OPERATIONS / SYSTEM LOGS</p><h1>日志中心</h1></div>
      <div className={styles.actions}>
        <label className={styles.refreshToggle}><input type="checkbox" checked={autoRefresh} onChange={(event) => setAutoRefresh(event.target.checked)} /><span />15 秒刷新</label>
        <button type="button" onClick={() => void refresh()} disabled={loading}>{loading ? "同步中" : "立即刷新"}</button>
      </div>
    </header>

    {error && <div className={styles.errorBanner} role="alert"><strong>日志请求未完成</strong><span>{error}</span></div>}
    {page.degraded && <div className={styles.logDegradedBanner} role="status"><strong>Loki 查询已降级</strong><span>当前结果来自 {page.source === "postgres" ? "PostgreSQL 持久化审计日志" : "本地日志存储"}；采集恢复后会自动切回 Loki。</span></div>}

    <section className={styles.logKpis} aria-label="日志摘要">
      <LogMetric label="匹配日志" value={page.summary.total} tone="blue" />
      <LogMetric label="错误" value={page.summary.errors} tone="red" />
      <LogMetric label="警告" value={page.summary.warnings} tone="amber" />
      <LogMetric label="涉及服务" value={page.summary.services} tone="violet" />
    </section>

    <section className={styles.logPanel}>
      <div className={styles.panelHeading}>
        <div><p>STRUCTURED LOG EXPLORER</p><h2>服务日志</h2></div>
        <span className={styles.logSource} data-source={page.source}>{page.source === "loki" ? "Loki" : page.source === "postgres" ? "PostgreSQL 回退" : "本地存储"} · {lastUpdated ? `更新于 ${formatClock(lastUpdated)}` : "等待同步"}</span>
      </div>
      <form className={styles.logFilters} onSubmit={applyFilters}>
        <label className={styles.searchField}><span>⌕</span><input aria-label="搜索日志" value={filters.query} onChange={(event) => setFilters({ ...filters, query: event.target.value })} placeholder="事件、消息、错误码或属性" /></label>
        <select aria-label="时间范围" value={filters.range} onChange={(event) => setFilters({ ...filters, range: event.target.value })}>
          <option value="15m">最近 15 分钟</option><option value="1h">最近 1 小时</option><option value="6h">最近 6 小时</option><option value="24h">最近 24 小时</option><option value="7d">最近 7 天</option>
        </select>
        <select aria-label="日志服务" value={filters.service} onChange={(event) => setFilters({ ...filters, service: event.target.value })}>
          <option value="">全部服务</option>{visibleServices.map((service) => <option key={service} value={service}>{service}</option>)}
        </select>
        <select aria-label="日志级别" value={filters.level} onChange={(event) => setFilters({ ...filters, level: event.target.value })}>
          <option value="">全部级别</option><option value="ERROR">ERROR</option><option value="WARN">WARN</option><option value="INFO">INFO</option><option value="DEBUG">DEBUG</option>
        </select>
        <input aria-label="Trace ID" value={filters.traceID} onChange={(event) => setFilters({ ...filters, traceID: event.target.value })} placeholder="Trace ID" />
        <input aria-label="Run ID" value={filters.runID} onChange={(event) => setFilters({ ...filters, runID: event.target.value })} placeholder="Run ID" />
        <button type="submit">应用筛选</button>
      </form>
      {(applied.traceID || applied.runID) && <div className={styles.correlationBar}>
        <span>关联筛选</span>{applied.traceID && <code>trace · {shortID(applied.traceID)}</code>}{applied.runID && <code>run · {shortID(applied.runID)}</code>}
        <button type="button" onClick={clearCorrelation}>清除</button>
      </div>}
      <LogTable entries={page.items} loading={loading} expanded={expanded} onExpand={(id) => setExpanded(expanded === id ? null : id)} onOpenRun={onOpenRun} />
      {page.next_before_id && <div className={styles.loadMore}><button type="button" disabled={loadingMore} onClick={() => void loadMore()}>{loadingMore ? "载入中…" : "加载更早日志"}</button></div>}
    </section>
    <p className={styles.logSafetyNote}>日志在写入前执行字段脱敏与长度限制；管理台不保存管理密钥，也不会记录 Prompt、响应正文或请求体。</p>
  </>;
}

function LogMetric({ label, value, tone }: { label: string; value: number; tone: string }) {
  return <article className={`${styles.logMetric} ${styles[tone]}`}><span>{label}<i /></span><strong>{new Intl.NumberFormat("zh-CN").format(value)}</strong></article>;
}

function LogTable({ entries, loading, expanded, onExpand, onOpenRun }: { entries: LogEntry[]; loading: boolean; expanded: number | null; onExpand: (id: number) => void; onOpenRun: (runID: string) => void }) {
  if (loading && !entries.length) return <div className={styles.emptyState}><strong>正在装载日志</strong><span>读取持久化结构化记录…</span></div>;
  if (!entries.length) return <div className={styles.emptyState}><strong>当前范围没有日志</strong><span>可扩大时间范围或清除筛选条件</span></div>;
  return <div className={styles.tableScroll}><table className={styles.logTable}>
    <thead><tr><th>时间</th><th>级别</th><th>服务</th><th>事件 / 消息</th><th>关联</th><th /></tr></thead>
    <tbody>{entries.map((entry) => <LogRows key={entry.id} entry={entry} expanded={expanded === entry.id} onExpand={() => onExpand(entry.id)} onOpenRun={onOpenRun} />)}</tbody>
  </table></div>;
}

function LogRows({ entry, expanded, onExpand, onOpenRun }: { entry: LogEntry; expanded: boolean; onExpand: () => void; onOpenRun: (runID: string) => void }) {
  return <>
    <tr className={expanded ? styles.selectedRow : undefined}>
      <td><strong>{formatClock(new Date(entry.occurred_at))}</strong><small>{formatDate(entry.occurred_at)}</small></td>
      <td><span className={styles.logLevel} data-level={entry.level}>{entry.level}</span></td>
      <td><strong className={styles.logService}>{entry.service}</strong><small>{entry.environment}</small></td>
      <td><strong className={styles.logEvent}>{entry.event}</strong><small className={styles.logMessage}>{entry.message}{entry.error_code ? ` · ${entry.error_code}` : ""}</small></td>
      <td><div className={styles.logLinks}>{entry.trace_id && <code title={`分布式 Trace · ${entry.trace_id}`}>T {shortID(entry.trace_id)}</code>}{entry.agent_trace_id && <code title={`Langfuse Agent Trace · ${entry.agent_trace_id}`}>A {shortID(entry.agent_trace_id)}</code>}{entry.run_id && <button type="button" title={entry.run_id} onClick={() => onOpenRun(entry.run_id!)}>R {shortID(entry.run_id)}</button>}{entry.node && <small>{entry.node}</small>}</div></td>
      <td><button type="button" className={styles.logExpand} aria-label={`${expanded ? "收起" : "展开"}日志 ${entry.id}`} onClick={onExpand}>{expanded ? "−" : "+"}</button></td>
    </tr>
    {expanded && <tr className={styles.logDetailRow}><td colSpan={6}><div><span><b>Trace</b><code>{entry.trace_id || "—"}</code></span><span><b>Span</b><code>{entry.span_id || "—"}</code></span><span><b>采样标记</b><code>{entry.trace_flags || "—"}</code></span><span><b>Agent Trace</b><code>{entry.agent_trace_id || "—"}</code></span><span><b>Run</b><code>{entry.run_id || "—"}</code></span><span><b>节点</b><code>{entry.node || "—"}</code></span><span><b>错误码</b><code>{entry.error_code || "—"}</code></span></div><pre>{JSON.stringify(entry.attributes ?? {}, null, 2)}</pre></td></tr>}
  </>;
}

function logQuery(filters: LogFilters, beforeID?: number) {
  const query = new URLSearchParams({ limit: "100", since: new Date(Date.now() - (rangeMilliseconds[filters.range] ?? rangeMilliseconds["1h"])).toISOString() });
  if (filters.service) query.set("service", filters.service);
  if (filters.level) query.set("level", filters.level);
  if (filters.query.trim()) query.set("q", filters.query.trim());
  if (filters.traceID.trim()) query.set("trace_id", filters.traceID.trim());
  if (filters.runID.trim()) query.set("run_id", filters.runID.trim());
  if (beforeID) query.set("before_id", String(beforeID));
  return query;
}

async function logFetch<T>(path: string, credentials: Credentials): Promise<T> {
  const response = await adminFetch(`${path}`, { headers: { ...adminHeaders(credentials) }, cache: "no-store" });
  const payload = await response.json().catch(() => ({})) as { message?: string };
  if (!response.ok) throw new Error(payload.message || `请求失败（${response.status}）`);
  return payload as T;
}

function logError(cause: unknown) { return cause instanceof Error ? cause.message : "日志服务暂时不可用"; }
function shortID(value: string) { return value.length > 15 ? `${value.slice(0, 8)}…${value.slice(-4)}` : value; }
function formatClock(value: Date) { return new Intl.DateTimeFormat("zh-CN", { hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false }).format(value); }
function formatDate(value: string) { return new Intl.DateTimeFormat("zh-CN", { month: "2-digit", day: "2-digit", year: "numeric" }).format(new Date(value)); }
