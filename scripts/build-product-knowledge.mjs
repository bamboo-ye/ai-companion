// Regenerate the offline knowledge bundle from explicit, reviewed repository sources.
// Node 24+ strips TypeScript types when importing the shared UI guide.
import { createHash } from "node:crypto";
import { readFile, writeFile, mkdir } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { userGuideTopics, adminGuideTopics } from "../web/app/onboarding-content.ts";

const root = new URL("../", import.meta.url);
const digest = (value) => createHash("sha256").update(value).digest("hex");
const files = [
  ["README.zh-CN.md", "项目介绍与部署", "project"],
  ["docs/CONTEXT_MANAGEMENT.md", "上下文、记忆与 Wiki", "technical"],
  ["docs/ADMIN_PASSKEY_LOGIN.md", "管理员登录与恢复", "admin"],
  ["docs/PRODUCT_KNOWLEDGE.md", "内置项目知识库", "technical"],
];
const pages = [];
const sources = [];
const readSource = async (path) => {
  const raw = await readFile(new URL(path, root), "utf8");
  sources.push({ path, sha256: digest(raw) });
  return raw;
};

const guidePath = "web/app/onboarding-content.ts";
const guideRaw = await readSource(guidePath);
for (const [audience, topics] of [["user", userGuideTopics], ["admin", adminGuideTopics]]) {
  for (const topic of topics) {
    const body = `${topic.summary}\n\n入口：${topic.path}\n\n${topic.steps.map((step, i) => `${i + 1}. ${step}`).join("\n")}\n\n注意：${topic.tip}${topic.example ? `\n\n示例（不是已执行操作）：${topic.example}` : ""}`;
    pages.push({
      id: `builtin-${audience}-${topic.id}`, title: topic.title, group: topic.group,
      audience, body, source: guidePath, section: `${audience === "user" ? "用户指南" : "后台指南"} / ${topic.title}`,
      version: digest(guideRaw), links: [],
    });
  }
}

// Keep every source character (apart from section-boundary whitespace). Headings
// inside fences are code, not section boundaries. Large sections are split at
// paragraph boundaries; oversized paragraphs remain intact for paginated reads.
for (const [path, group, audience] of files) {
  const raw = await readSource(path);
  const sections = [];
  let heading = group, body = [], fence = "", stack = [];
  const flush = () => {
    if (body.join("\n").trim()) sections.push({ heading, body: body.join("\n").trim() });
    body = [];
  };
  for (const line of raw.split("\n")) {
    const marker = line.match(/^\s*(`{3,}|~{3,})/);
    if (marker) {
      if (!fence) fence = marker[1];
      else if (marker[1][0] === fence[0] && marker[1].length >= fence.length) fence = "";
    }
    const match = !fence && line.match(/^(#{1,6})\s+(.+)$/);
    if (match) {
      flush();
      stack = stack.slice(0, match[1].length - 1);
      stack[match[1].length - 1] = match[2];
      heading = stack.filter(Boolean).join(" / ");
    }
    body.push(line);
  }
  flush();
  const duplicates = new Map();
  for (const section of sections) {
    const occurrence = (duplicates.get(section.heading) ?? 0) + 1;
    duplicates.set(section.heading, occurrence);
    const blocks = section.body.split(/\n\n+/), parts = [];
    let part = "";
    for (const block of blocks) {
      if (part && [...part, ...block].length > 2000) { parts.push(part); part = ""; }
      part += (part ? "\n\n" : "") + block;
    }
    if (part) parts.push(part);
    const ids = parts.map((_, i) => `builtin-${digest(`${path}:${section.heading}:${occurrence}:${i}`).slice(0, 20)}`);
    parts.forEach((body, i) => pages.push({
      id: ids[i], title: section.heading.split(" / ").at(-1) + (parts.length > 1 ? `（${i + 1}/${parts.length}）` : ""),
      group, audience, body, source: path, section: section.heading,
      version: digest(raw), links: ids.filter((id) => id !== ids[i]),
    }));
  }
}
const bundle = { schema: "product-knowledge-v1", revision: digest(JSON.stringify(sources)), sources, pages };
const output = new URL("internal/productknowledge/content/catalog.json", root);
const encoded = JSON.stringify(bundle, null, 2) + "\n";
if (process.argv.includes("--check")) {
  if (await readFile(output, "utf8") !== encoded) throw new Error("内置知识已过期，请运行 node scripts/build-product-knowledge.mjs 并提交生成文件。");
} else {
  await mkdir(new URL("./", output), { recursive: true });
  await writeFile(output, encoded);
}
console.log(`${pages.length} builtin pages, ${sources.length} sources, revision ${bundle.revision.slice(0, 12)} (${fileURLToPath(output)})`);
