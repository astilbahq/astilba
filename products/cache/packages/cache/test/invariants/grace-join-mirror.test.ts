// INV-5 (singleflight)
// INV-18 (the graced serve, re-validated at serve time)
// Sacred: append-only. Never modify, delete, weaken, skip, or .todo an existing test.
//
// The MIRROR of the direction grace-join-evidence.test.ts pins. There the LEADER read the servable
// copy and a later joiner found nothing, so the joiner is served the leader's value+evidence
// through the shared result. Here the asymmetry is reversed: the LEADER reads NOTHING (blind) and a
// later JOINER reads the servable copy itself. The origin then fails RETRIABLY for both.
//
// the ladder is the CALLER's own per-read algorithm; the singleflight is a mechanism INSIDE
// step 5, not a substitute for it. A joiner that arrived at step 5 holding its OWN graced copy (from
// its OWN steps 2–4) qualifies for grace by the letter — the leader's error classification is an
// INPUT to the joiner's step-5 tail, never a verdict over it. So the shared failure a joiner is
// handed is the CLASSIFICATION (retriable / not) as a fact; each waiter runs its OWN step-5
// tail against its OWN read. A blind leader propagates; a sighted joiner serves its own copy.
//
// The rig hand-wires the primitives (the INV-15 precedent) for the one thing `world()` cannot
// express: a leader whose read finds nothing and a joiner whose read finds the servable copy. The
// interleaving is the fixture, pinned by a held factory (not `fc.scheduler`) — these are not race
// tests, so the tooth cannot pass or fail by seed.

import { createCache, HttpError } from "@astilba/cache";
import type { Cache, Store, Tag } from "@astilba/cache";
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

/** A retriable origin failure (503 ∈ the default retriable set), so grace engages. */
const outage = async (): Promise<never> => {
  throw new HttpError(new Response(null, { status: 503 }));
};

interface Gate {
  readonly held: Promise<void>;
  open(): void;
}

/** A promise this test opens by hand — the fixture that makes the leader a leader: its factory
 * parks here while the joiner arrives, so "joiner" is a fact rather than a hope about scheduling. */
function gate(): Gate {
  // SAFETY: the `new Promise` executor below runs SYNCHRONOUSLY, before the constructor returns, so
  // `open` is assigned before this function returns and can never be observed unassigned.
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
 * read find nothing (blind) while the later JOINER's read finds the stored copy (sighted) — the
 * mirror of the fading store next door, which blinds the joiner instead of the leader.
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
      // An armed DATA read answers absence (the leader reads nothing); all else passes through.
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
 * at serve time — a hard-purged one would not, and grace would never engage. */
async function seedStaleCopy(r: Rig): Promise<void> {
  await r.cache.getOrSet({
    factory: async () => await Promise.resolve("good"),
    key: "k",
    tags: [r.tag],
  });
  await r.cache.expire({ tag: r.tag });
}

/**
 * Drive the outage with a BLIND leader: arm the store so the leader's read finds nothing, hold the
 * leader in its factory until the joiner has joined, then fail the origin for both. `run` is the
 * caller's form of the verb, so the same fixture serves the entry form and the plain form.
 */
async function outageWithBlindLeader<R>(
  r: Rig,
  run: (factory: () => Promise<never>) => Promise<R>
): Promise<readonly [PromiseSettledResult<R>, PromiseSettledResult<R>]> {
  const g = gate();
  r.arm.blind = true; // the LEADER's read finds nothing; the joiner's finds the stored copy.
  const leader = run(async () => {
    await g.held;
    return await outage();
  });
  await r.settle(); // the leader reads (absence), registers its fill, and parks in the factory
  const joiner = run(outage);
  await r.settle(); // the joiner reads (the copy), reaches singleflight, and JOINS
  g.open();
  const [a, b] = await Promise.allSettled([leader, joiner]);
  return [a, b];
}

const SHARED = {
  consistency: "strong",
  grace: "1h",
  key: "k",
} as const;

describe("grace across a singleflight join — the mirror (§5.1 step 5, §5.2, §11.18)", () => {
  test("INV-5/INV-18: a blind leader's joiner is served its OWN re-validated copy, {stale, servedOnError}, with its OWN evidence — while the leader propagates the error", async () => {
    const r = rig();
    await seedStaleCopy(r);
    const [led, joined] = await outageWithBlindLeader(
      r,
      async (factory) =>
        await r.cache.getOrSetEntry({ ...SHARED, factory, tags: [r.tag] })
    );

    // The leader read nothing, so it has no copy to serve: it propagates the classified retriable
    // failure. Its blindness is a fact, not a verdict handed to the joiner.
    expect(led.status).toBe("rejected");
    expect(led.status === "rejected" && led.reason instanceof HttpError).toBe(
      true
    );

    // THE TOOTH. The joiner holds a servable copy of its OWN (its own read found it), so it serves
    // THAT — re-validated now — never the leader's disposition. Every field is the
    // joiner's own read's evidence: found in L2 (so `tier:'l2'`, `durable:true`), stale, and an
    // error-path serve (`servedOnError`). The leader supplied none of this; it had nothing.
    expect(
      joined.status === "fulfilled" ? { ...joined.value, age: 0 } : joined
    ).toStrictEqual({
      age: 0,
      durable: true,
      servedOnError: true,
      skipped: false,
      stale: true,
      tier: "l2",
      value: "good",
    });
  });

  test("INV-5/INV-18: the plain form serves the blind leader's joiner its own graced value rather than rejecting it with the leader's failure", async () => {
    const r = rig();
    await seedStaleCopy(r);
    const [led, joined] = await outageWithBlindLeader(
      r,
      async (factory) =>
        await r.cache.getOrSet({ ...SHARED, factory, tags: [r.tag] })
    );

    // One shared origin failure, two dispositions decided per-caller against each caller's own read
    //: the blind leader propagates it, the sighted joiner survives it by serving its
    // own copy. A kernel that leaked the leader's verdict onto the joiner would reject BOTH — turning
    // one origin failure into N failures, the opposite of what stale-if-error exists to do. A kernel
    // that decided the joiner's serve on the leader's (empty) read would strand it the same way.
    expect(led.status).toBe("rejected");
    expect(joined).toStrictEqual({ status: "fulfilled", value: "good" });
  });
});

/**
 * Like `outageWithBlindLeader`, but a HARD delete lands after the joiner has captured its own copy
 * and before the origin fails — raising the hard watermark past that copy's birth, so the joiner's
 * candidate is DEAD against live knowledge. Counts factory runs so the caller can prove a
 * genuine join (exactly one: the leader's; the joiner's never runs).
 */
async function outageWithBlindLeaderThenHardDelete<R>(
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
    return await outage();
  });
  await r.settle(); // the leader reads (absence), registers its fill, and parks in the factory
  const joiner = run(async () => {
    factoryRuns += 1;
    return await outage();
  });
  await r.settle(); // the joiner reads (the copy) and JOINS — capturing that copy as its candidate
  // A HARD delete NOW (not a soft expire): it raises the hard watermark above the captured copy's
  // birth, so the copy is dead against live knowledge by the time the joiner's step-5 tail runs.
  await r.cache.delete({ tag: r.tag });
  g.open();
  const settled = await Promise.allSettled([leader, joiner]);
  return { factoryRuns, settled };
}

// INV-18: the graced serve is re-validated at SERVE time, across a join. A joiner
// serves its OWN copy only while that copy is still servable; when a hard delete lands mid-fill the
// joiner REFUSES the serve and takes the classified failure instead.
//
// That refusal is NOT the write-back fence, and the vocabulary matters: no `FencedError` is
// minted on this path. The waiter loses its claim to a graced serve and takes the factory's own
// classified failure, which each FORM then surfaces its own way: the plain form
// rethrows the typed error, the entry form resolves a MISS.
//
// BOTH forms are pinned below, and pinning both is the point: a waiter's outcome must not depend on
// which caller won singleflight, so a joiner in this state must resolve exactly as a SOLO caller in
// this state resolves. An entry-form joiner that REJECTED here, while an identical solo call
// resolved a miss, would be the very defect this suite exists to close, one path over.
//
// The soft-purge complement — a joiner whose copy stays servable IS served — is the two tests
// above; together they bracket the re-validation both ways (blind it to `true` and the hard cases
// red; blind it to `false` and the soft cases red).
describe("grace across a singleflight join — the §11.18 serve-time re-validation", () => {
  test("INV-18: the entry form resolves a MISS for a joiner whose own copy a hard delete killed mid-fill — never the deleted value, and never a rejection", async () => {
    const r = rig();
    await seedStaleCopy(r);
    const { settled, factoryRuns } = await outageWithBlindLeaderThenHardDelete(
      r,
      async (factory) =>
        await r.cache.getOrSetEntry({ ...SHARED, factory, tags: [r.tag] })
    );
    const [led, joined] = settled;

    // A genuine join: the leader's factory ran once, the joiner's never.
    expect(factoryRuns).toBe(1);

    // The blind leader holds no copy at all, so it rethrows the classified failure.
    expect(led.status).toBe("rejected");

    // THE TOOTH. The joiner read a servable copy of its own, but a HARD delete landed during the
    // fill and raised the hard watermark above that copy's birth. Its step-5 tail re-validates the
    // copy NOW and finds it DEAD — a hard-purged value is never resurrected on the error
    // path — so it REFUSES the serve. The entry form surfaces that refusal as a MISS, the same
    // outcome a solo caller holding a dead copy resolves: the deleted value must not escape, AND the
    // joiner must not be rejected where a solo call would have resolved. Blinding the re-validation
    // to `true` serves "good" here, which is exactly the value that must never escape.
    expect(joined.status === "fulfilled" ? joined.value : joined).toStrictEqual(
      {
        age: 0,
        durable: false,
        skipped: false,
        stale: false,
        tier: "miss",
        // oxlint-disable-next-line sonarjs/no-undefined-assignment -- a miss entry carries `value` PRESENT and undefined, and `toStrictEqual` distinguishes that from an absent property; `null` would assert a different shape than the one the read path produces.
        value: undefined,
      }
    );
  });

  test("INV-18: the plain form rejects with the classified failure for a joiner whose own copy a hard delete killed mid-fill", async () => {
    const r = rig();
    await seedStaleCopy(r);
    const { settled, factoryRuns } = await outageWithBlindLeaderThenHardDelete(
      r,
      async (factory) =>
        await r.cache.getOrSet({ ...SHARED, factory, tags: [r.tag] })
    );
    const [led, joined] = settled;

    expect(factoryRuns).toBe(1);
    expect(led.status).toBe("rejected");

    // The SAME refusal, surfaced the plain form's way: the typed classified error, never the deleted
    // value. No `FencedError` is minted — this is not the write-back fence — so the reason the
    // caller sees is the origin's own `HttpError`.
    expect(joined.status).toBe("rejected");
    expect(
      joined.status === "rejected" && joined.reason instanceof HttpError
    ).toBe(true);
  });
});
