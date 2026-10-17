// @astilba/cache/react-router — the REQUEST-PIGGYBACK poll driver (phase 2).
//
// PLATFORM: `src/adapters/**`. This module is the DRIVER half of the split:
// the pure `ReplicationPoller` (kernel) decides and schedules off the `nowMs` it is handed; THIS side
// decides WHEN to hand it one. It reads the poller off the same-package `replicationPollerOf` seam —
// the consumer that seam was built for. The wall clock itself is read one layer up, in the
// middleware (behind its own PLATFORM note) and passed in as `nowMs`; the kernel stays pure.
//
// PIGGYBACK-ONLY (settled ruling, not re-decided here). A Workers isolate gets no free-running
// timers, and an alarm is a Durable Object capability an isolate does not have. It needs neither: an
// IDLE isolate serves no reads, so its suspicion state has no consumer, and Workers evicts it anyway
// (a long idle ends in a cold boot, which fails closed by construction). So the tick rides the
// REQUEST: fired at request start, unawaited, through `ctx.waitUntil` — it RACES the loaders and
// never blocks them. One tick activates all three dormant paths at once (the pointer feed, the
// resync ladder, the baseline `__state` poll) because `onPollTick` drives all three.
//
// THE HONEST RESIDUE: freshness observation is REQUEST-COUPLED. Detection latency is therefore
// max(suspicion threshold, inter-request gap) — a registry that goes silent during a traffic lull is
// not noticed until the next request arrives (that request's FIRST observation sees the whole
// accumulated silence and raises immediately — the monitor is elapsed-silence, not counted ticks,
// so it is sampling-rate-independent). This is a documented property, not a defect to engineer
// around: the only reader that could be harmed by late detection is a reader that is not reading.
//
// STALENESS GATE: a burst of requests must not hammer the store. A tick is fired only when the last
// one is at least `TICK_MIN_INTERVAL_MS` old (the ladder's fastest rung — every longer cadence the
// poller owns itself: `pollDue` gates the 60s baseline, the ladder gates its own rungs). So 1000
// requests/second cost at most one tick per second, and the poller's own cadences are unchanged.

import type { Cache } from "../../cache";
import { replicationPollerOf, resolveCacheIdentity } from "../../cache";
import type { ReplicationPoller } from "../../replication-poller";
import { safeEmit, TELEMETRY_EVENTS } from "../../telemetry";
import type { SinkErrorHook, TelemetrySink } from "../../telemetry";

/**
 * Minimum ms between two piggybacked ticks on ONE cache instance. Set to the ladder's fastest rung
 * (~1s): ticking faster can never make the poller act sooner (every path it drives is gated by its
 * own due-time), so anything below this is pure waste under load.
 */
export const TICK_MIN_INTERVAL_MS = 1000;

/**
 * The counter a swallowed tick failure increments. The tick runs OUT-OF-BAND from the response,
 * so its failure must never surface to the caller — but a silently failing replication poll is
 * exactly the kind of degradation an operator has to be able to see, so the swallow is paired with
 * this event. Deliberately payload-free apart from the error's NAME (a code, never free-form detail
 * or a key/tag reference), so a plain sink can be pointed at it safely.
 *
 * NOTE: `registry_degraded` / `state_stale` — the richer telemetry names this event may
 * eventually be folded into — are now ENROLLED in the const-union catalog (`TELEMETRY_EVENTS`) as
 * RESERVED members, but their emit condition is unruled: `poll_tick_failed` keeps emitting as-is.
 * (FOR FABLE: whether to enrich/rename this counter into a degraded-mode signal is an open question.)
 */
export const POLL_TICK_FAILED: typeof TELEMETRY_EVENTS.poll_tick_failed =
  TELEMETRY_EVENTS.poll_tick_failed;

/** Last piggybacked tick per cache instance — the staleness gate's cursor. Per-isolate by nature
 * (an isolate holds one `Cache`), and keyed by the instance so two caches in one isolate never
 * starve each other's ticks.
 *
 * Keyed by RESOLVED identity (the third instance-keyed seam, and the one that does not live beside
 * the other two). A facade and the instance it fronts drive the SAME poller, so they must share the
 * SAME gate: two gates over one poller would let an app holding both objects tick it twice per
 * window, which is exactly the hammering the gate exists to stop. */
const lastTickMs = new WeakMap<Cache, number>();

/**
 * THE SWALLOW WRAP (adapter-side; the guard itself now lives in the shared `safeEmit`).
 * `ctx.waitUntil(tick())` on a rejecting promise surfaces as an unhandled rejection in the request's
 * lifecycle — a failing L2 read would then be able to reach the response of a request that never
 * needed that read. So the tick is wrapped: catch → count → swallow. Adapter code by ruling, not
 * kernel: the kernel's `resync` is entitled to reject, and the driver that chose to run it
 * out-of-band is what owes the caller its silence.
 *
 * The returned promise NEVER rejects — and the guarantee is AIRTIGHT over the whole failure surface,
 * INCLUDING the reporting channel itself: the telemetry emit is routed through `safeEmit`, so
 * neither a throwing sink nor a rejecting `async` sink can turn the swallow into an escape, and the
 * injected `onSinkError` hook observes a swallowed sink failure. (The default `waitUntil` drops
 * the promise on the floor; a rejection here would therefore become an unhandled rejection —
 * process-fatal on the Node/RR lane, an uncaught request error on workerd. A background freshness
 * probe must never be able to kill a user's request.)
 */
async function runTick(
  poller: ReplicationPoller,
  nowMs: number,
  telemetry?: TelemetrySink,
  onSinkError?: SinkErrorHook
): Promise<void> {
  try {
    await poller.onPollTick(nowMs);
  } catch (error: unknown) {
    safeEmit(
      telemetry,
      {
        error: error instanceof Error ? error.name : "unknown",
        type: POLL_TICK_FAILED,
      },
      onSinkError
    );
  }
}

/**
 * Fire one replication poll tick for `cache` IF the last one is stale (see `TICK_MIN_INTERVAL_MS`).
 *
 * Returns the in-flight tick — hand it straight to `ctx.waitUntil` so it outlives the response — or
 * `undefined` when nothing is due (the gate held, or the cache is registry-absent and has no reader
 * to drive). The returned promise NEVER rejects: a store/resync failure inside the tick is caught,
 * counted through `telemetry` (`poll_tick_failed`), and swallowed, because a background freshness
 * probe has no business failing a user's response. The `telemetry` sink is NOT trusted to keep that
 * promise: it is app code on the failure path, so a sink that throws — or an `async` sink that
 * rejects — is neutralized too, and floats nothing.
 */
export function tickIfStale(
  cache: Cache,
  nowMs: number,
  telemetry?: TelemetrySink,
  onSinkError?: SinkErrorHook
): Promise<void> | undefined {
  const poller = replicationPollerOf(cache);
  if (poller === undefined) {
    // A registry-absent instance (no bus, no reader, no mirror): there is nothing to poll.
    return undefined;
  }
  const gateKey = resolveCacheIdentity(cache);
  const last = lastTickMs.get(gateKey);
  if (last !== undefined && nowMs - last < TICK_MIN_INTERVAL_MS) {
    // The gate: a burst of requests rides ONE tick. Every cadence longer than this is the poller's
    // own business (`pollDue`, the ladder's rungs) — this only stops the hammering.
    return undefined;
  }
  lastTickMs.set(gateKey, nowMs);
  return runTick(poller, nowMs, telemetry, onSinkError);
}
