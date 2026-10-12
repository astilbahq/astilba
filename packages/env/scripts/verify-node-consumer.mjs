// @ts-check
/// <reference types="node" />

import { writeFile } from "node:fs/promises";
import { resolve } from "node:path";
import { pathToFileURL } from "node:url";

import {
  assertCleanArchiveInstall,
  createConsumer,
  readArtifact,
  removeConsumer,
  runPackageManager,
  run,
} from "./matrix-artifact.mjs";

const matrix = Object.freeze({
  "22.14.0": "10.9.2",
  "22.23.2": "10.9.8",
  "24.18.1": "11.16.0",
  "26.5.1": "11.17.0",
});
const [expectedNode, typescript] = process.argv.slice(2);
if (
  expectedNode === undefined ||
  !(expectedNode in matrix) ||
  (typescript !== "6.0.3" && typescript !== "7.0.2")
) {
  throw new Error(
    "Usage: node scripts/verify-node-consumer.mjs <22.14.0|22.23.2|24.18.1|26.5.1> <6.0.3|7.0.2>"
  );
}
if (process.versions.node !== expectedNode) {
  throw new Error("Node archive-consumer runtime differs from its matrix pin.");
}
/** @type {unknown} */
const expectedNpm = Object.getOwnPropertyDescriptor(
  matrix,
  expectedNode
)?.value;
if (typeof expectedNpm !== "string") {
  throw new TypeError("Node archive-consumer matrix pin is invalid.");
}
if (
  runPackageManager("npm", ["--version"], process.cwd()).trim() !== expectedNpm
) {
  throw new Error("npm archive-consumer runtime differs from its matrix pin.");
}

const { archive, sha256 } = await readArtifact();
const consumer = await createConsumer();
try {
  await writeFile(
    resolve(consumer, "package.json"),
    `${JSON.stringify(
      {
        name: "astilba-env-node-matrix-consumer",
        private: true,
        type: "module",
        dependencies: {
          "@astilba/env": pathToFileURL(archive).href,
        },
        devDependencies: {
          "@types/node": "22.14.0",
          typescript,
          vite: "8.1.5",
        },
      },
      undefined,
      2
    )}\n`
  );
  runPackageManager(
    "npm",
    ["install", "--ignore-scripts", "--package-lock=false"],
    consumer
  );
  await assertCleanArchiveInstall(consumer, {
    allowManagerMetadata: false,
    manager: "npm",
  });
  await writeFile(
    resolve(consumer, "smoke.ts"),
    [
      'import { defineEnvironment, env } from "@astilba/env";',
      'import type { ParseBootstrapOptions } from "@astilba/env/browser";',
      'import type { ProcessTargetDefinition } from "@astilba/env/runtime";',
      'import { astilbaEnvBrowserBoundary } from "@astilba/env/vite";',
      'const definition = defineEnvironment({ id: "com.example.matrix", entries: { value: env.public.build.string() }, consumers: { web: env.browser(["value"]) }, targets: { build: env.process("web", { value: "VALUE" }) } });',
      "const browser = undefined as unknown as ParseBootstrapOptions;",
      "const runtime = undefined as unknown as ProcessTargetDefinition;",
      "const vite = astilbaEnvBrowserBoundary;",
      "void [definition, browser, runtime, vite];",
      "",
    ].join("\n")
  );
  run(
    process.execPath,
    [
      resolve(consumer, "node_modules/typescript/bin/tsc"),
      "--noEmit",
      "--skipLibCheck",
      "--strict",
      "--exactOptionalPropertyTypes",
      "--moduleResolution",
      "Bundler",
      "--module",
      "ESNext",
      "--target",
      "ES2024",
      "--lib",
      "ES2024,ESNext.Disposable",
      "smoke.ts",
    ],
    consumer
  );
  await writeFile(
    resolve(consumer, "smoke.mjs"),
    'import { defineEnvironment } from "@astilba/env";\nif (typeof defineEnvironment !== "function") throw new Error("Runtime import failed.");\n'
  );
  run(process.execPath, ["smoke.mjs"], consumer);
  run(
    process.execPath,
    [
      "--input-type=module",
      "--eval",
      'const browser = await import("@astilba/env/browser"); if (typeof browser.loadBrowserBootstrap !== "function") throw new Error("Browser export is incomplete.");',
    ],
    consumer
  );
} finally {
  await removeConsumer(consumer);
}

process.stdout.write(
  `${JSON.stringify({
    archive: sha256,
    node: expectedNode,
    npm: expectedNpm,
    typescript,
    passed: true,
  })}\n`
);
