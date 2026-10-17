// Store conformance — the v0.1 FakeStore (and a CAS-capable reference) pass the
// Store contract. Non-sacred (CLAUDE.md rule 11); the real cloudflareKV / redis
// drivers pass the identical block in later clusters.

import {
  makeCasStoreHarness,
  makeFakeStoreHarness,
  makeMemoryStoreHarness,
  referenceCodec,
} from "./fixtures/reference";
import { runStoreContract } from "./runner/store";

// A stand-in value ceiling (bytes) so the oversize contract runs deterministically
// without allocating KV's real 25 MiB. The reference's `forceThrottle` control
// (always present) likewise arms the throttle-classification check.
const FAKE_MAX_VALUE_BYTES = 1024;

runStoreContract(makeFakeStoreHarness(0, FAKE_MAX_VALUE_BYTES), {
  codec: referenceCodec,
  label: "FakeStore (v0.1 reference, no CAS)",
  maxValueBytes: FAKE_MAX_VALUE_BYTES,
});

runStoreContract(makeCasStoreHarness(), {
  capabilities: { cas: true },
  codec: referenceCodec,
  label: "reference CAS store (capabilities.cas)",
});

// The kernel memory() L1 store as a third subject. A clock-injected
// memory runs the WHOLE contract: round-trip, RCW, read-hint, TTL-expiry (advance
// present), oversize (`too_large` + prior-survives), and the no-CAS branch. Throttle
// self-skips (L1 has no write-rate ceiling). Eviction/bounds — which the contract does
// not cover (FakeStore never evicts) — get their own teeth in test/unit/memory-store.
runStoreContract(makeMemoryStoreHarness(FAKE_MAX_VALUE_BYTES), {
  capabilities: { cas: false },
  codec: referenceCodec,
  label: "memory (L1)",
  maxValueBytes: FAKE_MAX_VALUE_BYTES,
});
