import type {
  BusFrame,
  CasOrder,
  CasRecord,
  Clock,
  Codec,
  Lock,
  Store,
  StoreValue,
  StoreWriteOptions,
} from "@astilba/cache";
import { memory } from "@astilba/cache";
import {
  FakeBus,
  FakeLock,
  FakeRegistry,
  FakeStore,
  SimClock,
} from "@astilba/harness";

import type { BusHarness } from "../runner/bus";
import type { ReaderHarness } from "../runner/reader";
import type { IsolationHarness, RegistryHarness } from "../runner/registry";
import type { StoreHarness } from "../runner/store";
import { adaptInvalidation, buildReaderParts } from "./reader-parts";

// The known-good reference implementations. The sealed v0.1 fakes (FakeStore /
// FakeRegistry / FakeLock) are the references that pass the contracts FIRST; the
// codec/CAS references are small test-local models (no v0.1 fake exists for a
// CAS-capable store — see DIVERGENCES D3). None of this modifies a fake or `src`.

/** The library-shaped safe-JSON reference codec (byte-stable). */
export const referenceCodec: Codec = {
  decode: (value: string): unknown => JSON.parse(value) as unknown,
  encode: (value: unknown): string => JSON.stringify(value),
  id: "conformance-json/1",
};

/**
 * A single-view FakeStore harness on a fresh timeline (skewable via `startMs`). Supplies
 * the `forceThrottle` control (a one-shot that throttles the NEXT write) so the throttle
 * check runs; pass `maxValueBytes` to also arm the oversize ceiling — absent leaves
 * the store unlimited (the v0.1 behavior).
 */
export function makeFakeStoreHarness(
  startMs = 0,
  maxValueBytes?: number
): () => StoreHarness {
  return (): StoreHarness => {
    const clock = new SimClock(startMs);
    const store = new FakeStore(clock, {
      replicas: 1,
      ...(maxValueBytes === undefined ? {} : { maxValueBytes }),
    });
    const replica = store.replica(0);
    let armThrottle = false;
    // `forceThrottle` arms the next write (any key) to throttle: at set-time the actual
    // key is handed to the FakeStore's per-key throttle control, so the contract check
    // need not know which key it writes.
    const wrapped: Store = {
      ...replica,
      set: async (key, value, options): Promise<void> => {
        if (armThrottle) {
          armThrottle = false;
          store.throttleKey(key);
        }
        await replica.set(key, value, options);
      },
    };
    return {
      advance: (ms: number): void => {
        clock.advance(ms);
      },
      forceThrottle: (): void => {
        armThrottle = true;
      },
      store: wrapped,
    };
  };
}

/**
 * A read failure classified in the read contract's own vocabulary, raised by the reference
 * store ITSELF. A class (oxlint's throw rules); recognized STRUCTURALLY by `isStoreReadError`, never
 * by identity — which is the point of the shape-based contract, so this shares nothing with the
 * kernel or with `cloudflareKV`'s own error and is still recognized by the same predicate.
 */
class ReferenceReadFailureError extends Error {
  readonly code = "unavailable";
  readonly retryable = true;

  constructor() {
    super("reference store: read failed (tier unavailable)");
    this.name = "ReferenceReadFailureError";
  }
}

/**
 * A minimal in-memory store that is byte-stable, RCW-honest, AND exposes an atomic
 * compare-and-set (optimistic-concurrency token). Stands in for the CAS-capable real
 * driver (Redis Lua / DO transactional storage) the base Store interface can't model.
 *
 * It also carries the ONE read fault hand in this fixture set (`failNextRead`), and carries it as a
 * DRIVER behavior rather than a wrapper's: classifying a tier's own faults is the shell's job, so a
 * reference that models a real driver must do it where a real driver does — inside `get`. The sealed
 * v0.1 FakeStore cannot host this: its injected read failure is unclassified on purpose (it is what
 * the kernel's rethrow path is FOR), and re-shaping it would edit a sealed fake's semantics to suit
 * a contract.
 */
class MapReferenceStore implements Store {
  readonly #data = new Map<string, string>();
  readonly #cas = new Map<string, CasRecord>();
  #token = 0;
  #failNextRead = false;

  /** Arm the next `get` to fail the way this store's tier fails — classified, from inside. */
  failNextRead(): void {
    this.#failNextRead = true;
  }

  async get(key: string): Promise<StoreValue | undefined> {
    if (this.#failNextRead) {
      this.#failNextRead = false;
      throw new ReferenceReadFailureError();
    }
    const value = this.#data.get(key);
    return value === undefined ? undefined : { value };
  }

  async set(
    key: string,
    value: string,
    _options?: StoreWriteOptions
  ): Promise<void> {
    this.#data.set(key, value);
  }

  async delete(key: string): Promise<void> {
    this.#data.delete(key);
  }

  async seed(key: string, order: CasOrder): Promise<void> {
    this.#token += 1;
    this.#cas.set(key, { order, token: this.#token });
  }

  async load(key: string): Promise<CasRecord | undefined> {
    return this.#cas.get(key);
  }

  async swap(
    key: string,
    expectedToken: number,
    next: CasOrder
  ): Promise<number | undefined> {
    const current = this.#cas.get(key);
    if (!current || current.token !== expectedToken) {
      return undefined;
    }
    this.#token += 1;
    this.#cas.set(key, { order: next, token: this.#token });
    return this.#token;
  }
}

/** A CAS-capable reference store harness (exercises `capabilities.cas`), and the one subject with a
 * READ fault hand — so the read-classification clause RUNS against a store that classifies its own
 * faults, rather than self-skipping everywhere and asserting nothing. */
export function makeCasStoreHarness(): () => StoreHarness {
  return (): StoreHarness => {
    const store = new MapReferenceStore();
    return {
      cas: store,
      forceReadFailure: (): void => {
        store.failNextRead();
      },
      store,
    };
  };
}

/**
 * The kernel `memory()` L1 store as a third Store conformance subject. Built with a
 * controllable test clock so `checkTtlExpiry` RUNS (the `advance` seam mirrors FakeStore)
 * rather than self-skipping, and with `maxBytes` set to the value ceiling so the oversize
 * check over-fills the budget. No `cas` (memory has no compare-and-set → the no-CAS branch
 * runs) and no `forceThrottle` (L1 has no write-rate ceiling → the throttle check self-skips).
 */
export function makeMemoryStoreHarness(
  maxValueBytes: number
): () => StoreHarness {
  return (): StoreHarness => {
    let now = 0;
    const clock: Clock = { now: (): number => now };
    const store = memory({ clock, maxBytes: maxValueBytes });
    return {
      advance: (ms: number): void => {
        now += ms;
      },
      store,
    };
  };
}

/** A FakeRegistry harness with fake-only controls (check-lag, horizon high-water). */
export function makeFakeRegistryHarness(startMs = 0): () => RegistryHarness {
  return (): RegistryHarness => {
    const clock = new SimClock(startMs);
    // Capture the emitted batches off the fake's own bus (broadcast-frame) — the
    // typed `BusFrame`s the machine flushed, for the batch-form checks. No store /
    // JSON round-trip: the frames arrive as objects on the synchronous zero-delay bus.
    const bus = new FakeBus(clock);
    const frames: BusFrame[] = [];
    bus.subscribe((event) => {
      if (event.kind === "frame") {
        frames.push(event.frame);
      }
    });
    const registry = new FakeRegistry(clock, { bus });
    return {
      controls: {
        advance: (ms: number): void => {
          clock.advance(ms);
          registry.pumpChecks(clock.now());
        },
        maxRetentionMs: (): number => registry.maxRegisteredRetentionMs,
        setCheckLag: (ms: number): void => {
          registry.setCheckLag(ms);
        },
      },
      emittedBatches: async (): Promise<readonly BusFrame[]> => [...frames],
      registry,
    };
  };
}

/**
 * The scoped-mirror isolation REFERENCE: `writeUnder(regId)` drives a REAL FakeRegistry
 * configured with `regId` — the machine's flush plan through the shared `executeFlushEffects`,
 * scoping every key via `mirrorKeys(regId)` — into one shared store. Two registries under
 * different regIds therefore write disjoint keyspaces; the isolation check proves it.
 */
export function makeScopedMirrorHarness(): () => IsolationHarness {
  return (): IsolationHarness => {
    const clock = new SimClock();
    const store = new FakeStore(clock, { replicas: 1 });
    const replica = store.replica(0);
    return {
      store: replica,
      writeUnder: async (regId: string): Promise<void> => {
        const registry = new FakeRegistry(clock, { regId, store: replica });
        // One hard purge ⇒ one flush ⇒ `delta:1` + the scoped `state` pointer echoing regId.
        await registry.delete([`iso:${regId}`]);
      },
    };
  };
}

/** A FakeLock reference factory. */
export function makeFakeLockHarness(): () => Lock {
  return (): Lock => new FakeLock();
}

/**
 * The replication-READER REFERENCE: the REAL `Invalidation`, scoped to a
 * NON-default `regId` (the scoped keyspace, like the convergence lane), driven through the
 * `ContractReader` surface. It passes every reader check FIRST; the deliberately-wrong readers
 * (fixtures/broken) each fail the one they target.
 */
export function makeReferenceReaderHarness(
  regId = "reader-x"
): () => ReaderHarness {
  return (): ReaderHarness => {
    const parts = buildReaderParts(regId);
    return {
      keys: parts.keys,
      reader: adaptInvalidation(parts.inval, parts.bus),
      regId: parts.regId,
      store: parts.store,
    };
  };
}

/**
 * A v2 FakeBus reference harness. At zero delay the FakeBus delivers frames
 * synchronously on publish, so no `pump` is exposed; `reconnect`/`dropNext` bridge to
 * the chaos surface so the checks can drive establishment and loss deterministically.
 */
export function makeFakeBusHarness(): () => BusHarness {
  return (): BusHarness => {
    const bus = new FakeBus(new SimClock());
    return {
      bus,
      dropNext: (n: number): void => {
        bus.dropNextFrames(n);
      },
      publish: (frame: BusFrame): void => {
        bus.publish(frame);
      },
      reconnect: (): void => {
        bus.reconnect();
      },
    };
  };
}
