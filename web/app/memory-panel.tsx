"use client";

import { FormEvent, useCallback, useEffect, useState } from "react";

const apiBase = process.env.NEXT_PUBLIC_API_BASE_URL ?? "http://localhost:8080";

type Memory = {
  id: string;
  type: string;
  content: string;
  pinned: boolean;
  sensitivity: string;
  source_message_id?: string;
  updated_at: string;
};

export function MemoryPanel({ token, onClose }: { token: string; onClose: () => void }) {
  const [items, setItems] = useState<Memory[]>([]);
  const [editing, setEditing] = useState("");
  const [draft, setDraft] = useState("");
  const [message, setMessage] = useState("");

  const load = useCallback(async () => {
    const response = await fetch(`${apiBase}/v1/memories`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    if (!response.ok) throw new Error("无法加载记忆");
    setItems(((await response.json()) as { items: Memory[] }).items);
  }, [token]);

  useEffect(() => {
    queueMicrotask(() => void load().catch((error: Error) => setMessage(error.message)));
  }, [load]);

  async function create(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = event.currentTarget;
    const content = String(new FormData(form).get("content") ?? "").trim();
    if (!content) return;
    try {
      const response = await fetch(`${apiBase}/v1/memories`, {
        method: "POST",
        headers: { "Content-Type": "application/json", Authorization: `Bearer ${token}` },
        body: JSON.stringify({ content }),
      });
      if (!response.ok) throw new Error("保存失败");
      form.reset();
      setMessage("记忆已保存");
      await load();
    } catch (error) {
      setMessage(error instanceof Error ? error.message : "保存失败");
    }
  }

  async function patch(id: string, body: Record<string, unknown>) {
    try {
      const response = await fetch(`${apiBase}/v1/memories/${id}${body.content ? "/corrections" : ""}`, {
        method: body.content ? "POST" : "PATCH",
        headers: { "Content-Type": "application/json", Authorization: `Bearer ${token}` },
        body: JSON.stringify(body),
      });
      if (!response.ok) throw new Error("更新失败");
      setEditing("");
      setMessage("");
      await load();
    } catch (error) {
      setMessage(error instanceof Error ? error.message : "更新失败");
    }
  }

  async function remove(id: string) {
    try {
      const response = await fetch(`${apiBase}/v1/memories/${id}`, {
        method: "DELETE",
        headers: { Authorization: `Bearer ${token}` },
      });
      if (!response.ok) throw new Error("删除失败");
      setMessage("");
      await load();
    } catch (error) {
      setMessage(error instanceof Error ? error.message : "删除失败");
    }
  }

  async function clear() {
    if (!window.confirm("确定清空所有长期记忆吗？此操作不会删除聊天记录。")) return;
    try {
      const response = await fetch(`${apiBase}/v1/memories`, {
        method: "DELETE",
        headers: { Authorization: `Bearer ${token}` },
      });
      if (!response.ok) throw new Error("清空失败");
      setMessage("长期记忆已清空");
      await load();
    } catch (error) {
      setMessage(error instanceof Error ? error.message : "清空失败");
    }
  }

  return (
    <section className="memoryPanel" aria-label="长期记忆管理">
      <header>
        <div>
          <small>可查看、修改和删除</small>
          <h3>AI 记住了什么</h3>
        </div>
        <button className="textButton" type="button" onClick={onClose}>返回角色</button>
      </header>
      <form className="memoryCreate" onSubmit={create}>
        <input name="content" minLength={2} maxLength={2000} required placeholder="手动添加一条记忆" />
        <button type="submit">添加</button>
      </form>
      {message && <p className="formMessage" role="status">{message}</p>}
      <div className="memoryList">
        {items.length === 0 ? (
          <p className="emptyState">还没有长期记忆。你可以在聊天中说“记住……”。</p>
        ) : items.map((item) => (
          <article key={item.id}>
            <div className="memoryMeta">
              <span>{labels[item.type] ?? item.type}</span>
              <span>{item.sensitivity === "normal" ? "普通" : item.sensitivity === "personal" ? "个人" : "敏感"}</span>
              {item.source_message_id && <span>来自聊天</span>}
            </div>
            {editing === item.id ? (
              <div className="memoryEdit">
                <input aria-label="编辑记忆内容" value={draft} onChange={(event) => setDraft(event.target.value)} />
                <button type="button" onClick={() => void patch(item.id, { content: draft })}>保存</button>
                <button type="button" onClick={() => setEditing("")}>取消</button>
              </div>
            ) : <p>{item.content}</p>}
            <div className="memoryActions">
              <button type="button" onClick={() => void patch(item.id, { pinned: !item.pinned })}>
                {item.pinned ? "取消固定" : "固定"}
              </button>
              <button type="button" onClick={() => { setEditing(item.id); setDraft(item.content); }}>编辑</button>
              <button type="button" onClick={() => void remove(item.id)}>删除</button>
            </div>
          </article>
        ))}
      </div>
      {items.length > 0 && (
        <button className="dangerButton" type="button" onClick={() => void clear()}>清空全部记忆</button>
      )}
    </section>
  );
}

const labels: Record<string, string> = {
  preference: "偏好",
  relationship: "关系",
  goal: "目标",
  commitment: "承诺",
  experience: "经历",
  fact: "事实",
};
