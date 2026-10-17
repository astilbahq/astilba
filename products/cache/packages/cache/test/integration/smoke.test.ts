// integration-lane smoke test. Proves the fourth (integration) lane actually
// runs on real workerd via @cloudflare/vitest-pool-workers: the test worker env
// loads, the Durable Object binding resolves to a namespace, and the placeholder
// Coordinator shell instantiates inside the isolate. NO registry semantics are
// asserted here — that is the conformance lane's job. Lane law: this lane is timing-tolerant.

import { runInDurableObject } from "cloudflare:test";
import { env } from "cloudflare:workers";
import { expect, it } from "vitest";

import {
  Coordinator,
  FLUSH_WINDOW_MS,
  KV_MIN_SAME_KEY_WRITE_INTERVAL_MS,
} from "../../src/adapters/cloudflare/coordinator";

it("loads the worker env and binds the Coordinator Durable Object namespace", () => {
  expect(env.COORDINATOR).toBeDefined();
});

it("floors the flush window at the KV same-key write ceiling — a PLATFORM constraint, not a tuning knob (§4.2, D17)", () => {
  // KV allows at most one write per second to a key; a second sub-second write 429s. The flush
  // window is DERIVED as max(tuning, floor), so it is ≥ 1s by construction — even if the tuning
  // term is lowered, two consecutive flush deadlines stay ≥ 1s apart (one `__state` bless/window).
  expect(KV_MIN_SAME_KEY_WRITE_INTERVAL_MS).toBe(1000);
  expect(FLUSH_WINDOW_MS).toBeGreaterThanOrEqual(KV_MIN_SAME_KEY_WRITE_INTERVAL_MS);
});

it("resolves a Coordinator stub that instantiates the placeholder shell in workerd", async () => {
  const id = env.COORDINATOR.idFromName("smoke");
  const stub = env.COORDINATOR.get(id);

  await runInDurableObject(stub, (instance) => {
    expect(instance).toBeInstanceOf(Coordinator);
  });
});
