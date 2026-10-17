// Bus conformance — the v2 FakeBus (frames | gap | reset) passes the Bus contract
// (interface-v2 ruling). Non-sacred (CLAUDE.md rule 11); the real doBus
// / redisBus drivers pass the identical block in later clusters.

import { makeFakeBusHarness } from "./fixtures/reference";
import { runBusContract } from "./runner/bus";

runBusContract(makeFakeBusHarness(), {
  label: "FakeBus (v2 reference — frames | gap | reset)",
});
