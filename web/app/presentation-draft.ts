import type { Deck } from "./presentation-model";

export type PresentationDraft = { deck: Deck; input: Record<string, unknown>; savedAt?: number };
const databaseName = "ai-companion-presentation";
const storeName = "drafts";

function draftDatabase(): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    const request = indexedDB.open(databaseName, 1);
    let settled = false;
    const fail = () => { settled = true; clearTimeout(timer); reject(new Error("无法打开草稿存储")); };
    const timer = setTimeout(fail, 5000);
    request.onupgradeneeded = () => {
      if (!request.result.objectStoreNames.contains(storeName)) request.result.createObjectStore(storeName);
    };
    request.onerror = fail;
    request.onblocked = fail;
    request.onsuccess = () => {
      clearTimeout(timer);
      if (settled) { request.result.close(); return; }
      settled = true;
      request.result.onversionchange = () => request.result.close();
      resolve(request.result);
    };
  });
}

async function transact(key: string, draft?: PresentationDraft): Promise<PresentationDraft | null> {
  const database = await draftDatabase();
  try {
    return await new Promise((resolve, reject) => {
      const transaction = database.transaction(storeName, draft ? "readwrite" : "readonly");
      const store = transaction.objectStore(storeName);
      const request = draft ? store.put(draft, key) : store.get(key);
      transaction.oncomplete = () => resolve(draft ?? request.result ?? null);
      transaction.onabort = () => reject(transaction.error ?? new Error("草稿保存失败"));
      transaction.onerror = () => reject(transaction.error ?? new Error("草稿存储不可用"));
    });
  } finally { database.close(); }
}

export async function loadPresentationDraft(key: string): Promise<PresentationDraft | null> {
  let stored: PresentationDraft | null = null;
  let legacy: PresentationDraft | null = null;
  try { stored = await transact(key); } catch { /* Fall back when IndexedDB is unavailable. */ }
  try { legacy = JSON.parse(localStorage.getItem(key) || "null") as PresentationDraft | null; } catch { /* A malformed old draft must not hide a valid new one. */ }
  // A later localStorage fallback must win over an older IndexedDB draft.
  if (legacy && (!stored || (legacy.savedAt ?? 0) > (stored.savedAt ?? 0))) return legacy;
  return stored;
}

export async function savePresentationDraft(key: string, value: PresentationDraft): Promise<void> {
  const draft = { ...value, savedAt: Date.now() };
  try { await transact(key, draft); }
  catch { localStorage.setItem(key, JSON.stringify(draft)); return; }
  // Keep old browser drafts readable until the new transaction has committed.
  try { localStorage.removeItem(key); } catch { /* Saving already succeeded. */ }
}
