// C4-a-1 — the two `createWorkersCache` factory teeth (§2/E22, E9).
//
//   • COMPILE-TIME tooth: the SIGNED required key set of `WorkersCacheConfig` is EXACTLY
//     `{ name, kv, coordinator }` — ceiling 3. Adding a 4th required field flips the `Equal` to
//     false and `Expect<false>` fails the integration typecheck (LEG2), breaking the build.
//   • RUNTIME tooth (real bindings): the factory stamps the fail-closed safe posture — eventual +
//     SIE + `unknownPolicy: 'registry-check'` — and inherits createCache's private-by-default scope
//     resolution (a principal-derived request stays L1-only, `durable:false`).
//
// Lane law (rule 12): the runtime tooth is a cold fill + a scope check; both resolve without timing
// tolerance, so no polling is needed despite the integration lane.

import { createWorkersCache } from "@astilba/cache/cloudflare";
import type { WorkersCacheConfig } from "@astilba/cache/cloudflare";
import { t } from "@astilba/cache";
import { env } from "cloudflare:workers";
import { describe, expect, it } from "vitest";

// ── Compile-time tooth: RequiredKeys<WorkersCacheConfig> is EXACTLY {name, kv, coordinator} ──

/** The keys of T that are REQUIRED (a key K is required iff {} is not assignable to Pick<T, K>). */
type RequiredKeys<T> = {
  [K in keyof T]-?: {} extends Pick<T, K> ? never : K;
}[keyof T];

/** Exact type equality (invariant in both directions — order-independent for unions). */
type Equal<A, B> =
  (<T>() => T extends A ? 1 : 2) extends <T>() => T extends B ? 1 : 2
    ? true
    : false;

/** Compiles only when its argument is exactly `true`. */
type Expect<T extends true> = T;

// THE TOOTH. If a 4th required field is added to WorkersCacheConfig, RequiredKeys gains it, the
// Equal becomes `false`, and `Expect<false>` is a type error here → LEG2 (`tsc -p
// test/integration/tsconfig.json`) fails. Ceiling 3, EXACTLY the signed key set.
type _WorkersCacheRequiredKeysAreExactlyThree = Expect<
  Equal<RequiredKeys<WorkersCacheConfig>, "name" | "kv" | "coordinator">
>;

// ── Runtime tooth: the safe posture, against real bindings ──

describe("createWorkersCache factory teeth (§2/E22, E9)", () => {
  it("safe posture is fail-closed: an eventual re-read live-checks + serves cached (registry-check, never miss)", async () => {
    // The mutation-(b) tooth. The factory stamps `unknownPolicy: 'registry-check'` (fail-closed).
    // On a cold fill the entry's tag is not yet known-clean, so a following EVENTUAL re-read must
    // resolve the unknown validity by a LIVE registry check, confirm the tag fresh, and SERVE the
    // cached value — the factory does NOT run again. Flip the default to a non-fail-closed `'miss'`
    // (unknown ⇒ treat as a miss) and the re-read refills (v2), reddening BOTH assertions below.
    const cache = createWorkersCache({
      name: "wc-failclosed",
      kv: env.CACHE_KV,
      coordinator: env.COORDINATOR,
    });
    const key = "wc-failclosed/k";
    const tag = t`book:failclosed`;
    let fills = 0;
    const factory = async (): Promise<string> => {
      fills += 1;
      return `v${fills}`;
    };
    expect(await cache.getOrSet({ key, tags: [tag], factory })).toBe("v1");
    expect(await cache.getOrSet({ key, tags: [tag], factory })).toBe("v1");
    expect(fills).toBe(1);
  });

  it("constructs against real bindings and serves; a principal-scoped fill stays private (L1-only)", async () => {
    // Minimal signed construction — real KV + real Coordinator DO namespace.
    const cache = createWorkersCache({
      name: "wc-posture",
      kv: env.CACHE_KV,
      coordinator: env.COORDINATOR,
    });

    // A public getOrSet serves — the full composition (memory L1 + cloudflareKV L2 + doRegistry +
    // doBus) was constructed and ran end-to-end against the real bindings.
    const served = await cache.getOrSet({
      key: "wc-posture/public",
      tags: [t`book:posture`],
      factory: async () => "shared",
    });
    expect(served).toBe("shared");

    // Private-by-default (§6, E17): a principal-derived request (a `userId`) resolves to a `usr:`
    // scope that is L1-ONLY and therefore NOT durable — the value is never published to the shared
    // L2 tier. Observed directly via the entry form's `durable` flag. A safe posture the factory
    // inherits unchanged from createCache's scope resolution.
    const entry = await cache.getOrSetEntry({
      key: "wc-posture/private",
      request: { userId: "u-posture" },
      factory: async () => "secret",
    });
    expect(entry.value).toBe("secret");
    expect(entry.durable).toBe(false);
  });
});
