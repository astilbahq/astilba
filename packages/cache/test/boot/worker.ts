// The boot witness worker. Its MODULE SCOPE is real global scope: `wrangler dev` evaluates this
// module when the isolate starts, before any request exists.
export { Coordinator } from "../../src/adapters/cloudflare/coordinator";

import { createWorkersCache } from "../../src/adapters/cloudflare";
import { t } from "../../src/index";
import { env } from "cloudflare:workers";

// ┌── Hero form, at the placement the hero block depicts: worker.ts module scope ──
const cache = createWorkersCache({
  name: 'journals',                       // one name ⇒ DO address, registry id, key namespace
  kv: env.CACHE_KV,
  coordinator: env.COORDINATOR,
})
// └────────────────────────────────────────────────────────────────────────────────

export default {
  async fetch(request: Request): Promise<Response> {
    const url = new URL(request.url);
    const key = url.searchParams.get("key") ?? "k";
    try {
      const value = await cache.getOrSet({
        key,
        tags: [t`book:boot`],
        factory: async () => "served",
      });
      return new Response(`OK ${value}`);
    } catch (error: unknown) {
      return new Response(`THREW ${String(error)}`, { status: 500 });
    }
  },
} satisfies ExportedHandler;
