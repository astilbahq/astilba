import { execFileSync } from "node:child_process";
import { appendFileSync } from "node:fs";
import { pathToFileURL } from "node:url";

function frontendOrDocs(path) {
  if (/(^|\/)(AGENTS|REVIEW)\.md$/.test(path)) return false;
  return (
    path === "README.md" ||
    /^docs\/.*\.md$/.test(path) ||
    /^apps\/platform\/(src|theme|public|tests|workshop-tests|\.storybook)\//.test(
      path,
    ) ||
    path === "apps/platform/panda.config.ts"
  );
}

export function nativePlan(event, cwd = process.cwd()) {
  if (event === "push" || event === "workflow_dispatch") return true;
  if (event !== "pull_request") throw new Error("Unsupported CI event");

  const git = (...args) => execFileSync("git", args, { cwd, encoding: "utf8" });
  // Compare the tested merge to its actual base, not a potentially stale event SHA.
  const parents = git("show", "-s", "--format=%P", "HEAD").trim().split(/\s+/);
  if (parents.length !== 2) throw new Error("Expected a two-parent PR merge");
  // Renames must include the removed path: moving a native input into src is not UI-only.
  const paths = git(
    "diff",
    "--no-renames",
    "--name-only",
    "-z",
    "HEAD^1",
    "HEAD",
  )
    .split("\0")
    .filter(Boolean);
  return paths.length === 0 || !paths.every(frontendOrDocs);
}

if (
  process.argv[1] &&
  import.meta.url === pathToFileURL(process.argv[1]).href
) {
  const native = nativePlan(process.env.GITHUB_EVENT_NAME);
  appendFileSync(process.env.GITHUB_OUTPUT, `native=${native}\n`);
  console.log(
    native
      ? "Both native architectures required"
      : "Frontend/docs-only: native checks skipped",
  );
}
