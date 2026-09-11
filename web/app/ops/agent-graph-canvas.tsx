"use client";

import { useMemo, useState } from "react";

import styles from "./operations.module.css";

export type PromptReference = { key: string; version_id?: string; version?: number; fingerprint?: string };
export type StudioNode = {
  key: string;
  type: "router" | "model" | "tool" | "end";
  model_role?: string;
  prompt?: PromptReference;
  prompt_template?: string;
  tools?: string[];
};
export type StudioEdge = { from: string; to: string; condition?: string };
export type StudioDefinition = Record<string, unknown> & {
  entry_node: string;
  nodes: StudioNode[];
  edges: StudioEdge[];
};
export type PromptDeployment = {
  key: string;
  revision: number;
  version: {
    id: string;
    version: number;
    fingerprint: string;
    payload: { display_name?: string; description?: string; template?: string; variables?: string[] };
  };
};

type GraphCanvasProps = {
  definition: string;
  prompts: PromptDeployment[];
  selectedNode: string;
  visitedNodes?: Set<string>;
  onSelectNode: (key: string) => void;
  onDefinitionChange: (value: string) => void;
};

const nodeWidth = 174;
const nodeHeight = 76;

export function AgentGraphCanvas({ definition, prompts, selectedNode, visitedNodes, onSelectNode, onDefinitionChange }: GraphCanvasProps) {
  const parsed = useMemo(() => parseStudioDefinition(definition), [definition]);
  const [edgeFrom, setEdgeFrom] = useState("");
  const [edgeTo, setEdgeTo] = useState("");
  const [edgeCondition, setEdgeCondition] = useState("");
  const layout = useMemo(() => parsed ? layoutGraph(parsed) : null, [parsed]);
  const selected = parsed?.nodes.find((node) => node.key === selectedNode);

  function commit(update: (draft: StudioDefinition) => void) {
    if (!parsed) return;
    const draft = JSON.parse(JSON.stringify(parsed)) as StudioDefinition;
    update(draft);
    onDefinitionChange(JSON.stringify(draft, null, 2));
  }

  function updateNode(field: keyof StudioNode, value: unknown) {
    commit((draft) => {
      const node = draft.nodes.find((item) => item.key === selectedNode);
      if (!node) return;
      if (field === "key") {
        const next = String(value).toLowerCase().replace(/[^a-z0-9_-]/g, "").slice(0, 96);
        if (!next || draft.nodes.some((item) => item !== node && item.key === next)) return;
        const previous = node.key;
        node.key = next;
        draft.edges = draft.edges.map((edge) => ({ ...edge, from: edge.from === previous ? next : edge.from, to: edge.to === previous ? next : edge.to }));
        if (draft.entry_node === previous) draft.entry_node = next;
        onSelectNode(next);
        return;
      }
      if (field === "type") {
        const type = value as StudioNode["type"];
        node.type = type;
        if (type === "model" || type === "router") {
          node.model_role = type === "router" ? "router" : "responder";
          node.prompt_template = node.prompt_template || "在预算和安全约束内处理当前请求。";
          delete node.tools;
        } else {
          delete node.model_role;
          delete node.prompt;
          delete node.prompt_template;
          if (type === "tool") node.tools = node.tools?.length ? node.tools : ["tool_key"];
          else delete node.tools;
        }
        if (type === "end") draft.edges = draft.edges.filter((edge) => edge.from !== node.key);
        return;
      }
      if (value === undefined || value === "") delete node[field];
      else (node as Record<string, unknown>)[field] = value;
    });
  }

  function updatePrompt(value: string) {
    commit((draft) => {
      const node = draft.nodes.find((item) => item.key === selectedNode);
      if (!node) return;
      if (value === "inline") {
        delete node.prompt;
        node.prompt_template ||= "在预算和安全约束内处理当前请求。";
      } else if (value.startsWith("active:")) {
        node.prompt = { key: value.slice("active:".length) };
        delete node.prompt_template;
      }
    });
  }

  function addNode(type: StudioNode["type"]) {
    if (!parsed) return;
    let sequence = parsed.nodes.length + 1;
    let key = `${type}-${sequence}`;
    while (parsed.nodes.some((node) => node.key === key)) key = `${type}-${++sequence}`;
    commit((draft) => {
      const node: StudioNode = { key, type };
      if (type === "router" || type === "model") {
        node.model_role = type === "router" ? "router" : "responder";
        node.prompt_template = "在预算和安全约束内处理当前请求。";
      }
      if (type === "tool") node.tools = ["tool_key"];
      draft.nodes.push(node);
      if (!draft.entry_node) draft.entry_node = key;
    });
    onSelectNode(key);
  }

  function removeSelectedNode() {
    if (!parsed || !selected) return;
    commit((draft) => {
      draft.nodes = draft.nodes.filter((node) => node.key !== selected.key);
      draft.edges = draft.edges.filter((edge) => edge.from !== selected.key && edge.to !== selected.key);
      if (draft.entry_node === selected.key) draft.entry_node = draft.nodes[0]?.key ?? "";
    });
    onSelectNode(parsed.nodes.find((node) => node.key !== selected.key)?.key ?? "");
  }

  function addEdge() {
    if (!parsed || !edgeFrom || !edgeTo || edgeFrom === edgeTo) return;
    const source = parsed.nodes.find((node) => node.key === edgeFrom);
    const condition = source?.type === "router" ? edgeCondition.trim().toLowerCase().replace(/[^a-z0-9_-]/g, "") : "";
    if (source?.type === "router" && !condition) return;
    commit((draft) => {
      if (!draft.edges.some((edge) => edge.from === edgeFrom && edge.to === edgeTo && (edge.condition ?? "") === condition)) {
        draft.edges.push({ from: edgeFrom, to: edgeTo, ...(condition ? { condition } : {}) });
      }
    });
    setEdgeCondition("");
  }

  if (!parsed || !layout) {
    return <div className={styles.graphParseError}><strong>画布暂时无法读取 DSL</strong><span>切换到源码视图修正 JSON 后，画布会自动恢复。</span></div>;
  }

  const promptValue = selected?.prompt?.version_id ? `pinned:${selected.prompt.version_id}` : selected?.prompt?.key ? `active:${selected.prompt.key}` : "inline";
  return <section className={styles.graphWorkspace}>
    <div className={styles.graphToolbar}>
      <span>添加节点</span>
      {(["router", "model", "tool", "end"] as StudioNode["type"][]).map((type) => <button key={type} type="button" onClick={() => addNode(type)}>+ {nodeTypeName(type)}</button>)}
      <em>{parsed.nodes.length} 节点 · {parsed.edges.length} 连线</em>
    </div>
    <div className={styles.graphCanvasViewport}>
      <div className={styles.graphCanvas} style={{ width: layout.width, height: layout.height }}>
        <svg width={layout.width} height={layout.height} aria-hidden="true">
          <defs><marker id="studio-arrow" markerWidth="8" markerHeight="8" refX="7" refY="4" orient="auto"><path d="M0,0 L8,4 L0,8 Z" /></marker></defs>
          {parsed.edges.map((edge, index) => {
            const source = layout.positions.get(edge.from);
            const target = layout.positions.get(edge.to);
            if (!source || !target) return null;
            const x1 = source.x + nodeWidth;
            const y1 = source.y + nodeHeight / 2;
            const x2 = target.x;
            const y2 = target.y + nodeHeight / 2;
            const bend = Math.max(36, (x2 - x1) / 2);
            return <g key={`${edge.from}-${edge.to}-${edge.condition ?? ""}-${index}`}>
              <path className={styles.graphEdge} d={`M ${x1} ${y1} C ${x1 + bend} ${y1}, ${x2 - bend} ${y2}, ${x2} ${y2}`} markerEnd="url(#studio-arrow)" />
              {edge.condition && <text className={styles.graphEdgeLabel} x={(x1 + x2) / 2} y={(y1 + y2) / 2 - 7}>{edge.condition}</text>}
            </g>;
          })}
        </svg>
        {parsed.nodes.map((node) => {
          const point = layout.positions.get(node.key)!;
          const prompt = node.prompt ? prompts.find((item) => item.key === node.prompt?.key) : undefined;
          return <button
            type="button"
            key={node.key}
            className={styles.graphNode}
            data-type={node.type}
            data-selected={node.key === selectedNode}
            data-visited={visitedNodes?.has(node.key) ?? false}
            style={{ left: point.x, top: point.y, width: nodeWidth, height: nodeHeight }}
            onClick={() => onSelectNode(node.key)}
          >
            <span>{node.type === "end" ? "终点" : nodeTypeName(node.type)}</span>
            <strong>{node.key}</strong>
            <small>{node.prompt ? `Prompt · ${prompt?.version.version ?? node.prompt.version ?? "当前"}` : node.model_role || (node.tools?.join(", ") ?? "完成")}</small>
          </button>;
        })}
      </div>
    </div>
    <div className={styles.graphLowerGrid}>
      <section className={styles.nodeInspector}>
        <div className={styles.subheading}><div><p>NODE INSPECTOR</p><h3>{selected ? `节点 · ${selected.key}` : "选择一个节点"}</h3></div>{selected && <button type="button" onClick={removeSelectedNode}>删除节点</button>}</div>
        {selected ? <div className={styles.nodeFields}>
          <label>节点 Key<input value={selected.key} onChange={(event) => updateNode("key", event.target.value)} /></label>
          <label>节点类型<select value={selected.type} onChange={(event) => updateNode("type", event.target.value)}><option value="router">路由</option><option value="model">模型</option><option value="tool">工具</option><option value="end">终点</option></select></label>
          {(selected.type === "router" || selected.type === "model") && <>
            <label>模型角色<select value={selected.model_role ?? ""} onChange={(event) => updateNode("model_role", event.target.value)}>{["planner", "router", "composer", "assessor", "responder", "companion_responder", "repairer"].map((role) => <option key={role}>{role}</option>)}</select></label>
            <label>Prompt 来源<select value={promptValue} onChange={(event) => updatePrompt(event.target.value)}>
              <option value="inline">节点内联 Prompt</option>
              {selected.prompt?.version_id && <option value={`pinned:${selected.prompt.version_id}`}>已锁定 · {selected.prompt.key} v{selected.prompt.version}</option>}
              {prompts.map((item) => <option key={item.key} value={`active:${item.key}`}>绑定当前发布 · {item.version.payload.display_name ?? item.key} v{item.version.version}</option>)}
            </select></label>
            {!selected.prompt && <label className={styles.fullField}>Prompt 模板<textarea value={selected.prompt_template ?? ""} onChange={(event) => updateNode("prompt_template", event.target.value)} /></label>}
            {selected.prompt && <div className={styles.promptBinding}><strong>{selected.prompt.key} · {selected.prompt.version ? `锁定 v${selected.prompt.version}` : "保存时锁定当前发布版"}</strong><span>{selected.prompt.fingerprint ? `指纹 ${shortFingerprint(selected.prompt.fingerprint)}` : "服务端会解析版本 ID、指纹和模板快照"}</span></div>}
          </>}
          {selected.type === "tool" && <label className={styles.fullField}>允许的工具（逗号分隔）<input value={(selected.tools ?? []).join(", ")} onChange={(event) => updateNode("tools", event.target.value.split(",").map((item) => item.trim().toLowerCase()).filter(Boolean))} /></label>}
          <label className={styles.entryToggle}><input type="checkbox" checked={parsed.entry_node === selected.key} onChange={() => commit((draft) => { draft.entry_node = selected.key; })} />设为入口节点</label>
        </div> : <div className={styles.emptyLine}>点击画布节点后，可编辑类型、模型角色、Prompt 和工具白名单。</div>}
      </section>
      <section className={styles.edgeEditor}>
        <div className={styles.subheading}><div><p>EDGE ROUTING</p><h3>连线与条件</h3></div><span>{parsed.edges.length}</span></div>
        <div className={styles.edgeComposer}>
          <select value={edgeFrom} onChange={(event) => setEdgeFrom(event.target.value)}><option value="">起点</option>{parsed.nodes.filter((node) => node.type !== "end").map((node) => <option key={node.key}>{node.key}</option>)}</select>
          <select value={edgeTo} onChange={(event) => setEdgeTo(event.target.value)}><option value="">终点</option>{parsed.nodes.map((node) => <option key={node.key}>{node.key}</option>)}</select>
          <input value={edgeCondition} onChange={(event) => setEdgeCondition(event.target.value)} placeholder="路由条件（仅 router）" />
          <button type="button" onClick={addEdge}>添加连线</button>
        </div>
        <ol className={styles.edgeList}>{parsed.edges.map((edge, index) => <li key={`${edge.from}-${edge.to}-${index}`}><span><strong>{edge.from} → {edge.to}</strong><small>{edge.condition || "默认流转"}</small></span><button type="button" aria-label={`删除 ${edge.from} 到 ${edge.to} 的连线`} onClick={() => commit((draft) => { draft.edges.splice(index, 1); })}>×</button></li>)}</ol>
      </section>
    </div>
  </section>;
}

export function parseStudioDefinition(value: string): StudioDefinition | null {
  try {
    const parsed = JSON.parse(value) as Record<string, unknown>;
    if (!Array.isArray(parsed.nodes) || !Array.isArray(parsed.edges) || typeof parsed.entry_node !== "string") return null;
    if (!parsed.nodes.every((node) => isStudioNode(node)) || !parsed.edges.every((edge) => isStudioEdge(edge))) return null;
    return parsed as StudioDefinition;
  } catch {
    return null;
  }
}

function isStudioNode(value: unknown): value is StudioNode {
  if (!isRecord(value)) return false;
  return typeof value.key === "string" && ["router", "model", "tool", "end"].includes(String(value.type));
}

function isStudioEdge(value: unknown): value is StudioEdge {
  return isRecord(value) && typeof value.from === "string" && typeof value.to === "string";
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function layoutGraph(definition: StudioDefinition) {
  const outgoing = new Map<string, string[]>();
  for (const edge of definition.edges) outgoing.set(edge.from, [...(outgoing.get(edge.from) ?? []), edge.to]);
  const depth = new Map<string, number>();
  const queue = definition.nodes.some((node) => node.key === definition.entry_node) ? [definition.entry_node] : [];
  if (queue[0]) depth.set(queue[0], 0);
  while (queue.length) {
    const key = queue.shift()!;
    for (const target of outgoing.get(key) ?? []) {
      if (!depth.has(target)) {
        depth.set(target, (depth.get(key) ?? 0) + 1);
        queue.push(target);
      }
    }
  }
  const reachableMax = Math.max(0, ...depth.values());
  for (const node of definition.nodes) if (!depth.has(node.key)) depth.set(node.key, reachableMax + 1);
  const groups = new Map<number, StudioNode[]>();
  for (const node of definition.nodes) groups.set(depth.get(node.key) ?? 0, [...(groups.get(depth.get(node.key) ?? 0) ?? []), node]);
  const maxRows = Math.max(1, ...Array.from(groups.values(), (items) => items.length));
  const width = Math.max(720, (Math.max(...depth.values()) + 1) * 250 + 54);
  const height = Math.max(310, maxRows * 118 + 70);
  const positions = new Map<string, { x: number; y: number }>();
  for (const [column, nodes] of groups) {
    const blockHeight = nodes.length * nodeHeight + Math.max(0, nodes.length - 1) * 42;
    const startY = Math.max(34, (height - blockHeight) / 2);
    nodes.forEach((node, row) => positions.set(node.key, { x: 34 + column * 250, y: startY + row * 118 }));
  }
  return { width, height, positions };
}

function nodeTypeName(value: StudioNode["type"]) {
  return ({ router: "路由", model: "模型", tool: "工具", end: "终点" } as const)[value];
}

function shortFingerprint(value: string) {
  return value ? `${value.slice(0, 8)}…${value.slice(-4)}` : "—";
}
