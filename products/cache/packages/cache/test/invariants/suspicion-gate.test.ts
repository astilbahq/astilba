// INV-11 — spec
// INV-25 — spec
// Sacred: append-only. Never modify, delete, weaken, skip, or .todo an existing test.
//
// The suspicion gate hardens INV-25/INV-11 — a gap or reconnect that may have hidden a
// durable purge holds the isolate fail-closed until it can PROVE it caught up. Two rules
// the traces below pin:
//   (i) whenever the suspect flag stands, an eventual read treats every tag as unknown
//       and resolves it by unknownPolicy (miss here) — never off local watermarks,
// however complete they look. Stricter than the allowance to keep serving on a
//       merely-degraded map (one degraded by a failed poll, but with no known gap).
//   (ii) the flag drops only once the isolate replays contiguously up to the pointer's
//       epoch AND sees the pointer's freshness move forward between two separate reads.
//       One read, or a pointer parked at/behind the applied epoch, settles nothing — the
//       store is free to answer a read from a lagging replica.
//
// The reachable pre-gate hole needs a WARM isolate: one that already served the entry once,
// so a live check recorded every tag it carries (the implicit key/namespace tags too).
// Its complete-but-stale map would wave a since-purged value through as fresh if a suspect
// gap let it skip rule (i). The strong prime below builds that map before the purge hides.

import { mkTag, world } from "@astilba/harness";
import { describe, expect, test } from "vitest";

describe("suspicion gate (E20)", () => {
  test("INV-25: a warm isolate whose skipped purge left the pointer unreadable refills instead of serving the dead value (E20 i)", async () => {
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
    // The reader serves the entry once: a live check records EVERY tag it carries, so
    // its local map is now complete and warm (the reachable pre- hole).
    await reader.getOrSetEntry({
      consistency: "strong",
      factory: async () => await Promise.resolve("prime"),
      key: "k",
      tags: [tag],
    });
    // Hide a hard purge at epoch 2: drop its bus frame (gap ⇒ suspect) and stall the
    // __state pointer so resync cannot read it (the cold protocol, tail).
    w.chaos.dropNextFrames(1);
    w.chaos.delayKey("__state", 60_000);
    await writer.delete({ tag });
    await w.advance(6000);
    const entry = await reader.getOrSetEntry({
      factory: async () => await Promise.resolve("v2"),
      key: "k",
      tags: [tag],
    });
    // Suspect ⇒ every tag unknown ⇒ 'miss' fails closed; the warm-but-stale map must NOT
    // serve the hard-deleted v1 as fresh.
    expect(entry.value).not.toBe("v1");
    expect([undefined, "v2"]).toContain(entry.value);
    expect(entry.stale).toBeFalsy();
  });

  test("INV-25: a warm isolate that reads a pointer parked at its own applied epoch stays fail-closed, not fresh (E20 ii)", async () => {
    const w = world({
      defaults: { unknownPolicy: "miss" },
      isolates: 2,
      replicas: 2,
    });
    const writer = w.isolate(0).cache;
    const reader = w.isolate(1).cache;
    const tag = mkTag("t0");
    await writer.expire({ tag });
    await w.advance(0);
    await writer.getOrSet({
      factory: async () => await Promise.resolve("v1"),
      key: "k",
      tags: [tag],
    });
    await w.advance(0);
    await reader.getOrSetEntry({
      consistency: "strong",
      factory: async () => await Promise.resolve("prime"),
      key: "k",
      tags: [tag],
    });
    await w.advance(50_000);
    // A hard purge at epoch 2 whose frame is dropped (gap ⇒ suspect); its later __state
    // bless is held behind replication lag, so the reader still reads the STALE epoch-1
    // pointer (epoch 1 ≤ its applied 1). A pointer that has not moved forward — and that
    // the store may serve from a lagging replica — is no proof the purge was seen.
    w.chaos.dropNextFrames(1);
    w.chaos.delayKey("__state", 10_000);
    await writer.delete({ tag });
    const entry = await reader.getOrSetEntry({
      factory: async () => await Promise.resolve("v2"),
      key: "k",
      tags: [tag],
    });
    expect(entry.value).not.toBe("v1");
    expect([undefined, "v2"]).toContain(entry.value);
    expect(entry.stale).toBeFalsy();
  });

  test("INV-25: once the pointer's freshness visibly moves forward, the isolate re-trusts its warm map and serves locally (E20 ii)", async () => {
    const w = world({
      defaults: { unknownPolicy: "miss" },
      isolates: 2,
      replicas: 2,
    });
    const writer = w.isolate(0).cache;
    const reader = w.isolate(1).cache;
    const tag = mkTag("t0");
    const other = mkTag("t1");
    // Warm epoch 1 (delivered) + born the entry at epoch 1 (locally fresh). No hard purge on
    // `tag` — the only obstacle to warm trust is a suspect episode the reader must resolve.
    await writer.expire({ tag });
    await w.advance(0);
    await writer.getOrSet({
      factory: async () => await Promise.resolve("seed"),
      key: "k",
      tags: [tag],
    });
    await w.advance(0);
    // A strong prime records every tag the entry carries WITHOUT resyncing (strong never
    // touches suspicion), so the map is complete and warm.
    await reader.getOrSetEntry({
      consistency: "strong",
      factory: async () => await Promise.resolve("prime"),
      key: "k",
      tags: [tag],
    });

    let fills = 0;
    const factory = async (): Promise<string> => {
      fills += 1;
      return await Promise.resolve(`refill-${fills}`);
    };

    // Semantic amendment. The convergence trigger moves
    // from pointer freshness (forgeable by the heartbeat) to the live-channel
    // reconnect handshake (reset→hello{head}) + verified catch-up; the assertion (fail-closed once,
    // then reuse after convergence) is unchanged. Warrant: R. Morris (comprehension exchange per
    // ADR-004-B) · applied by Claude Code under that warrant.
    // Re-anchor to the live-channel handshake. Stall the pointer, disconnect, and miss an
    // unrelated soft purge while disconnected (its durable bless lands but lags); a reconnect then
    // emits reset -> hello{head}, declaring a head the reader has NOT yet reached — so it stays
    // suspect until it can resync up to that head.
    w.chaos.delayKey("__state", 100);
    w.chaos.disconnect();
    await writer.expire({ tag: other });
    w.chaos.reconnect();
    await w.advance(0);

    // Read #1: suspect, and the stalled pointer keeps the reader from resyncing up to the declared
    // head — so it fails closed and 'miss' refills rather than serving the warm value.
    const first = await reader.getOrSetEntry({
      factory,
      key: "k",
      tags: [tag],
    });
    expect(first.value).toBe("refill-1");

    // The stalled pointer catches up: the reader resyncs the durable prefix up to the head the
    // reconnect's hello declared (appliedEpoch >= head) and re-trusts — the honest live-head convergence.
    await w.advance(100);

    // Read #2: converged ⇒ warm trust restored ⇒ `tag` classifies fresh from LOCAL knowledge with
    // no refill (the factory does not run again — the cached refill-1 is served, never refill-2).
    const second = await reader.getOrSetEntry({
      factory,
      key: "k",
      tags: [tag],
    });
    expect(second.value).toBe("refill-1");
    expect(second.stale).toBeFalsy();
    expect(fills).toBe(1);
  });
});
