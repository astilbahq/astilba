import assert from "node:assert/strict";
import { readFileSync, realpathSync } from "node:fs";
import { createRequire } from "node:module";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import YAML from "yaml";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const require = createRequire(resolve(root, "package.json"));
const read = (path) => JSON.parse(readFileSync(path, "utf8"));
const workspace = YAML.parse(readFileSync(resolve(root, "pnpm-workspace.yaml"), "utf8"));
const expected = workspace.catalog.vitest;
const direct = realpathSync(require.resolve("vitest/package.json"));
const owner = require.resolve("vite-plus/package.json");
assert.equal(read(owner).dependencies.vitest, expected, "Vite+ must declare the matching Vitest runner");
const runtime = createRequire(owner);
const fromRuntime = realpathSync(runtime.resolve("vitest/package.json"));
assert.equal(direct, fromRuntime, "Vite+ and direct tests must share one Vitest instance");
assert.equal(read(direct).version, expected);
assert.equal(read(require.resolve("@vitest/coverage-istanbul/package.json")).version, expected);

const lock = YAML.parse(readFileSync(resolve(root, "pnpm-lock.yaml"), "utf8"));
const cohort = Object.keys(lock.packages).filter((key) => /^(@vitest\/|vitest@)/u.test(key));
assert.ok(cohort.length >= 10, "Missing Vitest cohort in lockfile");
for (const key of cohort) {
  assert.equal(key.slice(key.lastIndexOf("@") + 1), expected, `Split Vitest cohort: ${key}`);
}
console.log(`One resolved Vitest ${expected} instance; ${cohort.length} matching locked cohort packages.`);
