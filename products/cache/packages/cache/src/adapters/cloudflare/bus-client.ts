/* References: E19. */

// PLATFORM: Cloudflare runtime bindings are confined to this adapter.

import type { Bus, BusEvent, Subscription } from "../../drivers";
import { CLOSE_PROTOCOL, CLOSE_SCOPE_MISMATCH, decode } from "./wire";

/** A message event as delivered to a WS `message` listener — `data` is the payload string. */
interface WireMessageEvent {
  readonly data: unknown;
}

/** A close event as delivered to a WS `close` listener. */
interface WireCloseEvent {
  readonly code: number;
  readonly reason: string;
}

/**
 * The minimal client-side socket surface the doBus client needs. workerd's client WebSocket
 * (`response.webSocket`, after `accept()`) satisfies it structurally; the conformance in-memory
 * pair implements the same shape.
 */
export interface ClientSocket {
  close(code?: number, reason?: string): void;
  addEventListener(
    type: "message",
    listener: (event: WireMessageEvent) => void
  ): void;
  addEventListener(
    type: "close",
    listener: (event: WireCloseEvent) => void
  ): void;
}

/** How the client obtains an OPEN socket. Returns synchronously (the conformance pair) or a promise
 * (a real `stub.fetch` → `accept()`); the client handles both. */
export type Dial = () => ClientSocket | Promise<ClientSocket>;

export interface DoBusCloseInfo {
  readonly code: number;
  readonly reason: string;
  readonly local: boolean;
}

export interface DoBusOptions {
  /* References: E19. */
  readonly regId: string;

  readonly onClose?: (info: DoBusCloseInfo) => void;
}

/** Is `value` a thenable (a promise-returning dial vs a synchronous one)? */
function isThenable(value: unknown): value is Promise<ClientSocket> {
  return (
    typeof value === "object" &&
    value !== null &&
    "then" in value &&
    typeof value.then === "function"
  );
}

/** Per-subscription mutable state — the live socket + one-shot close/report guards. */
interface SubState {
  socket: ClientSocket | null;
  closed: boolean;
  reported: boolean;
  /** The subscriber, so a close can DECLARE itself (`lost`, E28) rather than go quiet. */
  onEvent: ((event: BusEvent) => void) | null;
}

class DoBus implements Bus {
  readonly #dial: Dial;
  readonly #regId: string;
  readonly #onClose: ((info: DoBusCloseInfo) => void) | undefined;

  constructor(dial: Dial, options: DoBusOptions) {
    this.#dial = dial;
    this.#regId = options.regId;
    this.#onClose = options.onClose;
  }

  subscribe(onEvent: (event: BusEvent) => void): Subscription {
    const state: SubState = {
      closed: false,
      onEvent,
      reported: false,
      socket: null,
    };
    const attach = (socket: ClientSocket): void => {
      if (state.closed) {
        // Unsubscribed (or already closed) before the dial resolved — discard the socket.
        this.#safeSocketClose(socket, 1000, "astilba/cache: unsubscribed");
        return;
      }
      state.socket = socket;
      socket.addEventListener("message", (event) => {
        this.#onMessage(event.data, onEvent, state);
      });
      socket.addEventListener("close", (event) => {
        this.#report(state, {
          code: event.code,
          local: false,
          reason: event.reason,
        });
      });
    };
    const dialed = this.#dial();
    if (isThenable(dialed)) {
      void (async (): Promise<void> => {
        try {
          attach(await dialed);
        } catch (error: unknown) {
          this.#report(state, {
            code: CLOSE_PROTOCOL,
            local: true,
            reason: `astilba/cache: dial failed — ${String(error)}`,
          });
        }
      })();
    } else {
      attach(dialed);
    }
    return {
      close: (): void => {
        if (state.closed) {
          return;
        }
        // The SUBSCRIPTION closes regardless: dispatching stops here and now, which is this
        // object's own promise and one it can always keep. Whether the SOCKET closed is a separate
        // question with a separate answer — see below.
        state.closed = true;
        state.onEvent = null;
        if (state.socket) {
          const socketClosed = this.#safeSocketClose(
            state.socket,
            1000,
            "astilba/cache: unsubscribed"
          );
          if (!socketClosed) {
            // The platform refused (a cross-request handle). The socket is STILL OPEN, so the
            // reference is kept rather than nulled: a `socket: null` here would be this client
            // asserting a teardown it did not perform, and the next reader of this state would
            // inherit the assertion as a fact. It may defer; it may not lie. The server's
            // close-on-doubt at its next fan-out, and isolate eviction, bound the residue.
            return;
          }
        }
        state.socket = null;
      },
    };
  }

  #onMessage(
    data: unknown,
    onEvent: (event: BusEvent) => void,
    state: SubState
  ): void {
    if (state.closed) {
      return;
    }
    if (typeof data !== "string") {
      // We only ever ship text frames; a binary/absent payload is a protocol violation ⇒ close.
      this.#localClose(
        state,
        CLOSE_PROTOCOL,
        "astilba/cache: non-string frame"
      );
      return;
    }
    const result = decode(data);
    if (!result.ok) {
      this.#localClose(
        state,
        CLOSE_PROTOCOL,
        `astilba/cache: undecodable frame — ${result.reason}`
      );
      return;
    }
    const { event } = result;
    switch (event.kind) {
      case "hello": {
        if (event.regId !== this.#regId) {
          /* References: E19. */
          this.#localClose(
            state,
            CLOSE_SCOPE_MISMATCH,
            `astilba/cache: hello regId "${event.regId}" != configured "${this.#regId}"`
          );
          return;
        }
        // Strip the echo — the kernel BusEvent hello carries only the head.
        onEvent({ head: event.head, kind: "hello" });
        return;
      }
      case "reset": {
        onEvent({ kind: "reset" });
        return;
      }
      case "gap": {
        onEvent({ head: event.head, kind: "gap" });
        return;
      }
      case "frame": {
        onEvent({ frame: event.frame, kind: "frame" });
        return;
      }
      default: {
        const unreachable: never = event;
        this.#localClose(
          state,
          CLOSE_PROTOCOL,
          `astilba/cache: unreachable wire kind ${String(unreachable)}`
        );
      }
    }
  }

  #localClose(state: SubState, code: number, reason: string): void {
    if (state.closed) {
      return;
    }
    state.closed = true;
    if (state.socket) {
      this.#safeSocketClose(state.socket, code, reason);
    }
    this.#report(state, { code, local: true, reason });
  }

  /**
   * Fire the status report at most once per subscription, and DECLARE the loss to the subscriber
   * (E28). The declaration is what keeps a dead channel from reading as a quiet one: `reset`/`hello`
   * are the only other things this client dispatches, so without a `lost` the reader's evidence for
   * "the bus never came up" and "the bus has nothing to say" is identical — namely nothing.
   *
   * The subscriber is notified BEFORE the app's `onClose`, and its throw cannot eat the report (nor
   * the reverse): each is guarded separately, because the two consumers are independent and neither
   * one's failure is the other's business.
   */
  #report(state: SubState, info: DoBusCloseInfo): void {
    if (state.reported) {
      return;
    }
    state.reported = true;
    state.closed = true;
    const { onEvent } = state;
    if (onEvent) {
      try {
        onEvent({ kind: "lost" });
      } catch {
        // A subscriber that threw on the declaration; it does not get to suppress `onClose`.
      }
    }
    this.#onClose?.(info);
  }

  /**
   * Close the socket, tolerating a platform refusal — WITHOUT claiming success (E28).
   *
   * The refusal is real and routine: this socket may have been minted in an earlier request, and the
   * platform refuses `close()` from a different one exactly as it refuses every other operation on
   * a foreign handle. Swallowing that and marking the subscription closed anyway was the shape of
   * the lie: local state said "closed", the socket stayed open, and the server kept a subscriber it
   * would fan out to forever.
   *
   * SO THE RETURN VALUE IS THE HONESTY. `false` means the close did NOT happen and the socket is
   * still out there; the caller records that rather than overwriting it with an assumption. This may
   * DEFER a close (the leak below), and that is a cost; it may not claim one that never occurred.
   *
   * THE LEAK'S BOUND, STATED PLAINLY. A socket this cannot close is not unbounded: the server closes
   * on doubt at its next fan-out (a send to a socket whose client is gone fails, and the DO drops
   * it), and isolate eviction takes the rest. So the residue is one socket per refused close, for at
   * most as long as the Coordinator takes to speak again — priced and finite, which a silent
   * divergence between local state and the wire is not.
   */
  #safeSocketClose(
    socket: ClientSocket,
    code: number,
    reason: string
  ): boolean {
    try {
      socket.close(code, reason);
      return true;
    } catch {
      // Already torn down, or refused because this socket belongs to another request's context.
      // Either way: NOT closed by us. The caller is told, and tells the truth in turn.
      return false;
    }
  }
}

export function doBus(dial: Dial, options: DoBusOptions): Bus {
  return new DoBus(dial, options);
}
