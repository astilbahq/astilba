// INV-1 INV-2 — spec
// Sacred: append-only. Never modify, delete, weaken, skip, or .todo an existing test.

import { mkTag, world } from "@astilba/harness";
import { describe, expect, test } from "vitest";

// `strong` forgoes SWR — it never serves a stale value. A strong read runs one
// live un-memoized registry check and observes every durably-accepted purge before it began.
// The strong-MISS-FILL path is check-then-fetch: the live check runs BEFORE the
// factory, so bornEpoch is minted from now-current knowledge and the born value postdates
// every purge the check saw — hard OR soft. This pins the SOFT axis a hard-only fence misses:
// a gapped soft purge (durably accepted at the registry, bus frame dropped) must not leave a
// strong miss serving a stale-born value that the next strong read then refills.

describe("strong-miss fill is check-then-fetch: soft-purge idempotence (E18)", () => {
  test("INV-1 INV-2: a strong miss on a gapped soft-purged tag borns fresh; two strong reads of an unmutated key agree", async () => {
    const w = world({ isolates: 2 });
    const reader = w.isolate(1).cache;
    const t = mkTag("t0");

    // Soft-expire `t` on iso0 while the next bus frame is DROPPED: the soft purge is durably
    // accepted (registry soft watermark advances) yet the reader iso1 never learns it — its
    // local knowledge is gapped exactly as the counterexample (drop + expire). The
    // key `k` was NEVER cached, so both reads below are full misses that FILL.
    w.chaos.dropNextFrames(1);
    await w.isolate(0).cache.expire({ tag: t });

    // Two strong getOrSets of the never-cached key `k`, DISTINCT sentinels, NO mutation
    // between. Check-then-fetch borns "first" at a bornEpoch postdating the soft purge (from
    // the pre-fetch live check), so it serves FRESH and stable; read2 hits it, live-checks it
    // fresh, and agrees. A kernel that fetches-then-checks (or fences hard-only) borns "first"
    // STALE, serves it, then read2 finds it stale and refills "second" — the two strong reads
    // of an unmutated key DIVERGE. The design forbids the stale-born serve; that divergence is pinned.
    const first = await reader.getOrSet({
      consistency: "strong",
      factory: async () => await Promise.resolve("first"),
      key: "k",
      tags: [t],
    });
    const second = await reader.getOrSet({
      consistency: "strong",
      factory: async () => await Promise.resolve("second"),
      key: "k",
      tags: [t],
    });
    expect(second).toBe(first);
    expect(second).toBe("first");
  });

  test("INV-1: a gapped-stale strong miss performs exactly one fill and one registry check (E1)", async () => {
    // The design claims are testable: check-then-fetch fetches ONCE (a double-fetch fix would
    // fill twice) and checks ONCE (the budget — a kept post-fill check would check twice).
    const w = world({ isolates: 2 });
    const reader = w.isolate(1).cache;
    const t = mkTag("t0");
    w.chaos.dropNextFrames(1);
    await w.isolate(0).cache.expire({ tag: t });

    let fills = 0;
    const checksBefore = w.registry.checkCount;
    const served = await reader.getOrSet({
      consistency: "strong",
      factory: async () => {
        fills++;
        return await Promise.resolve("v");
      },
      key: "k",
      tags: [t],
    });
    expect(served).toBe("v");
    expect(fills).toBe(1);
    expect(w.registry.checkCount - checksBefore).toBe(1);
  });
});
