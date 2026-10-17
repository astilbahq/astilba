// INV-11 — spec
// INV-25 — spec
// Sacred: append-only. Never modify, delete, weaken, skip, or .todo an existing test.
//
// A suspect reader must not re-trust its warm map because a pointer's freshness field moved
// forward. The trace: a warm reader (entry born, tag strong-primed) misses a HARD purge
// whose bus frame is DROPPED (gap ⇒ suspect); the purge's own __state bless is held behind
// replication lag, so the reader keeps reading the OLDER pointer (its epoch still pre-purge); an
// liveness heartbeat then rewrites that older pointer with a higher `flushedAt`, so two successive reads
// see strictly-increasing freshness on a pointer whose epoch never advanced. The reader must count
// that freshness motion as no evidence it caught up — it stays suspect and refills, never serving
// the value the hard purge killed.
//
// Red-proven on `main` (whose freshness-advancement convergence clears here and serves the dead
// value); green with live-head convergence (the gap-opened episode declares no live-channel head the heartbeat can
// satisfy, so suspicion is sticky and the read fails closed).

import { mkTag, world } from "@astilba/harness";
import { describe, expect, test } from "vitest";

describe("suspicion gate (E21 — convergence anchors to the live channel)", () => {
  test("INV-25: a heartbeat-freshened PRE-PURGE pointer never clears a gap-skipped hard purge — the reader stays fail-closed (E21)", async () => {
    const w = world({
      defaults: { unknownPolicy: "miss" },
      isolates: 2,
      replicas: 2,
    });
    const writer = w.isolate(0).cache;
    const reader = w.isolate(1).cache;
    const tag = mkTag("t0");

    // Warm epoch 1 (delivered soft purge) and born the entry AT epoch 1 (locally fresh).
    await writer.expire({ tag });
    await w.advance(0);
    await writer.getOrSet({
      factory: async () => await Promise.resolve("v1"),
      key: "k",
      tags: [tag],
    });
    await w.advance(0);
    // The reader serves the entry once via a strong read: its live check records EVERY tag it
    // carries, so its local map is complete and warm (soft 1, hard 0 for t0).
    await reader.getOrSetEntry({
      consistency: "strong",
      factory: async () => await Promise.resolve("prime"),
      key: "k",
      tags: [tag],
    });

    // Stall the __state pointer so the hard purge's bless (epoch 2) stays invisible and the
    // reader keeps reading the PRE-PURGE (epoch-1) pointer through the reads below. The soft
    // flush already wrote an epoch-1 pointer (flushedAt 0); an heartbeat then writes a SECOND
    // epoch-1 pointer with a higher flushedAt — the "advancing pre-purge versions" of.
    w.chaos.delayKey("__state", 10_000);
    await w.advance(1000);
    await w.registry.heartbeat();

    // Hide a HARD purge at epoch 2: drop its bus frame (gap ⇒ suspect, new episode) — the reader
    // never learns it over the live channel, and its epoch-2 __state bless is stalled by the
    // delay above so the reader keeps reading the epoch-1 pointer (whose lastBatch omits the
    // hard-purge delta — the gap is never filled from the store either).
    w.chaos.dropNextFrames(1);
    await w.advance(1000);
    await writer.delete({ tag });

    let fills = 0;
    const factory = async (): Promise<string> => {
      fills += 1;
      return await Promise.resolve(`refill-${fills}`);
    };

    // Read #1 (clock at the pre-purge pointer's first visibility): suspect with no live-channel
    // head declared this episode ⇒ fails closed ⇒ 'miss' refills (refill-1).
    await w.advance(8000);
    const first = await reader.getOrSetEntry({
      factory,
      key: "k",
      tags: [tag],
    });
    expect(first.value).toBe("refill-1");

    // The heartbeat's freshened pre-purge pointer now becomes visible: its `flushedAt` is
    // strictly greater than read #1's. Under the SUPERSEDED rule this freshness motion cleared
    // suspicion; with live-head convergence it is not an anchor at all.
    await w.advance(1000);
    const second = await reader.getOrSetEntry({
      factory,
      key: "k",
      tags: [tag],
    });
    // no head was declared over the live channel this episode (the frame dropped; the
    // heartbeat carries none), so the reader stays suspect and refills AGAIN — it never re-trusts
    // the warm-but-stale map, and never serves a value minted against the skipped hard purge.
    expect(second.value).toBe("refill-2");
    expect(second.stale).toBeFalsy();
    expect(fills).toBe(2);
    // Belt: the hard-deleted original is never served either.
    expect(second.value).not.toBe("v1");
  });
});
