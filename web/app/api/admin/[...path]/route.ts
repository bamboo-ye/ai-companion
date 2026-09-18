// Keep administrator cookies first-party, including deployments where the
// public chat API is hosted on a different site. Upstream is server-configured.
export const runtime = "nodejs";
export const dynamic = "force-dynamic";

type RouteContext = { params: Promise<{ path: string[] }> };
const adminCookie = /^(?:__Host-)?ai_admin(?:_login|_register)?=/;

async function proxy(request: Request, context: RouteContext): Promise<Response> {
  const { path } = await context.params;
  if (!path.length || path.some((part) => part === "." || part === ".." || /[\\/]/.test(part))) {
    return Response.json({ message: "无效的后台路径" }, { status: 400 });
  }
  const base = process.env.ADMIN_API_BASE_URL ?? "http://localhost:8080";
  const url = new URL(`/v1/ops/${path.map(encodeURIComponent).join("/")}`, base);
  url.search = new URL(request.url).search;
  const headers = new Headers();
  for (const name of ["content-type", "origin", "x-admin-csrf", "idempotency-key"]) {
    const value = request.headers.get(name);
    if (value) headers.set(name, value);
  }
  const cookies = (request.headers.get("cookie") ?? "").split(";").map((part) => part.trim()).filter((part) => adminCookie.test(part));
  if (cookies.length) headers.set("cookie", cookies.join("; "));
  let body: ArrayBuffer | undefined;
  if (request.method !== "GET" && request.method !== "HEAD") {
    const limit = 2 * 1024 * 1024;
    if (Number(request.headers.get("content-length")) > limit) return Response.json({ message: "请求过大" }, { status: 413 });
    const reader = request.body?.getReader();
    const chunks: Uint8Array[] = [];
    let length = 0;
    if (reader) {
      while (true) {
        const { done, value } = await reader.read();
        if (done) break;
        length += value.byteLength;
        if (length > limit) { await reader.cancel(); return Response.json({ message: "请求过大" }, { status: 413 }); }
        chunks.push(value);
      }
    }
    const bytes = new Uint8Array(length);
    let offset = 0;
    for (const chunk of chunks) { bytes.set(chunk, offset); offset += chunk.byteLength; }
    body = bytes.buffer;
  }
  try {
    const upstream = await fetch(url, { method: request.method, headers, body, cache: "no-store", redirect: "manual", signal: AbortSignal.timeout(30_000) });
    const output = new Headers({ "Cache-Control": "no-store", "X-Content-Type-Options": "nosniff", "Referrer-Policy": "no-referrer" });
    for (const name of ["content-type", "content-disposition", "retry-after"]) {
      const value = upstream.headers.get(name); if (value) output.set(name, value);
    }
    for (const cookie of upstream.headers.getSetCookie()) if (adminCookie.test(cookie)) output.append("set-cookie", cookie);
    return new Response(upstream.body, { status: upstream.status, headers: output });
  } catch {
    return Response.json({ message: "后台 API 暂时无法连接" }, { status: 502, headers: { "Cache-Control": "no-store" } });
  }
}

export { proxy as GET, proxy as POST, proxy as PUT, proxy as PATCH, proxy as DELETE, proxy as HEAD };
