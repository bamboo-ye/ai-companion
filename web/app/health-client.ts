const apiBase = process.env.NEXT_PUBLIC_API_BASE_URL ?? "http://localhost:8080";
export type HealthRequest = (path: string, options?: RequestInit) => Promise<Response>;

export function healthClient(token: string) {
  const request: HealthRequest = async (path, options = {}) => {
    const headers = new Headers(options.headers);
    headers.set("Authorization", `Bearer ${token}`);
    if (options.body && !(options.body instanceof FormData)) headers.set("Content-Type", "application/json");
    const response = await fetch(`${apiBase}/v1/health${path}`, {
      ...options, headers, cache: "no-store", credentials: "omit", redirect: "error",
    });
    if (!response.ok) {
      const body = await response.json().catch(() => ({})) as { message?: string; error?: { message?: string } };
      throw new Error(body.error?.message || body.message || "暂时无法连接健康助手，请稍后重试。");
    }
    return response;
  };
  async function api<T>(path: string, options?: RequestInit): Promise<T> {
    const response = await request(path, options);
    return response.status === 204 ? undefined as T : response.json();
  }
  const post = <T>(path: string, body: unknown = {}, signal?: AbortSignal) =>
    api<T>(path, { method: "POST", body: JSON.stringify(body), signal });
  return { request, api, post };
}

export async function followHealthRun(
  request: HealthRequest,
  runID: string,
  signal: AbortSignal,
  onEvent: (event: string, value: Record<string, unknown>) => void,
) {
  let lastID = "";
  const path = `/runs/${encodeURIComponent(runID)}`;
  for (let attempt = 0; attempt < 3; attempt++) {
    try {
      const response = await request(`${path}/events${lastID ? `?after=${encodeURIComponent(lastID)}` : ""}`, { signal });
      const reader = response.body?.pipeThrough(new TextDecoderStream()).getReader();
      if (!reader) throw new Error("浏览器不支持流式响应。");
      try {
        let buffer = "";
        while (true) {
          const { done, value } = await reader.read();
          if (done) break;
          buffer += value;
          const frames = buffer.split(/\r?\n\r?\n/);
          buffer = frames.pop() ?? "";
          for (const frame of frames) {
            const id = frame.match(/^id: (\d+)$/m)?.[1];
            if (id) lastID = id;
            const event = frame.match(/^event: ([\w-]+)$/m)?.[1];
            const data = frame.match(/^data: (.*)$/m)?.[1];
            if (event === "end") return;
            if (event && data) onEvent(event, JSON.parse(data));
          }
        }
      } finally {
        await reader.cancel().catch(() => {});
        reader.releaseLock();
      }
    } catch (error) {
      if (signal.aborted) throw error;
    }

    try {
      const response = await request(path, { signal });
      const run = await response.json() as { status?: string };
      if (["succeeded", "failed", "cancelled", "waiting_user"].includes(run.status ?? "")) return;
    } catch (error) {
      if (signal.aborted) throw error;
    }
    if (attempt < 2) await new Promise(resolve => setTimeout(resolve, 700 * (attempt + 1)));
    signal.throwIfAborted();
  }
  throw new Error("连接中断，消息已保留。重新打开健康助手可查看结果。");
}

export function safeTimezone(value: unknown): string {
  if (typeof value === "string" && value) {
    try { return new Intl.DateTimeFormat("zh-CN", { timeZone: value }).resolvedOptions().timeZone; } catch {}
  }
  return "Asia/Shanghai";
}

export function dateLabel(value: string) {
  const date = new Date(value);
  return Number.isNaN(date.valueOf()) ? value : new Intl.DateTimeFormat("zh-CN", {
    month: "short", day: "numeric", hour: "2-digit", minute: "2-digit",
  }).format(date);
}

export function displayMetric(value: unknown) {
  return value == null ? "—" : String(value);
}

export function safeSource(value: string) {
  try { return new URL(value).protocol === "https:" ? value : undefined; } catch { return undefined; }
}
