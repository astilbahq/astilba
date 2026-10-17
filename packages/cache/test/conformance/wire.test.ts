import assert from "node:assert/strict";

import type { BusFrame } from "@astilba/cache";
import { RegistryMachine } from "@astilba/cache/registry";
import { describe, it } from "vitest";

import type { WireEvent } from "../../src/adapters/cloudflare/wire";
import {
  decode,
  encode,
  WIRE_VERSION,
} from "../../src/adapters/cloudflare/wire";

// doBus wire codec teeth. The ONE versioned envelope both the doBus
// server and the client speak. Three properties, each RUN here (deterministic lane — no fast-check):
//   • round-trip — encode∘decode is identity over the whole WireEvent union (reset|hello|gap|frame).
//   • canonical-bytes — a frame survives the wire BYTE-IDENTICAL: the shipped frame payload is the
// EXACT canonical serialization (`JSON.stringify(frame)`) the flush executor persists a `__delta`
//     with — the codec re-canonicalizes nothing.
//   • malformed-close — malformed bytes / unknown kind / a wrong version / a corrupt frame ⇒ a
//     CLOSE signal (`{ok:false}`), never a dispatched partial event.

/** A real machine-produced canonical frame: two tags, mixed axes, lex-sorted by the machine.
 * Using the machine (not a hand-built literal) proves the codec transports the SAME bytes the
 * durable path produces. */
function machineFrame(): BusFrame {
  const m = new RegistryMachine();
  m.expire(["z:tag"], 0);
  m.delete(["a:tag"], 0);
  const effects = m.flush(0);
  const persist = effects.find((e) => e.kind === "persist-batch");
  assert.ok(
    persist && persist.kind === "persist-batch",
    "expected a persist-batch effect"
  );
  return persist.frame;
}

/** A representative slice of the WireEvent union for the round-trip property. */
function wireEventCorpus(): readonly WireEvent[] {
  return [
    { kind: "reset" },
    { head: 0, kind: "hello", regId: "default" },
    { head: 42, kind: "hello", regId: "reader-x" },
    { head: 7, kind: "gap" },
    { frame: { changes: [], fromEpoch: 0, toEpoch: 1 }, kind: "frame" },
    { frame: machineFrame(), kind: "frame" },
  ];
}

describe("doBus wire codec — round-trip, canonical bytes, malformed-close (§4.2, ruling ②)", () => {
  it("round-trips every WireEvent kind through encode∘decode (identity)", () => {
    for (const event of wireEventCorpus()) {
      const result = decode(encode(event));
      assert.ok(result.ok, `decode must accept a self-encoded ${event.kind}`);
      assert.deepStrictEqual(
        result.event,
        event,
        `encode∘decode must be identity for ${event.kind}`
      );
    }
  });

  it("encode is deterministic — the same event yields byte-identical bytes", () => {
    for (const event of wireEventCorpus()) {
      assert.strictEqual(
        encode(event),
        encode(event),
        `encode must be byte-stable for ${event.kind}`
      );
    }
  });

  it("ships a frame BYTE-IDENTICAL to its F12 canonical serialization (the durable-mirror bytes)", () => {
    const frame = machineFrame();
    // The exact bytes the flush executor persists a `__delta` with (`JSON.stringify(frame)`).
    const canonical = JSON.stringify(frame);
    // THE pin: the wire envelope embeds the canonical serialization UNCHANGED as the frame
    // payload — byte-identical to what the flush executor persists a `__delta` with. The wire
    // mangles nothing (asserted directly on the bytes, no re-parse).
    assert.strictEqual(
      encode({ frame, kind: "frame" }),
      JSON.stringify({ frame: canonical, kind: "frame", v: WIRE_VERSION }),
      "the wire envelope must embed the UNCHANGED F12 canonical bytes as the frame payload"
    );
    // And it round-trips back to a SEMANTICALLY-identical frame (the reader parses it through the
    // SAME `readFrame` as a `__delta`, then only APPLIES it — it never re-serializes a bus frame, so
    // `readFrame`'s reconstruction key order is invisible, exactly as on the KV resync path).
    const decoded = decode(encode({ frame, kind: "frame" }));
    assert.ok(
      decoded.ok && decoded.event.kind === "frame",
      "frame must decode"
    );
    assert.deepStrictEqual(
      decoded.event.frame,
      frame,
      "a decoded frame must carry the identical changes/range as the original"
    );
  });

  it("CLOSES on malformed bytes, an unknown kind, a wrong version, and a corrupt frame — never dispatches", () => {
    const closers: readonly string[] = [
      "{ not json",
      "42",
      "null",
      JSON.stringify("a bare string"),
      JSON.stringify({ kind: "reset" }), // missing version ⇒ close (never guess)
      JSON.stringify({ kind: "reset", v: WIRE_VERSION + 1 }), // higher version ⇒ close, never interpret
      JSON.stringify({ kind: "reset", v: WIRE_VERSION - 1 }), // lower version ⇒ close too
      JSON.stringify({ kind: "banana", v: WIRE_VERSION }), // unknown kind
      JSON.stringify({ kind: "hello", v: WIRE_VERSION }), // hello missing head/regId
      JSON.stringify({
        head: "nope",
        kind: "hello",
        regId: "x",
        v: WIRE_VERSION,
      }), // non-number head
      JSON.stringify({ head: 1, kind: "hello", v: WIRE_VERSION }), // hello missing regId echo
      JSON.stringify({ kind: "gap", v: WIRE_VERSION }), // gap missing head
      JSON.stringify({ frame: 123, kind: "frame", v: WIRE_VERSION }), // frame payload not a string
      JSON.stringify({ frame: "{ not json", kind: "frame", v: WIRE_VERSION }), // frame body unparseable
      // A frame whose change carries a PRESENT-but-non-number axis — corruption (readFrame rejects).
      JSON.stringify({
        frame: JSON.stringify({
          changes: [{ softEpoch: "x", tag: "t" }],
          fromEpoch: 0,
          toEpoch: 1,
        }),
        kind: "frame",
        v: WIRE_VERSION,
      }),
    ];
    for (const bytes of closers) {
      const result = decode(bytes);
      assert.strictEqual(
        result.ok,
        false,
        `decode must CLOSE (never dispatch) on: ${bytes.slice(0, 40)}`
      );
    }
  });

  it("ACCEPTS a valid message (non-vacuity — the closers above are discriminating, not blanket)", () => {
    const ok = decode(encode({ head: 3, kind: "hello", regId: "default" }));
    assert.ok(ok.ok && ok.event.kind === "hello" && ok.event.head === 3);
  });
});
