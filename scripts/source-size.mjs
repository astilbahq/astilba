import { lstatSync, readdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { comparisonBase } from "./ratchet-base.mjs";

const baselinePath = "scripts/source-size-baseline.json";
const roots = new Set(["apps", "services", "contracts", "scripts", ".github"]);
const ignored = new Set(["node_modules", ".git", ".artifacts"]);
const generated = [
  "services/platform/db/generated",
  "services/platform/internal/api/api.gen.go",
  "apps/platform/src/api/generated",
  "apps/platform/src/i18n/generated",
  "apps/platform/.storybook/public/mockServiceWorker.js",
  "apps/platform/styled-system",
  "apps/platform/dist",
  "apps/platform/coverage",
];
const source = /\.(?:go|[cm]?[jt]sx?)$/;

function limit(path) {
  return /(?:^|\/)(?:tests|workshop-tests)\//.test(path) ||
    /(?:_test\.go|[.-](?:test|spec)\.[cm]?[jt]sx?)$/.test(path)
    ? 800
    : 600;
}

function lines(text) {
  if (!text) return 0;
  const parts = text.split(/\r\n|\n|\r/);
  return parts.length - (parts.at(-1) === "" ? 1 : 0);
}

function excluded(path) {
  return generated.some(
    (owner) => path === owner || path.startsWith(`${owner}/`),
  );
}

function sizes(root) {
  const oversized = {};
  function visit(directory, prefix = "") {
    for (const entry of readdirSync(directory, { withFileTypes: true })) {
      if (ignored.has(entry.name)) continue;
      const path = prefix ? `${prefix}/${entry.name}` : entry.name;
      if (excluded(path)) continue;
      if (!prefix && entry.isDirectory() && !roots.has(entry.name)) continue;
      if (entry.isSymbolicLink()) {
        if (prefix || source.test(path) || roots.has(path))
          throw new Error(`Source-size checks refuse symlinks: ${path}`);
        continue;
      }
      if (entry.isDirectory()) visit(join(directory, entry.name), path);
      else if (entry.isFile() && source.test(path)) {
        const count = lines(readFileSync(join(directory, entry.name), "utf8"));
        if (count > limit(path)) oversized[path] = count;
      }
    }
  }
  visit(root);
  return Object.fromEntries(
    Object.entries(oversized).sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0)),
  );
}

function parseBaseline(text, label) {
  const value = JSON.parse(text);
  if (!value || Array.isArray(value) || typeof value !== "object")
    throw new Error(`${label} must be a path-to-line-count object`);
  for (const [path, count] of Object.entries(value)) {
    if (
      !source.test(path) ||
      !Number.isSafeInteger(count) ||
      count <= limit(path)
    )
      throw new Error(`Invalid allowance in ${label}: ${path}`);
  }
  return value;
}

function comparison(root, requested, ci) {
  const history = comparisonBase(root, requested, ci, "source-size");
  if (!history) return null;
  const previous = history.entry(baselinePath);
  return {
    ...history,
    previous:
      previous === null ? null : parseBaseline(previous, "comparison baseline"),
  };
}

function run() {
  let requested = process.env.SOURCE_SIZE_BASE || undefined;
  let update = false;
  for (let i = 2; i < process.argv.length; i++) {
    const argument = process.argv[i];
    if (argument === "--write-baseline") update = true;
    else if (argument === "--base" && process.argv[i + 1])
      requested = process.argv[++i];
    else
      throw new Error(
        "Usage: node scripts/source-size.mjs [--base <ref>] [--write-baseline]",
      );
  }
  const root = process.cwd();
  const history = comparison(
    root,
    requested,
    !!process.env.CI && process.env.CI !== "false",
  );
  if (update && !history)
    throw new Error(
      "Updating source-size allowances requires Git comparison history",
    );
  const current = sizes(root);
  if (history) {
    for (const [path, count] of Object.entries(current)) {
      let prior;
      if (history.previous !== null) prior = history.previous[path];
      else {
        const text = history.entry(path);
        if (text !== null) {
          const measured = lines(text);
          if (measured > limit(path)) prior = measured;
        }
      }
      if (prior === undefined || count > prior)
        throw new Error(
          `${path}: ${count} lines exceeds ${prior ?? limit(path)}; split the source instead of adding or raising an allowance`,
        );
    }
  }
  const expected = `${JSON.stringify(current, null, 2)}\n`;
  const file = join(root, baselinePath);
  try {
    if (!lstatSync(file).isFile())
      throw new Error("The source-size baseline must be a regular file");
  } catch (error) {
    if (error.code !== "ENOENT" || !update) throw error;
  }
  if (update) writeFileSync(file, expected);
  else {
    const text = readFileSync(file, "utf8");
    parseBaseline(text, "source-size baseline");
    if (text !== expected)
      throw new Error(
        "Source-size allowances are stale; run node scripts/source-size.mjs --write-baseline with the same comparison base",
      );
  }
  console.log(
    `Source sizes pass (${Object.keys(current).length} shrinking legacy allowances).`,
  );
  if (!history)
    console.log(
      "Source archive: embedded sizes checked; historical ratchet unavailable.",
    );
}

try {
  run();
} catch (error) {
  console.error(error.message);
  process.exitCode = 1;
}
