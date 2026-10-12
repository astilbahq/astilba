// THE CONSTRUCTION-I/O CENSUS (E28, E28-b) — one of three conformance instruments.
//
// WHAT THIS INSTRUMENT PROVES, AND ONLY THIS: construction is COLD. Building a cache reaches for no
// binding — it mints no stub, opens no socket, fires no RPC. It is not a style preference: workerd
// refuses every one of those at global scope, so a factory that performs one cannot be called where
// the quickstart puts it, and that placement is the only one yielding a cache per isolate. A
// construction that mints also hands every later request a handle minted under the first, which the
// platform refuses in turn.
//
// WHAT IT DOES NOT PROVE — stated here because leaving it unstated is what let a violation through
// twice, green (E28-b). It says NOTHING about whether handles are minted PER USE. A factory that
// memoized its stub source — holding one connection forever, the exact shape the law forbids —
// builds just as coldly as one that mints per use, so this census waves it through. The family that
// catches it is `cross-request.test.ts`, and it is the only one that does. Nor does this file speak
// to whether the placement is legal; that finding belongs to the boot witness
// (`scripts/module-scope-boot.mjs`), which is the only rig whose module scope is a real isolate's.
// Three clauses, three instruments, and each of them can see exactly one.
//
// WHY A CENSUS AND NOT A LIST OF ASSERTIONS. The obvious test — "assert `get` was not called" — is
// only as good as the author's memory of which methods perform I/O, and this defect was found
// exactly because a census by NAME missed a site. So this counts by the THING: every binding is
// wrapped in a Proxy that records EVERY property access and EVERY call, including ones this file
// never heard of. A new I/O door added to a binding is caught without anyone remembering to add it
// here, and the assertion is against the EMPTY set rather than against an allowlist.
//
// NON-VACUITY IS PART OF THE PROOF, TWICE OVER. A census that records nothing would also pass
// against a factory that does nothing, so: (1) a deliberately-minting construction is shown to be
// CAUGHT (the census discriminates), and (2) the same instance is shown to perform real I/O on its
// first read (construction is deferred, not dead).
//
// LANE: integration, and DELIBERATELY not left of it — though nothing here is timing-dependent and
// no assertion tolerates anything. The census drives the REAL `createWorkersCache`, whose config
// names workerd's own binding types (`KVNamespace`, `DurableObjectNamespace`); a Node-side lane
// cannot NAME those types, and importing the factory there drags the `cloudflare:*` adapter subtree
// into the Node-typed program and reds the typecheck gate. So this lives where the Workers types
// live. It uses FAKE bindings regardless: construction is what is under test, and construction — if
// the clause holds — touches nothing real. The complementary proof, that the real runtime ACCEPTS a
// module-scope construction, is `scripts/module-scope-boot.mjs` on `wrangler dev`.
//
// THE INSTRUMENTS SEE DIFFERENT THINGS, AND NONE IS COMPLETE — measured, not assumed. Each of the
// three construction-I/O sites was restored in turn and both lanes were run against it:
//
//   • a SYNCHRONOUS mint (`coordinator.get(...)` hoisted into the factory) — caught by BOTH. It
//     throws during module evaluation, so workerd refuses to start the isolate at all.
//   • an ASYNC dial (the bus subscription moved back into the reader's constructor) — caught HERE
//     ONLY. The dial is `async`, so its global-scope refusal becomes a REJECTED PROMISE that the
//     client's detached `void (async …)()` swallows by design. The Worker boots clean and the boot
//     witness passes: the channel is simply dead, silently, forever.
//   • an RPC swallowed by design (the E11 retention lease fired from `createCache`, inside its
//     best-effort `try{}catch{}`) — caught HERE ONLY, for the same reason.
//
// So a green boot witness does NOT imply construction is cold; it implies construction did not
// throw. Two of the three sites are invisible to it precisely because they are the ones whose
// failures are swallowed — which is also why they survived so long. Symmetrically, a green census
// does not imply connections are minted per use: see the memoized-source liar named in
// `cross-request.test.ts`, which walks past this file untouched. Three instruments, three
// jurisdictions; deleting any one leaves a real class of defect unobserved.

import { describe, expect, it } from "vitest";

import { createWorkersCache } from "../../src/adapters/cloudflare/workers-cache";

/** Every access/call a Proxy witnessed, as `"<binding>.<prop>"`. */
type Ledger = string[];

/**
 * Wrap `target` so that every property READ and every CALL is recorded. Reads are recorded too, not
 * just calls: reaching for `.get` is already evidence that construction is shopping for a door it
 * has no business opening, and a lazy-thunk design touches nothing at all.
 */
function census<T extends object>(label: string, target: T, ledger: Ledger): T {
  return new Proxy(target, {
    get(obj, prop, receiver) {
      const name = String(prop);
      // `then` is probed by the runtime whenever a value passes through a promise; it is not the
      // caller shopping for a door, so recording it would be recording our own test harness.
      if (name === "then") {
        return Reflect.get(obj, prop, receiver);
      }
      ledger.push(`${label}.${name}`);
      const value = Reflect.get(obj, prop, receiver);
      if (typeof value === "function") {
        return (...args: unknown[]): unknown => {
          ledger.push(`${label}.${name}()`);
          // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- re-entering the wrapped target's own method with its own arguments; the cast only restores the callable type the Proxy `get` erased.
          return (value as (...a: unknown[]) => unknown).apply(obj, args);
        };
      }
      return value;
    },
  });
}

/** A KV namespace that is inert but structurally real. Any touch is recorded by the census wrapper. */
function fakeKv(): KVNamespace {
  const map = new Map<string, string>();
  const kv = {
    delete: async (key: string): Promise<void> => {
      map.delete(key);
    },
    get: async (key: string): Promise<string | null> => map.get(key) ?? null,
    getWithMetadata: async (
      key: string
    ): Promise<{ value: string | null; metadata: unknown }> => ({
      metadata: null,
      value: map.get(key) ?? null,
    }),
    list: async (): Promise<{
      keys: { name: string }[];
      list_complete: boolean;
    }> => ({ keys: [], list_complete: true }),
    put: async (key: string, value: string): Promise<void> => {
      map.set(key, value);
    },
  };
  // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- a structural stand-in for the KVNamespace surface this adapter actually uses (get/getWithMetadata/put/delete/list); the census only needs the doors to exist so a touch can be recorded.
  return kv as unknown as KVNamespace;
}

/** A Coordinator DO namespace whose `.get()` MINTS (recorded), and whose stub records its RPCs. */
function fakeCoordinator(ledger: Ledger): DurableObjectNamespace {
  const stub = {
    check: async (): Promise<unknown[]> => [],
    delete: async (): Promise<{ epoch: number }> => ({ epoch: 0 }),
    expire: async (): Promise<{ epoch: number }> => ({ epoch: 0 }),
    fetch: async (): Promise<Response> => new Response(null, { status: 500 }),
    registerRetention: async (): Promise<void> => {
      // Recorded by the census wrapper; no horizon state needed here.
    },
    id: "stub-id",
  };
  const namespace = {
    get: (): unknown => census("stub", stub, ledger),
    idFromName: (name: string): unknown => ({ toString: () => name }),
  };
  // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- a structural stand-in for the DurableObjectNamespace surface the adapter uses (idFromName + get); the census records every touch of either.
  return namespace as unknown as DurableObjectNamespace;
}

/** Build a censused set of bindings plus the ledger they write to. */
function censusBindings(): {
  ledger: Ledger;
  kv: KVNamespace;
  coordinator: DurableObjectNamespace;
} {
  const ledger: Ledger = [];
  return {
    coordinator: census("coordinator", fakeCoordinator(ledger), ledger),
    kv: census("kv", fakeKv(), ledger),
    ledger,
  };
}

describe("E28 — construction performs no I/O (the census)", () => {
  it("createWorkersCache touches NOTHING on any binding: the census is empty", () => {
    const { ledger, kv, coordinator } = censusBindings();

    createWorkersCache({
      coordinator:
        // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the census Proxy preserves the namespace's structure; the parameter is typed against the Coordinator-parameterized namespace, which the fake stands in for.
        coordinator as unknown as Parameters<
          typeof createWorkersCache
        >[0]["coordinator"],
      kv,
      name: "census",
    });

    // THE ASSERTION IS AGAINST THE EMPTY SET, not an allowlist. Construction captures `{namespace,
    // name}` in a thunk and wires drivers around it; it does not so much as READ `.get`. If this
    // ever reds, read the ledger: it names the exact door construction opened.
    expect(ledger).toStrictEqual([]);
  });

  it("NON-VACUITY (i): the census CATCHES a construction that mints", () => {
    // A stub minted at construction is the pre-E28 shape, and it is what workerd refuses at global
    // scope. Standing it up here proves the census above is discriminating rather than merely quiet:
    // the same instrument, pointed at the defect, sees it.
    const { ledger, coordinator } = censusBindings();

    // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- exercising the namespace's own surface directly to model the pre-E28 construction; the fake's shape is the census's subject, not a claim about the real type.
    const ns = coordinator as unknown as {
      get(id: unknown): unknown;
      idFromName(name: string): unknown;
    };
    ns.get(ns.idFromName("census"));

    // The mint is recorded — by the THING (a call on the namespace), with no allowlist consulted.
    expect(ledger).toContain("coordinator.get()");
  });

  it("NON-VACUITY (ii): the deferred I/O DOES happen on first use", async () => {
    // Construction being silent would be worthless if the instance were simply inert. The first read
    // must reach for the doors construction declined to touch — that is DEFERRAL, not absence.
    const { ledger, kv, coordinator } = censusBindings();

    const cache = createWorkersCache({
      coordinator:
        // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- as above: the census Proxy stands in for the Coordinator-parameterized namespace.
        coordinator as unknown as Parameters<
          typeof createWorkersCache
        >[0]["coordinator"],
      kv,
      name: "census",
      defaults: { maxEntryRetention: "30d" },
    });
    expect(ledger).toStrictEqual([]);

    await cache.getOrSet({
      factory: async (): Promise<string> => "v",
      key: "census/k",
    });

    // The stub is minted from INSIDE the read (`coordinator.get()`), which is the whole point: the
    // handle belongs to the context that is using it. And the E11 retention lease — deferred out of
    // construction by E28 — is registered here, on first use, rather than never.
    expect(ledger).toContain("coordinator.get()");
    expect(ledger).toContain("stub.registerRetention()");
  });
});
