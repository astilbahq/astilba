// @astilba/cache adapter — the doBus REDIAL driver (phase 2).
//
// PLATFORM: `src/adapters/**`. This is the home this design names: *transports
// report, they never retry themselves*. `doBus` dials, decodes, dispatches, and REPORTS its close —
// nothing more. Reconnect and backoff live HERE, one layer up, where the policy can be changed
// without touching the mechanism that carries the frames.
//
// THE LOOP IS LAZY, AND A TIMER IS NOT WHAT DRIVES IT. On `onClose` — a server close, an
// eviction, a dial failure, a protocol/scope refusal — this driver SCHEDULES: it records the instant
// the next attempt becomes due and returns. It dials nothing. The redial happens when a `tick`
// arrives at or after that instant, and a tick arrives from a request — which is the only context
// where a dial can succeed at all.
//
// WHY NOT A TIMER, AND WHY THE OLD ONE WAS WORSE THAN SLOW. `setTimeout` at global scope is refused
// outright by the platform (it is I/O), so scheduling a redial from a close that happened during
// module evaluation threw — inside a detached async IIFE, where the throw became an unhandled
// rejection and vanished. The ladder was dead from birth and the attempt counter never left 1. Even
// past that, a timer is the wrong instrument: it fires with no request context, so the dial it
// performs is refused; and an isolate frozen between requests DOES NOT RESUME ITS PENDING TIMERS —
// they are DROPPED (measured on workerd, not assumed; an earlier version of this comment claimed the
// opposite, and a redial that never happens is not "late", it is absent). So a timer here may only
// mark WHEN the next attempt is due; the attempt itself belongs to a request.
//
// BACKOFF: ×2 per consecutive failed establishment, capped at 300s, jittered ±20% off the injected
// `Rng` (the SAME family as the poll-failure backoff — one shape, two drivers). The counter RESETS on
// the `hello` that proves the new socket established, so a healthy socket that closes once redials
// promptly and only a genuinely-down Coordinator is backed off toward the cap. The ladder is now
// PACED BY TICKS rather than by wall-clock timers, so its resolution is the tick cadence — a
// deliberate trade: a redial that waits for the next request costs latency on an idle isolate, which
// serves no reads and therefore has no one to be stale to.
//
// A CLOSE IS NEVER FATAL, INCLUDING A SCOPE MISMATCH. A regId-mismatch close is a
// deployment error, not a transport fault, so redialing cannot fix it — but it also cannot make it
// worse: the client dispatches NOTHING on a mismatch, the reader stays fail-closed on the durable
// mirror, and the capped backoff bounds the cost of retrying at one dial per ~5 minutes. Uniform
// redial keeps the branch count at zero; the alternative (a permanent, silent give-up) would trade a
// loud, cheap, self-healing loop for a quiet dead channel.

import type { Bus, BusEvent, Rng, Subscription } from "../../drivers";
import type { Dial, DoBusCloseInfo } from "./bus-client";
import { doBus } from "./bus-client";

/** First redial delay (≈ the ladder's fastest rung). */
const DEFAULT_BASE_DELAY_MS = 1000;
/** Redial backoff ceiling — the same 300s cap the poll-failure backoff uses. */
const MAX_REDIAL_DELAY_MS = 300_000;
/** ×2 per consecutive failed establishment. */
const REDIAL_BACKOFF_FACTOR = 2;
/** Symmetric jitter fraction (±20%), drawn from the injected rng. */
const DEFAULT_JITTER_FRACTION = 0.2;

/** `redialingDoBus` configuration. `regId` + `onClose` mirror `DoBusOptions` (the close report is
 * PASSED THROUGH to the app after the redial is scheduled — the driver consumes it, it does not
 * swallow it); `rng` is the jitter source (injected — the platform boundary owns ambient randomness,
 * and `createWorkersCache` hands its one `Rng` to both the kernel and this driver). */
export interface RedialOptions {
  readonly regId: string;
  readonly rng: Rng;
  /** Status report, after this driver has scheduled the redial. Observability only. */
  readonly onClose?: (info: DoBusCloseInfo) => void;
  /** First-attempt delay before the ×2 ladder (default 1s). */
  readonly baseDelayMs?: number;
  /** Symmetric jitter fraction (default 0.2 → ±20%). */
  readonly jitterFraction?: number;
}

/**
 * A `Bus` that keeps ONE live doBus subscription up (driver duty), redialing
 * LAZILY. Wraps the mechanism-only client: every close schedules a backed-off redial that the
 * next `tick` performs, and the app's `onEvent` sees an uninterrupted event stream across sockets
 * (each new socket re-handshakes `reset` → `hello`, which is exactly the signal the kernel reader
 * needs to re-resync — the same events it would see on a fresh subscribe).
 */
class RedialingBus implements Bus {
  readonly #dial: Dial;
  readonly #options: RedialOptions;
  readonly #baseDelayMs: number;
  readonly #jitterFraction: number;

  /** The live inner subscription, or `null` while the channel is down. */
  #inner: Subscription | null = null;
  /** The consecutive-failure count the backoff ladder rides on. */
  #attempt = 0;
  /** `true` once `close()` is called: no tick may redial afterwards. */
  #closed = false;
  /** The instant a redial becomes due, or `null` when the channel is up (nothing to redial) or has
   * never been dialed. Written when a close is REPORTED; consumed by `tick`. */
  #dueAtMs: number | null = null;
  /** The subscriber, held so a redial can re-attach the SAME consumer to a new socket. */
  #onEvent: ((event: BusEvent) => void) | null = null;
  /** The most recent `nowMs` a tick supplied — this driver's ONLY clock (`null` before the first
   * tick). See `#backoffFrom` for why a close is dated from it. */
  #lastTickMs: number | null = null;

  constructor(dial: Dial, options: RedialOptions) {
    this.#dial = dial;
    this.#options = options;
    this.#baseDelayMs = options.baseDelayMs ?? DEFAULT_BASE_DELAY_MS;
    this.#jitterFraction = options.jitterFraction ?? DEFAULT_JITTER_FRACTION;
  }

  subscribe(onEvent: (event: BusEvent) => void): Subscription {
    this.#onEvent = onEvent;
    this.#connect();
    return {
      close: (): void => {
        this.#closed = true;
        this.#dueAtMs = null;
        this.#onEvent = null;
        this.#inner?.close();
        this.#inner = null;
      },
    };
  }

  /**
   * Perform a DUE redial — called from a request context, which is what makes the dial legal.
   * A no-op when the channel is up, when nothing is due yet, or after `close()`. Cheap and
   * idempotent, so a carrier may call it on every tick without gating.
   */
  tick(nowMs: number): void {
    this.#lastTickMs = nowMs;
    if (this.#closed || this.#dueAtMs === null || nowMs < this.#dueAtMs) {
      return;
    }
    // Consume the schedule BEFORE dialing: the dial's own close-report is what writes the next due
    // time, and leaving this set across the attempt would let a second tick fire a duplicate socket.
    this.#dueAtMs = null;
    this.#connect();
  }

  /** Open one socket and wire it. The close handler SCHEDULES the next attempt; it never dials. */
  #connect(): void {
    if (this.#closed || this.#onEvent === null) {
      return;
    }
    const onEvent = this.#onEvent;
    this.#inner = doBus(this.#dial, {
      onClose: (info: DoBusCloseInfo): void => {
        this.#onSocketClosed(info);
      },
      regId: this.#options.regId,
    }).subscribe((event: BusEvent): void => {
      if (event.kind === "hello") {
        // Establishment PROVED (the DO echoed our scope and declared its head): this socket is
        // healthy, so the next close starts the ladder from the bottom rung again.
        this.#attempt = 0;
      }
      onEvent(event);
    });
  }

  /** A socket closed: count it, schedule the next attempt, and pass the report through. */
  #onSocketClosed(info: DoBusCloseInfo): void {
    this.#inner = null;
    if (!this.#closed) {
      // WHETHER THIS CLOSE WAS A FAILED DIAL OR A HEALTHY SOCKET DROPPING IS NOT DECIDED HERE. The
      // socket declares `lost` to its subscriber either way, and the kernel reader — which knows
      // whether a `hello` ever arrived — is the one place that classifies it and counts it. This
      // driver owns redial POLICY: it counts attempts and schedules the next rung.
      this.#attempt += 1;
      // SCHEDULE ONLY: record when the next attempt becomes due and stop. No timer is set, so
      // nothing here can be refused at global scope, and no dial is attempted outside a request.
      this.#dueAtMs =
        this.#backoffFrom() + this.#jitter(this.#delayFor(this.#attempt));
    }
    // Pass the report through AFTER scheduling — the app observes the close either way, and an
    // app callback that throws can never eat the redial.
    this.#options.onClose?.(info);
  }

  /**
   * The instant a backoff is measured from. This driver is handed time ONLY by `tick`, and a socket
   * dies whenever it dies — between ticks, with no `nowMs` in scope. So a close is dated from the
   * last tick observed.
   *
   * THE RESIDUE, PRICED: the last tick is at most one tick-cadence old, so a rung can be served up
   * to that much early. It is bounded (never "arbitrarily stale" — every tick refreshes it) and it
   * errs toward redialing SOONER, which is the direction that costs a dial rather than a stale
   * serve. Against the rungs that matter — the cap is 300s, the cadence is ~1s — it is noise.
   *
   * Before the first tick there is no observation at all, so `0` stands in: the due-time becomes
   * "the delay, counted from the epoch", which every real `nowMs` already exceeds, so the first tick
   * after an early close redials at once. That is the right answer for the case it covers — a dial
   * that failed during the very first request, where a prompt retry is exactly what is wanted.
   */
  #backoffFrom(): number {
    return this.#lastTickMs ?? 0;
  }

  /** The un-jittered delay for the nth consecutive failed establishment: base × 2^(n−1), cap 300s. */
  #delayFor(attempt: number): number {
    const grown =
      this.#baseDelayMs * REDIAL_BACKOFF_FACTOR ** Math.max(0, attempt - 1);
    return Math.min(grown, MAX_REDIAL_DELAY_MS);
  }

  /** Jitter a delay symmetrically by ±`jitterFraction` off the injected rng — a fleet whose
   * Coordinator just restarted must not redial in lockstep. rng.next() ∈ [0,1) ⇒ factor ∈ [1−f, 1+f). */
  #jitter(baseMs: number): number {
    const f = this.#jitterFraction;
    return baseMs * (1 - f + 2 * f * this.#options.rng.next());
  }
}

/** A `Bus` whose redial ladder is driven by ticks rather than timers. `tick(nowMs)` performs a
 * DUE redial and is a no-op otherwise; the composition calls it from a request context. */
export interface RedialingBusHandle extends Bus {
  tick(nowMs: number): void;
}

/**
 * Build a self-redialing doBus `Bus`. This is the bus `createWorkersCache` wires: the
 * transport reports its close, and this driver owns the reconnect + the ×2/300s jittered
 * backoff — LAZILY, performed by `tick` inside a request context rather than by a timer that would
 * be refused at global scope and would race a dead isolate everywhere else. Use the bare `doBus`
 * instead when the caller drives redials itself (the integration lane does — tests own time).
 */
export function redialingDoBus(
  dial: Dial,
  options: RedialOptions
): RedialingBusHandle {
  return new RedialingBus(dial, options);
}
