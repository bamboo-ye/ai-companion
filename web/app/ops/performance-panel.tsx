"use client";

import { FormEvent, useCallback, useEffect, useMemo, useRef, useState } from "react";

import { adminFetch, adminHeaders, type AdminCredentials } from "./admin-auth";
import styles from "./operations.module.css";



type Credentials = AdminCredentials;
type Operator = { actor: string; role: string };
type VersionMetric = {
  dimension: string; group_id: string; key: string; label: string; version_id?: string; version?: number; revision?: number; config_version?: string;
  runs: number; sample_size: number; completed: number; failed: number; cancelled: number; quality_failures: number;
  success_rate: number; error_rate: number; quality_failure_rate: number; model_calls: number; prompt_tokens: number; completion_tokens: number;
  cost_micros: number; average_cost_micros: number; average_duration_ms: number; p50_duration_ms: number; p95_duration_ms: number;
};
type ModelMetric = {
  provider: string; model: string; calls: number; succeeded: number; failed: number; error_rate: number;
  prompt_tokens: number; completion_tokens: number; cached_tokens: number; cost_micros: number; average_cost_micros: number;
  average_latency_ms: number; p95_latency_ms: number;
};
type WindowMetric = {
  from: string; to: string; runs: number; sample_size: number; completed: number; failed: number; quality_failures: number;
  success_rate: number; error_rate: number; quality_failure_rate: number; model_calls: number; cost_micros: number;
  average_cost_micros: number; p95_duration_ms: number;
};
type Anomaly = { metric: string; severity: "warning" | "critical"; current: number; baseline: number; change: number; message: string };
type Analysis = { status: "stable" | "warning" | "critical" | "insufficient_data"; generated_at: string; current: WindowMetric; baseline: WindowMetric; anomalies: Anomaly[]; note?: string };
type TrendPoint = { from: string; to: string; runs: number; sample_size: number; completed: number; failed: number; success_rate: number; model_calls: number; cost_micros: number; p95_duration_ms: number };
type BudgetStatus = {
  id: string; name: string; module?: string; period: "daily" | "monthly"; cost_limit_micros: number; warning_ratio: number;
  enabled: boolean; forecast_alerts_enabled: boolean; forecast_lookback_days: number; forecast_min_samples: number;
  effect_observation_days: number; effect_min_samples: number;
  revision: number; updated_by: string; updated_at: string; window_from: string; window_to: string;
  used_cost_micros: number; remaining_micros: number; utilization: number; status: "normal" | "warning" | "exceeded" | "disabled";
};
type BudgetForecast = {
  budget_id: string; name: string; module?: string; period: "daily" | "monthly"; current_status: BudgetStatus["status"];
  risk: "on_track" | "projected_warning" | "projected_exceeded" | "exceeded" | "disabled" | "insufficient_data";
  confidence: "none" | "low" | "medium" | "high"; sample_size: number; observed_hours: number; used_cost_micros: number;
  cost_limit_micros: number; burn_rate_micros_per_hour: number; projected_cost_micros: number; projected_utilization: number;
  required_savings_micros: number; period_ends_at: string; projected_warning_at?: string; projected_limit_exceeded_at?: string;
};
type ModelRecommendation = {
  source_provider: string; source_model: string; target_provider: string; target_model: string; source_calls: number; target_calls: number;
  estimated_savings_micros_per_1000_calls: number; estimated_savings_ratio: number; error_rate_delta: number; p95_latency_delta_ms: number;
  confidence: "low" | "medium" | "high"; reason: string; requires_validation_before_adoption: boolean;
};
type Forecast = {
  status: "ready" | "insufficient_data"; generated_at: string; history_from: string; history_to: string;
  budget_forecasts: BudgetForecast[]; model_recommendations: ModelRecommendation[]; note: string;
};
type BudgetImpactProjection = {
  enabled: boolean; forecast_alerts_enabled: boolean; forecast_lookback_days: number; forecast_min_samples: number;
  status: BudgetStatus["status"]; signal: "disabled" | "safe" | "insufficient_samples" | "projected_exceeded" | "actual_warning" | "actual_exceeded";
  severity: "none" | "warning" | "critical"; will_alert: boolean; used_cost_micros: number; cost_limit_micros: number;
  utilization: number; forecast_risk: BudgetForecast["risk"] | "not_evaluated"; forecast_confidence: "none" | "low" | "medium" | "high";
  sample_size: number; projected_cost_micros: number; projected_utilization: number; projected_limit_exceeded_at?: string; reason: string;
};
type BudgetImpactPreview = {
  budget_id: string; generated_at: string; current: BudgetImpactProjection; proposed: BudgetImpactProjection;
  change: "unchanged" | "would_start_alerting" | "would_stop_alerting" | "would_escalate" | "would_deescalate" | "would_change_signal";
  note: string;
};
type BudgetForecastOutcome = {
  incident_id: string; budget_id: string; budget_name: string; module?: string; period: "daily" | "monthly";
  outcome: "hit" | "cleared" | "observing"; opened_at: string; outcome_at?: string; duration_minutes: number;
  initial_utilization: number; projected_utilization: number; projected_cost_micros: number; forecast_sample_size: number;
  predicted_limit_exceeded_at?: string;
};
type BudgetForecastHistorySummary = {
  budget_id: string; budget_name: string; module?: string; period: "daily" | "monthly"; predictions: number; hits: number;
  cleared: number; observing: number; decided: number; hit_rate: number; average_lead_time_minutes: number;
};
type BudgetPolicyDecision = {
  id: string; budget_id: string; recommendation_key: string; action: "increase_sample_gate" | "decrease_sample_gate";
  decision: "accepted" | "rejected"; confidence: "low" | "medium" | "high"; outcome_count: number; hit_rate: number;
  average_lead_time_minutes: number; current_lookback_days: number; current_min_samples: number; proposed_lookback_days: number;
  proposed_min_samples: number; recommendation_reason: string; reason: string; decided_by: string; decided_at: string; budget_revision: number;
  applied_at?: string; applied_by?: string; applied_budget_revision?: number; effect_observation_days?: number; effect_min_samples?: number;
};
type BudgetPolicyRecommendation = {
  budget_id: string; budget_name: string; module?: string; period: "daily" | "monthly";
  action: "not_applicable" | "collect_more_data" | "keep_policy" | "increase_sample_gate" | "decrease_sample_gate";
  confidence: "none" | "low" | "medium" | "high"; decided: number; hit_rate: number; average_lead_time_minutes: number;
  current_lookback_days: number; current_min_samples: number; proposed_lookback_days: number; proposed_min_samples: number;
  reason: string; requires_impact_preview: boolean; recommendation_key: string; feedback?: BudgetPolicyDecision;
};
type BudgetPolicyEffectMetrics = { predictions: number; hits: number; cleared: number; observing: number; decided: number; hit_rate: number; average_lead_time_minutes: number };
type BudgetPolicyRollbackVerification = {
  budget_revision: number; applied_by: string; applied_at: string; observation_days: number; minimum_decided_samples: number;
  observation_ends_at: string; observation_complete: boolean; after: BudgetPolicyEffectMetrics;
  status: "collecting" | "insufficient_data" | "recovered" | "improving" | "mixed" | "not_recovered"; note: string;
};
type BudgetPolicyEffectReview = {
  status: "acknowledged" | "closed"; disposition: "rollback_planned" | "continue_observing"; reason: string;
  reviewed_by: string; reviewed_at: string; rollback_applied_at?: string; rollback_applied_by?: string; rollback_budget_revision?: number;
  closed_reason?: string; closed_by?: string; closed_at?: string;
};
type BudgetPolicyEffect = {
  decision_id: string; budget_id: string; budget_name: string; module?: string; period: "daily" | "monthly"; recommendation_key: string;
  action: "increase_sample_gate" | "decrease_sample_gate"; current_lookback_days: number; current_min_samples: number; proposed_lookback_days: number; proposed_min_samples: number; applied_by: string;
  applied_at: string; applied_budget_revision: number; before: BudgetPolicyEffectMetrics; after: BudgetPolicyEffectMetrics;
  observation_days: number; minimum_decided_samples: number; observation_ends_at: string; observation_complete: boolean;
  status: "collecting" | "insufficient_data" | "improved" | "mixed" | "regressed" | "stable"; note: string;
  recommend_rollback: boolean; rollback_lookback_days?: number; rollback_min_samples?: number; rollback_reason?: string; rollback_can_apply: boolean;
  rollback_verification?: BudgetPolicyRollbackVerification;
  review?: BudgetPolicyEffectReview;
};
type BudgetForecastHistory = {
  generated_at: string; from: string; to: string; predictions: number; hits: number; cleared: number; observing: number;
  decided: number; hit_rate: number; average_lead_time_minutes: number; budgets: BudgetForecastHistorySummary[]; recommendations: BudgetPolicyRecommendation[]; effects: BudgetPolicyEffect[];
  items: BudgetForecastOutcome[]; note: string;
};
type BudgetForm = { name: string; module: string; period: string; limit: string; warning: string; enabled: boolean; forecastEnabled: boolean; lookback: string; minSamples: string; effectObservationDays: string; effectMinSamples: string; reason: string };

const emptyBudgetForm: BudgetForm = { name: "", module: "", period: "daily", limit: "10", warning: "80", enabled: true, forecastEnabled: true, lookback: "7", minSamples: "20", effectObservationDays: "30", effectMinSamples: "5", reason: "" };

function budgetFormFromStatus(item: BudgetStatus): BudgetForm {
  return { name: item.name, module: item.module || "", period: item.period, limit: (item.cost_limit_micros / 1_000_000).toString(), warning: (item.warning_ratio * 100).toString(), enabled: item.enabled, forecastEnabled: item.forecast_alerts_enabled, lookback: item.forecast_lookback_days.toString(), minSamples: item.forecast_min_samples.toString(), effectObservationDays: item.effect_observation_days.toString(), effectMinSamples: item.effect_min_samples.toString(), reason: "" };
}

function budgetRequestBody(form: BudgetForm, editing?: BudgetStatus | null) {
  return { name: form.name, module: form.module, period: form.period, cost_limit_micros: Math.round(Number(form.limit) * 1_000_000), warning_ratio: Number(form.warning) / 100, enabled: form.enabled, forecast_alerts_enabled: form.forecastEnabled, forecast_lookback_days: Number(form.lookback), forecast_min_samples: Number(form.minSamples), effect_observation_days: Number(form.effectObservationDays), effect_min_samples: Number(form.effectMinSamples), revision: editing?.revision || 0, reason: form.reason };
}

export function PerformancePanel({ credentials, operator }: { credentials: Credentials; operator: Operator }) {
  const [range, setRange] = useState("7d");
  const [module, setModule] = useState("");
  const [dimension, setDimension] = useState<"agent_version" | "model_profile">("agent_version");
  const [versions, setVersions] = useState<VersionMetric[]>([]);
  const [models, setModels] = useState<ModelMetric[]>([]);
  const [analysis, setAnalysis] = useState<Analysis | null>(null);
  const [trend, setTrend] = useState<TrendPoint[]>([]);
  const [budgets, setBudgets] = useState<BudgetStatus[]>([]);
  const [forecast, setForecast] = useState<Forecast | null>(null);
  const [baselineID, setBaselineID] = useState("");
  const [candidateID, setCandidateID] = useState("");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");

  const load = useCallback(async () => {
	await Promise.resolve();
    setLoading(true);
    setError("");
    const query = new URLSearchParams({ range, limit: "100" });
    if (module) query.set("module", module);
    try {
      const [versionPage, modelPage, anomalyPage, trendPage, budgetPage, forecastPage] = await Promise.all([
        performanceFetch<{ items: VersionMetric[] }>(`/v1/ops/performance/versions?${query}&dimension=${dimension}`, credentials),
        performanceFetch<{ items: ModelMetric[] }>(`/v1/ops/performance/models?${query}`, credentials),
        performanceFetch<Analysis>(`/v1/ops/performance/anomalies?window=1h&baseline=24h${module ? `&module=${module}` : ""}`, credentials),
        performanceFetch<{ items: TrendPoint[] }>(`/v1/ops/performance/trend?${query}`, credentials),
        performanceFetch<{ budgets: BudgetStatus[] }>("/v1/ops/performance/budgets", credentials),
        performanceFetch<Forecast>(`/v1/ops/performance/forecast?${query}`, credentials),
      ]);
      setVersions(versionPage.items);
      setModels(modelPage.items);
      setAnalysis(anomalyPage);
      setTrend(trendPage.items);
      setBudgets(budgetPage.budgets);
      setForecast(forecastPage);
      setBaselineID((current) => versionPage.items.some((item) => item.group_id === current) ? current : (versionPage.items[1]?.group_id ?? versionPage.items[0]?.group_id ?? ""));
      setCandidateID((current) => versionPage.items.some((item) => item.group_id === current) ? current : (versionPage.items[0]?.group_id ?? ""));
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "成本与质量数据暂时不可用");
    } finally {
      setLoading(false);
    }
  }, [credentials, dimension, module, range]);

  useEffect(() => {
    const timer = window.setTimeout(() => void load(), 0);
    return () => window.clearTimeout(timer);
  }, [load]);

  const totals = useMemo(() => versions.reduce((result, item) => ({
    runs: result.runs + item.runs,
    sample: result.sample + item.sample_size,
    completed: result.completed + item.completed,
    failures: result.failures + item.failed,
    quality: result.quality + item.quality_failures,
    calls: result.calls + item.model_calls,
    cost: result.cost + item.cost_micros,
  }), { runs: 0, sample: 0, completed: 0, failures: 0, quality: 0, calls: 0, cost: 0 }), [versions]);
  const baseline = versions.find((item) => item.group_id === baselineID);
  const candidate = versions.find((item) => item.group_id === candidateID);

  return <>
    <header className={styles.topbar}>
      <div><p>OPERATIONS / COST & QUALITY</p><h1>成本与质量</h1></div>
      <div className={styles.actions}><span className={styles.permissionTag}>只读分析 · 持久化数据</span><button type="button" onClick={() => void load()} disabled={loading}>{loading ? "计算中" : "刷新分析"}</button></div>
    </header>

    {error && <div className={styles.errorBanner} role="alert"><strong>分析未完成</strong><span>{error}</span></div>}

    <section className={styles.performanceFilters} aria-label="分析范围">
      <label>分析周期<select value={range} onChange={(event) => setRange(event.target.value)}><option value="24h">最近 24 小时</option><option value="7d">最近 7 天</option><option value="30d">最近 30 天</option><option value="90d">最近 90 天</option></select></label>
      <label>业务模块<select value={module} onChange={(event) => setModule(event.target.value)}><option value="">全部模块</option><option value="companion">陪伴</option><option value="life">生活</option><option value="work">工作</option></select></label>
      <label>分析维度<select value={dimension} onChange={(event) => setDimension(event.target.value as "agent_version" | "model_profile")}><option value="agent_version">Agent 版本</option><option value="model_profile">模型配置版本</option></select></label>
      <div><span>数据口径</span><strong>已结束运行 + 实际模型调用</strong></div>
    </section>

    <AnalysisSignal analysis={analysis} loading={loading} />

    <section className={styles.kpiGrid} aria-label="成本质量指标">
      <PerformanceKPI label="完成成功率" value={formatRate(rate(totals.completed, totals.sample))} note={`${totals.completed} / ${totals.sample} 个已结束运行`} tone="green" />
      <PerformanceKPI label="系统失败率" value={formatRate(rate(totals.failures, totals.sample))} note={`${totals.failures} 个失败或超时`} tone={totals.failures ? "red" : "blue"} />
      <PerformanceKPI label="质量失败率" value={formatRate(rate(totals.quality, totals.sample))} note="回复 / 产物 / 邮件质量判定" tone={totals.quality ? "amber" : "violet"} />
      <PerformanceKPI label="模型总成本" value={formatCost(totals.cost)} note={`${totals.calls} 次实际调用`} tone="amber" />
    </section>

    <TrendChart items={trend} range={range} loading={loading} />

    <ForecastPanel forecast={forecast} loading={loading} />

    <BudgetManager credentials={credentials} operator={operator} items={budgets} onSaved={load} />

    <section className={styles.comparisonPanel}>
      <div className={styles.panelHeading}><div><p>VERSION COMPARISON</p><h2>版本效果对比</h2></div><span>{dimension === "agent_version" ? "Agent 定义" : "模型配置"}</span></div>
      {versions.length ? <>
        <div className={styles.comparisonSelectors}>
          <label>基线版本<select value={baselineID} onChange={(event) => setBaselineID(event.target.value)}>{versions.map((item) => <option key={item.group_id} value={item.group_id}>{item.label}</option>)}</select></label>
          <span>对比</span>
          <label>候选版本<select value={candidateID} onChange={(event) => setCandidateID(event.target.value)}>{versions.map((item) => <option key={item.group_id} value={item.group_id}>{item.label}</option>)}</select></label>
        </div>
        {baseline && candidate && <div className={styles.deltaGrid}>
          <DeltaCard label="成功率" value={candidate.success_rate} baseline={baseline.success_rate} kind="rate" positive="up" />
          <DeltaCard label="质量失败率" value={candidate.quality_failure_rate} baseline={baseline.quality_failure_rate} kind="rate" positive="down" />
          <DeltaCard label="P95 耗时" value={candidate.p95_duration_ms} baseline={baseline.p95_duration_ms} kind="duration" positive="down" />
          <DeltaCard label="单次运行成本" value={candidate.average_cost_micros} baseline={baseline.average_cost_micros} kind="cost" positive="down" />
        </div>}
      </> : <EmptyAnalysis text={loading ? "正在聚合版本指标…" : "当前范围内没有可比较的 Agent Run"} />}
    </section>

    <section className={styles.performanceTablePanel}>
      <div className={styles.panelHeading}><div><p>VERSION BREAKDOWN</p><h2>版本明细</h2></div><span>{versions.length} 个版本</span></div>
      <VersionTable items={versions} loading={loading} />
    </section>

    <section className={styles.performanceTablePanel}>
      <div className={styles.panelHeading}><div><p>ACTUAL MODEL USAGE</p><h2>实际模型调用</h2></div><span>{models.length} 个模型</span></div>
      <ModelTable items={models} loading={loading} />
    </section>
  </>;
}

function ForecastPanel({ forecast, loading }: { forecast: Forecast | null; loading: boolean }) {
  const budgets = forecast?.budget_forecasts ?? [];
  const recommendations = forecast?.model_recommendations ?? [];
  return <section className={styles.forecastPanel}>
    <div className={styles.panelHeading}><div><p>CAPACITY FORECAST</p><h2>容量与成本预测</h2></div><span>{forecast ? `历史窗口 ${formatHours((new Date(forecast.history_to).getTime() - new Date(forecast.history_from).getTime()) / 3_600_000)}` : "只读预测"}</span></div>
    <div className={styles.forecastColumns}>
      <div className={styles.forecastGroup}>
        <header><strong>预算消耗预测</strong><small>按近期实际速度推算至周期结束</small></header>
        {budgets.length ? <div className={styles.forecastBudgetList}>{budgets.map((item) => <article key={item.budget_id} data-risk={item.risk}>
          <div><span>{item.name}</span><b>{forecastRiskLabel(item.risk)}</b></div>
          <strong>{formatCost(item.projected_cost_micros)} <small>/ {formatCost(item.cost_limit_micros)}</small></strong>
          <i><b style={{ width: `${Math.min(100, item.projected_utilization * 100)}%` }} /></i>
          <dl><div><dt>预计使用</dt><dd>{formatRate(item.projected_utilization)}</dd></div><div><dt>消耗速度</dt><dd>{formatCost(item.burn_rate_micros_per_hour)} / 小时</dd></div><div><dt>预计超额</dt><dd>{forecastTimeLabel(item.projected_limit_exceeded_at, item.risk)}</dd></div><div><dt>可信度</dt><dd>{confidenceLabel(item.confidence)} · {item.sample_size} 个样本</dd></div></dl>
          {item.required_savings_micros > 0 && <p>本周期需要至少节省 {formatCost(item.required_savings_micros)}</p>}
        </article>)}</div> : <EmptyAnalysis text={loading ? "正在计算预算消耗速度…" : "尚未设置匹配当前模块的成本预算。"} />}
      </div>
      <div className={styles.forecastGroup}>
        <header><strong>模型切换建议</strong><small>成本更低，同时守住错误率与延迟</small></header>
        {recommendations.length ? <div className={styles.recommendationList}>{recommendations.map((item) => <article key={`${item.source_provider}:${item.source_model}->${item.target_provider}:${item.target_model}`}>
          <div className={styles.modelSwitch}><span><small>当前</small><strong>{item.source_model}</strong><i>{item.source_provider}</i></span><b>→</b><span><small>建议评测</small><strong>{item.target_model}</strong><i>{item.target_provider}</i></span></div>
          <div className={styles.savingsRow}><strong>预计节省 {formatRate(item.estimated_savings_ratio)}</strong><span>每千次约 {formatCost(item.estimated_savings_micros_per_1000_calls)}</span></div>
          <p>{item.reason}</p><footer><span>{confidenceLabel(item.confidence)}可信度 · {item.source_calls}/{item.target_calls} 次调用</span><b>需先灰度验证</b></footer>
        </article>)}</div> : <EmptyAnalysis text={loading ? "正在筛选模型候选…" : "暂无同时满足成本、错误率和延迟保护线的替代模型。"} />}
      </div>
    </div>
    {forecast?.note && <footer className={styles.forecastNote}>{forecast.note}</footer>}
  </section>;
}

function TrendChart({ items, range, loading }: { items: TrendPoint[]; range: string; loading: boolean }) {
  const maxCost = Math.max(1, ...items.map((item) => item.cost_micros));
  const total = items.reduce((sum, item) => sum + item.cost_micros, 0);
  return <section className={styles.trendPanel}>
    <div className={styles.panelHeading}><div><p>COST TREND</p><h2>成本与成功率趋势</h2></div><span>{range === "24h" || range === "7d" ? "小时粒度" : "日粒度"} · {formatCost(total)}</span></div>
    {items.length ? <div className={styles.trendChart} role="img" aria-label="成本趋势柱状图">
      {items.map((item) => <article key={item.from} title={`${formatTrendTime(item.from, range)} · ${formatCost(item.cost_micros)} · 成功率 ${formatRate(item.success_rate)}`}>
        <div><span style={{ height: `${Math.max(item.cost_micros ? 4 : 1, item.cost_micros / maxCost * 100)}%` }} /></div>
        <i data-empty={!item.sample_size} style={{ bottom: `${Math.min(100, Math.max(0, item.success_rate * 100))}%` }} />
        <small>{formatTrendTime(item.from, range)}</small>
      </article>)}
    </div> : <EmptyAnalysis text={loading ? "正在生成成本趋势…" : "当前范围内没有趋势数据"} />}
    <div className={styles.trendLegend}><span><i />实际成本</span><span><i />成功率位置</span></div>
  </section>;
}

function BudgetManager({ credentials, operator, items, onSaved }: { credentials: Credentials; operator: Operator; items: BudgetStatus[]; onSaved: () => Promise<void> }) {
  const canEdit = operator.role === "admin";
  const [editing, setEditing] = useState<BudgetStatus | null | undefined>(undefined);
  const [form, setForm] = useState<BudgetForm>(emptyBudgetForm);
  const [saving, setSaving] = useState(false);
  const [previewing, setPreviewing] = useState(false);
  const [preview, setPreview] = useState<BudgetImpactPreview | null>(null);
  const [recommendationSource, setRecommendationSource] = useState<BudgetPolicyRecommendation | null>(null);
  const [rollbackSource, setRollbackSource] = useState<BudgetPolicyEffect | null>(null);
  const [message, setMessage] = useState("");
  const formRef = useRef<HTMLFormElement | null>(null);
  const previewRequestRef = useRef(0);

  const openForm = (item: BudgetStatus | null) => {
    previewRequestRef.current += 1;
    setEditing(item);
    setMessage("");
    setPreview(null);
    setPreviewing(false);
    setRecommendationSource(null);
    setRollbackSource(null);
    setForm(item ? budgetFormFromStatus(item) : emptyBudgetForm);
  };
  const changeForm = (patch: Partial<BudgetForm>) => {
    previewRequestRef.current += 1;
    setForm((current) => ({ ...current, ...patch }));
    setPreview(null);
    setPreviewing(false);
  };
  const runImpactPreview = async (budget: BudgetStatus, candidate: BudgetForm) => {
    const requestID = previewRequestRef.current + 1;
    previewRequestRef.current = requestID;
    setPreviewing(true);
    setMessage("");
    setPreview(null);
    try {
      const result = await performanceFetch<{ preview: BudgetImpactPreview }>(`/v1/ops/performance/budgets/${budget.id}/preview`, credentials, { method: "POST", body: JSON.stringify(budgetRequestBody(candidate, budget)) });
      if (previewRequestRef.current === requestID) setPreview(result.preview);
    } catch (cause) {
      if (previewRequestRef.current === requestID) setMessage(cause instanceof Error ? cause.message : "影响预览失败");
    } finally {
      if (previewRequestRef.current === requestID) setPreviewing(false);
    }
  };
  const previewImpact = async () => {
    if (!editing) return;
    await runImpactPreview(editing, form);
  };
  const previewRecommendation = async (recommendation: BudgetPolicyRecommendation) => {
    if (!canEdit) throw new Error("只有管理员可以带入策略建议");
    const budget = items.find((item) => item.id === recommendation.budget_id);
    if (!budget) throw new Error("预算配置已变化，请刷新数据后重试");
    const candidate = { ...budgetFormFromStatus(budget), lookback: recommendation.proposed_lookback_days.toString(), minSamples: recommendation.proposed_min_samples.toString() };
    setEditing(budget);
    setForm(candidate);
    setRecommendationSource(recommendation);
    setRollbackSource(null);
    window.setTimeout(() => formRef.current?.scrollIntoView({ behavior: "smooth", block: "start" }), 0);
    await runImpactPreview(budget, candidate);
  };
  const previewRollback = async (effect: BudgetPolicyEffect) => {
    if (!canEdit) throw new Error("只有管理员可以带入回滚建议");
    const budget = items.find((item) => item.id === effect.budget_id);
    if (!budget || !effect.recommend_rollback || !effect.rollback_lookback_days || !effect.rollback_min_samples) throw new Error("回滚建议已失效，请刷新数据后重试");
    const candidate = { ...budgetFormFromStatus(budget), lookback: effect.rollback_lookback_days.toString(), minSamples: effect.rollback_min_samples.toString() };
    setEditing(budget);
    setForm(candidate);
    setRecommendationSource(null);
    setRollbackSource(effect);
    window.setTimeout(() => formRef.current?.scrollIntoView({ behavior: "smooth", block: "start" }), 0);
    await runImpactPreview(budget, candidate);
  };
  const closeForm = () => {
    previewRequestRef.current += 1;
    setEditing(undefined);
    setPreviewing(false);
    setPreview(null);
    setRecommendationSource(null);
    setRollbackSource(null);
    setMessage("");
  };
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setSaving(true);
    setMessage("");
    try {
      await performanceFetch(editing ? `/v1/ops/performance/budgets/${editing.id}` : "/v1/ops/performance/budgets", credentials, { method: editing ? "PATCH" : "POST", body: JSON.stringify(budgetRequestBody(form, editing)) });
      await onSaved();
      closeForm();
    } catch (cause) {
      setMessage(cause instanceof Error ? cause.message : "预算保存失败");
    } finally {
      setSaving(false);
    }
  };

  return <section className={styles.budgetPanel}>
    <div className={styles.panelHeading}><div><p>BUDGET GUARDRAILS</p><h2>成本预算</h2></div>{canEdit ? <button type="button" onClick={() => openForm(null)}>新增预算</button> : <span>只读权限</span>}</div>
    {items.length ? <div className={styles.budgetGrid}>{items.map((item) => <article key={item.id} data-status={item.status}>
      <header><div><strong>{item.name}</strong><small>{moduleLabel(item.module)} · {item.period === "daily" ? "每日" : "每月"}</small></div><span>{budgetStatusLabel(item.status)}</span></header>
      <div className={styles.budgetNumbers}><strong>{formatCost(item.used_cost_micros)}</strong><span>/ {formatCost(item.cost_limit_micros)}</span></div>
      <div className={styles.budgetProgress}><i style={{ width: `${Math.min(100, item.utilization * 100)}%` }} /></div>
      <div className={styles.budgetForecastPolicy}><span>提前预警</span><strong>{item.forecast_alerts_enabled ? `${item.forecast_lookback_days} 天历史 · 至少 ${item.forecast_min_samples} 个样本` : "已关闭"}</strong></div>
      <div className={styles.budgetForecastPolicy}><span>效果观察</span><strong>{item.effect_observation_days} 天 · 至少 {item.effect_min_samples} 条结论</strong></div>
      <footer><span>已使用 {formatRate(item.utilization)}</span><span>{item.status === "exceeded" ? `超出 ${formatCost(item.used_cost_micros - item.cost_limit_micros)}` : `剩余 ${formatCost(item.remaining_micros)}`}</span>{canEdit && <button type="button" onClick={() => openForm(item)}>编辑</button>}</footer>
    </article>)}</div> : <EmptyAnalysis text="尚未设置成本预算；管理员可以按模块配置每日或每月红线。" />}
    {editing !== undefined && <form ref={formRef} className={styles.budgetForm} onSubmit={submit}>
      <div><strong>{editing ? "编辑预算" : "新增预算"}</strong><button type="button" onClick={closeForm}>关闭</button></div>
      {recommendationSource && <aside className={styles.budgetRecommendationNotice}><strong>已带入历史调优建议</strong><span>{recommendationSource.current_min_samples} → {recommendationSource.proposed_min_samples} 个最小样本；当前仅做只读试算，请补充变更原因后手动保存。</span></aside>}
      {rollbackSource && <aside className={styles.budgetRollbackNotice}><strong>已带入只读回滚建议</strong><span>恢复为 {rollbackSource.rollback_lookback_days} 天历史、至少 {rollbackSource.rollback_min_samples} 个预测样本；系统不会自动保存，请确认试算并补充原因。</span></aside>}
      <label>名称<input value={form.name} required minLength={2} maxLength={128} onChange={(event) => changeForm({ name: event.target.value })} placeholder="例如：工作模块每日预算" /></label>
      <label>业务模块<select value={form.module} onChange={(event) => changeForm({ module: event.target.value })}><option value="">全部模块</option><option value="companion">陪伴</option><option value="life">生活</option><option value="work">工作</option></select></label>
      <label>预算周期<select value={form.period} onChange={(event) => { const period = event.target.value; changeForm({ period, lookback: period === "monthly" ? "30" : "7" }); }}><option value="daily">每日</option><option value="monthly">每月</option></select></label>
      <label>成本上限（美元）<input type="number" min="0.000001" step="0.01" required value={form.limit} onChange={(event) => changeForm({ limit: event.target.value })} /></label>
      <label>预警线（%）<input type="number" min="1" max="99" required value={form.warning} onChange={(event) => changeForm({ warning: event.target.value })} /></label>
      <label className={styles.budgetEnabled}><input type="checkbox" checked={form.enabled} onChange={(event) => changeForm({ enabled: event.target.checked })} />启用预算</label>
      <label className={styles.budgetEnabled}><input type="checkbox" checked={form.forecastEnabled} onChange={(event) => changeForm({ forecastEnabled: event.target.checked })} />启用提前预警</label>
      <label>预测历史窗口（天）<input type="number" min="1" max="90" required disabled={!form.forecastEnabled} value={form.lookback} onChange={(event) => changeForm({ lookback: event.target.value })} /></label>
      <label>最小样本量<input type="number" min="5" max="10000" required disabled={!form.forecastEnabled} value={form.minSamples} onChange={(event) => changeForm({ minSamples: event.target.value })} /></label>
      <label>效果观察周期（天）<input type="number" min="7" max="180" required value={form.effectObservationDays} onChange={(event) => changeForm({ effectObservationDays: event.target.value })} /></label>
      <label>效果最低结论数<input type="number" min="5" max="100" required value={form.effectMinSamples} onChange={(event) => changeForm({ effectMinSamples: event.target.value })} /></label>
      <label className={styles.budgetReason}>变更原因<textarea value={form.reason} required minLength={2} maxLength={512} onChange={(event) => setForm((current) => ({ ...current, reason: event.target.value }))} placeholder="记录本次预算调整原因" /></label>
      {preview && <BudgetImpactPreviewView preview={preview} />}
      {message && <p role="alert">{message}</p>}
      <div className={styles.budgetFormActions} data-single={!editing}>
        {editing && <button type="button" onClick={() => void previewImpact()} disabled={saving || previewing}>{previewing ? "正在试算" : preview ? "重新预览影响" : "预览影响"}</button>}
        <button type="submit" disabled={saving || previewing}>{saving ? "保存中" : "保存预算"}</button>
      </div>
    </form>}
    <BudgetForecastHistoryReport credentials={credentials} canEdit={canEdit} previewBusy={previewing || saving} refreshKey={items.map((item) => `${item.id}:${item.revision}`).join("|")} onPreviewRecommendation={previewRecommendation} onPreviewRollback={previewRollback} />
  </section>;
}

function BudgetForecastHistoryReport({ credentials, canEdit, previewBusy, refreshKey, onPreviewRecommendation, onPreviewRollback }: { credentials: Credentials; canEdit: boolean; previewBusy: boolean; refreshKey: string; onPreviewRecommendation: (recommendation: BudgetPolicyRecommendation) => Promise<void>; onPreviewRollback: (effect: BudgetPolicyEffect) => Promise<void> }) {
  const [range, setRange] = useState("90d");
  const [history, setHistory] = useState<BudgetForecastHistory | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [actionError, setActionError] = useState("");
  const [previewingBudgetID, setPreviewingBudgetID] = useState("");
  const [decisionEditingKey, setDecisionEditingKey] = useState("");
  const [decision, setDecision] = useState<"accepted" | "rejected">("accepted");
  const [decisionReason, setDecisionReason] = useState("");
  const [savingDecision, setSavingDecision] = useState(false);
  const [effectReviewEditingID, setEffectReviewEditingID] = useState("");
  const [effectDisposition, setEffectDisposition] = useState<"rollback_planned" | "continue_observing">("rollback_planned");
  const [effectReviewReason, setEffectReviewReason] = useState("");
  const [effectCloseEditingID, setEffectCloseEditingID] = useState("");
  const [effectCloseReason, setEffectCloseReason] = useState("");
  const [savingEffectReview, setSavingEffectReview] = useState(false);
  const [exporting, setExporting] = useState("");
  const load = useCallback(async () => {
    await Promise.resolve();
    setLoading(true);
    setError("");
    try {
      setHistory(await performanceFetch<BudgetForecastHistory>(`/v1/ops/performance/budgets/forecast-history?range=${range}&limit=100`, credentials));
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "预测命中历史暂时不可用");
    } finally {
      setLoading(false);
    }
  }, [credentials, range]);
  useEffect(() => {
    const timer = window.setTimeout(() => void load(), 0);
    return () => window.clearTimeout(timer);
  }, [load, refreshKey]);

  const handlePreviewRecommendation = async (recommendation: BudgetPolicyRecommendation) => {
    setActionError("");
    setPreviewingBudgetID(recommendation.budget_id);
    try {
      await onPreviewRecommendation(recommendation);
    } catch (cause) {
      setActionError(cause instanceof Error ? cause.message : "建议暂时无法带入预览");
    } finally {
      setPreviewingBudgetID("");
    }
  };
  const handlePreviewRollback = async (effect: BudgetPolicyEffect) => {
    setActionError("");
    setPreviewingBudgetID(effect.budget_id);
    try {
      await onPreviewRollback(effect);
    } catch (cause) {
      setActionError(cause instanceof Error ? cause.message : "回滚建议暂时无法带入预览");
    } finally {
      setPreviewingBudgetID("");
    }
  };

  const openDecisionEditor = (recommendation: BudgetPolicyRecommendation) => {
    setActionError("");
    if (decisionEditingKey === recommendation.recommendation_key) {
      setDecisionEditingKey("");
      return;
    }
    setDecision(recommendation.feedback?.decision ?? "accepted");
    setDecisionReason(recommendation.feedback?.reason ?? "");
    setDecisionEditingKey(recommendation.recommendation_key);
  };

  const saveDecision = async (event: FormEvent, recommendation: BudgetPolicyRecommendation) => {
    event.preventDefault();
    setSavingDecision(true);
    setActionError("");
    try {
      await performanceFetch<{ feedback: BudgetPolicyDecision }>(`/v1/ops/performance/budgets/${recommendation.budget_id}/recommendations/${recommendation.recommendation_key}/decision?range=${range}`, credentials, { method: "POST", body: JSON.stringify({ decision, reason: decisionReason }) });
      setDecisionEditingKey("");
      await load();
    } catch (cause) {
      setActionError(cause instanceof Error ? cause.message : "调优决策保存失败");
    } finally {
      setSavingDecision(false);
    }
  };

  const openEffectReviewEditor = (effect: BudgetPolicyEffect) => {
    setActionError("");
    setEffectCloseEditingID("");
    if (effectReviewEditingID === effect.decision_id) {
      setEffectReviewEditingID("");
      return;
    }
    setEffectDisposition(effect.review?.disposition ?? "rollback_planned");
    setEffectReviewReason(effect.review?.reason ?? "");
    setEffectReviewEditingID(effect.decision_id);
  };

  const saveEffectReview = async (event: FormEvent, effect: BudgetPolicyEffect) => {
    event.preventDefault();
    setSavingEffectReview(true);
    setActionError("");
    try {
      await performanceFetch<{ review: BudgetPolicyEffectReview }>(`/v1/ops/performance/budgets/${effect.budget_id}/effects/${effect.decision_id}/acknowledge`, credentials, { method: "POST", body: JSON.stringify({ disposition: effectDisposition, reason: effectReviewReason }) });
      setEffectReviewEditingID("");
      await load();
    } catch (cause) {
      setActionError(cause instanceof Error ? cause.message : "效果处置记录失败");
    } finally {
      setSavingEffectReview(false);
    }
  };

  const openEffectCloseEditor = (effect: BudgetPolicyEffect) => {
    setActionError("");
    setEffectReviewEditingID("");
    if (effectCloseEditingID === effect.decision_id) {
      setEffectCloseEditingID("");
      return;
    }
    setEffectCloseReason("");
    setEffectCloseEditingID(effect.decision_id);
  };

  const closeEffectReview = async (event: FormEvent, effect: BudgetPolicyEffect) => {
    event.preventDefault();
    setSavingEffectReview(true);
    setActionError("");
    try {
      await performanceFetch<{ review: BudgetPolicyEffectReview }>(`/v1/ops/performance/budgets/${effect.budget_id}/effects/${effect.decision_id}/close`, credentials, { method: "POST", body: JSON.stringify({ reason: effectCloseReason }) });
      setEffectCloseEditingID("");
      await load();
    } catch (cause) {
      setActionError(cause instanceof Error ? cause.message : "效果建议关闭失败");
    } finally {
      setSavingEffectReview(false);
    }
  };

  const downloadReview = async (format: "markdown" | "json") => {
    setExporting(format);
    setActionError("");
    try {
      const response = await adminFetch(`/v1/ops/performance/budgets/report?range=${range}&format=${format}`, { headers: { ...adminHeaders(credentials) }, cache: "no-store" });
      if (!response.ok) { const payload = await response.json().catch(() => ({})) as { message?: string }; throw new Error(payload.message || `导出失败（${response.status}）`); }
      const blob = await response.blob();
      const href = URL.createObjectURL(blob); const anchor = document.createElement("a");
      anchor.href = href; anchor.download = `performance-budget-review-${range}.${format === "markdown" ? "md" : "json"}`; anchor.click(); URL.revokeObjectURL(href);
    } catch (cause) {
      setActionError(cause instanceof Error ? cause.message : "预算复盘导出失败");
    } finally {
      setExporting("");
    }
  };

  const items = history?.items ?? [];
  const budgets = history?.budgets ?? [];
  const recommendations = history?.recommendations ?? [];
  const effects = history?.effects ?? [];
  return <section className={styles.forecastHistory} aria-label="预算预测命中历史">
    <header><div><p>FORECAST OUTCOMES</p><h3>预测命中历史</h3></div><div className={styles.forecastHistoryActions}><button type="button" disabled={Boolean(exporting)} onClick={() => void downloadReview("markdown")}>{exporting === "markdown" ? "生成中" : "导出复盘"}</button><button type="button" disabled={Boolean(exporting)} onClick={() => void downloadReview("json")}>JSON</button><label>统计周期<select value={range} onChange={(event) => { setRange(event.target.value); setDecisionEditingKey(""); setEffectReviewEditingID(""); setEffectCloseEditingID(""); setActionError(""); }}><option value="30d">最近 30 天</option><option value="90d">最近 90 天</option><option value="365d">最近一年</option></select></label></div></header>
    {error ? <p className={styles.forecastHistoryError} role="alert">{error}</p> : <>
      <div className={styles.forecastHistoryKpis}>
        <article><span>提前预测</span><strong>{history?.predictions ?? 0}</strong><small>预测超限事故</small></article>
        <article><span>已结论命中率</span><strong>{history?.decided ? formatRate(history.hit_rate) : "暂无结论"}</strong><small>{history?.decided ?? 0} 条已有结果</small></article>
        <article><span>平均提前量</span><strong>{history?.hits ? formatHistoryDuration(history.average_lead_time_minutes) : "—"}</strong><small>预测至实际超限</small></article>
        <article><span>仍在观察</span><strong>{history?.observing ?? 0}</strong><small>不计入命中率</small></article>
      </div>
      {recommendations.length > 0 && <div className={styles.forecastRecommendations}>
        <div><strong>只读策略建议</strong><span>至少 5 条已有结论记录才会建议调整</span></div>
        <section>{recommendations.map((item) => <article key={item.budget_id} data-action={item.action}>
          <header><div><strong>{item.budget_name}</strong><small>{moduleLabel(item.module)} · {item.period === "daily" ? "每日" : "每月"}</small></div><span>{policyRecommendationLabel(item.action)}</span></header>
          <p>{item.reason}</p>
          <dl><div><dt>历史结论</dt><dd>{item.decided} 条</dd></div><div><dt>命中率</dt><dd>{item.decided ? formatRate(item.hit_rate) : "—"}</dd></div><div><dt>当前门槛</dt><dd>{item.current_min_samples} 样本</dd></div><div><dt>建议门槛</dt><dd>{item.proposed_min_samples} 样本</dd></div></dl>
          {item.feedback && <div className={styles.recommendationFeedback} data-decision={item.feedback.decision}><div><strong>{item.feedback.applied_at ? `已应用 · r${item.feedback.applied_budget_revision}` : item.feedback.decision === "accepted" ? "已记录采纳" : "已记录拒绝"}</strong><span>{item.feedback.decided_by} · {new Date(item.feedback.decided_at).toLocaleString("zh-CN")}</span></div><p>{item.feedback.reason}</p></div>}
          <footer><span>{confidenceLabel(item.confidence)}可信度</span>{item.requires_impact_preview && canEdit ? <div className={styles.recommendationActions}><button type="button" onClick={() => void handlePreviewRecommendation(item)} disabled={previewBusy || savingDecision || Boolean(previewingBudgetID)}>{previewingBudgetID === item.budget_id ? "正在试算" : "带入并预览"}</button><button type="button" data-kind="secondary" onClick={() => openDecisionEditor(item)} disabled={savingDecision}>{decisionEditingKey === item.recommendation_key ? "收起决策" : item.feedback ? "修改决策" : "记录决策"}</button></div> : <b>{item.requires_impact_preview ? "仅管理员可操作" : "当前不建议变更"}</b>}</footer>
          {decisionEditingKey === item.recommendation_key && <form className={styles.recommendationDecisionForm} onSubmit={(event) => void saveDecision(event, item)}>
            <label>人工决策<select value={decision} onChange={(event) => setDecision(event.target.value as "accepted" | "rejected")}><option value="accepted">采纳建议</option><option value="rejected">拒绝建议</option></select></label>
            <label>决策原因<textarea required minLength={2} maxLength={512} value={decisionReason} onChange={(event) => setDecisionReason(event.target.value)} placeholder="说明采纳或拒绝的依据" /></label>
            <div><span>只记录决定，不会修改预算参数。</span><button type="submit" disabled={savingDecision || previewBusy}>{savingDecision ? "保存中" : "保存决策"}</button></div>
          </form>}
        </article>)}</section>
      </div>}
      {effects.length > 0 && <div className={styles.policyEffects}>
        <div><strong>应用后效果</strong><span>仅统计应用 revision 下新开的预测事故</span></div>
        <section>{effects.map((item) => <article key={item.decision_id} data-status={item.status}>
          <header><div><strong>{item.budget_name || shortID(item.budget_id)}</strong><small>{moduleLabel(item.module)} · {item.current_min_samples} → {item.proposed_min_samples} 样本</small></div><span>{policyEffectStatusLabel(item.status)}</span></header>
          <div className={styles.policyEffectComparison}>
            <div><span>应用前</span><strong>{item.before.decided ? formatRate(item.before.hit_rate) : "—"}</strong><small>{item.before.decided} 条结论 · 提前 {formatHistoryDuration(item.before.average_lead_time_minutes)}</small></div>
            <b>→</b>
            <div><span>应用后</span><strong>{item.after.decided ? formatRate(item.after.hit_rate) : "收集中"}</strong><small>{item.after.decided} 条结论 · {item.after.hits ? `提前 ${formatHistoryDuration(item.after.average_lead_time_minutes)}` : "暂无命中"}</small></div>
          </div>
          <dl><div><dt>命中率变化</dt><dd>{item.after.decided ? formatRateDelta(item.after.hit_rate - item.before.hit_rate) : "—"}</dd></div><div><dt>提前量变化</dt><dd>{item.after.hits && item.before.hits ? formatMinuteDelta(item.after.average_lead_time_minutes - item.before.average_lead_time_minutes) : "—"}</dd></div><div><dt>观察门槛</dt><dd>{item.minimum_decided_samples} 条 / {item.observation_days} 天</dd></div><div><dt>应用版本</dt><dd>r{item.applied_budget_revision}</dd></div></dl>
          <p>{item.note}</p>
          {item.recommend_rollback && <aside className={styles.policyRollbackSuggestion}><div><strong>建议恢复原参数</strong><span>{item.rollback_lookback_days} 天历史 · 至少 {item.rollback_min_samples} 个预测样本</span></div><p>{item.rollback_reason}</p>{canEdit && item.review?.status !== "closed" && !item.review?.rollback_applied_at ? <div className={styles.policyRollbackActions}><button type="button" onClick={() => void handlePreviewRollback(item)} disabled={previewBusy || savingDecision || savingEffectReview || Boolean(previewingBudgetID)}>{previewingBudgetID === item.budget_id ? "正在试算" : "带入并预览回滚"}</button><button type="button" data-kind="secondary" onClick={() => openEffectReviewEditor(item)} disabled={savingEffectReview}>{effectReviewEditingID === item.decision_id ? "收起处置" : item.review ? "修改处置" : "确认异常"}</button></div> : <b>{item.review?.rollback_applied_at ? "回滚已执行" : item.review?.status === "closed" ? "处置已关闭" : "仅管理员可操作"}</b>}</aside>}
          {item.review && <aside className={styles.policyEffectReview} data-status={item.review.status}>
            <div><strong>{effectDispositionLabel(item.review.disposition)}</strong><span>{item.review.status === "closed" ? "已关闭" : "已确认"}</span></div>
            <p>{item.review.reason}</p>
            {item.review.rollback_applied_at ? <p className={styles.policyRollbackExecution} data-status="applied"><strong>回滚已关联 · r{item.review.rollback_budget_revision}</strong><span>{item.review.rollback_applied_by} · {new Date(item.review.rollback_applied_at).toLocaleString("zh-CN")}</span></p> : item.review.status === "acknowledged" && item.review.disposition === "rollback_planned" ? <p className={styles.policyRollbackExecution} data-status={item.rollback_can_apply ? "ready" : "stale"}><strong>{item.rollback_can_apply ? "等待人工保存原参数" : "计划需重新核对"}</strong><span>{item.rollback_can_apply ? "保存后会自动关联执行版本，但不会自动关闭处置。" : "当前预算版本或参数已变化，保存原参数不会自动关联。"}</span></p> : null}
            {item.rollback_verification && <section className={styles.policyRollbackReview} data-status={item.rollback_verification.status}>
              <header><div><strong>回滚后复盘</strong><small>原始基线 {item.before.decided ? formatRate(item.before.hit_rate) : "—"}</small></div><span>{rollbackVerificationStatusLabel(item.rollback_verification.status)}</span></header>
              <div className={styles.policyRollbackComparison}>
                <div><span>异常版本 r{item.applied_budget_revision}</span><strong>{item.after.decided ? formatRate(item.after.hit_rate) : "—"}</strong><small>{item.after.decided} 条结论 · 提前 {formatHistoryDuration(item.after.average_lead_time_minutes)}</small></div>
                <b>→</b>
                <div><span>回滚版本 r{item.rollback_verification.budget_revision}</span><strong>{item.rollback_verification.after.decided ? formatRate(item.rollback_verification.after.hit_rate) : "收集中"}</strong><small>{item.rollback_verification.after.decided} / {item.rollback_verification.minimum_decided_samples} 条结论</small></div>
              </div>
              <p>{item.rollback_verification.note}</p>
              <footer><span>{item.rollback_verification.observation_complete ? "观察期已结束" : `观察至 ${new Date(item.rollback_verification.observation_ends_at).toLocaleDateString("zh-CN")}`}</span><span>{item.rollback_verification.observation_days} 天观察期</span></footer>
            </section>}
            <div><span>{item.review.reviewed_by} · {new Date(item.review.reviewed_at).toLocaleString("zh-CN")}</span>{canEdit && item.review.status === "acknowledged" && <button type="button" onClick={() => openEffectCloseEditor(item)} disabled={savingEffectReview}>{effectCloseEditingID === item.decision_id ? "取消关闭" : "关闭建议"}</button>}</div>
            {item.review.closed_at && <small>{item.review.closed_by} 于 {new Date(item.review.closed_at).toLocaleString("zh-CN")} 关闭：{item.review.closed_reason}</small>}
          </aside>}
          {effectReviewEditingID === item.decision_id && <form className={styles.policyEffectReviewForm} onSubmit={(event) => void saveEffectReview(event, item)}><label>处置方向<select value={effectDisposition} onChange={(event) => setEffectDisposition(event.target.value as "rollback_planned" | "continue_observing")}><option value="rollback_planned">计划回滚</option><option value="continue_observing">继续观察</option></select></label><label>确认依据<textarea required minLength={2} maxLength={512} value={effectReviewReason} onChange={(event) => setEffectReviewReason(event.target.value)} placeholder="记录指标判断、负责人或后续计划" /></label><div><span>只记录处置，不会修改预算。</span><button type="submit" disabled={savingEffectReview || previewBusy}>{savingEffectReview ? "保存中" : "确认并留痕"}</button></div></form>}
          {effectCloseEditingID === item.decision_id && <form className={styles.policyEffectReviewForm} onSubmit={(event) => void closeEffectReview(event, item)}><label>关闭说明<textarea required minLength={2} maxLength={512} value={effectCloseReason} onChange={(event) => setEffectCloseReason(event.target.value)} placeholder="说明已完成回滚流程、继续观察或无需处理的依据" /></label><div><span>关闭后不可再次修改该处置。</span><button type="submit" disabled={savingEffectReview || previewBusy}>{savingEffectReview ? "关闭中" : "确认关闭"}</button></div></form>}
          <footer><span>{item.applied_by} · {item.observation_complete ? "观察期已结束" : `观察至 ${new Date(item.observation_ends_at).toLocaleDateString("zh-CN")}`}</span><span>{new Date(item.applied_at).toLocaleString("zh-CN")}</span></footer>
        </article>)}</section>
      </div>}
      {actionError && <p className={styles.forecastRecommendationError} role="alert">{actionError}</p>}
      {history?.predictions ? <div className={styles.forecastHistoryColumns}>
        <div className={styles.forecastHistoryGroup}>
          <strong>按预算汇总</strong>
          <div>{budgets.map((item) => <article key={item.budget_id}>
            <div><span>{item.budget_name}</span><small>{moduleLabel(item.module)} · {item.period === "daily" ? "每日" : "每月"}</small></div>
            <dl><div><dt>预测</dt><dd>{item.predictions}</dd></div><div><dt>命中</dt><dd>{item.hits}</dd></div><div><dt>提前解除</dt><dd>{item.cleared}</dd></div><div><dt>命中率</dt><dd>{item.decided ? formatRate(item.hit_rate) : "—"}</dd></div></dl>
          </article>)}</div>
        </div>
        <div className={styles.forecastHistoryGroup}>
          <strong>最近预测结果</strong>
          <div>{items.slice(0, 8).map((item) => <article key={item.incident_id} data-outcome={item.outcome}>
            <div><span>{item.budget_name}</span><b>{forecastOutcomeLabel(item.outcome)}</b></div>
            <p>{item.projected_cost_micros > 0 ? `当时预计 ${formatCost(item.projected_cost_micros)} · ${formatRate(item.projected_utilization)}` : "早期记录未保存完整预测快照"}</p>
            <footer><span>{new Date(item.opened_at).toLocaleString("zh-CN", { month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit" })}</span><span>{item.outcome === "observing" ? `${item.forecast_sample_size} 个样本` : `持续 ${formatHistoryDuration(item.duration_minutes)}`}</span></footer>
          </article>)}</div>
        </div>
      </div> : <div className={styles.forecastHistoryEmpty}>{loading ? "正在汇总预测结果…" : "所选周期内还没有预测超限事故。"}</div>}
      {history?.note && <footer className={styles.forecastHistoryNote}>{history.note}</footer>}
    </>}
  </section>;
}

function BudgetImpactPreviewView({ preview }: { preview: BudgetImpactPreview }) {
  return <section className={styles.budgetImpactPreview} data-change={preview.change}>
    <header><div><strong>{budgetImpactChangeLabel(preview.change)}</strong><small>基于 {new Date(preview.generated_at).toLocaleString("zh-CN")} 前的真实数据</small></div><span>只读试算</span></header>
    <div className={styles.budgetImpactGrid}>
      <BudgetImpactCard label="当前配置" item={preview.current} />
      <BudgetImpactCard label="拟议配置" item={preview.proposed} />
    </div>
    <footer>{preview.note}</footer>
  </section>;
}

function BudgetImpactCard({ label, item }: { label: string; item: BudgetImpactProjection }) {
  return <article data-alert={item.will_alert} data-severity={item.severity}>
    <div><span>{label}</span><b>{budgetImpactSignalLabel(item.signal)}</b></div>
    <strong>{formatCost(item.used_cost_micros)} <small>/ {formatCost(item.cost_limit_micros)}</small></strong>
    <dl>
      <div><dt>当前使用</dt><dd>{formatRate(item.utilization)}</dd></div>
      <div><dt>周期预测</dt><dd>{item.forecast_risk === "not_evaluated" ? "未执行" : formatCost(item.projected_cost_micros)}</dd></div>
      <div><dt>预测策略</dt><dd>{item.forecast_alerts_enabled ? `${item.forecast_lookback_days} 天 / ${item.forecast_min_samples} 样本` : "已关闭"}</dd></div>
      <div><dt>预测样本</dt><dd>{item.forecast_risk === "not_evaluated" ? "—" : `${item.sample_size} 个`}</dd></div>
    </dl>
    <p>{item.reason}</p>
  </article>;
}

function AnalysisSignal({ analysis, loading }: { analysis: Analysis | null; loading: boolean }) {
  if (!analysis) return <section className={styles.analysisSignal} data-status="loading"><i /><div><strong>{loading ? "正在建立历史基线" : "暂无异常分析"}</strong><span>系统会比较最近 1 小时与此前 24 小时。</span></div></section>;
  const title = analysis.status === "stable" ? "当前指标与历史基线一致" : analysis.status === "insufficient_data" ? "当前样本不足，暂不触发异常" : analysis.status === "critical" ? "发现严重指标偏移" : "发现需要关注的指标偏移";
  return <section className={styles.analysisSignal} data-status={analysis.status}>
    <i /><div><strong>{title}</strong><span>{analysis.note || (analysis.anomalies.length ? analysis.anomalies.map((item) => item.message).join("；") : `最近 1 小时 ${analysis.current.sample_size} 个样本，历史基线 ${analysis.baseline.sample_size} 个样本。`)}</span></div>
    <small>1h / 前 24h</small>
  </section>;
}

function PerformanceKPI({ label, value, note, tone }: { label: string; value: string; note: string; tone: string }) {
  return <article className={`${styles.metricCard} ${styles[tone]}`}><div><span>{label}</span><i /></div><strong>{value}</strong><small>{note}</small></article>;
}

function DeltaCard({ label, value, baseline, kind, positive }: { label: string; value: number; baseline: number; kind: "rate" | "duration" | "cost"; positive: "up" | "down" }) {
  const difference = value - baseline;
  const improved = positive === "up" ? difference > 0 : difference < 0;
  const unchanged = Math.abs(difference) < .00001;
  const formatted = kind === "rate" ? formatRate(value) : kind === "duration" ? formatDuration(value) : formatCost(value);
  const delta = kind === "rate" ? `${difference >= 0 ? "+" : ""}${(difference * 100).toFixed(1)}pp` : baseline > 0 ? `${difference >= 0 ? "+" : ""}${((difference / baseline) * 100).toFixed(1)}%` : "新基线";
  return <article data-direction={unchanged ? "flat" : improved ? "better" : "worse"}><span>{label}</span><strong>{formatted}</strong><small>{delta} · 基线 {kind === "rate" ? formatRate(baseline) : kind === "duration" ? formatDuration(baseline) : formatCost(baseline)}</small></article>;
}

function VersionTable({ items, loading }: { items: VersionMetric[]; loading: boolean }) {
  if (!items.length) return <EmptyAnalysis text={loading ? "正在加载版本数据…" : "当前范围内没有版本数据"} />;
  return <div className={styles.tableScroll}><table className={`${styles.runTable} ${styles.performanceTable}`}><thead><tr><th>版本</th><th>样本</th><th>成功率</th><th>质量失败</th><th>P95 耗时</th><th>Token</th><th>总成本 / 平均</th></tr></thead><tbody>{items.map((item) => <tr key={item.group_id}><td><strong>{item.label}</strong><small>{item.version_id ? shortID(item.version_id) : "内置运行配置"}</small></td><td><strong>{item.sample_size}</strong><small>{item.runs} 个运行</small></td><td><strong>{formatRate(item.success_rate)}</strong><small>{item.completed} 个完成</small></td><td><strong>{formatRate(item.quality_failure_rate)}</strong><small>{item.quality_failures} 个判定失败</small></td><td><strong>{formatDuration(item.p95_duration_ms)}</strong><small>P50 {formatDuration(item.p50_duration_ms)}</small></td><td><strong>{compactNumber(item.prompt_tokens + item.completion_tokens)}</strong><small>{item.model_calls} 次调用</small></td><td><strong>{formatCost(item.cost_micros)}</strong><small>{formatCost(item.average_cost_micros)} / 运行</small></td></tr>)}</tbody></table></div>;
}

function ModelTable({ items, loading }: { items: ModelMetric[]; loading: boolean }) {
  if (!items.length) return <EmptyAnalysis text={loading ? "正在加载模型调用…" : "当前范围内没有实际模型调用"} />;
  return <div className={styles.tableScroll}><table className={`${styles.runTable} ${styles.performanceTable}`}><thead><tr><th>服务 / 模型</th><th>调用</th><th>失败率</th><th>P95 延迟</th><th>Token</th><th>缓存 Token</th><th>总成本 / 单次</th></tr></thead><tbody>{items.map((item) => <tr key={`${item.provider}:${item.model}`}><td><strong>{item.model}</strong><small>{item.provider}</small></td><td><strong>{item.calls}</strong><small>{item.succeeded} 次成功</small></td><td><strong>{formatRate(item.error_rate)}</strong><small>{item.failed} 次失败</small></td><td><strong>{formatDuration(item.p95_latency_ms)}</strong><small>平均 {formatDuration(item.average_latency_ms)}</small></td><td><strong>{compactNumber(item.prompt_tokens + item.completion_tokens)}</strong><small>{compactNumber(item.prompt_tokens)} 输入</small></td><td><strong>{compactNumber(item.cached_tokens)}</strong><small>供应商返回值</small></td><td><strong>{formatCost(item.cost_micros)}</strong><small>{formatCost(item.average_cost_micros)} / 调用</small></td></tr>)}</tbody></table></div>;
}

function EmptyAnalysis({ text }: { text: string }) { return <div className={styles.performanceEmpty}>{text}</div>; }
function rate(value: number, total: number) { return total > 0 ? value / total : 0; }
function formatRate(value: number) { return `${(value * 100).toFixed(value >= .01 ? 1 : 2)}%`; }
function formatCost(micros: number) { return `$${(micros / 1_000_000).toFixed(micros >= 10_000 ? 3 : 5)}`; }
function formatDuration(ms: number) { if (ms < 1000) return `${Math.max(0, Math.round(ms))}ms`; if (ms < 60_000) return `${(ms / 1000).toFixed(ms < 10_000 ? 2 : 1)}s`; return `${Math.floor(ms / 60_000)}m ${Math.round((ms % 60_000) / 1000)}s`; }
function compactNumber(value: number) { return new Intl.NumberFormat("zh-CN", { notation: "compact", maximumFractionDigits: 1 }).format(value); }
function shortID(value: string) { return value.length > 14 ? `${value.slice(0, 8)}…${value.slice(-4)}` : value; }
function formatTrendTime(value: string, range: string) { const date = new Date(value); return range === "24h" ? `${date.getHours().toString().padStart(2, "0")}:00` : `${date.getMonth() + 1}/${date.getDate()}`; }
function moduleLabel(value?: string) { return value === "work" ? "工作" : value === "life" ? "生活" : value === "companion" ? "陪伴" : "全部模块"; }
function budgetStatusLabel(value: BudgetStatus["status"]) { return value === "exceeded" ? "已超限" : value === "warning" ? "接近上限" : value === "disabled" ? "已停用" : "正常"; }
function forecastRiskLabel(value: BudgetForecast["risk"]) { return value === "exceeded" ? "已经超额" : value === "projected_exceeded" ? "预计超额" : value === "projected_warning" ? "预计接近上限" : value === "disabled" ? "预算已停用" : value === "insufficient_data" ? "数据不足" : "预计正常"; }
function budgetImpactSignalLabel(value: BudgetImpactProjection["signal"]) { return value === "actual_exceeded" ? "实际已超限" : value === "actual_warning" ? "实际已预警" : value === "projected_exceeded" ? "预测会告警" : value === "insufficient_samples" ? "样本门槛拦截" : value === "disabled" ? "预算停用" : "不会告警"; }
function budgetImpactChangeLabel(value: BudgetImpactPreview["change"]) { return value === "would_start_alerting" ? "保存后将开始告警" : value === "would_stop_alerting" ? "保存后将停止当前告警" : value === "would_escalate" ? "保存后告警级别将升级" : value === "would_deescalate" ? "保存后告警级别将降低" : value === "would_change_signal" ? "保存后判定信号会改变" : "保存前后告警结果一致"; }
function forecastOutcomeLabel(value: BudgetForecastOutcome["outcome"]) { return value === "hit" ? "最终超限" : value === "cleared" ? "提前解除" : "观察中"; }
function policyRecommendationLabel(value: BudgetPolicyRecommendation["action"]) { return value === "increase_sample_gate" ? "提高样本门槛" : value === "decrease_sample_gate" ? "降低样本门槛" : value === "keep_policy" ? "保持当前策略" : value === "not_applicable" ? "暂不适用" : "继续积累数据"; }
function policyEffectStatusLabel(value: BudgetPolicyEffect["status"]) { return value === "improved" ? "效果改善" : value === "regressed" ? "需要复核" : value === "mixed" ? "部分改善" : value === "stable" ? "变化不明显" : value === "insufficient_data" ? "样本不足" : "数据收集中"; }
function rollbackVerificationStatusLabel(value: BudgetPolicyRollbackVerification["status"]) { return value === "recovered" ? "已恢复" : value === "improving" ? "正在恢复" : value === "mixed" ? "表现分化" : value === "not_recovered" ? "尚未恢复" : value === "insufficient_data" ? "样本不足" : "数据收集中"; }
function effectDispositionLabel(value: BudgetPolicyEffectReview["disposition"]) { return value === "rollback_planned" ? "计划回滚" : "继续观察"; }
function formatRateDelta(value: number) { return `${value >= 0 ? "+" : ""}${(value * 100).toFixed(1)}pp`; }
function formatMinuteDelta(value: number) { return `${value >= 0 ? "+" : ""}${Math.round(value)} 分钟`; }
function formatHistoryDuration(minutes: number) { if (minutes < 60) return `${Math.max(0, Math.round(minutes))} 分钟`; if (minutes < 1440) return `${(minutes / 60).toFixed(minutes < 600 ? 1 : 0)} 小时`; return `${(minutes / 1440).toFixed(minutes < 14_400 ? 1 : 0)} 天`; }
function confidenceLabel(value: "none" | "low" | "medium" | "high") { return value === "high" ? "高" : value === "medium" ? "中" : value === "low" ? "低" : "暂无"; }
function formatHours(value: number) { return value >= 48 ? `${Math.round(value / 24)} 天` : `${Math.max(1, Math.round(value))} 小时`; }
function forecastTimeLabel(value: string | undefined, risk: BudgetForecast["risk"]) { if (risk === "disabled") return "未启用"; if (!value) return "本周期不会触达"; if (risk === "exceeded") return "已触达"; return new Date(value).toLocaleString("zh-CN", { month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit" }); }

async function performanceFetch<T>(path: string, credentials: Credentials, init: RequestInit = {}): Promise<T> {
  const response = await adminFetch(`${path}`, { ...init, headers: { "Content-Type": "application/json", ...init.headers, ...adminHeaders(credentials) }, cache: "no-store" });
  const payload = await response.json().catch(() => ({})) as { message?: string };
  if (!response.ok) throw new Error(payload.message || `请求失败 (${response.status})`);
  return payload as T;
}
