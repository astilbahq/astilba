// The Workers entry — the only place in this app that knows what a binding is.
//
// THIS IS THE INTERPOSITION POINT, and it is worth being exact about why it is here rather than
// anywhere else. The hero factory takes a KV namespace and a Coordinator namespace and composes the
// cache over them. It does not take a fault hook, a test seam, or a chaos switch, and it must never
// grow one — so the only way to make the cache experience a broken KV is to hand it a KV that is
// broken. That is what happens below: the real bindings come out of `env`, go through this demo's
// wrappers, and the wrapped values are what the five-liner receives. The library's view is a
// `KVNamespace` and a `DurableObjectNamespace`, exactly as in production, and its behaviour under
// fault is therefore its real behaviour under fault rather than a mode it was asked to enter.
//
// ONE BINDING THIS APP CANNOT WRAP, and the demo is better for it: `REGISTRY_KV`. The Coordinator
// reads that binding from inside the Durable Object, out of its own `env`, and a Worker cannot
// write a Durable Object's env — the same wall that makes the heartbeat number a deployment step
// rather than a mechanism. So the Coordinator's writes to the mirror always land. What the demo
// faults is the READER's view of that mirror, through the KV binding the reader actually holds,
// which is the honest shape anyway: an outage is something a reader observes, not something a
// writer is told about.

import type { Cache, TelemetryEvent } from "@astilba/cache";
import { createWorkersCache } from "@astilba/cache/cloudflare";
import { env } from "cloudflare:workers";
import { createRequestHandler, RouterContextProvider } from "react-router";

import { HEARTBEAT_INTERVAL_MS, POSTURE } from "../app/bounds";
import type { DemoWorld, EpisodeState, MeasuredRescue, PendingPurge } from "../app/world";
import {
  armScene,
  healEpisode,
  waitUntilContext,
  worldContext,
} from "../app/world";
import { emptyBusStats, emptyKvStats, noFaults } from "../chaos/dials";
import { faultableCoordinator } from "../chaos/faultable-coordinator";
import { faultableKv } from "../chaos/faultable-kv";
import type { SceneId } from "../chaos/script";

// The Durable Object class this Worker HOSTS. The wrangler binding's `class_name` resolves against
// this entry, so the demo both runs the Coordinator and talks to it — one script, two roles.
export { Coordinator } from "@astilba/cache/cloudflare";

/** The ONE identity string: the Durable Object's name, the registry id, and the cache key
 * namespace all derive from it, so the address and the scope cannot drift apart. */
const REGISTRY_NAME = "demo";

/** How many telemetry events the console keeps. A demo that grew a log until the isolate died would
 * be a poor advertisement for anything. */
const EVENT_LOG_LIMIT = 50;

// `async` rather than a bare thunk returning a promise: a function that returns a promise says so in
// its signature. Found by the linter the moment this file stopped being excluded from it — the
// exclusion's premise was that every diagnostic here was a cascade from two unresolvable specifiers,
// and this one was not.
const handler = createRequestHandler(
  async () => await import("virtual:react-router/server-build"),
  import.meta.env.MODE
);

// ── Isolate-lived state: everything that is NOT I/O ─────────────────────────
//
// The dials, the counters, the event log, the armed scene, the rescue stopwatch and any reader an
// episode booted live for the isolate's whole life. A fault armed by one request is still armed for
// the next, which is the only way a console works at all. None of these are I/O objects, so none of
// them are bound to a request.
//
// ONE RECORD, AND THE SHAPE IS THE POINT. Half of this used to be loose `let`s in this file, cleared
// by bare inline statements — and this is the one file in the app no Node-side test can load, so
// those statements answered to nothing. They are now fields on `EpisodeState`, which `healEpisode`
// and `armScene` both write in full, and which the tooth beside `app/world.ts` compares as a whole.
// What can be forgotten in a checklist cannot be forgotten in a record comparison.

const episode: EpisodeState = {
  armed: null,
  busStats: emptyBusStats(),
  dials: noFaults(),
  episodeCache: null,
  events: [],
  kvStats: emptyKvStats(),
  pending: null,
  rescue: null,
};

const sink = (event: TelemetryEvent): void => {
  episode.events.push(event);
  if (episode.events.length > EVENT_LOG_LIMIT) {
    episode.events.shift();
  }
};

/** Parse the Coordinator's heartbeat var as it ACTUALLY arrived, rather than assuming the value this
 * repo's wrangler config sets. Unset means dormant, and dormant is the shipped default. */
function readCoordinatorHeartbeat(raw: string | undefined): number | null {
  if (raw === undefined) {
    return null;
  }
  const parsed = Number(raw);
  return Number.isFinite(parsed) && parsed > 0 ? parsed : null;
}

/**
 * THE CACHE IS BUILT ONCE, HERE, AT MODULE SCOPE — one per isolate, which is the placement the
 * quickstart teaches and the lifetime the design was always for.
 *
 * This app used to rebuild the cache on every request, and the reason was worth the paragraph it
 * got: the factory resolved the Coordinator namespace to a STUB at construction and held it, a
 * Workers stub belongs to the request that minted it, so a cache outliving a request met
 * `Cannot perform I/O on behalf of a different request` the moment it next touched the registry.
 * Rebuilding was the only lifetime that ran. The price was a cold L1 every request and a reader that
 * was newborn every request — one that had observed no silence and could therefore never grow
 * suspicious, no matter how long a fault stayed armed. The page had to print that caveat under every
 * table, because otherwise it showed a system serenely ignoring an outage.
 *
 * the factory holds the ADDRESS — `{namespace, name}` — and mints a stub per use,
 * inside whichever request is running. Construction wires and captures; it performs no I/O, and
 * minting counts as I/O. So this call is legal at module scope, where `cloudflare:workers`' `env` is
 * how bindings are reached before any request exists.
 *
 * WHAT THAT BUYS THIS DEMO: the reader accumulates. It has a channel state that persists across
 * requests, an applied epoch that advances, and silence it can actually observe.
 *
 * AND THE SAME LIFETIME IS WHY ONE SCENE NEEDS A SECOND INSTANCE. This reader's channel came up on
 * the first request anyone made, which is exactly right for every other page here and fatal to the
 * one scene whose subject is a channel that never came up: no amount of arming a dial can un-live a
 * life. So `mintCache` exists, the deaf-reader scene arms and mints, and the page names the rig
 * rather than pretending the long-lived reader is a newborn.
 */
function mintCache(): Cache {
  return createWorkersCache({
    // The real bindings, wrapped. This is the whole of the demo's chaos surface: the library receives
    // a `KVNamespace` and a `DurableObjectNamespace` and cannot tell them from the bindings themselves.
    coordinator: faultableCoordinator(
      env.COORDINATOR,
      episode.dials,
      episode.busStats
    ),
    defaults: {
      // The READER end of the one heartbeat number, passed explicitly rather than inherited — so the
      // number this app documents and the number it runs on are the same number, written down once.
      // The COORDINATOR end is `REGISTRY_HEARTBEAT_MS` in wrangler vars, and nothing checks that the
      // two agree; the console prints both and says so.
      heartbeatInterval: `${HEARTBEAT_INTERVAL_MS / 1000}s`,
      // TURNED DOWN FROM THE SHIPPED 60s, and the page says so wherever it prints a number derived
      // from it. It makes the poll rescue watchable; it does not make it happen. See `bounds.ts`.
      maxSyncLag: `${POSTURE.maxSyncLagMs / 1000}s`,
    },
    kv: faultableKv(env.CACHE_KV, episode.dials, episode.kvStats),
    name: REGISTRY_NAME,
    // The kernel's own raw facts, reachable from the hero path — `bus_dial_failed` and
    // `strong_degraded` among them. This parameter did not exist when this demo was written, and its
    // absence was the finding the console reported; it is here now, so the console reports what it
    // sees instead.
    telemetry: sink,
  });
}

/** The isolate's long-lived cache — built ONCE, at module scope, and the one every page narrates
 * unless an episode booted its own. */
const cache = mintCache();

const world: DemoWorld = {
  activeCache: () => episode.episodeCache ?? cache,
  arm(id: SceneId) {
    // ONE DOOR. Everything this transition resets, it resets by calling the same total function
    // `heal()` calls — see `app/world.ts`. This line used to be `clearFaults` plus a stopwatch
    // assignment, and the previous episode's counters, log and rescue rode through it onto the next
    // scene's page.
    armScene(id, episode, {
      mintCache,
      // PLATFORM: the wall clock, at the app's edge.
      nowMs: Date.now(),
      registryName: REGISTRY_NAME,
    });
  },
  armed: () => episode.armed,
  busStats: episode.busStats,
  coordinatorHeartbeatMs: readCoordinatorHeartbeat(env.REGISTRY_HEARTBEAT_MS),
  dials: episode.dials,
  events: episode.events,
  heal() {
    // TOTAL (see `DemoWorld.heal`), and it is the WHOLE record: the dials, the counters, the log,
    // the armed scene, the rescue stopwatch, and any reader the episode booted. The isolate's
    // long-lived cache is deliberately untouched — its reader's epoch and channel are the SYSTEM's
    // state, and resetting them would be the demo faking an isolate restart it cannot do.
    healEpisode(episode);
  },
  kvStats: episode.kvStats,
  lastRescue: () => episode.rescue,
  // PLATFORM: the wall clock, at the app's edge.
  nowMs: () => Date.now(),
  onEpisodeReader: () => episode.episodeCache !== null,
  pendingPurge: () => episode.pending,
  purgeIssued(purge: Omit<PendingPurge, "atMs">) {
    // PLATFORM: the wall clock, at the app's edge.
    episode.pending = { ...purge, atMs: Date.now() };
    episode.rescue = null;
  },
  registryName: REGISTRY_NAME,
  rescueObserved(observed: MeasuredRescue) {
    episode.rescue = observed;
    episode.pending = null;
  },
  sink,
};

export default {
  // `async`, for the same reason as the build thunk above: a function returning a promise says so.
  //
  // `ctx` is the PER-REQUEST `ExecutionContext`, captured here and nowhere else. Its `waitUntil` is
  // the mechanism the poll tick rides to outlive the response, so a purge learned mid-tick lands
  // instead of being cancelled at teardown. It is seeded onto the REQUEST context (not the
  // module-scope `world`, which outlives every request and must never hold one request's handle —
  // the lifetime law). `_env` is unread: bindings reach the module-scope cache through
  // `cloudflare:workers`' `env`, and this handler only needs the request and its execution context.
  async fetch(
    request: Request,
    _env: Env,
    ctx: ExecutionContext
  ): Promise<Response> {
    const context = new RouterContextProvider();
    context.set(worldContext, world);
    context.set(waitUntilContext, ctx.waitUntil.bind(ctx));
    return await handler(request, context);
  },
} satisfies ExportedHandler<Env>;
