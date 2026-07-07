"use client";

import { FormEvent, useCallback, useEffect, useState } from "react";
import { ChatPanel } from "./chat-panel";
import { DocumentPanel } from "./document-panel";
import { MemoryPanel } from "./memory-panel";
import { LifePanel } from "./life-panel";
import { WorkPanel } from "./work-panel";

const apiBase = process.env.NEXT_PUBLIC_API_BASE_URL ?? "http://localhost:8080";

type Character = {
  id: string;
  name: string;
  relationship: string;
  personality: string;
  speech_style: string;
  persona_version: number;
};

type TokenPair = { access_token: string; refresh_token: string };

export function CompanionStart() {
  const [token, setToken] = useState("");
  const [characters, setCharacters] = useState<Character[]>([]);
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);
  const [activeCharacter, setActiveCharacter] = useState<Character | null>(null);
  const [showMemories,setShowMemories]=useState(false);
  const [showDocuments, setShowDocuments] = useState(false);
  const [showLife, setShowLife] = useState(false);
  const [showWork, setShowWork] = useState(false);

  const loadCharacters = useCallback(async (accessToken: string) => {
    const response = await fetch(`${apiBase}/v1/characters`, { headers: { Authorization: `Bearer ${accessToken}` } });
    if (!response.ok) throw new Error("登录已过期，请重新注册或登录");
    const payload = (await response.json()) as { items: Character[] };
    setCharacters(payload.items);
  }, []);

  useEffect(() => {
    const saved = window.localStorage.getItem("ai-companion-access-token");
    if (!saved) return;
    queueMicrotask(() => {
      setToken(saved);
      void loadCharacters(saved).catch((error: Error) => setMessage(error.message));
    });
  }, [loadCharacters]);

  async function register(event: FormEvent<HTMLFormElement>) {
    event.preventDefault(); setBusy(true); setMessage("");
    const data = new FormData(event.currentTarget);
    try {
      const response = await fetch(`${apiBase}/v1/auth/register`, {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          email: data.get("email"), password: data.get("password"), display_name: data.get("displayName"),
          timezone: Intl.DateTimeFormat().resolvedOptions().timeZone, locale: navigator.language,
          device: { device_key: getDeviceKey(), name: navigator.userAgent, platform: "web" },
        }),
      });
      const payload = (await response.json()) as TokenPair & { message?: string };
      if (!response.ok) throw new Error(payload.message ?? "注册失败");
      window.localStorage.setItem("ai-companion-access-token", payload.access_token);
      window.localStorage.setItem("ai-companion-refresh-token", payload.refresh_token);
      setToken(payload.access_token); setMessage("账户已创建，现在给你的伙伴一个名字吧。");
      await loadCharacters(payload.access_token);
    } catch (error) { setMessage(error instanceof Error ? error.message : "注册失败"); }
    finally { setBusy(false); }
  }

  async function createCharacter(event: FormEvent<HTMLFormElement>) {
    event.preventDefault(); setBusy(true); setMessage("");
    const form = event.currentTarget;
    const data = new FormData(form);
    try {
      const response = await fetch(`${apiBase}/v1/characters`, {
        method: "POST", headers: { "Content-Type": "application/json", Authorization: `Bearer ${token}` },
        body: JSON.stringify({
          name: data.get("name"), relationship: data.get("relationship"), personality: data.get("personality"),
          speech_style: data.get("speechStyle"), initiative: "balanced", reply_length: "short",
          hobbies: [], boundaries: [], raw_prompt: "",
        }),
      });
      const payload = (await response.json()) as { message?: string };
      if (!response.ok) throw new Error(payload.message ?? "创建角色失败");
      form.reset(); setMessage("角色创建成功，可以开始聊天了。");
      await loadCharacters(token);
    } catch (error) { setMessage(error instanceof Error ? error.message : "创建角色失败"); }
    finally { setBusy(false); }
  }

  function signOut() {
    window.localStorage.removeItem("ai-companion-access-token"); window.localStorage.removeItem("ai-companion-refresh-token");
    setToken(""); setCharacters([]); setMessage("");
  }

  return (
    <section className="startPanel" id="companion-start" aria-labelledby="start-title">
      <div className="startCopy"><p className="eyebrow">M1 · 可体验切片</p><h2 id="start-title">创建你的第一个 AI 伙伴</h2><p>账户、设备时区、角色设定和 persona 版本都会被纳入同一条可靠链路。</p></div>
      {!token ? (
        <form className="startForm" onSubmit={register}>
          <label>昵称<input name="displayName" required maxLength={40} placeholder="小林" /></label>
          <label>邮箱<input name="email" required type="email" placeholder="you@example.com" /></label>
          <label>密码<input name="password" required type="password" minLength={8} placeholder="至少 8 位" /></label>
          <button disabled={busy} type="submit">{busy ? "正在创建…" : "注册并开始"}</button>
        </form>
      ) : (
        showWork ? <WorkPanel token={token} onClose={() => setShowWork(false)} /> : showLife ? <LifePanel token={token} onClose={() => setShowLife(false)} /> : showDocuments ? <DocumentPanel token={token} onClose={() => setShowDocuments(false)} /> : showMemories?<MemoryPanel token={token} onClose={()=>setShowMemories(false)}/>:activeCharacter ? <ChatPanel token={token} character={activeCharacter} onClose={()=>setActiveCharacter(null)} /> : <div className="characterWorkspace">
          <form className="startForm" onSubmit={createCharacter}>
            <label>角色名字<input name="name" required maxLength={40} placeholder="小棉" /></label>
            <label>你们的关系<input name="relationship" required placeholder="温柔朋友" /></label>
            <label>性格<textarea name="personality" required placeholder="耐心、体贴，但不说教" /></label>
            <label>说话方式<textarea name="speechStyle" required placeholder="短句，先共情再回应" /></label>
            <button disabled={busy} type="submit">{busy ? "正在编译 persona…" : "创建角色"}</button>
          </form>
          <div className="characterList"><div className="listHeader"><h3>我的角色</h3><div><button className="textButton" type="button" onClick={()=>setShowWork(true)}>工作伙伴</button><button className="textButton" type="button" onClick={()=>setShowLife(true)}>生活助手</button><button className="textButton" type="button" onClick={()=>setShowDocuments(true)}>文档库</button><button className="textButton" type="button" onClick={()=>setShowMemories(true)}>记忆管理</button><button className="textButton" type="button" onClick={signOut}>退出本机</button></div></div>
            {characters.length === 0 ? <p className="emptyState">还没有角色。</p> : characters.map((item) => <article key={item.id}><span>{item.name.slice(0, 1)}</span><div><strong>{item.name}</strong><small>{item.relationship} · persona v{item.persona_version}</small><p>{item.personality}</p><button className="chatAction" type="button" onClick={()=>setActiveCharacter(item)}>开始聊天</button></div></article>)}
          </div>
        </div>
      )}
      {message && <p className="formMessage" role="status">{message}</p>}
    </section>
  );
}

function getDeviceKey() {
  const current = window.localStorage.getItem("ai-companion-device-key"); if (current) return current;
  const created = crypto.randomUUID(); window.localStorage.setItem("ai-companion-device-key", created); return created;
}
