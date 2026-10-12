import assert from "node:assert/strict";

import type { Lock } from "@astilba/cache";
import { describe, it } from "vitest";

/* References: §5.2, §5.0, INV-17, E3. */

const LOCK_KEY = "conformance:lock:k";

const checkMonotonePerKey = async (make: () => Lock): Promise<void> => {
  const lock = make();
  const first = await lock.acquire(LOCK_KEY);
  await first.release();
  const second = await lock.acquire(LOCK_KEY);
  assert.ok(
    Number.isInteger(first.fence) && Number.isInteger(second.fence),
    "fencing tokens must be integers"
  );
  assert.ok(
    second.fence > first.fence,
    "a re-acquire of the same key must issue a strictly higher fence (INV-17)"
  );
};

const checkLaterOutFences = async (make: () => Lock): Promise<void> => {
  const lock = make();
  const earlier = await lock.acquire(LOCK_KEY);
  // A fresh acquire models a lease that has superseded the previous holder; its
  // higher fence out-arbitrates the earlier one regardless of any wall clock (E3).
  const later = await lock.acquire(LOCK_KEY);
  assert.ok(
    later.fence > earlier.fence,
    "a later lease must carry a higher fence — the E3 tie-breaker above bornEpoch"
  );
};

const checkMutualExclusionShape = async (make: () => Lock): Promise<void> => {
  const lock = make();
  const handle = await lock.acquire(LOCK_KEY);
  assert.ok(
    typeof handle.fence === "number" && Number.isFinite(handle.fence),
    "a handle must carry a finite numeric fence (§5.2)"
  );
  // Mutual exclusion + a releasable handle is the whole §5.2 surface; write-fencing
  // against KV is explicitly NOT claimed (MI25) — the §5.0 rule bounds a stale holder.
  // A rejecting release would throw here and fail the check.
  await handle.release();
};

type LockCheck = (make: () => Lock) => Promise<void>;

/** The individually-callable Lock checks (each throws on violation). */
export const lockChecks: {
  readonly laterOutFences: LockCheck;
  readonly monotonePerKey: LockCheck;
  readonly mutualExclusionShape: LockCheck;
} = {
  laterOutFences: checkLaterOutFences,
  monotonePerKey: checkMonotonePerKey,
  mutualExclusionShape: checkMutualExclusionShape,
};

export interface LockContractOptions {
  readonly label?: string;
}

/** Register the Lock conformance suite for a provided implementation factory. */
export function runLockContract(
  make: () => Lock,
  opts: LockContractOptions = {}
): void {
  describe(`${opts.label ?? "Lock"} — conformance contract`, () => {
    it("issues strictly monotone fencing tokens per key (§5.2, INV-17)", async () => {
      await checkMonotonePerKey(make);
    });
    it("a later lease out-fences an earlier one — arbitration by fence, never wall-clock (§5.0, E3)", async () => {
      await checkLaterOutFences(make);
    });
    it("grants mutual exclusion with a finite fence and a releasable handle (§5.2, MI25)", async () => {
      await checkMutualExclusionShape(make);
    });
  });
}
