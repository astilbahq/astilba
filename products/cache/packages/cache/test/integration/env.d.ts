// Ambient types for the integration lane. Pulls in the `cloudflare:test` module
// surface (env, runInDurableObject, …) shipped by @cloudflare/vitest-pool-workers;
// the `cloudflare:workers` runtime globals + the typed `Env` come from the adjacent
// generated `worker-configuration.d.ts` (via `wrangler types`).
/// <reference types="@cloudflare/vitest-pool-workers/types" />
