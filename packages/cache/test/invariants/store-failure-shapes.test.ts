import { describe, expect, it } from "vitest";

import { isStoreReadError, isStoreWriteError } from "../../src/drivers";

describe("structural driver failures preserve the read/write boundary (E13)", () => {
  it.each(["throttled", "unavailable"])(
    "recognizes %s from another error class without requiring shared identity",
    (code) => {
      class DriverFailureError extends Error {
        override readonly name = "DriverFailureError";
        readonly retryable = true;
        readonly code = code;
      }
      const failure = new DriverFailureError("driver failed");
      expect(isStoreReadError(failure)).toBe(true);
      expect(isStoreWriteError(failure)).toBe(true);
      expect(isStoreReadError({ code, retryable: false })).toBe(true);
      expect(isStoreWriteError({ code, retryable: false })).toBe(true);
    }
  );

  it("recognizes an oversize write without absorbing it as a read failure", () => {
    const failure = { code: "too_large", retryable: false };
    expect(isStoreWriteError(failure)).toBe(true);
    expect(isStoreReadError(failure)).toBe(false);
  });

  it.each([
    null,
    undefined,
    false,
    0,
    "unavailable",
    [],
    {},
    { code: "unknown", retryable: true },
    { code: "throttled" },
    { retryable: true },
    { code: "unavailable", retryable: "true" },
    { code: "unavailable", retryable: 1 },
    { code: "unavailable", retryable: null },
  ])("does not classify an unrecognized shape: %j", (failure) => {
    expect(isStoreReadError(failure)).toBe(false);
    expect(isStoreWriteError(failure)).toBe(false);
  });
});
