// INV-23 — spec §11.23, §2.2, MI1
// Sacred: append-only. Never modify, delete, weaken, skip, or .todo an existing test.
//
// The existing INV-23 guard (keys-tags.test.ts) is VACUOUS as a test of clear()'s
// mechanism: it applies a 5000ms `delayReplica` and only `advance(1000)`s, so the
// reader's own replica view never becomes visible to isolate 0's pre-clear write in
// the first place — the assertion (never serve "v1") passes even against a no-op
// `clear()`, because the replica delay alone hides "v1" from the reader. Nothing in
// that test actually exercises the `__ns` hard-purge machinery.
//
// This guard closes that gap. Two isolates share the SAME L2 replica (no
// `delayReplica`, no `advance`), so isolate B's `strong` read genuinely OBSERVES
// isolate A's pre-clear value in the shared store — the read is non-vacuous. The
// per-instance `nsVersion` counter that `clear()` also bumps (§2.2, MI1) is scoped
// to isolate A alone (`class NsVersion` in cache.ts is instantiated once per
// `createCache()` — i.e. once per isolate, never shared) and so cannot be what
// stops isolate B, since B's canonical key is built from ITS OWN (unbumped)
// nsVersion and is therefore identical to the pre-clear key. The ONLY defense that
// can prevent B from serving stale here is the reserved `__ns:<namespace>` tag's
// hard purge, applied through the shared registry and observed via B's `strong`
// live check (§4.1, §11.18 fencing path) — this isolates that mechanism as the
// thing under test.

import { mkTag, world } from "@astilba/harness";
import { describe, expect, it } from "vitest";

describe("ns-clear guard (__ns hard purge)", () => {
  it("INV-23: a different isolate's strong read never serves a pre-clear value once clear() lands, even though it can see the shared L2 write", async () => {
    // A single shared L2 replica (replicas: 1): isolate A and isolate B read/write
    // through the SAME store view with zero visibility delay, so B's read of A's
    // write is immediate and un-hidden by any chaos — the opposite setup from the
    // vacuous guard, which relies on a delay to hide the write.
    const w = world({ isolates: 2, replicas: 1 });
    const isolateA = w.isolate(0).cache;
    const isolateB = w.isolate(1).cache;
    const tag = mkTag("ns-clear-guard");

    await isolateA.getOrSet({
      factory: async () => await Promise.resolve("pre-clear-value"),
      key: "shared-key",
      tags: [tag],
    });

    // Sanity: B can, in fact, see A's write through the shared L2 — proves the
    // scenario is non-vacuous BEFORE clear() ever runs. If clear() were skipped
    // entirely, B must observe "pre-clear-value" here.
    const beforeClear = await isolateB.getOrSetEntry({
      consistency: "strong",
      factory: async () => await Promise.resolve("would-be-refill"),
      key: "shared-key",
      tags: [tag],
    });
    expect(beforeClear.value).toBe("pre-clear-value");

    await isolateA.clear(); // nsVersion bump (A-local only) + __ns hard purge (shared).

    // B's canonical key is built from B's OWN nsVersion counter, which A's clear()
    // never touches — so if the version bump were the only defense, B would still
    // resolve the identical pre-clear canonical key and read "pre-clear-value"
    // straight through. Only the __ns hard purge, visible to B's live registry
    // check under `strong` consistency, can fence this read.
    const afterClear = await isolateB.getOrSetEntry({
      consistency: "strong",
      factory: async () => await Promise.resolve("post-clear-refill"),
      key: "shared-key",
      tags: [tag],
    });
    expect(afterClear.value).toBe("post-clear-refill");
    expect(afterClear.value).not.toBe("pre-clear-value");
  });
});
