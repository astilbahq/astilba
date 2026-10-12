/* References: E5. */




















import {
  evictDurableObject,
  runDurableObjectAlarm,
  runInDurableObject,
} from "cloudflare:test";
import { env } from "cloudflare:workers";

import type { MirrorKeys } from "@astilba/cache/registry";
import { mirrorKeys, RegistryMachine } from "@astilba/cache/registry";
import { describe, expect, it } from "vitest";

const A = "hb:a";
const B = "hb:b";

// Per-test isolation (CACHE-29): every DO in this lane shares ONE KV namespace, and pool-workers'
// per-file isolated-storage frames drop under concurrent DO requests (workers-sdk #11031), so
// tests all writing `__reg:default:…` raced. A UNIQUE regId per test (the tool E19 built)
// partitions the namespace deterministically. Under E19-b the regId IS the DO name (identity =
// address), so `scope` writes NO shared global — the DO derives the SAME id from the test's own
// (already-unique) name (`idFromName(regId)`); `scope` just returns the matching key formatter.
function scope(regId: string): MirrorKeys {
  return mirrorKeys(regId);
}
/** Whether a recorded put key is a `delta` batch key (scheme-agnostic — matches the scoped form). */
const isDeltaKey = (key: string): boolean => key.includes(":delta:");

/** A 60s cadence — far above the ~1.1s flush window, so the two deadlines are unambiguous. */
const HEARTBEAT_MS = 60_000;
/** Must match `REGISTRY_SNAPSHOT_EVERY_BATCHES` in this lane's wrangler.jsonc (the DO reads it). */
const EVERY_BATCHES = 5;

/** A KV binding that records the KEY of every `put`, delegating the write to the real namespace. */
function makeRecorder(realKv: KVNamespace): {
  readonly recordingKv: KVNamespace;
  readonly plan: string[];
} {
  const plan: string[] = [];
  const recordingKv: KVNamespace = new Proxy(realKv, {
    get(target, property, receiver) {
      if (property === "put") {
        return (
          key: string,
          value: string,
          options?: KVNamespacePutOptions
        ): Promise<void> => {
          plan.push(key);
          return target.put(key, value, options);
        };
      }
      return Reflect.get(target, property, receiver);
    },
  });
  return { plan, recordingKv };
}

/** The `__state` pointer's numeric fields, or NaN where absent (avoids an `any` from JSON.parse). */
function pointerFields(json: string | null): {
  epoch: number;
  flushedAt: number;
  lastBatch: number;
  snapBatch: number;
} {
  const parsed = JSON.parse(json ?? "null") as Record<string, unknown>;
  const num = (value: unknown): number =>
    typeof value === "number" ? value : Number.NaN;
  return {
    epoch: num(parsed.epoch),
    flushedAt: num(parsed.flushedAt),
    lastBatch: num(parsed.lastBatch),
    snapBatch: num(parsed.snapBatch),
  };
}

/** The DO's currently-armed alarm deadline (or null), read from inside the object. */
async function armedAlarm(
  stub: DurableObjectStub<import("./worker").Coordinator>
): Promise<number | null> {
  return await runInDurableObject(stub, (_instance, state) =>
    state.storage.getAlarm()
  );
}

/** The DO's durable `__journal` length (−1 if absent), read from inside the object. */
async function journalLength(
  stub: DurableObjectStub<import("./worker").Coordinator>
): Promise<number> {
  return await runInDurableObject(stub, async (_instance, state) => {
    const journal = await state.storage.get("__journal");
    return Array.isArray(journal) ? journal.length : -1;
  });
}

describe("E5 heartbeat — the sound liveness path (C2-b-1b)", () => {
  it("a flush alarm with the heartbeat configured blesses __state EXACTLY once — never flush+heartbeat (D17)", async () => {
    const RID = "hb-flush-plus";
    const KEYS = scope(RID);
    const stub = env.COORDINATOR.get(env.COORDINATOR.idFromName(RID));
    const realKv = env.REGISTRY_KV;
    const { plan, recordingKv } = makeRecorder(realKv);

    Reflect.set(env, "REGISTRY_HEARTBEAT_MS", String(HEARTBEAT_MS));
    Reflect.set(env, "REGISTRY_KV", recordingKv);
    try {
      await stub.expire([A]); // opens the window (journal writes hit SQLite, not KV)
      plan.length = 0; // measure ONLY the flush alarm's KV write plan
      expect(await runDurableObjectAlarm(stub)).toBe(true);
    } finally {
      Reflect.set(env, "REGISTRY_KV", realKv);
      Reflect.deleteProperty(env, "REGISTRY_HEARTBEAT_MS");
    }

    // The flush alarm's WHOLE plan: the immutable batch, then ONE terminal __state bless. The old
    // post-flush heartbeat would append a SECOND __state here — the D17 double-write real KV 429s.
    expect(plan).toStrictEqual([KEYS.deltaKey(1), KEYS.stateKey]);
    expect(plan.filter((key) => key === KEYS.stateKey)).toHaveLength(1);
  });

  it("an idle heartbeat alarm blesses __state EXACTLY once and advances NO cursor (state-neutral)", async () => {
    const RID = "hb-idle";
    const KEYS = scope(RID);
    const stub = env.COORDINATOR.get(env.COORDINATOR.idFromName(RID));
    const realKv = env.REGISTRY_KV;
    const { plan, recordingKv } = makeRecorder(realKv);

    Reflect.set(env, "REGISTRY_HEARTBEAT_MS", String(HEARTBEAT_MS));
    Reflect.set(env, "REGISTRY_KV", recordingKv);
    try {
      // A purge + flush blesses __state and re-arms the heartbeat cadence.
      await stub.expire([A]);
      expect(await runDurableObjectAlarm(stub)).toBe(true);
      const afterFlush = pointerFields(await realKv.get(KEYS.stateKey));
      plan.length = 0; // measure ONLY the next (idle heartbeat) alarm

      // The re-armed heartbeat alarm now fires with an EMPTY window — a pure liveness refresh.
      expect(await runDurableObjectAlarm(stub)).toBe(true);

      // The idle alarm's whole plan: exactly one __state bless, no immutable object (no __delta —
      // the heartbeat minted no batch). A DOUBLED heartbeat plan would show two __state here…
      expect(plan).toStrictEqual([KEYS.stateKey]);
      expect(plan.filter((key) => isDeltaKey(key))).toHaveLength(0);

      // …yet the byte-identity below (cursors unchanged, only flushedAt moved) would STILL pass
      // under that double — last-write-wins erases the second put. The plan COUNT above is the only
      // oracle that catches it; these fields are the "byte-identity stays green" control.
      const afterHb = pointerFields(await realKv.get(KEYS.stateKey));
      expect(afterHb.epoch).toBe(afterFlush.epoch);
      expect(afterHb.lastBatch).toBe(afterFlush.lastBatch);
      expect(afterHb.snapBatch).toBe(afterFlush.snapBatch);
      expect(afterHb.flushedAt).toBeGreaterThanOrEqual(afterFlush.flushedAt);
    } finally {
      Reflect.set(env, "REGISTRY_KV", realKv);
      Reflect.deleteProperty(env, "REGISTRY_HEARTBEAT_MS");
    }
  });

  it("earliest-wins arbitration: a pending heartbeat never starves a later purge's flush; the flush re-arms the heartbeat after (D11)", async () => {
    const RID = "hb-arbitration";
    scope(RID);
    const stub = env.COORDINATOR.get(env.COORDINATOR.idFromName(RID));

    Reflect.set(env, "REGISTRY_HEARTBEAT_MS", String(HEARTBEAT_MS));
    try {
      // A purge + flush leaves the E5 heartbeat cadence armed ~HEARTBEAT_MS out: the two deadlines
      // compete for the one alarm slot, the nearer one takes it, and whichever lost re-arms once the
      // winner has fired. Here the flush won this tick and the heartbeat re-armed behind it.
      await stub.expire([A]);
      expect(await runDurableObjectAlarm(stub)).toBe(true);
      const armedAfterFlush = await armedAlarm(stub);
      expect(armedAfterFlush).not.toBeNull();
      // Far out — clearly the heartbeat cadence, not a flush deadline. (Removing the flush path's
      // re-arm would leave this null.)
      expect(armedAfterFlush ?? 0).toBeGreaterThan(Date.now() + HEARTBEAT_MS / 2);

      // A fresh purge must move the alarm EARLIER to its ~1.1s B5 flush deadline, NOT wait the
      // heartbeat interval. The old `getAlarm() !== null ⇒ return` guard (last-writer-wins) would
      // leave it ~HEARTBEAT_MS out and starve the flush.
      await stub.expire([B]);
      const armedAfterPurge = await armedAlarm(stub);
      expect(armedAfterPurge).not.toBeNull();
      expect(armedAfterPurge ?? 0).toBeLessThan(Date.now() + HEARTBEAT_MS / 2);
    } finally {
      Reflect.deleteProperty(env, "REGISTRY_HEARTBEAT_MS");
    }
  });

  it("heartbeats do not grow the journal, and replay across them is exact (journal-growth guard; D10, MI8)", async () => {
    const RID = "hb-journal-bound";
    const KEYS = scope(RID);
    const stub = env.COORDINATOR.get(env.COORDINATOR.idFromName(RID));
    const realKv = env.REGISTRY_KV;

    Reflect.set(env, "REGISTRY_HEARTBEAT_MS", String(HEARTBEAT_MS));
    try {
      // Two real command cycles (expire + flush), each appending real journal entries.
      const CYCLES = 2;
      for (let i = 0; i < CYCLES; i += 1) {
        await stub.expire([A]);
        expect(await runDurableObjectAlarm(stub)).toBe(true);
      }
      const lengthBefore = await journalLength(stub);
      expect(lengthBefore).toBeGreaterThan(0);

      // Fire MANY idle heartbeat alarms — each re-arms the cadence, so each subsequent fire runs.
      const HEARTBEATS = 20;
      for (let i = 0; i < HEARTBEATS; i += 1) {
        expect(await runDurableObjectAlarm(stub)).toBe(true);
      }
      const lengthAfter = await journalLength(stub);

      // The journal grew by ZERO across 20 heartbeats (not by 20) — a state-neutral command is not
      // journaled, so an idle registry never rewrites its journal toward the DO 2 MB per-value cap.
      expect(lengthAfter).toBe(lengthBefore);

      // Replay identity: one more mutation, EVICT (drop in-memory state), then flush post-resume.
      // The rehydrated machine replays a journal that recorded ZERO heartbeats and must mint the
      // reference batch byte-for-byte — the heartbeats corrupted nothing.
      await stub.expire([B]);
      await evictDurableObject(stub);
      expect(await runDurableObjectAlarm(stub)).toBe(true);

      const reference = new RegistryMachine({
        snapshotEveryBatches: EVERY_BATCHES,
      });
      reference.expire([A], 0);
      reference.flush(0); // seq 1
      reference.expire([A], 0);
      reference.flush(0); // seq 2
      reference.expire([B], 0);
      const refBatch = reference
        .flush(0)
        .find((effect) => effect.kind === "persist-batch");
      const refBytes = refBatch ? JSON.stringify(refBatch.frame) : undefined;

      expect(await realKv.get(KEYS.deltaKey(3))).toBe(refBytes);
      // Watermarks survived the heartbeats + eviction: a strong read reflects every real purge.
      const [a, b] = await stub.check([A, B]);
      expect(a?.known && a.softEpoch > 0).toBe(true);
      expect(b?.known && b.softEpoch > 0).toBe(true);
    } finally {
      Reflect.deleteProperty(env, "REGISTRY_HEARTBEAT_MS");
    }
  });
});
