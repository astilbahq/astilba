// Registry conformance — the v0.1 FakeRegistry passes the Registry contract.
// Non-sacred (CLAUDE.md rule 11); the real doRegistry client passes the identical
// block in a later cluster.

import { makeFakeRegistryHarness } from "./fixtures/reference";
import { runRegistryContract } from "./runner/registry";

runRegistryContract(makeFakeRegistryHarness(), {
  label: "FakeRegistry (v0.1 reference)",
});
