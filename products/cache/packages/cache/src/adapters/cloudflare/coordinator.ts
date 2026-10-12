/* References: §4.2, §4.3. */

// PLATFORM: this adapter invokes Worker runtime APIs.

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

/** The bindings the Coordinator reads. `REGISTRY_KV` is the §4.2 replication mirror. */
interface CoordinatorEnv {
  /** The KV namespace the `__delta`/`__snap`/`__state` replication mirror is written to (§4.2). */
  readonly REGISTRY_KV: KVNamespace;
  /* References: E5. */

  readonly REGISTRY_HEARTBEAT_MS?: string;
  /* References: §4.2. */

  readonly REGISTRY_SNAPSHOT_EVERY_BATCHES?: string;
  readonly REGISTRY_SNAPSHOT_INTERVAL_MS?: string;
  /* References: §4.2. */

  readonly REGISTRY_MAX_SNAPSHOT_BYTES?: string;
  /* References: §4.2, E19-b. */

  readonly REGISTRY_ID?: string;
}

/* References: E19-b, E19. */

export interface RegIdDivergence {
  /** The regId journaled at first boot — the identity that WINS (mirror keys stay `__reg:<this>:*`). */
  readonly journaled: string;
  /** The regId the current Durable Object name derives — observed, priced (misses/checks), never served. */
  readonly name: string;
}

type JournalEntry =
  | { readonly op: "expire" | "delete"; readonly tags: readonly string[] }
  | { readonly op: "registerRetention"; readonly retentionMs: number }
  | { readonly op: "flush"; readonly nowMs?: number }
  | { readonly op: "configure"; readonly options: RegistryMachineOptions };

// DO SQLite keys (the machine's durable state) ────────────────────────────────
/** The append-only command journal — the single source of truth for machine state. */
const JOURNAL_KEY = "__journal";
/** The B5 cursor: the epoch through which a batch has been durably accepted into KV. */
const CURSOR_KEY = "kvFlushedEpoch";
/**
 * The seeded registry identity (E19-b) — the `regId` this Coordinator journaled at FIRST boot,
 * derived from its Durable Object name. Written EXACTLY ONCE (with the first `configure`) and never
 * rewritten: it is the durable anchor the divergence detector compares a later name-derivation
 * against (R1 — journal-wins). Its presence marks "identity established"; its absence marks a first
 * boot. Distinct from `machine.regId` (which a bare cadence-`configure` would default) so the
 * seeded id is unambiguous.
 */
const IDENTITY_KEY = "regId";

/* References: §4.2, E19. */

/** Delta-object expiry in seconds (§4.2).
 * @testExport
 */
export const DELTA_TTL_S = 48 * 60 * 60;
/** Snapshot-object expiry in seconds.
 * @testExport
 */
export const SNAP_TTL_S = 7 * 24 * 60 * 60;

/**
 * KV's per-key write-rate ceiling in ms (§8): at most one write to the same key per second; a
 * second write within this window is answered with a 429. A PLATFORM CONSTRAINT, not a tuning
 * knob — the flush window may never coalesce tighter than this, or a burst would double-write
 * the one hot `__state` key sub-second and 429 (D17).
 */
// PLATFORM: Cloudflare KV same-key write ceiling (1 write/s/key).
const KV_MIN_SAME_KEY_WRITE_INTERVAL_MS = 1000;

/**
 * B5 fixed-deadline flush window (§4.2: honest bound ≤ 1.1s). A purge arms an alarm at the next
 * window boundary; many purges within a window coalesce into one batch. FLOORED at the KV
 * same-key ceiling so the window is ≥ 1s BY CONSTRUCTION — even if the tuning term is lowered,
 * the platform floor wins, so two consecutive flush deadlines are always ≥ 1s apart (one
 * `__state` bless per window).
 */
const FLUSH_WINDOW_TUNING_MS = 1100;
/** Batching window after applying the minimum write interval.
 * @testExport
 */
export const FLUSH_WINDOW_MS = Math.max(
  FLUSH_WINDOW_TUNING_MS,
  KV_MIN_SAME_KEY_WRITE_INTERVAL_MS
);
/** Minimum interval used to compute the batching window.
 * @testExport
 */
export { KV_MIN_SAME_KEY_WRITE_INTERVAL_MS };

/** Performs persistence and broadcast effects against the configured Store (§4.2).
 * @testExport
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
    // delta batches 48h. Chosen by the effect `kind` the executor passes (D16) — NEVER by parsing
    // the key. E19 scoped every key to `__reg:<regId>:snap:<seq>` / `…:delta:<seq>`, so the old
    // `startsWith("__snap:")` parse never matched and silently gave every scoped snapshot the 48h
    // delta TTL; the executor already discriminates the kind, so it is passed through, not re-derived.
    const expirationTtl = kind === "snap" ? SNAP_TTL_S : DELTA_TTL_S;
    await this.store.set(key, bytes, { expirationTtl });
  }

  async putPointer(key: string, bytes: string): Promise<void> {
    // The one hot `__state` key — no TTL (the pointer never expires). A KV 429 here classifies
    // `throttled` inside cloudflareKV and propagates; the flush fails whole (D17).
    await this.store.set(key, bytes);
  }

  broadcast(frame: BusFrame): void {
    /* References: §4.2. */

    this.broadcaster(frame);
  }

  telemetry(event: FlushTelemetryEvent): void {
    // PLATFORM: Workers observability. A skipped oversize snapshot or a non-retryable-write belt
    // (§4.2, CACHE-23, E13) is a loud operational signal — forward it to the platform log
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
  // The name/journal regId divergence detected at THIS instance's boot (E19-b, R1), or null. Set
  // once by `#establishConfig` during the first `#load` and stable thereafter (a re-entrant `#load`
  // returns the cached machine before re-establishing). A DO-boot observability surface only.
  #regIdDivergence: RegIdDivergence | null = null;

  /**
   * The name/journal regId divergence detected at this instance's boot (E19-b, R1), or null when the
   * name still derives the journaled identity. A read-only boot diagnostic (surfaced to the
   * integration divergence scene via `runInDurableObject`); never consulted on a hot path.
   */
  get regIdDivergence(): RegIdDivergence | null {
    return this.#regIdDivergence;
  }

  /** Strong read (§4.4): live, un-memoized, primary-only — the DO IS the primary. */
  async check(tags: readonly string[]): Promise<readonly TagKnowledge[]> {
    // Serialized after any in-flight mutation ⇒ read-your-writes on the primary (E1).
    return await this.ctx.blockConcurrencyWhile(async () => {
      const machine = await this.#load();
      return machine.knowledgeOf(tags);
    });
  }

  /** Soft purge (§2.2, §4.1): max-on-write bump of `softEpoch`. */
  async expire(tags: readonly string[]): Promise<RegistryAck> {
    return await this.ctx.blockConcurrencyWhile(
      async () => await this.#mutate("expire", tags)
    );
  }

  /** Hard purge (§2.2, §4.1): max-on-write bump of `hardEpoch`. */
  async delete(tags: readonly string[]): Promise<RegistryAck> {
    return await this.ctx.blockConcurrencyWhile(
      async () => await this.#mutate("delete", tags)
    );
  }

  /** Register a retention lease; the GC horizon rises to the high-water (§4.5, E11). */
  async registerRetention(retentionMs: number): Promise<void> {
    await this.ctx.blockConcurrencyWhile(async () => {
      const machine = await this.#load();
      machine.registerRetention(retentionMs);
      this.#journal.push({ op: "registerRetention", retentionMs });
      await this.#persist({ [JOURNAL_KEY]: this.#journal });
    });
  }

  /* References: §4.2, E19. */

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
      this.ctx.acceptWebSocket(server);
      const machine = await this.#load();
      const head = await this.#flushedEpoch();
      establish(server, head, machine.regId);
    });
    return new Response(null, { status: 101, webSocket: client });
  }

  /* References: §4.2. */

  override webSocketMessage(
    _ws: WebSocket,
    _message: ArrayBuffer | string
  ): void {
    // No application-message action is defined for this handler.
  }

  /* References: §4.2. */

  override webSocketClose(
    _ws: WebSocket,
    _code: number,
    _reason: string,
    _wasClean: boolean
  ): void {
    // No additional close action is defined for this handler.
  }

  override webSocketError(ws: WebSocket, _error: unknown): void {
    try {
      ws.close(1011, "astilba/cache coordinator: socket error");
    } catch {
      // Already torn down.
    }
  }

  /**
   * The DO alarm handler, serving BOTH the B5 flush window and the E5 heartbeat cadence off the one
   * alarm slot (§4.2, D11). A PURE function of durable state, idempotent under at-least-once
   * delivery + redeploy: it rehydrates the machine from the journal and either FLUSHES the
   * coalescing window `(kvFlushedEpoch, epoch]` into one content-addressed batch (advancing the
   * durable cursor ONLY after KV accepts), or — on an EMPTY window — refreshes the `__state`
   * freshness via a state-neutral heartbeat. A redelivery before the cursor advances re-writes the
   * identical (content-addressed, write-once) bytes; a redelivery after finds an empty window and
   * heartbeats (or no-ops when the cadence is unset). (§4.3 step 3, MI8.)
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
        // AFTER KV accepted (§4.3 step 3). The flush marker is what a later replay uses to drain the
        // pending window to the right boundary and reproduce batchSeq; its recorded `nowMs` re-drives
        // the snapshot cadence's time clause so replay reproduces snapBatch exactly (a replay with
        // nowMs=0 could never fire that clause).
        this.#journal.push({ nowMs, op: "flush" });
        await this.#persist({
          [JOURNAL_KEY]: this.#journal,
          [CURSOR_KEY]: machine.currentEpoch,
        });
        // A flush already blessed `__state` with a fresh `flushedAt`, so this alarm does NOT also
        // heartbeat — a second sub-second put to the one hot key is exactly the D17 double-write real
        // KV answers with a 429. The idle path (below) refreshes the pointer; the flush path never does.
      } else {
        // Empty window — an idle heartbeat tick (or an idempotent flush redelivery). Refresh the
        // `__state` freshness through the machine's state-neutral heartbeat: one pointer put via the
        // SAME shared executor, NO cursor move, and NO journal append (a state-neutral command has
        // nothing to replay, so journaling it would grow storage unboundedly for no value — D10).
        await this.#heartbeat(machine, nowMs);
      }
      // Re-arm the E5 heartbeat cadence for the next idle interval (earliest-wins: a later purge's
      // B5 flush deadline can still move this alarm EARLIER). Whichever deadline just fired, the other
      // re-arms after it — one alarm slot, two deadlines, nearest wins (D11).
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
    // Durable acceptance (§4.3 step 1): the ack is not returned until the mutation is
    // journalled. On a redeploy before this persist, the machine rebuilds WITHOUT this
    // mutation — as if it never happened — so the ack is honest.
    this.#journal.push({ op, tags });
    await this.#persist({ [JOURNAL_KEY]: this.#journal });
    // Arm the B5 coalescing alarm for each scheduled flush (the fake host flushes immediately).
    for (const schedule of effects) {
      await this.#armFlushAlarm(schedule.deadlineMs);
    }
    return ack;
  }

  /**
   * The KV mirror as a `Store` (§8) — the flush write path goes THROUGH the `cloudflareKV`
   * driver (not raw `put`), so a rejected write is classified into the structural
   * `StoreWriteError` vocabulary (§8, E13) the kernel already speaks, and the sink stays a
   * dumb key/bytes writer. Built per flush off the LIVE `env.REGISTRY_KV` binding.
   */
  #kvStore(): Store {
    return cloudflareKV(this.env.REGISTRY_KV);
  }

  /* References: §4.2. */

  async #flushedEpoch(): Promise<number> {
    return (await this.ctx.storage.get<number>(CURSOR_KEY)) ?? 0;
  }

  /* References: §4.2. */

  #fanOut(frame: BusFrame): void {
    fanOut(this.ctx.getWebSockets(), frame);
  }

  /**
   * B5 fixed-deadline alarm: arm at the next window boundary, EARLIEST-WINS (§4.2, D11). Sets the
   * alarm to the NEAREST of any currently-armed deadline (possibly the E5 heartbeat cadence) and
   * this flush's window boundary. Two properties, one `min`: a pending heartbeat NEVER starves the
   * flush (the honest ≤1.1s bound holds — the defect the old `getAlarm() !== null ⇒ return` guard
   * caused once a far heartbeat deadline was armed), and an already-armed EARLIER flush deadline is
   * never pushed forward (`setAlarm` overrides, so a naive re-arm would slide the deadline out and
   * starve the flush — §8 platform note).
   */
  async #armFlushAlarm(nowMs: number): Promise<void> {
    const deadline =
      (Math.floor(nowMs / FLUSH_WINDOW_MS) + 1) * FLUSH_WINDOW_MS;
    const existing = await this.ctx.storage.getAlarm();
    await this.ctx.storage.setAlarm(earliestAlarm(existing, deadline));
  }

  /**
   * Issue the machine's state-neutral E5 heartbeat: refresh `__state.flushedAt` through the SAME
   * shared executor + sink a flush uses — the executor is the ONLY `__state` writer, so there is no
   * raw put (D17). A no-op when the cadence is UNSET (the default in every test lane bar the
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
   * Re-arm the E5 heartbeat cadence, EARLIEST-WINS (§4.2, D11). Sets the alarm to the NEAREST of the
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

  /* References: E5, §4.2. */

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
   * Derive this Coordinator's regId from its platform identity (E19-b, R3): identity = address. The
   * DO's OWN name (`ctx.id.name`, the string clients address it by) wins; `env.REGISTRY_ID` is the
   * DEMOTED fallback for an un-named object, then `"default"`. A NAME violating E19's grammar
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
    // never a serve-dead (the pointer echo E19 + the hello echo C3-a still fail closed on mismatch).
    return resolveRegId(this.ctx.id.name, this.env.REGISTRY_ID);
  }

  /* References: E19-b, E19. */

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

    /* References: E19-b. */

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

  /* References: §4.2, E19-b. */

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

  /* References: E19-b. */

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
    /* References: E19-b. */

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
