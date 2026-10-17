// The identity=address scenes — the REAL Coordinator DO on
// workerd/Miniflare. A Coordinator's regId derives from its OWN Durable Object name (the string
// clients address it by), journaled at first boot and NEVER re-journaled. This lane exercises the
// three chartered behaviours end-to-end:
//   • first-boot journals the winner (the name) + writes land under `__reg:<name>:*` (R3);
//   • the `newUniqueId` fallback — an un-named object falls to env `REGISTRY_ID`, then "default" (R3);
//   • the DIVERGENCE scene — a DO whose journaled (env-seeded, un-named first boot) regId ≠ its later
//     name KEEPS the journaled identity: `regid_divergence` is recorded, writes STAY under the
//     journaled scope, the `hello` echoes the journaled id (a name-configured client closes loud),
//     and the orphaning mutation (routing regId back through the cadence reconcile ⇒ a rekey) reds it.
//
// Lane law: timing-tolerant. Message delivery over a real socket is async, so the hello
// scenes POLL to a bounded predicate; the DO flush is fired deterministically (`runDurableObjectAlarm`).

import {
  evictDurableObject,
  runDurableObjectAlarm,
  runInDurableObject,
} from "cloudflare:test";
import { env } from "cloudflare:workers";

import type { BusEvent } from "@astilba/cache";
import { mirrorKeys } from "@astilba/cache/registry";
import { describe, expect, it } from "vitest";

import type { ClientSocket } from "../../src/adapters/cloudflare/bus-client";
import { doBus } from "../../src/adapters/cloudflare/bus-client";
import type { RegIdDivergence } from "../../src/adapters/cloudflare/coordinator";
import type { Coordinator } from "./worker";

const A = "reader:a";

type CoordinatorStub = DurableObjectStub<Coordinator>;

/** The seeded registry identity durably recorded at first boot, read from inside the DO. */
async function seededRegId(stub: CoordinatorStub): Promise<string | undefined> {
  return await runInDurableObject(stub, (_instance, state) =>
    state.storage.get<string>("regId")
  );
}

/** The DO's boot-time regId divergence, read from inside the DO after forcing a `#load`. */
async function divergenceOf(
  stub: CoordinatorStub
): Promise<RegIdDivergence | null> {
  // Read the divergence the LIVE instance computed during its normal-dispatch boot (the caller
  // drives a mutate/flush first). We do NOT force a re-`#load` here: `runInDurableObject` runs the
  // callback against an instance whose `this.ctx.id.name` is unpopulated (the `state` param carries
  // the name, the instance's own `ctx` does not), so a forced re-derivation would wrongly resolve
  // to the un-named fallback. DO NOT "simplify" this to a forced re-`#load` inside the callback — it
  // would read the fallback id and manufacture a FALSE divergence (the Miniflare finding-2 trap).
  // Explicit type args: `runInDurableObject` cannot infer `O` back out of
  // `DurableObjectStub<O>`, so pin the instance to `Coordinator`.
  return await runInDurableObject<Coordinator, RegIdDivergence | null>(
    stub,
    (instance) => instance.regIdDivergence
  );
}

/** The DO's own name as the runtime sees it (the identity source), read from inside the DO. */
async function idName(stub: CoordinatorStub): Promise<string | undefined> {
  return await runInDurableObject(stub, (_instance, state) => state.id.name);
}

/** Drive one durable purge → flush so a fresh `__delta`/`__state` write lands under the live regId. */
async function mutateAndFlush(stub: CoordinatorStub): Promise<void> {
  await stub.delete([A]);
  expect(await runDurableObjectAlarm(stub)).toBe(true);
}

/** Open a WebSocket to the Coordinator DO and accept it (the client side). */
async function dialCoordinator(stub: CoordinatorStub): Promise<ClientSocket> {
  const response = await stub.fetch("https://coordinator/bus", {
    headers: { Upgrade: "websocket" },
  });
  const socket = response.webSocket;
  if (!socket) {
    throw new Error(`expected a 101 webSocket, got status ${response.status}`);
  }
  socket.accept();
  return socket;
}

/** Poll a predicate to true, bounded (timing-tolerant lane). */
async function waitFor(
  predicate: () => boolean,
  label: string
): Promise<void> {
  for (let i = 0; i < 200 && !predicate(); i++) {
    await new Promise((resolve) => {
      setTimeout(resolve, 5);
    });
  }
  if (!predicate()) {
    throw new Error(`waitFor timed out: ${label}`);
  }
}

describe("registry identity = address on workerd (§4.2, E19-b)", () => {
  it("platform trap (R3): idFromString drops the name; newUniqueId has no name", () => {
    // The divergence scene RELIES on these platform facts — assert them explicitly so a Miniflare
    // change would surface as a named failure here, not as a mysterious downstream one.
    const named = env.COORDINATOR.idFromName("trap-probe");
    expect(named.name).toBe("trap-probe");
    const viaString = env.COORDINATOR.idFromString(named.toString());
    expect(viaString.name).toBeUndefined(); // the name is dropped…
    expect(viaString.toString()).toBe(named.toString()); // …but it is the SAME id (shared storage)
    expect(env.COORDINATOR.newUniqueId().name).toBeUndefined();
  });

  it("(R3) first boot journals the WINNER (the DO name) + writes land under __reg:<name>:*", async () => {
    const NAME = "rid-firstboot";
    const stub = env.COORDINATOR.get(env.COORDINATOR.idFromName(NAME));
    await mutateAndFlush(stub);

    // The name is the seeded identity — journaled once, no divergence, and it is what the runtime
    // reports as the DO's own name (identity = address).
    expect(await seededRegId(stub)).toBe(NAME);
    expect(await divergenceOf(stub)).toBeNull();
    expect(await idName(stub)).toBe(NAME);
    // The mirror is scoped to the name; no stray "default" pointer was minted.
    expect(await env.REGISTRY_KV.get(mirrorKeys(NAME).stateKey)).not.toBeNull();
    expect(
      await env.REGISTRY_KV.get(mirrorKeys("default").stateKey)
    ).toBeNull();
  });

  it("(R3) newUniqueId fallback: an un-named object falls to env REGISTRY_ID, then default", async () => {
    // (a) un-named + env set ⇒ the env value is the DEMOTED fallback winner.
    const ENVID = "fallback-envid";
    Reflect.set(env, "REGISTRY_ID", ENVID);
    try {
      const uid = env.COORDINATOR.newUniqueId();
      const stub = env.COORDINATOR.get(uid);
      await mutateAndFlush(stub);
      expect(await idName(stub)).toBeUndefined(); // un-named object
      expect(await seededRegId(stub)).toBe(ENVID);
      expect(await divergenceOf(stub)).toBeNull();
      expect(
        await env.REGISTRY_KV.get(mirrorKeys(ENVID).stateKey)
      ).not.toBeNull();
    } finally {
      Reflect.deleteProperty(env, "REGISTRY_ID");
    }

    // (b) un-named + env UNSET ⇒ "default" (zero-config last resort).
    const uid2 = env.COORDINATOR.newUniqueId();
    const stub2 = env.COORDINATOR.get(uid2);
    await mutateAndFlush(stub2);
    expect(await seededRegId(stub2)).toBe("default");
    expect(
      await env.REGISTRY_KV.get(mirrorKeys("default").stateKey)
    ).not.toBeNull();
  });

  it("(R1) DIVERGENCE: journaled regId wins, writes stay scoped, hello echoes journaled, wrong-scope client closes loud", async () => {
    // A DO whose journaled identity ≠ its currently-derived one. Miniflare finding: `ctx.id.name` is
    // fixed at DO CREATION and is stable across later re-addressing (a DO minted via `idFromName`
    // keeps its name even when reached via `idFromString`; a nameless DO stays nameless) — so the
    // production `idFromString` name-drop trap (R3) is NOT reproducible by re-addressing here. The
    // constructible divergence exercises the SAME detector branch (derived ≠ journaled ⇒ keep
    // journaled) via an env change across boots of a NAMELESS DO: the un-named derivation reads
    // `env.REGISTRY_ID`, so changing it between boots makes the later derivation diverge.
    const JOURNALED = "rid-diverge-x";
    const DERIVED = "rid-diverge-y";
    const uid = env.COORDINATOR.newUniqueId(); // nameless, stable across both boots

    try {
      // FIRST BOOT with env = JOURNALED ⇒ the nameless DO journals JOURNALED, no divergence.
      Reflect.set(env, "REGISTRY_ID", JOURNALED);
      const stub1 = env.COORDINATOR.get(uid);
      await mutateAndFlush(stub1);
      expect(await seededRegId(stub1)).toBe(JOURNALED);
      expect(await divergenceOf(stub1)).toBeNull();

      // Evict, flip env to DERIVED, and reach the SAME nameless DO ⇒ a fresh instance whose
      // `#deriveRegId` now resolves to DERIVED ≠ the journaled JOURNALED.
      await evictDurableObject(stub1);
      Reflect.set(env, "REGISTRY_ID", DERIVED);
      const stub2 = env.COORDINATOR.get(uid);
      await mutateAndFlush(stub2);

      // The divergence is recorded; the journaled identity WINS.
      expect(await divergenceOf(stub2)).toStrictEqual({
        journaled: JOURNALED,
        name: DERIVED,
      });
      // Writes STAY under the journaled scope — the fresh delta (seq 2) + pointer landed under
      // JOURNALED, NEVER rekeyed to DERIVED (the orphaning defect R1 kills; blinding the detector
      // into the cadence reconcile would rekey here — writing `__reg:rid-diverge-y:*` and nulling
      // the divergence — and red this block).
      expect(
        await env.REGISTRY_KV.get(mirrorKeys(JOURNALED).stateKey)
      ).not.toBeNull();
      expect(
        await env.REGISTRY_KV.get(mirrorKeys(JOURNALED).deltaKey(2))
      ).not.toBeNull();
      expect(
        await env.REGISTRY_KV.get(mirrorKeys(DERIVED).stateKey)
      ).toBeNull();

      // The `hello` echoes the JOURNALED id: a client configured with DERIVED (the later boot's wrong
      // scope) reads a foreign channel and CLOSES loud (scope mismatch); a client configured with
      // the journaled id converges.
      let wrongScopeClosed = false;
      const busWrong = doBus(() => dialCoordinator(stub2), {
        onClose: (): void => {
          wrongScopeClosed = true;
        },
        regId: DERIVED,
      });
      const subWrong = busWrong.subscribe(() => {
        /* dispatch nothing — we only watch for the loud close */
      });
      await waitFor(
        () => wrongScopeClosed,
        "wrong-scope client closes loud on scope mismatch"
      );
      subWrong.close();

      const journaledEvents: BusEvent[] = [];
      let journaledClientClosed = false;
      const busJournaled = doBus(() => dialCoordinator(stub2), {
        onClose: (): void => {
          journaledClientClosed = true;
        },
        regId: JOURNALED,
      });
      const subJournaled = busJournaled.subscribe((event) => {
        journaledEvents.push(event);
      });
      await waitFor(
        () => journaledEvents.some((e) => e.kind === "hello"),
        "journaled-configured client receives the hello"
      );
      expect(journaledClientClosed).toBe(false); // scope matched — did NOT close
      subJournaled.close();
    } finally {
      Reflect.deleteProperty(env, "REGISTRY_ID");
    }
  });
});
