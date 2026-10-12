// The demo world — what a request can reach (C5-c).
//
// The cache, the dials, and the counters are built ONCE per isolate in `workers/app.ts` (where the
// real bindings are) and handed to the router as context. This module is deliberately free of the
// Workers runtime's types: the app's own program is a React program, and the moment it learned what
// a `KVNamespace` is, every route file would need the platform's globals to typecheck. The seam is
// here — the worker knows about bindings, the app knows about a `Cache`.
//
// STATE IS ISOLATE-SCOPED, which is worth saying out loud on a page about honesty: arming a fault
// arms it in the isolate that served the request. Locally, under one dev server, that is one
// isolate and the console behaves like a control panel. It would not be one control panel across a
// real deployment's isolates, and this app does not pretend to be an operations tool.

import type {
  Cache,
  ChannelState,
  TelemetryEvent,
  TelemetrySink,
} from "@astilba/cache";
import type { RouterContext } from "react-router";
import { createContext } from "react-router";

import type { BusFaultStats, FaultDials, KvFaultStats } from "../chaos/dials";
import { clearBusStats, clearFaults, clearKvStats } from "../chaos/dials";
import type { SceneId } from "../chaos/script";
import { sceneOf } from "../chaos/script";
import type { WitnessResult } from "./probe";

/** A scene that is currently armed, and when it was armed — the clock the narration's observed
 * silence is measured from. */
interface ArmedScene {
  readonly id: SceneId;
  readonly atMs: number;
}

/**
 * A purge the demo issued and is waiting to see arrive — the apparatus behind the poll-rescue
 * measurement, and the only stopwatch on this page.
 *
 * The deaf-reader scene's argument is that a reader whose bus is dead still converges, because the
 * poll carrier is a second, independent path to the same truth. That is a claim about TIME, so it
 * needs a measurement rather than a paragraph: mark the instant the purge was accepted and the
 * reader's applied epoch at that instant, then watch for the epoch to move. When it moves, the
 * carrier did it — the bus is refusing to dial, and there is no third path.
 */
export interface PendingPurge {
  readonly atMs: number;
  /** The reader's applied epoch at the moment the purge was accepted. The rescue is proven by this
   * number being LEFT BEHIND — an epoch that never moves is a reader that never learned. */
  readonly appliedEpochAtPurge: number;
  /**
   * The reader's applied epoch immediately AFTER `expire()` returned — and this field is the whole
   * reason the row below can be trusted.
   *
   * WHY IT IS MEASURED HERE RATHER THAN INFERRED. The demo purges from the same isolate whose reader
   * it is watching, and `expire()`'s own registry ack educates that reader ON THE SPOT: with the bus
   * refusing every dial and not one millisecond of the poll cadence elapsed, the applied epoch has
   * already moved by the time the purge's own response renders. So the reader learned — but not from
   * the carrier, and not from the socket. It learned because it was the one who asked.
   *
   * A rescue credited to the poll carrier on that evidence would be a fabrication, and nothing about
   * a dial counter would catch it: zero dials succeeded, exactly as a genuine carrier rescue would
   * look. This is the field that tells the two apart.
   */
  readonly appliedEpochAfterAck: number;
}

/**
 * WHICH PATH BROUGHT THE PURGE HOME. Three of them, and the demo may only ever name the one its
 * evidence can actually establish.
 *
 *   - `own-ack`        — this isolate issued the purge, and `expire()`'s registry ack taught its own
 *                        reader before any tick or frame. Nothing was rescued: the reader was never
 *                        lost. It is the ONLY path a single-isolate local run can produce, which is
 *                        why this member exists rather than being quietly folded into the
 *                        interesting one.
 *   - `carrier-proven` — the reader had NO CHANNEL at the advance, so the bus cannot have carried
 *                        the frame. The poll of the mirror pointer is the only path left. The
 *                        alternative is not argued away; it is eliminated by a state the reader
 *                        declares (E28).
 *   - `indeterminate`  — the channel was up at the advance. Either path could have carried it, and
 *                        this page does not claim which.
 */
export type RescuePath = "own-ack" | "carrier-proven" | "indeterminate";

/** A purge that arrived, how long it took, and BY WHICH PATH — measured, never estimated. */
export interface MeasuredRescue {
  readonly afterMs: number;
  readonly fromEpoch: number;
  readonly toEpoch: number;
  readonly path: RescuePath;
}

/**
 * EVERYTHING one armed EPISODE dirties, in ONE record — the dials that caused it, the counters and
 * events that are evidence of it, the scene that is armed, the rescue stopwatch, and the reader the
 * episode booted for itself.
 *
 * ONE RECORD BECAUSE THERE IS ONE RESET PATH. This used to be four pieces, and the other four lived
 * as bare `let`s in the Workers entry — the one file no Node-side test can load. So the tooth beside
 * this file had no jurisdiction over half the state it claimed to cover: `heal()` was toothed,
 * `arm()` cleared the dials and nothing else, and the previous episode's counters, log, pending
 * purge and last rescue rode onto the next scene's page to be attributed to ITS fault, while the
 * page told the reader that arming one scene heals the other first. A second partial-reset path is
 * a per-seam checklist wearing a button.
 *
 * GENERIC OVER THE CACHE, for the reason `faultableKv` next door is generic over the binding: the
 * library's `Cache` is a nine-method interface nothing but the real thing satisfies, so pinning this
 * field to it would make the whole-record tooth untestable except by casting something into a lie —
 * and this record is the last thing in the app that should go untested, because a piece that escapes
 * the reset is a sentence on screen crediting one scene's fault with another's evidence. The worker
 * instantiates it at `Cache`; the tooth instantiates it at a stand-in and compares records.
 */
export interface EpisodeState<TCache = Cache> {
  readonly dials: FaultDials;
  readonly kvStats: KvFaultStats;
  readonly busStats: BusFaultStats;
  readonly events: TelemetryEvent[];
  /** The scene currently armed, and when — the clock the "fault armed for" row reports. */
  armed: ArmedScene | null;
  /** The purge awaiting arrival — the rescue stopwatch. */
  pending: PendingPurge | null;
  /** The last measured rescue. */
  rescue: MeasuredRescue | null;
  /**
   * The reader this EPISODE booted for itself, or `null` when the page is talking to the isolate's
   * long-lived cache — see `armScene`. Part of the record because a reader booted under scene A's
   * fault, still answering on scene B's page, is exactly the contamination this record exists to
   * make impossible.
   */
  episodeCache: TCache | null;
}

/**
 * End the episode: dials off, counters zeroed, log emptied, scene disarmed, stopwatch dropped, and
 * the episode's own reader handed back.
 *
 * IT LIVES HERE RATHER THAN IN THE WORKER, and that placement is the tooth. The worker is a Workers
 * entry — it imports `cloudflare:workers`, so no Node-side test can load it — which means a `heal()`
 * written inline there is a `heal()` no test can reach. The tooth beside this file would then be
 * testing a copy of the logic that happened to be spelled the same way, and the day someone edited
 * the worker's copy the tooth would sit there, green, guarding nothing. So the composition is here,
 * the worker calls it, and the test drives the same function the app runs.
 *
 * TOTAL BY CONSTRUCTION: it takes the whole record and writes every field. `heal()` used to clear
 * the dials alone, and the result was a page that called itself healthy while printing a fault's
 * counters as live evidence and latching a degraded-registry judgment forever off an event log
 * nobody cleared.
 */
export function healEpisode<TCache>(state: EpisodeState<TCache>): void {
  clearFaults(state.dials);
  clearKvStats(state.kvStats);
  clearBusStats(state.busStats);
  // The log is the piece whose absence was invisible: a stale dial looks like a fault that will not
  // turn off, but a stale log looks like a healthy page that has decided your registry is degraded.
  state.events.length = 0;
  state.armed = null;
  state.pending = null;
  state.rescue = null;
  // Back to the isolate's long-lived reader — the one whose channel came up before anything was
  // armed. The episode's own reader is not reset; it is DROPPED, because it was only ever this
  // episode's.
  state.episodeCache = null;
}

/** What arming needs from the running app. */
export interface ArmOptions<TCache = Cache> {
  /** The registry name — the one identity string the factory derives everything from (E19-b). */
  readonly registryName: string;
  /**
   * Build a NEW cache over the same bindings. The worker's job, because this module is deliberately
   * free of the platform's types and must not learn what a binding is.
   */
  readonly mintCache: () => TCache;
  /** The wall clock, read at the app's edge and passed in — this module reads no time of its own. */
  readonly nowMs: number;
}

/**
 * Heal, then arm exactly one scene — the ONE door beside `healEpisode`'s, and it goes through it.
 *
 * A scene never shows the last one's residue, and that is now true by construction rather than by
 * someone remembering to extend a list: this calls the same total reset `heal()` calls. It used to
 * call `clearFaults` alone.
 *
 * THE MINT IS THE DEAF-READER SCENE'S WHOLE RIG, and the ORDER is the rig. A channel that never
 * established is a property of a reader's LIFETIME (E28), so a page can only show one honestly by
 * owning a lifetime that began under the dead dial. The isolate's long-lived reader came up on the
 * first request anyone ever made, long before a fault was armed — its channel reads `established`
 * and no amount of arming will change that, which is why "arm, then reload" could never produce the
 * state the scene is about. So a scene that declares it gets a reader minted HERE, after its dial is
 * set: the next request is that reader's first use, its first dial meets the refusal, and
 * `never-established` is the truth rather than a caption. A fresh reader booted under a dead phone
 * line is not a fabrication — it is the apparatus, and the page names it.
 */
export function armScene<TCache>(
  id: SceneId,
  state: EpisodeState<TCache>,
  options: ArmOptions<TCache>
): void {
  healEpisode(state);
  const scene = sceneOf(id);
  scene.arm(state.dials, { registryName: options.registryName });
  // AFTER the dial: construction performs no I/O (E28), so what matters is that the first USE lands
  // on the far side of the fault — but arming first is the ordering that makes that true no matter
  // what construction later decides to do.
  state.episodeCache = scene.bootsOwnReader ? options.mintCache() : null;
  state.armed = { atMs: options.nowMs, id };
}

/* References: E27. */

function pathOf(pending: PendingPurge, channel: ChannelState): RescuePath {
  if (pending.appliedEpochAfterAck > pending.appliedEpochAtPurge) {
    return "own-ack";
  }
  return channel === "established" ? "indeterminate" : "carrier-proven";
}

/**
 * Has a pending purge arrived? Pure, so the demo's one stopwatch is testable without a cache.
 *
 * The rescue is declared only when the reader's applied epoch has actually MOVED past where it stood
 * when the purge was accepted. Epoch movement is the whole proof: it advances only on contiguously
 * applied frames, so a reader whose number went up has genuinely learned something rather than
 * merely been asked to.
 */
export function rescueIfArrived(
  pending: PendingPurge | null,
  witness: WitnessResult,
  nowMs: number
): MeasuredRescue | null {
  if (pending === null || !witness.ok) {
    return null;
  }
  // The channel is read from the SAME witness statement that reports the advance, so the state that
  // rules the bus out is the state at the instant the cursor moved rather than a later one.
  const { appliedEpoch, channel } = witness.explanation.reader;
  if (appliedEpoch <= pending.appliedEpochAtPurge) {
    return null;
  }
  return {
    afterMs: nowMs - pending.atMs,
    fromEpoch: pending.appliedEpochAtPurge,
    path: pathOf(pending, channel),
    toEpoch: appliedEpoch,
  };
}

/** Everything a route can reach. */
export interface DemoWorld {
  /**
   * The cache this request talks to: the EPISODE's own reader when a scene booted one, and the
   * isolate's long-lived cache otherwise. Both are the hero five-liner over the same WRAPPED
   * bindings; what differs is when the reader was born, which is the only thing that can make a
   * never-established channel showable (E28). The page names which one it is holding.
   */
  activeCache(): Cache;
  /** The one identity string the factory derives the DO address, the registry id, and the key
   * namespace from (E19-b). */
  readonly registryName: string;
  readonly dials: FaultDials;
  readonly kvStats: KvFaultStats;
  readonly busStats: BusFaultStats;
  /**
   * The demo's telemetry sink — handed to the hero factory AND to the root middleware.
   *
   * WHAT IT CAN SEE, AND WHY THAT CHANGED. This app used to be able to say something sharp here: the
   * five-liner's config took a name, a KV namespace, a Coordinator and defaults — and no sink — so a
   * hero-path app could not see the kernel's own counters at all. A suppressed write-back, an L2
   * write error, a private entry leaving L1 all happened where no five-liner could watch them, and
   * the console printed the gap as a finding because that is what it was.
   *
   * The gap is closed. `WorkersCacheConfig` now carries an optional `telemetry`, so this sink is
   * passed straight to the factory and the console sees the kernel's raw facts — including the two
   * the deaf-reader scene is built on, `bus_dial_failed` and `strong_degraded`. The demo is still
   * the sink; what changed is that it no longer has to compose the drivers by hand to be one.
   *
   * WHAT IT STILL CANNOT SEE, because the honest half survives the good news: the two DERIVED names
   * (`registry_degraded`, `state_stale`) are enrolled in the catalogue and emitted by nobody — they
   * are judgments over accumulated state, and the library declines to pick a threshold it has not
   * measured. The demo derives them itself, out here, where you can argue with the numbers.
   */
  readonly sink: TelemetrySink;
  /** Every telemetry event this isolate's cache has emitted, newest last. The demo is the sink. */
  readonly events: readonly TelemetryEvent[];
  /** The COORDINATOR end of the heartbeat number as this deployment's wrangler config actually set
   * it — read from `env`, not assumed. `undefined` means unset, which means dormant. */
  readonly coordinatorHeartbeatMs: number | null;
  /** Is this request holding the episode's own reader rather than the isolate's long-lived one?
   * The page says so where it matters: a reader's age is the whole subject of one scene. */
  onEpisodeReader(): boolean;
  /** The scene currently armed, if any. */
  armed(): ArmedScene | null;
  arm(id: SceneId): void;
  /**
   * End the episode: every dial off, every counter zeroed, the event log emptied, the armed scene
   * and any pending purge measurement dropped.
   *
   * TOTAL, and that totality is the contract rather than an implementation detail — `heal()` used to
   * clear the dials alone, and the result was a page that called itself healthy while printing a
   * fault's counters as live evidence and latching a degraded-registry judgment forever off an event
   * log nobody cleared. The state after `heal()` is the state before anything was ever armed, and
   * `arm()` goes through the SAME function, so a scene can never inherit the last one's evidence.
   *
   * What it does NOT reset is the ISOLATE'S LONG-LIVED CACHE: that reader's applied epoch, its
   * channel and its L1 are the SYSTEM's state, not the demo's, and a demo that reached in to reset
   * them would be faking an isolate restart it cannot do. An EPISODE's own reader is a different
   * object with a different claim on it — the demo minted it, so the demo may drop it, and healing
   * does: the page goes back to the long-lived reader it has been narrating all along.
   */
  heal(): void;
  /** Mark a purge as issued and start the rescue stopwatch. */
  purgeIssued(purge: Omit<PendingPurge, "atMs">): void;
  /** The purge awaiting arrival, if any. */
  pendingPurge(): PendingPurge | null;
  /** Record that the reader's applied epoch moved past a pending purge, and how long it took. */
  rescueObserved(rescue: MeasuredRescue): void;
  /** The last measured rescue, if one has been observed. */
  lastRescue(): MeasuredRescue | null;
  /** PLATFORM: the wall clock, at the app's edge. */
  nowMs(): number;
}

/** The `RouterContext` the worker seeds and the root middleware reads. Created with no default, so
 * a route that runs without the worker having seeded it throws instead of silently finding nothing.
 */
export const worldContext: RouterContext<DemoWorld> =
  createContext<DemoWorld>();

/** The Workers `ExecutionContext.waitUntil`, as the middleware wants it. Not exported: consumers
 * reach it through the typed `waitUntilContext` value below, never by importing this alias. */
type WaitUntil = (promise: Promise<unknown>) => void;

/**
 * The per-request `waitUntil`, threaded through the router context — NEVER captured on the
 * module-scope `world`.
 *
 * `ExecutionContext.waitUntil` belongs to ONE request: it holds work open past that request's
 * response and no other's. The cache is built once per isolate (E28), so a `waitUntil` stored on it
 * would be one request's handle reused by every later request — the exact lifetime error E28 exists
 * to end. So it rides the request context instead: the worker seeds this from the CURRENT request's
 * `ctx.waitUntil` on every `fetch`, and the root middleware reads it here and hands it to
 * `cacheMiddleware` per call. A separate default is deliberately omitted — a request that reaches the
 * middleware without the worker having seeded it should throw, not silently drop the poll tick's
 * lifeline. The middleware's own `waitUntil` default (a floating, unheld tick) is the fallback for
 * deployments that wire none; this demo wires one.
 */
export const waitUntilContext: RouterContext<WaitUntil> =
  createContext<WaitUntil>();
