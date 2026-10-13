import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import { lstat, mkdir, readFile, readdir, writeFile } from "node:fs/promises";
import nodePath from "node:path";
import { pathToFileURL } from "node:url";
import { isDeepStrictEqual } from "node:util";

import { inspectArchive } from "../../packages/env/scripts/artifact-manifest.mjs";
import { cutover, root } from "./identity.mjs";

const { isAbsolute, relative, resolve, sep } = nodePath;

export const hash = (bytes) => createHash("sha256").update(bytes).digest("hex");
const format = "astilba.ui.release-artifacts/v1";
const objectId = /^[0-9a-f]{40}$/u;
const hashId = /^[0-9a-f]{64}$/u;
const stableVersion = /^(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)$/u;
const git = (...args) =>
  execFileSync("git", args, { cwd: root, encoding: "utf-8" }).trim();

const regularBytes = async (file) => {
  const info = await lstat(file);
  if (!info.isFile() || info.isSymbolicLink() || info.size > 64 * 1024 * 1024) {
    throw new Error("Artifact must be a bounded regular file.");
  }
  return readFile(file);
};

const tar = (arguments_) =>
  execFileSync("tar", arguments_, { maxBuffer: 70 * 1024 * 1024 });

export const inspectUiArchive = (directory, archive, entries) => {
  // Reject links before the reused inspector extracts anything.
  const listing = tar(["-tvzf", archive]).toString("utf-8").trim().split("\n");
  if (listing.length === 0 || listing.some((line) => !line.startsWith("-"))) {
    throw new Error("UI archive contains a non-regular entry.");
  }
  return inspectArchive(resolve(root, directory), archive, {
    bindToSource: false,
    ...(entries ? { expectedEntries: entries } : {}),
  });
};

const packedMetadata = (archive) =>
  JSON.parse(tar(["-xOzf", archive, "package/package.json"]).toString("utf-8"));

const sourceMetadata = async (directory, version) => {
  const manifest = JSON.parse(
    await readFile(resolve(root, directory, "package.json"), "utf-8")
  );
  const result = Object.fromEntries(
    Object.entries(manifest).filter(([key]) => key !== "packageManager")
  );
  if (directory === "packages/ui") {
    if (manifest.dependencies?.["@astilba/tokens"] !== "workspace:*") {
      throw new Error("UI must retain its coordinated tokens dependency.");
    }
    result.dependencies = {
      ...manifest.dependencies,
      "@astilba/tokens": version,
    };
  }
  return result;
};

const checksumBytes = (packages) =>
  Buffer.from(
    packages.map((p) => `${p.archive.sha256}  ${p.archive.name}\n`).join("")
  );

const matchesUiPackage = (entry, expected, version) =>
  entry?.name === expected.name &&
  entry.directory === expected.directory &&
  entry.version === version &&
  entry.archive?.name ===
    `astilba-${expected.name.split("/")[1]}-${version}.tgz` &&
  hashId.test(entry.archive.sha256) &&
  Number.isSafeInteger(entry.archive.bytes) &&
  entry.archive.bytes > 0 &&
  Array.isArray(entry.entries) &&
  entry.entries.length > 0;

export const validateUiManifest = (manifest) => {
  if (
    manifest?.format !== format ||
    !objectId.test(manifest.source?.commit) ||
    !objectId.test(manifest.source?.tree) ||
    !stableVersion.test(manifest.version) ||
    !Array.isArray(manifest.packages) ||
    manifest.packages.length !== 2
  ) {
    throw new Error("UI release manifest is malformed.");
  }
  for (const [index, expected] of cutover.products.ui.packages.entries()) {
    const entry = manifest.packages[index];
    if (!matchesUiPackage(entry, expected, manifest.version)) {
      throw new Error("UI release package or archive identity is not exact.");
    }
  }
  return manifest;
};

export const verifyUiArtifacts = async (
  artifactDirectory,
  expectedManifestHash
) => {
  const directory = resolve(artifactDirectory);
  const info = await lstat(directory);
  if (
    !info.isDirectory() ||
    info.isSymbolicLink() ||
    !hashId.test(expectedManifestHash)
  ) {
    throw new Error(
      "UI artifact directory or trusted manifest digest is invalid."
    );
  }
  const bytes = await regularBytes(resolve(directory, "manifest.json"));
  if (hash(bytes) !== expectedManifestHash) {
    throw new Error(
      "Transferred UI manifest differs from the verification job digest."
    );
  }
  const manifest = validateUiManifest(JSON.parse(bytes.toString("utf-8")));
  const names = [
    ...manifest.packages.map((p) => p.archive.name),
    "manifest.json",
    "SHA256SUMS",
  ].toSorted();
  const actualNames = await readdir(directory);
  if (!isDeepStrictEqual(actualNames.toSorted(), names)) {
    throw new Error("UI artifact file set is not exact.");
  }
  if (
    git("rev-parse", "HEAD") !== manifest.source.commit ||
    git("rev-parse", "HEAD^{tree}") !== manifest.source.tree
  ) {
    throw new Error("UI artifacts are not bound to this committed checkout.");
  }
  const sums = await regularBytes(resolve(directory, "SHA256SUMS"));
  if (!sums.equals(checksumBytes(manifest.packages))) {
    throw new Error("UI checksum record differs from the manifest.");
  }
  const inspections = await Promise.allSettled(
    manifest.packages.map(async (entry) => {
      const archive = resolve(directory, entry.archive.name);
      const data = await regularBytes(archive);
      if (
        data.byteLength !== entry.archive.bytes ||
        hash(data) !== entry.archive.sha256
      ) {
        throw new Error(
          "Transferred UI archive differs from the verified bytes."
        );
      }
      // The transfer job has no build dependencies; the original manifest retains the exact entry/mode/hash list.
      await inspectUiArchive(entry.directory, archive, entry.entries);
      if (
        !isDeepStrictEqual(
          packedMetadata(archive),
          await sourceMetadata(entry.directory, manifest.version)
        )
      ) {
        throw new Error("Packed UI metadata differs from committed source.");
      }
    })
  );
  const failed = inspections.find((result) => result.status === "rejected");
  if (failed) {
    throw new Error("UI artifact inspection failed.", { cause: failed.reason });
  }
  return manifest;
};

export const packUiArtifacts = async (artifactDirectory) => {
  const directory = resolve(artifactDirectory);
  const dedicatedRoot = resolve(root, ".artifacts");
  const child = relative(dedicatedRoot, directory);
  if (
    !child ||
    child.startsWith("..") ||
    isAbsolute(child) ||
    child.includes(sep)
  ) {
    throw new Error(
      "UI artifact directory must be a new direct child of .artifacts."
    );
  }
  if (git("status", "--porcelain=v1", "--untracked-files=all")) {
    throw new Error(
      "Packing UI artifacts requires a clean committed checkout."
    );
  }
  await mkdir(dedicatedRoot, { recursive: true });
  const parent = await lstat(dedicatedRoot);
  if (!parent.isDirectory() || parent.isSymbolicLink()) {
    throw new Error("Dedicated UI artifact root is not a real directory.");
  }
  await mkdir(directory);
  const source = {
    commit: git("rev-parse", "HEAD"),
    tree: git("rev-parse", "HEAD^{tree}"),
  };
  const manifests = await Promise.all(
    cutover.products.ui.packages.map(async ({ directory: packagePath }) =>
      JSON.parse(
        await readFile(resolve(root, packagePath, "package.json"), "utf-8")
      )
    )
  );
  const [{ version }] = manifests;
  if (
    !stableVersion.test(version) ||
    manifests.some((m) => m.version !== version || m.private === true)
  ) {
    throw new Error("UI and tokens must have the same public stable version.");
  }
  const packages = await Promise.all(
    cutover.products.ui.packages.map(async (entry) => {
      const packedLog = execFileSync(
        "pnpm",
        ["--filter", entry.name, "pack", "--pack-destination", directory],
        { cwd: root, maxBuffer: 32 * 1024 * 1024 }
      );
      process.stderr.write(packedLog);
      const name = `astilba-${entry.name.split("/")[1]}-${version}.tgz`;
      const archive = resolve(directory, name);
      const data = await regularBytes(archive);
      const entries = await inspectUiArchive(entry.directory, archive);
      if (
        !isDeepStrictEqual(
          packedMetadata(archive),
          await sourceMetadata(entry.directory, version)
        )
      ) {
        throw new Error("Packed UI metadata differs from source.");
      }
      await Promise.all(
        entries
          .filter((item) => item.path !== "package/package.json")
          .map(async (file) => {
            const path = file.path.slice("package/".length);
            const sourcePath =
              path === "LICENSE"
                ? resolve(root, "LICENSE")
                : resolve(root, entry.directory, path);
            if (hash(await readFile(sourcePath)) !== file.sha256) {
              throw new Error("Packed UI file differs from its built source.");
            }
          })
      );
      return {
        ...entry,
        archive: { bytes: data.byteLength, name, sha256: hash(data) },
        entries,
        version,
      };
    })
  );
  const manifest = validateUiManifest({ format, packages, source, version });
  const bytes = Buffer.from(`${JSON.stringify(manifest, null, 2)}\n`);
  await writeFile(resolve(directory, "manifest.json"), bytes, { flag: "wx" });
  await writeFile(resolve(directory, "SHA256SUMS"), checksumBytes(packages), {
    flag: "wx",
  });
  const digest = hash(bytes);
  await verifyUiArtifacts(directory, digest);
  return {
    directory,
    manifestSha256: digest,
    packages: packages.map((p) => p.name),
  };
};

if (
  process.argv[1] &&
  import.meta.url === pathToFileURL(process.argv[1]).href
) {
  const [command, directory, digest, ...extra] = process.argv.slice(2);
  if (
    !directory ||
    extra.length ||
    !["pack", "verify"].includes(command) ||
    (command === "pack" && digest) ||
    (command === "verify" && !digest)
  ) {
    throw new Error(
      "Usage: ui-artifacts.mjs pack <directory> | verify <directory> <trusted-manifest-sha256>"
    );
  }
  const result =
    command === "pack"
      ? await packUiArtifacts(directory)
      : await verifyUiArtifacts(directory, digest);
  process.stdout.write(`${JSON.stringify(result)}\n`);
}
