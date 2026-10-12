import { describe, expect, it } from "vitest";

import { ContractDefinitionError } from "./diagnostics.ts";
import {
  copyOpaqueShapeValue,
  copyPortableShapeValue,
  normalizeOpaqueShape,
  normalizePortableShape,
  PortableShapeValueError,
} from "./shapes.ts";

const configurationShape = {
  kind: "object",
  properties: [
    {
      name: "region",
      required: true,
      shape: { kind: "string" },
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
      name: "retries",
      required: false,
      shape: {
        kind: "safe-integer",
        maximum: 10,
        minimum: 0,
      },
    },
    {
      name: "enabled",
      required: true,
      shape: { kind: "boolean" },
    },
    {
      name: "marker",
      required: true,
      shape: { kind: "null" },
    },
  ],
} as const;

describe("portable output shapes", () => {
  it("normalizes exact object properties into canonical name order", () => {
    const normalized = normalizePortableShape(configurationShape);

    expect(
      normalized.properties.map((property) => property.name)
    ).toStrictEqual(["enabled", "features", "marker", "region", "retries"]);
    expect(Object.isFrozen(normalized)).toBe(true);
    expect(Object.isFrozen(normalized.properties)).toBe(true);
  });

  it("copies and recursively freezes matching data", () => {
    const output = copyPortableShapeValue(
      normalizePortableShape(configurationShape),
      {
        enabled: false,
        features: ["alpha", "beta"],
        marker: null,
        region: "eu-west",
        retries: 3,
      }
    );

    expect(output).toStrictEqual(
      Object.assign(Object.create(null), {
        enabled: false,
        features: ["alpha", "beta"],
        marker: null,
        region: "eu-west",
        retries: 3,
      })
    );
    expect(Object.getPrototypeOf(output)).toBeNull();
    expect(Object.isFrozen(output)).toBe(true);
    expect(Object.isFrozen(output.features)).toBe(true);
  });

  it("keeps optional absence distinct from invalid presence", () => {
    const shape = normalizeOpaqueShape({
      kind: "optional",
      value: { kind: "string" },
    });

    expect(copyOpaqueShapeValue(shape, undefined)).toBeUndefined();
    expect(copyOpaqueShapeValue(shape, "present")).toBe("present");
    expect(() => copyOpaqueShapeValue(shape, null)).toThrow(
      PortableShapeValueError
    );
  });

  it.each([
    {
      case: "unknown object key",
      value: {
        enabled: true,
        extra: "no",
        features: [],
        marker: null,
        region: "eu",
      },
    },
    {
      case: "missing required key",
      value: {
        enabled: true,
        features: [],
        marker: null,
      },
    },
    {
      case: "decimal",
      value: {
        enabled: true,
        features: [],
        marker: null,
        region: "eu",
        retries: 1.5,
      },
    },
    {
      case: "negative zero",
      value: {
        enabled: true,
        features: [],
        marker: null,
        region: "eu",
        retries: -0,
      },
    },
    {
      case: "wrong boolean",
      value: {
        enabled: "true",
        features: [],
        marker: null,
        region: "eu",
      },
    },
    {
      case: "non-plain object",
      value: new (class Configuration {
        enabled = true;
        features: string[] = [];
        marker = null;
        region = "eu";
      })(),
    },
  ])("rejects $case", ({ value }) => {
    expect(() => copyPortableShapeValue(configurationShape, value)).toThrow(
      PortableShapeValueError
    );
  });

  it("rejects sparse, cyclic, accessor-bearing, and oversized output", () => {
    const arrayShape = {
      items: { kind: "string" },
      kind: "array",
      maximumItems: 4,
      minimumItems: 0,
    } as const;
    const sparse: string[] = [];
    sparse.length = 2;
    expect(() => copyPortableShapeValue(arrayShape, sparse)).toThrow(
      PortableShapeValueError
    );

    const cyclic: unknown[] = [];
    cyclic.push(cyclic);
    const recursiveLookingShape = {
      items: {
        items: { kind: "string" },
        kind: "array",
        maximumItems: 4,
        minimumItems: 0,
      },
      kind: "array",
      maximumItems: 4,
      minimumItems: 0,
    } as const;
    expect(() => copyPortableShapeValue(recursiveLookingShape, cyclic)).toThrow(
      PortableShapeValueError
    );

    const accessor = {
      enabled: true,
      features: [],
      marker: null,
      get region() {
        throw new Error("sensitive-test-value");
      },
    };
    expect(() => copyPortableShapeValue(configurationShape, accessor)).toThrow(
      PortableShapeValueError
    );

    expect(() =>
      copyPortableShapeValue({ kind: "string" }, "x".repeat(65_537))
    ).toThrow(PortableShapeValueError);
  });

  it.each([
    {
      kind: "object",
      properties: [
        {
          name: "__proto__",
          required: true,
          shape: { kind: "string" },
        },
      ],
    },
    {
      kind: "object",
      properties: [
        {
          name: "same",
          required: true,
          shape: { kind: "string" },
        },
        {
          name: "same",
          required: false,
          shape: { kind: "string" },
        },
      ],
    },
    {
      items: { kind: "string" },
      kind: "array",
      maximumItems: 1025,
      minimumItems: 0,
    },
    {
      kind: "safe-integer",
      maximum: 0,
      minimum: 1,
    },
  ])("rejects an invalid declarative shape", (shape) => {
    expect(() =>
      normalizePortableShape(
        // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- Each table shape deliberately violates a portable-shape invariant.
        shape as unknown as Parameters<typeof normalizePortableShape>[0]
      )
    ).toThrow(ContractDefinitionError);
  });

  it("does not execute descriptor accessors while rejecting them", () => {
    let reads = 0;
    const shape = {
      get kind() {
        reads += 1;
        throw new Error("sensitive-test-value");
      },
    };

    expect(() =>
      normalizePortableShape(
        // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- The accessor-bearing value is intentionally forged to prove descriptor refusal.
        shape as unknown as Parameters<typeof normalizePortableShape>[0]
      )
    ).toThrow(ContractDefinitionError);
    expect(reads).toBe(0);
  });
});
