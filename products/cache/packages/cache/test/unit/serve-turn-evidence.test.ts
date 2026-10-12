// Ordinary unit teeth (NOT sacred) — erratum E29 (serve-turn evidence) over E13's write-back
// suppression. The sacred suite cannot reach the suppression branch: its only injected store
// failure is UNCLASSIFIED, which the kernel must propagate rather than absorb, so the classified
// shape is fixtured here — the same reason `writeback-classification.test.ts` and
// `store-read-suppression.test.ts` live in this lane.
//
// The case this file exists for: an entry can be in L1 and demonstrably NOT in L2, in the SAME
// isolate that just watched the write-back get suppressed. A serve that reports a constant
// `durable: true` on an L1 hit is not merely imprecise there — it is stating the opposite of a
// fact the isolate has already observed. E29's answer is not to make the serve read L2 (that would
// make an L1 hit cost what §8 says it must not) but to report exactly as much as it knows: nothing.
//
// Exact values throughout (`toBe(true)`/`toBe(false)`), never truthiness: `false` and ABSENT are
// different answers here, and only an exact assertion tells them apart. The preset's two
// `prefer-to-be-*` rules autofix that form away, so they are scoped off for this file in
// vite.config.ts.
//
// Each case bites ONE branch (rule 13) — the named mutation reds it:
//   1. suppressed-reread — fill under a suppressed L2 write-back, re-read in the same isolate.
//      (Blind: pin the L1-hit serve back to `durable: true` ⇒ reds, on a reachable false path.)
//   2. l2-read-observes — an entry found in L2 reports `true`.
//      (Blind: drop `serveDurability`'s L2 arm ⇒ absent ⇒ reds.)
//   3. the tiers disagree — the SAME stored value reports `true` from L2 and absent from L1.
//      (Blind: return a constant either way ⇒ one of the two arms reds.)

import { createCache, memory } from "@astilba/cache";
import type {
  Cache,
  Store,
  StoreValue,
  StoreWriteError,
  StoreWriteOptions,
} from "@astilba/cache";
import { describe, expect, it } from "vitest";

const NS = "serve-evidence";
const CLOCK = { now: (): number => 0 };
const RNG = { next: (): number => 0 };

/** A classified transient write failure — E13's structural contract (`code` + `retryable`), raised
 * as a class so oxlint's throw rules are satisfied. It shares no identity with the kernel's guard:
 * recognition is structural, which is the point. */
class ThrottledWriteError extends Error implements StoreWriteError {
  readonly code = "throttled";
  readonly retryable = true;

  constructor() {
    super("store write failed: throttled");
    this.name = "ThrottledWriteError";
  }
}

/** A `Store` whose writes are always suppressed (classified-transient) and which therefore holds
 * nothing — the L2 a value provably never reached. Reads pass through to `inner`, so the emptiness
 * is real rather than hidden behind a failing read. */
function writeSuppressingStore(inner: Store): Store {
  return {
    delete: async (key: string): Promise<void> => {
      await inner.delete(key);
    },
    get: async (key: string): Promise<StoreValue | undefined> =>
      await inner.get(key),
    set: async (
      _key: string,
      _value: string,
      _options?: StoreWriteOptions
    ): Promise<void> => {
      await Promise.resolve();
      throw new ThrottledWriteError();
    },
  };
}

function makeCache(l2: Store, l1?: Store): Cache {
  return createCache({
    clock: CLOCK,
    ...(l1 === undefined ? {} : { l1 }),
    l2,
    namespace: NS,
    rng: RNG,
  });
}

const factory = async (): Promise<string> => await Promise.resolve("v");

describe("E29 — a serve reports what its own evidence supports", () => {
  it("the E13-suppressed re-read: an L1 copy whose L2 write-back was suppressed reports durable ABSENT, never true", async () => {
    const cache = makeCache(writeSuppressingStore(memory()), memory());

    // The FILL turn: the factory runs, L1 is hydrated, and the L2 write-back is suppressed
    // (§5.0, E13) — the value serves, the durable copy does not land. The fill did the write and
    // awaited its outcome, so it reports a real boolean.
    const filled = await cache.getOrSetEntry({ factory, key: "k" });
    expect(filled.durable).toBe(false);

    // THE TOOTH: the same isolate re-reads. L1 is checked first (§5.1) and hits, so L2 is never
    // consulted — and this entry is `pub`, so its stored identity says nothing about tier
    // confinement either. The serve therefore has no evidence: not the write-back outcome (that
    // was the previous turn's, and no record of it rides in the envelope), not an L2 read (§5.1
    // does not perform one here, and §8 forbids adding one to decorate a field).
    //
    // Reporting `true` here — as a hardcoded fresh-hit constant does — is not a rounding error:
    // this isolate WATCHED the write-back get suppressed. The value is provably not in L2, and
    // `true` is the one answer the evidence positively excludes.
    const served = await cache.getOrSetEntry({ factory, key: "k" });
    expect(served.tier).toBe("l1");
    // Structural absence, not an assigned `undefined`, and above all not `true`.
    expect(Object.hasOwn(served, "durable")).toBe(false);
    expect(served.durable).toBeUndefined();
  });

  it("an entry found in L2 reports durable === true — this turn observed the shared tier", async () => {
    // No L1: every read goes to L2, so a hit there is a direct observation of the shared tier.
    const cache = makeCache(memory());

    await cache.getOrSetEntry({ factory, key: "k" });
    const served = await cache.getOrSetEntry({ factory, key: "k" });
    expect(served.value).toBe("v");
    expect(served.tier).toBe("l2");
    // Exact: found-in-L2 is the one serve that can positively claim durability.
    expect(served.durable).toBe(true);
  });

  it("one genuinely-durable value reports true from L2 and ABSENT from L1 — the report tracks the evidence, not the truth", async () => {
    // The discriminator for the whole rule, and its sharpest case. ONE entry, really written to a
    // shared L2, really durable. Two isolates read it; the only difference is the tier each one
    // finds it in — and they give different, both-honest answers. A kernel reporting a constant
    // cannot produce both.
    const l2 = memory();

    // The filling isolate hydrates its own L1 from the fill (a read of L2 does NOT — §5.1 serves
    // from the tier it hit and hydrates nothing), so this is how a `pub` entry comes to sit in L1.
    const withL1 = makeCache(l2, memory());
    await withL1.getOrSetEntry({ factory, key: "shared" });
    const fromL1 = await withL1.getOrSetEntry({ factory, key: "shared" });
    expect(fromL1.tier).toBe("l1");
    // The value IS durable — the next assertion proves it from the other isolate. This serve still
    // does not say so, because it did not look: absence reports the evidence, not the truth. That
    // is the honesty the field is for; claiming `true` here would be right by luck.
    expect(Object.hasOwn(fromL1, "durable")).toBe(false);

    // A second isolate with no L1 reads the SAME key: L2 answers, so this turn saw the copy itself.
    const withoutL1 = makeCache(l2);
    const fromL2 = await withoutL1.getOrSetEntry({ factory, key: "shared" });
    expect(fromL2.tier).toBe("l2");
    expect(fromL2.durable).toBe(true);
  });
});
