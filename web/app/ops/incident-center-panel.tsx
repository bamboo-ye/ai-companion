"use client";

import { FormEvent, useCallback, useEffect, useMemo, useState } from "react";

import type { LogFocus } from "./log-center-panel";
import { adminFetch, adminHeaders, type AdminCredentials } from "./admin-auth";
import styles from "./operations.module.css";



type Credentials = AdminCredentials;
type Operator = { actor: string; role: string; mfa_verified: boolean; legacy: boolean };
type AlertRule = { id: string; name: string; description?: string; service?: string; level: string; event_prefix?: string; window_minutes: number; threshold: number; severity: "warning" | "critical"; enabled: boolean; updated_at: string };
type Incident = { id: string; rule_id?: string; rule_name: string; source_type: "log_rule" | "performance_budget"; source_id: string; status: "open" | "acknowledged" | "resolved"; severity: "warning" | "critical"; title: string; summary: string; service?: string; level: string; event_prefix?: string; observed_value: number; threshold: number; window_minutes: number; window_started_at: string; window_ended_at: string; evidence: Record<string, unknown>; opened_at: string; acknowledged_at?: string; acknowledged_by?: string; resolved_at?: string; resolved_by?: string; resolution?: string; updated_at: string };
type Evaluation = { rules_evaluated: number; triggered: number; opened: number; updated: number; resolved: number };
type BudgetEvaluation = { budgets_evaluated: number; warning: number; exceeded: number; projected_exceeded: number; opened: number; escalated: number; resolved: number };
type RuleDraft = { name: string; description: string; service: string; level: string; eventPrefix: string; windowMinutes: string; threshold: string; severity: "warning" | "critical"; reason: string };
type AlertSubscription = { id: string; name: string; rule_id?: string; rule_name?: string; channel: "email" | "console"; target: string; minimum_severity: "warning" | "critical"; notify_on_open: boolean; notify_on_resolved: boolean; enabled: boolean; updated_at: string };
type IncidentNotification = { id: string; transition: "opened" | "escalated" | "resolved"; channel: string; target: string; delivery_id: string; status: "queued" | "processing" | "sent" | "failed"; attempts: number; failure_code?: string; created_at: string; sent_at?: string };
type SubscriptionDraft = { name: string; channel: "email" | "console"; target: string; ruleID: string; minimumSeverity: "warning" | "critical"; notifyOnOpen: boolean; notifyOnResolved: boolean; reason: string };

const emptyRule: RuleDraft = { name: "", description: "", service: "", level: "ERROR", eventPrefix: "", windowMinutes: "5", threshold: "5", severity: "critical", reason: "" };
const emptySubscription: SubscriptionDraft = { name: "", channel: "email", target: "", ruleID: "", minimumSeverity: "warning", notifyOnOpen: true, notifyOnResolved: true, reason: "" };

export function IncidentCenterPanel({ credentials, operator, onOpenLogs }: { credentials: Credentials; operator: Operator; onOpenLogs: (focus: LogFocus) => void }) {
  const [rules, setRules] = useState<AlertRule[]>([]);
  const [incidents, setIncidents] = useState<Incident[]>([]);
  const [subscriptions, setSubscriptions] = useState<AlertSubscription[]>([]);
  const [notifications, setNotifications] = useState<IncidentNotification[]>([]);
  const [status, setStatus] = useState("");
  const [severity, setSeverity] = useState("");
  const [source, setSource] = useState("");
  const [selectedID, setSelectedID] = useState("");
  const [reason, setReason] = useState("");
  const [showRuleForm, setShowRuleForm] = useState(false);
  const [draft, setDraft] = useState<RuleDraft>(emptyRule);
  const [subscriptionDraft, setSubscriptionDraft] = useState<SubscriptionDraft>(emptySubscription);
  const [showSubscriptionForm, setShowSubscriptionForm] = useState(false);
  const [loading, setLoading] = useState(true);
  const [working, setWorking] = useState("");
  const [error, setError] = useState("");
  const [message, setMessage] = useState("");
  const [lastEvaluation, setLastEvaluation] = useState<Evaluation | null>(null);
  const canOperate = operator.role === "support" || operator.role === "admin";
  const canAdmin = operator.role === "admin";

  const load = useCallback(async () => {
    const query = new URLSearchParams({ limit: "100" });
    if (status) query.set("status", status);
    if (severity) query.set("severity", severity);
    if (source) query.set("source", source);
    const [rulePage, incidentPage, subscriptionPage] = await Promise.all([
      incidentFetch<{ rules: AlertRule[] }>("/v1/ops/alert-rules", credentials),
      incidentFetch<{ incidents: Incident[] }>(`/v1/ops/incidents?${query}`, credentials),
      canAdmin ? incidentFetch<{ subscriptions: AlertSubscription[] }>("/v1/ops/alert-subscriptions", credentials) : Promise.resolve({ subscriptions: [] }),
    ]);
    setRules(rulePage.rules);
    setIncidents(incidentPage.incidents);
    setSubscriptions(subscriptionPage.subscriptions);
    setSelectedID((current) => current && incidentPage.incidents.some((item) => item.id === current) ? current : (incidentPage.incidents[0]?.id ?? ""));
  }, [canAdmin, credentials, severity, source, status]);

  useEffect(() => {
    let cancelled = false;
    const query = new URLSearchParams({ limit: "100" });
    if (status) query.set("status", status);
    if (severity) query.set("severity", severity);
    if (source) query.set("source", source);
    void Promise.all([
      incidentFetch<{ rules: AlertRule[] }>("/v1/ops/alert-rules", credentials),
      incidentFetch<{ incidents: Incident[] }>(`/v1/ops/incidents?${query}`, credentials),
      canAdmin ? incidentFetch<{ subscriptions: AlertSubscription[] }>("/v1/ops/alert-subscriptions", credentials) : Promise.resolve({ subscriptions: [] }),
    ]).then(([rulePage, incidentPage, subscriptionPage]) => {
      if (cancelled) return;
      setRules(rulePage.rules);
      setIncidents(incidentPage.incidents);
      setSubscriptions(subscriptionPage.subscriptions);
      setSelectedID(incidentPage.incidents[0]?.id ?? "");
      setLoading(false);
    }).catch((cause: unknown) => { if (!cancelled) { setError(errorMessage(cause)); setLoading(false); } });
    return () => { cancelled = true; };
  }, [canAdmin, credentials, severity, source, status]);

  useEffect(() => {
    if (!selectedID) return;
    let cancelled = false;
    const fetchNotifications = () => incidentFetch<{ notifications: IncidentNotification[] }>(`/v1/ops/incidents/${selectedID}/notifications`, credentials)
      .then((page) => { if (!cancelled) setNotifications(page.notifications); })
      .catch((cause: unknown) => { if (!cancelled) setError(errorMessage(cause)); });
    void fetchNotifications();
    const timer = window.setInterval(() => { void fetchNotifications(); }, 10_000);
    return () => { cancelled = true; window.clearInterval(timer); };
  }, [credentials, selectedID]);

  useEffect(() => {
    const timer = window.setInterval(() => { void load().catch(() => undefined); }, 15_000);
    return () => window.clearInterval(timer);
  }, [load]);

  const selected = useMemo(() => incidents.find((item) => item.id === selectedID) ?? null, [incidents, selectedID]);
  const selectedIsForecast = selected?.event_prefix === "performance.budget.projected_exceeded";
  const stats = useMemo(() => ({
    active: incidents.filter((item) => item.status !== "resolved").length,
    critical: incidents.filter((item) => item.status !== "resolved" && item.severity === "critical").length,
    acknowledged: incidents.filter((item) => item.status === "acknowledged").length,
    rules: rules.filter((rule) => rule.enabled).length,
  }), [incidents, rules]);

  async function refresh() {
    setLoading(true); setError("");
    try { await load(); } catch (cause) { setError(errorMessage(cause)); } finally { setLoading(false); }
  }

  async function evaluate() {
    setWorking("evaluate"); setError(""); setMessage("");
    try {
      const [result, budgetResult] = await Promise.all([
        incidentMutation<{ evaluation: Evaluation }>("/v1/ops/alerts/evaluate", "POST", credentials),
        incidentMutation<{ evaluation: BudgetEvaluation }>("/v1/ops/performance/budgets/evaluate", "POST", credentials),
      ]);
      setLastEvaluation(result.evaluation);
      setMessage(`已评估 ${result.evaluation.rules_evaluated} 条日志规则和 ${budgetResult.evaluation.budgets_evaluated} 条预算，发现 ${budgetResult.evaluation.projected_exceeded} 条预计超额风险，新增 ${result.evaluation.opened + budgetResult.evaluation.opened} 个事故，升级 ${budgetResult.evaluation.escalated} 个，恢复 ${result.evaluation.resolved + budgetResult.evaluation.resolved} 个`);
      await load();
    } catch (cause) { setError(errorMessage(cause)); } finally { setWorking(""); }
  }

  async function createRule(event: FormEvent<HTMLFormElement>) {
    event.preventDefault(); setWorking("create"); setError(""); setMessage("");
    try {
      await incidentMutation("/v1/ops/alert-rules", "POST", credentials, { name: draft.name, description: draft.description, service: draft.service, level: draft.level, event_prefix: draft.eventPrefix, window_minutes: Number(draft.windowMinutes), threshold: Number(draft.threshold), severity: draft.severity, enabled: true, reason: draft.reason });
      setDraft(emptyRule); setShowRuleForm(false); setMessage("告警规则已创建并进入周期评估"); await load();
    } catch (cause) { setError(errorMessage(cause)); } finally { setWorking(""); }
  }

  async function toggleRule(rule: AlertRule) {
    const changeReason = window.prompt(rule.enabled ? "请输入停用原因" : "请输入启用原因", rule.enabled ? "暂时停用规则" : "恢复规则评估");
    if (!changeReason?.trim()) return;
    setWorking(rule.id); setError("");
    try { await incidentMutation(`/v1/ops/alert-rules/${rule.id}`, "PATCH", credentials, { enabled: !rule.enabled, reason: changeReason.trim() }); await load(); } catch (cause) { setError(errorMessage(cause)); } finally { setWorking(""); }
  }

  async function createSubscription(event: FormEvent<HTMLFormElement>) {
    event.preventDefault(); setWorking("create-subscription"); setError(""); setMessage("");
    try {
      await incidentMutation("/v1/ops/alert-subscriptions", "POST", credentials, {
        name: subscriptionDraft.name, rule_id: subscriptionDraft.ruleID, channel: subscriptionDraft.channel, target: subscriptionDraft.target,
        minimum_severity: subscriptionDraft.minimumSeverity, notify_on_open: subscriptionDraft.notifyOnOpen,
        notify_on_resolved: subscriptionDraft.notifyOnResolved, enabled: true, reason: subscriptionDraft.reason,
      });
      setSubscriptionDraft(emptySubscription); setShowSubscriptionForm(false); setMessage(subscriptionDraft.channel === "email" ? "邮件通知订阅已启用" : "控制台通知订阅已启用"); await load();
    } catch (cause) { setError(errorMessage(cause)); } finally { setWorking(""); }
  }

  async function toggleSubscription(item: AlertSubscription) {
    const changeReason = window.prompt(item.enabled ? "请输入停用原因" : "请输入启用原因", item.enabled ? "暂时停用通知" : "恢复通知订阅");
    if (!changeReason?.trim()) return;
    setWorking(item.id); setError("");
    try { await incidentMutation(`/v1/ops/alert-subscriptions/${item.id}`, "PATCH", credentials, { enabled: !item.enabled, reason: changeReason.trim() }); await load(); } catch (cause) { setError(errorMessage(cause)); } finally { setWorking(""); }
  }

  async function downloadEvidence(format: "markdown" | "json") {
    if (!selected) return;
    setWorking(`evidence-${format}`); setError("");
    try {
      const response = await adminFetch(`/v1/ops/incidents/${selected.id}/evidence?format=${format}`, { headers: { ...adminHeaders(credentials) }, cache: "no-store" });
      if (!response.ok) { const payload = await response.json().catch(() => ({})) as { message?: string }; throw new Error(payload.message || `导出失败（${response.status}）`); }
      const blob = await response.blob();
      const href = URL.createObjectURL(blob); const anchor = document.createElement("a");
      anchor.href = href; anchor.download = `incident-${selected.id}-evidence.${format === "markdown" ? "md" : "json"}`; anchor.click(); URL.revokeObjectURL(href);
      setMessage("事故证据报告已生成");
    } catch (cause) { setError(errorMessage(cause)); } finally { setWorking(""); }
  }

  async function retryNotification(item: IncidentNotification) {
    const replayReason = window.prompt("请输入重新投递原因", "事故通知重新投递");
    if (!replayReason?.trim()) return;
    setWorking(item.id); setError("");
    try {
      await incidentMutation(`/v1/ops/email/deliveries/${item.delivery_id}/replay`, "POST", credentials, { reason: replayReason.trim() });
      const page = await incidentFetch<{ notifications: IncidentNotification[] }>(`/v1/ops/incidents/${selectedID}/notifications`, credentials); setNotifications(page.notifications); setMessage("通知已重新进入投递队列");
    } catch (cause) { setError(errorMessage(cause)); } finally { setWorking(""); }
  }

  async function changeIncident(action: "acknowledge" | "resolve") {
    if (!selected || !reason.trim()) { setError("请先填写本次处理说明"); return; }
    setWorking(action); setError(""); setMessage("");
    try {
      await incidentMutation(`/v1/ops/incidents/${selected.id}/${action}`, "POST", credentials, { reason: reason.trim() });
      setReason(""); setMessage(action === "acknowledge" ? "事故已确认，进入处理中" : "事故已标记为已解决"); await load();
    } catch (cause) { setError(errorMessage(cause)); } finally { setWorking(""); }
  }

  return <>
    <header className={styles.topbar}>
      <div><p>OPERATIONS / INCIDENT RESPONSE</p><h1>告警与事故</h1></div>
      <div className={styles.actions}><button type="button" onClick={() => void refresh()} disabled={loading}>{loading ? "同步中" : "立即刷新"}</button></div>
    </header>
    {error && <div className={styles.errorBanner} role="alert"><strong>操作未完成</strong><span>{error}</span></div>}
    {message && <div className={styles.successBanner}><strong>已更新</strong><span>{message}</span></div>}
    <section className={styles.logKpis} aria-label="事故摘要">
      <IncidentMetric label="活跃事故" value={stats.active} tone={stats.active ? "red" : "green"} />
      <IncidentMetric label="严重事故" value={stats.critical} tone="red" />
      <IncidentMetric label="处理中" value={stats.acknowledged} tone="amber" />
      <IncidentMetric label="启用规则" value={stats.rules} tone="violet" />
    </section>
    <section className={styles.incidentLayout}>
      <article className={styles.incidentListPanel}>
        <div className={styles.panelHeading}><div><p>INCIDENT QUEUE</p><h2>事故队列</h2></div><span>{incidents.length} 条</span></div>
        <div className={styles.incidentFilters}>
          <select aria-label="事故状态" value={status} onChange={(event) => setStatus(event.target.value)}><option value="">全部状态</option><option value="open">待处理</option><option value="acknowledged">处理中</option><option value="resolved">已解决</option></select>
          <select aria-label="事故级别" value={severity} onChange={(event) => setSeverity(event.target.value)}><option value="">全部级别</option><option value="critical">严重</option><option value="warning">警告</option></select>
          <select aria-label="事故来源" value={source} onChange={(event) => setSource(event.target.value)}><option value="">全部来源</option><option value="log_rule">日志规则</option><option value="performance_budget">成本预算</option></select>
        </div>
        <IncidentList items={incidents} selectedID={selectedID} onSelect={setSelectedID} />
      </article>
      <article className={styles.incidentWorkspace}>
        {selected ? <>
          <div className={styles.incidentHeadline}><span className={styles.incidentSeverity} data-severity={selected.severity}>{selected.severity === "critical" ? "严重" : "警告"}</span><span className={styles.incidentStatus} data-status={selected.status}>{statusLabel(selected.status)}</span></div>
          <h2>{selected.title}</h2><p>{selected.summary}</p>
          <div className={styles.incidentEvidenceGrid}>
            <div><small>{selectedIsForecast ? "预计使用率 / 阈值" : "当前值 / 阈值"}</small><strong>{selected.observed_value}{selected.source_type === "performance_budget" ? "%" : ""} / {selected.threshold}{selected.source_type === "performance_budget" ? "%" : ""}</strong></div>
            <div><small>{selected.source_type === "performance_budget" ? "预算周期" : "观测窗口"}</small><strong>{selected.source_type === "performance_budget" ? budgetPeriod(selected.evidence.period) : `${selected.window_minutes} 分钟`}</strong></div>
            <div><small>{selected.source_type === "performance_budget" ? "业务范围" : "服务"}</small><strong>{selected.source_type === "performance_budget" ? moduleName(selected.service) : (selected.service || "全部服务")}</strong></div>
            <div><small>{selected.source_type === "performance_budget" ? "预算信号" : "日志信号"}</small><strong>{selectedIsForecast ? "提前预测" : selected.level}{selected.event_prefix ? ` · ${selected.event_prefix}${selected.source_type === "log_rule" ? "*" : ""}` : ""}</strong></div>
          </div>
          <div className={styles.incidentTimeline}><span><i />开单 <b>{formatDateTime(selected.opened_at)}</b></span>{selected.acknowledged_at && <span><i />{selected.acknowledged_by} 确认 <b>{formatDateTime(selected.acknowledged_at)}</b></span>}{selected.resolved_at && <span><i />{selected.resolved_by} 解决 <b>{formatDateTime(selected.resolved_at)}</b></span>}</div>
          <div className={styles.incidentToolRow}>{selected.source_type === "log_rule" && <button className={styles.evidenceLink} type="button" onClick={() => onOpenLogs({ service: selected.service, level: selected.level, query: selected.event_prefix, range: rangeForMinutes(selected.window_minutes) })}>查看关联日志 →</button>}<button className={styles.evidenceLink} type="button" disabled={working !== ""} onClick={() => void downloadEvidence("markdown")}>下载证据报告 ↓</button></div>
          <div className={styles.incidentNotifications}><div><strong>通知记录</strong><span>{notifications.length} 条</span></div>{notifications.length ? <ul>{notifications.map((item) => <li key={item.id}><span><b>{notificationTransition(item.transition)} · {item.channel === "console" ? "控制台" : "邮件"}</b><small>{item.target} · {formatDateTime(item.created_at)}</small></span><em data-status={item.status}>{notificationStatus(item.status)}</em>{canOperate && item.channel === "email" && item.status === "failed" && <button type="button" onClick={() => void retryNotification(item)}>重试</button>}</li>)}</ul> : <p>本次事故没有匹配的通知订阅</p>}</div>
          {canOperate && selected.status !== "resolved" && <div className={styles.incidentActions}><label>处理说明<textarea value={reason} onChange={(event) => setReason(event.target.value)} placeholder="记录判断、处理动作或解决结论" /></label><div>{selected.status === "open" && <button type="button" disabled={working !== ""} onClick={() => void changeIncident("acknowledge")}>确认并处理</button>}<button type="button" disabled={working !== ""} onClick={() => void changeIncident("resolve")}>标记已解决</button></div></div>}
          {selected.resolution && <blockquote>{selected.resolution}</blockquote>}
        </> : <div className={styles.incidentEmpty}><strong>当前没有事故</strong><span>规则仍会每 30 秒自动评估</span></div>}
      </article>
    </section>
    <section className={styles.rulePanel}>
      <div className={styles.panelHeading}><div><p>ALERT POLICIES</p><h2>告警规则</h2></div><div className={styles.ruleHeadingActions}>{lastEvaluation && <small>{lastEvaluation.triggered} 条命中</small>}{canOperate && <button type="button" onClick={() => void evaluate()} disabled={working !== ""}>{working === "evaluate" ? "评估中…" : "立即评估"}</button>}{canAdmin && <button type="button" onClick={() => setShowRuleForm(!showRuleForm)}>{showRuleForm ? "收起" : "新增规则"}</button>}</div></div>
      {showRuleForm && <form className={styles.ruleForm} onSubmit={createRule}>
        <label>规则名称<input required value={draft.name} onChange={(event) => setDraft({ ...draft, name: event.target.value })} placeholder="例如：模型服务错误突增" /></label>
        <label>服务<input value={draft.service} onChange={(event) => setDraft({ ...draft, service: event.target.value })} placeholder="留空表示全部服务" /></label>
        <label>级别<select value={draft.level} onChange={(event) => setDraft({ ...draft, level: event.target.value })}><option>ERROR</option><option>WARN</option><option>INFO</option></select></label>
        <label>事件前缀<input value={draft.eventPrefix} onChange={(event) => setDraft({ ...draft, eventPrefix: event.target.value })} placeholder="可选，如 agent." /></label>
        <label>窗口（分钟）<input required type="number" min="1" max="1440" value={draft.windowMinutes} onChange={(event) => setDraft({ ...draft, windowMinutes: event.target.value })} /></label>
        <label>触发阈值<input required type="number" min="1" value={draft.threshold} onChange={(event) => setDraft({ ...draft, threshold: event.target.value })} /></label>
        <label>严重级别<select value={draft.severity} onChange={(event) => setDraft({ ...draft, severity: event.target.value as RuleDraft["severity"] })}><option value="critical">严重</option><option value="warning">警告</option></select></label>
        <label className={styles.fullField}>说明<textarea value={draft.description} onChange={(event) => setDraft({ ...draft, description: event.target.value })} /></label>
        <label className={styles.fullField}>创建原因<input required value={draft.reason} onChange={(event) => setDraft({ ...draft, reason: event.target.value })} /></label>
        <button type="submit" disabled={working !== ""}>创建并启用</button>
      </form>}
      <div className={styles.ruleGrid}>{rules.map((rule) => <article key={rule.id} data-enabled={rule.enabled}><header><span className={styles.incidentSeverity} data-severity={rule.severity}>{rule.severity === "critical" ? "严重" : "警告"}</span><b>{rule.enabled ? "评估中" : "已停用"}</b></header><h3>{rule.name}</h3><p>{rule.description || "结构化日志阈值规则"}</p><dl><div><dt>信号</dt><dd>{rule.service || "全部服务"} · {rule.level}</dd></div><div><dt>条件</dt><dd>{rule.window_minutes} 分钟 ≥ {rule.threshold} 条</dd></div></dl>{canAdmin && <button type="button" disabled={working !== ""} onClick={() => void toggleRule(rule)}>{rule.enabled ? "停用规则" : "启用规则"}</button>}</article>)}</div>
    </section>
    {canAdmin && <section className={styles.subscriptionPanel}>
      <div className={styles.panelHeading}><div><p>NOTIFICATION ROUTING</p><h2>通知订阅</h2></div><div className={styles.ruleHeadingActions}><small>{subscriptions.filter((item) => item.enabled).length} 条启用</small><button type="button" onClick={() => setShowSubscriptionForm(!showSubscriptionForm)}>{showSubscriptionForm ? "收起" : "新增订阅"}</button></div></div>
      {showSubscriptionForm && <form className={styles.subscriptionForm} onSubmit={createSubscription}>
        <label>订阅名称<input required value={subscriptionDraft.name} onChange={(event) => setSubscriptionDraft({ ...subscriptionDraft, name: event.target.value })} placeholder="例如：平台值班组" /></label>
        <label>通知渠道<select value={subscriptionDraft.channel} onChange={(event) => setSubscriptionDraft({ ...subscriptionDraft, channel: event.target.value as SubscriptionDraft["channel"], target: event.target.value === "console" ? "operations" : "" })}><option value="email">邮件</option><option value="console">管理控制台</option></select></label>
        <label>{subscriptionDraft.channel === "email" ? "接收邮箱" : "控制台收件箱"}<input required type={subscriptionDraft.channel === "email" ? "email" : "text"} value={subscriptionDraft.target} onChange={(event) => setSubscriptionDraft({ ...subscriptionDraft, target: event.target.value })} placeholder={subscriptionDraft.channel === "email" ? "oncall@example.com" : "operations"} /></label>
        <label>关联规则<select value={subscriptionDraft.ruleID} onChange={(event) => setSubscriptionDraft({ ...subscriptionDraft, ruleID: event.target.value })}><option value="">全部规则及预算事故</option>{rules.map((rule) => <option key={rule.id} value={rule.id}>{rule.name}</option>)}</select></label>
        <label>最低级别<select value={subscriptionDraft.minimumSeverity} onChange={(event) => setSubscriptionDraft({ ...subscriptionDraft, minimumSeverity: event.target.value as SubscriptionDraft["minimumSeverity"] })}><option value="warning">警告及严重</option><option value="critical">仅严重</option></select></label>
        <fieldset><legend>通知事件</legend><label><input type="checkbox" checked={subscriptionDraft.notifyOnOpen} onChange={(event) => setSubscriptionDraft({ ...subscriptionDraft, notifyOnOpen: event.target.checked })} />事故发生</label><label><input type="checkbox" checked={subscriptionDraft.notifyOnResolved} onChange={(event) => setSubscriptionDraft({ ...subscriptionDraft, notifyOnResolved: event.target.checked })} />事故恢复</label></fieldset>
        <label className={styles.fullField}>创建原因<input required value={subscriptionDraft.reason} onChange={(event) => setSubscriptionDraft({ ...subscriptionDraft, reason: event.target.value })} /></label>
        <button type="submit" disabled={working !== ""}>创建并启用</button>
      </form>}
      {subscriptions.length ? <div className={styles.subscriptionList}>{subscriptions.map((item) => <article key={item.id} data-enabled={item.enabled}><span><strong>{item.name}</strong><small>{item.channel === "console" ? "控制台" : "邮件"} · {item.target}</small></span><dl><div><dt>范围</dt><dd>{item.rule_name || "全部规则及预算事故"}</dd></div><div><dt>事件</dt><dd>{[item.notify_on_open && "发生/升级", item.notify_on_resolved && "恢复"].filter(Boolean).join("、")}</dd></div><div><dt>级别</dt><dd>{item.minimum_severity === "critical" ? "仅严重" : "警告及严重"}</dd></div></dl><button type="button" disabled={working !== ""} onClick={() => void toggleSubscription(item)}>{item.enabled ? "停用" : "启用"}</button></article>)}</div> : <div className={styles.subscriptionEmpty}>尚未配置通知订阅；告警仍会正常创建事故。</div>}
    </section>}
  </>;
}

function IncidentMetric({ label, value, tone }: { label: string; value: number; tone: string }) { return <article className={`${styles.logMetric} ${styles[tone]}`}><span>{label}<i /></span><strong>{value}</strong></article>; }
function IncidentList({ items, selectedID, onSelect }: { items: Incident[]; selectedID: string; onSelect: (id: string) => void }) {
  if (!items.length) return <div className={styles.incidentEmpty}><strong>没有匹配的事故</strong><span>调整筛选条件后重试</span></div>;
  return <ul className={styles.incidentList}>{items.map((item) => <li key={item.id}><button type="button" data-selected={item.id === selectedID} onClick={() => onSelect(item.id)}><span><i data-severity={item.severity} /><strong>{item.title}</strong><small>{item.event_prefix === "performance.budget.projected_exceeded" ? "预算预测" : item.source_type === "performance_budget" ? "成本预算" : (item.service || "全部服务")} · {formatDateTime(item.opened_at)}</small></span><em>{statusLabel(item.status)}</em></button></li>)}</ul>;
}
async function incidentFetch<T>(path: string, credentials: Credentials): Promise<T> { const response = await adminFetch(`${path}`, { headers: { ...adminHeaders(credentials) }, cache: "no-store" }); const payload = await response.json().catch(() => ({})) as { message?: string }; if (!response.ok) throw new Error(payload.message || `请求失败（${response.status}）`); return payload as T; }
async function incidentMutation<T = unknown>(path: string, method: string, credentials: Credentials, body?: unknown): Promise<T> { const response = await adminFetch(`${path}`, { method, headers: { ...adminHeaders(credentials), "Content-Type": "application/json" }, body: body === undefined ? undefined : JSON.stringify(body) }); const payload = await response.json().catch(() => ({})) as { message?: string }; if (!response.ok) throw new Error(payload.message || `请求失败（${response.status}）`); return payload as T; }
function errorMessage(cause: unknown) { return cause instanceof Error ? cause.message : "告警服务暂时不可用"; }
function statusLabel(status: string) { return ({ open: "待处理", acknowledged: "处理中", resolved: "已解决" } as Record<string, string>)[status] ?? status; }
function notificationStatus(status: string) { return ({ queued: "排队中", processing: "投递中", sent: "已发送", failed: "失败" } as Record<string, string>)[status] ?? status; }
function notificationTransition(value: IncidentNotification["transition"]) { return value === "opened" ? "事故发生" : value === "escalated" ? "升级为严重" : "事故恢复"; }
function budgetPeriod(value: unknown) { return value === "monthly" ? "每月" : "每日"; }
function moduleName(value?: string) { return value === "companion" ? "陪伴" : value === "life" ? "生活" : value === "work" ? "工作" : "全部模块"; }
function formatDateTime(value: string) { return new Intl.DateTimeFormat("zh-CN", { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", hour12: false }).format(new Date(value)); }
function rangeForMinutes(minutes: number) { if (minutes <= 15) return "15m"; if (minutes <= 60) return "1h"; if (minutes <= 360) return "6h"; if (minutes <= 1440) return "24h"; return "7d"; }
