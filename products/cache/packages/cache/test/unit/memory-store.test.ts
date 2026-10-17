// Ordinary unit teeth (NOT sacred), the memory() L1 store.
//
// The Store conformance contract drives round-trip / RCW / read-hint / TTL-expiry /
// oversize against the memory subject, but it does NOT cover bounds + LRU eviction —
// FakeStore never evicts, so the contract has no such check. These teeth pin the
// eviction/bounds/TTL-config branches memory() adds, each named so it bites exactly one
//: blind LRU eviction ⇒ a bounds tooth reds; blind byte-accounting ⇒ the
// maxBytes tooth reds; blind the too_large throw ⇒ the oversize tooth reds.

import { isStoreWriteError, memory } from "@astilba/cache";
import type { Store } from "@astilba/cache";
import { describe, expect, it } from "vitest";

const encoder = new TextEncoder();

/** A 10-byte ASCII value (bytes === length) — the byte-budget arithmetic unit. */
const tenBytes = (char: string): string => char.repeat(10);

/** Read a key's stored value (or undefined). A get is a recency TOUCH in the store. */
async function readValue(
  store: Store,
  key: string
): Promise<string | undefined> {
  const read = await store.get(key);
  return read?.value;
}

describe("memory() L1 store — bounds, eviction & TTL config (§8, C4-a-0-ii)", () => {
  it("evicts the least-recently-used entry once maxEntries is exceeded", async () => {
    const store = memory({ maxEntries: 2 });
    await store.set("k1", "a");
    await store.set("k2", "b");
    await store.set("k3", "c"); // size 3 > 2 ⇒ LRU (k1) evicted
    // Blinding the eviction loop leaves k1 resident ⇒ this reds.
    await expect(readValue(store, "k1")).resolves.toBeUndefined();
    await expect(readValue(store, "k2")).resolves.toBe("b");
    await expect(readValue(store, "k3")).resolves.toBe("c");
  });

  it("evicts LRU entries until the byte budget holds and never exceeds maxBytes", async () => {
    const budget = 30;
    const store = memory({ maxBytes: budget }); // maxEntries unbounded ⇒ eviction is purely byte-driven
    await store.set("a", tenBytes("a")); // total 10
    await store.set("b", tenBytes("b")); // total 20
    await store.set("c", tenBytes("c")); // total 30 — at budget, no evict
    await store.set("d", tenBytes("d")); // total 40 > 30 ⇒ evict LRU (a) back to 30
    // Blinding byte-accounting means totalBytes never crosses the budget ⇒ nothing evicts
    // ⇒ "a" survives ⇒ this reds.
    await expect(readValue(store, "a")).resolves.toBeUndefined();
    let residentBytes = 0;
    for (const key of ["b", "c", "d"]) {
      const value = await readValue(store, key);
      expect(value).toBeDefined();
      residentBytes += value === undefined ? 0 : encoder.encode(value).length;
    }
    expect(residentBytes).toBeLessThanOrEqual(budget);
  });

  it("tracks recency, not insertion order: a get protects a key so a later eviction drops the untouched one", async () => {
    const store = memory({ maxEntries: 2 });
    await store.set("k1", "a");
    await store.set("k2", "b");
    // Touch k1 ⇒ k1 becomes MRU, k2 becomes the LRU victim.
    await expect(readValue(store, "k1")).resolves.toBe("a");
    await store.set("k3", "c"); // size 3 > 2 ⇒ evict LRU (k2, NOT k1)
    // If recency were untracked (plain insertion order), k1 would be evicted instead ⇒ this reds.
    await expect(readValue(store, "k2")).resolves.toBeUndefined();
    await expect(readValue(store, "k1")).resolves.toBe("a");
    await expect(readValue(store, "k3")).resolves.toBe("c");
  });

  it("rejects an oversize value with a classified too_large and keeps the prior value byte-identical", async () => {
    const budget = 16;
    const store = memory({ maxBytes: budget });
    await store.set("k", "prior");
    const oversize = "x".repeat(budget + 1); // one byte over the whole budget
    let caught: unknown;
    try {
      await store.set("k", oversize);
    } catch (error) {
      caught = error;
    }
    // Blinding the too_large throw lets the oversize write land (truncated or whole) ⇒ this reds.
    expect(isStoreWriteError(caught)).toBeTruthy();
    expect(caught).toMatchObject({ code: "too_large", retryable: false });
    await expect(readValue(store, "k")).resolves.toBe("prior");
  });

  it("fails loud when a set carries expirationTtl but no clock was injected", async () => {
    const store = memory({}); // clock-less
    // Silently ignoring the TTL (storing without residency) would resolve ⇒ this reds.
    await expect(store.set("k", "v", { expirationTtl: 60 })).rejects.toThrow(
      /clock/u
    );
  });

  it("honors expirationTtl on the injected clock: present before expiry, absent once residency elapses", async () => {
    let now = 0;
    const store = memory({ clock: { now: (): number => now } });
    await store.set("k", "v", { expirationTtl: 1 }); // expireAt = now + 1000ms
    await expect(readValue(store, "k")).resolves.toBe("v");
    now = 1000; // >= expireAtMs ⇒ expired
    // Blinding the expiry check on get keeps the value resident ⇒ this reds.
    await expect(readValue(store, "k")).resolves.toBeUndefined();
  });

  it("delete removes an entry and frees its bytes against the budget", async () => {
    const budget = 20;
    const store = memory({ maxBytes: budget });
    await store.set("a", "x".repeat(15)); // total 15
    await store.delete("a"); // frees 15 ⇒ total 0
    await store.set("b", "y".repeat(15)); // total 15 — fits only if delete freed the bytes
    await expect(readValue(store, "a")).resolves.toBeUndefined();
    await expect(readValue(store, "b")).resolves.toBe("y".repeat(15));
  });
});
