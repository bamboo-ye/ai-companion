export type EvidenceReview = {
  status: "resolved" | "partially_resolved" | "unresolved";
  total_issues: number;
  resolved_issues: number;
  rounds: number;
  additional_reads: number;
  isolation_passed: boolean;
  sources_verified: boolean;
  termination_reason: string;
};

// Treat persisted/API summaries as untrusted data, including older deployments.
export function parseEvidenceReview(value: unknown): EvidenceReview | null {
  if (!value || typeof value !== "object") return null;
  const item = value as Record<string, unknown>;
  if (!["resolved", "partially_resolved", "unresolved"].includes(String(item.status))) return null;
  for (const [key, maximum] of [["total_issues", 12], ["resolved_issues", 12], ["rounds", 2], ["additional_reads", 2]] as const) {
    if (!Number.isInteger(item[key]) || Number(item[key]) < 0 || Number(item[key]) > maximum) return null;
  }
  if (!item.total_issues || Number(item.resolved_issues) > Number(item.total_issues)
    || typeof item.isolation_passed !== "boolean" || typeof item.sources_verified !== "boolean"
    || typeof item.termination_reason !== "string") return null;
  const review: EvidenceReview = {
    status: item.status as EvidenceReview["status"], total_issues: Number(item.total_issues),
    resolved_issues: Number(item.resolved_issues), rounds: Number(item.rounds),
    additional_reads: Number(item.additional_reads), isolation_passed: item.isolation_passed,
    sources_verified: item.sources_verified, termination_reason: item.termination_reason,
  };
  if (["evidence_access_changed", "source_version_changed", "snapshot_changed"].includes(review.termination_reason)) {
    review.sources_verified = false;
    review.termination_reason = "evidence_access_changed";
  }
  if (review.termination_reason === "independence_failed") review.isolation_passed = false;
  if (!review.isolation_passed || !review.sources_verified) review.resolved_issues = 0;
  review.status = review.resolved_issues === review.total_issues ? "resolved" : review.resolved_issues ? "partially_resolved" : "unresolved";
  if (review.termination_reason === "resolved" && review.status !== "resolved") review.termination_reason = "verification_incomplete";
  return review;
}

type ReviewMessage = { role: string; content: string; id: string };
export function evidenceReviewRunID(message: ReviewMessage): string | null {
  if (message.role !== "assistant") return null;
  return message.content.match(/<!--ai-agent-run:([a-zA-Z0-9_-]{1,128})\|(?:completed|failed|timed_out|cancelled)\|\d+-->\s*$/)?.[1] ?? null;
}

export function latestEvidenceReviewRun(messages: ReviewMessage[]): {runID: string; messageID: string} | null {
  // Never associate an older answer's verification with a new user request.
  const message = messages.at(-1);
  const runID = message && evidenceReviewRunID(message);
  return runID ? {runID, messageID: message.id} : null;
}

export async function restoreEvidenceReview(
  messages: ReviewMessage[], conversationID: string,
  loadRun: (runID: string) => Promise<unknown>,
): Promise<{runID: string; review: EvidenceReview} | null> {
  const target = latestEvidenceReviewRun(messages);
  if (!target) return null;
  const raw = await loadRun(target.runID);
  if (!raw || typeof raw !== "object") return null;
  const run = raw as Record<string, unknown>;
  if (run.id !== target.runID || run.conversation_id !== conversationID
    || !["completed", "failed", "timed_out", "cancelled"].includes(String(run.status))) return null;
  const output = run.output as Record<string, unknown> | undefined;
  const review = parseEvidenceReview(output?.evidence_review);
  return review ? {runID: target.runID, review} : null;
}

export function evidenceReviewLabel(review: EvidenceReview): string {
  if (review.termination_reason === "evidence_access_changed") return "资料已变化，需要重新核验";
  if (!review.sources_verified || !review.isolation_passed) return "依据不足，已保留分歧";
  if (review.status === "resolved") return "资料核验完成";
  if (review.status === "partially_resolved") return "部分问题仍待核对";
  return "依据不足，已保留分歧";
}

export function evidenceReviewReason(reason: string): string {
  const labels: Record<string, string> = {
    resolved: "当前分歧已完成证据核对。",
    no_new_evidence: "没有找到能进一步区分结论的新依据。",
    evidence_unavailable: "现有资料不足以确定结论。",
    evidence_access_changed: "来源内容或访问权限已变化，请重新发起核验。",
    deadline_exceeded: "已达到核验时间上限，未解决的问题继续保留。",
    round_limit: "已完成本次核验，仍有问题缺少充分依据。",
    independence_failed: "独立核验未通过，未采纳本次裁决。",
    debate_model_budget: "本次核验额度已用尽，未解决的问题继续保留。",
    evidence_read_failed: "补充资料读取失败，未据此确认结论。",
    input_budget_exceeded: "资料超出单次核验范围，请缩小问题范围后重试。",
    verification_incomplete: "核验记录不完整，不能据此确认结论。",
  };
  return labels[reason] ?? "核验已停止，请以回复中说明的限制为准。";
}
