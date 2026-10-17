import type { Rng, Store } from "./drivers";
import type {
  ResyncPolicy,
  ResyncStatus,
  SnapshotPolicy,
} from "./invalidation";
import type { MirrorKeys, StatePointer } from "./registry/mirror-keys";

// The replication-reader SHELL. The tick-driven retry ladder + poll
// driver that sits ABOVE the pure reader kernel (src/invalidation.ts) and OUT-OF-BAND from every
// read. The kernel decides (resync / resyncViaSnapshot / notePointerObservation / pollDue return
// a pure verdict); this shell SCHEDULES — it holds the ladder rung + the next-due time and acts on
// the tick that finds an attempt due. It never sleeps: `cache.ts` is inside the ground-rule-5
// ambient-timer-banned zone, so timing enters only as the `nowMs` a driver passes each tick and
// randomness only as the injected `Rng`. The DRIVER that calls `onPollTick` on a cadence is
// EXTERNAL to this pure zone (a deterministic test loop here; on workerd it is the
// request-piggyback driver in the RR adapter — `ctx.waitUntil(tick())` at request start, which is
// where ambient time and the tick-failure swallow live).
//
// THE LADDER NEVER BLOCKS A READ:
//   The retry ladder NEVER blocks a read. `validate()`'s awaited resync stays the bounded fast
// pass; a read arriving during `transient-miss` classifies `unknown` and takes the posture
//   NOW — the ladder heals the future, not the present. Fail-closed means what it says.
//
// So this poller only re-drives resync/resyncViaSnapshot in the background off the status the
// kernel already computed; the awaited read path in `validate()` is untouched.

/** The subset of the reader kernel (`Invalidation`) this shell drives — the seam a test double
 * can stand in for. `Invalidation` satisfies it structurally; nothing here reaches into kernel
 * internals or the frozen decision logic (2b-2-a stays sovereign). */
export interface ReplicationReader {
  /** Whether the reader currently distrusts its warm map (a read fails closed while true). */
  readonly suspect: boolean;
  /** One bounded delta pass; `deltasExhausted` escalates a persistent hole to `needs-snapshot`. */
  resync(policy?: ResyncPolicy): Promise<ResyncStatus>;
  /** The snapshot bridge; `snapshotExhausted` makes the permanent terminal-vs-converged ruling. */
  resyncViaSnapshot(policy?: SnapshotPolicy): Promise<ResyncStatus>;
  /** derivative liveness: one pointer observation (raise-only, sampling-rate-independent). */
  notePointerObservation(pointer: StatePointer, nowMs: number): void;
  /** The pure poll predicate: has `thresholdMs` elapsed since `lastPollMs`? */
  pollDue(nowMs: number, lastPollMs: number, thresholdMs: number): boolean;
}

/** Poll-cadence + ladder tuning. All in ms; the shell jitters them with the injected rng. */
export interface ReplicationPollerOptions {
  /** Base poll cadence — maxSyncLag (default 60s). Jittered ±`jitterFraction` per poll. */
  readonly pollBaseMs?: number;
  /** Ladder delay after attempt 1 (default ~1s), before the second delta retry. */
  readonly firstLadderDelayMs?: number;
  /** Ladder delay after attempts ≥2 (default ~5s), the escalation cadence. */
  readonly laterLadderDelayMs?: number;
  /** Symmetric jitter fraction (default 0.2 → ±20%), drawn from the injected rng. */
  readonly jitterFraction?: number;
}

// maxSyncLag base cadence. 60s is the WORKERS-profile default: the platform's KV
// propagation clause is "up to 60s or more", so the honest contract bound is 60s. The spec's `5s`
// is the Redis-profile cadence; the Workers value is reconciled by a post-measurement
// erratum if probes show the tail comfortably tighter. Numbers first, promises second.
const DEFAULT_POLL_BASE_MS = 60_000;
const DEFAULT_FIRST_LADDER_DELAY_MS = 1000;
const DEFAULT_LATER_LADDER_DELAY_MS = 5000;
const DEFAULT_JITTER_FRACTION = 0.2; // ±20%

/** Poll-failure backoff ceiling (knob): a warm baseline poll that does NOT converge doubles
 * the cadence (×2) up to this cap — 300s — and the doubled interval is jittered like any other, so a
 * fleet whose L2 is failing degrades to one attempt per ~5 minutes per isolate instead of hammering
 * a sick store every 60s. The cap is on the BASE interval; the ±jitter rides on top (a jittered
 * 300s is 240–360s), because de-synchronizing the fleet matters most exactly when the store is sick. */
const MAX_POLL_BACKOFF_MS = 300_000;
/** Backoff multiplier per consecutive failed baseline poll (×2). */
const POLL_BACKOFF_FACTOR = 2;

/** The rung at which the delta budget is spent and the pass may escalate to the snapshot bridge. */
const ESCALATE_AT_ATTEMPT = 3;
/** The rung at which the snapshot budget is ALSO spent — the reader makes the permanent ruling
 * (full replay if the chain is intact ⇒ converged, else terminal-gap). Below it, a lagging snap
 * gets one brief retry. */
const SNAPSHOT_FINAL_ATTEMPT = 4;

/** Active hole-chasing ladder state: the current rung and when the next attempt is due. */
interface Ladder {
  attempt: number;
  nextDueMs: number;
}

/** Parse the `__state` pointer bytes the reader resyncs off into the wire shape the
 * monitor consumes. Malformed / absent ⇒ `undefined` (the observation is simply skipped — a shell
 * read that cannot see the pointer never fabricates liveness). Numbers-only fields; missing
 * optionals default the same way the kernel reader does. */
function readStatePointer(raw: string): StatePointer | undefined {
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return undefined;
  }
  if (typeof parsed !== "object" || parsed === null) {
    return undefined;
  }
  // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- narrowing a confirmed non-null object to an index-readable record; every field read below is individually `typeof`-checked before use (mirrors the kernel reader's readPointer).
  const o = parsed as Record<string, unknown>;
  if (
    typeof o.epoch !== "number" ||
    typeof o.lastBatch !== "number" ||
    typeof o.regId !== "string"
  ) {
    return undefined;
  }
  return {
    epoch: o.epoch,
    flushedAt: typeof o.flushedAt === "number" ? o.flushedAt : 0,
    lastBatch: o.lastBatch,
    regId: o.regId,
    snapBatch: typeof o.snapBatch === "number" ? o.snapBatch : 0,
  };
}

/**
 * The tick-driven replication poll driver. Each `onPollTick(nowMs)`:
 *   1. reads the `__state` pointer and feeds the derivative liveness monitor
 *      (`notePointerObservation`) — raise-only, every tick, sampling-rate-independent;
 *   2. while the reader is SUSPECT, runs the retry ladder when an attempt is DUE:
 * attempt 1 → `resync()` (delta); +~1s → attempt 2 → `resync()` (delta); +~5s →
 *        attempt 3 → `resync({deltasExhausted})` and, on `needs-snapshot`, `resyncViaSnapshot()`
 *        (snapshot-permitted). `converged` clears the reader (and backs the ladder off to the
 *        baseline cadence when suspicion persists awaiting a live-channel head — never a spin);
 *        `terminal-gap` STOPS the ladder permanently (fail-closed, no spin);
 *   3. while WARM, runs the baseline liveness poll on the maxSyncLag cadence (60s ±20%), gated by
 * the kernel's pure `pollDue`, and BACKS OFF on failure: a non-converged poll doubles the
 *      interval (×2, cap 300s, still jittered); a converged one resets it to the base.
 *
 * Pure + tick-driven: no wall clock, no ambient timers, no sleeps — `nowMs` and the injected rng
 * are the only sources of time/randomness.
 *
 * RE-ENTRANCY-SAFE BY CONTRACT. `onPollTick` is NOT required to be awaited before the
 * next one is fired, and under the request-piggyback driver it routinely is not — a burst of
 * requests hands one isolate several ticks while a step's store I/O is still in flight. Two
 * properties make that safe HERE, at the root, rather than in each driver: (i) every due-decision
 * and the state writes it authorizes happen in ONE synchronous block — no `await` separates a
 * due-check from its commit, so no tick ever acts on a schedule another tick has already consumed;
 * (ii) at most ONE ladder step is in flight, and a tick arriving mid-step declines rather than
 * queues. The `__state` observation is exempt from both — the +25 vigil runs on EVERY tick.
 */
export class ReplicationPoller {
  readonly #reader: ReplicationReader;
  readonly #store: Store;
  readonly #keys: MirrorKeys;
  readonly #rng: Rng;

  readonly #pollBaseMs: number;
  readonly #firstLadderDelayMs: number;
  readonly #laterLadderDelayMs: number;
  readonly #jitterFraction: number;

  /** Active hole-chasing ladder, or `null` when warm (no hole to chase). */
  #ladder: Ladder | null = null;
  /** A terminal-gap verdict indexed to the pointer that produced it: the LADDER stays stopped
   * (fail-closed, no spin) while this chain cannot complete. Permanence attaches to the CHAIN, not
   * the vigil — the baseline observation never stops (below). Cleared on a fresh suspect episode OR
   * when a durable `snapBatch` advance past `#terminalSnapBatch` bridges the hole. */
  #terminal = false;
  /** The pointer `snapBatch` the terminal verdict was made against — expired deltas never return, so
   * ONLY an observed `snapBatch` beyond this can re-arm one snapshot-permitted attempt. `null` when
   * not terminal. */
  #terminalSnapBatch: number | null = null;
  /** Suspect state at the previous tick — the false→true EDGE opens a fresh episode. */
  #prevSuspect = false;
  /** SINGLE-FLIGHT: a ladder step is between its due-decision and its awaits. Set in the
   * SAME synchronous block as the due-decision that authorized it and cleared in that block's
   * `finally`, so at most one step ever runs. Ticks are request-piggybacked (an isolate gets no
   * free-running timer), which makes RE-ENTRY ROUTINE rather than exotic — a burst of requests can
   * hand this poller several ticks while one step's store I/O is still in flight. A tick that finds
   * a step running DECLINES; it never queues, because queueing merely defers the burst it exists to
   * prevent. Nothing is lost: the rung it would have run is already scheduled (below). */
  #stepInFlight = false;
  /** Last baseline poll time (`null` until the first warm tick seeds the cursor). */
  #lastPollMs: number | null = null;
  /** Jittered baseline threshold, recomputed each poll (the current interval ±20%). */
  #pollThresholdMs: number;
  /** The CURRENT baseline interval the threshold is drawn from: `#pollBaseMs` while polls converge,
   * doubled per consecutive failure up to `MAX_POLL_BACKOFF_MS`. Reset to the base by the
   * first poll that converges again — the backoff is a property of the current failure episode, never
   * a permanent cadence change. */
  #pollIntervalMs: number;

  constructor(
    reader: ReplicationReader,
    store: Store,
    keys: MirrorKeys,
    rng: Rng,
    options: ReplicationPollerOptions = {}
  ) {
    this.#reader = reader;
    this.#store = store;
    this.#keys = keys;
    this.#rng = rng;
    this.#pollBaseMs = options.pollBaseMs ?? DEFAULT_POLL_BASE_MS;
    this.#firstLadderDelayMs =
      options.firstLadderDelayMs ?? DEFAULT_FIRST_LADDER_DELAY_MS;
    this.#laterLadderDelayMs =
      options.laterLadderDelayMs ?? DEFAULT_LATER_LADDER_DELAY_MS;
    this.#jitterFraction = options.jitterFraction ?? DEFAULT_JITTER_FRACTION;
    this.#pollIntervalMs = this.#pollBaseMs;
    this.#pollThresholdMs = this.#jitter(this.#pollIntervalMs);
  }

  /**
   * Whether the reader's replication has reached a permanent terminal gap — the
   * hole-chasing ladder is stopped, fail-closed, until a durable `snapBatch` advance re-arms it.
   * Read-only exposition of the already-tracked `#terminal` verdict for `explain`; it
   * adds no bookkeeping — the field is written only by the tick machinery, never by this getter.
   */
  get terminal(): boolean {
    return this.#terminal;
  }

  /**
   * Advance the state machine one tick. Always observes the pointer for liveness; then
   * drives the ladder (suspect) or the baseline poll (warm). Out-of-band — never on a read path.
   */
  async onPollTick(nowMs: number): Promise<void> {
    // 1) The baseline `__state` observation — feed EVERY tick, INCLUDING while terminal. Errata +25:
    //    every isolate observes `__state` at least every maxSyncLag — the silent-drop backstop, the
    // silence monitor's feed, and the detector for a durable `snapBatch` advance that re-arms recovery.
    //    A pointer the shell cannot read (absent/garbled) is simply not observed; never fabricates
    //    freshness. The vigil never stops; only the hole-chasing LADDER does (below).
    const raw = await this.#store.get(this.#keys.stateKey);
    const pointer = raw === undefined ? undefined : readStatePointer(raw.value);
    if (pointer !== undefined) {
      this.#reader.notePointerObservation(pointer, nowMs);
    }

    // 2) SINGLE-FLIGHT DECLINE. A ladder step is still between its due-decision and its
    //    awaits: this tick takes NO decision and mutates NO state — it declines and returns. It does
    //    not queue (a queued tick is the same burst, merely deferred). The observation above ALREADY
    // ran, so the +25 vigil and the feed are never skipped by a decline — only the ACTING is.
    //    Placed BEFORE the episode-edge bookkeeping on purpose: while a step is in flight, no other
    //    tick may touch `#ladder` / `#terminal` / `#prevSuspect` out from under it, so the edge it
    //    would have seen is simply seen by the next tick that gets to act.
    if (this.#stepInFlight) {
      return;
    }

    const { suspect } = this.#reader;
    // A fresh suspect episode (false→true) clears any prior terminal verdict: the new gap/reset may
    // be fillable, so the ladder gets to try again.
    if (suspect && !this.#prevSuspect) {
      this.#terminal = false;
      this.#terminalSnapBatch = null;
      this.#ladder = null;
    }
    this.#prevSuspect = suspect;

    // 3) Terminal verdict: the LADDER stays stopped (never-spin) — but permanence is indexed to the
    //    CHAIN, not the reader. Only a durable `snapBatch` advance PAST the verdict index can bridge
    //    the hole (expired deltas never return), so re-arm exactly once per advancement: ONE
    //    snapshot-permitted attempt; a failure re-verdicts at the new index. An unchanged `snapBatch`
    //    does zero work here (no snap fetch, no ladder) — the observation above is the only cost.
    if (this.#terminal) {
      if (
        pointer !== undefined &&
        this.#terminalSnapBatch !== null &&
        pointer.snapBatch > this.#terminalSnapBatch
      ) {
        this.#terminal = false;
        this.#terminalSnapBatch = null;
        const status = await this.#reader.resyncViaSnapshot({
          snapshotExhausted: true,
        });
        if (status === "terminal-gap") {
          // Still unfillable at the new index — re-verdict and wait for the next advance.
          this.#terminal = true;
          this.#terminalSnapBatch = pointer.snapBatch;
        }
        // else converged/transient — the kernel clears suspicion via the live channel as usual.
      }
      return;
    }

    if (suspect) {
      // 4) Hole-chasing ladder. Arm it on the first suspect tick of the episode; act when due.
      const ladder = this.#ladder ?? { attempt: 0, nextDueMs: nowMs };
      this.#ladder = ladder;
      if (nowMs < ladder.nextDueMs) {
        return;
      }

      // ── THE DECIDE-AND-COMMIT BLOCK ────────────────────────────────────────────────
      // SYNCHRONOUS: the due-check above and EVERY state write it authorizes land in one turn, with
      // no `await` between them. The rung is claimed (`attempt`), the NEXT rung's due-time is
      // committed PROVISIONALLY — before the I/O, not after it — and the step is marked in flight.
      // So the state a re-entrant tick reads is always the state this decision already implies: it
      // sees the rung claimed and the next one scheduled, never the stale window in which a second
      // tick would pass the same due-check and run a second rung off the same decision (the ladder's
      // whole delta+snapshot budget burning down in seconds against a store that is still sick).
      // The outcome only ever OVERWRITES the provisional: `converged` → the warm cadence,
      // `terminal-gap` → no ladder at all, `transient-miss` → exactly this provisional retry. And an
      // I/O failure that never returns at all (a rejecting store — the very case this poll exists
      // for) leaves a COMMITTED next-due behind rather than an unpaced ladder.
      ladder.attempt += 1;
      ladder.nextDueMs =
        nowMs + this.#jitter(this.#ladderDelay(ladder.attempt));
      this.#stepInFlight = true;
      try {
        await this.#runLadderStep(nowMs, ladder, pointer?.snapBatch ?? 0);
      } finally {
        this.#stepInFlight = false;
      }
      return;
    }

    // 5) Warm — drop any hole-chasing ladder and run the baseline liveness poll on the maxSyncLag
    //    cadence, GATED by the kernel's pure `pollDue`. (The WARM path already commits before it
    // awaits: `#lastPollMs` is written above the `resync()` below, so it needs no change.)
    this.#ladder = null;
    if (this.#lastPollMs === null) {
      // Seed the poll cursor on the first warm tick; the baseline poll fires a cadence later.
      this.#lastPollMs = nowMs;
      this.#pollThresholdMs = this.#jitter(this.#pollIntervalMs);
      return;
    }
    if (this.#reader.pollDue(nowMs, this.#lastPollMs, this.#pollThresholdMs)) {
      this.#lastPollMs = nowMs;
      // POLL-FAILURE BACKOFF (knob). The baseline poll's status is no longer discarded: a
      // pass that does NOT converge (a transient hole, a snapshot escalation, a terminal verdict —
      // every non-converged verdict means this poll did not achieve liveness) DOUBLES the interval up
      // to the 300s cap; a converged pass RESETS it to the base. The next threshold is drawn off the
      // resulting interval, jittered as always, so a fleet backing off stays de-synchronized.
      const status = await this.#reader.resync();
      this.#pollIntervalMs =
        status === "converged"
          ? this.#pollBaseMs
          : this.#backedOff(this.#pollIntervalMs);
      this.#pollThresholdMs = this.#jitter(this.#pollIntervalMs);
    }
  }

  /** One backoff step: ×2, capped at 300s (never below the configured base — a base ABOVE the cap is
   * already slower than any backoff could make it, so the cap floors to it rather than SPEEDING the
   * poll up on failure). */
  #backedOff(intervalMs: number): number {
    const cap = Math.max(MAX_POLL_BACKOFF_MS, this.#pollBaseMs);
    return Math.min(intervalMs * POLL_BACKOFF_FACTOR, cap);
  }

  /**
   * Run one ladder rung. Rungs 1–2 retry the delta pass; rung 3 escalates
   * (`deltasExhausted`) and bridges via the snapshot on `needs-snapshot`; rung ≥4 spends the
   * snapshot budget so the reader makes the permanent ruling. Never sleeps.
   *
   * the caller has ALREADY claimed the rung (`ladder.attempt`) and committed its provisional
   * next-due, synchronously, before this is entered — so this method only ever OVERWRITES that
   * schedule when the outcome calls for a different one. It re-schedules NOTHING on the retry path
   * (the provisional IS the retry), and it writes nothing that a re-entrant tick could race, because
   * the in-flight flag the caller set means no other tick is deciding anything while this runs.
   */
  async #runLadderStep(
    nowMs: number,
    ladder: Ladder,
    observedSnapBatch: number
  ): Promise<void> {
    const { attempt } = ladder;

    let status = await this.#reader.resync({
      deltasExhausted: attempt >= ESCALATE_AT_ATTEMPT,
    });
    if (status === "needs-snapshot") {
      // Attempt-3 escalation: the delta budget is spent, so bridge via the snapshot. A lagging snap
      // gets one brief retry (rung 3); rung ≥4 spends the snapshot budget → permanent ruling.
      status = await this.#reader.resyncViaSnapshot({
        snapshotExhausted: attempt >= SNAPSHOT_FINAL_ATTEMPT,
      });
    }

    if (status === "converged") {
      // The delta/snapshot pass caught up. If suspicion persists (no live-channel head declared this
      // episode — the poll-only posture), a resync cannot clear it; back OFF to the baseline
      // cadence rather than spin, and let a bus hello clear suspicion asynchronously. This is the
      // SUCCESS overwrite of the provisional retry the caller committed before the awaits.
      ladder.attempt = 0;
      ladder.nextDueMs = nowMs + this.#jitter(this.#pollBaseMs);
      return;
    }
    // Otherwise: `terminal-gap` — unfillable at THIS pointer: stop the LADDER (no spin),
    // indexing the verdict to the `snapBatch` it was made against. The baseline vigil keeps observing
    // `__state`  and re-arms one attempt only when a later snapshot advances `snapBatch`
    // past this index.
    //
    // …or `transient-miss` (or a lingering needs-snapshot when the bridge did not converge): retry on
    // the ladder cadence — attempt 1 → ~1s, attempts ≥2 → ~5s (both jittered). NOTHING is written for
    // that case: the retry is EXACTLY the provisional due-time the caller committed BEFORE the awaits
    //. Re-writing it here would restore the post-await commit this fix removes — the window in
    // which a tick reads a schedule its predecessor never lived to write, and burns a rung it was
    // never owed.
    if (status === "terminal-gap") {
      this.#terminal = true;
      this.#terminalSnapBatch = observedSnapBatch;
      this.#ladder = null;
    }
  }

  /** The base ladder delay for the rung just run: attempt 1 → ~1s, attempts ≥2 → ~5s. */
  #ladderDelay(attempt: number): number {
    return attempt <= 1 ? this.#firstLadderDelayMs : this.#laterLadderDelayMs;
  }

  /** Jitter a base delay symmetrically by ±`jitterFraction`, drawn from the injected rng (no
   * ambient randomness). rng.next() ∈ [0,1) ⇒ factor ∈ [1−f, 1+f). */
  #jitter(baseMs: number): number {
    const f = this.#jitterFraction;
    return baseMs * (1 - f + 2 * f * this.#rng.next());
  }
}
