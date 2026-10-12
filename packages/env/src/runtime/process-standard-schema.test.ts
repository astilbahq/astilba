import { setImmediate as setImmediatePromise } from "node:timers/promises";

import { describe, expect, it } from "vitest";

import { checkProcessTargetWithSchemas } from "./index.ts";
import type {
  ProcessTargetDefinition,
  ProcessTargetSchemas,
  StandardSchemaV1,
} from "./index.ts";
import type { AggregateResult, Success } from "./model.ts";

function assertSuccess<TValue>(
  result: AggregateResult<TValue>
): asserts result is Success<TValue> {
  if (!result.ok) {
    throw new Error("Expected a successful aggregate result.");
  }
}

const asProcessTargetSchemas = (value: unknown): ProcessTargetSchemas =>
  // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- Invalid schema-map fixtures intentionally bypass the generated map type to exercise runtime refusal.
  value as ProcessTargetSchemas;

const nullRecord = <TValue extends object>(value: TValue): TValue => {
  Object.setPrototypeOf(value, null);
  return value;
};

const opaqueTarget = (
  options: {
    readonly inputOptional?: boolean;
    readonly required?: boolean;
  } = {}
): ProcessTargetDefinition => ({
  bindings: [{ entry: "setting", source: "SETTING" }],
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
          input:
            options.inputOptional === true
              ? {
                  kind: "optional",
                  value: { kind: "string" },
                }
              : { kind: "string" },
          kind: "opaque",
          output: {
            kind: "object",
            properties: [
              {
                name: "region",
                required: true,
                shape: { kind: "string" },
              },
            ],
          },
          revision: "1",
          semantics: "setting/v1",
        },
        identity: ["example.test", "setting"],
        lifecycle: "deployment",
        name: "setting",
        required: options.required ?? false,
        visibility: "private",
      },
    ],
    format: "astilba.env.projection",
    formatVersion: 1,
    kind: "server",
    projectionAbi: "astilba.env.projection/v1",
  },
});

const schema = (
  validate: StandardSchemaV1["~standard"]["validate"]
): StandardSchemaV1 => ({
  "~standard": {
    validate,
    vendor: "fixture",
    version: 1,
  },
});

describe("generated process runtime with opaque schemas", () => {
  it("validates the exact schema map before source access", async () => {
    const accessorMap: Record<string, unknown> = {};
    Object.setPrototypeOf(accessorMap, null);
    Object.defineProperty(accessorMap, "setting", {
      enumerable: true,
      get: () => schema(() => ({ value: { region: "eu" } })),
    });
    const symbolMap = {
      setting: schema(() => ({ value: { region: "eu" } })),
      [Symbol("extra")]: true,
    };
    const prototypeMap = {
      setting: schema(() => ({ value: { region: "eu" } })),
    };
    Object.setPrototypeOf(prototypeMap, { inherited: true });
    const cases: readonly (readonly [string, unknown])[] = [
      ["missing", {}],
      [
        "extra",
        {
          extra: schema(() => ({ value: { region: "eu" } })),
          setting: schema(() => ({ value: { region: "eu" } })),
        },
      ],
      ["symbol", symbolMap],
      ["accessor", accessorMap],
      ["prototype", prototypeMap],
    ];
    let sourceReads = 0;
    const source = new Proxy(
      {},
      {
        getOwnPropertyDescriptor() {
          sourceReads += 1;
        },
      }
    );

    for (const [, schemas] of cases) {
      const result = await checkProcessTargetWithSchemas(
        opaqueTarget(),
        source,
        asProcessTargetSchemas(schemas)
      );
      expect(result).toStrictEqual({
        diagnostics: [{ code: "ENV_CONTRACT_INVALID" }],
        ok: false,
      });
    }
    expect(sourceReads).toBe(0);
  });

  it("maps a correctly keyed malformed schema to its entry", async () => {
    let sourceReads = 0;
    const source = new Proxy(
      {},
      {
        getOwnPropertyDescriptor() {
          sourceReads += 1;
          return {
            configurable: true,
            enumerable: true,
            value: "input",
            writable: true,
          };
        },
      }
    );
    const malformedSchemas: ProcessTargetSchemas = {
      setting: schema(() => ({ value: { region: "unused" } })),
    };
    Reflect.deleteProperty(malformedSchemas, "setting");
    Object.defineProperty(malformedSchemas, "setting", {
      configurable: true,
      enumerable: true,
      writable: true,
    });
    const result = await checkProcessTargetWithSchemas(
      opaqueTarget({ required: true }),
      source,
      malformedSchemas
    );

    expect(result).toStrictEqual({
      diagnostics: [
        {
          code: "ENV_INVALID_VALUE",
          codec: "astilba.env.opaque/v1",
          consumer: "server",
          entry: "setting",
          lifecycle: "deployment",
        },
      ],
      ok: false,
    });
    expect(sourceReads).toBe(1);
  });

  it("treats optional entry plus non-optional missing input as absence", async () => {
    let calls = 0;
    const result = await checkProcessTargetWithSchemas<{
      readonly setting?: Readonly<{ region: string }>;
    }>(
      opaqueTarget(),
      {},
      {
        setting: schema(() => {
          calls += 1;
          return { value: { region: "unexpected" } };
        }),
      }
    );

    expect(result).toStrictEqual({
      ok: true,
      value: nullRecord({}),
    });
    expect(calls).toBe(0);
    assertSuccess(result);
    expect(Object.getPrototypeOf(result.value)).toBeNull();
    expect(Object.isFrozen(result.value)).toBe(true);
  });

  it("does not invoke a validator for required non-optional missing input", async () => {
    let calls = 0;
    const result = await checkProcessTargetWithSchemas(
      opaqueTarget({ required: true }),
      {},
      {
        setting: schema(() => {
          calls += 1;
          return { value: { region: "unexpected" } };
        }),
      }
    );

    expect(result).toStrictEqual({
      diagnostics: [
        {
          code: "ENV_MISSING_VALUE",
          codec: "astilba.env.opaque/v1",
          consumer: "server",
          entry: "setting",
          lifecycle: "deployment",
        },
      ],
      ok: false,
    });
    expect(calls).toBe(0);
  });

  it("passes undefined to an optional-input validator and owns its output", async () => {
    let observed: unknown = "not called";
    const result = await checkProcessTargetWithSchemas<{
      readonly setting: Readonly<{ region: string }>;
    }>(
      opaqueTarget({
        inputOptional: true,
        required: true,
      }),
      {},
      {
        setting: schema((input) => {
          observed = input;
          return {
            value: {
              region: "eu",
            },
          };
        }),
      }
    );

    expect(observed).toBeUndefined();
    expect(result).toStrictEqual({
      ok: true,
      value: nullRecord({
        setting: nullRecord({
          region: "eu",
        }),
      }),
    });
    assertSuccess(result);
    expect(Object.getPrototypeOf(result.value.setting)).toBeNull();
    expect(Object.isFrozen(result.value.setting)).toBe(true);
  });

  it("uses the exact Standard Schema observation order", async () => {
    const observations: string[] = [];
    const candidate = new Proxy(
      {
        value: { region: "eu" },
      },
      {
        get(target, property, receiver) {
          if (property === "then") {
            observations.push("result.then");
          }
          const result: unknown = Reflect.get(target, property, receiver);
          return result;
        },
        getOwnPropertyDescriptor(target, property) {
          observations.push(`result.${String(property)}`);
          return Reflect.getOwnPropertyDescriptor(target, property);
        },
        getPrototypeOf(target) {
          observations.push("result.prototype");
          return Reflect.getPrototypeOf(target);
        },
        ownKeys() {
          throw new Error("result keys must not be enumerated");
        },
      }
    );
    const standard = {
      get validate() {
        observations.push("standard.validate");
        return () => candidate;
      },
      get version() {
        observations.push("standard.version");
        return 1 as const;
      },
      vendor: "fixture",
    };
    const observedSchema: StandardSchemaV1 = {
      get "~standard"() {
        observations.push("schema.~standard");
        return standard;
      },
    };
    const result = await checkProcessTargetWithSchemas(
      opaqueTarget({ required: true }),
      { SETTING: "input" },
      {
        setting: observedSchema,
      }
    );

    expect(result).toMatchObject({ ok: true });
    expect(observations).toStrictEqual([
      "schema.~standard",
      "standard.version",
      "standard.validate",
      "result.then",
      "result.prototype",
      "result.issues",
      "result.value",
    ]);
  });

  it("refuses a structural thenable without invoking its function", async () => {
    let calls = 0;
    let thenReads = 0;
    const candidate: { readonly value: unknown } = {
      value: { region: "eu" },
    };
    Object.setPrototypeOf(candidate, null);
    const thenName = String.fromCodePoint(116, 104, 101, 110);
    Object.defineProperty(candidate, thenName, {
      get() {
        thenReads += 1;
        return () => {
          calls += 1;
        };
      },
    });
    const result = await checkProcessTargetWithSchemas(
      opaqueTarget({ required: true }),
      { SETTING: "input" },
      {
        setting: schema(() => candidate),
      }
    );

    expect(result).toStrictEqual({
      diagnostics: [
        {
          code: "ENV_VALIDATOR_ASYNC_UNSUPPORTED",
          codec: "astilba.env.opaque/v1",
          consumer: "server",
          entry: "setting",
          lifecycle: "deployment",
        },
      ],
      ok: false,
    });
    expect(thenReads).toBe(1);
    expect(calls).toBe(0);
  });

  it("observes a rejected Promise while refusing async validation", async () => {
    const unhandled: unknown[] = [];
    const listener = (reason: unknown): void => {
      unhandled.push(reason);
    };
    process.on("unhandledRejection", listener);
    try {
      const result = await checkProcessTargetWithSchemas(
        opaqueTarget({ required: true }),
        { SETTING: "input" },
        {
          setting: schema(async (): Promise<never> => {
            await Promise.reject(new Error("validator rejection"));
            throw new Error("Validator unexpectedly continued.");
          }),
        }
      );

      expect(result).toMatchObject({
        diagnostics: [
          {
            code: "ENV_VALIDATOR_ASYNC_UNSUPPORTED",
          },
        ],
        ok: false,
      });
      await setImmediatePromise();
      expect(unhandled).toStrictEqual([]);
    } finally {
      process.off("unhandledRejection", listener);
    }
  });

  it("refuses oversized opaque input before validator execution", async () => {
    let calls = 0;
    const result = await checkProcessTargetWithSchemas(
      opaqueTarget({ required: true }),
      {
        SETTING: "a".repeat(1_048_577),
      },
      {
        setting: schema(() => {
          calls += 1;
          return { value: { region: "eu" } };
        }),
      }
    );

    expect(result).toMatchObject({
      diagnostics: [{ code: "ENV_INVALID_VALUE" }],
      ok: false,
    });
    expect(calls).toBe(0);
  });
});
