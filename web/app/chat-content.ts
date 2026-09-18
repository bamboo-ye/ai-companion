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

// Only turn internal product citations into links; arbitrary model text remains
// escaped text. In particular javascript: and external URLs are never parsed.
export function productKnowledgeSegments(content: string): { text: string; href?: string }[] {
  const text = visibleChatText(content);
  const pattern = /\[([^\]\n]{1,160})\]\(\/?#knowledge=(builtin-[a-z0-9-]{1,72})\)/g;
  const segments: { text: string; href?: string }[] = [];
  let offset = 0;
  for (const match of text.matchAll(pattern)) {
    if (match.index > offset) segments.push({ text: text.slice(offset, match.index) });
    segments.push({ text: match[1], href: `/#knowledge=${match[2]}` });
    offset = match.index + match[0].length;
  }
  if (offset < text.length) segments.push({ text: text.slice(offset) });
  return segments;
}
