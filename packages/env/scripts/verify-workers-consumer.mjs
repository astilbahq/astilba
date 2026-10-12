// @ts-check
/// <reference types="node" />

import { createHash } from "node:crypto";
import { mkdir, readFile, readdir, writeFile } from "node:fs/promises";
import { relative, resolve, sep } from "node:path";
import { pathToFileURL } from "node:url";

import {
  assertCleanArchiveInstall,
  createConsumer,
  fail,
  installArchive,
  readArtifact,
  removeConsumer,
  run,
  runResult,
} from "./matrix-artifact.mjs";

const COMPATIBILITY_DATE = "2026-07-29";
const PUBLIC_ORIGIN_SOURCE_CANARY =
  "https://astilba-env-workers-source-canary.example";
const JSON_SOURCE_CANARY = '{"region":"workers-source-canary"}';
const SECRET_SOURCE_CANARY = "workers-secret-source-canary";
const WRANGLER_VERSION = "4.115.0";
const WORKERD_VERSION = "1.20260722.1";
const GENERATED_V1_WORKER_BASELINE = new URL(
  "../test/fixtures/workers/generated-v1-worker-baseline.json",
  import.meta.url
);
const GENERATED_V1_WORKER_BASELINE_INTEGRITY =
  "sha512-Pzhyq15LCuBC5wDj0QKNH6jKtjNVOD1TZcmHAmMFYcvUOETkj8NmAbEUQzScoRd8apCvY+zlZShvuYdPwfDazg==";
const GENERATED_V1_WORKER_BASELINE_PACKAGE = "@astilba/env@0.1.0";
const GENERATED_V1_WORKER_BASELINE_PATH =
  ".astilba/env/workerDeployment.server.ts";
const GENERATED_V1_WORKER_BASELINE_SHA256 =
  "8e55ef7cf12958bb2764a0dc9e7c2565695d483040a6dda1e4d11fbc133182ec";
const GENERATED_V1_WORKER_BASELINE_SIZE = 19_462;
const GENERATED_V1_WORKER_BASELINE_TARBALL =
  "https://registry.npmjs.org/@astilba/env/-/env-0.1.0.tgz";

/**
 * @typedef {Readonly<{
 *   archive: Readonly<{ integrity: string, tarball: string }>,
 *   generatedPath: string,
 *   package: string,
 *   sha256: string,
 *   size: number,
 * }>} GeneratedV1WorkerBaseline
 */

/** @param {unknown} value @returns {value is Record<string, unknown>} */
const isRecord = (value) =>
  typeof value === "object" && value !== null && !Array.isArray(value);

/** @param {unknown} value @returns {value is GeneratedV1WorkerBaseline} */
const isGeneratedV1WorkerBaseline = (value) => {
  if (!isRecord(value)) {
    return false;
  }
  const archive = value.archive;
  return (
    isRecord(archive) &&
    archive.integrity === GENERATED_V1_WORKER_BASELINE_INTEGRITY &&
    archive.tarball === GENERATED_V1_WORKER_BASELINE_TARBALL &&
    value.generatedPath === GENERATED_V1_WORKER_BASELINE_PATH &&
    value.package === GENERATED_V1_WORKER_BASELINE_PACKAGE &&
    value.sha256 === GENERATED_V1_WORKER_BASELINE_SHA256 &&
    value.size === GENERATED_V1_WORKER_BASELINE_SIZE
  );
};

/** @returns {Promise<GeneratedV1WorkerBaseline>} */
const readGeneratedV1WorkerBaseline = async () => {
  /** @type {unknown} */
  const value = JSON.parse(
    await readFile(GENERATED_V1_WORKER_BASELINE, "utf-8")
  );
  return isGeneratedV1WorkerBaseline(value)
    ? value
    : fail("The generated v1 Worker baseline provenance is invalid.");
};

const WORKER_ADMISSION_V1_FIXTURE = new URL(
  "../test/fixtures/workers/worker-admission-v1.astilba.env.txt",
  import.meta.url
);

const readWorkerAdmissionV1Fixture = async () =>
  await readFile(WORKER_ADMISSION_V1_FIXTURE, "utf-8");

const workerSource = [
  'import type { StandardSchemaV1 } from "@astilba/env/runtime";',
  'import { check as checkBuildBoundary } from "../.astilba/env/buildBoundary.server";',
  'import { check as checkOpaqueBoundary } from "../.astilba/env/opaqueBoundary.server";',
  'import { check as checkRequestBoundary } from "../.astilba/env/requestBoundary.server";',
  'import { check as checkSelectedCapability } from "../.astilba/env/selectedCapabilityDeployment.server";',
  'import { check as checkSelectedJson } from "../.astilba/env/selectedJsonDeployment.server";',
  'import { check as checkWorker, load as loadWorker } from "../.astilba/env/workerDeployment.server";',
  'import { check as checkNewer } from "./newer.server";',
  "",
  "type RefusalResult =",
  "  | Readonly<{ ok: true }>",
  "  | Readonly<{",
  "      diagnostics: readonly Readonly<{ code: string }>[];",
  "      ok: false;",
  "    }>;",
  "",
  "const unreadableSource = () => {",
  "  let bindingReads = 0;",
  "  return {",
  "    bindingReads: () => bindingReads,",
  "    source: new Proxy(",
  "      {},",
  "      {",
  "        getOwnPropertyDescriptor() {",
  "          bindingReads += 1;",
  '          throw new Error("A refused target read its source.");',
  "        },",
  "      }",
  "    ),",
  "  };",
  "};",
  "",
  "const observedSource = (source: object) => {",
  "  const descriptors: string[] = [];",
  "  return {",
  "    descriptors,",
  "    source: new Proxy(source, {",
  '      defineProperty() { throw new Error("A generated target mutated its source."); },',
  '      deleteProperty() { throw new Error("A generated target mutated its source."); },',
  '      get() { throw new Error("A generated target used an ordinary source read."); },',
  "      getOwnPropertyDescriptor(target, property) {",
  '        if (typeof property !== "string") throw new Error("A generated target read a non-string source key.");',
  "        descriptors.push(property);",
  "        return Reflect.getOwnPropertyDescriptor(target, property);",
  "      },",
  '      ownKeys() { throw new Error("A generated target enumerated its source."); },',
  '      set() { throw new Error("A generated target mutated its source."); },',
  "    }),",
  "  };",
  "};",
  "",
  "const refusalResponse = (",
  "  result: RefusalResult,",
  "  bindingReads: number,",
  "  validatorCalls = 0",
  "): Response =>",
  "  Response.json({",
  "    bindingReads,",
  "    codes: result.ok ? [] : result.diagnostics.map((item) => item.code),",
  "    ok: result.ok,",
  "    validatorCalls,",
  "  });",
  "",
  "export default {",
  "  async fetch(request: Request, env: Env): Promise<Response> {",
  "    const pathname = new URL(request.url).pathname;",
  '    if (pathname === "/build-boundary") {',
  "      const unreadable = unreadableSource();",
  "      const result = checkBuildBoundary(unreadable.source);",
  "      return refusalResponse(result, unreadable.bindingReads());",
  "    }",
  '    if (pathname === "/request-boundary") {',
  "      const unreadable = unreadableSource();",
  "      const result = checkRequestBoundary(unreadable.source);",
  "      return refusalResponse(result, unreadable.bindingReads());",
  "    }",
  '    if (pathname === "/opaque-boundary") {',
  "      const unreadable = unreadableSource();",
  "      let validatorCalls = 0;",
  "      const opaqueValue: StandardSchemaV1<string, string> = {",
  '        "~standard": {',
  "          validate(value: unknown) {",
  "            validatorCalls += 1;",
  '            return typeof value === "string"',
  "              ? { value }",
  "              : { issues: [] };",
  "          },",
  '          vendor: "fixture",',
  "          version: 1,",
  "        },",
  "      };",
  "      const result = await checkOpaqueBoundary(unreadable.source, {",
  "        opaqueValue,",
  "      });",
  "      return refusalResponse(",
  "        result,",
  "        unreadable.bindingReads(),",
  "        validatorCalls",
  "      );",
  "    }",
  '    if (pathname === "/selected-capability") {',
  "      const observed = observedSource(env);",
  "      const result = checkSelectedCapability(observed.source);",
  "      return Response.json({",
  "        codes: result.ok",
  "          ? []",
  "          : result.diagnostics.map((item) => item.code),",
  "        ok: result.ok,",
  "        selected: observed.descriptors,",
  "      });",
  "    }",
  '    if (pathname === "/selected-json") {',
  "      const observed = observedSource(env);",
  "      const result = checkSelectedJson(observed.source);",
  "      return Response.json({",
  "        codes: result.ok ? [] : result.diagnostics.map((item) => item.code),",
  "        ok: result.ok,",
  "        selected: observed.descriptors,",
  "      });",
  "    }",
  '    if (pathname === "/newer-handshake") {',
  "      const unreadable = unreadableSource();",
  "      const result = checkNewer(unreadable.source);",
  "      return refusalResponse(result, unreadable.bindingReads());",
  "    }",
  "    const checked = checkWorker(env);",
  "    const configuration = loadWorker(env);",
  "    if (!checked.ok || checked.value.featureEnabled !== configuration.featureEnabled) {",
  '      throw new Error("Generated check and load disagree for the Worker source.");',
  "    }",
  "    const observed = observedSource(env);",
  "    const observedResult = checkWorker(observed.source);",
  "    if (!observedResult.ok) {",
  '      throw new Error("Generated Worker check rejected its selected bindings.");',
  "    }",
  "    const alternate = Object.create(null) as Record<string, string>;",
  "    for (const name of observed.descriptors) {",
  "      const descriptor = Object.getOwnPropertyDescriptor(env, name);",
  '      if (descriptor === undefined || typeof descriptor.value !== "string") {',
  '        throw new Error("Generated Worker bindings must remain strings.");',
  "      }",
  "      alternate[name] = descriptor.value;",
  "    }",
  '    alternate.FEATURE_ENABLED = configuration.featureEnabled ? "false" : "true";',
  "    const alternateConfiguration = loadWorker(alternate);",
  "    if (alternateConfiguration.featureEnabled === configuration.featureEnabled) {",
  '      throw new Error("Generated Worker state was reused across distinct binding sets.");',
  "    }",
  "    return Response.json({",
  "      alternate: alternateConfiguration.featureEnabled,",
  "      checkAndLoad: true,",
  "      selected: observed.descriptors,",
  "    });",
  "  },",
  "} satisfies ExportedHandler<Env>;",
  "",
].join("\n");

const harnessSource = [
  'import { createHash } from "node:crypto";',
  'import { readFile } from "node:fs/promises";',
  'import { createTestHarness } from "wrangler";',
  "",
  'const artifactPath = new URL("./worker-artifact/index.js", import.meta.url);',
  "const artifactDigest = async () =>",
  '  createHash("sha256").update(await readFile(artifactPath)).digest("hex");',
  "const initialArtifact = await artifactDigest();",
  "",
  "const runBindingSet = async (bindingSet) => {",
  "  const sourceCanaries = bindingSet.canaries;",
  "  const server = createTestHarness({",
  "    root: process.cwd(),",
  "    workers: [",
  "      {",
  '        configPath: "./wrangler.harness.jsonc",',
  "        secrets: { WORKER_SECRET: bindingSet.secret },",
  "        vars: bindingSet.vars,",
  "      },",
  "    ],",
  "  });",
  "  try {",
  "    await server.listen();",
  "    const worker = server.getWorker();",
  "    const env = await worker.getEnv();",
  '    await env.UNRELATED_KV.put("admission-canary", bindingSet.canary);',
  "",
  '    const response = await server.fetch("https://worker.example/");',
  "    const responseText = await response.text();",
  "    if (",
  "      !response.ok ||",
  "      sourceCanaries.some((canary) => responseText.includes(canary))",
  "    ) {",
  '      throw new Error("Worker response disclosed a test-only source canary.");',
  "    }",
  "    const body = JSON.parse(responseText);",
  "    if (",
  "      body.checkAndLoad !== true ||",
  '      body.alternate !== (bindingSet.vars.FEATURE_ENABLED !== "true") ||',
  "      !Array.isArray(body.selected) ||",
  '      body.selected.includes("UNRELATED_KV") ||',
  '      !body.selected.includes("WORKER_SECRET")',
  "    ) {",
  '      throw new Error("Worker did not isolate generated check/load bindings.");',
  "    }",
  "",
  '    for (const path of ["/selected-capability", "/selected-json"]) {',
  '      const capabilityResponse = await server.fetch("https://worker.example" + path);',
  "      const capabilityText = await capabilityResponse.text();",
  "      if (",
  "        !capabilityResponse.ok ||",
  "        sourceCanaries.some((canary) => capabilityText.includes(canary))",
  "      ) {",
  '        throw new Error("A capability response disclosed a source canary.");',
  "      }",
  "      const capabilityBody = JSON.parse(capabilityText);",
  "      if (",
  "        capabilityBody.ok !== false ||",
  '        JSON.stringify(capabilityBody.codes) !== JSON.stringify(["ENV_SOURCE_INVALID"]) ||',
  "        !Array.isArray(capabilityBody.selected) ||",
  '        capabilityBody.selected.includes("UNRELATED_KV") ||',
  '        !capabilityBody.selected.includes("SELECTED_KV")',
  "      ) {",
  '        throw new Error("A selected capability object was accepted or over-read.");',
  "      }",
  "    }",
  "",
  '    const newerResponse = await server.fetch("https://worker.example/newer-handshake");',
  "    const newer = await newerResponse.json();",
  "    if (",
  "      !newerResponse.ok ||",
  "      newer.ok !== false ||",
  "      newer.bindingReads !== 0 ||",
  '      JSON.stringify(newer.codes) !== JSON.stringify(["ENV_FORMAT_UNSUPPORTED"])',
  "    ) {",
  '      throw new Error("A newer generated marker reached the Worker source.");',
  "    }",
  "",
  "    for (const [path, expectedCode] of [",
  '      ["/build-boundary", "ENV_CONTRACT_INVALID"],',
  '      ["/request-boundary", "ENV_CONTRACT_INVALID"],',
  '      ["/opaque-boundary", "ENV_OPAQUE_UNSUPPORTED"],',
  "    ]) {",
  "      const refusalResponse = await server.fetch(",
  '        "https://worker.example" + path',
  "      );",
  "      const refusal = await refusalResponse.json();",
  "      if (",
  "        !refusalResponse.ok ||",
  "        refusal.ok !== false ||",
  "        refusal.bindingReads !== 0 ||",
  "        refusal.validatorCalls !== 0 ||",
  "        JSON.stringify(refusal.codes) !== JSON.stringify([expectedCode])",
  "      ) {",
  '        throw new Error("A workerd runtime boundary executed a refused target.");',
  "      }",
  "    }",
  "",
  "    if (",
  '      (await env.UNRELATED_KV.get("admission-canary")) !== bindingSet.canary',
  "    ) {",
  '      throw new Error("The unselected KV binding was mutated by the Worker.");',
  "    }",
  "    if ((await artifactDigest()) !== initialArtifact) {",
  '      throw new Error("The Worker artifact changed between binding sets.");',
  "    }",
  "  } finally {",
  "    await server.close();",
  "  }",
  "};",
  "",
  "await runBindingSet({",
  "  canaries: [",
  '    "unrelated-capability-canary-a",',
  '    "workers-secret-source-canary-a",',
  '    "{\\\"region\\\":\\\"workers-source-canary-a\\\"}",',
  '    "https://astilba-env-workers-source-canary-a.example",',
  '    "worker-string-source-canary-a",',
  '    "worker-text-source-canary-a",',
  "  ],",
  '  canary: "unrelated-capability-canary-a",',
  '  secret: "workers-secret-source-canary-a",',
  "  vars: {",
  '    BOOLEAN_VALUE: "false",',
  '    ENUM_VALUE: "worker",',
  '    FEATURE_ENABLED: "true",',
  '    INTEGER_VALUE: "-7",',
  '    JSON_VALUE: "{\\\"region\\\":\\\"workers-source-canary-a\\\"}",',
  '    PUBLIC_ORIGIN: "https://astilba-env-workers-source-canary-a.example",',
  '    SAFE_INTEGER_VALUE: "7",',
  '    STRING_LIST_VALUE: "one,two",',
  '    STRING_VALUE: "worker-string-source-canary-a",',
  '    TEXT_VALUE: "worker-text-source-canary-a",',
  "  },",
  "});",
  "await runBindingSet({",
  "  canaries: [",
  '    "unrelated-capability-canary-b",',
  '    "workers-secret-source-canary-b",',
  '    "{\\\"region\\\":\\\"workers-source-canary-b\\\"}",',
  '    "https://astilba-env-workers-source-canary-b.example",',
  '    "worker-string-source-canary-b",',
  '    "worker-text-source-canary-b",',
  "  ],",
  '  canary: "unrelated-capability-canary-b",',
  '  secret: "workers-secret-source-canary-b",',
  "  vars: {",
  '    BOOLEAN_VALUE: "true",',
  '    ENUM_VALUE: "archive",',
  '    FEATURE_ENABLED: "false",',
  '    INTEGER_VALUE: "42",',
  '    JSON_VALUE: "{\\\"region\\\":\\\"workers-source-canary-b\\\"}",',
  '    PUBLIC_ORIGIN: "https://astilba-env-workers-source-canary-b.example",',
  '    SAFE_INTEGER_VALUE: "8",',
  '    STRING_LIST_VALUE: "three,four",',
  '    STRING_VALUE: "worker-string-source-canary-b",',
  '    TEXT_VALUE: "worker-text-source-canary-b",',
  "  },",
  "});",
  "",
  "process.stdout.write(",
  '  JSON.stringify({ artifact: initialArtifact, bindingSets: 2, passed: true }) + "\\n"',
  ");",
  "",
].join("\n");

const rejectedImports = Object.freeze({
  browser: Object.freeze({
    importedName: "loadBrowserBootstrap",
    specifier: "@astilba/env/browser",
  }),
  root: Object.freeze({
    importedName: "defineEnvironment",
    specifier: "@astilba/env",
  }),
  vite: Object.freeze({
    importedName: "astilbaEnvBrowserBoundary",
    specifier: "@astilba/env/vite",
  }),
});

const browserDefaultSource = [
  'import { parseBrowserBootstrap } from "@astilba/env/browser";',
  "",
  'const digest = "sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA";',
  "const projection = {",
  '  codecAbi: "astilba.env.codec/v1",',
  '  consumer: "browser",',
  '  contract: "example.browser",',
  "  decode(input, failure) {",
  '    if (typeof input.browserLabel !== "string") return failure("BOOTSTRAP_VALUE_INVALID");',
  "    return Object.freeze(Object.assign(Object.create(null), { browserLabel: input.browserLabel }));",
  "  },",
  "  digest,",
  '  format: "astilba.env.projection",',
  "  formatVersion: 1,",
  '  generated: "astilba.env.generated-module/v1",',
  '  kind: "public",',
  '  lifecycle: "deployment",',
  '  projectionAbi: "astilba.env.projection/v1",',
  "};",
  "const result = parseBrowserBootstrap({",
  '  expectedAudience: { origin: "https://browser.example" },',
  "  projection,",
  "  source: JSON.stringify({",
  '    audience: { origin: "https://browser.example" },',
  '    consumer: "browser",',
  '    contract: "example.browser",',
  '    lifecycle: "deployment",',
  "    projection: digest,",
  '    protocol: "astilba.env.bootstrap/v1",',
  '    values: { browserLabel: "archive-default" },',
  "  }),",
  "});",
  'if (result.values.browserLabel !== "archive-default") {',
  '  throw new Error("Browser default export did not retain bootstrap behavior.");',
  "}",
  'process.stdout.write(JSON.stringify({ browserDefault: true }) + "\\n");',
  "",
].join("\n");

/** @param {string} consumer */
const assertBrowserDefault = async (consumer) => {
  await writeFile(
    resolve(consumer, "browser-default.mjs"),
    browserDefaultSource
  );
  if (
    run(process.execPath, ["browser-default.mjs"], consumer).trim() !==
    '{"browserDefault":true}'
  ) {
    fail(
      "The archive browser default export did not retain its bootstrap behavior."
    );
  }
};

/** @param {string} consumer */
const assertStockWorkerd = async (consumer) => {
  const [wranglerPackage, workerdPackage] = await Promise.all([
    readFile(
      resolve(consumer, "node_modules", "wrangler", "package.json"),
      "utf-8"
    ),
    readFile(
      resolve(consumer, "node_modules", "workerd", "package.json"),
      "utf-8"
    ),
  ]);
  if (
    !workerdPackage.includes(`"version": "${WORKERD_VERSION}"`) ||
    !wranglerPackage.includes(`"workerd": "${WORKERD_VERSION}"`)
  ) {
    fail(
      "Wrangler did not provide the exact stock workerd admission evidence."
    );
  }
  return WORKERD_VERSION;
};

/**
 * @param {unknown} value
 * @returns {value is { artifact: string, bindingSets: 2, passed: true }}
 */
const isHarnessResult = (value) =>
  typeof value === "object" &&
  value !== null &&
  "artifact" in value &&
  typeof value.artifact === "string" &&
  "bindingSets" in value &&
  value.bindingSets === 2 &&
  "passed" in value &&
  value.passed === true;

/**
 * @param {string} source
 * @returns {{ artifact: string, bindingSets: 2, passed: true }}
 */
const parseHarnessResult = (source) => {
  /** @type {unknown} */
  const value = JSON.parse(source);
  return isHarnessResult(value)
    ? value
    : fail("The workerd harness did not return exact evidence.");
};

/** @param {string} path */
const portablePath = (path) => path.split(sep).join("/");

/**
 * @param {string} directory
 * @returns {Promise<string[]>}
 */
const collectFiles = async (directory) => {
  /** @type {string[]} */
  const files = [];
  /** @param {string} current */
  const visit = async (current) => {
    /** @type {import("node:fs").Dirent[]} */
    let entries;
    try {
      entries = await readdir(current, { withFileTypes: true });
    } catch (error) {
      if (
        typeof error === "object" &&
        error !== null &&
        "code" in error &&
        error.code === "ENOENT"
      ) {
        return;
      }
      throw error;
    }
    for (const entry of entries) {
      const path = resolve(current, entry.name);
      if (entry.isDirectory()) {
        await visit(path);
      } else if (entry.isFile()) {
        files.push(path);
      } else {
        fail("Wrangler output contains an unsupported filesystem node.");
      }
    }
  };
  await visit(directory);
  // oxlint-disable-next-line unicorn/no-array-sort -- The local accumulator has no aliases and is returned immediately.
  return files.sort();
};

/**
 * @param {string} main
 * @param {boolean} noBundle
 */
const wranglerConfiguration = (main, noBundle) => ({
  $schema: "./node_modules/wrangler/config-schema.json",
  compatibility_date: COMPATIBILITY_DATE,
  compatibility_flags: [],
  ...(noBundle ? { no_bundle: true } : {}),
  kv_namespaces: [
    {
      binding: "SELECTED_KV",
      id: "11111111111111111111111111111111",
    },
    {
      binding: "UNRELATED_KV",
      id: "22222222222222222222222222222222",
    },
  ],
  main,
  name: "astilba-env-workers-admission",
  secrets: { required: ["WORKER_SECRET"] },
  vars: {
    BOOLEAN_VALUE: "false",
    ENUM_VALUE: "worker",
    FEATURE_ENABLED: "true",
    INTEGER_VALUE: "-7",
    JSON_VALUE: JSON_SOURCE_CANARY,
    PUBLIC_ORIGIN: PUBLIC_ORIGIN_SOURCE_CANARY,
    SAFE_INTEGER_VALUE: "7",
    STRING_LIST_VALUE: "one,two",
    STRING_VALUE: "worker-string-source-canary",
    TEXT_VALUE: "worker-text-source-canary",
  },
});

/**
 * @param {string} directory
 * @param {string} name
 * @param {{ importedName: string, specifier: string }} rejected
 * @param {string} wrangler
 */
const assertRejectedBuild = async (directory, name, rejected, wrangler) => {
  const sourcePath = resolve(directory, "rejected", `${name}.ts`);
  const outputDirectory = resolve(directory, "rejected-output", name);
  await writeFile(
    sourcePath,
    [
      `import { ${rejected.importedName} } from ${JSON.stringify(rejected.specifier)};`,
      "",
      "export default {",
      "  fetch(): Response {",
      `    return new Response(typeof ${rejected.importedName});`,
      "  },",
      "};",
      "",
    ].join("\n")
  );
  const result = runResult(
    process.execPath,
    [wrangler, "deploy", sourcePath, "--dry-run", "--outdir", outputDirectory],
    directory
  );
  const output = `${result.stdout ?? ""}${result.stderr ?? ""}`;
  if (
    result.error !== undefined ||
    result.signal !== null ||
    result.status === null ||
    result.status === 0 ||
    !output.includes("@astilba/env")
  ) {
    fail(`Wrangler did not reject the ${name} package surface under workerd.`);
  }
  const emitted = (await collectFiles(outputDirectory)).filter((path) =>
    path.endsWith(".js")
  );
  if (emitted.length > 0) {
    fail(`Wrangler emitted the rejected ${name} package surface.`);
  }
};

/**
 * @param {string} directory
 * @param {string} archive
 */
const writeConsumerManifest = async (directory, archive) => {
  await writeFile(
    resolve(directory, "package.json"),
    `${JSON.stringify(
      {
        dependencies: {
          "@astilba/env": pathToFileURL(archive).href,
        },
        devDependencies: {
          typescript: "6.0.3",
          wrangler: WRANGLER_VERSION,
        },
        name: "astilba-env-workers-consumer",
        private: true,
        type: "module",
      },
      undefined,
      2
    )}\n`
  );
};

/**
 * @param {readonly string[]} arguments_
 */
const runWorkersConsumer = async (arguments_) => {
  if (arguments_.length > 0) {
    return fail("Cloudflare Workers consumer accepts no arguments.");
  }
  const [baseline, environmentDefinition, { archive, sha256 }] =
    await Promise.all([
      readGeneratedV1WorkerBaseline(),
      readWorkerAdmissionV1Fixture(),
      readArtifact(),
    ]);
  const consumer = await createConsumer();
  try {
    await writeConsumerManifest(consumer, archive);
    installArchive("npm", consumer, ["--package-lock=false"]);
    await assertCleanArchiveInstall(consumer, {
      allowManagerMetadata: false,
      manager: "npm",
    });

    const wrangler = resolve(
      consumer,
      "node_modules",
      "wrangler",
      "bin",
      "wrangler.js"
    );
    if (
      run(process.execPath, [wrangler, "--version"], consumer).trim() !==
      WRANGLER_VERSION
    ) {
      fail("Wrangler differs from the Cloudflare Workers admission pin.");
    }
    const stockWorkerd = await assertStockWorkerd(consumer);
    await assertBrowserDefault(consumer);
    await mkdir(resolve(consumer, "src"));
    await mkdir(resolve(consumer, "rejected"));
    await writeFile(
      resolve(consumer, "wrangler.jsonc"),
      `// Cloudflare Workers admission fixture; no compatibility shim or secret value.\n${JSON.stringify(
        wranglerConfiguration("src/index.ts", false),
        undefined,
        2
      )}\n`
    );
    await writeFile(resolve(consumer, "astilba.env.ts"), environmentDefinition);
    const cli = resolve(
      consumer,
      "node_modules",
      "@astilba",
      "env",
      "dist",
      "cli",
      "astilba-env.js"
    );
    run(process.execPath, [cli, "generate"], consumer, {
      ...process.env,
      BUILD_LABEL: "build-boundary",
    });
    const generatedDeploymentPath = resolve(
      consumer,
      ".astilba",
      "env",
      "workerDeployment.server.ts"
    );
    const generatedDeploymentBytes = await readFile(generatedDeploymentPath);
    const generatedDeploymentDigest = createHash("sha256")
      .update(generatedDeploymentBytes)
      .digest("hex");
    if (
      generatedDeploymentBytes.byteLength !== baseline.size ||
      generatedDeploymentDigest !== baseline.sha256
    ) {
      fail(
        "Generated Worker target diverges from the public @astilba/env@0.1.0 v1 byte baseline."
      );
    }
    const generatedDeploymentSource =
      generatedDeploymentBytes.toString("utf-8");
    const newerGeneratedSource = generatedDeploymentSource.replace(
      "astilba.env.generated-module/v1",
      "astilba.env.generated-module/v2"
    );
    if (newerGeneratedSource === generatedDeploymentSource) {
      fail("Generated Worker target has no v1 handshake marker to test.");
    }
    await writeFile(
      resolve(consumer, "src", "newer.server.ts"),
      newerGeneratedSource
    );
    run(process.execPath, [wrangler, "types"], consumer);
    run(process.execPath, [wrangler, "types", "--check"], consumer);

    const generatedTypes = await readFile(
      resolve(consumer, "worker-configuration.d.ts"),
      "utf-8"
    );
    for (const marker of [
      "interface Env",
      "FEATURE_ENABLED",
      "BOOLEAN_VALUE",
      "ENUM_VALUE",
      "INTEGER_VALUE",
      "JSON_VALUE",
      "PUBLIC_ORIGIN",
      "SAFE_INTEGER_VALUE",
      "STRING_LIST_VALUE",
      "STRING_VALUE",
      "TEXT_VALUE",
      "SELECTED_KV",
      "UNRELATED_KV",
      "WORKER_SECRET",
      "KVNamespace",
    ]) {
      if (!generatedTypes.includes(marker)) {
        fail(`Wrangler did not generate the expected Env marker: ${marker}`);
      }
    }
    if (
      generatedTypes.includes("test-only-placeholder") ||
      generatedTypes.includes(SECRET_SOURCE_CANARY)
    ) {
      fail("Wrangler-generated types contain a test-only secret value.");
    }

    await writeFile(resolve(consumer, "src", "index.ts"), workerSource);
    await writeFile(
      resolve(consumer, "tsconfig.json"),
      `${JSON.stringify(
        {
          compilerOptions: {
            exactOptionalPropertyTypes: true,
            lib: ["ES2024"],
            module: "ESNext",
            moduleResolution: "Bundler",
            noEmit: true,
            noUncheckedIndexedAccess: true,
            strict: true,
            target: "ES2024",
            verbatimModuleSyntax: true,
          },
          include: [
            ".astilba/env/**/*.ts",
            "src/**/*.ts",
            "worker-configuration.d.ts",
          ],
        },
        undefined,
        2
      )}\n`
    );
    run(
      process.execPath,
      [
        resolve(consumer, "node_modules", "typescript", "bin", "tsc"),
        "--project",
        "tsconfig.json",
      ],
      consumer
    );

    const artifactDirectory = resolve(consumer, "worker-artifact");
    run(
      process.execPath,
      [wrangler, "deploy", "--dry-run", "--outdir", artifactDirectory],
      consumer
    );
    const artifactFiles = await collectFiles(artifactDirectory);
    const artifactModules = artifactFiles.filter((path) =>
      path.endsWith(".js")
    );
    if (artifactModules.length !== 1) {
      fail("Wrangler did not emit exactly one Worker JavaScript artifact.");
    }
    const artifactModule =
      artifactModules[0] ?? fail("Wrangler Worker artifact is missing.");
    const artifactBytes = await readFile(artifactModule);
    const artifactSource = artifactBytes.toString("utf-8");
    for (const marker of [
      "nodejs_compat",
      "node:",
      "UNRELATED_KV",
      "@astilba/env/browser",
      "@astilba/env/vite",
      "bootstrap",
      "undeclared-target-material-canary",
    ]) {
      if (artifactSource.includes(marker)) {
        fail(
          "Worker artifact contains forbidden runtime or undeclared target material."
        );
      }
    }
    for (const canary of [
      PUBLIC_ORIGIN_SOURCE_CANARY,
      JSON_SOURCE_CANARY,
      SECRET_SOURCE_CANARY,
      "worker-string-source-canary",
      "worker-text-source-canary",
      "unrelated-capability-canary",
    ]) {
      if (artifactBytes.includes(Buffer.from(canary, "utf-8"))) {
        fail("Worker artifact contains an embedded source canary.");
      }
    }
    const configurationSource = await readFile(
      resolve(consumer, "wrangler.jsonc"),
      "utf-8"
    );
    if (
      configurationSource.includes("nodejs_compat") ||
      !configurationSource.includes('"compatibility_flags": []')
    ) {
      fail("Workers admission configuration enables an unexpected flag.");
    }

    const harnessMain = portablePath(relative(consumer, artifactModule));
    await writeFile(
      resolve(consumer, "wrangler.harness.jsonc"),
      `// Executes the exact dry-run artifact without rebundling it.\n${JSON.stringify(
        wranglerConfiguration(harnessMain, true),
        undefined,
        2
      )}\n`
    );
    await writeFile(resolve(consumer, "harness.mjs"), harnessSource);
    const harnessResult = run(
      process.execPath,
      ["harness.mjs"],
      consumer
    ).trim();
    const parsedHarnessResult = parseHarnessResult(harnessResult);
    const harnessArtifact = parsedHarnessResult.artifact;
    if (
      harnessArtifact !==
      createHash("sha256").update(artifactBytes).digest("hex")
    ) {
      fail("The workerd harness did not prove the exact Worker artifact.");
    }

    for (const [name, rejected] of Object.entries(rejectedImports)) {
      await assertRejectedBuild(consumer, name, rejected, wrangler);
    }

    process.stdout.write(
      `${JSON.stringify({
        archive: sha256,
        artifact: harnessArtifact,
        compatibilityDate: COMPATIBILITY_DATE,
        deploymentBindingSets: 2,
        firstPartyCodecs: true,
        generatedV1Baseline: {
          package: baseline.package,
          sha256: baseline.sha256,
          size: baseline.size,
        },
        nodejsCompat: false,
        passed: true,
        rejectedSurfaces: Object.keys(rejectedImports).length,
        runtime: "workerd",
        stockWorkerd,
        wrangler: WRANGLER_VERSION,
      })}\n`
    );
  } finally {
    await removeConsumer(consumer);
  }
};

if (process.argv[1] === import.meta.filename) {
  await runWorkersConsumer(process.argv.slice(2));
}
