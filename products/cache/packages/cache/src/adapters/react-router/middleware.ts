// @astilba/cache/react-router — the React-Router-v8 root middleware (phase 2, →).
//
// PLATFORM: lives under `src/adapters/**` (the sanctioned platform boundary). Two
// ambient platform surfaces, each behind a `// PLATFORM:` note below: `AsyncLocalStorage`
// (node:async_hooks) for the per-request frames, and the wall clock (`Date.now()`) that stamps the
// poll tick. The `Cache` itself arrives already-built via options (its clock/rng injected
// upstream at `createWorkersCache`); no ambient value ever reaches the kernel except as that tick's
// `nowMs` — which is the same wall clock the factory injected.
//
// SCOPE. This middleware:
//   1. provides a `Cache` into the RR request lifecycle via a `RouterContext<Cache>` (createContext
//      + the request's RouterContextProvider) so loaders/actions read it with `context.get(...)`;
//   2. opens an `AsyncLocalStorage` request frame carrying the request's principal-bearing
//      `RequestContext`, so a `getOrSet` call site reaches it via `currentRequest()` and passes it
// as `request` (the kernel derives `usr:<hash>` from `userId`/`tenant` internally);
//   3. opens a SECOND frame carrying this request's render collector, and binds it to the cache at
//      construction (`bindRenderCollector`) — that is the seam by which every hit and every fill the
// render consumes reaches the collector automatically, with its scope;
// 4. fires the REPLICATION POLL TICK at request START, unawaited, through `ctx.waitUntil` —
//      it races the loaders and never blocks them (see `poll-driver.ts` for the piggyback ruling,
//      the staleness gate, and the honest request-coupled-detection residue);
// 5. after `next()`, commits the collector and applies the header algorithm (below).
//
// AMENDS the UNCONDITIONAL `Cache-Control: private` STAMP — deliberately, and by name.
// An earlier change inscribed that stamp as an avowedly INTERIM posture, pending the change that would
// earn the upgrade. What replaces it is JURISDICTION, not a blanket public upgrade: the adapter now
// speaks only about what it has observed. It still never fabricates a shared-cache directive — it
// emits purge tags when it is safe to, demotes to `private` when it is not, and where it observed
// no dependency at all it fills in a safe default without touching what the application already said.

import { AsyncLocalStorage } from "node:async_hooks";

import { createContext } from "react-router";
import type { MiddlewareFunction, RouterContext } from "react-router";

import {
  bindRenderCollector,
  L3_TAG_SEPARATOR,
  managedDependenciesOf,
} from "../../cache";
import type {
  Cache,
  L3Budget,
  L3Emission,
  L3Ineligibility,
  RenderCollector,
  RequestContext,
} from "../../cache";
import { safeEmit, TELEMETRY_EVENTS } from "../../telemetry";
import type {
  L3IneligibleReason,
  SinkErrorHook,
  TelemetrySink,
} from "../../telemetry";
import { tickIfStale } from "./poll-driver";

/**
 * The RR-v8 middleware argument object (the first parameter RR passes a `MiddlewareFunction`:
 * `{ request, url, params, context }`, `context` being the request's `RouterContextProvider`).
 * Re-exported off `MiddlewareFunction` so the `request` derivation option below is typed without
 * naming react-router's private `DataFunctionArgs`.
 */
export type CacheMiddlewareArgs = Parameters<MiddlewareFunction>[0];

/** The single telemetry event a shared-cache-INELIGIBLE response emits — exactly
 * one per response, naming the reason CLASS in its `reason` field and nothing else. No tag names
 * ride the payload: which dependency did it is a question for `explain()`, and an operator
 * watching this counter only needs to see THAT a route stopped being CDN-cacheable. */
export const L3_INELIGIBLE: typeof TELEMETRY_EVENTS.l3_ineligible =
  TELEMETRY_EVENTS.l3_ineligible;

/**
 * The shared-cache header budget: 16 KB aggregate, ~1,000 tags. (The 1,024-char cap is a
 * purge-API limit, not a header one — header tags carry no per-tag cap.) Overridable per
 * middleware via `CacheMiddlewareOptions.l3Budget`.
 */
export const L3_BUDGET_DEFAULT: L3Budget = {
  maxBytes: 16 * 1024,
  maxTags: 1000,
};

/**
 * The reason CLASS a telemetry consumer sees, mapped from the kernel's `L3Ineligibility` union.
 * TWO VOCABULARIES, ONE MAPPING POINT — deliberately: the kernel's union is kebab (its own existing
 * convention, `late-tag`), while the event vocabulary is snake (`poll_tick_failed`,
 * `singleflight_option_mismatch`), and the design fixes `scope_unreadable` as the literal class name. This
 * function is the ONLY place the two meet; nothing downstream re-spells either.
 */
const REASON_CLASS: Readonly<Record<L3Ineligibility, L3IneligibleReason>> = {
  budget: "budget",
  "late-tag": "late_tag",
  scope: "scope",
  "scope-unreadable": "scope_unreadable",
};

/** The two response headers this adapter governs. `Cache-Control` carries the shared-cache
 * posture; `Cache-Tag` is the CDN purge handle keyed off the render's dependency tags. */
const CACHE_CONTROL = "Cache-Control";
const CACHE_TAG = "Cache-Tag";

/** The one directive the adapter ever WRITES to `Cache-Control`. It never writes
 * `public` or an `s-maxage` — opting into shared caching is the application's call, and the
 * adapter's job is to emit the purge tags when that is safe and to demote when it is not. */
const PRIVATE = "private";

/**
 * `cacheMiddleware` configuration. `cache` is the already-built instance the middleware provides
 * into the request lifecycle (a `createWorkersCache(...)` / `createCache(...)` result). `request`
 * is the OPTIONAL principal derivation: it maps the RR request to the identity fields the kernel
 * reads (`userId`/`tenant`) — the app owns that mapping because identity is app-domain.
 * Omit it and the request carries no derivable principal (the shared `pub` class, 4).
 *
 * `waitUntil` is the Workers `ExecutionContext.waitUntil` the poll tick rides on — pass
 * `context.cloudflare.ctx.waitUntil` (RR's Cloudflare load context) or `waitUntil` from
 * `cloudflare:workers`, bound. It is OPTIONAL and never load-bearing for correctness: without it the
 * tick still fires (unawaited, best-effort) and simply is not kept alive past the response. It is
 * never awaited on the request path either way — the tick races the loaders, it does not gate them.
 *
 * `telemetry` receives the driver's swallowed-tick counter (`poll_tick_failed`) and the single
 * `l3_ineligible` event a demoted response emits. Every delivery to it goes through
 * `safeEmit`, so a throwing/rejecting sink is swallowed; `onSinkError` is the OPTIONAL injectable
 * hook `safeEmit` calls when it swallows such a failure — a sink for reporting the reporter.
 *
 * `l3Budget` overrides the header budget (`L3_BUDGET_DEFAULT`).
 */
export interface CacheMiddlewareOptions {
  readonly cache: Cache;
  readonly request?: (args: CacheMiddlewareArgs) => RequestContext;
  readonly waitUntil?: (promise: Promise<unknown>) => void;
  readonly telemetry?: TelemetrySink;
  readonly onSinkError?: SinkErrorHook;
  readonly l3Budget?: L3Budget;
}

/**
 * The `RouterContext<Cache>` key the middleware writes the cache under. A loader/action reads the
 * request's cache with `context.get(cacheContext)`. Created with no default value, so a `get` in a
 * request the middleware never ran on throws (an honest misconfiguration signal, not a silent miss).
 */
export const cacheContext: RouterContext<Cache> = createContext<Cache>();

/** The empty principal-bearing frame used when no `request` derivation is configured — a request
 * with no adapter-visible identity: `derivePrincipal` finds none ⇒ the `pub` class. */
const EMPTY_REQUEST_CONTEXT: RequestContext = Object.freeze({});

// PLATFORM: `AsyncLocalStorage` is a Workers/Node platform API (node:async_hooks; the fixture's
// wrangler enables the `nodejs_als` compat flag). It holds the per-request frame the middleware
// runs `next()` inside, so any `getOrSet` call site in the request's async tree can reach the
// request's `RequestContext` via `currentRequest()` without threading it through every call.
const requestFrame = new AsyncLocalStorage<RequestContext>();

// PLATFORM: the SECOND `AsyncLocalStorage` frame (node:async_hooks) — this request's render
// collector. Kept as its own frame rather than folded into `requestFrame`'s value so that
// `currentRequest()`'s public contract stays exactly a `RequestContext` (an app reads it and hands
// it straight to `getOrSet`); a merged frame object would either change that public type or need
// unwrapping at both readers. The frames nest, so they open and close together regardless.
//
// The ALS lives HERE, in the adapter, and never in the kernel: `src/**` outside `src/adapters/**` is
// the pure kernel, and a static `node:async_hooks` import on the `./cloudflare` door
// would crash any Workers deployment without the `nodejs_als` compat flag at IMPORT time — a cost
// paid by users who never asked for render collection.
const collectorFrame = new AsyncLocalStorage<RenderCollector>();

/**
 * The current request's principal-bearing `RequestContext`, or `undefined` outside a middleware
 * frame. Pass it straight through to `getOrSet`'s `request` option so the kernel derives the
 * request's scope: `cache.getOrSet({ key, request: currentRequest(), factory })`.
 */
export function currentRequest(): RequestContext | undefined {
  return requestFrame.getStore();
}

/** The `waitUntil` used when the app configures none: the tick still runs, it is simply not held
 * alive past the response (best-effort, and safe — the driver's promise never rejects). */
function floatingWaitUntil(promise: Promise<unknown>): void {
  void promise;
}

/**
 * The header edits one response needs, decided BEFORE anything is written. Keeping the
 * decision separate from the write is what lets the same decision be applied twice — once in place,
 * and (when the response's header guard refuses) once on a rebuilt copy — with no risk of the two
 * attempts disagreeing.
 *
 *   - `cacheControl`: the directive to write, or `undefined` to leave the app's own untouched.
 *   - `cacheTag`: the value to write, `null` to REMOVE any tag present, `undefined` to leave alone.
 */
interface HeaderPosture {
  readonly cacheControl?: string;
  readonly cacheTag?: string | null;
}

/** Nothing to write — the app's headers stand exactly as authored. */
const NO_EDIT: HeaderPosture = Object.freeze({});

/**
 * Decide the posture for the response, given the render's emission and how many
 * dependencies that render consumed (`managed` — hits, fills, and explicit `dependsOn`
 * declarations, read off the kernel's in-package seam; it is deliberately NOT a field of the
 * spec-verbatim public `L3Emission`, which stays exactly the shape names).
 *
 * THREE postures, in precedence order:
 *
 *   1. NO MANAGED DEPENDENCY (the jurisdiction floor). The render consumed nothing this library
 *      manages, so the adapter asserts nothing: it fills in `Cache-Control: private` ONLY where the
 *      application set no cache header at all, and it leaves an app-set `Cache-Tag` alone. A
 *      default-fill, never an override — the adapter has observed nothing that licenses an opinion.
 *      This is also what separates the floor from an ELIGIBLE render that happens to emit no tag:
 *      the emission alone cannot tell those apart, and they get different postures.
 *
 *   2. INELIGIBLE (scope-poisoned, or late-tag/budget). `Cache-Control: private` is FORCED — it
 *      overwrites whatever the application set, because the application's own header is what has to
 *      lose here: the whole point of the branch is a route that asked to be publicly cached while
 *      its render read per-principal content. Any `Cache-Tag` is DELETED, including one the adapter
 *      never assembled: removing it, rather than declining to add one, is what the branch is for —
 *      a downstream cache parses the header off the wire whether or not it stores the response.
 *
 *   3. ELIGIBLE. The adapter's own assembled `Cache-Tag` replaces whatever was there — DEDUPLICATED
 *      here, at the join. One entry read by several loaders in a render reaches the collector once
 *      per consumption, so its tag arrives once per consumption; a header that repeats the same
 *      purge handle buys nothing and costs bytes. The dedup lives HERE and NOT in the collector on
 * purpose: `commitHeaders`'s BUDGET counts every occurrence (that is the measure invariant
 *      13 pins — collapsing duplicates there would make an over-budget tag list come out eligible),
 *      and emitting fewer bytes than were measured is always safe, while measuring fewer bytes than
 *      are emitted is not. The adapter does NOT fabricate public caching — no `public`, no
 *      `s-maxage`, ever. The application opts into shared caching with its own `Cache-Control`; the
 *      adapter's job is to emit the purge tags when that is safe and to demote when it is not. So
 *      the safe `private` default still fills in where the app wrote no cache header.
 */
function decidePosture(
  emission: L3Emission,
  managed: number,
  appSetCacheControl: boolean
): HeaderPosture {
  const safeDefault: HeaderPosture = appSetCacheControl
    ? NO_EDIT
    : { cacheControl: PRIVATE };

  if (managed === 0) {
    return safeDefault;
  }
  if (!emission.eligible) {
    return { cacheControl: PRIVATE, cacheTag: null };
  }

  const emitted = [...new Set(emission.cacheTags)];
  return {
    ...safeDefault,
    cacheTag: emitted.length > 0 ? emitted.join(L3_TAG_SEPARATOR) : null,
  };
}

/** Write one decided posture onto a mutable `Headers`. Throws iff the guard is immutable — which is
 * exactly the signal `applyHeaders` acts on, and never a partial write: an immutable guard rejects
 * the FIRST mutation, so either the whole posture lands or none of it does. */
function writePosture(headers: Headers, posture: HeaderPosture): void {
  if (posture.cacheControl !== undefined) {
    headers.set(CACHE_CONTROL, posture.cacheControl);
  }
  if (posture.cacheTag === null) {
    headers.delete(CACHE_TAG);
  } else if (posture.cacheTag !== undefined) {
    headers.set(CACHE_TAG, posture.cacheTag);
  }
}

/**
 * Rebuild `response` with a MUTABLE header guard and the posture written onto it, preserving status,
 * statusText, body, and every header the original carried (the `Response(body, response)` init form
 * copies all three). `undefined` when the platform refuses to reconstruct the response at all — see
 * `applyHeaders` for why that residue is safe.
 */
function rebuiltWithPosture(
  response: Response,
  posture: HeaderPosture
): Response | undefined {
  try {
    const rebuilt = new Response(response.body, response);
    writePosture(rebuilt.headers, posture);
    return rebuilt;
  } catch {
    return undefined;
  }
}

/**
 * Apply the posture to the response and return the response to send.
 *
 * NOT ALL RESPONSES ARE WRITABLE. A `Response` obtained from `Response.redirect(...)`, from a
 * `fetch()` passthrough, or from `caches.match(...)` carries an IMMUTABLE header guard: every
 * `set`/`delete` on it throws `TypeError`. A middleware that mutates headers unconditionally
 * therefore rejects its own promise on such a response and turns it into a 500 — a route that
 * merely redirects would break. So the write is attempted, and on refusal the SAME decided posture
 * is written onto a rebuilt copy (status, statusText, body and headers all preserved) which is
 * returned in the original's place.
 *
 * THE RESIDUE, and why it is safe: a response the platform will not let us reconstruct either
 * (`Response.error()` and the other sub-200 network-error forms) is returned untouched. Such a
 * response carries NO readable or writable headers at all — there is no app-set `Cache-Control` to
 * override and no `Cache-Tag` for a shared cache to read off it — so declining to write a posture
 * onto it withholds nothing and leaks nothing. The one thing that must never happen — a throw out
 * of the middleware — cannot.
 *
 * The telemetry event an ineligible response owes is emitted exactly once, off the
 * DECISION, so it is independent of which of the three write paths the response takes: one event per
 * response, naming the reason CLASS, carrying no tag names.
 */
function applyHeaders(
  response: Response,
  emission: L3Emission,
  telemetry: TelemetrySink | undefined,
  onSinkError: SinkErrorHook | undefined
): Response {
  // The render's consumed-dependency count, off the kernel's in-package seam — the one
  // fact the header decision needs that the public emission does not carry.
  const managed = managedDependenciesOf(emission);
  const posture = decidePosture(
    emission,
    managed,
    response.headers.has(CACHE_CONTROL)
  );

  if (
    managed > 0 &&
    !emission.eligible &&
    emission.ineligibleReason !== undefined
  ) {
    // Through `safeEmit`: the L3 emit is on the SYNCHRONOUS response path, so an app sink that
    // threw here would reject the middleware's own promise (a 500 on a route that merely demoted).
    safeEmit(
      telemetry,
      {
        reason: REASON_CLASS[emission.ineligibleReason],
        type: L3_INELIGIBLE,
      },
      onSinkError
    );
  }

  if (posture === NO_EDIT) {
    return response;
  }
  try {
    writePosture(response.headers, posture);
    return response;
  } catch {
    // An immutable header guard (a redirect / fetch / cache-API response). Not an error condition
    // — just a response that has to be rebuilt to be stamped.
  }
  return rebuiltWithPosture(response, posture) ?? response;
}

/**
 * Build the RR-v8 root middleware. Always-on in v8 (the `unstable_`/`v8_middleware` flag
 * was removed at 8.0.0): register the returned function in a route module's `middleware` array. It
 * provides the cache into `context`, opens the request + collector frames, fires the replication
 * poll tick (unawaited), `await`s `next()`, commits the collector, and applies the
 * header algorithm.
 */
export function cacheMiddleware(
  options: CacheMiddlewareOptions
): MiddlewareFunction {
  const {
    cache,
    l3Budget = L3_BUDGET_DEFAULT,
    onSinkError,
    request: deriveRequest,
    telemetry,
    waitUntil = floatingWaitUntil,
  } = options;
  // Bind the ambient-collector accessor ONCE, at construction. From here the kernel can
  // reach this request's collector — and ONLY through this binding, so a cache built without the
  // middleware records nothing and emits nothing (it fails closed).
  bindRenderCollector(cache, () => collectorFrame.getStore());
  return async (args, next) => {
    const requestContext = deriveRequest?.(args) ?? EMPTY_REQUEST_CONTEXT;
    const collector = cache.collect();
    return await requestFrame.run(requestContext, async () => {
      // Provide the cache into the RR request lifecycle: loaders read `context.get(cacheContext)`.
      args.context.set(cacheContext, cache);
      // The request-piggyback poll tick, fired at request START and NEVER awaited here: it
      // races the loaders below (a freshness probe must not add its latency to a response). The
      // driver returns `undefined` when the staleness gate holds or the cache has no reader; the
      // promise it does return never rejects (the swallow wrap counts + eats a tick failure).
      // PLATFORM: Workers wall clock — the tick's `nowMs`, the SAME timeline `createWorkersCache`
      // injects into the kernel as its `Clock`. The kernel itself reads no ambient time.
      const tick = tickIfStale(cache, Date.now(), telemetry, onSinkError);
      if (tick !== undefined) {
        waitUntil(tick);
      }
      return await collectorFrame.run(collector, async () => {
        // oxlint-disable-next-line node/callback-return -- `next` is React Router's middleware continuation, NOT a Node error-first callback; its result is awaited so the response headers can be decided, then returned below — not returned immediately.
        const response = await next();
        // Header commit: every dependency the render consumed is now known — a tag
        // discovered after this point is late BY CONSTRUCTION, which is what the L3 header rules require.
        // `applyHeaders` returns the response to SEND: the same object when its headers were
        // writable, a rebuilt copy when they were not (an immutable guard — see its doc comment).
        if (response instanceof Response) {
          return applyHeaders(
            response,
            collector.commitHeaders(l3Budget),
            telemetry,
            onSinkError
          );
        }
        return response;
      });
    });
  };
}
