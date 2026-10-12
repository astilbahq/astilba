// Ordinary unit teeth (NOT sacred) — C4-a-0-i. Grammar teeth for the two golden-path pure
// builders: t() (§2.3 + E10 — the tag trust boundary) and duration() (§2.1, F31). Each case
// bites a specific rejection/acceptance branch (rule 13): blinding that branch reds its case.
// assertUserTag's narrow `__`-backstop (MI13, DoD completion 1) is pinned narrow-not-weak here.

import {
  duration,
  InvalidDurationError,
  InvalidTagError,
  t,
} from "@astilba/cache";
import { mkTag } from "@astilba/harness";
import { describe, expect, it } from "vitest";

import { assertUserTag } from "../../src/tags";

describe("t() — the tag trust boundary (§2.3, E10)", () => {
  it("rejects an interpolation with an off-charset character", () => {
    expect(() => t`user:${"a b"}`).toThrow(InvalidTagError); // space
    expect(() => t`user:${"a@b"}`).toThrow(InvalidTagError); // @
    expect(() => t`user:${"a/b"}`).toThrow(InvalidTagError); // /
    expect(() => t`user:${"Book"}`).toThrow(InvalidTagError); // uppercase
  });

  it("rejects an interpolation that makes the tag begin with the reserved __ prefix", () => {
    expect(() => t`${"__evil"}`).toThrow(InvalidTagError);
  });

  it("rejects a result over 256 UTF-8 bytes", () => {
    expect(() => t`book:${"a".repeat(257)}`).toThrow(InvalidTagError);
  });

  it("accepts a clean tag and returns the brand equal to the string", () => {
    const tag = t`book:${5}`;
    expect(tag).toBe("book:5");
  });

  it("accepts every in-charset class (: | . - _ % digits lowercase)", () => {
    expect(() => t`ns:${"a.b-c_d|e%f0"}`).not.toThrow();
    expect(t`ns:${"a.b-c_d|e%f0"}`).toBe("ns:a.b-c_d|e%f0");
  });
});

describe("duration() — computed-value guards the type system can't catch (§2.1, F31)", () => {
  it("REJECTS the post-multiplication overflow: duration(Number.MAX_SAFE_INTEGER, 'd')", () => {
    // The F31 tooth: MAX_SAFE_INTEGER is a safe integer, but MAX_SAFE_INTEGER × 86_400_000
    // is not — the overflow is in the PRODUCT, which a naive pre-multiplication check misses.
    expect(() => duration(Number.MAX_SAFE_INTEGER, "d")).toThrow(
      InvalidDurationError
    );
  });

  it("rejects a non-positive value (0, -1)", () => {
    expect(() => duration(0, "s")).toThrow(InvalidDurationError);
    expect(() => duration(-1, "m")).toThrow(InvalidDurationError);
  });

  it("rejects a non-finite value (NaN, Infinity)", () => {
    expect(() => duration(Number.NaN, "s")).toThrow(InvalidDurationError);
    expect(() => duration(Number.POSITIVE_INFINITY, "h")).toThrow(
      InvalidDurationError
    );
  });

  it("accepts clean integral and fractional durations", () => {
    expect(duration(5, "m")).toBe("5m");
    expect(duration(1.5, "h")).toBe("1.5h");
  });
});

describe("assertUserTag — the narrow __-backstop is narrow, not weak (MI13)", () => {
  it("throws on a __-prefixed forged/raw brand", () => {
    // A raw brand that bypassed the builders (harness mkTag / an `as Tag` cast) still can't
    // smuggle a reserved `__` tag past the write boundary.
    expect(() => {
      assertUserTag(mkTag("__k:forged"));
    }).toThrow(InvalidTagError);
  });

  it("PASSES a merely-uppercase raw brand — it checks only the __ prefix, not charset", () => {
    // Deliberately narrow: charset is the builders' job, so a raw non-`__` brand (even an
    // off-charset one like uppercase) clears this backstop. Not-weak because __ still dies.
    expect(() => {
      assertUserTag(mkTag("BOOK:1"));
    }).not.toThrow();
  });
});
