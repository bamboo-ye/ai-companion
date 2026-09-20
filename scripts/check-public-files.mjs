// Inspect the Git index, not the ignored local configuration. No file contents
// or credential values are printed. Run after staging a public-release change.
import { execFileSync } from "node:child_process";

const files = execFileSync("git", ["ls-files", "-z"], { encoding: "utf8" }).split("\0").filter(Boolean);
const forbidden = files.filter((path) => {
  const name = path.split("/").at(-1);
  if (/^\.env(?:\.|$)/.test(name) && name !== ".env.example") return true;
  return /\.(pem|key|p12|pfx|jks|keystore)$/i.test(name)
    || /^(\.netrc|\.pypirc|\.npmrc)$/.test(name)
    || /^(credentials.*\.json|.*service-account.*\.json)$/i.test(name)
    || /(^|\/)(\.security-local|\.release-evidence|\.data|\.cache)(\/|$)/.test(path);
});
if (forbidden.length) {
  console.error("Private configuration or credentials are tracked:\n" + forbidden.join("\n"));
  process.exit(1);
}
console.log(`Public-file boundary passed for ${files.length} tracked files.`);
