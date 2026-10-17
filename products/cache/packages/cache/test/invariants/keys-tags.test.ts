// INV-10 — spec
// INV-22 — spec
// INV-23 — spec
// INV-29 (hardens INV-22's mechanism — kept distinct)
// Sacred: append-only. Never modify, delete, weaken, skip, or .todo an existing test.

import { compound, NotImplementedError } from "@astilba/cache";
import { mkTag, world } from "@astilba/harness";
import { test } from "@fast-check/vitest";
import fc from "fast-check";
import { describe, expect } from "vitest";

async function rejectionOf(promise: Promise<unknown>): Promise<unknown> {
  return await promise.then(
    () => null,
    (error: unknown) => error
  );
}

describe("keys & tags", () => {
  test("INV-10: expire({key}) invalidates via the reserved __k tag", async () => {
    const w = world({ isolates: 1 });
    const { cache } = w.isolate(0);
    await cache.getOrSet({
      factory: async () => await Promise.resolve("v1"),
      key: "homepage",
      tags: [],
    });
    await cache.expire({ key: "homepage" });
    const after = await cache.getOrSet({
      consistency: "strong",
      factory: async () => await Promise.resolve("v2"),
      key: "homepage",
      tags: [],
    });
    expect(after).toBe("v2");
  });

  test("INV-10: a user tag beginning __ is rejected at write", async () => {
    const w = world({ isolates: 1 });
    const { cache } = w.isolate(0);
    const err = await rejectionOf(
      cache.getOrSet({
        factory: async () => await Promise.resolve("v"),
        key: "k",
        tags: [mkTag("__k:forged")],
      })
    );
    // Must reject because the tag is reserved — NOT merely because the kernel is unimplemented.
    expect(err).toBeInstanceOf(Error);
    expect(err).not.toBeInstanceOf(NotImplementedError);
  });

  test("INV-23: clear() is observed by a lagging isolate through the conservative machinery", async () => {
    const w = world({ isolates: 2, replicas: 2 });
    const writer = w.isolate(0).cache;
    const reader = w.isolate(1).cache;
    const tag = mkTag("t0");
    await writer.getOrSet({
      factory: async () => await Promise.resolve("v1"),
      key: "k",
      tags: [tag],
    });
    await writer.clear(); // version bump + hard __ns watermark
    w.chaos.delayReplica(1, 5000);
    await w.advance(1000);
    const entry = await reader.getOrSetEntry({
      factory: async () => await Promise.resolve("v2"),
      key: "k",
      tags: [tag],
    });
    // The lagging isolate never serves pre-clear data as fresh.
    expect(entry.value).not.toBe("v1");
  });

  test.prop([fc.array(fc.string()), fc.array(fc.string())])(
    "INV-22: compound is injective over argument vectors",
    (a, b) => {
      fc.pre(JSON.stringify(a) !== JSON.stringify(b));
      expect(compound(...a)).not.toBe(compound(...b));
    }
  );

  test.prop([
    fc.array(
      fc.constantFrom("a", "b", ":", "|", "%", "%3a", "%7c", "a:b", "a%3ab", "")
    ),
    fc.array(
      fc.constantFrom("a", "b", ":", "|", "%", "%3a", "%7c", "a:b", "a%3ab", "")
    ),
  ])(
    "INV-29: compound is injective under the E10 escape grammar (%, :, |, empties, escape-lookalikes)",
    (a, b) => {
      fc.pre(JSON.stringify(a) !== JSON.stringify(b));
      expect(compound(...a)).not.toBe(compound(...b));
    }
  );
});
