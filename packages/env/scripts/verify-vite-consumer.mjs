// @ts-check
/// <reference types="node" />

import { access, writeFile } from "node:fs/promises";
import { resolve } from "node:path";

import {
  assertCleanArchiveInstall,
  createConsumer,
  fail,
  installArchive,
  readArtifact,
  removeConsumer,
  run,
  runResult,
  writeConsumerManifest,
} from "./matrix-artifact.mjs";

/**
 * @param {readonly string[]} arguments_
 */
const runViteConsumer = async (arguments_) => {
  if (arguments_.length > 0) {
    return fail("Vite consumer accepts no arguments.");
  }
  const { archive, sha256 } = await readArtifact();
  const consumer = await createConsumer();
  try {
    await writeConsumerManifest(consumer, archive);
    installArchive("npm", consumer, ["vite@8.1.5"]);
    await assertCleanArchiveInstall(consumer, {
      allowManagerMetadata: false,
      manager: "npm",
    });
    await writeFile(
      resolve(consumer, "vite.config.mjs"),
      [
        'import { astilbaEnvBrowserBoundary } from "@astilba/env/vite";',
        "",
        "export default { plugins: [astilbaEnvBrowserBoundary()] };",
        "",
      ].join("\n")
    );
    await writeFile(
      resolve(consumer, "index.html"),
      '<!doctype html><html><body><script type="module" src="/main.js"></script></body></html>\n'
    );
    await writeFile(
      resolve(consumer, "main.js"),
      'import { BOOTSTRAP_PROTOCOL } from "@astilba/env/browser";\ndocument.body.textContent = BOOTSTRAP_PROTOCOL;\n'
    );
    run(
      process.execPath,
      [
        resolve(consumer, "node_modules/vite/bin/vite.js"),
        "build",
        "--config",
        "vite.config.mjs",
      ],
      consumer
    );
    await access(resolve(consumer, "dist/index.html"));

    await writeFile(
      resolve(consumer, "private.html"),
      '<!doctype html><html><body><script type="module" src="/private.js"></script></body></html>\n'
    );
    await writeFile(
      resolve(consumer, "private.js"),
      'import { checkProcessTarget } from "@astilba/env/runtime";\ndocument.body.textContent = typeof checkProcessTarget;\n'
    );
    await writeFile(
      resolve(consumer, "vite.private.config.mjs"),
      [
        'import { astilbaEnvBrowserBoundary } from "@astilba/env/vite";',
        "",
        "export default {",
        "  plugins: [astilbaEnvBrowserBoundary()],",
        '  build: { outDir: "rejected-dist", rollupOptions: { input: "private.html" } },',
        "};",
        "",
      ].join("\n")
    );
    const rejected = runResult(
      process.execPath,
      [
        resolve(consumer, "node_modules/vite/bin/vite.js"),
        "build",
        "--config",
        "vite.private.config.mjs",
      ],
      consumer
    );
    const output = `${rejected.stdout}${rejected.stderr}`;
    if (
      rejected.status === null ||
      rejected.status === 0 ||
      !output.includes("ENV_BROWSER_PRIVATE_IMPORT") ||
      output.includes("INTERNAL_VALUE") ||
      output.includes("internalValue")
    ) {
      fail("Vite did not reject the private import with the stable refusal.");
    }
    await access(resolve(consumer, "rejected-dist/private.html"))
      .then(() =>
        fail("Vite emitted browser output after a private import refusal.")
      )
      // oxlint-disable-next-line typescript/use-unknown-in-catch-callback-variable -- This checked JavaScript callback has no type annotation syntax and narrows error immediately.
      .catch((error) => {
        if (
          error instanceof Error &&
          !("code" in error && error.code === "ENOENT")
        ) {
          throw error;
        }
      });

    process.stdout.write(
      `${JSON.stringify({ archive: sha256, passed: true, vite: "8.1.5" })}\n`
    );
  } finally {
    await removeConsumer(consumer);
  }
};

if (process.argv[1] === import.meta.filename) {
  await runViteConsumer(process.argv.slice(2));
}
