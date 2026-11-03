import { execFileSync } from "node:child_process";
import { appendFileSync } from "node:fs";
import { pathToFileURL } from "node:url";

const instructions = (path) => /(^|\/)(AGENTS|REVIEW)\.md$/.test(path);
const docs = (path) =>
  !instructions(path) && (path === "README.md" || /^docs\/.*\.md$/.test(path));
const dashboard = (path) =>
  !instructions(path) &&
  (/^apps\/platform\/(src|theme|public|tests|workshop-tests|\.storybook)\//.test(
    path,
  ) ||
    path === "apps/platform/panda.config.ts");
// Server-side packages without host-specific behaviour; the Go race lanes and
// browser suites still cover them.
const serverOnly = (path) =>
  /^services\/platform\/(internal\/(auth|alerts|mailer|billing|automationclient)|cmd\/cli)\//.test(
    path,
  );
const owns = (prefix, workflow) => (path) =>
  path.startsWith(prefix) ||
  path === `.github/workflows/${workflow}` ||
  path === "Makefile";
const javascript = (path) =>
  [
    "package.json",
    "pnpm-lock.yaml",
    "pnpm-workspace.yaml",
    "vite.config.ts",
  ].includes(path);

// A pull request skips a suite only when every changed path is known not to
// affect it; anything unrecognised selects the suite.
const affects = {
  native: (path) => !(docs(path) || dashboard(path) || serverOnly(path)),
  go: (path) => !(docs(path) || dashboard(path)),
  browser: (path) => !docs(path),
  durable: owns("experiments/durable-execution/", "go.yml"),
  git: owns("experiments/git-compose/", "git-providers.yml"),
  canvas: (path) =>
    owns("experiments/canvas-editor/", "canvas.yml")(path) || javascript(path),
};
const planner = (path) =>
  path.startsWith("scripts/ci-plan") || path === ".github/workflows/ci.yml";

export function ciPlan(event, cwd = process.cwd()) {
  const all = Object.fromEntries(
    Object.keys(affects).map((suite) => [suite, true]),
  );
  if (["push", "workflow_dispatch", "schedule"].includes(event)) return all;
  if (event !== "pull_request") throw new Error("Unsupported CI event");

  const env = Object.fromEntries(
    Object.entries(process.env).filter(([name]) => !name.startsWith("GIT_")),
  );
  const git = (...args) =>
    execFileSync("git", args, { cwd, encoding: "utf8", env });
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
  if (paths.length === 0 || paths.some(planner)) return all;
  return Object.fromEntries(
    Object.entries(affects).map(([suite, affected]) => [
      suite,
      paths.some(affected),
    ]),
  );
}

if (
  process.argv[1] &&
  import.meta.url === pathToFileURL(process.argv[1]).href
) {
  const plan = ciPlan(process.env.GITHUB_EVENT_NAME);
  for (const [suite, selected] of Object.entries(plan)) {
    appendFileSync(process.env.GITHUB_OUTPUT, `${suite}=${selected}\n`);
    console.log(`${suite}: ${selected ? "run" : "skip"}`);
  }
}
