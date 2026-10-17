// the LOCAL write-back fence, DELIVERED-purge arm. A strong fill
// whose mid-factory `dependsOn` tag carries a hard purge DELIVERED into local knowledge during the
// fill must fence the born value and re-fetch — it must never serve the born-dead value. An earlier change had
// added a post-settle union-delta live-check for a DIFFERENT (durably-accepted-but-undelivered)
// case; a later change reverted that (the strong pre-factory born-mint already dominates every purge accepted
// BEFORE initiation, so the delta check only ever touched during-fill concurrent purges — legal to
// miss under either linearization). This tooth pins the arm the surviving fence DOES own, and which
// had no tooth of its own before: a during-fill purge DELIVERED locally.
//
// Arithmetic witness (own words), born = fill's minted bornEpoch, hard = tag's local hardEpoch:
//   before-init:  purge@N ⇒ epoch→N, hard=N; strong init mints born=N; classify(born=N,hard=N):
//                 N>N is false ⇒ not dead ⇒ serve (legal + fresh). The pre-factory anchor, no fence.
//   during-fill DELIVERED  [THIS TOOTH]: pre-factory mints born=0 (idle registry); the factory
//                 hard-purges the dependency ⇒ epoch→1 AND the frame is DELIVERED locally
//                 (FakeRegistry window-0 flush → 0-delay FakeBus → this isolate's knowledge,
// synchronously); classify(born=0,hard=1): 1>0 ⇒ dead ⇒ fence fires ⇒ the
//                 fence-taught retry re-mints born=1 from the now-advanced local high-water and
//                 refetches ⇒ serves 'second'.
//   during-fill UNDELIVERED: born=0; purge@1 not delivered ⇒ tag locally UNKNOWN ⇒ fence passes ⇒
//                 'first' is served — a LEGAL concurrent linearization (either order is legal), an
//                 untoothable tautology: documented here, NOT asserted.
//
// Lane: integration folder, but DETERMINISTIC. The FakeRegistry+FakeBus twin delivers the purge
// SYNCHRONOUSLY into local knowledge — the only way to place it in the fence's view BEFORE the
// write-back check, which runs the instant the factory returns (a real socket would race the
// fence). No wall-clock, no frame-timing race ⇒ no timing tolerance despite the lane.

import { createCache, memory, t } from "@astilba/cache";
import { FakeBus, FakeRegistry, SimClock } from "@astilba/harness";
import { describe, expect, it } from "vitest";

describe("E26 — a strong fill fences a during-fill purge DELIVERED locally, then re-fetches", () => {
  it("the born value is fenced by the local write-back fence; the fence-taught retry serves the refetch", async () => {
    const clock = new SimClock(0);
    const bus = new FakeBus(clock);
    // A FakeRegistry wired to the SAME bus: its window-0 flush broadcasts each purge frame, which
    // the 0-delay bus delivers into this isolate's knowledge synchronously (delivery ≠ truth, but
    // here it IS delivered — that is the arm under test). No mirror store needed: the broadcast
    // fires regardless, and no gap/resync arises on the contiguous single frame.
    const registry = new FakeRegistry(clock, { bus });
    const cache = createCache({
      bus,
      clock: { now: (): number => 0 },
      l2: memory(),
      namespace: "e26",
      registry,
      rng: { next: (): number => 0 },
    });

    const purged = t`purged`;

    // First strong fill of a NEVER-cached key. The pre-factory check mints born=0 (idle registry).
    // The factory hard-purges `purged` DURING the fill (epoch→1) — delivered into local knowledge
    // synchronously, so `purged` is locally KNOWN-dead (hard=1) at the write-back fence. A
    // single-shot guard purges once so the fence-taught retry re-fetches cleanly (no re-purge ⇒ no
    // exhaustion). The factory returns a distinct value per invocation so the SERVED value reveals
    // which attempt won: 'first' (attempt 0, fenced) vs 'second' (retry, stored).
    let calls = 0;
    let purgedOnce = false;
    const served = await cache.getOrSet({
      consistency: "strong",
      factory: async (ctx) => {
        calls += 1;
        if (!purgedOnce) {
          purgedOnce = true;
          await cache.delete({ tag: purged });
        }
        ctx.dependsOn(purged);
        return await Promise.resolve(calls === 1 ? "first" : "second");
      },
      key: "k",
    });

    // classify(born=0, hard=1): 1>0 ⇒ dead ⇒ attempt 0 is fenced ⇒ the retry re-mints born=1 and
    // refetches ⇒ 'second'. Blinding the write-back fence (fenceWriteBack always true) stores and
    // serves 'first' on attempt 0 (calls===1) ⇒ this reds.
    expect(served).toBe("second");
    expect(calls).toBe(2);
  });
});
