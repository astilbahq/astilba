// INV-7 — spec
// INV-26
// Sacred: append-only. Never modify, delete, weaken, skip, or .todo an existing test.
//
// The not-found direction of the per-waiter disposition rule — the mirror of the retriable
// direction grace-join-mirror.test.ts pins: a join hands each waiter the shared FACT of what the
// origin returned, and each waiter decides its own disposition against its own read. A blind LEADER
// (its read finds nothing) fills for a key while a sighted JOINER (its read finds a servable stale
// copy) joins the same fill; the origin then returns 404 for both.
//
// INV-26 forbids a neg-write while a servable `val` exists — and that condition is ONTOLOGICAL
// (it is about the STORE), while the fill can only test what its OWN read SAW. A blind leader saw
// nothing, so it must not get to decide suppress-vs-write for everyone: the sighted joiner's read IS
// the existence proof the leader lacked. So the shared 404 is a FACT each waiter classifies against
// its OWN read — a sighted waiter SUPPRESSES the neg and serves its own copy; a blind waiter WRITES
// the neg and serves it. This mirrors the ladder being the caller's own per-read algorithm and
// the singleflight being a mechanism inside step 5, not a substitute for it.
//
// THE ORACLE IS THE SOLO PATH. Each assertion below compares the JOINED outcome to the outcome of
// the IDENTICAL SOLO call (same rig, same options, no second caller) — the joined path must resolve
// exactly as the solo call resolves, per API form. A tooth written only against what the joined code
// produces could ratify a defect; comparing against an independently-pinned solo call cannot.
//
// The rig hand-wires the primitives (the grace-join precedent) for the one thing `world()` cannot
// express: a leader whose read finds nothing and a joiner whose read finds the servable copy. The
// interleaving is pinned by a held factory (not `fc.scheduler`) — these are not race tests, so the
// tooth cannot pass or fail by seed.
//
// Warrant: R. Morris; applied by Claude Code. A genuine join is witnessed at the suppressing seat's
// singleflight join count (`sfJoins` >= 1, measured per arm); a factory-run count of one is
// consistent with both a joined caller and a self-serving second caller, and is not a join witness.

import { createCache, HttpError } from "@astilba/cache";
import type { Cache, CacheEntry, Store, Tag } from "@astilba/cache";
import {
  FakeBus,
  FakeLock,
  FakeRegistry,
  FakeStore,
  mkTag,
  SimClock,
  SimRng,
} from "@astilba/harness";
import { test } from "@fast-check/vitest";
import { describe, expect } from "vitest";

/** A `not_found` origin result (404 — a fact, negatively cached when `notFoundTtl` is declared). */
const notFound = async (): Promise<never> => {
  throw new HttpError(new Response(null, { status: 404 }));
};

interface Gate {
  readonly held: Promise<void>;
  open(): void;
}

/** A promise this test opens by hand — the fixture that makes the leader a leader: its factory parks
 * here while the joiner arrives, so "joiner" is a fact rather than a hope about scheduling. */
function gate(): Gate {
  // SAFETY: the `new Promise` executor runs SYNCHRONOUSLY, before the constructor returns, so `open`
  // is assigned before this function returns and can never be observed unassigned.
  let open!: () => void;
  // oxlint-disable-next-line promise/avoid-new -- a deferred is this fixture's control point; there is no library promise to await to pause at a chosen instant.
  const held = new Promise<void>((resolve) => {
    open = resolve;
  });
  return { held, open };
}

/**
 * An L2 whose NEXT DATA-key read answers absence once `arm.blind` is set, then reverts. Registry /
 * mirror keys (`__…`) pass through untouched. Armed just before the outage, it makes the LEADER's
 * read find nothing (blind) while the later JOINER's read finds the stored copy (sighted).
 */
function blindLeaderStore(inner: Store, arm: { blind: boolean }): Store {
  return {
    delete: async (key: string): Promise<void> => {
      await inner.delete(key);
    },
    get: async (key, readKind) => {
      const found = await inner.get(key, readKind);
      const blindThis = !key.includes("__") && arm.blind;
      if (blindThis) {
        arm.blind = false; // consumed by the leader's read; the joiner's read sees the copy.
      }
      return blindThis ? undefined : found;
    },
    set: async (key, value, options): Promise<void> => {
      await inner.set(key, value, options);
    },
  };
}

interface Rig {
  readonly cache: Cache;
  readonly arm: { blind: boolean };
  readonly tag: Tag;
  settle(): Promise<void>;
}

function rig(): Rig {
  const clock = new SimClock();
  const store = new FakeStore(clock, { replicas: 1 });
  const bus = new FakeBus(clock);
  const registry = new FakeRegistry(clock, {
    bus,
    regId: "reg",
    store: store.replica(0),
  });
  const arm = { blind: false };
  return {
    arm,
    cache: createCache({
      bus,
      clock,
      l2: blindLeaderStore(store.replica(0), arm),
      lock: new FakeLock(),
      namespace: "ns",
      registry,
      rng: new SimRng(1),
    }),
    settle: async (): Promise<void> => {
      await clock.runMicrotasks(12);
    },
    tag: mkTag("t0"),
  };
}

/** Seed a stored value, then soft-purge it: its candidate is STALE, so it re-validates as servable
 * at serve time — a hard-purged one would not, and suppression would never engage. */
async function seedStaleCopy(r: Rig): Promise<void> {
  await r.cache.getOrSet({
    factory: async () => await Promise.resolve("good"),
    key: "k",
    tags: [r.tag],
  });
  await r.cache.expire({ tag: r.tag });
}

const SHARED = {
  consistency: "strong",
  grace: "1h",
  key: "k",
  notFoundTtl: "30s",
} as const;

/**
 * Drive a 404 with a BLIND leader: arm the store so the leader's read finds nothing, hold the leader
 * in its factory until the joiner has joined, then 404 for both. Counts factory runs so the caller
 * can prove a genuine join (exactly one: the leader's; the joiner's never runs). `run` is the
 * caller's form of the verb, so the same fixture serves the entry form and the plain form.
 */
async function notFoundWithBlindLeader<R>(
  r: Rig,
  run: (factory: () => Promise<never>) => Promise<R>
): Promise<{
  readonly settled: readonly [PromiseSettledResult<R>, PromiseSettledResult<R>];
  readonly factoryRuns: number;
}> {
  const g = gate();
  let factoryRuns = 0;
  r.arm.blind = true; // the LEADER's read finds nothing; the joiner's finds the stored copy.
  const leader = run(async () => {
    factoryRuns += 1;
    await g.held;
    return await notFound();
  });
  await r.settle(); // the leader reads (absence), registers its fill, and parks in the factory
  const joiner = run(async () => {
    factoryRuns += 1;
    return await notFound();
  });
  await r.settle(); // the joiner reads (the copy), reaches singleflight, and JOINS
  g.open();
  const settled = await Promise.allSettled([leader, joiner]);
  return { factoryRuns, settled };
}

/** The IDENTICAL SOLO call — one caller, no join — used as the oracle every joined outcome is
 * measured against. `seed` decides whether this solo caller is SIGHTED (a servable stale copy in the
 * store) or BLIND (nothing to fall back on). */
async function solo<R>(
  seed: boolean,
  run: (cache: Cache, tag: Tag) => Promise<R>
): Promise<R> {
  const r = rig();
  if (seed) {
    await seedStaleCopy(r);
  }
  return await run(r.cache, r.tag);
}

/** The identical solo call IN THE JOINED RIG'S OWN WORLD: a store holding the same servable stale
 * copy, with this caller's own read blinded exactly as the leader's is. Seeding happens BEFORE the
 * arm, so the seed's own reads do not consume it.
 *
 * Why this exists: an equivalence pin has to hold its WORLD fixed. An identity asserted across two
 * DIFFERENT store states does not pin route-equivalence — it pins world-convergence, and collapsing
 * two distinguishable worlds into one observable is what a defect does, not what a specification
 * says. Whether a compare-fetch carried the val's bytes is route; whether the store HOLDS a val is
 * state, and state is allowed to decide outcomes — hit-versus-miss is the product.
 */
async function soloBlindAgainstSeeded<R>(
  run: (cache: Cache, tag: Tag) => Promise<R>
): Promise<R> {
  const r = rig();
  await seedStaleCopy(r);
  r.arm.blind = true;
  return await run(r.cache, r.tag);
}

/** `age` is a clock reading, not part of the disposition being pinned — normalize it on BOTH sides
 * so the comparison is of the served evidence, never of when the two rigs happened to be read. */
function norm<T>(entry: CacheEntry<T>): CacheEntry<T> {
  return { ...entry, age: 0 };
}

describe("negative caching across a singleflight join — the not-found mirror (E7/INV-26, E30)", () => {
  test("INV-26/E30: a blind leader's sighted joiner SUPPRESSES the neg and serves its OWN copy — the leader takes the neg — each resolving exactly as its identical solo call does (entry form)", async () => {
    const r = rig();
    await seedStaleCopy(r);
    const { settled, factoryRuns } = await notFoundWithBlindLeader(
      r,
      async (factory) =>
        await r.cache.getOrSetEntry({ ...SHARED, factory, tags: [r.tag] })
    );
    const [led, joined] = settled;

    // A genuine join: the leader's factory ran once, the joiner's never.
    expect(factoryRuns).toBe(1);

    // The oracles: the SAME solo call, once with a servable stale copy (sighted), once with nothing
    // (blind). These are the contract, pinned independently of the joined path below.
    const sightedOracle = await solo(
      true,
      async (cache, tag) =>
        await cache.getOrSetEntry({ ...SHARED, factory: notFound, tags: [tag] })
    );
    // Oracle amendment. This oracle ran against an EMPTY store while the rig above is
    // seeded, and demanded the two agree. That identity held only because the displacement defect
    // destroyed the seeded val, collapsing both worlds into one observable — so the pin was a
    // fossil of the very defect the guard removes, and it forced the suppressed fill turn to
    // report a durable not-found that provably does not exist. Seeded, the identity survives
    // honestly: the guard sits at one boundary, so this solo caller is suppressed too.
    const blindOracle = await soloBlindAgainstSeeded(
      async (cache, tag) =>
        await cache.getOrSetEntry({ ...SHARED, factory: notFound, tags: [tag] })
    );

    // Non-vacuity + spec anchor in ONE claim: the two solo oracles are genuinely DIFFERENT
    // dispositions — the sighted one keeps its own value (suppression), the blind one takes the
    // neg (`undefined`). If they were equal the mirror below would prove nothing.
    expect({
      blind: blindOracle.value,
      sighted: sightedOracle.value,
    }).toStrictEqual({
      // oxlint-disable-next-line sonarjs/no-undefined-assignment -- the blind oracle's served value is PRESENT and undefined (the neg); `toStrictEqual` must see it to tell that from an absent key, and `null` would assert a different shape.
      blind: undefined,
      sighted: "good",
    });

    // THE TOOTH. The blind LEADER writes the neg and serves it — exactly the blind solo oracle (a 404
    // is negatively cached, so the entry RESOLVES `value: undefined` at `tier:'origin'`; it is NOT
    // rejected). The sighted JOINER holds a servable copy of its OWN, so it SUPPRESSES the neg and
    // serves that copy — exactly the sighted solo oracle (found in L2: `tier:'l2'`, `durable:true`,
    // stale, and — a 404 not being `staleIfError` — WITHOUT `servedOnError`). On today's tree the
    // joiner is instead handed the leader's neg (`value: undefined, tier:'origin'`), so this reds.
    expect({
      joiner: joined.status === "fulfilled" ? norm(joined.value) : joined,
      leader: led.status === "fulfilled" ? norm(led.value) : led,
    }).toStrictEqual({
      joiner: norm(sightedOracle),
      leader: norm(blindOracle),
    });
  });

  test("INV-26/E30: the plain form serves the sighted joiner its own value and resolves the blind leader to the cached-miss undefined — never rejecting either — matching the identical solo calls", async () => {
    const r = rig();
    await seedStaleCopy(r);
    const { settled, factoryRuns } = await notFoundWithBlindLeader(
      r,
      async (factory) =>
        await r.cache.getOrSet({ ...SHARED, factory, tags: [r.tag] })
    );
    const [led, joined] = settled;

    expect(factoryRuns).toBe(1);

    // The plain-form solo oracles. The blind one RESOLVES `undefined` (a negatively-cached 404 is a
    // served miss at `tier:'origin'`, not the thrown `FencedError`/typed-error a true miss would be)
    // — the per-form distinction the plain wrapper draws. The sighted one resolves its own value.
    const sightedOracle = await solo(
      true,
      async (cache, tag) =>
        await cache.getOrSet({ ...SHARED, factory: notFound, tags: [tag] })
    );
    const blindOracle = await solo(
      false,
      async (cache, tag) =>
        await cache.getOrSet({ ...SHARED, factory: notFound, tags: [tag] })
    );
    // Non-vacuity + spec anchor: the sighted solo caller resolves its own value, the blind one the
    // cached-miss undefined.
    expect({ blind: blindOracle, sighted: sightedOracle }).toStrictEqual({
      // oxlint-disable-next-line sonarjs/no-undefined-assignment -- the plain blind oracle RESOLVES undefined (a served cached-miss); the equality must assert that exact value.
      blind: undefined,
      sighted: "good",
    });

    // THE TOOTH (plain form). One shared 404, two dispositions decided per-caller against each
    // caller's own read: the blind leader resolves the cached-miss undefined, the sighted joiner
    // survives it by serving its own "good". Both must RESOLVE (never reject), each equal to its solo
    // oracle. On today's tree the joiner is handed the leader's neg and resolves `undefined` — which
    // this comparison against the sighted solo oracle rejects.
    expect([led, joined]).toStrictEqual([
      { status: "fulfilled", value: blindOracle },
      { status: "fulfilled", value: sightedOracle },
    ]);
  });
});

describe("negative caching across a singleflight join — the neg-write still lands (E7/INV-26, E30)", () => {
  // The non-degeneracy arm: a genuine true-miss neg-write must STILL happen across a join. Without
  // this, a mutant that "suppresses everything" (never writes the neg) would pass the mirror above —
  // the sighted joiner suppresses there regardless. Here NO copy exists anywhere, so both waiters are
  // blind and each must write-and-serve the neg exactly as a solo blind caller does.
  test("INV-7/E30: with no servable copy anywhere, a joined pair both write-and-serve the neg, exactly as the identical solo blind call — and the neg PERSISTS (entry form)", async () => {
    const r = rig(); // no `seedStaleCopy`: the store is empty, so every read finds true absence.
    let followupRuns = 0;
    const { settled, factoryRuns } = await notFoundWithBlindLeader(
      r,
      async (factory) =>
        await r.cache.getOrSetEntry({ ...SHARED, factory, tags: [r.tag] })
    );
    const [led, joined] = settled;

    expect(factoryRuns).toBe(1);

    const blindOracle = await solo(
      false,
      async (cache, tag) =>
        await cache.getOrSetEntry({ ...SHARED, factory: notFound, tags: [tag] })
    );
    // Non-vacuity of the oracle itself (ABSOLUTE, not relative): the solo blind path produces a
    // SERVED neg — `value: undefined` at `tier:'origin'` with `durable:true` — not a miss or a skip.
    // A mutant that dropped the neg-write would break these (the served value would be a `tier:'miss'`
    // instead), independently of the joined comparison below.
    expect({
      durable: blindOracle.durable,
      tier: blindOracle.tier,
      value: blindOracle.value,
    }).toStrictEqual({
      durable: true,
      tier: "origin",
      // oxlint-disable-next-line sonarjs/no-undefined-assignment -- the served neg's value is PRESENT and undefined; the equality must see it to distinguish a served neg from a skip/miss shape.
      value: undefined,
    });

    // Both waiters resolve the neg — the true-miss disposition, matching the solo blind oracle.
    expect({
      joiner: joined.status === "fulfilled" ? norm(joined.value) : joined,
      leader: led.status === "fulfilled" ? norm(led.value) : led,
    }).toStrictEqual({ joiner: norm(blindOracle), leader: norm(blindOracle) });

    // The neg PERSISTED (proving a write, not merely a served `undefined`): a follow-up read within
    // `notFoundTtl` is answered from the stored neg — so the factory is NEVER invoked again. Had the
    // neg not landed, the empty store would miss and the factory would run. This is an ABSOLUTE
    // persistence check, independent of the solo oracle. (The re-read's own value/tier is the read
    // path's concern, not this join's — left unpinned.)
    await r.cache.getOrSetEntry({
      ...SHARED,
      factory: async () => {
        followupRuns += 1;
        return await notFound();
      },
      tags: [r.tag],
    });
    expect(followupRuns).toBe(0);
  });

  test("INV-7/E30: the plain form resolves both blind waiters to the cached-miss undefined, matching the identical solo blind call, and the neg persists", async () => {
    const r = rig();
    let followupRuns = 0;
    const { settled, factoryRuns } = await notFoundWithBlindLeader(
      r,
      async (factory) =>
        await r.cache.getOrSet({ ...SHARED, factory, tags: [r.tag] })
    );
    const [led, joined] = settled;

    expect(factoryRuns).toBe(1);

    const blindOracle = await solo(
      false,
      async (cache, tag) =>
        await cache.getOrSet({ ...SHARED, factory: notFound, tags: [tag] })
    );
    // The plain blind solo oracle RESOLVES undefined (a negatively-cached 404 is a served miss, not a
    // throw). Both joined waiters must resolve identically — never reject.
    expect(blindOracle).toBeUndefined();
    expect([led, joined]).toStrictEqual([
      { status: "fulfilled", value: blindOracle },
      { status: "fulfilled", value: blindOracle },
    ]);

    // The neg PERSISTED: a follow-up read within `notFoundTtl` is answered from the stored neg, so
    // the factory is NEVER re-invoked (an empty store would miss and run it). An ABSOLUTE persistence
    // check, independent of the solo oracle.
    await r.cache.getOrSet({
      ...SHARED,
      factory: async () => {
        followupRuns += 1;
        return await notFound();
      },
      tags: [r.tag],
    });
    expect(followupRuns).toBe(0);
  });
});

/**
 * The MIRROR IMAGE of `blindLeaderStore`: an L2 that lets the LEADER's data read through and blinds
 * the NEXT one — the JOINER's — then reverts, so every later read (the `explain` probe below) sees
 * the store again. Registry / mirror keys (`__…`) pass through untouched.
 *
 * This is the asymmetry the leader-blind rig cannot express: a leader that DID read the servable
 * copy, and a joiner holding nothing of its own. A joiner with no copy can only report what the
 * shared serve hands it, which is what makes inheritance observable — where both happened to hold the
 * same candidate, a kernel consulting either one would look correct.
 */
function blindJoinerStore(
  inner: Store,
  arm: { armed: boolean; pass: number }
): Store {
  return {
    delete: async (key: string): Promise<void> => {
      await inner.delete(key);
    },
    get: async (key, readKind) => {
      const found = await inner.get(key, readKind);
      if (key.includes("__") || !arm.armed) {
        return found;
      }
      const passes = arm.pass > 0;
      if (passes) {
        arm.pass -= 1; // the LEADER's read — it sees the stored copy (sighted).
      } else {
        arm.armed = false; // consumed by the JOINER's read; later reads see the store.
      }
      return passes ? found : undefined;
    },
    set: async (key, value, options): Promise<void> => {
      await inner.set(key, value, options);
    },
  };
}

interface SightedLeaderRig {
  readonly cache: Cache;
  readonly arm: { armed: boolean; pass: number };
  readonly tag: Tag;
  settle(): Promise<void>;
}

/** The sighted-leader rig. Deliberately a SEPARATE rig from the blind-leader one above rather than a
 * parameterization of it: the tests already standing there are teeth, and they keep their fixture. */
function sightedLeaderRig(): SightedLeaderRig {
  const clock = new SimClock();
  const store = new FakeStore(clock, { replicas: 1 });
  const bus = new FakeBus(clock);
  const registry = new FakeRegistry(clock, {
    bus,
    regId: "reg",
    store: store.replica(0),
  });
  const arm = { armed: false, pass: 0 };
  return {
    arm,
    cache: createCache({
      bus,
      clock,
      l2: blindJoinerStore(store.replica(0), arm),
      lock: new FakeLock(),
      namespace: "ns",
      registry,
      rng: new SimRng(1),
    }),
    settle: async (): Promise<void> => {
      await clock.runMicrotasks(12);
    },
    tag: mkTag("t0"),
  };
}

/** Seed a stored value and soft-purge it, so its candidate is STALE and re-validates as servable at
 * serve time — the sighted-leader rig's counterpart of the seeding above. */
async function seedStaleCopyOn(cache: Cache, tag: Tag): Promise<void> {
  await cache.getOrSet({
    factory: async () => await Promise.resolve("good"),
    key: "k",
    tags: [tag],
  });
  await cache.expire({ tag });
}

/**
 * Drive a 404 with a SIGHTED leader: arm the store so the leader's read passes and the JOINER's finds
 * nothing, hold the leader in its factory until the joiner has joined, then 404 for both. Counts
 * factory runs so the caller can prove a genuine join (exactly one: the leader's).
 */
async function notFoundWithSightedLeader<R>(
  r: SightedLeaderRig,
  run: (factory: () => Promise<never>) => Promise<R>
): Promise<{
  readonly settled: readonly [PromiseSettledResult<R>, PromiseSettledResult<R>];
  readonly factoryRuns: number;
}> {
  const g = gate();
  let factoryRuns = 0;
  r.arm.armed = true;
  r.arm.pass = 1; // the LEADER's read sees the copy; the JOINER's finds absence.
  const leader = run(async () => {
    factoryRuns += 1;
    await g.held;
    return await notFound();
  });
  await r.settle(); // the leader reads (the copy), registers its fill, and parks in the factory
  const joiner = run(async () => {
    factoryRuns += 1;
    return await notFound();
  });
  await r.settle(); // the joiner reads (absence), reaches singleflight, and JOINS
  g.open();
  const settled = await Promise.allSettled([leader, joiner]);
  return { factoryRuns, settled };
}

/** What the store actually HOLDS for the key afterwards, as the read-only witness reports it:
 * the stored envelope's kind — `val` if the copy survived, `neg` if a neg was written over it. The
 * witness never hydrates, so probing cannot itself change the answer. */
async function storedKind(cache: Cache): Promise<string> {
  const seen = await cache.explain("k");
  return seen.kind === "present" ? seen.identity.kind : seen.kind;
}

describe("negative caching across a singleflight join — a sighted leader's suppression is inherited (E7/INV-26, E30)", () => {
  // The mirror of the blind-leader suites above, and the direction that closes the loop: when the
  // LEADER is the one holding the servable copy, it decides its own disposition (suppression) and
  // the joiner INHERITS that serve. The joiner here reads NOTHING of its own, so if the kernel ever
  // fell back to deciding a waiter's disposition on that waiter's own (empty) read, the joiner would
  // write a neg straight over the leader's still-servable value — the displacement the neg-write guard exists to
  // forbid, reached from the opposite side of the join. Both the SERVE and what the store is left
  // holding are pinned, because only the second catches a displacement that still served the value.
  test("INV-26/E30: a sighted leader suppresses the neg and its blind joiner INHERITS that serve with the leader's own entry evidence — and the stored val survives un-displaced (entry form)", async () => {
    const r = sightedLeaderRig();
    await seedStaleCopyOn(r.cache, r.tag);
    const { settled, factoryRuns } = await notFoundWithSightedLeader(
      r,
      async (factory) =>
        await r.cache.getOrSetEntry({ ...SHARED, factory, tags: [r.tag] })
    );
    const [led, joined] = settled;

    // A genuine join: the leader's factory ran once, the joiner's never.
    expect(factoryRuns).toBe(1);

    // The oracle: the IDENTICAL SOLO sighted call. A solo caller holding a still-servable copy
    // suppresses the neg and serves that copy — the contract both waiters must land on.
    const sightedOracle = await solo(
      true,
      async (cache, tag) =>
        await cache.getOrSetEntry({ ...SHARED, factory: notFound, tags: [tag] })
    );
    expect(sightedOracle.value).toBe("good");

    // THE TOOTH. The leader serves its own suppressed copy; the blind JOINER — which read nothing —
    // is handed that same serve and reports the LEADER's entry evidence (found in L2, so
    // `tier:'l2'`, `durable:true`, stale, and WITHOUT `servedOnError`, a 404 not being `staleIfError`).
    // Both equal the sighted solo oracle. Blind the leader-sighted arm and the leader still serves
    // (its own tail suppresses), but the joiner falls through to its own empty read and takes a neg.
    expect({
      joiner: joined.status === "fulfilled" ? norm(joined.value) : joined,
      leader: led.status === "fulfilled" ? norm(led.value) : led,
    }).toStrictEqual({
      joiner: norm(sightedOracle),
      leader: norm(sightedOracle),
    });

    // THE DURABLE HALF. Suppression means nothing was written, so the store must still hold the VAL.
    // This is the assertion a serve-only tooth cannot make: a joiner that wrote a neg over the val
    // could still have been handed "good" to serve, and the displacement would pass unseen. Blinding
    // the leader-sighted arm leaves the blind joiner writing its neg here, and this reads `neg`.
    await expect(storedKind(r.cache)).resolves.toBe("val");
  });

  test("INV-26/E30: the plain form hands a sighted leader's blind joiner the suppressed value rather than a neg, and leaves the stored val in place", async () => {
    const r = sightedLeaderRig();
    await seedStaleCopyOn(r.cache, r.tag);
    const { settled, factoryRuns } = await notFoundWithSightedLeader(
      r,
      async (factory) =>
        await r.cache.getOrSet({ ...SHARED, factory, tags: [r.tag] })
    );
    const [led, joined] = settled;

    expect(factoryRuns).toBe(1);

    const sightedOracle = await solo(
      true,
      async (cache, tag) =>
        await cache.getOrSet({ ...SHARED, factory: notFound, tags: [tag] })
    );
    expect(sightedOracle).toBe("good");

    // Both waiters RESOLVE the suppressed value — neither rejects, and neither receives the neg's
    // `undefined`. A joiner deciding on its own empty read would resolve `undefined` here.
    expect([led, joined]).toStrictEqual([
      { status: "fulfilled", value: sightedOracle },
      { status: "fulfilled", value: sightedOracle },
    ]);

    // And the stored copy is untouched: a suppressed neg is a neg that was never written.
    await expect(storedKind(r.cache)).resolves.toBe("val");
  });
});

/** What the tier saw of the neg RECORDING: how many `kind:'neg'` envelopes were written, and a
 * one-shot hook fired at the instant the first one lands — the moment the fill is recording the fact,
 * which is where the question "is this still inside the fill's window?" is asked. One-shot so a
 * probe's own writes cannot re-enter it. */
interface NegWriteWatch {
  writes: number;
  // Explicitly `| undefined`: the hook is CLEARED after firing (one-shot), and under
  // `exactOptionalPropertyTypes` an optional property cannot be assigned `undefined` unless its type
  // admits it.
  onFirst?: (() => Promise<void>) | undefined;
}

function negWriteWatchStore(inner: Store, watch: NegWriteWatch): Store {
  return {
    delete: async (key: string): Promise<void> => {
      await inner.delete(key);
    },
    get: async (key, readKind) => await inner.get(key, readKind),
    set: async (key, value, options): Promise<void> => {
      if (options?.metadata?.kind === "neg") {
        watch.writes += 1;
        const first = watch.onFirst;
        if (first) {
          // oxlint-disable-next-line sonarjs/no-undefined-assignment -- clearing an OPTIONAL callback is exactly what absence means here; `null` would not satisfy its `(() => Promise<void>) | undefined` type, and the one-shot disarm is what stops a probe's own write from re-entering.
          watch.onFirst = undefined;
          await first();
        }
      }
      await inner.set(key, value, options);
    },
  };
}

interface NegWatchRig {
  readonly cache: Cache;
  readonly watch: NegWriteWatch;
  readonly tag: Tag;
  settle(): Promise<void>;
}

/** A rig over an EMPTY store — so every caller's read finds true absence and every waiter is blind —
 * whose tier reports what the neg recording actually did. */
function negWatchRig(): NegWatchRig {
  const clock = new SimClock();
  const store = new FakeStore(clock, { replicas: 1 });
  const bus = new FakeBus(clock);
  const registry = new FakeRegistry(clock, {
    bus,
    regId: "reg",
    store: store.replica(0),
  });
  const watch: NegWriteWatch = { writes: 0 };
  return {
    cache: createCache({
      bus,
      clock,
      l2: negWriteWatchStore(store.replica(0), watch),
      lock: new FakeLock(),
      namespace: "ns",
      registry,
      rng: new SimRng(1),
    }),
    settle: async (): Promise<void> => {
      await clock.runMicrotasks(12);
    },
    tag: mkTag("t0"),
    watch,
  };
}

describe("negative caching across a singleflight join — recording the fact is WORK, done once inside the window (§5.0, §5.2, E7, E30)", () => {
  // A join shares the WORK and its product; only the disposition is per-waiter. Recording the 404 is
  // work — the exact counterpart of the success path storing its filled value — so it belongs inside
  // the fill, inside the window the singleflight registration holds, and it happens ONCE.
  //
  // Siting it in the waiters' tails instead breaks two things at once, and there is a test here for
  // each. (a) The registration is released when the fill settles, so a tail-sited write is still
  // pending while the slot is already free: a later same-key caller can fill and store a value, and
  // the trailing neg lands on top of it — equal epochs are admitted by the born-monotone compare,
  // which orders on epoch and fence. (b) Every blind waiter runs its own tail, so one 404 becomes N
  // writes.
  test("INV-7/E30: the fact is recorded INSIDE the fill's window — a caller arriving at the very instant of the neg write still JOINS, and never runs a factory of its own", async () => {
    const r = negWatchRig();
    const g = gate();
    let probeRuns = 0;
    let leaderRuns = 0;
    let probe: Promise<unknown> = Promise.resolve();

    // Fired as the neg write lands. A caller arriving NOW is the question this test asks: while the
    // fact is being recorded, is the fill still in flight? If the recording sits inside the window
    // the answer is yes and this caller JOINS (its factory never runs). If the recording were sited
    // after the registration settled, the slot would already be free and this caller would run a
    // second factory against the same key — the opening through which a value can be stored and then
    // buried by the trailing write.
    r.watch.onFirst = async (): Promise<void> => {
      probe = r.cache.getOrSetEntry({
        ...SHARED,
        factory: async () => {
          probeRuns += 1;
          return await notFound();
        },
        tags: [r.tag],
      });
      await r.settle(); // let it reach the point where it either joins or starts its own fill
    };

    const leader = r.cache.getOrSetEntry({
      ...SHARED,
      factory: async () => {
        leaderRuns += 1;
        await g.held;
        return await notFound();
      },
      tags: [r.tag],
    });
    await r.settle(); // the leader reads (absence), registers its fill, and parks in the factory
    g.open();
    await leader;
    await probe;

    // Non-vacuity: the leader really did fill, and a neg really was recorded — so the hook that
    // carries this test's whole question actually fired. The COUNT is the next test's claim; here it
    // only has to be non-zero, so this check cannot pre-empt the tooth below.
    expect({ leaderRuns, recorded: r.watch.writes > 0 }).toStrictEqual({
      leaderRuns: 1,
      recorded: true,
    });

    // THE TOOTH. The arriving caller JOINED rather than starting a competing fill, which is only
    // possible if the registration was still held while the fact was being recorded. Site the
    // recording after the registration settles and this caller finds a free slot, runs its own
    // factory against the same key, and this reads 1.
    expect(probeRuns).toBe(0);
  });

  test("INV-7/E30: one 404 across a join is recorded ONCE, however many blind waiters it serves", async () => {
    const r = negWatchRig();
    const g = gate();
    let factoryRuns = 0;
    const call = async (): Promise<CacheEntry<unknown>> =>
      await r.cache.getOrSetEntry({
        ...SHARED,
        factory: async () => {
          factoryRuns += 1;
          await g.held;
          return await notFound();
        },
        tags: [r.tag],
      });

    const leader = call();
    await r.settle(); // the leader registers its fill and parks in the factory
    const joiners = [call(), call()];
    await r.settle(); // both joiners reach singleflight and JOIN
    g.open();
    const served = await Promise.all([leader, ...joiners]);

    // A genuine three-way join: one factory invocation for all of them.
    expect(factoryRuns).toBe(1);

    // All three are served the recorded neg — the disposition each blind waiter reaches on its own
    // read, which is the same one because they are in the same state.
    expect(served.map((e) => e.value)).toStrictEqual([
      undefined,
      undefined,
      undefined,
    ]);

    // THE TOOTH. The fact was recorded ONCE. A tail-sited write would issue one per blind waiter —
    // three writes for one 404, each re-stamping the same fact and burning the tier's per-key write
    // budget for nothing.
    expect(r.watch.writes).toBe(1);
  });
});

/**
 * A caller who reads NOTHING of its own is blind to what the STORE holds — and the suppression
 * turns on the store, not on the reader. The suites above pin the DISPOSITION side of that (who is
 * served what); these pin the side no serve can witness: what the key is left holding.
 *
 * The distinction is the whole of it. the suppression rule is ONTOLOGICAL — it turns on what the
 * store actually holds — while a filling caller's own read is merely EPISTEMIC evidence about that.
 * Where the two agree, either one looks like the rule. The blind-leader fixture is where they come
 * apart: the leader sees nothing, the store holds a servable val, and a kernel that decided the
 * WRITE on the leader's read buries it. Serving the sighted joiner its own copy does not repair
 * that — the joiner is served once, and every reader after it gets the negative.
 *
 * So the oracle here is `explain()`, the non-hydrating witness: it reports the STORED
 * envelope's kind without touching it, which is the only way to tell a val that survived from a val
 * that was displaced behind a correct-looking serve.
 */
describe("negative caching across a singleflight join — a neg never lands on the val a blind leader could not see (E7/INV-26)", () => {
  test("INV-26/E7: a blind leader's 404 leaves the STORED val standing, and a SUBSEQUENT INDEPENDENT read is served that val — never the neg (entry form)", async () => {
    const r = rig();
    await seedStaleCopy(r);
    const { settled, factoryRuns } = await notFoundWithBlindLeader(
      r,
      async (factory) =>
        await r.cache.getOrSetEntry({ ...SHARED, factory, tags: [r.tag] })
    );
    const [led, joined] = settled;

    // Non-vacuity: a genuine join, in the shape this suite is about — the LEADER blind (served the
    // not-found fact), the JOINER sighted (served its own copy).
    expect({
      factoryRuns,
      joiner: joined.status === "fulfilled" ? joined.value.value : joined,
      leader: led.status === "fulfilled" ? led.value.value : led,
    }).toStrictEqual({
      factoryRuns: 1,
      joiner: "good",
      // oxlint-disable-next-line sonarjs/no-undefined-assignment -- the blind leader's served value is PRESENT and undefined (the not-found fact); `toStrictEqual` must see it to tell that from an absent key.
      leader: undefined,
    });

    // THE DISPLACEMENT PROBE. Nothing was written, so the key still holds the val. Decide the WRITE
    // on the leader's blind read and a neg lands here instead, and this reads `neg`.
    await expect(storedKind(r.cache)).resolves.toBe("val");

    // And the consequence that makes the probe matter: a THIRD caller, joined to nothing, reading on
    // its own after both waiters settled. It finds the val the store kept and is served it (the own
    // suppression, reached epistemically this time — this caller CAN see the copy). Where the neg had
    // landed, this caller and every one after it received `undefined` instead, permanently.
    const later = await r.cache.getOrSetEntry({
      ...SHARED,
      factory: notFound,
      tags: [r.tag],
    });
    expect(later.value).toBe("good");
    // Serving it did not displace it either.
    await expect(storedKind(r.cache)).resolves.toBe("val");
  });

  test("INV-26/E7: the plain form likewise leaves the stored val standing, and serves it to the next independent reader", async () => {
    const r = rig();
    await seedStaleCopy(r);
    const { settled, factoryRuns } = await notFoundWithBlindLeader(
      r,
      async (factory) =>
        await r.cache.getOrSet({ ...SHARED, factory, tags: [r.tag] })
    );
    const [led, joined] = settled;

    expect({ factoryRuns, led, joined }).toStrictEqual({
      factoryRuns: 1,
      joined: { status: "fulfilled", value: "good" },
      // oxlint-disable-next-line sonarjs/no-undefined-assignment -- the plain blind leader RESOLVES undefined (a served not-found fact); the equality must assert that exact value.
      led: { status: "fulfilled", value: undefined },
    });

    await expect(storedKind(r.cache)).resolves.toBe("val");
    await expect(
      r.cache.getOrSet({ ...SHARED, factory: notFound, tags: [r.tag] })
    ).resolves.toBe("good");
  });

  // JURISDICTION OF THIS TEST, stated because it is narrower than it looks and a reader who
  // mistakes its scope will misread a green. It pins ROUTE-independence WITHIN ONE WORLD: both
  // sides below are blind seats over the same seeded store, differing only in whether a second
  // caller joined. Its RED CONDITION is therefore join-conditional divergence at either seat —
  // plumbing that makes "this caller led a singleflight" observable where the design erased it.
  // Seat-level changes cannot red it, and that is correct rather than a weakness: with the guard
  // sited at one boundary, a change at the write seat or the serve seat moves BOTH arms together
  // (measured: nine such mutations, none red here, each caught by its own tooth elsewhere).
  //
  // ITS RED CONDITION, stated so a later edit can be checked against it: a mutation reds this test
  // only if it makes the serve depend on ATTACHMENT — a suppressing seat that hands the blind caller
  // the envelope the WRITE layer's compare-fetch saw, instead of that caller's own not-found. That is
  // the optimization the design invites and the design forbids: a seat serves what its OWN read produced,
  // and the write layer's compare-fetch is not this caller's read.
  //
  // WHAT A CANDIDATE MUTATION MUST GATE ON, because two obvious choices are inert here and an inert
  // mutation reads as a tooth that does not bite. The factory-run count does NOT distinguish the arms
  // — a joiner shares the leader's fill and a self-serving caller never fills, so it is 1 either way
  // — and neither does a count of reads. Attachment itself is the discriminator. The leaked value is
  // the envelope's `val`; there is no `value` field, and reading one yields `undefined`, which is
  // what the seat already serves. Any such gate must be scoped PER CACHE INSTANCE: a process-wide one
  // feeds the solo control too, moves both arms together, and leaves this test green against a
  // mutation it should have caught.
  //
  // Current code cannot produce that divergence. The validation's own record — the mutant's exact
  // edits, the pair it ran against, and the reds observed — is kept where records of that kind belong
  // and cited from there.
  test("INV-26/E7: what the write layer SAW never leaks into the blind seat's own serve — it resolves exactly as its identical solo call, per form", async () => {
    const r = rig();
    await seedStaleCopy(r);
    const entryRun = await notFoundWithBlindLeader(
      r,
      async (factory) =>
        await r.cache.getOrSetEntry({ ...SHARED, factory, tags: [r.tag] })
    );
    const [ledEntry] = entryRun.settled;

    const p = rig();
    await seedStaleCopy(p);
    const plainRun = await notFoundWithBlindLeader(
      p,
      async (factory) =>
        await p.cache.getOrSet({ ...SHARED, factory, tags: [p.tag] })
    );
    const [ledPlain] = plainRun.settled;

    // THE ORACLES, per form: the identical SOLO blind call, run in THIS RIG'S OWN WORLD — same
    // seeded store, same blinded read, one caller instead of two. An oracle whose store were EMPTY
    // would be a different world, and demanding the two agree would pin world-convergence rather
    // than the route-independence this test is about.
    const blindEntryOracle = await soloBlindAgainstSeeded(
      async (cache, tag) =>
        await cache.getOrSetEntry({ ...SHARED, factory: notFound, tags: [tag] })
    );
    const blindPlainOracle = await soloBlindAgainstSeeded(
      async (cache, tag) =>
        await cache.getOrSet({ ...SHARED, factory: notFound, tags: [tag] })
    );

    // THE TOOTH, and it pins ROUTE-independence: joining changes nothing a caller can see. Both
    // sides inhabit the same world — seeded store, blinded read — and differ only in whether a
    // second caller was present, so what this equality forbids is the JOIN leaking into the serve:
    // hand the blind seat the val it never read, or vary its evidence by whether it led a join, and
    // the two stop matching while every disposition test above stays green. It deliberately does
    // NOT pin the suppressed turn's `durable`: that is a fact about the STORE, both sides are
    // suppressed identically, and pinning it against a differently-stocked world is exactly what
    // made the earlier oracle a fossil of the defect (see the amended oracle above).
    expect({
      entry: ledEntry.status === "fulfilled" ? norm(ledEntry.value) : ledEntry,
      plain: ledPlain.status === "fulfilled" ? ledPlain.value : ledPlain,
    }).toStrictEqual({
      entry: norm(blindEntryOracle),
      plain: blindPlainOracle,
    });
  });

  test("INV-7/E7: a genuine TRUE MISS still writes the neg — the store holds it afterwards", async () => {
    // The non-degeneracy arm stated where the guard actually sits. The suites above prove a neg is
    // refused over a protected val; this proves the refusal is CONDITIONAL. A guard that suppressed
    // unconditionally would satisfy every suppression claim in this file and cache no 404 ever
    // again — and `explain()` is what tells the two apart, because both serve the same `undefined`.
    const r = rig(); // no `seedStaleCopy`: nothing is stored, so the write meets a true miss.
    await expect(storedKind(r.cache)).resolves.toBe("absent");

    const served = await r.cache.getOrSetEntry({
      ...SHARED,
      factory: notFound,
      tags: [r.tag],
    });
    expect(served.value).toBeUndefined();

    // THE TOOTH: the neg LANDED. Suppress unconditionally and this reads `absent`.
    await expect(storedKind(r.cache)).resolves.toBe("neg");
  });

  // The design gives the fill turn's `durable` a single meaning: a report on THIS turn's own write,
  // awaited past settle. A suppressed write is a settle with a KNOWN outcome — the neg landed
  // nowhere — so the honest report is `false`, and `true` would assert a durable not-found that
  // provably does not exist. Note what this pins and how it differs from the equivalence pins
  // above: those hold their world FIXED and forbid the route from mattering; this one VARIES the
  // world deliberately and requires the report to follow it, because whether the store holds a val
  // is state, and state is exactly what a caller's outcome may turn on.
  test("INV-26/E7/E29: a SUPPRESSED neg-write reports durable:false while a neg that genuinely LANDS reports durable:true — the field tracks this turn's own write, not the tier's tidiness", async () => {
    // SUPPRESSED ARM — a servable copy is stored and this caller's read is blinded, so the write
    // seat meets the val the caller never saw and refuses the neg.
    const s = rig();
    await seedStaleCopy(s);
    s.arm.blind = true;
    const suppressed = await s.cache.getOrSetEntry({
      ...SHARED,
      factory: notFound,
      tags: [s.tag],
    });
    // Non-vacuity, arm one: the suppression actually happened — the val still stands.
    await expect(storedKind(s.cache)).resolves.toBe("val");

    // TRUE-MISS ARM — nothing stored anywhere, so the same call writes its neg for real.
    const m = rig();
    const landed = await m.cache.getOrSetEntry({
      ...SHARED,
      factory: notFound,
      tags: [m.tag],
    });
    // Non-vacuity, arm two: this neg genuinely reached the durable tier.
    await expect(storedKind(m.cache)).resolves.toBe("neg");

    // THE TOOTH, pinned in BOTH directions so that no constant satisfies it: report `true`
    // unconditionally and the suppressed arm reds; report `false` unconditionally and the landed
    // arm reds. Before this existed, reverting the suppressed turn to `true` left all 542 tests
    // green — the field's meaning on this path was law with nothing holding it.
    expect({
      landed: landed.durable,
      suppressed: suppressed.durable,
    }).toStrictEqual({ landed: true, suppressed: false });
  });
});

/** A retriable origin failure (503) — the `staleIfError` condition, which a 404 is NOT. */
const serverError = async (): Promise<never> => {
  throw new HttpError(new Response(null, { status: 503 }));
};

describe("E7 suppression vs the error-path grace serve — the flag names an event (E7, E31, §5.4, §11.18)", () => {
  // the seat holding the protected copy serves it in the grace path's own SHAPE — stale,
  // re-validated at serve time — but NOT carrying `servedOnError`. That flag marks a
  // stale-if-error serve, and a not-found is not an error condition: the origin answered, and this
  // system chose to protect a stored value against that answer. Setting the flag would assert an
  // event that never happened, and it would collapse a distinction the serve kinds are built on —
  // "the origin failed and grace rescued this" and "the origin says gone and The neg-write guard protects the value"
  // are different worlds to whoever reads the evidence.
  test("INV-26/E7/E31: a suppressed serve is stale WITHOUT servedOnError, while the same value served through a retriable failure carries both", async () => {
    // Same world both times — a stored copy, soft-purged so it is stale-but-servable, and a caller
    // that can see it. The ONLY difference is what the origin did.
    const suppressed = await solo(
      true,
      async (cache, tag) =>
        await cache.getOrSetEntry({ ...SHARED, factory: notFound, tags: [tag] })
    );
    const graced = await solo(
      true,
      async (cache, tag) =>
        await cache.getOrSetEntry({
          ...SHARED,
          factory: serverError,
          tags: [tag],
        })
    );

    // Non-vacuity: both really did serve the stored value rather than failing or missing. Without
    // this the flag comparison below could be satisfied by two serves that never happened.
    expect({
      graced: graced.value,
      suppressed: suppressed.value,
    }).toStrictEqual({ graced: "good", suppressed: "good" });

    // THE TOOTH, both arms in one assertion so that no constant satisfies it: set the flag
    // unconditionally and the suppressed row gains `servedOnError: true`; drop it entirely and the
    // graced row loses it. `stale` is asserted on both to pin that the two serves differ on the
    // FLAG alone and not on the shape of the serve.
    expect({
      graced: { servedOnError: graced.servedOnError, stale: graced.stale },
      suppressed: {
        servedOnError: suppressed.servedOnError,
        stale: suppressed.stale,
      },
    }).toStrictEqual({
      graced: { servedOnError: true, stale: true },
      // oxlint-disable-next-line sonarjs/no-undefined-assignment -- the ABSENCE of the flag is the claim; `null` would assert a different shape and pass while the real defect shipped.
      suppressed: { servedOnError: undefined, stale: true },
    });
  });
});
