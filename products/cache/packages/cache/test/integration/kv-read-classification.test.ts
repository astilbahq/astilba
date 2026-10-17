import { env } from "cloudflare:test";
import { createCache } from "@astilba/cache";
import { describe, expect, it } from "vitest";

import { cloudflareKV } from "../../src/adapters/cloudflare/kv";
import type { StoreMetadata } from "../../src/drivers";

// The DRIVER-FED tooth for the GET half. The kernel's read guard absorbs only a
// CLASSIFIED rejection and rethrows anything else — deliberately, so an unnamed driver fault reaches
// the caller instead of laundering into a confident "not found". That posture only pays if the
// driver at the boundary actually names its faults: platform knowledge lives in the adapter, and
// `kv.ts` is the only file in this repo that knows what a KV 429 looks like. An adapter that
// classifies nothing turns the kernel's backstop into the behavior of every real read failure.
//
// So this drives the REAL `cloudflareKV` — not a hand-written fake Store — through the REAL
// `createCache`, with a namespace whose read rejects in KV's production shape (an ordinary `Error`
// carrying `status: 429`; Miniflare emits no real 429 of its own, D2/D14). What must happen is the
// whole GET half end to end: the read failure is classified at the boundary, suppressed to a plain
// miss, and refilled from origin. The caller never sees it.
//
// Mutation: remove the try/catch around the `getWithMetadata` call in
// `CloudflareKvStore.get` ⇒ the raw rejection escapes the driver unclassified ⇒ the kernel's
// backstop rethrows it ⇒ `getOrSet` rejects ⇒ reds. That mutation is the pre-fix state of the file.
//
// Lane: integration — not by lane law's tolerance (this trace is deterministic and uses none) but by
// TYPE reach: `src/adapters/cloudflare/**` is excluded from the kernel tsc program and typechecks
// only under this lane's Workers-typed program, so the driver's own door opens here and nowhere left
// of it.

const NS = "kv-read-classification";
const CLOCK = { now: (): number => 0 };
const RNG = { next: (): number => 0 };

/**
 * KV's production rate-limit rejection, reproduced: workerd throws an ORDINARY `Error` carrying a
 * numeric `status` — no bespoke class, no documented structured field beyond it. Built with
 * `Object.assign` rather than a subclass precisely so nothing here is more structured than the real
 * thing; a driver that only recognizes a tidy fixture recognizes nothing in production.
 */
function kvGetRateLimited(): Error {
  return Object.assign(new Error("KV GET failed: 429 Too Many Requests"), {
    status: 429,
  });
}

/**
 * The real KV binding with its READ replaced by a rejection — writes and deletes still land in real
 * KV. `new Proxy<KVNamespace>` is typed by the target, so the binding needs no cast.
 */
function readRejectingNamespace(inner: KVNamespace): KVNamespace {
  return new Proxy(inner, {
    get(target, property, receiver) {
      if (property === "getWithMetadata") {
        return (): Promise<
          KVNamespaceGetWithMetadataResult<string, StoreMetadata>
        > => Promise.reject(kvGetRateLimited());
      }
      return Reflect.get(target, property, receiver);
    },
  });
}

describe("cloudflareKV classifies a rejected read (§8, E13)", () => {
  it("suppresses a production-shaped 429 GET to a miss and refills — the caller never sees it", async () => {
    const cache = createCache({
      clock: CLOCK,
      l2: cloudflareKV(readRejectingNamespace(env.REGISTRY_KV)),
      namespace: NS,
      rng: RNG,
    });
    let calls = 0;
    // The whole ordered mechanism in one trace: the adapter names the fault, the kernel absorbs the
    // named fault, and the miss it becomes is refilled from origin. An unclassified escape reds the
    // resolve; a driver that swallowed the read into a silent `undefined` would still pass here —
    // which is why suppression is proven at the KERNEL's guard (unit lane) and CLASSIFICATION is
    // proven here, at the only layer that knows a 429 from anything else.
    await expect(
      cache.getOrSet({
        factory: async (): Promise<string> => {
          calls += 1;
          return "filled";
        },
        key: "k",
      })
    ).resolves.toBe("filled");
    expect(calls).toBe(1);
  });

  it("classifies TOTALLY — a rejection of NO recognized shape is still a miss-class event", async () => {
    // the posture is about the READ, not about the taxonomy: an L2 read failure of ANY shape is a
    // miss-class event, so the driver's classifier is TOTAL and an unrecognized rejection lands on
    // transient `unavailable` rather than escaping unnamed. A driver that classified only the shapes
    // it recognizes would leave every UNRECOGNIZED production fault — the ones nobody wrote a
    // recognizer for, which is the interesting set — falling through the kernel's backstop to the
    // caller. Blind: make `classifyReadError` rethrow what `isRateLimited` does not match ⇒ reds.
    const opaque: KVNamespace = new Proxy(env.REGISTRY_KV, {
      get(target, property, receiver) {
        if (property === "getWithMetadata") {
          return (): Promise<never> =>
            Promise.reject(new Error("kv: something nobody has a recognizer for"));
        }
        return Reflect.get(target, property, receiver);
      },
    });
    const cache = createCache({
      clock: CLOCK,
      l2: cloudflareKV(opaque),
      namespace: NS,
      rng: RNG,
    });
    await expect(
      cache.getOrSet({
        factory: async (): Promise<string> => "filled",
        key: "k2",
      })
    ).resolves.toBe("filled");
  });
});
