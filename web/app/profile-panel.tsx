"use client";

import { FormEvent, useState } from "react";

const apiBase = process.env.NEXT_PUBLIC_API_BASE_URL ?? "http://localhost:8080";

export type UserProfile = {
  id: string;
  email: string;
  display_name: string;
  timezone: string;
  locale: string;
  status?: "active" | "disabled" | "deleted";
  created_at: string;
  updated_at: string;
};

type Props = {
  token: string;
  user: UserProfile;
  onClose: () => void;
};

type APIError = { message?: string };

export function ProfilePanel({ token, user, onClose }: Props) {
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  const [messageKind, setMessageKind] = useState<"success" | "error">("success");
  const initial = Array.from(user.display_name.trim())[0] ?? user.email.slice(0, 1).toUpperCase();

  async function changePassword(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = event.currentTarget;
    const data = new FormData(form);
    const currentPassword = String(data.get("currentPassword") ?? "");
    const newPassword = String(data.get("newPassword") ?? "");
    const confirmPassword = String(data.get("confirmPassword") ?? "");
    setMessage("");
    setMessageKind("error");
    if (newPassword !== confirmPassword) {
      setMessage("两次输入的新密码不一致");
      return;
    }
    if (currentPassword === newPassword) {
      setMessage("新密码不能与当前密码相同");
      return;
    }
    setBusy(true);
    try {
      const response = await fetch(`${apiBase}/v1/users/me/password`, {
        method: "POST",
        headers: { "Content-Type": "application/json", Authorization: `Bearer ${token}` },
        body: JSON.stringify({ current_password: currentPassword, new_password: newPassword }),
      });
      if (!response.ok) {
        const payload = await response.json().catch(() => ({})) as APIError;
        throw new Error(payload.message ?? "密码修改失败，请稍后重试");
      }
      form.reset();
      setMessageKind("success");
      setMessage("密码已更新，其他设备上的登录已自动退出。");
    } catch (error) {
      setMessageKind("error");
      setMessage(error instanceof Error ? error.message : "密码修改失败，请稍后重试");
    } finally {
      setBusy(false);
    }
  }

  return (
    <section className="profilePanel" aria-labelledby="profile-title">
      <header className="profileHeader">
        <button className="textButton" type="button" onClick={onClose}>← 返回主页</button>
        <span className="profileStatus"><i aria-hidden="true" />账户正常</span>
      </header>

      <div className="profileHero">
        <span className="profileAvatar" aria-hidden="true">{initial}</span>
        <div><p className="eyebrow">MY PROFILE</p><h1 id="profile-title">{user.display_name}</h1><p>{user.email}</p></div>
      </div>

      <div className="profileGrid">
        <section className="profileCard" aria-labelledby="account-details-title">
          <div className="profileCardHeading"><span aria-hidden="true">◎</span><div><h2 id="account-details-title">账户资料</h2><p>你的基础账户信息</p></div></div>
          <dl className="profileDetails">
            <div><dt>昵称</dt><dd>{user.display_name}</dd></div>
            <div><dt>登录邮箱</dt><dd>{user.email}</dd></div>
            <div><dt>时区</dt><dd>{user.timezone}</dd></div>
            <div><dt>语言</dt><dd>{formatLocale(user.locale)}</dd></div>
            <div><dt>加入时间</dt><dd>{formatDate(user.created_at)}</dd></div>
          </dl>
        </section>

        <section className="profileCard securityCard" aria-labelledby="security-title">
          <div className="profileCardHeading"><span aria-hidden="true">◇</span><div><h2 id="security-title">账户安全</h2><p>修改密码后，其他设备会退出登录</p></div></div>
          <form className="passwordForm" onSubmit={changePassword}>
            <label>当前密码<input name="currentPassword" type="password" required autoComplete="current-password" placeholder="请输入当前密码" /></label>
            <label>新密码<input name="newPassword" type="password" required minLength={8} maxLength={128} autoComplete="new-password" placeholder="8–128 位字符" /></label>
            <label>确认新密码<input name="confirmPassword" type="password" required minLength={8} maxLength={128} autoComplete="new-password" placeholder="再次输入新密码" /></label>
            <button type="submit" disabled={busy}>{busy ? "正在保存…" : "更新密码"}</button>
          </form>
          {message && <p className={`profileMessage ${messageKind}`} role="status" aria-live="polite">{message}</p>}
        </section>
      </div>
    </section>
  );
}

function formatLocale(locale: string) {
  const normalized = locale.toLowerCase();
  if (normalized.startsWith("zh")) return `简体中文（${locale}）`;
  if (normalized.startsWith("en")) return `English (${locale})`;
  return locale;
}

function formatDate(value: string) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "—";
  return date.toLocaleDateString("zh-CN", { year: "numeric", month: "long", day: "numeric" });
}
