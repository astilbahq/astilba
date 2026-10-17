import type { Codec } from "./drivers";

// The built-in default codec. `world()` wires no explicit codec, so the core
// falls back to this safe-mode JSON codec to store/read values. A real deployment
// swaps in devalue safe-mode / a superjson-class codec; the kernel only needs a
// deterministic, byte-stable round-trip with an identity (`codecId`) so the
// codecId-mismatch fail-closed path (INV-27) has something to compare against.
//
// "Safe mode" here means the decode is a plain structured JSON parse with no
// prototype/`__proto__` reachability — it never revives functions, dates, or
// class instances, matching devalue's safe posture. Anything the encoder cannot
// represent (a `BigInt`, a function) surfaces at encode time as it would with any
// JSON codec, rather than silently corrupting durable bytes.

const SAFE_CODEC_ID = "safe/1";

/** The library's default safe-mode codec. Deterministic JSON round-trip. */
export const safeCodec: Codec = {
  decode(value: string): unknown {
    // A structured parse; a malformed body throws and the read path fails closed
    // to a miss (INV-8), never a mis-typed serve.
    return JSON.parse(value) as unknown;
  },
  encode(value: unknown): string {
    return JSON.stringify(value);
  },
  id: SAFE_CODEC_ID,
};
