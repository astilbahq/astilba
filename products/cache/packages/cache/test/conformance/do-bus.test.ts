/* References: §4.2. */

import { makeDoBusHarness } from "./fixtures/do-bus";
import { runBusContract } from "./runner/bus";

runBusContract(makeDoBusHarness(), {
  label: "doBus (in-memory pair — real codec/server/client)",
});
