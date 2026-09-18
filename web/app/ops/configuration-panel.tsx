"use client";

import { FormEvent, useCallback, useEffect, useMemo, useState } from "react";

import { BillingUsagePanel } from "./billing-usage-panel";
import { RuntimeConvergencePanel } from "./runtime-convergence-panel";
import { adminFetch, adminHeaders, type AdminCredentials } from "./admin-auth";
import styles from "./operations.module.css";



type Credentials = AdminCredentials;
type Operator = { actor: string; role: string; mfa_verified: boolean; legacy: boolean };
type ConfigVersion = {
  id: string;
  kind: "billing_plan" | "model_profile";
  key: string;
  version: number;
  base_version: number;
  status: string;
  payload: Record<string, unknown>;
  fingerprint: string;
  reason: string;
  created_by: string;
  submitted_by?: string;
  published_by?: string;
  updated_at: string;
};
type Deployment = { environment: string; key: string; revision: number; deployed_at: string; deployed_by: string; version: ConfigVersion };
type ConfigPage = { versions: ConfigVersion[]; deployments: Deployment[] };
type Provider = { key: string; provider_type: string; display_name: string; base_url: string; credential_ref: string; status: string };
type Model = { model_id: string; provider_key: string; display_name: string; model_class: string; context_window: number; max_output_tokens: number; zero_price: boolean; status: string };
type Limit = { resource: string; limit: number };
type PlanPayload = { display_name?: string; status?: string; effective_mode?: string; limits?: Limit[]; allowed_model_classes?: string[]; allowed_skills?: string[] };
type PlanDraft = { displayName: string; documents: string; skillRuns: string; workspaces: string; agentRuns: string; modelCost: string; modelClasses: string[]; reason: string };

const emptyPlan: PlanDraft = { displayName: "", documents: "", skillRuns: "", workspaces: "", agentRuns: "", modelCost: "", modelClasses: ["free"], reason: "" };

export function ConfigurationPanel({ credentials, operator }: { credentials: Credentials; operator: Operator }) {
  const [tab, setTab] = useState<"plans" | "models" | "usage" | "runtime">("plans");
  const [plans, setPlans] = useState<ConfigPage>({ versions: [], deployments: [] });
  const [profiles, setProfiles] = useState<ConfigPage>({ versions: [], deployments: [] });
  const [providers, setProviders] = useState<Provider[]>([]);
  const [models, setModels] = useState<Model[]>([]);
  const [selectedPlan, setSelectedPlan] = useState("free");
  const [planDraft, setPlanDraft] = useState<PlanDraft>(emptyPlan);
  const [profileText, setProfileText] = useState("");
  const [profileReason, setProfileReason] = useState("");
  const [loading, setLoading] = useState(true);
  const [working, setWorking] = useState("");
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      const [planPage, profilePage, providerPage, modelPage] = await Promise.all([
        configFetch<ConfigPage>("/v1/ops/billing/plans", credentials),
        configFetch<ConfigPage>("/v1/ops/model-profiles", credentials),
        configFetch<{ providers: Provider[] }>("/v1/ops/model/providers", credentials),
        configFetch<{ models: Model[] }>("/v1/ops/model/catalog", credentials),
      ]);
      setPlans(planPage);
      setProfiles(profilePage);
      setProviders(providerPage.providers);
      setModels(modelPage.models);
    } catch (cause) {
      setError(configError(cause));
    } finally {
      setLoading(false);
    }
  }, [credentials]);

  useEffect(() => {
    let cancelled = false;
    void fetchConfiguration(credentials).then(({ planPage, profilePage, providerPage, modelPage }) => {
      if (cancelled) return;
      setPlans(planPage);
      setProfiles(profilePage);
      setProviders(providerPage.providers);
      setModels(modelPage.models);
      const initialPlan = planPage.deployments.find((item) => item.key === "free")?.version ?? latestVersion(planPage.versions, "free");
      if (initialPlan) setPlanDraft(planDraftFromVersion(initialPlan));
      const initialProfile = profilePage.deployments[0]?.version ?? profilePage.versions[0];
      if (initialProfile) setProfileText(JSON.stringify(initialProfile.payload, null, 2));
      setLoading(false);
    }).catch((cause: unknown) => {
      if (cancelled) return;
      setError(configError(cause));
      setLoading(false);
    });
    return () => { cancelled = true; };
  }, [credentials]);

  const planKeys = useMemo(() => Array.from(new Set([...plans.deployments.map((item) => item.key), ...plans.versions.map((item) => item.key)])).sort(), [plans]);
  const latestPlan = useMemo(() => latestVersion(plans.versions, selectedPlan), [plans.versions, selectedPlan]);
  const activePlan = useMemo(() => plans.deployments.find((item) => item.key === selectedPlan)?.version ?? latestPlan, [latestPlan, plans.deployments, selectedPlan]);

  function choosePlan(key: string) {
    setSelectedPlan(key);
    const version = plans.deployments.find((item) => item.key === key)?.version ?? latestVersion(plans.versions, key);
    if (version) setPlanDraft(planDraftFromVersion(version));
  }

  async function createPlan(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const optionalLimits: Limit[] = [];
    if (planDraft.agentRuns !== "") optionalLimits.push({ resource: "agent_runs_monthly", limit: Number(planDraft.agentRuns) });
    if (planDraft.modelCost !== "") optionalLimits.push({ resource: "model_cost_micros_monthly", limit: Number(planDraft.modelCost) });
    await mutate("create-plan", `/v1/ops/billing/plans/${selectedPlan}/versions`, {
      base_version: latestPlan?.version ?? 0,
      display_name: planDraft.displayName,
      status: "active",
      effective_mode: "immediate",
      limits: [
        { resource: "documents_active", limit: Number(planDraft.documents) },
        { resource: "skill_runs_monthly", limit: Number(planDraft.skillRuns) },
        { resource: "workspaces", limit: Number(planDraft.workspaces) },
        ...optionalLimits,
      ],
      allowed_model_classes: planDraft.modelClasses,
      allowed_skills: (activePlan?.payload as PlanPayload | undefined)?.allowed_skills ?? [],
      reason: planDraft.reason,
    });
  }

  async function createProfile(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    try {
      const payload = JSON.parse(profileText) as Record<string, unknown>;
      await mutate("create-profile", "/v1/ops/model-profiles/production-default/versions", {
        base_version: latestVersion(profiles.versions, "production-default")?.version ?? 0,
        ...payload,
        reason: profileReason,
      });
    } catch (cause) {
      setError(cause instanceof SyntaxError ? "模型 Profile JSON 格式无效" : configError(cause));
    }
  }

  async function workflow(version: ConfigVersion, action: "validate" | "submit" | "publish") {
    await mutate(`${version.id}-${action}`, `/v1/ops/config-versions/${version.id}/${action}`);
  }

  async function mutate(key: string, path: string, body?: unknown) {
    setWorking(key);
    setError("");
    setMessage("");
    try {
      const result = await configFetch<{ runtime_message?: string; version?: ConfigVersion }>(path, credentials, { method: "POST", body });
      setMessage(result.runtime_message || "操作成功，配置工作流已更新");
      await load();
    } catch (cause) {
      setError(configError(cause));
    } finally {
      setWorking("");
    }
  }

  return <>
    <header className={styles.topbar}>
      <div><p>OPERATIONS / CONFIGURATION</p><h1>配置中心</h1></div>
      <div className={styles.actions}><span className={styles.permissionTag}>{operator.role} · {operator.mfa_verified ? "MFA 已验证" : "未验证 MFA"}</span><button type="button" onClick={() => void load()} disabled={loading}>{loading ? "同步中" : "刷新配置"}</button></div>
    </header>

    <section className={styles.configNotice}>
      <div><i />版本化控制面已启用</div>
      <p>每次变更都生成不可变版本。发布需要另一名管理员和 MFA；API 与 Agent Worker 会周期加载并上报实际版本。</p>
    </section>
    {error && <div className={styles.errorBanner} role="alert"><strong>配置操作未完成</strong><span>{error}</span></div>}
    {message && <div className={styles.successBanner} role="status">{message}</div>}

    <div className={styles.configTabs}>
      <button type="button" className={tab === "plans" ? styles.activeTab : undefined} onClick={() => setTab("plans")}>套餐与额度</button>
      <button type="button" className={tab === "models" ? styles.activeTab : undefined} onClick={() => setTab("models")}>模型服务与路由</button>
      <button type="button" className={tab === "runtime" ? styles.activeTab : undefined} onClick={() => setTab("runtime")}>多实例收敛</button>
      {operator.role !== "viewer" && <button type="button" className={tab === "usage" ? styles.activeTab : undefined} onClick={() => setTab("usage")}>用户用量账本</button>}
    </div>

    {tab === "usage" ? <BillingUsagePanel credentials={credentials} operator={operator} /> : tab === "runtime" ? <RuntimeConvergencePanel credentials={credentials} /> : tab === "plans" ? <section className={styles.configLayout}>
      <article className={styles.configListCard}>
        <div className={styles.panelHeading}><div><p>ACTIVE CATALOG</p><h2>套餐目录</h2></div><span>{planKeys.length} 个套餐</span></div>
        <div className={styles.planList}>{planKeys.map((key) => {
          const deployment = plans.deployments.find((item) => item.key === key);
          const version = deployment?.version ?? latestVersion(plans.versions, key);
          const payload = version?.payload as PlanPayload | undefined;
          return <button type="button" key={key} className={selectedPlan === key ? styles.selectedPlan : undefined} onClick={() => choosePlan(key)}><span><strong>{payload?.display_name ?? key}</strong><small>{key} · v{version?.version ?? "—"}</small></span>{deployment ? <b>已发布</b> : <em>未部署</em>}</button>;
        })}</div>
        <VersionHistory versions={plans.versions.filter((item) => item.key === selectedPlan)} operator={operator} working={working} onAction={workflow} />
      </article>

      <article className={styles.editorCard}>
        <div className={styles.panelHeading}><div><p>NEW IMMUTABLE VERSION</p><h2>调整 {selectedPlan} 套餐</h2></div><span>基于 v{latestPlan?.version ?? 0}</span></div>
        <form className={styles.configForm} onSubmit={createPlan}>
          <label className={styles.fullField}>显示名称<input value={planDraft.displayName} onChange={(event) => setPlanDraft({ ...planDraft, displayName: event.target.value })} required /></label>
          <label>活跃文档数<input type="number" min="0" value={planDraft.documents} onChange={(event) => setPlanDraft({ ...planDraft, documents: event.target.value })} required /></label>
          <label>每月 Skill Runs<input type="number" min="0" value={planDraft.skillRuns} onChange={(event) => setPlanDraft({ ...planDraft, skillRuns: event.target.value })} required /></label>
          <label>工作区数量<input type="number" min="0" value={planDraft.workspaces} onChange={(event) => setPlanDraft({ ...planDraft, workspaces: event.target.value })} required /></label>
          <label>每月 Agent Runs<input type="number" min="0" value={planDraft.agentRuns} onChange={(event) => setPlanDraft({ ...planDraft, agentRuns: event.target.value })} placeholder="可选" /></label>
          <label className={styles.fullField}>每月模型成本上限（micros）<input type="number" min="0" value={planDraft.modelCost} onChange={(event) => setPlanDraft({ ...planDraft, modelCost: event.target.value })} placeholder="可选" /></label>
          <fieldset className={styles.fullField}><legend>可用模型等级</legend>{["free", "standard", "quality"].map((value) => <label key={value}><input type="checkbox" checked={planDraft.modelClasses.includes(value)} onChange={() => setPlanDraft({ ...planDraft, modelClasses: toggleValue(planDraft.modelClasses, value) })} />{value}</label>)}</fieldset>
          <label className={styles.fullField}>变更原因<textarea value={planDraft.reason} onChange={(event) => setPlanDraft({ ...planDraft, reason: event.target.value })} required placeholder="说明业务目标、影响和回滚条件" /></label>
          <button className={styles.primaryWide} disabled={operator.role !== "admin" || working !== ""}>{working === "create-plan" ? "正在创建…" : "创建草稿版本"}</button>
        </form>
      </article>
    </section> : <section className={styles.modelSection}>
      <div className={styles.catalogGrid}>
        <article className={styles.configListCard}><div className={styles.panelHeading}><div><p>PROVIDER CONNECTIONS</p><h2>预注册服务</h2></div><span>{providers.length}</span></div>{providers.map((provider) => <div className={styles.providerRow} key={provider.key}><i /><span><strong>{provider.display_name}</strong><small>{provider.base_url}</small></span><b>{provider.status}</b></div>)}</article>
        <article className={styles.configListCard}><div className={styles.panelHeading}><div><p>APPROVED CATALOG</p><h2>模型目录</h2></div><span>{models.length}</span></div><div className={styles.modelCatalog}>{models.map((model) => <div key={model.model_id}><span><strong>{model.display_name}</strong><small>{model.model_id}</small></span><b data-class={model.model_class}>{model.model_class}{model.zero_price ? " · $0" : ""}</b></div>)}</div></article>
      </div>
      <div className={styles.configLayout}>
        <article className={styles.configListCard}><div className={styles.panelHeading}><div><p>PROFILE HISTORY</p><h2>production-default</h2></div></div><VersionHistory versions={profiles.versions.filter((item) => item.key === "production-default")} operator={operator} working={working} onAction={workflow} /></article>
        <article className={styles.editorCard}>
          <div className={styles.panelHeading}><div><p>ROUTING SNAPSHOT</p><h2>创建模型 Profile</h2></div><span>基于 v{latestVersion(profiles.versions, "production-default")?.version ?? 0}</span></div>
          <form className={styles.profileForm} onSubmit={createProfile}>
            <label>完整 Profile JSON<textarea spellCheck={false} value={profileText} onChange={(event) => setProfileText(event.target.value)} required /></label>
            <label>变更原因<input value={profileReason} onChange={(event) => setProfileReason(event.target.value)} required placeholder="模型切换、评测依据和回滚阈值" /></label>
            <button className={styles.primaryWide} disabled={operator.role !== "admin" || working !== ""}>{working === "create-profile" ? "正在创建…" : "创建 Profile 草稿"}</button>
          </form>
          <p className={styles.runtimeCaveat}>模型发布后由 Agent Worker 在轮询周期内热加载；空闲旧进程会被替换，运行中的任务使用原快照完成。可通过 Worker 的 model_config 指标确认实际版本。</p>
        </article>
      </div>
    </section>}
  </>;
}

function VersionHistory({ versions, operator, working, onAction }: { versions: ConfigVersion[]; operator: Operator; working: string; onAction: (version: ConfigVersion, action: "validate" | "submit" | "publish") => void }) {
  if (!versions.length) return <div className={styles.emptyLine}>暂无版本</div>;
  return <div className={styles.versionHistory}><h3>版本历史</h3>{versions.map((version) => {
    const action = version.status === "draft" ? "validate" : version.status === "validated" ? "submit" : version.status === "submitted" ? "publish" : null;
    const sameAuthor = action === "publish" && (operator.actor === version.created_by || operator.actor === version.submitted_by);
    return <div key={version.id}><span><strong>v{version.version}</strong><small>{shortFingerprint(version.fingerprint)} · {version.created_by}</small></span><StatusPill status={version.status} />{action && <button type="button" disabled={operator.role !== "admin" || working !== "" || sameAuthor || (action === "publish" && !operator.mfa_verified)} title={sameAuthor ? "需要另一名管理员发布" : undefined} onClick={() => onAction(version, action)}>{working === `${version.id}-${action}` ? "处理中" : ({ validate: "校验", submit: "提交", publish: "发布" } as const)[action]}</button>}</div>;
  })}</div>;
}

function StatusPill({ status }: { status: string }) { return <b className={styles.configStatus} data-status={status}>{({ draft: "草稿", validated: "已校验", submitted: "待审批", published: "已发布", superseded: "已替代" } as Record<string, string>)[status] ?? status}</b>; }

async function fetchConfiguration(credentials: Credentials) {
  const [planPage, profilePage, providerPage, modelPage] = await Promise.all([
    configFetch<ConfigPage>("/v1/ops/billing/plans", credentials),
    configFetch<ConfigPage>("/v1/ops/model-profiles", credentials),
    configFetch<{ providers: Provider[] }>("/v1/ops/model/providers", credentials),
    configFetch<{ models: Model[] }>("/v1/ops/model/catalog", credentials),
  ]);
  return { planPage, profilePage, providerPage, modelPage };
}

async function configFetch<T>(path: string, credentials: Credentials, options?: { method?: string; body?: unknown }): Promise<T> {
  const headers: Record<string, string> = { ...adminHeaders(credentials) };
  if (options?.body !== undefined) headers["Content-Type"] = "application/json";
  const response = await adminFetch(`${path}`, { method: options?.method ?? "GET", headers, body: options?.body === undefined ? undefined : JSON.stringify(options.body), cache: "no-store" });
  const payload = await response.json().catch(() => ({})) as { message?: string };
  if (!response.ok) throw new Error(payload.message || `请求失败（${response.status}）`);
  return payload as T;
}

function latestVersion(versions: ConfigVersion[], key: string) { return versions.filter((item) => item.key === key).sort((a, b) => b.version - a.version)[0]; }
function planDraftFromVersion(version: ConfigVersion): PlanDraft {
  const payload = version.payload as PlanPayload;
  const limits = Object.fromEntries((payload.limits ?? []).map((item) => [item.resource, String(item.limit)]));
  return {
    displayName: payload.display_name ?? version.key,
    documents: limits.documents_active ?? "0",
    skillRuns: limits.skill_runs_monthly ?? "0",
    workspaces: limits.workspaces ?? "0",
    agentRuns: limits.agent_runs_monthly ?? "",
    modelCost: limits.model_cost_micros_monthly ?? "",
    modelClasses: payload.allowed_model_classes ?? ["free"],
    reason: "",
  };
}
function toggleValue(values: string[], value: string) { return values.includes(value) ? values.filter((item) => item !== value) : [...values, value]; }
function shortFingerprint(value: string) { return value ? `${value.slice(0, 8)}…${value.slice(-4)}` : "—"; }
function configError(cause: unknown) { return cause instanceof Error ? cause.message : "配置中心暂时不可用"; }
