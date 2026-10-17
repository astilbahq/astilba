// Test B — the rehydration transcript. The SAME command script is driven against
// BOTH hosts of the one pure registry machine — the harness `FakeRegistry` and the real
// Coordinator Durable Object — and their outputs are asserted BYTE-IDENTICAL: the command
// responses (epoch acks), and the content-addressed `__delta` batches (canonical
// form). This is the payoff of the thin-shell design: two hosts, one machine, identical
// bytes.
//
// The MANDATORY rehydration point (`evictDurableObject`) tears down the DO's in-memory
// state while preserving durable storage, then resumes and flushes via
// `runDurableObjectAlarm` (deterministic — not wall time). This is the shell bug the
// transcript exists to catch: a storage round-trip that corrupts the machine's rebuilt
// state would make the resumed batch diverge. If the two hosts' batches differed
// byte-for-byte, that would be a real finding (a monotonicity/determinism STOP, not a
// ledger entry — the machine is the shared truth).
//
// Lane law: the flush is fired deterministically, so this integration test
// carries no timing tolerance in its assertions.

import {
  evictDurableObject,
} from "cloudflare:test";
import { env } from "cloudflare:workers";

import type { Store, StoreValue } from "@astilba/cache";
import type { MirrorKeys } from "@astilba/cache/registry";
import { mirrorKeys, RegistryMachine } from "@astilba/cache/registry";
import { FakeRegistry, SimClock } from "@astilba/harness";

import { describe, expect, it } from "vitest";

import { fireAlarm } from "./registry-harness";

const A = "transcript:a";
const B = "transcript:b";
const C = "transcript:c";

// Both hosts default `regId:"default"` (no REGISTRY_ID wired in this lane's wrangler.jsonc),
// so the mirror keys are `__reg:default:…`. Address them through the same
// `mirrorKeys` the kernel writes with, so the transcript compares the real scoped bytes.
// Per-test isolation. Every DO in this lane shares ONE KV namespace, and
// the Workers pool's per-file isolated-storage frames are known to drop under concurrent DO
// requests (workers-sdk #11031) — so tests that all wrote the same `__reg:default:…` keys
// raced. A UNIQUE regId per test (the tool built) partitions the namespace into
// `__reg:<id>:…`, deterministically. The regId IS the DO name (identity = address),
// so `scope` writes NO shared global — the DO derives the SAME id from the test's own
// (already-unique) name (`idFromName(regId)`); the fake host is constructed with the SAME id so the
// byte-identity comparison still holds. `scope` just returns the matching key formatter.
function scope(regId: string): MirrorKeys {
  return mirrorKeys(regId);
}

type Command =
  | { readonly op: "expire"; readonly tags: readonly string[] }
  | { readonly op: "delete"; readonly tags: readonly string[] };

const SCRIPT: readonly Command[] = [
  { op: "expire", tags: [A] },
  { op: "delete", tags: [B] },
  { op: "expire", tags: [A, C] },
  { op: "delete", tags: [A, B] },
];

/** A minimal `Store` that captures every write byte-for-byte AND the ORDER of the keys. */
class CapturingStore implements Store {
  readonly writes = new Map<string, string>();
  /** The ordered key write-plan — the transcript-v2 oracle dimension (write COUNTS + order). */
  readonly writeLog: string[] = [];

  async get(key: string): Promise<StoreValue | undefined> {
    const value = this.writes.get(key);
    return value === undefined ? undefined : { value };
  }

  async set(key: string, value: string): Promise<void> {
    this.writeLog.push(key);
    this.writes.set(key, value);
  }

  async delete(key: string): Promise<void> {
    this.writes.delete(key);
  }
}

/** Drop the wall-time `flushedAt` term so the two hosts' pointers compare (sim vs Date.now). */
function pointerSansTime(json: string | null | undefined): unknown {
  if (json === undefined || json === null) {
    return undefined;
  }
  const parsed = JSON.parse(json) as Record<string, unknown>;
  const { flushedAt: _flushedAt, ...rest } = parsed;
  return rest;
}

/** The `__state` pointer's blessed `snapBatch`, or undefined if the pointer is absent. */
function pointerSnapBatch(json: string | null | undefined): number | undefined {
  if (json === undefined || json === null) {
    return undefined;
  }
  const parsed = JSON.parse(json) as Record<string, unknown>;
  return typeof parsed.snapBatch === "number" ? parsed.snapBatch : undefined;
}

describe("rehydration transcript — FakeRegistry and the real Coordinator emit identical bytes", () => {
  it("emits byte-identical per-flush __delta batches, acks, and pointers across both hosts", async () => {
    const RID = "transcript-parity";
    const KEYS = scope(RID);
    const fakeStore = new CapturingStore();
    const fake = new FakeRegistry(new SimClock(0), { regId: RID, store: fakeStore });

    const stub = env.COORDINATOR.get(env.COORDINATOR.idFromName(RID));
    const kv = env.REGISTRY_KV;

    let seq = 0;
    for (const cmd of SCRIPT) {
      // Command responses (epoch acks) — the fake auto-flushes (window 0); the DO
      // coalesces, so we fire its alarm to close the one-mutation window in lockstep.
      const fakeAck =
        cmd.op === "expire"
          ? await fake.expire(cmd.tags)
          : await fake.delete(cmd.tags);
      const doAck =
        cmd.op === "expire"
          ? await stub.expire(cmd.tags)
          : await stub.delete(cmd.tags);
      expect(doAck).toStrictEqual(fakeAck);

      await fireAlarm(stub);
      seq += 1;

      // The content-addressed batch: byte-identical JSON on both hosts.
      const fakeDelta = fakeStore.writes.get(KEYS.deltaKey(seq));
      const doDelta = await kv.get(KEYS.deltaKey(seq));
      expect(fakeDelta).toBeDefined();
      expect(doDelta).toBe(fakeDelta);

      // The `__state` pointer: identical modulo the wall-time `flushedAt` term (a
      // documented fake-vs-real divergence — D9; sim clock vs `Date.now()`).
      const doState = await kv.get(KEYS.stateKey);
      expect(pointerSansTime(doState)).toStrictEqual(
        pointerSansTime(fakeStore.writes.get(KEYS.stateKey))
      );
    }
  });

  it("resumes across an eviction and coalesces the open window into the reference batch", async () => {
    const RID = "transcript-rehydrate";
    const KEYS = scope(RID);
    const stub = env.COORDINATOR.get(env.COORDINATOR.idFromName(RID));
    const kv = env.REGISTRY_KV;

    // Fill the coalescing window with the whole script — NO flush yet (no alarm fired).
    for (const cmd of SCRIPT) {
      if (cmd.op === "expire") {
        await stub.expire(cmd.tags);
      } else {
        await stub.delete(cmd.tags);
      }
    }

    // MANDATORY rehydration: drop in-memory state, keep durable storage, then resume.
    await evictDurableObject(stub);

    // Fire the flush deterministically post-resume; the machine is rebuilt from the
    // durable journal and drains the SAME pending window `(0, epoch]`.
    await fireAlarm(stub);

    // Reference: the pure machine (the truth FakeRegistry also hosts) driven with the
    // same script + one flush. The coalesced DO batch must equal it byte-for-byte.
    const reference = new RegistryMachine();
    for (const cmd of SCRIPT) {
      if (cmd.op === "expire") {
        reference.expire(cmd.tags, 0);
      } else {
        reference.delete(cmd.tags, 0);
      }
    }
    const refEffects = reference.flush(0);
    const refBatch = refEffects.find((e) => e.kind === "persist-batch");
    expect(refBatch).toBeDefined();
    const refBytes = refBatch ? JSON.stringify(refBatch.frame) : undefined;

    const doBytes = await kv.get(KEYS.deltaKey(1));
    expect(doBytes).toBe(refBytes);

    // Rehydration preserved the watermarks: a strong read reflects the whole script.
    const knowledge = await stub.check([A, B, C]);
    const [a, b] = knowledge;
    expect(a?.known && a.hardEpoch > 0).toBe(true); // A was hard-deleted last
    expect(b?.known && b.hardEpoch > 0).toBe(true); // B was hard-deleted
  });

  // Must match `REGISTRY_SNAPSHOT_EVERY_BATCHES` in this lane's wrangler.jsonc: the DO reads
  // its cadence from that config var, the fake takes the same value via its constructor, so
  // both hosts trip the snapshot at the same seq. Crossing at seq 5 keeps the two cases above
  // (≤4 flushes) below the cadence, so their snapBatch stays 0 and they are unaffected.
  const EVERY_BATCHES = 5;

  // Five single-mutation flushes across both axes: at seq 5 the cadence fires on both hosts.
  const SNAP_SCRIPT: readonly Command[] = [
    { op: "expire", tags: [A] },
    { op: "delete", tags: [B] },
    { op: "expire", tags: [C] },
    { op: "delete", tags: [A] },
    { op: "expire", tags: [B, C] },
  ];

  it("crosses the snapshot cadence and emits byte-identical __snap bytes across both hosts (§4.2, F12)", async () => {
    const RID = "transcript-cadence";
    const KEYS = scope(RID);
    const fakeStore = new CapturingStore();
    const fake = new FakeRegistry(new SimClock(0), {
      regId: RID,
      snapshotEveryBatches: EVERY_BATCHES,
      store: fakeStore,
    });

    const stub = env.COORDINATOR.get(
      env.COORDINATOR.idFromName(RID)
    );
    const kv = env.REGISTRY_KV;

    let seq = 0;
    for (const cmd of SNAP_SCRIPT) {
      const fakeAck =
        cmd.op === "expire"
          ? await fake.expire(cmd.tags)
          : await fake.delete(cmd.tags);
      const doAck =
        cmd.op === "expire"
          ? await stub.expire(cmd.tags)
          : await stub.delete(cmd.tags);
      expect(doAck).toStrictEqual(fakeAck);

      await fireAlarm(stub);
      seq += 1;

      // The delta batch stays byte-identical every flush, as in the first case.
      const fakeDelta = fakeStore.writes.get(KEYS.deltaKey(seq));
      expect(await kv.get(KEYS.deltaKey(seq))).toBe(fakeDelta);

      const fakeSnap = fakeStore.writes.get(KEYS.snapKey(seq));
      const doSnap = await kv.get(KEYS.snapKey(seq));
      if (seq < EVERY_BATCHES) {
        // Below the cadence: neither host has minted a snapshot at this seq yet.
        expect(fakeSnap).toBeUndefined();
        expect(doSnap).toBeNull();
      } else {
        // At the cadence: BOTH hosts emit the __snap checkpoint, byte-for-byte identical —
        // the canonical batch over (0, epoch] (fromEpoch 0), one serializer, two hosts.
        expect(fakeSnap).toBeDefined();
        expect(doSnap).toBe(fakeSnap);
        expect(JSON.parse(fakeSnap ?? "null")).toMatchObject({
          fromEpoch: 0,
          toEpoch: seq, // one epoch per single-tag mutation ⇒ epoch === seq here
        });
      }
    }

    // The blessed pointer advanced snapBatch on both hosts (modulo the wall-time term).
    const doState = await kv.get(KEYS.stateKey);
    expect(pointerSansTime(doState)).toStrictEqual(
      pointerSansTime(fakeStore.writes.get(KEYS.stateKey))
    );
    expect(pointerSansTime(doState)).toMatchObject({ snapBatch: EVERY_BATCHES });
  });

  it("reproduces snapBatch across an eviction — the rehydrated machine keeps the blessed snapshot cursor (§4.2, MI8)", async () => {
    const RID = "transcript-rehydrate-snap";
    const KEYS = scope(RID);
    const fakeStore = new CapturingStore();
    const fake = new FakeRegistry(new SimClock(0), {
      regId: RID,
      snapshotEveryBatches: EVERY_BATCHES,
      store: fakeStore,
    });
    const stub = env.COORDINATOR.get(env.COORDINATOR.idFromName(RID));
    const kv = env.REGISTRY_KV;

    // Cross the cadence on BOTH hosts (snapshot minted at seq EVERY_BATCHES), no eviction yet.
    for (const cmd of SNAP_SCRIPT) {
      if (cmd.op === "expire") {
        await fake.expire(cmd.tags);
        await stub.expire(cmd.tags);
      } else {
        await fake.delete(cmd.tags);
        await stub.delete(cmd.tags);
      }
      await fireAlarm(stub);
    }
    expect(await kv.get(KEYS.snapKey(EVERY_BATCHES))).toBe(
      fakeStore.writes.get(KEYS.snapKey(EVERY_BATCHES))
    );

    // One more mutation, then EVICT before its flush. On resume the machine rebuilds from the
    // journal and must reconstruct snapBatch = EVERY_BATCHES (NOT reset to 0) — a machine that
    // reset it, or dropped the env cadence on reload, would wrongly re-snapshot at seq+1.
    await fake.expire([A]);
    await stub.expire([A]);
    await evictDurableObject(stub);
    await fireAlarm(stub);
    const seq = SNAP_SCRIPT.length + 1;

    // The next batch is byte-identical, neither host re-snapshots (seq − snapBatch < cadence),
    // and the blessed pointer still carries the pre-eviction snapBatch — matching the un-evicted
    // fake reference exactly.
    expect(await kv.get(KEYS.deltaKey(seq))).toBe(fakeStore.writes.get(KEYS.deltaKey(seq)));
    expect(await kv.get(KEYS.snapKey(seq))).toBeNull();
    expect(fakeStore.writes.get(KEYS.snapKey(seq))).toBeUndefined();
    expect(pointerSansTime(await kv.get(KEYS.stateKey))).toStrictEqual(
      pointerSansTime(fakeStore.writes.get(KEYS.stateKey))
    );
    // snapBatch never regresses across the rehydration.
    expect(pointerSnapBatch(await kv.get(KEYS.stateKey))).toBe(EVERY_BATCHES);
  });

  it("a changed cadence env var after first boot journals a fresh configure, applied PROSPECTIVELY (§4.2, Ruling B, MI8)", async () => {
    const RID = "transcript-config-prospective";
    const KEYS = scope(RID);
    const stub = env.COORDINATOR.get(env.COORDINATOR.idFromName(RID));
    const kv = env.REGISTRY_KV;

    // Cross the cadence under the DEPLOYED value (5); first boot journals `configure(5)`.
    for (const cmd of SNAP_SCRIPT) {
      if (cmd.op === "expire") {
        await stub.expire(cmd.tags);
      } else {
        await stub.delete(cmd.tags);
      }
      await fireAlarm(stub);
    }
    const snapBatchBefore = pointerSnapBatch(await kv.get(KEYS.stateKey));
    expect(snapBatchBefore).toBe(EVERY_BATCHES); // 5

    // Shrink the cadence to 2, then force a rehydration. Ruling B: `env` may only AUTHOR a fresh
    // journaled `configure`, applied PROSPECTIVELY at a definite journal position — never re-read
    // into the machine constructor (which would replay the WHOLE journal under cadence 2 and fork
    // snapBatch, blessing a phantom KEYS.snapKey(6)). The journal is the single durable truth.
    //
    // `wrangler types` narrows a `vars` entry to its LITERAL value (`"5"`), so a plain assignment
    // of `"2"` is a type error. `Reflect.set` writes through the runtime object without a cast
    //. The DO reads the same live `env` object — which is what makes the
    // constructor-re-read MUTATION observable (it would fork snapBatch to 6 here).
    const original = env.REGISTRY_SNAPSHOT_EVERY_BATCHES;
    Reflect.set(env, "REGISTRY_SNAPSHOT_EVERY_BATCHES", "2");
    try {
      await stub.expire([A]); // seq 6 — journalled, alarm armed
      await evictDurableObject(stub); // drop in-memory state; keep the journal
      await fireAlarm(stub);

      // NO retroactive fork: seq 6 does not cross the PROSPECTIVE cadence 2 (6 − 5 = 1 < 2), so
      // snapBatch holds at 5 and no KEYS.snapKey(6) exists. A machine rebuilt from env=2 would instead
      // have snapped during replay (seq 2/4/6) → snapBatch 6, a written KEYS.snapKey(6).
      const snapBatchAfter = pointerSnapBatch(await kv.get(KEYS.stateKey));
      expect(snapBatchAfter).toBe(EVERY_BATCHES); // 5, NOT 6
      expect(snapBatchAfter ?? 0).toBeGreaterThanOrEqual(snapBatchBefore ?? 0); // monotone across rehydration
      expect(await kv.get(KEYS.snapKey(6))).toBeNull();

      // PROSPECTIVE proof: seq 7 DOES cross the new cadence 2 (7 − 5 = 2 ≥ 2) — a snapshot the old
      // cadence 5 would never fire, so env=2 genuinely took effect going forward.
      await stub.expire([B]); // seq 7
      await fireAlarm(stub);
      const snapBatchSeven = pointerSnapBatch(await kv.get(KEYS.stateKey));
      expect(snapBatchSeven).toBe(7);
      expect(snapBatchSeven ?? 0).toBeGreaterThanOrEqual(snapBatchAfter ?? 0); // still monotone
      expect(await kv.get(KEYS.snapKey(7))).not.toBeNull();
    } finally {
      Reflect.set(env, "REGISTRY_SNAPSHOT_EVERY_BATCHES", original);
    }
  });

  // Transcript v2 — compare the ordered EFFECT PLAN, not just the final bytes. The
  // oracle-scope lesson made machinery: a double `__state` write is byte-identical under
  // last-write-wins, so a plan that records COUNTS + order is the only oracle that catches it.
  // This observes the LIVE DO's KV writes (a recording binding substituted on the shared `env`),
  // so a DO-side regression becomes a transcript failure — not an invisible production 429.
  it("transcript v2: the DO's flush write plan matches the fake's, one __state bless per flush (§4.2, D17)", async () => {
    const RID = "transcript-v2-plan";
    const KEYS = scope(RID);
    const fakeStore = new CapturingStore();
    const fake = new FakeRegistry(new SimClock(0), {
      regId: RID,
      snapshotEveryBatches: EVERY_BATCHES,
      store: fakeStore,
    });
    const stub = env.COORDINATOR.get(env.COORDINATOR.idFromName(RID));

    // A recording KV binding: log every put's KEY, delegate the write to the real namespace so
    // the DO still functions. `new Proxy<KVNamespace>(target, …)` is typed by the target — no
    // cast. Substituted on the LIVE `env` the DO shares (the same mechanism the
    // config test's mutation relies on), and restored in `finally`.
    const realKv = env.REGISTRY_KV;
    const doPlan: string[] = [];
    const recordingKv: KVNamespace = new Proxy(realKv, {
      get(target, property, receiver) {
        if (property === "put") {
          return (
            key: string,
            value: string,
            options?: KVNamespacePutOptions
          ): Promise<void> => {
            doPlan.push(key);
            return target.put(key, value, options);
          };
        }
        return Reflect.get(target, property, receiver);
      },
    });

    Reflect.set(env, "REGISTRY_KV", recordingKv);
    expect(env.REGISTRY_KV).toBe(recordingKv); // the swap took (env is mutable, DO shares it)
    try {
      for (const cmd of SNAP_SCRIPT) {
        if (cmd.op === "expire") {
          await fake.expire(cmd.tags);
        } else {
          await fake.delete(cmd.tags);
        }
        if (cmd.op === "expire") {
          await stub.expire(cmd.tags);
        } else {
          await stub.delete(cmd.tags);
        }
        await fireAlarm(stub);
      }
    } finally {
      Reflect.set(env, "REGISTRY_KV", realKv);
    }

    // The two hosts wrote the SAME ordered plan: [__delta:n, (…__snap:5), __state] × 5 flushes.
    expect(doPlan).toStrictEqual(fakeStore.writeLog);

    // Structural once-only, asserted on the DO plan DIRECTLY (independent of the fake): exactly
    // one `__state` bless per flush, never two in a row (the double-write a 429 would answer),
    // and the crossing flush writes `__snap:5` before its `__state`.
    const stateWrites = doPlan.filter((k) => k === KEYS.stateKey);
    expect(stateWrites).toHaveLength(SNAP_SCRIPT.length);
    for (let i = 1; i < doPlan.length; i += 1) {
      const adjacentBless = doPlan[i] === KEYS.stateKey && doPlan[i - 1] === KEYS.stateKey;
      expect(adjacentBless).toBe(false);
    }
    expect(doPlan.indexOf(KEYS.snapKey(5))).toBeLessThan(doPlan.lastIndexOf(KEYS.stateKey));
  });

  // The snapshot SIZE gate: the skip is MACHINE-decided, so both hosts skip the
  // SAME cadence-due-but-oversize snapshot. The DO takes its ceiling from a config var (via the
  // journaled `configure` channel, Ruling B), the fake from its constructor — both tiny, so the
  // seq-5 snapshot blows past it and neither host writes a `__snap`. `REGISTRY_MAX_SNAPSHOT_BYTES`
  // is NOT in this lane's wrangler vars, so it is set on the LIVE `env` the DO shares (the same
  // mechanism the config-prospective test uses) and torn down in `finally`.
  it("both hosts SKIP an oversize snapshot identically: no __snap written, snapBatch unadvanced, byte-identical (§4.2, CACHE-23)", async () => {
    const RID = "transcript-oversize-skip";
    const KEYS = scope(RID);
    const fakeStore = new CapturingStore();
    const fake = new FakeRegistry(new SimClock(0), {
      maxSnapshotBytes: 10,
      regId: RID,
      snapshotEveryBatches: EVERY_BATCHES,
      store: fakeStore,
    });
    const stub = env.COORDINATOR.get(env.COORDINATOR.idFromName(RID));
    const kv = env.REGISTRY_KV;

    const hadMax = Reflect.has(env, "REGISTRY_MAX_SNAPSHOT_BYTES");
    const originalMax = Reflect.get(env, "REGISTRY_MAX_SNAPSHOT_BYTES");
    Reflect.set(env, "REGISTRY_MAX_SNAPSHOT_BYTES", "10");
    try {
      let seq = 0;
      for (const cmd of SNAP_SCRIPT) {
        const fakeAck =
          cmd.op === "expire"
            ? await fake.expire(cmd.tags)
            : await fake.delete(cmd.tags);
        const doAck =
          cmd.op === "expire"
            ? await stub.expire(cmd.tags)
            : await stub.delete(cmd.tags);
        expect(doAck).toStrictEqual(fakeAck);
        await fireAlarm(stub);
        seq += 1;

        // The delta batch stays byte-identical every flush — the skip never touches the delta path.
        const fakeDelta = fakeStore.writes.get(KEYS.deltaKey(seq));
        expect(fakeDelta).toBeDefined();
        expect(await kv.get(KEYS.deltaKey(seq))).toBe(fakeDelta);

        // At the cadence seq the snapshot is DUE but OVERSIZE ⇒ SKIPPED on BOTH hosts: no __snap
        // object is written by either, at any seq (crossing at 5, but never below it either).
        expect(await kv.get(KEYS.snapKey(seq))).toBeNull();
        expect(fakeStore.writes.get(KEYS.snapKey(seq))).toBeUndefined();
      }

      // The blessed pointer never advanced snapBatch (stalled at 0), identical across hosts modulo
      // the wall-time `flushedAt` term (the sim-vs-Date.now divergence).
      const doState = await kv.get(KEYS.stateKey);
      expect(pointerSansTime(doState)).toStrictEqual(
        pointerSansTime(fakeStore.writes.get(KEYS.stateKey))
      );
      expect(pointerSnapBatch(doState)).toBe(0);

      // The fake host recorded the skip on its telemetry channel; the DO forwards the same event to
      // the platform log (its observable twin — thin-shell: one executor routes both identically).
      expect(fake.telemetryEvents).toContainEqual(
        expect.objectContaining({ event: "snapshot-skipped-oversize" })
      );
    } finally {
      if (hadMax) {
        Reflect.set(env, "REGISTRY_MAX_SNAPSHOT_BYTES", originalMax);
      } else {
        Reflect.deleteProperty(env, "REGISTRY_MAX_SNAPSHOT_BYTES");
      }
    }
  });
});
