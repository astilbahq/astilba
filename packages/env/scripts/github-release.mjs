// @ts-check
/// <reference types="node" />

import { readdir, readFile } from "node:fs/promises";
import { resolve } from "node:path";
import { pathToFileURL } from "node:url";
import { isDeepStrictEqual } from "node:util";

import { parseManifest, sha256 } from "./artifact-manifest.mjs";

/** @typedef {{ id: number, name: string, size: number, state: "uploaded" }} ReleaseAsset */
/** @typedef {{ assets: ReleaseAsset[], releaseId: number, tag: string }} ReleasePlan */

/** @param {string} message @returns {never} */
const fail = (message) => {
  throw new Error(message);
};

/** @param {unknown} value @returns {value is Record<string, unknown>} */
const isRecord = (value) =>
  typeof value === "object" && value !== null && !Array.isArray(value);

/** @param {unknown} value @returns {value is number} */
const isNonnegativeSafeInteger = (value) =>
  typeof value === "number" && Number.isSafeInteger(value) && value >= 0;

/** @param {unknown} value @returns {value is number} */
const isPositiveSafeInteger = (value) =>
  typeof value === "number" && Number.isSafeInteger(value) && value > 0;

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

/** @param {string} source */
const parseJson = (source) => {
  /** @type {unknown} */
  let value;
  try {
    value = JSON.parse(source);
  } catch {
    return fail("GitHub release metadata is not valid JSON.");
  }
  return value;
};

/** @param {string} artifactDirectory */
const readExpectedAssets = async (artifactDirectory) => {
  const directory = resolve(artifactDirectory);
  const manifestPath = resolve(directory, "manifest.json");
  const manifestBytes = await readFile(manifestPath);
  const manifest = parseManifest(manifestBytes);
  const archivePath = resolve(directory, manifest.archive.name);
  const archiveBytes = await readFile(archivePath);
  if (
    archiveBytes.byteLength !== manifest.archive.bytes ||
    sha256(archiveBytes) !== manifest.archive.sha256
  ) {
    return fail("Release archive does not match its manifest.");
  }
  return {
    assets: new Map([
      [manifest.archive.name, archiveBytes],
      ["manifest.json", manifestBytes],
    ]),
    version: manifest.package.version,
  };
};

/** @param {unknown} value @param {ReadonlyMap<string, Buffer>} expectedAssets @param {string} expectedTag */
const normalizeRelease = (value, expectedAssets, expectedTag) => {
  if (
    !isRecord(value) ||
    !isPositiveSafeInteger(value.id) ||
    value.tag_name !== expectedTag ||
    value.draft !== false ||
    value.prerelease !== false ||
    !Array.isArray(value.assets) ||
    value.assets.length !== expectedAssets.size
  ) {
    return fail("GitHub release state does not match the requested release.");
  }

  /** @type {ReleaseAsset[]} */
  const assets = [];
  for (const asset of value.assets) {
    if (
      !isRecord(asset) ||
      !isPositiveSafeInteger(asset.id) ||
      typeof asset.name !== "string" ||
      asset.state !== "uploaded" ||
      !isNonnegativeSafeInteger(asset.size)
    ) {
      return fail("GitHub release asset metadata is invalid.");
    }
    const expected = expectedAssets.get(asset.name);
    if (
      expected === undefined ||
      asset.size !== expected.byteLength ||
      assets.some(({ name }) => name === asset.name)
    ) {
      return fail("GitHub release assets do not match the verified artifact.");
    }
    assets.push({
      id: asset.id,
      name: asset.name,
      size: asset.size,
      state: "uploaded",
    });
  }
  assets.sort((left, right) => left.name.localeCompare(right.name, "en"));
  return {
    assets,
    releaseId: value.id,
    tag: expectedTag,
  };
};

/**
 * @param {{ artifactDirectory: string, expectedTag: string, releaseSource: string }} options
 * @returns {Promise<ReleasePlan>}
 */
export const buildReleasePlan = async ({
  artifactDirectory,
  expectedTag,
  releaseSource,
}) => {
  const expected = await readExpectedAssets(artifactDirectory);
  if (expectedTag !== `v${expected.version}`) {
    return fail("Requested release tag does not match the verified artifact.");
  }
  return normalizeRelease(
    parseJson(releaseSource),
    expected.assets,
    expectedTag
  );
};

/**
 * @param {{
 *   afterSource: string,
 *   artifactDirectory: string,
 *   beforeSource: string,
 *   downloadedDirectory: string,
 *   expectedTag: string,
 * }} options
 */
export const verifyReleaseEvidence = async ({
  afterSource,
  artifactDirectory,
  beforeSource,
  downloadedDirectory,
  expectedTag,
}) => {
  const expected = await readExpectedAssets(artifactDirectory);
  if (expectedTag !== `v${expected.version}`) {
    return fail("Requested release tag does not match the verified artifact.");
  }
  const before = normalizeRelease(
    parseJson(beforeSource),
    expected.assets,
    expectedTag
  );
  const after = normalizeRelease(
    parseJson(afterSource),
    expected.assets,
    expectedTag
  );
  if (!isDeepStrictEqual(before, after)) {
    return fail("GitHub release changed while its assets were verified.");
  }

  const downloadedEntries = await readdir(resolve(downloadedDirectory), {
    withFileTypes: true,
  });
  const downloadedNames = sortedStrings(
    downloadedEntries.map(({ name }) => name)
  );
  const expectedNames = sortedStrings([...expected.assets.keys()]);
  if (
    downloadedEntries.some((entry) => !entry.isFile()) ||
    !isDeepStrictEqual(downloadedNames, expectedNames)
  ) {
    return fail("Downloaded GitHub release asset set is not exact.");
  }
  for (const [name, expectedBytes] of expected.assets) {
    const actualBytes = await readFile(resolve(downloadedDirectory, name));
    if (!actualBytes.equals(expectedBytes)) {
      return fail("Downloaded GitHub release asset bytes do not match.");
    }
  }

  return {
    assets: expectedNames,
    passed: true,
    releaseId: after.releaseId,
    tag: after.tag,
  };
};

const main = async () => {
  const [command, ...arguments_] = process.argv.slice(2);
  if (command === "plan" && arguments_.length === 3) {
    const [releasePath, expectedTag, artifactDirectory] = arguments_;
    if (
      releasePath === undefined ||
      expectedTag === undefined ||
      artifactDirectory === undefined
    ) {
      return fail("GitHub release plan arguments are unavailable.");
    }
    const plan = await buildReleasePlan({
      artifactDirectory,
      expectedTag,
      releaseSource: await readFile(releasePath, "utf-8"),
    });
    process.stdout.write(`${JSON.stringify(plan)}\n`);
    return;
  }
  if (command === "verify" && arguments_.length === 5) {
    const [
      beforePath,
      afterPath,
      expectedTag,
      artifactDirectory,
      downloadedDirectory,
    ] = arguments_;
    if (
      beforePath === undefined ||
      afterPath === undefined ||
      expectedTag === undefined ||
      artifactDirectory === undefined ||
      downloadedDirectory === undefined
    ) {
      return fail("GitHub release verification arguments are unavailable.");
    }
    const result = await verifyReleaseEvidence({
      afterSource: await readFile(afterPath, "utf-8"),
      artifactDirectory,
      beforeSource: await readFile(beforePath, "utf-8"),
      downloadedDirectory,
      expectedTag,
    });
    process.stdout.write(`${JSON.stringify(result)}\n`);
    return;
  }
  return fail(
    "Usage: node scripts/github-release.mjs <plan|verify> <arguments...>"
  );
};

if (
  process.argv[1] !== undefined &&
  import.meta.url === pathToFileURL(process.argv[1]).href
) {
  await main();
}
