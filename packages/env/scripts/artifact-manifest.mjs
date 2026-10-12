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
  rm,
} from "node:fs/promises";
import { tmpdir } from "node:os";
import { relative, resolve, sep } from "node:path";
import { isDeepStrictEqual } from "node:util";

import { canonicalStrictDescendant } from "./filesystem-containment.mjs";

export const FORMAT = "astilba.env.release-artifact/v1";
const HASH = /^[0-9a-f]{64}$/u;
const GIT_OBJECT = /^[0-9a-f]{40}$/u;
const ARCHIVE_NAME = /^[a-z0-9][a-z0-9._-]*\.tgz$/u;
const MAXIMUM_MANIFEST_BYTES = 1_048_576;
const MAXIMUM_ARCHIVE_BYTES = 64 * 1024 * 1024;

/** @typedef {{ bytes: number, mode: 420 | 493, path: string, sha256: string }} Entry */
/** @typedef {{ commit: string, tree: string }} Source */
/** @typedef {{ name: string, version: string }} PackageIdentity */
/** @typedef {{ bytes: number, name: string, sha256: string }} Archive */
/** @typedef {{ format: string, source: Source, package: PackageIdentity, archive: Archive, entries: Entry[] }} ArtifactManifest */

/** @param {string} message @returns {never} */
const fail = (message) => {
  throw new Error(message);
};

/** @param {Uint8Array | string} value */
export const sha256 = (value) =>
  createHash("sha256").update(value).digest("hex");

/** @param {string} left @param {string} right */
const compareStrings = (left, right) => {
  if (left < right) {
    return -1;
  }
  if (left > right) {
    return 1;
  }
  return 0;
};

/** @param {readonly string[]} values */
const sortedStrings = (values) => {
  /** @type {string[]} */
  const output = [];
  for (const value of values) {
    const index = output.findIndex(
      (current) => compareStrings(value, current) < 0
    );
    if (index === -1) {
      output.push(value);
    } else {
      output.splice(index, 0, value);
    }
  }
  return output;
};

/** @param {unknown} value @returns {value is Record<string, unknown>} */
const isRecord = (value) =>
  typeof value === "object" && value !== null && !Array.isArray(value);

/** @param {Record<string, unknown>} value @param {readonly string[]} keys */
const hasExactKeys = (value, keys) => {
  const actual = sortedStrings(Object.keys(value));
  const expected = sortedStrings(keys);
  return (
    actual.length === expected.length &&
    actual.every((key, index) => key === expected[index])
  );
};

/** @param {unknown} value @returns {value is number} */
const isNonnegativeSafeInteger = (value) =>
  typeof value === "number" && Number.isSafeInteger(value) && value >= 0;

/** @param {unknown} value @returns {value is number} */
const isPositiveSafeInteger = (value) =>
  typeof value === "number" && Number.isSafeInteger(value) && value > 0;

/** @param {string} value */
const containsControl = (value) => {
  for (const character of value) {
    const code = character.codePointAt(0);
    if (code !== undefined && (code < 32 || code === 127)) {
      return true;
    }
  }
  return false;
};

/** @param {unknown} value @returns {value is string} */
const isSafeArchivePath = (value) =>
  typeof value === "string" &&
  value.startsWith("package/") &&
  !value.startsWith("/") &&
  !value.includes("\\") &&
  !value.includes("%") &&
  value.normalize("NFC") === value &&
  value
    .split("/")
    .every(
      (segment) =>
        segment !== "" &&
        segment !== "." &&
        segment !== ".." &&
        !containsControl(segment)
    );

/** @param {unknown} value @returns {value is Entry} */
const isEntry = (value) =>
  isRecord(value) &&
  hasExactKeys(value, ["bytes", "mode", "path", "sha256"]) &&
  isNonnegativeSafeInteger(value.bytes) &&
  (value.mode === 0o644 || value.mode === 0o755) &&
  isSafeArchivePath(value.path) &&
  typeof value.sha256 === "string" &&
  HASH.test(value.sha256);

/** @param {unknown} value @returns {value is ArtifactManifest} */
const isManifest = (value) => {
  if (
    !isRecord(value) ||
    !hasExactKeys(value, [
      "archive",
      "entries",
      "format",
      "package",
      "source",
    ]) ||
    value.format !== FORMAT ||
    !isRecord(value.source) ||
    !hasExactKeys(value.source, ["commit", "tree"]) ||
    typeof value.source.commit !== "string" ||
    !GIT_OBJECT.test(value.source.commit) ||
    typeof value.source.tree !== "string" ||
    !GIT_OBJECT.test(value.source.tree) ||
    !isRecord(value.package) ||
    !hasExactKeys(value.package, ["name", "version"]) ||
    value.package.name !== "@astilba/env" ||
    typeof value.package.version !== "string" ||
    !/^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$/u.test(value.package.version) ||
    !isRecord(value.archive) ||
    !hasExactKeys(value.archive, ["bytes", "name", "sha256"]) ||
    typeof value.archive.name !== "string" ||
    !ARCHIVE_NAME.test(value.archive.name) ||
    !isPositiveSafeInteger(value.archive.bytes) ||
    value.archive.bytes > MAXIMUM_ARCHIVE_BYTES ||
    typeof value.archive.sha256 !== "string" ||
    !HASH.test(value.archive.sha256) ||
    !Array.isArray(value.entries) ||
    value.entries.length === 0 ||
    value.entries.length > 256 ||
    !value.entries.every(isEntry)
  ) {
    return false;
  }
  const entries = value.entries;
  return entries.every(
    (entry, index) =>
      index === 0 || (entries[index - 1]?.path ?? "") < entry.path
  );
};

/** @param {ArtifactManifest} manifest */
export const encodeManifest = (manifest) => {
  if (!isManifest(manifest)) {
    return fail("Release artifact manifest is invalid.");
  }
  return `${JSON.stringify(manifest, null, 2)}\n`;
};

/** @param {Uint8Array | string} source @returns {ArtifactManifest} */
export const parseManifest = (source) => {
  const text =
    typeof source === "string" ? source : Buffer.from(source).toString("utf-8");
  if (
    (typeof source === "string"
      ? Buffer.byteLength(source)
      : source.byteLength) > MAXIMUM_MANIFEST_BYTES
  ) {
    return fail("Release artifact manifest is too large.");
  }
  /** @type {unknown} */
  let value;
  try {
    value = JSON.parse(text);
  } catch {
    return fail("Release artifact manifest is not valid JSON.");
  }
  if (!isManifest(value) || encodeManifest(value) !== text) {
    return fail("Release artifact manifest has an invalid shape.");
  }
  return value;
};

/** @param {readonly string[]} arguments_ @param {string} cwd */
const git = (arguments_, cwd) => {
  // oxlint-disable-next-line sonarjs/no-os-command-from-path -- Git is the required source identity authority.
  const result = spawnSync("git", ["-C", cwd, ...arguments_], {
    encoding: "utf-8",
    stdio: ["ignore", "pipe", "pipe"],
  });
  if (
    result.status !== 0 ||
    result.signal !== null ||
    typeof result.stdout !== "string"
  ) {
    return fail("Release source Git identity is unavailable.");
  }
  return result.stdout.trimEnd();
};

/** @param {string} root */
export const captureSource = async (root) => {
  const commit = git(["rev-parse", "--verify", "HEAD^{commit}"], root);
  const tree = git(["rev-parse", "--verify", "HEAD^{tree}"], root);
  if (
    !GIT_OBJECT.test(commit) ||
    !GIT_OBJECT.test(tree) ||
    git(["status", "--porcelain=v1", "--untracked-files=all"], root) !== ""
  ) {
    return fail("Release source must be a clean committed checkout.");
  }
  const packagePath = resolve(root, "package.json");
  const gitRoot = git(["rev-parse", "--show-toplevel"], root);
  const committedMetadata = await canonicalStrictDescendant(
    gitRoot,
    packagePath
  );
  const committedPath = committedMetadata.relative.split(sep).join("/");
  const currentPackage = await readFile(packagePath);
  const committedPackage = spawnSync(
    // oxlint-disable-next-line sonarjs/no-os-command-from-path -- Git supplies the committed package bytes.
    "git",
    ["-C", root, "show", `${commit}:${committedPath}`],
    { encoding: null, stdio: ["ignore", "pipe", "pipe"] }
  );
  if (
    committedPackage.status !== 0 ||
    !(committedPackage.stdout instanceof Buffer) ||
    !committedPackage.stdout.equals(currentPackage)
  ) {
    return fail("Release package metadata is not the committed source.");
  }
  /** @type {unknown} */
  const packageJson = JSON.parse(currentPackage.toString("utf-8"));
  if (
    !isRecord(packageJson) ||
    packageJson.name !== "@astilba/env" ||
    typeof packageJson.version !== "string"
  ) {
    return fail("Release package identity is invalid.");
  }
  return Object.freeze({
    source: Object.freeze({ commit, tree }),
    package: Object.freeze({
      name: packageJson.name,
      version: packageJson.version,
    }),
  });
};

/** @param {readonly string[]} arguments_ */
const tar = (arguments_) => {
  // oxlint-disable-next-line sonarjs/no-os-command-from-path -- tar inspects the archive after safe path validation.
  const result = spawnSync("tar", arguments_, {
    encoding: "utf-8",
    env: { ...process.env, LC_ALL: "C", TZ: "UTC" },
    stdio: ["ignore", "pipe", "pipe"],
  });
  if (
    result.status !== 0 ||
    result.signal !== null ||
    typeof result.stdout !== "string"
  ) {
    return fail("Release archive could not be inspected.");
  }
  return result.stdout;
};

/** @param {string} source */
const lines = (source) => {
  const normalized = source.replaceAll("\r\n", "\n").trimEnd();
  if (normalized === "") {
    return [];
  }
  const output = normalized.split("\n");
  return output.some((line) => line === "")
    ? fail("Release archive listing is invalid.")
    : output;
};

/** @param {string} metadata @param {string} path */
const parseVerboseEntry = (metadata, path) => {
  const numeric =
    /^(?<permissions>-[rwx-]{9})\s+\d+\s+\d+\s+\d+\s+(?<bytes>\d+)\s+.+\s(?<path>.+)$/u;
  const gnu =
    /^(?<permissions>-[rwx-]{9})\s+\S+\/\S+\s+(?<bytes>\d+)\s+.+\s(?<path>.+)$/u;
  const groups = numeric.exec(metadata)?.groups ?? gnu.exec(metadata)?.groups;
  if (
    groups === undefined ||
    groups.path !== path ||
    (groups.permissions !== "-rw-r--r--" && groups.permissions !== "-rwxr-xr-x")
  ) {
    return fail("Release archive contains a non-regular or malformed entry.");
  }
  const bytes = Number(groups.bytes);
  if (!Number.isSafeInteger(bytes) || bytes < 0) {
    return fail("Release archive contains an invalid file size.");
  }
  return Object.freeze({
    bytes,
    mode: groups.permissions === "-rwxr-xr-x" ? 0o755 : 0o644,
  });
};

/** @param {string} directory */
const walkFiles = async (directory) => {
  /** @type {string[]} */
  const files = [];
  /** @param {string} current */
  const walk = async (current) => {
    const entries = await opendir(current);
    for await (const entry of entries) {
      const path = resolve(current, entry.name);
      const metadata = await lstat(path);
      if (metadata.isSymbolicLink()) {
        return fail("Release tree contains a symbolic link.");
      }
      await canonicalStrictDescendant(directory, path);
      if (metadata.isDirectory()) {
        await walk(path);
      } else if (metadata.isFile()) {
        files.push(relative(directory, path).split(sep).join("/"));
      } else {
        return fail("Release tree contains an unsupported filesystem node.");
      }
    }
  };
  await walk(directory);
  return sortedStrings(files);
};

/** @param {string} root */
const expectedPackagePaths = async (root) => {
  const dist = resolve(root, "dist");
  const distFiles = await walkFiles(dist);
  return sortedStrings([
    "package/LICENSE",
    "package/README.md",
    ...distFiles.map((path) => `package/dist/${path}`),
    "package/package.json",
  ]);
};

/** @param {string} root @param {string} extractedRoot @param {string} path */
const verifySourceBinding = async (root, extractedRoot, path) => {
  const packed = await readFile(resolve(extractedRoot, path));
  if (path === "package/package.json") {
    /** @type {unknown} */
    const packedJson = JSON.parse(packed.toString("utf-8"));
    /** @type {unknown} */
    const sourceJson = JSON.parse(
      await readFile(resolve(root, "package.json"), "utf-8")
    );
    if (!isRecord(packedJson) || !isRecord(sourceJson)) {
      return fail("Packed package metadata is invalid.");
    }
    const expected = Object.fromEntries(
      Object.entries(sourceJson).filter(([key]) => key !== "packageManager")
    );
    if (
      !isDeepStrictEqual(packedJson, expected) ||
      JSON.stringify(packedJson, null, 2) !== packed.toString("utf-8")
    ) {
      return fail("Packed package metadata differs from its public source.");
    }
    return packed;
  }
  const sourcePath = resolve(root, path.slice("package/".length));
  const source = await readFile(sourcePath);
  if (!source.equals(packed)) {
    return fail("Packed file differs from its public source.");
  }
  return packed;
};

/**
 * @param {string} root
 * @param {string} archive
 * @param {{ bindToSource?: boolean, expectedEntries?: readonly Entry[] }} [options]
 * @returns {Promise<Entry[]>}
 */
export const inspectArchive = async (root, archive, options = {}) => {
  const archiveMetadata = await lstat(archive);
  if (
    archiveMetadata.isSymbolicLink() ||
    !archiveMetadata.isFile() ||
    archiveMetadata.size <= 0 ||
    archiveMetadata.size > MAXIMUM_ARCHIVE_BYTES
  ) {
    return fail("Release archive is not a bounded regular file.");
  }
  const listing = lines(tar(["-tzf", archive]));
  const verbose = lines(tar(["-tvzf", archive]));
  if (
    listing.length === 0 ||
    listing.length !== verbose.length ||
    new Set(listing).size !== listing.length ||
    listing.some((path) => !isSafeArchivePath(path))
  ) {
    return fail("Release archive path allowlist is invalid.");
  }
  const expected =
    options.expectedEntries === undefined
      ? await expectedPackagePaths(root)
      : options.expectedEntries.map((entry) => entry.path);
  if (
    sortedStrings(listing).some((path, index) => path !== expected[index]) ||
    listing.length !== expected.length
  ) {
    return fail("Release archive differs from the exact public allowlist.");
  }
  const temporary = await mkdtemp(resolve(tmpdir(), "astilba-env-archive-"));
  try {
    tar(["-xzf", archive, "-C", temporary]);
    const extracted = await realpath(temporary);
    const actual = await walkFiles(extracted);
    if (
      actual.length !== expected.length ||
      actual.some((path, index) => path !== expected[index])
    ) {
      return fail("Extracted release tree differs from its allowlist.");
    }
    /** @type {Entry[]} */
    const entries = [];
    for (const [index, path] of listing.entries()) {
      const declared = parseVerboseEntry(verbose[index] ?? "", path);
      const extractedPath = resolve(extracted, path);
      await canonicalStrictDescendant(extracted, extractedPath);
      const metadata = await lstat(extractedPath);
      if (metadata.isSymbolicLink() || !metadata.isFile()) {
        return fail("Extracted release entry is not a regular file.");
      }
      const bytes =
        options.bindToSource === false
          ? await readFile(extractedPath)
          : await verifySourceBinding(root, extracted, path);
      const expectedEntry = options.expectedEntries?.find(
        (entry) => entry.path === path
      );
      if (
        options.expectedEntries !== undefined &&
        (expectedEntry === undefined ||
          expectedEntry.bytes !== bytes.byteLength ||
          expectedEntry.mode !== declared.mode ||
          expectedEntry.sha256 !== sha256(bytes))
      ) {
        return fail("Release archive differs from its declared entries.");
      }
      const expectedMode =
        path === "package/dist/cli/astilba-env.js" ? 0o755 : 0o644;
      if (
        declared.bytes !== bytes.byteLength ||
        declared.mode !== expectedMode ||
        (process.platform !== "win32" &&
          metadata.mode % 0o1000 !== expectedMode)
      ) {
        return fail("Release archive file metadata is invalid.");
      }
      entries.push({
        bytes: bytes.byteLength,
        mode: expectedMode,
        path,
        sha256: sha256(bytes),
      });
    }
    /** @type {Entry[]} */
    const ordered = [];
    for (const entry of entries) {
      const index = ordered.findIndex(
        (current) => compareStrings(entry.path, current.path) < 0
      );
      if (index === -1) {
        ordered.push(entry);
      } else {
        ordered.splice(index, 0, entry);
      }
    }
    return ordered;
  } finally {
    await rm(temporary, { force: true, recursive: true });
  }
};

/**
 * @param {string} root
 * @param {string} archive
 * @param {ArtifactManifest} manifest
 */
export const verifyManifestBinding = async (root, archive, manifest) => {
  const checkout = await captureSource(root);
  const bytes = await readFile(archive);
  const metadata = await lstat(archive);
  if (
    manifest.source.commit !== checkout.source.commit ||
    manifest.source.tree !== checkout.source.tree ||
    manifest.package.name !== checkout.package.name ||
    manifest.package.version !== checkout.package.version ||
    manifest.archive.name !== archive.split(sep).at(-1) ||
    manifest.archive.bytes !== metadata.size ||
    manifest.archive.sha256 !== sha256(bytes)
  ) {
    return fail("Release artifact manifest is not bound to this source.");
  }
  const entries = await inspectArchive(root, archive, {
    bindToSource: false,
    expectedEntries: manifest.entries,
  });
  if (JSON.stringify(entries) !== JSON.stringify(manifest.entries)) {
    return fail("Release archive entries differ from their manifest.");
  }
};
