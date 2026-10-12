---
title: Inspect cache behavior
description: Witness stored entries with explain() and route operational events through telemetry sinks.
---

Astilba Cache exposes two complementary observability surfaces. <code>cache.explain()</code> takes a point-in-time witness of one public key, while telemetry reports operational events as they occur. Neither surface turns uncertainty into a stronger correctness claim.

## Inspect one key

Call <code>explain(key)</code> with the same application-facing key used by <code>getOrSet()</code>:

~~~ts
const explanation = await cache.explain(`product:${productId}`)

switch (explanation.kind) {
  case "present":
    console.log({
      tier: explanation.tier,
      identity: explanation.identity,
      verdict: explanation.verdict,
      reader: explanation.reader,
    })
    break
  case "absent":
    console.log("not present", explanation.reader)
    break
  case "read-failed":
    console.warn("presence unknown: a Store did not answer", explanation.reader)
}
~~~

The <code>kind</code> discriminant separates three materially different observations:

- <code>present</code> means a decodable entry was found in L1 or L2;
- <code>absent</code> means every probed tier answered and no decodable entry was found;
- <code>read-failed</code> means at least one classified Store read failure was suppressed and no other tier supplied the entry, so presence is unknown.

A missing, undecodable, or codec-incompatible entry is a reportable <code>{ kind: "absent", tier: "miss" }</code> result only when every probed Store answered. A classified Store failure is not laundered into the same answer: if one tier fails while another contains only an undecodable or incompatible entry, the result remains <code>read-failed</code>. That arm deliberately has no <code>tier</code> or <code>identity</code>.

| Result field | What it witnesses |
| --- | --- |
| <code>kind</code> | Whether the probe found an entry, proved absence, or could not establish either answer because a Store read failed. |
| <code>key</code> | The application-facing key passed to <code>explain()</code>. |
| <code>tier</code> | On <code>present</code>, the first decodable copy observed in L1 or L2. On <code>absent</code>, <code>miss</code>. Absent from <code>read-failed</code>. |
| <code>identity.tags</code> | The stored user and reserved tags. |
| <code>identity.scope</code> | The stored scope literal, or an explicit <code>unreadable</code> marker. |
| <code>identity.bornEpoch</code> | The invalidation epoch at which the entry was born. |
| <code>identity.ttl</code> | Stored TTL evidence. Current entries report <code>{ kind: "not-stored" }</code>. |
| <code>identity.kind</code> | A value or negative entry. |
| <code>verdict</code> | Current <code>fresh</code>, <code>stale</code>, <code>dead</code>, or <code>unknown</code> classification and the soft and hard epochs behind it. |
| <code>reader</code> | The local reader's applied epoch, suspicion state, terminal recovery state, and live-channel state. |

Without a coordinated invalidation reader, a present entry receives the codec-only fresh verdict and zero epochs. That says no local invalidation authority exists to classify it otherwise; it is not evidence from a Registry check.

<code>reader.channel</code> distinguishes <code>never-established</code>, <code>established</code>, and <code>lost</code>. This matters because a Bus that never completed its first handshake can still have <code>suspect: false</code>: no continuity gap has occurred, but there is also no established live channel. Read both fields when diagnosing delivery health.

## Understand the witness boundary

<code>explain()</code> reports what this Cache instance already knows. It deliberately does not:

- perform a live Registry check;
- trigger mirror recovery or resynchronization;
- hydrate an L2 result into L1;
- preserve a historical dependency graph;
- accept a scope argument.

The method canonicalizes the key at the default public scope. It cannot directly address tenant or principal-derived variants. Within a React Router request, <code>requestDependencies</code> can still show the dependencies the current render has recorded, including their scope evidence and any <code>l3</code> flag.

The L1 and L2 probes call each Store's ordinary <code>get()</code>. Cache performs no write while explaining, but a Store may have read behavior of its own; for example, <code>memory()</code> updates LRU recency on a successful read.

:::note[Use explain for diagnosis, not synchronization]
If the witness reports <code>suspect</code> or <code>unknown</code>, make the production read through the normal Cache API. That path applies the configured unknown policy, live checks, and bounded recovery rules. Calling <code>explain()</code> does not repair the state it describes.
:::

## Configure telemetry

Pass a sink directly for local use, or use <code>TelemetryConfig</code> when you need hosted pseudonymization or sink-failure reporting:

~~~ts
const cache = createCache({
  namespace: "storefront",
  clock,
  rng,
  l2,
  telemetry: {
    sink: (event) => logger.info(event),
    onSinkError: (error) => logger.warn({ error }, "cache telemetry failed"),
  },
})
~~~

Built-in event delivery swallows both synchronous sink throws and asynchronous sink rejections so observability code cannot fail a read, fill, or response. <code>onSinkError</code> is called for a swallowed failure; failures from that hook are swallowed too.

For a hosted sink, set <code>hosted: true</code> and provide a project <code>salt</code>. String fields other than the event type are HMAC-pseudonymized before delivery. Hosted mode without a salt suppresses the event instead of forwarding raw strings.

## Use the event catalog

<code>TELEMETRY_EVENTS</code> is the public catalog, and <code>TelemetryEventName</code> is the union of its values.

| Event | Current source behavior |
| --- | --- |
| <code>writeback_throttled</code> | A retryable throttled L2 write was suppressed and the origin result remained non-durable. |
| <code>l2_write_error</code> | Another retryable L2 write failure was suppressed. |
| <code>singleflight_option_mismatch</code> | Production mode ran incompatible same-key work separately. |
| <code>store_read_suppressed</code> | A classified <code>throttled</code> or <code>unavailable</code> Store read failed. The serving path treated that tier as a miss; the event includes the structural code and canonical key. |
| <code>neg_suppressed</code> | An opted-in 404 was suppressed in favor of a grace-eligible stale value, or the L2 negative-write guard refused it because L2 held a decodable value whose invalidation verdict was fresh or stale, or could not be established. |
| <code>private_evicted</code> | A configured <code>memory()</code> sink observed a principal-scoped entry evicted by an LRU bound. |
| <code>poll_tick_failed</code> | Request-driven recovery work rejected in the Workers carrier or React Router lifecycle integration. |
| <code>l3_ineligible</code> | React Router demoted a managed response for <code>budget</code>, <code>late_tag</code>, <code>scope</code>, or <code>scope_unreadable</code>. |
| <code>regid_divergence</code> | The Coordinator writes a Workers log when a derived Registry identity differs from its journaled identity. |
| <code>bus_dial_failed</code> | The invalidation reader received <code>lost</code> without a currently established channel: an initial dial or a later redial did not complete its <code>hello</code> handshake. |
| <code>strong_degraded</code> | A failed strong Registry check degraded that call to eventual because <code>onUnavailable: "eventual"</code> was configured. The current reason is <code>registry_unreachable</code>. |
| <code>registry_degraded</code> | Reserved in the catalog; no current emit site. |
| <code>state_stale</code> | Reserved in the catalog; no current emit site. |

The catalog closes the event-name vocabulary, not every event payload. Treat fields other than <code>type</code> according to the specific event you consume.

## Wire adapter and memory events separately

The React Router middleware accepts its own <code>telemetry</code> and <code>onSinkError</code> options for <code>poll_tick_failed</code> and <code>l3_ineligible</code>. Pass the same sink explicitly if you want those events beside kernel events:

~~~ts
cacheMiddleware({ cache, telemetry: sink, onSinkError, waitUntil })
~~~

The <code>memory()</code> driver also accepts <code>telemetry</code> and <code>onSinkError</code>. It emits <code>private_evicted</code> only when memory pressure removes a <code>usr:</code>-scoped entry, with count and byte information but no key, hash, or tag. TTL expiry, explicit deletion, and replacement do not emit that event.

<code>createWorkersCache()</code> accepts <code>telemetry</code> for kernel and Workers-carrier events. It still fixes its internal L1 construction without a separate memory sink, so receiving <code>private_evicted</code> from a memory L1 requires direct composition with <code>createCache()</code> and <code>memory({ telemetry })</code>. React Router middleware telemetry is likewise an explicit option; pass the same sink to both surfaces when you want one event stream.

Continue with [Cache HTTP responses](/docs/cache/response-caching/) for the response safety gate, [Control cache sharing](/docs/cache/scopes-and-privacy/) for private storage rules, or [API reference](/docs/cache/api-reference/) for the complete types.
