import { execFile } from "node:child_process";
import { promisify } from "node:util";
import path from "node:path";

export const runtime = "nodejs";
let running = false;
export async function POST(request: Request) {
  if (process.env.NODE_ENV !== "development" || process.env.DEBATE_PREVIEW !== "true") return new Response(null, {status: 404});
  // A local inspection route is never available in a production build. Reject
  // cross-origin requests and bound concurrency; no arbitrary subprocess input.
  const origin = request.headers.get("origin");
  let sameOrigin = false;
  try {
    const parsed = new URL(origin ?? "");
    sameOrigin = parsed.protocol === "http:" && ["localhost", "127.0.0.1", "[::1]"].includes(parsed.hostname) && parsed.host === request.headers.get("host");
  } catch { /* Missing or invalid origin is rejected. */ }
  if (!sameOrigin) return new Response(null, {status: 403});
  if (running) return new Response(null, {status: 429});
  let scenario: unknown;
  try { scenario = (await request.json()).scenario; } catch { return new Response(null, {status: 400}); }
  if (typeof scenario !== "string" || !["resolved", "unresolved", "revoked", "write_denied", "off"].includes(scenario)) return new Response(null, {status: 400});
  running = true;
  try {
    const root = path.resolve(process.cwd(), "..");
    const { stdout } = await promisify(execFile)(path.join(root, "workers/python/.venv/bin/python"), ["-m", "ai_companion_worker.evaluation.debate_scenarios", scenario], {
      cwd: root, timeout: 15000, maxBuffer: 256000,
      env: {...process.env, PYTHONPATH: path.join(root, "workers/python/src")},
    });
    return Response.json(JSON.parse(stdout), {headers: {"Cache-Control": "no-store"}});
  } catch { return new Response(null, {status: 500}); }
  finally { running = false; }
}
