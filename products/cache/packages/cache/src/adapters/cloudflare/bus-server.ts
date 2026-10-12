// PLATFORM: Cloudflare runtime bindings are confined to this adapter.

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

/* References: E19. */

export function establish(
  socket: ServerSocket,
  head: number,
  regId: string
): void {
  sendOrClose(socket, encode({ kind: "reset" }));
  sendOrClose(socket, encode({ head, kind: "hello", regId }));
}

export function fanOut(sockets: Iterable<ServerSocket>, frame: BusFrame): void {
  const data = encode({ frame, kind: "frame" });
  for (const socket of sockets) {
    sendOrClose(socket, data);
  }
}
