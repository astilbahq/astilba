import { describe, expect, test } from "vitest";

import { SimClock, SimRng, SkewedClock } from "./clock";

// Harness self-tests (P1) — the deterministic foundation earns its own tests: a
// broken clock/RNG would silently invalidate every invariant above it. Ordinary
// unit tests, NOT part of the sacred invariant set.

describe(SimClock, () => {
  test("starts at 0 and advances deterministically", () => {
    const clock = new SimClock();
    expect(clock.now()).toBe(0);
    clock.advance(100);
    expect(clock.now()).toBe(100);
    clock.advance(50);
    expect(clock.now()).toBe(150);
  });

  test("never moves backward", () => {
    const clock = new SimClock();
    expect(() => {
      clock.advance(-1);
    }).toThrow(/must be >= 0/u);
  });

  test("honours a custom start", () => {
    const clock = new SimClock(1000);
    expect(clock.now()).toBe(1000);
  });
});

describe(SimRng, () => {
  test("same seed → identical stream (reproducible)", () => {
    const a = new SimRng(42);
    const b = new SimRng(42);
    const streamA = Array.from({ length: 8 }, () => a.next());
    const streamB = Array.from({ length: 8 }, () => b.next());
    expect(streamA).toStrictEqual(streamB);
  });

  test("different seeds → different streams", () => {
    const a = new SimRng(1);
    const b = new SimRng(2);
    expect(a.next()).not.toBe(b.next());
  });

  test("produces floats in [0, 1)", () => {
    const rng = new SimRng(7);
    for (let i = 0; i < 200; i++) {
      const x = rng.next();
      expect(x).toBeGreaterThanOrEqual(0);
      expect(x).toBeLessThan(1);
    }
  });
});

describe(SkewedClock, () => {
  test("shifts now() by the skew while sharing the base timeline", () => {
    const base = new SimClock();
    const skewed = new SkewedClock(base, 15_000);
    expect(skewed.now()).toBe(15_000);
    base.advance(1000);
    expect(base.now()).toBe(1000);
    expect(skewed.now()).toBe(16_000); // moved with the base, still +15s ahead
  });

  test("setSkew mutates the offset; epochs (elsewhere) are unaffected", () => {
    const base = new SimClock();
    const skewed = new SkewedClock(base);
    expect(skewed.now()).toBe(0);
    skewed.setSkew(500);
    expect(skewed.now()).toBe(500);
    expect(skewed.skewMs).toBe(500);
  });
});
