/* References: E25. */

import { createCache, InvalidTagError, memory } from "@astilba/cache";
import { FakeLock, mkTag } from "@astilba/harness";
import { describe, expect, it } from "vitest";

// One past MI13's ≤126 user-tag ceiling (E24) — the settle-union that trips `validateUserTags`.
const OVER_CEILING = Array.from({ length: 127 }, (_, i) => mkTag(`t:${i}`));

describe("E25 defect B — a settle-time throw releases the fill's lock", () => {
  it("a lock:true fill whose settle-union exceeds 126 throws AND releases the lock (no leak)", async () => {
    const lock = new FakeLock();
    const cache = createCache({
      clock: { now: (): number => 0 },
      l2: memory(),
      lock,
      namespace: "e25b",
      rng: { next: (): number => 0 },
    });
    const key = "e25b/over-ceiling";

    // The factory succeeds, then `setTags` authors an over-ceiling union — so the throw lands at
    // settle-time validation, AFTER `maybeLock` acquired the lock (the exact pre-E25 leak window).
    await expect(
      cache.getOrSet({
        factory: async (ctx) => {
          ctx.setTags(OVER_CEILING);
          return await Promise.resolve("v");
        },
        key,
        lock: true,
      })
    ).rejects.toBeInstanceOf(InvalidTagError);

    // The finally released it — the next caller of this key is not blocked behind a leaked lease.
    expect(lock.isHeld(key)).toBeFalsy();
  });
});
