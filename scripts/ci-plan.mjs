import { execFileSync } from "node:child_process";
import { appendFileSync } from "node:fs";
import { posix } from "node:path";
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
    ".github/actions/install-pnpm/action.yml",
    "package.json",
    "pnpm-lock.yaml",
    "pnpm-workspace.yaml",
    "scripts/fetch-attempts.mjs",
    "scripts/install-pnpm.mjs",
    "vite.config.ts",
  ].includes(path);
// Every suite with fixture images pulls them through these.
const imagePulls = (path) =>
  path.startsWith(".github/actions/pull-images/") ||
  path === "scripts/fetch-attempts.mjs";

// The scripts a Makefile target's recipe runs with node.
export function recipeScripts(makefile, target) {
  const lines = makefile.split("\n");
  const start = lines.findIndex((line) => line.startsWith(`${target}:`));
  if (start < 0) throw new Error(`The Makefile has no ${target} target`);
  const scripts = [];
  for (const line of lines.slice(start + 1)) {
    if (!line.startsWith("\t")) break;
    for (const [, script] of line.matchAll(
      /\bnode\s+(scripts\/[\w./-]+\.mjs)\b/g,
    ))
      scripts.push(script);
  }
  if (scripts.length === 0) throw new Error(`${target} runs no scripts`);
  return scripts;
}

// Every module the entry scripts load from the repository, themselves included, following
// relative static, side-effect, re-export and dynamic imports. A specifier that only looks like
// an import, in a comment for instance, adds a path, which can only select the scan more often.
export function localModules(entries, read) {
  const found = new Set();
  const pending = [...entries];
  while (pending.length > 0) {
    const path = posix.normalize(pending.pop());
    if (found.has(path)) continue;
    found.add(path);
    for (const [, specifier] of read(path).matchAll(
      /(?:\bfrom|\bimport)\s*\(?\s*["'](\.{1,2}\/[^"']+)["']/g,
    ))
      pending.push(posix.join(posix.dirname(path), specifier));
  }
  return found;
}

// `make check-vulnerabilities` is the scan, so it depends on every script that recipe runs and
// every module those load, at the base and in the tested commit. A pull request that stops a
// module being loaded changes the module that loaded it, which is still loaded. The modules are
// read from git, so a new import is followed without anyone listing it here. Without them at
// either end, null.
function scanScripts(revs, read) {
  try {
    return new Set(
      revs.flatMap((rev) => [
        ...localModules(
          recipeScripts(read(rev, "Makefile"), "check-vulnerabilities"),
          (path) => read(rev, path),
        ),
      ]),
    );
  } catch {
    return null;
  }
}

const vulnerabilityInputs = (path, scripts) =>
  scripts === null ||
  scripts.has(path) ||
  path.endsWith(".go") ||
  /(^|\/)(go\.mod|go\.sum|package\.json)$/.test(path) ||
  javascript(path) ||
  path === "Makefile" ||
  path === ".github/vulnerability-allowlist.json" ||
  path === ".github/workflows/vulnerability-scan.yml" ||
  path === ".github/workflows/application.yml" ||
  // The scan also confirms the pinned Public Suffix List matches upstream.
  path.startsWith("services/platform/internal/publicsuffix/");

// A pull request or merge group skips a suite only when every changed path is
// known not to affect it; anything unrecognised selects the suite.
const affects = {
  // The proxy tests run the configurations on the hosting page.
  native: (path) =>
    path === "docs/hosting.md" ||
    !(docs(path) || dashboard(path) || serverOnly(path)),
  go: (path) => !(docs(path) || dashboard(path)),
  browser: (path) => !docs(path),
  durable: (path) =>
    owns("experiments/durable-execution/", "go.yml")(path) || imagePulls(path),
  git: (path) =>
    owns("experiments/git-compose/", "git-providers.yml")(path) ||
    imagePulls(path),
  canvas: (path) =>
    owns("experiments/canvas-editor/", "canvas.yml")(path) || javascript(path),
  vulnerabilities: vulnerabilityInputs,
};
const planner = (path) =>
  path.startsWith("scripts/ci-plan") || path === ".github/workflows/ci.yml";

export function ciPlan(event, cwd = process.cwd(), mergeGroupBase) {
  const all = Object.fromEntries(
    Object.keys(affects).map((suite) => [suite, true]),
  );
  if (["push", "workflow_dispatch", "schedule"].includes(event)) return all;
  if (!["pull_request", "merge_group"].includes(event))
    throw new Error("Unsupported CI event");

  const env = Object.fromEntries(
    Object.entries(process.env).filter(([name]) => !name.startsWith("GIT_")),
  );
  const git = (...args) =>
    execFileSync("git", args, { cwd, encoding: "utf8", env });
  // A squash group is a stack of single-parent commits, so its first parent can
  // omit pull requests in the group. Only the event's base bounds all of them.
  let base = mergeGroupBase;
  if (event === "pull_request") {
    // Compare the tested merge to its actual base, not a potentially stale event SHA.
    const parents = git("show", "-s", "--format=%P", "HEAD")
      .trim()
      .split(/\s+/);
    if (parents.length !== 2) throw new Error("Expected a two-parent PR merge");
    base = "HEAD^1";
  }
  // Renames must include the removed path: moving a native input into src is not UI-only.
  const paths = git("diff", "--no-renames", "--name-only", "-z", base, "HEAD")
    .split("\0")
    .filter(Boolean);
  if (paths.length === 0 || paths.some(planner)) return all;
  const scripts = scanScripts([base, "HEAD"], (rev, path) =>
    execFileSync("git", ["show", `${rev}:${path}`], {
      cwd,
      encoding: "utf8",
      env,
      stdio: ["ignore", "pipe", "ignore"],
    }),
  );
  return Object.fromEntries(
    Object.entries(affects).map(([suite, affected]) => [
      suite,
      paths.some((path) => affected(path, scripts)),
    ]),
  );
}

if (
  process.argv[1] &&
  import.meta.url === pathToFileURL(process.argv[1]).href
) {
  const plan = ciPlan(
    process.env.GITHUB_EVENT_NAME,
    process.cwd(),
    process.env.MERGE_GROUP_BASE_SHA,
  );
  for (const [suite, selected] of Object.entries(plan)) {
    appendFileSync(process.env.GITHUB_OUTPUT, `${suite}=${selected}\n`);
    console.log(`${suite}: ${selected ? "run" : "skip"}`);
  }
}
