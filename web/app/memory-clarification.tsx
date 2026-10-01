"use client";

import { useState } from "react";

export type MemoryClarification = { issue_id: string; question: string; candidate_ids: string[] };
type Candidate = { id: string; content: string };

export function MemoryClarificationCard({ question, candidates, onCorrect }: {
  question: MemoryClarification; candidates: Candidate[]; onCorrect: (candidate: Candidate) => void;
}) {
  const related = candidates.filter(item => question.candidate_ids.includes(item.id));
  if (related.length < 2) return null;
  return <aside className="evidenceReview" aria-label="记忆澄清">
    <strong>有一处需要你确认</strong><p>{question.question}</p>
    {related.map(item => <div key={item.id}>
      <p>{item.content}</p>
      <button type="button" onClick={() => onCorrect(item)} aria-label={`纠正记录：${item.content}`}>纠正这条记录</button>
    </div>)}
    <small>确认前，两条记录都会保留。请补充适用时间或情境，再保存你的纠正。</small>
  </aside>;
}

export function MemoryCorrectionEditor({ value, onChange, onSave, onCancel }: {
  value: string; onChange: (value: string) => void; onSave: (value: string) => Promise<void> | void; onCancel: () => void;
}) {
  const [saving, setSaving] = useState(false);
  return <form className="memoryEdit" onSubmit={async event => {
    event.preventDefault();
    const content = value.trim();
    if (saving || content.length < 2 || content.length > 2000) return;
    setSaving(true);
    try { await onSave(content); } finally { setSaving(false); }
  }}>
    <input aria-label="纠正记忆内容" minLength={2} maxLength={2000} required autoFocus value={value} disabled={saving} onChange={event => onChange(event.target.value)}/>
    <button type="submit" disabled={saving || value.trim().length < 2}>{saving ? "保存中…" : "保存纠正"}</button>
    <button type="button" disabled={saving} onClick={onCancel}>取消</button>
  </form>;
}
