// Ordinary unit teeth (NOT sacred). `cache.explain(key)` is a WITNESS, not a
// historian: it attests what the system already knows at the instant of the call and reports what it
// cannot know as `unknown`, adding no tracked state. These teeth pin the store-facing half of the
// contract; the reader-verdict half lives in explain-reader.test.ts.
//
// Each case bites ONE branch — the named mutation reds it:
//   1. introspection-not-testimony — the struck fields (`appliedBatch`, a persisted `dependencies`
//                          string[] graph) are GONE from `Explanation` (compile-level; re-adding
// either reds tsc / LEG1).
//   2. absence-is-reportable — explain of a missing key returns a well-typed absent result and never
//                          throws. (Blind: make the absent path throw ⇒ reds.)
//   3. tier-is-observation-not-hydration — an L2-only entry reports tier `l2`, and explain NEVER
//                          hydrates L1 (zero L1 writes across the call). (Blind: swap the non-mutating
//                          probe for the hydrating read ⇒ an L1 write lands ⇒ reds.)
//   4. identity-as-stored — the stored tag set, the readable `pub` scope, kind, and bornEpoch are
//                          read off the store. (Blind: drop the identity read ⇒ reds.)
// 5. scope-unreadable — a stored entry whose scope the surface cannot read reports an
//                          `unreadable` marker, distinct from a readable literal. (Blind: collapse
//                          the readable/unreadable distinction ⇒ reds.)
//   6. ttl-as-stored — the ttl is read off the raw metadata: `not-stored` where the format carries
//                          none (v0.2 always, even for a DECLARED ttl), `stored` with the seconds a
//                          format that carries one supplies. (Blind, both directions: launder the
//                          reconstruction default back in ⇒ the not-stored case reds; pin the answer
//                          to a constant `not-stored` ⇒ the stored case reds.)
//   7. request-scoped attribution — with a bound render collector, explain surfaces the request's
//                          per-dependency records; WITHOUT one the field is absent. (Blind: return no
//                          attribution ⇒ the bound case reds.)
//
// Lane: unit. Deterministic by construction — an injected clock + fake stores; no registry needed for
// the store-facing contract. No timing tolerance, no retry.

import assert from "node:assert/strict";

import { createCache, memory, t } from "@astilba/cache";
import type {
  Cache,
  Explanation,
  ReadKind,
  Store,
  StoreMetadata,
  StoreValue,
  StoreWriteOptions,
} from "@astilba/cache";
import { describe, expect, it } from "vitest";

// The internal binding seam the RR middleware itself uses (`bindRenderCollector`) — NOT public API
// (not re-exported from the barrel), so this tooth reaches it the way its one production consumer
// does: by module path, from inside the package.
import { bindRenderCollector } from "../../src/cache";

const NS = "explain";
const CLOCK = { now: (): number => 0 };
const RNG = { next: (): number => 0 };

/** A registry-absent cache over one durable tier — enough to fill, store, and witness. */
function makeCache(l2: Store = memory()): Cache {
  return createCache({ clock: CLOCK, l2, namespace: NS, rng: RNG });
}

/** A `Store` wrapper counting `set` calls — the format-independent proof that explain never
 * hydrates L1 (a hydrating read would `set`, a presence probe never does). Reads pass through. */
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
 * A `Store` whose reads return metadata with the `scope` field REMOVED (mirrors rr-l3-scope's
 * fixture): a stored entry whose scope is unreadable on the surface. Writes pass through untouched.
 */
function scopeStrippingStore(inner: Store): Store {
  return {
    delete: async (key: string): Promise<void> => {
      await inner.delete(key);
    },
    get: async (
      key: string,
      readKind?: ReadKind
    ): Promise<StoreValue | undefined> => {
      const stored = await inner.get(key, readKind);
      if (stored?.metadata === undefined) {
        return stored;
      }
      const stripped: StoreMetadata = Object.fromEntries(
        Object.entries(stored.metadata).filter(([field]) => field !== "scope")
      );
      return { metadata: stripped, value: stored.value };
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

/** A store whose reads corrupt the entry so the probe's fail-closed branches fire: `"decode"`
 * replaces the body with bytes the codec cannot decode (the decode try/catch), `"codec"` swaps the
 * stored codecId for a foreign one (the codecId-mismatch guard). Both are absence causes
 * requires `explain` to report without throwing — the same fail-closed shape the serve path holds. */
function corruptingStore(inner: Store, mode: "codec" | "decode"): Store {
  return {
    delete: async (key: string): Promise<void> => {
      await inner.delete(key);
    },
    get: async (
      key: string,
      readKind?: ReadKind
    ): Promise<StoreValue | undefined> => {
      const stored = await inner.get(key, readKind);
      if (stored?.metadata === undefined) {
        return stored;
      }
      if (mode === "decode") {
        // A matching codecId, but a body the codec cannot decode.
        return { metadata: stored.metadata, value: "{ not decodable" };
      }
      // A foreign codecId the cache's codec does not accept.
      return {
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

/** A `Store` whose reads STAMP a `ttl` field into metadata — a stored format that carries one,
 * which v0.2's write path never produces. The forward direction of the ttl witness: what the store's
 * bytes carry is what the witness reports, with no change to the witness itself. */
function ttlStampingStore(inner: Store, seconds: number): Store {
  return {
    delete: async (key: string): Promise<void> => {
      await inner.delete(key);
    },
    get: async (
      key: string,
      readKind?: ReadKind
    ): Promise<StoreValue | undefined> => {
      const stored = await inner.get(key, readKind);
      if (stored?.metadata === undefined) {
        return stored;
      }
      return {
        metadata: { ...stored.metadata, ttl: seconds },
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

// ── 1. introspection-not-testimony (compile-level) ───────────────────────────────────────────────

/** The `present` arm — the shape that carried the struck fields in the old `Explanation`. */
type PresentExplanation = Extract<Explanation, { kind: "present" }>;
/** `true` iff `K` is a key of `T` — the compile-level witness for a struck field's ABSENCE. */
type HasKey<T, K extends string> = K extends keyof T ? true : false;

describe("explain — introspection-not-testimony: the struck fields are gone (E27)", () => {
  it("Explanation carries NO appliedBatch and NO persisted dependencies string[] graph", () => {
    // The real assertion is the TYPE annotation: if `appliedBatch` (or a persisted `dependencies`
    // graph) reappeared on the present arm, `HasKey<…>` becomes `true` and `= false` fails to
    // compile — LEG1 reds. The runtime `expect` pins the witnesses non-vacuously.
    const noAppliedBatch: HasKey<PresentExplanation, "appliedBatch"> = false;
    const noDependencyGraph: HasKey<PresentExplanation, "dependencies"> = false;
    // oxlint-disable-next-line sonarjs/no-trivial-assertions -- the type annotation is the check
    expect(noAppliedBatch).toBeFalsy();
    // oxlint-disable-next-line sonarjs/no-trivial-assertions -- the type annotation is the check
    expect(noDependencyGraph).toBeFalsy();
  });
});

// ── 2. absence-is-reportable ─────────────────────────────────────────────────────────────────────

describe("explain — absence is a reportable state, not an error (E27 (1))", () => {
  it("explain of a missing key returns a well-typed absent result and never throws", async () => {
    const cache = makeCache();
    const explanation = await cache.explain("no-such-key");
    // The discriminant narrows the absent arm — it carries no identity/verdict (compile + runtime),
    // and `tier` is reachable only THROUGH the narrowing (the read-failed arm carries none).
    assert.ok(explanation.kind === "absent");
    expect(explanation.tier).toBe("miss");
  });

  // The probe fails closed on EVERY absence cause, not just a plain miss (never throws).
  // Mutation: drop the decode try/catch in `probeStoredEntry` ⇒ explain throws the decode error out
  // of the async method on an undecodable entry ⇒ reds.
  it("an UNDECODABLE stored entry reports absent, not a throw", async () => {
    const l2 = memory();
    const writer = createCache({ clock: CLOCK, l2, namespace: NS, rng: RNG });
    await writer.getOrSet({
      factory: async (): Promise<string> => "v",
      key: "k",
      tags: [t`x`],
    });
    const reader = createCache({
      clock: CLOCK,
      l2: corruptingStore(l2, "decode"),
      namespace: NS,
      rng: RNG,
    });
    const explanation = await reader.explain("k");
    assert.ok(explanation.kind === "absent");
    expect(explanation.tier).toBe("miss");
  });

  // Mutation: drop the codecId-mismatch guard in `probeStoredEntry` ⇒ an entry stamped with a
  // foreign codecId is decoded anyway and reported PRESENT (INV-27: a foreign codec must never
  // run) ⇒ the `kind === "absent"` assertion reds.
  it("a CODEC-MISMATCHED stored entry reports absent, not a throw", async () => {
    const l2 = memory();
    const writer = createCache({ clock: CLOCK, l2, namespace: NS, rng: RNG });
    await writer.getOrSet({
      factory: async (): Promise<string> => "v",
      key: "k",
      tags: [t`x`],
    });
    const reader = createCache({
      clock: CLOCK,
      l2: corruptingStore(l2, "codec"),
      namespace: NS,
      rng: RNG,
    });
    const explanation = await reader.explain("k");
    assert.ok(explanation.kind === "absent");
    expect(explanation.tier).toBe("miss");
  });
});

// ── 3. tier-is-observation-not-hydration ─────────────────────────────────────────────────────────

describe("explain — tier is an observation, never a hydrating read (E27 (3))", () => {
  it("an L2-only entry reports tier l2 and explain performs ZERO L1 writes", async () => {
    const l2 = memory();
    // Writer fills the shared L2 (and its own L1).
    const writer = createCache({
      clock: CLOCK,
      l1: memory(),
      l2,
      namespace: NS,
      rng: RNG,
    });
    await writer.getOrSet({
      factory: async (): Promise<string> => "v",
      key: "k",
      tags: [t`x`],
    });

    // Reader shares the L2 but has its own EMPTY L1 (the multi-isolate reality: durable in L2, not
    // yet in this isolate's L1).
    const l1 = countingStore(memory());
    const reader = createCache({
      clock: CLOCK,
      l1: l1.store,
      l2,
      namespace: NS,
      rng: RNG,
    });

    const setsBefore = l1.sets;
    const explanation = await reader.explain("k");

    assert.ok(explanation.kind === "present");
    expect(explanation.tier).toBe("l2");
    // The tooth: explain probed L1 (a read) but never HYDRATED it (no write). Swapping the probe for
    // the hydrating read would call hydrateL1 ⇒ l1.set ⇒ this count grows ⇒ red.
    expect(l1.sets).toBe(setsBefore);
  });
});

// ── 4/5. identity as stored (readable + unreadable) ──────────────────────────────────────────────

describe("explain — identity is read from the store (E27 (1), E23/E24)", () => {
  it("reports the stored tag set (user + reserved) and the entry kind", async () => {
    const cache = makeCache();
    const tagX = t`x`;
    const tagY = t`y`;
    await cache.getOrSet({
      factory: async (): Promise<string> => "v",
      key: "k",
      tags: [tagX, tagY],
    });

    const explanation = await cache.explain("k");
    assert.ok(
      explanation.kind === "present",
      "expected the stored entry to be present"
    );
    const { identity } = explanation;
    // The stored tag set — the user tags PLUS the reserved per-key/per-namespace tags.
    expect(identity.tags).toContain(tagX);
    expect(identity.tags).toContain(tagY);
    expect(identity.tags.some((tag) => tag.startsWith("__k:"))).toBeTruthy();
    expect(identity.tags.some((tag) => tag.startsWith("__ns:"))).toBeTruthy();
    expect(identity.kind).toBe("val");
  });

  it("reports the readable pub scope, the born epoch, and the codec-only fresh verdict", async () => {
    const cache = makeCache();
    await cache.getOrSet({
      factory: async (): Promise<string> => "v",
      key: "k",
      tags: [t`x`],
    });

    const explanation = await cache.explain("k");
    assert.ok(
      explanation.kind === "present",
      "expected the stored entry to be present"
    );
    expect(explanation.identity.scope).toStrictEqual({
      kind: "readable",
      literal: "pub",
    });
    expect(explanation.identity.bornEpoch).toBe(0); // registry-absent ⇒ bornEpoch floor 0
    // Registry-absent ⇒ no watermark authority ⇒ the codec-only fresh verdict.
    expect(explanation.verdict).toStrictEqual({
      hardEpoch: 0,
      softEpoch: 0,
      validity: "fresh",
    });
  });

  it("reports an UNREADABLE scope marker when the surface returns no readable scope (E23)", async () => {
    // The entry is written normally, then read back through a surface that drops the `scope` field —
    // the readable-vs-unreadable distinction the gate turns on.
    const cache = makeCache(scopeStrippingStore(memory()));
    await cache.getOrSet({
      factory: async (): Promise<string> => "v",
      key: "k",
      tags: [t`x`],
    });

    const explanation = await cache.explain("k");
    assert.ok(
      explanation.kind === "present",
      "expected the stored entry to be present"
    );
    // The tooth: a readable literal and an unreadable marker are DIFFERENT states — collapsing them
    // (always reporting readable) reds here.
    expect(explanation.identity.scope).toStrictEqual({ kind: "unreadable" });
  });

  // Mutation: launder the reconstruction default back into identity — report the envelope's
  // rebuilt `ttl` (a hardcoded 0) or the DECLARED `5m` as if the store carried it ⇒ the
  // `not-stored` assertion reds. A ttl the format never wrote is not stored identity, and a
  // defaulted 0 reported as identity is the witness testifying to bytes that do not exist.
  it("reports ttl NOT-STORED — the v0.2 format carries no ttl, and a declared one is not stored identity", async () => {
    const cache = makeCache();
    await cache.getOrSet({
      factory: async (): Promise<string> => "v",
      key: "k",
      tags: [t`x`],
      ttl: "5m", // declared, joins the singleflight identity — and is never written to the store
    });

    const explanation = await cache.explain("k");
    assert.ok(
      explanation.kind === "present",
      "expected the stored entry to be present"
    );
    expect(explanation.identity.ttl).toStrictEqual({ kind: "not-stored" });
  });

  // Mutation: pin the witness to a constant `not-stored` (ignore the raw metadata) ⇒ reds. The
  // mirror of the tooth above: `not-stored` must be a READ RESULT, not a hardcoded answer — the
  // pair is what makes the two arms structurally distinct in both directions rather than one.
  it("reports ttl STORED with its seconds when the surface carries one", async () => {
    const cache = makeCache(ttlStampingStore(memory(), 300));
    await cache.getOrSet({
      factory: async (): Promise<string> => "v",
      key: "k",
      tags: [t`x`],
    });

    const explanation = await cache.explain("k");
    assert.ok(
      explanation.kind === "present",
      "expected the stored entry to be present"
    );
    expect(explanation.identity.ttl).toStrictEqual({
      kind: "stored",
      seconds: 300,
    });
  });
});

// ── 7. request-scoped attribution ────────────────────────────────────────────────────────────────

describe("explain — request-scoped per-dependency attribution (E27 (5), E23)", () => {
  it("is absent when no render collector is bound to the request", async () => {
    const cache = makeCache();
    const unbound = await cache.explain("k");
    // It lives and dies with a request, and there is no request here.
    expect(unbound.requestDependencies).toBeUndefined();
  });

  it("surfaces the bound collector's per-dependency records", async () => {
    const cache = makeCache();
    const collector = cache.collect();
    bindRenderCollector(cache, () => collector);
    const tagA = t`a`;
    const tagB = t`b`;
    collector.dependsOn(tagA);
    collector.dependsOn(tagB, { l3: false });

    const bound = await cache.explain("k");
    // The tooth: exactly the recorded dependencies, scope evidence and `l3` intact. Returning no
    // attribution reds this.
    expect(bound.requestDependencies).toStrictEqual([
      { scope: { kind: "none" }, tags: [tagA] },
      { l3: false, scope: { kind: "none" }, tags: [tagB] },
    ]);
  });
});
