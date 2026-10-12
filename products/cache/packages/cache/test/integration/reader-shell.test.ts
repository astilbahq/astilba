// The C2-b-2 reader-shell payoff (§4.2, E20/E21) — the replication READER driven against the REAL
// Coordinator Durable Object + the REAL cloudflareKV mirror on workerd. The DO writes the
// `__delta`/`__snap`/`__state` mirror (the WRITER side, C1/C2-b); a fresh `Invalidation` reader
// resyncs off it, and the tick-driven `ReplicationPoller` (C2-b-2-b) drives the retry ladder
// out-of-band. doBus is C3, so a harness `FakeBus` supplies the live channel: its subscribe-time
// `hello{head}` declares the DO's reached epoch (E21 — convergence anchors to a live-channel head,
// never to pointer freshness).
//
// Two flows: (1) cold-boot resync off the KV mirror ALONE; (2) a delta hole BELOW a blessed
// snapshot routes the ladder through the snapshot bridge, not the hole.
//
// Lane law (rule 12): the DO flush is fired deterministically (`runDurableObjectAlarm`) and the
// poller ticks on explicit `nowMs`, so these assertions carry no timing tolerance despite the lane.

import { fireAlarm } from "./registry-harness";
import { env } from "cloudflare:workers";

import type { Envelope } from "@astilba/cache";
import type { MirrorKeys } from "@astilba/cache/registry";
import { mirrorKeys } from "@astilba/cache/registry";
import { FakeBus, SimClock } from "@astilba/harness";
import { describe, expect, it } from "vitest";

import { doRegistry } from "../../src/adapters/cloudflare/do-registry";
import { cloudflareKV } from "../../src/adapters/cloudflare/kv";
import { Invalidation } from "../../src/invalidation";
import { ReplicationPoller } from "../../src/replication-poller";

const A = "reader:a";
const B = "reader:b";
const X = "reader:x";

// Per-test isolation (CACHE-29): a UNIQUE regId per test partitions the shared KV namespace into
// `__reg:<id>:…` (E19). Under E19-b the regId IS the DO name (`idFromName(regId)`, identity =
// address), so `scope` writes NO shared global — the DO derives the SAME id from its name, and the
// reader is constructed with the SAME id so it resyncs the exact scoped keyspace the DO writes.
function scope(regId: string): MirrorKeys {
  return mirrorKeys(regId);
}

/** Must match `REGISTRY_SNAPSHOT_EVERY_BATCHES` in this lane's wrangler.jsonc (the DO reads it). */
const EVERY_BATCHES = 5;

/** A deterministic rng — with `jitterFraction: 0` the poller ignores it, so cadences are exact. */
const rng = { next: (): number => 0.5 };

/** A minimal eventual-read probe envelope over `tags` born at `bornEpoch` (§3): only `tags` +
 * `bornEpoch` steer the §4.1 validity table, the rest are inert placeholders. */
function envelope(tags: readonly string[], bornEpoch: number): Envelope {
  return {
    bornEpoch,
    bornMs: 0,
    codecId: "probe/1",
    enc: "json",
    grace: 0,
    key: "integration:reader:probe",
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

/** The blessed `__state` pointer's `epoch` — the DO's reached durable epoch (the E21 head). */
async function pointerEpoch(stateKey: string): Promise<number> {
  const raw = await env.REGISTRY_KV.get(stateKey);
  const parsed = JSON.parse(raw ?? "null") as Record<string, unknown>;
  return typeof parsed.epoch === "number" ? parsed.epoch : 0;
}

/** Advance the reader off the poller's out-of-band ticks until it reconverges (or a generous cap —
 * a persistent divergence would leave it suspect and red the caller's assertion). */
async function driveUntilConverged(
  poller: ReplicationPoller,
  reader: Invalidation
): Promise<void> {
  for (let t = 0; t <= 600_000 && reader.suspect; t += 60_000) {
    await poller.onPollTick(t);
  }
}

describe("replication reader shell on workerd (§4.2, E20/E21)", () => {
  it("cold-boot: a fresh reader resyncs off the KV mirror ALONE and converges on the DO's head (E21)", async () => {
    const RID = "reader-coldboot";
    const KEYS = scope(RID);
    const stub = env.COORDINATOR.get(env.COORDINATOR.idFromName(RID));
    const kv = env.REGISTRY_KV;

    // Drive the DO to a known durable state: A hard-deleted (seq 1), B soft-expired (seq 2). Each
    // single-tag mutation is one flush ⇒ one `__delta` batch, so epoch === seq.
    await stub.delete([A]);
    await fireAlarm(stub);
    await stub.expire([B]);
    await fireAlarm(stub);

    const head = await pointerEpoch(KEYS.stateKey);
    expect(head).toBe(2);

    // A fresh reader with NO prior state. Feed a `hello{head}` at the DO's reached epoch over the
    // FakeBus (E21): advance the channel head BEFORE subscribe, so the subscribe-time hello declares
    // it. No frame is ever delivered — the reader learns the watermarks off KV alone.
    const bus = new FakeBus(new SimClock(0));
    bus.publish({ changes: [], fromEpoch: 0, toEpoch: head });
    const store = cloudflareKV(kv);
    const reader = new Invalidation(doRegistry(stub, RID), store, bus);
    // Put the reader IN USE (E28): construction captures the bus and defers the subscription to the
    // first read, so a module-scope cache can be built where the platform forbids I/O. This case
    // drives the reader directly, so it opens the channel explicitly — what a first read would do.
    reader.ensureSubscribed();
    expect(reader.suspect).toBe(true); // reset + hello{head} suspended warm trust

    const poller = new ReplicationPoller(reader, store, KEYS, rng, {
      jitterFraction: 0,
    });
    await driveUntilConverged(poller, reader);

    // Converged off the KV mirror alone, having reached the declared head.
    expect(reader.suspect).toBe(false);
    expect(reader.appliedEpoch).toBe(head);

    // Served knowledge is correct — both watermarks were learned from the KV `__delta` batches.
    expect(
      (await reader.validate(envelope([A], 0), "eventual", "miss")).validity
    ).toBe("dead"); // A hard-deleted at epoch 1
    expect(
      (await reader.validate(envelope([B], 0), "eventual", "miss")).validity
    ).toBe("stale"); // B soft-expired at epoch 2
  });

  it("forced hole: a delta BELOW a blessed snapshot routes the ladder via the snapshot, not the hole (§2)", async () => {
    const RID = "reader-forcedhole";
    const KEYS = scope(RID);
    const stub = env.COORDINATOR.get(env.COORDINATOR.idFromName(RID));
    const kv = env.REGISTRY_KV;

    // Six single-tag flushes. X is HARD-purged at seq 3 (BELOW the snapshot floor); the cadence
    // fires at seq 5 (`__snap:5`, a from-0 checkpoint that INCLUDES X's purge); seq 6 is a tail
    // delta ABOVE the floor. So only the snapshot — never delta 3 — can teach the reader about X.
    const script: readonly (readonly [op: "expire" | "delete", tag: string])[] = [
      ["expire", A], // seq 1
      ["expire", B], // seq 2
      ["delete", X], // seq 3 — the distinctive hard purge below the floor
      ["expire", A], // seq 4
      ["expire", B], // seq 5 — crosses the cadence ⇒ __snap:5 blessed
      ["expire", A], // seq 6 — tail delta above the floor
    ];
    for (const [op, tag] of script) {
      if (op === "expire") {
        await stub.expire([tag]);
      } else {
        await stub.delete([tag]);
      }
      await fireAlarm(stub);
    }

    const head = await pointerEpoch(KEYS.stateKey);
    expect(head).toBe(script.length); // 6
    expect(await kv.get(KEYS.snapKey(EVERY_BATCHES))).not.toBeNull(); // __snap:5 blessed

    // Synthesize the hole deterministically: delete the `__delta:3` batch below the snapshot floor.
    await kv.delete(KEYS.deltaKey(3));
    expect(await kv.get(KEYS.deltaKey(3))).toBeNull();

    const bus = new FakeBus(new SimClock(0));
    bus.publish({ changes: [], fromEpoch: 0, toEpoch: head });
    const store = cloudflareKV(kv);
    const reader = new Invalidation(doRegistry(stub, RID), store, bus);
    // Put the reader IN USE (E28): construction captures the bus and defers the subscription to the
    // first read, so a module-scope cache can be built where the platform forbids I/O. This case
    // drives the reader directly, so it opens the channel explicitly — what a first read would do.
    reader.ensureSubscribed();
    expect(reader.suspect).toBe(true);

    const poller = new ReplicationPoller(reader, store, KEYS, rng, {
      jitterFraction: 0,
    });
    await driveUntilConverged(poller, reader);

    // The ladder escalated resync → needs-snapshot → resyncViaSnapshot and reached the head THROUGH
    // the snapshot (which bridged the below-floor hole), not through the missing delta.
    expect(reader.suspect).toBe(false);
    expect(reader.appliedEpoch).toBe(head); // reached 6 despite the hole at 3

    // The proof it routed via the snapshot: X's hard purge (carried ONLY by delta 3, now deleted,
    // and by the from-0 snapshot) is known — so a value born before it reads DEAD. Had the reader
    // stalled on the hole, X would be unknown and this would be `unknown`, not `dead`.
    expect(
      (await reader.validate(envelope([X], 2), "eventual", "miss")).validity
    ).toBe("dead");
  });
});
