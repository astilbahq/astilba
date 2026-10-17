// the fill's post-acquire body runs under a `try/finally` that releases the
// lock on EVERY exit path. Previously, `validateUserTags` throwing the count-ceiling error at
// settle-union ran AFTER `maybeLock` acquired the lock and BEFORE the (pre-fence) release, with no
// try/finally over it — so a `lock:true` fill that over-ran the ceiling at settle LEAKED the lock,
// blocking every other isolate's fill of that key until lease expiry. The single finally covers the
// foreground caller AND the background refresh (both route through `fillAndStore`). The `finally`
// releases on ANY throw from the guarded region regardless of throw flavor, so this one ceiling
// (synchronous) throw pins the release property; blinding the finally-release reds it.
//
// Lane: unit (deterministic). FakeLock is non-blocking by construction, so the leak is observed
// DIRECTLY via its `isHeld(key)` accessor — a leaked lock stays held; a released one does not.

import { createCache, InvalidTagError, memory } from "@astilba/cache";
import { FakeLock, mkTag } from "@astilba/harness";
import { describe, expect, it } from "vitest";

// One past the ≤126 user-tag ceiling — the settle-union that trips `validateUserTags`.
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
    // settle-time validation, AFTER `maybeLock` acquired the lock (the exact pre- leak window).
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
