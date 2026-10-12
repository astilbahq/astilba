// Bun portability smoke suite (P3) — bun:test surface only, NO fast-check.
// Phase-1 proof: the kernel's ESM output loads + resolves on the Bun runtime, and
// entry points throw NotImplementedError SPECIFICALLY. Outside the sacred set;
// excluded from both vitest projects (P2). Expands to real behavior at RED→GREEN.

import { expect, test } from "bun:test";
import {
  compound,
  createCache,
  duration,
  globalTag,
  HttpError,
  isRetriableHttp,
  memory,
  NotImplementedError,
  t,
} from "@astilba/cache";

test("the public API resolves on the Bun runtime", () => {
  expect(typeof createCache).toBe("function");
  expect(typeof memory).toBe("function");
  expect(typeof t).toBe("function");
  expect(typeof compound).toBe("function");
  expect(typeof globalTag).toBe("function");
  expect(typeof duration).toBe("function");
  expect(typeof isRetriableHttp).toBe("function");
});

test("entry points throw NotImplementedError specifically", () => {
  expect(() => globalTag("global")).toThrow(NotImplementedError);
});

// memory() went green as the bounded in-memory L1 Store (§8); it is no longer a
// throwing stub, so — like t()/duration()/compound() — it moves to a resolves check.
test("memory() resolves on the Bun runtime as an L1 Store (§8)", () => {
  const store = memory({ maxEntries: 512, maxBytes: 5_000_000 });
  expect(typeof store.get).toBe("function");
  expect(typeof store.set).toBe("function");
  expect(typeof store.delete).toBe("function");
});

// compound() went green first (INV-22, INV-29 — E10 injective encoding); it is no
// longer a throwing stub, so it moves out of the NotImplementedError block above.
test("compound() resolves on the Bun runtime (INV-22, INV-29)", () => {
  expect(compound("book", "1")).toBe("2|book|1");
});

// t() went green with the tag trust-boundary builder (§2.3, E10 — validate-and-reject);
// duration() with the F31 post-multiplication guard (§2.1). Both leave the stub block.
test("t() and duration() resolve on the Bun runtime (§2.3, §2.1)", () => {
  expect(t`book:1`).toBe("book:1");
  expect(duration(5, "m")).toBe("5m");
});

// isRetriableHttp() went green with the error-path grace / negative-caching cluster
// (INV-3, INV-7, INV-18, INV-26 — §5.4, E2); it is no longer a throwing stub.
test("isRetriableHttp() resolves on the Bun runtime (§5.4, E2)", () => {
  expect(isRetriableHttp(new HttpError(new Response(null, { status: 503 })))).toBe(
    true
  );
  expect(isRetriableHttp(new HttpError(new Response(null, { status: 404 })))).toBe(
    false
  );
  expect(isRetriableHttp(new Error("x"))).toBe(false);
});
