"use client";

import { useState, type FormEvent } from "react";
import { adminFetch, registerPasskey, passkeyError } from "./admin-auth";
import styles from "./operations.module.css";

export function AdminSecurityPanel({ role, onClose }: { role: string; onClose: () => void }) {
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");
  const [link, setLink] = useState("");
  async function backup() {
    setBusy(true); setError(""); setMessage("");
    try { await registerPasskey(); setMessage("备用通行密钥已绑定。可在系统弹窗中选择另一台设备或安全密钥。"); }
    catch (cause) { setError(passkeyError(cause)); }
    finally { setBusy(false); }
  }
  async function invite(event: FormEvent<HTMLFormElement>) {
    event.preventDefault(); const form = event.currentTarget; const data = new FormData(form);
    setBusy(true); setError(""); setLink(""); setMessage("");
    try {
      const response = await adminFetch("/v1/ops/auth/invitations", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(Object.fromEntries(data)) });
      const payload = await response.json() as { invite?: string; message?: string };
      if (!response.ok || !payload.invite) throw new Error(payload.message ?? "创建邀请失败");
      setLink(`${window.location.origin}/admin#invite=${payload.invite}`); form.reset();
    } catch (cause) { setError(passkeyError(cause)); }
    finally { setBusy(false); }
  }
  async function revoke() {
    setBusy(true); setError("");
    try {
      const response = await adminFetch("/v1/ops/auth/sessions/revoke", { method: "POST" });
      if (!response.ok) throw new Error("撤销会话失败，请重试");
      window.dispatchEvent(new Event("admin-session-expired"));
    } catch (cause) { setError(passkeyError(cause)); }
    finally { setBusy(false); }
  }
  return <section className={styles.securityPanel} aria-label="账号与通行密钥">
    <header><h2>账号与通行密钥</h2><button type="button" onClick={onClose}>关闭</button></header>
    <p>绑定备用设备，避免设备丢失后无法登录。账号恢复由部署管理员核验身份后处理。</p>
    <div className={styles.securityActions}><button type="button" disabled={busy} onClick={() => void backup()}>添加备用通行密钥</button><button type="button" disabled={busy} onClick={() => void revoke()}>退出此账号的所有会话</button></div>
    {role === "admin" && <form onSubmit={(event) => void invite(event)} className={styles.securityInvite}>
      <h3>邀请后台成员</h3>
      <label>账号 ID<input name="id" required maxLength={128} autoComplete="off" /></label>
      <label>显示名称<input name="name" required maxLength={128} /></label>
      <label>权限<select name="role" defaultValue="viewer"><option value="viewer">只读</option><option value="support">运营支持</option><option value="admin">管理员</option></select></label>
      <label>授权原因<input name="reason" required maxLength={512} /></label>
      <button type="submit" disabled={busy}>{busy ? "处理中…" : "创建一次性邀请"}</button>
    </form>}
    {link && <label className={styles.inviteResult}>邀请链接（30 分钟有效，仅显示于本次操作）<input readOnly value={link} onFocus={(event) => event.target.select()} /><small>请通过可信渠道发送给受邀人。链接允许绑定其后台账号。</small></label>}
    {message && <p role="status">{message}</p>}
    {error && <p className={styles.loginError} role="alert">{error}</p>}
  </section>;
}
