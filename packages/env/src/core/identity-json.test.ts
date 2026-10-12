import { describe, expect, it } from "vitest";

import {
  canonicalJson,
  canonicalJsonBytes,
  deepFreezeJson,
  hasAsciiCaseFoldCollision,
  isContractId,
  isLocalId,
  isRawSourceName,
  sha256Digest,
} from "./index.ts";
import type { JsonValue } from "./index.ts";

describe("portable identities", () => {
  it("accepts only the pinned contract and local ID grammars", () => {
    expect(isContractId("com.example.platform")).toBe(true);
    expect(isContractId("a.b")).toBe(true);
    expect(isContractId("example")).toBe(false);
    expect(isContractId("Com.example")).toBe(false);
    expect(isContractId("com.exämple")).toBe(false);
    expect(isContractId("com.-example")).toBe(false);
    expect(isContractId(`com.${"a".repeat(64)}`)).toBe(false);

    expect(isLocalId("releaseSha")).toBe(true);
    expect(isLocalId("release_sha")).toBe(false);
    expect(isLocalId("ReleaseSha")).toBe(false);
    expect(isLocalId(`a${"B".repeat(63)}`)).toBe(true);
    expect(isLocalId(`a${"B".repeat(64)}`)).toBe(false);
  });

  it("uses the portable raw-name grammar and ASCII case folding", () => {
    expect(isRawSourceName("INTERNAL_API_TOKEN")).toBe(true);
    expect(isRawSourceName("_INTERNAL_API_TOKEN")).toBe(true);
    expect(isRawSourceName("InternalApiToken")).toBe(false);
    expect(hasAsciiCaseFoldCollision(["featureMode", "featuremode"])).toBe(
      true
    );
    expect(hasAsciiCaseFoldCollision(["featureMode", "brandLabel"])).toBe(
      false
    );
  });
});

describe("canonical JSON", () => {
  it("sorts object keys while retaining array order", () => {
    const left = { a: "first", z: [3, 2, 1] };
    Object.setPrototypeOf(left, null);
    const right = { a: "first", z: [3, 2, 1] };
    Object.setPrototypeOf(right, null);

    expect(canonicalJson(left)).toBe('{"a":"first","z":[3,2,1]}');
    expect(canonicalJsonBytes(left)).toStrictEqual(canonicalJsonBytes(right));
  });

  it("copies to recursively frozen JSON-safe output", () => {
    const input = {
      list: [{ value: "safe" }],
    };
    const output = deepFreezeJson(input);

    expect(Object.isFrozen(output)).toBe(true);
    expect(Object.isFrozen(output.list)).toBe(true);
    expect(Object.isFrozen(output.list[0])).toBe(true);
    expect(Object.getPrototypeOf(output)).toBeNull();
    expect(output).not.toBe(input);
    const inputItem = input.list.at(0);
    const outputItem = output.list.at(0);
    if (inputItem === undefined || outputItem === undefined) {
      throw new TypeError("The JSON fixture is missing its value.");
    }
    expect(() => {
      inputItem.value = "changed";
    }).not.toThrow();
    expect(outputItem.value).toBe("safe");
  });

  it.each([
    -0,
    -1.5,
    1.5,
    Number.NaN,
    Number.POSITIVE_INFINITY,
    1n,
    undefined,
    Symbol("not-json"),
    () => "not-json",
    "\uD800",
  ])("rejects a non-portable value", (value) => {
    // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- Each table value is deliberately outside the portable JSON domain.
    expect(() => canonicalJson(value as JsonValue)).toThrow(TypeError);
  });

  it("rejects cycles, accessors, holes, prototypes, and dangerous keys", () => {
    const cycle: Record<string, JsonValue> = {};
    Object.setPrototypeOf(cycle, null);
    cycle.self = cycle;

    const accessor: Record<string, unknown> = {};
    Object.setPrototypeOf(accessor, null);
    Object.defineProperty(accessor, "value", {
      enumerable: true,
      get: () => "hidden",
    });

    const dangerous: Record<string, unknown> = {};
    Object.setPrototypeOf(dangerous, null);
    Object.defineProperty(dangerous, "__proto__", {
      enumerable: true,
      value: "value",
    });
    const sparse: JsonValue[] = [];
    sparse.length = 1;

    expect(() => canonicalJson(cycle)).toThrow(TypeError);
    // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- This accessor-bearing record is intentionally hostile JSON input.
    expect(() => canonicalJson(accessor as JsonValue)).toThrow(TypeError);
    expect(() => canonicalJson(sparse)).toThrow(TypeError);
    // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- Date is deliberately forged as JSON input to prove prototype refusal.
    expect(() => canonicalJson(new Date() as unknown as JsonValue)).toThrow(
      TypeError
    );
    // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- This own __proto__ key is deliberately hostile JSON input.
    expect(() => canonicalJson(dangerous as JsonValue)).toThrow(TypeError);
  });

  it("produces an unpadded Web Crypto SHA-256 base64url digest", async () => {
    await expect(sha256Digest(new Uint8Array())).resolves.toBe(
      "sha256-47DEQpj8HBSa-_TImW-5JCeuQeRkm5NMpJWZG3hSuFU"
    );
  });
});
