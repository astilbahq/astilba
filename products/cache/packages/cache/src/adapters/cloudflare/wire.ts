// @astilba/cache adapter — the doBus wire codec (phase 2).
//
// PLATFORM: lives under `src/adapters/**` (the sanctioned platform boundary) but is
// PURE — no ambient time/randomness, no `cloudflare:*` import — so it ALSO loads in the Node
// conformance lane. ONE module owns the versioned wire envelope + `encode`/`decode`, imported by
// BOTH the doBus server handler AND the client factory, so the two ends can never re-drift to
// different bytes.
//
// ENVELOPE (versioned, one event per WS message; message boundaries ARE the framing — no length
// prefixes): `{"v":1,"kind":<k>, …payload}`. The FRAME payload is the EXISTING canonical
// serialization passed through UNCHANGED — `encode` ships `JSON.stringify(frame)` (the SAME call
// the flush executor persists a `__delta` with, over the SAME machine-canonical frame), and
// `decode` re-parses it through the reader's OWN `readFrame`. One serializer, one
// parser, for the durable mirror AND the live channel: this codec is a transport, never a
// serializer of its own — it re-canonicalizes nothing.
//
// STRICT DECODE: malformed bytes / an unknown `kind` / any `v` that is not exactly this
// version ⇒ a typed CLOSE-and-reconnect signal (`{ok:false}`) — never a partial event, never a
// guess at a future version. The caller maps a close signal to an explicit socket close; the
// establishment handshake (`reset` → `hello`) is the recovery.

import type { BusFrame } from "../../drivers";
import { readFrame } from "../../invalidation";

/**
 * The wire envelope version. A message declaring any other `v` is CLOSED, never interpreted.
 *
 * @testExport exported solely so tests assert the single source of truth; not public API, no
 * production importer — the conformance wire suite pins the version byte the codec stamps/accepts.
 */
export const WIRE_VERSION = 1;

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
export const CLOSE_DELIVERY_DOUBT = 4001;
export const CLOSE_SCOPE_MISMATCH = 4002;
export const CLOSE_PROTOCOL = 4003;

/**
 * One decoded wire event. The `hello` carries a `regId` ECHO the CLIENT asserts
 * against its configured scope BEFORE mapping to the kernel `BusEvent` (which has no `regId`) — a
 * mismatch closes the socket loud, dispatching nothing. `gap` stays transport-optional in the
 * union (doBus never emits it); a redisBus that does emit it uses the SAME codec. Every
 * other kind maps 1:1 to a `BusEvent`.
 */
export type WireEvent =
  | { readonly kind: "reset" }
  | { readonly kind: "hello"; readonly head: number; readonly regId: string }
  | { readonly kind: "gap"; readonly head: number }
  | { readonly kind: "frame"; readonly frame: BusFrame };

/**
 * A strict-decode outcome: a decoded event, OR a CLOSE-and-reconnect signal carrying a
 * reason. The caller never dispatches a partial event — an `{ok:false}` is a socket close.
 */
export type DecodeResult =
  | { readonly ok: true; readonly event: WireEvent }
  | { readonly ok: false; readonly reason: string };

/** Narrow an `unknown` to an index-readable record, or `undefined`. Every field is `typeof`-checked
 * at the call site below before use, so the record view is sound. */
function asRecord(value: unknown): Record<string, unknown> | undefined {
  if (typeof value !== "object" || value === null) {
    return undefined;
  }
  // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- narrowing a confirmed non-null object to an index-readable record; each field is typeof-checked at the decode call sites below.
  return value as Record<string, unknown>;
}

/**
 * Encode one wire event to a single WS-message string. The `frame` payload is
 * `JSON.stringify(frame)` — the canonical bytes, carried UNCHANGED as a nested JSON string, so
 * a frame survives the wire byte-identical (the canonical-bytes teeth pin it). Keys are emitted in
 * a fixed order so the envelope bytes are themselves deterministic.
 */
export function encode(event: WireEvent): string {
  switch (event.kind) {
    case "reset": {
      return JSON.stringify({ kind: "reset", v: WIRE_VERSION });
    }
    case "hello": {
      return JSON.stringify({
        head: event.head,
        kind: "hello",
        regId: event.regId,
        v: WIRE_VERSION,
      });
    }
    case "gap": {
      return JSON.stringify({ head: event.head, kind: "gap", v: WIRE_VERSION });
    }
    case "frame": {
      // The canonical serialization, UNCHANGED — the exact bytes the flush executor persists.
      return JSON.stringify({
        frame: JSON.stringify(event.frame),
        kind: "frame",
        v: WIRE_VERSION,
      });
    }
    default: {
      const unreachable: never = event;
      throw new Error(`unknown wire event: ${String(unreachable)}`);
    }
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
export function decode(bytes: string): DecodeResult {
  let parsed: unknown;
  try {
    parsed = JSON.parse(bytes);
  } catch {
    return { ok: false, reason: "malformed json" };
  }
  const env = asRecord(parsed);
  if (!env) {
    return { ok: false, reason: "envelope is not an object" };
  }
  if (env.v !== WIRE_VERSION) {
    // Never guess a future (or past) version — close and let the handshake recover.
    return { ok: false, reason: `unsupported wire version ${String(env.v)}` };
  }
  switch (env.kind) {
    case "reset": {
      return { event: { kind: "reset" }, ok: true };
    }
    case "hello": {
      if (typeof env.head !== "number" || typeof env.regId !== "string") {
        return { ok: false, reason: "malformed hello" };
      }
      return {
        event: { head: env.head, kind: "hello", regId: env.regId },
        ok: true,
      };
    }
    case "gap": {
      if (typeof env.head !== "number") {
        return { ok: false, reason: "malformed gap" };
      }
      return { event: { head: env.head, kind: "gap" }, ok: true };
    }
    case "frame": {
      if (typeof env.frame !== "string") {
        return { ok: false, reason: "malformed frame payload" };
      }
      // Reuse the reader's frame parser: a corrupt change holes the whole frame ⇒ close (never a
      // partial apply). One parser for the durable mirror and the live channel.
      const frame = readFrame(env.frame);
      if (!frame) {
        return { ok: false, reason: "frame failed F12 parse" };
      }
      return { event: { frame, kind: "frame" }, ok: true };
    }
    default: {
      return { ok: false, reason: `unknown wire kind ${String(env.kind)}` };
    }
  }
}
