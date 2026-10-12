/* References: §4.2, E4. */

import { makeFakeBusHarness } from "./fixtures/reference";
import { runBusContract } from "./runner/bus";

runBusContract(makeFakeBusHarness(), {
  label: "FakeBus (v2 reference — frames | gap | reset)",
});
