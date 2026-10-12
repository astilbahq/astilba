---
title: Runtime architecture
description: See how the portable Astilba Cache kernel composes storage, coordination, time, and encoding capabilities.
---

Astilba Cache keeps cache semantics in a portable kernel and receives runtime capabilities through typed contracts. The kernel owns keys, scopes, tier order, decode safety, fill coordination, and invalidation decisions. A runtime supplies storage, coordination, time, randomness, recovery ticks, and any custom encoding.

This page explains the runtime capability model. For application-level definitions and a smaller starting point, begin with [Cache fundamentals](/docs/cache/core-concepts/) or the [source walkthrough](/docs/cache/quickstart/).

## Capabilities at the boundary

| Capability | What Cache uses it for | Current requirement |
| --- | --- | --- |
| <code>Clock</code> and <code>Rng</code> | Injected time and randomness keep the kernel portable and deterministic in tests. | Required by <code>createCache()</code>; a future runtime preset should normally supply them. |
| <code>Store</code> | Key/value I/O for local L1, shared L2, and the replication mirror. Classified throttled or unavailable reads become observable misses; unclassified failures propagate. | L2 is required whenever a factory must run. <code>memory()</code> and <code>cloudflareKV()</code> are implemented source drivers. |
| <code>Registry</code> | The authoritative record of soft, hard, and namespace invalidation. | Required by <code>expire()</code>, <code>delete()</code>, and <code>clear()</code>. |
| <code>Bus</code> | The live delivery path for ordered invalidation events, including explicit channel loss. | Participates in coordinated read validation when Registry and L2 are also configured. The Cloudflare subpath includes a Durable Object Bus and tick-driven redial wrapper. |
| <code>Codec</code> | Value encoding and a wire identity checked before decode. | Optional when the built-in JSON round trip is sufficient. |
| <code>Lock</code> | Cross-isolate exclusion and write arbitration. | Optional and only used when a driver is supplied and the call opts in. |
| Render collector | Records value dependencies and decides whether a response may carry their user tags. | The React Router middleware binds and commits it automatically. |
| <code>Cdn</code> | A future edge-purge boundary. | Declared but not wired in the current kernel; response-tag emission does not invoke it. |
| Poll tick driver | Calls the kernel's attached recovery poller outside the foreground read work. | <code>createWorkersCache()</code> carries read-triggered ticks; React Router can start and lifecycle-adopt request-entry ticks. Raw embeddings need an equivalent driver if they want proactive recovery. |

These contracts keep the correctness rules independent of a storage vendor. They do not make every driver combination equivalent: coordinated invalidation needs a complete coordination path.

## Follow one read or fill

1. Cache resolves the namespace, scope, and user key into one canonical storage key.
2. It checks L1 before L2, verifies the stored codec identity, and reconstructs the entry.
3. When coordinated invalidation is configured, it decides whether its tag knowledge is sufficient for the requested consistency level. A strong miss performs a live check before its factory.
4. On a miss, compatible callers share one in-isolate factory execution.
5. When the factory settles, Cache validates the union of call-level and factory-declared tags.
6. Before write-back, Cache checks the final tag set for a conflicting hard invalidation, then writes only to tiers allowed by the resolved scope.

See [Read and cache values](/docs/cache/reading-and-filling/) for return metadata, singleflight compatibility, codec changes, and fill failures. See [How Cache works](/docs/cache/how-it-works/) for the invalidation and recovery path around the same operation.

## Compose the current source preview

| Configuration | Current behavior |
| --- | --- |
| <code>clock</code> + <code>rng</code> + <code>l2</code> | Reads and fills through the portable kernel without coordinated invalidation. |
| Add <code>l1</code> | Adds an isolate-local read tier and retains principal-derived, L1-only values. |
| Add <code>registry</code> | Enables the purge methods. Reads do not build the coordinated invalidation path without Bus. |
| Add <code>registry</code> + <code>bus</code> alongside <code>l2</code> | Enables coordinated validation, live delivery, explicit channel-state tracking, delta-and-snapshot recovery, and construction of the internal replication poller. A Registry-plus-Bus configuration without L2 is rejected. |
| Add <code>lock</code> | Allows opted-in calls to coordinate work across isolates. No production Lock driver is exported. |
| Add a custom <code>codec</code> | Changes the wire format and identity. Accepted older identities must still be decodable by that codec. |
| Run through <code>cacheMiddleware()</code> | Binds a request-scoped render collector, records served entries with scope evidence, emits eligible response tags, and demotes unsafe responses. |
| Use <code>createWorkersCache()</code> | Supplies Clock, Rng, bounded memory L1, KV L2, lazily minted Coordinator Registry handles, a tick-redialed Durable Object Bus, and a read-triggered recovery carrier with safe source defaults. |

Supplying Bus without Registry does not build the invalidation reader. Registry without Bus can still accept purge commands, but it is not a coordinated read configuration. Supplying Registry and Bus without L2 throws at construction because the reader would have no recovery mirror.

Both <code>createCache()</code> and <code>createWorkersCache()</code> construct without I/O. Coordinated lifecycle work is deferred until a read or purge needs it. The Workers factory can therefore live at module scope while minting Durable Object stubs inside the request that uses them.

## Keep runtime integrations outside the kernel

The reviewed source snapshot exposes two publish-shaped adapter entry points:

- <code>@astilba/cache/cloudflare</code> contains the Workers factory plus its KV, Coordinator, Registry, Bus, tick-driven redial, and request carrier pieces.
- <code>@astilba/cache/react-router</code> contains React Router v8 server middleware, typed Cache context, request-frame access, request-entry recovery lifecycle adoption, and scope-aware response-tag collection.

Both are part of the package export map and covered by source tests. Neither is installable from npm yet, and the integration worker and React Router fixture remain test hosts rather than application templates.

:::caution[Source preview only]
These combinations describe the reviewed source snapshot. <code>@astilba/cache</code> is not published and does not yet have a public checkout, supported installation, compatibility policy, or production deployment path.
:::

## Related

- [Driver implementations](/docs/cache/drivers-and-status/) lists each contract and integration boundary.
- [Consistency and resilience](/docs/cache/consistency-and-resilience/) explains live checks, unknown knowledge, and stale-on-error policy.
- [Cache HTTP responses](/docs/cache/response-caching/) explains the render collector and response-header gate.
- [Implementation status](/docs/cache/api-status/) records incomplete and provisional surfaces.
