// Ordinary unit teeth (NOT sacred) — C5-a (CACHE-21, MI12, D3/D4). memory()'s private-eviction
// disclosure: a PRIVATE (`usr:`-scoped) entry evicted from the LRU emits exactly one
// `private_evicted`, payload = reason class + counts + bytes ONLY (E23 payload discipline). Each
// case bites one branch (rule 13):
//   • emit tooth        — evicting a `usr:` entry emits exactly one event. Drop the emit ⇒ reds.
//   • no-identifier     — the payload carries NO key/hash/tag/value; only { type, count, bytes }. A
//     mutation that leaks the evicted key/scope into the payload ⇒ the strict-shape assertion reds.
//   • private-only      — evicting a `pub` (or scope-less) entry emits NOTHING (detection reads the
//     `usr:` scope metadata literal, not the key).
//   • safeEmit routing  — a throwing sink is swallowed (set() still resolves) and onSinkError fires;
//     two stores never cross-talk (D4, injectable — no module latch).

import { describe, expect, it } from "vitest";

import { memory } from "../../src/drivers";
import type { TelemetryEvent } from "../../src/telemetry";

/** UTF-8 byte length — what memory() accounts and discloses as `bytes`. */
const bytesOf = (value: string): number =>
  new TextEncoder().encode(value).length;

/** A canonical-shaped private key + its stored `scope` metadata literal (`usr:<hash>`, §3/§6). The
 * kernel writes both on every L1 set; memory() reads the metadata literal's `usr:` prefix (never the
 * hash) to know an evicted entry was private. */
const usrMeta = { metadata: { scope: "usr:9f3ahash" } } as const;
const pubMeta = { metadata: { scope: "pub" } } as const;

/** A telemetry sink whose transport is broken — the eviction disclosure calls it, safeEmit swallows
 * the throw, and the injected `onSinkError` observes it. */
const throwingSink = (): void => {
  throw new Error("private-evicted sink is broken");
};

describe("memory() MI12 private-eviction disclosure (CACHE-21, §6, E23)", () => {
  it("evicting a usr: entry emits exactly one private_evicted — payload is class + count + bytes ONLY", async () => {
    const events: TelemetryEvent[] = [];
    const store = memory({
      maxEntries: 1,
      telemetry: (event) => {
        events.push(event);
      },
    });
    // The victim: a PRIVATE entry whose key names the user and whose scope carries a principal hash.
    await store.set("1:journals:usr:9f3ahash:profile", "abcde", usrMeta);
    // A second set overflows maxEntries:1 ⇒ the LRU (the private entry) is evicted.
    await store.set("1:journals:usr:9f3ahash:settings", "x", usrMeta);

    // Exactly one disclosure. Drop the emit in memory()'s eviction loop and this is length 0 ⇒ reds.
    expect(events).toHaveLength(1);
    // The WHOLE payload — nothing but the class, a count, and the freed bytes. A mutation that
    // leaked the evicted key or scope hash into the payload adds a field ⇒ this strict shape reds.
    expect(events[0]).toStrictEqual({
      bytes: bytesOf("abcde"),
      count: 1,
      type: "private_evicted",
    });
    // Belt-and-braces on the privacy claim: no key fragment, no scope hash, no value content, no tag
    // survives serialization.
    const serialized = JSON.stringify(events[0]);
    for (const secret of ["profile", "settings", "usr", "9f3ahash", "abcde"]) {
      expect(serialized).not.toContain(secret);
    }
  });

  it("evicting a pub entry emits NOTHING (detection is the usr: scope literal, not the key)", async () => {
    const events: TelemetryEvent[] = [];
    const store = memory({
      maxEntries: 1,
      telemetry: (event) => {
        events.push(event);
      },
    });
    await store.set("1:journals:pub:home", "abcde", pubMeta);
    await store.set("1:journals:pub:about", "x", pubMeta);
    expect(events).toStrictEqual([]);
  });

  it("evicting a scope-less entry (raw store use) emits NOTHING", async () => {
    const events: TelemetryEvent[] = [];
    const store = memory({
      maxEntries: 1,
      telemetry: (event) => {
        events.push(event);
      },
    });
    // No `scope` metadata at all — a non-canonical store never matches the private predicate.
    await store.set("raw-key-1", "abcde");
    await store.set("raw-key-2", "x");
    expect(events).toStrictEqual([]);
  });

  it("routes the emit through safeEmit: a throwing sink is swallowed (set resolves) and onSinkError fires", async () => {
    const errors: unknown[] = [];
    const store = memory({
      maxEntries: 1,
      onSinkError: (caught) => {
        errors.push(caught);
      },
      telemetry: throwingSink,
    });
    await store.set("1:journals:usr:9f3ahash:profile", "abcde", usrMeta);
    // The eviction fires the (throwing) sink. If the emit were a RAW sink call the throw would
    // propagate out of set() ⇒ this rejects. Through safeEmit it is swallowed and set() resolves.
    await expect(
      store.set("1:journals:usr:9f3ahash:settings", "x", usrMeta)
    ).resolves.toBeUndefined();
    // The swallowed failure reached the injected hook.
    expect(errors).toHaveLength(1);
    expect(errors[0]).toBeInstanceOf(Error);
  });

  it("onSinkError is per-store, no cross-talk: two stores each observe only their own failure (D4)", async () => {
    const errorsA: unknown[] = [];
    const errorsB: unknown[] = [];
    const storeA = memory({
      maxEntries: 1,
      onSinkError: (caught) => {
        errorsA.push(caught);
      },
      telemetry: throwingSink,
    });
    const storeB = memory({
      maxEntries: 1,
      onSinkError: (caught) => {
        errorsB.push(caught);
      },
      telemetry: throwingSink,
    });
    // storeA evicts a private entry (its throwing sink fires); storeB does NOT (one entry, no
    // overflow). A module latch would leak storeA's failure into B's hook, or miscount ⇒ reds.
    await storeA.set("1:journals:usr:9f3ahash:profile", "abcde", usrMeta);
    await storeA.set("1:journals:usr:9f3ahash:settings", "x", usrMeta);
    await storeB.set("1:journals:usr:9f3ahash:profile", "abcde", usrMeta);
    expect(errorsA).toHaveLength(1);
    expect(errorsB).toHaveLength(0);
  });
});
