"use client";

import { FormEvent, useCallback, useEffect, useState } from "react";

const apiBase = process.env.NEXT_PUBLIC_API_BASE_URL ?? "http://localhost:8080";

type Reminder = {
  id: string; title: string; local_due?: string; timezone: string; recurrence: string;
  needs_clarification: string[]; status: string; system_sync_status: string;
};
type Plan = { id: string; title: string; local_date: string; items: { id: string; title: string; priority: string; status: string }[] };
type LedgerCandidate = {
  id: string; amount_minor?: number; direction?: string; category: string; merchant?: string;
  needs_clarification: string[]; status: string;
};
type LedgerEntry = { id: string; amount_minor: number; direction: string; category: string; merchant?: string; occurred_at: string };
type LedgerSummary = { income_minor: number; expense_minor: number; balance_minor: number; entry_count: number };
type LedgerExport = { id: string; status: string; file_name?: string; failure_code?: string };

export function LifePanel({ token, onClose }: { token: string; onClose: () => void }) {
  const [reminders, setReminders] = useState<Reminder[]>([]);
  const [plans, setPlans] = useState<Plan[]>([]);
  const [entries, setEntries] = useState<LedgerEntry[]>([]);
  const [summary, setSummary] = useState<LedgerSummary | null>(null);
  const [reminderCandidate, setReminderCandidate] = useState<Reminder | null>(null);
  const [ledgerCandidate, setLedgerCandidate] = useState<LedgerCandidate | null>(null);
  const [notice, setNotice] = useState("");
  const [busy, setBusy] = useState("");
  const timezone = Intl.DateTimeFormat().resolvedOptions().timeZone;
  const today = new Date().toLocaleDateString("en-CA", { timeZone: timezone });
  const month = today.slice(0, 7);

  const request = useCallback(async <T,>(path: string, init?: RequestInit): Promise<T> => {
    const response = await fetch(`${apiBase}${path}`, {
      ...init,
      headers: { "Content-Type": "application/json", Authorization: `Bearer ${token}`, ...init?.headers },
    });
    if (response.status === 204) return undefined as T;
    const payload = await response.json() as T & { message?: string };
    if (!response.ok) throw new Error(payload.message ?? "操作失败，请稍后重试");
    return payload;
  }, [token]);

  const refresh = useCallback(async () => {
    const [reminderPayload, planPayload, entryPayload, summaryPayload] = await Promise.all([
      request<{ items: Reminder[] }>("/v1/reminders"),
      request<{ items: Plan[] }>(`/v1/plans?date=${today}`),
      request<{ items: LedgerEntry[] }>("/v1/ledger/entries?limit=20"),
      request<LedgerSummary>(`/v1/ledger/summary?month=${month}&timezone=${encodeURIComponent(timezone)}&currency=CNY`),
    ]);
    setReminders(reminderPayload.items); setPlans(planPayload.items);
    setEntries(entryPayload.items); setSummary(summaryPayload);
  }, [month, request, timezone, today]);

  useEffect(() => {
    queueMicrotask(() => { void refresh().catch((error: Error) => setNotice(error.message)); });
  }, [refresh]);

  async function parseReminder(event: FormEvent<HTMLFormElement>) {
    event.preventDefault(); const form = event.currentTarget; const data = new FormData(form);
    setBusy("reminder"); setNotice("");
    try {
      const candidate = await request<Reminder>("/v1/reminders/candidates", { method: "POST", body: JSON.stringify({ text: data.get("text"), timezone }) });
      setReminderCandidate(candidate); setNotice(candidate.needs_clarification.length ? "还缺少明确时间，请补充后重新解析。" : "请核对时间，确认后才会创建提醒。");
    } catch (error) { setNotice(error instanceof Error ? error.message : "提醒解析失败"); }
    finally { setBusy(""); }
  }

  async function confirmReminder() {
    if (!reminderCandidate) return; setBusy("reminder-confirm");
    try {
      await request(`/v1/reminders/${reminderCandidate.id}/confirm`, { method: "POST", headers: { "Idempotency-Key": crypto.randomUUID() }, body: "{}" });
      setReminderCandidate(null); setNotice("应用内提醒已创建；系统提醒会在移动端授权后同步。"); await refresh();
    } catch (error) { setNotice(error instanceof Error ? error.message : "确认失败"); }
    finally { setBusy(""); }
  }

  async function completeReminder(id: string) {
    setBusy(id); try { await request(`/v1/reminders/${id}/complete`, { method: "POST" }); await refresh(); }
    catch (error) { setNotice(error instanceof Error ? error.message : "完成提醒失败"); } finally { setBusy(""); }
  }

  async function createPlan(event: FormEvent<HTMLFormElement>) {
    event.preventDefault(); const form = event.currentTarget; const data = new FormData(form); setBusy("plan");
    try {
      await request("/v1/plans", { method: "POST", body: JSON.stringify({ title: data.get("title"), local_date: data.get("date"), timezone, items: [{ title: data.get("item"), priority: "medium", estimated_minutes: 30, source: "web" }] }) });
      form.reset(); setNotice("计划已创建。"); await refresh();
    } catch (error) { setNotice(error instanceof Error ? error.message : "创建计划失败"); } finally { setBusy(""); }
  }

  async function parseLedger(event: FormEvent<HTMLFormElement>) {
    event.preventDefault(); const data = new FormData(event.currentTarget); setBusy("ledger");
    try {
      const candidate = await request<LedgerCandidate>("/v1/ledger/candidates", { method: "POST", body: JSON.stringify({ text: data.get("text"), timezone }) });
      setLedgerCandidate(candidate); setNotice(candidate.needs_clarification.length ? "账目还不完整，请补充金额、收支方向或日期。" : "请核对候选账目，确认后才会入账。");
    } catch (error) { setNotice(error instanceof Error ? error.message : "账目解析失败"); } finally { setBusy(""); }
  }

  async function confirmLedger() {
    if (!ledgerCandidate) return; setBusy("ledger-confirm");
    try {
      await request(`/v1/ledger/candidates/${ledgerCandidate.id}/confirm`, { method: "POST", headers: { "Idempotency-Key": crypto.randomUUID() }, body: "{}" });
      setLedgerCandidate(null); setNotice("账目已确认入账。"); await refresh();
    } catch (error) { setNotice(error instanceof Error ? error.message : "确认入账失败"); } finally { setBusy(""); }
  }

  async function exportLedger() {
    setBusy("export");
    try {
      const response = await fetch(`${apiBase}/v1/ledger/exports`, { method: "POST", headers: { "Content-Type": "application/json", Authorization: `Bearer ${token}`, "Idempotency-Key": crypto.randomUUID() }, body: JSON.stringify({ month, timezone, currency: "CNY" }) });
      if (!response.ok) { const error = await response.json() as { message?: string }; throw new Error(error.message ?? "导出失败"); }
      const envelope = await response.json() as { export: LedgerExport };
      setNotice("账本正在后台生成…");
      let job = envelope.export;
      for (let attempt = 0; attempt < 120 && job.status !== "completed"; attempt += 1) {
        if (job.status === "failed") throw new Error(job.failure_code ?? "导出失败");
        await delay(250);
        job = await request<LedgerExport>(`/v1/ledger/exports/${job.id}`);
      }
      if (job.status !== "completed") throw new Error("导出仍在处理中，请稍后重试");
      const fileResponse = await fetch(`${apiBase}/v1/ledger/exports/${job.id}/file`, { headers: { Authorization: `Bearer ${token}` } });
      if (!fileResponse.ok) throw new Error("导出文件暂不可用");
      const url = URL.createObjectURL(await fileResponse.blob()); const anchor = document.createElement("a");
      anchor.href = url; anchor.download = job.file_name ?? `伴AI账本-${month}.xlsx`; anchor.click(); URL.revokeObjectURL(url);
      setNotice("账本导出完成。");
    } catch (error) { setNotice(error instanceof Error ? error.message : "导出失败"); } finally { setBusy(""); }
  }

  return <section className="lifePanel" aria-labelledby="life-title">
    <header><div><h3 id="life-title">生活助手</h3><small>计划、提醒和账本都先确认，再执行</small></div><button className="textButton" type="button" onClick={onClose}>返回</button></header>
    {notice && <p className="lifeNotice" role="status">{notice}</p>}
    <div className="lifeGrid">
      <article className="lifeSection"><h4>一句话设提醒</h4><form onSubmit={parseReminder}><input name="text" required placeholder="明早九点提醒我交报告"/><button disabled={busy !== ""}>解析</button></form>
        {reminderCandidate && <div className="candidateCard"><strong>{reminderCandidate.title || "待补充标题"}</strong><span>{reminderCandidate.local_due || "时间待补充"} · {reminderCandidate.timezone}</span>{reminderCandidate.needs_clarification.length > 0 && <small>待补充：{reminderCandidate.needs_clarification.join("、")}</small>}<button disabled={reminderCandidate.needs_clarification.length > 0 || busy !== ""} onClick={confirmReminder}>确认创建</button></div>}
        <div className="compactList">{reminders.filter((item) => item.status === "active").map((item) => <div key={item.id}><span><strong>{item.title}</strong><small>{item.local_due} · {syncLabel(item.system_sync_status)}</small></span><button disabled={busy !== ""} onClick={() => completeReminder(item.id)}>完成</button></div>)}</div>
      </article>
      <article className="lifeSection"><h4>今日计划</h4><form className="stackForm" onSubmit={createPlan}><input name="title" required placeholder="计划名称"/><input name="date" required type="date" defaultValue={today}/><input name="item" required placeholder="第一件要做的事"/><button disabled={busy !== ""}>创建计划</button></form>
        <div className="compactList">{plans.map((plan) => <div className="planRow" key={plan.id}><span><strong>{plan.title}</strong><small>{plan.local_date} · {plan.items.length} 项</small></span></div>)}</div>
      </article>
      <article className="lifeSection ledgerSection"><div className="sectionTitle"><div><h4>确认式账本</h4><small>{month} · {summary?.entry_count ?? 0} 笔</small></div><button disabled={busy !== ""} onClick={exportLedger}>导出 Excel</button></div>
        <div className="summaryStrip"><span>收入<strong>{money(summary?.income_minor ?? 0)}</strong></span><span>支出<strong>{money(summary?.expense_minor ?? 0)}</strong></span><span>结余<strong>{money(summary?.balance_minor ?? 0)}</strong></span></div>
        <form onSubmit={parseLedger}><input name="text" required placeholder="昨晚打车36元"/><button disabled={busy !== ""}>解析</button></form>
        {ledgerCandidate && <div className="candidateCard"><strong>{ledgerCandidate.direction === "income" ? "收入" : "支出"} {money(ledgerCandidate.amount_minor ?? 0)}</strong><span>{ledgerCandidate.category}{ledgerCandidate.merchant ? ` · ${ledgerCandidate.merchant}` : ""}</span>{ledgerCandidate.needs_clarification.length > 0 && <small>待补充：{ledgerCandidate.needs_clarification.join("、")}</small>}<button disabled={ledgerCandidate.needs_clarification.length > 0 || busy !== ""} onClick={confirmLedger}>确认入账</button></div>}
        <div className="compactList">{entries.slice(0, 6).map((item) => <div key={item.id}><span><strong>{item.category}{item.merchant ? ` · ${item.merchant}` : ""}</strong><small>{new Date(item.occurred_at).toLocaleString()}</small></span><b className={item.direction}>{item.direction === "income" ? "+" : "−"}{money(item.amount_minor)}</b></div>)}</div>
      </article>
    </div>
  </section>;
}

function money(minor: number) { return new Intl.NumberFormat("zh-CN", { style: "currency", currency: "CNY" }).format(minor / 100); }
function delay(milliseconds: number) { return new Promise((resolve) => window.setTimeout(resolve, milliseconds)); }
function syncLabel(status: string) {
  return ({ synced: "已同步系统提醒", permission_denied: "未获系统权限，应用内仍有效", failed: "系统同步失败，应用内仍有效", not_requested: "应用内提醒" } as Record<string, string>)[status] ?? "应用内提醒";
}
