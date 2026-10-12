// @ts-check
/// <reference types="node" />

import { access, readFile } from "node:fs/promises";
import { resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { assertToolContainment } from "./tool-containment.mjs";

const root = fileURLToPath(new URL("../", import.meta.url));

/**
 * @param {string} message
 * @returns {never}
 */
const fail = (message) => {
  throw new Error(message);
};

/**
 * @param {string} value
 * @returns {[number, number, number]}
 */
const parseVersion = (value) => {
  const match = /^(?<major>\d+)\.(?<minor>\d+)\.(?<patch>\d+)$/u.exec(value);
  const groups = match?.groups;
  if (groups === undefined) {
    throw new TypeError(`Unsupported version format: ${value}`);
  }
  /** @type {unknown} */
  const major = Object.getOwnPropertyDescriptor(groups, "major")?.value;
  /** @type {unknown} */
  const minor = Object.getOwnPropertyDescriptor(groups, "minor")?.value;
  /** @type {unknown} */
  const patch = Object.getOwnPropertyDescriptor(groups, "patch")?.value;
  if (
    typeof major !== "string" ||
    typeof minor !== "string" ||
    typeof patch !== "string"
  ) {
    throw new TypeError(`Unsupported version format: ${value}`);
  }
  return [Number(major), Number(minor), Number(patch)];
};

/** @param {[number, number, number]} version */
const supportedNode = (version) => {
  const [major, minor] = version;
  return (major === 22 && minor >= 14) || major === 24 || major === 26;
};

/**
 * @param {string} packageName
 * @param {string} version
 */
const assertInstalledPackage = async (packageName, version) => {
  const packagePath = resolve(
    root,
    "node_modules",
    packageName,
    "package.json"
  );
  /** @type {unknown} */
  const packageJson = JSON.parse(await readFile(packagePath, "utf-8"));
  if (
    typeof packageJson !== "object" ||
    packageJson === null ||
    Array.isArray(packageJson)
  ) {
    fail(`${packageName} package metadata must be an object.`);
  }
  const versionDescriptor = Object.getOwnPropertyDescriptor(
    packageJson,
    "version"
  );
  if (
    versionDescriptor === undefined ||
    !("value" in versionDescriptor) ||
    typeof versionDescriptor.value !== "string" ||
    versionDescriptor.value !== version
  ) {
    fail(`${packageName} must resolve to ${version}.`);
  }
  await assertToolContainment(root, packagePath);
};

if (process.release.name !== "node") {
  fail("verify-tools must run under Node.js.");
}

const nodeVersion = parseVersion(process.versions.node);
if (!supportedNode(nodeVersion)) {
  fail(
    `Node ${process.versions.node} is outside the supported release matrix.`
  );
}

const installed = await Promise.all(
  /** @type {const} */ ([
    ["typescript", "6.0.3"],
    ["typescript-7", "7.0.2"],
    ["vite", "8.1.5"],
    ["workerd", "1.20260724.1"],
  ]).map(async ([packageName, version]) => {
    await assertInstalledPackage(packageName, version);
    return { packageName, version };
  })
);

await Promise.all([
  access(resolve(root, "node_modules/typescript/bin/tsc")),
  access(resolve(root, "node_modules/typescript-7/bin/tsc")),
]);

process.stdout.write(
  `${JSON.stringify({
    node: process.versions.node,
    packages: installed.map(({ packageName, version }) => ({
      packageName,
      version,
    })),
  })}\n`
);
