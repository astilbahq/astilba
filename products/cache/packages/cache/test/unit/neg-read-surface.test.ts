// Ordinary unit teeth (NOT sacred) — the cached-neg READ surface. Sacred pins the fact a neg
// re-read surfaces (`value: undefined`, §5.1 step 5 / §3 — negative-caching.test.ts); these pin the
// two implementation halves sacred deliberately leaves free, because both are storage policy rather
// than specified semantics:
//
//   1. neg-serve-evidence — a served neg reports the tier it was FOUND in and the durability that
//                      tier proves (E29): the neg's one write is `writeNegEnvelope`'s L2 write, so a
//                      re-read reports `l2` / `durable:true`. (Blind: report a constant tier or
//                      durability ⇒ reds.) The value assertion rides along as the near-miss guard —
//                      the placeholder is `null`, and `null` is what a truthiness-shaped check
//                      would wave through.
//   2. neg-hydrates-no-l1 — a neg fill performs ZERO L1 writes. Its placeholder body under a
//                      `kind:'val'` label would be a value-shaped envelope holding a value no
//                      origin produced — and a grace/stale-if-error candidate, which §11.7 forbids a
//                      neg from ever being. (Blind: drop the `storedKind === "neg"` arm in
//                      `hydrateL1` ⇒ an L1 write lands ⇒ reds.) The count is codec-independent,
//                      which is the point: today's default codec cannot encode `undefined` to a
//                      decodable body, so the mislabeled entry merely always MISSES — an accident,
//                      not a guarantee, and invisible to any behavioral assertion.
//   3. neg-evicts-stale-l1 — that same arm DELETES what L1 already held. Storing nothing is only
//                      half the rule: a fill runs when the read found nothing servable, and an L1
//                      read a classified transient failure suppressed (E13) is a plain miss over an
//                      entry still sitting there. Left standing, it outlives the 404 — the tier
//                      reads cleanly again, L1 is consulted first (§5.1), and the superseded value
//                      answers in preference to the neg L2 holds. (Blind: drop the `l1.delete` and
//                      leave a plain early return ⇒ the old value is served after recovery ⇒ reds.)
//                      Asserted twice: through the public serve, and against L1's own contents.
//                      Its CONTROL asserts a value fill still leaves a READABLE L1 entry, so
//                      "evict unconditionally" cannot pass either.
//
// Lane: unit — the scenario needs a store read that rejects with E13's classified shape, which the
// sacred harness's fakes cannot inject (their only injected failure is unclassified), and L1
// tier-write policy is storage policy §3 leaves free rather than specified semantics.
// Deterministic by construction — an injected clock + fake stores, no registry. No timing
// tolerance, no retry.

import assert from "node:assert/strict";

import { createCache, HttpError, memory, t } from "@astilba/cache";
import type {
  Cache,
  ReadKind,
  Store,
  StoreValue,
  StoreWriteOptions,
} from "@astilba/cache";
import { describe, expect, it } from "vitest";

const NS = "neg";
const CLOCK = { now: (): number => 0 };
const RNG = { next: (): number => 0 };

const notFound = async (): Promise<never> =>
  await Promise.reject(new HttpError(new Response(null, { status: 404 })));

/** A `Store` wrapper counting `set` calls — the format-independent proof that a neg fill writes
 * this tier or does not. Reads pass through. */
interface CountingStore {
  readonly store: Store;
  sets: number;
}
function countingStore(inner: Store): CountingStore {
  const wrapper: CountingStore = {
    sets: 0,
    store: {
      delete: async (key: string): Promise<void> => {
        await inner.delete(key);
      },
      get: async (
        key: string,
        readKind?: ReadKind
      ): Promise<StoreValue | undefined> => await inner.get(key, readKind),
      set: async (
        key: string,
        value: string,
        options?: StoreWriteOptions
      ): Promise<void> => {
        wrapper.sets += 1;
        await inner.set(key, value, options);
      },
    },
  };
  return wrapper;
}

/**
 * A rejection carrying E13's own classified shape (`code` + `retryable`) — the contract the kernel
 * discriminates on structurally, raised as a class so oxlint's throw rules are satisfied. It shares
 * no identity with the kernel's guard, which is the point.
 */
class ClassifiedReadFailureError extends Error {
  readonly code = "unavailable";
  readonly retryable = true;

  constructor() {
    super("store read failed: unavailable");
    this.name = "ClassifiedReadFailureError";
  }
}

/** An L1 whose reads reject with that shape while `failReads` is set — the one state in which a
 * fill runs OVER an entry this tier still holds. Writes and deletes pass straight through, and the
 * physical keys `set` saw are recorded so a test can probe L1's OWN contents rather than inferring
 * them from a serve. `inner` is exposed for exactly that probe. */
interface FlakyStore {
  readonly inner: Store;
  readonly store: Store;
  readonly writtenKeys: string[];
  failReads: boolean;
}
function flakyStore(inner: Store): FlakyStore {
  const wrapper: FlakyStore = {
    failReads: false,
    inner,
    store: {
      delete: async (key: string): Promise<void> => {
        await inner.delete(key);
      },
      get: async (
        key: string,
        readKind?: ReadKind
      ): Promise<StoreValue | undefined> => {
        if (wrapper.failReads) {
          throw new ClassifiedReadFailureError();
        }
        return await inner.get(key, readKind);
      },
      set: async (
        key: string,
        value: string,
        options?: StoreWriteOptions
      ): Promise<void> => {
        wrapper.writtenKeys.push(key);
        await inner.set(key, value, options);
      },
    },
    writtenKeys: [],
  };
  return wrapper;
}

/** An L2 that answers every read with absence while `forgotten` is set — the durable copy aged out
 * of residency. A write clears the flag, so the neg this tier then records is readable again. */
interface ForgetfulStore {
  readonly store: Store;
  forgotten: boolean;
}
function forgetfulStore(inner: Store): ForgetfulStore {
  const wrapper: ForgetfulStore = {
    forgotten: false,
    store: {
      delete: async (key: string): Promise<void> => {
        await inner.delete(key);
      },
      get: async (
        key: string,
        readKind?: ReadKind
      ): Promise<StoreValue | undefined> =>
        wrapper.forgotten ? undefined : await inner.get(key, readKind),
      set: async (
        key: string,
        value: string,
        options?: StoreWriteOptions
      ): Promise<void> => {
        wrapper.forgotten = false;
        await inner.set(key, value, options);
      },
    },
  };
  return wrapper;
}

function makeCache(l1: Store, l2: Store): Cache {
  return createCache({ clock: CLOCK, l1, l2, namespace: NS, rng: RNG });
}

/** The single physical key L1 was hydrated at — read off the recorded writes, because the kernel
 * canonicalizes it (namespace/scope/version) and a test that guessed it would probe nothing and
 * pass. */
function hydratedKey(l1: FlakyStore): string {
  const [key] = l1.writtenKeys;
  assert.ok(
    key !== undefined,
    "L1 was never hydrated — the fixture proves nothing"
  );
  return key;
}

// ── 1. neg-serve-evidence ────────────────────────────────────────────────────────────────────────

describe("a served neg reports the evidence of the serve (E29)", () => {
  it("a cached-neg re-read reports the tier it was found in, with that tier's durability", async () => {
    const l1 = memory();
    const cache = makeCache(l1, memory());
    await cache.getOrSetEntry({
      factory: notFound,
      key: "k",
      notFoundTtl: "30s",
      tags: [t`x`],
    });

    const reread = await cache.getOrSetEntry({
      factory: notFound,
      key: "k",
      notFoundTtl: "30s",
      tags: [t`x`],
    });
    // Found in the durable tier — the neg's only home — so the read is the proof of durability
    // (E29). An exact value, not a truthiness check: `durable` has a third, ABSENT state.
    expect(reread.tier).toBe("l2");
    expect(reread.durable).toBe(true);
    // And the value is the FACT, not the placeholder. Stated as two assertions because they fail
    // differently: the second is what a `null` placeholder trips.
    expect(reread.value).toBeUndefined();
    expect(reread.value).not.toBeNull();
  });
});

// ── 2. neg-hydrates-no-l1 ────────────────────────────────────────────────────────────────────────

describe("a neg fill writes only its own tier (§3, §5.1 step 5)", () => {
  it("performs ZERO L1 writes — no value-labeled copy of the placeholder", async () => {
    const l1 = countingStore(memory());
    const cache = makeCache(l1.store, memory());

    const before = l1.sets;
    const fill = await cache.getOrSetEntry({
      factory: notFound,
      key: "k",
      notFoundTtl: "30s",
      tags: [t`x`],
    });
    expect(fill.value).toBeUndefined();
    expect(l1.sets).toBe(before);
  });

  it("CONTROL: a successful value fill DOES hydrate L1", async () => {
    const l1 = countingStore(memory());
    const cache = makeCache(l1.store, memory());

    const before = l1.sets;
    await cache.getOrSet({
      factory: async (): Promise<string> => "v",
      key: "k",
      tags: [t`x`],
    });
    // The arm above is a neg-only refusal, not a disabled write-through: without this control,
    // deleting the L1 hydration outright would pass the tooth it is supposed to bite.
    expect(l1.sets).toBeGreaterThan(before);
  });
});

// ── 3. neg-evicts-stale-l1 ───────────────────────────────────────────────────────────────────────

describe("a neg fill leaves no superseded value behind in L1 (§3, §5.1 step 5, E13)", () => {
  /** The one arrangement that puts a fill on top of a live L1 entry: L1 holds `v1`, its reads are
   * suppressed to a miss by a classified transient failure (E13), and L2 no longer has the key — so
   * every tier reads as absent, the factory runs, and it 404s. */
  async function negOverLiveL1(): Promise<{
    readonly cache: Cache;
    readonly l1: FlakyStore;
    readonly origin: () => number;
  }> {
    const l1 = flakyStore(memory());
    const l2 = forgetfulStore(memory());
    const cache = makeCache(l1.store, l2.store);
    let calls = 0;
    await cache.getOrSet({
      factory: async (): Promise<string> => "v1",
      key: "k",
      tags: [t`x`],
    });

    l1.failReads = true;
    l2.forgotten = true;
    const negTurn = await cache.getOrSetEntry({
      factory: async (): Promise<never> => {
        calls += 1;
        return await notFound();
      },
      key: "k",
      notFoundTtl: "30s",
      tags: [t`x`],
    });
    expect(negTurn.value).toBeUndefined();
    expect(calls).toBe(1);
    return { cache, l1, origin: (): number => calls };
  }

  it("a read once L1 is readable again serves the remembered 404, not the superseded value", async () => {
    const { cache, l1, origin } = await negOverLiveL1();

    // The tier reads cleanly again, and L1 is consulted FIRST (§5.1 steps 2/4) — so if anything of
    // `v1` survived the neg fill, this is the read that hands it back.
    l1.failReads = false;
    const after = await cache.getOrSetEntry({
      factory: notFound,
      key: "k",
      notFoundTtl: "30s",
      tags: [t`x`],
    });
    // Stated as three assertions because they fail differently: the fact, the specific resurrection
    // (a truthiness check would wave `undefined` through but so would it wave through a `null`
    // placeholder — this names the value that must NOT come back), and the tier that answered.
    expect(after.value).toBeUndefined();
    expect(after.value).not.toBe("v1");
    expect(after.tier).toBe("l2");
    // And it was the STORED neg answering, not a second 404 refilling: the origin was asked once.
    expect(origin()).toBe(1);
  });

  it("L1's own contents hold nothing for the key once the neg fill returns", async () => {
    const { l1 } = await negOverLiveL1();

    // Asserted against the tier itself, not through a serve: the serve above could also be
    // satisfied by an L1 entry that merely fails to decode, and "always misses" is a property of
    // today's codec rather than of this tier's contents.
    await expect(l1.inner.get(hydratedKey(l1))).resolves.toBeUndefined();
  });

  it("CONTROL: a value fill leaves a READABLE L1 entry — the eviction is neg-only", async () => {
    const l1 = flakyStore(memory());
    const cache = makeCache(l1.store, memory());
    await cache.getOrSet({
      factory: async (): Promise<string> => "v1",
      key: "k",
      tags: [t`x`],
    });

    // The counted control above forbids "never write L1"; this one forbids the other degenerate
    // reading — "delete L1 on every fill" — which would satisfy both eviction assertions while
    // destroying the write-through §5.1 asks for.
    await expect(l1.inner.get(hydratedKey(l1))).resolves.toBeDefined();
  });
});
