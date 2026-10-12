import { mkKey, mkTag, world } from "@astilba/harness";
import { describe, expect, test } from "vitest";

/* References: §5.0, E20, INV-9, INV-12. */

describe("CACHE-32: fence-taught retry restores fill availability", () => {
  test("gapped eventual fill succeeds via retry — exactly 2 fills, 0 registry checks (resync-taught path)", async () => {
    // `unknownPolicy: "miss"` pins the RESYNC-taught path (E21): the gapped isolate stays suspect
    // (its dropped hard-purge frame declared no live-channel head — E21 no longer clears via pointer
    // freshness), so its eventual unknown-tag read learns the purge from the RESYNC (store), never a
    // teaching registry check. Under the default `registry-check` policy the Q4 check would teach
    // `hardEpoch` ahead and the fill would born fresh on attempt 1 (the CHECK-taught path — a valid
    // E21 outcome, but not the fence-taught retry this pins).
    const w = world({ defaults: { unknownPolicy: "miss" }, isolates: 2 });
    const key = mkKey("k");
    const tag = mkTag("t0");
    await w.isolate(0).cache.expire({ tag });
    await w
      .isolate(0)
      .cache.getOrSet({ factory: async () => "a", key, tags: [tag] });
    // The reader serves the entry once via a strong read so its live check records EVERY envelope
    // tag (the implicit key/namespace tags too) into local knowledge — the warm map whose §5.0 fence
    // can classify DEAD (not merely unknown) once the resync teaches the hard purge. Its check runs
    // BEFORE `checksBefore`, so it never counts against the fill's zero-round-trip budget.
    await w.isolate(1).cache.getOrSetEntry({
      consistency: "strong",
      factory: async () => "prime",
      key,
      tags: [tag],
    });
    w.chaos.dropNextFrames(1);
    await w.isolate(0).cache.delete({ tag });
    await w.advance(1);

    // The gapped iso1 fill borns behind the hard delete on attempt 1 and is fenced; the retry
    // re-mints from the now-resynced high-water and refetches, borning fresh. On `main` (no
    // retry loop) attempt 1's fence is terminal ⇒ FencedError. `checks == 0`: the retry re-mints
    // from LOCAL knowledge, and the resync (not a check) teaches the hard purge (§1).
    let fills = 0;
    const checksBefore = w.registry.checkCount;
    const filled = await w.isolate(1).cache.getOrSet({
      factory: async () => {
        fills += 1;
        return "c";
      },
      key,
      tags: [tag],
    });
    expect(filled).toBe("c");
    expect(fills).toBe(2);
    expect(w.registry.checkCount - checksBefore).toBe(0);
  });

  test("check-learned-ahead under blocked resync borns fresh on attempt 1 (contiguous floor suffices)", async () => {
    // The standing certificate for the contiguous-only floor: when a suspect isolate learns the
    // hard via its read's registry check (resync blocked by delayKey), that check advances the
    // contiguous high-water (#recordChecked), so the fill borns FRESH on the first attempt — no
    // fence, no retry. A per-tag `#known` floor would be dead here; contiguous already suffices.
    const w = world({ isolates: 2 });
    const key = mkKey("k");
    const tag = mkTag("t0");
    await w
      .isolate(1)
      .cache.getOrSet({ factory: async () => "warm", key, tags: [tag] });
    w.chaos.delayKey("__state", 1_000_000);
    w.chaos.dropNextFrames(1);
    await w.isolate(0).cache.expire({ tag });
    await w.isolate(0).cache.delete({ tag });
    await w.advance(1);

    let fills = 0;
    const filled = await w.isolate(1).cache.getOrSet({
      factory: async () => {
        fills += 1;
        return "c";
      },
      key,
      tags: [tag],
    });
    expect(filled).toBe("c");
    expect(fills).toBe(1);
  });
});
