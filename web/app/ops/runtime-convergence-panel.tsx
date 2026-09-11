"use client";

import { useCallback, useEffect, useState } from "react";

import styles from "./operations.module.css";

const apiBase = process.env.NEXT_PUBLIC_API_BASE_URL ?? "http://localhost:8080";

type Credentials = { key: string };
type RuntimeState = {
  instance_id: string;
  service: string;
  kind: string;
  key: string;
  version_id?: string;
  revision?: number;
  desired_version_id?: string;
  desired_revision?: number;
  convergence: "converged" | "outdated" | "error" | "offline";
  last_error?: string;
  started_at: string;
  last_seen_at: string;
};
type Convergence = {
  environment: string;
  generated_at: string;
  stale_after_seconds: number;
  summary: Record<string, number>;
  instances: RuntimeState[];
};

const statusLabels: Record<string, string> = { converged: "已收敛", outdated: "待更新", error: "加载错误", offline: "已离线" };
const kindLabels: Record<string, string> = { billing_plan: "套餐", model_profile: "模型 Profile", agent_definition: "Agent", prompt: "Prompt" };

export function RuntimeConvergencePanel({ credentials }: { credentials: Credentials }) {
  const [report, setReport] = useState<Convergence | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    try {
      const payload = await fetchConvergence(credentials);
      setReport(payload);
      setError("");
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "配置收敛状态暂时不可用");
    } finally {
      setLoading(false);
    }
  }, [credentials]);

  useEffect(() => {
    let cancelled = false;
    void fetchConvergence(credentials).then((payload) => {
      if (cancelled) return;
      setReport(payload);
      setError("");
      setLoading(false);
    }).catch((cause: unknown) => {
      if (cancelled) return;
      setError(cause instanceof Error ? cause.message : "配置收敛状态暂时不可用");
      setLoading(false);
    });
    const timer = window.setInterval(() => void load(), 15_000);
    return () => { cancelled = true; window.clearInterval(timer); };
  }, [credentials, load]);

  return <section className={styles.convergenceWorkspace}>
    {error && <div className={styles.errorBanner} role="alert"><strong>收敛状态读取失败</strong><span>{error}</span></div>}
    <section className={styles.convergenceSummary}>
      <article><small>在线且一致</small><strong>{report?.summary.converged ?? 0}</strong><span>实际版本与期望修订一致</span></article>
      <article><small>等待更新</small><strong>{report?.summary.outdated ?? 0}</strong><span>实例仍使用旧版本</span></article>
      <article><small>加载错误</small><strong>{report?.summary.error ?? 0}</strong><span>保留上一份可用快照</span></article>
      <article><small>离线记录</small><strong>{report?.summary.offline ?? 0}</strong><span>超过 {report?.stale_after_seconds ?? 120} 秒未上报</span></article>
    </section>
    <article className={styles.configListCard}>
      <div className={styles.panelHeading}><div><p>RUNTIME CONFIGURATION PROOF</p><h2>实例加载状态</h2></div><span>{report?.environment ?? "—"} · {report?.instances.length ?? 0} 项</span></div>
      <div className={styles.convergenceTable}>
        <div className={styles.convergenceHead}><span>实例 / 服务</span><span>配置</span><span>实际 → 期望</span><span>最近上报</span><span>状态</span></div>
        {report?.instances.map((item) => <div key={`${item.instance_id}-${item.kind}-${item.key}`}>
          <span><strong>{shortID(item.instance_id)}</strong><small>{item.service}</small></span>
          <span><strong>{kindLabels[item.kind] ?? item.kind}</strong><small>{item.key}</small></span>
          <span><strong>r{item.revision ?? 0} → r{item.desired_revision ?? 0}</strong><small>{shortID(item.version_id ?? "未加载")} → {shortID(item.desired_version_id ?? "未知")}</small></span>
          <span><strong>{formatAgo(item.last_seen_at)}</strong><small>启动 {formatDate(item.started_at)}</small></span>
          <span><b data-status={item.convergence}>{statusLabels[item.convergence]}</b>{item.last_error && <small title={item.last_error}>{item.last_error}</small>}</span>
        </div>)}
      </div>
      {!loading && report?.instances.length === 0 && <div className={styles.emptyLine}>尚无实例上报；服务启动后的第一个轮询周期内会自动出现。</div>}
    </article>
  </section>;
}

async function fetchConvergence(credentials: Credentials): Promise<Convergence> {
  const response = await fetch(`${apiBase}/v1/ops/configuration/convergence`, { headers: { Authorization: `Bearer ${credentials.key}` }, cache: "no-store" });
  const payload = await response.json().catch(() => ({})) as Convergence & { message?: string };
  if (!response.ok) throw new Error(payload.message || `请求失败（${response.status}）`);
  return payload;
}

function shortID(value: string) { return value.length > 18 ? `${value.slice(0, 8)}…${value.slice(-5)}` : value; }
function formatDate(value: string) { return new Intl.DateTimeFormat("zh-CN", { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit" }).format(new Date(value)); }
function formatAgo(value: string) { const seconds = Math.max(0, Math.round((Date.now() - new Date(value).getTime()) / 1000)); return seconds < 60 ? `${seconds} 秒前` : `${Math.floor(seconds / 60)} 分钟前`; }
