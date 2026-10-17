import { isStoreWriteError } from "@astilba/cache";
import type { StoreWriteError } from "@astilba/cache";
import {
  executeFlushEffects,
  mirrorKeys,
  RegistryMachine,
} from "@astilba/cache/registry";
import type {
  FlushSink,
  FlushTelemetryEvent,
  ImmutableKind,
} from "@astilba/cache/registry";
import { describe, expect, test } from "vitest";

// The default-scoped physical mirror keys both the machine and this executor address —
// a default machine mints `regId:"default"`, so the executor writes `__reg:default:…`.
const KEYS = mirrorKeys("default");

// Teeth for the shared flush executor — the thin-shell rule on the write path.
// shipped two production defects the two-host transcript could not observe because it
// compared only FINAL bytes, blind to write COUNTS: two hosts making the same redundant
// `__state` put are byte-identical (last-write-wins erases the evidence), yet on real KV the
// SECOND sub-second put to the one hot key 429s unconditionally (an intra-invocation livelock).
// The structural response: the machine emits ONE terminal `advance-pointer`, ordered last, and
// BOTH hosts drive it through `executeFlushEffects`. These tests pin the once-only + ordering
// property on the executor's OUTPUT — a place a future double-write becomes a red test, not an
// invisible 429. Mutate the executor to putPointer twice ⇒ the first test goes red.

/** A sink that logs every call so the ORDER and COUNT of writes are observable. Also captures each
 * pointer bless's BYTES (to read a blessed snapBatch back) and every telemetry event — both in
 * SEPARATE channels so the `calls` shape stays `{op, key}`. */
class RecordingSink implements FlushSink {
  readonly calls: { readonly op: string; readonly key: string }[] = [];
  readonly telemetryEvents: FlushTelemetryEvent[] = [];
  /** The bytes of each `putPointer` bless, in order — so a test can parse the blessed snapBatch. */
  readonly pointerWrites: string[] = [];
  /** Every immutable write's `(key, kind)` — the executor passes the effect kind so a host maps
   * residency by KIND, never by parsing the registry-scoped key. */
  readonly immutableWrites: {
    readonly key: string;
    readonly kind: ImmutableKind;
  }[] = [];

  async putImmutable(
    key: string,
    _bytes: string,
    kind: ImmutableKind
  ): Promise<void> {
    this.immutableWrites.push({ key, kind });
    this.calls.push({ key, op: "putImmutable" });
  }

  async putPointer(key: string, bytes: string): Promise<void> {
    this.pointerWrites.push(bytes);
    this.calls.push({ key, op: "putPointer" });
  }

  broadcast(): void {
    this.calls.push({ key: "", op: "broadcast" });
  }

  telemetry(event: FlushTelemetryEvent): void {
    this.telemetryEvents.push(event);
  }
}

/** A classified `throttled` rejection satisfying the structural contract. */
class ThrottledWriteError extends Error implements StoreWriteError {
  readonly code = "throttled";
  readonly retryable = true;

  constructor() {
    super("write throttled (KV 1 write/s/key, 429)");
    this.name = "ThrottledWriteError";
  }
}

/** A sink whose pointer bless always 429s — models the KV same-key ceiling on `__state`. */
class ThrottledPointerSink implements FlushSink {
  readonly immutableKeys: string[] = [];
  blessed = false;

  async putImmutable(
    key: string,
    _bytes: string,
    _kind: ImmutableKind
  ): Promise<void> {
    this.immutableKeys.push(key);
  }

  async putPointer(_key: string, _bytes: string): Promise<void> {
    throw new ThrottledWriteError();
  }

  broadcast(): void {
    // The frame path is not under test here; drop it (only the pointer bless is exercised).
  }

  telemetry(): void {
    // No telemetry expected on this path; a no-op keeps the sink a valid FlushSink.
  }
}

/** A classified `too_large` rejection — the KV 25 MiB ceiling, NON-retryable. */
class TooLargeWriteError extends Error implements StoreWriteError {
  readonly code = "too_large";
  readonly retryable = false;

  constructor() {
    super("cloudflareKV: value too large");
    this.name = "TooLargeWriteError";
  }
}

/** A sink whose IMMUTABLE put rejects with a classified error — models an oversize delta/snapshot
 * (non-retryable) or a transient store failure (retryable). Records telemetry for the belt teeth. */
class RejectingImmutableSink implements FlushSink {
  readonly telemetryEvents: FlushTelemetryEvent[] = [];
  pointerBlessed = false;

  // The rejection is BOTH an Error (thrown) and a structural StoreWriteError (classified) — the
  // two concrete errors below satisfy both, so `throw` stays an error-object throw.
  constructor(private readonly error: Error & StoreWriteError) {}

  async putImmutable(
    _key: string,
    _bytes: string,
    _kind: ImmutableKind
  ): Promise<void> {
    throw this.error;
  }

  async putPointer(_key: string, _bytes: string): Promise<void> {
    this.pointerBlessed = true;
  }

  broadcast(): void {
    // Unreachable — the immutable put rejects before the broadcast/pointer effects.
  }

  telemetry(event: FlushTelemetryEvent): void {
    this.telemetryEvents.push(event);
  }
}

/** A machine whose every flush also snapshots — so a flush emits TWO immutable objects. */
function crossingFlush(): RegistryMachine {
  const m = new RegistryMachine({ snapshotEveryBatches: 1 });
  m.expire(["a"], 0);
  return m;
}

/** A machine whose seq-1 flush is cadence-due but OVERSIZE (tiny ceiling) ⇒ the machine SKIPS the
 * snapshot: the plan carries the delta + pointer but NO __snap, plus a telemetry effect. */
function oversizeSkipFlush(): RegistryMachine {
  const m = new RegistryMachine({
    maxSnapshotBytes: 10,
    snapshotEveryBatches: 1,
  });
  m.expire(["a"], 0);
  return m;
}

describe("executeFlushEffects — one pointer bless per flush, ordered last (§4.2, D17)", () => {
  test("exactly ONE putPointer, ordered LAST, after every putImmutable", async () => {
    const m = crossingFlush();
    const sink = new RecordingSink();
    await executeFlushEffects(m.flush(0), sink, m.regId);

    const pointerCalls = sink.calls.filter((c) => c.op === "putPointer");
    // The load-bearing assertion the double-write mutation must break: exactly one bless.
    expect(pointerCalls).toHaveLength(1);

    const pointerIdx = sink.calls.findIndex((c) => c.op === "putPointer");
    expect(pointerIdx).toBe(sink.calls.length - 1); // it is the LAST call

    const immutableIdxs = sink.calls
      .map((c, i) => (c.op === "putImmutable" ? i : -1))
      .filter((i) => i >= 0);
    expect(immutableIdxs.length).toBeGreaterThan(0); // non-vacuous: there WERE immutable writes
    for (const i of immutableIdxs) {
      expect(i).toBeLessThan(pointerIdx); // every immutable object landed before the bless
    }
  });

  test("addresses the __delta/__snap/__state wire keys in the fixed plan order", async () => {
    const m = crossingFlush();
    const sink = new RecordingSink();
    await executeFlushEffects(m.flush(0), sink, m.regId);
    expect(sink.calls.map((c) => `${c.op}:${c.key}`)).toStrictEqual([
      `putImmutable:${KEYS.deltaKey(1)}`,
      `putImmutable:${KEYS.snapKey(1)}`,
      "broadcast:",
      `putPointer:${KEYS.stateKey}`,
    ]);
  });

  test("tags each immutable write with its effect KIND — delta vs snap — so a host maps residency by kind, not by parsing the E19-scoped key (§4.2, D16, E19)", async () => {
    // The crossing flush emits a delta AND a snapshot. The executor discriminates the effect
    // (`persist-batch` vs `persist-snapshot`) and PASSES that kind to the sink. With registry scoping, the physical
    // keys are `__reg:default:{delta,snap}:<seq>`, so a `startsWith("__snap:")` key-parse never
    // matches and residency MUST ride the kind. This is the executor-side tooth for the
    // threading: blind either arm (pass "delta" for a snapshot, or drop the kind so it arrives
    // `undefined`) and the snap entry below reds — the same defect that silently gave every scoped
    // snapshot the 48h delta TTL before this fix.
    const m = crossingFlush();
    const sink = new RecordingSink();
    await executeFlushEffects(m.flush(0), sink, m.regId);
    expect(sink.immutableWrites).toStrictEqual([
      { key: KEYS.deltaKey(1), kind: "delta" },
      { key: KEYS.snapKey(1), kind: "snap" },
    ]);
  });

  test("a heartbeat effect drives EXACTLY one putPointer and zero putImmutable (E5, §4.2, D17)", async () => {
    // A heartbeat plan is a single terminal `advance-pointer`; the executor maps it to one
    // `__state` bless with no immutable object. Doubling the heartbeat plan (two effects) would
    // show two putPointer here — the once-only property, on the executor's output.
    const m = new RegistryMachine();
    m.expire(["a"], 0);
    m.flush(0); // advance to a non-trivial machine state
    const sink = new RecordingSink();
    await executeFlushEffects([m.heartbeat(1234)], sink, m.regId);
    expect(sink.calls).toStrictEqual([
      { key: KEYS.stateKey, op: "putPointer" },
    ]);
  });

  test("a putPointer 429 PROPAGATES `throttled` — the flush fails whole, no swallow (§8, E13)", async () => {
    const m = new RegistryMachine();
    m.expire(["a"], 0);
    const sink = new ThrottledPointerSink();
    // Not swallowed: the classified rejection surfaces so the whole flush fails and the alarm
    // retries. Nothing was blessed; the immutable object below is idempotent on a re-drive.
    await expect(
      executeFlushEffects(m.flush(0), sink, m.regId)
    ).rejects.toSatisfy(isStoreWriteError);
    expect(sink.immutableKeys).toContain(KEYS.deltaKey(1)); // the delta landed (content-addressed, idempotent)
    expect(sink.blessed).toBeFalsy(); // …but no partial blessing is possible by construction
  });
});

describe("executeFlushEffects — the snapshot SIZE gate + the non-retryable-write belt (§4.2, CACHE-23, E13)", () => {
  test("an OVERSIZE-crossing flush executes [delta, pointer-once-last] with NO __snap put and snapBatch UNADVANCED (§4.2, CACHE-23, D17)", async () => {
    const m = oversizeSkipFlush();
    const sink = new RecordingSink();
    await executeFlushEffects(m.flush(0), sink, m.regId);

    // The executed plan writes ONLY the delta immutable — NEVER the __snap object (skipped).
    const immutablePuts = sink.calls
      .filter((c) => c.op === "putImmutable")
      .map((c) => c.key);
    expect(immutablePuts).toStrictEqual([KEYS.deltaKey(1)]); // delta only — snap absent

    // Exactly ONE pointer bless, ordered LAST (preserved through the skip path).
    const pointerCalls = sink.calls.filter((c) => c.op === "putPointer");
    expect(pointerCalls).toHaveLength(1);
    expect(sink.calls.at(-1)?.op).toBe("putPointer");

    // THE half-skip tooth: the blessed pointer bytes carry snapBatch:0 (NOT advanced). A mutation
    // suppressing the __snap put yet advancing #snapBatch would bless `"snapBatch":1` — referencing
    // a __snap that was never written. Asserting the exact executed bytes catches exactly that.
    expect(sink.pointerWrites).toStrictEqual([
      '{"epoch":1,"flushedAt":0,"lastBatch":1,"regId":"default","snapBatch":0}',
    ]);
  });

  test("the executor ROUTES the machine's snapshot-skipped-oversize effect to sink.telemetry (§4.2, CACHE-23)", async () => {
    const m = oversizeSkipFlush();
    const sink = new RecordingSink();
    await executeFlushEffects(m.flush(0), sink, m.regId);
    // The skip is a telemetry effect, NOT a put — it lands on the sink's observability channel,
    // carrying the machine's measured ceiling (and bytes).
    expect(sink.telemetryEvents).toHaveLength(1);
    expect(sink.telemetryEvents).toContainEqual(
      expect.objectContaining({
        ceiling: 10,
        event: "snapshot-skipped-oversize",
      })
    );
  });

  test("the belt: a NON-retryable immutable rejection emits flush_failed_nonretryable{code} then PROPAGATES — no bless (§8, E13, CACHE-23)", async () => {
    // An oversize delta (KV `too_large`, retryable:false) no retry can clear — the pathological
    // delta bound distinct from the machine's snapshot skip. The executor's belt makes it OBSERVABLE.
    const m = new RegistryMachine();
    m.expire(["a"], 0);
    const sink = new RejectingImmutableSink(new TooLargeWriteError());
    await expect(
      executeFlushEffects(m.flush(0), sink, m.regId)
    ).rejects.toSatisfy(isStoreWriteError); // still fails whole (propagates)
    // The reserved event fired, carrying the classified code — before propagation.
    expect(sink.telemetryEvents).toStrictEqual([
      { code: "too_large", event: "flush_failed_nonretryable" },
    ]);
    expect(sink.pointerBlessed).toBeFalsy(); // nothing blessed — no pointer references a missing object
  });

  test("the belt is SILENT on a RETRYABLE rejection — it propagates untouched for the alarm to retry (§8, E13)", async () => {
    // `throttled`/`unavailable` are retryable: the belt must NOT tag them nonretryable (a mutation
    // dropping the `!error.retryable` guard would wrongly emit telemetry here — this catches it).
    const m = new RegistryMachine();
    m.expire(["a"], 0);
    const sink = new RejectingImmutableSink(new ThrottledWriteError());
    await expect(
      executeFlushEffects(m.flush(0), sink, m.regId)
    ).rejects.toSatisfy(isStoreWriteError);
    expect(sink.telemetryEvents).toStrictEqual([]); // no nonretryable telemetry
    expect(sink.pointerBlessed).toBeFalsy();
  });
});
