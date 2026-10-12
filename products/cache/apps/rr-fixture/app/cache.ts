import { createCache, type Cache } from "@astilba/cache";

// A minimal build-smoke cache (no L2/registry/bus drivers). The fixture proves the BUILD — that
// real Vite 8 compiles the RR adapter + kernel — not runtime I/O, so a driverless instance is all
// the middleware needs to compile against. clock/rng are injected (the app boundary, not grep-guarded).
export const cache: Cache = createCache({
  namespace: "rr-fixture",
  clock: { now: () => Date.now() },
  rng: { next: () => Math.random() },
});
