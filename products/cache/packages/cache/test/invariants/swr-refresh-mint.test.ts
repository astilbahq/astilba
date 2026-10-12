// INV-30 — spec §5.0 (the SWR background refresh's mint; INV-9's converse direction)
// Sacred: append-only. Never modify, delete, weaken, skip, or .todo an existing test.
//
// §5.0 names the SWR background refresh among the paths that stamp a newly minted value with the
// epoch applied at FILL START. INV-9 pins one direction of that stamp — a fill that began before a
// visible purge must not escape it. This pins the other: a refresh that begins AFTER a purge must
// not be TRAPPED by it. A refresh minting from the stale value's own envelope re-births the value
// behind the very purge that made it stale, so the refreshed copy is stale at birth and the cache
// can never converge — it re-refreshes on every read, forever, while serving a value it just
// replaced.
//
// The two directions are one rule and need each other's teeth: a mint that only ever under-states
// satisfies INV-9 and starves; a mint that only ever over-states converges and serves purged
// values. The cases below bracket the live one from both sides.

import { mkTag, world } from "@astilba/harness";
import { test } from "@fast-check/vitest";
import { describe, expect } from "vitest";

describe("SWR refresh mint (§5.0)", () => {
  test("INV-30: a refreshed value is stamped with the CURRENT applied epoch, so a purge that landed BEFORE the refresh cannot strand it stale at birth", async () => {
    const w = world({ isolates: 1 });
    const { cache } = w.isolate(0);
    const tag = mkTag("t0");
    let fills = 0;
    const factory = async (): Promise<string> => {
      fills += 1;
      return await Promise.resolve(`v${String(fills)}`);
    };

    await cache.getOrSet({ factory, key: "k", tags: [tag] });

    // A soft purge lands: the stored value is stale from here on, and the applied epoch advances
    // past the epoch `v1` was born at.
    await cache.expire({ tag });

    // §5.1 step 6 — the stale value is served, and a refresh runs for NEXT time. The SERVED value
    // is always the stale one, so this arm says nothing about the mint; it only sets it up. The
    // `fills` term is the non-vacuity check: without it the pin below could pass on a kernel that
    // never refreshed at all.
    const swr = await cache.getOrSetEntry({ factory, key: "k", tags: [tag] });
    expect({ fills, stale: swr.stale, value: swr.value }).toStrictEqual({
      fills: 2,
      stale: true,
      value: "v1",
    });

    // THE PIN. `v2` was minted while the purge was already applied, so the purge does not reach
    // it: the next read finds it FRESH and serves it with no further refresh. Minting from the
    // stale envelope instead stamps `v2` at `v1`'s epoch, which the same purge still dominates —
    // `v2` lands already-stale and this read is another SWR serve (`stale: true`, a third factory
    // run), never a fresh one. All three terms are asserted together because they are one claim:
    // the refreshed value is fresh, it is the value the refresh produced, and nothing refreshed
    // again.
    const next = await cache.getOrSetEntry({ factory, key: "k", tags: [tag] });
    expect({ fills, stale: next.stale, value: next.value }).toStrictEqual({
      fills: 2,
      stale: false,
      value: "v2",
    });
  });

  test("INV-30 (complement): the refreshed value is not born immune — a purge landing AFTER the refresh still makes it stale", async () => {
    const w = world({ isolates: 1 });
    const { cache } = w.isolate(0);
    const tag = mkTag("t0");
    let fills = 0;
    const factory = async (): Promise<string> => {
      fills += 1;
      return await Promise.resolve(`v${String(fills)}`);
    };

    await cache.getOrSet({ factory, key: "k", tags: [tag] });
    await cache.expire({ tag });
    // The SWR serve, whose refresh mints `v2` at the now-applied epoch.
    await cache.getOrSetEntry({ factory, key: "k", tags: [tag] });
    expect(fills).toBe(2);

    // A LATER purge must still reach `v2`. This is what rules out the degenerate mints that would
    // satisfy the case above: a stamp of `Infinity`, or any epoch the kernel invents rather than
    // reads, converges beautifully and then serves purged values forever. Only a stamp taken from
    // the epoch actually applied at fill start passes both this and the case above.
    await cache.expire({ tag });
    const after = await cache.getOrSetEntry({ factory, key: "k", tags: [tag] });
    expect(after.value).toBe("v2");
    expect(after.stale).toBeTruthy();
    expect(fills).toBe(3);
  });

  test("INV-30 (control): with no purge at all, a fresh value serves fresh and no refresh runs", async () => {
    const w = world({ isolates: 1 });
    const { cache } = w.isolate(0);
    const tag = mkTag("t0");
    let fills = 0;
    const factory = async (): Promise<string> => {
      fills += 1;
      return await Promise.resolve(`v${String(fills)}`);
    };

    await cache.getOrSet({ factory, key: "k", tags: [tag] });
    // The baseline the two cases above are read against: absent a purge, a stored value is fresh
    // and the SWR path is not entered. A kernel that refreshed here would make `stale` above mean
    // nothing.
    const entry = await cache.getOrSetEntry({ factory, key: "k", tags: [tag] });
    expect(entry.value).toBe("v1");
    expect(entry.stale).toBeFalsy();
    expect(fills).toBe(1);
  });
});
