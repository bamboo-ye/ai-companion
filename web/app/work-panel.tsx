"use client";

import { FormEvent, useCallback, useEffect, useState } from "react";

import type { SkillRun } from "./skill-run-client";

const apiBase = process.env.NEXT_PUBLIC_API_BASE_URL ?? "http://localhost:8080";

type SkillManifest = {
  name: string; version: string; display_name: string; description: string; risk_level: string;
  requires_confirmation: boolean; enabled: boolean;
};
type IntentRoute = {
  intent: string; confidence: number; required_slots: string[]; risk_level: string; suggested_skill?: string;
  slots: Record<string, unknown>; layer: string; version: string; reason: string;
};
type MCPServerSummary = { name: string; enabled: boolean; allowed_tools: string[]; protocol_version: string; transport: string };

export function WorkPanel({ token, onClose }: { token: string; onClose: () => void }) {
  const [skills, setSkills] = useState<SkillManifest[]>([]);
  const [route, setRoute] = useState<IntentRoute | null>(null);
  const [mcpServers, setMcpServers] = useState<MCPServerSummary[]>([]);
  const [notice, setNotice] = useState("");
  const [busy, setBusy] = useState("");

  const request = useCallback(async <T,>(path: string, init?: RequestInit): Promise<T> => {
    const response = await fetch(`${apiBase}${path}`, {
      ...init,
      headers: { "Content-Type": "application/json", Authorization: `Bearer ${token}`, ...init?.headers },
    });
    const payload = await response.json() as T & { message?: string };
    if (!response.ok) throw new Error(payload.message ?? "工作任务操作失败");
    return payload;
  }, [token]);

  const refresh = useCallback(async () => {
    const [skillPayload, mcpPayload] = await Promise.all([
      request<{ items: SkillManifest[] }>("/v1/skills"),
      request<{ items: MCPServerSummary[] }>("/v1/mcp/servers"),
    ]);
    setSkills(skillPayload.items); setMcpServers(mcpPayload.items);
  }, [request]);

  useEffect(() => {
    queueMicrotask(() => { void refresh().catch((error: Error) => setNotice(error.message)); });
  }, [refresh]);

  async function startSkill(name: string, input: Record<string, unknown>) {
    setBusy(name); setNotice("");
    try {
      const payload = await request<{ run: SkillRun }>(`/v1/skills/${name}/runs`, {
        method: "POST", headers: { "Idempotency-Key": crypto.randomUUID() }, body: JSON.stringify({ input }),
      });
      setNotice(payload.run.status === "waiting_confirmation" ? "任务已生成预览，确认后才会创建文件。" : payload.run.status === "queued" ? "任务已进入持久队列，可安全等待 Worker 执行。" : "任务已完成。");
      await refresh();
    } catch (error) { setNotice(error instanceof Error ? error.message : "任务创建失败"); }
    finally { setBusy(""); }
  }

  async function toggleSkill(item: SkillManifest) {
    setBusy(item.name); setNotice("");
    try {
      await request(`/v1/skills/${item.name}/settings`, { method: "PUT", body: JSON.stringify({ enabled: !item.enabled }) });
      setNotice(item.enabled ? `已停用 ${item.display_name}。` : `已启用 ${item.display_name}。`);
      await refresh();
    } catch (error) { setNotice(error instanceof Error ? error.message : "Skill 设置失败"); }
    finally { setBusy(""); }
  }

  function skillEnabled(name: string) { return skills.find((item) => item.name === name)?.enabled ?? false; }

  function translate(event: FormEvent<HTMLFormElement>) {
    event.preventDefault(); const data = new FormData(event.currentTarget);
    void startSkill("office.translate", { text: data.get("text"), target_language: data.get("target"), tone: data.get("tone") });
  }

  async function suggest(event: FormEvent<HTMLFormElement>) {
    event.preventDefault(); const data = new FormData(event.currentTarget); setBusy("intent-route"); setNotice("");
    try {
      const result = await request<IntentRoute>("/v1/intent/route", { method: "POST", body: JSON.stringify({ text: data.get("text"), page: "work" }) });
      setRoute(result); setNotice(result.suggested_skill ? "已给出 Skill 建议，不会自动执行。" : "需要补充信息后再选择能力。");
    } catch (error) { setNotice(error instanceof Error ? error.message : "意图识别失败"); }
    finally { setBusy(""); }
  }

  function createDocument(event: FormEvent<HTMLFormElement>) {
    event.preventDefault(); const data = new FormData(event.currentTarget);
    void startSkill("office.markdown_document", { title: data.get("title"), content: data.get("content"), filename: data.get("filename") });
  }

  function editDocx(event: FormEvent<HTMLFormElement>) {
    event.preventDefault(); const data = new FormData(event.currentTarget);
    void withSourceFile(data, "source", [".docx"], async (source) => {
      await startSkill("office.docx_edit", { ...source, append_text: data.get("append_text"), output_filename: data.get("output_filename") });
    });
  }

  function generatePptx(event: FormEvent<HTMLFormElement>) {
    event.preventDefault(); const data = new FormData(event.currentTarget);
    const action = ((event.nativeEvent as SubmitEvent).submitter as HTMLButtonElement | null)?.value;
    void startSkill(action === "outline" ? "office.pptx_outline" : "office.pptx_generate", {
      title: data.get("title"), audience: data.get("audience"), style: data.get("style"), brief: data.get("brief"),
      slide_count: Number(data.get("slide_count")), filename: data.get("filename"),
    });
  }

  function profileTable(event: FormEvent<HTMLFormElement>) {
    event.preventDefault(); const data = new FormData(event.currentTarget);
    void withSourceFile(data, "source", [".csv", ".xlsx"], async (source) => {
      await startSkill("office.tabular_profile", source);
    });
  }

  async function withSourceFile(data: FormData, field: string, extensions: string[], action: (source: { source_filename: string; source_base64: string }) => Promise<void>) {
    try {
      const file = data.get(field);
      if (!(file instanceof File) || file.size === 0) throw new Error("请选择源文件");
      const lowerName = file.name.toLowerCase();
      if (!extensions.some((extension) => lowerName.endsWith(extension))) throw new Error(`仅支持 ${extensions.join(" / ")} 文件`);
      if (file.size > 700 * 1024) throw new Error("首版单文件不能超过 700KB");
      await action({ source_filename: file.name, source_base64: await fileToBase64(file) });
    } catch (error) { setNotice(error instanceof Error ? error.message : "文件读取失败"); }
  }

  return <section className="workPanel" aria-labelledby="work-title">
    <header><div><h3 id="work-title">工作台</h3><small>创建任务、配置 Skill 与生成文件</small></div><button className="textButton" type="button" onClick={onClose}>返回</button></header>
    {notice && <p className="workNotice" role="status">{notice}</p>}
    <div className="skillCatalog" aria-label="Skill 启停设置">
      {skills.map((item) => <span key={item.name} className={item.enabled ? "" : "disabled"}><strong>{item.display_name}</strong><small>v{item.version} · {item.enabled ? riskLabel(item) : "已停用"}</small><button type="button" disabled={busy !== ""} onClick={() => toggleSkill(item)}>{item.enabled ? "停用" : "启用"}</button></span>)}
    </div>
    <section className="intentAdvisor" aria-labelledby="intent-title"><div><h4 id="intent-title">我该用哪个 Skill？</h4><small>规则路由只给建议，不会自动执行工具。</small></div><form className="workForm" onSubmit={suggest}><input name="text" required maxLength={4000} placeholder="例如：给管理层生成 8 页季度复盘 PPT"/><button disabled={busy !== ""}>分析任务</button></form>
      {route && <div className="intentResult"><strong>{route.suggested_skill ? skillName(route.suggested_skill) : intentName(route.intent)}</strong><span>置信度 {Math.round(route.confidence * 100)}% · 风险 {riskName(route.risk_level)} · {route.layer}</span><small>{route.reason}</small>{route.required_slots.length > 0 && <small>还需要：{route.required_slots.join("、")}</small>}</div>}
    </section>
    <div className="mcpSummary" aria-label="MCP 白名单状态"><strong>MCP 2025-11-25 · stdio 双白名单</strong>{mcpServers.length === 0 ? <small>未配置外部服务器（默认安全状态）</small> : mcpServers.map((server) => <small key={server.name}>{server.name}：{server.allowed_tools.join("、")}</small>)}</div>
    <div className="workGrid">
      <article className="workSkill"><h4>翻译预览</h4><p>保留原文，对照输出；无外部副作用。</p><form className="workForm" onSubmit={translate}><textarea name="text" required placeholder="输入要翻译的内容"/><div><input name="target" required defaultValue="English" aria-label="目标语言"/><select name="tone" defaultValue="neutral" aria-label="翻译语气"><option value="neutral">中性</option><option value="formal">正式</option></select></div><button disabled={busy !== "" || !skillEnabled("office.translate")}>开始翻译</button></form></article>
      <article className="workSkill"><h4>邮件草稿</h4><p>邮件生成已统一进入工作伙伴聊天的质量工作流：自动识别语言，补齐称呼、自我介绍、礼貌请求和署名，消除重复段落，并在不合格时限次重写。</p><button type="button" disabled={busy !== "" || !skillEnabled("office.email_draft")} onClick={onClose}>返回工作伙伴</button></article>
      <article className="workSkill"><h4>Markdown 文档</h4><p>先预览确认，再创建新副本；不会覆盖源文件。</p><form className="workForm" onSubmit={createDocument}><input name="title" required placeholder="文档标题"/><input name="filename" placeholder="文件名（可选）"/><textarea name="content" required placeholder="文档内容"/><button disabled={busy !== "" || !skillEnabled("office.markdown_document")}>生成待确认任务</button></form></article>
      <article className="workSkill"><h4>DOCX 副本编辑</h4><p>读取小型 DOCX，确认后在新副本末尾追加段落。</p><form className="workForm" onSubmit={editDocx}><input name="source" type="file" accept=".docx" required aria-label="DOCX 源文件"/><input name="output_filename" placeholder="输出文件名（可选）"/><textarea name="append_text" required placeholder="要追加的段落，每行一个段落"/><button disabled={busy !== "" || !skillEnabled("office.docx_edit")}>生成待确认编辑</button></form></article>
      <article className="workSkill"><h4>PPTX 大纲/生成</h4><p>可先预览逐页大纲；受众、页数和风格填写完整后直接生成新演示。</p><form className="workForm" onSubmit={generatePptx}><input name="title" required placeholder="演示标题"/><input name="audience" required placeholder="目标受众"/><div><input name="slide_count" type="number" min="3" max="20" defaultValue="6" required aria-label="幻灯片页数"/><input name="style" defaultValue="简洁专业" required aria-label="演示风格"/></div><input name="filename" placeholder="输出文件名（可选）"/><textarea name="brief" required placeholder="内容简报；可按行填写章节"/><div className="workFormActions"><button name="action" value="outline" disabled={busy !== "" || !skillEnabled("office.pptx_outline")}>预览大纲</button><button name="action" value="generate" disabled={busy !== "" || !skillEnabled("office.pptx_generate")}>直接生成演示</button></div></form></article>
      <article className="workSkill"><h4>CSV/XLSX 分析</h4><p>识别表头、类型、缺失值、重复行和数值摘要。</p><form className="workForm" onSubmit={profileTable}><input name="source" type="file" accept=".csv,.xlsx" required aria-label="CSV 或 XLSX 源文件"/><small>UTF-8 CSV 或无宏 XLSX，最大 700KB，最多分析 10,000 行。</small><button disabled={busy !== "" || !skillEnabled("office.tabular_profile")}>开始分析</button></form></article>
    </div>
  </section>;
}

function riskLabel(skill: SkillManifest) { return skill.requires_confirmation ? "执行前确认" : skill.risk_level === "none" ? "无副作用" : skill.risk_level; }
async function fileToBase64(file: File): Promise<string> {
  const dataUrl = await new Promise<string>((resolve, reject) => {
    const reader = new FileReader(); reader.onerror = () => reject(new Error("文件读取失败"));
    reader.onload = () => resolve(String(reader.result)); reader.readAsDataURL(file);
  });
  return dataUrl.slice(dataUrl.indexOf(",") + 1);
}

function skillName(name: string) { return ({
  "office.translate": "翻译", "office.email_draft": "邮件草稿", "office.markdown_document": "Markdown 文档",
  "office.docx_edit": "DOCX 副本编辑", "office.pptx_outline": "PPTX 大纲", "office.pptx_generate": "PPTX 生成", "office.tabular_profile": "CSV/XLSX 分析",
} as Record<string, string>)[name] ?? name; }
function intentName(intent: string) { return ({ casual_chat: "情感陪伴", ledger: "记账", reminder: "提醒", document_qa: "文档问答", office: "工作伙伴", finance: "金融研究", image: "图片创作", unknown: "需要澄清" } as Record<string, string>)[intent] ?? intent; }
function riskName(risk: string) { return ({ none: "无", low: "低", medium: "中", high: "高" } as Record<string, string>)[risk] ?? risk; }
