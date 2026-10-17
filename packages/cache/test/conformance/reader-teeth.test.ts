import { describe, expect, it } from "vitest";

import {
  makeNeverRaiseReaderHarness,
  makeNoTerminalReaderHarness,
  makeOverFetchReaderHarness,
  makeServeThroughReaderHarness,
  makeStrictSnapReaderHarness,
  makeStuckPollReaderHarness,
  makeUnderCoveringSnapReaderHarness,
} from "./fixtures/broken";
import { makeReferenceReaderHarness } from "./fixtures/reference";
import { readerChecks } from "./runner/reader";

// Reader teeth: each reader check is driven against a
// deliberately-wrong reader and MUST fail, and against the reference reader and MUST pass.
// Green-on-reference alone proves nothing — the reference IS the real Invalidation — so each
// check here demonstrates it discriminates a liar. We assert only THAT the targeted check
// rejects, never on an exact message (so the proofs don't go brittle).

describe("reader teeth — every check discriminates a wrong reader", () => {
  it("stale-pointer check REJECTS a serve-through reader", async () => {
    await expect(
      readerChecks.stalePointerFailsClosed(makeServeThroughReaderHarness())
    ).rejects.toThrow();
  });

  it("stale-pointer check ACCEPTS the reference (non-vacuity)", async () => {
    await expect(
      readerChecks.stalePointerFailsClosed(makeReferenceReaderHarness())
    ).resolves.toBeUndefined();
  });

  it("hole-never-serves-through check REJECTS a serve-through reader", async () => {
    await expect(
      readerChecks.holeNeverServesThrough(makeServeThroughReaderHarness())
    ).rejects.toThrow();
  });

  it("hole-never-serves-through check ACCEPTS the reference (non-vacuity)", async () => {
    await expect(
      readerChecks.holeNeverServesThrough(makeReferenceReaderHarness())
    ).resolves.toBeUndefined();
  });

  it("probe-past-pointer check REJECTS an over-fetching reader", async () => {
    await expect(
      readerChecks.neverProbesPastPointer(makeOverFetchReaderHarness())
    ).rejects.toThrow();
  });

  it("probe-past-pointer check ACCEPTS the reference (non-vacuity)", async () => {
    await expect(
      readerChecks.neverProbesPastPointer(makeReferenceReaderHarness())
    ).resolves.toBeUndefined();
  });

  it("terminal-hole check REJECTS a reader that never rules terminal (would spin)", async () => {
    await expect(
      readerChecks.terminalHolePermanent(makeNoTerminalReaderHarness())
    ).rejects.toThrow();
  });

  it("terminal-hole check ACCEPTS the reference (non-vacuity)", async () => {
    await expect(
      readerChecks.terminalHolePermanent(makeReferenceReaderHarness())
    ).resolves.toBeUndefined();
  });

  it("lagging-snapBatch check REJECTS a reader that flags the lag anomalous", async () => {
    await expect(
      readerChecks.laggingSnapBatchConverges(makeStrictSnapReaderHarness())
    ).rejects.toThrow();
  });

  it("lagging-snapBatch check ACCEPTS the reference (non-vacuity)", async () => {
    await expect(
      readerChecks.laggingSnapBatchConverges(makeReferenceReaderHarness())
    ).resolves.toBeUndefined();
  });

  it("blessed-but-missing-snap check REJECTS an under-covering reader (claims coverage it lacks)", async () => {
    await expect(
      readerChecks.blessedButMissingSnap(makeUnderCoveringSnapReaderHarness())
    ).rejects.toThrow();
  });

  it("blessed-but-missing-snap check ACCEPTS the reference (non-vacuity)", async () => {
    await expect(
      readerChecks.blessedButMissingSnap(makeReferenceReaderHarness())
    ).resolves.toBeUndefined();
  });

  it("E5-raising check REJECTS a reader that never raises on missed heartbeats", async () => {
    await expect(
      readerChecks.e5DerivativeRaising(makeNeverRaiseReaderHarness())
    ).rejects.toThrow();
  });

  it("E5-raising check ACCEPTS the reference (non-vacuity)", async () => {
    await expect(
      readerChecks.e5DerivativeRaising(makeReferenceReaderHarness())
    ).resolves.toBeUndefined();
  });

  it("pollDue check REJECTS a reader whose predicate is stuck false", async () => {
    await expect(
      readerChecks.pollDuePredicate(makeStuckPollReaderHarness())
    ).rejects.toThrow();
  });

  it("pollDue check ACCEPTS the reference (non-vacuity)", async () => {
    await expect(
      readerChecks.pollDuePredicate(makeReferenceReaderHarness())
    ).resolves.toBeUndefined();
  });
});
