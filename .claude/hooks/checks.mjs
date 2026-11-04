import { spawn } from "node:child_process";
import { existsSync } from "node:fs";
import { dirname, join } from "node:path";

const OUTPUT_LINES = 40;
const OUTPUT_CHARS = 3000;

const format =
  /^(?:apps\/platform\/|experiments\/canvas-editor\/|vite\.config\.ts$)/;
const sourceSize =
  /^(?:(?:apps|services|contracts|scripts|\.github)\/.*\.(?:go|[cm]?[jt]sx?)|scripts\/source-size-baseline\.json)$/;
const inlineSQL =
  /^(?:services\/platform\/.*\.go|scripts\/(?:inline-sql|ratchet-base).*)$/;
const generated =
  /^(?:contracts\/platform\/|services\/platform\/(?:db\/(?:queries|migrations|generated)\/|sqlc\.yaml|go\.(?:mod|sum)$|internal\/api\/api\.gen\.go)|apps\/platform\/src\/api\/generated\/|scripts\/platform-generate)/;
const ciPlan = /^(?:scripts\/ci-plan|\.github\/workflows\/ci\.yml)/;

// Keeps the checks offline: pnpm would otherwise download the pinned version of
// itself, and Go a missing module or toolchain.
const offline = {
  ...Object.fromEntries(
    Object.entries(process.env).filter(([name]) => !name.startsWith("GIT_")),
  ),
  GIT_NO_LAZY_FETCH: "1",
  GOPROXY: "off",
  GOTOOLCHAIN: "local",
  npm_config_manage_package_manager_versions: "false",
  npm_config_offline: "true",
};
// Failures that say this machine cannot run the check, not that the code is wrong.
const unavailable =
  /module lookup disabled by GOPROXY=off|GOTOOLCHAIN=local\)|ERR_PNPM_NO_OFFLINE/;

export const formattable = (path) => format.test(path);

function goModule(root, path) {
  for (let dir = dirname(path); ; dir = dirname(dir)) {
    if (existsSync(join(root, dir, "go.mod"))) return dir;
    if (dir === ".") return null;
  }
}

export function checksFor(root, files) {
  const present = files.filter((path) => existsSync(join(root, path)));
  const any = (pattern) => files.some((path) => pattern.test(path));
  const checks = [];
  const add = (name, cmd, options = {}) =>
    checks.push({ name, cmd, cwd: root, ...options });

  const go = present.filter((path) => path.endsWith(".go"));
  if (go.length)
    add("gofmt", ["gofmt", "-l", ...go.map((path) => `./${path}`)], {
      failOnOutput: true,
    });
  const formatted = present.filter(formattable);
  if (formatted.length)
    add(
      "format",
      [
        "node_modules/.bin/vp",
        "fmt",
        "--check",
        "--no-error-on-unmatched-pattern",
        ...formatted.map((path) => `./${path}`),
      ],
      { requires: "node_modules/.bin/vp" },
    );
  if (any(sourceSize)) add("source size", ["make", "check-source-size"]);
  if (any(inlineSQL)) add("inline SQL", ["make", "check-inline-sql"]);
  if (any(ciPlan)) add("CI plan", ["make", "check-ci"]);
  if (any(/^\.claude\/hooks\//)) add("hooks", ["make", "check-hooks"]);

  const modules = new Set(
    files
      .filter((path) => /(?:\.go|(?:^|\/)go\.(?:mod|sum))$/.test(path))
      .map((path) => goModule(root, path))
      .filter(Boolean),
  );
  // Vetting the module also finds callers a change broke, and stays quick.
  for (const module of modules)
    add(`go vet ${module}`, ["go", "vet", "./..."], {
      cwd: join(root, module),
    });

  if (any(generated))
    add("generated files", ["pnpm", "generate:check"], {
      requires: "node_modules",
    });
  if (any(/^apps\/platform\//))
    add("dashboard", ["pnpm", "--filter", "@astilba/dashboard", "check"], {
      requires: "apps/platform/node_modules",
    });
  if (any(/^experiments\/canvas-editor\//))
    add("canvas", ["pnpm", "--filter", "@astilba/canvas-spike", "check"], {
      requires: "experiments/canvas-editor/node_modules",
    });
  return checks;
}

function tail(output) {
  return output
    .trim()
    .split("\n")
    .slice(-OUTPUT_LINES)
    .join("\n")
    .slice(-OUTPUT_CHARS);
}

function run(root, check, deadline) {
  const skipped = (detail) => ({ ...check, status: "skipped", detail });
  if (check.requires && !existsSync(join(root, check.requires)))
    return skipped(`${check.requires} is missing; run pnpm install`);
  return new Promise((resolve) => {
    const child = spawn(check.cmd[0], check.cmd.slice(1), {
      cwd: check.cwd,
      env: offline,
      detached: true,
      stdio: ["ignore", "pipe", "pipe"],
    });
    let output = "";
    let timedOut = false;
    const collect = (chunk) => {
      output = (output + chunk).slice(-64 * 1024);
    };
    child.stdout.on("data", collect);
    child.stderr.on("data", collect);
    const timer = setTimeout(() => {
      timedOut = true;
      process.kill(-child.pid, "SIGKILL");
    }, deadline - Date.now());
    child.on("error", (error) => {
      clearTimeout(timer);
      resolve(skipped(error.message));
    });
    child.on("close", (code) => {
      clearTimeout(timer);
      const clean = code === 0 && !(check.failOnOutput && output.trim());
      if (timedOut) resolve(skipped("timed out"));
      else if (clean) resolve({ ...check, status: "passed" });
      else if (unavailable.test(output)) resolve(skipped(tail(output)));
      else resolve({ ...check, status: "failed", detail: tail(output) });
    });
  });
}

export const runChecks = (root, checks, budgetMs) => {
  const deadline = Date.now() + budgetMs;
  return Promise.all(checks.map((check) => run(root, check, deadline)));
};
