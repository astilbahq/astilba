// @ts-check
/// <reference types="node" />

import { spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import { lstat, readFile, readdir } from "node:fs/promises";
import { dirname, relative, resolve, sep } from "node:path";
import { fileURLToPath } from "node:url";
import { isDeepStrictEqual } from "node:util";
import { gzipSync } from "node:zlib";

import { build } from "esbuild";

import { canonicalStrictDescendant } from "./filesystem-containment.mjs";
import { run } from "./matrix-artifact.mjs";

const root = fileURLToPath(new URL("../", import.meta.url));
const dist = resolve(root, "dist");
const browserRoot = resolve(dist, "browser");

/** @param {string} message @returns {never} */
const fail = (message) => {
  throw new Error(message);
};

/** @param {unknown} value @returns {value is Record<string, unknown>} */
const isRecord = (value) =>
  typeof value === "object" && value !== null && !Array.isArray(value);

/** @param {number} mode */
const permissionMode = (mode) => mode % 0o1000;

/** @param {readonly string[]} values */
const sortedStrings = (values) => {
  /** @type {string[]} */
  const output = [];
  for (const value of values) {
    const index = output.findIndex((current) => value < current);
    if (index === -1) {
      output.push(value);
    } else {
      output.splice(index, 0, value);
    }
  }
  return output;
};

/** @type {unknown} */
const packageJson = JSON.parse(
  await readFile(resolve(root, "package.json"), "utf-8")
);
const expectedExports = {
  ".": {
    types: "./dist/index.d.ts",
    workerd: null,
    node: "./dist/index.js",
  },
  "./browser": {
    types: "./dist/browser/index.d.ts",
    workerd: null,
    browser: "./dist/browser/index.js",
    default: "./dist/browser/index.js",
  },
  "./runtime": {
    types: "./dist/runtime/index.d.ts",
    workerd: "./dist/runtime/workerd.js",
    node: "./dist/runtime/index.js",
  },
  "./vite": {
    types: "./dist/vite/index.d.ts",
    workerd: null,
    node: "./dist/vite/index.js",
  },
};
const expectedBugs = {
  url: "https://github.com/astilbahq/env/issues",
};
const expectedRepository = {
  type: "git",
  url: "git+https://github.com/astilbahq/env.git",
};
const expectedPublishConfig = {
  access: "public",
  provenance: true,
};
const expectedEngines = {
  node: ">=22.14.0 <23.0.0 || >=24.0.0 <25.0.0 || >=26.0.0 <27.0.0",
};
const expectedPeerDependencies = {
  vite: ">=8.1.5 <9.0.0",
};
const expectedPeerDependenciesMeta = {
  vite: {
    optional: true,
  },
};
const expectedDistFiles = sortedStrings([
  "adapters/record.js",
  "artifacts/output.js",
  "authoring/environment.d.ts",
  "authoring/environment.js",
  "browser/failure.d.ts",
  "browser/failure.js",
  "browser/index.d.ts",
  "browser/index.js",
  "browser/json.js",
  "browser/loader.d.ts",
  "browser/loader.js",
  "cli/astilba-env.d.ts",
  "cli/astilba-env.js",
  "cli/compile.d.ts",
  "cli/compile.js",
  "core/bounded-json.js",
  "core/codecs.js",
  "core/contract.js",
  "core/descriptor.js",
  "core/diagnostics.js",
  "core/digest.js",
  "core/identity.js",
  "core/json.js",
  "core/resolve.js",
  "core/server-codecs.js",
  "core/shapes.d.ts",
  "core/shapes.js",
  "core/types.d.ts",
  "core/types.js",
  "index.d.ts",
  "index.js",
  "inventory/contract-inventory.js",
  "planning/plan.d.ts",
  "planning/plan.js",
  "planning/snapshot.js",
  "planning/types.d.ts",
  "product/compilation.d.ts",
  "product/compilation.js",
  "product/generate.js",
  "provider/cloudflare.js",
  "provider/types.d.ts",
  "runtime/index.d.ts",
  "runtime/index.js",
  "runtime/model.d.ts",
  "runtime/process-standard-schema.d.ts",
  "runtime/process-standard-schema.js",
  "runtime/process.d.ts",
  "runtime/process.js",
  "runtime/resolution.js",
  "runtime/results.js",
  "runtime/standard-schema.d.ts",
  "runtime/standard-schema.js",
  "runtime/validation.js",
  "runtime/workerd.d.ts",
  "runtime/workerd.js",
  "vite/boundary.js",
  "vite/index.d.ts",
  "vite/index.js",
]);

if (
  !isRecord(packageJson) ||
  packageJson.name !== "@astilba/env" ||
  packageJson.version !== "0.3.0" ||
  Object.hasOwn(packageJson, "private") ||
  packageJson.license !== "MIT" ||
  packageJson.homepage !== "https://github.com/astilbahq/env#readme" ||
  !isDeepStrictEqual(packageJson.bugs, expectedBugs) ||
  !isDeepStrictEqual(packageJson.repository, expectedRepository) ||
  packageJson.funding !== "https://github.com/sponsors/astilbahq" ||
  !isDeepStrictEqual(packageJson.publishConfig, expectedPublishConfig) ||
  !isDeepStrictEqual(packageJson.engines, expectedEngines) ||
  !isDeepStrictEqual(packageJson.peerDependencies, expectedPeerDependencies) ||
  !isDeepStrictEqual(
    packageJson.peerDependenciesMeta,
    expectedPeerDependenciesMeta
  ) ||
  packageJson.type !== "module" ||
  packageJson.sideEffects !== false ||
  JSON.stringify(packageJson.exports) !== JSON.stringify(expectedExports) ||
  JSON.stringify(packageJson.files) !==
    JSON.stringify(["dist", "LICENSE", "README.md"]) ||
  JSON.stringify(packageJson.bin) !==
    JSON.stringify({ "astilba-env": "./dist/cli/astilba-env.js" })
) {
  fail("Package metadata differs from the exact public release surface.");
}

/**
 * @param {string} directory
 * @returns {Promise<string[]>}
 */
const walk = async (directory) => {
  /** @type {string[]} */
  const files = [];
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    const path = resolve(directory, entry.name);
    const metadata = await lstat(path);
    if (metadata.isSymbolicLink()) {
      fail("Package output contains a symbolic link.");
    }
    if (metadata.isDirectory()) {
      files.push(...(await walk(path)));
    } else if (metadata.isFile()) {
      files.push(path);
    } else {
      fail("Package output contains an unsupported filesystem node.");
    }
  }
  return files;
};

const distMetadata = await lstat(dist);
if (distMetadata.isSymbolicLink() || !distMetadata.isDirectory()) {
  fail("Package output root is not a real directory.");
}
const outputFiles = await walk(dist);
const actualDistFiles = sortedStrings(
  outputFiles.map((path) => relative(dist, path).split(sep).join("/"))
);
if (JSON.stringify(actualDistFiles) !== JSON.stringify(expectedDistFiles)) {
  fail("Package output differs from the exact dist allowlist.");
}

const executable = await lstat(resolve(dist, "cli/astilba-env.js"));
if (
  executable.isSymbolicLink() ||
  !executable.isFile() ||
  permissionMode(executable.mode) !== 0o755
) {
  fail("Package CLI is not a regular executable file.");
}
for (const path of outputFiles) {
  const metadata = await lstat(path);
  const portable = relative(dist, path).split(sep).join("/");
  const expectedMode = portable === "cli/astilba-env.js" ? 0o755 : 0o644;
  if (
    metadata.isSymbolicLink() ||
    !metadata.isFile() ||
    permissionMode(metadata.mode) !== expectedMode
  ) {
    fail(`Package output has an invalid type or mode: ${portable}`);
  }
}

const nodeBrowserResolution = spawnSync(
  process.execPath,
  [
    "--input-type=module",
    "--eval",
    'const browser = await import("@astilba/env/browser"); if (typeof browser.loadBrowserBootstrap !== "function") throw new Error("Browser export is incomplete.");',
  ],
  {
    cwd: root,
    encoding: "utf-8",
    maxBuffer: 4096,
    stdio: ["ignore", "pipe", "pipe"],
    timeout: 10_000,
  }
);
if (
  nodeBrowserResolution.error !== undefined ||
  nodeBrowserResolution.signal !== null ||
  nodeBrowserResolution.status !== 0
) {
  fail("Node could not resolve the public browser export.");
}

const importPattern =
  /\b(?:from\s*|import\s*\(\s*|import\s*)["'](?<specifier>[^"']+)["']/gu;
/** @type {Set<string>} */
const visited = new Set();
/** @param {string} path */
const inspectBrowserModule = async (path) => {
  const { canonicalTarget } = await canonicalStrictDescendant(
    browserRoot,
    path
  );
  if (visited.has(canonicalTarget)) {
    return;
  }
  visited.add(canonicalTarget);
  const metadata = await lstat(canonicalTarget);
  if (metadata.isSymbolicLink() || !metadata.isFile()) {
    fail("Browser graph contains a non-regular file.");
  }
  const source = await readFile(canonicalTarget, "utf-8");
  for (const match of source.matchAll(importPattern)) {
    const specifier = match.groups?.specifier;
    if (specifier === undefined) {
      continue;
    }
    if (specifier.startsWith("node:")) {
      fail(`Browser graph contains a Node builtin: ${specifier}`);
    }
    if (specifier === "@astilba/env" || specifier === "@astilba/env/runtime") {
      fail(`Browser graph contains a private runtime import: ${specifier}`);
    }
    if (!specifier.startsWith(".")) {
      fail(`Browser graph contains an external import: ${specifier}`);
    }
    await inspectBrowserModule(resolve(dirname(canonicalTarget), specifier));
  }
};
await inspectBrowserModule(resolve(browserRoot, "index.js"));

const forbidden = [
  "/Users/",
  "/home/",
  "C:\\Users\\",
  "file://",
  "private-brotli-canary",
  "private-gzip-canary",
  "sensitive-test-value",
  "sourceMappingURL",
  "sourceURL",
];
/** @type {{ bytes: number, mode: number, path: string, sha256: string }[]} */
const verifiedFiles = [];
for (const path of outputFiles) {
  const metadata = await lstat(path);
  const bytes = await readFile(path);
  const text = bytes.toString("utf-8");
  for (const marker of forbidden) {
    if (text.includes(marker)) {
      fail(`Package output contains a forbidden marker: ${marker}`);
    }
  }
  verifiedFiles.push({
    bytes: bytes.byteLength,
    mode: permissionMode(metadata.mode),
    path: relative(root, path).split(sep).join("/"),
    sha256: createHash("sha256").update(bytes).digest("hex"),
  });
}
verifiedFiles.sort((left, right) => left.path.localeCompare(right.path));

const browserBundle = await build({
  bundle: true,
  entryPoints: [resolve(browserRoot, "index.js")],
  format: "esm",
  logLevel: "silent",
  minify: true,
  platform: "browser",
  write: false,
});
const browserOutput = browserBundle.outputFiles[0];
if (browserBundle.outputFiles.length !== 1 || browserOutput === undefined) {
  fail("Browser export did not produce exactly one bundle.");
}
const browserGzipBytes = gzipSync(browserOutput.contents, {
  level: 9,
}).byteLength;
if (browserGzipBytes > 4096) {
  fail(`Browser export exceeds 4,096 gzip bytes: ${browserGzipBytes}`);
}

run("pnpm", ["exec", "publint"], root);
run("pnpm", ["exec", "attw", "--pack", "--profile", "esm-only"], root);
process.stdout.write(
  `${JSON.stringify({
    browserFiles: visited.size,
    browserGzipBytes,
    files: verifiedFiles.length,
    passed: true,
    verifiedFiles,
  })}\n`
);
