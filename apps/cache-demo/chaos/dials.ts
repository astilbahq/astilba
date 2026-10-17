// The demo's fault dials — the ONLY mutable chaos state in this app.
//
// WHERE CHAOS LIVES, AND WHY IT LIVES THERE. Every fault this demo injects is DEMO-SIDE
// INTERPOSITION: the wrappers beside this file implement the platform's own interfaces
// (`KVNamespace`, `DurableObjectNamespace`) and sit between the real binding and the library. The
// library has no fault-injection surface, no test hook, and no chaos seam — and none may be added
// for this demo's benefit. That is not an aesthetic preference: a library that ships a way to make
// itself fail ships a way for a caller to make it fail, and every such seam is a branch the real
// deployment carries forever to serve a demo that runs for ten minutes. The faults belong to the
// harness, so they live in the harness.
//
// These dials are therefore what a real outage looks like FROM THE BINDING'S SIDE — a KV that
// rejects, a socket that will not open — and nothing else. No dial reaches past the binding to tell
// the cache what to conclude; the cache's conclusions are the thing being demonstrated, and a demo
// that could dictate them would be demonstrating itself.

/** The faults the demo's `KVNamespace` wrapper can apply to the real binding. */
interface KvFaults {
  /** Reject every read (`get`/`getWithMetadata`) — a KV that is there but answering nothing. */
  failReads: boolean;
  /** Reject every write (`put`/`delete`). */
  failWrites: boolean;
  /**
   * Reject reads of keys under this prefix, passing every other key through. This is how a
   * Coordinator outage is modelled at the KV end: its mirror keys stop being readable while the
   * cache's own entries stay perfectly fine — which is exactly the shape of the real failure, and
   * exactly why "the store is up" is not the same claim as "the registry is up".
   */
  unreadablePrefix: string | null;
}

// A LATENCY DIAL USED TO LIVE HERE, AND IT IS GONE ON PURPOSE — recorded because a deletion nobody
// explains gets re-added by the next person who thinks of it. It was `latencyMs`: the wrapper slept
// on it before every operation. No scene ever armed it, nothing on any page could set it, and no
// test drove it — it was reachable only by editing the source, which makes it not a dial but a
// comment with a `setTimeout` in it. It was also modelling the wrong fault for every scene this demo
// actually has: a SLOW store is a different subject from a DEAD one, and the latency the flagship
// scene cares about is the origin refill's — the price the miss-class posture makes the caller pay
// — which the app measures at its own edge off the real read rather than off a number it injected.
// A dial that can only be exercised by a test written to exercise it is a dial that proves the test
// works. Deleted rather than wired to an invented scene.

/** The fault the demo's Coordinator wrapper can apply. Connection loss is the ONLY one, because per
 * the bus transport's design it is the only loss mode that exists: the socket is open or it is not,
 * and a transport reports rather than retries. A dial wrapper that could drop individual frames
 * would be modelling a failure the real system cannot have. */
interface BusFaults {
  /** Refuse the WebSocket upgrade — the dial does not connect. */
  dialRefused: boolean;
}

/** Every dial the demo owns, in one mutable record the scenes write and the wrappers read. */
export interface FaultDials {
  readonly kv: KvFaults;
  readonly bus: BusFaults;
}

// ── What the wrappers actually DID ──────────────────────────────────────────
//
// A scene whose fault intercepted nothing demonstrated nothing — and on screen it looks exactly
// like a system that shrugged the fault off, which is the most dishonest picture this demo could
// paint. The only thing that tells those two apart is a count, so the wrappers keep one. These
// records live here, beside the dials and clear of the platform's types, so the narration can read
// them without dragging the Workers runtime into the app's own program.

/** What the KV wrapper did to the traffic that reached it. */
export interface KvFaultStats {
  readsFailed: number;
  readsPassed: number;
  writesFailed: number;
  writesPassed: number;
}

/** What the Coordinator wrapper did to the dials that reached it. */
export interface BusFaultStats {
  dialsRefused: number;
  dialsPassed: number;
}

/** A fresh, all-zero KV stats record. */
export function emptyKvStats(): KvFaultStats {
  return { readsFailed: 0, readsPassed: 0, writesFailed: 0, writesPassed: 0 };
}

/** A fresh, all-zero bus stats record. */
export function emptyBusStats(): BusFaultStats {
  return { dialsPassed: 0, dialsRefused: 0 };
}

/** A fresh set of dials, all faults off — the healthy system. */
export function noFaults(): FaultDials {
  return {
    bus: { dialRefused: false },
    kv: {
      failReads: false,
      failWrites: false,
      unreadablePrefix: null,
    },
  };
}

/** Turn every dial off, in place (the scenes' `heal` step). */
export function clearFaults(dials: FaultDials): void {
  dials.kv.failReads = false;
  dials.kv.failWrites = false;
  dials.kv.unreadablePrefix = null;
  dials.bus.dialRefused = false;
}

/**
 * Zero the counters, in place — the OTHER half of healing, and the half that was missing.
 *
 * Healing used to clear the dials and nothing else, which produced a page that contradicted itself
 * in three places at once: it said "Not armed. This is the healthy system", and directly underneath
 * reported six KV reads rejected as "proof the fault reached the binding", and derived
 * `registry_degraded` from an event log that still held every swallowed tick the fault had caused.
 * Worse, the judgment LATCHED: `registry_degraded` is derived from the log, the log was never
 * cleared, so once a fault had been armed the page claimed a degraded registry forever — including
 * on a system that had been healthy for an hour.
 *
 * The counters are evidence for a CLAIM the page makes in the present tense — that the fault reached
 * the binding — so they are scoped to the episode that claim is about. Heal ends the episode. What
 * `heal` restores is not merely that the dials are off: it is that the page is once again a page
 * about a healthy system, and that is a statement about every number on it.
 */
export function clearKvStats(stats: KvFaultStats): void {
  stats.readsFailed = 0;
  stats.readsPassed = 0;
  stats.writesFailed = 0;
  stats.writesPassed = 0;
}

/** Zero the bus counters, in place — see `clearKvStats`. */
export function clearBusStats(stats: BusFaultStats): void {
  stats.dialsPassed = 0;
  stats.dialsRefused = 0;
}
