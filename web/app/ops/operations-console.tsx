"use client";

import { FormEvent, useCallback, useEffect, useMemo, useRef, useState } from "react";
import Image from "next/image";
import Link from "next/link";

import { ConfigurationPanel } from "./configuration-panel";
import { AgentStudioPanel } from "./agent-studio-panel";
import { LogCenterPanel, LogFocus } from "./log-center-panel";
import { IncidentCenterPanel } from "./incident-center-panel";
import { PerformancePanel } from "./performance-panel";
import { OnboardingGuide } from "../onboarding-guide";
import { adminGuideTopics, type AdminGuideTarget } from "../onboarding-content";
import { adminFetch, adminHeaders, adminCredentials, loginWithPasskey, registerPasskey, logoutAdmin, passkeyError, type AdminCredentials } from "./admin-auth";
import { AdminSecurityPanel } from "./admin-security-panel";
import styles from "./operations.module.css";



type Credentials = AdminCredentials;
type OperatorBootstrap = {
  operator: { actor: string; role: string; mfa_verified: boolean; legacy: boolean };
  capabilities: Record<string, boolean>;
  integrations?: {
    langfuse?: {
      enabled: boolean;
      configured: boolean;
      base_url: string;
      sample_rate: number;
      capture_content: boolean;
      responsibility: string;
    };
    loki?: {
      enabled: boolean;
      configured: boolean;
      base_url: string;
      responsibility: string;
      storage: string;
      fallback: string;
    };
  };
};
type RunSummary = {
  id: string;
  thread_id: string;
  user_id: string;
  conversation_id: string;
  module: "companion" | "life" | "work";
  graph_name: string;
  graph_version: string;
  agent_definition_key?: string;
  agent_definition_version_id?: string;
  agent_definition_version?: number;
  agent_definition_revision?: number;
  agent_definition_fingerprint?: string;
  model_profile_key?: string;
  model_profile_version_id?: string;
  model_profile_revision?: number;
  model_profile_config_version?: string;
  model_profile_fingerprint?: string;
  status: string;
  current_node?: string;
  revision: number;
  model_calls: number;
  prompt_tokens: number;
  completion_tokens: number;
  cost_micros: number;
  tool_calls: number;
  error_code?: string;
  error_message?: string;
  duration_ms: number;
  deadline_at: string;
  created_at: string;
  updated_at: string;
  completed_at?: string;
};
type Reliability = {
  level: string;
  reason: string;
  queue_lag: number;
  oldest_job_age_ms: number;
  model_error_rate: number;
  p95_latency_ms: number;
  observed_at: string;
  agent_runs: Record<string, number>;
  model_usage_recent: { model_calls?: number; model_cost_micros?: number };
};
type NodeTrace = { node?: string; status?: string; duration_ms?: number; details?: Record<string, unknown> };
type ModelCall = {
  graph_node?: string;
  role?: string;
  status?: string;
  provider?: string;
  requested_model?: string;
  returned_model?: string;
  prompt_tokens?: number;
  completion_tokens?: number;
  cached_tokens?: number;
  cost_micros?: number;
  latency_ms?: number;
  error_status?: string;
};
type ToolCall = {
  id: string;
  tool_name: string;
  risk_level: string;
  status: string;
  error_code?: string;
  created_at: string;
};
type RunEvent = {
  id: number;
  sequence: number;
  type: string;
  created_at: string;
  metadata?: Record<string, unknown>;
};
type RunDetail = {
  run: RunSummary;
  graph: Record<string, unknown>;
  budget: { limits?: Record<string, unknown>; usage?: Record<string, unknown> };
  model_manifest: Record<string, unknown>;
  model_calls: ModelCall[];
  node_trace: NodeTrace[];
  langfuse?: { enabled: boolean; trace_id?: string; initialized?: boolean; capture_content?: boolean; environment?: string };
  tool_calls: ToolCall[];
  events: RunEvent[];
};
type Filters = { status: string; module: string; query: string };

const initialFilters: Filters = { status: "", module: "", query: "" };
const activeStatuses = new Set(["accepted", "queued", "running", "waiting_approval", "waiting_tool", "cancel_requested"]);

export function OperationsConsole() {
  const [credentials, setCredentials] = useState<Credentials | null>(null);
  const [bootstrap, setBootstrap] = useState<OperatorBootstrap | null>(null);
  const [runs, setRuns] = useState<RunSummary[]>([]);
  const [reliability, setReliability] = useState<Reliability | null>(null);
  const [filters, setFilters] = useState<Filters>(initialFilters);
  const [appliedFilters, setAppliedFilters] = useState<Filters>(initialFilters);
  const [selectedID, setSelectedID] = useState("");
  const [detail, setDetail] = useState<RunDetail | null>(null);
  const [loading, setLoading] = useState(false);
  const [detailLoading, setDetailLoading] = useState(false);
  const [autoRefresh, setAutoRefresh] = useState(true);
  const [error, setError] = useState("");
  const [lastUpdated, setLastUpdated] = useState<Date | null>(null);
  const [view, setView] = useState<AdminGuideTarget>("observability");
  const [logFocus, setLogFocus] = useState<LogFocus | undefined>();
  const [restoring, setRestoring] = useState(true);
  const [invite, setInvite] = useState("");
  const [securityOpen, setSecurityOpen] = useState(false);
  const invitationRef = useRef<string | null>(null);

  const loadDashboard = useCallback(async (auth: Credentials, requested: Filters) => {
    const query = new URLSearchParams({ limit: "100" });
    if (requested.status) query.set("status", requested.status);
    if (requested.module) query.set("module", requested.module);
    if (requested.query.trim()) query.set("q", requested.query.trim());
    const [identity, runPage, health] = await Promise.all([
      opsFetch<OperatorBootstrap>("/v1/ops/console/bootstrap", auth),
      opsFetch<{ items: RunSummary[] }>(`/v1/ops/agent-runs?${query}`, auth),
      opsFetch<Reliability>("/v1/ops/reliability", auth),
    ]);
    setBootstrap(identity);
    setRuns(runPage.items);
    setReliability(health);
    setLastUpdated(new Date());
  }, []);

  useEffect(() => {
    let active = true;
    async function restore() {
      invitationRef.current ??= new URLSearchParams(window.location.hash.slice(1)).get("invite") ?? "";
      const invitation = invitationRef.current;
      if (invitation) {
        window.history.replaceState(null, "", window.location.pathname + window.location.search);
        setInvite(invitation);
        setRestoring(false);
        return;
      }
      try {
        const response = await adminFetch("/v1/ops/console/bootstrap");
        if (response.ok && active) {
          await loadDashboard(adminCredentials, initialFilters);
          if (active) setCredentials(adminCredentials);
        } else if (response.status !== 401 && active) {
          const payload = await response.json() as { message?: string };
          setError(payload.message ?? "无法恢复后台会话");
        }
      } catch (cause) {
        if (active) setError(passkeyError(cause));
      } finally {
        if (active) setRestoring(false);
      }
    }
    void restore();
    return () => { active = false; };
  }, [loadDashboard]);

  useEffect(() => {
    const expired = () => {
      if (!credentials) return;
      setCredentials(null); setBootstrap(null); setRuns([]); setDetail(null); setSecurityOpen(false);
      setError("后台会话已过期，请重新使用通行密钥登录。");
    };
    window.addEventListener("admin-session-expired", expired);
    return () => window.removeEventListener("admin-session-expired", expired);
  }, [credentials]);

  const refresh = useCallback(async () => {
    if (!credentials) return;
    setLoading(true);
    setError("");
    try {
      await loadDashboard(credentials, appliedFilters);
    } catch (cause) {
      setError(errorMessage(cause));
    } finally {
      setLoading(false);
    }
  }, [appliedFilters, credentials, loadDashboard]);

  const openRun = useCallback(async (runID: string, auth = credentials) => {
    if (!auth) return;
    setSelectedID(runID);
    setDetailLoading(true);
    setError("");
    try {
      setDetail(await opsFetch<RunDetail>(`/v1/ops/agent-runs/${runID}`, auth));
    } catch (cause) {
      setDetail(null);
      setError(errorMessage(cause));
    } finally {
      setDetailLoading(false);
    }
  }, [credentials]);

  useEffect(() => {
    if (!credentials || !autoRefresh) return;
    const timer = window.setInterval(() => {
      void loadDashboard(credentials, appliedFilters).catch((cause: unknown) => setError(errorMessage(cause)));
      if (selectedID) void opsFetch<RunDetail>(`/v1/ops/agent-runs/${selectedID}`, credentials).then(setDetail).catch(() => undefined);
    }, 15_000);
    return () => window.clearInterval(timer);
  }, [appliedFilters, autoRefresh, credentials, loadDashboard, selectedID]);

  async function connect(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setLoading(true);
    setError("");
    try {
      await loginWithPasskey();
      await loadDashboard(adminCredentials, initialFilters);
      setCredentials(adminCredentials);
    } catch (cause) {
      setError(passkeyError(cause));
    } finally {
      setLoading(false);
    }
  }

  async function bindInvite() {
    if (!invite.trim()) { setError("请输入管理员提供的一次性邀请码"); return; }
    setLoading(true); setError("");
    try {
      await registerPasskey(invite.trim());
      setInvite("");
      await loadDashboard(adminCredentials, initialFilters);
      setCredentials(adminCredentials);
    } catch (cause) { setError(passkeyError(cause)); }
    finally { setLoading(false); }
  }

  function applyFilters(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setAppliedFilters(filters);
    setSelectedID("");
    setDetail(null);
    if (credentials) {
      setLoading(true);
      setError("");
      void loadDashboard(credentials, filters)
        .catch((cause: unknown) => setError(errorMessage(cause)))
        .finally(() => setLoading(false));
    }
  }

  async function disconnect() {
    try { await logoutAdmin(); } catch (cause) { setError(passkeyError(cause)); return; }
    setCredentials(null);
    setBootstrap(null);
    setRuns([]);
    setReliability(null);
    setDetail(null);
    setSelectedID("");
    setError("");
    setSecurityOpen(false);
  }

  const pageStats = useMemo(() => ({
    active: runs.filter((run) => activeStatuses.has(run.status)).length,
    failed: runs.filter((run) => run.status === "failed" || run.status === "timed_out").length,
    cost: runs.reduce((sum, run) => sum + run.cost_micros, 0),
  }), [runs]);

  if (!credentials || !bootstrap) {
    return <LoginPanel onConnect={connect} onBind={() => void bindInvite()} invite={invite} onInvite={setInvite} loading={loading || restoring} error={error} />;
  }

  return (
    <main className={styles.page}>
      <aside className={styles.sidebar}>
        <div className={styles.brand}>
          <Image className={styles.brandLogo} src="/icons/logo.png" width={46} height={46} priority alt="" />
          <div><strong>伴AI</strong><small>管理与观测中心</small></div>
        </div>
        <nav aria-label="管理功能">
          <button type="button" className={view === "observability" ? styles.activeNav : undefined} onClick={() => setView("observability")}><span>◉</span>运行观测</button>
          <button type="button" className={view === "logs" ? styles.activeNav : undefined} onClick={() => { setLogFocus(undefined); setView("logs"); }}><span>⌁</span>日志中心</button>
          <button type="button" className={view === "incidents" ? styles.activeNav : undefined} onClick={() => setView("incidents")}><span>!</span>告警与事故</button>
          <button type="button" className={view === "performance" ? styles.activeNav : undefined} onClick={() => setView("performance")}><span>↗</span>成本与质量</button>
          <button type="button" className={view === "configuration" ? styles.activeNav : undefined} onClick={() => setView("configuration")}><span>◇</span>配置中心</button>
          <button type="button" className={view === "studio" ? styles.activeNav : undefined} onClick={() => setView("studio")}><span>✦</span>Agent Studio</button>
        </nav>
        <div className={styles.sidebarFooter}>
          <span className={styles.liveDot} /> API 已连接
          <small>{bootstrap.operator.actor} · {bootstrap.operator.role}</small>
          <button type="button" onClick={() => setSecurityOpen(!securityOpen)}>账号与通行密钥</button>
          <button type="button" onClick={() => void disconnect()}>退出登录</button>
        </div>
      </aside>

      <section className={styles.workspace} id="overview">
        {securityOpen && <AdminSecurityPanel role={bootstrap.operator.role} onClose={() => setSecurityOpen(false)} />}
        <div className={styles.guideToolbar}>
        <OnboardingGuide
          scope="admin"
          topics={adminGuideTopics}
          autoStart
          toolbar
          currentTopic={view}
          onNavigate={(target) => { if (target === "logs") setLogFocus(undefined); setView(target); }}
        />
        </div>
        {view === "logs" ? (
          <LogCenterPanel credentials={credentials} focus={logFocus} onOpenRun={(runID) => { setView("observability"); void openRun(runID); }} />
        ) : view === "incidents" ? (
          <IncidentCenterPanel credentials={credentials} operator={bootstrap.operator} onOpenLogs={(focus) => { setLogFocus(focus); setView("logs"); }} />
        ) : view === "performance" ? (
          <PerformancePanel credentials={credentials} operator={bootstrap.operator} />
        ) : view === "configuration" ? (
          <ConfigurationPanel credentials={credentials} operator={bootstrap.operator} />
        ) : view === "studio" ? (
          <AgentStudioPanel credentials={credentials} operator={bootstrap.operator} />
        ) : <>
        <header className={styles.topbar}>
          <div>
            <p>OPERATIONS / AGENT RUNTIME</p>
            <h1>运行观测</h1>
          </div>
          <div className={styles.actions}>
            <label className={styles.refreshToggle}>
              <input type="checkbox" checked={autoRefresh} onChange={(event) => setAutoRefresh(event.target.checked)} />
              <span />15 秒刷新
            </label>
            <button type="button" onClick={() => void refresh()} disabled={loading}>{loading ? "同步中" : "立即刷新"}</button>
          </div>
        </header>

        {error && <div className={styles.errorBanner} role="alert"><strong>请求未完成</strong><span>{error}</span></div>}

        <section className={styles.healthStrip} aria-label="系统状态">
          <div className={styles.healthLead}>
            <span className={reliability?.level === "L0" ? styles.healthyPulse : styles.warningPulse} />
            <div><small>当前保护级别</small><strong>{reliability?.level ?? "—"}</strong></div>
          </div>
          <p>{reliability?.reason || "尚无可靠性采样"}</p>
          <small>{lastUpdated ? `更新于 ${formatClock(lastUpdated)}` : "等待同步"}</small>
        </section>

        <section className={styles.integrationStrip} aria-label="LLM 与 Agent 专项观测">
          <div>
            <i data-enabled={bootstrap.integrations?.langfuse?.enabled && bootstrap.integrations.langfuse.configured} />
            <span><small>LLM / AGENT OBSERVABILITY</small><strong>Langfuse</strong></span>
          </div>
          <p>{bootstrap.integrations?.langfuse?.enabled ? `已接入 · ${Math.round((bootstrap.integrations.langfuse.sample_rate ?? 1) * 100)}% 采样 · ${bootstrap.integrations.langfuse.capture_content ? "正文采集已开启" : "默认仅上传脱敏元数据"}` : "未启用；本地运行证据与系统监控继续正常工作"}</p>
          {bootstrap.integrations?.langfuse?.enabled && bootstrap.integrations.langfuse.base_url
            ? <a href={bootstrap.integrations.langfuse.base_url} target="_blank" rel="noreferrer">打开 Langfuse ↗</a>
            : <span>可选集成</span>}
        </section>

        <section className={styles.integrationStrip} aria-label="跨服务结构化日志">
          <div>
            <i data-enabled={bootstrap.integrations?.loki?.enabled && bootstrap.integrations.loki.configured} />
            <span><small>SYSTEM LOG OBSERVABILITY</small><strong>Loki + Alloy</strong></span>
          </div>
          <p>{bootstrap.integrations?.loki?.enabled ? "已接入 · JSON 结构化采集 · Run / Trace / Agent Trace 可关联查询" : "未启用；日志中心继续使用 PostgreSQL 持久化记录"}</p>
          <span>{bootstrap.integrations?.loki?.enabled ? "PostgreSQL 自动回退" : "可选集成"}</span>
        </section>

        <section className={styles.kpiGrid} aria-label="运行指标">
          <MetricCard label="活跃运行" value={String(pageStats.active)} note={`当前页共 ${runs.length} 条`} tone="blue" />
          <MetricCard label="失败 / 超时" value={String(pageStats.failed)} note={percent(pageStats.failed, runs.length)} tone={pageStats.failed ? "red" : "green"} />
          <MetricCard label="模型 P95" value={formatDuration(reliability?.p95_latency_ms ?? 0)} note={`错误率 ${formatRate(reliability?.model_error_rate ?? 0)}`} tone="violet" />
          <MetricCard label="本页模型成本" value={formatCost(pageStats.cost)} note={`${runs.reduce((sum, run) => sum + run.model_calls, 0)} 次调用`} tone="amber" />
        </section>

        <section className={styles.runPanel}>
          <div className={styles.panelHeading}>
            <div><p>TRACE EXPLORER</p><h2>Agent Runs</h2></div>
            <span>{runs.length} 条结果</span>
          </div>
          <form className={styles.filters} onSubmit={applyFilters}>
            <label className={styles.searchField}><span>⌕</span><input aria-label="搜索运行" value={filters.query} onChange={(event) => setFilters({ ...filters, query: event.target.value })} placeholder="运行、线程或会话 ID" /></label>
            <select aria-label="状态" value={filters.status} onChange={(event) => setFilters({ ...filters, status: event.target.value })}>
              <option value="">全部状态</option><option value="running">运行中</option><option value="waiting_approval">等待审批</option><option value="waiting_tool">等待工具</option><option value="completed">已完成</option><option value="failed">失败</option><option value="timed_out">超时</option><option value="cancelled">已取消</option>
            </select>
            <select aria-label="模块" value={filters.module} onChange={(event) => setFilters({ ...filters, module: event.target.value })}>
              <option value="">全部模块</option><option value="companion">陪伴</option><option value="life">生活</option><option value="work">工作</option>
            </select>
            <button type="submit">应用筛选</button>
          </form>
          <RunTable runs={runs} selectedID={selectedID} onSelect={(id) => void openRun(id)} />
        </section>

        {(selectedID || detailLoading) && <RunInspector detail={detail} loading={detailLoading} langfuseBaseURL={bootstrap.integrations?.langfuse?.base_url ?? ""} onClose={() => { setSelectedID(""); setDetail(null); }} />}
        </>}
      </section>
    </main>
  );
}

function LoginPanel({ onConnect, onBind, invite, onInvite, loading, error }: { onConnect: (event: FormEvent<HTMLFormElement>) => void; onBind: () => void; invite: string; onInvite: (value: string) => void; loading: boolean; error: string }) {
  return (
    <main className={styles.loginPage}>
      <section className={styles.loginCard} aria-labelledby="admin-login-title">
        <div className={styles.loginBrand}>
          <Image className={styles.brandLogo} src="/icons/logo.png" width={46} height={46} priority alt="" />
          <div><strong>伴AI</strong><small>管理与观测中心</small></div>
        </div>
        <div className={styles.loginIntro}>
          <p>ADMIN ACCESS</p>
          <h1 id="admin-login-title">欢迎回来</h1>
          <span>使用通行密钥登录，在系统弹窗中选择设备 PIN 完成验证。</span>
        </div>
        <form className={styles.loginForm} onSubmit={onConnect}>
          {error && <p className={styles.loginError} role="alert">{error}</p>}
          <button type="submit" disabled={loading}>{loading ? "正在验证…" : "使用通行密钥登录"}</button>
          <small>PIN 由设备本地验证。可用验证方式由系统决定；无需向本站提供设备 PIN。</small>
          <details open={invite ? true : undefined}>
            <summary>首次使用？绑定管理员邀请</summary>
            <label>一次性邀请码<input type="password" autoComplete="off" value={invite} onChange={(event) => onInvite(event.target.value)} spellCheck={false} placeholder="粘贴管理员提供的邀请码" /></label>
            <button type="button" onClick={onBind} disabled={loading || !invite.trim()}>绑定通行密钥并登录</button>
            <small>邀请在 30 分钟后失效，每次绑定尝试消耗一次邀请。</small>
          </details>
          <Link className={styles.userEntryLink} href="/">← 返回普通用户登录</Link>
        </form>
        <OnboardingGuide scope="admin" topics={adminGuideTopics} compact />
      </section>
    </main>
  );
}

function MetricCard({ label, value, note, tone }: { label: string; value: string; note: string; tone: string }) {
  return <article className={`${styles.metricCard} ${styles[tone]}`}><div><span>{label}</span><i /></div><strong>{value}</strong><small>{note}</small></article>;
}

function RunTable({ runs, selectedID, onSelect }: { runs: RunSummary[]; selectedID: string; onSelect: (id: string) => void }) {
  if (!runs.length) return <div className={styles.emptyState}><strong>没有匹配的运行</strong><span>尝试调整筛选条件，或等待新的 Agent Run。</span></div>;
  return (
    <div className={styles.tableScroll}>
      <table className={styles.runTable}>
        <thead><tr><th>运行 / 模块</th><th>状态</th><th>当前节点</th><th>模型用量</th><th>成本</th><th>耗时</th><th>开始时间</th><th /></tr></thead>
        <tbody>{runs.map((run) => (
          <tr key={run.id} className={selectedID === run.id ? styles.selectedRow : undefined}>
            <td><strong className={styles.mono}>{shortID(run.id)}</strong><small>{moduleName(run.module)} · {run.graph_version}</small></td>
            <td><StatusBadge status={run.status} /></td>
            <td><span className={styles.nodeName}>{run.current_node || "—"}</span><small>{run.tool_calls} 次工具调用</small></td>
            <td><strong>{compactNumber(run.prompt_tokens + run.completion_tokens)}</strong><small>{run.model_calls} 次模型调用</small></td>
            <td><strong>{formatCost(run.cost_micros)}</strong></td>
            <td><strong>{formatDuration(run.duration_ms)}</strong></td>
            <td><strong>{formatDate(run.created_at)}</strong><small>{formatClock(new Date(run.created_at))}</small></td>
            <td><button type="button" aria-label={`查看运行 ${run.id}`} onClick={() => onSelect(run.id)}>→</button></td>
          </tr>
        ))}</tbody>
      </table>
    </div>
  );
}

function RunInspector({ detail, loading, langfuseBaseURL, onClose }: { detail: RunDetail | null; loading: boolean; langfuseBaseURL: string; onClose: () => void }) {
  return (
    <section className={styles.inspector} aria-live="polite">
      <div className={styles.inspectorHeading}>
        <div><p>RUN INSPECTOR</p><h2>{detail ? shortID(detail.run.id) : "加载运行详情"}</h2>{detail && <span className={styles.mono}>{detail.run.id}</span>}</div>
        <button type="button" onClick={onClose}>关闭 ×</button>
      </div>
      {loading && <div className={styles.detailLoading}>正在装载节点轨迹与模型调用…</div>}
      {!loading && detail && <>
        <div className={styles.detailMeta}>
          <div><small>状态</small><StatusBadge status={detail.run.status} /></div>
          <div><small>图版本</small><strong>{String(detail.graph.name ?? detail.run.graph_name)}@{String(detail.graph.version ?? detail.run.graph_version)}</strong></div>
          <div><small>会话</small><strong className={styles.mono}>{shortID(detail.run.conversation_id)}</strong></div>
          <div><small>总耗时</small><strong>{formatDuration(detail.run.duration_ms)}</strong></div>
        </div>
        <div className={styles.detailGrid}>
          <article className={styles.traceCard}>
            <div className={styles.subheading}><div><p>EXECUTION FLOW</p><h3>节点轨迹</h3></div><span>{detail.node_trace.length} 节点</span></div>
            <NodeTimeline nodes={detail.node_trace} />
          </article>
          <article className={styles.budgetCard}>
            <div className={styles.subheading}><div><p>GUARDRAILS</p><h3>预算使用</h3></div></div>
            <BudgetUsage budget={detail.budget} />
            <div className={styles.manifest}><small>Agent 定义</small><strong>{detail.run.agent_definition_key ?? "内置受治理图"}</strong><span>{detail.run.agent_definition_version ? `v${detail.run.agent_definition_version}` : "builtin"}{detail.run.agent_definition_revision ? ` · rev ${detail.run.agent_definition_revision}` : ""}</span></div>
            <div className={styles.manifest}><small>本次模型配置</small><strong>{String(detail.model_manifest.provider ?? "未记录")}</strong><span>{detail.run.model_profile_config_version ?? String(detail.model_manifest.config_version ?? "—")}{detail.run.model_profile_revision ? ` · rev ${detail.run.model_profile_revision}` : ""}</span></div>
            <div className={styles.langfuseTrace} data-enabled={detail.langfuse?.enabled === true}>
              <span><small>Langfuse Trace</small><strong>{detail.langfuse?.enabled ? (detail.langfuse.initialized ? "本地观测已建立" : "观测初始化失败") : "本次未启用"}</strong></span>
              {detail.langfuse?.trace_id && <code>{detail.langfuse.trace_id}</code>}
              {detail.langfuse?.enabled && langfuseBaseURL && <a href={langfuseBaseURL} target="_blank" rel="noreferrer">查看专项观测 ↗</a>}
            </div>
          </article>
        </div>
        <article className={styles.callsCard}>
          <div className={styles.subheading}><div><p>MODEL INVOCATIONS</p><h3>模型调用</h3></div><span>{detail.model_calls.length} 次</span></div>
          <ModelCallTable calls={detail.model_calls} />
        </article>
        <div className={styles.detailGrid}>
          <article className={styles.callsCard}><div className={styles.subheading}><div><p>SIDE EFFECTS</p><h3>工具调用</h3></div><span>{detail.tool_calls.length} 次</span></div><ToolCallList calls={detail.tool_calls} /></article>
          <article className={styles.callsCard}><div className={styles.subheading}><div><p>STATE CHANGES</p><h3>运行事件</h3></div><span>{detail.events.length} 条</span></div><EventList events={detail.events} /></article>
        </div>
      </>}
    </section>
  );
}

function NodeTimeline({ nodes }: { nodes: NodeTrace[] }) {
  if (!nodes.length) return <EmptyLine text="该运行尚未写入节点轨迹" />;
  const max = Math.max(1, ...nodes.map((node) => node.duration_ms ?? 0));
  return <ol className={styles.timeline}>{nodes.map((node, index) => <li key={`${node.node}-${index}`}><i data-status={node.status} /><div><strong>{node.node || "unknown"}</strong><small>{node.status || "unknown"}</small></div><span><b style={{ width: `${Math.max(4, ((node.duration_ms ?? 0) / max) * 100)}%` }} /></span><em>{formatDuration(node.duration_ms ?? 0)}</em></li>)}</ol>;
}

function BudgetUsage({ budget }: { budget: RunDetail["budget"] }) {
  const limits = budget.limits ?? {};
  const usage = budget.usage ?? {};
  const keys = ["model_calls", "tool_calls", "actions", "repair_model_calls"].filter((key) => key in limits || key in usage);
  if (!keys.length) return <EmptyLine text="该运行尚未写入预算数据" />;
  return <div className={styles.budgetList}>{keys.map((key) => {
    const used = numberValue(usage[key]); const limit = numberValue(limits[`max_${key}`] ?? limits[key]);
    const ratio = limit > 0 ? Math.min(100, (used / limit) * 100) : 0;
    return <div key={key}><span><strong>{labelForBudget(key)}</strong><small>{used} / {limit || "—"}</small></span><i><b style={{ width: `${ratio}%` }} /></i></div>;
  })}</div>;
}

function ModelCallTable({ calls }: { calls: ModelCall[] }) {
  if (!calls.length) return <EmptyLine text="本次运行没有模型调用" />;
  return <div className={styles.callRows}>{calls.map((call, index) => <div key={`${call.graph_node}-${index}`}><span><strong>{call.role || call.graph_node || "model"}</strong><small>{call.graph_node || "—"}</small></span><span><strong>{call.returned_model || call.requested_model || "—"}</strong><small>{call.provider || "—"}</small></span><span><strong>{compactNumber((call.prompt_tokens ?? 0) + (call.completion_tokens ?? 0))} tokens</strong><small>{call.prompt_tokens ?? 0} 输入 / {call.completion_tokens ?? 0} 输出</small></span><span><strong>{formatDuration(call.latency_ms ?? 0)}</strong><small>{formatCost(call.cost_micros ?? 0)}</small></span><StatusBadge status={call.status || call.error_status || "unknown"} /></div>)}</div>;
}

function ToolCallList({ calls }: { calls: ToolCall[] }) {
  if (!calls.length) return <EmptyLine text="本次运行没有工具调用" />;
  return <ul className={styles.compactList}>{calls.map((call) => <li key={call.id}><i data-status={call.status} /><span><strong>{call.tool_name}</strong><small>{call.risk_level} risk · {formatClock(new Date(call.created_at))}</small></span><StatusBadge status={call.status} /></li>)}</ul>;
}

function EventList({ events }: { events: RunEvent[] }) {
  if (!events.length) return <EmptyLine text="该运行尚无状态事件" />;
  return <ul className={styles.compactList}>{events.map((event) => <li key={event.id}><i data-status={event.type} /><span><strong>{event.type}</strong><small>#{event.sequence} · {formatClock(new Date(event.created_at))}</small></span></li>)}</ul>;
}

function StatusBadge({ status }: { status: string }) {
  const category = statusCategory(status);
  return <span className={`${styles.status} ${styles[category]}`}><i />{statusLabel(status)}</span>;
}

function EmptyLine({ text }: { text: string }) { return <div className={styles.emptyLine}>{text}</div>; }

async function opsFetch<T>(path: string, credentials: Credentials): Promise<T> {
  const headers: Record<string, string> = { ...adminHeaders(credentials) };
  const response = await adminFetch(`${path}`, { headers, cache: "no-store" });
  const payload = await response.json().catch(() => ({})) as { message?: string };
  if (!response.ok) throw new Error(payload.message || `请求失败（${response.status}）`);
  return payload as T;
}

function errorMessage(cause: unknown) { return cause instanceof Error ? cause.message : "服务暂时不可用"; }
function numberValue(value: unknown) { return typeof value === "number" && Number.isFinite(value) ? value : 0; }
function shortID(id: string) { return id.length > 12 ? `${id.slice(0, 8)}…${id.slice(-4)}` : id; }
function compactNumber(value: number) { return new Intl.NumberFormat("zh-CN", { notation: value > 9999 ? "compact" : "standard", maximumFractionDigits: 1 }).format(value); }
function formatRate(value: number) { return `${(value * 100).toFixed(value > 0.01 ? 1 : 2)}%`; }
function percent(value: number, total: number) { return total ? `当前结果的 ${((value / total) * 100).toFixed(1)}%` : "当前结果的 0%"; }
function formatCost(micros: number) { return `$${(micros / 1_000_000).toFixed(micros >= 10_000 ? 3 : 5)}`; }
function formatDuration(ms: number) { if (ms < 1000) return `${Math.max(0, Math.round(ms))}ms`; if (ms < 60_000) return `${(ms / 1000).toFixed(ms < 10_000 ? 2 : 1)}s`; return `${Math.floor(ms / 60_000)}m ${Math.round((ms % 60_000) / 1000)}s`; }
function formatDate(value: string) { return new Intl.DateTimeFormat("zh-CN", { month: "2-digit", day: "2-digit" }).format(new Date(value)); }
function formatClock(value: Date) { return new Intl.DateTimeFormat("zh-CN", { hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false }).format(value); }
function moduleName(module: string) { return ({ companion: "陪伴", life: "生活", work: "工作" } as Record<string, string>)[module] ?? module; }
function labelForBudget(key: string) { return ({ model_calls: "模型调用", tool_calls: "工具调用", actions: "行动次数", repair_model_calls: "修复调用" } as Record<string, string>)[key] ?? key; }
function statusCategory(status: string) { if (["completed", "succeeded", "ready"].includes(status)) return "statusGood"; if (["failed", "timed_out", "blocked"].includes(status)) return "statusBad"; if (["running", "accepted", "queued"].includes(status)) return "statusActive"; if (["waiting_approval", "waiting_tool", "cancel_requested"].includes(status)) return "statusWait"; return "statusMuted"; }
function statusLabel(status: string) { return ({ accepted: "已接受", queued: "排队中", running: "运行中", waiting_approval: "等待审批", waiting_tool: "等待工具", completed: "已完成", failed: "失败", timed_out: "超时", cancel_requested: "取消中", cancelled: "已取消", succeeded: "成功", proposed: "待处理", approved: "已审批" } as Record<string, string>)[status] ?? status; }
