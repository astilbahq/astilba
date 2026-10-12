import { setImmediate } from "node:timers/promises";

import type { Rng, Store, StoreValue } from "@astilba/cache";
import { mirrorKeys } from "@astilba/cache/registry";
import type { MirrorKeys, StatePointer } from "@astilba/cache/registry";
import { describe, expect, it } from "vitest";

import type {
  ResyncPolicy,
  ResyncStatus,
  SnapshotPolicy,
} from "../../src/invalidation";
import { ReplicationPoller } from "../../src/replication-poller";
import type { ReplicationReader } from "../../src/replication-poller";
import { buildReaderParts } from "../conformance/fixtures/reader-parts";

/* References: §4.2, E20, E21, E5. */

const REG_ID = "poller-unit";
const KEYS: MirrorKeys = mirrorKeys(REG_ID);

/** A fixed-value rng — with `jitterFraction: 0` the poller ignores it, so cadences are exact. */
const fixedRng = (value = 0.5): Rng => ({ next: (): number => value });

/** The `transient-miss` retry signal (an object, not a string — the poller schedules the next
 * attempt off it). `seq` is inert to the shell; it only steers the kernel's own delta cursor. */
const transientMiss: ResyncStatus = { kind: "transient-miss", seq: 0 };

/** A Map-backed store; the poller only reads `stateKey` for the E5 observation. `reads` counts
 * `get`s so a test can prove the +25 baseline observation keeps firing while terminal. */
class MapStore implements Store {
  readonly data = new Map<string, string>();
  reads = 0;

  async get(key: string): Promise<StoreValue | undefined> {
    this.reads += 1;
    const value = this.data.get(key);
    return value === undefined ? undefined : { value };
  }

  async set(key: string, value: string): Promise<void> {
    this.data.set(key, value);
  }

  async delete(key: string): Promise<void> {
    this.data.delete(key);
  }
}

/** A scriptable reader double: `suspect` is caller-controlled, and every resync/snapshot verdict
 * comes from an injected script keyed by the running call count. Records call counts so a test can
 * prove the ladder stopped (or kept spinning). */
class ScriptedReader implements ReplicationReader {
  suspect: boolean;
  resyncCalls = 0;
  snapshotCalls = 0;

  constructor(
    suspect: boolean,
    private readonly onResync: (
      policy: ResyncPolicy | undefined,
      call: number
    ) => ResyncStatus,
    private readonly onSnapshot: (
      policy: SnapshotPolicy | undefined,
      call: number
    ) => ResyncStatus = () => "terminal-gap"
  ) {
    this.suspect = suspect;
  }

  async resync(policy?: ResyncPolicy): Promise<ResyncStatus> {
    this.resyncCalls += 1;
    return this.onResync(policy, this.resyncCalls);
  }

  async resyncViaSnapshot(policy?: SnapshotPolicy): Promise<ResyncStatus> {
    this.snapshotCalls += 1;
    return this.onSnapshot(policy, this.snapshotCalls);
  }

  notePointerObservation(_pointer: StatePointer, _nowMs: number): void {
    // The scripted double ignores the observation; the E5-raise path uses a REAL reader instead.
  }

  pollDue(nowMs: number, lastPollMs: number, thresholdMs: number): boolean {
    return nowMs - lastPollMs >= thresholdMs;
  }
}

describe("ReplicationPoller — baseline poll cadence gating (§4.2)", () => {
  it("polls only after the maxSyncLag threshold elapses — never every tick (blind pollDue ⇒ red)", async () => {
    // A warm reader (never suspect): only the baseline liveness poll runs. Base 1000ms, zero jitter
    // ⇒ the threshold is exactly 1000ms, so the cadence boundary is unambiguous.
    const reader = new ScriptedReader(false, () => "converged");
    const poller = new ReplicationPoller(
      reader,
      new MapStore(),
      KEYS,
      fixedRng(),
      {
        jitterFraction: 0,
        pollBaseMs: 1000,
      }
    );

    await poller.onPollTick(0); // first warm tick SEEDS the cursor — no poll
    expect(reader.resyncCalls).toBe(0);

    await poller.onPollTick(500); // BELOW the threshold — the gate holds, no poll
    // The tooth: with pollDue-gating blinded (poll every tick), this tick would poll ⇒ count 1.
    expect(reader.resyncCalls).toBe(0);

    await poller.onPollTick(1000); // AT the threshold — poll fires
    expect(reader.resyncCalls).toBe(1);

    await poller.onPollTick(1500); // below the next threshold — no poll
    expect(reader.resyncCalls).toBe(1);

    await poller.onPollTick(2000); // next cadence boundary — poll fires
    expect(reader.resyncCalls).toBe(2);
  });
});

describe("ReplicationPoller — retry ladder escalation + terminal stop (§4.2, E20/E21)", () => {
  it("escalates a persistent delta hole to the snapshot bridge at attempt 3 (blind escalation ⇒ red)", async () => {
    // The delta pass NEVER converges (persistent hole ⇒ transient-miss, then needs-snapshot once the
    // budget is spent); only the snapshot bridge can converge. On converge the fake clears suspicion.
    const reader = new ScriptedReader(
      true,
      (policy) =>
        policy?.deltasExhausted === true ? "needs-snapshot" : transientMiss,
      (_policy, _call): ResyncStatus => {
        reader.suspect = false; // the snapshot bridged the hole — the reader reconverges
        return "converged";
      }
    );
    const poller = new ReplicationPoller(
      reader,
      new MapStore(),
      KEYS,
      fixedRng(),
      {
        jitterFraction: 0,
        firstLadderDelayMs: 1000,
        laterLadderDelayMs: 1000,
      }
    );

    // Drive ticks generously past every ladder delay until the reader reconverges (or a cap).
    for (let t = 0; t <= 100_000 && reader.suspect; t += 2000) {
      await poller.onPollTick(t);
    }

    // Escalation happened: the snapshot bridge was reached exactly once and cleared suspicion. With
    // attempt-3 escalation blinded (needs-snapshot never calls resyncViaSnapshot) the reader would
    // stall suspect forever and this assertion reds.
    expect(reader.suspect).toBeFalsy();
    expect(reader.snapshotCalls).toBe(1);
    expect(reader.resyncCalls).toBe(3); // rungs 1,2 (delta) then rung 3 (escalate)
  });

  it("runs ZERO ladder attempts while terminal AND snapBatch is unchanged — no cost-spin (re-arm-every-tick ⇒ red)", async () => {
    // Always suspect; the hole is unfillable. Delta pass ⇒ needs-snapshot once exhausted; the
    // snapshot bridge ⇒ transient-miss while it has budget, then terminal-gap once spent. The store
    // holds a pointer whose `snapBatch` NEVER advances, so no re-arm is ever warranted.
    const store = new MapStore();
    await store.set(
      KEYS.stateKey,
      JSON.stringify({
        epoch: 10,
        flushedAt: 1,
        lastBatch: 10,
        regId: REG_ID,
        snapBatch: 5,
      })
    );
    const reader = new ScriptedReader(
      true,
      (policy) =>
        policy?.deltasExhausted === true ? "needs-snapshot" : transientMiss,
      (policy) =>
        policy?.snapshotExhausted === true ? "terminal-gap" : transientMiss
    );
    const poller = new ReplicationPoller(reader, store, KEYS, fixedRng(), {
      jitterFraction: 0,
      firstLadderDelayMs: 1000,
      laterLadderDelayMs: 1000,
    });

    // Drive well past the point terminal-gap is reached (rung 4 spends the snapshot budget).
    for (let t = 0; t <= 40_000; t += 1000) {
      await poller.onPollTick(t);
    }
    const snapshotAtStop = reader.snapshotCalls;
    const resyncAtStop = reader.resyncCalls;
    expect(snapshotAtStop).toBe(2);

    for (let t = 41_000; t <= 200_000; t += 1000) {
      await poller.onPollTick(t);
    }
    expect(reader.snapshotCalls).toBe(snapshotAtStop);
    expect(reader.resyncCalls).toBe(resyncAtStop);
  });
});

describe("ReplicationPoller — the terminal verdict is indexed to the pointer (§4.2, E20, errata +25)", () => {
  /** A pointer scoped to `REG_ID` at a given `snapBatch` (the durable checkpoint index). */
  const pointerAt = (snapBatch: number): string =>
    JSON.stringify({
      epoch: 10,
      flushedAt: 1,
      lastBatch: 10,
      regId: REG_ID,
      snapBatch,
    });

  /** A reader whose ladder always rules terminal (snapshot missing at the verdict index), until
   * `armed` flips — modelling a later snapshot appearing that finally bridges the hole. */
  const terminalReader = (armed: { value: boolean }): ScriptedReader => {
    const reader: ScriptedReader = new ScriptedReader(
      true,
      (policy) =>
        policy?.deltasExhausted === true ? "needs-snapshot" : transientMiss,
      (policy) => {
        if (armed.value) {
          reader.suspect = false; // the newer snapshot bridged the hole — reconverge
          return "converged";
        }
        return policy?.snapshotExhausted === true
          ? "terminal-gap"
          : transientMiss;
      }
    );
    return reader;
  };

  it("re-arms ONE snapshot-permitted attempt when a later snapshot advances snapBatch — recovers (never-re-arm ⇒ red)", async () => {
    const store = new MapStore();
    await store.set(KEYS.stateKey, pointerAt(5)); // verdict will index to snapBatch 5
    const armed = { value: false };
    const reader = terminalReader(armed);
    const poller = new ReplicationPoller(reader, store, KEYS, fixedRng(), {
      firstLadderDelayMs: 1000,
      jitterFraction: 0,
      laterLadderDelayMs: 1000,
    });

    for (let t = 0; t <= 40_000; t += 1000) {
      await poller.onPollTick(t); // drive to terminal, indexed at snapBatch 5
    }
    const snapAtTerminal = reader.snapshotCalls;
    expect(reader.suspect).toBeTruthy(); // stuck terminal — no recovery yet

    // A LATER snapshot is blessed: the pointer's snapBatch advances 5 → 6. The vigil (still reading
    // __state every tick) detects it and re-arms exactly one snapshot-permitted attempt, which now
    // converges. Blind the re-arm and the reader stays terminal forever ⇒ this reds.
    armed.value = true;
    await store.set(KEYS.stateKey, pointerAt(6));
    await poller.onPollTick(50_000);
    expect(reader.snapshotCalls).toBe(snapAtTerminal + 1); // exactly ONE re-arm attempt
    expect(reader.suspect).toBeFalsy(); // recovered — the recovery contract finally fires
  });

  it("keeps observing __state every tick while terminal — the errata +25 backstop never stops (suppress-observation ⇒ red)", async () => {
    const store = new MapStore();
    await store.set(KEYS.stateKey, pointerAt(5));
    const reader = terminalReader({ value: false });
    const poller = new ReplicationPoller(reader, store, KEYS, fixedRng(), {
      firstLadderDelayMs: 1000,
      jitterFraction: 0,
      laterLadderDelayMs: 1000,
    });

    for (let t = 0; t <= 40_000; t += 1000) {
      await poller.onPollTick(t); // reach terminal
    }
    const readsAtTerminal = store.reads;

    // 20 more ticks while terminal (snapBatch unchanged). Every one STILL reads __state — the +25
    // backstop that feeds E5 and detects a snapBatch advance. Suppress the observation (skip the
    // read while terminal) and the count stops climbing ⇒ this reds.
    let ticks = 0;
    for (let t = 41_000; t <= 60_000; t += 1000) {
      await poller.onPollTick(t);
      ticks += 1;
    }
    expect(store.reads).toBe(readsAtTerminal + ticks); // one __state read per terminal tick
  });
});

/** Drive ticks from `from` in `stepMs` increments and return the FIRST time the baseline poll fires
 * again (the tick at which `resyncCalls` grew). The GAP between two such times IS the interval the
 * poller chose — so the ladder of gaps is the backoff ladder, observed from outside. */
async function nextPollAt(
  poller: ReplicationPoller,
  reader: ScriptedReader,
  from: number,
  stepMs = 1000
): Promise<number> {
  const before = reader.resyncCalls;
  for (let t = from; t <= from + 2_000_000; t += stepMs) {
    await poller.onPollTick(t);
    if (reader.resyncCalls > before) {
      return t;
    }
  }
  throw new Error("the baseline poll never fired again");
}

describe("ReplicationPoller — poll-failure backoff: ×2, cap 300s, jittered (§4.2, C4-b)", () => {
  it("doubles the interval on each failed poll and CAPS it at 300s (blind the ×2 or the cap ⇒ red)", async () => {
    // A warm reader whose baseline poll NEVER converges (a sick L2: every pass a transient miss).
    // Zero jitter ⇒ the chosen interval is exactly the backed-off base, so the ladder is exact.
    const reader = new ScriptedReader(false, () => transientMiss);
    const poller = new ReplicationPoller(
      reader,
      new MapStore(),
      KEYS,
      fixedRng(),
      {
        jitterFraction: 0,
        pollBaseMs: 60_000,
      }
    );

    await poller.onPollTick(0); // seeds the poll cursor
    // Poll 1 at the base cadence (60s) — it fails.
    const p1 = await nextPollAt(poller, reader, 1000, 1000);
    expect(p1).toBe(60_000);
    // Then the ladder: 120s, 240s, and 300s FOREVER (the cap) — never 480s.
    const p2 = await nextPollAt(poller, reader, p1 + 1000, 1000);
    expect(p2 - p1).toBe(120_000); // ×2
    const p3 = await nextPollAt(poller, reader, p2 + 1000, 1000);
    expect(p3 - p2).toBe(240_000); // ×2 again
    const p4 = await nextPollAt(poller, reader, p3 + 1000, 1000);
    expect(p4 - p3).toBe(300_000); // capped (480_000 without the cap)
    const p5 = await nextPollAt(poller, reader, p4 + 1000, 1000);
    expect(p5 - p4).toBe(300_000); // stays capped
    // The tooth: with the ×2 blinded (interval stays 60s) every gap above is 60_000; with the cap
    // blinded, p4−p3 is 480_000. Either mutation reds this case.
  });

  it("RESETS the interval to the base on the first poll that converges (blind the reset ⇒ red)", async () => {
    // The poll fails twice (60s → back off to 120s → 240s), then converges: the NEXT interval must
    // be the base 60s again, not the backed-off 240s the failure episode had climbed to.
    let converge = false;
    const reader = new ScriptedReader(false, () =>
      converge ? "converged" : transientMiss
    );
    const poller = new ReplicationPoller(
      reader,
      new MapStore(),
      KEYS,
      fixedRng(),
      {
        jitterFraction: 0,
        pollBaseMs: 60_000,
      }
    );

    await poller.onPollTick(0);
    const p1 = await nextPollAt(poller, reader, 1000, 1000); // fails → interval 120s
    const p2 = await nextPollAt(poller, reader, p1 + 1000, 1000); // fails → interval 240s
    expect(p2 - p1).toBe(120_000);

    converge = true; // the store recovers; this poll converges → interval RESETS to the base
    const p3 = await nextPollAt(poller, reader, p2 + 1000, 1000);
    expect(p3 - p2).toBe(240_000); // (the interval the last failure had set)

    const p4 = await nextPollAt(poller, reader, p3 + 1000, 1000);
    // Back to the base cadence. Blind the reset (keep doubling / hold the backed-off interval) and
    // this is 480_000 (or 240_000) ⇒ red.
    expect(p4 - p3).toBe(60_000);
  });

  it("jitters the backed-off interval too — the fleet de-synchronizes when the store is sick", async () => {
    // rng.next() = 0 ⇒ factor (1 − f) = 0.8. The first failure backs 60s off to 120s; the NEXT
    // threshold is a JITTERED 120s ⇒ 96s, not a bare 120s. (The backoff multiplies the base the
    // jitter is drawn from — it does not replace the jitter.)
    const reader = new ScriptedReader(false, () => transientMiss);
    const poller = new ReplicationPoller(
      reader,
      new MapStore(),
      KEYS,
      fixedRng(0),
      { jitterFraction: 0.2, pollBaseMs: 60_000 }
    );

    await poller.onPollTick(0);
    const p1 = await nextPollAt(poller, reader, 1000, 1000); // 60s × 0.8 = 48s
    expect(p1).toBe(48_000);
    const p2 = await nextPollAt(poller, reader, p1 + 1000, 1000);
    expect(p2 - p1).toBe(96_000); // 120s × 0.8 — backed off AND jittered
  });
});

function unsettled(): void {
  return undefined;
}

/** A promise the test settles by hand — a store read that has not come back yet. */
function deferred<T>(): {
  promise: Promise<T>;
  settle: (value: T) => void;
  fail: (reason: unknown) => void;
} {
  let settle: (value: T) => void = unsettled;
  let fail: (reason: unknown) => void = unsettled;
  // oxlint-disable-next-line promise/avoid-new -- a test-controlled deferred is exactly what the rule cannot express with async/await: there IS no upstream promise to return or await — the TEST is the upstream, and holding a store read open by hand is the only way to script overlap deterministically (rule 12: no timers, no wall clock).
  const promise = new Promise<T>((resolve, reject) => {
    settle = resolve;
    fail = reject;
  });
  return { fail, promise, settle };
}

/** Drain every pending microtask (one macrotask hop). Time itself is still explicit — this only
 * lets already-scheduled continuations run, it never advances the poller's `nowMs`. */
async function flush(): Promise<void> {
  await setImmediate();
}

/** The driver's swallow: a piggybacked tick's rejection never reaches the caller (poll-driver.ts). */
function swallowed(): void {
  return undefined;
}

/** Fire a tick the way the request-piggyback driver does: UNAWAITED, rejection swallowed. */
async function piggyback(
  poller: ReplicationPoller,
  nowMs: number
): Promise<void> {
  await poller.onPollTick(nowMs).catch(swallowed);
}

/** A reader whose `resync` HANGS — the store read is still out — until the test settles or fails it.
 * Records every rung's policy (so the ladder's attempt ladder is visible from outside: the escalation
 * flag flips at attempt 3) and the PEAK number of rungs ever in flight at once. */
class GatedReader implements ReplicationReader {
  suspect = true;
  resyncCalls = 0;
  snapshotCalls = 0;
  inFlight = 0;
  maxInFlight = 0;
  readonly policies: (ResyncPolicy | undefined)[] = [];
  readonly #pending: ReturnType<typeof deferred<ResyncStatus>>[] = [];

  async resync(policy?: ResyncPolicy): Promise<ResyncStatus> {
    this.resyncCalls += 1;
    this.policies.push(policy);
    this.inFlight += 1;
    this.maxInFlight = Math.max(this.maxInFlight, this.inFlight);
    const gate = deferred<ResyncStatus>();
    this.#pending.push(gate);
    try {
      return await gate.promise;
    } finally {
      this.inFlight -= 1;
    }
  }

  async resyncViaSnapshot(): Promise<ResyncStatus> {
    this.snapshotCalls += 1;
    return "terminal-gap";
  }

  notePointerObservation(_pointer: StatePointer, _nowMs: number): void {
    // No pointer is written in these cases; the observation is never fed.
  }

  pollDue(nowMs: number, lastPollMs: number, thresholdMs: number): boolean {
    return nowMs - lastPollMs >= thresholdMs;
  }

  /** The sick L2 finally answers every hung rung — by FAILING. */
  failAll(): void {
    for (const gate of this.#pending.splice(0)) {
      gate.fail(new Error("l2 down"));
    }
  }

  /** The slow L2 finally answers every hung rung — the hole is still there. */
  settleAll(status: ResyncStatus): void {
    for (const gate of this.#pending.splice(0)) {
      gate.settle(status);
    }
  }
}

describe("ReplicationPoller — re-entrancy: the decide-and-commit block paces the ladder (§4.2, C4-b F1)", () => {
  it("a rung whose store I/O FAILS still leaves the ladder paced — attempts match the serial control (post-await nextDueMs ⇒ red)", async () => {
    // THE SERIAL CONTROL. The same four tick times, awaited one at a time, against a lagging (but
    // answering) store: rung 1 at t=0 arms the ~1s first delay ⇒ rung 2 at t=1000 arms the ~5s
    // escalation cadence ⇒ t=2000 and t=3000 are NOT due. Two rungs, neither escalated.
    const controlPolicies: (ResyncPolicy | undefined)[] = [];
    const control = new ScriptedReader(true, (policy) => {
      controlPolicies.push(policy);
      return transientMiss;
    });
    const controlPoller = new ReplicationPoller(
      control,
      new MapStore(),
      KEYS,
      fixedRng(),
      { firstLadderDelayMs: 1000, jitterFraction: 0, laterLadderDelayMs: 5000 }
    );
    for (const t of [0, 1000, 2000, 3000]) {
      await controlPoller.onPollTick(t);
    }

    // THE PIGGYBACKED RUN. Same cadences — but the ticks are fired unawaited (a burst of requests),
    // and the L2 is SICK: every rung hangs, then fails. The failure is the whole point: a step that
    // never returns can never write a post-await schedule, so the schedule has to be committed
    // BEFORE the I/O or it is lost, and an unpaced ladder burns a rung on every single tick.
    const reader = new GatedReader();
    const poller = new ReplicationPoller(
      reader,
      new MapStore(),
      KEYS,
      fixedRng(),
      { firstLadderDelayMs: 1000, jitterFraction: 0, laterLadderDelayMs: 5000 }
    );

    const t0 = piggyback(poller, 0); // rung 1 — claimed, now hanging on the sick store
    await flush();
    expect(reader.resyncCalls).toBe(1);

    // A tick lands while rung 1's read is STILL OUT (the re-entry the piggyback driver makes routine).
    const t1 = piggyback(poller, 1000);
    await flush();
    expect(reader.resyncCalls).toBe(1); // it DECLINED — no second rung on the same decision

    reader.failAll(); // the sick store answers rung 1: it fails (the swallow wrap eats it)
    await Promise.all([t0, t1]);

    // The rung is due again at 1000 (the provisional commit rung 1 made BEFORE it ever touched the
    // store — the write the failure would otherwise have taken with it).
    const t2 = piggyback(poller, 2000);
    await flush();
    reader.failAll();
    await t2;

    const t3 = piggyback(poller, 3000);
    await flush();
    reader.failAll();
    await t3;

    expect(reader.resyncCalls).toBe(control.resyncCalls); // 2 rungs — exactly the serial control's
    expect(reader.policies).toStrictEqual(controlPolicies); // …and the SAME rungs: no escalation
    expect(reader.snapshotCalls).toBe(0);
  });
});

describe("ReplicationPoller — re-entrancy: SINGLE-FLIGHT, a tick mid-step declines (§4.2, C4-b F1)", () => {
  it("never runs two ladder steps at once, even when a rung outlives the rung spacing (drop the in-flight flag ⇒ red)", async () => {
    // Rung duration > rung spacing: the ladder's cadence is 1s (both delays), the ticks arrive 1s
    // apart, and NO rung ever comes back while they do. So every tick after the first finds a step in
    // flight AND finds itself due — the exact overlap the piggyback driver produces under load.
    const reader = new GatedReader();
    const poller = new ReplicationPoller(
      reader,
      new MapStore(),
      KEYS,
      fixedRng(),
      { firstLadderDelayMs: 1000, jitterFraction: 0, laterLadderDelayMs: 1000 }
    );

    const ticks: Promise<void>[] = [];
    for (const t of [0, 1000, 2000, 3000]) {
      ticks.push(piggyback(poller, t));
      await flush(); // each tick gets all the way to its decision before the next is fired
    }

    // THE CONTRACT: one step, ever. With the flag dropped, ticks 2–4 each pass their (correctly
    // committed) due-check while rung N is still hanging, and run rungs 2, 3 and 4 CONCURRENTLY —
    // maxInFlight climbs to 4 and the ladder is at its snapshot budget in 3 seconds ⇒ both red.
    expect(reader.maxInFlight).toBe(1);
    expect(reader.resyncCalls).toBe(1);

    reader.settleAll(transientMiss); // the slow store finally answers; the hole is still there
    await Promise.all(ticks);
    expect(reader.resyncCalls).toBe(1); // and a declined tick NEVER queued: no deferred burst lands
  });
});

describe("ReplicationPoller — E5 pointer observation feeds derivative suspicion (§4.2, CACHE-27)", () => {
  it("reads the __state pointer each tick and raises suspicion on observed silence (blind the read ⇒ never raises)", async () => {
    // A REAL reader (default 30s heartbeat ⇒ raise after 90s of silence; ControllableBus does not
    // auto-establish, so it starts non-suspect). The store holds a pointer whose `flushedAt` never
    // advances, so the poller's per-tick observation must raise E5 once the silence window elapses.
    const { inval, store, keys } = buildReaderParts(REG_ID);
    const pointer: StatePointer = {
      epoch: 0,
      flushedAt: 42,
      lastBatch: 0,
      regId: REG_ID,
      snapBatch: 0,
    };
    await store.set(keys.stateKey, JSON.stringify(pointer));
    const poller = new ReplicationPoller(inval, store, keys, fixedRng(), {
      jitterFraction: 0,
      pollBaseMs: 10_000_000, // keep the baseline poll out of the way of the E5 observation
    });

    expect(inval.suspect).toBeFalsy();
    await poller.onPollTick(0); // seeds the silence baseline
    await poller.onPollTick(30_000); // silence 30s < 90s — no raise
    await poller.onPollTick(60_000); // silence 60s < 90s — no raise
    expect(inval.suspect).toBeFalsy();
    await poller.onPollTick(91_000); // silence 91s ≥ 90s — E5 raises
    // The tooth: blind the poller's pointer read (never feed notePointerObservation) ⇒ never raises.
    expect(inval.suspect).toBeTruthy();
  });
});
