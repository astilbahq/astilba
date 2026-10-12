// Writer→store→reader convergence under a NON-DEFAULT regId (E19/E21) — the round-trip the
// prior C2-b-1d attempt severed and could not see. Deterministic conformance lane (rule 12): the
// world advances on a SimClock, no timing tolerance.
//
// The dimension no other oracle here compares: the WRITER (a `world`-shared FakeRegistry scoped to
// `regId:"tenant-x"`) blesses the §4.2 mirror under `__reg:tenant-x:…`, and the READER (that
// world's reader cache, constructed with the SAME regId via `registry.regId`) must resync off
// EXACTLY those keys — read the scoped pointer, replay the scoped deltas up to the head the live
// channel declares, restore warm trust — and then reflect a hard purge that only the mirror
// carried. A single-registry / default-regId test could round-trip even under the OLD unscoped
// scheme; forcing a non-default regId is what makes the two sides' key agreement load-bearing.
//
// Convergence anchors to the live-channel head (E21): a reconnect emits reset → hello{head}, and
// the reader re-trusts once its scoped replay has carried the applied epoch up to that head. Pointer
// freshness / the E5 heartbeat is no longer a convergence anchor (CACHE-27).
//
// RED-PROOF (run at review): flip EITHER side's key scheme alone and this test reds — the reader
// pointed at unscoped `__state` (invalidation.ts) never finds the writer's scoped pointer, so its
// applied epoch never reaches the declared head and it stays suspect forever (its refill never
// stabilizes); the writer's prefix changed (execute.ts) leaves the reader's scoped key empty, so
// the scoped-pointer assertion below also fails.

import { mirrorKeys } from "@astilba/cache/registry";
import { mkTag, world } from "@astilba/harness";
import { describe, expect, it } from "vitest";

const REG_ID = "tenant-x";

describe("registry convergence under a non-default regId (E19/E21)", () => {
  it("the reader resyncs the SCOPED mirror, restores warm trust, and reflects a hard purge", async () => {
    const w = world({
      defaults: { unknownPolicy: "miss" },
      isolates: 2,
      regId: REG_ID,
      replicas: 2,
    });
    const writer = w.isolate(0).cache;
    const reader = w.isolate(1).cache;
    const tag = mkTag("t0");

    // Warm epoch 1 (delivered soft purge) and born the entry at epoch 1 (locally fresh), then a
    // strong prime so the reader's local map records EVERY tag the entry carries — the warm map
    // whose completeness must NOT let a since-purged value wave through while suspect (E20 i).
    await writer.expire({ tag });
    await w.advance(0);
    await writer.getOrSet({
      factory: async () => await Promise.resolve("v1"),
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
    await w.advance(0);

    // Hard purge at epoch 2 — DROP its bus frame so the reader must learn it by resyncing the
    // SCOPED delta, not the bus. The scoped `delta:2` + a fresh pointer land in the mirror.
    w.chaos.dropNextFrames(1);
    await writer.delete({ tag });
    await w.advance(1000);

    let fills = 0;
    const factory = async (): Promise<string> => {
      fills += 1;
      return await Promise.resolve(`v2-${fills}`);
    };

    // Read #1: suspect (the dropped hard-purge frame opened a new episode with no live-channel head
    // declared). The resync applies the SCOPED delta — so the reader now KNOWS the hard purge and
    // its applied epoch reaches the purge epoch — but with no head declared this episode it stays
    // suspect: every tag is unknown ⇒ 'miss' refills (v1 is never served), warm trust not restored.
    await reader.getOrSetEntry({ factory, key: "k", tags: [tag] });

    // Convergence is re-anchored to the live-channel handshake (E21): a reconnect emits
    // reset → hello{head}. The reader — already resynced up to that head via read #1's scoped
    // replay — reaches appliedEpoch ≥ head and re-trusts. A SEVERED reader could not reach the
    // head (its scoped replay found nothing), so the same reconnect leaves IT suspect.
    w.chaos.reconnect();
    await w.advance(0);

    // Read #2: suspicion CLEARED ⇒ warm trust restored. Read #1's refill — born at the converged
    // epoch, postdating the hard purge — classifies FRESH from LOCAL knowledge and is served with
    // no further refill.
    const second = await reader.getOrSetEntry({
      factory,
      key: "k",
      tags: [tag],
    });
    const fillsAfterConverge = fills;

    // Read #3: still converged, so read #2's value is served again from LOCAL knowledge WITHOUT a
    // refill. A SEVERED reader (never found the scoped pointer) never reached the declared head, so
    // it stays suspect forever and re-refills here: a different value, a higher fill count. That gap
    // is the convergence signal a broken round-trip cannot fake — and v1 (hard-deleted) is never
    // served either way (the purge is reflected).
    const third = await reader.getOrSetEntry({
      factory,
      key: "k",
      tags: [tag],
    });
    expect(third.value).not.toBe("v1");
    expect(third.value).toBe(second.value); // served the warm value locally…
    expect(fills).toBe(fillsAfterConverge); // …with NO further refill ⇒ converged

    // The writer blessed the SCOPED pointer, echoing the non-default regId, and wrote NO unscoped
    // pointer. A writer whose prefix diverged would leave this scoped key empty ⇒ echo undefined.
    const scoped = mirrorKeys(REG_ID);
    const ptr = await w.store.replica(0).get(scoped.stateKey);
    const parsed: unknown = JSON.parse(ptr?.value ?? "null");
    const echoedRegId =
      typeof parsed === "object" && parsed !== null && "regId" in parsed
        ? parsed.regId
        : undefined;
    expect(echoedRegId).toBe(REG_ID);
    await expect(w.store.replica(0).get("__state")).resolves.toBeUndefined();
  });
});
