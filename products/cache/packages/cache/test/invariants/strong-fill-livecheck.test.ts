// INV-1 — spec
// Sacred: append-only. Never modify, delete, weaken, skip, or .todo an existing test.

import { mkTag, world } from "@astilba/harness";
import { describe, expect, test } from "vitest";

// Invariant 1: once a hard delete has become visible at whatever level a
// read asks for, the entry it removed stops being servable — and `strong` asks to
// see it right away, with no propagation window to wait out. The strong pre-factory check moves
// INV-12/the enforcement to read time and carries it onto the serve leg: a value
// whose birth predates a hard purge the registry already accepted durably must not
// reach the caller, and at `strong` least of all — the design requires that check to be
// live and un-memoized, so there is no window to hide in. The strong-MISS-FILL path
// stores a value whose tag is locally UNKNOWN (the LOCAL write-back fence reads
// only local knowledge, so it passes) — and must run the live check on the SERVE
// leg before returning it, fencing and re-filling with fresh stamps under the
// attempt budget 3 if the born value is dead against the live watermark.

describe("strong post-fill live check (E16 serve leg)", () => {
  test("INV-1: a strong fill never serves a value born against a durable purge the isolate hasn't observed locally", async () => {
    const w = world({ isolates: 1 });
    const { cache } = w.isolate(0);

    // COMPLEMENT (runs first, must stay green under the fix and under a mutation that
    // blinds the serve-leg check): a legitimate strong fill of a NEVER-purged tag is
    // NOT spuriously fenced. The value is served and a repeat strong read agrees — the
    // post-fill live check classifies it fresh and lets it through. Guards against a
    // degenerate "fix" that fences every strong fill.
    const clean = mkTag("clean");
    const cleanFirst = await cache.getOrSet({
      consistency: "strong",
      factory: async () => await Promise.resolve("clean-value"),
      key: "clean-key",
      tags: [clean],
    });
    const cleanSecond = await cache.getOrSet({
      consistency: "strong",
      factory: async () => await Promise.resolve("clean-REFILL-must-not-run"),
      key: "clean-key",
      tags: [clean],
    });
    expect(cleanFirst).toBe("clean-value");
    expect(cleanSecond).toBe("clean-value");

    // THE DISCRIMINATOR. Durably hard-purge `purged` at the registry, but DROP the bus
    // frame so this isolate never learns of it: the tag is dead at the registry
    // (hard watermark epoch 1) yet locally UNKNOWN. The local write-back fence — it
    // reads only local knowledge — therefore passes a value born (epoch 0) against it.
    // Selector is { tag } (singular): a { tags } object would purge nothing.
    const purged = mkTag("purged");
    w.chaos.dropNextFrames(1);
    await cache.delete({ tag: purged });

    // Two strong getOrSets of a NEVER-cached key, with DISTINCT factory values and NO
    // mutation between them. A strong read must never serve the born-dead "first" value:
    // the post-fill live check fences it and re-fills with a fresh stamp (bornEpoch now
    // postdates the purge), so the first call establishes a durably-fresh value the
    // second strong read agrees with — both observe "first". A kernel that skips the
    // serve-leg check serves the born-dead "first", then the second strong read finds
    // that stored value dead and refills "second": two strong reads of an unmutated key
    // DIVERGE ("first" then "second"). That divergence is the failure this test pins.
    const first = await cache.getOrSet({
      consistency: "strong",
      factory: async () => await Promise.resolve("first"),
      key: "k",
      tags: [purged],
    });
    const second = await cache.getOrSet({
      consistency: "strong",
      factory: async () => await Promise.resolve("second"),
      key: "k",
      tags: [purged],
    });
    expect(second).toBe(first);
    expect(second).toBe("first");
  });
});
