// Ordinary unit teeth (NOT sacred) — C5-b, erratum E27. The reader-facing half of `explain`: the
// verdict-with-evidence (E27 (2)) and the reader's own state surfaced via read-only accessors
// (E27 (4)). The verdict is a PURE classify over the reader's CURRENT knowledge — no live check, no
// resync, no recording (introspection, not testimony) — and `unknown` stays `unknown`.
//
// Each case bites ONE branch (rule 13) — the named mutation reds it:
//   1. verdict table — a locally-DEAD tag ⇒ `dead` with the purge (hard) epoch as evidence; a
//                      locally-STALE tag ⇒ `stale` with the soft epoch; a KNOWN-clean tag ⇒ `fresh`;
//                      an UNKNOWN tag ⇒ `unknown`. (Blind: force one classification ⇒ reds.)
//   2. unknown-stays-unknown / the E20 (i) gate — a SUSPECT reader fails closed to `unknown` even
//                      when its local knowledge would say `fresh`. (Blind: drop the suspect gate ⇒
//                      the suspect case returns `fresh` ⇒ reds.)
//   3. appliedEpoch exposition — reader.appliedEpoch tracks the reader's applied epoch. (Blind:
//                      hardcode 0 ⇒ the advanced case reds.)
//   4. suspicion exposition — reader.suspect tracks the reader's actual suspicion. (Blind: hardcode
//                      a constant ⇒ the raised case reds.)
//   5. terminal exposition — reader.terminal tracks the replication shell's terminal verdict. (Blind:
//                      hardcode the poller getter, OR explain's read of it ⇒ the terminal case reds.)
//
// Lane: unit. Deterministic — cases 1/2 drive a REAL Invalidation over hand-published bus frames
// (the envelope's tag set is controlled directly, so a single-tag verdict is exact); cases 3/4/5 use
// the SimClock `world()` and the poller's explicit `nowMs` ticks. No timing tolerance, no retry.

import assert from "node:assert/strict";

import { memory } from "@astilba/cache";
import type { Envelope } from "@astilba/cache";
import { DEFAULT_REG_ID, mirrorKeys } from "@astilba/cache/registry";
import {
  FakeBus,
  FakeRegistry,
  mkTag,
  SimClock,
  world,
} from "@astilba/harness";
import { describe, expect, it } from "vitest";

import { replicationPollerOf } from "../../src/cache";
import { Invalidation } from "../../src/invalidation";

// ── Part A: the verdict, over a REAL reader with a controlled tag set ─────────────────────────────

const TAG = "t0";

/** A real `Invalidation` plus the bus its knowledge is fed over. The registry is wired only for
 * `regId`; every watermark enters through a hand-published frame, so the reader's knowledge is
 * exact. */
function buildReader(): { inval: Invalidation; bus: FakeBus } {
  const clock = new SimClock(0);
  const bus = new FakeBus(clock);
  const registry = new FakeRegistry(clock, { bus });
  const inval = new Invalidation(registry, memory(), bus);
  // Put the reader IN USE (E28): construction captures the bus and defers the subscription to first
  // use, so a reader nobody has read from is not listening. These cases drive the verdict over a
  // reader in its steady state, so the fixture subscribes explicitly — exactly what a first read
  // would do.
  inval.ensureSubscribed();
  return { bus, inval };
}

/** A minimal valid `val` envelope carrying exactly `tags` and `bornEpoch` — the only two fields the
 * verdict reads. */
function envelopeWith(tags: readonly string[], bornEpoch: number): Envelope {
  return {
    bornEpoch,
    bornMs: 0,
    codecId: "safe/1",
    enc: "json",
    grace: 0,
    key: "k",
    kind: "val",
    scope: "pub",
    size: 1,
    src: "factory",
    storedAt: 0,
    tags,
    ttl: 0,
    v: 3,
    val: "x",
  };
}

describe("explain — the verdict is a pure classify over current knowledge (E27 (2))", () => {
  it("an UNKNOWN tag ⇒ unknown (fail-closed narration), with no fabricated evidence", () => {
    const { inval } = buildReader();
    // No frame delivered ⇒ the tag is unknown.
    expect(inval.explainVerdict(envelopeWith([TAG], 0))).toStrictEqual({
      hardEpoch: 0,
      softEpoch: 0,
      validity: "unknown",
    });
  });

  it("a KNOWN-clean tag ⇒ fresh, with the known watermark as evidence", () => {
    const { inval, bus } = buildReader();
    bus.publish({
      changes: [{ softEpoch: 1, tag: TAG }],
      fromEpoch: 0,
      toEpoch: 1,
    });
    // bornEpoch 10 postdates the soft watermark ⇒ fresh.
    expect(inval.explainVerdict(envelopeWith([TAG], 10))).toStrictEqual({
      hardEpoch: 0,
      softEpoch: 1,
      validity: "fresh",
    });
  });

  it("a soft-purged tag ⇒ stale, with the soft epoch as evidence", () => {
    const { inval, bus } = buildReader();
    bus.publish({
      changes: [{ softEpoch: 5, tag: TAG }],
      fromEpoch: 0,
      toEpoch: 5,
    });
    expect(inval.explainVerdict(envelopeWith([TAG], 0))).toStrictEqual({
      hardEpoch: 0,
      softEpoch: 5,
      validity: "stale",
    });
  });

  it("a hard-purged tag ⇒ dead, with the PURGE (hard) epoch as evidence", () => {
    const { inval, bus } = buildReader();
    bus.publish({
      changes: [{ hardEpoch: 5, tag: TAG }],
      fromEpoch: 0,
      toEpoch: 5,
    });
    // The tooth: `dead` with `hardEpoch: 5` — the purge epoch. Blinding the classification (e.g.
    // forcing `fresh`) reds both the validity and the evidence here.
    expect(inval.explainVerdict(envelopeWith([TAG], 0))).toStrictEqual({
      hardEpoch: 5,
      softEpoch: 0,
      validity: "dead",
    });
  });

  it("a SUSPECT reader fails closed to unknown even when local knowledge would say fresh (E20 (i))", () => {
    const { inval, bus } = buildReader();
    // Make the tag KNOWN-clean first (a fresh verdict would follow from local knowledge alone) …
    bus.publish({
      changes: [{ softEpoch: 1, tag: TAG }],
      fromEpoch: 0,
      toEpoch: 1,
    });
    // … then a NON-CONTIGUOUS frame proves a hole ⇒ the map goes suspect (not applied).
    bus.publish({
      changes: [{ hardEpoch: 9, tag: "other" }],
      fromEpoch: 5,
      toEpoch: 9,
    });
    expect(inval.suspect).toBeTruthy();
    // The tooth: local knowledge says fresh (soft 1 ≤ born 10), but the suspect gate forces unknown.
    // Dropping the gate returns `fresh` here ⇒ reds.
    expect(inval.explainVerdict(envelopeWith([TAG], 10)).validity).toBe(
      "unknown"
    );
  });
});

// ── Part A (end-to-end): unknown-stays-unknown through the whole explain() path ──────────────────

describe("explain — a stored entry whose tags the reader does not know reads unknown (E27 (2))", () => {
  it("a freshly-filled entry (tags never made known) ⇒ verdict unknown, not a fabricated fresh", async () => {
    const iso = world({ isolates: 1 }).isolate(0);
    await iso.cache.getOrSet({
      factory: async (): Promise<string> => "v",
      key: "k",
      tags: [mkTag("t0")],
    });
    const explanation = await iso.cache.explain("k");
    assert.ok(
      explanation.kind === "present",
      "expected the stored entry to be present"
    );
    // A fill never makes its tags KNOWN (it is not a purge), so classify fails closed to unknown —
    // the reserved `__k`/`__ns` tags are unknown too. Blinding classify to `fresh` reds this.
    expect(explanation.verdict.validity).toBe("unknown");
  });
});

// ── Part B: the reader's own state, surfaced via read-only accessors (E27 (4)) ───────────────────

describe("explain — reader state is exposed from already-tracked state (E27 (4))", () => {
  it("appliedEpoch tracks the reader's applied epoch (a delivered purge advances it)", async () => {
    const w = world({ isolates: 1 });
    const iso = w.isolate(0);
    // A fresh reader rests at applied epoch 0.
    const before = await iso.cache.explain("k");
    expect(before.reader.appliedEpoch).toBe(0);

    // A delivered hard purge advances the applied epoch to 1.
    await iso.cache.delete({ tag: mkTag("t0") });
    await w.advance(0);
    const after = await iso.cache.explain("k");
    // The tooth: hardcoding appliedEpoch to 0 reds this.
    expect(after.reader.appliedEpoch).toBe(1);
  });

  it("suspect tracks the reader's actual suspicion (a dropped frame raises it)", async () => {
    const w = world({ isolates: 1 });
    const iso = w.isolate(0);
    const warm = await iso.cache.explain("k");
    expect(warm.reader.suspect).toBeFalsy();

    // A lost delta frame leaves the map suspect (the gap is unresolved).
    w.chaos.dropNextFrames(1);
    await iso.cache.delete({ tag: mkTag("t0") });
    await w.advance(0);
    const raised = await iso.cache.explain("k");
    // The tooth: hardcoding a constant reds either the warm case or this raised one.
    expect(raised.reader.suspect).toBeTruthy();
  });

  it("terminal tracks the replication shell's terminal-gap verdict", async () => {
    const w = world({ isolates: 1 });
    const iso = w.isolate(0);
    const poller = replicationPollerOf(iso.cache);
    assert.ok(poller !== undefined, "a registry-bearing cache has a poller");

    // A fresh reader is not terminal.
    const before = await iso.cache.explain("k");
    expect(before.reader.terminal).toBeFalsy();
    expect(poller.terminal).toBeFalsy();

    // Raise suspicion via a lost frame, then break the durable mirror the ladder resyncs off: a
    // pointer that claims a snapshot (snapBatch 5) and deltas up to 10, with NEITHER the snapshot nor
    // the deltas present. The ladder exhausts its delta + snapshot budgets and rules terminal-gap.
    w.chaos.dropNextFrames(1);
    await iso.cache.delete({ tag: mkTag("z") });
    await w.advance(1000); // let any durable mirror flush settle before we overwrite it
    const keys = mirrorKeys(DEFAULT_REG_ID);
    const rep = w.store.replica(0);
    await rep.delete(keys.deltaKey(1));
    await rep.set(
      keys.stateKey,
      JSON.stringify({
        epoch: 10,
        flushedAt: 1,
        lastBatch: 10,
        regId: DEFAULT_REG_ID,
        snapBatch: 5,
      })
    );

    // Drive the poller past every ladder rung (rung 4 spends the snapshot budget ⇒ terminal-gap).
    for (let nowMs = 0; nowMs <= 40_000; nowMs += 1000) {
      await poller.onPollTick(nowMs);
    }
    expect(poller.terminal).toBeTruthy();

    const explanation = await iso.cache.explain("k");
    // The tooth: hardcoding the poller getter (or explain's read of it) to `false` reds this.
    expect(explanation.reader.terminal).toBeTruthy();
  });
});
