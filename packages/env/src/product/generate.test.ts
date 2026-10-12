import { execFile as execFileCallback } from "node:child_process";
import { mkdtemp, readFile, rm, stat, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { resolve } from "node:path";

import { transform } from "esbuild";
import { afterEach, describe, expect, it } from "vitest";

import { inspectTree } from "../artifacts/tree.ts";
import { defineEnvironment, env } from "../authoring/index.ts";
import { canonicalJson } from "../core/index.ts";
import type { ProviderBindingPlan } from "../provider/types.ts";
import { encodeCliCompilationV1 } from "./compilation.ts";
import {
  compileProduct,
  compileProductFromCompilation,
  GENERATED_FORMAT,
  GeneratedOutputStaleError,
  generateEnvironment,
  prepareGeneratedOutput,
  writeGeneratedProduct,
} from "./index.ts";
import type { GeneratedProduct, ProductCompilation } from "./index.ts";

const temporaryRoots: string[] = [];

afterEach(async () => {
  await Promise.all(
    temporaryRoots.splice(0).map(async (root) => {
      await rm(root, { force: true, recursive: true });
    })
  );
});

const MAXIMUM_COMPILATION_BYTES = 8_388_608;
const MAXIMUM_GENERATED_FILE_BYTES = 8_388_608;
const MAXIMUM_GENERATED_FILES = 2048;
const MAXIMUM_GENERATED_TREE_BYTES = 67_108_864;

const execFile = async (
  file: string,
  arguments_: readonly string[],
  cwd: string
): Promise<void> => {
  await new Promise<void>((fulfill, reject) => {
    execFileCallback(file, arguments_, { cwd }, (error) => {
      if (error === null) {
        fulfill();
        return;
      }
      reject(
        error instanceof Error
          ? error
          : new Error("The test child-process command failed.")
      );
    });
  });
};

type BrowserProjection = Readonly<Record<string, unknown>> & {
  readonly decode: (
    input: Readonly<Record<string, unknown>>,
    failure: (code: string) => never
  ) => Readonly<Record<string, unknown>>;
};

const isRecord = (value: unknown): value is Readonly<Record<string, unknown>> =>
  typeof value === "object" && value !== null && !Array.isArray(value);

const isBrowserProjection = (value: unknown): value is BrowserProjection =>
  isRecord(value) && typeof value.decode === "function";

const isServerCheck = (
  value: unknown
): value is (source: Readonly<Record<string, unknown>>) => unknown =>
  typeof value === "function";

const declaration = (databaseSource = "DATABASE_URL") =>
  defineEnvironment({
    consumers: {
      database: env.server(["databaseUrl"]),
      server: env.server(),
    },
    entries: {
      authSecret: env.private.deployment.secret(),
      databaseUrl: env.private.deployment.text({
        normalise: "trim",
      }),
      port: env.public.deployment.integer({
        maximum: 65_535,
        minimum: 0,
      }),
    },
    id: "com.astilba.generated",
    targets: {
      database: env.process("database", {
        databaseUrl: databaseSource,
      }),
      server: env.process("server", {
        authSecret: "BETTER_AUTH_SECRET",
        databaseUrl: databaseSource,
        port: "PORT",
      }),
    },
  });

const configurationShape = {
  kind: "object",
  properties: [
    {
      name: "features",
      required: true,
      shape: {
        items: { kind: "string" },
        kind: "array",
        maximumItems: 4,
        minimumItems: 0,
      },
    },
    {
      name: "region",
      required: true,
      shape: { kind: "string" },
    },
  ],
} as const;

const typedDeclaration = () =>
  defineEnvironment({
    consumers: {
      browser: env.browser([
        "analyticsId",
        "apiOrigin",
        "appName",
        "clientConfiguration",
        "debug",
        "features",
        "port",
      ]),
      server: env.server(),
    },
    entries: {
      analyticsId: env.public.deployment.string({
        minimumCodePoints: 1,
        required: false,
      }),
      apiOrigin: env.public.deployment.origin(),
      appName: env.public.deployment.string({
        maximumCodePoints: 128,
        minimumCodePoints: 1,
      }),
      clientConfiguration: env.public.deployment.json(configurationShape),
      configuration: env.private.deployment.opaque({
        input: {
          kind: "optional",
          value: { kind: "string" },
        },
        output: configurationShape,
        revision: "1",
        semantics: "example/configuration@1",
      }),
      debug: env.public.deployment.boolean(),
      features: env.public.deployment.stringList({
        maximumItemCodePoints: 32,
        maximumItems: 8,
      }),
      port: env.public.deployment.safeInteger({
        maximum: 65_535,
        minimum: 1,
      }),
    },
    id: "com.astilba.generated-typed",
    targets: {
      server: env.process("server", {
        analyticsId: "ANALYTICS_ID",
        apiOrigin: "API_URL",
        appName: "APP_NAME",
        clientConfiguration: "PUBLIC_CONFIG",
        configuration: "CONFIG",
        debug: "DEBUG",
        features: "FEATURES",
        port: "PORT",
      }),
    },
  });

const temporaryRoot = async (): Promise<string> => {
  const root = await mkdtemp(resolve(tmpdir(), "astilba-env-product-"));
  temporaryRoots.push(root);
  return root;
};

const typecheckGeneratedBrowserModule = async (
  root: string,
  modulePath: string
): Promise<void> => {
  const configPath = resolve(root, "tsconfig.generated-browser.json");
  await writeFile(
    configPath,
    `${JSON.stringify({
      compilerOptions: {
        allowImportingTsExtensions: true,
        baseUrl: process.cwd(),
        ignoreDeprecations: "6.0",
        lib: ["ES2024", "DOM", "DOM.Iterable"],
        module: "ESNext",
        moduleResolution: "Bundler",
        noEmit: true,
        paths: {
          "@astilba/env/browser": ["src/browser/index.ts"],
        },
        strict: true,
        target: "ES2024",
      },
      files: [modulePath],
    })}\n`,
    "utf-8"
  );
  await execFile(
    process.execPath,
    [
      resolve(process.cwd(), "node_modules/typescript/bin/tsc"),
      "--project",
      configPath,
    ],
    root
  );
};

const importBrowserProjection = async (
  source: string
): Promise<
  Readonly<{
    projection: Readonly<Record<string, unknown>> & {
      decode: (
        input: Readonly<Record<string, unknown>>,
        failure: (code: string) => never
      ) => Readonly<Record<string, unknown>>;
    };
  }>
> => {
  const transformed = await transform(source, {
    format: "esm",
    loader: "ts",
    target: "es2022",
  });
  const imported: unknown = await import(
    `data:text/javascript;base64,${Buffer.from(transformed.code).toString("base64")}`
  );
  if (!isRecord(imported) || !isBrowserProjection(imported.projection)) {
    throw new TypeError("Generated browser projection has an invalid shape.");
  }
  return { projection: imported.projection };
};

const importServerDefinition = async (
  source: string
): Promise<Readonly<Record<string, unknown>>> => {
  const stubbed = source.replace(
    /import \{[\s\S]*?\} from "@astilba\/env\/runtime";/u,
    [
      "const checkProcessTarget = <TValue>(definition: unknown, _source: unknown): unknown => definition;",
      "const loadProcessTarget = <TValue>(definition: unknown, _source: unknown): unknown => definition;",
    ].join("\n")
  );
  const transformed = await transform(stubbed, {
    format: "esm",
    loader: "ts",
    target: "es2022",
  });
  const imported: unknown = await import(
    `data:text/javascript;base64,${Buffer.from(transformed.code).toString("base64")}`
  );
  if (!isRecord(imported) || !isServerCheck(imported.check)) {
    throw new TypeError("Generated server definition has an invalid shape.");
  }
  const definition: unknown = imported.check({});
  if (!isRecord(definition)) {
    throw new TypeError("Generated server definition is not a record.");
  }
  return definition;
};

const withFiles = (
  product: GeneratedProduct,
  files: ReadonlyMap<string, string>
): GeneratedProduct =>
  Object.freeze({
    compiled: product.compiled,
    files,
    snapshot: product.snapshot,
  });

const buildDeclaration = () =>
  defineEnvironment({
    consumers: {
      browser: env.browser(["releaseSha"]),
    },
    entries: {
      releaseSha: env.public.build.string({
        minimumCodePoints: 1,
      }),
    },
    id: "com.astilba.build-output",
    targets: {
      browserBuild: env.process("browser", {
        releaseSha: "RELEASE_SHA",
      }),
    },
  });

const browserBuildPlan: ProviderBindingPlan = Object.freeze({
  adapterAbi: "astilba.env.adapter.process-record/v1",
  bindings: Object.freeze([
    Object.freeze({
      channel: "build",
      class: "non-confidential",
      entry: "releaseSha",
      kind: "public_text",
      rawName: "RELEASE_SHA",
    }),
  ]),
  format: "astilba.env.binding-plan/v1",
  target: "browserBuild",
});

describe("deterministic product generation", () => {
  it("produces byte-identical outputs and an exact generated target type", async () => {
    const root = await temporaryRoot();
    await generateEnvironment(declaration(), { projectRoot: root });
    const first = await inspectTree(resolve(root, ".astilba/env"));
    const serverModule = await readFile(
      resolve(root, ".astilba/env/server.server.ts"),
      "utf-8"
    );

    await generateEnvironment(declaration(), { projectRoot: root });
    const second = await inspectTree(resolve(root, ".astilba/env"));

    expect(second.digest).toBe(first.digest);
    expect(serverModule).toContain('readonly "authSecret": string;');
    expect(serverModule).toContain('readonly "port": number;');
    expect(serverModule).toContain('from "@astilba/env/runtime";');
    expect(serverModule).not.toContain("sensitive-test-value");
  });

  it("checks without writes and reports semantic, binding, and file drift", async () => {
    const root = await temporaryRoot();
    await generateEnvironment(declaration(), { projectRoot: root });
    const modulePath = resolve(root, ".astilba/env/server.server.ts");
    const before = await stat(modulePath);

    await generateEnvironment(declaration(), {
      check: true,
      projectRoot: root,
    });
    const checked = await stat(modulePath);
    expect(checked.mtimeMs).toBe(before.mtimeMs);

    await expect(
      generateEnvironment(declaration("DATABASE_CONNECTION"), {
        check: true,
        projectRoot: root,
      })
    ).rejects.toBeInstanceOf(GeneratedOutputStaleError);
    expect((await stat(modulePath)).mtimeMs).toBe(before.mtimeMs);

    await writeFile(modulePath, "manual drift\n", "utf-8");
    const drifted = await stat(modulePath);
    await expect(
      generateEnvironment(declaration(), {
        check: true,
        projectRoot: root,
      })
    ).rejects.toMatchObject({
      paths: ["server.server.ts"],
    });
    expect((await stat(modulePath)).mtimeMs).toBe(drifted.mtimeMs);
  });

  it("generates exact typed server and browser projections without private metadata", async () => {
    const root = await temporaryRoot();
    await generateEnvironment(typedDeclaration(), { projectRoot: root });
    const serverModule = await readFile(
      resolve(root, ".astilba/env/server.server.ts"),
      "utf-8"
    );
    const browserModule = await readFile(
      resolve(root, ".astilba/env/browser/browser.deployment.ts"),
      "utf-8"
    );

    expect(serverModule).toContain(
      'readonly "configuration": { readonly "features": readonly (string)[]; readonly "region": string; };'
    );
    expect(serverModule).toContain(
      'readonly "configuration": readonly [string | undefined, { readonly "features": readonly (string)[]; readonly "region": string; }];'
    );
    expect(serverModule).toContain(
      "loadProcessTargetWithSchemas<Configuration>"
    );
    expect(serverModule).toContain("Promise<Configuration>");
    expect(serverModule).toContain('readonly "debug": boolean;');
    expect(serverModule).toContain('readonly "features": readonly string[];');
    expect(serverModule).toContain('readonly "port": number;');
    expect(browserModule).toContain(
      "as unknown as BrowserProjection<Configuration>"
    );
    expect(browserModule).toContain(
      'Object.defineProperty(generatedProjection, "generated", { enumerable: true, value: "astilba.env.generated-module/v1" });'
    );
    expect(browserModule).toContain(
      'Object.defineProperty(generatedProjection, "lifecycle", { enumerable: true, value: "deployment" });'
    );
    expect(browserModule).not.toContain("const generatedProjection = {");
    expect(browserModule).toContain(
      'readonly "clientConfiguration": { readonly "features": readonly (string)[]; readonly "region": string; };'
    );
    expect(browserModule).toContain('readonly "debug": boolean;');
    expect(browserModule).toContain('readonly "features": readonly string[];');
    expect(browserModule).toContain('readonly "port": number;');
    expect(browserModule).toContain('failure("BOOTSTRAP_UNKNOWN_FIELD")');
    expect(browserModule).not.toContain("astilba.env.boolean-exact");
    expect(browserModule).not.toContain("astilba.env.safe-integer-decimal");
    expect(browserModule).not.toContain("astilba.env.string-list-comma");
    expect(browserModule).not.toContain('readonly "configuration"');
    expect(browserModule).not.toContain("example/configuration");
    expect(browserModule).not.toContain("CONFIG");
    expect(browserModule).not.toContain("private");

    const imported = await importBrowserProjection(browserModule);
    const failure = (code: string): never => {
      throw new Error(code);
    };
    const clientConfiguration = Object.freeze({
      features: Object.freeze(["alpha"]),
      region: "eu-west",
    });
    const decoded = imported.projection.decode(
      Object.freeze({
        apiOrigin: "https://api.example:443/",
        appName: "Example",
        clientConfiguration,
        debug: false,
        features: Object.freeze(["alpha", "beta"]),
        port: 4100,
      }),
      failure
    );

    expect(Reflect.ownKeys(decoded)).toStrictEqual([
      "apiOrigin",
      "appName",
      "clientConfiguration",
      "debug",
      "features",
      "port",
    ]);
    expect(decoded.apiOrigin).toBe("https://api.example");
    expect(decoded.appName).toBe("Example");
    expect(decoded.debug).toBe(false);
    expect(decoded.features).toStrictEqual(["alpha", "beta"]);
    expect(decoded.port).toBe(4100);
    expect(decoded.clientConfiguration).not.toBe(clientConfiguration);
    expect(Object.getPrototypeOf(decoded.clientConfiguration)).toBeNull();
    expect(Object.getPrototypeOf(decoded)).toBeNull();
    expect(Object.isFrozen(decoded)).toBe(true);
    expect(Object.isFrozen(decoded.features)).toBe(true);
    expect(Object.getPrototypeOf(imported.projection)).toBeNull();
    expect(Object.isFrozen(imported.projection)).toBe(true);
    expect(Reflect.ownKeys(imported.projection)).toStrictEqual([
      "codecAbi",
      "consumer",
      "contract",
      "decode",
      "digest",
      "format",
      "formatVersion",
      "generated",
      "kind",
      "lifecycle",
      "projectionAbi",
    ]);
    for (const key of Reflect.ownKeys(imported.projection)) {
      const descriptor = Object.getOwnPropertyDescriptor(
        imported.projection,
        key
      );
      expect(descriptor?.configurable).toBe(false);
      expect(descriptor?.enumerable).toBe(true);
      expect(descriptor?.writable).toBe(false);
    }
    expect(() =>
      imported.projection.decode(
        Object.freeze({
          ...decoded,
          unknown: true,
        }),
        failure
      )
    ).toThrow("BOOTSTRAP_UNKNOWN_FIELD");

    const rejectedOrigins = [
      "http://api.example.com",
      "HTTPS://api.example.com",
      " https://api.example.com",
      "https://API.example.com",
      "https://api.example.com/path",
      "https://api.example.com//",
      "https://api.example.com?query",
      "https://api.example.com#fragment",
      "https://user@api.example.com",
      "https://example",
      "https://localhost",
      "https://127.0.0.1",
      "https://127.0.00.1",
      "https://127.1",
      "https://127.0.1",
      "https://0x7f.0.0.1",
      "https://0x7f.0.0.0x1",
      "https://0x7f.0x0.0x0.0x1",
      "https://0177.0.0.1",
      "https://2130706433",
      "https://[::1]",
      "https://api.exämple",
      "https://api.example.com:0",
      "https://api.example.com:0443",
      "https://api.example.com:65536",
      "https://api.example.com:",
    ];
    const failures = rejectedOrigins.map((apiOrigin) => {
      try {
        imported.projection.decode(
          Object.freeze({
            ...decoded,
            apiOrigin,
          }),
          failure
        );
      } catch (error) {
        return error instanceof Error ? error.message : "non-error";
      }
      return "accepted";
    });
    expect(failures).toStrictEqual(
      rejectedOrigins.map(() => "BOOTSTRAP_VALUE_INVALID")
    );
  });

  it("leaves browser decoder separator lines empty for mixed optional and required entries", async () => {
    const root = await temporaryRoot();
    await generateEnvironment(typedDeclaration(), { projectRoot: root });
    const browserModule = await readFile(
      resolve(root, ".astilba/env/browser/browser.deployment.ts"),
      "utf-8"
    );
    const decoder = browserModule.slice(
      browserModule.indexOf("  const selected ="),
      browserModule.indexOf("  return Object.freeze(values)")
    );

    expect(decoder).toContain('  if (Object.hasOwn(input, "analyticsId")) {');
    expect(decoder).toContain(
      '  }\n\n  if (!Object.hasOwn(input, "apiOrigin")) {'
    );
    expect(decoder).toContain('  if (!Object.hasOwn(input, "appName")) {');
    expect(
      browserModule.split("\n").filter((line) => /\s+$/u.test(line))
    ).toStrictEqual([]);
  });

  it("typechecks generated public string-list and safe-integer browser projections in TypeScript 6", async () => {
    const root = await temporaryRoot();
    await generateEnvironment(typedDeclaration(), { projectRoot: root });
    const browserModule = resolve(
      root,
      ".astilba/env/browser/browser.deployment.ts"
    );

    await expect(
      typecheckGeneratedBrowserModule(root, browserModule)
    ).resolves.toBeUndefined();

    const imported = await importBrowserProjection(
      await readFile(browserModule, "utf-8")
    );
    const failure = (code: string): never => {
      throw new Error(code);
    };
    const values = Object.freeze({
      apiOrigin: "https://api.example",
      appName: "Example",
      clientConfiguration: Object.freeze({
        features: Object.freeze(["alpha"]),
        region: "eu-west",
      }),
      debug: false,
      features: Object.freeze(["alpha", "beta"]),
      port: 4100,
    });
    expect(imported.projection.decode(values, failure)).toMatchObject({
      features: ["alpha", "beta"],
      port: 4100,
    });
    expect(() =>
      imported.projection.decode(
        Object.freeze({ ...values, features: Object.freeze(["alpha", 0]) }),
        failure
      )
    ).toThrow("BOOTSTRAP_VALUE_INVALID");
    const sparse = ["alpha"];
    sparse.length = 3;
    sparse[2] = "beta";
    expect(() =>
      imported.projection.decode(
        Object.freeze({ ...values, features: Object.freeze(sparse) }),
        failure
      )
    ).toThrow("BOOTSTRAP_VALUE_INVALID");
    expect(() =>
      imported.projection.decode(
        Object.freeze({ ...values, port: 0.5 }),
        failure
      )
    ).toThrow("BOOTSTRAP_VALUE_INVALID");
  });

  it("renders an inert owned browser build configuration from an explicit source", async () => {
    const root = await temporaryRoot();

    await generateEnvironment(buildDeclaration(), {
      projectRoot: root,
      source: { RELEASE_SHA: "abc<&" },
    });
    const moduleSource = await readFile(
      resolve(root, ".astilba/env/browser/browser.build.ts"),
      "utf-8"
    );

    expect(moduleSource).toContain("\\u003c\\u0026");
    expect(moduleSource).not.toContain("RELEASE_SHA");
    expect(moduleSource).not.toContain("@astilba/env/runtime");
    expect(moduleSource).not.toContain("projection");

    const transformed = await transform(moduleSource, {
      format: "esm",
      loader: "ts",
      target: "es2022",
    });
    const imported: unknown = await import(
      `data:text/javascript;base64,${Buffer.from(transformed.code).toString("base64")}`
    );
    if (!isRecord(imported) || !isRecord(imported.configuration)) {
      throw new TypeError(
        "Generated browser configuration has an invalid shape."
      );
    }
    const configuration = imported.configuration;
    expect({ ...configuration }).toStrictEqual({
      releaseSha: "abc<&",
    });
    expect(Object.getPrototypeOf(configuration)).toBeNull();
    expect(Object.isFrozen(configuration)).toBe(true);
  });

  it("keeps boolean, numeric, and string-list build values as importable literals", async () => {
    const root = await temporaryRoot();
    const environment = defineEnvironment({
      consumers: {
        browser: env.browser(["count", "enabled", "labels"]),
        server: env.server(),
      },
      entries: {
        count: env.public.build.safeInteger({ maximum: 99, minimum: 0 }),
        enabled: env.public.build.boolean(),
        labels: env.public.build.stringList({
          minimumItemCodePoints: 2,
          maximumItemCodePoints: 16,
          maximumItems: 4,
        }),
      },
      id: "com.astilba.literal-build-values",
      targets: {
        browser: env.process("browser", {
          count: "COUNT",
          enabled: "ENABLED",
          labels: "LABELS",
        }),
        server: env.process("server", {
          count: "COUNT",
          enabled: "ENABLED",
          labels: "LABELS",
        }),
      },
    });

    await generateEnvironment(environment, {
      projectRoot: root,
      source: { COUNT: "7", ENABLED: "false", LABELS: "al,be" },
    });
    const moduleSource = await readFile(
      resolve(root, ".astilba/env/browser/browser.build.ts"),
      "utf-8"
    );
    expect(moduleSource).toContain("value: 7");
    expect(moduleSource).toContain("value: false");
    expect(moduleSource).toContain('Object.freeze(["al","be"])');
    expect(moduleSource).not.toContain('value: "false"');

    const transformed = await transform(moduleSource, {
      format: "esm",
      loader: "ts",
      target: "es2022",
    });
    const imported: unknown = await import(
      `data:text/javascript;base64,${Buffer.from(transformed.code).toString("base64")}`
    );
    if (!isRecord(imported) || !isRecord(imported.configuration)) {
      throw new TypeError(
        "Generated browser configuration has an invalid shape."
      );
    }
    expect({ ...imported.configuration }).toStrictEqual({
      count: 7,
      enabled: false,
      labels: ["al", "be"],
    });
    const serverModule = await readFile(
      resolve(root, ".astilba/env/server.server.ts"),
      "utf-8"
    );
    const serverDefinition = await importServerDefinition(serverModule);
    const renderedDefinition = JSON.stringify(serverDefinition);
    expect(renderedDefinition).toContain('"minimumItemCodePoints":2');
    expect(renderedDefinition).toContain('"maximumItemCodePoints":16');
  });

  it("renders application-controlled metadata only as inert owned literals", async () => {
    const root = await temporaryRoot();
    const dangerous =
      '");globalThis.__astilbaInjected=true;//</script><&\u2028\u2029';
    const propertyName = `field${dangerous}`;
    const environment = defineEnvironment({
      consumers: {
        browser: env.browser(["mode", "payload"]),
        server: env.server(["mode"]),
      },
      entries: {
        mode: env.public.deployment.enum(["safe", dangerous]),
        payload: env.public.deployment.json({
          kind: "object",
          properties: [
            {
              name: propertyName,
              required: true,
              shape: { kind: "string" },
            },
          ],
        }),
      },
      id: "com.astilba.literal-safety",
      targets: {
        server: env.process("server", {
          mode: "MODE",
        }),
      },
    });

    await generateEnvironment(environment, { projectRoot: root });
    const browserModule = await readFile(
      resolve(root, ".astilba/env/browser/browser.deployment.ts"),
      "utf-8"
    );
    const serverModule = await readFile(
      resolve(root, ".astilba/env/server.server.ts"),
      "utf-8"
    );
    for (const moduleSource of [browserModule, serverModule]) {
      expect(moduleSource).not.toContain("</script>");
      expect(moduleSource).not.toContain("\u2028");
      expect(moduleSource).not.toContain("\u2029");
      expect(moduleSource).toContain(
        "\\u003c/script\\u003e\\u003c\\u0026\\u2028\\u2029"
      );
    }
    expect(browserModule).toContain("new Set(Object.freeze([");
    expect(serverModule).toContain("const definition = (() => {");
    expect(serverModule).toContain(
      "const output = Object.create(null) as Record<string, unknown>;"
    );
    expect(serverModule).toContain("Object.freeze([");

    const importedBrowser = await importBrowserProjection(browserModule);
    const decoded = importedBrowser.projection.decode(
      Object.freeze({
        mode: dangerous,
        payload: Object.freeze({
          [propertyName]: "safe",
        }),
      }),
      (code): never => {
        throw new Error(code);
      }
    );
    const payload: unknown = decoded.payload;
    expect(decoded.mode).toBe(dangerous);
    expect(Object.getPrototypeOf(decoded)).toBeNull();
    expect(Object.isFrozen(decoded)).toBe(true);
    if (typeof payload !== "object" || payload === null) {
      throw new TypeError("Expected an owned generated payload.");
    }
    expect(Object.getPrototypeOf(payload)).toBeNull();
    expect(Object.isFrozen(payload)).toBe(true);
    expect(Reflect.get(payload, propertyName)).toBe("safe");

    const definition = await importServerDefinition(serverModule);
    expect(Object.getPrototypeOf(definition)).toBeNull();
    expect(Object.isFrozen(definition)).toBe(true);
    const bindings: unknown = definition.bindings;
    if (!Array.isArray(bindings)) {
      throw new TypeError("Expected generated bindings.");
    }
    expect(Object.isFrozen(bindings)).toBe(true);
    const binding: unknown = bindings[0];
    if (typeof binding !== "object" || binding === null) {
      throw new TypeError("Expected a generated binding.");
    }
    expect(Object.getPrototypeOf(binding)).toBeNull();
    expect(Object.isFrozen(binding)).toBe(true);
    expect(Reflect.get(binding, "source")).toBe("MODE");
  });

  it("enforces the exact CliCompilationV1 byte boundary before source observation", async () => {
    const product = await compileProduct(buildDeclaration(), {
      RELEASE_SHA: "safe",
    });
    const projection = product.compiled.projections[0];
    if (projection === undefined) {
      throw new TypeError("Expected a compiled browser projection.");
    }
    const inputWithContract = (contract: string): ProductCompilation => ({
      compiled: {
        full: product.compiled.full,
        projections: [
          {
            ...projection,
            manifest: {
              ...projection.manifest,
              contract,
            },
          },
        ],
      },
      targets: [
        {
          bindingPlan: browserBuildPlan,
          consumer: "browser",
        },
      ],
    });
    const overhead = encodeCliCompilationV1(inputWithContract("")).byteLength;
    const exact = inputWithContract(
      "x".repeat(MAXIMUM_COMPILATION_BYTES - overhead)
    );
    expect(encodeCliCompilationV1(exact).byteLength).toBe(
      MAXIMUM_COMPILATION_BYTES
    );

    let sourceReads = 0;
    const throwingSource = {
      get RELEASE_SHA(): never {
        sourceReads += 1;
        throw new Error("source must remain unobserved");
      },
    };
    expect(() =>
      compileProductFromCompilation(
        inputWithContract("x".repeat(MAXIMUM_COMPILATION_BYTES - overhead + 1)),
        throwingSource
      )
    ).toThrow("The compiled declaration is too large.");
    expect(sourceReads).toBe(0);
  });

  it("enforces exact generated-output byte and file boundaries before check-mode writes", async () => {
    const product = await compileProduct(declaration());
    const root = await temporaryRoot();
    const prepared = await prepareGeneratedOutput({ projectRoot: root });
    const outputRoot = resolve(root, ".astilba/env");
    const forge = (files: Map<string, string>): GeneratedProduct => {
      files.set(
        "manifest.json",
        `${canonicalJson({
          files: Object.freeze(
            [...files.keys()]
              .filter((path) => path !== "manifest.json")
              .toSorted()
          ),
          format: GENERATED_FORMAT,
        })}\n`
      );
      return withFiles(product, files);
    };
    const expectStaleWithoutWrites = async (
      candidate: GeneratedProduct
    ): Promise<void> => {
      await expect(
        writeGeneratedProduct(candidate, prepared, true)
      ).rejects.toBeInstanceOf(GeneratedOutputStaleError);
      await expect(stat(outputRoot)).rejects.toMatchObject({ code: "ENOENT" });
    };
    const expectRejectedWithoutWrites = async (
      candidate: GeneratedProduct,
      message: string
    ): Promise<void> => {
      await expect(
        writeGeneratedProduct(candidate, prepared, true)
      ).rejects.toThrow(message);
      await expect(stat(outputRoot)).rejects.toMatchObject({ code: "ENOENT" });
    };

    const exactFile = new Map(product.files);
    exactFile.set(
      "limitFile.server.ts",
      "x".repeat(MAXIMUM_GENERATED_FILE_BYTES)
    );
    await expectStaleWithoutWrites(forge(exactFile));

    const overlongFile = new Map(exactFile);
    overlongFile.set(
      "limitFile.server.ts",
      "x".repeat(MAXIMUM_GENERATED_FILE_BYTES + 1)
    );
    await expectRejectedWithoutWrites(
      forge(overlongFile),
      "Astilba Env generated output exceeds its per-file limit."
    );

    const exactFiles = new Map(product.files);
    for (
      let index = exactFiles.size;
      index < MAXIMUM_GENERATED_FILES;
      index += 1
    ) {
      exactFiles.set(`limit${index}.server.ts`, "");
    }
    expect(exactFiles.size).toBe(MAXIMUM_GENERATED_FILES);
    await expectStaleWithoutWrites(forge(exactFiles));

    const extraFile = new Map(exactFiles);
    extraFile.set(`limit${MAXIMUM_GENERATED_FILES}.server.ts`, "");
    await expectRejectedWithoutWrites(
      forge(extraFile),
      "Astilba Env generated output exceeds its file limit."
    );

    const exactTree = new Map(product.files);
    const maximumFile = "x".repeat(MAXIMUM_GENERATED_FILE_BYTES);
    for (let index = 0; index < 7; index += 1) {
      exactTree.set(`treeLimit${index}.server.ts`, maximumFile);
    }
    exactTree.set("treeLimit7.server.ts", "");
    const treeBaseline = forge(exactTree);
    const finalFileBytes =
      MAXIMUM_GENERATED_TREE_BYTES -
      [...treeBaseline.files.values()].reduce(
        (total, text) => total + Buffer.byteLength(text, "utf-8"),
        0
      );
    expect(finalFileBytes).toBeGreaterThan(0);
    expect(finalFileBytes).toBeLessThanOrEqual(MAXIMUM_GENERATED_FILE_BYTES);
    exactTree.set("treeLimit7.server.ts", "x".repeat(finalFileBytes));
    const exactTreeProduct = forge(exactTree);
    expect(
      [...exactTreeProduct.files.values()].reduce(
        (total, text) => total + Buffer.byteLength(text, "utf-8"),
        0
      )
    ).toBe(MAXIMUM_GENERATED_TREE_BYTES);
    await expectStaleWithoutWrites(exactTreeProduct);

    const overlongTree = new Map(exactTreeProduct.files);
    const finalFile = overlongTree.get("treeLimit7.server.ts");
    if (finalFile === undefined) {
      throw new TypeError("Expected the final generated tree file.");
    }
    overlongTree.set("treeLimit7.server.ts", `${finalFile}x`);
    await expectRejectedWithoutWrites(
      forge(overlongTree),
      "Astilba Env generated output exceeds its tree limit."
    );
  });
});
