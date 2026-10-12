---
title: Cloudflare Workers
description: Compose the current Cache source with Cloudflare KV, a Coordinator Durable Object, and the Workers factory.
---

The current source exposes a Cloudflare entry point at <code>@astilba/cache/cloudflare</code>. Its <code>createWorkersCache()</code> factory combines the portable kernel with a bounded memory L1, Cloudflare KV L2, a Coordinator Durable Object, a live WebSocket Bus, and request-driven recovery behavior.

:::caution[Implemented in source, not released]
The subpath is part of the reviewed source snapshot and publish configuration, and its primary path runs under workerd integration tests. The package is still absent from npm, elapsed TTL is incomplete, and the deployment has not completed its production-measurement and release gates.
:::

## See what the factory owns

You provide one stable registry name and two Cloudflare bindings:

~~~ts title="cache.server.ts"
import { env } from "cloudflare:workers"
import { createWorkersCache } from "@astilba/cache/cloudflare"

export const cache = createWorkersCache({
  name: "storefront",
  kv: env.CACHE_KV,
  coordinator: env.COORDINATOR,
  telemetry: (event) => console.info(event),
})
~~~

Create this instance once at module scope. Construction performs no I/O: the factory captures the Coordinator namespace and name as an address recipe, then mints a request-scoped stub each time Registry or Bus work needs one. Retention registration, the first Bus dial, polling, and any later redial happen lazily from request activity.

The Worker entry must separately export the Durable Object class so Wrangler can bind it:

~~~ts title="worker.ts"
export { Coordinator } from "@astilba/cache/cloudflare"

export default {
  async fetch(): Promise<Response> {
    return new Response("Worker ready")
  },
} satisfies ExportedHandler
~~~

The <code>name</code> is one identity, not a display label. The factory uses it as:

- the named Coordinator Durable Object address;
- the Registry and replication-mirror identifier;
- the Cache namespace used in canonical keys.

Keep it stable for the lifetime of the cache domain. Changing it addresses a different Durable Object and a different keyspace.

The factory also supplies:

- a Workers wall-clock <code>Clock</code> and random <code>Rng</code> at the platform boundary;
- <code>memory({ clock, maxEntries: 512, maxBytes: 5_000_000 })</code> as L1;
- <code>cloudflareKV()</code> as L2;
- <code>doRegistry()</code> with a thunk that mints a Coordinator stub per use;
- a <code>doBus()</code> connection whose backed-off redials are performed by request-time ticks rather than timers;
- a carrier on <code>getOrSet()</code> and <code>getOrSetEntry()</code> that drives the poller and any due Bus redial at most once per second without awaiting that work;
- eventual consistency, live Registry checks for unknown knowledge, the default HTTP retry classifier, and a 30-second reader heartbeat interval unless you override those fields in <code>defaults</code>.

The only required configuration fields are <code>name</code>, <code>kv</code>, and <code>coordinator</code>. Optional <code>defaults</code> override policy, <code>telemetry</code> observes kernel and carrier events, and <code>takedownSensitive</code> makes unknown invalidation knowledge throw rather than refill. The factory's internal memory L1 does not have a separate telemetry option for <code>private_evicted</code>.

## Configure the Worker bindings

The Worker must export <code>Coordinator</code>, bind that class as a SQLite-backed Durable Object, and make one KV namespace visible under two binding names:

~~~jsonc title="wrangler.jsonc"
{
  "$schema": "./node_modules/wrangler/config-schema.json",
  "name": "storefront-worker",
  "main": "src/worker.ts",
  "compatibility_date": "2026-07-15",
  "compatibility_flags": ["nodejs_compat"],

  "durable_objects": {
    "bindings": [
      { "name": "COORDINATOR", "class_name": "Coordinator" }
    ]
  },

  "exports": {
    "Coordinator": {
      "type": "durable-object",
      "storage": "sqlite"
    }
  },

  "kv_namespaces": [
    { "binding": "CACHE_KV", "id": "<your-kv-namespace-id>" },
    { "binding": "REGISTRY_KV", "id": "<your-kv-namespace-id>" }
  ],

  "vars": {
    "REGISTRY_HEARTBEAT_MS": "30000"
  }
}
~~~

Both KV bindings point to the same namespace. <code>CACHE_KV</code> is the L2 Store the reader sees; <code>REGISTRY_KV</code> is where the Coordinator writes replication pointers, deltas, and snapshots. If they point at different namespaces, the reader cannot recover from the mirror the Coordinator produced.

New deployments should declare the class lifecycle in the top-level <code>exports</code> map. Cloudflare still supports the older <code>migrations</code> array with <code>new_sqlite_classes</code>, and current internal fixtures may still use it, but the two forms are mutually exclusive. Follow Cloudflare's [Durable Object class exports guide](https://developers.cloudflare.com/durable-objects/reference/durable-objects-migrations/) before adapting an existing deployment.

The <code>nodejs_compat</code> flag is required because the root package uses <code>node:crypto</code>. Use a compatibility date of 2024-09-23 or later, as required by Cloudflare's [Node.js compatibility guide](https://developers.cloudflare.com/workers/runtime-apis/nodejs/). The flag also supplies the AsyncLocalStorage support used by the React Router adapter. The narrower <code>nodejs_als</code> flag alone is not sufficient to boot the package.

## Decide whether to enable idle heartbeats

<code>createWorkersCache()</code> configures the reader to expect a 30-second heartbeat interval. The Coordinator cannot set its own deployment variables, so you must separately opt its idle heartbeat in with:

~~~jsonc
"vars": { "REGISTRY_HEARTBEAT_MS": "30000" }
~~~

Leaving the variable unset keeps idle heartbeats dormant. That avoids a recurring Durable Object alarm and KV write for every idle registry, but a reader can become conservatively suspicious and pay for live checks until it reconverges. Setting it to <code>30000</code> matches the factory's reader default.

If you override <code>defaults.heartbeatInterval</code>, update the deployment variable deliberately as well. The two settings are not synchronized by the library.

## Use the cache in a Worker

Once constructed, application code uses the same portable API:

~~~ts title="load-product.ts"
import { t } from "@astilba/cache"
import { cache } from "./cache.server"

export async function getProduct(productId: string) {
  return cache.getOrSet({
    key: `product:${productId}`,
    tags: [t`product:${productId}`],
    factory: ({ signal }) => loadProduct(productId, signal),
  })
}
~~~

After updating the source of truth, invalidate the same dependency:

~~~ts
await saveProduct(productId, input)
await cache.delete({ tag: t`product:${productId}` })
~~~

The mutation reaches the authoritative Coordinator. Active isolates receive live Bus events; suspect readers can recover through the KV mirror. A strong read performs a live check before serving a stored entry and before filling a strong miss.

Cloudflare KV failures use the same classified Store boundary as the kernel. A classified KV read failure emits <code>store_read_suppressed</code> and behaves as a miss for that tier. The factory's memory L1 absorbs the outage for values it already holds and for successful refills; without an L1, a sustained L2 outage could force every call back to origin.

## Understand the local chaos evidence

The reviewed source snapshot includes an unpublished React Router v8 app under <code>apps/demo</code>. It is not hosted or publicly downloadable. The app runs <code>createWorkersCache()</code> against local KV and Coordinator bindings, then places demo-owned wrappers between those bindings and the library. Cache itself has no fault-injection API.

The app contains three scenes:

| Scene | Injected fault | What to inspect |
| --- | --- | --- |
| Backend kill | The demo KV wrapper rejects every read and write. | A cold or missing L1 is required to exercise the failing KV path; a warm L1 can bypass KV. In an internal run, use a cold key or empty local state, or rely on the wrapper counters. Classified L2 reads become observable misses, successful origin work can refill L1, suppressed write-back remains non-durable, and <code>explain()</code> distinguishes <code>read-failed</code> from absence. |
| Bus drop | The demo refuses future Coordinator WebSocket dials. | A reader created while the fault is armed reports <code>never-established</code> and emits <code>bus_dial_failed</code>. The fault does not sever an existing socket. A purge issued by the same reader is learned from its own acknowledgement, so the scene does not mislabel it as cross-isolate polling rescue. |
| Bus and mirror loss (labelled “Registry outage” in the app) | The demo refuses future Bus dials and makes replication-mirror key reads fail. | Live Registry RPC remains available, and an already-established socket may remain connected. In the Vite development rig the channel was already <code>never-established</code>, so the scene removes both warm invalidation inputs there. Unknown knowledge follows the configured fail-closed posture and may pay for a live Registry check. |

The Vite development rig uses <code>@cloudflare/vite-plugin</code>; the built rig launches the generated Worker through Wrangler. Both use Cloudflare's local Workers tooling and run Worker code in workerd through Miniflare. In the reviewed local evidence, the WebSocket hello did not complete in the Vite development rig, while the built Wrangler rig established the channel. Treat that as an observed rig difference, not a claim that Vite runs outside workerd.

A required source CI lane now holds the built-rig claim. It builds the demo, boots the emitted Worker with its real local Coordinator and KV bindings, and reads the channel from a narrow JSON status route rather than scraping page markup. The status reports the armed scene, channel, dial-failure count, event names, and any witness error.

After a boot budget of 90 seconds, each channel arm polls that status every 500 milliseconds for at most 30 seconds; the scene action has its own 30-second budget. The healthy arm must reach <code>established</code>. The Bus-drop fault is then armed before the app creates a fresh scene-owned reader whose dial is refused. That armed arm must report <code>never-established</code> plus at least one <code>bus_dial_failed</code> event. It is a negative control: it proves the status instrument can distinguish a refused connection from a healthy one.

A non-200 response, malformed status, witness error, stalled body, or budget that expires during a read is a probe failure and says nothing about the channel. If completed status reads continue until the budget ends without reaching the required state, the lane instead records <code>not-within-budget</code>: a channel verdict and a failed arm.

The lane proves that the composed demo as built can expose those two states on local workerd. It proves neither Cache kernel semantics nor Bus-mechanism correctness; the invariant and integration lanes own those questions. It also proves no propagation latency, production availability, or deployed SLO. Deployed probes remain absent.

The internal app reduces <code>maxSyncLag</code> from the factory's 60-second baseline to five seconds so request-driven polling is watchable locally. It prints what happened beside what the configuration permits and asserts no timing or consistency SLO. No deployed measurements have been taken; those remain a release gate.

## Know the operational boundary

The source path currently includes:

- KV value-size rejection and write-failure classification;
- Coordinator command journaling, coalesced flushes, snapshots, and Registry RPC;
- WebSocket Bus delivery with scope checks, explicit lost-channel reporting, and tick-driven jittered redial backoff;
- reactive read-path recovery plus an out-of-band polling state machine;
- a factory-owned request-driven carrier for plain Worker reads;
- optional React Router lifecycle adoption of middleware ticks through <code>waitUntil</code>;
- the source-only three-scene chaos evidence app described above;
- the required composed-demo workerd boot witness and its armed negative control.

It does not yet provide:

- an npm release or supported upgrade policy;
- elapsed TTL, grace, or negative-entry expiry enforcement—entry age is measured for observability but does not enforce policy;
- journal checkpointing and truncation for a long-lived Coordinator;
- a production Lock or CDN purge driver;
- an end-to-end CDN purge path, even though the React Router adapter now emits safe <code>Cache-Tag</code> headers;
- deployed consistency, propagation, caching, and production-threshold measurements that complete the Workers release path.

Continue with [React Router](/docs/cache/react-and-server-apps/) if that is your server framework, [Cache HTTP responses](/docs/cache/response-caching/) for the response-tag safety model, [Driver implementations](/docs/cache/drivers-and-status/) for component-level status, or [Implementation status](/docs/cache/api-status/) for kernel limitations.
