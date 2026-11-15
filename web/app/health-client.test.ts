import assert from "node:assert/strict";
import test from "node:test";
import { followHealthRun, type HealthRequest } from "./health-client.ts";

test("health stream keeps UTF-8 text intact and resumes without replaying earlier events", async () => {
  const seen: unknown[] = [];
  const calls: string[] = [];
  const request: HealthRequest = async path => {
    calls.push(path);
    if (path === "/runs/test-run") return Response.json({ status: "running" });
    const text = path.includes("after=12")
      ? "id: 13\nevent: delta\ndata: {\"text\":\"康\"}\n\nevent: end\ndata: {\"status\":\"succeeded\"}\n\n"
      : "id: 12\nevent: delta\ndata: {\"text\":\"健\"}\n\n";
    const bytes = new TextEncoder().encode(text);
    const halfway = bytes.findIndex(byte => byte >= 0xe0) + 1; // split the first UTF-8 character
    return new Response(new ReadableStream({
      start(controller) {
        controller.enqueue(bytes.slice(0, halfway));
        controller.enqueue(bytes.slice(halfway));
        controller.close();
      },
    }));
  };
  await followHealthRun(request, "test-run", new AbortController().signal, (event, data) => {
    seen.push([event, data.text]);
  });
  assert.deepEqual(calls, ["/runs/test-run/events", "/runs/test-run", "/runs/test-run/events?after=12"]);
  assert.deepEqual(seen, [["delta", "健"], ["delta", "康"]]);
});
