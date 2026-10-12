/* References: §4.2, E5, E20, E21. */

// PLATFORM: Cloudflare runtime bindings are confined to this adapter.

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

/* References: §10. */

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
 * rejects — is neutralized too (F2), and floats nothing.
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
