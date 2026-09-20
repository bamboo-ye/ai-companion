"use client";

import { FormEvent, useCallback, useEffect, useMemo, useState } from "react";
import Image from "next/image";
import { ChatPanel } from "./chat-panel";
import { DocumentPanel } from "./document-panel";
import { BuiltinKnowledgePanel } from "./builtin-knowledge-panel";
import { LifePanel } from "./life-panel";
import { MemoryPanel } from "./memory-panel";
import { TaskHistoryPanel } from "./task-history-panel";
import { WorkPanel } from "./work-panel";
import { OnboardingGuide } from "./onboarding-guide";
import { userGuideTopics, type UserGuideTarget } from "./onboarding-content";
import { ProfilePanel, type UserProfile } from "./profile-panel";

const apiBase = process.env.NEXT_PUBLIC_API_BASE_URL ?? "http://localhost:8080";

type ModuleKey = "companion" | "life" | "work";
type Screen = "dashboard" | "chat" | "work-tools" | "task-history" | "documents" | "memories" | "profile" | "knowledge";
type AuthMode = "login" | "register";

type Character = {
  id: string;
  module: ModuleKey;
  name: string;
  relationship: string;
  personality: string;
  speech_style: string;
  persona_version: number;
  created_at?: string;
};

type Conversation = {
  id: string;
  character_id: string;
  title: string;
  last_message_at: string | null;
  created_at: string;
  updated_at: string;
};

type TokenPair = {
  access_token: string;
  refresh_token: string;
  access_expires_at?: string;
  refresh_expires_at?: string;
  user?: UserProfile;
  message?: string;
};

class AuthRequestError extends Error {
  constructor(message: string, readonly status: number) {
    super(message);
    this.name = "AuthRequestError";
  }
}

const modules: Record<ModuleKey, { icon: string; title: string; description: string; empty: string }> = {
  companion: { icon: "♥", title: "情感陪伴", description: "温暖倾听，陪你安放每一种情绪", empty: "创建一个懂你的伙伴，开始第一段对话。" },
  life: { icon: "✓", title: "生活助手", description: "日程提醒、生活记录与日常陪伴", empty: "添加一个生活伙伴，一起安排日常。" },
  work: { icon: "▣", title: "工作伙伴", description: "围绕任务、资料和协作高效推进", empty: "添加一个工作伙伴，开始处理任务。" },
};

export function CompanionStart() {
  const [checkingSession, setCheckingSession] = useState(true);
  const [authMode, setAuthMode] = useState<AuthMode>("login");
  const [loginEmail, setLoginEmail] = useState("");
  const [token, setToken] = useState("");
  const [currentUser, setCurrentUser] = useState<UserProfile | null>(null);
  const [characters, setCharacters] = useState<Character[]>([]);
  const [conversations, setConversations] = useState<Conversation[]>([]);
  const [activeModule, setActiveModule] = useState<ModuleKey>("companion");
  const [screen, setScreen] = useState<Screen>("dashboard");
  const [activeCharacter, setActiveCharacter] = useState<Character | null>(null);
  const [showCharacterForm, setShowCharacterForm] = useState(false);
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);
  const [knowledgePageID, setKnowledgePageID] = useState("");

  useEffect(() => {
    const pageID = new URLSearchParams(window.location.hash.slice(1)).get("knowledge");
    if (pageID && /^builtin-[a-z0-9-]{1,72}$/.test(pageID)) queueMicrotask(() => {
      setKnowledgePageID(pageID); setScreen("knowledge");
    });
  }, []);

  const loadDashboard = useCallback(async (accessToken: string) => {
    const headers = { Authorization: `Bearer ${accessToken}` };
    const [characterResponse, conversationResponse, profileResponse] = await Promise.all([
      fetch(`${apiBase}/v1/characters`, { headers }),
      fetch(`${apiBase}/v1/conversations`, { headers }),
      fetch(`${apiBase}/v1/users/me`, { headers }),
    ]);
    if (!characterResponse.ok || !conversationResponse.ok || !profileResponse.ok) {
      const failed = !characterResponse.ok ? characterResponse : !conversationResponse.ok ? conversationResponse : profileResponse;
      const payload = await failed.json().catch(() => ({ message: "" })) as { message?: string };
      throw new AuthRequestError(
        payload.message || (failed.status === 401 ? "登录状态无效或已过期" : "暂时无法加载账户数据"),
        failed.status,
      );
    }
    const characterPayload = (await characterResponse.json()) as { items: Character[] };
    const conversationPayload = (await conversationResponse.json()) as { items: Conversation[] };
    const profilePayload = (await profileResponse.json()) as UserProfile;
    setCharacters(characterPayload.items);
    setConversations(conversationPayload.items);
    setCurrentUser(profilePayload);
  }, []);

  useEffect(() => {
    let active = true;
    queueMicrotask(() => {
      const restore = async () => {
        const savedAccess = window.localStorage.getItem("ai-companion-access-token");
        const savedRefresh = window.localStorage.getItem("ai-companion-refresh-token");
        if (!savedAccess && !savedRefresh) return;

        if (savedAccess) {
          try {
            await loadDashboard(savedAccess);
            if (active) setToken(savedAccess);
            return;
          } catch (error) {
            if (!isUnauthorized(error)) {
              if (active) {
                setToken(savedAccess);
                setMessage(error instanceof Error ? error.message : "暂时无法验证登录状态");
              }
              return;
            }
          }
        }

        if (!savedRefresh) {
          clearStoredSession();
          if (active) setMessage("登录已过期，请重新登录");
          return;
        }

        try {
          const pair = await refreshSession(savedRefresh);
          storeSession(pair);
          await loadDashboard(pair.access_token);
          if (active) setToken(pair.access_token);
        } catch (error) {
          if (isUnauthorized(error)) {
            clearStoredSession();
            if (active) setMessage("登录已过期，请重新登录");
            return;
          }
          if (active) setMessage(error instanceof Error ? error.message : "暂时无法验证登录状态，请稍后重试");
        }
      };

      void restore().finally(() => {
        if (active) setCheckingSession(false);
      });
    });
    return () => { active = false; };
  }, [loadDashboard]);

  useEffect(() => {
    if (!token) return;
    let active = true;
    let timer = 0;

    const rotate = async () => {
      const refreshToken = window.localStorage.getItem("ai-companion-refresh-token");
      if (!refreshToken) return;
      try {
        const pair = await refreshSession(refreshToken);
        if (!active) return;
        storeSession(pair);
        setToken(pair.access_token);
        if (pair.user) setCurrentUser(pair.user);
      } catch (error) {
        if (!active) return;
        if (isUnauthorized(error)) {
          clearStoredSession();
          setToken("");
          setCharacters([]);
          setConversations([]);
          setCurrentUser(null);
          setMessage("登录已过期，请重新登录");
          return;
        }
        timer = window.setTimeout(() => { void rotate(); }, 30_000);
      }
    };

    timer = window.setTimeout(() => { void rotate(); }, accessRefreshDelay(token));
    return () => {
      active = false;
      window.clearTimeout(timer);
    };
  }, [token]);

  const conversationByCharacter = useMemo(() => {
    const result = new Map<string, Conversation>();
    for (const item of conversations) {
      const current = result.get(item.character_id);
      if (!current || conversationTime(item) > conversationTime(current)) result.set(item.character_id, item);
    }
    return result;
  }, [conversations]);

  const orderedCharacters = useMemo(() => [...characters].sort((left, right) => {
    const leftConversation = conversationByCharacter.get(left.id);
    const rightConversation = conversationByCharacter.get(right.id);
    return conversationTime(rightConversation) - conversationTime(leftConversation);
  }), [characters, conversationByCharacter]);

  const moduleCharacters = useMemo(
    () => orderedCharacters.filter((character) => character.module === activeModule),
    [activeModule, orderedCharacters],
  );

  async function login(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setBusy(true);
    setMessage("");
    const data = new FormData(event.currentTarget);
    try {
      const response = await fetch(`${apiBase}/v1/auth/login`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          email: data.get("email"),
          password: data.get("password"),
          timezone: Intl.DateTimeFormat().resolvedOptions().timeZone,
          device: deviceInput(),
        }),
      });
      const payload = (await response.json()) as TokenPair;
      if (!response.ok) throw new Error(payload.message ?? "邮箱或密码不正确");
      storeSession(payload);
      setToken(payload.access_token);
      await loadDashboard(payload.access_token);
      setScreen(knowledgePageID ? "knowledge" : "dashboard");
      setMessage("");
    } catch (error) {
      setMessage(error instanceof Error ? error.message : "登录失败");
    } finally {
      setBusy(false);
    }
  }

  async function register(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setBusy(true);
    setMessage("");
    const data = new FormData(event.currentTarget);
    const email = String(data.get("email") ?? "");
    try {
      const response = await fetch(`${apiBase}/v1/auth/register`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          email,
          password: data.get("password"),
          display_name: data.get("displayName"),
          timezone: Intl.DateTimeFormat().resolvedOptions().timeZone,
          locale: navigator.language,
          device: deviceInput(),
        }),
      });
      const payload = (await response.json()) as TokenPair;
      if (!response.ok) throw new Error(payload.message ?? "注册失败");
      await fetch(`${apiBase}/v1/auth/logout`, {
        method: "POST",
        headers: { Authorization: `Bearer ${payload.access_token}` },
      }).catch(() => undefined);
      setLoginEmail(email);
      setAuthMode("login");
      setMessage("注册成功，请使用新账户登录。");
    } catch (error) {
      setMessage(error instanceof Error ? error.message : "注册失败");
    } finally {
      setBusy(false);
    }
  }

  async function createCharacter(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setBusy(true);
    setMessage("");
    const form = event.currentTarget;
    const data = new FormData(form);
    try {
      const response = await fetch(`${apiBase}/v1/characters`, {
        method: "POST",
        headers: { "Content-Type": "application/json", Authorization: `Bearer ${token}` },
        body: JSON.stringify({
          module: activeModule,
          name: data.get("name"),
          relationship: data.get("relationship"),
          personality: data.get("personality"),
          speech_style: data.get("speechStyle"),
          initiative: "balanced",
          reply_length: "short",
          hobbies: [],
          boundaries: [],
          raw_prompt: "",
        }),
      });
      const payload = (await response.json()) as { message?: string };
      if (!response.ok) throw new Error(payload.message ?? "创建角色失败");
      form.reset();
      await loadDashboard(token);
      setShowCharacterForm(false);
      setMessage(`角色创建成功，已经加入“${modules[activeModule].title}”模块。`);
    } catch (error) {
      setMessage(error instanceof Error ? error.message : "创建角色失败");
    } finally {
      setBusy(false);
    }
  }

  async function signOut() {
    if (token) {
      await fetch(`${apiBase}/v1/auth/logout`, { method: "POST", headers: { Authorization: `Bearer ${token}` } }).catch(() => undefined);
    }
    clearStoredSession();
    setToken("");
    setCharacters([]);
    setConversations([]);
    setCurrentUser(null);
    setScreen("dashboard");
    setActiveCharacter(null);
    setMessage("");
    setAuthMode("login");
  }

  function chooseModule(module: ModuleKey) {
    setActiveModule(module);
    setScreen("dashboard");
    setActiveCharacter(null);
    setShowCharacterForm(false);
    setMessage("");
  }

  function openChat(character: Character) {
    setActiveCharacter(character);
    setScreen("chat");
    setMessage("");
  }

  function navigateFromGuide(target: UserGuideTarget) {
    if (target === "companion" || target === "life" || target === "work") {
      chooseModule(target);
      return;
    }
    if (target === "knowledge") {
      setKnowledgePageID("");
      setScreen("knowledge");
      setActiveCharacter(null);
      setShowCharacterForm(false);
      setMessage("");
      return;
    }
    if (target === "profile") {
      setScreen("profile");
      setActiveCharacter(null);
      setShowCharacterForm(false);
      setMessage("");
      return;
    }
    setActiveModule(target === "memories" ? "companion" : "work");
    setScreen(target);
    setActiveCharacter(null);
    setShowCharacterForm(false);
    setMessage("");
  }

  async function deleteConversation(character: Character, conversation: Conversation) {
    if (!window.confirm(`删除与“${character.name}”的会话？历史消息将不再显示，但角色仍会保留。`)) return;
    setBusy(true);
    setMessage("");
    try {
      const response = await fetch(`${apiBase}/v1/conversations/${conversation.id}`, {
        method: "DELETE",
        headers: { Authorization: `Bearer ${token}` },
      });
      if (!response.ok) throw new Error("删除会话失败");
      await loadDashboard(token);
      setMessage(`与“${character.name}”的会话已删除，可以重新开始会话。`);
    } catch (error) {
      setMessage(error instanceof Error ? error.message : "删除会话失败");
    } finally {
      setBusy(false);
    }
  }

  async function deleteCharacter(character: Character) {
    if (!window.confirm(`删除角色“${character.name}”？该角色及其会话入口将从当前模块移除。`)) return;
    setBusy(true);
    setMessage("");
    try {
      const response = await fetch(`${apiBase}/v1/characters/${character.id}`, {
        method: "DELETE",
        headers: { Authorization: `Bearer ${token}` },
      });
      if (!response.ok) throw new Error("删除角色失败");
      await loadDashboard(token);
      setMessage(`角色“${character.name}”已删除。`);
    } catch (error) {
      setMessage(error instanceof Error ? error.message : "删除角色失败");
    } finally {
      setBusy(false);
    }
  }

  if (checkingSession) {
    return <main className="authShell"><div className="sessionLoader" role="status">正在加载伴AI…</div></main>;
  }

  if (!token) {
    return (
      <main className="authShell">
        <section className="authCard" aria-labelledby="auth-title">
          <div className="authBrand"><Image className="brandLogo" src="/icons/logo.png" width={46} height={46} priority alt="" /><div><strong>伴AI</strong><small>懂陪伴，也能一起把事情做好</small></div></div>
          <div className="authIntro">
            <p className="eyebrow">WELCOME</p>
            <h1 id="auth-title">{authMode === "login" ? "欢迎回来" : "创建你的账户"}</h1>
            <p>{authMode === "login" ? "登录后继续与你的 AI 伙伴并肩前行。" : "注册完成后，请返回登录页进入主页。"}</p>
          </div>
          {authMode === "login" ? (
            <form className="authForm" key={loginEmail} onSubmit={login}>
              <label>邮箱<input name="email" required type="email" autoComplete="email" defaultValue={loginEmail} placeholder="you@example.com" /></label>
              <label>密码<input name="password" required type="password" autoComplete="current-password" placeholder="请输入密码" /></label>
              <button disabled={busy} type="submit">{busy ? "正在登录…" : "登录"}</button>
            </form>
          ) : (
            <form className="authForm" onSubmit={register}>
              <label>昵称<input name="displayName" required maxLength={40} autoComplete="name" placeholder="怎么称呼你" /></label>
              <label>邮箱<input name="email" required type="email" autoComplete="email" placeholder="you@example.com" /></label>
              <label>密码<input name="password" required type="password" minLength={8} autoComplete="new-password" placeholder="至少 8 位" /></label>
              <button disabled={busy} type="submit">{busy ? "正在注册…" : "注册"}</button>
            </form>
          )}
          {message && <p className="authMessage" role="status">{message}</p>}
          <p className="authSwitch">
            {authMode === "login" ? "还没有账户？" : "已经有账户？"}
            <button type="button" onClick={() => { setAuthMode(authMode === "login" ? "register" : "login"); setMessage(""); }}>
              {authMode === "login" ? "立即注册" : "返回登录"}
            </button>
          </p>
          <OnboardingGuide key="user-login-guide" scope="user" topics={userGuideTopics} compact />
        </section>
      </main>
    );
  }

  const currentModule = modules[activeModule];
  return (
    <main className={`appFrame module-${activeModule}`}>
      <aside className="appSidebar">
        <div className="sidebarBrand"><Image className="brandLogo" src="/icons/logo.png" width={46} height={46} priority alt="" /><div><strong>伴AI</strong><small>你的智能伙伴</small></div></div>
        <nav aria-label="主页模块">
          {(Object.keys(modules) as ModuleKey[]).map((key) => (
            <button className={activeModule === key && screen === "dashboard" ? "active" : ""} key={key} type="button" onClick={() => chooseModule(key)}>
              <span className={`moduleNavLogo moduleNavLogo-${key}`} aria-hidden="true" /><div><strong>{modules[key].title}</strong><small>{modules[key].description.split("，")[0]}</small></div>
            </button>
          ))}
        </nav>
        <div className="sidebarFooter">
          <button className="sidebarLogout" type="button" onClick={() => { setKnowledgePageID(""); setScreen("knowledge"); }}>产品知识</button>
          <button className={`sidebarProfile ${screen === "profile" ? "active" : ""}`} type="button" onClick={() => { setScreen("profile"); setActiveCharacter(null); setShowCharacterForm(false); setMessage(""); }}>
            <span aria-hidden="true">{currentUser ? (Array.from(currentUser.display_name.trim())[0] ?? currentUser.email.slice(0, 1).toUpperCase()) : "我"}</span>
            <div><strong>{currentUser?.display_name ?? "个人主页"}</strong><small>个人主页</small></div>
          </button>
          <button className="sidebarLogout" type="button" onClick={() => void signOut()}>退出登录</button>
        </div>
      </aside>

      <section className="appWorkspace">
        <header className={screen === "dashboard" ? "workspaceHeader" : "workspaceUtilityHeader"}>
          {screen === "dashboard" && <div><p className="eyebrow">{activeModule === "companion" ? "COMPANION" : activeModule === "life" ? "LIFE" : "WORK"}</p><h1>{currentModule.title}</h1><p>{currentModule.description}</p></div>}
          <div className="workspaceHeaderActions">
            <OnboardingGuide
              key="user-workspace-guide"
              scope="user"
              topics={userGuideTopics}
              autoStart
              toolbar
              currentTopic={screen === "dashboard" ? (activeModule === "companion" ? "characters" : activeModule) : screen}
              onNavigate={navigateFromGuide}
            />
            {screen === "dashboard" && <button className="primaryAction" type="button" onClick={() => setShowCharacterForm(true)}>＋ 添加角色</button>}
          </div>
        </header>
        {screen === "dashboard" && (
          <>
            <div className="moduleTools">
              {activeModule === "companion" && <button type="button" onClick={() => setScreen("memories")}><span>✦</span><div><strong>记忆管理</strong><small>查看与维护长期记忆</small></div></button>}
              {activeModule === "work" && <><button type="button" onClick={() => setScreen("work-tools")}><span>⚙</span><div><strong>工作台</strong><small>技能配置与文件生成</small></div></button><button type="button" onClick={() => setScreen("task-history")}><span>↻</span><div><strong>历史任务</strong><small>状态、结果与失败重试</small></div></button><button type="button" onClick={() => setScreen("documents")}><span>▤</span><div><strong>Wiki</strong><small>上传、解析与检索资料</small></div></button></>}
            </div>

            {activeModule === "life" && <LifePanel token={token} embedded />}

            {showCharacterForm && (
              <section className="createCharacterCard" aria-labelledby="create-character-title">
                <div className="sectionHeading"><div><small>所有内容均可留空，系统会使用当前模块的默认设定</small><h2 id="create-character-title">添加角色</h2></div><button className="textButton" type="button" onClick={() => setShowCharacterForm(false)}>取消</button></div>
                <form className="characterForm" onSubmit={createCharacter}>
                  <label>角色名字<input name="name" maxLength={40} placeholder={activeModule === "work" ? "默认：阿策" : activeModule === "life" ? "默认：小满" : "默认：小棉"} /></label>
                  <label>你们的关系<input name="relationship" placeholder={activeModule === "work" ? "默认：工作搭档" : activeModule === "life" ? "默认：生活管家" : "默认：温柔朋友"} /></label>
                  <label>性格<textarea name="personality" placeholder="留空使用当前模块默认性格" /></label>
                  <label>说话方式<textarea name="speechStyle" placeholder="留空使用当前模块默认说话方式" /></label>
                  <button disabled={busy} type="submit">{busy ? "正在创建…" : "创建角色"}</button>
                </form>
              </section>
            )}

            <section className="conversationSection" aria-labelledby="conversation-title">
              <div className="sectionHeading"><div><small>{moduleCharacters.length} 个角色</small><h2 id="conversation-title">角色会话</h2></div><button className="secondaryAction" type="button" onClick={() => setShowCharacterForm(true)}>添加角色</button></div>
              {moduleCharacters.length === 0 ? (
                <div className="dashboardEmpty"><span aria-hidden="true">{currentModule.icon}</span><h3>还没有角色会话</h3><p>{currentModule.empty}</p><button type="button" onClick={() => setShowCharacterForm(true)}>创建第一个角色</button></div>
              ) : (
                <div className="roleConversationList">
                  {moduleCharacters.map((character) => {
                    const conversation = conversationByCharacter.get(character.id);
                    return (
                      <article key={character.id}>
                        <span className={`roleAvatar moduleNavLogo moduleNavLogo-${character.module}`} aria-hidden="true" />
                        <div className="roleConversationCopy"><div><strong>{character.name}</strong><span>{character.relationship}</span></div><p>{conversation?.title || character.personality || `与${character.name}开始一段新对话`}</p></div>
                        <div className="conversationMeta">
                          <small>{conversation?.last_message_at ? formatTime(conversation.last_message_at) : "尚未开始"}</small>
                          <div className="conversationActions">
                            <button disabled={busy} type="button" onClick={() => openChat(character)}>{conversation ? "继续会话" : "开始会话"}</button>
                            {conversation && <button className="dangerAction" disabled={busy} type="button" onClick={() => void deleteConversation(character, conversation)}>删除会话</button>}
                            <button className="dangerAction" disabled={busy} type="button" onClick={() => void deleteCharacter(character)}>删除角色</button>
                          </div>
                        </div>
                      </article>
                    );
                  })}
                </div>
              )}
            </section>
            {message && <p className="formMessage" role="status">{message}</p>}
          </>
        )}

        {screen === "chat" && activeCharacter && <ChatPanel token={token} character={activeCharacter} onClose={() => { setScreen("dashboard"); setActiveCharacter(null); void loadDashboard(token); }} />}
        {screen === "memories" && <MemoryPanel token={token} onClose={() => setScreen("dashboard")} />}
        {screen === "work-tools" && <WorkPanel token={token} onClose={() => setScreen("dashboard")} />}
        {screen === "task-history" && <TaskHistoryPanel token={token} onClose={() => setScreen("dashboard")} />}
        {screen === "knowledge" && <section className="documentPanel"><button type="button" className="textButton" onClick={() => setScreen("dashboard")}>返回角色</button><BuiltinKnowledgePanel token={token} initialPageID={knowledgePageID} /></section>}
        {screen === "documents" && <DocumentPanel token={token} onClose={() => setScreen("dashboard")} />}
        {screen === "profile" && currentUser && <ProfilePanel token={token} user={currentUser} onClose={() => setScreen("dashboard")} />}
      </section>
    </main>
  );
}

function getDeviceKey() {
  const current = window.localStorage.getItem("ai-companion-device-key");
  if (current) return current;
  const created = crypto.randomUUID();
  window.localStorage.setItem("ai-companion-device-key", created);
  return created;
}

function deviceInput() {
  return { device_key: getDeviceKey(), name: "Web 浏览器", platform: "web" };
}

function storeSession(pair: TokenPair) {
  window.localStorage.setItem("ai-companion-access-token", pair.access_token);
  window.localStorage.setItem("ai-companion-refresh-token", pair.refresh_token);
  if (pair.access_expires_at) window.localStorage.setItem("ai-companion-access-expires-at", pair.access_expires_at);
  else window.localStorage.removeItem("ai-companion-access-expires-at");
  if (pair.refresh_expires_at) window.localStorage.setItem("ai-companion-refresh-expires-at", pair.refresh_expires_at);
  else window.localStorage.removeItem("ai-companion-refresh-expires-at");
}

function clearStoredSession() {
  window.localStorage.removeItem("ai-companion-access-token");
  window.localStorage.removeItem("ai-companion-refresh-token");
  window.localStorage.removeItem("ai-companion-access-expires-at");
  window.localStorage.removeItem("ai-companion-refresh-expires-at");
}

async function refreshSession(refreshToken: string) {
  let response: Response;
  try {
    response = await fetch(`${apiBase}/v1/auth/refresh`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ refresh_token: refreshToken }),
    });
  } catch {
    throw new Error("暂时无法连接服务，已保留登录信息并将在稍后重试");
  }
  const payload = await response.json().catch(() => ({ message: "" })) as Partial<TokenPair> & { message?: string };
  if (!response.ok || !payload.access_token || !payload.refresh_token) {
    throw new AuthRequestError(
      payload.message || (response.status === 401 ? "登录状态无效或已过期" : "暂时无法刷新登录状态"),
      response.status,
    );
  }
  return payload as TokenPair;
}

function isUnauthorized(error: unknown) {
  return error instanceof AuthRequestError && error.status === 401;
}

function accessRefreshDelay(accessToken: string) {
  const storedExpiry = window.localStorage.getItem("ai-companion-access-expires-at");
  let expiry = storedExpiry ? Date.parse(storedExpiry) : Number.NaN;
  if (!Number.isFinite(expiry)) {
    try {
      const encoded = accessToken.split(".")[1] ?? "";
      const padded = encoded.replace(/-/g, "+").replace(/_/g, "/").padEnd(Math.ceil(encoded.length / 4) * 4, "=");
      const claims = JSON.parse(window.atob(padded)) as { exp?: number };
      expiry = typeof claims.exp === "number" ? claims.exp * 1000 : Number.NaN;
    } catch {
      expiry = Number.NaN;
    }
  }
  if (!Number.isFinite(expiry)) return 10 * 60 * 1000;
  return Math.max(5_000, expiry - Date.now() - 60_000);
}

function conversationTime(item?: Conversation) {
  if (!item) return 0;
  return new Date(item.last_message_at ?? item.updated_at ?? item.created_at).getTime();
}

function formatTime(value: string) {
  const date = new Date(value);
  const now = new Date();
  if (date.toDateString() === now.toDateString()) return date.toLocaleTimeString("zh-CN", { hour: "2-digit", minute: "2-digit" });
  return date.toLocaleDateString("zh-CN", { month: "numeric", day: "numeric" });
}
