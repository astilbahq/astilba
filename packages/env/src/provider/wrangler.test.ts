/* oxlint-disable typescript/no-unsafe-type-assertion -- Boundary fixtures deliberately tamper with erased provider inputs. */

import { describe, expect, it } from "vitest";

import {
  checkWranglerBindingConformance,
  inspectWranglerJsonc,
  parseSecretBindingInventory,
  ProviderMetadataError,
} from "./index.ts";

const booleanCodec = Object.freeze({
  abi: "astilba.env.boolean-exact/v1",
  blank: "missing",
  falseInput: "false",
  kind: "boolean",
  trueInput: "true",
});

const entry = (
  name: string,
  lifecycle: "build" | "deployment" | "request" = "deployment",
  visibility?: "private" | "public",
  required = true
) => ({
  codec: booleanCodec,
  identity: ["example.platform", name],
  lifecycle,
  name,
  required,
  ...(visibility === undefined ? {} : { visibility }),
});

const projection = (
  lifecycle: "build" | "deployment" | "request" = "deployment"
) => ({
  canonicalisation: "astilba.jcs/v1",
  codecAbi: "astilba.env.codec/v1",
  consumer: "web",
  contract: "example.platform",
  entries: [entry("database", lifecycle)],
  format: "astilba.env.projection",
  formatVersion: 1,
  kind: "public",
  projectionAbi: "astilba.env.projection/v1",
});

const serverProjection = (overrides: Record<string, unknown> = {}) => ({
  canonicalisation: "astilba.jcs/v1",
  codecAbi: "astilba.env.codec/v1",
  consumer: "server",
  contract: "example.platform",
  entries: [
    entry("database", "deployment", "private"),
    entry("token", "deployment", "private", false),
  ],
  format: "astilba.env.projection",
  formatVersion: 1,
  kind: "server",
  projectionAbi: "astilba.env.projection/v1",
  ...overrides,
});

const plan = (overrides: Record<string, unknown> = {}) => ({
  adapterAbi: "astilba.env.adapter.cloudflare-workers/v1",
  bindings: [
    {
      channel: "deployment",
      class: "non-confidential",
      entry: "database",
      kind: "plain_text",
      rawName: "DATABASE",
      ...overrides,
    },
  ],
  format: "astilba.env.binding-plan/v1",
  target: "web",
});

const input = (overrides: Record<string, unknown> = {}) => ({
  bindingPlan: plan(),
  projection: projection(),
  wranglerJsonc: '{"vars":{"DATABASE":"value"}}',
  ...overrides,
});

const code = (operation: () => unknown): string => {
  try {
    operation();
  } catch (error) {
    expect(error).toBeInstanceOf(ProviderMetadataError);
    return (error as ProviderMetadataError).code;
  }
  throw new Error("expected provider metadata failure");
};

const observedProxy = <Value extends object>(
  value: Value,
  ownKeyOrder: readonly (string | symbol)[]
): Readonly<{ events: string[]; proxy: Value }> => {
  const events: string[] = [];
  return {
    events,
    proxy: new Proxy(value, {
      get: () => {
        throw new Error("ordinary property read");
      },
      getOwnPropertyDescriptor: (target, property) => {
        events.push(`descriptor:${String(property)}`);
        return Reflect.getOwnPropertyDescriptor(target, property);
      },
      getPrototypeOf: (target) => {
        events.push("prototype");
        return Reflect.getPrototypeOf(target);
      },
      ownKeys: () => {
        events.push("ownKeys");
        return [...ownKeyOrder];
      },
    }),
  };
};

const bindingPlanError = (projectionInput: unknown): void => {
  expect(
    code(() =>
      checkWranglerBindingConformance(
        input({ projection: projectionInput }) as never
      )
    )
  ).toBe("BINDING_PLAN_INVALID");
};

describe("provider metadata boundary", () => {
  it("accepts a complete public projection", () => {
    expect(checkWranglerBindingConformance(input() as never)).toMatchObject({
      confidence: "PROVEN",
      grade: "checked-offline-configuration",
      pass: true,
    });
  });

  it("remaps malformed projection codec, identity, and contract to the stable plan error", () => {
    bindingPlanError({
      ...projection(),
      entries: [
        {
          ...entry("database"),
          codec: {
            ...booleanCodec,
            abi: "astilba.env.boolean-exact/v2",
          },
        },
      ],
    });
    bindingPlanError({
      ...projection(),
      entries: [{ ...entry("database"), identity: ["example.platform"] }],
    });
    bindingPlanError({ ...projection(), contract: "not-a-contract" });
  });

  it("remaps server visibility, ordering, duplicate, and v2 rule tampering", () => {
    expect(
      checkWranglerBindingConformance(
        input({ projection: serverProjection() }) as never
      ).pass
    ).toBe(true);
    expect(
      checkWranglerBindingConformance(
        input({
          projection: serverProjection({
            formatVersion: 2,
            rules: [
              {
                abi: "astilba.env.present-together/v1",
                entries: [
                  ["example.platform", "database"],
                  ["example.platform", "token"],
                ],
                id: "credentials",
                kind: "present-together",
              },
            ],
          }),
        }) as never
      ).pass
    ).toBe(true);
    bindingPlanError({
      ...serverProjection(),
      entries: [entry("database", "build", "private")],
    });
    bindingPlanError({
      ...serverProjection(),
      entries: [
        entry("token", "deployment", "private"),
        entry("database", "deployment", "private"),
      ],
    });
    bindingPlanError({
      ...serverProjection(),
      entries: [
        entry("database", "deployment", "private"),
        entry("database", "deployment", "private"),
      ],
    });
    bindingPlanError({ ...serverProjection(), formatVersion: 2, rules: [] });
    bindingPlanError({ ...projection(), formatVersion: 2, rules: [] });
  });

  it("fails an impossible, duplicate, or lifecycle-mismatched binding mapping before reporting", () => {
    expect(
      code(() =>
        checkWranglerBindingConformance(
          input({ bindingPlan: plan({ entry: "unmatched" }) }) as never
        )
      )
    ).toBe("BINDING_PLAN_INVALID");
    expect(
      code(() =>
        checkWranglerBindingConformance(
          input({
            bindingPlan: {
              ...plan(),
              bindings: [
                plan().bindings[0],
                { ...plan().bindings[0], rawName: "DATABASE_TWO" },
              ],
            },
          }) as never
        )
      )
    ).toBe("BINDING_PLAN_INVALID");
    expect(
      code(() =>
        checkWranglerBindingConformance(
          input({ bindingPlan: plan({ channel: "request" }) }) as never
        )
      )
    ).toBe("BINDING_PLAN_INVALID");
  });

  it("keeps build unknown kinds unknown while allowing build public_text without a provider binding", () => {
    const unknown = checkWranglerBindingConformance(
      input({
        bindingPlan: plan({ channel: "build", kind: "future_kind" }),
        projection: projection("build"),
        wranglerJsonc: "{}",
      }) as never
    );
    expect(unknown.confidence).toBe("UNKNOWN");
    expect(unknown.pass).toBe(false);
    expect(unknown.issues).toContainEqual({
      code: "UNKNOWN_PROVIDER_KIND",
      name: "DATABASE",
    });

    const known = checkWranglerBindingConformance(
      input({
        bindingPlan: plan({ channel: "build", kind: "public_text" }),
        projection: projection("build"),
        wranglerJsonc: "{}",
      }) as never
    );
    expect(known.confidence).toBe("PROVEN");
    expect(known.pass).toBe(true);
  });

  it("makes colliding Wrangler and secret observations ambiguous", () => {
    const result = checkWranglerBindingConformance(
      input({
        bindingPlan: plan({
          class: "confidential",
          kind: "secret_text",
          rawName: "TOKEN",
        }),
        secretInventory: {
          bindings: [{ kind: "secret_text", name: "TOKEN" }],
          format: "astilba.env.binding-inventory/v1",
          target: "web",
        },
        wranglerJsonc: '{"vars":{"TOKEN":"value"}}',
      }) as never
    );
    expect(result.bindings[0]).toMatchObject({
      observedKind: null,
      status: "KIND_MISMATCH",
    });
  });

  it("reports an absent secret inventory as offline-unverified without provider access", () => {
    const result = checkWranglerBindingConformance(
      input({
        bindingPlan: plan({
          class: "confidential",
          kind: "secret_text",
          rawName: "TOKEN",
        }),
        wranglerJsonc: "{}",
      }) as never
    );

    expect(result).toMatchObject({
      confidence: "PROVEN",
      grade: "UNVERIFIED",
      issues: [{ code: "SECRET_INVENTORY_UNVERIFIED", name: "TOKEN" }],
      liveVerified: false,
      pass: false,
    });
    expect(result.bindings).toContainEqual({
      class: "confidential",
      expectedKind: "secret_text",
      name: "TOKEN",
      observedKind: null,
      status: "UNVERIFIED",
    });
  });

  it("observes provider DTO containers through sorted descriptors without ordinary reads", () => {
    const originalPlan = plan();
    const originalBinding = originalPlan.bindings[0];
    if (originalBinding === undefined) {
      throw new Error("provider fixture must contain one binding");
    }
    const binding = observedProxy(originalBinding, [
      "rawName",
      "kind",
      "entry",
      "class",
      "channel",
    ]);
    const bindings = observedProxy([binding.proxy], ["length", "0"]);
    const bindingPlan = observedProxy(
      { ...originalPlan, bindings: bindings.proxy },
      ["target", "format", "bindings", "adapterAbi"]
    );
    const outer = observedProxy(input({ bindingPlan: bindingPlan.proxy }), [
      "wranglerJsonc",
      "projection",
      "bindingPlan",
    ]);

    expect(checkWranglerBindingConformance(outer.proxy as never).pass).toBe(
      true
    );
    expect(outer.events).toStrictEqual([
      "prototype",
      "ownKeys",
      "descriptor:bindingPlan",
      "descriptor:projection",
      "descriptor:wranglerJsonc",
    ]);
    expect(bindingPlan.events).toStrictEqual([
      "prototype",
      "ownKeys",
      "descriptor:adapterAbi",
      "descriptor:bindings",
      "descriptor:format",
      "descriptor:target",
    ]);
    expect(bindings.events).toStrictEqual([
      "prototype",
      "ownKeys",
      "descriptor:0",
      "descriptor:length",
    ]);
    expect(binding.events).toStrictEqual([
      "prototype",
      "ownKeys",
      "descriptor:channel",
      "descriptor:class",
      "descriptor:entry",
      "descriptor:kind",
      "descriptor:rawName",
    ]);
  });

  it("remaps provider errors forged by hostile boundary traps to the owning operation", () => {
    const forgedPlan = new Proxy(plan(), {
      ownKeys: () => {
        throw new ProviderMetadataError("BINDING_PLAN_UNSUPPORTED");
      },
    });
    expect(
      code(() =>
        checkWranglerBindingConformance(
          input({ bindingPlan: forgedPlan }) as never
        )
      )
    ).toBe("BINDING_PLAN_INVALID");

    const forgedInventory = new Proxy(
      {
        bindings: [],
        format: "astilba.env.binding-inventory/v1",
        target: "web",
      },
      {
        ownKeys: () => {
          throw new ProviderMetadataError("SECRET_INVENTORY_UNSUPPORTED");
        },
      }
    );
    expect(code(() => parseSecretBindingInventory(forgedInventory))).toBe(
      "SECRET_INVENTORY_INVALID"
    );
    expect(
      code(() =>
        checkWranglerBindingConformance(
          input({ secretInventory: forgedInventory }) as never
        )
      )
    ).toBe("SECRET_INVENTORY_INVALID");

    const forgedProjection = new Proxy(projection(), {
      ownKeys: () => {
        throw new ProviderMetadataError("SECRET_INVENTORY_INVALID");
      },
    });
    expect(
      code(() =>
        checkWranglerBindingConformance(
          input({ projection: forgedProjection }) as never
        )
      )
    ).toBe("BINDING_PLAN_INVALID");
  });

  it("refuses recognised newer provider formats before reading bindings", () => {
    const inaccessibleBindings = new Proxy([], {
      getPrototypeOf: () => {
        throw new Error("bindings must not be observed");
      },
    });

    expect(
      code(() =>
        checkWranglerBindingConformance(
          input({
            bindingPlan: {
              ...plan(),
              bindings: inaccessibleBindings,
              format: "astilba.env.binding-plan/v2",
            },
          }) as never
        )
      )
    ).toBe("BINDING_PLAN_UNSUPPORTED");
    expect(
      code(() =>
        checkWranglerBindingConformance(
          input({
            bindingPlan: {
              ...plan(),
              adapterAbi: "astilba.env.adapter.cloudflare-workers/v2",
              bindings: inaccessibleBindings,
            },
          }) as never
        )
      )
    ).toBe("BINDING_PLAN_UNSUPPORTED");
    expect(
      code(() =>
        parseSecretBindingInventory({
          bindings: inaccessibleBindings,
          format: "astilba.env.binding-inventory/v2",
          target: "web",
        })
      )
    ).toBe("SECRET_INVENTORY_UNSUPPORTED");
  });

  it("reports an outer non-string Wrangler JSONC value with its own code", () => {
    expect(
      code(() =>
        checkWranglerBindingConformance(input({ wranglerJsonc: 1 }) as never)
      )
    ).toBe("WRANGLER_JSONC_INVALID");
  });

  it("refuses named Wrangler environments even when root bindings match", () => {
    const configurations = [
      '{"vars":{"DATABASE":"value"},"env":{"production":{}}}',
      '{"vars":{"DATABASE":"value"},"env":{"production":{"vars":{"DATABASE":"changed"}}}}',
      '{"vars":{"DATABASE":"value"},"env":{"production":{"vars":{"DATABASE":"value"}}}}',
      '{"vars":{"DATABASE":"value"},"env":{}}',
    ];

    for (const wranglerJsonc of configurations) {
      expect(code(() => inspectWranglerJsonc(wranglerJsonc))).toBe(
        "WRANGLER_JSONC_INVALID"
      );
      expect(
        code(() =>
          checkWranglerBindingConformance(input({ wranglerJsonc }) as never)
        )
      ).toBe("WRANGLER_JSONC_INVALID");
    }
  });

  it("rejects JSONC, plan, and inventory limits with their public errors", () => {
    expect(code(() => inspectWranglerJsonc("\uFEFF{}"))).toBe(
      "WRANGLER_JSONC_INVALID"
    );
    const jsoncRows = (count: number) =>
      `{"vars":{${Array.from(
        { length: count },
        (_, index) => `"VALUE_${index}":"x"`
      ).join(",")}}}`;
    expect(inspectWranglerJsonc(jsoncRows(2048)).bindings).toHaveLength(2048);
    expect(code(() => inspectWranglerJsonc(jsoncRows(2049)))).toBe(
      "WRANGLER_JSONC_INVALID"
    );
    expect(
      code(() =>
        checkWranglerBindingConformance(
          input({
            bindingPlan: {
              ...plan(),
              bindings: Array.from({ length: 2049 }, () => plan().bindings[0]),
            },
          }) as never
        )
      )
    ).toBe("BINDING_PLAN_INVALID");
    const inventory = (count: number) => ({
      bindings: Array.from({ length: count }, (_, index) => ({
        kind: "secret_text",
        name: `TOKEN_${index}`,
      })),
      format: "astilba.env.binding-inventory/v1",
      target: "web",
    });
    expect(parseSecretBindingInventory(inventory(2048)).bindings).toHaveLength(
      2048
    );
    expect(code(() => parseSecretBindingInventory(inventory(2049)))).toBe(
      "SECRET_INVENTORY_INVALID"
    );
  });

  it("handles the maximal provider report within the serialised bound", () => {
    const index = (value: number) => String(value).padStart(4, "0");
    const entries = Array.from({ length: 2048 }, (_, value) =>
      entry(`entry${index(value)}`)
    );
    const result = checkWranglerBindingConformance(
      input({
        bindingPlan: {
          ...plan(),
          bindings: entries.map((item, value) => ({
            channel: "deployment",
            class: "non-confidential",
            entry: item.name,
            kind: "plain_text",
            rawName: `EXPECTED_${index(value)}`,
          })),
        },
        projection: { ...projection(), entries },
        secretInventory: {
          bindings: Array.from({ length: 2048 }, (_, value) => ({
            kind: "secret_text",
            name: `SECRET_${index(value)}`,
          })),
          format: "astilba.env.binding-inventory/v1",
          target: "web",
        },
        wranglerJsonc: JSON.stringify({
          vars: Object.fromEntries(
            Array.from({ length: 2048 }, (_, value) => [
              `OBSERVED_${index(value)}`,
              "x",
            ])
          ),
        }),
      }) as never
    );

    expect(result.bindings).toHaveLength(2048);
    expect(result.grade).toBe("synthetic-declared-inventory");
    expect(result.issues).toHaveLength(6144);
    expect(result.pass).toBe(false);
    expect(
      new TextEncoder().encode(JSON.stringify(result)).byteLength
    ).toBeLessThanOrEqual(8_388_608);
  });

  it("enforces JSONC depth and cumulative-item boundaries exactly", () => {
    const nested = (depth: number) =>
      `${'{"nested":'.repeat(depth)}null${"}".repeat(depth)}`;
    expect(inspectWranglerJsonc(nested(64)).bindings).toStrictEqual([]);
    expect(code(() => inspectWranglerJsonc(nested(65)))).toBe(
      "WRANGLER_JSONC_INVALID"
    );
    const items = (count: number) =>
      `{"payload":{${Array.from(
        { length: count },
        (_, index) => `"item${index}":0`
      ).join(",")}}}`;
    expect(inspectWranglerJsonc(items(65_535)).bindings).toStrictEqual([]);
    expect(code(() => inspectWranglerJsonc(items(65_536)))).toBe(
      "WRANGLER_JSONC_INVALID"
    );
  });

  it("contains hostile observation failures behind stable provider errors", () => {
    const hostile = new Proxy(
      {},
      {
        get: () => {
          throw new Error("must not leak");
        },
        getOwnPropertyDescriptor: () => ({
          configurable: true,
          enumerable: true,
          value: "x",
          writable: true,
        }),
        getPrototypeOf: () => Object.prototype,
        ownKeys: () => ["bindingPlan", "projection", "wranglerJsonc"],
      }
    );
    expect(code(() => checkWranglerBindingConformance(hostile as never))).toBe(
      "BINDING_PLAN_INVALID"
    );
    const accessor = input();
    Object.defineProperty(accessor, "bindingPlan", {
      enumerable: true,
      get: () => {
        throw new Error("must not leak");
      },
    });
    expect(code(() => checkWranglerBindingConformance(accessor as never))).toBe(
      "BINDING_PLAN_INVALID"
    );
    expect(
      code(() =>
        parseSecretBindingInventory(
          new Proxy(
            {},
            {
              getPrototypeOf: () => {
                throw new Error("must not leak");
              },
            }
          )
        )
      )
    ).toBe("SECRET_INVENTORY_INVALID");
  });
});
