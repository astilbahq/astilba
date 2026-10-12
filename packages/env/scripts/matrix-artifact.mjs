// @ts-check
/// <reference types="node" />

import { spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import {
  lstat,
  mkdtemp,
  opendir,
  readFile,
  realpath,
  readdir,
  rm,
  writeFile,
} from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, relative, resolve, sep, win32 } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

import { parseManifest, verifyManifestBinding } from "./artifact-manifest.mjs";
import { canonicalStrictDescendant } from "./filesystem-containment.mjs";

/** @typedef {"bun" | "npm" | "pnpm"} PackageManager */

export const root = fileURLToPath(new URL("../", import.meta.url));

/** @param {string} message @returns {never} */
export const fail = (message) => {
  throw new Error(message);
};

/** @param {string} command @param {readonly string[]} arguments_ @param {string} cwd @param {NodeJS.ProcessEnv} [env] */
export const runResult = (command, arguments_, cwd, env = process.env) =>
  spawnSync(command, arguments_, {
    cwd,
    encoding: "utf-8",
    env,
    stdio: ["ignore", "pipe", "pipe"],
  });

/**
 * @param {readonly string[]} arguments_
 * @param {{ execPath?: string, platform?: NodeJS.Platform }} [options]
 */
const resolveNpmInvocation = (
  arguments_,
  { execPath = process.execPath, platform = process.platform } = {}
) => {
  if (platform !== "win32") {
    return { arguments_, command: "npm" };
  }
  return {
    arguments_: [
      win32.resolve(
        win32.dirname(execPath),
        "node_modules",
        "npm",
        "bin",
        "npm-cli.js"
      ),
      ...arguments_,
    ],
    command: execPath,
  };
};

/**
 * @param {PackageManager} manager
 * @param {readonly string[]} arguments_
 * @param {{ execPath?: string, platform?: NodeJS.Platform }} [options]
 */
export const resolvePackageManagerInvocation = (
  manager,
  arguments_,
  options
) =>
  manager === "npm"
    ? resolveNpmInvocation(arguments_, options)
    : { arguments_, command: manager };

/**
 * @param {{ error?: NodeJS.ErrnoException, signal: NodeJS.Signals | null, status: number | null }} result
 */
const runExit = (result) => {
  if (result.error === undefined) {
    return result.signal === null
      ? `status ${result.status ?? "unknown"}`
      : `signal ${result.signal}`;
  }
  return `error ${result.error.code ?? "unknown"}: ${result.error.message}`;
};

/**
 * @param {string} command
 * @param {readonly string[]} arguments_
 * @param {{ error?: NodeJS.ErrnoException, signal: NodeJS.Signals | null, status: number | null, stderr?: string | Buffer, stdout?: string | Buffer }} result
 */
export const formatRunFailure = (command, arguments_, result) => {
  /** @param {string | Buffer | undefined} value */
  const output = (value) =>
    value === undefined ? "<unavailable>" : value.toString();
  return `${command} ${arguments_.join(" ")} failed (${runExit(result)}).\nstdout:\n${output(result.stdout)}\nstderr:\n${output(result.stderr)}`;
};

/**
 * @param {string} command
 * @param {readonly string[]} arguments_
 * @param {{ error?: NodeJS.ErrnoException, signal: NodeJS.Signals | null, status: number | null, stderr?: string | Buffer, stdout?: string | Buffer }} result
 */
export const formatRunSummary = (command, arguments_, result) => {
  /** @param {string | Buffer | undefined} value */
  const outputSize = (value) =>
    value === undefined ? "<unavailable>" : `${Buffer.byteLength(value)} bytes`;
  return `${command} ${arguments_.join(" ")} failed (${runExit(result)}; stdout ${outputSize(result.stdout)}; stderr ${outputSize(result.stderr)}).`;
};

/** @param {string} command @param {readonly string[]} arguments_ @param {string} cwd @param {NodeJS.ProcessEnv} [env] */
export const run = (command, arguments_, cwd, env = process.env) => {
  const result = runResult(command, arguments_, cwd, env);
  if (
    result.error !== undefined ||
    result.status !== 0 ||
    result.signal !== null
  ) {
    fail(formatRunFailure(command, arguments_, result));
  }
  return result.stdout;
};

/** @param {PackageManager} manager @param {readonly string[]} arguments_ @param {string} cwd @param {NodeJS.ProcessEnv} [env] */
export const runPackageManager = (
  manager,
  arguments_,
  cwd,
  env = process.env
) => {
  const invocation = resolvePackageManagerInvocation(manager, arguments_);
  return run(invocation.command, invocation.arguments_, cwd, env);
};

const artifactDirectory = () =>
  resolve(root, process.env.ASTILBA_ENV_ARTIFACT_DIR ?? ".artifacts/local");

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

export const readArtifact = async () => {
  const directory = artifactDirectory();
  const dedicatedRoot = resolve(root, ".artifacts");
  await canonicalStrictDescendant(dedicatedRoot, directory);
  const names = await readdir(directory);
  const files = names.filter((file) => file.endsWith(".tgz"));
  if (
    names.length !== 2 ||
    files.length !== 1 ||
    !names.includes("manifest.json")
  ) {
    fail("Expected exactly one packed archive and its manifest.");
  }
  const name = files[0] ?? fail("Packed archive name is missing.");
  const archive = resolve(directory, name);
  const bytes = await readFile(archive);
  const manifest = parseManifest(
    await readFile(resolve(directory, "manifest.json"))
  );
  await verifyManifestBinding(root, archive, manifest);
  return Object.freeze({
    archive,
    manifest,
    sha256: createHash("sha256").update(bytes).digest("hex"),
  });
};

export const createConsumer = async () =>
  await mkdtemp(resolve(tmpdir(), "astilba-env-public-consumer-"));

/** @param {string} directory */
export const removeConsumer = async (directory) => {
  await rm(directory, { force: true, recursive: true });
};

/** @param {string} directory @param {string} archive */
export const writeConsumerManifest = async (directory, archive) => {
  await writeFile(
    resolve(directory, "package.json"),
    `${JSON.stringify(
      {
        name: "astilba-env-public-consumer",
        private: true,
        type: "module",
        dependencies: { "@astilba/env": pathToFileURL(archive).href },
      },
      undefined,
      2
    )}\n`
  );
};

/** @param {PackageManager} manager @param {string} directory @param {readonly string[]} [additional] */
export const installArchive = (manager, directory, additional = []) => {
  const arguments_ = ["install", "--ignore-scripts", ...additional];
  runPackageManager(manager, arguments_, directory);
};

/** @param {unknown} value @returns {value is Record<string, unknown>} */
const isRecord = (value) =>
  typeof value === "object" && value !== null && !Array.isArray(value);

/**
 * @param {{
 *   expectedMode: number,
 *   installedMode: number,
 *   manager: PackageManager,
 *   path: string,
 *   platform: NodeJS.Platform
 * }} options
 */
export const isAcceptedInstalledMode = ({
  expectedMode,
  installedMode,
  manager,
  path,
  platform,
}) => {
  // Bun's manager-owned install normalization may widen its installed CLI
  // mode. The archive and manifest remain 0755; path, bytes, and hash stay exact.
  const bunNormalizedExecutable =
    manager === "bun" &&
    path === "dist/cli/astilba-env.js" &&
    expectedMode === 0o755 &&
    installedMode === 0o777;
  return (
    platform === "win32" ||
    installedMode === expectedMode ||
    bunNormalizedExecutable
  );
};

/**
 * @param {string} directory
 * @param {{ allowManagerMetadata: boolean, manager: PackageManager }} options
 */
export const assertCleanArchiveInstall = async (
  directory,
  { allowManagerMetadata, manager }
) => {
  if (allowManagerMetadata && manager !== "pnpm") {
    fail("Only the pnpm consumer may allow package-manager metadata.");
  }
  const packagePath = resolve(
    directory,
    "node_modules/@astilba/env/package.json"
  );
  const [source, canonicalPackagePath, canonicalNodeModules, artifact] =
    await Promise.all([
      readFile(packagePath, "utf-8"),
      realpath(packagePath),
      realpath(resolve(directory, "node_modules")),
      readArtifact(),
    ]);
  await canonicalStrictDescendant(canonicalNodeModules, canonicalPackagePath);
  /** @type {unknown} */
  const packageJson = JSON.parse(source);
  if (
    !isRecord(packageJson) ||
    packageJson.name !== "@astilba/env" ||
    packageJson.version !== artifact.manifest.package.version
  ) {
    fail("Consumer did not install the expected archive package.");
  }
  const packageRoot = dirname(canonicalPackagePath);
  /** @type {Map<string, import("./artifact-manifest.mjs").Entry>} */
  const expected = new Map(
    artifact.manifest.entries.map((entry) => [
      entry.path.slice("package/".length),
      entry,
    ])
  );
  const manifestContainsManagerMetadata = [...expected.keys()].some(
    (path) => path === "node_modules" || path.startsWith("node_modules/")
  );
  /** @type {string[]} */
  const seen = [];
  /** @param {string} current */
  const walk = async (current) => {
    const entries = await opendir(current);
    for await (const entry of entries) {
      const path = resolve(current, entry.name);
      const metadata = await lstat(path);
      if (metadata.isSymbolicLink()) {
        fail("Installed archive package contains a symbolic link.");
      }
      await canonicalStrictDescendant(packageRoot, path);
      const portable = relative(packageRoot, path).split(sep).join("/");
      const isAllowedManagerMetadata =
        allowManagerMetadata &&
        manager === "pnpm" &&
        current === packageRoot &&
        portable === "node_modules" &&
        !manifestContainsManagerMetadata;
      if (isAllowedManagerMetadata) {
        if (!metadata.isDirectory()) {
          fail("Installed package-manager metadata root is not a directory.");
        }
        continue;
      }
      if (metadata.isDirectory()) {
        await walk(path);
        continue;
      }
      const expectedEntry = expected.get(portable);
      if (!metadata.isFile()) {
        fail("Installed archive package differs from its exact manifest.");
      }
      if (expectedEntry === undefined) {
        throw new Error(
          "Installed archive package differs from its exact manifest."
        );
      }
      const bytes = await readFile(path);
      const installedMode = metadata.mode % 0o1000;
      if (
        bytes.byteLength !== expectedEntry.bytes ||
        createHash("sha256").update(bytes).digest("hex") !==
          expectedEntry.sha256 ||
        !isAcceptedInstalledMode({
          expectedMode: expectedEntry.mode,
          installedMode,
          manager,
          path: portable,
          platform: process.platform,
        })
      ) {
        fail(
          "Installed archive package bytes or modes differ from its manifest."
        );
      }
      seen.push(portable);
    }
  };
  await walk(packageRoot);
  const sorted = sortedStrings(seen);
  const expectedPaths = sortedStrings([...expected.keys()]);
  if (
    sorted.length !== expected.size ||
    sorted.some((path, index) => path !== expectedPaths[index])
  ) {
    fail("Installed archive package is incomplete.");
  }
};

/** @param {string} directory @param {string} source */
export const writeSmokeModule = async (directory, source) => {
  await writeFile(resolve(directory, "smoke.mjs"), source);
};
