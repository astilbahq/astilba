// FaultableKV — the demo's `KVNamespace` interposition.
//
// A wrapper around the REAL KV binding that implements the platform's own interface and can be told
// to reject reads, reject writes, or make one keyspace unreadable. The library sees a
// `KVNamespace` and nothing else; it cannot tell this apart from a bad afternoon at the datacentre,
// which is the entire point — the cache's behaviour under fault has to be the cache's behaviour
// under fault, not a special mode it entered because we asked it to.
//
// A PROXY, NOT A REIMPLEMENTATION, and deliberately: `KVNamespace` is thirty-odd overloads deep, and
// a hand-written stand-in would be a second implementation of the platform's surface that drifts
// from it silently — the demo would end up demonstrating the wrapper's bugs. Everything this file
// does not name is LITERALLY the real binding's own method, reached through it. Only `get`,
// `getWithMetadata`, `put` and `delete` are intercepted; every other member passes through
// untouched.
//
// FAULTS ARE RAISED THE WAY KV RAISES THEM — a rejected promise carrying an `Error`. The driver
// classifies a rejected write into the structural failure contract by inspecting the
// rejection, so a fault that rejected with something else would be exercising a classification path
// the real binding never produces. `throttled` is the one shape worth naming explicitly (KV's
// 1 write/s/key ceiling arrives as a 429), so `failWrites` rejects with a plain unavailability by
// default and the throttle shape stays a scene's explicit choice.

import type { FaultDials, KvFaultStats } from "./dials";
import type { PlatformMethod } from "./platform-method";
import { asPlatformMethod } from "./platform-method";

/** The KV methods this wrapper intercepts. Anything else on the binding passes straight through. */
const READ_METHODS: ReadonlySet<string> = new Set(["get", "getWithMetadata"]);
const WRITE_METHODS: ReadonlySet<string> = new Set(["put", "delete"]);

/** The rejection a faulted KV operation produces — the shape the real binding rejects with. */
class KvFaultError extends Error {
  constructor(operation: string, reason: string) {
    super(`chaos: KV ${operation} rejected — ${reason}`);
    this.name = "KvFaultError";
  }
}

/** Does this call target the unreadable keyspace? Only the FIRST argument is inspected, and only
 * when it is a string: KV's batch-read overloads take an array, which this demo never drives. */
function targetsUnreadablePrefix(
  args: readonly unknown[],
  prefix: string | null
): boolean {
  if (prefix === null) {
    return false;
  }
  const [key] = args;
  return typeof key === "string" && key.startsWith(prefix);
}

/**
 * Wrap a real `KVNamespace` so the demo's dials can fault it. At the call site `T` infers as
 * `KVNamespace`, so what the hero five-liner receives as its `kv` IS a `KVNamespace` and it composes
 * its L2 over this exactly as it would over the binding itself.
 *
 * GENERIC, and for a reason worth defending rather than apologising for. `KVNamespace` is thirty-odd
 * overloads deep, and nothing that is not the real binding can satisfy it — a hand-built stand-in
 * fails to typecheck against the `arrayBuffer` and `stream` overloads alone. Pinning the parameter
 * to `KVNamespace` would therefore have made this wrapper unTESTABLE except by casting something
 * into a lie, and this wrapper is the last thing in the app that should go untested: it is a Proxy
 * over a platform object, which is precisely the kind of code that fails in ways review does not
 * catch and a test does. Inference gives the call site the exact same type it would have had.
 */
export function faultableKv<T extends object>(
  real: T,
  dials: FaultDials,
  stats: KvFaultStats
): T {
  return new Proxy(real, {
    get(target, property) {
      const member: unknown = Reflect.get(target, property);
      if (typeof property !== "string" || typeof member !== "function") {
        return member;
      }
      const method: PlatformMethod = asPlatformMethod(member);
      const isRead = READ_METHODS.has(property);
      const isWrite = WRITE_METHODS.has(property);
      if (!(isRead || isWrite)) {
        // Not a faultable operation — hand back the binding's own method, BOUND TO THE BINDING. The
        // bind is load-bearing: a host object's methods reach internal slots, so one invoked with
        // this Proxy as its receiver fails the brand check. Both directions are toothed next door.
        return method.bind(target);
      }
      return async (...args: readonly unknown[]): Promise<unknown> => {
        if (isRead) {
          if (dials.kv.failReads) {
            stats.readsFailed++;
            throw new KvFaultError(property, "reads are failing");
          }
          if (targetsUnreadablePrefix(args, dials.kv.unreadablePrefix)) {
            stats.readsFailed++;
            throw new KvFaultError(property, "keyspace is unreadable");
          }
          stats.readsPassed++;
        }
        if (isWrite) {
          if (dials.kv.failWrites) {
            stats.writesFailed++;
            throw new KvFaultError(property, "writes are failing");
          }
          stats.writesPassed++;
        }
        return await Reflect.apply(method, target, args);
      };
    },
  });
}
