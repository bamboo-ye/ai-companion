"use client";

import { useEffect, useId, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { type GuideScope, type GuideTopic, searchGuideTopics } from "./onboarding-content";
import { type GuideProgress, guideStorageKey, parseGuideProgress } from "./onboarding-state";
import styles from "./onboarding.module.css";

type GuideProps<T extends string> = {
  scope: GuideScope;
  topics: readonly GuideTopic<T>[];
  currentTopic?: string;
  autoStart?: boolean;
  compact?: boolean;
  toolbar?: boolean;
  onNavigate?: (target: T) => void;
};

export function OnboardingGuide<T extends string>({ scope, topics, currentTopic = "welcome", autoStart = false, compact = false, toolbar = false, onNavigate }: GuideProps<T>) {
  const [progress, setProgress] = useState<GuideProgress | null>(null);
  const [open, setOpen] = useState(false);
  const [selectedID, setSelectedID] = useState(topics[0]?.id ?? "");
  const [query, setQuery] = useState("");
  const [storageUnavailable, setStorageUnavailable] = useState(false);
  const dialogRef = useRef<HTMLDialogElement>(null);
  const articleRef = useRef<HTMLElement>(null);
  const directoryRef = useRef<HTMLElement>(null);
  const headingRef = useRef<HTMLHeadingElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const titleID = useId();
  const descriptionID = useId();
  const topicTitleID = useId();
  const storageKey = guideStorageKey(scope);
  const scopeLabel = scope === "admin" ? "后台管理" : "普通用户";

  useEffect(() => {
    let active = true;
    queueMicrotask(() => {
      if (!active) return;
      let raw: string | null = null;
      try { raw = window.localStorage.getItem(storageKey); }
      catch { setStorageUnavailable(true); }
      const saved = parseGuideProgress(raw, topics.map((topic) => topic.id));
      setProgress(saved);
      setSelectedID(saved.lastTopic);
      if (autoStart && !saved.dismissed) setOpen(true);
    });
    return () => { active = false; };
  }, [autoStart, storageKey, topics]);

  useEffect(() => {
    if (!open) return;
    const dialog = dialogRef.current;
    if (!dialog) return;
    const previousFocus = document.activeElement;
    const trigger = triggerRef.current;
    const previousOverflow = document.body.style.overflow;
    dialog.showModal();
    document.body.style.overflow = "hidden";
    return () => {
      dialog.close();
      document.body.style.overflow = previousOverflow;
      if (previousFocus instanceof HTMLElement && previousFocus !== document.body && previousFocus.isConnected) previousFocus.focus();
      else trigger?.focus();
    };
  }, [open]);

  const matches = searchGuideTopics(topics, query);
  const selected = matches.find((topic) => topic.id === selectedID) ?? matches[0];
  const index = selected ? matches.indexOf(selected) : -1;
  const readCount = progress?.read.length ?? 0;
  const contextTopic = topics.find((topic) => topic.id === currentTopic) ?? topics[0];
  const groups = [...new Set(matches.map((topic) => topic.group))];

  useEffect(() => {
    articleRef.current?.scrollTo({ top: 0 });
  }, [selected?.id]);

  useEffect(() => {
    if (!open) return;
    const directory = directoryRef.current;
    if (!directory) return;
    const revealChapter = () => directory.querySelector('[aria-current="step"]')?.scrollIntoView({ block: "nearest", inline: "nearest" });
    revealChapter();
    const observer = new ResizeObserver(revealChapter);
    observer.observe(directory);
    return () => observer.disconnect();
  }, [open, selected?.id]);

  function saveProgress(next: GuideProgress) {
    setProgress(next);
    try { window.localStorage.setItem(storageKey, JSON.stringify(next)); }
    catch { setStorageUnavailable(true); }
  }

  function showGuide(topicID: string) {
    setQuery("");
    setSelectedID(topicID);
    setOpen(true);
  }

  function closeGuide() {
    if (progress) saveProgress({ ...progress, dismissed: true, lastTopic: selected?.id ?? progress.lastTopic });
    setOpen(false);
  }

  function selectTopic(topicID: string) {
    setSelectedID(topicID);
    if (progress) saveProgress({ ...progress, lastTopic: topicID });
    window.requestAnimationFrame(() => headingRef.current?.focus());
  }

  function markRead() {
    if (!progress || !selected) return;
    saveProgress({ ...progress, dismissed: true, lastTopic: selected.id, read: [...new Set([...progress.read, selected.id])] });
  }

  function nextTopic() {
    if (!progress || !selected) return;
    const next = matches[index + 1];
    saveProgress({ ...progress, dismissed: true, lastTopic: next?.id ?? selected.id, read: [...new Set([...progress.read, selected.id])] });
    if (next) {
      setSelectedID(next.id);
      window.requestAnimationFrame(() => headingRef.current?.focus());
    } else {
      setOpen(false);
    }
  }

  function restart() {
    saveProgress({ dismissed: true, read: [], lastTopic: topics[0]?.id ?? "" });
    setQuery("");
    setSelectedID(topics[0]?.id ?? "");
    window.requestAnimationFrame(() => headingRef.current?.focus());
  }

  return <>
    <div className={toolbar ? styles.toolbarLauncher : compact ? styles.compactLauncher : styles.launcher} data-scope={scope}>
      {!compact && !toolbar && <div className={styles.launcherCopy}>
        <span className={styles.guideIcon} aria-hidden="true">?</span>
        <div><strong>{readCount === topics.length ? "功能指南，随时查阅" : "从第一步开始，熟悉每个功能"}</strong><span>{contextTopic?.title} · 已读 {readCount}/{topics.length} 节</span></div>
      </div>}
      <div className={styles.launcherActions}>
        <button ref={triggerRef} type="button" aria-haspopup="dialog" disabled={!progress} onClick={() => showGuide(compact ? "welcome" : contextTopic?.id ?? "welcome")}>{toolbar && <span className={styles.buttonIcon} aria-hidden="true">?</span>}新手指引</button>
        {!compact && <button type="button" className={styles.quietButton} disabled={!progress} onClick={() => showGuide(progress?.lastTopic ?? "welcome")}>继续阅读</button>}
      </div>
    </div>
    {progress && createPortal(
      <dialog ref={dialogRef} className={styles.dialog} aria-labelledby={titleID} aria-describedby={descriptionID} onCancel={(event) => { event.preventDefault(); closeGuide(); }}>
        <div className={styles.dialogLayout}>
          <header className={styles.header}>
            <div><span className={styles.eyebrow}>伴AI · {scopeLabel}</span><h2 id={titleID}>新手指引</h2><p id={descriptionID}>按步骤上手，或搜索你现在想用的功能。</p></div>
            <button type="button" className={styles.closeButton} onClick={closeGuide} aria-label="关闭新手指引">×</button>
          </header>
          <div className={styles.progressRow}>
            <span role="status">已读 {readCount}/{topics.length} 节{readCount === topics.length ? " · 全部读完了" : ""}</span>
            <progress aria-label="指南阅读进度" value={readCount} max={topics.length} />
            <button type="button" className={styles.quietButton} onClick={restart}>重新开始</button>
          </div>
          <div className={styles.body}>
            <aside className={styles.directory}>
              <label className={styles.search}><span>搜索功能</span><input type="search" aria-label="搜索功能" value={query} onChange={(event) => setQuery(event.target.value)} placeholder={scope === "admin" ? "如：灰度、预算、MFA" : "如：记账、PPT、重试"} /></label>
              {query.trim() && <p className={styles.searchCount} role="status">找到 {matches.length} 节 <button type="button" className={styles.quietButton} onClick={() => setQuery("")}>清除搜索</button></p>}
              <nav ref={directoryRef} className={styles.topicNav} aria-label={`${scopeLabel}指南目录`}>
                {groups.map((group) => <div className={styles.topicGroup} key={group}><h3>{group}</h3>{matches.filter((topic) => topic.group === group).map((topic) => <button key={topic.id} type="button" aria-current={selected?.id === topic.id ? "step" : undefined} onClick={() => selectTopic(topic.id)}><span className={styles.readDot} data-read={progress.read.includes(topic.id)} aria-label={progress.read.includes(topic.id) ? "已读" : "未读"}>{progress.read.includes(topic.id) ? "✓" : "○"}</span><span>{topic.title}</span></button>)}</div>)}
              </nav>
            </aside>
            {selected ? <article className={styles.article} ref={articleRef} aria-labelledby={topicTitleID}>
              <span className={styles.chapter}>{selected.group} · {index + 1} / {matches.length}{query.trim() ? " 搜索结果" : ""}</span>
              <h3 id={topicTitleID} ref={headingRef} tabIndex={-1}>{selected.title}</h3>
              <p className={styles.summary}>{selected.summary}</p>
              <div className={styles.path}><span>功能位置</span><strong>{selected.path}</strong></div>
              <ol className={styles.steps}>{selected.steps.map((step, stepIndex) => <li key={step}><span aria-hidden="true">{stepIndex + 1}</span><p>{step}</p></li>)}</ol>
              {selected.example && <div className={styles.example}><strong>试试这样填写或提问</strong><p>{selected.example}</p></div>}
              <div className={styles.tip}><strong>使用提示</strong><p>{selected.tip}</p></div>
              {selected.target && selected.action && (onNavigate
                ? <button type="button" className={styles.navigateButton} onClick={() => { closeGuide(); onNavigate(selected.target!); }}>{selected.action}<span aria-hidden="true"> →</span></button>
                : <p className={styles.loginHint}>登录后可前往对应功能，当前可以先阅读全部指南。</p>)}
            </article> : <div className={styles.noResults}><strong>没有找到相关功能</strong><p>试试更短的关键词，例如「{scope === "admin" ? "日志" : "文档"}」。</p><button type="button" onClick={() => setQuery("")}>查看全部功能</button></div>}
          </div>
          <footer className={styles.footer}>
            <div className={styles.footerNote}><button type="button" className={styles.quietButton} onClick={closeGuide}>暂时跳过</button><span>{storageUnavailable ? "当前无法保存进度，仍可正常阅读" : "进度保存在当前浏览器，可随时重开"}</span></div>
            <div className={styles.footerActions}>
              <button type="button" disabled={index <= 0} onClick={() => selectTopic(matches[index - 1].id)}>上一节</button>
              <button type="button" disabled={!selected || progress.read.includes(selected.id)} onClick={markRead}>{selected && progress.read.includes(selected.id) ? "已读" : "标记已读"}</button>
              <button type="button" className={styles.nextButton} disabled={!selected} onClick={nextTopic}>{index === matches.length - 1 ? "读完并关闭" : "下一节"}</button>
            </div>
          </footer>
        </div>
      </dialog>, document.body,
    )}
  </>;
}
