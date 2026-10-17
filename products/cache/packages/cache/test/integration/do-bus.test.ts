// The doBus live-channel exit scenes (rulings) — the REAL
// Coordinator DO WebSocket endpoint + the REAL doBus client, end-to-end on workerd/Miniflare. The
// DO accepts hibernatable sockets, runs the reset→hello handshake (head = its OWN last-flushed
// epoch), and fans each flush frame out statelessly; the client dials, decodes, dispatches, and
// reports its close (no self-reconnect — the test redials explicitly).
//
// Lane law: this lane is timing-tolerant. Message delivery over a real socket is async,
// so the scenes POLL to a convergence predicate (bounded) rather than asserting synchronously.
//
// Scene coverage: (i) connect → reset→hello at the DO's flushed head; (ii) mutate →
// flush → frame delivered live → the reader advances; (iii) evict{close} mid-subscription → the
// client observes the close → redial → fresh handshake → KV resync converges; (iv) hibernation
// wake — evict{hibernate}, then a flush wakes the DO and fans out to the still-open socket; (v) two
// subscribers, one killed socket does not stall the other. The send-throw close-on-doubt MICRO-path
// is the deterministic conformance proof (Miniflare will not throw on send); the WS-per-DO
// concurrency ceiling is a deployed probe (docs say "low thousands, then shard").

import {
  evictDurableObject,
  runDurableObjectAlarm,
  runInDurableObject,
} from "cloudflare:test";
import { env } from "cloudflare:workers";

import type { BusEvent, Envelope } from "@astilba/cache";
import type { MirrorKeys } from "@astilba/cache/registry";
import { mirrorKeys } from "@astilba/cache/registry";
import { describe, expect, it } from "vitest";

import type { ClientSocket } from "../../src/adapters/cloudflare/bus-client";
import { doBus } from "../../src/adapters/cloudflare/bus-client";
import { doRegistry } from "../../src/adapters/cloudflare/do-registry";
import { cloudflareKV } from "../../src/adapters/cloudflare/kv";
import { Invalidation } from "../../src/invalidation";

const A = "reader:a";

/** A UNIQUE regId per test partitions the shared KV namespace into `__reg:<id>:…`.
 * The regId IS the DO name (`idFromName(regId)`, identity = address), so no shared
 * global is written — the DO derives the SAME id from its name; the client + reader use it too. */
function scope(regId: string): MirrorKeys {
  return mirrorKeys(regId);
}

/** Open a WebSocket to the Coordinator DO and accept it (the client side). */
async function dialCoordinator(
  stub: DurableObjectStub
): Promise<ClientSocket> {
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

/** A raw doBus subscription that logs every BusEvent + tracks whether the socket closed. */
interface Collector {
  readonly events: BusEvent[];
  closed: () => boolean;
  close: () => void;
}

function connect(stub: DurableObjectStub, regId: string): Collector {
  const events: BusEvent[] = [];
  let closed = false;
  const bus = doBus(() => dialCoordinator(stub), {
    onClose: (): void => {
      closed = true;
    },
    regId,
  });
  const sub = bus.subscribe((event) => {
    events.push(event);
  });
  return {
    close: (): void => {
      sub.close();
    },
    closed: (): boolean => closed,
    events,
  };
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

/** The `hello` head an establishment declared (the DO's flushed epoch), or -1 if none yet. */
function helloHead(events: readonly BusEvent[]): number {
  const hello = events.find((e) => e.kind === "hello");
  return hello && hello.kind === "hello" ? hello.head : -1;
}

/** A minimal eventual-read probe envelope: only `tags`/`bornEpoch` steer the table. */
function envelope(tags: readonly string[], bornEpoch: number): Envelope {
  return {
    bornEpoch,
    bornMs: 0,
    codecId: "probe/1",
    enc: "json",
    grace: 0,
    key: "integration:do-bus:probe",
    kind: "val",
    scope: "pub",
    size: 0,
    src: "probe",
    storedAt: 0,
    tags,
    ttl: 0,
    v: 3,
    val: null,
  };
}

describe("doBus live channel on workerd (§4.2, E4/E19/E21)", () => {
  it("(i) connect → reset → hello declaring the DO's flushed head (③)", async () => {
    const RID = "dobus-connect";
    scope(RID);
    const stub = env.COORDINATOR.get(env.COORDINATOR.idFromName(RID));

    // A FRESH DO (no flush yet): the handshake declares head 0.
    const c1 = connect(stub, RID);
    await waitFor(() => c1.events.length >= 2, "c1 handshake");
    expect(c1.events[0]?.kind).toBe("reset");
    expect(c1.events[1]?.kind).toBe("hello");
    expect(helloHead(c1.events)).toBe(0);
    c1.close();

    // Advance the DO durably: one hard purge (seq 1). A SECOND connection's hello now declares 1 —
    // the head is the DO's OWN last-flushed epoch, not machine.currentEpoch, not a KV read-back.
    await stub.delete([A]);
    expect(await runDurableObjectAlarm(stub)).toBe(true);
    const c2 = connect(stub, RID);
    await waitFor(() => c2.events.length >= 2, "c2 handshake");
    expect(helloHead(c2.events)).toBe(1);
    c2.close();
  });

  it("(ii) mutate → flush → frame delivered live → the reader advances (E21)", async () => {
    const RID = "dobus-live";
    const KEYS = scope(RID);
    const stub = env.COORDINATOR.get(env.COORDINATOR.idFromName(RID));
    const store = cloudflareKV(env.REGISTRY_KV);

    // A reader over the LIVE channel. The DO is idle (head 0), so its hello converges the reader at
    // once (appliedEpoch 0 ≥ head 0).
    const reader = new Invalidation(doRegistry(stub, RID), store, doBus(
      () => dialCoordinator(stub),
      { regId: RID }
    ));
    // Put the reader IN USE: construction captures the bus and defers the subscription to the
    // first read, so a module-scope cache can be built where the platform forbids I/O. This case
    // drives the reader directly, so it opens the channel explicitly — what a first read would do.
    reader.ensureSubscribed();
    await waitFor(() => !reader.suspect, "idle reader converges on hello");
    expect(reader.appliedEpoch).toBe(0);

    // Mutate + flush: the flush fans a frame(0→1) out over the live socket; the reader applies it
    // contiguously and advances — NO KV resync needed (delivered live).
    await stub.delete([A]);
    expect(await runDurableObjectAlarm(stub)).toBe(true);
    await waitFor(() => reader.appliedEpoch === 1, "live frame advances applied");
    expect(reader.suspect).toBe(false);

    // The delivered watermark is correct: A was hard-deleted at epoch 1 ⇒ a value born at 0 is DEAD.
    expect(
      (await reader.validate(envelope([A], 0), "eventual", "miss")).validity
    ).toBe("dead");
    // Non-vacuous: the KV mirror also carries it (the live frame matched the durable batch).
    expect(await env.REGISTRY_KV.get(KEYS.deltaKey(1))).not.toBeNull();
    reader.close();
  });

  it("(iii) evict{close} mid-subscription → observe close → redial → handshake → resync converges (⑤/E20/E21)", async () => {
    const RID = "dobus-evict";
    const KEYS = scope(RID);
    const stub = env.COORDINATOR.get(env.COORDINATOR.idFromName(RID));
    const store = cloudflareKV(env.REGISTRY_KV);

    // Durable state so a redialed reader has a head > 0 to resync UP TO: A hard-deleted at seq 1.
    await stub.delete([A]);
    expect(await runDurableObjectAlarm(stub)).toBe(true);

    // A first live subscriber handshakes; eviction then CLOSES its socket — the client observes the
    // close (no self-reconnect; the redial is explicit).
    const c1 = connect(stub, RID);
    await waitFor(() => c1.events.length >= 2, "pre-evict handshake");
    await evictDurableObject(stub, { webSockets: "close" });
    await waitFor(() => c1.closed(), "client observes the eviction close");
    c1.close();

    // Redial into a FRESH reader (what a reconnect driver would do). The handshake declares head 1
    // over the woken DO, suspending warm trust (applied 0 < 1); a bounded resync off the KV mirror
    // then carries the reader to the declared head and clears suspicion.
    const reader = new Invalidation(doRegistry(stub, RID), store, doBus(
      () => dialCoordinator(stub),
      { regId: RID }
    ));
    // Put the reader IN USE: construction captures the bus and defers the subscription to the
    // first read, so a module-scope cache can be built where the platform forbids I/O. This case
    // drives the reader directly, so it opens the channel explicitly — what a first read would do.
    reader.ensureSubscribed();
    await waitFor(() => reader.suspect, "redial handshake suspends warm trust at head 1");
    for (let i = 0; i < 40 && reader.suspect; i++) {
      await reader.resync({ deltasExhausted: true });
      await new Promise((resolve) => {
        setTimeout(resolve, 5);
      });
    }
    expect(reader.suspect).toBe(false);
    expect(reader.appliedEpoch).toBe(1);
    // The resync carried the durable purge: A hard-deleted at 1 ⇒ a value born at 0 reads DEAD.
    expect(
      (await reader.validate(envelope([A], 0), "eventual", "miss")).validity
    ).toBe("dead");
    expect(await env.REGISTRY_KV.get(KEYS.stateKey)).not.toBeNull();
    reader.close();
  });

  it("(iv) hibernation wake → an evicted DO wakes on the flush alarm and fans out to the still-open socket (④)", async () => {
    const RID = "dobus-hibernate";
    scope(RID);
    const stub = env.COORDINATOR.get(env.COORDINATOR.idFromName(RID));

    const c1 = connect(stub, RID);
    await waitFor(() => c1.events.length >= 2, "pre-hibernate handshake");
    const before = c1.events.length;

    // Evict, HIBERNATING the socket (default): it stays open, the DO leaves memory. A mutation +
    // flush alarm must WAKE the DO (reconstruct + re-run constructor), and getWebSockets() must
    // return the hibernated socket so the fan-out reaches it.
    await evictDurableObject(stub); // default: webSockets "hibernate"
    await stub.delete([A]);
    expect(await runDurableObjectAlarm(stub)).toBe(true);

    // The woken DO fanned the frame out to the still-open socket.
    await waitFor(
      () => c1.events.slice(before).some((e) => e.kind === "frame"),
      "hibernation wake delivers the flush frame"
    );
    const frame = c1.events.slice(before).find((e) => e.kind === "frame");
    expect(frame?.kind === "frame" && frame.frame.toEpoch).toBe(1);

    // Identity = address: the DO's OWN name survives hibernation + wake — after
    // `evictDurableObject` drops it from memory and the flush alarm reconstructs it, `id.name` is
    // still the addressing name, so the name-derived regId is stable across the wake (the map's cheap
    // confirmation that the seeding source does not evaporate on rehydration).
    expect(
      await runInDurableObject(stub, (_instance, state) => state.id.name)
    ).toBe(RID);
    c1.close();
  });

  it("(v) two subscribers, one killed socket does not stall the other (①/④)", async () => {
    const RID = "dobus-fanout";
    scope(RID);
    const stub = env.COORDINATOR.get(env.COORDINATOR.idFromName(RID));

    const alive = connect(stub, RID);
    const doomed = connect(stub, RID);
    await waitFor(
      () => alive.events.length >= 2 && doomed.events.length >= 2,
      "both subscribers handshook"
    );
    const aliveBefore = alive.events.length;

    // Kill one subscriber's socket from the client side. The DO's next getWebSockets() will not
    // include it; the fan-out to the survivor must not be blocked.
    doomed.close();

    await stub.delete([A]);
    expect(await runDurableObjectAlarm(stub)).toBe(true);

    await waitFor(
      () => alive.events.slice(aliveBefore).some((e) => e.kind === "frame"),
      "the surviving subscriber still receives the frame"
    );
    alive.close();
  });
});
