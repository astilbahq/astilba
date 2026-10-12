import assert from "node:assert/strict";

import type {
  BusEvent,
  BusFrame,
  Store,
  TagChange,
  Validity,
} from "@astilba/cache";
import type { MirrorKeys, StatePointer } from "@astilba/cache/registry";
import { describe, it } from "vitest";

import type {
  ResyncPolicy,
  ResyncStatus,
  SnapshotPolicy,
} from "../../../src/invalidation";

// Replication READER contract (§4.2, E19/E20/E21, F12). The per-isolate reader's honest
// job is to resync off the durable `state`/`delta`/`snap` mirror and DECIDE — converge,
// retry, escalate, or fail-closed-permanent — WITHOUT ever serving a value through an
// unfilled hole. The reference is the real `Invalidation`; the deliberately-wrong reader
// fixtures (fixtures/broken) each violate ONE property so `reader-teeth.test.ts` can prove
// the matching check discriminates a liar (the C0 birthright). Deterministic lane (rule 12):
// attempts/ticks are injected explicitly — no real timers, no timing tolerance. The shell's
// retry-sleep ladder + poll driver are 2b-2-b; this pins the pure decision surface only.

/** The minimal reader surface the contract drives — both the reference (`Invalidation`)
 * and every liar implement it. Reads issued to the store are logged for the over-fetch check;
 * bus events, attempt policies, and heartbeat ticks are all injected explicitly. */
export interface ContractReader {
  /** Deliver one bus event (reset/hello/gap/frame) — drives suspicion + the E21 head anchor. */
  applyBusEvent(event: BusEvent): void;
  /** The bounded DELTA pass (validate's fast pass). Returns the status the shell schedules off. */
  resync(policy?: ResyncPolicy): Promise<ResyncStatus>;
  /** The escalated SNAPSHOT bridge (the shell drives this after `needs-snapshot`). */
  resyncViaSnapshot(policy?: SnapshotPolicy): Promise<ResyncStatus>;
  /** The eventual-read serve verdict over an entry: `unknown` ⇒ fail-closed (NO serve-through). */
  verdict(tags: readonly string[], bornEpoch: number): Promise<Validity>;
  /** Feed one heartbeat pointer observation (E5 derivative raising). */
  notePointerObservation(pointer: StatePointer, nowMs: number): void;
  /** Whether the reader currently distrusts its warm map (E4/E20/E5). */
  suspect(): boolean;
  /** The poll-due predicate (pure). */
  pollDue(nowMs: number, lastPollMs: number, thresholdMs: number): boolean;
}

/** A store whose reads are logged — a real KV/DO store cannot expose this, so the over-fetch
 * check runs only where the harness supplies it (the reference + liar fixtures here). Extends the
 * base `Store` so a fixture can seed via `set` and a liar can over-fetch via `get`. */
export interface RecordingStore extends Store {
  reads(): readonly string[];
  clearReads(): void;
}

/** What a reader-implementation factory returns for the contract to drive. */
export interface ReaderHarness {
  readonly reader: ContractReader;
  readonly store: RecordingStore;
  readonly keys: MirrorKeys;
  /** The registry id scoping the mirror keyspace + echoed by seeded pointers (E19). */
  readonly regId: string;
}

export interface ReaderContractOptions {
  readonly label?: string;
}

// ── Deterministic scenario builders (epoch === batch seq, the 1:1 convention) ────────────

const TAG = "conformance:reader:t";
const CONVERGED: ResyncStatus = "converged";
const TERMINAL_GAP: ResyncStatus = "terminal-gap";
const UNKNOWN: Validity = "unknown";
/** Mirrors the kernel's documented maxSyncLag base cadence (§4.2, 60s) — the shell jitters ±20%. */
const POLL_BASE_MS = 60_000;

/** The retry arm of `ResyncStatus` — narrows to the object member for its `seq` (test-side). */
function isTransientMiss(
  status: ResyncStatus
): status is { readonly kind: "transient-miss"; readonly seq: number } {
  return typeof status === "object";
}

/** A delta batch at `seq`: `fromEpoch = seq-1`, `toEpoch = seq` (the 1:1 convention). */
function deltaFrame(seq: number, changes: readonly TagChange[]): BusFrame {
  return { changes, fromEpoch: seq - 1, toEpoch: seq };
}

/** A from-0 snapshot frame covering `(0, toEpoch]`. */
function snapFrame(toEpoch: number, changes: readonly TagChange[]): BusFrame {
  return { changes, fromEpoch: 0, toEpoch };
}

/** A `__state` pointer scoped to `regId` (E19). */
function pointerFor(
  regId: string,
  fields: {
    readonly epoch: number;
    readonly lastBatch: number;
    readonly snapBatch: number;
    readonly flushedAt?: number;
  }
): StatePointer {
  return {
    epoch: fields.epoch,
    flushedAt: fields.flushedAt ?? fields.epoch,
    lastBatch: fields.lastBatch,
    regId,
    snapBatch: fields.snapBatch,
  };
}

async function putPointer(h: ReaderHarness, p: StatePointer): Promise<void> {
  await h.store.set(h.keys.stateKey, JSON.stringify(p));
}
async function putDelta(
  h: ReaderHarness,
  seq: number,
  frame: BusFrame
): Promise<void> {
  await h.store.set(h.keys.deltaKey(seq), JSON.stringify(frame));
}
async function putSnap(
  h: ReaderHarness,
  seq: number,
  frame: BusFrame
): Promise<void> {
  await h.store.set(h.keys.snapKey(seq), JSON.stringify(frame));
}

/** Establish a fresh episode declaring `head` over the live channel (reset → hello). */
function establish(reader: ContractReader, head: number): void {
  reader.applyBusEvent({ kind: "reset" });
  reader.applyBusEvent({ head, kind: "hello" });
}

/** Every mirror key the reader is ALLOWED to read for this pointer (E19; no over-fetch):
 * the pointer, the blessed snapshot alone, and deltas 1..lastBatch — nothing past the pointer. */
function allowedReadKeys(h: ReaderHarness, p: StatePointer): Set<string> {
  const allowed = new Set<string>([
    h.keys.stateKey,
    h.keys.snapKey(p.snapBatch),
  ]);
  for (let seq = 1; seq <= p.lastBatch; seq++) {
    allowed.add(h.keys.deltaKey(seq));
  }
  return allowed;
}

// ── The six fixture checks (each throws on violation) ────────────────────────────────────

/** 1. stale-pointer ⇒ fail-closed. A pointer LAGGING behind the declared head leaves the
 * reader suspect (it cannot reach the head), so an eventual read fails closed — the warm value
 * is NEVER served through. A reader that serves off the stale pointer (ignoring the head) fails. */
const checkStalePointerFailsClosed = async (
  make: () => ReaderHarness
): Promise<void> => {
  const h = make();
  await putDelta(h, 1, deltaFrame(1, [{ softEpoch: 1, tag: TAG }]));
  await putPointer(
    h,
    pointerFor(h.regId, { epoch: 1, lastBatch: 1, snapBatch: 0 })
  );
  // The live channel declares head 2 — epoch 2 happened but the pointer is parked at epoch 1.
  establish(h.reader, 2);
  const status = await h.reader.resync();
  // The delta pass reaches the (stale) pointer's lastBatch, but the head is above it.
  assert.strictEqual(
    status,
    CONVERGED,
    "the delta pass reaches the stale pointer"
  );
  assert.ok(
    h.reader.suspect(),
    "a pointer lagging behind the declared head leaves the reader suspect"
  );
  const v = await h.reader.verdict([TAG], 5);
  assert.strictEqual(
    v,
    UNKNOWN,
    "a lagging pointer must fail closed — the warm value is not served through"
  );
};

/** 2. hole-beyond-retries ⇒ snapshot-never-through. An unfilled hole the snapshot cannot bridge
 * (no blessed snap, chain broken) must NEVER let the reader serve the stale value through. A
 * serve-through-a-hole reader fails. */
const checkHoleNeverServesThrough = async (
  make: () => ReaderHarness
): Promise<void> => {
  const h = make();
  // lastBatch 2, but delta:2 is MISSING (the hole); no blessed snapshot (snapBatch 0).
  await putDelta(h, 1, deltaFrame(1, [{ softEpoch: 1, tag: TAG }]));
  await putPointer(
    h,
    pointerFor(h.regId, { epoch: 2, lastBatch: 2, snapBatch: 0 })
  );
  establish(h.reader, 2);
  const retry = await h.reader.resync();
  assert.ok(
    isTransientMiss(retry) && retry.seq === 2,
    "an unfilled hole first signals transient-miss at the missing seq"
  );
  const escalate = await h.reader.resync({ deltasExhausted: true });
  assert.strictEqual(
    escalate,
    "needs-snapshot",
    "with the delta-retry budget spent, the reader escalates"
  );
  const ruled = await h.reader.resyncViaSnapshot({ snapshotExhausted: true });
  assert.strictEqual(
    ruled,
    TERMINAL_GAP,
    "no snapshot + a broken chain is a terminal gap"
  );
  const v = await h.reader.verdict([TAG], 5);
  assert.strictEqual(
    v,
    UNKNOWN,
    "a value must never be served through an unfilled hole"
  );
};

/** 3. probe-past-pointer fails. The reader must fetch ONLY the pointer, deltas 1..lastBatch, and
 * the blessed snapshot — never a delta past `lastBatch` nor an unblessed `__snap`. Observed via
 * the read-logging store; an over-fetching reader fails. */
const checkNeverProbesPastPointer = async (
  make: () => ReaderHarness
): Promise<void> => {
  const h = make();
  const p = pointerFor(h.regId, { epoch: 2, lastBatch: 2, snapBatch: 1 });
  await putSnap(h, 1, snapFrame(1, [{ softEpoch: 1, tag: TAG }]));
  await putDelta(h, 1, deltaFrame(1, [{ softEpoch: 1, tag: TAG }]));
  await putDelta(h, 2, deltaFrame(2, [{ hardEpoch: 2, tag: TAG }]));
  await putPointer(h, p);
  establish(h.reader, 2);
  h.store.clearReads();
  // Exercise BOTH read paths — the delta pass and the snapshot bridge.
  await h.reader.resync();
  await h.reader.resyncViaSnapshot({ snapshotExhausted: true });
  const allowed = allowedReadKeys(h, p);
  for (const key of h.store.reads()) {
    assert.ok(
      allowed.has(key),
      `the reader must not probe past the pointer — read an out-of-bounds key ${JSON.stringify(key)} (E19/§4.4)`
    );
  }
};

/** 4. terminal-hole ⇒ permanent-gap, fail-closed. An unfillable chain (blessed snap gone AND
 * the delta chain below broken) rules `terminal-gap` — permanent (a re-drive stays terminal),
 * stays suspect, never served through. A reader that keeps signalling retry (would spin) or
 * serves through fails. */
const checkTerminalHolePermanent = async (
  make: () => ReaderHarness
): Promise<void> => {
  const h = make();
  // snapBatch 2 blessed but snap:2 MISSING; delta:2 MISSING (chain broken below the snap).
  await putDelta(h, 1, deltaFrame(1, [{ softEpoch: 1, tag: TAG }]));
  await putDelta(h, 3, deltaFrame(3, [{ hardEpoch: 3, tag: TAG }]));
  await putPointer(
    h,
    pointerFor(h.regId, { epoch: 3, lastBatch: 3, snapBatch: 2 })
  );
  establish(h.reader, 3);
  const brief = await h.reader.resyncViaSnapshot();
  assert.ok(
    isTransientMiss(brief) && brief.seq === 2,
    "a blessed-but-missing snap gets ONE brief retry before the ruling"
  );
  const ruled = await h.reader.resyncViaSnapshot({ snapshotExhausted: true });
  assert.strictEqual(
    ruled,
    TERMINAL_GAP,
    "snap gone AND chain broken is a terminal gap"
  );
  // Permanence: a re-drive stays terminal (the shell can stop retrying) — never flips to serve.
  const again = await h.reader.resyncViaSnapshot({ snapshotExhausted: true });
  assert.strictEqual(
    again,
    TERMINAL_GAP,
    "a terminal gap is permanent, not transient"
  );
  assert.ok(
    h.reader.suspect(),
    "a terminal gap keeps the reader suspect (fail-closed)"
  );
  const v = await h.reader.verdict([TAG], 5);
  assert.strictEqual(v, UNKNOWN, "a terminal gap never serves through");
};

/** 5. lagging-snapBatch is legal. `snapBatch` may trail `lastBatch` arbitrarily; the reader
 * bridges the GC'd region below the snap floor and converges — NOT flagged anomalous. A reader
 * that refuses a lagging snapBatch (treats it as a gap) fails. */
const checkLaggingSnapBatchConverges = async (
  make: () => ReaderHarness
): Promise<void> => {
  const h = make();
  // snapBatch 2 trails lastBatch 4; deltas 1,2 are GC'd (absent) below the snap floor (legal).
  await putSnap(h, 2, snapFrame(2, [{ softEpoch: 2, tag: TAG }]));
  await putDelta(h, 3, deltaFrame(3, [{ softEpoch: 3, tag: TAG }]));
  await putDelta(h, 4, deltaFrame(4, [{ softEpoch: 4, tag: TAG }]));
  await putPointer(
    h,
    pointerFor(h.regId, { epoch: 4, lastBatch: 4, snapBatch: 2 })
  );
  establish(h.reader, 4);
  // Deltas 1,2 missing ⇒ the delta pass holes; escalate to the snapshot bridge.
  const status = await h.reader.resyncViaSnapshot();
  assert.strictEqual(
    status,
    CONVERGED,
    "a lagging snapBatch bridges the GC'd floor and converges — not anomalous"
  );
  assert.ok(!h.reader.suspect(), "reaching the declared head clears suspicion");
  const v = await h.reader.verdict([TAG], 5);
  assert.strictEqual(
    v,
    "fresh",
    "the converged reader serves the warm value normally (soft@4 ≤ born 5)"
  );
};

/** 6. blessed-but-missing-snap. The reference retries once, then rules: full delta replay if the
 * chain `(0, lastBatch]` is intact (⇒ converged), else terminal. A reader that treats a missing
 * blessed snap as covered (converges/serves regardless of the chain) fails on the broken chain. */
const checkBlessedButMissingSnap = async (
  make: () => ReaderHarness
): Promise<void> => {
  // (a) INTACT chain: snap:2 missing but deltas 1..3 all present ⇒ full replay converges.
  const intact = make();
  await putDelta(intact, 1, deltaFrame(1, [{ softEpoch: 1, tag: TAG }]));
  await putDelta(intact, 2, deltaFrame(2, [{ softEpoch: 2, tag: TAG }]));
  await putDelta(intact, 3, deltaFrame(3, [{ hardEpoch: 3, tag: TAG }]));
  await putPointer(
    intact,
    pointerFor(intact.regId, { epoch: 3, lastBatch: 3, snapBatch: 2 })
  );
  establish(intact.reader, 3);
  const okStatus = await intact.reader.resyncViaSnapshot({
    snapshotExhausted: true,
  });
  assert.strictEqual(
    okStatus,
    CONVERGED,
    "a missing blessed snap recovers via full delta replay when the chain is intact"
  );

  // (b) BROKEN chain: snap:2 missing AND delta:2 missing ⇒ terminal-gap (never served through).
  const broken = make();
  await putDelta(broken, 1, deltaFrame(1, [{ softEpoch: 1, tag: TAG }]));
  await putDelta(broken, 3, deltaFrame(3, [{ hardEpoch: 3, tag: TAG }]));
  await putPointer(
    broken,
    pointerFor(broken.regId, { epoch: 3, lastBatch: 3, snapBatch: 2 })
  );
  establish(broken.reader, 3);
  const ruled = await broken.reader.resyncViaSnapshot({
    snapshotExhausted: true,
  });
  assert.strictEqual(
    ruled,
    TERMINAL_GAP,
    "a missing blessed snap over a broken chain is terminal — not silently 'covered'"
  );
  const v = await broken.reader.verdict([TAG], 5);
  assert.strictEqual(v, UNKNOWN, "the broken-chain reader fails closed");
};

/** 7. E5 derivative raising. Observed SILENCE — `flushedAt` failing to advance for `3 ×
 * heartbeatIntervalMs` (90s at the 30s default) — RAISES suspicion. Derivative-only (elapsed
 * silence, never absolute age) and raise-only (an advancing `flushedAt` never clears it —
 * CACHE-27). Sampling-rate-independent: the threshold is elapsed time, not a tick count. A reader
 * that never raises fails. */
const checkE5DerivativeRaising = async (
  make: () => ReaderHarness
): Promise<void> => {
  const h = make();
  // A pointer whose `flushedAt` never advances across ticks — the dead-writer silence signal.
  const stale = pointerFor(h.regId, {
    epoch: 1,
    flushedAt: 5,
    lastBatch: 1,
    snapBatch: 0,
  });
  assert.ok(!h.reader.suspect(), "a fresh reader is not suspect");
  h.reader.notePointerObservation(stale, 0); // baseline — silence starts at t=0
  h.reader.notePointerObservation(stale, 60_000); // 60s of silence — under the 90s threshold
  assert.ok(
    !h.reader.suspect(),
    "60s of observed silence does not raise (< 90s)"
  );
  h.reader.notePointerObservation(stale, 120_000); // ≥ 90s of silence ⇒ raise
  assert.ok(
    h.reader.suspect(),
    "observed silence reaching 3× the heartbeat interval raises suspicion (E5)"
  );
  // Raise-only: an advancing flushedAt must NOT clear suspicion (CACHE-27).
  h.reader.notePointerObservation(
    pointerFor(h.regId, {
      epoch: 2,
      flushedAt: 99,
      lastBatch: 1,
      snapBatch: 0,
    }),
    180_000
  );
  assert.ok(
    h.reader.suspect(),
    "an advancing flushedAt never clears suspicion (derivative RAISE-only — CACHE-27)"
  );
};

/** 8. pollDue predicate. Due exactly when `nowMs - lastPollMs >= thresholdMs` — pure and
 * deterministic (the base 60s cadence + jitter enter as `thresholdMs`). A reader whose predicate
 * ignores the elapsed comparison fails. */
const checkPollDuePredicate = async (
  make: () => ReaderHarness
): Promise<void> => {
  const h = make();
  assert.strictEqual(
    h.reader.pollDue(1000, 0, 1000),
    true,
    "due exactly at the threshold"
  );
  assert.strictEqual(
    h.reader.pollDue(999, 0, 1000),
    false,
    "not due before the threshold"
  );
  assert.strictEqual(
    h.reader.pollDue(POLL_BASE_MS, 0, POLL_BASE_MS),
    true,
    "due at the maxSyncLag base cadence"
  );
  assert.strictEqual(
    h.reader.pollDue(POLL_BASE_MS - 1, 0, POLL_BASE_MS),
    false,
    "not due one tick before the base cadence"
  );
};

type ReaderCheck = (make: () => ReaderHarness) => Promise<void>;

/** The individually-callable reader checks (each throws on violation). */
export const readerChecks: {
  readonly stalePointerFailsClosed: ReaderCheck;
  readonly holeNeverServesThrough: ReaderCheck;
  readonly neverProbesPastPointer: ReaderCheck;
  readonly terminalHolePermanent: ReaderCheck;
  readonly laggingSnapBatchConverges: ReaderCheck;
  readonly blessedButMissingSnap: ReaderCheck;
  readonly e5DerivativeRaising: ReaderCheck;
  readonly pollDuePredicate: ReaderCheck;
} = {
  blessedButMissingSnap: checkBlessedButMissingSnap,
  e5DerivativeRaising: checkE5DerivativeRaising,
  holeNeverServesThrough: checkHoleNeverServesThrough,
  laggingSnapBatchConverges: checkLaggingSnapBatchConverges,
  neverProbesPastPointer: checkNeverProbesPastPointer,
  pollDuePredicate: checkPollDuePredicate,
  stalePointerFailsClosed: checkStalePointerFailsClosed,
  terminalHolePermanent: checkTerminalHolePermanent,
};

/** Register the reader conformance suite for a provided implementation factory. */
export function runReaderContract(
  make: () => ReaderHarness,
  opts: ReaderContractOptions = {}
): void {
  describe(`${opts.label ?? "Reader"} — conformance contract`, () => {
    it("fails closed on a lagging pointer — never serves through a stale pointer (§4.2, E21)", async () => {
      await checkStalePointerFailsClosed(make);
    });
    it("never serves a value through an unfilled hole (§4.2, E20 i)", async () => {
      await checkHoleNeverServesThrough(make);
    });
    it("fetches only the pointer, deltas 1..lastBatch, and the blessed snap — never past the pointer (§4.4, E19)", async () => {
      await checkNeverProbesPastPointer(make);
    });
    it("rules a terminal gap permanent and fail-closed on an unfillable chain (§4.2, §3)", async () => {
      await checkTerminalHolePermanent(make);
    });
    it("converges on a lagging snapBatch — bridging the GC'd floor is not anomalous (§4.2)", async () => {
      await checkLaggingSnapBatchConverges(make);
    });
    it("recovers a blessed-but-missing snap via full replay when intact, else terminal (§2, §3)", async () => {
      await checkBlessedButMissingSnap(make);
    });
    it("raises suspicion after three derivative missed heartbeats, raise-only (E5, CACHE-27)", async () => {
      await checkE5DerivativeRaising(make);
    });
    it("decides poll-due purely on the elapsed threshold (§4.2, maxSyncLag)", async () => {
      await checkPollDuePredicate(make);
    });
  });
}
