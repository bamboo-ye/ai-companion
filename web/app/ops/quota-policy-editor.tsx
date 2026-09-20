"use client";

import { FormEvent, useEffect, useState } from "react";
import { adminFetch } from "./admin-auth";
import { quotaDraft, quotaFields, quotaLimits, type QuotaLimits } from "./quota-input";
import styles from "./operations.module.css";

type Policy = { user_id?: string; limits: QuotaLimits; revision: number; actor?: string; reason?: string; updated_at?: string };
type Policies = { global: Policy; user: Policy };

export function QuotaPolicyEditor({ userID, userLabel, canEdit, onSaved }: { userID?: string; userLabel?: string; canEdit: boolean; onSaved: () => void }) {
  const global = !userID;
  const path = global ? "/v1/ops/billing/quotas" : `/v1/ops/billing/users/${encodeURIComponent(userID)}/quotas`;
  const [policy, setPolicy] = useState<Policy | null>(null);
  const [draft, setDraft] = useState(() => quotaDraft());
  const [reason, setReason] = useState("");
  const [working, setWorking] = useState(false);
  const [error, setError] = useState("");
  const [message, setMessage] = useState("");
  const [reload, setReload] = useState(0);
  const [confirmGlobal, setConfirmGlobal] = useState(false);

  useEffect(() => {
    let cancelled = false;
    void request<Policies>(path).then((p) => {
      if (cancelled) return;
      const current = global ? p.global : p.user;
      setPolicy(current);
      setDraft(quotaDraft(current.limits));
      setReason("");
      setConfirmGlobal(false);
    }).catch((cause: unknown) => { if (!cancelled) setError(errorText(cause)); });
    return () => { cancelled = true; };
  }, [path, global, reload]);

  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!policy || !canEdit || (global && !confirmGlobal)) return;
    setWorking(true); setError(""); setMessage("");
    try {
      const next = await request<Policy>(path, { method: "PUT", body: JSON.stringify({ limits: quotaLimits(draft), revision: policy.revision, reason }) });
      setPolicy(next); setReason(""); setConfirmGlobal(false);
      setMessage(global ? "全体用户额度已生效；用户单独设置继续优先。" : "该用户额度已生效。");
      onSaved();
    } catch (cause) { setError(errorText(cause)); }
    finally { setWorking(false); }
  }

  return <article className={styles.editorCard}>
    <div className={styles.panelHeading}><div><p>{global ? "ALL USERS" : "USER OVERRIDE"}</p><h2>{global ? "全体用户额度" : "指定用户额度"}</h2></div><button type="button" className={styles.quotaReload} disabled={working} onClick={() => { setPolicy(null); setError(""); setMessage(""); setReload((v) => v + 1); }}>重新加载</button></div>
    {userLabel && <p className={styles.quotaHelp}><strong>当前用户：{userLabel}</strong></p>}
    <p className={styles.quotaHelp}>{global ? "适用于所有现有和新注册用户，包括付费套餐用户。用户单独设置优先；继承套餐则使用各自套餐额度。" : "仅影响当前所选用户。继承全体设置时，依次使用全体用户额度、套餐额度。"} 0 表示不允许新增使用；修改上限不会清零已用量。</p>
    {error && <div className={styles.errorBanner} role="alert">{error}</div>}
    {message && <div className={styles.successBanner} role="status">{message}</div>}
    {!policy ? <div className={styles.emptyLine}>{error ? "额度设置读取失败，请重新加载。" : "正在读取额度设置…"}</div> : <form className={styles.quotaForm} onSubmit={save}>
      <div className={styles.quotaFields}>{quotaFields.map(({ key, label, cost }) => <div key={key} className={styles.quotaField}>
        <label htmlFor={`${userID ?? "all"}-${key}-mode`}>{label}</label>
        <select id={`${userID ?? "all"}-${key}-mode`} value={draft[key].mode} disabled={!canEdit || working} onChange={(e) => { const mode = e.target.value as "inherit" | "unlimited" | "custom"; setDraft((v) => ({ ...v, [key]: { ...v[key], mode } })); }}>
          <option value="inherit">{global ? "继承套餐" : "继承全体设置"}</option><option value="custom">指定额度</option><option value="unlimited">不限额</option>
        </select>
        {draft[key].mode === "custom" && <input aria-label={`${label}上限`} type="number" min="0" step={cost ? "0.000001" : "1"} required disabled={!canEdit || working} value={draft[key].value} placeholder={cost ? "例如 10.00" : "例如 100"} onChange={(e) => { const value = e.target.value; setDraft((v) => ({ ...v, [key]: { ...v[key], value } })); }} />}
      </div>)}</div>
      {policy.updated_at && <p className={styles.quotaHelp}>最近修改：{policy.actor} · {new Date(policy.updated_at).toLocaleString("zh-CN")} · {policy.reason}</p>}
      {canEdit ? <>
        <button type="button" className={styles.quotaReset} disabled={working} onClick={() => { setDraft(quotaDraft()); setMessage("已将表单恢复为继承，填写原因并保存后生效。"); }}>全部恢复继承</button>
        <label className={styles.quotaReason}>修改原因<textarea value={reason} onChange={(e) => setReason(e.target.value)} required maxLength={512} disabled={working} placeholder="填写调整依据，保存后计入审计日志" /></label>
        {global && <label className={styles.quotaConfirm}><input type="checkbox" checked={confirmGlobal} disabled={working} onChange={(e) => setConfirmGlobal(e.target.checked)} />确认应用于所有用户，包括未来注册用户；保留已有用户单独设置。</label>}
        <button className={styles.primaryWide} disabled={working || !reason.trim() || (global && !confirmGlobal)}>{working ? "正在保存…" : global ? "保存全体用户额度" : "保存指定用户额度"}</button>
      </> : <p className={styles.quotaHelp}>仅管理员可修改额度。</p>}
    </form>}
  </article>;
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await adminFetch(path, { ...init, headers: { "Content-Type": "application/json" } });
  const payload = await response.json() as T & { message?: string };
  if (!response.ok) throw new Error(payload.message ?? `请求失败（${response.status}）`);
  return payload;
}
function errorText(cause: unknown) { return cause instanceof Error ? cause.message : "额度操作失败，请重试"; }
