import type { Clock, Rng } from "@astilba/cache";

// The deterministic time and randomness the kernel receives. Everything is driven
// by this logical clock; nothing awaits real time. fast-check's own seeded PRNG
// lives in this test layer and is exempt from the src grep-guard.

/**
 * A logical clock. `now()` is the injected `Clock`; `advance`/`runMicrotasks`
 * are sim controls the world drives — they are NOT part of the `Clock` the kernel
 * sees.
 */
export class SimClock implements Clock {
  #now: number;

  constructor(start = 0) {
    this.#now = start;
  }

  now(): number {
    return this.#now;
  }

  /** Move logical time forward. Never moves backward. */
  advance(ms: number): void {
    if (ms < 0) {
      throw new Error(`SimClock.advance: ms must be >= 0 (got ${ms})`);
    }
    this.#now += ms;
  }

  /** Flush the microtask queue so pending promise continuations settle deterministically. */
  async runMicrotasks(rounds = 3): Promise<void> {
    for (let i = 0; i < rounds; i++) {
      await Promise.resolve();
    }
  }
}

/**
 * A per-isolate view of the shared logical clock with an injectable wall-clock
 * skew. Advancing the base `SimClock` moves every view together; the skew shifts
 * only this isolate's `now()` — so a stale writer can hold a strictly HIGHER
 * `bornMs` than a fresher one while the registry epoch (the correctness clock) is
 * unaffected. This is how the "wall clock never arbitrates" trace is built.
 */
export class SkewedClock implements Clock {
  readonly #base: SimClock;
  #skewMs: number;

  constructor(base: SimClock, skewMs = 0) {
    this.#base = base;
    this.#skewMs = skewMs;
  }

  now(): number {
    return this.#base.now() + this.#skewMs;
  }

  setSkew(ms: number): void {
    this.#skewMs = ms;
  }

  get skewMs(): number {
    return this.#skewMs;
  }
}

/**
 * A seeded, reproducible PRNG (mulberry32). The kernel's only randomness source
 * (jitter, etc.); never `Math.random`. Same seed ⇒ same stream.
 */
export class SimRng implements Rng {
  #state: number;

  constructor(seed: number) {
    // Coerce to a 32-bit unsigned integer state.
    this.#state = seed >>> 0;
  }

  /** A float in [0, 1). */
  next(): number {
    this.#state = (this.#state + 0x6d_2b_79_f5) >>> 0;
    let t = this.#state;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4_294_967_296;
  }
}
