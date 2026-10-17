// The shared flush-effect executor — the thin-shell rule applied to the WRITE path.
//
// Both hosts of the one pure registry machine — the harness FakeRegistry (Sim store/bus) and
// the real Coordinator Durable Object (KV) — drive a flush's effect plan through THIS one
// function, so the write ORDER and the once-only `__state` bless exist in exactly ONE place.
// The machine already emits `advance-pointer` exactly once, ordered last; the executor just
// maps each effect onto a host-specific sink. It serializes the frames/pointer ITSELF, so the
// content-addressed bytes are identical across hosts by construction.
//
// PURE: no ambient time/randomness (the grep-guard scans src/** outside adapters, and this
// path is in scope). The sinks do the I/O; the executor owns only sequencing.

import type { BusFrame, ImmutableKind, StoreWriteError } from "../drivers";
import { isStoreWriteError } from "../drivers";
import type { FlushEffect } from "./machine";
import { mirrorKeys } from "./mirror-keys";

/**
 * A telemetry event routed through the flush sink — observability, never a
 * persisted object. Two shapes, one channel:
 *   - `snapshot-skipped-oversize` — the MACHINE skipped a due snapshot whose canonical shipped
 *     bytes exceed `maxSnapshotBytes` (a `SnapshotSkippedEffect` the executor forwards here).
 *   - `flush_failed_nonretryable` — the EXECUTOR's belt: a NON-retryable classified write rejection
 *     on an immutable object (an oversize delta/snapshot — the KV `too_large` bound no retry clears).
 */
export type FlushTelemetryEvent =
  | {
      readonly event: "snapshot-skipped-oversize";
      readonly bytes: number;
      readonly ceiling: number;
    }
  | {
      readonly event: "flush_failed_nonretryable";
      readonly code: StoreWriteError["code"];
    };

// `ImmutableKind` — the write-side mirror-object taxonomy — now lives in `../drivers` (the single
// source shared with the read-side `ReadKind`); re-exported here so `@astilba/cache/registry`
// consumers keep importing it from the registry surface. The executor (above import) passes it to
// `putImmutable` so a host maps kind → residency, never by parsing the (coped) key.
export type { ImmutableKind } from "../drivers";

/**
 * A host's write plumbing for one flush. The executor owns the ORDER and the once-only pointer
 * write; the sink owns only the I/O:
 *   - `putImmutable` writes a content-addressed, write-once object (`__delta:<seq>` /
 *     `__snap:<seq>`) — a host attaches its own residency (the DO's KV TTL) by the `kind` the
 * executor passes, NEVER by parsing the key ( scoped every key to `__reg:<regId>:…`, so a
 * key-prefix parse silently mis-routed every scoped snapshot to the delta TTL).
 *   - `putPointer` blesses the ONE hot `__state` key (≤1 write/1.1s). Called EXACTLY ONCE per
 *     flush, last — if it rejects (KV 429 ⇒ `throttled`), the rejection propagates and the whole
 *     flush fails; nothing was blessed, the immutable objects are idempotent, the alarm retries.
 *   - `broadcast` publishes the replication frame (the fake to its Sim bus; the DO drops it
 * until the hibernatable-socket bus exists).
 *   - `telemetry` receives an observability event (a skipped oversize snapshot, or the executor's
 *     non-retryable-write belt). Identical across hosts (thin-shell): the fake records it for the
 *     teeth, the real Coordinator forwards it to the platform. Never gates correctness.
 */
export interface FlushSink {
  putImmutable(key: string, bytes: string, kind: ImmutableKind): Promise<void>;
  putPointer(key: string, bytes: string): Promise<void>;
  broadcast(frame: BusFrame): void | Promise<void>;
  telemetry(event: FlushTelemetryEvent): void | Promise<void>;
}

/**
 * Write an immutable object THROUGH the non-retryable-write belt. The
 * executor-side backstop for the delta/snapshot SIZE bound, distinct from the machine's snapshot
 * skip: a NON-retryable classified rejection — an oversize immutable (KV `too_large`, 25 MiB, the
 * code `cloudflareKV` raises), the pathological bound no retry can clear — emits a
 * `flush_failed_nonretryable{code}` telemetry event so the failure is OBSERVABLE, then PROPAGATES
 * (the flush fails whole; the terminal `__state` bless never runs, so no pointer references an
 * object that was never written). Retryable rejections (`throttled`/`unavailable`) propagate
 * UNTOUCHED — the alarm retries them, exactly as before.
 */
async function putImmutableWithBelt(
  sink: FlushSink,
  key: string,
  bytes: string,
  kind: ImmutableKind
): Promise<void> {
  try {
    await sink.putImmutable(key, bytes, kind);
  } catch (error) {
    if (isStoreWriteError(error) && !error.retryable) {
      await sink.telemetry({
        code: error.code,
        event: "flush_failed_nonretryable",
      });
    }
    throw error;
  }
}

/**
 * Execute a flush's effect plan against a host sink. The plan already carries
 * the once-only terminal `advance-pointer` last, so this never issues two pointer puts in one
 * flush. It does NOT catch a sink rejection: a failed `putPointer` fails the flush whole.
 *
 * `regId` scopes every key through `mirrorKeys(regId)` — the SAME formatter the reader
 * addresses with — so the writer and reader can never re-drift to different bytes. The
 * host passes its machine's `regId`; that same id is echoed in the pointer payload the machine
 * minted, so the physical key and the payload agree by construction.
 */
export async function executeFlushEffects(
  effects: readonly FlushEffect[],
  sink: FlushSink,
  regId: string
): Promise<void> {
  const keys = mirrorKeys(regId);
  for (const effect of effects) {
    switch (effect.kind) {
      case "persist-batch": {
        await putImmutableWithBelt(
          sink,
          keys.deltaKey(effect.seq),
          JSON.stringify(effect.frame),
          "delta"
        );
        break;
      }
      case "persist-snapshot": {
        await putImmutableWithBelt(
          sink,
          keys.snapKey(effect.seq),
          JSON.stringify(effect.frame),
          "snap"
        );
        break;
      }
      case "broadcast-frame": {
        await sink.broadcast(effect.frame);
        break;
      }
      case "telemetry": {
        // The machine's `snapshot-skipped-oversize` decision — NOT a persisted
        // object. Forward its measured bytes/ceiling to the sink's observability channel.
        await sink.telemetry({
          bytes: effect.bytes,
          ceiling: effect.ceiling,
          event: effect.event,
        });
        break;
      }
      case "advance-pointer": {
        await sink.putPointer(keys.stateKey, JSON.stringify(effect.pointer));
        break;
      }
      default: {
        const unreachable: never = effect;
        throw new Error(`unknown flush effect: ${String(unreachable)}`);
      }
    }
  }
}
