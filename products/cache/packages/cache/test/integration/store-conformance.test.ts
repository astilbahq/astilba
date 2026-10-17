// Test B — the Store conformance dividend. The EXACT Store conformance contract
// the v0.1 FakeStore passes (test/conformance/store.test.ts) run here, UNMODIFIED,
// against the REAL `cloudflareKV` driver over the workerd `REGISTRY_KV` binding. This
// adjudicates the real KV driver, not just the fake. The real driver provides no
// fake-only controls, so the control-gated checks (TTL expiry, throttle, oversize)
// self-skip; the byte-stable round-trip + same-view read-your-own-write are asserted
// against real KV. `ttlFloorSeconds: 60` documents KV's 60s write-side floor;
// `maxValueBytes` is omitted on purpose (a 25 MiB allocation is expensive and the
// oversize path is already covered deterministically in the conformance lane). Lane law
//: integration is timing-tolerant, but this contract is deterministic — no
// timing here (Miniflare KV is strongly consistent, so the round-trip needs no retry).

import { runStoreContract } from "../conformance/runner/store";
import { makeCloudflareKvStore } from "./store-harness";

runStoreContract(makeCloudflareKvStore, {
  label: "cloudflareKV (real KV namespace binding)",
  ttlFloorSeconds: 60,
});
