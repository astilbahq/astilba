// INV-1 — spec
// INV-2 — spec
// INV-6 — spec
// Sacred: append-only. Never modify, delete, weaken, skip, or .todo an existing test.

import { commandsArb, mkTag, modelSetup, world } from "@astilba/harness";
import { test } from "@fast-check/vitest";
import fc from "fast-check";
import { describe, expect } from "vitest";

describe("purge visibility", () => {
  test("INV-1: a hard delete is never served once visible; strong observes it immediately", async () => {
    const w = world({ isolates: 2 });
    const { cache } = w.isolate(0);
    const tag = mkTag("t0");
    await cache.getOrSet({
      factory: async () => await Promise.resolve("v1"),
      key: "k",
      tags: [tag],
    });
    await cache.delete({ tag });
    const served = await cache.getOrSet({
      consistency: "strong",
      factory: async () => await Promise.resolve("v2"),
      key: "k",
      tags: [tag],
    });
    // The deleted v1 is never served; a strong read refills.
    expect(served).toBe("v2");
  });

  test("INV-2: no entry is served whose bornEpoch <= a visible watermark epoch", async () => {
    const w = world({ isolates: 1 });
    const { cache } = w.isolate(0);
    const tag = mkTag("t0");
    await cache.getOrSet({
      factory: async () => await Promise.resolve("v1"),
      key: "k",
      tags: [tag],
    });
    await cache.delete({ tag }); // raises hardEpoch past the entry's bornEpoch
    const entry = await cache.getOrSetEntry({
      consistency: "strong",
      factory: async () => await Promise.resolve("v2"),
      key: "k",
      tags: [tag],
    });
    expect(entry.value).toBe("v2");
    expect(entry.stale).toBeFalsy();
  });

  test("INV-6: cold-path purge visibility includes state-pointer propagation", async () => {
    // unknownPolicy 'miss' makes the __state-propagation gap the binding constraint:
    // the cold reader cannot learn the purge, so its knowledge is unknown and it must
    // fail closed to a miss/refill rather than resolving via an instant registry check.
    const w = world({
      defaults: { unknownPolicy: "miss" },
      isolates: 2,
      replicas: 2,
    });
    const writer = w.isolate(0).cache;
    const reader = w.isolate(1).cache;
    const tag = mkTag("t0");
    await writer.getOrSet({
      factory: async () => await Promise.resolve("v1"),
      key: "k",
      tags: [tag],
    });
    // Gap the reader's knowledge BEFORE the purge: drop the delta frame and stall the
    // __state pointer so the hard delete never reaches the cold reader (tail).
    // The reader's DATA replica is NOT delayed, so it genuinely holds v1 and could
    // wrongly serve it as fresh — the fail-closed obligation is real, not vacuous.
    w.chaos.dropNextFrames(5);
    w.chaos.delayKey("__state", 60_000);
    await writer.delete({ tag });
    await w.advance(1000); // within the cold bound; the purge has not propagated
    const entry = await reader.getOrSetEntry({
      factory: async () => await Promise.resolve("v2"),
      key: "k",
      tags: [tag],
    });
    // observed serve ⊆ legal serves: the cold reader holds v1 but its knowledge is
    // gapped/unknown, so it fails closed — a miss (undefined) or a fresh refill (v2),
    // never the hard-deleted v1 as fresh.
    expect(entry.value).not.toBe("v1");
    expect([undefined, "v2"]).toContain(entry.value);
    expect(entry.stale).toBeFalsy();
  });

  test.prop([commandsArb()])(
    "INV-1 INV-2 INV-6 property: observed serve is a subset of legal serves under interleaved purges",
    async (cmds) => {
      await fc.asyncModelRun(modelSetup({ isolates: 2, replicas: 2 }), cmds);
    }
  );
});
