"use client";

import { useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { PresentationCanvas } from "./presentation-canvas";
import type { Deck, DesignSlide, Scene, ThemeID } from "./presentation-model";

type Preview = { deck: Deck; scenes: Scene[] };
type Template = Preview & { id: string; title: string; category: string; description: string };
type PagePreset = { id: string; title: string; category: string; description: string; variants: Record<ThemeID, Preview> };
type Library = { version: number; templates: Template[]; pages: PagePreset[] };

export function PresentationLibrary({ mode, theme, onClose, onTemplate, onInsert }: {
  mode: "templates" | "pages"; theme: ThemeID; onClose: () => void;
  onTemplate: (deck: Deck) => void; onInsert: (slide: DesignSlide) => void;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const [library, setLibrary] = useState<Library | null>(null);
  const [error, setError] = useState("");
  const [retry, setRetry] = useState(0);
  const [category, setCategory] = useState("全部");
  const [query, setQuery] = useState("");
  const [selected, setSelected] = useState("");
  const [page, setPage] = useState(0);

  useEffect(() => {
    const element = dialog.current;
    const focused = document.activeElement as HTMLElement | null;
    const overflow = document.body.style.overflow;
    document.body.style.overflow = "hidden"; element?.showModal();
    return () => { element?.close(); document.body.style.overflow = overflow; focused?.focus(); };
  }, []);
  useEffect(() => {
    const controller = new AbortController();
    void fetch("/presentation-library.json", { signal: controller.signal }).then(async response => {
      if (!response.ok) throw new Error("模板库暂时无法加载");
      const data = await response.json() as Library;
      if (data.version !== 1 || !data.templates?.length || !data.pages?.length) throw new Error("模板库版本不匹配，请刷新后重试");
      setLibrary(data); setError("");
    }).catch(cause => { if (!controller.signal.aborted) setError(cause instanceof Error ? cause.message : "模板库加载失败"); });
    return () => controller.abort();
  }, [retry]);

  const entries: Template[] = mode === "templates" ? library?.templates ?? [] : library?.pages.map(p => ({ ...p, ...p.variants[theme] })) ?? [];
  const filtered = entries.filter(p => (category === "全部" || p.category === category) && `${p.title} ${p.description} ${p.category}`.includes(query.trim()));
  const current = filtered.find(p => p.id === selected) ?? filtered[0];
  const currentPage = current ? Math.min(page, current.deck.slides.length - 1) : 0;

  return createPortal(<dialog ref={dialog} className="studioLibrary" aria-labelledby="studio-library-title" onCancel={onClose}>
    <header className="studioLibraryHeader"><div><span>精选原创 · 可继续编辑</span><h2 id="studio-library-title">{mode === "templates" ? "成品模板库" : "添加页面"}</h2><p>{mode === "templates" ? "先预览完整结构，再替换为自己的内容。" : "在当前页后插入，沿用整稿主题和动画。"}</p></div><button type="button" onClick={onClose} aria-label="关闭模板库">关闭</button></header>
    {error ? <div className="studioLibraryMessage" role="alert">{error}<button onClick={() => { setError(""); setRetry(n => n + 1); }}>重试加载</button></div> : !library ? <p className="studioLibraryMessage" role="status">正在加载模板预览…</p> : <>
      <div className="studioLibraryFilters"><input autoFocus aria-label="搜索模板" placeholder="搜索标题、用途或版式" value={query} onChange={e => { setQuery(e.target.value); setPage(0); }}/><nav aria-label="模板分类">{["全部", ...new Set(entries.map(p => p.category))].map(c => <button key={c} aria-pressed={category === c} onClick={() => { setCategory(c); setPage(0); }}>{c}</button>)}</nav></div>
      <div className="studioLibraryBody"><div className="studioLibraryGrid" aria-label="模板列表">{filtered.map(entry => <button key={entry.id} className="studioLibraryCard" aria-pressed={current?.id === entry.id} aria-label={`预览 ${entry.title}`} onClick={() => { setSelected(entry.id); setPage(0); }}><PresentationCanvas scene={entry.scenes[0]} deck={entry.deck}/><strong>{entry.title}</strong><span>{entry.category} · {entry.deck.slides.length} 页</span></button>)}{!filtered.length && <p role="status">没有找到匹配的模板，试试其他关键词。</p>}</div>
        {current && <section className="studioLibraryDetail" aria-label="模板详情"><PresentationCanvas scene={current.scenes[currentPage]} deck={current.deck}/><div className="studioLibraryPager"><button disabled={currentPage === 0} onClick={() => setPage(currentPage - 1)}>上一页</button><span aria-live="polite">{currentPage + 1} / {current.deck.slides.length} · {current.deck.slides[currentPage].title}</span><button disabled={currentPage === current.deck.slides.length - 1} onClick={() => setPage(currentPage + 1)}>下一页</button></div><h3>{current.title}</h3><p>{current.description}</p><small>{mode === "templates" ? "模板中的「待填写」需要替换为实际内容。" : "插入后可修改标题、正文、图表及表格。示例数据需替换为真实数据。"}{mode === "pages" && " 预览使用所选主题的默认配色；应用后保留你的品牌设置。"}</small><button className="studioLibraryUse" onClick={() => mode === "templates" ? onTemplate(current.deck) : onInsert(current.deck.slides[0])}>{mode === "templates" ? "使用此模板" : "插入此页"}</button></section>}
      </div>
    </>}
  </dialog>, document.body);
}
