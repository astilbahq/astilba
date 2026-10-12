// Telemetry (§10). A hosted sink pseudonymizes every string field a kernel emits:
// key and tag references arrive as per-project HMAC-salted hashes (MI4) — so a short
// or guessable identifier can't be recovered from the hash — and free-form detail is
// reduced to error codes plus hashed references. Invariant 15 checks that a hosted
// sink never receives a raw key, tag, or detail.

/**
 * The telemetry event-name CATALOG (§10, CACHE-21) — the single, kernel-hosted registry of every
 * event `type` this library emits, the same shape as the exported error classes. Each member is an
 * event NAME; the value equals the key so a producer can reference a name as `TELEMETRY_EVENTS.<name>`
 * (a literal type, refactor-safe) while `TelemetryEventName` (below) is the union of them all.
 *
 * The names are grouped by their emitting LAYER (kernel / L1 driver / react-router adapter /
 * cloudflare adapter) — the annotation the charter requires. The machine-checked layer map (which
 * fails to compile if a member is unannotated) lives in the catalog's exhaustiveness tooth in the
 * test lane.
 *
 * GROWTH: adding a name is ADDITIVE and consumer-SAFE (a sink RECEIVES events, so a producer that
 * emits a wider vocabulary hands a consumer a value from a set it already types against — narrowing,
 * not widening, at the receiver) — so a new name is a MINOR-version change. Removing or renaming one
 * is breaking.
 */
export const TELEMETRY_EVENTS = {
  // ── kernel — emitTelemetry (cache.ts): the write-back + singleflight counters ──
  writeback_throttled: "writeback_throttled",
  l2_write_error: "l2_write_error",
  singleflight_option_mismatch: "singleflight_option_mismatch",
  // A classified store READ failure absorbed into a miss (E13). A RAW fact, not a judgment: it
  // states what happened at one instant — this read failed and was suppressed — with no threshold
  // and no accumulated state behind it, so it emits at the moment of suppression and is enrolled
  // LIVE, not reserved. An UNCLASSIFIED read failure emits nothing: it propagates to the caller,
  // which is already the loudest channel there is, and naming it "suppressed" would be false.
  store_read_suppressed: "store_read_suppressed",
  // An E7 suppression: a 404 that a stored `val` the letter protects turned away (§5.1 step 5, E7,
  // E31). It fires at EITHER of the two seats the suppression can be rendered at — the serve path's
  // epistemic check, where the caller holds the protected copy and serves it, and the write layer's
  // kind-aware guard, where the write is refused outright — because E31 attaches the consequence to
  // whichever seat renders it, one event per suppression. Do NOT read it as a refused write-back
  // alone: at the serve seat there is no write-back to refuse, and reading it that way once left the
  // commoner of the two cases emitting nothing at all.
  //
  // A RAW fact on the same terms as `store_read_suppressed`: it states what happened at one instant,
  // with no threshold and no accumulated state behind it, so it emits at the moment of suppression
  // and is enrolled LIVE. It is the one channel that reports a 404 the caller asked to cache and the
  // kernel declined to; without it the decline is invisible, and a 404 that never becomes a cached
  // negative looks exactly like a 404 nobody asked to cache.
  neg_suppressed: "neg_suppressed",
  // ── kernel L1 driver — memory() (drivers.ts, MI12): a private entry left L1 ──
  private_evicted: "private_evicted",
  // ── react-router adapter — poll driver (poll-driver.ts, §4.2): swallowed tick ──
  poll_tick_failed: "poll_tick_failed",
  // ── react-router adapter — middleware (middleware.ts, §7.2/E23): demoted response ──
  l3_ineligible: "l3_ineligible",
  // ── cloudflare adapter — coordinator (coordinator.ts, E19): journaled-id mismatch ──
  regid_divergence: "regid_divergence",
  // ── kernel — the reader (invalidation.ts, E28): a lazy bus dial did not establish ──
  // A RAW fact on the same terms as `store_read_suppressed`: it states what happened at one
  // instant — this dial did not come up — with no threshold and no accumulated state behind it. It
  // is BOUNDED without needing one: a dial is attempted only when a tick finds one due, so the
  // event's rate is the tick's rate, not the read's. It exists because the alternative is silence,
  // and a channel that never came up is indistinguishable from a quiet healthy one to everything
  // except this event.
  bus_dial_failed: "bus_dial_failed",
  // ── kernel — the read path (cache.ts, E28/§1): a strong read served eventual instead ──
  // Also a RAW fact: this call, at this instant, could not reach the registry and took the eventual
  // posture because the caller opted into it. Carries a reason CLASS only, never the error's own
  // words. A degrade the caller asked for is still a degrade the operator must be able to see —
  // a strong read that answers with eventual's guarantee and says nothing has misreported its own
  // health to the one caller who asked for the stronger answer.
  strong_degraded: "strong_degraded",
  // ── RESERVED (C2 degraded-mode names) — enrolled in the vocabulary, no emit site yet ──
  // A raw event states a fact and may emit the instant the fact occurs (`poll_tick_failed`,
  // `private_evicted`). A DERIVED event states a judgment over accumulated state — these two are
  // judgments (thresholds over consecutive failures, backoff, elapsed silence) — and a judgment
  // has no legitimate emit point until a threshold is defined AND something acts on it. That first
  // consumer is the demo narration; its emit semantics are defined there. Enrolled here so the
  // vocabulary is closed; `poll_tick_failed` stays a raw fact, unchanged.
  registry_degraded: "registry_degraded",
  state_stale: "state_stale",
} as const;

/** The closed vocabulary of telemetry event names (§10, CACHE-21). `TelemetryEvent.type` is this
 * union, and `safeEmit` accepts only members of it — an off-catalog emit is a COMPILE error. */
export type TelemetryEventName =
  (typeof TELEMETRY_EVENTS)[keyof typeof TELEMETRY_EVENTS];

/**
 * The reason CLASS a shared-cache-ineligible response reports in its `l3_ineligible` payload (§7.2,
 * E23). These are REASON values carried on ONE event, never standalone event types. The middleware
 * is the single point that maps the kernel's `L3Ineligibility` union to these snake-case classes;
 * the payload names the class and nothing that identifies the poisoning dependency (that is
 * `explain()`'s job, C5).
 */
export type L3IneligibleReason =
  | "budget"
  | "late_tag"
  | "scope"
  | "scope_unreadable";

/**
 * The reason CLASS a `strong_degraded` payload reports (§1, E28) — why a strong read could not
 * live-check and served under eventual's posture instead. A CLASS, never the error's own words: the
 * registry's failure text is arbitrary app/platform detail (it can carry a URL, an id, a stack), and
 * this event's whole job is to be safe to point a plain sink at. `registry_unreachable` is the only
 * member today; it is a union rather than a bare literal so a second cause arrives as a widened
 * payload the sink already types against, not as a new event name.
 */
export type StrongDegradedReason = "registry_unreachable";

/**
 * A telemetry event. `type` is a CATALOG member (§10, CACHE-21); additional fields are
 * event-specific. Hosted sinks pseudonymize all string fields (invariant 15, MI4). The `type`
 * narrowing from a bare `string` is consumer-safe by variance: a sink is a RECEIVER, so an app
 * typed against the older `string` still accepts every catalog member.
 */
export interface TelemetryEvent {
  readonly type: TelemetryEventName;
  readonly [field: string]: unknown;
}

/** A telemetry sink (§2 `telemetry`). Receives events as they are emitted. */
export type TelemetrySink = (event: TelemetryEvent) => void;

/**
 * The hook `safeEmit` calls when it SWALLOWS an app-supplied sink's failure (§10, CACHE-21). It is
 * INJECTABLE — passed in at the emit site, never a module global — so "observed exactly once" is
 * testable with no module-latch order-coupling: two instances each carry their own hook and never
 * cross-talk. It receives the swallowed error, and is itself the LAST link in the swallow chain, so
 * if it throws that too is swallowed (safeEmit's never-throws contract is total).
 */
export type SinkErrorHook = (error: unknown) => void;

/**
 * Hosted telemetry configuration (§10, MI4). When `hosted` is set, the kernel pseudonymizes every
 * emitted string field before delivery — key/tag references become per-project HMAC-salted hashes
 * (never raw), and `detail` becomes error codes + hashed refs. `salt` is the per-project HMAC salt
 * that keeps short or guessable identifiers from being reversed out of the hash. `onSinkError` (see
 * `SinkErrorHook`) is the injectable broken-sink hook `safeEmit` calls on a swallowed sink failure.
 * Invariant 15.
 */
export interface TelemetryConfig {
  readonly sink: TelemetrySink;
  readonly hosted?: boolean;
  readonly salt?: string;
  readonly onSinkError?: SinkErrorHook;
}

/**
 * The app's sink as the PLATFORM actually calls it. `TelemetrySink` is declared `(event) => void`,
 * but a `void` return type accepts ANY return value — so an `async` sink type-checks against it and
 * hands back a promise. Reading the return through this widened view (no cast: `void` is assignable
 * to `unknown`) is what lets `safeEmit` ADOPT a rejecting thenable instead of letting it float.
 */
type SinkAsCalled = (event: TelemetryEvent) => unknown;

/** Report a swallowed sink failure to the injected hook, itself guarded. The hook is app code at the
 * very end of the swallow chain, so its own failure has no meta-channel to report to — swallowed, so
 * `safeEmit` cannot throw or float through it. */
function reportSinkError(
  hook: SinkErrorHook | undefined,
  error: unknown
): void {
  if (hook === undefined) {
    return;
  }
  // The hook is arbitrary app code (like the sink) — a `void` return type accepts an `async`
  // hook, so it can both THROW synchronously and REJECT asynchronously. Guard BOTH the same way
  // `safeEmit` guards the sink: sync throw → `try`/`catch`; rejecting thenable → ADOPT it
  // (`Promise.resolve(...).catch`) so it can never float as an unhandled rejection. The hook is
  // the last link in the swallow chain, so its own failure has nowhere to report — swallowed.
  const called: (value: unknown) => unknown = hook;
  try {
    // oxlint-disable-next-line promise/prefer-await-to-then, promise/prefer-await-to-callbacks -- adopt-don't-await, identical to the sink handling in `safeEmit`: the hook is the last link, its rejection is swallowed with nowhere to route.
    void Promise.resolve(called(error)).catch(() => {
      // The hook's own async failure; swallowed (last link, no meta-channel).
    });
  } catch {
    // A hook that threw SYNCHRONOUSLY; swallowed for the same reason.
  }
}

/**
 * Emit ONE event through an app-supplied sink so that NOTHING it does can escape (§10, CACHE-21;
 * the swallow-wrap extracted from C4-b's poll driver and made shared). A sink is arbitrary app code;
 * both of its failure modes are neutralized here:
 *
 *   • a SYNCHRONOUS throw   → the `try`/`catch` (without it the caller's own path would throw);
 *   • a REJECTING thenable  → the returned value is ADOPTED (`Promise.resolve(...).catch(...)`) so a
 *     rejecting `async` sink can never float out as an unhandled rejection.
 *
 * On either failure the injected `onSinkError` hook is called exactly once for that emit. `safeEmit`
 * NEVER throws and returns nothing the caller must await (adopt-don't-await: it must not inherit
 * app-code latency on a background or response path).
 *
 * The TYPE is the structural tooth: `event` is a `TelemetryEvent`, whose `type` is the CATALOG
 * union — so an off-catalog event name is a COMPILE error at the call site, never a grep finding.
 */
export function safeEmit(
  sink: TelemetrySink | undefined,
  event: TelemetryEvent,
  onSinkError?: SinkErrorHook
): void {
  if (sink === undefined) {
    return;
  }
  const called: SinkAsCalled = sink;
  try {
    // oxlint-disable-next-line promise/prefer-await-to-then, promise/prefer-await-to-callbacks -- the sink's promise is deliberately NOT awaited: safeEmit is synchronous and must not inherit app-code latency on a background/response path. `Promise.resolve(...).catch(...)` ADOPTS a rejection so it can never float unhandled — there is nothing left to await, and the inline handler only routes the swallowed error to the injected hook.
    void Promise.resolve(called(event)).catch((error: unknown) => {
      reportSinkError(onSinkError, error);
    });
  } catch (error: unknown) {
    // A sink that threw SYNCHRONOUSLY. Swallowed for the same reason as the adopted rejection above.
    reportSinkError(onSinkError, error);
  }
}
