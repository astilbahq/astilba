// The durable-note's tooth (C5-c, E29) — the one that fails if `false` and ABSENT are collapsed.
//
// E29's whole point is that a serve's `durable` field has THREE answers, and the two that a
// truthiness read merges — `false` (known not durable) and ABSENT (unknowable) — are the ones a
// consumer most needs kept apart. The old console rendered them as one; this tooth pins them as
// three, and its keystone is the case that goes red the instant they are merged again.
//
// Absence is exercised as a MISSING PROPERTY (`{}`), not an explicit `undefined` — the exact shape
// `CacheEntry.durable?` delivers when the serve could not decide.

import { describe, expect, it } from "vitest";

import { durableNote, durableVerdict } from "./durable-note";

describe("durableVerdict classifies the three serve-turn states (E29)", () => {
  const cases = [
    { entry: { durable: true }, verdict: "durable" },
    { entry: { durable: false }, verdict: "l1-only" },
    { entry: {}, verdict: "unknowable" },
  ] as const;

  it.each(cases)("classifies $entry as $verdict", ({ entry, verdict }) => {
    expect(durableVerdict(entry)).toBe(verdict);
  });

  it("never folds ABSENT into `false` — the collapse E29 exists to forbid", () => {
    // THE KEYSTONE. A truthiness read (`durable ? … : …`) maps both `false` and a missing property
    // to the else-arm; this asserts they land on DIFFERENT verdicts. Rewrite the classifier as a
    // truthiness check and this is the assertion that reddens.
    expect(durableVerdict({ durable: false })).not.toBe(durableVerdict({}));
  });
});

describe("durableNote renders three distinct strings", () => {
  it("gives true / false / absent three different cells", () => {
    const seen = new Set([
      durableNote({ durable: true }),
      durableNote({ durable: false }),
      durableNote({}),
    ]);
    expect(seen.size, "two durable states render identically").toBe(3);
  });

  it("reports ABSENT as unknowable, never as a confident `not durable`", () => {
    // The confident lie the old two-state render produced: absent → "landed in L1 only". Absent must
    // say it cannot tell, and must NOT claim the L1-only story that belongs to `false`.
    const absent = durableNote({});
    expect(absent).toMatch(/unknowable/u);
    expect(absent).not.toMatch(/landed in L1 only/u);
  });

  it("reports `false` as the known L1-only story (E13), not as unknowable", () => {
    const known = durableNote({ durable: false });
    expect(known).toMatch(/landed in L1 only/u);
    expect(known).not.toMatch(/unknowable/u);
  });

  it("reports `true` as durable, proven by the L2 read", () => {
    expect(durableNote({ durable: true })).toMatch(/found in L2/u);
  });
});
