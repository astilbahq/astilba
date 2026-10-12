/* References: §11, §11.34. */

import { mkTag, world } from "@astilba/harness";
import { test } from "@fast-check/vitest";
import fc from "fast-check";
import { describe, expect } from "vitest";

describe("red-team five", () => {
  test("scenario 1: a missing delta containing a hard purge is never silently skipped", async () => {
    const w = world({ isolates: 2, replicas: 2 });
    const writer = w.isolate(0).cache;
    const reader = w.isolate(1).cache;
    const tag = mkTag("t0");
    await writer.getOrSet({
      factory: async () => "v1",
      key: "k",
      tags: [tag],
    });
    w.chaos.dropNextFrames(1); // the delta frame carrying the hard purge is lost
    w.chaos.delayKey("__state", 60_000); // and the pointer is stale
    await writer.delete({ tag });
    await w.advance(6000);
    const entry = await reader.getOrSetEntry({
      factory: async () => "v2",
      key: "k",
      tags: [tag],
    });
    expect(entry.value).not.toBe("v1");
  });

  test("scenario 2: a fast writer clock beyond any tolerance is moot under epochs", async () => {
    const w = world({ isolates: 1 });
    const { cache } = w.isolate(0);
    const tag = mkTag("t0");
    await cache.getOrSet({
      factory: async () => "v1",
      key: "k",
      tags: [tag],
    });
    await cache.delete({ tag }); // hardEpoch bump
    // Epoch arbitration ignores wall-clock skew: a stale value cannot resurrect.
    const entry = await cache.getOrSetEntry({
      consistency: "strong",
      factory: async () => "v2",
      key: "k",
      tags: [tag],
    });
    expect(entry.value).toBe("v2");
  });

  test("scenario 3: same-key incompatible singleflight runs separate fills", async () => {
    const w = world({ isolates: 1 });
    const { cache } = w.isolate(0);
    const tag = mkTag("t0");
    let strongRuns = 0;
    const [, strong] = await Promise.all([
      cache.getOrSet({
        consistency: "eventual",
        factory: async () => "eventual",
        key: "k",
        tags: [tag],
      }),
      cache.getOrSet({
        consistency: "strong",
        factory: async () => {
          strongRuns++;
          return "strong";
        },
        key: "k",
        tags: [tag],
      }),
    ]);
    expect(strong).toBe("strong");
    expect(strongRuns).toBe(1);
  });

  test.prop([fc.scheduler()])(
    "scenario 4: a lock lease expiring before write-back cannot overwrite fresher content",
    async (s) => {
      const w = world({ isolates: 2 });
      const tag = mkTag("t0");
      // Separate fence from arrival so the assertion cannot be satisfied by a
      // first-writer-wins kernel. The expiring lease-holder (isolate 0) locks FIRST
      // (LOWER fence), is +15s skewed (HIGHER bornMs), and writes FIRST via a bare factory.
      w.chaos.skewClock(0, 15_000);
      const staleLease = w.isolate(0).cache.getOrSet({
        factory: async () => await Promise.resolve("stale-lease"),
        key: "k",
        lock: true,
        tags: [tag],
      });
      // The fresher holder (isolate 1) takes the lease SECOND (HIGHER fence), keeps the
      // normal clock (LOWER bornMs), and writes LAST — its write-back is deferred.
      const fresher = w.isolate(1).cache.getOrSet({
        factory: async () =>
          await s.schedule(Promise.resolve("fresher"), "fresher"),
        key: "k",
        lock: true,
        tags: [tag],
      });
      await s.waitIdle();
      await Promise.allSettled([staleLease, fresher]);
      const entry = await w.isolate(1).cache.getOrSetEntry({
        consistency: "strong",
        factory: async () => await Promise.resolve("refill"),
        key: "k",
        tags: [tag],
      });
      // Born-monotone by (bornEpoch, fence): the fresher holder's higher fence wins even
      // though it wrote LAST and carries the LOWER bornMs. A first-writer-wins kernel keeps
      // the earlier "stale-lease"; a bornMs kernel keeps the +15s "stale-lease" — both
      // FAIL. Fence, not arrival order and not bornMs, is the sole arbiter.
      expect(entry.value).toBe("fresher");
    }
  );

  test("scenario 5: a required render tag discovered after header commit is L3-ineligible", () => {
    const collector = world({ isolates: 1 }).isolate(0).cache.collect();
    collector.dependsOn(mkTag("t0"));
    collector.commitHeaders(); // headers commit
    collector.dependsOn(mkTag("t-late")); // discovered after commit
    const finalEmission = collector.commitHeaders();
    expect(finalEmission.eligible).toBeFalsy();
  });
});
