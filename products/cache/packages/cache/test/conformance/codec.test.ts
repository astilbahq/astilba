// Codec conformance — the library-shaped safe-JSON reference codec passes the Codec
// contract. Non-sacred (CLAUDE.md rule 11); a real devalue/superjson-class codec
// passes the identical block later.

import { referenceCodec } from "./fixtures/reference";
import { runCodecContract } from "./runner/codec";

runCodecContract(() => referenceCodec, {
  label: "reference safe-JSON codec",
});
