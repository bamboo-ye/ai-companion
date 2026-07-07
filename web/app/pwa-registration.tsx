"use client";

import { useEffect, useState } from "react";

export function PWARegistration() {
  const [ready, setReady] = useState(false);

  useEffect(() => {
    if (!("serviceWorker" in navigator) || process.env.NODE_ENV !== "production") return;
    let cancelled = false;
    window.addEventListener("load", () => {
      void navigator.serviceWorker.register("/sw.js").then(() => {
        if (!cancelled) setReady(true);
      }).catch(() => {
        if (!cancelled) setReady(false);
      });
    }, { once: true });
    return () => {
      cancelled = true;
    };
  }, []);

  return ready ? <span className="pwaStatus" aria-label="PWA ready">可离线打开</span> : null;
}
