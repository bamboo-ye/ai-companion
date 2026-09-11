import type { GuideScope } from "./onboarding-content.ts";

export type GuideProgress = { dismissed: boolean; read: string[]; lastTopic: string };

export function guideStorageKey(scope: GuideScope) {
  return `ai-companion-onboarding:${scope}:v1`;
}

export function parseGuideProgress(raw: string | null, topicIDs: readonly string[]): GuideProgress {
  const empty: GuideProgress = { dismissed: false, read: [], lastTopic: topicIDs[0] ?? "" };
  if (!raw) return empty;
  try {
    const value: unknown = JSON.parse(raw);
    if (!value || typeof value !== "object" || Array.isArray(value)) return empty;
    const saved = value as Record<string, unknown>;
    return {
      dismissed: saved.dismissed === true,
      read: Array.isArray(saved.read) ? [...new Set(saved.read.filter((id): id is string => typeof id === "string" && topicIDs.includes(id)))] : [],
      lastTopic: typeof saved.lastTopic === "string" && topicIDs.includes(saved.lastTopic) ? saved.lastTopic : empty.lastTopic,
    };
  } catch {
    return empty;
  }
}
