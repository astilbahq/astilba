// The KV wrapper's own tests (C5-c) — the demo's machinery, never the kernel's semantics.
//
// What is on trial here is the wrapper: does the dial actually reach the binding, does an unfaulted
// call pass through untouched, and do the counters count. Nothing here asserts anything about the
// cache — the cache's behaviour is the invariant suite's business, and re-testing it through a demo
// fixture would be worse coverage of the same thing.
//
// The counters are the reason this file matters more than it looks. A scene whose fault intercepted
// nothing renders, on screen, exactly like a system that shrugged the fault off — so the difference
// between "the demo proved something" and "the demo proved nothing" is a number that has to be
// right.

import { describe, expect, it } from "vitest";

import type { FaultDials } from "./dials";
import { emptyKvStats, noFaults } from "./dials";
import { faultableKv } from "./faultable-kv";

/**
 * A stand-in for the binding: records what reached it, and answers. This is NOT a `KVNamespace`
 * (nothing that is not the real binding can be one — its overloads see to that), which is exactly
 * why the wrapper is generic. What it has to be is an object whose methods the Proxy can intercept,
 * and it is.
 *
 * IT KEEPS ITS STATE IN A PRIVATE FIELD, AND THAT IS THE POINT RATHER THAN A STYLE CHOICE. A real
 * KV binding is a host object whose methods reach internal slots, so calling one with the Proxy as
 * its receiver fails — the proxy has no such slots. `#reached` reproduces exactly that: a private
 * field is brand-checked against the real instance, and a Proxy fails the brand check. So a method
 * handed back UNBOUND, then invoked with the proxy as `this`, throws here for the same reason it
 * throws against the platform.
 *
 * The fixture that came before this one stored `reached` as an ordinary property. Every method
 * worked perfectly with the proxy as its receiver, so the wrapper's `.bind(target)` passthrough was
 * pure decoration as far as any test could tell — it could be deleted with the suite green. The
 * private field is what gives the passthrough a jurisdiction a tooth can bite.
 */
class BackingKv {
  readonly #reached: string[] = [];

  /** What actually reached the binding. A getter, so the tests can read it through the wrapper's
   * own passthrough — which is itself part of what is on trial. */
  get reached(): readonly string[] {
    return this.#reached;
  }

  async delete(key: string): Promise<void> {
    this.#reached.push(`delete:${key}`);
    await Promise.resolve();
  }

  async get(key: string): Promise<string> {
    this.#reached.push(`get:${key}`);
    return await Promise.resolve(`value-of:${key}`);
  }

  async getWithMetadata(
    key: string
  ): Promise<{ metadata: null; value: string }> {
    this.#reached.push(`getWithMetadata:${key}`);
    return await Promise.resolve({ metadata: null, value: `value-of:${key}` });
  }

  /** A method the wrapper does NOT fault — the passthrough's whole subject. It touches `#reached`,
   * so it can only run with the REAL binding as its receiver. */
  async list(): Promise<{ keys: readonly string[] }> {
    this.#reached.push("list");
    return await Promise.resolve({ keys: [] });
  }

  async put(key: string, value: string): Promise<void> {
    this.#reached.push(`put:${key}=${value}`);
    await Promise.resolve();
  }
}

function backingKv() {
  return new BackingKv();
}

function harness(mutate?: (dials: FaultDials) => void) {
  const backing = backingKv();
  const dials = noFaults();
  mutate?.(dials);
  const stats = emptyKvStats();
  return { backing, dials, kv: faultableKv(backing, dials, stats), stats };
}

describe("faultableKv — healthy passthrough", () => {
  it("reaches the real binding and returns its answer untouched", async () => {
    const { backing, kv, stats } = harness();
    await expect(kv.get("k")).resolves.toBe("value-of:k");
    await kv.put("k", "v");
    expect(backing.reached).toStrictEqual(["get:k", "put:k=v"]);
    expect(stats).toStrictEqual({
      readsFailed: 0,
      readsPassed: 1,
      writesFailed: 0,
      writesPassed: 1,
    });
  });

  it("passes an unfaulted method through BOUND TO THE BINDING, not to the wrapper", async () => {
    // THE TOOTH FOR THE `.bind(target)` PASSTHROUGH. `list` is not a faultable operation, so the
    // wrapper hands back the binding's own method — and it must hand it back bound to the BINDING.
    // Return it raw and it is invoked with the Proxy as its receiver, which on a real host object
    // means reaching for internal slots the proxy does not have. `#reached` reproduces that brand
    // check exactly, so deleting the `.bind(target)` turns this call into a TypeError.
    //
    // The test this replaces called `kv.list()` against a fixture whose state was an ordinary
    // property. That works with ANY receiver, so it passed with the bind deleted — a test named for
    // binding that never once depended on it.
    const { backing, kv } = harness();
    await expect(kv.list()).resolves.toStrictEqual({ keys: [] });
    expect(backing.reached).toStrictEqual(["list"]);
  });

  it("keeps an unfaulted method working after it is DETACHED from the wrapper", async () => {
    // The same branch from the other side, and the shape a caller actually reaches for: pull the
    // method off and call it later. Bound, it still finds its binding; unbound, `this` is undefined
    // and it cannot.
    const { backing, kv } = harness();
    // oxlint-disable-next-line typescript/unbound-method -- DETACHING IS THE SUBJECT. The rule warns that pulling a method off an object loses its `this`; this test exists precisely to prove the wrapper hands back a method that has already been bound to the binding, so detaching it is safe. Obeying the rule here would delete the tooth.
    const { list } = kv;
    await expect(list()).resolves.toStrictEqual({ keys: [] });
    expect(backing.reached).toStrictEqual(["list"]);
  });
});

describe("faultableKv — failReads", () => {
  it("rejects the read, counts it, and never reaches the binding", async () => {
    const { backing, kv, stats } = harness((dials) => {
      dials.kv.failReads = true;
    });
    await expect(kv.getWithMetadata("k")).rejects.toThrow(/reads are failing/u);
    // The point of the fault is that the binding never saw the call.
    expect(backing.reached).toStrictEqual([]);
    expect(stats.readsFailed).toBe(1);
    expect(stats.readsPassed).toBe(0);
  });

  it("leaves writes alone", async () => {
    const { backing, kv, stats } = harness((dials) => {
      dials.kv.failReads = true;
    });
    await kv.put("k", "v");
    expect(backing.reached).toStrictEqual(["put:k=v"]);
    expect(stats.writesPassed).toBe(1);
  });
});

describe("faultableKv — failWrites", () => {
  it("rejects the write, counts it, and never reaches the binding", async () => {
    const { backing, kv, stats } = harness((dials) => {
      dials.kv.failWrites = true;
    });
    await expect(kv.put("k", "v")).rejects.toThrow(/writes are failing/u);
    expect(backing.reached).toStrictEqual([]);
    expect(stats.writesFailed).toBe(1);
  });

  it("leaves reads alone", async () => {
    const { kv, stats } = harness((dials) => {
      dials.kv.failWrites = true;
    });
    await expect(kv.get("k")).resolves.toBe("value-of:k");
    expect(stats.readsPassed).toBe(1);
  });
});

describe("faultableKv — unreadablePrefix", () => {
  it("rejects only reads under the prefix, and passes every other key through", async () => {
    const { backing, kv, stats } = harness((dials) => {
      dials.kv.unreadablePrefix = "__reg:demo:";
    });
    await expect(kv.get("__reg:demo:state")).rejects.toThrow(
      /keyspace is unreadable/u
    );
    await expect(kv.get("demo:headline")).resolves.toBe(
      "value-of:demo:headline"
    );
    // THE distinction the registry-outage scene is built on: the entry is readable, the mirror is
    // not. A prefix that faulted everything would make it the backend-kill scene wearing a
    // different name.
    expect(backing.reached).toStrictEqual(["get:demo:headline"]);
    expect(stats.readsFailed).toBe(1);
    expect(stats.readsPassed).toBe(1);
  });

  it("does not fault writes under the prefix — an outage is what a READER observes", async () => {
    const { kv, stats } = harness((dials) => {
      dials.kv.unreadablePrefix = "__reg:demo:";
    });
    await kv.put("__reg:demo:state", "v");
    expect(stats.writesPassed).toBe(1);
  });
});

describe("faultableKv — healing", () => {
  it("stops faulting once the dial is turned back off", async () => {
    const { kv, dials, stats } = harness((d) => {
      d.kv.failReads = true;
    });
    await expect(kv.get("k")).rejects.toThrow(/reads are failing/u);
    dials.kv.failReads = false;
    await expect(kv.get("k")).resolves.toBe("value-of:k");
    expect(stats.readsFailed).toBe(1);
    expect(stats.readsPassed).toBe(1);
  });
});
