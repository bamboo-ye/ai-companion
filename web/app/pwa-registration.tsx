"use client";

import { useEffect, useState } from "react";

export function PWARegistration() {
  const [ready, setReady] = useState(false);

  useEffect(() => {
    if (!("serviceWorker" in navigator) || process.env.NODE_ENV !== "production") return;

    let active = true;
    const register = async () => {
      try {
        await navigator.serviceWorker.register("/sw.js", { scope: "/" });
        await navigator.serviceWorker.ready;
        if (active) setReady(true);
      } catch {
        if (active) setReady(false);
      }
    };

    if (document.readyState === "complete") {
      void register();
    } else {
      window.addEventListener("load", register, { once: true });
    }

    return () => {
      active = false;
      window.removeEventListener("load", register);
    };
  }, []);

  return ready ? <span className="pwaStatus" aria-label="PWA ready">可离线打开</span> : null;
}
