import assert from "node:assert/strict";
import test from "node:test";
import { loadPresentationDraft, savePresentationDraft } from "./presentation-draft.ts";
import type { PresentationDraft } from "./presentation-draft.ts";

test("browser drafts remain recoverable when IndexedDB is unavailable", async (t) => {
  const values = new Map<string, string>();
  const original = Object.getOwnPropertyDescriptor(globalThis, "localStorage");
  const storage = { getItem: (key: string) => values.get(key) ?? null, setItem: (key: string, value: string) => values.set(key, value), removeItem: (key: string) => values.delete(key) };
  Object.defineProperty(globalThis, "localStorage", { configurable: true, value: storage });
  t.after(() => { if (original) Object.defineProperty(globalThis, "localStorage", original); else Reflect.deleteProperty(globalThis, "localStorage"); });
  const draft: PresentationDraft = { deck: { version: "presentation-design-v2", id: "qa", title: "恢复", audience: "验收", theme_id: "business", mode: "present", animation: "none", revision: 2, slides: [], assets: {}, brand: {} }, input: { brief: "源内容" } };
  await t.test("retains existing localStorage drafts and account isolation", async () => {
    values.set("account-a", JSON.stringify(draft));
    assert.deepEqual(await loadPresentationDraft("account-a"), draft);
    assert.equal(await loadPresentationDraft("account-b"), null);
    await savePresentationDraft("account-b", { ...draft, deck: { ...draft.deck, title: "独立草稿" } });
    assert.equal((await loadPresentationDraft("account-a"))?.deck.title, "恢复");
    assert.equal((await loadPresentationDraft("account-b"))?.deck.title, "独立草稿");
    assert.ok((await loadPresentationDraft("account-b"))?.savedAt);
  });
  await t.test("rejects failed fallback writes instead of claiming a successful save", async () => {
    storage.setItem = () => { throw new DOMException("Full", "QuotaExceededError"); };
    await assert.rejects(savePresentationDraft("full", draft), { name: "QuotaExceededError" });
  });
  await t.test("malformed legacy data cannot crash opening the workbench", async () => {
    values.set("broken", "{");
    assert.equal(await loadPresentationDraft("broken"), null);
  });
});
