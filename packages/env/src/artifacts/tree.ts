import { createHash } from "node:crypto";
import { constants } from "node:fs";
import { lstat, open, readdir, realpath } from "node:fs/promises";
import { isAbsolute, relative, resolve, sep } from "node:path";
import { brotliDecompressSync, gunzipSync } from "node:zlib";

type TreeFile = Readonly<{
  path: string;
  bytes: Uint8Array;
  digest: string;
}>;

export type TreeEvidence = Readonly<{
  digest: string;
  files: readonly TreeFile[];
}>;

export type InspectTreeOptions = Readonly<{
  exclude?: (path: string, kind: "directory" | "file") => boolean;
}>;

const sha256 = (value: Uint8Array | string): string =>
  createHash("sha256").update(value).digest("base64url");

const toPortablePath = (path: string): string => path.split(sep).join("/");

const isContained = (root: string, candidate: string): boolean => {
  const path = relative(root, candidate);
  return (
    path === "" ||
    (!path.startsWith(`..${sep}`) && path !== ".." && !isAbsolute(path))
  );
};

const listFiles = async (
  root: string,
  canonicalRoot: string,
  current: string,
  options: InspectTreeOptions
): Promise<string[]> => {
  const currentMetadata = await lstat(current);
  if (currentMetadata.isSymbolicLink() || !currentMetadata.isDirectory()) {
    throw new TypeError("Artifact tree directories must not be symbolic links");
  }
  const canonicalCurrent = await realpath(current);
  if (!isContained(canonicalRoot, canonicalCurrent)) {
    throw new TypeError("Artifact tree traversal escaped its root");
  }

  const entries = await readdir(current, { withFileTypes: true });
  const output: string[] = [];

  for (const entry of entries) {
    const absolute = resolve(current, entry.name);
    const metadata = await lstat(absolute);
    if (metadata.isSymbolicLink() || entry.isSymbolicLink()) {
      throw new TypeError("Artifact trees must not contain symbolic links");
    }
    const portablePath = toPortablePath(relative(root, absolute));
    if (metadata.isDirectory() && entry.isDirectory()) {
      if (options.exclude?.(portablePath, "directory") === true) {
        continue;
      }
      output.push(...(await listFiles(root, canonicalRoot, absolute, options)));
      continue;
    }
    if (metadata.isFile() && entry.isFile()) {
      if (options.exclude?.(portablePath, "file") !== true) {
        output.push(absolute);
      }
      continue;
    }
    throw new TypeError(
      "Artifact trees may contain only directories and files"
    );
  }

  return output;
};

const readStableFile = async (path: string): Promise<Uint8Array> => {
  const handle = await open(path, constants.O_RDONLY | constants.O_NOFOLLOW);
  try {
    const before = await handle.stat({ bigint: true });
    if (!before.isFile()) {
      throw new TypeError("Artifact tree entries must be regular files");
    }
    const bytes = await handle.readFile();
    const after = await handle.stat({ bigint: true });
    if (
      before.dev !== after.dev ||
      before.ino !== after.ino ||
      before.size !== after.size ||
      before.mtimeNs !== after.mtimeNs
    ) {
      throw new TypeError("Artifact file changed while it was inspected");
    }
    return bytes;
  } finally {
    await handle.close();
  }
};

export const inspectTree = async (
  root: string,
  options: InspectTreeOptions = {}
): Promise<TreeEvidence> => {
  const absoluteRoot = resolve(root);
  const rootMetadata = await lstat(absoluteRoot);
  if (rootMetadata.isSymbolicLink() || !rootMetadata.isDirectory()) {
    throw new TypeError("Artifact tree root must be a real directory");
  }
  const canonicalRoot = await realpath(absoluteRoot);
  const paths = (
    await listFiles(absoluteRoot, canonicalRoot, absoluteRoot, options)
  ).toSorted((left, right) => {
    const leftPath = toPortablePath(relative(absoluteRoot, left));
    const rightPath = toPortablePath(relative(absoluteRoot, right));
    if (leftPath < rightPath) {
      return -1;
    }
    return leftPath > rightPath ? 1 : 0;
  });
  const files: TreeFile[] = [];

  for (const path of paths) {
    const bytes = await readStableFile(path);
    files.push({
      bytes,
      digest: sha256(bytes),
      path: toPortablePath(relative(absoluteRoot, path)),
    });
  }

  const canonicalIndex = JSON.stringify(
    files.map((file) => [file.path, file.digest])
  );

  return Object.freeze({
    digest: `sha256-${sha256(canonicalIndex)}`,
    files: Object.freeze(files),
  });
};

export type Leak = Readonly<{
  file: string;
  needle: string;
}>;

const MAXIMUM_DECOMPRESSED_SCAN_BYTES = 64 * 1024 * 1024;

const decompressedBytes = (file: TreeFile): readonly Uint8Array[] => {
  const lowerPath = file.path.toLowerCase();
  if (lowerPath.endsWith(".br")) {
    return [
      brotliDecompressSync(file.bytes, {
        maxOutputLength: MAXIMUM_DECOMPRESSED_SCAN_BYTES,
      }),
    ];
  }
  if (lowerPath.endsWith(".gz") || lowerPath.endsWith(".tgz")) {
    return [
      gunzipSync(file.bytes, {
        maxOutputLength: MAXIMUM_DECOMPRESSED_SCAN_BYTES,
      }),
    ];
  }
  return [];
};

export const scanTree = async (
  root: string,
  needles: readonly string[]
): Promise<readonly Leak[]> => {
  const tree = await inspectTree(root);
  const leaks: Leak[] = [];
  const encodedNeedles = needles.map((needle) => ({
    bytes: Buffer.from(needle, "utf-8"),
    needle,
  }));

  for (const file of tree.files) {
    const representations = [file.bytes, ...decompressedBytes(file)];
    for (const { bytes: needleBytes, needle } of encodedNeedles) {
      if (
        representations.some((bytes) =>
          Buffer.from(bytes).includes(needleBytes)
        )
      ) {
        leaks.push({ file: file.path, needle });
      }
    }
  }

  return Object.freeze(leaks);
};
