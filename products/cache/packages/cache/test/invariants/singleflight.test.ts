// INV-5  — spec §11.5, errata E6
// INV-24 — spec §11.24 (M10)
// Sacred: append-only. Never modify, delete, weaken, skip, or .todo an existing test.

import { mkTag, world } from "@astilba/harness";
import { test } from "@fast-check/vitest";
import fc from "fast-check";
import { describe, expect } from "vitest";

describe("singleflight", () => {
  test("INV-5: one factory per isolate per compatible-options key", async () => {
    const w = world({ isolates: 1 });
    const { cache } = w.isolate(0);
    const tag = mkTag("t0");
    let calls = 0;
    const factory = async () => {
      calls++;
      return await Promise.resolve("v");
    };
    const [a, b] = await Promise.all([
      cache.getOrSet({ factory, key: "k", tags: [tag] }),
      cache.getOrSet({ factory, key: "k", tags: [tag] }),
    ]);
    expect(a).toBe("v");
    expect(b).toBe("v");
    expect(calls).toBe(1); // singleflight: one factory serves both callers
  });

  test("INV-5: a strong request never joins a non-strong fill (E6)", async () => {
    const w = world({ isolates: 1 });
    const { cache } = w.isolate(0);
    const tag = mkTag("t0");
    let strongRuns = 0;
    const [, strong] = await Promise.all([
      cache.getOrSet({
        consistency: "eventual",
        factory: async () => await Promise.resolve("eventual"),
        key: "k",
        tags: [tag],
      }),
      cache.getOrSet({
        consistency: "strong",
        factory: async () => {
          strongRuns++;
          return await Promise.resolve("strong");
        },
        key: "k",
        tags: [tag],
      }),
    ]);
    // The strong caller runs its own fill (join identity includes consistency).
    expect(strong).toBe("strong");
    expect(strongRuns).toBe(1);
  });

  test("INV-24: skip() is reachable only from getOrSetEntry and yields a skipped entry", async () => {
    const w = world({ isolates: 1 });
    const { cache } = w.isolate(0);
    const tag = mkTag("t0");
    const entry = await cache.getOrSetEntry<string>({
      factory: (ctx) => ctx.skip(), // `skip` exists only on the entry-form ctx (M10)
      key: "k",
      tags: [tag],
    });
    expect(entry.skipped).toBeTruthy();
    expect(entry.value).toBeUndefined();
  });

  test.prop([fc.integer({ max: 8, min: 2 })])(
    "INV-5 property: N concurrent same-key fills run exactly one factory",
    async (n) => {
      const w = world({ isolates: 1 });
      const { cache } = w.isolate(0);
      const tag = mkTag("t0");
      let calls = 0;
      const factory = async () => {
        calls++;
        return await Promise.resolve("v");
      };
      await Promise.all(
        Array.from(
          { length: n },
          async () => await cache.getOrSet({ factory, key: "k", tags: [tag] })
        )
      );
      expect(calls).toBe(1);
    }
  );
});
