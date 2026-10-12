import assert from "node:assert/strict";

import type { Codec } from "@astilba/cache";
import { describe, it } from "vitest";

// Codec contract (§8, E8). A pluggable value codec must round-trip byte-stable within
// a codec version (INV-8): encode is deterministic, decode reproduces the value, and
// re-encoding a decoded value yields identical bytes. It must expose a stable,
// non-empty `codecId` — the field E8's envelope gate compares BEFORE decode so a
// mismatch fails closed to a miss (INV-27; the compare-before-decode gate itself is
// kernel logic, exercised in sacred). A malformed body must throw so the read path
// can miss rather than mis-serve. The library's default safe-JSON codec is the
// reference; a real devalue/superjson-class codec passes the same block later.

const codecValues: readonly unknown[] = [
  { a: 1, b: [2, 3], c: "x" },
  "plain-string",
  42,
  true,
  { nested: { deep: [1, 2, 3] } },
];

const checkRoundTrip = async (make: () => Codec): Promise<void> => {
  const codec = make();
  for (const value of codecValues) {
    const bytes = codec.encode(value);
    assert.strictEqual(
      codec.encode(value),
      bytes,
      "encode must be deterministic (byte-stable within a version)"
    );
    assert.deepStrictEqual(
      codec.decode(bytes),
      value,
      "decode must reproduce the encoded value"
    );
    assert.strictEqual(
      codec.encode(codec.decode(bytes)),
      bytes,
      "re-encoding a decoded value must yield identical bytes"
    );
  }
};

const checkIdStable = async (make: () => Codec): Promise<void> => {
  const codec = make();
  assert.ok(
    typeof codec.id === "string" && codec.id.length > 0,
    "a codec must expose a non-empty codecId (the E8 gate field)"
  );
  assert.strictEqual(
    codec.id,
    codec.id,
    "the codecId must be stable across reads"
  );
};

const checkDecodeFailsClosed = async (make: () => Codec): Promise<void> => {
  const codec = make();
  assert.throws(
    () => codec.decode("{ this is not valid codec output"),
    "decode of a malformed body must throw so the read path can fail closed (INV-8)"
  );
};

type CodecCheck = (make: () => Codec) => Promise<void>;

/** The individually-callable Codec checks (each throws on violation). */
export const codecChecks: {
  readonly decodeFailsClosed: CodecCheck;
  readonly idStable: CodecCheck;
  readonly roundTrip: CodecCheck;
} = {
  decodeFailsClosed: checkDecodeFailsClosed,
  idStable: checkIdStable,
  roundTrip: checkRoundTrip,
};

export interface CodecContractOptions {
  readonly label?: string;
}

/** Register the Codec conformance suite for a provided implementation factory. */
export function runCodecContract(
  make: () => Codec,
  opts: CodecContractOptions = {}
): void {
  describe(`${opts.label ?? "Codec"} — conformance contract`, () => {
    it("round-trips byte-stable within a codec version (§8, INV-8)", async () => {
      await checkRoundTrip(make);
    });
    it("exposes a stable non-empty codecId the envelope gate compares before decode (E8, INV-27)", async () => {
      await checkIdStable(make);
    });
    it("fails closed on a malformed body so the read path can miss, never mis-serve (§8, INV-8)", async () => {
      await checkDecodeFailsClosed(make);
    });
  });
}
