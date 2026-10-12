// The bounds this deployment DOCUMENTS (C5-c) — the right-hand column of every narration.
//
// READ THIS BEFORE READING A NUMBER OFF THE SCREEN. Everything here is a PERMISSION, not a promise.
// "Suspicion raises after 90s of observed silence" means the reader is allowed to take that long to
// notice, not that it will notice in that time — and this demo never asserts that a measurement
// satisfied one of these. There is no SLO on this page. There is no SLO anywhere in this app. A
// demo that asserted a latency or consistency guarantee would be a demo that goes red on a slow
// afternoon and teaches its operator that red means nothing; worse, it would be making a promise
// the library's own documentation is careful not to make. What this app does instead is print what
// happened next to what was permitted, and let the two disagree in public if they disagree.
//
// The measured distributions behind these numbers are not ours to publish: they come from deployed
// probes, and until they exist the honest posture is that these are the bounds we chose, not the
// behaviour we achieved. Numbers first, promises second.

import { TICK_MIN_INTERVAL_MS } from "@astilba/cache/react-router";

/**
 * THE HEARTBEAT NUMBER — 30s — which this demo owns at BOTH ends and matches on purpose.
 *
 * It has one value and two deployment surfaces, and nothing in the system makes them agree:
 *
 *   • the COORDINATOR end is `REGISTRY_HEARTBEAT_MS` in wrangler `vars` (see wrangler.jsonc). A
 *     Worker cannot write a Durable Object's env, so deploy-time config is that end's only seed.
 *   • the READER end is `defaults.heartbeatInterval`, which this app passes explicitly rather than
 *     inheriting, so the number is written down once, here, and spent twice.
 *
 * That this demo hosts both ends in one script is packaging, not a mechanism — split the
 * Coordinator into its own Worker and only one of the two travels with it. The console prints both
 * ends and whether they agree, because "matched by construction" is exactly the kind of claim that
 * is comfortable, load-bearing, and false.
 *
 * A mismatch is not a bug in the safe direction: a reader with the SHORTER interval raises
 * suspicion spuriously, which costs live registry checks and never a stale serve. The reverse
 * merely raises late. Nothing enforces equality because enforcing it would trade a priced,
 * observable degradation for a new failure mode.
 */
export const HEARTBEAT_INTERVAL_MS = 30_000;

/** The multiple of the heartbeat interval at which OBSERVED silence raises suspicion (E5). The
 * library owns this rule; the demo restates it to have something to print in the documented column,
 * and prints it as a permission the reader has rather than a deadline it meets. */
const SUSPICION_SILENCE_MULTIPLE = 3;

/**
 * THE POLL CADENCE THIS DEMO SETS — 5s — AND IT IS NOT THE SHIPPED DEFAULT.
 *
 * The library's baseline is 60s. This app passes `maxSyncLag: '5s'` explicitly, and the page says so
 * wherever it prints a number derived from it, because a demo that quietly ran a faster deployment
 * than the one it documents would be showing you a rescue that the shipped configuration does not
 * make in that time.
 *
 * WHY IT IS TURNED DOWN. The poll carrier is what rescues a reader whose bus is dead — the whole
 * argument of the deaf-reader scene — and at the 60s baseline that rescue lands on the far side of
 * a minute of reloading, which nobody watches. Turning the cadence down does not change WHETHER the
 * carrier rescues the reader; it changes how long you have to sit there to see it. The mechanism on
 * camera is the shipped one.
 *
 * WHAT IT DOES NOT DO — the honest half. `maxSyncLag` is the reader's re-read cadence, which is ONE
 * of the two terms in the eventual bound: §1's honest bound is the cadence PLUS the flush window
 * PLUS `__state` KV propagation, and that last term is documented as up to 60 seconds or more with
 * no ceiling. Turning this dial down shortens the term this deployment controls and does nothing
 * whatever to the one it does not. So the rescue this page measures is a LOWER bound on a real
 * deployment's, and the number it prints is this local Miniflare's, where KV propagation is
 * effectively free — which is exactly why the page prints the measurement and not a promise.
 */
const MAX_SYNC_LAG_MS = 5000;

/** The library's own shipped baseline, restated so the page can print what it turned DOWN FROM.
 * Not read from the library: it is the value that applies when nobody passes `maxSyncLag`, and there
 * is no export that spells an absent option's default. That makes it a number this file is
 * responsible for keeping true, so it is named here rather than buried in a sentence. */
const SHIPPED_MAX_SYNC_LAG_MS = 60_000;

/** The posture this deployment runs under, and the bounds it therefore documents. */
export const POSTURE = {
  /** The heartbeat cadence, both ends (E5). */
  heartbeatIntervalMs: HEARTBEAT_INTERVAL_MS,
  /** The base cadence at which the reader re-reads the mirror pointer, jittered ±20% per poll —
   * THIS DEPLOYMENT'S, turned down from the shipped baseline; see `MAX_SYNC_LAG_MS`. */
  maxSyncLagMs: MAX_SYNC_LAG_MS,
  /** The shipped baseline this demo overrides, printed beside the one it runs. */
  shippedMaxSyncLagMs: SHIPPED_MAX_SYNC_LAG_MS,
  /** How long OBSERVED silence must run before the reader may raise suspicion (E5). Derivative and
   * raise-only: a live-but-slow writer resets the baseline, and a fast sampler cannot witness 90s of
   * silence in less than 90s. */
  suspicionAfterMs: SUSPICION_SILENCE_MULTIPLE * HEARTBEAT_INTERVAL_MS,
  /** The floor between request-piggybacked replication ticks — the real exported number, not a
   * restatement. A burst of requests costs at most one tick per second. */
  tickMinIntervalMs: TICK_MIN_INTERVAL_MS,
} as const;

/** The sentence every narration carries. It is not decoration. */
export const NO_SLO_CAVEAT =
  "These are bounds this deployment documents, not guarantees it makes. The demo prints what it measured beside what is permitted and asserts nothing about the gap: a single run on one machine is an anecdote, and the measured distributions come from deployed probes, not from this page.";

/**
 * THE LIFETIME NOTE — and it now says the opposite of what it used to say.
 *
 * This demo used to carry a caveat costing it its best scene: the cache was rebuilt on every
 * request, because the factory resolved the Coordinator namespace to a stub and HELD it, and a
 * Workers stub belongs to the request that minted it — so a cache outliving a request could not
 * touch its registry again. The reader was therefore newborn every request, had observed no
 * silence, and could never grow suspicious. The page said so, loudly, because the alternative was a
 * console showing a system serenely ignoring an outage.
 *
 * That is fixed (E28). The factory now holds the ADDRESS — a namespace and a name — and mints a
 * stub per use, inside whichever request is running. Construction performs no I/O, so the cache is
 * built ONCE at module scope (see `workers/app.ts`), one per isolate, which is the placement the
 * quickstart teaches. The reader accumulates state across requests: it has a channel state, an
 * applied epoch that advances, and silence it can actually observe.
 *
 * So the caveat that remains is a much smaller one, and it is about the RIG rather than the design:
 * the reader's ticks ride on requests (that is the carrier), so nothing is observed on a page nobody
 * loads. Detection latency is bounded below by the gap between reloads, not just by the threshold.
 */
export const LIFETIME_CAVEAT =
  "The cache on this page is built once per isolate, at module scope, and outlives every request — so the reader below is the same reader across reloads and its state accumulates. Its freshness ticks ride on requests, which is the honest residue: nothing is observed on a page nobody loads, so detection latency is bounded below by the gap between your reloads and not by the threshold alone. Reload to give it something to observe.";

/** Render a duration the way the console prints it. */
export function ms(value: number): string {
  return value >= 1000 ? `${(value / 1000).toFixed(1)}s` : `${value}ms`;
}
