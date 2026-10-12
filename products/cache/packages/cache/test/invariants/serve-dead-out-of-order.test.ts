// INV-1 INV-25 — spec §11.1, §4.2, errata E1, E4, E20, E21
// Sacred: append-only. Never modify, delete, weaken, skip, or .todo an existing test.
//
// A hard purge must never be SERVED once durably accepted (§11.1) — not fresh, not stale via
// SWR. Under out-of-order / dropped bus delivery a reader can converge before it has accounted
// for a higher hard purge that a lower frame (arriving after) masks. Convergence therefore
// clears ONLY when every KNOWN loss is accounted: each loss-revealing event declares a head the
// reader must reach (E21) — a `hello`, a `gap` (its `head`), or a NON-CONTIGUOUS frame (its
// `toEpoch`); a contiguous frame declares nothing (it would "converge wherever it stands").
//
// Three arms, each a warm reader (entry born epoch 0, tag strong-primed) that misses a hard
// purge and MUST fail closed — refill, never serve the born-0 value the hard purge killed:
//   • reorder — hard delivered before soft (non-contiguous), soft after; the soft's contiguous
//     apply must not clear the hole the hard revealed.
//   • drop    — the hard frame is dropped (a `gap` whose head ≥ the lost frame's epoch); the
//     later soft frame must not clear it.
//   • composition — three emissions, the hard (highest) dropped, a middle frame reordered ahead,
//     AND the `__state` flush stalled so resync reaches only the middle epoch. The gap's head
//     (the hard epoch) outruns both the reordered frame and the stalled pointer, so suspicion
//     holds while the flush lags. (This arm is the one a per-frame-only anchor lets through.)
//
// RED-PROVEN on this PR's PRE-FIX branch head (the kernel where a contiguous frame self-declares
// and a gap carries no head): all three serve the born-0 value stale. `main` is legitimately
// GREEN (its startup-suspect reader never converges through these traces) — the red-proof target
// is the pre-fix BRANCH, not main.

import { mkTag, world } from "@astilba/harness";
import { describe, expect, test } from "vitest";

type World = ReturnType<typeof world>;
type Tag = ReturnType<typeof mkTag>;

/** Warm the reader: store `k` under `tag` (born epoch 0), then strong-prime so a live check
 * records every envelope tag into local knowledge. The reader is warm and converged. */
async function warmReader(w: World, tag: Tag): Promise<void> {
  await w.isolate(0).cache.getOrSet({
    factory: async () => await Promise.resolve("v1"),
    key: "k",
    tags: [tag],
  });
  await w.advance(0);
  await w.isolate(1).cache.getOrSetEntry({
    consistency: "strong",
    factory: async () => await Promise.resolve("prime"),
    key: "k",
    tags: [tag],
  });
  await w.advance(0);
}

/** Read `k` at eventual consistency; a `miss` policy refills on a fail-closed read. */
async function readEntry(w: World, tag: Tag) {
  return await w.isolate(1).cache.getOrSetEntry({
    factory: async () => await Promise.resolve("v2"),
    key: "k",
    tags: [tag],
  });
}

describe("serve-dead under out-of-order delivery (E21 — C″)", () => {
  test("INV-1/25: reorder — a soft frame arriving after a higher hard frame does not clear the hole (fail closed)", async () => {
    const w = world({ defaults: { unknownPolicy: "miss" }, isolates: 2 });
    const writer = w.isolate(0).cache;
    const tag = mkTag("t0");
    await warmReader(w, tag);

    // Soft (epoch 1) then hard (epoch 2), reordered so the HARD (non-contiguous) is delivered
    // first and the soft (contiguous) second. The reader must stay suspect until it accounts for
    // the hard the non-contiguous frame declared.
    w.bus.delay(1);
    w.bus.reorderNext();
    await writer.expire({ tag });
    await writer.delete({ tag });
    await w.advance(1);

    const entry = await readEntry(w, tag);
    expect(entry.value).not.toBe("v1");
    expect([undefined, "v2"]).toContain(entry.value);
    expect(entry.stale).toBeFalsy();
  });

  test("INV-1/25: drop — a dropped hard frame's gap{head} keeps suspicion until the purge is accounted (fail closed)", async () => {
    const w = world({ defaults: { unknownPolicy: "miss" }, isolates: 2 });
    const writer = w.isolate(0).cache;
    const tag = mkTag("t0");
    await warmReader(w, tag);

    // Soft (epoch 1) delivered; hard (epoch 2) DROPPED — a `gap` whose head ≥ the lost epoch. The
    // later contiguous soft frame must not clear the hole the gap's head declared.
    w.bus.delay(1);
    await writer.expire({ tag });
    w.chaos.dropNextFrames(1);
    await writer.delete({ tag });
    await w.advance(1);

    const entry = await readEntry(w, tag);
    expect(entry.value).not.toBe("v1");
    expect([undefined, "v2"]).toContain(entry.value);
    expect(entry.stale).toBeFalsy();
  });

  test("INV-1/25: composition — dropped-high + reordered-middle + stalled flush; the gap's head outruns the lagging pointer (fail closed)", async () => {
    const w = world({ defaults: { unknownPolicy: "miss" }, isolates: 2 });
    const writer = w.isolate(0).cache;
    const tag = mkTag("t0");
    const other = mkTag("t1");
    await warmReader(w, tag);

    // A (soft `tag`, epoch 1), B (soft `other`, epoch 2), C (hard `tag`, epoch 3). Buffer A and B
    // (reordered so B is delivered ahead of A), then stall `__state` and DROP C — its gap declares
    // head 3. The pointer lags at epoch 2 (A/B flushed before the stall; C's bless is stalled), so
    // resync reaches only applied 2 < the declared head 3: suspicion holds while the flush lags.
    w.bus.delay(100);
    w.bus.reorderNext();
    await writer.expire({ tag }); // A epoch 1
    await writer.expire({ tag: other }); // B epoch 2 (reorder swaps B ahead of A)
    await w.advance(50); // A/B still buffered (deliver at 100); their __state is visible
    w.chaos.delayKey("__state", 60); // stall subsequent __state so C's epoch-3 bless lags
    w.chaos.dropNextFrames(1);
    await writer.delete({ tag }); // C epoch 3 dropped → gap{head=3}
    await w.advance(50); // deliver B then A; read while C's pointer is still stalled

    const entry = await readEntry(w, tag);
    expect(entry.value).not.toBe("v1");
    expect([undefined, "v2"]).toContain(entry.value);
    expect(entry.stale).toBeFalsy();
  });
});
