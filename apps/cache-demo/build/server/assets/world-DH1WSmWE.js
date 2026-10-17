import { r as __toESM, t as __commonJSMin } from "./rolldown-runtime-B-1-B7_t.js";
import { createHash, createHmac } from "node:crypto";
//#region ../../packages/cache/src/errors.ts
/**
* Thrown by every public entry point during the correctness-kernel phase. A RED
* invariant that fails with `NotImplementedError` is red for the right reason
* (the kernel is unimplemented), as opposed to a toolchain/config failure.
*/
var NotImplementedError = class extends Error {
	constructor(symbol) {
		super(`${symbol}: not implemented in the correctness-kernel phase`);
		this.name = "NotImplementedError";
	}
};
/**
* The single kernel-phase stub body. Every public function/method delegates here
* so the switch to real logic is a body replacement with zero signature churn.
* Callers pass their arguments through so parameters are genuinely referenced;
* inside an `async` method the thrown error becomes a rejected promise.
*/
function notImplemented(symbol, ..._args) {
	throw new NotImplementedError(symbol);
}
/**
* Distinguishes cache-originated timeouts (retriable — evidence about origin
* health) from caller aborts (not evidence of origin health).
*/
var CacheTimeoutError = class extends Error {
	source;
	constructor(source, message) {
		super(message ?? `cache operation timed out (source: ${source})`);
		this.name = "CacheTimeoutError";
		this.source = source;
	}
};
/**
* Thrown by a `strong` read when the registry is unreachable. Opt out with
* `onUnavailable: 'eventual'`.
*/
var RegistryUnavailableError = class extends Error {
	/**
	* `options.cause` carries the error this one displaced, where there was one. The serve-time
	* re-validation of a graced candidate runs INSIDE a failed fill: if the registry is unreachable
	* there, this error is what the caller sees instead of the factory failure that started it, so
	* the factory failure rides along rather than being dropped. Nothing is lost and callers get one
	* uniform type. Widened additively — `new RegistryUnavailableError(msg)` is unchanged.
	*/
	constructor(message, options) {
		super(message ?? "registry unreachable", options);
		this.name = "RegistryUnavailableError";
	}
};
/**
* Thrown by a read whose tag knowledge is UNKNOWN under `unknownPolicy: 'error'`.
*
* WHAT MAKES THIS DISTINCT FROM A MISS, AND WHY THE DISTINCTION IS THE POINT. Every other
* fail-closed resolution refills from origin, which is hard-safe exactly when the origin is truth.
* `'error'` is the policy for the case where it is NOT: a namespace under a takedown or a legal
* embargo. There, an origin that has not caught up yet can lose a race with the purge and hand back
* exactly the content that was supposed to vanish. So this policy declines to guess: it neither
* serves the unknown value nor replaces it
* with a fresh one, and the caller — who knows what the content is — decides. A policy that resolved
* to the same refill as `'miss'` would be a switch that reads as protection and does nothing, which
* is worse than no switch at all.
*
* Carries the key and the tags whose knowledge was unknown, so a caller can tell an embargoed
* dependency from a cold one. EXACTLY those: the tags this read could not establish, and no others.
* A payload of everything the entry declared cannot answer the question the field exists for — the
* caller would have to guess which of them was the problem, which is the guess this policy refuses
* to make on their behalf. Reserved (`__`-prefixed) tags never appear here: they are the kernel's
* own bookkeeping, they are unknown for exactly the same reason the user's are, and naming them
* tells the caller nothing they can act on.
*/
var UnknownTagError = class extends Error {
	key;
	tags;
	/**
	* `armedBy` names what selected this resolution — an explicit `unknownPolicy: 'error'`, or the
	* `takedownSensitive` override that outranks it. The message used to state the former
	* unconditionally, which was wrong precisely on the takedown path: the one place where the
	* operator most needs to know which switch fired is the one where the message named the other.
	*/
	constructor(key, tags, armedBy = "unknownPolicy") {
		const cause = armedBy === "takedownSensitive" ? "takedownSensitive is set" : "unknownPolicy is 'error'";
		super(`read for key ${JSON.stringify(key)} has unknown tag knowledge and ${cause} (§5a, E9)`);
		this.name = "UnknownTagError";
		this.key = key;
		this.tags = tags;
	}
};
/** A typed HTTP error carrying the offending status and response. */
var HttpError = class extends Error {
	status;
	response;
	constructor(response) {
		super(`HTTP ${response.status}`);
		this.name = "HttpError";
		this.status = response.status;
		this.response = response;
	}
};
/**
* Thrown at the `getOrSet`/`getOrSetEntry` write boundary when a caller-supplied
* tag is malformed — in particular a user tag beginning with the reserved `__`
* prefix. A real runtime error, never `NotImplementedError`: tag
* validation is not deferred kernel logic.
*/
var InvalidTagError = class extends Error {
	tag;
	constructor(tag, reason) {
		super(`invalid tag ${JSON.stringify(tag)}: ${reason}`);
		this.name = "InvalidTagError";
		this.tag = tag;
	}
};
/**
* Thrown by the plain `getOrSet` form when a fill is fenced past its write-back
* retry attempt budget: a hard delete keeps landing mid-fill/mid-
* refresh so the accepted result never postdates every purge visible at
* write-back, and the fill fails within its attempt budget rather than ever
* storing or serving a fenced value. A real runtime outcome, never
* `NotImplementedError` — the entry form (`getOrSetEntry`) surfaces the same
* outcome as `{ value: undefined }` instead of throwing.
*/
var FencedError = class extends Error {
	key;
	constructor(key, message) {
		super(message ?? `fill for key ${JSON.stringify(key)} fenced past its write-back attempt budget (§5.0/MI6)`);
		this.name = "FencedError";
		this.key = key;
	}
};
/**
* Thrown when a factory declares a dependency after it has settled: a
* `ctx.dependsOn`/`ctx.setTags` call reaching the context AFTER the factory promise
* resolved or rejected. The fill's tag set closes at settle, so a later declaration is
* outside the value's lifetime and cannot join it. A real runtime error, never
* `NotImplementedError`. Carries the offending key.
*/
var FactorySettledError = class extends Error {
	key;
	constructor(key) {
		super(`factory for key ${JSON.stringify(key)} declared a dependency after it settled (§2.1, E24)`);
		this.name = "FactorySettledError";
		this.key = key;
	}
};
/**
* The `EntryFactoryCtx.skip()` sentinel (M10, invariant 24) — a private
* throw/catch signal, analogous to how a classified factory error rides
* `fail()`. `skip()` is typed `never` so it never falsely resolves `undefined`
* as a caller's `T`; `fillAndStore` catches this exact class and turns it into
* a `{ skipped: true, value: undefined }` outcome — nothing is stored, nothing
* is served. Never part of the public surface (not exported from `index.ts`).
*/
var SkipSignalError = class extends Error {
	constructor() {
		super("ctx.skip() called — the fill is skipped (nothing stored, nothing served)");
		this.name = "SkipSignalError";
	}
};
//#endregion
//#region ../../packages/cache/src/registry/mirror-keys.ts
/** The default `regId` — single-registry deployments stay zero-config. */
var DEFAULT_REG_ID = "default";
/** The `regId` charset + length bound (`[a-z0-9._-]`, ≤ 64). */
var REG_ID_PATTERN = /^[a-z0-9._-]{1,64}$/u;
/**
* Does `regId` satisfy the charset/length (`[a-z0-9._-]`, 1–64 chars)? The pure predicate
* behind `assertRegId` — the SINGLE source of the grammar, so the machine's throwing gate and the
* adapter's name-derivation refusal (`resolveRegId`) test the same rule and can never drift.
*/
function isRegId(regId) {
	return REG_ID_PATTERN.test(regId);
}
/**
* Validate a `regId` against the charset/length (`[a-z0-9._-]`, 1–64 chars) — the gate
* that makes the scoped keyspace injective without escaping (the delimiter `:` cannot occur
* inside a `regId`). Called at `configure` time (the machine's single seeding path), so a
* malformed id fails LOUD before it can ever mint an ambiguous key. Throws on violation.
*/
function assertRegId(regId) {
	if (!isRegId(regId)) throw new Error(`invalid regId ${JSON.stringify(regId)} — must match [a-z0-9._-], length 1-64 (E19)`);
}
/**
* Build the registry-scoped mirror keys for `regId`. `regId` is expected to be
* already validated (it enters the machine only via `configure`, which calls `assertRegId`);
* this is a pure formatter on the hot flush/resync path, so it does not re-validate. Both the
* writer (execute.ts) and the reader (invalidation.ts) address keys ONLY through here.
*/
function mirrorKeys(regId) {
	const prefix = `__reg:${regId}:`;
	return {
		deltaKey: (seq) => `${prefix}delta:${seq}`,
		snapKey: (seq) => `${prefix}snap:${seq}`,
		stateKey: `${prefix}state`
	};
}
//#endregion
//#region ../../packages/cache/src/tags.ts
/** The two reserved tag namespaces: the per-key digest and the
* per-namespace watermark. A user tag beginning with this prefix is rejected
* at write — only the kernel itself may stamp `__k:<h>` / `__ns:<name>`. */
var RESERVED_TAG_PREFIX = "__";
/** Whether `tag` falls in the reserved `__`-prefixed namespace. */
function isReservedTag(tag) {
	return tag.startsWith(RESERVED_TAG_PREFIX);
}
/**
* Validate one caller-supplied tag at the write boundary. Throws a
* real `InvalidTagError` — never `NotImplementedError` — the instant a
* `__`-prefixed tag reaches `getOrSet`/`getOrSetEntry`/a purge selector, so a
* forged reserved tag can never be smuggled in as a "user" tag.
*
* Division of labor: the CHARSET/length/non-empty authority is the builders — `t`
* validates-and-rejects, `compound` escapes — so a `Tag` they produce is already
* well-formed. This write-time check is deliberately the NARROW `__`-prefix backstop,
* not a re-validation: it catches only the one class the type system cannot — a
* `__`-prefixed string force-branded to `Tag` (a raw `as Tag` cast, a harness `mkTag`,
* any hand-forged brand) that never passed through a builder. It intentionally does NOT
* re-check charset — narrow, not weak.
*/
function assertUserTag(tag) {
	if (isReservedTag(tag)) throw new InvalidTagError(tag, `user tags may not begin with the reserved "${RESERVED_TAG_PREFIX}" prefix (§2.3, MI13)`);
}
/** The full tag charset: the design's `[a-z0-9:_\-.|]` extended with
* `%` (`compound()`'s escape byte). One or more of these, nothing else. `-` is placed
* last so it is a literal, not a range. */
var TAG_CHARSET = /^[a-z0-9:_.|%-]+$/u;
/** Maximum tag size — a UTF-8 BYTE budget, not code units. */
var MAX_TAG_BYTES = 256;
/** UTF-8 byte length of `s` — the tag budget is bytes, not string length. */
function byteLength(s) {
	return new TextEncoder().encode(s).length;
}
/**
* Template helper for a single tag, e.g. ``t`book:${id}` ``. The tag TRUST
* BOUNDARY: the interpolations (`values`) are UNTRUSTED user data, so `t` assembles the
* template, then VALIDATES-and-REJECTS the final string against the full grammar
* (non-empty, charset, ≤256 UTF-8 bytes, not `__`-prefixed) — throwing `InvalidTagError`
* on any violation. It never escapes-by-guess: escaping is `compound()`'s separate
* contract, and silently rewriting an interpolation is exactly how tag injection slips
* through. On success returns the branded `Tag`.
*/
function t(strings, ...values) {
	let assembled = "";
	for (const [i, literal] of strings.entries()) {
		assembled += literal;
		if (i < values.length) assembled += String(values[i]);
	}
	if (assembled.length === 0) throw new InvalidTagError(assembled, "a tag must be non-empty (§2.3)");
	assertUserTag(assembled);
	if (!TAG_CHARSET.test(assembled)) throw new InvalidTagError(assembled, "a tag character is outside the [a-z0-9:_-.|%] grammar (§2.3, E10)");
	if (byteLength(assembled) > MAX_TAG_BYTES) throw new InvalidTagError(assembled, `a tag may not exceed ${MAX_TAG_BYTES} UTF-8 bytes (§2.3)`);
	return assembled;
}
//#endregion
//#region ../../packages/cache/src/telemetry.ts
/**
* The telemetry event-name CATALOG — the single, kernel-hosted registry of every
* event `type` this library emits, the same shape as the exported error classes. Each member is an
* event NAME; the value equals the key so a producer can reference a name as `TELEMETRY_EVENTS.<name>`
* (a literal type, refactor-safe) while `TelemetryEventName` (below) is the union of them all.
*
* The names are grouped by their emitting LAYER (kernel / L1 driver / react-router adapter /
* cloudflare adapter) — the annotation the charter requires. The machine-checked layer map (which
* fails to compile if a member is unannotated) lives in the catalog's exhaustiveness tooth in the
* test lane.
*
* GROWTH: adding a name is ADDITIVE and consumer-SAFE (a sink RECEIVES events, so a producer that
* emits a wider vocabulary hands a consumer a value from a set it already types against — narrowing,
* not widening, at the receiver) — so a new name is a MINOR-version change. Removing or renaming one
* is breaking.
*/
var TELEMETRY_EVENTS = {
	writeback_throttled: "writeback_throttled",
	l2_write_error: "l2_write_error",
	singleflight_option_mismatch: "singleflight_option_mismatch",
	store_read_suppressed: "store_read_suppressed",
	neg_suppressed: "neg_suppressed",
	private_evicted: "private_evicted",
	poll_tick_failed: "poll_tick_failed",
	l3_ineligible: "l3_ineligible",
	regid_divergence: "regid_divergence",
	bus_dial_failed: "bus_dial_failed",
	strong_degraded: "strong_degraded",
	registry_degraded: "registry_degraded",
	state_stale: "state_stale"
};
/** Report a swallowed sink failure to the injected hook, itself guarded. The hook is app code at the
* very end of the swallow chain, so its own failure has no meta-channel to report to — swallowed, so
* `safeEmit` cannot throw or float through it. */
function reportSinkError(hook, error) {
	if (hook === void 0) return;
	const called = hook;
	try {
		Promise.resolve(called(error)).catch(() => {});
	} catch {}
}
/**
* Emit ONE event through an app-supplied sink so that NOTHING it does can escape (
* the swallow-wrap extracted from the poll driver and made shared). A sink is arbitrary app code;
* both of its failure modes are neutralized here:
*
*   • a SYNCHRONOUS throw → the `try`/`catch` (without it the caller's own path would throw);
*   • a REJECTING thenable → the returned value is ADOPTED (`Promise.resolve(...).catch(...)`) so a
*     rejecting `async` sink can never float out as an unhandled rejection.
*
* On either failure the injected `onSinkError` hook is called exactly once for that emit. `safeEmit`
* NEVER throws and returns nothing the caller must await (adopt-don't-await: it must not inherit
* app-code latency on a background or response path).
*
* The TYPE is the structural tooth: `event` is a `TelemetryEvent`, whose `type` is the CATALOG
* union — so an off-catalog event name is a COMPILE error at the call site, never a grep finding.
*/
function safeEmit(sink, event, onSinkError) {
	if (sink === void 0) return;
	const called = sink;
	try {
		Promise.resolve(called(event)).catch((error) => {
			reportSinkError(onSinkError, error);
		});
	} catch (error) {
		reportSinkError(onSinkError, error);
	}
}
//#endregion
//#region ../../packages/cache/src/invalidation.ts
/** The one reason class a `strong_degraded` emit carries today. */
var STRONG_DEGRADED_REGISTRY_UNREACHABLE = "registry_unreachable";
/** The channel's starting state, and the one a reader returns to saying rather than assuming: it has
* never heard a `hello`. Named once so the three sites that compare against it cannot drift. */
var NEVER_ESTABLISHED = "never-established";
/** Build the `transient-miss` retry signal for `seq` (the single runtime literal source). */
function transientMiss(seq) {
	return {
		kind: "transient-miss",
		seq
	};
}
/** Narrow an `unknown` to an index-readable object, or `undefined`. The
* single narrowing assertion in this module: every field read off the result is
* individually type-checked below before use, so the record view is sound. */
function parseValueObject(value) {
	if (typeof value !== "object" || value === null) return;
	return value;
}
/** Parse a stored JSON string to an object, or `undefined` on a malformed body. */
function parseJsonObject(raw) {
	let parsed;
	try {
		parsed = JSON.parse(raw);
	} catch {
		return;
	}
	return parseValueObject(parsed);
}
/** Read the `state` pointer shape from stored bytes. Malformed — including a
* non-string or absent `regId` echo — ⇒ undefined (fail closed). */
function readPointer(raw) {
	const o = parseJsonObject(raw);
	if (!o || typeof o.epoch !== "number" || typeof o.lastBatch !== "number" || typeof o.regId !== "string") return;
	const snapBatch = typeof o.snapBatch === "number" ? o.snapBatch : 0;
	const flushedAt = typeof o.flushedAt === "number" ? o.flushedAt : 0;
	return {
		epoch: o.epoch,
		flushedAt,
		lastBatch: o.lastBatch,
		regId: o.regId,
		snapBatch
	};
}
/**
* Read one watermark change from a parsed value. A CORRUPT change ⇒ `undefined`
* (fail closed), DISTINGUISHED from a legitimately-absent optional axis:
*   • a PRESENT-but-non-number `softEpoch`/`hardEpoch` (the field is on the object but is not a
*     `number`) is corruption — dropping it and applying the rest would silently delete a purge
* from history (a serve-dead vector under non-canonical KV writes);
*   • an AXIS-EMPTY change (a `tag` but NEITHER a numeric `softEpoch` NOR a numeric `hardEpoch`)
* violates the ≥1-axis rule.
* An ABSENT optional axis stays legal — a change may legitimately touch a single axis. A corrupt
* change holes its whole batch via `readFrame` (never a partial-apply); the caller fails closed.
*/
function readChange(value) {
	const rec = parseValueObject(value);
	if (!rec) return;
	const { tag } = rec;
	if (typeof tag !== "string") return;
	const soft = rec.softEpoch;
	const hard = rec.hardEpoch;
	if ("softEpoch" in rec && typeof soft !== "number" || "hardEpoch" in rec && typeof hard !== "number") return;
	if (typeof soft !== "number" && typeof hard !== "number") return;
	return {
		tag,
		...typeof soft === "number" ? { softEpoch: soft } : {},
		...typeof hard === "number" ? { hardEpoch: hard } : {}
	};
}
/**
* Read a `__delta` batch frame from stored bytes. Malformed ⇒ undefined. Exported so the
* doBus wire codec (`src/adapters/cloudflare/wire.ts`) validates a wire-carried frame through the
* SAME frame parser the KV-resync path uses — one parser for the durable mirror AND the live channel,
* so a corrupt wire change fails closed exactly like a corrupt `__delta` byte (never a partial
* apply). Additive export only: no reader branch changes, no behavior change (the corrupt-change
* fail-closed semantics are pinned by `reader-corrupt-change-fail-closed`).
*/
function readFrame(raw) {
	const o = parseJsonObject(raw);
	if (!o || typeof o.fromEpoch !== "number" || typeof o.toEpoch !== "number" || !Array.isArray(o.changes)) return;
	const changes = [];
	for (const entry of o.changes) {
		const change = readChange(entry);
		if (!change) return;
		changes.push(change);
	}
	return {
		changes,
		fromEpoch: o.fromEpoch,
		toEpoch: o.toEpoch
	};
}
/** Merge a frame's watermark changes into a knowledge map (max-on-write). */
function applyChanges(known, frame) {
	for (const change of frame.changes) {
		const w = known.get(change.tag) ?? {
			hardEpoch: 0,
			softEpoch: 0
		};
		if (change.softEpoch !== void 0) w.softEpoch = Math.max(w.softEpoch, change.softEpoch);
		if (change.hardEpoch !== void 0) w.hardEpoch = Math.max(w.hardEpoch, change.hardEpoch);
		known.set(change.tag, w);
	}
}
/** The validity table over an entry's `bornEpoch` and per-tag knowledge. */
function classify(bornEpoch, knowledge) {
	if (knowledge.some((k) => !k.known)) return "unknown";
	if (knowledge.some((k) => k.known && k.hardEpoch > bornEpoch)) return "dead";
	if (knowledge.some((k) => k.known && k.softEpoch > bornEpoch)) return "stale";
	return "fresh";
}
/** The highest `throughEpoch` a live check verified (0 if none is `known`). */
function throughEpochOf(knowledge) {
	let max = 0;
	for (const k of knowledge) if (k.known && k.throughEpoch > max) max = k.throughEpoch;
	return max;
}
/** Default liveness heartbeat interval in ms — the cadence at which a live writer refreshes the
* pointer's freshness field. The reader mirrors the coordinator's `REGISTRY_HEARTBEAT_MS`; the
* 2b-2-b shell threads the real value at construction. Suspicion raises after `3 ×` this much
* OBSERVED SILENCE — an elapsed-time derivative, sampling-rate-independent (not a tick count). */
var DEFAULT_HEARTBEAT_INTERVAL_MS = 3e4;
var SUSPICION_SILENCE_MULTIPLE$1 = 3;
/**
* The per-isolate invalidation state. Tracks tri-state tag knowledge
* fed by the bus, the highest contiguously-applied epoch, and a `suspect` flag
* raised on any gap/reconnect that a resync must clear before warm trust resumes.
*Tags absent from the map are `unknown` and fail closed (invariant 11).
*/
var Invalidation = class {
	#registry;
	#stateStore;
	/** The bus, HELD as an address rather than a subscription. Construction wires; it does not
	* dial. The first read that needs the channel subscribes (`#ensureSubscribed`), inside the request
	* context that asked — which is the only context where a dial is permitted to happen at all. */
	#bus;
	/** Telemetry for the reader's own raw facts — currently `bus_dial_failed`. Optional: a cache
	* built with no sink emits nothing, and the reader never depends on the emit for correctness. */
	#emit;
	/** This isolate's mirror-key scope, read from `registry.regId` at construction. It
	* both scopes the keys this reader resyncs off (`mirrorKeys(#regId)`) AND is the value the
	* pointer's echoed `regId` must equal — a mismatch reads a foreign keyspace and fails closed. */
	#regId;
	#subscription = null;
	/** `close()` was called: no further subscribe may happen (a lazy dial must not resurrect a closed
	* reader — the one way deferral could outlive its owner). */
	#closed = false;
	/** The live channel's state — see `ChannelState`. Starts at the honest answer: this reader
	* has never heard a `hello`, and until it does it says so rather than resting on silence. */
	#channel = NEVER_ESTABLISHED;
	/** tag → applied watermark; absence ⇒ `unknown` (tri-state). */
	#known = /* @__PURE__ */ new Map();
	/** Highest contiguous epoch applied from bus frames / resync. Drives gap
	* detection — must stay strictly contiguous. */
	#appliedEpoch = 0;
	/** Highest epoch this isolate has VERIFIED from any authority — the contiguous bus
	* `#appliedEpoch` OR a live check's `throughEpoch`. This is the writer's
	* applied registry epoch for bornEpoch minting: a fill after a verified
	* live check must postdate what that check observed, so a legitimate refill on a
	* behind isolate is not fenced by its own recorded watermark. */
	#knownThroughEpoch = 0;
	/** Raised on a bus gap or reset; forces resync before warm trust. While raised,
	* every tag is treated as unknown at eventual reads — a possibly-gapped map is not the
	* merely-degraded warm map Q4 lets an isolate keep serving on. */
	#suspect = false;
	/** The max head DECLARED over the currently-open live channel during THIS suspect episode
	* — from a LOSS-revealing event only: `hello.head`, `gap.head`, or a NON-CONTIGUOUS
	* frame's `toEpoch`. A contiguous frame never declares (it only reaches toward a named head).
	* `null` until a head is declared this episode; a `reset` discards it (new episode), a `gap`
	* RAISES it (≥ the frame it lost) — so a hidden purge behind a hole is accounted before
	* convergence, never skipped (serve-dead — the shape). Convergence anchors
	* to THIS, never to pointer freshness. */
	#headThisEpisode = null;
	/** The `flushedAt` of the pointer at the last observation that ADVANCED it. `null` until
	* the first observation seeds the baseline. Compared DERIVATIVELY — "did it advance?" via
	* EQUALITY/`>` — never as an absolute-freshness anchor ( : `flushedAt` may not clear
	* suspicion nor date the map). */
	#lastFlushedAt = null;
	/** The `nowMs` of the previous observation — a MONOTONICITY guard so a stale/reordered tick
	* (`nowMs ≤` this) is a true no-op, NOT an age anchor (it is never compared to `flushedAt`).
	* `null` until the first observation. */
	#lastObservationMs = null;
	/** The `nowMs` at which the current unbroken SILENCE began — the last observation that seeded
	* the baseline or saw `flushedAt` advance. `nowMs − this` is the observed silence; reaching
	* `#suspicionAfterMs` RAISES suspicion (derivative, raise-only, never clears). `null` until the
	* first observation. */
	#silenceStartMs = null;
	/** Raise suspicion once OBSERVED SILENCE reaches this — `3 × heartbeatIntervalMs`, fixed at
	* construction. Elapsed silence is sampling-rate-INDEPENDENT: fast ticks cannot witness this much
	* silence in less than this much time, so a false raise is structurally impossible. */
	#suspicionAfterMs;
	constructor(registry, stateStore, bus, heartbeatIntervalMs = DEFAULT_HEARTBEAT_INTERVAL_MS, emit) {
		this.#registry = registry;
		this.#emit = emit;
		this.#suspicionAfterMs = SUSPICION_SILENCE_MULTIPLE$1 * heartbeatIntervalMs;
		this.#stateStore = stateStore;
		this.#regId = registry.regId;
		this.#bus = bus;
	}
	/**
	* Subscribe to the invalidation bus if this reader has not already — THE LAZY DIAL,
	* and the deferral the reachability rule requires of construction. Idempotent and synchronous, so any entry point
	* may call it unconditionally; the dial itself is the transport's business and may complete long
	* after this returns.
	*
	* CALL IT FROM EVERY PATH THAT IS "USE" — the read path (`validate`), the tick path (`resync` /
	* `resyncViaSnapshot`), and the purge verbs, which are registry RPCs and therefore already inside
	* a request context. NEVER from `explain`: a witness that dialed would be testimony rather than
	* introspection, which is why `explainVerdict` is the one reader entry point that does not
	* appear on that list.
	*
	* NOTHING IS MISSED BY SUBSCRIBING LATE. A purge published before this reader was listening is not
	* lost: every establishment handshakes `reset` → `hello{head}`, which suspends warm trust and
	* anchors a resync against the durable `__state`/`__delta` mirror. The live channel is an
	* optimization over that mirror, never the sole path — so the cost of subscribing at first use is
	* one cold resync, and the benefit is an isolate that can be built at module scope at all.
	*
	* The subscriber consumes a BusEvent union: a `reset` (transport (re)establishment) or a `gap`
	* (dropped frame) marks the map suspect and forces a resync before warm trust resumes; a `frame`
	* takes the contiguity path; a `lost` records the channel's own state.
	*/
	ensureSubscribed() {
		if (this.#subscription !== null || this.#closed) return;
		this.#subscription = this.#bus.subscribe((event) => {
			this.#applyEvent(event);
		});
	}
	get appliedEpoch() {
		return this.#appliedEpoch;
	}
	/** Whether this isolate currently DISTRUSTS its warm map — read-only. A read
	* arriving while suspect fails closed (the gate); the shell reads it to know whether
	* the ladder still has work. Raised by a bus gap/reset OR the derivative heartbeat miss;
	* cleared ONLY by `#tryConverge` reaching a live-channel head (never by pointer freshness). */
	get suspect() {
		return this.#suspect;
	}
	/** The live channel's state — read-only exposition of state this reader already tracks for
	* its own use, never new bookkeeping added for the witness. */
	get channel() {
		return this.#channel;
	}
	close() {
		this.#closed = true;
		this.#subscription?.close();
		this.#subscription = null;
	}
	/** Advance the contiguous applied epoch (and the verified high-water) to `toEpoch`. */
	#advanceApplied(toEpoch) {
		this.#appliedEpoch = toEpoch;
		this.#knownThroughEpoch = Math.max(this.#knownThroughEpoch, toEpoch);
	}
	/**
	* Dispatch one bus event. Convergence anchors to a head DECLARED this episode,
	*from exactly the LOSS-REVEALING events: a `hello` (the channel head), a `gap`
	* (its `head`, ≥ the frame it lost), or a NON-CONTIGUOUS frame (its `toEpoch`, the hole
	* it proves). A CONTIGUOUS frame does NOT declare — it only reaches toward a head already named.
	* A `reset` discards the anchor (a new episode; the following `hello` re-declares). The kernel,
	* not the bus, owns contiguity validation.
	*/
	#applyEvent(event) {
		switch (event.kind) {
			case "frame":
				this.#applyFrame(event.frame);
				return;
			case "hello":
				this.#channel = "established";
				this.#headThisEpisode = Math.max(this.#headThisEpisode ?? 0, event.head);
				this.#tryConverge();
				return;
			case "lost": {
				const healthyClose = this.#channel === "established";
				this.#channel = this.#channel === NEVER_ESTABLISHED ? NEVER_ESTABLISHED : "lost";
				if (!healthyClose) this.#emit?.({ type: TELEMETRY_EVENTS.bus_dial_failed });
				return;
			}
			case "gap":
				this.#suspect = true;
				this.#headThisEpisode = Math.max(this.#headThisEpisode ?? 0, event.head);
				return;
			case "reset":
				this.#suspect = true;
				this.#headThisEpisode = null;
				return;
			default: return event;
		}
	}
	/** Apply a bus frame if contiguous; otherwise raise suspicion. */
	#applyFrame(frame) {
		if (frame.fromEpoch !== this.#appliedEpoch) {
			this.#suspect = true;
			this.#headThisEpisode = Math.max(this.#headThisEpisode ?? 0, frame.toEpoch);
			return;
		}
		applyChanges(this.#known, frame);
		this.#advanceApplied(frame.toEpoch);
		this.#tryConverge();
	}
	/**
	* Clear suspicion iff a head was DECLARED over the live channel this episode (hello/frame)
	* AND the applied epoch has reached it. `#appliedEpoch` advances only by contiguous
	* application, so `applied >= head` already encodes hole-free reach (β) — no separate holes
	* check. Pointer freshness never arbitrates clearing: a heartbeat refreshes the pointer's
	* freshness field without minting epochs, so freshness motion is not evidence the reader has
	* caught up; only a live-channel head declaration anchors convergence. Bus-down /
	* poll-only declares no head ⇒ sticky suspect (posture, priced).
	*/
	#tryConverge() {
		if (this.#suspect && this.#headThisEpisode !== null && this.#appliedEpoch >= this.#headThisEpisode) this.#suspect = false;
	}
	/**
	* Cold/resync path — the bounded single DELTA pass. Read the `__state`
	* pointer from L2 and replay the `(applied, lastBatch]` delta batches — durably write-once
	* in L2 even when the bus frame was dropped — applying the contiguous prefix (β), which
	* advances `#appliedEpoch`. A missing pointer or a hole leaves affected knowledge `unknown`
	* (fail closed). Resync does NOT clear suspicion off pointer freshness: a heartbeat refreshes the
	* freshness field without minting epochs, so freshness motion is not evidence of catch-up.
	*It only APPLIES the durable prefix; clearing is `#tryConverge`'s job, and fires iff
	* a head was DECLARED over the live channel this episode (hello/frame) AND this replay carried
	* the applied epoch up to it.
	*
	* SURFACES A STATUS (2b-2-a): the walk's hole is no longer discarded — it is returned so the
	* SHELL (2b-2-b) can schedule the retry ladder off a pure decision. `resync` NEVER sleeps; the
	* escalation across attempts is driven by the caller's `policy.deltasExhausted`, not by time.
	*
	* THE RETRY LADDER NEVER BLOCKS A READ: `validate()`'s awaited resync stays THIS bounded fast
	* pass; a read arriving during `transient-miss` classifies `unknown` and takes the posture
	* NOW — the ladder heals the future, not the present. Fail-closed means what it says. (So
	* `validate` calls `resync()` with the default policy and ignores the status; it does not wait
	* on retries, and a `transient-miss`/`needs-snapshot`/`terminal-gap` read fails closed at once
	* via the gate.)
	*/
	async resync(policy) {
		this.ensureSubscribed();
		const deltasExhausted = policy?.deltasExhausted ?? false;
		const mirror = await this.#readMirrorPointer();
		if (!mirror) {
			this.#tryConverge();
			return transientMiss(0);
		}
		const hole = await this.#replayBatches(mirror.store, mirror.keys, 1, mirror.pointer.lastBatch);
		this.#tryConverge();
		if (hole === null) return "converged";
		return deltasExhausted ? "needs-snapshot" : transientMiss(hole);
	}
	/**
	* The SNAPSHOT bridge — the escalation the shell drives after `resync` returns
	* `needs-snapshot`. Load ONLY the pointer-blessed snapshot `__snap:<pointer.snapBatch>` (a
	* from-0 `BusFrame`, `fromEpoch:0`, parsed by the SAME `readFrame`), apply it as an ABSOLUTE
	* base (`#appliedEpoch` ← `snap.toEpoch` when ahead — bridging a hole in the now-GC-eligible
	* region below `snapBatch`), then replay the tail `(snapBatch, lastBatch]`. Never fetches an
	* UNBLESSED snap nor a delta past `lastBatch` (the over-fetch discipline the contract pins).
	*
	* A lagging `snapBatch` is FIRST-CLASS NORMAL (1c): `snapBatch` may trail `lastBatch`
	* arbitrarily — the arithmetic on `(snapBatch, lastBatch]` is correct, no warning, no anomaly.
	*
	* BLESSED-BUT-MISSING SNAP: if `__snap:<snapBatch>` is itself absent, give it ONE brief retry
	* (`transient-miss` — it may be lagging); once the caller's snapshot-budget is spent
	* (`snapshotExhausted`), rule permanently — a FULL delta replay from the floor if the chain
	* `(0, lastBatch]` is intact (⇒ `converged`), else `terminal-gap`. No spin, no serve-through.
	*/
	async resyncViaSnapshot(policy) {
		this.ensureSubscribed();
		const snapshotExhausted = policy?.snapshotExhausted ?? false;
		const mirror = await this.#readMirrorPointer();
		if (!mirror) {
			this.#tryConverge();
			return transientMiss(0);
		}
		const { store, keys, pointer } = mirror;
		const snapRaw = await store.get(keys.snapKey(pointer.snapBatch), "snap");
		const snap = snapRaw ? readFrame(snapRaw.value) : void 0;
		if (!snap) {
			if (!snapshotExhausted) {
				this.#tryConverge();
				return transientMiss(pointer.snapBatch);
			}
			const holeNoSnap = await this.#replayBatches(store, keys, 1, pointer.lastBatch);
			this.#tryConverge();
			return holeNoSnap === null ? "converged" : "terminal-gap";
		}
		this.#applySnapshotBase(snap);
		const tailHole = await this.#replayBatches(store, keys, pointer.snapBatch + 1, pointer.lastBatch);
		this.#tryConverge();
		if (tailHole === null) return "converged";
		return transientMiss(tailHole);
	}
	/**
	* Read the `__state` pointer through the registry-scoped keys, returning the scoped
	* `store`/`keys` alongside it, or `undefined` when the pointer is absent/malformed OR echoes a
	* foreign `regId` (a scheme/prefix divergence or another registry's pointer — fail closed).
	*/
	async #readMirrorPointer() {
		const store = this.#stateStore;
		const keys = mirrorKeys(this.#regId);
		const raw = await store.get(keys.stateKey, "pointer");
		const pointer = raw ? readPointer(raw.value) : void 0;
		if (!pointer || pointer.regId !== this.#regId) return;
		return {
			keys,
			pointer,
			store
		};
	}
	/**
	* Replay batches `fromSeq..lastBatch` (XRANGE discipline), applying each whose
	* `fromEpoch` is contiguous with the applied epoch. Returns the seq of the FIRST hole
	* (a missing/garbled/non-contiguous forward batch — fail closed), or `null` when the walk
	* reached `lastBatch` hole-free. `fromSeq` is `1` for a from-floor `(0, lastBatch]` replay and
	* `snapBatch + 1` for a snapshot-bridged tail `(snapBatch, lastBatch]` — so GC'd batches below
	* a snapshot floor are never miscounted as holes. Reads ONLY `deltaKey(fromSeq..lastBatch)` —
	* never a seq past the pointer — through the SAME `keys` the pointer was read with (
	* over-fetch discipline).
	*/
	async #replayBatches(store, keys, fromSeq, lastBatch) {
		let firstHole = null;
		for (let seq = fromSeq; seq <= lastBatch; seq++) {
			const batchRaw = await store.get(keys.deltaKey(seq), "delta");
			const frame = batchRaw ? readFrame(batchRaw.value) : void 0;
			const applied = frame ? this.#applyIfContiguous(frame) : false;
			if (!(frame && applied) && firstHole === null) firstHole = seq;
		}
		return firstHole;
	}
	/** Apply a batch frame iff it advances the applied epoch contiguously. */
	#applyIfContiguous(frame) {
		if (frame.toEpoch <= this.#appliedEpoch) return true;
		if (frame.fromEpoch !== this.#appliedEpoch) return false;
		applyChanges(this.#known, frame);
		this.#advanceApplied(frame.toEpoch);
		return true;
	}
	/**
	* Apply a from-0 snapshot frame as an ABSOLUTE base: merge its watermarks (max-on-write,
	* never regressing a higher already-known value) and JUMP the applied epoch to `snap.toEpoch`
	* when the snapshot is AHEAD — a complete `(0, snap.toEpoch]` checkpoint carries its own
	* contiguity from the floor, so no `fromEpoch === applied` gate applies. A snapshot BEHIND the
	* applied epoch (a lagging `snapBatch` — first-class normal) adds nothing and never regresses.
	*/
	#applySnapshotBase(snap) {
		if (snap.toEpoch <= this.#appliedEpoch) return;
		applyChanges(this.#known, snap);
		this.#advanceApplied(snap.toEpoch);
	}
	/**
	* derivative suspicion RAISING — a PURE state update fed one pointer
	* observation. A heartbeat refreshes the pointer's freshness field without minting epochs, so
	* freshness MOTION is not evidence the reader has caught up; only a live-channel head declaration
	* anchors convergence. This monitor uses freshness the OTHER way — to RAISE: it tracks OBSERVED
	* SILENCE (how long `flushedAt` has failed to advance) and raises `#suspect` once that silence
	* reaches `#suspicionAfterMs`. DERIVATIVE-ONLY, RAISE-ONLY: `flushedAt` never clears suspicion nor
	* anchors absolute freshness — only `>`/equality ACROSS observations, only to RAISE.
	* `nowMs` is compared ONLY to our own prior watch-times (`#lastObservationMs`, `#silenceStartMs`),
	* NEVER to `flushedAt`, so no absolute-age anchor sneaks in. Measuring ELAPSED silence (not
	* counting non-advancing ticks) makes the raise SAMPLING-RATE-INDEPENDENT: fast ticks cannot
	* witness `#suspicionAfterMs` of silence in less time, and a slow-but-live writer resets the
	* window. The shell feeds it observations (2b-2-b); here explicit test ticks.
	*/
	notePointerObservation(pointer, nowMs) {
		if (this.#lastObservationMs !== null && nowMs <= this.#lastObservationMs) return;
		this.#lastObservationMs = nowMs;
		if (this.#lastFlushedAt === null || this.#silenceStartMs === null) {
			this.#lastFlushedAt = pointer.flushedAt;
			this.#silenceStartMs = nowMs;
			return;
		}
		if (pointer.flushedAt > this.#lastFlushedAt) {
			this.#lastFlushedAt = pointer.flushedAt;
			this.#silenceStartMs = nowMs;
			return;
		}
		if (nowMs - this.#silenceStartMs >= this.#suspicionAfterMs) this.#suspect = true;
	}
	/**
	* The poll DECISION — a PURE, deterministic predicate: has at least `thresholdMs`
	* elapsed since the last poll? The base cadence is maxSyncLag (60s) and the ±20% jitter is the
	* SHELL's rng job (2b-2-b) — both enter as the caller-computed `thresholdMs`, so no ambient
	* time/rng lives here. The poll DRIVER (the timer that calls this) is 2b-2-b.
	*/
	pollDue(nowMs, lastPollMs, thresholdMs) {
		return nowMs - lastPollMs >= thresholdMs;
	}
	/**
	* The `bornEpoch` to stamp into a fresh envelope at fill start, read
	* SYNCHRONOUSLY from LOCAL VERIFIED knowledge — the highest epoch this isolate has
	* observed from any authority (contiguous bus applies OR a recorded live check),
	* never a fresh live read. So the default read path keeps the zero-round-trip cost
	* model, while a fill that follows a verified live check postdates what that check
	* observed — a legitimate refill on a behind isolate is not fenced by its own
	* recorded watermark. A lagging high-water can only UNDER-state bornEpoch ⇒
	* over-invalidation ⇒ the safe direction. The caller snapshots
	* this EARLY as the fence-critical floor (a concurrent bus purge landing after the
	* snapshot cannot lift it, so INV-9/12 hold) and only raises the fill's bornEpoch by
	* the epoch its OWN reads verified (via `validate`'s `checkedThrough`), never by a
	* concurrent frame — see `runGetOrSet`.
	*/
	localBornEpoch() {
		return this.#knownThroughEpoch;
	}
	/**
	* The verdict `explain` reports — a PURE read of CURRENT local knowledge: classify
	* the envelope against what this isolate knows NOW, with NO live check, NO resync, and NO
	* recording. It is exposition, not testimony: it adds no tracked state and mutates nothing (the
	* introspection-not-testimony line). A suspect map fails closed to `unknown` (the posture
	* an eventual read would take), and `unknown` stays `unknown`. The soft/hard watermarks are the
	* evidence the bornEpoch was weighed against — the purge epochs behind a `stale`/`dead` verdict
	* (`0` when nothing is known / fail-closed).
	*/
	explainVerdict(envelope) {
		const knowledge = this.#localKnowledge(envelope.tags);
		let softEpoch = 0;
		let hardEpoch = 0;
		for (const k of knowledge) if (k.known) {
			softEpoch = Math.max(softEpoch, k.softEpoch);
			hardEpoch = Math.max(hardEpoch, k.hardEpoch);
		}
		const validity = this.#suspect ? "unknown" : classify(envelope.bornEpoch, knowledge);
		return {
			hardEpoch,
			softEpoch,
			validity
		};
	}
	/**
	* Validate an envelope against this isolate's knowledge (table). `strong`
	* (or an unknown tag under `registry-check`) consults a live registry read; a
	* plain `eventual` read uses only local bus-fed knowledge and fails closed on any
	* unknown/suspect tag (invariant 11). Never serves a hard-purged value.
	*
	* REACHABILITY IS SPLIT BY CHANNEL. This method is the REGISTRY-RPC half: a failure here is
	* a strong read's `RegistryUnavailableError` or, on the eventual path, a `'miss'`.
	* The BUS half is not represented here at all — a bus that is down or never came up is never
	* grounds for a throw, because the eventual path's whole design is to be correct without it.
	*/
	async validate(envelope, consistency, resolution, onUnavailable) {
		const { tags, bornEpoch } = envelope;
		this.ensureSubscribed();
		if (consistency === "strong") try {
			return await this.#strongLiveCheck(tags, bornEpoch);
		} catch (error) {
			if (onUnavailable !== "eventual") throw new RegistryUnavailableError(`strong read could not reach the registry — ${String(error)}`);
			this.#emit?.({
				reason: STRONG_DEGRADED_REGISTRY_UNREACHABLE,
				type: TELEMETRY_EVENTS.strong_degraded
			});
		}
		if (this.#suspect) await this.resync();
		const localVerdict = this.#suspect ? "unknown" : classify(bornEpoch, this.#localKnowledge(tags));
		if (localVerdict !== "unknown") return {
			checkedThrough: 0,
			validity: localVerdict
		};
		if (typeof resolution !== "string") throw new UnknownTagError(envelope.key, this.#unknownUserTags(tags), resolution.armedBy);
		if (resolution !== "registry-check") return {
			checkedThrough: 0,
			validity: "unknown"
		};
		let checked;
		try {
			checked = await this.#registry.check(tags);
		} catch {
			return {
				checkedThrough: 0,
				validity: "unknown"
			};
		}
		this.#recordChecked(tags, checked);
		return {
			checkedThrough: throughEpochOf(checked),
			validity: classify(bornEpoch, checked)
		};
	}
	/**
	* The strong-read live check: ONE live, un-memoized registry read over
	* `tags`, memoized into local knowledge (`#recordChecked`), classified against
	* `bornEpoch` via the table. The single engine of both the strong READ arm of
	* `validate` and the pre-fetch strong-miss check (`liveCheckForFill`) —
	* local knowledge is never consulted here; the primary read is the authority.
	*/
	async #strongLiveCheck(tags, bornEpoch) {
		const knowledge = await this.#registry.check(tags);
		this.#recordChecked(tags, knowledge);
		return {
			checkedThrough: throughEpochOf(knowledge),
			validity: classify(bornEpoch, knowledge)
		};
	}
	/**
	* Pre-fetch strong-miss live check: the SAME live un-memoized
	* registry read `#strongLiveCheck` runs, executed BEFORE the factory on a strong fill so
	* the fill's bornEpoch is minted from now-current knowledge — an understatement, never an
	* overstatement, of recency (the safe direction). Records the reading into `#known` (
	* memoization), so a later eventual read of the tag fails closed with no second round-trip.
	* Returns the verified high-water; the caller raises the fill's bornEpoch to it.
	*/
	async liveCheckForFill(tags, onUnavailable) {
		try {
			const { checkedThrough } = await this.#strongLiveCheck(tags, 0);
			return checkedThrough;
		} catch (error) {
			if (onUnavailable !== "eventual") throw new RegistryUnavailableError(`strong fill could not reach the registry to mint its bornEpoch — ${String(error)}`);
			this.#emit?.({
				reason: STRONG_DEGRADED_REGISTRY_UNREACHABLE,
				type: TELEMETRY_EVENTS.strong_degraded
			});
			return 0;
		}
	}
	/** Merge a verified live-check result into local knowledge. Max-on-write, so
	* a concurrently-applied newer bus frame is never regressed. The check's
	* `throughEpoch` advances the verified high-water (bornEpoch source) so a subsequent
	* refill postdates what this check observed. */
	#recordChecked(tags, knowledge) {
		for (const [i, tag] of tags.entries()) {
			const k = knowledge[i];
			if (k === void 0 || !k.known) continue;
			const w = this.#known.get(tag) ?? {
				hardEpoch: 0,
				softEpoch: 0
			};
			w.softEpoch = Math.max(w.softEpoch, k.softEpoch);
			w.hardEpoch = Math.max(w.hardEpoch, k.hardEpoch);
			this.#known.set(tag, w);
			this.#knownThroughEpoch = Math.max(this.#knownThroughEpoch, k.throughEpoch);
		}
	}
	/**
	* The write-back fence, evaluated against LOCAL bus-applied knowledge: any tag
	* `hardEpoch > bornEpoch` in the isolate's contiguously-applied state ⇒ the candidate
	* is fenced (a hard purge landed DURING the fill and was delivered here). Returns
	* `true` when the write-back may proceed. A soft purge lands already-stale but still
	* stores; only DEAD fences.
	*
	* DESIGN (per the INV-12 erratum): the write-back guard is realized as a LOCAL
	* fence here plus read-time fail-closed resolution — not a live registry read on the
	* write path. The local fence catches a purge that landed AND delivered locally DURING the
	* fill (tag KNOWN-dead locally ⇒ INV-9/12); it lets a cold/dropped-frame isolate STORE a value
	* whose tag is locally unknown (origin is truth after a purge — the legitimate
	* refill case). A born-dead value that slips past this fence is stored but INERT: its own
	* SUBSEQUENT eventual reads fail closed on the unknown tag and live-check — that read-time
	* check is where the GOAL (never serve a value born against a durably-accepted purge) is
	* enforced for THAT path. A strong fill's OWN serve is anchored earlier, at INITIATION: the
	* pre-factory check minted its `bornEpoch` at the global epoch, so it already dominates
	* every purge accepted before initiation — no read-time check re-verifies the fill
	* that produced it.
	*/
	fenceWriteBack(tags, bornEpoch) {
		return classify(bornEpoch, this.#localKnowledge(tags)) !== "dead";
	}
	/** Local tri-state knowledge for these tags (absent ⇒ unknown). */
	/**
	* The USER tags this read could not establish — what `UnknownTagError` carries.
	*
	* Under suspicion EVERY tag is unknown (the gate treats a map with a hole as knowing
	* nothing), so the whole user set is the honest answer there. Otherwise it is exactly the tags the
	* map has no watermark for. Reserved tags are dropped either way: `__k:`/`__ns:` are the kernel's
	* own bookkeeping, they are unknown for the same reason the user's tags are, and a caller cannot
	* do anything with them — the same reason they never reach an L3 header.
	*/
	#unknownUserTags(tags) {
		const knowledge = this.#localKnowledge(tags);
		return tags.filter((tag, index) => !isReservedTag(tag) && (this.#suspect || knowledge[index]?.known !== true));
	}
	#localKnowledge(tags) {
		return tags.map((tag) => {
			const w = this.#known.get(tag);
			if (!w) return { known: false };
			return {
				hardEpoch: w.hardEpoch,
				known: true,
				softEpoch: w.softEpoch,
				throughEpoch: this.#appliedEpoch
			};
		});
	}
};
//#endregion
//#region ../../packages/cache/src/drivers.ts
/**
* Structural type-guard for `StoreWriteError`. Purely shape-based — it NEVER
* uses `instanceof`, so a harness fake and a real driver are recognized by the SAME
* check without a shared class. Total and safe on arbitrary `unknown`: rejects
* non-objects/null, requires `code` to be exactly one of the three literals and
* `retryable` to be a boolean.
*/
function isStoreWriteError(e) {
	if (typeof e !== "object" || e === null) return false;
	const code = "code" in e ? e.code : void 0;
	const retryable = "retryable" in e ? e.retryable : void 0;
	return (code === "throttled" || code === "too_large" || code === "unavailable") && typeof retryable === "boolean";
}
/**
* Structural type-guard for `StoreReadError` — shape-based, never `instanceof`,
* exactly as `isStoreWriteError` is. Total and safe on arbitrary `unknown`. A driver error
* that carries a read-suppressible `code` satisfies this whatever class raised it, which is
* the point: the own `unavailable` shape is recognized without a shared identity.
*/
function isStoreReadError(e) {
	if (typeof e !== "object" || e === null) return false;
	const code = "code" in e ? e.code : void 0;
	const retryable = "retryable" in e ? e.retryable : void 0;
	return (code === "throttled" || code === "unavailable") && typeof retryable === "boolean";
}
/**
* Rejection raised by `memory().set` when a single value's UTF-8 byte length exceeds the
* store's total `maxBytes` budget — a value larger than the whole budget can never fit, so
* it is REJECTED, never truncated. Satisfies the structural `StoreWriteError`
* contract (`too_large`, non-retryable) WITHOUT sharing a class with the kernel: consumers
* discriminate via `isStoreWriteError` (structural), never `instanceof`. The kernel-side
* twin of the harness `OversizeValueError`; internal, so it needs no export.
*/
var MemoryOversizeError = class extends Error {
	code = "too_large";
	retryable = false;
	constructor(key, sizeBytes, maxBytes) {
		super(`value too large: ${key} (${sizeBytes} > ${maxBytes} bytes)`);
		this.name = "MemoryOversizeError";
	}
};
/**
* Rejection raised by `memory().set` when a write carries `expirationTtl` but the store was built
* with NO injected clock — the residency cannot be honored without a `now` source, so the write
* FAILS LOUD rather than silently drop the TTL. Unlike `MemoryOversizeError` this is a
* MISCONFIGURATION, not a `StoreWriteError`: it must NOT satisfy the structural write-failure
* contract the kernel absorbs — a residency the store can never honor is a programmer error,
* surfaced typed so a caller can `instanceof`-catch it, never quietly ignored. Internal, mirroring
* `MemoryOversizeError` (neither is re-exported from `index.ts`).
*/
var MemoryConfigError = class extends Error {
	constructor(message) {
		super(message);
		this.name = "MemoryConfigError";
	}
};
/** is this a PRIVATE (`usr:`-scoped) L1 entry? Read off the `scope`
* metadata LITERAL the kernel writes with every L1 set (`pub` / `usr:<h>` / `ten:<h>`) — the
* `usr:` PREFIX only, never the hash. A `usr:` entry is per-principal, so its eviction is disclosed;
* detection touches nothing identifying, and a non-canonical store (no `scope` metadata) never
* matches. */
function isPrivateEntry(entry) {
	const scope = entry.metadata?.scope;
	return typeof scope === "string" && scope.startsWith("usr:");
}
/**
* In-process L1 store — isolate-local, bounded by `maxEntries` / `maxBytes` with LRU
* eviction on either bound (an insertion-ordered `Map`, re-inserted on touch, so recency —
* not mere insertion order — picks the victim). Each `memory()` owns a PER-INSTANCE byte
* accounter (Σ value UTF-8 byte lengths).
*
* an optional `telemetry` sink receives a `private_evicted` event when a
* PRIVATE (`usr:`-scoped) entry is evicted from the LRU under memory pressure — reason class +
* counts + bytes ONLY, so an operator sees L1 pressure on private data without the event ever
* naming WHAT was evicted (payload discipline). The `usr:`-priority-protected LRU (evict
* private entries LAST) is a spec MAY and stays UNBUILT: this discloses whatever the current LRU
* policy evicts, it does not change the victim. The cross-instance shared/joint reserve (warn
* on Σ`maxBytes` overflow across isolate L1s) is a separate advisory extra, still DEFERRED.
*/
function memory(options) {
	const clock = options?.clock;
	const maxEntries = options?.maxEntries;
	const maxBytes = options?.maxBytes;
	const telemetry = options?.telemetry;
	const onSinkError = options?.onSinkError;
	const encoder = new TextEncoder();
	const entries = /* @__PURE__ */ new Map();
	let totalBytes = 0;
	const drop = (key, entry) => {
		entries.delete(key);
		totalBytes -= entry.bytes;
	};
	const overBounds = () => maxEntries !== void 0 && entries.size > maxEntries || maxBytes !== void 0 && totalBytes > maxBytes;
	return {
		delete: async (key) => {
			const entry = entries.get(key);
			if (entry !== void 0) drop(key, entry);
		},
		get: async (key) => {
			const entry = entries.get(key);
			const expired = entry !== void 0 && clock !== void 0 && entry.expireAtMs !== void 0 && clock.now() >= entry.expireAtMs;
			if (entry === void 0 || expired) {
				if (entry !== void 0) drop(key, entry);
				return;
			}
			entries.delete(key);
			entries.set(key, entry);
			return entry.metadata === void 0 ? { value: entry.value } : {
				metadata: entry.metadata,
				value: entry.value
			};
		},
		set: async (key, value, writeOptions) => {
			const bytes = encoder.encode(value).length;
			if (maxBytes !== void 0 && bytes > maxBytes) throw new MemoryOversizeError(key, bytes, maxBytes);
			let expireAtMs;
			if (writeOptions?.expirationTtl !== void 0) {
				if (clock === void 0) throw new MemoryConfigError("memory(): expirationTtl requires an injected clock — construct memory({ clock, … }) to honor residency (§8)");
				expireAtMs = clock.now() + writeOptions.expirationTtl * 1e3;
			}
			const existing = entries.get(key);
			if (existing !== void 0) drop(key, existing);
			entries.set(key, {
				bytes,
				value,
				...writeOptions?.metadata === void 0 ? {} : { metadata: writeOptions.metadata },
				...expireAtMs === void 0 ? {} : { expireAtMs }
			});
			totalBytes += bytes;
			while (overBounds()) {
				const lruKey = entries.keys().next().value;
				if (lruKey === void 0) break;
				const lru = entries.get(lruKey);
				if (lru !== void 0) {
					drop(lruKey, lru);
					if (telemetry !== void 0 && isPrivateEntry(lru)) safeEmit(telemetry, {
						bytes: lru.bytes,
						count: 1,
						type: TELEMETRY_EVENTS.private_evicted
					}, onSinkError);
				}
			}
		}
	};
}
//#endregion
//#region ../../packages/cache/src/duration.ts
/**
* Milliseconds per unit — the ONE `Duration`→ms grammar source. Shared with
* `cache.ts`'s coarse `durationMs` parser (which imports this const) so the overflow
* check here and the retention/heartbeat parse there agree on the factor table byte-for-byte.
* `m` = minutes.
*/
var DURATION_UNIT_MS = {
	d: 864e5,
	h: 36e5,
	m: 6e4,
	ms: 1,
	s: 1e3
};
//#endregion
//#region ../../packages/cache/src/http.ts
/** The default retriable HTTP status set (Cloudflare 52x joins it). */
var RETRIABLE_STATUSES = /* @__PURE__ */ new Set([
	408,
	425,
	429,
	500,
	502,
	503,
	504,
	520,
	521,
	522,
	523,
	524,
	525,
	526,
	527,
	530
]);
/**
* The default `staleIfError` predicate — retriable: the default
* retriable set, network errors, and cache-originated timeouts. `404/410/403` are
* facts, never masked (never retriable) — a caller-abort `CacheTimeoutError` with
* `source:'caller'` is likewise not evidence of origin health.
*/
function isRetriableHttp(err) {
	if (err instanceof HttpError) return RETRIABLE_STATUSES.has(err.status);
	if (err instanceof CacheTimeoutError) return err.source === "cache";
	return err instanceof TypeError;
}
//#endregion
//#region ../../packages/cache/src/replication-poller.ts
var DEFAULT_POLL_BASE_MS = 6e4;
var DEFAULT_FIRST_LADDER_DELAY_MS = 1e3;
var DEFAULT_LATER_LADDER_DELAY_MS = 5e3;
var DEFAULT_JITTER_FRACTION = .2;
/** Poll-failure backoff ceiling (knob): a warm baseline poll that does NOT converge doubles
* the cadence (×2) up to this cap — 300s — and the doubled interval is jittered like any other, so a
* fleet whose L2 is failing degrades to one attempt per ~5 minutes per isolate instead of hammering
* a sick store every 60s. The cap is on the BASE interval; the ±jitter rides on top (a jittered
* 300s is 240–360s), because de-synchronizing the fleet matters most exactly when the store is sick. */
var MAX_POLL_BACKOFF_MS = 3e5;
/** Backoff multiplier per consecutive failed baseline poll (×2). */
var POLL_BACKOFF_FACTOR = 2;
/** The rung at which the delta budget is spent and the pass may escalate to the snapshot bridge. */
var ESCALATE_AT_ATTEMPT = 3;
/** The rung at which the snapshot budget is ALSO spent — the reader makes the permanent ruling
* (full replay if the chain is intact ⇒ converged, else terminal-gap). Below it, a lagging snap
* gets one brief retry. */
var SNAPSHOT_FINAL_ATTEMPT = 4;
/** Parse the `__state` pointer bytes the reader resyncs off into the wire shape the
* monitor consumes. Malformed / absent ⇒ `undefined` (the observation is simply skipped — a shell
* read that cannot see the pointer never fabricates liveness). Numbers-only fields; missing
* optionals default the same way the kernel reader does. */
function readStatePointer(raw) {
	let parsed;
	try {
		parsed = JSON.parse(raw);
	} catch {
		return;
	}
	if (typeof parsed !== "object" || parsed === null) return;
	const o = parsed;
	if (typeof o.epoch !== "number" || typeof o.lastBatch !== "number" || typeof o.regId !== "string") return;
	return {
		epoch: o.epoch,
		flushedAt: typeof o.flushedAt === "number" ? o.flushedAt : 0,
		lastBatch: o.lastBatch,
		regId: o.regId,
		snapBatch: typeof o.snapBatch === "number" ? o.snapBatch : 0
	};
}
/**
* The tick-driven replication poll driver. Each `onPollTick(nowMs)`:
*   1. reads the `__state` pointer and feeds the derivative liveness monitor
*      (`notePointerObservation`) — raise-only, every tick, sampling-rate-independent;
*   2. while the reader is SUSPECT, runs the retry ladder when an attempt is DUE:
* attempt 1 → `resync()` (delta); +~1s → attempt 2 → `resync()` (delta); +~5s →
*        attempt 3 → `resync({deltasExhausted})` and, on `needs-snapshot`, `resyncViaSnapshot()`
*        (snapshot-permitted). `converged` clears the reader (and backs the ladder off to the
*        baseline cadence when suspicion persists awaiting a live-channel head — never a spin);
*        `terminal-gap` STOPS the ladder permanently (fail-closed, no spin);
*   3. while WARM, runs the baseline liveness poll on the maxSyncLag cadence (60s ±20%), gated by
* the kernel's pure `pollDue`, and BACKS OFF on failure: a non-converged poll doubles the
*      interval (×2, cap 300s, still jittered); a converged one resets it to the base.
*
* Pure + tick-driven: no wall clock, no ambient timers, no sleeps — `nowMs` and the injected rng
* are the only sources of time/randomness.
*
* RE-ENTRANCY-SAFE BY CONTRACT. `onPollTick` is NOT required to be awaited before the
* next one is fired, and under the request-piggyback driver it routinely is not — a burst of
* requests hands one isolate several ticks while a step's store I/O is still in flight. Two
* properties make that safe HERE, at the root, rather than in each driver: (i) every due-decision
* and the state writes it authorizes happen in ONE synchronous block — no `await` separates a
* due-check from its commit, so no tick ever acts on a schedule another tick has already consumed;
* (ii) at most ONE ladder step is in flight, and a tick arriving mid-step declines rather than
* queues. The `__state` observation is exempt from both — the +25 vigil runs on EVERY tick.
*/
var ReplicationPoller = class {
	#reader;
	#store;
	#keys;
	#rng;
	#pollBaseMs;
	#firstLadderDelayMs;
	#laterLadderDelayMs;
	#jitterFraction;
	/** Active hole-chasing ladder, or `null` when warm (no hole to chase). */
	#ladder = null;
	/** A terminal-gap verdict indexed to the pointer that produced it: the LADDER stays stopped
	* (fail-closed, no spin) while this chain cannot complete. Permanence attaches to the CHAIN, not
	* the vigil — the baseline observation never stops (below). Cleared on a fresh suspect episode OR
	* when a durable `snapBatch` advance past `#terminalSnapBatch` bridges the hole. */
	#terminal = false;
	/** The pointer `snapBatch` the terminal verdict was made against — expired deltas never return, so
	* ONLY an observed `snapBatch` beyond this can re-arm one snapshot-permitted attempt. `null` when
	* not terminal. */
	#terminalSnapBatch = null;
	/** Suspect state at the previous tick — the false→true EDGE opens a fresh episode. */
	#prevSuspect = false;
	/** SINGLE-FLIGHT: a ladder step is between its due-decision and its awaits. Set in the
	* SAME synchronous block as the due-decision that authorized it and cleared in that block's
	* `finally`, so at most one step ever runs. Ticks are request-piggybacked (an isolate gets no
	* free-running timer), which makes RE-ENTRY ROUTINE rather than exotic — a burst of requests can
	* hand this poller several ticks while one step's store I/O is still in flight. A tick that finds
	* a step running DECLINES; it never queues, because queueing merely defers the burst it exists to
	* prevent. Nothing is lost: the rung it would have run is already scheduled (below). */
	#stepInFlight = false;
	/** Last baseline poll time (`null` until the first warm tick seeds the cursor). */
	#lastPollMs = null;
	/** Jittered baseline threshold, recomputed each poll (the current interval ±20%). */
	#pollThresholdMs;
	/** The CURRENT baseline interval the threshold is drawn from: `#pollBaseMs` while polls converge,
	* doubled per consecutive failure up to `MAX_POLL_BACKOFF_MS`. Reset to the base by the
	* first poll that converges again — the backoff is a property of the current failure episode, never
	* a permanent cadence change. */
	#pollIntervalMs;
	constructor(reader, store, keys, rng, options = {}) {
		this.#reader = reader;
		this.#store = store;
		this.#keys = keys;
		this.#rng = rng;
		this.#pollBaseMs = options.pollBaseMs ?? DEFAULT_POLL_BASE_MS;
		this.#firstLadderDelayMs = options.firstLadderDelayMs ?? DEFAULT_FIRST_LADDER_DELAY_MS;
		this.#laterLadderDelayMs = options.laterLadderDelayMs ?? DEFAULT_LATER_LADDER_DELAY_MS;
		this.#jitterFraction = options.jitterFraction ?? DEFAULT_JITTER_FRACTION;
		this.#pollIntervalMs = this.#pollBaseMs;
		this.#pollThresholdMs = this.#jitter(this.#pollIntervalMs);
	}
	/**
	* Whether the reader's replication has reached a permanent terminal gap — the
	* hole-chasing ladder is stopped, fail-closed, until a durable `snapBatch` advance re-arms it.
	* Read-only exposition of the already-tracked `#terminal` verdict for `explain`; it
	* adds no bookkeeping — the field is written only by the tick machinery, never by this getter.
	*/
	get terminal() {
		return this.#terminal;
	}
	/**
	* Advance the state machine one tick. Always observes the pointer for liveness; then
	* drives the ladder (suspect) or the baseline poll (warm). Out-of-band — never on a read path.
	*/
	async onPollTick(nowMs) {
		const raw = await this.#store.get(this.#keys.stateKey);
		const pointer = raw === void 0 ? void 0 : readStatePointer(raw.value);
		if (pointer !== void 0) this.#reader.notePointerObservation(pointer, nowMs);
		if (this.#stepInFlight) return;
		const { suspect } = this.#reader;
		if (suspect && !this.#prevSuspect) {
			this.#terminal = false;
			this.#terminalSnapBatch = null;
			this.#ladder = null;
		}
		this.#prevSuspect = suspect;
		if (this.#terminal) {
			if (pointer !== void 0 && this.#terminalSnapBatch !== null && pointer.snapBatch > this.#terminalSnapBatch) {
				this.#terminal = false;
				this.#terminalSnapBatch = null;
				if (await this.#reader.resyncViaSnapshot({ snapshotExhausted: true }) === "terminal-gap") {
					this.#terminal = true;
					this.#terminalSnapBatch = pointer.snapBatch;
				}
			}
			return;
		}
		if (suspect) {
			const ladder = this.#ladder ?? {
				attempt: 0,
				nextDueMs: nowMs
			};
			this.#ladder = ladder;
			if (nowMs < ladder.nextDueMs) return;
			ladder.attempt += 1;
			ladder.nextDueMs = nowMs + this.#jitter(this.#ladderDelay(ladder.attempt));
			this.#stepInFlight = true;
			try {
				await this.#runLadderStep(nowMs, ladder, pointer?.snapBatch ?? 0);
			} finally {
				this.#stepInFlight = false;
			}
			return;
		}
		this.#ladder = null;
		if (this.#lastPollMs === null) {
			this.#lastPollMs = nowMs;
			this.#pollThresholdMs = this.#jitter(this.#pollIntervalMs);
			return;
		}
		if (this.#reader.pollDue(nowMs, this.#lastPollMs, this.#pollThresholdMs)) {
			this.#lastPollMs = nowMs;
			const status = await this.#reader.resync();
			this.#pollIntervalMs = status === "converged" ? this.#pollBaseMs : this.#backedOff(this.#pollIntervalMs);
			this.#pollThresholdMs = this.#jitter(this.#pollIntervalMs);
		}
	}
	/** One backoff step: ×2, capped at 300s (never below the configured base — a base ABOVE the cap is
	* already slower than any backoff could make it, so the cap floors to it rather than SPEEDING the
	* poll up on failure). */
	#backedOff(intervalMs) {
		const cap = Math.max(MAX_POLL_BACKOFF_MS, this.#pollBaseMs);
		return Math.min(intervalMs * POLL_BACKOFF_FACTOR, cap);
	}
	/**
	* Run one ladder rung. Rungs 1–2 retry the delta pass; rung 3 escalates
	* (`deltasExhausted`) and bridges via the snapshot on `needs-snapshot`; rung ≥4 spends the
	* snapshot budget so the reader makes the permanent ruling. Never sleeps.
	*
	* the caller has ALREADY claimed the rung (`ladder.attempt`) and committed its provisional
	* next-due, synchronously, before this is entered — so this method only ever OVERWRITES that
	* schedule when the outcome calls for a different one. It re-schedules NOTHING on the retry path
	* (the provisional IS the retry), and it writes nothing that a re-entrant tick could race, because
	* the in-flight flag the caller set means no other tick is deciding anything while this runs.
	*/
	async #runLadderStep(nowMs, ladder, observedSnapBatch) {
		const { attempt } = ladder;
		let status = await this.#reader.resync({ deltasExhausted: attempt >= ESCALATE_AT_ATTEMPT });
		if (status === "needs-snapshot") status = await this.#reader.resyncViaSnapshot({ snapshotExhausted: attempt >= SNAPSHOT_FINAL_ATTEMPT });
		if (status === "converged") {
			ladder.attempt = 0;
			ladder.nextDueMs = nowMs + this.#jitter(this.#pollBaseMs);
			return;
		}
		if (status === "terminal-gap") {
			this.#terminal = true;
			this.#terminalSnapBatch = observedSnapBatch;
			this.#ladder = null;
		}
	}
	/** The base ladder delay for the rung just run: attempt 1 → ~1s, attempts ≥2 → ~5s. */
	#ladderDelay(attempt) {
		return attempt <= 1 ? this.#firstLadderDelayMs : this.#laterLadderDelayMs;
	}
	/** Jitter a base delay symmetrically by ±`jitterFraction`, drawn from the injected rng (no
	* ambient randomness). rng.next() ∈ [0,1) ⇒ factor ∈ [1−f, 1+f). */
	#jitter(baseMs) {
		const f = this.#jitterFraction;
		return baseMs * (1 - f + 2 * f * this.#rng.next());
	}
};
/** The library's default safe-mode codec. Deterministic JSON round-trip. */
var safeCodec = {
	decode(value) {
		return JSON.parse(value);
	},
	encode(value) {
		return JSON.stringify(value);
	},
	id: "safe/1"
};
//#endregion
//#region ../../packages/cache/src/cache.ts
/** The `<scope>` literal of the one shared, principal-free storage class. It is
* the ONLY value that keeps a response shared-cacheable under. */
var PUBLIC_SCOPE = "pub";
/** The scope evidence a served HIT carries: its stored literal, or the fail-closed
* `unreadable` class when the shared surface returned none. */
function hitScopeEvidence(hit) {
	if (hit.scopeUnreadable || hit.rawScope === void 0) return { kind: "unreadable" };
	return {
		kind: "readable",
		literal: hit.rawScope
	};
}
/** The scope evidence a FILL carries: the literal this kernel just resolved for it.
*Never `unreadable` — a computed scope cannot fail to be read. */
function fillScopeEvidence(literal) {
	return {
		kind: "readable",
		literal
	};
}
/** Record one consumed dependency on the ambient collector, if there is one. */
function recordDependency(ambient, dependency) {
	ambient?.()?.record(dependency);
}
/** Record a SERVED stored entry as a consumed dependency: the tags it was stored
* with, plus the scope evidence read off the surface it came from — `unreadable` when that
* surface returned no scope at all, which poisons the response rather than guessing. A
* `undefined` hit is a serve that never happened, and records nothing. */
function recordServedHit(ambient, hit) {
	if (hit === void 0) return;
	recordDependency(ambient, {
		scope: hitScopeEvidence(hit),
		tags: hit.envelope.tags
	});
}
/** The verdict for a registry-absent instance: with no watermark authority every
* stored entry reads FRESH, exactly as the codec-only read path classifies it — there is nothing
* that could purge it, so nothing is `stale`/`dead`/`unknown`. */
var FRESH_VERDICT = {
	hardEpoch: 0,
	softEpoch: 0,
	validity: "fresh"
};
/**
* The request-scoped per-dependency attribution `explain` surfaces — the bound
* render collector's recorded dependencies, projected to the public shape. `undefined` when no
* collector is bound (outside an adapter request frame): the attribution lives and dies with the
* request, so its ABSENCE is itself the honest report (a witness reports what it knows; unbound ⇒
* there is nothing to attribute).
*/
function explainAttribution(ambient) {
	const state = ambient?.();
	if (state === void 0) return;
	return state.records.map((dependency) => ({
		scope: dependency.scope,
		tags: dependency.tags,
		...dependency.l3 === void 0 ? {} : { l3: dependency.l3 }
	}));
}
/** Per-instance namespace-version counter. `clear()` bumps it so
* every canonical key minted AFTER the bump is unreachable from a pre-clear key —
* a second, independent defense alongside the `__ns` hard watermark (INV-23). Held
* as a small mutable cell (not a plain closure variable) so it can be threaded
* through the free functions below exactly like `Invalidation`. */
var NsVersion = class {
	#version = 0;
	get current() {
		return this.#version;
	}
	bump() {
		this.#version += 1;
	}
};
/** Canonical key form `<ns-version>:<ns>:<scope>:<user-key>`. */
function canonicalKey(nsVersion, namespace, scope, userKey) {
	return `${nsVersion}:${namespace}:${scope}:${userKey}`;
}
/**
* The adapter-visible identity fields a default-scope request can carry, in
* precedence order ( principal-derived scoping; adapter-visible
* identity / request-identity-header values). `userId` is the per-user
* principal the RR/Workers adapter surfaces; a request that carries only a
* `tenant` still bears adapter-visible identity, so it too derives a principal.
* A request with none of these carries no derivable principal.
*/
var PRINCIPAL_FIELDS = ["userId", "tenant"];
/**
* Derive the adapter-visible principal from a request. Returns
* the first present identity field's value keyed by its field name, or `undefined`
* when the request carries no recognized identity (no context / null principal ⇒
* shared `pub` class). Adapter-visible identity is a primitive
* (a userId/tenant string, a header value) — a non-primitive field value is not a
* clean principal and is skipped. A read here goes through the caller-supplied
* request object directly (NOT the dev-mode guarded Proxy) — principal derivation
* is the kernel's own inspection, not a public-factory closure, so it must never
* itself trip the guarded-accessor demotion (INV-4 test 2).
*/
function derivePrincipal(request) {
	if (request === void 0) return;
	for (const field of PRINCIPAL_FIELDS) {
		const value = request[field];
		if (typeof value === "string" || typeof value === "number" || typeof value === "boolean" || typeof value === "bigint") return `${field}:${value.toString()}`;
	}
}
/**
* Hash a derived principal into the `usr:<hash>` scope suffix. A
* synchronous SHA-256 truncated to 128 bits (32 hex) — hashing is not randomness,
*and this keeps the fill path synchronous. The result is
* non-empty and checkable, so the abolished hashless `usr:` literal can
* never recur: every `usr:` scope carries a principal hash.
*/
function hashPrincipal(principal) {
	return createHash("sha256").update(principal).digest("hex").slice(0, 32);
}
/**
* The `<scope>` literal prefix of the per-principal storage class — the ONE
* class `resolveScope` confines to the isolate-local L1. Shared by both directions so they
* cannot drift: the MINT below builds a literal with it, and `storedScopeIsL1Only` reads a
* STORED literal back through it. A serve derives confinement from the identity the entry was
* stored under, so the two sides must agree on one prefix by construction.
*/
var PRINCIPAL_SCOPE_PREFIX = "usr:";
function resolveScope(input) {
	const { scope } = input;
	if (scope === "public") return {
		l1Only: false,
		literal: "pub"
	};
	if (scope !== void 0 && typeof scope === "object") return {
		l1Only: false,
		literal: `ten:${hashPrincipal(scope.tenant)}`
	};
	const principal = derivePrincipal(input.request);
	if (principal !== void 0) return {
		l1Only: true,
		literal: `${PRINCIPAL_SCOPE_PREFIX}${hashPrincipal(principal)}`
	};
	return {
		l1Only: false,
		literal: "pub"
	};
}
/**
* Whether a STORED `<scope>` literal names the L1-confined class — the read-back twin
* of `resolveScope`'s `l1Only`, over a literal rather than the call inputs a serve no longer has.
* `pub`/`ten:` are shared-tier classes; only `usr:<hash>` is confined.
*
* NOTE the asymmetry this CANNOT see, by construction: a `pub` fill DEMOTED at fill time by the
* dev-mode guarded-accessor Proxy stores the `pub` literal, so its confinement leaves no trace in
* the stored identity. Such an entry reads back as `pub` here, and a serve reports its durability
* ABSENT — unknowable, which is the honest answer, rather than a `true` the literal cannot
* support.
*/
function storedScopeIsL1Only(literal) {
	return literal.startsWith(PRINCIPAL_SCOPE_PREFIX);
}
/** The `<scope>` literal alone — for the contexts (canonical key, join
* identity, key-selector purge) that need only the string, not the tier policy. */
function scopeLiteral(input = {}) {
	return resolveScope(input).literal;
}
/** Normalize the config's `telemetry` (a bare sink function, a config object, or
* absent) to `{ sink, hosted?, salt?, onSinkError? }`. A bare function carries no
* hosted/salt/onSinkError; the config-object form passes them through unchanged. */
function telemetryOf(config) {
	const { telemetry } = config;
	if (telemetry === void 0) return;
	if (typeof telemetry === "function") return { sink: telemetry };
	return telemetry;
}
/**
* Pseudonymize one string field for a HOSTED sink: a per-project
* HMAC-salted hash, so a short or low-entropy identifier (a key or a tag) is not
* offline-invertible and never travels raw. The salt is the per-project secret
* (`hostedTelemetry(salt)`), so holding key+tag fixed and changing ONLY the salt
* changes the ref — and the ref tracks its own input, so a key ref moves iff the
* key moves and a tag ref moves iff the tag moves (INV-15). HMAC (not a bare hash)
* so the salt genuinely keys the digest per project.
*/
function saltedRef(salt, value) {
	return createHmac("sha256", salt).update(value).digest("hex").slice(0, 32);
}
/** Redact one event field for a hosted sink: HMAC-salt every string (per-field,
* including inside a string array — key ref, tag refs), leave non-string scalars
* intact. `type` (the event NAME, not sensitive) is redacted by the caller's field
* filter, never here. A field-total salted pseudonymization — not
* anonymization. */
function redactField(salt, value) {
	if (typeof value === "string") return saltedRef(salt, value);
	if (Array.isArray(value)) return value.map((element) => redactField(salt, element));
	return value;
}
/**
* Emit a telemetry event. A plain sink receives the event verbatim
* (raw key/tag/detail — redaction is hosted-only by design, INV-15). A HOSTED sink
* receives a field-total salted pseudonymization: every string field EXCEPT `type`
* (the event name) is replaced by a per-project HMAC-salted hash of its own value,
* so no raw key/tag/salt/detail ever leaves the process. A hosted config
* missing a salt cannot pseudonymize, so it is suppressed rather than leaking raw
* fields. The final delivery — plain or redacted — goes through `safeEmit`, so an
* app sink that throws or rejects is swallowed (its `onSinkError` hook notified) and
* can never surface on the kernel's own path.
*/
function emitTelemetry(config, event) {
	const telemetry = telemetryOf(config);
	if (telemetry === void 0) return;
	if (telemetry.hosted !== true) {
		safeEmit(telemetry.sink, event, telemetry.onSinkError);
		return;
	}
	const { salt } = telemetry;
	if (salt === void 0) return;
	const redacted = { type: event.type };
	for (const [field, value] of Object.entries(event)) {
		if (field === "type") continue;
		redacted[field] = redactField(salt, value);
	}
	safeEmit(telemetry.sink, redacted, telemetry.onSinkError);
}
/** The reserved per-key tag: `__k:<h>`, `<h>` a ≥128-bit (32 hex char) digest
* of the canonical key (scoped, ns-versioned). SHA-256 truncated to 128 bits — a
* synchronous digest (`node:crypto`, available on both Node and Bun) so the fill
* path stays fully synchronous; hashing is not randomness. `expire
* ({key})` recomputes this same digest to target the identical tag. */
function keyTag(canonical) {
	return `__k:${createHash("sha256").update(canonical).digest("hex").slice(0, 32)}`;
}
/** The reserved per-namespace tag: `__ns:<name>`. Every envelope
* carries it so `clear()`'s hard purge rides the same epoch/bus/conservative
* machinery as any other purge — this is what lets a lagging isolate's read
* fail closed instead of serving pre-clear data (INV-23). */
function nsTag(namespace) {
	return `__ns:${namespace}`;
}
/** Validate the committed user-tag SET at the write boundary. Two ceilings, both
* per typed `InvalidTagError`, never `NotImplementedError`:
*   - the `__`-prefix backstop, per tag — a forged reserved tag never reaches storage;
*   - the count ceiling, over the whole set — at most `MAX_USER_TAGS` user tags.
* The caller passes whatever set is being committed — the declared tags at `getOrSet`'s
* boundary, or the settle-union of declared + `setTags` + `dependsOn` — so the ceiling
* is route-independent by construction: one function, one set, one boundary. The reserved
* `__k`/`__ns` tags are stamped separately and never counted here. The set is expected already
* deduped (the ceiling counts distinct user tags, not raw call arity). */
function validateUserTags(tags) {
	if (tags === void 0) return;
	if (tags.length > 126) throw new InvalidTagError(tags[126] ?? "", `an entry may not carry more than 126 user tags (§2.3, MI13); got ${tags.length}`);
	for (const tag of tags) assertUserTag(tag);
}
/** `tags` are sorted, deduped, and include `__k`/`__ns`. Dedupes FIRST, then validates the
* distinct set — so the count ceiling measures distinct user tags, not raw call
* arity — and returns just the caller-supplied set, sorted and deduped. The reserved
* `__k`/`__ns` tags are appended separately by `envelopeTags` below, since they depend on the
* canonical key / namespace, not just the call options. */
function sortedUserTags(tags) {
	if (tags === void 0) return [];
	const distinct = [...new Set(tags)].toSorted();
	validateUserTags(distinct);
	return distinct;
}
/** The full stored-envelope tag set: sorted user tags plus the reserved
* `__k:<h>` (per-key digest of the canonical key) and `__ns:<name>` (per-namespace
* watermark) tags. This is what rides the dual-epoch invalidation core, so
* `expire({key})` / `clear()` invalidate through the same machinery as any tag
* purge. */
function envelopeTags(userTags, canonical, namespace) {
	return [
		...userTags,
		keyTag(canonical),
		nsTag(namespace)
	].toSorted();
}
function makeTagAccumulator(declared) {
	const membership = [];
	let authored;
	let settled = false;
	return {
		isSettled() {
			return settled;
		},
		markSettled() {
			settled = true;
		},
		addMembership(tag) {
			assertUserTag(tag);
			membership.push(tag);
		},
		author(tags) {
			for (const tag of tags) assertUserTag(tag);
			authored = [...tags];
		},
		settleUserTags() {
			return [.../* @__PURE__ */ new Set([...authored ?? declared, ...membership])].toSorted();
		}
	};
}
/** Resolve the codec, falling back to the built-in safe-mode codec when the
* config wires none — `world()` wires no codec, so the core needs a default
* to store/read values at all. */
function codecOf(config) {
	return config.codec ?? safeCodec;
}
/**
* The consistency this call reads at: per-call, then the instance's declared default, then
* the own (`eventual`).
*
* The middle step is what makes `CacheDefaults.consistency` a setting rather than a decoration:
* without it, a caller who declared `defaults: { consistency: 'strong' }` got eventual reads and
* nothing said so. The final fallback is unchanged and matches what every shipped default already
* declares, so an instance that sets nothing reads exactly as it did before.
*/
function consistencyOf(config, options) {
	return options.consistency ?? config.defaults?.consistency ?? "eventual";
}
/**
* How fail-closed `unknown` resolves for this instance.
*
* `takedownSensitive` is an OVERRIDE, and the precedence is the whole point. It outranks an
* explicitly-set `unknownPolicy` rather than merely filling in for an absent one — because as an
* absent-policy default it was unreachable in practice: any composition that sets `unknownPolicy`
* shadows it, and `createWorkersCache`'s own safe posture sets it, so the switch was dead on the
* entire hero path. A legal-embargo control that a routine, recommended default silently disables is
* worse than no control: it reads as protection and does nothing.
*
* Reading it as an override is also what the flag MEANS. `unknownPolicy` says how this namespace
* would like unknowns resolved; `takedownSensitive` says this namespace is one where guessing is not
* allowed. The second is a statement about the CONTENT, so it cannot be outranked by a preference.
*/
function unknownResolution(config) {
	if (config.takedownSensitive === true || config.defaults?.takedownSensitive === true) return {
		armedBy: "takedownSensitive",
		kind: "error"
	};
	const policy = config.defaults?.unknownPolicy ?? "registry-check";
	return policy === "error" ? {
		armedBy: "unknownPolicy",
		kind: "error"
	} : policy;
}
/** The `staleIfError` predicate for this instance — the configured
* override, or the default HTTP classifier. */
function staleIfErrorOf(config) {
	return config.defaults?.staleIfError ?? isRetriableHttp;
}
/** Whether a thrown error is the `not_found` fact (a 404) — never masked,
* never retriable, but eligible for negative caching when `notFoundTtl` is declared. */
function isNotFound(err) {
	return err instanceof HttpError && err.status === 404;
}
/** A miss with no live check performed (absent / codecId / decode). */
var MISS_READ = { checkedThrough: 0 };
/** Whether a stored entry's codec identity matches (or is an accepted migration
* source) — checked BEFORE decode so a foreign codec never runs. */
function codecIdMatches(storedCodecId, codec, config) {
	if (storedCodecId === codec.id) return true;
	const acceptCodecs = config.defaults?.acceptCodecs ?? [];
	return typeof storedCodecId === "string" && acceptCodecs.includes(storedCodecId);
}
/** Reconstruct the envelope shell from stored bytes + metadata. `bornEpoch`/
* `tags` ride in metadata so validation runs without a second decode. */
function envelopeFromStore(key, raw, metadata, decoded, codecId) {
	const bornEpoch = typeof metadata.bornEpoch === "number" ? metadata.bornEpoch : 0;
	const bornMs = typeof metadata.bornMs === "number" ? metadata.bornMs : 0;
	const tags = Array.isArray(metadata.tags) ? metadata.tags.filter((t) => typeof t === "string") : [];
	return {
		bornEpoch,
		bornMs,
		codecId,
		enc: "json",
		grace: 0,
		key,
		kind: metadata.kind === "neg" ? "neg" : "val",
		scope: typeof metadata.scope === "string" ? metadata.scope : "pub",
		size: raw.length,
		src: "factory",
		storedAt: bornMs,
		tags,
		ttl: 0,
		v: 3,
		val: decoded,
		...typeof metadata.fence === "number" ? { fence: metadata.fence } : {}
	};
}
/**
* The value a HIT on this envelope SURFACES.
*
* A `kind:'neg'` envelope is a remembered 404: it caches the not-found FACT, and the envelope gives it no
* value to carry. Its stored bytes are a JSON-representable placeholder (`writeNegEnvelope`) chosen
* only because `undefined` is not a storable body — they are never the answer. So a read of a neg
* surfaces `undefined`, which is EXACTLY what the fill turn that wrote it surfaced (INV-7): the
* point of caching a 404 is that the next reader gets the same answer without paying the origin,
* and an answer that changes on the second call is not a cached fact at all. Serving the placeholder
* instead handed the caller a `null` no origin ever produced, and reported a durable VALUE hit for
* an entry holding only a negative.
*
* A `val` envelope surfaces its decoded value unchanged. This is the ONE point where the two kinds
* diverge on the read path; every other neg rule is already a refusal to serve at all ( a
* stale neg is never grace/SWR-served, so the only servable neg is a fresh one).
*/
function servedValue(envelope) {
	if (envelope.kind === "neg") return;
	return envelope.val;
}
/**
* Classify a rejected `Store.get` — the read-side twin of `classifyWriteBackFailure`, and
* consistent with its reasoning rather than merely its shape:
*
*   - CLASSIFIED (`throttled` / `unavailable`) ⇒ returned to the caller, which absorbs it. The kernel signs
*     that a suppressed L2 GET error is a plain MISS — never routed to unknown/fail-closed — so on
* the serving path the caller refills, and a configured L1 (checked FIRST) may already
*     have served without this tier being read at all.
*   - UNCLASSIFIED ⇒ RETHROWN unchanged, for the same reason the write half rethrows one: the kernel
*     absorbs only failures a driver has named, and a rejection nobody classified is not evidence of
*     absence. Swallowing it would turn an unknown fault into a confident "not found" — which, on a
*     read, is the one answer that silently refills over whatever is really there.
*
* Suppression is the only telemetry-bearing outcome (`store_read_suppressed`, a raw fact at the
* instant it occurs). A rethrown error needs no event: it reaches the caller intact.
*/
function classifyStoreReadFailure(config, key, error) {
	if (!isStoreReadError(error)) throw error;
	emitTelemetry(config, {
		code: error.code,
		key,
		type: TELEMETRY_EVENTS.store_read_suppressed
	});
	return error;
}
/** The ONE spelling of the "the store did not answer" discriminant, shared by the probe's outcome
* and the witness's own arm so the two can never drift apart. */
var READ_FAILED_KIND = "read-failed";
/** The two probe outcomes that carry no payload — one shared instance each, exactly as `MISS_READ`
* is for the read path. */
var PROBE_ABSENT = { kind: "absent" };
var PROBE_READ_FAILED = { kind: READ_FAILED_KIND };
/**
* A NON-mutating presence probe for `explain`: read `canonical` from `store` with a
* plain `store.get` — never a hydrating read — and reconstruct its envelope plus the RAW stored
* scope; `absent` when the entry is missing, codec-mismatched, or undecodable; `read-failed`
* when the store's own read was suppressed. The read fails closed exactly as the serve path
* does (INV-8/27); it observes, it never writes.
*/
async function probeStoredEntry(store, codec, config, canonical) {
	let stored;
	try {
		stored = await store.get(canonical);
	} catch (error) {
		classifyStoreReadFailure(config, canonical, error);
		return PROBE_READ_FAILED;
	}
	if (!stored || !codecIdMatches(stored.metadata?.codecId, codec, config)) return PROBE_ABSENT;
	let decoded;
	try {
		decoded = codec.decode(stored.value);
	} catch {
		return PROBE_ABSENT;
	}
	const envelope = envelopeFromStore(canonical, stored.value, stored.metadata ?? {}, decoded, codec.id);
	const storedScope = stored.metadata?.scope;
	const scope = typeof storedScope === "string" ? {
		kind: "readable",
		literal: storedScope
	} : { kind: "unreadable" };
	const storedTtl = stored.metadata?.ttl;
	return {
		envelope,
		kind: "found",
		scope,
		ttl: typeof storedTtl === "number" ? {
			kind: "stored",
			seconds: storedTtl
		} : { kind: "not-stored" }
	};
}
/**
* THE READ PATH'S CLASSIFIER for the SERVING path. Given raw stored bytes and metadata, it answers
* what a reader on THIS instance would make of them: an envelope, or nothing at all. (The
* introspection probe still runs its own copy of this sequence — see `probeStoredEntry`, which
* answers a different question and must report the causes of absence rather than collapse them to
* one. Folding the two is worth doing and is not this change.)
*
* Two gates precede the reconstruction and both fail CLOSED, because a reader that cannot identify
* or decode an entry is served a miss rather than a guess — the codecId check (INV-27/ : a
* mismatch `acceptCodecs` does not admit is a miss) and `decode` itself (INV-8: a body this codec
* cannot read is a miss). Only past both is an envelope built.
*
* Both seats call THIS function, by identity, and that is the point. "Servable" is defined by the
* read path, so the write layer's neg-write guard must ask the READER's question rather than a looser one
* of its own: an envelope no reader can classify as a val is not a servable good value — it is a
* miss wearing bytes, and protecting it protects a ghost. Answering that question with a partial
* copy of these steps is exactly how two seats stop meaning one thing while each keeps passing its
* own tests.
*/
function readerEnvelope(config, codec, key, stored) {
	if (!codecIdMatches(stored.metadata?.codecId, codec, config)) return;
	let decoded;
	try {
		decoded = codec.decode(stored.value);
	} catch {
		return;
	}
	return envelopeFromStore(key, stored.value, stored.metadata ?? {}, decoded, codec.id);
}
/**
* Read one tier: codecId-check + decode + validate against knowledge.
* `hit` is a `ReadHit` (fresh or stale-servable) or `undefined` (absent / dead /
* decode failure / codecId mismatch / suppressed read failure / unknown-fail-closed ⇒ the
* caller refills). `checkedThrough` is the epoch a live check by THIS read verified (0 if
* none), which the refill uses so a behind isolate does not fence its own legitimate refetch.
*/
async function readTier(store, codec, config, inval, consistency, key) {
	let stored;
	try {
		stored = await store.get(key);
	} catch (error) {
		classifyStoreReadFailure(config, key, error);
		return MISS_READ;
	}
	if (!stored) return MISS_READ;
	const envelope = readerEnvelope(config, codec, key, stored);
	if (!envelope) return MISS_READ;
	const storedScope = stored.metadata?.scope;
	const rawScope = typeof storedScope === "string" ? storedScope : void 0;
	const scopeEvidence = {
		scopeUnreadable: rawScope === void 0,
		...rawScope === void 0 ? {} : { rawScope }
	};
	if (!inval) return {
		checkedThrough: 0,
		hit: {
			...scopeEvidence,
			envelope,
			stale: false,
			value: servedValue(envelope)
		}
	};
	const { validity, checkedThrough } = await inval.validate(envelope, consistency, unknownResolution(config), config.defaults?.onUnavailable);
	if (validity === "fresh") return {
		checkedThrough,
		hit: {
			...scopeEvidence,
			envelope,
			stale: false,
			value: servedValue(envelope)
		}
	};
	if (validity === "stale" && envelope.kind !== "neg") return {
		checkedThrough,
		hit: {
			...scopeEvidence,
			envelope,
			stale: true,
			value: servedValue(envelope)
		}
	};
	if (envelope.kind === "neg") return { checkedThrough };
	return {
		checkedThrough,
		staleCandidate: {
			...scopeEvidence,
			envelope,
			stale: true,
			value: servedValue(envelope)
		}
	};
}
/**
* DEVELOPMENT-TIME TEACHING TOOL — NOT A PRODUCTION SAFETY NET. It is dev-gated by design
* (`config.dev === true`), and it stays that way. Two readings to head off, in both directions:
*
*   - DO NOT PROMOTE IT TO PRODUCTION believing it closes a privacy hole. It does not close one:
*     an application that hand-mixes per-principal content into a public-scoped fill without
*     touching `ctx.request` at all is untouched by it, so it was never a boundary — it is a
*     tripwire that catches the honest mistake early, in the one environment where the developer
*     is watching. Making the demotion live in production would make an entry's STORAGE TIER a
*     function of what a factory happened to read on one execution: the same key lands durable on
*     the request whose factory took the cache-hit path and L1-only on the request whose factory
*     read the principal, so the tier stops being a property of the call and becomes a property of
*     the run. That is nondeterministic caching, and it is a worse bargain than the mistake it
*     would be trying to catch.
*
*   - DO NOT DELETE IT believing it is dead code. INV-4 pins it, and the demotion is exactly
*     what that invariant's enforceable half asserts — a public fill that read request state is
*     confined to L1 and never reaches the shared tier. Blind it and the sacred suite reds.
*
* It deliberately does NOT feed the shared-cacheability gate. That gate answers a different
* question — whether the scopes the library DECLARED and STORED permit a shared HTTP cache to keep
* the response — and it answers it from evidence that survives the request. This guard sees
* only what one factory did while it ran. Wiring a runtime observation into that gate would make
* the emitted response posture vary run to run for the same route, which is the same nondeterminism
* as above, on the wire instead of in the tiers.
*/
function buildRequestGuard(config, options) {
	const { request } = options;
	if (!(config.dev === true && options.scope === "public") || request === void 0) return {
		request,
		wasTouched: () => false
	};
	let touched = false;
	return {
		request: new Proxy(request, { get(target, prop, receiver) {
			touched = true;
			return Reflect.get(target, prop, receiver);
		} }),
		wasTouched: () => touched
	};
}
/** Acquire the cross-isolate lock if requested, returning its fence handle. */
async function maybeLock(config, options) {
	if (options.lock !== true) return;
	const { lock } = config;
	if (!lock) return;
	return await lock.acquire(options.key);
}
/**
* Classify a rejected L2 write-back into the kernel's write-failure semantics.
* WIRES CLASSIFICATION + TELEMETRY ONLY — no REFRESH_QUEUE / retry machinery is built
* (that is deferred). A recognized transient failure is SUPPRESSED: the write did not
* become durable, but the operation does not fail the caller and produces no
* fail-closed/unknown state — the function returns `false` (non-durable) and emits the
* reserved telemetry name. Everything else keeps today's behavior and PROPAGATES:
*   - `throttled` (retryable) → `writeback_throttled`, suppressed (`durable:false`).
*   - `unavailable` (retryable) → `l2_write_error`, suppressed (`durable:false`).
*   - `too_large` (permanent, non-retryable) → rethrown, never silently suppressed.
*   - unclassified → surfaced as `l2_write_error` telemetry, then rethrown unchanged.
*/
function classifyWriteBackFailure(config, key, error) {
	const classified = isStoreWriteError(error) ? error : void 0;
	if (classified?.code === "throttled") {
		emitTelemetry(config, {
			key,
			type: "writeback_throttled"
		});
		return false;
	}
	if (classified?.code === "unavailable") {
		emitTelemetry(config, {
			key,
			type: "l2_write_error"
		});
		return false;
	}
	if (classified === void 0) emitTelemetry(config, {
		key,
		type: "l2_write_error"
	});
	throw error;
}
/**
* Is this stored envelope a `val` that protects? THE suppression-worthiness
* test, and it lives in ONE place because two seats have to agree about it:
*
*   - the SERVE seat (`gracedStillServable`) asks it of the candidate THIS read holds, deciding
*     whether an error-path serve may draw on it;
*   - the WRITE seat (`negDisplacesProtectedVal`) asks it of the envelope the born-monotone compare
*     already fetched, deciding whether a `kind:'neg'` candidate may land on it.
*
* One function, because the two answers may never disagree in the direction that destroys data: a
* val the serve path would hand to a caller is exactly a val the write path must refuse to bury.
* Written twice they drift, and the drift is silent — each site keeps passing its own tests while
* the pair stops meaning one thing.
*
* `validity` is the verdict, produced by the same machinery on both sides. `unknown` is
* deliberately NOT settled here: it is a GAP rather than a window, and the two seats answer a gap
* differently on purpose — the serve seat refuses to serve THROUGH one, the write seat refuses to
* write through one. Each states that rule, with its own derivation, at its own site.
*/
function e7ProtectsStoredVal(envelope, validity) {
	return envelope.kind !== "neg" && (validity === "fresh" || validity === "stale");
}
/** The `UnknownResolution` a RE-VALIDATION of an already-stored envelope asks for: settle
* an unknown tag with one live registry read. Named once because the two seats that re-validate the
* same envelope — the serve seat's grace check and the write seat's neg-write guard — must ask the SAME
* question of it; drifting onto different resolutions would let them reach different verdicts about
* one entry and call it agreement. Distinct from `unknownResolution`, which reports the instance's
* configured READ-time policy. */
var REVALIDATION_RESOLUTION = "registry-check";
/**
* the kind-aware guard: would landing a `kind:'neg'` candidate here bury a
* `val` the letter protects? Asked of the envelope the born-monotone compare ALREADY fetched, which
* is why it lives at the write layer — one boundary, so every neg-write source passes it identically
* and the outcome cannot vary by the route that reached it.
*
* The question is ONTOLOGICAL: what does the STORE hold? That is a different question from the one
* the filling caller's own read can answer, and the gap between them is the whole defect this closes
* — a caller blind to a servable val (a tier that was unreadable for a moment, or a join whose
* leader read nothing) would otherwise write straight over it, and every later reader would be
* served the negative. Note also which KIND is which: `FilledAttempt.storedKind` describes what THIS
* fill produced, while the kind read here is the STORED envelope's — different objects, and only the
* second one can answer this.
*
* The within-grace term is not re-invented: it is the verdict `validate` gives, asked on the
* same terms the serve seat asks for it (`gracedStillServable`), and settled by the shared predicate
* both seats share.
*/
async function negDisplacesProtectedVal(config, key, existing, inval, consistency) {
	const stored = readerEnvelope(config, codecOf(config), key, existing);
	if (!stored) return false;
	if (!inval) return e7ProtectsStoredVal(stored, "fresh");
	let validity;
	try {
		({validity} = await inval.validate(stored, consistency, REVALIDATION_RESOLUTION, config.defaults?.onUnavailable));
	} catch {
		validity = "unknown";
	}
	return e7ProtectsStoredVal(stored, validity === "unknown" ? "stale" : validity);
}
/**
* Born-monotone write-back (invariant 17): never replace a stored
* envelope with a strictly-higher `(bornEpoch, fence)`. Read-compare-write against
* L2 — the KV CAS-less residual race is documented and bounded to one refresh cycle.
*`bornMs` is never an arbiter. `durable` is `true` when the candidate landed (or a
* strictly-newer envelope already sits durably), `false` when a classified `throttled`/`unavailable`
* failure suppressed it.
*
* It is ALSO where the kind-aware guard sits, for the reason the guard needs and this function
* already has: the stored envelope is in hand here. A `kind:'neg'` candidate may only land on a true
* miss, so every neg-write source — a solo caller's own disposition, a blind leader's in-window
* recording — meets the same test on the same evidence, and route-independence holds by construction
* rather than by matching two call sites. `val`-over-`neg` is untouched: recovery is the direction
* The neg-write guard leaves open, and the guard is asked only of a neg candidate.
*/
async function bornMonotoneWrite(config, l2, key, encoded, candidate, inval, consistency) {
	let existing;
	try {
		existing = await l2.get(key);
	} catch (error) {
		return {
			durable: classifyWriteBackFailure(config, key, error),
			writeSuppressed: false
		};
	}
	if (existing) {
		if (candidate.kind === "neg" && await negDisplacesProtectedVal(config, key, existing, inval, consistency)) {
			emitTelemetry(config, {
				key,
				type: TELEMETRY_EVENTS.neg_suppressed
			});
			return {
				durable: false,
				writeSuppressed: true
			};
		}
		const meta = existing.metadata ?? {};
		const storedBornEpoch = typeof meta.bornEpoch === "number" ? meta.bornEpoch : 0;
		const storedFence = typeof meta.fence === "number" ? meta.fence : -1;
		const candidateFence = candidate.fence ?? -1;
		if (storedBornEpoch > candidate.bornEpoch || storedBornEpoch === candidate.bornEpoch && storedFence > candidateFence) return {
			durable: true,
			writeSuppressed: false
		};
	}
	try {
		await l2.set(key, encoded, { metadata: {
			bornEpoch: candidate.bornEpoch,
			bornMs: candidate.bornMs,
			codecId: candidate.codecId,
			enc: candidate.enc,
			kind: candidate.kind,
			scope: candidate.scope,
			tags: candidate.tags,
			v: candidate.v,
			...candidate.fence === void 0 ? {} : { fence: candidate.fence }
		} });
		return {
			durable: true,
			writeSuppressed: false
		};
	} catch (error) {
		return {
			durable: classifyWriteBackFailure(config, key, error),
			writeSuppressed: false
		};
	}
}
/**
* Re-validate a graced candidate's tags AT SERVE TIME: a hard delete
* that landed after the candidate was read (e.g. during the failed refresh) must
* fence it here — a graced value is never served once its purge is visible. No
* `Invalidation` (registry-absent) ⇒ nothing to invalidate it ⇒ always servable.
*
* AT `strong` THIS IS A LIVE REGISTRY CHECK LIKE ANY OTHER, so it honors `onUnavailable`
* exactly as its sibling sites do. Leaving it unguarded did not avoid a throw — it MANUFACTURED
* one: the guard inside `validate` fires for a caller who explicitly opted out, which is the one
* caller who asked not to see it.
*
* `factoryError` is what this re-validation runs on behalf of, and it rides out as the thrown
* error's `cause`. That answers the reason the guard was omitted here — the fear that a throw would
* REPLACE the caller's factory error — without leaving the site unguarded: nothing is dropped, and
* callers still get the one type the API names.
*
* WHEN THE OPT-OUT IS TAKEN this call drops to what an eventual read would do, and the candidate is
* settled against local knowledge alone. A tag whose hardness cannot be established stays UNKNOWN,
* and an unknown tag is never graced (ladder sends unknown to 5a and never to grace) —
* when hardness is exactly what we failed to learn, a hard purge is one of the things the gap could
* be concealing. So an unknown candidate is not servable, and the factory's own error is what the
* caller ends up with.
*/
async function gracedStillServable(inval, consistency, envelope, onUnavailable, factoryError) {
	if (!inval) return e7ProtectsStoredVal(envelope, "fresh");
	let validity;
	try {
		({validity} = await inval.validate(envelope, consistency, REVALIDATION_RESOLUTION, onUnavailable));
	} catch (error) {
		if (error instanceof RegistryUnavailableError) throw new RegistryUnavailableError(error.message, { cause: factoryError });
		throw error;
	}
	return e7ProtectsStoredVal(envelope, validity);
}
/**
* Write a `kind:'neg'` envelope for a `not_found` fact.
* `notFoundTtl` is the per-call opt-in; the write shares the same born-monotone
* fence as any write-back.
*
* Returns the write's OUTCOME (durability, and whether the guard refused it) AND
* the `bornMs` it stamped — the neg's birth instant, read off the clock exactly once here and
* carried out for every later consumer, the same way a value fill's is.
*
* `inval`/`consistency` ride down for the guard alone: it decides against the STORE, and deciding
* against the store means asking the same validity question a reader asks of what it finds there.
*/
async function writeNegEnvelope(config, key, tags, scope, bornEpoch, inval, consistency) {
	const { l2 } = config;
	const bornMs = config.clock.now();
	if (!l2) return {
		bornMs,
		durable: true,
		writeSuppressed: false
	};
	const codec = codecOf(config);
	const encoded = codec.encode(null);
	return {
		bornMs,
		...await bornMonotoneWrite(config, l2, key, encoded, {
			bornEpoch,
			bornMs,
			enc: "json",
			grace: 0,
			key,
			kind: "neg",
			scope,
			size: encoded.length,
			src: "factory",
			storedAt: bornMs,
			tags,
			ttl: 0,
			v: 3,
			val: null,
			codecId: codec.id
		}, inval, consistency)
	};
}
/** Build the INV-26 neg-suppression serve of a still-servable candidate (a 404 that a servable
* `val` displaces). ONE constructor for both call sites — the leader-sighted classification and a
* blind leader's per-waiter tail — so `value` is always the served candidate's own value.
*
* It also EMITS, and the emit belongs here rather than at either caller. The design lists `neg_suppressed`
* among a suppression's consequences, and the design attaches those consequences to whichever seat renders
* the suppression, one event per suppression. This is the serve seat's renderer; the write layer's
* guard emits its own at its own site. Emitting from the shared constructor is what makes one
* event per suppression structural instead of a convention two call sites have to remember —
* before it, the epistemic suppression that the own rationale describes fired no telemetry at all,
* so the commonest case was the silent one. */
function negSuppressed(config, key, served) {
	emitTelemetry(config, {
		key,
		type: TELEMETRY_EVENTS.neg_suppressed
	});
	return {
		kind: "neg-suppressed",
		served,
		value: served.hit.value
	};
}
/**
* Write the `not_found` neg and package it as the `filled` attempt every
* SERVED-fill path already understands: `value:undefined`, the neg's stored tag set, and the write's
* own `durable`/`bornMs`. A neg carries no `bornEpoch` on the outcome — its presence is the marker
* of a successful VALUE fill, which this is not — so L1 hydration falls back to the caller's
* outer floor exactly as the value path intends.
*
* ONE writer of the neg for two call sites: the leader that read a candidate of its own that
* re-validated DEAD (its own disposition, in `classifyFillError`), and any blind waiter's own tail
* (`runOwnNotFoundTail`). Both stamp the fill's tags/scope/bornEpoch, so a neg written on either path
* is byte-identical to the one a solo caller writes — and both meet the write-layer guard, which is
* why a SUPPRESSED write is one of the outcomes this can carry (`writeSuppressed`): the fill still
* produced the not-found FACT and still serves it, but nothing was stored and nothing is evicted.
*/
async function negWriteFilled(config, key, tags, scope, bornEpoch, inval, consistency) {
	const neg = await writeNegEnvelope(config, key, tags, scope, bornEpoch, inval, consistency);
	return {
		bornMs: neg.bornMs,
		durable: neg.durable,
		kind: "filled",
		storedKind: "neg",
		tags,
		value: void 0,
		writeSuppressed: neg.writeSuppressed
	};
}
/**
* The factory error-path classification, extracted out of
* `fillAndStore` to keep its cognitive complexity down:
*   - `ctx.skip()` (M10/invariant 24): `{kind:'skipped'}` — nothing stored,
*     nothing served; no write-back of any kind, not even a neg.
*   - `not_found` (404, a fact — never masked): with `notFoundTtl` declared and THIS fill's own read
*     holding a still-servable `val`, suppress the neg-write and serve that val — a neg must never
* displace a servable value — with no `servedOnError` (a suppressed neg is not an
*     error-path serve). This fill's own copy re-validated DEAD ⇒ write the neg and serve it (its own
*     disposition). This fill read NO copy at all (a blind leader) ⇒ ride the 404 out as a shareable
*     `not-found` FACT so each waiter runs its own tail against its own read ( `NotFoundFact
*     Attempt`), the mirror of the retriable branch's fenced-vs-fact split just below. Without
*     `notFoundTtl`, rethrow.
*   - retriable (`staleIfError`, default `isRetriableHttp`) with a graced `val`
* still servable at serve time: serve it, `servedOnError: true`. A
*     `kind:'neg'` graced candidate is NEVER offered here — it is never grace/SIE-
* served — so a neg-only history rethrows.
*   - otherwise (non-retriable, or no servable graced copy): rethrow — a failed or
* timed-out factory never modifies the stored good value.
*/
async function classifyFillError(error, config, key, tags, scope, bornEpoch, inval, consistency, options, graced) {
	if (error instanceof SkipSignalError) return { kind: "skipped" };
	if (isNotFound(error)) {
		if (options.notFoundTtl === void 0) throw error;
		if (graced) {
			if (await gracedStillServable(inval, consistency, graced.hit.envelope, config.defaults?.onUnavailable, error)) return negSuppressed(config, key, graced);
			return await negWriteFilled(config, key, tags, scope, bornEpoch, inval, consistency);
		}
		return {
			error,
			kind: "not-found",
			neg: await negWriteFilled(config, key, tags, scope, bornEpoch, inval, consistency),
			scope
		};
	}
	if (options.grace !== void 0 && staleIfErrorOf(config)(error)) {
		if (graced) {
			if (await gracedStillServable(inval, consistency, graced.hit.envelope, config.defaults?.onUnavailable, error)) return {
				kind: "graced",
				served: graced,
				value: graced.hit.value
			};
			return {
				error,
				kind: "fenced"
			};
		}
		return {
			error,
			kind: "retriable-failed"
		};
	}
	throw error;
}
/**
* Fill: run the factory, fence the write-back, then born-monotone
* write-through to L2. Returns `{kind:'filled', value}` on success,
* `{kind:'fenced'}` when the fill was fenced by a hard purge visible at write-back
* (never store or serve a fenced value), `{kind:'skipped'}` on
* `ctx.skip()` (M10/invariant 24), or `{kind:'graced', value}` when the
* factory threw a retriable error and `graced` — the currently-known
* stale-but-servable `val` — is STILL servable once re-validated at serve time.
*`bornEpoch` is captured by the caller at FILL START so a
* purge that bumps the epoch after the fill began cannot retroactively make the
* born value look fresh. Error-path handling is `classifyFillError` above.
*/
async function fillAndStore(config, options, key, inval, consistency, bornEpoch, graced, ambient) {
	const codec = codecOf(config);
	const { l2 } = config;
	if (!l2) return notImplemented("cache.getOrSet (no l2 configured)", config, options);
	const declaredUserTags = sortedUserTags(options.tags);
	const declaredTags = envelopeTags(declaredUserTags, key, config.namespace);
	const accum = makeTagAccumulator(declaredUserTags);
	const resolvedScope = resolveScope(options);
	const scope = resolvedScope.literal;
	const guard = buildRequestGuard(config, options);
	const lockHandle = await maybeLock(config, options);
	const ctx = {
		dependsOn(tag, dependsOptions) {
			if (dependsOptions?.l3 === false) notImplemented("FactoryCtx.dependsOn({ l3: false }) — deferred to v0.5", config, options);
			if (accum.isSettled()) throw new FactorySettledError(key);
			accum.addMembership(tag);
			recordDependency(ambient, {
				scope: fillScopeEvidence(scope),
				tags: [tag],
				...dependsOptions?.l3 === void 0 ? {} : { l3: dependsOptions.l3 }
			});
		},
		fail(err) {
			if (err === void 0) throw new Error("factory failed (non-retriable)");
			if (err instanceof Error) throw err;
			throw new Error("factory failed (non-retriable)", { cause: err });
		},
		/** Deferred to v0.4 — explicit grace-reuse from within a fill is not part of this phase. */
		reuseGraced() {
			return notImplemented("FactoryCtx.reuseGraced (deferred to v0.4)", config, options);
		},
		...guard.request === void 0 ? {} : { request: guard.request },
		setTags(tags) {
			if (accum.isSettled()) throw new FactorySettledError(key);
			accum.author(tags);
		},
		/** Deferred to v0.4 — the TTL-override (what it overrides, how it meets grace and
		* the duration grammar) is a v0.4 design slot, not a silent no-op (spec-surface sweep). */
		setTtl() {
			notImplemented("FactoryCtx.setTtl (deferred to v0.4)", config, options);
		},
		signal: new AbortController().signal,
		skip() {
			throw new SkipSignalError();
		}
	};
	try {
		let value;
		try {
			value = await options.factory(ctx);
		} catch (error) {
			accum.markSettled();
			return await classifyFillError(error, config, key, declaredTags, scope, bornEpoch, inval, consistency, options, graced);
		}
		accum.markSettled();
		const userTags = accum.settleUserTags();
		validateUserTags(userTags);
		const tags = envelopeTags(userTags, key, config.namespace);
		if (inval && tags.length > 0 && !inval.fenceWriteBack(tags, bornEpoch)) return { kind: "fenced" };
		const bornMs = config.clock.now();
		const encoded = codec.encode(value);
		const fence = lockHandle?.fence;
		const envelope = {
			bornEpoch,
			bornMs,
			enc: "json",
			grace: 0,
			key,
			kind: "val",
			scope,
			size: encoded.length,
			src: "factory",
			storedAt: bornMs,
			tags,
			ttl: 0,
			v: 3,
			val: value,
			codecId: codec.id,
			...fence === void 0 ? {} : { fence }
		};
		const written = resolvedScope.l1Only || guard.wasTouched() ? {
			durable: false,
			writeSuppressed: false
		} : await bornMonotoneWrite(config, l2, key, encoded, envelope, inval, consistency);
		return {
			bornEpoch,
			bornMs,
			durable: written.durable,
			kind: "filled",
			storedKind: "val",
			tags,
			value,
			writeSuppressed: written.writeSuppressed
		};
	} finally {
		await lockHandle?.release();
	}
}
/** Write-back attempt budget: fence-taught retries. A fill fenced by a
* hard purge that landed mid-fill re-mints its `bornEpoch` from the now-advanced verified
* high-water and refetches, up to this many attempts before failing typed. */
var WRITE_BACK_ATTEMPT_BUDGET = 3;
/**
* `fillAndStore` fronted by the CHECK-THEN-FETCH strong-miss reorder
* and the fence-taught retry. On a
* `strong`, registry-backed fill the live un-memoized registry check
* (`inval.liveCheckForFill`) runs over the fill's tags BEFORE the factory; the fill's
* `bornEpoch` is raised to the now-current verified high-water, so the born value
* postdates every purge that check observed — hard OR soft. A strong read
* therefore never serves a stale-born or hard-dead value, and two strong reads of an
* unmutated key agree.
*
* If the LOCAL write-back fence (`fenceWriteBack`) fences the fill — a hard purge
* landed DURING the factory and was delivered/verified into local knowledge — the fill
* RE-MINTS `bornEpoch` from the now-advanced verified high-water (`localBornEpoch`,
* which a resync or this call's own live check has caught up between attempts, per
* `#recordChecked`) and REFETCHES: the honest post-acceptance fetch the purge demanded,
* which borns past it and passes the fence ( observations commission later
* fetches, they never bless the earlier one). Bounded by the attempt budget; an
* attempt whose re-mint cannot advance the floor gives up (the fence is not from an
* observable purge). Exhaustion returns `fenced` — `getOrSet` throws `FencedError`,
* `getOrSetEntry` misses. `eventual`/registry-absent/tags-empty fills skip
* the pre-fetch check but share the retry.
*/
async function fillAndStoreChecked(config, options, key, inval, consistency, bornEpoch, graced, ambient) {
	const tags = inval ? envelopeTags(sortedUserTags(options.tags), key, config.namespace) : [];
	let bornEpochForFill = bornEpoch;
	if (consistency === "strong" && inval && tags.length > 0) {
		const checkedThrough = await inval.liveCheckForFill(tags, config.defaults?.onUnavailable);
		bornEpochForFill = Math.max(bornEpochForFill, checkedThrough);
	}
	for (let attempt = 0; attempt < WRITE_BACK_ATTEMPT_BUDGET; attempt++) {
		const result = await fillAndStore(config, options, key, inval, consistency, bornEpochForFill, graced, ambient);
		if (result.kind !== "fenced" || !inval || tags.length === 0) return result;
		const reMinted = Math.max(bornEpochForFill, inval.localBornEpoch());
		if (reMinted === bornEpochForFill) return result;
		bornEpochForFill = reMinted;
	}
	return { kind: "fenced" };
}
/** A fresh, empty singleflight registry for one isolate. */
function newSingleflight() {
	return {
		byIdentity: /* @__PURE__ */ new Map(),
		byKey: /* @__PURE__ */ new Map()
	};
}
/**
* The singleflight JOIN IDENTITY ( strict option-compatibility, step
* the canonical key plus every structurally-relevant option a
* waiter would otherwise silently miss. Two concurrent calls coalesce onto ONE
* factory invocation iff their join identities are byte-identical strings.
*
*   - `key`: the already-canonicalized (ns-versioned, scoped) key — two calls
*     for different keys must never coalesce.
*   - `consistency`: a `strong` request never joins a non-strong (`eventual`)
*     fill — an eventual waiter MAY join a strong fill (strictly safer), so the
*     identity is keyed on the REQUESTING call's own consistency, not some
*     merged value; a `strong` caller's identity differs from an `eventual`
*     caller's, so they never hash equal and each strong call always runs its
*     own fill, while two `eventual` calls (or two `strong` calls) still share.
*   - `apiForm`: `getOrSet` vs `getOrSetEntry` — a plain-`T` waiter must
*     never join an entry-form fill's ctx (skip-capability differs, M10).
*   - structural options (GPT note): tags (sorted+deduped, so order never
*     matters), ttl, grace, scope, notFoundTtl, codec id — a mismatch here means
*     a waiter could receive an envelope missing its own declared dependency
*     tags, the exact stringly-typed no-op class this library exists to kill.
*
* `dev` mismatch handling (throw vs a separate fill + telemetry event) belongs
* to a later cluster; this cluster only builds the identity and coalesces
* exact matches — a mismatch here simply produces a different map key, so it
* already runs its own separate fill (the safe default) without yet emitting
* `singleflight_option_mismatch` or dev-throwing.
*/
function joinIdentity(key, consistency, apiForm, codec, options) {
	const userTags = sortedUserTags(options.tags);
	return JSON.stringify({
		apiForm,
		codecId: codec.id,
		consistency,
		grace: options.grace ?? null,
		key,
		notFoundTtl: options.notFoundTtl ?? null,
		scope: scopeLiteral(options),
		tags: userTags,
		ttl: options.ttl ?? null
	});
}
/** A structural options mismatch between concurrent same-key fills.
* The message points at the fix (declare compatible options); dev throws it. */
var SingleflightOptionMismatchError = class extends Error {
	constructor(userKey) {
		super(`singleflight option mismatch for key ${JSON.stringify(userKey)}: concurrent calls declared incompatible options (tags/ttl/grace/scope/notFoundTtl/codec). Make the options identical, or serialize the calls.`);
		this.name = "SingleflightOptionMismatchError";
	}
};
/**
* Detect + handle a singleflight OPTION MISMATCH: another fill for
* the SAME canonical key is already in flight under a DIFFERENT join identity.
* `dev:true` throws (a caller bug surfaced loudly); prod runs a SEPARATE fill and
* emits `singleflight_option_mismatch` — carrying the colliding key + the
* declared tags (hosted sinks salt every field, INV-15). A matching-identity
* joiner never reaches here (it joined in `singleflight` before this ran).
*/
function handleOptionMismatch(map, key, identity, config, options) {
	const identities = map.byKey.get(key);
	if (identities === void 0) return;
	let incumbent;
	for (const [inFlight, record] of identities) if (inFlight !== identity) {
		incumbent = record;
		break;
	}
	if (incumbent === void 0) return;
	if (config.dev === true) throw new SingleflightOptionMismatchError(options.key);
	emitTelemetry(config, {
		key,
		tags: incumbent.envelopeTags,
		type: "singleflight_option_mismatch"
	});
}
/** Track this identity as in flight for its canonical key (mismatch detection),
* remembering its full stored tag set for a later mismatch event's tag ref. */
function trackKeyIdentity(map, key, identity, fillEnvelopeTags) {
	const identities = map.byKey.get(key) ?? /* @__PURE__ */ new Map();
	const existing = identities.get(identity);
	if (existing) existing.count += 1;
	else identities.set(identity, {
		count: 1,
		envelopeTags: fillEnvelopeTags
	});
	map.byKey.set(key, identities);
}
/** Untrack this identity once its fill settles (mismatch detection cleanup). */
function untrackKeyIdentity(map, key, identity) {
	const identities = map.byKey.get(key);
	if (identities === void 0) return;
	const record = identities.get(identity);
	if (record !== void 0) {
		record.count -= 1;
		if (record.count <= 0) identities.delete(identity);
	}
	if (identities.size === 0) map.byKey.delete(key);
}
/**
* Run `fill` under in-isolate singleflight (invariant 5): if a fill with
* the SAME join identity is already in flight, join it instead of starting a
* second factory invocation — both callers resolve to the one result. Otherwise,
* a concurrent fill for the same KEY under a DIFFERENT identity is an OPTION
* MISMATCH: dev throws, prod runs a separate fill + emits the event
* (`handleOptionMismatch`). Then register this call's fill and run it.
*
* CRITICAL — coalescing under `Promise.all` / same-tick concurrent callers
* (not just `fc.scheduler` interleavings): the in-flight promise is registered
* into the map SYNCHRONOUSLY, in the same microtask turn as the identity
* lookup, with NO `await` between the `byIdentity.get` check and the
* registration. `fill()` itself is only invoked (and starts doing real async
* work) after registration. Two callers racing through `Promise.all` each
* invoke `singleflight` synchronously up to this point; the first to run
* (JS's single-threaded execution order gives the two `Promise.all` callbacks
* a well-defined sequential dispatch — there is no `await` before either call
* reaches here) wins the identity check and registers, so a same-identity
* second call synchronously observes the first's entry and joins it, while a
* different-identity second call synchronously observes the mismatch. The entry
* is removed in a `finally` once the fill settles (success or failure) — a
* subsequent, non-concurrent call always runs its own fresh fill.
*/
async function singleflight(map, key, identity, config, options, fill) {
	const existing = map.byIdentity.get(identity);
	if (existing) return await existing;
	handleOptionMismatch(map, key, identity, config, options);
	trackKeyIdentity(map, key, identity, envelopeTags(sortedUserTags(options.tags), key, config.namespace));
	const registration = (async () => {
		try {
			return await fill();
		} finally {
			map.byIdentity.delete(identity);
			untrackKeyIdentity(map, key, identity);
		}
	})();
	map.byIdentity.set(identity, registration);
	return await registration;
}
/**
* Open the mint for one read, snapshotting the `bornEpoch` floor SYNCHRONOUSLY — the writer's
* applied registry epoch at fill start. Taking it at OPEN is the fence-critical part:
* a purge that lands via a concurrent bus frame after this call cannot lift the floor, so a fill
* that BEGAN before a concurrent purge still fences correctly at write-back (INV-9/INV-12).
* Registry-absent ⇒ 0.
*/
function openBornMint(inval) {
	const floor = inval ? inval.localBornEpoch() : 0;
	return { stamp: (checkedThrough) => Math.max(floor, checkedThrough) };
}
/**
* What a SERVE can prove about the served value's durability. A serve
* reports only what ITS OWN evidence supports:
*
*   - found in L2 ⇒ `true`. The read itself observed the value in the shared tier, so the claim is
*     backed by something this turn actually did.
*   - the STORED scope resolves L1-only (a `usr:` principal) ⇒ `false`. Derived from the identity
*     the entry was stored under, not from this turn: such an entry is never published to L2, so
*     the kernel KNOWS it is not durable.
*   - otherwise ⇒ ABSENT. A `pub`/`ten:` entry found in L1, or a scope the surface did not return
* readably: the L1 envelope carries no write-back record, and the serve algorithm does
*     not consult L2 on an L1 hit, and adding a read to populate this field would make an L1 hit
*     cost what the driver contract forbids. The write-through outcome simply cannot be settled here, and
*     an unsettled question is reported as such rather than guessed in either direction.
*
* Returned as a spread FRAGMENT so absence is STRUCTURAL — the property simply not present, never
* a sentinel and never an explicit `undefined` (`exactOptionalPropertyTypes` keeps the difference
* observable to a consumer that compares against `false`).
*/
function serveDurability(tier, hit) {
	if (tier === "l2") return { durable: true };
	const evidence = hitScopeEvidence(hit);
	if (evidence.kind === "readable" && storedScopeIsL1Only(evidence.literal)) return { durable: false };
	return {};
}
/**
* The error-path grace serve: a stored candidate that re-validated as
* still-servable is served `stale:true` after the fill failed. `graced` (a retriable failure)
* carries `servedOnError`; `neg-suppressed` (a 404, which is not `staleIfError`) does not.
*
* Every field here reports the CANDIDATE — the entry actually being served — rather than the fill
* that failed: its tier is the tier it was read from, its durability what that tier can prove,
*its age its own stamp. The earlier shape named `tier:'l2'` and `durable:true` as
* constants, for a value that may have been found in L1 and never written to a shared tier at all.
*/
function gracedServe(attempt, ambient) {
	const { served } = attempt;
	recordServedHit(ambient, served.hit);
	return {
		...serveDurability(served.tier, served.hit),
		bornMs: served.hit.envelope.bornMs,
		skipped: false,
		stale: true,
		tier: served.tier,
		value: attempt.value,
		...attempt.kind === "graced" ? { servedOnError: true } : {}
	};
}
/**
* One caller's error-path tail over a SHARED retriable failure: the fill that
* failed produced no serve of its own (a blind leader), so it handed out the classified error as a
* FACT. This runs on behalf of a caller against ITS OWN read — a singleflight joiner that read a
* copy of its own, or the blind leader itself. Each state resolves to exactly what a SOLO caller in
* that same state resolves to, because a caller's outcome must not depend on which caller happened
* to win singleflight:
*
*   - this caller read a `val` candidate that STILL re-validates as servable now ( a
*     hard delete during the fill must not resurrect it): serve it `{stale:true, servedOnError}`
* with THIS candidate's own evidence (tier/durable/age), exactly as a leader's own
*     graced serve would. `staleCandidate` is already a `ServedCandidate`, so the evidence-riding
*     machinery is reused unchanged.
*   - this caller read a candidate that re-validates DEAD: it REFUSES the serve and takes the
*     classified failure — returned as the FENCED OUTCOME, not thrown, so the wrapper applies the
*     same per-form mapping the solo path gets for this state (the plain form rethrows the typed
*     error, the entry form resolves a miss). Throwing here would make an entry-form joiner reject
*     where an identical solo call resolves a miss.
*   - this caller read no candidate at all: the classified error is rethrown, exactly as a solo
* caller with nothing to fall back on rethrows it.
*
* A joiner's recourse is thus judged against its OWN read, never the leader's disposition — the
* leader's blindness is a fact it shares, not a verdict it imposes.
*/
async function runOwnErrorTail(config, options, inval, consistency, staleCandidate, error, ambient) {
	if (options.grace !== void 0 && staleCandidate !== void 0) {
		if (await gracedStillServable(inval, consistency, staleCandidate.hit.envelope, config.defaults?.onUnavailable, error)) return gracedServe({
			kind: "graced",
			served: staleCandidate,
			value: staleCandidate.hit.value
		}, ambient);
		return {
			durable: false,
			error,
			skipped: false,
			stale: false,
			tier: "miss"
		};
	}
	throw error;
}
/** The read tiers in order: L1 then L2. Absent tiers are skipped. */
function tiersOf(config) {
	const tiers = [];
	if (config.l1) tiers.push([config.l1, "l1"]);
	if (config.l2) tiers.push([config.l2, "l2"]);
	return tiers;
}
/** Write the freshly-filled value into L1 (write-through) — best-effort. The whole fill
* outcome rides in, because every field written here belongs to that ONE fill: its `bornEpoch` so
* both tiers validate identically ( `fallbackBornEpoch` covers the neg-write outcome, which
* stamps none), its `bornMs` so both tiers report the same age ( birth is an event, and a value
* has ONE: the stamp is minted at fill and carried here, never re-read off the clock, which would
* date the L1 copy to the moment the L2 write finished and make one value two ages), and its STORED
* tag set (settle-union) so the two tiers are tag-coherent: a later L1 hit replays the
* same purge handles L2 carries.
*
* A `kind:'neg'` fill whose write LANDED stores nothing here and EVICTS whatever L1 still holds for
* the key — two halves of one rule, and neither is sufficient alone. A neg whose write the guard
* SUPPRESSED does neither: eviction follows the write's fate (see below).
*
* It stores nothing because the neg's storage is `writeNegEnvelope`'s single L2 write
*  — that function is the neg's whole write path, and it deliberately writes one tier. What
* this write-through would put in L1 is a `kind:'val'`-stamped copy of the neg's placeholder body:
* an envelope claiming to be a VALUE, holding a value no origin produced. Read back, it would
* surface that placeholder (the read path's neg rule keys on the stored `kind`, which this write
* misstates) and, worse, stand as a grace/stale-if-error candidate — precisely what invariant 7 forbids a
* neg from ever being. Today the default codec cannot encode `undefined` to a decodable body, so
* such a write lands as an unreadable entry that always misses; that is an accident of one codec,
* not a guarantee any other owes us.
*
* It evicts because L1 may ALREADY hold an OLDER value for this key. A fill runs when the read
* found nothing servable — and a tier read that a classified transient failure suppressed is
* a plain MISS over an entry that is still sitting there. Refusing the write alone leaves that
* entry standing, and it outlives the 404: once the tier reads cleanly again L1 is consulted first
* (steps 2/4) and answers with the superseded value, in preference to the neg L2 now holds —
* the origin's not-found reverted by a tier that was merely unreadable for a moment. Deleting ends
* the turn with L1 holding nothing for the key, so reads fall through to the tier that remembers
* the 404 and a served neg still reports the tier and durability its one write proves.
*
* Whether a neg SHOULD also LIVE in L1 is a storage-policy question the design does not settle — it would
* change that reported tier and durability — so this evicts rather than storing a neg envelope,
* closing the mislabel and the resurrection without deciding it.
*
* EVICTION FOLLOWS THE WRITE'S FATE. Both halves above are premised on a neg that
* LANDED: the eviction exists to stop a superseded L1 copy outliving a 404 the durable tier now
* remembers. A neg the guard suppressed leaves the durable tier remembering no 404 at all — the
* verdict the suppression rendered is that the val stands — so deleting that val's L1 copy would
* contradict the verdict on the way out, and cost a refill of a value that is still good. A
* suppressed write mutates nothing, in either tier. */
async function hydrateL1(config, codec, key, options, attempt, fallbackBornEpoch) {
	const { l1 } = config;
	if (!l1) return;
	if (attempt.storedKind === "neg") {
		if (attempt.writeSuppressed) return;
		try {
			await l1.delete(key);
		} catch {}
		return;
	}
	const { bornMs, tags, value } = attempt;
	const bornEpoch = attempt.bornEpoch ?? fallbackBornEpoch;
	try {
		await l1.set(key, codec.encode(value), { metadata: {
			bornEpoch,
			bornMs,
			codecId: codec.id,
			enc: "json",
			kind: "val",
			scope: scopeLiteral(options),
			tags,
			v: 3
		} });
	} catch {}
}
/**
* The successful foreground fill's serve: record the dependency, hydrate L1, and
* report the fill's own outcome.
*
* This is the one serve that can always answer `durable` (leaves it untouched): it performed
* the write-back itself and awaited the result, so it reports a real boolean rather than the absent
* arm the read-path serves need.
*/
async function filledServe(config, codec, key, options, scope, attempt, bornEpoch, ambient) {
	const storedTags = attempt.tags;
	recordDependency(ambient, {
		scope: fillScopeEvidence(scope),
		tags: storedTags
	});
	const filled = attempt.value;
	await hydrateL1(config, codec, key, options, attempt, bornEpoch);
	return {
		durable: attempt.durable,
		bornMs: attempt.bornMs,
		skipped: false,
		stale: false,
		tier: "origin",
		value: filled
	};
}
/**
* One caller's error-path neg tail over a SHARED `not_found` fact. The fill
* read no candidate of its own (a blind leader), so it recorded the fact and handed it out rather
* than deciding, from a read that saw nothing, what every waiter should be served. This decides
* DISPOSITION ONLY — it records nothing, because the recording already happened once,
* inside the fill's window. It runs on behalf of one caller against ITS OWN read (a joiner that read
* a copy of its own, or the blind leader itself), so that each caller resolves EXACTLY as a SOLO
* caller in that same state resolves — an outcome must not depend on which caller won singleflight:
*
*   - this caller read a `val` candidate that STILL re-validates as servable now: the guard serves
* that val `{stale:true}` with THIS candidate's own evidence (tier/durable/age), and — a
*     404 not being `staleIfError` — WITHOUT `servedOnError`, exactly as a solo sighted caller does.
*     The gate matches the solo path's: this arm is offered only when `grace` was declared (the solo
*     fill receives its `staleCandidate` only then).
*   - otherwise (this caller read no candidate, or its own copy re-validated dead): it takes the
*     RECORDED neg as its outcome, served `{tier:'origin'}` with that write's own evidence — the same
*     `filled` serve a solo blind caller performs over its own neg-write.
*
* A joiner's disposition is thus judged against its OWN read — its read is the existence proof the
* blind leader lacked — while the fact itself was recorded exactly once, by the fill that found it.
*/
async function runOwnNotFoundTail(config, options, inval, consistency, key, staleCandidate, fact, outerBornEpoch, ambient) {
	if (options.grace !== void 0 && staleCandidate !== void 0 && await gracedStillServable(inval, consistency, staleCandidate.hit.envelope, config.defaults?.onUnavailable, fact.error)) return gracedServe(negSuppressed(config, key, staleCandidate), ambient);
	return await filledServe(config, codecOf(config), key, options, fact.scope, fact.neg, outerBornEpoch, ambient);
}
/**
* A STALE hit → the SWR path (SWR). The OBSERVABLE contract, independent of `grace`:
* **this response always serves the stale value with `stale:true`** — never the
* refreshed/fresh result. A refresh runs synchronously (the full background /
* `waitUntil` / REFRESH_QUEUE machinery of step 6 stays deferred; a synchronous
* refresh is a placeholder that produces the same observable serve), and its result
* is written through for the NEXT read only. It never changes the value THIS call
* returns, and its failure never propagates to the caller — SWR's promise is a fast
* stale response.
*
* `servedOnError:true` iff the refresh failed RETRIABLY (per `staleIfError`)
* within a grace window (`grace` declared on the call) AND the stale copy is still
* servable after serve-time re-validation — the `graced` outcome. A
* successful refresh, a fenced/non-servable refresh, a suppressed 404 ( a neg
* never displaces a servable val, and a 404 is not `staleIfError`), and a
* non-retriable refresh failure all serve the stale value WITHOUT `servedOnError`.
* `grace` only affects whether the error path yields `servedOnError`, never whether
* the stale value is served (it always is).
*/
async function refreshOrGrace(config, options, key, inval, consistency, tier, hit, bornEpoch) {
	const codec = codecOf(config);
	let graced = false;
	try {
		const attempt = await fillAndStore(config, options, key, inval, consistency, bornEpoch, {
			hit,
			tier
		});
		if (attempt.kind === "filled") await hydrateL1(config, codec, key, options, attempt, bornEpoch);
		graced = attempt.kind === "graced" && options.grace !== void 0;
	} catch {}
	return {
		...serveDurability(tier, hit),
		bornMs: hit.envelope.bornMs,
		skipped: false,
		stale: true,
		tier,
		value: hit.value,
		...graced ? { servedOnError: true } : {}
	};
}
/**
* The core getOrSet path: L1 → L2 → foreground fill with the fence and
* Born-monotone write-back. Serves fresh directly; serves a soft-stale value
* with `stale:true`; refills on miss/dead/unknown. Shared by `getOrSet` (plain) and
* `getOrSetEntry`.
*/
async function runGetOrSet(config, options, inval, nsVersion, apiForm, inFlight, ambient) {
	const codec = codecOf(config);
	const scope = scopeLiteral(options);
	const key = canonicalKey(nsVersion.current, config.namespace, scope, options.key);
	const consistency = consistencyOf(config, options);
	const mint = openBornMint(inval);
	let checkedThrough = 0;
	let staleCandidate;
	for (const [store, tier] of tiersOf(config)) {
		const read = await readTier(store, codec, config, inval, consistency, key);
		checkedThrough = Math.max(checkedThrough, read.checkedThrough);
		if (read.hit) {
			if (read.hit.stale && consistency !== "strong") {
				recordServedHit(ambient, read.hit);
				return await refreshOrGrace(config, options, key, inval, consistency, tier, read.hit, mint.stamp(checkedThrough));
			}
			if (!read.hit.stale) {
				recordServedHit(ambient, read.hit);
				return {
					...serveDurability(tier, read.hit),
					bornMs: read.hit.envelope.bornMs,
					skipped: false,
					stale: false,
					tier,
					value: read.hit.value
				};
			}
		}
		const candidate = read.hit?.stale === true ? read.hit : read.staleCandidate;
		if (candidate !== void 0) staleCandidate = {
			hit: candidate,
			tier
		};
	}
	const bornEpoch = mint.stamp(checkedThrough);
	const attempt = await singleflight(inFlight, key, joinIdentity(key, consistency, apiForm, codec, options), config, options, async () => await fillAndStoreChecked(config, options, key, inval, consistency, bornEpoch, options.grace === void 0 ? void 0 : staleCandidate, ambient));
	if (attempt.kind === "filled") return await filledServe(config, codec, key, options, scope, attempt, bornEpoch, ambient);
	if (attempt.kind === "graced" || attempt.kind === "neg-suppressed") return gracedServe(attempt, ambient);
	if (attempt.kind === "retriable-failed") return await runOwnErrorTail(config, options, inval, consistency, staleCandidate, attempt.error, ambient);
	if (attempt.kind === "not-found") return await runOwnNotFoundTail(config, options, inval, consistency, key, staleCandidate, attempt, bornEpoch, ambient);
	if (attempt.kind === "skipped") return {
		durable: false,
		skipped: true,
		stale: false,
		tier: "miss"
	};
	return {
		durable: false,
		skipped: false,
		stale: false,
		tier: "miss",
		...attempt.error === void 0 ? {} : { error: attempt.error }
	};
}
/** Whether a raw regex-captured unit is a known `DurationUnit`. */
function isDurationUnit(unit) {
	return unit !== void 0 && Object.hasOwn(DURATION_UNIT_MS, unit);
}
/** Convert a `Duration` to milliseconds. The full `Duration` grammar is
* deferred; the retention lease and the reader's liveness heartbeat cadence need
* only a coarse ms value, so this parses the `<number><unit>` shape. */
function durationMs(duration) {
	const match = /^(?<value>[0-9]+(?:\.[0-9]+)?|\.[0-9]+)(?<unit>ms|s|m|h|d)$/u.exec(duration);
	const value = Number(match?.groups?.value ?? "0");
	const rawUnit = match?.groups?.unit;
	return value * (isDurationUnit(rawUnit) ? DURATION_UNIT_MS[rawUnit] : 1);
}
/** Build the per-instance invalidation core when a registry + bus are wired.
*Registry-absent instances (codec.test.ts) get `undefined` and keep the
* always-fresh codec path.
*
* A registry+bus instance REQUIRES an L2 replication mirror: the reader's only safe way to
* clear a bus gap is to resync off the `state`/`delta` mirror there. Constructing
* it without one is rejected LOUDLY here (dead-branch ruling) — the reader has no
* honest job with no store. This throw is dead today (a registry+bus config is only ever wired
* with L2), and it is guarded precisely because that reachability would change silently if L2
* became optional: the old handling — clearing suspicion without resyncing — was a fail-OPEN
* hole. Fail loud instead of silently reopening it. */
function buildInvalidation(config) {
	const { registry, bus, l2 } = config;
	if (!registry || !bus) return;
	if (!l2) throw new Error("createCache: a registry+bus instance requires an l2 store (the §4.2 replication mirror the reader resyncs off — E20)");
	const emit = (event) => {
		emitTelemetry(config, event);
	};
	const heartbeat = config.defaults?.heartbeatInterval;
	return new Invalidation(registry, l2, bus, heartbeat === void 0 ? void 0 : durationMs(heartbeat), emit);
}
/**
* A facade → the instance it fronts. THE ONE PLACE an alternate identity for one cache is recorded.
*
* WHY THIS EXISTS, AND WHY IT IS ONE MECHANISM RATHER THAN A HABIT. Several internal seams hang
* per-instance state off a `Cache` through a module-private WeakMap, keyed by the object itself. A
* composition that returns a WRAPPER around a `createCache` instance — `createWorkersCache` does, to
* hang its freshness carrier on the read path — hands callers an object those maps have never heard
* of. The address a seam is keyed by is therefore the identity CALLERS HOLD, and a facade that
* re-registers one seam and forgets another does not fail loudly: it produces a seam that silently
* answers `undefined` forever. Registering per-seam is the fix that does not generalize — the next
* seam, or the next facade, reopens it.
*
* So identity is aliased ONCE, here, and every seam lookup resolves through `resolveCacheIdentity`
* BEFORE keying. A new seam is covered because it resolves like the others; a new facade is covered
* because it aliases like the others. Neither has to remember the list.
*/
var identityAliases = /* @__PURE__ */ new WeakMap();
/**
* The instance `cache` stands for — itself, unless it is a facade over another.
*
* SAME-PACKAGE internal seam, NOT public API (not re-exported from `index.ts`). Every WeakMap keyed
* by a `Cache` resolves through this before keying, so a facade and the instance it fronts address
* ONE slot. A cache that is nobody's facade resolves to itself, which is every raw `createCache`
* instance and therefore the overwhelmingly common path.
*
* ONE STEP IS ENOUGH because `aliasCacheIdentity` flattens the chain at registration.
*/
function resolveCacheIdentity(cache) {
	return identityAliases.get(cache) ?? cache;
}
/**
* Record that `facade` is an alternate identity for `inner` — called ONCE, by whatever
* composition returns a wrapper, at construction. SAME-PACKAGE internal seam, NOT public API.
*
* THE TARGET IS RESOLVED AS IT IS STORED, so a facade over a facade lands on the instance at the
* bottom rather than on the middle link. That keeps `resolveCacheIdentity` a single lookup on the
* hot path while making a stack of facades correct BY CONSTRUCTION rather than by anyone noticing
* that it is a stack — the chain is flattened once, here, where it is built.
*/
function aliasCacheIdentity(facade, inner) {
	identityAliases.set(facade, resolveCacheIdentity(inner));
}
/** Cache instance → its replication poller. Module-private internal state: the
* poller is attached WITHOUT touching the public `Cache` surface. the request-piggyback driver
* (the RR adapter) is the READER of this map — the seam arrived with its consumer, so the map is no
* longer write-only and the `sonarjs/no-unused-collection` suppression that carried the named "C4"
* expiry is DELETED (its premise is gone; a suppression outliving its premise is a lie). */
var replicationPollers = /* @__PURE__ */ new WeakMap();
/**
* The replication poller attached to `cache`, or `undefined` for a registry-absent instance (which
* has no reader to drive). SAME-PACKAGE internal seam, NOT public API: it is deliberately not
* re-exported from `index.ts`, and its ONE consumer is the request-piggyback tick driver in the
* RR adapter (`src/adapters/react-router/poll-driver.ts`), which owns the staleness gate + the
* failure swallow. Kept as narrow as that consumer needs: a `Cache` in, its poller out — no other
* kernel internal crosses the boundary.
*
* Resolves identity first, so a facade over an instance finds that instance's poller. Without that
* an RR app on a wrapping factory would lose its replication poll SILENTLY — `tickIfStale` reads a
* missing poller as "registry-absent, nothing to drive" and returns.
*/
function replicationPollerOf(cache) {
	return replicationPollers.get(resolveCacheIdentity(cache));
}
/** A `RenderCollector` → its internal scope-bearing writer. Module-private: the
* public collector interface cannot carry the scope half (a public `dependsOn` has no entry
* behind it), so the kernel's automatic hit/fill recording reaches it through this map
* instead of through a widened public type or a cast. */
var collectorStates = /* @__PURE__ */ new WeakMap();
/**
* An `L3Emission` → how many dependencies the render had consumed when that emission was
* committed — hits, fills, and explicit `dependsOn` declarations. Same
* module-private-WeakMap seam as `collectorStates` above, and for a kindred reason: the count is
* a fact the ADAPTER's header decision needs and no caller of the public type does, and
* `L3Emission` is a spec-verbatim shape — so the count rides the private channel rather
* than widening a published surface with a field only this package reads.
*/
var emissionCounts = /* @__PURE__ */ new WeakMap();
/** Bank an emission's consumed-dependency count on the seam and return the emission (the ONE
* writer of `emissionCounts` — the count is banked with the emission it belongs to, so the two
* can never drift apart). */
function withManagedDependencies(emission, count) {
	emissionCounts.set(emission, count);
	return emission;
}
/**
* How many dependencies the render had consumed when `emission` was committed.
* SAME-PACKAGE internal seam, NOT public API: deliberately not re-exported from `index.ts`, and
* its ONE consumer is the React-Router middleware's header decision, where ZERO is the adapter's
* JURISDICTION FLOOR — a render that consumed no managed dependency gets a safe DEFAULT and
* nothing more (`private` filled in only where the app wrote no `Cache-Control`, an app-set
* `Cache-Tag` left where it is, nothing overridden). It is what separates that floor from an
* eligible render whose dependencies simply emit no tag.
*
* An emission this kernel never minted reads as 0, i.e. as the floor — the direction in which the
* adapter asserts least, which is the safe one for an unrecognized input.
*/
function managedDependenciesOf(emission) {
	return emissionCounts.get(emission) ?? 0;
}
/**
* A `Cache` → the accessor that yields the AMBIENT render collector for the request in
* flight. Same module-private-WeakMap seam as `replicationPollers` above, and the
* same reason: the collector must reach the kernel WITHOUT widening the public `Cache`
* surface and WITHOUT the kernel importing `node:async_hooks` (the request frame is the
* adapter's — a Workers deployment without the ALS compat flag must still be able to import
* this module).
*
* Keyed by RESOLVED identity, like every other instance-keyed seam here: an adapter binds against
* the cache the app holds, and the kernel looks up against the instance running the read. Those are
* different objects whenever a facade is in play, and this map is where they have to meet — a
* privacy gate that reads an empty collector concludes the render consumed nothing and asserts
* nothing, which is the loudest possible silence.
*/
var renderCollectors = /* @__PURE__ */ new WeakMap();
/**
* Bind the adapter's ambient-collector accessor to `cache`. SAME-PACKAGE internal
* seam, NOT public API: deliberately not re-exported from `index.ts`, and its ONE consumer is
* the React-Router middleware (`src/adapters/react-router/middleware.ts`), which owns the
* `AsyncLocalStorage` request frame the accessor reads.
*
* The consequence is the safety property, not a limitation: a cache used WITHOUT the adapter
* has no bound accessor, so the kernel records nothing, no `commitHeaders` ever runs, and no
* header is ever emitted. Nothing to leak, by construction — it fails closed.
*/
function bindRenderCollector(cache, accessor) {
	renderCollectors.set(resolveCacheIdentity(cache), accessor);
}
/** The ambient collector's internal writer for `cache`, or `undefined` when the adapter bound
* none / no request frame is open. */
function ambientCollectorOf(cache) {
	const collector = renderCollectors.get(resolveCacheIdentity(cache))?.();
	return collector === void 0 ? void 0 : collectorStates.get(collector);
}
/** Build the tick-driven replication poll driver alongside the reader, under the
* SAME condition (registry+bus+L2, already validated by `buildInvalidation`). It is CONSTRUCTED
* here — never a knip entry: the construction makes `replication-poller.ts` reachable, so deleting
* this wiring reds knip, which is the point (the dead-code gate becomes the wiring's tooth). The
* poller runs no timer of its own; C4 drives it (deterministic ticks drive it in tests). The
* `maxSyncLag` base cadence defaults to the Workers profile (60s) unless config overrides. */
function buildReplicationPoller(config, reader) {
	const { l2, registry } = config;
	if (!l2 || !registry) throw new Error("createCache: the replication poller requires the same registry+l2 as the reader (§4.2)");
	const maxSyncLag = config.defaults?.maxSyncLag;
	const options = maxSyncLag === void 0 ? {} : { pollBaseMs: durationMs(maxSyncLag) };
	return new ReplicationPoller(reader, l2, mirrorKeys(registry.regId), config.rng, options);
}
/**
* Register this instance's retention lease — DEFERRED TO FIRST USE, best-effort
* and fire-and-forget; a registration failure never blocks anything (the GC horizon extends under
* uncertainty).
*
* IT USED TO FIRE FROM `createCache`, AND THAT IS AN RPC AT CONSTRUCTION. Construction performs no
* I/O: at module scope the platform refuses the call outright, and the bare `try{}catch{}` around it
* — swallowing by design, which is right for a best-effort lease — meant the refusal was invisible.
* A lease that never registered and never said so is the worst of both: the Coordinator's GC horizon
* is not extended by an instance it never heard from, and nothing anywhere reports it.
*
* Returns a function the first read calls. It runs AT MOST ONCE per instance and never blocks: the
* lease is a hint to the GC horizon, not a precondition for reading, so making a read wait on it
* would trade a correctness-neutral registration for real latency on the request that happened to be
* first.
*/
function retentionRegistrar(config) {
	const { registry } = config;
	const retention = config.defaults?.maxEntryRetention;
	if (!registry || retention === void 0) return () => {};
	let registered = false;
	return () => {
		if (registered) return;
		registered = true;
		(async () => {
			try {
				await registry.registerRetention(durationMs(retention));
			} catch {}
		})();
	};
}
/** Resolve a purge selector to its registry tag(s). A key selector maps to
* the reserved `__k` tag digest of that key's canonical form — the
* IDENTICAL digest `expire`/`delete` by tag would never see, since the literal
* `__k` form is internal and only `expire({key})`/`delete({key})` may target
* it. No `scope` and no request on the key selector ⇒ the same `pub` default
* a scope-less, contextless `getOrSet` call stamps, so
* `expire({key: options.key})` targets exactly the tag a matching contextless
* `getOrSet({key: options.key})` carries. */
function selectorTags(selector, config, nsVersion) {
	if ("tag" in selector) return [selector.tag];
	return [keyTag(canonicalKey(nsVersion.current, config.namespace, scopeLiteral(), selector.key))];
}
/** Wrap a registry ack into a `PurgeResult`. `flushed`/`edgePurged`
* resolve minimally here — the registry mutation already durably accepted + emitted
* the __delta/__state batch (steps 1–3); the KV-acceptance handoff and CDN
* drain (step 4) are deferred to a later cluster. */
function purgeResult(ack) {
	return {
		edgePurged: async () => {
			await Promise.resolve();
		},
		epoch: ack.epoch,
		flushed: async () => {
			await Promise.resolve();
		},
		matchedHint: "unknown"
	};
}
/**
* Build a cache instance. Wires the dual-epoch invalidation core when a
* registry + bus are present; otherwise runs the registry-absent codec path.
* Not-yet-implemented verbs still throw `NotImplementedError`.
*
* CONSTRUCTION PERFORMS NO I/O. What happens here is bookkeeping over the arguments —
* addresses are recorded, components are pointed at each other, and no door is opened. Every
* deferred piece (the bus subscription via `Invalidation`'s lazy dial; the retention lease via
* `registerRetention` below) waits for the first read that needs it and runs inside that read's
* request context. This is what makes one cache per isolate, built at module scope, a legal and
* expected shape rather than an isolate that refuses to start.
*/
function createCache(config) {
	const inval = buildInvalidation(config);
	const poller = inval === void 0 ? void 0 : buildReplicationPoller(config, inval);
	/** The lease, armed but NOT fired — the first read pays it, and only the first. */
	const registerRetention = retentionRegistrar(config);
	const nsVersion = new NsVersion();
	const inFlight = newSingleflight();
	const ambientCell = { lookup() {} };
	const ambient = () => ambientCell.lookup();
	const instance = {
		async clear() {
			const { registry } = config;
			if (!registry) return notImplemented("cache.clear (no registry configured)", config);
			inval?.ensureSubscribed();
			nsVersion.bump();
			return purgeResult(await registry.delete([nsTag(config.namespace)]));
		},
		collect() {
			const required = [];
			let committed = false;
			let lateTag = false;
			let managedDependencies = 0;
			let poisoned = false;
			let scopeUnreadable = false;
			const records = [];
			const state = {
				get records() {
					return records;
				},
				record(dependency) {
					records.push(dependency);
					const { l3, scope, tags } = dependency;
					managedDependencies += 1;
					if (scope.kind === "unreadable") {
						scopeUnreadable = true;
						poisoned = true;
					} else if (scope.kind === "readable" && scope.literal !== PUBLIC_SCOPE) poisoned = true;
					if (l3 === false) return;
					if (committed) {
						lateTag = true;
						return;
					}
					required.push(...tags);
				}
			};
			const collector = {
				commitHeaders(budget) {
					const cacheTags = required.filter((tag) => !isReservedTag(tag));
					committed = true;
					if (poisoned) return withManagedDependencies({
						cacheTags: [],
						eligible: false,
						ineligibleReason: scopeUnreadable ? "scope-unreadable" : "scope"
					}, managedDependencies);
					if (lateTag) return withManagedDependencies({
						cacheTags,
						eligible: false,
						ineligibleReason: "late-tag"
					}, managedDependencies);
					const maxTags = budget?.maxTags;
					if (maxTags !== void 0 && cacheTags.length > maxTags) return withManagedDependencies({
						cacheTags,
						eligible: false,
						ineligibleReason: "budget"
					}, managedDependencies);
					const maxBytes = budget?.maxBytes;
					if (maxBytes !== void 0) {
						if (new TextEncoder().encode(cacheTags.join(",")).length > maxBytes) return withManagedDependencies({
							cacheTags,
							eligible: false,
							ineligibleReason: "budget"
						}, managedDependencies);
					}
					return withManagedDependencies({
						cacheTags,
						eligible: true
					}, managedDependencies);
				},
				dependsOn(tag, dependsOptions) {
					state.record({
						scope: { kind: "none" },
						tags: [tag],
						...dependsOptions?.l3 === void 0 ? {} : { l3: dependsOptions.l3 }
					});
				}
			};
			collectorStates.set(collector, state);
			return collector;
		},
		async delete(selector) {
			const { registry } = config;
			if (!registry) return notImplemented("cache.delete (no registry configured)", selector);
			inval?.ensureSubscribed();
			return purgeResult(await registry.delete(selectorTags(selector, config, nsVersion)));
		},
		/** Deferred to v0.4 — namespace-wide hard purge behind the origin-load guard. */
		async deleteAll(guard) {
			return notImplemented("cache.deleteAll (deferred to v0.4)", config, guard);
		},
		async expire(selector) {
			const { registry } = config;
			if (!registry) return notImplemented("cache.expire (no registry configured)", selector);
			inval?.ensureSubscribed();
			return purgeResult(await registry.expire(selectorTags(selector, config, nsVersion)));
		},
		/** Deferred to v0.4 — namespace-wide soft purge behind the origin-load guard. */
		async expireAll(guard) {
			return notImplemented("cache.expireAll (deferred to v0.4)", config, guard);
		},
		/**
		* Witness a key's state — introspection, not testimony: every field is drawn from
		* an ALREADY-KNOWN source, and nothing here mutates state. The tier is a NON-mutating presence
		* probe (`store.get`, never `hydrateL1` ); the verdict is a pure classify over the
		* reader's CURRENT knowledge (no live check, no resync ); the reader state is exposed
		* via read-only accessors; the request attribution is the bound render collector's
		* request-scoped records. The key is canonicalized at the DEFAULT (`pub`) scope — the
		* signature carries no scope, so `explain` introspects the default-scoped entry for that key.
		*/
		async explain(key) {
			const codec = codecOf(config);
			const canonical = canonicalKey(nsVersion.current, config.namespace, scopeLiteral(), key);
			const reader = {
				appliedEpoch: inval?.appliedEpoch ?? 0,
				channel: inval?.channel ?? "never-established",
				suspect: inval?.suspect ?? false,
				terminal: poller?.terminal ?? false
			};
			const requestDependencies = explainAttribution(ambient);
			const attribution = requestDependencies === void 0 ? {} : { requestDependencies };
			let readFailed = false;
			for (const [store, tier] of tiersOf(config)) {
				const probed = await probeStoredEntry(store, codec, config, canonical);
				if (probed.kind !== "found") {
					readFailed ||= probed.kind === READ_FAILED_KIND;
					continue;
				}
				return {
					identity: {
						bornEpoch: probed.envelope.bornEpoch,
						kind: probed.envelope.kind,
						scope: probed.scope,
						tags: probed.envelope.tags,
						ttl: probed.ttl
					},
					key,
					kind: "present",
					reader,
					tier,
					verdict: inval ? inval.explainVerdict(probed.envelope) : FRESH_VERDICT,
					...attribution
				};
			}
			if (readFailed) return {
				key,
				kind: READ_FAILED_KIND,
				reader,
				...attribution
			};
			return {
				key,
				kind: "absent",
				reader,
				tier: "miss",
				...attribution
			};
		},
		async getOrSet(options) {
			registerRetention();
			inval?.ensureSubscribed();
			const outcome = await runGetOrSet(config, options, inval, nsVersion, "getOrSet", inFlight, ambient);
			if (outcome.value === void 0 && outcome.tier === "miss") {
				if (outcome.error !== void 0) throw outcome.error;
				throw new FencedError(options.key);
			}
			return outcome.value;
		},
		async getOrSetEntry(options) {
			registerRetention();
			inval?.ensureSubscribed();
			const outcome = await runGetOrSet(config, options, inval, nsVersion, "getOrSetEntry", inFlight, ambient);
			return {
				age: outcome.bornMs === void 0 ? 0 : Math.max(0, config.clock.now() - outcome.bornMs),
				...outcome.durable === void 0 ? {} : { durable: outcome.durable },
				skipped: outcome.skipped,
				stale: outcome.stale,
				tier: outcome.tier,
				value: outcome.value,
				...outcome.servedOnError === void 0 ? {} : { servedOnError: outcome.servedOnError }
			};
		}
	};
	ambientCell.lookup = () => ambientCollectorOf(instance);
	if (poller !== void 0) replicationPollers.set(instance, poller);
	return instance;
}
//#endregion
//#region ../../node_modules/.pnpm/react-router@8.3.0_react-dom@19.2.7_react@19.2.7__react@19.2.7/node_modules/react-router/dist/production/lib/router/url.js
/**
* react-router v8.3.0
*
* Copyright (c) Remix Software Inc.
*
* This source code is licensed under the MIT license found in the
* LICENSE.md file in the root directory of this source tree.
*
* @license MIT
*/
var ABSOLUTE_URL_REGEX = /^(?:[a-z][a-z0-9+.-]*:|[\\/]{2})/i;
var PROTOCOL_RELATIVE_URL_REGEX = /^[\\/]{2}/;
function normalizeProtocolRelativeUrl(url, protocol) {
	return protocol + url.replace(/\\/g, "/");
}
//#endregion
//#region ../../node_modules/.pnpm/react-router@8.3.0_react-dom@19.2.7_react@19.2.7__react@19.2.7/node_modules/react-router/dist/production/lib/router/history.js
/**
* react-router v8.3.0
*
* Copyright (c) Remix Software Inc.
*
* This source code is licensed under the MIT license found in the
* LICENSE.md file in the root directory of this source tree.
*
* @license MIT
*/
function invariant(value, message) {
	if (value === false || value === null || typeof value === "undefined") throw new Error(message);
}
function warning(cond, message) {
	if (!cond) {
		if (typeof console !== "undefined") console.warn(message);
		try {
			throw new Error(message);
		} catch (e) {}
	}
}
function createKey() {
	return Math.random().toString(36).substring(2, 10);
}
/**
* Creates a Location object with a unique key from the given Path
*/
function createLocation(current, to, state = null, key, mask) {
	return {
		pathname: typeof current === "string" ? current : current.pathname,
		search: "",
		hash: "",
		...typeof to === "string" ? parsePath(to) : to,
		state,
		key: to && to.key || key || createKey(),
		mask
	};
}
/**
* Creates a string URL path from the given pathname, search, and hash components.
*
* @public
* @category Utils
* @param path The pathname, search, and hash components to combine.
* @returns The combined URL path.
*/
function createPath({ pathname = "/", search = "", hash = "" }) {
	if (search && search !== "?") pathname += search.charAt(0) === "?" ? search : "?" + search;
	if (hash && hash !== "#") pathname += hash.charAt(0) === "#" ? hash : "#" + hash;
	return pathname;
}
/**
* Parses a string URL path into its separate pathname, search, and hash components.
*
* @public
* @category Utils
* @param path The URL path to parse.
* @returns The parsed pathname, search, and hash components.
*/
function parsePath(path) {
	let parsedPath = {};
	if (path) {
		let hashIndex = path.indexOf("#");
		if (hashIndex >= 0) {
			parsedPath.hash = path.substring(hashIndex);
			path = path.substring(0, hashIndex);
		}
		let searchIndex = path.indexOf("?");
		if (searchIndex >= 0) {
			parsedPath.search = path.substring(searchIndex);
			path = path.substring(0, searchIndex);
		}
		if (path) parsedPath.pathname = path;
	}
	return parsedPath;
}
//#endregion
//#region ../../node_modules/.pnpm/react@19.2.7/node_modules/react/cjs/react.production.js
/**
* @license React
* react.production.js
*
* Copyright (c) Meta Platforms, Inc. and affiliates.
*
* This source code is licensed under the MIT license found in the
* LICENSE file in the root directory of this source tree.
*/
var require_react_production = /* @__PURE__ */ __commonJSMin(((exports) => {
	var REACT_ELEMENT_TYPE = Symbol.for("react.transitional.element");
	var REACT_PORTAL_TYPE = Symbol.for("react.portal");
	var REACT_FRAGMENT_TYPE = Symbol.for("react.fragment");
	var REACT_STRICT_MODE_TYPE = Symbol.for("react.strict_mode");
	var REACT_PROFILER_TYPE = Symbol.for("react.profiler");
	var REACT_CONSUMER_TYPE = Symbol.for("react.consumer");
	var REACT_CONTEXT_TYPE = Symbol.for("react.context");
	var REACT_FORWARD_REF_TYPE = Symbol.for("react.forward_ref");
	var REACT_SUSPENSE_TYPE = Symbol.for("react.suspense");
	var REACT_MEMO_TYPE = Symbol.for("react.memo");
	var REACT_LAZY_TYPE = Symbol.for("react.lazy");
	var REACT_ACTIVITY_TYPE = Symbol.for("react.activity");
	var MAYBE_ITERATOR_SYMBOL = Symbol.iterator;
	function getIteratorFn(maybeIterable) {
		if (null === maybeIterable || "object" !== typeof maybeIterable) return null;
		maybeIterable = MAYBE_ITERATOR_SYMBOL && maybeIterable[MAYBE_ITERATOR_SYMBOL] || maybeIterable["@@iterator"];
		return "function" === typeof maybeIterable ? maybeIterable : null;
	}
	var ReactNoopUpdateQueue = {
		isMounted: function() {
			return !1;
		},
		enqueueForceUpdate: function() {},
		enqueueReplaceState: function() {},
		enqueueSetState: function() {}
	};
	var assign = Object.assign;
	var emptyObject = {};
	function Component(props, context, updater) {
		this.props = props;
		this.context = context;
		this.refs = emptyObject;
		this.updater = updater || ReactNoopUpdateQueue;
	}
	Component.prototype.isReactComponent = {};
	Component.prototype.setState = function(partialState, callback) {
		if ("object" !== typeof partialState && "function" !== typeof partialState && null != partialState) throw Error("takes an object of state variables to update or a function which returns an object of state variables.");
		this.updater.enqueueSetState(this, partialState, callback, "setState");
	};
	Component.prototype.forceUpdate = function(callback) {
		this.updater.enqueueForceUpdate(this, callback, "forceUpdate");
	};
	function ComponentDummy() {}
	ComponentDummy.prototype = Component.prototype;
	function PureComponent(props, context, updater) {
		this.props = props;
		this.context = context;
		this.refs = emptyObject;
		this.updater = updater || ReactNoopUpdateQueue;
	}
	var pureComponentPrototype = PureComponent.prototype = new ComponentDummy();
	pureComponentPrototype.constructor = PureComponent;
	assign(pureComponentPrototype, Component.prototype);
	pureComponentPrototype.isPureReactComponent = !0;
	var isArrayImpl = Array.isArray;
	function noop() {}
	var ReactSharedInternals = {
		H: null,
		A: null,
		T: null,
		S: null
	};
	var hasOwnProperty = Object.prototype.hasOwnProperty;
	function ReactElement(type, key, props) {
		var refProp = props.ref;
		return {
			$$typeof: REACT_ELEMENT_TYPE,
			type,
			key,
			ref: void 0 !== refProp ? refProp : null,
			props
		};
	}
	function cloneAndReplaceKey(oldElement, newKey) {
		return ReactElement(oldElement.type, newKey, oldElement.props);
	}
	function isValidElement(object) {
		return "object" === typeof object && null !== object && object.$$typeof === REACT_ELEMENT_TYPE;
	}
	function escape(key) {
		var escaperLookup = {
			"=": "=0",
			":": "=2"
		};
		return "$" + key.replace(/[=:]/g, function(match) {
			return escaperLookup[match];
		});
	}
	var userProvidedKeyEscapeRegex = /\/+/g;
	function getElementKey(element, index) {
		return "object" === typeof element && null !== element && null != element.key ? escape("" + element.key) : index.toString(36);
	}
	function resolveThenable(thenable) {
		switch (thenable.status) {
			case "fulfilled": return thenable.value;
			case "rejected": throw thenable.reason;
			default: switch ("string" === typeof thenable.status ? thenable.then(noop, noop) : (thenable.status = "pending", thenable.then(function(fulfilledValue) {
				"pending" === thenable.status && (thenable.status = "fulfilled", thenable.value = fulfilledValue);
			}, function(error) {
				"pending" === thenable.status && (thenable.status = "rejected", thenable.reason = error);
			})), thenable.status) {
				case "fulfilled": return thenable.value;
				case "rejected": throw thenable.reason;
			}
		}
		throw thenable;
	}
	function mapIntoArray(children, array, escapedPrefix, nameSoFar, callback) {
		var type = typeof children;
		if ("undefined" === type || "boolean" === type) children = null;
		var invokeCallback = !1;
		if (null === children) invokeCallback = !0;
		else switch (type) {
			case "bigint":
			case "string":
			case "number":
				invokeCallback = !0;
				break;
			case "object": switch (children.$$typeof) {
				case REACT_ELEMENT_TYPE:
				case REACT_PORTAL_TYPE:
					invokeCallback = !0;
					break;
				case REACT_LAZY_TYPE: return invokeCallback = children._init, mapIntoArray(invokeCallback(children._payload), array, escapedPrefix, nameSoFar, callback);
			}
		}
		if (invokeCallback) return callback = callback(children), invokeCallback = "" === nameSoFar ? "." + getElementKey(children, 0) : nameSoFar, isArrayImpl(callback) ? (escapedPrefix = "", null != invokeCallback && (escapedPrefix = invokeCallback.replace(userProvidedKeyEscapeRegex, "$&/") + "/"), mapIntoArray(callback, array, escapedPrefix, "", function(c) {
			return c;
		})) : null != callback && (isValidElement(callback) && (callback = cloneAndReplaceKey(callback, escapedPrefix + (null == callback.key || children && children.key === callback.key ? "" : ("" + callback.key).replace(userProvidedKeyEscapeRegex, "$&/") + "/") + invokeCallback)), array.push(callback)), 1;
		invokeCallback = 0;
		var nextNamePrefix = "" === nameSoFar ? "." : nameSoFar + ":";
		if (isArrayImpl(children)) for (var i = 0; i < children.length; i++) nameSoFar = children[i], type = nextNamePrefix + getElementKey(nameSoFar, i), invokeCallback += mapIntoArray(nameSoFar, array, escapedPrefix, type, callback);
		else if (i = getIteratorFn(children), "function" === typeof i) for (children = i.call(children), i = 0; !(nameSoFar = children.next()).done;) nameSoFar = nameSoFar.value, type = nextNamePrefix + getElementKey(nameSoFar, i++), invokeCallback += mapIntoArray(nameSoFar, array, escapedPrefix, type, callback);
		else if ("object" === type) {
			if ("function" === typeof children.then) return mapIntoArray(resolveThenable(children), array, escapedPrefix, nameSoFar, callback);
			array = String(children);
			throw Error("Objects are not valid as a React child (found: " + ("[object Object]" === array ? "object with keys {" + Object.keys(children).join(", ") + "}" : array) + "). If you meant to render a collection of children, use an array instead.");
		}
		return invokeCallback;
	}
	function mapChildren(children, func, context) {
		if (null == children) return children;
		var result = [], count = 0;
		mapIntoArray(children, result, "", "", function(child) {
			return func.call(context, child, count++);
		});
		return result;
	}
	function lazyInitializer(payload) {
		if (-1 === payload._status) {
			var ctor = payload._result;
			ctor = ctor();
			ctor.then(function(moduleObject) {
				if (0 === payload._status || -1 === payload._status) payload._status = 1, payload._result = moduleObject;
			}, function(error) {
				if (0 === payload._status || -1 === payload._status) payload._status = 2, payload._result = error;
			});
			-1 === payload._status && (payload._status = 0, payload._result = ctor);
		}
		if (1 === payload._status) return payload._result.default;
		throw payload._result;
	}
	var reportGlobalError = "function" === typeof reportError ? reportError : function(error) {
		if ("object" === typeof window && "function" === typeof window.ErrorEvent) {
			var event = new window.ErrorEvent("error", {
				bubbles: !0,
				cancelable: !0,
				message: "object" === typeof error && null !== error && "string" === typeof error.message ? String(error.message) : String(error),
				error
			});
			if (!window.dispatchEvent(event)) return;
		} else if ("object" === typeof process && "function" === typeof process.emit) {
			process.emit("uncaughtException", error);
			return;
		}
		console.error(error);
	};
	var Children = {
		map: mapChildren,
		forEach: function(children, forEachFunc, forEachContext) {
			mapChildren(children, function() {
				forEachFunc.apply(this, arguments);
			}, forEachContext);
		},
		count: function(children) {
			var n = 0;
			mapChildren(children, function() {
				n++;
			});
			return n;
		},
		toArray: function(children) {
			return mapChildren(children, function(child) {
				return child;
			}) || [];
		},
		only: function(children) {
			if (!isValidElement(children)) throw Error("React.Children.only expected to receive a single React element child.");
			return children;
		}
	};
	exports.Activity = REACT_ACTIVITY_TYPE;
	exports.Children = Children;
	exports.Component = Component;
	exports.Fragment = REACT_FRAGMENT_TYPE;
	exports.Profiler = REACT_PROFILER_TYPE;
	exports.PureComponent = PureComponent;
	exports.StrictMode = REACT_STRICT_MODE_TYPE;
	exports.Suspense = REACT_SUSPENSE_TYPE;
	exports.__CLIENT_INTERNALS_DO_NOT_USE_OR_WARN_USERS_THEY_CANNOT_UPGRADE = ReactSharedInternals;
	exports.__COMPILER_RUNTIME = {
		__proto__: null,
		c: function(size) {
			return ReactSharedInternals.H.useMemoCache(size);
		}
	};
	exports.cache = function(fn) {
		return function() {
			return fn.apply(null, arguments);
		};
	};
	exports.cacheSignal = function() {
		return null;
	};
	exports.cloneElement = function(element, config, children) {
		if (null === element || void 0 === element) throw Error("The argument must be a React element, but you passed " + element + ".");
		var props = assign({}, element.props), key = element.key;
		if (null != config) for (propName in void 0 !== config.key && (key = "" + config.key), config) !hasOwnProperty.call(config, propName) || "key" === propName || "__self" === propName || "__source" === propName || "ref" === propName && void 0 === config.ref || (props[propName] = config[propName]);
		var propName = arguments.length - 2;
		if (1 === propName) props.children = children;
		else if (1 < propName) {
			for (var childArray = Array(propName), i = 0; i < propName; i++) childArray[i] = arguments[i + 2];
			props.children = childArray;
		}
		return ReactElement(element.type, key, props);
	};
	exports.createContext = function(defaultValue) {
		defaultValue = {
			$$typeof: REACT_CONTEXT_TYPE,
			_currentValue: defaultValue,
			_currentValue2: defaultValue,
			_threadCount: 0,
			Provider: null,
			Consumer: null
		};
		defaultValue.Provider = defaultValue;
		defaultValue.Consumer = {
			$$typeof: REACT_CONSUMER_TYPE,
			_context: defaultValue
		};
		return defaultValue;
	};
	exports.createElement = function(type, config, children) {
		var propName, props = {}, key = null;
		if (null != config) for (propName in void 0 !== config.key && (key = "" + config.key), config) hasOwnProperty.call(config, propName) && "key" !== propName && "__self" !== propName && "__source" !== propName && (props[propName] = config[propName]);
		var childrenLength = arguments.length - 2;
		if (1 === childrenLength) props.children = children;
		else if (1 < childrenLength) {
			for (var childArray = Array(childrenLength), i = 0; i < childrenLength; i++) childArray[i] = arguments[i + 2];
			props.children = childArray;
		}
		if (type && type.defaultProps) for (propName in childrenLength = type.defaultProps, childrenLength) void 0 === props[propName] && (props[propName] = childrenLength[propName]);
		return ReactElement(type, key, props);
	};
	exports.createRef = function() {
		return { current: null };
	};
	exports.forwardRef = function(render) {
		return {
			$$typeof: REACT_FORWARD_REF_TYPE,
			render
		};
	};
	exports.isValidElement = isValidElement;
	exports.lazy = function(ctor) {
		return {
			$$typeof: REACT_LAZY_TYPE,
			_payload: {
				_status: -1,
				_result: ctor
			},
			_init: lazyInitializer
		};
	};
	exports.memo = function(type, compare) {
		return {
			$$typeof: REACT_MEMO_TYPE,
			type,
			compare: void 0 === compare ? null : compare
		};
	};
	exports.startTransition = function(scope) {
		var prevTransition = ReactSharedInternals.T, currentTransition = {};
		ReactSharedInternals.T = currentTransition;
		try {
			var returnValue = scope(), onStartTransitionFinish = ReactSharedInternals.S;
			null !== onStartTransitionFinish && onStartTransitionFinish(currentTransition, returnValue);
			"object" === typeof returnValue && null !== returnValue && "function" === typeof returnValue.then && returnValue.then(noop, reportGlobalError);
		} catch (error) {
			reportGlobalError(error);
		} finally {
			null !== prevTransition && null !== currentTransition.types && (prevTransition.types = currentTransition.types), ReactSharedInternals.T = prevTransition;
		}
	};
	exports.unstable_useCacheRefresh = function() {
		return ReactSharedInternals.H.useCacheRefresh();
	};
	exports.use = function(usable) {
		return ReactSharedInternals.H.use(usable);
	};
	exports.useActionState = function(action, initialState, permalink) {
		return ReactSharedInternals.H.useActionState(action, initialState, permalink);
	};
	exports.useCallback = function(callback, deps) {
		return ReactSharedInternals.H.useCallback(callback, deps);
	};
	exports.useContext = function(Context) {
		return ReactSharedInternals.H.useContext(Context);
	};
	exports.useDebugValue = function() {};
	exports.useDeferredValue = function(value, initialValue) {
		return ReactSharedInternals.H.useDeferredValue(value, initialValue);
	};
	exports.useEffect = function(create, deps) {
		return ReactSharedInternals.H.useEffect(create, deps);
	};
	exports.useEffectEvent = function(callback) {
		return ReactSharedInternals.H.useEffectEvent(callback);
	};
	exports.useId = function() {
		return ReactSharedInternals.H.useId();
	};
	exports.useImperativeHandle = function(ref, create, deps) {
		return ReactSharedInternals.H.useImperativeHandle(ref, create, deps);
	};
	exports.useInsertionEffect = function(create, deps) {
		return ReactSharedInternals.H.useInsertionEffect(create, deps);
	};
	exports.useLayoutEffect = function(create, deps) {
		return ReactSharedInternals.H.useLayoutEffect(create, deps);
	};
	exports.useMemo = function(create, deps) {
		return ReactSharedInternals.H.useMemo(create, deps);
	};
	exports.useOptimistic = function(passthrough, reducer) {
		return ReactSharedInternals.H.useOptimistic(passthrough, reducer);
	};
	exports.useReducer = function(reducer, initialArg, init) {
		return ReactSharedInternals.H.useReducer(reducer, initialArg, init);
	};
	exports.useRef = function(initialValue) {
		return ReactSharedInternals.H.useRef(initialValue);
	};
	exports.useState = function(initialState) {
		return ReactSharedInternals.H.useState(initialState);
	};
	exports.useSyncExternalStore = function(subscribe, getSnapshot, getServerSnapshot) {
		return ReactSharedInternals.H.useSyncExternalStore(subscribe, getSnapshot, getServerSnapshot);
	};
	exports.useTransition = function() {
		return ReactSharedInternals.H.useTransition();
	};
	exports.version = "19.2.7";
}));
//#endregion
//#region ../../node_modules/.pnpm/react@19.2.7/node_modules/react/index.js
var require_react = /* @__PURE__ */ __commonJSMin(((exports, module) => {
	module.exports = require_react_production();
}));
//#endregion
//#region ../../node_modules/.pnpm/react-router@8.3.0_react-dom@19.2.7_react@19.2.7__react@19.2.7/node_modules/react-router/dist/production/lib/router/utils.js
/**
* react-router v8.3.0
*
* Copyright (c) Remix Software Inc.
*
* This source code is licensed under the MIT license found in the
* LICENSE.md file in the root directory of this source tree.
*
* @license MIT
*/
var import_react = /* @__PURE__ */ __toESM(require_react(), 1);
/**
* Creates a type-safe {@link RouterContext} object that can be used to
* store and retrieve arbitrary values in [`action`](../../start/framework/route-module#action)s,
* [`loader`](../../start/framework/route-module#loader)s, and [middleware](../../how-to/middleware).
* Similar to React's [`createContext`](https://react.dev/reference/react/createContext),
* but specifically designed for React Router's request/response lifecycle.
*
* If a `defaultValue` is provided, it will be returned from `context.get()`
* when no value has been set for the context. Otherwise, reading this context
* when no value has been set will throw an error.
*
* ```tsx filename=app/context.ts
* import { createContext } from "react-router";
*
* // Create a context for user data
* export const userContext =
*   createContext<User | null>(null);
* ```
*
* ```tsx filename=app/middleware/auth.ts
* import { getUserFromSession } from "~/auth.server";
* import { userContext } from "~/context";
*
* export const authMiddleware = async ({
*   context,
*   request,
* }) => {
*   const user = await getUserFromSession(request);
*   context.set(userContext, user);
* };
* ```
*
* ```tsx filename=app/routes/profile.tsx
* import { userContext } from "~/context";
*
* export async function loader({
*   context,
* }: Route.LoaderArgs) {
*   const user = context.get(userContext);
*
*   if (!user) {
*     throw new Response("Unauthorized", { status: 401 });
*   }
*
*   return { user };
* }
* ```
*
* @public
* @category Utils
* @mode framework
* @mode data
* @param defaultValue An optional default value for the context. This value
* will be returned if no value has been set for this context.
* @returns A {@link RouterContext} object that can be used with
* `context.get()` and `context.set()` in [`action`](../../start/framework/route-module#action)s,
* [`loader`](../../start/framework/route-module#loader)s, and [middleware](../../how-to/middleware).
*/
function createContext(defaultValue) {
	return { defaultValue };
}
/**
* Provides methods for writing/reading values in application context in a
* type-safe way. Primarily for usage with [middleware](../../how-to/middleware).
*
* @example
* import {
*   createContext,
*   RouterContextProvider
* } from "react-router";
*
* const userContext = createContext<User | null>(null);
* const contextProvider = new RouterContextProvider();
* contextProvider.set(userContext, getUser());
* //                               ^ Type-safe
* const user = contextProvider.get(userContext);
* //    ^ User
*
* @public
* @category Utils
* @mode framework
* @mode data
*/
var RouterContextProvider = class {
	#map = /* @__PURE__ */ new Map();
	/**
	* Create a new `RouterContextProvider` instance
	* @param init An optional initial context map to populate the provider with
	*/
	constructor(init) {
		if (init) for (let [context, value] of init) this.set(context, value);
	}
	/**
	* Access a value from the context. If no value has been set for the context,
	* it will return the context's `defaultValue` if provided, or throw an error
	* if no `defaultValue` was set.
	* @param context The context to get the value for
	* @returns The value for the context, or the context's `defaultValue` if no
	* value was set
	*/
	get(context) {
		if (this.#map.has(context)) return this.#map.get(context);
		if (context.defaultValue !== void 0) return context.defaultValue;
		throw new Error("No value found for context");
	}
	/**
	* Set a value for the context. If the context already has a value set, this
	* will overwrite it.
	*
	* @param context The context to set the value for
	* @param value The value to set for the context
	* @returns {void}
	*/
	set(context, value) {
		this.#map.set(context, value);
	}
};
var unsupportedLazyRouteObjectKeys = /* @__PURE__ */ new Set([
	"lazy",
	"caseSensitive",
	"path",
	"id",
	"index",
	"children"
]);
function isUnsupportedLazyRouteObjectKey(key) {
	return unsupportedLazyRouteObjectKeys.has(key);
}
var unsupportedLazyRouteFunctionKeys = /* @__PURE__ */ new Set([
	"lazy",
	"caseSensitive",
	"path",
	"id",
	"index",
	"middleware",
	"children"
]);
function isUnsupportedLazyRouteFunctionKey(key) {
	return unsupportedLazyRouteFunctionKeys.has(key);
}
function isIndexRoute(route) {
	return route.index === true;
}
function defaultMapRouteProperties(route) {
	let updates = {};
	if (route.Component) Object.assign(updates, {
		element: import_react.createElement(route.Component),
		Component: void 0
	});
	if (route.HydrateFallback) Object.assign(updates, {
		hydrateFallbackElement: import_react.createElement(route.HydrateFallback),
		HydrateFallback: void 0
	});
	if (route.ErrorBoundary) Object.assign(updates, {
		errorElement: import_react.createElement(route.ErrorBoundary),
		ErrorBoundary: void 0
	});
	return updates;
}
function convertRoutesToDataRoutes(routes, mapRouteProperties = defaultMapRouteProperties, parentPath = [], manifest = {}, allowInPlaceMutations = false) {
	return routes.map((route, index) => {
		let treePath = [...parentPath, String(index)];
		let id = typeof route.id === "string" ? route.id : treePath.join("-");
		invariant(route.index !== true || !route.children, `Cannot specify children on an index route`);
		invariant(allowInPlaceMutations || !manifest[id], `Found a route id collision on id "${id}".  Route id's must be globally unique within Data Router usages`);
		if (isIndexRoute(route)) {
			let indexRoute = {
				...route,
				id
			};
			manifest[id] = mergeRouteUpdates(indexRoute, mapRouteProperties(indexRoute));
			return indexRoute;
		} else {
			let pathOrLayoutRoute = {
				...route,
				id,
				children: void 0
			};
			manifest[id] = mergeRouteUpdates(pathOrLayoutRoute, mapRouteProperties(pathOrLayoutRoute));
			if (route.children) pathOrLayoutRoute.children = convertRoutesToDataRoutes(route.children, mapRouteProperties, treePath, manifest, allowInPlaceMutations);
			return pathOrLayoutRoute;
		}
	});
}
function mergeRouteUpdates(route, updates) {
	return Object.assign(route, {
		...updates,
		...typeof updates.lazy === "object" && updates.lazy != null ? { lazy: {
			...route.lazy,
			...updates.lazy
		} } : {}
	});
}
/**
* Matches the given routes to a location and returns the match data.
*
* @example
* import { matchRoutes } from "react-router";
*
* let routes = [{
*   path: "/",
*   Component: Root,
*   children: [{
*     path: "dashboard",
*     Component: Dashboard,
*   }]
* }];
*
* matchRoutes(routes, "/dashboard"); // [rootMatch, dashboardMatch]
*
* @public
* @category Utils
* @param routes The array of route objects to match against.
* @param locationArg The location to match against, either a string path or a
* partial {@link Location} object
* @param basename Optional base path to strip from the location before matching.
* Defaults to `/`.
* @returns An array of matched routes, or `null` if no matches were found.
*/
function matchRoutes(routes, locationArg, basename = "/") {
	return matchRoutesImpl(routes, locationArg, basename, false);
}
function matchRoutesImpl(routes, locationArg, basename, allowPartial, precomputedBranches) {
	let pathname = stripBasename((typeof locationArg === "string" ? parsePath(locationArg) : locationArg).pathname || "/", basename);
	if (pathname == null) return null;
	let branches = precomputedBranches ?? flattenAndRankRoutes(routes);
	let matches = null;
	let decoded = decodePath(pathname);
	for (let i = 0; matches == null && i < branches.length; ++i) matches = matchRouteBranch(branches[i], decoded, allowPartial);
	return matches;
}
function convertRouteMatchToUiMatch(match, loaderData) {
	let { route, pathname, params } = match;
	return {
		id: route.id,
		pathname,
		params,
		loaderData: loaderData[route.id],
		handle: route.handle
	};
}
function flattenAndRankRoutes(routes) {
	let branches = flattenRoutes(routes);
	rankRouteBranches(branches);
	return branches;
}
function flattenRoutes(routes, branches = [], parentsMeta = [], parentPath = "", _hasParentOptionalSegments = false) {
	let flattenRoute = (route, index, hasParentOptionalSegments = _hasParentOptionalSegments, relativePath) => {
		let meta = {
			relativePath: relativePath === void 0 ? route.path || "" : relativePath,
			caseSensitive: route.caseSensitive === true,
			childrenIndex: index,
			route
		};
		if (meta.relativePath.startsWith("/")) {
			if (!meta.relativePath.startsWith(parentPath) && hasParentOptionalSegments) return;
			invariant(meta.relativePath.startsWith(parentPath), `Absolute route path "${meta.relativePath}" nested under path "${parentPath}" is not valid. An absolute child route path must start with the combined path of all its parent routes.`);
			meta.relativePath = meta.relativePath.slice(parentPath.length);
		}
		let path = joinPaths([parentPath, meta.relativePath]);
		let routesMeta = parentsMeta.concat(meta);
		if (route.children && route.children.length > 0) {
			invariant(route.index !== true, `Index routes must not have child routes. Please remove all child routes from route path "${path}".`);
			flattenRoutes(route.children, branches, routesMeta, path, hasParentOptionalSegments);
		}
		if (route.path == null && !route.index) return;
		branches.push({
			path,
			score: computeScore(path, route.index),
			routesMeta: routesMeta.map((meta, i) => {
				let [matcher, params] = compilePath(meta.relativePath, meta.caseSensitive, i === routesMeta.length - 1);
				return {
					...meta,
					matcher,
					compiledParams: params
				};
			})
		});
	};
	routes.forEach((route, index) => {
		if (route.path === "" || !route.path?.includes("?")) flattenRoute(route, index);
		else for (let exploded of explodeOptionalSegments(route.path)) flattenRoute(route, index, true, exploded);
	});
	return branches;
}
function explodeOptionalSegments(path) {
	let segments = path.split("/");
	if (segments.length === 0) return [];
	let [first, ...rest] = segments;
	let isOptional = first.endsWith("?");
	let required = first.replace(/\?$/, "");
	if (rest.length === 0) return isOptional ? [required, ""] : [required];
	let restExploded = explodeOptionalSegments(rest.join("/"));
	let result = [];
	result.push(...restExploded.map((subpath) => subpath === "" ? required : [required, subpath].join("/")));
	if (isOptional) result.push(...restExploded);
	return result.map((exploded) => path.startsWith("/") && exploded === "" ? "/" : exploded);
}
function rankRouteBranches(branches) {
	branches.sort((a, b) => a.score !== b.score ? b.score - a.score : compareIndexes(a.routesMeta.map((meta) => meta.childrenIndex), b.routesMeta.map((meta) => meta.childrenIndex)));
}
var paramRe = /^:[\w-]+$/;
var partialParamRe = /^:[\w-]+/;
var partialDynamicSegmentValue = 3.5;
var dynamicSegmentValue = 3;
var indexRouteValue = 2;
var emptySegmentValue = 1;
var staticSegmentValue = 10;
var splatPenalty = -2;
var isSplat = (s) => s === "*";
function computeScore(path, index) {
	let segments = path.split("/");
	let initialScore = segments.length;
	if (segments.some(isSplat)) initialScore += splatPenalty;
	if (index) initialScore += indexRouteValue;
	return segments.filter((s) => !isSplat(s)).reduce((score, segment) => score + (paramRe.test(segment) ? dynamicSegmentValue : partialParamRe.test(segment) ? partialDynamicSegmentValue : segment === "" ? emptySegmentValue : staticSegmentValue), initialScore);
}
function compareIndexes(a, b) {
	return a.length === b.length && a.slice(0, -1).every((n, i) => n === b[i]) ? a[a.length - 1] - b[b.length - 1] : 0;
}
function matchRouteBranch(branch, pathname, allowPartial = false) {
	let { routesMeta } = branch;
	let matchedParams = {};
	let matchedPathname = "/";
	let matches = [];
	for (let i = 0; i < routesMeta.length; ++i) {
		let meta = routesMeta[i];
		let end = i === routesMeta.length - 1;
		let remainingPathname = matchedPathname === "/" ? pathname : pathname.slice(matchedPathname.length) || "/";
		let pattern = {
			path: meta.relativePath,
			caseSensitive: meta.caseSensitive,
			end
		};
		let match = meta.matcher && meta.compiledParams ? matchPathImpl(pattern, remainingPathname, meta.matcher, meta.compiledParams) : matchPath(pattern, remainingPathname);
		let route = meta.route;
		if (!match && end && allowPartial && !routesMeta[routesMeta.length - 1].route.index) match = matchPath({
			path: meta.relativePath,
			caseSensitive: meta.caseSensitive,
			end: false
		}, remainingPathname);
		if (!match) return null;
		Object.assign(matchedParams, match.params);
		matches.push({
			params: matchedParams,
			pathname: joinPaths([matchedPathname, match.pathname]),
			pathnameBase: normalizePathname(joinPaths([matchedPathname, match.pathnameBase])),
			route
		});
		if (match.pathnameBase !== "/") matchedPathname = joinPaths([matchedPathname, match.pathnameBase]);
	}
	return matches;
}
/**
* Characters that `encodeURIComponent` escapes but that are valid literally in
* a URL path segment. Per RFC 3986 §3.3, a path segment is made of `pchar`:
*
* ```
* pchar = unreserved / pct-encoded / sub-delims / ":" / "@"
* sub-delims = "!" / "$" / "&" / "'" / "(" / ")" / "*" / "+" / "," / ";" / "="
* ```
*
* `encodeURIComponent` targets query-string values, where `$ & + , ; = : @`
* are delimiters and must be escaped — but in a path segment they carry no
* special meaning, and browsers keep them literal in `location.pathname`.
* (`! ' ( ) *` and the unreserved set are already left alone by
* `encodeURIComponent`, so they need no restoring.)
*/
var PATH_PARAM_OVERESCAPED = {
	"%24": "$",
	"%26": "&",
	"%2B": "+",
	"%2C": ",",
	"%3A": ":",
	"%3B": ";",
	"%3D": "=",
	"%40": "@"
};
/**
* Encodes a param value for interpolation into a single URL path segment.
*
* Escapes characters that would break the path (`/ ? # %`, whitespace,
* non-ASCII, …) while leaving characters that RFC 3986 permits literally in a
* path segment untouched. Escaping those would needlessly rewrite URLs — e.g.
*  a semver build param `1.0.0+1` would become `1.0.0%2B1` even though browsers
* display and match the `+` literally in `location.pathname`.
*
* See [RFC 3986 §3.3](https://datatracker.ietf.org/doc/html/rfc3986#section-3.3))
*
* @param value The param value to encode.
* @returns The encoded value, safe for use as a single path segment.
*/
function encodePathParam(value) {
	return encodeURIComponent(value).replace(/%(?:24|26|2B|2C|3A|3B|3D|40)/g, (match) => PATH_PARAM_OVERESCAPED[match]);
}
/**
* Performs pattern matching on a URL pathname and returns information about
* the match.
*
* @public
* @category Utils
* @param pattern The pattern to match against the URL pathname. This can be a
* string or a {@link PathPattern} object. If a string is provided, it will be
* treated as a pattern with `caseSensitive` set to `false` and `end` set to
* `true`.
* @param pathname The URL pathname to match against the pattern.
* @returns A path match object if the pattern matches the pathname,
* or `null` if it does not match.
*/
function matchPath(pattern, pathname) {
	if (typeof pattern === "string") pattern = {
		path: pattern,
		caseSensitive: false,
		end: true
	};
	let [matcher, compiledParams] = compilePath(pattern.path, pattern.caseSensitive, pattern.end);
	return matchPathImpl(pattern, pathname, matcher, compiledParams);
}
function matchPathImpl(pattern, pathname, matcher, compiledParams) {
	let match = pathname.match(matcher);
	if (!match) return null;
	let matchedPathname = match[0];
	let pathnameBase = matchedPathname.replace(/(.)\/+$/, "$1");
	let captureGroups = match.slice(1);
	return {
		params: compiledParams.reduce((memo, { paramName, isOptional }, index) => {
			if (paramName === "*") {
				let splatValue = captureGroups[index] || "";
				pathnameBase = matchedPathname.slice(0, matchedPathname.length - splatValue.length).replace(/(.)\/+$/, "$1");
			}
			const value = captureGroups[index];
			if (isOptional && !value) memo[paramName] = void 0;
			else memo[paramName] = (value || "").replace(/%2F/g, "/");
			return memo;
		}, {}),
		pathname: matchedPathname,
		pathnameBase,
		pattern
	};
}
function compilePath(path, caseSensitive = false, end = true) {
	warning(path === "*" || !path.endsWith("*") || path.endsWith("/*"), `Route path "${path}" will be treated as if it were "${path.replace(/\*$/, "/*")}" because the \`*\` character must always follow a \`/\` in the pattern. To get rid of this warning, please change the route path to "${path.replace(/\*$/, "/*")}".`);
	let params = [];
	let regexpSource = "^" + path.replace(/\/*\*?$/, "").replace(/^\/*/, "/").replace(/[\\.*+^${}|()[\]]/g, "\\$&").replace(/\/:([\w-]+)(\?)?/g, (match, paramName, isOptional, index, str) => {
		params.push({
			paramName,
			isOptional: isOptional != null
		});
		if (isOptional) {
			let nextChar = str.charAt(index + match.length);
			if (nextChar && nextChar !== "/") return "/([^\\/]*)";
			return "(?:/([^\\/]*))?";
		}
		return "/([^\\/]+)";
	}).replace(/\/([\w-]+)\?(?=\/|$|\()/g, "(?:/$1)?");
	if (path.endsWith("*")) {
		params.push({ paramName: "*" });
		regexpSource += path === "*" || path === "/*" ? "(.*)$" : "(?:\\/(.+)|\\/*)$";
	} else if (end) regexpSource += "\\/*$";
	else if (path !== "" && path !== "/") regexpSource += "(?:(?=\\/|$))";
	return [new RegExp(regexpSource, caseSensitive ? void 0 : "i"), params];
}
function decodePath(value) {
	try {
		return value.split("/").map((v) => decodeURIComponent(v).replace(/\//g, "%2F")).join("/");
	} catch (error) {
		warning(false, `The URL path "${value}" could not be decoded because it is a malformed URL segment. This is probably due to a bad percent encoding (${error}).`);
		return value;
	}
}
function stripBasename(pathname, basename) {
	if (basename === "/") return pathname;
	if (!pathname.toLowerCase().startsWith(basename.toLowerCase())) return null;
	let startIndex = basename.endsWith("/") ? basename.length - 1 : basename.length;
	let nextChar = pathname.charAt(startIndex);
	if (nextChar && nextChar !== "/") return null;
	return pathname.slice(startIndex) || "/";
}
function prependBasename({ basename, pathname }) {
	return pathname === "/" ? basename : joinPaths([basename, pathname]);
}
var isAbsoluteUrl = (url) => ABSOLUTE_URL_REGEX.test(url);
/**
* Returns a resolved {@link Path} object relative to the given pathname.
*
* @public
* @category Utils
* @param to The path to resolve, either a string or a partial {@link Path}
* object.
* @param fromPathname The pathname to resolve the path from. Defaults to `/`.
* @returns A {@link Path} object with the resolved pathname, search, and hash.
*/
function resolvePath(to, fromPathname = "/") {
	let { pathname: toPathname, search = "", hash = "" } = typeof to === "string" ? parsePath(to) : to;
	let pathname;
	if (toPathname) {
		toPathname = removeDoubleSlashes(toPathname);
		if (toPathname.startsWith("/")) pathname = resolvePathname(toPathname.substring(1), "/");
		else pathname = resolvePathname(toPathname, fromPathname);
	} else pathname = fromPathname;
	return {
		pathname,
		search: normalizeSearch(search),
		hash: normalizeHash(hash)
	};
}
function resolvePathname(relativePath, fromPathname) {
	let segments = removeTrailingSlash(fromPathname).split("/");
	relativePath.split("/").forEach((segment) => {
		if (segment === "..") {
			if (segments.length > 1) segments.pop();
		} else if (segment !== ".") segments.push(segment);
	});
	return segments.length > 1 ? segments.join("/") : "/";
}
function getInvalidPathError(char, field, dest, path) {
	return `Cannot include a '${char}' character in a manually specified \`to.${field}\` field [${JSON.stringify(path)}].  Please separate it out to the \`to.${dest}\` field. Alternatively you may provide the full path as a string in <Link to="..."> and the router will parse it for you.`;
}
function getPathContributingMatches(matches) {
	return matches.filter((match, index) => index === 0 || match.route.path && match.route.path.length > 0);
}
function getResolveToMatches(matches) {
	let pathMatches = getPathContributingMatches(matches);
	return pathMatches.map((match, idx) => idx === pathMatches.length - 1 ? match.pathname : match.pathnameBase);
}
function resolveTo(toArg, routePathnames, locationPathname, isPathRelative = false) {
	let to;
	if (typeof toArg === "string") to = parsePath(toArg);
	else {
		to = { ...toArg };
		invariant(!to.pathname || !to.pathname.includes("?"), getInvalidPathError("?", "pathname", "search", to));
		invariant(!to.pathname || !to.pathname.includes("#"), getInvalidPathError("#", "pathname", "hash", to));
		invariant(!to.search || !to.search.includes("#"), getInvalidPathError("#", "search", "hash", to));
	}
	let isEmptyPath = toArg === "" || to.pathname === "";
	let toPathname = isEmptyPath ? "/" : to.pathname;
	let from;
	if (toPathname == null) from = locationPathname;
	else {
		let routePathnameIndex = routePathnames.length - 1;
		if (!isPathRelative && toPathname.startsWith("..")) {
			let toSegments = toPathname.split("/");
			while (toSegments[0] === "..") {
				toSegments.shift();
				routePathnameIndex -= 1;
			}
			to.pathname = toSegments.join("/");
		}
		from = routePathnameIndex >= 0 ? routePathnames[routePathnameIndex] : "/";
	}
	let path = resolvePath(to, from);
	let hasExplicitTrailingSlash = toPathname && toPathname !== "/" && toPathname.endsWith("/");
	let hasCurrentTrailingSlash = (isEmptyPath || toPathname === ".") && locationPathname.endsWith("/");
	if (!path.pathname.endsWith("/") && (hasExplicitTrailingSlash || hasCurrentTrailingSlash)) path.pathname += "/";
	return path;
}
var removeDoubleSlashes = (path) => path.replace(/[\\/]{2,}/g, "/");
var joinPaths = (paths) => removeDoubleSlashes(paths.join("/"));
var removeTrailingSlash = (path) => path.replace(/\/+$/, "");
var normalizePathname = (pathname) => removeTrailingSlash(pathname).replace(/^\/*/, "/");
var normalizeSearch = (search) => !search || search === "?" ? "" : search.startsWith("?") ? search : "?" + search;
var normalizeHash = (hash) => !hash || hash === "#" ? "" : hash.startsWith("#") ? hash : "#" + hash;
var DataWithResponseInit = class {
	type = "DataWithResponseInit";
	data;
	init;
	constructor(data, init) {
		this.data = data;
		this.init = init || null;
	}
};
/**
* Create "responses" that contain `headers`/`status` without forcing
* serialization into an actual [`Response`](https://developer.mozilla.org/en-US/docs/Web/API/Response)
*
* @example
* import { data } from "react-router";
*
* export async function action({ request }: Route.ActionArgs) {
*   let formData = await request.formData();
*   let item = await createItem(formData);
*   return data(item, {
*     headers: { "X-Custom-Header": "value" }
*     status: 201,
*   });
* }
*
* @public
* @category Utils
* @mode framework
* @mode data
* @param data The data to be included in the response.
* @param init The status code or a `ResponseInit` object to be included in the
* response.
* @returns A {@link DataWithResponseInit} instance containing the data and
* response init.
*/
function data(data, init) {
	return new DataWithResponseInit(data, typeof init === "number" ? { status: init } : init);
}
/**
* A redirect [`Response`](https://developer.mozilla.org/en-US/docs/Web/API/Response).
* Sets the status code and the [`Location`](https://developer.mozilla.org/en-US/docs/Web/HTTP/Headers/Location)
* header. Defaults to [`302 Found`](https://developer.mozilla.org/en-US/docs/Web/HTTP/Status/302).
*
* This utility accepts absolute URLs and can navigate to external domains, so
* the application should validate any user-supplied inputs to redirects.
*
* @example
* import { redirect } from "react-router";
*
* export async function loader({ request }: Route.LoaderArgs) {
*   if (!isLoggedIn(request))
*     throw redirect("/login");
*   }
*
*   // ...
* }
*
* @public
* @category Utils
* @mode framework
* @mode data
* @param url The URL to redirect to.
* @param init The status code or a `ResponseInit` object to be included in the
* response.
* @returns A [`Response`](https://developer.mozilla.org/en-US/docs/Web/API/Response)
* object with the redirect status and [`Location`](https://developer.mozilla.org/en-US/docs/Web/HTTP/Headers/Location)
* header.
*/
var redirect = (url, init = 302) => {
	let responseInit = init;
	if (typeof responseInit === "number") responseInit = { status: responseInit };
	else if (typeof responseInit.status === "undefined") responseInit.status = 302;
	let headers = new Headers(responseInit.headers);
	headers.set("Location", url);
	return new Response(null, {
		...responseInit,
		headers
	});
};
/**
* A redirect [`Response`](https://developer.mozilla.org/en-US/docs/Web/API/Response)
* that will force a document reload to the new location. Sets the status code
* and the [`Location`](https://developer.mozilla.org/en-US/docs/Web/HTTP/Headers/Location)
* header. Defaults to [`302 Found`](https://developer.mozilla.org/en-US/docs/Web/HTTP/Status/302).
*
* This utility accepts absolute URLs and can navigate to external domains, so
* the application should validate any user-supplied inputs to redirects.
*
* ```tsx filename=routes/logout.tsx
* import { redirectDocument } from "react-router";
*
* import { destroySession } from "../sessions.server";
*
* export async function action({ request }: Route.ActionArgs) {
*   let session = await getSession(request.headers.get("Cookie"));
*   return redirectDocument("/", {
*     headers: { "Set-Cookie": await destroySession(session) }
*   });
* }
* ```
*
* @public
* @category Utils
* @mode framework
* @mode data
* @param url The URL to redirect to.
* @param init The status code or a `ResponseInit` object to be included in the
* response.
* @returns A [`Response`](https://developer.mozilla.org/en-US/docs/Web/API/Response)
* object with the redirect status and [`Location`](https://developer.mozilla.org/en-US/docs/Web/HTTP/Headers/Location)
* header.
*/
var redirectDocument = (url, init) => {
	let response = redirect(url, init);
	response.headers.set("X-Remix-Reload-Document", "true");
	return response;
};
/**
* A redirect [`Response`](https://developer.mozilla.org/en-US/docs/Web/API/Response)
* that will perform a [`history.replaceState`](https://developer.mozilla.org/en-US/docs/Web/API/History/replaceState)
* instead of a [`history.pushState`](https://developer.mozilla.org/en-US/docs/Web/API/History/pushState)
* for client-side navigation redirects. Sets the status code and the [`Location`](https://developer.mozilla.org/en-US/docs/Web/HTTP/Headers/Location)
* header. Defaults to [`302 Found`](https://developer.mozilla.org/en-US/docs/Web/HTTP/Status/302).
*
* @example
* import { replace } from "react-router";
*
* export async function loader() {
*   return replace("/new-location");
* }
*
* @public
* @category Utils
* @mode framework
* @mode data
* @param url The URL to redirect to.
* @param init The status code or a `ResponseInit` object to be included in the
* response.
* @returns A [`Response`](https://developer.mozilla.org/en-US/docs/Web/API/Response)
* object with the redirect status and [`Location`](https://developer.mozilla.org/en-US/docs/Web/HTTP/Headers/Location)
* header.
*/
var replace = (url, init) => {
	let response = redirect(url, init);
	response.headers.set("X-Remix-Replace", "true");
	return response;
};
var SUPPORTED_ERROR_TYPES = [
	"EvalError",
	"RangeError",
	"ReferenceError",
	"SyntaxError",
	"TypeError",
	"URIError"
];
var ErrorResponseImpl = class {
	status;
	statusText;
	data;
	error;
	internal;
	constructor(status, statusText, data, internal = false) {
		this.status = status;
		this.statusText = statusText || "";
		this.internal = internal;
		if (data instanceof Error) {
			this.data = data.toString();
			this.error = data;
		} else this.data = data;
	}
};
/**
* Check if the given error is an {@link ErrorResponse} generated from a 4xx/5xx
* [`Response`](https://developer.mozilla.org/en-US/docs/Web/API/Response)
* thrown from an [`action`](../../start/framework/route-module#action) or
* [`loader`](../../start/framework/route-module#loader) function.
*
* @example
* import { isRouteErrorResponse } from "react-router";
*
* export function ErrorBoundary({ error }: Route.ErrorBoundaryProps) {
*   if (isRouteErrorResponse(error)) {
*     return (
*       <>
*         <p>Error: `${error.status}: ${error.statusText}`</p>
*         <p>{error.data}</p>
*       </>
*     );
*   }
*
*   return (
*     <p>Error: {error instanceof Error ? error.message : "Unknown Error"}</p>
*   );
* }
*
* @public
* @category Utils
* @mode framework
* @mode data
* @param error The error to check.
* @returns `true` if the error is an {@link ErrorResponse}, `false` otherwise.
*/
function isRouteErrorResponse(error) {
	return error != null && typeof error.status === "number" && typeof error.statusText === "string" && typeof error.internal === "boolean" && "data" in error;
}
function getRoutePattern(matches) {
	return joinPaths(matches.map((m) => m.route.path).filter(Boolean)) || "/";
}
function createDataFunctionUrl(request, path) {
	let url = new URL(typeof request === "string" || request instanceof URL ? request : request.url);
	let parsed = typeof path === "string" ? parsePath(path) : path;
	url.pathname = parsed.pathname || "/";
	if (parsed.search) {
		let searchParams = new URLSearchParams(parsed.search);
		let indexValues = searchParams.getAll("index");
		searchParams.delete("index");
		for (let value of indexValues.filter(Boolean)) searchParams.append("index", value);
		let search = searchParams.toString();
		url.search = search ? `?${search}` : "";
	} else url.search = "";
	url.hash = parsed.hash || "";
	return url;
}
var isBrowser = typeof window !== "undefined" && typeof window.document !== "undefined" && typeof window.document.createElement !== "undefined";
function parseToInfo(_to, basename) {
	let to = _to;
	if (typeof to !== "string" || !ABSOLUTE_URL_REGEX.test(to)) return {
		absoluteURL: void 0,
		isExternal: false,
		to
	};
	let absoluteURL = to;
	let isExternal = false;
	if (isBrowser) try {
		let currentUrl = new URL(window.location.href);
		let targetUrl = PROTOCOL_RELATIVE_URL_REGEX.test(to) ? new URL(normalizeProtocolRelativeUrl(to, currentUrl.protocol)) : new URL(to);
		let path = stripBasename(targetUrl.pathname, basename);
		if (targetUrl.origin === currentUrl.origin && path != null) to = path + targetUrl.search + targetUrl.hash;
		else isExternal = true;
	} catch (e) {
		warning(false, `<Link to="${to}"> contains an invalid URL which will probably break when clicked - please update to a valid URL path.`);
	}
	return {
		absoluteURL,
		isExternal,
		to
	};
}
//#endregion
//#region ../../node_modules/.pnpm/react-router@8.3.0_react-dom@19.2.7_react@19.2.7__react@19.2.7/node_modules/react-router/dist/production/lib/router/instrumentation.js
/**
* react-router v8.3.0
*
* Copyright (c) Remix Software Inc.
*
* This source code is licensed under the MIT license found in the
* LICENSE.md file in the root directory of this source tree.
*
* @license MIT
*/
var UninstrumentedSymbol = Symbol("Uninstrumented");
var instrumentationResultMetaContext = createContext();
function getRouteInstrumentationUpdates(fns, route) {
	let aggregated = {
		lazy: [],
		"lazy.loader": [],
		"lazy.action": [],
		"lazy.middleware": [],
		middleware: [],
		loader: [],
		action: []
	};
	fns.forEach((fn) => fn({
		id: route.id,
		index: route.index,
		path: route.path,
		instrument(i) {
			if (i.lazy != null) aggregated.lazy.push(i.lazy);
			if (i["lazy.loader"] != null) aggregated["lazy.loader"].push(i["lazy.loader"]);
			if (i["lazy.action"] != null) aggregated["lazy.action"].push(i["lazy.action"]);
			if (i["lazy.middleware"] != null) aggregated["lazy.middleware"].push(i["lazy.middleware"]);
			if (i.middleware != null) aggregated.middleware.push(i.middleware);
			if (i.loader != null) aggregated.loader.push(i.loader);
			if (i.action != null) aggregated.action.push(i.action);
		}
	}));
	let updates = {};
	if (typeof route.lazy === "function" && aggregated.lazy.length > 0) {
		let lazy = route.lazy;
		updates.lazy = async (...args) => {
			return throwOrReturnResult(await recurseRight(aggregated.lazy, void 0, () => lazy(...args), getInstrumentationInnerResult));
		};
	}
	if (typeof route.lazy === "object") {
		let lazyObject = route.lazy;
		if (typeof lazyObject.middleware === "function" && aggregated["lazy.middleware"].length > 0) {
			let middleware = lazyObject.middleware;
			updates.lazy = Object.assign(updates.lazy || {}, { middleware: async (...args) => {
				return throwOrReturnResult(await recurseRight(aggregated["lazy.middleware"], void 0, () => middleware(...args), getInstrumentationInnerResult));
			} });
		}
		if (typeof lazyObject.loader === "function" && aggregated["lazy.loader"].length > 0) {
			let loader = lazyObject.loader;
			updates.lazy = Object.assign(updates.lazy || {}, { loader: async (...args) => {
				return throwOrReturnResult(await recurseRight(aggregated["lazy.loader"], void 0, () => loader(...args), getInstrumentationInnerResult));
			} });
		}
		if (typeof lazyObject.action === "function" && aggregated["lazy.action"].length > 0) {
			let action = lazyObject.action;
			updates.lazy = Object.assign(updates.lazy || {}, { action: async (...args) => {
				return throwOrReturnResult(await recurseRight(aggregated["lazy.action"], void 0, () => action(...args), getInstrumentationInnerResult));
			} });
		}
	}
	if (typeof route.loader === "function" && aggregated.loader.length > 0) {
		let original = getUninstrumentedHandler(route.loader);
		let instrumented = async (...args) => {
			return throwOrReturnResult(await recurseRight(aggregated.loader, getHandlerInfo(args[0]), () => original(...args), getInstrumentationInnerResult));
		};
		if (original.hydrate === true) instrumented.hydrate = true;
		setUninstrumentedHandler(instrumented, original);
		updates.loader = instrumented;
	}
	if (typeof route.action === "function" && aggregated.action.length > 0) {
		let original = getUninstrumentedHandler(route.action);
		let instrumented = async (...args) => {
			return throwOrReturnResult(await recurseRight(aggregated.action, getHandlerInfo(args[0]), () => original(...args), getInstrumentationInnerResult));
		};
		setUninstrumentedHandler(instrumented, original);
		updates.action = instrumented;
	}
	if (route.middleware && route.middleware.length > 0 && aggregated.middleware.length > 0) updates.middleware = route.middleware.map((middleware) => {
		let original = getUninstrumentedHandler(middleware);
		let instrumented = async (...args) => {
			return throwOrReturnResult(await recurseRight(aggregated.middleware, getHandlerInfo(args[0]), () => original(...args), getInstrumentationInnerResult));
		};
		setUninstrumentedHandler(instrumented, original);
		return instrumented;
	});
	return updates;
}
function instrumentHandler(handler, fns) {
	let aggregated = { request: [] };
	fns.forEach((fn) => fn({ instrument(i) {
		if (i.request != null) aggregated.request.push(i.request);
	} }));
	let instrumentedHandler = handler;
	if (aggregated.request.length > 0) instrumentedHandler = async (...args) => {
		let [request, context] = args;
		let instrumentationContext = context ?? new RouterContextProvider();
		return throwOrReturnResult(await recurseRight(aggregated.request, {
			request: getReadonlyRequest(request),
			context: getReadonlyContext(instrumentationContext)
		}, () => handler(request, instrumentationContext), (result, info) => {
			let meta;
			try {
				meta = info.context?.get(instrumentationResultMetaContext);
			} catch {}
			invariant(result.value instanceof Response, "Expected a Response from the request handler");
			return {
				...getInstrumentationInnerResult(result),
				statusCode: result.value.status,
				meta
			};
		}));
	};
	return instrumentedHandler;
}
function getUninstrumentedHandler(handler) {
	return handler[UninstrumentedSymbol] ?? handler;
}
function setUninstrumentedHandler(handler, uninstrumentedHandler) {
	handler[UninstrumentedSymbol] = uninstrumentedHandler;
}
function throwOrReturnResult(result) {
	if (result.type === "error") throw result.value;
	return result.value;
}
async function recurseRight(impls, info, handler, getInnerResult, state = {
	result: null,
	innerResult: null
}, index = impls.length - 1) {
	let impl = impls[index];
	if (!impl) {
		try {
			state.result = {
				type: "success",
				value: await handler()
			};
		} catch (e) {
			state.result = {
				type: "error",
				value: e
			};
		}
		state.innerResult = getInnerResult(state.result, info);
	} else {
		let handlerPromise = void 0;
		let callHandler = async () => {
			if (handlerPromise) console.error("You cannot call instrumented handlers more than once");
			else handlerPromise = recurseRight(impls, info, handler, getInnerResult, state, index - 1);
			await handlerPromise;
			invariant(state.innerResult, "Expected an inner result");
			return state.innerResult;
		};
		try {
			await impl(callHandler, info);
		} catch (e) {
			console.error("An instrumentation function threw an error:", e);
		}
		if (!handlerPromise) await callHandler();
		await handlerPromise;
	}
	if (state.result) return state.result;
	state.result = {
		type: "error",
		value: /* @__PURE__ */ new Error("No result assigned in instrumentation chain.")
	};
	state.innerResult = getInnerResult(state.result, info);
	return state.result;
}
function getInstrumentationInnerResult(result) {
	if (result.type === "error" && result.value instanceof Error) return {
		status: "error",
		error: result.value
	};
	return {
		status: "success",
		error: void 0
	};
}
function getHandlerInfo(args) {
	let { request, context, params } = args;
	return {
		...args,
		request: getReadonlyRequest(request),
		params: { ...params },
		context: getReadonlyContext(context)
	};
}
function getReadonlyRequest(request) {
	return {
		method: request.method,
		url: request.url,
		headers: { get: (...args) => request.headers.get(...args) }
	};
}
function getReadonlyContext(context) {
	return { get: (ctx) => context.get(ctx) };
}
//#endregion
//#region ../../node_modules/.pnpm/react-router@8.3.0_react-dom@19.2.7_react@19.2.7__react@19.2.7/node_modules/react-router/dist/production/lib/router/router.js
/**
* react-router v8.3.0
*
* Copyright (c) Remix Software Inc.
*
* This source code is licensed under the MIT license found in the
* LICENSE.md file in the root directory of this source tree.
*
* @license MIT
*/
var validMutationMethodsArr = [
	"POST",
	"PUT",
	"PATCH",
	"DELETE"
];
var validMutationMethods = new Set(validMutationMethodsArr);
var validRequestMethodsArr = ["GET", ...validMutationMethodsArr];
var validRequestMethods = new Set(validRequestMethodsArr);
var redirectStatusCodes = /* @__PURE__ */ new Set([
	301,
	302,
	303,
	307,
	308
]);
var IDLE_NAVIGATION = {
	state: "idle",
	location: void 0,
	matches: void 0,
	historyAction: void 0,
	formMethod: void 0,
	formAction: void 0,
	formEncType: void 0,
	formData: void 0,
	json: void 0,
	text: void 0
};
var IDLE_FETCHER = {
	state: "idle",
	data: void 0,
	formMethod: void 0,
	formAction: void 0,
	formEncType: void 0,
	formData: void 0,
	json: void 0,
	text: void 0
};
var IDLE_BLOCKER = {
	state: "unblocked",
	proceed: void 0,
	reset: void 0,
	location: void 0
};
var ResetLoaderDataSymbol = Symbol("ResetLoaderData");
/**
* Create a static handler to perform server-side data loading
*
* @example
* export async function handleRequest(request: Request) {
*   let { query, dataRoutes } = createStaticHandler(routes);
*   let context = await query(request);
*
*   if (context instanceof Response) {
*     return context;
*   }
*
*   let router = createStaticRouter(dataRoutes, context);
*   return new Response(
*     ReactDOMServer.renderToString(<StaticRouterProvider ... />),
*     { headers: { "Content-Type": "text/html" } }
*   );
* }
*
* @public
* @category Data Routers
* @mode data
* @param routes The {@link RouteObject | route objects} to create a static
* handler for
* @param opts Options
* @param opts.basename The base URL for the static handler (default: `/`)
* @param opts.future Future flags for the static handler
* @returns A static handler that can be used to query data for the provided
* routes
*/
function createStaticHandler(routes, opts) {
	invariant(routes.length > 0, "You must provide a non-empty routes array to createStaticHandler");
	let manifest = {};
	let basename = (opts ? opts.basename : null) || "/";
	let _mapRouteProperties = opts?.mapRouteProperties;
	let mapRouteProperties = _mapRouteProperties ? _mapRouteProperties : () => ({});
	({ ...opts?.future });
	if (opts?.instrumentations) {
		let instrumentations = opts.instrumentations;
		mapRouteProperties = (route) => {
			return {
				..._mapRouteProperties?.(route),
				...getRouteInstrumentationUpdates(instrumentations.map((i) => i.route).filter(Boolean), route)
			};
		};
	}
	let dataRoutes = convertRoutesToDataRoutes(routes, mapRouteProperties, void 0, manifest);
	let routeBranches = flattenAndRankRoutes(dataRoutes);
	/**
	* The query() method is intended for document requests, in which we want to
	* call an optional action and potentially multiple loaders for all nested
	* routes.  It returns a StaticHandlerContext object, which is very similar
	* to the router state (location, loaderData, actionData, errors, etc.) and
	* also adds SSR-specific information such as the statusCode and headers
	* from action/loaders Responses.
	*
	* It _should_ never throw and should report all errors through the
	* returned handlerContext.errors object, properly associating errors to
	* their error boundary.  Additionally, it tracks _deepestRenderedBoundaryId
	* which can be used to emulate React error boundaries during SSR by performing
	* a second pass only down to the boundaryId.
	*
	* The one exception where we do not return a StaticHandlerContext is when a
	* redirect response is returned or thrown from any action/loader.  We
	* propagate that out and return the raw Response so the HTTP server can
	* return it directly.
	*
	* - `opts.requestContext` is an optional server context that will be passed
	*   to actions/loaders in the `context` parameter
	* - `opts.skipLoaderErrorBubbling` is an optional parameter that will prevent
	*   the bubbling of errors which allows single-fetch-type implementations
	*   where the client will handle the bubbling and we may need to return data
	*   for the handling route
	*/
	async function query(request, { requestContext, filterMatchesToLoad, skipLoaderErrorBubbling, skipRevalidation, dataStrategy, generateMiddlewareResponse, normalizePath } = {}) {
		let normalizePathImpl = normalizePath || defaultNormalizePath;
		let method = request.method;
		let location = createLocation("", normalizePathImpl(request), null, "default");
		let matches = matchRoutesImpl(dataRoutes, location, basename, false, routeBranches);
		requestContext = requestContext != null ? requestContext : new RouterContextProvider();
		if (!isValidMethod(method) && method !== "HEAD") {
			let error = getInternalRouterError(405, { method });
			let { matches: methodNotAllowedMatches, route } = getShortCircuitMatches(dataRoutes);
			let staticContext = {
				basename,
				location,
				matches: methodNotAllowedMatches,
				loaderData: {},
				actionData: null,
				errors: { [route.id]: error },
				statusCode: error.status,
				loaderHeaders: {},
				actionHeaders: {}
			};
			return generateMiddlewareResponse ? generateMiddlewareResponse(() => Promise.resolve(staticContext)) : staticContext;
		} else if (!matches) {
			let error = getInternalRouterError(404, { pathname: location.pathname });
			let { matches: notFoundMatches, route } = getShortCircuitMatches(dataRoutes);
			let staticContext = {
				basename,
				location,
				matches: notFoundMatches,
				loaderData: {},
				actionData: null,
				errors: { [route.id]: error },
				statusCode: error.status,
				loaderHeaders: {},
				actionHeaders: {}
			};
			return generateMiddlewareResponse ? generateMiddlewareResponse(() => Promise.resolve(staticContext)) : staticContext;
		}
		if (generateMiddlewareResponse) {
			invariant(requestContext instanceof RouterContextProvider, "When using middleware in `staticHandler.query()`, any provided `requestContext` must be an instance of `RouterContextProvider`");
			try {
				await loadLazyMiddlewareForMatches(matches, manifest, mapRouteProperties);
				let renderedStaticContext;
				let response = await runServerMiddlewarePipeline({
					request,
					url: createDataFunctionUrl(request, location),
					pattern: getRoutePattern(matches),
					matches,
					params: matches[0].params,
					context: requestContext
				}, async () => {
					return await generateMiddlewareResponse(async (revalidationRequest, opts = {}) => {
						let result = await queryImpl(revalidationRequest, location, matches, requestContext, dataStrategy || null, skipLoaderErrorBubbling === true, null, "filterMatchesToLoad" in opts ? opts.filterMatchesToLoad ?? null : filterMatchesToLoad ?? null, skipRevalidation === true);
						if (isResponse(result)) return result;
						renderedStaticContext = {
							location,
							basename,
							...result
						};
						return renderedStaticContext;
					});
				}, async (error, routeId) => {
					if (isRedirectResponse(error)) return error;
					if (isResponse(error)) try {
						error = new ErrorResponseImpl(error.status, error.statusText, await parseResponseBody(error));
					} catch (e) {
						error = e;
					}
					if (isDataWithResponseInit(error)) error = dataWithResponseInitToErrorResponse(error);
					if (renderedStaticContext) {
						if (routeId in renderedStaticContext.loaderData) renderedStaticContext.loaderData[routeId] = void 0;
						let staticContext = getStaticContextFromError(dataRoutes, renderedStaticContext, error, skipLoaderErrorBubbling ? routeId : findNearestBoundary(matches, routeId).route.id);
						return generateMiddlewareResponse(() => Promise.resolve(staticContext));
					} else {
						let staticContext = {
							matches,
							location,
							basename,
							loaderData: {},
							actionData: null,
							errors: { [skipLoaderErrorBubbling ? routeId : findNearestBoundary(matches, matches.find((m) => m.route.id === routeId || m.route.loader)?.route.id || routeId).route.id]: error },
							statusCode: isRouteErrorResponse(error) ? error.status : 500,
							actionHeaders: {},
							loaderHeaders: {}
						};
						return generateMiddlewareResponse(() => Promise.resolve(staticContext));
					}
				});
				invariant(isResponse(response), "Expected a response in query()");
				return response;
			} catch (e) {
				if (isResponse(e)) return e;
				throw e;
			}
		}
		let result = await queryImpl(request, location, matches, requestContext, dataStrategy || null, skipLoaderErrorBubbling === true, null, filterMatchesToLoad || null, skipRevalidation === true);
		if (isResponse(result)) return result;
		return {
			location,
			basename,
			...result
		};
	}
	/**
	* The queryRoute() method is intended for targeted route requests, either
	* for fetch ?_data requests or resource route requests.  In this case, we
	* are only ever calling a single action or loader, and we are returning the
	* returned value directly.  In most cases, this will be a Response returned
	* from the action/loader, but it may be a primitive or other value as well -
	* and in such cases the calling context should handle that accordingly.
	*
	* We do respect the throw/return differentiation, so if an action/loader
	* throws, then this method will throw the value.  This is important so we
	* can do proper boundary identification in Remix where a thrown Response
	* must go to the Catch Boundary but a returned Response is happy-path.
	*
	* One thing to note is that any Router-initiated Errors that make sense
	* to associate with a status code will be thrown as an ErrorResponse
	* instance which include the raw Error, such that the calling context can
	* serialize the error as they see fit while including the proper response
	* code.  Examples here are 404 and 405 errors that occur prior to reaching
	* any user-defined loaders.
	*
	* - `opts.routeId` allows you to specify the specific route handler to call.
	*   If not provided the handler will determine the proper route by matching
	*   against `request.url`
	* - `opts.requestContext` is an optional server context that will be passed
	*    to actions/loaders in the `context` parameter
	*/
	async function queryRoute(request, { routeId, requestContext, dataStrategy, generateMiddlewareResponse, normalizePath } = {}) {
		let normalizePathImpl = normalizePath || defaultNormalizePath;
		let method = request.method;
		let location = createLocation("", normalizePathImpl(request), null, "default");
		let matches = matchRoutesImpl(dataRoutes, location, basename, false, routeBranches);
		requestContext = requestContext != null ? requestContext : new RouterContextProvider();
		if (!isValidMethod(method) && method !== "HEAD" && method !== "OPTIONS") throw getInternalRouterError(405, { method });
		else if (!matches) throw getInternalRouterError(404, { pathname: location.pathname });
		let match = routeId ? matches.find((m) => m.route.id === routeId) : getTargetMatch(matches, location);
		if (routeId && !match) throw getInternalRouterError(403, {
			pathname: location.pathname,
			routeId
		});
		else if (!match) throw getInternalRouterError(404, { pathname: location.pathname });
		if (generateMiddlewareResponse) {
			invariant(requestContext instanceof RouterContextProvider, "When using middleware in `staticHandler.queryRoute()`, any provided `requestContext` must be an instance of `RouterContextProvider`");
			await loadLazyMiddlewareForMatches(matches, manifest, mapRouteProperties);
			return await runServerMiddlewarePipeline({
				request,
				url: createDataFunctionUrl(request, location),
				pattern: getRoutePattern(matches),
				matches,
				params: matches[0].params,
				context: requestContext
			}, async () => {
				return await generateMiddlewareResponse(async (innerRequest) => {
					let processed = handleQueryResult(await queryImpl(innerRequest, location, matches, requestContext, dataStrategy || null, false, match, null, false));
					return isResponse(processed) ? processed : typeof processed === "string" ? new Response(processed) : Response.json(processed);
				});
			}, (error) => {
				if (isDataWithResponseInit(error)) return Promise.resolve(dataWithResponseInitToResponse(error));
				if (isResponse(error)) return Promise.resolve(error);
				throw error;
			});
		}
		return handleQueryResult(await queryImpl(request, location, matches, requestContext, dataStrategy || null, false, match, null, false));
		function handleQueryResult(result) {
			if (isResponse(result)) return result;
			let error = result.errors ? Object.values(result.errors)[0] : void 0;
			if (error !== void 0) throw error;
			if (result.actionData) return Object.values(result.actionData)[0];
			if (result.loaderData) return Object.values(result.loaderData)[0];
		}
	}
	async function queryImpl(request, location, matches, requestContext, dataStrategy, skipLoaderErrorBubbling, routeMatch, filterMatchesToLoad, skipRevalidation) {
		invariant(request.signal, "query()/queryRoute() requests must contain an AbortController signal");
		try {
			if (isMutationMethod(request.method)) return await submit(request, location, matches, routeMatch || getTargetMatch(matches, location), requestContext, dataStrategy, skipLoaderErrorBubbling, routeMatch != null, filterMatchesToLoad, skipRevalidation);
			let result = await loadRouteData(request, location, matches, requestContext, dataStrategy, skipLoaderErrorBubbling, routeMatch, filterMatchesToLoad);
			return isResponse(result) ? result : {
				...result,
				actionData: null,
				actionHeaders: {}
			};
		} catch (e) {
			if (isDataStrategyResult(e) && isResponse(e.result)) {
				if (e.type === "error") throw e.result;
				return e.result;
			}
			if (isRedirectResponse(e)) return e;
			throw e;
		}
	}
	async function submit(request, location, matches, actionMatch, requestContext, dataStrategy, skipLoaderErrorBubbling, isRouteRequest, filterMatchesToLoad, skipRevalidation) {
		let result;
		if (!actionMatch.route.action && !actionMatch.route.lazy) {
			let error = getInternalRouterError(405, {
				method: request.method,
				pathname: new URL(request.url).pathname,
				routeId: actionMatch.route.id
			});
			if (isRouteRequest) throw error;
			result = {
				type: "error",
				error
			};
		} else {
			result = (await callDataStrategy(request, location, getTargetedDataStrategyMatches(mapRouteProperties, manifest, request, location, matches, actionMatch, [], requestContext), isRouteRequest, requestContext, dataStrategy))[actionMatch.route.id];
			if (request.signal.aborted) throwStaticHandlerAbortedError(request, isRouteRequest);
		}
		if (isRedirectResult(result)) throw new Response(null, {
			status: result.response.status,
			headers: { Location: result.response.headers.get("Location") }
		});
		if (isRouteRequest) {
			if (isErrorResult(result)) throw result.error;
			return {
				matches: [actionMatch],
				loaderData: {},
				actionData: { [actionMatch.route.id]: result.data },
				errors: null,
				statusCode: 200,
				loaderHeaders: {},
				actionHeaders: {}
			};
		}
		if (skipRevalidation) if (isErrorResult(result)) {
			let boundaryMatch = skipLoaderErrorBubbling ? actionMatch : findNearestBoundary(matches, actionMatch.route.id);
			return {
				statusCode: isRouteErrorResponse(result.error) ? result.error.status : result.statusCode != null ? result.statusCode : 500,
				actionData: null,
				actionHeaders: { ...result.headers ? { [actionMatch.route.id]: result.headers } : {} },
				matches,
				loaderData: {},
				errors: { [boundaryMatch.route.id]: result.error },
				loaderHeaders: {}
			};
		} else return {
			actionData: { [actionMatch.route.id]: result.data },
			actionHeaders: result.headers ? { [actionMatch.route.id]: result.headers } : {},
			matches,
			loaderData: {},
			errors: null,
			statusCode: result.statusCode || 200,
			loaderHeaders: {}
		};
		let loaderRequest = new Request(request.url, {
			headers: request.headers,
			redirect: request.redirect,
			signal: request.signal
		});
		if (isErrorResult(result)) return {
			...await loadRouteData(loaderRequest, location, matches, requestContext, dataStrategy, skipLoaderErrorBubbling, null, filterMatchesToLoad, [(skipLoaderErrorBubbling ? actionMatch : findNearestBoundary(matches, actionMatch.route.id)).route.id, result]),
			statusCode: isRouteErrorResponse(result.error) ? result.error.status : result.statusCode != null ? result.statusCode : 500,
			actionData: null,
			actionHeaders: { ...result.headers ? { [actionMatch.route.id]: result.headers } : {} }
		};
		return {
			...await loadRouteData(loaderRequest, location, matches, requestContext, dataStrategy, skipLoaderErrorBubbling, null, filterMatchesToLoad),
			actionData: { [actionMatch.route.id]: result.data },
			...result.statusCode ? { statusCode: result.statusCode } : {},
			actionHeaders: result.headers ? { [actionMatch.route.id]: result.headers } : {}
		};
	}
	async function loadRouteData(request, location, matches, requestContext, dataStrategy, skipLoaderErrorBubbling, routeMatch, filterMatchesToLoad, pendingActionResult) {
		let isRouteRequest = routeMatch != null;
		if (isRouteRequest && !routeMatch?.route.loader && !routeMatch?.route.lazy) throw getInternalRouterError(400, {
			method: request.method,
			pathname: new URL(request.url).pathname,
			routeId: routeMatch?.route.id
		});
		let dsMatches;
		if (routeMatch) dsMatches = getTargetedDataStrategyMatches(mapRouteProperties, manifest, request, location, matches, routeMatch, [], requestContext);
		else {
			let maxIdx = pendingActionResult && isErrorResult(pendingActionResult[1]) ? matches.findIndex((m) => m.route.id === pendingActionResult[0]) - 1 : void 0;
			let pattern = getRoutePattern(matches);
			dsMatches = matches.map((match, index) => {
				if (maxIdx != null && index > maxIdx) return getDataStrategyMatch(mapRouteProperties, manifest, request, location, pattern, match, [], requestContext, false);
				return getDataStrategyMatch(mapRouteProperties, manifest, request, location, pattern, match, [], requestContext, (match.route.loader || match.route.lazy) != null && (!filterMatchesToLoad || filterMatchesToLoad(match)));
			});
		}
		if (!dataStrategy && !dsMatches.some((m) => m.shouldLoad)) return {
			matches,
			loaderData: {},
			errors: pendingActionResult && isErrorResult(pendingActionResult[1]) ? { [pendingActionResult[0]]: pendingActionResult[1].error } : null,
			statusCode: 200,
			loaderHeaders: {}
		};
		let results = await callDataStrategy(request, location, dsMatches, isRouteRequest, requestContext, dataStrategy);
		if (request.signal.aborted) throwStaticHandlerAbortedError(request, isRouteRequest);
		return {
			...processRouteLoaderData(matches, results, pendingActionResult, true, skipLoaderErrorBubbling),
			matches
		};
	}
	async function callDataStrategy(request, location, matches, isRouteRequest, requestContext, dataStrategy) {
		let results = await callDataStrategyImpl(dataStrategy || defaultDataStrategy, request, location, matches, null, requestContext, true);
		let dataResults = {};
		await Promise.all(matches.map(async (match) => {
			if (!(match.route.id in results)) return;
			let result = results[match.route.id];
			if (isRedirectDataStrategyResult(result)) {
				let response = result.result;
				throw normalizeRelativeRoutingRedirectResponse(response, request, match.route.id, matches, basename);
			}
			if (isRouteRequest) {
				if (isResponse(result.result)) throw result;
				else if (isDataWithResponseInit(result.result)) throw dataWithResponseInitToResponse(result.result);
			}
			dataResults[match.route.id] = await convertDataStrategyResultToDataResult(result);
		}));
		return dataResults;
	}
	return {
		dataRoutes,
		_internalRouteBranches: routeBranches,
		query,
		queryRoute
	};
}
/**
* Given an existing StaticHandlerContext and an error thrown at render time,
* provide an updated StaticHandlerContext suitable for a second SSR render
*
* @category Utils
*/
function getStaticContextFromError(routes, handlerContext, error, boundaryId) {
	let errorBoundaryId = boundaryId || handlerContext._deepestRenderedBoundaryId || routes[0].id;
	return {
		...handlerContext,
		statusCode: isRouteErrorResponse(error) ? error.status : 500,
		errors: { [errorBoundaryId]: error }
	};
}
function throwStaticHandlerAbortedError(request, isRouteRequest) {
	if (request.signal.reason !== void 0) throw request.signal.reason;
	throw new Error(`${isRouteRequest ? "queryRoute" : "query"}() call aborted without an \`AbortSignal.reason\`: ${request.method} ${request.url}`);
}
function defaultNormalizePath(request) {
	let url = new URL(request.url);
	return {
		pathname: url.pathname,
		search: url.search,
		hash: url.hash
	};
}
function normalizeTo(location, matches, basename, to, fromRouteId, relative) {
	let contextualMatches;
	let activeRouteMatch;
	if (fromRouteId) {
		contextualMatches = [];
		for (let match of matches) {
			contextualMatches.push(match);
			if (match.route.id === fromRouteId) {
				activeRouteMatch = match;
				break;
			}
		}
	} else {
		contextualMatches = matches;
		activeRouteMatch = matches[matches.length - 1];
	}
	let path = resolveTo(to ? to : ".", getResolveToMatches(contextualMatches), stripBasename(location.pathname, basename) || location.pathname, relative === "path");
	if (to == null) {
		path.search = location.search;
		path.hash = location.hash;
	}
	if ((to == null || to === "" || to === ".") && activeRouteMatch) {
		let nakedIndex = hasNakedIndexQuery(path.search);
		if (activeRouteMatch.route.index && !nakedIndex) path.search = path.search ? path.search.replace(/^\?/, "?index&") : "?index";
		else if (!activeRouteMatch.route.index && nakedIndex) {
			let params = new URLSearchParams(path.search);
			let indexValues = params.getAll("index");
			params.delete("index");
			indexValues.filter((v) => v).forEach((v) => params.append("index", v));
			let qs = params.toString();
			path.search = qs ? `?${qs}` : "";
		}
	}
	if (basename !== "/") path.pathname = prependBasename({
		basename,
		pathname: path.pathname
	});
	return createPath(path);
}
function shouldRevalidateLoader(loaderMatch, arg) {
	if (loaderMatch.route.shouldRevalidate) {
		let routeChoice = loaderMatch.route.shouldRevalidate(arg);
		if (typeof routeChoice === "boolean") return routeChoice;
	}
	return arg.defaultShouldRevalidate;
}
var lazyRoutePropertyCache = /* @__PURE__ */ new WeakMap();
var loadLazyRouteProperty = ({ key, route, manifest, mapRouteProperties }) => {
	let routeToUpdate = manifest[route.id];
	invariant(routeToUpdate, "No route found in manifest");
	if (!routeToUpdate.lazy || typeof routeToUpdate.lazy !== "object") return;
	let lazyFn = routeToUpdate.lazy[key];
	if (!lazyFn) return;
	let cache = lazyRoutePropertyCache.get(routeToUpdate);
	if (!cache) {
		cache = {};
		lazyRoutePropertyCache.set(routeToUpdate, cache);
	}
	let cachedPromise = cache[key];
	if (cachedPromise) return cachedPromise;
	let propertyPromise = (async () => {
		let isUnsupported = isUnsupportedLazyRouteObjectKey(key);
		let isStaticallyDefined = routeToUpdate[key] !== void 0;
		if (isUnsupported) {
			warning(!isUnsupported, "Route property " + key + " is not a supported lazy route property. This property will be ignored.");
			cache[key] = Promise.resolve();
		} else if (isStaticallyDefined) warning(false, `Route "${routeToUpdate.id}" has a static property "${key}" defined. The lazy property will be ignored.`);
		else {
			let value = await lazyFn();
			if (value != null) {
				Object.assign(routeToUpdate, { [key]: value });
				Object.assign(routeToUpdate, mapRouteProperties(routeToUpdate));
			}
		}
		if (typeof routeToUpdate.lazy === "object") {
			routeToUpdate.lazy[key] = void 0;
			if (Object.values(routeToUpdate.lazy).every((value) => value === void 0)) routeToUpdate.lazy = void 0;
		}
	})();
	cache[key] = propertyPromise;
	return propertyPromise;
};
var lazyRouteFunctionCache = /* @__PURE__ */ new WeakMap();
/**
* Execute route.lazy functions to lazily load route modules (loader, action,
* shouldRevalidate) and update the routeManifest in place which shares objects
* with dataRoutes so those get updated as well.
*/
function loadLazyRoute(route, type, manifest, mapRouteProperties, lazyRoutePropertiesToSkip) {
	let routeToUpdate = manifest[route.id];
	invariant(routeToUpdate, "No route found in manifest");
	if (!route.lazy) return {
		lazyRoutePromise: void 0,
		lazyHandlerPromise: void 0
	};
	if (typeof route.lazy === "function") {
		let cachedPromise = lazyRouteFunctionCache.get(routeToUpdate);
		if (cachedPromise) return {
			lazyRoutePromise: cachedPromise,
			lazyHandlerPromise: cachedPromise
		};
		let lazyRoutePromise = (async () => {
			invariant(typeof route.lazy === "function", "No lazy route function found");
			let lazyRoute = await route.lazy();
			let routeUpdates = {};
			for (let lazyRouteProperty in lazyRoute) {
				let lazyValue = lazyRoute[lazyRouteProperty];
				if (lazyValue === void 0) continue;
				let isUnsupported = isUnsupportedLazyRouteFunctionKey(lazyRouteProperty);
				let isStaticallyDefined = routeToUpdate[lazyRouteProperty] !== void 0;
				if (isUnsupported) warning(!isUnsupported, "Route property " + lazyRouteProperty + " is not a supported property to be returned from a lazy route function. This property will be ignored.");
				else if (isStaticallyDefined) warning(!isStaticallyDefined, `Route "${routeToUpdate.id}" has a static property "${lazyRouteProperty}" defined but its lazy function is also returning a value for this property. The lazy route property "${lazyRouteProperty}" will be ignored.`);
				else routeUpdates[lazyRouteProperty] = lazyValue;
			}
			Object.assign(routeToUpdate, routeUpdates);
			Object.assign(routeToUpdate, {
				...mapRouteProperties(routeToUpdate),
				lazy: void 0
			});
		})();
		lazyRouteFunctionCache.set(routeToUpdate, lazyRoutePromise);
		lazyRoutePromise.catch(() => {});
		return {
			lazyRoutePromise,
			lazyHandlerPromise: lazyRoutePromise
		};
	}
	let lazyKeys = Object.keys(route.lazy);
	let lazyPropertyPromises = [];
	let lazyHandlerPromise = void 0;
	for (let key of lazyKeys) {
		if (lazyRoutePropertiesToSkip && lazyRoutePropertiesToSkip.includes(key)) continue;
		let promise = loadLazyRouteProperty({
			key,
			route,
			manifest,
			mapRouteProperties
		});
		if (promise) {
			lazyPropertyPromises.push(promise);
			if (key === type) lazyHandlerPromise = promise;
		}
	}
	let lazyRoutePromise = lazyPropertyPromises.length > 0 ? Promise.all(lazyPropertyPromises).then(() => {}) : void 0;
	lazyRoutePromise?.catch(() => {});
	lazyHandlerPromise?.catch(() => {});
	return {
		lazyRoutePromise,
		lazyHandlerPromise
	};
}
function isNonNullable(value) {
	return value !== void 0;
}
function loadLazyMiddlewareForMatches(matches, manifest, mapRouteProperties) {
	let promises = matches.map(({ route }) => {
		if (typeof route.lazy !== "object" || !route.lazy.middleware) return;
		return loadLazyRouteProperty({
			key: "middleware",
			route,
			manifest,
			mapRouteProperties
		});
	}).filter(isNonNullable);
	return promises.length > 0 ? Promise.all(promises) : void 0;
}
async function defaultDataStrategy(args) {
	let matchesToLoad = args.matches.filter((m) => m.shouldLoad);
	let keyedResults = {};
	(await Promise.all(matchesToLoad.map((m) => m.resolve()))).forEach((result, i) => {
		keyedResults[matchesToLoad[i].route.id] = result;
	});
	return keyedResults;
}
function runServerMiddlewarePipeline(args, handler, errorHandler) {
	return runMiddlewarePipeline(args, handler, processResult, isResponse, errorHandler);
	function processResult(result) {
		return isDataWithResponseInit(result) ? dataWithResponseInitToResponse(result) : result;
	}
}
function runClientMiddlewarePipeline(args, handler) {
	return runMiddlewarePipeline(args, handler, (r) => {
		if (isRedirectResponse(r)) throw r;
		return r;
	}, isDataStrategyResults, errorHandler);
	async function errorHandler(error, routeId, nextResult) {
		if (nextResult) return Object.assign(nextResult.value, { [routeId]: {
			type: "error",
			result: error
		} });
		else {
			let { matches } = args;
			let maxBoundaryIdx = Math.min(Math.max(matches.findIndex((m) => m.route.id === routeId), 0), Math.max(matches.findIndex((m) => m.shouldCallHandler()), 0));
			let deepestRouteId = matches[maxBoundaryIdx].route.id;
			for (let match of matches.slice(0, maxBoundaryIdx + 1)) try {
				await match._lazyPromises?.route;
			} catch {
				deepestRouteId = match.route.id;
				break;
			}
			return { [findNearestBoundary(matches, deepestRouteId).route.id]: {
				type: "error",
				result: error
			} };
		}
	}
}
async function runMiddlewarePipeline(args, handler, processResult, isResult, errorHandler) {
	let { matches, ...dataFnArgs } = args;
	return await callRouteMiddleware(dataFnArgs, matches.flatMap((m) => m.route.middleware ? m.route.middleware.map((fn) => [m.route.id, fn]) : []), handler, processResult, isResult, errorHandler);
}
async function callRouteMiddleware(args, middlewares, handler, processResult, isResult, errorHandler, idx = 0) {
	let { request } = args;
	if (request.signal.aborted) throw request.signal.reason ?? /* @__PURE__ */ new Error(`Request aborted: ${request.method} ${request.url}`);
	let tuple = middlewares[idx];
	if (!tuple) return await handler();
	let [routeId, middleware] = tuple;
	let nextResult;
	let next = async () => {
		if (nextResult) throw new Error("You may only call `next()` once per middleware");
		try {
			nextResult = { value: await callRouteMiddleware(args, middlewares, handler, processResult, isResult, errorHandler, idx + 1) };
			return nextResult.value;
		} catch (error) {
			nextResult = { value: await errorHandler(error, routeId, nextResult) };
			return nextResult.value;
		}
	};
	try {
		let value = await middleware(args, next);
		let result = value != null ? processResult(value) : void 0;
		if (isResult(result)) return result;
		else if (nextResult) return result ?? nextResult.value;
		else {
			nextResult = { value: await next() };
			return nextResult.value;
		}
	} catch (error) {
		return await errorHandler(error, routeId, nextResult);
	}
}
function getDataStrategyMatchLazyPromises(mapRouteProperties, manifest, request, match, lazyRoutePropertiesToSkip) {
	let lazyMiddlewarePromise = loadLazyRouteProperty({
		key: "middleware",
		route: match.route,
		manifest,
		mapRouteProperties
	});
	let lazyRoutePromises = loadLazyRoute(match.route, isMutationMethod(request.method) ? "action" : "loader", manifest, mapRouteProperties, lazyRoutePropertiesToSkip);
	return {
		middleware: lazyMiddlewarePromise,
		route: lazyRoutePromises.lazyRoutePromise,
		handler: lazyRoutePromises.lazyHandlerPromise
	};
}
function getDataStrategyMatch(mapRouteProperties, manifest, request, path, pattern, match, lazyRoutePropertiesToSkip, scopedContext, shouldLoad, shouldRevalidateArgs = null, callSiteDefaultShouldRevalidate) {
	let isUsingNewApi = false;
	let _lazyPromises = getDataStrategyMatchLazyPromises(mapRouteProperties, manifest, request, match, lazyRoutePropertiesToSkip);
	return {
		...match,
		_lazyPromises,
		shouldLoad,
		shouldRevalidateArgs,
		shouldCallHandler(defaultShouldRevalidate) {
			isUsingNewApi = true;
			if (!shouldRevalidateArgs) return shouldLoad;
			if (typeof callSiteDefaultShouldRevalidate === "boolean") return shouldRevalidateLoader(match, {
				...shouldRevalidateArgs,
				defaultShouldRevalidate: callSiteDefaultShouldRevalidate
			});
			if (typeof defaultShouldRevalidate === "boolean") return shouldRevalidateLoader(match, {
				...shouldRevalidateArgs,
				defaultShouldRevalidate
			});
			return shouldRevalidateLoader(match, shouldRevalidateArgs);
		},
		resolve(handlerOverride) {
			let { lazy, loader, middleware } = match.route;
			let callHandler = isUsingNewApi || shouldLoad || handlerOverride && !isMutationMethod(request.method) && (lazy || loader);
			let isMiddlewareOnlyRoute = middleware && middleware.length > 0 && !loader && !lazy;
			if (callHandler && (isMutationMethod(request.method) || !isMiddlewareOnlyRoute)) return callLoaderOrAction({
				request,
				path,
				pattern,
				match,
				lazyHandlerPromise: _lazyPromises?.handler,
				lazyRoutePromise: _lazyPromises?.route,
				handlerOverride,
				scopedContext
			});
			return Promise.resolve({
				type: "data",
				result: void 0
			});
		}
	};
}
function getTargetedDataStrategyMatches(mapRouteProperties, manifest, request, path, matches, targetMatch, lazyRoutePropertiesToSkip, scopedContext, shouldRevalidateArgs = null) {
	return matches.map((match) => {
		if (match.route.id !== targetMatch.route.id) return {
			...match,
			shouldLoad: false,
			shouldRevalidateArgs,
			shouldCallHandler: () => false,
			_lazyPromises: getDataStrategyMatchLazyPromises(mapRouteProperties, manifest, request, match, lazyRoutePropertiesToSkip),
			resolve: () => Promise.resolve({
				type: "data",
				result: void 0
			})
		};
		return getDataStrategyMatch(mapRouteProperties, manifest, request, path, getRoutePattern(matches), match, lazyRoutePropertiesToSkip, scopedContext, true, shouldRevalidateArgs);
	});
}
async function callDataStrategyImpl(dataStrategyImpl, request, path, matches, fetcherKey, scopedContext, isStaticHandler) {
	if (matches.some((m) => m._lazyPromises?.middleware)) await Promise.all(matches.map((m) => m._lazyPromises?.middleware));
	let dataStrategyArgs = {
		request,
		url: createDataFunctionUrl(request, path),
		pattern: getRoutePattern(matches),
		params: matches[0].params,
		context: scopedContext,
		matches
	};
	let runClientMiddleware = isStaticHandler ? () => {
		throw new Error("You cannot call `runClientMiddleware()` from a static handler `dataStrategy`. Middleware is run outside of `dataStrategy` during SSR in order to bubble up the Response.  You can enable middleware via the `respond` API in `query`/`queryRoute`");
	} : (cb) => {
		let typedDataStrategyArgs = dataStrategyArgs;
		return runClientMiddlewarePipeline(typedDataStrategyArgs, () => {
			return cb({
				...typedDataStrategyArgs,
				fetcherKey,
				runClientMiddleware: () => {
					throw new Error("Cannot call `runClientMiddleware()` from within an `runClientMiddleware` handler");
				}
			});
		});
	};
	let results = await dataStrategyImpl({
		...dataStrategyArgs,
		fetcherKey,
		runClientMiddleware
	});
	try {
		await Promise.all(matches.flatMap((m) => [m._lazyPromises?.handler, m._lazyPromises?.route]));
	} catch (e) {}
	return results;
}
async function callLoaderOrAction({ request, path, pattern, match, lazyHandlerPromise, lazyRoutePromise, handlerOverride, scopedContext }) {
	let result;
	let onReject;
	let isAction = isMutationMethod(request.method);
	let type = isAction ? "action" : "loader";
	let runHandler = (handler) => {
		let reject;
		let abortPromise = new Promise((_, r) => reject = r);
		onReject = () => reject();
		request.signal.addEventListener("abort", onReject);
		let actualHandler = (ctx) => {
			if (typeof handler !== "function") return Promise.reject(/* @__PURE__ */ new Error(`You cannot call the handler for a route which defines a boolean "${type}" [routeId: ${match.route.id}]`));
			return handler({
				request,
				url: createDataFunctionUrl(request, path),
				pattern,
				params: match.params,
				context: scopedContext
			}, ...ctx !== void 0 ? [ctx] : []);
		};
		let handlerPromise = (async () => {
			try {
				return {
					type: "data",
					result: await (handlerOverride ? handlerOverride((ctx) => actualHandler(ctx)) : actualHandler())
				};
			} catch (e) {
				return {
					type: "error",
					result: e
				};
			}
		})();
		return Promise.race([handlerPromise, abortPromise]);
	};
	try {
		let handler = isAction ? match.route.action : match.route.loader;
		if (lazyHandlerPromise || lazyRoutePromise) if (handler) {
			let handlerError;
			let [value] = await Promise.all([
				runHandler(handler).catch((e) => {
					handlerError = e;
				}),
				lazyHandlerPromise,
				lazyRoutePromise
			]);
			if (handlerError !== void 0) throw handlerError;
			result = value;
		} else {
			await lazyHandlerPromise;
			let handler = isAction ? match.route.action : match.route.loader;
			if (handler) [result] = await Promise.all([runHandler(handler), lazyRoutePromise]);
			else if (type === "action") {
				let url = new URL(request.url);
				let pathname = url.pathname + url.search;
				throw getInternalRouterError(405, {
					method: request.method,
					pathname,
					routeId: match.route.id
				});
			} else return {
				type: "data",
				result: void 0
			};
		}
		else if (!handler) {
			let url = new URL(request.url);
			throw getInternalRouterError(404, { pathname: url.pathname + url.search });
		} else result = await runHandler(handler);
	} catch (e) {
		return {
			type: "error",
			result: e
		};
	} finally {
		if (onReject) request.signal.removeEventListener("abort", onReject);
	}
	return result;
}
async function parseResponseBody(response) {
	let contentType = response.headers.get("Content-Type");
	if (contentType && /\bapplication\/json\b/.test(contentType)) return response.body == null ? null : response.json();
	return response.text();
}
async function convertDataStrategyResultToDataResult(dataStrategyResult) {
	let { result, type } = dataStrategyResult;
	if (isResponse(result)) {
		let data;
		try {
			data = await parseResponseBody(result);
		} catch (e) {
			return {
				type: "error",
				error: e
			};
		}
		if (type === "error") return {
			type: "error",
			error: new ErrorResponseImpl(result.status, result.statusText, data),
			statusCode: result.status,
			headers: result.headers
		};
		return {
			type: "data",
			data,
			statusCode: result.status,
			headers: result.headers
		};
	}
	if (type === "error") {
		if (isDataWithResponseInit(result)) {
			if (result.data instanceof Error) return {
				type: "error",
				error: result.data,
				statusCode: result.init?.status,
				headers: result.init?.headers ? new Headers(result.init.headers) : void 0
			};
			return {
				type: "error",
				error: dataWithResponseInitToErrorResponse(result),
				statusCode: isRouteErrorResponse(result) ? result.status : void 0,
				headers: result.init?.headers ? new Headers(result.init.headers) : void 0
			};
		}
		return {
			type: "error",
			error: result,
			statusCode: isRouteErrorResponse(result) ? result.status : void 0
		};
	}
	if (isDataWithResponseInit(result)) return {
		type: "data",
		data: result.data,
		statusCode: result.init?.status,
		headers: result.init?.headers ? new Headers(result.init.headers) : void 0
	};
	return {
		type: "data",
		data: result
	};
}
function normalizeRelativeRoutingRedirectResponse(response, request, routeId, matches, basename) {
	let location = response.headers.get("Location");
	invariant(location, "Redirects returned/thrown from loaders/actions must have a Location header");
	if (!isAbsoluteUrl(location)) {
		let trimmedMatches = matches.slice(0, matches.findIndex((m) => m.route.id === routeId) + 1);
		location = normalizeTo(new URL(request.url), trimmedMatches, basename, location);
		response.headers.set("Location", location);
	}
	return response;
}
var invalidProtocols = [
	"about:",
	"blob:",
	"chrome:",
	"chrome-untrusted:",
	"content:",
	"data:",
	"devtools:",
	"file:",
	"filesystem:",
	"javascript:"
];
function hasInvalidProtocol(location) {
	try {
		return invalidProtocols.includes(new URL(location).protocol);
	} catch {
		return false;
	}
}
function processRouteLoaderData(matches, results, pendingActionResult, isStaticHandler = false, skipLoaderErrorBubbling = false) {
	let loaderData = {};
	let errors = null;
	let statusCode;
	let foundError = false;
	let loaderHeaders = {};
	let pendingError = pendingActionResult && isErrorResult(pendingActionResult[1]) ? pendingActionResult[1].error : void 0;
	matches.forEach((match) => {
		if (!(match.route.id in results)) return;
		let id = match.route.id;
		let result = results[id];
		invariant(!isRedirectResult(result), "Cannot handle redirect results in processLoaderData");
		if (isErrorResult(result)) {
			let error = result.error;
			if (pendingError !== void 0) {
				error = pendingError;
				pendingError = void 0;
			}
			errors = errors || {};
			if (skipLoaderErrorBubbling) errors[id] = error;
			else {
				let boundaryMatch = findNearestBoundary(matches, id);
				if (errors[boundaryMatch.route.id] == null) errors[boundaryMatch.route.id] = error;
			}
			if (!isStaticHandler) loaderData[id] = ResetLoaderDataSymbol;
			if (!foundError) {
				foundError = true;
				statusCode = isRouteErrorResponse(result.error) ? result.error.status : 500;
			}
			if (result.headers) loaderHeaders[id] = result.headers;
		} else {
			loaderData[id] = result.data;
			if (result.statusCode && result.statusCode !== 200 && !foundError) statusCode = result.statusCode;
			if (result.headers) loaderHeaders[id] = result.headers;
		}
	});
	if (pendingError !== void 0 && pendingActionResult) {
		errors = { [pendingActionResult[0]]: pendingError };
		if (pendingActionResult[2]) loaderData[pendingActionResult[2]] = void 0;
	}
	return {
		loaderData,
		errors,
		statusCode: statusCode || 200,
		loaderHeaders
	};
}
function findNearestBoundary(matches, routeId) {
	return (routeId ? matches.slice(0, matches.findIndex((m) => m.route.id === routeId) + 1) : [...matches]).reverse().find((m) => m.route.ErrorBoundary != null || m.route.errorElement != null) || matches[0];
}
function getShortCircuitMatches(routes) {
	let route = routes.length === 1 ? routes[0] : routes.find((r) => r.index || !r.path || r.path === "/") || { id: `__shim-error-route__` };
	return {
		matches: [{
			params: {},
			pathname: "",
			pathnameBase: "",
			route
		}],
		route
	};
}
function getInternalRouterError(status, { pathname, routeId, method, type, message } = {}) {
	let statusText = "Unknown Server Error";
	let errorMessage = "Unknown @remix-run/router error";
	if (status === 400) {
		statusText = "Bad Request";
		if (method && pathname && routeId) errorMessage = `You made a ${method} request to "${pathname}" but did not provide a \`loader\` for route "${routeId}", so there is no way to handle the request.`;
		else if (type === "invalid-body") errorMessage = "Unable to encode submission body";
	} else if (status === 403) {
		statusText = "Forbidden";
		errorMessage = `Route "${routeId}" does not match URL "${pathname}"`;
	} else if (status === 404) {
		statusText = "Not Found";
		errorMessage = `No route matches URL "${pathname}"`;
	} else if (status === 405) {
		statusText = "Method Not Allowed";
		if (method && pathname && routeId) errorMessage = `You made a ${method.toUpperCase()} request to "${pathname}" but did not provide an \`action\` for route "${routeId}", so there is no way to handle the request.`;
		else if (method) errorMessage = `Invalid request method "${method.toUpperCase()}"`;
	}
	return new ErrorResponseImpl(status || 500, statusText, new Error(errorMessage), true);
}
function dataWithResponseInitToResponse(data) {
	return Response.json(data.data, data.init ?? void 0);
}
function dataWithResponseInitToErrorResponse(data) {
	return new ErrorResponseImpl(data.init?.status ?? 500, data.init?.statusText ?? "Internal Server Error", data.data);
}
function isDataStrategyResults(result) {
	return result != null && typeof result === "object" && Object.entries(result).every(([key, value]) => typeof key === "string" && isDataStrategyResult(value));
}
function isDataStrategyResult(result) {
	return result != null && typeof result === "object" && "type" in result && "result" in result && (result.type === "data" || result.type === "error");
}
function isRedirectDataStrategyResult(result) {
	return isResponse(result.result) && redirectStatusCodes.has(result.result.status);
}
function isErrorResult(result) {
	return result.type === "error";
}
function isRedirectResult(result) {
	return (result && result.type) === "redirect";
}
function isDataWithResponseInit(value) {
	return typeof value === "object" && value != null && "type" in value && "data" in value && "init" in value && value.type === "DataWithResponseInit";
}
function isResponse(value) {
	return value != null && typeof value.status === "number" && typeof value.statusText === "string" && typeof value.headers === "object" && typeof value.body !== "undefined";
}
function isRedirectStatusCode(statusCode) {
	return redirectStatusCodes.has(statusCode);
}
function isRedirectResponse(result) {
	return isResponse(result) && isRedirectStatusCode(result.status) && result.headers.has("Location");
}
function isValidMethod(method) {
	return validRequestMethods.has(method.toUpperCase());
}
function isMutationMethod(method) {
	return validMutationMethods.has(method.toUpperCase());
}
function hasNakedIndexQuery(search) {
	return new URLSearchParams(search).getAll("index").some((v) => v === "");
}
function getTargetMatch(matches, location) {
	let search = typeof location === "string" ? parsePath(location).search : location.search;
	if (matches[matches.length - 1].route.index && hasNakedIndexQuery(search || "")) return matches[matches.length - 1];
	let pathMatches = getPathContributingMatches(matches);
	return pathMatches[pathMatches.length - 1];
}
//#endregion
//#region ../../node_modules/.pnpm/react-router@8.3.0_react-dom@19.2.7_react@19.2.7__react@19.2.7/node_modules/react-router/dist/production/lib/dom/ssr/markup.js
/**
* react-router v8.3.0
*
* Copyright (c) Remix Software Inc.
*
* This source code is licensed under the MIT license found in the
* LICENSE.md file in the root directory of this source tree.
*
* @license MIT
*/
var ESCAPE_LOOKUP = {
	"&": "\\u0026",
	">": "\\u003e",
	"<": "\\u003c",
	"\u2028": "\\u2028",
	"\u2029": "\\u2029"
};
var ESCAPE_REGEX = /[&><\u2028\u2029]/g;
function escapeHtml(html) {
	return html.replace(ESCAPE_REGEX, (match) => ESCAPE_LOOKUP[match]);
}
//#endregion
//#region ../../node_modules/.pnpm/react-router@8.3.0_react-dom@19.2.7_react@19.2.7__react@19.2.7/node_modules/react-router/dist/production/vendor/turbo-stream-v2/utils.js
/**
* react-router v8.3.0
*
* Copyright (c) Remix Software Inc.
*
* This source code is licensed under the MIT license found in the
* LICENSE.md file in the root directory of this source tree.
*
* @license MIT
*/
var Deferred = class {
	promise;
	resolve;
	reject;
	constructor() {
		this.promise = new Promise((resolve, reject) => {
			this.resolve = resolve;
			this.reject = reject;
		});
	}
};
function createLineSplittingTransform() {
	const decoder = new TextDecoder();
	let leftover = "";
	return new TransformStream({
		transform(chunk, controller) {
			const str = decoder.decode(chunk, { stream: true });
			const parts = (leftover + str).split("\n");
			leftover = parts.pop() || "";
			for (const part of parts) controller.enqueue(part);
		},
		flush(controller) {
			if (leftover) controller.enqueue(leftover);
		}
	});
}
//#endregion
//#region ../../node_modules/.pnpm/react-router@8.3.0_react-dom@19.2.7_react@19.2.7__react@19.2.7/node_modules/react-router/dist/production/vendor/turbo-stream-v2/flatten.js
/**
* react-router v8.3.0
*
* Copyright (c) Remix Software Inc.
*
* This source code is licensed under the MIT license found in the
* LICENSE.md file in the root directory of this source tree.
*
* @license MIT
*/
var TIME_LIMIT_MS = 1;
var getNow = () => Date.now();
var yieldToMain = () => new Promise((resolve) => setTimeout(resolve, 0));
async function flatten(input) {
	const { indices } = this;
	const existing = indices.get(input);
	if (existing) return [existing];
	if (input === void 0) return -7;
	if (input === null) return -5;
	if (Number.isNaN(input)) return -2;
	if (input === Number.POSITIVE_INFINITY) return -6;
	if (input === Number.NEGATIVE_INFINITY) return -3;
	if (input === 0 && 1 / input < 0) return -4;
	const index = this.index++;
	indices.set(input, index);
	const stack = [[input, index]];
	await stringify.call(this, stack);
	return index;
}
async function stringify(stack) {
	const { deferred, indices, plugins, postPlugins } = this;
	const str = this.stringified;
	let lastYieldTime = getNow();
	const flattenValue = (value) => {
		const existing = indices.get(value);
		if (existing) return [existing];
		if (value === void 0) return -7;
		if (value === null) return -5;
		if (Number.isNaN(value)) return -2;
		if (value === Number.POSITIVE_INFINITY) return -6;
		if (value === Number.NEGATIVE_INFINITY) return -3;
		if (value === 0 && 1 / value < 0) return -4;
		const index = this.index++;
		indices.set(value, index);
		stack.push([value, index]);
		return index;
	};
	let i = 0;
	while (stack.length > 0) {
		const now = getNow();
		if (++i % 6e3 === 0 && now - lastYieldTime >= TIME_LIMIT_MS) {
			await yieldToMain();
			lastYieldTime = getNow();
		}
		const [input, index] = stack.pop();
		const partsForObj = (obj) => Object.keys(obj).map((k) => `"_${flattenValue(k)}":${flattenValue(obj[k])}`).join(",");
		let error = null;
		switch (typeof input) {
			case "boolean":
			case "number":
			case "string":
				str[index] = JSON.stringify(input);
				break;
			case "bigint":
				str[index] = `["B","${input}"]`;
				break;
			case "symbol": {
				const keyFor = Symbol.keyFor(input);
				if (!keyFor) error = /* @__PURE__ */ new Error("Cannot encode symbol unless created with Symbol.for()");
				else str[index] = `["Y",${JSON.stringify(keyFor)}]`;
				break;
			}
			case "object": {
				if (!input) {
					str[index] = `-5`;
					break;
				}
				const isArray = Array.isArray(input);
				let pluginHandled = false;
				if (!isArray && plugins) for (const plugin of plugins) {
					const pluginResult = plugin(input);
					if (Array.isArray(pluginResult)) {
						pluginHandled = true;
						const [pluginIdentifier, ...rest] = pluginResult;
						str[index] = `[${JSON.stringify(pluginIdentifier)}`;
						if (rest.length > 0) str[index] += `,${rest.map((v) => flattenValue(v)).join(",")}`;
						str[index] += "]";
						break;
					}
				}
				if (!pluginHandled) {
					let result = isArray ? "[" : "{";
					if (isArray) {
						for (let i = 0; i < input.length; i++) result += (i ? "," : "") + (i in input ? flattenValue(input[i]) : -1);
						str[index] = `${result}]`;
					} else if (input instanceof Date) {
						const dateTime = input.getTime();
						str[index] = `["D",${Number.isNaN(dateTime) ? JSON.stringify("invalid") : dateTime}]`;
					} else if (input instanceof URL) str[index] = `["U",${JSON.stringify(input.href)}]`;
					else if (input instanceof RegExp) str[index] = `["R",${JSON.stringify(input.source)},${JSON.stringify(input.flags)}]`;
					else if (input instanceof Set) if (input.size > 0) str[index] = `["S",${[...input].map((val) => flattenValue(val)).join(",")}]`;
					else str[index] = `["S"]`;
					else if (input instanceof Map) if (input.size > 0) str[index] = `["M",${[...input].flatMap(([k, v]) => [flattenValue(k), flattenValue(v)]).join(",")}]`;
					else str[index] = `["M"]`;
					else if (input instanceof Promise) {
						str[index] = `["P",${index}]`;
						deferred[index] = input;
					} else if (input instanceof Error) {
						str[index] = `["E",${JSON.stringify(input.message)}`;
						if (input.name !== "Error") str[index] += `,${JSON.stringify(input.name)}`;
						str[index] += "]";
					} else if (Object.getPrototypeOf(input) === null) str[index] = `["N",{${partsForObj(input)}}]`;
					else if (isPlainObject(input)) str[index] = `{${partsForObj(input)}}`;
					else error = /* @__PURE__ */ new Error("Cannot encode object with prototype");
				}
				break;
			}
			default: {
				const isArray = Array.isArray(input);
				let pluginHandled = false;
				if (!isArray && plugins) for (const plugin of plugins) {
					const pluginResult = plugin(input);
					if (Array.isArray(pluginResult)) {
						pluginHandled = true;
						const [pluginIdentifier, ...rest] = pluginResult;
						str[index] = `[${JSON.stringify(pluginIdentifier)}`;
						if (rest.length > 0) str[index] += `,${rest.map((v) => flattenValue(v)).join(",")}`;
						str[index] += "]";
						break;
					}
				}
				if (!pluginHandled) error = /* @__PURE__ */ new Error("Cannot encode function or unexpected type");
			}
		}
		if (error) {
			let pluginHandled = false;
			if (postPlugins) for (const plugin of postPlugins) {
				const pluginResult = plugin(input);
				if (Array.isArray(pluginResult)) {
					pluginHandled = true;
					const [pluginIdentifier, ...rest] = pluginResult;
					str[index] = `[${JSON.stringify(pluginIdentifier)}`;
					if (rest.length > 0) str[index] += `,${rest.map((v) => flattenValue(v)).join(",")}`;
					str[index] += "]";
					break;
				}
			}
			if (!pluginHandled) throw error;
		}
	}
}
var objectProtoNames = Object.getOwnPropertyNames(Object.prototype).sort().join("\0");
function isPlainObject(thing) {
	const proto = Object.getPrototypeOf(thing);
	return proto === Object.prototype || proto === null || Object.getOwnPropertyNames(proto).sort().join("\0") === objectProtoNames;
}
//#endregion
//#region ../../node_modules/.pnpm/react-router@8.3.0_react-dom@19.2.7_react@19.2.7__react@19.2.7/node_modules/react-router/dist/production/vendor/turbo-stream-v2/unflatten.js
/**
* react-router v8.3.0
*
* Copyright (c) Remix Software Inc.
*
* This source code is licensed under the MIT license found in the
* LICENSE.md file in the root directory of this source tree.
*
* @license MIT
*/
var globalObj = typeof window !== "undefined" ? window : typeof globalThis !== "undefined" ? globalThis : void 0;
function unflatten(parsed) {
	const { hydrated, values } = this;
	if (typeof parsed === "number") return hydrate.call(this, parsed);
	if (!Array.isArray(parsed) || !parsed.length) throw new SyntaxError();
	const startIndex = values.length;
	for (const value of parsed) values.push(value);
	hydrated.length = values.length;
	return hydrate.call(this, startIndex);
}
function hydrate(index) {
	const { hydrated, values, deferred, plugins } = this;
	let result;
	const stack = [[index, (v) => {
		result = v;
	}]];
	let postRun = [];
	while (stack.length > 0) {
		const [index, set] = stack.pop();
		switch (index) {
			case -7:
				set(void 0);
				continue;
			case -5:
				set(null);
				continue;
			case -2:
				set(NaN);
				continue;
			case -6:
				set(Infinity);
				continue;
			case -3:
				set(-Infinity);
				continue;
			case -4:
				set(-0);
				continue;
		}
		if (hydrated[index]) {
			set(hydrated[index]);
			continue;
		}
		const value = values[index];
		if (!value || typeof value !== "object") {
			hydrated[index] = value;
			set(value);
			continue;
		}
		if (Array.isArray(value)) if (typeof value[0] === "string") {
			const [type, b, c] = value;
			switch (type) {
				case "D":
					set(hydrated[index] = new Date(b));
					continue;
				case "U":
					set(hydrated[index] = new URL(b));
					continue;
				case "B":
					set(hydrated[index] = BigInt(b));
					continue;
				case "R":
					set(hydrated[index] = new RegExp(b, c));
					continue;
				case "Y":
					set(hydrated[index] = Symbol.for(b));
					continue;
				case "S":
					const newSet = /* @__PURE__ */ new Set();
					hydrated[index] = newSet;
					for (let i = value.length - 1; i > 0; i--) stack.push([value[i], (v) => {
						newSet.add(v);
					}]);
					set(newSet);
					continue;
				case "M":
					const map = /* @__PURE__ */ new Map();
					hydrated[index] = map;
					for (let i = value.length - 2; i > 0; i -= 2) {
						const r = [];
						stack.push([value[i + 1], (v) => {
							r[1] = v;
						}]);
						stack.push([value[i], (k) => {
							r[0] = k;
						}]);
						postRun.push(() => {
							map.set(r[0], r[1]);
						});
					}
					set(map);
					continue;
				case "N":
					const obj = Object.create(null);
					hydrated[index] = obj;
					for (const key of Object.keys(b).reverse()) {
						const r = [];
						stack.push([b[key], (v) => {
							r[1] = v;
						}]);
						stack.push([Number(key.slice(1)), (k) => {
							r[0] = k;
						}]);
						postRun.push(() => {
							obj[r[0]] = r[1];
						});
					}
					set(obj);
					continue;
				case "P":
					if (hydrated[b]) set(hydrated[index] = hydrated[b]);
					else {
						const d = new Deferred();
						deferred[b] = d;
						set(hydrated[index] = d.promise);
					}
					continue;
				case "E":
					const [, message, errorType] = value;
					let error = errorType && globalObj && SUPPORTED_ERROR_TYPES.includes(errorType) && errorType in globalObj && typeof globalObj[errorType] === "function" ? new globalObj[errorType](message) : new Error(message);
					hydrated[index] = error;
					set(error);
					continue;
				case "Z":
					set(hydrated[index] = hydrated[b]);
					continue;
				default:
					if (Array.isArray(plugins)) {
						const r = [];
						const vals = value.slice(1);
						for (let i = 0; i < vals.length; i++) {
							const v = vals[i];
							stack.push([v, (v) => {
								r[i] = v;
							}]);
						}
						postRun.push(() => {
							for (const plugin of plugins) {
								const result = plugin(value[0], ...r);
								if (result) {
									set(hydrated[index] = result.value);
									return;
								}
							}
							throw new SyntaxError();
						});
						continue;
					}
					throw new SyntaxError();
			}
		} else {
			const array = [];
			hydrated[index] = array;
			for (let i = 0; i < value.length; i++) {
				const n = value[i];
				if (n !== -1) stack.push([n, (v) => {
					array[i] = v;
				}]);
			}
			set(array);
			continue;
		}
		else {
			const object = {};
			hydrated[index] = object;
			for (const key of Object.keys(value).reverse()) {
				const r = [];
				stack.push([value[key], (v) => {
					r[1] = v;
				}]);
				stack.push([Number(key.slice(1)), (k) => {
					r[0] = k;
				}]);
				postRun.push(() => {
					object[r[0]] = r[1];
				});
			}
			set(object);
			continue;
		}
	}
	while (postRun.length > 0) postRun.pop()();
	return result;
}
//#endregion
//#region ../../node_modules/.pnpm/react-router@8.3.0_react-dom@19.2.7_react@19.2.7__react@19.2.7/node_modules/react-router/dist/production/vendor/turbo-stream-v2/turbo-stream.js
/**
* react-router v8.3.0
*
* Copyright (c) Remix Software Inc.
*
* This source code is licensed under the MIT license found in the
* LICENSE.md file in the root directory of this source tree.
*
* @license MIT
*/
async function decode(readable, options) {
	const { plugins } = options ?? {};
	const done = new Deferred();
	const reader = readable.pipeThrough(createLineSplittingTransform()).getReader();
	const decoder = {
		values: [],
		hydrated: [],
		deferred: {},
		plugins
	};
	const decoded = await decodeInitial.call(decoder, reader);
	let donePromise = done.promise;
	if (decoded.done) done.resolve();
	else donePromise = decodeDeferred.call(decoder, reader).then(done.resolve).catch((reason) => {
		for (const deferred of Object.values(decoder.deferred)) deferred.reject(reason);
		done.reject(reason);
	});
	return {
		done: donePromise.then(() => reader.closed),
		value: decoded.value
	};
}
async function decodeInitial(reader) {
	const read = await reader.read();
	if (!read.value) throw new SyntaxError();
	let line;
	try {
		line = JSON.parse(read.value);
	} catch (reason) {
		throw new SyntaxError();
	}
	return {
		done: read.done,
		value: unflatten.call(this, line)
	};
}
async function decodeDeferred(reader) {
	let read = await reader.read();
	while (!read.done) {
		if (!read.value) continue;
		const line = read.value;
		switch (line[0]) {
			case "P": {
				const colonIndex = line.indexOf(":");
				const deferredId = Number(line.slice(1, colonIndex));
				const deferred = this.deferred[deferredId];
				if (!deferred) throw new Error(`Deferred ID ${deferredId} not found in stream`);
				const lineData = line.slice(colonIndex + 1);
				let jsonLine;
				try {
					jsonLine = JSON.parse(lineData);
				} catch (reason) {
					throw new SyntaxError();
				}
				const value = unflatten.call(this, jsonLine);
				deferred.resolve(value);
				break;
			}
			case "E": {
				const colonIndex = line.indexOf(":");
				const deferredId = Number(line.slice(1, colonIndex));
				const deferred = this.deferred[deferredId];
				if (!deferred) throw new Error(`Deferred ID ${deferredId} not found in stream`);
				const lineData = line.slice(colonIndex + 1);
				let jsonLine;
				try {
					jsonLine = JSON.parse(lineData);
				} catch (reason) {
					throw new SyntaxError();
				}
				const value = unflatten.call(this, jsonLine);
				deferred.reject(value);
				break;
			}
			default: throw new SyntaxError();
		}
		read = await reader.read();
	}
}
function encode(input, options) {
	const { onComplete, plugins, postPlugins, signal } = options ?? {};
	const encoder = {
		deferred: {},
		index: 0,
		indices: /* @__PURE__ */ new Map(),
		stringified: [],
		plugins,
		postPlugins,
		signal
	};
	const textEncoder = new TextEncoder();
	let lastSentIndex = 0;
	return new ReadableStream({ async start(controller) {
		const id = await flatten.call(encoder, input);
		if (Array.isArray(id)) throw new Error("This should never happen");
		if (id < 0) controller.enqueue(textEncoder.encode(`${id}\n`));
		else {
			controller.enqueue(textEncoder.encode(`[${encoder.stringified.join(",")}]\n`));
			lastSentIndex = encoder.stringified.length - 1;
		}
		const seenPromises = /* @__PURE__ */ new WeakSet();
		let processingChain = Promise.resolve();
		if (Object.keys(encoder.deferred).length) {
			let raceDone;
			const racePromise = new Promise((resolve, reject) => {
				raceDone = resolve;
				if (signal) {
					const rejectPromise = () => reject(signal.reason || /* @__PURE__ */ new Error("Signal was aborted."));
					if (signal.aborted) rejectPromise();
					else signal.addEventListener("abort", (event) => {
						rejectPromise();
					});
				}
			});
			while (Object.keys(encoder.deferred).length > 0) {
				for (const [deferredId, deferred] of Object.entries(encoder.deferred)) {
					if (seenPromises.has(deferred)) continue;
					seenPromises.add(encoder.deferred[Number(deferredId)] = Promise.race([racePromise, deferred]).then((resolved) => {
						processingChain = processingChain.then(async () => {
							const id = await flatten.call(encoder, resolved);
							if (Array.isArray(id)) {
								controller.enqueue(textEncoder.encode(`P${deferredId}:[["Z",${id[0]}]]\n`));
								encoder.index++;
								lastSentIndex++;
							} else if (id < 0) controller.enqueue(textEncoder.encode(`P${deferredId}:${id}\n`));
							else {
								const values = encoder.stringified.slice(lastSentIndex + 1).join(",");
								controller.enqueue(textEncoder.encode(`P${deferredId}:[${values}]\n`));
								lastSentIndex = encoder.stringified.length - 1;
							}
						});
						return processingChain;
					}, (reason) => {
						processingChain = processingChain.then(async () => {
							if (!reason || typeof reason !== "object" || !(reason instanceof Error)) reason = /* @__PURE__ */ new Error("An unknown error occurred");
							const id = await flatten.call(encoder, reason);
							if (Array.isArray(id)) {
								controller.enqueue(textEncoder.encode(`E${deferredId}:[["Z",${id[0]}]]\n`));
								encoder.index++;
								lastSentIndex++;
							} else if (id < 0) controller.enqueue(textEncoder.encode(`E${deferredId}:${id}\n`));
							else {
								const values = encoder.stringified.slice(lastSentIndex + 1).join(",");
								controller.enqueue(textEncoder.encode(`E${deferredId}:[${values}]\n`));
								lastSentIndex = encoder.stringified.length - 1;
							}
						});
						return processingChain;
					}).finally(() => {
						delete encoder.deferred[Number(deferredId)];
					}));
				}
				await Promise.race(Object.values(encoder.deferred));
			}
			raceDone();
		}
		await Promise.all(Object.values(encoder.deferred));
		await processingChain;
		controller.close();
		onComplete?.();
	} });
}
//#endregion
//#region ../../node_modules/.pnpm/react-router@8.3.0_react-dom@19.2.7_react@19.2.7__react@19.2.7/node_modules/react-router/dist/production/lib/dom/ssr/single-fetch.js
/**
* react-router v8.3.0
*
* Copyright (c) Remix Software Inc.
*
* This source code is licensed under the MIT license found in the
* LICENSE.md file in the root directory of this source tree.
*
* @license MIT
*/
var SingleFetchRedirectSymbol = Symbol("SingleFetchRedirect");
var NO_BODY_STATUS_CODES = /* @__PURE__ */ new Set([
	100,
	101,
	204,
	205
]);
function StreamTransfer({ context, identifier, reader, textDecoder, nonce }) {
	if (!context.renderMeta || !context.renderMeta.didRenderScripts) return null;
	if (!context.renderMeta.streamCache) context.renderMeta.streamCache = {};
	let { streamCache } = context.renderMeta;
	let promise = streamCache[identifier];
	if (!promise) promise = streamCache[identifier] = reader.read().then((result) => {
		streamCache[identifier].result = {
			done: result.done,
			value: textDecoder.decode(result.value, { stream: true })
		};
	}).catch((e) => {
		streamCache[identifier].error = e;
	});
	if (promise.error) throw promise.error;
	if (promise.result === void 0) throw promise;
	let { done, value } = promise.result;
	let scriptTag = value ? /* @__PURE__ */ import_react.createElement("script", {
		nonce,
		dangerouslySetInnerHTML: { __html: `window.__reactRouterContext.streamController.enqueue(${escapeHtml(JSON.stringify(value))});` }
	}) : null;
	if (done) return /* @__PURE__ */ import_react.createElement(import_react.Fragment, null, scriptTag, /* @__PURE__ */ import_react.createElement("script", {
		nonce,
		dangerouslySetInnerHTML: { __html: `window.__reactRouterContext.streamController.close();` }
	}));
	else return /* @__PURE__ */ import_react.createElement(import_react.Fragment, null, scriptTag, /* @__PURE__ */ import_react.createElement(import_react.Suspense, null, /* @__PURE__ */ import_react.createElement(StreamTransfer, {
		context,
		identifier: identifier + 1,
		reader,
		textDecoder,
		nonce
	})));
}
function singleFetchUrl(reqUrl, extension) {
	let url = typeof reqUrl === "string" ? new URL(reqUrl, typeof window === "undefined" ? "server://singlefetch/" : window.location.origin) : reqUrl;
	if (url.pathname.endsWith("/")) url.pathname = `${url.pathname}_.${extension}`;
	else url.pathname = `${url.pathname}.${extension}`;
	return url;
}
function decodeViaTurboStream(body, global) {
	return decode(body, { plugins: [(type, ...rest) => {
		if (type === "SanitizedError") {
			let [name, message, stack] = rest;
			let Constructor = Error;
			if (name && SUPPORTED_ERROR_TYPES.includes(name) && name in global && typeof global[name] === "function") Constructor = global[name];
			let error = new Constructor(message);
			error.stack = stack;
			return { value: error };
		}
		if (type === "ErrorResponse") {
			let [data, status, statusText] = rest;
			return { value: new ErrorResponseImpl(status, statusText, data) };
		}
		if (type === "SingleFetchRedirect") return { value: { [SingleFetchRedirectSymbol]: rest[0] } };
		if (type === "SingleFetchClassInstance") return { value: rest[0] };
		if (type === "SingleFetchFallback") return { value: void 0 };
	}] });
}
//#endregion
//#region ../../node_modules/.pnpm/react-router@8.3.0_react-dom@19.2.7_react@19.2.7__react@19.2.7/node_modules/react-router/dist/production/lib/dom/ssr/fog-of-war.js
/**
* react-router v8.3.0
*
* Copyright (c) Remix Software Inc.
*
* This source code is licensed under the MIT license found in the
* LICENSE.md file in the root directory of this source tree.
*
* @license MIT
*/
function isFogOfWarEnabled(routeDiscovery, ssr) {
	return routeDiscovery.mode === "lazy" && ssr === true;
}
function getPartialManifest({ sri, ...manifest }, router) {
	let routeIds = new Set(router.state.matches.map((m) => m.route.id));
	let segments = router.state.location.pathname.split("/").filter(Boolean);
	let paths = ["/"];
	segments.pop();
	while (segments.length > 0) {
		paths.push(`/${segments.join("/")}`);
		segments.pop();
	}
	paths.forEach((path) => {
		let matches = matchRoutesImpl(router.routes, path, router.basename || "/", false, router.branches);
		if (matches) matches.forEach((m) => routeIds.add(m.route.id));
	});
	let initialRoutes = [...routeIds].reduce((acc, id) => Object.assign(acc, { [id]: manifest.routes[id] }), {});
	return {
		...manifest,
		routes: initialRoutes,
		sri: sri ? true : void 0
	};
}
function getManifestPath(_manifestPath, basename) {
	let manifestPath = _manifestPath || "/__manifest";
	return basename == null ? manifestPath : joinPaths([basename, manifestPath]);
}
//#endregion
//#region ../../packages/cache/src/adapters/react-router/poll-driver.ts
/**
* Minimum ms between two piggybacked ticks on ONE cache instance. Set to the ladder's fastest rung
* (~1s): ticking faster can never make the poller act sooner (every path it drives is gated by its
* own due-time), so anything below this is pure waste under load.
*/
var TICK_MIN_INTERVAL_MS = 1e3;
/**
* The counter a swallowed tick failure increments. The tick runs OUT-OF-BAND from the response,
* so its failure must never surface to the caller — but a silently failing replication poll is
* exactly the kind of degradation an operator has to be able to see, so the swallow is paired with
* this event. Deliberately payload-free apart from the error's NAME (a code, never free-form detail
* or a key/tag reference), so a plain sink can be pointed at it safely.
*
* NOTE: `registry_degraded` / `state_stale` — the richer telemetry names this event may
* eventually be folded into — are now ENROLLED in the const-union catalog (`TELEMETRY_EVENTS`) as
* RESERVED members, but their emit condition is unruled: `poll_tick_failed` keeps emitting as-is.
* (FOR FABLE: whether to enrich/rename this counter into a degraded-mode signal is an open question.)
*/
var POLL_TICK_FAILED = TELEMETRY_EVENTS.poll_tick_failed;
/** Last piggybacked tick per cache instance — the staleness gate's cursor. Per-isolate by nature
* (an isolate holds one `Cache`), and keyed by the instance so two caches in one isolate never
* starve each other's ticks.
*
* Keyed by RESOLVED identity (the third instance-keyed seam, and the one that does not live beside
* the other two). A facade and the instance it fronts drive the SAME poller, so they must share the
* SAME gate: two gates over one poller would let an app holding both objects tick it twice per
* window, which is exactly the hammering the gate exists to stop. */
var lastTickMs = /* @__PURE__ */ new WeakMap();
/**
* THE SWALLOW WRAP (adapter-side; the guard itself now lives in the shared `safeEmit`).
* `ctx.waitUntil(tick())` on a rejecting promise surfaces as an unhandled rejection in the request's
* lifecycle — a failing L2 read would then be able to reach the response of a request that never
* needed that read. So the tick is wrapped: catch → count → swallow. Adapter code by ruling, not
* kernel: the kernel's `resync` is entitled to reject, and the driver that chose to run it
* out-of-band is what owes the caller its silence.
*
* The returned promise NEVER rejects — and the guarantee is AIRTIGHT over the whole failure surface,
* INCLUDING the reporting channel itself: the telemetry emit is routed through `safeEmit`, so
* neither a throwing sink nor a rejecting `async` sink can turn the swallow into an escape, and the
* injected `onSinkError` hook observes a swallowed sink failure. (The default `waitUntil` drops
* the promise on the floor; a rejection here would therefore become an unhandled rejection —
* process-fatal on the Node/RR lane, an uncaught request error on workerd. A background freshness
* probe must never be able to kill a user's request.)
*/
async function runTick(poller, nowMs, telemetry, onSinkError) {
	try {
		await poller.onPollTick(nowMs);
	} catch (error) {
		safeEmit(telemetry, {
			error: error instanceof Error ? error.name : "unknown",
			type: POLL_TICK_FAILED
		}, onSinkError);
	}
}
/**
* Fire one replication poll tick for `cache` IF the last one is stale (see `TICK_MIN_INTERVAL_MS`).
*
* Returns the in-flight tick — hand it straight to `ctx.waitUntil` so it outlives the response — or
* `undefined` when nothing is due (the gate held, or the cache is registry-absent and has no reader
* to drive). The returned promise NEVER rejects: a store/resync failure inside the tick is caught,
* counted through `telemetry` (`poll_tick_failed`), and swallowed, because a background freshness
* probe has no business failing a user's response. The `telemetry` sink is NOT trusted to keep that
* promise: it is app code on the failure path, so a sink that throws — or an `async` sink that
* rejects — is neutralized too, and floats nothing.
*/
function tickIfStale(cache, nowMs, telemetry, onSinkError) {
	const poller = replicationPollerOf(cache);
	if (poller === void 0) return;
	const gateKey = resolveCacheIdentity(cache);
	const last = lastTickMs.get(gateKey);
	if (last !== void 0 && nowMs - last < 1e3) return;
	lastTickMs.set(gateKey, nowMs);
	return runTick(poller, nowMs, telemetry, onSinkError);
}
//#endregion
//#region app/bounds.ts
/**
* THE HEARTBEAT NUMBER — 30s — which this demo owns at BOTH ends and matches on purpose.
*
* It has one value and two deployment surfaces, and nothing in the system makes them agree:
*
*   • the COORDINATOR end is `REGISTRY_HEARTBEAT_MS` in wrangler `vars` (see wrangler.jsonc). A
*     Worker cannot write a Durable Object's env, so deploy-time config is that end's only seed.
*   • the READER end is `defaults.heartbeatInterval`, which this app passes explicitly rather than
*     inheriting, so the number is written down once, here, and spent twice.
*
* That this demo hosts both ends in one script is packaging, not a mechanism — split the
* Coordinator into its own Worker and only one of the two travels with it. The console prints both
* ends and whether they agree, because "matched by construction" is exactly the kind of claim that
* is comfortable, load-bearing, and false.
*
* A mismatch is not a bug in the safe direction: a reader with the SHORTER interval raises
* suspicion spuriously, which costs live registry checks and never a stale serve. The reverse
* merely raises late. Nothing enforces equality because enforcing it would trade a priced,
* observable degradation for a new failure mode.
*/
var HEARTBEAT_INTERVAL_MS = 3e4;
/** The posture this deployment runs under, and the bounds it therefore documents. */
var POSTURE = {
	/** The heartbeat cadence, both ends. */
	heartbeatIntervalMs: HEARTBEAT_INTERVAL_MS,
	/** The base cadence at which the reader re-reads the mirror pointer, jittered ±20% per poll —
	* THIS DEPLOYMENT'S, turned down from the shipped baseline; see `MAX_SYNC_LAG_MS`. */
	maxSyncLagMs: 5e3,
	/** The shipped baseline this demo overrides, printed beside the one it runs. */
	shippedMaxSyncLagMs: 6e4,
	/** How long OBSERVED silence must run before the reader may raise suspicion. Derivative and
	* raise-only: a live-but-slow writer resets the baseline, and a fast sampler cannot witness 90s of
	* silence in less than 90s. */
	suspicionAfterMs: 3 * HEARTBEAT_INTERVAL_MS,
	/** The floor between request-piggybacked replication ticks — the real exported number, not a
	* restatement. A burst of requests costs at most one tick per second. */
	tickMinIntervalMs: TICK_MIN_INTERVAL_MS
};
/** The sentence every narration carries. It is not decoration. */
var NO_SLO_CAVEAT = "These are bounds this deployment documents, not guarantees it makes. The demo prints what it measured beside what is permitted and asserts nothing about the gap: a single run on one machine is an anecdote, and the measured distributions come from deployed probes, not from this page.";
/**
* THE LIFETIME NOTE — and it now says the opposite of what it used to say.
*
* This demo used to carry a caveat costing it its best scene: the cache was rebuilt on every
* request, because the factory resolved the Coordinator namespace to a stub and HELD it, and a
* Workers stub belongs to the request that minted it — so a cache outliving a request could not
* touch its registry again. The reader was therefore newborn every request, had observed no
* silence, and could never grow suspicious. The page said so, loudly, because the alternative was a
* console showing a system serenely ignoring an outage.
*
* That is fixed. The factory now holds the ADDRESS — a namespace and a name — and mints a
* stub per use, inside whichever request is running. Construction performs no I/O, so the cache is
* built ONCE at module scope (see `workers/app.ts`), one per isolate, which is the placement the
* quickstart teaches. The reader accumulates state across requests: it has a channel state, an
* applied epoch that advances, and silence it can actually observe.
*
* So the caveat that remains is a much smaller one, and it is about the RIG rather than the design:
* the reader's ticks ride on requests (that is the carrier), so nothing is observed on a page nobody
* loads. Detection latency is bounded below by the gap between reloads, not just by the threshold.
*/
var LIFETIME_CAVEAT = "The cache on this page is built once per isolate, at module scope, and outlives every request — so the reader below is the same reader across reloads and its state accumulates. Its freshness ticks ride on requests, which is the honest residue: nothing is observed on a page nobody loads, so detection latency is bounded below by the gap between your reloads and not by the threshold alone. Reload to give it something to observe.";
/** Render a duration the way the console prints it. */
function ms(value) {
	return value >= 1e3 ? `${(value / 1e3).toFixed(1)}s` : `${value}ms`;
}
//#endregion
//#region chaos/dials.ts
/** A fresh, all-zero KV stats record. */
function emptyKvStats() {
	return {
		readsFailed: 0,
		readsPassed: 0,
		writesFailed: 0,
		writesPassed: 0
	};
}
/** A fresh, all-zero bus stats record. */
function emptyBusStats() {
	return {
		dialsPassed: 0,
		dialsRefused: 0
	};
}
/** A fresh set of dials, all faults off — the healthy system. */
function noFaults() {
	return {
		bus: { dialRefused: false },
		kv: {
			failReads: false,
			failWrites: false,
			unreadablePrefix: null
		}
	};
}
/** Turn every dial off, in place (the scenes' `heal` step). */
function clearFaults(dials) {
	dials.kv.failReads = false;
	dials.kv.failWrites = false;
	dials.kv.unreadablePrefix = null;
	dials.bus.dialRefused = false;
}
/**
* Zero the counters, in place — the OTHER half of healing, and the half that was missing.
*
* Healing used to clear the dials and nothing else, which produced a page that contradicted itself
* in three places at once: it said "Not armed. This is the healthy system", and directly underneath
* reported six KV reads rejected as "proof the fault reached the binding", and derived
* `registry_degraded` from an event log that still held every swallowed tick the fault had caused.
* Worse, the judgment LATCHED: `registry_degraded` is derived from the log, the log was never
* cleared, so once a fault had been armed the page claimed a degraded registry forever — including
* on a system that had been healthy for an hour.
*
* The counters are evidence for a CLAIM the page makes in the present tense — that the fault reached
* the binding — so they are scoped to the episode that claim is about. Heal ends the episode. What
* `heal` restores is not merely that the dials are off: it is that the page is once again a page
* about a healthy system, and that is a statement about every number on it.
*/
function clearKvStats(stats) {
	stats.readsFailed = 0;
	stats.readsPassed = 0;
	stats.writesFailed = 0;
	stats.writesPassed = 0;
}
/** Zero the bus counters, in place — see `clearKvStats`. */
function clearBusStats(stats) {
	stats.dialsPassed = 0;
	stats.dialsRefused = 0;
}
//#endregion
//#region chaos/script.ts
/** Every scene id, in the order the console lists them. */
var SCENE_IDS = [
	"backend-kill",
	"bus-drop",
	"registry-outage"
];
/** Is this string one of the three scene ids? The route's `:sceneId` param arrives as a bare
* string, so the boundary that turns it into a `SceneId` is here, once. */
function isSceneId(value) {
	return SCENE_IDS.includes(value);
}
/**
* The replication mirror's key prefix for `registryName`, taken from the library's OWN key formatter
* rather than re-spelled here. The mirror's grammar is the library's to define and to change; a demo
* that hard-coded it would keep "working" after a change by faulting nothing at all — a scene that
* intercepts zero reads is indistinguishable, on screen, from a system that shrugged off the fault,
* and that is the most dishonest thing this demo could do. Deriving it from the formatter means a
* regrammar breaks the demo loudly instead; the wrapper's counters and the scene's own test are the
* belt to this brace.
*/
function mirrorPrefix(registryName) {
	const { stateKey } = mirrorKeys(registryName);
	return stateKey.slice(0, stateKey.length - 5);
}
var SCENES = {
	"backend-kill": {
		arm(dials) {
			dials.kv.failReads = true;
			dials.kv.failWrites = true;
		},
		bootsOwnReader: false,
		id: "backend-kill",
		injects: "the KV binding rejects every read and every write",
		mayHappen: [
			"a failing L2 read may be classified, suppressed, and absorbed into a plain miss — never routed to unknown, never surfaced as an error (E13). This is the divergence: with the store alive that read is a hit, and the whole scene is what the library does INSTEAD of erroring",
			"so every read that misses L1 may become a fill, where the control would have been served from L2 — the origin pays what the durable tier would have. That cost is the trade, and it is what the fault actually causes",
			"reads may be served from L1 — an isolate-local tier the fault never touched, so it behaves exactly as it would with KV alive. The refill lands there and the outage may cost ONE origin call however long it runs; watch the factory counter stop climbing. Nothing here is the dead tier's doing: it is the tier the dead one is not",
			"a fill's write-back may be suppressed rather than fail the caller, and `l2_write_error` may appear in the telemetry row — the control writes back silently and logs nothing, so this row IS the fault",
			"`explain()` may report `read-failed` rather than `absent`: a store that did not answer is not a store that said `nothing`. With KV alive this arm is unreachable, which is what makes it worth a row"
		],
		premise: "the durable tier is the one every isolate shares. When it goes, the question is whether the cache degrades or invents — and the answer costs latency, which is the whole trade.",
		purgeInstruction: null,
		title: "Backend kill — the durable store stops answering"
	},
	"bus-drop": {
		arm(dials) {
			dials.bus.dialRefused = true;
		},
		bootsOwnReader: true,
		id: "bus-drop",
		injects: "the Coordinator's WebSocket dial refuses to connect",
		mayHappen: [
			"THE RIG, STATED: arming this scene boots a reader of its own, minted the instant the dial was set — so its first dial is its first act and it meets the refusal. `never-established` is a property of a reader's LIFETIME, and a page can only show one honestly by owning a lifetime that started under the fault. The isolate's long-lived reader, which every other page here narrates, came up before you armed anything; healing gives it back",
			"that reader's channel may report `never-established` — a declared state, not silence, and the one thing that tells a dead bus from a quiet healthy one (E28)",
			"each failed lazy dial may emit a raw `bus_dial_failed` — the alternative to which is silence",
			"the reader may NOT be suspect, and that is not the contradiction it looks like: suspicion is about a GAP in a channel, and a channel that never came up has no gap. Read the channel row, not the suspicion row",
			"the transport reports the loss and the redialing driver backs off — it does not retry in place, and no timer performs the redial",
			"a purge still arrives with the bus dead — but read WHICH PATH the rescue row names before crediting the poll carrier. Purging from this page teaches this isolate's own reader through `expire()`'s ack, instantly, with every dial refused: that is an own-ack, not a rescue, and the row says so. A carrier rescue needs a reader that did NOT issue the purge, which needs a second isolate this page does not have"
		],
		premise: "invalidation travels over one socket. When it will not open, the reader stops learning — and a reader that does not know it stopped learning is the failure worth preventing. Watch it say so.",
		purgeInstruction: "Purge the tag — then read the rescue row's PATH before crediting anything. This page purges from the same isolate whose reader it watches, so the ack teaches that reader on the spot: an own-ack, not a rescue.",
		title: "Bus drop — the invalidation socket will not open, and the reader says so"
	},
	"registry-outage": {
		arm(dials, { registryName }) {
			dials.bus.dialRefused = true;
			dials.kv.unreadablePrefix = mirrorPrefix(registryName);
		},
		bootsOwnReader: false,
		id: "registry-outage",
		injects: "the Coordinator's dial refuses AND its mirror keyspace stops answering reads",
		mayHappen: [
			"the reader loses both inputs — no frames, no mirror — and its knowledge stops at the last epoch it applied",
			"observed silence past the suspicion threshold may raise suspicion",
			"entries whose validity cannot be established may report `unknown` — and `unknown` may resolve to a live registry check rather than a serve (E9)"
		],
		premise: "the entries are readable and the authority that says whether they are still true is not. Every stale serve in the world begins here.",
		purgeInstruction: "Purge the tag — the invalidation path runs through the machinery this scene broke, so a purge that cannot be recorded fails loudly rather than being quietly lost. That refusal is the scene.",
		title: "Registry outage — the Coordinator is unreachable"
	}
};
/** The scene with this id. */
function sceneOf(id) {
	return SCENES[id];
}
/** Every scene, in console order. */
function allScenes() {
	return SCENE_IDS.map((id) => SCENES[id]);
}
//#endregion
//#region app/world.ts
/**
* End the episode: dials off, counters zeroed, log emptied, scene disarmed, stopwatch dropped, and
* the episode's own reader handed back.
*
* IT LIVES HERE RATHER THAN IN THE WORKER, and that placement is the tooth. The worker is a Workers
* entry — it imports `cloudflare:workers`, so no Node-side test can load it — which means a `heal()`
* written inline there is a `heal()` no test can reach. The tooth beside this file would then be
* testing a copy of the logic that happened to be spelled the same way, and the day someone edited
* the worker's copy the tooth would sit there, green, guarding nothing. So the composition is here,
* the worker calls it, and the test drives the same function the app runs.
*
* TOTAL BY CONSTRUCTION: it takes the whole record and writes every field. `heal()` used to clear
* the dials alone, and the result was a page that called itself healthy while printing a fault's
* counters as live evidence and latching a degraded-registry judgment forever off an event log
* nobody cleared.
*/
function healEpisode(state) {
	clearFaults(state.dials);
	clearKvStats(state.kvStats);
	clearBusStats(state.busStats);
	state.events.length = 0;
	state.armed = null;
	state.pending = null;
	state.rescue = null;
	state.episodeCache = null;
}
/**
* Heal, then arm exactly one scene — the ONE door beside `healEpisode`'s, and it goes through it.
*
* A scene never shows the last one's residue, and that is now true by construction rather than by
* someone remembering to extend a list: this calls the same total reset `heal()` calls. It used to
* call `clearFaults` alone.
*
* THE MINT IS THE DEAF-READER SCENE'S WHOLE RIG, and the ORDER is the rig. A channel that never
* established is a property of a reader's LIFETIME, so a page can only show one honestly by
* owning a lifetime that began under the dead dial. The isolate's long-lived reader came up on the
* first request anyone ever made, long before a fault was armed — its channel reads `established`
* and no amount of arming will change that, which is why "arm, then reload" could never produce the
* state the scene is about. So a scene that declares it gets a reader minted HERE, after its dial is
* set: the next request is that reader's first use, its first dial meets the refusal, and
* `never-established` is the truth rather than a caption. A fresh reader booted under a dead phone
* line is not a fabrication — it is the apparatus, and the page names it.
*/
function armScene(id, state, options) {
	healEpisode(state);
	const scene = sceneOf(id);
	scene.arm(state.dials, { registryName: options.registryName });
	state.episodeCache = scene.bootsOwnReader ? options.mintCache() : null;
	state.armed = {
		atMs: options.nowMs,
		id
	};
}
/**
* Which path can the demo actually establish? Ordered by what it can PROVE, most specific first.
*
* IT READS THE READER'S CHANNEL, NOT A DIAL COUNT, AND THE DIFFERENCE IS THE WHOLE POINT. A
* dial-count delta — no new successful dial across the wait — detects only a socket that came up
* DURING the wait. A socket that opened before the purge and stayed up produces no new dial, so the
* delta is zero, so that rule would print `carrier` — "proven" — over a fully established bus. That
* is a fabrication: evidence that cannot tell the alternatives apart, narrated as though it could.
*
* The channel state eliminates rather than argues. A reader with no channel has nowhere for a frame
* to have arrived, so the bus is ruled OUT and the carrier is what is left. A reader with a channel
* leaves both paths open, and the demo says so out loud instead of picking the flattering one — a
* page whose subject is that unknown stays unknown does not get to make an exception for the
* one row where the truth would be a better story.
*/
function pathOf(pending, channel) {
	if (pending.appliedEpochAfterAck > pending.appliedEpochAtPurge) return "own-ack";
	return channel === "established" ? "indeterminate" : "carrier-proven";
}
/**
* Has a pending purge arrived? Pure, so the demo's one stopwatch is testable without a cache.
*
* The rescue is declared only when the reader's applied epoch has actually MOVED past where it stood
* when the purge was accepted. Epoch movement is the whole proof: it advances only on contiguously
* applied frames, so a reader whose number went up has genuinely learned something rather than
* merely been asked to.
*/
function rescueIfArrived(pending, witness, nowMs) {
	if (pending === null || !witness.ok) return null;
	const { appliedEpoch, channel } = witness.explanation.reader;
	if (appliedEpoch <= pending.appliedEpochAtPurge) return null;
	return {
		afterMs: nowMs - pending.atMs,
		fromEpoch: pending.appliedEpochAtPurge,
		path: pathOf(pending, channel),
		toEpoch: appliedEpoch
	};
}
/** The `RouterContext` the worker seeds and the root middleware reads. Created with no default, so
* a route that runs without the worker having seeded it throws instead of silently finding nothing.
*/
var worldContext = createContext();
/**
* The per-request `waitUntil`, threaded through the router context — NEVER captured on the
* module-scope `world`.
*
* `ExecutionContext.waitUntil` belongs to ONE request: it holds work open past that request's
* response and no other's. The cache is built once per isolate, so a `waitUntil` stored on it
* would be one request's handle reused by every later request — the exact lifetime error the reachability rule exists
* to end. So it rides the request context instead: the worker seeds this from the CURRENT request's
* `ctx.waitUntil` on every `fetch`, and the root middleware reads it here and hands it to
* `cacheMiddleware` per call. A separate default is deliberately omitted — a request that reaches the
* middleware without the worker having seeded it should throw, not silently drop the poll tick's
* lifeline. The middleware's own `waitUntil` default (a floating, unheld tick) is the fallback for
* deployments that wire none; this demo wires one.
*/
var waitUntilContext = createContext();
//#endregion
export { isRouteErrorResponse as $, IDLE_NAVIGATION as A, assertRegId as At, instrumentationResultMetaContext as B, StreamTransfer as C, isStoreWriteError as Ct, escapeHtml as D, safeEmit as Dt, encode as E, TELEMETRY_EVENTS as Et, isMutationMethod as F, createContext as G, RouterContextProvider as H, isRedirectResponse as I, defaultMapRouteProperties as J, createDataFunctionUrl as K, isRedirectStatusCode as L, getStaticContextFromError as M, mirrorKeys as Mt, hasInvalidProtocol as N, IDLE_BLOCKER as O, t as Ot, isDataWithResponseInit as P, isBrowser as Q, isResponse as R, SingleFetchRedirectSymbol as S, isRetriableHttp as St, singleFetchUrl as T, readFrame as Tt, convertRouteMatchToUiMatch as U, ErrorResponseImpl as V, convertRoutesToDataRoutes as W, getResolveToMatches as X, encodePathParam as Y, getRoutePattern as Z, tickIfStale as _, aliasCacheIdentity as _t, worldContext as a, redirect as at, isFogOfWarEnabled as b, managedDependenciesOf as bt, sceneOf as c, replace as ct, noFaults as d, require_react as dt, joinPaths as et, HEARTBEAT_INTERVAL_MS as f, createPath as ft, ms as g, ABSOLUTE_URL_REGEX as gt, POSTURE as h, warning as ht, waitUntilContext as i, parseToInfo as it, createStaticHandler as j, isRegId as jt, IDLE_FETCHER as k, DEFAULT_REG_ID as kt, emptyBusStats as l, resolveTo as lt, NO_SLO_CAVEAT as m, parsePath as mt, healEpisode as n, matchRoutes as nt, allScenes as o, redirectDocument as ot, LIFETIME_CAVEAT as p, invariant as pt, data as q, rescueIfArrived as r, matchRoutesImpl as rt, isSceneId as s, removeTrailingSlash as st, armScene as t, matchPath as tt, emptyKvStats as u, stripBasename as ut, getManifestPath as v, bindRenderCollector as vt, decodeViaTurboStream as w, memory as wt, NO_BODY_STATUS_CODES as x, replicationPollerOf as xt, getPartialManifest as y, createCache as yt, instrumentHandler as z };
