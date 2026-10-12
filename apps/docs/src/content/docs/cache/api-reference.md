---
title: API reference
description: Reference the root, Cloudflare, and React Router exports in the current @astilba/cache source snapshot.
---

This page documents the complete root export surface and the two public adapter subpaths in the current source snapshot. Start with the [overview](/docs/cache/overview/) or [source walkthrough](/docs/cache/quickstart/) if you are learning the library; use this page when you need an exact method, option, result field, or driver contract.

:::caution[Unreleased and unevenly implemented]
<code>@astilba/cache</code> is not published to npm. Some declarations describe intended behavior that still throws or remains inert. Each section calls out important boundaries; [Implementation status](/docs/cache/api-status/) is the authoritative implementation ledger.
:::

## Create a cache

### <code>createCache(config)</code>

Creates a <code>Cache</code> instance from application-supplied capabilities.

~~~ts
const cache = createCache({
  namespace: "storefront",
  clock,
  rng,
  l2: store,
})
~~~

The raw constructor requires <code>namespace</code>, <code>clock</code>, and <code>rng</code>. Construction performs no I/O; retention registration and Bus establishment are deferred until the first read or purge that needs them. A factory fill also requires <code>l2</code>; without it, the fill throws <code>NotImplementedError</code>. <code>createWorkersCache()</code> is the higher-level Workers constructor.

### <code>CacheConfig</code>

| Field | Type | Meaning and current boundary |
| --- | --- | --- |
| <code>namespace</code> | <code>string</code> | Required stable boundary for canonical keys and namespace invalidation. |
| <code>clock</code> | <code>Clock</code> | Required source of logical time. |
| <code>rng</code> | <code>Rng</code> | Required source of randomness. |
| <code>l1</code> | <code>Store</code> | Optional local tier. Retains principal-derived values that cannot be written to shared storage. |
| <code>l2</code> | <code>Store</code> | Shared or durable tier. Currently required whenever a factory runs and can also hold replication-mirror objects for recovery. |
| <code>registry</code> | <code>Registry</code> | Authoritative invalidation driver. Required by the purge methods. |
| <code>bus</code> | <code>Bus</code> | Live invalidation delivery. Coordinated validation is built when Registry and L2 are also configured. Registry plus Bus without L2 throws at construction. |
| <code>cdn</code> | <code>Cdn</code> | Declared L3 purge capability; not invoked by the current kernel. |
| <code>lock</code> | <code>Lock</code> | Optional cross-instance fill lock. A read opts in with <code>lock: true</code>. |
| <code>codec</code> | <code>Codec</code> | Value encoder and wire identity. Defaults to the built-in JSON round trip. |
| <code>defaults</code> | <code>CacheDefaults</code> | Instance policy defaults. Timing fields remain partial; consistency and unavailable-policy fields are active. |
| <code>telemetry</code> | <code>TelemetrySink \| TelemetryConfig</code> | Receives operational events. Hosted mode pseudonymizes string fields when a salt is present; configured delivery isolates sink failures and can call <code>onSinkError</code>. |
| <code>takedownSensitive</code> | <code>boolean</code> | Makes unknown invalidation knowledge throw <code>UnknownTagError</code>. A <code>true</code> value here or in <code>defaults</code> activates the safety override; <code>false</code> at one level does not cancel <code>true</code> at the other. |
| <code>dev</code> | <code>boolean</code> | Makes incompatible same-key singleflight calls fail loudly and guards request reads inside explicitly public factories. |

### <code>CacheDefaults</code>

| Field | Meaning and current boundary |
| --- | --- |
| <code>ttl</code> | Default freshness duration. Declared but not applied; elapsed expiry is unfinished. |
| <code>grace</code> | Default stale window. Declared but not applied; elapsed grace is unfinished. |
| <code>maxEntryRetention</code> | Maximum retention registered lazily when a configured Registry is first used. It has no registration target without that driver. |
| <code>consistency</code> | Default <code>eventual</code> or <code>strong</code> consistency. A per-call value wins; an omitted value otherwise resolves to eventual. |
| <code>unknownPolicy</code> | Chooses <code>registry-check</code>, <code>miss</code>, or <code>error</code> for unknown invalidation knowledge. The error posture throws <code>UnknownTagError</code>. |
| <code>staleIfError</code> | Replaces the default <code>isRetriableHttp()</code> failure classifier. |
| <code>graceBackoff</code> | Declared retry backoff for grace behavior; not consumed today. |
| <code>maxSyncLag</code> | Base cadence for the attached replication poller. Defaults to the Workers profile's 60 seconds when omitted. |
| <code>acceptCodecs</code> | Additional stored codec identities the current Codec is allowed to decode. |
| <code>heartbeatInterval</code> | Reader-side heartbeat interval used to derive the invalidation-silence threshold. The Workers factory defaults it to 30 seconds; the Coordinator deployment variable is configured separately. |
| <code>onUnavailable</code> | With <code>"eventual"</code>, a failed strong Registry check degrades that call to eventual and emits <code>strong_degraded</code>. Otherwise the read throws <code>RegistryUnavailableError</code>. |
| <code>takedownSensitive</code> | The defaults-level form of the unknown-as-error safety override. If either this value or the top-level value is <code>true</code>, it outranks <code>unknownPolicy</code> and unknown knowledge throws <code>UnknownTagError</code>. |

## Read or fill values

### <code>Cache</code> methods

| Method | Returns | Current behavior |
| --- | --- | --- |
| <code>getOrSet(options)</code> | <code>Promise&lt;T&gt;</code> | Returns a usable hit or runs the factory. When an opted-in 404 takes the negative disposition, it resolves as <code>undefined</code>; include <code>undefined</code> in <code>T</code>. A terminal fenced fill throws <code>FencedError</code>. |
| <code>getOrSetEntry(options)</code> | <code>Promise&lt;CacheEntry&lt;T&gt;&gt;</code> | Adds <code>skip()</code> and returns read metadata. A terminal fenced fill becomes a miss entry. |
| <code>expire(selector)</code> | <code>Promise&lt;PurgeResult&gt;</code> | Applies a soft invalidation through Registry. |
| <code>delete(selector)</code> | <code>Promise&lt;PurgeResult&gt;</code> | Applies a hard invalidation through Registry. CDN modes are not wired. |
| <code>clear()</code> | <code>Promise&lt;PurgeResult&gt;</code> | Bumps the local namespace version and hard-invalidates the reserved namespace tag. |
| <code>expireAll(guard)</code> | <code>Promise&lt;PurgeResult&gt;</code> | Declared with an explicit origin-load acknowledgement; currently throws <code>NotImplementedError</code>. |
| <code>deleteAll(guard)</code> | <code>Promise&lt;PurgeResult&gt;</code> | Declared with an explicit origin-load acknowledgement; currently throws <code>NotImplementedError</code>. |
| <code>collect()</code> | <code>RenderCollector</code> | Creates a scope-aware L3 dependency collector. React Router binds one per request so hits and fills contribute automatically. |
| <code>explain(key)</code> | <code>Promise&lt;Explanation&gt;</code> | Witnesses the default-public key in L1 then L2, its stored identity, current local verdict and reader state, and any request-scoped attribution. It distinguishes a present entry, proven absence, and a suppressed Store read failure. |

### Read option types

<code>GetOrSetOptions&lt;T&gt;</code> and <code>GetOrSetEntryOptions&lt;T&gt;</code> extend <code>GetOptions</code> with their respective factory type.

| <code>GetOptions</code> field | Meaning and current boundary |
| --- | --- |
| <code>key</code> | Required application-facing key. Namespace, scope, and namespace version are added internally. |
| <code>tags</code> | Optional dependency tags. Use branded values created by the tag helpers. |
| <code>ttl</code> | Intended freshness duration. It participates in singleflight compatibility but elapsed time is not enforced. |
| <code>grace</code> | Opts a stale candidate into classified error fallback. The declared duration is not enforced. |
| <code>notFoundTtl</code> | Its presence allows an <code>HttpError</code> 404 to attempt a negative L2 entry. A negative fill and a later read that reaches an invalidation-fresh negative surface <code>undefined</code>; the duration is not enforced. A current servable value in that L2 can refuse the negative write. |
| <code>scope</code> | <code>"public"</code> or <code>{ tenant }</code>. When omitted, visible identity derives a principal-local scope; contextless work resolves public. |
| <code>consistency</code> | <code>"eventual"</code> or <code>"strong"</code>. Strong live-checks a stored entry and pre-checks a miss before running its factory when coordinated invalidation is active. |
| <code>lock</code> | Requests a configured cross-instance Lock. Without a driver, <code>true</code> currently continues unlocked. |
| <code>request</code> | Adapter-provided <code>RequestContext</code> used for identity derivation and the development public-scope guard. |
| <code>factory</code> | Async origin loader. Its context is <code>FactoryCtx&lt;T&gt;</code> or <code>EntryFactoryCtx&lt;T&gt;</code>. |

The exported option types are <code>GetOptions</code>, <code>GetOrSetOptions</code>, and <code>GetOrSetEntryOptions</code>.

Compatible calls share one foreground factory execution only when key, tags, TTL, grace, negative-cache TTL, resolved scope, codec identity, consistency, and API form agree. A successful fill or an already-revalidated stale serve is shared with its evidence. If the factory-running call had no stale candidate and shares only a classified transient failure, each waiting caller makes its own stale-on-error decision using the candidate it read before joining. The classifier is not invoked when <code>grace</code> is absent. If no caller has a candidate, both API forms propagate the error. If serve-time revalidation rejects a waiting caller's candidate after a hard invalidation, the plain form propagates the classified error while the entry form returns its documented miss result.

When serve-time validation completes, an opted-in shared 404 has three served dispositions:

- A factory-running caller with <code>grace</code> and a still-servable stale value suppresses the negative write. Every compatible joiner inherits that value and its evidence.
- If that grace-eligible candidate revalidates as dead or unknown, the leader attempts one negative L2 write and every compatible joiner inherits the negative result.
- If the leader has no grace-eligible candidate, it attempts the negative once inside the singleflight window and shares the not-found fact. Each waiter then uses its own earlier read: one with a grace-eligible, still-servable stale value returns it, while one without a servable stale candidate returns <code>undefined</code>.

Without <code>grace</code>, every compatible caller takes the negative disposition even if it observed a stale value. Validation may instead throw <code>RegistryUnavailableError</code> under the configured unavailable posture.

Before persistence, every negative attempt passes through the L2 negative-write guard. A decodable existing value whose invalidation verdict is fresh or stale—or cannot be established—refuses the write. The not-found disposition still surfaces <code>undefined</code> from origin with <code>durable: false</code>, while the existing L2 value and L1 state remain unchanged. Codec-incompatible, undecodable, or dead L2 bytes do not trigger this guard; normal newer-envelope arbitration still applies afterward.

A negative result that reaches the serve path skips L1 hydration and triggers best-effort deletion of any older L1 copy unless the guard refused the write. This cleanup still runs after a classified retryable write failure. Both a stale-candidate suppression and a guard refusal emit <code>neg_suppressed</code>.

### Factory context

| <code>FactoryCtx&lt;T&gt;</code> member | Meaning and current boundary |
| --- | --- |
| <code>signal</code> | A fresh <code>AbortSignal</code>. The kernel does not yet abort it on a cache deadline. |
| <code>graced</code> | Optional <code>GracedInfo</code> describing a stale candidate. It is not populated today. |
| <code>request</code> | The adapter request object. Under <code>dev: true</code> and explicit public scope, property reads can demote the fill to L1-only. |
| <code>fail(err?)</code> | Throws a factory failure without pretending it is a returned <code>T</code>. |
| <code>dependsOn(tag, options?)</code> | Adds a membership to the final stored tag set and the active render collector. <code>{ l3: false }</code> currently throws <code>NotImplementedError</code>. |
| <code>setTags(tags)</code> | Authors a replacement for the call-level tag base. <code>dependsOn()</code> memberships remain unioned independently. |
| <code>setTtl(ttl)</code> | Declared factory-time TTL override; currently throws <code>NotImplementedError</code>. |
| <code>reuseGraced()</code> | Intended typed reuse of the graced value after provenance checks; currently throws <code>NotImplementedError</code>. |

The final settle-time union may contain at most 126 distinct user tags. Invalid, reserved, or excessive tags fail before storage. Calling <code>dependsOn()</code> or <code>setTags()</code> after the factory promise settles throws <code>FactorySettledError</code>.

<code>EntryFactoryCtx&lt;T&gt;</code> adds <code>skip(): never</code>. Calling it produces a skipped entry and stores nothing. The plain <code>FactoryCtx&lt;T&gt;</code> deliberately has no <code>skip()</code>.

Related exports are <code>FactoryCtx</code>, <code>EntryFactoryCtx</code>, <code>GracedInfo</code>, and <code>RequestContext</code>.

<code>GracedInfo</code> exposes the candidate's unknown-typed <code>value</code>, source identifier <code>src</code>, schema version <code>v</code>, and original <code>bornMs</code>. It is metadata for <code>reuseGraced()</code>, not permission to cast the unknown value to <code>T</code>.

### <code>CacheEntry&lt;T&gt;</code>

| Field | Meaning and current boundary |
| --- | --- |
| <code>value</code> | The value, or <code>undefined</code> for a miss, skip, or negative entry. A fresh negative keeps the tier, age, and durability evidence of the entry that supplied that fact. |
| <code>skipped</code> | The entry factory called <code>skip()</code>. |
| <code>stale</code> | The returned value was not fresh at this read's consistency level. |
| <code>age</code> | Whole milliseconds since the served envelope's <code>bornMs</code> fill-start timestamp, measured with the injected Clock and clamped at zero. A miss or skip reports zero. This is observability, not TTL enforcement. |
| <code>tier</code> | <code>l1</code>, <code>l2</code>, <code>origin</code>, or <code>miss</code>. The exported <code>Tier</code> union also declares <code>l1.5</code>, which is not emitted. |
| <code>servedOnError</code> | Present when a classified transient failure reused a revalidated stale candidate. |
| <code>durable</code> | Optional serve evidence. Every newly filled origin result reports whether L2 accepted what that turn produced or a newer durable entry won. A refused negative and a suppressed Store write report <code>false</code>; an L2 hit reports <code>true</code>; a principal-scoped L1 hit reports <code>false</code>. A public or tenant L1 hit omits the field because that serve does not consult L2. |

## Invalidate values

### Selector types

<code>ExpireSelector</code> accepts <code>{ tag, scope? }</code> or <code>{ key }</code>. <code>DeleteSelector</code> accepts those shapes plus an optional <code>cdn</code> mode.

- A tag selector affects every entry carrying that tag. The current implementation ignores its optional <code>scope</code> field.
- A key selector targets the contextless public canonical key only. Use a dependency tag when tenant or principal variants may exist.
- <code>CdnMode</code> is <code>"enqueue" | "await" | "block"</code>, but no mode invokes the configured CDN today.

<code>OriginLoadGuard</code> is the explicit <code>{ iUnderstandTheOriginLoad: true }</code> acknowledgement required by the unimplemented <code>expireAll()</code> and <code>deleteAll()</code> methods.

### <code>PurgeResult</code>

| Field or method | Meaning and current boundary |
| --- | --- |
| <code>epoch</code> | Registry epoch returned for the mutation. |
| <code>matchedHint</code> | Best-effort <code>MatchedHint</code>: <code>yes</code>, <code>no-such-scope</code>, or <code>unknown</code>. It is always <code>unknown</code> today. |
| <code>flushed({ timeout? })</code> | Intended durable, Bus, and mirror-acceptance completion. Resolves immediately today. |
| <code>edgePurged({ timeout? })</code> | Intended CDN-acceptance completion. Resolves immediately without a CDN purge today. |

See [Invalidate cached data](/docs/cache/tags-and-invalidation/) for safe mutation order and [Implementation status](/docs/cache/api-status/) before using completion fields operationally.

## Build tags and durations

### Tag exports

| Export | Purpose and current boundary |
| --- | --- |
| <code>Tag</code> | Branded string accepted by cache reads and invalidation selectors. |
| <code>TagPart</code> | <code>string &#124; number</code> input used by tag helpers. |
| <code>compound(...parts)</code> | Implemented positional builder. Escapes <code>%</code>, <code>:</code>, and <code>|</code> and prefixes arity. It does not currently apply <code>t</code>'s remaining character or byte-budget validation to its parts. |
| <code>t</code> | Implemented tagged-template trust boundary. Validates a non-empty, non-reserved final tag against the lowercase grammar and 256 UTF-8-byte ceiling; it rejects rather than escaping interpolations. |
| <code>globalTag(name)</code> | Declared globally scoped tag helper; currently throws <code>NotImplementedError</code>. |

User tags beginning with <code>__</code> are reserved and rejected with <code>InvalidTagError</code> at the cache boundary.

### Duration exports

<code>Duration</code> is a template-literal type such as <code>"250ms"</code>, <code>"5m"</code>, or <code>"1.5h"</code>. <code>DurationUnit</code> is <code>"ms" | "s" | "m" | "h" | "d"</code>; <code>m</code> means minutes.

<code>duration(value, unit)</code> is the implemented computed-value helper. It rejects non-positive or non-finite values and any multiplied millisecond result that is not a finite, positive, safe integer, throwing <code>InvalidDurationError</code>. Duration strings can appear in typed options, although elapsed TTL and grace behavior is unfinished.

## Driver contracts

Most application developers should receive drivers from a runtime package. These exports exist for adapter authors, test harnesses, and advanced integrations.

### Storage

The core Store shape is:

~~~ts
interface Store {
  get(key: string, readKind?: ReadKind): Promise<StoreValue | undefined>
  set(
    key: string,
    value: string,
    options?: StoreWriteOptions,
  ): Promise<void>
  delete(key: string): Promise<void>
}
~~~

| Export | Purpose |
| --- | --- |
| <code>Store</code> | Async <code>get()</code>, <code>set()</code>, and <code>delete()</code> contract used by L1, L2, and mirror storage. |
| <code>ReadKind</code> | Optional Store read hint: <code>"pointer"</code>, <code>"delta"</code>, or <code>"snap"</code>. Drivers may map it to different read-cache policy. |
| <code>StoreValue</code> | Stored string value plus optional metadata. |
| <code>StoreMetadata</code> | Readonly metadata record. |
| <code>StoreWriteOptions</code> | Optional physical <code>expirationTtl</code> in seconds and metadata. |
| <code>StoreWriteError</code> | Structural write rejection with code <code>throttled</code>, <code>too_large</code>, or <code>unavailable</code>, plus retryability and optional cause. |
| <code>isStoreWriteError(value)</code> | Implemented shape-based type guard for <code>StoreWriteError</code>. |
| <code>MemoryOptions</code> | Optional <code>clock</code>, <code>maxEntries</code>, UTF-8 <code>maxBytes</code>, <code>telemetry</code>, and <code>onSinkError</code> for the local Store. |
| <code>memory(options?)</code> | Implemented per-instance LRU Store. It evicts to both configured bounds, rejects a single oversize value as <code>too_large</code>, honors Store-level <code>expirationTtl</code> with a Clock, and can report pressure eviction of principal-scoped entries without identifiers. |

Calling <code>Store.set()</code> with <code>expirationTtl</code> on a clockless memory Store fails loudly instead of silently ignoring residency. The Cache kernel does not currently pass value TTL through as Store residency, so this behavior matters primarily to direct Store users and replication objects.

Serving reads recognize the following structural rejection:

~~~ts
interface ClassifiedStoreReadFailure {
  readonly code: "throttled" | "unavailable"
  readonly retryable: boolean
  readonly cause?: unknown
}
~~~

The shape and its guard are deliberately not root exports, but classification is shape-based rather than <code>instanceof</code>-based. A custom Store can therefore reject <code>get()</code> with that exact shape; the bundled <code>cloudflareKV()</code> driver provides the covered implementation. A classified failure emits <code>store_read_suppressed</code> and acts as a tier miss, while any rejection without both a recognized <code>code</code> and boolean <code>retryable</code> propagates unchanged. <code>explain()</code> reports <code>read-failed</code> when a classified failure prevents it from proving either presence or absence.

<code>CasOrder</code> contains <code>epoch</code> and <code>fence</code>. <code>CasRecord</code> adds an optimistic-concurrency <code>token</code>. <code>CasStore</code> declares <code>seed(key, order)</code>, <code>load(key)</code>, and <code>swap(key, expectedToken, next)</code>. It is an optional atomic compare-and-set capability for durable drivers; the current kernel does not consume it.

### Invalidation coordination

| Export | Purpose |
| --- | --- |
| <code>Registry</code> | Registry identity, live checks, soft and hard mutations, and retention registration. |
| <code>RegistryAck</code> | Mutation acknowledgement containing the accepted epoch. |
| <code>TagChange</code> | One tag's optional soft and hard watermark changes. |
| <code>BusFrame</code> | A contiguous <code>fromEpoch</code> to <code>toEpoch</code> range of changes. |
| <code>BusEvent</code> | <code>frame</code>, <code>gap</code>, <code>reset</code>, <code>hello</code>, or <code>lost</code> event delivered to a subscriber. |
| <code>Bus</code> | Subscribes the kernel to Bus events. The kernel validates continuity. |
| <code>Subscription</code> | Handle with <code>close()</code>. |

The Registry is authoritative; the Bus is a delivery mechanism. A gap or reset suspends warm trust until recovery establishes a verified position.

The <code>Registry</code> contract exposes:

| Member | Meaning |
| --- | --- |
| <code>regId</code> | Stable Registry identity used to scope and verify recovery-mirror objects. |
| <code>check(tags)</code> | Returns live <code>TagKnowledge</code> for each requested tag. |
| <code>expire(tags)</code> | Advances soft watermarks and returns a <code>RegistryAck</code>. |
| <code>delete(tags)</code> | Advances hard watermarks and returns a <code>RegistryAck</code>. |
| <code>registerRetention(retentionMs)</code> | Registers this instance's maximum retention. |

<code>RegistryAck</code> contains the accepted <code>epoch</code>. <code>TagChange</code> contains a <code>tag</code> plus optional <code>softEpoch</code> and <code>hardEpoch</code>. <code>BusFrame</code> contains <code>fromEpoch</code>, <code>toEpoch</code>, and a list of changes. <code>BusEvent</code> is one of:

- <code>{ kind: "frame", frame }</code> for a contiguous change frame;
- <code>{ kind: "gap", head }</code> when delivery loss is known and the transport declares the minimum head the reader must reach;
- <code>{ kind: "reset" }</code> when the transport is re-established;
- <code>{ kind: "hello", head }</code> immediately after establishment, declaring the live channel's current head;
- <code>{ kind: "lost" }</code> when a live transport is unavailable, whether it never established or was later lost.

### Other capabilities

| Export | Purpose and current boundary |
| --- | --- |
| <code>Clock</code> | <code>now(): number</code>, the kernel's explicit logical-time source. |
| <code>Rng</code> | <code>next(): number</code>, the kernel's explicit random source in <code>[0, 1)</code>. |
| <code>Codec</code> | Wire identity plus synchronous <code>encode()</code> and <code>decode()</code>. |
| <code>Lock</code> | Acquires a <code>LockHandle</code> for a canonical key. |
| <code>LockHandle</code> | Carries a monotone <code>fence</code> and async <code>release()</code>. |
| <code>Cdn</code> | Accepts a set of tags and a <code>CdnMode</code>. Declared but not called today. |
| <code>CdnMode</code> | CDN acceptance request: <code>enqueue</code>, <code>await</code>, or <code>block</code>. |

See [Driver implementations](/docs/cache/drivers-and-status/) for available implementations.

## Consistency and stored data

| Export | Meaning |
| --- | --- |
| <code>Consistency</code> | <code>eventual</code> or <code>strong</code>. |
| <code>Watermark</code> | Monotone <code>softEpoch</code> and <code>hardEpoch</code> for a tag. |
| <code>TagKnowledge</code> | Either known watermarks with a verified <code>throughEpoch</code>, or <code>{ known: false }</code>. |
| <code>Validity</code> | Validation result: <code>fresh</code>, <code>stale</code>, <code>dead</code>, or <code>unknown</code>. |
| <code>UnknownPolicy</code> | <code>registry-check</code>, <code>miss</code>, or the throwing <code>error</code> policy. |
| <code>ChannelState</code> | Reader-observed live-channel state: <code>never-established</code>, <code>established</code>, or <code>lost</code>. |
| <code>Scope</code> | Explicit <code>public</code> or tenant scope. An omitted scope may derive a principal-local storage class. |
| <code>Tier</code> | Result tier: <code>l1</code>, declared <code>l1.5</code>, <code>l2</code>, <code>origin</code>, or <code>miss</code>. |

<code>Envelope&lt;T&gt;</code> is the exported schema-v3 stored entry. Most applications should not construct envelopes directly.

| Field | Meaning |
| --- | --- |
| <code>v</code> | Literal schema version <code>3</code>. |
| <code>key</code> | Canonical namespace-version, namespace, scope, and user-key string. |
| <code>val</code> | Stored value after decoding. For <code>kind: "neg"</code>, the decoded placeholder is internal and a serving read returns <code>undefined</code> instead. |
| <code>bornEpoch</code> | Invalidation epoch captured at fill start. |
| <code>bornMs</code> | Fill-start time for TTL, grace, age, and retention arithmetic—not invalidation ordering. |
| <code>storedAt</code> | Observability timestamp. |
| <code>ttl</code>, <code>grace</code> | Stored timing fields. They are zero in current entries. |
| <code>tags</code> | Sorted, deduplicated user and reserved dependency tags. |
| <code>scope</code> | Resolved public, tenant, or principal storage class. |
| <code>src</code> | Source identifier. |
| <code>kind</code> | <code>EnvelopeKind</code>: <code>val</code> or negative <code>neg</code>. |
| <code>enc</code> | <code>EnvelopeEnc</code>: <code>json</code>, <code>json+gz</code>, or <code>bin</code>. |
| <code>size</code> | Stored payload size. |
| <code>codecId</code> | Wire identity checked before decoding. |
| <code>fence</code> | Optional Lock fencing token. |

## Render collection and L3

| Export | Purpose and current boundary |
| --- | --- |
| <code>RenderCollector</code> | Records dependency tags and makes an eligibility decision at header commit. |
| <code>L3Budget</code> | Optional <code>maxBytes</code> and <code>maxTags</code> limits. |
| <code>L3Emission</code> | Eligibility, emitted cache tags, and optional ineligibility reason. |
| <code>L3Ineligibility</code> | <code>late-tag</code>, <code>budget</code>, <code>scope</code>, or <code>scope-unreadable</code>. |
| <code>Explanation</code> | Three-arm <code>kind</code>-discriminated witness returned by <code>explain()</code>: <code>present</code>, <code>absent</code>, or <code>read-failed</code>. |
| <code>ExplainIdentity</code> | Stored tags, scope evidence, birth epoch, TTL evidence, and entry kind. |
| <code>ExplainVerdict</code> | Current local validity plus the soft and hard epochs behind it. |
| <code>ExplainReaderState</code> | Applied epoch, suspicion state, terminal recovery state, and <code>ChannelState</code>. |
| <code>ExplainScope</code>, <code>ExplainTtl</code> | Readable-versus-unreadable scope and stored-versus-not-stored TTL evidence. |
| <code>ExplainedDependency</code>, <code>ExplainDependencyScope</code> | Request-scoped render attribution and its scope evidence. |

The collector and React Router header integration are implemented. The CDN purge path remains incomplete.

<code>RenderCollector.dependsOn(tag, { l3? })</code> records a render-only dependency with no managed scope claim. Setting <code>l3: false</code> excludes its tag from emission, timing, and budget checks. <code>commitHeaders({ maxBytes?, maxTags? })</code> returns an <code>L3Emission</code>. Automatically recorded hits and fills carry scope evidence; any non-public or unreadable scope makes the emission ineligible and empty.

<code>explain(key)</code> probes L1 then L2 without hydrating L1, live-checking the Registry, or resynchronizing the reader. It addresses the default public canonical key because the signature has no scope input. Every arm contains <code>key</code>, <code>reader</code>, and optional request-scoped <code>requestDependencies</code>. The <code>present</code> arm adds <code>tier</code>, <code>identity</code>, and <code>verdict</code>; the proven <code>absent</code> arm reports <code>tier: "miss"</code>; <code>read-failed</code> has no tier or identity because at least one Store did not answer and no other tier supplied the entry. Current entries report TTL as <code>{ kind: "not-stored" }</code>.

## HTTP failures and errors

### HTTP helpers

<code>httpError(response)</code> returns an <code>HttpError</code> carrying the response and status. <code>isRetriableHttp(error)</code> is the implemented default stale-on-error classifier: it accepts network <code>TypeError</code> values, cache-originated timeouts, selected transient HTTP statuses, and Cloudflare 52x/530 statuses. It does not classify caller aborts or fact-like 403, 404, and 410 responses as retriable.

### Error exports

| Export | Meaning and current boundary |
| --- | --- |
| <code>HttpError</code> | Typed non-success HTTP response with <code>status</code> and <code>response</code>. |
| <code>CacheTimeoutError</code> | Timeout carrying a <code>CacheTimeoutSource</code> of <code>cache</code> or <code>caller</code>. The kernel does not currently create cache-deadline aborts. |
| <code>FencedError</code> | A plain-value fill was fenced by a conflicting hard invalidation, leaving no value to return. |
| <code>InvalidTagError</code> | Caller supplied a malformed or reserved tag at the cache boundary. |
| <code>InvalidDurationError</code> | <code>duration()</code> received a non-positive, non-finite, fractional-millisecond, or unsafe computed duration. |
| <code>FactorySettledError</code> | A retained factory context declared tags after its factory promise settled. Carries the offending key. |
| <code>NotImplementedError</code> | A declared preview surface was called before implementation. |
| <code>RegistryUnavailableError</code> | A strong Registry check failed and the call was not configured to degrade to eventual. |
| <code>UnknownTagError</code> | Unknown invalidation knowledge reached the <code>error</code> posture. Carries the affected user tags. |

## Telemetry

| Export | Meaning |
| --- | --- |
| <code>TelemetryEvent</code> | Event name in <code>type</code> plus event-specific fields. |
| <code>TelemetrySink</code> | Function receiving each emitted event. |
| <code>TelemetryEventName</code> | Union of all values in the event catalog. |
| <code>TELEMETRY_EVENTS</code> | Public event-name catalog for kernel, memory, React Router, and Cloudflare events, including reserved names. |
| <code>StrongDegradedReason</code> | Current reason union for <code>strong_degraded</code>: <code>registry_unreachable</code>. |
| <code>SinkErrorHook</code> | Optional observer for a sink failure that built-in delivery swallowed. |
| <code>TelemetryConfig</code> | Sink plus optional <code>hosted</code> flag, project <code>salt</code>, and <code>onSinkError</code>. |

A plain sink may receive raw identifiers. Hosted mode with a salt HMAC-pseudonymizes emitted string fields except the structural event type. Hosted mode without a salt suppresses events rather than forwarding raw strings. Built-in delivery swallows synchronous throws and asynchronous rejections from the sink; it also guards the optional error hook.

## Cloudflare adapter exports

Import these names from <code>@astilba/cache/cloudflare</code>. The subpath resolves only in a Workers-compatible runtime because <code>Coordinator</code> uses <code>cloudflare:workers</code>.

| Export | Purpose and current boundary |
| --- | --- |
| <code>createWorkersCache(config)</code> | Composes a Workers Clock and Rng, bounded memory L1, KV L2, named Coordinator Registry, lazily redialed Bus, and a request-driven recovery carrier. Construction performs no I/O. |
| <code>WorkersCacheConfig</code> | Requires <code>name</code>, <code>kv</code>, and <code>coordinator</code>; accepts optional <code>defaults</code>, kernel <code>telemetry</code>, and <code>takedownSensitive</code>. |
| <code>Coordinator</code> | Durable Object class the Worker must export and bind with a SQLite lifecycle declaration. Prefer the top-level <code>exports</code> map for a new Worker; legacy <code>migrations</code> remain supported. Its environment requires <code>REGISTRY_KV</code> and accepts Registry heartbeat and snapshot tuning variables. |
| <code>cloudflareKV(namespace)</code> | Builds the Cloudflare KV Store driver. |
| <code>doRegistry(source, regId?)</code> | Builds the thin Coordinator RPC Registry from a stub or a thunk that mints one. The Registry ID must match the named Durable Object identity. |
| <code>StubSource</code> | A Coordinator stub or a zero-argument stub factory. Use the thunk form when the Registry outlives a request. |
| <code>doBus(dial, options)</code> | Builds a mechanism-only WebSocket Bus client. It reports closure but does not reconnect itself. |
| <code>Dial</code> | Function returning a compatible client socket synchronously or asynchronously. |
| <code>DoBusOptions</code> | Requires <code>regId</code> and accepts an <code>onClose</code> status callback. |
| <code>DoBusCloseInfo</code> | Close code, reason, and whether the client initiated the closure. |
| <code>redialingDoBus(dial, options)</code> | Wraps <code>doBus()</code> with jittered exponential reconnection whose due attempts are performed by request-time ticks, never platform timers. |
| <code>RedialOptions</code> | Registry identity, injected Rng, and optional close callback, base delay, and jitter fraction. |
| <code>RedialingBusHandle</code> | A Bus with <code>tick(nowMs)</code>; a due tick performs one lazy redial and otherwise does nothing. |
| <code>InvalidRegistryNameError</code> | A named Coordinator identity violates the lowercase <code>[a-z0-9._-]</code>, 1–64-character Registry grammar. |

See [Cloudflare Workers](/docs/cache/cloudflare-workers/) for the binding relationship and operational limits.

## React Router adapter exports

Import these names from <code>@astilba/cache/react-router</code>. React and React Router are optional peer dependencies so root and Cloudflare-only consumers do not need them.

| Export | Purpose and current boundary |
| --- | --- |
| <code>cacheMiddleware(options)</code> | Creates React Router v8 server middleware that provides Cache, opens request and render frames, triggers poll ticks, and commits scope-safe response headers. |
| <code>CacheMiddlewareOptions</code> | Requires <code>cache</code>; accepts synchronous request identity derivation, <code>waitUntil</code>, telemetry, <code>onSinkError</code>, and an L3 budget override. |
| <code>CacheMiddlewareArgs</code> | The argument object React Router passes to server middleware, re-exported for identity mappers. |
| <code>cacheContext</code> | Typed Router context key. Loaders and actions read the request's Cache with <code>context.get(cacheContext)</code>. |
| <code>currentRequest()</code> | Returns the current AsyncLocalStorage-backed <code>RequestContext</code>, or <code>undefined</code> outside the middleware frame. |
| <code>POLL_TICK_FAILED</code> | The <code>"poll_tick_failed"</code> telemetry event name emitted when out-of-band recovery work rejects. |
| <code>TICK_MIN_INTERVAL_MS</code> | One-second minimum between middleware request-entry ticks for the same Cache instance. |
| <code>L3_INELIGIBLE</code> | The <code>"l3_ineligible"</code> event name emitted once for a demoted managed response. |
| <code>L3_BUDGET_DEFAULT</code> | Default 16 KB and 1,000-occurrence response-tag budget. |

On Cloudflare Workers, the package requires a compatibility date of 2024-09-23 or later and the <code>nodejs_compat</code> flag: the root uses <code>node:crypto</code>, and that flag also supplies the AsyncLocalStorage support used by this adapter. <code>nodejs_als</code> alone is insufficient. The middleware preserves an application-authored cache policy for eligible public renders, emits deduplicated user tags, and forces private posture for unsafe renders. It never writes <code>public</code> or <code>s-maxage</code>. See [React Router](/docs/cache/react-and-server-apps/).

## Export boundary

The publish configuration contains four supported doors: the root entry point, <code>./cloudflare</code>, <code>./react-router</code>, and <code>./package.json</code>. Deep source paths are not public APIs.

The source workspace also exposes <code>@astilba/cache/registry</code> so its test harness and Coordinator can share the state machine. The publish configuration deliberately omits it; applications must not import that source-only subpath.

For implementation gaps, inert fields, and integration availability, continue to [Implementation status](/docs/cache/api-status/).
