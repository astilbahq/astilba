import type { Bus, BusEvent, BusFrame, Subscription } from "@astilba/cache";

import type { SimClock } from "./clock";

// FakeBus — cross-instance BusEvent delivery with injectable drop, delay, reorder,
// and disconnect/reconnect (§4.2, E4/E21). It speaks the interface union internally:
// a `reset` THEN a `hello{head}` on initial connect and on every reconnect() (B1′), a
// `gap{head}` on a dropped/lost frame (B2′), and a `frame` normally. `head` is the max
// `toEpoch` ever broadcast on this channel — the registry's current durable epoch,
// declared regardless of whether the frame was delivered (delivery ≠ truth; models the
// C3 DO's durable state) — so a `gap{head}` declares a head ≥ the frame it lost. Delivery
// is at-most-once, so a drop or a disconnect leaves a
// hole the kernel must detect and resync — the kernel, not the bus, owns epoch-contiguity
// validation (B3). `publish` is the registry side; the world pumps due frame deliveries on
// `advance`. The chaos surface (`dropNextFrames`/`disconnect`/`reconnect`) keeps its v0.1
// signatures so the sacred traces do not move.

type EventHandler = (event: BusEvent) => void;

interface Pending {
  readonly frame: BusFrame;
  readonly deliverAtMs: number;
}

export class FakeBus implements Bus {
  readonly #clock: SimClock;
  readonly #handlers = new Set<EventHandler>();
  #pending: Pending[] = [];

  #connected = true;
  #dropNext = 0;
  #delayMs = 0;
  #reorderNext = false;
  /** Max `toEpoch` ever broadcast — the channel's declared head (B1′). Advanced on every
   * `publish` regardless of delivery (delivery ≠ truth; models the C3 DO's durable state). */
  #head = 0;
  /** Cumulative count of frames ACTUALLY dropped (a `gap` emitted), and of buffered-frame
   * pairs ACTUALLY swapped out of order (CACHE-28-ii Stage 2). These count the DELIVERY
   * perturbation FIRING, not the knob being armed — arming `dropNextFrames`/`reorderNext`
   * with no subsequent publish, or a reorder with fewer than two buffered frames, is a no-op
   * that never increments. The chaos property reads them to floor each dimension's reach:
   * a wide+chaos run that never actually dropped a frame / never actually reordered has not
   * exercised that arm, and its floor reds (the anti-inert-knob teeth in property form). */
  #gapFireCount = 0;
  #reorderFireCount = 0;

  constructor(clock: SimClock) {
    this.#clock = clock;
  }

  subscribe(onEvent: EventHandler): Subscription {
    this.#handlers.add(onEvent);
    // A subscriber establishing its transport gets `reset` THEN `hello{head}` BEFORE any
    // frame (B1′) — all warm trust starts suspended, and the hello declares the head this
    // subscriber must reach to reconverge (E4/E21).
    if (this.#connected) {
      onEvent({ kind: "reset" });
      onEvent({ head: this.#head, kind: "hello" });
    }
    return {
      close: () => {
        this.#handlers.delete(onEvent);
      },
    };
  }

  /** Registry side: broadcast a frame (subject to chaos). */
  publish(frame: BusFrame): void {
    // The channel declares the registry's current epoch the instant it is broadcast —
    // BEFORE the drop/disconnect logic (delivery ≠ truth). A frame lost to a drop or a
    // disconnect still advanced the durable head; a later hello declares it (B1′, E21).
    this.#head = Math.max(this.#head, frame.toEpoch);
    if (!this.#connected) {
      return; // at-most-once: lost while disconnected (reconnect emits `reset` → `hello`)
    }
    if (this.#dropNext > 0) {
      this.#dropNext--;
      // A dropped frame is a hole: signal `gap{head}` so the subscriber resyncs (B2′). `#head`
      // (set above) already includes THIS dropped frame's `toEpoch` — delivery ≠ truth — so
      // `gap.head` ≥ every frame emitted before the signal: the loss-revealing event declares
      // the head the reader must reach. The frame itself is never delivered (at-most-once).
      this.#gapFireCount++;
      this.#emit({ head: this.#head, kind: "gap" });
      return;
    }
    const deliverAtMs = this.#clock.now() + this.#delayMs;
    this.#pending.push({ deliverAtMs, frame });
    if (this.#reorderNext && this.#pending.length >= 2) {
      // Swap the two most recent pending frames so they arrive out of order.
      const n = this.#pending.length;
      const a = this.#pending[n - 1];
      const b = this.#pending[n - 2];
      if (a && b) {
        this.#pending[n - 1] = b;
        this.#pending[n - 2] = a;
        this.#reorderFireCount++;
      }
      this.#reorderNext = false;
    }
    if (this.#delayMs === 0) {
      this.deliverDue(this.#clock.now());
    }
  }

  /** Deliver every pending frame whose delivery time has arrived (world drives this). */
  deliverDue(now: number): void {
    const due: Pending[] = [];
    const rest: Pending[] = [];
    for (const p of this.#pending) {
      (p.deliverAtMs <= now ? due : rest).push(p);
    }
    this.#pending = rest;
    for (const p of due) {
      this.#emit({ frame: p.frame, kind: "frame" });
    }
  }

  /** Fan a single event out to every subscriber. */
  #emit(event: BusEvent): void {
    for (const handler of this.#handlers) {
      handler(event);
    }
  }

  // ── Chaos controls (world.chaos) ──────────────────────────────────────────

  /** Drop the next `n` published frames (each creates an epoch gap → `gap`). */
  dropNextFrames(n: number): void {
    this.#dropNext += n;
  }

  /** Delay subsequent frames by `ms` (delivered on the next `advance` past that time). */
  delay(ms: number): void {
    this.#delayMs = ms;
  }

  /** Swap the ordering of the next two buffered frames. */
  reorderNext(): void {
    this.#reorderNext = true;
  }

  /** Sever delivery — frames published while disconnected are lost (at-most-once). */
  disconnect(): void {
    this.#connected = false;
  }

  /** Restore delivery — the transport re-establishes, so every subscriber gets a `reset`
   * THEN a `hello{head}` and must resync up to that head before warm trust resumes (B1′/E4/E21).
   * An idle registry's head equals the applied epoch, so a caught-up subscriber reconverges at
   * once on the hello; a behind one resyncs the delta prefix up to it first. */
  reconnect(): void {
    this.#connected = true;
    this.#emit({ kind: "reset" });
    this.#emit({ head: this.#head, kind: "hello" });
  }

  get connected(): boolean {
    return this.#connected;
  }

  /** Frames actually dropped so far (a `gap` was emitted) — the reach signal for the GAP
   * dimension (CACHE-28-ii Stage 2). Fresh per world, so `> 0` means a frame was dropped
   * during THIS run. */
  get gapFireCount(): number {
    return this.#gapFireCount;
  }

  /** Buffered-frame pairs actually swapped out of order so far — the reach signal for the
   * REORDER dimension. A swap fires only with two buffered frames (⇒ a prior `delayFrames`
   * held them), so `> 0` proves an out-of-order delivery genuinely happened this run. */
  get reorderFireCount(): number {
    return this.#reorderFireCount;
  }
}
