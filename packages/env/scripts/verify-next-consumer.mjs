// @ts-check
/// <reference types="node" />

import { spawn, spawnSync } from "node:child_process";
import {
  access,
  mkdir,
  readdir,
  readFile,
  rm,
  writeFile,
} from "node:fs/promises";
import { resolve } from "node:path";

import {
  assertCleanArchiveInstall,
  createConsumer,
  fail,
  formatRunSummary,
  installArchive,
  readArtifact,
  removeConsumer,
  run,
  writeConsumerManifest,
} from "./matrix-artifact.mjs";

const modes = new Set([
  "app-static",
  "app-request",
  "pages-static",
  "pages-request",
]);

/**
 * @param {readonly string[]} arguments_
 */
export const resolveNextConsumerArguments = (arguments_) => {
  const [nextVersion, mode] = arguments_;
  if (
    arguments_.length !== 2 ||
    (nextVersion !== "15.5.22" && nextVersion !== "16.2.12") ||
    typeof mode !== "string" ||
    !modes.has(mode)
  ) {
    return fail(
      "Usage: node scripts/verify-next-consumer.mjs <15.5.22|16.2.12> <app-static|app-request|pages-static|pages-request>"
    );
  }
  return { mode, nextVersion };
};

const configuredEnvironment = Object.freeze({
  CLIENT_MODE: "standard",
  COUNT: "7",
  EMPTY_VALUE: "",
  ENABLED: "true",
  INTERNAL_VALUE: "private-next-canary",
  LABEL: "build",
  NEXT_TELEMETRY_DISABLED: "1",
});

const definition = [
  'import { defineEnvironment, env } from "@astilba/env";',
  "export default defineEnvironment({",
  '  id: "com.example.nextarchive",',
  "  entries: {",
  '    clientMode: env.public.build.enum(["standard", "compact"]),',
  "    count: env.public.deployment.safeInteger({ maximum: 99, minimum: 0 }),",
  "    empty: env.public.deployment.string({ required: false }),",
  "    enabled: env.public.deployment.boolean(),",
  "    internalValue: env.private.deployment.secret(),",
  "    label: env.public.deployment.string(),",
  "  },",
  '  consumers: { server: env.server(), web: env.browser(["clientMode", "count", "empty", "enabled", "label"]) },',
  "  targets: {",
  '    webBuild: env.process("web", { clientMode: "CLIENT_MODE" }),',
  '    serverDeployment: env.process("server", { count: "COUNT", empty: "EMPTY_VALUE", enabled: "ENABLED", internalValue: "INTERNAL_VALUE", label: "LABEL" }),',
  '    webDeployment: env.process("web", { count: "COUNT", empty: "EMPTY_VALUE", enabled: "ENABLED", label: "LABEL" }),',
  "  },",
  "});",
  "",
].join("\n");

const validClient = [
  '"use client";',
  'import { loadBrowserBootstrap } from "@astilba/env/browser";',
  'import { projection } from "../.astilba/env/browser/web.deployment.ts";',
  "export const EnvClient = () => { void loadBrowserBootstrap; return <output data-env-digest={projection.digest}>env-ready</output>; };",
  "",
].join("\n");

const staticClient = [
  '"use client";',
  'import { configuration } from "../.astilba/env/browser/web.build.ts";',
  'import { projection } from "../.astilba/env/browser/web.deployment.ts";',
  "export const EnvClient = () => <output data-env-digest={projection.digest}>{configuration.clientMode}</output>;",
  "",
].join("\n");

const invalidClient = [
  '"use client";',
  'import { checkProcessTarget } from "@astilba/env/runtime";',
  "export const EnvClient = () => <output>{String(checkProcessTarget)}</output>;",
  "",
].join("\n");

const route = [
  'import { NextResponse } from "next/server";',
  'import { projection } from "../../../.astilba/env/browser/web.deployment.ts";',
  'import { check } from "../../../.astilba/env/serverDeployment.server.ts";',
  'export const dynamic = "force-dynamic";',
  "export function GET(request) {",
  "  const result = check(process.env);",
  "  if (!result.ok) return NextResponse.json({ diagnostics: result.diagnostics, ok: false }, { status: 500 });",
  "  const values = result.value;",
  '  return NextResponse.json({ audience: { origin: request.nextUrl.origin }, consumer: projection.consumer, contract: projection.contract, lifecycle: projection.lifecycle, projection: projection.digest, protocol: "astilba.env.bootstrap/v1", values: { count: values.count, empty: values.empty, enabled: values.enabled, label: values.label } }, { headers: { "Cache-Control": "private, no-store" } });',
  "}",
  "",
].join("\n");

/** @param {string} directory @param {string} nextVersion */
const writeSharedFixture = async (directory, nextVersion) => {
  await writeFile(resolve(directory, "astilba.env.ts"), definition);
  await mkdir(resolve(directory, "app", "api", "env"), { recursive: true });
  await writeFile(
    resolve(directory, "app", "layout.js"),
    "export default function Layout({ children }) { return <html><body>{children}</body></html>; }\n"
  );
  await writeFile(resolve(directory, "app", "client.js"), invalidClient);
  await writeFile(
    resolve(directory, "app", "page.js"),
    'import { EnvClient } from "./client";\nexport default function Page() { return <EnvClient />; }\n'
  );
  await writeFile(resolve(directory, "app", "api", "env", "route.js"), route);
  if (nextVersion === "15.5.22") {
    await writeFile(
      resolve(directory, "middleware.js"),
      'import { NextResponse } from "next/server";\nimport { check } from "./.astilba/env/serverDeployment.server.ts";\nexport const runtime = "nodejs";\nexport function middleware() { return check(process.env).ok ? NextResponse.next() : new NextResponse("invalid", { status: 500 }); }\n'
    );
  } else {
    await writeFile(
      resolve(directory, "proxy.js"),
      'import { NextResponse } from "next/server";\nimport { check } from "./.astilba/env/serverDeployment.server.ts";\nexport function proxy() { return check(process.env).ok ? NextResponse.next() : new NextResponse("invalid", { status: 500 }); }\n'
    );
  }
};

/** @param {string} directory @param {string} mode */
const writeModeFixture = async (directory, mode) => {
  await writeFile(
    resolve(directory, "app", "client.js"),
    mode === "app-static" || mode === "pages-static"
      ? staticClient
      : validClient
  );
  if (mode === "app-static" || mode === "app-request") {
    await writeFile(
      resolve(directory, "app", "page.js"),
      mode === "app-static"
        ? 'import { EnvClient } from "./client";\nexport default function Page() { return <EnvClient />; }\n'
        : 'import { check } from "../.astilba/env/serverDeployment.server.ts";\nimport { EnvClient } from "./client";\nexport const dynamic = "force-dynamic";\nexport default function Page() { const result = check(process.env); return <main>{result.ok ? <><EnvClient />{result.value.label}</> : "invalid"}</main>; }\n'
    );
    return;
  }
  await rm(resolve(directory, "app", "page.js"));
  await mkdir(resolve(directory, "pages"));
  await writeFile(
    resolve(directory, "pages", "_document.js"),
    'import { Html, Head, Main, NextScript } from "next/document";\nimport { projection } from "../.astilba/env/browser/web.deployment.ts";\nfunction EnvDocument({ envelope }) { return <Html><Head /><body><data id="astilba-env-bootstrap" value={envelope} /><Main /><NextScript /></body></Html>; }\nEnvDocument.getInitialProps = async (context) => { const initial = await context.defaultGetInitialProps(context); return { ...initial, envelope: JSON.stringify({ audience: { origin: "https://example.test" }, consumer: projection.consumer, contract: projection.contract, lifecycle: projection.lifecycle, projection: projection.digest, protocol: "astilba.env.bootstrap/v1", values: { count: 0, empty: "", enabled: false, label: "pending" } }) }; };\nexport default EnvDocument;\n'
  );
  await writeFile(
    resolve(directory, "pages", "index.js"),
    mode === "pages-static"
      ? 'import { EnvClient } from "../app/client";\nexport async function getStaticProps() { return { props: { mode: "static" } }; }\nexport default function Page() { return <EnvClient />; }\n'
      : 'import { check } from "../.astilba/env/serverDeployment.server.ts";\nimport { EnvClient } from "../app/client";\nexport async function getServerSideProps() { const result = check(process.env); return { props: { label: result.ok ? result.value.label : "invalid" } }; }\nexport default function Page({ label }) { return <main><EnvClient />{label}</main>; }\n'
  );
};

/** @param {string} directory */
const assertClientRejectsServerGraph = async (directory) => {
  const next = resolve(directory, "node_modules/next/dist/bin/next");
  const result = spawnSync(process.execPath, [next, "build"], {
    cwd: directory,
    encoding: "utf-8",
    env: { ...process.env, ...configuredEnvironment },
    stdio: ["ignore", "pipe", "pipe"],
    timeout: 120_000,
  });
  const output = `${result.stdout}${result.stderr}`;
  const namesClientModule = /\bapp[\\/]client\.js\b/u.test(output);
  const namesRuntimeOnlyImport =
    /@astilba[\\/]env[\\/]runtime\b/u.test(output) ||
    (/@astilba[\\/]env\b/u.test(output) &&
      /\bPackage path\s+["']?\.?[\\/]runtime["']?\s+is not exported\b/iu.test(
        output
      ));
  if (
    result.error !== undefined ||
    result.signal !== null ||
    result.status === null ||
    result.status === 0 ||
    !namesRuntimeOnlyImport ||
    !namesClientModule
  ) {
    fail(
      `Next client-boundary build was not a causal ordinary refusal (${JSON.stringify({ clientModule: namesClientModule, outputCharacters: output.length, runtimeOnlyImport: namesRuntimeOnlyImport, signal: result.signal, spawned: result.error === undefined, status: result.status })}).`
    );
  }
  await rm(resolve(directory, ".next"), { force: true, recursive: true });
};

/** @param {string} directory */
const build = async (directory) => {
  const result = spawnSync(
    process.execPath,
    [resolve(directory, "node_modules/next/dist/bin/next"), "build"],
    {
      cwd: directory,
      encoding: "utf-8",
      env: { ...process.env, ...configuredEnvironment },
      stdio: ["ignore", "pipe", "pipe"],
      timeout: 120_000,
    }
  );
  if (
    result.error !== undefined ||
    result.signal !== null ||
    result.status !== 0
  ) {
    fail(formatRunSummary("Next", ["build"], result));
  }
  await access(resolve(directory, ".next/BUILD_ID"));
};

/** @param {string} directory */
const scanClientArtifacts = async (directory) => {
  const markers = [
    "INTERNAL_VALUE",
    "internalValue",
    "private-next-canary",
    "serverDeployment.server",
  ];
  /** @param {string} current */
  const visit = async (current) => {
    for (const entry of await readdir(current, { withFileTypes: true })) {
      const path = resolve(current, entry.name);
      if (entry.isDirectory()) {
        await visit(path);
      } else if (
        entry.isFile() &&
        (path.endsWith(".js") || path.endsWith(".map"))
      ) {
        const source = await readFile(path, "utf-8");
        if (markers.some((marker) => source.includes(marker))) {
          fail("Next client artifact contains private Env material.");
        }
      }
    }
  };
  await visit(resolve(directory, ".next/static"));
};

/**
 * @param {string} directory
 * @param {string} nextVersion
 * @param {string} mode
 * @param {string | undefined} label
 * @param {boolean} [expectFailure]
 */
const readEnvelope = async (
  directory,
  nextVersion,
  mode,
  label,
  expectFailure = false
) => {
  const modeOffset =
    mode === "app-request"
      ? 1
      : mode === "app-static"
        ? 2
        : mode === "pages-request"
          ? 3
          : 4;
  const port = (nextVersion === "15.5.22" ? 31_550 : 31_560) + modeOffset;
  const endpoint = new URL(`/api/env`, `http://localhost:${port}`);
  const child = spawn(
    process.execPath,
    [
      resolve(directory, "node_modules/next/dist/bin/next"),
      "start",
      "--port",
      String(port),
    ],
    {
      cwd: directory,
      env: { ...process.env, ...configuredEnvironment, LABEL: label },
      stdio: "ignore",
    }
  );
  /** @type {Error | undefined} */
  let childFailure;
  /** @type {{ exitCode: number | null, signal: NodeJS.Signals | null } | undefined} */
  let childExit;
  /** @type {Promise<void>} */
  const childClosed = new Promise((settle) => {
    child.once("error", (error) => {
      childFailure = error;
    });
    child.once("close", (exitCode, signal) => {
      childExit = { exitCode, signal };
      settle();
    });
  });
  try {
    const readinessDeadline = Date.now() + 30_000;
    while (Date.now() < readinessDeadline) {
      if (childFailure !== undefined) {
        fail(`Next failed to start: ${childFailure.message}`);
      }
      if (childExit !== undefined) {
        fail(
          `Next exited before serving the generated Env bootstrap route (${childExit.exitCode ?? childExit.signal ?? "unknown"}).`
        );
      }
      let response;
      try {
        response = await fetch(endpoint, { redirect: "error" });
      } catch {
        // Next is still starting.
      }
      if (response !== undefined && (response.ok || expectFailure)) {
        const transportOrigin = new URL(response.url).origin;
        if (transportOrigin !== endpoint.origin) {
          fail(
            "Next bootstrap response origin did not match the canonical endpoint."
          );
        }
        const source = await response.text();
        if (source.includes("private-next-canary")) {
          fail("Next bootstrap response disclosed a private value.");
        }
        if (expectFailure && response.status !== 500) {
          fail("Next accepted a missing required public value.");
        }
        if (!expectFailure && !response.ok) {
          fail("Next rejected a valid Env bootstrap response.");
        }
        return { origin: transportOrigin, source };
      }
      await new Promise((resume) => {
        setTimeout(resume, 150);
      });
    }
    return fail("Next did not serve the generated Env bootstrap route.");
  } finally {
    if (childExit === undefined) {
      child.kill("SIGTERM");
      await Promise.race([
        childClosed,
        new Promise((settle) => {
          setTimeout(settle, 5000);
        }),
      ]);
      if (childExit === undefined) {
        child.kill("SIGKILL");
        await childClosed;
      }
    }
  }
};

/** @param {string} directory @param {string} source @param {string} origin */
const validateEnvelope = async (directory, source, origin) => {
  await writeFile(
    resolve(directory, "validate-bootstrap.mjs"),
    'import { parseBrowserBootstrap } from "./node_modules/@astilba/env/dist/browser/index.js";\nimport { projection } from "./.astilba/env/browser/web.deployment.ts";\nconst result = parseBrowserBootstrap({ expectedAudience: { origin: process.argv[3] }, projection, source: process.argv[2] });\nif (result.values.count !== 7 || result.values.empty !== "" || result.values.enabled !== true) throw new Error("Typed browser values were not preserved.");\n'
  );
  run(
    process.execPath,
    ["--experimental-strip-types", "validate-bootstrap.mjs", source, origin],
    directory
  );
};

/**
 * @param {readonly string[]} arguments_
 */
const runNextConsumer = async (arguments_) => {
  const { mode, nextVersion } = resolveNextConsumerArguments(arguments_);
  const { archive, sha256 } = await readArtifact();
  const consumer = await createConsumer();
  try {
    await writeConsumerManifest(consumer, archive);
    installArchive("npm", consumer, [
      `next@${nextVersion}`,
      "react@19.2.8",
      "react-dom@19.2.8",
    ]);
    await assertCleanArchiveInstall(consumer, {
      allowManagerMetadata: false,
      manager: "npm",
    });
    await writeSharedFixture(consumer, nextVersion);
    const cli = resolve(
      consumer,
      "node_modules/@astilba/env/dist/cli/astilba-env.js"
    );
    run(process.execPath, [cli, "generate"], consumer, {
      ...process.env,
      CLIENT_MODE: "standard",
    });
    await assertClientRejectsServerGraph(consumer);
    await writeModeFixture(consumer, mode);
    await build(consumer);
    await scanClientArtifacts(consumer);
    const alpha = await readEnvelope(consumer, nextVersion, mode, "alpha");
    await validateEnvelope(consumer, alpha.source, alpha.origin);
    const beta = await readEnvelope(consumer, nextVersion, mode, "beta");
    if (alpha.source === beta.source) {
      fail(
        "One built Next artifact did not observe distinct deployment values."
      );
    }
    await readEnvelope(consumer, nextVersion, mode, undefined, true);
    process.stdout.write(
      `${JSON.stringify({ archive: sha256, mode, next: nextVersion, passed: true })}\n`
    );
  } finally {
    await removeConsumer(consumer);
  }
};

if (process.argv[1] === import.meta.filename) {
  await runNextConsumer(process.argv.slice(2));
}
