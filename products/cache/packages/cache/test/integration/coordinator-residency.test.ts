// The Coordinator residency decision (§4.2, D16, E19) — the fix for C2-b-2-0.
//
// The bug: `CoordinatorFlushSink` chose snapshot-vs-delta KV residency by PARSING the key
// (`key.startsWith("__snap:")`). But E19 scoped EVERY mirror key to `__reg:<regId>:snap:<seq>` /
// `…:delta:<seq>`, so that prefix NEVER matched — every scoped snapshot silently took the 48h
// DELTA_TTL instead of D16's 7d SNAP_TTL. TTL is Miniflare-blind (D2/D14), so the transcript
// lane could not see it. The observable here is the residency the sink SELECTS (the `expirationTtl`
// it passes to its `Store`), NOT KV's enforcement — captured by a fake `Store` behind the REAL
// `CoordinatorFlushSink`, driven by the SAME `executeFlushEffects` the DO uses (thin-shell).
//
// This test is deterministic (no wall time, no KV round-trip); it lives in the integration lane
// only because it imports the real Coordinator sink, whose module resolves `cloudflare:workers`.
// It relies on none of the lane's timing tolerance (rule 12).

import type { Store, StoreValue, StoreWriteOptions } from "@astilba/cache";
import { executeFlushEffects, mirrorKeys, RegistryMachine } from "@astilba/cache/registry";
import { describe, expect, it } from "vitest";

import {
  CoordinatorFlushSink,
  DELTA_TTL_S,
  SNAP_TTL_S,
} from "../../src/adapters/cloudflare/coordinator";

/** A `Store` that captures each write's key and the SELECTED `expirationTtl` — the residency
 * decision the sink makes, before KV ever enforces it (Miniflare-blind, D2/D14). */
class CapturingResidencyStore implements Store {
  readonly writes: {
    readonly key: string;
    readonly expirationTtl: number | undefined;
  }[] = [];

  async get(): Promise<StoreValue | undefined> {
    return undefined;
  }

  async set(
    key: string,
    _value: string,
    options?: StoreWriteOptions
  ): Promise<void> {
    this.writes.push({ expirationTtl: options?.expirationTtl, key });
  }

  async delete(): Promise<void> {
    // Unused on the flush write path.
  }
}

describe("CoordinatorFlushSink — KV residency by effect kind, not key parse (§4.2, D16, E19)", () => {
  it("gives a scoped __reg:<id>:snap:<seq> put the 7d snapshot TTL and a delta the 48h TTL", async () => {
    const RID = "resid-probe";
    const KEYS = mirrorKeys(RID);
    // The scoped snapshot key never begins with `__snap:` — the exact reason the old
    // `startsWith("__snap:")` residency parse could NEVER match post-E19.
    expect(KEYS.snapKey(1).startsWith("__snap:")).toBe(false);

    // Cadence 1 ⇒ the single mutation's flush BOTH persists a delta batch and mints a snapshot.
    const machine = new RegistryMachine({ regId: RID, snapshotEveryBatches: 1 });
    machine.expire(["r"], 0);

    const store = new CapturingResidencyStore();
    // The broadcaster (C3-a) is irrelevant to residency; a no-op keeps this focused on the puts.
    const sink = new CoordinatorFlushSink(store, () => {
      // no live channel under test here
    });
    await executeFlushEffects(machine.flush(0), sink, machine.regId);

    const delta = store.writes.find((w) => w.key === KEYS.deltaKey(1));
    const snap = store.writes.find((w) => w.key === KEYS.snapKey(1));
    const state = store.writes.find((w) => w.key === KEYS.stateKey);

    // Non-vacuous: both immutable objects landed under their SCOPED keys, plus the pointer.
    expect(delta).toBeDefined();
    expect(snap).toBeDefined();
    expect(state).toBeDefined();

    // THE red-proof (reds on the pre-fix `startsWith` parse: scoped snap → 48h, mis-classified):
    // residency now rides the effect kind, so the snapshot gets its 7d floor.
    expect(snap?.expirationTtl).toBe(SNAP_TTL_S); // 7d — the D16 snapshot residency
    expect(delta?.expirationTtl).toBe(DELTA_TTL_S); // 48h — the delta residency
    // The hot `__state` pointer carries NO TTL (it never expires).
    expect(state?.expirationTtl).toBeUndefined();
  });
});
