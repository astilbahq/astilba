// @astilba/cache/cloudflare — the PUBLIC Cloudflare adapter surface (phase 2).
//
// The `./cloudflare` subpath barrel: the `createWorkersCache` factory + the driver factories
// (`cloudflareKV`, `doRegistry`, `doBus`, and `redialingDoBus`, the reconnecting bus the
// factory itself wires) + the `Coordinator` Durable Object class a Worker registers, plus the types a
// caller needs to compose the raw `createCache` path (`Dial`, `DoBusOptions`, `DoBusCloseInfo`,
// `RedialOptions`, `RedialingBusHandle`, `StubSource`, `WorkersCacheConfig`) and the
// `InvalidRegistryNameError` a boot refusal throws (so a caller can `instanceof` it — R2).
// Deep internals — the wire codec (`wire.ts`), the bus server (`bus-server.ts`, `establish`/
// `fanOut`), the DO machine, and `readFrame` — are deliberately NOT re-exported: they stay
// unexported deep paths. `Coordinator` imports the workerd runtime (`cloudflare:workers`), so this
// barrel resolves only inside the Workers isolate.
//
// `Scheduler` is GONE: the redial ladder is driven by `tick` from a request context, so the
// timer seam it named no longer exists. Its removal is a breaking change to an unreleased adapter,
// which is the only window in which it is free.

export {
  type Dial,
  doBus,
  type DoBusCloseInfo,
  type DoBusOptions,
} from "./bus-client";
export { Coordinator } from "./coordinator";
export { doRegistry, type StubSource } from "./do-registry";
export { cloudflareKV } from "./kv";
export {
  redialingDoBus,
  type RedialingBusHandle,
  type RedialOptions,
} from "./redial";
export { InvalidRegistryNameError } from "./reg-id";
export { createWorkersCache, type WorkersCacheConfig } from "./workers-cache";
