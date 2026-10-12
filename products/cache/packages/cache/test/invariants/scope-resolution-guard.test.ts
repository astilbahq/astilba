/* References: INV-4, §11.4, §6, E17, INV-20, INV-8, E1. */

import { createCache } from "@astilba/cache";
import type { Store, StoreMetadata, StoreWriteOptions } from "@astilba/cache";
import { FakeStore, SimClock, SimRng, world } from "@astilba/harness";
import { test } from "@fast-check/vitest";
import { describe, expect } from "vitest";

/** A `Store` decorator that reads/writes through to `inner` (so the cache behaves
 * exactly as with a bare store) while capturing the metadata of every write — the
 * only way to witness the stored envelope's resolved `scope` without editing the
 * harness. `writes` is the ordered log of captured write metadata. */
interface CapturingStore extends Store {
  readonly writes: readonly StoreMetadata[];
}

function capturingStore(inner: Store): CapturingStore {
  const writes: StoreMetadata[] = [];
  return {
    delete: async (key) => {
      await inner.delete(key);
    },
    get: async (key) => await inner.get(key),
    set: async (key, value, options?: StoreWriteOptions) => {
      if (options?.metadata !== undefined) {
        writes.push(options.metadata);
      }
      await inner.set(key, value, options);
    },
    writes,
  };
}

describe("scope resolution (E17)", () => {
  test("INV-4: a contextless default write is durable and readable from a second isolate (the `pub` storage class, E17 rule 3)", async () => {
    const w = world({ isolates: 2 });
    const producer = w.isolate(0).cache;
    const reader = w.isolate(1).cache;
    // No `scope`, no `request` — the contextless default. Per E17 rule 3 this is the
    // shared `pub` class: it must land durably in L2, not be confined to L1.
    const written = await producer.getOrSetEntry({
      factory: async () => await Promise.resolve("shared-value"),
      key: "landing-page",
    });
    expect(written.value).toBe("shared-value");
    expect(written.durable).toBeTruthy();
    // A SECOND isolate (its own empty L1) must read the SAME value from the shared
    // tier — never a refill. If the contextless write had regressed to L1-only, the
    // producer's entry would be invisible here and this factory would run instead.
    const seen = await reader.getOrSetEntry({
      factory: async () => await Promise.resolve("would-be-refill"),
      key: "landing-page",
    });
    expect(seen.value).toBe("shared-value");
  });

  test("INV-4: a request-bearing default write is `usr:<hash>` — L1-only, durable:false, and the stored scope carries a non-empty principal hash (E17 rule 2, hashless `usr:` abolished)", async () => {
    const clock = new SimClock();
    const store = new FakeStore(clock, { replicas: 1 });
    const l1 = capturingStore(new FakeStore(clock, { replicas: 1 }).replica(0));
    const l2 = capturingStore(store.replica(0));
    const cache = createCache({
      clock,
      l1,
      l2,
      namespace: "journals",
      rng: new SimRng(1),
    });
    // No declared `scope`, but a request carrying an adapter-derived principal —
    // E17 rule 2: `usr:<hash>`, confined to L1, never published to the shared tier.
    const written = await cache.getOrSetEntry({
      factory: async () => await Promise.resolve("private-value"),
      key: "profile",
      request: { userId: "u-42" },
    });
    expect(written.value).toBe("private-value");
    // Private ⇒ L1-only ⇒ not durable (never written to the shared L2 tier).
    expect(written.durable).toBeFalsy();
    expect(l2.writes).toHaveLength(0);
    // The stored envelope's scope begins `usr:` AND carries a non-empty hash suffix —
    // NOT the abolished bare `"usr:"` literal (E17: every `usr:` carries a hash).
    const scopes = l1.writes
      .map((meta) => meta.scope)
      .filter((scope): scope is string => typeof scope === "string");
    expect(scopes.length).toBeGreaterThan(0);
    for (const scope of scopes) {
      expect(scope.startsWith("usr:")).toBeTruthy();
      expect(scope.length).toBeGreaterThan("usr:".length);
      expect(scope).not.toBe("usr:");
    }
  });

  test("INV-4: a declared tenant scope stores a HASHED `ten:<h>` literal, never the raw tenant (§3, E17 addendum)", async () => {
    const clock = new SimClock();
    const store = new FakeStore(clock, { replicas: 1 });
    const l1 = capturingStore(new FakeStore(clock, { replicas: 1 }).replica(0));
    const l2 = capturingStore(store.replica(0));
    const cache = createCache({
      clock,
      l1,
      l2,
      namespace: "journals",
      rng: new SimRng(1),
    });
    // A declared `scope:{tenant}` write — §3's envelope `scope` grammar
    // (`'pub' | 'usr:<h>' | 'ten:<h>'`) hashes the tenant exactly like `usr:<h>`
    // (E17 addendum: "`ten:<h>` uses the same 128-bit truncation (§3)"). The raw
    // `ten:<tenant>` literal is the abolished pre-v0.1 deviation.
    const written = await cache.getOrSetEntry({
      factory: async () => await Promise.resolve("tenant-value"),
      key: "dashboard",
      scope: { tenant: "acme" },
    });
    expect(written.value).toBe("tenant-value");
    // Tenant scope is durable (a scoped shared tier), so it lands in L2.
    const scopes = l2.writes
      .map((meta) => meta.scope)
      .filter((scope): scope is string => typeof scope === "string");
    expect(scopes.length).toBeGreaterThan(0);
    for (const scope of scopes) {
      expect(scope.startsWith("ten:")).toBeTruthy();
      expect(scope.length).toBeGreaterThan("ten:".length);
      // NOT the raw tenant literal — the hashless `ten:acme` form is the
      // deviation the E17 addendum flags to correct before v0.1.
      expect(scope).not.toBe("ten:acme");
    }
  });
});
