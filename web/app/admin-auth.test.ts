import assert from "node:assert/strict";
import test from "node:test";
import { GET, POST } from "./api/admin/[...path]/route.ts";
import { adminFetch } from "./ops/admin-auth.ts";

test("admin proxy forwards only admin cookies and configured API paths", async (t) => {
  const original = process.env.ADMIN_API_BASE_URL;
  process.env.ADMIN_API_BASE_URL = "http://internal-api:8080";
  t.after(() => { if (original === undefined) delete process.env.ADMIN_API_BASE_URL; else process.env.ADMIN_API_BASE_URL = original; });
  t.mock.method(globalThis, "fetch", async (url: URL, init: RequestInit) => {
    assert.equal(url.href, "http://internal-api:8080/v1/ops/auth/logout");
    const headers = new Headers(init.headers);
    assert.equal(headers.get("authorization"), null);
    assert.equal(headers.get("cookie"), "__Host-ai_admin=session");
    assert.equal(headers.get("origin"), "https://console.example.com");
    assert.equal(headers.get("x-admin-csrf"), "1");
    assert.equal(init.redirect, "manual");
    const output = new Headers();
    output.append("set-cookie", "__Host-ai_admin=; Path=/; Secure; HttpOnly; Max-Age=0");
    output.append("set-cookie", "user_session=must-not-forward");
    return new Response(null, { status: 204, headers: output });
  });
  const response = await POST(new Request("https://console.example.com/api/admin/auth/logout", { method: "POST", headers: { Cookie: "user_session=user-secret; __Host-ai_admin=session", Authorization: "Bearer long-lived-token", Origin: "https://console.example.com", "X-Admin-CSRF": "1" } }), { params: Promise.resolve({ path: ["auth", "logout"] }) });
  assert.equal(response.status, 204);
  assert.equal(response.headers.getSetCookie().length, 1);
  assert.equal(response.headers.get("cache-control"), "no-store");
});

test("admin proxy rejects traversal and oversized streaming bodies before forwarding", async (t) => {
  const fetch = t.mock.method(globalThis, "fetch", async () => { throw new Error("must not forward"); });
  const pathResponse = await GET(new Request("https://console.example.com/api/admin/anything"), { params: Promise.resolve({ path: ["..", "users"] }) });
  assert.equal(pathResponse.status, 400);
  const response = await POST(new Request("https://console.example.com/api/admin/auth/logout", { method: "POST", body: "x".repeat(2 * 1024 * 1024 + 1) }), { params: Promise.resolve({ path: ["auth", "logout"] }) });
  assert.equal(response.status, 413);
  assert.equal(fetch.mock.callCount(), 0);
});

test("all admin panel requests use first-party sessions and never carry Bearer credentials", async (t) => {
  t.mock.method(globalThis, "fetch", async (url: string, init: RequestInit) => {
    assert.equal(url, "/api/admin/agent-runs?limit=10");
    assert.equal(init.credentials, "same-origin");
    assert.equal(init.cache, "no-store");
    assert.equal(new Headers(init.headers).get("Authorization"), null);
    assert.equal(new Headers(init.headers).get("X-Admin-CSRF"), "1");
    return Response.json({ items: [] });
  });
  const response = await adminFetch("/v1/ops/agent-runs?limit=10", { headers: { Authorization: "Bearer obsolete-token" } });
  assert.equal(response.status, 200);
  await assert.rejects(adminFetch("https://untrusted.example.com/"), /无效的后台 API 路径/);
});
