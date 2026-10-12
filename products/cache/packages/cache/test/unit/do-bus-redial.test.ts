/* References: E28. */

import type { BusEvent, Rng } from "@astilba/cache";
import { describe, expect, it } from "vitest";

import type { ClientSocket } from "../../src/adapters/cloudflare/bus-client";
// Deep-imported, NOT via `@astilba/cache/cloudflare`: that barrel re-exports the Coordinator, whose
// `cloudflare:workers` import resolves only inside the Workers isolate. `redial.ts` itself is pure
// (E28 removed its last platform touch — the default scheduler — so it now has none at all) — the
// same reason the conformance lane drives `bus-client`/`wire` from Node.
import { redialingDoBus } from "../../src/adapters/cloudflare/redial";
import { encode } from "../../src/adapters/cloudflare/wire";

const REG_ID = "redial-unit";

type MessageListener = (event: { readonly data: unknown }) => void;
type CloseListener = (event: {
  readonly code: number;
  readonly reason: string;
}) => void;

class FakeSocket implements ClientSocket {
  #onMessage: MessageListener | undefined;
  #onClose: CloseListener | undefined;

  addEventListener(
    ...args: ["message", MessageListener] | ["close", CloseListener]
  ): void {
    if (args[0] === "message") {
      const [, onMessage] = args;
      this.#onMessage = onMessage;
      return;
    }
    const [, onClose] = args;
    this.#onClose = onClose;
  }

  close(): void {
    // The client's own close never loops back as a transport close event here.
  }

  hello(head: number): void {
    this.#onMessage?.({ data: encode({ head, kind: "hello", regId: REG_ID }) });
  }

  /** The transport delivers a close (server close, eviction, network drop). */
  fireClose(code = 1006, reason = "socket closed"): void {
    this.#onClose?.({ code, reason });
  }
}

/** `next() = 0.5` with the ±20% jitter ⇒ factor exactly 1.0: the due-times ARE the ladder's rungs. */
const midRng: Rng = { next: () => 0.5 };

/** A dial that hands out fresh fake sockets and records every one it minted. */
function recordingDial(): {
  readonly dial: () => ClientSocket;
  readonly sockets: FakeSocket[];
} {
  const sockets: FakeSocket[] = [];
  return {
    dial: (): ClientSocket => {
      const socket = new FakeSocket();
      sockets.push(socket);
      return socket;
    },
    sockets,
  };
}

describe("doBus redial driver — lazy, tick-driven reconnect (C4-b, ruling ⑤, E28)", () => {
  it("re-establishes a closed socket on a DUE tick, and NOT before one (blind the redial ⇒ dead channel)", () => {
    const { dial, sockets } = recordingDial();
    const events: BusEvent[] = [];
    const bus = redialingDoBus(dial, { regId: REG_ID, rng: midRng });
    const subscription = bus.subscribe((event) => {
      events.push(event);
    });

    // Socket 1 establishes and delivers its hello. A tick seeds the driver's clock at t=0.
    sockets[0]?.hello(3);
    bus.tick(0);
    // The transport then closes (an eviction). The driver SCHEDULES the bottom rung (1s) — and dials
    // NOTHING: no timer exists to fire it, and no request context is available to a close.
    sockets[0]?.fireClose();
    expect(sockets).toHaveLength(1);

    // A tick BEFORE the rung is due changes nothing. This is the E28 pacing branch.
    bus.tick(999);
    expect(sockets).toHaveLength(1);

    // The rung comes due: THIS tick dials — inside the request context that supplied it. A NEW
    // socket re-handshakes and its hello flows to the SAME subscriber — one uninterrupted event
    // stream across two sockets. Blind the redial (drop the onClose wiring) and there is no second
    // socket and no second hello ⇒ red.
    bus.tick(1000);
    expect(sockets).toHaveLength(2);
    sockets[1]?.hello(7);
    expect(events).toStrictEqual([
      { head: 3, kind: "hello" },
      { kind: "lost" },
      { head: 7, kind: "hello" },
    ]);

    subscription.close();
  });

  it("backs off ×2 per consecutive failed establishment and CAPS at 300s (blind the ×2 or the cap ⇒ red)", () => {
    // Every socket closes without ever saying hello — an establishment that never completes (the
    // Coordinator is down). Each failure climbs one rung, and the rung is read off WHEN a tick is
    // allowed to redial rather than off a recorded timer delay.
    const { dial, sockets } = recordingDial();
    const bus = redialingDoBus(dial, { regId: REG_ID, rng: midRng });
    const subscription = bus.subscribe(() => {
      // The reader's own handling is not this case's subject.
    });

    // base 1s, ×2 per rung, capped at 300s. At each rung: a tick one ms EARLY must not dial; the
    // tick AT the due time must. Blind the doubling and the early tick dials ⇒ red.
    const rungs = [
      1000, 2000, 4000, 8000, 16_000, 32_000, 64_000, 128_000, 256_000, 300_000,
      300_000,
    ];
    let now = 0;
    for (const [i, rung] of rungs.entries()) {
      bus.tick(now); // the driver's clock, from which this close is dated
      sockets[i]?.fireClose();
      const expected = i + 1;
      bus.tick(now + rung - 1);
      expect(sockets).toHaveLength(expected); // too early — no dial
      now += rung;
      bus.tick(now);
      expect(sockets).toHaveLength(expected + 1); // due — dialed
    }

    subscription.close();
  });

  it("RESETS the ladder on the hello that proves establishment (blind the reset ⇒ the delay keeps climbing)", () => {
    const { dial, sockets } = recordingDial();
    const bus = redialingDoBus(dial, { regId: REG_ID, rng: midRng });
    const subscription = bus.subscribe(() => {
      // Not this case's subject.
    });

    // Two failed establishments: rungs 1s, then 2s.
    bus.tick(0);
    sockets[0]?.fireClose();
    bus.tick(1000);
    expect(sockets).toHaveLength(2);
    sockets[1]?.fireClose();
    bus.tick(3000);
    expect(sockets).toHaveLength(3);

    // Socket 3 ESTABLISHES (hello), then closes later — a healthy socket that got evicted.
    sockets[2]?.hello(1);
    sockets[2]?.fireClose();

    // Its redial is at the BOTTOM rung again (1s from now=3000), not 4s: the hello reset the
    // counter. Blind the reset and the rung is 4000, so the tick at 4000 does NOT dial ⇒ red. A
    // healthy channel that blips once would otherwise carry a dead one's backoff, and every eviction
    // would push the fleet further from its live channel.
    bus.tick(4000);
    expect(sockets).toHaveLength(4);

    subscription.close();
  });

  it("DECLARES every close as `lost` to the subscriber — establishment is not this driver's to judge", () => {
    // The dial-failure-as-silence tooth (E28). Blind the declaration and a bus that never came up is
    // indistinguishable from a healthy one with nothing to say.
    //
    // Both closes below declare `lost`, and that is the point: the socket cannot tell a dial that
    // never came up from a working channel ending, because from its side both are just an ending.
    // The reader draws that line (it saw the `hello`, or it did not) and counts the failures — one
    // fact, one site. This driver reports; it does not classify.
    const { dial, sockets } = recordingDial();
    const events: BusEvent[] = [];
    const bus = redialingDoBus(dial, {
      regId: REG_ID,
      rng: midRng,
    });
    const subscription = bus.subscribe((event) => {
      events.push(event);
    });

    // Socket 1 never says hello, then closes: declared.
    sockets[0]?.fireClose();
    expect(events).toStrictEqual([{ kind: "lost" }]);

    // Socket 2 ESTABLISHES first, then closes: declared just the same.
    bus.tick(0);
    bus.tick(1000);
    expect(sockets).toHaveLength(2);
    sockets[1]?.hello(5);
    sockets[1]?.fireClose();
    expect(events.filter((event) => event.kind === "lost")).toHaveLength(2);

    subscription.close();
  });

  it("an app-closed subscription cancels the pending redial — no zombie socket", () => {
    const { dial, sockets } = recordingDial();
    const bus = redialingDoBus(dial, { regId: REG_ID, rng: midRng });
    const subscription = bus.subscribe(() => {
      // Not this case's subject.
    });

    bus.tick(0);
    sockets[0]?.fireClose(); // a redial is pending…
    subscription.close(); // …and the app unsubscribes before any tick makes it due.
    bus.tick(1_000_000); // a tick long past due is now a no-op
    expect(sockets).toHaveLength(1);
  });
});
