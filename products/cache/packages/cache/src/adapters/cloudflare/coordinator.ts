// @astilba/cache adapter — the Coordinator Durable Object (phase 2).
//
// PLATFORM: this file lives under `src/adapters/**`, the sanctioned platform
// boundary. It imports the workerd runtime base class from
// `cloudflare:workers` — a runtime-only module resolvable only inside the Workers
// isolate — so this subtree is carved out of the kernel's Node-side tsc program and
// typechecked under the integration lane's Workers-typed config. Ambient platform
// APIs (DO storage, alarms, KV) and real wall time are legal HERE, each carrying a
// `// PLATFORM:` note; the machine and kernel stay pure.
//
// THE THIN SHELL. This DO HOSTS the pure `RegistryMachine` (@astilba/cache/registry) —
// the SAME machine the harness `FakeRegistry` hosts and the sacred suite validates. It
// owns none of the registry semantics; it only:
//   1. persists the machine's command sequence to DO SQLite storage (event sourcing),
//   2. rehydrates the machine by replaying that journal on a cold start / redeploy,
//   3. runs the command and executes the returned effect DESCRIPTIONS against real
//      plumbing — persist-batch → a KV write, schedule-flush → a coalescing alarm,
// broadcast-frame → a stateless fan-out to the hibernatable doBus sockets (retired).
//
// WHY A JOURNAL, NOT A SNAPSHOT. The machine is pure and OPAQUE: its epoch counter,
// per-tag watermarks, batch sequence, and the un-flushed coalescing buffer are all
// private, and it exposes no load-from-snapshot constructor. The only machine-faithful
// way to reconstruct its EXACT state (incl. the pending window + batchSeq + the snapshot
// cadence's snapBatch the next flush depends on) without touching it is to replay the exact
// command sequence through a fresh instance. Because the machine is a deterministic function
// of its commands — with each flush re-driven at its RECORDED nowMs so the snapshot time
// clause resolves identically — replay reproduces byte-identical effects: the thin-shell
// identity.

import { DurableObject } from "cloudflare:workers";

import type { BusFrame, RegistryAck, Store } from "../../drivers";
import type { TagKnowledge } from "../../knowledge";
import {
  earliestAlarm,
  executeFlushEffects,
  RegistryMachine,
} from "../../registry";
import type {
  FlushSink,
  FlushTelemetryEvent,
  ImmutableKind,
  RegistryMachineOptions,
} from "../../registry";
import { TELEMETRY_EVENTS } from "../../telemetry";
import { establish, fanOut } from "./bus-server";
import { cloudflareKV } from "./kv";
import { resolveRegId } from "./reg-id";

/** The bindings the Coordinator reads. `REGISTRY_KV` is the replication mirror. */
interface CoordinatorEnv {
  /** The KV namespace the `__delta`/`__snap`/`__state` replication mirror is written to. */
  readonly REGISTRY_KV: KVNamespace;
  /**
   * liveness heartbeat interval in ms, as a config var (`env` decides WHEN the shell
   * issues a heartbeat command — it never parameterizes the machine). When set (Paid deployments),
   * an idle alarm refreshes `__state`'s freshness even with no purges, via the machine's
   * state-neutral heartbeat routed through the shared executor; earliest-wins arbitration keeps it
   * from starving the flush deadline, and a flush never also heartbeats in the same alarm.
   * UNSET by default (off — the default in every test lane; the heartbeat suite sets it per-test),
   * so the heartbeat path stays fully dormant.
   *
   * THE COORDINATOR END OF THE ONE HEARTBEAT NUMBER, and the end NO WORKER CAN REACH.
   * This var and the reader's `defaults.heartbeatInterval` are the two ends of the liveness
   * contract, but a Worker cannot write a Durable Object's `env`: deploy-time wrangler config is
   * this end's ONLY seed. So the two ends are NOT matched by construction — they are ONE DOCUMENTED
   * NUMBER over TWO DEPLOYMENT SURFACES, and matching them is a deployment step. Recommended
   * production posture: `"vars": { "REGISTRY_HEARTBEAT_MS": "30000" }` — the same 30s the
   * `createWorkersCache` reader defaults to (see `DEFAULT_HEARTBEAT_INTERVAL` in `workers-cache.ts`
   * for the full price of leaving it unset). A change here lands at the DO's next boot (one boot's
   * lag). A drift between the ends is safe-direction (a shorter reader interval raises suspicion
   * SPURIOUSLY: live registry checks, telemetry-visible, never a stale serve), which is why nothing
   * enforces equality; the asymmetry is documented at that constant rather than engineered away.
   */
  readonly REGISTRY_HEARTBEAT_MS?: string;
  /**
   * Snapshot cadence knobs, as config vars. `env` may only AUTHOR a journaled `configure`
   * command (Ruling B) — it never parameterizes the machine directly. On boot the shell diffs
   * `env` against the journal's effective config and, if changed, journals a fresh `configure`
   * (see `#reconcileConfig`), applied PROSPECTIVELY at a definite journal position — replay-exact
   * forever. So a later var change can never retroactively fork `snapBatch` or bless a `__snap`
   * that was never written; the journal is the single durable truth. UNSET ⇒ the machine's
   * 500-batch / 6h defaults.
   */
  readonly REGISTRY_SNAPSHOT_EVERY_BATCHES?: string;
  readonly REGISTRY_SNAPSHOT_INTERVAL_MS?: string;
  /**
   * Snapshot SIZE ceiling in bytes, as a config var. Seeds the machine's
   * `maxSnapshotBytes` through the SAME journaled `configure` channel as the cadence (Ruling B):
   * `env` authors it, the journal is the single durable truth, replay reproduces the skip decision.
   * A due snapshot whose canonical shipped bytes exceed this is SKIPPED (never truncated, never a
   * phantom bless). UNSET ⇒ the machine's 25 MiB default (the KV per-value profile).
   */
  readonly REGISTRY_MAX_SNAPSHOT_BYTES?: string;
  /**
   * The registry id scoping the mirror keyspace — the DEMOTED FALLBACK, as a config var.
   * Identity = address: a Coordinator's `regId` derives from its OWN Durable Object name
   * (`ctx.id.name`), the string clients address it by. `REGISTRY_ID` is consulted ONLY for an
   * un-named object (`newUniqueId` ⇒ `ctx.id.name === undefined`), never as an override (an override
   * would resurrect the one-registry-per-deployment defect kills). UNSET ⇒ `"default"`
   * (single-registry deployments stay zero-config). Validated `[a-z0-9._-]`, ≤ 64 — a malformed
   * value fails the boot loud (see `resolveRegId`) rather than minting a bad key.
   */
  readonly REGISTRY_ID?: string;
}

/**
 * A name/journal regId DIVERGENCE detected at boot. regId is journaled at first
 * `configure` and NEVER re-journaled; on a later boot where the DO's name derives a DIFFERENT id
 * than the journaled one, the Coordinator KEEPS the journaled identity and records this — it can
 * never serve dead, because both replication channels already fail closed on identity mismatch (the
 * pointer's regId echo, `hello`'s regId echo). Migration to a new identity =
 * addressing a NEW name (fresh journal, fresh keyspace), never a rekey of the live one.
 */
export interface RegIdDivergence {
  /** The regId journaled at first boot — the identity that WINS (mirror keys stay `__reg:<this>:*`). */
  readonly journaled: string;
  /** The regId the current Durable Object name derives — observed, priced (misses/checks), never served. */
  readonly name: string;
}

/**
 * A durably-journalled machine command. Replayed in order to rebuild the machine. A `flush`
 * carries the wall-clock `nowMs` it ran at so replay re-drives the snapshot cadence's time
 * clause EXACTLY (a replay with `nowMs = 0` could never fire it, silently forking `snapBatch`).
 * `nowMs` is optional for backward-compatible reads of pre-snapshot journals (default 0). A
 * `configure` records the effective snapshot cadence at its journal position (Ruling B): `env`
 * authors it, the journal is the single durable truth, and replay reproduces the cadence
 * prospectively — no `SNAP_CONFIG_KEY` side-table, no retroactive fork.
 */
type JournalEntry =
  | { readonly op: "expire" | "delete"; readonly tags: readonly string[] }
  | { readonly op: "registerRetention"; readonly retentionMs: number }
  | { readonly op: "flush"; readonly nowMs?: number }
  | { readonly op: "configure"; readonly options: RegistryMachineOptions };

// DO SQLite keys (the machine's durable state) ────────────────────────────────
/** The append-only command journal — the single source of truth for machine state. */
const JOURNAL_KEY = "__journal";
/** The cursor: the epoch through which a batch has been durably accepted into KV. */
const CURSOR_KEY = "kvFlushedEpoch";
/**
 * The seeded registry identity — the `regId` this Coordinator journaled at FIRST boot,
 * derived from its Durable Object name. Written EXACTLY ONCE (with the first `configure`) and never
 * rewritten: it is the durable anchor the divergence detector compares a later name-derivation
 * against (R1 — journal-wins). Its presence marks "identity established"; its absence marks a first
 * boot. Distinct from `machine.regId` (which a bare cadence-`configure` would default) so the
 * seeded id is unambiguous.
 */
const IDENTITY_KEY = "regId";

// KV mirror keys — written BYTE-IDENTICALLY to the fake host's mirror ───
// The one hot `__state` pointer key is addressed ONLY inside `executeFlushEffects` (the sole
// `__state` writer, for both the flush bless and the heartbeat refresh) — the shell holds no
// literal for it, so no code path outside the executor can put it. NO host holds an
// immutable-key literal either: residency is chosen by the effect `kind` the executor passes,
// never by parsing the registry-scoped key — see `CoordinatorFlushSink.putImmutable`.

/**
 * KV physical residency, in SECONDS — KV `expirationTtl`. Delta batches age out at
 * 48h; content-addressed snapshots at 7d; the `__state` pointer never expires (no TTL). TTL
 * is host plumbing, not machine state — invisible to the content-addressed batch/snapshot
 * bytes the transcript compares.
 *
 * @testExport exported solely so tests assert the single source of truth; not public API, no
 * production importer — the integration residency scene pins the delta TTL (48h) the sink SELECTS
 * (the decision, not KV's Miniflare-blind enforcement).
 */
export const DELTA_TTL_S = 48 * 60 * 60;
/**
 * @testExport exported solely so tests assert the single source of truth; not public API, no
 * production importer — the integration residency scene pins the snapshot TTL (7d) the sink SELECTS.
 */
export const SNAP_TTL_S = 7 * 24 * 60 * 60;

/**
 * KV's per-key write-rate ceiling in ms: at most one write to the same key per second; a
 * second write within this window is answered with a 429. A PLATFORM CONSTRAINT, not a tuning
 * knob — the flush window may never coalesce tighter than this, or a burst would double-write
 * the one hot `__state` key sub-second and 429.
 */
// PLATFORM: Cloudflare KV same-key write ceiling (1 write/s/key).
const KV_MIN_SAME_KEY_WRITE_INTERVAL_MS = 1000;

/**
 * B5 fixed-deadline flush window (honest bound ≤ 1.1s). A purge arms an alarm at the next
 * window boundary; many purges within a window coalesce into one batch. FLOORED at the KV
 * same-key ceiling so the window is ≥ 1s BY CONSTRUCTION — even if the tuning term is lowered,
 * the platform floor wins, so two consecutive flush deadlines are always ≥ 1s apart (one
 * `__state` bless per window).
 */
const FLUSH_WINDOW_TUNING_MS = 1100;
/**
 * @testExport exported solely so tests assert the single source of truth; not public API, no
 * production importer — a test pins the ≥1s floored flush window (the coalescing invariant, so a
 * lowered tuning term can never breach the platform floor).
 */
export const FLUSH_WINDOW_MS = Math.max(
  FLUSH_WINDOW_TUNING_MS,
  KV_MIN_SAME_KEY_WRITE_INTERVAL_MS
);
/**
 * @testExport exported solely so tests assert the single source of truth; not public API, no
 * production importer — a test pins the KV same-key 1-write/s ceiling the flush window floors to.
 */
export { KV_MIN_SAME_KEY_WRITE_INTERVAL_MS };

/**
 * The Coordinator's KV flush sink. `executeFlushEffects` owns the write ORDER and
 * the once-only `__state` bless; this sink owns only the KV I/O — routed through the
 * `cloudflareKV` Store driver, so a rejected write is CLASSIFIED into the structural
 * `StoreWriteError` vocabulary the kernel already speaks — no second classification.
 * A `__state` 429 ⇒ `throttled` PROPAGATES out of the flush: nothing was blessed, the immutable
 * objects are content-addressed/idempotent, and the alarm retries (no recovery logic needed).
 *
 * @testExport exported solely so tests assert the single source of truth; not public API, no
 * production importer — the integration residency scene constructs it directly to drive the KV
 * write-order + `__state`-bless behavior in isolation.
 */
export class CoordinatorFlushSink implements FlushSink {
  constructor(
    private readonly store: Store,
    private readonly broadcaster: (frame: BusFrame) => void
  ) {}

  async putImmutable(
    key: string,
    bytes: string,
    kind: ImmutableKind
  ): Promise<void> {
    // Residency is host plumbing, invisible to the content-addressed bytes: snapshots live 7d,
    // delta batches 48h. Chosen by the effect `kind` the executor passes — NEVER by parsing
    // the key. Registry scoping put every key under to `__reg:<regId>:snap:<seq>` / `…:delta:<seq>`, so the old
    // `startsWith("__snap:")` parse never matched and silently gave every scoped snapshot the 48h
    // delta TTL; the executor already discriminates the kind, so it is passed through, not re-derived.
    const expirationTtl = kind === "snap" ? SNAP_TTL_S : DELTA_TTL_S;
    await this.store.set(key, bytes, { expirationTtl });
  }

  async putPointer(key: string, bytes: string): Promise<void> {
    // The one hot `__state` key — no TTL (the pointer never expires). A KV 429 here classifies
    // `throttled` inside cloudflareKV and propagates; the flush fails whole.
    await this.store.set(key, bytes);
  }

  broadcast(frame: BusFrame): void {
    // Retired: fan the replication frame out to every hibernatable subscriber via the
    // Coordinator's stateless fan-out. A subscriber that misses it (a doubtful socket
    // closed) fails closed and resyncs off __state/__delta (the cold protocol) —
    // the frame is a LIVE-channel optimization over the durable mirror, never the sole path.
    this.broadcaster(frame);
  }

  telemetry(event: FlushTelemetryEvent): void {
    // PLATFORM: Workers observability. A skipped oversize snapshot or a non-retryable-write belt
    // is a loud operational signal — forward it to the platform log
    // (wrangler tail / Workers Logs). Never gates correctness; the fake host's twin RECORDS the
    // same event for the teeth (thin-shell: one executor routes both hosts identically).
    console.warn("astilba/cache coordinator flush telemetry", event);
  }
}

export class Coordinator extends DurableObject<CoordinatorEnv> {
  // The hosted pure machine. `undefined` until first touched or after a redeploy —
  // rebuilt lazily from the durable journal by `#load()` inside a serialized section.
  #machine: RegistryMachine | undefined;
  // In-memory mirror of the durable journal, so appends don't re-read storage.
  #journal: JournalEntry[] = [];
  // The name/journal regId divergence detected at THIS instance's boot, or null. Set
  // once by `#establishConfig` during the first `#load` and stable thereafter (a re-entrant `#load`
  // returns the cached machine before re-establishing). A DO-boot observability surface only.
  #regIdDivergence: RegIdDivergence | null = null;

  /**
   * The name/journal regId divergence detected at this instance's boot, or null when the
   * name still derives the journaled identity. A read-only boot diagnostic (surfaced to the
   * integration divergence scene via `runInDurableObject`); never consulted on a hot path.
   */
  get regIdDivergence(): RegIdDivergence | null {
    return this.#regIdDivergence;
  }

  /** Strong read: live, un-memoized, primary-only — the DO IS the primary. */
  async check(tags: readonly string[]): Promise<readonly TagKnowledge[]> {
    // Serialized after any in-flight mutation ⇒ read-your-writes on the primary.
    return await this.ctx.blockConcurrencyWhile(async () => {
      const machine = await this.#load();
      return machine.knowledgeOf(tags);
    });
  }

  /** Soft purge: max-on-write bump of `softEpoch`. */
  async expire(tags: readonly string[]): Promise<RegistryAck> {
    return await this.ctx.blockConcurrencyWhile(
      async () => await this.#mutate("expire", tags)
    );
  }

  /** Hard purge: max-on-write bump of `hardEpoch`. */
  async delete(tags: readonly string[]): Promise<RegistryAck> {
    return await this.ctx.blockConcurrencyWhile(
      async () => await this.#mutate("delete", tags)
    );
  }

  /** Register a retention lease; the GC horizon rises to the high-water. */
  async registerRetention(retentionMs: number): Promise<void> {
    await this.ctx.blockConcurrencyWhile(async () => {
      const machine = await this.#load();
      machine.registerRetention(retentionMs);
      this.#journal.push({ op: "registerRetention", retentionMs });
      await this.#persist({ [JOURNAL_KEY]: this.#journal });
    });
  }

  /**
   * The doBus live-channel endpoint. A subscriber opens a WebSocket here; the DO
   * accepts it HIBERNATABLY (no per-subscriber attachment, ZERO replay state) and runs
   * the establishment handshake: `reset` → `hello{head, regId}`, BEFORE any frame. `head`
   * is the DO's OWN last-flushed epoch (`#flushedEpoch`) and the `hello` echoes `regId`,
   *so a mismatched subscriber closes loud. Serialized under `blockConcurrencyWhile` so the
   * handshake snapshots a consistent (cursor, regId) against any in-flight mutation.
   */
  override async fetch(request: Request): Promise<Response> {
    if (request.headers.get("Upgrade") !== "websocket") {
      return new Response(
        "astilba/cache coordinator: expected a WebSocket upgrade",
        {
          status: 426,
        }
      );
    }
    const { 0: client, 1: server } = new WebSocketPair();
    await this.ctx.blockConcurrencyWhile(async () => {
      // Hibernatable accept: the runtime need not pin this DO to memory while the socket
      // is open. No attachment is serialized — the DO keeps no per-subscriber state.
      this.ctx.acceptWebSocket(server);
      const machine = await this.#load();
      const head = await this.#flushedEpoch();
      establish(server, head, machine.regId);
    });
    return new Response(null, { status: 101, webSocket: client });
  }

  /**
   * Hibernatable WebSocket message handler. The doBus client is RECEIVE-ONLY —
   * it dials, decodes, and dispatches; it never sends. Any inbound message is unexpected and there
   * is no per-subscriber state to update, so it is ignored. Defined so the hibernation runtime has a
   * message handler for woken sockets.
   */
  override webSocketMessage(
    _ws: WebSocket,
    _message: ArrayBuffer | string
  ): void {
    // Receive-only protocol — nothing to do (stateless fan-out).
  }

  /**
   * Hibernatable WebSocket close handler. Stateless fan-out holds ZERO
   * per-subscriber state, so a closed socket needs no reclamation — a later `getWebSockets()` simply
   * will not include it. Defined so the hibernation runtime has a close handler for woken sockets.
   */
  override webSocketClose(
    _ws: WebSocket,
    _code: number,
    _reason: string,
    _wasClean: boolean
  ): void {
    // No per-subscriber state to reclaim.
  }

  /** Hibernatable WebSocket error handler — best-effort close; no state to reclaim. */
  override webSocketError(ws: WebSocket, _error: unknown): void {
    try {
      ws.close(1011, "astilba/cache coordinator: socket error");
    } catch {
      // Already torn down.
    }
  }

  /**
   * The DO alarm handler, serving BOTH the flush window and the heartbeat cadence off the one
   * alarm slot. A PURE function of durable state, idempotent under at-least-once
   * delivery + redeploy: it rehydrates the machine from the journal and either FLUSHES the
   * coalescing window `(kvFlushedEpoch, epoch]` into one content-addressed batch (advancing the
   * durable cursor ONLY after KV accepts), or — on an EMPTY window — refreshes the `__state`
   * freshness via a state-neutral heartbeat. A redelivery before the cursor advances re-writes the
   * identical (content-addressed, write-once) bytes; a redelivery after finds an empty window and
   * heartbeats (or no-ops when the cadence is unset).
   */
  override async alarm(): Promise<void> {
    await this.ctx.blockConcurrencyWhile(async () => {
      const machine = await this.#load();
      // PLATFORM: wall clock feeds only the non-correctness-bearing `flushedAt` pointer
      // field (a staleness heuristic input); the epoch counter is the correctness clock.
      const nowMs = Date.now();
      const effects = machine.flush(nowMs);
      if (effects.length > 0) {
        // A real flush. Drive the plan through the SHARED executor (thin-shell rule): the immutable
        // __delta/__snap objects, then the ONE terminal __state bless — never two sub-second puts to
        // the hot key. A KV 429 on the pointer classifies `throttled` and propagates: the flush
        // fails whole, nothing was blessed, this alarm retries.
        await executeFlushEffects(
          effects,
          new CoordinatorFlushSink(this.#kvStore(), (frame) => {
            this.#fanOut(frame);
          }),
          machine.regId
        );
        // Cursor advance — journal the flush marker + record kvFlushedEpoch, ATOMICALLY and ONLY
        // AFTER KV accepted. The flush marker is what a later replay uses to drain the
        // pending window to the right boundary and reproduce batchSeq; its recorded `nowMs` re-drives
        // the snapshot cadence's time clause so replay reproduces snapBatch exactly (a replay with
        // nowMs=0 could never fire that clause).
        this.#journal.push({ nowMs, op: "flush" });
        await this.#persist({
          [JOURNAL_KEY]: this.#journal,
          [CURSOR_KEY]: machine.currentEpoch,
        });
        // A flush already blessed `__state` with a fresh `flushedAt`, so this alarm does NOT also
        // heartbeat — a second sub-second put to the one hot key is exactly the double-write real
        // KV answers with a 429. The idle path (below) refreshes the pointer; the flush path never does.
      } else {
        // Empty window — an idle heartbeat tick (or an idempotent flush redelivery). Refresh the
        // `__state` freshness through the machine's state-neutral heartbeat: one pointer put via the
        // SAME shared executor, NO cursor move, and NO journal append (a state-neutral command has
        // nothing to replay, so journaling it would grow storage unboundedly for no value — D10).
        await this.#heartbeat(machine, nowMs);
      }
      // Re-arm the heartbeat cadence for the next idle interval (earliest-wins: a later purge's
      // B5 flush deadline can still move this alarm EARLIER). Whichever deadline just fired, the other
      // re-arms after it — one alarm slot, two deadlines, nearest wins.
      await this.#armHeartbeatAlarm(nowMs);
    });
  }

  // ── internals ──────────────────────────────────────────────────────────────

  async #mutate(
    op: "expire" | "delete",
    tags: readonly string[]
  ): Promise<RegistryAck> {
    const machine = await this.#load();
    // PLATFORM: wall clock; passed to the pure machine as the non-correctness-bearing
    // `nowMs` parameter only (the machine never reads an ambient clock itself).
    const nowMs = Date.now();
    const { ack, effects } =
      op === "expire"
        ? machine.expire(tags, nowMs)
        : machine.delete(tags, nowMs);
    // Durable acceptance: the ack is not returned until the mutation is
    // journalled. On a redeploy before this persist, the machine rebuilds WITHOUT this
    // mutation — as if it never happened — so the ack is honest.
    this.#journal.push({ op, tags });
    await this.#persist({ [JOURNAL_KEY]: this.#journal });
    // Arm the coalescing alarm for each scheduled flush (the fake host flushes immediately).
    for (const schedule of effects) {
      await this.#armFlushAlarm(schedule.deadlineMs);
    }
    return ack;
  }

  /**
   * The KV mirror as a `Store` — the flush write path goes THROUGH the `cloudflareKV`
   * driver (not raw `put`), so a rejected write is classified into the structural
   * `StoreWriteError` vocabulary the kernel already speaks, and the sink stays a
   * dumb key/bytes writer. Built per flush off the LIVE `env.REGISTRY_KV` binding.
   */
  #kvStore(): Store {
    return cloudflareKV(this.env.REGISTRY_KV);
  }

  /**
   * The DO's OWN last-flushed epoch — the doBus `hello` head. It is the durable
   * `kvFlushedEpoch` cursor the last pointer bless advanced (the value that pointer carried), read
   * from journaled DO state. NEVER `machine.currentEpoch` (it over-declares into the un-flushed
   * `(kvFlushedEpoch, epoch]` window, stranding a caught-up subscriber suspect on a purge not yet in
   * KV), and NEVER a KV read-back (the mirror is stale-licensed; the DO is the truth source). Absent
   * (a cold DO before any flush) ⇒ 0.
   */
  async #flushedEpoch(): Promise<number> {
    return (await this.ctx.storage.get<number>(CURSOR_KEY)) ?? 0;
  }

  /**
   * Stateless fan-out of one replication frame to every hibernatable subscriber.
   * Reads the CURRENT sockets via `getWebSockets()` (post-wake-safe) and delivers the frame once
   * each; a doubtful subscriber is CLOSED, never blocking the rest. No replay buffer, no
   * per-subscriber state — `reset` → `hello` + the reader's KV resync is the catch-up. doBus emits
   * NO server `gap`.
   */
  #fanOut(frame: BusFrame): void {
    fanOut(this.ctx.getWebSockets(), frame);
  }

  /**
   * B5 fixed-deadline alarm: arm at the next window boundary, EARLIEST-WINS. Sets the
   * alarm to the NEAREST of any currently-armed deadline (possibly the heartbeat cadence) and
   * this flush's window boundary. Two properties, one `min`: a pending heartbeat NEVER starves the
   * flush (the honest ≤1.1s bound holds — the defect the old `getAlarm() !== null ⇒ return` guard
   * caused once a far heartbeat deadline was armed), and an already-armed EARLIER flush deadline is
   * never pushed forward (`setAlarm` overrides, so a naive re-arm would slide the deadline out and
   * starve the flush — platform note).
   */
  async #armFlushAlarm(nowMs: number): Promise<void> {
    const deadline =
      (Math.floor(nowMs / FLUSH_WINDOW_MS) + 1) * FLUSH_WINDOW_MS;
    const existing = await this.ctx.storage.getAlarm();
    await this.ctx.storage.setAlarm(earliestAlarm(existing, deadline));
  }

  /**
   * Issue the machine's state-neutral liveness heartbeat: refresh `__state.flushedAt` through the SAME
   * shared executor + sink a flush uses — the executor is the ONLY `__state` writer, so there is no
   * raw put. A no-op when the cadence is UNSET (the default in every test lane bar the
   * heartbeat suite), so an idle alarm there blesses nothing. The pointer carries the current
   * epoch/lastBatch/snapBatch UNCHANGED — nothing durable moves, so there is no journal append.
   */
  async #heartbeat(machine: RegistryMachine, nowMs: number): Promise<void> {
    if (this.#heartbeatIntervalMs() === null) {
      return;
    }
    await executeFlushEffects(
      [machine.heartbeat(nowMs)],
      // A heartbeat plan carries NO broadcast-frame effect, so this broadcaster is never invoked;
      // it is passed only to satisfy the shared sink shape.
      new CoordinatorFlushSink(this.#kvStore(), (frame) => {
        this.#fanOut(frame);
      }),
      machine.regId
    );
  }

  /**
   * Re-arm the heartbeat cadence, EARLIEST-WINS. Sets the alarm to the NEAREST of the
   * currently-armed deadline and `nowMs + interval`, so a pending B5 flush deadline is never pushed
   * out (unlike the old unconditional `setAlarm(now + interval)` last-writer-wins override). No-op
   * when the cadence is unset — the heartbeat path stays fully dormant there.
   */
  async #armHeartbeatAlarm(nowMs: number): Promise<void> {
    const intervalMs = this.#heartbeatIntervalMs();
    if (intervalMs === null) {
      return;
    }
    const existing = await this.ctx.storage.getAlarm();
    await this.ctx.storage.setAlarm(
      earliestAlarm(existing, nowMs + intervalMs)
    );
  }

  /**
   * The heartbeat interval in ms from the config var (Ruling B): `env` decides WHEN the
   * shell issues a heartbeat command, never a machine parameter. `null` when unset or malformed —
   * the whole heartbeat path stays dormant (the default in every test lane bar the heartbeat suite).
   */
  #heartbeatIntervalMs(): number | null {
    const configured = this.env.REGISTRY_HEARTBEAT_MS;
    if (configured === undefined) {
      return null;
    }
    const intervalMs = Number(configured);
    if (!Number.isFinite(intervalMs) || intervalMs <= 0) {
      return null;
    }
    return intervalMs;
  }

  /**
   * Derive this Coordinator's regId from its platform identity: identity = address. The
   * DO's OWN name (`ctx.id.name`, the string clients address it by) wins; `env.REGISTRY_ID` is the
   * DEMOTED fallback for an un-named object, then `"default"`. A NAME violating the grammar
   * REFUSES loud (R2 — a typed `InvalidRegistryNameError`). Called only from `#establishConfig`.
   */
  #deriveRegId(): string {
    // PLATFORM: `ctx.id.name` is a platform read — the DO's OWN address. It is `undefined` for an
    // object minted by `newUniqueId`, AND when THIS stub was reached via `idFromString` (which drops
    // the name even for an id originally minted by `idFromName`); both fall through to
    // `env.REGISTRY_ID`, then `"default"`. `resolveRegId` owns the precedence + the grammar refusal
    // (R2/R3); the platform reads happen HERE, at the sanctioned boundary.
    //
    // CONSEQUENCE under this design (why a dropped name is a fidelity note, not a hazard): a name
    // that reads `undefined` degrades to the fallback chain, journal-wins KEEPS the already-journaled
    // identity, and the worst case is a spurious `regid_divergence` telemetry line — NEVER a rekey,
    // never a serve-dead (the pointer echo hello echo still fail closed on mismatch).
    return resolveRegId(this.ctx.id.name, this.env.REGISTRY_ID);
  }

  /**
   * Establish identity + cadence at boot (R1 + Ruling B). Two concerns, one seam:
   *
   * IDENTITY (R1 — journal-wins, never re-journal). regId derives from the DO name (`#deriveRegId`,
   * which REFUSES an invalid name — R2). At FIRST boot (no `IDENTITY_KEY`) it is journaled ONCE,
   * folded into the first `configure` with the deployed cadence, and recorded durably. On a LATER
   * boot regId is NEVER re-derived-and-rejournaled: this is a pure DIVERGENCE DETECTOR — if the name
   * now derives a DIFFERENT id than the journaled one, emit `regid_divergence` telemetry and KEEP
   * the journaled identity. Divergence cannot serve dead (both replication channels fail closed on
   * identity mismatch — the pointer's regId echo hello's regId echo ); it costs
   * misses/checks/telemetry, never a stale serve. Migration to a new identity = addressing a NEW
   * name (fresh journal, fresh keyspace), never a rekey of the live one.
   *
   * A name-seeded DO can NEVER diverge from itself: name-first precedence makes the journaled id
   * EQUAL the name at first boot, and the name is the address, so a steady-state named object always
   * re-derives its own journaled id. A `regid_divergence` in the field therefore means one of exactly
   * two things — legacy env-seeded data (journaled before), or a fallback-seeded (`newUniqueId`)
   * object whose env/default seed later disagrees — never a healthy name-addressed registry.
   *
   * CADENCE (Ruling B, unchanged in spirit). `env` may only AUTHOR a journaled `configure`; it never
   * parameterizes the machine directly. A deployed cadence differing from the journal's effective
   * config journals a fresh `configure` — PROSPECTIVE, at a definite journal position, replay-exact.
   * Crucially it carries the JOURNALED regId forward, so a cadence change NEVER rekeys the mirror
   * (the orphaning defect the old regId-in-the-diff path would cause — R1). Idempotent: a boot whose
   * cadence already matches writes nothing.
   */
  async #establishConfig(machine: RegistryMachine): Promise<void> {
    const derived = this.#deriveRegId();
    const seeded = await this.ctx.storage.get<string>(IDENTITY_KEY);
    const cadence = this.#cadenceOptionsFromEnv();

    if (seeded === undefined) {
      // FIRST BOOT: journal the derived identity ONCE, folded with the deployed cadence into a
      // single `configure`, and record `IDENTITY_KEY` atomically so it is never re-derived-and-
      // rejournaled. `derived` already passed the charset gate (`resolveRegId`); `configure`
      // re-asserts it (defense in depth).
      const options: RegistryMachineOptions = { ...cadence, regId: derived };
      machine.configure(options);
      this.#journal.push({ op: "configure", options });
      await this.#persist({
        [IDENTITY_KEY]: derived,
        [JOURNAL_KEY]: this.#journal,
      });
      this.#regIdDivergence = null;
      return;
    }

    // LATER BOOT — regId is journal-wins. Replay already applied the first-boot `configure`, so
    // `machine.regId === seeded`. If the name now derives a different id, that is a DIVERGENCE: keep
    // the journaled identity, record + log it, and NEVER append a prospective `configure{regId}`.
    if (derived === seeded) {
      this.#regIdDivergence = null;
    } else {
      this.#regIdDivergence = { journaled: seeded, name: derived };
      // PLATFORM: Workers observability. A loud operational signal (wrangler tail / Workers Logs) —
      // never gates correctness (the fail-closed echoes do). Same channel as flush telemetry.
      console.warn("astilba/cache coordinator regid divergence", {
        event: TELEMETRY_EVENTS.regid_divergence,
        journaled: seeded,
        name: derived,
      });
    }

    // Cadence reconcile (Ruling B), carrying the JOURNALED regId (`seeded`) forward. The comparison
    // resolves `env` through a throwaway machine (the machine's own defaults are the single source
    // of truth) and compares ONLY the cadence knobs — regId is NOT a reconciled knob (identity = address lifted
    // it into the divergence detector above), so a name/journal divergence never triggers a rekey.
    const desired = new RegistryMachine();
    desired.configure(cadence);
    const current = machine.effectiveConfig;
    const want = desired.effectiveConfig;
    if (
      current.snapshotEveryBatches === want.snapshotEveryBatches &&
      current.snapshotIntervalMs === want.snapshotIntervalMs &&
      current.maxSnapshotBytes === want.maxSnapshotBytes
    ) {
      return;
    }
    const options: RegistryMachineOptions = { ...cadence, regId: seeded };
    machine.configure(options);
    this.#journal.push({ op: "configure", options });
    await this.#persist({ [JOURNAL_KEY]: this.#journal });
  }

  /**
   * Parse the snapshot CADENCE knobs from config vars — an empty object ⇒ the machine's
   * 500-batch / 6h / 25 MiB defaults. Only well-formed values are forwarded
   * (exactOptionalPropertyTypes: no `undefined` pass-through). The scoping `regId` is NOT parsed
   * here (it derives from the DO name via `#deriveRegId`, journal-wins) — this returns
   * cadence-only options that `#establishConfig` combines with the journaled regId to author a
   * `configure` (Ruling B). Never fed to the machine constructor on rehydration.
   */
  #cadenceOptionsFromEnv(): RegistryMachineOptions {
    const options: {
      snapshotEveryBatches?: number;
      snapshotIntervalMs?: number;
      maxSnapshotBytes?: number;
    } = {};
    const everyBatches = Number(this.env.REGISTRY_SNAPSHOT_EVERY_BATCHES);
    if (Number.isFinite(everyBatches) && everyBatches > 0) {
      options.snapshotEveryBatches = everyBatches;
    }
    const intervalMs = Number(this.env.REGISTRY_SNAPSHOT_INTERVAL_MS);
    if (Number.isFinite(intervalMs) && intervalMs > 0) {
      options.snapshotIntervalMs = intervalMs;
    }
    const maxSnapshotBytes = Number(this.env.REGISTRY_MAX_SNAPSHOT_BYTES);
    if (Number.isFinite(maxSnapshotBytes) && maxSnapshotBytes > 0) {
      options.maxSnapshotBytes = maxSnapshotBytes;
    }
    return options;
  }

  /**
   * Rebuild the hosted machine from durable state on first touch / after a redeploy. Replays the
   * journal through a FRESH machine (the journal is the SINGLE durable truth — cadence + identity
   * included, via `configure` commands), discarding the replayed effects (state reconstruction only
   * — no re-write to KV, no re-broadcast). Then establishes config (`#establishConfig`): seeds the
   * Identity once / detects its divergence, and reconciles the cadence into a fresh
   * `configure` if the deployed cadence changed (Ruling B). Serialized by the caller's
   * `blockConcurrencyWhile`, so two events never rehydrate concurrently.
   */
  async #load(): Promise<RegistryMachine> {
    if (this.#machine !== undefined) {
      return this.#machine;
    }
    const journal =
      (await this.ctx.storage.get<JournalEntry[]>(JOURNAL_KEY)) ?? [];
    const machine = new RegistryMachine();
    for (const entry of journal) {
      switch (entry.op) {
        case "expire": {
          machine.expire(entry.tags, 0);
          break;
        }
        case "delete": {
          machine.delete(entry.tags, 0);
          break;
        }
        case "registerRetention": {
          machine.registerRetention(entry.retentionMs);
          break;
        }
        case "configure": {
          // Cadence enters the machine ONLY as a journaled command (Ruling B), applied at its
          // journal position — prospective, so replay reproduces snapBatch exactly.
          machine.configure(entry.options);
          break;
        }
        case "flush": {
          // Re-drive the flush at its RECORDED nowMs so the snapshot cadence's time clause
          // resolves identically to the live run (replay-exact snapBatch). Effects are
          // discarded — state reconstruction only; `__delta`/`__snap` are already in KV.
          machine.flush(entry.nowMs ?? 0);
          break;
        }
        default: {
          const unreachable: never = entry;
          throw new Error(`unknown journal entry: ${String(unreachable)}`);
        }
      }
    }
    this.#journal = journal;
    this.#machine = machine;
    // Identity + cadence establishment happens AFTER replay + caching, so any `configure` sits at a
    // definite journal position and a re-entrant `#load` sees the cached machine. This is where the
    // Identity is seeded-once / its divergence detected (never re-journaled), and the cadence
    // is reconciled (Ruling B) preserving that journaled identity.
    await this.#establishConfig(machine);
    return machine;
  }

  /**
   * Persist durable state, aborting the instance on any storage failure so a partial
   * write can never leave the in-memory machine ahead of the journal: the next event
   * rehydrates from the last consistent durable state (crash-consistency).
   */
  async #persist(entries: Record<string, unknown>): Promise<void> {
    try {
      await this.ctx.storage.put(entries);
    } catch (error) {
      // The in-memory machine has advanced past what durably committed — discard it.
      this.ctx.abort("coordinator: durable persist failed");
      throw error;
    }
  }
}
