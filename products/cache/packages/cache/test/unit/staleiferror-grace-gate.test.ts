// The classifier is USER-supplied (`defaults.staleIfError`), so the kernel must not invoke it
// on a call where no graced serve is possible. A call declaring no `grace` is exactly that: no
// waiter on the fill can serve a stale copy (`grace` rides the singleflight join identity, so every
// waiter declared the same), and the factory error rethrows whatever the classifier would have said.
// Running app code on a path that cannot use its answer is a side effect the caller never asked for
// — and a classifier that throws would surface from a call that declared no interest in grace.
//
// ORDINARY regression (not sacred): a callback-invocation contract, not a numbered safety
// invariant (the remit line — safety is sacred's province). Both directions are pinned, so the
// gate cannot degenerate into "never classify" either.

import { HttpError } from "@astilba/cache";
import { mkKey, mkTag, world } from "@astilba/harness";
import { describe, expect, test } from "vitest";

/** A retriable origin failure (503 ∈ the default retriable set). */
const retriableFail = async (): Promise<never> => {
  throw new HttpError(new Response(null, { status: 503 }));
};

/** A world whose classifier counts its own invocations, so a test can assert it never ran. */
function countingWorld(counter: { calls: number }): ReturnType<typeof world> {
  return world({
    defaults: {
      staleIfError: (err: unknown): boolean => {
        counter.calls += 1;
        return err instanceof HttpError;
      },
    },
    isolates: 1,
  });
}

describe("§5.4: the user's classifier runs only where a graced serve is possible", () => {
  test("a call declaring NO grace never invokes staleIfError — the error rethrows without consulting it", async () => {
    const counter = { calls: 0 };
    const w = countingWorld(counter);
    const key = mkKey("k");
    const tag = mkTag("t0");

    // A cold key: the fill runs, the origin fails retriably, and NO grace is declared — so no
    // graced serve is reachable however the failure classifies.
    await expect(
      w.isolate(0).cache.getOrSet({ factory: retriableFail, key, tags: [tag] })
    ).rejects.toBeInstanceOf(HttpError);

    // THE TOOTH: the user's predicate was never called. Removing the declared-grace gate on the
    // HTTP classification invokes it here, which is app code running on a path that cannot use it.
    expect(counter.calls).toBe(0);
  });

  test("a call declaring grace DOES invoke staleIfError, and its graced copy is served", async () => {
    const counter = { calls: 0 };
    const w = countingWorld(counter);
    const key = mkKey("k");
    const tag = mkTag("t0");
    await w
      .isolate(0)
      .cache.getOrSet({ factory: async () => "good", key, tags: [tag] });
    await w.isolate(0).cache.expire({ tag }); // soft — the copy stays servable

    // The complement, so the gate cannot degenerate into "never classify": with grace declared the
    // classifier is consulted, and its `true` is what turns the failure into a stale-if-error serve.
    const served = await w.isolate(0).cache.getOrSet({
      factory: retriableFail,
      grace: "1h",
      key,
      tags: [tag],
    });
    expect(served).toBe("good");
    expect(counter.calls).toBeGreaterThan(0);
  });
});
