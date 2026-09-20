import assert from "node:assert/strict";
import { execFileSync, spawnSync } from "node:child_process";
import { mkdtempSync, mkdirSync, writeFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";

const guard = fileURLToPath(new URL("./check-public-files.mjs", import.meta.url));

function check(paths) {
  const directory = mkdtempSync(join(tmpdir(), "public-file-guard-"));
  try {
    execFileSync("git", ["init", "--quiet", directory]);
    for (const path of paths) {
      const target = join(directory, path);
      mkdirSync(dirname(target), { recursive: true });
      writeFileSync(target, "synthetic fixture only\n");
    }
    execFileSync("git", ["add", "--", ...paths], { cwd: directory });
    return spawnSync(process.execPath, [guard], { cwd: directory, encoding: "utf8" });
  } finally {
    rmSync(directory, { recursive: true, force: true });
  }
}

test("example configuration and normal source can be published", () => {
  const result = check([".env.example", "web/.env.example", "src/main.go"]);
  assert.equal(result.status, 0, result.stderr);
});

test("private configuration is rejected at any directory depth", () => {
  for (const path of [".env", "web/.env.local", "nested/.env.production", "keys/server.key", "nested/account.p12", "nested/.npmrc", "credentials-prod.json", "gcp-service-account.json", ".security-local/report.json"]) {
    const result = check([path]);
    assert.equal(result.status, 1, path);
    assert.ok(result.stderr.includes(path));
    assert.ok(!result.stderr.includes("synthetic fixture only"));
  }
});
