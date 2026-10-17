// classify's DEAD edge is the STRICT `hardEpoch > bornEpoch`
// (invalidation.ts). The whole before-initiation anchor rides on that strictness — a strong fill
// whose bornEpoch is minted AT a purge's epoch (born == hard) is FRESH, because the pre-factory
// born-mint linearizes the fill at/after that purge. A `>=` "hardening" would instead
// fence every born==hard value and drive it into re-mint-and-fence exhaustion. This tooth pins the
// equality boundary directly (an ORDINARY fakes-lane regression, not sacred).
//
//   `delete` raises hard[tag] to epoch N and advances the global epoch to N; the immediately-
//   following STRONG fill's pre-factory check mints born = throughEpoch = N (== hard). Strict
//   classify(born=N, hard=N): N > N is FALSE ⇒ NOT dead ⇒ the value is stored and served on the
//   FIRST attempt (fills === 1 — no fence, no retry).
//   Under the `>=` mutation: N >= N is TRUE ⇒ dead ⇒ the fence fires; the fence-taught retry
//   re-mints born from the local high-water (also N) ⇒ cannot advance ⇒ terminal FencedError
//   (typed exhaustion) ⇒ this reds.
//
// Fakes lane, deterministic (no wall-clock, no fast-check).

import { mkKey, mkTag, world } from "@astilba/harness";
import { describe, expect, test } from "vitest";

describe("E26: classify's strict `hardEpoch > bornEpoch` — a born==hard value serves ONCE", () => {
  test("a strong fill whose bornEpoch EQUALS a hard-purge epoch is fresh (fills === 1, no exhaustion)", async () => {
    const w = world({ isolates: 1 });
    const key = mkKey("k");
    const tag = mkTag("t0");
    // Hard-purge the tag FIRST: the global epoch advances to N and hard[tag] = N. No entry exists
    // yet — this only raises the watermark, it births nothing.
    await w.isolate(0).cache.delete({ tag });
    // A strong fill of a never-cached key tagged `tag`. Its pre-factory check mints born = the global
    // epoch = N, EQUAL to hard[tag] = N (and records hard[tag]=N into local knowledge). Strict `>`
    // classifies fresh, so the write-back fence passes and the value is served on the first attempt.
    let fills = 0;
    const filled = await w.isolate(0).cache.getOrSet({
      consistency: "strong",
      factory: async () => {
        fills += 1;
        return await Promise.resolve("v");
      },
      key,
      tags: [tag],
    });
    expect(filled).toBe("v");
    // born == hard ⇒ strict `>` is false ⇒ exactly ONE fill. A `>=` hardening fences the born==hard
    // value ⇒ the retry cannot advance the born floor ⇒ terminal FencedError ⇒ this assertion is
    // never reached (the await rejects), redding the tooth.
    expect(fills).toBe(1);
  });
});
