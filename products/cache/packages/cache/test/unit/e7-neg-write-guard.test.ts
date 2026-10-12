// Ordinary unit teeth (NOT sacred) — E7's KIND-AWARE WRITE GUARD (§5.1 step 5, E7/INV-26).
//
// Sacred pins the specified outcome — a live value survives a 404 — and that the store is left
// holding that val (negative-caching.test.ts, negative-caching-join.test.ts). These pin the guard's
// own arms — the conditions under which it refuses and the conditions under which it must not, plus
// the two things a refusal is obliged to leave behind:
//
//   1. release arm    — a val the letter does NOT protect (hard-purged ⇒ dead) admits the neg. This
//                       is the OVER-suppression check: a guard that protected every stored envelope
//                       would satisfy every suppression claim in the sacred file and never cache a
//                       404 over a dead entry again. (Blind: treat `dead` as protected ⇒ reds.)
//   2. unknown arm    — a window the guard cannot ESTABLISH resolves toward suppression. The two
//                       errors are not the same size: a wrongly-refused neg costs one origin
//                       round-trip and self-corrects on the next read, while a wrongly-landed neg
//                       destroys a live value with nothing left that remembers it. (Blind: let an
//                       unresolvable verdict admit the write ⇒ the val is buried ⇒ reds.)
//   3. telemetry      — `neg_suppressed` fires AT the suppression site, once, with the payload
//                       discipline the kernel's other write-path events use (`{key, type}`). A
//                       refusal is the one write a caller asked for that the kernel declined; with
//                       no event it is indistinguishable from a 404 nobody asked to cache.
//                       (Blind: drop the emit ⇒ reds. Control: a true miss emits nothing.)
//   4. eviction fate  — a refused write mutates NOTHING: no L2 write, and no L1 eviction. The
//                       eviction exists to stop a superseded L1 copy outliving a 404 the durable
//                       tier now remembers; a refusal means the durable tier remembers no 404, so
//                       deleting the val's L1 copy would contradict the verdict the refusal just
//                       rendered. (Blind: evict on every neg outcome ⇒ reds. Control: a neg that
//                       LANDS still evicts.)
//
// Lane: unit — each arm needs a caller BLIND to what the store holds (the only state in which the
// ontological guard and the filling caller's own read can disagree) and, for the unknown arm, a
// registry that fails mid-fill. Deterministic by construction: injected clock + seeded rng + fake
// drivers, an interleaving pinned by explicit arming rather than by scheduling. No timing tolerance,
// no retry.

import assert from "node:assert/strict";

import { createCache, HttpError, memory } from "@astilba/cache";
import type {
  Cache,
  ReadKind,
  Registry,
  RegistryAck,
  Store,
  StoreValue,
  StoreWriteOptions,
  Tag,
  TagKnowledge,
  TelemetryEvent,
} from "@astilba/cache";
import {
  FakeBus,
  FakeLock,
  FakeRegistry,
  FakeStore,
  mkTag,
  SimClock,
  SimRng,
} from "@astilba/harness";
import { describe, expect, it } from "vitest";

const notFound = async (): Promise<never> =>
  await Promise.reject(new HttpError(new Response(null, { status: 404 })));

/** The call shape every arm below uses. `grace` is declared so a SIGHTED caller in this state would
 * suppress epistemically — which is exactly what makes the BLIND caller the discriminating case. */
const SHARED = {
  consistency: "strong",
  grace: "1h",
  key: "k",
  notFoundTtl: "30s",
} as const;

/** A `Store` that answers absence for the next `blind` DATA-key reads, then reverts — the fixture
 * that makes a caller blind to what this tier is really holding (a tier unreadable for a moment;
 * §5.2's leader that read nothing). Registry / mirror keys (`__…`) pass through untouched, and never
 * consume the budget. The write layer's OWN compare read is the one that must still see the store,
 * so the budget is spent by the tier reads that precede it. */
function blindingStore(inner: Store, arm: { blind: number }): Store {
  return {
    delete: async (key: string): Promise<void> => {
      await inner.delete(key);
    },
    get: async (
      key: string,
      readKind?: ReadKind
    ): Promise<StoreValue | undefined> => {
      const found = await inner.get(key, readKind);
      if (key.includes("__") || arm.blind <= 0) {
        return found;
      }
      arm.blind -= 1;
      return undefined;
    },
    set: async (
      key: string,
      value: string,
      options?: StoreWriteOptions
    ): Promise<void> => {
      await inner.set(key, value, options);
    },
  };
}

/** An L1 that records the keys it was asked to WRITE and to DELETE, and exposes the tier underneath
 * so a test can probe L1's own contents rather than inferring them from a serve. The written key is
 * recorded rather than reconstructed, so the probe cannot drift from the kernel's canonical-key
 * rule (§3). */
interface RecordingL1 {
  readonly inner: Store;
  readonly store: Store;
  readonly written: string[];
  readonly deleted: string[];
}
function recordingL1(inner: Store): RecordingL1 {
  const wrapper: RecordingL1 = {
    deleted: [],
    inner,
    store: {
      delete: async (key: string): Promise<void> => {
        wrapper.deleted.push(key);
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
        wrapper.written.push(key);
        await inner.set(key, value, options);
      },
    },
    written: [],
  };
  return wrapper;
}

/** The registry's own failure, raised as a class so oxlint's throw rules are satisfied. */
class RegistryDownError extends Error {
  constructor() {
    super("registry unreachable");
    this.name = "RegistryDownError";
  }
}

/** A `Registry` whose live CHECK rejects while armed — everything else passes through. Armed from
 * INSIDE the factory, so the fill's own pre-factory live check has already succeeded and the only
 * check left to fail is the guard's. That is what isolates the unknown arm: the caller reaches the
 * write layer normally and the guard alone is denied a verdict. */
function failableRegistry(inner: Registry, arm: { down: boolean }): Registry {
  return {
    check: async (
      tags: readonly string[]
    ): Promise<readonly TagKnowledge[]> => {
      if (arm.down) {
        throw new RegistryDownError();
      }
      return await inner.check(tags);
    },
    delete: async (tags: readonly string[]): Promise<RegistryAck> =>
      await inner.delete(tags),
    expire: async (tags: readonly string[]): Promise<RegistryAck> =>
      await inner.expire(tags),
    regId: inner.regId,
    registerRetention: async (retentionMs: number): Promise<void> => {
      await inner.registerRetention(retentionMs);
    },
  };
}

interface Rig {
  readonly cache: Cache;
  readonly blind: { blind: number };
  readonly corrupt: { mode: CorruptMode };
  readonly registryArm: { down: boolean };
  readonly l1: RecordingL1;
  readonly events: TelemetryEvent[];
  readonly tag: Tag;
}

/** An L2 whose reads make the stored entry UNREADABLE to this instance, on the two axes the read
 * path fails closed on: `"codec"` stamps a foreign codecId (E8/INV-27), `"decode"` leaves the
 * codecId intact and replaces the body with bytes this codec cannot decode (INV-8). `"none"` passes
 * through, so arming happens after seeding and no existing fixture changes shape. */
type CorruptMode = "none" | "codec" | "decode";

function corruptibleStore(inner: Store, arm: { mode: CorruptMode }): Store {
  return {
    delete: async (key: string): Promise<void> => {
      await inner.delete(key);
    },
    get: async (
      key: string,
      readKind?: ReadKind
    ): Promise<StoreValue | undefined> => {
      const stored = await inner.get(key, readKind);
      // Only the stored VALUE entry is made unreadable. That is the scenario's actual shape: the
      // val was written by an earlier codec generation, while anything this instance writes now
      // (the neg under test) is its own and perfectly readable. Corrupting indiscriminately would
      // also swallow the neg and make the fixed tree look identical to the broken one.
      if (
        arm.mode === "none" ||
        stored?.metadata === undefined ||
        stored.metadata.kind !== "val"
      ) {
        return stored;
      }
      return arm.mode === "decode"
        ? { metadata: stored.metadata, value: "{ not decodable" }
        : {
            metadata: { ...stored.metadata, codecId: "foreign-codec:v9" },
            value: stored.value,
          };
    },
    set: async (
      key: string,
      value: string,
      options?: StoreWriteOptions
    ): Promise<void> => {
      await inner.set(key, value, options);
    },
  };
}

function rig(): Rig {
  const clock = new SimClock();
  const store = new FakeStore(clock, { replicas: 1 });
  const bus = new FakeBus(clock);
  const registryArm = { down: false };
  const registry = failableRegistry(
    new FakeRegistry(clock, { bus, regId: "reg", store: store.replica(0) }),
    registryArm
  );
  const blind = { blind: 0 };
  const corrupt: { mode: CorruptMode } = { mode: "none" };
  const l1 = recordingL1(memory());
  const events: TelemetryEvent[] = [];
  return {
    blind,
    corrupt,
    cache: createCache({
      bus,
      clock,
      // Blinding wraps the recorder, not the other way round: the budget must be spent by the TIER
      // reads (§5.1 steps 2/4), and the recorder still sees every write and delete underneath it.
      l1: blindingStore(l1.store, blind),
      l2: blindingStore(corruptibleStore(store.replica(0), corrupt), blind),
      lock: new FakeLock(),
      namespace: "ns",
      registry,
      rng: new SimRng(1),
      telemetry: {
        sink: (event: TelemetryEvent): void => {
          events.push(event);
        },
      },
    }),
    events,
    l1,
    registryArm,
    tag: mkTag("t0"),
  };
}

/** Seed a stored value for the key both tiers then hold. */
async function seed(r: Rig): Promise<void> {
  await r.cache.getOrSet({
    factory: async () => await Promise.resolve("good"),
    key: "k",
    tags: [r.tag],
  });
}

/** Run one 404 fill with the caller BLIND at both tiers — it reads nothing of its own, so what the
 * store holds is decided at the write layer or not at all. `duringFactory` runs after the fill's own
 * pre-factory work and before the 404, which is where a mid-fill fault belongs. */
async function blind404(r: Rig, duringFactory?: () => void): Promise<unknown> {
  r.blind.blind = 2; // the L1 tier read, then the L2 tier read (§5.1 steps 2/4).
  const entry = await r.cache.getOrSetEntry({
    ...SHARED,
    factory: async () => {
      duringFactory?.();
      return await notFound();
    },
    tags: [r.tag],
  });
  return entry.value;
}

/** What the key actually HOLDS afterwards, as the non-hydrating witness reports it (E27) — the only
 * oracle that can tell a val that survived from a val a correct-looking serve displaced. */
async function storedKind(cache: Cache): Promise<string> {
  const seen = await cache.explain("k");
  return seen.kind === "present" ? seen.identity.kind : seen.kind;
}

function suppressions(events: readonly TelemetryEvent[]): TelemetryEvent[] {
  return events.filter((e) => e.type === "neg_suppressed");
}

describe("E7 kind-aware neg-write guard — which stored vals it protects (§5.1 step 5, E7/INV-26)", () => {
  it("RELEASE ARM: a val the letter releases (hard-purged ⇒ dead) admits the neg, while a val it protects refuses it", async () => {
    // The DEAD arm. A hard purge leaves the bytes in the store but the entry is dead to every
    // reader, so there is no servable value for E7 to protect and the 404 is the newer truth.
    const dead = rig();
    await seed(dead);
    await dead.cache.delete({ tag: dead.tag });
    await expect(blind404(dead)).resolves.toBeUndefined();

    // The PROTECTED arm, as the discriminating control on the same fixture: a soft purge leaves the
    // val stale — still within grace, still servable — so the neg is refused.
    const alive = rig();
    await seed(alive);
    await alive.cache.expire({ tag: alive.tag });
    await expect(blind404(alive)).resolves.toBeUndefined();

    // THE TOOTH, stated as the PAIR: both callers were equally blind, both got the same 404, and
    // both were served the same not-found fact — only what the store was left holding differs, and
    // it differs on exactly the term E7 names. Protect every stored envelope and the first reads
    // `val`; release every stored envelope and the second reads `neg`.
    await expect(
      Promise.all([storedKind(dead.cache), storedKind(alive.cache)])
    ).resolves.toStrictEqual(["neg", "val"]);

    // Non-vacuity: both writes really did reach the guard and it really did answer them differently
    // — the released one was never refused, the protected one was refused exactly once. Without this
    // the pair above could also be produced by a caller that never got as far as the write layer.
    expect({
      dead: suppressions(dead.events).length,
      alive: suppressions(alive.events).length,
    }).toStrictEqual({ alive: 1, dead: 0 });
  });

  it("UNKNOWN ARM: a window the guard cannot establish resolves toward suppression — the val stands", async () => {
    const r = rig();
    await seed(r);
    await r.cache.expire({ tag: r.tag });

    // The registry goes down INSIDE the factory: the fill's pre-factory live check already ran, so
    // the guard's own check is the only one denied a verdict. The §4.1 table has a name for that
    // state — `unknown` — reached here by a throw rather than by a value.
    await expect(
      blind404(r, () => {
        r.registryArm.down = true;
      })
    ).resolves.toBeUndefined();
    r.registryArm.down = false;

    // THE TOOTH. A gap is not permission. The asymmetry is the derivation: refusing this neg costs
    // one origin round-trip, and the next read re-asks the origin and writes it then; landing it
    // would destroy a live value permanently, with every later reader served the negative. Resolve
    // an unestablishable window the other way and this reads `neg`.
    await expect(storedKind(r.cache)).resolves.toBe("val");
    // And it is genuinely the guard's refusal, not a fill that never got that far.
    expect(suppressions(r.events)).toHaveLength(1);
  });
});

describe("E7 kind-aware neg-write guard — what a refusal leaves behind (§5.1 step 5, §10, E7)", () => {
  it("TELEMETRY: `neg_suppressed` fires once at the suppression site, with the write path's payload discipline", async () => {
    const r = rig();
    await seed(r);
    await r.cache.expire({ tag: r.tag });
    await blind404(r);

    // ONCE, at the suppression site, with the payload discipline the kernel's sibling write-path
    // events carry: the event NAME plus the key reference the sink pseudonymizes, and nothing else.
    // A refusal reports THAT it happened and for which key; the stored value's own bytes are never an
    // operator's to receive. Count and shape in one claim, so neither can pass while the other slips.
    expect(
      suppressions(r.events).map((e) => Object.keys(e).toSorted())
    ).toStrictEqual([["key", "type"]]);

    // CONTROL: a genuine true miss emits nothing — the event marks a DECLINED write, not every neg.
    // Without it "emit on every neg-write" would satisfy the claim above.
    const missed = rig();
    await blind404(missed);
    expect({
      fired: suppressions(missed.events).length,
      stored: await storedKind(missed.cache),
    }).toStrictEqual({ fired: 0, stored: "neg" });
  });

  it("EVICTION FOLLOWS THE WRITE'S FATE: a refused neg evicts nothing from L1, while a neg that LANDS still does", async () => {
    const r = rig();
    await seed(r); // the value is written through to L1 as well as L2.
    await r.cache.expire({ tag: r.tag });
    // The key the seeding fill actually hydrated L1 under — the tier's own record of what it wrote,
    // never reconstructed here, so this probe cannot drift from the kernel's canonical-key rule (§3).
    const [l1Key] = r.l1.written;
    assert.ok(
      l1Key !== undefined,
      "the seeding value fill must have hydrated L1 for this tooth to mean anything"
    );
    const l1Holds = async (): Promise<boolean> =>
      (await r.l1.inner.get(l1Key)) !== undefined;
    await expect(l1Holds()).resolves.toBe(true);

    await blind404(r);

    // THE TOOTH. The eviction exists to stop a superseded L1 copy outliving a 404 the durable tier
    // now remembers. Here the durable tier remembers no 404 — the verdict the refusal rendered is
    // that the val stands — so evicting that val's L1 copy would contradict the verdict on the way
    // out and cost a refill of a value that is still good. Stated twice in one claim: no delete was
    // issued, and L1 still holds the entry.
    expect({
      deleted: r.l1.deleted,
      holds: await l1Holds(),
    }).toStrictEqual({ deleted: [], holds: true });

    // CONTROL: the eviction is not merely gone. A neg that LANDS still evicts, so "never evict"
    // cannot pass this pair either.
    const landed = rig();
    await seed(landed);
    await landed.cache.delete({ tag: landed.tag }); // dead ⇒ the guard releases ⇒ the neg lands
    await blind404(landed);
    expect({
      evictions: landed.l1.deleted.length,
      stored: await storedKind(landed.cache),
    }).toStrictEqual({ evictions: 1, stored: "neg" });
  });
});

describe("E7 kind-aware neg-write guard — SERVABLE is the read path's answer (§5.1 step 5, E7/INV-26, E8/INV-27, E31)", () => {
  // E7 protects a SERVABLE good value, and servability has a signed definition the read path
  // already enforces: a codecId this instance does not accept is a miss (E8/INV-27), and a body it
  // cannot decode is a miss (INV-8). An envelope failing either is not a val — it is a miss wearing
  // bytes, invisible to every reader here — so protecting it protects a ghost. The failure mode is
  // an unbounded origin amplifier rather than a wrong answer: the neg can never land, so every
  // request pays the origin for as long as the condition holds, and no reader can ever be served
  // the thing being protected. Both arms below are the same tooth through the two gates.
  const AMPLIFIER_CALLS = 4;

  async function askRepeatedly(
    r: Rig
  ): Promise<{ originCalls: number; kind: string }> {
    let originCalls = 0;
    // Hoisted: one closure, created once, so the loop declares no function that captures a
    // mutable binding from its own scope.
    const countingFactory = async (): Promise<never> => {
      originCalls += 1;
      return await notFound();
    };
    for (let i = 0; i < AMPLIFIER_CALLS; i += 1) {
      // Each ask blinds its OWN L1 read, so the fill meets the L2 entry rather than a local hit.
      // Note what this fixture is and is not: L1 still holds the value written at seed time — it is
      // the tier READ that is blinded, not the tier that is empty — so the durable-tier witness
      // below can report `val` on the strength of that copy even when nothing readable sits in L2.
      // The real-world shape this stands in for is an instance whose codec generation changed; the
      // blinding is how a unit test reaches that state without a second codec.
      r.blind.blind = 1;
      // oxlint-disable-next-line no-await-in-loop -- sequential BY DESIGN: the point is what the
      // second and later calls find after the first one's write did or did not land.
      await r.cache.getOrSetEntry({
        ...SHARED,
        factory: countingFactory,
        tags: [r.tag],
      });
    }
    return { kind: await storedKind(r.cache), originCalls };
  }

  it("AMPLIFIER ARM (codecId mismatch): a val no reader can classify protects nothing — the neg lands and the origin is asked ONCE", async () => {
    const ghost = rig();
    await seed(ghost);
    ghost.corrupt.mode = "codec"; // armed AFTER seeding: the bytes are real, this instance cannot read them.

    // The discriminating control on the same fixture: an identical rig whose stored val this
    // instance CAN read, soft-purged so it is stale-but-servable — the val E7 exists to protect.
    const real = rig();
    await seed(real);
    await real.cache.expire({ tag: real.tag });

    const [ghosted, protectedVal] = await Promise.all([
      askRepeatedly(ghost),
      askRepeatedly(real),
    ]);

    // THE TOOTH, stated as the pair. Where no reader could ever be served the stored entry, the 404
    // is cached and the origin is asked once. Where the entry is genuinely servable, the guard
    // refuses and the val stands. Reconstruct the stored envelope at the write seat WITHOUT the
    // read path's gates — the shortcut this replaces — and the first row becomes
    // `{ kind: "val", originCalls: 4 }` (measured): the unreadable entry is protected as though it
    // were servable, so the neg never lands and every ask pays the origin again, for as long as the
    // condition holds.
    expect({ ghost: ghosted, real: protectedVal }).toStrictEqual({
      ghost: { kind: "neg", originCalls: 1 },
      real: { kind: "val", originCalls: AMPLIFIER_CALLS },
    });

    // Non-vacuity, and the sharpest single number: the ghost was never suppressed at all, while the
    // servable val was suppressed on every ask. Without this the pair above could be produced by a
    // guard that simply stopped running.
    expect({
      ghost: suppressions(ghost.events).length,
      real: suppressions(real.events).length,
    }).toStrictEqual({ ghost: 0, real: AMPLIFIER_CALLS });
  });

  it("AMPLIFIER ARM (undecodable body): the same tooth through the decode gate rather than the codecId gate", async () => {
    const ghost = rig();
    await seed(ghost);
    // codecId intact, body unreadable — so this arm cannot pass by matching on codecId alone, and
    // it fails only if the guard actually decodes what the read path decodes.
    ghost.corrupt.mode = "decode";

    await expect(askRepeatedly(ghost)).resolves.toStrictEqual({
      kind: "neg",
      originCalls: 1,
    });
    expect(suppressions(ghost.events)).toHaveLength(0);
  });
});

describe("E7 kind-aware neg-write guard — the two terms that had no witness (§5.1 step 5, E7/INV-26, §4.1)", () => {
  it("KIND TERM: a stored NEG is not a protected val — a second 404 lands over it, while a stored val refuses one", async () => {
    // The guard's predicate excludes `kind === "neg"` before it ever looks at the window. Without a
    // witness that term is invisible: every suppression test in this file seeds a VAL, so a guard
    // that protected negatives too would satisfy all of them and quietly stop refreshing negatives.
    const negs = rig(); // no seed: the first 404 meets a true miss and lands.
    await expect(blind404(negs)).resolves.toBeUndefined();
    // The SECOND 404 now meets a stored NEG — the first one landed, since the store was empty.
    // It must land too: a negative does not protect anything. (What the store holds after both is
    // asserted in the paired claim below, so it is not re-checked here.)
    await expect(blind404(negs)).resolves.toBeUndefined();

    // The discriminating control on the same shape — a stored VAL, which does protect.
    const val = rig();
    await seed(val);
    await val.cache.expire({ tag: val.tag });
    await expect(blind404(val)).resolves.toBeUndefined();

    // THE TOOTH, as the pair: both stores held something, both callers were blind, both got the same
    // 404 — and the guard answered on KIND alone. Drop the `kind` term and the first row becomes 1.
    expect({
      overNeg: suppressions(negs.events).length,
      overVal: suppressions(val.events).length,
    }).toStrictEqual({ overNeg: 0, overVal: 1 });
    await expect(
      Promise.all([storedKind(negs.cache), storedKind(val.cache)])
    ).resolves.toStrictEqual(["neg", "val"]);
  });

  it("REGISTRY-ABSENT ARM: with no watermark authority every stored entry validates FRESH, so a servable val still refuses the neg", async () => {
    // A cache built without registry/bus has no §4.1 authority, so this arm — not `validate` — IS
    // the whole E7 guard for it. Every other test here runs with a registry, so the arm carried no
    // witness at all: blind it to `false` and a registry-less deployment buries live values on any
    // transient 404, with every gate green.
    const clock = new SimClock();
    const store = new FakeStore(clock, { replicas: 1 });
    const blind = { blind: 0 };
    const events: TelemetryEvent[] = [];
    const cache = createCache({
      clock,
      l2: blindingStore(store.replica(0), blind),
      namespace: "ns",
      rng: new SimRng(1),
      telemetry: {
        sink: (event: TelemetryEvent): void => {
          events.push(event);
        },
      },
    });
    const tag = mkTag("t0");
    await cache.getOrSet({
      factory: async () => await Promise.resolve("good"),
      key: "k",
      tags: [tag],
    });

    blind.blind = 1; // no L1 here, so one unit blinds the single tier read.
    await expect(
      cache.getOrSetEntry({ ...SHARED, factory: notFound, tags: [tag] })
      // oxlint-disable-next-line sonarjs/no-undefined-assignment -- the served not-found's value is PRESENT and undefined; `null` would assert a different shape and the matcher must see the real one.
    ).resolves.toMatchObject({ value: undefined });

    // THE TOOTH: the val stands, and the refusal was rendered rather than skipped. Return `false`
    // from the registry-absent arm and this reads `neg` with no event.
    const seen = await cache.explain("k");
    expect({
      kind: seen.kind === "present" ? seen.identity.kind : seen.kind,
      suppressed: suppressions(events).length,
    }).toStrictEqual({ kind: "val", suppressed: 1 });
  });
});

describe("E7 suppression telemetry — one event per suppression, at the seat that rendered it (E7, E31, §10)", () => {
  it("the SERVE seat emits too: a sighted caller that suppresses reports it, exactly as a blind caller's refused write does", async () => {
    // E31 attaches `neg_suppressed` to whichever seat renders the suppression. The two seats are
    // reached by different callers: a caller holding the protected copy suppresses at the SERVE
    // seat and serves its own value, while a caller blind to it reaches the WRITE layer's guard.
    // Only the second had a witness before this, which left the commoner case silent — the one E7's
    // own rationale describes.
    const sighted = rig();
    await seed(sighted);
    await sighted.cache.expire({ tag: sighted.tag }); // stale-but-servable
    // NOT blinded: this caller's own read finds the copy, so it suppresses at the serve seat.
    const served = await sighted.cache.getOrSetEntry({
      ...SHARED,
      factory: notFound,
      tags: [sighted.tag],
    });
    expect(served.value).toBe("good");

    const blind = rig();
    await seed(blind);
    await blind.cache.expire({ tag: blind.tag });
    await expect(blind404(blind)).resolves.toBeUndefined();

    // THE TOOTH: both seats rendered one suppression each and both said so. Drop the serve-seat
    // emit and the first count reads 0 while every disposition test in this file stays green.
    expect({
      serveSeat: suppressions(sighted.events).length,
      writeSeat: suppressions(blind.events).length,
    }).toStrictEqual({ serveSeat: 1, writeSeat: 1 });
  });
});

describe("E7 guard × the neg-write eviction — one posture, two halves (§5.1 step 5, §3, E7/INV-26, E13)", () => {
  // JURISDICTION: pins the guard/eviction COMPOSITION — the write that records the fact retires the
  // superseded copy, so coexistence is bounded by the WRITE and not by residency. The guard's scope
  // is the store it writes; it does not consult L1, because a per-isolate tier deciding a durable
  // outcome would make that outcome depend on which isolate raced first. What keeps the two tiers
  // honest is not the guard reaching wider but the eviction firing on the same event.
  it("COEXISTENCE: an L1-servable val does not block the L2 neg, and the landing neg retires it", async () => {
    const r = rig();
    await seed(r); // the value is now in L1 and in L2
    // L2's copy becomes unreadable to this instance while L1's stays perfectly readable — the state
    // in which "servable" genuinely differs by tier, and the only state where the guard's scope is
    // observable at all.
    r.corrupt.mode = "codec";

    // One ask with its own L1 read blinded, so the fill meets the unreadable L2 entry and reaches
    // the write layer's guard rather than being served locally.
    r.blind.blind = 1;
    await expect(
      r.cache.getOrSetEntry({ ...SHARED, factory: notFound, tags: [r.tag] })
    ).resolves.toMatchObject({
      // oxlint-disable-next-line sonarjs/no-undefined-assignment -- the served not-found's value is PRESENT and undefined; `null` would assert a different shape.
      value: undefined,
    });

    // HALF ONE — the neg LANDED. An entry no reader on this instance can classify is not a servable
    // val, so it protects nothing, whatever L1 happens to hold. Make the guard consult L1 and this
    // reads `val`: the durable tier would never learn the origin's latest word.
    await expect(storedKind(r.cache)).resolves.toBe("neg");

    // HALF TWO — the same isolate's very next read is served the NEGATIVE, not the stale local copy.
    // The write that recorded the fact retired the superseded L1 entry in the same motion (§3's
    // eviction, which carries its own tooth in this file's "what a refusal leaves behind" suite —
    // the control there is that a REFUSED write evicts nothing). Serving the old val here would be
    // precisely the resurrection that eviction exists to prevent.
    const nextRead = await r.cache.getOrSetEntry({
      ...SHARED,
      factory: notFound,
      tags: [r.tag],
    });
    expect({ tier: nextRead.tier, value: nextRead.value }).toStrictEqual({
      tier: "l2",
      // oxlint-disable-next-line sonarjs/no-undefined-assignment -- as above: the negative is PRESENT and undefined, and that is the claim.
      value: undefined,
    });
  });
});
