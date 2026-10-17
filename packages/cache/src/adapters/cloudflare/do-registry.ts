// @astilba/cache adapter — the `doRegistry` client (phase 2).
//
// PLATFORM: `src/adapters/**` platform boundary. The `Registry`
// implementation the kernel talks to when the authoritative Coordinator is a Cloudflare
// Durable Object. It is a THIN RPC forwarder: `check`/`expire`/`delete`/
// `registerRetention` round-trip to the Coordinator DO stub, which hosts the pure
// registry machine. All semantics live in the DO + the machine; this only marshals the
// call. This is the implementation the Registry conformance contract runs against on
// real workerd (the conformance dividend).

import type { Registry, RegistryAck } from "../../drivers";
import type { TagKnowledge } from "../../knowledge";
import { DEFAULT_REG_ID } from "../../registry";
import type { Coordinator } from "./coordinator";

/**
 * Where a Coordinator stub comes from (widened by ADDITIVELY, in the warrant).
 *
 * A STUB is a client-held I/O handle: it belongs to the request context that minted it, and the
 * platform refuses it anywhere else. A long-lived cache therefore cannot hold one — but the API defines
 * `doRegistry(stub, regId)`, and a caller who mints and uses inside ONE request (the explicit
 * composition, as written) is doing nothing wrong. So the parameter widens instead of changing: a
 * bare stub stays exactly as valid as it was, and a THUNK lets a caller that outlives a request hand
 * over the ADDRESS — the recipe for a stub — rather than a stub. The thunk is invoked per use, so
 * whichever request is running gets a handle of its own.
 */
export type StubSource =
  | DurableObjectStub<Coordinator>
  | (() => DurableObjectStub<Coordinator>);

/** A `Registry` backed by a Coordinator Durable Object, reached over its RPC stub. */
class DoRegistry implements Registry {
  readonly #source: StubSource;

  /** The mirror-key scope the reader resyncs under. Defaults to `"default"`; it MUST equal the
   * Coordinator's regId so the client-side reader addresses the same keyspace the DO writes. Under
   * that regId IS the Coordinator's Durable Object NAME (identity = address) — so a caller who
   * addresses the DO via `idFromName(name)` passes that SAME `name` here. (C4 ledger: the
   * request-piggyback golden path should take ONE registry `name` and derive BOTH the DO stub —
   * `env.COORDINATOR.get(idFromName(name))` — AND this `regId` from it, so the address and the scope
   * cannot drift.) */
  readonly regId: string;

  constructor(source: StubSource, regId: string) {
    this.#source = source;
    this.regId = regId;
  }

  /** The stub for THIS call. A thunk source is invoked per use — never memoized, which is the
   * entire point: a cached stub is a stub minted in some earlier request, and the platform refuses
   * it. Minting is cheap (it is address arithmetic, not a connection), so there is nothing to save
   * by holding one and a working cache to lose. A bare stub source is returned as-is: the caller
   * chose to own the lifetime, and the signature says they may. */
  get #stub(): DurableObjectStub<Coordinator> {
    return typeof this.#source === "function" ? this.#source() : this.#source;
  }

  async check(tags: readonly string[]): Promise<readonly TagKnowledge[]> {
    return await this.#stub.check(tags);
  }

  async expire(tags: readonly string[]): Promise<RegistryAck> {
    return await this.#stub.expire(tags);
  }

  async delete(tags: readonly string[]): Promise<RegistryAck> {
    return await this.#stub.delete(tags);
  }

  async registerRetention(retentionMs: number): Promise<void> {
    await this.#stub.registerRetention(retentionMs);
  }
}

/** Build a `Registry` from a Coordinator DO stub OR a thunk that mints one (`StubSource` —
 * the widening is additive, so `doRegistry(stub, regId)` remains exactly as signed). `regId`
 * must match the Coordinator's regId — which is its Durable Object NAME
 * (identity = address) — so the client-side reader resyncs the same scoped keyspace the DO writes;
 * defaults to `"default"` for single-registry (zero-config) deployments.
 *
 * Pass a THUNK from anything that outlives a request (`createWorkersCache` does); pass a bare stub
 * when the caller mints and uses within one. */
export function doRegistry(
  source: StubSource,
  regId = DEFAULT_REG_ID
): Registry {
  return new DoRegistry(source, regId);
}
