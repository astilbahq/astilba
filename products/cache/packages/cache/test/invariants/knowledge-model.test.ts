// INV-11 — spec
// INV-21 — spec
// INV-25 (race-shaped: fc.scheduler())
// Sacred: append-only. Never modify, delete, weaken, skip, or .todo an existing test.

import { commandsArb, mkTag, modelSetup, world } from "@astilba/harness";
import { test } from "@fast-check/vitest";
import fc from "fast-check";
import { describe, expect } from "vitest";

describe("knowledge model", () => {
  test("INV-11: unknown tag knowledge never serves fresh — registry-check or miss", async () => {
    const w = world({ isolates: 2, replicas: 2 });
    const writer = w.isolate(0).cache;
    const reader = w.isolate(1).cache;
    const tag = mkTag("t0");
    await writer.getOrSet({
      factory: async () => await Promise.resolve("v1"),
      key: "k",
      tags: [tag],
    });
    w.chaos.dropNextFrames(5); // reader's knowledge is gapped ⇒ unknown
    w.chaos.delayKey("__state", 60_000);
    await writer.delete({ tag });
    await w.advance(1000);
    const entry = await reader.getOrSetEntry({
      factory: async () => await Promise.resolve("v2"),
      key: "k",
      tags: [tag],
    });
    // Gapped/unknown ⇒ registry-check or miss; v1 is never served fresh.
    expect(entry.value).not.toBe("v1");
  });

  test("INV-21: tag-map eviction can never make a stale/dead envelope validate fresh", async () => {
    const w = world({ isolates: 1 });
    const { cache } = w.isolate(0);
    const tag = mkTag("t0");
    await cache.getOrSet({
      factory: async () => await Promise.resolve("v1"),
      key: "k",
      tags: [tag],
    });
    await cache.delete({ tag }); // dead
    await w.advance(100_000); // time + eviction pressure
    const entry = await cache.getOrSetEntry({
      consistency: "strong",
      factory: async () => await Promise.resolve("v2"),
      key: "k",
      tags: [tag],
    });
    expect(entry.value).toBe("v2");
    expect(entry.stale).toBeFalsy();
  });

  test.prop([fc.scheduler()])(
    "INV-25: a bus gap/reconnect forces resync before warm trust (E4)",
    async (s) => {
      const w = world({ isolates: 2, replicas: 2 });
      const writer = w.isolate(0).cache;
      const reader = w.isolate(1).cache;
      const tag = mkTag("t0");
      await writer.getOrSet({
        factory: async () => await Promise.resolve("v1"),
        key: "k",
        tags: [tag],
      });
      w.chaos.disconnect();
      const purgeP = writer.delete({ tag }); // frame lost while the socket is down
      w.chaos.reconnect();
      await s.waitIdle();
      await Promise.allSettled([purgeP]);
      await w.advance(6000); // > maxSyncLag ⇒ forced resync
      const entry = await reader.getOrSetEntry({
        factory: async () => await Promise.resolve("v2"),
        key: "k",
        tags: [tag],
      });
      // Reconnect forces resync before warm trust; the lost purge is not missed.
      expect(entry.value).not.toBe("v1");
    }
  );

  test.prop([commandsArb()])(
    "INV-11 INV-21 INV-25 property: observed serve is a subset of legal serves under bus chaos",
    async (cmds) => {
      await fc.asyncModelRun(modelSetup({ isolates: 2, replicas: 2 }), cmds);
    }
  );
});
