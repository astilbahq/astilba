// @ts-check
/// <reference types="node" />

import { writeFile } from "node:fs/promises";
import { resolve } from "node:path";

import {
  assertCleanArchiveInstall,
  createConsumer,
  installArchive,
  readArtifact,
  removeConsumer,
  run,
  runResult,
  runPackageManager,
  writeConsumerManifest,
} from "./matrix-artifact.mjs";

const { archive, sha256 } = await readArtifact();
/** @type {readonly ("bun" | "npm" | "pnpm")[]} */
const managers = ["npm", "pnpm", "bun"];
const selected = process.argv.slice(2);
const requested = selected.length === 0 ? [...managers] : selected;
/** @param {string} value @returns {value is "bun" | "npm" | "pnpm"} */
const isManager = (value) =>
  value === "bun" || value === "npm" || value === "pnpm";

/** @param {unknown} value @returns {value is Record<string, unknown>} */
const isRecord = (value) =>
  typeof value === "object" && value !== null && !Array.isArray(value);

/** @param {string} output @returns {Record<string, unknown>} */
const parseJson = (output) => {
  /** @type {unknown} */
  const parsed = JSON.parse(output);
  if (!isRecord(parsed)) {
    throw new TypeError("Packed CLI output is not a JSON object.");
  }
  return parsed;
};

/** @param {string} consumer */
const verifyInventoryCli = async (consumer) => {
  await writeFile(
    resolve(consumer, "astilba.env.ts"),
    `import { defineEnvironment, env } from "@astilba/env";

export default defineEnvironment({
  id: "com.example.inventory-consumer",
  entries: {
    apiKey: env.private.deployment.secret(),
    sentryDsn: env.private.deployment.secret({ required: false }),
  },
  consumers: { worker: env.server() },
  targets: {
    workerDeployment: env.process("worker", {
      apiKey: "API_KEY",
      sentryDsn: "SENTRY_DSN",
    }),
  },
});
`
  );
  const observed = resolve(consumer, "observed.json");
  await writeFile(
    observed,
    '{"entries":[{"name":"API_KEY"},{"name":"EXTRA_KEY"}],"format":"astilba.env.observed-name-inventory/v1"}\n'
  );
  const cli = resolve(
    consumer,
    "node_modules",
    "@astilba",
    "env",
    "dist",
    "cli",
    "astilba-env.js"
  );
  const targetArguments = ["--target", "workerDeployment", "--json"];
  const exported = parseJson(
    run(
      process.execPath,
      [cli, "inventory", "export", ...targetArguments],
      consumer
    )
  );
  const exportedInventory = exported.inventory;
  if (
    exported.ok !== true ||
    exported.operation !== "export" ||
    !isRecord(exportedInventory) ||
    exportedInventory.format !== "astilba.env.contract-inventory/v1" ||
    !Array.isArray(exportedInventory.entries) ||
    exportedInventory.entries.length !== 2
  ) {
    throw new Error("Packed CLI inventory export is incomplete.");
  }
  const checkedOpen = parseJson(
    run(
      process.execPath,
      [cli, "inventory", "check", ...targetArguments, "--observed", observed],
      consumer
    )
  );
  const openReport = checkedOpen.report;
  if (
    checkedOpen.ok !== true ||
    checkedOpen.operation !== "check" ||
    !isRecord(openReport) ||
    openReport.ownership !== "open" ||
    openReport.pass !== true
  ) {
    throw new Error("Packed CLI open inventory check is incomplete.");
  }
  const checkedClosed = runResult(
    process.execPath,
    [
      cli,
      "inventory",
      "check",
      ...targetArguments,
      "--observed",
      observed,
      "--ownership",
      "closed",
    ],
    consumer
  );
  if (
    checkedClosed.error !== undefined ||
    checkedClosed.signal !== null ||
    checkedClosed.status !== 1 ||
    checkedClosed.stderr !== ""
  ) {
    throw new Error(
      "Packed CLI closed inventory check has unstable exit semantics."
    );
  }
  const closedOutput = parseJson(checkedClosed.stdout);
  const closedReport = closedOutput.report;
  if (
    closedOutput.ok !== false ||
    !isRecord(closedReport) ||
    closedReport.ownership !== "closed" ||
    closedReport.pass !== false
  ) {
    throw new Error("Packed CLI closed inventory report is incomplete.");
  }
};

if (requested.length === 0 || !requested.every(isManager)) {
  throw new Error("Usage: node scripts/verify-consumer.mjs [npm|pnpm|bun ...]");
}

for (const manager of requested) {
  let expectedVersion;
  if (manager === "bun") {
    expectedVersion = "1.3.14";
  } else if (manager === "pnpm") {
    expectedVersion = "11.10.0";
  } else {
    expectedVersion =
      process.versions.node === "22.14.0" ? "10.9.2" : "11.16.0";
  }
  const consumer = await createConsumer();
  try {
    if (
      runPackageManager(manager, ["--version"], consumer).trim() !==
      expectedVersion
    ) {
      throw new Error(
        `${manager} archive-consumer runtime differs from its matrix pin.`
      );
    }
    await writeConsumerManifest(consumer, archive);
    installArchive(manager, consumer);
    await assertCleanArchiveInstall(consumer, {
      allowManagerMetadata: manager === "pnpm",
      manager,
    });
    await writeFile(
      resolve(consumer, "smoke.mjs"),
      'import { defineEnvironment, env } from "@astilba/env";\nconst definition = defineEnvironment({ id: "com.example.consumer", entries: { value: env.public.build.string() }, consumers: { web: env.browser(["value"]) }, targets: { build: env.process("web", { value: "VALUE" }) } });\nprocess.stdout.write(definition.id + "\\n");\n'
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
    await verifyInventoryCli(consumer);
  } finally {
    await removeConsumer(consumer);
  }
}

process.stdout.write(
  `${JSON.stringify({ archive: sha256, managers: requested, passed: true })}\n`
);
