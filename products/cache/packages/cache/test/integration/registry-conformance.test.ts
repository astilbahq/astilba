// Test A — the conformance dividend. The EXACT Registry conformance contract that
// the v0.1 FakeRegistry passes (test/conformance/registry.test.ts) is run here, UNMODIFIED,
// against the REAL Coordinator Durable Object on workerd. This discharges the delegated
// under-invalidation debt: the Registry contract now adjudicates the real DO, not just the
// fake. The real driver provides no fake-only `controls`, so the control-gated checks
// (round-trip lag) self-skip; every driver-observable registry property is asserted through
// the public `check`/`expire`/`delete`/`registerRetention` surface. Lane law:
// integration is timing-tolerant, but this contract is deterministic — no timing here.

import { runRegistryContract } from "../conformance/runner/registry";
import { makeDoRegistry } from "./registry-harness";

runRegistryContract(makeDoRegistry, {
  label: "doRegistry (real Coordinator Durable Object)",
});
