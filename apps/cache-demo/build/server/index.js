import { $ as isRouteErrorResponse, At as assertRegId, B as instrumentationResultMetaContext, Ct as isStoreWriteError, D as escapeHtml, Dt as safeEmit, E as encode$1, Et as TELEMETRY_EVENTS, F as isMutationMethod, H as RouterContextProvider, I as isRedirectResponse, J as defaultMapRouteProperties, K as createDataFunctionUrl, L as isRedirectStatusCode, M as getStaticContextFromError, Mt as mirrorKeys, P as isDataWithResponseInit, R as isResponse, S as SingleFetchRedirectSymbol, St as isRetriableHttp, Tt as readFrame, V as ErrorResponseImpl, Z as getRoutePattern, _t as aliasCacheIdentity, a as worldContext, at as redirect, ct as replace, d as noFaults, f as HEARTBEAT_INTERVAL_MS, h as POSTURE, i as waitUntilContext, j as createStaticHandler, jt as isRegId, kt as DEFAULT_REG_ID, l as emptyBusStats, n as healEpisode, ot as redirectDocument, rt as matchRoutesImpl, st as removeTrailingSlash, t as armScene, u as emptyKvStats, ut as stripBasename, v as getManifestPath, w as decodeViaTurboStream, wt as memory, x as NO_BODY_STATUS_CODES, xt as replicationPollerOf, yt as createCache, z as instrumentHandler } from "./assets/world-DH1WSmWE.js";
import { DurableObject, env } from "cloudflare:workers";
/**
* App-specific WebSocket close codes (the 4000–4999 private range). Each is EXPLICIT so a
* subscriber can tell WHY it was cut and knows the remedy is the same everywhere: redial → the
* `reset` → `hello` handshake → KV resync (cold protocol).
*   - `DELIVERY_DOUBT` — the SERVER closed a subscriber on any delivery doubt (a throwing `send`, a
* non-OPEN socket): doBus never risks a silent gap, it closes.
*   - `SCOPE_MISMATCH` — the CLIENT closed because the DO's `hello` echoed a `regId` ≠ its own
* configured scope: it read a foreign channel, so it fails closed loud.
*   - `PROTOCOL` — the CLIENT closed on an undecodable message (malformed / unknown kind / wrong
* version): never dispatch a partial event, never guess a future version.
*/
var CLOSE_DELIVERY_DOUBT = 4001;
var CLOSE_SCOPE_MISMATCH = 4002;
var CLOSE_PROTOCOL = 4003;
/** Narrow an `unknown` to an index-readable record, or `undefined`. Every field is `typeof`-checked
* at the call site below before use, so the record view is sound. */
function asRecord(value) {
	if (typeof value !== "object" || value === null) return;
	return value;
}
/**
* Encode one wire event to a single WS-message string. The `frame` payload is
* `JSON.stringify(frame)` — the canonical bytes, carried UNCHANGED as a nested JSON string, so
* a frame survives the wire byte-identical (the canonical-bytes teeth pin it). Keys are emitted in
* a fixed order so the envelope bytes are themselves deterministic.
*/
function encode(event) {
	switch (event.kind) {
		case "reset": return JSON.stringify({
			kind: "reset",
			v: 1
		});
		case "hello": return JSON.stringify({
			head: event.head,
			kind: "hello",
			regId: event.regId,
			v: 1
		});
		case "gap": return JSON.stringify({
			head: event.head,
			kind: "gap",
			v: 1
		});
		case "frame": return JSON.stringify({
			frame: JSON.stringify(event.frame),
			kind: "frame",
			v: 1
		});
		default: throw new Error(`unknown wire event: ${String(event)}`);
	}
}
/**
* Decode one WS-message string, STRICTLY. Any of — non-JSON bytes, a non-object, a `v`
* that is not exactly `WIRE_VERSION` (a higher version is NEVER guessed; a lower one is refused
* too), an unknown `kind`, or a malformed payload (a non-number `head`/absent `regId`, or a frame
* the `readFrame` rejects) — yields `{ok:false}`, a CLOSE-and-reconnect signal. A well-formed
* message yields `{ok:true, event}`; the frame is validated through the reader's own `readFrame`,
* so a corrupt wire change fails closed exactly like a corrupt `__delta` byte.
*/
function decode(bytes) {
	let parsed;
	try {
		parsed = JSON.parse(bytes);
	} catch {
		return {
			ok: false,
			reason: "malformed json"
		};
	}
	const env = asRecord(parsed);
	if (!env) return {
		ok: false,
		reason: "envelope is not an object"
	};
	if (env.v !== 1) return {
		ok: false,
		reason: `unsupported wire version ${String(env.v)}`
	};
	switch (env.kind) {
		case "reset": return {
			event: { kind: "reset" },
			ok: true
		};
		case "hello":
			if (typeof env.head !== "number" || typeof env.regId !== "string") return {
				ok: false,
				reason: "malformed hello"
			};
			return {
				event: {
					head: env.head,
					kind: "hello",
					regId: env.regId
				},
				ok: true
			};
		case "gap":
			if (typeof env.head !== "number") return {
				ok: false,
				reason: "malformed gap"
			};
			return {
				event: {
					head: env.head,
					kind: "gap"
				},
				ok: true
			};
		case "frame": {
			if (typeof env.frame !== "string") return {
				ok: false,
				reason: "malformed frame payload"
			};
			const frame = readFrame(env.frame);
			if (!frame) return {
				ok: false,
				reason: "frame failed F12 parse"
			};
			return {
				event: {
					frame,
					kind: "frame"
				},
				ok: true
			};
		}
		default: return {
			ok: false,
			reason: `unknown wire kind ${String(env.kind)}`
		};
	}
}
//#endregion
//#region ../../packages/cache/src/adapters/cloudflare/bus-client.ts
/** Is `value` a thenable (a promise-returning dial vs a synchronous one)? */
function isThenable(value) {
	return typeof value === "object" && value !== null && "then" in value && typeof value.then === "function";
}
var DoBus = class {
	#dial;
	#regId;
	#onClose;
	constructor(dial, options) {
		this.#dial = dial;
		this.#regId = options.regId;
		this.#onClose = options.onClose;
	}
	subscribe(onEvent) {
		const state = {
			closed: false,
			onEvent,
			reported: false,
			socket: null
		};
		const attach = (socket) => {
			if (state.closed) {
				this.#safeSocketClose(socket, 1e3, "astilba/cache: unsubscribed");
				return;
			}
			state.socket = socket;
			socket.addEventListener("message", (event) => {
				this.#onMessage(event.data, onEvent, state);
			});
			socket.addEventListener("close", (event) => {
				this.#report(state, {
					code: event.code,
					local: false,
					reason: event.reason
				});
			});
		};
		const dialed = this.#dial();
		if (isThenable(dialed)) (async () => {
			try {
				attach(await dialed);
			} catch (error) {
				this.#report(state, {
					code: CLOSE_PROTOCOL,
					local: true,
					reason: `astilba/cache: dial failed — ${String(error)}`
				});
			}
		})();
		else attach(dialed);
		return { close: () => {
			if (state.closed) return;
			state.closed = true;
			state.onEvent = null;
			if (state.socket) {
				if (!this.#safeSocketClose(state.socket, 1e3, "astilba/cache: unsubscribed")) return;
			}
			state.socket = null;
		} };
	}
	/** Decode one message and dispatch it, or CLOSE loud on a protocol/scope violation (rulings). */
	#onMessage(data, onEvent, state) {
		if (state.closed) return;
		if (typeof data !== "string") {
			this.#localClose(state, CLOSE_PROTOCOL, "astilba/cache: non-string frame");
			return;
		}
		const result = decode(data);
		if (!result.ok) {
			this.#localClose(state, CLOSE_PROTOCOL, `astilba/cache: undecodable frame — ${result.reason}`);
			return;
		}
		const { event } = result;
		switch (event.kind) {
			case "hello":
				if (event.regId !== this.#regId) {
					this.#localClose(state, CLOSE_SCOPE_MISMATCH, `astilba/cache: hello regId "${event.regId}" != configured "${this.#regId}"`);
					return;
				}
				onEvent({
					head: event.head,
					kind: "hello"
				});
				return;
			case "reset":
				onEvent({ kind: "reset" });
				return;
			case "gap":
				onEvent({
					head: event.head,
					kind: "gap"
				});
				return;
			case "frame":
				onEvent({
					frame: event.frame,
					kind: "frame"
				});
				return;
			default: {
				const unreachable = event;
				this.#localClose(state, CLOSE_PROTOCOL, `astilba/cache: unreachable wire kind ${String(unreachable)}`);
			}
		}
	}
	/** The client initiates a close (protocol/scope) and reports it as `local`. */
	#localClose(state, code, reason) {
		if (state.closed) return;
		state.closed = true;
		if (state.socket) this.#safeSocketClose(state.socket, code, reason);
		this.#report(state, {
			code,
			local: true,
			reason
		});
	}
	/**
	* Fire the status report at most once per subscription, and DECLARE the loss to the subscriber.
	*The declaration is what keeps a dead channel from reading as a quiet one: `reset`/`hello`
	* are the only other things this client dispatches, so without a `lost` the reader's evidence for
	* "the bus never came up" and "the bus has nothing to say" is identical — namely nothing.
	*
	* The subscriber is notified BEFORE the app's `onClose`, and its throw cannot eat the report (nor
	* the reverse): each is guarded separately, because the two consumers are independent and neither
	* one's failure is the other's business.
	*/
	#report(state, info) {
		if (state.reported) return;
		state.reported = true;
		state.closed = true;
		const { onEvent } = state;
		if (onEvent) try {
			onEvent({ kind: "lost" });
		} catch {}
		this.#onClose?.(info);
	}
	/**
	* Close the socket, tolerating a platform refusal — WITHOUT claiming success.
	*
	* The refusal is real and routine: this socket may have been minted in an earlier request, and the
	* platform refuses `close()` from a different one exactly as it refuses every other operation on
	* a foreign handle. Swallowing that and marking the subscription closed anyway was the shape of
	* the lie: local state said "closed", the socket stayed open, and the server kept a subscriber it
	* would fan out to forever.
	*
	* SO THE RETURN VALUE IS THE HONESTY. `false` means the close did NOT happen and the socket is
	* still out there; the caller records that rather than overwriting it with an assumption. This may
	* DEFER a close (the leak below), and that is a cost; it may not claim one that never occurred.
	*
	* THE LEAK'S BOUND, STATED PLAINLY. A socket this cannot close is not unbounded: the server closes
	* on doubt at its next fan-out (a send to a socket whose client is gone fails, and the DO drops
	* it), and isolate eviction takes the rest. So the residue is one socket per refused close, for at
	* most as long as the Coordinator takes to speak again — priced and finite, which a silent
	* divergence between local state and the wire is not.
	*/
	#safeSocketClose(socket, code, reason) {
		try {
			socket.close(code, reason);
			return true;
		} catch {
			return false;
		}
	}
};
/**
* Build a `doBus` client `Bus`. `dial` obtains an OPEN socket (synchronously in the
* conformance pair; a `stub.fetch` → `accept()` promise in workerd). `options.regId` is the scope
* the DO's `hello` must echo; `options.onClose` receives the socket's close (
* NO self-reconnect). The reader subscribes to the returned `Bus`; the client decodes each message
* and dispatches the `BusEvent`, closing loud on any protocol/scope violation.
*/
function doBus(dial, options) {
	return new DoBus(dial, options);
}
//#endregion
//#region ../../packages/cache/src/registry/alarm.ts
/**
* The NEAREST of a currently-armed alarm and a newly-desired deadline. A `null`
* existing means nothing is armed yet, so the desired deadline wins outright. Never pushes an armed
* alarm LATER — the min guarantees a sooner deadline (a due flush) always survives a later one (the
* idle heartbeat cadence), and re-arming with the sooner value is idempotent.
*/
function earliestAlarm(existing, desired) {
	return existing === null ? desired : Math.min(existing, desired);
}
//#endregion
//#region ../../packages/cache/src/registry/execute.ts
/**
* Write an immutable object THROUGH the non-retryable-write belt. The
* executor-side backstop for the delta/snapshot SIZE bound, distinct from the machine's snapshot
* skip: a NON-retryable classified rejection — an oversize immutable (KV `too_large`, 25 MiB, the
* code `cloudflareKV` raises), the pathological bound no retry can clear — emits a
* `flush_failed_nonretryable{code}` telemetry event so the failure is OBSERVABLE, then PROPAGATES
* (the flush fails whole; the terminal `__state` bless never runs, so no pointer references an
* object that was never written). Retryable rejections (`throttled`/`unavailable`) propagate
* UNTOUCHED — the alarm retries them, exactly as before.
*/
async function putImmutableWithBelt(sink, key, bytes, kind) {
	try {
		await sink.putImmutable(key, bytes, kind);
	} catch (error) {
		if (isStoreWriteError(error) && !error.retryable) await sink.telemetry({
			code: error.code,
			event: "flush_failed_nonretryable"
		});
		throw error;
	}
}
/**
* Execute a flush's effect plan against a host sink. The plan already carries
* the once-only terminal `advance-pointer` last, so this never issues two pointer puts in one
* flush. It does NOT catch a sink rejection: a failed `putPointer` fails the flush whole.
*
* `regId` scopes every key through `mirrorKeys(regId)` — the SAME formatter the reader
* addresses with — so the writer and reader can never re-drift to different bytes. The
* host passes its machine's `regId`; that same id is echoed in the pointer payload the machine
* minted, so the physical key and the payload agree by construction.
*/
async function executeFlushEffects(effects, sink, regId) {
	const keys = mirrorKeys(regId);
	for (const effect of effects) switch (effect.kind) {
		case "persist-batch":
			await putImmutableWithBelt(sink, keys.deltaKey(effect.seq), JSON.stringify(effect.frame), "delta");
			break;
		case "persist-snapshot":
			await putImmutableWithBelt(sink, keys.snapKey(effect.seq), JSON.stringify(effect.frame), "snap");
			break;
		case "broadcast-frame":
			await sink.broadcast(effect.frame);
			break;
		case "telemetry":
			await sink.telemetry({
				bytes: effect.bytes,
				ceiling: effect.ceiling,
				event: effect.event
			});
			break;
		case "advance-pointer":
			await sink.putPointer(keys.stateKey, JSON.stringify(effect.pointer));
			break;
		default: throw new Error(`unknown flush effect: ${String(effect)}`);
	}
}
//#endregion
//#region ../../packages/cache/src/registry/machine.ts
/** Default snapshot cadence: checkpoint every 500 batches OR every 6h. */
var DEFAULT_SNAPSHOT_EVERY_BATCHES = 500;
var DEFAULT_SNAPSHOT_INTERVAL_MS = 360 * 60 * 1e3;
/** Default snapshot SIZE ceiling: the KV 25 MiB per-value profile. A due
* snapshot whose canonical shipped bytes exceed this is SKIPPED (never truncated, never a phantom
* bless) — the machine-owned gate above the executor-side delta bound. */
var DEFAULT_MAX_SNAPSHOT_BYTES = 25 * 1024 * 1024;
/**
* Fold a coalescing window's buffered changes into the CANONICAL batch form:
*   - every tag appears EXACTLY ONCE, carrying its max soft/hard epoch over the window
*     (any duplicate tags folded via max-on-write), and
*   - the changes are ordered LEXICOGRAPHICALLY by tag (UTF-16 code-unit order).
* Only an axis actually touched in the window is emitted — an untouched axis has no delta
* over the (fromEpoch, toEpoch] range. Content-addressing makes a batch a pure
* function of durable state over its range, so it has one serialization; the kernel applies
* changes via max-on-write, which is commutative and idempotent, so dedup + lex-sort
* carry the SAME knowledge as the raw concatenation (INV-14/19/20 preserved). The emitted
* key order is alphabetical (hardEpoch, softEpoch, tag) — the repo's stable wire convention —
* so a canonical change has exactly one serialization on every host.
*/
function canonicalBatch(pending) {
	const folded = /* @__PURE__ */ new Map();
	for (const change of pending) {
		const acc = folded.get(change.tag) ?? {};
		if (change.softEpoch !== void 0) acc.softEpoch = Math.max(acc.softEpoch ?? change.softEpoch, change.softEpoch);
		if (change.hardEpoch !== void 0) acc.hardEpoch = Math.max(acc.hardEpoch ?? change.hardEpoch, change.hardEpoch);
		folded.set(change.tag, acc);
	}
	return [...folded.keys()].toSorted().map((tag) => {
		const acc = folded.get(tag) ?? {};
		return {
			...acc.hardEpoch === void 0 ? {} : { hardEpoch: acc.hardEpoch },
			...acc.softEpoch === void 0 ? {} : { softEpoch: acc.softEpoch },
			tag
		};
	});
}
/**
* The registry state machine. Holds dual watermark epochs per tag, a monotone epoch
* counter, a durable batch sequence, the monotone GC-horizon high-water, the snapshot
* cadence watermark (`snapBatch`/`lastSnapMs`), and the un-flushed (coalescing) change
* buffer. Every method is synchronous and side-effect free with respect to the outside
* world: mutations update machine state and return effect descriptions; queries return
* values.
*/
var RegistryMachine = class {
	#watermarks = /* @__PURE__ */ new Map();
	#epoch = 0;
	#batchSeq = 0;
	#gcHorizonMs = 0;
	#pendingFrom = null;
	#pendingChanges = [];
	#snapBatch = 0;
	#lastSnapMs = null;
	#snapshotEveryBatches = DEFAULT_SNAPSHOT_EVERY_BATCHES;
	#snapshotIntervalMs = DEFAULT_SNAPSHOT_INTERVAL_MS;
	#maxSnapshotBytes = DEFAULT_MAX_SNAPSHOT_BYTES;
	#regId = DEFAULT_REG_ID;
	constructor(options = {}) {
		this.configure(options);
	}
	/** The current Coordinator epoch (the correctness clock, machine state). */
	get currentEpoch() {
		return this.#epoch;
	}
	/** The batch seq of the last emitted snapshot (`__state.snapBatch`), machine state. */
	get snapBatch() {
		return this.#snapBatch;
	}
	/** The last durably-emitted batch seq (`lastBatch`), machine state — both the flush and the heartbeat pointer effects carry it; a host may also read it directly. */
	get lastBatch() {
		return this.#batchSeq;
	}
	/** The effective machine config, machine state — the shell diffs `env` against this to decide whether to journal a fresh `configure` (Ruling B). */
	get effectiveConfig() {
		return {
			maxSnapshotBytes: this.#maxSnapshotBytes,
			regId: this.#regId,
			snapshotEveryBatches: this.#snapshotEveryBatches,
			snapshotIntervalMs: this.#snapshotIntervalMs
		};
	}
	/** The mirror-key scoping id, machine state — the host reads it to build the
	* scoped keyspace it writes the flush effects to (`mirrorKeys(regId)`). */
	get regId() {
		return this.#regId;
	}
	/**
	* Apply an effective snapshot cadence as machine STATE. A JOURNALED command (Ruling B):
	* `env` may AUTHOR this — it never parameterizes the machine directly — so a replay reproduces
	* the cadence AT ITS JOURNAL POSITION, prospective by construction. Unset knobs fall to the
	* defaults, exactly as the constructor seed does. Never retroactive: it changes only how future
	* flushes trip the cadence, so `snapBatch` stays monotone across a config change.
	*/
	configure(options) {
		this.#snapshotEveryBatches = options.snapshotEveryBatches ?? DEFAULT_SNAPSHOT_EVERY_BATCHES;
		this.#snapshotIntervalMs = options.snapshotIntervalMs ?? DEFAULT_SNAPSHOT_INTERVAL_MS;
		this.#maxSnapshotBytes = options.maxSnapshotBytes ?? DEFAULT_MAX_SNAPSHOT_BYTES;
		const regId = options.regId ?? "default";
		assertRegId(regId);
		this.#regId = regId;
	}
	/** The monotone high-water GC horizon in ms — never retracts. */
	get gcHorizonMs() {
		return this.#gcHorizonMs;
	}
	/** Authoritative dual watermark for a tag; absent tags read as a zero watermark. */
	watermarkOf(tag) {
		const w = this.#watermarks.get(tag);
		return {
			hardEpoch: w?.hardEpoch ?? 0,
			softEpoch: w?.softEpoch ?? 0
		};
	}
	/**
	* Live watermark knowledge for these tags. A primary read is always authoritative
	* and known; an untouched tag reads as a known zero watermark, never as unknown.
	*/
	knowledgeOf(tags) {
		const through = this.#epoch;
		return tags.map((tag) => {
			const w = this.#watermarks.get(tag);
			return {
				hardEpoch: w?.hardEpoch ?? 0,
				known: true,
				softEpoch: w?.softEpoch ?? 0,
				throughEpoch: through
			};
		});
	}
	/** Soft purge: max-on-write bump of `softEpoch` for each tag. */
	expire(tags, nowMs) {
		return this.#mutate(tags, "soft", nowMs);
	}
	/** Hard purge: max-on-write bump of `hardEpoch` for each tag. */
	delete(tags, nowMs) {
		return this.#mutate(tags, "hard", nowMs);
	}
	/** Register a retention lease; the GC horizon rises to the high-water. */
	registerRetention(retentionMs) {
		this.#gcHorizonMs = Math.max(this.#gcHorizonMs, retentionMs);
	}
	/**
	* Describe the GC horizon at `nowMs`. Pure: the machine collects nothing;
	* a host uses `collectBeforeMs` to decide what to reclaim. Uncertainty extends, never
	* shortens, the horizon — it is the monotone high-water retention.
	*/
	gc(nowMs) {
		return {
			collectBeforeMs: nowMs - this.#gcHorizonMs,
			horizonMs: this.#gcHorizonMs
		};
	}
	/**
	* Flush the coalescing window into a persist-batch + broadcast-frame, plus a
	* persist-snapshot when the cadence is due. Emits nothing when no epoch has advanced
	* since the last flush. The batch takes the next durable seq (`lastBatch + 1`) and spans
	* `(fromEpoch, toEpoch)` — content-addressed so replays are byte-identical. The
	* buffered changes are canonicalized (each tag once at its max epoch, lex-sorted) so the
	* batch has exactly ONE serialization across hosts (`canonicalBatch` — behavior-preserving
	* under max-on-write).
	*
	* SNAPSHOT CADENCE: emit a snapshot when `seq − snapBatch ≥ snapshotEveryBatches`
	* OR `nowMs − lastSnapMs ≥ snapshotIntervalMs`. The seq clause is replay-safe (a pure
	* function of the command journal); the time clause reads the `nowMs` PARAMETER, so a host
	* that replays flushes with the recorded `nowMs` reproduces `snapBatch` exactly.
	*
	* SIZE GATE: before emitting a due snapshot, measure its CANONICAL SHIPPED
	* bytes; if they exceed `maxSnapshotBytes`, SKIP it — emit a `snapshot-skipped-oversize`
	* telemetry effect INSTEAD of `persist-snapshot`, leave `#snapBatch` UNADVANCED (no phantom
	* checkpoint blessed), and let batch/broadcast/pointer proceed normally. `#lastSnapMs` advances
	* so the time clause does not re-fire every flush, but the batch clause stays tripped — a loud
	* re-attempt on each subsequent flush. The gate is journaled (replay-deterministic).
	*
	* Emission order is batch → snapshot → broadcast → advance-pointer, so a host writes every
	* immutable object (`__delta:<seq>`, and `__snap:<seq>` when the cadence fires) BEFORE the
	* ONE terminal `advance-pointer` blesses `__state` (write-then-bless). The pointer is atomic
	* across `lastBatch` AND `snapBatch` — there is no stale intermediate pointer, so no host can
	* issue two sub-second `__state` puts in one flush.
	*/
	flush(nowMs) {
		if (this.#pendingFrom === null) return [];
		const fromEpoch = this.#pendingFrom;
		const toEpoch = this.#epoch;
		const changes = canonicalBatch(this.#pendingChanges);
		this.#pendingFrom = null;
		this.#pendingChanges = [];
		this.#batchSeq += 1;
		const seq = this.#batchSeq;
		const frame = {
			changes,
			fromEpoch,
			toEpoch
		};
		const effects = [{
			frame,
			kind: "persist-batch",
			seq
		}];
		this.#lastSnapMs ??= nowMs;
		const dueByBatches = seq - this.#snapBatch >= this.#snapshotEveryBatches;
		const dueByTime = nowMs - this.#lastSnapMs >= this.#snapshotIntervalMs;
		if (dueByBatches || dueByTime) {
			const snapFrame = {
				changes: this.#snapshotChanges(),
				fromEpoch: 0,
				toEpoch
			};
			const snapBytes = new TextEncoder().encode(JSON.stringify(snapFrame)).length;
			if (snapBytes <= this.#maxSnapshotBytes) {
				this.#snapBatch = seq;
				this.#lastSnapMs = nowMs;
				effects.push({
					frame: snapFrame,
					kind: "persist-snapshot",
					seq
				});
			} else {
				this.#lastSnapMs = nowMs;
				effects.push({
					bytes: snapBytes,
					ceiling: this.#maxSnapshotBytes,
					event: "snapshot-skipped-oversize",
					kind: "telemetry"
				});
			}
		}
		effects.push({
			frame,
			kind: "broadcast-frame"
		});
		const pointer = {
			epoch: toEpoch,
			flushedAt: nowMs,
			lastBatch: seq,
			regId: this.#regId,
			snapBatch: this.#snapBatch
		};
		effects.push({
			kind: "advance-pointer",
			pointer
		});
		return effects;
	}
	/**
	* A liveness heartbeat. Refreshes the `__state` pointer's freshness field
	* (`flushedAt`) at `nowMs` while advancing NO cursor: it reads the CURRENT epoch/lastBatch/
	* snapBatch and reuses the terminal `advance-pointer` effect, so a host blesses `__state`
	* through the SAME executor a flush uses (one hot-key put, never a raw write). Emitted
	* as a SINGLE pointer effect: the freshness fields move, the monotone cursors do not.
	*
	* STATE-NEUTRAL by construction — it mutates nothing, so the monotone cursors cannot move and
	* replaying it is a pure no-op. That is the property that lets a host decline to journal a
	* heartbeat for replay-exactness (replay is exact over the NON-heartbeat commands alone). The
	* shell decides WHEN to issue it (an env-gated cadence, Ruling B); the machine only supplies the
	* pointer bytes — `env` authors the command, it never parameterizes the machine.
	*/
	heartbeat(nowMs) {
		return {
			kind: "advance-pointer",
			pointer: {
				epoch: this.#epoch,
				flushedAt: nowMs,
				lastBatch: this.#batchSeq,
				regId: this.#regId,
				snapBatch: this.#snapBatch
			}
		};
	}
	/**
	* The canonical snapshot change set: every tag that has ever advanced, folded
	* over (0, epoch] by the SAME serializer the delta path uses (`canonicalBatch`) — one entry
	* per tag, touched axes only (a 0 axis is absent, never a fabricated 0), per-axis max,
	* lex-sorted. So a snapshot is byte-identical to the single delta batch that would carry the
	* whole watermark map — one serializer, one applier (the unification).
	*/
	#snapshotChanges() {
		const pending = [];
		for (const [tag, w] of this.#watermarks) pending.push({
			tag,
			...w.hardEpoch > 0 ? { hardEpoch: w.hardEpoch } : {},
			...w.softEpoch > 0 ? { softEpoch: w.softEpoch } : {}
		});
		return canonicalBatch(pending);
	}
	#mutate(tags, kind, nowMs) {
		const fromEpoch = this.#epoch;
		const toEpoch = fromEpoch + 1;
		this.#epoch = toEpoch;
		for (const tag of tags) {
			const w = this.#watermarks.get(tag) ?? {
				hardEpoch: 0,
				softEpoch: 0
			};
			if (kind === "soft") {
				w.softEpoch = Math.max(w.softEpoch, toEpoch);
				this.#pendingChanges.push({
					softEpoch: w.softEpoch,
					tag
				});
			} else {
				w.hardEpoch = Math.max(w.hardEpoch, toEpoch);
				this.#pendingChanges.push({
					hardEpoch: w.hardEpoch,
					tag
				});
			}
			this.#watermarks.set(tag, w);
		}
		this.#pendingFrom ??= fromEpoch;
		return {
			ack: { epoch: toEpoch },
			effects: [{
				deadlineMs: nowMs,
				kind: "schedule-flush"
			}]
		};
	}
};
//#endregion
//#region ../../packages/cache/src/adapters/cloudflare/bus-server.ts
/** WS `OPEN` readyState (the numeric constant workerd and the browser share). */
var WS_OPEN = 1;
/** Close a socket, swallowing any throw (it may already be gone — the subscriber redials + resyncs). */
function safeClose(socket) {
	try {
		socket.close(CLOSE_DELIVERY_DOUBT, "astilba/cache: delivery doubt — reconnect + resync");
	} catch {}
}
/**
* Send one datum to one subscriber, or CLOSE it on ANY delivery doubt: a non-OPEN
* socket, or a `send` that throws. Returns `true` iff the datum was handed to `send` cleanly. Never
* rethrows — a doubtful subscriber is closed and skipped so the fan-out continues to the rest.
*/
function sendOrClose(socket, data) {
	if (socket.readyState !== void 0 && socket.readyState !== WS_OPEN) {
		safeClose(socket);
		return false;
	}
	try {
		socket.send(data);
		return true;
	} catch {
		safeClose(socket);
		return false;
	}
}
/**
* The establishment handshake: emit `reset` THEN `hello{head, regId}` BEFORE any frame, so
* a subscriber starts with warm trust SUSPENDED and a head to reconverge on. `head` is the DO's OWN
* last-flushed epoch — NEVER `machine.currentEpoch` (it over-declares into the
* un-flushed window), NEVER a KV read-back (the mirror is stale-licensed; the DO is the truth
* source). The `hello` echoes `regId` so a mismatched subscriber closes loud.
*/
function establish(socket, head, regId) {
	sendOrClose(socket, encode({ kind: "reset" }));
	sendOrClose(socket, encode({
		head,
		kind: "hello",
		regId
	}));
}
/**
* Fan ONE frame out to every current subscriber (stateless; no replay buffer). The frame
* is encoded ONCE, then delivered per-socket via `sendOrClose`, so a doubtful subscriber is closed
* and the rest still receive it. doBus emits NO `gap` here: the frame is a normal
* delivery, and the only failure mode is a per-socket CLOSE, whose remedy is the handshake.
*/
function fanOut(sockets, frame) {
	const data = encode({
		frame,
		kind: "frame"
	});
	for (const socket of sockets) sendOrClose(socket, data);
}
//#endregion
//#region ../../packages/cache/src/adapters/cloudflare/kv.ts
/**
* KV's per-value ceiling (driver table): 25 MiB. An over-limit `set` is REJECTED
* `too_large` before any I/O — never truncated, never written.
*/
var MAX_VALUE_BYTES = 25 * 1024 * 1024;
/**
* KV's write-side minimum residency (driver table): a `put` whose `expirationTtl`
* is below 60s is silently raised to 60s by KV itself. The driver raises it explicitly
* so the honored TTL is observable and matches what KV would do. Distinct from the
* read-side `cacheTtl` minimum (30s) — a different knob, not conflated here.
*/
var KV_MIN_EXPIRATION_TTL = 60;
/**
* Read-cache `cacheTtl` per mirror-object kind, in SECONDS. The mutable `pointer`
* (`__state`) takes KV's pinned read-cache MINIMUM (30s); the immutable `delta`/`snap` cache hard
* (24h — cache the immutable, never the mutable). An UNHINTED read passes no `cacheTtl` and inherits
* KV's 60s edge-cache default (NOT minimal caching — the Appendix C trap this wiring closes).
*/
var READ_CACHE_TTL_S = {
	delta: 86400,
	pointer: 30,
	snap: 86400
};
/**
* A `StoreWriteError`-shaped rejection raised by the driver. A CLASS (not a bare
* object) so oxlint's throw rules are satisfied; consumers discriminate it
* STRUCTURALLY via `isStoreWriteError`, never by this class's identity, so it
* is never exported. `cause` carries the original KV rejection where there is one.
*/
var KvStoreWriteError = class extends Error {
	code;
	retryable;
	constructor(code, retryable, message, cause) {
		super(message, cause === void 0 ? void 0 : { cause });
		this.name = "KvStoreWriteError";
		this.code = code;
		this.retryable = retryable;
	}
};
/**
* A `StoreReadError`-shaped rejection raised by the driver — the GET-side twin of
* `KvStoreWriteError`, and a CLASS for the same reason (oxlint's throw rules), recognized
* STRUCTURALLY by the kernel (`isStoreReadError`) and never by identity, so it is never
* exported. `cause` carries the original KV rejection.
*/
var KvStoreReadError = class extends Error {
	code;
	retryable;
	constructor(code, retryable, message, cause) {
		super(message, { cause });
		this.name = "KvStoreReadError";
		this.code = code;
		this.retryable = retryable;
	}
};
/**
* Detect a KV rate-limit (429) rejection DEFENSIVELY. KV enforces 1 write/s/key and
* surfaces the ceiling as a 429; workerd throws that as an ordinary `Error` with no
* documented, stable structured field. So probe a few shapes WITHOUT assuming any
* exists — a numeric `status`/`statusCode`/`code` equal to 429, or a message that names
* the 429 / rate-limit condition — and treat everything unrecognized as a transient
* `unavailable` rather than mis-tagging it `throttled`. (Miniflare emits no real 429, so
* this branch is proven by the deterministic conformance fixture, not the integration
* lane — see DIVERGENCES true production 429 is a probe concern.)
*/
function isRateLimited(error) {
	if (typeof error === "object" && error !== null) {
		if ("status" in error && error.status === 429) return true;
		if ("statusCode" in error && error.statusCode === 429) return true;
		if ("code" in error && error.code === 429) return true;
	}
	const message = error instanceof Error ? error.message : "";
	return /\b429\b|too many requests|rate ?limit/iu.test(message);
}
/** Map a rejected KV write to the structural failure contract. */
function classifyWriteError(error) {
	if (isRateLimited(error)) return new KvStoreWriteError("throttled", true, "cloudflareKV: write throttled (KV 1 write/s/key ceiling, 429)", error);
	return new KvStoreWriteError("unavailable", true, "cloudflareKV: write failed (durable store unavailable)", error);
}
/**
* Map a rejected KV read to the structural failure contract. TOTAL, which is the whole
* design: the posture is that an L2 read failure of ANY shape is a miss-class event, so there is
* no rejection this function declines to name. `isRateLimited` is reused verbatim — the same 429 the
* write half recognizes, since it is the same platform saying the same thing — and everything else
* lands on transient `unavailable`.
*
* The totality is not tidiness. A partial classifier would name the shapes someone already thought
* of and drop the rest through the kernel's rethrow to the caller — and the rest is precisely the
* interesting set: the production faults nobody wrote a recognizer for. Read-side, over-naming costs
* an origin refill; under-naming costs the request. There is no read-side `too_large`: a size
* ceiling is a property of what a WRITE offers.
*/
function classifyReadError(error) {
	if (isRateLimited(error)) return new KvStoreReadError("throttled", true, "cloudflareKV: read throttled (KV rate ceiling, 429)", error);
	return new KvStoreReadError("unavailable", true, "cloudflareKV: read failed (durable store unavailable)", error);
}
/** A `Store` backed by a single Cloudflare `KVNamespace` binding. */
var CloudflareKvStore = class {
	#ns;
	constructor(ns) {
		this.#ns = ns;
	}
	async get(key, readKind) {
		const cacheTtl = readKind === void 0 ? void 0 : READ_CACHE_TTL_S[readKind];
		let result;
		try {
			result = await this.#ns.getWithMetadata(key, cacheTtl === void 0 ? void 0 : { cacheTtl });
		} catch (error) {
			throw classifyReadError(error);
		}
		if (result.value === null) return;
		return result.metadata === null ? { value: result.value } : {
			metadata: result.metadata,
			value: result.value
		};
	}
	async set(key, value, options) {
		const sizeBytes = new TextEncoder().encode(value).length;
		if (sizeBytes > MAX_VALUE_BYTES) throw new KvStoreWriteError("too_large", false, `cloudflareKV: value too large (${sizeBytes} > ${MAX_VALUE_BYTES} bytes)`);
		const putOptions = {
			...options?.expirationTtl === void 0 ? {} : { expirationTtl: Math.max(KV_MIN_EXPIRATION_TTL, Math.ceil(options.expirationTtl)) },
			...options?.metadata === void 0 ? {} : { metadata: options.metadata }
		};
		try {
			await this.#ns.put(key, value, putOptions);
		} catch (error) {
			throw classifyWriteError(error);
		}
	}
	async delete(key) {
		await this.#ns.delete(key);
	}
};
/** Build a `Store` over a Cloudflare `KVNamespace` binding. */
function cloudflareKV(ns) {
	return new CloudflareKvStore(ns);
}
//#endregion
//#region ../../packages/cache/src/adapters/cloudflare/reg-id.ts
/**
* Refused: a Coordinator addressed by a Durable Object name that violates the regId
* grammar (`[a-z0-9._-]`, 1–64). Typed so the boot failure is unambiguous and machine-checkable —
* the fix is a conforming name or an explicit `REGISTRY_ID`. Thrown at first boot only (before any
* data), so it strands nothing: validate-and-refuse, not journal-wins. `Error.name` stays the class
* name; `registryName` carries the offending Durable Object name.
*/
var InvalidRegistryNameError = class extends Error {
	registryName;
	constructor(registryName) {
		super(`astilba/cache coordinator: Durable Object name ${JSON.stringify(registryName)} is not a valid registry id — it must match [a-z0-9._-], length 1-64; use a conforming name or set REGISTRY_ID (E19, E19-b)`);
		this.name = "InvalidRegistryNameError";
		this.registryName = registryName;
	}
};
/**
* Resolve a Coordinator's regId from its platform identity. `name` is `ctx.id.name`
* (undefined for an un-named / `idFromString`-reached object); `envRegId` is `env.REGISTRY_ID`.
* Precedence: name ?? env ?? `"default"`. A NAME that violates the grammar REFUSES with a typed
* `InvalidRegistryNameError` (R2). An env/default winner is still asserted through the existing
* `assertRegId` (R3 — every winner passes the same charset gate), so a malformed `REGISTRY_ID` also
* fails loud. An empty-string env value is treated as unset (falls through to `"default"`).
*/
function resolveRegId(name, envRegId) {
	if (name !== void 0) {
		if (!isRegId(name)) throw new InvalidRegistryNameError(name);
		return name;
	}
	const fallback = envRegId !== void 0 && envRegId !== "" ? envRegId : DEFAULT_REG_ID;
	assertRegId(fallback);
	return fallback;
}
//#endregion
//#region ../../packages/cache/src/adapters/cloudflare/coordinator.ts
/** The append-only command journal — the single source of truth for machine state. */
var JOURNAL_KEY = "__journal";
/** The cursor: the epoch through which a batch has been durably accepted into KV. */
var CURSOR_KEY = "kvFlushedEpoch";
/**
* The seeded registry identity — the `regId` this Coordinator journaled at FIRST boot,
* derived from its Durable Object name. Written EXACTLY ONCE (with the first `configure`) and never
* rewritten: it is the durable anchor the divergence detector compares a later name-derivation
* against (R1 — journal-wins). Its presence marks "identity established"; its absence marks a first
* boot. Distinct from `machine.regId` (which a bare cadence-`configure` would default) so the
* seeded id is unambiguous.
*/
var IDENTITY_KEY = "regId";
/**
* KV physical residency, in SECONDS — KV `expirationTtl`. Delta batches age out at
* 48h; content-addressed snapshots at 7d; the `__state` pointer never expires (no TTL). TTL
* is host plumbing, not machine state — invisible to the content-addressed batch/snapshot
* bytes the transcript compares.
*
* @testExport exported solely so tests assert the single source of truth; not public API, no
* production importer — the integration residency scene pins the delta TTL (48h) the sink SELECTS
* (the decision, not KV's Miniflare-blind enforcement).
*/
var DELTA_TTL_S = 2880 * 60;
/**
* @testExport exported solely so tests assert the single source of truth; not public API, no
* production importer — the integration residency scene pins the snapshot TTL (7d) the sink SELECTS.
*/
var SNAP_TTL_S = 10080 * 60;
/**
* @testExport exported solely so tests assert the single source of truth; not public API, no
* production importer — a test pins the ≥1s floored flush window (the coalescing invariant, so a
* lowered tuning term can never breach the platform floor).
*/
var FLUSH_WINDOW_MS = Math.max(1100, 1e3);
/**
* The Coordinator's KV flush sink. `executeFlushEffects` owns the write ORDER and
* the once-only `__state` bless; this sink owns only the KV I/O — routed through the
* `cloudflareKV` Store driver, so a rejected write is CLASSIFIED into the structural
* `StoreWriteError` vocabulary the kernel already speaks — no second classification.
* A `__state` 429 ⇒ `throttled` PROPAGATES out of the flush: nothing was blessed, the immutable
* objects are content-addressed/idempotent, and the alarm retries (no recovery logic needed).
*
* @testExport exported solely so tests assert the single source of truth; not public API, no
* production importer — the integration residency scene constructs it directly to drive the KV
* write-order + `__state`-bless behavior in isolation.
*/
var CoordinatorFlushSink = class {
	store;
	broadcaster;
	constructor(store, broadcaster) {
		this.store = store;
		this.broadcaster = broadcaster;
	}
	async putImmutable(key, bytes, kind) {
		const expirationTtl = kind === "snap" ? SNAP_TTL_S : DELTA_TTL_S;
		await this.store.set(key, bytes, { expirationTtl });
	}
	async putPointer(key, bytes) {
		await this.store.set(key, bytes);
	}
	broadcast(frame) {
		this.broadcaster(frame);
	}
	telemetry(event) {
		console.warn("astilba/cache coordinator flush telemetry", event);
	}
};
var Coordinator = class extends DurableObject {
	#machine;
	#journal = [];
	#regIdDivergence = null;
	/**
	* The name/journal regId divergence detected at this instance's boot, or null when the
	* name still derives the journaled identity. A read-only boot diagnostic (surfaced to the
	* integration divergence scene via `runInDurableObject`); never consulted on a hot path.
	*/
	get regIdDivergence() {
		return this.#regIdDivergence;
	}
	/** Strong read: live, un-memoized, primary-only — the DO IS the primary. */
	async check(tags) {
		return await this.ctx.blockConcurrencyWhile(async () => {
			return (await this.#load()).knowledgeOf(tags);
		});
	}
	/** Soft purge: max-on-write bump of `softEpoch`. */
	async expire(tags) {
		return await this.ctx.blockConcurrencyWhile(async () => await this.#mutate("expire", tags));
	}
	/** Hard purge: max-on-write bump of `hardEpoch`. */
	async delete(tags) {
		return await this.ctx.blockConcurrencyWhile(async () => await this.#mutate("delete", tags));
	}
	/** Register a retention lease; the GC horizon rises to the high-water. */
	async registerRetention(retentionMs) {
		await this.ctx.blockConcurrencyWhile(async () => {
			(await this.#load()).registerRetention(retentionMs);
			this.#journal.push({
				op: "registerRetention",
				retentionMs
			});
			await this.#persist({ [JOURNAL_KEY]: this.#journal });
		});
	}
	/**
	* The doBus live-channel endpoint. A subscriber opens a WebSocket here; the DO
	* accepts it HIBERNATABLY (no per-subscriber attachment, ZERO replay state) and runs
	* the establishment handshake: `reset` → `hello{head, regId}`, BEFORE any frame. `head`
	* is the DO's OWN last-flushed epoch (`#flushedEpoch`) and the `hello` echoes `regId`,
	*so a mismatched subscriber closes loud. Serialized under `blockConcurrencyWhile` so the
	* handshake snapshots a consistent (cursor, regId) against any in-flight mutation.
	*/
	async fetch(request) {
		if (request.headers.get("Upgrade") !== "websocket") return new Response("astilba/cache coordinator: expected a WebSocket upgrade", { status: 426 });
		const { 0: client, 1: server } = new WebSocketPair();
		await this.ctx.blockConcurrencyWhile(async () => {
			this.ctx.acceptWebSocket(server);
			const machine = await this.#load();
			const head = await this.#flushedEpoch();
			establish(server, head, machine.regId);
		});
		return new Response(null, {
			status: 101,
			webSocket: client
		});
	}
	/**
	* Hibernatable WebSocket message handler. The doBus client is RECEIVE-ONLY —
	* it dials, decodes, and dispatches; it never sends. Any inbound message is unexpected and there
	* is no per-subscriber state to update, so it is ignored. Defined so the hibernation runtime has a
	* message handler for woken sockets.
	*/
	webSocketMessage(_ws, _message) {}
	/**
	* Hibernatable WebSocket close handler. Stateless fan-out holds ZERO
	* per-subscriber state, so a closed socket needs no reclamation — a later `getWebSockets()` simply
	* will not include it. Defined so the hibernation runtime has a close handler for woken sockets.
	*/
	webSocketClose(_ws, _code, _reason, _wasClean) {}
	/** Hibernatable WebSocket error handler — best-effort close; no state to reclaim. */
	webSocketError(ws, _error) {
		try {
			ws.close(1011, "astilba/cache coordinator: socket error");
		} catch {}
	}
	/**
	* The DO alarm handler, serving BOTH the flush window and the heartbeat cadence off the one
	* alarm slot. A PURE function of durable state, idempotent under at-least-once
	* delivery + redeploy: it rehydrates the machine from the journal and either FLUSHES the
	* coalescing window `(kvFlushedEpoch, epoch]` into one content-addressed batch (advancing the
	* durable cursor ONLY after KV accepts), or — on an EMPTY window — refreshes the `__state`
	* freshness via a state-neutral heartbeat. A redelivery before the cursor advances re-writes the
	* identical (content-addressed, write-once) bytes; a redelivery after finds an empty window and
	* heartbeats (or no-ops when the cadence is unset).
	*/
	async alarm() {
		await this.ctx.blockConcurrencyWhile(async () => {
			const machine = await this.#load();
			const nowMs = Date.now();
			const effects = machine.flush(nowMs);
			if (effects.length > 0) {
				await executeFlushEffects(effects, new CoordinatorFlushSink(this.#kvStore(), (frame) => {
					this.#fanOut(frame);
				}), machine.regId);
				this.#journal.push({
					nowMs,
					op: "flush"
				});
				await this.#persist({
					[JOURNAL_KEY]: this.#journal,
					[CURSOR_KEY]: machine.currentEpoch
				});
			} else await this.#heartbeat(machine, nowMs);
			await this.#armHeartbeatAlarm(nowMs);
		});
	}
	async #mutate(op, tags) {
		const machine = await this.#load();
		const nowMs = Date.now();
		const { ack, effects } = op === "expire" ? machine.expire(tags, nowMs) : machine.delete(tags, nowMs);
		this.#journal.push({
			op,
			tags
		});
		await this.#persist({ [JOURNAL_KEY]: this.#journal });
		for (const schedule of effects) await this.#armFlushAlarm(schedule.deadlineMs);
		return ack;
	}
	/**
	* The KV mirror as a `Store` — the flush write path goes THROUGH the `cloudflareKV`
	* driver (not raw `put`), so a rejected write is classified into the structural
	* `StoreWriteError` vocabulary the kernel already speaks, and the sink stays a
	* dumb key/bytes writer. Built per flush off the LIVE `env.REGISTRY_KV` binding.
	*/
	#kvStore() {
		return cloudflareKV(this.env.REGISTRY_KV);
	}
	/**
	* The DO's OWN last-flushed epoch — the doBus `hello` head. It is the durable
	* `kvFlushedEpoch` cursor the last pointer bless advanced (the value that pointer carried), read
	* from journaled DO state. NEVER `machine.currentEpoch` (it over-declares into the un-flushed
	* `(kvFlushedEpoch, epoch]` window, stranding a caught-up subscriber suspect on a purge not yet in
	* KV), and NEVER a KV read-back (the mirror is stale-licensed; the DO is the truth source). Absent
	* (a cold DO before any flush) ⇒ 0.
	*/
	async #flushedEpoch() {
		return await this.ctx.storage.get(CURSOR_KEY) ?? 0;
	}
	/**
	* Stateless fan-out of one replication frame to every hibernatable subscriber.
	* Reads the CURRENT sockets via `getWebSockets()` (post-wake-safe) and delivers the frame once
	* each; a doubtful subscriber is CLOSED, never blocking the rest. No replay buffer, no
	* per-subscriber state — `reset` → `hello` + the reader's KV resync is the catch-up. doBus emits
	* NO server `gap`.
	*/
	#fanOut(frame) {
		fanOut(this.ctx.getWebSockets(), frame);
	}
	/**
	* B5 fixed-deadline alarm: arm at the next window boundary, EARLIEST-WINS. Sets the
	* alarm to the NEAREST of any currently-armed deadline (possibly the heartbeat cadence) and
	* this flush's window boundary. Two properties, one `min`: a pending heartbeat NEVER starves the
	* flush (the honest ≤1.1s bound holds — the defect the old `getAlarm() !== null ⇒ return` guard
	* caused once a far heartbeat deadline was armed), and an already-armed EARLIER flush deadline is
	* never pushed forward (`setAlarm` overrides, so a naive re-arm would slide the deadline out and
	* starve the flush — platform note).
	*/
	async #armFlushAlarm(nowMs) {
		const deadline = (Math.floor(nowMs / FLUSH_WINDOW_MS) + 1) * FLUSH_WINDOW_MS;
		const existing = await this.ctx.storage.getAlarm();
		await this.ctx.storage.setAlarm(earliestAlarm(existing, deadline));
	}
	/**
	* Issue the machine's state-neutral liveness heartbeat: refresh `__state.flushedAt` through the SAME
	* shared executor + sink a flush uses — the executor is the ONLY `__state` writer, so there is no
	* raw put. A no-op when the cadence is UNSET (the default in every test lane bar the
	* heartbeat suite), so an idle alarm there blesses nothing. The pointer carries the current
	* epoch/lastBatch/snapBatch UNCHANGED — nothing durable moves, so there is no journal append.
	*/
	async #heartbeat(machine, nowMs) {
		if (this.#heartbeatIntervalMs() === null) return;
		await executeFlushEffects([machine.heartbeat(nowMs)], new CoordinatorFlushSink(this.#kvStore(), (frame) => {
			this.#fanOut(frame);
		}), machine.regId);
	}
	/**
	* Re-arm the heartbeat cadence, EARLIEST-WINS. Sets the alarm to the NEAREST of the
	* currently-armed deadline and `nowMs + interval`, so a pending B5 flush deadline is never pushed
	* out (unlike the old unconditional `setAlarm(now + interval)` last-writer-wins override). No-op
	* when the cadence is unset — the heartbeat path stays fully dormant there.
	*/
	async #armHeartbeatAlarm(nowMs) {
		const intervalMs = this.#heartbeatIntervalMs();
		if (intervalMs === null) return;
		const existing = await this.ctx.storage.getAlarm();
		await this.ctx.storage.setAlarm(earliestAlarm(existing, nowMs + intervalMs));
	}
	/**
	* The heartbeat interval in ms from the config var (Ruling B): `env` decides WHEN the
	* shell issues a heartbeat command, never a machine parameter. `null` when unset or malformed —
	* the whole heartbeat path stays dormant (the default in every test lane bar the heartbeat suite).
	*/
	#heartbeatIntervalMs() {
		const configured = this.env.REGISTRY_HEARTBEAT_MS;
		if (configured === void 0) return null;
		const intervalMs = Number(configured);
		if (!Number.isFinite(intervalMs) || intervalMs <= 0) return null;
		return intervalMs;
	}
	/**
	* Derive this Coordinator's regId from its platform identity: identity = address. The
	* DO's OWN name (`ctx.id.name`, the string clients address it by) wins; `env.REGISTRY_ID` is the
	* DEMOTED fallback for an un-named object, then `"default"`. A NAME violating the grammar
	* REFUSES loud (R2 — a typed `InvalidRegistryNameError`). Called only from `#establishConfig`.
	*/
	#deriveRegId() {
		return resolveRegId(this.ctx.id.name, this.env.REGISTRY_ID);
	}
	/**
	* Establish identity + cadence at boot (R1 + Ruling B). Two concerns, one seam:
	*
	* IDENTITY (R1 — journal-wins, never re-journal). regId derives from the DO name (`#deriveRegId`,
	* which REFUSES an invalid name — R2). At FIRST boot (no `IDENTITY_KEY`) it is journaled ONCE,
	* folded into the first `configure` with the deployed cadence, and recorded durably. On a LATER
	* boot regId is NEVER re-derived-and-rejournaled: this is a pure DIVERGENCE DETECTOR — if the name
	* now derives a DIFFERENT id than the journaled one, emit `regid_divergence` telemetry and KEEP
	* the journaled identity. Divergence cannot serve dead (both replication channels fail closed on
	* identity mismatch — the pointer's regId echo hello's regId echo ); it costs
	* misses/checks/telemetry, never a stale serve. Migration to a new identity = addressing a NEW
	* name (fresh journal, fresh keyspace), never a rekey of the live one.
	*
	* A name-seeded DO can NEVER diverge from itself: name-first precedence makes the journaled id
	* EQUAL the name at first boot, and the name is the address, so a steady-state named object always
	* re-derives its own journaled id. A `regid_divergence` in the field therefore means one of exactly
	* two things — legacy env-seeded data (journaled before), or a fallback-seeded (`newUniqueId`)
	* object whose env/default seed later disagrees — never a healthy name-addressed registry.
	*
	* CADENCE (Ruling B, unchanged in spirit). `env` may only AUTHOR a journaled `configure`; it never
	* parameterizes the machine directly. A deployed cadence differing from the journal's effective
	* config journals a fresh `configure` — PROSPECTIVE, at a definite journal position, replay-exact.
	* Crucially it carries the JOURNALED regId forward, so a cadence change NEVER rekeys the mirror
	* (the orphaning defect the old regId-in-the-diff path would cause — R1). Idempotent: a boot whose
	* cadence already matches writes nothing.
	*/
	async #establishConfig(machine) {
		const derived = this.#deriveRegId();
		const seeded = await this.ctx.storage.get(IDENTITY_KEY);
		const cadence = this.#cadenceOptionsFromEnv();
		if (seeded === void 0) {
			const options = {
				...cadence,
				regId: derived
			};
			machine.configure(options);
			this.#journal.push({
				op: "configure",
				options
			});
			await this.#persist({
				[IDENTITY_KEY]: derived,
				[JOURNAL_KEY]: this.#journal
			});
			this.#regIdDivergence = null;
			return;
		}
		if (derived === seeded) this.#regIdDivergence = null;
		else {
			this.#regIdDivergence = {
				journaled: seeded,
				name: derived
			};
			console.warn("astilba/cache coordinator regid divergence", {
				event: TELEMETRY_EVENTS.regid_divergence,
				journaled: seeded,
				name: derived
			});
		}
		const desired = new RegistryMachine();
		desired.configure(cadence);
		const current = machine.effectiveConfig;
		const want = desired.effectiveConfig;
		if (current.snapshotEveryBatches === want.snapshotEveryBatches && current.snapshotIntervalMs === want.snapshotIntervalMs && current.maxSnapshotBytes === want.maxSnapshotBytes) return;
		const options = {
			...cadence,
			regId: seeded
		};
		machine.configure(options);
		this.#journal.push({
			op: "configure",
			options
		});
		await this.#persist({ [JOURNAL_KEY]: this.#journal });
	}
	/**
	* Parse the snapshot CADENCE knobs from config vars — an empty object ⇒ the machine's
	* 500-batch / 6h / 25 MiB defaults. Only well-formed values are forwarded
	* (exactOptionalPropertyTypes: no `undefined` pass-through). The scoping `regId` is NOT parsed
	* here (it derives from the DO name via `#deriveRegId`, journal-wins) — this returns
	* cadence-only options that `#establishConfig` combines with the journaled regId to author a
	* `configure` (Ruling B). Never fed to the machine constructor on rehydration.
	*/
	#cadenceOptionsFromEnv() {
		const options = {};
		const everyBatches = Number(this.env.REGISTRY_SNAPSHOT_EVERY_BATCHES);
		if (Number.isFinite(everyBatches) && everyBatches > 0) options.snapshotEveryBatches = everyBatches;
		const intervalMs = Number(this.env.REGISTRY_SNAPSHOT_INTERVAL_MS);
		if (Number.isFinite(intervalMs) && intervalMs > 0) options.snapshotIntervalMs = intervalMs;
		const maxSnapshotBytes = Number(this.env.REGISTRY_MAX_SNAPSHOT_BYTES);
		if (Number.isFinite(maxSnapshotBytes) && maxSnapshotBytes > 0) options.maxSnapshotBytes = maxSnapshotBytes;
		return options;
	}
	/**
	* Rebuild the hosted machine from durable state on first touch / after a redeploy. Replays the
	* journal through a FRESH machine (the journal is the SINGLE durable truth — cadence + identity
	* included, via `configure` commands), discarding the replayed effects (state reconstruction only
	* — no re-write to KV, no re-broadcast). Then establishes config (`#establishConfig`): seeds the
	* Identity once / detects its divergence, and reconciles the cadence into a fresh
	* `configure` if the deployed cadence changed (Ruling B). Serialized by the caller's
	* `blockConcurrencyWhile`, so two events never rehydrate concurrently.
	*/
	async #load() {
		if (this.#machine !== void 0) return this.#machine;
		const journal = await this.ctx.storage.get(JOURNAL_KEY) ?? [];
		const machine = new RegistryMachine();
		for (const entry of journal) switch (entry.op) {
			case "expire":
				machine.expire(entry.tags, 0);
				break;
			case "delete":
				machine.delete(entry.tags, 0);
				break;
			case "registerRetention":
				machine.registerRetention(entry.retentionMs);
				break;
			case "configure":
				machine.configure(entry.options);
				break;
			case "flush":
				machine.flush(entry.nowMs ?? 0);
				break;
			default: throw new Error(`unknown journal entry: ${String(entry)}`);
		}
		this.#journal = journal;
		this.#machine = machine;
		await this.#establishConfig(machine);
		return machine;
	}
	/**
	* Persist durable state, aborting the instance on any storage failure so a partial
	* write can never leave the in-memory machine ahead of the journal: the next event
	* rehydrates from the last consistent durable state (crash-consistency).
	*/
	async #persist(entries) {
		try {
			await this.ctx.storage.put(entries);
		} catch (error) {
			this.ctx.abort("coordinator: durable persist failed");
			throw error;
		}
	}
};
//#endregion
//#region ../../packages/cache/src/adapters/cloudflare/do-registry.ts
/** A `Registry` backed by a Coordinator Durable Object, reached over its RPC stub. */
var DoRegistry = class {
	#source;
	/** The mirror-key scope the reader resyncs under. Defaults to `"default"`; it MUST equal the
	* Coordinator's regId so the client-side reader addresses the same keyspace the DO writes. Under
	* that regId IS the Coordinator's Durable Object NAME (identity = address) — so a caller who
	* addresses the DO via `idFromName(name)` passes that SAME `name` here. (C4 ledger: the
	* request-piggyback golden path should take ONE registry `name` and derive BOTH the DO stub —
	* `env.COORDINATOR.get(idFromName(name))` — AND this `regId` from it, so the address and the scope
	* cannot drift.) */
	regId;
	constructor(source, regId) {
		this.#source = source;
		this.regId = regId;
	}
	/** The stub for THIS call. A thunk source is invoked per use — never memoized, which is the
	* entire point: a cached stub is a stub minted in some earlier request, and the platform refuses
	* it. Minting is cheap (it is address arithmetic, not a connection), so there is nothing to save
	* by holding one and a working cache to lose. A bare stub source is returned as-is: the caller
	* chose to own the lifetime, and the signature says they may. */
	get #stub() {
		return typeof this.#source === "function" ? this.#source() : this.#source;
	}
	async check(tags) {
		return await this.#stub.check(tags);
	}
	async expire(tags) {
		return await this.#stub.expire(tags);
	}
	async delete(tags) {
		return await this.#stub.delete(tags);
	}
	async registerRetention(retentionMs) {
		await this.#stub.registerRetention(retentionMs);
	}
};
/** Build a `Registry` from a Coordinator DO stub OR a thunk that mints one (`StubSource` —
* the widening is additive, so `doRegistry(stub, regId)` remains exactly as signed). `regId`
* must match the Coordinator's regId — which is its Durable Object NAME
* (identity = address) — so the client-side reader resyncs the same scoped keyspace the DO writes;
* defaults to `"default"` for single-registry (zero-config) deployments.
*
* Pass a THUNK from anything that outlives a request (`createWorkersCache` does); pass a bare stub
* when the caller mints and uses within one. */
function doRegistry(source, regId = DEFAULT_REG_ID) {
	return new DoRegistry(source, regId);
}
//#endregion
//#region ../../packages/cache/src/adapters/cloudflare/redial.ts
/** First redial delay (≈ the ladder's fastest rung). */
var DEFAULT_BASE_DELAY_MS = 1e3;
/** Redial backoff ceiling — the same 300s cap the poll-failure backoff uses. */
var MAX_REDIAL_DELAY_MS = 3e5;
/** ×2 per consecutive failed establishment. */
var REDIAL_BACKOFF_FACTOR = 2;
/** Symmetric jitter fraction (±20%), drawn from the injected rng. */
var DEFAULT_JITTER_FRACTION = .2;
/**
* A `Bus` that keeps ONE live doBus subscription up (driver duty), redialing
* LAZILY. Wraps the mechanism-only client: every close schedules a backed-off redial that the
* next `tick` performs, and the app's `onEvent` sees an uninterrupted event stream across sockets
* (each new socket re-handshakes `reset` → `hello`, which is exactly the signal the kernel reader
* needs to re-resync — the same events it would see on a fresh subscribe).
*/
var RedialingBus = class {
	#dial;
	#options;
	#baseDelayMs;
	#jitterFraction;
	/** The live inner subscription, or `null` while the channel is down. */
	#inner = null;
	/** The consecutive-failure count the backoff ladder rides on. */
	#attempt = 0;
	/** `true` once `close()` is called: no tick may redial afterwards. */
	#closed = false;
	/** The instant a redial becomes due, or `null` when the channel is up (nothing to redial) or has
	* never been dialed. Written when a close is REPORTED; consumed by `tick`. */
	#dueAtMs = null;
	/** The subscriber, held so a redial can re-attach the SAME consumer to a new socket. */
	#onEvent = null;
	/** The most recent `nowMs` a tick supplied — this driver's ONLY clock (`null` before the first
	* tick). See `#backoffFrom` for why a close is dated from it. */
	#lastTickMs = null;
	constructor(dial, options) {
		this.#dial = dial;
		this.#options = options;
		this.#baseDelayMs = options.baseDelayMs ?? DEFAULT_BASE_DELAY_MS;
		this.#jitterFraction = options.jitterFraction ?? DEFAULT_JITTER_FRACTION;
	}
	subscribe(onEvent) {
		this.#onEvent = onEvent;
		this.#connect();
		return { close: () => {
			this.#closed = true;
			this.#dueAtMs = null;
			this.#onEvent = null;
			this.#inner?.close();
			this.#inner = null;
		} };
	}
	/**
	* Perform a DUE redial — called from a request context, which is what makes the dial legal.
	* A no-op when the channel is up, when nothing is due yet, or after `close()`. Cheap and
	* idempotent, so a carrier may call it on every tick without gating.
	*/
	tick(nowMs) {
		this.#lastTickMs = nowMs;
		if (this.#closed || this.#dueAtMs === null || nowMs < this.#dueAtMs) return;
		this.#dueAtMs = null;
		this.#connect();
	}
	/** Open one socket and wire it. The close handler SCHEDULES the next attempt; it never dials. */
	#connect() {
		if (this.#closed || this.#onEvent === null) return;
		const onEvent = this.#onEvent;
		this.#inner = doBus(this.#dial, {
			onClose: (info) => {
				this.#onSocketClosed(info);
			},
			regId: this.#options.regId
		}).subscribe((event) => {
			if (event.kind === "hello") this.#attempt = 0;
			onEvent(event);
		});
	}
	/** A socket closed: count it, schedule the next attempt, and pass the report through. */
	#onSocketClosed(info) {
		this.#inner = null;
		if (!this.#closed) {
			this.#attempt += 1;
			this.#dueAtMs = this.#backoffFrom() + this.#jitter(this.#delayFor(this.#attempt));
		}
		this.#options.onClose?.(info);
	}
	/**
	* The instant a backoff is measured from. This driver is handed time ONLY by `tick`, and a socket
	* dies whenever it dies — between ticks, with no `nowMs` in scope. So a close is dated from the
	* last tick observed.
	*
	* THE RESIDUE, PRICED: the last tick is at most one tick-cadence old, so a rung can be served up
	* to that much early. It is bounded (never "arbitrarily stale" — every tick refreshes it) and it
	* errs toward redialing SOONER, which is the direction that costs a dial rather than a stale
	* serve. Against the rungs that matter — the cap is 300s, the cadence is ~1s — it is noise.
	*
	* Before the first tick there is no observation at all, so `0` stands in: the due-time becomes
	* "the delay, counted from the epoch", which every real `nowMs` already exceeds, so the first tick
	* after an early close redials at once. That is the right answer for the case it covers — a dial
	* that failed during the very first request, where a prompt retry is exactly what is wanted.
	*/
	#backoffFrom() {
		return this.#lastTickMs ?? 0;
	}
	/** The un-jittered delay for the nth consecutive failed establishment: base × 2^(n−1), cap 300s. */
	#delayFor(attempt) {
		const grown = this.#baseDelayMs * REDIAL_BACKOFF_FACTOR ** Math.max(0, attempt - 1);
		return Math.min(grown, MAX_REDIAL_DELAY_MS);
	}
	/** Jitter a delay symmetrically by ±`jitterFraction` off the injected rng — a fleet whose
	* Coordinator just restarted must not redial in lockstep. rng.next() ∈ [0,1) ⇒ factor ∈ [1−f, 1+f). */
	#jitter(baseMs) {
		const f = this.#jitterFraction;
		return baseMs * (1 - f + 2 * f * this.#options.rng.next());
	}
};
/**
* Build a self-redialing doBus `Bus`. This is the bus `createWorkersCache` wires: the
* transport reports its close, and this driver owns the reconnect + the ×2/300s jittered
* backoff — LAZILY, performed by `tick` inside a request context rather than by a timer that would
* be refused at global scope and would race a dead isolate everywhere else. Use the bare `doBus`
* instead when the caller drives redials itself (the integration lane does — tests own time).
*/
function redialingDoBus(dial, options) {
	return new RedialingBus(dial, options);
}
//#endregion
//#region ../../packages/cache/src/adapters/cloudflare/workers-cache.ts
/**
* The safe posture `createWorkersCache` stamps unless a caller overrides it: reads are
* `eventual`, stale-if-error resilience is on (the default HTTP retriable classifier), an
* `unknown` validity fails CLOSED to a live registry check (`registry-check`, never `miss`), and the
* liveness heartbeat cadence the reader's silence monitor mirrors is the one number above (30s) — the
* READER end of it; the Coordinator end is a wrangler var no factory can reach (R6).
* Set EXPLICITLY (not left to `createCache`'s implicit defaults) so the posture is a single, pinned
* object the factory spreads — a caller's `config.defaults` still wins per-field. Private-by-default
* (a principal-derived request is `usr:`-scoped, L1-only) is `createCache`'s own scope resolution,
*inherited unchanged. Internal (a caller never needs it — the factory already merges it
* with `config.defaults`); the safe-posture tooth proves it BEHAVIORALLY, not by importing it.
*/
var SAFE_DEFAULTS = {
	consistency: "eventual",
	heartbeatInterval: "30s",
	staleIfError: isRetriableHttp,
	unknownPolicy: "registry-check"
};
/** The Coordinator `/bus` WebSocket endpoint the dial upgrades against (mirrors the DO's router). */
var BUS_URL = "https://coordinator/bus";
/**
* Minimum ms between two carrier ticks on ONE instance. The ladder's fastest rung (~1s): ticking
* faster cannot make the poller act sooner (every path it drives is gated by its own due-time), so
* anything below this is pure waste under load. A burst of 1000 reads/second costs one tick.
*/
var CARRIER_TICK_MIN_INTERVAL_MS = 1e3;
/**
* THE CARRIER (the Ticks clause). Anything this library ships as a ready-made composition has to
* bring a freshness driver with it; this is the one `createWorkersCache` brings.
*
* WHY IT HAS TO EXIST. The poller and the redial ladder are both tick-driven and neither runs a
* timer — a Workers isolate has no free-running timers, and the reachability rule forbids a timer performing I/O
* anyway. Until now the ONLY thing that ever called a tick was the React Router middleware, so a
* plain Worker built on this factory had no driver at all: its `__state` poll never ran, its
* silence monitor was never fed, and its redial ladder never advanced. Nothing then held its
* eventual reads inside the staleness bound INV-25 fixes. Whoever ships the drivers owes them
* something to drive them.
*
* WHY THE READ PATH. It is the only thing a plain Worker is guaranteed to do. Riding the read means
* freshness observation is request-coupled — detection latency is max(threshold, inter-request gap)
* — which is the same honest residue the middleware's request-start tick has, and for the same
* reason it is acceptable: the only reader a late observation could hurt is one that is not reading.
*
* ADOPT-DON'T-AWAIT. The tick is fired and NOT awaited: a freshness probe must never add latency to
* the read that happened to trigger it, and must never fail it. `#run` catches everything, so the
* returned promise cannot reject and nothing floats as an unhandled rejection.
*
* CANCELLATION-TOLERANT, AND HERE IS THE ARGUMENT. An unawaited promise in a Workers request is
* cancelled when the response is returned — the tick's in-flight I/O simply rejects. That is safe,
* by three independent properties, none of which is an accident of timing:
* (i) the tick's writes are per-frame atomic and MONOTONE (max-on-write watermarks, a contiguous
*         applied epoch), so an interrupted pass leaves the reader BEHIND where it would have been —
*         never ahead. Behind is the fail-closed direction: it costs a live check, never a stale
*         serve.
* (ii) suspicion is cleared only AFTER a replay completes, so a pass cut short mid-replay cannot
*         leave the reader falsely converged. The half-applied state is exactly the state it would
*         have had if the pass had never started, plus some correctly-applied frames.
*   (iii) the poller's in-flight guard is released in a `finally`, and the rejection a teardown
*         produces travels through the same `await` any other failure does — so it cannot wedge.
* The cursor below is written BEFORE the tick is fired for the same family of reasons: a cancelled
* tick must not leave the gate open for the next read to retry into a hot loop.
*/
function withCarrier(cache, bus, clock, sink, onSinkError) {
	const poller = replicationPollerOf(cache);
	let lastTickMs = null;
	const run = async (nowMs) => {
		try {
			bus.tick(nowMs);
			await poller?.onPollTick(nowMs);
		} catch (error) {
			safeEmit(sink, {
				error: error instanceof Error ? error.name : "unknown",
				type: TELEMETRY_EVENTS.poll_tick_failed
			}, onSinkError);
		}
	};
	const carry = () => {
		const nowMs = clock.now();
		if (lastTickMs !== null && nowMs - lastTickMs < CARRIER_TICK_MIN_INTERVAL_MS) return;
		lastTickMs = nowMs;
		run(nowMs);
	};
	const carried = {
		...cache,
		async getOrSet(options) {
			carry();
			return await cache.getOrSet(options);
		},
		async getOrSetEntry(options) {
			carry();
			return await cache.getOrSetEntry(options);
		}
	};
	aliasCacheIdentity(carried, cache);
	return carried;
}
/** L1 bounds for the Workers profile (example): 512 entries, 5 MB. */
var L1_MAX_ENTRIES = 512;
var L1_MAX_BYTES = 5e6;
/**
* Build a Workers-native `Cache` from one registry `name` + a KV namespace + a Coordinator DO
* namespace. Sugar over `createCache`: the wall clock and RNG enter here at the platform
* boundary and reach the kernel only as the injected `clock`/`rng`; the drivers are composed against
* the single `name` (DO stub, registry id, key namespace). A caller's `defaults` override the safe
* posture per-field; everything else is fixed.
*
* WHY THE L1 IS NOT OPTIONAL HERE, AND WHAT IT COSTS TO SKIP IT. This factory always composes an L1,
* and under an L2 outage that L1 is the absorber. A failing L2 read is suppressed to a plain miss,
*so the reader refills from origin — and with an L1 the refill lands there, is checked first
* on every subsequent read, and the failing tier is never consulted again: one origin call,
* however long the outage runs. Take the L1 away and nothing catches the refill. The read suppresses
* to a miss, the write-back's read-compare cannot read, so nothing lands durably (`durable: false`),
* and the next call repeats the whole trip — every call, forever, for as long as L2 is down. So a
* raw `createCache` composed WITHOUT an `l1` converts an L2 outage into an unbounded origin
* stampede, exactly when the origin is least able to absorb one. That is a priced consequence of
* the miss-class posture, not a defect — a suppressed read must refill, and a refill it cannot
* cache must happen again. Compose an L1, or accept the bill.
*
* ONE DEPLOYMENT STEP THIS FACTORY CANNOT DO FOR YOU (R6). A Worker cannot write a Durable Object's
* `env`, so the Coordinator's idle liveness heartbeat is seeded ONLY by its own wrangler config, and it
* ships DORMANT. Recommended production posture — set it to the same 30s this reader defaults to
* (see `DEFAULT_HEARTBEAT_INTERVAL` for the full price of leaving it unset, and for why a mismatch
* is safe-direction rather than a bug):
*
*     // wrangler.jsonc (the Worker that hosts the Coordinator Durable Object)
*     "vars": { "REGISTRY_HEARTBEAT_MS": "30000" }
*/
function createWorkersCache(config) {
	const { name, kv, coordinator } = config;
	const clock = { now: () => Date.now() };
	const rng = { next: () => Math.random() };
	const stubFor = () => coordinator.get(coordinator.idFromName(name));
	const dial = async () => {
		const response = await stubFor().fetch(BUS_URL, { headers: { Upgrade: "websocket" } });
		const socket = response.webSocket;
		if (!socket) throw new Error(`astilba/cache: expected a 101 webSocket from the Coordinator /bus endpoint, got status ${response.status}`);
		socket.accept();
		return socket;
	};
	const { telemetry } = config;
	const sink = typeof telemetry === "function" ? telemetry : telemetry?.sink;
	const onSinkError = typeof telemetry === "function" ? void 0 : telemetry?.onSinkError;
	const bus = redialingDoBus(dial, {
		regId: name,
		rng
	});
	return withCarrier(createCache({
		namespace: name,
		clock,
		rng,
		l1: memory({
			clock,
			maxEntries: L1_MAX_ENTRIES,
			maxBytes: L1_MAX_BYTES
		}),
		l2: cloudflareKV(kv),
		registry: doRegistry(stubFor, name),
		bus,
		defaults: {
			...SAFE_DEFAULTS,
			...config.defaults
		},
		...telemetry === void 0 ? {} : { telemetry },
		...config.takedownSensitive === void 0 ? {} : { takedownSensitive: config.takedownSensitive }
	}), bus, clock, sink, onSinkError);
}
//#endregion
//#region ../../node_modules/.pnpm/cookie-es@3.1.1/node_modules/cookie-es/dist/index.mjs
function splitSetCookieString(cookiesString) {
	if (Array.isArray(cookiesString)) return cookiesString.flatMap((c) => splitSetCookieString(c));
	if (typeof cookiesString !== "string") return [];
	const cookiesStrings = [];
	let pos = 0;
	let start;
	let ch;
	let lastComma;
	let nextStart;
	let cookiesSeparatorFound;
	const skipWhitespace = () => {
		while (pos < cookiesString.length && /\s/.test(cookiesString.charAt(pos))) pos += 1;
		return pos < cookiesString.length;
	};
	const notSpecialChar = () => {
		ch = cookiesString.charAt(pos);
		return ch !== "=" && ch !== ";" && ch !== ",";
	};
	while (pos < cookiesString.length) {
		start = pos;
		cookiesSeparatorFound = false;
		while (skipWhitespace()) {
			ch = cookiesString.charAt(pos);
			if (ch === ",") {
				lastComma = pos;
				pos += 1;
				skipWhitespace();
				nextStart = pos;
				while (pos < cookiesString.length && notSpecialChar()) pos += 1;
				if (pos < cookiesString.length && cookiesString.charAt(pos) === "=") {
					cookiesSeparatorFound = true;
					pos = nextStart;
					cookiesStrings.push(cookiesString.slice(start, lastComma));
					start = pos;
				} else pos = lastComma + 1;
			} else pos += 1;
		}
		if (!cookiesSeparatorFound || pos >= cookiesString.length) cookiesStrings.push(cookiesString.slice(start));
	}
	return cookiesStrings;
}
//#endregion
//#region ../../node_modules/.pnpm/react-router@8.3.0_react-dom@19.2.7_react@19.2.7__react@19.2.7/node_modules/react-router/dist/production/lib/server-runtime/mode.js
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
function isServerMode(value) {
	return value === "development" || value === "production" || value === "test";
}
//#endregion
//#region ../../node_modules/.pnpm/react-router@8.3.0_react-dom@19.2.7_react@19.2.7__react@19.2.7/node_modules/react-router/dist/production/lib/server-runtime/dev.js
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
var globalDevServerHooksKey = "__reactRouterDevServerHooks";
function getDevServerHooks() {
	return globalThis[globalDevServerHooksKey];
}
function getBuildTimeHeader(request, headerName) {
	if (typeof process !== "undefined") try {
		if (process.env.hasOwnProperty("IS_RR_BUILD_REQUEST") && process.env.IS_RR_BUILD_REQUEST === "yes") return request.headers.get(headerName);
	} catch (e) {}
	return null;
}
//#endregion
//#region ../../node_modules/.pnpm/react-router@8.3.0_react-dom@19.2.7_react@19.2.7__react@19.2.7/node_modules/react-router/dist/production/lib/server-runtime/entry.js
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
function createEntryRouteModules(manifest) {
	return Object.keys(manifest).reduce((memo, routeId) => {
		let route = manifest[routeId];
		if (route) memo[routeId] = route.module;
		return memo;
	}, {});
}
//#endregion
//#region ../../node_modules/.pnpm/react-router@8.3.0_react-dom@19.2.7_react@19.2.7__react@19.2.7/node_modules/react-router/dist/production/lib/server-runtime/errors.js
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
/**
* This thing probably warrants some explanation.
*
* The whole point here is to emulate componentDidCatch for server rendering and
* data loading. It can get tricky. React can do this on component boundaries
* but doesn't support it for server rendering or data loading. We know enough
* with nested routes to be able to emulate the behavior (because we know them
* statically before rendering.)
*
* Each route can export an `ErrorBoundary`.
*
* - When rendering throws an error, the nearest error boundary will render
*   (normal react componentDidCatch). This will be the route's own boundary, but
*   if none is provided, it will bubble up to the parents.
* - When data loading throws an error, the nearest error boundary will render
* - When performing an action, the nearest error boundary for the action's
*   route tree will render (no redirect happens)
*
* During normal react rendering, we do nothing special, just normal
* componentDidCatch.
*
* For server rendering, we mutate `renderBoundaryRouteId` to know the last
* layout that has an error boundary that tried to render. This emulates which
* layout would catch a thrown error. If the rendering fails, we catch the error
* on the server, and go again a second time with the emulator holding on to the
* information it needs to render the same error boundary as a dynamically
* thrown render error.
*
* When data loading, server or client side, we use the emulator to likewise
* hang on to the error and re-render at the appropriate layout (where a thrown
* error would have been caught by cDC).
*
* When actions throw, it all works the same. There's an edge case to be aware
* of though. Actions normally are required to redirect, but in the case of
* errors, we render the action's route with the emulator holding on to the
* error. If during this render a parent route/loader throws we ignore that new
* error and render the action's original error as deeply as possible. In other
* words, we simply ignore the new error and use the action's error in place
* because it came first, and that just wouldn't be fair to let errors cut in
* line.
*/
function sanitizeError(error, serverMode) {
	if (error instanceof Error && serverMode !== "development") {
		let sanitized = /* @__PURE__ */ new Error("Unexpected Server Error");
		sanitized.stack = void 0;
		return sanitized;
	}
	return error;
}
function sanitizeErrors(errors, serverMode) {
	return Object.entries(errors).reduce((acc, [routeId, error]) => {
		return Object.assign(acc, { [routeId]: sanitizeError(error, serverMode) });
	}, {});
}
function serializeError(error, serverMode) {
	let sanitized = sanitizeError(error, serverMode);
	return {
		message: sanitized.message,
		stack: sanitized.stack
	};
}
//#endregion
//#region ../../node_modules/.pnpm/react-router@8.3.0_react-dom@19.2.7_react@19.2.7__react@19.2.7/node_modules/react-router/dist/production/lib/server-runtime/invariant.js
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
	if (value === false || value === null || typeof value === "undefined") {
		console.error("The following error is a bug in React Router; please open an issue! https://github.com/remix-run/react-router/issues/new/choose");
		throw new Error(message);
	}
}
//#endregion
//#region ../../node_modules/.pnpm/react-router@8.3.0_react-dom@19.2.7_react@19.2.7__react@19.2.7/node_modules/react-router/dist/production/lib/server-runtime/routeMatching.js
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
function matchServerRoutes(manifest, dataRoutes, branches, pathname, basename) {
	let matches = matchRoutesImpl(dataRoutes, pathname, basename ?? "/", false, branches);
	if (!matches) return null;
	return matches.map((match) => {
		let route = manifest[match.route.id];
		invariant(route, `Route with id "${match.route.id}" not found in manifest.`);
		return {
			params: match.params,
			pathname: match.pathname,
			route
		};
	});
}
//#endregion
//#region ../../node_modules/.pnpm/react-router@8.3.0_react-dom@19.2.7_react@19.2.7__react@19.2.7/node_modules/react-router/dist/production/lib/server-runtime/data.js
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
async function callRouteHandler(handler, args) {
	let result = await handler({
		request: args.request,
		url: args.url,
		params: args.params,
		context: args.context,
		pattern: args.pattern
	});
	if (isDataWithResponseInit(result) && result.init && result.init.status && isRedirectStatusCode(result.init.status)) throw new Response(null, result.init);
	return result;
}
//#endregion
//#region ../../node_modules/.pnpm/react-router@8.3.0_react-dom@19.2.7_react@19.2.7__react@19.2.7/node_modules/react-router/dist/production/lib/server-runtime/routes.js
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
function groupRoutesByParentId(manifest) {
	let routes = {};
	Object.values(manifest).forEach((route) => {
		if (route) {
			let parentId = route.parentId || "";
			if (!routes[parentId]) routes[parentId] = [];
			routes[parentId].push(route);
		}
	});
	return routes;
}
function createStaticHandlerDataRoutes(manifest, parentId = "", routesByParentId = groupRoutesByParentId(manifest)) {
	return (routesByParentId[parentId] || []).map((route) => {
		let commonRoute = {
			id: route.id,
			path: route.path,
			middleware: route.module.middleware,
			loader: route.module.loader ? async (args) => {
				let preRenderedData = getBuildTimeHeader(args.request, "X-React-Router-Prerender-Data");
				if (preRenderedData != null) {
					let encoded = preRenderedData ? decodeURI(preRenderedData) : preRenderedData;
					invariant(encoded, "Missing prerendered data for route");
					let uint8array = new TextEncoder().encode(encoded);
					let data = (await decodeViaTurboStream(new ReadableStream({ start(controller) {
						controller.enqueue(uint8array);
						controller.close();
					} }), global)).value;
					if (data && SingleFetchRedirectSymbol in data) {
						let result = data[SingleFetchRedirectSymbol];
						let init = { status: result.status };
						if (result.reload) throw redirectDocument(result.redirect, init);
						else if (result.replace) throw replace(result.redirect, init);
						else throw redirect(result.redirect, init);
					} else {
						invariant(data && route.id in data, "Unable to decode prerendered data");
						let result = data[route.id];
						invariant("data" in result, "Unable to process prerendered data");
						return result.data;
					}
				}
				return await callRouteHandler(route.module.loader, args);
			} : void 0,
			action: route.module.action ? (args) => callRouteHandler(route.module.action, args) : void 0,
			ErrorBoundary: route.id === "root" || route.module.ErrorBoundary != null ? () => null : void 0,
			handle: route.module.handle
		};
		return route.index ? {
			index: true,
			...commonRoute
		} : {
			caseSensitive: route.caseSensitive,
			children: createStaticHandlerDataRoutes(manifest, route.id, routesByParentId),
			...commonRoute
		};
	});
}
//#endregion
//#region ../../node_modules/.pnpm/react-router@8.3.0_react-dom@19.2.7_react@19.2.7__react@19.2.7/node_modules/react-router/dist/production/lib/server-runtime/serverHandoff.js
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
function createServerHandoffString(serverHandoff) {
	return escapeHtml(JSON.stringify(serverHandoff));
}
//#endregion
//#region ../../node_modules/.pnpm/react-router@8.3.0_react-dom@19.2.7_react@19.2.7__react@19.2.7/node_modules/react-router/dist/production/lib/server-runtime/headers.js
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
function getDocumentHeaders(context, build) {
	return getDocumentHeadersImpl(context, (m) => {
		let route = build.routes[m.route.id];
		invariant(route, `Route with id "${m.route.id}" not found in build`);
		return route.module.headers;
	});
}
function getDocumentHeadersImpl(context, getRouteHeadersFn, _defaultHeaders) {
	let boundaryIdx = context.errors ? context.matches.findIndex((m) => context.errors[m.route.id]) : -1;
	let matches = boundaryIdx >= 0 ? context.matches.slice(0, boundaryIdx + 1) : context.matches;
	let errorHeaders;
	if (boundaryIdx >= 0) {
		let { actionHeaders, actionData, loaderHeaders, loaderData } = context;
		context.matches.slice(boundaryIdx).some((match) => {
			let id = match.route.id;
			if (actionHeaders[id] && (!actionData || !actionData.hasOwnProperty(id))) errorHeaders = actionHeaders[id];
			else if (loaderHeaders[id] && !loaderData.hasOwnProperty(id)) errorHeaders = loaderHeaders[id];
			return errorHeaders != null;
		});
	}
	const defaultHeaders = new Headers(_defaultHeaders);
	return matches.reduce((parentHeaders, match, idx) => {
		let { id } = match.route;
		let loaderHeaders = context.loaderHeaders[id] || new Headers();
		let actionHeaders = context.actionHeaders[id] || new Headers();
		let includeErrorHeaders = errorHeaders != null && idx === matches.length - 1;
		let includeErrorCookies = includeErrorHeaders && errorHeaders !== loaderHeaders && errorHeaders !== actionHeaders;
		let headersFn = getRouteHeadersFn(match);
		if (headersFn == null) {
			let headers = new Headers(parentHeaders);
			if (includeErrorCookies) prependCookies(errorHeaders, headers);
			prependCookies(actionHeaders, headers);
			prependCookies(loaderHeaders, headers);
			return headers;
		}
		let headers = new Headers(typeof headersFn === "function" ? headersFn({
			loaderHeaders,
			parentHeaders,
			actionHeaders,
			errorHeaders: includeErrorHeaders ? errorHeaders : void 0
		}) : headersFn);
		if (includeErrorCookies) prependCookies(errorHeaders, headers);
		prependCookies(actionHeaders, headers);
		prependCookies(loaderHeaders, headers);
		prependCookies(parentHeaders, headers);
		return headers;
	}, new Headers(defaultHeaders));
}
function prependCookies(parentHeaders, childHeaders) {
	let parentSetCookieString = parentHeaders.get("Set-Cookie");
	if (parentSetCookieString) {
		let cookies = splitSetCookieString(parentSetCookieString);
		let childCookies = new Set(childHeaders.getSetCookie());
		cookies.forEach((cookie) => {
			if (!childCookies.has(cookie)) childHeaders.append("Set-Cookie", cookie);
		});
	}
}
//#endregion
//#region ../../node_modules/.pnpm/react-router@8.3.0_react-dom@19.2.7_react@19.2.7__react@19.2.7/node_modules/react-router/dist/production/lib/actions.js
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
function throwIfPotentialCSRFAttack(request, allowedActionOrigins) {
	let originHeader = request.headers.get("origin");
	let originDomain = null;
	try {
		originDomain = typeof originHeader === "string" && originHeader !== "null" ? new URL(originHeader).host : originHeader;
	} catch {
		throw new Error(`\`origin\` header is not a valid URL. Aborting the action.`);
	}
	let host = new URL(request.url).host;
	if (originDomain && originDomain !== host) {
		if (!isAllowedOrigin(originDomain, allowedActionOrigins)) throw new Error("The `request.url` host does not match `origin` header from a forwarded action request. Aborting the action.");
	}
}
function matchWildcardDomain(domain, pattern) {
	const domainParts = domain.split(".");
	const patternParts = pattern.split(".");
	if (patternParts.length < 1) return false;
	if (domainParts.length < patternParts.length) return false;
	while (patternParts.length) {
		const patternPart = patternParts.pop();
		const domainPart = domainParts.pop();
		switch (patternPart) {
			case "": return false;
			case "*": if (domainPart) continue;
			else return false;
			case "**":
				if (patternParts.length > 0) return false;
				return domainPart !== void 0;
			case void 0:
			default: if (domainPart !== patternPart) return false;
		}
	}
	return domainParts.length === 0;
}
function isAllowedOrigin(originDomain, allowedActionOrigins = []) {
	return allowedActionOrigins.some((allowedOrigin) => allowedOrigin && (allowedOrigin === originDomain || matchWildcardDomain(originDomain, allowedOrigin)));
}
//#endregion
//#region ../../node_modules/.pnpm/react-router@8.3.0_react-dom@19.2.7_react@19.2.7__react@19.2.7/node_modules/react-router/dist/production/lib/server-runtime/urls.js
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
function getNormalizedPath(request) {
	let url = new URL(request.url);
	let pathname = url.pathname;
	if (pathname.endsWith("/_.data")) pathname = pathname.replace(/_\.data$/, "");
	else pathname = pathname.replace(/\.data$/, "");
	let searchParams = new URLSearchParams(url.search);
	searchParams.delete("_routes");
	let search = searchParams.toString();
	if (search) search = `?${search}`;
	return {
		pathname,
		search,
		hash: ""
	};
}
//#endregion
//#region ../../node_modules/.pnpm/react-router@8.3.0_react-dom@19.2.7_react@19.2.7__react@19.2.7/node_modules/react-router/dist/production/lib/server-runtime/single-fetch.js
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
var SERVER_NO_BODY_STATUS_CODES = /* @__PURE__ */ new Set([...NO_BODY_STATUS_CODES, 304]);
async function singleFetchAction(build, serverMode, staticHandler, request, loadContext, handleError) {
	try {
		try {
			throwIfPotentialCSRFAttack(request, Array.isArray(build.allowedActionOrigins) ? build.allowedActionOrigins : []);
		} catch (e) {
			return handleQueryError(/* @__PURE__ */ new Error("Bad Request"), 400);
		}
		return handleQueryResult(await staticHandler.query(request, {
			requestContext: loadContext,
			skipLoaderErrorBubbling: true,
			skipRevalidation: true,
			generateMiddlewareResponse: async (query) => {
				try {
					return handleQueryResult(await query(request));
				} catch (error) {
					return handleQueryError(error);
				}
			},
			normalizePath: (r) => getNormalizedPath(r)
		}));
	} catch (error) {
		return handleQueryError(error);
	}
	function handleQueryResult(result) {
		return isResponse(result) ? result : staticContextToResponse(result);
	}
	function handleQueryError(error, status = 500) {
		handleError(error);
		return generateSingleFetchResponse(request, build, serverMode, {
			result: { error },
			headers: new Headers(),
			status
		});
	}
	function staticContextToResponse(context) {
		let headers = getDocumentHeaders(context, build);
		if (isRedirectStatusCode(context.statusCode) && headers.has("Location")) return new Response(null, {
			status: context.statusCode,
			headers
		});
		if (context.errors) {
			Object.values(context.errors).forEach((err) => {
				if (!isRouteErrorResponse(err) || err.error) handleError(err);
			});
			context.errors = sanitizeErrors(context.errors, serverMode);
		}
		let singleFetchResult;
		if (context.errors) singleFetchResult = { error: Object.values(context.errors)[0] };
		else singleFetchResult = { data: Object.values(context.actionData || {})[0] };
		return generateSingleFetchResponse(request, build, serverMode, {
			result: singleFetchResult,
			headers,
			status: context.statusCode
		});
	}
}
async function singleFetchLoaders(build, serverMode, staticHandler, request, loadContext, handleError) {
	let routesParam = new URL(request.url).searchParams.get("_routes");
	let loadRouteIds = routesParam ? new Set(routesParam.split(",")) : null;
	try {
		return handleQueryResult(await staticHandler.query(request, {
			requestContext: loadContext,
			filterMatchesToLoad: (m) => !loadRouteIds || loadRouteIds.has(m.route.id),
			skipLoaderErrorBubbling: true,
			generateMiddlewareResponse: async (query) => {
				try {
					return handleQueryResult(await query(request));
				} catch (error) {
					return handleQueryError(error);
				}
			},
			normalizePath: (r) => getNormalizedPath(r)
		}));
	} catch (error) {
		return handleQueryError(error);
	}
	function handleQueryResult(result) {
		return isResponse(result) ? result : staticContextToResponse(result);
	}
	function handleQueryError(error) {
		handleError(error);
		return generateSingleFetchResponse(request, build, serverMode, {
			result: { error },
			headers: new Headers(),
			status: 500
		});
	}
	function staticContextToResponse(context) {
		let headers = getDocumentHeaders(context, build);
		if (isRedirectStatusCode(context.statusCode) && headers.has("Location")) return new Response(null, {
			status: context.statusCode,
			headers
		});
		if (context.errors) {
			Object.values(context.errors).forEach((err) => {
				if (!isRouteErrorResponse(err) || err.error) handleError(err);
			});
			context.errors = sanitizeErrors(context.errors, serverMode);
		}
		let results = {};
		let loadedMatches = new Set(context.matches.filter((m) => loadRouteIds ? loadRouteIds.has(m.route.id) : m.route.loader != null).map((m) => m.route.id));
		if (context.errors) for (let [id, error] of Object.entries(context.errors)) results[id] = { error };
		for (let [id, data] of Object.entries(context.loaderData)) if (!(id in results) && loadedMatches.has(id)) results[id] = { data };
		return generateSingleFetchResponse(request, build, serverMode, {
			result: results,
			headers,
			status: context.statusCode
		});
	}
}
function generateSingleFetchResponse(request, build, serverMode, { result, headers, status }) {
	let resultHeaders = new Headers(headers);
	resultHeaders.set("X-Remix-Response", "yes");
	if (SERVER_NO_BODY_STATUS_CODES.has(status)) return new Response(null, {
		status,
		headers: resultHeaders
	});
	resultHeaders.set("Content-Type", "text/x-script");
	resultHeaders.delete("Content-Length");
	return new Response(encodeViaTurboStream(result, request.signal, build.entry.module.streamTimeout, serverMode), {
		status: status || 200,
		headers: resultHeaders
	});
}
function generateSingleFetchRedirectResponse(redirectResponse, request, build, serverMode) {
	let redirect = getSingleFetchRedirect(redirectResponse.status, redirectResponse.headers, build.basename);
	let headers = new Headers(redirectResponse.headers);
	headers.delete("Location");
	headers.set("Content-Type", "text/x-script");
	return generateSingleFetchResponse(request, build, serverMode, {
		result: request.method === "GET" ? { [SingleFetchRedirectSymbol]: redirect } : redirect,
		headers,
		status: 202
	});
}
function getSingleFetchRedirect(status, headers, basename) {
	let redirect = headers.get("Location");
	if (basename) redirect = stripBasename(redirect, basename) || redirect;
	return {
		redirect,
		status,
		revalidate: headers.has("X-Remix-Revalidate") || headers.has("Set-Cookie"),
		reload: headers.has("X-Remix-Reload-Document"),
		replace: headers.has("X-Remix-Replace")
	};
}
function encodeViaTurboStream(data, requestSignal, streamTimeout, serverMode) {
	let controller = new AbortController();
	let timeoutId = setTimeout(() => {
		controller.abort(/* @__PURE__ */ new Error("Server Timeout"));
		cleanupCallbacks();
	}, typeof streamTimeout === "number" ? streamTimeout : 4950);
	let abortControllerOnRequestAbort = () => {
		controller.abort(requestSignal.reason);
		cleanupCallbacks();
	};
	requestSignal.addEventListener("abort", abortControllerOnRequestAbort);
	let cleanupCallbacks = () => {
		clearTimeout(timeoutId);
		requestSignal.removeEventListener("abort", abortControllerOnRequestAbort);
	};
	return encode$1(data, {
		signal: controller.signal,
		onComplete: cleanupCallbacks,
		plugins: [(value) => {
			if (value instanceof Error) {
				let { name, message, stack } = serverMode === "production" ? sanitizeError(value, serverMode) : value;
				return [
					"SanitizedError",
					name,
					message,
					stack
				];
			}
			if (value instanceof ErrorResponseImpl) {
				let { data, status, statusText } = value;
				return [
					"ErrorResponse",
					data,
					status,
					statusText
				];
			}
			if (value && typeof value === "object" && SingleFetchRedirectSymbol in value) return ["SingleFetchRedirect", value[SingleFetchRedirectSymbol]];
		}],
		postPlugins: [(value) => {
			if (!value) return;
			if (typeof value !== "object") return;
			return ["SingleFetchClassInstance", Object.fromEntries(Object.entries(value))];
		}, () => ["SingleFetchFallback"]]
	});
}
//#endregion
//#region ../../node_modules/.pnpm/react-router@8.3.0_react-dom@19.2.7_react@19.2.7__react@19.2.7/node_modules/react-router/dist/production/lib/server-runtime/server.js
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
function derive(build, mode) {
	let dataRoutes = createStaticHandlerDataRoutes(build.routes);
	let serverMode = isServerMode(mode) ? mode : "production";
	let staticHandler = createStaticHandler(dataRoutes, {
		basename: build.basename,
		mapRouteProperties: defaultMapRouteProperties,
		instrumentations: build.entry.module.instrumentations,
		future: build.future
	});
	let errorHandler = build.entry.module.handleError || ((error, { request }) => {
		if (serverMode !== "test" && !request.signal.aborted) console.error(isRouteErrorResponse(error) && error.error ? error.error : error);
	});
	let requestHandlerInstrumentations = build.entry.module.instrumentations?.map((i) => i.handler).filter(Boolean);
	let requestHandler = async (request, initialContext) => {
		let params = {};
		let loadContext;
		let handleError = (error) => {
			if (mode === "development") getDevServerHooks()?.processRequestError?.(error);
			errorHandler(error, {
				context: loadContext,
				params,
				request
			});
		};
		if (initialContext && !(initialContext instanceof RouterContextProvider)) {
			let error = /* @__PURE__ */ new Error("Invalid `context` value provided to `handleRequest`. You must return an instance of `RouterContextProvider` from your `getLoadContext` function.");
			handleError(error);
			return returnLastResortErrorResponse(error, serverMode);
		}
		loadContext = initialContext || new RouterContextProvider();
		let requestUrl = new URL(request.url);
		let normalizedPath = getNormalizedPath(request);
		let normalizedPathname = normalizedPath.pathname;
		let isSpaMode = getBuildTimeHeader(request, "X-React-Router-SPA-Mode") === "yes";
		if (!build.ssr) {
			let decodedPath = decodeURI(normalizedPathname);
			if (build.basename && build.basename !== "/") {
				let strippedPath = stripBasename(decodedPath, build.basename);
				if (strippedPath == null) {
					errorHandler(new ErrorResponseImpl(404, "Not Found", `Refusing to prerender the \`${decodedPath}\` path because it does not start with the basename \`${build.basename}\``), {
						context: loadContext,
						params,
						request
					});
					return new Response("Not Found", {
						status: 404,
						statusText: "Not Found"
					});
				}
				decodedPath = strippedPath;
			}
			if (build.prerender.length === 0) isSpaMode = true;
			else if (!build.prerender.some((p) => removeTrailingSlash(p) === removeTrailingSlash(decodedPath))) if (requestUrl.pathname.endsWith(".data")) {
				errorHandler(new ErrorResponseImpl(404, "Not Found", `Refusing to SSR the path \`${decodedPath}\` because \`ssr:false\` is set and the path is not included in the \`prerender\` config, so in production the path will be a 404.`), {
					context: loadContext,
					params,
					request
				});
				return new Response("Not Found", {
					status: 404,
					statusText: "Not Found"
				});
			} else isSpaMode = true;
		}
		let manifestUrl = getManifestPath(build.routeDiscovery.manifestPath, build.basename);
		if (build.routeDiscovery.mode === "lazy" && requestUrl.pathname === manifestUrl) try {
			return await handleManifestRequest(build, staticHandler.dataRoutes, staticHandler._internalRouteBranches, requestUrl);
		} catch (e) {
			handleError(e);
			return new Response("Unknown Server Error", { status: 500 });
		}
		let matches = matchServerRoutes(build.routes, staticHandler.dataRoutes, staticHandler._internalRouteBranches, normalizedPathname, build.basename);
		if (matches && matches.length > 0) Object.assign(params, matches[0].params);
		if (requestHandlerInstrumentations?.length) loadContext.set(instrumentationResultMetaContext, {
			url: createDataFunctionUrl(request, normalizedPath),
			pattern: matches ? getRoutePattern(matches) : "",
			params: matches?.[0]?.params ? { ...matches[0].params } : {}
		});
		let response;
		if (requestUrl.pathname.endsWith(".data")) {
			response = await handleSingleFetchRequest(serverMode, build, staticHandler, request, loadContext, handleError);
			if (isRedirectResponse(response)) response = generateSingleFetchRedirectResponse(response, request, build, serverMode);
			if (build.entry.module.handleDataRequest) {
				response = await build.entry.module.handleDataRequest(response, {
					context: loadContext,
					params: matches ? matches[0].params : {},
					request
				});
				if (isRedirectResponse(response)) response = generateSingleFetchRedirectResponse(response, request, build, serverMode);
			}
		} else if (!isSpaMode && matches && matches[matches.length - 1].route.module.default == null && matches[matches.length - 1].route.module.ErrorBoundary == null) response = await handleResourceRequest(serverMode, build, staticHandler, matches.slice(-1)[0].route.id, request, loadContext, handleError);
		else {
			let { pathname } = requestUrl;
			let criticalCss = void 0;
			if (build.unstable_getCriticalCss) criticalCss = await build.unstable_getCriticalCss({ pathname });
			else if (mode === "development" && getDevServerHooks()?.getCriticalCss) criticalCss = await getDevServerHooks()?.getCriticalCss?.(pathname);
			response = await handleDocumentRequest(serverMode, build, staticHandler, request, loadContext, handleError, isSpaMode, criticalCss);
		}
		if (request.method === "HEAD") return new Response(null, {
			headers: response.headers,
			status: response.status,
			statusText: response.statusText
		});
		return response;
	};
	if (requestHandlerInstrumentations?.length) requestHandler = instrumentHandler(requestHandler, requestHandlerInstrumentations);
	return {
		serverMode,
		staticHandler,
		errorHandler,
		requestHandler
	};
}
/**
* Creates a request handler for a React Router server build.
*
* This is a low-level API used by server adapters to translate incoming
* requests into React Router responses.
*
* @category Utils
* @param build The server build, or a function that resolves to the server
* build, used to handle requests.
* @param mode The mode in which the server build is running.
* @returns A request handler that returns a response for each incoming request.
*/
var createRequestHandler = (build, mode) => {
	let _build;
	let serverMode;
	let staticHandler;
	let errorHandler;
	let _requestHandler;
	return async function requestHandler(request, initialContext) {
		_build = typeof build === "function" ? await build() : build;
		if (typeof build === "function") {
			let derived = derive(_build, mode);
			serverMode = derived.serverMode;
			staticHandler = derived.staticHandler;
			errorHandler = derived.errorHandler;
			_requestHandler = derived.requestHandler;
		} else if (!serverMode || !staticHandler || !errorHandler || !_requestHandler) {
			let derived = derive(_build, mode);
			serverMode = derived.serverMode;
			staticHandler = derived.staticHandler;
			errorHandler = derived.errorHandler;
			_requestHandler = derived.requestHandler;
		}
		return _requestHandler(request, initialContext);
	};
};
async function handleManifestRequest(build, dataRoutes, branches, url) {
	if (url.toString().length > 7680) return new Response(null, {
		statusText: "Bad Request",
		status: 400
	});
	if (build.assets.version !== url.searchParams.get("version")) return new Response(null, {
		status: 204,
		headers: { "X-Remix-Reload-Document": "true" }
	});
	let patches = {};
	if (url.searchParams.has("paths")) {
		let pathParam = url.searchParams.get("paths") || "";
		let paths = new Set(pathParam.split(",").filter(Boolean));
		for (let path of paths) {
			if (!path.startsWith("/")) path = `/${path}`;
			let matches = matchServerRoutes(build.routes, dataRoutes, branches, path, build.basename);
			if (matches) for (let match of matches) {
				let routeId = match.route.id;
				let route = build.assets.routes[routeId];
				if (route) patches[routeId] = route;
			}
		}
		return Response.json(patches, { headers: { "Cache-Control": "public, max-age=31536000, immutable" } });
	}
	return new Response("Invalid Request", { status: 400 });
}
async function handleSingleFetchRequest(serverMode, build, staticHandler, request, loadContext, handleError) {
	return isMutationMethod(request.method) ? await singleFetchAction(build, serverMode, staticHandler, request, loadContext, handleError) : await singleFetchLoaders(build, serverMode, staticHandler, request, loadContext, handleError);
}
async function handleDocumentRequest(serverMode, build, staticHandler, request, loadContext, handleError, isSpaMode, criticalCss) {
	try {
		if (isMutationMethod(request.method)) try {
			throwIfPotentialCSRFAttack(request, Array.isArray(build.allowedActionOrigins) ? build.allowedActionOrigins : []);
		} catch (e) {
			handleError(e);
			return new Response("Bad Request", { status: 400 });
		}
		let result = await staticHandler.query(request, {
			requestContext: loadContext,
			generateMiddlewareResponse: async (query) => {
				try {
					let innerResult = await query(request);
					if (!isResponse(innerResult)) innerResult = await renderHtml(innerResult, isSpaMode);
					return innerResult;
				} catch (error) {
					handleError(error);
					return new Response(null, { status: 500 });
				}
			},
			normalizePath: (r) => getNormalizedPath(r)
		});
		if (!isResponse(result)) result = await renderHtml(result, isSpaMode);
		return result;
	} catch (error) {
		handleError(error);
		return new Response(null, { status: 500 });
	}
	async function renderHtml(context, isSpaMode) {
		let headers = getDocumentHeaders(context, build);
		if (SERVER_NO_BODY_STATUS_CODES.has(context.statusCode)) return new Response(null, {
			status: context.statusCode,
			headers
		});
		if (context.errors) {
			Object.values(context.errors).forEach((err) => {
				if (!isRouteErrorResponse(err) || err.error) handleError(err);
			});
			context.errors = sanitizeErrors(context.errors, serverMode);
		}
		let state = {
			loaderData: context.loaderData,
			actionData: context.actionData,
			errors: context.errors
		};
		let baseServerHandoff = {
			basename: build.basename,
			future: build.future,
			routeDiscovery: build.routeDiscovery,
			ssr: build.ssr,
			isSpaMode
		};
		let entryContext = {
			manifest: build.assets,
			branches: staticHandler._internalRouteBranches,
			routeModules: createEntryRouteModules(build.routes),
			staticHandlerContext: context,
			criticalCss,
			serverHandoffString: createServerHandoffString({
				...baseServerHandoff,
				criticalCss
			}),
			serverHandoffStream: encodeViaTurboStream(state, request.signal, build.entry.module.streamTimeout, serverMode),
			renderMeta: {},
			future: build.future,
			ssr: build.ssr,
			routeDiscovery: build.routeDiscovery,
			isSpaMode,
			serializeError: (err) => serializeError(err, serverMode)
		};
		let handleDocumentRequestFunction = build.entry.module.default;
		try {
			return await handleDocumentRequestFunction(request, context.statusCode, headers, entryContext, loadContext);
		} catch (error) {
			handleError(error);
			let errorForSecondRender = error;
			if (isResponse(error)) try {
				let data = await unwrapResponse(error);
				errorForSecondRender = new ErrorResponseImpl(error.status, error.statusText, data);
			} catch (e) {}
			context = getStaticContextFromError(staticHandler.dataRoutes, context, errorForSecondRender);
			if (context.errors) context.errors = sanitizeErrors(context.errors, serverMode);
			let state = {
				loaderData: context.loaderData,
				actionData: context.actionData,
				errors: context.errors
			};
			entryContext = {
				...entryContext,
				staticHandlerContext: context,
				serverHandoffString: createServerHandoffString(baseServerHandoff),
				serverHandoffStream: encodeViaTurboStream(state, request.signal, build.entry.module.streamTimeout, serverMode),
				renderMeta: {}
			};
			try {
				return await handleDocumentRequestFunction(request, context.statusCode, headers, entryContext, loadContext);
			} catch (error) {
				handleError(error);
				return returnLastResortErrorResponse(error, serverMode);
			}
		}
	}
}
async function handleResourceRequest(serverMode, build, staticHandler, routeId, request, loadContext, handleError) {
	try {
		return handleQueryRouteResult(await staticHandler.queryRoute(request, {
			routeId,
			requestContext: loadContext,
			generateMiddlewareResponse: async (queryRoute) => {
				try {
					return handleQueryRouteResult(await queryRoute(request));
				} catch (error) {
					return handleQueryRouteError(error);
				}
			},
			normalizePath: (r) => getNormalizedPath(r)
		}));
	} catch (error) {
		return handleQueryRouteError(error);
	}
	function handleQueryRouteResult(result) {
		if (isResponse(result)) return result;
		if (typeof result === "string") return new Response(result);
		return Response.json(result);
	}
	function handleQueryRouteError(error) {
		if (isResponse(error)) return error;
		if (isRouteErrorResponse(error)) {
			handleError(error);
			return errorResponseToJson(error, serverMode);
		}
		if (error instanceof Error && error.message === "Expected a response from queryRoute") {
			let newError = /* @__PURE__ */ new Error("Expected a Response to be returned from resource route handler");
			handleError(newError);
			return returnLastResortErrorResponse(newError, serverMode);
		}
		handleError(error);
		return returnLastResortErrorResponse(error, serverMode);
	}
}
function errorResponseToJson(errorResponse, serverMode) {
	return Response.json(serializeError(errorResponse.error || /* @__PURE__ */ new Error("Unexpected Server Error"), serverMode), {
		status: errorResponse.status,
		statusText: errorResponse.statusText
	});
}
function returnLastResortErrorResponse(error, serverMode) {
	let message = "Unexpected Server Error";
	if (serverMode !== "production") message += `\n\n${String(error)}`;
	return new Response(message, {
		status: 500,
		headers: { "Content-Type": "text/plain" }
	});
}
function unwrapResponse(response) {
	let contentType = response.headers.get("Content-Type");
	return contentType && /\bapplication\/json\b/.test(contentType) ? response.body == null ? null : response.json() : response.text();
}
//#endregion
//#region chaos/platform-method.ts
/**
* View a value already proven callable as a `PlatformMethod`.
*
* The assertion is the narrowest one available and it is doing real work rather than papering over a
* doubt: the caller has established `typeof value === "function"`, so the value IS callable, and all
* this states is the argument and return types that `Function` declines to carry. The alternative
* spellings all end in `any` — which is the thing being avoided — so this is the honest floor rather
* than a shortcut past it.
*/
function asPlatformMethod(value) {
	if (typeof value !== "function") throw new TypeError("chaos: asPlatformMethod called with a value that is not callable");
	return value;
}
//#endregion
//#region chaos/faultable-coordinator.ts
/** The rejection a refused dial produces. The real shape of an unreachable Durable Object is a
* rejected `fetch`, so that is the shape this raises — the redialing bus driver sees a failed dial
* and applies its own backoff, which is the behaviour on camera. */
var DialRefusedError = class extends Error {
	constructor() {
		super("chaos: Coordinator dial refused — the socket did not open");
		this.name = "DialRefusedError";
	}
};
/** Wrap one Durable Object stub so its `fetch` (and only its `fetch`) can be refused. */
function faultableStub(real, dials, stats) {
	return new Proxy(real, { get(target, property) {
		const member = Reflect.get(target, property);
		if (property !== "fetch" || typeof member !== "function") return member;
		const dial = asPlatformMethod(member);
		return async (...args) => {
			if (dials.bus.dialRefused) {
				stats.dialsRefused++;
				throw new DialRefusedError();
			}
			stats.dialsPassed++;
			return await Reflect.apply(dial, target, args);
		};
	} });
}
/**
* Wrap the real `COORDINATOR` namespace so every stub it hands out can have its dial refused. The
* returned value IS a `DurableObjectNamespace<Coordinator>` — the library receives it as the
* `coordinator` of the hero five-liner and addresses it exactly as it would the binding itself.
*/
function faultableCoordinator(real, dials, stats) {
	return new Proxy(real, { get(target, property) {
		const member = Reflect.get(target, property);
		if (property !== "get" || typeof member !== "function") return typeof member === "function" ? asPlatformMethod(member).bind(target) : member;
		const get = asPlatformMethod(member);
		return (...args) => {
			const stub = Reflect.apply(get, target, args);
			return typeof stub === "object" && stub !== null ? faultableStub(stub, dials, stats) : stub;
		};
	} });
}
//#endregion
//#region chaos/faultable-kv.ts
/** The KV methods this wrapper intercepts. Anything else on the binding passes straight through. */
var READ_METHODS = /* @__PURE__ */ new Set(["get", "getWithMetadata"]);
var WRITE_METHODS = /* @__PURE__ */ new Set(["put", "delete"]);
/** The rejection a faulted KV operation produces — the shape the real binding rejects with. */
var KvFaultError = class extends Error {
	constructor(operation, reason) {
		super(`chaos: KV ${operation} rejected — ${reason}`);
		this.name = "KvFaultError";
	}
};
/** Does this call target the unreadable keyspace? Only the FIRST argument is inspected, and only
* when it is a string: KV's batch-read overloads take an array, which this demo never drives. */
function targetsUnreadablePrefix(args, prefix) {
	if (prefix === null) return false;
	const [key] = args;
	return typeof key === "string" && key.startsWith(prefix);
}
/**
* Wrap a real `KVNamespace` so the demo's dials can fault it. At the call site `T` infers as
* `KVNamespace`, so what the hero five-liner receives as its `kv` IS a `KVNamespace` and it composes
* its L2 over this exactly as it would over the binding itself.
*
* GENERIC, and for a reason worth defending rather than apologising for. `KVNamespace` is thirty-odd
* overloads deep, and nothing that is not the real binding can satisfy it — a hand-built stand-in
* fails to typecheck against the `arrayBuffer` and `stream` overloads alone. Pinning the parameter
* to `KVNamespace` would therefore have made this wrapper unTESTABLE except by casting something
* into a lie, and this wrapper is the last thing in the app that should go untested: it is a Proxy
* over a platform object, which is precisely the kind of code that fails in ways review does not
* catch and a test does. Inference gives the call site the exact same type it would have had.
*/
function faultableKv(real, dials, stats) {
	return new Proxy(real, { get(target, property) {
		const member = Reflect.get(target, property);
		if (typeof property !== "string" || typeof member !== "function") return member;
		const method = asPlatformMethod(member);
		const isRead = READ_METHODS.has(property);
		const isWrite = WRITE_METHODS.has(property);
		if (!(isRead || isWrite)) return method.bind(target);
		return async (...args) => {
			if (isRead) {
				if (dials.kv.failReads) {
					stats.readsFailed++;
					throw new KvFaultError(property, "reads are failing");
				}
				if (targetsUnreadablePrefix(args, dials.kv.unreadablePrefix)) {
					stats.readsFailed++;
					throw new KvFaultError(property, "keyspace is unreadable");
				}
				stats.readsPassed++;
			}
			if (isWrite) {
				if (dials.kv.failWrites) {
					stats.writesFailed++;
					throw new KvFaultError(property, "writes are failing");
				}
				stats.writesPassed++;
			}
			return await Reflect.apply(method, target, args);
		};
	} });
}
//#endregion
//#region workers/app.ts
/** The ONE identity string: the Durable Object's name, the registry id, and the cache key
* namespace all derive from it, so the address and the scope cannot drift apart. */
var REGISTRY_NAME = "demo";
/** How many telemetry events the console keeps. A demo that grew a log until the isolate died would
* be a poor advertisement for anything. */
var EVENT_LOG_LIMIT = 50;
var handler = createRequestHandler(async () => await import("./assets/server-build-CV4JBGS-.js"), "production");
var episode = {
	armed: null,
	busStats: emptyBusStats(),
	dials: noFaults(),
	episodeCache: null,
	events: [],
	kvStats: emptyKvStats(),
	pending: null,
	rescue: null
};
var sink = (event) => {
	episode.events.push(event);
	if (episode.events.length > EVENT_LOG_LIMIT) episode.events.shift();
};
/** Parse the Coordinator's heartbeat var as it ACTUALLY arrived, rather than assuming the value this
* repo's wrangler config sets. Unset means dormant, and dormant is the shipped default. */
function readCoordinatorHeartbeat(raw) {
	if (raw === void 0) return null;
	const parsed = Number(raw);
	return Number.isFinite(parsed) && parsed > 0 ? parsed : null;
}
/**
* THE CACHE IS BUILT ONCE, HERE, AT MODULE SCOPE — one per isolate, which is the placement the
* quickstart teaches and the lifetime the design was always for.
*
* This app used to rebuild the cache on every request, and the reason was worth the paragraph it
* got: the factory resolved the Coordinator namespace to a STUB at construction and held it, a
* Workers stub belongs to the request that minted it, so a cache outliving a request met
* `Cannot perform I/O on behalf of a different request` the moment it next touched the registry.
* Rebuilding was the only lifetime that ran. The price was a cold L1 every request and a reader that
* was newborn every request — one that had observed no silence and could therefore never grow
* suspicious, no matter how long a fault stayed armed. The page had to print that caveat under every
* table, because otherwise it showed a system serenely ignoring an outage.
*
* the factory holds the ADDRESS — `{namespace, name}` — and mints a stub per use,
* inside whichever request is running. Construction wires and captures; it performs no I/O, and
* minting counts as I/O. So this call is legal at module scope, where `cloudflare:workers`' `env` is
* how bindings are reached before any request exists.
*
* WHAT THAT BUYS THIS DEMO: the reader accumulates. It has a channel state that persists across
* requests, an applied epoch that advances, and silence it can actually observe.
*
* AND THE SAME LIFETIME IS WHY ONE SCENE NEEDS A SECOND INSTANCE. This reader's channel came up on
* the first request anyone made, which is exactly right for every other page here and fatal to the
* one scene whose subject is a channel that never came up: no amount of arming a dial can un-live a
* life. So `mintCache` exists, the deaf-reader scene arms and mints, and the page names the rig
* rather than pretending the long-lived reader is a newborn.
*/
function mintCache() {
	return createWorkersCache({
		coordinator: faultableCoordinator(env.COORDINATOR, episode.dials, episode.busStats),
		defaults: {
			heartbeatInterval: `${HEARTBEAT_INTERVAL_MS / 1e3}s`,
			maxSyncLag: `${POSTURE.maxSyncLagMs / 1e3}s`
		},
		kv: faultableKv(env.CACHE_KV, episode.dials, episode.kvStats),
		name: REGISTRY_NAME,
		telemetry: sink
	});
}
/** The isolate's long-lived cache — built ONCE, at module scope, and the one every page narrates
* unless an episode booted its own. */
var cache = mintCache();
var world = {
	activeCache: () => episode.episodeCache ?? cache,
	arm(id) {
		armScene(id, episode, {
			mintCache,
			nowMs: Date.now(),
			registryName: REGISTRY_NAME
		});
	},
	armed: () => episode.armed,
	busStats: episode.busStats,
	coordinatorHeartbeatMs: readCoordinatorHeartbeat(env.REGISTRY_HEARTBEAT_MS),
	dials: episode.dials,
	events: episode.events,
	heal() {
		healEpisode(episode);
	},
	kvStats: episode.kvStats,
	lastRescue: () => episode.rescue,
	nowMs: () => Date.now(),
	onEpisodeReader: () => episode.episodeCache !== null,
	pendingPurge: () => episode.pending,
	purgeIssued(purge) {
		episode.pending = {
			...purge,
			atMs: Date.now()
		};
		episode.rescue = null;
	},
	registryName: REGISTRY_NAME,
	rescueObserved(observed) {
		episode.rescue = observed;
		episode.pending = null;
	},
	sink
};
//#endregion
//#region \0virtual:cloudflare/worker-entry
var worker_entry_default = { async fetch(request, _env, ctx) {
	const context = new RouterContextProvider();
	context.set(worldContext, world);
	context.set(waitUntilContext, ctx.waitUntil.bind(ctx));
	return await handler(request, context);
} };
//#endregion
export { Coordinator, worker_entry_default as default };
