import type { BusFrame, TagChange, Validity } from "@astilba/cache";
import fc from "fast-check";
import { describe, expect, test } from "vitest";

import {
  buildReaderParts,
  probeEnvelope,
} from "../conformance/fixtures/reader-parts";

// Reader-side snapshot-replay equivalence (§2, F12) — the reader mirror of the writer-side
// theorem in registry-machine.test.ts. Loading `__snap:<snapBatch>` and replaying the tail
// `(snapBatch, lastBatch]` must yield the SAME knowledge as replaying every delta `(0, lastBatch]`
// from zero. Proven by driving the REAL Invalidation two ways and comparing its serve verdicts to
// each other AND to the `applyFrames` reducer oracle (the reducer pattern reused reader-side).

const TAGS = ["a", "b", "c", "d"] as const;
const REG_ID = "reader-eq";

interface Watermark {
  soft: number;
  hard: number;
}

/** Apply frames (deltas and/or a snapshot) via per-axis max-on-write — the way a replica reduces
 * them (§4.1). The reducer of record for the reader-side equivalence theorem. */
function applyFrames(frames: readonly BusFrame[]): Map<string, Watermark> {
  const map = new Map<string, Watermark>();
  for (const frame of frames) {
    for (const change of frame.changes) {
      const w = map.get(change.tag) ?? { hard: 0, soft: 0 };
      if (change.softEpoch !== undefined) {
        w.soft = Math.max(w.soft, change.softEpoch);
      }
      if (change.hardEpoch !== undefined) {
        w.hard = Math.max(w.hard, change.hardEpoch);
      }
      map.set(change.tag, w);
    }
  }
  return map;
}

/** The §4.1 validity table over one known tag's watermark and an entry's `bornEpoch`. */
function classify(born: number, w: Watermark): Validity {
  if (w.hard > born) {
    return "dead";
  }
  if (w.soft > born) {
    return "stale";
  }
  return "fresh";
}

const command = fc.record({
  op: fc.constantFrom("expire" as const, "delete" as const),
  tag: fc.constantFrom(...TAGS),
});
/** One flush = one delta batch's coalesced purges. */
const group = fc.array(command, { maxLength: 3, minLength: 1 });
const groupsArb = fc.array(group, { maxLength: 6, minLength: 1 });

type Group = readonly {
  readonly op: "expire" | "delete";
  readonly tag: string;
}[];

/** The delta batch at `seq` (epoch === seq): the group's purges as soft/hard changes. */
function deltaFor(seq: number, g: Group): BusFrame {
  const changes: TagChange[] = g.map((c) =>
    c.op === "expire"
      ? { softEpoch: seq, tag: c.tag }
      : { hardEpoch: seq, tag: c.tag }
  );
  return { changes, fromEpoch: seq - 1, toEpoch: seq };
}

/** A cumulative from-0 snapshot over deltas `1..k` (§2). */
function snapFor(k: number, deltas: readonly BusFrame[]): BusFrame {
  const cumulative = applyFrames(deltas.slice(0, k));
  const changes: TagChange[] = [...cumulative.entries()].map(([tag, w]) => ({
    hardEpoch: w.hard,
    softEpoch: w.soft,
    tag,
  }));
  return { changes, fromEpoch: 0, toEpoch: k };
}

async function seed(
  parts: ReturnType<typeof buildReaderParts>,
  entries: readonly (readonly [string, unknown])[]
): Promise<void> {
  for (const [key, value] of entries) {
    await parts.store.set(key, JSON.stringify(value));
  }
}

describe("reader-side snapshot-replay equivalence (property, §2/F12)", () => {
  test("snap(0,k] + tail(k,n] ≡ deltas(0,n] — same knowledge, agreeing with the oracle", async () => {
    const holds = fc.assert(
      fc.asyncProperty(groupsArb, async (groups) => {
        const deltas = groups.map((g, i) => deltaFor(i + 1, g));
        const n = deltas.length;
        const oracle = applyFrames(deltas);
        const touched = [...oracle.keys()];
        const pointer = (k: number): unknown => ({
          epoch: n,
          flushedAt: n,
          lastBatch: n,
          regId: REG_ID,
          snapBatch: k,
        });

        for (let k = 1; k <= n; k += 1) {
          // FULL reader: every delta present, plain delta-pass resync.
          const full = buildReaderParts(REG_ID);
          await seed(full, [
            [full.keys.stateKey, pointer(k)],
            ...deltas.map((d, i): [string, unknown] => [
              full.keys.deltaKey(i + 1),
              d,
            ]),
          ]);
          full.bus.emit({ kind: "reset" });
          full.bus.emit({ head: n, kind: "hello" });
          const fullStatus = await full.inval.resync();

          // SNAP reader: snapshot at k + tail deltas only (deltas 1..k absent), snapshot bridge.
          const snap = buildReaderParts(REG_ID);
          await seed(snap, [
            [snap.keys.stateKey, pointer(k)],
            [snap.keys.snapKey(k), snapFor(k, deltas)],
            ...deltas
              .slice(k)
              .map((d, i): [string, unknown] => [
                snap.keys.deltaKey(k + i + 1),
                d,
              ]),
          ]);
          snap.bus.emit({ kind: "reset" });
          snap.bus.emit({ head: n, kind: "hello" });
          const snapStatus = await snap.inval.resyncViaSnapshot();

          if (fullStatus !== "converged" || snapStatus !== "converged") {
            return false;
          }
          if (full.inval.suspect || snap.inval.suspect) {
            return false; // both must have cleared suspicion (reached the declared head)
          }

          // Both readers must agree with each other AND the oracle on every touched tag across
          // bornEpochs that straddle the watermark boundaries.
          for (const tag of touched) {
            const w = oracle.get(tag);
            if (w === undefined) {
              return false;
            }
            for (let born = 0; born <= n + 1; born += 1) {
              const env = probeEnvelope([tag], born);
              const fullR = await full.inval.validate(env, "eventual", "miss");
              const snapR = await snap.inval.validate(env, "eventual", "miss");
              const oracleV = classify(born, w);
              if (
                fullR.validity !== snapR.validity ||
                fullR.validity !== oracleV
              ) {
                return false;
              }
            }
          }
        }
        return true;
      })
    );
    await expect(holds).resolves.toBeUndefined();
  });
});
