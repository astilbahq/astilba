import { describe, expect, it } from "vitest";

import {
  checkProcessTarget,
  EnvironmentConfigurationError,
  loadProcessTarget,
} from "./index.ts";
import type { ProcessTargetDefinition } from "./index.ts";
import type {
  AggregateFailure,
  AggregateResult,
  CoreDiagnostic,
  Success,
} from "./model.ts";

function assertSuccess<TValue>(
  result: AggregateResult<TValue>
): asserts result is Success<TValue> {
  if (!result.ok) {
    throw new Error("Expected a successful aggregate result.");
  }
}

function assertFailure<TValue>(
  result: AggregateResult<TValue>
): asserts result is AggregateFailure {
  if (result.ok) {
    throw new Error("Expected a failed aggregate result.");
  }
}

function assertEnvironmentConfigurationError(
  value: unknown
): asserts value is EnvironmentConfigurationError {
  if (!(value instanceof EnvironmentConfigurationError)) {
    throw new Error("Expected EnvironmentConfigurationError.");
  }
}

function assertRuleDiagnostic(
  value: CoreDiagnostic
): asserts value is Extract<
  CoreDiagnostic,
  Readonly<{ code: "ENV_RULE_VIOLATION" }>
> {
  if (value.code !== "ENV_RULE_VIOLATION") {
    throw new Error("Expected ENV_RULE_VIOLATION.");
  }
}

const asProcessTargetDefinition = (value: unknown): ProcessTargetDefinition =>
  // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- Invalid boundary fixtures intentionally bypass the generated target type to exercise runtime refusal.
  value as ProcessTargetDefinition;

const nullRecord = <TValue extends object>(value: TValue): TValue => {
  Object.setPrototypeOf(value, null);
  return value;
};

const baseProjection = {
  canonicalisation: "astilba.jcs/v1",
  codecAbi: "astilba.env.codec/v1",
  consumer: "server",
  contract: "example.test",
  entries: [
    {
      codec: {
        abi: "astilba.env.boolean-exact/v1",
        blank: "missing",
        falseInput: "false",
        kind: "boolean",
        trueInput: "true",
      },
      identity: ["example.test", "enabled"],
      lifecycle: "deployment",
      name: "enabled",
      required: true,
      visibility: "public",
    },
    {
      codec: {
        abi: "astilba.env.text/v1",
        blank: "missing",
        kind: "text",
        maxCodePoints: 128,
        minCodePoints: 1,
        normalise: "preserve",
      },
      identity: ["example.test", "secret"],
      lifecycle: "deployment",
      name: "secret",
      required: true,
      visibility: "private",
    },
  ],
  format: "astilba.env.projection",
  formatVersion: 1,
  kind: "server",
  projectionAbi: "astilba.env.projection/v1",
} as const;

const target: ProcessTargetDefinition = {
  bindings: [
    { entry: "enabled", source: "ENABLED" },
    { entry: "secret", source: "SECRET" },
  ],
  generated: "astilba.env.generated-module/v1",
  lifecycle: "deployment",
  projection: baseProjection,
};

interface WranglerGeneratedEnvFixture {
  readonly ENABLED: string;
  readonly SECRET: string;
  readonly UNRELATED_KV: object;
}

describe("generated process runtime", () => {
  it("accepts a generated binding interface without an index signature", () => {
    const source: WranglerGeneratedEnvFixture = {
      ENABLED: "true",
      SECRET: "exact value",
      UNRELATED_KV: Object.freeze({}),
    };

    expect(
      loadProcessTarget<{
        readonly enabled: boolean;
        readonly secret: string;
      }>(target, source)
    ).toStrictEqual(
      nullRecord({
        enabled: true,
        secret: "exact value",
      })
    );
  });

  it("returns an exact owned result without observing unrelated input", () => {
    let unrelatedReads = 0;
    const result = checkProcessTarget<{
      readonly enabled: boolean;
      readonly secret: string;
    }>(target, {
      ENABLED: "true",
      SECRET: "exact value",
      get UNRELATED() {
        unrelatedReads += 1;
        throw new Error("unrelated input was read");
      },
    });

    expect(result).toStrictEqual({
      ok: true,
      value: nullRecord({
        enabled: true,
        secret: "exact value",
      }),
    });
    expect(unrelatedReads).toBe(0);
    expect(Object.getPrototypeOf(result)).toBe(Object.prototype);
    expect(Reflect.ownKeys(result)).toStrictEqual(["ok", "value"]);
    expect(Object.getOwnPropertyDescriptor(result, "ok")).toStrictEqual({
      configurable: false,
      enumerable: true,
      value: true,
      writable: false,
    });
    expect(Object.isFrozen(result)).toBe(true);
    assertSuccess(result);
    expect(Object.getPrototypeOf(result.value)).toBeNull();
    expect(Object.isFrozen(result.value)).toBe(true);
  });

  it("refuses a recognised newer generated module before source access", () => {
    let sourceReads = 0;
    const source = new Proxy(
      {},
      {
        getOwnPropertyDescriptor() {
          sourceReads += 1;
        },
      }
    );
    const result = checkProcessTarget(
      asProcessTargetDefinition({
        ...target,
        generated: "astilba.env.generated-module/v2",
      }),
      source
    );

    expect(result).toStrictEqual({
      diagnostics: [{ code: "ENV_FORMAT_UNSUPPORTED" }],
      ok: false,
    });
    expect(sourceReads).toBe(0);
  });

  it("rejects a malformed current target before source access", () => {
    let sourceReads = 0;
    const source = new Proxy(
      {},
      {
        getOwnPropertyDescriptor() {
          sourceReads += 1;
        },
      }
    );
    const result = checkProcessTarget(
      {
        ...target,
        bindings: [{ entry: "enabled", source: "ENABLED" }],
      },
      source
    );

    expect(result).toStrictEqual({
      diagnostics: [{ code: "ENV_CONTRACT_INVALID" }],
      ok: false,
    });
    expect(sourceReads).toBe(0);
  });

  it.each([
    [
      "projection",
      {
        ...target,
        projection: {
          ...baseProjection,
          formatVersion: 3,
        },
      },
    ],
    [
      "codec",
      {
        ...target,
        projection: {
          ...baseProjection,
          entries: [
            {
              ...baseProjection.entries[0],
              codec: {
                ...baseProjection.entries[0].codec,
                abi: "astilba.env.boolean-exact/v2",
              },
            },
            baseProjection.entries[1],
          ],
        },
      },
    ],
  ])(
    "refuses a recognised newer %s format before source access",
    (_name, definition) => {
      let sourceReads = 0;
      const source = new Proxy(
        {},
        {
          getOwnPropertyDescriptor() {
            sourceReads += 1;
          },
        }
      );

      expect(
        checkProcessTarget(asProcessTargetDefinition(definition), source)
      ).toStrictEqual({
        diagnostics: [{ code: "ENV_FORMAT_UNSUPPORTED" }],
        ok: false,
      });
      expect(sourceReads).toBe(0);
    }
  );

  it("observes requested source names in sorted order and stops safely", () => {
    const observed: PropertyKey[] = [];
    const source = new Proxy(
      {},
      {
        getOwnPropertyDescriptor(_object, name) {
          observed.push(name);
          if (name === "ALPHA") {
            return {
              configurable: true,
              enumerable: true,
              get: () => "not invoked",
            };
          }
          return {
            configurable: true,
            enumerable: true,
            value: "true",
            writable: true,
          };
        },
      }
    );
    const reordered: ProcessTargetDefinition = {
      ...target,
      bindings: [
        { entry: "enabled", source: "ZED" },
        { entry: "secret", source: "ALPHA" },
      ],
    };

    expect(checkProcessTarget(reordered, source)).toStrictEqual({
      diagnostics: [
        {
          code: "ENV_SOURCE_INVALID",
          consumer: "server",
        },
      ],
      ok: false,
    });
    expect(observed).toStrictEqual(["ALPHA"]);
  });

  it("creates guarded value-free configuration errors", () => {
    const checked = checkProcessTarget(target, {
      ENABLED: "unknown",
    });
    expect(checked.ok).toBe(false);

    let caught: unknown;
    try {
      loadProcessTarget(target, {
        ENABLED: "unknown",
      });
    } catch (error) {
      caught = error;
    }
    expect(caught).toBeInstanceOf(EnvironmentConfigurationError);
    expect(caught).toMatchObject({
      message: "Astilba Env configuration is invalid.",
      name: "EnvironmentConfigurationError",
    });
    assertEnvironmentConfigurationError(caught);
    const error = caught;
    expect(Reflect.ownKeys(error)).not.toContain("cause");
    for (const name of ["name", "message", "diagnostics"]) {
      expect(Object.getOwnPropertyDescriptor(error, name)).toMatchObject({
        configurable: false,
        enumerable: false,
        writable: false,
      });
    }
    expect(Object.isFrozen(error.diagnostics)).toBe(true);
    expect(Object.isFrozen(error.diagnostics[0])).toBe(true);
    expect(JSON.stringify(error)).toBe("{}");
    assertFailure(checked);
    expect(error.diagnostics).not.toBe(checked.diagnostics);
    expect(error.diagnostics[0]).not.toBe(checked.diagnostics[0]);

    expect(() => {
      Reflect.construct(EnvironmentConfigurationError, []);
    }).toThrow(
      new TypeError(
        "EnvironmentConfigurationError cannot be constructed directly."
      )
    );
  });

  it("owns exact aggregate and rule diagnostic DTOs", () => {
    const ruleTarget = {
      bindings: [
        { entry: "alpha", source: "ALPHA" },
        { entry: "beta", source: "BETA" },
      ],
      generated: "astilba.env.generated-module/v1",
      lifecycle: "deployment",
      projection: {
        canonicalisation: "astilba.jcs/v1",
        codecAbi: "astilba.env.codec/v1",
        consumer: "server",
        contract: "example.test",
        entries: [
          {
            codec: {
              abi: "astilba.env.boolean-exact/v1",
              blank: "missing",
              falseInput: "false",
              kind: "boolean",
              trueInput: "true",
            },
            identity: ["example.test", "alpha"],
            lifecycle: "deployment",
            name: "alpha",
            required: false,
            visibility: "public",
          },
          {
            codec: {
              abi: "astilba.env.boolean-exact/v1",
              blank: "missing",
              falseInput: "false",
              kind: "boolean",
              trueInput: "true",
            },
            identity: ["example.test", "beta"],
            lifecycle: "deployment",
            name: "beta",
            required: false,
            visibility: "public",
          },
        ],
        format: "astilba.env.projection",
        formatVersion: 2,
        kind: "server",
        projectionAbi: "astilba.env.projection/v1",
        rules: [
          {
            abi: "astilba.env.present-together/v1",
            entries: [
              ["example.test", "alpha"],
              ["example.test", "beta"],
            ],
            id: "pair",
            kind: "present-together",
          },
        ],
      },
    } as const satisfies ProcessTargetDefinition;
    const first = checkProcessTarget(ruleTarget, {
      ALPHA: "true",
    });
    const second = checkProcessTarget(ruleTarget, {
      ALPHA: "true",
    });

    expect(first).toStrictEqual({
      diagnostics: [
        {
          code: "ENV_RULE_VIOLATION",
          consumer: "server",
          entries: ["alpha", "beta"],
          lifecycle: "deployment",
          rule: "pair",
        },
      ],
      ok: false,
    });
    assertFailure(first);
    assertFailure(second);
    expect(Object.getPrototypeOf(first)).toBe(Object.prototype);
    expect(Reflect.ownKeys(first)).toStrictEqual(["diagnostics", "ok"]);
    expect(Object.getOwnPropertyDescriptor(first, "diagnostics")).toStrictEqual(
      {
        configurable: false,
        enumerable: true,
        value: first.diagnostics,
        writable: false,
      }
    );
    expect(Object.isFrozen(first)).toBe(true);
    const item = first.diagnostics[0];
    const secondItem = second.diagnostics[0];
    assertRuleDiagnostic(item);
    assertRuleDiagnostic(secondItem);
    expect(Reflect.ownKeys(item)).toStrictEqual([
      "code",
      "consumer",
      "entries",
      "lifecycle",
      "rule",
    ]);
    expect(Object.isFrozen(first.diagnostics)).toBe(true);
    expect(Object.isFrozen(item)).toBe(true);
    expect(Object.isFrozen(item.entries)).toBe(true);
    expect(item.entries).not.toBe(secondItem.entries);
    expect(first.diagnostics).not.toBe(second.diagnostics);
    expect(item).not.toBe(secondItem);
  });

  it("accepts a generated public build projection", () => {
    const publicTarget = {
      bindings: [{ entry: "enabled", source: "ENABLED" }],
      generated: "astilba.env.generated-module/v1",
      lifecycle: "build",
      projection: {
        canonicalisation: "astilba.jcs/v1",
        codecAbi: "astilba.env.codec/v1",
        consumer: "browser",
        contract: "example.test",
        entries: [
          {
            codec: {
              abi: "astilba.env.boolean-exact/v1",
              blank: "missing",
              falseInput: "false",
              kind: "boolean",
              trueInput: "true",
            },
            identity: ["example.test", "enabled"],
            lifecycle: "build",
            name: "enabled",
            required: true,
          },
        ],
        format: "astilba.env.projection",
        formatVersion: 1,
        kind: "public",
        projectionAbi: "astilba.env.projection/v1",
      },
    } as const satisfies ProcessTargetDefinition;

    expect(
      loadProcessTarget<{ readonly enabled: boolean }>(publicTarget, {
        ENABLED: "false",
      })
    ).toStrictEqual(nullRecord({ enabled: false }));
  });

  it("refuses selected opaque entries synchronously before source access", () => {
    let sourceReads = 0;
    const opaqueTarget = {
      bindings: [{ entry: "token", source: "TOKEN" }],
      generated: "astilba.env.generated-module/v1",
      lifecycle: "deployment",
      projection: {
        canonicalisation: "astilba.jcs/v1",
        codecAbi: "astilba.env.codec/v1",
        consumer: "server",
        contract: "example.test",
        entries: [
          {
            codec: {
              abi: "astilba.env.opaque/v1",
              input: { kind: "string" },
              kind: "opaque",
              output: { kind: "string" },
              revision: "1",
              semantics: "token/v1",
            },
            identity: ["example.test", "token"],
            lifecycle: "deployment",
            name: "token",
            required: false,
            visibility: "private",
          },
        ],
        format: "astilba.env.projection",
        formatVersion: 1,
        kind: "server",
        projectionAbi: "astilba.env.projection/v1",
      },
    } as const satisfies ProcessTargetDefinition;
    const source = new Proxy(
      {},
      {
        getOwnPropertyDescriptor() {
          sourceReads += 1;
        },
      }
    );

    expect(checkProcessTarget(opaqueTarget, source)).toStrictEqual({
      diagnostics: [
        {
          code: "ENV_OPAQUE_UNSUPPORTED",
          codec: "astilba.env.opaque/v1",
          consumer: "server",
          entry: "token",
          lifecycle: "deployment",
        },
      ],
      ok: false,
    });
    expect(sourceReads).toBe(0);
  });
});
