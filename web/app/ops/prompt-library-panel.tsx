"use client";

import { FormEvent, useCallback, useEffect, useMemo, useState } from "react";

import { PromptDeployment } from "./agent-graph-canvas";
import styles from "./operations.module.css";

const apiBase = process.env.NEXT_PUBLIC_API_BASE_URL ?? "http://localhost:8080";

type Credentials = { key: string };
type Operator = { actor: string; role: string; mfa_verified: boolean; legacy: boolean };
type PromptVersion = {
  id: string;
  key: string;
  version: number;
  status: string;
  payload: { display_name?: string; description?: string; template?: string; variables?: string[] };
  fingerprint: string;
  created_by: string;
  submitted_by?: string;
  published_by?: string;
  updated_at: string;
};
type PromptPage = { versions: PromptVersion[]; deployments: PromptDeployment[] };

export function PromptLibraryPanel({ credentials, operator, refreshKey, onDeploymentsChange }: {
  credentials: Credentials;
  operator: Operator;
  refreshKey?: number;
  onDeploymentsChange?: (deployments: PromptDeployment[]) => void;
}) {
  const [page, setPage] = useState<PromptPage>({ versions: [], deployments: [] });
  const [promptKey, setPromptKey] = useState("trusted-answer");
  const [displayName, setDisplayName] = useState("可信回答");
  const [description, setDescription] = useState("供 Agent 模型节点复用的受治理 Prompt。");
  const [template, setTemplate] = useState("基于可信上下文回答 {{user_message}}，不要虚构已经执行的操作。");
  const [variables, setVariables] = useState("user_message");
  const [reason, setReason] = useState("");
  const [loading, setLoading] = useState(true);
  const [working, setWorking] = useState("");
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      const result = await promptFetch<PromptPage>("/v1/ops/prompts", credentials);
      setPage(result);
      onDeploymentsChange?.(result.deployments);
    } catch (cause) {
      setError(promptError(cause));
    } finally {
      setLoading(false);
    }
  }, [credentials, onDeploymentsChange]);

  useEffect(() => {
    let cancelled = false;
    void promptFetch<PromptPage>("/v1/ops/prompts", credentials).then((result) => {
      if (cancelled) return;
      setPage(result);
      setLoading(false);
      onDeploymentsChange?.(result.deployments);
    }).catch((cause: unknown) => {
      if (cancelled) return;
      setError(promptError(cause));
      setLoading(false);
    });
    return () => { cancelled = true; };
  }, [credentials, onDeploymentsChange, refreshKey]);

  const selectedVersions = useMemo(
    () => page.versions.filter((item) => item.key === promptKey).sort((a, b) => b.version - a.version),
    [page.versions, promptKey],
  );
  const active = page.deployments.find((item) => item.key === promptKey);

  function selectPrompt(key: string) {
    const deployment = page.deployments.find((item) => item.key === key);
    const latest = page.versions.filter((item) => item.key === key).sort((a, b) => b.version - a.version)[0];
    const selected = deployment?.version ?? latest;
    if (!selected) return;
    setPromptKey(key);
    setDisplayName(selected.payload.display_name ?? key);
    setDescription(selected.payload.description ?? "");
    setTemplate(selected.payload.template ?? "");
    setVariables((selected.payload.variables ?? []).join(", "));
    setReason("");
  }

  async function createDraft(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    await mutate("create", `/v1/ops/prompts/${encodeURIComponent(promptKey)}/versions`, {
      base_version: selectedVersions[0]?.version ?? 0,
      display_name: displayName,
      description,
      template,
      variables: variables.split(",").map((item) => item.trim().toLowerCase()).filter(Boolean),
      reason,
    });
  }

  async function workflow(version: PromptVersion, action: "validate" | "submit" | "publish") {
    await mutate(`${version.id}-${action}`, `/v1/ops/config-versions/${version.id}/${action}`);
  }

  async function rollback(version: PromptVersion) {
    await mutate(`${version.id}-rollback`, `/v1/ops/prompts/${encodeURIComponent(version.key)}/rollback`, {
      target_version: version.version,
      reason: reason || `restore prompt ${version.key} v${version.version}`,
    });
  }

  async function mutate(key: string, path: string, body?: unknown) {
    setWorking(key);
    setMessage("");
    setError("");
    try {
      const result = await promptFetch<{ runtime_message?: string }>(path, credentials, { method: "POST", body });
      setMessage(result.runtime_message ?? "Prompt 版本工作流已更新");
      await load();
    } catch (cause) {
      setError(promptError(cause));
    } finally {
      setWorking("");
    }
  }

  const keys = Array.from(new Set([...page.versions.map((item) => item.key), promptKey])).sort();
  return <>
    <section className={styles.configNotice}>
      <div><i />Prompt 独立资产</div>
      <p>模板与变量单独生成不可变版本；Agent 保存时锁定具体 Prompt 版本和指纹，发布新模板不会改变历史 Agent。</p>
    </section>
    {error && <div className={styles.errorBanner} role="alert"><strong>Prompt 操作未完成</strong><span>{error}</span></div>}
    {message && <div className={styles.successBanner} role="status">{message}</div>}
    <section className={styles.configLayout}>
      <article className={styles.configListCard}>
        <div className={styles.panelHeading}><div><p>PROMPT REGISTRY</p><h2>Prompt 资产</h2></div><span>{page.versions.length} 个版本</span></div>
        <div className={styles.planList}>{keys.map((key) => {
          const deployment = page.deployments.find((item) => item.key === key);
          const latest = page.versions.filter((item) => item.key === key).sort((a, b) => b.version - a.version)[0];
          const shown = deployment?.version ?? latest;
          return <button type="button" key={key} className={promptKey === key ? styles.selectedPlan : undefined} onClick={() => selectPrompt(key)}><span><strong>{shown?.payload.display_name ?? key}</strong><small>{key}{shown ? ` · v${shown.version}` : " · 新建"}</small></span>{deployment ? <b>已发布</b> : <em>草稿</em>}</button>;
        })}</div>
        <div className={styles.versionHistory}><h3>版本历史</h3>{selectedVersions.length === 0 ? <div className={styles.emptyLine}>保存后会在这里形成独立版本链。</div> : selectedVersions.map((version) => {
          const action = version.status === "draft" ? "validate" : version.status === "validated" ? "submit" : version.status === "submitted" ? "publish" : null;
          const sameAuthor = action === "publish" && (operator.actor === version.created_by || operator.actor === version.submitted_by);
          const canRollback = (version.status === "published" || version.status === "superseded") && active?.version.id !== version.id;
          return <div key={version.id}><span><strong>v{version.version}</strong><small>{shortFingerprint(version.fingerprint)} · {version.created_by}</small></span><b className={styles.configStatus} data-status={version.status}>{statusName(version.status)}</b><div className={styles.versionActions}>
            {canRollback && <button type="button" disabled={operator.role !== "admin" || !hasStepUp(operator) || working !== "" || version.created_by === operator.actor || version.published_by === operator.actor} onClick={() => void rollback(version)}>{working === `${version.id}-rollback` ? "回滚中" : "回滚到此版"}</button>}
            {action && <button type="button" disabled={operator.role !== "admin" || working !== "" || sameAuthor || (action === "publish" && !hasStepUp(operator))} onClick={() => void workflow(version, action)}>{working === `${version.id}-${action}` ? "处理中" : actionName(action)}</button>}
          </div></div>;
        })}</div>
      </article>
      <article className={styles.editorCard}>
        <div className={styles.panelHeading}><div><p>IMMUTABLE PROMPT</p><h2>编写 Prompt</h2></div><span>{active ? `线上 v${active.version.version}` : "尚未发布"}</span></div>
        <form className={styles.promptForm} onSubmit={createDraft}>
          <div className={styles.promptMetaGrid}>
            <label>Prompt Key<input value={promptKey} onChange={(event) => setPromptKey(event.target.value.toLowerCase())} pattern="[a-z0-9_-]+" required /></label>
            <label>显示名称<input value={displayName} onChange={(event) => setDisplayName(event.target.value)} maxLength={120} required /></label>
          </div>
          <label>用途说明<input value={description} onChange={(event) => setDescription(event.target.value)} maxLength={1000} /></label>
          <label>Prompt 模板<textarea className={styles.promptEditor} value={template} onChange={(event) => setTemplate(event.target.value)} maxLength={12000} spellCheck={false} required /></label>
          <label>变量（逗号分隔）<input value={variables} onChange={(event) => setVariables(event.target.value)} placeholder="user_message, trusted_context" /></label>
          <div className={styles.promptVariableHint}>使用 <code>{"{{variable_name}}"}</code> 引用变量；未声明变量、重复变量和括号不配对会被服务端阻断。</div>
          <label>变更原因<input value={reason} onChange={(event) => setReason(event.target.value)} required placeholder="说明修改目标、验证方法和回滚条件" /></label>
          <button type="submit" className={styles.sandboxRunButton} disabled={operator.role !== "admin" || working !== ""}>{working === "create" ? "正在保存…" : `保存为不可变 v${(selectedVersions[0]?.version ?? 0) + 1}`}</button>
        </form>
      </article>
    </section>
  </>;
}

async function promptFetch<T>(path: string, credentials: Credentials, options?: { method?: string; body?: unknown }): Promise<T> {
  const headers: Record<string, string> = { Authorization: `Bearer ${credentials.key}` };
  if (options?.body !== undefined) headers["Content-Type"] = "application/json";
  const response = await fetch(`${apiBase}${path}`, { method: options?.method ?? "GET", headers, body: options?.body === undefined ? undefined : JSON.stringify(options.body), cache: "no-store" });
  const payload = await response.json().catch(() => ({})) as { message?: string };
  if (!response.ok) throw new Error(payload.message || `请求失败（${response.status}）`);
  return payload as T;
}

function promptError(cause: unknown) { return cause instanceof Error ? cause.message : "Prompt 版本库暂时不可用"; }
function shortFingerprint(value: string) { return value ? `${value.slice(0, 8)}…${value.slice(-4)}` : "—"; }
function statusName(value: string) { return ({ draft: "草稿", validated: "已校验", submitted: "待审批", published: "已发布", superseded: "已替代" } as Record<string, string>)[value] ?? value; }
function actionName(value: "validate" | "submit" | "publish") { return ({ validate: "校验", submit: "提交", publish: "发布" } as const)[value]; }
function hasStepUp(operator: Operator) { return operator.mfa_verified || operator.legacy; }
