import { describe, expect, expectTypeOf, it } from "vitest";

import {
  booleanCodec,
  canonicalJson,
  compareCodecCompatibility,
  ContractDefinitionError,
  jsonCodec,
  normalizePublicCodecDescriptor,
  normalizePortableCodecDescriptor,
  resolvePortableValue,
  safeIntegerCodec,
  stringCodec,
  stringListCodec,
  validatePortableValue,
} from "./index.ts";
import type { PublicCodecDescriptor } from "./index.ts";

const exactBoolean = () =>
  booleanCodec({
    blank: "missing",
    falseInput: "false",
    trueInput: "true",
  });

const port = () =>
  safeIntegerCodec({
    blank: "missing",
    maximum: 65_535,
    minimum: 1,
  });

const features = (emptyItems: "drop" | "invalid" = "drop") =>
  stringListCodec({
    emptyItems,
    maximumItemCodePoints: 16,
    maximumItems: 4,
    minimumItemCodePoints: 1,
    minimumItems: 0,
  });

const configurationShape = {
  kind: "object",
  properties: [
    {
      name: "enabled",
      required: true,
      shape: { kind: "boolean" },
    },
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
      name: "marker",
      required: true,
      shape: { kind: "null" },
    },
    {
      name: "region",
      required: true,
      shape: { kind: "string" },
    },
    {
      name: "retries",
      required: false,
      shape: {
        kind: "safe-integer",
        maximum: 10,
        minimum: 0,
      },
    },
  ],
} as const;

const configuration = (blank: "invalid" | "missing" = "missing") =>
  jsonCodec({
    blank,
    shape: configurationShape,
  });

describe("first-party public codec descriptors", () => {
  it("normalizes frozen JSON-safe descriptors with complete raw semantics", () => {
    const descriptor = {
      abi: "astilba.env.boolean-exact/v1",
      blank: "missing",
      falseInput: "disabled",
      kind: "boolean",
      trueInput: "enabled",
    } as const;
    Object.setPrototypeOf(descriptor, null);
    const normalized = normalizePublicCodecDescriptor(descriptor);

    expect(normalized).toStrictEqual({
      abi: "astilba.env.boolean-exact/v1",
      blank: "missing",
      falseInput: "disabled",
      kind: "boolean",
      trueInput: "enabled",
    });
    expect(Object.getPrototypeOf(normalized)).toBe(Object.prototype);
    expect(Object.isFrozen(normalized)).toBe(true);
    expect(canonicalJson(descriptor)).toBe(
      '{"abi":"astilba.env.boolean-exact/v1","blank":"missing","falseInput":"disabled","kind":"boolean","trueInput":"enabled"}'
    );
  });

  it("rejects ambiguous, non-canonical, or unbounded descriptors", () => {
    expect(() =>
      booleanCodec({
        blank: "invalid",
        falseInput: "same",
        trueInput: "same",
      })
    ).toThrow(ContractDefinitionError);
    expect(() =>
      booleanCodec({
        blank: "invalid",
        falseInput: "false",
        trueInput: "",
      })
    ).toThrow(ContractDefinitionError);
    expect(() =>
      safeIntegerCodec({
        blank: "invalid",
        maximum: 10,
        minimum: -0,
      })
    ).toThrow(ContractDefinitionError);
    expect(() =>
      stringListCodec({
        emptyItems: "drop",
        maximumItemCodePoints: 65,
        maximumItems: 1024,
        minimumItemCodePoints: 1,
        minimumItems: 0,
      })
    ).toThrow(ContractDefinitionError);
    expect(() =>
      stringListCodec({
        emptyItems: "drop",
        maximumItemCodePoints: 65_537,
        maximumItems: 0,
        minimumItemCodePoints: 1,
        minimumItems: 0,
      })
    ).toThrow(ContractDefinitionError);
    expect(() =>
      // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- This extra own field deliberately forges an invalid public descriptor.
      normalizePublicCodecDescriptor({
        ...exactBoolean(),
        extra: true,
      } as unknown as PublicCodecDescriptor)
    ).toThrow(ContractDefinitionError);
  });

  it("does not invoke an accessor while refusing an executable descriptor", () => {
    let reads = 0;
    const descriptor = {
      abi: "astilba.env.boolean-exact/v1",
      blank: "missing",
      falseInput: "false",
      get kind() {
        reads += 1;
        return "boolean";
      },
      trueInput: "true",
    };

    expect(() =>
      normalizePublicCodecDescriptor(
        // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- The accessor-bearing object is deliberately not a public codec descriptor.
        descriptor as unknown as PublicCodecDescriptor
      )
    ).toThrow(ContractDefinitionError);
    expect(reads).toBe(0);
  });

  it("compares every declared semantic field exactly", () => {
    const base = features();

    expect(compareCodecCompatibility(base, features())).toBe("EQUAL");
    for (const different of [
      stringListCodec({
        emptyItems: "invalid",
        maximumItemCodePoints: 16,
        maximumItems: 4,
        minimumItemCodePoints: 1,
        minimumItems: 0,
      }),
      stringListCodec({
        emptyItems: "drop",
        maximumItemCodePoints: 15,
        maximumItems: 4,
        minimumItemCodePoints: 1,
        minimumItems: 0,
      }),
      stringListCodec({
        emptyItems: "drop",
        maximumItemCodePoints: 16,
        maximumItems: 3,
        minimumItemCodePoints: 1,
        minimumItems: 0,
      }),
      stringListCodec({
        emptyItems: "drop",
        maximumItemCodePoints: 16,
        maximumItems: 4,
        minimumItemCodePoints: 2,
        minimumItems: 0,
      }),
      stringListCodec({
        emptyItems: "drop",
        maximumItemCodePoints: 16,
        maximumItems: 4,
        minimumItemCodePoints: 1,
        minimumItems: 1,
      }),
    ]) {
      expect(compareCodecCompatibility(base, different)).toBe("UNEQUAL");
    }

    const boolean = exactBoolean();
    for (const different of [
      booleanCodec({
        blank: "invalid",
        falseInput: "false",
        trueInput: "true",
      }),
      booleanCodec({
        blank: "missing",
        falseInput: "0",
        trueInput: "true",
      }),
      booleanCodec({
        blank: "missing",
        falseInput: "false",
        trueInput: "1",
      }),
    ]) {
      expect(compareCodecCompatibility(boolean, different)).toBe("UNEQUAL");
    }

    expect(compareCodecCompatibility(port(), port())).toBe("EQUAL");
    expect(
      compareCodecCompatibility(
        port(),
        safeIntegerCodec({
          blank: "invalid",
          maximum: 65_535,
          minimum: 1,
        })
      )
    ).toBe("UNEQUAL");
    expect(
      compareCodecCompatibility(
        port(),
        safeIntegerCodec({
          blank: "missing",
          maximum: 65_534,
          minimum: 1,
        })
      )
    ).toBe("UNEQUAL");
    expect(
      compareCodecCompatibility(
        port(),
        safeIntegerCodec({
          blank: "missing",
          maximum: 65_535,
          minimum: 2,
        })
      )
    ).toBe("UNEQUAL");

    expect(compareCodecCompatibility(configuration(), configuration())).toBe(
      "EQUAL"
    );
    expect(
      compareCodecCompatibility(configuration(), configuration("invalid"))
    ).toBe("UNEQUAL");
    expect(
      compareCodecCompatibility(
        configuration(),
        jsonCodec({
          blank: "missing",
          shape: {
            ...configurationShape,
            properties: configurationShape.properties.map((property) =>
              property.name === "retries"
                ? {
                    ...property,
                    shape: {
                      kind: "safe-integer" as const,
                      maximum: 11,
                      minimum: 0,
                    },
                  }
                : property
            ),
          },
        })
      )
    ).toBe("UNEQUAL");
  });
});

describe("raw public resolution and typed output validation", () => {
  it("rejects raw strings before code-point work can exceed declared bounds", () => {
    const descriptor = stringCodec({
      maxCodePoints: 1,
      minCodePoints: 1,
    });

    expect(resolvePortableValue(descriptor, "x".repeat(100_000))).toStrictEqual(
      {
        code: "ENV_INVALID_VALUE",
        ok: false,
      }
    );
    expect(validatePortableValue(descriptor, "😀")).toStrictEqual({
      ok: true,
      value: "😀",
    });
    expect(validatePortableValue(descriptor, "😀😀")).toStrictEqual({
      code: "ENV_INVALID_VALUE",
      ok: false,
    });
  });

  it("keeps exact boolean parsing separate from boolean output validation", () => {
    const descriptor = exactBoolean();

    expect(resolvePortableValue(descriptor, "true")).toStrictEqual({
      ok: true,
      present: true,
      value: true,
    });
    expect(resolvePortableValue(descriptor, "false")).toStrictEqual({
      ok: true,
      present: true,
      value: false,
    });
    expect(resolvePortableValue(descriptor, "")).toStrictEqual({
      ok: true,
      present: false,
    });
    expect(
      resolvePortableValue(
        booleanCodec({
          blank: "invalid",
          falseInput: "false",
          trueInput: "true",
        }),
        ""
      )
    ).toStrictEqual({
      code: "ENV_INVALID_VALUE",
      ok: false,
    });
    expect(resolvePortableValue(descriptor, undefined)).toStrictEqual({
      ok: true,
      present: false,
    });
    for (const input of ["TRUE", "1", " false", "false "]) {
      expect(resolvePortableValue(descriptor, input)).toStrictEqual({
        code: "ENV_INVALID_VALUE",
        ok: false,
      });
    }

    expect(validatePortableValue(descriptor, true)).toStrictEqual({
      ok: true,
      value: true,
    });
    expect(validatePortableValue(descriptor, "true")).toStrictEqual({
      code: "ENV_INVALID_VALUE",
      ok: false,
    });

    const result = resolvePortableValue(descriptor, "true");
    if (result.ok && result.present) {
      expectTypeOf(result.value).toEqualTypeOf<boolean>();
    }
  });

  it("parses only canonical safe-integer decimal and validates typed bounds", () => {
    const descriptor = port();

    for (const [input, output] of [
      ["1", 1],
      ["4100", 4100],
      ["65535", 65_535],
    ] as const) {
      expect(resolvePortableValue(descriptor, input)).toStrictEqual({
        ok: true,
        present: true,
        value: output,
      });
    }

    for (const input of [
      "0",
      "+1",
      "01",
      "-0",
      " 1",
      "1 ",
      "1.0",
      "1e3",
      "65536",
      "9007199254740992",
    ]) {
      expect(resolvePortableValue(descriptor, input)).toStrictEqual({
        code: "ENV_INVALID_VALUE",
        ok: false,
      });
    }

    expect(resolvePortableValue(descriptor, "")).toStrictEqual({
      ok: true,
      present: false,
    });
    expect(
      resolvePortableValue(
        safeIntegerCodec({
          blank: "invalid",
          maximum: 65_535,
          minimum: 1,
        }),
        ""
      )
    ).toStrictEqual({
      code: "ENV_INVALID_VALUE",
      ok: false,
    });
    expect(validatePortableValue(descriptor, 4100)).toStrictEqual({
      ok: true,
      value: 4100,
    });
    for (const input of ["4100", 0, 65_536, -0, 1.5]) {
      expect(validatePortableValue(descriptor, input)).toStrictEqual({
        code: "ENV_INVALID_VALUE",
        ok: false,
      });
    }

    const result = resolvePortableValue(descriptor, "4100");
    if (result.ok && result.present) {
      expectTypeOf(result.value).toEqualTypeOf<number>();
    }
  });

  it("admits both canonical safe-integer boundaries when declared", () => {
    const descriptor = safeIntegerCodec({
      blank: "invalid",
      maximum: Number.MAX_SAFE_INTEGER,
      minimum: Number.MIN_SAFE_INTEGER,
    });

    expect(resolvePortableValue(descriptor, "9007199254740991")).toStrictEqual({
      ok: true,
      present: true,
      value: Number.MAX_SAFE_INTEGER,
    });
    expect(resolvePortableValue(descriptor, "-9007199254740991")).toStrictEqual(
      {
        ok: true,
        present: true,
        value: Number.MIN_SAFE_INTEGER,
      }
    );
  });

  it("drops or rejects empty comma items exactly and freezes list output", () => {
    const dropped = resolvePortableValue(features("drop"), "alpha,beta,,gamma");
    expect(dropped).toStrictEqual({
      ok: true,
      present: true,
      value: ["alpha", "beta", "gamma"],
    });
    if (!dropped.ok || !dropped.present) {
      throw new TypeError("Expected a resolved list");
    }
    expect(Object.isFrozen(dropped.value)).toBe(true);
    expectTypeOf(dropped.value).toEqualTypeOf<readonly string[]>();

    expect(resolvePortableValue(features("drop"), "")).toStrictEqual({
      ok: true,
      present: true,
      value: [],
    });
    expect(resolvePortableValue(features("drop"), "alpha, beta")).toStrictEqual(
      {
        ok: true,
        present: true,
        value: ["alpha", " beta"],
      }
    );
    expect(
      resolvePortableValue(features("invalid"), "alpha,,gamma")
    ).toStrictEqual({
      code: "ENV_INVALID_VALUE",
      ok: false,
    });
    expect(resolvePortableValue(features(), undefined)).toStrictEqual({
      ok: true,
      present: false,
    });
    expect(
      resolvePortableValue(features("drop"), ",".repeat(1024))
    ).toStrictEqual({
      code: "ENV_INVALID_VALUE",
      ok: false,
    });
  });

  it("validates, copies, and freezes already-typed string lists", () => {
    const source = ["alpha", "beta"];
    const result = validatePortableValue(features(), source);

    expect(result).toStrictEqual({
      ok: true,
      value: ["alpha", "beta"],
    });
    if (!result.ok) {
      throw new TypeError("Expected a validated list");
    }
    expect(result.value).not.toBe(source);
    expect(Object.isFrozen(result.value)).toBe(true);
    source[0] = "changed";
    expect(result.value).toStrictEqual(["alpha", "beta"]);
    expect(validatePortableValue(features(), "alpha,beta")).toStrictEqual({
      code: "ENV_INVALID_VALUE",
      ok: false,
    });
  });

  it("fails closed on malformed typed list structures without invoking accessors", () => {
    const descriptor = features();
    const sparse: string[] = [];
    sparse.length = 1;
    const extra = ["alpha"] as string[] & { extra?: string };
    extra.extra = "value";
    const subclass = new (class extends Array<string> {})("alpha");
    let reads = 0;
    const accessor: string[] = [];
    Object.defineProperty(accessor, "0", {
      enumerable: true,
      get() {
        reads += 1;
        return "alpha";
      },
    });
    accessor.length = 1;
    const proxy = Proxy.revocable(["alpha"], {});
    proxy.revoke();

    for (const input of [
      sparse,
      extra,
      subclass,
      accessor,
      proxy.proxy,
      ["alpha", 1],
      ["alpha", "\uD800"],
      ["alpha", "this-item-is-too-long"],
      ["a", "b", "c", "d", "e"],
    ]) {
      expect(validatePortableValue(descriptor, input)).toStrictEqual({
        code: "ENV_INVALID_VALUE",
        ok: false,
      });
    }
    expect(reads).toBe(0);
  });

  it("parses, copies, freezes, and types an exact portable JSON object", () => {
    const result = resolvePortableValue(
      configuration(),
      '{"region":"eu-west","enabled":false,"features":["alpha","beta"],"marker":null,"retries":3}'
    );

    expect(result).toStrictEqual({
      ok: true,
      present: true,
      // oxlint-disable-next-line typescript/no-unsafe-assignment -- This expected value deliberately has the null prototype required by the portable JSON contract.
      value: Object.assign(Object.create(null), {
        enabled: false,
        features: ["alpha", "beta"],
        marker: null,
        region: "eu-west",
        retries: 3,
      }),
    });
    if (!result.ok || !result.present) {
      throw new TypeError("Expected resolved JSON.");
    }
    expectTypeOf(result.value).toEqualTypeOf<{
      readonly enabled: boolean;
      readonly features: readonly string[];
      readonly marker: null;
      readonly region: string;
      readonly retries?: number;
    }>();
    expect(Object.getPrototypeOf(result.value)).toBeNull();
    expect(Object.isFrozen(result.value)).toBe(true);
    expect(Object.isFrozen(result.value.features)).toBe(true);
  });

  it("keeps JSON blank and missing policy explicit", () => {
    expect(resolvePortableValue(configuration(), undefined)).toStrictEqual({
      ok: true,
      present: false,
    });
    expect(resolvePortableValue(configuration(), "")).toStrictEqual({
      ok: true,
      present: false,
    });
    expect(resolvePortableValue(configuration("invalid"), "")).toStrictEqual({
      code: "ENV_INVALID_VALUE",
      ok: false,
    });
  });

  it.each([
    ["malformed", "{"],
    [
      "duplicate top-level key",
      '{"enabled":true,"enabled":false,"features":[],"marker":null,"region":"eu"}',
    ],
    [
      "duplicate nested key",
      '{"enabled":true,"features":[],"marker":null,"region":"eu","retries":1,"nested":{"same":1,"same":2}}',
    ],
    [
      "unknown key",
      '{"enabled":true,"extra":false,"features":[],"marker":null,"region":"eu"}',
    ],
    ["missing key", '{"enabled":true,"features":[],"marker":null}'],
    [
      "decimal",
      '{"enabled":true,"features":[],"marker":null,"region":"eu","retries":1.5}',
    ],
    [
      "negative zero",
      '{"enabled":true,"features":[],"marker":null,"region":"eu","retries":-0}',
    ],
    [
      "unsafe integer",
      '{"enabled":true,"features":[],"marker":null,"region":"eu","retries":9007199254740992}',
    ],
    [
      "dangerous key",
      '{"enabled":true,"features":[],"marker":null,"region":"eu","__proto__":"no"}',
    ],
    [
      "lone surrogate",
      '{"enabled":true,"features":["\\ud800"],"marker":null,"region":"eu"}',
    ],
  ])("rejects raw JSON with a %s", (_case, input) => {
    expect(resolvePortableValue(configuration(), input)).toStrictEqual({
      code: "ENV_INVALID_VALUE",
      ok: false,
    });
  });

  it("copies typed JSON input and refuses sparse, cyclic, or accessor-bearing values", () => {
    const source = {
      enabled: true,
      features: ["alpha"],
      marker: null,
      region: "eu",
    };
    const resolved = validatePortableValue(configuration(), source);
    expect(resolved).toStrictEqual({
      ok: true,
      // oxlint-disable-next-line typescript/no-unsafe-assignment -- This expected value deliberately has the null prototype required by the portable JSON contract.
      value: Object.assign(Object.create(null), source),
    });
    if (!resolved.ok) {
      throw new TypeError("Expected validated JSON.");
    }
    expect(resolved.value).not.toBe(source);
    expect(resolved.value.features).not.toBe(source.features);
    source.features[0] = "changed";
    expect(resolved.value.features).toStrictEqual(["alpha"]);

    const sparse: string[] = [];
    sparse.length = 1;
    const cyclic: unknown[] = [];
    cyclic.push(cyclic);
    let reads = 0;
    const accessor = {
      enabled: true,
      features: [],
      marker: null,
      get region() {
        reads += 1;
        return "eu";
      },
    };

    for (const input of [
      { ...source, features: sparse },
      { ...source, features: cyclic },
      accessor,
    ]) {
      expect(validatePortableValue(configuration(), input)).toStrictEqual({
        code: "ENV_INVALID_VALUE",
        ok: false,
      });
    }
    expect(reads).toBe(0);
  });
});

const compileTimePortableNormalizerContract = (): void => {
  // @ts-expect-error Public source codecs use the complete public normalizer.
  void normalizePortableCodecDescriptor(exactBoolean());
};

void compileTimePortableNormalizerContract;
