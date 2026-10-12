import type { Store, StoreValue } from "@astilba/cache";
import { mirrorKeys } from "@astilba/cache/registry";
import { describe, expect, test } from "vitest";

import { SimClock } from "./clock";
import { FakeRegistry } from "./registry";

// Harness self-tests (P1) — FakeRegistry watermark monotonicity: max-on-write,
// soft never regresses hard, monotone epoch counter.

/** The default-scoped physical mirror keys (§4.2, E19) the FakeRegistry writes — a default
 * FakeRegistry seeds `regId:"default"`, so its keys are `__reg:default:…`. */
const KEYS = mirrorKeys("default");
/** The single hot pointer key (§4.2) — blessed at most once per flush. */
const STATE_KEY = KEYS.stateKey;
/** Whether a logged key is a `delta` batch key (scheme-agnostic — matches the scoped form). */
const isDeltaKey = (key: string): boolean => key.includes(":delta:");

/** A `Store` that records the ORDER of every key written — for the write-then-bless proofs. */
class WriteLogStore implements Store {
  readonly log: string[] = [];
  readonly values = new Map<string, string>();

  async get(key: string): Promise<StoreValue | undefined> {
    const value = this.values.get(key);
    return value === undefined ? undefined : { value };
  }

  async set(key: string, value: string): Promise<void> {
    this.log.push(key);
    this.values.set(key, value);
  }

  async delete(key: string): Promise<void> {
    this.values.delete(key);
  }
}

describe(FakeRegistry, () => {
  test("epoch increments monotonically per mutation", async () => {
    const registry = new FakeRegistry(new SimClock());
    expect(registry.currentEpoch).toBe(0);
    const expired = await registry.expire(["t"]);
    expect(expired.epoch).toBe(1);
    const deleted = await registry.delete(["t"]);
    expect(deleted.epoch).toBe(2);
    expect(registry.currentEpoch).toBe(2);
  });

  test("soft never regresses hard; watermarks are max-on-write", async () => {
    const registry = new FakeRegistry(new SimClock());
    await registry.delete(["t"]); // hardEpoch = 1
    expect(registry.watermark("t")).toStrictEqual({
      hardEpoch: 1,
      softEpoch: 0,
    });
    await registry.expire(["t"]); // softEpoch = 2, hard unchanged
    expect(registry.watermark("t")).toStrictEqual({
      hardEpoch: 1,
      softEpoch: 2,
    });
  });

  test("repeated soft purges only raise the watermark", async () => {
    const registry = new FakeRegistry(new SimClock());
    await registry.expire(["t"]);
    await registry.expire(["t"]);
    expect(registry.watermark("t").softEpoch).toBe(2);
  });

  test("check reports known knowledge for every requested tag", async () => {
    const registry = new FakeRegistry(new SimClock());
    await registry.expire(["a"]);
    const knowledge = await registry.check(["a", "b"]);
    expect(knowledge[0]).toStrictEqual({
      hardEpoch: 0,
      known: true,
      softEpoch: 1,
      throughEpoch: 1,
    });
    expect(knowledge[1]).toStrictEqual({
      hardEpoch: 0,
      known: true,
      softEpoch: 0,
      throughEpoch: 1,
    });
  });

  test("a snapshot-crossing flush blesses __state EXACTLY ONCE, after __snap (write-then-bless; KV 1-write/s/key)", async () => {
    // snapshotEveryBatches: 3 ⇒ the third flush both writes __delta:3 and mints __snap:3.
    const store = new WriteLogStore();
    const registry = new FakeRegistry(new SimClock(), {
      snapshotEveryBatches: 3,
      store,
    });
    await registry.expire(["a"]); // seq 1
    await registry.delete(["b"]); // seq 2
    await registry.expire(["c"]); // seq 3 — crosses the cadence
    // ONE __state write per flush — three flushes, three blessings. Two puts to the single
    // hot key in one flush would 429 on real KV (docs: 1 write/s/key); the count catches it.
    const stateWrites = store.log.filter((key) => key === STATE_KEY);
    expect(stateWrites).toHaveLength(3);
    // Write-then-bless on the crossing flush: delta:3 → snap:3 → the blessing state pointer.
    const deltaIdx = store.log.indexOf(KEYS.deltaKey(3));
    const snapIdx = store.log.indexOf(KEYS.snapKey(3));
    const lastStateIdx = store.log.lastIndexOf(STATE_KEY);
    expect(deltaIdx).toBeLessThan(snapIdx);
    expect(snapIdx).toBeLessThan(lastStateIdx);
  });

  test("records each immutable write's KIND — delta vs snap — the fake's observable twin of the Coordinator's kind→residency policy (§4.2, D16, E19)", async () => {
    // The Sim store carries no TTL, so the fake mirrors the residency DECISION as the recorded
    // `kind`. A snapshot-crossing flush must record a `"snap"` write (the kind the real Coordinator
    // maps to the 7d TTL) alongside the `"delta"`s. snapshotEveryBatches: 2 ⇒ the 2nd flush crosses.
    // No store is wired — the kind is recorded regardless (it is what a residency-blind fake can twin).
    const registry = new FakeRegistry(new SimClock(), {
      snapshotEveryBatches: 2,
    });
    await registry.expire(["a"]); // seq 1 — delta only
    await registry.delete(["b"]); // seq 2 — delta + snapshot (cadence crossed)
    expect(registry.immutableWrites).toStrictEqual([
      { key: KEYS.deltaKey(1), kind: "delta" },
      { key: KEYS.deltaKey(2), kind: "delta" },
      { key: KEYS.snapKey(2), kind: "snap" },
    ]);
  });

  test("an OVERSIZE snapshot is SKIPPED on the fake host: no __snap written, snapBatch unadvanced, telemetry recorded (§4.2, CACHE-23)", async () => {
    // snapshotEveryBatches: 1 makes the cadence due every flush; a tiny ceiling makes the snap
    // oversize, so the machine SKIPS it. The fake host must write NO __snap object and record the
    // skip on its telemetry channel (the observable twin of the DO's platform forward).
    const store = new WriteLogStore();
    const registry = new FakeRegistry(new SimClock(), {
      maxSnapshotBytes: 10,
      snapshotEveryBatches: 1,
      store,
    });
    await registry.expire(["a"]); // seq 1 — cadence due, but oversize ⇒ skipped
    // The delta landed; the __snap object never did.
    expect(store.log).toContain(KEYS.deltaKey(1));
    expect(store.log).not.toContain(KEYS.snapKey(1));
    // Still exactly one __state bless (the skip does not disturb the once-only pointer contract).
    expect(store.log.filter((key) => key === STATE_KEY)).toHaveLength(1);
    // The blessed pointer never advanced snapBatch (read from the written bytes — a half-skip that
    // advanced snapBatch would carry `"snapBatch":1` here, referencing a __snap that was never put).
    expect(store.values.get(STATE_KEY)).toContain('"snapBatch":0');
    // The skip was recorded on the telemetry channel (the DO forwards the same event to the platform).
    expect(registry.telemetryEvents).toContainEqual(
      expect.objectContaining({
        ceiling: 10,
        event: "snapshot-skipped-oversize",
      })
    );
  });

  test("heartbeat blesses __state EXACTLY once and advances no cursor (E5 twin; §4.2, D17)", async () => {
    // The fake has no alarms, so it mirrors only the meaningful part: the same one-put, cursor-
    // untouched pointer refresh the real Coordinator issues on an idle alarm, through the SAME
    // executor + sink. A single flush first, then a heartbeat. The SimClock stays at 0, so the
    // flush and heartbeat pointers share `flushedAt: 0` — a byte-identical re-bless proves the
    // heartbeat moved NO cursor, no field-by-field parse needed.
    const store = new WriteLogStore();
    const registry = new FakeRegistry(new SimClock(), { store });
    await registry.expire(["a"]); // seq 1 — blesses __state once
    const stateAfterFlush = store.values.get(STATE_KEY);
    await registry.heartbeat();
    // Exactly two __state writes total: one per flush, one per heartbeat — never two in one op.
    expect(store.log.filter((key) => key === STATE_KEY)).toHaveLength(2);
    // No immutable object accompanies the heartbeat (still just the one delta from the flush).
    expect(store.log.filter(isDeltaKey)).toHaveLength(1);
    // State-neutral: the re-blessed pointer is byte-identical, and the registry's own epoch held.
    expect(store.values.get(STATE_KEY)).toBe(stateAfterFlush);
    expect(registry.currentEpoch).toBe(1);
  });

  test("registered retention is tracked as a monotone high-water (E11)", async () => {
    const registry = new FakeRegistry(new SimClock());
    await registry.registerRetention(1000);
    await registry.registerRetention(5000);
    await registry.registerRetention(2000);
    expect(registry.maxRegisteredRetentionMs).toBe(5000);
  });

  test("check round-trip lag: resolves only after the world advances past it, with authoritative knowledge", async () => {
    const clock = new SimClock();
    const registry = new FakeRegistry(clock);
    registry.setCheckLag(5000);
    await registry.expire(["t"]); // soft epoch 1 before the check is issued
    let settled = false;
    const pending = registry.check(["t"]).then((knowledge) => {
      settled = true;
      return knowledge;
    });
    await clock.runMicrotasks();
    expect(settled).toBeFalsy(); // still in flight — the DO round-trip has not elapsed
    // A hard delete lands DURING the round-trip; the resolved knowledge must reflect it
    // (primary reads are authoritative — §4.4 — only their completion is delayed).
    await registry.delete(["t"]); // hard epoch 2
    clock.advance(5000);
    registry.pumpChecks(clock.now());
    const knowledge = await pending;
    expect(settled).toBeTruthy();
    expect(knowledge[0]).toStrictEqual({
      hardEpoch: 2,
      known: true,
      softEpoch: 1,
      throughEpoch: 2,
    });
  });
});
