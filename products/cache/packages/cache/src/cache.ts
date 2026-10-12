import { createHash, createHmac } from "node:crypto";

import { isStoreReadError, isStoreWriteError } from "./drivers";
import type {
  Bus,
  Cdn,
  CdnMode,
  Clock,
  Codec,
  Lock,
  LockHandle,
  Registry,
  Rng,
  Store,
  StoreReadError,
  StoreValue,
} from "./drivers";
import { DURATION_UNIT_MS } from "./duration";
import type { Duration, DurationUnit } from "./duration";
import type { Envelope, EnvelopeKind } from "./envelope";
import {
  FactorySettledError,
  FencedError,
  HttpError,
  InvalidTagError,
  notImplemented,
  RegistryUnavailableError,
  SkipSignalError,
} from "./errors";
import { isRetriableHttp } from "./http";
import { Invalidation } from "./invalidation";
import type { ChannelState, UnknownResolution } from "./invalidation";
import type { Consistency, Validity } from "./knowledge";
import { mirrorKeys } from "./registry/mirror-keys";
import { ReplicationPoller } from "./replication-poller";
import type { ReplicationPollerOptions } from "./replication-poller";
import { safeCodec } from "./safe-codec";
import { assertUserTag, isReservedTag, MAX_USER_TAGS } from "./tags";
import type { Tag } from "./tags";
import { safeEmit, TELEMETRY_EVENTS } from "./telemetry";
import type {
  TelemetryConfig,
  TelemetryEvent,
  TelemetrySink,
} from "./telemetry";

// Re-exported here because it is part of `ExplainReaderState`'s public shape (E28): a field a caller
// can read is a type a caller must be able to name. `invalidation.ts` is an internal module and does
// not publish itself; `cache.ts` is the door its public surface goes through.
export type { ChannelState } from "./invalidation";

// The public cache surface — §2 (createCache / verbs), §5 (getOrSet sequence),
// §6 (scopes), §7 (render-time collection), with errata E6/E9/E13/E14/E15.

// ── Scopes & tiers ──────────────────────────────────────────────────────────

/**
 * A scope claim (§6). `'public'` is an enforced shared-tier claim; `{tenant}` is
 * tenant scoping. Omitting `scope` is private-by-default (principal-derived,
 * `usr:` ⇒ L1-only).
 */
export type Scope = "public" | { readonly tenant: string };

/** The tier an entry was served from (§2.1 `tier`). */
export type Tier = "l1" | "l1.5" | "l2" | "origin" | "miss";

/** How fail-closed `unknown` resolves (§5, E9 adds `'error'`). */
export type UnknownPolicy = "registry-check" | "miss" | "error";

// ── Factory context ─────────────────────────────────────────────────────────

/** Envelope provenance exposed to a factory for the graced value (§2.1, N7). */
export interface GracedInfo {
  readonly value: unknown;
  readonly src: string;
  readonly v: number;
  readonly bornMs: number;
}

/**
 * An adapter-provided request object (§6, §7). In a public-scoped factory dev mode
 * wraps it in a Proxy for the frame, so any property read demotes the entry off the
 * shared tier (closures over raw request state are the boundary invariant 4 names).
 */
export type RequestContext = Readonly<Record<string, unknown>>;

/**
 * The factory context for `getOrSet` (§2.1). Deliberately has NO `skip()` — the
 * plain-`T` API cannot skip (M10; invariant 24). `reuseGraced()` is the typed
 * primitive for reusing stale bytes (E14) — never launder `graced.value` (an
 * `unknown`) to `T`.
 */
export interface FactoryCtx<T> {
  readonly signal: AbortSignal;
  readonly graced?: GracedInfo;
  /** The adapter request object — a guarded accessor under public scope (§6). */
  readonly request?: RequestContext;
  /** Throw a classified error; bare `fail()` is non-retriable (§2.1, N5). */
  fail(err?: unknown): never;
  /** Contribute a dependency tag; `l3:false` marks it droppable for shared caches (§7). */
  dependsOn(tag: Tag, options?: { readonly l3?: boolean }): void;
  setTags(tags: readonly Tag[]): void;
  setTtl(ttl: Duration): void;
  /** Reuse the graced value with an identity + provenance/codec check (E14). */
  reuseGraced(): T;
}

/**
 * The factory context for `getOrSetEntry` (§2.1). Adds `skip()` — reachable ONLY
 * through the entry form, with sound `never` typing so it cannot silently resolve
 * `undefined` typed as `T` (M10; invariant 24).
 */
export interface EntryFactoryCtx<T> extends FactoryCtx<T> {
  skip(): never;
}

// ── getOrSet / getOrSetEntry ────────────────────────────────────────────────

/** Options common to `getOrSet` and `getOrSetEntry` (§2.1). */
export interface GetOptions {
  readonly key: string;
  readonly tags?: readonly Tag[];
  /**
   * Declared time-to-live (§2.1). In v0.2 this is part of the singleflight join identity — two
   * concurrent same-key fills declaring different ttls are an options mismatch (§10) — but it is
   * NOT stored with the entry and does not drive expiry: validity is decided by epoch comparison,
   * never a wall clock (§1(3)), and the write path passes no residency hint to the stores. So an
   * entry outlives its declared ttl until a purge or store eviction removes it. Wiring ttl to
   * residency and to stored identity is the v0.4 ttl design slot, beside `FactoryCtx.setTtl`.
   */
  readonly ttl?: Duration;
  readonly grace?: Duration;
  readonly notFoundTtl?: Duration;
  readonly scope?: Scope;
  /**
   * Read consistency (§1). Part of the singleflight join identity: a `strong`
   * request never joins a non-strong fill (E6).
   */
  readonly consistency?: Consistency;
  /** Opt in to a cross-isolate lock; the fill carries `Envelope.fence` (§5.1/§5.2). */
  readonly lock?: boolean;
  /** The adapter request object for this call, surfaced as a guarded accessor (§6). */
  readonly request?: RequestContext;
}

/** `getOrSet` options — plain-`T` factory, no `skip` (§2.1). */
export interface GetOrSetOptions<T> extends GetOptions {
  readonly factory: (ctx: FactoryCtx<T>) => Promise<T>;
}

/** `getOrSetEntry` options — entry-form factory, `skip`-capable (§2.1, M10). */
export interface GetOrSetEntryOptions<T> extends GetOptions {
  readonly factory: (ctx: EntryFactoryCtx<T>) => Promise<T>;
}

/** The `getOrSetEntry` result (§2.1, E13 adds `durable`, E29 makes it optional). */
export interface CacheEntry<T> {
  readonly value: T | undefined;
  readonly skipped: boolean;
  readonly stale: boolean;
  readonly age: number;
  readonly tier: Tier;
  readonly servedOnError?: boolean;
  /**
   * What THIS serve can prove about the value's durability (E13, E29):
   *   - `true`  — the value is in the shared L2 tier (a fill that wrote it there, or a read that
   *               found it there: either way, proof).
   *   - `false` — the value is known NOT durable: the fill was confined to L1 (a `usr:` scope, a
   *               dev-mode guarded-accessor demotion), its L2 write-back was suppressed (E13),
   *               or E7's guard REFUSED the write outright — a `kind:'neg'` candidate over a
   *               stored val the letter protects. Only the last was never issued at all; `false`
   *               reports this turn's own write either way, because in every one of them the
   *               kernel positively knows no durable copy of what it tried to write exists.
   *   - ABSENT  — unknowable on this serve: a `pub`/`ten:` entry found in L1 carries no
   *               write-back record, and a serve does not consult L2 on an L1 hit (§5.1, §8).
   *
   * An absent field is a real answer, not a `false` in disguise. A truthiness test collapses it into
   * the conservative direction (not provably durable), which is a safe default; code that must
   * distinguish known-not-durable from unknowable compares against `false` explicitly.
   */
  readonly durable?: boolean;
}

// ── The verbs ───────────────────────────────────────────────────────────────

/** Best-effort match hint on a purge result (§6.3, E15 renamed from `matched`). */
export type MatchedHint = "yes" | "no-such-scope" | "unknown";

/** The result of `expire` / `delete` / `clear` (§2.2, E15). */
export interface PurgeResult {
  readonly epoch: number;
  /** Best-effort (§6.3); legal workflows must not branch on it (E15). */
  readonly matchedHint: MatchedHint;
  /** Durable + bus handoff + KV batch accepted. NOT the edge (§2.2). */
  flushed(options?: { readonly timeout?: Duration }): Promise<void>;
  /** CDN purge accepted (queue drained) — a distinct promise (MI9/MI10). */
  edgePurged(options?: { readonly timeout?: Duration }): Promise<void>;
}

/** `expire` selector — a tag (optionally scope-qualified) or a key (§2.2). */
export type ExpireSelector =
  | { readonly tag: Tag; readonly scope?: Scope }
  | { readonly key: string };

/** `delete` selector — like `expire`, plus a CDN mode (§2.2). */
export type DeleteSelector =
  | { readonly tag: Tag; readonly scope?: Scope; readonly cdn?: CdnMode }
  | { readonly key: string; readonly cdn?: CdnMode };

/** The explicit acknowledgement `expireAll` / `deleteAll` require (§2.2). */
export interface OriginLoadGuard {
  readonly iUnderstandTheOriginLoad: true;
}

// ── Render-time tag collection & L3 (§7, §7.2) ──────────────────────────────

/** Header-byte budget for shared-cache emission (§7.2: 16 KB / ~1,000-tag). */
export interface L3Budget {
  readonly maxBytes?: number;
  readonly maxTags?: number;
}

/**
 * Why a response is ineligible for shared HTTP caches (§7.2, §11.13, E23).
 *
 *   - `late-tag` / `budget`: the §7.2 timing + header-budget triggers.
 *   - `scope`: the render consumed a dependency whose stored scope is readable but
 *     NOT `pub` (a `usr:`/`ten:` entry) — per-principal content (E23).
 *   - `scope-unreadable`: a consumed dependency's stored scope could not be read at
 *     all (absent / malformed). It fails CLOSED to non-public: an unverifiable scope
 *     is treated exactly like a private one (E23).
 */
export type L3Ineligibility =
  | "late-tag"
  | "budget"
  | "scope"
  | "scope-unreadable";

/** The shared-cache emission decision at header commit (§7.2, E23). */
export interface L3Emission {
  /** `false` ⇒ `Cache-Control: private` / no `s-maxage` (§7.2). */
  readonly eligible: boolean;
  /** Cache-Tag values to emit; `__k`/`__ns` are never emitted (MI14). Always empty
   * on a scope-ineligible emission — a poisoned response emits no tag at all (E23). */
  readonly cacheTags: readonly string[];
  readonly ineligibleReason?: L3Ineligibility;
}

/**
 * Request-scoped render-time dependency-tag collector (§7). Cache hits and fills during
 * the request contribute tags AND their scope evidence automatically (that automatic
 * contribution is what makes the §7.2/E23 gate safe-by-default: a render cannot forget to
 * declare the private entry it just read). `commitHeaders` decides shared-HTTP-cache
 * eligibility at header-commit time: a non-public consumed dependency poisons the response
 * outright (E23), and beneath that gate the §7.2 timing/budget rules still apply — required
 * tags discovered late, or exceeding the L3 budget, make the response ineligible
 * (invariant 13, §7.2).
 */
export interface RenderCollector {
  dependsOn(tag: Tag, options?: { readonly l3?: boolean }): void;
  commitHeaders(budget?: L3Budget): L3Emission;
}

/** The `<scope>` literal of the one shared, principal-free storage class (§3, §6). It is
 * the ONLY value that keeps a response shared-cacheable under E23. */
const PUBLIC_SCOPE = "pub";

/**
 * The byte the emitted `Cache-Tag` list is joined on (§7.2). SAME-PACKAGE seam, deliberately
 * shared: `commitHeaders` measures the header budget against the JOINED string and the adapter
 * WRITES the joined string, so the two must agree on the separator by construction — a budget
 * measured on one separator and a header written with another is a budget measuring nothing.
 */
export const L3_TAG_SEPARATOR = ",";

/**
 * The scope evidence one consumed dependency contributes to the render collector
 * (§7.2, E23):
 *
 *   - `none`: no managed entry stands behind this tag (a bare `RenderCollector.dependsOn`
 *     from outside a fill). It contributes a tag and NO scope claim, so it can never
 *     poison — there is nothing to poison with.
 *   - `readable`: the scope literal, either read verbatim off the shared surface (a hit)
 *     or resolved by this kernel for this fill. Poisons unless it is exactly `pub`.
 *   - `unreadable`: the shared surface returned no readable scope for a HIT. Fails closed
 *     ⇒ non-public ⇒ poisons, under its own reason class. A FILL can never produce this:
 *     a fill's scope is COMPUTED here (`resolveScope`), never read back off a surface that
 *     might have lost it.
 */
type ScopeEvidence =
  | { readonly kind: "none" }
  | { readonly kind: "readable"; readonly literal: string }
  | { readonly kind: "unreadable" };

/** A dependency the render consumed — its tags, its scope evidence, and whether it is
 * droppable for shared caches (`l3:false`, §7). */
interface RecordedDependency {
  readonly tags: readonly string[];
  readonly scope: ScopeEvidence;
  readonly l3?: boolean;
}

/** The collector's INTERNAL writer — the scope-bearing half of `RenderCollector`, which the
 * public interface cannot carry (a public `dependsOn` has no entry behind it, so it has no
 * scope to declare). Reached only through the module-private map below. */
interface CollectorState {
  record(dependency: RecordedDependency): void;
  /** The per-dependency records consumed so far (§7, E23) — the source of `explain`'s request-scoped
   * attribution (E27 (5)). REQUEST-SCOPED by construction: it lives on the collector, which the
   * adapter opens per request and discards after it, so this is not the struck persisted dependency
   * graph — it dies with the render. Read-only view; `explain` only reads it. */
  readonly records: readonly RecordedDependency[];
}

/** The scope evidence a served HIT carries: its stored literal, or the fail-closed
 * `unreadable` class when the shared surface returned none (§7.2, E23). */
function hitScopeEvidence(hit: ReadHit<unknown>): ScopeEvidence {
  if (hit.scopeUnreadable || hit.rawScope === undefined) {
    return { kind: "unreadable" };
  }
  return { kind: "readable", literal: hit.rawScope };
}

/** The scope evidence a FILL carries: the literal this kernel just resolved for it (§6,
 * E17). Never `unreadable` — a computed scope cannot fail to be read. */
function fillScopeEvidence(literal: string): ScopeEvidence {
  return { kind: "readable", literal };
}

/** Reach the ambient render collector for the in-flight request, if the adapter bound one
 * (`bindRenderCollector`). Unbound ⇒ `undefined` ⇒ the kernel records nothing and no
 * emission is ever produced: a cache used WITHOUT the adapter fails closed by construction. */
type AmbientCollector = () => CollectorState | undefined;

/** Record one consumed dependency on the ambient collector, if there is one (§7, E23). */
function recordDependency(
  ambient: AmbientCollector | undefined,
  dependency: RecordedDependency
): void {
  ambient?.()?.record(dependency);
}

/** Record a SERVED stored entry as a consumed dependency (§7, E23): the tags it was stored
 * with, plus the scope evidence read off the surface it came from — `unreadable` when that
 * surface returned no scope at all, which poisons the response rather than guessing. A
 * `undefined` hit is a serve that never happened, and records nothing. */
function recordServedHit(
  ambient: AmbientCollector | undefined,
  hit: ReadHit<unknown> | undefined
): void {
  if (hit === undefined) {
    return;
  }
  recordDependency(ambient, {
    scope: hitScopeEvidence(hit),
    tags: hit.envelope.tags,
  });
}

/* References: §7, E27, §7.2, E23. */

export type ExplainScope =
  | { readonly kind: "readable"; readonly literal: string }
  | { readonly kind: "unreadable" };

/**
 * A stored entry's ttl as read from the surface (§7, E27 (1)), preserving the stored-vs-not-stored
 * distinction: the number the stored format carries, or `not-stored` where it carries none. The two
 * arms are structurally distinct and there is no sentinel — a stored ttl of `0` and a ttl the format
 * never wrote are different states, and only the store's own bytes decide which. The v0.2 format
 * persists no ttl (see the v0.4 ttl slot named beside `FactoryCtx.setTtl`), so the witness reports
 * `not-stored` today; a format that later carries one is reported through this same arm.
 */
export type ExplainTtl =
  | { readonly kind: "stored"; readonly seconds: number }
  | { readonly kind: "not-stored" };

/**
 * The scope evidence one recorded render dependency carries (§7, §7.2, E23) — the public projection
 * of the collector's per-dependency scope. Adds `none` (a bare `dependsOn` with no managed entry
 * behind it, which carries no scope claim) to the stored two-arm form.
 */
export type ExplainDependencyScope = { readonly kind: "none" } | ExplainScope;

/**
 * One render dependency `explain` attributes to the in-flight request (§7, E23) — its tags, its
 * scope evidence, and whether it is droppable for shared caches (`l3:false`). Request-scoped: only
 * surfaced while a render collector is bound (E27 (5)).
 */
export interface ExplainedDependency {
  readonly tags: readonly string[];
  readonly scope: ExplainDependencyScope;
  readonly l3?: boolean;
}

/**
 * The entry's stored identity as `explain` reads it from the store (§3, E23/E24, E27 (1)): the
 * stored tag set, the scope as stored (readable-vs-unreadable), the born epoch, the ttl as stored
 * (stored-vs-not-stored), and the entry kind. Every field is read off the stored bytes: a
 * reconstruction default the reader supplies is not stored identity, and reporting one as identity
 * would be the witness testifying to a value the store never carried.
 */
export interface ExplainIdentity {
  readonly tags: readonly string[];
  readonly scope: ExplainScope;
  readonly bornEpoch: number;
  readonly ttl: ExplainTtl;
  readonly kind: EnvelopeKind;
}

/**
 * The classification `explain` reaches and the epochs behind it (§4.1, E27 (2)): the reader's
 * current-knowledge verdict over the entry, plus the tag-knowledge watermarks the born epoch was
 * weighed against (the purge epochs behind a `stale`/`dead` verdict, `0` when nothing is known).
 * `unknown` stays `unknown` — the fail-closed narration is the point, not a gap.
 */
export interface ExplainVerdict {
  readonly validity: Validity;
  readonly softEpoch: number;
  readonly hardEpoch: number;
}

/**
 * The reader's own already-tracked state at the moment of the call (§4.1/§4.2, E27 (4), E28),
 * surfaced via read-only accessors — `explain` adds no tracking of its own.
 * `appliedEpoch`/`suspect`/`channel` come from the invalidation reader; `terminal` from its
 * replication shell. A registry-absent instance has no reader, so all four rest at their neutral
 * values.
 *
 * `channel` is E27-conformant on the letter that matters: the reader tracks it for its own use (the
 * lazy redial needs to know whether it has a channel), so surfacing it ADDS NO BOOKKEEPING — it
 * exposes a fact already on the object. The witness may speak; it may not touch cache state, and
 * reading this getter does not dial, subscribe, or record.
 *
 * It is here because `suspect` alone cannot answer the question an operator actually asks. A reader
 * whose bus never connected is NOT suspect — it has no gap, it has no channel — so a healthy-looking
 * `suspect: false` is exactly what a dead bus produces. `channel` is the field that tells those two
 * apart.
 */
export interface ExplainReaderState {
  readonly appliedEpoch: number;
  readonly suspect: boolean;
  readonly terminal: boolean;
  /** The live channel's state (E28) — `never-established` until the first `hello` ever arrives. */
  readonly channel: ChannelState;
}

/** Fields common to every arm of an `Explanation` (§7, E27) — what the witness knows regardless of
 * what the store said. `tier` is NOT here: it is an observation over the tiers, and a probe that
 * failed observed none, so the arm that could not read carries no tier to report. */
interface ExplanationBase {
  readonly key: string;
  readonly reader: ExplainReaderState;
  /** Request-scoped per-dependency attribution (§7, E23, E27 (5)) — present (possibly empty) only
   * while a render collector is bound to the in-flight request; ABSENT otherwise. It lives and dies
   * with the request. */
  readonly requestDependencies?: readonly ExplainedDependency[];
}

/**
 * What `explain(key)` reports (§7, E27) — three structurally distinct arms on one `kind`
 * discriminant, the same two-arm discipline `ExplainScope`/`ExplainTtl` already carry, extended to
 * the state the store itself can withhold. There is no sentinel, and no arm is reachable by reading
 * a field off another.
 *
 *   - `present`     — the entry was read: its stored identity and the reader's verdict, found in
 *                     `tier`.
 *   - `absent`      — every tier was read and none holds the key. A REPORTABLE state, not an error:
 *                     `explain` never throws for a missing key.
 *   - `read-failed` — a tier's read failed and was suppressed (E13), and no other tier held the key,
 *                     so whether it exists is UNKNOWN. Distinct from `absent` because the difference
 *                     is real: one is the store answering "nothing", the other is the store not
 *                     answering. Reporting the second as the first would be the witness laundering
 *                     an error into an answer — what E27's closing sentence forbids. It carries NO
 *                     `tier` and NO `identity`: a probe that could not read observed neither.
 *
 * The precedence is what each state can support: a found entry is FOUND however the other tiers
 * behaved; and a failed read outranks absence, because a tier that never answered cannot be counted
 * as one that answered "no". `explain` cannot refill, so E13's serving-path remedy does not transfer
 * — the honest report is the whole remedy available here.
 */
export type Explanation =
  | (ExplanationBase & {
      readonly kind: "present";
      /* References: E27. */

      readonly tier: Tier;
      readonly identity: ExplainIdentity;
      readonly verdict: ExplainVerdict;
    })
  | (ExplanationBase & { readonly kind: "absent"; readonly tier: "miss" })
  | (ExplanationBase & { readonly kind: "read-failed" });

/** The verdict for a registry-absent instance (§4.1, INV-8/27): with no watermark authority every
 * stored entry reads FRESH, exactly as the codec-only read path classifies it — there is nothing
 * that could purge it, so nothing is `stale`/`dead`/`unknown`. */
const FRESH_VERDICT: ExplainVerdict = {
  hardEpoch: 0,
  softEpoch: 0,
  validity: "fresh",
};

/**
 * The request-scoped per-dependency attribution `explain` surfaces (§7, E23, E27 (5)) — the bound
 * render collector's recorded dependencies, projected to the public shape. `undefined` when no
 * collector is bound (outside an adapter request frame): the attribution lives and dies with the
 * request, so its ABSENCE is itself the honest report (a witness reports what it knows; unbound ⇒
 * there is nothing to attribute).
 */
function explainAttribution(
  ambient: AmbientCollector | undefined
): readonly ExplainedDependency[] | undefined {
  const state = ambient?.();
  if (state === undefined) {
    return undefined;
  }
  return state.records.map((dependency): ExplainedDependency => ({
    scope: dependency.scope,
    tags: dependency.tags,
    ...(dependency.l3 === undefined ? {} : { l3: dependency.l3 }),
  }));
}

// ── Configuration ───────────────────────────────────────────────────────────

/** Instance defaults (§2). */
export interface CacheDefaults {
  readonly ttl?: Duration;
  readonly grace?: Duration;
  /** Registered with the Coordinator at bind (§4.5, M3, E11). */
  readonly maxEntryRetention?: Duration;
  readonly consistency?: Consistency;
  readonly unknownPolicy?: UnknownPolicy;
  readonly staleIfError?: (err: unknown) => boolean;
  readonly graceBackoff?: Duration;
  readonly maxSyncLag?: Duration;
  /** Decode-only codec compatibility for intentional migrations (E8). */
  readonly acceptCodecs?: readonly string[];
  /** Paid idle-heartbeat interval for `__state` liveness (E5). */
  readonly heartbeatInterval?: Duration;
  /** Strong opt-out: degrade to eventual instead of throwing (§1). */
  readonly onUnavailable?: "eventual";
  /**
   * `unknown` resolves to `'error'` — the read THROWS rather than refilling (E9/E28).
   *
   * SETTABLE HERE, not only at the top level, because `defaults` is the surface a composition
   * actually threads: `createWorkersCache` forwards `defaults` wholesale, so a flag that existed
   * only on `CacheConfig` could not be reached through any factory that wraps `createCache`. A
   * legal-embargo control that only the raw path can set is a control most callers do not have.
   *
   * It is an OVERRIDE, not a default: it outranks `unknownPolicy` — see `unknownResolution`.
   */
  readonly takedownSensitive?: boolean;
}

/* References: §2. */
export interface CacheConfig {
  readonly namespace: string;
  readonly clock: Clock;
  readonly rng: Rng;
  readonly l1?: Store;
  readonly l2?: Store;
  readonly registry?: Registry;
  readonly bus?: Bus;
  readonly cdn?: Cdn;
  /** Opt-in cross-isolate lock backend (§5.2). */
  readonly lock?: Lock;
  /** Pluggable value codec (§8, E8). Defaults to the library's safe-mode codec. */
  readonly codec?: Codec;
  readonly defaults?: CacheDefaults;
  /** A plain sink, or a hosted config that pseudonymizes every string field (§10, MI4). */
  readonly telemetry?: TelemetrySink | TelemetryConfig;
  /** `unknown` + registry-unreachable resolves to `'error'`, not `'miss'` (E9). */
  readonly takedownSensitive?: boolean;
  /**
   * Dev mode: a singleflight option mismatch throws + a guarded-accessor read in a
   * public factory throws; prod (`false`) runs a separate fill and emits a
   * `singleflight_option_mismatch` event instead (§2.1, §6).
   */
  readonly dev?: boolean;
}

// ── The cache facade ────────────────────────────────────────────────────────

/** The public cache instance (§2). */
export interface Cache {
  getOrSet<T>(options: GetOrSetOptions<T>): Promise<T>;
  getOrSetEntry<T>(options: GetOrSetEntryOptions<T>): Promise<CacheEntry<T>>;
  expire(selector: ExpireSelector): Promise<PurgeResult>;
  delete(selector: DeleteSelector): Promise<PurgeResult>;
  /** Namespace version bump + hard watermark on the reserved `__ns` tag (§2.2, MI1). */
  clear(): Promise<PurgeResult>;
  expireAll(guard: OriginLoadGuard): Promise<PurgeResult>;
  deleteAll(guard: OriginLoadGuard): Promise<PurgeResult>;
  /** Open a request-scoped render-time tag collector (§7). */
  collect(): RenderCollector;
  /* References: §7, E27, §7.2, E23. */

  explain(key: string): Promise<Explanation>;
}

// ── getOrSet fill/read + dual-epoch invalidation (§5, §3, §4, §8, E1/E3/E8) ──
//
// Cluster E, stage E1 — the dual-epoch invalidation core. Extends the Cluster C
// codec fill/read seam with:
//   • bornEpoch minting at fill start (§3, §5.0);
//   • the §4.1 validity table (fresh/stale/dead/unknown) via `Invalidation`;
//   • the §5.0/E1 write-back fence (a live registry read catches a mid-fill purge);
//   • born-monotone (bornEpoch, fence) write-back arbitration (§5.0, E3);
//   • `expire`/`delete` verbs + bus propagation/resync (§4.2, E4).
//
// The read seam is now:
//   read bytes → codecId-check → decode → validate(envelope) → serve | stale | refill
// (codecId is checked BEFORE decode per the PR-#4 / CACHE-9 reorder, so a codec
// swap fails closed to a miss without ever invoking a foreign decoder — E8.)
//
// In the registry-ABSENT config (codec.test.ts) no `Invalidation` is built, so the
// validation seam returns FRESH for every codec-clean entry exactly as before —
// INV-8/27 are untouched.

/** Per-instance namespace-version counter (§2.2, §3, MI1). `clear()` bumps it so
 * every canonical key minted AFTER the bump is unreachable from a pre-clear key —
 * a second, independent defense alongside the `__ns` hard watermark (INV-23). Held
 * as a small mutable cell (not a plain closure variable) so it can be threaded
 * through the free functions below exactly like `Invalidation`. */
class NsVersion {
  #version = 0;

  get current(): number {
    return this.#version;
  }

  bump(): void {
    this.#version += 1;
  }
}

/** Canonical key form `<ns-version>:<ns>:<scope>:<user-key>` (§3). */
function canonicalKey(
  nsVersion: number,
  namespace: string,
  scope: string,
  userKey: string
): string {
  return `${nsVersion}:${namespace}:${scope}:${userKey}`;
}

/**
 * The adapter-visible identity fields a default-scope request can carry, in
 * precedence order (§6 principal-derived scoping; F34 adapter-visible
 * identity / request-identity-header values). `userId` is the per-user
 * principal the RR/Workers adapter surfaces; a request that carries only a
 * `tenant` still bears adapter-visible identity, so it too derives a principal.
 * A request with none of these carries no derivable principal (E17 rule 3).
 */
const PRINCIPAL_FIELDS = ["userId", "tenant"] as const;

/**
 * Derive the adapter-visible principal from a request (§6, E17 rule 2). Returns
 * the first present identity field's value keyed by its field name, or `undefined`
 * when the request carries no recognized identity (no context / null principal ⇒
 * E17 rule 3, the shared `pub` class). Adapter-visible identity is a primitive
 * (a userId/tenant string, a header value) — a non-primitive field value is not a
 * clean principal and is skipped. A read here goes through the caller-supplied
 * request object directly (NOT the dev-mode guarded Proxy) — principal derivation
 * is the kernel's own inspection, not a public-factory closure, so it must never
 * itself trip the guarded-accessor demotion (§6, INV-4 test 2).
 */
function derivePrincipal(
  request: RequestContext | undefined
): string | undefined {
  if (request === undefined) {
    return undefined;
  }
  for (const field of PRINCIPAL_FIELDS) {
    const value = request[field];
    if (
      typeof value === "string" ||
      typeof value === "number" ||
      typeof value === "boolean" ||
      typeof value === "bigint"
    ) {
      // `field:` prefixes the value so two distinct identity fields never collide
      // to the same principal (a `userId` of "acme" ≠ a `tenant` of "acme").
      return `${field}:${value.toString()}`;
    }
  }
  return undefined;
}

/* References: §3, §6, E17. */

function hashPrincipal(principal: string): string {
  return createHash("sha256").update(principal).digest("hex").slice(0, 32);
}

/* References: §6, E17, §3, §2.1, INV-4, INV-20, INV-8, E1. */

interface ScopeResolution {
  /** The `<scope>` segment of the canonical key (§3). */
  readonly literal: string;
  /** Confined to isolate-local L1 — never written to the shared L2 tier. */
  readonly l1Only: boolean;
}

/** The scope inputs a resolution reads — the declared `scope` and the request the
 * principal derives from (§6). A key-selector purge supplies neither (⇒ `pub`). */
type ScopeInput = Pick<GetOptions, "scope" | "request">;

/**
 * The `<scope>` literal prefix of the per-principal storage class (§3, §6, E17) — the ONE
 * class `resolveScope` confines to the isolate-local L1. Shared by both directions so they
 * cannot drift: the MINT below builds a literal with it, and `storedScopeIsL1Only` reads a
 * STORED literal back through it. A serve derives confinement from the identity the entry was
 * stored under, so the two sides must agree on one prefix by construction.
 */
const PRINCIPAL_SCOPE_PREFIX = "usr:";

function resolveScope(input: ScopeInput): ScopeResolution {
  const { scope } = input;
  if (scope === "public") {
    return { l1Only: false, literal: "pub" };
  }
  if (scope !== undefined && typeof scope === "object") {
    // §3, E17 addendum: `ten:<h>` uses the same 128-bit SHA-256 truncation as
    // `usr:<h>`/`__k:<h>` — never the raw tenant literal (a pre-v0.1 deviation
    // the addendum flags to correct before the keyspace is persisted).
    return { l1Only: false, literal: `ten:${hashPrincipal(scope.tenant)}` };
  }
  const principal = derivePrincipal(input.request);
  if (principal !== undefined) {
    return {
      l1Only: true,
      literal: `${PRINCIPAL_SCOPE_PREFIX}${hashPrincipal(principal)}`,
    };
  }
  return { l1Only: false, literal: "pub" };
}

/**
 * Whether a STORED `<scope>` literal names the L1-confined class (§6, E17) — the read-back twin
 * of `resolveScope`'s `l1Only`, over a literal rather than the call inputs a serve no longer has.
 * `pub`/`ten:` are shared-tier classes; only `usr:<hash>` is confined.
 *
 * NOTE the asymmetry this CANNOT see, by construction: a `pub` fill DEMOTED at fill time by the
 * dev-mode guarded-accessor Proxy stores the `pub` literal, so its confinement leaves no trace in
 * the stored identity. Such an entry reads back as `pub` here, and a serve reports its durability
 * ABSENT (E29) — unknowable, which is the honest answer, rather than a `true` the literal cannot
 * support.
 */
function storedScopeIsL1Only(literal: string): boolean {
  return literal.startsWith(PRINCIPAL_SCOPE_PREFIX);
}

/** The `<scope>` literal alone (§3) — for the contexts (canonical key, join
 * identity, key-selector purge) that need only the string, not the tier policy. */
function scopeLiteral(input: ScopeInput = {}): string {
  return resolveScope(input).literal;
}

// ── Telemetry emission (§10, MI4) ────────────────────────────────────────────

/** Normalize the config's `telemetry` (a bare sink function, a config object, or
 * absent) to `{ sink, hosted?, salt?, onSinkError? }`. A bare function carries no
 * hosted/salt/onSinkError; the config-object form passes them through unchanged. */
function telemetryOf(config: CacheConfig): TelemetryConfig | undefined {
  const { telemetry } = config;
  if (telemetry === undefined) {
    return undefined;
  }
  if (typeof telemetry === "function") {
    return { sink: telemetry };
  }
  return telemetry;
}

/**
 * Pseudonymize one string field for a HOSTED sink (§10, MI4): a per-project
 * HMAC-salted hash, so a short or low-entropy identifier (a key or a tag) is not
 * offline-invertible and never travels raw. The salt is the per-project secret
 * (`hostedTelemetry(salt)`), so holding key+tag fixed and changing ONLY the salt
 * changes the ref — and the ref tracks its own input, so a key ref moves iff the
 * key moves and a tag ref moves iff the tag moves (INV-15). HMAC (not a bare hash)
 * so the salt genuinely keys the digest per project.
 */
function saltedRef(salt: string, value: string): string {
  return createHmac("sha256", salt).update(value).digest("hex").slice(0, 32);
}

/** Redact one event field for a hosted sink: HMAC-salt every string (per-field,
 * including inside a string array — key ref, tag refs), leave non-string scalars
 * intact. `type` (the event NAME, not sensitive) is redacted by the caller's field
 * filter, never here. A field-total salted pseudonymization (§10, MI4) — not
 * anonymization. */
function redactField(salt: string, value: unknown): unknown {
  if (typeof value === "string") {
    return saltedRef(salt, value);
  }
  if (Array.isArray(value)) {
    return value.map((element) => redactField(salt, element));
  }
  return value;
}

/**
 * Emit a telemetry event (§10, CACHE-21). A plain sink receives the event verbatim
 * (raw key/tag/detail — redaction is hosted-only by design, INV-15). A HOSTED sink
 * receives a field-total salted pseudonymization: every string field EXCEPT `type`
 * (the event name) is replaced by a per-project HMAC-salted hash of its own value,
 * so no raw key/tag/salt/detail ever leaves the process (MI4). A hosted config
 * missing a salt cannot pseudonymize, so it is suppressed rather than leaking raw
 * fields. The final delivery — plain or redacted — goes through `safeEmit`, so an
 * app sink that throws or rejects is swallowed (its `onSinkError` hook notified) and
 * can never surface on the kernel's own path (D2/D4).
 */
function emitTelemetry(config: CacheConfig, event: TelemetryEvent): void {
  const telemetry = telemetryOf(config);
  if (telemetry === undefined) {
    return;
  }
  if (telemetry.hosted !== true) {
    safeEmit(telemetry.sink, event, telemetry.onSinkError);
    return;
  }
  const { salt } = telemetry;
  if (salt === undefined) {
    // A hosted sink with no salt cannot produce salted refs; fail closed (emit
    // nothing) rather than forward raw identifiers to a hosted collector (MI4).
    return;
  }
  const redacted: Record<string, unknown> = { type: event.type };
  for (const [field, value] of Object.entries(event)) {
    if (field === "type") {
      continue;
    }
    redacted[field] = redactField(salt, value);
  }
  // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- `redacted` is built from a `TelemetryEvent`'s own entries with `type` preserved as a catalog member; it satisfies the `{ type: TelemetryEventName; [field]: unknown }` shape by construction.
  safeEmit(telemetry.sink, redacted as TelemetryEvent, telemetry.onSinkError);
}

/* References: §3, §2.2. */

function keyTag(canonical: string): string {
  const digest = createHash("sha256").update(canonical).digest("hex");
  return `__k:${digest.slice(0, 32)}`;
}

/** The reserved per-namespace tag (§3, §2.2, MI1): `__ns:<name>`. Every envelope
 * carries it so `clear()`'s hard purge rides the same epoch/bus/conservative
 * machinery as any other purge (MI1) — this is what lets a lagging isolate's read
 * fail closed instead of serving pre-clear data (INV-23). */
function nsTag(namespace: string): string {
  return `__ns:${namespace}`;
}

/** Validate the committed user-tag SET at the write boundary (§2.3, MI13). Two ceilings, both
 * per typed `InvalidTagError`, never `NotImplementedError`:
 *   - the `__`-prefix backstop, per tag — a forged reserved tag never reaches storage;
 *   - the count ceiling, over the whole set — at most `MAX_USER_TAGS` user tags.
 * The caller passes whatever set is being committed — the declared tags at `getOrSet`'s
 * boundary, or the settle-union of declared + `setTags` + `dependsOn` (E24) — so the ceiling
 * is route-independent by construction: one function, one set, one boundary. The reserved
 * `__k`/`__ns` tags are stamped separately and never counted here. The set is expected already
 * deduped (the ceiling counts distinct user tags, not raw call arity). */
function validateUserTags(tags: readonly string[] | undefined): void {
  if (tags === undefined) {
    return;
  }
  if (tags.length > MAX_USER_TAGS) {
    throw new InvalidTagError(
      tags[MAX_USER_TAGS] ?? "",
      `an entry may not carry more than ${MAX_USER_TAGS} user tags (§2.3, MI13); got ${tags.length}`
    );
  }
  for (const tag of tags) {
    assertUserTag(tag);
  }
}

/** §3 `tags` are sorted, deduped, and include `__k`/`__ns`. Dedupes FIRST, then validates the
 * distinct set (§2.3, MI13) — so the count ceiling measures distinct user tags, not raw call
 * arity — and returns just the caller-supplied set, sorted and deduped. The reserved
 * `__k`/`__ns` tags are appended separately by `envelopeTags` below, since they depend on the
 * canonical key / namespace, not just the call options. */
function sortedUserTags(tags: readonly Tag[] | undefined): string[] {
  if (tags === undefined) {
    return [];
  }
  const distinct = [...new Set<string>(tags)].toSorted();
  validateUserTags(distinct);
  return distinct;
}

/** The full stored-envelope tag set (§3): sorted user tags plus the reserved
 * `__k:<h>` (per-key digest of the canonical key) and `__ns:<name>` (per-namespace
 * watermark, MI1) tags. This is what rides the dual-epoch invalidation core, so
 * `expire({key})` / `clear()` invalidate through the same machinery as any tag
 * purge (§2.2). */
function envelopeTags(
  userTags: readonly string[],
  canonical: string,
  namespace: string
): string[] {
  return [...userTags, keyTag(canonical), nsTag(namespace)].toSorted();
}

/* References: §2.3, E24. */

interface TagAccumulator {
  isSettled(): boolean;
  markSettled(): void;
  addMembership(tag: Tag): void;
  author(tags: readonly Tag[]): void;
  settleUserTags(): string[];
}

function makeTagAccumulator(declared: readonly string[]): TagAccumulator {
  const membership: string[] = [];
  let authored: readonly string[] | undefined;
  let settled = false;
  return {
    isSettled(): boolean {
      return settled;
    },
    markSettled(): void {
      settled = true;
    },
    addMembership(tag: Tag): void {
      assertUserTag(tag); // per-call `__` backstop (§2.3, MI13, E24)
      membership.push(tag);
    },
    author(tags: readonly Tag[]): void {
      for (const tag of tags) {
        assertUserTag(tag); // per-call `__` backstop (§2.3, MI13, E24)
      }
      authored = [...tags]; // AUTHORS the declared set — replaces the base, does not touch membership.
    },
    settleUserTags(): string[] {
      const base = authored ?? declared;
      return [...new Set<string>([...base, ...membership])].toSorted();
    },
  };
}

/** Resolve the codec, falling back to the built-in safe-mode codec when the
 * config wires none (§8) — `world()` wires no codec, so the core needs a default
 * to store/read values at all. */
function codecOf(config: CacheConfig): Codec {
  return config.codec ?? safeCodec;
}

/**
 * The consistency this call reads at (§1): per-call, then the instance's declared default, then
 * §1's own (`eventual`).
 *
 * The middle step is what makes `CacheDefaults.consistency` a setting rather than a decoration:
 * without it, a caller who declared `defaults: { consistency: 'strong' }` got eventual reads and
 * nothing said so. The final fallback is unchanged and matches what every shipped default already
 * declares, so an instance that sets nothing reads exactly as it did before.
 */
function consistencyOf(
  config: CacheConfig,
  options: Pick<GetOptions, "consistency">
): Consistency {
  return options.consistency ?? config.defaults?.consistency ?? "eventual";
}

/**
 * How fail-closed `unknown` resolves for this instance (§5a, E9, E28).
 *
 * `takedownSensitive` is an OVERRIDE, and the precedence is the whole point (E28). It outranks an
 * explicitly-set `unknownPolicy` rather than merely filling in for an absent one — because as an
 * absent-policy default it was unreachable in practice: any composition that sets `unknownPolicy`
 * shadows it, and `createWorkersCache`'s own safe posture sets it, so the switch was dead on the
 * entire hero path. A legal-embargo control that a routine, recommended default silently disables is
 * worse than no control: it reads as protection and does nothing.
 *
 * Reading it as an override is also what the flag MEANS. `unknownPolicy` says how this namespace
 * would like unknowns resolved; `takedownSensitive` says this namespace is one where guessing is not
 * allowed. The second is a statement about the CONTENT, so it cannot be outranked by a preference.
 */
function unknownResolution(config: CacheConfig): UnknownResolution {
  // Either surface arms it (E28): the top-level flag, or `defaults` — the object every wrapping
  // factory threads. Neither is the "real" one; they are two doors to the same statement.
  if (
    config.takedownSensitive === true ||
    config.defaults?.takedownSensitive === true
  ) {
    return { armedBy: "takedownSensitive", kind: "error" };
  }
  // Otherwise the caller's explicit policy wins, and the §2 default is a live registry check.
  const policy = config.defaults?.unknownPolicy ?? "registry-check";
  return policy === "error"
    ? { armedBy: "unknownPolicy", kind: "error" }
    : policy;
}

/** The `staleIfError` predicate for this instance (§5.4) — the configured
 * override, or the default HTTP classifier (E2). */
function staleIfErrorOf(config: CacheConfig): (err: unknown) => boolean {
  return config.defaults?.staleIfError ?? isRetriableHttp;
}

/** Whether a thrown error is the §5.4/§5.1 `not_found` fact (a 404) — never masked,
 * never retriable, but eligible for negative caching when `notFoundTtl` is declared. */
function isNotFound(err: unknown): boolean {
  return err instanceof HttpError && err.status === 404;
}

/**
 * A decoded, validated stored entry ready to serve (§5), carrying the RAW stored scope
 * the §7.2/E23 shared-cacheability gate predicates on.
 *
 * WHY a second scope field when `envelope.scope` already exists: `envelopeFromStore`
 * applies the E17 read-path default, substituting the shared `pub` class whenever the
 * stored `scope` is absent or is not a string. Past that substitution `envelope.scope`
 * can no longer distinguish "stored as public" from "no scope stored at all" — and the
 * eligibility gate turns on exactly that distinction, because the second case is an entry
 * whose privacy this kernel cannot verify. So the gate reads the field AS STORED, before
 * the substitution: `rawScope` is the literal the surface actually returned, and
 * `scopeUnreadable` is true when it returned no readable string (absent / non-string). An
 * unreadable scope is treated as non-public and poisons the response (§7.2). Tier placement
 * is unaffected — `envelope.scope` still carries the E17 default, untouched.
 */
interface ReadHit<T> {
  readonly value: T;
  readonly envelope: Envelope<T>;
  readonly stale: boolean;
  /** The stored `scope` literal exactly as the surface returned it (§7.2, E23). */
  readonly rawScope?: string;
  /** The stored `scope` was absent or not a string — unreadable ⇒ non-public (E23). */
  readonly scopeUnreadable: boolean;
}

/**
 * A fill attempt's classification (§5.1 step 5 / §5.0):
 *   - `filled`: the factory succeeded and the fresh value was written through
 *     (carries `value`, possibly `undefined` for a stored neg).
 *   - `graced`: the factory threw a RETRIABLE error (§5.4/E2) and a stale `val`
 *     candidate is still servable after serve-time re-validation (§11.18) — an
 *     error-path serve, so `servedOnError` applies.
 *   - `neg-suppressed`: the factory threw a 404 while a servable `val` candidate
 *     exists, so the neg-write is suppressed (E7/INV-26) — the stale val is served
 *     but this is NOT an error-path serve (§5.4's `staleIfError`), so `servedOnError`
 *     does NOT apply.
 *   - `fenced`: fenced past the write-back guard (§5.0/MI6), or a retriable error
 *     whose graced candidate is no longer servable at serve time — never served.
 *   - `skipped`: the `getOrSetEntry` factory called `ctx.skip()` (§2.1, M10,
 *     invariant 24) — nothing is stored, nothing is served; the entry form
 *     surfaces `{ skipped: true, value: undefined }`.
 * A non-retriable error, or a retriable error with no candidate at all, rethrows
 * from `fillAndStore` (§11.3 — a failed factory never fabricates a value).
 */
/**
 * A stored entry a serve is drawn from, carried WITH the tier it was actually read from — the
 * evidence a serve reports under E29. The tier is part of the candidate because a `ReadHit` alone
 * cannot say where it came from, and a serve that has to guess is exactly what E29 forbids.
 */
interface ServedCandidate<T> {
  readonly hit: ReadHit<T>;
  readonly tier: Tier;
}

/** A successful fill: the factory ran and its value (or a `not_found` neg) was written through. */
interface FilledAttempt<T> {
  readonly kind: "filled";
  readonly value: T;
  /** The STORED envelope tag set this fill actually wrote (§3, E24): the settle-union of
   * declared + `setTags` + `dependsOn` for a value fill, the declared set for a neg-write.
   * Every SERVED-fill path draws its tag evidence from here — the singleflight-join / fill
   * record and L1 hydration — so a joiner and the CDN-cached copy carry the same purge handles
   * as the fill that created them, not the caller's declared subset. */
  readonly tags: readonly string[];
  /** The resolved durability of this write-back (§5.0, E13). `false` when the L2 write was
   * suppressed by a classified `throttled`/`unavailable` failure (the value still serves, the
   * durable copy did not land) or the fill was L1-only (§6, E17: a `usr:<hash>` scope, or a
   * public dev-mode factory that read a guarded request accessor — INV-4); `true` when it landed
   * durably. Required: a fill performed its own write-back and awaited the outcome, so it always
   * knows. */
  readonly durable: boolean;
  /** The `bornMs` this fill STAMPED into the envelope (§3), read off the injected clock once at
   * fill time. REQUIRED, and the single source of the value's birth instant: every later
   * consumer — the L2 write, the L1 hydration, the entry form's `age` — carries this number
   * rather than re-reading a clock. Birth is an event; a value has one (CACHE-39). */
  readonly bornMs: number;
  /** The `bornEpoch` actually STAMPED onto a successful VALUE fill (§3, §5.0). Carried so
   * L1 hydration re-uses the same epoch a §5.0 budget-3 retry re-stamped (E16/CACHE-26),
   * keeping L1 and L2 born-coherent. The `not_found` neg-write filled outcome (an error-path
   * serve, §5.1 step 5) carries none and falls back to the caller's outer floor. */
  readonly bornEpoch?: number;
  /** WHICH KIND of envelope this fill stored (§3): a successful factory's `val`, or the
   * `kind:'neg'` a declared `notFoundTtl` wrote for a 404 (§5.1 step 5). Carried EXPLICITLY,
   * and required at both construction sites, because the two outcomes share this shape while
   * differing in what a serve may do with them — and the difference was previously legible only
   * as `value === undefined`, which a factory whose success type includes `undefined` produces
   * too. The tier-write policy reads it (see `hydrateL1`); the serve paths do not need to, since
   * a neg fill's surfaced value is already `undefined` on the turn that wrote it. */
  readonly storedKind: "val" | "neg";
  /** Whether E7's kind-aware write guard REFUSED this fill's write (§5.1 step 5, E7/INV-26): a
   * stored `val` the letter protects sat under a `kind:'neg'` candidate, so the write was never
   * issued and the store is unchanged. Both this and E13's transient suppression
   * report `durable:false` — that field answers whether a durable copy of what this turn tried to
   * write now exists, and the answer is no in both — so `durable` does NOT tell them apart. THIS
   * field is the discriminator: E13's write was attempted and did not land, leaving something a
   * retry could still fix; an E7 refusal was never issued and leaves the store exactly as E7
   * requires. Read by the tier-write policy
   * (see `hydrateL1`), where EVICTION FOLLOWS THE WRITE'S FATE: a write that mutated nothing evicts
   * nothing. Always `false` on a `val` fill — recovering a value over a neg is the direction E7
   * leaves open, so a value write is never the thing this guards against. */
  readonly writeSuppressed: boolean;
}

/**
 * A serve of a STORED candidate rather than a fresh fill:
 *   - `graced`: the factory threw a RETRIABLE error (§5.4/E2) and the candidate is still
 *     servable after serve-time re-validation (§11.18) — an error-path serve, so
 *     `servedOnError` applies.
 *   - `neg-suppressed`: the factory threw a 404 while a servable `val` candidate exists, so the
 *     neg-write is suppressed (E7/INV-26) — the val is served, but this is NOT an error-path
 *     serve (§5.4's `staleIfError`), so `servedOnError` does NOT apply.
 *
 * `served` is REQUIRED, and that is the whole point: these two kinds exist only because a
 * candidate was supplied, so the candidate rides the outcome itself. A singleflight JOINER never
 * calls `fillAndStore` — it awaits the LEADER's promise (§5.2) — so its own local reads say
 * nothing about the value it is handed. Evidence is a property of the SERVE, not of the caller:
 * carrying it here is what lets a joiner report the tier, staleness, age and durability of the
 * value it actually receives. Deriving any of that from the caller's own state would describe a
 * different read.
 */
interface GracedAttempt<T> {
  readonly kind: "graced";
  readonly value: T;
  readonly served: ServedCandidate<T>;
}

interface NegSuppressedAttempt<T> {
  readonly kind: "neg-suppressed";
  readonly value: T;
  readonly served: ServedCandidate<T>;
}

/** The two kinds that serve a stored candidate, sharing one shape. Split by kind so a serve can
 * be narrowed to exactly one of them — `servedOnError` distinguishes them (E7). */
type ServedAttempt<T> = GracedAttempt<T> | NegSuppressedAttempt<T>;

/** Fenced past the write-back guard (§5.0/MI6), or a retriable error whose graced candidate is no
 * longer servable at serve time (§11.18) — never served. */
interface FencedAttempt {
  readonly kind: "fenced";
  /** The classified factory error, carried when a retriable failure produced this fence
   * (candidate no longer servable, §11.18). The plain `getOrSet` form throws it (a runtime error
   * is not a `NotImplementedError`); the entry form resolves a miss (`value: undefined`). Absent
   * on an E1 write-back fence (a hard purge, not a factory error). */
  readonly error?: unknown;
}

/**
 * The factory threw a RETRIABLE error (§5.4) and THIS fill produced no servable graced serve of
 * its own — it read no candidate (a blind leader). The classified error rides out as a shareable
 * FACT, never a verdict (§5.2): a singleflight joiner handed this result runs its OWN §5.1-step-5
 * error tail against its OWN read — a joiner that read a servable graced copy serves it; a caller
 * that read none propagates the classified error. This is why a leader's blindness never rejects a
 * joiner who read a servable copy itself.
 */
interface RetriableFailedAttempt {
  readonly kind: "retriable-failed";
  readonly error: unknown;
}

/**
 * The factory threw a `not_found` (404) while THIS fill read NO candidate of its own (a blind
 * leader, §5.2) and `notFoundTtl` was declared. Two things travel here, and keeping them apart is the
 * whole point (E30):
 *
 *   - The RECORDING of the fact is WORK, and the fill already did it — `neg` is the write this fill
 *     performed, ONCE, inside its own §5.0 fence and inside the singleflight window, exactly as the
 *     success path stores the filled value inside that same window. A join shares the work and its
 *     product; no waiter re-does it, and no waiter records the fact again. (A waiter that goes on
 *     to SERVE the neg still hydrates its own L1 as every fill serve does — that is the read-through
 *     copy, not the authoritative record, and it is not what this clause governs.)
 *   - The DISPOSITION is not shared. Each waiter runs its own E7 tail against its own read
 *     (`runOwnNotFoundTail`), because the SUPPRESS-vs-SERVE decision E7/INV-26 governs is EPISTEMIC —
 *     it turns on whether a servable `val` was actually seen, and a blind read cannot settle that. A
 *     waiter that read a still-servable `val` serves it (its own read is the existence proof the
 *     blind leader lacked); a waiter that read none takes the recorded neg as its outcome.
 *
 * Siting the write out here in the waiters' tails — outside the window the registration holds — is
 * what this shape exists to prevent: the slot would be free while the write was still pending, so a
 * later same-key caller could fill and store a value that the trailing neg then landed on top of, and
 * every blind waiter would issue its own duplicate write.
 */
interface NotFoundFactAttempt<T> {
  readonly kind: "not-found";
  readonly error: unknown;
  /** The neg this fill ALREADY wrote, in-window, carried as the `filled` outcome every serving path
   * understands: its stored tag set (E24), the write's own `durable`/`bornMs`. Waiters SERVE this;
   * they never re-write it. */
  readonly neg: FilledAttempt<T>;
  /** The fill's resolved scope literal (§6, E17) — the serve's scope evidence, computed by the fill
   * rather than re-derived per waiter. */
  readonly scope: string;
}

/** The `getOrSetEntry` factory called `ctx.skip()` (§2.1, M10, invariant 24) — nothing stored,
 * nothing served; the entry form surfaces `{ skipped: true, value: undefined }`. */
interface SkippedAttempt {
  readonly kind: "skipped";
}

/**
 * A fill attempt's classification, as a UNION rather than one shape with optional fields: each
 * kind carries exactly the evidence it has, and cannot be constructed without it. The two facts
 * this shape is responsible for — a served candidate always accompanies a `graced` serve, and a
 * `filled` outcome always knows its own birth instant — are now the compiler's to enforce rather
 * than a comment's to assert.
 */
type FillAttempt<T> =
  | FilledAttempt<T>
  | ServedAttempt<T>
  | FencedAttempt
  | RetriableFailedAttempt
  | NotFoundFactAttempt<T>
  | SkippedAttempt;

/** One tier's read outcome: a servable `hit` (omitted ⇒ refill) plus the epoch a
 * live check by this read verified (§5a) — the refill's bornEpoch floor.
 * `staleCandidate` carries a decoded `val`-kind envelope even when NOT servable as
 * `hit` (dead/unknown at THIS read) — the raw material for the error-path grace
 * fallback (§5.1 step 5, §11.18): a hard purge can render a candidate un-servable
 * as a direct hit while a later serve-time re-validation is still the authority a
 * retriable factory failure consults. Never populated for `kind:'neg'` (§11.7). */
interface TierRead<T> {
  readonly hit?: ReadHit<T>;
  readonly staleCandidate?: ReadHit<T>;
  readonly checkedThrough: number;
}

/** A miss with no live check performed (absent / codecId / decode). */
const MISS_READ: TierRead<never> = { checkedThrough: 0 };

/** The internal getOrSet outcome carrying enough for both the plain and entry forms.
 * `value` is omitted (not `undefined`) on a fenced/skip miss (§5.0). */
interface FillOutcome<T> {
  readonly value?: T;
  readonly stale: boolean;
  readonly tier: Tier;
  /** What this outcome's serve can prove about durability — absent ⇒ unknowable (E29). See
   * `CacheEntry.durable`, which this surfaces unchanged. */
  readonly durable?: boolean;
  /** The SERVED envelope's `bornMs` (§3) — the entry form derives `age` from it against the
   * injected clock. Absent when nothing was served (a miss / a skip), where there is no age to
   * report. Never an arbiter (E3): a report only. */
  readonly bornMs?: number;
  readonly skipped: boolean;
  readonly servedOnError?: boolean;
  /** On a `tier:'miss'` outcome produced by a retriable factory failure whose graced
   * copy is no longer servable (§11.18), the classified error the plain `getOrSet`
   * form rethrows (never a `NotImplementedError` for a runtime outcome); the entry
   * form ignores it and surfaces `value: undefined`. Absent for an E1 write-back
   * fence (a hard purge). */
  readonly error?: unknown;
}

/** Whether a stored entry's codec identity matches (or is an accepted migration
 * source) — checked BEFORE decode so a foreign codec never runs (E8/CACHE-9). */
function codecIdMatches(
  storedCodecId: unknown,
  codec: Codec,
  config: CacheConfig
): boolean {
  if (storedCodecId === codec.id) {
    return true;
  }
  const acceptCodecs = config.defaults?.acceptCodecs ?? [];
  return (
    typeof storedCodecId === "string" && acceptCodecs.includes(storedCodecId)
  );
}

/** Reconstruct the envelope shell from stored bytes + metadata (§3). `bornEpoch`/
 * `tags` ride in metadata so validation (§4.1) runs without a second decode. */
function envelopeFromStore<T>(
  key: string,
  raw: string,
  metadata: Readonly<Record<string, unknown>>,
  decoded: T,
  codecId: string
): Envelope<T> {
  const bornEpoch =
    typeof metadata.bornEpoch === "number" ? metadata.bornEpoch : 0;
  const bornMs = typeof metadata.bornMs === "number" ? metadata.bornMs : 0;
  const tags = Array.isArray(metadata.tags)
    ? metadata.tags.filter((t): t is string => typeof t === "string")
    : [];
  const kind = metadata.kind === "neg" ? "neg" : "val";
  // Read-time reconstruction default (§3). An entry this kernel writes always
  // stamps its resolved scope; a missing-scope stored envelope defaults to the
  // shared `pub` class, never the abolished hashless `usr:` literal (E17).
  const scope = typeof metadata.scope === "string" ? metadata.scope : "pub";
  return {
    bornEpoch,
    bornMs,
    codecId,
    enc: "json",
    grace: 0,
    key,
    kind,
    scope,
    size: raw.length,
    src: "factory",
    storedAt: bornMs,
    tags,
    ttl: 0,
    v: 3,
    val: decoded,
    ...(typeof metadata.fence === "number" ? { fence: metadata.fence } : {}),
  };
}

/**
 * The value a HIT on this envelope SURFACES (§3, §5.1 step 5, §11.7).
 *
 * A `kind:'neg'` envelope is a remembered 404: it caches the not-found FACT, and §3 gives it no
 * value to carry. Its stored bytes are a JSON-representable placeholder (`writeNegEnvelope`) chosen
 * only because `undefined` is not a storable body — they are never the answer. So a read of a neg
 * surfaces `undefined`, which is EXACTLY what the fill turn that wrote it surfaced (INV-7): the
 * point of caching a 404 is that the next reader gets the same answer without paying the origin,
 * and an answer that changes on the second call is not a cached fact at all. Serving the placeholder
 * instead handed the caller a `null` no origin ever produced, and reported a durable VALUE hit for
 * an entry holding only a negative.
 *
 * A `val` envelope surfaces its decoded value unchanged. This is the ONE point where the two kinds
 * diverge on the read path; every other neg rule is already a refusal to serve at all (§11.7 — a
 * stale neg is never grace/SWR-served, so the only servable neg is a fresh one).
 */
function servedValue<T>(envelope: Envelope<T>): T {
  if (envelope.kind === "neg") {
    // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the same narrowing the neg WRITE path takes (`classifyFillError`): a `kind:'neg'` entry's value is `undefined` by §3, and `T` legitimately includes it for a factory whose declared success type allows it. Both API forms surface it per §5.1 step 5.
    return undefined as T;
  }
  return envelope.val;
}

/**
 * Classify a rejected `Store.get` (§8, E13) — the read-side twin of `classifyWriteBackFailure`, and
 * consistent with its reasoning rather than merely its shape:
 *
 *   - CLASSIFIED (`throttled` / `unavailable`) ⇒ returned to the caller, which absorbs it. E13 signs
 *     that a suppressed L2 GET error is a plain MISS — never routed to unknown/fail-closed — so on
 *     the serving path the caller refills, and a configured L1 (checked FIRST, §5.1) may already
 *     have served without this tier being read at all.
 *   - UNCLASSIFIED ⇒ RETHROWN unchanged, for the same reason the write half rethrows one: the kernel
 *     absorbs only failures a driver has named, and a rejection nobody classified is not evidence of
 *     absence. Swallowing it would turn an unknown fault into a confident "not found" — which, on a
 *     read, is the one answer that silently refills over whatever is really there.
 *
 * Suppression is the only telemetry-bearing outcome (`store_read_suppressed`, a raw fact at the
 * instant it occurs). A rethrown error needs no event: it reaches the caller intact.
 */
function classifyStoreReadFailure(
  config: CacheConfig,
  key: string,
  error: unknown
): StoreReadError {
  if (!isStoreReadError(error)) {
    // oxlint-disable-next-line typescript/only-throw-error -- rethrowing the ORIGINAL rejection verbatim (still `unknown`), exactly as `classifyWriteBackFailure` does: re-wrapping would erase the driver's own error identity from a fault the caller is meant to see.
    throw error;
  }
  emitTelemetry(config, {
    code: error.code,
    key,
    type: TELEMETRY_EVENTS.store_read_suppressed,
  });
  return error;
}

/**
 * What a presence probe found (§7, E27) — three structurally distinct states, no sentinel. The
 * third exists because a read that FAILED is not a read that found nothing: reporting `absent` for
 * it would launder an error into an answer, the same laundering the stored-vs-reconstructed `ttl`
 * distinction exists to prevent. `explain` cannot refill, so E13's serving-path remedy (a suppressed
 * read is a miss, and the reader refills) has nothing to offer here — the witness reports the gap.
 */
type ProbeResult =
  | {
      readonly kind: "found";
      readonly envelope: Envelope;
      readonly scope: ExplainScope;
      readonly ttl: ExplainTtl;
    }
  | { readonly kind: "absent" }
  | { readonly kind: "read-failed" };

/** The ONE spelling of the "the store did not answer" discriminant, shared by the probe's outcome
 * and the witness's own arm so the two can never drift apart. */
const READ_FAILED_KIND = "read-failed";

/** The two probe outcomes that carry no payload — one shared instance each, exactly as `MISS_READ`
 * is for the read path. */
const PROBE_ABSENT: ProbeResult = { kind: "absent" };
const PROBE_READ_FAILED: ProbeResult = { kind: READ_FAILED_KIND };

/**
 * A NON-mutating presence probe for `explain` (§7, E27 (3)): read `canonical` from `store` with a
 * plain `store.get` — never a hydrating read — and reconstruct its envelope plus the RAW stored
 * scope (E23); `absent` when the entry is missing, codec-mismatched, or undecodable; `read-failed`
 * when the store's own read was suppressed (E13). The read fails closed exactly as the serve path
 * does (INV-8/27); it observes, it never writes.
 */
async function probeStoredEntry(
  store: Store,
  codec: Codec,
  config: CacheConfig,
  canonical: string
): Promise<ProbeResult> {
  let stored: StoreValue | undefined;
  try {
    stored = await store.get(canonical);
  } catch (error) {
    // A classified read failure is suppressed (E13) and reported as its own state; an unclassified
    // one propagates out of `explain` unchanged.
    classifyStoreReadFailure(config, canonical, error);
    return PROBE_READ_FAILED;
  }
  if (!stored || !codecIdMatches(stored.metadata?.codecId, codec, config)) {
    return PROBE_ABSENT;
  }
  let decoded: unknown;
  try {
    decoded = codec.decode(stored.value);
  } catch {
    return PROBE_ABSENT;
  }
  const envelope = envelopeFromStore(
    canonical,
    stored.value,
    stored.metadata ?? {},
    decoded,
    codec.id
  );
  // Scope AS STORED, before the E17 `pub` substitution collapses "absent" into "pub" — the
  // readable-vs-unreadable distinction the E23 gate turns on (§7.2).
  const storedScope = stored.metadata?.scope;
  const scope: ExplainScope =
    typeof storedScope === "string"
      ? { kind: "readable", literal: storedScope }
      : { kind: "unreadable" };
  // TTL AS STORED, on the same terms and for the same reason: read off the raw metadata, before
  // `envelopeFromStore`'s reconstruction default collapses "the format carries none" into `0`
  // (E27 (1)). What the store carries is stored identity; what the reader substitutes is not.
  const storedTtl = stored.metadata?.ttl;
  const ttl: ExplainTtl =
    typeof storedTtl === "number"
      ? { kind: "stored", seconds: storedTtl }
      : { kind: "not-stored" };
  return { envelope, kind: "found", scope, ttl };
}

/**
 * THE READ PATH'S CLASSIFIER for the SERVING path. Given raw stored bytes and metadata, it answers
 * what a reader on THIS instance would make of them: an envelope, or nothing at all. (The
 * introspection probe still runs its own copy of this sequence — see `probeStoredEntry`, which
 * answers a different question and must report the causes of absence rather than collapse them to
 * one. Folding the two is worth doing and is not this change.)
 *
 * Two gates precede the reconstruction and both fail CLOSED, because a reader that cannot identify
 * or decode an entry is served a miss rather than a guess — the codecId check (INV-27/E8: a
 * mismatch `acceptCodecs` does not admit is a miss) and `decode` itself (INV-8: a body this codec
 * cannot read is a miss). Only past both is an envelope built.
 *
 * Both seats call THIS function, by identity, and that is the point. "Servable" is defined by the
 * read path, so the write layer's E7 guard must ask the READER's question rather than a looser one
 * of its own: an envelope no reader can classify as a val is not a servable good value — it is a
 * miss wearing bytes, and protecting it protects a ghost. Answering that question with a partial
 * copy of these steps is exactly how two seats stop meaning one thing while each keeps passing its
 * own tests.
 */
function readerEnvelope<T>(
  config: CacheConfig,
  codec: Codec,
  key: string,
  stored: StoreValue
): Envelope<T> | undefined {
  // INV-27/E8: codecId BEFORE decode; a mismatch (not accepted) ⇒ miss.
  if (!codecIdMatches(stored.metadata?.codecId, codec, config)) {
    return undefined;
  }
  let decoded: T;
  try {
    // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the one sound narrowing point in the codec system: `codec.decode` is `string -> unknown`, and the codecId match just above (E8) licenses treating the result as this instance's own `T`.
    decoded = codec.decode(stored.value) as T;
  } catch {
    // INV-8: a decode failure fails closed to a miss — never a mis-typed serve.
    return undefined;
  }
  return envelopeFromStore<T>(
    key,
    stored.value,
    stored.metadata ?? {},
    decoded,
    codec.id
  );
}

/**
 * Read one tier: codecId-check + decode + validate against knowledge (§5, §4.1).
 * `hit` is a `ReadHit` (fresh or stale-servable) or `undefined` (absent / dead /
 * decode failure / codecId mismatch / suppressed read failure / unknown-fail-closed ⇒ the
 * caller refills). `checkedThrough` is the epoch a live check by THIS read verified (0 if
 * none), which the refill uses so a behind isolate does not fence its own legitimate refetch.
 */
async function readTier<T>(
  store: Store,
  codec: Codec,
  config: CacheConfig,
  inval: Invalidation | undefined,
  consistency: Consistency,
  key: string
): Promise<TierRead<T>> {
  let stored: StoreValue | undefined;
  try {
    stored = await store.get(key);
  } catch (error) {
    // E13: a suppressed store GET error is a PLAIN MISS — never unknown, never fail-closed. A miss
    // on the serving path means refill, so the tier's failure costs an origin read, not the request.
    // An unclassified rejection propagates instead (`classifyStoreReadFailure`).
    classifyStoreReadFailure(config, key, error);
    return MISS_READ;
  }
  if (!stored) {
    return MISS_READ;
  }
  // The classifier decides readability (codecId, then decode) and reconstructs. Both gates fail
  // closed to a miss; the write seat's E7 guard asks this same function so the two cannot disagree.
  const envelope = readerEnvelope<T>(config, codec, key, stored);
  if (!envelope) {
    return MISS_READ;
  }

  // The RAW scope evidence (§7.2, E23) — read from THIS store's metadata, BEFORE and
  // independent of `envelopeFromStore`'s E17 `pub` substitution just above. A readable
  // string is carried verbatim; anything else (absent, non-string) is UNREADABLE and is
  // treated as non-public at the eligibility gate. Deliberately NOT derived from
  // `envelope.scope`: the substitution has already collapsed "absent" into "pub" there, so
  // that field can no longer answer the question the gate asks.
  const storedScope = stored.metadata?.scope;
  const rawScope = typeof storedScope === "string" ? storedScope : undefined;
  const scopeEvidence = {
    scopeUnreadable: rawScope === undefined,
    ...(rawScope === undefined ? {} : { rawScope }),
  } as const;

  // validate(envelope): the §4.1 seam. Registry-absent ⇒ no `Invalidation` ⇒ FRESH
  // (no watermark authority — codec path unchanged). Otherwise the dual-epoch table
  // decides fresh/stale/dead/unknown against bus-fed knowledge or a live read.
  if (!inval) {
    return {
      checkedThrough: 0,
      hit: {
        ...scopeEvidence,
        envelope,
        stale: false,
        value: servedValue(envelope),
      },
    };
  }
  const { validity, checkedThrough } = await inval.validate(
    envelope,
    consistency,
    unknownResolution(config),
    config.defaults?.onUnavailable
  );
  if (validity === "fresh") {
    // The one servable neg is a FRESH one, and it surfaces `undefined` (§3, §5.1 step 5) — see
    // `servedValue`. Everything else this serve reports stays observational (E29): the tier it was
    // found in, the durability that tier proves, its own `bornMs`. Only the value is the fact.
    return {
      checkedThrough,
      hit: {
        ...scopeEvidence,
        envelope,
        stale: false,
        value: servedValue(envelope),
      },
    };
  }
  // A soft purge lands the value already-stale but still grace/SWR-servable (§4.1).
  // A neg is never grace/SWR-served (§3, N1) — treat as a miss. dead/unknown ⇒ never
  // serve; refill (fail closed, invariant 11).
  if (validity === "stale" && envelope.kind !== "neg") {
    return {
      checkedThrough,
      // `servedValue` is a no-op on this arm BY CONSTRUCTION (the kind exclusion just above owns
      // it), and is written here anyway so the form a neg surfaces is a property of the READ PATH
      // rather than of one arm's guard: were that exclusion ever loosened, a neg would still
      // surface `undefined` rather than its placeholder.
      hit: {
        ...scopeEvidence,
        envelope,
        stale: true,
        value: servedValue(envelope),
      },
    };
  }
  // Not directly servable (dead/unknown, or stale-but-neg) — never a `hit`. A
  // `val`-kind candidate is still carried as `staleCandidate`: raw material for the
  // error-path grace fallback, which re-validates it AT SERVE TIME (§11.18) rather
  // than trusting this read's classification (a hard delete is exactly why this read
  // said "dead" in the first place, so the fallback's re-check will agree — this
  // only matters when a decode/registry race makes the two checks disagree).
  if (envelope.kind === "neg") {
    return { checkedThrough };
  }
  return {
    checkedThrough,
    staleCandidate: {
      ...scopeEvidence,
      envelope,
      stale: true,
      // Same reasoning as the stale arm: a neg never reaches here (the early return above), and
      // routing through `servedValue` keeps that a stated property rather than an implicit one.
      value: servedValue(envelope),
    },
  };
}

/**
 * Build the factory's `ctx.request` and the demotion tracker (§6, INV-4 test 2).
 * Under a PUBLIC-scoped factory in dev mode with a request present, the request is
 * wrapped in a Proxy for the frame: ANY property READ marks the fill DEMOTED (the
 * resulting entry is confined to L1, never published to the shared tier — the
 * enforceable boundary invariant 4 names). The Proxy reads THROUGH to the
 * underlying request, so the factory still sees the real value and the harness's
 * `guardedRequest` records the read. The trigger is a READ of a guarded property —
 * NOT request presence (a public factory that never touches the request stays
 * durable, CONTROL B) and NOT factory arity (CONTROL B touches `ctx.signal`, a
 * sanctioned accessor, and stays durable). Outside public+dev the request is passed
 * through unwrapped and reads never demote.
 */
interface RequestGuard {
  readonly request: RequestContext | undefined;
  /** True once a guarded request property has been read by a public dev factory. */
  wasTouched(): boolean;
}

/**
 * DEVELOPMENT-TIME TEACHING TOOL — NOT A PRODUCTION SAFETY NET. It is dev-gated by design
 * (`config.dev === true`), and it stays that way. Two readings to head off, in both directions:
 *
 *   - DO NOT PROMOTE IT TO PRODUCTION believing it closes a privacy hole. It does not close one:
 *     an application that hand-mixes per-principal content into a public-scoped fill without
 *     touching `ctx.request` at all is untouched by it, so it was never a boundary — it is a
 *     tripwire that catches the honest mistake early, in the one environment where the developer
 *     is watching. Making the demotion live in production would make an entry's STORAGE TIER a
 *     function of what a factory happened to read on one execution: the same key lands durable on
 *     the request whose factory took the cache-hit path and L1-only on the request whose factory
 *     read the principal, so the tier stops being a property of the call and becomes a property of
 *     the run. That is nondeterministic caching, and it is a worse bargain than the mistake it
 *     would be trying to catch.
 *
 *   - DO NOT DELETE IT believing it is dead code. INV-4 (§6) pins it, and the demotion is exactly
 *     what that invariant's enforceable half asserts — a public fill that read request state is
 *     confined to L1 and never reaches the shared tier. Blind it and the sacred suite reds.
 *
 * It deliberately does NOT feed the §7.2 shared-cacheability gate. That gate answers a different
 * question — whether the scopes the library DECLARED and STORED permit a shared HTTP cache to keep
 * the response — and it answers it from evidence that survives the request (E17). This guard sees
 * only what one factory did while it ran. Wiring a runtime observation into that gate would make
 * the emitted response posture vary run to run for the same route, which is the same nondeterminism
 * as above, on the wire instead of in the tiers.
 */
function buildRequestGuard(
  config: CacheConfig,
  options: GetOptions
): RequestGuard {
  const { request } = options;
  const guardActive = config.dev === true && options.scope === "public";
  if (!guardActive || request === undefined) {
    return { request, wasTouched: () => false };
  }
  let touched = false;
  const guarded: RequestContext = new Proxy(request, {
    get(target, prop, receiver): unknown {
      // A public factory READING any request property demotes the entry off the
      // shared tier (§6). Read THROUGH so the factory sees the real value and the
      // adapter's own guard (harness `guardedRequest`) still witnesses the read.
      touched = true;
      return Reflect.get(target, prop, receiver) as unknown;
    },
  });
  return { request: guarded, wasTouched: () => touched };
}

/** Acquire the cross-isolate lock if requested (§5.2), returning its fence handle. */
async function maybeLock(
  config: CacheConfig,
  options: GetOptions
): Promise<LockHandle | undefined> {
  if (options.lock !== true) {
    return undefined;
  }
  const { lock } = config;
  if (!lock) {
    return undefined;
  }
  return await lock.acquire(options.key);
}

/**
 * Classify a rejected L2 write-back into the kernel's E13 write-failure semantics.
 * WIRES CLASSIFICATION + TELEMETRY ONLY — no REFRESH_QUEUE / retry machinery is built
 * (that is deferred). A recognized transient failure is SUPPRESSED: the write did not
 * become durable, but the operation does not fail the caller and produces no
 * fail-closed/unknown state — the function returns `false` (non-durable) and emits the
 * reserved telemetry name. Everything else keeps today's behavior and PROPAGATES:
 *   - `throttled`   (retryable) → `writeback_throttled`, suppressed (`durable:false`).
 *   - `unavailable` (retryable) → `l2_write_error`, suppressed (`durable:false`).
 *   - `too_large`   (permanent, non-retryable) → rethrown, never silently suppressed.
 *   - unclassified  → surfaced as `l2_write_error` telemetry, then rethrown unchanged.
 */
function classifyWriteBackFailure(
  config: CacheConfig,
  key: string,
  error: unknown
): boolean {
  const classified = isStoreWriteError(error) ? error : undefined;
  if (classified?.code === "throttled") {
    emitTelemetry(config, { key, type: "writeback_throttled" });
    return false;
  }
  if (classified?.code === "unavailable") {
    emitTelemetry(config, { key, type: "l2_write_error" });
    return false;
  }
  // Not a suppressible transient. `too_large` (permanent, retryable:false) propagates on
  // its own — the value can never be stored, so it is never silently suppressed (E13,
  // never truncate). An UNCLASSIFIED rejection also keeps today's behavior — it PROPAGATES
  // (never silently swallowed), but is surfaced as `l2_write_error` telemetry first for
  // observability. Either way, rethrow the ORIGINAL error (still `unknown` here).
  if (classified === undefined) {
    emitTelemetry(config, { key, type: "l2_write_error" });
  }
  throw error;
}

/**
 * What ONE born-monotone write-back did (§5.0, E13, E7). Two independent facts, because a candidate
 * that did not land has two possible causes and they must never be conflated:
 *
 *   - `durable` — whether the durable tier now holds a copy this write is content with: THIS
 *     candidate, or a strictly-newer envelope already there that supersedes it (E3 ordering).
 *     `false` under E13's
 *     classified `throttled`/`unavailable` failure (issued, did not land) AND under an E7 refusal
 *     (never issued): in both the kernel positively knows no durable copy of the candidate exists,
 *     which is exactly what the field reports.
 *   - `writeSuppressed` — whether E7's kind-aware guard REFUSED the write outright. This is the
 *     field that separates the two: nothing was issued, the store is unchanged, and everything that
 *     follows a write follows this too. The pair is not `(landed, why-not)` — it is a fact about the
 *     candidate and a fact about the attempt, and only the second distinguishes the causes.
 */
interface WriteBackOutcome {
  readonly durable: boolean;
  readonly writeSuppressed: boolean;
}

/**
 * Is this stored envelope a `val` that E7 protects (§11.18, E7/INV-26)? THE suppression-worthiness
 * test, and it lives in ONE place because two seats have to agree about it:
 *
 *   - the SERVE seat (`gracedStillServable`) asks it of the candidate THIS read holds, deciding
 *     whether an error-path serve may draw on it;
 *   - the WRITE seat (`negDisplacesProtectedVal`) asks it of the envelope the born-monotone compare
 *     already fetched, deciding whether a `kind:'neg'` candidate may land on it.
 *
 * One function, because the two answers may never disagree in the direction that destroys data: a
 * val the serve path would hand to a caller is exactly a val the write path must refuse to bury.
 * Written twice they drift, and the drift is silent — each site keeps passing its own tests while
 * the pair stops meaning one thing.
 *
 * `validity` is the §4.1 verdict, produced by the same machinery on both sides. `unknown` is
 * deliberately NOT settled here: it is a GAP rather than a window, and the two seats answer a gap
 * differently on purpose — the serve seat refuses to serve THROUGH one, the write seat refuses to
 * write through one. Each states that rule, with its own derivation, at its own site.
 */
function e7ProtectsStoredVal(envelope: Envelope, validity: Validity): boolean {
  return (
    envelope.kind !== "neg" && (validity === "fresh" || validity === "stale")
  );
}

/** The `UnknownResolution` a RE-VALIDATION of an already-stored envelope asks for (§5a, E9): settle
 * an unknown tag with one live registry read. Named once because the two seats that re-validate the
 * same envelope — the serve seat's grace check and the write seat's E7 guard — must ask the SAME
 * question of it; drifting onto different resolutions would let them reach different verdicts about
 * one entry and call it agreement. Distinct from `unknownResolution`, which reports the instance's
 * configured READ-time policy. */
const REVALIDATION_RESOLUTION: UnknownResolution = "registry-check";

/**
 * E7's kind-aware guard (§5.1 step 5, E7/INV-26): would landing a `kind:'neg'` candidate here bury a
 * `val` the letter protects? Asked of the envelope the born-monotone compare ALREADY fetched, which
 * is why it lives at the write layer — one boundary, so every neg-write source passes it identically
 * and the outcome cannot vary by the route that reached it.
 *
 * The question is ONTOLOGICAL: what does the STORE hold? That is a different question from the one
 * the filling caller's own read can answer, and the gap between them is the whole defect this closes
 * — a caller blind to a servable val (a tier that was unreadable for a moment, or a §5.2 join whose
 * leader read nothing) would otherwise write straight over it, and every later reader would be
 * served the negative. Note also which KIND is which: `FilledAttempt.storedKind` describes what THIS
 * fill produced, while the kind read here is the STORED envelope's — different objects, and only the
 * second one can answer this.
 *
 * The within-grace term is not re-invented: it is the §4.1 verdict `validate` gives, asked on the
 * same terms the serve seat asks for it (`gracedStillServable`), and settled by the shared predicate
 * both seats share.
 */
async function negDisplacesProtectedVal(
  config: CacheConfig,
  key: string,
  existing: StoreValue,
  inval: Invalidation | undefined,
  consistency: Consistency
): Promise<boolean> {
  // Classified by the READ PATH'S OWN classifier, called by identity — so the kind this guard reads
  // is precisely the kind a reader would read, gates included. E7 protects a SERVABLE good value,
  // and servability is the read path's definition: E8/INV-27 makes a codecId mismatch a miss and
  // §5.1 makes a decode failure a miss, so an envelope failing either is not a val this guard may
  // protect. It is a miss wearing bytes — `readTier` returns MISS for it and `explain()` reports
  // `absent` — and suppressing over it would protect a ghost: the neg could never land, every
  // request would pay the origin for as long as the condition held, and no reader could ever be
  // served the thing being protected. Cost of decoding here, stated because it is the reason the
  // earlier shortcut existed: it is bounded by the miss path's own origin call, which this write is
  // already the tail of.
  //
  // JURISDICTION: this guard protects THE STORE IT WRITES, and that scope is deliberate. E7's crime
  // is displacement — a neg overwriting a val where that val lives — which is a same-store event, so
  // a durable neg landing beside a warm L1 copy destroys nothing and the serve path's tier
  // precedence governs what any reader sees. Consulting L1 here would be worse than redundant: L1 is
  // per-isolate, so whether a DURABLE outcome occurred would depend on which isolate's write raced
  // first, and any isolate lacking the warm copy would land the neg regardless — plumbing deciding a
  // durable fact, which is the very class the shared classifier above exists to kill. The L1-only
  // val's own class is priced non-durable by E13 and reported honestly by E29; protecting it through
  // the write guard would upgrade that class by a side door.
  //
  // What bounds the coexistence is therefore the WRITE, not residency: the L1 copy coexists with an
  // unreadable L2 until the durable tier speaks, and the moment a neg lands the same event that
  // records the fact retires the superseded copy (the eviction that follows a landed neg — a stale
  // val must never outlive the durable tier's newer word). Guard and eviction are two halves of one
  // posture, and the pairing is pinned as such by the coexistence tooth in the guard's unit suite.
  const stored = readerEnvelope<unknown>(
    config,
    codecOf(config),
    key,
    existing
  );
  if (!stored) {
    return false;
  }
  if (!inval) {
    // Registry-absent ⇒ no watermark authority ⇒ every stored entry validates FRESH (§4.1) — the
    // same rule the serve seat takes, so the two agree with no second statement of it.
    return e7ProtectsStoredVal(stored, "fresh");
  }
  let validity: Validity;
  try {
    ({ validity } = await inval.validate(
      stored,
      consistency,
      REVALIDATION_RESOLUTION,
      config.defaults?.onUnavailable
    ));
  } catch {
    // The verdict could not be established at all (an unreachable registry, an embargoed tag). That
    // is the state the §4.1 table already names `unknown`, reached by a throw instead of a value —
    // and it is resolved below exactly as any other gap is. Nothing is rethrown: this guard is
    // deciding whether to WRITE, and the caller's own read has its own reachability rules.
    validity = "unknown";
  }
  // UNKNOWN FAILS TOWARD SUPPRESSION. A gap is not permission, so the write seat resolves it to the
  // protective end of the window — §4.1 already reads `stale` as purged-but-still-inside-the-window
  // — and asks the shared predicate with that. The two errors are not the same size: suppressing a
  // neg that should have landed costs ONE origin round-trip, and the next read re-asks the origin and
  // writes it then; landing a neg that should have been suppressed destroys a live value, with every
  // later reader served the negative and nothing left that remembers what was overwritten. The
  // default follows the irreversible one. The resolution touches the WINDOW only — kind is read
  // straight off the stored metadata and needs no registry — so a stored `neg` still admits the
  // write, gap or no gap.
  return e7ProtectsStoredVal(
    stored,
    validity === "unknown" ? "stale" : validity
  );
}

/**
 * Born-monotone write-back (§5.0, E3, invariant 17): never replace a stored
 * envelope with a strictly-higher `(bornEpoch, fence)`. Read-compare-write against
 * L2 — the KV CAS-less residual race is documented and bounded to one refresh cycle
 * (§5.2). `bornMs` is never an arbiter (E3). `durable` is `true` when the candidate landed (or a
 * strictly-newer envelope already sits durably), `false` when a classified `throttled`/`unavailable`
 * failure suppressed it (§5.0, E13).
 *
 * It is ALSO where E7's kind-aware guard sits, for the reason the guard needs and this function
 * already has: the stored envelope is in hand here. A `kind:'neg'` candidate may only land on a true
 * miss, so every neg-write source — a solo caller's own disposition, a blind leader's in-window
 * recording — meets the same test on the same evidence, and route-independence holds by construction
 * rather than by matching two call sites. `val`-over-`neg` is untouched: recovery is the direction
 * E7 leaves open, and the guard is asked only of a neg candidate.
 */
async function bornMonotoneWrite<T>(
  config: CacheConfig,
  l2: Store,
  key: string,
  encoded: string,
  candidate: Envelope<T>,
  inval: Invalidation | undefined,
  consistency: Consistency
): Promise<WriteBackOutcome> {
  let existing: StoreValue | undefined;
  try {
    existing = await l2.get(key);
  } catch (error) {
    // The write-back's read-compare is MANDATORY, not advisory: E3's arbitration is decided against
    // the stored envelope, so a read that did not happen leaves the candidate's ordering UNDECIDED.
    // Treating that as "nothing stored" and writing anyway could replace a strictly-newer envelope —
    // exactly what INV-17 forbids — so the write-back does not land. That outcome is already named:
    // a classified transient makes the write-back NON-DURABLE (E13's write half — telemetry, no
    // caller-visible failure, and `getOrSetEntry` surfaces `durable:false` for a value that reached
    // L1 only). An unclassified rejection propagates, unchanged. Nothing is written on either path,
    // so INV-17 holds by construction rather than by argument.
    return {
      durable: classifyWriteBackFailure(config, key, error),
      writeSuppressed: false,
    };
  }
  if (existing) {
    // E7/INV-26 — THE KIND-AWARE GUARD, and the only place the neg-write asks it. A neg may land
    // only on a true miss; a stored `val` the letter protects turns it away, unwritten. Nothing else
    // in this function runs on that path: the born-monotone compare below arbitrates an ordering
    // between two envelopes, and there is no ordering to arbitrate for a write that is not happening.
    if (
      candidate.kind === "neg" &&
      (await negDisplacesProtectedVal(
        config,
        key,
        existing,
        inval,
        consistency
      ))
    ) {
      emitTelemetry(config, { key, type: TELEMETRY_EVENTS.neg_suppressed });
      // `durable:false`. E29 makes this turn's `durable` a report on the fill's OWN write, awaited
      // post-settle — and a suppression IS a settle, with a known outcome: the neg landed nowhere.
      // The kernel does not merely lack confirmation here, it has positive knowledge that no
      // not-found was stored, so `true` would assert a durable not-found that provably does not
      // exist. That the durable tier is in exactly the state E7 wants is a fact about the STORE and
      // is reported by the served entry's own evidence; it is not this field's claim to make.
      //
      // The tempting argument for `true` — that a caller's outcome may not vary by plumbing, so a
      // blind caller's serve should match the solo serve it would have received had the guard
      // never had anything to look at — conflates STATE with ROUTE. Route-independence holds the
      // store fixed: whether this compare-fetch carried the val's bytes is plumbing and may not
      // decide anything. But whether the store HOLDS a val is state, and state is exactly what may
      // decide outcomes — hit-versus-miss is the product. "Had the guard nothing to look at"
      // describes a DIFFERENT WORLD, not a different route through this one. Route-independence
      // survives intact and is pinned as such: against an equally-stocked store the solo caller is
      // suppressed too, because this guard is the one boundary, so both report `false`.
      return { durable: false, writeSuppressed: true };
    }
    const meta = existing.metadata ?? {};
    const storedBornEpoch =
      typeof meta.bornEpoch === "number" ? meta.bornEpoch : 0;
    const storedFence = typeof meta.fence === "number" ? meta.fence : -1;
    const candidateFence = candidate.fence ?? -1;
    // Strictly-newer stored ⇒ reject the candidate (E3 ordering: bornEpoch, then
    // fence). Equal or older stored ⇒ the candidate lands.
    const storedNewer =
      storedBornEpoch > candidate.bornEpoch ||
      (storedBornEpoch === candidate.bornEpoch && storedFence > candidateFence);
    if (storedNewer) {
      // A strictly-newer envelope already sits durably in L2; the entry is durable.
      return { durable: true, writeSuppressed: false };
    }
  }
  try {
    await l2.set(key, encoded, {
      metadata: {
        bornEpoch: candidate.bornEpoch,
        bornMs: candidate.bornMs,
        codecId: candidate.codecId,
        enc: candidate.enc,
        kind: candidate.kind,
        scope: candidate.scope,
        tags: candidate.tags,
        v: candidate.v,
        ...(candidate.fence === undefined ? {} : { fence: candidate.fence }),
      },
    });
    return { durable: true, writeSuppressed: false };
  } catch (error) {
    return {
      durable: classifyWriteBackFailure(config, key, error),
      writeSuppressed: false,
    };
  }
}

/**
 * Re-validate a graced candidate's tags AT SERVE TIME (§11.18, MI5): a hard delete
 * that landed after the candidate was read (e.g. during the failed refresh) must
 * fence it here — a graced value is never served once its purge is visible. No
 * `Invalidation` (registry-absent) ⇒ nothing to invalidate it ⇒ always servable.
 *
 * AT `strong` THIS IS A LIVE REGISTRY CHECK LIKE ANY OTHER (§1, E28), so it honors `onUnavailable`
 * exactly as its sibling sites do. Leaving it unguarded did not avoid a throw — it MANUFACTURED
 * one: the guard inside `validate` fires for a caller who explicitly opted out, which is the one
 * caller who asked not to see it.
 *
 * `factoryError` is what this re-validation runs on behalf of, and it rides out as the thrown
 * error's `cause`. That answers the reason the guard was omitted here — the fear that a throw would
 * REPLACE the caller's factory error — without leaving the site unguarded: nothing is dropped, and
 * callers still get the one type §1 names.
 *
 * WHEN THE OPT-OUT IS TAKEN this call drops to what an eventual read would do, and the candidate is
 * settled against local knowledge alone. A tag whose hardness cannot be established stays UNKNOWN,
 * and an unknown tag is never graced (§1; §5.1's ladder sends unknown to 5a and never to grace) —
 * when hardness is exactly what we failed to learn, a hard purge is one of the things the gap could
 * be concealing. So an unknown candidate is not servable, and the factory's own error is what the
 * caller ends up with.
 */
async function gracedStillServable(
  inval: Invalidation | undefined,
  consistency: Consistency,
  envelope: Envelope,
  onUnavailable: "eventual" | undefined,
  factoryError: unknown
): Promise<boolean> {
  if (!inval) {
    // Registry-absent ⇒ no watermark authority ⇒ every stored entry validates FRESH (§4.1, and see
    // `readTier`'s own registry-absent arm). Routed through the shared predicate rather than
    // returning a bare `true`, so the registry-absent rule reads identically at both seats — and so
    // the kind exclusion stays a property of the TEST rather than of whichever arm reached it.
    return e7ProtectsStoredVal(envelope, "fresh");
  }
  let validity: Validity;
  try {
    ({ validity } = await inval.validate(
      envelope,
      consistency,
      REVALIDATION_RESOLUTION,
      onUnavailable
    ));
  } catch (error: unknown) {
    if (error instanceof RegistryUnavailableError) {
      // Re-thrown solely to hang the factory error off it: same type, same message (the registry's
      // own failure text is already inside it), plus the failure this check was serving.
      throw new RegistryUnavailableError(error.message, {
        cause: factoryError,
      });
    }
    throw error;
  }
  // DEAD (hard-purged) or UNKNOWN (fail-closed, may hide a hard purge) must never
  // be served on the error path; only a confirmed non-dead result is servable. THIS SEAT'S rule for
  // the gap: refusing to serve through one costs a refill and nothing else, so the serve seat can
  // afford to be strict where the write seat cannot (see `negDisplacesProtectedVal`).
  return e7ProtectsStoredVal(envelope, validity);
}

/**
 * Write a `kind:'neg'` envelope for a `not_found` fact (§3, §5.1 step 5, N1/N2).
 * `notFoundTtl` is the per-call opt-in; the write shares the same born-monotone
 * fence as any write-back.
 *
 * Returns the write's OUTCOME (§5.0, E13, E7 — durability, and whether E7's guard refused it) AND
 * the `bornMs` it stamped — the neg's birth instant, read off the clock exactly once here and
 * carried out for every later consumer, the same way a value fill's is.
 *
 * `inval`/`consistency` ride down for the guard alone: it decides against the STORE, and deciding
 * against the store means asking the same §4.1 question a reader asks of what it finds there.
 */
async function writeNegEnvelope(
  config: CacheConfig,
  key: string,
  tags: readonly string[],
  scope: string,
  bornEpoch: number,
  inval: Invalidation | undefined,
  consistency: Consistency
): Promise<WriteBackOutcome & { readonly bornMs: number }> {
  const { l2 } = config;
  const bornMs = config.clock.now();
  if (!l2) {
    // Unreachable in practice — the caller (`classifyFillError` ⇐ `fillAndStore`) has
    // already returned early when no L2 is configured. Nothing was suppressed.
    return { bornMs, durable: true, writeSuppressed: false };
  }
  const codec = codecOf(config);
  // A neg carries no real value (§3) — encode a JSON-representable placeholder
  // rather than `undefined` (which `JSON.stringify` maps to the non-string
  // `undefined`, not a valid stored body). Every read of a neg surfaces
  // `value: undefined` regardless of these stored bytes (§5.1 step 5).
  const encoded = codec.encode(null);
  const envelope: Envelope<null> = {
    bornEpoch,
    bornMs,
    enc: "json",
    grace: 0,
    key,
    kind: "neg",
    scope,
    size: encoded.length,
    src: "factory",
    storedAt: bornMs,
    tags,
    ttl: 0,
    v: 3,
    val: null,
    codecId: codec.id,
  };
  const written = await bornMonotoneWrite(
    config,
    l2,
    key,
    encoded,
    envelope,
    inval,
    consistency
  );
  return { bornMs, ...written };
}

/** Build the E7/INV-26 neg-suppression serve of a still-servable candidate (a 404 that a servable
 * `val` displaces). ONE constructor for both call sites — the leader-sighted classification and a
 * blind leader's per-waiter tail — so `value` is always the served candidate's own value.
 *
 * It also EMITS, and the emit belongs here rather than at either caller. E7 lists `neg_suppressed`
 * among a suppression's consequences, and E31 attaches those consequences to whichever seat renders
 * the suppression, one event per suppression. This is the serve seat's renderer; the write layer's
 * guard emits its own at its own site. Emitting from the shared constructor is what makes one
 * event per suppression structural instead of a convention two call sites have to remember —
 * before it, the epistemic suppression that E7's own rationale describes fired no telemetry at all,
 * so the commonest case was the silent one. */
function negSuppressed<T>(
  config: CacheConfig,
  key: string,
  served: ServedCandidate<T>
): NegSuppressedAttempt<T> {
  emitTelemetry(config, { key, type: TELEMETRY_EVENTS.neg_suppressed });
  return { kind: "neg-suppressed", served, value: served.hit.value };
}

/**
 * Write the `not_found` neg (§5.1 step 5, N1/N2) and package it as the `filled` attempt every
 * SERVED-fill path already understands: `value:undefined`, the neg's stored tag set, and the write's
 * own `durable`/`bornMs`. A neg carries no `bornEpoch` on the outcome — its presence is the marker
 * of a successful VALUE fill (E16), which this is not — so L1 hydration falls back to the caller's
 * outer floor exactly as the value path intends.
 *
 * ONE writer of the neg for two call sites: the leader that read a candidate of its own that
 * re-validated DEAD (its own disposition, in `classifyFillError`), and any blind waiter's own E7 tail
 * (`runOwnNotFoundTail`). Both stamp the fill's tags/scope/bornEpoch, so a neg written on either path
 * is byte-identical to the one a solo caller writes — and both meet E7's write-layer guard, which is
 * why a SUPPRESSED write is one of the outcomes this can carry (`writeSuppressed`): the fill still
 * produced the not-found FACT and still serves it, but nothing was stored and nothing is evicted.
 */
async function negWriteFilled<T>(
  config: CacheConfig,
  key: string,
  tags: readonly string[],
  scope: string,
  bornEpoch: number,
  inval: Invalidation | undefined,
  consistency: Consistency
): Promise<FilledAttempt<T>> {
  const neg = await writeNegEnvelope(
    config,
    key,
    tags,
    scope,
    bornEpoch,
    inval,
    consistency
  );
  // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- a `kind:'neg'` envelope's declared value is `undefined`; `T` legitimately includes `undefined` for a factory whose declared success type allows it, and `getOrSetEntry`/`getOrSet` surface it as `value: undefined` per §3.
  const negValue = undefined as T;
  return {
    bornMs: neg.bornMs,
    durable: neg.durable,
    kind: "filled",
    storedKind: "neg",
    tags,
    value: negValue,
    writeSuppressed: neg.writeSuppressed,
  };
}

/**
 * The factory error-path classification (§5.1 step 5, E2/E7), extracted out of
 * `fillAndStore` to keep its cognitive complexity down:
 *   - `ctx.skip()` (§2.1/M10/invariant 24): `{kind:'skipped'}` — nothing stored,
 *     nothing served; no write-back of any kind, not even a neg.
 *   - `not_found` (404, a fact — never masked): with `notFoundTtl` declared and THIS fill's own read
 *     holding a still-servable `val`, suppress the neg-write and serve that val — a neg must never
 *     displace a servable value (E7/INV-26) — with no `servedOnError` (a suppressed neg is not an
 *     error-path serve). This fill's own copy re-validated DEAD ⇒ write the neg and serve it (its own
 *     disposition). This fill read NO copy at all (a blind leader) ⇒ ride the 404 out as a shareable
 *     `not-found` FACT so each waiter runs its own E7 tail against its own read (E30, `NotFoundFact
 *     Attempt`), the mirror of the retriable branch's fenced-vs-fact split just below. Without
 *     `notFoundTtl`, rethrow.
 *   - retriable (§5.4 `staleIfError`, default `isRetriableHttp`) with a graced `val`
 *     still servable at serve time: serve it, `servedOnError: true` (§11.18). A
 *     `kind:'neg'` graced candidate is NEVER offered here — it is never grace/SIE-
 *     served (§11.7) — so a neg-only history rethrows.
 *   - otherwise (non-retriable, or no servable graced copy): rethrow — a failed or
 *     timed-out factory never modifies the stored good value (§11.3).
 */
async function classifyFillError<T>(
  error: unknown,
  config: CacheConfig,
  key: string,
  tags: readonly string[],
  scope: string,
  bornEpoch: number,
  inval: Invalidation | undefined,
  consistency: Consistency,
  options: GetOrSetOptions<T> | GetOrSetEntryOptions<T>,
  graced: ServedCandidate<T> | undefined
): Promise<FillAttempt<T>> {
  if (error instanceof SkipSignalError) {
    return { kind: "skipped" };
  }

  if (isNotFound(error)) {
    if (options.notFoundTtl === undefined) {
      throw error; // a fact, never masked — no negative caching declared (§5.4).
    }
    if (graced) {
      // This fill read a `val` candidate of its own. E7/INV-26: a neg must never displace a servable
      // val, so suppress the neg-write and serve that val IFF it is still servable at serve time
      // (§11.18) — but this is NOT an error-path serve (a 404 is not `staleIfError`), so it carries
      // no `servedOnError`. The candidate rides the outcome (see `ServedAttempt`): a joiner handed
      // this result reports THIS entry's evidence, never its own unrelated local reads.
      if (
        await gracedStillServable(
          inval,
          consistency,
          graced.hit.envelope,
          config.defaults?.onUnavailable,
          error
        )
      ) {
        return negSuppressed(config, key, graced);
      }
      // This fill's OWN copy re-validated DEAD (a hard delete landed after the read): there is no
      // servable val here to protect, so the neg-write lands — this fill's own disposition. The
      // joiner-sharing case is the blind-leader arm below (mirroring the retriable branch, whose
      // sighted-but-dead leader likewise returns its own `fenced` rather than the shared fact).
      return await negWriteFilled(
        config,
        key,
        tags,
        scope,
        bornEpoch,
        inval,
        consistency
      );
    }
    // This fill read NO candidate of its own (a blind leader, §5.2). RECORD the fact now — here,
    // inside the fill, which is inside the §5.0 fence and inside the singleflight window the
    // registration holds — because recording is WORK, and work is what a join shares (E30). Deferring
    // it to the waiters' tails would publish the fill's completion while its write was still pending:
    // the slot would be free, a later same-key caller could fill and store a value, and this neg
    // would land on top of it (equal epochs are admitted, §5.0/E3) — and every blind waiter would
    // issue its own duplicate write besides.
    //
    // The DISPOSITION still travels per waiter (E30): whether a servable val exists is an EPISTEMIC
    // fact this blind read could not establish, and E7's suppression turns on exactly that. So the
    // recorded neg rides out with the classified error as a FACT, and each waiter's own tail decides
    // what to serve — a sighted joiner serves its own val, a blind waiter serves this neg. No waiter
    // writes.
    const neg = await negWriteFilled<T>(
      config,
      key,
      tags,
      scope,
      bornEpoch,
      inval,
      consistency
    );
    return { error, kind: "not-found", neg, scope };
  }

  // DECLARED grace gates the §5.4 classification, and the gate is load-bearing beyond taste:
  // `staleIfErrorOf` resolves a USER-supplied predicate, so it must not be invoked on calls that
  // could never produce a graced serve. A call declaring no `grace` is one of those — no waiter on
  // this fill can serve a stale copy (`grace` rides the join identity, so every waiter declared the
  // same), and the error rethrows below exactly as it always has, without consulting the classifier.
  if (options.grace !== undefined && staleIfErrorOf(config)(error)) {
    // Retriable (§5.4). What this fill can produce depends on what THIS fill's OWN read found —
    // never on some other caller's read (§5.1 step 5 is the caller's per-read algorithm; §5.2's
    // singleflight is a mechanism inside it, not a substitute for it).
    if (graced) {
      // INV-7: a `kind:'neg'` graced candidate is never grace/SIE-served — only a
      // `val` candidate ever reaches here (the read path excludes neg from `hit`).
      // INV-18: re-validate NOW — a hard delete landing after the read must fence it.
      if (
        await gracedStillServable(
          inval,
          consistency,
          graced.hit.envelope,
          config.defaults?.onUnavailable,
          error
        )
      ) {
        // The candidate rides the outcome (see `ServedAttempt`) — a joiner inherits this SERVE with
        // its evidence (§5.2, E24): the leader-sighted direction the grace-join evidence suite pins.
        return { kind: "graced", served: graced, value: graced.hit.value };
      }
      // The candidate read a moment ago is no longer servable once re-validated
      // (a hard delete became visible after the read, e.g. mid-fill) — §11.18's
      // fence: never served. There is no servable copy, so per §5.1 step 5 the plain
      // form rethrows this classified (retriable) error and the entry form resolves a
      // miss — the caller sees a real runtime error, never a `NotImplementedError`.
      // `error` rides the outcome so the wrapper can throw it without re-classifying.
      return { error, kind: "fenced" };
    }
    // This fill read NO candidate of its own (a blind leader, §5.2). The classified retriable
    // failure rides out as a FACT, never a verdict: each waiter runs its own §5.1-step-5 tail
    // against its own read. A joiner that read a servable graced copy serves it; a caller that read
    // none propagates the error. A leader's blindness is not a joiner's disposition.
    return { error, kind: "retriable-failed" };
  }
  // Non-retriable: rethrow. INV-3: a failed/timed-out factory never modifies the stored
  // good value — nothing was written above.
  throw error;
}

/**
 * Fill: run the factory, fence the write-back (§5.0/E1), then born-monotone
 * write-through to L2 (§5.0/E3). Returns `{kind:'filled', value}` on success,
 * `{kind:'fenced'}` when the fill was fenced by a hard purge visible at write-back
 * (§5.0/MI6 — never store or serve a fenced value), `{kind:'skipped'}` on
 * `ctx.skip()` (§2.1/M10/invariant 24), or `{kind:'graced', value}` when the
 * factory threw a retriable error (§5.4/E2) and `graced` — the currently-known
 * stale-but-servable `val` — is STILL servable once re-validated at serve time
 * (§11.18). `bornEpoch` is captured by the caller at FILL START (§3, §5.0) so a
 * purge that bumps the epoch after the fill began cannot retroactively make the
 * born value look fresh. Error-path handling is `classifyFillError` above.
 */
async function fillAndStore<T>(
  config: CacheConfig,
  options: GetOrSetOptions<T> | GetOrSetEntryOptions<T>,
  key: string,
  inval: Invalidation | undefined,
  consistency: Consistency,
  bornEpoch: number,
  graced: ServedCandidate<T> | undefined,
  // OPTIONAL, and the omission is load-bearing: the SWR background refresh
  // (`refreshOrGrace`) calls this WITHOUT a collector. That refresh's value is never the
  // served one — the caller already recorded the stale hit it is serving — so feeding the
  // collector from there would attribute the NEXT read's dependencies to this render and
  // double-count its tags against the §7.2 budget. A refresh is a write-ahead, not a
  // consumption (§5.1 step 6, §7).
  ambient?: AmbientCollector
): Promise<FillAttempt<T>> {
  const codec = codecOf(config);
  const { l2 } = config;
  if (!l2) {
    return notImplemented("cache.getOrSet (no l2 configured)", config, options);
  }

  // Declared tags, validated + deduped at the boundary (§2.3, MI13/E24) — a fail-fast on a
  // forged `__` tag or an over-ceiling declared set BEFORE the factory runs. `declaredTags` is
  // also the error/neg-write path's stored set: a neg caches the 404 FACT, and the settle-union
  // (`setTags`/`dependsOn`) belongs to the VALUE a successful factory produced.
  const declaredUserTags = sortedUserTags(options.tags);
  const declaredTags = envelopeTags(declaredUserTags, key, config.namespace);
  // The per-fill tag accumulator (E24): `dependsOn`/`setTags` contributions land here and union
  // with `declaredUserTags` at settle, after the factory closes.
  const accum = makeTagAccumulator(declaredUserTags);
  // Total scope resolution (§6, E17): `usr:<hash>` is L1-only by construction;
  // `pub`/`ten:` are durable unless the public dev-mode guarded-accessor Proxy
  // demotes THIS fill (decided after the factory runs, `guard.wasTouched()`).
  const resolvedScope = resolveScope(options);
  const scope = resolvedScope.literal;
  const guard = buildRequestGuard(config, options);

  const lockHandle = await maybeLock(config, options);
  // An `EntryFactoryCtx<T>` satisfies both factory shapes: the plain `getOrSet`
  // factory (`FactoryCtx<T>`) only reads the base members, and the entry factory
  // needs `skip()`. `skip()` is reachable ONLY through the entry form's TYPE
  // (`EntryFactoryCtx<T>`, not `FactoryCtx<T>`, M10) — the runtime ctx object
  // below is shared plumbing, but a plain `getOrSet` factory is typed so it can
  // never see `skip` on its `ctx` parameter and so can never call it.
  const ctx: EntryFactoryCtx<T> = {
    dependsOn(tag: Tag, dependsOptions?: { readonly l3?: boolean }): void {
      // E25: a `dependsOn` tag PERSISTS into the stored set (E24), which is FLAT — it carries no
      // per-tag emission class — so an `l3:false` dependency would emit on every later serve,
      // breaking the E23 severability the qualifier promises. Until per-tag stored metadata exists
      // (v0.5), refuse the qualifier VALUE here rather than silently un-honor it; the `false` VALUE
      // defers, the `{l3?}` shape stays valid API. `l3:false` remains fully live on the
      // collection/emission surface (`collect().dependsOn`), which stores nothing.
      if (dependsOptions?.l3 === false) {
        notImplemented(
          "FactoryCtx.dependsOn({ l3: false }) — deferred to v0.5",
          config,
          options
        );
      }
      // E24: after the factory settles the fill's tag set is closed, so a later declaration is
      // outside the value's lifetime — throw rather than record it against a value that is
      // already stored/served (§2.1).
      if (accum.isSettled()) {
        throw new FactorySettledError(key);
      }
      // E24: the tag JOINS the entry's stored set (validated per-call for the `__` backstop),
      // ADDING membership to the settle-union — never epochs, never scope, never duration. This
      // persistence is IN ADDITION to the E23 render-collector recording below, not a
      // replacement for it. `l3:false` is severable from BOTH: it suppresses shared-surface tag
      // EMISSION (E23) but an `l3:false` dependency STILL joins the stored set.
      accum.addMembership(tag);
      // Render-time dependency collection (§7, E23). The declaration carries THIS fill's
      // resolved scope literal — the factory is running inside the fill, so the entry it
      // is declaring a dependency for is this one, and its scope is the scope the kernel
      // just resolved (never a value read back off a store).
      //
      // `l3:false` is SEVERABLE (E23): it suppresses tag EMISSION only. The scope evidence
      // is recorded regardless — a droppable tag on a per-principal entry is still a
      // per-principal entry, and a body's shared-cacheability was never the tag's to
      // decide. The suppression therefore lives INSIDE `record`, strictly after the scope
      // is banked (see `collect()`), not as an early return here.
      recordDependency(ambient, {
        scope: fillScopeEvidence(scope),
        tags: [tag],
        ...(dependsOptions?.l3 === undefined ? {} : { l3: dependsOptions.l3 }),
      });
    },
    fail(err?: unknown): never {
      if (err === undefined) {
        throw new Error("factory failed (non-retriable)");
      }
      if (err instanceof Error) {
        throw err;
      }
      throw new Error("factory failed (non-retriable)", { cause: err });
    },
    /** Deferred to v0.4 — explicit grace-reuse from within a fill is not part of this phase. */
    reuseGraced(): T {
      return notImplemented(
        "FactoryCtx.reuseGraced (deferred to v0.4)",
        config,
        options
      );
    },
    // The adapter request object (§6). Under public scope in dev mode this is the
    // guarded Proxy: a read demotes the entry off the shared tier (INV-4 test 2).
    ...(guard.request === undefined ? {} : { request: guard.request }),
    setTags(tags: readonly Tag[]): void {
      // E24: `setTags` AUTHORS the declared set — it REPLACES `options.tags` as the base
      // contribution to the settle-union, while `dependsOn` membership still accumulates
      // independently (neither erases the other). Same post-settle rule and per-call `__`
      // backstop as `dependsOn`. A silent no-op was non-conformant (E24).
      if (accum.isSettled()) {
        throw new FactorySettledError(key);
      }
      accum.author(tags);
    },
    /** Deferred to v0.4 — the §2.1 TTL-override (what it overrides, how it meets grace and
     * the duration grammar) is a v0.4 design slot, not a silent no-op (spec-surface sweep). */
    setTtl(): void {
      notImplemented("FactoryCtx.setTtl (deferred to v0.4)", config, options);
    },
    signal: new AbortController().signal,
    skip(): never {
      // §2.1, M10, invariant 24: a private sentinel throw, caught below and
      // turned into a `skipped` outcome — nothing stored, nothing served.
      // Never reachable from `getOrSet`'s `FactoryCtx<T>` type (no `skip`
      // member), only from `getOrSetEntry`'s `EntryFactoryCtx<T>`.
      throw new SkipSignalError();
    },
  };

  // E25 defect B — the RULED SPINE: acquire → [factory] → settle-union → validate (MI13 ceiling)
  // → write-back → RELEASE-IN-FINALLY. The lock is
  // released on EVERY exit path (factory-error return, ceiling throw, fenced
  // return, filled return) so a settle-time throw past `maybeLock` can never leak the lock and
  // block every other isolate's fill of this key until lease expiry. ONE finally covers BOTH
  // callers (foreground `fillAndStoreChecked` + background `refreshOrGrace`). A fence-fire retry
  // re-enters via a FRESH `fillAndStore` call, which acquires its own lock — the finally released
  // the prior one first, so no attempt re-enters holding a lock (holds by construction).
  try {
    let value: T;
    try {
      value = await options.factory(ctx);
    } catch (error) {
      // The factory SETTLED (rejected): close the tag set before the error path runs so a
      // stashed-ctx late `dependsOn`/`setTags` throws (§2.1, E24).
      accum.markSettled();
      return await classifyFillError(
        error,
        config,
        key,
        declaredTags,
        scope,
        bornEpoch,
        inval,
        consistency,
        options,
        graced
      );
    }
    // The factory SETTLED (resolved): close the tag set (§2.1, E24) so the union below is the
    // final one and any later declaration throws.
    accum.markSettled();
    // The entry's STORED user-tag set (§2.3, E24): the union at settle of declared + `setTags` +
    // `dependsOn`. Validated as ONE set at the write boundary — the count ceiling and `__`
    // backstop (MI13) meet every source at once. Exceeding fails the fill here (through the
    // finally, which releases the lock); nothing is stored (no envelope is built, no write-back
    // runs). With no `setTags`/`dependsOn`, this is byte-identical to `declaredUserTags`.
    const userTags = accum.settleUserTags();
    validateUserTags(userTags);
    const tags = envelopeTags(userTags, key, config.namespace);

    // §5.0 write-back fence against local bus-applied knowledge (per the E1/INV-12
    // erratum): a hard purge that landed DURING the fill (delivered here ⇒ tag known-dead
    // locally) fences the born value — it is never stored (MI6). The fence runs over the SETTLE
    // set (E24), so a hard purge on a `dependsOn`-declared tag fences exactly as a declared one
    // does. A born-dead value that slips past this local fence (cold/dropped-frame isolate) is
    // stored but INERT — its first eventual read fails closed and live-checks (§5a). THIS fill's
    // own serve is not covered by that read-time check: it is anchored at INITIATION, where the
    // strong pre-factory check (E16) minted `bornEpoch` at the global epoch and so dominates every
    // purge accepted before initiation (§5, INV-12); only SUBSEQUENT strong reads live-check at
    // their own initiation. This fence therefore only ever catches a purge that landed AND
    // delivered locally DURING the fill (a concurrent purge either linearization order may miss) —
    // a retry-and-refetch bonus, not the serve-freshness guarantee (E26).
    if (inval && tags.length > 0 && !inval.fenceWriteBack(tags, bornEpoch)) {
      return { kind: "fenced" };
    }

    const bornMs = config.clock.now();
    const encoded = codec.encode(value);
    const fence = lockHandle?.fence;
    const envelope: Envelope<T> = {
      bornEpoch,
      bornMs,
      enc: "json",
      grace: 0,
      key,
      kind: "val",
      scope,
      size: encoded.length,
      src: "factory",
      storedAt: bornMs,
      tags,
      ttl: 0,
      v: 3,
      val: value,
      codecId: codec.id,
      ...(fence === undefined ? {} : { fence }),
    };

    // Tier policy (§6, E17): an entry is L1-only iff its scope confines it —
    // a `usr:<hash>` (adapter-derived principal) OR a public dev-mode factory that
    // read a guarded request accessor (`guard.wasTouched()`). An L1-only entry is
    // NEVER written to the shared L2 tier: a second isolate misses it (INV-4). A
    // `pub`/`ten:` entry that touched nothing lands durably in L2 as before
    // (INV-20/INV-8/E1 — contextless default writes stay in the shared tier).
    const l1Only = resolvedScope.l1Only || guard.wasTouched();
    // A `pub`/`ten:` fill writes through to L2 (§6/E17); an L1-only fill never does and is
    // non-durable by construction. Capture whether the write-back became durable: a
    // classified `throttled`/`unavailable` failure is suppressed to `durable:false`
    // (§5.0/E13) — the value still serves, the durable copy simply did not land.
    const written: WriteBackOutcome = l1Only
      ? { durable: false, writeSuppressed: false }
      : await bornMonotoneWrite(
          config,
          l2,
          key,
          encoded,
          envelope,
          inval,
          consistency
        );
    // NOTE: the fill does NOT mark its tags known-clean. A tag becomes known only via a
    // verified source — a bus frame or the recorded live check on the eventual
    // fail-closed path (§4.1, §5a). A cold/dropped-frame isolate can pass the local
    // write-back fence and store a born-dead value, so fabricating a clean record here
    // would let a later eventual read serve it as fresh; instead that value stays INERT
    // and its first eventual read fails closed and live-checks — while at `strong` the value
    // is born fresh in the first place, because the check-then-fetch pre-fetch check
    // (`fillAndStoreChecked`) already raised `bornEpoch` past every purge it observed (CACHE-31).
    return {
      bornEpoch,
      bornMs,
      durable: written.durable,
      kind: "filled",
      storedKind: "val",
      tags,
      value,
      // Carried rather than hardcoded `false`: E7's guard turns on the CANDIDATE's kind, and a value
      // fill's candidate is a `val`, so the write layer answers this — the one place that knows.
      writeSuppressed: written.writeSuppressed,
    };
  } finally {
    // A real lock releases AFTER the write-back (the prior pre-write-back release was a noted
    // simplification, superseded here). FakeLock's fence token is captured above before this runs,
    // and its tokens are monotone by acquire order, so the fence is stable regardless (§5.2).
    await lockHandle?.release();
  }
}

/** §5.0 write-back attempt budget: fence-taught retries (CACHE-32). A fill fenced by a
 * hard purge that landed mid-fill re-mints its `bornEpoch` from the now-advanced verified
 * high-water and refetches, up to this many attempts before failing typed. */
const WRITE_BACK_ATTEMPT_BUDGET = 3;

/**
 * `fillAndStore` fronted by the CHECK-THEN-FETCH strong-miss reorder (INV-1, INV-2,
 * §5.0, E1, E16, E18 — CACHE-31) and the §5.0 fence-taught retry (CACHE-32). On a
 * `strong`, registry-backed fill the live un-memoized registry check
 * (`inval.liveCheckForFill`) runs over the fill's tags BEFORE the factory; the fill's
 * `bornEpoch` is raised to the now-current verified high-water, so the born value
 * postdates every purge that check observed — hard (E16) OR soft (E18). A strong read
 * therefore never serves a stale-born or hard-dead value, and two strong reads of an
 * unmutated key agree.
 *
 * If the §5.0 LOCAL write-back fence (`fenceWriteBack`) fences the fill — a hard purge
 * landed DURING the factory and was delivered/verified into local knowledge — the fill
 * RE-MINTS `bornEpoch` from the now-advanced verified high-water (`localBornEpoch`,
 * which a resync or this call's own live check has caught up between attempts, per
 * `#recordChecked`) and REFETCHES: the honest post-acceptance fetch the purge demanded,
 * which borns past it and passes the fence (CACHE-32 — observations commission later
 * fetches, they never bless the earlier one). Bounded by the §5.0 attempt budget; an
 * attempt whose re-mint cannot advance the floor gives up (the fence is not from an
 * observable purge). Exhaustion returns `fenced` — `getOrSet` throws `FencedError`,
 * `getOrSetEntry` misses (§5.0/MI6). `eventual`/registry-absent/tags-empty fills skip
 * the pre-fetch check but share the retry.
 */
async function fillAndStoreChecked<T>(
  config: CacheConfig,
  options: GetOrSetOptions<T> | GetOrSetEntryOptions<T>,
  key: string,
  inval: Invalidation | undefined,
  consistency: Consistency,
  bornEpoch: number,
  graced: ServedCandidate<T> | undefined,
  ambient: AmbientCollector | undefined
): Promise<FillAttempt<T>> {
  const tags = inval
    ? envelopeTags(sortedUserTags(options.tags), key, config.namespace)
    : [];
  let bornEpochForFill = bornEpoch;
  // `tags` always carries the reserved `__k`/`__ns` that `envelopeTags` appends (§5), so
  // `tags.length > 0` holds even for a fill that declared NO user tags — the strong pre-factory
  // mint runs on every registry-backed strong fill. Narrowing this gate to USER tags would skip
  // the mint on an undeclared strong fill and forfeit its initiation anchor (born minted at the
  // global epoch — E16/INV-12/E26): do NOT filter the reserved tags out of this condition.
  if (consistency === "strong" && inval && tags.length > 0) {
    const checkedThrough = await inval.liveCheckForFill(
      tags,
      config.defaults?.onUnavailable
    );
    bornEpochForFill = Math.max(bornEpochForFill, checkedThrough);
  }
  for (let attempt = 0; attempt < WRITE_BACK_ATTEMPT_BUDGET; attempt++) {
    const result = await fillAndStore(
      config,
      options,
      key,
      inval,
      consistency,
      bornEpochForFill,
      graced,
      ambient
    );
    if (result.kind !== "fenced" || !inval || tags.length === 0) {
      return result;
    }
    const reMinted = Math.max(bornEpochForFill, inval.localBornEpoch());
    if (reMinted === bornEpochForFill) {
      // UNREACHABLE for a pure §5.0 write-back fence, held by an invariant worth stating: that fence
      // fires only when a hard purge has been delivered into THIS isolate's local knowledge during
      // the fill (§4.1) — and that same local delivery is exactly what lifts `localBornEpoch()` past
      // the fenced born. So a fired write-back fence always lets the re-mint advance, and this
      // no-advance give-up is never reached by it; every terminal write-back fence is the
      // budget-exhaustion give-up below, which `FencedError`'s "past its write-back attempt budget"
      // text reports accurately. (An error-bearing §11.18 grace fence can arrive here, and rides its
      // classified error straight out — it throws that error, never a `FencedError`.) TRIPWIRE: if a
      // future change ever lets a fence fire WITHOUT advancing the re-mint — a purge known but not
      // locally applied — this branch becomes reachable, a single fence is NOT a budget exhaustion,
      // and the fence message must gain a disposition split or it will lie.
      return result;
    }
    bornEpochForFill = reMinted;
  }
  return { kind: "fenced" };
}

/**
 * Which public entry point a call arrived through (§2.1, E6). Part of the
 * singleflight join identity, which distinguishes the two forms because they
 * differ in skip-capability: an `EntryFactoryCtx` waiter (which can `skip`)
 * must never receive a value produced by a plain `getOrSet` factory's ctx
 * (which cannot), and vice versa.
 */
type ApiForm = "getOrSet" | "getOrSetEntry";

/**
 * The in-isolate singleflight registry — §5.2 makes in-isolate singleflight
 * mandatory (invariant 5). One per `createCache()` instance, i.e. per isolate
 * (`world()` calls `createCache` once per isolate, §5.2).
 *
 *   - `byIdentity`: in-flight fills keyed by JOIN IDENTITY (`joinIdentity`). A
 *     concurrent call with the SAME identity JOINS the one fill. Removed as soon
 *     as the fill settles, so the next non-concurrent call runs a fresh fill.
 *   - `byKey`: the join identities currently in flight for each canonical KEY —
 *     used to detect an OPTION MISMATCH (§2.1, §10): two concurrent same-KEY
 *     fills whose identities DIFFER (e.g. different declared tags). A joiner with
 *     a matching identity never reaches this; only a genuinely-incompatible
 *     concurrent call does. A count per identity handles multiple joiners cleanly.
 */
/** One in-flight fill's presence for its canonical key (mismatch detection). A
 * refcount handles multiple joiners of the SAME identity; `envelopeTags` is that
 * fill's full stored tag set, referenced by a `singleflight_option_mismatch`
 * event so the event names the INCUMBENT fill's declared tags (§2.1, §10). */
interface KeyIdentity {
  count: number;
  readonly envelopeTags: readonly string[];
}

interface SingleflightMap {
  readonly byIdentity: Map<string, Promise<FillAttempt<unknown>>>;
  readonly byKey: Map<string, Map<string, KeyIdentity>>;
}

/** A fresh, empty singleflight registry for one isolate. */
function newSingleflight(): SingleflightMap {
  return { byIdentity: new Map(), byKey: new Map() };
}

/**
 * The singleflight JOIN IDENTITY (§2.1 strict option-compatibility, §5.1 step
 * 5, errata E6): the canonical key plus every structurally-relevant option a
 * waiter would otherwise silently miss. Two concurrent calls coalesce onto ONE
 * factory invocation iff their join identities are byte-identical strings.
 *
 *   - `key`: the already-canonicalized (ns-versioned, scoped) key — two calls
 *     for different keys must never coalesce.
 *   - `consistency` (E6): a `strong` request never joins a non-strong (`eventual`)
 *     fill — an eventual waiter MAY join a strong fill (strictly safer), so the
 *     identity is keyed on the REQUESTING call's own consistency, not some
 *     merged value; a `strong` caller's identity differs from an `eventual`
 *     caller's, so they never hash equal and each strong call always runs its
 *     own fill, while two `eventual` calls (or two `strong` calls) still share.
 *   - `apiForm` (E6): `getOrSet` vs `getOrSetEntry` — a plain-`T` waiter must
 *     never join an entry-form fill's ctx (skip-capability differs, M10).
 *   - structural options (§2.1 GPT note): tags (sorted+deduped, so order never
 *     matters), ttl, grace, scope, notFoundTtl, codec id — a mismatch here means
 *     a waiter could receive an envelope missing its own declared dependency
 *     tags, the exact stringly-typed no-op class this library exists to kill.
 *
 * `dev` mismatch handling (throw vs a separate fill + telemetry event) belongs
 * to a later cluster; this cluster only builds the identity and coalesces
 * exact matches — a mismatch here simply produces a different map key, so it
 * already runs its own separate fill (the safe default) without yet emitting
 * `singleflight_option_mismatch` or dev-throwing.
 */
function joinIdentity<T>(
  key: string,
  consistency: Consistency,
  apiForm: ApiForm,
  codec: Codec,
  options: GetOrSetOptions<T> | GetOrSetEntryOptions<T>
): string {
  const userTags = sortedUserTags(options.tags);
  return JSON.stringify({
    apiForm,
    codecId: codec.id,
    consistency,
    grace: options.grace ?? null,
    key,
    notFoundTtl: options.notFoundTtl ?? null,
    scope: scopeLiteral(options),
    tags: userTags,
    ttl: options.ttl ?? null,
  });
}

/** A structural options mismatch between concurrent same-key fills (§2.1, §10).
 * The message points at the fix (declare compatible options); dev throws it. */
class SingleflightOptionMismatchError extends Error {
  constructor(userKey: string) {
    super(
      `singleflight option mismatch for key ${JSON.stringify(userKey)}: ` +
        "concurrent calls declared incompatible options (tags/ttl/grace/scope/" +
        "notFoundTtl/codec). Make the options identical, or serialize the calls."
    );
    this.name = "SingleflightOptionMismatchError";
  }
}

/**
 * Detect + handle a singleflight OPTION MISMATCH (§2.1, §10): another fill for
 * the SAME canonical key is already in flight under a DIFFERENT join identity.
 * `dev:true` throws (a caller bug surfaced loudly); prod runs a SEPARATE fill and
 * emits `singleflight_option_mismatch` — carrying the colliding key + the
 * declared tags (hosted sinks salt every field, INV-15). A matching-identity
 * joiner never reaches here (it joined in `singleflight` before this ran).
 */
function handleOptionMismatch<T>(
  map: SingleflightMap,
  key: string,
  identity: string,
  config: CacheConfig,
  options: GetOrSetOptions<T> | GetOrSetEntryOptions<T>
): void {
  const identities = map.byKey.get(key);
  if (identities === undefined) {
    return;
  }
  // Some OTHER identity is in flight for this key (this call's own identity was
  // not found in `byIdentity`, so any present identity is incompatible). Emit the
  // event ABOUT the INCUMBENT fill — the one this arriving call collides with —
  // so the event's tag ref names the in-flight fill's declared tags (§2.1, §10).
  let incumbent: KeyIdentity | undefined;
  for (const [inFlight, record] of identities) {
    if (inFlight !== identity) {
      incumbent = record;
      break;
    }
  }
  if (incumbent === undefined) {
    return;
  }
  if (config.dev === true) {
    throw new SingleflightOptionMismatchError(options.key);
  }
  emitTelemetry(config, {
    key,
    tags: incumbent.envelopeTags,
    type: "singleflight_option_mismatch",
  });
}

/** Track this identity as in flight for its canonical key (mismatch detection),
 * remembering its full stored tag set for a later mismatch event's tag ref. */
function trackKeyIdentity(
  map: SingleflightMap,
  key: string,
  identity: string,
  fillEnvelopeTags: readonly string[]
): void {
  const identities = map.byKey.get(key) ?? new Map<string, KeyIdentity>();
  const existing = identities.get(identity);
  if (existing) {
    existing.count += 1;
  } else {
    identities.set(identity, { count: 1, envelopeTags: fillEnvelopeTags });
  }
  map.byKey.set(key, identities);
}

/** Untrack this identity once its fill settles (mismatch detection cleanup). */
function untrackKeyIdentity(
  map: SingleflightMap,
  key: string,
  identity: string
): void {
  const identities = map.byKey.get(key);
  if (identities === undefined) {
    return;
  }
  const record = identities.get(identity);
  if (record !== undefined) {
    record.count -= 1;
    if (record.count <= 0) {
      identities.delete(identity);
    }
  }
  if (identities.size === 0) {
    map.byKey.delete(key);
  }
}

/**
 * Run `fill` under in-isolate singleflight (§5.2, invariant 5): if a fill with
 * the SAME join identity is already in flight, join it instead of starting a
 * second factory invocation — both callers resolve to the one result. Otherwise,
 * a concurrent fill for the same KEY under a DIFFERENT identity is an OPTION
 * MISMATCH (§2.1, §10): dev throws, prod runs a separate fill + emits the event
 * (`handleOptionMismatch`). Then register this call's fill and run it.
 *
 * CRITICAL — coalescing under `Promise.all` / same-tick concurrent callers
 * (not just `fc.scheduler` interleavings): the in-flight promise is registered
 * into the map SYNCHRONOUSLY, in the same microtask turn as the identity
 * lookup, with NO `await` between the `byIdentity.get` check and the
 * registration. `fill()` itself is only invoked (and starts doing real async
 * work) after registration. Two callers racing through `Promise.all` each
 * invoke `singleflight` synchronously up to this point; the first to run
 * (JS's single-threaded execution order gives the two `Promise.all` callbacks
 * a well-defined sequential dispatch — there is no `await` before either call
 * reaches here) wins the identity check and registers, so a same-identity
 * second call synchronously observes the first's entry and joins it, while a
 * different-identity second call synchronously observes the mismatch. The entry
 * is removed in a `finally` once the fill settles (success or failure) — a
 * subsequent, non-concurrent call always runs its own fresh fill.
 */
async function singleflight<T>(
  map: SingleflightMap,
  key: string,
  identity: string,
  config: CacheConfig,
  options: GetOrSetOptions<T> | GetOrSetEntryOptions<T>,
  fill: () => Promise<FillAttempt<T>>
): Promise<FillAttempt<T>> {
  const existing = map.byIdentity.get(identity);
  if (existing) {
    // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the map is keyed by join identity, which encodes `T`'s call-site shape (key/tags/ttl/grace/scope/notFoundTtl/codec/consistency/apiForm); a joining caller with an identical identity is, by construction, asking for the same `T`.
    return await (existing as Promise<FillAttempt<T>>);
  }
  // No matching identity in flight: any in-flight fill for the SAME key is an
  // incompatible-options collision (§2.1). This runs SYNCHRONOUSLY (before any
  // `await`) so a concurrent same-tick pair reliably sees each other.
  handleOptionMismatch(map, key, identity, config, options);
  // Register BEFORE the first `await` so a concurrent same-tick joiner's
  // `byIdentity.get` above observes this entry (see the doc comment above). Carry
  // this fill's full stored tag set so a later colliding call's mismatch event can
  // reference the incumbent's declared tags (§2.1, §10).
  const fillEnvelopeTags = envelopeTags(
    sortedUserTags(options.tags),
    key,
    config.namespace
  );
  trackKeyIdentity(map, key, identity, fillEnvelopeTags);
  const registration = (async (): Promise<FillAttempt<T>> => {
    try {
      return await fill();
    } finally {
      map.byIdentity.delete(identity);
      untrackKeyIdentity(map, key, identity);
    }
  })();
  map.byIdentity.set(identity, registration);
  return await registration;
}

/**
 * THE source of a fresh `bornEpoch` stamp (§3, §5.0). Every mint on the read path — the
 * foreground fill AND the SWR background refresh, which §5.0 names side by side — derives its
 * stamp here, so no path can stamp a newly minted value with an epoch taken from some OTHER
 * value's envelope. A stale hit's `bornEpoch` describes when THAT value was born; a value born
 * now takes the epoch applied now, or the refresh mints a value that is already stale at birth
 * and can never make progress.
 *
 * Two call sites that happen to agree is what this type exists to prevent: the derivation is
 * here, once, and the call sites hold only its result.
 */
interface BornMint {
  /**
   * The stamp for a value minted NOW: the fill-start floor, raised by whatever live checks the
   * caller has verified since (§5a). A behind isolate must not fence its own legitimate refetch,
   * while the floor bounds the concurrent-purge case (INV-9/INV-12).
   */
  stamp(checkedThrough: number): number;
}

/**
 * Open the mint for one read, snapshotting the `bornEpoch` floor SYNCHRONOUSLY — the writer's
 * applied registry epoch at fill start (§3, §5.0). Taking it at OPEN is the fence-critical part:
 * a purge that lands via a concurrent bus frame after this call cannot lift the floor, so a fill
 * that BEGAN before a concurrent purge still fences correctly at write-back (INV-9/INV-12).
 * Registry-absent ⇒ 0.
 */
function openBornMint(inval: Invalidation | undefined): BornMint {
  const floor = inval ? inval.localBornEpoch() : 0;
  return {
    stamp: (checkedThrough: number): number => Math.max(floor, checkedThrough),
  };
}

/* References: §5.1, §8, E13, E29. */

function serveDurability(
  tier: Tier,
  hit: ReadHit<unknown>
): { readonly durable?: boolean } {
  if (tier === "l2") {
    return { durable: true };
  }
  const evidence = hitScopeEvidence(hit);
  if (evidence.kind === "readable" && storedScopeIsL1Only(evidence.literal)) {
    return { durable: false };
  }
  return {};
}

/**
 * The error-path grace serve (§5.1 step 5, §11.18, E7): a stored candidate that re-validated as
 * still-servable is served `stale:true` after the fill failed. `graced` (a retriable failure)
 * carries `servedOnError`; `neg-suppressed` (a 404, which is not `staleIfError`) does not.
 *
 * Every field here reports the CANDIDATE — the entry actually being served — rather than the fill
 * that failed: its tier is the tier it was read from, its durability what that tier can prove
 * (E29), its age its own stamp. The pre-E29 shape named `tier:'l2'` and `durable:true` as
 * constants, for a value that may have been found in L1 and never written to a shared tier at all.
 */
function gracedServe<T>(
  attempt: ServedAttempt<T>,
  ambient: AmbientCollector | undefined
): FillOutcome<T> {
  // The candidate comes off the ATTEMPT, never off the calling read's own state: a singleflight
  // joiner is handed this same outcome and must describe the value in it (§5.2).
  const { served } = attempt;
  // An error-path grace serve still SERVES a stored entry — so it is a consumed dependency exactly
  // like a hit (§7, E23), carrying that entry's raw stored scope. A response does not become
  // shared-cacheable because the origin was down.
  recordServedHit(ambient, served.hit);
  return {
    ...serveDurability(served.tier, served.hit),
    bornMs: served.hit.envelope.bornMs,
    skipped: false,
    stale: true,
    tier: served.tier,
    value: attempt.value,
    ...(attempt.kind === "graced" ? { servedOnError: true } : {}),
  };
}

/**
 * One caller's §5.1-step-5 error tail over a SHARED retriable failure (§5.2, §5.4): the fill that
 * failed produced no serve of its own (a blind leader), so it handed out the classified error as a
 * FACT. This runs on behalf of a caller against ITS OWN read — a singleflight joiner that read a
 * copy of its own, or the blind leader itself. Each state resolves to exactly what a SOLO caller in
 * that same state resolves to, because a caller's outcome must not depend on which caller happened
 * to win singleflight:
 *
 *   - this caller read a `val` candidate that STILL re-validates as servable now (§11.18/MI5 — a
 *     hard delete during the fill must not resurrect it): serve it `{stale:true, servedOnError}`
 *     with THIS candidate's own evidence (tier/durable/age, E24/E29), exactly as a leader's own
 *     graced serve would. `staleCandidate` is already a `ServedCandidate`, so the evidence-riding
 *     machinery is reused unchanged.
 *   - this caller read a candidate that re-validates DEAD: it REFUSES the serve and takes the
 *     classified failure — returned as the FENCED OUTCOME, not thrown, so the wrapper applies the
 *     same per-form mapping the solo path gets for this state (the plain form rethrows the typed
 *     error, the entry form resolves a miss). Throwing here would make an entry-form joiner reject
 *     where an identical solo call resolves a miss.
 *   - this caller read no candidate at all: the classified error is rethrown, exactly as a solo
 *     caller with nothing to fall back on rethrows it (§5.1 step 5).
 *
 * A joiner's recourse is thus judged against its OWN read, never the leader's disposition — the
 * leader's blindness is a fact it shares, not a verdict it imposes.
 */
async function runOwnErrorTail<T>(
  config: CacheConfig,
  options: GetOrSetOptions<T> | GetOrSetEntryOptions<T>,
  inval: Invalidation | undefined,
  consistency: Consistency,
  staleCandidate: ServedCandidate<T> | undefined,
  error: unknown,
  ambient: AmbientCollector | undefined
): Promise<FillOutcome<T>> {
  if (options.grace !== undefined && staleCandidate !== undefined) {
    if (
      await gracedStillServable(
        inval,
        consistency,
        staleCandidate.hit.envelope,
        config.defaults?.onUnavailable,
        error
      )
    ) {
      return gracedServe(
        {
          kind: "graced",
          served: staleCandidate,
          value: staleCandidate.hit.value,
        },
        ambient
      );
    }
    // This caller's OWN copy re-validated dead (§11.18): refuse the serve and hand the classified
    // failure back as the fenced OUTCOME, letting the wrapper map it per form exactly as it does
    // for a solo caller holding a dead candidate.
    return {
      durable: false,
      error,
      skipped: false,
      stale: false,
      tier: "miss",
    };
  }
  // oxlint-disable-next-line typescript/only-throw-error -- rethrowing the ORIGINAL classified factory throwable verbatim (§5.1 step 5's typed rethrow, typically the caller's `HttpError`); re-wrapping would erase the typed status the classifier and callers depend on. A caller holding no candidate at all is served exactly what a solo caller with nothing to fall back on is.
  throw error;
}

/** The read tiers in order (§5.1): L1 then L2. Absent tiers are skipped. */
function tiersOf(config: CacheConfig): readonly (readonly [Store, Tier])[] {
  const tiers: (readonly [Store, Tier])[] = [];
  if (config.l1) {
    tiers.push([config.l1, "l1"]);
  }
  if (config.l2) {
    tiers.push([config.l2, "l2"]);
  }
  return tiers;
}

/** Write the freshly-filled value into L1 (§5.1 write-through) — best-effort. The whole fill
 * outcome rides in, because every field written here belongs to that ONE fill: its `bornEpoch` so
 * both tiers validate identically (§4.1 — `fallbackBornEpoch` covers the neg-write outcome, which
 * stamps none), its `bornMs` so both tiers report the same age (§3 — birth is an event, and a value
 * has ONE: the stamp is minted at fill and carried here, never re-read off the clock, which would
 * date the L1 copy to the moment the L2 write finished and make one value two ages), and its STORED
 * tag set (E24 — the settle-union) so the two tiers are tag-coherent: a later L1 hit replays the
 * same purge handles L2 carries.
 *
 * A `kind:'neg'` fill whose write LANDED stores nothing here and EVICTS whatever L1 still holds for
 * the key — two halves of one rule, and neither is sufficient alone. A neg whose write E7's guard
 * SUPPRESSED does neither: eviction follows the write's fate (see below).
 *
 * It stores nothing because the neg's storage is `writeNegEnvelope`'s single L2 write (§3, §5.1
 * step 5) — that function is the neg's whole write path, and it deliberately writes one tier. What
 * this write-through would put in L1 is a `kind:'val'`-stamped copy of the neg's placeholder body:
 * an envelope claiming to be a VALUE, holding a value no origin produced. Read back, it would
 * surface that placeholder (the read path's neg rule keys on the stored `kind`, which this write
 * misstates) and, worse, stand as a grace/stale-if-error candidate — precisely what §11.7 forbids a
 * neg from ever being. Today the default codec cannot encode `undefined` to a decodable body, so
 * such a write lands as an unreadable entry that always misses; that is an accident of one codec,
 * not a guarantee any other owes us.
 *
 * It evicts because L1 may ALREADY hold an OLDER value for this key. A fill runs when the read
 * found nothing servable — and a tier read that a classified transient failure suppressed (E13) is
 * a plain MISS over an entry that is still sitting there. Refusing the write alone leaves that
 * entry standing, and it outlives the 404: once the tier reads cleanly again L1 is consulted first
 * (§5.1 steps 2/4) and answers with the superseded value, in preference to the neg L2 now holds —
 * the origin's not-found reverted by a tier that was merely unreadable for a moment. Deleting ends
 * the turn with L1 holding nothing for the key, so reads fall through to the tier that remembers
 * the 404 and a served neg still reports the tier and durability its one write proves (E29).
 *
 * Whether a neg SHOULD also LIVE in L1 is a storage-policy question §3 does not settle — it would
 * change that reported tier and durability — so this evicts rather than storing a neg envelope,
 * closing the mislabel and the resurrection without deciding it.
 *
 * EVICTION FOLLOWS THE WRITE'S FATE (E7/INV-26). Both halves above are premised on a neg that
 * LANDED: the eviction exists to stop a superseded L1 copy outliving a 404 the durable tier now
 * remembers. A neg E7's guard suppressed leaves the durable tier remembering no 404 at all — the
 * verdict the suppression rendered is that the val stands — so deleting that val's L1 copy would
 * contradict the verdict on the way out, and cost a refill of a value that is still good. A
 * suppressed write mutates nothing, in either tier. */
async function hydrateL1<T>(
  config: CacheConfig,
  codec: Codec,
  key: string,
  options: GetOrSetOptions<T> | GetOrSetEntryOptions<T>,
  attempt: FilledAttempt<T>,
  fallbackBornEpoch: number
): Promise<void> {
  const { l1 } = config;
  if (!l1) {
    return;
  }
  if (attempt.storedKind === "neg") {
    if (attempt.writeSuppressed) {
      // E7's guard refused the write, so there is no 404 in the durable tier for an L1 copy to
      // outlive — and the val this suppression protected may be exactly what L1 is holding.
      return;
    }
    try {
      await l1.delete(key);
    } catch {
      // Best-effort on the same terms as the write below: L1 is a cache tier, not the record of
      // truth, so a failed eviction never blocks serving the fill. What a failure leaves behind
      // is stated exactly, because this path attaches no expiry: the previous entry stays
      // readable until that tier evicts it on its own terms or a later fill overwrites it. The
      // durable tier still holds the 404, so a caller that misses L1 gets the fact — but a
      // caller that hits the surviving entry is served it. Narrowing that window further means
      // keeping a marker in L1 for a key the origin says is absent, which is the open question
      // of whether negatives may live in L1 at all (§3 does not settle it), and it is not
      // settled here.
    }
    return;
  }
  const { bornMs, tags, value } = attempt;
  const bornEpoch = attempt.bornEpoch ?? fallbackBornEpoch;
  try {
    await l1.set(key, codec.encode(value), {
      metadata: {
        bornEpoch,
        bornMs,
        codecId: codec.id,
        enc: "json",
        kind: "val",
        scope: scopeLiteral(options),
        tags,
        v: 3,
      },
    });
  } catch {
    // L1 hydration is best-effort; a failure never blocks serving the fill.
  }
}

/**
 * The successful foreground fill's serve (§5.1 step 5): record the dependency, hydrate L1, and
 * report the fill's own outcome.
 *
 * This is the one serve that can always answer `durable` (E29 leaves it untouched): it performed
 * the write-back itself and awaited the result, so it reports a real boolean rather than the absent
 * arm the read-path serves need.
 */
async function filledServe<T>(
  config: CacheConfig,
  codec: Codec,
  key: string,
  options: GetOrSetOptions<T> | GetOrSetEntryOptions<T>,
  scope: string,
  attempt: FilledAttempt<T>,
  bornEpoch: number,
  ambient: AmbientCollector | undefined
): Promise<FillOutcome<T>> {
  // A SERVED fill is a consumed dependency (§7, E23): the entry's STORED tag set (E24 — the
  // settle-union, NOT the caller's declared subset) plus the scope literal THIS kernel resolved
  // for it (§6, E17). Both fields ride ONE record off ONE channel — the fill's own outcome — so a
  // singleflight JOINER, which reaches this record with the LEADER's `attempt`, inherits the
  // leader's stored tags and its scope together, never its own `options.tags`. A fill's scope is
  // computed, never read back off a shared surface, so `scope_unreadable` cannot arise here.
  // Recorded on the SERVED outcome only: a skipped/fenced fill produced nothing the response
  // consumed (an explicit `ctx.dependsOn` inside such a factory still stands on its own — it is a
  // declaration, not a consumption).
  const storedTags = attempt.tags;
  recordDependency(ambient, {
    scope: fillScopeEvidence(scope),
    tags: storedTags,
  });
  const filled = attempt.value;
  // Hydrate L1 from the fresh fill so a later read on this isolate hits L1 — this is the L1-only
  // entry's ONLY home; a durable entry also lives in L2. The whole outcome rides in: its own
  // `bornEpoch` (a §5.0 budget-3 retry may have re-stamped it past the outer floor, E16/CACHE-26)
  // so L1 and L2 stay born-coherent, with the outer floor as the fallback the neg-write outcome
  // takes; its STORED tag set (E24) so the L1 copy is tag-coherent with L2 — a later L1 hit
  // replays the same purge handles, never the declared subset; and its stored KIND, on which a
  // neg-write stores nothing and evicts any older L1 entry instead (§3 — see `hydrateL1`).
  await hydrateL1(config, codec, key, options, attempt, bornEpoch);
  return {
    // `durable:false` when the fill was confined to L1 (§6, E17): a `usr:<hash>` scope or a public
    // dev-mode factory that read a guarded accessor (INV-4). A contextless default write resolves
    // to `pub` and lands durably (INV-20/8) — UNLESS its L2 write-back was suppressed by a
    // classified transient failure (§5.0/E13), which `attempt.durable` carries as `false`.
    durable: attempt.durable,
    bornMs: attempt.bornMs,
    skipped: false,
    stale: false,
    tier: "origin",
    value: filled,
  };
}

/**
 * One caller's §5.1-step-5 E7 tail over a SHARED `not_found` fact (§5.2, E7/INV-26, E30). The fill
 * read no candidate of its own (a blind leader), so it recorded the fact and handed it out rather
 * than deciding, from a read that saw nothing, what every waiter should be served. This decides
 * DISPOSITION ONLY — it records nothing, because the recording already happened once,
 * inside the fill's window. It runs on behalf of one caller against ITS OWN read (a joiner that read
 * a copy of its own, or the blind leader itself), so that each caller resolves EXACTLY as a SOLO
 * caller in that same state resolves — an outcome must not depend on which caller won singleflight:
 *
 *   - this caller read a `val` candidate that STILL re-validates as servable now (§11.18): E7 serves
 *     that val `{stale:true}` with THIS candidate's own evidence (tier/durable/age, E24/E29), and — a
 *     404 not being `staleIfError` — WITHOUT `servedOnError`, exactly as a solo sighted caller does.
 *     The gate matches the solo path's: this arm is offered only when `grace` was declared (the solo
 *     fill receives its `staleCandidate` only then).
 *   - otherwise (this caller read no candidate, or its own copy re-validated dead): it takes the
 *     RECORDED neg as its outcome, served `{tier:'origin'}` with that write's own evidence — the same
 *     `filled` serve a solo blind caller performs over its own neg-write.
 *
 * A joiner's disposition is thus judged against its OWN read — its read is the existence proof the
 * blind leader lacked — while the fact itself was recorded exactly once, by the fill that found it.
 */
async function runOwnNotFoundTail<T>(
  config: CacheConfig,
  options: GetOrSetOptions<T> | GetOrSetEntryOptions<T>,
  inval: Invalidation | undefined,
  consistency: Consistency,
  key: string,
  staleCandidate: ServedCandidate<T> | undefined,
  fact: NotFoundFactAttempt<T>,
  outerBornEpoch: number,
  ambient: AmbientCollector | undefined
): Promise<FillOutcome<T>> {
  if (
    options.grace !== undefined &&
    staleCandidate !== undefined &&
    (await gracedStillServable(
      inval,
      consistency,
      staleCandidate.hit.envelope,
      config.defaults?.onUnavailable,
      fact.error
    ))
  ) {
    // E7 against THIS caller's own still-servable copy — served with its own evidence, never the
    // leader's (blind) read. `neg-suppressed` carries no `servedOnError`.
    return gracedServe(negSuppressed(config, key, staleCandidate), ambient);
  }
  // No servable copy of this caller's own ⇒ SERVE the neg the fill already recorded. Nothing is
  // written here: this is the disposition half, and the write half ran once, in-window. `filledServe`
  // gets this caller's OUTER `bornEpoch` as the L1-hydration floor, matching the solo path (a neg
  // carries no outcome `bornEpoch` of its own).
  return await filledServe(
    config,
    codecOf(config),
    key,
    options,
    fact.scope,
    fact.neg,
    outerBornEpoch,
    ambient
  );
}

/**
 * A STALE hit → §5.1 step 6 (SWR). The OBSERVABLE contract, independent of `grace`:
 * **this response always serves the stale value with `stale:true`** — never the
 * refreshed/fresh result. A refresh runs synchronously (the full background /
 * `waitUntil` / REFRESH_QUEUE machinery of step 6 stays deferred; a synchronous
 * refresh is a placeholder that produces the same observable serve), and its result
 * is written through for the NEXT read only. It never changes the value THIS call
 * returns, and its failure never propagates to the caller — SWR's promise is a fast
 * stale response.
 *
 * `servedOnError:true` iff the refresh failed RETRIABLY (per `staleIfError`, §5.4)
 * within a grace window (`grace` declared on the call) AND the stale copy is still
 * servable after serve-time re-validation (§11.18) — the `graced` outcome. A
 * successful refresh, a fenced/non-servable refresh, a suppressed 404 (E7 — a neg
 * never displaces a servable val, and a 404 is not `staleIfError`), and a
 * non-retriable refresh failure all serve the stale value WITHOUT `servedOnError`.
 * `grace` only affects whether the error path yields `servedOnError`, never whether
 * the stale value is served (it always is).
 */
async function refreshOrGrace<T>(
  config: CacheConfig,
  options: GetOrSetOptions<T> | GetOrSetEntryOptions<T>,
  key: string,
  inval: Invalidation | undefined,
  consistency: Consistency,
  tier: Tier,
  hit: ReadHit<T>,
  bornEpoch: number
): Promise<FillOutcome<T>> {
  const codec = codecOf(config);
  // `bornEpoch` is the CURRENT stamp from the caller's `BornMint` (§5.0 names the SWR background
  // refresh among the paths that mint `bornEpoch := appliedEpoch` at fill start) — NOT the stale
  // hit's own epoch. The stale envelope's stamp describes when THAT value was born; minting the
  // refreshed value from it would re-birth it behind every purge the stale copy already trails,
  // so the refresh could never make progress.
  //
  // The stale value is ALWAYS the served value (SWR). The refresh is best-effort for
  // NEXT time: any failure — retriable, non-retriable, or fenced — is swallowed here
  // and never propagates, because the current response is already decided (§5.1
  // step 6). We only inspect the refresh outcome to decide `servedOnError`.
  let graced = false;
  try {
    // NOTE the omitted trailing `ambient` argument — see `fillAndStore`'s parameter doc. A
    // background refresh contributes NOTHING to this response's collector; the stale hit the
    // caller is serving was already recorded as the consumed dependency (§7, E23).
    const attempt = await fillAndStore(
      config,
      options,
      key,
      inval,
      consistency,
      bornEpoch,
      // The refresh's own graced candidate is the stale hit it is refreshing, carried with the
      // tier it was read from so any serve drawn from it reports that tier (E29).
      { hit, tier }
    );
    if (attempt.kind === "filled") {
      // The refresh succeeded — write it through for NEXT time (hydrate L1) off the refresh
      // fill's OWN outcome, exactly as the foreground path hydrates: its stored tag set (E24 —
      // the settle-union) so the refreshed L1 copy stays tag-coherent with the L2 write the same
      // refresh just performed, and its own stamps, because L1 and L2 must agree on the epoch the
      // refreshed value was born at (or the two tiers validate one value differently, §4.1) and
      // on the instant (or they report two ages for it, §3). A refresh whose factory 404'd into a
      // neg-write reaches here too — and evicts instead of storing, per §3 (see `hydrateL1`): this
      // response still serves the stale hit (SWR, decided above), but the copy behind it must not
      // outlive the 404 the refresh just recorded.
      await hydrateL1(config, codec, key, options, attempt, bornEpoch);
    }
    // A retriable refresh failure whose stale copy is still servable, within a grace
    // window, is the stale-if-error serve (§5.4, §11.18). E7's `neg-suppressed` and
    // a `fenced` (no-longer-servable) refresh are NOT error-path serves.
    graced = attempt.kind === "graced" && options.grace !== undefined;
  } catch {
    // A non-retriable refresh failure (`fillAndStore` rethrew): the stale value is
    // still served — a background refresh's failure never becomes the response
    // (§5.1 step 6). It is not an error-path serve, so `servedOnError` stays falsy.
  }
  // The SERVED value is the stale hit — so the evidence reported is the HIT's, drawn from the
  // tier it was actually found in (E29). The refresh that just ran wrote a value for NEXT time;
  // it is not what this response carries, so its outcome is not this serve's testimony.
  return {
    ...serveDurability(tier, hit),
    bornMs: hit.envelope.bornMs,
    skipped: false,
    stale: true,
    tier,
    value: hit.value,
    ...(graced ? { servedOnError: true } : {}),
  };
}

/**
 * The core getOrSet path (§5.1): L1 → L2 → foreground fill with the §5.0 fence and
 * §5.0/E3 born-monotone write-back. Serves fresh directly; serves a soft-stale value
 * with `stale:true`; refills on miss/dead/unknown. Shared by `getOrSet` (plain) and
 * `getOrSetEntry`.
 */
async function runGetOrSet<T>(
  config: CacheConfig,
  options: GetOrSetOptions<T> | GetOrSetEntryOptions<T>,
  inval: Invalidation | undefined,
  nsVersion: NsVersion,
  apiForm: ApiForm,
  inFlight: SingleflightMap,
  ambient: AmbientCollector | undefined
): Promise<FillOutcome<T>> {
  const codec = codecOf(config);
  const scope = scopeLiteral(options);
  const key = canonicalKey(
    nsVersion.current,
    config.namespace,
    scope,
    options.key
  );
  const consistency: Consistency = consistencyOf(config, options);

  // The ONE mint for this read (§3, §5.0). Opening it snapshots the bornEpoch floor
  // SYNCHRONOUSLY here from LOCAL verified knowledge — never a fresh live read, so a cache HIT
  // below performs ZERO registry round-trips (§1) — and every stamp this read hands out, to the
  // foreground fill or to the SWR background refresh, comes back through `mint.stamp`.
  const mint = openBornMint(inval);

  // L1 → L2 read (§5.1 steps 2/4). L1 is isolate-local; L2 is durable. An eventual
  // hit validates against LOCAL knowledge only — no registry round-trip (§1); a
  // `strong` hit (or an unknown-tag fail-closed resolution) spends its one permitted
  // live check inside `validate` (§1, §5a, E1). `checkedThrough` carries the epoch
  // THIS call's own live checks verified — distinct from a concurrent bus frame — so a
  // same-call refill can postdate its own read without INV-9's concurrent purge lifting
  // the fence.
  let checkedThrough = 0;
  // The candidate rides WITH the tier it was found in: a serve reports the tier the value was
  // actually read from (E29), which a bare `ReadHit` no longer remembers once the loop moves on.
  let staleCandidate:
    | { readonly hit: ReadHit<T>; readonly tier: Tier }
    | undefined;
  for (const [store, tier] of tiersOf(config)) {
    const read = await readTier<T>(
      store,
      codec,
      config,
      inval,
      consistency,
      key
    );
    // Folded BEFORE the serve arms below, so a stamp minted on the SWR path postdates the live
    // checks this read already performed — exactly as the foreground refill's does. The arms that
    // return skip nothing by this: each one returns, and the value is monotone.
    checkedThrough = Math.max(checkedThrough, read.checkedThrough);
    if (read.hit) {
      // A stale hit forks on consistency (§1): a `strong` read observes every purge
      // durably accepted before it began, INCLUDING a soft purge — a soft-purged
      // (stale) entry is exactly the observed-purge case, so `strong` must NOT
      // serve it (that would be observably indistinguishable from missing the
      // purge). `eventual`'s SWR contract is unaffected: ANY stale hit → §5.1 step 6
      // — serve the stale value with `stale:true` and run a refresh for next time —
      // the served value is ALWAYS the stale one, never the refresh result (see
      // `refreshOrGrace`). `grace` does NOT gate this; it only affects whether a
      // retriable refresh failure yields `servedOnError` (§5.4). A FRESH hit serves
      // directly with no refresh, at either consistency.
      if (read.hit.stale && consistency !== "strong") {
        // A SERVED (stale/SWR) hit is a consumed dependency (§7, E23): its tags and its
        // RAW stored scope both ride to the ambient collector before the response is
        // decided. The value the caller receives came out of this entry — whatever the
        // background refresh does next.
        recordServedHit(ambient, read.hit);
        return await refreshOrGrace(
          config,
          options,
          key,
          inval,
          consistency,
          tier,
          read.hit,
          mint.stamp(checkedThrough)
        );
      }
      if (!read.hit.stale) {
        // A SERVED (fresh) hit is a consumed dependency (§7, E23).
        recordServedHit(ambient, read.hit);
        return {
          // What THIS serve can prove (E29): an L2 read proves durability; a stored `usr:` scope
          // proves the opposite; an L1 `pub`/`ten:` hit proves neither, and says so by absence.
          ...serveDurability(tier, read.hit),
          bornMs: read.hit.envelope.bornMs,
          skipped: false,
          stale: false,
          tier,
          value: read.hit.value,
        };
      }
      // `strong` + stale: do NOT serve the stale value or fall through to SWR —
      // fall through to the foreground refill below exactly as a dead/unknown miss
      // would, carrying this stale envelope forward only as `staleCandidate` (the
      // error-path grace fallback, §11.18), never as a direct serve.
    }
    // Not servable directly at this consistency (dead/unknown, or strong-observed
    // stale) — but a `val`-kind candidate from a later (more durable) tier still
    // stands as the graced fallback material a `grace`-declared foreground fill
    // re-validates at serve time (§11.18). A later tier's candidate overrides an
    // earlier one's — L2 outlives L1, so it is the more durable source of the
    // last-known-good value.
    const candidate = read.hit?.stale === true ? read.hit : read.staleCandidate;
    if (candidate !== undefined) {
      staleCandidate = { hit: candidate, tier };
    }
  }

  // The refill's stamp, from the same mint the SWR refresh above draws on (§5.0).
  const bornEpoch = mint.stamp(checkedThrough);

  /* References: §5.0, E1, §5.1, §11.3, §5.2, §2.1, E6. */

  const identity = joinIdentity(key, consistency, apiForm, codec, options);
  const attempt = await singleflight(
    inFlight,
    key,
    identity,
    config,
    options,
    async () =>
      await fillAndStoreChecked(
        config,
        options,
        key,
        inval,
        consistency,
        bornEpoch,
        options.grace === undefined ? undefined : staleCandidate,
        ambient
      )
  );
  if (attempt.kind === "filled") {
    return await filledServe(
      config,
      codec,
      key,
      options,
      scope,
      attempt,
      bornEpoch,
      ambient
    );
  }
  if (attempt.kind === "graced" || attempt.kind === "neg-suppressed") {
    // The candidate rides the ATTEMPT, so this reads the evidence of the value being served and
    // consults nothing local. That matters most for a singleflight JOINER (§5.2): it never ran
    // `fillAndStore`, it awaited the leader's promise, and its own reads may have found nothing
    // at all — yet it is handed the leader's value and must describe THAT. Deciding this arm on
    // the caller's own `staleCandidate` would strand every joiner in an outage, refusing the
    // graced value precisely when stale-if-error exists to supply it.
    return gracedServe(attempt, ambient);
  }
  if (attempt.kind === "retriable-failed") {
    // The shared fill produced no serve of its own (a blind leader) and handed out the classified
    // retriable failure as a FACT (§5.2, §5.4). THIS caller runs its own §5.1-step-5 tail against
    // its OWN read — not the leader's disposition. A joiner that read a servable graced copy of its
    // own serves it (with its own evidence, E24/E29); the blind leader, whose `staleCandidate` is
    // undefined, propagates the error. Recourse is per-caller against its own read.
    return await runOwnErrorTail(
      config,
      options,
      inval,
      consistency,
      staleCandidate,
      attempt.error,
      ambient
    );
  }
  if (attempt.kind === "not-found") {
    // The shared fill read nothing of its own (a blind leader), RECORDED the 404 once inside its own
    // window, and handed the fact out (§5.2, E7/INV-26, E30). THIS caller now runs its own E7 tail
    // against its OWN read — disposition only, recording nothing: a joiner that read a still-servable `val`
    // serves that val (its own read is the existence proof the leader lacked); the blind leader,
    // whose `staleCandidate` is undefined, serves the recorded neg. What to SERVE is per-caller,
    // never the leader's to impose; what to STORE was settled once, by the fill.
    return await runOwnNotFoundTail(
      config,
      options,
      inval,
      consistency,
      key,
      staleCandidate,
      attempt,
      bornEpoch,
      ambient
    );
  }
  if (attempt.kind === "skipped") {
    // §2.1/M10/invariant 24: `ctx.skip()` — nothing stored, nothing served.
    // Only reachable via `getOrSetEntry` (`EntryFactoryCtx.skip`); the plain
    // `getOrSet` factory's `FactoryCtx<T>` type has no `skip` member, so this
    // branch is unreachable from that call path by construction (M10).
    return {
      durable: false,
      skipped: true,
      stale: false,
      tier: "miss",
    };
  }
  // Fenced past the fill: never store or serve a fenced value (§5.0/MI6). `value` is
  // omitted — the entry form surfaces it as `undefined`; the plain form rethrows the
  // classified factory error when the fence came from a retriable failure (§5.1 step
  // 5), else falls back to the E1 write-back-fence outcome.
  return {
    durable: false,
    skipped: false,
    stale: false,
    tier: "miss",
    ...(attempt.error === undefined ? {} : { error: attempt.error }),
  };
}

/** Whether a raw regex-captured unit is a known `DurationUnit` (§2.1). */
function isDurationUnit(unit: string | undefined): unit is DurationUnit {
  return unit !== undefined && Object.hasOwn(DURATION_UNIT_MS, unit);
}

/** Convert a `Duration` to milliseconds (§4.5). The full `Duration` grammar (MI28) is
 * deferred; the retention lease (§4.5) and the reader's E5 heartbeat cadence (§4.2) need
 * only a coarse ms value, so this parses the `<number><unit>` shape. */
function durationMs(duration: Duration): number {
  const match = /^(?<value>[0-9]*\.?[0-9]+)(?<unit>ms|s|m|h|d)$/u.exec(
    duration
  );
  const value = Number(match?.groups?.value ?? "0");
  const rawUnit = match?.groups?.unit;
  // The factor table is the shared `DURATION_UNIT_MS` grammar source (duration.ts) — one
  // source for both this coarse parse and `duration()`'s F31 check. Narrow the regex-captured
  // unit to `DurationUnit` before the keyed lookup; an unmatched shape keeps the prior ×1 fallback.
  const factor = isDurationUnit(rawUnit) ? DURATION_UNIT_MS[rawUnit] : 1;
  return value * factor;
}

/* References: §4, §4.2, E4, E20. */

function buildInvalidation(config: CacheConfig): Invalidation | undefined {
  const { registry, bus, l2 } = config;
  if (!registry || !bus) {
    return undefined;
  }
  if (!l2) {
    throw new Error(
      "createCache: a registry+bus instance requires an l2 store (the §4.2 replication mirror the reader resyncs off — E20)"
    );
  }
  // E5 (§4.2): the reader mirrors the coordinator's `REGISTRY_HEARTBEAT_MS` so the derivative
  // silence monitor uses the SAME cadence a live writer refreshes the pointer on. Ledger: a
  // reader interval SHORTER than the coordinator's errs toward early suspicion (safe, Q4-priced);
  // longer toward a late raise. Absent config falls to the kernel's own 30s default.
  // E28: the reader emits its own raw facts (`bus_dial_failed`, `strong_degraded`) through the
  // instance's configured sink, guarded exactly like every other emit. A cache with no sink hands
  // over `undefined` and the reader emits nothing — the events are observability, never correctness.
  const emit = (event: TelemetryEvent): void => {
    emitTelemetry(config, event);
  };
  const heartbeat = config.defaults?.heartbeatInterval;
  return new Invalidation(
    registry,
    l2,
    bus,
    heartbeat === undefined ? undefined : durationMs(heartbeat),
    emit
  );
}

/**
 * A facade → the instance it fronts. THE ONE PLACE an alternate identity for one cache is recorded
 * (§7, §4.2).
 *
 * WHY THIS EXISTS, AND WHY IT IS ONE MECHANISM RATHER THAN A HABIT. Several internal seams hang
 * per-instance state off a `Cache` through a module-private WeakMap, keyed by the object itself. A
 * composition that returns a WRAPPER around a `createCache` instance — `createWorkersCache` does, to
 * hang its freshness carrier on the read path — hands callers an object those maps have never heard
 * of. The address a seam is keyed by is therefore the identity CALLERS HOLD, and a facade that
 * re-registers one seam and forgets another does not fail loudly: it produces a seam that silently
 * answers `undefined` forever. Registering per-seam is the fix that does not generalize — the next
 * seam, or the next facade, reopens it.
 *
 * So identity is aliased ONCE, here, and every seam lookup resolves through `resolveCacheIdentity`
 * BEFORE keying. A new seam is covered because it resolves like the others; a new facade is covered
 * because it aliases like the others. Neither has to remember the list.
 */
const identityAliases = new WeakMap<Cache, Cache>();

/**
 * The instance `cache` stands for — itself, unless it is a facade over another (§7, §4.2).
 *
 * SAME-PACKAGE internal seam, NOT public API (not re-exported from `index.ts`). Every WeakMap keyed
 * by a `Cache` resolves through this before keying, so a facade and the instance it fronts address
 * ONE slot. A cache that is nobody's facade resolves to itself, which is every raw `createCache`
 * instance and therefore the overwhelmingly common path.
 *
 * ONE STEP IS ENOUGH because `aliasCacheIdentity` flattens the chain at registration.
 */
export function resolveCacheIdentity(cache: Cache): Cache {
  return identityAliases.get(cache) ?? cache;
}

/**
 * Record that `facade` is an alternate identity for `inner` (§7, §4.2) — called ONCE, by whatever
 * composition returns a wrapper, at construction. SAME-PACKAGE internal seam, NOT public API.
 *
 * THE TARGET IS RESOLVED AS IT IS STORED, so a facade over a facade lands on the instance at the
 * bottom rather than on the middle link. That keeps `resolveCacheIdentity` a single lookup on the
 * hot path while making a stack of facades correct BY CONSTRUCTION rather than by anyone noticing
 * that it is a stack — the chain is flattened once, here, where it is built.
 */
export function aliasCacheIdentity(facade: Cache, inner: Cache): void {
  identityAliases.set(facade, resolveCacheIdentity(inner));
}

/** Cache instance → its replication poller (§4.2, C2-b-2-b). Module-private internal state: the
 * poller is attached WITHOUT touching the public `Cache` surface. C4-b's request-piggyback driver
 * (the RR adapter) is the READER of this map — the seam arrived with its consumer, so the map is no
 * longer write-only and the `sonarjs/no-unused-collection` suppression that carried the named "C4"
 * expiry is DELETED (its premise is gone; a suppression outliving its premise is a lie). */
const replicationPollers = new WeakMap<Cache, ReplicationPoller>();

/**
 * The replication poller attached to `cache`, or `undefined` for a registry-absent instance (which
 * has no reader to drive — §4.2). SAME-PACKAGE internal seam, NOT public API: it is deliberately not
 * re-exported from `index.ts`, and its ONE consumer is the C4-b request-piggyback tick driver in the
 * RR adapter (`src/adapters/react-router/poll-driver.ts`), which owns the staleness gate + the
 * failure swallow. Kept as narrow as that consumer needs: a `Cache` in, its poller out — no other
 * kernel internal crosses the boundary.
 *
 * Resolves identity first, so a facade over an instance finds that instance's poller. Without that
 * an RR app on a wrapping factory would lose its replication poll SILENTLY — `tickIfStale` reads a
 * missing poller as "registry-absent, nothing to drive" and returns.
 */
export function replicationPollerOf(
  cache: Cache
): ReplicationPoller | undefined {
  return replicationPollers.get(resolveCacheIdentity(cache));
}

/** A `RenderCollector` → its internal scope-bearing writer (§7, E23). Module-private: the
 * public collector interface cannot carry the scope half (a public `dependsOn` has no entry
 * behind it), so the kernel's automatic hit/fill recording reaches it through this map
 * instead of through a widened public type or a cast. */
const collectorStates = new WeakMap<RenderCollector, CollectorState>();

/**
 * An `L3Emission` → how many dependencies the render had consumed when that emission was
 * committed — hits, fills, and explicit `dependsOn` declarations (§7, E23). Same
 * module-private-WeakMap seam as `collectorStates` above, and for a kindred reason: the count is
 * a fact the ADAPTER's header decision needs and no caller of the public type does, and
 * `L3Emission` is a spec-verbatim shape (§7.2) — so the count rides the private channel rather
 * than widening a published surface with a field only this package reads.
 */
const emissionCounts = new WeakMap<L3Emission, number>();

/** Bank an emission's consumed-dependency count on the seam and return the emission (the ONE
 * writer of `emissionCounts` — the count is banked with the emission it belongs to, so the two
 * can never drift apart). */
function withManagedDependencies(
  emission: L3Emission,
  count: number
): L3Emission {
  emissionCounts.set(emission, count);
  return emission;
}

/**
 * How many dependencies the render had consumed when `emission` was committed (§7, E23).
 * SAME-PACKAGE internal seam, NOT public API: deliberately not re-exported from `index.ts`, and
 * its ONE consumer is the React-Router middleware's header decision, where ZERO is the adapter's
 * JURISDICTION FLOOR — a render that consumed no managed dependency gets a safe DEFAULT and
 * nothing more (`private` filled in only where the app wrote no `Cache-Control`, an app-set
 * `Cache-Tag` left where it is, nothing overridden). It is what separates that floor from an
 * eligible render whose dependencies simply emit no tag.
 *
 * An emission this kernel never minted reads as 0, i.e. as the floor — the direction in which the
 * adapter asserts least, which is the safe one for an unrecognized input.
 */
export function managedDependenciesOf(emission: L3Emission): number {
  return emissionCounts.get(emission) ?? 0;
}

/**
 * A `Cache` → the accessor that yields the AMBIENT render collector for the request in
 * flight (§7, E23). Same module-private-WeakMap seam as `replicationPollers` above, and the
 * same reason: the collector must reach the kernel WITHOUT widening the public `Cache`
 * surface and WITHOUT the kernel importing `node:async_hooks` (the request frame is the
 * adapter's — a Workers deployment without the ALS compat flag must still be able to import
 * this module).
 *
 * Keyed by RESOLVED identity, like every other instance-keyed seam here: an adapter binds against
 * the cache the app holds, and the kernel looks up against the instance running the read. Those are
 * different objects whenever a facade is in play, and this map is where they have to meet — a
 * privacy gate that reads an empty collector concludes the render consumed nothing and asserts
 * nothing, which is the loudest possible silence.
 */
const renderCollectors = new WeakMap<
  Cache,
  () => RenderCollector | undefined
>();

/**
 * Bind the adapter's ambient-collector accessor to `cache` (§7, E23). SAME-PACKAGE internal
 * seam, NOT public API: deliberately not re-exported from `index.ts`, and its ONE consumer is
 * the React-Router middleware (`src/adapters/react-router/middleware.ts`), which owns the
 * `AsyncLocalStorage` request frame the accessor reads.
 *
 * The consequence is the safety property, not a limitation: a cache used WITHOUT the adapter
 * has no bound accessor, so the kernel records nothing, no `commitHeaders` ever runs, and no
 * header is ever emitted. Nothing to leak, by construction — it fails closed.
 */
export function bindRenderCollector(
  cache: Cache,
  accessor: () => RenderCollector | undefined
): void {
  renderCollectors.set(resolveCacheIdentity(cache), accessor);
}

/** The ambient collector's internal writer for `cache`, or `undefined` when the adapter bound
 * none / no request frame is open (§7, E23). */
function ambientCollectorOf(cache: Cache): CollectorState | undefined {
  const collector = renderCollectors.get(resolveCacheIdentity(cache))?.();
  return collector === undefined ? undefined : collectorStates.get(collector);
}

/* References: §4.2. */

function buildReplicationPoller(
  config: CacheConfig,
  reader: Invalidation
): ReplicationPoller {
  const { l2, registry } = config;
  if (!l2 || !registry) {
    // Unreachable: a `reader` exists only when `buildInvalidation` validated registry+bus+l2.
    throw new Error(
      "createCache: the replication poller requires the same registry+l2 as the reader (§4.2)"
    );
  }
  const maxSyncLag = config.defaults?.maxSyncLag;
  const options: ReplicationPollerOptions =
    maxSyncLag === undefined ? {} : { pollBaseMs: durationMs(maxSyncLag) };
  return new ReplicationPoller(
    reader,
    l2,
    mirrorKeys(registry.regId),
    config.rng,
    options
  );
}

/**
 * Register this instance's retention lease (§4.5, E11) — DEFERRED TO FIRST USE (E28), best-effort
 * and fire-and-forget; a registration failure never blocks anything (the GC horizon extends under
 * uncertainty, E11).
 *
 * IT USED TO FIRE FROM `createCache`, AND THAT IS AN RPC AT CONSTRUCTION. Construction performs no
 * I/O: at module scope the platform refuses the call outright, and the bare `try{}catch{}` around it
 * — swallowing by design, which is right for a best-effort lease — meant the refusal was invisible.
 * A lease that never registered and never said so is the worst of both: the Coordinator's GC horizon
 * is not extended by an instance it never heard from, and nothing anywhere reports it.
 *
 * Returns a function the first read calls. It runs AT MOST ONCE per instance and never blocks: the
 * lease is a hint to the GC horizon, not a precondition for reading, so making a read wait on it
 * would trade a correctness-neutral registration for real latency on the request that happened to be
 * first.
 */
function retentionRegistrar(config: CacheConfig): () => void {
  const { registry } = config;
  const retention = config.defaults?.maxEntryRetention;
  if (!registry || retention === undefined) {
    return (): void => {
      // Nothing to register: no registry, or no retention declared.
    };
  }
  let registered = false;
  return (): void => {
    if (registered) {
      return;
    }
    registered = true;
    void (async (): Promise<void> => {
      try {
        await registry.registerRetention(durationMs(retention));
      } catch {
        // Best-effort; the GC horizon extends under registration uncertainty (E11).
      }
    })();
  };
}

/** Resolve a purge selector to its registry tag(s) (§2.2). A key selector maps to
 * the reserved `__k` tag digest of that key's canonical form (§3, INV-10) — the
 * IDENTICAL digest `expire`/`delete` by tag would never see, since the literal
 * `__k` form is internal (N3) and only `expire({key})`/`delete({key})` may target
 * it. No `scope` and no request on the key selector (§2.2) ⇒ the same `pub` default
 * (E17 rule 3) a scope-less, contextless `getOrSet` call stamps, so
 * `expire({key: options.key})` targets exactly the tag a matching contextless
 * `getOrSet({key: options.key})` carries. */
function selectorTags(
  selector: ExpireSelector | DeleteSelector,
  config: CacheConfig,
  nsVersion: NsVersion
): readonly string[] {
  if ("tag" in selector) {
    return [selector.tag];
  }
  const canonical = canonicalKey(
    nsVersion.current,
    config.namespace,
    scopeLiteral(),
    selector.key
  );
  return [keyTag(canonical)];
}

/** Wrap a registry ack into a `PurgeResult` (§2.2, E15). `flushed`/`edgePurged`
 * resolve minimally here — the registry mutation already durably accepted + emitted
 * the __delta/__state batch (§4.3 steps 1–3); the KV-acceptance handoff and CDN
 * drain (step 4, MI9/MI10) are deferred to a later cluster. */
function purgeResult(ack: { readonly epoch: number }): PurgeResult {
  return {
    edgePurged: async (): Promise<void> => {
      await Promise.resolve();
    },
    epoch: ack.epoch,
    flushed: async (): Promise<void> => {
      await Promise.resolve();
    },
    matchedHint: "unknown",
  };
}

/**
 * Build a cache instance (§2). Wires the dual-epoch invalidation core when a
 * registry + bus are present; otherwise runs the registry-absent codec path.
 * Not-yet-implemented verbs still throw `NotImplementedError`.
 *
 * CONSTRUCTION PERFORMS NO I/O (E28). What happens here is bookkeeping over the arguments —
 * addresses are recorded, components are pointed at each other, and no door is opened. Every
 * deferred piece (the bus subscription via `Invalidation`'s lazy dial; the E11 retention lease via
 * `registerRetention` below) waits for the first read that needs it and runs inside that read's
 * request context. This is what makes one cache per isolate, built at module scope, a legal and
 * expected shape rather than an isolate that refuses to start.
 */
export function createCache(config: CacheConfig): Cache {
  const inval = buildInvalidation(config);
  const poller =
    inval === undefined ? undefined : buildReplicationPoller(config, inval);
  /** The E11 lease, armed but NOT fired (E28) — the first read pays it, and only the first. */
  const registerRetention = retentionRegistrar(config);
  const nsVersion = new NsVersion();
  // §5.2 makes in-isolate singleflight mandatory (invariant 5): one registry per
  // `createCache()` instance, i.e. one per isolate — `world()` calls
  // `createCache` once per isolate, so this registry never crosses isolate
  // boundaries (the cross-isolate lock, §5.2, is the separate opt-in).
  const inFlight: SingleflightMap = newSingleflight();
  // The ambient render collector for the request in flight (§7, E23), reached through the
  // module-private WeakMap seam above — which is keyed by the instance. The lookup is held in a
  // CELL and wired one line AFTER the instance literal below, because a closure over `instance`
  // written INSIDE that literal would reference the very binding it is initializing. `ambient`
  // itself is stable, so the methods can close over it; what it delegates to is filled in the
  // moment the instance exists.
  //
  // Evaluated PER CALL (never captured once): the adapter binds one accessor at middleware
  // construction and that accessor reads a per-request async-local frame, so the collector this
  // yields belongs to whichever request's async tree the `getOrSet` is executing in. Unbound (no
  // adapter) ⇒ `undefined` ⇒ nothing is recorded and nothing is ever emitted.
  const ambientCell: { lookup: AmbientCollector } = {
    lookup(): CollectorState | undefined {
      // The pre-wiring state, and the resting state of every cache that never meets an adapter:
      // no accessor, hence no collector, hence nothing recorded (§7, E23 — it fails closed).
      return undefined;
    },
  };
  const ambient: AmbientCollector = () => ambientCell.lookup();

  const instance: Cache = {
    async clear(): Promise<PurgeResult> {
      // §2.2/MI1: version bump + a hard watermark on the reserved `__ns:<name>`
      // tag — TWO independent defenses against a lagging isolate ever serving
      // pre-clear data as fresh (INV-23). (1) Bumping `nsVersion` changes every
      // canonical key THIS instance mints from now on, so a future fill is simply
      // unreachable from the old key. (2) The hard purge on `__ns:<name>` rides
      // the SAME epoch/bus/conservative machinery as any other tag purge (every
      // envelope already carries `__ns:<name>`, `envelopeTags`) — so even a
      // lagging replica/isolate that still resolves the OLD canonical key (e.g. it
      // hasn't observed the version bump some other way) fails closed via the
      // ordinary dual-epoch path the moment it reads that stale entry, exactly
      // like any hard-deleted tag (§4.1). `clear()` needs no registry-configured
      // guard beyond `delete`'s own (§2.2 doesn't gate `clear` behind
      // `iUnderstandTheOriginLoad` — that guard is `expireAll`/`deleteAll`-only).
      const { registry } = config;
      if (!registry) {
        return notImplemented("cache.clear (no registry configured)", config);
      }
      // A purge is USE (E28) — see `delete`.
      inval?.ensureSubscribed();
      nsVersion.bump();
      const ack = await registry.delete([nsTag(config.namespace)]);
      return purgeResult(ack);
    },
    collect(): RenderCollector {
      // §7/§7.2/E23: a request-scoped collector. Every dependency the render consumes —
      // a hit, a fill, or an explicit `dependsOn` — is recorded here with its TAGS and its
      // SCOPE evidence. `l3:false` marks a tag droppable for shared-cache purposes, so it
      // is excluded from the "required" set that governs both the late-tag check and the
      // budget check, and from the emitted `cacheTags` (it is never meant to reach a shared
      // cache) — but it is SEVERABLE from the scope gate below and never suppresses the
      // scope evidence. `__k`/`__ns` are reserved and never emitted (MI14) — the automatic
      // hit path DOES see them (they ride every stored envelope), so the reserved-PREFIX
      // filter at commit is load-bearing, not merely defensive.
      const required: string[] = [];
      let committed = false;
      let lateTag = false;
      // E23 scope evidence, accumulated across every consumed dependency.
      let managedDependencies = 0;
      // Any dependency whose scope is readable-but-not-`pub`, OR unreadable. ONE poisoned
      // dependency poisons the response — a mixed render degrades to private.
      let poisoned = false;
      // Any dependency whose stored scope could not be read at all. Strictly the more
      // alarming signal (a scope we cannot verify is an integrity failure, not a policy
      // choice), so it OUTRANKS plain `scope` as the reported reason.
      let scopeUnreadable = false;
      // The per-dependency records, retained for `explain`'s request-scoped attribution (E27 (5)).
      // Request-scoped: this array lives on the collector, which the adapter opens per request and
      // discards after — not the struck persisted graph (E27). Purely additive: the aggregation
      // below is unchanged; nothing reads this except `explain`.
      const records: RecordedDependency[] = [];

      const state: CollectorState = {
        get records(): readonly RecordedDependency[] {
          return records;
        },
        record(dependency: RecordedDependency): void {
          records.push(dependency);
          const { l3, scope, tags } = dependency;
          managedDependencies += 1;
          // The scope is banked FIRST and unconditionally (E23 severability): `l3:false`
          // governs tag EMISSION, never the body's shared-cacheability. Moving the `l3`
          // gate above this line is exactly the leak this ordering exists to prevent.
          if (scope.kind === "unreadable") {
            scopeUnreadable = true;
            poisoned = true;
          } else if (
            scope.kind === "readable" &&
            scope.literal !== PUBLIC_SCOPE
          ) {
            poisoned = true;
          }
          if (l3 === false) {
            return; // droppable: contributes scope, never a tag (§7, E23).
          }
          if (committed) {
            lateTag = true;
            return;
          }
          required.push(...tags);
        },
      };

      const collector: RenderCollector = {
        commitHeaders(budget?: L3Budget): L3Emission {
          const cacheTags = required.filter((tag) => !isReservedTag(tag));

          committed = true;

          // The E23 SCOPE GATE runs FIRST, and it is total: once `poisoned` is set, no later
          // check and no caller-supplied option is consulted, so there is no path by which a
          // route opt-in, a per-tag `l3:false`, or an application-set header can turn the
          // emission back to eligible. The emission carries NO tags either — the adapter can
          // only emit what this returns, so returning an empty list is what makes it
          // impossible for a purge handle to reach the wire on a poisoned response (§7.2).
          if (poisoned) {
            return withManagedDependencies(
              {
                cacheTags: [],
                eligible: false,
                ineligibleReason: scopeUnreadable
                  ? "scope-unreadable"
                  : "scope",
              },
              managedDependencies
            );
          }

          // §7.2's timing + budget rules, UNCHANGED, beneath the scope gate.
          if (lateTag) {
            return withManagedDependencies(
              { cacheTags, eligible: false, ineligibleReason: "late-tag" },
              managedDependencies
            );
          }

          const maxTags = budget?.maxTags;
          if (maxTags !== undefined && cacheTags.length > maxTags) {
            return withManagedDependencies(
              { cacheTags, eligible: false, ineligibleReason: "budget" },
              managedDependencies
            );
          }

          const maxBytes = budget?.maxBytes;
          if (maxBytes !== undefined) {
            // Measure the HEADER, not the tags. The adapter writes the list JOINED by
            // `L3_TAG_SEPARATOR` (§7.2), so the N−1 separator bytes are part of what the
            // response actually carries. Summing the bare tags under-counts by N−1 and lets a
            // header that is over the limit pass a check it was never really measured against —
            // an over-limit `Cache-Tag` is what a CDN rejects or truncates, and a truncated
            // header is lost purge handles. So the budget is measured against the exact string
            // that goes on the wire.
            const byteLength = new TextEncoder().encode(
              cacheTags.join(L3_TAG_SEPARATOR)
            ).length;
            if (byteLength > maxBytes) {
              return withManagedDependencies(
                { cacheTags, eligible: false, ineligibleReason: "budget" },
                managedDependencies
              );
            }
          }

          return withManagedDependencies(
            { cacheTags, eligible: true },
            managedDependencies
          );
        },
        dependsOn(tag: Tag, dependsOptions?: { readonly l3?: boolean }): void {
          // The PUBLIC surface: a bare declaration with no managed entry behind it, so it
          // carries no scope claim and can never poison. (A declaration made from INSIDE a
          // fill goes through `FactoryCtx.dependsOn`, which does carry the fill's scope.)
          state.record({
            scope: { kind: "none" },
            tags: [tag],
            ...(dependsOptions?.l3 === undefined
              ? {}
              : { l3: dependsOptions.l3 }),
          });
        },
      };
      collectorStates.set(collector, state);
      return collector;
    },
    async delete(selector: DeleteSelector): Promise<PurgeResult> {
      // Hard purge (§2.2): max-on-write bump of `hardEpoch`; unrecoverable in
      // registry-consulting tiers. The registry emits the __delta/__state batch and
      // broadcasts the frame before resolving (§4.3 steps 1–3). CDN drain (step 4)
      // is deferred (MI9/MI10).
      const { registry } = config;
      if (!registry) {
        return notImplemented(
          "cache.delete (no registry configured)",
          selector
        );
      }
      // A purge is USE (E28): it is a registry RPC, so it already runs inside a request context,
      // and an instance issuing purges is exactly one that should be listening for them. This is the
      // deferred subscription's other entry point — without it, an instance that only ever purges
      // would never open its channel.
      inval?.ensureSubscribed();
      const ack = await registry.delete(
        selectorTags(selector, config, nsVersion)
      );
      return purgeResult(ack);
    },
    /** Deferred to v0.4 — namespace-wide hard purge behind the origin-load guard (§2.2). */
    async deleteAll(guard: OriginLoadGuard): Promise<PurgeResult> {
      return notImplemented(
        "cache.deleteAll (deferred to v0.4)",
        config,
        guard
      );
    },
    async expire(selector: ExpireSelector): Promise<PurgeResult> {
      // Soft purge (§2.2): max-on-write bump of `softEpoch`; the value lands stale
      // but stays grace/SWR-servable (§4.1). Same registry batch/broadcast as delete.
      const { registry } = config;
      if (!registry) {
        return notImplemented(
          "cache.expire (no registry configured)",
          selector
        );
      }
      // A purge is USE (E28) — see `delete`.
      inval?.ensureSubscribed();
      const ack = await registry.expire(
        selectorTags(selector, config, nsVersion)
      );
      return purgeResult(ack);
    },
    /** Deferred to v0.4 — namespace-wide soft purge behind the origin-load guard (§2.2). */
    async expireAll(guard: OriginLoadGuard): Promise<PurgeResult> {
      return notImplemented(
        "cache.expireAll (deferred to v0.4)",
        config,
        guard
      );
    },
    /**
     * Witness a key's state (§7, E27) — introspection, not testimony: every field is drawn from
     * an ALREADY-KNOWN source, and nothing here mutates state. The tier is a NON-mutating presence
     * probe (`store.get`, never `hydrateL1` — E27 (3)); the verdict is a pure classify over the
     * reader's CURRENT knowledge (no live check, no resync — E27 (2)); the reader state is exposed
     * via read-only accessors (E27 (4)); the request attribution is the bound render collector's
     * request-scoped records (E27 (5)). The key is canonicalized at the DEFAULT (`pub`) scope — the
     * signature carries no scope, so `explain` introspects the default-scoped entry for that key.
     */
    async explain(key: string): Promise<Explanation> {
      const codec = codecOf(config);
      const canonical = canonicalKey(
        nsVersion.current,
        config.namespace,
        scopeLiteral(),
        key
      );
      const reader: ExplainReaderState = {
        appliedEpoch: inval?.appliedEpoch ?? 0,
        // A registry-absent instance has no channel to have established (E28): reporting
        // `never-established` for it is the literal truth, and it is also the honest neutral —
        // "nothing has connected" is exactly the state of a cache with nothing to connect to.
        channel: inval?.channel ?? "never-established",
        suspect: inval?.suspect ?? false,
        terminal: poller?.terminal ?? false,
      };
      const requestDependencies = explainAttribution(ambient);
      const attribution =
        requestDependencies === undefined ? {} : { requestDependencies };

      // Tier is an OBSERVATION (E27 (3)): probe L1 then L2 with a plain `store.get` — the read that
      // does NOT hydrate — and report the first tier whose bytes reconstruct into a decodable
      // envelope for this key. `hydrateL1` (the mutation the normal read performs) is never called.
      // A tier whose read FAILED (E13-suppressed) is remembered, not forgotten: it leaves this loop
      // unable to say the key is absent, because that tier never answered.
      let readFailed = false;
      for (const [store, tier] of tiersOf(config)) {
        const probed = await probeStoredEntry(store, codec, config, canonical);
        if (probed.kind !== "found") {
          readFailed ||= probed.kind === READ_FAILED_KIND;
          continue;
        }
        return {
          identity: {
            bornEpoch: probed.envelope.bornEpoch,
            kind: probed.envelope.kind,
            scope: probed.scope,
            tags: probed.envelope.tags,
            ttl: probed.ttl,
          },
          key,
          kind: "present",
          reader,
          tier,
          verdict: inval
            ? inval.explainVerdict(probed.envelope)
            : FRESH_VERDICT,
          ...attribution,
        };
      }
      if (readFailed) {
        // No tier held the key AND at least one could not be read: whether it exists is unknown, and
        // the witness says so rather than reporting the absence it cannot establish (E27).
        return { key, kind: READ_FAILED_KIND, reader, ...attribution };
      }
      // Absence is a reportable state, not an error (E27 (1)) — no throw.
      return { key, kind: "absent", reader, tier: "miss", ...attribution };
    },
    async getOrSet<T>(options: GetOrSetOptions<T>): Promise<T> {
      // FIRST USE (E28), here at the door, inside a request context where I/O is legal: the E11
      // lease is paid and the bus subscription opens. Both are deferred out of construction, and
      // both belong to the first READ rather than to the first read that happens to hit — a cold
      // fill never classifies anything, so hanging the subscription off `validate` alone would leave
      // an isolate that only ever cold-fills with no channel at all.
      registerRetention();
      inval?.ensureSubscribed();
      const outcome = await runGetOrSet<T>(
        config,
        options,
        inval,
        nsVersion,
        "getOrSet",
        inFlight,
        ambient
      );
      if (outcome.value === undefined && outcome.tier === "miss") {
        if (outcome.error !== undefined) {
          // A retriable factory failure whose graced copy is no longer servable
          // (§5.1 step 5 — a retriable failure with no servable copy left rethrows the typed error): surface the
          // real classified error — a runtime outcome is never a NotImplementedError.
          // oxlint-disable-next-line typescript/only-throw-error -- rethrowing the ORIGINAL factory throwable verbatim (the spec's typed-rethrow rule, §5.1 step 5 — typically the caller's `HttpError`); re-wrapping would erase the typed status the classifier and callers depend on.
          throw outcome.error;
        }
        // The fill was fenced past its write-back attempt budget (§5.0/MI6): no
        // value is servable — a real runtime outcome (a hard delete kept landing
        // mid-fill), never a `NotImplementedError` stub marker.
        throw new FencedError(options.key);
      }
      // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- `runGetOrSet` returns `T | undefined`; a non-miss outcome always carries a real `T` (possibly `undefined` as a legitimate value), so this narrows the served value to the caller's `T`.
      return outcome.value as T;
    },
    async getOrSetEntry<T>(
      options: GetOrSetEntryOptions<T>
    ): Promise<CacheEntry<T>> {
      // First use (E28) — see `getOrSet`.
      registerRetention();
      inval?.ensureSubscribed();
      const outcome = await runGetOrSet<T>(
        config,
        options,
        inval,
        nsVersion,
        "getOrSetEntry",
        inFlight,
        ambient
      );
      const entry: CacheEntry<T> = {
        /* References: §3, E29, E3. */

        age:
          outcome.bornMs === undefined
            ? 0
            : Math.max(0, config.clock.now() - outcome.bornMs),
        // Forwarded STRUCTURALLY (E29): when the serve could not prove durability either way, the
        // property is absent here too. Spreading a conditional — rather than assigning
        // `outcome.durable` — is what keeps absent distinct from `false` under
        // `exactOptionalPropertyTypes`; a plain assignment would materialize an explicit
        // `undefined` and collapse the distinction the field exists to carry.
        ...(outcome.durable === undefined ? {} : { durable: outcome.durable }),
        skipped: outcome.skipped,
        stale: outcome.stale,
        tier: outcome.tier,
        value: outcome.value,
        ...(outcome.servedOnError === undefined
          ? {}
          : { servedOnError: outcome.servedOnError }),
      };
      return entry;
    },
  };
  // The instance now exists, so its own ambient-collector lookup can be wired (see `ambientCell`).
  ambientCell.lookup = () => ambientCollectorOf(instance);
  if (poller !== undefined) {
    // Held for the C4-b request-piggyback poll driver to tick (it reaches it via
    // `replicationPollerOf`); a driver-less embedding still converges reactively via `validate()`.
    replicationPollers.set(instance, poller);
  }
  return instance;
}
