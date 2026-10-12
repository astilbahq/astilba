// C4 exit gate — the spec-surface method sweep found `FactoryCtx.setTtl` (§2.1) was a SILENT
// NO-OP: a spec-named method that did nothing. Disposition-law forbids silent stubs — a method
// either works or throws typed. `setTtl` is deferred to v0.4 (its TTL-override semantics are a
// v0.4 design slot, alongside `reuseGraced`/`deleteAll`/`expireAll`), so it now throws a typed
// `NotImplementedError` naming that target. This tooth pins the throw between gates, so the
// deferral cannot silently regress back to a no-op without reddening a test.

import assert from "node:assert/strict";

import { createCache, memory, NotImplementedError } from "@astilba/cache";
import { describe, expect, it } from "vitest";

describe("FactoryCtx.setTtl — deferred to v0.4 (typed, not silent)", () => {
  it("calling ctx.setTtl inside a factory throws NotImplementedError naming v0.4", async () => {
    const cache = createCache({
      clock: { now: (): number => 0 },
      l2: memory(),
      namespace: "setttl",
      rng: { next: (): number => 0 },
    });

    let err: unknown;
    try {
      await cache.getOrSet({
        factory: async (ctx) => {
          ctx.setTtl("60s");
          return await Promise.resolve("v");
        },
        key: "k",
      });
    } catch (error) {
      err = error;
    }

    // `assert.ok` narrows to NotImplementedError (no cast); its message names the v0.4 target.
    assert.ok(err instanceof NotImplementedError);
    expect(err.message).toContain("v0.4");
    expect(err.message).toContain("setTtl");
  });
});
