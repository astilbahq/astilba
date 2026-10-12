import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync, readdirSync, rmSync } from "node:fs";
import { createRequire } from "node:module";
import { tmpdir } from "node:os";
import path from "node:path";

const require = createRequire(import.meta.url);
const publintManifest = path.resolve(
  path.dirname(require.resolve("publint")),
  "../package.json"
);
const attwManifest = require.resolve("@arethetypeswrong/cli/package.json");
const pnpm = process.env.npm_execpath;
assert.ok(
  typeof pnpm === "string" && pnpm.length > 0,
  "Run this check through pnpm"
);
/** @param {string[]} args Arguments forwarded to the pinned package manager. */
const run = (args) =>
  execFileSync(process.execPath, [pnpm, ...args], { stdio: "inherit" });

/**
 * @param {string} manifest Path to the installed tool's package manifest.
 * @param {string} name Expected CLI name.
 * @param {string[]} args Arguments forwarded to the tool.
 */
function tool(manifest, name, args) {
  /** @type {unknown} */
  const metadata = JSON.parse(readFileSync(manifest, "utf-8"));
  assert.ok(
    metadata !== null && typeof metadata === "object" && "bin" in metadata
  );
  const { bin } = metadata;
  let entry;
  if (typeof bin === "string") {
    entry = bin;
  } else {
    assert.ok(bin !== null && typeof bin === "object" && "attw" in bin);
    assert.equal(name, "attw");
    assert.equal(typeof bin.attw, "string");
    entry = bin.attw;
  }
  assert.ok(typeof entry === "string" && entry.length > 0);
  execFileSync(
    process.execPath,
    [path.resolve(path.dirname(manifest), entry), ...args],
    {
      stdio: "inherit",
    }
  );
}

const directory = mkdtempSync(path.join(tmpdir(), "astilba-cache-pack-"));
try {
  run(["exec", "vp", "pack"]);
  execFileSync(process.execPath, ["../../scripts/assert-no-async-hooks.mjs"], {
    stdio: "inherit",
  });
  run(["pack", "--pack-destination", directory]);
  const archives = readdirSync(directory).filter((name) =>
    name.endsWith(".tgz")
  );
  assert.equal(archives.length, 1, "Expected one package archive");
  const archive = path.join(directory, archives[0]);
  tool(publintManifest, "publint", ["--strict", archive]);
  tool(attwManifest, "attw", [archive, "--profile", "esm-only"]);
} finally {
  // This exact temporary directory was created by this invocation.
  rmSync(directory, { recursive: true, force: true });
}
