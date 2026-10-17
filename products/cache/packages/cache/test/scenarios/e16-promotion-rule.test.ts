// Strong-read promotion-rule regression
// Sacred: append-only. Never modify, delete, weaken, skip, or .todo an existing test.

import { mkTag, world } from "@astilba/harness";
import { describe, expect, test } from "vitest";

// Shared setup for both cases below: isolate 0 purges a tag whose bus frame is
// dropped before it is ever delivered, so isolate 1 never observes the purge and
// stays locally unknown on that tag. Isolate 1 then fills against the key — the
// write-back fence passes (unknown, not known-dead locally) and stores a value
// born BEHIND the purge ("born-dead"). The fill must NOT promote the tag to
// known-clean on isolate 1: only a verified authority (a contiguously-applied bus
// frame, or a recorded live check) may do that.
async function setupBornDeadFill(): Promise<{
  filler: ReturnType<ReturnType<typeof world>["isolate"]>["cache"];
  tag: ReturnType<typeof mkTag>;
}> {
  const w = world({ isolates: 2 });
  const purger = w.isolate(0).cache;
  const filler = w.isolate(1).cache;
  const tag = mkTag("t0");

  // Drop the purge's bus frame before it is published: a lone dropped frame is
  // never delivered to any subscriber, so isolate 1's gap-detection never fires
  // and it stays NOT suspect (its knowledge is plain-unknown on this tag, not a
  // flagged gap) — the exact "behind but not suspect" precondition the lemma
  // guards.
  w.chaos.dropNextFrames(1);

  await purger.delete({ tag }); // registry hardEpoch bumps; the frame never arrives
  await w.advance(0); // settle microtasks; isolate 1's applied epoch is unchanged

  const filled = await filler.getOrSet({
    factory: async () => "born-dead",
    key: "k",
    tags: [tag],
  });
  // Sanity: the fill actually happened and stored the born-dead value — otherwise
  // the counterexample below would be vacuous.
  expect(filled).toBe("born-dead");

  return { filler, tag };
}

describe("E16 promotion rule", () => {
  test("a fill that passes the write-back fence with an unknown tag does not promote it to known-clean: the storing isolate's eventual re-read refills, not serves born-dead", async () => {
    const { filler, tag } = await setupBornDeadFill();

    const second = await filler.getOrSet({
      consistency: "eventual",
      factory: async () => "refilled-eventual",
      key: "k",
      tags: [tag],
    });

    // If the fill had (wrongly) recorded the unknown tag as known-clean, this
    // second eventual read would classify the stored envelope FRESH off that
    // fabricated record and serve "born-dead" again. The lemma requires the tag
    // stay unknown until a verified authority resolves it, so this read fails
    // closed (unknown ⇒ registry-check/miss) and refills.
    expect(second).not.toBe("born-dead");
    expect(second).toBe("refilled-eventual");
  });

  test("a fill that passes the write-back fence with an unknown tag does not promote it to known-clean: the storing isolate's strong re-read refills, not serves born-dead", async () => {
    const { filler, tag } = await setupBornDeadFill();

    const third = await filler.getOrSet({
      consistency: "strong",
      factory: async () => "refilled-strong",
      key: "k",
      tags: [tag],
    });

    // A strong read always performs a live, un-memoized registry check —
    // independent of whatever the fill did (or didn't) record locally — so it
    // must also refill rather than serve the born-dead value.
    expect(third).not.toBe("born-dead");
    expect(third).toBe("refilled-strong");
  });
});
