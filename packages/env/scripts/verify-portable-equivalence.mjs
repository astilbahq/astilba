// @ts-check
/// <reference types="node" />

import { lstat, readFile, writeFile } from "node:fs/promises";
import { isAbsolute, relative, resolve, sep } from "node:path";

import {
  canonicalStrictDescendant,
  strictDescendant,
} from "./filesystem-containment.mjs";
import {
  assertCleanArchiveInstall,
  createConsumer,
  fail,
  installArchive,
  readArtifact,
  removeConsumer,
  root,
  run,
  writeConsumerManifest,
  writeSmokeModule,
} from "./matrix-artifact.mjs";

const nativePath = { isAbsolute, relative, sep };

const portableVerificationBody = [
  "const portableSignature = () => {",
  "const digest = `sha256-${'A'.repeat(43)}`;",
  "const decode = (input, failure) => {",
  "  const keys = Reflect.ownKeys(input);",
  '  const descriptor = Object.getOwnPropertyDescriptor(input, "value");',
  '  if (keys.length !== 1 || keys[0] !== "value" || descriptor === undefined || !("value" in descriptor) || descriptor.enumerable !== true || descriptor.value !== "portable") failure("BOOTSTRAP_VALUE_INVALID");',
  "  const output = Object.create(null);",
  '  Object.defineProperty(output, "value", { configurable: false, enumerable: true, value: descriptor.value, writable: false });',
  "  return Object.freeze(output);",
  "};",
  "const projection = Object.create(null);",
  "for (const [key, value] of Object.entries({",
  '  codecAbi: "astilba.env.codec/v1",',
  '  consumer: "web",',
  '  contract: "example.portable",',
  "  decode,",
  "  digest,",
  '  format: "astilba.env.projection",',
  "  formatVersion: 1,",
  '  generated: "astilba.env.generated-module/v1",',
  '  kind: "public",',
  '  lifecycle: "deployment",',
  '  projectionAbi: "astilba.env.projection/v1",',
  "})) Object.defineProperty(projection, key, { configurable: false, enumerable: true, value, writable: false });",
  "  const result = parseBrowserBootstrap({",
  '  expectedAudience: { origin: "https://portable.example" },',
  "  projection: Object.freeze(projection),",
  '  source: JSON.stringify({ audience: { origin: "https://portable.example" }, consumer: "web", contract: "example.portable", lifecycle: "deployment", projection: digest, protocol: "astilba.env.bootstrap/v1", values: { value: "portable" } }),',
  "  });",
  "  return JSON.stringify({",
  "    origin: result.audience.origin,",
  "    value: result.values.value,",
  "  });",
  "};",
];
const expectedPortableSignature = JSON.stringify({
  origin: "https://portable.example",
  value: "portable",
});
const portableModule = [
  'import { parseBrowserBootstrap } from "./node_modules/@astilba/env/dist/browser/index.js";',
  "",
  ...portableVerificationBody,
  "",
  "process.stdout.write(portableSignature());",
  "",
].join("\n");

/**
 * @param {string} packageRoot
 * @returns {Promise<Map<string, string>>}
 */
const collectModuleGraph = async (packageRoot) => {
  /** @type {Map<string, string>} */
  const modules = new Map();
  const entry = resolve(packageRoot, "dist/browser/index.js");
  /** @param {string} modulePath */
  const visit = async (modulePath) => {
    const { canonicalTarget: canonical } = await canonicalStrictDescendant(
      packageRoot,
      modulePath
    );
    if (modules.has(canonical)) {
      return;
    }
    const metadata = await lstat(canonical);
    if (!metadata.isFile() || metadata.isSymbolicLink()) {
      fail("Portable module graph contains a non-regular module.");
    }
    const source = await readFile(canonical, "utf-8");
    modules.set(canonical, source);
    const imports =
      /\b(?:from\s*|import\s*\(\s*|import\s*)["'](?<specifier>[^"']+)["']/gu;
    for (const match of source.matchAll(imports)) {
      const specifier = match.groups?.specifier;
      if (specifier !== undefined && specifier.startsWith(".")) {
        await visit(resolve(canonical, "..", specifier));
      } else if (specifier !== undefined) {
        fail(`Portable module graph contains an external import: ${specifier}`);
      }
    }
  };
  await visit(entry);
  return modules;
};

/**
 * @param {string} directory
 */
const writeWorkerdFixture = async (directory) => {
  const packageRoot = resolve(directory, "node_modules/@astilba/env");
  const {
    canonicalRoot: canonicalDirectory,
    canonicalTarget: canonicalPackageRoot,
  } = await canonicalStrictDescendant(directory, packageRoot);
  const modules = await collectModuleGraph(canonicalPackageRoot);
  const worker = [
    'import { parseBrowserBootstrap } from "./package/dist/browser/index.js";',
    "",
    ...portableVerificationBody,
    "",
    "export const test = {",
    "  async test() {",
    "    const signature = portableSignature();",
    `    if (signature !== ${JSON.stringify(expectedPortableSignature)}) {`,
    '      throw new Error("workerd browser projection differs from Node and Bun.");',
    "    }",
    "  },",
    "};",
    "",
  ].join("\n");
  await writeFile(resolve(directory, "worker.mjs"), worker);
  const moduleSources = [...modules.keys()].map((modulePath) => {
    const name = `package/${strictDescendant(nativePath, canonicalPackageRoot, modulePath).relative.split(sep).join("/")}`;
    const source = strictDescendant(nativePath, canonicalDirectory, modulePath)
      .relative.split(sep)
      .join("/");
    return `(name = ${JSON.stringify(name)}, esModule = embed ${JSON.stringify(source)})`;
  });
  /** @type {string[]} */
  const orderedModuleSources = [];
  for (const entry of moduleSources) {
    const index = orderedModuleSources.findIndex((current) => current > entry);
    if (index === -1) {
      orderedModuleSources.push(entry);
    } else {
      orderedModuleSources.splice(index, 0, entry);
    }
  }
  const moduleEntries = [
    '(name = "worker.mjs", esModule = embed "worker.mjs")',
    ...orderedModuleSources,
  ];
  await writeFile(
    resolve(directory, "workerd.capnp"),
    [
      'using Workerd = import "/workerd/workerd.capnp";',
      "",
      "const config :Workerd.Config = (",
      '  services = [(name = "portable", worker = .portableWorker)],',
      ");",
      "",
      "const portableWorker :Workerd.Worker = (",
      '  compatibilityDate = "2026-07-24",',
      `  modules = [${moduleEntries.join(", ")}],`,
      ");",
      "",
    ].join("\n")
  );
};

/**
 * @param {readonly string[]} arguments_
 */
const runPortableEquivalence = async (arguments_) => {
  if (arguments_.length > 0) {
    return fail("Portable equivalence accepts no arguments.");
  }
  const { archive, sha256 } = await readArtifact();
  if (run("bun", ["--version"], root).trim() !== "1.3.14") {
    fail("Bun portable runtime differs from its matrix pin.");
  }
  const consumer = await createConsumer();
  try {
    await writeConsumerManifest(consumer, archive);
    installArchive("npm", consumer);
    await assertCleanArchiveInstall(consumer, {
      allowManagerMetadata: false,
      manager: "npm",
    });
    await writeSmokeModule(consumer, portableModule);
    const nodeSignature = run(process.execPath, ["smoke.mjs"], consumer).trim();
    const bunSignature = run("bun", ["smoke.mjs"], consumer).trim();
    if (nodeSignature !== bunSignature) {
      fail("Bun portable signature differs from Node.");
    }
    await writeWorkerdFixture(consumer);
    run(
      resolve(root, "node_modules", "workerd", "bin", "workerd"),
      ["test", "workerd.capnp", "--no-verbose"],
      consumer
    );
    process.stdout.write(
      `${JSON.stringify({ archive: sha256, passed: true, runtimes: 3 })}\n`
    );
  } finally {
    await removeConsumer(consumer);
  }
};

if (process.argv[1] === import.meta.filename) {
  await runPortableEquivalence(process.argv.slice(2));
}
