"use client";

import { FormEvent, useCallback, useEffect, useState } from "react";

import { adminFetch, adminHeaders, type AdminCredentials } from "./admin-auth";
import styles from "./operations.module.css";



type Credentials = AdminCredentials;
type Operator = { actor: string; role: string; mfa_verified: boolean; legacy: boolean };
type User = { id: string; email: string; display_name: string; status: string };
type UsageItem = { resource: string; actual: number; adjustment: number; used: number; limit: number; remaining: number; period_start?: string; period_end?: string };
type BillingSummary = { plan: { code: string; display_name: string }; subscription: { status: string; current_period_start: string; current_period_end: string }; usage: UsageItem[] };
type Adjustment = { id: string; resource: string; delta: number; reason: string; actor: string; period_start?: string; period_end?: string; created_at: string };

const resourceLabels: Record<string, string> = {
  documents: "活跃文档",
  skill_runs: "Skill Runs",
  workspaces: "工作区",
  agent_runs: "Agent Runs",
  model_cost_micros: "模型成本",
};

export function BillingUsagePanel({ credentials, operator }: { credentials: Credentials; operator: Operator }) {
  const [query, setQuery] = useState("");
  const [users, setUsers] = useState<User[]>([]);
  const [selected, setSelected] = useState<User | null>(null);
  const [summary, setSummary] = useState<BillingSummary | null>(null);
  const [adjustments, setAdjustments] = useState<Adjustment[]>([]);
  const [resource, setResource] = useState("agent_runs");
  const [delta, setDelta] = useState("");
  const [reason, setReason] = useState("");
  const [loading, setLoading] = useState(true);
  const [working, setWorking] = useState(false);
  const [error, setError] = useState("");
  const [message, setMessage] = useState("");

  const loadLedger = useCallback(async (user: User) => {
    setLoading(true);
    setError("");
    try {
      const [nextSummary, page] = await Promise.all([
        billingFetch<BillingSummary>(`/v1/ops/billing/users/${user.id}/usage`, credentials),
        billingFetch<{ adjustments: Adjustment[] }>(`/v1/ops/billing/users/${user.id}/adjustments?limit=100`, credentials),
      ]);
      setSummary(nextSummary);
      setAdjustments(page.adjustments);
    } catch (cause) {
      setError(billingError(cause));
      setSummary(null);
      setAdjustments([]);
    } finally {
      setLoading(false);
    }
  }, [credentials]);

  useEffect(() => {
    let cancelled = false;
    void billingFetch<{ users: User[] }>("/v1/ops/users?limit=30", credentials).then((page) => {
      if (cancelled) return;
      setUsers(page.users);
      const initial = page.users[0] ?? null;
      setSelected(initial);
      if (initial) void loadLedger(initial);
      else setLoading(false);
    }).catch((cause: unknown) => {
      if (cancelled) return;
      setError(billingError(cause));
      setLoading(false);
    });
    return () => { cancelled = true; };
  }, [credentials, loadLedger]);

  async function search(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
	setLoading(true);
	setError("");
	try {
	  const page = await billingFetch<{ users: User[] }>(`/v1/ops/users?limit=30&q=${encodeURIComponent(query.trim())}`, credentials);
	  setUsers(page.users);
	  const next = selected && page.users.some((item) => item.id === selected.id) ? selected : page.users[0] ?? null;
	  setSelected(next);
	  if (next) await loadLedger(next);
	  else { setSummary(null); setAdjustments([]); }
	} catch (cause) {
	  setError(billingError(cause));
	} finally {
	  setLoading(false);
	}
  }

  async function adjust(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!selected) return;
    setWorking(true);
    setError("");
    setMessage("");
    try {
      const result = await billingFetch<{ summary: BillingSummary }>(`/v1/ops/billing/users/${selected.id}/adjustments`, credentials, {
        method: "POST", body: { resource, delta: Number(delta), reason },
      });
      setSummary(result.summary);
      const page = await billingFetch<{ adjustments: Adjustment[] }>(`/v1/ops/billing/users/${selected.id}/adjustments?limit=100`, credentials);
      setAdjustments(page.adjustments);
      setDelta("");
      setReason("");
      setMessage("额度调整已写入不可变账本并记录审计日志");
    } catch (cause) {
      setError(billingError(cause));
    } finally {
      setWorking(false);
    }
  }

  return <section className={styles.usageWorkspace}>
    {error && <div className={styles.errorBanner} role="alert"><strong>用量账本操作未完成</strong><span>{error}</span></div>}
    {message && <div className={styles.successBanner} role="status">{message}</div>}
    <div className={styles.usageLayout}>
      <article className={styles.configListCard}>
        <div className={styles.panelHeading}><div><p>ACCOUNT LOOKUP</p><h2>选择用户</h2></div><span>{users.length} 条</span></div>
        <form className={styles.usageSearch} onSubmit={search}><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="邮箱、昵称或用户 ID" /><button disabled={loading}>搜索</button></form>
        <div className={styles.usageUsers}>{users.map((user) => <button type="button" key={user.id} data-selected={selected?.id === user.id} onClick={() => { setSelected(user); void loadLedger(user); }}><span><strong>{user.display_name || user.email}</strong><small>{user.email}</small></span><b>{user.status}</b></button>)}</div>
        {!loading && users.length === 0 && <div className={styles.emptyLine}>没有匹配的用户</div>}
      </article>

      <div className={styles.usageDetail}>
        <article className={styles.editorCard}>
          <div className={styles.panelHeading}><div><p>UNIFIED USAGE</p><h2>{selected ? selected.display_name || selected.email : "用量明细"}</h2></div><span>{summary ? `${summary.plan.display_name} · ${summary.plan.code}` : "—"}</span></div>
          {summary ? <div className={styles.usageCards}>{summary.usage.map((item) => <UsageCard key={item.resource} item={item} />)}</div> : <div className={styles.emptyLine}>{loading ? "正在读取统一账本…" : "请选择用户"}</div>}
        </article>

        <article className={styles.editorCard}>
          <div className={styles.panelHeading}><div><p>APPEND-ONLY ADJUSTMENT</p><h2>人工调整</h2></div><span>{operator.mfa_verified || operator.legacy ? "受控操作" : "需要 MFA"}</span></div>
          <form className={styles.configForm} onSubmit={adjust}>
            <label>资源<select value={resource} onChange={(event) => setResource(event.target.value)}>{Object.entries(resourceLabels).map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></label>
            <label>调整量<input type="number" value={delta} onChange={(event) => setDelta(event.target.value)} required placeholder="正数增加用量，负数抵扣" /></label>
            <label className={styles.fullField}>调整原因<textarea value={reason} onChange={(event) => setReason(event.target.value)} required maxLength={512} placeholder="工单、补偿依据或纠错说明" /></label>
            <button className={styles.primaryWide} disabled={!selected || operator.role !== "admin" || (!operator.mfa_verified && !operator.legacy) || working}>{working ? "正在写入…" : "写入调整账本"}</button>
          </form>
        </article>

        <article className={styles.configListCard}>
          <div className={styles.panelHeading}><div><p>AUDITABLE HISTORY</p><h2>调整记录</h2></div><span>{adjustments.length} 条</span></div>
          <div className={styles.adjustmentList}>{adjustments.map((item) => <div key={item.id}><span><strong>{resourceLabels[item.resource] ?? item.resource}</strong><small>{item.reason}</small></span><b data-positive={item.delta > 0}>{item.delta > 0 ? "+" : ""}{formatUsage(item.resource, item.delta)}</b><em>{item.actor}<small>{formatDate(item.created_at)}</small></em></div>)}</div>
          {!loading && adjustments.length === 0 && <div className={styles.emptyLine}>暂无人工调整</div>}
        </article>
      </div>
    </div>
  </section>;
}

function UsageCard({ item }: { item: UsageItem }) {
  const unlimited = item.limit < 0;
  const ratio = unlimited || item.limit === 0 ? 0 : Math.min(100, Math.round(item.used / item.limit * 100));
  return <div className={styles.usageCard}><span><small>{resourceLabels[item.resource] ?? item.resource}</small><strong>{formatUsage(item.resource, item.used)} <em>/ {unlimited ? "不限" : formatUsage(item.resource, item.limit)}</em></strong></span><div><i style={{ width: `${ratio}%` }} /></div><p>实际 {formatUsage(item.resource, item.actual)} · 调整 {item.adjustment > 0 ? "+" : ""}{formatUsage(item.resource, item.adjustment)}</p></div>;
}

async function billingFetch<T>(path: string, credentials: Credentials, options?: { method?: string; body?: unknown }): Promise<T> {
  const headers: Record<string, string> = { ...adminHeaders(credentials) };
  if (options?.body !== undefined) headers["Content-Type"] = "application/json";
  const response = await adminFetch(`${path}`, { method: options?.method ?? "GET", headers, body: options?.body === undefined ? undefined : JSON.stringify(options.body), cache: "no-store" });
  const payload = await response.json().catch(() => ({})) as { message?: string };
  if (!response.ok) throw new Error(payload.message || `请求失败（${response.status}）`);
  return payload as T;
}

function formatUsage(resource: string, value: number) { return resource === "model_cost_micros" ? `$${(value / 1_000_000).toFixed(4)}` : new Intl.NumberFormat("zh-CN").format(value); }
function formatDate(value: string) { return new Intl.DateTimeFormat("zh-CN", { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit" }).format(new Date(value)); }
function billingError(cause: unknown) { return cause instanceof Error ? cause.message : "用量账本暂时不可用"; }
