// INV-5 (singleflight)
// INV-18 (the graced serve, observed through a join)
// Sacred: append-only. Never modify, delete, weaken, skip, or .todo an existing test.
//
// Singleflight's contract is that a joiner is handed the LEADER's result. Grace's
// contract is that a retriable origin failure still serves a servable stored copy.
// Where they cross — an origin outage with several readers on one key — the two have to agree
// about one thing: what the joiner is told.
//
// Evidence is a property of the SERVE, not of the caller. A joiner never ran the fill; it awaited
// the leader's promise, and its own reads describe a different moment. So the value it receives and
// the evidence describing that value must come from the same place — the shared result — or the
// joiner narrates a read it never performed, or refuses a value it was handed.
//
// The rig hand-wires the primitives (the INV-15 precedent) for one thing `world()` cannot express:
// a leader whose read finds the candidate and a joiner whose read does not. That asymmetry is what
// proves the joiner is answered from the shared result rather than from its own state — where both
// happen to hold the same candidate, a kernel consulting either one looks correct.
//
// Ordering is explicit, not raced: the leader's factory is held open until the joiner has provably
// joined. `fc.scheduler()` (this suite's tool for shaping races) is deliberately NOT used here —
// these are not race tests. The interleaving is the fixture, and it is pinned so the tooth cannot
// pass or fail by seed.

import { createCache, FencedError, HttpError } from "@astilba/cache";
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

/**
 * A promise this test opens by hand — the fixture that makes the leader a leader: its factory
 * parks here while the joiner arrives, so "joiner" is a fact rather than a hope about scheduling.
 * `promise/avoid-new` is right that production code should await existing promises; a test that
 * must PAUSE execution at a chosen point has nothing to await but a promise it constructs itself.
 */
function gate(): Gate {
  let open!: () => void;
  // oxlint-disable-next-line promise/avoid-new -- a deferred is the construct under test's control point; there is no library promise to await here (see this function's doc).
  const held = new Promise<void>((resolve) => {
    open = resolve;
  });
  return { held, open };
}

/**
 * An L2 that answers DATA-key reads only while `budget.n` lasts, then reports absence. Registry
 * keys pass through untouched. Models the leader reading the stored copy and a later joiner
 * finding nothing — a shared tier that is flaky during the same incident that took the origin down.
 */
function fadingStore(inner: Store, budget: { n: number }): Store {
  return {
    delete: async (key: string): Promise<void> => {
      await inner.delete(key);
    },
    get: async (key, readKind) => {
      const found = await inner.get(key, readKind);
      // Registry/mirror keys are not what fades — only the DATA key the readers race over.
      const fades = !key.includes("__");
      const exhausted = fades && budget.n <= 0;
      if (fades && !exhausted) {
        budget.n -= 1;
      }
      // An exhausted budget makes the tier answer absence, which is a store's "no such key".
      return exhausted ? undefined : found;
    },
    set: async (key, value, options): Promise<void> => {
      await inner.set(key, value, options);
    },
  };
}

interface Rig {
  readonly cache: Cache;
  readonly budget: { n: number };
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
  const budget = { n: Number.MAX_SAFE_INTEGER };
  return {
    budget,
    cache: createCache({
      bus,
      clock,
      l2: fadingStore(store.replica(0), budget),
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
 * Drive the outage: ONE data read succeeds (the leader's), the leader parks in its factory until
 * the joiner has joined, then the origin fails for both. `run` is the caller's form of the verb, so
 * the same fixture serves the entry form and the plain form.
 */
async function outageWithJoiner<R>(
  r: Rig,
  run: (factory: () => Promise<never>) => Promise<R>
): Promise<readonly [PromiseSettledResult<R>, PromiseSettledResult<R>]> {
  const g = gate();
  r.budget.n = 1; // exactly ONE data read succeeds — the leader's; the joiner's finds absence.
  const leader = run(async () => {
    await g.held;
    return await outage();
  });
  await r.settle(); // the leader reads, registers its fill, and parks in the factory
  const joiner = run(outage);
  await r.settle(); // the joiner reads (absence), reaches singleflight, and JOINS
  g.open();
  const [a, b] = await Promise.allSettled([leader, joiner]);
  return [a, b];
}

const SHARED = {
  consistency: "strong",
  grace: "1h",
  key: "k",
} as const;

describe("grace across a singleflight join (§5.2, §11.18, E29)", () => {
  test("INV-5/INV-18: a joiner is served the leader's graced value AND reports the leader's evidence, though its own read found nothing", async () => {
    const r = rig();
    await seedStaleCopy(r);
    const [led, joined] = await outageWithJoiner(
      r,
      async (factory) =>
        await r.cache.getOrSetEntry({ ...SHARED, factory, tags: [r.tag] })
    );

    // Non-vacuity: the leader really did take the error-path grace serve. Without
    // this the comparison below could hold with both sides equally broken.
    expect(led.status === "fulfilled" && led.value.value).toBe("good");
    // THE TOOTH. The joiner holds no candidate of its own — its read found absence — yet it is
    // handed the leader's value, so it must describe THAT value: same tier, same staleness, same
    // durability, same error-path flag. One object because it is one claim: a serve and its
    // evidence travel together or they describe different things.
    expect(
      joined.status === "fulfilled" ? { ...joined.value, age: 0 } : joined
    ).toStrictEqual(
      led.status === "fulfilled" ? { ...led.value, age: 0 } : led
    );
  });

  test("INV-5/INV-18: the plain form does not throw at a joiner during an outage — a joiner whose own read found nothing is served the leader's graced value, not an error", async () => {
    const r = rig();
    await seedStaleCopy(r);
    const [led, joined] = await outageWithJoiner(
      r,
      async (factory) =>
        await r.cache.getOrSet({ ...SHARED, factory, tags: [r.tag] })
    );

    // An outage is exactly when a graced value must reach the caller that asked for it. Refusing a
    // joiner because its OWN read found nothing turns one origin failure into N-1 request failures
    // — the opposite of what grace exists to do.
    //
    // SCOPE, stated so the next reader does not over-read the title: this pins ONE direction — the
    // leader holds the candidate, the joiner does not. The mirror (a blind leader, a joiner that
    // read a servable copy itself) is NOT pinned here and does NOT hold today: the leader's failure
    // classification never sees a candidate, so it rethrows and the shared registration rejects the
    // joiner with it. That gap predates the evidence-riding shape and is untouched by it — evidence
    // cannot ride a serve that never happens — and nothing here locks the current behaviour in.
    expect([led, joined]).toStrictEqual([
      { status: "fulfilled", value: "good" },
      { status: "fulfilled", value: "good" },
    ]);
  });

  test("INV-5/INV-18 (phantom fence): no caller is told its fill was fenced — that error's text is a claim about a fence, and no fence fired here", async () => {
    const r = rig();
    await seedStaleCopy(r);
    const settled = await outageWithJoiner(
      r,
      async (factory) =>
        await r.cache.getOrSet({ ...SHARED, factory, tags: [r.tag] })
    );

    // A typed error's message is read by whoever is debugging the outage. `FencedError` asserts a
    // hard purge kept fencing this fill until its write-back budget ran out — a specific and
    // alarming story. On this path nothing was purged, no write-back was attempted, and the joiner
    // never even ran a factory. The class must be unreachable here: whoever is reading deserves the
    // real shape of the incident, not a fence that never fired.
    const fenced = settled.filter(
      (s) => s.status === "rejected" && s.reason instanceof FencedError
    );
    expect(fenced).toStrictEqual([]);
  });
});
