/* References: §4.2. */

// PLATFORM: Cloudflare runtime bindings are confined to this adapter.

import type { BusFrame } from "../../drivers";
import { readFrame } from "../../invalidation";

/** Version placed on serialized bus messages.
 * @testExport
 */
export const WIRE_VERSION = 1;

/* References: §4.2, E19. */

export const CLOSE_DELIVERY_DOUBT = 4001;
export const CLOSE_SCOPE_MISMATCH = 4002;
export const CLOSE_PROTOCOL = 4003;

/* References: E19. */

export type WireEvent =
  | { readonly kind: "reset" }
  | { readonly kind: "hello"; readonly head: number; readonly regId: string }
  | { readonly kind: "gap"; readonly head: number }
  | { readonly kind: "frame"; readonly frame: BusFrame };

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
      // The F12 canonical serialization, UNCHANGED — the exact bytes the flush executor persists.
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
      // Reuse the reader's F12 parser: a corrupt change holes the whole frame ⇒ close (never a
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
