import type { Rng, Store } from "./drivers";
import type {
  ResyncPolicy,
  ResyncStatus,
  SnapshotPolicy,
} from "./invalidation";
import type { MirrorKeys, StatePointer } from "./registry/mirror-keys";

/* References: §4.2, E20, E21. */

export interface ReplicationReader {
  /** Whether the reader currently distrusts its warm map (E20 (i) — a read fails closed while true). */
  readonly suspect: boolean;
  /** One bounded delta pass; `deltasExhausted` escalates a persistent hole to `needs-snapshot`. */
  resync(policy?: ResyncPolicy): Promise<ResyncStatus>;

  resyncViaSnapshot(policy?: SnapshotPolicy): Promise<ResyncStatus>;
  /** E5 derivative liveness: one pointer observation (raise-only, sampling-rate-independent). */
  notePointerObservation(pointer: StatePointer, nowMs: number): void;
  /** The pure poll predicate: has `thresholdMs` elapsed since `lastPollMs`? */
  pollDue(nowMs: number, lastPollMs: number, thresholdMs: number): boolean;
}

/** Poll-cadence + ladder tuning (§4.2). All in ms; the shell jitters them with the injected rng. */
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

/* References: §4.2. */

const DEFAULT_POLL_BASE_MS = 60_000;
const DEFAULT_FIRST_LADDER_DELAY_MS = 1000;
const DEFAULT_LATER_LADDER_DELAY_MS = 5000;
const DEFAULT_JITTER_FRACTION = 0.2; // ±20%

/** Poll-failure backoff ceiling (Q4 knob, C4-b): a warm baseline poll that does NOT converge doubles
 * the cadence (×2) up to this cap — 300s — and the doubled interval is jittered like any other, so a
 * fleet whose L2 is failing degrades to one attempt per ~5 minutes per isolate instead of hammering
 * a sick store every 60s. The cap is on the BASE interval; the ±jitter rides on top (a jittered
 * 300s is 240–360s), because de-synchronizing the fleet matters most exactly when the store is sick. */
const MAX_POLL_BACKOFF_MS = 300_000;
/** Backoff multiplier per consecutive failed baseline poll (×2). */
const POLL_BACKOFF_FACTOR = 2;

/** The rung at which the delta budget is spent and the pass may escalate to the snapshot bridge. */
const ESCALATE_AT_ATTEMPT = 3;

const SNAPSHOT_FINAL_ATTEMPT = 4;

/** Active hole-chasing ladder state: the current rung and when the next attempt is due. */
interface Ladder {
  attempt: number;
  nextDueMs: number;
}

/** Parse the `__state` pointer bytes the reader resyncs off (§4.2, E19) into the wire shape the E5
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

/* References: §4.2, E20, E21, E5. */

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
   * when a durable `snapBatch` advance past `#terminalSnapBatch` bridges the hole (§2, errata +25). */
  #terminal = false;
  /** The pointer `snapBatch` the terminal verdict was made against — expired deltas never return, so
   * ONLY an observed `snapBatch` beyond this can re-arm one snapshot-permitted attempt. `null` when
   * not terminal. */
  #terminalSnapBatch: number | null = null;
  /** Suspect state at the previous tick — the false→true EDGE opens a fresh episode. */
  #prevSuspect = false;
  /** SINGLE-FLIGHT (C4-b, F1): a ladder step is between its due-decision and its awaits. Set in the
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
   * doubled per consecutive failure up to `MAX_POLL_BACKOFF_MS` (C4-b). Reset to the base by the
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
   * Whether the reader's replication has reached a permanent terminal gap (§4.2, E20/E21) — the
   * hole-chasing ladder is stopped, fail-closed, until a durable `snapBatch` advance re-arms it.
   * Read-only exposition of the already-tracked `#terminal` verdict for `explain` (E27 (4)); it
   * adds no bookkeeping — the field is written only by the tick machinery, never by this getter.
   */
  get terminal(): boolean {
    return this.#terminal;
  }

  /**
   * Advance the state machine one tick (§4.2). Always observes the pointer for E5 liveness; then
   * drives the ladder (suspect) or the baseline poll (warm). Out-of-band — never on a read path.
   */
  async onPollTick(nowMs: number): Promise<void> {
    // 1) The baseline `__state` observation — feed EVERY tick, INCLUDING while terminal. Errata +25:
    //    every isolate observes `__state` at least every maxSyncLag — the silent-drop backstop, the
    //    E5 monitor's feed, and the detector for a durable `snapBatch` advance that re-arms recovery.
    //    A pointer the shell cannot read (absent/garbled) is simply not observed; never fabricates
    //    freshness. The vigil never stops; only the hole-chasing LADDER does (below).
    const raw = await this.#store.get(this.#keys.stateKey);
    const pointer = raw === undefined ? undefined : readStatePointer(raw.value);
    if (pointer !== undefined) {
      this.#reader.notePointerObservation(pointer, nowMs);
    }

    // 2) SINGLE-FLIGHT DECLINE (C4-b, F1). A ladder step is still between its due-decision and its
    //    awaits: this tick takes NO decision and mutates NO state — it declines and returns. It does
    //    not queue (a queued tick is the same burst, merely deferred). The observation above ALREADY
    //    ran, so the +25 vigil and the E5 feed are never skipped by a decline — only the ACTING is.
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

      // ── THE DECIDE-AND-COMMIT BLOCK (C4-b, F1) ────────────────────────────────────────────────
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
    //    awaits: `#lastPollMs` is written above the `resync()` below, so it needs no F1 change.)
    this.#ladder = null;
    if (this.#lastPollMs === null) {
      // Seed the poll cursor on the first warm tick; the baseline poll fires a cadence later.
      this.#lastPollMs = nowMs;
      this.#pollThresholdMs = this.#jitter(this.#pollIntervalMs);
      return;
    }
    if (this.#reader.pollDue(nowMs, this.#lastPollMs, this.#pollThresholdMs)) {
      this.#lastPollMs = nowMs;
      // POLL-FAILURE BACKOFF (C4-b, Q4 knob). The baseline poll's status is no longer discarded: a
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

  /* References: §4.2, E20, E21. */

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
      status = await this.#reader.resyncViaSnapshot({
        snapshotExhausted: attempt >= SNAPSHOT_FINAL_ATTEMPT,
      });
    }

    if (status === "converged") {
      // The delta/snapshot pass caught up. If suspicion persists (no live-channel head declared this
      // episode — the Q4 poll-only posture), a resync cannot clear it; back OFF to the baseline
      // cadence rather than spin, and let a bus hello clear suspicion asynchronously. This is the
      // SUCCESS overwrite of the provisional retry the caller committed before the awaits.
      ladder.attempt = 0;
      ladder.nextDueMs = nowMs + this.#jitter(this.#pollBaseMs);
      return;
    }
    // Otherwise: `terminal-gap` — unfillable at THIS pointer (§3/E20): stop the LADDER (no spin),
    // indexing the verdict to the `snapBatch` it was made against. The baseline vigil keeps observing
    // `__state` (errata +25) and re-arms one attempt only when a later snapshot advances `snapBatch`
    // past this index.
    //
    // …or `transient-miss` (or a lingering needs-snapshot when the bridge did not converge): retry on
    // the ladder cadence — attempt 1 → ~1s, attempts ≥2 → ~5s (both jittered). NOTHING is written for
    // that case: the retry is EXACTLY the provisional due-time the caller committed BEFORE the awaits
    // (F1). Re-writing it here would restore the post-await commit this fix removes — the window in
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

  #jitter(baseMs: number): number {
    const f = this.#jitterFraction;
    return baseMs * (1 - f + 2 * f * this.#rng.next());
  }
}
