// @ts-check
/// <reference types="node" />

import {
  lstat,
  mkdir,
  readFile,
  realpath,
  readdir,
  writeFile,
} from "node:fs/promises";
import { isAbsolute, relative, resolve, sep } from "node:path";
import { fileURLToPath } from "node:url";

import {
  FORMAT,
  captureSource,
  encodeManifest,
  inspectArchive,
  sha256,
} from "./artifact-manifest.mjs";
import {
  canonicalStrictDescendant,
  strictDescendant,
} from "./filesystem-containment.mjs";
import { run } from "./matrix-artifact.mjs";

export const root = fileURLToPath(new URL("../", import.meta.url));

export const prepareArtifactDirectory = async () => {
  const dedicatedRoot = resolve(root, ".artifacts");
  await mkdir(dedicatedRoot, { recursive: true });
  const rootMetadata = await lstat(dedicatedRoot);
  if (rootMetadata.isSymbolicLink() || !rootMetadata.isDirectory()) {
    throw new Error("Dedicated artifact root is not a real directory.");
  }
  const canonicalRoot = await realpath(dedicatedRoot);
  const selected = resolve(
    root,
    process.env.ASTILBA_ENV_ARTIFACT_DIR ?? ".artifacts/local"
  );
  const containment = strictDescendant(
    { isAbsolute, relative, sep },
    dedicatedRoot,
    selected
  );
  if (containment.relative.includes(sep)) {
    throw new Error(
      "Artifact directory must be a direct child of the dedicated root."
    );
  }
  try {
    await mkdir(selected);
  } catch (error) {
    const alreadyExists =
      typeof error === "object" &&
      error !== null &&
      "code" in error &&
      error.code === "EEXIST";
    if (alreadyExists) {
      throw new Error("Artifact directory must not already exist.", {
        cause: error,
      });
    }
    throw error;
  }
  const canonical = await canonicalStrictDescendant(canonicalRoot, selected);
  return canonical.canonicalTarget;
};

const packArtifact = async () => {
  const checkout = await captureSource(root);
  const artifacts = await prepareArtifactDirectory();
  run("pnpm", ["pack", "--pack-destination", artifacts], root);
  const parsedArchiveNames = (await readdir(artifacts)).filter((name) =>
    name.endsWith(".tgz")
  );
  if (
    !Array.isArray(parsedArchiveNames) ||
    parsedArchiveNames.length !== 1 ||
    typeof parsedArchiveNames[0] !== "string"
  ) {
    throw new Error("Expected exactly one packed release archive.");
  }
  const archive = resolve(artifacts, parsedArchiveNames[0]);
  const archiveBytes = await readFile(archive);
  const entries = await inspectArchive(root, archive);
  const manifest = {
    format: FORMAT,
    source: checkout.source,
    package: checkout.package,
    archive: {
      bytes: archiveBytes.byteLength,
      name: parsedArchiveNames[0],
      sha256: sha256(archiveBytes),
    },
    entries,
  };
  await writeFile(
    resolve(artifacts, "manifest.json"),
    encodeManifest(manifest)
  );
  process.stdout.write(
    `${JSON.stringify({ archive, manifest: resolve(artifacts, "manifest.json"), sha256: manifest.archive.sha256 })}\n`
  );
};

if (process.argv[1] === import.meta.filename) {
  await packArtifact();
}
