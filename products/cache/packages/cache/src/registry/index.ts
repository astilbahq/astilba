// @astilba/cache/registry — the pure registry state machine (the Coordinator core).
// Internal subpath: both the harness FakeRegistry and the future Cloudflare Durable
// Object embed this SAME module (one machine, two hosts — the thin-shell rule). Not
// part of the published public surface (dev-only export; adapters ship relative).

export { earliestAlarm } from "./alarm";
export {
  executeFlushEffects,
  type FlushSink,
  type FlushTelemetryEvent,
  type ImmutableKind,
} from "./execute";
export {
  type AdvancePointerEffect,
  type BroadcastFrameEffect,
  type EffectiveConfig,
  type FlushEffect,
  type GcHorizon,
  type MutationTransition,
  type PersistBatchEffect,
  type PersistSnapshotEffect,
  type PurgeKind,
  RegistryMachine,
  type RegistryEffect,
  type RegistryMachineOptions,
  type ScheduleFlushEffect,
  type SnapshotSkippedEffect,
  type WatermarkView,
} from "./machine";
export {
  assertRegId,
  DEFAULT_REG_ID,
  isRegId,
  type MirrorKeys,
  mirrorKeys,
  type StatePointer,
} from "./mirror-keys";
