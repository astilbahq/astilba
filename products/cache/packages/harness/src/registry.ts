import type {
  BusFrame,
  Registry,
  RegistryAck,
  Store,
  TagKnowledge,
} from "@astilba/cache";
import { executeFlushEffects, RegistryMachine } from "@astilba/cache/registry";
import type {
  FlushSink,
  FlushTelemetryEvent,
  ImmutableKind,
} from "@astilba/cache/registry";

import type { FakeBus } from "./bus";
import type { SimClock } from "./clock";

// Re-exported so the harness surface still carries the pointer shape (§4.2).
export type { StatePointer } from "@astilba/cache/registry";

// FakeRegistry — the authoritative Coordinator (§4.1–§4.3), a THIN HOST around the pure
// RegistryMachine (@astilba/cache/registry). The machine owns the semantics — dual watermark
// epochs (independent, monotone, max-on-write), strictly-monotone epoch issuance, per-flush
// content-addressed delta batches (§4.2, E12), the snapshot cadence, and the monotone GC
// horizon (§4.5, E11). This host keeps only its Sim-plumbing bits — the injected clock, the
// check round-trip chaos, the Sim bus/store — and drives the machine's flush effect plan
// through the SHARED `executeFlushEffects` (the same executor the real Coordinator uses), so
// write order + the once-only `__state` bless live in ONE place. Its only host policy is
// window-0: a mutation's schedule-flush is honored by flushing immediately. The public surface
// is unchanged, so the invariant traces and the Registry conformance contract do not move.

/**
 * The Sim host's flush sink (§4.2, D17). `executeFlushEffects` owns the ORDER and the once-only
 * pointer bless; this sink owns only the I/O — it mirrors the immutable `__delta:*`/`__snap:*`
 * objects into the Sim store and blesses the single `__state` pointer (the store carries its
 * own visibility lag, the source of pointer-staleness suspicion), and publishes the frame to
 * the Sim bus. Behavioural twin of the real Coordinator's KV sink so the transcript stays
 * byte-identical. When no store is wired the object writes are inert (broadcast still fires).
 * The Sim store carries no residency (no TTL), so the fake's observable twin of the Coordinator's
 * `kind → residency` policy (D16) is the RECORDED `kind` of each immutable write — the teeth read
 * it to assert a snapshot put carries `"snap"`, which is what the Coordinator maps to the 7d TTL.
 */
class FakeFlushSink implements FlushSink {
  /** Every telemetry event the executor routed here (CACHE-23) — the teeth read this to assert a
   * skipped oversize snapshot / a non-retryable-write belt fired. The real Coordinator forwards to
   * the platform; the fake RECORDS (its observable twin). */
  readonly telemetryEvents: FlushTelemetryEvent[] = [];

  /** Every immutable write's `(key, kind)`, in order (§4.2, D16) — the fake's observable twin of the
   * Coordinator's `kind → residency` decision. The Sim store has no TTL, so `kind` is what a test
   * asserts: a snapshot put must carry `"snap"` (the kind the real Coordinator maps to the 7d TTL). */
  readonly immutableWrites: {
    readonly key: string;
    readonly kind: ImmutableKind;
  }[] = [];

  constructor(
    private readonly writer: Store | undefined,
    private readonly bus: FakeBus | undefined
  ) {}

  async putImmutable(
    key: string,
    bytes: string,
    kind: ImmutableKind
  ): Promise<void> {
    this.immutableWrites.push({ key, kind });
    await this.writer?.set(key, bytes);
  }

  async putPointer(key: string, bytes: string): Promise<void> {
    await this.writer?.set(key, bytes);
  }

  broadcast(frame: BusFrame): void {
    this.bus?.publish(frame);
  }

  telemetry(event: FlushTelemetryEvent): void {
    this.telemetryEvents.push(event);
  }
}

export interface FakeRegistryDeps {
  readonly store?: Store;
  readonly bus?: FakeBus;
  /** Snapshot-cadence knobs forwarded to the hosted machine (§4.2). Unset ⇒ defaults. */
  readonly snapshotEveryBatches?: number;
  readonly snapshotIntervalMs?: number;
  /** Snapshot SIZE ceiling forwarded to the hosted machine (§4.2, CACHE-23). Unset ⇒ 25 MiB. */
  readonly maxSnapshotBytes?: number;
  /** The mirror-key scope (E19) forwarded to the hosted machine via `configure`. Unset ⇒
   * `"default"` — like the DO's first-boot seed, it enters the machine only as a `configure`. */
  readonly regId?: string;
}

export class FakeRegistry implements Registry {
  readonly #clock: SimClock;
  readonly #sink: FakeFlushSink;
  readonly #machine: RegistryMachine;

  // Injectable round-trip latency on `check` (the DO/primary read). The result is
  // ALWAYS authoritative (primary-only reads never lag — §4.4); only its COMPLETION
  // is delayed, modelling §1's honest cold bound (a cold read cannot resolve unknown
  // for free). Pending checks resolve — against the CURRENT watermarks — when the
  // world advances past their due time.
  #checkLagMs = 0;
  #pendingChecks: {
    dueAt: number;
    tags: readonly string[];
    resolve: (knowledge: readonly TagKnowledge[]) => void;
  }[] = [];

  // Monotonic count of `check` round-trips (the DO/primary read) this registry has
  // served — the efficiency tooth for E1's one-round-trip budget (CACHE-31). Read by
  // tests via `world.registry.checkCount`; never gates semantics.
  #checkCount = 0;

  constructor(clock: SimClock, deps: FakeRegistryDeps = {}) {
    this.#clock = clock;
    this.#sink = new FakeFlushSink(deps.store, deps.bus);
    this.#machine = new RegistryMachine();

    const options: {
      snapshotEveryBatches?: number;
      snapshotIntervalMs?: number;
      maxSnapshotBytes?: number;
      regId?: string;
    } = {};
    if (deps.snapshotEveryBatches !== undefined) {
      options.snapshotEveryBatches = deps.snapshotEveryBatches;
    }
    if (deps.snapshotIntervalMs !== undefined) {
      options.snapshotIntervalMs = deps.snapshotIntervalMs;
    }
    if (deps.maxSnapshotBytes !== undefined) {
      options.maxSnapshotBytes = deps.maxSnapshotBytes;
    }
    if (deps.regId !== undefined) {
      options.regId = deps.regId;
    }
    this.#machine.configure(options);
  }

  get currentEpoch(): number {
    return this.#machine.currentEpoch;
  }

  /** Every flush-sink telemetry event recorded (§4.2, CACHE-23) — the teeth read this to observe
   * a skipped oversize snapshot or a non-retryable-write belt firing. The real Coordinator forwards
   * these to the platform; the fake records them (its observable twin). Never gates semantics. */
  get telemetryEvents(): readonly FlushTelemetryEvent[] {
    return this.#sink.telemetryEvents;
  }

  /** Every immutable mirror write's `(key, kind)` in order (§4.2, D16) — the fake's observable twin
   * of the real Coordinator's `kind → residency` decision. The Sim store has no TTL, so the teeth
   * assert the `kind` (a snapshot put carries `"snap"`, the kind the Coordinator maps to the 7d TTL);
   * the residency NUMBER itself is pinned against the real sink in the integration lane. */
  get immutableWrites(): readonly {
    readonly key: string;
    readonly kind: ImmutableKind;
  }[] {
    return this.#sink.immutableWrites;
  }

  /** The mirror-key scope (§4.2, E19) — the Registry-interface channel the reader reads to
   * scope its resync keyspace and to assert the pointer echo. Sourced from the hosted machine. */
  get regId(): string {
    return this.#machine.regId;
  }

  /** Authoritative watermark for a tag (test accessor). */
  watermark(tag: string): { softEpoch: number; hardEpoch: number } {
    return this.#machine.watermarkOf(tag);
  }

  /** Monotonic count of `check` round-trips served (CACHE-31 efficiency tooth). */
  get checkCount(): number {
    return this.#checkCount;
  }

  async check(tags: readonly string[]): Promise<readonly TagKnowledge[]> {
    this.#checkCount++;
    if (this.#checkLagMs <= 0) {
      return this.#machine.knowledgeOf(tags);
    }
    const dueAt = this.#clock.now() + this.#checkLagMs;
    // oxlint-disable-next-line promise/avoid-new -- a deferred promise whose resolver is stored and fired by pumpChecks when the simulated DO round-trip elapses
    const deferred = new Promise<readonly TagKnowledge[]>((resolve) => {
      this.#pendingChecks.push({ dueAt, resolve, tags });
    });
    return await deferred;
  }

  /** Inject a `check` round-trip latency in ms (the DO/primary read cost). */
  setCheckLag(ms: number): void {
    this.#checkLagMs = ms;
  }

  /** Resolve any `check` whose latency has elapsed (the world drives this on advance). */
  pumpChecks(now: number): void {
    const due = this.#pendingChecks.filter((p) => p.dueAt <= now);
    this.#pendingChecks = this.#pendingChecks.filter((p) => p.dueAt > now);
    for (const p of due) {
      p.resolve(this.#machine.knowledgeOf(p.tags));
    }
  }

  async expire(tags: readonly string[]): Promise<RegistryAck> {
    return await this.#applyPurge(tags, "soft");
  }

  async delete(tags: readonly string[]): Promise<RegistryAck> {
    return await this.#applyPurge(tags, "hard");
  }

  async registerRetention(retentionMs: number): Promise<void> {
    this.#machine.registerRetention(retentionMs);
  }

  /**
   * The host's state-neutral E5 heartbeat twin (§4.2). The fake has no alarms, so nothing DRIVES a
   * cadence here; this exposes the same one-put pointer refresh the real Coordinator issues on an
   * idle alarm, driven through the SAME `executeFlushEffects` + sink — a behavioural twin so a
   * harness self-test can pin the once-only, cursor-untouched contract without a Durable Object. It
   * refreshes `__state.flushedAt` only; the machine advances nothing (not on the Registry interface,
   * like the other host-only accessors — a heartbeat is alarm-driven, never an RPC).
   */
  async heartbeat(): Promise<void> {
    await executeFlushEffects(
      [this.#machine.heartbeat(this.#clock.now())],
      this.#sink,
      this.#machine.regId
    );
  }

  /** GC horizon input: max registered retention, monotone high-water (§4.5, E11). */
  get maxRegisteredRetentionMs(): number {
    return this.#machine.gcHorizonMs;
  }

  async #applyPurge(
    tags: readonly string[],
    kind: "soft" | "hard"
  ): Promise<RegistryAck> {
    const now = this.#clock.now();
    const { ack, effects } =
      kind === "soft"
        ? this.#machine.expire(tags, now)
        : this.#machine.delete(tags, now);
    // Window-0 host: honor each scheduled flush immediately (the deadline is `now`), driving the
    // machine's flush plan through the SAME executor the real Coordinator uses (thin-shell rule).
    for (const schedule of effects) {
      await executeFlushEffects(
        this.#machine.flush(schedule.deadlineMs),
        this.#sink,
        this.#machine.regId
      );
    }
    return ack;
  }
}
