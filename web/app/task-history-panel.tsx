"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import {
  actOnSkillRun, confirmationPreview, downloadSkillFile, isActiveRun,
  listSkillRuns, skillName, SkillRun, statusLabel,
} from "./skill-run-client";

type StatusFilter = "all" | "active" | "failed" | "succeeded";

export function TaskHistoryPanel({ token, onClose }: { token: string; onClose: () => void }) {
  const [runs, setRuns] = useState<SkillRun[]>([]);
  const [filter, setFilter] = useState<StatusFilter>("all");
  const [notice, setNotice] = useState("");
  const [busy, setBusy] = useState("");

  const refresh = useCallback(async () => {
    setRuns(await listSkillRuns(token, { limit: 100 }));
  }, [token]);

  useEffect(() => {
    let active = true;
    const load = async () => {
      try { const items = await listSkillRuns(token, { limit: 100 }); if (active) setRuns(items); }
      catch (error) { if (active) setNotice(error instanceof Error ? error.message : "无法读取任务历史"); }
    };
    void load();
    const timer = window.setInterval(() => { if (active) void load(); }, 2500);
    return () => { active = false; window.clearInterval(timer); };
  }, [token]);

  const visibleRuns = useMemo(() => runs.filter((run) => {
    if (filter === "active") return isActiveRun(run);
    if (filter === "failed") return run.status === "failed" || run.status === "cancelled";
    if (filter === "succeeded") return run.status === "succeeded";
    return true;
  }), [filter, runs]);

  async function act(run: SkillRun, action: "confirm" | "cancel" | "retry") {
    setBusy(run.id); setNotice("");
    try {
      const result = await actOnSkillRun(token, run.id, action);
      setRuns((current) => current.map((item) => item.id === result.id ? result : item));
      setNotice(action === "retry" ? "已提交重试；最终结果会同步发送到原聊天。" : action === "confirm" ? "任务已确认。" : "任务已取消。");
      window.dispatchEvent(new CustomEvent("ai-companion-skill-runs-updated", { detail: { runID: run.id } }));
      await refresh();
    } catch (error) { setNotice(error instanceof Error ? error.message : "任务操作失败"); }
    finally { setBusy(""); }
  }

  async function download(run: SkillRun, file: SkillRun["files"][number]) {
    setBusy(file.id); setNotice("");
    try { await downloadSkillFile(token, run, file); }
    catch (error) { setNotice(error instanceof Error ? error.message : "文件下载失败"); }
    finally { setBusy(""); }
  }

  return <section className="workPanel taskHistoryPanel" aria-labelledby="task-history-title">
    <header><div><h3 id="task-history-title">历史任务</h3><small>统一查看任务状态、结果与重试记录</small></div><button className="textButton" type="button" onClick={onClose}>返回</button></header>
    {notice && <p className="workNotice" role="status">{notice}</p>}
    <div className="taskFilters" aria-label="筛选任务">
      {(["all", "active", "failed", "succeeded"] as StatusFilter[]).map((value) => <button key={value} type="button" className={filter === value ? "active" : ""} onClick={() => setFilter(value)}>{({ all: "全部", active: "进行中", failed: "失败/取消", succeeded: "已完成" })[value]}</button>)}
      <span>{visibleRuns.length} 个任务</span>
    </div>
    <TaskRunList runs={visibleRuns} busy={busy} onAct={act} onDownload={download} />
  </section>;
}

function TaskRunList({ runs, busy, onAct, onDownload }: {
  runs: SkillRun[]; busy: string;
  onAct: (run: SkillRun, action: "confirm" | "cancel" | "retry") => void;
  onDownload: (run: SkillRun, file: SkillRun["files"][number]) => void;
}) {
  if (runs.length === 0) return <p className="emptyState">当前筛选下没有任务。</p>;
  return <div className="runBoard"><div className="runList">{runs.map((run) => <article key={run.id}>
    <div className="runHeadline"><span><strong>{skillName(run.skill_name)}</strong><small>第 {run.attempt} 次 · {run.current_state} · {formatTaskTime(run.updated_at)}</small></span><b className={`runStatus ${run.status}`}>{statusLabel(run.status)}</b></div>
    {run.status === "waiting_confirmation" && <><small>请核对以下执行参数：</small><pre>{JSON.stringify(confirmationPreview(run.input), null, 2)}</pre></>}
    {run.output && <pre>{JSON.stringify(run.output, null, 2)}</pre>}
    {run.error_message && <p className="runError">{run.error_message}</p>}
    <div className="stepLine">{(run.steps ?? []).map((step) => <span key={step.id} className={step.status}>{step.sequence}. {step.state}</span>)}</div>
    <div className="runActions">
      {run.status === "waiting_confirmation" && <><button disabled={busy !== ""} onClick={() => onAct(run, "confirm")}>确认执行</button><button disabled={busy !== ""} onClick={() => onAct(run, "cancel")}>取消</button></>}
      {(run.status === "failed" || run.status === "cancelled") && <button disabled={busy !== ""} onClick={() => onAct(run, "retry")}>重试</button>}
      {(run.files ?? []).map((file) => <button key={file.id} disabled={busy !== ""} onClick={() => onDownload(run, file)}>下载 {file.name}</button>)}
    </div>
  </article>)}</div></div>;
}

function formatTaskTime(value: string) {
  return new Date(value).toLocaleString("zh-CN", { month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit" });
}
