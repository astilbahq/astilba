// The Coordinator wrapper's own tests (C5-c) — the demo's machinery.
//
// Two of these exist because of bugs this wrapper actually shipped with, and both had the same
// shape: the wrapper injecting a fault it never meant to inject, into the exact path it exists to
// observe. A Durable Object stub is itself a proxy that SYNTHESISES its RPC methods on property
// access, so the two reflexes any wrapper author reaches for — forward the receiver, bind the
// method — are both traps, and both of them fail at runtime with an error that looks like the
// library's fault rather than the harness's. The `rpcStub` fixture below reproduces the synthesis
// faithfully enough to catch them, which is the only reason they cannot come back.

import { describe, expect, it } from "vitest";

import type { FaultDials } from "./dials";
import { emptyBusStats, noFaults } from "./dials";
import { faultableCoordinator } from "./faultable-coordinator";

/** The RPC methods the Coordinator actually implements. Anything else the stub is asked to
 * synthesise fails at the far end — including, notoriously, `bind`. */
const RPC_METHODS: ReadonlySet<string> = new Set([
  "check",
  "delete",
  "expire",
  "registerRetention",
]);

/**
 * A stand-in for a Durable Object stub that behaves like one in the two ways that bit this wrapper
 * for real: `fetch` is a genuine method, and EVERY other property access synthesises a callable
 * whose invocation is a remote call. So `stub.bind` does not give you `Function.prototype.bind` —
 * it gives you a callable that tries to invoke a method named `bind` on the far side and fails.
 * Synthesis happens against the RECEIVER, so being handed the wrong one fails the same way.
 */
type RpcMethod = (...args: readonly unknown[]) => Promise<unknown>;
type RpcSurface = Record<string, RpcMethod>;

/** Reach a method on the fixture stub. A real stub synthesises EVERY property, so nothing here is
 * ever genuinely missing — this only satisfies the compiler's (correct) pessimism about indexing an
 * open record, and keeps the tests reading as the property accesses they are really making. */
function on(stub: RpcSurface, method: string): RpcMethod {
  const found = stub[method];
  if (found === undefined) {
    throw new Error(`the fixture stub synthesised nothing for ${method}`);
  }
  return found;
}

function rpcStub(reached: string[]) {
  const target: RpcSurface = {
    fetch: async (...args) => {
      reached.push(`fetch:${String(args[0])}`);
      return await Promise.resolve({ status: 101 });
    },
  };
  const stub: RpcSurface = new Proxy(target, {
    get(base, property, receiver) {
      if (property === "fetch") {
        return Reflect.get(base, property);
      }
      if (receiver !== stub) {
        // The real stub synthesises against the receiver; handed the wrong one, every call fails.
        throw new TypeError(
          `The RPC receiver does not implement the method "${String(property)}".`
        );
      }
      const method = async (...args: readonly unknown[]) => {
        if (!RPC_METHODS.has(String(property))) {
          throw new TypeError(
            `The RPC receiver does not implement the method "${String(property)}".`
          );
        }
        reached.push(`rpc:${String(property)}(${args.join(",")})`);
        return `rpc-result:${String(property)}`;
      };
      // A synthesised RPC method is not an ordinary function you may poke at: it is a handle to a
      // remote path, and reaching for a property ON it asks the far side for a method by that name.
      // So `someRpcMethod.bind` is not `Function.prototype.bind` — it is a request for a
      // Coordinator method called `bind`, and it fails. Modelling this is the whole reason the
      // wrapper's `.bind` reflex is catchable here; a plain function would let it through, because
      // `.bind` on a plain function works perfectly. It did let it through, once.
      return new Proxy(method, {
        get(_target, accessed) {
          throw new TypeError(
            `The RPC receiver does not implement the method "${String(accessed)}".`
          );
        },
      });
    },
  });
  return stub;
}

/**
 * A stand-in for the `COORDINATOR` NAMESPACE — as distinct from the stub it hands out.
 *
 * IT KEEPS ITS SCOPE IN A PRIVATE FIELD FOR THE SAME REASON THE KV FIXTURE DOES. A real namespace
 * binding is a host object, and its methods reach internal slots that a Proxy receiver does not
 * have. `idFromName` here touches `#scope`, which is brand-checked against the real instance — so
 * the wrapper's `member.bind(target)` passthrough for every non-`get` member is load-bearing, and
 * deleting it turns `idFromName` into a TypeError instead of leaving the suite green.
 *
 * The namespace's `.bind` and the STUB's `.bind` are two different branches in two different
 * proxies, and they want opposite things: the namespace's members must be BOUND (they are ordinary
 * host methods), while the stub's synthesised RPC surface must never be touched at all (asking it
 * for `.bind` asks the far side for a method by that name). Getting one right says nothing about
 * the other, so each has its own tooth.
 */
class CoordinatorNamespace {
  readonly #scope: string;
  readonly #stub: RpcSurface;

  constructor(scope: string, stub: RpcSurface) {
    this.#scope = scope;
    this.#stub = stub;
  }

  get(_id: string): RpcSurface {
    return this.#stub;
  }

  /** Pure arithmetic over the name in the real thing — but it is a method ON the binding, so it
   * needs the binding as its receiver. */
  idFromName(name: string): string {
    return `${this.#scope}:${name}`;
  }
}

function harness(mutate?: (dials: FaultDials) => void) {
  const reached: string[] = [];
  const stub = rpcStub(reached);
  const namespace = new CoordinatorNamespace("id", stub);
  const dials = noFaults();
  mutate?.(dials);
  const stats = emptyBusStats();
  return {
    coordinator: faultableCoordinator(namespace, dials, stats),
    dials,
    reached,
    stats,
  };
}

describe("faultableCoordinator — the namespace's own members", () => {
  it("passes a non-`get` member through BOUND TO THE NAMESPACE", () => {
    // THE TOOTH FOR THE NAMESPACE PROXY'S `.bind(target)`. `idFromName` is not `get`, so the
    // wrapper hands back the binding's own method — bound. Hand it back raw and it is invoked with
    // the Proxy as its receiver, and `#scope`'s brand check refuses it, exactly as a host object's
    // internal slots would. The fixture this replaces returned a template literal that touched no
    // state at all, so the bind could be deleted with every test still green.
    const { coordinator } = harness();
    expect(coordinator.idFromName("demo")).toBe("id:demo");
  });

  it("keeps a namespace member working after it is DETACHED", () => {
    const { coordinator } = harness();
    // oxlint-disable-next-line typescript/unbound-method -- DETACHING IS THE SUBJECT: this test proves the namespace hands back a method already bound to the binding, so detaching it is safe. Obeying the rule here would delete the tooth.
    const { idFromName } = coordinator;
    expect(idFromName("demo")).toBe("id:demo");
  });
});

describe("faultableCoordinator — healthy passthrough", () => {
  it("dials through to the real stub and counts it", async () => {
    const { coordinator, reached, stats } = harness();
    const stub = coordinator.get(coordinator.idFromName("demo"));
    await expect(
      on(stub, "fetch")("https://coordinator/bus")
    ).resolves.toStrictEqual({
      status: 101,
    });
    expect(reached).toStrictEqual(["fetch:https://coordinator/bus"]);
    expect(stats).toStrictEqual({ dialsPassed: 1, dialsRefused: 0 });
  });

  it("leaves the stub's synthesised RPC surface completely alone", async () => {
    // The wrapper's whole contract beyond `fetch`: be invisible. If it forwards itself as the
    // receiver, or reaches for `.bind` on a synthesised method, the stub raises and the demo has
    // broken the registry path it came to watch.
    const { coordinator, reached } = harness();
    const stub = coordinator.get("id:demo");
    await expect(on(stub, "check")(["t0"])).resolves.toBe("rpc-result:check");
    await expect(on(stub, "expire")(["t0"])).resolves.toBe("rpc-result:expire");
    expect(reached).toStrictEqual(["rpc:check(t0)", "rpc:expire(t0)"]);
  });
});

describe("faultableCoordinator — dialRefused", () => {
  it("refuses the dial, counts it, and never reaches the stub", async () => {
    const { coordinator, reached, stats } = harness((dials) => {
      dials.bus.dialRefused = true;
    });
    const stub = coordinator.get("id:demo");
    await expect(on(stub, "fetch")("https://coordinator/bus")).rejects.toThrow(
      /the socket did not open/u
    );
    expect(reached).toStrictEqual([]);
    expect(stats).toStrictEqual({ dialsPassed: 0, dialsRefused: 1 });
  });

  it("does NOT refuse the registry's RPC — the outage is modelled from the reader's seat", async () => {
    // Deliberate, and the scene depends on it: a refused dial and an unreadable mirror are what a
    // READER observes when the Coordinator is gone. The live registry check surviving is the price
    // the design says gets paid under uncertainty, and the demo's job is to show it being paid.
    const { coordinator } = harness((dials) => {
      dials.bus.dialRefused = true;
    });
    const stub = coordinator.get("id:demo");
    await expect(on(stub, "check")(["t0"])).resolves.toBe("rpc-result:check");
  });

  it("stops refusing once the dial is turned back off", async () => {
    const { coordinator, dials, stats } = harness((d) => {
      d.bus.dialRefused = true;
    });
    const stub = coordinator.get("id:demo");
    await expect(on(stub, "fetch")("u")).rejects.toThrow(
      /the socket did not open/u
    );
    dials.bus.dialRefused = false;
    await expect(on(stub, "fetch")("u")).resolves.toStrictEqual({
      status: 101,
    });
    expect(stats).toStrictEqual({ dialsPassed: 1, dialsRefused: 1 });
  });
});
