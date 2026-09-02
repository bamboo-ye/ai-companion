const apiBase = process.env.NEXT_PUBLIC_API_BASE_URL ?? "http://localhost:8080";

export type RunStep = { id: string; sequence: number; state: string; status: string; tool_name?: string; error_message?: string };
export type GeneratedFile = { id: string; name: string; media_type: string; size_bytes: number };
export type SkillRun = {
  id: string; skill_name: string; status: string; current_state: string; attempt: number; revision: number;
  requires_confirmation: boolean; input: Record<string, unknown>; output?: Record<string, unknown>; error_message?: string;
  conversation_id?: string; origin_message_id?: string; created_at: string; updated_at: string;
  steps: RunStep[]; files: GeneratedFile[];
};

async function taskRequest<T>(token: string, path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(`${apiBase}${path}`, {
    ...init,
    headers: { "Content-Type": "application/json", Authorization: `Bearer ${token}`, ...init?.headers },
  });
  const payload = await response.json() as T & { message?: string };
  if (!response.ok) throw new Error(payload.message ?? "任务操作失败");
  return payload;
}

export async function listSkillRuns(token: string, options: { limit?: number; conversationID?: string } = {}) {
  const query = new URLSearchParams({ limit: String(options.limit ?? 50) });
  if (options.conversationID) query.set("conversation_id", options.conversationID);
  return (await taskRequest<{ items: SkillRun[] }>(token, `/v1/skill-runs?${query}`)).items;
}

export function getSkillRun(token: string, runID: string) {
  return taskRequest<SkillRun>(token, `/v1/skill-runs/${runID}`);
}

export function actOnSkillRun(token: string, runID: string, action: "confirm" | "cancel" | "retry") {
  return taskRequest<SkillRun>(token, `/v1/skill-runs/${runID}/${action}`, {
    method: "POST",
    headers: { "Idempotency-Key": crypto.randomUUID() },
    body: "{}",
  });
}

export async function downloadSkillFile(token: string, run: SkillRun, file: GeneratedFile) {
  const response = await fetch(`${apiBase}/v1/skill-runs/${run.id}/files/${file.id}`, { headers: { Authorization: `Bearer ${token}` } });
  if (!response.ok) throw new Error("文件下载失败");
  const url = URL.createObjectURL(await response.blob());
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = file.name;
  document.body.appendChild(anchor);
  anchor.click();
  anchor.remove();
  URL.revokeObjectURL(url);
}

export function skillName(name: string) { return ({
  "office.translate": "翻译", "office.email_draft": "邮件草稿", "office.markdown_document": "Markdown 文档",
  "office.docx_edit": "DOCX 副本编辑", "office.pptx_outline": "PPTX 大纲", "office.pptx_generate": "PPTX 生成",
  "office.tabular_profile": "CSV/XLSX 分析", "office.document_extract": "附件正文提取", "office.pdf_translate": "PDF 多模态翻译",
} as Record<string, string>)[name] ?? name; }

export function statusLabel(status: string) { return ({
  waiting_confirmation: "待确认", queued: "排队中", running: "执行中", succeeded: "已完成", failed: "失败", cancelled: "已取消",
} as Record<string, string>)[status] ?? status; }

export function confirmationPreview(input: Record<string, unknown>) {
  return Object.fromEntries(Object.entries(input).map(([key, value]) => [key, key.endsWith("_base64") ? "[源文件已附加，内容不在确认卡展示]" : value]));
}

export function isActiveRun(run: SkillRun) {
  return run.status === "queued" || run.status === "running" || run.status === "waiting_confirmation";
}
