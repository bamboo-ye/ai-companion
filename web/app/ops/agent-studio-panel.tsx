"use client";

import { FormEvent, useCallback, useEffect, useMemo, useState } from "react";

import { AgentGraphCanvas, parseStudioDefinition, PromptDeployment } from "./agent-graph-canvas";
import styles from "./operations.module.css";
import { PromptLibraryPanel } from "./prompt-library-panel";

const apiBase = process.env.NEXT_PUBLIC_API_BASE_URL ?? "http://localhost:8080";

type Credentials = { key: string };
type Operator = { actor: string; role: string; mfa_verified: boolean; legacy: boolean };
type AgentVersion = {
  id: string;
  key: string;
  version: number;
  status: string;
  payload: Record<string, unknown>;
  fingerprint: string;
  created_by: string;
  submitted_by?: string;
  updated_at: string;
};
type AgentPage = { versions: AgentVersion[]; deployments: { key: string; revision: number; version: AgentVersion }[] };
type AgentCompilation = {
  compiler_version: string;
  executable: boolean;
  fingerprint: string;
  entry_node: string;
  nodes: { key: string; type: string; model_role?: string; tools?: string[]; outgoing: { to: string; condition?: string }[] }[];
  budget: { max_steps: number; max_model_calls: number; max_tool_calls: number; max_total_tokens: number; timeout_ms: number };
};
type AgentSandboxReport = {
  schema_version: string;
  run_id: string;
  mode: "synthetic";
  status: string;
  outcome: string;
  response: string;
  safety: { side_effects: boolean; production_data_access: boolean; external_model_calls: number; external_tool_calls: number };
  budget: { limits: Record<string, unknown>; usage: Record<string, unknown> };
  node_outputs: Record<string, unknown>;
  node_trace: { node?: string; status?: string; duration_ms?: number; details?: Record<string, unknown> }[];
  model_calls: { graph_node?: string; role?: string; prompt_tokens?: number; completion_tokens?: number }[];
  tool_calls: { node?: string; stage?: string; tool_name?: string; status?: string; external_call?: boolean }[];
  interrupts: { type?: string; tool_name?: string; risk_level?: string; task_id?: string }[];
};
type AgentNodeDebug = {
  node: { key: string; type: string; model_role?: string; prompt?: { key: string; version_id?: string; version?: number; fingerprint?: string }; prompt_template?: string; tools?: string[] };
  status: string;
  input: Record<string, unknown>;
  output?: unknown;
  trace: { node?: string; status?: string; duration_ms?: number; details?: Record<string, unknown> }[];
  model_calls: Record<string, unknown>[];
  tool_calls: Record<string, unknown>[];
};
type AgentEvaluationRun = {
  id: string;
  agent_version_id: string;
  agent_fingerprint: string;
  suite: { name: string; version: string; baseline_run_id?: string; cases: { id: string }[] };
  suite_fingerprint: string;
  status: string;
  decision?: "pass" | "fail";
  summary: {
    total_cases: number; passed_cases: number; failed_cases: number; pass_rate: number;
    model_calls: number; tool_calls: number; total_tokens: number;
    comparison?: { baseline_run_id: string; pass_rate_delta: number; model_calls_delta: number; total_tokens_delta: number };
  };
  results: { case_id: string; passed: boolean; failure?: string; status?: string; outcome?: string; assertions: { name: string; passed: boolean }[] }[];
  created_by: string;
  started_at: string;
};
type AgentRolloutMetrics = {
  sample_size: number; completed: number; failed: number; quality_failures: number;
  error_rate: number; quality_failure_rate: number; p95_latency_ms: number; average_cost_micros: number;
};
type AgentRollout = {
  id: string; agent_key: string; agent_version_id: string; agent_version: number;
  baseline_version_id: string; baseline_version: number; traffic_percent: number;
  status: "running" | "ready" | "paused" | "promoted" | "aborted";
  decision: "collecting" | "pass" | "fail"; revision: number;
  policy: {
    minimum_sample_size: number; observation_window_seconds: number; max_error_rate: number;
    max_quality_failure_rate: number; max_p95_latency_ms: number; max_average_cost_micros: number;
    max_error_rate_regression: number; max_p95_latency_regression_ratio: number; max_average_cost_regression_ratio: number;
  };
  summary: { candidate: AgentRolloutMetrics; baseline: AgentRolloutMetrics; window_started_at?: string; window_ended_at?: string };
  violations: { metric: string; expected: number; actual: number; message: string }[];
  created_at: string; evaluated_at?: string;
};

const starterGraph = JSON.stringify({
  display_name: "工作助理",
  description: "对工作请求分类并生成受预算约束的回复。",
  modules: ["work"],
  model_profile: "production-default",
  entry_node: "route",
  nodes: [
    { key: "route", type: "router", model_role: "router", prompt_template: "判断用户请求应直接回答还是进入任务处理。" },
    { key: "answer", type: "model", model_role: "responder", prompt_template: "基于可信上下文回答用户，不虚构执行结果。" },
    { key: "done", type: "end" },
  ],
  edges: [
    { from: "route", to: "answer", condition: "respond" },
    { from: "route", to: "done", condition: "stop" },
    { from: "answer", to: "done" },
  ],
  budget: { max_steps: 8, max_model_calls: 4, max_tool_calls: 0, max_total_tokens: 12000, timeout_ms: 120000 },
}, null, 2);

export function AgentStudioPanel({ credentials, operator }: { credentials: Credentials; operator: Operator }) {
  const [workspaceView, setWorkspaceView] = useState<"agents" | "prompts">("agents");
  const [editorView, setEditorView] = useState<"visual" | "source">("visual");
  const [promptRefresh, setPromptRefresh] = useState(0);
  const [page, setPage] = useState<AgentPage>({ versions: [], deployments: [] });
  const [promptDeployments, setPromptDeployments] = useState<PromptDeployment[]>([]);
  const [agentKey, setAgentKey] = useState("work-assistant");
  const [definition, setDefinition] = useState(starterGraph);
  const [reason, setReason] = useState("");
  const [loading, setLoading] = useState(true);
  const [working, setWorking] = useState("");
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");
  const [compilation, setCompilation] = useState<AgentCompilation | null>(null);
  const [sandboxReport, setSandboxReport] = useState<AgentSandboxReport | null>(null);
	const [nodeDebug, setNodeDebug] = useState<AgentNodeDebug | null>(null);
	const [selectedNode, setSelectedNode] = useState("route");
	const [evaluationSuite, setEvaluationSuite] = useState("");
	const [evaluationRuns, setEvaluationRuns] = useState<Record<string, AgentEvaluationRun[]>>({});
  const [rollouts, setRollouts] = useState<AgentRollout[]>([]);
  const [rolloutPercent, setRolloutPercent] = useState(10);
  const [sandboxModule, setSandboxModule] = useState("work");
  const [sandboxMessage, setSandboxMessage] = useState("请根据合成上下文处理这条测试请求");
  const [routeChoices, setRouteChoices] = useState<Record<string, string>>({});
  const [modelOutputs, setModelOutputs] = useState<Record<string, string>>({});
  const [toolNames, setToolNames] = useState<Record<string, string>>({});
  const [toolOutputs, setToolOutputs] = useState<Record<string, string>>({});
  const [toolApprovals, setToolApprovals] = useState<Record<string, "none" | "pause" | "approve" | "reject">>({});

  const load = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      setPage(await studioFetch<AgentPage>("/v1/ops/agents", credentials));
    } catch (cause) {
      setError(studioError(cause));
    } finally {
      setLoading(false);
    }
  }, [credentials]);

  useEffect(() => {
    let cancelled = false;
    void studioFetch<AgentPage>("/v1/ops/agents", credentials).then((result) => {
      if (cancelled) return;
      setPage(result);
      setLoading(false);
    }).catch((cause: unknown) => {
      if (cancelled) return;
      setError(studioError(cause));
      setLoading(false);
    });
    return () => { cancelled = true; };
  }, [credentials]);

  const loadPrompts = useCallback(async () => {
    try {
      const result = await studioFetch<{ deployments: PromptDeployment[] }>("/v1/ops/prompts", credentials);
      setPromptDeployments(result.deployments);
    } catch {
      setPromptDeployments([]);
    }
  }, [credentials]);

  useEffect(() => {
    let cancelled = false;
    void studioFetch<{ deployments: PromptDeployment[] }>("/v1/ops/prompts", credentials)
      .then((result) => { if (!cancelled) setPromptDeployments(result.deployments); })
      .catch(() => { if (!cancelled) setPromptDeployments([]); });
    return () => { cancelled = true; };
  }, [credentials]);

  const selectedVersions = useMemo(
    () => page.versions.filter((item) => item.key === agentKey).sort((a, b) => b.version - a.version),
    [agentKey, page.versions],
  );

  const parsedDefinition = useMemo(() => parseStudioDefinition(definition), [definition]);
  const visitedNodes = useMemo(() => {
    const result = new Set<string>();
    if (!sandboxReport || !parsedDefinition) return result;
    for (const node of parsedDefinition.nodes) {
      if (sandboxReport.node_trace.some((item) => runtimeNodeMatches(item.node, node.key))) result.add(node.key);
    }
    return result;
  }, [parsedDefinition, sandboxReport]);

	const selectedEvaluationVersion = selectedVersions[0];
	const latestEvaluationRun = selectedEvaluationVersion ? evaluationRuns[selectedEvaluationVersion.id]?.[0] : undefined;

	useEffect(() => {
		if (!selectedEvaluationVersion) return;
		let cancelled = false;
		void studioFetch<{ evaluation_runs: AgentEvaluationRun[] }>(`/v1/ops/agent-versions/${selectedEvaluationVersion.id}/evaluations?limit=10`, credentials).then((result) => {
			if (!cancelled) {
				setEvaluationSuite(defaultEvaluationSuite(selectedEvaluationVersion.payload));
				setEvaluationRuns((current) => ({ ...current, [selectedEvaluationVersion.id]: result.evaluation_runs }));
			}
		}).catch(() => {
			if (!cancelled) {
				setEvaluationSuite(defaultEvaluationSuite(selectedEvaluationVersion.payload));
				setEvaluationRuns((current) => ({ ...current, [selectedEvaluationVersion.id]: [] }));
			}
		});
		return () => { cancelled = true; };
	}, [credentials, selectedEvaluationVersion]);

  const loadRollouts = useCallback(async () => {
    if (!agentKey || !/^[a-z0-9_-]+$/.test(agentKey)) return;
    const result = await studioFetch<{ rollouts: AgentRollout[] }>(`/v1/ops/agents/${encodeURIComponent(agentKey)}/rollouts?limit=20`, credentials);
    setRollouts(result.rollouts);
  }, [agentKey, credentials]);

  useEffect(() => {
    let cancelled = false;
    if (!agentKey || !/^[a-z0-9_-]+$/.test(agentKey)) return;
    void studioFetch<{ rollouts: AgentRollout[] }>(`/v1/ops/agents/${encodeURIComponent(agentKey)}/rollouts?limit=20`, credentials)
      .then((result) => { if (!cancelled) setRollouts(result.rollouts); })
      .catch(() => { if (!cancelled) setRollouts([]); });
    return () => { cancelled = true; };
  }, [agentKey, credentials]);

  async function createDraft(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    try {
      const payload = JSON.parse(definition) as Record<string, unknown>;
      await mutate("create", `/v1/ops/agents/${encodeURIComponent(agentKey)}/versions`, {
        base_version: selectedVersions[0]?.version ?? 0,
        ...payload,
        reason,
      });
    } catch (cause) {
      setError(cause instanceof SyntaxError ? "Agent DSL JSON 格式无效" : studioError(cause));
    }
  }

  async function workflow(version: AgentVersion, action: "validate" | "submit" | "publish") {
    await mutate(`${version.id}-${action}`, `/v1/ops/config-versions/${version.id}/${action}`);
  }

  async function compileDraft() {
    setWorking("compile");
    setMessage("");
    setError("");
    setCompilation(null);
    setSandboxReport(null);
    try {
      const payload = JSON.parse(definition) as Record<string, unknown>;
      const result = await studioFetch<{ compilation: AgentCompilation }>(`/v1/ops/agents/${encodeURIComponent(agentKey)}/compile`, credentials, { method: "POST", body: payload });
      setCompilation(result.compilation);
      const modules = Array.isArray(payload.modules) ? payload.modules.filter((item): item is string => typeof item === "string") : [];
      if (!modules.includes(sandboxModule) && modules[0]) setSandboxModule(modules[0]);
      setMessage("沙箱编译通过；未调用模型或业务工具");
    } catch (cause) {
      setError(cause instanceof SyntaxError ? "Agent DSL JSON 格式无效" : studioError(cause));
    } finally {
      setWorking("");
    }
  }

  async function runSandbox(debugNode = "") {
    if (!compilation) return;
    setWorking(debugNode ? "node-debug" : "dry-run");
    setMessage("");
    setError("");
    setSandboxReport(null);
		setNodeDebug(null);
    try {
      const payload = JSON.parse(definition) as Record<string, unknown>;
      const routes = Object.fromEntries(compilation.nodes.filter((node) => node.type === "router").map((node) => [node.key, { condition: routeChoices[node.key] ?? node.outgoing[0]?.condition ?? "" }]));
      const models = Object.fromEntries(compilation.nodes.filter((node) => node.type === "model").map((node) => [node.key, { response: modelOutputs[node.key] || `合成模型输出：${node.key} 已完成。` }]));
      const tools = Object.fromEntries(compilation.nodes.filter((node) => node.type === "tool").map((node) => {
        const approval = toolApprovals[node.key] ?? "none";
        return [node.key, {
          tool_name: toolNames[node.key] ?? node.tools?.[0],
          arguments: {},
          response: toolOutputs[node.key] || `合成工具 ${node.tools?.[0] ?? node.key} 已完成。`,
          requires_approval: approval !== "none",
          approval: approval === "none" ? "pause" : approval,
          risk_level: approval === "none" ? "none" : "medium",
        }];
      }));
      const result = await studioFetch<{ report: AgentSandboxReport; node_debug?: AgentNodeDebug }>(`/v1/ops/agents/${encodeURIComponent(agentKey)}/dry-run`, credentials, { method: "POST", body: { definition: payload, scenario: { module: sandboxModule, user_message: sandboxMessage, routes, models, tools }, ...(debugNode ? { debug_node: debugNode } : {}) } });
      setSandboxReport(result.report);
      setNodeDebug(result.node_debug ?? null);
      setMessage(debugNode ? `节点 ${debugNode} 调试完成；已聚焦该节点的输入、输出与调用证据` : result.report.status === "completed" ? "合成试跑完成；外部模型和业务工具调用均为 0" : "合成试跑已安全停在中断点");
    } catch (cause) {
      setError(cause instanceof SyntaxError ? "Agent DSL JSON 格式无效" : studioError(cause));
    } finally {
      setWorking("");
    }
  }

	async function runEvaluation(version: AgentVersion, suiteText = evaluationSuite) {
		setWorking(`${version.id}-evaluation`);
		setMessage("");
		setError("");
		try {
			const suite = JSON.parse(suiteText) as Record<string, unknown>;
			const result = await studioFetch<{ evaluation_run: AgentEvaluationRun }>(`/v1/ops/agent-versions/${version.id}/evaluations`, credentials, { method: "POST", body: suite });
			setEvaluationRuns((current) => ({ ...current, [version.id]: [result.evaluation_run, ...(current[version.id] ?? []).filter((item) => item.id !== result.evaluation_run.id)] }));
			setMessage(result.evaluation_run.decision === "pass" ? "版本化评测通过，已解锁提交门禁" : "评测已完成，但发布门禁仍处于阻断状态");
		} catch (cause) {
			setError(cause instanceof SyntaxError ? "评测套件 JSON 格式无效" : studioError(cause));
		} finally {
			setWorking("");
		}
	}

  async function startRollout(version: AgentVersion) {
    setWorking(`${version.id}-rollout`);
    setMessage("");
    setError("");
    try {
      const result = await studioFetch<{ rollout: AgentRollout }>(`/v1/ops/agent-versions/${version.id}/rollouts`, credentials, {
        method: "POST", body: { traffic_percent: rolloutPercent },
      });
      setRollouts((current) => [result.rollout, ...current.filter((item) => item.id !== result.rollout.id)]);
      setMessage(`${rolloutPercent}% 灰度已启动；系统将按用户稳定分流并采集线上门禁指标`);
    } catch (cause) {
      setError(studioError(cause));
    } finally {
      setWorking("");
    }
  }

  async function refreshRollout(rollout: AgentRollout) {
    setWorking(`${rollout.id}-refresh`);
    setMessage("");
    setError("");
    try {
      const result = await studioFetch<{ rollout: AgentRollout }>(`/v1/ops/agent-rollouts/${rollout.id}/refresh`, credentials, { method: "POST" });
      setRollouts((current) => [result.rollout, ...current.filter((item) => item.id !== result.rollout.id)]);
      setMessage(result.rollout.status === "ready" ? "线上门禁已通过，可以全量发布" : result.rollout.status === "paused" ? "指标越线，灰度流量已自动暂停" : "样本仍在采集中");
    } catch (cause) {
      setError(studioError(cause));
    } finally {
      setWorking("");
    }
  }

  async function abortRollout(rollout: AgentRollout) {
    setWorking(`${rollout.id}-abort`);
    setMessage("");
    setError("");
    try {
      const result = await studioFetch<{ rollout: AgentRollout }>(`/v1/ops/agent-rollouts/${rollout.id}/abort`, credentials, { method: "POST", body: { reason: "operator stopped rollout from Agent Studio" } });
      setRollouts((current) => [result.rollout, ...current.filter((item) => item.id !== result.rollout.id)]);
      setMessage("灰度已停止；所有后续新 Run 已回到稳定版本");
    } catch (cause) {
      setError(studioError(cause));
    } finally {
      setWorking("");
    }
  }

  async function mutate(key: string, path: string, body?: unknown) {
    setWorking(key);
    setMessage("");
    setError("");
    try {
      const result = await studioFetch<{ runtime_message?: string }>(path, credentials, { method: "POST", body });
	      setMessage(result.runtime_message ?? "Agent 版本工作流已更新");
	      await load();
	      await loadRollouts();
    } catch (cause) {
      setError(studioError(cause));
    } finally {
      setWorking("");
    }
  }

  function updateDefinition(value: string) {
    setDefinition(value);
    setCompilation(null);
    setSandboxReport(null);
    setNodeDebug(null);
  }

  const studioHeader = <>
    <header className={styles.topbar}>
      <div><p>OPERATIONS / AGENT STUDIO</p><h1>Agent Studio</h1></div>
      <div className={styles.actions}><span className={styles.permissionTag}>{operator.role} · {operator.mfa_verified ? "MFA 已验证" : operator.legacy ? "开发密钥授权" : "未验证 MFA"}</span><button type="button" onClick={() => { if (workspaceView === "agents") void load(); else { setPromptRefresh((value) => value + 1); void loadPrompts(); } }} disabled={loading}>{loading ? "同步中" : "刷新版本"}</button></div>
    </header>
    <nav className={styles.configTabs} aria-label="Agent Studio 工作区">
      <button type="button" className={workspaceView === "agents" ? styles.activeTab : undefined} onClick={() => setWorkspaceView("agents")}>Agent 编排</button>
      <button type="button" className={workspaceView === "prompts" ? styles.activeTab : undefined} onClick={() => setWorkspaceView("prompts")}>Prompt 版本库</button>
    </nav>
  </>;

  if (workspaceView === "prompts") {
    return <>{studioHeader}<PromptLibraryPanel credentials={credentials} operator={operator} refreshKey={promptRefresh} onDeploymentsChange={setPromptDeployments} /></>;
  }

  return <>
		{studioHeader}
    <section className={styles.configNotice}>
      <div><i />声明式 Agent DSL</div>
      <p>节点、边、模型角色、工具白名单和预算会在服务端校验。每次保存生成不可变版本，发布遵循双人审批与 MFA。</p>
    </section>
    {error && <div className={styles.errorBanner} role="alert"><strong>Studio 操作未完成</strong><span>{error}</span></div>}
    {message && <div className={styles.successBanner} role="status">{message}</div>}
    <section className={styles.configLayout}>
      <article className={styles.configListCard}>
        <div className={styles.panelHeading}><div><p>VERSION REGISTRY</p><h2>Agent 版本</h2></div><span>{page.versions.length} 个版本</span></div>
        <div className={styles.planList}>{Array.from(new Set(page.versions.map((item) => item.key))).sort().map((key) => {
          const active = page.deployments.find((item) => item.key === key);
          const latest = page.versions.filter((item) => item.key === key).sort((a, b) => b.version - a.version)[0];
          return <button type="button" key={key} className={agentKey === key ? styles.selectedPlan : undefined} onClick={() => { const selected = active?.version ?? latest; setAgentKey(key); setDefinition(JSON.stringify(selected.payload, null, 2)); setSelectedNode(typeof selected.payload.entry_node === "string" ? selected.payload.entry_node : ""); setSandboxModule(firstModule(selected.payload)); setCompilation(null); setSandboxReport(null); setNodeDebug(null); setEvaluationSuite(defaultEvaluationSuite(selected.payload)); }}><span><strong>{String((active?.version ?? latest).payload.display_name ?? key)}</strong><small>{key} · v{(active?.version ?? latest).version}</small></span>{active ? <b>已发布</b> : <em>草稿</em>}</button>;
        })}</div>
	        <div className={styles.versionHistory}><h3>版本历史</h3>{selectedVersions.length === 0 ? <div className={styles.emptyLine}>当前 key 尚无版本</div> : selectedVersions.map((version) => {
	          const action = version.status === "draft" ? "validate" : version.status === "validated" ? "submit" : version.status === "submitted" ? "publish" : null;
	          const latestEvaluation = evaluationRuns[version.id]?.[0];
	          const evaluationPassed = latestEvaluation?.decision === "pass";
	          const active = page.deployments.find((item) => item.key === version.key);
	          const requiresRollout = version.status === "submitted" && Boolean(active && active.version.id !== version.id);
	          const versionRollout = rollouts.find((item) => item.agent_version_id === version.id);
	          const rolloutReady = versionRollout?.status === "ready" && versionRollout.decision === "pass";
	          const sameAuthor = action === "publish" && (operator.actor === version.created_by || operator.actor === version.submitted_by);
	          return <div key={version.id}><span><strong>v{version.version}</strong><small>{shortFingerprint(version.fingerprint)} · {version.created_by}</small>{latestEvaluation && <small className={styles.evalHint} data-decision={latestEvaluation.decision}>{latestEvaluation.decision === "pass" ? "评测通过" : "评测阻断"} · {latestEvaluation.summary.passed_cases}/{latestEvaluation.summary.total_cases}</small>}{versionRollout && <small className={styles.evalHint} data-decision={versionRollout.decision}>{rolloutStatusName(versionRollout)} · {versionRollout.traffic_percent}%</small>}</span><b className={styles.configStatus} data-status={version.status}>{statusName(version.status)}</b><div className={styles.versionActions}>{version.status !== "draft" && <button type="button" disabled={working !== "" || !canDryRun(operator.role)} onClick={() => { const suite = defaultEvaluationSuite(version.payload); setEvaluationSuite(suite); void runEvaluation(version, suite); }}>{working === `${version.id}-evaluation` ? "评测中" : "评测"}</button>}{requiresRollout && !versionRollout && <button type="button" disabled={operator.role !== "admin" || !hasStepUp(operator) || working !== "" || sameAuthor} onClick={() => void startRollout(version)}>{working === `${version.id}-rollout` ? "启动中" : "启动灰度"}</button>}{action && <button type="button" disabled={operator.role !== "admin" || working !== "" || sameAuthor || (action === "submit" && !evaluationPassed) || (action === "publish" && (!hasStepUp(operator) || (requiresRollout && !rolloutReady)))} onClick={() => void workflow(version, action)}>{working === `${version.id}-${action}` ? "处理中" : actionName(action)}</button>}</div></div>;
	        })}</div>
      </article>
      <article className={styles.editorCard}>
        <div className={styles.panelHeading}><div><p>VISUAL GRAPH EDITOR</p><h2>编写 Agent</h2></div><div className={styles.editorSwitch}><button type="button" data-active={editorView === "visual"} onClick={() => setEditorView("visual")}>画布</button><button type="button" data-active={editorView === "source"} onClick={() => setEditorView("source")}>源码</button></div></div>
        <form className={styles.profileForm} onSubmit={createDraft}>
          <label>Agent Key<input value={agentKey} onChange={(event) => { setAgentKey(event.target.value.toLowerCase()); setCompilation(null); setSandboxReport(null); }} pattern="[a-z0-9_-]+" required /></label>
					{editorView === "visual" ? <AgentGraphCanvas definition={definition} prompts={promptDeployments} selectedNode={selectedNode} visitedNodes={visitedNodes} onSelectNode={setSelectedNode} onDefinitionChange={updateDefinition} /> : <label>声明式图定义<textarea spellCheck={false} value={definition} onChange={(event) => updateDefinition(event.target.value)} required /></label>}
          <label>变更原因<input value={reason} onChange={(event) => setReason(event.target.value)} required placeholder="说明目标、验证方式和回滚条件" /></label>
          <div className={styles.studioActions}>
            <button type="button" onClick={() => void compileDraft()} disabled={working !== ""}>{working === "compile" ? "编译中…" : "沙箱编译"}</button>
            <button className={styles.primaryWide} disabled={operator.role !== "admin" || working !== ""}>{working === "create" ? "正在创建…" : "保存不可变草稿"}</button>
          </div>
        </form>
        {compilation && <div className={styles.compilePreview}>
          <strong>{compilation.compiler_version} · 可执行</strong>
          <span>入口 {compilation.entry_node} · {compilation.nodes.length} 个节点 · 指纹 {shortFingerprint(compilation.fingerprint)}</span>
          <span>预算：{compilation.budget.max_steps} 步 / {compilation.budget.max_model_calls} 次模型 / {compilation.budget.max_tool_calls} 次工具 / {compilation.budget.max_total_tokens.toLocaleString()} tokens</span>
          <ol>{compilation.nodes.map((node) => <li key={node.key}><b>{node.key}</b> · {node.type}{node.model_role ? ` / ${node.model_role}` : ""} → {node.outgoing.map((edge) => `${edge.condition ? `${edge.condition}:` : ""}${edge.to}`).join("，") || "结束"}</li>)}</ol>
        </div>}
        {compilation && <section className={styles.sandboxCard}>
          <div className={styles.subheading}><div><p>SYNTHETIC DRY RUN</p><h3>合成数据试跑</h3></div><span>零外部调用</span></div>
          <div className={styles.sandboxFields}>
            <label>测试模块<select value={sandboxModule} onChange={(event) => setSandboxModule(event.target.value)}>{definitionModules(definition).map((module) => <option key={module} value={module}>{moduleName(module)}</option>)}</select></label>
            <label className={styles.fullField}>合成用户请求<input value={sandboxMessage} onChange={(event) => setSandboxMessage(event.target.value)} maxLength={12000} required /></label>
            {compilation.nodes.filter((node) => node.type === "router").map((node) => <label key={node.key}>路由 · {node.key}<select value={routeChoices[node.key] ?? node.outgoing[0]?.condition ?? ""} onChange={(event) => setRouteChoices((current) => ({ ...current, [node.key]: event.target.value }))}>{node.outgoing.map((edge) => <option key={edge.condition} value={edge.condition}>{edge.condition} → {edge.to}</option>)}</select></label>)}
            {compilation.nodes.filter((node) => node.type === "model").map((node) => <label key={node.key} className={styles.fullField}>模型替身输出 · {node.key}<input value={modelOutputs[node.key] ?? ""} onChange={(event) => setModelOutputs((current) => ({ ...current, [node.key]: event.target.value }))} maxLength={12000} placeholder="此处内容将作为合成模型结果" /></label>)}
            {compilation.nodes.filter((node) => node.type === "tool").map((node) => <div key={node.key} className={styles.toolFixture}>
              <strong>工具替身 · {node.key}</strong>
              <label>选择工具<select value={toolNames[node.key] ?? node.tools?.[0] ?? ""} onChange={(event) => setToolNames((current) => ({ ...current, [node.key]: event.target.value }))}>{(node.tools ?? []).map((tool) => <option key={tool}>{tool}</option>)}</select></label>
              <label>替身结果<input value={toolOutputs[node.key] ?? ""} onChange={(event) => setToolOutputs((current) => ({ ...current, [node.key]: event.target.value }))} placeholder="合成工具已完成" /></label>
              <label>审批分支<select value={toolApprovals[node.key] ?? "none"} onChange={(event) => setToolApprovals((current) => ({ ...current, [node.key]: event.target.value as "none" | "pause" | "approve" | "reject" }))}><option value="none">无需审批</option><option value="pause">停在待审批</option><option value="approve">模拟批准并继续</option><option value="reject">模拟拒绝</option></select></label>
            </div>)}
          </div>
			<div className={styles.debugRunActions}>
				<button type="button" className={styles.sandboxRunButton} onClick={() => void runSandbox()} disabled={working !== "" || !canDryRun(operator.role)}>{working === "dry-run" ? "试跑中…" : canDryRun(operator.role) ? "运行完整试跑" : "需要 support 或 admin 权限"}</button>
				<button type="button" className={styles.nodeDebugButton} onClick={() => void runSandbox(selectedNode)} disabled={working !== "" || !selectedNode || !canDryRun(operator.role)}>{working === "node-debug" ? "调试中…" : selectedNode ? `调试节点 · ${selectedNode}` : "先选择节点"}</button>
			</div>
        </section>}
        {sandboxReport && <SandboxReport report={sandboxReport} />}
		{nodeDebug && <NodeDebugReport debug={nodeDebug} />}
		{selectedEvaluationVersion && <section className={styles.evaluationCard}>
			<div className={styles.subheading}><div><p>VERSIONED EVALUATION GATE</p><h3>版本化评测门禁</h3></div><span>v{selectedEvaluationVersion.version} · {selectedEvaluationVersion.status === "draft" ? "请先校验" : "可执行"}</span></div>
			<p>套件沿用 agent-eval-case-v1；每个模块至少一个场景，并强制校验状态、结果、模型调用和执行步数。通过记录与当前版本指纹绑定。</p>
			<textarea className={styles.evaluationEditor} spellCheck={false} value={evaluationSuite} onChange={(event) => setEvaluationSuite(event.target.value)} />
			<button type="button" className={styles.sandboxRunButton} onClick={() => void runEvaluation(selectedEvaluationVersion)} disabled={working !== "" || selectedEvaluationVersion.status === "draft" || !canDryRun(operator.role)}>{working === `${selectedEvaluationVersion.id}-evaluation` ? "正在运行评测…" : selectedEvaluationVersion.status === "draft" ? "先完成静态校验" : "运行版本化评测"}</button>
			{(evaluationRuns[selectedEvaluationVersion.id]?.length ?? 0) > 0 && <div className={styles.evaluationHistory}>{evaluationRuns[selectedEvaluationVersion.id].slice(0, 5).map((run) => <span key={run.id} data-decision={run.decision}><b>{run.decision === "pass" ? "通过" : "阻断"}</b><small>{run.suite.version} · {run.id.slice(0, 8)}</small></span>)}</div>}
		</section>}
		{latestEvaluationRun && <EvaluationReport run={latestEvaluationRun} />}
		<section className={styles.rolloutCard}>
			<div className={styles.subheading}><div><p>ONLINE CANARY GATE</p><h3>线上灰度发布</h3></div><span>{rollouts[0] ? rolloutStatusName(rollouts[0]) : "暂无灰度"}</span></div>
			<div className={styles.rolloutControls}><label>候选流量<select value={rolloutPercent} onChange={(event) => setRolloutPercent(Number(event.target.value))}><option value={5}>5%</option><option value={10}>10%</option><option value={25}>25%</option><option value={50}>50%</option></select></label><p>同一用户稳定进入同一版本；错误率、质量失败、P95 延迟和平均成本任一越线都会自动停流。</p></div>
			{rollouts[0] ? <RolloutReport rollout={rollouts[0]} working={working} operator={operator} onRefresh={refreshRollout} onAbort={abortRollout} /> : <div className={styles.emptyLine}>提交新版本后，可在左侧版本操作中启动受控灰度。</div>}
		</section>
        <p className={styles.runtimeCaveat}>已发布定义会绑定到后续新 Run，按声明的节点和边执行，并固化版本、预算、超时和工具白名单；已有 Run 始终使用原快照。沙箱编译只检查执行计划；合成试跑会执行同一份 LangGraph，但模型、工具、审批和等待均为隔离替身，不访问生产数据。</p>
      </article>
    </section>
  </>;
}

function NodeDebugReport({ debug }: { debug: AgentNodeDebug }) {
  const prompt = debug.node.prompt;
  return <section className={styles.nodeDebugReport}>
    <div className={styles.subheading}><div><p>NODE-LEVEL DEBUG</p><h3>{debug.node.key} · {debug.node.type}</h3></div><span data-status={debug.status}>{debug.status === "not_visited" ? "本路径未经过" : debug.status}</span></div>
    {prompt && <div className={styles.debugPromptEvidence}><strong>Prompt {prompt.key} · v{prompt.version ?? "待解析"}</strong><span>{prompt.fingerprint ? shortFingerprint(prompt.fingerprint) : "保存 Agent 版本时锁定"}</span></div>}
    <div className={styles.debugEvidenceGrid}>
      <div><h4>节点输入</h4><pre>{safeJSON(debug.input)}</pre></div>
      <div><h4>节点输出</h4><pre>{safeJSON(debug.output ?? null)}</pre></div>
    </div>
    <div className={styles.debugCallStrip}>
      <span><strong>{debug.trace.length}</strong> 条节点轨迹</span>
      <span><strong>{debug.model_calls.length}</strong> 次模型调用</span>
      <span><strong>{debug.tool_calls.length}</strong> 次工具调用</span>
      <span><strong>{debug.node.model_role ?? debug.node.tools?.join(", ") ?? "无外部依赖"}</strong> 执行契约</span>
    </div>
    {debug.trace.length > 0 && <ol className={styles.sandboxTrace}>{debug.trace.map((item, index) => <li key={`${item.node}-${index}`}><i data-status={item.status} /><span><strong>{friendlyNode(item.node)}</strong><small>{traceSummary(item)}</small></span><em>{item.duration_ms ?? 0} ms</em></li>)}</ol>}
  </section>;
}

function SandboxReport({ report }: { report: AgentSandboxReport }) {
  const usage = report.budget.usage;
  return <section className={styles.sandboxReport}>
    <div className={styles.subheading}><div><p>DRY RUN REPORT</p><h3>试跑报告</h3></div><span data-status={report.status}>{sandboxStatus(report.status)}</span></div>
    <div className={styles.sandboxSafety}>
      <strong>{report.safety.side_effects || report.safety.production_data_access ? "安全边界异常" : "隔离边界通过"}</strong>
      <span>外部模型 {report.safety.external_model_calls} · 外部工具 {report.safety.external_tool_calls} · 生产数据 {report.safety.production_data_access ? "已访问" : "未访问"}</span>
    </div>
    <blockquote>{report.response || "本次路径没有生成回复"}</blockquote>
    <div className={styles.sandboxStats}>
      <div><small>结果</small><strong>{report.outcome}</strong></div>
      <div><small>模型调用</small><strong>{asNumber(usage.model_calls)}</strong></div>
      <div><small>Token</small><strong>{asNumber(usage.prompt_tokens) + asNumber(usage.completion_tokens)}</strong></div>
      <div><small>工具动作</small><strong>{asNumber(usage.actions)}</strong></div>
    </div>
    {report.interrupts.length > 0 && <div className={styles.sandboxInterrupt}><strong>等待点</strong><span>{interruptName(report.interrupts[0])}</span></div>}
    <ol className={styles.sandboxTrace}>{report.node_trace.map((item, index) => <li key={`${item.node}-${index}`}><i data-status={item.status} /><span><strong>{friendlyNode(item.node)}</strong><small>{traceSummary(item)}</small></span><em>{item.duration_ms ?? 0} ms</em></li>)}</ol>
  </section>;
}

function EvaluationReport({ run }: { run: AgentEvaluationRun }) {
	const comparison = run.summary.comparison;
	return <section className={styles.evaluationReport}>
		<div className={styles.subheading}><div><p>EVALUATION REPORT</p><h3>{run.suite.name} · {run.suite.version}</h3></div><span data-decision={run.decision}>{run.decision === "pass" ? "门禁通过" : "门禁阻断"}</span></div>
		<div className={styles.sandboxStats}>
			<div><small>通过场景</small><strong>{run.summary.passed_cases}/{run.summary.total_cases}</strong></div>
			<div><small>通过率</small><strong>{Math.round(run.summary.pass_rate * 100)}%</strong></div>
			<div><small>模型调用</small><strong>{run.summary.model_calls}</strong></div>
			<div><small>Token</small><strong>{run.summary.total_tokens}</strong></div>
		</div>
		{comparison && <div className={styles.evaluationComparison}><strong>相对基线</strong><span>通过率 {formatDelta(comparison.pass_rate_delta * 100)}pp · 模型调用 {formatDelta(comparison.model_calls_delta)} · Token {formatDelta(comparison.total_tokens_delta)}</span></div>}
		<ol className={styles.evaluationCases}>{run.results.map((result) => <li key={result.case_id} data-passed={result.passed}><i /><span><strong>{result.case_id}</strong><small>{result.failure || `${result.status ?? "unknown"} · ${result.outcome ?? "unknown"} · ${result.assertions.filter((item) => item.passed).length}/${result.assertions.length} 断言`}</small></span><b>{result.passed ? "通过" : "失败"}</b></li>)}</ol>
		<footer>Run {run.id} · Agent {shortFingerprint(run.agent_fingerprint)} · Suite {shortFingerprint(run.suite_fingerprint)} · {new Date(run.started_at).toLocaleString("zh-CN")}</footer>
	</section>;
	}

function RolloutReport({ rollout, working, operator, onRefresh, onAbort }: {
  rollout: AgentRollout; working: string; operator: Operator;
  onRefresh: (rollout: AgentRollout) => Promise<void>; onAbort: (rollout: AgentRollout) => Promise<void>;
}) {
  const candidate = rollout.summary.candidate;
  const active = rollout.status === "running" || rollout.status === "ready";
  return <div className={styles.rolloutReport}>
    <div className={styles.rolloutHeadline}>
      <span><strong>v{rollout.agent_version} 候选</strong><small>基线 v{rollout.baseline_version} · {rollout.traffic_percent}% 流量 · {candidate.sample_size}/{rollout.policy.minimum_sample_size} 样本</small></span>
      <b data-status={rollout.status}>{rolloutStatusName(rollout)}</b>
    </div>
    <div className={styles.sandboxStats}>
      <div><small>错误率</small><strong>{formatRate(candidate.error_rate)}</strong></div>
      <div><small>质量失败</small><strong>{formatRate(candidate.quality_failure_rate)}</strong></div>
      <div><small>P95 延迟</small><strong>{Math.round(candidate.p95_latency_ms)} ms</strong></div>
      <div><small>平均成本</small><strong>{Math.round(candidate.average_cost_micros).toLocaleString()} μ</strong></div>
    </div>
    {rollout.violations.length > 0 && <ol className={styles.rolloutViolations}>{rollout.violations.map((item) => <li key={item.metric}><strong>{item.message}</strong><small>{item.metric} · 实际 {formatMetric(item.actual)} / 上限 {formatMetric(item.expected)}</small></li>)}</ol>}
    <div className={styles.rolloutActions}>
      <button type="button" disabled={!active || working !== "" || !canDryRun(operator.role)} onClick={() => void onRefresh(rollout)}>{working === `${rollout.id}-refresh` ? "计算中…" : "刷新线上门禁"}</button>
      <button type="button" disabled={!active || working !== "" || operator.role !== "admin" || !hasStepUp(operator)} onClick={() => void onAbort(rollout)}>{working === `${rollout.id}-abort` ? "停止中…" : "停止并回到基线"}</button>
    </div>
    <footer>Rollout {rollout.id} · {new Date(rollout.evaluated_at ?? rollout.created_at).toLocaleString("zh-CN")}</footer>
  </div>;
}

function defaultEvaluationSuite(payload: Record<string, unknown>) {
	const nodes = Array.isArray(payload.nodes) ? payload.nodes.filter(isRecord) : [];
	const edges = Array.isArray(payload.edges) ? payload.edges.filter(isRecord) : [];
	const modules = Array.isArray(payload.modules) ? payload.modules.filter((item): item is string => typeof item === "string") : [];
	const budget = isRecord(payload.budget) ? payload.budget : {};
	const maxModelCalls = safeInteger(budget.max_model_calls, 0);
	const maxSteps = Math.max(1, safeInteger(budget.max_steps, 1));
	const routes = Object.fromEntries(nodes.filter((node) => node.type === "router" && typeof node.key === "string").map((node) => {
		const edge = edges.find((item) => item.from === node.key && typeof item.condition === "string");
		return [node.key as string, { condition: typeof edge?.condition === "string" ? edge.condition : "" }];
	}));
	const models = Object.fromEntries(nodes.filter((node) => node.type === "model" && typeof node.key === "string").map((node) => [node.key as string, { response: `合成评测输出：${node.key as string} 已完成。`, prompt_tokens: 12, completion_tokens: 8 }]));
	const tools = Object.fromEntries(nodes.filter((node) => node.type === "tool" && typeof node.key === "string").map((node) => {
		const allowed = Array.isArray(node.tools) ? node.tools.filter((item): item is string => typeof item === "string") : [];
		return [node.key as string, { tool_name: allowed[0] ?? "", arguments: {}, response: `合成工具 ${allowed[0] ?? node.key as string} 已完成。`, requires_approval: false, risk_level: "none" }];
	}));
	return JSON.stringify({
		schema_version: "agent-eval-suite-v1",
		name: "studio-smoke",
		version: "v1",
		cases: modules.map((module) => ({
			schema_version: "agent-eval-case-v1",
			id: `${module}-synthetic-control-flow`,
			tags: ["synthetic", "release-gate", module],
			given: { module, user_message: `验证 ${module} 模块的声明式控制流`, context: { synthetic: { routes, models, tools } }, tools: [] },
			expect: { status: "completed", outcome: "completed", max_model_calls: maxModelCalls, max_steps: maxSteps, max_retries: 0, quality_pass: true },
			tape: {},
		})),
	}, null, 2);
}

async function studioFetch<T>(path: string, credentials: Credentials, options?: { method?: string; body?: unknown }): Promise<T> {
  const headers: Record<string, string> = { Authorization: `Bearer ${credentials.key}` };
  if (options?.body !== undefined) headers["Content-Type"] = "application/json";
  const response = await fetch(`${apiBase}${path}`, { method: options?.method ?? "GET", headers, body: options?.body === undefined ? undefined : JSON.stringify(options.body), cache: "no-store" });
  const payload = await response.json().catch(() => ({})) as { message?: string };
  if (!response.ok) throw new Error(payload.message || `请求失败（${response.status}）`);
  return payload as T;
}

function shortFingerprint(value: string) { return value ? `${value.slice(0, 8)}…${value.slice(-4)}` : "—"; }
function statusName(value: string) { return ({ draft: "草稿", validated: "已校验", submitted: "待审批", published: "已发布", superseded: "已替代" } as Record<string, string>)[value] ?? value; }
function actionName(value: "validate" | "submit" | "publish") { return ({ validate: "校验", submit: "提交", publish: "发布" } as const)[value]; }
function studioError(cause: unknown) { return cause instanceof Error ? cause.message : "Agent Studio 暂时不可用"; }
function firstModule(payload: Record<string, unknown>) { return Array.isArray(payload.modules) && typeof payload.modules[0] === "string" ? payload.modules[0] : "work"; }
function definitionModules(value: string) { try { const payload = JSON.parse(value) as Record<string, unknown>; return Array.isArray(payload.modules) ? payload.modules.filter((item): item is string => typeof item === "string") : []; } catch { return []; } }
function moduleName(value: string) { return ({ companion: "陪伴", life: "生活", work: "工作" } as Record<string, string>)[value] ?? value; }
function canDryRun(role: string) { return role === "support" || role === "admin"; }
function hasStepUp(operator: Operator) { return operator.mfa_verified || operator.legacy; }
function asNumber(value: unknown) { return typeof value === "number" && Number.isFinite(value) ? value : 0; }
function sandboxStatus(value: string) { return ({ completed: "已完成", waiting_approval: "待审批", waiting_tool: "等待工具" } as Record<string, string>)[value] ?? value; }
function friendlyNode(value = "") { return value.replace(/^studio__/, "").replace(/^__studio_/, "").replace(/__/g, " · ") || "节点"; }
function traceSummary(item: { status?: string; details?: Record<string, unknown> }) { const details = item.details ?? {}; const hint = details.tool_name ?? details.role ?? details.status ?? details.condition; return `${item.status ?? "unknown"}${hint ? ` · ${String(hint)}` : ""}`; }
function interruptName(value: { type?: string; tool_name?: string; task_id?: string }) { return value.type === "tool_approval" ? `工具审批 · ${value.tool_name ?? "未命名工具"}` : `异步工具 · ${value.task_id ?? "等待结果"}`; }
function isRecord(value: unknown): value is Record<string, unknown> { return typeof value === "object" && value !== null && !Array.isArray(value); }
function safeInteger(value: unknown, fallback: number) { return typeof value === "number" && Number.isInteger(value) && value >= 0 ? value : fallback; }
function formatDelta(value: number) { return `${value > 0 ? "+" : ""}${Math.round(value * 100) / 100}`; }
function formatRate(value: number) { return `${Math.round((value || 0) * 10000) / 100}%`; }
function formatMetric(value: number) { return Number.isFinite(value) ? String(Math.round(value * 10000) / 10000) : "—"; }
function rolloutStatusName(rollout: Pick<AgentRollout, "status" | "decision">) {
  if (rollout.status === "running" && rollout.decision === "collecting") return "采集中";
  return ({ ready: "门禁通过", paused: "已自动暂停", promoted: "已全量发布", aborted: "已停止", running: "灰度中" } as Record<string, string>)[rollout.status] ?? rollout.status;
}
function runtimeNodeMatches(value: string | undefined, key: string) { return value === key || value === `studio__${key}` || value?.endsWith(`__${key}`) === true; }
function safeJSON(value: unknown) { try { return JSON.stringify(value, null, 2); } catch { return "无法序列化"; } }
