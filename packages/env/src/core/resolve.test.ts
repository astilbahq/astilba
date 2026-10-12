import { describe, expect, it } from "vitest";

import {
  compileContract,
  findProjection,
  opaqueCodec,
  originCodec,
  resolveEntry,
  resolveLifecycle,
  resolvePublicLifecycle,
  stringCodec,
} from "./index.ts";
import type {
  CompiledProjection,
  ContractDefinition,
  ResolutionBinding,
} from "./index.ts";

const bindings: readonly ResolutionBinding[] = [
  { entry: "apiOrigin", source: "API_ORIGIN" },
  { entry: "supportOrigin", source: "SUPPORT_ORIGIN" },
  { entry: "internalToken", source: "INTERNAL_TOKEN" },
  { entry: "tenantTheme", source: "TENANT_THEME" },
];

async function projection(): Promise<CompiledProjection> {
  const definition: ContractDefinition = {
    consumers: [
      {
        entries: [
          ["com.example.shared", "apiOrigin"],
          ["com.example.shared", "supportOrigin"],
          ["com.example.private", "internalToken"],
          ["com.example.tenant", "tenantTheme"],
        ],
        id: "worker",
        kind: "server",
      },
    ],
    entries: [
      {
        codec: originCodec(),
        fragment: "com.example.shared",
        id: "apiOrigin",
        lifecycle: "deployment",
        required: true,
        visibility: "public",
      },
      {
        codec: originCodec(),
        fragment: "com.example.shared",
        id: "supportOrigin",
        lifecycle: "deployment",
        required: false,
        visibility: "public",
      },
      {
        codec: stringCodec({ minCodePoints: 16, maxCodePoints: 128 }),
        fragment: "com.example.private",
        id: "internalToken",
        lifecycle: "deployment",
        required: true,
        visibility: "private",
      },
      {
        codec: stringCodec({ minCodePoints: 1, maxCodePoints: 20 }),
        fragment: "com.example.tenant",
        id: "tenantTheme",
        lifecycle: "request",
        required: true,
        visibility: "public",
      },
    ],
    id: "com.example.contract",
  };
  const compiled = await compileContract(definition);
  const worker = findProjection(compiled, "worker");
  if (worker === undefined) {
    throw new Error("Fixture projection is missing.");
  }
  return worker;
}

describe("explicit-record resolution", () => {
  it("resolves one lifecycle into a frozen null-prototype record", async () => {
    const worker = await projection();
    const source = {
      API_ORIGIN: "https://api.example.com/",
      INTERNAL_TOKEN: "sensitive-test-value-0001",
    };
    const result = resolveLifecycle(
      worker.manifest,
      "deployment",
      bindings,
      source
    );

    expect(result).toStrictEqual({
      ok: true,
      // oxlint-disable-next-line typescript/no-unsafe-assignment -- This expected value deliberately has the null prototype required by the resolver's JSON contract.
      value: Object.assign(Object.create(null), {
        apiOrigin: "https://api.example.com",
        internalToken: "sensitive-test-value-0001",
      }),
    });
    if (!result.ok) {
      throw new Error("Expected the valid configuration to resolve.");
    }
    expect(Object.isFrozen(result.value)).toBe(true);
    expect(Object.getPrototypeOf(result.value)).toBeNull();
    expect(source).toStrictEqual({
      API_ORIGIN: "https://api.example.com/",
      INTERNAL_TOKEN: "sensitive-test-value-0001",
    });
  });

  it("distinguishes optional missing from optional invalid", async () => {
    const worker = await projection();
    const missing = resolveLifecycle(worker.manifest, "deployment", bindings, {
      API_ORIGIN: "https://api.example.com",
      INTERNAL_TOKEN: "sensitive-test-value-0001",
    });
    expect(missing.ok && "supportOrigin" in missing.value).toBe(false);

    const invalid = resolveLifecycle(worker.manifest, "deployment", bindings, {
      API_ORIGIN: "https://api.example.com",
      INTERNAL_TOKEN: "sensitive-test-value-0001",
      SUPPORT_ORIGIN: "",
    });
    expect(invalid).toStrictEqual({
      diagnostic: {
        code: "ENV_INVALID_VALUE",
        codec: "astilba.env.origin-ascii/v1",
        consumer: "worker",
        entry: "supportOrigin",
        lifecycle: "deployment",
      },
      ok: false,
    });
  });

  it("returns a stable lifecycle error before reading a value", async () => {
    const worker = await projection();
    let reads = 0;
    const source: Record<string, unknown> = {};
    Object.setPrototypeOf(source, null);
    Object.defineProperty(source, "API_ORIGIN", {
      enumerable: true,
      get() {
        reads += 1;
        throw new Error("sensitive-test-value");
      },
    });

    const result = resolveEntry(
      worker.manifest,
      "apiOrigin",
      "build",
      bindings,
      source
    );
    expect(result).toStrictEqual({
      diagnostic: {
        code: "ENV_LIFECYCLE_ACCESS",
        codec: "astilba.env.origin-ascii/v1",
        consumer: "worker",
        entry: "apiOrigin",
        lifecycle: "deployment",
      },
      ok: false,
    });
    expect(reads).toBe(0);
    expect(JSON.stringify(result)).not.toContain("sensitive-test-value");
  });

  it("rejects selected accessors and inherited source values", async () => {
    const worker = await projection();
    let reads = 0;
    const accessor: Record<string, unknown> = {};
    Object.setPrototypeOf(accessor, null);
    Object.defineProperty(accessor, "API_ORIGIN", {
      enumerable: true,
      get() {
        reads += 1;
        return "https://api.example.com";
      },
    });
    Object.defineProperty(accessor, "INTERNAL_TOKEN", {
      enumerable: true,
      value: "sensitive-test-value-0001",
    });
    const accessorResult = resolveLifecycle(
      worker.manifest,
      "deployment",
      bindings,
      accessor
    );
    expect(accessorResult).toStrictEqual({
      diagnostic: {
        code: "ENV_SOURCE_INVALID",
        consumer: "worker",
      },
      ok: false,
    });
    expect(reads).toBe(0);

    // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- The inherited source value is deliberately forged to prove own-property refusal.
    const inherited = Object.create({
      API_ORIGIN: "https://api.example.com",
    }) as Record<string, unknown>;
    inherited.INTERNAL_TOKEN = "sensitive-test-value-0001";
    expect(
      resolveLifecycle(worker.manifest, "deployment", bindings, inherited)
    ).toStrictEqual({
      diagnostic: {
        code: "ENV_SOURCE_INVALID",
        consumer: "worker",
      },
      ok: false,
    });
  });

  it("does not inspect unrelated accessors", async () => {
    const worker = await projection();
    let reads = 0;
    const source = {
      API_ORIGIN: "https://api.example.com",
      INTERNAL_TOKEN: "sensitive-test-value-0001",
      get UNSELECTED_PRIVATE() {
        reads += 1;
        throw new Error("sensitive-test-value");
      },
    };

    expect(
      resolveLifecycle(worker.manifest, "deployment", bindings, source).ok
    ).toBe(true);
    expect(reads).toBe(0);
  });

  it("rejects raw-name case collisions before source access", async () => {
    const worker = await projection();
    // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- The duplicate folded raw name deliberately violates the binding contract.
    const collidingBindings = [
      ...bindings,
      { entry: "supportOrigin", source: "api_origin" },
    ] as unknown as readonly ResolutionBinding[];
    const result = resolveLifecycle(
      worker.manifest,
      "deployment",
      collidingBindings,
      Object.create(null)
    );

    expect(result).toStrictEqual({
      diagnostic: {
        code: "ENV_BINDING_INVALID",
        consumer: "worker",
      },
      ok: false,
    });
  });

  it("fails closed on invalid lifecycle and public projection misuse", async () => {
    const worker = await projection();
    expect(
      resolveLifecycle(
        worker.manifest,
        // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- This invalid lifecycle is intentionally forged to prove a fail-closed response.
        "invalid" as "build",
        bindings,
        Object.create(null)
      )
    ).toStrictEqual({
      diagnostic: {
        code: "ENV_LIFECYCLE_ACCESS",
        consumer: "worker",
      },
      ok: false,
    });
    expect(
      resolvePublicLifecycle(
        // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- A server manifest is deliberately passed to the public resolver to prove visibility refusal.
        worker.manifest as never,
        "deployment",
        bindings,
        Object.create(null)
      )
    ).toStrictEqual({
      diagnostic: {
        code: "ENV_VISIBILITY_ACCESS",
        consumer: "worker",
      },
      ok: false,
    });
  });

  it("records opaque semantics but never executes them", async () => {
    const definition: ContractDefinition = {
      consumers: [
        {
          entries: [["com.example.private", "custom"]],
          id: "service",
          kind: "server",
        },
      ],
      entries: [
        {
          codec: opaqueCodec({
            input: { kind: "string" },
            output: { kind: "string" },
            revision: "1",
            semantics: "acme/custom@1",
          }),
          fragment: "com.example.private",
          id: "custom",
          lifecycle: "deployment",
          required: true,
          visibility: "private",
        },
      ],
      id: "com.example.contract",
    };
    const compiled = await compileContract(definition);
    const service = findProjection(compiled, "service");
    if (service === undefined) {
      throw new Error("Fixture projection is missing.");
    }

    let sourceReads = 0;
    const source: Record<string, unknown> = {};
    Object.setPrototypeOf(source, null);
    Object.defineProperty(source, "CUSTOM", {
      enumerable: true,
      get() {
        sourceReads += 1;
        return "value";
      },
    });

    expect(
      resolveLifecycle(
        service.manifest,
        "deployment",
        [{ entry: "custom", source: "CUSTOM" }],
        source
      )
    ).toStrictEqual({
      diagnostic: {
        code: "ENV_OPAQUE_UNSUPPORTED",
        codec: "astilba.env.opaque/v1",
        consumer: "service",
        entry: "custom",
        lifecycle: "deployment",
      },
      ok: false,
    });
    expect(sourceReads).toBe(0);
  });
});
