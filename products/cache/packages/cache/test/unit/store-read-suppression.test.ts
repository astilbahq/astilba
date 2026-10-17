// Ordinary unit teeth (NOT sacred) — erratum store-read suppression and the explain witness's read-failed
// arm). A a suppressed store GET error is a PLAIN MISS: never unknown, never
// fail-closed. On the serving path a miss means refill, so a failing tier costs an origin read
// rather than the request. `explain()` cannot refill, so that remedy does not transfer — the witness
// gets its own state instead, distinct from absence.
//
// The sacred suite cannot reach these branches: its only injected store failure is UNCLASSIFIED
// (`InjectedStoreError` carries no `code`), and an unclassified rejection is exactly what the kernel
// must NOT absorb. So the classified shape is fixtured here, in the lane that owns driver contracts.
//
// Each case bites ONE branch — the named mutation reds it:
//   1. suppressed-get-is-a-miss — a classified rejecting L2 read refills from the factory and serves.
//                          (Blind: unwrap `readTier`'s try/catch ⇒ the raw error escapes ⇒ reds.)
//   2. unclassified-propagates — a rejection no driver classified reaches the caller unchanged.
//                          (Blind: suppress everything in `classifyStoreReadFailure` ⇒ reds.)
// 3. l1-may-serve — an L1 copy serves while L2's read keeps failing; the factory does not
//                          re-run. (Blind: unwrap the guard ⇒ the L2 read throws before L1's hit can
//                          be returned ⇒ reds.)
//   4. durable-false disclosure — a fill whose L2 write-back cannot read-compare lands in L1 only and
//                          says so. (Blind: unwrap `bornMonotoneWrite`'s read guard ⇒ reds.)
//   5. suppression is observable — `store_read_suppressed` carries the classified code.
//                          (Blind: drop the emit ⇒ reds.)
// 6. read-failed ≠ absent — four teeth, because the distinction has four ways to collapse:
//                          (a) laundering read-failed into absent reds the suppressed-read case;
//                          (b) pinning the answer to a constant read-failed reds the absent case —
//                          one direction alone would pin the answer, not the reading;
//                          (c) ACROSS tiers: a later tier's absence must not erase an earlier tier's
//                          read failure. (Blind: `readFailed ||= …` → `readFailed = …` in explain's
//                          tier loop ⇒ L1-suppressed + L2-absent launders to absent ⇒ reds.)
//                          (d) the arm carries NO tier — the structural property that makes it a
//                          distinct state rather than a re-labelled miss. (Blind: re-add a
//                          `tier:'miss'` sentinel to the read-failed return ⇒ reds.)
//   7. a found entry is found — an unreadable L1 does not stop `explain` reporting an entry L2 holds.
//                          (Blind: return read-failed on the first failing tier ⇒ reds.)
//
// Lane: unit. Deterministic by construction — injected clock, fake stores, no registry. No timing
// tolerance, no retry.

import assert from "node:assert/strict";

import { createCache, memory } from "@astilba/cache";
import type {
  Cache,
  ReadKind,
  Store,
  StoreValue,
  StoreWriteOptions,
  TelemetryEvent,
} from "@astilba/cache";
import { describe, expect, it } from "vitest";

const NS = "read-suppression";
const CLOCK = { now: (): number => 0 };
const RNG = { next: (): number => 0 };

/**
 * A rejection carrying the OWN classified shape — the structural contract the kernel discriminates
 * on (`code` + `retryable`), raised as a CLASS so oxlint's throw rules are satisfied. Nothing here
 * shares an identity with the kernel's guard: recognition is structural, which is the point.
 */
class ClassifiedReadFailureError extends Error {
  readonly code: "throttled" | "unavailable";
  readonly retryable = true;

  constructor(code: "throttled" | "unavailable") {
    super(`store read failed: ${code}`);
    this.name = "ClassifiedReadFailureError";
    this.code = code;
  }
}

/** A rejection NO driver classified — carries no `code`, so the kernel must not absorb it. */
class UnclassifiedReadFailureError extends Error {
  constructor() {
    super("store read failed: cause unknown");
    this.name = "UnclassifiedReadFailureError";
  }
}

/** A `Store` whose reads always reject with `error`; writes and deletes pass through to `inner`, so
 * a fill still lands wherever it can (the L1-only case names). */
function readRejectingStore(inner: Store, error: () => Error): Store {
  return {
    delete: async (key: string): Promise<void> => {
      await inner.delete(key);
    },
    get: async (): Promise<StoreValue | undefined> => {
      await Promise.resolve();
      throw error();
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

/** A `Store` that rejects reads only until `heal()` is called — lets one trace hold a stored entry
 * that later becomes unreadable, which is the shape `explain` must not report as absence. */
interface HealableStore {
  readonly store: Store;
  fail: boolean;
}
function healableStore(inner: Store): HealableStore {
  const wrapper: HealableStore = {
    fail: false,
    store: {
      delete: async (key: string): Promise<void> => {
        await inner.delete(key);
      },
      get: async (
        key: string,
        readKind?: ReadKind
      ): Promise<StoreValue | undefined> => {
        if (wrapper.fail) {
          throw new ClassifiedReadFailureError("unavailable");
        }
        return await inner.get(key, readKind);
      },
      set: async (
        key: string,
        value: string,
        options?: StoreWriteOptions
      ): Promise<void> => {
        await inner.set(key, value, options);
      },
    },
  };
  return wrapper;
}

interface Harness {
  readonly cache: Cache;
  readonly events: TelemetryEvent[];
}
function makeCache(l2: Store, l1?: Store): Harness {
  const events: TelemetryEvent[] = [];
  const cache = createCache({
    clock: CLOCK,
    ...(l1 === undefined ? {} : { l1 }),
    l2,
    namespace: NS,
    rng: RNG,
    telemetry: (event) => {
      events.push(event);
    },
  });
  return { cache, events };
}

// ── 1. a suppressed GET is a plain miss ──────────────────────────────────────────────────────────

describe("E13 — a suppressed store GET error is a plain miss", () => {
  it("a classified rejecting L2 read refills from the factory and serves the value", async () => {
    const { cache } = makeCache(
      readRejectingStore(
        memory(),
        () => new ClassifiedReadFailureError("unavailable")
      )
    );
    let calls = 0;
    const value = await cache.getOrSet({
      factory: async (): Promise<string> => {
        calls += 1;
        return "filled";
      },
      key: "k",
    });
    // The whole of the GET half in two assertions: the read failure did not reach the caller, and
    // the miss it became was refilled from origin.
    expect(value).toBe("filled");
    expect(calls).toBe(1);
  });

  it("a throttled read is suppressed on the same terms as an unavailable one", async () => {
    const { cache } = makeCache(
      readRejectingStore(
        memory(),
        () => new ClassifiedReadFailureError("throttled")
      )
    );
    const value = await cache.getOrSet({
      factory: async (): Promise<string> => "filled",
      key: "k",
    });
    expect(value).toBe("filled");
  });
});

// ── 2. an unclassified rejection is never absorbed ───────────────────────────────────────────────

describe("E13 — the kernel absorbs only what a driver classified", () => {
  it("an unclassified read rejection propagates to the caller unchanged", async () => {
    // The unclassified store is L1, over a WORKING L2 — deliberately, so this bites the READ
    // guard alone. With it at L2 the case passes even when the read guard swallows everything,
    // because the write-back's own read-compare rethrows the same rejection a moment later: the
    // caller sees the error either way and the tooth proves nothing. (Observed, not predicted.)
    const { cache } = makeCache(
      memory(),
      readRejectingStore(memory(), () => new UnclassifiedReadFailureError())
    );
    // The write half's reasoning, mirrored: a rejection nobody classified is not evidence of
    // absence, and swallowing it would turn an unknown fault into a confident "not found".
    await expect(
      cache.getOrSet({
        factory: async (): Promise<string> => "filled",
        key: "k",
      })
    ).rejects.toBeInstanceOf(UnclassifiedReadFailureError);
  });

  it("an unclassified read rejection propagates out of explain() too", async () => {
    const { cache } = makeCache(
      readRejectingStore(memory(), () => new UnclassifiedReadFailureError())
    );
    await expect(cache.explain("k")).rejects.toBeInstanceOf(
      UnclassifiedReadFailureError
    );
  });
});

// ── 3. L1 may serve (the own parenthetical) ────────────────────────────────────────────────────

describe("E13 — L1 may serve while L2's read keeps failing", () => {
  it("an L1 copy serves the second read and the factory does not run again", async () => {
    const { cache } = makeCache(
      readRejectingStore(
        memory(),
        () => new ClassifiedReadFailureError("unavailable")
      ),
      memory()
    );
    let calls = 0;
    const factory = async (): Promise<string> => {
      calls += 1;
      return `fill-${calls}`;
    };
    // Fill #1: L1 misses, L2's read is suppressed to a miss, the factory runs, L1 is hydrated.
    await expect(cache.getOrSet({ factory, key: "k" })).resolves.toBe("fill-1");
    // Fill #2: L1 is checked FIRST and hits, so the failing L2 is never read at all — the
    // rescue is structural, not a special case. A second factory run would mean L1 never served.
    await expect(cache.getOrSet({ factory, key: "k" })).resolves.toBe("fill-1");
    expect(calls).toBe(1);
  });
});

// ── 4. durable:false — the fill that landed in L1 only ───────────────────────────────────────────

describe("E13 — a write-back that cannot read-compare is non-durable, not a failure", () => {
  it("getOrSetEntry serves the value and discloses durable:false", async () => {
    const { cache } = makeCache(
      readRejectingStore(
        memory(),
        () => new ClassifiedReadFailureError("unavailable")
      ),
      memory()
    );
    const entry = await cache.getOrSetEntry({
      factory: async (): Promise<string> => "filled",
      key: "k",
    });
    // the own disclosure: the caller can see a value that landed only in L1. The write-back's
    // read-compare could not be performed, so the candidate never landed durably — and
    // nothing was written blind to satisfy it.
    expect(entry.value).toBe("filled");
    expect(entry.durable).toBeFalsy();
  });
});

// ── 5. suppression is observable ─────────────────────────────────────────────────────────────────

describe("E13 — a suppressed read states its raw fact", () => {
  it("emits store_read_suppressed carrying the classified code", async () => {
    const { cache, events } = makeCache(
      readRejectingStore(
        memory(),
        () => new ClassifiedReadFailureError("throttled")
      )
    );
    await cache.getOrSet({
      factory: async (): Promise<string> => "filled",
      key: "k",
    });
    const suppressed = events.filter(
      (event) => event.type === "store_read_suppressed"
    );
    expect(suppressed.length).toBeGreaterThan(0);
    expect(suppressed[0]?.code).toBe("throttled");
  });
});

// ── 6. explain: read-failed is DISTINCT from absent (both directions) ────────────────────────────

describe("E27 — the witness reports a failed read as its own state", () => {
  it("a suppressed read reports read-failed, NOT absent", async () => {
    const { cache } = makeCache(
      readRejectingStore(
        memory(),
        () => new ClassifiedReadFailureError("unavailable")
      )
    );
    const explanation = await cache.explain("k");
    // Direction (i): laundering read-failed into absent reds here. Reporting `absent` would be the
    // witness stating a fact it does not have — the store did not answer, so nothing establishes
    // that the key is missing.
    expect(explanation.kind).toBe("read-failed");
    // Structurally distinct, no sentinel: the read-failed arm carries NO tier, because a probe that
    // could not read observed none. ASSERTED, not merely narrowed — the type keeps `tier` off this
    // arm, but a sentinel added back at the return site is still an object with a `tier` at runtime,
    // and a comment is not a gate. This is the property that makes read-failed a third STATE rather
    // than absent wearing a different label.
    assert.ok(explanation.kind === "read-failed");
    expect("tier" in explanation).toBeFalsy();
    // The reader's own state is still knowable and still reported.
    expect(explanation.reader.appliedEpoch).toBe(0);
  });

  it("a LATER tier's absence does not erase an EARLIER tier's read failure", async () => {
    // L1 cannot be read; L2 is genuinely empty. Tiers are probed L1-then-L2, so the failure is
    // observed FIRST and the absence LAST — the order in which an accumulator that merely tracks the
    // last tier reports `absent` and calls it an answer. Nothing about the key is established: the
    // tier that might hold it never spoke. Reporting absence here is exactly the laundering
    // forbids, arriving by arithmetic rather than by intent.
    const { cache } = makeCache(
      memory(),
      readRejectingStore(
        memory(),
        () => new ClassifiedReadFailureError("unavailable")
      )
    );
    const explanation = await cache.explain("never-stored");
    expect(explanation.kind).toBe("read-failed");
  });

  it("an absent key still reports absent — the answer is READ, not pinned", async () => {
    const { cache } = makeCache(memory());
    const explanation = await cache.explain("no-such-key");
    // Direction (ii): pinning the answer to a constant `read-failed` reds here. A pair in one
    // direction only would pin the answer rather than the reading.
    assert.ok(explanation.kind === "absent");
    expect(explanation.tier).toBe("miss");
  });

  it("a stored key still reports present — the third arm did not swallow the other two", async () => {
    const healable = healableStore(memory());
    const { cache } = makeCache(healable.store);
    await cache.getOrSet({
      factory: async (): Promise<string> => "v",
      key: "k",
    });
    const before = await cache.explain("k");
    assert.ok(before.kind === "present");
    // The SAME key, the SAME store — only the read now fails. Absent and read-failed are not
    // interchangeable readings of one situation; they are different situations.
    healable.fail = true;
    const after = await cache.explain("k");
    expect(after.kind).toBe("read-failed");
  });
});

// ── 7. a found entry is found ────────────────────────────────────────────────────────────────────

describe("E27 — a failing tier does not hide an entry another tier holds", () => {
  it("an unreadable L1 does not stop explain reporting the entry stored in L2", async () => {
    const l2 = memory();
    const { cache } = makeCache(
      l2,
      readRejectingStore(
        memory(),
        () => new ClassifiedReadFailureError("unavailable")
      )
    );
    await cache.getOrSet({
      factory: async (): Promise<string> => "v",
      key: "k",
    });
    const explanation = await cache.explain("k");
    // Precedence: a found entry is FOUND however the other tiers behaved. Only a probe that found
    // nothing anywhere AND could not read somewhere is read-failed.
    assert.ok(explanation.kind === "present");
    expect(explanation.tier).toBe("l2");
  });
});
