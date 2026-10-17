// CROSS-REQUEST family — one of three conformance
// instruments, and the ONLY one with this jurisdiction.
//
// WHAT THIS INSTRUMENT PROVES, AND WHAT NOTHING ELSE HERE DOES: that a handle is minted for each
// use and let go, rather than kept. the central clause, and the one no other rig in this repo can
// see. The census next door rules only on whether construction touched a binding; the boot witness
// rules only on whether module-scope placement runs at all. A cache can satisfy both of those and
// still hoard a connection, which is exactly what happened.
//
// THE ORACLE DIMENSION THIS OPENS. Every other test in this repo lives inside ONE request context:
// the sacred suite runs on fakes with no platform at all, and the rest of this lane constructs and
// exercises a cache inside a single `it(...)`. So nothing here could observe the one law that
// governs a cache a real Worker holds — that client-held I/O handles belong to the request that
// minted them and are refused everywhere else. A cold fill of a new key succeeds under a broken
// instance, which is precisely why the gap was invisible: the composition looks healthy and fails
// on the first read that has to consult the registry.
//
// ── THE NAMED LIAR ──────────────────────────────────────────────────────────────────────
// The mutation this family exists to catch, recorded because it is the one that got away twice:
//
//     MEMOIZE THE STUB SOURCE in `createWorkersCache` — hoist `stubFor`'s result behind a cache,
//         let stub = undefined
//         const stubFor = () => (stub ??= coordinator.get(coordinator.idFromName(name)))
//     so the factory mints ONE handle at first use and retains it forever.
//
// That is precisely the violation the reachability rule forbids: a long-lived object holding a connection instead of
// an address. It is also, measured, a LIAR that passes:
//
//   • the construction-I/O census — GREEN. Construction is still perfectly cold; the memo is not
//     filled until the first use. Coldness and per-use minting are different properties.
//   • the module-scope boot witness — GREEN. It boots, and both its requests are cold fills of
//     different keys, so neither reaches the handle the other minted.
//   • the entire Node-side test suite — GREEN. It has no platform to refuse anything.
//
// Only the cases below red, because only they issue a SECOND request that reaches a handle the FIRST
// one minted, against real workerd, which then refuses it. That is what a jurisdiction is for, and
// why the design makes every instrument state one: a gate listed by name alone reads as covering more
// than it can see, and the gap between the two is where this defect lived, twice, in plain sight.
//
// THE APPARATUS (an observation names its rig). `SELF.fetch()` enters `worker.ts`'s handler in a
// request context distinct from the test's own — verified on this pool by holding a stub across two
// such calls and watching the platform refuse the second. The worker holds ONE module-scope cache;
// each route below is therefore a second, third, … request against an instance built earlier.
//
// Lane law: these are integration-lane tests — real workerd, real bindings. The
// assertions are deterministic (fill counts and served values), not timing-dependent.

import { SELF } from "cloudflare:test";
import { describe, expect, it } from "vitest";

/** The platform refusal a request-scoped handle produces when reused in a later request. */
const CROSS_REQUEST_REFUSAL = "Cannot perform I/O on behalf of a different request";

/** One request into the worker, as its own request context. Returns the body. */
async function hit(path: string): Promise<string> {
  const response = await SELF.fetch(`https://witness${path}`);
  return await response.text();
}

describe("E28 — one instance, many requests (the cross-request family)", () => {
  it("reuse-l1hit: a second request serves the entry the first request filled", async () => {
    // Request 1 builds nothing new: the cache already exists at module scope. It cold-fills.
    const first = await hit("/e28/get?key=e28/l1hit");
    expect(first).toBe("v1");
    // Request 2 — a DIFFERENT request context, same instance. The entry is in L1 and its tag is
    // known-clean, so this must serve `v1` without refilling. Under a cache holding a stub minted in
    // request 1, any registry-touching path here is refused by the platform instead.
    const second = await hit("/e28/get?key=e28/l1hit");
    expect(second).not.toContain(CROSS_REQUEST_REFUSAL);
    expect(second).toBe("v1");
  });

  it("reuse-strong: a strong read in a later request live-checks the registry", async () => {
    // A strong read ALWAYS performs a live registry RPC — it is the shortest path from a
    // reused instance to a stub call, and the first thing a held handle breaks.
    const first = await hit("/e28/get?key=e28/strong");
    expect(first).toBe("v1");
    const second = await hit("/e28/strong?key=e28/strong");
    expect(second).not.toContain(CROSS_REQUEST_REFUSAL);
    // The live check confirms the tag clean, so the stored value serves — the factory does not rerun.
    expect(second).toBe("v1");
  });

  it("reuse-expire: a purge issued in a later request reaches the registry and invalidates", async () => {
    const first = await hit("/e28/get?key=e28/expire");
    expect(first).toBe("v1");
    // `expire` is a registry mutation from a request that did not build the cache.
    const purge = await hit("/e28/expire");
    expect(purge).not.toContain(CROSS_REQUEST_REFUSAL);
    expect(purge).toMatch(/^epoch \d+$/u);
    // …and the purge is real: a strong read in yet another request observes it and refills.
    const refilled = await hit("/e28/strong?key=e28/expire");
    expect(refilled).not.toContain(CROSS_REQUEST_REFUSAL);
    expect(refilled).toBe("v2");
  });
});
