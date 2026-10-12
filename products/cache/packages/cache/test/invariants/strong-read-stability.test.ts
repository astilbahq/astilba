// INV-1/INV-2 — spec §11.1, §11.2, errata E16
// Sacred: append-only. Never modify, delete, weaken, skip, or .todo an existing test.

import { mkKey, mkTag, world } from "@astilba/harness";
import { test } from "@fast-check/vitest";
import fc from "fast-check";
import { describe, expect } from "vitest";

// A MODEL-FREE metamorphic invariant (§11.1/§11.2, E16): two `strong` reads of the SAME
// key with NO mutation between them observe the SAME value. A strong read is a live-checked,
// stable observation of durable truth (§1); back-to-back strong reads cannot disagree
// unless something was purged or refilled between them — and here nothing is. The second
// read IS the oracle: no legal-set math, just `read1 === read2`. Distinct sentinel factory
// values make any divergence directly observable — the interesting failure is read1 serving
// a value born behind a gapped hard purge that read2's live check then rejects and refills.
//
// The environment before the two reads is arbitrary (fills / purges / soft expires / a bus
// gap / time), because the invariant must hold from ANY reachable state. The gap+delete arm
// is the one that manufactures the born-dead precondition E16 governs.

const ISO = fc.nat(1); // a 2-isolate world: purger/reader may be the same or different
const VAL = fc.string({ maxLength: 6 });

type SetupOp =
  | { readonly kind: "fill"; readonly iso: number; readonly value: string }
  | { readonly kind: "delete"; readonly iso: number }
  | { readonly kind: "expire"; readonly iso: number }
  | { readonly kind: "drop" }
  | { readonly kind: "advance"; readonly ms: number };

const setupOp: fc.Arbitrary<SetupOp> = fc.oneof(
  fc.record({ iso: ISO, kind: fc.constant("fill" as const), value: VAL }),
  fc.record({ iso: ISO, kind: fc.constant("delete" as const) }),
  fc.record({ iso: ISO, kind: fc.constant("expire" as const) }),
  fc.record({ kind: fc.constant("drop" as const) }),
  fc.record({
    kind: fc.constant("advance" as const),
    ms: fc.integer({ max: 5000, min: 1 }),
  })
);

/** Apply a generated environment prefix, then return the reader isolate's cache. */
async function applySetup(
  w: ReturnType<typeof world>,
  ops: readonly SetupOp[],
  key: string,
  tag: ReturnType<typeof mkTag>
): Promise<void> {
  for (const op of ops) {
    if (op.kind === "fill") {
      try {
        await w.isolate(op.iso).cache.getOrSet({
          factory: async () => op.value,
          key,
          tags: [tag],
        });
      } catch (error) {
        // A legal §5.0 write-back fence on a setup fill (a purge accepted mid-factory across
        // the retry budget, §5.0/MI6) is specced behavior — discard this generated case rather
        // than fail. The strong-read assertions below are never softened (E18/E16 stand).
        if (error instanceof Error && error.name === "FencedError") {
          fc.pre(false);
        } else {
          throw error;
        }
      }
    } else if (op.kind === "delete") {
      await w.isolate(op.iso).cache.delete({ tag });
    } else if (op.kind === "expire") {
      await w.isolate(op.iso).cache.expire({ tag });
    } else if (op.kind === "drop") {
      w.chaos.dropNextFrames(1);
    } else {
      await w.advance(op.ms);
    }
  }
}

describe("strong read stability (metamorphic)", () => {
  test("INV-1 INV-2: two back-to-back strong reads of an unpurged key agree (benign arm)", async () => {
    // The purge-free instance of the invariant: read1 fills live, read2 live-checks it live
    // and serves the same value (its distinct sentinel factory never runs). Holds today.
    const w = world({ isolates: 2 });
    const key = mkKey("k");
    const tag = mkTag("t0");
    const reader = w.isolate(1).cache;
    const read1 = await reader.getOrSet({
      consistency: "strong",
      factory: async () => "a",
      key,
      tags: [tag],
    });
    const read2 = await reader.getOrSet({
      consistency: "strong",
      factory: async () => "b",
      key,
      tags: [tag],
    });
    expect(read1).toBe(read2);
  });

  /* References: §5.0. */

  test.prop([fc.array(setupOp, { maxLength: 12 }), VAL, VAL])(
    "INV-1 INV-2 property: two strong reads of a key with no mutation between are equal (E16)",
    async (ops, v1, v2) => {
      const w = world({ isolates: 2 });
      const key = mkKey("k");
      const tag = mkTag("t0");
      await applySetup(w, ops, key, tag);
      // Two strong reads of the same key, back-to-back, with NO mutation between them.
      const reader = w.isolate(1).cache;
      const read1 = await reader.getOrSet({
        consistency: "strong",
        factory: async () => v1,
        key,
        tags: [tag],
      });
      const read2 = await reader.getOrSet({
        consistency: "strong",
        factory: async () => v2,
        key,
        tags: [tag],
      });
      expect(read1).toBe(read2);
    }
  );
});
