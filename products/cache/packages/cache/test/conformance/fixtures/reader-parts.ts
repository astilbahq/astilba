import type {
  Bus,
  BusEvent,
  Envelope,
  Registry,
  RegistryAck,
  StoreValue,
  Subscription,
  TagKnowledge,
  Validity,
} from "@astilba/cache";
import { mirrorKeys } from "@astilba/cache/registry";
import type { MirrorKeys, StatePointer } from "@astilba/cache/registry";

import { Invalidation } from "../../../src/invalidation";
import type {
  ResyncPolicy,
  ResyncStatus,
  SnapshotPolicy,
} from "../../../src/invalidation";
import type { ContractReader, RecordingStore } from "../runner/reader";

// Shared building blocks for the replication-READER contract (runner/reader.ts). The
// reference reader (fixtures/reference.ts) and the deliberately-wrong readers
// (fixtures/broken) both compose these: a read-LOGGING store (for the over-fetch check),
// a controllable bus (explicit reset/hello/gap/frame injection — no auto-establishment),
// and a trivial registry (only `regId` + a benign `check` matter to the reader). None of
// this modifies `src` — it drives the REAL `Invalidation` as the reference.

/** A Map-backed store whose reads are LOGGED (over-fetch discipline). Seeding uses `set`
 * (not logged); the reader's `get`s are logged so a check can prove nothing past the pointer
 * was probed. */
class RecordingMapStore implements RecordingStore {
  readonly #data = new Map<string, string>();
  readonly #reads: string[] = [];

  async get(key: string): Promise<StoreValue | undefined> {
    this.#reads.push(key);
    const value = this.#data.get(key);
    return value === undefined ? undefined : { value };
  }

  async set(key: string, value: string): Promise<void> {
    this.#data.set(key, value);
  }

  async delete(key: string): Promise<void> {
    this.#data.delete(key);
  }

  reads(): readonly string[] {
    return [...this.#reads];
  }

  clearReads(): void {
    this.#reads.length = 0;
  }
}

/** A bus the check drives explicitly. It does NOT auto-emit on subscribe — the
 * reader starts in a clean, non-suspect state and every event (reset/hello/gap/frame) is injected
 * by the check, so scenarios are fully deterministic. */
export class ControllableBus implements Bus {
  readonly #handlers = new Set<(event: BusEvent) => void>();

  subscribe(onEvent: (event: BusEvent) => void): Subscription {
    this.#handlers.add(onEvent);
    return {
      close: (): void => {
        this.#handlers.delete(onEvent);
      },
    };
  }

  emit(event: BusEvent): void {
    for (const handler of this.#handlers) {
      handler(event);
    }
  }
}

/** A trivial registry: only `regId` (scoping) and a benign `check` are exercised by the
 * reader (the eventual+`miss` verdict path never consults it). */
class StubRegistry implements Registry {
  readonly regId: string;

  constructor(regId: string) {
    this.regId = regId;
  }

  async check(tags: readonly string[]): Promise<readonly TagKnowledge[]> {
    return tags.map((): TagKnowledge => ({
      hardEpoch: 0,
      known: true,
      softEpoch: 0,
      throughEpoch: 0,
    }));
  }

  async expire(_tags: readonly string[]): Promise<RegistryAck> {
    return { epoch: 0 };
  }

  async delete(_tags: readonly string[]): Promise<RegistryAck> {
    return { epoch: 0 };
  }

  async registerRetention(_retentionMs: number): Promise<void> {
    // No horizon state — a reader never registers retention.
  }
}

/** A minimal eventual-read probe envelope over `tags` born at `bornEpoch`. Only `tags` +
 * `bornEpoch` steer the validity table; the rest are inert placeholders. */
export function probeEnvelope(
  tags: readonly string[],
  bornEpoch: number
): Envelope {
  return {
    bornEpoch,
    bornMs: 0,
    codecId: "probe/1",
    enc: "json",
    grace: 0,
    key: "conformance:reader:probe",
    kind: "val",
    scope: "pub",
    size: 0,
    src: "probe",
    storedAt: 0,
    tags,
    ttl: 0,
    v: 3,
    val: null,
  };
}

/** Adapt a real `Invalidation` + its controllable bus to the `ContractReader` surface. */
export function adaptInvalidation(
  inval: Invalidation,
  bus: ControllableBus
): ContractReader {
  return {
    applyBusEvent: (event: BusEvent): void => {
      bus.emit(event);
    },
    notePointerObservation: (pointer: StatePointer, nowMs: number): void => {
      inval.notePointerObservation(pointer, nowMs);
    },
    pollDue: (
      nowMs: number,
      lastPollMs: number,
      thresholdMs: number
    ): boolean => inval.pollDue(nowMs, lastPollMs, thresholdMs),
    resync: async (policy?: ResyncPolicy): Promise<ResyncStatus> =>
      await inval.resync(policy),
    resyncViaSnapshot: async (policy?: SnapshotPolicy): Promise<ResyncStatus> =>
      await inval.resyncViaSnapshot(policy),
    suspect: (): boolean => inval.suspect,
    verdict: async (
      tags: readonly string[],
      bornEpoch: number
    ): Promise<Validity> => {
      const { validity } = await inval.validate(
        probeEnvelope(tags, bornEpoch),
        "eventual",
        "miss"
      );
      return validity;
    },
  };
}

/** The raw parts of a reader scenario: the real `Invalidation` plus the store/bus/keys the
 * contract drives and seeds. */
export interface ReaderParts {
  readonly store: RecordingStore;
  readonly bus: ControllableBus;
  readonly inval: Invalidation;
  readonly keys: MirrorKeys;
  readonly regId: string;
}

/** Construct a fresh reader scenario scoped to `regId` ( a NON-default id exercises the
 * scoped keyspace, like the convergence lane). */
export function buildReaderParts(regId: string): ReaderParts {
  const store = new RecordingMapStore();
  const bus = new ControllableBus();
  const inval = new Invalidation(new StubRegistry(regId), store, bus);
  // PUT THE READER IN USE. Construction no longer subscribes — it captures the bus and defers
  // the dial to the first read, so that one cache per isolate can be built at module scope where the
  // platform forbids I/O. These contracts drive a reader in its STEADY state (they publish frames
  // and expect them applied), so the fixture states that explicitly rather than relying on a
  // constructor side effect it no longer has. This is the same thing a first `validate` would do.
  inval.ensureSubscribed();
  return { bus, inval, keys: mirrorKeys(regId), regId, store };
}
