// @astilba/cache adapter — the doBus SERVER fan-out + handshake (phase 2, rulings).
//
// PLATFORM: lives under `src/adapters/**` but is PURE — no ambient time/randomness,
// no `cloudflare:*` import — so the Node conformance lane drives the SAME logic the Coordinator
// hosts. It operates on a STRUCTURAL `ServerSocket` (workerd's hibernatable WebSocket from
// `getWebSockets()`/`acceptWebSocket` satisfies it; the conformance in-memory pair implements the
// same shape), so the fan-out/handshake code is exercised identically by the real DO and the fake.
//
// NO SERVER GAP. WS delivery is per-connection ORDERED, so silent mid-stream loss cannot
// happen: doBus emits NO `gap`. Any delivery DOUBT (a throwing `send`, a non-OPEN socket) ⇒ CLOSE
// that ONE socket with an explicit code; the establishment handshake is the recovery, and one dead
// subscriber never blocks the fan-out to the rest.
//
// STATELESS FAN-OUT. The DO holds ZERO per-subscriber replay state: `reset` → `hello` +
// the reader's KV resync IS the catch-up. No replay buffer lives here.

import type { BusFrame } from "../../drivers";
import { CLOSE_DELIVERY_DOUBT, encode } from "./wire";

/**
 * The minimal server-side socket surface the fan-out needs. workerd's hibernatable WebSocket
 * satisfies it structurally; the conformance in-memory pair implements the same shape. `readyState`
 * is optional — a socket that does not expose it is treated as open and doubt is caught by a
 * throwing `send` instead.
 */
export interface ServerSocket {
  send(data: string): void;
  close(code: number, reason?: string): void;
  readonly readyState?: number;
}

/** WS `OPEN` readyState (the numeric constant workerd and the browser share). */
const WS_OPEN = 1;

/** Close a socket, swallowing any throw (it may already be gone — the subscriber redials + resyncs). */
function safeClose(socket: ServerSocket): void {
  try {
    socket.close(
      CLOSE_DELIVERY_DOUBT,
      "astilba/cache: delivery doubt — reconnect + resync"
    );
  } catch {
    // Already torn down — nothing more to do.
  }
}

/**
 * Send one datum to one subscriber, or CLOSE it on ANY delivery doubt: a non-OPEN
 * socket, or a `send` that throws. Returns `true` iff the datum was handed to `send` cleanly. Never
 * rethrows — a doubtful subscriber is closed and skipped so the fan-out continues to the rest.
 */
function sendOrClose(socket: ServerSocket, data: string): boolean {
  if (socket.readyState !== undefined && socket.readyState !== WS_OPEN) {
    // Closing/closed/connecting — delivery is doubtful. Close rather than risk a silent gap.
    safeClose(socket);
    return false;
  }
  try {
    socket.send(data);
    return true;
  } catch {
    safeClose(socket);
    return false;
  }
}

/**
 * The establishment handshake: emit `reset` THEN `hello{head, regId}` BEFORE any frame, so
 * a subscriber starts with warm trust SUSPENDED and a head to reconverge on. `head` is the DO's OWN
 * last-flushed epoch — NEVER `machine.currentEpoch` (it over-declares into the
 * un-flushed window), NEVER a KV read-back (the mirror is stale-licensed; the DO is the truth
 * source). The `hello` echoes `regId` so a mismatched subscriber closes loud.
 */
export function establish(
  socket: ServerSocket,
  head: number,
  regId: string
): void {
  sendOrClose(socket, encode({ kind: "reset" }));
  sendOrClose(socket, encode({ head, kind: "hello", regId }));
}

/**
 * Fan ONE frame out to every current subscriber (stateless; no replay buffer). The frame
 * is encoded ONCE, then delivered per-socket via `sendOrClose`, so a doubtful subscriber is closed
 * and the rest still receive it. doBus emits NO `gap` here: the frame is a normal
 * delivery, and the only failure mode is a per-socket CLOSE, whose remedy is the handshake.
 */
export function fanOut(sockets: Iterable<ServerSocket>, frame: BusFrame): void {
  const data = encode({ frame, kind: "frame" });
  for (const socket of sockets) {
    sendOrClose(socket, data);
  }
}
