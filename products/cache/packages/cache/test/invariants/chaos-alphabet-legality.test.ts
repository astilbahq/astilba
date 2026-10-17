// INV-1 INV-25 — spec
// Sacred: append-only. Never modify, delete, weaken, skip, or .todo an existing test.
//
// This suite certifies VALUE-MEMBERSHIP legality under the chaos alphabet: `observed serve ⊆ legal`
// — nothing `∉ legal` is ever served — under every DELIVERY perturbation (a dropped frame/gap,
// buffered+reordered frames, a stalled `__state` pointer, an heartbeat that freshens the pointer
// WITHOUT minting an epoch, and strong reads as a first-class action). None is a truth-changing
// event: they perturb DELIVERY, not acceptance. So the model's `legal`/`tagKeys` sets — what the
// registry ACCEPTED under the SimClock-serialized issue order — are invariant under the whole
// alphabet: only an accepted `delete` clears `legal`, a soft `expire` never does, a fill commits the
// value it offered. Every perturbation can only make the reader MORE conservative, never serve a
// value the purge killed.
//
// DIMENSION BOUNDARY. A value-membership oracle certifies WHAT was served, never WHEN it was born.
// BIRTH-EPOCH and stability violations — a freshly-fetched value carrying a stale birth certificate,
// two strong reads of an unmutated key disagreeing — are a different dimension, owned by the
// dedicated metamorphic tests (`strong-read-stability`, `strong-fill-livecheck`,
// `strong-miss-soft-idempotence`), which assert read-equality. The strong reads HERE participate in
// value-membership (a dead value they serve is `∉ legal`), enrich reachable knowledge via
// memoization, and floor the strong reach — they do NOT witness the strong-miss born-dead bug (the strong-MISS born-dead
// serve), whose factory bytes the model just blessed into `legal`, so it is membership-invisible and
// owned there. The one carve-out is the deterministic `strong hit-path` probe below — a value-membership
// case (a strong HIT serving a now-dead CACHED value) the metamorphic MISS-path scenes never reach.
//
// SERVE-DEAD LIAR TAXONOMY (a scene selects its shape by whether the purge is durable-reachable or
// dropped-and-stalled — because reorder/delay perturb DELIVERY, never durable truth: the writer
// sequences `delta`/`__state` into the mirror regardless of what the bus did to the frames):
//   • CONVERGENCE-shaped — the purge is UNLEARNABLE this episode (frame dropped AND the durable path
//     stalled), so only the suspicion-clearing rule guards it. The convergence episode + the
// deterministic stalled-pointer scene below carry this (warm reader · dropped HARD frame · stalled
// `__state` · liveness heartbeat). RED-PROVEN by blinding `Invalidation.#tryConverge` (clear suspicion
//     on a DECLARED head without REACHING it) ⇒ the gap-hidden purge serves dead. `main` green.
//   • CLASSIFY-shaped — the purge is DELIVERED but disordered, hence LEARNABLE (resync replays it
//     from the durable mirror regardless of frame order). The reorder episode carries this (warm
//     reader · a reordered pair carrying a HARD delete out of order). RED-PROVEN by blinding the
//     hard-purge `classify` (the known-path defence). It is NOT convergence-shaped: under the
//     convergence blind the reorder scene STILL fails closed, because resync applies the delivered
//     purge — convergence-clearing is not the lone defence on delivered disorder.
//
// Fence sentinel — do NOT add a `FencedError` catch here. A caller-visible `FencedError` is
// unreachable in this sequential command lane (every path writing a tag's hard high-water advances
// the applied high-water in the same step, so the fence-taught retry re-mints `bornEpoch` from
// the co-advanced floor and self-heals within one attempt; exhaustion needs a 3× mid-factory
// applied-delete race only `fc.scheduler` produces, which `fc.commands` cannot). A fence surfacing
// under this chaos alphabet is therefore a REAL finding — let it propagate and adjudicate it.

import {
  commandsArbChaos,
  commandsArbConvergenceEpisode,
  commandsArbReorderEpisode,
  delayedStateReachedCases,
  gapReachedCases,
  heartbeatReachedCases,
  mkTag,
  modelSetup,
  reorderReachedCases,
  resetChaosReaches,
  strongReachedCases,
  world,
} from "@astilba/harness";
import { test } from "@fast-check/vitest";
import fc from "fast-check";
import { afterAll, beforeAll, describe, expect } from "vitest";

// Per-dimension reach floors. Each counts CASES (property runs) that
// actually FIRED the dimension's delivery perturbation — a frame genuinely dropped, a buffered
// pair genuinely swapped, a per-key delay genuinely held a newer pointer stale, a heartbeat
// landed in a gap-hidden-purge + stalled-pointer episode, a strong read landed on a hard-dead
// key. A dimension that never fired was not exercised, so its floor reds (the anti-inert-knob
// teeth). The counts are carried by the two FIXED-SKELETON episodes below (each fires its baked
// dimension ~once per run); the uniform `commandsArbChaos` alphabet contributes well below the
// floor to every dimension, so these floors guarantee the EPISODES keep firing their dimensions —
// they do NOT independently floor the uniform alphabet's per-dimension coverage. Each dimension's
// starvation LIAR is therefore a neuter of its EPISODE source (e.g. drop the episode's strong reads
// to zero the strong reach) — that drops the count below the floor and reds it.
//
// CALIBRATION (fast-check 4.8.0; 2026-07-13). Combined reach over the THREE properties below
// (chaos random + convergence episode + reorder episode), each `numRuns: 100` at the pinned seed
// 0x5eed, plus a 10-seed sweep {0x5eed,1,2,3,42,12345,99999,0xbeef,7,555}. Observed minimums
// across the sweep: gap 111, reorder 100, delayedState 100, heartbeat 100, strong 100. EPISODE/
// RANDOM split (pinned seed): each dimension is ~100 from its fixed episode (one fire per run) plus
// a small uniform-`commandsArbChaos` contribution — gap +26, reorder +2, delayedState +1, heartbeat
// +1, strong +2 — so the uniform alphabet ALONE never clears a floor; the floors track the EPISODES
// (above). Each floor (40) sits well below half of every minimum, so a stress `numRuns` bump or a
// seed change cannot flake it; only starving the dimension can. These are COVERAGE floors tied to the pinned run
// budget (numRuns 100 × three properties) BY DEFINITION — do NOT make them conditional on numRuns
// (a self-disabling gate). Restore the budget rather than lowering a floor (sacred =
// warrant-guarded). Re-verify on any fast-check bump (the ^4.8.0 caret, rule 7).
const GAP_FLOOR = 40;
const REORDER_FLOOR = 40;
const DELAYED_STATE_FLOOR = 40;
const HEARTBEAT_FLOOR = 40;
const STRONG_FLOOR = 40;

describe("chaos-alphabet legality (observed ⊆ legal under delivery perturbation)", () => {
  beforeAll(() => {
    resetChaosReaches();
  });

  test("INV-1/25: neither an eventual NOR a strong read serves a gap-hidden, heartbeat-freshened, stalled-pointer hard purge (CACHE-27 shape)", async () => {
    // The deterministic anchor of the episode property, catching BOTH serve-dead arms. A warm
    // reader misses a HARD purge whose frame is dropped, while its `__state` pointer is stalled and
    // an heartbeat rewrites that stale pointer with a higher `flushedAt` (freshness motion, no
    // epoch). Freshness is not catch-up:
    //   • the EVENTUAL read is the convergence-anchor witness — it consults the suspect-gated local
    //     map, so a kernel that cleared suspicion on the declared head WITHOUT reaching it (or on
    // freshness) would serve the dead value here;
    //   • the STRONG read confirms strong ALSO fails closed here, participating in the same
    // value-membership bound (a dead serve is `∉ legal`). It does NOT witness the strong-miss born-dead bug (the
    //     strong-MISS born-dead serve): the eventual read has already refilled the key, so there is
    // no dead value on the strong leg. That bug is owned by `strong-fill-livecheck` (miss path);
    // the strong-HIT live-check case is owned by the `strong hit-path` probe below.
    // On the correct kernel both reads fail closed and REFILL.
    const w = world({
      defaults: { unknownPolicy: "miss" },
      isolates: 2,
      replicas: 2,
    });
    const writer = w.isolate(0).cache;
    const reader = w.isolate(1).cache;
    const tag = mkTag("t0");

    // Warm epoch 1 (a delivered soft purge) and born the entry at epoch 1 (locally fresh); the
    // reader strong-primes so its local map records every tag the entry carries (soft 1, hard 0).
    await writer.expire({ tag });
    await w.advance(0);
    await writer.getOrSet({
      factory: async () => await Promise.resolve("warm"),
      key: "k",
      tags: [tag],
    });
    await w.advance(0);
    await reader.getOrSetEntry({
      consistency: "strong",
      factory: async () => await Promise.resolve("prime"),
      key: "k",
      tags: [tag],
    });

    // Stall the pointer, then freshen it with a heartbeat (a higher `flushedAt`, epoch unchanged).
    w.chaos.delayKey("__state", 10_000);
    await w.advance(1000);
    await w.registry.heartbeat();

    // Hide a HARD purge (epoch 2) behind a dropped frame (gap ⇒ suspect); its epoch-2 pointer bless
    // is stalled by the delay, so the reader keeps reading the epoch-1 (pre-purge) pointer.
    w.chaos.dropNextFrames(1);
    await w.advance(1000);
    await writer.delete({ tag });

    // Eventual read (past the heartbeat's now-visible freshness bump): suspect, no reached head ⇒
    // fail closed ⇒ 'miss' refills. Never serves the dead value.
    await w.advance(8000);
    const eventual = await reader.getOrSetEntry({
      factory: async () => await Promise.resolve("refill-e"),
      key: "k",
      tags: [tag],
    });
    // `toBe("refill-e")` IS the serve-dead assertion: a convergence bug serves the dead `warm`,
    // and `warm !== refill-e`, so this reds on the mutation.
    expect(eventual.value).toBe("refill-e");
    expect(eventual.stale).toBeFalsy();

    // Strong read: the live check observes the durable hard purge and refills authoritatively; a
    // kernel that skipped it would serve `warm` here, redding `toBe("refill-s")`.
    await w.advance(1000);
    const strong = await reader.getOrSetEntry({
      consistency: "strong",
      factory: async () => await Promise.resolve("refill-s"),
      key: "k",
      tags: [tag],
    });
    expect(strong.value).toBe("refill-s");
    expect(strong.stale).toBeFalsy();
  });

  test("INV-1: a strong HIT live-checks a delayed (in-flight, unsuspected) hard purge — E1 hit-path", async () => {
    // The SILENT-PROPAGATION window, and the one scene where strong differs from eventual on a HIT.
    // A hard purge is durably accepted at the registry, but its frame is DELAYED in flight (not
    // dropped) — so no gap fires and the reader never turns suspect. It is warm and legitimately
    // ignorant. A strong read HITS the cached entry; only the un-memoized live check (the `validate`
    // hit-path) observes the durable purge and refills. A kernel that skipped it classifies from
    // stale-but-unsuspect local knowledge and serves the dead value — value-illegal (`∉ legal`). This
    // is the HIT-path twin: the miss path is owned by `strong-fill-livecheck`; this scene is
    // the hit path's first tooth, and it is value-membership-visible (a dead CACHED value is served,
    // not a fresh fetch with a stale birth certificate).
    const w = world({
      defaults: { unknownPolicy: "miss" },
      isolates: 2,
      replicas: 2,
    });
    const writer = w.isolate(0).cache;
    const reader = w.isolate(1).cache;
    const tag = mkTag("t0");
    // Reader caches v0 and a strong prime records every tag the entry carries — CURRENT, soon-stale
    // knowledge, so the follow-up read is a HIT and not a miss.
    await writer.getOrSet({
      factory: async () => await Promise.resolve("v0"),
      key: "k",
      tags: [tag],
    });
    await w.advance(0);
    await reader.getOrSetEntry({
      consistency: "strong",
      factory: async () => await Promise.resolve("v0"),
      key: "k",
      tags: [tag],
    });
    // Delay frames: the coming purge is in flight, never delivered here — the reader stays warm and
    // unsuspect (a delay emits no gap, unlike a drop).
    w.chaos.delayFrames(100_000);
    await w.advance(1);
    // Durably hard-purge at the registry; its frame is delayed, not dropped.
    await writer.delete({ tag });
    await w.advance(1);
    // Strong HIT read: the live check must observe the durable purge and refill, never serve v0.
    const hit = await reader.getOrSetEntry({
      consistency: "strong",
      factory: async () => await Promise.resolve("v1"),
      key: "k",
      tags: [tag],
    });
    expect(hit.value).toBe("v1");
    expect(hit.value).not.toBe("v0");
  });

  test.prop([commandsArbChaos()])(
    "INV-1 property: observed serve ⊆ legal under the widened delivery alphabet",
    async (cmds) => {
      await fc.asyncModelRun(modelSetup({ isolates: 2, replicas: 2 }), cmds);
    }
  );

  test.prop([commandsArbConvergenceEpisode()])(
    "INV-25 property: a gap-hidden hard purge stays fail-closed across a stalled + heartbeat-freshened pointer",
    async (cmds) => {
      await fc.asyncModelRun(modelSetup({ isolates: 2, replicas: 2 }), cmds);
    }
  );

  test.prop([commandsArbReorderEpisode()])(
    "INV-1 property: out-of-order (reordered) soft frames never make a value serve illegally",
    async (cmds) => {
      await fc.asyncModelRun(modelSetup({ isolates: 2, replicas: 2 }), cmds);
    }
  );

  afterAll(() => {
    // The alphabet is vacuous for any dimension it never actually fires; each floor makes that a
    // red test rather than a silent pass. (The reach counters accumulate over BOTH properties.)
    expect(gapReachedCases()).toBeGreaterThanOrEqual(GAP_FLOOR);
    expect(reorderReachedCases()).toBeGreaterThanOrEqual(REORDER_FLOOR);
    expect(delayedStateReachedCases()).toBeGreaterThanOrEqual(
      DELAYED_STATE_FLOOR
    );
    expect(heartbeatReachedCases()).toBeGreaterThanOrEqual(HEARTBEAT_FLOOR);
    expect(strongReachedCases()).toBeGreaterThanOrEqual(STRONG_FLOOR);
  });
});
