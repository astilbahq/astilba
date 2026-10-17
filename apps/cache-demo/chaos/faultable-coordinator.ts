// The demo's Coordinator interposition — dial loss.
//
// A wrapper around the REAL `COORDINATOR` Durable Object namespace whose only fault is that the
// WebSocket upgrade does not connect. That is the whole vocabulary on purpose: the bus transport
// has exactly one loss mode. It does not drop frames, it does not corrupt them, it does not deliver
// them late out of malice — the socket is open, or it is closed and the transport says so. A dial
// wrapper offering "drop one frame" would be injecting a fault the real system has no way to
// produce, and the demo would be narrating fiction.
//
// The frame-level chaos that DOES exist — dropped frames, reordering, delay — lives in the
// deterministic fakes, where a simulated clock makes it meaningful and where the invariant suite
// already holds it to account. It is not portable out here and this demo does not pretend it is:
// out here the honest fault is that the socket will not open, and what the reader does about it is
// what there is to watch.
//
// A PROXY, for the same reason the KV wrapper is one: a Durable Object stub's RPC surface is
// generated from the class's own type, so it cannot be hand-implemented — and everything this file
// does not intercept must be the real thing, not an approximation of it. Only the stub's `fetch`
// (the dial) is intercepted; every RPC method, and every other namespace member, passes through.
//
// WHAT THIS WRAPPER DELIBERATELY DOES NOT DO: it does not refuse the registry's RPC calls. A
// Coordinator outage is modelled from the READER's seat — the socket will not open, and the mirror
// keys stop being readable (that half lives on the KV wrapper) — which is what a reader observes
// when the Coordinator is gone. Refusing `check` as well would be a second, different fault wearing
// the same name, and the scene it belongs to is the one where the read path itself has to answer
// with no authority to ask. The reader going blind is the scene here; the live check surviving is
// not an oversight, it is the price the design says gets paid, and the demo's job is to show it
// getting paid rather than to arrange for it not to be.

import type { BusFaultStats, FaultDials } from "./dials";
import { asPlatformMethod } from "./platform-method";

/** The rejection a refused dial produces. The real shape of an unreachable Durable Object is a
 * rejected `fetch`, so that is the shape this raises — the redialing bus driver sees a failed dial
 * and applies its own backoff, which is the behaviour on camera. */
class DialRefusedError extends Error {
  constructor() {
    super("chaos: Coordinator dial refused — the socket did not open");
    this.name = "DialRefusedError";
  }
}

/** Wrap one Durable Object stub so its `fetch` (and only its `fetch`) can be refused. */
function faultableStub<T extends object>(
  real: T,
  dials: FaultDials,
  stats: BusFaultStats
): T {
  return new Proxy(real, {
    // TOUCH NOTHING BUT `fetch`, and touch it as little as possible. A Durable Object stub is ITSELF
    // a proxy: its RPC methods do not exist as properties, they are SYNTHESISED on access. Two
    // consequences, both learned the hard way, and both of which had the demo injecting faults it
    // never meant to inject — into the exact path it exists to observe:
    //
    //   • forwarding this wrapper as the `Reflect.get` RECEIVER makes the stub synthesise against
    //     the wrong object, and every RPC call fails, reporting that the receiver does not implement
    //     the method. Hence the two-argument read below.
    //   • `typeof member === "function"` is true for EVERY property on a stub, so the usual
    //     passthrough reflex — hand back `member.bind(target)` — asks the stub to synthesise, and
    //     then remotely invoke, a method literally named `bind`. It fails with `The RPC receiver
    //     does not implement the method "bind"`. So nothing here is bound: a synthesised RPC method
    //     already carries its own target, and the correct handling of the stub's RPC surface is to
    //     return it untouched and stay out of the way.
    get(target, property) {
      const member: unknown = Reflect.get(target, property);
      if (property !== "fetch" || typeof member !== "function") {
        return member;
      }
      const dial = asPlatformMethod(member);
      return async (...args: readonly unknown[]): Promise<unknown> => {
        if (dials.bus.dialRefused) {
          stats.dialsRefused++;
          throw new DialRefusedError();
        }
        stats.dialsPassed++;
        return await Reflect.apply(dial, target, args);
      };
    },
  });
}

/**
 * Wrap the real `COORDINATOR` namespace so every stub it hands out can have its dial refused. The
 * returned value IS a `DurableObjectNamespace<Coordinator>` — the library receives it as the
 * `coordinator` of the hero five-liner and addresses it exactly as it would the binding itself.
 */
export function faultableCoordinator<T extends object>(
  real: T,
  dials: FaultDials,
  stats: BusFaultStats
): T {
  return new Proxy(real, {
    get(target, property) {
      const member: unknown = Reflect.get(target, property);
      if (property !== "get" || typeof member !== "function") {
        // Every OTHER member of the namespace: handed back BOUND TO THE NAMESPACE. A namespace is a
        // host object and its methods reach internal slots, so one invoked with this Proxy as its
        // receiver fails the brand check — `idFromName` is the one the library actually calls, and
        // the tooth next door drives it both attached and detached.
        //
        // Note this is the OPPOSITE of what the STUB's proxy must do below: a stub SYNTHESISES its
        // RPC surface, so asking it for `.bind` asks the far side for a method by that name. Two
        // proxies, two rules, and getting one right proves nothing about the other.
        return typeof member === "function"
          ? asPlatformMethod(member).bind(target)
          : member;
      }
      const get = asPlatformMethod(member);
      return (...args: readonly unknown[]): unknown => {
        const stub: unknown = Reflect.apply(get, target, args);
        // Every stub the namespace hands out is wrapped on its way past, so the dial fault applies
        // to whichever one the library actually dials.
        return typeof stub === "object" && stub !== null
          ? faultableStub(stub, dials, stats)
          : stub;
      };
    },
  });
}
