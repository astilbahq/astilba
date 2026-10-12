// Integration-lane factory: a `StoreHarness` backed by the REAL `cloudflareKV` driver
// over the workerd `REGISTRY_KV` binding, for driving the UNMODIFIED Store conformance
// contract on real KV (C2-a). Mirrors `registry-harness.ts` (the C1-c conformance
// dividend), but for the Store contract instead of the Registry contract.
//
// A real KV driver exposes NO fake-only controls (no `advance`, no `forceThrottle`), so
// the control-gated checks self-skip exactly as C0-a designed: TTL expiry (no `advance`),
// throttle classification (no `forceThrottle` — Miniflare emits no real 429, D2), and the
// oversize check (no `maxValueBytes` supplied — the 25 MiB ceiling is proven
// deterministically in the conformance lane, D13/D2). What runs for real here is the
// byte-stable round-trip and the same-view read-your-own-write against actual KV.

import { env } from "cloudflare:workers";

import { cloudflareKV } from "../../src/adapters/cloudflare/kv";
import type { StoreHarness } from "../conformance/runner/store";

/**
 * A `cloudflareKV`-backed `Store` harness over the workerd KV binding (no controls). The
 * contract drives distinct fixed keys per check, so the single shared namespace needs no
 * per-`make` isolation (unlike the DO registry, whose per-check state must be fresh).
 */
export function makeCloudflareKvStore(): StoreHarness {
  return { store: cloudflareKV(env.REGISTRY_KV) };
}
