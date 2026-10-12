---
title: Implementation status
description: Check which Astilba Cache methods, helpers, drivers, and adapters work in the current unreleased source build.
---

Astilba Cache is unreleased. The portable correctness kernel and the main Workers source path are active. The Workers composition now keeps construction I/O-free, acquires request-scoped platform handles lazily, and carries its own request-driven recovery ticks. React Router provides scope-aware L3 dependency collection and response tags. Elapsed TTL and grace policy, CDN purge delivery, several convenience methods, and production release gates remain incomplete.

Use this page as the preview ledger. “Implemented” means behavior exists in the reviewed source snapshot and is exercised by the repository's test lanes. It does not mean the package has an npm installation or production support commitment.

This ledger was reviewed against Cache source commit <code>726e1b0</code> from 27 July 2026. The Cache repository is not publicly accessible, so the identifier is maintainer provenance rather than a checkout link. Later source changes are not documented here until this snapshot advances.

Use [API reference](/docs/cache/api-reference/) for exact exported shapes. When another page and this ledger appear to differ, follow this page and report the stale page.

## Configuration requirements

| Requirement | Current boundary |
| --- | --- |
| <code>namespace</code>, <code>clock</code>, <code>rng</code> | Required by raw <code>createCache()</code>. <code>createWorkersCache()</code> supplies Clock and Rng. |
| <code>l2</code> | Required when a read must run its factory. Without L2, a fill throws <code>NotImplementedError</code>. |
| <code>l1</code> | Optional. Required if principal-derived, L1-only values should survive beyond the current call. The Workers factory supplies bounded <code>memory()</code>. |
| <code>registry</code> | Required by <code>expire()</code>, <code>delete()</code>, and <code>clear()</code>. |
| <code>registry</code> + <code>bus</code> + <code>l2</code> | Required for coordinated read validation and mirror recovery. Registry plus Bus without L2 throws at construction. |
| Workers bindings | <code>createWorkersCache()</code> requires a stable name, KV namespace, and Coordinator Durable Object namespace. The Coordinator separately requires the same KV namespace under <code>REGISTRY_KV</code>. |
| Workers compatibility | Use a compatibility date of 2024-09-23 or later and enable <code>nodejs_compat</code>. The root package uses <code>node:crypto</code>, and that flag also supplies the AsyncLocalStorage support the React Router adapter needs. <code>nodejs_als</code> alone cannot boot the package. |

## Implemented in the root API

| Surface | Current behavior |
| --- | --- |
| <code>createCache()</code> | Builds the cache facade and optional invalidation reader and poller without performing I/O. Retention registration and Bus establishment are deferred until the first read or purge that uses them. |
| <code>getOrSet()</code> | Reads L1 then L2 or fills a value with compatible in-isolate singleflight. If a shared factory fails transiently, a waiting caller can still make its own stale-on-error decision using the stale candidate it read before joining. When an opted-in 404 takes the negative disposition, it resolves as <code>undefined</code>; callers using the plain form must include <code>undefined</code> in <code>T</code>. A terminal fenced fill throws <code>FencedError</code>. |
| <code>getOrSetEntry()</code> | Adds <code>skip()</code> and returns value, the tier actually observed, stale and skip state, elapsed age, optional serve-on-error metadata, and only the durability evidence that serve can prove. After a shared transient failure, it preserves the entry form's miss result when its own candidate is no longer servable. A fresh cached negative reports <code>value: undefined</code> with the actual tier and durability evidence. |
| Consistency defaults | A per-call <code>consistency</code> overrides <code>defaults.consistency</code>; an omitted value otherwise resolves to eventual. |
| Strong reads and fills | Live-check stored entries and pre-check strong misses when coordinated invalidation is configured. Registry failure throws <code>RegistryUnavailableError</code>, or <code>defaults.onUnavailable: "eventual"</code> degrades that call and emits <code>strong_degraded</code>. A verified mid-fill hard purge can trigger a bounded re-mint and refetch. |
| Unknown error posture | <code>unknownPolicy: "error"</code> throws <code>UnknownTagError</code> instead of refilling. <code>takedownSensitive</code> selects the same outcome and outranks an explicit unknown policy. |
| <code>expire()</code>, <code>delete()</code>, <code>clear()</code> | Apply soft, hard, and namespace invalidation through a supplied Registry. |
| <code>collect()</code> | Records explicit dependencies and decides scope, timing, and byte/count budget eligibility at header commit. React Router binds it per request so served hits and fills contribute tags and scope evidence automatically. |
| <code>explain()</code> | Witnesses a default-public key in L1 then L2, its stored identity, current local invalidation verdict and reader state, including Bus channel state, and any request-scoped render attribution. It distinguishes <code>present</code> and proven <code>absent</code>, and returns <code>read-failed</code> when a classified Store read was suppressed and no tier supplies the entry. |
| Factory dependency tags | <code>setTags()</code> replaces the call-level base and <code>dependsOn()</code> adds memberships. The validated settle-time union is stored and used by render collection and write-back fencing. |
| Classified Store failures | Retryable classified L2 writes are suppressed into a non-durable fill. A classified Store read failure emits <code>store_read_suppressed</code> and is a plain serving-path miss; unclassified read failures propagate unchanged. |
| L2 negative-write guard | Before writing a negative, Cache reads the current L2 entry and applies the serving path's codec and invalidation checks. It refuses the write when it finds a decodable value that is fresh or stale, or when that value's invalidation verdict cannot be established. The value remains stored and the attempted negative is non-durable. |
| <code>memory()</code> | Provides a bounded per-instance LRU Store. It enforces <code>maxEntries</code>, UTF-8 <code>maxBytes</code>, and Store-level expiry when constructed with a Clock. |
| <code>t</code>, <code>compound()</code> | Build branded tags. <code>t</code> validates the complete final grammar and byte budget; <code>compound()</code> provides positional delimiter escaping. |
| <code>duration()</code> | Builds computed duration strings and rejects non-positive, non-finite, fractional-millisecond, or unsafe products with <code>InvalidDurationError</code>. |
| <code>httpError()</code>, <code>isRetriableHttp()</code> | Preserve HTTP status information and implement the default stale-on-error classifier. |
| Custom <code>Codec</code> and <code>Lock</code> | Participate in decode safety, fill identity, cross-isolate exclusion, and write arbitration when supplied. |
| Telemetry | Uses the exported <code>TELEMETRY_EVENTS</code> catalog, emits plain events or HMAC-pseudonymizes hosted string fields, and isolates sink throws or rejections with optional <code>onSinkError</code>. |

Scope resolution, negative-entry safety, serve-time stale revalidation, codec identity checks, classified L2 write failures, eventual fail-closed behavior, snapshot-capable recovery, and write-back fencing are also implemented. With <code>notFoundTtl</code>, an <code>HttpError</code> 404 has three singleflight dispositions:

- A factory-running caller with <code>grace</code> and a still-servable stale value suppresses the negative write. Every compatible joiner inherits that value and its evidence.
- If that candidate revalidates as dead or unknown, the leader attempts one negative L2 write and every compatible joiner inherits the negative result.
- If the leader has no grace-eligible candidate, it attempts the negative once and shares the not-found fact. A waiter with <code>grace</code> and a still-servable stale value may return it; one without a servable stale candidate returns <code>undefined</code>.

Serve-time validation can still throw when the configured Registry-unavailable posture does not permit a verdict.

The L2 negative-write guard resolves a Registry gap toward refusal rather than risking displacement. The current 404 outcome still surfaces <code>undefined</code> at <code>tier: "origin"</code> with <code>durable: false</code>, but L2 is unchanged and no L1 cleanup runs. The guard does not inspect L1. Codec-incompatible, undecodable, or dead L2 bytes do not trigger it; normal newer-envelope arbitration still applies afterward.

Both a guard refusal and a stale candidate suppressing the 404 emit <code>neg_suppressed</code>. A retryable Store write failure is a separate non-durable outcome. A waiter that returns its own candidate retains that candidate's evidence.

## Implemented source adapters and evidence

| Entry point | Current behavior |
| --- | --- |
| <code>@astilba/cache/cloudflare</code> | Exports <code>createWorkersCache</code>, <code>cloudflareKV</code>, <code>Coordinator</code>, <code>doRegistry</code>, <code>doBus</code>, <code>redialingDoBus</code>, and their public configuration types. |
| <code>@astilba/cache/react-router</code> | Exports root server middleware, typed Cache context, current request access, default L3 budget and ineligibility event constants, and the observable poll-tick event and interval constants. |
| Recovery poller | Observes pointer liveness, runs bounded delta and snapshot recovery, and backs baseline polling off on failure. <code>createWorkersCache()</code> drives both polling and lazy Bus redial from request-time reads; React Router can additionally adopt middleware ticks into the response lifecycle with <code>waitUntil</code>. |
| Local chaos demo | An unpublished React Router v8 app composes the real Workers factory with local KV and Coordinator bindings. Demo-owned wrappers inject a KV backend failure, refuse future Bus dials, or make mirror reads unavailable while leaving live Registry RPC intact. The last fault does not close an already-established socket. The app prints observed outcomes beside documented permissions and asserts no SLO. |
| Composed demo boot witness | A required CI lane builds that demo, boots its emitted Worker on local workerd with the real Coordinator and KV bindings, and reads a machine-facing status route. The healthy arm must reach <code>established</code>; an armed Bus-dial refusal is the negative control and must report <code>never-established</code> plus <code>bus_dial_failed</code>. The lane proves the app is wired to expose those states, not kernel semantics or deployed behavior. |

The two adapter subpaths are present in the source and publish export map, but no package has been published. The poller is an internal seam and the demo is an unpublished workspace app; neither creates another public package entry point.

## Partial or provisional behavior

| Surface | Current boundary |
| --- | --- |
| TTL and grace | Duration builders work, but stored TTL and grace fields are zero and elapsed expiry is not enforced. Defaults for TTL and grace are not applied. Entry <code>age</code> is now measured from the served envelope's <code>bornMs</code> fill-start timestamp; it is observability, not TTL enforcement. |
| <code>notFoundTtl</code> | Its presence allows an <code>HttpError</code> 404 to take one negative L2 write attempt, but the duration is not enforced. A negative fill and a later read that reaches the invalidation-fresh negative surface <code>undefined</code>, never the internal stored placeholder. A refused negative or suppressed retryable write failure leaves the origin outcome non-durable. Negative entries remain excluded from grace and stale-on-error. |
| Negative L1 cleanup | A negative result that reaches the serve path skips L1 hydration and best-effort deletes any older L1 copy unless the L2 negative-write guard refused the write. This cleanup still runs after a classified retryable L2 write failure. A failed L1 delete does not block the call; the older value can remain readable until that tier evicts it or a later value fill overwrites it. A guard refusal leaves both tiers unchanged. |
| Eventual stale refresh | The stale value is returned, but refresh work is currently awaited before that response resolves. Background adoption, queue retry, and latency decoupling are incomplete. |
| Factory cancellation and grace context | The factory receives a fresh <code>AbortSignal</code>, but the kernel does not abort it on a cache deadline. <code>ctx.graced</code> is not populated. |
| <code>compound()</code> validation | Positional escaping works, but compound parts do not receive <code>t</code>'s remaining character or 256-byte validation in the reviewed snapshot. Keep parts grammar-safe. |
| React Router response caching | Automatic dependency and scope collection, <code>Cache-Tag</code> emission, safe private demotion, immutable-response rebuilding, and an overridable 16 KB / 1,000-occurrence budget work. The middleware never invents <code>public</code> or <code>s-maxage</code>; the application must opt into shared caching. |
| Factory response qualifier | <code>FactoryCtx.dependsOn(tag, { l3: false })</code> throws until stored per-tag emission metadata exists. The same qualifier works on <code>RenderCollector.dependsOn()</code>. |
| Explain scope and authority | <code>explain(key)</code> addresses only the default public canonical key. It uses current local knowledge and performs no live check, resync, or L1 hydration. |
| Telemetry catalog | <code>neg_suppressed</code> is live for a 404 suppressed in favor of a grace-eligible stale value or refused by the L2 negative-write guard. <code>registry_degraded</code> and <code>state_stale</code> are reserved names with no emit sites. <code>createWorkersCache()</code> accepts kernel telemetry, but its fixed internal memory L1 still has no separate <code>private_evicted</code> sink. React Router middleware telemetry also remains an explicit option. |
| Purge result | <code>matchedHint</code> is always <code>"unknown"</code>; <code>flushed()</code> and <code>edgePurged()</code> resolve without tracking the promised completion boundaries. |
| Key invalidation | <code>{ key }</code> targets the contextless public canonical key only. Use dependency tags for tenant or principal-derived variants. |
| Scope-qualified tag selectors | The selector type accepts <code>scope</code>, but Registry tag resolution ignores it. A tag purge affects every entry carrying that tag. |
| Lock option | <code>lock: true</code> uses a configured Lock; without one it silently continues without cross-isolate locking. No production Lock adapter is exported. |
| Coordinator journal | Durable and replayable, but append-only without production checkpointing or truncation. |
| Workers measurements | Workerd lanes, the local chaos demo, and the required composed-app boot witness cover local composition and failure posture. Deployed consistency, propagation, caching, and production-threshold measurements are still pending. |

<code>defaults.consistency</code>, <code>unknownPolicy</code>, <code>onUnavailable</code>, <code>takedownSensitive</code>, <code>maxEntryRetention</code>, <code>maxSyncLag</code>, and <code>heartbeatInterval</code> are consumed. <code>maxSyncLag</code> tunes the attached recovery poller; <code>heartbeatInterval</code> tunes the reader's silence threshold and must be coordinated separately with the Coordinator deployment variable.

## Throwing placeholders

These exports or paths currently throw <code>NotImplementedError</code>:

- <code>globalTag()</code>
- <code>expireAll()</code> and <code>deleteAll()</code>
- <code>FactoryCtx.reuseGraced()</code>
- <code>FactoryCtx.setTtl()</code>
- <code>FactoryCtx.dependsOn(tag, { l3: false })</code>
- a factory fill with no L2 Store
- <code>expire()</code>, <code>delete()</code>, or <code>clear()</code> without a Registry

## Declared but inert surfaces

These APIs currently return normally or exist in the types but do not apply their intended behavior:

- <code>CacheConfig.cdn</code> and the <code>cdn</code> mode on <code>delete()</code> do not invoke a CDN driver.
- <code>defaults.graceBackoff</code> is not consumed.
- <code>Tier</code> includes <code>"l1.5"</code>, but the current read path does not emit it.

<code>expireAll()</code> and <code>deleteAll()</code> still require their explicit origin-load guard at the type level, but the methods do not run yet.

## Integrations not present

There is no Redis or Valkey adapter, production Lock, CDN purge path, or deployed-probe package. React Router is the only shared-response adapter. Its fixture and the Cloudflare integration Worker are tests, while the local chaos demo is an evidence app; none of them is a production application template.

:::note[Development tags are not package releases]
The repository's <code>v0.1</code> tag records the sealed correctness-kernel milestone. npm still has no <code>@astilba/cache</code> package. Keep using the Unreleased documentation until an installable release exists.
:::

## Related

- [Source walkthrough](/docs/cache/quickstart/) follows the smallest reviewed source configuration.
- [Cloudflare Workers](/docs/cache/cloudflare-workers/) documents the current runtime factory and bindings.
- [Cache HTTP responses](/docs/cache/response-caching/) documents the implemented L3 collection and header boundary.
- [Inspect cache behavior](/docs/cache/observability/) documents <code>explain()</code> and the telemetry catalog.
- [API reference](/docs/cache/api-reference/) documents every root and adapter export.
- [Runtime architecture](/docs/cache/architecture/) maps configuration requirements to capability contracts.
- [Driver implementations](/docs/cache/drivers-and-status/) gives component-level implementation detail.
