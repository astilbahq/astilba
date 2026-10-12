---
title: Cache fundamentals
description: Learn the Astilba Cache vocabulary from a basic read through distributed invalidation.
---

You can understand Astilba Cache without starting with its distributed machinery. Begin with a key, a factory, and a Store. Add the remaining components only when an application spreads across isolates, machines, and shared caches.

## Begin with the read path

| Term | Plain-language meaning |
| --- | --- |
| **Cache instance** | The object returned by <code>createCache()</code>. Application code reads and invalidates through it. |
| **Key** | The application-facing name of one result, such as <code>product:sku-123</code>. |
| **Factory** | The async function that loads the value on a miss. The database or upstream API it calls is the **origin** or **source of truth**. |
| **Hit** | Cache found a stored value that is legal to serve. |
| **Miss** | Cache could not reuse a stored value, so it runs the factory or reports a miss outcome. |
| **Entry** | A stored value plus metadata such as its tags, scope, codec identity, and invalidation epoch. |
| **Namespace** | A stable boundary around one cache domain. <code>clear()</code> advances that namespace so older keys become unreachable. |
| **Tag** | A dependency label. Several keys can carry the same tag and be invalidated together. |
| **Scope** | A sharing rule: public, tenant-scoped, or principal-derived and local-only. |

<code>getOrSet()</code> returns only the value. <code>getOrSetEntry()</code> also reports where it came from, its elapsed age, whether it was stale or skipped, and whatever durability this serve can prove.

## Understand the storage tiers

The tier names describe where a value lives, not different value formats.

| Tier | What it is | Typical use |
| --- | --- | --- |
| **L1** | An optional Store local to one process or worker isolate. Other instances cannot see it. | Fast repeat reads and retention of principal-derived values that must not enter shared storage. |
| **L2** | A Store shared or durable across calls and, depending on the driver, across instances. | The main reusable server-side copy. The current kernel requires L2 whenever a factory must run. The source walkthrough uses <code>memory()</code> in this slot only as a non-durable demonstration. |
| **L3** | A shared HTTP or CDN response cache outside the value-store path. | Caching rendered responses. The React Router adapter can emit safe dependency tags; CDN purge delivery is not implemented. |
| **Origin** | The factory result before or while it is written to a Store. | The database or upstream request supplied by the application. |

Both L1 and L2 implement the same <code>Store</code> contract. A runtime decides whether that Store is an in-memory map, a platform KV service, Redis, or another backend.

## Separate values from invalidation

Storage answers “do I have bytes for this key?” Invalidation answers “are those bytes still legal to serve?” Astilba Cache keeps those questions separate.

| Component | Responsibility |
| --- | --- |
| **Registry** | The authoritative record of soft and hard invalidation watermarks. Purge methods write to it; strong stored-entry reads check it live. |
| **Bus** | The fast delivery path for ordered invalidation events to active cache instances. A reset or gap makes local knowledge suspect; an explicit lost event records channel reachability separately. |
| **Replication mirror** | Durable L2 pointer, delta, and snapshot objects that let a suspect instance recover invalidation changes it missed on the Bus. |
| **Recovery reader** | Replays contiguous deltas first, then can use the pointer-blessed snapshot as a recovery floor when a persistent hole exhausts the delta retry budget. It remains fail closed if the chain cannot be established. |
| **Replication poller** | Drives baseline mirror observation and bounded recovery retries outside foreground value work. The Workers factory supplies read-triggered ticks, React Router can adopt request-entry ticks, and raw runtimes need an equivalent driver. |

The Bus is not the authority. It transports updates; the Registry and verified local or mirror history establish what is known. Coordinated read validation is built only when Registry, Bus, and L2 are all available. <code>createCache()</code> rejects a Registry-plus-Bus configuration without L2 because that reader would have no safe recovery mirror.

## Know the injected capabilities

| Capability | Why it exists | Do basic callers usually choose it? |
| --- | --- | --- |
| **Store** | Reads and writes encoded values or replication objects. | Use <code>memory()</code> locally or receive a driver from a runtime integration. |
| **Clock** | Supplies logical time without hard-coding a platform clock into the portable kernel. | The current API requires it; a runtime preset should normally provide it. |
| **Rng** | Supplies randomness through an explicit, testable source. | The current API requires it; a runtime preset should normally provide it. |
| **Codec** | Encodes values and identifies their wire format before decoding. | Only when the default JSON round trip is insufficient. |
| **Lock** | Coordinates opted-in fills across instances and supplies a fencing token. | Only for cross-instance contention. |
| **Cdn** | Accepts shared HTTP-cache purge work. | The contract exists, but no implementation is wired today. Response-tag emission does not call it. |
| **Telemetry sink** | Receives cache events; hosted mode pseudonymizes string fields with a project salt. | Only when operating or observing Cache. |

Clock and Rng exist for portability and deterministic tests. They are construction details, not concepts application code should need on every read.

## Add components progressively

| Configuration | What it gives you today |
| --- | --- |
| <code>clock</code> + <code>rng</code> + <code>l2</code> | Basic read, fill, and reuse without coordinated invalidation. The source walkthrough uses <code>memory()</code> for this preview-only shape. |
| Add <code>l1</code> | Process-local reads and retention for values that cannot be shared. |
| Add <code>registry</code> | Enables the purge methods, but does not create coordinated read validation by itself. |
| Add <code>registry</code> + <code>bus</code> alongside the existing <code>l2</code> | Live invalidation delivery, channel-state tracking, coordinated validation, snapshot-capable mirror recovery, and an attached replication poller. The embedding runtime or adapter must supply a tick driver for background recovery; reads still perform reactive recovery without one. |
| Add <code>lock</code> | Lets individual calls opt into cross-instance fill exclusion. |
| Use React Router render collection | Automatically records served Cache entries, rejects non-public response dependencies, and emits eligible user tags. |
| Add <code>cdn</code> | Declares the L3 purge boundary; the current implementation does not drive it. |
| Use <code>createWorkersCache()</code> | Composes memory L1, KV L2, lazily addressed Coordinator Registry, tick-redialed Bus, and a read-triggered recovery carrier with Workers defaults. |

## Distinguish policy terms

- **TTL** is how long a value should be fresh by elapsed time. Duration values and the <code>duration()</code> builder are implemented, but the cache does not enforce elapsed TTL yet.
- **Grace** is the period in which an eligible stale value may be reused after a classified transient failure. Its duration is not enforced yet.
- A **negative entry** remembers an opted-in 404 without inventing a value. An invalidation-fresh negative resolves as <code>undefined</code>; it is never served through grace or stale-on-error. Before writing one, Cache checks L2 and refuses when it finds a decodable value whose invalidation verdict is fresh or stale, or cannot be established. Its declared duration is not enforced yet.
- **Eventual consistency** uses verified local invalidation knowledge and fails closed when that knowledge is insufficient.
- **Strong consistency** performs a live Registry check before serving a stored entry and before running the factory for a strong miss when coordinated invalidation is active.
- **Soft invalidation** makes an older value stale.
- **Hard invalidation** makes an older value unreadable.
- **Singleflight** lets compatible callers in one instance share one foreground factory execution.
- **Fencing** prevents a result produced across a conflicting hard invalidation from being accepted as current. The current fill path can re-mint its birth epoch and refetch within a bounded three-attempt budget when verified knowledge advances.

Continue with the [source walkthrough](/docs/cache/quickstart/) for a concrete read, [Cloudflare Workers](/docs/cache/cloudflare-workers/) for the composed runtime, [Cache HTTP responses](/docs/cache/response-caching/) for L3 collection, [How Cache works](/docs/cache/how-it-works/) for the complete sequence, or [runtime architecture](/docs/cache/architecture/) for the capability contracts.
