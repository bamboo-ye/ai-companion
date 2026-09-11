import type { Metadata } from "next";
import type { ReactNode } from "react";

export const metadata: Metadata = {
  title: "Agent Control · 伴AI",
  description: "伴AI Agent 运行观测与运维控制台",
};

export default function OperationsLayout({ children }: Readonly<{ children: ReactNode }>) {
  return children;
}
