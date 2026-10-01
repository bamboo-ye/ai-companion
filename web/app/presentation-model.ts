export const DESIGN_VERSION = "presentation-design-v2";
export type ThemeID = "business" | "technology" | "editorial" | "companion";
export type Motion = "none" | "subtle" | "explain" | "showcase";
export type Chart = { labels: string[]; values: number[]; series: string; unit: string };
export type SlideItem = { id: string; text: string; source?: string };
export type Override = Partial<{ x: number; y: number; w: number; h: number; font_size: number; color: string; fill: string; locked: boolean }>;
export type DesignSlide = {
  id: string; title: string; layout: string; items: SlideItem[]; notes: string; source: string; locked: boolean;
  purpose?: string;
  image_id: string; image_fit: "contain" | "cover"; animation: Motion | "inherit"; duration_ms: number; advance: "click" | "auto";
  regions?: { lead: string; main: string[]; aside: string; foundation: string[]; footer: string; flow: "down" | "up" | "none" };
  overrides: Record<string, Override>; chart?: Chart; table?: { columns: string[]; rows: string[][] };
};
export type Deck = {
  version: string; id: string; title: string; audience: string; theme_id: ThemeID; mode: "present" | "read" | "teach";
  typography?: {title_size: number; body_size: number; radius: number};
  style_profile?: {name: string; rationale: string; prompt: string; storyline?: string};
  animation: Motion; revision: number; brand: Record<string, string>; slides: DesignSlide[];
  assets: Record<string, { data_base64: string; media_type: string; source: string }>;
};
export type SceneNode = {
  id: string; type: "text" | "shape" | "image" | "chart" | "table" | "connector"; x: number; y: number; w: number; h: number;
  text?: string; lines?: string[]; font?: string; font_size?: number; line_height?: number; bold?: boolean; color?: string;
  radius?: number; direction?: string; fill?: string; kind?: string; group: number; asset_id?: string; fit?: string; chart?: Chart; chart_type?: string;
  table?: { columns: string[]; rows: string[][] }; accent?: string;
  header_color?: string; row_heights?: number[]; cell_lines?: string[][][];
};
export type Scene = { id: string; background: string; foreground?: string; nodes: SceneNode[]; animation: Motion; duration_ms: number; advance: "click" | "auto"; warnings: string[] };
export type DesignOutput = {
  deck_spec: Deck; scenes: Scene[]; catalog: { themes: Record<string, Record<string, string>>; layouts: Record<string, string>; fonts: string[] };
  quality_report: { passed: boolean; violations: { code: string; message: string; slide?: number; object_id?: string }[]; repairs: string[] };
  render_report: { status: string; passed: boolean; message?: string }; rendered_previews: string[]; warnings: string[];
};
export type History = { past: Deck[]; present: Deck; future: Deck[] };

export function commit(history: History, deck: Deck): History {
  if (JSON.stringify(history.present) === JSON.stringify(deck)) return history;
  return { past: [...history.past.slice(-29), history.present], present: { ...deck, revision: history.present.revision + 1 }, future: [] };
}
export function acceptDesign(history: History, deck: Deck): History {
  if (JSON.stringify(history.present) === JSON.stringify(deck)) return history;
  return { past: [...history.past.slice(-29), history.present], present: deck, future: [] };
}
export function undo(history: History): History {
  if (!history.past.length) return history;
  return { past: history.past.slice(0, -1), present: history.past[history.past.length - 1], future: [history.present, ...history.future] };
}
export function redo(history: History): History {
  if (!history.future.length) return history;
  return { past: [...history.past, history.present], present: history.future[0], future: history.future.slice(1) };
}
export function updateSlide(deck: Deck, id: string, patch: Partial<DesignSlide>): Deck {
  return { ...deck, slides: deck.slides.map(s => s.id === id && (!s.locked || Object.keys(patch).every(key => key === "locked")) ? { ...s, ...patch } : s) };
}
export function insertSlide(deck: Deck, afterID: string, preset: DesignSlide, id: string): Deck {
  if (deck.slides.length >= 60) throw new Error("每份演示最多 60 页，请先整理现有页面");
  if (deck.slides.some(s => s.id === id)) throw new Error("页面标识重复，请重试");
  const index = deck.slides.findIndex(s => s.id === afterID);
  if (index < 0) throw new Error("当前页不存在，请重新选择页面");
  const slide = { ...structuredClone(preset), id, locked: false, animation: "inherit" as const, overrides: {} };
  if (slide.image_id && !deck.assets[slide.image_id]) throw new Error("页面图片缺失，请先导入图片");
  return { ...deck, slides: [...deck.slides.slice(0, index + 1), slide, ...deck.slides.slice(index + 1)] };
}
export function removeSlide(deck: Deck, id: string): Deck {
  const slide = deck.slides.find(s => s.id === id);
  if (!slide || slide.locked || deck.slides.length <= 1) return deck;
  const slides = deck.slides.filter(s => s.id !== id);
  const assets = { ...deck.assets };
  if (slide.image_id && !slides.some(s => s.image_id === slide.image_id)) delete assets[slide.image_id];
  return { ...deck, slides, assets };
}
export function instantiateTemplate(template: Deck, id: string): Deck {
  const deck = structuredClone(template);
  return { ...deck, id, revision: 1, slides: deck.slides.map((s, i) => ({ ...s, id: `${id}-p${i}`, locked: false })) };
}
export function renameDeck(deck: Deck, title: string, audience: string, syncCover: boolean): Deck {
  title = title.trim(); audience = audience.trim();
  if (!title || title.length > 200 || audience.length > 200) throw new Error("请填写 1–200 字的演示标题，听众不能超过 200 字");
  const first = deck.slides[0];
  const slides = syncCover && first && !first.locked && ["cover", "cover-split"].includes(first.layout)
    ? [{ ...first, title }, ...deck.slides.slice(1)] : deck.slides;
  return { ...deck, title, audience, slides };
}
export function replaceSlideImage(deck: Deck, slideID: string, imageID: string, asset?: Deck["assets"][string]): Deck {
  const slide = deck.slides.find(item => item.id === slideID);
  if (!slide || slide.locked) return deck;
  const updated = updateSlide(deck, slideID, { image_id: imageID, layout: slide.layout === "capability-map" ? slide.layout : imageID ? "image-right" : "bullets", image_fit: "contain" });
  const assets = { ...deck.assets, ...(asset ? { [imageID]: asset } : {}) };
  if (slide.image_id && !updated.slides.some(item => item.image_id === slide.image_id)) delete assets[slide.image_id];
  return { ...updated, assets };
}
export function moveSlide(deck: Deck, id: string, delta: number): Deck {
  const slides = [...deck.slides];
  const index = slides.findIndex(s => s.id === id), next = index + delta;
  if (index < 0 || next < 0 || next >= slides.length) return deck;
  [slides[index], slides[next]] = [slides[next], slides[index]];
  return { ...deck, slides };
}
export function splitSlide(deck: Deck, id: string, newID: string): Deck {
  const index = deck.slides.findIndex(s => s.id === id), slide = deck.slides[index];
  if (!slide || slide.locked || slide.items.length < 2 || deck.slides.length >= 60 || slide.table || slide.chart || slide.layout === "capability-map") return deck;
  const midpoint = Math.ceil(slide.items.length / 2);
  const first = { ...slide, layout: slide.image_id ? "image-right" : "bullets", items: slide.items.slice(0, midpoint), overrides: {} };
  const second = { ...slide, id: newID, image_id: "", layout: "bullets", items: slide.items.slice(midpoint), overrides: {} };
  return { ...deck, slides: [...deck.slides.slice(0, index), first, second, ...deck.slides.slice(index + 1)] };
}
export function editItems(existing: SlideItem[], text: string): SlideItem[] {
  const used = new Set(existing.map(item => item.id));
  return text.split("\n").filter(line => line.trim()).map((line, i) => {
    let id = existing[i]?.id;
    if (!id) { let n = i; while (used.has(`item-${n}`)) n++; id = `item-${n}`; used.add(id); }
    return { id, text: line, source: existing[i]?.source ?? "" };
  });
}
export function parseTable(text: string): NonNullable<DesignSlide["table"]> {
  const rows = text.trim().split("\n").map(row => row.split("|").map(cell => cell.trim()));
  const columns = rows.shift();
  if (!columns?.length || columns.length > 8 || !rows.length || rows.length > 12 || rows.some(row => row.length !== columns.length)) throw new Error("首行填写表头，用 | 分隔各列；需要 1–8 列和 1–12 行记录，每行列数保持一致");
  return { columns, rows };
}
export function parseTheme(text: string): { theme_id: ThemeID; brand: Record<string, string> } {
  const value = JSON.parse(text);
  if (value?.version !== "presentation-theme-v1" || !["business", "technology", "editorial", "companion"].includes(value.theme_id) || !value.brand || typeof value.brand !== "object") throw new Error("请选择本工作台保存的品牌主题");
  const brand: Record<string, string> = {};
  for (const key of ["accent", "background", "ink", "muted", "surface"]) {
    if (value.brand[key] !== undefined) { if (!/^[\da-f]{6}$/i.test(value.brand[key])) throw new Error("主题颜色无效"); brand[key] = value.brand[key]; }
  }
  for (const key of ["heading_font", "body_font"]) {
    if (value.brand[key] !== undefined) { if (!["Noto Sans CJK SC", "Noto Serif CJK SC", "WenQuanYi Zen Hei"].includes(value.brand[key])) throw new Error("主题字体不受支持"); brand[key] = value.brand[key]; }
  }
  return { theme_id: value.theme_id, brand };
}
export function parseChart(text: string, series: string, unit: string): Chart {
  const rows = text.split("\n").filter(s => s.trim());
  if (!rows.length || rows.length > 12) throw new Error("图表需要 1–12 行数据");
  const labels: string[] = [], values: number[] = [];
  for (const row of rows) {
    const match = row.match(/^(.+?)[,，\t]\s*(-?\d+(?:\.\d+)?)\s*$/);
    if (!match || Math.abs(Number(match[2])) > 1e12) throw new Error("每行填写「标签,数值」，例如：第一季度,120");
    labels.push(match[1].trim()); values.push(Number(match[2]));
  }
  return { labels, values, series, unit };
}
export function parseDeck(text: string): Deck {
  if (text.length > 22_000_000) throw new Error("设计稿超过 22MB");
  const value = JSON.parse(text) as Deck;
  if (!value || value.version !== DESIGN_VERSION || !Array.isArray(value.slides) || !value.slides.length || value.slides.length > 60 || typeof value.title !== "string") throw new Error("请选择本工作台导出的 .deck.json 设计稿");
  // The server remains authoritative; imports must pass its schema before display.
  return value;
}
export function isDesignOutput(value: unknown): value is DesignOutput {
  return !!value && typeof value === "object" && "deck_spec" in value && (value as DesignOutput).deck_spec?.version === DESIGN_VERSION && Array.isArray((value as DesignOutput).scenes);
}
