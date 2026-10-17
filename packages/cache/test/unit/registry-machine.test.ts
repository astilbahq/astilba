import type { BusFrame } from "@astilba/cache";
import { RegistryMachine } from "@astilba/cache/registry";
import type {
  AdvancePointerEffect,
  FlushEffect,
  PersistBatchEffect,
  PersistSnapshotEffect,
  RegistryEffect,
  ScheduleFlushEffect,
  SnapshotSkippedEffect,
} from "@astilba/cache/registry";
import fc from "fast-check";
import { describe, expect, test } from "vitest";

// Unit coverage for the extracted pure registry machine (@astilba/cache/registry).
// The sacred suite + the Registry conformance contract exercise it via the re-wrapped
// FakeRegistry (immediate-flush host). These direct tests pin the parts the fake's
// window-0 policy never reaches on its own — the coalescing flush, gc(nowMs), and the
// byte-stable wire format the DO host must reproduce (thin-shell rule).

function persistOf(effects: readonly RegistryEffect[]): PersistBatchEffect {
  const effect = effects.find((e) => e.kind === "persist-batch");
  if (!effect || effect.kind !== "persist-batch") {
    throw new Error("expected a persist-batch effect");
  }
  return effect;
}

function scheduleOf(effects: readonly RegistryEffect[]): ScheduleFlushEffect {
  const effect = effects.find((e) => e.kind === "schedule-flush");
  if (!effect || effect.kind !== "schedule-flush") {
    throw new Error("expected a schedule-flush effect");
  }
  return effect;
}

/** The persist-snapshot effect if the cadence fired this flush, else undefined. */
function maybeSnapshotOf(
  effects: readonly RegistryEffect[]
): PersistSnapshotEffect | undefined {
  const effect = effects.find((e) => e.kind === "persist-snapshot");
  return effect && effect.kind === "persist-snapshot" ? effect : undefined;
}

function snapshotOf(effects: readonly RegistryEffect[]): PersistSnapshotEffect {
  const effect = maybeSnapshotOf(effects);
  if (!effect) {
    throw new Error("expected a persist-snapshot effect");
  }
  return effect;
}

/** The flush's single terminal pointer bless. */
function advanceOf(effects: readonly FlushEffect[]): AdvancePointerEffect {
  const effect = effects.find((e) => e.kind === "advance-pointer");
  if (!effect || effect.kind !== "advance-pointer") {
    throw new Error("expected an advance-pointer effect");
  }
  return effect;
}

/** The `snapshot-skipped-oversize` telemetry effect if the size gate tripped, else undefined. */
function maybeSkipOf(
  effects: readonly FlushEffect[]
): SnapshotSkippedEffect | undefined {
  const effect = effects.find((e) => e.kind === "telemetry");
  return effect && effect.kind === "telemetry" ? effect : undefined;
}

/** The `snapshot-skipped-oversize` telemetry effect; throws if the size gate did not trip. */
function skipOf(effects: readonly FlushEffect[]): SnapshotSkippedEffect {
  const effect = maybeSkipOf(effects);
  if (!effect) {
    throw new Error("expected a snapshot-skipped-oversize telemetry effect");
  }
  return effect;
}

/**
 * Apply a sequence of frames (deltas and/or a snapshot) via per-axis max-on-write, the way
 * a replica reduces them, and normalize to a stable comparable string. The reducer of
 * record for the snapshot-replay equivalence theorem.
 */
function applyFrames(frames: readonly BusFrame[]): string {
  const soft = new Map<string, number>();
  const hard = new Map<string, number>();
  for (const frame of frames) {
    for (const change of frame.changes) {
      if (change.softEpoch !== undefined) {
        soft.set(
          change.tag,
          Math.max(soft.get(change.tag) ?? 0, change.softEpoch)
        );
      }
      if (change.hardEpoch !== undefined) {
        hard.set(
          change.tag,
          Math.max(hard.get(change.tag) ?? 0, change.hardEpoch)
        );
      }
    }
  }
  const tags = [...new Set([...soft.keys(), ...hard.keys()])].toSorted();
  return JSON.stringify(
    tags.map((tag) => ({
      hard: hard.get(tag) ?? 0,
      soft: soft.get(tag) ?? 0,
      tag,
    }))
  );
}

/** Drive one mutation and immediately flush it at nowMs 0 — the window-0 rhythm. */
function runFlush(
  m: RegistryMachine,
  op: "expire" | "delete",
  tags: readonly string[]
): readonly FlushEffect[] {
  if (op === "expire") {
    m.expire(tags, 0);
  } else {
    m.delete(tags, 0);
  }
  return m.flush(0);
}

describe(RegistryMachine, () => {
  test("a mutation mints a strictly-monotone epoch and schedules a flush at nowMs", () => {
    const m = new RegistryMachine();
    expect(m.currentEpoch).toBe(0);
    const e1 = m.expire(["a"], 1234);
    expect(e1.ack.epoch).toBe(1);
    expect(m.currentEpoch).toBe(1);
    expect(scheduleOf(e1.effects).deadlineMs).toBe(1234);
    const e2 = m.delete(["a"], 1234);
    expect(e2.ack.epoch).toBe(2);
  });

  test("watermarks are max-on-write and soft never regresses hard (§4.1)", () => {
    const m = new RegistryMachine();
    m.delete(["t"], 0); // hardEpoch = 1
    expect(m.watermarkOf("t")).toStrictEqual({ hardEpoch: 1, softEpoch: 0 });
    m.expire(["t"], 0); // softEpoch = 2, hard unchanged
    expect(m.watermarkOf("t")).toStrictEqual({ hardEpoch: 1, softEpoch: 2 });
  });

  test("knowledgeOf is known/authoritative with throughEpoch at the current epoch (§4.4)", () => {
    const m = new RegistryMachine();
    m.expire(["a"], 0);
    const [a, b] = m.knowledgeOf(["a", "untouched"]);
    expect(a).toStrictEqual({
      hardEpoch: 0,
      known: true,
      softEpoch: 1,
      throughEpoch: 1,
    });
    expect(b).toStrictEqual({
      hardEpoch: 0,
      known: true,
      softEpoch: 0,
      throughEpoch: 1,
    });
  });

  test("an immediate single-mutation flush is byte-stable in the v0.1 wire format", () => {
    const m = new RegistryMachine();
    m.expire(["a"], 5000);
    const effects = m.flush(5000);
    const persist = persistOf(effects);
    expect(persist.seq).toBe(1);
    expect(JSON.stringify(persist.frame)).toBe(
      '{"changes":[{"softEpoch":1,"tag":"a"}],"fromEpoch":0,"toEpoch":1}'
    );
    // The pointer is the single terminal effect; its byte-stable form now carries the
    // `regId` echo (alphabetical key order: …lastBatch, regId, snapBatch).
    expect(JSON.stringify(advanceOf(effects).pointer)).toBe(
      '{"epoch":1,"flushedAt":5000,"lastBatch":1,"regId":"default","snapBatch":0}'
    );
  });

  test("flush emits nothing when no epoch advanced since the last flush", () => {
    const m = new RegistryMachine();
    m.expire(["a"], 0);
    expect(m.flush(0)).toHaveLength(3); // persist-batch + broadcast-frame + advance-pointer
    expect(m.flush(0)).toStrictEqual([]); // buffer drained
  });

  test("coalescing: many mutations before a flush mint ONE wider-range batch (E12)", () => {
    const m = new RegistryMachine();
    m.expire(["a"], 0); // 0 -> 1
    m.delete(["b"], 0); // 1 -> 2
    const persist = persistOf(m.flush(9000));
    expect(persist.seq).toBe(1); // one batch = lastBatch + 1
    expect(persist.frame.fromEpoch).toBe(0);
    expect(persist.frame.toEpoch).toBe(2);
    expect(persist.frame.changes).toStrictEqual([
      { softEpoch: 1, tag: "a" },
      { hardEpoch: 2, tag: "b" },
    ]);
    // A subsequent mutation mints the NEXT durable seq.
    m.expire(["c"], 0);
    expect(persistOf(m.flush(0)).seq).toBe(2);
  });

  test("a flush emits the CANONICAL batch: dup tags folded to max, lex-sorted, one serialization (E12)", () => {
    const m = new RegistryMachine();
    // Non-lexicographic input order (b before a) plus a tag touched three times across
    // both axes — the discriminating window a plain concatenation could not canonicalize.
    m.expire(["b", "a"], 0); // 0 -> 1: soft b=1, soft a=1
    m.delete(["a"], 0); //     1 -> 2: hard a=2  (a now spans both axes)
    m.expire(["a"], 0); //     2 -> 3: soft a=3  (dup soft a: 1 then 3 -> folds to max 3)
    const persist = persistOf(m.flush(0));
    expect(persist.seq).toBe(1); // still ONE batch
    // a appears EXACTLY once carrying max soft(3)/hard(2); tags lex-sorted (a before b,
    // though b was mutated first); b's untouched hard axis is omitted (no delta in range).
    expect(persist.frame.changes).toStrictEqual([
      { hardEpoch: 2, softEpoch: 3, tag: "a" },
      { softEpoch: 1, tag: "b" },
    ]);
    // Exactly ONE serialization — the byte-for-byte contract the DO host must reproduce.
    expect(JSON.stringify(persist.frame)).toBe(
      '{"changes":[{"hardEpoch":2,"softEpoch":3,"tag":"a"},{"softEpoch":1,"tag":"b"}],"fromEpoch":0,"toEpoch":3}'
    );
  });

  test("re-announcement across batches is legal and idempotent under max-on-write (E12)", () => {
    const m = new RegistryMachine();
    m.expire(["a"], 0); // batch 1: soft a=1
    expect(persistOf(m.flush(0)).frame.changes).toStrictEqual([
      { softEpoch: 1, tag: "a" },
    ]);
    m.expire(["a"], 0); // batch 2: a re-appears at a higher epoch — NOT suppressed
    const b2 = persistOf(m.flush(0)).frame;
    expect(b2.fromEpoch).toBe(1);
    expect(b2.toEpoch).toBe(2);
    expect(b2.changes).toStrictEqual([{ softEpoch: 2, tag: "a" }]);
  });

  test("producer side: a flushed batch NEVER emits an axis-empty change (F12 addendum)", () => {
    const m = new RegistryMachine();
    // A representative window touching soft-only, hard-only, and both-axes tags, with a
    // repeated tag and non-lexicographic order — the shapes canonicalization must fold.
    m.expire(["b", "a"], 0); // soft b, soft a
    m.delete(["a"], 0); //     hard a (a spans both axes)
    m.delete(["c"], 0); //     hard-only c
    m.expire(["a"], 0); //     soft a again (dup)
    const persist = persistOf(m.flush(0));
    expect(persist.frame.changes.length).toBeGreaterThan(0);
    for (const change of persist.frame.changes) {
      // Illegal otherwise: a change with neither axis asserts nothing (addendum).
      expect(
        change.softEpoch !== undefined || change.hardEpoch !== undefined
      ).toBeTruthy();
    }
  });

  test("producer side (property): no random mutation window flushes an axis-empty change (F12)", () => {
    const command = fc.record({
      op: fc.constantFrom("expire" as const, "delete" as const),
      tags: fc.uniqueArray(fc.constantFrom("a", "b", "c", "d"), {
        minLength: 1,
      }),
    });
    const holds = (): void => {
      fc.assert(
        fc.property(fc.array(command, { minLength: 1 }), (window) => {
          const m = new RegistryMachine();
          for (const { op, tags } of window) {
            if (op === "expire") {
              m.expire(tags, 0);
            } else {
              m.delete(tags, 0);
            }
          }
          const persist = persistOf(m.flush(0));
          // Every emitted change carries at least one axis — never both-absent.
          return persist.frame.changes.every(
            (change) =>
              change.softEpoch !== undefined || change.hardEpoch !== undefined
          );
        })
      );
    };
    // `fc.assert` throws a counterexample on violation; asserting it does NOT is the check.
    expect(holds).not.toThrow();
  });

  test("gc horizon is a monotone high-water; uncertainty never shortens it (E11)", () => {
    const m = new RegistryMachine();
    expect(m.gcHorizonMs).toBe(0);
    m.registerRetention(1000);
    expect(m.gc(10_000)).toStrictEqual({
      collectBeforeMs: 9000,
      horizonMs: 1000,
    });
    m.registerRetention(5000);
    expect(m.gcHorizonMs).toBe(5000); // a larger lease raises the horizon
    m.registerRetention(2000);
    expect(m.gcHorizonMs).toBe(5000); // a smaller lease never retracts it
    expect(m.gc(10_000).collectBeforeMs).toBe(5000);
  });

  // ── Snapshot emission ────────────────────────────────────────────

  test("the default cadence emits NO snapshot on an ordinary single-mutation flush (§4.2)", () => {
    const m = new RegistryMachine(); // defaults: 500 batches / 6h
    m.expire(["a"], 5000);
    const effects = m.flush(5000);
    expect(maybeSnapshotOf(effects)).toBeUndefined();
    expect(m.snapBatch).toBe(0);
  });

  test("cadence fires on the seq clause: a snapshot at snapshotEveryBatches (§4.2)", () => {
    // A large interval isolates the batch clause. Each mutation is flushed on its own.
    const m = new RegistryMachine({
      snapshotEveryBatches: 3,
      snapshotIntervalMs: Number.MAX_SAFE_INTEGER,
    });
    expect(maybeSnapshotOf(runFlush(m, "expire", ["a"]))).toBeUndefined(); // seq 1
    runFlush(m, "delete", ["b"]); // seq 2 — still below the cadence
    expect(m.snapBatch).toBe(0); // no snapshot minted before seq 3
    const snap = snapshotOf(runFlush(m, "expire", ["c"])); // seq 3 — 3 − 0 ≥ 3
    expect(snap.seq).toBe(3);
    expect(snap.frame).toMatchObject({ fromEpoch: 0, toEpoch: 3 });
    expect(m.snapBatch).toBe(3); // advanced only on emission
  });

  test("cadence fires on the time clause: a snapshot at snapshotIntervalMs (§4.2)", () => {
    // A huge batch budget isolates the time clause; the baseline is the first flush's nowMs.
    const m = new RegistryMachine({
      snapshotEveryBatches: 10_000,
      snapshotIntervalMs: 1000,
    });
    m.expire(["a"], 0);
    expect(maybeSnapshotOf(m.flush(0))).toBeUndefined(); // seq 1, baseline = 0
    m.expire(["b"], 1000);
    const snap = snapshotOf(m.flush(1000)); // seq 2, 1000 − 0 ≥ 1000
    expect(snap.seq).toBe(2);
    expect(snap.frame.toEpoch).toBe(2);
    expect(m.snapBatch).toBe(2);
  });

  test("a snapshot IS the canonical batch over (0, seq] — same serializer, same bytes (F12)", () => {
    // snapshotEveryBatches: 1 ⇒ every flush also emits a snapshot over (0, epoch].
    const m = new RegistryMachine({ snapshotEveryBatches: 1 });
    runFlush(m, "expire", ["b", "a"]); // seq 1: soft a=1, b=1
    m.delete(["a"], 0); // hard a=2
    const snap = snapshotOf(m.flush(0)); // seq 2: snapshot over (0, 2]
    // Byte-for-byte the canonical form: a folded across both axes (max soft 1 / hard 2),
    // b soft-only, lex-sorted, key order hardEpoch→softEpoch→tag; frame changes→from→to.
    expect(JSON.stringify(snap.frame)).toBe(
      '{"changes":[{"hardEpoch":2,"softEpoch":1,"tag":"a"},{"softEpoch":1,"tag":"b"}],"fromEpoch":0,"toEpoch":2}'
    );
    // The unification, proven directly: a FRESH machine driven with the same mutations and one
    // coalesced flush over (0, 2] emits a delta batch whose bytes equal the snapshot's — one
    // serializer, whether the range starts at a snapshot or at zero.
    const ref = new RegistryMachine();
    ref.expire(["b", "a"], 0);
    ref.delete(["a"], 0);
    const refBatch = persistOf(ref.flush(0)).frame;
    expect(JSON.stringify(snap.frame)).toBe(JSON.stringify(refBatch));
  });

  test("a snapshot omits never-advanced axes and never-touched tags (F12)", () => {
    const m = new RegistryMachine({ snapshotEveryBatches: 1 });
    m.delete(["c"], 0); // epoch 1: hard-only c
    m.expire(["b"], 0); // epoch 2: soft-only b
    m.delete(["a"], 0); // epoch 3: a hard then…
    const snap = snapshotOf(runFlush(m, "expire", ["a"])); // epoch 4: …a soft — both axes
    const byTag = new Map(snap.frame.changes.map((c) => [c.tag, c]));
    // Each strict-equal pins the WHOLE change object, so a never-advanced axis being ABSENT
    // (not a fabricated 0) is asserted by construction — no extra axis-empty loop needed.
    expect(byTag.get("c")).toStrictEqual({ hardEpoch: 1, tag: "c" }); // soft absent
    expect(byTag.get("b")).toStrictEqual({ softEpoch: 2, tag: "b" }); // hard absent
    expect(byTag.get("a")).toStrictEqual({
      hardEpoch: 3,
      softEpoch: 4,
      tag: "a",
    }); // both
    expect(byTag.has("never-touched")).toBeFalsy(); // absent tag ≡ epoch 0
  });

  test("the ONE terminal pointer blesses the post-flush snapBatch atomically — no stale intermediate (§4.2, D17)", () => {
    const m = new RegistryMachine({ snapshotEveryBatches: 2 });
    // Before the cadence fires, the single pointer still carries snapBatch 0.
    expect(advanceOf(runFlush(m, "expire", ["a"])).pointer.snapBatch).toBe(0); // seq 1
    const e2 = runFlush(m, "expire", ["b"]); // seq 2 — crosses
    // Still exactly ONE pointer, already carrying the advanced snapBatch (no stale batch pointer
    // referencing the old snapshot) — atomic across lastBatch AND snapBatch.
    expect(e2.filter((e) => e.kind === "advance-pointer")).toHaveLength(1);
    expect(advanceOf(e2).pointer).toStrictEqual({
      epoch: 2,
      flushedAt: 0,
      lastBatch: 2,
      regId: "default",
      snapBatch: 2,
    });
  });

  test("emission order within a flush is batch → snapshot → broadcast → advance-pointer (§4.2, D17)", () => {
    const m = new RegistryMachine({ snapshotEveryBatches: 1 });
    m.expire(["a"], 0);
    const kinds = m.flush(0).map((e) => e.kind);
    // The terminal pointer is LAST, after every immutable-object effect — the once-only property
    // made structural in the machine's output.
    expect(kinds).toStrictEqual([
      "persist-batch",
      "persist-snapshot",
      "broadcast-frame",
      "advance-pointer",
    ]);
  });

  test("snapshot-replay equivalence (property): snap(0,k] + deltas(k,n] ≡ deltas(0,n] (F12)", () => {
    const command = fc.record({
      op: fc.constantFrom("expire" as const, "delete" as const),
      tags: fc.uniqueArray(fc.constantFrom("a", "b", "c", "d"), {
        minLength: 1,
      }),
    });
    // A "group" is the set of mutations coalesced into one flush; each flush also snapshots
    // (snapshotEveryBatches: 1), so we capture a full snapshot over (0, epoch_k] at every k.
    const group = fc.array(command, { minLength: 1, maxLength: 3 });
    const holds = (): void => {
      fc.assert(
        fc.property(
          fc.array(group, { minLength: 1, maxLength: 6 }),
          (groups) => {
            const m = new RegistryMachine({ snapshotEveryBatches: 1 });
            const deltas: BusFrame[] = [];
            const snaps: BusFrame[] = [];
            for (const g of groups) {
              for (const { op, tags } of g) {
                if (op === "expire") {
                  m.expire(tags, 0);
                } else {
                  m.delete(tags, 0);
                }
              }
              const effects = m.flush(0);
              deltas.push(persistOf(effects).frame);
              snaps.push(snapshotOf(effects).frame);
            }
            const n = deltas.length;
            const full = applyFrames(deltas);
            // For EVERY split point k in [1, n]: loading the snapshot at k then replaying the
            // deltas after k must reconstruct the same watermark map as replaying all deltas.
            for (let k = 1; k <= n; k += 1) {
              const snapK = snaps[k - 1];
              if (snapK === undefined) {
                return false; // unreachable: k ∈ [1, n] and snaps.length === n
              }
              const reconstructed = applyFrames([snapK, ...deltas.slice(k)]);
              if (reconstructed !== full) {
                return false;
              }
            }
            return true;
          }
        )
      );
    };
    expect(holds).not.toThrow();
  });

  test("rehydration is snapshot-exact: replaying flushes at their recorded nowMs reproduces snapBatch, replaying at 0 does not (the replay hazard) (§4.2, MI8)", () => {
    // A time-clause-only cadence (huge batch budget): the snapshot fires on wall time, the
    // clause a journal replay with nowMs=0 can never reach. This is the hazard the recorded
    // flush nowMs exists to close — mirrored exactly by the Coordinator's `#load` replay.
    const opts = {
      snapshotEveryBatches: 10_000,
      snapshotIntervalMs: 1000,
    } as const;
    const live = new RegistryMachine(opts);
    live.expire(["a"], 0);
    live.flush(0); // seq 1 — baseline nowMs 0, below the interval
    live.expire(["b"], 5000);
    live.flush(5000); // seq 2 — 5000 − 0 ≥ 1000 ⇒ snapshot, snapBatch = 2
    live.expire(["c"], 5500);
    live.flush(5500); // seq 3 — 5500 − 5000 < 1000 ⇒ no snapshot
    expect(live.snapBatch).toBe(2);

    // Faithful replay (mutations at 0, each flush at its RECORDED nowMs) — the Coordinator's
    // `flush(entry.nowMs ?? 0)` — reproduces snapBatch exactly.
    const good = new RegistryMachine(opts);
    good.expire(["a"], 0);
    good.flush(0);
    good.expire(["b"], 0);
    good.flush(5000);
    good.expire(["c"], 0);
    good.flush(5500);
    expect(good.snapBatch).toBe(live.snapBatch);

    // The pre-fix behavior — replaying every flush at nowMs 0 — silently forks snapBatch.
    const bad = new RegistryMachine(opts);
    bad.expire(["a"], 0);
    bad.flush(0);
    bad.expire(["b"], 0);
    bad.flush(0);
    bad.expire(["c"], 0);
    bad.flush(0);
    expect(bad.snapBatch).toBe(0);
    expect(bad.snapBatch).not.toBe(live.snapBatch);
  });

  // ── Snapshot SIZE gate ──────────────────────────────────────

  test("the SIZE gate SKIPS a due snapshot over the ceiling: telemetry emitted, NO persist-snapshot, snapBatch UNADVANCED (§4.2, CACHE-23)", () => {
    // Cadence due at seq 1 (every 1 batch), a tiny ceiling so the snap's ~65 shipped bytes blow
    // past it deterministically. This is the branch the MUTATION (drop the size check ⇒ always
    // emit persist-snapshot) must turn red: a persist-snapshot would appear and snapBatch advance.
    const m = new RegistryMachine({
      maxSnapshotBytes: 10,
      snapshotEveryBatches: 1,
    });
    const effects = runFlush(m, "expire", ["a"]); // seq 1 — cadence due, but oversize

    // The snapshot was SKIPPED: NO persist-snapshot, a telemetry effect in its place.
    expect(maybeSnapshotOf(effects)).toBeUndefined();
    const skip = skipOf(effects);
    expect(skip.event).toBe("snapshot-skipped-oversize");
    expect(skip.ceiling).toBe(10);
    // MEASURE-ON-CANONICAL-BYTES: the reported bytes equal the UTF-8 length of the EXACT
    // serialization the executor would ship (`JSON.stringify(frame)`), not an estimate — and it is
    // genuinely over the ceiling (65 > 10).
    const canonicalBytes = new TextEncoder().encode(
      '{"changes":[{"softEpoch":1,"tag":"a"}],"fromEpoch":0,"toEpoch":1}'
    ).length;
    expect(skip.bytes).toBe(canonicalBytes);
    // No phantom checkpoint blessed: #snapBatch did NOT advance.
    expect(m.snapBatch).toBe(0);
  });

  test("an oversize-skip flush still emits the batch + terminal pointer, in order, with the UNCHANGED snapBatch (§4.2, CACHE-23, D17)", () => {
    const m = new RegistryMachine({
      maxSnapshotBytes: 10,
      snapshotEveryBatches: 1,
    });
    const effects = runFlush(m, "expire", ["a"]); // seq 1 — skipped
    // The batch proceeds NORMALLY; the terminal pointer carries the UNCHANGED snapBatch (0).
    expect(persistOf(effects).seq).toBe(1);
    const { pointer } = advanceOf(effects);
    expect(pointer.lastBatch).toBe(1);
    expect(pointer.snapBatch).toBe(0);
    // Emission order: batch → telemetry → broadcast → advance-pointer (persist-snapshot ABSENT).
    expect(effects.map((e) => e.kind)).toStrictEqual([
      "persist-batch",
      "telemetry",
      "broadcast-frame",
      "advance-pointer",
    ]);
  });

  test("a within-ceiling snapshot is UNAFFECTED by the gate — persist-snapshot still emitted, snapBatch advances (§4.2, CACHE-23 non-vacuity)", () => {
    // The SAME cadence, a generous ceiling: the snapshot ships as before. Proves the gate is a
    // conditional skip, not a blanket suppression — a mutation that always skipped would red here.
    const m = new RegistryMachine({
      maxSnapshotBytes: 25 * 1024 * 1024,
      snapshotEveryBatches: 1,
    });
    const effects = runFlush(m, "expire", ["a"]); // seq 1 — cadence due, within ceiling
    expect(maybeSkipOf(effects)).toBeUndefined();
    expect(snapshotOf(effects).seq).toBe(1);
    expect(m.snapBatch).toBe(1);
  });

  test("the skip is LOUD: the batch clause stays tripped, so each subsequent flush RE-ATTEMPTS (§4.2, CACHE-23)", () => {
    // Because #snapBatch never advances, `seq − snapBatch ≥ everyBatches` stays true after the
    // first skip — every following oversize flush emits the telemetry again (a persistent alarm),
    // and the time clause does NOT double-fire (advancing #lastSnapMs). All under a journaled
    // ceiling, so it is deterministic.
    const m = new RegistryMachine({
      maxSnapshotBytes: 10,
      snapshotEveryBatches: 1,
    });
    expect(maybeSkipOf(runFlush(m, "expire", ["a"]))).toBeDefined(); // seq 1 — skip
    expect(maybeSkipOf(runFlush(m, "delete", ["b"]))).toBeDefined(); // seq 2 — re-attempt, skip
    expect(maybeSkipOf(runFlush(m, "expire", ["c"]))).toBeDefined(); // seq 3 — re-attempt, skip
    expect(m.snapBatch).toBe(0); // never advanced across the whole stall
  });

  test("replay identity across an oversize skip: a fresh machine with the SAME journaled ceiling reconstructs the stalled snapBatch (§4.2, CACHE-23, MI8)", () => {
    const opts = { maxSnapshotBytes: 10, snapshotEveryBatches: 1 } as const;
    // Live: two cadence-due flushes, both oversize ⇒ both skipped, snapBatch stalled at 0.
    const live = new RegistryMachine(opts);
    runFlush(live, "expire", ["a"]); // seq 1 — skipped
    runFlush(live, "delete", ["b"]); // seq 2 — skipped
    expect(live.snapBatch).toBe(0);

    // Replay the identical command script through a FRESH machine with the SAME journaled ceiling.
    // The skip is a pure function of the (journaled) ceiling + the canonical bytes, so the replay
    // reconstructs byte-identical state — snapBatch stalled at the same value, no phantom snapshot.
    const replay = new RegistryMachine(opts);
    replay.expire(["a"], 0);
    replay.flush(0);
    replay.delete(["b"], 0);
    const lastEffects = replay.flush(0);
    expect(replay.snapBatch).toBe(live.snapBatch); // 0 — stalled identically
    expect(replay.lastBatch).toBe(live.lastBatch); // 2
    expect(advanceOf(lastEffects).pointer.snapBatch).toBe(0);

    // Non-vacuity: with the DEFAULT (25 MiB) ceiling the same script would NOT stall — snapBatch
    // advances. The skip is genuinely the journaled ceiling's doing, not an artifact of the script.
    const unbounded = new RegistryMachine({ snapshotEveryBatches: 1 });
    runFlush(unbounded, "expire", ["a"]);
    expect(unbounded.snapBatch).toBe(1);
  });

  test("configure seeds maxSnapshotBytes into effectiveConfig; unset falls to the 25 MiB default (§4.2, CACHE-23, Ruling B)", () => {
    const m = new RegistryMachine();
    expect(m.effectiveConfig.maxSnapshotBytes).toBe(25 * 1024 * 1024);
    m.configure({ maxSnapshotBytes: 4096 });
    expect(m.effectiveConfig.maxSnapshotBytes).toBe(4096);
    // Prospective + isolated like the cadence knobs: a cadence-only reconfigure re-seeds the default.
    m.configure({ snapshotEveryBatches: 3 });
    expect(m.effectiveConfig.maxSnapshotBytes).toBe(25 * 1024 * 1024);
  });

  // ── configure: cadence as machine state (Ruling B) ───────────────────────────

  test("configure sets the effective cadence; unset knobs fall to the defaults (§4.2, Ruling B)", () => {
    const m = new RegistryMachine();
    expect(m.effectiveConfig).toStrictEqual({
      maxSnapshotBytes: 25 * 1024 * 1024,
      regId: "default",
      snapshotEveryBatches: 500,
      snapshotIntervalMs: 6 * 60 * 60 * 1000,
    });
    m.configure({ snapshotEveryBatches: 3 });
    // The set knob takes; the unset one falls back to the default (not left at the prior value).
    expect(m.effectiveConfig).toStrictEqual({
      maxSnapshotBytes: 25 * 1024 * 1024,
      regId: "default",
      snapshotEveryBatches: 3,
      snapshotIntervalMs: 6 * 60 * 60 * 1000,
    });
    m.configure({ snapshotIntervalMs: 1000 });
    expect(m.effectiveConfig).toStrictEqual({
      maxSnapshotBytes: 25 * 1024 * 1024,
      regId: "default",
      snapshotEveryBatches: 500,
      snapshotIntervalMs: 1000,
    });
  });

  test("a configured cadence governs the snapshot clause — identically to the constructor seed (§4.2)", () => {
    // configure is the JOURNALED path; the constructor seed is the mutation vector it replaces.
    // Both must yield the same emission, so a host that switches to configure sees no drift.
    const viaConfigure = new RegistryMachine();
    viaConfigure.configure({
      snapshotEveryBatches: 3,
      snapshotIntervalMs: Number.MAX_SAFE_INTEGER,
    });
    expect(
      maybeSnapshotOf(runFlush(viaConfigure, "expire", ["a"]))
    ).toBeUndefined(); // seq 1
    runFlush(viaConfigure, "delete", ["b"]); // seq 2
    const snap = snapshotOf(runFlush(viaConfigure, "expire", ["c"])); // seq 3 — crosses
    expect(snap.seq).toBe(3);
    expect(viaConfigure.snapBatch).toBe(3);
  });

  test("configure is PROSPECTIVE: shrinking the cadence never retro-snapshots earlier batches (§4.2, Ruling B)", () => {
    // Large cadence for the first flushes (no snapshot), then a small cadence going forward. A
    // retroactive application would have snapped at seq 2; a prospective one only snaps ahead.
    const m = new RegistryMachine({ snapshotEveryBatches: 10 });
    runFlush(m, "expire", ["a"]); // seq 1 — no snap (1 < 10)
    runFlush(m, "delete", ["b"]); // seq 2 — no snap (2 < 10)
    expect(m.snapBatch).toBe(0);
    m.configure({ snapshotEveryBatches: 2 }); // shrink, going forward only
    // seq 3: 3 − snapBatch(0) = 3 ≥ 2 ⇒ snapshot NOW (prospective). Earlier batches unaffected.
    const snap = snapshotOf(runFlush(m, "expire", ["c"]));
    expect(snap.seq).toBe(3);
    expect(m.snapBatch).toBe(3);
  });

  test("snapBatch is monotone non-decreasing across flushes AND across a cadence change (§4.2, Ruling B teeth)", () => {
    const m = new RegistryMachine({ snapshotEveryBatches: 2 });
    const seen: number[] = [];
    for (const [i, tag] of ["a", "b", "c", "d", "e"].entries()) {
      if (i === 2) {
        m.configure({ snapshotEveryBatches: 4 }); // widen mid-stream
      }
      runFlush(m, "expire", [tag]);
      seen.push(m.snapBatch);
    }
    // Never regresses, whatever the cadence did between flushes (the shared regression signature).
    for (let i = 1; i < seen.length; i += 1) {
      const prev = seen[i - 1] ?? 0;
      const cur = seen[i] ?? 0;
      expect(cur).toBeGreaterThanOrEqual(prev);
    }
  });

  test("lastBatch mirrors the durable batch seq for the host (§4.2)", () => {
    const m = new RegistryMachine();
    expect(m.lastBatch).toBe(0);
    runFlush(m, "expire", ["a"]);
    expect(m.lastBatch).toBe(1);
    runFlush(m, "delete", ["b"]);
    expect(m.lastBatch).toBe(2);
    expect(advanceOf(runFlush(m, "expire", ["c"])).pointer.lastBatch).toBe(3);
    expect(m.lastBatch).toBe(3);
  });

  test("property: snapBatch and lastBatch never regress across flushes (the shared regression signature)", () => {
    // A regressing cursor is a pointer blessing an object that may never have been written —
    // the failure mode BOTH findings share. Across any sequence of purges and flushes,
    // including flushes that cross the snapshot cadence and idempotent empty-window flushes,
    // neither cursor may ever go backwards.
    const step = fc.record({
      flush: fc.boolean(),
      op: fc.constantFrom("expire" as const, "delete" as const),
      tags: fc.uniqueArray(fc.constantFrom("a", "b", "c", "d"), {
        minLength: 1,
      }),
    });
    const holds = (): void => {
      fc.assert(
        fc.property(
          fc.array(step, { minLength: 1 }),
          fc.integer({ max: 4, min: 1 }),
          (window, snapshotEveryBatches) => {
            const m = new RegistryMachine({ snapshotEveryBatches });
            let snap = m.snapBatch;
            let last = m.lastBatch;
            for (const [i, s] of window.entries()) {
              if (s.op === "expire") {
                m.expire(s.tags, i);
              } else {
                m.delete(s.tags, i);
              }
              if (s.flush) {
                m.flush(i);
              }
              if (m.snapBatch < snap || m.lastBatch < last) {
                return false;
              }
              snap = m.snapBatch;
              last = m.lastBatch;
            }
            // A final drain, then an idempotent empty-window flush: neither may regress.
            m.flush(window.length);
            m.flush(window.length + 1);
            return m.snapBatch >= snap && m.lastBatch >= last;
          }
        )
      );
    };
    expect(holds).not.toThrow();
  });

  // ── regId scoping — machine state authored by configure ─────────────────

  test("configure seeds regId into effectiveConfig AND the blessed pointer echo (E19)", () => {
    const m = new RegistryMachine();
    expect(m.regId).toBe("default"); // the zero-config default
    m.configure({ regId: "tenant-x" });
    expect(m.regId).toBe("tenant-x");
    expect(m.effectiveConfig.regId).toBe("tenant-x");
    // The flush AND the heartbeat pointer echo the configured regId — the reader's belt against
    // a scheme/prefix divergence (a machine that dropped regId from the pointer breaks this).
    expect(advanceOf(runFlush(m, "expire", ["a"])).pointer.regId).toBe(
      "tenant-x"
    );
    expect(m.heartbeat(0).pointer.regId).toBe("tenant-x");
  });

  test("configure REJECTS a regId outside the E19 charset/length (validated at configure time)", () => {
    const m = new RegistryMachine();
    // ':' is the key delimiter, OUTSIDE [a-z0-9._-] — an id carrying one would make the scoped
    // key ambiguous, so configure throws rather than mint it (injective-by-construction).
    expect(() => {
      m.configure({ regId: "bad:id" });
    }).toThrow(/regId/u);
    expect(() => {
      m.configure({ regId: "A".repeat(65) });
    }).toThrow(/regId/u); // over the 64-char bound
    expect(() => {
      m.configure({ regId: "" });
    }).toThrow(/regId/u); // under the 1-char minimum
    // A well-formed id across the whole legal charset is accepted (non-vacuity).
    expect(() => {
      m.configure({ regId: "reg-1.default_x" });
    }).not.toThrow();
  });

  // ── Heartbeat — a state-neutral pointer refresh ────────────────────

  test("heartbeat emits the single terminal pointer effect, refreshing flushedAt ONLY (§4.2, E5)", () => {
    const m = new RegistryMachine();
    m.expire(["a"], 0);
    m.flush(1000); // seq 1, epoch 1, snapBatch 0 — a non-trivial baseline state
    const epochBefore = m.currentEpoch;
    const lastBefore = m.lastBatch;
    const snapBefore = m.snapBatch;

    const hb = m.heartbeat(9999);
    // Reuses the flush's terminal effect kind — one hot-key bless, not a fresh effect shape.
    expect(hb.kind).toBe("advance-pointer");
    // Every cursor is carried UNCHANGED; only the freshness field moves to nowMs. A mutation that
    // let the heartbeat advance a cursor (lastBatch/snapBatch/epoch) breaks this exact equality.
    expect(hb.pointer).toStrictEqual({
      epoch: epochBefore,
      flushedAt: 9999,
      lastBatch: lastBefore,
      regId: "default",
      snapBatch: snapBefore,
    });
  });

  test("heartbeat is state-neutral: the machine advances NOTHING (the no-journal precondition) (§4.2)", () => {
    const m = new RegistryMachine({ snapshotEveryBatches: 1 });
    runFlush(m, "expire", ["a"]); // seq 1 — the cadence fires, snapBatch → 1
    const before = {
      epoch: m.currentEpoch,
      last: m.lastBatch,
      snap: m.snapBatch,
    };
    // Many heartbeats at advancing wall times move no machine state at all.
    for (const t of [10, 20, 30, 40, 50]) {
      const hb = m.heartbeat(t);
      expect(hb.pointer.snapBatch).toBe(before.snap); // carries the blessed cursor…
    }
    // …and never advances it. This is what lets a host NOT journal a heartbeat: replaying it is a
    // no-op, so the durable journal is exact over the non-heartbeat commands alone.
    expect(m.currentEpoch).toBe(before.epoch);
    expect(m.lastBatch).toBe(before.last);
    expect(m.snapBatch).toBe(before.snap);
  });

  test("replay identity across heartbeats: a later flush is byte-identical with or without intervening heartbeats (§4.2, MI8)", () => {
    const build = (withHeartbeats: boolean): string => {
      const m = new RegistryMachine();
      m.expire(["b", "a"], 0);
      m.flush(0); // seq 1
      if (withHeartbeats) {
        // Heartbeats at arbitrary times between the two flushes.
        m.heartbeat(10);
        m.heartbeat(20);
        m.heartbeat(30);
      }
      m.delete(["a"], 0);
      return JSON.stringify(persistOf(m.flush(40)).frame);
    };
    // The machine-level face of "a host need not journal heartbeats; replay is exact without them":
    // heartbeats change nothing the next batch serializes.
    expect(build(true)).toBe(build(false));
  });
});
