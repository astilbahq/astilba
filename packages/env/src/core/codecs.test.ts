import { describe, expect, it } from "vitest";

import {
  compareCodecCompatibility,
  ContractDefinitionError,
  enumCodec,
  normalizeCodecDescriptor,
  opaqueCodec,
  originCodec,
  stringCodec,
  validatePortableValue,
} from "./index.ts";
import type { CodecDescriptor } from "./index.ts";

describe("portable codecs", () => {
  it("counts Unicode code points rather than UTF-16 code units", () => {
    const codec = stringCodec({ minCodePoints: 1, maxCodePoints: 1 });

    expect(validatePortableValue(codec, "🪻")).toStrictEqual({
      ok: true,
      value: "🪻",
    });
    expect(validatePortableValue(codec, "ab")).toStrictEqual({
      code: "ENV_INVALID_VALUE",
      ok: false,
    });
    expect(validatePortableValue(codec, "\uD800")).toStrictEqual({
      code: "ENV_INVALID_VALUE",
      ok: false,
    });
  });

  it("normalizes enum declaration order and rejects duplicates", () => {
    expect(enumCodec(["safe", "preview"]).values).toStrictEqual([
      "preview",
      "safe",
    ]);
    expect(() => enumCodec(["safe", "safe"])).toThrow(ContractDefinitionError);
  });

  it.each([
    ["https://api.example.com", "https://api.example.com"],
    ["https://api.example.com/", "https://api.example.com"],
    ["https://api.example.com:443", "https://api.example.com"],
    ["https://api.example.com:8443/", "https://api.example.com:8443"],
    ["https://0xcorp.example", "https://0xcorp.example"],
    ["https://api.0xcorp", "https://api.0xcorp"],
  ])("canonicalizes an allowed ASCII HTTPS origin", (input, output) => {
    expect(validatePortableValue(originCodec(), input)).toStrictEqual({
      ok: true,
      value: output,
    });
  });

  it.each([
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
  ])("rejects a non-authoritative origin: %s", (input) => {
    expect(validatePortableValue(originCodec(), input)).toStrictEqual({
      code: "ENV_INVALID_VALUE",
      ok: false,
    });
  });

  it("keeps every opaque comparison unknown", () => {
    const first = opaqueCodec({
      input: { kind: "string" },
      output: { kind: "string" },
      revision: "1",
      semantics: "acme/normalised-code@1",
    });
    const repeated = opaqueCodec({
      input: { kind: "string" },
      output: { kind: "string" },
      revision: "1",
      semantics: "acme/normalised-code@1",
    });

    expect(compareCodecCompatibility(first, repeated)).toBe("UNKNOWN");
    expect(
      compareCodecCompatibility(
        stringCodec({ minCodePoints: 1, maxCodePoints: 10 }),
        stringCodec({ minCodePoints: 1, maxCodePoints: 10 })
      )
    ).toBe("EQUAL");
  });

  it("accepts only process-compatible opaque input shapes", () => {
    expect(
      opaqueCodec({
        input: { kind: "string" },
        output: { kind: "boolean" },
        revision: "1",
        semantics: "acme/direct-input@1",
      }).input
    ).toStrictEqual({ kind: "string" });
    expect(
      opaqueCodec({
        input: {
          kind: "optional",
          value: { kind: "string" },
        },
        output: {
          items: { kind: "boolean" },
          kind: "array",
          maximumItems: 2,
          minimumItems: 0,
        },
        revision: "1",
        semantics: "acme/optional-input@1",
      }).input
    ).toStrictEqual({
      kind: "optional",
      value: { kind: "string" },
    });
  });

  it.each([
    { kind: "boolean" },
    {
      items: { kind: "string" },
      kind: "array",
      maximumItems: 1,
      minimumItems: 0,
    },
    {
      kind: "object",
      properties: [
        {
          name: "region",
          required: true,
          shape: { kind: "string" },
        },
      ],
    },
  ])("rejects an unreachable opaque input shape", (input) => {
    expect(() =>
      // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- This case forges an unreachable opaque input shape to prove descriptor refusal.
      normalizeCodecDescriptor({
        abi: "astilba.env.opaque/v1",
        input,
        kind: "opaque",
        output: { kind: "string" },
        revision: "1",
        semantics: "acme/invalid-input@1",
      } as unknown as CodecDescriptor)
    ).toThrow(ContractDefinitionError);
  });

  it("does not execute accessors while rejecting executable descriptors", () => {
    let reads = 0;
    const descriptor = {
      abi: "astilba.env.string-code-point/v1",
      get kind() {
        reads += 1;
        throw new Error("sensitive-test-value");
      },
      maxCodePoints: 1,
      minCodePoints: 1,
    };

    expect(() =>
      // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- The accessor-bearing object is intentionally not a valid codec descriptor.
      normalizeCodecDescriptor(descriptor as unknown as CodecDescriptor)
    ).toThrow(ContractDefinitionError);
    expect(reads).toBe(0);
  });
});
