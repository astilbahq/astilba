import { createCache } from "@astilba/cache";
import type { Cache, CacheDefaults, Consistency } from "@astilba/cache";
import { DEFAULT_REG_ID, mirrorKeys } from "@astilba/cache/registry";

import { FakeBus } from "./bus";
import { SimClock, SimRng, SkewedClock } from "./clock";
import { FakeLock } from "./lock";
import { FakeRegistry } from "./registry";
import { FakeStore } from "./store";

// world(config) composes M isolates over shared FakeStore replicas + a shared
// FakeRegistry/FakeBus/FakeLock. Each isolate gets its own L1, its own (skewable)
// clock, and its own cache. Exposes the chaos surface the invariant traces drive.

/** The default fast-check-independent SimRng seed for kernel randomness. */
export const DEFAULT_RNG_SEED = 0x5e_ed;

/**
 * The harness-owned logical→physical mirror-key resolver. Chaos knobs address the
 * replication mirror keys by their LOGICAL name (`"__state"`, `"__delta:<seq>"`, `"__snap:<seq>"`,
 * as the sacred traces do); the harness resolves each to the world's PHYSICAL store key through
 * the SAME `mirrorKeys(regId)` the kernel writes with — the mapping is the HARNESS's, not the
 * kernel's, so the sacred `delayKey("__state", …)` call sites keep biting after registry scoping moved the
 * physical keys to `__reg:<regId>:…` without the traces moving.
 */
function makeMirrorKeyResolver(regId: string): (logical: string) => string {
  const keys = mirrorKeys(regId);
  return (logical: string): string => {
    if (logical === "__state") {
      return keys.stateKey;
    }
    const delta = /^__delta:(?<seq>\d+)$/u.exec(logical);
    if (delta?.groups?.seq !== undefined) {
      return keys.deltaKey(Number(delta.groups.seq));
    }
    const snap = /^__snap:(?<seq>\d+)$/u.exec(logical);
    if (snap?.groups?.seq !== undefined) {
      return keys.snapKey(Number(snap.groups.seq));
    }
    return logical;
  };
}

export interface WorldConfig {
  /** Number of isolates (M). Default 2. */
  readonly isolates?: number;
  /** Number of shared-L2 replica views. Default = isolates. */
  readonly replicas?: number;
  readonly namespace?: string;
  readonly defaults?: CacheDefaults;
  readonly consistency?: Consistency;
  readonly rngSeed?: number;
  /** Enable dev mode (guarded-accessor Proxy demotion + mismatch-throws). */
  readonly dev?: boolean;
  /** The registry id scoping the mirror keyspace. Default `"default"`. The shared
   * FakeRegistry is configured with it and the chaos knobs resolve logical mirror keys against
   * it, so a non-default world exercises the scoped keyspace end-to-end. */
  readonly regId?: string;
}

export interface Isolate {
  readonly index: number;
  readonly cache: Cache;
  /** This isolate's private L1 (isolate-local). */
  readonly l1: FakeStore;
  /** This isolate's clock — skew it to hold a higher/lower wall-clock (trace). */
  readonly clock: SkewedClock;
}

/** The chaos surface (triggers) the invariant traces drive. */
export interface WorldChaos {
  dropNextFrames(n: number): void;
  delayReplica(index: number, ms: number): void;
  delayKey(key: string, ms: number): void;
  /** Buffer subsequent published frames for `ms` before delivery. Lifted
   * from `bus.delay` onto the chaos surface so a command drives out-of-order delivery through the
   * LOGICAL knob, never reaching into `world.bus` (the fourth-class-key anti-pattern in transport
   * costume). Buffering is also the precondition for `reorderFrames` — a swap needs two frames in
   * flight at once, which only a non-zero delay produces. */
  delayFrames(ms: number): void;
  /** Swap the next two BUFFERED frames so they arrive out of order. Lifted
   * from `bus.reorderNext`; a no-op unless `delayFrames` has two frames buffered when the second
   * publishes. Out-of-order delivery is the reorder dimension's perturbation. */
  reorderFrames(): void;
  disconnect(): void;
  reconnect(): void;
  crashIsolate(index: number): void;
  throttleKey(key: string): void;
  /** Shift isolate `index`'s wall clock by `ms` (bornMs only; epochs unaffected). */
  skewClock(index: number, ms: number): void;
  /** Inject a registry `check` round-trip latency (the DO/primary read cost). */
  setCheckLag(ms: number): void;
  /**
   * Inject the logical ms a WRITE to the shared L2 store costs ( a durable write is not
   * free). The clock advances by `ms` inside each `set`, so a caller that awaits a write-back
   * observes time having passed. Distinct from `delayReplica`/`delayKey`, which hold a COMPLETED
   * write invisible (replication lag) and move no clock. Default 0 — writes are free unless a
   * trace asks otherwise, so this perturbs nothing that does not opt in.
   */
  setL2WriteCost(ms: number): void;
}

export interface World {
  readonly clock: SimClock;
  readonly rng: SimRng;
  /** The shared durable L2 (replicated). */
  readonly store: FakeStore;
  readonly registry: FakeRegistry;
  readonly bus: FakeBus;
  /** The shared cross-isolate lock (monotonic fencing tokens). */
  readonly lock: FakeLock;
  readonly chaos: WorldChaos;
  isolate(index: number): Isolate;
  readonly isolateCount: number;
  /** Advance logical time, deliver due bus frames, resolve due checks, settle microtasks. */
  advance(ms: number): Promise<void>;
}

export function world(config: WorldConfig = {}): World {
  const isolateCount = config.isolates ?? 2;
  const replicas = config.replicas ?? isolateCount;
  const namespace = config.namespace ?? "test";
  const clock = new SimClock();
  const rng = new SimRng(config.rngSeed ?? DEFAULT_RNG_SEED);
  const store = new FakeStore(clock, { replicas });
  const bus = new FakeBus(clock);
  const regId = config.regId ?? DEFAULT_REG_ID;
  const registry = new FakeRegistry(clock, {
    bus,
    regId,
    store: store.replica(0),
  });
  const resolveMirrorKey = makeMirrorKeyResolver(regId);
  const lock = new FakeLock();
  const isolateClocks: SkewedClock[] = [];

  const buildIsolate = (index: number): Isolate => {
    const l1 = new FakeStore(clock, { replicas: 1 });
    const isolateClock = new SkewedClock(clock);
    isolateClocks[index] = isolateClock;
    const cache = createCache({
      bus,
      clock: isolateClock,
      l1: l1.replica(0),
      l2: store.replica(index % replicas),
      lock,
      namespace,
      registry,
      rng,
      ...(config.dev === undefined ? {} : { dev: config.dev }),
      ...(config.defaults === undefined ? {} : { defaults: config.defaults }),
    });
    return { cache, clock: isolateClock, index, l1 };
  };

  const isolates: Isolate[] = [];
  for (let i = 0; i < isolateCount; i++) {
    isolates.push(buildIsolate(i));
  }

  return {
    async advance(ms: number): Promise<void> {
      clock.advance(ms);
      bus.deliverDue(clock.now());
      registry.pumpChecks(clock.now());
      await clock.runMicrotasks();
    },
    bus,
    chaos: {
      crashIsolate: (index) => {
        // Models an isolate losing its in-memory L1 + tag-knowledge: rebuild it.
        isolates[index] = buildIsolate(index);
      },
      delayFrames: (msDelay) => {
        bus.delay(msDelay);
      },
      delayKey: (key, msDelay) => {
        store.delayKey(resolveMirrorKey(key), msDelay);
      },
      delayReplica: (index, msDelay) => {
        store.delayReplica(index, msDelay);
      },
      disconnect: () => {
        bus.disconnect();
      },
      dropNextFrames: (n) => {
        bus.dropNextFrames(n);
      },
      reconnect: () => {
        bus.reconnect();
      },
      reorderFrames: () => {
        bus.reorderNext();
      },
      setCheckLag: (ms) => {
        registry.setCheckLag(ms);
      },
      setL2WriteCost: (ms) => {
        store.setWriteCost(ms);
      },
      skewClock: (index, ms) => {
        isolateClocks[index]?.setSkew(ms);
      },
      throttleKey: (key) => {
        store.throttleKey(resolveMirrorKey(key));
      },
    },
    clock,
    isolate(index: number): Isolate {
      const iso = isolates[index];
      if (!iso) {
        throw new Error(
          `world.isolate: index ${index} out of range [0,${isolateCount})`
        );
      }
      return iso;
    },
    isolateCount,
    lock,
    registry,
    rng,
    store,
  };
}
