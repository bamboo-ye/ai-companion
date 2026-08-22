const internalMetadataPattern = /<!--\s*ai-(?:document|generated-file|ledger-export|skill-run|agent-run|generation-job):[\s\S]*?-->/g;

export function visibleChatText(content: string) {
  return content.replace(internalMetadataPattern, "").trim();
}

export function confirmationPrompt(summary: string) {
  const visibleSummary = visibleChatText(summary);
  return `确认执行以下更新吗？\n\n${visibleSummary || "请核对此次更新。"}`;
}

export function executionResultNotice(response?: string) {
  const visibleResponse = visibleChatText(response ?? "");
  return visibleResponse ? `执行结果：${visibleResponse}` : "";
}
