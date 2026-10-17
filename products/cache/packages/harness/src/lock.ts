import type { Lock, LockHandle } from "@astilba/cache";

// FakeLock — a deterministic cross-isolate lock issuing MONOTONIC fencing
// tokens. Each acquire hands out a strictly higher token than the last, so a later
// lease-holder always out-fences an earlier one. The born-monotone write-back rule
// uses that token — never `bornMs` — to reject a stale lease-holder's
// late write. No blocking: a fresh acquire models a lease that has superseded the
// previous holder (whose lower token is now stale), which is exactly the
// "lock lease expiring before write-back" scenario.

export class FakeLock implements Lock {
  #fence = 0;
  readonly #held = new Set<string>();

  async acquire(key: string): Promise<LockHandle> {
    this.#fence += 1;
    const fence = this.#fence;
    this.#held.add(key);
    return {
      fence,
      release: async () => {
        this.#held.delete(key);
      },
    };
  }

  /** Test accessor: the most recently issued fencing token. */
  get lastFence(): number {
    return this.#fence;
  }

  /** Test accessor: whether `key` is currently held by an unreleased handle. */
  isHeld(key: string): boolean {
    return this.#held.has(key);
  }
}
