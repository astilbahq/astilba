// INV-14 — spec §11.14
// INV-16 — spec §11.16 (§4.5, M3)
// INV-19 — spec §11.19 (M2)
// INV-20 — spec §11.20 (B5, MI8)
// INV-28 — errata E11 (§11 invariant amendment)
// Sacred: append-only. Never modify, delete, weaken, skip, or .todo an existing test.

import { commandsArb, mkTag, modelSetup, world } from "@astilba/harness";
import { test } from "@fast-check/vitest";
import fc from "fast-check";
import { describe, expect } from "vitest";

describe("watermarks & registry", () => {
  test("INV-14: a later soft purge never regresses a hard purge (monotone, independent)", async () => {
    const w = world({ isolates: 1 });
    const { cache } = w.isolate(0);
    const tag = mkTag("t0");
    await cache.getOrSet({
      factory: async () => await Promise.resolve("v1"),
      key: "k",
      tags: [tag],
    });
    await cache.delete({ tag }); // hard
    await cache.expire({ tag }); // later soft — must not un-delete
    const entry = await cache.getOrSetEntry({
      consistency: "strong",
      factory: async () => await Promise.resolve("v2"),
      key: "k",
      tags: [tag],
    });
    expect(entry.value).toBe("v2");
    expect(entry.stale).toBeFalsy();
  });

  test("INV-16: GC never collects a watermark younger than the max registered retention", async () => {
    const w = world({ isolates: 1 });
    const { cache } = w.isolate(0);
    const tag = mkTag("t0");
    await cache.getOrSet({
      factory: async () => await Promise.resolve("v1"),
      key: "k",
      tags: [tag],
    });
    await cache.delete({ tag });
    await w.advance(1000); // within retention ⇒ watermark must survive
    const entry = await cache.getOrSetEntry({
      consistency: "strong",
      factory: async () => await Promise.resolve("v2"),
      key: "k",
      tags: [tag],
    });
    expect(entry.value).toBe("v2"); // the delete watermark is still enforced
  });

  test("INV-19: a bus gap forces resync before applying later frames", async () => {
    const w = world({ isolates: 2, replicas: 2 });
    const writer = w.isolate(0).cache;
    const reader = w.isolate(1).cache;
    const tag0 = mkTag("t0");
    const tag1 = mkTag("t1");
    await writer.getOrSet({
      factory: async () => await Promise.resolve("v1"),
      key: "k",
      tags: [tag0],
    });
    w.chaos.dropNextFrames(1); // drop the delete frame ⇒ gap
    await writer.delete({ tag: tag0 });
    await writer.expire({ tag: tag1 }); // a later frame arrives after the gap
    await w.advance(6000); // gap forces resync
    const entry = await reader.getOrSetEntry({
      factory: async () => await Promise.resolve("v2"),
      key: "k",
      tags: [tag0],
    });
    expect(entry.value).not.toBe("v1"); // the gapped delete is not silently skipped
  });

  test("INV-20: under sustained purges the flush fires at least once per coalescing window", async () => {
    const w = world({ isolates: 2, replicas: 2 });
    const writer = w.isolate(0).cache;
    const reader = w.isolate(1).cache;
    const tag = mkTag("t0");
    await writer.getOrSet({
      factory: async () => await Promise.resolve("v1"),
      key: "k",
      tags: [tag],
    });
    for (let i = 0; i < 5; i++) {
      await writer.expire({ tag });
      await w.advance(1100); // cross a coalescing window each iteration
    }
    const entry = await reader.getOrSetEntry({
      factory: async () => await Promise.resolve("v2"),
      key: "k",
      tags: [tag],
    });
    expect(entry.stale).toBeTruthy(); // the flush propagated the soft purge
  });

  test("INV-28: the GC horizon is a monotone high-water under registration churn (E11)", async () => {
    const w = world({ isolates: 2 });
    const writer = w.isolate(0).cache;
    const tag = mkTag("t0");
    await writer.getOrSet({
      factory: async () => await Promise.resolve("v1"),
      key: "k",
      tags: [tag],
    });
    await writer.delete({ tag });
    w.chaos.crashIsolate(1); // fleet shrink — the horizon must not retract
    await w.advance(1000);
    const entry = await writer.getOrSetEntry({
      consistency: "strong",
      factory: async () => await Promise.resolve("v2"),
      key: "k",
      tags: [tag],
    });
    expect(entry.value).toBe("v2"); // the delete watermark is still enforced
  });

  test.prop([commandsArb()])(
    "INV-14 INV-16 INV-19 INV-20 INV-28 property: observed serve is a subset of legal serves",
    async (cmds) => {
      await fc.asyncModelRun(modelSetup({ isolates: 2, replicas: 2 }), cmds);
    }
  );
});
