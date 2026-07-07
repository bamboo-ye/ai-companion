import type { Metadata, Viewport } from "next";
import type { ReactNode } from "react";

import { PWARegistration } from "./pwa-registration";
import "./styles.css";

export const metadata: Metadata = {
  title: "伴AI",
  description: "懂陪伴，也能一起把事情做好。",
  applicationName: "伴AI",
  appleWebApp: {
    capable: true,
    title: "伴AI",
    statusBarStyle: "default",
  },
};

export const viewport: Viewport = {
  themeColor: "#e9638a",
};

export default function RootLayout({ children }: Readonly<{ children: ReactNode }>) {
  return (
    <html lang="zh-CN">
      <body>
        <PWARegistration />
        {children}
      </body>
    </html>
  );
}
