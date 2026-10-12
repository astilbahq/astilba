import { constants } from "node:fs";
import { lstat, mkdir, open, opendir, realpath, rm } from "node:fs/promises";
import { isAbsolute, relative, resolve, sep } from "node:path";

import {
  BoundedJsonFailure,
  parseBoundedJsonValue,
} from "../core/bounded-json.ts";
import { isLocalId } from "../core/identity.ts";
import { canonicalJson } from "../core/json.ts";
import type { JsonObject, JsonValue } from "../core/types.ts";

const GENERATED_FORMAT = "astilba.env.generated/v1";
const GENERATED_FORMAT_VERSION =
  /^astilba\.env\.generated\/v(?<version>[1-9][0-9]*)$/u;
const MAXIMUM_DIRECTORIES = 2;
const MAXIMUM_FILE_BYTES = 8_388_608;
const MAXIMUM_FILES = 2048;
const MAXIMUM_LISTED_FILES = MAXIMUM_FILES - 1;
const MAXIMUM_NODES = MAXIMUM_FILES + MAXIMUM_DIRECTORIES;
const MAXIMUM_TREE_BYTES = 67_108_864;

const SERIAL_JSON_LIMITS = Object.freeze({
  maximumArrayItems: 65_536,
  maximumBytes: MAXIMUM_FILE_BYTES,
  maximumContainerItems: 262_144,
  maximumDepth: 64,
  maximumObjectKeys: 262_144,
  maximumStringBytes: 1_048_576,
});

const isMissing = (error: unknown): error is NodeJS.ErrnoException =>
  error instanceof Error && "code" in error && error.code === "ENOENT";

export type GeneratedDirectoryFailureCode =
  | "ENV_GENERATED_FORMAT_UNSUPPORTED"
  | "ENV_GENERATED_INVALID";

export class GeneratedDirectoryFailure extends Error {
  readonly code: GeneratedDirectoryFailureCode;

  constructor(code: GeneratedDirectoryFailureCode) {
    super(code);
    this.name = "GeneratedDirectoryFailure";
    this.code = code;
  }
}

export type GeneratedDirectoryState = Readonly<{
  exists: boolean;
  files: readonly string[];
  manifestSource: string | null;
}>;

const fail = (code: GeneratedDirectoryFailureCode): never => {
  throw new GeneratedDirectoryFailure(code);
};

const metadataOrUndefined = async (
  path: string
): Promise<Awaited<ReturnType<typeof lstat>> | undefined> => {
  try {
    return await lstat(path);
  } catch (error) {
    if (isMissing(error)) {
      return undefined;
    }
    throw error;
  }
};

const readStableRegularFile = async (path: string): Promise<Uint8Array> => {
  // oxlint-disable-next-line no-bitwise -- POSIX open flags are a bit field; O_NOFOLLOW must be combined with O_RDONLY.
  const handle = await open(path, constants.O_RDONLY | constants.O_NOFOLLOW);
  try {
    const before = await handle.stat({ bigint: true });
    if (!before.isFile() || before.size > BigInt(MAXIMUM_FILE_BYTES)) {
      return fail("ENV_GENERATED_INVALID");
    }
    const bytes = await handle.readFile();
    const after = await handle.stat({ bigint: true });
    if (
      before.dev !== after.dev ||
      before.ino !== after.ino ||
      before.size !== after.size ||
      before.mtimeNs !== after.mtimeNs
    ) {
      return fail("ENV_GENERATED_INVALID");
    }
    return bytes;
  } finally {
    await handle.close();
  }
};

const containedSegments = (
  trustedRoot: string,
  target: string
): readonly string[] => {
  const path = relative(trustedRoot, target);
  if (
    path === "" ||
    path === ".." ||
    path.startsWith(`..${sep}`) ||
    isAbsolute(path)
  ) {
    return fail("ENV_GENERATED_INVALID");
  }
  return path.split(sep);
};

const assertRealDirectory = async (path: string): Promise<void> => {
  let metadata;
  try {
    metadata = await lstat(path);
  } catch {
    return fail("ENV_GENERATED_INVALID");
  }
  if (metadata.isSymbolicLink() || !metadata.isDirectory()) {
    fail("ENV_GENERATED_INVALID");
  }
};

const canonicalRootAndSegments = async (
  trustedRoot: string,
  target: string
): Promise<
  Readonly<{
    root: string;
    segments: readonly string[];
    target: string;
  }>
> => {
  const absoluteRoot = resolve(trustedRoot);
  const absoluteTarget = resolve(target);
  const segments = containedSegments(absoluteRoot, absoluteTarget);

  await assertRealDirectory(absoluteRoot);
  const canonicalRoot = await realpath(absoluteRoot);

  let current = canonicalRoot;
  for (const segment of segments) {
    current = resolve(current, segment);
    const metadata = await metadataOrUndefined(current);
    if (metadata === undefined) {
      break;
    }
    if (metadata.isSymbolicLink() || !metadata.isDirectory()) {
      fail("ENV_GENERATED_INVALID");
    }
    const canonicalCurrent = await realpath(current);
    const canonicalPath = relative(canonicalRoot, canonicalCurrent);
    if (
      canonicalPath === ".." ||
      canonicalPath.startsWith(`..${sep}`) ||
      isAbsolute(canonicalPath)
    ) {
      fail("ENV_GENERATED_INVALID");
    }
  }

  return Object.freeze({
    root: canonicalRoot,
    segments,
    target: resolve(canonicalRoot, ...segments),
  });
};

const isJsonObject = (value: JsonValue): value is JsonObject =>
  typeof value === "object" && value !== null && !Array.isArray(value);

const exactKeys = (value: JsonObject, expected: readonly string[]): boolean => {
  const keys = Object.keys(value);
  return (
    keys.length === expected.length &&
    keys.every((key) => expected.includes(key))
  );
};

const matchesLocalIdPath = (
  path: string,
  prefix: string,
  suffix: string
): boolean => {
  if (!path.startsWith(prefix) || !path.endsWith(suffix)) {
    return false;
  }
  return isLocalId(path.slice(prefix.length, -suffix.length));
};

const generatedPath = (path: string): boolean => {
  if (path === "contract.json" || path === "snapshot.json") {
    return true;
  }
  return (
    matchesLocalIdPath(path, "consumers/", ".public.json") ||
    matchesLocalIdPath(path, "consumers/", ".server.json") ||
    matchesLocalIdPath(path, "browser/", ".build.ts") ||
    matchesLocalIdPath(path, "browser/", ".deployment.ts") ||
    matchesLocalIdPath(path, "browser/", ".request.ts") ||
    matchesLocalIdPath(path, "", ".server.ts")
  );
};

const decodeManifest = (bytes: Uint8Array): string => {
  if (
    bytes.byteLength > MAXIMUM_FILE_BYTES ||
    (bytes[0] === 0xef && bytes[1] === 0xbb && bytes[2] === 0xbf)
  ) {
    return fail("ENV_GENERATED_INVALID");
  }
  try {
    return new TextDecoder("utf-8", {
      fatal: true,
      ignoreBOM: true,
    }).decode(bytes);
  } catch {
    return fail("ENV_GENERATED_INVALID");
  }
};

const parseManifest = (source: string): readonly string[] => {
  let parsed: JsonValue;
  try {
    parsed = parseBoundedJsonValue(source, SERIAL_JSON_LIMITS);
  } catch (error) {
    if (error instanceof BoundedJsonFailure) {
      return fail("ENV_GENERATED_INVALID");
    }
    throw error;
  }
  if (!isJsonObject(parsed)) {
    return fail("ENV_GENERATED_INVALID");
  }

  const { format } = parsed;
  const version =
    typeof format === "string"
      ? GENERATED_FORMAT_VERSION.exec(format)?.groups?.version
      : undefined;
  if (version !== undefined && version !== "1") {
    return fail("ENV_GENERATED_FORMAT_UNSUPPORTED");
  }
  if (`${canonicalJson(parsed)}\n` !== source) {
    return fail("ENV_GENERATED_INVALID");
  }
  if (
    !exactKeys(parsed, ["files", "format"]) ||
    format !== GENERATED_FORMAT ||
    !Array.isArray(parsed.files) ||
    parsed.files.length > MAXIMUM_LISTED_FILES
  ) {
    return fail("ENV_GENERATED_INVALID");
  }

  const files: string[] = [];
  let previous: string | undefined;
  for (const item of parsed.files) {
    if (
      typeof item !== "string" ||
      !generatedPath(item) ||
      item === "manifest.json" ||
      (previous !== undefined && item <= previous)
    ) {
      return fail("ENV_GENERATED_INVALID");
    }
    files.push(item);
    previous = item;
  }
  return Object.freeze(files);
};

const requiredDirectories = (files: readonly string[]): ReadonlySet<string> => {
  const directories = new Set<string>();
  for (const path of files) {
    let slash = path.lastIndexOf("/");
    while (slash !== -1) {
      const directory = path.slice(0, slash);
      directories.add(directory);
      slash = directory.lastIndexOf("/");
    }
  }
  if (directories.size > MAXIMUM_DIRECTORIES) {
    return fail("ENV_GENERATED_INVALID");
  }
  return directories;
};

interface TreeState {
  readonly directories: string[];
  readonly files: string[];
  nodes: number;
  totalBytes: number;
}

const walkTreeDirectory = async (
  root: string,
  current: string,
  expectedDirectories: ReadonlySet<string>,
  expectedFiles: ReadonlySet<string>,
  state: TreeState
): Promise<void> => {
  let directory;
  try {
    directory = await opendir(current);
  } catch {
    return fail("ENV_GENERATED_INVALID");
  }

  for await (const entry of directory) {
    state.nodes += 1;
    if (state.nodes > MAXIMUM_NODES) {
      return fail("ENV_GENERATED_INVALID");
    }
    const path = resolve(current, entry.name);
    const metadata = await metadataOrUndefined(path);
    if (metadata === undefined) {
      return fail("ENV_GENERATED_INVALID");
    }
    if (metadata.isSymbolicLink()) {
      return fail("ENV_GENERATED_INVALID");
    }
    const relativePath = relative(root, path).split(sep).join("/");
    if (metadata.isDirectory()) {
      if (!expectedDirectories.has(relativePath)) {
        return fail("ENV_GENERATED_INVALID");
      }
      state.directories.push(relativePath);
      await walkTreeDirectory(
        root,
        path,
        expectedDirectories,
        expectedFiles,
        state
      );
      continue;
    }
    const size = metadata.size;
    if (
      !metadata.isFile() ||
      !expectedFiles.has(relativePath) ||
      typeof size !== "number" ||
      size > MAXIMUM_FILE_BYTES
    ) {
      return fail("ENV_GENERATED_INVALID");
    }
    state.files.push(relativePath);
    state.totalBytes += size;
    if (state.totalBytes > MAXIMUM_TREE_BYTES) {
      return fail("ENV_GENERATED_INVALID");
    }
  }
};

const walkTree = async (
  root: string,
  expectedDirectories: ReadonlySet<string>,
  expectedFiles: ReadonlySet<string>
): Promise<
  Readonly<{
    directories: readonly string[];
    files: readonly string[];
    totalBytes: number;
  }>
> => {
  const state: TreeState = {
    directories: [],
    files: [],
    nodes: 0,
    totalBytes: 0,
  };
  await walkTreeDirectory(
    root,
    root,
    expectedDirectories,
    expectedFiles,
    state
  );
  return Object.freeze({
    directories: Object.freeze(state.directories.toSorted()),
    files: Object.freeze(state.files.toSorted()),
    totalBytes: state.totalBytes,
  });
};

export const inspectGeneratedDirectory = async (
  trustedRoot: string,
  target: string
): Promise<GeneratedDirectoryState> => {
  const location = await canonicalRootAndSegments(trustedRoot, target);
  const targetMetadata = await metadataOrUndefined(location.target);
  if (targetMetadata === undefined) {
    return Object.freeze({
      exists: false,
      files: Object.freeze([]),
      manifestSource: null,
    });
  }
  if (targetMetadata.isSymbolicLink() || !targetMetadata.isDirectory()) {
    return fail("ENV_GENERATED_INVALID");
  }

  const manifestPath = resolve(location.target, "manifest.json");
  let manifestBytes: Uint8Array;
  try {
    manifestBytes = await readStableRegularFile(manifestPath);
  } catch {
    return fail("ENV_GENERATED_INVALID");
  }
  const manifestSource = decodeManifest(manifestBytes);
  const ownedFiles = parseManifest(manifestSource);
  const expectedFiles = new Set([...ownedFiles, "manifest.json"]);
  const expectedDirectories = requiredDirectories(ownedFiles);
  const tree = await walkTree(
    location.target,
    expectedDirectories,
    expectedFiles
  );
  const sortedExpectedFiles = [...expectedFiles].toSorted();
  if (
    tree.files.length !== sortedExpectedFiles.length ||
    tree.files.some((path, index) => path !== sortedExpectedFiles[index])
  ) {
    return fail("ENV_GENERATED_INVALID");
  }

  if (
    tree.directories.length !== expectedDirectories.size ||
    tree.directories.some((path) => !expectedDirectories.has(path))
  ) {
    return fail("ENV_GENERATED_INVALID");
  }

  return Object.freeze({
    exists: true,
    files: ownedFiles,
    manifestSource,
  });
};

const sameState = (
  left: GeneratedDirectoryState,
  right: GeneratedDirectoryState
): boolean =>
  left.exists === right.exists &&
  left.manifestSource === right.manifestSource &&
  left.files.length === right.files.length &&
  left.files.every((path, index) => path === right.files[index]);

const createMissingAncestors = async (
  root: string,
  segments: readonly string[]
): Promise<void> => {
  let current = root;
  for (const segment of segments.slice(0, -1)) {
    current = resolve(current, segment);
    const metadata = await metadataOrUndefined(current);
    if (metadata === undefined) {
      try {
        await mkdir(current);
      } catch (error) {
        if (
          !(
            error instanceof Error &&
            "code" in error &&
            error.code === "EEXIST"
          )
        ) {
          throw error;
        }
      }
    }
    await assertRealDirectory(current);
  }
};

export const replaceGeneratedDirectory = async (
  trustedRoot: string,
  target: string,
  previous: GeneratedDirectoryState
): Promise<void> => {
  const location = await canonicalRootAndSegments(trustedRoot, target);
  const current = await inspectGeneratedDirectory(trustedRoot, target);
  if (!sameState(previous, current)) {
    return fail("ENV_GENERATED_INVALID");
  }

  await createMissingAncestors(location.root, location.segments);
  if (current.exists) {
    await rm(location.target, { recursive: true });
  }
  await mkdir(location.target);
};
