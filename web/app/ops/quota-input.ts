export const quotaFields = [
  { key: "documents", label: "活跃文档数", cost: false },
  { key: "skill_runs_per_month", label: "每月 Skill 运行次数", cost: false },
  { key: "workspaces", label: "工作区数量", cost: false },
  { key: "agent_runs_per_month", label: "每月 Agent 运行次数", cost: false },
  { key: "model_cost_micros_monthly", label: "每月模型成本（美元）", cost: true },
] as const;

export type QuotaKey = typeof quotaFields[number]["key"];
export type QuotaLimits = Record<QuotaKey, number | null>;
export type QuotaDraft = Record<QuotaKey, { mode: "inherit" | "unlimited" | "custom"; value: string }>;

export function quotaDraft(limits?: QuotaLimits): QuotaDraft {
  return Object.fromEntries(quotaFields.map(({ key, cost }) => {
    const value = limits?.[key];
    const fraction = value == null ? 0 : value % 1_000_000;
    return [key, { mode: value == null ? "inherit" : value === -1 ? "unlimited" : "custom", value: value == null || value < 0 ? "" : cost ? `${(value - fraction) / 1_000_000}.${String(fraction).padStart(6, "0")}` : String(value) }];
  })) as QuotaDraft;
}

export function quotaLimits(draft: QuotaDraft): QuotaLimits {
  return Object.fromEntries(quotaFields.map(({ key, label, cost }) => {
    const field = draft[key];
    if (field.mode === "inherit") return [key, null];
    if (field.mode === "unlimited") return [key, -1];
    const text = field.value.trim();
    if (!(cost ? /^\d+(?:\.\d{1,6})?$/ : /^\d+$/).test(text)) throw new Error(`${label}须填写${cost ? "非负金额，最多 6 位小数" : "非负整数"}`);
    const [whole, fraction = ""] = text.split(".");
    const value = cost ? Number(whole) * 1_000_000 + Number(fraction.padEnd(6, "0")) : Number(whole);
    if (!Number.isSafeInteger(value)) throw new Error(`${label}超出支持范围`);
    return [key, value];
  })) as QuotaLimits;
}
