import type { BusEvent, BusFrame } from "@astilba/cache";

import type { ClientSocket } from "../../../src/adapters/cloudflare/bus-client";
import { doBus } from "../../../src/adapters/cloudflare/bus-client";
import type { ServerSocket } from "../../../src/adapters/cloudflare/bus-server";
import { establish, fanOut } from "../../../src/adapters/cloudflare/bus-server";
import { decode } from "../../../src/adapters/cloudflare/wire";
import type { BusHarness, DoubtProbe, MismatchProbe } from "../runner/bus";

/* References: §4.2, E19. */

const WS_OPEN = 1;
const WS_CLOSING = 2;
const WS_CLOSED = 3;

interface MessageEventLike {
  readonly data: string;
}
interface CloseEventLike {
  readonly code: number;
  readonly reason: string;
}
type MessageListener = (event: MessageEventLike) => void;
type CloseListener = (event: CloseEventLike) => void;

/**
 * One END of an in-memory duplex WebSocket pair. Satisfies BOTH `ServerSocket` (send/close/
 * readyState — used by the server fan-out) AND `ClientSocket` (addEventListener — used by the doBus
 * client). Messages received before a `message` listener attaches are BUFFERED and flushed on
 * attach, so the establishment `reset`/`hello` (sent during `dial`, before the client listens) are
 * not lost — the whole pair is synchronous and correctly ordered.
 */
class MemorySocket {
  #peer: MemorySocket | undefined;
  readonly #messageListeners: MessageListener[] = [];
  readonly #closeListeners: CloseListener[] = [];
  #inbox: string[] = [];
  #throwOnSend = false;
  readyState = WS_OPEN;

  link(peer: MemorySocket): void {
    this.#peer = peer;
  }

  /** Receive a datum FROM the peer (called by the peer's `send`). Buffered until a listener attaches. */
  receiveFromPeer(data: string): void {
    if (this.#messageListeners.length === 0) {
      this.#inbox.push(data);
      return;
    }
    for (const listener of this.#messageListeners) {
      listener({ data });
    }
  }

  /** The peer closed — mark this end closed and fire its close listeners. */
  onPeerClose(code: number, reason: string): void {
    if (this.readyState === WS_CLOSED) {
      return;
    }
    this.readyState = WS_CLOSED;
    for (const listener of this.#closeListeners) {
      listener({ code, reason });
    }
  }

  send(data: string): void {
    if (this.#throwOnSend) {
      throw new Error("memory socket: forced send failure (delivery doubt)");
    }
    if (this.readyState !== WS_OPEN) {
      throw new Error("memory socket: not open");
    }
    this.#peer?.receiveFromPeer(data);
  }

  close(code = 1000, reason = ""): void {
    if (this.readyState === WS_CLOSED) {
      return;
    }
    this.readyState = WS_CLOSED;
    for (const listener of this.#closeListeners) {
      listener({ code, reason });
    }
    this.#peer?.onPeerClose(code, reason);
  }

  addEventListener(type: "message", listener: MessageListener): void;
  addEventListener(type: "close", listener: CloseListener): void;
  addEventListener(
    type: "message" | "close",
    listener: MessageListener | CloseListener
  ): void {
    if (type === "message") {
      // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the "message" overload guarantees a MessageListener; the two params are uncorrelated in the impl signature, so a narrowing cast is required.
      const messageListener = listener as MessageListener;
      this.#messageListeners.push(messageListener);
      const buffered = this.#inbox;
      this.#inbox = [];
      for (const data of buffered) {
        messageListener({ data });
      }
      return;
    }
    // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- the "close" overload guarantees a CloseListener (same uncorrelated-params reason as above).
    this.#closeListeners.push(listener as CloseListener);
  }

  forceDoubt(): void {
    this.#throwOnSend = true;
    this.readyState = WS_CLOSING;
  }
}

function makeSocketPair(): { client: MemorySocket; server: MemorySocket } {
  const client = new MemorySocket();
  const server = new MemorySocket();
  client.link(server);
  server.link(client);
  return { client, server };
}

class MemoryServer {
  readonly #sockets = new Set<MemorySocket>();
  #head = 0;
  readonly regId: string;
  readonly #broadcaster: (
    sockets: Iterable<ServerSocket>,
    frame: BusFrame
  ) => void;

  constructor(
    regId: string,
    broadcaster: (
      sockets: Iterable<ServerSocket>,
      frame: BusFrame
    ) => void = fanOut
  ) {
    this.regId = regId;
    this.#broadcaster = broadcaster;
  }

  connect(serverEnd: MemorySocket): void {
    this.#sockets.add(serverEnd);
    serverEnd.addEventListener("close", () => {
      this.#sockets.delete(serverEnd);
    });
    establish(serverEnd, this.#head, this.regId);
  }

  publish(frame: BusFrame): void {
    this.#head = Math.max(this.#head, frame.toEpoch);
    this.#broadcaster(this.#sockets, frame);
  }

  /** Model a transport re-establishment (the contract's `reconnect`): re-run the handshake on every
   * live socket — the real `establish` with the CURRENT head, so the fresh `hello` declares it. */
  reEstablish(): void {
    for (const socket of this.#sockets) {
      establish(socket, this.#head, this.regId);
    }
  }
}

/** Dial a fresh client socket, register + establish its server end. `onServerEnd` captures the
 * server end (for the doubt probe to arm it). */
function dialAgainst(
  server: MemoryServer,
  onServerEnd?: (socket: MemorySocket) => void
): ClientSocket {
  const pair = makeSocketPair();
  onServerEnd?.(pair.server);
  server.connect(pair.server);
  return pair.client;
}

/** Drive a mismatched-scope subscriber against `server` via the given client factory, capturing the
 * events it received + whether it closed (③). A conformant client dispatches NO hello and closes. */
function runMismatchProbe(
  server: MemoryServer,
  makeClient: typeof doBus
): MismatchProbe {
  const events: BusEvent[] = [];
  let closed = false;
  const mismatchedBus = makeClient(() => dialAgainst(server), {
    onClose: (): void => {
      closed = true;
    },
    regId: "conformance-mismatch", // != server.regId ("default")
  });
  const sub = mismatchedBus.subscribe((event) => {
    events.push(event);
  });
  sub.close();
  return { closed, events };
}

function runDoubtProbe(server: MemoryServer, frame: BusFrame): DoubtProbe {
  const doubtedFrames: BusFrame[] = [];
  const healthyFrames: BusFrame[] = [];
  let doubtedClosed = false;
  let healthyClosed = false;
  let doubtedServerEnd: MemorySocket | undefined;

  const doubtedBus = doBus(
    () =>
      dialAgainst(server, (socket) => {
        doubtedServerEnd = socket;
      }),
    {
      onClose: (): void => {
        doubtedClosed = true;
      },
      regId: server.regId,
    }
  );
  doubtedBus.subscribe((event) => {
    if (event.kind === "frame") {
      doubtedFrames.push(event.frame);
    }
  });

  const healthyBus = doBus(() => dialAgainst(server), {
    onClose: (): void => {
      healthyClosed = true;
    },
    regId: server.regId,
  });
  healthyBus.subscribe((event) => {
    if (event.kind === "frame") {
      healthyFrames.push(event.frame);
    }
  });

  doubtedServerEnd?.forceDoubt();
  server.publish(frame);

  return {
    doubted: { closed: doubtedClosed, frames: doubtedFrames },
    healthy: { closed: healthyClosed, frames: healthyFrames },
  };
}

export function makeDoBusHarness(): () => BusHarness {
  return (): BusHarness => {
    const server = new MemoryServer("default");
    const bus = doBus(() => dialAgainst(server), { regId: server.regId });
    return {
      bus,
      publish: (frame): void => {
        server.publish(frame);
      },
      reconnect: (): void => {
        server.reEstablish();
      },
      subscribeMismatchedScope: async (): Promise<MismatchProbe> =>
        runMismatchProbe(server, doBus),
      deliveryDoubtProbe: async (frame): Promise<DoubtProbe> =>
        runDoubtProbe(server, frame),
    };
  };
}

// ── Deliberately-BROKEN doBus harnesses (teeth) ──────────────────────────────────────────────────

/**
 * A CLIENT that SKIPS the E19 regId-echo check (③) — it dispatches every decoded event, `hello`
 * included, regardless of the scope the DO echoed. A mismatched-scope subscriber therefore RECEIVES
 * a hello for a foreign channel instead of closing loud. Targets: the regId-echo core check.
 */
function naiveNoEchoClient(
  dial: () => ClientSocket,
  onClose: () => void
): { subscribe: (onEvent: (event: BusEvent) => void) => void } {
  return {
    subscribe: (onEvent): void => {
      const socket = dial();
      socket.addEventListener("message", (event) => {
        if (typeof event.data !== "string") {
          return;
        }
        const result = decode(event.data);
        if (!result.ok) {
          return;
        }
        const wire = result.event;
        if (wire.kind === "hello") {
          // BUG: dispatches the hello WITHOUT asserting `wire.regId` matches — the foreign-channel
          // read E19 forbids. A conformant client closes loud here.
          onEvent({ head: wire.head, kind: "hello" });
          return;
        }
        onEvent(wire);
      });
      socket.addEventListener("close", () => {
        onClose();
      });
    },
  };
}

export function makeNoEchoCheckDoBusHarness(): () => BusHarness {
  return (): BusHarness => {
    const server = new MemoryServer("default");
    const bus = doBus(() => dialAgainst(server), { regId: server.regId });
    return {
      bus,
      publish: (frame): void => {
        server.publish(frame);
      },
      subscribeMismatchedScope: async (): Promise<MismatchProbe> => {
        const events: BusEvent[] = [];
        let closed = false;
        const naive = naiveNoEchoClient(
          () => dialAgainst(server),
          () => {
            closed = true;
          }
        );
        naive.subscribe((event) => {
          events.push(event);
        });
        return { closed, events };
      },
    };
  };
}

function naiveNoCloseFanOut(
  sockets: Iterable<ServerSocket>,
  frame: BusFrame
): void {
  const data = JSON.stringify({
    frame: JSON.stringify(frame),
    kind: "frame",
    v: 1,
  });
  for (const socket of sockets) {
    // BUG: skip a non-OPEN socket, but do NOT close it (the silent-gap risk ① forbids).
    if (socket.readyState !== undefined && socket.readyState !== WS_OPEN) {
      continue;
    }
    try {
      socket.send(data);
    } catch {
      // BUG: swallow a throwing send WITHOUT closing — the subscriber is left half-dead.
    }
  }
}

export function makeNoCloseOnDoubtDoBusHarness(): () => BusHarness {
  return (): BusHarness => {
    const server = new MemoryServer("default", naiveNoCloseFanOut);
    const bus = doBus(() => dialAgainst(server), { regId: server.regId });
    return {
      bus,
      publish: (frame): void => {
        server.publish(frame);
      },
      deliveryDoubtProbe: async (frame): Promise<DoubtProbe> =>
        runDoubtProbe(server, frame),
    };
  };
}
