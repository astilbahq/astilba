import { lstatSync, readdirSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { pathToFileURL } from "node:url";
import { comparisonBase } from "./ratchet-base.mjs";

const root = "services/platform";
const generated = [
  "services/platform/db/generated",
  "services/platform/internal/api/api.gen.go",
];
const isGenerated = (path) =>
  generated.some((owner) => path === owner || path.startsWith(`${owner}/`));
const counted = (path) =>
  path.endsWith(".go") && !path.endsWith("_test.go") && !isGenerated(path);
const statement =
  /^\s*(?:SELECT|INSERT|UPDATE|DELETE|WITH|CREATE|ALTER|DROP|TRUNCATE|LOCK)\s/;
const exception = /\/\/\s*inline-sql:\s*\S/;
const exceptionLine = /^\s*\/\/\s*inline-sql:\s*\S/;

// Counts Go string literals that begin an SQL statement. A literal is exempt
// when its own line carries `// inline-sql: <reason>`, or the line above is only
// that comment.
export function inlineSQL(text) {
  const lines = text.split("\n");
  const exempt = (offset) => {
    const line = text.slice(0, offset).split("\n").length - 1;
    return (
      exception.test(lines[line]) || exceptionLine.test(lines[line - 1] ?? "")
    );
  };
  let count = 0;
  for (let i = 0; i < text.length; i++) {
    const char = text[i];
    if (char === "/" && text[i + 1] === "/") {
      i = text.indexOf("\n", i);
      if (i < 0) break;
    } else if (char === "/" && text[i + 1] === "*") {
      i = text.indexOf("*/", i + 2) + 1;
      if (i <= 0) break;
    } else if (char === "'") {
      while (++i < text.length && text[i] !== "'") if (text[i] === "\\") i++;
    } else if (char === '"' || char === "`") {
      const start = i;
      let value = "";
      while (++i < text.length && text[i] !== char) {
        if (char === '"' && text[i] === "\\") value += text[++i];
        else value += text[i];
      }
      if (statement.test(value) && !exempt(start)) count++;
    }
  }
  return count;
}

function files(directory, prefix, found = []) {
  for (const entry of readdirSync(directory, { withFileTypes: true })) {
    const path = `${prefix}/${entry.name}`;
    if (isGenerated(path)) continue;
    if (entry.isSymbolicLink())
      throw new Error(`Inline SQL checks refuse symlinks: ${path}`);
    if (entry.isDirectory()) files(join(directory, entry.name), path, found);
    else if (entry.isFile() && counted(path)) found.push(path);
  }
  return found;
}

function run() {
  let requested = process.env.SOURCE_SIZE_BASE || undefined;
  for (let i = 2; i < process.argv.length; i++) {
    if (process.argv[i] === "--base" && process.argv[i + 1])
      requested = process.argv[++i];
    else throw new Error("Usage: node scripts/inline-sql.mjs [--base <ref>]");
  }
  const cwd = process.cwd();
  lstatSync(join(cwd, root));
  const history = comparisonBase(
    cwd,
    requested,
    !!process.env.CI && process.env.CI !== "false",
    "inline SQL",
  );
  // Compare per Go package so legacy SQL can move between a package's files,
  // for example when splitting an oversized file.
  const packages = new Map();
  let total = 0;
  for (const path of files(join(cwd, root), root)) {
    const count = inlineSQL(readFileSync(join(cwd, path), "utf8"));
    const name = dirname(path);
    packages.set(name, (packages.get(name) ?? 0) + count);
    total += count;
  }
  const grown = [];
  if (history)
    for (const [name, count] of [...packages].sort(([a], [b]) =>
      a < b ? -1 : a > b ? 1 : 0,
    )) {
      if (count === 0) continue;
      const prior = history
        .files(name)
        .filter(counted)
        .reduce((sum, path) => sum + inlineSQL(history.entry(path) ?? ""), 0);
      if (count > prior)
        grown.push(`${name}: ${count} inline statements, was ${prior}`);
    }
  if (grown.length)
    throw new Error(
      `${grown.join("\n")}\nAdd platform queries to services/platform/db/queries (sqlc) or justify an exception with // inline-sql: <reason>`,
    );
  console.log(`Inline SQL does not grow (${total} legacy statements remain).`);
  if (!history)
    console.log("Source archive: historical inline SQL ratchet unavailable.");
}

if (
  process.argv[1] &&
  import.meta.url === pathToFileURL(process.argv[1]).href
) {
  try {
    run();
  } catch (error) {
    console.error(error.message);
    process.exitCode = 1;
  }
}
