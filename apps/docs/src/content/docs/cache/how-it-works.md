---
title: How Cache works
description: Follow one value through storage, invalidation, recovery, and a safe refill.
---

Astilba Cache separates the path that stores values from the path that decides whether those values are still legal to serve. If L1, L2, Registry, or Bus are unfamiliar, read [Cache fundamentals](/docs/cache/core-concepts/) first.

| Plane | Components | Responsibility |
| --- | --- | --- |
| Value path | Cache kernel, local L1 Store, shared L2 Store, factory, Codec | Find, validate, fill, and store a value. |
| Invalidation path | authoritative Registry, delivery Bus, L2 mirror, replication reader | Mint invalidation order and distribute tag watermarks to every active instance. |

The separation is deliberate. A fast storage hit is useful only if the reader can also establish that the entry has not been invalidated.

## Follow one read

1. **Resolve a canonical key.** Cache combines the namespace version, resolved scope, and user key. Public, tenant, and principal-derived values therefore occupy different storage keys.
2. **Read L1, then L2.** L1 is an optional isolate-local <code>Store</code>. L2 is the shared or durable <code>Store</code> and is currently required whenever a factory must run. A classified throttled or unavailable Store read is observed through telemetry and treated as a miss for serving; an unclassified error propagates.
3. **Check the codec before decoding.** Stored metadata carries a codec identity. A foreign or unsupported identity becomes a miss before its bytes reach the decoder.
4. **Validate invalidation knowledge.** With coordinated invalidation configured, the entry's birth epoch is compared with the soft and hard watermarks for all of its tags.
5. **Serve or fill.** A fresh value entry is returned. A fresh negative entry returns <code>undefined</code>. A soft-stale eventual value follows the refresh path; negatives never do. A dead or unknown entry is not served directly, so the factory runs or an error is surfaced. A strong coordinated miss establishes the Registry's current epoch before the factory starts.
6. **Close and fence the result.** The final stored tag set combines the call and factory declarations. Before write-back, Cache checks those tags for a hard purge delivered during the fill. If verified knowledge advanced, it can re-mint the birth epoch and refetch within a bounded three-attempt budget instead of publishing a born-dead value. If all attempts are fenced, the plain <code>getOrSet()</code> form fails closed with <code>FencedError</code>; <code>getOrSetEntry()</code> returns a non-durable miss.
7. **Write by scope.** Shared public and tenant entries may reach L2. Principal-derived entries are L1-only. A successful fill also hydrates L1 when one is configured.

Compatible foreground fills share one in-isolate promise. A successful shared fill or revalidated stale serve carries the served value and its evidence to callers waiting for that work. If the factory-running call had no stale candidate and shares only a classified transient failure, each waiting caller makes its own stale-on-error decision using the candidate from its earlier read and revalidates that candidate before serving it.

When serve-time validation completes, an opted-in 404 has three singleflight dispositions:

- A factory-running caller with <code>grace</code> and a still-servable stale value suppresses the negative write. Every compatible joiner inherits that value and its evidence.
- If that candidate revalidates as dead or unknown, the leader attempts the negative as the shared result.
- If the leader has no grace-eligible candidate, it attempts one negative L2 write and shares the not-found fact. A waiter with its own grace-eligible, still-servable stale value can return it; one without a servable stale candidate receives <code>undefined</code>.

Without <code>grace</code>, all compatible callers take the negative disposition even if they observed stale values. Validation may instead throw under the configured Registry-unavailable posture.

Before persistence, the L2 negative-write guard reads the existing entry in the same Store with the serving path's codec and invalidation rules. A readable value that is fresh or stale—or whose invalidation verdict cannot be established—refuses the negative. Callers taking the not-found disposition still receive <code>undefined</code>, but the attempted negative is non-durable and the Store remains unchanged.

A negative result that reaches the serve path skips L1 hydration and triggers best-effort removal of any older L1 copy unless the guard refused the write. This cleanup still runs after a classified retryable write failure. A guard refusal leaves L1 alone, while a waiter serving its own candidate retains that candidate's evidence.

Cross-isolate exclusion is separate and opt-in through a <code>Lock</code> driver.

## How invalidation travels

The Registry is the authority. It keeps a monotone epoch and two watermarks per tag:

- a **soft watermark** makes older values stale;
- a **hard watermark** makes older values unreadable.

The Bus is the warm path. It carries ordered frames to active isolates. A reset, declared gap, or non-contiguous frame marks local knowledge suspect. The reader separately records whether the channel has never established, is established, or was lost, so an operator can distinguish transport reachability from continuity suspicion.

The L2 replication mirror stores one mutable pointer plus immutable delta batches and snapshots. A suspect reader first tries a bounded contiguous delta replay. If a persistent hole spends that retry budget, it can load the exact snapshot named by the pointer and then replay the tail above it. Missing, corrupt, foreign, or otherwise unfillable recovery data keeps the reader fail closed after both bounded recovery paths are exhausted.

Recovery has two triggers:

- a suspect read performs one bounded fast resync attempt before classifying the entry;
- an attached replication poller observes pointer liveness and drives bounded retry and snapshot escalation outside the read path.

<code>createWorkersCache()</code> supplies an unawaited carrier on <code>getOrSet()</code> and <code>getOrSetEntry()</code> that drives both the poller and due Bus redials at most once per second. The React Router adapter also ticks at request entry and can adopt that work with <code>waitUntil</code>. An idle isolate receives no ticks, but it also serves no reads; the next Cache value read observes the elapsed state and starts one. A raw runtime embedding must provide its own driver if it wants proactive recovery rather than relying only on reactive read-path recovery.

## How a rendered response earns tags

The React Router middleware binds one render collector to each request. When Cache serves a hit or completes a fill inside that frame, it records the entry's tags and scope evidence. After route work completes, the middleware commits the collector:

1. Any tenant, principal, or unreadable scope makes the response private and removes response tags.
2. A late or over-budget tag also makes the response private.
3. An eligible public render receives a deduplicated <code>Cache-Tag</code> header containing user tags; reserved key and namespace tags stay internal.
4. The middleware preserves an application-authored shared-cache policy only for an eligible public render and never creates one. The privacy and timing/budget gates above override any shared policy and force <code>Cache-Control: private</code>. Without an application policy, the middleware also defaults to private.

This connects value dependencies to a shared-response purge vocabulary, but the current <code>Cdn</code> path does not send the purge. See [Cache HTTP responses](/docs/cache/response-caching/).

## What “unknown” means

Tag knowledge is effectively tri-state:

- **known** — the reader has verified watermarks for the tag;
- **unknown** — the tag has not been verified;
- **suspect** — a transport or recovery event means the warm map cannot be trusted.

Unknown and suspect are not treated as fresh. The default eventual policy attempts a live Registry check; an application may choose a conservative miss or a throwing <code>UnknownTagError</code> posture instead. <code>takedownSensitive</code> forces that throwing posture and outranks the general policy. When coordinated invalidation is active, a strong read uses a live, un-memoized Registry check before serving a stored entry and before filling a strong miss. Registry failure throws <code>RegistryUnavailableError</code> unless <code>onUnavailable: "eventual"</code> degrades that call to the conservative eventual rules and emits <code>strong_degraded</code>.

## Timing is the major unfinished layer

The invalidation ordering above is active. Elapsed-time expiry is not: TTL, grace, and <code>notFoundTtl</code> are present in the types but their durations are not enforced. Entry <code>age</code> is now measured from the served envelope's <code>bornMs</code> fill-start timestamp, but it is evidence only and does not drive freshness. A stale refresh is also awaited in the current kernel instead of being adopted by a background lifecycle. Keep those limitations separate from the implemented epoch and watermark model.

## Related

- [Runtime architecture](/docs/cache/architecture/) maps these operations to the supplied capability contracts.
- [Cache fundamentals](/docs/cache/core-concepts/) provides the plain-language vocabulary.
- [Invalidate cached data](/docs/cache/tags-and-invalidation/) explains soft and hard mutations from the caller's side.
- [Consistency and resilience](/docs/cache/consistency-and-resilience/) covers live checks and stale-on-error behavior.
- [Inspect cache behavior](/docs/cache/observability/) shows the same entry and reader state as a point-in-time witness.
