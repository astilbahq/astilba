// @ts-check
/// <reference types="node" />

import { createHash } from "node:crypto";
import {
  appendFile,
  copyFile,
  lstat,
  readdir,
  readFile,
} from "node:fs/promises";
import { relative, resolve, sep } from "node:path";
import { fileURLToPath } from "node:url";
import { brotliDecompressSync, gunzipSync } from "node:zlib";

import {
  assertCleanArchiveInstall,
  createConsumer,
  fail,
  installArchive,
  removeConsumer,
  readArtifact,
  run,
  runResult,
  writeConsumerManifest,
} from "./matrix-artifact.mjs";

/**
 * @param {string} path
 * @param {Uint8Array} bytes
 */
const decodedBrowserBytes = (path, bytes) => {
  if (path.endsWith(".br")) {
    return brotliDecompressSync(bytes);
  }
  if (path.endsWith(".gz")) {
    return gunzipSync(bytes);
  }
  return bytes;
};

/** @param {string} directory */
const snapshot = async (directory) => {
  /** @type {{ path: string, sha256: string }[]} */
  const files = [];
  /** @param {string} current */
  const walk = async (current) => {
    for (const entry of await readdir(current, { withFileTypes: true })) {
      if (
        current === directory &&
        [
          "astilba.env.ts",
          "node_modules",
          "package-lock.json",
          "package.json",
        ].includes(entry.name)
      ) {
        continue;
      }
      const path = resolve(current, entry.name);
      const metadata = await lstat(path);
      if (metadata.isDirectory()) {
        await walk(path);
      } else if (metadata.isFile() && !metadata.isSymbolicLink()) {
        files.push({
          path: relative(directory, path).split(sep).join("/"),
          sha256: createHash("sha256")
            .update(await readFile(path))
            .digest("hex"),
        });
      } else {
        fail(
          "Deterministic generation produced an unsupported filesystem node."
        );
      }
    }
  };
  await walk(directory);
  /** @type {{ path: string, sha256: string }[]} */
  const ordered = [];
  for (const file of files) {
    const index = ordered.findIndex(
      (current) => current.path.localeCompare(file.path) > 0
    );
    if (index === -1) {
      ordered.push(file);
    } else {
      ordered.splice(index, 0, file);
    }
  }
  return ordered;
};

/**
 * @param {readonly string[]} arguments_
 */
const runDeterministicGeneration = async (arguments_) => {
  if (arguments_.length > 0) {
    return fail("Deterministic generation accepts no arguments.");
  }
  const { archive, sha256 } = await readArtifact();
  const consumer = await createConsumer();
  try {
    await writeConsumerManifest(consumer, archive);
    installArchive("npm", consumer);
    await assertCleanArchiveInstall(consumer, {
      allowManagerMetadata: false,
      manager: "npm",
    });
    await copyFile(
      fileURLToPath(
        new URL("../test/fixtures/matrix/astilba.env.ts", import.meta.url)
      ),
      resolve(consumer, "astilba.env.ts")
    );
    const cli = resolve(
      consumer,
      "node_modules/@astilba/env/dist/cli/astilba-env.js"
    );
    const generationEnvironment = {
      ...process.env,
      API_ORIGIN: "https://generation.example",
      CLIENT_MODE: "standard",
      INTERNAL_VALUE: "private-internal-canary",
      REQUEST_LABEL: "generation-request",
    };
    run(process.execPath, [cli, "generate"], consumer, generationEnvironment);
    const first = await snapshot(consumer);
    if (first.length === 0) {
      fail("Generation produced no output to compare.");
    }
    const browserFiles = first.filter((file) =>
      file.path.startsWith(".astilba/env/browser/")
    );
    if (browserFiles.length === 0) {
      fail("Generation produced no browser projection to inspect.");
    }
    for (const file of browserFiles) {
      const bytes = decodedBrowserBytes(
        file.path,
        await readFile(resolve(consumer, file.path))
      );
      const source = new TextDecoder().decode(bytes);
      for (const marker of [
        "internalValue",
        "INTERNAL_VALUE",
        "private-internal-canary",
        "private-brotli-canary",
        "private-gzip-canary",
        "astilba.env.ts",
        "file://",
        "sourceMappingURL",
        resolve(consumer).split(sep).join("/"),
      ]) {
        if (source.includes(marker)) {
          fail(
            `Generated browser projection contains a private marker: ${marker}`
          );
        }
      }
    }
    run(
      process.execPath,
      [cli, "generate", "--check"],
      consumer,
      generationEnvironment
    );
    const afterCurrentCheck = await snapshot(consumer);
    if (JSON.stringify(first) !== JSON.stringify(afterCurrentCheck)) {
      fail("A successful generated-output check mutated the generated files.");
    }

    const driftPath = resolve(consumer, browserFiles[0]?.path ?? "");
    if (driftPath === consumer) {
      fail("Generation did not provide a browser output to drift.");
    }
    await appendFile(driftPath, "\n// archive-check-drift\n");
    const drifted = await snapshot(consumer);
    const staleCheck = runResult(
      process.execPath,
      [cli, "generate", "--check"],
      consumer,
      generationEnvironment
    );
    if (staleCheck.status === null || staleCheck.status === 0) {
      fail(
        "Generated-output check accepted deliberately drifted browser output."
      );
    }
    const afterStaleCheck = await snapshot(consumer);
    if (JSON.stringify(drifted) !== JSON.stringify(afterStaleCheck)) {
      fail("A stale generated-output check mutated the drifted output.");
    }

    run(process.execPath, [cli, "generate"], consumer, generationEnvironment);
    const second = await snapshot(consumer);
    if (JSON.stringify(first) !== JSON.stringify(second)) {
      fail("Generation is not deterministic across identical inputs.");
    }
    process.stdout.write(
      `${JSON.stringify({ archive: sha256, files: first.length, passed: true })}\n`
    );
  } finally {
    await removeConsumer(consumer);
  }
};

if (process.argv[1] === import.meta.filename) {
  await runDeterministicGeneration(process.argv.slice(2));
}
