"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import type { ChangeEvent, FormEvent } from "react";
import Image from "next/image";
import { createPortal } from "react-dom";
import { listSkillRuns } from "./skill-run-client";
import { PresentationLibrary } from "./presentation-library";
import { PresentationDownload } from "./presentation-download";
import { loadPresentationDraft, savePresentationDraft } from "./presentation-draft";
import type { SkillRun } from "./skill-run-client";
import { PresentationCanvas } from "./presentation-canvas";
import { acceptDesign, commit, editItems, isDesignOutput, insertSlide, instantiateTemplate, moveSlide, removeSlide, renameDeck, parseChart, parseDeck, parseTable, parseTheme, redo, replaceSlideImage, splitSlide, undo, updateSlide } from "./presentation-model";
import type { Deck, DesignOutput, DesignSlide, History, Motion, Override, ThemeID } from "./presentation-model";
import "./presentation-studio.css";

const apiBase = process.env.NEXT_PUBLIC_API_BASE_URL ?? "http://localhost:8080";
const themes: { id: ThemeID; title: string; description: string; colors: string[] }[] = [
  { id: "companion", title: "伴AI", description: "暖白粉紫 · 温暖陪伴", colors: ["#FFFAF3", "#553E3A", "#A35082"] },
  { id: "business", title: "远山", description: "清晰有序 · 商务汇报", colors: ["#F6F7FB", "#172B4D", "#305BD6"] },
  { id: "technology", title: "深空", description: "深色舞台 · 科技产品", colors: ["#111827", "#F3F7FF", "#72DCD1"] },
  { id: "editorial", title: "纸间", description: "温润纸感 · 教学人文", colors: ["#FAF6EE", "#302D27", "#A64C33"] },
];
const example = "## 活动目标\n- 面向社区居民提供固定的阅读交流时间\n- 通过共读和分享连接不同年龄的读者\n## 活动构成\n### 共读空间\n- 每周安排一次线下共读\n- 提供安静阅读区域\n### 交流活动\n- 围绕当周书目交流\n- 记录参与者的推荐书单\n### 社区支持\n- 志愿者负责场地与签到\n- 通过社区公告发布活动\n## 参与流程\n- 查看当周活动与书目\n- 选择场次并报名\n- 参加共读与交流\n- 留下反馈与推荐\n## 组织方式对比\n- 固定场次：便于形成稳定习惯，需要持续维护场地\n- 主题专场：便于吸引特定读者，需要提前准备书目\n## 推进计划\n- 准备期：确定场地、书目与志愿者分工\n- 试办期：开展首次活动并收集反馈\n- 调整期：根据反馈确定后续场次\n## 待确认事项\n- 确定首次活动的时间与地点\n- 明确志愿者排班和报名人数上限";

async function skillRequest(token: string, name: string, input: Record<string, unknown>, signal: AbortSignal, onStatus: (s: string) => void): Promise<SkillRun> {
  const request = async (path: string, init?: RequestInit) => {
    const response = await fetch(`${apiBase}${path}`, { ...init, signal, headers: { "Content-Type": "application/json", Authorization: `Bearer ${token}`, ...init?.headers } });
    const value = await response.json();
    if (!response.ok) throw new Error(value.message ?? "演示任务失败");
    return value;
  };
  const started = await request(`/v1/skills/${name}/runs`, { method: "POST", headers: { "Idempotency-Key": crypto.randomUUID() }, body: JSON.stringify({ input }) });
  let run = started.run as SkillRun;
  const deadline = Date.now() + 360_000;
  while (run.status === "queued" || run.status === "running") {
    if (Date.now() > deadline) throw new Error("任务仍在后台执行，可从任务记录恢复结果");
    onStatus(run.status === "queued" ? "任务排队中…" : name === "office.pptx_generate" ? "正在导出并逐页渲染验证…" : "正在计算版式与文字容量…");
    await new Promise<void>((resolve, reject) => {
      const abort = () => { clearTimeout(timer); reject(new DOMException("Aborted", "AbortError")); };
      const timer = setTimeout(() => { signal.removeEventListener("abort", abort); resolve(); }, 1000);
      signal.addEventListener("abort", abort, { once: true });
    });
    run = await request(`/v1/skill-runs/${run.id}`) as SkillRun;
  }
  if (run.status !== "succeeded") throw new Error(run.error_message ?? `任务状态：${run.status}`);
  return run;
}

function downloadJSON(name: string, value: unknown) {
  const url = URL.createObjectURL(new Blob([JSON.stringify(value, null, 2)], { type: "application/json" }));
  const a = document.createElement("a"); a.href = url; a.download = name;
  document.body.appendChild(a); a.click(); a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 30_000);
}

function fileBase64(file: File): Promise<string> {
  return new Promise((resolve, reject) => { const reader = new FileReader(); reader.onload = () => resolve(String(reader.result).split(',')[1]); reader.onerror = reject; reader.readAsDataURL(file); });
}

export function PresentationStudio({ token, userID, enabled, onClose }: { token: string; userID: string; enabled: boolean; onClose: () => void }) {
  const [history, setHistory] = useState<History | null>(null);
  const [output, setOutput] = useState<DesignOutput | null>(null);
  const [selected, setSelected] = useState("");
  const [selectedNode, setSelectedNode] = useState("");
  const [theme, setTheme] = useState<ThemeID>("business");
  const [brief, setBrief] = useState("");
  const [stylePrompt, setStylePrompt] = useState("");
  const [projectName, setProjectName] = useState("伴AI");
  const [busy, setBusy] = useState(false);
  const [status, setStatus] = useState("");
  const [error, setError] = useState("");
  const [run, setRun] = useState<SkillRun | null>(null);
  const [recent, setRecent] = useState<SkillRun[]>([]);
  const [playing, setPlaying] = useState(false);
  const [libraryMode, setLibraryMode] = useState<"templates" | "pages" | null>(null);
  const [revealed, setRevealed] = useState(0);
  const [actual, setActual] = useState(false);
  const [chartText, setChartText] = useState("");
  const [chartUnit, setChartUnit] = useState("");
  const [chartSeries, setChartSeries] = useState("数值");
  const [tableText, setTableText] = useState("");
  const [localDraft, setLocalDraft] = useState(false);
  const [draftNotice, setDraftNotice] = useState("");
  const [chatEdit, setChatEdit] = useState("");
  const requestRef = useRef<AbortController | null>(null);
  const initialInput = useRef<Record<string, unknown>>({});
  const sourceInput = useRef<Record<string, unknown>>({});
  const draftKey = `ai-companion-presentation-v2:${userID}`;
  const deck = history?.present;
  const index = deck ? Math.max(0, deck.slides.findIndex(s => s.id === selected)) : -1;
  const slide = deck?.slides[index];
  const scene = output?.scenes.find(s => s.id === slide?.id);
  const node = scene?.nodes.find(n => n.id === selectedNode);
  const dirty = !!deck && JSON.stringify(deck) !== JSON.stringify(output?.deck_spec);
  const groupCount = scene ? Math.max(0, ...scene.nodes.map(n => n.group ?? 0)) : 0;

  useEffect(() => () => requestRef.current?.abort(), []);
  useEffect(() => {
    let active = true;
    void (userID ? loadPresentationDraft(draftKey) : Promise.resolve(null)).then(saved => { if (active) setLocalDraft(!!saved); });
    void listSkillRuns(token, { limit: 30 }).then(items => setRecent(items.filter(r => isDesignOutput(r.output)).slice(0, 12))).catch(() => {});
    return () => { active = false; };
  }, [draftKey, token, userID]);
  useEffect(() => {
    if (!deck || !userID) return;
    let active = true;
    const timer = setTimeout(() => {
      void savePresentationDraft(draftKey, { deck, input: initialInput.current }).then(() => {
        if (active) { setLocalDraft(true); setDraftNotice("草稿已自动保存在此浏览器"); }
      }).catch(() => { if (active) setDraftNotice("草稿未保存：浏览器存储不可用或空间不足，请保存版本或下载设计稿"); });
    }, 700);
    return () => { active = false; clearTimeout(timer); };
  }, [deck, draftKey, userID]);

  function change(next: Deck) {
    if (busy) return;
    setHistory(previous => previous ? commit(previous, next) : { past: [], present: next, future: [] });
    setActual(false); setRun(null); setError("");
  }
  function patchSlide(patch: Partial<DesignSlide>) {
    if (deck && slide) change(updateSlide(deck, slide.id, patch));
  }

  function restoreHistory(next: History) {
    const nextSlide = next.present.slides.find(s => s.id === selected) ?? next.present.slides[Math.min(Math.max(index, 0), next.present.slides.length - 1)];
    setHistory(next); setSelected(nextSlide.id); setSelectedNode(""); setRevealed(0);
    syncDataFields(nextSlide); setActual(false); setRun(null); setError("");
  }

  async function generate(input: Record<string, unknown>, exporting = false, preserveHistory = false) {
    if (busy) return;
    setBusy(true); setError(""); setStatus(exporting ? "正在生成可编辑文件…" : "正在设计页面…");
    const controller = new AbortController(); requestRef.current = controller;
    try {
      const result = await skillRequest(token, exporting ? "office.pptx_generate" : "office.pptx_outline", { ...input, design_version: "2" }, controller.signal, setStatus);
      if (result.output?.operation_error) throw new Error(String((result.output.operation_error as {code?: string}).code ?? "风格设计失败，请重试"));
      if (!isDesignOutput(result.output)) throw new Error("服务器尚未启用新版演示工作台，请更新文档 Worker");
      const design = result.output;
      setOutput(design);
      setHistory(previous => preserveHistory && previous ? acceptDesign(previous, design.deck_spec) : { past: [], present: design.deck_spec, future: [] });
      const nextSlide = design.deck_spec.slides.find(s => s.id === selected) ?? design.deck_spec.slides[0];
      setSelected(nextSlide.id); syncDataFields(nextSlide);
      setRun(result); setActual(exporting && design.rendered_previews.length > 0);
      setStatus(design.quality_report.passed ? exporting ? "文件已生成，可下载并继续编辑" : `已设计 ${design.deck_spec.slides.length} 页；修改会自动保存在此浏览器` : "发现排版问题，请修正标记页面后导出");
      setRecent(previous => [result, ...previous.filter(r => r.id !== result.id)].slice(0, 12));
    } catch (cause) {
      if (!(cause instanceof DOMException && cause.name === "AbortError")) { setStatus(""); setError(cause instanceof Error ? cause.message : "演示任务失败"); }
    } finally { if (!controller.signal.aborted) setBusy(false); }
  }

  async function createFromTemplate(template: Deck) {
    setLibraryMode(null); initialInput.current = {}; sourceInput.current = {};
    const next = instantiateTemplate(template, `deck-${crypto.randomUUID()}`);
    await generate(currentInput(next));
  }
  function addPage(preset: DesignSlide) {
    if (!deck || !slide || busy) return;
    try {
      const id = `slide-${crypto.randomUUID()}`;
      const next = insertSlide(deck, slide.id, preset, id);
      change(next); setSelected(id); setSelectedNode(""); syncDataFields(next.slides.find(s => s.id === id));
      setLibraryMode(null); setStatus("已添加页面，编辑内容后应用修改查看效果");
    } catch (cause) { setError(cause instanceof Error ? cause.message : "添加页面失败"); setLibraryMode(null); }
  }

  async function create(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const data = new FormData(event.currentTarget);
    initialInput.current = { title: data.get("title"), audience: data.get("audience"), slide_count: Number(data.get("slide_count")), style: themes.find(t => t.id === theme)?.description, theme_id: theme, brief, style_prompt: stylePrompt.trim() || undefined, presentation_mode: data.get("mode"), animation: data.get("mode") === "teach" ? "explain" : "subtle", visual_mode: "none", ...sourceInput.current };
    await generate(initialInput.current);
  }
  function currentInput(value: Deck): Record<string, unknown> {
    return { ...initialInput.current, style_prompt: undefined, title: value.title, audience: value.audience || "演示听众", style: value.theme_id, brief: String(initialInput.current.brief || "编辑设计稿"), slide_count: value.slides.length, deck_spec: value };
  }
  async function importDocument(event: ChangeEvent<HTMLInputElement>) {
    const file = event.target.files?.[0]; event.target.value = "";
    if (!file) return;
    try {
      if (file.size > 22_000_000) throw new Error("文件不能超过 22MB");
      if (file.name.endsWith(".json")) {
        const imported = parseDeck(await file.text());
        initialInput.current = {};
        await generate(currentInput(imported));
      } else if (/\.(txt|md)$/i.test(file.name)) {
        const text = await file.text();
        if (text.length > 100000) throw new Error("文本不能超过 100,000 字符");
        sourceInput.current = {}; setBrief(text); setStatus("已导入文本，可调整内容后生成");
      } else if (/\.pdf$/i.test(file.name)) {
        if (file.size > 15_000_000) throw new Error("PDF 不能超过 15MB");
        setBusy(true); setStatus("正在提取 PDF 正文与页码…");
        const controller = new AbortController(); requestRef.current = controller;
        try {
          const encoded = await fileBase64(file), chunks: string[] = [];
          let round = 1;
          while (true) {
            const result = await skillRequest(token, "office.document_extract", { source_filename: file.name, source_base64: encoded, media_type: "application/pdf", round_start: round }, controller.signal, () => setStatus("正在提取 PDF 正文与页码…"));
            const extracted = result.output as Record<string, unknown>;
            chunks.push(String(extracted.text || ""));
            if (chunks.join("\n").length > 100000) throw new Error("PDF 正文超过 100,000 字符，请拆分资料后导入");
            if (!extracted.has_more) break;
            const next = Number(extracted.next_round);
            if (next <= round || next > 100) throw new Error("资料未提取完整，请拆分 PDF 后导入");
            round = next;
          }
          const text = chunks.join("\n").replace(/\[\[DOCUMENT ROUND \d+\]\]/g, "").replace(/<!--[^]*?-->/g, "").replace(/\[\[PAGES? (\d+)(?:-(\d+))?\]\]/g, (_, start, end) => `## ${file.name} · 第 ${start}${end ? `–${end}` : ""} 页`);
          sourceInput.current = { source_documents: [{ filename: file.name, media_type: "application/pdf", data_base64: encoded }], source_coverage: { coverage_ratio: 1, truncated: false }, visual_mode: "source_only" };
          setBrief(text); setStatus("已完整提取 PDF 正文与页码；生成后请核对章节与来源");
        } finally { if (!controller.signal.aborted) setBusy(false); }
      } else throw new Error("支持文本 PDF、Markdown、TXT 和 .deck.json 设计稿");
    } catch (cause) { setStatus(""); setError(cause instanceof Error ? cause.message : "导入失败"); }
  }
  async function restoreDraft() {
    try {
      const saved = userID ? await loadPresentationDraft(draftKey) : null;
      if (!saved) throw new Error("没有可恢复的草稿");
      initialInput.current = saved.input;
      await generate(currentInput(saved.deck));
    } catch (cause) { setError(cause instanceof Error ? cause.message : "草稿无法恢复"); }
  }
  async function addImage(event: ChangeEvent<HTMLInputElement>) {
    const file = event.target.files?.[0]; event.target.value = "";
    if (!file || !deck || !slide || slide.locked) return;
    try {
      if (!['image/png', 'image/jpeg'].includes(file.type) || file.size > 2_000_000) throw new Error("请选择 2MB 内的 PNG 或 JPEG 图片");
      const data = await fileBase64(file);
      change(replaceSlideImage(deck, slide.id, `image-${crypto.randomUUID()}`, { data_base64: data, media_type: file.type, source: file.name }));
    } catch (cause) { setError(cause instanceof Error ? cause.message : "图片读取失败"); }
  }
  function applyChart() {
    try { patchSlide({ chart: parseChart(chartText, chartSeries, chartUnit), layout: slide?.layout.startsWith("chart-") ? slide.layout : "chart-bar" }); setError(""); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "图表数据无效"); }
  }
  async function importTheme(event: ChangeEvent<HTMLInputElement>) {
    const file = event.target.files?.[0]; event.target.value = "";
    if (!file || !deck) return;
    try { if (file.size > 10000) throw new Error("品牌主题文件过大"); change({ ...deck, ...parseTheme(await file.text()) }); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "主题导入失败"); }
  }
  function syncDataFields(value?: DesignSlide) {
    setChartText(value?.chart?.labels.map((label, i) => `${label},${value.chart!.values[i]}`).join("\n") ?? "");
    setChartSeries(value?.chart?.series ?? "数值"); setChartUnit(value?.chart?.unit ?? "");
    setTableText(value?.table ? [value.table.columns, ...value.table.rows].map(row => row.join(" | ")).join("\n") : "");
  }
  function selectSlide(id: string) {
    setSelected(id); setSelectedNode(""); setRevealed(0);
    syncDataFields(deck?.slides.find(s => s.id === id));
  }
  const advance = useCallback((back = false) => {
    if (!deck || !scene) return;
    if (back) {
      if (revealed > 0) setRevealed(n => n - 1);
      else if (index > 0) { setSelected(deck.slides[index - 1].id); setRevealed(0); }
    } else if ((scene.animation === "explain" || scene.animation === "showcase") && revealed < groupCount) setRevealed(n => n + 1);
    else if (index < deck.slides.length - 1) { setSelected(deck.slides[index + 1].id); setRevealed(0); }
    else setPlaying(false);
  }, [deck, scene, index, groupCount, revealed]);
  useEffect(() => {
    if (!playing) return;
    const priorOverflow = document.body.style.overflow;
    const priorFocus = document.activeElement as HTMLElement | null;
    document.body.style.overflow = "hidden";
    return () => { document.body.style.overflow = priorOverflow; priorFocus?.focus(); };
  }, [playing]);
  useEffect(() => {
    if (!playing) return;
    const listener = (event: KeyboardEvent) => {
      if (event.key === "Escape") setPlaying(false);
      else if (event.key === "Tab") {
        const buttons = Array.from(document.querySelectorAll<HTMLButtonElement>(".studioPlayer button"));
        const first = buttons[0], last = buttons[buttons.length - 1];
        if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last?.focus(); }
        else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first?.focus(); }
      }
      else if (["ArrowRight", " ", "Enter"].includes(event.key) && !(event.target instanceof HTMLButtonElement && event.key !== "ArrowRight")) { event.preventDefault(); advance(); }
      else if (event.key === "ArrowLeft") { event.preventDefault(); advance(true); }
    };
    window.addEventListener("keydown", listener);
    return () => window.removeEventListener("keydown", listener);
  }, [playing, advance]);
  useEffect(() => {
    if (!playing || scene?.advance !== "auto" || revealed >= groupCount || !["explain", "showcase"].includes(scene.animation)) return;
    const timer = setTimeout(() => setRevealed(n => n + 1), Math.max(700, scene.duration_ms + 250));
    return () => clearTimeout(timer);
  }, [playing, scene, revealed, groupCount]);

  function applyInstruction() {
    if (!deck || !slide || slide.locked) return;
    const match = Object.entries(output?.catalog.layouts ?? {}).find(([, label]) => chatEdit.includes(label));
    if (match) { patchSlide({ layout: match[0], overrides: {} }); setStatus(`已将当前页设为${match[1]}，应用修改后查看结果`); }
    else if (/拆页|拆成两页/.test(chatEdit)) change(splitSlide(deck, slide.id, `slide-${crypto.randomUUID()}`));
    else if (/取消动画|无动画/.test(chatEdit)) patchSlide({ animation: "none" });
    else setError("当前支持按版式名称换版式、拆页和取消动画；内容改写请直接编辑正文");
    setChatEdit("");
  }

  return <section className="presentationStudio" aria-label="PPT 制作工作台">
    <header className="studioHeader"><div><span className="studioEyebrow">PRESENTATION STUDIO</span><h2>{deck ? deck.title : "把想法，做成一场好演示"}</h2><p>{deck ? `${deck.slides.length} 页 · 16:9 · 版本 ${deck.revision}` : "从完整叙事开始，统筹每页的内容、视觉与讲述节奏。"}</p></div><button type="button" onClick={onClose}>返回工作台</button></header>
    {(status || busy) && <p className="studioStatus" role="status" aria-live="polite">{busy && <span className="studioSpinner"/>}{status}</p>}
    {error && <p className="studioError" role="alert">{error}</p>}
    {!deck && <div className="studioTemplateEntry"><div><strong>从成品模板开始</strong><p>季度复盘、项目提案、课程讲解，先看完整结构，再填入自己的内容。</p></div><button type="button" disabled={busy || !enabled} onClick={() => setLibraryMode("templates")}>浏览成品模板</button></div>}
    {!deck && <div className="studioStart">
      <form onSubmit={create} className="studioBrief"><fieldset disabled={busy || !enabled}><h3>这次，想讲什么？</h3><label>演示标题<input name="title" placeholder="例如：季度产品进展与下一步计划" required maxLength={200}/></label><div className="studioRow"><label>听众<input name="audience" defaultValue="项目团队" required maxLength={200}/></label><label>目标页数<input name="slide_count" type="number" min="1" max="60" defaultValue="8" required/></label></div><label>使用方式<select name="mode"><option value="present">现场演讲 · 重点突出</option><option value="read">阅读汇报 · 信息完整</option><option value="teach">教学讲解 · 分步展开</option></select></label><label>内容与资料<textarea value={brief} onChange={e => setBrief(e.target.value)} required maxLength={100000} rows={11} placeholder={"用 ## 标记章节，每行写一个要点。\n也可以导入文本 PDF、Markdown 或 TXT。"}/></label><div className="studioActions"><button type="button" onClick={() => setBrief(example)}>填入示例内容</button><label className="studioFileButton">导入资料 / 设计稿<input type="file" accept=".pdf,.txt,.md,.json" onChange={e => void importDocument(e)}/></label></div><button className="studioPrimary" type="submit">生成设计预览 <span aria-hidden="true">→</span></button><small>多页演示用 ## 标记页面；图解页内可用 ### 划分模块。图解、对比、流程与结论可组合在同一份演示中。</small></fieldset></form>
      <aside className="studioThemes"><label className="studioStylePrompt">用一句话描述风格或用途<textarea aria-label="风格或用途" value={stylePrompt} maxLength={2000} onChange={e => setStylePrompt(e.target.value)} disabled={busy} placeholder="给投资人看的温暖科技感项目介绍，或适合课堂讲解的清晰图解" rows={4}/><small>AI会通读整稿，规划每页的表达重点、版式与动画节奏，并保持配色和字体一致。留空则使用既定风格。</small></label><h3>选择演示风格</h3><p>一套一致的字体、配色与页面节奏。</p>{themes.map(t => <button key={t.id} type="button" className={`studioTheme ${theme === t.id ? "selected" : ""}`} onClick={() => setTheme(t.id)} disabled={busy} aria-pressed={theme === t.id}><span className="studioThemePreview" style={{ background: t.colors[0], color: t.colors[1], fontFamily: t.id === 'editorial' ? '"Noto Serif CJK SC", serif' : undefined }}><i style={{ background: t.colors[2] }}/><strong>让重点<br/>被看见。</strong><span>清晰表达，自有分量</span></span><span className="studioThemeCaption"><strong>{t.title}</strong><small>{t.description}</small><span>{t.colors.map(c => <i key={c} style={{ background: c }}/>)}</span></span></button>)}
        {localDraft && <button type="button" disabled={busy} onClick={() => void restoreDraft()}>恢复本地草稿</button>}
      </aside>
    </div>}
    {!deck && <details className="studioBuiltin"><summary>使用内置项目资料生成演示</summary><form className="studioProjectEntry" onSubmit={e => { e.preventDefault(); const data = new FormData(e.currentTarget); const pages = Number(data.get("project_pages")); initialInput.current = { project_name: projectName, title: `${projectName} ${pages === 1 ? "项目能力全景" : "项目介绍"}`, audience: "项目介绍听众", style: "项目品牌风格", brief: "", slide_count: pages, style_prompt: stylePrompt.trim() || undefined, visual_mode: "none" }; void generate(initialInput.current); }}><div><strong>从已核对的项目知识开始</strong><p>完整介绍包含定位、能力全景、使用场景与运行支持。其他项目可在上方提供资料。</p></div><label>项目名称<input value={projectName} maxLength={100} onChange={e => setProjectName(e.target.value)} disabled={busy} placeholder="伴AI" required/></label><label>内容范围<select name="project_pages" disabled={busy} defaultValue="8"><option value="8">完整项目介绍 · 8 页</option><option value="1">仅一页能力全景图</option></select></label><button className="studioPrimary" disabled={busy || !enabled}>生成项目演示</button></form></details>}
    {recent.length > 0 && <label className="studioRecent">恢复已保存版本<select value="" disabled={busy} onChange={e => { const saved = recent.find(r => r.id === e.target.value); if (saved && isDesignOutput(saved.output)) { initialInput.current = { ...saved.input }; void generate(currentInput(saved.output.deck_spec)); } }}><option value="">选择任务记录…</option>{recent.map(r => <option key={r.id} value={r.id}>{String(r.output?.title)} · {new Date(r.updated_at).toLocaleString()} · {r.files.length ? "已导出" : "设计稿"}</option>)}</select></label>}
    {deck && slide && <>
      <details className="studioDeckSettings"><summary>演示设置 · 标题与听众</summary><form key={`${deck.id}:${deck.title}:${deck.audience}`} onSubmit={e => { e.preventDefault(); const data = new FormData(e.currentTarget); try { change(renameDeck(deck, String(data.get("title") || ""), String(data.get("audience") || ""), data.get("syncCover") === "on")); setStatus("演示信息已更新，应用修改后查看结果"); } catch (cause) { setError(cause instanceof Error ? cause.message : "演示信息无效"); } }}><fieldset disabled={busy}><label>整稿标题<input name="title" defaultValue={deck.title} required maxLength={200}/></label><label>听众<input name="audience" defaultValue={deck.audience} maxLength={200}/></label><label className="studioInlineChoice"><input name="syncCover" type="checkbox" defaultChecked={!deck.slides[0].locked && ["cover", "cover-split"].includes(deck.slides[0].layout)} disabled={deck.slides[0].locked || !["cover", "cover-split"].includes(deck.slides[0].layout)}/>同步封面标题（已锁定封面保持不变）</label><button type="submit">更新演示信息</button></fieldset></form></details>
      <fieldset className="studioToolbar" disabled={busy}><label>主题<select value={deck.theme_id} onChange={e => change({ ...deck, theme_id: e.target.value as ThemeID, brand: {} })}>{themes.map(t => <option key={t.id} value={t.id}>{t.title} · {t.description}</option>)}</select></label><label>全稿动画<select value={deck.animation} onChange={e => change({ ...deck, animation: e.target.value as Motion, slides: deck.slides.map(s => s.locked ? s : { ...s, animation: "inherit" }) })}><option value="none">无动画</option><option value="subtle">克制 · 淡化</option><option value="explain">讲解 · 分步出现</option><option value="showcase">展示 · 推入与出现</option></select></label><button onClick={() => { if (history) restoreHistory(undo(history)); }} disabled={!history?.past.length}>撤销</button><button onClick={() => { if (history) restoreHistory(redo(history)); }} disabled={!history?.future.length}>重做</button><button className={dirty ? "studioPrimary" : ""} onClick={() => void generate(currentInput(deck), false, true)}>{dirty ? "应用修改" : "保存版本"}</button><button disabled={dirty || !output?.quality_report.passed} onClick={() => { setPlaying(true); setRevealed(0); }}>试播</button><button className="studioPrimary" onClick={() => void generate(currentInput(deck), true, true)}>导出 PPTX / PDF</button></fieldset>
      <form className="studioStyleConversation" onSubmit={e => { e.preventDefault(); void generate({ ...currentInput(deck), style_prompt: stylePrompt.trim() }, false, true); }}><label>继续描述风格或用途<input aria-label="对话调整风格" value={stylePrompt} onChange={e => setStylePrompt(e.target.value)} maxLength={2000} placeholder="更简洁，改为适合课堂讲解的清晰图解" disabled={busy}/></label><button className="studioPrimary" disabled={busy || !stylePrompt.trim()}>AI调整风格</button>{deck.style_profile && <p><strong>{deck.style_profile.name}</strong> {deck.style_profile.rationale}</p>}</form>
      <details className="studioStoryboard"><summary>整稿结构与节奏 · {deck.slides.length} 页</summary>{deck.style_profile?.storyline && <p>{deck.style_profile.storyline}</p>}<ol>{deck.slides.map((s, i) => { const preview = output?.scenes.find(v => v.id === s.id); return <li key={s.id}><button type="button" aria-current={s.id === slide.id ? "page" : undefined} onClick={() => selectSlide(s.id)}>{preview && <PresentationCanvas scene={preview} deck={deck}/>}<strong>{String(i + 1).padStart(2, "0")} {s.title}</strong><span>{s.purpose || output?.catalog.layouts[s.layout]}</span><small>{output?.catalog.layouts[s.layout]} · {({ none: "静态呈现", subtle: "克制淡化", explain: "逐步讲解", showcase: "重点展示" })[s.animation === "inherit" ? deck.animation : s.animation]}</small></button></li>; })}</ol></details>
      <div className="studioEditor">
        <nav className="studioSlides" aria-label="幻灯片导航">{deck.slides.map((s, i) => {
          const preview = output?.scenes.find(v => v.id === s.id);
          return <button key={s.id} type="button" aria-current={s.id === slide.id ? "page" : undefined} onClick={() => selectSlide(s.id)}><span className="studioThumbnail">{preview ? <PresentationCanvas scene={preview} deck={deck}/> : <span>应用修改后预览</span>}</span><span><b>{String(i + 1).padStart(2, '0')}</b> {s.title}{s.locked && " · 已锁定"}</span></button>;
        })}</nav>
        <main className="studioStage"><div className="studioStageTop"><span>{dirty ? "预览待更新 · 点击应用修改" : "设计预览"}</span>{output?.rendered_previews[index] && !dirty && <button type="button" onClick={() => setActual(!actual)}>{actual ? "查看设计预览" : "查看导出实拍"}</button>}</div>
          {actual && output?.rendered_previews[index] && !dirty ? <Image unoptimized width={960} height={540} className="studioActual" alt={`实际 PPTX 渲染第 ${index + 1} 页`} src={output.rendered_previews[index]}/> : scene ? <PresentationCanvas scene={scene} deck={deck} selectedNode={selectedNode} onSelect={setSelectedNode}/> : <div className="studioEmptyPreview">应用修改后生成此页预览</div>}
          <div className="studioPageTools"><button disabled={busy || deck.slides.length >= 60} onClick={() => setLibraryMode("pages")}>添加页面</button><button disabled={busy || index <= 0} onClick={() => change(moveSlide(deck, slide.id, -1))}>向前移</button><button disabled={busy || index >= deck.slides.length - 1} onClick={() => change(moveSlide(deck, slide.id, 1))}>向后移</button><button disabled={busy} onClick={() => patchSlide({ locked: !slide.locked })}>{slide.locked ? "解锁页面" : "锁定页面"}</button><button disabled={busy || slide.locked || slide.items.length < 2 || !!slide.table || !!slide.chart || slide.layout === "capability-map" || deck.slides.length >= 60} onClick={() => change(splitSlide(deck, slide.id, `slide-${crypto.randomUUID()}`))}>拆成两页</button><button disabled={busy || deck.slides.length >= 60} onClick={() => { const id = `slide-${crypto.randomUUID()}`; const copied = { ...structuredClone(slide), id, locked: false }; change({ ...deck, slides: [...deck.slides.slice(0, index + 1), copied, ...deck.slides.slice(index + 1)] }); setSelected(id); setSelectedNode(""); syncDataFields(copied); }}>复制页</button><button disabled={busy || slide.locked || deck.slides.length <= 1} onClick={() => { change(removeSlide(deck, slide.id)); selectSlide(deck.slides[index ? index - 1 : 1].id); }}>删除页</button></div>
          <form className="studioQuickEdit" onSubmit={e => { e.preventDefault(); applyInstruction(); }}><input aria-label="单页修改指令" value={chatEdit} onChange={e => setChatEdit(e.target.value)} disabled={busy || slide.locked} placeholder="例如：改成时间轴、拆页、取消动画"/><button disabled={busy || slide.locked || !chatEdit.trim()}>调整此页</button></form>
          <details className="studioNotes"><summary>讲稿与来源</summary><label>演讲备注<textarea value={slide.notes} disabled={busy || slide.locked} onChange={e => patchSlide({ notes: e.target.value })} rows={4}/></label><label>来源说明<input value={slide.source} disabled={busy || slide.locked} onChange={e => patchSlide({ source: e.target.value })}/></label></details>
          {output && <div className={`studioQuality ${output.quality_report.passed ? "passed" : "failed"}`}><strong>{output.quality_report.passed ? "版式检查通过" : "需要调整"}{dirty && "（上次预览）"}</strong>{output.quality_report.violations.map((v, i) => <p key={i}>{v.slide ? `第 ${v.slide} 页：` : ""}{v.message}</p>)}{[...output.quality_report.repairs, ...output.warnings, ...(scene?.warnings ?? [])].map((w, i) => <p key={`w-${i}`}>{w}</p>)}{output.render_report.status === "verified" && <p>最终 PPTX 已由 LibreOffice 渲染检查；PowerPoint / WPS 动画播放需在对应软件中验证。</p>}</div>}
          {run && !dirty && run.files.length > 0 && <div className="studioDownloads">{run.files.map(file => <PresentationDownload key={`${run.id}:${file.id}`} token={token} runID={run.id} file={file}/>)}</div>}
        </main>
        <aside className="studioInspector"><fieldset disabled={busy || slide.locked}><h3>页面设计</h3>{slide.purpose && <p className="studioPagePurpose">{slide.purpose}</p>}<label>标题<input aria-label="页面标题" value={slide.title} maxLength={200} onChange={e => patchSlide({ title: e.target.value })}/></label><label>版式<select value={slide.layout} onChange={e => patchSlide({ layout: e.target.value, overrides: {} })}>{Object.entries(output?.catalog.layouts ?? {}).map(([value, label]) => <option key={value} value={value} disabled={value === "capability-map" && (slide.items.length < 2 || slide.items.length > 10)}>{label}</option>)}</select></label>{slide.layout === "capability-map" ? <div className="studioDiagramFields"><p>每个模块首行是标题，后续行是说明。</p>{slide.items.map((item, i) => <label key={item.id}>{`模块 ${i + 1}`}<textarea aria-label={`图解模块${i + 1}`} value={item.text} onChange={e => patchSlide({ items: slide.items.map((v, n) => n === i ? { ...v, text: e.target.value } : v) })} rows={4}/></label>)}</div> : <label>正文 · 每行一个内容块<textarea aria-label="页面正文" value={slide.items.map(i => i.text).join("\n")} onChange={e => patchSlide({ items: editItems(slide.items, e.target.value) })} rows={7}/></label>}
          <details open={!!slide.image_id}><summary>图片</summary><label className="studioFileButton">{slide.image_id ? "替换图片" : "添加图片"}<input type="file" accept="image/png,image/jpeg" onChange={e => void addImage(e)}/></label>{slide.image_id && <><label>图片适配<select value={slide.image_fit} onChange={e => patchSlide({ image_fit: e.target.value as "contain" | "cover" })}><option value="contain">完整显示</option><option value="cover">裁剪填满</option></select></label><button onClick={() => change(replaceSlideImage(deck, slide.id, ""))}>移除图片</button></>}</details>
          <details open={!!slide.chart}><summary>可编辑图表</summary><label>系列名称<input value={chartSeries} onChange={e => setChartSeries(e.target.value)}/></label><label>单位<input value={chartUnit} onChange={e => setChartUnit(e.target.value)} maxLength={20}/></label><label>数据 · 每行标签,数值<textarea aria-label="图表数据" value={chartText} onChange={e => setChartText(e.target.value)} placeholder={"第一季度,120\n第二季度,180"} rows={4}/></label><button onClick={applyChart}>应用图表数据</button>{slide.chart && <button onClick={() => patchSlide({ chart: undefined, layout: "bullets" })}>移除图表</button>}</details>
          <details open={!!slide.table}><summary>可编辑表格</summary><label>首行表头 · 用 | 分隔<textarea aria-label="表格数据" value={tableText} onChange={e => setTableText(e.target.value)} placeholder={"阶段 | 状态 | 负责人\n调研 | 已完成 | 产品组\n验收 | 进行中 | 项目组"} rows={6}/></label><button onClick={() => { try { patchSlide({ table: parseTable(tableText), layout: "table" }); setError(""); } catch (cause) { setError(cause instanceof Error ? cause.message : "表格数据无效"); } }}>应用表格数据</button>{slide.table && <button onClick={() => patchSlide({ table: undefined, layout: "bullets" })}>移除表格</button>}</details>
          <details><summary>单页动画</summary><label>效果<select value={slide.animation} onChange={e => patchSlide({ animation: e.target.value as Motion | "inherit" })}><option value="inherit">跟随全稿</option><option value="none">无动画</option><option value="subtle">淡化转场</option><option value="explain">逐组淡入</option><option value="showcase">推入与淡入</option></select></label><label>触发<select value={slide.advance} onChange={e => patchSlide({ advance: e.target.value as "click" | "auto" })}><option value="click">点击下一步</option><option value="auto">自动依次出现</option></select></label><label>时长（毫秒）<input type="number" min="100" max="2000" step="50" value={slide.duration_ms} onChange={e => patchSlide({ duration_ms: Number(e.target.value) })}/></label></details>
          <details><summary>品牌与字体</summary><label>强调色<input type="color" value={`#${deck.brand.accent || output?.catalog.themes[deck.theme_id]?.accent || '305BD6'}`} onChange={e => change({ ...deck, brand: { ...deck.brand, accent: e.target.value.slice(1) } })}/></label>{["heading_font", "body_font"].map(key => <label key={key}>{key === "heading_font" ? "标题字体" : "正文字体"}<select value={deck.brand[key] || output?.catalog.themes[deck.theme_id]?.[key]} onChange={e => change({ ...deck, brand: { ...deck.brand, [key]: e.target.value } })}>{output?.catalog.fonts.map(f => <option key={f}>{f}</option>)}</select></label>)}<button onClick={() => downloadJSON(`${deck.title}.theme.json`, { version: "presentation-theme-v1", theme_id: deck.theme_id, brand: deck.brand })}>保存品牌主题</button><label className="studioFileButton">导入品牌主题<input type="file" accept=".json" onChange={e => void importTheme(e)}/></label></details>
          {node && <details open><summary>选中对象：{node.id}</summary>{["x", "y", "w", "h", ...(node.type === "text" ? ["font_size"] : [])].map(key => <label key={key}>{({ x: "水平位置", y: "垂直位置", w: "宽度", h: "高度", font_size: "字号" } as Record<string, string>)[key]}<input type="number" min={key === "font_size" ? 10 : 0} max={key === "font_size" ? 72 : key === "y" || key === "h" ? 540 : 960} value={slide.overrides[node.id]?.[key as keyof Override] as number ?? node[key as keyof typeof node] as number} onChange={e => patchSlide({ overrides: { ...slide.overrides, [node.id]: { ...slide.overrides[node.id], [key]: Number(e.target.value) } } })}/></label>)}<label><input type="checkbox" checked={slide.overrides[node.id]?.locked ?? false} onChange={e => patchSlide({ overrides: { ...slide.overrides, [node.id]: { ...slide.overrides[node.id], locked: e.target.checked } } })}/>固定此对象字号</label><button onClick={() => { const overrides = { ...slide.overrides }; delete overrides[node.id]; patchSlide({ overrides }); }}>恢复自动排版</button></details>}
        </fieldset>{slide.locked && <p>页面已锁定，解锁后可修改内容。</p>}</aside>
      </div>
      <footer className="studioFooter"><button disabled={busy} onClick={() => downloadJSON(`${deck.title}.deck.json`, deck)}>保存本地设计稿</button><label className="studioFileButton">导入设计稿<input type="file" accept=".json" disabled={busy} onChange={e => void importDocument(e)}/></label><button disabled={busy} onClick={() => { setHistory(null); setOutput(null); setRun(null); setSelected(""); setBrief(""); setStatus(""); setError(""); sourceInput.current = {}; }}>新建演示</button><small role="status">{draftNotice || "草稿仅保存在当前账户的此浏览器中"}；保存版本后可从任务记录恢复。</small></footer>
    </>}
    {libraryMode && <PresentationLibrary mode={libraryMode} theme={deck?.theme_id ?? theme} onClose={() => setLibraryMode(null)} onTemplate={template => void createFromTemplate(template)} onInsert={addPage}/>}
    {playing && scene && deck && createPortal(<div className="studioPlayer" role="dialog" aria-modal="true" aria-label="演示试播"><div className="studioPlayerBar"><span>{index + 1} / {deck.slides.length}</span><span>空格 / → 下一步，← 上一步，Esc 退出</span><button autoFocus onClick={() => setPlaying(false)}>退出试播</button></div><div className="studioPlayerCanvas" onClick={() => advance()}><PresentationCanvas key={scene.id} scene={scene} deck={deck} revealed={scene.animation === "explain" || scene.animation === "showcase" ? revealed : Infinity} animate/></div><div className="studioPlayerControls"><button onClick={() => advance(true)}>上一步</button><button onClick={() => advance()}>下一步</button></div></div>, document.body)}
  </section>;
}
