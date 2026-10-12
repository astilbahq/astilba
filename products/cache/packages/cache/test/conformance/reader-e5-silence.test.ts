import type { StatePointer } from "@astilba/cache/registry";
import { describe, expect, test } from "vitest";

import { buildReaderParts } from "./fixtures/reader-parts";

// C2-b-2-a — E5 elapsed-silence monitor teeth (§4.2, CACHE-27). The reference reader's default
// heartbeat interval is 30s ⇒ suspicion raises after 3× = 90s of OBSERVED SILENCE. The monitor
// measures elapsed silence (not a count of non-advancing ticks), so the raise is sampling-rate-
// independent. These pin, and are the red target for, the three rule-13 mutations run in review:
//   • drop the elapsed comparison ⇒ the fast-sampling tooth fires (raises far too early);
//   • drop the guard-before-write ⇒ the reordered-tick tooth fires (a stale tick delays the raise);
//   • drop the baseline reset on an advance ⇒ the advance tooth fires (a live writer false-raises).

const REG = "e5-silence";

function ptr(regId: string, flushedAt: number, epoch = 1): StatePointer {
  return { epoch, flushedAt, lastBatch: 1, regId, snapBatch: 0 };
}

describe("E5 elapsed-silence monitor is sampling-rate-independent (§4.2, CACHE-27)", () => {
  test("fast sampling does NOT raise before 90s of real silence (no-raise at 85s, raise at 90s)", () => {
    const { inval } = buildReaderParts(REG);
    const dead = ptr(REG, 5); // a frozen flushedAt — a dead writer
    // 5s ticks, six per heartbeat interval: raw non-advance COUNTING would raise inside ~15s; the
    // elapsed-silence monitor does not until 90s of wall time has actually been observed.
    for (let t = 0; t <= 85_000; t += 5000) {
      inval.notePointerObservation(dead, t);
    }
    expect(inval.suspect).toBeFalsy(); // 85s of silence across 18 non-advancing ticks — still calm
    inval.notePointerObservation(dead, 90_000);
    expect(inval.suspect).toBeTruthy(); // 90s elapsed ⇒ raise
  });

  test("default cadence raises at 3× the interval (samples at 0/60/120s ⇒ raise at 120s)", () => {
    const { inval } = buildReaderParts(REG);
    const dead = ptr(REG, 5);
    inval.notePointerObservation(dead, 0);
    inval.notePointerObservation(dead, 60_000);
    expect(inval.suspect).toBeFalsy(); // 60s < 90s
    inval.notePointerObservation(dead, 120_000);
    expect(inval.suspect).toBeTruthy(); // first observation that can witness ≥ 90s of silence
  });

  test("an advancing flushedAt RESETS the silence window — a slow-but-live writer never false-raises", () => {
    const { inval } = buildReaderParts(REG);
    inval.notePointerObservation(ptr(REG, 5), 0);
    inval.notePointerObservation(ptr(REG, 5), 60_000); // 60s of silence
    inval.notePointerObservation(ptr(REG, 9, 2), 80_000); // flushedAt advanced ⇒ window resets to 80s
    inval.notePointerObservation(ptr(REG, 9, 2), 160_000); // only 80s of silence SINCE the reset
    expect(inval.suspect).toBeFalsy(); // < 90s since the reset, though 160s of wall time elapsed
    inval.notePointerObservation(ptr(REG, 9, 2), 170_000); // 90s since the reset ⇒ raise
    expect(inval.suspect).toBeTruthy();
  });

  test("a reordered tick is INERT — it cannot reset the silence window to a stale time (guard before write)", () => {
    const { inval } = buildReaderParts(REG);
    inval.notePointerObservation(ptr(REG, 100), 0); // seed — silence starts at t=0
    inval.notePointerObservation(ptr(REG, 100), 50_000); // watch advances to 50s
    // A reordered observation (nowMs 10s ≤ the last watch 50s) carrying a SPURIOUSLY HIGHER
    // flushedAt (clock skew / out-of-order read). Processed, it would look like an advance and
    // reset the silence window to t=10s — delaying the dead-writer raise. It must be a no-op.
    inval.notePointerObservation(ptr(REG, 110, 2), 10_000);
    inval.notePointerObservation(ptr(REG, 100), 90_000); // 90s of silence measured from t=0 ⇒ raise
    expect(inval.suspect).toBeTruthy();
  });
});
