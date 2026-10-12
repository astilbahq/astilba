// INV-3  — spec §11.3
// INV-9  — spec §11.9   (race-shaped: fc.scheduler())
// INV-12 — spec §11.12, errata E1   (race-shaped: fc.scheduler())
// INV-17 — spec §11.17, errata E3   (race-shaped: fc.scheduler())
// INV-18 — spec §11.18
// Sacred: append-only. Never modify, delete, weaken, skip, or .todo an existing test.

import { HttpError } from "@astilba/cache";
import { mkTag, world } from "@astilba/harness";
import { test } from "@fast-check/vitest";
import fc from "fast-check";
import { describe, expect } from "vitest";

// A retriable origin failure (503 ∈ the default retriable set, E2) so the
// grace/stale-if-error serve path engages — a plain Error is non-retriable and
// would rethrow before the stored good value is ever consulted.
const retriableFail = async (): Promise<never> => {
  throw new HttpError(new Response(null, { status: 503 }));
};

describe("fill fencing", () => {
  test("INV-3: a failed or timed-out factory never modifies a stored good value", async () => {
    const w = world({ isolates: 1 });
    const { cache } = w.isolate(0);
    const tag = mkTag("t0");
    await cache.getOrSet({
      factory: async () => await Promise.resolve("good"),
      key: "k",
      tags: [tag],
    });
    await cache.expire({ tag }); // mark stale so a refresh runs
    const entry = await cache.getOrSetEntry({
      factory: retriableFail,
      grace: "1h",
      key: "k",
      tags: [tag],
    });
    // The failed factory is a no-op on the stored good value; grace serves it.
    expect(entry.value).toBe("good");
    expect(entry.servedOnError).toBeTruthy();
  });

  test.prop([fc.scheduler()])(
    "INV-9: a fill begun before a visible purge cannot produce an entry that escapes it",
    async (s) => {
      const w = world({ isolates: 1 });
      const { cache } = w.isolate(0);
      const tag = mkTag("t0");
      const fillP = cache.getOrSet({
        factory: async () =>
          await s.schedule(Promise.resolve("born-before-purge"), "factory"),
        key: "k",
        tags: [tag],
      });
      const purgeP = cache.delete({ tag });
      await s.waitIdle();
      await Promise.allSettled([fillP, purgeP]);
      const readback = await cache.getOrSet({
        consistency: "strong",
        factory: async () => await Promise.resolve("fresh"),
        key: "k",
        tags: [tag],
      });
      // The fenced fill cannot escape the purge; a strong read refills fresh.
      expect(readback).toBe("fresh");
    }
  );

  test.prop([fc.scheduler()])(
    "INV-12: a hard delete landing mid-fill fences the write-back via a live registry read (E1)",
    async (s) => {
      const w = world({ isolates: 1 });
      const { cache } = w.isolate(0);
      const tag = mkTag("t0");
      const fillP = cache.getOrSet({
        factory: async () =>
          await s.schedule(Promise.resolve("mid-fill"), "factory"),
        key: "k",
        tags: [tag],
      });
      const purgeP = cache.delete({ tag });
      await s.waitIdle();
      const [fill] = await Promise.allSettled([fillP, purgeP]);
      // The accepted result postdates the purge, or the fill fails within budget —
      // a fenced write-back is never stored and served as fresh.
      const entry = await cache.getOrSetEntry({
        consistency: "strong",
        factory: async () => await Promise.resolve("fresh"),
        key: "k",
        tags: [tag],
      });
      expect(entry.value).toBe("fresh");
      expect(fill.status).toBeDefined();
    }
  );

  test.prop([fc.scheduler()])(
    "INV-17: a write-back never replaces a strictly higher (bornEpoch, fence); bornMs never arbitrates (E3)",
    async (s) => {
      const w = world({ isolates: 2 });
      const tag = mkTag("t0");
      // Isolate 0 is the stale lease-holder: skew its clock +15s so it stamps a
      // strictly HIGHER bornMs. It starts (and locks) first, so it holds the LOWER
      // fencing token; its write-back is deferred so it lands LAST.
      w.chaos.skewClock(0, 15_000);
      const staleLease = w.isolate(0).cache.getOrSet({
        factory: async () =>
          await s.schedule(Promise.resolve("stale-lease"), "stale"),
        key: "k",
        lock: true,
        tags: [tag],
      });
      // Isolate 1 is fresher: normal clock (LOWER bornMs), locks second (HIGHER
      // fencing token), completes first.
      const fresher = w.isolate(1).cache.getOrSet({
        factory: async () => await Promise.resolve("fresher"),
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
      // Born-monotone by (bornEpoch, fence): the stale lease-holder's late write-back
      // carries the higher bornMs but the lower fence, so it is rejected; bornMs is
      // never the arbiter. A kernel that ordered on bornMs would wrongly keep "stale-lease".
      expect(entry.value).toBe("fresher");
      expect(entry.value).not.toBe("stale-lease");
    }
  );

  test.prop([fc.scheduler()])(
    "INV-17: the higher fence wins even when it writes LAST and carries a lower bornMs (E3)",
    async (s) => {
      const w = world({ isolates: 2 });
      const tag = mkTag("t0");
      /* References: INV-17, E3, INV-9, INV-12. */

      w.chaos.skewClock(0, 15_000);
      const firstLowFence = w.isolate(0).cache.getOrSet({
        factory: async () => await Promise.resolve("first-lowfence"),
        key: "k",
        lock: true,
        tags: [tag],
      });
      // Isolate 1 locks SECOND (HIGHER fence), keeps the normal clock (LOWER bornMs), and
      // writes LAST — its write-back is deferred to the scheduler so it lands after
      // isolate 0's.
      const lastHighFence = w.isolate(1).cache.getOrSet({
        factory: async () =>
          await s.schedule(Promise.resolve("last-highfence"), "high"),
        key: "k",
        lock: true,
        tags: [tag],
      });
      await s.waitIdle();
      await Promise.allSettled([firstLowFence, lastHighFence]);
      const entry = await w.isolate(1).cache.getOrSetEntry({
        consistency: "strong",
        factory: async () => await Promise.resolve("refill"),
        key: "k",
        tags: [tag],
      });
      // Fence — not arrival order, not bornMs — is the sole arbiter. The higher fence
      // arrived LAST and carries the LOWER bornMs, yet it must win: a first-writer-wins
      // kernel keeps "first-lowfence" and FAILS this; a bornMs kernel keeps the +15s
      // "first-lowfence" and FAILS this. Paired with the case above (higher fence writes
      // FIRST, which fails last-writer-wins), this brackets BOTH arrival-order heuristics.
      expect(entry.value).toBe("last-highfence");
      expect(entry.value).not.toBe("first-lowfence");
    }
  );

  test("INV-18: the graced value served on the error path is re-validated at serve time", async () => {
    const w = world({ isolates: 1 });
    const { cache } = w.isolate(0);
    const tag = mkTag("t0");
    await cache.getOrSet({
      factory: async () => await Promise.resolve("graced"),
      grace: "1h",
      key: "k",
      tags: [tag],
    });
    await cache.expire({ tag });
    await cache.delete({ tag }); // a hard delete during the fill must not resurrect via the error path
    const entry = await cache.getOrSetEntry({
      factory: retriableFail,
      grace: "1h",
      key: "k",
      tags: [tag],
    });
    // Re-validated at serve time: a hard-deleted value is not served even on the error path.
    expect(entry.value).not.toBe("graced");
  });
});
