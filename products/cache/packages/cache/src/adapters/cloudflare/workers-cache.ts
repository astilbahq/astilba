/* References: §2, E22, E19-b. */

// PLATFORM: Cloudflare runtime bindings are confined to this adapter.

import type {
  Cache,
  CacheDefaults,
  CacheEntry,
  GetOrSetEntryOptions,
  GetOrSetOptions,
} from "../../cache";
import {
  aliasCacheIdentity,
  createCache,
  replicationPollerOf,
} from "../../cache";
import type { Clock, Rng } from "../../drivers";
import { memory } from "../../drivers";
import { isRetriableHttp } from "../../http";
import { safeEmit, TELEMETRY_EVENTS } from "../../telemetry";
import type {
  SinkErrorHook,
  TelemetryConfig,
  TelemetrySink,
} from "../../telemetry";
import type { ClientSocket } from "./bus-client";
import type { Coordinator } from "./coordinator";
import { doRegistry } from "./do-registry";
import { cloudflareKV } from "./kv";
import { redialingDoBus } from "./redial";
import type { RedialingBusHandle } from "./redial";

/**
 * `createWorkersCache` configuration (§2/E22 — the SIGNED shape). The required key set is EXACTLY
 * `{ name, kv, coordinator }`; `defaults` is optional. `name` is the ONE identity string: it becomes
 * the Coordinator DO address (`idFromName(name)`), the registry id (`regId`), and the cache key
 * namespace — identity = address (E19-b). `coordinator` is parameterized by `Coordinator` so
 * `.get(idFromName(name))` yields the typed stub `doRegistry` needs.
 */
export interface WorkersCacheConfig {
  readonly name: string;
  readonly kv: KVNamespace;
  readonly coordinator: DurableObjectNamespace<Coordinator>;
  readonly defaults?: CacheDefaults;
  /** A plain sink, or a hosted config that pseudonymizes every string field (§10, MI4). Passed
   * straight through to `createCache`. OPTIONAL, so the signed required key set is untouched — but
   * PRESENT, because without it the events this composition emits (`bus_dial_failed`,
   * `strong_degraded`, …) are unreachable for every hero-path user, which is a health surface that
   * exists only on paper. */
  readonly telemetry?: TelemetrySink | TelemetryConfig;
  /** `unknown` resolves to `'error'` — the read THROWS rather than refilling (E9/E28). An OVERRIDE:
   * it outranks `defaults.unknownPolicy`, including this factory's own safe-posture default. For
   * namespaces under a takedown or legal embargo, where refilling from origin could resurrect the
   * content that had to disappear. Optional; the key set is untouched. */
  readonly takedownSensitive?: boolean;
}

/* References: §4.2, E5. */

const DEFAULT_HEARTBEAT_INTERVAL = "30s";

/**
 * The safe posture `createWorkersCache` stamps unless a caller overrides it (§1/§2, E9): reads are
 * `eventual`, stale-if-error resilience is on (the default HTTP retriable classifier, §5.4/E2), an
 * `unknown` validity fails CLOSED to a live registry check (`registry-check`, never `miss`), and the
 * E5 heartbeat cadence the reader's silence monitor mirrors is the one number above (30s) — the
 * READER end of it; the Coordinator end is a wrangler var no factory can reach (R6).
 * Set EXPLICITLY (not left to `createCache`'s implicit defaults) so the posture is a single, pinned
 * object the factory spreads — a caller's `config.defaults` still wins per-field. Private-by-default
 * (a principal-derived request is `usr:`-scoped, L1-only) is `createCache`'s own scope resolution
 * (§6, E17), inherited unchanged. Internal (a caller never needs it — the factory already merges it
 * with `config.defaults`); the safe-posture tooth proves it BEHAVIORALLY, not by importing it.
 */
const SAFE_DEFAULTS = {
  consistency: "eventual",
  heartbeatInterval: DEFAULT_HEARTBEAT_INTERVAL,
  staleIfError: isRetriableHttp,
  unknownPolicy: "registry-check",
} satisfies CacheDefaults;

/** The Coordinator `/bus` WebSocket endpoint the dial upgrades against (mirrors the DO's router). */
const BUS_URL = "https://coordinator/bus";

/**
 * Minimum ms between two carrier ticks on ONE instance. The ladder's fastest rung (~1s): ticking
 * faster cannot make the poller act sooner (every path it drives is gated by its own due-time), so
 * anything below this is pure waste under load. A burst of 1000 reads/second costs one tick.
 */
const CARRIER_TICK_MIN_INTERVAL_MS = 1000;

/**
 * THE CARRIER (E28's Ticks clause). Anything this library ships as a ready-made composition has to
 * bring a freshness driver with it; this is the one `createWorkersCache` brings.
 *
 * WHY IT HAS TO EXIST. The poller and the redial ladder are both tick-driven and neither runs a
 * timer — a Workers isolate has no free-running timers, and E28 forbids a timer performing I/O
 * anyway. Until now the ONLY thing that ever called a tick was the React Router middleware, so a
 * plain Worker built on this factory had no driver at all: its `__state` poll never ran, its E5
 * silence monitor was never fed, and its redial ladder never advanced. Nothing then held its
 * eventual reads inside the staleness bound INV-25 fixes. Whoever ships the drivers owes them
 * something to drive them.
 *
 * WHY THE READ PATH. It is the only thing a plain Worker is guaranteed to do. Riding the read means
 * freshness observation is request-coupled — detection latency is max(threshold, inter-request gap)
 * — which is the same honest residue the middleware's request-start tick has, and for the same
 * reason it is acceptable: the only reader a late observation could hurt is one that is not reading.
 *
 * ADOPT-DON'T-AWAIT. The tick is fired and NOT awaited: a freshness probe must never add latency to
 * the read that happened to trigger it, and must never fail it. `#run` catches everything, so the
 * returned promise cannot reject and nothing floats as an unhandled rejection.
 *
 * CANCELLATION-TOLERANT, AND HERE IS THE ARGUMENT. An unawaited promise in a Workers request is
 * cancelled when the response is returned — the tick's in-flight I/O simply rejects. That is safe,
 * by three independent properties, none of which is an accident of timing:
 *   (i)   the tick's writes are per-frame atomic and MONOTONE (max-on-write watermarks, a contiguous
 *         applied epoch), so an interrupted pass leaves the reader BEHIND where it would have been —
 *         never ahead. Behind is the fail-closed direction: it costs a live check, never a stale
 *         serve.
 *   (ii)  suspicion is cleared only AFTER a replay completes, so a pass cut short mid-replay cannot
 *         leave the reader falsely converged. The half-applied state is exactly the state it would
 *         have had if the pass had never started, plus some correctly-applied frames.
 *   (iii) the poller's in-flight guard is released in a `finally`, and the rejection a teardown
 *         produces travels through the same `await` any other failure does — so it cannot wedge.
 * The cursor below is written BEFORE the tick is fired for the same family of reasons: a cancelled
 * tick must not leave the gate open for the next read to retry into a hot loop.
 */
function withCarrier(
  cache: Cache,
  bus: RedialingBusHandle,
  clock: Clock,
  sink: TelemetrySink | undefined,
  onSinkError: SinkErrorHook | undefined
): Cache {
  const poller = replicationPollerOf(cache);
  let lastTickMs: number | null = null;

  const run = async (nowMs: number): Promise<void> => {
    try {
      // The bus redial first and synchronously: it is the cheap half (a due-check, and at most one
      // dial), and a channel that comes back up is what lets the poller stop being the only source
      // of freshness.
      bus.tick(nowMs);
      await poller?.onPollTick(nowMs);
    } catch (error: unknown) {
      // The swallow. A background freshness probe has no business failing a user's read — and this
      // is also where a request-teardown cancellation lands, which is precisely why the catch is
      // total rather than selective.
      safeEmit(
        sink,
        {
          error: error instanceof Error ? error.name : "unknown",
          type: TELEMETRY_EVENTS.poll_tick_failed,
        },
        onSinkError
      );
    }
  };

  const carry = (): void => {
    // PLATFORM: Workers wall clock, read through the injected boundary clock — the tick cadence is
    // real-time by nature and no ambient time crosses into the kernel (it enters as `nowMs`).
    const nowMs = clock.now();
    if (
      lastTickMs !== null &&
      nowMs - lastTickMs < CARRIER_TICK_MIN_INTERVAL_MS
    ) {
      return;
    }
    // COMMIT BEFORE FIRING: the cursor moves first, so a tick that is cancelled (or that fails) does
    // not reopen the gate for the very next read.
    lastTickMs = nowMs;
    void run(nowMs);
  };

  const carried: Cache = {
    ...cache,
    async getOrSet<T>(options: GetOrSetOptions<T>): Promise<T> {
      carry();
      return await cache.getOrSet(options);
    },
    async getOrSetEntry<T>(
      options: GetOrSetEntryOptions<T>
    ): Promise<CacheEntry<T>> {
      carry();
      return await cache.getOrSetEntry(options);
    },
  };
  // IDENTITY, ALIASED ONCE. `carried` is what callers hold; `cache` is what the kernel's
  // instance-keyed seams are registered against. This one line is what makes those the same
  // address everywhere — the replication poller, the render collector the privacy gate reads, the
  // poll driver's gate, and any seam added later, none of which this function names or knows about.
  // Registering seams one at a time from here is what failed before: the poller was re-attached and
  // the collector was not, so E23's scope gate read an empty render and a per-principal body went
  // out declared shared-cacheable.
  aliasCacheIdentity(carried, cache);
  return carried;
}

/** L1 bounds for the Workers profile (§2/E22 example): 512 entries, 5 MB. */
const L1_MAX_ENTRIES = 512;
const L1_MAX_BYTES = 5_000_000;

/**
 * Build a Workers-native `Cache` from one registry `name` + a KV namespace + a Coordinator DO
 * namespace (§2/E22). Sugar over `createCache`: the wall clock and RNG enter here at the platform
 * boundary and reach the kernel only as the injected `clock`/`rng`; the drivers are composed against
 * the single `name` (DO stub, registry id, key namespace). A caller's `defaults` override the safe
 * posture per-field; everything else is fixed.
 *
 * WHY THE L1 IS NOT OPTIONAL HERE, AND WHAT IT COSTS TO SKIP IT. This factory always composes an L1,
 * and under an L2 outage that L1 is the absorber. A failing L2 read is suppressed to a plain miss
 * (E13), so the reader refills from origin — and with an L1 the refill lands there, is checked first
 * on every subsequent read (§5.1), and the failing tier is never consulted again: one origin call,
 * however long the outage runs. Take the L1 away and nothing catches the refill. The read suppresses
 * to a miss, the write-back's read-compare cannot read, so nothing lands durably (`durable: false`),
 * and the next call repeats the whole trip — every call, forever, for as long as L2 is down. So a
 * raw `createCache` composed WITHOUT an `l1` converts an L2 outage into an unbounded origin
 * stampede, exactly when the origin is least able to absorb one. That is a priced consequence of
 * E13's miss-class posture, not a defect — a suppressed read must refill, and a refill it cannot
 * cache must happen again. Compose an L1, or accept the bill.
 *
 * ONE DEPLOYMENT STEP THIS FACTORY CANNOT DO FOR YOU (R6). A Worker cannot write a Durable Object's
 * `env`, so the Coordinator's idle E5 heartbeat is seeded ONLY by its own wrangler config, and it
 * ships DORMANT. Recommended production posture — set it to the same 30s this reader defaults to
 * (see `DEFAULT_HEARTBEAT_INTERVAL` for the full price of leaving it unset, and for why a mismatch
 * is safe-direction rather than a bug):
 *
 *     // wrangler.jsonc (the Worker that hosts the Coordinator Durable Object)
 *     "vars": { "REGISTRY_HEARTBEAT_MS": "30000" }
 */
export function createWorkersCache(config: WorkersCacheConfig): Cache {
  const { name, kv, coordinator } = config;
  const clock: Clock = {
    // PLATFORM: Workers wall clock; reaches the pure kernel ONLY via the injected `clock` below.
    now: () => Date.now(),
  };
  const rng: Rng = {
    // PLATFORM: Workers RNG; kernel randomness is injected — this boundary is the only ambient source.
    // oxlint-disable-next-line sonarjs/pseudo-random -- adapter jitter is non-cryptographic; the kernel receives randomness through its Rng input
    next: () => Math.random(),
  };
  // ── THE ADDRESS, NOT THE CONNECTION (E28) ───────────────────────────────────────────────────────
  // Identity = address (E19-b): `name` ⇒ the DO address AND the registry id. What this factory
  // captures is the pair {namespace, name} — a recipe — and NOTHING is minted here. `idFromName` is
  // pure arithmetic over the name and would be legal; `.get()` is the mint, and a mint is I/O. At
  // module scope (the placement the quickstart teaches) the platform refuses it outright and the
  // isolate never starts; past that, a stub minted once and held is refused by every request except
  // the one that made it. So the thunk is the whole design: called per use, from inside whichever
  // request is running, it hands that request a handle of its own.
  const stubFor = (): DurableObjectStub<Coordinator> =>
    coordinator.get(coordinator.idFromName(name));

  const dial = async (): Promise<ClientSocket> => {
    // PLATFORM: ambient Workers `fetch` WebSocket upgrade against a freshly-minted Coordinator stub.
    const response = await stubFor().fetch(BUS_URL, {
      headers: { Upgrade: "websocket" },
    });
    const socket = response.webSocket;
    if (!socket) {
      throw new Error(
        `astilba/cache: expected a 101 webSocket from the Coordinator /bus endpoint, got status ${response.status}`
      );
    }
    socket.accept();
    return socket;
  };
  const { telemetry } = config;
  const sink = typeof telemetry === "function" ? telemetry : telemetry?.sink;
  const onSinkError =
    typeof telemetry === "function" ? undefined : telemetry?.onSinkError;
  /* References: E28. */

  const bus = redialingDoBus(dial, {
    regId: name,
    rng,
  });
  const cache = createCache({
    namespace: name,
    clock,
    rng,
    // RIDER (ii): pass the boundary clock into memory() so a future TTL-to-L1 residency is honest.
    l1: memory({ clock, maxEntries: L1_MAX_ENTRIES, maxBytes: L1_MAX_BYTES }),
    l2: cloudflareKV(kv),
    registry: doRegistry(stubFor, name),
    bus,
    defaults: { ...SAFE_DEFAULTS, ...config.defaults },
    ...(telemetry === undefined ? {} : { telemetry }),
    ...(config.takedownSensitive === undefined
      ? {}
      : { takedownSensitive: config.takedownSensitive }),
  });
  return withCarrier(cache, bus, clock, sink, onSinkError);
}
