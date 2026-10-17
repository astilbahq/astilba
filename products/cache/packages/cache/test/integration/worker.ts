// Integration-lane test worker (scaffold). NOT shipped in the published
// package — it exists only so `@cloudflare/vitest-pool-workers` has a `main` entry
// that (a) re-exports the placeholder `Coordinator` Durable Object class the
// wrangler config binds, and (b) provides a default fetch handler for the isolate.
//
// This worker is the platform host under test; it carries no kernel logic.
//
// IT ALSO HOSTS THE CROSS-REQUEST WITNESS. A `SELF.fetch()` from a test enters this handler in
// a request context of its OWN — measured, not assumed — so two such calls against the ONE instance
// below are two genuinely different request contexts. That is the oracle dimension no test in this
// repo could reach from inside a single `it(...)`: a handle minted under one request and reused
// under the next is exactly what the platform refuses, and exactly what a published cache held
// across requests must never do.

// Re-export so the `durable_objects` binding's `class_name` (Coordinator) resolves
// against this `main` entry, and so tests importing this module share the exact
// class instance the runtime binds.
export { Coordinator } from "../../src/adapters/cloudflare/coordinator";

import { t } from "@astilba/cache";
import { createWorkersCache } from "@astilba/cache/cloudflare";
import { env } from "cloudflare:workers";

/** ONE cache per isolate, at module scope — the placement, held across every request
 * below. Under this pool the module is evaluated lazily inside whichever request first reaches it,
 * so this placement does NOT witness the global-scope law (that is `scripts/module-scope-boot.mjs`,
 * on `wrangler dev`); what it witnesses here is REUSE — the instance outliving the request that
 * built it. */
const cache = createWorkersCache({
  name: "e28-reuse",
  kv: env.CACHE_KV,
  coordinator: env.COORDINATOR,
});

/** The tag every witness route shares, so `expire` in one request is visible to a read in the next. */
const BOOK_TAG = t`book:e28`;

/** Fill counters, per key — module state, so a test can see whether a LATER request refilled. */
const fills = new Map<string, number>();

function factoryFor(key: string): () => Promise<string> {
  return async (): Promise<string> => {
    const n = (fills.get(key) ?? 0) + 1;
    fills.set(key, n);
    return `v${n}`;
  };
}

/** Run one witness route, reporting a throw as its own body so the TEST names the platform refusal
 * rather than seeing an opaque 500. */
async function run(body: () => Promise<string>): Promise<Response> {
  try {
    return new Response(await body());
  } catch (error: unknown) {
    return new Response(`THREW ${String(error)}`, { status: 500 });
  }
}

export default {
  async fetch(request: Request): Promise<Response> {
    const url = new URL(request.url);
    const key = url.searchParams.get("key") ?? "e28/k";
    switch (url.pathname) {
      case "/e28/get": {
        return await run(async () =>
          cache.getOrSet({ key, tags: [BOOK_TAG], factory: factoryFor(key) })
        );
      }
      case "/e28/strong": {
        return await run(async () =>
          cache.getOrSet({
            consistency: "strong",
            factory: factoryFor(key),
            key,
            tags: [BOOK_TAG],
          })
        );
      }
      case "/e28/expire": {
        return await run(async () => {
          const result = await cache.expire({ tag: BOOK_TAG });
          return `epoch ${result.epoch}`;
        });
      }
      default: {
        return new Response("astilba-cache integration test worker", {
          status: 200,
        });
      }
    }
  },
} satisfies ExportedHandler;
