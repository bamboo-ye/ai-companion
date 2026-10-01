"use client";

import { useEffect, useState } from "react";
import type { GeneratedFile } from "./skill-run-client";

const apiBase = process.env.NEXT_PUBLIC_API_BASE_URL ?? "http://localhost:8080";

export function PresentationDownload({ token, runID, file, label: customLabel, autoPrepare = true }: { token: string; runID: string; file: Pick<GeneratedFile, "id" | "name">; label?: string; autoPrepare?: boolean }) {
  const [url, setURL] = useState("");
  const [error, setError] = useState(false);
  const [attempt, setAttempt] = useState(0);
  const [requested, setRequested] = useState(autoPrepare);
  const label = customLabel ?? (file.name.endsWith(".pptx") ? "PPTX" : file.name.endsWith(".pdf") ? "PDF" : file.name.endsWith(".png") ? "高清 PNG" : "设计稿");
  useEffect(() => {
    if (!requested) return;
    const controller = new AbortController();
    let objectURL = "";
    void fetch(`${apiBase}/v1/skill-runs/${runID}/files/${file.id}`, { signal: controller.signal, headers: { Authorization: `Bearer ${token}` } })
      .then(async response => {
        if (!response.ok) throw new Error("文件下载失败");
        const blob = await response.blob();
        if (controller.signal.aborted) return;
        objectURL = URL.createObjectURL(blob);
        setURL(objectURL);
      }).catch(() => { if (!controller.signal.aborted) setError(true); });
    return () => {
      controller.abort();
      if (objectURL) setTimeout(() => URL.revokeObjectURL(objectURL), 30_000);
    };
  }, [token, runID, file.id, attempt, requested]);
  // A real link keeps the download inside the user's gesture and remains
  // available to the browser's save-link menu after the authenticated fetch.
  if (url) return <a href={url} download={file.name}>下载 {label}</a>;
  return <button disabled={requested && !error} onClick={() => { setRequested(true); setError(false); setAttempt(n => n + 1); }}>{error ? `重试下载 ${label}` : requested ? `准备 ${label}…` : `准备下载 ${label}`}</button>;
}
