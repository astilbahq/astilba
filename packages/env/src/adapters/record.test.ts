import { describe, expect, it } from "vitest";

import { materializeStringRecord } from "./record.ts";

describe("thin record adapter", () => {
  it("copies only declared own string values into a null-prototype record", () => {
    const inherited = { SECRET: "inherited" };
    const source: Record<string, unknown> = {
      API_ORIGIN: "https://api.example",
      UNUSED: "ignored",
    };
    Object.setPrototypeOf(source, inherited);

    const output = materializeStringRecord(source, ["API_ORIGIN", "SECRET"]);

    expect(Object.getPrototypeOf(output)).toBeNull();
    expect(Object.entries(output)).toStrictEqual([
      ["API_ORIGIN", "https://api.example"],
    ]);
    expect(Object.hasOwn(output, "UNUSED")).toBe(false);
    expect(Object.hasOwn(output, "SECRET")).toBe(false);
  });

  it("rejects duplicate source identities before reading values", () => {
    expect(() =>
      materializeStringRecord({}, ["API_ORIGIN", "API_ORIGIN"])
    ).toThrow("Case-folding source collision");
  });

  it("rejects names outside the portable uppercase grammar", () => {
    expect(() => materializeStringRecord({}, ["api_origin"])).toThrow(
      "Unsupported raw source name"
    );
  });

  it("rejects an accessor without invoking it", () => {
    let reads = 0;
    const source: Record<string, unknown> = {};
    Object.setPrototypeOf(source, null);
    Object.defineProperty(source, "INTERNAL_TOKEN", {
      enumerable: true,
      get() {
        reads += 1;
        throw new Error("sensitive-test-value");
      },
    });

    expect(() => materializeStringRecord(source, ["INTERNAL_TOKEN"])).toThrow(
      "Source values must be own data properties"
    );
    expect(reads).toBe(0);
  });
});
