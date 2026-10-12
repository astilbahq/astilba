import { describe, expect, it } from "vitest";

import { integerCodec, resolveServerValue, textCodec } from "./index.ts";

describe("deterministic server codecs", () => {
  it("trims ordinary text while preserving a non-blank secret exactly", () => {
    const trimmed = textCodec({
      blank: "missing",
      maxCodePoints: 64,
      minCodePoints: 1,
      normalise: "trim",
    });
    const secret = textCodec({
      blank: "missing",
      maxCodePoints: 64,
      minCodePoints: 1,
      normalise: "preserve",
    });

    expect(resolveServerValue(trimmed, "  value  ")).toStrictEqual({
      ok: true,
      present: true,
      value: "value",
    });
    expect(resolveServerValue(secret, "  value  ")).toStrictEqual({
      ok: true,
      present: true,
      value: "  value  ",
    });
    expect(resolveServerValue(secret, "   ")).toStrictEqual({
      ok: true,
      present: false,
    });
  });

  it("parses bounded decimal integers and keeps missing input absent", () => {
    const port = integerCodec({
      blank: "missing",
      default: null,
      maximum: 65_535,
      minimum: 0,
    });

    expect(resolveServerValue(port, undefined)).toStrictEqual({
      ok: true,
      present: false,
    });
    expect(resolveServerValue(port, " 8080 ")).toStrictEqual({
      ok: true,
      present: true,
      value: 8080,
    });
    expect(resolveServerValue(port, "   ")).toStrictEqual({
      ok: true,
      present: false,
    });
    expect(resolveServerValue(port, "1e3")).toStrictEqual({
      code: "ENV_INVALID_VALUE",
      ok: false,
    });
    expect(resolveServerValue(port, "70000")).toStrictEqual({
      code: "ENV_INVALID_VALUE",
      ok: false,
    });
  });

  it.each([
    ["0", 0],
    ["65535", 65_535],
    ["+1", 1],
    ["01", 1],
    ["-0", 0],
  ])("defines accepted decimal syntax for %s", (input, output) => {
    const codec = integerCodec({
      blank: "missing",
      default: null,
      maximum: 65_535,
      minimum: 0,
    });
    expect(resolveServerValue(codec, input)).toStrictEqual({
      ok: true,
      present: true,
      value: output,
    });
  });

  it.each(["-1", "65536", "1.0", "1e3", "0x10", "９", "9007199254740992"])(
    "rejects non-contract integer syntax or range: %s",
    (input) => {
      const codec = integerCodec({
        blank: "missing",
        default: null,
        maximum: 65_535,
        minimum: 0,
      });
      expect(resolveServerValue(codec, input)).toStrictEqual({
        code: "ENV_INVALID_VALUE",
        ok: false,
      });
    }
  );

  it("treats an own undefined source as missing for first-party server codecs", () => {
    const optional = textCodec({
      blank: "missing",
      maxCodePoints: 64,
      minCodePoints: 1,
      normalise: "trim",
    });
    expect(resolveServerValue(optional, undefined)).toStrictEqual({
      ok: true,
      present: false,
    });
  });
});
